# Notification Service

"Sends" notifications when orders are placed. A small **Python (FastAPI)** service that is
**async-only** in the mesh: it consumes the **`order.created`** Kafka topic and, per order,
emits a structured **"notification sent"** log line (no real email — this is a demo platform).

It has **no database**. The only HTTP surface is a tiny read-only **inspection API** plus the
standard ops endpoints. The last N notifications are kept in an **in-memory ring buffer** so you
can eyeball what was sent.

> **Why it exists:** it's the consumer fan-out demo for `order.created` (alongside `inventory`
> and `recommendation`) and the platform's **KEDA-on-Kafka-lag scaling candidate** — stateless,
> no DB, scales purely on consumer-group lag.

---

## TL;DR — run it locally

```bash
cd services/notification
docker compose up --build

curl localhost:8089/healthz
curl localhost:8089/readyz

# produce a test order.created event (key = order_id):
docker compose exec redpanda rpk topic create order.created || true
echo '123 {"event":"order.created","order_id":123,"user_id":42,"items":[{"product_id":7,"quantity":2,"price_cents":1299}],"total_cents":2598,"created_at":"2026-06-16T12:00:00Z"}' \
  | docker compose exec -T redpanda rpk topic produce order.created --format '%k %v\n'

curl 'localhost:8089/notifications?limit=10'
```

You should see a `{"msg":"notification sent","channel":"email","order_id":123,"user_id":42,...}`
line in the service logs, and the record returned by `GET /notifications`.

---

## What it does

- **Consumer:** subscribes to Kafka topic `order.created` (consumer group `notification`).
- On each event it **"sends"** a notification — a structured JSON log line; **no real email**.
- **Idempotent by `order_id`:** a re-delivered event for an order already notified is a no-op.
- **Ring buffer:** keeps the last `NOTIFICATION_BUFFER` (default 100) notifications in memory.
- Runs as an `asyncio` task started in the app **lifespan**; **drains on shutdown** (SIGTERM).

The `order.created` schema is owned by the `order` service (see `services/api-details.md` §4).

---

## API

Base URL: `http://<host>:8089`

| Method | Path | Description | Success |
|---|---|---|---|
| GET | `/notifications` | Last N notifications, newest first (`?limit=`) | 200 |
| GET | `/healthz` | **Liveness** — process is up | 200 |
| GET | `/readyz` | **Readiness** — Kafka consumer connected | 200 / 503 |
| GET | `/metrics` | Prometheus metrics | 200 |

**`Notification`**
```json
{ "channel": "email", "order_id": 123, "user_id": 42, "sent_at": "2026-06-16T12:00:00.123456+00:00" }
```
`?limit=` is clamped to `1…NOTIFICATION_BUFFER` (defaults to 50). FastAPI also serves
interactive docs at `/docs`.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example).

| Variable | Default | Secret? | Notes |
|---|---|---|---|
| `PORT` | `8089` | no | HTTP listen port |
| `LOG_LEVEL` | `info` | no | `debug`/`info`/`warn`/`error` |
| `KAFKA_BROKERS` | `localhost:9092` | no | Kafka bootstrap servers (k8s: `kafka:9092`) |
| `KAFKA_TOPIC` | `order.created` | no | Topic to consume |
| `KAFKA_GROUP` | `notification` | no | Consumer group id |
| `NOTIFICATION_BUFFER` | `100` | no | Ring-buffer size for `GET /notifications` |

There are **no secrets** — this service has no datastore.

---

## Metrics

RED (HTTP) per `services/api-details.md` §1.3, plus the async-worker series:

- `http_requests_total{route,method,status}` · `http_request_duration_seconds{route,method}`
- `events_consumed_total{topic,result}` — `result` is `success` | `duplicate` | `error`
- `event_processing_duration_seconds{topic}`
- `notifications_sent_total` — domain counter for notifications "sent"

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `8089` (HTTP — ops + inspection only; **no business traffic**)
- **Image:** build from this dir; push to ECR as `…/notification:<git-sha>`
- **No database / no Secret** — this service is **stateless** (ring buffer is in-memory and
  intentionally ephemeral; losing it on restart is fine).
- **Probes:**
  - Liveness  → `GET /healthz`
  - Readiness → `GET /readyz` (503 until the Kafka consumer is connected)
- **ConfigMap keys:** `PORT`, `LOG_LEVEL`, `KAFKA_BROKERS`, `KAFKA_TOPIC`, `KAFKA_GROUP`,
  `NOTIFICATION_BUFFER`
- **Secret keys:** _none_
- **Dependencies:** **Kafka** (`KAFKA_BROKERS`). Consumes `order.created`. Calls nothing downstream.
- **Service discovery:** **no** upstream callers — async-only. Not in the gateway route table.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 96Mi` · limits `cpu: 250m, memory: 192Mi`.
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.

### ⚡ KEDA scale-to-zero (this service's headline DevOps feature)

Stateless + no DB + pure Kafka consumer → ideal for **KEDA's `kafka` scaler** on
**consumer-group lag**. Sketch a `ScaledObject` targeting this Deployment:

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: notification
spec:
  scaleTargetRef:
    name: notification          # the Deployment
  minReplicaCount: 0            # scale to zero when there's no lag
  maxReplicaCount: 10
  triggers:
    - type: kafka
      metadata:
        bootstrapServers: kafka:9092
        consumerGroup: notification
        topic: order.created
        lagThreshold: "50"       # ~events of lag per replica before scaling up
```

> Note: with `minReplicaCount: 0`, `/readyz` only matters while a replica is running; KEDA wakes
> a pod on incoming lag and scales it back to zero once the group is caught up.

> Suggested manifest set: `Deployment`, `ConfigMap`, `ScaledObject` (KEDA). A `Service` is
> optional — only needed if you want Prometheus to scrape `/metrics` via a ClusterIP/ServiceMonitor.

---

## Enterprise contract (what this service gives your platform layer)

- **Async-worker metrics** at `/metrics`: `events_consumed_total{topic,result}` and
  `event_processing_duration_seconds{topic}`, plus `notifications_sent_total` and the standard
  RED series for the inspection API.
- **Idempotent consumption** keyed by `order_id` — safe under at-least-once delivery / redelivery.
- **Trace correlation:** reads the incoming `traceparent`/B3 trace id on HTTP requests and stamps
  it on log lines (`trace_id`).
- **Structured JSON logs** to stdout (including the `"notification sent"` event line).
- **Graceful shutdown** via FastAPI lifespan on SIGTERM — the Kafka consumer drains and closes,
  then in-flight HTTP requests finish. Clean rollouts + KEDA scale-down.
- **Readiness gates on the Kafka consumer** being connected.

---

## Project layout

```
notification/
├── app/
│   ├── __main__.py       # entrypoint: `python -m app` -> uvicorn
│   ├── main.py           # FastAPI app, middleware, inspection + ops routes, lifespan
│   ├── consumer.py       # aiokafka consumer (retry-connect), ring buffer, idempotency
│   ├── config.py         # env-var configuration
│   ├── models.py         # Pydantic schema (Notification)
│   ├── metrics.py        # RED + async-worker metrics
│   ├── logging_setup.py  # JSON logging with trace_id
│   └── trace.py          # trace-context extraction + propagation header list
├── requirements.txt
├── Dockerfile            # multi-stage → slim non-root image
├── docker-compose.yml    # LOCAL dev: Redpanda + notification
├── .env.example
└── README.md
```
