# Payment Service

The platform's **mock authorizer** — a small **Go** HTTP service backed by **Postgres** that
"authorizes" payments for orders. It **approves by default** and records every decision, so the
`order` service has a real payment row to point at and the demo has an audit trail.

It is also the platform's deliberate **fault target**. A single env var, `FAIL_MODE`, turns it into
a deterministic source of declines or 5xx errors — a clean hook for chaos engineering and for
demonstrating **Istio fault injection** / retries / circuit breaking without editing code.

```
checkout:  order → payment   (POST /payments {order_id, user_id, amount_cents})
                              approve → 201 status:"approved"
                              decline → 201 status:"declined"  (order maps to 402)
                              error   → 500                    (authorizer "outage")
```

---

## TL;DR — run it locally

`docker compose up` here starts **payment + its Postgres** together:

```bash
cd services/payment
docker compose up --build

# authorize a payment (approved by default)
curl -X POST localhost:8087/payments \
  -H 'Content-Type: application/json' \
  -d '{"order_id":1,"user_id":42,"amount_cents":2598}'

# fetch a stored payment
curl localhost:8087/payments/1
```

To demo the fault behaviour, set `FAIL_MODE` to `decline` or `error` in `docker-compose.yml`
(or the environment) and restart.

---

## API

Base URL: `http://<host>:8087`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| POST | `/payments` | Authorize + persist a payment | `PaymentInput` | 201 |
| GET | `/payments/{id}` | Fetch a stored payment | — | 200 / 404 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | — | 200 |

**`PaymentInput`**
```json
{ "order_id": 1, "user_id": 42, "amount_cents": 2598 }
```
**`Payment`** (response)
```json
{ "id": 1, "order_id": 1, "user_id": 42, "amount_cents": 2598, "status": "approved", "created_at": "2026-06-16T12:00:00Z" }
```

`status` is `"approved"` or `"declined"`. Errors return `{"error":"message"}`.

### FAIL_MODE — the fault hook

| `FAIL_MODE` | POST /payments behaviour | Persisted? |
|---|---|---|
| `off` (default) | `201` with `status:"approved"` | yes |
| `decline` | `201` with `status:"declined"` | yes |
| `error` | `500 {"error":"payment authorizer error"}` | no |

The `decline` outcome is what lets the `order` service exercise its **402 "payment declined"** path;
`error` simulates an authorizer outage for retry/circuit-breaker demos.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8087` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `payment` | no | DB user |
| `DB_PASSWORD` | `payment` | **yes** | DB password → **Secret** |
| `DB_NAME` | `payment` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |
| `FAIL_MODE` | `off` | no | `off`/`decline`/`error` — fault hook |

---

## DevOps handoff — what you need for the manifests

- **Container port:** `8087` (HTTP)
- **Image:** build from this dir; push to ECR as `…/payment:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`, `FAIL_MODE`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:**
  - Its **own Postgres** (separate DB/StatefulSet — database-per-service).
  - No downstream service calls. The `order` service reaches it at `PAYMENT_URL` (e.g. `http://payment:8087`).
- **Resource hints (tune later):** requests `cpu: 50m, memory: 64Mi` · limits `cpu: 250m, memory: 128Mi`
- **Security:** distroless **non-root**; filesystem can be read-only.
- **Istio (later phases):** payment is the **fault-injection target** — flip `FAIL_MODE=decline`/`error`
  via the ConfigMap, or inject HTTP aborts/delays on its `VirtualService`, to test the `order`
  service's retries, timeouts, and circuit breaking (DestinationRule outlier detection).

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8087), `ConfigMap`, `Secret` (DB),
> plus the payment Postgres StatefulSet.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/payments/{id}`), so SLO
  dashboards and burn-rate alerts have low-cardinality series.
- **Trace propagation:** reads the incoming trace id (W3C `traceparent`, then B3, then
  `x-request-id`) and attaches it to every log line (`trace_id`), so the `order → payment` hop joins
  one trace in Jaeger/Tempo.
- **Structured JSON logs** to stdout (Loki-friendly).
- **Graceful shutdown** on SIGTERM (drains in-flight requests) — clean rollouts + PDB behaviour.
- **Readiness gates on the DB**, so traffic isn't sent to a pod that can't serve.
- **Deterministic fault hook** (`FAIL_MODE`) for chaos demos.

---

## Project layout

```
payment/
├── main.go         # entrypoint: config, DB, server, graceful shutdown
├── config.go       # env-var configuration + JSON logger
├── models.go       # Payment + PaymentInput + validation
├── store.go        # Postgres payment store (retry-connect + migration)
├── handlers.go     # HTTP routes, mock authorizer, helpers
├── metrics.go      # RED metrics + logging middleware (route label, trace id)
├── trace.go        # trace-context extraction + propagation helpers
├── Dockerfile      # multi-stage → distroless non-root image
├── docker-compose.yml  # LOCAL dev: payment + Postgres
├── .env.example
└── README.md
```

## Notes for later phases
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum`. The Dockerfile
  tolerates its absence for now.
- **Idempotency:** today a repeated POST creates a new payment row. A real authorizer would be
  idempotent on `order_id` (e.g. unique index + upsert). A good Phase 5–6 hardening exercise.
