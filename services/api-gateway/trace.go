package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// ctxKey is an unexported type for context keys defined in this package.
type ctxKey int

const (
	traceIDKey ctxKey = iota
	routeKey
)

// propagationHeaders are the tracing/correlation headers that must survive a hop.
//
// httputil.ReverseProxy copies request headers to the upstream by default, so the
// gateway already forwards these for free — but we keep the canonical list here so
// the trace-id logging helper and any explicit downstream calls (JWKS fetch) agree
// on what "trace context" means. Istio's sidecars generate the spans; the app's
// only job is to NOT drop these headers.
var propagationHeaders = []string{
	"x-request-id",
	"traceparent", // W3C trace context
	"tracestate",
	"x-b3-traceid", // Zipkin/B3 (Istio's default)
	"x-b3-spanid",
	"x-b3-parentspanid",
	"x-b3-sampled",
	"x-b3-flags",
	"b3",
}

// traceIDFromTraceparent extracts the 32-hex-char trace-id from a W3C
// traceparent header of the form: <version>-<trace-id>-<parent-id>-<flags>.
func traceIDFromTraceparent(tp string) string {
	parts := strings.Split(tp, "-")
	if len(parts) >= 2 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

// traceIDFrom returns the best available trace identifier for log correlation,
// preferring the W3C trace-id, then B3, then the request id.
func traceIDFrom(r *http.Request) string {
	if tid := traceIDFromTraceparent(r.Header.Get("traceparent")); tid != "" {
		return tid
	}
	if tid := r.Header.Get("x-b3-traceid"); tid != "" {
		return tid
	}
	return r.Header.Get("x-request-id")
}

// loggerFrom returns a logger annotated with the request's trace id (if any),
// so every log line can be joined to its distributed trace in Loki/Tempo.
func loggerFrom(ctx context.Context) *slog.Logger {
	if tid, ok := ctx.Value(traceIDKey).(string); ok && tid != "" {
		return slog.With("trace_id", tid)
	}
	return slog.Default()
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
