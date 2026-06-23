# Phase 22 — Global observability

## Goal
With three regions live, get **one global view across all of them** — metrics, logs, traces, and SLOs spanning Mumbai + UK + Singapore — without giving up the per-region observability each cluster already has.

## Why this phase
This is the end state: operating the whole global estate from a single pane of glass. One Grafana, one "is checkout healthy everywhere?" question, plus cheap long-term retention — while every region keeps scraping itself locally.
- **Builds on:** Phase 07 (per-region observability) and Phase 21 (all three regions exist).
- **Unlocks:** running the entire global estate from one Grafana.

## Scope
**In scope:** Thanos sidecars on each region's Prometheus shipping to S3; a central Thanos Querier (dedup) + Thanos Store; one Grafana spanning all regions; cross-region log query via Loki on S3; cross-region trace stitching via Tempo; per-region AND global burn-rate SLOs with Alertmanager routing by severity/region.
**Out of scope:** the per-region observability stack build itself (Phase 07); adding regions (Phase 21).

## What it needs to do
- Each region's Prometheus keeps scraping **locally**; a **Thanos sidecar** exposes it to a central Querier and ships sealed TSDB blocks to **S3**.
- A central **Thanos Querier** fans out to all regions and **dedups** overlapping series into one global answer; **Thanos Store** serves long-term blocks back from S3.
- One **Grafana** points at the Querier so dashboards are global by default and drill per-region.
- Each region's **Loki** uses an **S3 backend** with cross-region query; each region's **Tempo** holds local spans stitched end-to-end by a shared W3C `trace_id`.
- Burn-rate SLOs are computed **per-region** and **globally**; **Alertmanager** routes by severity + region.
- Scraping stays local per region, avoiding cross-region scrape latency.
- No single central Prometheus SPOF; a region's failure doesn't blind the others.
- Long-term metrics/logs live cheaply on S3 via Thanos Store / Loki S3 backend.
- Overlapping series are de-duplicated so global numbers aren't doubled.

## Architecture

```
 ┌─ MUMBAI ──────┐    ┌─ LONDON ──────┐    ┌─ SINGAPORE ───┐
 │ Prometheus    │    │ Prometheus    │    │ Prometheus    │    scrape LOCALLY (low latency)
 │  + Thanos     │    │  + Thanos     │    │  + Thanos     │
 │    sidecar    │    │    sidecar    │    │    sidecar    │
 └──────┬────────┘    └──────┬────────┘    └──────┬────────┘
        └────────────────────┼─────────────────────┘
                             ▼
              ┌──────────────────────────────┐
              │   Thanos Querier (global)    │   dedup across regions      (NEW)
              │   Thanos Store ◄─► S3        │   long-term metrics
              └──────────────┬───────────────┘
                             ▼
                  ┌────────────────────────┐
                  │     ONE  Grafana       │   dashboards span all regions + per-region; SLOs global+local
                  └────────────────────────┘
   logs: per-region Loki ─► S3 backend (query across)   ·   traces: per-region Tempo (trace_id stitches)
```
**What's new in this step:** per-region Prometheus + Thanos rolled up into one global, de-duplicated view
(one Grafana for all regions) with long-term metrics in S3 — without giving up local scraping.

## The design
Keep Prometheus scraping **local** in each region; add a global query layer on top that **dedups** into one view.
```
   Mumbai            UK              Singapore
 Prometheus       Prometheus       Prometheus      ◄─ scrape LOCAL (low latency), unchanged
 +Thanos sidecar  +Thanos sidecar  +Thanos sidecar ─► S3 (per region) ─┐ long-term blocks
      └─────────────────┬───────────────┘                               │
                       ▼  fan-out + DEDUP                               ▼
                 Thanos Querier ◄──────────────────────────────── Thanos Store (reads S3)
                       │
                    Grafana (ONE)  → dashboards span all 3 regions AND drill per-region
 Loki/region → S3 (query across regions) · Tempo/region (stitch a trace_id end-to-end)
 Alertmanager: SLOs computed per-region AND global; routes by severity/region
```

## How it works / why this approach
- **Metrics — local scrape, global query.** Each Prometheus keeps scraping its own region (no cross-region scrape latency). A Thanos sidecar on each exposes that Prometheus to a central Thanos Querier, which fans out to all regions and dedups overlapping series into one global answer. The sidecar also ships sealed TSDB blocks to S3; Thanos Store serves those back for long-term queries. One Grafana points at the Querier → every dashboard is global by default and drills to a single region.
- **Logs.** Each region's Loki uses an S3 backend; queries fan across regions (or ship to a central index). **Traces.** Each region's Tempo holds local spans, but because a cross-region request carries one W3C `trace_id`, that id stitches the request end-to-end across regions.
- **SLOs/alerts.** Burn-rate SLOs are computed per-region and globally (global checkout availability across all users). Alertmanager routes by severity + region so the right on-call gets it.

Why Thanos and not the alternatives? A single central Prometheus scraping all regions means cross-region scrape latency, huge inter-region data transfer, and a SPOF. Prometheus federation can roll up a few aggregates but doesn't scale to full global query or long-term retention. Thanos gives global query + dedup + cheap long-term on S3 while scraping stays local. (Equivalent paths: Grafana Mimir, Cortex, or managed Amazon Managed Prometheus (AMP) — same model, different operator burden.)

This is the global rollup of the per-region observability from Phase 07 — that section already noted "Thanos sidecar for global view later"; this is that later.

## How to build it
1. Add a **Thanos sidecar** to each region's Prometheus; give each an **S3 bucket** + IRSA to write blocks.
2. Deploy **Thanos Querier** centrally, wired to all 3 sidecar endpoints (Store API), with **dedup** on.
3. Deploy **Thanos Store** (reads the S3 blocks) so long-term history is queryable.
4. Point **one Grafana** at the Querier; build dashboards that are **global** and **per-region** (template var).
5. Put each **Loki** on **S3** and enable **cross-region log query**; confirm Tempo stitches a cross-region `trace_id`.
6. Define **global** burn-rate SLOs alongside the per-region ones; route Alertmanager by **severity/region**.

## Done when
- One Grafana dashboard shows **checkout availability + p95 across all 3 regions** and drills into any single region.
- A series that exists in two regions shows up **once** (dedup), not doubled.
- A **cross-region request** (UK read → Mumbai write) is followed end-to-end by its `trace_id`; its logs are findable globally.
- A query for data **older than local Prometheus retention** still returns (served from Thanos Store + S3).
- A **global SLO breach** fires once, routed to the right region's on-call; per-region alerts still fire independently.

---
> Interview one-liner: *"I keep Prometheus scraping locally in every region and put a Thanos sidecar on each shipping to S3; a central Thanos Querier fans out and dedups all regions into one global view in a single Grafana, with Thanos Store for long-term — so I get global query plus cheap retention without a central Prometheus SPOF or the cross-region scrape latency and data transfer that federation can't escape; Loki on S3 and per-region Tempo let me stitch a trace_id across regions, and SLOs run both per-region and global."*
