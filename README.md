# E-Commerce Microservices on EKS — an enterprise platform, built by hand

A 6-microservice e-commerce platform built **from scratch** and run on
**AWS EKS** — taken all the way from **CI/CD → a single region → multi-region active-active**, every
piece hand-built and understood, no shortcuts. The business logic is deliberately thin; the
**infrastructure** is the point. (Scope trimmed to 6 services to move fast while still exercising every
platform pattern — edge, JWT auth, a sync call chain, Kafka async, and database-per-service.)

**The infrastructure (what gets built):**
- **Compute / orchestration** — AWS **EKS** + **Karpenter** (spot-first node autoscaling), multi-AZ
- **CI/CD** — **Jenkins** (one pipeline per service) → **Amazon ECR**; deploy stays manual `kubectl` (no GitOps)
- **Service mesh** — **Istio**: mTLS, VirtualService/DestinationRule, canary/traffic-split, fault injection, tracing
- **Datastores** — in-cluster StatefulSets: **Postgres ×4 · Kafka**
- **Scaling & reliability** — **HPA / VPA / KEDA · PodDisruptionBudgets · Velero** backups · **Chaos Mesh**
- **Observability** — **Prometheus · Grafana · Loki · Tempo · OpenTelemetry · Alertmanager** (+ **Thanos** global view)
- **Edge & networking** — **Route 53 · ALB · ExternalDNS · NetworkPolicy** (default-deny)
- **Multi-region** — global geo-routing · cross-region **DB replication** + **Kafka MirrorMaker 2** · **DR**
- **Security & access** — **IRSA** · SSO/AD → **EKS RBAC** · **Client VPN + Transit Gateway**

Claude wrote all the microservice code (done); **you build every piece of the infrastructure above, by hand.**

---

## The end-state at a glance

3 regions, each a complete autonomous stack, behind a global router that serves every user from their
nearest healthy region — with cross-region data replication and a tested DR path.

```
   developers ─► Jenkins ─► ECR (cross-region replicated)        users worldwide
                                                                       │
                                                ┌──────────────────────▼──────────────────────┐
                                                │  GLOBAL ROUTER · Route 53 geo + health checks │
                                                └──────┬───────────────┬───────────────┬───────┘
                                                       ▼               ▼               ▼
                                              ┌─ MUMBAI ────┐  ┌─ LONDON ────┐  ┌─ SINGAPORE ─┐
                                              │ ap-south-1  │  │ eu-west-2   │  │ap-southeast-1
                                              │ EKS·Istio   │  │ EKS·Istio   │  │ EKS·Istio   │
                                              │ DB·Kafka·LB │  │ DB·Kafka·LB │  │ DB·Kafka·LB │
                                              │ observ.     │  │ observ.     │  │ observ.     │
                                              └──────┬──────┘  └──────┬──────┘  └──────┬──────┘
                                                     └── DB replication + Kafka MM2 ───┘
                                                              ─► Thanos global view
```

**Built one region first** (Mumbai), proven end-to-end, *then* replicated. Multi-region is the last layer.

---

## The plan

[`requirement-execution-plan/`](./requirement-execution-plan/) is the **single ordered master plan** — 22 phases:

- **01–08 — build one region:** CI/CD → EKS cluster → datastores → full fleet → Istio mesh →
  reliability → observability → chaos.
- **09–22 — go multi-region:** add a region + global routing → DB replication → sessions → Kafka →
  scaling → multi-region CI/CD → deploy strategy → release → DR → dev access → access management →
  storage tiering → 3rd region → global observability.

Each phase doc carries an **evolving architecture diagram** (you watch the system grow), detailed
**execution steps**, an exit check, and an interview one-liner.

---

## Documentation map

**Plan** (start here)
- [`requirement-execution-plan/`](./requirement-execution-plan/) — the ordered 22-phase master plan (design + steps).

**Architecture** (the what / why)
- [`APPLICATION-ARCHITECTURE.md`](./APPLICATION-ARCHITECTURE.md) — how the **app** works: services, call graph, checkout flow.
- [`SINGLE-REGION-ARCHITECTURE.md`](./SINGLE-REGION-ARCHITECTURE.md) — one region's **anatomy** (the unit you replicate).
- [`MULTI-REGION-ARCHITECTURE.md`](./MULTI-REGION-ARCHITECTURE.md) — the **final** multi-region architecture, CI/CD → observability.

**Hands-on runbook** (exact commands + the gotchas I hit)
- [`step-by-step-implementation/`](./step-by-step-implementation/) — the hands-on runbook, in small numbered parts: `1.x` CI/CD · `2.x` EKS cluster · `3.x` datastores + first deploy · `4.x` full fleet + Kafka · `5.x` Istio · `6.x` reliability · `7.x` observability · `8.x` chaos · `9.x` second region + global routing.

**Learning / interview notes**
- [`project_learning.md`](./project_learning.md) — the "why / how it works" concept deep-dives for every step, grouped by phase (kept separate so the runbook stays action-only).

**Reference**
- [`services/api-details.md`](./services/api-details.md) — API + env-var integration spec for all services.
- [`services/`](./services/) — the 13 components (each: source + Dockerfile + README + Jenkinsfile).
- [`.ci/jobs.groovy`](./.ci/jobs.groovy) — the Job DSL seed that generates all 13 Jenkins pipelines.

> **Reading order:** this README → `requirement-execution-plan/` → `APPLICATION-ARCHITECTURE.md` →
> `SINGLE-REGION-ARCHITECTURE.md` → `MULTI-REGION-ARCHITECTURE.md`.

---

## Key facts / constants

| Thing | Value |
|---|---|
| AWS account | `169424082295` |
| Regions | **Mumbai `ap-south-1`** (primary) · London `eu-west-2` · Singapore `ap-southeast-1` (later) |
| EKS cluster | `ecom-cluster` |
| ECR repo | `ecom-repo` (shared; image tag = `<service>-<git-sha>`, immutable, no `:latest`) |
| CI | Jenkins on a t3.medium EC2 — one Pipeline job per service |
| CD | **manual `kubectl apply`** (no GitOps — deliberate, for learning) |
| Mesh | Istio, inside each region |
| Datastores | Postgres ×4 · Kafka — in-cluster StatefulSets |
| Data model | single-write-primary + cross-region read replicas |

---

## The 6 microservices

`api-gateway` (edge) · `auth` · `catalog` · `order` (checkout orchestrator) · `inventory` · `frontend` (UI).

- **Sync chain:** `frontend → api-gateway → order → inventory` (and `api-gateway → catalog` for browsing)
- **Async events (Kafka):** `order` emits `order.created` → `inventory`

> Scope was trimmed from the original 12 services + worker to these 6 — enough to exercise every platform
> pattern (edge, JWT auth, a multi-hop sync chain, Kafka producer/consumer, database-per-service) while
> keeping the build fast.

Full detail in [`APPLICATION-ARCHITECTURE.md`](./APPLICATION-ARCHITECTURE.md).
