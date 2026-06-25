package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/bcrypt"
)

// dbTimeout bounds every database-backed request.
const dbTimeout = 5 * time.Second

// Handler holds dependencies shared by all HTTP handlers.
type Handler struct {
	store  *Store
	signer *Signer
}

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(store *Store, signer *Signer) http.Handler {
	h := &Handler{store: store, signer: signer}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /register", withRoute("/register", h.register))
	mux.HandleFunc("POST /login", withRoute("/login", h.login))
	mux.HandleFunc("GET /validate", withRoute("/validate", h.validate))
	mux.HandleFunc("GET /.well-known/jwks.json", withRoute("/jwks", h.jwks))

	// Operational endpoints
	mux.HandleFunc("GET /healthz", withRoute("/healthz", h.healthz)) // liveness
	mux.HandleFunc("GET /readyz", withRoute("/readyz", h.readyz))    // readiness
	mux.Handle("GET /metrics", promhttp.Handler())

	// Middleware chain: metrics wraps logging wraps the mux.
	return metricsMiddleware(loggingMiddleware(mux))
}

// register creates a login identity. In the trimmed build there is no separate
// user service, so auth stores the credential and mints the user_id itself
// (the DB BIGSERIAL), returning it to the caller.
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	in, ok := decodeRegister(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	if _, err := h.store.GetByEmail(ctx, in.Email); err == nil {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		log.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to register")
		return
	}

	userID, err := h.store.CreateCredential(ctx, in.Email, string(hash))
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		log.Error("create credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to register")
		return
	}

	log.Info("user registered", "user_id", userID)
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": userID, "email": in.Email})
}

// login verifies the password and issues a signed JWT.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	log := loggerFrom(r.Context())
	in, ok := decodeLogin(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()

	cred, err := h.store.GetByEmail(ctx, in.Email)
	if err != nil {
		// Return the SAME response for "no such user" and "wrong password" so we
		// don't leak which emails are registered (user enumeration defense).
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(in.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, expiresAt, err := h.signer.Issue(cred.UserID, cred.Email)
	if err != nil {
		log.Error("issue token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	log.Info("user logged in", "user_id", cred.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(expiresAt).Seconds()),
	})
}

// validate verifies a bearer token and returns its claims. Handy for the
// api-gateway before mesh-level RequestAuthentication is in place.
func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	claims, err := h.signer.Validate(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "claims": claims})
}

// jwks publishes the public verification key(s). Istio fetches this URL.
func (h *Handler) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.signer.JWKS())
}

// healthz is the liveness probe: the process is running. No dependencies checked.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: only ready if the database is reachable.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

func decodeRegister(w http.ResponseWriter, r *http.Request) (RegisterInput, bool) {
	var in RegisterInput
	if !decodeJSON(w, r, &in) {
		return RegisterInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return RegisterInput{}, false
	}
	return in, true
}

func decodeLogin(w http.ResponseWriter, r *http.Request) (LoginInput, bool) {
	var in LoginInput
	if !decodeJSON(w, r, &in) {
		return LoginInput{}, false
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return LoginInput{}, false
	}
	return in, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
