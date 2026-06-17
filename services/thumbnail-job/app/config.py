"""Runtime configuration, read entirely from environment variables.

In Kubernetes the non-secret values are injected via a ConfigMap — nothing is
hardcoded. This is a Kafka WORKER (no DB, no listen port for traffic), so the
config is just broker wiring plus the metrics port Prometheus scrapes.
"""
from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    kafka_brokers: str
    kafka_topic: str
    kafka_group: str
    log_level: str
    metrics_port: int


def _getenv(key: str, fallback: str) -> str:
    val = os.getenv(key)
    return val if val else fallback


def load_settings() -> Settings:
    return Settings(
        kafka_brokers=_getenv("KAFKA_BROKERS", "localhost:9092"),
        kafka_topic=_getenv("KAFKA_TOPIC", "thumbnail.requests"),
        kafka_group=_getenv("KAFKA_GROUP", "thumbnail"),
        log_level=_getenv("LOG_LEVEL", "info").lower(),
        metrics_port=int(_getenv("METRICS_PORT", "9100")),
    )


settings = load_settings()
