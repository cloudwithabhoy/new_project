package main

import (
	"errors"
	"time"
)

// Payment status values (the only two the contract allows on a stored row).
const (
	StatusApproved = "approved"
	StatusDeclined = "declined"
)

// Payment is a stored authorization decision. The amount is in integer cents
// (no floats for money — avoids rounding surprises across the platform).
type Payment struct {
	ID         int64     `json:"id"`
	OrderID    int64     `json:"order_id"`
	UserID     int64     `json:"user_id"`
	AmountCents int64    `json:"amount_cents"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// PaymentInput is the client payload for POST /payments. The order service is
// the primary caller (it generates the order_id before persisting the order).
type PaymentInput struct {
	OrderID     int64 `json:"order_id"`
	UserID      int64 `json:"user_id"`
	AmountCents int64 `json:"amount_cents"`
}

// Validate enforces basic rules before anything touches the database.
func (in PaymentInput) Validate() error {
	if in.OrderID <= 0 {
		return errors.New("order_id is required")
	}
	if in.UserID <= 0 {
		return errors.New("user_id is required")
	}
	if in.AmountCents <= 0 {
		return errors.New("amount_cents must be positive")
	}
	return nil
}
