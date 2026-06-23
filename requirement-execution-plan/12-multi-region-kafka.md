# Phase 12 — Multi-region Kafka

## Goal
The async backbone works across regions: events flow and aggregate instead of staying trapped in one
region. Each region runs its own Kafka, and MirrorMaker 2 mirrors the topics that need to be global.

## Why this phase
The services are event-driven, so once you have multiple regions the events have to cross between them —
a UK checkout still needs to reach Mumbai's analytics and inventory.
- **Builds on:** Phase 10 (cross-region DB replication — single write primary, local read replicas).
- **Unlocks:** Phase 17 (cross-region disaster recovery and failover ordering).

## Scope
**In scope:** one Kafka (KRaft) cluster per region; MirrorMaker 2 mirroring the global topic set
(`order.created`) with source-prefixed topic names and offset translation; cross-region consumers
(analytics, reconciliation) reading the prefixed mirrored topics.
**Out of scope:** cross-region disaster recovery / failover ordering (Phase 17); stretched or central
single-cluster Kafka topologies; mirroring of region-local topics such as `thumbnail.requests`.

## What it needs to do
- Each region runs its own Kafka (KRaft, 3 brokers) serving its local producers and consumers.
- `order.created` is mirrored across regions; `thumbnail.requests` stays local-only.
- MM2 writes mirrored topics under a source-cluster prefix (UK's `order.created` lands in Mumbai as
  `uk.order.created`) so consumers can tell local from remote, and loops are impossible.
- Cross-region consumers (analytics, reconciliation) read the prefixed mirrored topics.
- Consumers are idempotent by `order_id`, so a consumer that sees both a local and a mirrored copy
  processes it once.
- Kafka consensus (the KRaft quorum) and the ISR never span a region boundary, so a cross-region network
  partition can't break a quorum.
- MM2 offset translation maps committed offsets source→target, so a consumer can fail over to the mirrored
  topic and resume roughly where it left off — not from zero, not double-counting.

## Architecture

```
                 users ─► Global Router ─► nearest region
          ┌──────────────────────────────┴─────────────────────────────┐
          ▼                                                             ▼
 ┌─ MUMBAI ──────────────────────────────┐     ┌─ LONDON ──────────────────────────────
 │  order ──► ┌──────────────────┐        │     │  order ──► ┌────────────────────┐
 │            │   Kafka (KRaft)  │        │ MM2 │            │   Kafka (KRaft)    │   (NEW: per-region
 │            │   order.created  │◄═══════╪════►│            │  uk.order.created  │    Kafka + MirrorMaker 2)
 │            └────────┬─────────┘ mirror │     │            └─────────┬──────────┘
 │  consumers (idempotent by order_id):   │     │  consumers (idempotent by order_id):
 │   inventory · notification · recommend │     │   inventory · notification · recommend
 └────────────────────────────────────────┘     └────────────────────────────────────────
   DB replication (Step 10) underneath   ·   thumbnail.requests stays region-local
```
**What's new in this step:** each region runs its own Kafka; MirrorMaker 2 mirrors order events across
regions, and consumers stay idempotent by order_id so mirrored duplicates are safe.

## The design
One Kafka cluster per region (autonomous, like everything else), with MM2 mirroring the global topics.
```
   Mumbai (primary)                 UK (eu-west-2)               Singapore (ap-southeast-1)
 ┌──────────────────┐            ┌──────────────────┐            ┌──────────────────┐
 │ Kafka KRaft ×3   │            │ Kafka KRaft ×3   │            │ Kafka KRaft ×3   │
 │ order.created    │            │ order.created    │            │ order.created    │
 │ thumbnail.req    │            │ thumbnail.req    │            │ thumbnail.req    │
 └───┬──────────▲───┘            └───┬──────────▲───┘            └───▲──────────────┘
     │ local    │  MM2               │ local    │  MM2               │  MM2
     ▼ consumers│ (mirror)           ▼ consumers│ (mirror)           │ (mirror in)
 inventory/notif/  └──────────►  inventory/notif/  └──────────►  analytics / inventory
 recommendation     mumbai.order   recommendation     uk.order      reconciliation
 thumbnail-job      .created       thumbnail-job      .created    (reads remote-prefixed topics)
```
MM2 writes mirrored topics under a **source prefix** (e.g. UK's `order.created` lands in Mumbai as
`uk.order.created`), so a consumer can tell local from remote and loops are impossible.

## How it works / why this approach
One Kafka cluster per region (autonomous, like everything else) plus MM2 mirroring the global topics. MM2
is a Kafka Connect job that replicates topics source→target: it copies records, renames topics with a
source-cluster prefix (`<source>.<topic>`) so replication can't loop back on itself, syncs topic
configs/ACLs, and does offset translation — mapping a consumer's committed offset on the source to the
equivalent offset on the target so a consumer can fail over to the mirrored topic and resume roughly where
it left off (not from zero, not double-counting).

Which topics go global, and which stay local:
- **`order.created` is the global one.** Writes are single-primary (Mumbai), but orders can originate in
  any region (a UK user's checkout publishes to UK Kafka). The local consumers
  (`inventory`/`notification`/`recommendation`) handle it in-region; MM2 mirrors it to other regions for
  cross-region needs — analytics/recommendation aggregation and inventory reconciliation against the single
  source-of-truth stock. Idempotency by `order_id` means a consumer seeing both a local and a mirrored copy
  commits once.
- **`thumbnail.requests` stays purely local** — image processing is a regional concern, the
  `thumbnail-job` (KEDA scale-to-zero on consumer lag) runs per region, so there's nothing to mirror.

Why not the alternatives:
- **One stretched Kafka cluster across regions** — brokers in Mumbai/UK/Singapore in a single cluster.
  KRaft quorum and the ISR (in-sync replica set) now span cross-region links; replication latency
  stalls produce acks, flaps the ISR, and a network partition breaks the quorum — exactly the
  partition-tolerance failure mode you're trying to avoid. Don't stretch consensus across regions.
- **One central Kafka** all regions produce/consume against — every event pays cross-region latency and
  the cluster is a single point of failure; lose that region and the whole bus is down.
- **Aggregate cluster** pattern (one region's Kafka is a fan-in target that mirrors *in* from all
  others, e.g. for global analytics) — useful and worth a mention; here Singapore/Mumbai effectively act
  as aggregate consumers for reconciliation. It's a *topology on top of* per-region clusters + MM2, not a
  replacement for them.

## How to build it
1. Confirm each region's **Kafka (KRaft, 3 brokers)** is healthy and serving its **local** producers/consumers.
2. Decide the **global topic set**: `order.created` mirrored; `thumbnail.requests` local-only.
3. Deploy **MM2** (Kafka Connect / Strimzi `KafkaMirrorMaker2`) per source→target pair you need; set the
   **source-prefix** replication policy and enable **offset translation**.
4. Point cross-region consumers (analytics/reconciliation) at the **prefixed** mirrored topics
   (`mumbai.order.created`, `uk.order.created`, …).
5. Verify idempotency holds when a consumer sees both the local and the mirrored copy.

## Done when
- A checkout in UK publishes `order.created` to UK Kafka; UK's local consumers process it, and MM2 lands it
  in Mumbai/Singapore as `uk.order.created` within the expected lag.
- A consumer that gets both a local and a mirrored event commits the order once (idempotent by `order_id`).
- `thumbnail.requests` is not mirrored anywhere; KEDA still scales `thumbnail-job` 0→N→0 locally.
- No replication loop: mirrored topics only ever carry a foreign prefix; no topic mirrors back onto itself.

---
> Interview one-liner: *"Each region runs its own Kafka so consensus and the ISR never cross a region boundary; MirrorMaker 2 mirrors only the global topics like `order.created` with source-prefixed topic names to prevent loops and offset translation for clean failover, while local-only topics like the thumbnail queue stay put — and because every consumer is idempotent by `order_id`, a mirrored duplicate is a safe no-op rather than a stretched-cluster latency and partition problem."*
