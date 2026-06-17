"""Structured JSON logging to stdout, with the per-message trace id attached.

JSON logs are what you want in Kubernetes so Loki/Promtail can parse fields, and
the `trace_id` field lets you jump from a log line to its distributed trace.
"""
from __future__ import annotations

import json
import logging
import sys
from datetime import datetime, timezone

from .trace import trace_id_var

# Attributes present on a vanilla LogRecord; anything else was passed via `extra=`
# and should be promoted to a top-level JSON field.
_STD_ATTRS = set(
    vars(logging.makeLogRecord({})).keys()
) | {"message", "asctime", "taskName"}


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload = {
            "time": datetime.fromtimestamp(record.created, tz=timezone.utc).isoformat(),
            "level": record.levelname.lower(),
            "logger": record.name,
            "msg": record.getMessage(),
        }
        tid = trace_id_var.get()
        if tid:
            payload["trace_id"] = tid

        # Promote any `extra={...}` fields (e.g. product_id, image_url, duration_ms).
        for key, val in record.__dict__.items():
            if key not in _STD_ATTRS and not key.startswith("_"):
                payload[key] = val

        if record.exc_info:
            payload["error"] = self.formatException(record.exc_info)

        return json.dumps(payload, default=str)


def configure_logging(level: str) -> None:
    lvl = {
        "debug": logging.DEBUG,
        "info": logging.INFO,
        "warn": logging.WARNING,
        "warning": logging.WARNING,
        "error": logging.ERROR,
    }.get(level, logging.INFO)

    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())

    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(lvl)

    # aiokafka is chatty at INFO on rebalance; keep it at WARNING so the worker's
    # own structured lines aren't drowned out.
    logging.getLogger("aiokafka").setLevel(logging.WARNING)
