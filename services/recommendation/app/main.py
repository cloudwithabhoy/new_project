"""The recommendation service: product recommendations over Postgres.

Sync side: `GET /recommendations` ranks products using the active A/B `VARIANT`
  - variant "a" → most popular products overall.
  - variant "b" → products co-purchased with the given `product_id` (falls back to
    popular when there's no `product_id` or no co-purchase data yet).
Async side: a Kafka consumer of `order.created` builds the popularity / co-purchase
counts those rankings read from.

Ships the platform contract: RED metrics + consumer metrics, JSON logs with trace
ids, readiness gated on DB + consumer connectivity, and graceful shutdown (uvicorn's
lifespan on SIGTERM drains in-flight requests and stops the consumer).
"""
from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .config import settings
from .consumer import Consumer
from .db import Store
from .logging_setup import configure_logging
from .metrics import (
    http_request_duration_seconds,
    http_requests_total,
    recommendation_requests_total,
    route_template,
)
from .models import Recommendations
from .trace import trace_id_from_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("recommendation")

# Endpoints excluded from per-request access logging to avoid probe/scrape spam.
_QUIET_PATHS = {"/healthz", "/readyz", "/metrics"}

# How many product ids to return in a recommendation response.
_DEFAULT_LIMIT = 10


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup: connect + migrate, then start the async consumer task. Failing here
    # keeps the pod from becoming ready.
    store = await Store.connect(settings)
    await store.migrate()
    consumer = Consumer(settings, store)
    await consumer.start()
    app.state.store = store
    app.state.consumer = consumer
    log.info(
        "recommendation service started",
        extra={"port": settings.port, "variant": settings.variant},
    )
    try:
        yield
    finally:
        # Shutdown: uvicorn runs this on SIGTERM. Stop the consumer (drain) first,
        # then close the DB pool.
        await consumer.stop()
        await store.close()
        log.info("recommendation service stopped")


app = FastAPI(title="recommendation-service", lifespan=lifespan)


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


# --- recommendations ---


@app.get("/recommendations", response_model=Recommendations)
async def recommendations(
    request: Request,
    user_id: int | None = None,
    product_id: int | None = None,
):
    variant = settings.variant
    recommendation_requests_total.labels(variant).inc()

    if variant == "b" and product_id is not None:
        # Variant B: products co-purchased with the given product; fall back to the
        # popular ranking when we have no co-purchase signal for it yet.
        ids = await store(request).co_purchased(product_id, _DEFAULT_LIMIT)
        if not ids:
            ids = await store(request).top_popular(_DEFAULT_LIMIT)
    else:
        # Variant A (or B with no product_id): most popular products overall.
        ids = await store(request).top_popular(_DEFAULT_LIMIT)

    return Recommendations(variant=variant, product_ids=ids)


# --- operational endpoints ---


@app.get("/healthz")
async def healthz():
    # Liveness: the process is up. No dependencies checked.
    return {"status": "ok"}


@app.get("/readyz")
async def readyz(request: Request):
    # Readiness: ready only if the database is reachable AND the consumer is connected.
    try:
        await store(request).ping()
    except Exception:  # noqa: BLE001
        return JSONResponse(status_code=503, content={"error": "database not ready"})
    if not consumer(request).connected:
        return JSONResponse(status_code=503, content={"error": "consumer not ready"})
    return {"status": "ready"}


@app.get("/metrics")
async def metrics():
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
