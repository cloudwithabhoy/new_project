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

// ErrNotFound is returned when a requested product does not exist.
var ErrNotFound = errors.New("product not found")

// Store wraps the Postgres connection pool and all catalog data access.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore connects to Postgres, retrying because in Kubernetes the database
// pod may not be ready at the moment this service starts. This retry loop is a
// common, important pattern for services that depend on a database.
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

// Migrate creates the products table if it does not exist. For a learning
// project this inline migration is fine; a real service would use a migration
// tool (golang-migrate, goose, etc.).
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS products (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    price_cents BIGINT NOT NULL DEFAULT 0,
    stock       INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	return err
}

const productColumns = `id, name, description, price_cents, stock, created_at, updated_at`

// ListProducts returns a page of products ordered by id.
func (s *Store) ListProducts(ctx context.Context, limit, offset int) ([]Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+productColumns+` FROM products ORDER BY id LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]Product, 0)
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

// GetProduct returns a single product or ErrNotFound.
func (s *Store) GetProduct(ctx context.Context, id int64) (Product, error) {
	var p Product
	err := s.pool.QueryRow(ctx,
		`SELECT `+productColumns+` FROM products WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	return p, err
}

// CreateProduct inserts a new product and returns the stored row.
func (s *Store) CreateProduct(ctx context.Context, in ProductInput) (Product, error) {
	var p Product
	err := s.pool.QueryRow(ctx,
		`INSERT INTO products (name, description, price_cents, stock)
         VALUES ($1, $2, $3, $4)
         RETURNING `+productColumns,
		in.Name, in.Description, in.PriceCents, in.Stock).
		Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// UpdateProduct overwrites an existing product or returns ErrNotFound.
func (s *Store) UpdateProduct(ctx context.Context, id int64, in ProductInput) (Product, error) {
	var p Product
	err := s.pool.QueryRow(ctx,
		`UPDATE products
         SET name = $1, description = $2, price_cents = $3, stock = $4, updated_at = now()
         WHERE id = $5
         RETURNING `+productColumns,
		in.Name, in.Description, in.PriceCents, in.Stock, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.Stock, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	return p, err
}

// DeleteProduct removes a product or returns ErrNotFound.
func (s *Store) DeleteProduct(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
