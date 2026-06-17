package main

import (
	"errors"
	"time"
)

// OrderItem is a single line in an order. It mirrors the cart line shape
// (product_id, name, price_cents, quantity) so the cart snapshot carries
// straight through into the persisted order.
type OrderItem struct {
	ProductID  int64  `json:"product_id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Quantity   int    `json:"quantity"`
}

// Order is the canonical order resource returned by the API and persisted in
// Postgres. The contract (§3) defines exactly these fields.
type Order struct {
	ID         int64       `json:"id"`
	UserID     int64       `json:"user_id"`
	Items      []OrderItem `json:"items"`
	TotalCents int64       `json:"total_cents"`
	Status     string      `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
}

// CreateOrderInput is the client payload for POST /orders. The orchestrator only
// needs the user id — the cart contents, totals, and pricing are sourced
// authoritatively from the cart service, never trusted from the client.
type CreateOrderInput struct {
	UserID int64 `json:"user_id"`
}

// Validate enforces the minimal rule: a positive user id.
func (in CreateOrderInput) Validate() error {
	if in.UserID <= 0 {
		return errors.New("user_id is required")
	}
	return nil
}
