"""RED metrics (Rate, Errors, Duration) for Prometheus.

The `route` label is the matched route TEMPLATE (e.g. "/search"), never the raw
path, so query strings and ids don't explode metric cardinality. These series
feed your SLO and Grafana RED dashboards.
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


def route_template(request) -> str:
    """Return the matched route's path template, or 'other' if unmatched (404)."""
    route = request.scope.get("route")
    # Starlette routes expose `.path_format` (e.g. "/search").
    return getattr(route, "path_format", None) or "other"
