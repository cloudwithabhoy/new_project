package main

import (
	"errors"
	"strings"
)

// RegisterInput is the client payload for POST /register.
type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
}

// LoginInput is the client payload for POST /login.
type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// minPasswordLen is a deliberately modest floor for a learning project.
const minPasswordLen = 8

// Validate enforces basic registration rules before anything touches the
// database or the downstream user service.
func (in RegisterInput) Validate() error {
	if !looksLikeEmail(in.Email) {
		return errors.New("a valid email is required")
	}
	if len(in.Password) < minPasswordLen {
		return errors.New("password must be at least 8 characters")
	}
	if strings.TrimSpace(in.FullName) == "" {
		return errors.New("full_name is required")
	}
	return nil
}

// Validate enforces basic login rules.
func (in LoginInput) Validate() error {
	if !looksLikeEmail(in.Email) {
		return errors.New("a valid email is required")
	}
	if in.Password == "" {
		return errors.New("password is required")
	}
	return nil
}

// looksLikeEmail is a cheap sanity check — not full RFC validation. The user
// service does stricter validation; auth only needs to reject obvious garbage.
func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " ")
}
