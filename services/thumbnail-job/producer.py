"""Standalone load generator: enqueue N test messages to `thumbnail.requests`.

This is the DevOps-side knob for *watching KEDA scale*: push a burst of messages,
the topic lag jumps, KEDA scales the `thumbnail-job` Deployment up from 0; once
the worker drains the lag back to 0, KEDA scales it back down. Not part of the
service image — run it from your laptop or a debug pod.

Usage:
    python producer.py                 # 100 messages -> localhost:9092
    python producer.py 5000            # 5000 messages
    KAFKA_BROKERS=kafka:9092 python producer.py 5000

Env: KAFKA_BROKERS (default localhost:9092), KAFKA_TOPIC (default thumbnail.requests).
Only depends on aiokafka (already in requirements.txt).
"""
from __future__ import annotations

import asyncio
import json
import os
import sys

from aiokafka import AIOKafkaProducer


async def produce(count: int) -> None:
    brokers = os.getenv("KAFKA_BROKERS", "localhost:9092")
    topic = os.getenv("KAFKA_TOPIC", "thumbnail.requests")

    producer = AIOKafkaProducer(bootstrap_servers=brokers)
    await producer.start()
    print(f"producing {count} messages to {topic} via {brokers}", file=sys.stderr)
    try:
        for i in range(count):
            payload = {
                "product_id": i,
                "image_url": f"https://example.com/img/{i}.jpg",
            }
            await producer.send_and_wait(
                topic,
                value=json.dumps(payload).encode("utf-8"),
                key=str(i).encode("utf-8"),
            )
            if (i + 1) % 100 == 0:
                print(f"  sent {i + 1}/{count}", file=sys.stderr)
        await producer.flush()
        print(f"done: sent {count} messages", file=sys.stderr)
    finally:
        await producer.stop()


if __name__ == "__main__":
    n = int(sys.argv[1]) if len(sys.argv) > 1 else 100
    asyncio.run(produce(n))
