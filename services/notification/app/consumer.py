"""Async Kafka consumer for `order.created` → "sends" a notification.

Mirrors the platform contract for async workers:
- Retry the initial broker connect (the Kafka pod may not be ready at startup).
- Idempotent by `order_id` (re-delivered events are a no-op success).
- Keeps the last N notifications in an in-memory ring buffer for the inspection API.
- Started as an asyncio task in the app lifespan; drains cleanly on shutdown.
- Emits `events_consumed_total{topic,result}`, `event_processing_duration_seconds{topic}`,
  and `notifications_sent_total`.

There is NO real email here — "sending" is a structured log line, by design (this
is a demo platform service and the KEDA-on-Kafka-lag scaling candidate).
"""
from __future__ import annotations

import asyncio
import json
import logging
import time
from collections import deque
from datetime import datetime, timezone
from typing import Deque, Dict, List, Optional

from aiokafka import AIOKafkaConsumer

from .config import Settings
from .metrics import (
    event_processing_duration_seconds,
    events_consumed_total,
    notifications_sent_total,
)

log = logging.getLogger("notification.consumer")


class NotificationConsumer:
    """Owns the aiokafka consumer, the ring buffer, and the idempotency set."""

    def __init__(self, cfg: Settings) -> None:
        self._cfg = cfg
        self._consumer: Optional[AIOKafkaConsumer] = None
        self._task: Optional[asyncio.Task] = None
        self._stopping = asyncio.Event()
        self._connected = False
        # Ring buffer of the last N notifications (newest appended to the right).
        self._buffer: Deque[Dict] = deque(maxlen=cfg.notification_buffer)
        # Idempotency: order_ids we've already notified for. Bounded to the buffer
        # size window so it doesn't grow unbounded for a long-running process.
        self._seen: Deque[int] = deque(maxlen=max(cfg.notification_buffer * 10, 1000))
        self._seen_set: set[int] = set()

    @property
    def connected(self) -> bool:
        """True once the consumer has successfully started (used by /readyz)."""
        return self._connected

    async def start(self) -> None:
        """Connect to Kafka (with retry) and launch the consume loop as a task."""
        self._consumer = AIOKafkaConsumer(
            self._cfg.kafka_topic,
            bootstrap_servers=self._cfg.kafka_brokers,
            group_id=self._cfg.kafka_group,
            enable_auto_commit=True,
            auto_offset_reset="earliest",
            value_deserializer=lambda v: v,
        )

        last_err: Exception | None = None
        for attempt in range(1, 11):
            try:
                await self._consumer.start()
                self._connected = True
                log.info(
                    "kafka consumer connected",
                    extra={"topic": self._cfg.kafka_topic, "group": self._cfg.kafka_group},
                )
                break
            except Exception as err:  # noqa: BLE001 - retry on any connect error
                last_err = err
                log.warning("kafka not ready, retrying", extra={"attempt": attempt})
                await asyncio.sleep(3)
        else:
            raise RuntimeError(f"could not connect to kafka after retries: {last_err}")

        self._task = asyncio.create_task(self._run(), name="notification-consumer")

    async def _run(self) -> None:
        assert self._consumer is not None
        try:
            async for msg in self._consumer:
                if self._stopping.is_set():
                    break
                await self._handle(msg)
        except asyncio.CancelledError:
            raise
        except Exception:  # noqa: BLE001 - never let the loop die silently
            log.exception("consumer loop error")

    async def _handle(self, msg) -> None:
        topic = self._cfg.kafka_topic
        start = time.perf_counter()
        try:
            event = json.loads(msg.value)
        except (ValueError, TypeError):
            events_consumed_total.labels(topic, "error").inc()
            event_processing_duration_seconds.labels(topic).observe(
                time.perf_counter() - start
            )
            log.warning("skipping malformed event")
            return

        order_id = event.get("order_id")
        user_id = event.get("user_id")

        # Idempotent by order_id: re-delivered events are a no-op success.
        if order_id is not None and order_id in self._seen_set:
            events_consumed_total.labels(topic, "duplicate").inc()
            event_processing_duration_seconds.labels(topic).observe(
                time.perf_counter() - start
            )
            log.info("duplicate event ignored", extra={"order_id": order_id})
            return

        self._send(order_id, user_id)

        if order_id is not None:
            if len(self._seen) == self._seen.maxlen:
                evicted = self._seen[0]
                self._seen_set.discard(evicted)
            self._seen.append(order_id)
            self._seen_set.add(order_id)

        events_consumed_total.labels(topic, "success").inc()
        event_processing_duration_seconds.labels(topic).observe(
            time.perf_counter() - start
        )

    def _send(self, order_id, user_id) -> None:
        """"Send" the notification: emit a structured log line and record it.

        No real email — this is a demo platform service.
        """
        sent_at = datetime.now(tz=timezone.utc).isoformat()
        record = {
            "channel": "email",
            "order_id": order_id,
            "user_id": user_id,
            "sent_at": sent_at,
        }
        self._buffer.append(record)
        notifications_sent_total.inc()
        # The structured "notification sent" log line the spec asks for.
        log.info(
            "notification sent",
            extra={"channel": "email", "order_id": order_id, "user_id": user_id},
        )

    def recent(self, limit: int) -> List[Dict]:
        """Return the last `limit` notifications, newest first."""
        items = list(self._buffer)
        items.reverse()
        return items[:limit]

    async def stop(self) -> None:
        """Drain on shutdown: stop the loop, cancel the task, close the consumer."""
        self._stopping.set()
        if self._task is not None:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        if self._consumer is not None:
            await self._consumer.stop()
        self._connected = False
        log.info("kafka consumer stopped")
