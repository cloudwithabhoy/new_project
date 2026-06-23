# Phase 15 — Deployment strategy

## Goal
Give each service a safe way to ship a new version inside one region, with a clear rollback move
for every approach.

## Why this phase
Every change ships as a new version that has to replace the running one without disrupting customers,
and different changes carry different risk — so you need a few rollout styles and a rule for picking each.
- **Builds on:** Phase 08 (Istio in the region with weighted traffic-splits already in the design).
- **Unlocks:** Phase 16 (the multi-region release that coordinates these per-region rollouts).

## Scope
**In scope:** per-service rollout strategies inside one region — rolling update (the default), an Istio
weighted canary for risky/customer-facing changes, blue-green where instant rollback matters; plus the
rollback move for each.
**Out of scope:** coordinating a release *across* regions (order, gates, halt-on-regression) — that is
Phase 16.

## What it needs to do
- Every Deployment does a rolling update by default, gated by readiness probes so no requests drop.
- Risky/customer-facing services can run an Istio canary: v2 as a second subset, traffic shifted by
  VirtualService weights `95/5 → 50/50 → 100`.
- A/B routing (e.g. `recommendation` by `VARIANT` header) uses the same mesh mechanism, routing by header
  instead of weight.
- Services that need instant rollback can run blue-green: two Deployments (`-blue`/`-green`) with a
  one-flip cutover.
- A canary's blast radius stays small and gets judged against RED metrics / SLOs at each step; a bad signal
  reverts the weight to 0.
- Each mode has a written-down rollback move.

## Architecture

```
 ┌─ rolling out one service's new version, inside one region ─────────────────
 │
 │  ROLLING (default)   v1 ■■■  ─►  v1■ v2□  ─►  v2□□□        k8s maxSurge / maxUnavailable
 │
 │  CANARY (Istio)      VirtualService weights:                (NEW: mesh traffic-split)
 │       v1 ──95%──┐
 │       v2 ── 5%──┴─► judge by RED metrics / SLO ─► 50/50 ─► 100% v2
 │
 │  BLUE-GREEN          blue(v1) live   │   green(v2) staged ─► flip ─► instant rollback (2× resources)
 └────────────────────────────────────────────────────────────────────────────
   default = rolling   ·   risky / customer-facing = Istio canary   ·   need instant rollback = blue-green
```
**What's new in this step:** per-service rollout strategies — rolling by default, Istio-weighted canary
for risky changes, blue-green where instant rollback matters (this is *within* one region; across
regions is Step 16).

## The design
You pick the rollout per service and per change; the same `<svc>` Service stays in front, and only the subset/Deployment behind it changes.

```
ROLLING (default)        CANARY (Istio, risky)         BLUE-GREEN (instant rollback)
 Deployment <svc>         DestinationRule: v1 | v2       Deploy <svc>-blue (live)
  maxSurge / maxUnavail   VirtualService weights:        Deploy <svc>-green (new, idle)
  k8s swaps pods 1-by-1    95/5 ─► 50/50 ─► 100          flip Service/VS selector ─► green
  (briefly mixed v1+v2)    judged by RED/SLOs            rollback = flip back to blue
                           e.g. catalog v2, reco A/B     (~2× pods during the switch)
```

## How it works / why this approach
Three rollout modes, chosen per service/change:

- **Rolling update** *(the k8s default — most services).* The Deployment replaces pods incrementally under
  `maxSurge`/`maxUnavailable`; readiness probes gate each new pod. Simple, no extra objects, no double cost.
- **Canary via Istio** *(risky changes — `catalog` v2, `recommendation` A/B).* Deploy v2 as a second subset
  (`DestinationRule` `subsets: v1,v2`), then shift the `VirtualService` weights `95/5 → 50/50 → 100`,
  watching v2's error rate / latency against SLOs at each step. Bad signal → set weight back to 0. A/B is the
  same mechanism, routing by header instead of weight.
- **Blue-green** *(instant rollback required).* Two full Deployments — blue live, green new and idle — and
  you flip the Service selector (or VirtualService destination) to green in one move; rollback is flipping
  back. Instant, but ~2× resources while both run.

Why this mix and not the alternatives: rolling is the simplest and the right default, but it briefly serves
mixed versions and rolls back slower. Canary gives the safest gradual exposure and keeps the blast radius to
a few percent of users — but it's only as good as the metrics judging it, so it needs SLOs. Blue-green gives
the fastest switch and rollback but doubles resources during the cutover and shifts 100% at once (no gradual
bake). So: rolling everywhere by default, Istio canary for risky services, blue-green only where a one-flip
rollback is worth the cost. This is all per-region, per-service; coordinating a release across regions is
Phase 16.

## How to build it
1. **Default rolling** on every Deployment: set sane `maxSurge`/`maxUnavailable` + readiness probes; deploy by bumping the image tag to the new `<svc>-<sha>` and `kubectl apply`.
2. **Canary plumbing** for risky services: `DestinationRule` subsets `v1/v2` + a `VirtualService` with weights; deploy v2 at `5%`, then promote `5 → 50 → 100` as RED/SLOs stay green (roll back to `0` if not).
3. **Blue-green** for the services that need instant rollback: two Deployments (`-blue`/`-green`), flip the Service/`VirtualService` selector to cut over, keep blue warm until green is proven, then retire blue.
4. **Document the rollback move** for each mode (rolling: re-apply previous tag; canary: weight→0; blue-green: flip back).

## Done when
- A routine service ships via rolling update with no dropped requests (readiness gating works).
- `catalog` v2 takes 5% of traffic, you watch its SLOs, then promote to 100% — and a forced bad v2 is pulled by setting its weight back to 0.
- A blue-green service cuts over by one flip and rolls back instantly by flipping back to blue.

---
> Interview one-liner: *"Per service in a region I pick the rollout to fit the risk: rolling update as the default (simple, but briefly mixed versions), an Istio canary for risky changes like catalog v2 — deploy v2 as a subset and shift VirtualService weights 5→50→100 judged by RED SLOs — and blue-green where I need a one-flip instant rollback at the cost of double resources; coordinating the release across regions is the next step."*
