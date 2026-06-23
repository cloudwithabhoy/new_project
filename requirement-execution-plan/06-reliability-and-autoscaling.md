# Phase 06 — Reliability & autoscaling

## Goal
The platform scales and protects itself automatically — pods and nodes grow with demand, critical
services survive disruptions, and you've proven you can restore from a backup.

## Why this phase
A running, meshed app still isn't trustworthy under real load or routine disruptions (node drains,
deploys, AZ events) — so you make it elastic at every layer and prove recovery instead of assuming it.
- **Builds on:** Phase 05 (the mesh — HPA on RPS uses Prometheus-adapter metrics from it).
- **Unlocks:** Phase 07 (SLO tuning from real metrics) and festive-peak scaling later (Phase 13).

## Scope
**In scope:** HPA on catalog/search/api-gateway (CPU + Prometheus-adapter RPS); KEDA on Kafka consumers
and `thumbnail-job` scale-to-zero; VPA in recommend mode; Karpenter node autoscaling (spot,
consolidation); PodDisruptionBudgets; topology spread + anti-affinity across AZs a/b/c; Velero scheduled
namespace backups + PV snapshots + a restore drill.
**Out of scope:** the full observability stack and SLO tuning from real metrics (Phase 07); chaos
validation of these protections (Phase 08); festive peak scaling (Phase 13).

## What it needs to do
- HPA scales catalog/search/api-gateway on CPU and on Prometheus-adapter RPS, no manual intervention.
- KEDA scales Kafka consumers on consumer lag and scales `thumbnail-job` to zero when it's idle.
- VPA runs in recommend mode to right-size resource requests.
- Karpenter adds and consolidates nodes (including spot) in response to pending pods.
- PodDisruptionBudgets keep critical services above their minimum replica count during node drains.
- Workloads spread across AZs a/b/c, so a single-AZ disruption doesn't take a service down.
- Velero takes scheduled namespace backups with PV snapshots, and you can actually restore from them.

## Architecture

```
   ┌─ MUMBAI ────────────────────────────────────────────  (NEW: scales & protects itself)
   │  HPA (catalog/search/api-gw — CPU + RPS)   ·   KEDA (consumers — Kafka lag)
   │  VPA (right-size requests)   ·   Karpenter (node autoscaling, spot, consolidation)
   │  PodDisruptionBudget   ·   topology spread + anti-affinity (AZ a/b/c)
   │  Velero (scheduled namespace backups + PV snapshots + restore drill)
   └──────────────────────────────────────────────────────
```
**What's new in this step:** autoscaling at every layer, disruption protection, and a tested backup/restore.

## Done when
- A load test drives HPA + Karpenter to add pods and nodes.
- KEDA scales Kafka consumers on lag and `thumbnail-job` scales to zero when idle.
- A node drain respects PodDisruptionBudgets.
- A namespace is successfully restored from a Velero backup.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/6.1-autoscaling.md`](../step-by-step-implementation/6.1-autoscaling.md) —
  HPA on catalog/search/api-gateway (CPU then Prometheus-adapter RPS); KEDA on the Kafka consumers + `thumbnail-job` scale-to-zero; VPA recommend mode; Karpenter adds nodes. *(deep-dive: HPA vs Karpenter vs VPA)*
- [`step-by-step-implementation/6.2-disruption-protection.md`](../step-by-step-implementation/6.2-disruption-protection.md) —
  PodDisruptionBudgets on critical services + topology spread / anti-affinity across AZs. *(deep-dive: voluntary vs involuntary disruption)*
- [`step-by-step-implementation/6.3-backup-and-restore.md`](../step-by-step-implementation/6.3-backup-and-restore.md) —
  Velero (S3 + IRSA), scheduled namespace backups + PV snapshots, and a restore drill. *(deep-dive: why you rehearse restores)*

---
> Interview one-liner: *"Reliability is layered: HPA/KEDA scale pods on CPU/RPS/Kafka-lag, Karpenter scales nodes, PDBs +
> multi-AZ spread protect availability, and Velero gives a tested restore — proven with a load test and a
> restore drill, not assumed."*
