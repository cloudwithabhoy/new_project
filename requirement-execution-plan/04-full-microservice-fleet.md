# Phase 04 — Deploy the full microservice fleet + async backbone

## Goal
The whole application goes live: all 6 microservices running in the cluster
(pre-mesh, ALB ingress), with the Kafka event backbone wired up.

## Why this phase
The deploy path is proven on one service, but there's no working app until the whole fleet is live — and
the later phases (mesh, scaling, observability, resilience) need a real running app to act on.
- **Builds on:** Phase 03 (datastores running, deploy path proven with catalog).
- **Unlocks:** Phase 05+ (a real app to mesh, scale, observe, and break).

## Scope
**In scope:** the remaining 5 services deployed with the 5-object recipe from 3.2; service-to-service
discovery via cluster DNS `*_URL` env vars; `auth`'s shared `JWT_PRIVATE_KEY_PEM` Secret; the Kafka
backbone (`order` producing `order.created`, consumed by inventory / notification / recommendation);
`thumbnail-job` as a Job; default-deny NetworkPolicy opened to the exact call graph; ALB Ingress →
api-gateway + frontend.
**Out of scope:** the service mesh (Phase 05); autoscaling policy and observability (Phases 06–07);
KEDA-driven scaling of `thumbnail-job` (later).

## What it needs to do
- All 6 microservices run in the cluster behind ALB ingress (pre-mesh).
- Services find each other via cluster DNS using `*_URL` env vars — no hardcoded addresses.
- `auth` signs tokens from one shared `JWT_PRIVATE_KEY_PEM` Secret.
- `order` produces `order.created`; `inventory`, `notification`, and `recommendation` consume it, so order
  side-effects happen asynchronously and each side can scale or fail on its own.
- A default-deny NetworkPolicy in `shop` / `data` is opened to exactly the application call graph — only
  the real edges are allowed, everything else east-west is denied.
- ALB Ingress routes external traffic to api-gateway and frontend.
- Each service keeps its own database; no shared databases.

## Architecture

```
   users ─► ALB Ingress ─► api-gateway ─► [ 6 microservices ] ─► datastores   (NEW: full fleet)
                                              │
                       order ─► Kafka(order.created) ─► inventory · notification · recommendation
                                                                          (NEW: async backbone)
   NetworkPolicy default-deny ─► open exactly the call-graph edges  ·  thumbnail-job (Job/KEDA later)
```
**What's new in this step:** the whole application is live — synchronous checkout chain + the async
event fan-out — behind an ALB.

## Done when
- A full checkout works end-to-end through the running app.
- Placing an order fans out `order.created` to inventory, notification, and recommendation.
- All 6 services are running behind ALB ingress.
- Only the exact call-graph edges are allowed; all other east-west traffic is denied.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/4.1-deploy-the-fleet.md`](../step-by-step-implementation/4.1-deploy-the-fleet.md) —
  deploy the remaining 5 services with the 5-object recipe from 3.2; service-to-service discovery via
  cluster DNS `*_URL` env vars; `auth`'s one shared `JWT_PRIVATE_KEY_PEM` Secret. *(deep-dive: Service DNS → ClusterIP → kube-proxy)*
- [`step-by-step-implementation/4.2-kafka-async-backbone.md`](../step-by-step-implementation/4.2-kafka-async-backbone.md) —
  `order` produces `order.created`; `inventory` / `notification` / `recommendation` consume it; `thumbnail-job` as a Job. *(deep-dive: sync vs async events)*
- [`step-by-step-implementation/4.3-networkpolicy-and-edge.md`](../step-by-step-implementation/4.3-networkpolicy-and-edge.md) —
  default-deny NetworkPolicy in `shop`/`data`, opened to exactly the call graph; ALB Ingress → api-gateway + frontend. *(deep-dive: zero-trust east-west networking)*

---
> Interview one-liner: *"The full fleet deployed with database-per-service, env-var service discovery, default-deny
> NetworkPolicies opened to the exact call graph, and the Kafka async backbone — a real checkout traces
> through the sync chain and fans out events."*
