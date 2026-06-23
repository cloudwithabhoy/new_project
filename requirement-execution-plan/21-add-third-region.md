# Phase 21 — Add the third region (Singapore)

## Goal
Add a **third region (Singapore / `ap-southeast-1`)** by **following the runbook**, not redesigning anything — the same autonomous regional stack, global-routed, DB-replicated, Kafka-meshed — proving the architecture scales to N regions.

## Why this phase
Mumbai (primary) and UK are already live. If adding region #3 is "follow the runbook," the design is proven repeatable. Singapore is also lower-traffic, so it becomes the natural **canary region** for releases.
- **Builds on:** Phases 09-20 (the full multi-region machinery proven on two regions).
- **Unlocks:** Singapore as a low-traffic canary region for releases.

## Scope
**In scope:** provisioning the full Singapore single-region stack; adding SG to the global router, the Mumbai-primary read-replica fan-out, the Kafka MM2 mesh, ECR replication, the release/canary matrix, and global observability.
**Out of scope:** any bespoke per-region redesign; changing the single-write-primary model (writes still go to Mumbai).

## What it needs to do
- Provision the entire single-region stack (Phases 01-08) in `ap-southeast-1`: EKS, node group, ALB controller, namespaces, datastores, services, Istio, scaling, observability.
- Add one global-router latency/geo record + health check for SG (Phase 09) so SG users are served locally and SG joins the failover pool.
- Add a Singapore read replica off the Mumbai primary (Phase 10); SG reads local, **writes still go to the single primary**.
- Add SG to the Kafka MM2 mesh (Phase 12) and as an ECR replication target (Phase 14).
- Add SG to the release matrix as the **canary region** (Phase 16) and to global observability (Phase 22).
- Adding region #3 requires no redesign — only the runbook and per-region values.
- Stateless RS256 JWTs verify anywhere, so a third region adds zero session work.
- Failing SG's health check routes those users to the next-nearest region.
- What grows with region count is O(number of regions): replica fan-out, MM2 links, transfer cost, rollout matrix.

## Architecture

```
              users worldwide ─► GLOBAL ROUTER  (Route 53 geo + health checks)
          ┌──────────────────────────┼──────────────────────────┐
          ▼                          ▼                          ▼
 ┌─ MUMBAI ─────────┐      ┌─ LONDON ─────────┐      ┌─ SINGAPORE ──────┐   (NEW: 3rd region)
 │ full stack       │      │ full stack       │      │ full stack       │
 │ PRIMARY DB       │      │ read replica     │      │ read replica     │
 │ Kafka            │      │ Kafka            │      │ Kafka            │
 └───────┬──────────┘      └───────┬──────────┘      └───────┬──────────┘
         └─ DB replication (WAL) ──┴──── + Kafka MM2 mirroring ───────────┘
   ECR replicated to SG  ·  added to the release order (good canary region)  ·  added to global observ.
   what grows with #regions: replica fan-out · MM2 links · cross-region transfer cost
```
**What's new in this step:** Singapore, added by repeating Steps 16–20 — proving the architecture scales
to N regions by following the runbook, not a redesign.

## The design
Singapore = the *entire* single-region stack, slotted into every existing global mechanism — nothing new is invented, one more leaf is added to each topology.
```
                users worldwide ─► GLOBAL ROUTER (Route 53 latency/geo + health checks)
          nearest=IN│        nearest=UK│        nearest=SG│  ◄── NEW record + health check
                ▼              ▼              ▼
          ALB(Mumbai)     ALB(London)    ALB(Singapore)      each: own EKS + Istio + full stack
           PRIMARY DB ──WAL──► UK replica ──┐
                    └──────WAL──────────────┴──► SG read replica   ◄── NEW replica (writes → Mumbai)
          Kafka(IN) ◄─MM2─► Kafka(UK) ◄─MM2─► Kafka(SG)            ◄── NEW MM2 links
          ECR(IN, primary) ──replication──► ECR(UK) + ECR(SG)     ◄── NEW replication target
```

## How it works / why this approach
It is a repeat of Steps 16–20 for `ap-southeast-1`:
- **Provision the stack** (the single-region build, Phases 01-08): EKS `ecom-cluster`, node group, ALB controller, namespaces, datastores, services, Istio, scaling, observability.
- **Global router** (Phase 09): one more latency/geo record + health check → SG users get served locally, and SG joins the failover pool.
- **Read replica** (Phase 10): a Singapore replica off the Mumbai primary; SG reads local, writes still go to the single primary. (Read-after-write handled as before.)
- **Sessions** (Phase 11): nothing to do — stateless RS256 JWTs verify anywhere, so a third region adds zero session work.
- **Kafka mesh** (Phase 12): add MM2 links so SG-local topics mirror out and global topics mirror in (source-prefixed, offset-translated).
- **ECR** (Phase 14): add `ap-southeast-1` as a replication target; Jenkins still pushes once, replication fans out the same SHA.
- **Release ordering** (Phase 16): add SG to the promotion matrix — as the lowest-traffic region, it's the canary: deploy here first, bake against SLOs, then promote UK → Mumbai.
- **Global observability** (Phase 22): add SG's Prometheus + Thanos sidecar so the global Querier sees all three regions.

What this validates: if region #3 is "follow the runbook," the design is proven repeatable — autonomous regional stacks + global geo-routing + single-write-primary scale out linearly. If it had required redesign, something in the 2-region pattern was wrong.

What is O(number of regions) and grows: the DB replication fan-out (one more replica to keep in sync off the primary), Kafka MM2 links, cross-region data-transfer cost, and the release/rollout matrix. These scale with region count, so each addition is more replication topology and more spend — watch them.

Why not the alternatives? A bespoke per-region design defeats the whole point (repeatability, one runbook, one mental model); adding regions before the 2-region pattern is proven just multiplies an unvalidated design — you fix it once at two regions, then stamp it out.

## How to build it
1. **Provision Singapore** (the single-region build (Phases 01-08)): EKS + ALB + namespaces + datastores + services + Istio + scaling + observability.
2. **Add the global-router record + health check** for SG (Phase 09).
3. **Add the SG read replica** off the Mumbai primary; wire reads→local, writes→primary (Phase 10).
4. **Add SG to the Kafka MM2 mesh** (Phase 12) and as an **ECR replication target** (Phase 14).
5. **Add SG to the release matrix as the canary region** (Phase 16) and to **global observability** (Phase 22).

## Done when
- A request from Singapore lands on the **SG** stack; failing SG's health check routes those users to the next-nearest region.
- A write in Mumbai appears in the **SG replica** within expected lag; SG reads local, writes to Mumbai.
- An `order.created` event mirrors between SG and the other regions' Kafka; the latest image SHA is present in `ap-southeast-1` ECR.
- A canary release deployed to **Singapore first** bakes against SLOs before promoting outward.

---
> Interview one-liner: *"Adding region #3 — Singapore — is following the runbook, not redesigning: stamp out the same autonomous stack, add one record to the global router, one read replica off the single primary, one set of MM2 links, one ECR replication target, and one Prometheus into the Thanos view; JWTs are stateless so sessions are free. The only things that grow with region count are replication fan-out, mirroring links, transfer cost, and the rollout matrix — and because Singapore is the lowest-traffic region, it becomes my canary; if scaling to N regions is 'follow the runbook,' the architecture is proven."*
