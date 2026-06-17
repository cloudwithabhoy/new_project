package main

import (
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewHandler wires up the operational endpoints + the reverse-proxy catch-all and
// returns the root handler with the metrics+logging middleware chain.
func NewHandler(router *Router, verifier *Verifier) http.Handler {
	mux := http.NewServeMux()

	// Operational endpoints (registered before the catch-all so they win).
	mux.HandleFunc("GET /healthz", withRoute("/healthz", healthz)) // liveness
	mux.HandleFunc("GET /readyz", withRoute("/readyz", readyz(verifier)))
	mux.Handle("GET /metrics", promhttp.Handler())

	// Everything else is reverse-proxied. The router sets its own route label
	// (the upstream prefix) per request for bounded-cardinality metrics.
	mux.Handle("/", router)

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

// healthz is the liveness probe: the process is running. No dependencies checked.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: only ready once auth's JWKS has been loaded, so
// the gateway never accepts traffic it cannot authenticate.
func readyz(verifier *Verifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifier.Ready() {
			writeError(w, http.StatusServiceUnavailable, "jwks not loaded")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

// --- helpers (mirrors auth's response helpers so error shapes are uniform) ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
