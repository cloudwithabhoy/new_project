package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when an order id does not exist.
var ErrNotFound = errors.New("order not found")

// Store wraps the Postgres connection pool and all order data access.
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

// Migrate creates the orders table and the id sequence if they do not exist.
//
// orders.id defaults from orders_id_seq, which we ALSO use directly via
// NextID() to pre-allocate the order id BEFORE we charge payment / reserve
// inventory (see the order_id ordering note below).
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE SEQUENCE IF NOT EXISTS orders_id_seq;

CREATE TABLE IF NOT EXISTS orders (
    id          BIGINT PRIMARY KEY DEFAULT nextval('orders_id_seq'),
    user_id     BIGINT NOT NULL,
    items       JSONB  NOT NULL,
    total_cents BIGINT NOT NULL,
    status      TEXT   NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS orders_user_id_idx ON orders (user_id);`)
	return err
}

// NextID reserves the next order id from the Postgres sequence.
//
// order_id ordering vs payment (a deliberate design point): the contract has us
// charge payment and reserve inventory with the SAME order_id we persist, and
// the payment call happens BEFORE the row exists. Rather than insert a throwaway
// "pending" row and update it later, we pull the id straight from the sequence
// up front with `nextval`. The id is then threaded through payment + inventory +
// the final INSERT (with id explicit) + the Kafka key, so every hop in the saga
// agrees on the order id. Gaps in the sequence (from a declined payment that
// never inserts) are expected and harmless — sequences are not gap-free anyway.
func (s *Store) NextID(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT nextval('orders_id_seq')`).Scan(&id)
	return id, err
}

// Insert persists a fully-orchestrated order using the pre-allocated id. By this
// point payment is approved and inventory is reserved, so the order is durable
// and "confirmed".
func (s *Store) Insert(ctx context.Context, o Order) error {
	items, err := json.Marshal(o.Items)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO orders (id, user_id, items, total_cents, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		o.ID, o.UserID, items, o.TotalCents, o.Status, o.CreatedAt)
	return err
}

// Get returns a single order by id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (Order, error) {
	var o Order
	var items []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, items, total_cents, status, created_at FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.UserID, &items, &o.TotalCents, &o.Status, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	if err := json.Unmarshal(items, &o.Items); err != nil {
		return Order{}, err
	}
	return o, nil
}

// ListByUser returns all orders for a user, newest first.
func (s *Store) ListByUser(ctx context.Context, userID int64) ([]Order, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, items, total_cents, status, created_at
		 FROM orders WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		var items []byte
		if err := rows.Scan(&o.ID, &o.UserID, &items, &o.TotalCents, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(items, &o.Items); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}
