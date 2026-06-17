"""Trace-context helpers for the worker.

Istio's Envoy sidecars create spans for HTTP; for Kafka the trace context rides
in the *message headers*. The producer (e.g. `order`/`catalog`) stamps the same
W3C/B3 propagation headers onto the record, so this consumer reads them back to
correlate its log lines with the originating trace. The worker is a leaf, so it
just surfaces the trace id into its logs — nothing to forward downstream.
"""
from __future__ import annotations

from contextvars import ContextVar

# Per-message trace id, set by the worker loop and read by the log formatter.
trace_id_var: ContextVar[str] = ContextVar("trace_id", default="")

# Headers Istio uses; forward these verbatim if this worker ever calls another.
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


def trace_id_from_kafka_headers(headers) -> str:
    """Extract a trace id from aiokafka record headers for log correlation.

    aiokafka exposes headers as a list of (key: str, value: bytes) tuples. We
    prefer W3C `traceparent`, then B3 `x-b3-traceid`, then `x-request-id`.
    """
    h: dict[str, str] = {}
    for key, val in headers or ():
        if val is None:
            continue
        try:
            h[key.lower()] = val.decode("utf-8")
        except (AttributeError, UnicodeDecodeError):
            continue

    tp = h.get("traceparent", "")
    if tp:
        # traceparent = <version>-<trace-id>-<parent-id>-<flags>
        parts = tp.split("-")
        if len(parts) >= 2 and len(parts[1]) == 32:
            return parts[1]
    return h.get("x-b3-traceid") or h.get("x-request-id") or ""
