# Phase 05 — Istio service mesh

## Goal
The whole fleet runs inside a service mesh, so you get mTLS, traffic control, and tracing across every
service without touching app code.

## Why this phase
The services are written in different languages, and you don't want to re-build security, traffic
policy, and tracing inside each one — the mesh gives you all of that at the infrastructure layer.
- **Builds on:** Phase 04 (the full app running behind ALB ingress).
- **Unlocks:** Phase 06 (RPS-based autoscaling that leans on mesh metrics).

## Scope
**In scope:** installing Istio with Envoy sidecars on every pod in `shop`; STRICT mTLS via
`PeerAuthentication`; moving the edge to an Istio `Gateway` + `VirtualService`; traffic management
(`DestinationRule` connection pools + outlier detection / circuit breaking); Kiali topology and
Jaeger/Tempo tracing.
**Out of scope:** RPS-driven autoscaling and disruption protection (Phase 06); the full observability
stack with SLOs and burn-rate alerts (Phase 07); chaos game-days (Phase 08); canary releases (Phase 15).

## What it needs to do
- Every pod in the `shop` namespace gets an Envoy sidecar injected automatically.
- Service-to-service traffic is encrypted with STRICT mTLS — no plaintext fallback.
- The edge is served by an Istio `Gateway` + `VirtualService`, and the old ALB Ingress is retired.
- `DestinationRule` connection pools and outlier detection eject failing instances to protect callers.
- You can see the mesh topology in Kiali and trace a request end-to-end in Jaeger/Tempo.
- All of this works without changing any application code.

## Architecture

```
   users ─► ALB ─► ┌──────────────────┐ ─► api-gateway ─(mTLS mesh)─► services   (NEW: Istio)
                   │ Istio Ingress GW │
                   └──────────────────┘
   every pod + Envoy sidecar  ·  PeerAuthentication STRICT  ·  VirtualService / DestinationRule
   Kiali (live topology)  ·  Jaeger/Tempo (a checkout trace: gateway → order → payment → inventory)
```
**What's new in this step:** Istio — sidecars on every pod, STRICT mTLS, the edge moved to an Istio
Gateway, and full distributed tracing.

## Done when
- Every pod in `shop` has an Envoy sidecar.
- STRICT mTLS is enforced and visible in Kiali (no plaintext service-to-service traffic).
- The edge is served by an Istio `Gateway` + `VirtualService` and the old ALB Ingress is gone.
- A checkout traces across `gateway → order → payment → inventory` end-to-end in Jaeger/Tempo.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/5.1-install-istio-and-mtls.md`](../step-by-step-implementation/5.1-install-istio-and-mtls.md) —
  `istioctl install`, sidecar injection on `shop`, redeploy, then STRICT mTLS via `PeerAuthentication`. *(deep-dive: sidecar data-plane vs control-plane + automatic mTLS)*
- [`step-by-step-implementation/5.2-istio-gateway-edge.md`](../step-by-step-implementation/5.2-istio-gateway-edge.md) —
  move the edge from ALB Ingress to an Istio `Gateway` + `VirtualService` and retire the old Ingress. *(deep-dive: north-south vs east-west)*
- [`step-by-step-implementation/5.3-traffic-management-and-tracing.md`](../step-by-step-implementation/5.3-traffic-management-and-tracing.md) —
  `DestinationRule` connection pools + outlier detection (circuit breaking); Kiali topology + a real checkout trace in Jaeger/Tempo. *(deep-dive: circuit breaking & outlier ejection)*

---
> Interview one-liner: *"Istio gives me uniform mTLS, traffic management, and tracing across a polyglot fleet without touching
> app code — every pod gets an Envoy sidecar, the edge is an Istio Gateway, and a checkout is one
> end-to-end trace."*
