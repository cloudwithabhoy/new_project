# Inventory Service

Stock levels for the platform. A **Python (FastAPI)** service backed by **Postgres** that is
**both a sync HTTP API and an async Kafka consumer**:

- **Sync API** — seed/read stock and reserve it atomically. `order` calls
  `POST /inventory/reserve` during checkout.
- **Async worker** — consumes `order.created` from Kafka and **commits** reservations
  (reserved → sold). Runs as a background asyncio task started in the FastAPI lifespan and
  cancelled/drained on shutdown.

> **Correctness is the whole point here.** Reservations are **all-or-none** (a shortfall on any
> line changes nothing) and **idempotent by `order_id`** on both paths — the HTTP reserve and the
> at-least-once Kafka commit — so retries and event redelivery never double-count stock.

---

## TL;DR — run it locally

```bash
cd services/inventory
docker compose up --build      # Postgres + Redpanda (Kafka) + inventory

curl localhost:8088/healthz

# seed stock for product 7
curl -X PUT localhost:8088/inventory/7 \
  -H 'Content-Type: application/json' -d '{"available":10}'

# reserve 2 units against order 1 (atomic, idempotent)
curl -X POST localhost:8088/inventory/reserve \
  -H 'Content-Type: application/json' \
  -d '{"order_id":1,"items":[{"product_id":7,"quantity":2}]}'

curl localhost:8088/inventory/7      # -> {"product_id":7,"available":8,"reserved":2}

# emit an order.created event → the consumer commits the reservation (reserved -> sold)
echo '{"event":"order.created","order_id":1,"user_id":42,"items":[{"product_id":7,"quantity":2,"price_cents":1299}],"total_cents":2598,"created_at":"2026-06-16T12:00:00Z"}' \
  | docker compose exec -T redpanda rpk topic produce order.created --key 1

curl localhost:8088/inventory/7      # -> {"product_id":7,"available":8,"reserved":0}
```

---

## API

Base URL: `http://<host>:8088`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| GET | `/inventory/{product_id}` | Read stock | — | 200 / 404 |
| PUT | `/inventory/{product_id}` | Set/seed stock (upsert) | `StockInput` | 200 |
| POST | `/inventory/reserve` | Atomic all-or-none reservation | `ReserveInput` | 200 / 409 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable **and** Kafka consumer connected | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED + consumer metrics | — | 200 |

**`StockInput`**
```json
{ "available": 10 }
```

**`ReserveInput`**
```json
{ "order_id": 1, "items": [ { "product_id": 7, "quantity": 2 } ] }
```
- Decrements `available`, increments `reserved` for **every** line in one transaction.
- If any line lacks stock (or the row is missing) → **409** `{"error":"insufficient stock"}` and
  **nothing changes**.
- **Idempotent by `order_id`** — re-reserving the same order is a no-op success (`200`).

**`Inventory` (response of GET / PUT)**
```json
{ "product_id": 7, "available": 8, "reserved": 2 }
```

FastAPI also serves interactive docs at `/docs`; validation errors → 422.

### Async: `order.created` consumer
Subscribes to topic **`order.created`** (group **`inventory`**). On each event it commits the
reservation — `reserved -= qty`, treating the order as sold — **idempotent by `order_id`** (a
`reservations.committed` flag guards against Kafka redelivery). Offsets are committed **after** the
DB write, so a crash re-delivers rather than drops. Event schema is `api-details.md` §4.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8088` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `inventory` | no | DB user |
| `DB_PASSWORD` | `inventory` | **yes** | DB password → **Secret** |
| `DB_NAME` | `inventory` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |
| `KAFKA_BROKERS` | `localhost:9092` | no | Kafka bootstrap servers (k8s: `kafka:9092`) |
| `KAFKA_TOPIC` | `order.created` | no | Topic consumed |
| `KAFKA_GROUP` | `inventory` | no | Consumer group id |

---

## DevOps handoff — what you need for the manifests

- **Container port:** `8088` (HTTP). The Kafka consumer needs **no** inbound port (it dials out).
- **Image:** build from this dir; push to ECR as `…/inventory:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable **and** the Kafka consumer is connected)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`,
  `KAFKA_BROKERS`, `KAFKA_TOPIC`, `KAFKA_GROUP`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:**
  - its **own Postgres** (separate DB/StatefulSet — database-per-service)
  - **Kafka** (`order.created` topic) — consumer group `inventory`
- **Service discovery:** `order` reaches it at `http://inventory:8088` via its `INVENTORY_URL`.
- **Resource hints (tune later):** requests `cpu: 75m, memory: 128Mi` · limits `cpu: 300m, memory: 256Mi`
  (a bit higher than user — it also runs the Kafka client).
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.
- **Scaling note:** the consumer group is `inventory`; replicas share partitions of `order.created`,
  so scale-out is bounded by the topic's partition count.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8088), `ConfigMap`, `Secret`,
> plus the inventory Postgres StatefulSet. Kafka is a shared platform dependency.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` (`route` is the template, e.g.
  `/inventory/{product_id}` — bounded cardinality), **plus** worker metrics
  `events_consumed_total{topic,result}` and `event_processing_duration_seconds{topic}`.
- **Trace correlation:** reads the incoming `traceparent`/B3 trace id and stamps it on every log
  line (`trace_id`), so a Loki log links to its Tempo/Jaeger trace.
- **Structured JSON logs** to stdout.
- **Graceful shutdown** via FastAPI lifespan on SIGTERM: the consumer task is cancelled and drained,
  Kafka and the DB pool close cleanly — safe rollouts.
- **Retry-on-startup** for **both** Postgres and Kafka (pods may not be ready when this starts).
- **Readiness gates on DB *and* Kafka**, so traffic isn't sent to a pod that can't serve or consume.

---

## Project layout

```
inventory/
├── app/
│   ├── __main__.py       # entrypoint: `python -m app` -> uvicorn
│   ├── main.py           # FastAPI app, middleware, routes, lifespan (starts consumer)
│   ├── config.py         # env-var configuration (DB + Kafka)
│   ├── db.py             # asyncpg pool (retry-connect + migration) + reserve/commit txns
│   ├── consumer.py       # aiokafka order.created consumer (retry-connect, idempotent)
│   ├── models.py         # Pydantic schemas (StockInput / Inventory / ReserveInput)
│   ├── metrics.py        # RED (HTTP) + consumer metrics
│   ├── logging_setup.py  # JSON logging with trace_id
│   └── trace.py          # trace-context extraction + propagation header list
├── requirements.txt
├── Dockerfile            # multi-stage → slim non-root image
├── docker-compose.yml    # LOCAL dev: Postgres + Redpanda (Kafka) + inventory
├── .env.example
└── README.md
```
