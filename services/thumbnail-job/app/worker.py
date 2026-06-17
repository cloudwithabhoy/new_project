"""The thumbnail worker: consume `thumbnail.requests`, "process" each image.

A KEDA-driven Kafka consumer (api-details §3 `thumbnail-job`). It reads
`{product_id, image_url}` messages, simulates thumbnail work (~200ms), and emits
worker metrics + structured logs. Designed for **scale-to-zero**: when the topic
lag is 0 KEDA scales the Deployment to 0; a burst of messages scales it back up.

Delivery is at-least-once (we use auto-commit), and processing is naturally
idempotent here — re-processing the same `product_id`/`image_url` just redoes the
sleep, so a duplicate is harmless. We never crash on a bad/duplicate message;
malformed JSON is logged as an `error` result and skipped.

Graceful shutdown (api-details §1.6): on SIGTERM/SIGINT we stop the consume loop,
let the in-flight message finish, close the consumer (committing offsets), shut
the metrics server, and exit 0 — clean rollouts.
"""
from __future__ import annotations

import asyncio
import json
import logging
import signal
import time

from aiokafka import AIOKafkaConsumer
from aiokafka.errors import KafkaConnectionError

from .config import Settings, settings
from .logging_setup import configure_logging
from .metrics import (
    thumbnail_processing_duration_seconds,
    thumbnails_processed_total,
)
from .server import start_metrics_server
from .trace import trace_id_from_kafka_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("thumbnail")

# Simulated per-image work time.
WORK_SECONDS = 0.2


async def _connect(cfg: Settings) -> AIOKafkaConsumer:
    """Create + start a consumer, retrying the initial connect (api-details §1.7).

    The broker pod may not be ready when this pod starts, so we back off and
    retry rather than crash-loop.
    """
    consumer = AIOKafkaConsumer(
        cfg.kafka_topic,
        bootstrap_servers=cfg.kafka_brokers,
        group_id=cfg.kafka_group,
        # at-least-once: auto-commit offsets; processing is idempotent.
        enable_auto_commit=True,
        auto_offset_reset="earliest",
    )
    backoff = 1.0
    while True:
        try:
            await consumer.start()
            log.info(
                "kafka consumer connected",
                extra={
                    "brokers": cfg.kafka_brokers,
                    "topic": cfg.kafka_topic,
                    "group": cfg.kafka_group,
                },
            )
            return consumer
        except KafkaConnectionError as exc:
            log.warning(
                "kafka not ready, retrying",
                extra={"backoff_s": backoff, "detail": str(exc)},
            )
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 15.0)


async def _process(msg) -> None:
    """Handle one Kafka record. Never raises — records a result either way."""
    # Correlate every log line for this message with its originating trace.
    trace_id_var.set(trace_id_from_kafka_headers(msg.headers))

    start = time.perf_counter()
    try:
        payload = json.loads(msg.value)
        product_id = payload.get("product_id")
        image_url = payload.get("image_url")
    except (json.JSONDecodeError, AttributeError, TypeError):
        thumbnails_processed_total.labels("error").inc()
        log.error(
            "skipping malformed message",
            extra={"partition": msg.partition, "offset": msg.offset},
        )
        return

    log.info(
        "thumbnail start",
        extra={"product_id": product_id, "image_url": image_url},
    )
    try:
        await asyncio.sleep(WORK_SECONDS)  # simulate the resize/encode work
        elapsed = time.perf_counter() - start
        thumbnail_processing_duration_seconds.observe(elapsed)
        thumbnails_processed_total.labels("success").inc()
        log.info(
            "thumbnail done",
            extra={
                "product_id": product_id,
                "image_url": image_url,
                "duration_ms": round(elapsed * 1000, 1),
            },
        )
    except Exception:  # noqa: BLE001 — never let one bad message kill the loop
        thumbnails_processed_total.labels("error").inc()
        log.exception(
            "thumbnail failed",
            extra={"product_id": product_id, "image_url": image_url},
        )


async def run() -> None:
    cfg = settings
    httpd = start_metrics_server(cfg.metrics_port)

    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)

    consumer = await _connect(cfg)
    log.info("thumbnail worker started")
    try:
        while not stop.is_set():
            # Bounded poll so we periodically re-check the stop flag even when idle.
            batches = await consumer.getmany(timeout_ms=1000, max_records=10)
            for _tp, records in batches.items():
                for msg in records:
                    # Finish the in-flight message even if a stop arrives mid-batch.
                    await _process(msg)
    finally:
        # Graceful drain: stop the loop, close the consumer (commits offsets),
        # then tear down the metrics server. Then we exit 0.
        log.info("thumbnail worker stopping")
        await consumer.stop()
        httpd.shutdown()
        log.info("thumbnail worker stopped")


def main() -> None:
    asyncio.run(run())


if __name__ == "__main__":
    main()
