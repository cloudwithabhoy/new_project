"""The notification service: consume `order.created`, "send" notifications.

Async-only in the mesh — nothing calls it over HTTP for business logic. It consumes
the `order.created` Kafka topic and emits a structured "notification sent" log line
per order (no real email). A small read-only inspection API exposes the last N
notifications from an in-memory ring buffer for demos/debugging.

Ships the platform contract: RED metrics + async-worker metrics, JSON logs with
trace ids, Kafka-gated readiness, and graceful shutdown (the consumer drains on
SIGTERM via uvicorn's lifespan). This service has NO database.
"""
from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Response
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .config import settings
from .consumer import NotificationConsumer
from .logging_setup import configure_logging
from .metrics import http_request_duration_seconds, http_requests_total, route_template
from .models import Notification
from .trace import trace_id_from_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("notification")

# Endpoints excluded from per-request access logging to avoid probe/scrape spam.
_QUIET_PATHS = {"/healthz", "/readyz", "/metrics"}


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup: connect to Kafka (with retry) and launch the consume loop. Failing
    # here keeps the pod from becoming ready.
    consumer = NotificationConsumer(settings)
    await consumer.start()
    app.state.consumer = consumer
    log.info("notification service started", extra={"port": settings.port})
    try:
        yield
    finally:
        # Shutdown: uvicorn runs this on SIGTERM. Drain the consumer first, then
        # let in-flight HTTP requests finish.
        await consumer.stop()
        log.info("notification service stopped")


app = FastAPI(title="notification-service", lifespan=lifespan)


@app.middleware("http")
async def observability_middleware(request: Request, call_next):
    # Seed the trace id so every log line in this request is correlated.
    trace_id_var.set(trace_id_from_headers(request.headers))

    start = time.perf_counter()
    response = await call_next(request)
    elapsed = time.perf_counter() - start

    route = route_template(request)
    http_requests_total.labels(route, request.method, str(response.status_code)).inc()
    http_request_duration_seconds.labels(route, request.method).observe(elapsed)

    if request.url.path not in _QUIET_PATHS:
        log.info(
            "request",
            extra={
                "method": request.method,
                "path": request.url.path,
                "status": response.status_code,
                "duration_ms": round(elapsed * 1000, 1),
            },
        )
    return response


def consumer(request: Request) -> NotificationConsumer:
    return request.app.state.consumer


# --- inspection API ---


@app.get("/notifications", response_model=list[Notification])
async def list_notifications(request: Request, limit: int = 50):
    # Read-only view of the in-memory ring buffer, newest first.
    limit = 50 if limit <= 0 or limit > settings.notification_buffer else limit
    return consumer(request).recent(limit)


# --- operational endpoints ---


@app.get("/healthz")
async def healthz():
    # Liveness: the process is up. No dependencies checked.
    return {"status": "ok"}


@app.get("/readyz")
async def readyz(request: Request):
    # Readiness: only ready if the Kafka consumer is connected.
    if not consumer(request).connected:
        return Response(
            content='{"error":"kafka consumer not ready"}',
            media_type="application/json",
            status_code=503,
        )
    return {"status": "ready"}


@app.get("/metrics")
async def metrics():
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
