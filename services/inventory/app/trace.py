"""Trace-context helpers.

Istio's Envoy sidecars create the spans; the application's job is only to read the
incoming trace id (for log correlation) and — for services that call downstream —
forward the propagation headers. inventory is a leaf over HTTP, so it just surfaces
the trace id into its logs so a log line in Loki links back to the trace in Tempo.
"""
from __future__ import annotations

from contextvars import ContextVar

# Per-request trace id, set by the middleware and read by the log formatter.
trace_id_var: ContextVar[str] = ContextVar("trace_id", default="")

# Headers Istio uses; forward these verbatim if this service ever calls another.
PROPAGATION_HEADERS = (
    "x-request-id",
    "traceparent",
    "tracestate",
    "x-b3-traceid",
    "x-b3-spanid",
    "x-b3-parentspanid",
    "x-b3-sampled",
    "x-b3-flags",
    "b3",
)


def trace_id_from_headers(headers) -> str:
    """Extract a trace id for log correlation, preferring W3C, then B3, then req-id."""
    tp = headers.get("traceparent", "")
    if tp:
        # traceparent = <version>-<trace-id>-<parent-id>-<flags>
        parts = tp.split("-")
        if len(parts) >= 2 and len(parts[1]) == 32:
            return parts[1]
    return headers.get("x-b3-traceid") or headers.get("x-request-id") or ""
