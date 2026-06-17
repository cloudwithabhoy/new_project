"""The user service: profile CRUD over Postgres.

A leaf service in the mesh (it calls nothing downstream). auth calls it to create a
profile during registration. Ships the platform contract: RED metrics, JSON logs
with trace ids, DB-gated readiness, and graceful shutdown (handled by uvicorn's
lifespan on SIGTERM).
"""
from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from .config import settings
from .db import EmailConflict, NotFound, Store
from .logging_setup import configure_logging
from .metrics import http_request_duration_seconds, http_requests_total, route_template
from .models import User, UserInput
from .trace import trace_id_from_headers, trace_id_var

configure_logging(settings.log_level)
log = logging.getLogger("user")

# Endpoints excluded from per-request access logging to avoid probe/scrape spam.
_QUIET_PATHS = {"/healthz", "/readyz", "/metrics"}


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup: connect + migrate. Failing here keeps the pod from becoming ready.
    store = await Store.connect(settings)
    await store.migrate()
    app.state.store = store
    log.info("user service started", extra={"port": settings.port})
    try:
        yield
    finally:
        # Shutdown: uvicorn runs this on SIGTERM, draining in-flight requests first.
        await store.close()
        log.info("user service stopped")


app = FastAPI(title="user-service", lifespan=lifespan)


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


# --- profile CRUD ---


@app.post("/users", status_code=201, response_model=User)
async def create_user(payload: UserInput, request: Request):
    try:
        return await store(request).create(payload.email, payload.full_name)
    except EmailConflict:
        return JSONResponse(status_code=409, content={"error": "email already exists"})


@app.get("/users", response_model=list[User])
async def list_users(request: Request, limit: int = 50, offset: int = 0):
    limit = 50 if limit <= 0 or limit > 200 else limit
    offset = max(offset, 0)
    return await store(request).list(limit, offset)


@app.get("/users/by-email", response_model=User)
async def get_user_by_email(email: str, request: Request):
    try:
        return await store(request).get_by_email(email)
    except NotFound:
        return JSONResponse(status_code=404, content={"error": "user not found"})


@app.get("/users/{user_id}", response_model=User)
async def get_user(user_id: int, request: Request):
    try:
        return await store(request).get(user_id)
    except NotFound:
        return JSONResponse(status_code=404, content={"error": "user not found"})


@app.put("/users/{user_id}", response_model=User)
async def update_user(user_id: int, payload: UserInput, request: Request):
    try:
        return await store(request).update(user_id, payload.email, payload.full_name)
    except NotFound:
        return JSONResponse(status_code=404, content={"error": "user not found"})
    except EmailConflict:
        return JSONResponse(status_code=409, content={"error": "email already exists"})


# --- operational endpoints ---


@app.get("/healthz")
async def healthz():
    # Liveness: the process is up. No dependencies checked.
    return {"status": "ok"}


@app.get("/readyz")
async def readyz(request: Request):
    # Readiness: only ready if the database is reachable.
    try:
        await store(request).ping()
    except Exception:  # noqa: BLE001
        return JSONResponse(status_code=503, content={"error": "database not ready"})
    return {"status": "ready"}


@app.get("/metrics")
async def metrics():
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)
