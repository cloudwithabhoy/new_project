package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrProductNotFound means the catalog service does not know this product (404).
var ErrProductNotFound = errors.New("product not found")

// CatalogClient is a thin HTTP client for the downstream catalog service. This is
// the synchronous inter-service hop (cart -> catalog) that gives the Istio mesh a
// real distributed trace to display in Kiali/Jaeger.
type CatalogClient struct {
	baseURL string
	http    *http.Client
}

// NewCatalogClient builds a client with a bounded timeout. Istio adds retries/
// timeouts/circuit-breaking at the mesh layer, but a client-side timeout is still
// good hygiene so a hung dependency can't pin a request forever.
func NewCatalogClient(baseURL string) *CatalogClient {
	return &CatalogClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Product is the subset of the catalog service's response cart needs to snapshot
// into a cart line.
type Product struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}

// GetProduct fetches a product from catalog so cart can validate it exists and
// snapshot its name + price. propagated carries the trace-context headers from
// the originating request so the call joins the trace. A catalog 404 surfaces as
// ErrProductNotFound so the handler can return 404 "product not found".
func (c *CatalogClient) GetProduct(ctx context.Context, productID int64, propagated http.Header) (*Product, error) {
	url := fmt.Sprintf("%s/products/%d", c.baseURL, productID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, vals := range propagated {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call catalog service: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out Product
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode catalog response: %w", err)
		}
		return &out, nil
	case http.StatusNotFound:
		return nil, ErrProductNotFound
	default:
		return nil, fmt.Errorf("catalog service returned status %d", resp.StatusCode)
	}
}
