"""Runtime configuration, read entirely from environment variables.

In Kubernetes the non-secret values are injected via a ConfigMap and DB_PASSWORD
via a Secret — nothing is hardcoded. `VARIANT` is the A/B (Istio canary) knob that
selects the ranking strategy.
"""
from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    port: int
    log_level: str
    db_host: str
    db_port: int
    db_user: str
    db_password: str
    db_name: str
    db_sslmode: str
    kafka_brokers: str
    kafka_topic: str
    kafka_group: str
    variant: str

    @property
    def dsn(self) -> str:
        # asyncpg understands a standard postgres URL. ssl is handled separately
        # (see db.py) because asyncpg uses an `ssl` arg rather than a query param.
        return (
            f"postgresql://{self.db_user}:{self.db_password}"
            f"@{self.db_host}:{self.db_port}/{self.db_name}"
        )


def _getenv(key: str, fallback: str) -> str:
    val = os.getenv(key)
    return val if val else fallback


def load_settings() -> Settings:
    # VARIANT is the A/B knob; anything other than "b" falls back to "a".
    variant = _getenv("VARIANT", "a").lower()
    if variant not in ("a", "b"):
        variant = "a"
    return Settings(
        port=int(_getenv("PORT", "8090")),
        log_level=_getenv("LOG_LEVEL", "info").lower(),
        db_host=_getenv("DB_HOST", "localhost"),
        db_port=int(_getenv("DB_PORT", "5432")),
        db_user=_getenv("DB_USER", "recommendation"),
        db_password=_getenv("DB_PASSWORD", "recommendation"),
        db_name=_getenv("DB_NAME", "recommendation"),
        db_sslmode=_getenv("DB_SSLMODE", "disable"),
        kafka_brokers=_getenv("KAFKA_BROKERS", "localhost:9092"),
        kafka_topic=_getenv("KAFKA_TOPIC", "order.created"),
        kafka_group=_getenv("KAFKA_GROUP", "recommendation"),
        variant=variant,
    )


settings = load_settings()
