# Phase 09 — Add a second region + global routing

## Goal
Stand up a second, independent region (London / `eu-west-2`) that runs the whole stack, and put a global
router in front of both so each user is served by their nearest healthy region — with automatic failover.

## Why this phase
One proven region is the template, not the finish line. To serve users close to them and to survive a
whole-region outage, you copy the proven region into a second one and route each user to the nearest
healthy region — UK users to UK, India users to Mumbai. Every later cross-region piece needs a second
region to exist first.
- **Builds on:** Phase 08 (one fully-working, proven single region with its manifests in Git, so a second
  region is "re-apply in a new cluster").
- **Unlocks:** Phase 10 (cross-region database replication).

## Scope
**In scope:** a second self-contained regional stack (own ALB, EKS cluster, datastores, observability); a
global router (Route 53 latency/geo routing with per-region health checks, or AWS Global Accelerator);
health-checked failover.
**Out of scope:** cross-region data replication (Phase 10); session portability across regions
(Phase 11); the Kafka event backbone across regions (Phase 12); promote-on-failover DR (Phase 17); a
third region (Phase 21).

## What it needs to do
- A second region (London / `eu-west-2`) runs the entire single-region stack on its own: own ALB, EKS
  cluster, Istio gateway, all 12 services, and datastores (Postgres ×7, Redis, ES, Kafka).
- A global router gives one DNS name and picks a region per user by latency (or by geography).
- Each region has its own health checks.
- An unhealthy region drops out of the routing answers, so its users re-route to the next-nearest healthy
  region.
- Istio stays inside each region; the global layer only chooses which region serves a request.
- Each region can fail without dragging the others down — regions are independent (active-active).
- Each user is served by their nearest healthy region, so latency stays low.
- Failover is automatic on a health-check failure. With Route 53 it's bounded by the DNS TTL (60s); with
  Global Accelerator it's near-instant over the AWS backbone.
- Both regions deploy from the same manifests with only per-region values changing, so they don't drift
  apart.

## Architecture

```
                                  ┌────────────────────────────────┐
             users worldwide ────►│         GLOBAL  ROUTER         │   (NEW)
                                  │   Route 53 latency / geo +      │
                                  │   per-region health checks      │   (or AWS Global Accelerator)
                                  └────────┬─────────────────┬──────┘
                   nearest = India / APAC  │      nearest = Europe  │   (NEW: UK region)
                                           ▼                        ▼
 ┌─ MUMBAI · ap-south-1 ───────────────────┐   ┌─ LONDON · eu-west-2 ────────────────────  (NEW)
 │  ALB ─► Istio GW ─► api-gateway          │   │  ALB ─► Istio GW ─► api-gateway
 │   ├─ auth · user · catalog · search      │   │   ├─ auth · user · catalog · search
 │   ├─ cart · order · payment · inventory  │   │   ├─ cart · order · payment · inventory
 │   └─ notification · recommendation · ui  │   │   └─ notification · recommendation · ui
 │  data: Postgres ×7 · Redis · ES · Kafka  │   │  data: Postgres ×7 · Redis · ES · Kafka
 │  multi-AZ · autoscaling · observability  │   │  multi-AZ · autoscaling · observability
 └──────────────────────────────────────────┘   └─────────────────────────────────────────
        full single-region stack, autonomous                 an independent copy of the same stack
```
**What's new in this step:** a second autonomous region (London) + a global router that sends each user
to the nearest *healthy* region, with health-checked failover.

## The design
Two independent regional stacks sit behind a global router that only decides which region handles a
request.
```
              users worldwide
                    │
        ┌───────────▼───────────┐  GLOBAL ROUTER
        │ Route 53 latency/geo   │  + health checks ── drops unhealthy region ─► failover
        │ (or AWS Global Acc.)   │
        └─────┬───────────┬──────┘
       nearest=IN│  nearest=UK│
              ▼            ▼
        ALB(Mumbai)   ALB(London)
         Istio GW      Istio GW
        api-gateway   api-gateway      (each region serves on its own)
         full stack    full stack
```

## How it works / why this approach
Each region is the whole single-region stack — its own ALB, EKS cluster, datastores, and observability —
and a global router sits above both.

You have two solid options for the global router. **Route 53 latency-based routing + health checks** is
the default: one DNS name with a record per region; Route 53 hands back the lowest-latency healthy region,
and a failed health check pulls that region from the answers so users re-resolve to the next-nearest one.
(Geolocation routing instead picks by country.) **AWS Global Accelerator** is the upgrade: two anycast
IPs; users hit the nearest AWS edge and ride the AWS backbone to the nearest healthy region, which gives
near-instant failover (no DNS TTL) and better latency.

Independent regional stacks beat one stretched mesh because keeping each region self-contained means a
region can fail without taking the others with it, it's simpler, and it's how most teams run active-active.
Istio stays inside each region; the global layer just picks the region. A single global ALB isn't an
option — an ALB/NLB is regional and can't span regions, so you need a per-region LB plus a global layer
above them. CloudFront is an edge CDN for assets and caching, not a region-picker for dynamic API traffic.

This phase gives you routing plus a running stack, but the new region can't serve real reads/writes until
its data shows up — that's Phase 10 (DB replication) and Phase 12 (Kafka).

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/9.1-provision-second-region.md`](../step-by-step-implementation/9.1-provision-second-region.md) —
  stand up `ecom-cluster-uk` in `eu-west-2` (cluster + OIDC + node group), the same add-ons (ALB Controller, ExternalDNS, Karpenter), and namespaces + quotas — re-running 2.1/2.2/2.3 with UK values. *(deep-dive: a region as the unit of replication)*
- [`step-by-step-implementation/9.2-deploy-the-stack.md`](../step-by-step-implementation/9.2-deploy-the-stack.md) —
  switch kubectl context to UK and re-apply the same datastore / fleet / Istio manifests with per-region values only; confirm a UK-local checkout. *(deep-dive: shared manifests + per-region values; drift is the enemy)*
- [`step-by-step-implementation/9.3-global-router-and-failover.md`](../step-by-step-implementation/9.3-global-router-and-failover.md) —
  Route 53 latency routing over both regional ALBs + per-region health checks (60s TTL), with Global Accelerator as the instant-failover upgrade; verify near-region routing and health-checked failover. *(deep-dive: global traffic management; Istio stays inside each region)*

## Done when
- A request from the UK lands on the UK stack; a request from India lands on Mumbai (checked via response
  headers / region label).
- Failing a region's health check (or stopping its ingress) automatically routes users to the other
  region.

---
> Interview one-liner: *"Each region runs the full stack independently; a global layer — Route 53 latency routing with health
> checks, or Global Accelerator for instant backbone failover — sends each user to the nearest healthy
> region. ALBs are regional, so there's no single global LB; the global layer sits above the per-region
> LBs, and I keep Istio inside each region rather than stretching one mesh across them."*
