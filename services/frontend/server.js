// frontend — Express server that serves a single-page static UI and exposes
// runtime config + ops endpoints. No datastore. Node 20 (global fetch), ESM.
//
// Conventions (services/api-details.md §1):
//   - config 100% via env vars
//   - GET /healthz (liveness), /readyz (readiness), /metrics (Prometheus)
//   - RED metrics: http_requests_total{route,method,status},
//     http_request_duration_seconds{route,method}
//   - structured JSON logs to stdout with a trace_id
//   - graceful shutdown on SIGTERM (drain in-flight)

import express from 'express';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import {
  Registry,
  collectDefaultMetrics,
  Counter,
  Histogram,
} from 'prom-client';

// ---------------------------------------------------------------------------
// Config (env vars, with local-dev defaults)
// ---------------------------------------------------------------------------
const config = {
  port: parseInt(process.env.PORT ?? '3000', 10),
  logLevel: (process.env.LOG_LEVEL ?? 'info').toLowerCase(),
  apiGatewayUrl: process.env.API_GATEWAY_URL ?? 'http://localhost:8080',
};

const __dirname = dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = join(__dirname, 'public');

// ---------------------------------------------------------------------------
// Structured JSON logging to stdout, with leveling + trace_id
// ---------------------------------------------------------------------------
const LEVELS = { debug: 10, info: 20, warn: 30, error: 40 };
const threshold = LEVELS[config.logLevel] ?? LEVELS.info;

function log(level, msg, fields = {}) {
  if ((LEVELS[level] ?? LEVELS.info) < threshold) return;
  const line = {
    ts: new Date().toISOString(),
    level,
    service: 'frontend',
    msg,
    ...fields,
  };
  process.stdout.write(`${JSON.stringify(line)}\n`);
}

// Trace propagation (api-details §1.5): W3C traceparent, then B3, then x-request-id.
const TRACE_HEADERS = [
  'x-request-id',
  'traceparent',
  'tracestate',
  'x-b3-traceid',
  'x-b3-spanid',
  'x-b3-parentspanid',
  'x-b3-sampled',
  'x-b3-flags',
  'b3',
];

function traceIdFrom(req) {
  const tp = req.headers['traceparent'];
  if (typeof tp === 'string') {
    // version-traceid-spanid-flags
    const parts = tp.split('-');
    if (parts.length >= 2 && parts[1]) return parts[1];
  }
  const b3 = req.headers['x-b3-traceid'];
  if (typeof b3 === 'string' && b3) return b3;
  const rid = req.headers['x-request-id'];
  if (typeof rid === 'string' && rid) return rid;
  return undefined;
}

// ---------------------------------------------------------------------------
// Metrics (prom-client): default metrics + RED + page_views_total
// ---------------------------------------------------------------------------
const registry = new Registry();
collectDefaultMetrics({ register: registry });

const httpRequests = new Counter({
  name: 'http_requests_total',
  help: 'Total HTTP requests',
  labelNames: ['route', 'method', 'status'],
  registers: [registry],
});

const httpDuration = new Histogram({
  name: 'http_request_duration_seconds',
  help: 'HTTP request duration in seconds',
  labelNames: ['route', 'method'],
  registers: [registry],
});

const pageViews = new Counter({
  name: 'page_views_total',
  help: 'Total page views of the single-page UI',
  registers: [registry],
});

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------
const app = express();
app.disable('x-powered-by');

// Per-request: stamp trace_id, time the request, record RED metrics on finish.
app.use((req, res, next) => {
  const start = process.hrtime.bigint();
  req.traceId = traceIdFrom(req);

  res.on('finish', () => {
    // route template (bounded cardinality), never the raw path.
    const route =
      req.route?.path ?? (req.path === '/' ? '/' : req.path) ?? 'unknown';
    const method = req.method;
    const status = String(res.statusCode);
    const seconds = Number(process.hrtime.bigint() - start) / 1e9;

    httpRequests.inc({ route, method, status });
    httpDuration.observe({ route, method }, seconds);

    log('info', 'http_request', {
      trace_id: req.traceId,
      route,
      method,
      status: res.statusCode,
      duration_ms: Math.round(seconds * 1000),
    });
  });

  next();
});

// Runtime config for the browser — backend URL is NOT baked into the page.
app.get('/config', (req, res) => {
  res.json({ apiGatewayUrl: config.apiGatewayUrl });
});

// Liveness — no dependency checks, always 200 if the process is up.
app.get('/healthz', (req, res) => {
  res.json({ status: 'ok' });
});

// Readiness — 200. We do a best-effort reachability probe of the gateway's
// /healthz but DO NOT hard-fail readiness on it: the frontend is a static UI
// and the browser (not this pod) talks to the gateway, so a transient gateway
// blip should not pull the UI out of rotation. We report the probe result for
// observability only.
app.get('/readyz', async (req, res) => {
  let gateway = 'unknown';
  try {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 1000);
    const headers = forwardTraceHeaders(req);
    const r = await fetch(`${config.apiGatewayUrl}/healthz`, {
      signal: ctrl.signal,
      headers,
    });
    clearTimeout(t);
    gateway = r.ok ? 'reachable' : `status_${r.status}`;
  } catch (err) {
    gateway = 'unreachable';
    log('debug', 'readyz gateway probe failed', {
      trace_id: req.traceId,
      error: String(err),
    });
  }
  res.status(200).json({ status: 'ready', api_gateway: gateway });
});

// Prometheus exposition.
app.get('/metrics', async (req, res) => {
  res.set('Content-Type', registry.contentType);
  res.end(await registry.metrics());
});

// Count page views on the SPA entrypoint, then serve static assets.
app.get('/', (req, res, next) => {
  pageViews.inc();
  res.sendFile(join(PUBLIC_DIR, 'index.html'), (err) => {
    if (err) next(err);
  });
});

app.use(express.static(PUBLIC_DIR));

// 404 + error handlers, JSON shape per api-details §1.10.
app.use((req, res) => {
  res.status(404).json({ error: 'not found' });
});

// eslint-disable-next-line no-unused-vars
app.use((err, req, res, next) => {
  log('error', 'unhandled error', {
    trace_id: req.traceId,
    error: String(err?.stack ?? err),
  });
  if (res.headersSent) return;
  res.status(500).json({ error: 'internal server error' });
});

// Helper: copy through trace headers on downstream calls (api-details §1.5).
function forwardTraceHeaders(req) {
  const out = {};
  for (const h of TRACE_HEADERS) {
    const v = req.headers[h];
    if (typeof v === 'string' && v) out[h] = v;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Start + graceful shutdown
// ---------------------------------------------------------------------------
const server = app.listen(config.port, () => {
  log('info', 'frontend listening', {
    port: config.port,
    api_gateway_url: config.apiGatewayUrl,
    log_level: config.logLevel,
  });
});

function shutdown(signal) {
  log('info', 'shutdown signal received, draining', { signal });
  server.close((err) => {
    if (err) {
      log('error', 'error during shutdown', { error: String(err) });
      process.exit(1);
    }
    log('info', 'drained, exiting');
    process.exit(0);
  });
  // Safety net: force exit if drain hangs.
  setTimeout(() => {
    log('warn', 'drain timed out, forcing exit');
    process.exit(1);
  }, 10000).unref();
}

process.on('SIGTERM', () => shutdown('SIGTERM'));
process.on('SIGINT', () => shutdown('SIGINT'));


//test