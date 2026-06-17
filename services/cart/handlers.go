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

// redisTimeout bounds every Redis-backed request.
const redisTimeout = 5 * time.Second

// Handler holds dependencies shared by all HTTP handlers.
type Handler struct {
	store   *Store
	catalog *CatalogClient
}

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(store *Store, catalog *CatalogClient) http.Handler {
	h := &Handler{store: store, catalog: catalog}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /carts/{user_id}", withRoute("/carts/{user_id}", h.getCart))
	mux.HandleFunc("POST /carts/{user_id}/items", withRoute("/carts/{user_id}/items", h.addItem))
	mux.HandleFunc("DELETE /carts/{user_id}/items/{product_id}", withRoute("/carts/{user_id}/items/{product_id}", h.removeItem))
	mux.HandleFunc("DELETE /carts/{user_id}", withRoute("/carts/{user_id}", h.clearCart))

	// Operational endpoints
	mux.HandleFunc("GET /healthz", withRoute("/healthz", h.healthz)) // liveness
	mux.HandleFunc("GET /readyz", withRoute("/readyz", h.readyz))    // readiness
	mux.Handle("GET /metrics", promhttp.Handler())

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

// getCart returns the user's cart, or an empty cart if none exists yet.
func (h *Handler) getCart(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	userID := r.PathValue("user_id")

	ctx, cancel := context.WithTimeout(r.Context(), redisTimeout)
	defer cancel()

	cart, err := h.store.Get(ctx, userID)
	if err != nil {
		log.Error("get cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to load cart")
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

// addItem validates the product against catalog (the traced sync hop), snapshots
// its name + price into the cart line, and returns the updated cart. Adding a
// product already in the cart increments its quantity.
func (h *Handler) addItem(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	userID := r.PathValue("user_id")

	in, ok := decodeAddItem(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), redisTimeout)
	defer cancel()

	// Validate the product and snapshot name + price from catalog. A 404 there
	// becomes a 404 here; any other failure is an upstream problem (502).
	product, err := h.catalog.GetProduct(ctx, in.ProductID, propagate(r))
	if errors.Is(err, ErrProductNotFound) {
		writeError(w, http.StatusNotFound, "product not found")
		return
	}
	if err != nil {
		log.Error("catalog lookup failed", "error", err, "product_id", in.ProductID)
		writeError(w, http.StatusBadGateway, "catalog service unavailable")
		return
	}

	cart, err := h.store.Get(ctx, userID)
	if err != nil {
		log.Error("get cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to load cart")
		return
	}

	cart.upsertItem(product.ID, product.Name, product.PriceCents, in.Quantity)

	if err := h.store.Save(ctx, &cart); err != nil {
		log.Error("save cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to update cart")
		return
	}

	log.Info("item added to cart", "user_id", userID, "product_id", product.ID, "quantity", in.Quantity)
	writeJSON(w, http.StatusOK, cart)
}

// removeItem drops a product line and returns the updated cart. Removing an
// absent product is a no-op success — DELETE is idempotent.
func (h *Handler) removeItem(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	userID := r.PathValue("user_id")

	productID, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid product_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), redisTimeout)
	defer cancel()

	cart, err := h.store.Get(ctx, userID)
	if err != nil {
		log.Error("get cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to load cart")
		return
	}

	cart.removeItem(productID)

	if err := h.store.Save(ctx, &cart); err != nil {
		log.Error("save cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to update cart")
		return
	}

	log.Info("item removed from cart", "user_id", userID, "product_id", productID)
	writeJSON(w, http.StatusOK, cart)
}

// clearCart deletes the user's cart entirely, returning 204. Clearing an absent
// cart is a no-op success.
func (h *Handler) clearCart(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	userID := r.PathValue("user_id")

	ctx, cancel := context.WithTimeout(r.Context(), redisTimeout)
	defer cancel()

	if err := h.store.Delete(ctx, userID); err != nil {
		log.Error("clear cart", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to clear cart")
		return
	}

	log.Info("cart cleared", "user_id", userID)
	w.WriteHeader(http.StatusNoContent)
}

// healthz is the liveness probe: the process is running. No dependencies checked.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: only ready if Redis is reachable.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

func decodeAddItem(w http.ResponseWriter, r *http.Request) (AddItemInput, bool) {
	var in AddItemInput
	if !decodeJSON(w, r, &in) {
		return AddItemInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return AddItemInput{}, false
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
