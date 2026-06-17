# Recommendation Service

Product recommendations for the platform. A small **Python (FastAPI)** service backed by
**Postgres**, with a **sync API** for serving recommendations and an **async Kafka consumer**
that learns popularity / co-purchase signals from orders.

It is an **A/B service**: the `VARIANT` env var (`a` | `b`) selects the ranking strategy and is
echoed in every response — this is the knob your **Istio canary / A-B routing** flips.

> **Two halves, one service.** The HTTP side answers `GET /recommendations`. The consumer side
> subscribes to `order.created` and, for each order, bumps per-product popularity and per-pair
> co-purchase counts. The rankings read what the consumer has written.

---

## TL;DR — run it locally

```bash
cd services/recommendation
docker compose up --build

curl localhost:8090/healthz
curl 'localhost:8090/recommendations?user_id=42'
curl 'localhost:8090/recommendations?product_id=7'
```

Feed the consumer a fake order (Redpanda exposes the Kafka API on `localhost:9092`):

```bash
# produce one order.created event with rpk inside the redpanda container
docker compose exec redpanda rpk topic produce order.created --key 123 <<'EOF'
{"event":"order.created","order_id":123,"user_id":42,"items":[{"product_id":7,"quantity":2,"price_cents":1299},{"product_id":9,"quantity":1,"price_cents":500}],"total_cents":3098,"created_at":"2026-06-16T12:00:00Z"}
EOF

# now product 7 and product 9 are popular, and they are co-purchased with each other
curl 'localhost:8090/recommendations?product_id=7'   # variant b -> [9] (then popular)
```

---

## API

Base URL: `http://<host>:8090`

| Method | Path | Description | Query | Success |
|---|---|---|---|---|
| GET | `/recommendations` | Ranked product ids for the active variant | `user_id=` or `product_id=` | 200 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable **and** consumer connected | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED + consumer metrics | — | 200 |

**Response shape**
```json
{ "variant": "a", "product_ids": [7, 9, 3] }
```

### A/B variants

| `VARIANT` | Strategy |
|---|---|
| `a` | **Most popular overall** — top products by order count. Ignores `product_id`. |
| `b` | **Co-purchased-with** — products most often bought together with the given `product_id`; **falls back to popular** when there's no `product_id` or no co-purchase data yet. |

`user_id` is accepted for both variants (and for future per-user personalization) but the current
strategies key off global popularity / the supplied `product_id`.

---

## Async consumer (`order.created`)

- **Topic:** `order.created` · **Group:** `recommendation` (`KAFKA_GROUP`).
- For each event: `+1` popularity for every product in the order, and `+1` co-purchase count for
  every ordered pair of products in that same order (directional, so a lookup by either product
  finds the other).
- **Idempotent by `order_id`** via the `processed_orders` ledger — a redelivered event (Kafka is
  at-least-once) is counted at most once. Offsets are committed **after** the DB write.
- Runs as an **asyncio task started in the FastAPI lifespan**; on SIGTERM it is cancelled and the
  client is stopped (drain) before the DB pool closes.

### Tables

| Table | Columns | Purpose |
|---|---|---|
| `product_popularity` | `product_id PK, count` | popularity ranking (variant a) |
| `co_purchase` | `product_id, other_product_id, count, PK(product_id, other_product_id)` | co-purchase ranking (variant b) |
| `processed_orders` | `order_id PK, created_at` | idempotency ledger |

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8090` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `recommendation` | no | DB user |
| `DB_PASSWORD` | `recommendation` | **yes** | DB password → **Secret** |
| `DB_NAME` | `recommendation` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |
| `KAFKA_BROKERS` | `localhost:9092` | no | Kafka bootstrap servers |
| `KAFKA_TOPIC` | `order.created` | no | topic consumed |
| `KAFKA_GROUP` | `recommendation` | no | consumer group id |
| `VARIANT` | `a` | no | **A/B knob** — `a` (popular) or `b` (co-purchased) |

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `8090` (HTTP)
- **Image:** build from this dir; push to ECR as `…/recommendation:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable **and** the Kafka consumer is connected)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`,
  `KAFKA_BROKERS`, `KAFKA_TOPIC`, `KAFKA_GROUP`, `VARIANT`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:** its **own Postgres** (separate DB/StatefulSet — database-per-service) and
  **Kafka** (consumes `order.created`, emitted by `order`).
- **Service discovery:** reached by api-gateway at `http://recommendation:8090`
  (`RECOMMENDATION_URL`). Makes no synchronous upstream calls itself.
- **A/B & canary:** `VARIANT` is the A-B knob. Run **two Deployments** (or one with a canary
  subset) — e.g. `variant=a` as stable and `variant=b` as canary — and split traffic with an Istio
  `VirtualService` / `DestinationRule`. The `recommendation_requests_total{variant}` metric lets you
  watch the live split, and each response carries its `variant` for client-side attribution.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 96Mi` · limits `cpu: 250m, memory: 192Mi`
  (Python's baseline RSS is a bit higher than Go's).
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.

> Suggested manifest set: `Deployment` (×2 for a/b or 1 + canary), `Service` (ClusterIP 8090),
> `ConfigMap`, `Secret`, plus the recommendation Postgres StatefulSet, and the Istio
> `VirtualService`/`DestinationRule` for the canary split.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/recommendations`), so
  cardinality stays bounded.
- **Consumer metrics:** `events_consumed_total{topic,result}` (`result` ∈ `ok|duplicate|invalid|error`)
  and `event_processing_duration_seconds{topic}` — the async-worker contract.
- **A/B metric:** `recommendation_requests_total{variant}` for canary observability.
- **Trace correlation:** reads the incoming `traceparent`/B3 trace id and stamps it on every log
  line (`trace_id`).
- **Structured JSON logs** to stdout.
- **Graceful shutdown** via FastAPI lifespan on SIGTERM: the Kafka consumer is drained/stopped, then
  the DB pool closes — clean rollouts + PDB behaviour.
- **Idempotent consumer** (by `order_id`) and **retry-on-connect** for both Postgres and Kafka.
- **Readiness gates on DB + consumer**, so traffic isn't sent to a pod that can't serve.

---

## Project layout

```
recommendation/
├── app/
│   ├── __main__.py       # entrypoint: `python -m app` -> uvicorn
│   ├── main.py           # FastAPI app, middleware, routes, lifespan (+ consumer task)
│   ├── config.py         # env-var configuration (incl. VARIANT + Kafka)
│   ├── db.py             # asyncpg pool (retry-connect + migration) + rankings/writes
│   ├── consumer.py       # aiokafka order.created consumer (idempotent, draining)
│   ├── models.py         # Pydantic schemas (Recommendations)
│   ├── metrics.py        # RED + consumer + variant metrics
│   ├── logging_setup.py  # JSON logging with trace_id
│   └── trace.py          # trace-context extraction + propagation header list
├── requirements.txt
├── Dockerfile            # multi-stage → slim non-root image
├── docker-compose.yml    # LOCAL dev: Postgres + Redpanda + recommendation
├── .env.example
└── README.md
```
