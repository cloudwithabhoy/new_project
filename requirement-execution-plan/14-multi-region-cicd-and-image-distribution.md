# Phase 14 — Multi-region CI/CD & Image Distribution

## Goal
The same image reaches every region efficiently and identically. The platform builds each service image
once, distributes that exact image to every region's ECR, and deploys the same git SHA everywhere, so all
three regions run a byte-identical artifact.

## Why this phase
With multiple regions to deploy to, you don't want each region rebuilding the same code — that wastes time
and risks drift. Build once, copy the bits everywhere, deploy the same SHA.
- **Builds on:** Phase 09 (the second and third regions provisioned, each with its own ECR).
- **Unlocks:** Phase 16 (a deterministic, same-SHA release across regions).

## Scope
**In scope:** an ECR `ecom-repo` per region with immutable tags; registry-level cross-region replication
from the primary region; manual `kubectl` deploy per region pinned to the same `<svc>-<sha>`.
**Out of scope:** GitOps; ordering the rollout across regions / canary one region first (Phase 16);
per-region rebuilds; cross-region pulls at runtime.

## What it needs to do
- Each region has an ECR repo `ecom-repo` set to immutable tags, so a `<svc>-<sha>` can never be
  overwritten.
- The pipeline builds one image and tags it `ecom-repo:<svc>-<git-sha>` (the git SHA is the version, never
  `:latest`).
- Registry replication rules in the primary region (`ap-south-1`) automatically copy every pushed image to
  `eu-west-2` and `ap-southeast-1`.
- Jenkins keeps pushing to the primary region only; replication does the fan-out.
- Deploy is manual per region: target each cluster context and `kubectl set image`/`apply` the same
  `<svc>-<sha>`.
- The same SHA that ran in Mumbai is the exact bits rolled to UK and Singapore — no per-region rebuild, no
  drift. A rollback is just "redeploy the previous SHA."
- Each region's nodes pull from their local ECR — low latency, lower data-transfer cost — and pulls keep
  working even if another region is down.

## Architecture

```
   git push ─► ┌──────────┐ ─► docker build ─► ┌───────────────────────────┐
               │ Jenkins  │                     │  ECR (Mumbai)  ecom-repo  │  source registry
               └──────────┘                     │  tag: <svc>-<git-sha>     │
                                                └─────────────┬─────────────┘
                              (NEW)  ECR cross-region replication
                          ┌───────────────────────┴───────────────────────┐
                          ▼                                                ▼
                ┌──────────────────┐                            ┌──────────────────┐
                │  ECR (London)    │                            │  ECR (Singapore) │   each region
                └────────┬─────────┘                            └────────┬─────────┘   pulls LOCALLY
                         ▼                                               ▼
            you: kubectl apply  <same sha>  ─► UK cluster    …    ─► SG cluster
```
**What's new in this step:** build ONCE → one immutable `<svc>-<sha>` image, distributed by ECR
cross-region replication so every region pulls locally and runs the identical artifact.

## The design
One build → one immutable image → registry-level fan-out → manual deploy per region.
```
git push ─► Jenkins (one Pipeline job per service)
                │ builds ONCE
                ▼
        ECR ecom-repo (ap-south-1, PRIMARY registry)
        push ecom-repo:<svc>-<sha>   (immutable tag, no :latest)
                │
   ECR cross-region REPLICATION (registry rules) — automatic copy
        ├──────────────► ECR ecom-repo (eu-west-2)
        └──────────────► ECR ecom-repo (ap-southeast-1)
                │ nodes pull LOCALLY in each region
                ▼
   you: kubectl --context <region> set image / apply  ← same <svc>-<sha> everywhere (MANUAL)
```

## How it works / why this approach
One build → one immutable image → registry-level fan-out → manual deploy per region.

- **Build once, deploy many.** The pipeline builds **one** image and tags it `ecom-repo:<svc>-<sha>`.
  The git SHA *is* the version, so the **same SHA** that ran in Mumbai is the exact bits you roll to UK
  and Singapore — no per-region rebuild, no chance of drift. Immutable tags (never `:latest`) mean a SHA
  always means the same image, so a rollback is just "redeploy the previous SHA."
- **Image distribution = ECR cross-region replication.** You set **registry replication rules** once in
  the primary region; ECR then **automatically copies** every pushed image to each region's ECR. Jenkins
  still pushes to **one** registry — replication does the fan-out. Each region's nodes then **pull from
  their local ECR**: low latency, lower data-transfer cost, and pulls keep working **even if another
  region is down** (no runtime dependency on the primary).
- **Deploy stays manual.** CI/CD ends at "image is in every region's ECR." You deploy by hand, per region
  — target each cluster context and `kubectl set image`/`apply` the **same** `<svc>-<sha>`. (No GitOps.
  *Ordering* the rollout across regions — canary one region first — is **Step 16**.)

Why not the alternatives:
- **Build per region** (a pipeline per region): wasteful (N× build time/cost) and the real danger is
  **drift** — three builds can produce three different images for "the same" commit. Build once = one
  artifact, provably identical.
- **Pull images cross-region at runtime** (one region's nodes pull from another region's ECR): adds
  cross-region **latency** and **data-transfer cost** on every pull, and **couples availability** — if
  that registry's region is down, your nodes can't pull.
- **A single shared registry pulled by all regions:** same problem — every node in every region does a
  **cross-region pull**, which is slow, expensive, and a single point of failure. Replication makes the
  bits **local** to each region instead.

## How to build it
1. **Create the ECR repo `ecom-repo` in each region** (`ap-south-1`, `eu-west-2`, `ap-southeast-1`); set
   the repo to **immutable tags** so a `<svc>-<sha>` can never be overwritten.
2. **Configure registry replication** in the primary region (`ap-south-1`): replication rules → destinations
   `eu-west-2` and `ap-southeast-1`. (Replication copies images pushed **after** the rule exists.)
3. **Leave Jenkins as-is** — it keeps pushing `ecom-repo:<svc>-<sha>` to the primary region only; verify a
   pushed image **appears in all three** ECRs.
4. **Deploy per region, same SHA:** for each cluster context, `kubectl set image .../<svc>=…/ecom-repo:<svc>-<sha>`
   (or apply the manifest pinned to that SHA). Every region ends on the identical tag.

## Done when
- One Jenkins build of a service produces one `ecom-repo:<svc>-<sha>`, and that tag is visible in the
  Mumbai, UK, and Singapore ECRs (replication worked).
- Each region's pods pull that image from their local ECR (check pull source / no cross-region pull).
- All three regions report the same running SHA for the service — identical artifact everywhere.

---
> Interview one-liner: *"I build each service once into an immutable `ecom-repo:<service>-<git-sha>` and use ECR cross-region replication to copy that exact image into every region's registry, so every node pulls locally and all three regions run the byte-identical artifact for a given SHA; deploy stays manual `kubectl` per region — I never rebuild per region (that risks drift) and never pull cross-region at runtime (latency, cost, and availability coupling)."*
