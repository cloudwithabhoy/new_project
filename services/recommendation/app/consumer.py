"""Async Kafka consumer for `order.created`, run as an asyncio task in the lifespan.

For every order it increments per-product popularity counts and per-pair co-purchase
counts (products bought together in the same order). Processing is **idempotent by
`order_id`** via the `processed_orders` ledger in the store, so a redelivered event
(at-least-once Kafka) is counted at most once.

Operational shape (platform contract):
  - Retry the initial broker connect (the Kafka pod may not be ready at startup).
  - Manual offset commit AFTER the DB write succeeds, so a crash re-delivers rather
    than silently dropping an event.
  - Graceful drain on shutdown: stop the loop, then `consumer.stop()`.
  - Metrics: `events_consumed_total{topic,result}` and
    `event_processing_duration_seconds{topic}`.
"""
from __future__ import annotations

import asyncio
import json
import logging
import time

from aiokafka import AIOKafkaConsumer
from aiokafka.errors import KafkaError

from .config import Settings
from .db import Store
from .metrics import events_consumed_total, event_processing_duration_seconds

log = logging.getLogger("recommendation.consumer")


class Consumer:
    """Owns the aiokafka consumer and the background poll loop."""

    def __init__(self, cfg: Settings, store: Store) -> None:
        self._cfg = cfg
        self._store = store
        self._consumer: AIOKafkaConsumer | None = None
        self._task: asyncio.Task | None = None
        self._stopping = False
        self.connected = False

    async def start(self) -> None:
        """Connect (with retry) and spawn the background consume loop."""
        self._consumer = AIOKafkaConsumer(
            self._cfg.kafka_topic,
            bootstrap_servers=self._cfg.kafka_brokers,
            group_id=self._cfg.kafka_group,
            enable_auto_commit=False,  # commit only after the DB write lands
            auto_offset_reset="earliest",
            value_deserializer=lambda b: b,
        )

        last_err: Exception | None = None
        for attempt in range(1, 11):
            try:
                await self._consumer.start()
                self.connected = True
                log.info("connected to kafka", extra={"topic": self._cfg.kafka_topic})
                break
            except KafkaError as err:
                last_err = err
                log.warning("kafka not ready, retrying", extra={"attempt": attempt})
                await asyncio.sleep(3)
        else:
            raise RuntimeError(f"could not connect to kafka after retries: {last_err}")

        self._task = asyncio.create_task(self._run(), name="kafka-consume-loop")

    async def _run(self) -> None:
        assert self._consumer is not None
        try:
            async for msg in self._consumer:
                if self._stopping:
                    break
                await self._handle(msg)
                # Commit AFTER the handler so a crash re-delivers (at-least-once).
                await self._consumer.commit()
        except asyncio.CancelledError:  # normal on shutdown
            raise
        except Exception:  # noqa: BLE001 - keep the loop alive on unexpected errors
            log.exception("consume loop crashed")

    async def _handle(self, msg) -> None:
        topic = self._cfg.kafka_topic
        start = time.perf_counter()
        result = "ok"
        try:
            event = json.loads(msg.value)
            order_id = int(event["order_id"])
            product_ids = [int(it["product_id"]) for it in event.get("items", [])]

            newly = await self._store.record_order(order_id, product_ids)
            result = "ok" if newly else "duplicate"
            log.info(
                "event processed",
                extra={
                    "topic": topic,
                    "order_id": order_id,
                    "products": len(product_ids),
                    "result": result,
                },
            )
        except (json.JSONDecodeError, KeyError, ValueError, TypeError):
            # Malformed event: count it, log it, but DON'T block the partition.
            result = "invalid"
            log.exception("invalid event payload", extra={"topic": topic})
        except Exception:  # noqa: BLE001 - DB or other transient failure
            result = "error"
            log.exception("event processing failed", extra={"topic": topic})
        finally:
            event_processing_duration_seconds.labels(topic).observe(
                time.perf_counter() - start
            )
            events_consumed_total.labels(topic, result).inc()

    async def stop(self) -> None:
        """Stop accepting new work and drain: cancel the loop, then close the client."""
        self._stopping = True
        if self._task is not None:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        if self._consumer is not None:
            await self._consumer.stop()
        self.connected = False
        log.info("kafka consumer stopped")
