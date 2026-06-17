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
9. **Per-microservice deliverables:** source + `Dockerfile` + `README.md` (with a "📦 DevOps handoff"
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
| user | Python | 8082 | `http://user:8082` (`USER_URL`) | Postgres |
| catalog | Go | 8083 | `http://catalog:8083` (`CATALOG_URL`) | Postgres |
| search | Python | 8084 | `http://search:8084` (`SEARCH_URL`) | Elasticsearch |
| cart | Go | 8085 | `http://cart:8085` (`CART_URL`) | Redis |
| order | Go | 8086 | `http://order:8086` (`ORDER_URL`) | Postgres |
| payment | Go | 8087 | `http://payment:8087` (`PAYMENT_URL`) | Postgres |
| inventory | Python | 8088 | `http://inventory:8088` (`INVENTORY_URL`) | Postgres |
| notification | Python | 8089 | (async only) | — |
| recommendation | Python | 8090 | `http://recommendation:8090` (`RECOMMENDATION_URL`) | Postgres |
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

### user (built) — `:8082`
`POST /users {email, full_name}` → 201 `User` · `GET /users/{id}` · `GET /users` ·
`PUT /users/{id}` · `GET /users/by-email?email=`. `User = {id,email,full_name,created_at,updated_at}`

### cart — `:8085` (Redis), calls **catalog**
- `GET /carts/{user_id}` → `Cart`
- `POST /carts/{user_id}/items {product_id, quantity}` → 200 `Cart`
  - Validates product via `GET {CATALOG_URL}/products/{product_id}` (404 → 404 "product not found").
  - Snapshots `name` + `price_cents` from catalog into the cart line.
- `DELETE /carts/{user_id}/items/{product_id}` → 200 `Cart`
- `DELETE /carts/{user_id}` → 204 (clear)
- `Cart = {user_id, items:[{product_id, name, price_cents, quantity}], total_cents}`
- Redis key: `cart:{user_id}` storing the JSON cart. `INVENTORY`-agnostic.

### payment — `:8087` (Postgres), **fault target**
- `POST /payments {order_id, user_id, amount_cents}` → 201 `Payment`
  - Mock authorizer: **approve** by default. Env `FAIL_MODE` (`off|decline|error`) forces declines
    (`status:"declined"`, still 201) or `error` (500) — a deterministic hook for chaos/fault demos
    alongside Istio fault injection.
- `GET /payments/{id}` → `Payment`
- `Payment = {id, order_id, user_id, amount_cents, status:"approved"|"declined", created_at}`

### inventory — `:8088` (Postgres), sync **and** async
- `GET /inventory/{product_id}` → `{product_id, available, reserved}` (404 if unseeded)
- `PUT /inventory/{product_id} {available}` → set/seed stock (so you can populate it)
- `POST /inventory/reserve {order_id, items:[{product_id, quantity}]}` → 200 `{reserved:true}`
  - Decrement `available`, increment `reserved` atomically; **409** `{error:"insufficient stock"}` if any line can't be met (reserve nothing — all-or-none).
  - **Idempotent by `order_id`** (re-reserving the same order is a no-op success).
- **Async:** consumes `order.created` → commits the reservation (reserved → sold); idempotent by order_id.

### order — `:8086` (Postgres), **orchestrator**, calls cart/payment/inventory + emits Kafka
- `POST /orders {user_id}` → 201 `Order`
  1. `GET {CART_URL}/carts/{user_id}` — 400 if empty cart.
  2. `POST {PAYMENT_URL}/payments {order_id(generated), user_id, amount_cents}` — declined → 402 `{error:"payment declined"}`.
  3. `POST {INVENTORY_URL}/inventory/reserve {order_id, items}` — 409 → 409 passthrough.
  4. Persist order (`status:"confirmed"`), **emit `order.created`** to Kafka.
  5. `DELETE {CART_URL}/carts/{user_id}` (best-effort clear).
- `GET /orders/{id}` → `Order` · `GET /orders?user_id=` → `[Order]`
- `Order = {id, user_id, items:[{product_id, name, price_cents, quantity}], total_cents, status, created_at}`

### search — `:8084` (Elasticsearch), calls **catalog**
- `GET /search?q=&limit=` → `{query, hits:[Product-ish], total}` (queries the ES index)
- `POST /reindex` → pulls all products from `GET {CATALOG_URL}/products` and bulk-indexes them; returns `{indexed: n}`
- Index name `products`. Readiness depends on Elasticsearch reachable.

### recommendation — `:8090` (Postgres), async + sync
- `GET /recommendations?user_id=` or `?product_id=` → `{variant, product_ids:[...]}`
- **A/B:** env `VARIANT` (`a|b`) changes the ranking strategy; include it in the response + a
  `recommendation_requests_total{variant}` metric.
- **Async:** consumes `order.created` → increments co-purchase / popularity counts used by the rankings.

### notification — `:8089` (no DB), async only (+ ops endpoints)
- Consumes `order.created` → "sends" a notification (structured log line; no real email).
- `GET /notifications?limit=` → last N notifications from an in-memory ring buffer (for demo/inspection).
- Readiness depends on the Kafka consumer being connected.

### api-gateway — `:8080`, edge router (reverse proxy) to everything
- Path → upstream:
  - `/api/auth/*` → `AUTH_URL` (strip `/api/auth`)
  - `/api/users/*` → `USER_URL` (strip `/api/users` → `/users...`)
  - `/api/products/*` → `CATALOG_URL`
  - `/api/search/*` → `SEARCH_URL`
  - `/api/cart/*` → `CART_URL` (→ `/carts...`)
  - `/api/orders/*` → `ORDER_URL`
  - `/api/recommendations/*` → `RECOMMENDATION_URL`
- **AuthN:** for protected prefixes (`/api/cart`, `/api/orders`), require `Authorization: Bearer`,
  verify the JWT against auth's JWKS (`GET {AUTH_URL}/.well-known/jwks.json`, cache it), reject 401
  if invalid, and inject `X-User-Id: <sub>` to the upstream. Public: `/api/auth/*`, `/api/products/*`, `/api/search/*`.
- Use `net/http/httputil.ReverseProxy`; preserve/forward trace headers (proxy passes them through).
- `GET /healthz` · `GET /readyz` (200 if it can reach auth's JWKS) · `GET /metrics`.

### frontend — `:3000` (Node/Express)
- Serves a tiny single-page UI (static HTML+JS) that calls the gateway: register/login, browse
  products, add to cart, checkout, view orders. Keep it minimal but functional.
- Runtime config: `GET /config` returns `{apiGatewayUrl}` from `API_GATEWAY_URL` env so the page
  isn't built with a hardcoded backend (browser hits the gateway directly).
- `GET /healthz` · `GET /readyz` · `GET /metrics` (prom-client default + a page-view counter).

### thumbnail-job — worker (Python), KEDA-driven, **no HTTP server required**
- Consumes Kafka topic `thumbnail.requests` (`KAFKA_GROUP=thumbnail`), "processes" each image
  (simulate work: read `{product_id, image_url}`, sleep ~200ms, log result). Idempotent.
- Exposes a tiny metrics endpoint on `:9100` (`thumbnails_processed_total`) OR pushes nothing —
  prefer a minimal HTTP `/metrics` + `/healthz` on `:9100` so KEDA/Prometheus can see it.
- Designed for **KEDA scale-to-zero** on Kafka lag. Include a `producer.py` helper to enqueue test
  messages, and document the ScaledObject trigger (kafka lag) in the README for the DevOps side.

---

## 4. Kafka event schemas

**Topic `order.created`** (emitted by `order`; consumed by `inventory`, `notification`, `recommendation`):
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
- Key = `order_id` (string). Consumers must be **idempotent by `order_id`**.
- JSON, UTF-8. Producers set the key so partitioning is stable.

**Topic `thumbnail.requests`** (consumed by `thumbnail-job`):
```json
{ "product_id": 7, "image_url": "https://example/img/7.jpg" }
```

---

## 5. Cross-microservice call map (for your Istio routing & NetworkPolicies)

```
frontend ─→ api-gateway ─┬─→ auth ─→ user
                         ├─→ user
                         ├─→ catalog
                         ├─→ search ─→ catalog
                         ├─→ cart ─→ catalog
                         ├─→ order ─→ cart
                         │           ─→ payment
                         │           ─→ inventory
                         │           ─→ Kafka(order.created)
                         └─→ recommendation

Kafka(order.created) ─→ inventory (commit)
                     ─→ notification (notify)
                     ─→ recommendation (rank)

Kafka(thumbnail.requests) ─→ thumbnail-job
```
