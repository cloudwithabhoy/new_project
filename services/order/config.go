package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Config holds all runtime configuration for the order service.
// Every value comes from an environment variable so that in Kubernetes the
// non-secret values can be injected via a ConfigMap and the secret values
// (DB_PASSWORD) via a Secret.
type Config struct {
	Port       string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	LogLevel   string

	// Downstream service base URL. In the trimmed build order calls only
	// inventory (reserve stock during checkout); injected via ConfigMap so
	// Kubernetes/Istio owns the wiring, no address hardcoded.
	InventoryURL string

	// Kafka producer configuration. order emits `order.created` after persisting.
	KafkaBrokers []string
	KafkaTopic   string
}

// LoadConfig reads configuration from the environment, applying sensible
// local-development defaults when a variable is not set.
func LoadConfig() Config {
	return Config{
		Port:         getenv("PORT", "8086"),
		DBHost:       getenv("DB_HOST", "localhost"),
		DBPort:       getenv("DB_PORT", "5432"),
		DBUser:       getenv("DB_USER", "order"),
		DBPassword:   getenv("DB_PASSWORD", "order"),
		DBName:       getenv("DB_NAME", "order"),
		DBSSLMode:    getenv("DB_SSLMODE", "disable"),
		LogLevel:     getenv("LOG_LEVEL", "info"),
		InventoryURL: getenv("INVENTORY_URL", "http://localhost:8088"),
		KafkaBrokers: splitAndTrim(getenv("KAFKA_BROKERS", "localhost:9092")),
		KafkaTopic:   getenv("KAFKA_TOPIC", "order.created"),
	}
}

// DSN builds the Postgres connection string from the individual config values.
func (c Config) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName, c.DBSSLMode)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitAndTrim turns a comma-separated env value (e.g. "b1:9092,b2:9092") into a
// clean slice of broker addresses for the Kafka writer.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
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
