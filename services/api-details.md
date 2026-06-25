# API Details — the integration spec

This is the **authoritative** contract every microservice is built against: shared conventions, ports,
env-var wiring, HTTP APIs, and Kafka event schemas. If code and this doc disagree, this doc wins
(or we change it deliberately). It exists so microservices actually integrate and so your Istio routing,
ConfigMaps, and AuthorizationPolicies target stable shapes.

Reference implementations to copy style from:
- **Go microservices** → mirror `services/auth` (config.go, main.go, store.go, handlers.go, metrics.go, trace.go, models.go).
- **Python microservices** → mirror `services/user` (`app/` package: config, db, metrics, logging_setup, trace, main, __main__).

---

## 1. Conventions EVERY microservice must follow (non-negotiable)

1. **Config 100% via env vars** (no hardcoded addresses). Local-dev defaults allowed.
2. **Operational endpoints:**
   - `GET /healthz` — liveness, no dependency checks, always 200 if process up.
   - `GET /readyz` — readiness, 200 only if critical deps (DB / broker / ES) reachable, else 503.
   - `GET /metrics` — Prometheus exposition.
3. **RED metrics** (exact names, so dashboards are uniform):
   - `http_requests_total{route,method,status}` (counter)
   - `http_request_duration_seconds{route,method}` (histogram, default buckets)
   - `route` is the **route template** (e.g. `/orders/{id}`), never the raw path — bounded cardinality.
   - Async workers (consumers) instead expose: `events_consumed_total{topic,result}` and
     `event_processing_duration_seconds{topic}`.
4. **Structured JSON logs to stdout**, one object per line, including a `trace_id` field.
5. **Trace propagation:** read the incoming trace id (W3C `traceparent`, then B3 `x-b3-traceid`,
   then `x-request-id`) for logs; **forward these headers on every downstream call** so one trace
   spans microservices. Header list:
   `x-request-id, traceparent, tracestate, x-b3-traceid, x-b3-spanid, x-b3-parentspanid, x-b3-sampled, x-b3-flags, b3`.
6. **Graceful shutdown** on SIGTERM: stop accepting new work, drain in-flight, close pools, exit.
7. **Retry the initial dependency connect** (DB/broker/ES pod may not be ready at startup).
8. **Dockerfile**: multi-stage, small image, **non-root**. Go → `distroless/static-debian12:nonroot`
   (reuse `auth`'s Dockerfile incl. the `-mod=mod` retry loop). Python → `python:3.12-slim` non-root
   (reuse `user`'s Dockerfile). Node → `node:20-slim` non-root.
9. **Per-microservice deliverables:** source + `Dockerfile` + `README.md` (with a "DevOps handoff"
   section listing container port, probes, ConfigMap keys, Secret keys, dependencies, resource
   hints) + `.env.example` + `docker-compose.yml` (LOCAL dev only) + the microservice's
   config/secret key list.
10. **Error responses:** JSON `{"error":"message"}` with the right status (400/401/404/409/5xx).

### Library choices (keep the polyglot stack consistent)
- Go HTTP: stdlib `net/http` + Go 1.22 `ServeMux` (method+path patterns). No web framework.
- Go Postgres: `github.com/jackc/pgx/v5`. Go Redis: `github.com/redis/go-redis/v9`.
- Go Kafka: `github.com/segmentio/kafka-go` (pure Go, builds on distroless static).
- Go JWT: `github.com/golang-jwt/jwt/v5`.
- Python web: FastAPI + uvicorn (run via `python -m app`). Python Postgres: `asyncpg`.
- Python Kafka: `aiokafka` (pure Python). Python ES: `elasticsearch` (AsyncElasticsearch).
- Node: `express` + `prom-client`; Node 20 global `fetch`.

---

## 2. Ports & service-discovery env vars

| Microservice | Lang | Port | Reached by others at | Datastore |
|---|---|---|---|---|
| api-gateway | Go | 8080 | (edge) | — |
| auth | Go | 8081 | `http://auth:8081` (`AUTH_URL`) | Postgres |
| catalog | Go | 8083 | `http://catalog:8083` (`CATALOG_URL`) | Postgres |
| order | Go | 8086 | `http://order:8086` (`ORDER_URL`) | Postgres |
| inventory | Python | 8088 | `http://inventory:8088` (`INVENTORY_URL`) | Postgres |
| frontend | Node | 3000 | (edge UI) | — |

**Kafka:** brokers via `KAFKA_BROKERS` (e.g. `kafka:9092`). Consumers set `KAFKA_GROUP`.

---

## 3. HTTP API contracts

### catalog (built) — `:8083`
`GET /products` · `POST /products` · `GET /products/{id}` · `PUT /products/{id}` · `DELETE /products/{id}`
`Product = {id, name, description, price_cents, stock, created_at, updated_at}`

### auth (built) — `:8081`
`POST /register` · `POST /login` → `{access_token, token_type, expires_in}` · `GET /validate` ·
`GET /.well-known/jwks.json`. JWT claims: `iss,aud,sub(=user_id),email,iat,exp`, RS256, `kid` header.

### inventory — `:8088` (Postgres), sync **and** async
- `GET /inventory/{product_id}` → `{product_id, available, reserved}` (404 if unseeded)
- `PUT /inventory/{product_id} {available}` → set/seed stock (so you can populate it)
- `POST /inventory/reserve {order_id, items:[{product_id, quantity}]}` → 200 `{reserved:true}`
  - Decrement `available`, increment `reserved` atomically; **409** `{error:"insufficient stock"}` if any line can't be met (reserve nothing — all-or-none).
  - **Idempotent by `order_id`** (re-reserving the same order is a no-op success).
- **Async:** consumes `order.created` → commits the reservation (reserved → sold); idempotent by order_id.

### order — `:8086` (Postgres), **orchestrator**, calls inventory + emits Kafka
- `POST /orders {user_id, items:[{product_id, name, price_cents, quantity}]}` → 201 `Order`
  - *Trimmed build:* the client submits the line items directly (no cart/payment service); the total is computed server-side.
  1. Pre-allocate `order_id`; compute `total_cents` from the items.
  2. `POST {INVENTORY_URL}/inventory/reserve {order_id, items}` — 409 → 409 passthrough.
  3. Persist order (`status:"confirmed"`), **emit `order.created`** to Kafka.
- `GET /orders/{id}` → `Order` · `GET /orders?user_id=` → `[Order]`
- `Order = {id, user_id, items:[{product_id, name, price_cents, quantity}], total_cents, status, created_at}`

### api-gateway — `:8080`, edge router (reverse proxy)
- Path → upstream:
  - `/api/auth/*` → `AUTH_URL` (strip `/api/auth`) — public
  - `/api/products/*` → `CATALOG_URL` — public
  - `/api/orders/*` → `ORDER_URL` — **protected**
- **AuthN:** for the protected prefix (`/api/orders`), require `Authorization: Bearer`, verify the JWT
  against auth's JWKS (`GET {AUTH_URL}/.well-known/jwks.json`, cache it), reject 401 if invalid, and
  inject `X-User-Id: <sub>` to the upstream. Public: `/api/auth/*`, `/api/products/*`.
- Use `net/http/httputil.ReverseProxy`; preserve/forward trace headers (proxy passes them through).
- `GET /healthz` · `GET /readyz` (200 if it can reach auth's JWKS) · `GET /metrics`.

### frontend — `:3000` (Node/Express)
- Serves a tiny single-page UI (static HTML+JS) that calls the gateway: register/login, browse
  products, place an order (submit line items), view orders. Keep it minimal but functional.
- Runtime config: `GET /config` returns `{apiGatewayUrl}` from `API_GATEWAY_URL` env so the page
  isn't built with a hardcoded backend (browser hits the gateway directly).
- `GET /healthz` · `GET /readyz` · `GET /metrics` (prom-client default + a page-view counter).

---

## 4. Kafka event schemas

**Topic `order.created`** (emitted by `order`; consumed by `inventory`):
```json
{
  "event": "order.created",
  "order_id": 123,
  "user_id": 42,
  "items": [{"product_id": 7, "quantity": 2, "price_cents": 1299}],
  "total_cents": 2598,
  "created_at": "2026-06-16T12:00:00Z"
}
```
- Key = `order_id` (string). The consumer must be **idempotent by `order_id`**.
- JSON, UTF-8. Producers set the key so partitioning is stable.

---

## 5. Cross-microservice call map (for your Istio routing & NetworkPolicies)

```
frontend ─→ api-gateway ─┬─→ auth
                         ├─→ catalog
                         └─→ order ─→ inventory
                                    ─→ Kafka(order.created)

Kafka(order.created) ─→ inventory (commit reservation)
```
