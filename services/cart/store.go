package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store wraps the Redis client and all cart data access. A cart lives as a single
// JSON document at key cart:{user_id} — simple, atomic to read/write, and a
// natural fit for Redis.
type Store struct {
	rdb *redis.Client
}

// NewStore connects to Redis, retrying because in Kubernetes the Redis pod may
// not be ready at the moment this service starts.
func NewStore(ctx context.Context, cfg Config) (*Store, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = rdb.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			slog.Info("connected to redis")
			return &Store{rdb: rdb}, nil
		}
		slog.Warn("redis not ready, retrying", "attempt", attempt, "error", err)
		time.Sleep(3 * time.Second)
	}
	_ = rdb.Close()
	return nil, fmt.Errorf("could not connect to redis after retries: %w", err)
}

// Close releases the Redis client.
func (s *Store) Close() { _ = s.rdb.Close() }

// Ping checks Redis connectivity; used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }

// cartKey is the Redis key holding a user's cart JSON.
func cartKey(userID string) string { return "cart:" + userID }

// Get loads a user's cart. A missing key is not an error — it yields an empty
// cart, because "no cart yet" and "empty cart" are the same thing to a client.
func (s *Store) Get(ctx context.Context, userID string) (Cart, error) {
	raw, err := s.rdb.Get(ctx, cartKey(userID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Cart{UserID: userID, Items: []CartItem{}}, nil
	}
	if err != nil {
		return Cart{}, err
	}

	var cart Cart
	if err := json.Unmarshal(raw, &cart); err != nil {
		return Cart{}, fmt.Errorf("decode cart: %w", err)
	}
	cart.UserID = userID
	cart.recompute()
	return cart, nil
}

// Save persists the cart JSON, recomputing the total first so it can never drift
// from the line items.
func (s *Store) Save(ctx context.Context, cart *Cart) error {
	cart.recompute()
	body, err := json.Marshal(cart)
	if err != nil {
		return fmt.Errorf("encode cart: %w", err)
	}
	return s.rdb.Set(ctx, cartKey(cart.UserID), body, 0).Err()
}

// Delete clears a user's cart. Deleting an absent cart is a no-op success.
func (s *Store) Delete(ctx context.Context, userID string) error {
	return s.rdb.Del(ctx, cartKey(userID)).Err()
}
