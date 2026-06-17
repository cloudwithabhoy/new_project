package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no payment exists for a given id.
var ErrNotFound = errors.New("payment not found")

// Store wraps the Postgres connection pool and all payment data access.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore connects to Postgres, retrying because in Kubernetes the database
// pod may not be ready at the moment this service starts.
func NewStore(ctx context.Context, cfg Config) (*Store, error) {
	var pool *pgxpool.Pool
	var err error

	for attempt := 1; attempt <= 10; attempt++ {
		pool, err = pgxpool.New(ctx, cfg.DSN())
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				slog.Info("connected to database")
				return &Store{pool: pool}, nil
			}
			pool.Close()
		}
		slog.Warn("database not ready, retrying", "attempt", attempt, "error", err)
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("could not connect to database after retries: %w", err)
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database connectivity; used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate creates the payments table if it does not exist.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS payments (
    id           BIGSERIAL PRIMARY KEY,
    order_id     BIGINT NOT NULL,
    user_id      BIGINT NOT NULL,
    amount_cents BIGINT NOT NULL,
    status       TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	return err
}

// CreatePayment persists an authorization decision and returns the stored row
// (with its generated id and created_at). Every payment is recorded — including
// declines — so the demo has an audit trail of every authorizer outcome.
func (s *Store) CreatePayment(ctx context.Context, orderID, userID, amountCents int64, status string) (Payment, error) {
	var p Payment
	err := s.pool.QueryRow(ctx,
		`INSERT INTO payments (order_id, user_id, amount_cents, status)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, order_id, user_id, amount_cents, status, created_at`,
		orderID, userID, amountCents, status).
		Scan(&p.ID, &p.OrderID, &p.UserID, &p.AmountCents, &p.Status, &p.CreatedAt)
	return p, err
}

// GetByID fetches a single payment, surfacing ErrNotFound so the handler can
// return 404.
func (s *Store) GetByID(ctx context.Context, id int64) (Payment, error) {
	var p Payment
	err := s.pool.QueryRow(ctx,
		`SELECT id, order_id, user_id, amount_cents, status, created_at
		 FROM payments WHERE id = $1`, id).
		Scan(&p.ID, &p.OrderID, &p.UserID, &p.AmountCents, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrNotFound
	}
	return p, err
}
