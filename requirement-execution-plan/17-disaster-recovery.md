# Phase 17 — Disaster recovery

## Goal
Survive losing a whole region with minimal downtime and data loss, turning a region outage into a rehearsed,
boring procedure.

## Why this phase
A whole region will eventually fail, and the platform has to drain its users to the survivors instead of
relying on hope. If the failed region held the write primary (Mumbai), writes have to be recovered by
promoting a replica, and the region has to fail back cleanly when it returns. Because we already run
active-active, most of this comes for free — this phase makes it tested and routine.
- **Builds on:** Phases 09–10 (Route 53 health-check routing + single-write-primary with read replicas).
- **Unlocks:** production confidence — a region loss becomes a known drill, not a crisis.

## Scope
**In scope:** automatic user-facing failover (Route 53 health checks), write-primary promotion of a read
replica, failback to Mumbai, restore-from-backup for the corruption case, and a DR game-day.
**Out of scope:** the routing and replication foundations themselves (Phases 09–10), which DR builds on.

## What it needs to do
- A failed Route 53 region health check drops that region from DNS answers automatically.
- Surviving regions keep serving reads locally from their replicas.
- A read replica can be promoted to write primary, with every region re-pointing its `WRITER`.
- Failback re-syncs the returned region, demotes the temporary primary, and re-promotes Mumbai with no lost
  or duplicated data (proven by reconciliation).
- User-facing reroute is near-automatic (RTO ≈ DNS TTL, near-zero with Global Accelerator); RPO = replication
  lag (sub-second).
- Velero + DB snapshot restore brings a namespace/datastore back to a chosen point in time — the safety net
  for data corruption that replication would otherwise just copy.

## Architecture

```
                 users ─► Global Router  (per-region health checks)
          ┌──────────────────────────────┴─────────────────────────────┐
          ▼                                                             ▼
 ┌─ MUMBAI (PRIMARY)  ✗ UNHEALTHY ───────┐     ┌─ LONDON (healthy) ───────────────────
 │  health check FAILS                   │     │  receives the rerouted users
 │  region down                          │     │  reads served from UK replica (local)
 └────────────────────────────────────────┘     │   ┌─────────────────────────────┐
        │                                         │   │  PROMOTE UK replica ─► new   │   (NEW)
        ├─ Route 53 drops Mumbai ─► users to UK ─►│   │  write PRIMARY               │
        └─ DR action: promote a replica ─────────►│   └─────────────────────────────┘
   model = ACTIVE-ACTIVE  (auto reroute = low RTO · RPO = replication lag)  ·  fail back when Mumbai returns
   Velero / DB snapshots = the deeper safety net (data corruption, not just region loss)
```
**What's new in this step:** region-failure DR — Route 53 auto-reroutes users to a healthy region, and
you promote a read replica to the new write primary; fail back later. (Active-active = mostly automatic.)

## The design
Because we run active-active, DR is mostly the routing + replication we already built, with one manual move — write-primary promotion.

```
        users ─► Route 53 (health checks)
                   │  Mumbai health check FAILS
                   ▼  ─► drop Mumbai from answers (AUTOMATIC)
        ┌──────────┴───────────┐
        ▼                      ▼
   UK (eu-west-2)        Singapore (ap-southeast-1)
   reads: local        reads: local 
   writes: were Mumbai ──► PROMOTE UK replica → new PRIMARY (manual)
                              other regions re-point WRITER → UK
   ── later: Mumbai back ─► re-sync, demote, re-promote (failback)
```

## How it works / why this approach
There are four classic DR models (RTO = time to recover, RPO = data you can lose, $ = standing cost):
- **Backup & Restore** — restore from snapshots into a new region. Cheapest, RTO hours, RPO = snapshot age.
- **Pilot Light** — core minimal (DB replica, configs) always on in standby; scale up on failover. Low $,
  RTO ~tens of minutes.
- **Warm Standby** — a scaled-down full copy always running; scale it up. Medium $, RTO minutes.
- **Active-Active / Multi-Site** — full capacity live in every region. Most expensive, lowest RTO.

We chose active-active, and we basically get it for free because all 3 regions already run live. So
user-facing failover is mostly automatic: a failed Route 53 health check drops the region, users re-resolve
to the nearest healthy one where reads are already served locally. RTO ≈ DNS TTL (near-zero with Global
Accelerator); RPO = replication lag (sub-second). Velero/snapshots cover the other disaster — data
corruption / bad deploy — which replication faithfully copies and replicas can't undo.

The one manual action is write-primary promotion. If the dead region held the Mumbai primary, writes stop
everywhere (the others are read-only replicas). The DR action is to promote a UK (or SG) replica to primary
and re-point every region's `WRITER` at it. That's the RTO-critical step, so script it. Why active-active
over the others here: warm/pilot/backup are cheaper but trade slower RTO, and we'd be paying for standby
capacity we already run hot. The honest trade-off is that active-active is the costliest model (3× full
stacks + cross-region transfer), justified because it doubles as our scaling and latency story, not just DR.

## How to build it
1. **Confirm the automatic path**: Route 53 health checks per region, low TTL — fail a region, watch users reroute.
2. **Write the promotion runbook**: exact steps/commands to promote a replica → primary and re-point `WRITER`.
3. **Failback procedure**: when Mumbai returns, re-sync it from the temp primary, **demote** the temp, **re-promote** Mumbai.
4. **Wire restore-from-backup**: Velero restore + DB snapshot restore steps, for the corruption case.
5. **DR game-day**: simulate a full region loss, run the runbook, time RTO/RPO, write a **postmortem**, fix gaps.

## Done when
- Failing Mumbai's health check reroutes users to UK/SG automatically; reads keep working there.
- After promoting a replica, writes succeed against the new primary from every surviving region.
- Failback restores Mumbai as primary with no lost/duplicated data, verified by a reconciliation check.
- A Velero/snapshot restore brings a namespace/datastore back from a chosen point in time.

---
> Interview one-liner: *"Because we already run active-active across three regions, region failure is mostly automatic — Route 53 health checks drop the bad region and users reroute to the nearest healthy one where reads are already local — so the only manual, RTO-critical step is promoting a read replica to write primary if Mumbai dies, then failing back when it returns; I keep Velero and DB snapshots as the separate safety net for data corruption, which replication would otherwise just copy."*
