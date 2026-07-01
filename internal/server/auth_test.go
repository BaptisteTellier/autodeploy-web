package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/auth"
)

func TestRequireAuthAllowlistAndSession(t *testing.T) {
	dir := t.TempDir()
	mgr := newAuthManager(dir, false, "", "")
	s := &Server{auth: mgr}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := s.requireAuth(next)

	do := func(method, path string, ck *http.Cookie, origin, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil) // Host defaults to example.com
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ck != nil {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Public paths reach next even with no config/session.
	if rec := do("GET", "/healthz", nil, "", ""); rec.Code != http.StatusTeapot {
		t.Errorf("/healthz not public: %d", rec.Code)
	}
	if rec := do("GET", "/static/app.js", nil, "", ""); rec.Code != http.StatusTeapot {
		t.Errorf("/static not public: %d", rec.Code)
	}
	// Not configured → HTML GET redirected to /setup.
	if rec := do("GET", "/", nil, "", "text/html"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Errorf("unconfigured redirect = %d %q, want 302 /setup", rec.Code, rec.Header().Get("Location"))
	}

	if err := mgr.setPassword("password123"); err != nil {
		t.Fatal(err)
	}

	// Configured, no session → HTML GET to /login, fetch to 401.
	if rec := do("GET", "/", nil, "", "text/html"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("no-session redirect = %d %q, want 302 /login", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do("GET", "/search", nil, "", "application/json"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no-session fetch = %d, want 401", rec.Code)
	}

	ck := &http.Cookie{Name: sessionCookie, Value: auth.NewSession(mgr.sessionSecret(), time.Now())}

	if rec := do("GET", "/", ck, "", "text/html"); rec.Code != http.StatusTeapot {
		t.Errorf("valid-session GET = %d, want next", rec.Code)
	}
	// CSRF: cross-origin unsafe method blocked; same-origin (or no Origin) allowed.
	if rec := do("POST", "/jobs", ck, "https://evil.example", ""); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rec.Code)
	}
	if rec := do("POST", "/jobs", ck, "http://example.com", ""); rec.Code != http.StatusTeapot {
		t.Errorf("same-origin POST = %d, want next", rec.Code)
	}
	if rec := do("POST", "/jobs", ck, "", ""); rec.Code != http.StatusTeapot {
		t.Errorf("no-Origin POST = %d, want next", rec.Code)
	}
	// A stale/garbage cookie is rejected.
	if rec := do("GET", "/", &http.Cookie{Name: sessionCookie, Value: "garbage"}, "", "text/html"); rec.Code != http.StatusFound {
		t.Errorf("garbage cookie = %d, want 302", rec.Code)
	}
}

func TestRequireAuthDisabled(t *testing.T) {
	mgr := newAuthManager(t.TempDir(), true, "", "") // AUTH_DISABLED
	s := &Server{auth: mgr}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	s.requireAuth(next).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("auth disabled should pass through: %d", rec.Code)
	}
}

func TestAuthManagerLockout(t *testing.T) {
	mgr := newAuthManager(t.TempDir(), false, "", "")
	if err := mgr.setPassword("password123"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < lockThreshold; i++ {
		if ok, _ := mgr.checkLogin("wrong"); ok {
			t.Fatal("wrong password accepted")
		}
	}
	// Now locked — even the correct password is refused with a retry hint.
	if ok, wait := mgr.checkLogin("password123"); ok || wait <= 0 {
		t.Errorf("expected lockout (ok=%v wait=%v)", ok, wait)
	}
}

func TestEnvHashDisablesSetup(t *testing.T) {
	h, _ := auth.HashPassword("frompass")
	mgr := newAuthManager(t.TempDir(), false, h, "")
	if !mgr.isConfigured() {
		t.Fatal("env hash should mark configured")
	}
	if !mgr.envHash {
		t.Error("envHash flag not set")
	}
}

// TestExecuteAuthPages renders the standalone login/setup pages for both langs.
func TestExecuteAuthPages(t *testing.T) {
	sets := parseTemplates()
	for _, lang := range supportedLangs {
		for _, page := range []string{"views/login.html", "views/setup.html"} {
			tmpl := sets[lang][page]
			if tmpl == nil {
				t.Fatalf("lang %q: %s not parsed", lang, page)
			}
			data := map[string]any{"Lang": lang, "Version": "test", "Next": "/", "Error": "oops"}
			if err := tmpl.Execute(io.Discard, data); err != nil {
				t.Errorf("lang %q: %s execute: %v", lang, page, err)
			}
		}
	}
}
