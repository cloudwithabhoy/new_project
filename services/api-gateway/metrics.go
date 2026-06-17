package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RED metrics (Rate, Errors, Duration) exposed on /metrics. The `route` label is
// the matched route TEMPLATE — for the gateway that is the upstream PREFIX
// (e.g. "/api/cart"), never the raw path ("/api/cart/carts/5"), so cardinality
// stays bounded — this is what your SLO and Grafana RED dashboards consume.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed.",
		},
		[]string{"route", "method", "status"},
	)
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route", "method"},
	)
)

// routeHolder lets a per-route wrapper communicate the matched route template
// back up to the metrics middleware (the handler runs inside the middleware, so
// we hand it a pointer to mutate rather than threading a new context back out).
type routeHolder struct{ name string }

// withRoute tags a handler with its route template for metrics labelling.
func withRoute(name string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setRoute(r, name)
		fn(w, r)
	}
}

// setRoute records the route template for the current request so the metrics
// middleware labels it correctly. The proxy handler uses this to report the
// upstream prefix (e.g. "/api/cart") rather than the full path.
func setRoute(r *http.Request, name string) {
	if h, ok := r.Context().Value(routeKey).(*routeHolder); ok {
		h.name = name
	}
}

// statusRecorder captures the response status code so middleware can record it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// metricsMiddleware records RED metrics for every request, labelled by route.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holder := &routeHolder{name: "other"}
		ctx := context.WithValue(r.Context(), routeKey, holder)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		httpRequestsTotal.WithLabelValues(holder.name, r.Method, strconv.Itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(holder.name, r.Method).Observe(time.Since(start).Seconds())
	})
}

// loggingMiddleware emits one structured log line per request with the trace id,
// and seeds the request context with that trace id for handler logs. It skips the
// high-frequency probe/metrics endpoints to avoid log spam.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := traceIDFrom(r)
		ctx := context.WithValue(r.Context(), traceIDKey, traceID)
		r = r.WithContext(ctx)

		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", traceID,
		)
	})
}
