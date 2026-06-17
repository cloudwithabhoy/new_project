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

// ErrUserConflict means the user service rejected the email as already existing.
var ErrUserConflict = errors.New("user already exists")

// UserClient is a thin HTTP client for the downstream user service. This is the
// synchronous inter-service hop (auth -> user) that gives the Istio mesh a real
// distributed trace to display in Kiali/Jaeger.
type UserClient struct {
	baseURL string
	http    *http.Client
}

// NewUserClient builds a client with a bounded timeout. Istio adds retries/
// timeouts/circuit-breaking at the mesh layer, but a client-side timeout is still
// good hygiene so a hung dependency can't pin a request forever.
func NewUserClient(baseURL string) *UserClient {
	return &UserClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// CreateUserRequest is the payload auth sends to the user service.
type CreateUserRequest struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

// CreateUserResponse is the subset of the user service's response auth needs.
type CreateUserResponse struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

// CreateUser asks the user service to create a profile. propagated carries the
// trace-context headers from the originating request so the call joins the trace.
func (c *UserClient) CreateUser(ctx context.Context, in CreateUserRequest, propagated http.Header) (*CreateUserResponse, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/users", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, vals := range propagated {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call user service: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var out CreateUserResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode user response: %w", err)
		}
		return &out, nil
	case http.StatusConflict:
		return nil, ErrUserConflict
	default:
		return nil, fmt.Errorf("user service returned status %d", resp.StatusCode)
	}
}
