"""Runtime configuration, read entirely from environment variables.

In Kubernetes the non-secret values are injected via a ConfigMap — nothing is
hardcoded. This service has no datastore, so all config is Kafka + HTTP wiring.
"""
from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    port: int
    log_level: str
    kafka_brokers: str
    kafka_topic: str
    kafka_group: str
    notification_buffer: int


def _getenv(key: str, fallback: str) -> str:
    val = os.getenv(key)
    return val if val else fallback


def load_settings() -> Settings:
    return Settings(
        port=int(_getenv("PORT", "8089")),
        log_level=_getenv("LOG_LEVEL", "info").lower(),
        kafka_brokers=_getenv("KAFKA_BROKERS", "localhost:9092"),
        kafka_topic=_getenv("KAFKA_TOPIC", "order.created"),
        kafka_group=_getenv("KAFKA_GROUP", "notification"),
        notification_buffer=int(_getenv("NOTIFICATION_BUFFER", "100")),
    )


settings = load_settings()
