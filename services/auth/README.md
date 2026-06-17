# Auth Service

The authentication boundary for the platform. A small **Go** HTTP service backed by
**Postgres** that registers users, verifies passwords (**bcrypt**), and issues **RS256 JWTs**.
It publishes a **JWKS** endpoint so the Istio mesh (and any other verifier) can validate tokens
without ever holding the signing key.

This is a **Phase 2** service. It introduces the first **synchronous inter-service call**:
`auth → user` (to create the profile during registration) — the mesh's first real distributed trace.

```
register:  client → auth → user        (auth stores credentials, user stores profile)
login:     client → auth                (verify password, mint JWT)
verify:    Istio / api-gateway → auth/.well-known/jwks.json   (validate tokens)
```

---

## TL;DR — run it locally

`docker compose up` here starts **auth + user + two Postgres instances** together:

```bash
cd services/auth
docker compose up --build

# register (auth calls the user service to create the profile)
curl -X POST localhost:8081/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.com","password":"supersecret","full_name":"Ada Lovelace"}'

# login -> get a JWT
curl -X POST localhost:8081/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.com","password":"supersecret"}'

# inspect the public keys Istio would use to verify tokens
curl localhost:8081/.well-known/jwks.json
```

---

## API

Base URL: `http://<host>:8081`

| Method | Path | Description | Body | Success |
|---|---|---|---|---|
| POST | `/register` | Create credentials + profile (calls `user`) | `RegisterInput` | 201 |
| POST | `/login` | Verify password, return a JWT | `LoginInput` | 200 |
| GET | `/validate` | Validate a `Bearer` token, return claims | — (Authorization header) | 200 / 401 |
| GET | `/.well-known/jwks.json` | Public keys for token verification (JWKS) | — | 200 |
| GET | `/healthz` | **Liveness** — process is up | — | 200 |
| GET | `/readyz` | **Readiness** — DB reachable | — | 200 / 503 |
| GET | `/metrics` | Prometheus RED metrics | — | 200 |

**`RegisterInput`**
```json
{ "email": "string (required)", "password": "string (≥8 chars)", "full_name": "string (required)" }
```
**`LoginInput`**
```json
{ "email": "string", "password": "string" }
```
**Login success**
```json
{ "access_token": "<jwt>", "token_type": "Bearer", "expires_in": 3600 }
```

The JWT is **RS256**, with claims: `iss`, `aud`, `sub` (= user id), `email`, `iat`, `exp`, and a
`kid` header pointing at the JWKS key. Errors return `{"error":"message"}`.

Security notes: login returns the **same** error for unknown-email and wrong-password (user
enumeration defense); passwords are hashed with **bcrypt**.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8081` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `DB_HOST` | `localhost` | no | Postgres host (k8s Service name) |
| `DB_PORT` | `5432` | no | Postgres port |
| `DB_USER` | `auth` | no | DB user |
| `DB_PASSWORD` | `auth` | **yes** | DB password → **Secret** |
| `DB_NAME` | `auth` | no | DB name |
| `DB_SSLMODE` | `disable` | no | `require` for TLS (RDS) |
| `USER_URL` | `http://localhost:8082` | no | Base URL of the user service |
| `JWT_PRIVATE_KEY_PEM` | *(empty → ephemeral)* | **yes** | RSA private key PEM → **Secret** |
| `JWT_ISSUER` | `auth.ecommerce.local` | no | `iss` claim (Istio matches this) |
| `JWT_AUDIENCE` | `ecommerce` | no | `aud` claim |
| `JWT_TTL_MINUTES` | `60` | no | Access-token lifetime |

> ⚠️ **Always set `JWT_PRIVATE_KEY_PEM` in Kubernetes.** Without it the service generates an
> ephemeral key on startup — tokens break on every restart and the JWKS changes. Generate one:
> ```bash
> openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out auth-jwt.pem
> ```
> then load `auth-jwt.pem` into a Secret keyed as `JWT_PRIVATE_KEY_PEM`.

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `8081` (HTTP)
- **Image:** build from this dir; push to ECR as `…/auth:<git-sha>`
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until Postgres reachable)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSLMODE`, `USER_URL`, `JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_TTL_MINUTES`
- **Secret keys:** `DB_PASSWORD`, `JWT_PRIVATE_KEY_PEM`
- **Dependencies:**
  - Its **own Postgres** (separate DB/StatefulSet from catalog — database-per-service).
  - The **user service** reachable at `USER_URL` (e.g. `http://user:8082`).
- **Service discovery:** set `USER_URL` to the user Service DNS name. No hardcoded addresses.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 64Mi` · limits `cpu: 250m, memory: 128Mi`
- **Security:** distroless **non-root**; filesystem can be read-only.
- **Istio (later phases):** point a `RequestAuthentication` at
  `jwksUri: http://auth.<ns>.svc.cluster.local:8081/.well-known/jwks.json` and match
  `issuer: auth.ecommerce.local`. Then an `AuthorizationPolicy` can require a valid JWT on
  `order`, `cart`, etc.

> Suggested manifest set: `Deployment`, `Service` (ClusterIP 8081), `ConfigMap`, `Secret`
> (DB + JWT key), plus the auth Postgres StatefulSet. Add `Gateway`/`VirtualService` once the mesh is in.

---

## Enterprise contract (what this service gives your platform layer)

- **RED metrics** at `/metrics`: `http_requests_total{route,method,status}` and
  `http_request_duration_seconds{route,method}` — `route` is the template (`/login`), so SLO
  dashboards and burn-rate alerts have low-cardinality series.
- **Trace propagation:** forwards W3C (`traceparent`) and B3 headers on the `auth → user` call, so
  the hop shows as one trace in Jaeger/Tempo. Trace id is attached to every log line (`trace_id`).
- **Structured JSON logs** to stdout (Loki-friendly).
- **Graceful shutdown** on SIGTERM (drains in-flight requests) — clean rollouts + PDB behaviour.
- **Readiness gates on the DB**, so traffic isn't sent to a pod that can't serve.

---

## Project layout

```
auth/
├── main.go         # entrypoint: config, DB, signer, user client, server, graceful shutdown
├── config.go       # env-var configuration + JSON logger
├── models.go       # RegisterInput / LoginInput + validation
├── store.go        # Postgres credential store (retry-connect + migration)
├── jwt.go          # RS256 signer, token issue/validate, JWKS, RFC-7638 kid
├── userclient.go   # synchronous client for the user service (trace-propagating)
├── handlers.go     # HTTP routes, handlers, helpers
├── metrics.go      # RED metrics + logging middleware (route label, trace id)
├── trace.go        # trace-context extraction + propagation helpers
├── Dockerfile      # multi-stage → distroless non-root image
├── docker-compose.yml  # LOCAL dev: auth + user + 2 Postgres
├── .env.example
└── README.md
```

## Notes for later phases
- **go.sum:** once you have Go locally, run `go mod tidy` and commit `go.sum`. The Dockerfile
  tolerates its absence for now.
- **Key rotation:** the JWKS supports multiple keys; today we publish one. Rotation (publish new
  `kid`, sign with it, retire the old) is a good Phase 5–6 exercise.
