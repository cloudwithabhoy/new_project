# Cart Service

The shopping cart for the platform. A small **Go** HTTP service backed by **Redis** that holds each
user's cart as a single JSON document. When an item is added it **validates the product against the
catalog service** and **snapshots** the product's `name` + `price_cents` into the cart line, so the
cart stays correct even if catalog prices change later.

This service introduces a **synchronous inter-service call**: `cart → catalog` (to validate a product
and snapshot its name + price) — another real distributed trace through the mesh.

```
add item:  client → cart → catalog     (cart validates product, snapshots name+price, stores in Redis)
get cart:  client → cart                (read the JSON document at cart:{user_id})
clear:     order  → cart                (DELETE the cart after a successful checkout)
```

---

## TL;DR — run it locally

`docker compose up` here starts **cart + catalog + Redis + catalog's Postgres** together:

```bash
cd services/cart
docker compose up --build

# seed a product in catalog first
curl -X POST localhost:8083/products \
  -H 'Content-Type: application/json' \
  -d '{"name":"Widget","description":"a widget","price_cents":1299,"stock":10}'

# add it to user 42's cart (cart validates it against catalog + snapshots name/price)
curl -X POST localhost:8085/carts/42/items \
  -H 'Content-Type: application/json' \
  -d '{"product_id":1,"quantity":2}'

# read the cart back
curl localhost:8085/carts/42

# remove a line
curl -X DELETE localhost:8085/carts/42/items/1

# clear the whole cart
curl -X DELETE localhost:8085/carts/42
```

---

## API

Base URL: `http://<host>:8085`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| GET | `/carts/{user_id}` | Get the cart (empty cart if none) | — | 200 |
| POST | `/carts/{user_id}/items` | Add/increment an item (calls `catalog`) | `AddItemInput` | 200 |
| DELETE | `/carts/{user_id}/items/{product_id}` | Remove an item line | — | 200 |
| DELETE | `/carts/{user_id}` | Clear the cart | — | 204 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — Redis reachable | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | — | 200 |

**`AddItemInput`**
```json
{ "product_id": 1, "quantity": 2 }
```
**`Cart`**
```json
{
  "user_id": "42",
  "items": [
    { "product_id": 1, "name": "Widget", "price_cents": 1299, "quantity": 2 }
  ],
  "total_cents": 2598
}
```

Behaviour notes:
- **Empty cart** — `GET` on an unknown user returns `{"user_id":"...","items":[],"total_cents":0}` (200, never 404).
- **Product validation** — `POST /items` calls `GET {CATALOG_URL}/products/{product_id}`; a catalog
  **404** returns **404 `{"error":"product not found"}`**. Other catalog failures return 502.
- **Snapshot** — `name` + `price_cents` are copied from catalog at add-time; a later catalog price
  change does **not** mutate an existing cart line.
- **Increment** — adding a product already in the cart increments its quantity (no duplicate line).
- **Idempotent deletes** — removing/clearing an absent item or cart is a no-op success.
- `total_cents` is always recomputed from the lines before the cart is stored or returned.

Errors return `{"error":"message"}`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8085` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `REDIS_ADDR` | `localhost:6379` | no | Redis host:port (k8s Service name) |
| `REDIS_PASSWORD` | *(empty)* | **yes** | Redis AUTH password → **Secret** (may be empty) |
| `REDIS_DB` | `0` | no | Redis logical DB index |
| `CATALOG_URL` | `http://localhost:8083` | no | Base URL of the catalog service |

---

## DevOps handoff — what you need for the manifests

- **Container port:** `8085` (HTTP)
- **Image:** build from this dir; push to ECR as `…/cart:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Redis reachable)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `REDIS_ADDR`, `REDIS_DB`, `CATALOG_URL`
- **Secret keys:** `REDIS_PASSWORD` (may be empty — still keep it in a Secret, not the ConfigMap)
- **Dependencies:**
  - Its **own Redis** (Deployment/StatefulSet or a managed ElastiCache endpoint).
  - The **catalog service** reachable at `CATALOG_URL` (e.g. `http://catalog:8083`).
- **Service discovery:** set `CATALOG_URL` to the catalog Service DNS name. No hardcoded addresses.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 64Mi` · limits `cpu: 250m, memory: 128Mi`
- **Security:** distroless **non-root**; filesystem can be read-only.
- **Istio (later phases):** an `AuthorizationPolicy` can require a valid JWT on `cart` once the mesh's
  `RequestAuthentication` points at auth's JWKS. cart is called by `api-gateway` (user traffic) and by
  `order` (read cart + clear on checkout) — size your `NetworkPolicy`/`AuthorizationPolicy` for both.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8085), `ConfigMap`, `Secret`
> (Redis password), plus a Redis Deployment/StatefulSet (or external ElastiCache).

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/carts/{user_id}`), so SLO
  dashboards and burn-rate alerts have low-cardinality series.
- **Trace propagation:** forwards W3C (`traceparent`) and B3 headers on the `cart → catalog` call, so
  the hop shows as one trace in Jaeger/Tempo. Trace id is attached to every log line (`trace_id`).
- **Structured JSON logs** to stdout (Loki-friendly).
- **Graceful shutdown** on SIGTERM (drains in-flight requests) — clean rollouts + PDB behaviour.
- **Readiness gates on Redis**, so traffic isn't sent to a pod that can't serve.

---

## Project layout

```
cart/
├── main.go            # entrypoint: config, Redis store, catalog client, server, graceful shutdown
├── config.go          # env-var configuration + JSON logger
├── models.go          # Cart / CartItem / AddItemInput + validation + total recompute
├── store.go           # Redis cart store (retry-connect, get/save/delete JSON document)
├── catalogclient.go   # synchronous client for the catalog service (trace-propagating)
├── handlers.go        # HTTP routes, handlers, helpers
├── metrics.go         # RED metrics + logging middleware (route label, trace id)
├── trace.go           # trace-context extraction + propagation helpers
├── Dockerfile         # multi-stage → distroless non-root image
├── docker-compose.yml # LOCAL dev: cart + catalog + Redis + catalog Postgres
├── .env.example
└── README.md
```

## Notes for later phases
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum`. The Dockerfile
  tolerates its absence for now.
- **TTL / expiry:** carts are stored without an expiry today. A natural enhancement is a Redis TTL
  (e.g. abandon carts after N days) — set it in `store.Save`.
