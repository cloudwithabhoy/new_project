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

// propagationHeaders are the tracing/correlation headers we copy from an
// incoming request onto any outgoing request, so a single trace stitches
// together across services through the Istio mesh (Envoy + Jaeger/Tempo).
//
// payment makes no downstream calls today, but we keep the list so that any
// future hop forwards the headers — dropping them breaks the trace at this hop.
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

// propagate builds a header set to attach to outgoing requests, copying the
// trace-context headers from the incoming request.
func propagate(r *http.Request) http.Header {
	h := http.Header{}
	for _, key := range propagationHeaders {
		if v := r.Header.Get(key); v != "" {
			h.Set(key, v)
		}
	}
	return h
}

// loggerFrom returns a logger annotated with the request's trace id (if any),
// so every log line can be joined to its distributed trace in Loki/Tempo.
func loggerFrom(ctx context.Context) *slog.Logger {
	if tid, ok := ctx.Value(traceIDKey).(string); ok && tid != "" {
		return slog.With("trace_id", tid)
	}
	return slog.Default()
}
