"""RED metrics (Rate, Errors, Duration) for Prometheus.

For the synchronous HTTP API the `route` label is the matched route TEMPLATE
(e.g. "/inventory/{product_id}"), never the raw path, so id values don't explode
metric cardinality. The async Kafka consumer is a worker, so per the platform
contract it exposes its own pair: events_consumed_total{topic,result} and
event_processing_duration_seconds{topic}. Together they feed the same RED
dashboards from both the sync and async sides of this service.
"""
from __future__ import annotations

from prometheus_client import Counter, Histogram

# --- HTTP (sync API) ---

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

# --- Kafka consumer (async worker) ---

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
    # Starlette routes expose `.path_format` (e.g. "/inventory/{product_id}").
    return getattr(route, "path_format", None) or "other"
