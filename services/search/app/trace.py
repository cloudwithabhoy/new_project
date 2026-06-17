"""Trace-context helpers.

Istio's Envoy sidecars create the spans; the application's job is to read the
incoming trace id (for log correlation) and — because this service calls the
catalog downstream during reindex — forward the propagation headers so one trace
spans both services.
"""
from __future__ import annotations

from contextvars import ContextVar

# Per-request trace id, set by the middleware and read by the log formatter.
trace_id_var: ContextVar[str] = ContextVar("trace_id", default="")

# Headers Istio uses; forward these verbatim on every downstream call.
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


def propagation_headers(headers) -> dict[str, str]:
    """Pick the trace-propagation headers out of an incoming request, to forward
    verbatim on a downstream call so the trace stays unbroken."""
    out: dict[str, str] = {}
    for name in PROPAGATION_HEADERS:
        val = headers.get(name)
        if val:
            out[name] = val
    return out
