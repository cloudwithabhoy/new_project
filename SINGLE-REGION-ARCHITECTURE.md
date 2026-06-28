# Single-Region Architecture — one region, in depth

The anatomy of **one region (Mumbai / `ap-south-1`)** — the complete stack inside a single EKS cluster,
from the edge down to the datastores. This is the **unit you replicate**: get it rock-solid once, then
stamp it out per region (the multi-region end-state is [`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md)).

- **What the app is** → [`APPLICATION-ARCHITECTURE.md`](./APPLICATION-ARCHITECTURE.md)
- **API / env wiring** → [`services/api-details.md`](./services/api-details.md)
- **The ordered build plan** → [`requirement-execution-plan/`](./requirement-execution-plan/) (Phases 01–08 build this region)
- **The final multi-region end-state** → [`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md)

---

## 1. The region, end to end

```
                          Internet
                             │  Route 53 (DNS)  shop.<domain>
                             ▼
                    AWS ALB   (AWS Load Balancer Controller + ExternalDNS)
                             │
                             ▼
                ┌─ Istio Ingress Gateway ──┐   TLS terminates here · mTLS inside the mesh
                │   /       ─► frontend     │
                │   /api/*  ─► api-gateway  │
                └────────────┬─────────────┘
                             ▼  (mesh)
   ┌─ EKS cluster · ecom-cluster · ap-south-1  (nodes across AZ a / b / c) ──────────┐
   │   shop ns ─► 6 services (+ Envoy sidecars) ─► data ns ─► datastores             │
   │   observability scrapes every pod · Karpenter manages nodes                     │
   └─────────────────────────────────────────────────────────────────────────────────┘
   CI/CD (separate):  git push ─► Jenkins ─► ECR  ecom-repo:<svc>-<sha>  ──(you: kubectl)──► cluster
```
One EKS cluster, one region. CI/CD builds images into **ECR**; you deploy them **manually** with `kubectl`.

---

## 2. Cluster anatomy (namespaces)

```
 ┌─ EKS cluster: ecom-cluster · ap-south-1   (Karpenter spot nodes · AZ a/b/c) ─────────┐
 │ control plane: AWS-managed                                                           │
 │                                                                                      │
 │ istio-system  istiod · ingress gateway (north-south)                                 │
 ├──────────────────────────────────────────────────────────────────────────────────────┤
 │ shop          frontend · api-gateway · auth · catalog · order · inventory            │
 │               each: Deployment + Service + ConfigMap + Secret + HPA/KEDA + PDB       │
 ├──────────────────────────────────────────────────────────────────────────────────────┤
 │ data          postgres-auth · postgres-catalog · postgres-order · postgres-inventory │
 │               kafka (KRaft, single-broker event bus)                                 │
 ├──────────────────────────────────────────────────────────────────────────────────────┤
 │ platform      observability: prometheus · grafana · loki · tempo · otel              │
 │               chaos-mesh · velero (backups)                                          │
 └──────────────────────────────────────────────────────────────────────────────────────┘
```
- **`shop`** — apps, mesh-injected, with `ResourceQuota` + `LimitRange`.
- **`data`** — stateful workloads, kept separate so app rollouts never touch datastores.
- **`istio-system` / `observability` / `chaos` / `velero`** — platform.
- **`NetworkPolicy` default-deny** in `shop` + `data`, then open exactly the call-graph edges from
  [`APPLICATION-ARCHITECTURE.md`](./APPLICATION-ARCHITECTURE.md) §3b.

---

## 3. The standard per-service bundle (learn once, repeat 6×)

```
 service "X"  (e.g. order)
   ├─ Deployment          image ecom-repo:X-<sha> · envFrom ConfigMap+Secret
   │                      livenessProbe /healthz · readinessProbe /readyz · requests/limits
   │                      terminationGracePeriodSeconds · topologySpread + podAntiAffinity (AZs)
   ├─ Service (ClusterIP) port from api-details §2
   ├─ ConfigMap           non-secret env: PORT · LOG_LEVEL · *_URL · tunables
   ├─ Secret              DB_PASSWORD   (+ auth also JWT_PRIVATE_KEY_PEM)
   ├─ ServiceAccount      (+ IRSA for AWS access)
   ├─ HPA or KEDA         catalog → HPA (CPU/RPS) · inventory consumer → KEDA (Kafka lag)
   ├─ PodDisruptionBudget minAvailable for critical services
   └─ Istio               VirtualService + DestinationRule (routing · retries · timeouts · outlier detection)
```
**Datastores** are StatefulSets: `postgres-X` (PVC on EBS gp3 · headless Service · Secret) and
`kafka` (KRaft, single broker).

---

## 4. Traffic — north-south and east-west

```
 NORTH-SOUTH (users in):
   Internet ─► Route 53 ─► ALB ─► Istio Ingress Gateway ─► frontend / api-gateway
   (ExternalDNS writes the Route 53 record from the Gateway annotation)

 EAST-WEST (service-to-service, in the mesh):
   pod ─► [Envoy] ─────────────► [Envoy] ─► pod        every call is mTLS, traced, policy-checked
```
- **mTLS STRICT** (`PeerAuthentication`) — SPIFFE identities rotated by istiod.
- **`DestinationRule`** per service — connection pools + **outlier detection** (circuit breaking) + subsets for canary.
- **`VirtualService`** per service — timeouts, retries, **traffic splits** (catalog v1/v2 canary).
- **`RequestAuthentication`** (auth's JWKS) + **`AuthorizationPolicy`** move the JWT check from the
  api-gateway into the mesh.

## 5. Event bus (Kafka) in-cluster

```
   data ns:  kafka StatefulSet (KRaft, single broker)  ──  Service kafka:9092
      order ──order.created──►  inventory   (its own consumer group; KEDA can scale it on lag)
```
Services reach it via `KAFKA_BROKERS=kafka.data.svc.cluster.local:9092`. (Cross-region mirroring is a
multi-region concern — [`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md) §4.)

---

## 6. Scaling, HA & node management (within the region)

| Layer | Tool | Applied to | Signal |
|---|---|---|---|
| Pods (HTTP) | **HPA** | catalog, api-gateway | CPU, then Prometheus-adapter RPS |
| Pods (events) | **KEDA** | inventory (order.created consumer) | Kafka consumer lag |
| Pod right-sizing | **VPA** (recommend) | a few services | observed usage → tune requests |
| Nodes | **Karpenter** | whole cluster | pending pods → provision spot; consolidate when idle |
| Disruption | **PodDisruptionBudget** | critical services | protect minAvailable during drains/upgrades |

```
   nodes + pods spread across AZ a / b / c   (topologySpreadConstraints + podAntiAffinity)
   datastore: primary + replica in a DIFFERENT AZ
   ─► losing one AZ does NOT take a service down       (in-region HA)
```
This is **in-region resilience** (AZ-level) — distinct from cross-region **DR**
([`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md) §8).

---

## 7. Observability (per cluster)

```
   app pods (/metrics · JSON logs · trace headers)  +  Envoy sidecars
        │ scrape            │ logs                    │ traces
        ▼                   ▼                         ▼
   Prometheus            Loki ◄ promtail/alloy        Tempo ◄ OTel Collector
        │  (+ Thanos sidecar — global view in DEPLOYMENT §7)   (logs + traces joined by trace_id)
        ▼
   Grafana (RED per service · USE per node · Istio mesh · SLO/error-budget)
   Alertmanager (multi-window burn-rate alerts)
```
SLOs on RED metrics (e.g. checkout availability 99.5%, p95 latency).

---

## 8. Config, secrets & identity

| Concern | Now | Later (security add-on) |
|---|---|---|
| Non-secret config | ConfigMap per service | — |
| Secrets | Kubernetes Secret (DB passwords, JWT key) | External Secrets Operator → AWS Secrets Manager / Vault |
| AWS access | ServiceAccount + **IRSA** | scoped IAM policies |
| TLS | Istio internal / self-signed | cert-manager for public certs |

> `JWT_PRIVATE_KEY_PEM` must be a **stable** Secret (generate once) — otherwise every `auth` restart
> invalidates live tokens and the published JWKS. See `services/auth/README.md`.

## 9. Backup & resilience validation
- **Velero** — scheduled backups of `shop` + `data` + PV snapshots; rehearse a **restore drill**.
- **Chaos Mesh** — pod-kill, network latency/partition, CPU/mem stress + Istio fault injection on
  `order` / `inventory`. Game-days with postmortems; chaos must show HPA/KEDA/PDB/outlier-detection
  holding the SLO.

## 10. CI/CD → manual deploy

```
   git push ─► Jenkins (per-service job): lint ─► test ─► docker build ─► push  ecom-repo:<svc>-<sha>
   then YOU, by hand:  kubectl -n shop set image deploy/<svc> <svc>=…/ecom-repo:<svc>-<sha>
```
CI **builds + pushes only**; deploy stays manual `kubectl`. Full CI setup →
[`step-by-step-implementation/02-set-up-jenkins.md`](./step-by-step-implementation/02-set-up-jenkins.md).

---

## 11. Which phases build this region
[`requirement-execution-plan/`](./requirement-execution-plan/): **01** CI/CD → **02** EKS cluster → **03**
datastores + first service → **04** full fleet + Kafka → **05** Istio mesh → **06** reliability/scaling →
**07** observability/SLOs → **08** chaos. Once this region is **fully functional and proven**, you
replicate it per region → [`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md).
