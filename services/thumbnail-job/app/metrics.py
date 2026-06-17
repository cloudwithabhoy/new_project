"""Prometheus metrics for the thumbnail worker.

This is an async worker, so it exposes worker metrics rather than the RED HTTP
metrics (per api-details §3 conventions). KEDA scales the Deployment on Kafka lag;
these series are for the Grafana throughput/latency view and for SLOs.

  - thumbnails_processed_total{result}        — counter, result in {success, error}
  - thumbnail_processing_duration_seconds     — histogram of per-message work time
"""
from __future__ import annotations

from prometheus_client import Counter, Histogram

thumbnails_processed_total = Counter(
    "thumbnails_processed_total",
    "Total number of thumbnail messages processed, labelled by outcome.",
    ["result"],
)

thumbnail_processing_duration_seconds = Histogram(
    "thumbnail_processing_duration_seconds",
    "Per-message thumbnail processing time in seconds.",
)
