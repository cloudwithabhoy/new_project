# Application Architecture — how the e-commerce platform works end to end

A DevOps-oriented walkthrough of the application **before** you wrap Kubernetes/Istio around it.
Read this to understand *what* you're deploying and *how requests actually move through it*, so your
manifests, probes, routing, NetworkPolicies, and dashboards are built against reality.

- **API-level detail** (every endpoint, payload, env key) lives in [`services/api-details.md`](./services/api-details.md).
- **Per-microservice "📦 DevOps handoff"** (ports, probes, ConfigMap/Secret keys, resource hints) lives in each `services/<name>/README.md`.
- **The platform/phased plan** lives in [`EXECUTION_PLAN.md`](./EXECUTION_PLAN.md).

This file is the **map**; those are the **detail**.

---

## 1. The system in one paragraph

A customer hits the **frontend** (web UI), which calls a single edge **api-gateway**. The gateway
authenticates the request (JWT) and routes it to one of 12 small microservices. Most calls are
**synchronous HTTP/REST**. Placing an order is the one rich flow: **order** orchestrates a
synchronous chain (cart → payment → inventory) and then publishes an **`order.created` event to
Kafka**, which **inventory**, **notification**, and **recommendation** consume **asynchronously**.
Each microservice owns its **own datastore** (database-per-microservice). A separate **thumbnail-job** worker
consumes an image queue and is designed to scale to zero. That's the whole system: a small amount
of business logic deliberately spread across many microservices so the *infrastructure* has realistic
sync + async traffic to mesh, scale, observe, and break.

---

## 2. Microservice catalog

| Microservice | Lang | Port | Datastore | Role | Calls (sync) | Events |
|---|---|---|---|---|---|---|
| **frontend** | Node | 3000 | — | Web UI (SPA) | → api-gateway | — |
| **api-gateway** | Go | 8080 | — | Edge router + JWT auth | → all microservices | — |
| **auth** | Go | 8081 | Postgres | Credentials, issues JWTs, JWKS | → user | — |
| **user** | Python | 8082 | Postgres | User profiles (leaf) | — | — |
| **catalog** | Go | 8083 | Postgres | Product CRUD | — | — |
| **search** | Python | 8084 | Elasticsearch | Product search / reindex | → catalog | — |
| **cart** | Go | 8085 | Redis | Shopping cart | → catalog | — |
| **order** | Go | 8086 | Postgres | **Checkout orchestrator** | → cart, payment, inventory | **produces `order.created`** |
| **payment** | Go | 8087 | Postgres | Mock charge (fault target) | — | — |
| **inventory** | Python | 8088 | Postgres | Stock reserve + commit | — | **consumes `order.created`** |
| **notification** | Python | 8089 | — (in-memory) | "Sends" confirmations | — | **consumes `order.created`** |
| **recommendation** | Python | 8090 | Postgres | A/B recommendations | — | **consumes `order.created`** |
| **thumbnail-job** | Python | 9100¹ | — | Image processor (KEDA) | — | **consumes `thumbnail.requests`** |

¹ `9100` is only a metrics/health port; the thumbnail-job is a queue worker, not an HTTP API.

**Datastores in play:** Postgres ×5 (auth, user, catalog, order, payment, inventory, recommendation
— each its **own** DB), Redis ×1 (cart), Elasticsearch ×1 (search), Kafka ×1 (the event bus).

---

## 3. Layered topology

```
                          ┌─────────────┐
   browser ──────────────►│  frontend   │  :3000  (serves the SPA + /config)
                          └──────┬──────┘
                 the browser then calls the gateway directly (URL from /config)
                                 │
                          ┌──────▼──────┐
                          │ api-gateway │  :8080   edge: JWT verify (JWKS), route, inject X-User-Id
                          └──┬───┬───┬──┘
        ┌────────────┬───────┘   │   └────────┬─────────────┬───────────────┐
        ▼            ▼           ▼            ▼             ▼               ▼
     ┌──────┐    ┌──────┐    ┌────────┐   ┌──────┐    ┌────────┐      ┌──────────────┐
     │ auth │    │ user │    │catalog │   │search│    │  cart  │      │    order     │
     │ 8081 │───►│ 8082 │    │ 8083   │◄──│ 8084 │◄───│ 8085   │      │    8086      │
     └──┬───┘    └──┬───┘    └──┬─────┘   └──┬───┘    └──┬─────┘      └──┬───┬───┬───┘
      Postgres   Postgres    Postgres      ES         Redis        sync │   │   │
                                                                  ┌─────┘   │   └─────┐
                                                                  ▼         ▼         ▼
                                                              ┌───────┐ ┌────────┐ (cart, above)
                                                              │payment│ │inventory│
                                                              │ 8087  │ │  8088  │
                                                              └──┬────┘ └──┬─────┘
                                                              Postgres   Postgres
                                                                  
   order, after the sync chain, publishes ──►  ┌──────────────────────┐
                                               │ Kafka: order.created │
                                               └──┬─────────┬─────────┬┘
                                       async ─────┘         │         └───────────
                                          ▼                 ▼                     ▼
                                   ┌────────────┐   ┌──────────────┐     ┌────────────────┐
                                   │ inventory  │   │ notification │     │ recommendation │
                                   │ (commit)   │   │   8089       │     │     8090       │
                                   └────────────┘   └──────────────┘     └────────────────┘
                                     Postgres        in-memory ring         Postgres

   (separate queue)  Kafka: thumbnail.requests ──► thumbnail-job (KEDA scale-to-zero)
```

**Two traffic shapes on purpose:**
- **Sync (request/response, blocking):** everything through the gateway, and `order`'s chain.
- **Async (event, non-blocking):** `order.created` fan-out, and the thumbnail queue.

---

## 4. Communication patterns

### Synchronous — HTTP/REST over the mesh
- Plain JSON over HTTP. No microservice hardcodes another's address; every dependency URL comes from an
  **env var** (`CATALOG_URL`, `PAYMENT_URL`, …) you supply via ConfigMap. In-cluster these resolve
  to Kubernetes Service DNS (`http://catalog:8083`).
- Each caller forwards **trace-context headers** (`traceparent`, `x-b3-*`, `x-request-id`) so one
  user action is a single distributed trace across many pods (Jaeger/Tempo via Istio).

### Asynchronous — Kafka events
- **Producer:** `order` writes one `order.created` message (key = `order_id`) after a successful checkout.
- **Consumers:** `inventory`, `notification`, `recommendation` each have their **own consumer group**,
  so all three get every event independently.
- **Delivery is at-least-once** → every consumer is **idempotent by `order_id`** (re-processing a
  duplicate is a no-op). This is why you can restart a consumer pod safely.

---

## 5. End-to-end flows (the important part)

### Flow A — Register
```
frontend → api-gateway (/api/auth/register, public) → auth
   auth: validate input
        → POST user (/users)         [creates the profile, returns user_id]   ← the first traced sync hop
        → bcrypt-hash password
        → store credential(user_id, email, hash) in auth's Postgres
   ← 201 {user_id, email}
```
`auth` owns **secrets** (password hash, signing key); `user` owns the **profile**. Separate DBs.

### Flow B — Login → token
```
frontend → api-gateway (/api/auth/login, public) → auth
   auth: look up credential by email → bcrypt-compare
        → mint RS256 JWT (sub=user_id, email, exp) signed with its private key
   ← 200 {access_token, expires_in}
frontend stores the JWT and sends it as  Authorization: Bearer <jwt>  on later calls.
```
The matching **public** key is published at `auth/.well-known/jwks.json`.

### Flow C — Browse & search (public, read-heavy)
```
frontend → api-gateway (/api/products) → catalog → Postgres        (list/get products)
frontend → api-gateway (/api/search?q=) → search → Elasticsearch   (full-text)
   search is populated by POST /reindex, which pulls all products from catalog and bulk-indexes them.
```

### Flow D — Add to cart (authenticated)
```
frontend → api-gateway (/api/cart, PROTECTED) :
   gateway verifies JWT against auth's JWKS, injects X-User-Id, forwards →
cart → GET catalog/products/{id}   [validate product exists, snapshot name+price]
cart → store/Update JSON cart at Redis key  cart:{user_id}
   ← 200 Cart {items, total_cents}
```

### Flow E — Checkout  ★ the centerpiece (sync chain **then** async fan-out)
```
frontend → api-gateway (/api/orders, PROTECTED) → order   POST /orders {user_id}

  order (orchestrator), all hops carry the same trace + the pre-allocated order_id:
   1. GET  cart/carts/{user_id}              → items + total      (400 if cart empty)
   2. POST payment/payments {order_id,...}   → approved/declined  (402 if declined)
   3. POST inventory/reserve {order_id,items}→ reserve stock      (409 if insufficient)
   4. INSERT order row (status "confirmed")  → order's Postgres
   5. PUBLISH order.created → Kafka          (key = order_id)
   6. DELETE cart/carts/{user_id}            → best-effort clear
   ← 201 Order

  …then, asynchronously and independently (buyer already got their 201):
   Kafka order.created ─► inventory      : commit the reservation (reserved → sold), idempotent
                        ─► notification   : "send" confirmation, keep in ring buffer
                        ─► recommendation : update popularity + co-purchase counts
```
This single flow is what gives the mesh something worth showing: a **multi-hop sync trace** (gateway
→ order → payment → inventory) **and** an **event fan-out** off the same action.

### Flow F — Thumbnail processing (independent async)
```
producer (or catalog, later) → Kafka thumbnail.requests → thumbnail-job
   worker: read {product_id, image_url}, process (~200ms), record metric
KEDA watches the topic's consumer lag → scales the worker 0→N→0.
```

---

## 6. Edge request lifecycle (auth enforcement)

```
            ┌──────────────────────── api-gateway ────────────────────────┐
request ───►│ match path prefix → pick upstream                            │
            │ public  (/api/auth, /api/products, /api/search): pass through │
            │ protected (/api/cart, /api/orders):                          │
            │    • require Authorization: Bearer                           │
            │    • verify RS256 sig against cached JWKS (refresh on new kid)│
            │    • valid → strip client X-User-Id, inject trusted X-User-Id │
            │    • invalid/missing → 401 (never reaches the upstream)       │
            └───────────────────────────┬─────────────────────────────────┘
                                        ▼ ReverseProxy (trace headers preserved)
                                     upstream microservice
```
> **DevOps note:** this app-level JWT check is the *starting* point. In Phase 3+ you move enforcement
> into the mesh: Istio `RequestAuthentication` (validates JWT via the same JWKS URL) +
> `AuthorizationPolicy` (require a valid principal to reach `order`/`cart`). The app stays as a
> safety net / `X-User-Id` provider.

---

## 7. Cross-cutting concerns (what every microservice guarantees — and why you care)

Every microservice implements the same contract. Each maps directly to a Kubernetes/observability concern:

| App feature | Endpoint / behavior | DevOps use |
|---|---|---|
| **Liveness** | `GET /healthz` (no deps) | `livenessProbe` — restart a wedged pod |
| **Readiness** | `GET /readyz` (checks DB/broker/ES) | `readinessProbe` — keep traffic off a pod that can't serve; gates rollouts |
| **RED metrics** | `GET /metrics` → `http_requests_total{route,method,status}`, `http_request_duration_seconds` | Prometheus scrape; HPA custom metrics; SLO dashboards + burn-rate alerts |
| **Async metrics** | consumers expose `events_consumed_total`, `event_processing_duration_seconds` | KEDA scaling signal; consumer-lag dashboards |
| **Trace propagation** | forwards `traceparent`/`b3` headers downstream | Jaeger/Tempo end-to-end traces through Istio |
| **Structured JSON logs** | stdout, one JSON/line, with `trace_id` | Loki/CloudWatch parsing; log↔trace correlation |
| **Graceful shutdown** | drains on SIGTERM | clean rolling updates, node drains, respects PodDisruptionBudgets |
| **Retry initial connect** | DB/broker/ES not-ready at boot is tolerated | pods don't crash-loop just because their datastore starts a few seconds later |

The `route` label is always the **template** (`/orders/{id}`), never the raw path — bounded
cardinality so Prometheus doesn't explode.

---

## 8. Runtime dependency & startup order

Services tolerate dependencies starting late (retry-connect), so strict ordering isn't *required* —
but this is the sane bring-up order and tells you what blocks what:

```
1. Datastores:  Postgres (×per microservice), Redis, Elasticsearch, Kafka
2. Core:        catalog, user
3. Auth:        auth        (needs user)
4. Mid:         cart (needs catalog), search (needs catalog), payment, inventory
5. Orchestrator: order      (needs cart, payment, inventory, Kafka)
6. Async consumers: inventory*, notification, recommendation   (need Kafka; *inventory also serves sync)
7. Edge:        api-gateway (needs auth's JWKS to be reachable for /readyz), then frontend
8. Workers:     thumbnail-job (needs Kafka)
```
A microservice's `/readyz` going 503 is the signal that one of *its* deps isn't up yet — not a crash.

---

## 9. Failure modes & resilience (what to expect under chaos)

| Inject | What the app does | What you'll observe |
|---|---|---|
| `payment` returns 500 (`FAIL_MODE=error` or Istio fault) | `order` checkout fails fast | 5xx on `/api/orders`; trace shows the failing hop; SLO burn |
| `payment` declines (`FAIL_MODE=decline`) | `order` returns **402** | order never persisted; no event emitted |
| insufficient stock | `inventory/reserve` → **409**, all-or-none | order rejected; stock unchanged |
| a consumer pod dies mid-event | at-least-once redelivery; idempotent by `order_id` | no double-commit; consumer lag blips then recovers |
| catalog down | `cart`/`search` return **502** | add-to-cart/search fail; product browse may still serve cached ES |
| order can't reach Kafka | sync part still returns 201; event emit is best-effort | **known gap:** no transactional outbox — documented learning point |
| DB starts late | retry-connect; `/readyz` 503 until up | pod stays un-ready, no traffic, no crash-loop |

> Two deliberately-documented gaps (good chaos/learning material, not bugs): (1) **no outbox** — a
> crash between persist and publish can drop an event; (2) **no compensation** — a successful charge
> followed by an inventory 409 isn't auto-refunded. These are where you'd later add sagas.

---

## 10. What this means for you (DevOps) — app → infra mapping

Per microservice you will hand-write (Phases 1–4):
- **Deployment** (image from ECR, the env from ConfigMap/Secret, liveness=`/healthz`, readiness=`/readyz`, resource requests/limits, SIGTERM grace)
- **Service** (ClusterIP on the port in §2)
- **ConfigMap** (non-secret env: ports, `*_URL`, tunables) + **Secret** (DB passwords, `JWT_PRIVATE_KEY_PEM`)
- **Datastore** workload (Postgres/Redis/ES **StatefulSet** + PVC; Kafka StatefulSet) — one per owning microservice
- Later: Istio `Gateway`/`VirtualService`/`DestinationRule`, `PeerAuthentication` (mTLS),
  `RequestAuthentication` + `AuthorizationPolicy` (JWT), `HPA`/`KEDA`, `PodDisruptionBudget`,
  `NetworkPolicy` (default-deny, then the §3 edges), and the `thumbnail-job` `ScaledObject` (Kafka lag).

**Config/secret inventory to template your manifests** — full per-microservice lists are in each
`README.md`'s "📦 DevOps handoff"; the env wiring map is `services/api-details.md` §2.

**Golden rule from the plan:** read each manifest before you `kubectl apply` it. This doc tells you
what the thing you're applying actually *does*.

---

## 11. Where to go next

- Endpoint/payload specifics → `services/api-details.md`
- A given microservice's knobs → `services/<name>/README.md`
- Deploy order & phases → `EXECUTION_PLAN.md` (you're at Phase 1: deploy `catalog` + its Postgres by hand)
