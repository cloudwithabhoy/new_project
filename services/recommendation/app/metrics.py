"""RED metrics (Rate, Errors, Duration) plus async-consumer metrics for Prometheus.

The `route` label is the matched route TEMPLATE (e.g. "/recommendations"), never the
raw path, so query values don't explode metric cardinality. These series feed your
SLO and Grafana RED dashboards.

The consumer metrics (`events_consumed_total`, `event_processing_duration_seconds`)
follow the platform contract for async workers. `recommendation_requests_total` is a
per-variant counter so you can watch the A/B split during an Istio canary.
"""
from __future__ import annotations

from prometheus_client import Counter, Histogram

http_requests_total = Counter(
    "http_requests_total",
    "Total number of HTTP requests processed.",
    ["route", "method", "status"],
)

http_request_duration_seconds = Histogram(
    "http_request_duration_seconds",
    "HTTP request latency in seconds.",
    ["route", "method"],
)

# Per-variant recommendation counter — watch the A/B split during a canary rollout.
recommendation_requests_total = Counter(
    "recommendation_requests_total",
    "Total number of recommendation requests served, by A/B variant.",
    ["variant"],
)

# Async-worker metrics (platform contract for consumers).
events_consumed_total = Counter(
    "events_consumed_total",
    "Total number of Kafka events consumed.",
    ["topic", "result"],
)

event_processing_duration_seconds = Histogram(
    "event_processing_duration_seconds",
    "Kafka event processing latency in seconds.",
    ["topic"],
)


def route_template(request) -> str:
    """Return the matched route's path template, or 'other' if unmatched (404)."""
    route = request.scope.get("route")
    # Starlette routes expose `.path_format` (e.g. "/recommendations").
    return getattr(route, "path_format", None) or "other"
