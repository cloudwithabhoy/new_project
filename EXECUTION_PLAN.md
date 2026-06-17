# E-Commerce Microservices on EKS — Enterprise Execution Plan

A 12-microservice e-commerce platform built **from scratch** (raw Kubernetes manifests, **no Helm,
no GitOps**) and run on **AWS EKS** with an **Istio** service mesh — every object **hand-written and
`kubectl apply`-ed yourself**, building up to a **multi-region, multi-cluster** active-active estate.

This is the **"toughest"** track: not a toy. It is shaped like a real platform/SRE estate. You start
with **CI/CD**, stand up **one cluster in one region**, get the **datastores and microservices
running**, then add the mesh, autoscaling, observability, chaos, and finally multi-region — one
earned layer at a time, all by hand.

> ✅ **Development is done.** All 12 microservices + the `thumbnail-job` worker are code-complete and
> compile/parse clean (see §3). **This plan is now your DevOps execution track** — the remaining work
> is all infrastructure, in the order below.

> **Division of labor**
> - **Claude (me)** → wrote all microservice code, Dockerfiles, and the API contracts. Apps are
>   *observability-ready, resilient, mesh-friendly*. I adjust code when your platform needs it.
> - **You (DevOps/SRE)** → CI/CD, registry, **all** Kubernetes manifests, Istio, EKS clusters,
>   networking, autoscaling, chaos, observability, SLOs, backup/DR — deployed by hand with `kubectl`.

---

## 0. What "enterprise / toughest" means here (decisions locked)

| Decision | Choice | Why it's hard (good) |
|---|---|---|
| **Delivery** | **Fully manual `kubectl apply`** — no Argo CD / Flux | You feel every primitive; nothing hidden behind automation. You hand-roll progressive delivery with Istio. |
| **CI/CD** | **Jenkins** builds + pushes images to ECR; **deploy stays manual** | Automate the boring part (image build), keep the learning part (deploy) in your hands. |
| **Topology** | **Multi-region + multi-cluster**, active-active (end state) | Istio multi-primary mesh, cross-cluster discovery, global DNS failover, cross-region data replication. The hardest footprint — reached last. |
| **Reliability & chaos** | **In scope** | HPA + VPA + KEDA, PodDisruptionBudgets, Karpenter, Chaos Mesh/Litmus game-days, Velero backup/restore, DR drills, manual canary/blue-green via Istio. |
| **Observability & SLOs** | **In scope** | Prometheus + Thanos (global view), Grafana, Alertmanager, Loki logs, Tempo/Jaeger traces, OpenTelemetry, SLO/error-budget dashboards, alerting. |
| **Security & supply chain** | **Deferred (optional add-on)** | Not selected. Slots in cleanly later: OPA/Kyverno, Pod Security, Trivy/Cosign/SBOM, Falco, Vault + External Secrets, cert-manager, granular RBAC. **Strongly recommended before any "real" use.** |
| **Data layer** | **Hand-rolled StatefulSets** (not operators) | You wire Postgres replication, Kafka MirrorMaker, Redis, and Elasticsearch by hand. Operators are an optional later upgrade. |

> ⚠️ **Cost warning up front.** You run **single-cluster, single-region** for most of this plan
> (Phases 1–8). Multi-region (Phases 9–10) means **2+ EKS control planes** (~$0.10/hr each, always
> on) plus cross-region data transfer — you only spin that up at the end, so you control when the
> meter accelerates. See §9.

---

## 1. Goal & Learning Objectives

Deeply learn **CI/CD + Kubernetes + EKS + service mesh + the surrounding platform/SRE estate, by
hand.** The app is deliberately simple business-wise — the microservices are "traffic generators"
for the infrastructure you actually want to master.

By the end you will have hand-written and *understood* every item below:

**CI/CD**
- Jenkins pipeline: lint → test → build → push to ECR (multi-arch), git-SHA tagging, one reusable pipeline
- Image promotion by tag; deploy stays a manual `kubectl` action

**Workloads & scheduling**
- Deployment, StatefulSet, Job, CronJob, DaemonSet
- Affinity/anti-affinity, topology spread constraints, taints/tolerations, priority classes

**Config & identity**
- ConfigMap, Secret, ServiceAccount + IRSA (IAM Roles for Service Accounts)
- Namespaces, ResourceQuota, LimitRange, granular RBAC

**Networking**
- Service (ClusterIP/Headless/LoadBalancer), Ingress / Gateway API, NetworkPolicy (default-deny)
- AWS Load Balancer Controller, ExternalDNS, Route 53 latency/failover routing
- Multi-cluster east-west gateways, cross-cluster service discovery

**Scaling & resilience**
- HPA (CPU + custom metrics), VPA, KEDA (event-driven, scale-to-zero)
- Karpenter (node autoscaling), Cluster Autoscaler trade-offs
- PodDisruptionBudget, resource requests/limits, graceful shutdown, readiness gating

**Service mesh (Istio)**
- mTLS (STRICT), VirtualService/DestinationRule, locality-aware load balancing
- Manual canary + traffic-split + blue-green, retries/timeouts/circuit-breaking, fault injection
- Multi-primary mesh across regions, east-west gateway, failover & outlier detection

**Reliability engineering**
- Chaos engineering (Chaos Mesh / Litmus): pod kill, network latency/partition, CPU/mem stress
- Velero backup/restore, PV snapshots, cross-region restore
- Documented, *rehearsed* disaster-recovery runbook (region failover game-day)

**Observability & SLOs**
- Prometheus (per cluster) + **Thanos** (global query/dedup/long-term storage)
- Grafana dashboards, Alertmanager routing, **SLOs + error budgets + burn-rate alerts**
- Loki (logs), Tempo/Jaeger (distributed tracing), OpenTelemetry Collector pipeline
- RED/USE method dashboards; golden-signal alerting

---

## 2. Tech Stack

| Layer | Choice | Notes |
|---|---|---|
| Backend languages | **Go + Python** | Polyglot mesh demo, not a zoo |
| Frontend | **Node (Express SPA)** | Single UI microservice |
| Datastores | Postgres, Redis, Elasticsearch | In-cluster StatefulSets, hand-replicated across regions |
| Messaging | **Kafka** (+ MirrorMaker 2 cross-region) | Async events + cross-region mirroring |
| Mesh | **Istio** (multi-primary at the end) | mTLS + traffic mgmt + multi-cluster + observability |
| Cloud | **AWS EKS** — 1 region first, 2 at the end | eksctl or Terraform |
| Registry | **Amazon ECR** | one repo per microservice (cross-region replication later) |
| Node autoscaling | **Karpenter** | spot-first provisioning |
| **CI/CD** | **Jenkins** | builds + pushes to ECR only (**no deploy**) |
| Service discovery | **Env-var URLs + mesh** | microservices read target URLs from env; Istio handles cross-cluster routing |
| CD | **Manual `kubectl apply`** | no Argo CD / Flux — deploy by hand, on purpose |
| Observability | Prometheus + **Thanos**, Grafana, Alertmanager, Loki, Tempo, OTel | global multi-cluster view |
| Reliability | HPA/VPA/KEDA, Karpenter, PDB, Velero, Chaos Mesh | autoscale, protect, break, recover |

---

## 3. Microservice Inventory

| # | Microservice | Lang | Port | Datastore | Talks to | Type |
|---|---|---|---|---|---|---|
| 1 | `api-gateway` | Go | 8080 | — | all (routing) | edge ✅ built |
| 2 | `auth` | Go | 8081 | Postgres | user | sync ✅ built |
| 3 | `user` | Python | 8082 | Postgres | — | sync ✅ built |
| 4 | `catalog` | Go | 8083 | Postgres | — | read-heavy ✅ built |
| 5 | `search` | Python | 8084 | Elasticsearch | catalog | read-heavy ✅ built |
| 6 | `cart` | Go | 8085 | Redis | catalog | sync ✅ built |
| 7 | `order` | Go | 8086 | Postgres | cart, payment, inventory | orchestrator ✅ built |
| 8 | `payment` | Go | 8087 | Postgres | — (mock) | fault target ✅ built |
| 9 | `inventory` | Python | 8088 | Postgres | Kafka (consumer) | sync + async ✅ built |
| 10 | `notification` | Python | 8089 | — | Kafka (consumer) | async ✅ built |
| 11 | `recommendation` | Python | 8090 | Postgres | Kafka (consumer) | async / A-B ✅ built |
| 12 | `frontend` | Node | 3000 | — | api-gateway | UI ✅ built |

> **🎉 All 12 microservices + the `thumbnail-job` worker are code-complete and verified to
> compile/parse.** The integration spec is [`services/api-details.md`](./services/api-details.md);
> how the app works is [`ARCHITECTURE.md`](./ARCHITECTURE.md); the deployment shape is
> [`DEPLOYMENT_ARCHITECTURE.md`](./DEPLOYMENT_ARCHITECTURE.md).

**Plus infra workloads you deploy:** Postgres (×7, one per owning microservice), Redis, Elasticsearch,
Kafka (KRaft), and a **`thumbnail-job`** (Kubernetes Job + KEDA) that processes product images off a
queue — so you also learn Jobs + scale-to-zero. ✅ worker built.

**Every microservice supports the platform layer:** `/healthz` + `/readyz`, `/metrics` (RED), W3C
trace propagation, JSON logs with `trace_id`, graceful shutdown on SIGTERM, config 100% via env vars.

### Key interaction flows (so the mesh has something to show)
- **Sync chain (tracing + circuit breaking):** `frontend → api-gateway → order → payment → inventory`
- **Async events (Kafka):** `order` emits `order.created` → consumed by `inventory`, `notification`, `recommendation`
- **Read-heavy (HPA + canary):** `catalog`, `search`
- **Auth boundary (mesh authz):** `auth` issues JWT; `api-gateway` + Istio `AuthorizationPolicy` enforce it

---

## 4. Repository Layout (monorepo)

```
new_project/
├── EXECUTION_PLAN.md          # this file
├── ARCHITECTURE.md            # how the app works end-to-end
├── DEPLOYMENT_ARCHITECTURE.md # the EKS/Istio/multi-region deployment blueprint
├── learning/                  # ← your study tracks (theory + hands-on, numbered)
│   ├── jenkins/  docker/  kubernetes/  istio/  eks/
│   ├── observability/  reliability-chaos/  multi-cluster/
├── services/                  # ← Claude delivered this (all built)
│   ├── api-details.md         #   the integration spec
│   ├── catalog/ auth/ user/ api-gateway/ cart/ order/ payment/
│   ├── inventory/ notification/ recommendation/ search/ frontend/ thumbnail-job/
├── .ci/                       # ← You own this (Jenkinsfile + shared pipeline lib)
├── deploy/                    # ← You own this (applied manually with kubectl)
│   ├── base/                  #   per-microservice raw manifests
│   ├── overlays/              #   per-cluster/per-region differences (plain copies, by hand)
│   ├── infra/                 #   postgres/redis/kafka/es statefulsets + replication
│   ├── istio/                 #   mesh: gateways, VS/DR, peerauth, east-west gateway
│   ├── observability/         #   prometheus, thanos, grafana, alertmanager, loki, tempo, otel
│   ├── reliability/           #   hpa/vpa/keda, pdb, karpenter, velero
│   └── chaos/                 #   chaos-mesh experiments
└── infra-eks/                 # ← You own this (eksctl/terraform, per region)
    ├── region-primary/  region-secondary/  global/ (route53, ecr replication)
```

---

## 5. Phased Execution Plan

**Golden rule: build bottom-up, one working layer at a time.** Each phase is a complete, working
state you fully understand before adding the next. Every deploy is a manual `kubectl apply` — read
each manifest before you apply it.

Legend: **(You)** = DevOps work · code is **✅ already delivered** for every phase.

```
P1 CI/CD (Jenkins → ECR)   →   P2 One cluster, one region   →   P3 Datastores + first deploy
   →   P4 Full microservice fleet   →   P5 Istio mesh   →   P6 Reliability   →   P7 Observability
   →   P8 Chaos   →   P9 Second cluster (multi-cluster mesh)   →   P10 Multi-region + DR   →   P11 Hardening
```

---

### Phase 1 — CI/CD with Jenkins (build + push to ECR) (You)
**Goal:** every microservice has a reproducibly-built image in ECR, tagged by git SHA. **No cluster
needed yet** — Jenkins + ECR are independent of EKS, so images sit ready for Phase 3+.
- [ ] Create **ECR repositories** — one per microservice + the worker (13 repos)
- [ ] Stand up **Jenkins** (EC2/VM or a container) with AWS creds to push to ECR (later: IRSA/OIDC)
- [ ] Author **one reusable pipeline** (shared library / templated `Jenkinsfile`):
      checkout → lint → test → `docker build` (multi-arch) → tag = `<git-sha>` → push to ECR
- [ ] Run it for **all 13 images**; confirm they land in ECR
- [ ] Document the **deploy step stays manual** (`kubectl set image …:<sha>` or apply updated Deployment)
- **Code:** ✅ every microservice ships a multi-stage, non-root Dockerfile ready for this pipeline

**Exit check:** pushing a commit auto-builds + pushes that microservice's image to ECR, tagged by
SHA; the pipeline is re-runnable and works for any microservice.

---

### Phase 2 — One EKS cluster, one region (You)
**Goal:** one empty but real EKS cluster you can deploy to.
- [ ] Provision **EKS cluster #1** in a single region (eksctl or Terraform) — managed node group,
      2–3 nodes, **spot**
- [ ] `kubectl` access; verify `kubectl get nodes`
- [ ] Install **AWS Load Balancer Controller** (Ingress → ALB) and **ExternalDNS**
- [ ] Install **Karpenter** (you'll lean on it for autoscaling + spot)
- [ ] Create **namespaces**: `shop` (apps), `data` (datastores); add `ResourceQuota` + `LimitRange`
- [ ] Cost guardrail: scale-node-group-to-zero + teardown scripts

**Exit check:** `kubectl apply` a hello pod, reach it via an ALB, Karpenter provisions a node on demand.

---

### Phase 3 — Databases & datastores, + first real deploy (You)
**Goal:** the stateful layer is running, and one microservice is live end-to-end to prove the path.
- [ ] Deploy **datastores as StatefulSets** in `data` (each: PVC on `gp3`, headless Service, Secret):
      - **Postgres ×7** — one per owning microservice (`auth, user, catalog, order, payment, inventory, recommendation`) — **database-per-service**
      - **Redis** (cart) · **Elasticsearch** (search) · **Kafka** (KRaft, the event bus)
- [ ] Verify connectivity + persistence (PVC survives a pod restart)
- [ ] Deploy the **first microservice end-to-end: `catalog`** + wire it to `postgres-catalog`
      (`Deployment`, `Service`, `ConfigMap`, `Secret`, `Ingress`), with liveness/readiness probes
      and resource requests/limits
- [ ] Seed data: `POST /products`; confirm it persists across a `catalog` pod restart

**Exit check:** `curl` `catalog` through the ALB; data persists across restarts; every datastore is
`Ready` and reachable from the `shop` namespace.

---

### Phase 4 — Deploy the full microservice fleet + async backbone (You)
**Goal:** all 12 microservices + `thumbnail-job` running in the one cluster (pre-mesh, ALB ingress).
- [ ] For **each microservice**: `Deployment` (image from ECR, `envFrom` ConfigMap + Secret, probes,
      requests/limits, `terminationGracePeriodSeconds`), `Service`, `ConfigMap`, `Secret`
- [ ] Wire **service discovery**: `*_URL` env vars (`CATALOG_URL`, `USER_URL`, `CART_URL`, …) →
      Kubernetes Service DNS; `auth`'s stable `JWT_PRIVATE_KEY_PEM` Secret
- [ ] Wire **Kafka consumers** (`inventory`, `notification`, `recommendation`) and **`thumbnail-job`**
      (as a Job/Deployment; KEDA comes in Phase 6)
- [ ] **NetworkPolicy**: default-deny in `shop` + `data`, then open exactly the call-graph edges
- [ ] Expose the edge via **ALB Ingress** → `api-gateway` + `frontend`

**Exit check:** a full checkout works end-to-end through the running app; placing an order fans out
`order.created` to inventory + notification + recommendation.

---

### Phase 5 — Istio service mesh (You)
**Goal:** the mesh wraps the running app — mTLS, traffic management, traces.
- [ ] Install **Istio** (istioctl); enable sidecar injection on the `shop` namespace; redeploy
- [ ] **mTLS STRICT** (`PeerAuthentication`); confirm in **Kiali**
- [ ] Replace ALB Ingress with **Istio Gateway + VirtualService** at the edge
- [ ] Install **Kiali + Jaeger/Tempo** — see the topology graph and a real distributed trace
- [ ] Add **DestinationRules** (connection pools, outlier detection = circuit breaking)

**Exit check:** a checkout traces across `gateway → order → payment → inventory` end-to-end in
Jaeger/Tempo, with mTLS shown in Kiali.

---

### Phase 6 — Reliability & autoscaling (You)
**Goal:** the system scales and protects itself.
- [ ] **HPA** on `catalog`, `search` (CPU first, then **Prometheus-adapter RPS** custom metrics)
- [ ] **KEDA** on async consumers (scale on **Kafka lag**) and the **`thumbnail-job`** (scale-to-zero)
- [ ] **VPA** (recommend mode) on a couple of microservices to right-size requests
- [ ] **Karpenter** consolidation + spot-interruption handling
- [ ] **PodDisruptionBudgets** on critical microservices; topology spread + anti-affinity
- [ ] **Velero**: scheduled namespace backups + PV snapshots; do a **restore drill**

**Exit check:** load test drives HPA + Karpenter to add pods/nodes; a node drain respects PDBs; you
restore a namespace from a Velero backup.

---

### Phase 7 — Observability & SLOs (You)
**Goal:** see and reason about the system; SLOs with error budgets.
- [ ] **Prometheus** scraping app `/metrics`; **Grafana** dashboards (RED per microservice, USE per node, Istio mesh)
- [ ] **Loki** + Promtail/Alloy — app JSON logs, queryable by `trace_id`
- [ ] **Tempo** (or Jaeger) via **OpenTelemetry Collector** — traces linked from logs + metric exemplars
- [ ] Define **SLOs** (e.g. checkout availability 99.5%, p95 latency) → **burn-rate alerts**
- [ ] **Alertmanager** routing (severity → channel), runbook links in alerts

**Exit check:** one checkout shows up as **metrics + logs + a trace**, all correlated by `trace_id`;
an SLO dashboard shows error budget; a synthetic failure fires a burn-rate alert.

---

### Phase 8 — Chaos engineering & game-days (You)
**Goal:** prove resilience by breaking things on purpose.
- [ ] Install **Chaos Mesh** (or Litmus)
- [ ] Experiments: **pod-kill**, **network latency/loss**, **partition**, **CPU/mem stress**, clock skew
- [ ] Combine with Istio **fault injection** + `payment`'s `FAIL_MODE` → observe retries + circuit breaking + outlier ejection
- [ ] Run a **game-day**: inject failure, watch SLO burn, follow the runbook, write a postmortem

**Exit check:** you can break a microservice and watch the mesh + autoscaling + alerts keep the SLO
(or learn exactly why they don't, and fix it).

---

### Phase 9 — Second cluster, multi-cluster mesh (You)
**Goal:** two clusters, one mesh (start **same region** to keep it sane).
- [ ] Stand up **EKS cluster #2**; **shared root CA** for Istio (cross-cluster trust)
- [ ] **Istio multi-primary** with an **east-west gateway**; endpoints discovered across clusters
- [ ] **Locality-aware load balancing** + failover (prefer local, spill over on failure)
- [ ] Replicate ECR images / config to cluster #2 (by hand — feel the toil)

**Exit check:** kill all `payment` pods in cluster #1; requests transparently serve from cluster #2.

---

### Phase 10 — Multi-region, active-active + DR (You)
**Goal:** the full enterprise footprint across two AWS regions.
- [ ] Cluster #2 (or #3) in a **second region**; mesh spans regions
- [ ] **Route 53** latency + **failover** routing; ExternalDNS per region; health checks
- [ ] **ECR cross-region replication**
- [ ] **Data strategy (hand-rolled):** Postgres cross-region **streaming replication** (documented
      promote-on-failover); Kafka **MirrorMaker 2**; Redis/ES replication or per-region with
      documented consistency trade-offs
- [ ] **Thanos**: per-cluster Prometheus + sidecar → **global query view** (dedup, long-term S3)
- [ ] **DR runbook + game-day:** simulate full region-A outage → fail over to region B → restore → write it up
- **Code:** ✅ consumers are idempotent and surface region/zone-friendly labels; minor tweaks on request

**Exit check:** take region A offline; global DNS + mesh + replicated data keep checkout working from
region B; Thanos shows one global view; you can fail back.

---

### Phase 11 — Hardening & definition-of-done (You)
**Goal:** production-shaped, still deployed manually, fully runbooked.
- [ ] Resource tuning from VPA data; namespace quotas; **cost review** (Kubecost optional)
- [ ] Tighten NetworkPolicies; Istio **`RequestAuthentication` + `AuthorizationPolicy`** (JWT) end-to-end
- [ ] Full **manual spin-up / tear-down / DR runbook** — recreate the whole estate from Git
- [ ] *(Optional add-on)* **Security & supply chain track** — see §7
- [ ] *(Optional add-on)* **Migrate data to operators** (CloudNativePG, Strimzi, ECK)

**Exit check:** you can recreate both regions from your manifests with documented `kubectl` steps,
and a region failover is a rehearsed, boring procedure.

---

## 6. Working Agreement (how we collaborate)

- Code is **delivered**; I adjust it when your platform needs a tweak (health path, lameduck delay,
  pre-stop hook, metric label, region awareness).
- Each microservice ships: code + Dockerfile + README + `.env.example` + the **config/secret key
  list** + **ports/endpoints** + **metrics/trace/log contract** — that's your manifest input.
- The **API contracts** are stable and documented in `services/api-details.md`, so your Istio
  routing/authz target fixed shapes.
- **Service discovery:** every microservice reads dependency URLs from **env vars** (`CATALOG_URL`,
  `ORDER_URL`, …); you supply them via ConfigMap. The mesh handles cross-cluster.
- **CI is Jenkins:** Dockerfiles are Jenkins-friendly multi-stage builds; you own the pipeline
  (build → push to ECR). Deploy stays manual `kubectl apply`.

---

## 7. Optional add-on — Security & supply chain (deferred, recommended)

Not selected for the core scope, but this is what makes it *truly* enterprise. Slot it in around
Phase 6–11 when you want it:
- **Policy:** OPA Gatekeeper or **Kyverno** (enforce non-root, resource limits, no `:latest`, etc.)
- **Pod Security Standards** (restricted), seccomp, read-only rootfs, drop capabilities
- **Supply chain:** **Trivy** image scanning in the Jenkins pipeline, **Cosign** signing, **SBOM**
- **Runtime:** **Falco** for runtime threat detection
- **Secrets:** **External Secrets Operator** → AWS Secrets Manager (IRSA), or **Vault**
- **TLS:** **cert-manager** for ingress/gateway certs
- **RBAC:** least-privilege ServiceAccounts, audit `kubectl auth can-i`

> Say **"add the security track"** and I'll fold it into the plan + learning folder.

---

## 8. Learning tracks (theory + hands-on)

Under `learning/`, each tool gets numbered lessons — **theory first, then do-it**.

| Track | For phases | Status |
|---|---|---|
| Jenkins (CI/CD) | 1 | 🟢 in progress |
| Docker | 1 | ⚪ planned |
| AWS EKS | 2, 9–10 | ⚪ planned |
| Kubernetes (core) | 2–4 | ⚪ planned |
| Istio (mesh) | 5, 9–10 | ⚪ planned |
| Reliability & chaos | 6, 8 | ⚪ planned |
| Observability & SLOs | 7 | ⚪ planned |
| Multi-cluster / multi-region | 9–10 | ⚪ planned |

---

## 9. Cost control (EKS is *not* free)

- **Stay single-cluster, single-region for Phases 1–8** — one EKS control plane (~$0.10/hr) + a small
  spot node group. Spin the 2nd cluster/region only for Phases 9–10, and tear it down between sessions.
- Use **spot** + **Karpenter consolidation**; scale node groups to **0** when idle (script it).
- Run datastores **in-cluster** (not RDS/MSK) until you choose to upgrade.
- Multi-region adds **2+ control planes** + **cross-region data transfer** (replication + Thanos S3) — watch it.
- Keep **all** manifests in Git so any cluster/region is one documented re-apply away.
- A **teardown checklist** is part of the DR runbook — losing track of running clusters is the
  classic way to get a surprise bill.

---

## 10. Definition of Done

- [ ] A working **Jenkins CI** pipeline (build + push to ECR; deploy stays manual)
- [ ] 12 microservices running on EKS, all in the Istio mesh with **mTLS**
- [ ] Every Kubernetes primitive in §1 hand-written and `kubectl apply`-ed yourself
- [ ] **Autoscaling** (HPA/VPA/KEDA/Karpenter) + **PDBs** demonstrably protecting the system
- [ ] **Full observability**: correlated metrics + logs + traces; SLOs with error budgets + burn-rate alerts
- [ ] **Chaos game-days** run, with postmortems; resilience proven, not assumed
- [ ] **Velero backups** + a restore drill
- [ ] **Multi-cluster + multi-region** with locality-aware failover, global DNS, and a **rehearsed DR runbook**
- [ ] *(Stretch)* Security & supply-chain track and/or data operators added

---

## 11. Next Action

Development is done — start the DevOps track at **Phase 1**:

1. **Phase 1 — Jenkins CI/CD:** create the ECR repos, stand up Jenkins, and build + push all 13
   images with one reusable pipeline. (Your `learning/jenkins/` track is already started.)
2. Then **Phase 2** (one EKS cluster, one region) → **Phase 3** (datastores + deploy `catalog`).

> Want help moving? I can **scaffold `.ci/` (a templated Jenkinsfile + shared pipeline)**, or
> **scaffold the Phase 3 `catalog` manifest set** (Deployment/Service/ConfigMap/Secret + Postgres
> StatefulSet) as a worked, readable example. Say which and I'll generate it.
