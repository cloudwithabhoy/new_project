package main

import "errors"

// Cart is the full cart document persisted as JSON at Redis key cart:{user_id}.
// total_cents is always recomputed from the lines before the cart is stored or
// returned, so it can never drift from the items.
type Cart struct {
	UserID     string     `json:"user_id"`
	Items      []CartItem `json:"items"`
	TotalCents int64      `json:"total_cents"`
}

// CartItem is a single line in the cart. name and price_cents are SNAPSHOTTED
// from the catalog at add-time — the cart deliberately does not re-fetch them, so
// a later price change in catalog doesn't silently mutate an existing cart.
type CartItem struct {
	ProductID  int64  `json:"product_id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Quantity   int    `json:"quantity"`
}

// AddItemInput is the client payload for POST /carts/{user_id}/items.
type AddItemInput struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

// Validate enforces basic add-to-cart rules before anything touches Redis or the
// downstream catalog service.
func (in AddItemInput) Validate() error {
	if in.ProductID <= 0 {
		return errors.New("a valid product_id is required")
	}
	if in.Quantity <= 0 {
		return errors.New("quantity must be greater than zero")
	}
	return nil
}

// recompute refreshes total_cents from the current line items and guarantees a
// non-nil items slice so an empty cart marshals as [] rather than null.
func (c *Cart) recompute() {
	if c.Items == nil {
		c.Items = []CartItem{}
	}
	var total int64
	for _, it := range c.Items {
		total += it.PriceCents * int64(it.Quantity)
	}
	c.TotalCents = total
}

// upsertItem adds quantity to an existing product line or appends a new one,
// snapshotting the catalog name + price. Adding an existing product increments
// its quantity rather than duplicating the line.
func (c *Cart) upsertItem(productID int64, name string, priceCents int64, quantity int) {
	for i := range c.Items {
		if c.Items[i].ProductID == productID {
			c.Items[i].Quantity += quantity
			// Refresh the snapshot to the latest catalog values on re-add.
			c.Items[i].Name = name
			c.Items[i].PriceCents = priceCents
			return
		}
	}
	c.Items = append(c.Items, CartItem{
		ProductID:  productID,
		Name:       name,
		PriceCents: priceCents,
		Quantity:   quantity,
	})
}

// removeItem drops the line for productID (if present). Removing an absent
// product is a no-op success — DELETE is idempotent.
func (c *Cart) removeItem(productID int64) {
	out := c.Items[:0]
	for _, it := range c.Items {
		if it.ProductID != productID {
			out = append(out, it)
		}
	}
	c.Items = out
}
