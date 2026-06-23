# Phase 16 — Multi-region release

## Goal
Roll a proven service version out across all three regions one at a time, with a gate between each, so a
bad release gets caught in one small region instead of everywhere.

## Why this phase
Once a version is proven safe in one region, it still has to reach Mumbai (`ap-south-1`, primary), UK
(`eu-west-2`), and Singapore (`ap-southeast-1`) — and releasing everywhere at once means a bad version takes
down every region and rollback becomes a global scramble.
- **Builds on:** Phase 15 (the per-service in-region rollout strategy is chosen).
- **Unlocks:** ongoing safe delivery to the whole estate, and the DR confidence that builds on it (Phase 17).

## Scope
**In scope:** the cross-region release order (canary region → … → primary last), manual gates between
regions, exact-SHA rollback, and expand-contract schema handling for mixed-version periods.
**Out of scope:** the per-service in-region rollout mechanics (rolling/canary/blue-green) — that is Phase 15;
automated promotion controllers (GitOps/Argo Rollouts) are deliberately left out.

## What it needs to do
- The same immutable `<svc>-<git-sha>` deploys to every region — no per-region rebuild.
- Deployment follows a fixed order: canary region (Singapore) → UK → primary (Mumbai) last.
- A manual gate between regions reads SLOs / error-budget burn before the next `kubectl apply`.
- A bad release is caught in the low-traffic canary region before it reaches the bigger ones.
- Rollback in any region is re-applying the previous `<svc>-<git-sha>` in that region only.
- DB schema changes are backward-compatible (expand-contract) so mixed versions run against the shared
  global primary during the rollout.

## Architecture

```
   new image  <svc>-<sha>   (already in every region's ECR, Step 14)
        │
        ▼  (1) canary region first  (lowest traffic)
   ┌─ SINGAPORE ─┐   deploy ─► bake & watch SLOs / error budget ─► gate ✔
   └──────┬──────┘
          ▼  (2)
   ┌─ LONDON ────┐   deploy ─► bake & watch SLOs ─► gate ✔
   └──────┬──────┘
          ▼  (3) primary LAST
   ┌─ MUMBAI ────┐   deploy ─► done
   └─────────────┘
   rollback = redeploy the previous <sha>   ·   DB changes must be backward-compatible (expand-contract)
```
**What's new in this step:** a progressive region-by-region rollout — canary region → … → the primary
region last — gated by SLOs, with exact-SHA rollback. (NEW: the cross-region release order.)

## The design
The Phase-15 in-region strategy runs inside each region; this phase just sets the order across regions.

```
        new tag <svc>-<git-sha>  (one artifact, built once)
                       │
   1) CANARY REGION ──► Singapore (ap-southeast-1, lowest traffic)
                       │   kubectl apply --context=sg
                       ▼   ── bake: watch SLOs / error-budget burn ──► GATE
   2) NEXT REGION   ──► UK (eu-west-2)
                       │   kubectl apply --context=uk
                       ▼   ── bake + GATE ──►
   3) PRIMARY LAST  ──► Mumbai (ap-south-1, highest traffic + DB primary)
                           kubectl apply --context=mumbai
   rollback (any region) = re-apply previous <svc>-<git-sha> in that region
```

## How it works / why this approach
You deploy to the canary region (Singapore — least traffic, smallest blast radius), bake while watching that
region's dashboards (SLOs, error-budget burn, latency, 5xx), then promote to UK, then Mumbai. Manual gates
sit between regions: you read the burn-rate before running the next `kubectl apply`. The exact same
`<svc>-<git-sha>` moves region to region — no rebuild, byte-identical — so what you baked in Singapore is
what lands in Mumbai. Rollback is redeploying the previous SHA in the affected region only; immutable tags
make that an exact, known-good restore, not "rebuild and hope."

Schema changes have to be backward-compatible (expand-contract), because during the rollout regions run
mixed versions against the shared global primary (Phase 10): expand (add columns/tables, dual-write) → roll
out all regions → contract (drop the old) only after every region is on the new version.

Why not the alternatives: all-regions-at-once is maximum blast radius — a bad release hits every user
globally and rollback is a global scramble. Primary-first (Mumbai) means validating by hurting your largest
region (and the DB-primary region) first — the worst place to find a regression. GitOps / Argo Rollouts
would automate the promotion and gating, but our model is deliberately manual `kubectl apply` per region
(learning), so the gates are human-read dashboards, not controllers.

## How to build it
1. **Pick the canary region** (Singapore) and the **promotion order** Singapore → UK → Mumbai; document
   it as the release runbook.
2. **Schema first (if any):** ship the **expand** migration (backward-compatible) before deploying app
   code; verify old + new code both work on it.
3. **Deploy canary:** `kubectl apply` the new SHA to **Singapore** (using its Phase-15 strategy).
4. **Bake + gate:** watch Singapore SLOs / burn-rate for the bake window; proceed only if green.
5. **Promote:** same SHA to **UK**, bake + gate; then same SHA to **Mumbai** (primary) last.
6. **Contract:** after all regions are on the new version, run the **contract** migration (drop old
   schema). Keep the previous SHA handy for rollback throughout.

## Done when
- The same `<svc>-<git-sha>` is running in all 3 regions, applied Singapore → UK → Mumbai with a green gate between each.
- A deliberately bad release deployed to the canary is caught by SLO/burn-rate before it reaches UK or Mumbai, and rollback = re-applying the previous SHA in Singapore restores it exactly.
- During the rollout, a request works whether it lands on an old-version region or a new-version region (backward-compatible schema holds against the shared primary).

---
> Interview one-liner: *"I roll a release out region by region, never all at once: the same immutable `<svc>-<git-sha>` goes to the lowest-traffic canary region (Singapore) first, bakes against SLOs and error-budget burn, then promotes through UK to the primary Mumbai last — gates between regions, rollback by redeploying the previous SHA, and expand-contract migrations because regions run mixed versions against the shared primary mid-rollout. Argo Rollouts would automate it, but here the promotion is deliberately manual kubectl per region."*
