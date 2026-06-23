# Order Service

The checkout **orchestrator** for the platform. A **Go** HTTP service backed by **Postgres** that
turns a user's cart into a confirmed order by fanning out to three downstream services and then
emitting a Kafka event.

This is the service where the mesh gets interesting: it makes **three synchronous inter-service
calls** (`order → cart`, `order → payment`, `order → inventory`) **and** becomes a **Kafka
producer** (`order.created`). It is the hub of the checkout saga and the source of the event that
drives `inventory` (commit), `notification` (notify), and `recommendation` (rank).

```
POST /orders {user_id}:
  client → order ─→ cart        (GET  /carts/{user_id})        400 if empty
                 ─→ payment     (POST /payments)               402 if declined
                 ─→ inventory   (POST /inventory/reserve)      409 passthrough
                 ─→ Postgres    (persist, status "confirmed")
                 ─→ Kafka(order.created)
                 ─→ cart        (DELETE /carts/{user_id})      best-effort clear
```

---

## TL;DR — run it locally

`docker compose up` here starts **order + Postgres + Redpanda (Kafka)**. order is the orchestrator,
so a *full* checkout also needs **cart, payment, and inventory** running — see the note in
`docker-compose.yml`. The compose stack alone is enough to verify the service boots, is ready, and
serves the read endpoints.

```bash
cd services/order
docker compose up --build

# liveness / readiness (readiness = DB reachable + Kafka writer initialized)
curl localhost:8086/healthz
curl localhost:8086/readyz

# create an order (requires cart/payment/inventory reachable + a non-empty cart for the user)
curl -X POST localhost:8086/orders \
  -H 'Content-Type: application/json' \
  -d '{"user_id": 42}'

# fetch one / list a user's orders
curl localhost:8086/orders/1
curl 'localhost:8086/orders?user_id=42'
```

---

## API

Base URL: `http://<host>:8086`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| POST | `/orders` | Orchestrate checkout → create order | `{ "user_id": <int> }` | 201 `Order` |
| GET | `/orders/{id}` | Fetch one order | — | 200 `Order` / 404 |
| GET | `/orders?user_id=` | List a user's orders (newest first) | — | 200 `[Order]` |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable (+ Kafka writer ready) | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | — | 200 |

**`Order`**
```json
{
  "id": 1,
  "user_id": 42,
  "items": [{"product_id": 7, "name": "Widget", "price_cents": 1299, "quantity": 2}],
  "total_cents": 2598,
  "status": "confirmed",
  "created_at": "2026-06-16T12:00:00Z"
}
```

**`POST /orders` outcomes**
| Condition | Status | Body |
|---|---|---|
| Success | 201 | `Order` |
| Empty cart | 400 | `{"error":"cart is empty"}` |
| Payment declined | 402 | `{"error":"payment declined"}` |
| Insufficient stock (inventory 409) | 409 | `{"error":"insufficient stock"}` |
| A downstream is unreachable | 502 | `{"error":"... unavailable"}` |

The client only sends `user_id`. The line items, prices, and total are sourced **authoritatively
from the cart service** — never trusted from the client.

### order_id ordering vs payment (design note)
The contract has us **charge payment and reserve inventory with the same `order_id` we persist**,
and payment happens **before** the order row exists. We **pre-allocate the id from a Postgres
sequence** (`SELECT nextval('orders_id_seq')`) before calling payment, then thread that id through
payment → inventory → the final `INSERT` (id explicit) → the Kafka message key. So every hop of the
saga agrees on the order id. A declined payment simply never inserts, leaving a gap in the sequence
— expected and harmless (sequences aren't gap-free). This is cleaner than inserting a throwaway
"pending" row and updating it.

### Event emitted — `order.created` (Kafka)
After persisting, order emits to topic `order.created` (key = `order_id`), per **api-details §4**:
```json
{
  "event": "order.created",
  "order_id": 1,
  "user_id": 42,
  "items": [{"product_id": 7, "quantity": 2, "price_cents": 1299}],
  "total_cents": 2598,
  "created_at": "2026-06-16T12:00:00Z"
}
```
Consumers (`inventory`, `notification`, `recommendation`) are idempotent by `order_id`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8086` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `order` | no | DB user |
| `DB_PASSWORD` | `order` | **yes** | DB password → **Secret** |
| `DB_NAME` | `order` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |
| `CART_URL` | `http://localhost:8085` | no | cart service base URL |
| `PAYMENT_URL` | `http://localhost:8087` | no | payment service base URL |
| `INVENTORY_URL` | `http://localhost:8088` | no | inventory service base URL |
| `KAFKA_BROKERS` | `localhost:9092` | no | comma-separated broker list (e.g. `kafka:9092`) |
| `KAFKA_TOPIC` | `order.created` | no | topic to produce to |

---

## DevOps handoff — what you need for the manifests

- **Container port:** `8086` (HTTP)
- **Image:** build from this dir; push to ECR as `…/order:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable; also checks the Kafka writer is initialized)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`, `CART_URL`, `PAYMENT_URL`, `INVENTORY_URL`, `KAFKA_BROKERS`, `KAFKA_TOPIC`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:**
  - Its **own Postgres** (separate DB/StatefulSet — database-per-service).
  - **cart** (`CART_URL`), **payment** (`PAYMENT_URL`), **inventory** (`INVENTORY_URL`) reachable.
  - **Kafka** brokers at `KAFKA_BROKERS` (it is a **producer** of `order.created`).
- **Service discovery:** set the `*_URL` vars to the in-cluster Service DNS names. No hardcoded addresses.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 64Mi` · limits `cpu: 250m, memory: 128Mi`
- **Security:** distroless **non-root**; filesystem can be read-only.
- **Istio (later phases):**
  - This is a great node for **fault injection** demos (the `payment` service has a `FAIL_MODE`
    hook and an Istio fault on `order → payment` shows up clearly here).
  - Add `AuthorizationPolicy` to require a valid JWT (gateway injects `X-User-Id`).
  - **NetworkPolicy / Sidecar:** egress to cart, payment, inventory, Kafka; ingress from api-gateway.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8086), `ConfigMap`, `Secret` (DB),
> plus the order Postgres StatefulSet. Kafka topic `order.created` should exist (or rely on auto
> topic creation in dev). Add `VirtualService`/`DestinationRule` once the mesh is in.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/orders/{id}`), so SLO
  dashboards and burn-rate alerts have low-cardinality series.
- **Trace propagation:** forwards W3C (`traceparent`) and B3 headers on **every** downstream hop
  (cart, payment, inventory), so the full checkout shows as one trace in Jaeger/Tempo. Trace id is
  attached to every log line (`trace_id`).
- **Structured JSON logs** to stdout (Loki-friendly).
- **Graceful shutdown** on SIGTERM: drains in-flight checkouts, then closes the Kafka writer and DB
  pool — clean rollouts + PDB behaviour.
- **Readiness gates on the DB**, so traffic isn't sent to a pod that can't serve.

---

## Project layout

```
order/
├── main.go         # entrypoint: config, DB, clients, Kafka producer, server, graceful shutdown
├── config.go       # env-var configuration + JSON logger
├── models.go       # Order / OrderItem / CreateOrderInput + validation
├── store.go        # Postgres order store (retry-connect, migration, sequence-based id)
├── clients.go      # trace-propagating clients for cart / payment / inventory
├── producer.go     # Kafka producer for order.created (segmentio/kafka-go)
├── handlers.go     # HTTP routes, the checkout orchestration, helpers
├── metrics.go      # RED metrics + logging middleware (route label, trace id)
├── trace.go        # trace-context extraction + propagation helpers
├── Dockerfile      # multi-stage → distroless non-root image
├── docker-compose.yml  # LOCAL dev: order + Postgres + Redpanda
├── .env.example
└── README.md
```

## Notes for later phases
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum`. The Dockerfile
  tolerates its absence for now.
- **Outbox pattern:** persist + emit are not atomic (at-least-once, best-effort emit). If the broker
  hiccups after the row is committed, downstream consumers miss the event. A transactional
  **outbox** (write the event to an `outbox` table in the same tx, relay async) is the Phase 5–6 fix.
- **Saga compensation:** today a payment is captured before inventory is reserved; an inventory 409
  after a successful charge would need a refund/void compensation. Left documented as a learning
  point rather than hidden.
```
