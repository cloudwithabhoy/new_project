"""Minimal HTTP server for ops endpoints, run in a background thread.

A Kafka worker has no inbound HTTP traffic, but Kubernetes still wants a liveness
probe and Prometheus still wants a scrape target. Rather than pull in a web
framework, we serve `/healthz` and `/metrics` from the stdlib `http.server` on a
daemon thread alongside the asyncio consumer loop (api-details §1.2 / §3).
"""
from __future__ import annotations

import logging
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

log = logging.getLogger("thumbnail.http")


class _Handler(BaseHTTPRequestHandler):
    # Silence the default stderr access log; we emit our own structured logs.
    def log_message(self, *args, **kwargs) -> None:  # noqa: D401
        return

    def _send(self, status: int, body: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            # Liveness: the process is up. No dependency checks (api-details §1.2).
            self._send(200, b'{"status":"ok"}', "application/json")
        elif self.path == "/metrics":
            self._send(200, generate_latest(), CONTENT_TYPE_LATEST)
        else:
            self._send(404, b'{"error":"not found"}', "application/json")

    do_HEAD = do_GET


def start_metrics_server(port: int) -> ThreadingHTTPServer:
    """Start the ops HTTP server on a daemon thread and return it (for shutdown)."""
    httpd = ThreadingHTTPServer(("0.0.0.0", port), _Handler)
    thread = threading.Thread(
        target=httpd.serve_forever, name="metrics-http", daemon=True
    )
    thread.start()
    log.info("metrics server listening", extra={"port": port})
    return httpd
