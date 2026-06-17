# User Service

User profiles for the platform. A small **Python (FastAPI)** service backed by **Postgres**,
exposing CRUD over user profiles plus health and metrics endpoints.

This is a **Phase 2** service and a **leaf** in the mesh — it calls nothing downstream. The
**auth** service calls it (`auth → user`) to create a profile during registration.

> **Separation of concerns:** `auth` owns secrets (password hashes, JWT signing); `user` owns the
> profile (email, name). Each has its own database — **database-per-service**, the microservice norm.

---

## TL;DR — run it locally

```bash
cd services/user
docker compose up --build

curl localhost:8082/healthz
curl -X POST localhost:8082/users \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.com","full_name":"Ada Lovelace"}'
curl localhost:8082/users
curl localhost:8082/users/1
curl 'localhost:8082/users/by-email?email=ada@example.com'
```

---

## API

Base URL: `http://<host>:8082`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| GET | `/users` | List users (`?limit=&offset=`) | — | 200 |
| POST | `/users` | Create a user | `UserInput` | 201 |
| GET | `/users/{id}` | Get one user | — | 200 |
| PUT | `/users/{id}` | Update a user | `UserInput` | 200 |
| GET | `/users/by-email?email=` | Look up by email | — | 200 / 404 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | — | 200 |

**`UserInput`**
```json
{ "email": "valid@email.com (required)", "full_name": "string (required, 1–200 chars)" }
```
Validation is enforced by Pydantic (e.g. malformed email → 422). Duplicate email → 409.
FastAPI also serves interactive docs at `/docs`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8082` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `user_svc` | no | DB user |
| `DB_PASSWORD` | `user_svc` | **yes** | DB password → **Secret** |
| `DB_NAME` | `users` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `8082` (HTTP)
- **Image:** build from this dir; push to ECR as `…/user:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`
- **Secret keys:** `DB_PASSWORD`
- **Dependencies:** its **own Postgres** (separate DB/StatefulSet — database-per-service).
- **Service discovery:** this service makes **no** upstream calls. `auth` reaches it at
  `http://user:8082` via its `USER_URL`.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 96Mi` · limits `cpu: 250m, memory: 192Mi`
  (Python's baseline RSS is a bit higher than Go's.)
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8082), `ConfigMap`, `Secret`,
> plus the user Postgres StatefulSet.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/users/{id}`), so
  cardinality stays bounded for SLO dashboards and burn-rate alerts.
- **Trace correlation:** reads the incoming `traceparent`/B3 trace id and stamps it on every log
  line (`trace_id`), so a Loki log links to its Tempo/Jaeger trace. (Leaf service — nothing to
  forward; `app/trace.py` already lists the headers to propagate if that changes.)
- **Structured JSON logs** to stdout.
- **Graceful shutdown** via FastAPI lifespan on SIGTERM (uvicorn drains in-flight requests, then
  the DB pool closes) — clean rollouts + PDB behaviour.
- **Readiness gates on the DB**, so traffic isn't sent to a pod that can't serve.

---

## Project layout

```
user/
├── app/
│   ├── __main__.py       # entrypoint: `python -m app` -> uvicorn
│   ├── main.py           # FastAPI app, middleware, routes, lifespan
│   ├── config.py         # env-var configuration
│   ├── db.py             # asyncpg pool (retry-connect + migration) + queries
│   ├── models.py         # Pydantic schemas (UserInput / User)
│   ├── metrics.py        # RED metrics + route-template labelling
│   ├── logging_setup.py  # JSON logging with trace_id
│   └── trace.py          # trace-context extraction + propagation header list
├── requirements.txt
├── Dockerfile            # multi-stage → slim non-root image
├── docker-compose.yml    # LOCAL dev: Postgres + user
├── .env.example
└── README.md
```
