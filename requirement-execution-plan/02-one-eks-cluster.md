# Phase 02 — One EKS cluster (single region)

## Goal
One empty but real EKS cluster in Mumbai (`ap-south-1`) — with ingress, autoscaling nodes, and isolated
namespaces — ready to receive workloads.

## Why this phase
Images exist in ECR but have nowhere to run. You need a real, production-shaped cluster before any
datastore or service can land.
- **Builds on:** Phase 01 (images waiting in ECR).
- **Unlocks:** Phase 03 (a place to run the datastores and the first service).

## Scope
**In scope:** the EKS control plane; a Karpenter-managed spot node group across AZs a/b/c; the AWS Load
Balancer Controller and ExternalDNS for ingress; the `shop` and `data` namespaces with `ResourceQuota` /
`LimitRange` guardrails.
**Out of scope:** datastores and any deployed service (Phase 03); the full fleet and event backbone
(Phase 04); service mesh, autoscaling policy, observability (Phases 05–07).

## What it needs to do
- A managed EKS control plane runs in `ap-south-1`, with an OIDC provider enabled for IRSA.
- Compute comes from a Karpenter-managed spot node group spanning AZs a / b / c.
- The AWS Load Balancer Controller maps Kubernetes Ingress to an ALB.
- ExternalDNS publishes ingress hostnames to Route 53.
- Namespaces `shop` (apps) and `data` (datastores) exist with `ResourceQuota` / `LimitRange` guardrails.
- Nodes spread across AZs a / b / c, so the cluster survives losing a single AZ.
- Compute runs on spot capacity with a scale-to-zero / delete cost guardrail.
- Cluster add-ons authenticate via IRSA (OIDC), not static credentials.

## Architecture

```
   ┌─ MUMBAI · ap-south-1 ───────────────────────────────  (NEW: empty EKS cluster)
   │  EKS control plane (AWS-managed)
   │  node group  (Karpenter, spot, across AZ a / b / c)
   │  AWS Load Balancer Controller   ·   ExternalDNS
   │  namespaces:  shop (apps)   ·   data (datastores)   + ResourceQuota / LimitRange
   └──────────────────────────────────────────────────────
   images waiting in ECR (Phase 01)   ─►   nothing deployed yet
```
**What's new in this step:** a real cluster with autoscaling nodes, an ingress path, and namespaces —
ready to receive workloads.

## Done when
- A `kubectl apply`'d hello pod is reachable via an ALB.
- Karpenter provisions a node on demand.
- Namespaces `shop` and `data` exist with `ResourceQuota` / `LimitRange` applied.
- Ingress hostnames resolve via ExternalDNS / Route 53.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/2.1-create-eks-cluster.md`](../step-by-step-implementation/2.1-create-eks-cluster.md) —
  eksctl control plane (`--without-nodegroup`), OIDC provider, managed node group, kubeconfig, and the
  scale-to-zero / delete cost guardrail. *(deep-dive: OIDC & IRSA)* — **done.**
- [`step-by-step-implementation/2.2-cluster-addons.md`](../step-by-step-implementation/2.2-cluster-addons.md) —
  the **AWS Load Balancer Controller** (Ingress → ALB), **ExternalDNS** (→ Route 53), and **Karpenter**
  (spot node autoscaling), each installed via IRSA. *(deep-dive: the operator/reconcile pattern)*
- [`step-by-step-implementation/2.3-namespaces-and-quotas.md`](../step-by-step-implementation/2.3-namespaces-and-quotas.md) —
  namespaces `shop` + `data` with `ResourceQuota` / `LimitRange` guardrails. *(deep-dive: Quota vs LimitRange)*

> Only `2.1` is executed so far (cluster `ecom-cluster` exists); `2.2` and `2.3` are the remaining
> Phase 02 work before the datastores in Phase 03.

---
> Interview one-liner: *"A single managed EKS cluster with Karpenter-provisioned spot nodes across AZs, the AWS LB Controller
> for ingress, and isolated `shop`/`data` namespaces — an empty but production-shaped landing zone."*
