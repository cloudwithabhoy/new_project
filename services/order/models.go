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

// CreateOrderInput is the client payload for POST /orders. In the trimmed build
// there is no cart service, so the client submits the line items directly; the
// order total is computed server-side from them.
type CreateOrderInput struct {
	UserID int64       `json:"user_id"`
	Items  []OrderItem `json:"items"`
}

// Validate enforces: a positive user id and at least one valid line item.
func (in CreateOrderInput) Validate() error {
	if in.UserID <= 0 {
		return errors.New("user_id is required")
	}
	if len(in.Items) == 0 {
		return errors.New("items is required (at least one line)")
	}
	for _, it := range in.Items {
		if it.ProductID <= 0 || it.Quantity <= 0 {
			return errors.New("each item needs a positive product_id and quantity")
		}
	}
	return nil
}
