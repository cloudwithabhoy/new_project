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

	// Root context cancelled on shutdown — stops the JWKS background refresher.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The verifier validates RS256 tokens against auth's PUBLIC JWKS. It fetches
	// the key set once now (best-effort) and keeps it warm with a background
	// refresh; readiness gates on it having loaded at least once.
	verifier := NewVerifier(cfg.AuthURL, cfg.JWTIssuer, cfg.JWTAudience)
	verifier.Start(ctx)

	router, err := NewRouter(cfg, verifier)
	if err != nil {
		slog.Error("failed to build router", "error", err)
		os.Exit(1)
	}
	router.logRoutes()

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: NewHandler(router, verifier),
		// No WriteTimeout: as a reverse proxy we don't want to truncate slow
		// upstream responses. ReadHeaderTimeout still guards against slowloris.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Run the server in a goroutine so main can wait for shutdown signals.
	go func() {
		slog.Info("api-gateway starting", "port", cfg.Port, "auth_url", cfg.AuthURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: on SIGTERM (what Kubernetes sends during a rollout or
	// node drain) stop accepting new requests and let in-flight ones finish. This
	// is what makes PodDisruptionBudgets and rolling updates clean.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutdown signal received, draining connections")
	cancel() // stop the JWKS refresher
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("api-gateway stopped")
}
