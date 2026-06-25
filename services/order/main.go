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

	// Downstream clients for the orchestration fan-out. No addresses hardcoded —
	// the URL comes from ConfigMap so Istio/Kubernetes owns the wiring.
	inventory := NewInventoryClient(cfg.InventoryURL)

	// Kafka producer for order.created. The writer dials lazily, so this never
	// blocks startup if the broker pod isn't ready yet.
	producer := NewProducer(cfg.KafkaBrokers, cfg.KafkaTopic)
	defer func() {
		if err := producer.Close(); err != nil {
			slog.Error("kafka writer close failed", "error", err)
		}
	}()

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      NewRouter(store, inventory, producer),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second, // orchestration fan-out can take longer than a simple read
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine so main can wait for shutdown signals.
	go func() {
		slog.Info("order service starting",
			"port", cfg.Port,
			"inventory_url", cfg.InventoryURL,
			"kafka_topic", cfg.KafkaTopic,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: on SIGTERM (what Kubernetes sends during a rollout or
	// node drain) stop accepting new requests, let in-flight checkouts finish,
	// then close the Kafka writer (deferred) and the DB pool (deferred).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutdown signal received, draining connections")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("order service stopped")
}
