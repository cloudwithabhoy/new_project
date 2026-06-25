package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg := LoadConfig()
	slog.SetDefault(newLogger(cfg.LogLevel))

	ctx := context.Background()

	store, err := NewStore(ctx, cfg)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// The signer holds the RSA key used to mint JWTs and serve JWKS. A stable key
	// (from JWT_PRIVATE_KEY_PEM) is required in Kubernetes so tokens survive pod
	// restarts and Istio can validate them against the published JWKS.
	signer, err := NewSigner(cfg)
	if err != nil {
		slog.Error("failed to initialize JWT signer", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      NewRouter(store, signer),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine so main can wait for shutdown signals.
	go func() {
		slog.Info("auth service starting", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: on SIGTERM (what Kubernetes sends during a rollout or node
	// drain) stop accepting new requests and let in-flight ones finish. This is what
	// makes PodDisruptionBudgets and rolling updates clean.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutdown signal received, draining connections")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("auth service stopped")
}
