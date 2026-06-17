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

// orchestrateTimeout bounds a whole checkout fan-out (cart -> payment ->
// inventory -> persist -> emit). It must comfortably exceed the sum of the
// per-client 5s timeouts plus the DB writes.
const orchestrateTimeout = 25 * time.Second

// Handler holds dependencies shared by all HTTP handlers.
type Handler struct {
	store     *Store
	cart      *CartClient
	payment   *PaymentClient
	inventory *InventoryClient
	producer  *Producer
}

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(store *Store, cart *CartClient, payment *PaymentClient, inventory *InventoryClient, producer *Producer) http.Handler {
	h := &Handler{store: store, cart: cart, payment: payment, inventory: inventory, producer: producer}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /orders", withRoute("/orders", h.createOrder))
	mux.HandleFunc("GET /orders/{id}", withRoute("/orders/{id}", h.getOrder))
	mux.HandleFunc("GET /orders", withRoute("/orders", h.listOrders))

	// Operational endpoints
	mux.HandleFunc("GET /healthz", withRoute("/healthz", h.healthz)) // liveness
	mux.HandleFunc("GET /readyz", withRoute("/readyz", h.readyz))    // readiness
	mux.Handle("GET /metrics", promhttp.Handler())

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

// createOrder is the orchestration: the checkout saga per §3.
//
//	1. GET cart (400 if empty)
//	2. pre-allocate order_id, POST payment  (402 if declined)
//	3. POST inventory/reserve               (409 passthrough)
//	4. persist order (status "confirmed") + emit order.created
//	5. best-effort clear cart
//
// Trace headers are forwarded on every downstream hop so the whole checkout is
// one trace.
func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	in, ok := decodeCreateOrder(w, r)
	if !ok {
		return
	}

	headers := propagate(r)
	ctx, cancel := context.WithTimeout(r.Context(), orchestrateTimeout)
	defer cancel()

	// 1. Source the authoritative cart. Empty cart -> 400.
	cart, err := h.cart.GetCart(ctx, in.UserID, headers)
	if errors.Is(err, ErrCartEmpty) {
		writeError(w, http.StatusBadRequest, "cart is empty")
		return
	}
	if err != nil {
		log.Error("get cart failed", "error", err, "user_id", in.UserID)
		writeError(w, http.StatusBadGateway, "cart service unavailable")
		return
	}

	// 2. Pre-allocate the order id from the sequence so payment + inventory +
	// the persisted row + the Kafka key all agree on it (see store.NextID).
	idCtx, idCancel := context.WithTimeout(ctx, dbTimeout)
	orderID, err := h.store.NextID(idCtx)
	idCancel()
	if err != nil {
		log.Error("allocate order id failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create order")
		return
	}

	// Charge payment with the pre-allocated id. Declines are a normal business
	// outcome -> 402, not a 5xx.
	_, err = h.payment.Charge(ctx, PaymentRequest{
		OrderID:     orderID,
		UserID:      in.UserID,
		AmountCents: cart.TotalCents,
	}, headers)
	if errors.Is(err, ErrPaymentDeclined) {
		log.Info("payment declined", "order_id", orderID, "user_id", in.UserID)
		writeError(w, http.StatusPaymentRequired, "payment declined")
		return
	}
	if err != nil {
		log.Error("payment failed", "error", err, "order_id", orderID)
		writeError(w, http.StatusBadGateway, "payment service unavailable")
		return
	}

	// 3. Reserve inventory (all-or-none). 409 passes straight through.
	if err := h.inventory.Reserve(ctx, ReserveRequest{
		OrderID: orderID,
		Items:   reserveItems(cart.Items),
	}, headers); err != nil {
		if errors.Is(err, ErrInsufficientStock) {
			log.Info("inventory reservation rejected", "order_id", orderID)
			writeError(w, http.StatusConflict, "insufficient stock")
			return
		}
		log.Error("inventory reserve failed", "error", err, "order_id", orderID)
		writeError(w, http.StatusBadGateway, "inventory service unavailable")
		return
	}

	// 4. Persist the confirmed order, then emit order.created.
	order := Order{
		ID:         orderID,
		UserID:     in.UserID,
		Items:      cart.Items,
		TotalCents: cart.TotalCents,
		Status:     "confirmed",
		CreatedAt:  time.Now().UTC(),
	}
	insCtx, insCancel := context.WithTimeout(ctx, dbTimeout)
	err = h.store.Insert(insCtx, order)
	insCancel()
	if err != nil {
		log.Error("persist order failed", "error", err, "order_id", orderID)
		writeError(w, http.StatusInternalServerError, "failed to create order")
		return
	}

	// Emit the event. Payment is captured and inventory reserved, so the order
	// IS confirmed even if the broker hiccups; we log the emit failure loudly
	// (it'd leave downstream consumers out of sync — a known at-least-once gap a
	// real system would close with a transactional outbox) but still return 201.
	if err := h.producer.Emit(ctx, OrderCreatedEvent{
		Event:      "order.created",
		OrderID:    order.ID,
		UserID:     order.UserID,
		Items:      eventItems(order.Items),
		TotalCents: order.TotalCents,
		CreatedAt:  order.CreatedAt,
	}); err != nil {
		log.Error("emit order.created failed", "error", err, "order_id", order.ID)
	}

	// 5. Best-effort cart clear — never fail the order over this.
	if err := h.cart.ClearCart(ctx, in.UserID, headers); err != nil {
		log.Warn("cart clear failed (best-effort)", "error", err, "user_id", in.UserID)
	}

	log.Info("order created", "order_id", order.ID, "user_id", order.UserID, "total_cents", order.TotalCents)
	writeJSON(w, http.StatusCreated, order)
}

// getOrder returns a single order by id (404 if missing).
func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	order, err := h.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		loggerFrom(r.Context()).Error("get order failed", "error", err, "order_id", id)
		writeError(w, http.StatusInternalServerError, "failed to fetch order")
		return
	}
	writeJSON(w, http.StatusOK, order)
}

// listOrders returns all orders for a user via ?user_id=.
func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "user_id query parameter is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	orders, err := h.store.ListByUser(ctx, userID)
	if err != nil {
		loggerFrom(r.Context()).Error("list orders failed", "error", err, "user_id", userID)
		writeError(w, http.StatusInternalServerError, "failed to list orders")
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

// healthz is the liveness probe: the process is running. No dependencies checked.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: ready only if the database is reachable. The
// Kafka writer is created at startup (lazy-dial) so a constructed producer is
// enough for the broker dependency; the DB ping is the hard gate.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	if h.producer == nil {
		writeError(w, http.StatusServiceUnavailable, "kafka writer not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

// reserveItems projects cart lines into the inventory reservation shape.
func reserveItems(items []OrderItem) []ReserveItem {
	out := make([]ReserveItem, 0, len(items))
	for _, it := range items {
		out = append(out, ReserveItem{ProductID: it.ProductID, Quantity: it.Quantity})
	}
	return out
}

// eventItems projects order lines into the leaner order.created event shape (§4).
func eventItems(items []OrderItem) []EventItem {
	out := make([]EventItem, 0, len(items))
	for _, it := range items {
		out = append(out, EventItem{ProductID: it.ProductID, Quantity: it.Quantity, PriceCents: it.PriceCents})
	}
	return out
}

func decodeCreateOrder(w http.ResponseWriter, r *http.Request) (CreateOrderInput, bool) {
	var in CreateOrderInput
	if !decodeJSON(w, r, &in) {
		return CreateOrderInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return CreateOrderInput{}, false
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
