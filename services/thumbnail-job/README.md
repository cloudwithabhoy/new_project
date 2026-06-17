# thumbnail-job (worker)

Generates product thumbnails. A small **Python** Kafka **worker** (not a web service) that consumes
`thumbnail.requests`, "processes" each image (simulated ~200ms of resize/encode work), and emits
worker metrics + structured logs.

It is **stateless**, has **no inbound HTTP traffic**, and is built for **KEDA scale-to-zero** on
Kafka lag: idle → 0 replicas; a burst of messages → KEDA scales it up; lag drained → back to 0.

> **Worker, not a CRUD app.** Unlike `services/user` (FastAPI + Postgres), there is no API and no
> database. The only HTTP it serves is `/healthz` + `/metrics` on `:9100` for the probe and the
> Prometheus scrape — KEDA does the autoscaling off Kafka lag, separately.

---

## TL;DR — run it locally

```bash
cd services/thumbnail-job
docker compose up --build         # Redpanda (Kafka API) + the worker

# In another shell: push a burst of test messages and watch the worker logs.
pip install aiokafka
KAFKA_BROKERS=localhost:9092 python producer.py 500

curl localhost:9100/healthz
curl localhost:9100/metrics | grep -E 'thumbnails_processed_total|thumbnail_processing_duration_seconds'
```

Each consumed message logs a `thumbnail start` / `thumbnail done` JSON line with `product_id`,
`image_url`, `duration_ms`, and a `trace_id` (if the producer set trace headers).

---

## What it does

- **Consumes** Kafka topic `thumbnail.requests` (group `thumbnail`) with `aiokafka`.
- Each message is JSON `{ "product_id": 7, "image_url": "https://example/img/7.jpg" }` (api-details §4).
- **Processes** it: log start → `asyncio.sleep(0.2)` (simulated work) → log done.
- **At-least-once + idempotent:** offsets auto-commit; re-processing a duplicate just redoes the
  sleep (harmless). Malformed JSON is counted as `error` and skipped — one bad message never kills
  the loop.
- **Graceful shutdown** on SIGTERM/SIGINT: stop consuming, finish the in-flight message, close the
  consumer (commit offsets), stop the metrics server, exit 0.

---

## Configuration (environment variables)

See [`.env.example`](./.env.example). All keys are non-secret → **ConfigMap**.

| Variable | Default | Notes |
|---|---|---|
| `KAFKA_BROKERS` | `localhost:9092` | k8s: `kafka:9092` |
| `KAFKA_TOPIC` | `thumbnail.requests` | source topic |
| `KAFKA_GROUP` | `thumbnail` | consumer group — **must match** the KEDA trigger's `consumerGroup` |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `METRICS_PORT` | `9100` | `/healthz` + `/metrics` listen port |

---

## Metrics

Worker metrics (not RED HTTP metrics — this is an async consumer, api-details §1.3) at
`http://<host>:9100/metrics`:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `thumbnails_processed_total` | counter | `result` (`success`\|`error`) | messages processed |
| `thumbnail_processing_duration_seconds` | histogram | — | per-message work time |

---

## 📦 DevOps handoff — what you need for the manifests

- **Container port:** `9100` (HTTP — `/healthz` + `/metrics` only; **no** business traffic).
- **Image:** build from this dir; push to ECR as `…/thumbnail-job:<git-sha>`.
- **Probes:**
  - Liveness  → `GET /healthz` on `9100` (always 200 if the process is up).
  - Readiness → not strictly needed (no inbound traffic). If you want one, reuse `GET /healthz`;
    do **not** gate readiness on Kafka, or a scaled-up pod could flap during rebalances.
- **ConfigMap keys:** `KAFKA_BROKERS`, `KAFKA_TOPIC`, `KAFKA_GROUP`, `LOG_LEVEL`, `METRICS_PORT`.
- **Secret keys:** none.
- **Dependencies:** Kafka brokers reachable at `KAFKA_BROKERS` (`kafka:9092`). No DB.
- **Service discovery:** makes **no** upstream calls; nothing calls it over HTTP. Work arrives via
  Kafka only.
- **Scrape:** annotate the pod for Prometheus (`prometheus.io/scrape: "true"`,
  `prometheus.io/port: "9100"`, `prometheus.io/path: "/metrics"`) or add a ServiceMonitor.
- **Resource hints (tune later):** requests `cpu: 50m, memory: 96Mi` · limits `cpu: 250m,
  memory: 192Mi`.
- **Security:** runs as **non-root** (uid 10001); filesystem can be read-only.

> Suggested manifest set: `Deployment` (with `minReplicaCount: 0` via KEDA), `ConfigMap`,
> a KEDA `ScaledObject`, and (optional) a headless `Service` + `ServiceMonitor` for scraping.
> No `Service` is required for traffic — there is none.

### KEDA ScaledObject — scale-to-zero on Kafka lag

KEDA watches the consumer-group lag on `thumbnail.requests` and scales the Deployment between
`minReplicaCount: 0` and `maxReplicaCount` based on `lagThreshold`. Trigger to wire up:

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: thumbnail-job
spec:
  scaleTargetRef:
    name: thumbnail-job            # the Deployment
  minReplicaCount: 0               # scale-to-zero when there is no lag
  maxReplicaCount: 10
  cooldownPeriod: 60               # wait 60s of no lag before scaling back to 0
  pollingInterval: 15
  triggers:
    - type: kafka
      metadata:
        bootstrapServers: kafka:9092          # == KAFKA_BROKERS
        consumerGroup: thumbnail              # == KAFKA_GROUP
        topic: thumbnail.requests             # == KAFKA_TOPIC
        lagThreshold: "5"                     # ~1 replica per 5 lagging messages
        offsetResetPolicy: earliest
```

The three Kafka values **must** match this worker's env (`KAFKA_BROKERS` / `KAFKA_GROUP` /
`KAFKA_TOPIC`) or KEDA will read the wrong lag. To demo it: `kubectl get deploy thumbnail-job`
should show `0/0`; run `producer.py 5000`; watch it scale up, drain, and return to `0/0`.

---

## Enterprise contract (what this worker gives your platform layer)

- **Worker metrics** at `/metrics`: `thumbnails_processed_total{result}` and
  `thumbnail_processing_duration_seconds` — throughput + latency for Grafana/SLOs.
- **Trace correlation:** reads `traceparent`/B3 trace id from the **Kafka message headers** and
  stamps it on every log line (`trace_id`), so a Loki log links to its Tempo/Jaeger trace.
- **Structured JSON logs** to stdout, one object per line.
- **Graceful shutdown** on SIGTERM: drains the in-flight message, commits offsets, exits 0 — clean
  rollouts and clean KEDA scale-downs.
- **Retry-connect** to Kafka with backoff (the broker pod may start after this one).

---

## Project layout

```
thumbnail-job/
├── app/
│   ├── __main__.py         # entrypoint: `python -m app` -> worker.main()
│   ├── worker.py           # aiokafka consume loop, processing, graceful shutdown
│   ├── config.py           # env-var configuration
│   ├── server.py           # stdlib http.server thread: /healthz + /metrics on :9100
│   ├── metrics.py          # worker metrics (processed_total, processing_duration)
│   ├── logging_setup.py    # JSON logging with trace_id
│   └── trace.py            # trace-id extraction from Kafka headers
├── producer.py             # standalone load generator (enqueue N test messages)
├── requirements.txt        # aiokafka + prometheus-client
├── Dockerfile              # multi-stage → slim non-root image
├── docker-compose.yml      # LOCAL dev: Redpanda + thumbnail-job
├── .env.example
└── README.md
```
