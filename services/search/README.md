# Search Service

Full-text product search for the platform. A small **Python (FastAPI)** service backed by
**Elasticsearch**, indexing the catalog's products and serving queries over them, plus health and
metrics endpoints.

It calls **catalog** (`search → catalog`) only during a reindex to pull the product list; queries
are served entirely from the local ES index, so reads stay fast and don't fan out.

> **Backing store, not source of truth:** `catalog` owns products in Postgres. `search` keeps a
> denormalized, query-optimized **copy** in Elasticsearch (`products` index). Rebuild it any time
> with `POST /reindex`.

---

## TL;DR — run it locally

```bash
cd services/search
docker compose up --build      # Elasticsearch + catalog (+ its Postgres) + search

# seed a product in the catalog:
curl -X POST localhost:8083/products \
  -H 'Content-Type: application/json' \
  -d '{"name":"Red Shoes","description":"comfy running shoes","price_cents":4999,"stock":10}'

# pull catalog -> ES, then query:
curl -X POST localhost:8084/reindex
curl 'localhost:8084/search?q=shoes'
curl 'localhost:8084/search?q=&limit=50'   # empty q -> recent/all
```

---

## API

Base URL: `http://<host>:8084`

| Method | Path | Description | Success |
|---|---|---|---|
| GET | `/search?q=&limit=` | Full-text search over `products` (multi_match on name/description). Empty `q` → recent/all. `limit` default 20, capped at 100. | 200 |
| POST | `/reindex` | Pull all products from `GET {CATALOG_URL}/products` (paginated) and bulk-index them (doc id = product id). | 200 |
| GET | `/healthz` | **Liveness** — process is up | 200 |
| GET | `/readyz` | **Readiness** — Elasticsearch reachable | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | 200 |

**`GET /search` response**
```json
{ "query": "shoes", "hits": [ { "id": 1, "name": "Red Shoes", "description": "...", "price_cents": 4999, "stock": 10 } ], "total": 1 }
```

**`POST /reindex` response**
```json
{ "indexed": 42 }
```

FastAPI also serves interactive docs at `/docs`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8084` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `ELASTICSEARCH_URL` | `http://localhost:9200` | no | ES endpoint (k8s Service name) |
| `CATALOG_URL` | `http://localhost:8083` | no | catalog base URL (source for `/reindex`) |

This service has **no secrets of its own** (local-dev ES runs with security disabled).

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `8084` (HTTP)
- **Image:** build from this dir; push to ECR as `…/search:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Elasticsearch reachable)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `ELASTICSEARCH_URL`, `CATALOG_URL`
- **Secret keys:** none (if you point at a secured/managed ES, add ES creds as a Secret and wire
  them into `ELASTICSEARCH_URL` / the client).
- **Dependencies:**
  - **Elasticsearch** (own cluster/StatefulSet or managed; holds the `products` index).
  - **catalog** at `http://catalog:8083` (`CATALOG_URL`) — only hit during `/reindex`.
- **Service discovery:** reached by others at `http://search:8084` (`SEARCH_URL`); the api-gateway
  proxies `/api/search/*` here (public route).
- **Resource hints (tune later):** requests `cpu: 50m, memory: 96Mi` · limits `cpu: 250m, memory: 192Mi`
  (the service itself is light; size Elasticsearch separately — it's the heavy component).
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.
- **Operational note:** `/reindex` is a full rebuild; schedule it (CronJob) or trigger after large
  catalog changes. It's idempotent (doc id = product id → upserts).

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8084), `ConfigMap`, plus the
> Elasticsearch cluster (StatefulSet or managed). Optionally a `CronJob` that POSTs `/reindex`.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/search`), so the
  query string never inflates cardinality on SLO dashboards.
- **Trace correlation + propagation:** reads the incoming `traceparent`/B3 trace id and stamps it on
  every log line (`trace_id`); on the catalog call during `/reindex` it **forwards** the
  propagation headers so the trace spans `search → catalog`.
- **Structured JSON logs** to stdout.
- **Graceful shutdown** via FastAPI lifespan on SIGTERM (uvicorn drains in-flight requests, then the
  ES client closes) — clean rollouts + PDB behaviour.
- **Readiness gates on Elasticsearch**, so traffic isn't sent to a pod that can't serve queries.
- **Retry the initial ES connect** at startup (the ES pod may not be ready yet).

---

## Project layout

```
search/
├── app/
│   ├── __main__.py       # entrypoint: `python -m app` -> uvicorn
│   ├── main.py           # FastAPI app, middleware, routes, lifespan
│   ├── config.py         # env-var configuration
│   ├── es.py             # AsyncElasticsearch client (retry-connect + mapping) + search/bulk
│   ├── catalog.py        # catalog client for /reindex (paginate + forward trace headers)
│   ├── models.py         # Pydantic response schemas
│   ├── metrics.py        # RED metrics + route-template labelling
│   ├── logging_setup.py  # JSON logging with trace_id
│   └── trace.py          # trace-context extraction + propagation headers
├── requirements.txt
├── Dockerfile            # multi-stage → slim non-root image
├── docker-compose.yml    # LOCAL dev: Elasticsearch + catalog + search
├── .env.example
└── README.md
```
