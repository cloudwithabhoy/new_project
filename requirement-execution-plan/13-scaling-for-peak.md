# Phase 13 — Scaling for Peak (festive season)

## Goal
The platform carries a festive-season traffic spike (like a sale event) across all three regions
(Mumbai / UK / Singapore) with no single point of failure — pods, nodes, and database reads all scale, and
any one AZ can die with zero downtime.

## Why this phase
With regions and replicas already in place, this is where you get the confidence to take real high-traffic
load. Each region scales independently for its own local surge — this is in-region HA, not cross-region DR.
- **Builds on:** Phase 06 (the autoscaling foundation — HPA/KEDA/Karpenter, read/write split live).
- **Unlocks:** Phase 17 (cross-region disaster recovery / failover).

## Scope
**In scope:** autoscaling (HPA on CPU+RPS, KEDA on Kafka lag, Karpenter spot-first nodes); extra read
replicas per region with read/write split; pre-scaling for a known peak; no-SPOF guardrails
(topologySpread + antiAffinity, PDBs, multi-AZ ALB and DB standby).
**Out of scope:** cross-region disaster recovery / failover (Phase 17); write sharding; vertical scaling as
a strategy.

## What it needs to do
- HPA scales the stateless tier (frontend, api-gateway, catalog, search) on CPU and Prometheus-adapter RPS.
- KEDA scales the event tier (async consumers, thumbnail-job) 0→N on Kafka consumer lag.
- Karpenter provisions nodes for pending pods, spot-first with on-demand fallback, and consolidates back
  down when the spike passes.
- Reads land on local read replicas while writes go to the single primary; extra read replicas are added
  per region for the peak.
- A known event is handled by scheduled pre-scaling (warm pods, warm nodes, extra replicas) before the
  event window, rather than reacting after users are already queued.
- Every layer runs ≥2 replicas spread across AZs via `topologySpreadConstraints` + `podAntiAffinity`; PDBs
  protect `minAvailable` during drains/upgrades; the ALB is multi-AZ; the DB has a Multi-AZ standby.
- Losing an entire AZ causes pods to reschedule and the standby to cover with no downtime.
- On critical read-after-write paths (e.g. "view the order you just placed"), reads come from the primary,
  since async replicas lag.

## Architecture

```
                 users (festive surge) ─► Global Router ─► nearest region
          ┌──────────────────────────────┴─────────────────────────────┐
          ▼                                                             ▼
 ┌─ MUMBAI ──────────────────────────────┐     ┌─ LONDON ──────────────────────────────
 │  HPA  ▲ pods (catalog/search/api-gw)   │     │  HPA  ▲ pods
 │  KEDA ▲ consumers (on Kafka lag)       │     │  KEDA ▲ consumers
 │  Karpenter ▲ nodes (spot-first)        │     │  Karpenter ▲ nodes
 │  reads ─► PRIMARY + read replicas ▲▲▲  │     │  reads ─► UK replicas ▲▲   (NEW: more read
 │  pre-scale ahead of the known event    │     │  scaled out for the peak        replicas for peak)
 │  pods across AZ a/b/c · PDB · no SPOF  │     │  pods across AZ a/b/c · PDB
 └────────────────────────────────────────┘     └────────────────────────────────────────
   writes still ─► single Mumbai primary (Step 10)   ·   an AZ can die with no downtime (in-region HA)
```
**What's new in this step:** autoscaling (HPA/KEDA/Karpenter) + extra read replicas + pre-scaling for a
known peak, with every layer ≥2 spread across AZs so there is no single point of failure.

## The design
Three layers scale together, every layer ≥2 replicas spread across AZs.
```
              festive spike (per region)
                       │
   ┌───────────────────▼───────────────────────────────────┐
   │ PODS                                                   │
   │  HPA  → frontend/api-gateway/catalog/search  (CPU+RPS) │
   │  KEDA → consumers + thumbnail-job   (Kafka lag)        │
   │           ▲ pending pods                               │
   │ NODES     │                                            │
   │  Karpenter → spot-first, on-demand fallback (AZ a/b/c) │
   │ DB READS                                               │
   │  primary ──WAL──► +N local read replicas (reads here)  │
   │           writes ─────────────► single primary        │
   └────────────────────────────────────────────────────────┘
   every layer: ≥2 replicas · topologySpread+antiAffinity · PDB · ALB multi-AZ · DB Multi-AZ standby
```

## How it works / why this approach
Three layers scale together, every layer ≥2 replicas spread across AZs.

Pods — two signals for two workload shapes:
- **HPA** on the stateless tier (frontend, api-gateway, catalog, search): scale on **CPU**, then on
  **Prometheus-adapter RPS** so we react to *request rate* before CPU even moves.
- **KEDA** on the event tier (async consumers, thumbnail-job): scale on **Kafka consumer lag** — the
  honest backlog signal — and scale **0→N** when work appears.

Nodes — Karpenter. When HPA/KEDA create pods that won't fit, Karpenter provisions nodes for the
pending pods: spot-first (cheap for burst) with on-demand fallback so a spot reclaim can't starve the
sale. It consolidates back down when the spike passes.

Database reads — replicas, not bigger instances. E-commerce traffic is mostly reads (~90%), so we absorb
the spike by adding read replicas and keeping the read/write split: writes → primary, reads → local
replicas. For a known event we pre-scale / overprovision ahead of time (scheduled scaling — warm pods,
warm nodes, extra replicas before the gates open) rather than wait for autoscalers to react after users
are already queued.

No SPOF. Every layer runs ≥2 replicas spread across AZs via `topologySpreadConstraints` +
`podAntiAffinity`; PDBs protect `minAvailable` during drains/upgrades; the ALB is multi-AZ; the DB has a
Multi-AZ standby. Lose an entire AZ and pods reschedule, the standby covers — no downtime.

Why not the alternatives:
- **Vertical scaling** (bigger pods/nodes): has a ceiling and gives no availability benefit — one fat
  replica is still a SPOF.
- **Only reacting via HPA for a *known* spike:** autoscalers lag the surge; for a scheduled event you
  pre-scale so capacity is already there.
- **Sharding writes:** unnecessary — the load is reads, and we scale those with replicas; single-write
  primary stays simple (no conflict resolution).

> **Read-after-write caveat** (from Step 10): async replicas lag, so right after a write a local read
> can miss it. On critical paths (e.g. "view the order you just placed") read from the **primary**.

## How to build it
1. **HPA** on frontend/api-gateway/catalog/search — CPU + Prometheus-adapter **RPS** targets.
2. **KEDA** ScaledObjects on consumers + thumbnail-job — **Kafka lag** trigger (0→N).
3. **Karpenter** provisioner: **spot-first + on-demand fallback**, AZ a/b/c, consolidation on.
4. **Add read replicas** per region; confirm reads hit local replicas, writes hit the primary.
5. **No-SPOF guardrails**: `topologySpreadConstraints` + `podAntiAffinity` + **PDBs** on every critical
   service; verify ALB and DB standby are multi-AZ.
6. **Pre-scale plan**: scheduled scaling to warm pods/nodes/replicas **before** the event window.

## Done when
- Synthetic load drives RPS up → HPA adds pods, Karpenter adds spot nodes, latency holds.
- Flooding a Kafka topic grows lag → KEDA scales consumers/thumbnail-job and the backlog drains.
- Reads land on local replicas; a known-event pre-scale leaves capacity ready before traffic.
- Kill one AZ's nodes mid-load → pods reschedule, DB standby covers, no downtime.

---
> Interview one-liner: *"For a known festive spike I scale every layer with no SPOF — HPA on CPU+RPS for stateless services, KEDA on Kafka lag for consumers, Karpenter spot-first with on-demand fallback for nodes, and read replicas with a read/write split for the read-heavy load — pre-scaling ahead of the event instead of reacting late, with ≥2 replicas spread across AZs plus PDBs and Multi-AZ standby so any one AZ can die with zero downtime."*
