# Phase 20 — Data storage & cold tiering

## Goal
Stop the operational database from carrying years of old data. Match each piece of data to a **storage tier by how often it's read and how old it is** — hot in the DB, warm in replicas/indexes, cold/archive in S3 — so the DB stays fast and cheap while old data is retained cheaply.

## Why this phase
Keeping everything hot in the database is expensive and slow — bigger tables, slower scans, longer backups, and you pay SSD prices for data nobody reads. Tiering keeps the hot path fast and gives you cheap long-term retention for compliance.
- **Builds on:** Phase 10 (the data layer whose old records you tier off; hot data lives here).
- **Unlocks:** retention/compliance without bloating the primary database.

## Scope
**In scope:** three storage tiers (hot Postgres, warm replicas/Elasticsearch, cold S3 + Glacier); S3 archive buckets with SSE-KMS + Object Lock where compliance requires it; S3 lifecycle rules; wiring archive producers (order/event archive jobs, Velero/RDS snapshots, Thanos, Loki) at their S3 backends.
**Out of scope:** the hot DB tier build itself (Phase 10); DR/backup schedule definition (Phase 17, which this consumes).

## What it needs to do
- Hot data (recent orders/users/inventory) stays on SSD-backed Postgres/Aurora.
- Warm data (read replicas, Elasticsearch indexes, recent logs/metrics) stays queryable and online, but cheaper than the primary.
- Cold/archive data (old orders/event history, audit logs, all backups) lands in **S3**, with **lifecycle policies** auto-transitioning objects by age (Standard → Standard-IA → Glacier → Deep Archive).
- Platform data routes to S3: Velero & RDS snapshots, Thanos long-term metrics, Loki long-term logs.
- An archive job ages old orders/events out of Postgres/Kafka into S3.
- Storage cost tracks access frequency — Standard-IA/Glacier are a small fraction of hot-DB cost.
- Moving cold data off the DB keeps the working set small, so queries and backups/restores stay fast.
- Cold data is encrypted (SSE-KMS) and access-controlled — it's often the most sensitive (audit/financial history); use S3 Object Lock for compliance retention.
- Storage class is matched to access pattern, not just age — live reads never sit in Glacier (minutes-to-hours retrieval).

## Architecture

```
   HOT   ┌───────────────────────────────┐   live, fast, expensive
         │  Postgres  (primary + replicas)│   recent orders / users / inventory
         └──────────────┬─────────────────┘
   WARM  ┌──────────────▼─────────────────┐   recent-but-not-live
         │  read replicas · Elasticsearch  │   recent logs / metrics
         └──────────────┬─────────────────┘
   COLD  ┌──────────────▼──────────────────────────────────┐   (NEW)
         │  Amazon S3  ──lifecycle──►  S3 Standard-IA        │   old orders, audit logs, backups,
         │             ──►  Glacier  ──►  Glacier Deep Archive│   Velero/DB snapshots, Thanos/Loki long-term
         └──────────────────────────────────────────────────┘
   match the storage class to access frequency  ·  colder = cheaper but slower to retrieve
```
**What's new in this step:** tiered storage — hot DB → warm replicas/ES → cold S3 + Glacier via lifecycle
rules, so old data is retained cheaply without bloating the operational database.

## The design
Three tiers, by access frequency × age. Cold lives in S3; lifecycle policies move it down automatically.
```
  HOT  (ms, expensive)   Postgres primary + Multi-AZ standby  — live orders, users, inventory
        │
  WARM (fast, cheaper)   read replicas · Elasticsearch · recent logs/metrics (Loki/Thanos)
        │  age / access drops
  COLD / ARCHIVE (cheap, slow)        Amazon S3
        └─►  S3 Standard ──30/90d──► S3 Standard-IA ──1yr──► Glacier ──3yr──► Glacier Deep Archive
             • old orders/event history archived from Postgres/Kafka
             • DB backups & snapshots (Velero, RDS)   • long-term logs (Loki S3) / metrics (Thanos S3)
```

## How it works / why this approach
- **HOT — in the DB.** Recent orders, users, inventory: SSD-backed Postgres/Aurora. Fast and expensive; keep it small so queries and backups stay quick.
- **WARM — recent-but-not-live.** Local read replicas (Phase 10), Elasticsearch search indexes, and recent logs/metrics. Queryable, cheaper than the primary, still online.
- **COLD / ARCHIVE — S3 + Glacier.** Old order/event history (aged out of Postgres/Kafka), audit logs, and all backups land in S3. S3 lifecycle policies auto-transition objects by age: e.g. orders >1yr → Standard-IA, >3yr → Glacier; logs >30d → Glacier. Retention/compliance drives the exact schedule.
- **Where the platform data goes:** Velero & RDS snapshots → S3; long-term metrics → Thanos S3 store; long-term logs → Loki S3 backend. The observability stack keeps only a hot window locally and ships the rest to S3 — same tiering idea, applied to telemetry.
- **Archived ≠ unprotected:** cold data still needs encryption (SSE-KMS) and access control — it's often the most sensitive (audit/financial history). Use S3 Object Lock for compliance retention.

Why tier? Storage cost tracks access frequency. Standard-IA/Glacier are a small fraction of hot-DB cost, so moving cold data off the DB shrinks spend and keeps the DB fast (smaller working set, faster backups/restores).

Why not keep everything hot in the DB? It's expensive and it slows the DB — bigger tables, slower scans, longer backups, and you pay SSD prices for data nobody reads.

Why not put live data in Glacier to save money? Glacier retrieval is minutes to hours — fine for archives and audits, fatal for a live read. Match the storage class to the access pattern, not just to age.

## How to build it
1. **Define tiers & retention** per dataset (orders, audit, logs, metrics, backups) — ties to Phase-10 RPO.
2. **Create the archive S3 buckets** with SSE-KMS encryption + bucket policy/IAM; Object Lock where
   compliance requires it.
3. **Add S3 lifecycle rules**: Standard → Standard-IA → Glacier → Deep Archive on the age thresholds above.
4. **Wire the producers**: an archive job ages old orders/events out of Postgres/Kafka into S3; point
   **Velero/RDS snapshots, Thanos, and Loki** at their S3 buckets.
5. **Test a cold read** — restore/retrieve one archived object and confirm the retrieval-time expectation.

## Done when
- A >1yr order is **gone from the hot DB** but retrievable from S3 (Standard-IA), and a >3yr one sits in Glacier.
- Velero backups, Thanos blocks, and Loki chunks are visible in their S3 buckets and **encrypted**.
- A lifecycle transition has actually fired (object's storage class changed) and a Glacier retrieval completes within its expected (minutes-hours) window.

---
> Interview one-liner: *"I tier data by access pattern and age: hot live data in Postgres, warm data in read replicas and
> Elasticsearch, and cold/archive — old orders, audit logs, all backups, plus long-term Loki logs and
> Thanos metrics — in S3 with lifecycle rules that move it Standard → IA → Glacier → Deep Archive on age.
> Keeping everything hot is expensive and slows the DB; Glacier is cheap but has minutes-to-hours
> retrieval, so I match storage class to access frequency and keep the archive encrypted and access-controlled."*
