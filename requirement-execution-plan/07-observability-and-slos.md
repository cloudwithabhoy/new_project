# Phase 07 — Observability & SLOs

## Goal
You get a unified view of the platform — metrics, logs, and traces correlated together — plus SLOs and
burn-rate alerts so a single checkout is observable end-to-end.

## Why this phase
You can't safely operate (or break) a system you can't see — raw signals aren't enough, so you correlate
metrics, logs, and traces and turn them into SLOs and error budgets you can act on.
- **Builds on:** Phase 06 (HPA custom metrics already lean on Prometheus).
- **Unlocks:** Phase 08 (chaos game-days that need this visibility to judge impact).

## Scope
**In scope:** Prometheus + Grafana (kube-prometheus-stack); a ServiceMonitor scraping `/metrics`;
RED / USE / Istio dashboards; Loki + Promtail/Alloy for logs; Tempo via the OpenTelemetry Collector for
traces, correlated by `trace_id`; SLOs + error budgets; multi-window burn-rate alert rules with
Alertmanager routing.
**Out of scope:** chaos/fault injection that exercises this visibility (Phase 08); cross-region
observability aggregation (later phases). Autoscaling already consumes Prometheus metrics from Phase 06.

## What it needs to do
- Prometheus scrapes service `/metrics` via a ServiceMonitor and stores RED/USE/Istio signals.
- Grafana shows RED per service, USE per node, and Istio mesh dashboards — no bespoke queries needed.
- Loki ingests JSON logs via Promtail/Alloy; Tempo ingests traces via the OpenTelemetry Collector.
- Logs and traces are joined by `trace_id`, so one checkout is viewable across all three signals.
- SLOs with error budgets are defined, and multi-window burn-rate alerts route through Alertmanager so
  alerts signal real user impact, not noise.

## Architecture

```
   app /metrics  ·  JSON logs  ·  trace headers   +   Envoy sidecars
        │ scrape            │ logs                  │ traces
        ▼                   ▼                       ▼
   Prometheus            Loki ◄ promtail/alloy      Tempo ◄ OTel Collector      (NEW: observability)
        ▼                              (logs + traces joined by trace_id)
   Grafana (RED per service · USE per node · Istio mesh)  ·  Alertmanager (SLO burn-rate alerts)
```
**What's new in this step:** the full observability stack — one checkout becomes correlated
metrics + logs + a trace, with SLOs and burn-rate alerts.

## Done when
- One checkout shows up as metrics + logs + a trace, all correlated by `trace_id`.
- Grafana shows RED per service, USE per node, and Istio mesh dashboards.
- An SLO dashboard shows the error budget.
- A synthetic failure fires a burn-rate alert through Alertmanager.

## How to build it
The exact commands, manifests, verify steps, and concept deep-dives live in the runbook — broken into
small parts:
- [`step-by-step-implementation/7.1-metrics-and-dashboards.md`](../step-by-step-implementation/7.1-metrics-and-dashboards.md) —
  Prometheus + Grafana (kube-prometheus-stack), a ServiceMonitor scraping `/metrics`, and RED / USE / Istio dashboards. *(deep-dive: the pull model + RED vs USE)*
- [`step-by-step-implementation/7.2-logs-and-traces.md`](../step-by-step-implementation/7.2-logs-and-traces.md) —
  Loki + Promtail/Alloy for logs and Tempo via the OpenTelemetry Collector for traces, correlated by `trace_id`. *(deep-dive: one checkout, three views)*
- [`step-by-step-implementation/7.3-slos-and-alerts.md`](../step-by-step-implementation/7.3-slos-and-alerts.md) —
  SLOs + error budget, multi-window burn-rate alert rules, and Alertmanager routing. *(deep-dive: burn-rate alerting)*

---
> Interview one-liner: *"Every service emits RED metrics, JSON logs with a trace_id, and W3C traces; Prometheus + Loki + Tempo
> correlate them so one checkout is observable end-to-end, and SLOs with burn-rate alerts turn that into
> actionable signals."*
