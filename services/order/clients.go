package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The only synchronous inter-service hop the order orchestrator makes is
// order -> inventory (reserve stock during checkout). It forwards the incoming
// trace-context headers so the whole checkout shows up as ONE trace in
// Kiali/Jaeger. Istio adds retries/timeouts/circuit-breaking at the mesh layer,
// but a client-side timeout is still good hygiene so a hung dependency can't pin
// a checkout forever.

// ErrInsufficientStock means inventory could not reserve all lines (409).
var ErrInsufficientStock = errors.New("insufficient stock")

// newHTTPClient builds a client with a bounded timeout shared by all downstream calls.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// applyHeaders copies the propagated trace headers onto an outgoing request.
func applyHeaders(req *http.Request, propagated http.Header) {
	req.Header.Set("Content-Type", "application/json")
	for key, vals := range propagated {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}
}

// --- inventory client ---

// ReserveItem is one line of a reservation request. inventory only needs the
// product id and quantity.
type ReserveItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

// ReserveRequest is the payload order sends to the inventory service.
type ReserveRequest struct {
	OrderID int64         `json:"order_id"`
	Items   []ReserveItem `json:"items"`
}

// InventoryClient calls the inventory service.
type InventoryClient struct {
	baseURL string
	http    *http.Client
}

func NewInventoryClient(baseURL string) *InventoryClient {
	return &InventoryClient{baseURL: baseURL, http: newHTTPClient()}
}

// Reserve places an all-or-none stock reservation for the order. A 409 from
// inventory ("insufficient stock") surfaces as ErrInsufficientStock so the
// handler can pass the 409 straight through. The reservation is idempotent by
// order_id on the inventory side, so a retry of the same order is safe.
func (c *InventoryClient) Reserve(ctx context.Context, in ReserveRequest, propagated http.Header) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inventory/reserve", bytes.NewReader(body))
	if err != nil {
		return err
	}
	applyHeaders(req, propagated)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call inventory service: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		return ErrInsufficientStock
	default:
		return fmt.Errorf("inventory service returned status %d", resp.StatusCode)
	}
}
