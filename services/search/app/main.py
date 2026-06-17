"""The search service: full-text product search over Elasticsearch.

Indexes the catalog's products into the ES index "products" and serves queries
against it. Calls catalog only during reindex (forwarding trace headers). Ships the
platform contract: RED metrics, JSON logs with trace ids, ES-gated readiness, and
graceful shutdown (the ES client is closed on SIGTERM via uvicorn's lifespan).
"""
from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .catalog import CatalogError, fetch_all_products
from .config import settings
from .es import Index
from .logging_setup import configure_logging
from .metrics import http_request_duration_seconds, http_requests_total, route_template
from .models import ReindexResponse, SearchResponse
from .trace import propagation_headers, trace_id_from_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("search")

# Endpoints excluded from per-request access logging to avoid probe/scrape spam.
_QUIET_PATHS = {"/healthz", "/readyz", "/metrics"}

# Result-size bounds for GET /search.
_DEFAULT_LIMIT = 20
_MAX_LIMIT = 100


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup: connect to ES (with retry) and ensure the index exists. Failing here
    # keeps the pod from becoming ready.
    index = await Index.connect(settings)
    app.state.index = index
    log.info("search service started", extra={"port": settings.port})
    try:
        yield
    finally:
        # Shutdown: uvicorn runs this on SIGTERM, draining in-flight requests first.
        await index.close()
        log.info("search service stopped")


app = FastAPI(title="search-service", lifespan=lifespan)


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


def index(request: Request) -> Index:
    return request.app.state.index


# --- search API ---


@app.get("/search", response_model=SearchResponse)
async def search(request: Request, q: str = "", limit: int = _DEFAULT_LIMIT):
    # Clamp limit to a sane window (empty/invalid → default, hard cap at 100).
    limit = _DEFAULT_LIMIT if limit <= 0 else min(limit, _MAX_LIMIT)
    try:
        hits, total = await index(request).search(q, limit)
    except Exception:  # noqa: BLE001
        log.exception("search query failed")
        return JSONResponse(status_code=503, content={"error": "search unavailable"})
    return SearchResponse(query=q, hits=hits, total=total)


@app.post("/reindex", response_model=ReindexResponse)
async def reindex(request: Request):
    # Forward incoming trace headers so the catalog call joins this trace.
    headers = propagation_headers(request.headers)
    try:
        products = await fetch_all_products(settings, headers)
    except CatalogError as err:
        log.error("catalog fetch failed", extra={"detail": str(err)})
        return JSONResponse(status_code=502, content={"error": "catalog unavailable"})

    if not products:
        return ReindexResponse(indexed=0)

    try:
        n = await index(request).bulk_index(products)
    except Exception:  # noqa: BLE001
        log.exception("bulk index failed")
        return JSONResponse(status_code=503, content={"error": "index unavailable"})

    log.info("reindex complete", extra={"indexed": n})
    return ReindexResponse(indexed=n)


# --- operational endpoints ---


@app.get("/healthz")
async def healthz():
    # Liveness: the process is up. No dependencies checked.
    return {"status": "ok"}


@app.get("/readyz")
async def readyz(request: Request):
    # Readiness: only ready if Elasticsearch is reachable.
    try:
        await index(request).ping()
    except Exception:  # noqa: BLE001
        return JSONResponse(
            status_code=503, content={"error": "elasticsearch not ready"}
        )
    return {"status": "ready"}


@app.get("/metrics")
async def metrics():
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
