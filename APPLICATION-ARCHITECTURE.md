# Application Architecture — how the e-commerce platform works

How the application behaves **before** any Kubernetes/Istio wrapping — what each microservice does, how
a request moves through the system, and where the sync and async paths are. Read this so your manifests,
probes, routing, NetworkPolicies, and dashboards are built against how the app *actually* works.

- **API / endpoint / env detail** → [`services/api-details.md`](./services/api-details.md)
- **Per-service "DevOps handoff"** (ports, probes, ConfigMap/Secret keys) → each `services/<name>/README.md`
- **Deploy one region** → [`SINGLE-REGION-ARCHITECTURE.md`](./SINGLE-REGION-ARCHITECTURE.md) · **go global** → [`DEPLOYMENT-ARCHITECTURE.md`](./DEPLOYMENT-ARCHITECTURE.md)
- **The ordered build plan** → [`requirement-execution-plan/`](./requirement-execution-plan/) · **front door** → [`README.md`](./README.md)

This file is the **map** of the app; those are the **detail**.

---

## 1. The system in one paragraph

A customer hits the **frontend** (web UI), which calls a single edge **api-gateway**. The gateway
authenticates the request (JWT) and routes it to one of 12 small microservices. Most calls are
**synchronous HTTP/REST**. Placing an order is the one rich flow: **order** orchestrates a synchronous
chain (cart → payment → inventory), then publishes an **`order.created`** event to **Kafka**, which
**inventory**, **notification**, and **recommendation** consume **asynchronously**. Each service owns
**its own datastore** (database-per-service). A separate **thumbnail-job** worker drains an image queue
and scales to zero. The business logic is deliberately thin — these 13 components exist to give the
*infrastructure* realistic **sync + async** traffic to mesh, scale, observe, and break.

---

## 2. Microservice catalog

| Microservice | Lang | Port | Datastore | Role | Calls (sync) | Events |
|---|---|---|---|---|---|---|
| **frontend** | Node | 3000 | — | Web UI (SPA) | api-gateway | — |
| **api-gateway** | Go | 8080 | — | Edge router + JWT auth | all services | — |
| **auth** | Go | 8081 | Postgres | Credentials, mints JWTs, JWKS | user | — |
| **user** | Python | 8082 | Postgres | User profiles (leaf) | — | — |
| **catalog** | Go | 8083 | Postgres | Product CRUD | — | — |
| **search** | Python | 8084 | Elasticsearch | Product search / reindex | catalog | — |
| **cart** | Go | 8085 | Redis | Shopping cart | catalog | — |
| **order** | Go | 8086 | Postgres | **Checkout orchestrator** | cart, payment, inventory | **produces** `order.created` |
| **payment** | Go | 8087 | Postgres | Mock charge (fault target) | — | — |
| **inventory** | Python | 8088 | Postgres | Stock reserve + commit | — | **consumes** `order.created` |
| **notification** | Python | 8089 | in-memory | Sends confirmations | — | **consumes** `order.created` |
| **recommendation** | Python | 8090 | Postgres | A/B recommendations | — | **consumes** `order.created` |
| **thumbnail-job** | Python | 9100¹ | — | Image processor (KEDA) | — | **consumes** `thumbnail.requests` |

¹ `9100` is only a metrics/health port — `thumbnail-job` is a queue worker, not an HTTP API.

**Datastores:** **Postgres ×7** (auth, user, catalog, order, payment, inventory, recommendation — each
its own DB) · **Redis ×1** (cart) · **Elasticsearch ×1** (search) · **Kafka ×1** (the event bus).

---

## 3. Topology — three views

### 3a. Layers (request top to bottom)
```
   ┌───────────────────────────────────────────────────────────────────────┐
   │  EDGE                                                                   │
   │   browser ──► frontend (:3000, SPA) ──► api-gateway (:8080)            │
   │                        (browser then calls the gateway directly)       │
   │   api-gateway: verify JWT via JWKS · route /api/* · inject X-User-Id   │
   └────────────────────────────────────┬──────────────────────────────────┘
                                        ▼  /api/*
   ┌───────────────────────────────────────────────────────────────────────┐
   │  SERVICES   (each = a pod + Envoy sidecar, own ClusterIP)              │
   │   auth   user   catalog   search   cart   order   payment             │
   │   inventory   notification   recommendation        (+ thumbnail-job)   │
   └────────────────────────────────────┬──────────────────────────────────┘
                                        ▼  owns
   ┌───────────────────────────────────────────────────────────────────────┐
   │  DATA  (database-per-service, StatefulSets)                           │
   │   Postgres ×7  ·  Redis (cart)  ·  Elasticsearch (search)  ·  Kafka    │
   └───────────────────────────────────────────────────────────────────────┘
```

### 3b. Synchronous call graph (who calls whom, over HTTP)
```
   browser ──► frontend ──► api-gateway ──┬──► auth ──► user
                                          ├──► user
                                          ├──► catalog
                                          ├──► search ──► catalog
                                          ├──► cart ──► catalog
                                          ├──► order ──┬──► cart
                                          │            ├──► payment
                                          │            └──► inventory
                                          └──► recommendation
```

### 3c. Asynchronous events (Kafka, non-blocking)
```
   order ──publish──► Kafka topic  order.created  (key = order_id) ──┬──► inventory      commit reservation
                                                                     ├──► notification   send confirmation
                                                                     └──► recommendation update counts

   producer ──────► Kafka topic  thumbnail.requests ──────────────────► thumbnail-job   (KEDA scales 0 → N → 0)
```

**Two traffic shapes on purpose:** **sync** (blocking request/response through the gateway + order's
chain) and **async** (non-blocking event fan-out + the thumbnail queue).

---

## 4. Communication patterns

**Synchronous — HTTP/REST.** Plain JSON. No service hardcodes another's address; every dependency URL is
an **env var** (`CATALOG_URL`, `PAYMENT_URL`, …) supplied via ConfigMap, resolving in-cluster to
Kubernetes Service DNS (`http://catalog:8083`). Each caller **forwards trace-context headers**
(`traceparent`, `x-b3-*`, `x-request-id`) so one user action is a single distributed trace.

**Asynchronous — Kafka events.** `order` writes one `order.created` message (key = `order_id`) after a
successful checkout. `inventory`, `notification`, `recommendation` each have their **own consumer group**,
so all three receive every event independently. Delivery is **at-least-once**, so every consumer is
**idempotent by `order_id`** — which is why a consumer pod can be restarted safely.

---

## 5. End-to-end flows

### Checkout — the centerpiece (sync chain, then async fan-out)
```
   browser ──► api-gateway ──► order    POST /orders {user_id}
   ┌─ order orchestrates  (one trace · one pre-allocated order_id) ────────────────┐
   │  1.  GET    cart/carts/{user_id}         → items + total       (400 if empty)   │
   │  2.  POST   payment/payments {order_id}  → approved / declined  (402 if declined)│
   │  3.  POST   inventory/reserve {order_id} → reserve stock        (409 if short)   │
   │  4.  INSERT order row (status confirmed) → order's Postgres                      │
   │  5.  PUBLISH order.created ──► Kafka      (key = order_id)                        │
   │  6.  DELETE cart/carts/{user_id}         (best-effort clear)                      │
   └───────────────────────────────────────────────┬─────────────────────────────────┘
                                                    ▼  201 Order   (buyer is done here)
   async, independently:   Kafka order.created ──┬──► inventory      commit (reserved → sold)
                                                 ├──► notification   send confirmation
                                                 └──► recommendation update popularity / co-purchase
```
This one action is a **multi-hop sync trace** (`gateway → order → payment → inventory`) **and** an
**event fan-out** — exactly what the mesh, tracing, and scaling have to show.

### The other flows (concise)
```
Register  frontend ─► gw (/api/auth/register, public) ─► auth ─► POST user/users (creates profile)
                      ─► bcrypt-hash ─► store credential in auth's Postgres ─► 201   (first traced sync hop)
Login     frontend ─► gw (/api/auth/login, public) ─► auth ─► bcrypt-compare ─► mint RS256 JWT ─► 200
                      browser then sends  Authorization: Bearer <jwt>   (verified anywhere via JWKS)
Browse    frontend ─► gw (/api/products) ─► catalog ─► Postgres
Search    frontend ─► gw (/api/search?q=) ─► search ─► Elasticsearch   (POST /reindex pulls from catalog)
Add cart  frontend ─► gw (/api/cart, PROTECTED) ─► cart ─► GET catalog/products/{id} (snapshot name+price)
                      ─► store JSON cart at Redis  cart:{user_id}
Thumbnail producer ─► Kafka thumbnail.requests ─► thumbnail-job  (process ~200ms; KEDA scales on lag)
```
`auth` owns **secrets** (password hash + signing key); `user` owns the **profile** — separate databases.

---

## 6. Edge request lifecycle (auth enforcement)

```
            ┌──────────────────────────── api-gateway ────────────────────────────┐
  request ─►│  match path prefix → pick upstream                                   │
            │                                                                       │
            │  PUBLIC   (/api/auth, /api/products, /api/search)  ─► pass through    │
            │                                                                       │
            │  PROTECTED (/api/cart, /api/orders):                                  │
            │     - require  Authorization: Bearer <jwt>                            │
            │     - verify RS256 signature against cached JWKS (refresh on new kid) │
            │     - valid   ─► strip client X-User-Id, inject trusted X-User-Id     │
            │     - invalid ─► 401  (never reaches the upstream)                     │
            └───────────────────────────────────┬───────────────────────────────────┘
                                                ▼  ReverseProxy (trace headers preserved)
                                          upstream microservice
```
> **DevOps note:** this app-level JWT check is the *starting* point. From the Istio phase you move
> enforcement into the mesh — `RequestAuthentication` (validates the JWT against the same JWKS URL) +
> `AuthorizationPolicy` (require a valid principal to reach `order`/`cart`). The app stays as a safety
> net and the `X-User-Id` provider.

---

## 7. Cross-cutting contract (every service guarantees these — and why you care)

| App feature | Endpoint / behavior | DevOps use |
|---|---|---|
| **Liveness** | `GET /healthz` (no deps) | `livenessProbe` — restart a wedged pod |
| **Readiness** | `GET /readyz` (checks DB/broker/ES) | `readinessProbe` — keep traffic off a pod that can't serve; gates rollouts |
| **RED metrics** | `GET /metrics` → `http_requests_total{route,method,status}`, `http_request_duration_seconds` | Prometheus scrape; HPA custom metrics; SLO dashboards |
| **Async metrics** | consumers expose `events_consumed_total`, `event_processing_duration_seconds` | KEDA scaling signal; consumer-lag dashboards |
| **Trace propagation** | forwards `traceparent`/`b3` headers downstream | end-to-end traces in Jaeger/Tempo through Istio |
| **Structured JSON logs** | stdout, one JSON/line, with `trace_id` | Loki parsing; log ↔ trace correlation |
| **Graceful shutdown** | drains on SIGTERM | clean rolling updates, node drains, respects PDBs |
| **Retry initial connect** | tolerates DB/broker/ES not-ready at boot | no crash-loop just because a datastore starts a few seconds later |

The `route` label is always the **template** (`/orders/{id}`), never the raw path — bounded cardinality
so Prometheus doesn't explode.

---

## 8. Startup order (what blocks what)

Services tolerate late dependencies (retry-connect), so strict ordering isn't *required* — but this is
the sane bring-up order:
```
   1  Datastores      Postgres (×7) · Redis · Elasticsearch · Kafka
   2  Core            catalog · user
   3  Auth            auth                 (needs user)
   4  Mid             cart (needs catalog) · search (needs catalog) · payment · inventory
   5  Orchestrator    order                (needs cart, payment, inventory, Kafka)
   6  Consumers       inventory* · notification · recommendation   (need Kafka)
   7  Edge            api-gateway (needs auth's JWKS for /readyz) ─► frontend
   8  Workers         thumbnail-job        (needs Kafka)
```
A service's `/readyz` returning 503 means one of *its* deps isn't up yet — not a crash.

---

## 9. Failure modes (what to expect under chaos)

| Inject | What the app does | What you observe |
|---|---|---|
| `payment` 500 (`FAIL_MODE=error` / Istio fault) | checkout fails fast | 5xx on `/api/orders`; trace shows the failing hop; SLO burn |
| `payment` declines (`FAIL_MODE=decline`) | `order` returns **402** | order never persisted; no event emitted |
| insufficient stock | `inventory/reserve` → **409** (all-or-none) | order rejected; stock unchanged |
| consumer pod dies mid-event | at-least-once redelivery; idempotent by `order_id` | no double-commit; consumer lag blips then recovers |
| `catalog` down | `cart`/`search` return **502** | add-to-cart/search fail; browse may still serve cached ES |
| `order` can't reach Kafka | sync part still returns 201; emit is best-effort | **known gap:** no transactional outbox |
| DB starts late | retry-connect; `/readyz` 503 until up | pod stays un-ready, no traffic, no crash-loop |

> Two **deliberate** gaps (good chaos/learning material, not bugs): (1) **no outbox** — a crash between
> persist and publish can drop an event; (2) **no compensation** — a successful charge followed by an
> inventory 409 isn't auto-refunded. These are where you'd add sagas later.

---

## 10. App → infra mapping (what you hand-write per service)

```
   per service:   Deployment (image from ECR · envFrom ConfigMap+Secret · liveness=/healthz ·
                              readiness=/readyz · requests/limits · SIGTERM grace)
                  + Service (ClusterIP, port from §2)
                  + ConfigMap (ports, *_URL, tunables)   + Secret (DB password, JWT_PRIVATE_KEY_PEM)
   per datastore: StatefulSet + PVC (Postgres/Redis/ES/Kafka)   — one per owning service
   later (mesh+): VirtualService/DestinationRule · PeerAuthentication (mTLS) ·
                  RequestAuthentication + AuthorizationPolicy (JWT) · HPA/KEDA · PDB ·
                  NetworkPolicy (default-deny → the §3b edges) · thumbnail-job ScaledObject (Kafka lag)
```
Full per-service config/secret keys are in each `README.md`'s "DevOps handoff"; the env-wiring map is
[`services/api-details.md`](./services/api-details.md) §2.

**Build order:** see [`requirement-execution-plan/`](./requirement-execution-plan/) — datastores + `catalog` go in
Phase 03, the full fleet in Phase 04. **Golden rule:** read each manifest before you `kubectl apply` it;
this doc tells you what the thing you're applying actually does.
