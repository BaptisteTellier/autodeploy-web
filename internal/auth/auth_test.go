package auth

import (
	"encoding/base64"
	"testing"
	"time"
)

func mustSecret(t *testing.T) []byte {
	t.Helper()
	s, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return b
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") {
		t.Error("correct password rejected")
	}
	if CheckPassword(h, "wrong") {
		t.Error("wrong password accepted")
	}
}

func TestSession(t *testing.T) {
	sec := mustSecret(t)
	now := time.Now()
	tok := NewSession(sec, now)

	if !ValidSession(sec, tok, now) {
		t.Error("fresh session rejected")
	}
	if !ValidSession(sec, tok, now.Add(29*24*time.Hour)) {
		t.Error("session within max age rejected")
	}
	if ValidSession(sec, tok, now.Add(SessionMaxAge+time.Hour)) {
		t.Error("expired session accepted")
	}
	// Tampered signature.
	if ValidSession(sec, tok+"x", now) {
		t.Error("tampered token accepted")
	}
	// Different secret (simulates rotation on password change).
	if ValidSession(mustSecret(t), tok, now) {
		t.Error("token validated under a different secret")
	}
	// Issued in the future beyond skew.
	future := NewSession(sec, now.Add(time.Hour))
	if ValidSession(sec, future, now) {
		t.Error("future-dated token accepted")
	}
	// Garbage.
	if ValidSession(sec, "nope", now) || ValidSession(sec, "", now) {
		t.Error("malformed token accepted")
	}
}

func TestCSRF(t *testing.T) {
	sec := mustSecret(t)
	session := NewSession(sec, time.Now())
	tok := CSRFToken(sec, session)
	if tok == "" {
		t.Fatal("empty CSRF token for valid session")
	}
	if !ValidCSRF(sec, session, tok) {
		t.Error("valid CSRF token rejected")
	}
	if ValidCSRF(sec, session, "bad") {
		t.Error("bad CSRF token accepted")
	}
	if ValidCSRF(sec, "", tok) || ValidCSRF(sec, session, "") {
		t.Error("empty session/token accepted")
	}
	if CSRFToken(sec, "") != "" {
		t.Error("CSRF token for empty session should be empty")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short7!"); err == nil {
		t.Error("7-char password should be rejected")
	}
	if err := ValidatePassword("longenough"); err != nil {
		t.Errorf("8+ char password rejected: %v", err)
	}
}
