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
		slog.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	catalog := NewCatalogClient(cfg.CatalogServiceURL)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      NewRouter(store, catalog),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine so main can wait for shutdown signals.
	go func() {
		slog.Info("cart service starting", "port", cfg.Port, "catalog_url", cfg.CatalogServiceURL)
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
	slog.Info("cart service stopped")
}
