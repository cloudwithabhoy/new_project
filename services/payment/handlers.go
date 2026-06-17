package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// dbTimeout bounds every database-backed request.
const dbTimeout = 5 * time.Second

// Handler holds dependencies shared by all HTTP handlers.
type Handler struct {
	store    *Store
	failMode string
}

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(store *Store, failMode string) http.Handler {
	h := &Handler{store: store, failMode: failMode}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /payments", withRoute("/payments", h.createPayment))
	mux.HandleFunc("GET /payments/{id}", withRoute("/payments/{id}", h.getPayment))

	// Operational endpoints
	mux.HandleFunc("GET /healthz", withRoute("/healthz", h.healthz)) // liveness
	mux.HandleFunc("GET /readyz", withRoute("/readyz", h.readyz))    // readiness
	mux.Handle("GET /metrics", promhttp.Handler())

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

// createPayment is the mock authorizer. It approves by default; FAIL_MODE turns
// it into a deterministic fault target for chaos/Istio demos:
//
//	off     → persist "approved", return 201.
//	decline → persist "declined", return 201 (the order service maps this to 402).
//	error   → return 500 and persist NOTHING (simulates an authorizer outage).
//
// Every non-error outcome is written to Postgres so there is an audit trail.
func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	in, ok := decodePayment(w, r)
	if !ok {
		return
	}

	// The "error" fault is injected before we touch the database: the authorizer
	// "fell over" and produced no decision, so nothing is persisted.
	if h.failMode == FailModeError {
		log.Warn("FAIL_MODE=error: simulating authorizer outage", "order_id", in.OrderID)
		writeError(w, http.StatusInternalServerError, "payment authorizer error")
		return
	}

	// Decide the outcome. Default is approve; FAIL_MODE=decline forces a decline.
	status := StatusApproved
	if h.failMode == FailModeDecline {
		status = StatusDeclined
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	payment, err := h.store.CreatePayment(ctx, in.OrderID, in.UserID, in.AmountCents, status)
	if err != nil {
		log.Error("create payment", "error", err, "order_id", in.OrderID)
		writeError(w, http.StatusInternalServerError, "failed to record payment")
		return
	}

	log.Info("payment recorded", "payment_id", payment.ID, "order_id", payment.OrderID, "status", payment.Status)
	writeJSON(w, http.StatusCreated, payment)
}

// getPayment returns a single stored payment, 404 if it does not exist.
func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid payment id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	payment, err := h.store.GetByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "payment not found")
		return
	}
	if err != nil {
		log.Error("get payment", "error", err, "payment_id", id)
		writeError(w, http.StatusInternalServerError, "failed to fetch payment")
		return
	}

	writeJSON(w, http.StatusOK, payment)
}

// healthz is the liveness probe: the process is running. No dependencies checked.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: only ready if the database is reachable.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

func decodePayment(w http.ResponseWriter, r *http.Request) (PaymentInput, bool) {
	var in PaymentInput
	if !decodeJSON(w, r, &in) {
		return PaymentInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return PaymentInput{}, false
	}
	return in, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
