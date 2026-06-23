# Catalog Service

The product catalog for the e-commerce platform. A small **Go** HTTP service backed by
**Postgres**, exposing CRUD over products plus health and metrics endpoints.

This is the **Phase 1** service — the first thing you'll deploy to EKS by hand.

---

## TL;DR — run it locally

```bash
cd services/catalog
docker compose up --build      # starts Postgres + catalog
# in another terminal:
curl localhost:8083/healthz
curl -X POST localhost:8083/products \
  -H 'Content-Type: application/json' \
  -d '{"name":"Coffee Mug","description":"350ml ceramic","price_cents":1299,"stock":42}'
curl localhost:8083/products
```

---

## API

Base URL: `http://<host>:8083`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| GET | `/products` | List products (`?limit=&offset=`) | — | 200 |
| POST | `/products` | Create a product | `ProductInput` | 201 |
| GET | `/products/{id}` | Get one product | — | 200 |
| PUT | `/products/{id}` | Update a product | `ProductInput` | 200 |
| DELETE | `/products/{id}` | Delete a product | — | 204 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable | — | 200 / 503 |
| GET | `/metrics` | Prometheus metrics | — | 200 |

**`ProductInput`**
```json
{ "name": "string (required)", "description": "string", "price_cents": 0, "stock": 0 }
```

Errors return `{"error":"message"}` with the appropriate status (400/404/500).

---

## Configuration (environment variables)

All config is via env vars. See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8083` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `catalog` | no | DB user |
| `DB_PASSWORD` | `catalog` | **yes** | DB password → **Kubernetes Secret** |
| `DB_NAME` | `catalog` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |

---

## DevOps handoff — what you need for the manifests

Everything you need to write the Kubernetes manifests, in one place:

- **Container port:** `8083` (HTTP)
- **Image:** build from this dir; push to ECR as `…/catalog:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz` (no dependencies; safe to probe aggressively)
  - Readiness → `GET /readyz` (returns 503'til Postgres is reachable)
- **ConfigMap keys (non-secret):** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:** a Postgres instance (you'll deploy it as a StatefulSet in Phase 1).
  Point `DB_HOST` at its Service name.
- **Service discovery:** this service has **no upstream service calls** — it only needs the DB.
  (Later services will get `*_URL` env vars to reach this one at `http://catalog:8083`.)
- **Resource hints (starting point, tune later):**
  requests `cpu: 50m, memory: 64Mi` · limits `cpu: 250m, memory: 128Mi`
- **Security:** image runs as **non-root** (distroless `nonroot`); filesystem can be read-only.
- **Migrations:** the service auto-creates its table on startup (no separate migration Job needed
  for Phase 1).

> Suggested manifest set to hand-write: `Deployment`, `Service` (ClusterIP, port 8083),
> `ConfigMap`, `Secret`, `Ingress` (or Istio `Gateway`+`VirtualService` once the mesh is in).

---

## Project layout

```
catalog/
├── main.go             # entrypoint: config, DB connect, server, graceful shutdown
├── config.go           # env-var configuration + logger
├── models.go           # Product / ProductInput + validation
├── store.go            # Postgres data access (with startup retry + migration)
├── handlers.go         # HTTP routes, handlers, helpers
├── metrics.go          # Prometheus metrics + logging middleware
├── Dockerfile          # multi-stage build → distroless non-root image
├── docker-compose.yml  # LOCAL dev only (Postgres + service)
├── .env.example        # config reference
└── README.md
```

---

## Notes for later phases

- **Tracing:** when Istio is added (Phase 3), the mesh provides L7 traces automatically. App-level
  spans can be added later if you want richer traces.
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum` for reproducible
  builds. The Dockerfile tolerates its absence for now.
