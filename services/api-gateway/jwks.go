package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This is the REVERSE of auth's jwt.go: auth SIGNS RS256 tokens with its private
// key and publishes the matching public key as a JWKS document; the gateway
// FETCHES that JWKS, reconstructs the RSA public key from the JWK (n, e) members,
// and VERIFIES tokens with it. The gateway never holds a signing secret — the
// enterprise pattern is "auth signs, everyone else verifies".
//
// In later phases Istio's RequestAuthentication does exactly this fetch+verify at
// the sidecar, and this app-level Verifier is retired (see README DevOps handoff).

// jwksRefreshInterval bounds how stale a cached key set may get before a
// proactive background refresh. Verification also lazily refreshes on a cache
// miss (an unknown kid), which covers key rotation between scheduled refreshes.
const (
	jwksRefreshInterval = 5 * time.Minute
	jwksFetchTimeout    = 5 * time.Second
	jwksMinRefreshGap   = 30 * time.Second // rate-limit lazy refreshes on miss
)

// jwk is a single RSA public key from the JWKS document (RFC 7517).
type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

// Verifier validates RS256 JWTs against auth's JWKS, which it caches in memory
// and refreshes periodically (and lazily on an unknown kid, to absorb rotation).
type Verifier struct {
	jwksURL  string
	issuer   string
	audience string
	http     *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey // kid -> public key
	loaded    bool                      // true once we have fetched at least once
	lastFetch time.Time
	lastErr   error
}

// NewVerifier builds a verifier that fetches the JWKS from {authURL}.
func NewVerifier(authURL, issuer, audience string) *Verifier {
	return &Verifier{
		jwksURL:  authURL + "/.well-known/jwks.json",
		issuer:   issuer,
		audience: audience,
		http:     &http.Client{Timeout: jwksFetchTimeout},
		keys:     map[string]*rsa.PublicKey{},
	}
}

// Start kicks off a background loop that periodically refreshes the JWKS so the
// cache stays warm even without traffic, and does an initial best-effort fetch.
// A failed initial fetch is non-fatal: readiness will report not-ready until the
// keys load, and verification lazily retries — auth's pod may simply be starting.
func (v *Verifier) Start(ctx context.Context) {
	if err := v.refresh(ctx); err != nil {
		slog.Warn("initial JWKS fetch failed; will retry in background", "error", err, "url", v.jwksURL)
	}
	go func() {
		ticker := time.NewTicker(jwksRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := v.refresh(ctx); err != nil {
					slog.Warn("JWKS refresh failed", "error", err, "url", v.jwksURL)
				}
			}
		}
	}()
}

// refresh fetches the JWKS and atomically swaps the cached key set on success.
func (v *Verifier) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, jwksFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		v.setErr(err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
		v.setErr(err)
		return err
	}

	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		v.setErr(err)
		return err
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := k.toRSAPublicKey()
		if err != nil {
			slog.Warn("skipping malformed JWK", "kid", k.Kid, "error", err)
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		err := errors.New("JWKS contained no usable RSA keys")
		v.setErr(err)
		return err
	}

	v.mu.Lock()
	v.keys = keys
	v.loaded = true
	v.lastFetch = time.Now()
	v.lastErr = nil
	v.mu.Unlock()

	slog.Info("JWKS refreshed", "keys", len(keys))
	return nil
}

func (v *Verifier) setErr(err error) {
	v.mu.Lock()
	v.lastErr = err
	v.mu.Unlock()
}

// Ready reports whether at least one key set has been successfully loaded. The
// readiness probe gates on this so the gateway doesn't accept traffic it can't
// authenticate.
func (v *Verifier) Ready() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.loaded
}

// keyByKid returns the cached public key for a kid, if present.
func (v *Verifier) keyByKid(kid string) (*rsa.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	k, ok := v.keys[kid]
	return k, ok
}

// Verify validates the token's RS256 signature and standard claims (iss, aud,
// exp) and returns the subject ("sub", = user id). The signing key is selected
// by the token's "kid" header against the cached JWKS; an unknown kid triggers a
// rate-limited lazy refresh to absorb key rotation between scheduled refreshes.
func (v *Verifier) Verify(ctx context.Context, tokenStr string) (sub string, err error) {
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		if key, ok := v.keyByKid(kid); ok {
			return key, nil
		}
		// Unknown kid: auth may have rotated its key. Lazily refresh (rate-limited
		// so a flood of bad-kid tokens can't hammer auth) and try once more.
		if v.shouldLazyRefresh() {
			if rerr := v.refresh(ctx); rerr != nil {
				return nil, fmt.Errorf("refresh JWKS for kid %q: %w", kid, rerr)
			}
		}
		if key, ok := v.keyByKid(kid); ok {
			return key, nil
		}
		return nil, fmt.Errorf("no JWKS key for kid %q", kid)
	},
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}

	sub, err = claims.GetSubject()
	if err != nil || sub == "" {
		return "", errors.New("token missing subject")
	}
	return sub, nil
}

// shouldLazyRefresh rate-limits on-miss refreshes so unknown-kid tokens can't be
// used to hammer auth's JWKS endpoint.
func (v *Verifier) shouldLazyRefresh() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return time.Since(v.lastFetch) > jwksMinRefreshGap
}

// toRSAPublicKey reconstructs an *rsa.PublicKey from a JWK's base64url-encoded
// modulus (n) and exponent (e) — the inverse of how auth encodes its public key
// in jwt.go's JWKS().
func (k jwk) toRSAPublicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() <= 0 {
		return nil, errors.New("invalid exponent")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}
