# API Gateway

The **edge** of the platform. A small **Go** reverse proxy (stdlib
`net/http/httputil.ReverseProxy`) that fronts every backend service: it routes by
path prefix to the right upstream, enforces JWT authentication on protected
prefixes, and forwards trace-context headers so a single request shows as one
distributed trace across the mesh. **No datastore.**

This is the platform's single front door — the browser/frontend talks only to the
gateway, never to backend services directly.

```
client → api-gateway ─┬─→ auth            (public)
                      ├─→ user            (public)
                      ├─→ catalog         (public)
                      ├─→ search          (public)
                      ├─→ cart            (PROTECTED — Bearer JWT)
                      ├─→ orders          (PROTECTED — Bearer JWT)
                      └─→ recommendation  (public)

verify: api-gateway → auth/.well-known/jwks.json   (fetch + cache public keys)
```

The gateway is the **reverse** of auth: auth **signs** RS256 tokens with a private
key and publishes the public key as a JWKS; the gateway **fetches** that JWKS and
**verifies** tokens with the reconstructed public key. It never holds a signing
secret.

---

## TL;DR — run it locally

The gateway has no datastore but it needs the **upstreams running**. Start the
backend services first (each has its own `docker compose up`), then:

```bash
cd services/api-gateway
docker compose up --build      # see docker-compose.yml for *_URL wiring

# public route — proxied to catalog (strips /api/products)
curl localhost:8080/api/products

# get a JWT from auth, then hit a protected route
JWT=$(curl -s -X POST localhost:8081/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.com","password":"supersecret"}' | jq -r .access_token)

# protected route — verified, then proxied to cart with X-User-Id injected
curl -H "Authorization: Bearer $JWT" localhost:8080/api/cart/carts/42
```

---

## Routing

Each prefix is stripped before forwarding; the remaining path is preserved verbatim.

| Prefix | Upstream (env) | Auth | Example |
|---|---|---|---|
| `/api/auth/*` | `AUTH_URL` | public | `/api/auth/login` → `AUTH_URL/login` |
| `/api/users/*` | `USER_URL` | public | `/api/users/123` → `USER_URL/users/123` |
| `/api/products/*` | `CATALOG_URL` | public | `/api/products/5` → `CATALOG_URL/products/5` |
| `/api/search/*` | `SEARCH_URL` | public | `/api/search?q=x` → `SEARCH_URL/search?q=x` |
| `/api/cart/*` | `CART_URL` | **JWT** | `/api/cart/carts/5` → `CART_URL/carts/5` |
| `/api/orders/*` | `ORDER_URL` | **JWT** | `/api/orders/9` → `ORDER_URL/orders/9` |
| `/api/recommendations/*` | `RECOMMENDATION_URL` | public | `/api/recommendations?user_id=1` → `RECOMMENDATION_URL/recommendations?...` |

Unmatched paths return `404 {"error":"no route for path"}`.

### Authentication (protected prefixes)

For `/api/cart` and `/api/orders` the gateway requires
`Authorization: Bearer <jwt>` and:

1. Verifies the token is **RS256**, signed by a key in auth's JWKS (matched by the
   token's `kid` header), with a valid `iss` (`JWT_ISSUER`), `aud` (`JWT_AUDIENCE`)
   and unexpired `exp`.
2. On success injects **`X-User-Id: <sub>`** for the upstream (any client-supplied
   `X-User-Id` is stripped first so it can't be spoofed) and proxies the request.
3. On any failure returns **`401 {"error":"unauthorized"}`** and does not proxy.

Public prefixes (`/api/auth`, `/api/products`, `/api/search`, `/api/users`,
`/api/recommendations`) pass straight through.

### JWKS caching & verification

- On startup the gateway does a **best-effort fetch** of
  `{AUTH_URL}/.well-known/jwks.json`, decodes each JWK's base64url `n`/`e` into an
  `*rsa.PublicKey`, and caches them by `kid` in memory.
- A **background goroutine refreshes** every 5 minutes so the cache stays warm
  even with no traffic.
- Verification looks the signing key up by the token's `kid`. On a **cache miss**
  (unknown kid — e.g. auth rotated its key) it does a **lazy refresh**, rate-limited
  to once per 30s so bad-kid tokens can't hammer auth, then retries once.
- A failed startup fetch is **non-fatal**: `/readyz` stays 503 until the keys load
  (auth's pod may still be starting), and verification lazily retries.

### Trace propagation

`httputil.ReverseProxy` copies the incoming request headers to the upstream by
default, so the W3C (`traceparent`) and B3 (`x-b3-*`) headers propagate
automatically — the gateway only takes care not to clobber them. The trace id is
also attached to every gateway log line (`trace_id`).

---

## Operational endpoints

| Method | Path | Description | Success |
|---|---|---|---|
| GET | `/healthz` | **Liveness** — process is up | 200 |
| GET | `/readyz` | **Readiness** — auth's JWKS loaded/reachable | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | 200 |

RED metrics are labelled by the **upstream prefix** as the `route` template
(`/api/cart`, not `/api/cart/carts/5`) to keep cardinality bounded:
`http_requests_total{route,method,status}` and
`http_request_duration_seconds{route,method}`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example). The gateway has **no secrets**.

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `AUTH_URL` | `http://localhost:8081` | auth base URL (also source of JWKS) |
| `USER_URL` | `http://localhost:8082` | user base URL |
| `CATALOG_URL` | `http://localhost:8083` | catalog base URL |
| `SEARCH_URL` | `http://localhost:8084` | search base URL |
| `CART_URL` | `http://localhost:8085` | cart base URL |
| `ORDER_URL` | `http://localhost:8086` | order base URL |
| `RECOMMENDATION_URL` | `http://localhost:8090` | recommendation base URL |
| `JWT_ISSUER` | `auth.ecommerce.local` | expected `iss` (must match auth) |
| `JWT_AUDIENCE` | `ecommerce` | expected `aud` (must match auth) |

---

## DevOps handoff — what you need for the manifests

- **Container port:** `8080` (HTTP), edge service.
- **Image:** build from this dir; push to ECR as `…/api-gateway:<git-sha>`.
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until auth's JWKS is fetched/reachable)
- **ConfigMap keys (ALL config is non-secret):** `PORT`, `LOG_LEVEL`, `AUTH_URL`,
  `USER_URL`, `CATALOG_URL`, `SEARCH_URL`, `CART_URL`, `ORDER_URL`,
  `RECOMMENDATION_URL`, `JWT_ISSUER`, `JWT_AUDIENCE`.
- **Secret keys:** none. The gateway verifies with auth's **public** JWKS.
- **Dependencies:** the upstream Services (`auth`, `user`, `catalog`, `search`,
  `cart`, `order`, `recommendation`). Readiness specifically needs **auth**
  reachable for the JWKS.
- **Service discovery:** set every `*_URL` to the corresponding Service DNS name
  (e.g. `http://auth:8081`). No hardcoded addresses.
- **Exposure:** this is the public edge — front it with an Istio
  `Gateway` + `VirtualService` (or an ALB/Ingress) and terminate TLS there.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 64Mi` ·
  limits `cpu: 250m, memory: 128Mi`. Stateless → scale horizontally.
- **Security:** distroless **non-root**; filesystem can be read-only. No secrets
  mounted.

> ### Istio replaces the app-level JWT check later
> The JWT verification here is a **bootstrapping/Phase-appropriate** measure. Once
> the mesh is in, move it to the sidecar:
> - a **`RequestAuthentication`** pointing at
>   `jwksUri: http://auth.<ns>.svc.cluster.local:8081/.well-known/jwks.json` with
>   `issuer: auth.ecommerce.local` validates tokens at the edge for you, and
> - an **`AuthorizationPolicy`** requires a valid principal on the protected
>   routes/workloads (`cart`, `order`).
>
> At that point the gateway can drop `jwks.go` + the protected-prefix check and
> become a pure router (Envoy can also inject the verified `sub` as a header). Keep
> the public/protected prefix split documented so the AuthorizationPolicy mirrors it.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8080), `ConfigMap`,
> plus the Istio `Gateway`/`VirtualService` once the mesh is in.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `route` = the upstream prefix template, so SLO
  dashboards and burn-rate alerts have low-cardinality series.
- **Trace propagation:** forwards W3C + B3 headers on every proxied hop (free with
  `ReverseProxy`), so a request is one trace in Jaeger/Tempo. Trace id on every log
  line (`trace_id`).
- **Structured JSON logs** to stdout (Loki-friendly).
- **Graceful shutdown** on SIGTERM (drains in-flight requests, stops the JWKS
  refresher) — clean rollouts + PDB behaviour.
- **Readiness gates on auth's JWKS**, so traffic isn't sent to a gateway that can't
  authenticate protected routes.

---

## Project layout

```
api-gateway/
├── main.go         # entrypoint: config, verifier, router, server, graceful shutdown
├── config.go       # env-var configuration + JSON logger
├── proxy.go        # route table, prefix→upstream ReverseProxy, auth enforcement
├── jwks.go         # JWKS fetch/cache/refresh + RS256 token verification (reverse of auth/jwt.go)
├── handlers.go     # router wiring + ops endpoints (/healthz, /readyz, /metrics)
├── metrics.go      # RED metrics + logging middleware (route label, trace id)
├── trace.go        # trace-context extraction helpers
├── Dockerfile      # multi-stage → distroless non-root image
├── docker-compose.yml  # LOCAL dev: just the gateway (needs upstreams running)
├── .env.example
└── README.md
```

## Notes for later phases
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum`. The
  Dockerfile tolerates its absence for now.
- **Key rotation:** the verifier already absorbs rotation via the lazy on-miss
  refresh; auth publishing a new `kid` is picked up automatically.
