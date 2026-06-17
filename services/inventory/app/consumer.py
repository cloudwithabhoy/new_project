"""Async Kafka consumer for the `order.created` topic.

This is the asynchronous half of the inventory service. `order` emits `order.created`
when an order is confirmed; we consume it and *commit* the reservation — turning the
reserved stock into a sale (`reserved -= qty`). Idempotent by `order_id` (the store's
`commit_reservation` guards against redelivery), which matters because Kafka is
at-least-once.

Lifecycle: a single `Consumer` is created in the FastAPI lifespan, `start()`ed (which
retries the initial broker connect — the Kafka pod may not be ready yet), and then
`run()` loops in a background asyncio task. On shutdown the task is cancelled and
`stop()` drains/closes the client. Readiness gates on `connected`.

Offsets are committed manually only *after* the DB write succeeds, so a crash mid-event
re-delivers it rather than silently dropping the reservation.
"""
from __future__ import annotations

import asyncio
import json
import logging
import time

from aiokafka import AIOKafkaConsumer
from aiokafka.errors import KafkaConnectionError

from .config import Settings
from .db import Store
from .metrics import event_processing_duration_seconds, events_consumed_total
from .models import ReserveItem

log = logging.getLogger("inventory.consumer")


class Consumer:
    def __init__(self, cfg: Settings, store: Store) -> None:
        self._cfg = cfg
        self._store = store
        self._consumer: AIOKafkaConsumer | None = None
        self.connected = False

    async def start(self) -> None:
        """Create the client and connect, retrying while the broker comes up."""
        self._consumer = AIOKafkaConsumer(
            self._cfg.kafka_topic,
            bootstrap_servers=self._cfg.kafka_brokers,
            group_id=self._cfg.kafka_group,
            enable_auto_commit=False,  # commit only after the DB write succeeds
            auto_offset_reset="earliest",
            value_deserializer=lambda b: b,  # decode per-message so bad JSON is skippable
        )
        last_err: Exception | None = None
        for attempt in range(1, 11):
            try:
                await self._consumer.start()
                self.connected = True
                log.info(
                    "connected to kafka",
                    extra={"brokers": self._cfg.kafka_brokers, "topic": self._cfg.kafka_topic},
                )
                return
            except KafkaConnectionError as err:
                last_err = err
                log.warning("kafka not ready, retrying", extra={"attempt": attempt})
                await asyncio.sleep(3)
        raise RuntimeError(f"could not connect to kafka after retries: {last_err}")

    async def run(self) -> None:
        """Main consume loop. Runs as a background task until cancelled."""
        assert self._consumer is not None
        try:
            async for msg in self._consumer:
                await self._handle(msg)
                # At-least-once: commit only after the handler has durably applied
                # the event, so a crash re-delivers rather than drops it.
                await self._consumer.commit()
        except asyncio.CancelledError:
            log.info("consumer loop cancelled")
            raise

    async def _handle(self, msg) -> None:
        topic = msg.topic
        start = time.perf_counter()
        result = "ok"
        try:
            event = json.loads(msg.value)
            order_id = int(event["order_id"])
            items = [
                ReserveItem(product_id=int(i["product_id"]), quantity=int(i["quantity"]))
                for i in event.get("items", [])
            ]
            await self._store.commit_reservation(order_id, items)
            log.info(
                "order.created committed",
                extra={"order_id": order_id, "lines": len(items)},
            )
        except (json.JSONDecodeError, KeyError, ValueError) as err:
            # A malformed/unparseable event is a poison pill — log and skip it
            # (offset still advances) rather than wedging the whole partition.
            result = "invalid"
            log.error("skipping malformed event", extra={"error": str(err)})
        except Exception as err:  # noqa: BLE001
            result = "error"
            log.error("failed to process event", extra={"error": str(err)})
            raise  # don't commit the offset → event is re-delivered
        finally:
            events_consumed_total.labels(topic, result).inc()
            event_processing_duration_seconds.labels(topic).observe(
                time.perf_counter() - start
            )

    async def stop(self) -> None:
        """Drain and close the client (called on shutdown)."""
        self.connected = False
        if self._consumer is not None:
            await self._consumer.stop()
            log.info("kafka consumer stopped")
