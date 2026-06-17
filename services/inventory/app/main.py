"""The inventory service: stock levels over Postgres — sync API *and* async worker.

Two faces, one service:
- **Sync HTTP API** — seed/read stock and reserve it atomically (all-or-none,
  idempotent by order_id). `order` calls `POST /inventory/reserve` during checkout.
- **Async Kafka consumer** — subscribes to `order.created` and commits reservations
  (reserved → sold). It runs as a background asyncio task started in the lifespan and
  is cancelled/drained on shutdown.

Ships the platform contract: RED metrics for HTTP + consumer metrics for the worker,
JSON logs with trace ids, readiness gated on BOTH the DB and the Kafka consumer being
connected, and graceful shutdown (uvicorn drives the lifespan on SIGTERM).
"""
from __future__ import annotations

import asyncio
import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .config import settings
from .consumer import Consumer
from .db import InsufficientStock, NotFound, Store
from .logging_setup import configure_logging
from .metrics import http_request_duration_seconds, http_requests_total, route_template
from .models import Inventory, ReserveInput, StockInput
from .trace import trace_id_from_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("inventory")

# Endpoints excluded from per-request access logging to avoid probe/scrape spam.
_QUIET_PATHS = {"/healthz", "/readyz", "/metrics"}


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup: connect + migrate. Failing here keeps the pod from becoming ready.
    store = await Store.connect(settings)
    await store.migrate()
    app.state.store = store

    # Start the async Kafka consumer (retries the initial broker connect) and run
    # its consume loop as a background task we own for the life of the process.
    consumer = Consumer(settings, store)
    await consumer.start()
    app.state.consumer = consumer
    app.state.consumer_task = asyncio.create_task(consumer.run())

    log.info("inventory service started", extra={"port": settings.port})
    try:
        yield
    finally:
        # Shutdown: uvicorn runs this on SIGTERM, draining in-flight requests first.
        # Cancel the consumer loop, then drain/close Kafka and the DB pool.
        task = app.state.consumer_task
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass
        await consumer.stop()
        await store.close()
        log.info("inventory service stopped")


app = FastAPI(title="inventory-service", lifespan=lifespan)


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


def store(request: Request) -> Store:
    return request.app.state.store


def consumer(request: Request) -> Consumer:
    return request.app.state.consumer


# --- inventory API ---


@app.get("/inventory/{product_id}", response_model=Inventory)
async def get_inventory(product_id: int, request: Request):
    try:
        return await store(request).get(product_id)
    except NotFound:
        return JSONResponse(status_code=404, content={"error": "product not found"})


@app.put("/inventory/{product_id}", response_model=Inventory)
async def set_inventory(product_id: int, payload: StockInput, request: Request):
    # Set/seed stock for a product (upsert): create the row or update `available`.
    return await store(request).upsert(product_id, payload.available)


@app.post("/inventory/reserve")
async def reserve(payload: ReserveInput, request: Request):
    # All-or-none atomic reservation, idempotent by order_id. A shortfall on any
    # line reserves nothing and returns 409.
    try:
        await store(request).reserve(payload.order_id, payload.items)
    except InsufficientStock:
        return JSONResponse(status_code=409, content={"error": "insufficient stock"})
    return {"reserved": True}


# --- operational endpoints ---


@app.get("/healthz")
async def healthz():
    # Liveness: the process is up. No dependencies checked.
    return {"status": "ok"}


@app.get("/readyz")
async def readyz(request: Request):
    # Readiness: only ready if the DB is reachable AND the Kafka consumer connected.
    try:
        await store(request).ping()
    except Exception:  # noqa: BLE001
        return JSONResponse(status_code=503, content={"error": "database not ready"})
    if not consumer(request).connected:
        return JSONResponse(status_code=503, content={"error": "kafka not ready"})
    return {"status": "ready"}


@app.get("/metrics")
async def metrics():
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
