"""Runtime configuration, read entirely from environment variables.

In Kubernetes the non-secret values are injected via a ConfigMap — nothing is
hardcoded. This service is stateless apart from its Elasticsearch backing store,
so it has no secrets of its own.
"""
from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    port: int
    log_level: str
    elasticsearch_url: str
    catalog_url: str


def _getenv(key: str, fallback: str) -> str:
    val = os.getenv(key)
    return val if val else fallback


def load_settings() -> Settings:
    return Settings(
        port=int(_getenv("PORT", "8084")),
        log_level=_getenv("LOG_LEVEL", "info").lower(),
        elasticsearch_url=_getenv("ELASTICSEARCH_URL", "http://localhost:9200"),
        catalog_url=_getenv("CATALOG_URL", "http://localhost:8083").rstrip("/"),
    )


settings = load_settings()
