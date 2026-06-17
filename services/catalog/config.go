package main

import (
	"fmt"
	"log/slog"
	"os"
)

// Config holds all runtime configuration for the catalog service.
// Every value comes from an environment variable so that in Kubernetes the
// non-secret values can be injected via a ConfigMap and DB_PASSWORD via a Secret.
type Config struct {
	Port       string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	LogLevel   string
}

// LoadConfig reads configuration from the environment, applying sensible
// local-development defaults when a variable is not set.
func LoadConfig() Config {
	return Config{
		Port:       getenv("PORT", "8083"),
		DBHost:     getenv("DB_HOST", "localhost"),
		DBPort:     getenv("DB_PORT", "5432"),
		DBUser:     getenv("DB_USER", "catalog"),
		DBPassword: getenv("DB_PASSWORD", "catalog"),
		DBName:     getenv("DB_NAME", "catalog"),
		DBSSLMode:  getenv("DB_SSLMODE", "disable"),
		LogLevel:   getenv("LOG_LEVEL", "info"),
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

// newLogger returns a structured JSON logger at the requested level.
// JSON logs are what you want in Kubernetes so log aggregators can parse them.
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
