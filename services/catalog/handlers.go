package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// dbTimeout bounds every database-backed request.
const dbTimeout = 5 * time.Second

// Handler holds dependencies shared by all HTTP handlers.
type Handler struct {
	store *Store
}

// NewRouter wires up all routes and middleware and returns the root handler.
// Uses the Go 1.22+ ServeMux which supports method + path-parameter patterns,
// so we need no third-party router.
func NewRouter(store *Store) http.Handler {
	h := &Handler{store: store}
	mux := http.NewServeMux()

	// Product CRUD
	mux.HandleFunc("GET /products", h.listProducts)
	mux.HandleFunc("POST /products", h.createProduct)
	mux.HandleFunc("GET /products/{id}", h.getProduct)
	mux.HandleFunc("PUT /products/{id}", h.updateProduct)
	mux.HandleFunc("DELETE /products/{id}", h.deleteProduct)

	// Operational endpoints
	mux.HandleFunc("GET /healthz", h.healthz) // liveness  — is the process up?
	mux.HandleFunc("GET /readyz", h.readyz)   // readiness — can it serve (DB reachable)?
	mux.Handle("GET /metrics", promhttp.Handler())

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	products, err := h.store.ListProducts(ctx, limit, offset)
	if err != nil {
		slog.Error("list products", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list products")
		return
	}
	writeJSON(w, http.StatusOK, products)
}

func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	p, err := h.store.GetProduct(ctx, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		slog.Error("get product", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get product")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeInput(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	p, err := h.store.CreateProduct(ctx, in)
	if err != nil {
		slog.Error("create product", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create product")
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	in, ok := decodeInput(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	p, err := h.store.UpdateProduct(ctx, id, in)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		slog.Error("update product", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update product")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) deleteProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	err := h.store.DeleteProduct(ctx, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		slog.Error("delete product", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete product")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid product id")
		return 0, false
	}
	return id, true
}

func decodeInput(w http.ResponseWriter, r *http.Request) (ProductInput, bool) {
	var in ProductInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return ProductInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return ProductInput{}, false
	}
	return in, true
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
