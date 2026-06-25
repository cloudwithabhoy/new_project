package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no credentials exist for a given email.
var ErrNotFound = errors.New("credentials not found")

// ErrConflict is returned when an email is already registered (unique violation).
var ErrConflict = errors.New("email already registered")

// Credential is a stored login identity. The user's profile (name, etc.) lives
// in the user service; auth only owns the secret material and the link to the
// profile via user_id.
type Credential struct {
	UserID       int64
	Email        string
	PasswordHash string
}

// Store wraps the Postgres connection pool and all auth data access.
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

// Migrate creates the credentials table if it does not exist. In the trimmed
// build there is no separate user service, so auth mints the user_id itself
// (BIGSERIAL) instead of receiving it from the user service.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS credentials (
    user_id       BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	return err
}

// CreateCredential stores a new login identity, generating the user_id, and
// returns it. A duplicate email surfaces as ErrConflict so the handler can
// return 409.
func (s *Store) CreateCredential(ctx context.Context, email, passwordHash string) (int64, error) {
	var userID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO credentials (email, password_hash) VALUES ($1, $2) RETURNING user_id`,
		email, passwordHash).Scan(&userID)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return 0, ErrConflict
	}
	return userID, err
}

// GetByEmail looks up the stored credential for a login attempt.
func (s *Store) GetByEmail(ctx context.Context, email string) (Credential, error) {
	var c Credential
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, email, password_hash FROM credentials WHERE email = $1`, email).
		Scan(&c.UserID, &c.Email, &c.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return c, err
}
