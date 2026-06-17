package main

import (
	"log/slog"
	"os"
)

// Config holds all runtime configuration for the api-gateway service.
// Every value comes from an environment variable so that in Kubernetes the
// non-secret values can be injected via a ConfigMap. The gateway has NO secrets
// of its own — it verifies tokens against auth's PUBLIC JWKS, never a private key.
type Config struct {
	Port     string
	LogLevel string

	// Upstream base URLs. The gateway reverse-proxies to these by path prefix.
	// No address is hardcoded — Kubernetes/Istio owns the wiring via ConfigMap.
	AuthURL           string
	UserURL           string
	CatalogURL        string
	SearchURL         string
	CartURL           string
	OrderURL          string
	RecommendationURL string

	// JWT verification configuration. The gateway validates RS256 tokens minted
	// by auth, checking these claims. JWKS is fetched from {AuthURL}.
	JWTIssuer   string // expected "iss" claim, e.g. "auth.ecommerce.local"
	JWTAudience string // expected "aud" claim, e.g. "ecommerce"
}

// LoadConfig reads configuration from the environment, applying sensible
// local-development defaults when a variable is not set.
func LoadConfig() Config {
	return Config{
		Port:              getenv("PORT", "8080"),
		LogLevel:          getenv("LOG_LEVEL", "info"),
		AuthURL:           getenv("AUTH_URL", "http://localhost:8081"),
		UserURL:           getenv("USER_URL", "http://localhost:8082"),
		CatalogURL:        getenv("CATALOG_URL", "http://localhost:8083"),
		SearchURL:         getenv("SEARCH_URL", "http://localhost:8084"),
		CartURL:           getenv("CART_URL", "http://localhost:8085"),
		OrderURL:          getenv("ORDER_URL", "http://localhost:8086"),
		RecommendationURL: getenv("RECOMMENDATION_URL", "http://localhost:8090"),
		JWTIssuer:         getenv("JWT_ISSUER", "auth.ecommerce.local"),
		JWTAudience:       getenv("JWT_AUDIENCE", "ecommerce"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
