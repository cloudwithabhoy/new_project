# Frontend Service

The **edge UI** for the platform. A tiny **Node 20 (Express)** server that serves a
**single-page static UI** (vanilla HTML + JS, **no build step / no React toolchain**) and a
runtime-config endpoint. It has **no datastore** and makes **no server-side downstream calls** —
the browser talks to the **api-gateway** directly.

This is the reference **Node** service: `express` + `prom-client` only, Node 20's global `fetch`,
ESM, non-root container.

> **Why the browser calls the gateway (not this server):** keeping the frontend a dumb static host
> avoids a second proxy hop and keeps auth (Bearer JWT) end-to-end between the browser and the
> gateway. The only server-side responsibility is telling the page *where* the gateway is, via
> `GET /config`, so the backend URL is **never hardcoded** into the HTML.

---

## TL;DR — run it locally

```bash
cd services/frontend
npm install            # generates package-lock.json (needed for `npm ci` in Docker)
API_GATEWAY_URL=http://localhost:8080 npm start
# then open http://localhost:3000

# or via Docker (needs the gateway reachable from your browser at :8080):
docker compose up --build
```

The UI supports: **register → login** (JWT stored in `localStorage`) → **list products** →
**add to cart** → **view cart** → **checkout** (creates an order) → **view orders**, all against the
gateway's `/api/*` routes.

---

## Endpoints (this server)

| Method | Path | Description | Success |
|---|---|---|---|
| GET | `/` | Single-page UI (`public/index.html`); increments `page_views_total` | 200 |
| GET | `/config` | `{ "apiGatewayUrl": "<API_GATEWAY_URL>" }` for the browser | 200 |
| GET | `/healthz` | **Liveness** — process is up | 200 |
| GET | `/readyz` | **Readiness** — see note below | 200 |
| GET | `/metrics` | Prometheus RED metrics + defaults + page-view counter | 200 |

### Readiness choice
`/readyz` **always returns 200** when the process is up. It does a *best-effort* 1s probe of
`API_GATEWAY_URL/healthz` and reports the result as `{"status":"ready","api_gateway":"reachable|unreachable|status_NNN"}`,
but it **does not hard-fail readiness** on it. Rationale: the frontend is a static UI and it's the
**browser** (not this pod) that calls the gateway, so a transient gateway blip shouldn't pull the UI
pods out of rotation. Flip the probe to gate readiness only if you want the UI to disappear whenever
the gateway is down.

### Gateway routes the page calls (browser → gateway)
`POST /api/auth/register` · `POST /api/auth/login` · `GET /api/products` ·
`GET|POST /api/cart/{user_id}[/items]` · `POST /api/orders` · `GET /api/orders?user_id=`.
Protected prefixes (`/api/cart`, `/api/orders`) get the `Authorization: Bearer <jwt>` header; the
`user_id` is read from the JWT `sub` claim.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example). **No secrets.**

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `3000` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `API_GATEWAY_URL` | `http://localhost:8080` | no | Gateway base URL **reachable from the browser** (Ingress/public URL in a real cluster, *not* the in-cluster Service name) |

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `3000` (HTTP)
- **Image:** build from this dir; push to ECR as `…/frontend:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (returns 200 once the process is up; does **not** gate on the gateway — see "Readiness choice")
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `API_GATEWAY_URL`
- **Secret keys:** **none** — this service holds no secrets.
- **Dependencies:** none at the server level (no DB). Functionally the **browser** needs the
  **api-gateway** reachable at `API_GATEWAY_URL`. ⚠️ Set `API_GATEWAY_URL` to a **browser-reachable**
  URL (the gateway's Ingress/public host), not `http://api-gateway:8080` (that only resolves
  in-cluster, the browser can't reach it).
- **Service discovery:** this is the **edge UI**; nothing in the mesh calls it. Expose it via
  Ingress/Gateway.
- **Resource hints (tune later):** requests `cpu: 25m, memory: 64Mi` · limits `cpu: 200m, memory: 128Mi`
  (Node baseline RSS; static-serving workload is light).
- **Security:** runs as **non-root** (built-in `node` user, uid 1000); filesystem can be read-only.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 3000), `ConfigMap`, plus an
> `Ingress`/Gateway `VirtualService` to publish it. No Secret, no StatefulSet.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the route template, so cardinality
  stays bounded. Plus `page_views_total` and prom-client default process/Node metrics.
- **Trace correlation:** reads the incoming `traceparent` → `x-b3-traceid` → `x-request-id` and
  stamps it on every log line (`trace_id`). The server-side `/readyz` gateway probe forwards the
  full trace-header set. (The browser → gateway calls carry their own context.)
- **Structured JSON logs** to stdout, one object per line.
- **Graceful shutdown** on SIGTERM: `server.close()` drains in-flight requests (10s safety timeout),
  then exits — clean rollouts + PDB behaviour.

---

## Project layout

```
frontend/
├── server.js              # Express app: /config, ops endpoints, static serving, metrics, logging, shutdown
├── public/
│   ├── index.html         # single-page UI
│   ├── app.js             # vanilla JS: auth, products, cart, checkout, orders
│   └── style.css          # styling
├── package.json           # express + prom-client (pinned), ESM, Node ≥20
├── Dockerfile             # node:20-slim, non-root, npm ci --omit=dev
├── docker-compose.yml     # LOCAL dev: frontend only (gateway runs separately)
├── .dockerignore
├── .env.example
└── README.md
```
