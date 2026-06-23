# Phase 03 — Datastores & first deploy (catalog)

## Goal
The stateful layer is running, and **one microservice (`catalog`) is live end-to-end** to prove the
deploy path before the whole fleet.

## Why this phase
The services are stateful, so the data layer has to exist first — and you prove the deploy path with
**one** service before risking the whole fleet.
- **Builds on:** Phase 02 (an empty cluster with ingress + namespaces).
- **Unlocks:** Phase 04 (the StatefulSet pattern + the databases the full fleet needs).

## Scope
**In scope:** the datastores as StatefulSets (Postgres-per-service, Redis, Elasticsearch, Kafka) on `gp3`
storage; the `catalog` service wired to its own database and exposed through the ALB.
**Out of scope:** the other 11 services + the Kafka backbone (Phase 04); mesh, autoscaling, observability
(Phases 05–07); cross-region replication (Phase 10).

## What it needs to do
- Each database runs as its own StatefulSet with its own disk (database-per-service, no sharing).
- Each datastore is reachable by a stable name from the `shop` namespace.
- `catalog` is deployed, talks to its Postgres, and is reachable through the ALB.
- Product data lives in Postgres and survives the pod being killed.
- Credentials come from Secrets, never baked into images.

## Architecture

```
   users ─► ALB ─► Ingress ─► catalog                       (NEW: first service live)
   ┌─ MUMBAI ────────────────────────────────────────────
   │  data ns:  postgres-catalog  +  Redis · Elasticsearch · Kafka   (NEW: datastores)
   │            (StatefulSets · PVC on gp3 · headless Service · Secret)
   │  shop ns:  catalog ─► postgres-catalog
   │            (Deployment · Service · ConfigMap · Secret · probes)
   └──────────────────────────────────────────────────────
```
**What's new in this step:** the datastores (as StatefulSets) and the first real service wired to its
own database, reachable through the ALB.

## Done when
- Every datastore is `Ready` and reachable from the `shop` namespace.
- `catalog` is reachable through the ALB and returns a product you created.
- That product is still there after the `catalog` pod restarts.
- A datastore's data survives a `delete pod` (the volume re-attaches).

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/3.1-datastores.md`](../step-by-step-implementation/3.1-datastores.md) —
  EBS CSI + gp3 StorageClass, the `postgres-catalog` StatefulSet + headless Service, proving persistence,
  and stamping out the rest of the datastores. *(deep-dive: StatefulSet vs Deployment, PV/PVC)*
- [`step-by-step-implementation/3.2-deploy-catalog.md`](../step-by-step-implementation/3.2-deploy-catalog.md) —
  the `catalog` service (ConfigMap + Secret + Deployment + Service + Ingress), rollout, and end-to-end
  seed/verify through the ALB. *(deep-dive: readiness probes & Services/Endpoints)*

---
> Interview one-liner: *"Datastores run in-cluster as StatefulSets with per-service databases, and I prove
> the path with one service — catalog wired to its Postgres, reachable via the ALB, data surviving
> restarts — before rolling out the fleet."*
