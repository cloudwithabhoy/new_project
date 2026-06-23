# Phase 08 — Chaos engineering & game-days

## Goal
You deliberately break things and run game-days to prove the region survives failure — so it becomes a
trusted unit you can replicate, not just one you hope works.

## Why this phase
Every resilience mechanism you've built (mesh outlier detection, autoscaling, disruption budgets,
alerting) is just a hypothesis until you test it under real faults — so you inject failure and watch.
- **Builds on:** Phase 07 (observability/SLOs — you need to *see* the impact of chaos).
- **Unlocks:** Phase 09 (replication — passing the game-day is the gate to it).

## Scope
**In scope:** Chaos Mesh experiments (pod-kill, network latency/loss, partition, CPU/mem stress, clock
skew); Istio fault injection; `payment` `FAIL_MODE`; a full game-day (hypothesis → inject → watch the SLO
hold → postmortem) as the gate to Phase 09.
**Out of scope:** multi-region replication itself (Phase 09); any new resilience features — this phase
validates what already exists rather than adding capabilities.

## What it needs to do
- Chaos Mesh is installed and runs experiments: pod-kill, network latency/loss/partition, CPU/mem
  stress, and clock skew.
- Istio fault injection and the `payment` `FAIL_MODE` let you simulate application-level failures.
- Under injected failure, the mesh + autoscaling + alerts keep the SLO — or you identify the gap and fix
  it.
- The impact of every experiment is visible through the Phase 07 observability stack (SLO burn, traces,
  logs).
- Experiments follow a structured, scientific approach so results are reproducible and reviewable.
- A game-day runs end-to-end (hypothesis, injection, SLO observation, written postmortem), and passing
  it is the explicit gate to Phase 09.

## Architecture

```
   ┌─ MUMBAI ────────────────────────────────────────────  (NEW: resilience proven, not assumed)
   │  Chaos Mesh:  pod-kill · network latency/loss · partition · CPU/mem stress · clock skew
   │  +  Istio fault injection  +  payment FAIL_MODE
   │  game-day:  inject failure ─► watch HPA/KEDA/PDB/outlier-detection hold the SLO ─► postmortem
   └──────────────────────────────────────────────────────
   ===  the single region is now COMPLETE and proven  ─►  ready to replicate (Step 09)  ===
```
**What's new in this step:** deliberate failure injection that validates everything built so far —
after this, the region is a proven unit to stamp out.

## Done when
- Chaos Mesh runs the full set of experiments (pod-kill, network faults, partition, stress, clock skew).
- Istio fault injection and `payment` `FAIL_MODE` exercise application-level failures.
- A microservice can be broken while the mesh + autoscaling + alerts keep the SLO (or the gap is
  identified and fixed).
- A full game-day is completed with a postmortem, gating progress to Phase 09.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/8.1-chaos-experiments.md`](../step-by-step-implementation/8.1-chaos-experiments.md) —
  install Chaos Mesh and run experiments (pod-kill, network latency/loss/partition, CPU/mem stress, clock skew) plus Istio fault injection + `payment` `FAIL_MODE`. *(deep-dive: the scientific method of an experiment)*
- [`step-by-step-implementation/8.2-game-day.md`](../step-by-step-implementation/8.2-game-day.md) —
  run a full game-day (hypothesis → inject → watch the SLO hold → postmortem); passing it is the gate to Phase 09. *(deep-dive: game-days as proof of resilience)*

---
> Interview one-liner: *"I prove resilience with chaos — pod kills, network faults, Istio fault injection — and a game-day where
> I watch the SLO and write a postmortem. Only once a region survives that do I treat it as the proven unit
> to replicate across regions."*
