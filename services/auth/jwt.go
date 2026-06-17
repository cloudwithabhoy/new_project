package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Signer mints RS256 JWTs and serves the matching public key as a JWKS document.
//
// Why RS256 (asymmetric) and not HS256 (shared secret)? Because in the mesh,
// Istio's RequestAuthentication validates tokens by fetching this service's
// JWKS — it must verify with a PUBLIC key without ever holding the signing
// secret. That is the enterprise pattern: auth signs, everyone else verifies.
type Signer struct {
	priv     *rsa.PrivateKey
	kid      string
	issuer   string
	audience string
	ttl      time.Duration
}

// NewSigner loads the RSA private key from config, or generates an EPHEMERAL one
// if none is supplied. An ephemeral key is fine for local dev but unacceptable in
// Kubernetes: it changes on every pod restart, invalidating live tokens and the
// published JWKS. Always supply JWT_PRIVATE_KEY_PEM via a Secret in-cluster.
func NewSigner(cfg Config) (*Signer, error) {
	var priv *rsa.PrivateKey
	var err error

	if cfg.JWTPrivateKeyPEM != "" {
		priv, err = parseRSAPrivateKey(cfg.JWTPrivateKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse JWT_PRIVATE_KEY_PEM: %w", err)
		}
	} else {
		slog.Warn("JWT_PRIVATE_KEY_PEM not set — generating an EPHEMERAL signing key; " +
			"tokens will not survive a restart. Set a stable key via Secret in Kubernetes.")
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
	}

	return &Signer{
		priv:     priv,
		kid:      jwkThumbprint(&priv.PublicKey),
		issuer:   cfg.JWTIssuer,
		audience: cfg.JWTAudience,
		ttl:      cfg.JWTTTL,
	}, nil
}

// Issue mints a signed access token for the given user.
func (s *Signer) Issue(userID int64, email string) (token string, expiresAt time.Time, err error) {
	now := time.Now()
	expiresAt = now.Add(s.ttl)
	claims := jwt.MapClaims{
		"iss":   s.issuer,
		"aud":   s.audience,
		"sub":   strconv.FormatInt(userID, 10),
		"email": email,
		"iat":   now.Unix(),
		"exp":   expiresAt.Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = s.kid
	token, err = t.SignedString(s.priv)
	return token, expiresAt, err
}

// Validate verifies a token's signature and standard claims, returning its claims.
func (s *Signer) Validate(tokenStr string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return &s.priv.PublicKey, nil
	},
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithValidMethods([]string{"RS256"}),
	)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// JWKS returns the public JSON Web Key Set served at /.well-known/jwks.json.
// Istio (and any verifier) fetches this to validate tokens.
func (s *Signer) JWKS() map[string]any {
	pub := s.priv.PublicKey
	return map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": s.kid,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
}

// parseRSAPrivateKey accepts a PEM-encoded key in either PKCS#1 or PKCS#8 form.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA private key")
	}
	return key, nil
}

// jwkThumbprint computes the RFC 7638 thumbprint of the RSA public key, used as
// the stable key id (kid). The member ordering below is mandated by the RFC.
func jwkThumbprint(pub *rsa.PublicKey) string {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	canonical, _ := json.Marshal(map[string]string{"e": e, "kty": "RSA", "n": n})
	sum := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
