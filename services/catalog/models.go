package main

import (
	"errors"
	"strings"
	"time"
)

// Product is a catalog item as stored and returned by the API.
type Product struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceCents  int64     `json:"price_cents"`
	Stock       int       `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProductInput is the client-supplied payload for create/update requests.
// It deliberately omits server-managed fields (id, timestamps).
type ProductInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"price_cents"`
	Stock       int    `json:"stock"`
}

// Validate enforces basic input rules before the data reaches the database.
func (p ProductInput) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if p.PriceCents < 0 {
		return errors.New("price_cents must be >= 0")
	}
	if p.Stock < 0 {
		return errors.New("stock must be >= 0")
	}
	return nil
}
