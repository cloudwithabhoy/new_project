# Deployment Architecture — running the platform on EKS

The **target-state infrastructure blueprint**: how the 12 microservices + datastores + event bus get
deployed, meshed, scaled, observed, and (eventually) spread across two AWS regions — all by
**hand-written `kubectl apply`** (no Helm, no GitOps), per the locked decisions in
[`EXECUTION_PLAN.md`](./EXECUTION_PLAN.md).

- **What the app *is*** → [`ARCHITECTURE.md`](./ARCHITECTURE.md)
- **API / env wiring** → [`services/api-details.md`](./services/api-details.md)
- **Phase-by-phase build order** → [`EXECUTION_PLAN.md`](./EXECUTION_PLAN.md)
- **This doc** = the deployment *shape* you build toward, and what each layer looks like in-cluster.

> **Build it up, don't build it all.** This is the *final* picture (multi-region, active-active).
> You reach it in stages: single cluster (Phases 0–7) → second cluster/mesh (8) → second region (9).
> Read each section as "here's the end state + the order you get there."

---

## 0. Locked decisions that shape everything

| Decision | Value | Deployment consequence |
|---|---|---|
| Delivery | **Manual `kubectl apply`** | Raw YAML in `deploy/`, applied by hand. No Argo/Flux reconciler. |
| Topology | **Multi-region + multi-cluster**, active-active | ≥2 EKS clusters, Istio multi-primary, Route 53 failover, cross-region data replication. |
| Templating | **No Helm / no Kustomize required** | Per-cluster diffs are hand-maintained copies under `deploy/overlays/`. |
| Scope in | Reliability+chaos, Observability+SLOs | HPA/VPA/KEDA/Karpenter/PDB/Velero/Chaos-Mesh + Prom/Thanos/Grafana/Loki/Tempo. |
| Scope deferred | Security/supply-chain, data-operators | StatefulSet datastores (not operators); security track slots in later. |

---

## 1. Repo → cluster: where manifests live

```
deploy/                         # ← you own all of this, applied by hand
├── base/                       # per-microservice manifests (Deployment, Service, ConfigMap, Secret)
│   ├── catalog/  auth/  user/  cart/  order/  payment/ ...
├── overlays/                   # per-cluster / per-region copies that differ (image tag, replicas, region label)
│   ├── us-east-1/   us-west-2/
├── infra/                      # datastores: postgres-*, redis, elasticsearch, kafka (StatefulSets + PVCs)
├── istio/                      # Gateway, VirtualService, DestinationRule, PeerAuthentication, east-west gw
├── observability/              # prometheus, thanos, grafana, alertmanager, loki, tempo, otel-collector
├── reliability/                # hpa, vpa, keda ScaledObjects, pdb, karpenter NodePools, velero schedules
└── chaos/                      # chaos-mesh experiments

infra-eks/                      # eksctl/terraform per region (cluster, node groups, IRSA, addons)
├── region-primary/  region-secondary/  global/   # global = route53, ecr replication
.ci/                            # Jenkinsfile (build + push to ECR only)
```

Image flow (CI is build/push only — **deploy stays manual**):
```
git push → Jenkins: lint → test → docker build → push to ECR (tag = git SHA)
                                                      │
you, by hand:  kubectl set image deploy/<svc> <svc>=…/<svc>:<sha>   (or apply the updated Deployment)
```

---

## 2. One cluster, anatomically

What lives inside a single EKS cluster (this is the unit you replicate per region):

```
┌──────────────────────────── EKS cluster (one region) ─────────────────────────────┐
│ control plane (AWS-managed)            Karpenter-provisioned spot nodes            │
│                                                                                     │
│  ┌── namespace: istio-system ───────────────────────────────────────────────────┐ │
│  │  istiod · ingress gateway (north-south) · east-west gateway (cross-cluster)   │ │
│  └──────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                     │
│  ┌── namespace: shop  (sidecar-injected) ───────────────────────────────────────┐ │
│  │  Deployments (each: pod = app + Envoy sidecar):                               │ │
│  │   frontend  api-gateway  auth  user  catalog  search  cart                    │ │
│  │   order  payment  inventory  notification  recommendation                     │ │
│  │  Each has: Service (ClusterIP) · ConfigMap · Secret · HPA/KEDA · PDB          │ │
│  │  thumbnail-job: KEDA ScaledObject (0→N on Kafka lag)                          │ │
│  └──────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                     │
│  ┌── namespace: data (StatefulSets + PVCs on EBS gp3) ───────────────────────────┐ │
│  │  postgres-auth  postgres-user  postgres-catalog  postgres-order               │ │
│  │  postgres-payment  postgres-inventory  postgres-recommendation                │ │
│  │  redis (cart)   elasticsearch (search)   kafka (KRaft, event bus)             │ │
│  └──────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                     │
│  ┌── namespace: observability ──┐  ┌── namespace: chaos ──┐  ┌── velero ──┐        │
│  │ prometheus+thanos sidecar    │  │ chaos-mesh           │  │ backups    │        │
│  │ grafana alertmanager loki    │  └──────────────────────┘  └────────────┘        │
│  │ tempo otel-collector         │                                                   │
│  └──────────────────────────────┘                                                   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

**Namespace design (isolation + quotas):**
- `shop` — all 12 app microservices (mesh sidecar injection ON). `ResourceQuota` + `LimitRange`.
- `data` — stateful workloads, kept separate so app rollouts never touch datastores.
- `istio-system`, `observability`, `chaos`, `velero` — platform.
- `NetworkPolicy`: **default-deny** in `shop` + `data`, then open exactly the §3-ARCHITECTURE edges.

---

## 3. The standard per-microservice deployment bundle

Every app microservice is the **same shape** — learn it once, repeat 12×:

```
microservice "X"  (e.g. order)
├── Deployment            image …/X:<sha>; envFrom ConfigMap + Secret;
│                         livenessProbe /healthz; readinessProbe /readyz;
│                         requests/limits (from README hints); terminationGracePeriodSeconds;
│                         topologySpreadConstraints + podAntiAffinity (spread across AZs/nodes)
├── Service (ClusterIP)   port from api-details §2 (e.g. 8086)
├── ConfigMap             non-secret env: PORT, LOG_LEVEL, *_URL, tunables
├── Secret                DB_PASSWORD (+ auth also JWT_PRIVATE_KEY_PEM)
├── ServiceAccount        (+ IRSA later, for AWS access)
├── HPA  or  KEDA          catalog/search → HPA(CPU/RPS); consumers/thumbnail → KEDA(Kafka lag)
├── PodDisruptionBudget   minAvailable for critical microservices
└── Istio: VirtualService + DestinationRule (routing, retries, timeouts, outlier detection)
```

**Datastores** differ — they're StatefulSets:
```
postgres-X   StatefulSet(replicas=1, later +replica) · headless Service · PVC(EBS gp3) · Secret(creds)
redis        StatefulSet/Deployment · PVC · Service
elasticsearch StatefulSet · PVC · Service
kafka        StatefulSet(KRaft, 3 brokers) · headless Service · PVCs
```

---

## 4. North–south traffic (how users get in)

```
Internet
  │  DNS: shop.example.com  (Route 53)
  ▼
AWS ALB / NLB  ── created by ── AWS Load Balancer Controller
  │
  ▼
Istio Ingress Gateway (istio-system)         ← TLS terminates here (cert-manager later)
  │  Gateway + VirtualService route by host/path
  ├── /            → frontend       (:3000)
  └── /api/*       → api-gateway    (:8080)  → (mesh) → all backends
```
- **ExternalDNS** manages the Route 53 record from the Gateway/Service annotation.
- Phase 0–2 you can start with a plain **ALB Ingress**; Phase 3 you switch the edge to the **Istio
  Gateway** (so mTLS + traffic management + tracing cover the edge too).

---

## 5. East–west traffic (service-to-service, inside the mesh)

- Every `shop` pod runs an **Envoy sidecar**; all service-to-service calls go pod→Envoy→Envoy→pod.
- **mTLS STRICT** (`PeerAuthentication`) — identities are SPIFFE SVIDs, certs rotated by istiod.
- **`DestinationRule`** per microservice: connection pools, **outlier detection** (eject unhealthy
  endpoints = circuit breaking), subsets for canary.
- **`VirtualService`** per microservice: timeouts, retries, and **traffic splits** (e.g. `catalog` v1/v2
  90/10 canary; `recommendation` A/B via the `VARIANT`-labelled subsets).
- **`RequestAuthentication`** points at `auth`'s JWKS (`…/.well-known/jwks.json`) +
  **`AuthorizationPolicy`** requires a valid principal to reach `order`/`cart` — moving the JWT check
  the api-gateway does today into the mesh.

---

## 6. The event bus (Kafka) in-cluster

```
namespace data:  kafka StatefulSet (KRaft, 3 brokers, PVCs)  ──  Service kafka:9092
        ▲ produce                         ▼ consume (own group each)
     order ──order.created──►  inventory · notification · recommendation
   producer ──thumbnail.requests──►  thumbnail-job (KEDA scales 0→N on lag)
```
- Services reach it via `KAFKA_BROKERS=kafka.data.svc.cluster.local:9092` (ConfigMap).
- **KEDA** reads consumer-group **lag** as the scaling metric → scale-to-zero workers.
- Multi-region (§9): **MirrorMaker 2** mirrors topics across regions.

---

## 7. Scaling & node management

| Layer | Tool | Applied to | Signal |
|---|---|---|---|
| Pods (HTTP) | **HPA** | catalog, search, api-gateway | CPU, then Prometheus-adapter RPS (`http_requests_total`) |
| Pods (events) | **KEDA** | inventory/notification/recommendation, thumbnail-job | Kafka consumer lag |
| Pod right-sizing | **VPA** (recommend mode) | a few microservices | observed usage → tune requests |
| Nodes | **Karpenter** | whole cluster | pending pods → provision spot nodes; consolidate when idle |
| Disruption | **PodDisruptionBudget** | critical microservices | protect minAvailable during drains/upgrades |

Pods use `topologySpreadConstraints` (across AZs) + `podAntiAffinity` so a node/AZ loss doesn't take
a whole microservice down.

---

## 8. Observability stack (per cluster, federated globally)

```
app pods (/metrics, JSON logs, trace headers) + Envoy sidecars
   │ scrape            │ logs                │ traces
   ▼                   ▼                     ▼
Prometheus ──► Thanos sidecar       Loki ◄─ promtail/alloy     Tempo ◄─ OTel Collector
   │  (per cluster, 15d local)         (logs, by trace_id)        (spans, from Envoy + apps)
   ▼
Grafana (dashboards: RED per microservice, USE per node, Istio mesh, SLO/error-budget)
Alertmanager (burn-rate alerts → notification channel)
```
- **SLOs** defined on the RED metrics (e.g. checkout availability 99.5%, p95 latency) → **multi-window
  burn-rate alerts**.
- **Thanos** gives one **global query view** across both regions' Prometheis (dedup + long-term S3),
  so a single Grafana shows the whole estate (§9).
- **Correlation:** `trace_id` in logs links Loki ↔ Tempo; metric exemplars link Prometheus ↔ Tempo.

---

## 9. Multi-cluster & multi-region (the end state)

Reached in Phases 8–9. Two regions, **one mesh**, active-active:

```
                        Route 53  (latency + failover routing, health-checked)
                       ┌──────────────────────────┬──────────────────────────┐
                       ▼                           ▼
        ┌──────────── REGION A (us-east-1) ──┐   ┌──── REGION B (us-west-2) ──────────┐
        │ EKS cluster A                       │   │ EKS cluster B                       │
        │  Istio (multi-primary, shared root  │◄─►│  Istio (multi-primary)              │
        │   CA)  ┌ east-west gateway ┐────────┼───┼──────┌ east-west gateway ┐          │
        │  shop / data / observability ns     │   │  shop / data / observability ns     │
        │  Prometheus + Thanos sidecar ───────┼───┼───► Thanos Querier (global) ◄───────┤
        │  Postgres (primary) ───stream repl──┼───┼──► Postgres (read replica / standby)│
        │  Kafka ───────── MirrorMaker 2 ─────┼───┼──► Kafka                            │
        └─────────────────────────────────────┘   └─────────────────────────────────────┘
                       ▲ ECR (region A) ──cross-region replication──► ECR (region B)
```

**How it behaves:**
- **Discovery:** istiod in each cluster learns the other's endpoints via the **east-west gateway**;
  a microservice can call `payment` and Envoy routes to a **local** endpoint first.
- **Failover:** **locality-aware load balancing** + outlier detection — if region A's `payment` is
  unhealthy, traffic spills to region B automatically. Kill all of A's `order` pods → requests serve
  from B.
- **Global DNS:** Route 53 health checks each region's ingress; on a region outage, DNS fails users
  over to the healthy region.
- **Data:** Postgres **streaming replication** (documented promote-on-failover); Kafka **MirrorMaker
  2**; Redis/ES replicated or per-region with documented consistency trade-offs. **This is the
  hardest part and the core DR learning.**
- **Observability:** a **Thanos Querier** dedups both clusters' metrics into one global view.

**DR runbook + game-day:** simulate full region-A loss → verify DNS + mesh + replicated data keep
checkout working from B → fail back → write it up. (Phase 9 exit check.)

---

## 10. Config, secrets & identity

| Concern | Now (core scope) | Later (security add-on) |
|---|---|---|
| Non-secret config | **ConfigMap** per microservice (ports, `*_URL`, tunables) | — |
| Secrets | **Kubernetes Secret** (DB passwords, `JWT_PRIVATE_KEY_PEM`) | **External Secrets Operator → AWS Secrets Manager**, or Vault |
| AWS access | **ServiceAccount + IRSA** (per microservice that needs AWS) | scoped IAM policies |
| TLS certs | self-signed / Istio internal | **cert-manager** for public certs |
| Policy | NetworkPolicy default-deny | **Kyverno/OPA**, Pod Security, image signing (Cosign), Trivy, Falco |

> The `JWT_PRIVATE_KEY_PEM` Secret is special: it **must be stable** (generate once, store in the
> Secret) or every `auth` restart invalidates live tokens and the published JWKS. See `auth/README.md`.

---

## 11. Backup, restore & resilience validation

- **Velero**: scheduled backups of `shop` + `data` namespaces and **PV snapshots**; rehearse a
  **restore drill** (Phase 5 exit check) and a **cross-region restore** (Phase 9).
- **Chaos Mesh** (Phase 7): pod-kill, network latency/partition, CPU/mem stress, combined with Istio
  fault injection on `payment` (`FAIL_MODE` or mesh faults). Run **game-days** with postmortems.
- Resilience proven, not assumed: chaos must show HPA/KEDA/PDB/outlier-detection keeping the SLO.

---

## 12. Deploy order (maps to EXECUTION_PLAN phases)

```
P0  cluster A + Karpenter + ALB controller + ECR                          (empty platform)
P1  catalog + postgres-catalog                                            (1 microservice, by hand)
P2  Jenkins build/push; + auth (+pg) + user (+pg)                         (CI + auth boundary)
P3  install Istio; + api-gateway, cart(+redis), order(+pg), payment(+pg)  (mesh + checkout chain)
P4  + inventory(+pg), notification, recommendation(+pg), search(+es),     (full fleet + async)
    frontend, kafka, thumbnail-job
P5  HPA/VPA/KEDA, Karpenter tuning, PDB, Velero                           (reliability)
P6  Prometheus/Thanos/Grafana/Loki/Tempo/OTel + SLOs + alerts            (observability)
P7  Chaos Mesh game-days                                                  (chaos)
P8  cluster B (same region), Istio multi-primary, east-west gateway       (multi-cluster mesh)
P9  region B, Route 53 failover, data replication, Thanos global, DR      (multi-region)
P10 hardening, cost review, runbook  (+ optional security/operators)      (production-shaped)
```

---

## 13. Production traffic, end to end (the full picture)

```
user ─DNS(Route53)─► ALB ─► Istio Ingress GW ─► api-gateway
   api-gateway ─(mTLS, mesh)─► order
       order ─► cart ─► catalog                     (sync, traced, locality-routed)
       order ─► payment                              (sync; outlier detection = circuit breaker)
       order ─► inventory                            (sync reserve)
       order ─► Kafka(order.created)                 (async)
                   ├─► inventory (commit)            (each its own consumer group,
                   ├─► notification                   may run in either region,
                   └─► recommendation                 scaled by KEDA on lag)
   if region A unhealthy ─► Route53 + mesh locality failover ─► region B serves it
   everything emits: RED metrics→Prom/Thanos, spans→Tempo, logs→Loki, all joined by trace_id
```

---

## 14. Cost guardrails (multi-region isn't free)

- **2+ EKS control planes** (~$0.10/hr each) run even at zero nodes — build single-cluster first,
  spin region B only for Phases 8–9, tear it down between sessions.
- Spot + Karpenter consolidation; scale node groups to **0** when idle (script it).
- Datastores in-cluster (not RDS/MSK) until you choose to upgrade.
- Watch **cross-region transfer** (replication + Thanos S3). Keep all manifests in Git so any region
  is one documented re-apply away — and a **teardown checklist** is part of the DR runbook.
