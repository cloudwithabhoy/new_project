package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the auth service.
// Every value comes from an environment variable so that in Kubernetes the
// non-secret values can be injected via a ConfigMap and the secret values
// (DB_PASSWORD, JWT_PRIVATE_KEY_PEM) via a Secret.
type Config struct {
	Port       string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	LogLevel   string

	// UserServiceURL is the base URL of the user service (e.g. http://user:8082).
	// auth calls it to create the user profile during registration. No address is
	// hardcoded — you inject this via ConfigMap so Kubernetes/Istio owns the wiring.
	UserServiceURL string

	// JWT signing configuration.
	JWTPrivateKeyPEM string        // RSA private key (PEM). If empty, an EPHEMERAL key is generated on startup.
	JWTIssuer        string        // "iss" claim, e.g. "auth.ecommerce.local"
	JWTAudience      string        // "aud" claim, e.g. "ecommerce"
	JWTTTL           time.Duration // access-token lifetime
}

// LoadConfig reads configuration from the environment, applying sensible
// local-development defaults when a variable is not set.
func LoadConfig() Config {
	return Config{
		Port:           getenv("PORT", "8081"),
		DBHost:         getenv("DB_HOST", "localhost"),
		DBPort:         getenv("DB_PORT", "5432"),
		DBUser:         getenv("DB_USER", "auth"),
		DBPassword:     getenv("DB_PASSWORD", "auth"),
		DBName:         getenv("DB_NAME", "auth"),
		DBSSLMode:      getenv("DB_SSLMODE", "disable"),
		LogLevel:       getenv("LOG_LEVEL", "info"),
		UserServiceURL: getenv("USER_URL", "http://localhost:8082"),
		JWTPrivateKeyPEM: os.Getenv("JWT_PRIVATE_KEY_PEM"),
		JWTIssuer:        getenv("JWT_ISSUER", "auth.ecommerce.local"),
		JWTAudience:      getenv("JWT_AUDIENCE", "ecommerce"),
		JWTTTL:           time.Duration(getenvInt("JWT_TTL_MINUTES", 60)) * time.Minute,
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
