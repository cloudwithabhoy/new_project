package main

import (
	"log/slog"
	"os"
	"strconv"
)

// Config holds all runtime configuration for the cart service.
// Every value comes from an environment variable so that in Kubernetes the
// non-secret values can be injected via a ConfigMap and the secret values
// (REDIS_PASSWORD) via a Secret.
type Config struct {
	Port     string
	LogLevel string

	// Redis connection. The cart's only datastore — each cart is a single JSON
	// blob at key cart:{user_id}.
	RedisAddr     string
	RedisPassword string // SECRET — may be empty for local dev (no AUTH).
	RedisDB       int

	// CatalogServiceURL is the base URL of the catalog service (e.g. http://catalog:8083).
	// cart calls it to validate a product and snapshot its name + price into the
	// cart line. No address is hardcoded — you inject this via ConfigMap so
	// Kubernetes/Istio owns the wiring.
	CatalogServiceURL string
}

// LoadConfig reads configuration from the environment, applying sensible
// local-development defaults when a variable is not set.
func LoadConfig() Config {
	return Config{
		Port:              getenv("PORT", "8085"),
		LogLevel:          getenv("LOG_LEVEL", "info"),
		RedisAddr:         getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     os.Getenv("REDIS_PASSWORD"),
		RedisDB:           getenvInt("REDIS_DB", 0),
		CatalogServiceURL: getenv("CATALOG_URL", "http://localhost:8083"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// newLogger returns a structured JSON logger at the requested level.
// JSON logs are what you want in Kubernetes so Loki/aggregators can parse them.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
