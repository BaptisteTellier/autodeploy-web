// Package auth provides the pure primitives behind autodeploy-web's optional
// single-admin authentication: password hashing, a stateless signed session
// token, and a session-derived CSRF token. All functions are I/O-free and
// deterministic given their inputs, so they are straightforward to unit-test;
// persistence, HTTP wiring and policy live in the server package.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SessionMaxAge is the absolute lifetime of a session. There is deliberately NO
// idle/sliding timeout — once signed in the operator stays signed in until this
// hard limit, logout, or a secret rotation (e.g. password change).
const SessionMaxAge = 30 * 24 * time.Hour

// bcryptCost is a sensible default work factor for a local admin credential.
const bcryptCost = 12

var b64 = base64.RawURLEncoding

// HashPassword returns a bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword reports whether pw matches the bcrypt hash (constant-time).
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// GenerateSecret returns 32 bytes of cryptographically-random data, suitable as
// the HMAC signing secret for sessions/CSRF. Base64 (raw-url) encoded for JSON.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// hmacHex computes base64url(HMAC-SHA256(secret, msg)).
func mac(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return b64.EncodeToString(m.Sum(nil))
}

// NewSession mints a stateless session token of the form "<issuedUnix>.<mac>".
// The token carries no user data (single admin) — only proof that the server
// (holder of secret) issued it at a given time.
func NewSession(secret []byte, now time.Time) string {
	payload := strconv.FormatInt(now.Unix(), 10)
	return payload + "." + mac(secret, payload)
}

// ValidSession verifies a session token's signature and that it is within
// SessionMaxAge of now. A rotated secret invalidates all outstanding tokens.
func ValidSession(secret []byte, token string, now time.Time) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || payload == "" || sig == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(mac(secret, payload))) != 1 {
		return false
	}
	issued, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(issued, 0))
	// Reject the future (allow a little clock skew) and anything past the max age.
	return age >= -5*time.Minute && age <= SessionMaxAge
}

// CSRFToken derives a per-session CSRF token from the session token. It is
// unguessable without the secret and stable for the life of the session, so it
// can be embedded in pages and required (double-submit) on state-changing
// requests. Returns "" for an empty session.
func CSRFToken(secret []byte, session string) string {
	if session == "" {
		return ""
	}
	return mac(secret, "csrf|"+session)
}

// ValidCSRF constant-time compares a presented CSRF token against the one
// derived from the request's session token.
func ValidCSRF(secret []byte, session, token string) bool {
	if session == "" || token == "" {
		return false
	}
	want := CSRFToken(secret, session)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

// ErrWeakPassword is returned by ValidatePassword for passwords that are too short.
var ErrWeakPassword = errors.New("password must be at least 8 characters")

// ValidatePassword enforces a minimal strength policy for the admin password.
func ValidatePassword(pw string) error {
	if len(pw) < 8 {
		return ErrWeakPassword
	}
	return nil
}
