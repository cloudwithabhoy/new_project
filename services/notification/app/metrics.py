"""Prometheus metrics: RED for HTTP, plus the async-worker series.

The `route` label is the matched route TEMPLATE (e.g. "/notifications"), never the
raw path, so values don't explode metric cardinality. These series feed your SLO
and Grafana RED dashboards.

Per api-details §1.3, async workers also expose `events_consumed_total{topic,result}`
and `event_processing_duration_seconds{topic}`. We add `notifications_sent_total`
as this service's domain counter.
"""
from __future__ import annotations

from prometheus_client import Counter, Histogram

# --- RED (HTTP) ---
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

# --- Async worker (Kafka consumer) ---
events_consumed_total = Counter(
    "events_consumed_total",
    "Total number of Kafka events consumed, by topic and result.",
    ["topic", "result"],
)

event_processing_duration_seconds = Histogram(
    "event_processing_duration_seconds",
    "Time spent processing a consumed Kafka event, in seconds.",
    ["topic"],
)

notifications_sent_total = Counter(
    "notifications_sent_total",
    "Total number of notifications 'sent'.",
)


def route_template(request) -> str:
    """Return the matched route's path template, or 'other' if unmatched (404)."""
    route = request.scope.get("route")
    # Starlette routes expose `.path_format` (e.g. "/notifications").
    return getattr(route, "path_format", None) or "other"
