# Multi-Region Architecture — the final end-state

The complete deployed platform in its **final form**: multi-region active-active on AWS EKS, end to end
from **CI/CD to observability**. This is the *what it looks like when done* picture — the per-phase build
order is in [`requirement-execution-plan/`](./requirement-execution-plan/); the app itself is in
[`APPLICATION-ARCHITECTURE.md`](./APPLICATION-ARCHITECTURE.md); one region's anatomy is in
[`SINGLE-REGION-ARCHITECTURE.md`](./SINGLE-REGION-ARCHITECTURE.md).

---

## 0. The whole picture

```
   developers ─► git push ─► ┌──────────┐ ─► ┌────────────────────────────┐
                             │ Jenkins  │     │  ECR (Mumbai, primary)     │
                             └──────────┘     │  ecom-repo:<svc>-<sha>     │
                                              └─────────────┬──────────────┘
                                   ECR cross-region replication
                          ┌────────────────────────┴────────────────────────┐
                          ▼                         ▼                         ▼
                    ECR (Mumbai)             ECR (London)             ECR (Singapore)
                          └──────── you: kubectl apply <sha> per region (manual) ───────┘

                                       users worldwide
                                             │
                       ┌─────────────────────▼─────────────────────┐
                       │  GLOBAL ROUTER  ·  Route 53 geo/latency    │
                       │  + per-region health checks (failover)     │
                       └────────┬──────────────┬──────────────┬─────┘
                                ▼              ▼              ▼
   ┌──── MUMBAI · ap-south-1 ─────┐ ┌─ LONDON · eu-west-2 ─┐ ┌─ SINGAPORE · ap-southeast-1 ─┐
   │ ALB ► Istio GW ► api-gateway │ │ ALB ► Istio ► api-gw │ │ ALB ► Istio ► api-gw         │
   │   ► 12 services (+ sidecars) │ │   ► services         │ │   ► services                 │
   │ data: PRIMARY DB · Kafka     │ │ data: read replica · │ │ data: read replica ·         │
   │                              │ │   Kafka              │ │   Kafka                       │
   │ HPA/KEDA/Karpenter · PDB     │ │ (same)               │ │ (same)                        │
   │ Prometheus + Thanos sidecar  │ │ + Thanos sidecar     │ │ + Thanos sidecar              │
   └────────┬─────────────────────┘ └────────┬─────────────┘ └────────┬──────────────────────┘
            │     DB replication (WAL)  +  Kafka MirrorMaker 2          │
            └───────────────────────────┬──────────────────────────────┘
                                        ▼
                          ┌──────────────────────────────┐
                          │  Thanos Querier (global) ─► ONE Grafana   (global observability)
                          └──────────────────────────────┘
```

Each region is a **complete, autonomous stack**; they are joined only by the **global router** (front),
**data replication + Kafka mirroring** (behind), and the **global metrics view** (Thanos).

---

## 1. CI/CD & image distribution

```
   git push ─► Jenkins (one Pipeline per service)
               lint ─► test ─► docker build ─► push  ECR  ecom-repo:<svc>-<sha>   (immutable, no :latest)
                                                       │
                              ECR cross-region replication (registry rule)
                          ┌──────────────────────────┴──────────────────────────┐
                          ▼                          ▼                          ▼
                    each region's ECR  ──► nodes pull LOCALLY  ──► you: kubectl set image <sha>
```
- **Build once, deploy many** — one immutable `<svc>-<sha>` image is replicated to every region's ECR; the
  **same SHA** is deployed everywhere.
- **Deploy stays manual** (`kubectl`), rolled region-by-region (canary region first).

## 2. Global edge & routing

```
   user ─► Route 53 (latency/geo + health checks)  ─► nearest HEALTHY region's ALB
        ─► Istio Ingress Gateway (TLS terminate, mTLS inside) ─► api-gateway ─► services
   region unhealthy ─► health check drops it ─► users re-route to the next-nearest region
```
- Each region owns its **regional ALB**; the **global router** only selects the region.
- **Istio stays inside each region** (not stretched across regions).

## 3. Inside a region (the unit, replicated ×3)

```
 ┌─ EKS cluster (one region) ───────────────────────────────────────────────
 │  istio-system :  istiod · ingress gateway
 │  shop (mesh-injected) :  api-gateway · auth · user · catalog · search · cart
 │                          order · payment · inventory · notification · recommendation
 │                          thumbnail-job (KEDA 0→N)        each pod = app + Envoy sidecar
 │  data :  Postgres ×4 · Kafka     (StatefulSets, EBS gp3, multi-AZ)
 │  observability :  Prometheus(+Thanos sidecar) · Grafana · Loki · Tempo · Alertmanager
 │  nodes : Karpenter spot across AZ a/b/c · NetworkPolicy default-deny
 └───────────────────────────────────────────────────────────────────────────
```
Per-service objects: `Deployment · Service · ConfigMap · Secret · ServiceAccount(IRSA) · HPA/KEDA · PDB ·
VirtualService/DestinationRule`. Full anatomy → [`SINGLE-REGION-ARCHITECTURE.md`](./SINGLE-REGION-ARCHITECTURE.md).

## 4. Data — cross-region

```
   writes ─────► PRIMARY Postgres (Mumbai) ──WAL stream──► read replica (London)
   reads  ─────► local read replica          ─────────────► read replica (Singapore)
   every region: writes → the one primary · reads → local replica · promote replica on DR

   order ─► Kafka (per region) ◄──── MirrorMaker 2 ────► Kafka (other regions)
            consumers idempotent by order_id (mirrored duplicates are safe)
```
**Single-write-primary** (active-active reads, one write primary) · per-region Kafka mirrored by **MM2**.

## 5. Scaling & high availability

```
   pods  : HPA (CPU + RPS) · KEDA (Kafka lag, scale-to-zero)
   nodes : Karpenter (spot-first, consolidation)
   data  : read replicas (scale reads) · Multi-AZ standby (auto-failover)
   spread: topologySpreadConstraints + podAntiAffinity across AZ a/b/c · PodDisruptionBudgets
   ─► an AZ can fail with no downtime (in-region HA); a region failing is DR (§8)
```

## 6. Security & access (final state)

```
   in-mesh   : mTLS STRICT (PeerAuthentication) · RequestAuthentication + AuthorizationPolicy (JWT via JWKS)
   network   : NetworkPolicy default-deny → only the call-graph edges open · private EKS API endpoints
   workloads : ServiceAccount + IRSA (per-pod least-privilege AWS access; no static keys)
   secrets   : Kubernetes Secret → (add-on) External Secrets Operator / Vault
   people    : Azure AD/Okta ─► IAM Identity Center (permission sets) ─► EKS RBAC (namespace-scoped)
   dev access: AWS Client VPN + Transit Gateway ─► reach every region's private resources
```

## 7. Observability (global)

```
 ┌ Mumbai ┐  ┌ London ┐  ┌ Singapore ┐     each region scrapes LOCALLY
 │ Prom+   │  │ Prom+   │  │ Prom+      │
 │ Thanos  │  │ Thanos  │  │ Thanos     │
 │ sidecar │  │ sidecar │  │ sidecar    │
 └────┬────┘  └────┬────┘  └─────┬──────┘
      └────────────┼─────────────┘
                   ▼
        Thanos Querier (global, dedup) + Thanos Store ◄─► S3 (long-term)
                   ▼
            ONE Grafana   (dashboards span all regions + per-region; SLOs global + local)
   logs: per-region Loki ─► S3 backend (query across)   ·   traces: per-region Tempo (trace_id stitches)
```
Apps emit RED metrics + JSON logs with `trace_id` + W3C traces; SLOs + multi-window burn-rate alerts.

## 8. Disaster recovery

```
   region fails ─► Route 53 health check drops it ─► users auto-route to a healthy region (reads local there)
                ─► if the failed region held the WRITE PRIMARY ─► promote a read replica ─► new primary
                ─► fail back when the region returns (re-sync · demote · re-promote)
   model = ACTIVE-ACTIVE (low RTO via auto-reroute · RPO = replication lag) · Velero/snapshots = deeper safety net
```

---

## 9. Cost note
3 regions = **3 EKS control planes** + 3 node groups + cross-region data transfer (replication, MM2,
Thanos S3). Keep **Mumbai** always-on as primary; bring UK/Singapore up only when working multi-region.
All manifests live in Git so any region is one documented re-apply away.
