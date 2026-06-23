# Design Execution Plan — build the platform, step by step

The **single master plan**: the ordered, step-by-step build of the whole platform — from **CI/CD** to a
**fully-working single region** to the **multi-region, multi-use-case** end state. Each numbered phase is
a milestone you build, **in order**; every phase has detailed execution steps and connects to the
previous one. This is the *one* place that sequences the work.

> **Golden rule:** finish a region **completely** (Phases 01–08) before adding the next region, then add
> **one capability at a time** (Phases 09–22). Never two half-built things at once.

**Division of labour:** Claude wrote all microservice code, Dockerfiles, and API contracts (done). **You
(DevOps/SRE)** do all the infrastructure — every phase below — by hand with `kubectl` (no GitOps).

---

## The phases

### Single region — build it completely first
| # | Phase | Adds |
|---|---|---|
| [01](./01-cicd-with-jenkins.md) | **CI/CD with Jenkins** | every image built + pushed to ECR (`ecom-repo:<svc>-<sha>`) |
| [02](./02-one-eks-cluster.md) | **One EKS cluster** | an empty real cluster in `ap-south-1` (LB controller, Karpenter, namespaces) |
| [03](./03-datastores-and-first-deploy.md) | **Datastores + first deploy** | StatefulSet datastores + `catalog` live end-to-end |
| [04](./04-full-microservice-fleet.md) | **Full microservice fleet** | all 13 components + the Kafka async backbone |
| [05](./05-istio-service-mesh.md) | **Istio service mesh** | mTLS, Istio Gateway, traffic management, tracing |
| [06](./06-reliability-and-autoscaling.md) | **Reliability & autoscaling** | HPA/KEDA/VPA/Karpenter, PDB, Velero |
| [07](./07-observability-and-slos.md) | **Observability & SLOs** | Prometheus/Grafana/Loki/Tempo + SLOs + burn-rate alerts |
| [08](./08-chaos-engineering.md) | **Chaos engineering** | prove resilience; the region is now a proven unit |

### Multi region — replicate the region, then add capabilities
| # | Phase | Adds |
|---|---|---|
| [09](./09-add-second-region-and-global-routing.md) | **Add a second region + global routing** | UK region + a global LB sending users to the nearest region |
| [10](./10-cross-region-database-replication.md) | **Cross-region DB replication** | write→one primary, read→local replicas, auto-replication |
| [11](./11-session-management.md) | **Session management** | stateless JWT — users servable by any region |
| [12](./12-multi-region-kafka.md) | **Multi-region Kafka** | per-region Kafka + MirrorMaker 2 |
| [13](./13-scaling-for-peak.md) | **Scaling for peak (festive)** | read replicas, autoscaling, no single point of failure |
| [14](./14-multi-region-cicd-and-image-distribution.md) | **Multi-region CI/CD & images** | build once; ECR cross-region replication |
| [15](./15-deployment-strategy.md) | **Deployment strategy** | rolling / canary / blue-green |
| [16](./16-multi-region-release.md) | **Multi-region release** | region-by-region rollout, canary region, rollback |
| [17](./17-disaster-recovery.md) | **Disaster recovery** | region-failure DR model, failover + failback |
| [18](./18-developer-network-access.md) | **Developer network access** | VPN / Transit Gateway to every region |
| [19](./19-access-management.md) | **Access management** | SSO/AD groups → env-scoped RBAC + IAM |
| [20](./20-data-storage-and-cold-tiering.md) | **Data storage & cold tiering** | hot / warm / cold (S3 Glacier) |
| [21](./21-add-third-region.md) | **Add the third region** | Singapore — validate scale-out |
| [22](./22-global-observability.md) | **Global observability** | per-region Prometheus + Thanos global view |

Each phase doc: **Goal · Architecture (evolving end-to-end diagram, with `(NEW: …)` markers) ·
Prerequisites · Execution steps / What you build · Exit check · Interview one-liner.**

---

## Locked decisions (what shapes everything)

| Decision | Choice |
|---|---|
| **Delivery** | fully manual `kubectl apply` — no Argo CD / Flux (feel every primitive) |
| **CI/CD** | Jenkins builds + pushes to ECR; **deploy stays manual** |
| **Topology** | multi-region active-active — 3 regions, global geo-routing, **independent regional stacks** (Istio inside each region, not stretched) |
| **Data** | **single-write-primary** + cross-region read replicas (active-active reads; not multi-master writes) |
| **Datastores** | in-cluster **StatefulSets** (Postgres/Redis/ES/Kafka) — hand-wired, not operators |
| **Reliability & chaos** | in scope (HPA/VPA/KEDA/Karpenter/PDB/Velero/Chaos Mesh) |
| **Observability & SLOs** | in scope (Prometheus + Thanos, Grafana, Loki, Tempo, OTel) |
| **Security & supply chain** | deferred add-on (OPA/Kyverno, Trivy/Cosign, Vault/External Secrets, cert-manager) |

---

## Tech stack & components

**Stack:** Go + Python + Node services · Postgres / Redis / Elasticsearch / Kafka · Istio · AWS EKS ·
Amazon ECR · Karpenter · Jenkins · Prometheus/Thanos/Grafana/Loki/Tempo.

**13 components** (all code-complete): `api-gateway` `auth` `user` `catalog` `search` `cart` `order`
`payment` `inventory` `notification` `recommendation` `frontend` + `thumbnail-job` (worker).
Full detail: [`../APPLICATION-ARCHITECTURE.md`](../APPLICATION-ARCHITECTURE.md) ·
[`../services/api-details.md`](../services/api-details.md).

**Architecture references:** [`../SINGLE-REGION-ARCHITECTURE.md`](../SINGLE-REGION-ARCHITECTURE.md) ·
[`../DEPLOYMENT-ARCHITECTURE.md`](../DEPLOYMENT-ARCHITECTURE.md).
**Hands-on runbook (exact commands + gotchas):** [`../step-by-step-implementation/`](../step-by-step-implementation/).

---

## Cost control (EKS is not free)
- Stay **single-region (Phases 01–08)** for most of the build — one EKS control plane + a small spot
  node group. Use spot + Karpenter consolidation; scale node groups to **0** when idle.
- Multi-region (09+) means **3 EKS control planes** + cross-region data transfer — stand it up
  deliberately; keep Mumbai always-on and bring UK/Singapore up only when working multi-region.
- Keep all manifests in Git so any region is one documented re-apply away.

## Definition of done
- All 13 components running across **3 regions**, each in the Istio mesh with mTLS.
- Every Kubernetes primitive hand-written and `kubectl apply`-ed yourself.
- Global geo-routing + cross-region replication + a rehearsed DR game-day.
- Autoscaling + PDBs proven; correlated metrics/logs/traces with SLOs + burn-rate alerts (Thanos global).
- A working Jenkins CI pipeline (deploy stays manual).
