# Phase 10 — Cross-region database replication

## Goal
Give the second region real data: every region writes to one global primary (consistent), and each region
reads from a local replica (fast).

## Why this phase
A second region is useless until its data is there too. The London stack from Phase 09 is running, but it
has no local data to read from and no consistent path for writes. Here you make its data real: a UK user
reads from a UK-local replica while writes flow to the single Mumbai primary, which replicates out to every
region automatically.
- **Builds on:** Phase 09 (a second region running behind global routing, but with no local data yet —
  that's what this phase fixes).
- **Unlocks:** Phase 11 (session management) and later read-scaling and promote-on-failover DR.

## Scope
**In scope:** a UK read replica replicating from the Mumbai primary; app read/write split (writer and
reader endpoints); read-after-write handling on critical paths; lag and failover-readiness validation.
**Out of scope:** multi-master / write-anywhere replication; actual primary promotion / failover
(Phase 17 DR); a Singapore replica (Phase 21); the Kafka backbone (Phase 12).

## What it needs to do
- A read replica runs in the UK region, replicating from the Mumbai primary.
- All writes from any region go to the single Mumbai primary.
- Each region's reads go to its local replica.
- The primary streams its WAL to every regional replica continuously.
- Reads that must be fresh right after a write are served from the primary (e.g. show the order you just
  placed), or a user is pinned to the primary briefly after a write.
- One write primary means strong write consistency with no conflict resolution to worry about.
- Reads are served locally per region; far-region writes pay cross-region latency by design.
- Async streaming keeps lag small (near-real-time); the read-after-write case is handled on critical paths.
- The primary is a write single-point-of-failure, softened by an in-AZ Multi-AZ sync standby and fast
  promotion (Phase 17 DR).

## Architecture

```
                 users ─► Global Router ─► nearest healthy region
          ┌─────────────────────────────────┴────────────────────────────┐
          ▼                                                               ▼
 ┌─ MUMBAI · ap-south-1  (PRIMARY) ───────────────┐   ┌─ LONDON · eu-west-2 ────────────────────
 │                                                │   │
 │  app ──WRITE──►  ┌────────────────────┐         │   │  app ──READ──►  ┌──────────────────┐
 │  app ──READ───►  │  PRIMARY Postgres  │         │   │                 │  UK read replica │   (NEW)
 │                  │  (single master)   │──WAL────┼───┼───────────────► │  (applies WAL)   │
 │                  └────────────────────┘  stream │   │  app ──WRITE──────────► (to Mumbai) │
 └─────────────────────────────────────────────────┘   └──────────────────────────────────────
        every region's WRITES go to the one Mumbai primary;  each region READS its local replica
        (NEW: cross-region streaming replication — WAL)
```
**What's new in this step:** cross-region database replication — all writes go to the single Mumbai
primary, every region reads from its *local* replica, and the primary streams changes (WAL) out to them.

## The design
One global primary takes every write and streams its changes out to a read replica in each region.
```
            ┌────────── Mumbai (primary region) ──────────┐
  writes ──►│  PRIMARY ──WAL stream──► local read replica │
            └─────┬──────────────────┬─────────────────────┘
        cross-region stream          │
              ▼                       ▼
       UK read replica         (Singapore read replica, Step 21)
       UK app READS here  ◄── local, fast
       UK app WRITES ───────────────────────────► back to Mumbai PRIMARY
```

## How it works / why this approach
The model is one write primary plus read replicas — active-active for reads, single-primary for writes.

**Replication.** The primary streams its WAL (write-ahead log) to the replicas, which keep replaying it as
near-real-time read-only copies. Async streaming gives tiny lag and the best performance, with eventual
consistency on the replicas; sync (used for the in-AZ standby) gives zero data loss but slower writes. The
learn-it path is self-managed Postgres streaming/logical replication to a replica in each region. The
managed path is Aurora Global Database — storage-level cross-region replication, usually under a second of
lag, with fast secondary-region promotion.

**App wiring — read/write split.** Two connection targets: `WRITER` points at the primary (all writes),
`READER` points at the local region's replica(s) (all reads).

**Why one write primary (chosen).** It's simple, gives strong write consistency, needs no conflict
resolution, and it scales the part that actually needs scaling — reads are roughly 90% of e-commerce
traffic. The costs: far-region writes pay cross-region latency, and the primary is a write
single-point-of-failure (softened by a Multi-AZ standby + fast promotion → Phase 17 DR).

**Why not multi-master / write-anywhere** (Aurora multi-master, CockroachDB, Cassandra, DynamoDB global
tables): you get low-latency writes everywhere, but you take on conflict resolution and weaker write
consistency — a lot more complexity. That's the "true active-active writes" upgrade, left out on purpose.

**Read-after-write caveat.** Async replicas lag, so a read right after a write can miss it. The fix: send
read-after-write-critical reads to the primary (e.g. show the order you just placed from the primary), or
pin a user to the primary briefly after a write.

## How to build it
1. Stand up a **read replica in the UK region**, replicating from the Mumbai primary.
2. Point the UK app's **reads at the local replica**, **writes at the Mumbai primary** (two endpoints /
   connection strings, or a managed reader endpoint).
3. Handle **read-after-write** on the critical paths (read those from the primary).
4. Validate lag + failover-readiness (the actual promotion is the DR step, 10).

## Done when
- A write in Mumbai appears in the UK replica within the expected lag.
- The UK app reads locally (low latency) and writes to Mumbai successfully.
- A "place order then view it" flow is consistent (served from the primary on the read-after-write path).

---
> Interview one-liner: *"Single global write primary, read replicas in every region. The primary streams WAL to the replicas;
> the app does a read/write split — writes to the writer endpoint, reads to the local reader endpoint. It
> gives strong write consistency and cheap read scaling; the costs are cross-region write latency and
> read-after-write lag, which I handle by reading critical-path data from the primary. Write-anywhere would
> need multi-master with conflict resolution, which I deliberately avoid."*
