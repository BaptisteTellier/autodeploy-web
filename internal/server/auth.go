package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/auth"
)

const (
	sessionCookie = "adw_session"
	// lockThreshold consecutive failures triggers a cooldown; a small, fixed
	// window is plenty to blunt online brute-force of a local admin password.
	lockThreshold = 5
	lockWindow    = 30 * time.Second
)

// authFile is the on-disk shape of <DataDir>/auth.json (mode 0600).
type authFile struct {
	PasswordHash string `json:"password_hash"`
	Secret       string `json:"secret"` // base64 raw-url, HMAC signing key
}

// authManager holds the runtime auth state: whether auth is enabled, the admin
// password hash, the HMAC secret, and a tiny failed-login limiter. It is safe
// for concurrent use.
type authManager struct {
	mu        sync.Mutex
	path      string
	enabled   bool
	envHash   bool // hash came from the environment (authoritative; /setup disabled)
	hash      string
	secret    []byte
	fails     int
	lockUntil time.Time
}

// newAuthManager builds the manager from <dataDir>/auth.json plus environment
// overrides. Precedence for the password: ADMIN_PASSWORD_HASH > ADMIN_PASSWORD
// > persisted hash. A stable HMAC secret is generated and persisted on first
// run so sessions survive restarts (no idle timeout means we want them sticky).
func newAuthManager(dataDir string, disabled bool, envHash, envPassword string) *authManager {
	m := &authManager{path: filepath.Join(dataDir, "auth.json"), enabled: !disabled}

	af := loadAuthFile(m.path)
	m.hash = af.PasswordHash
	if af.Secret != "" {
		if b, err := base64.RawURLEncoding.DecodeString(af.Secret); err == nil {
			m.secret = b
		}
	}

	switch {
	case strings.TrimSpace(envHash) != "":
		m.hash = strings.TrimSpace(envHash)
		m.envHash = true
	case strings.TrimSpace(envPassword) != "":
		if h, err := auth.HashPassword(strings.TrimSpace(envPassword)); err == nil {
			m.hash = h
			m.envHash = true
		}
	}

	// Ensure a persisted signing secret whenever auth is on.
	if m.enabled && len(m.secret) == 0 {
		if s, err := auth.GenerateSecret(); err == nil {
			if b, derr := base64.RawURLEncoding.DecodeString(s); derr == nil {
				m.secret = b
				af.Secret = s
				_ = saveAuthFile(m.path, af) // best-effort; regenerated next boot if it fails
			}
		}
	}
	return m
}

func loadAuthFile(path string) authFile {
	var af authFile
	b, err := os.ReadFile(path)
	if err != nil {
		return af
	}
	_ = json.Unmarshal(b, &af)
	return af
}

func saveAuthFile(path string, af authFile) error {
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (m *authManager) isConfigured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hash != ""
}

func (m *authManager) sessionSecret() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.secret
}

// setPassword hashes and persists a new admin password (first-run /setup or a
// later change) and rotates the signing secret, which invalidates all existing
// sessions. Refused when the hash is pinned by the environment.
func (m *authManager) setPassword(pw string) error {
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	sec, err := auth.GenerateSecret()
	if err != nil {
		return err
	}
	secBytes, err := base64.RawURLEncoding.DecodeString(sec)
	if err != nil {
		return err
	}
	if err := saveAuthFile(m.path, authFile{PasswordHash: h, Secret: sec}); err != nil {
		return err
	}
	m.mu.Lock()
	m.hash, m.secret, m.fails, m.lockUntil = h, secBytes, 0, time.Time{}
	m.mu.Unlock()
	return nil
}

// checkLogin verifies pw against the stored hash, applying a simple lockout
// after lockThreshold consecutive failures. Returns (ok, retryAfter).
func (m *authManager) checkLogin(pw string) (bool, time.Duration) {
	m.mu.Lock()
	hash := m.hash
	if !m.lockUntil.IsZero() && time.Now().Before(m.lockUntil) {
		wait := time.Until(m.lockUntil)
		m.mu.Unlock()
		return false, wait
	}
	m.mu.Unlock()

	ok := hash != "" && auth.CheckPassword(hash, pw)

	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.fails, m.lockUntil = 0, time.Time{}
		return true, 0
	}
	m.fails++
	if m.fails >= lockThreshold {
		m.lockUntil = time.Now().Add(lockWindow)
		m.fails = 0
	}
	return false, 0
}

// --- middleware -------------------------------------------------------------

// authPublic reports whether a path is reachable without a session.
func authPublic(p string) bool {
	switch p {
	case "/login", "/logout", "/setup", "/healthz":
		return true
	}
	// /static/* (assets) and /lang/* (language switch, so the login page's EN/FR
	// toggle works before sign-in) need no session.
	return strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/lang/")
}

// requireAuth gates every non-public route behind a valid session (when auth is
// enabled), forces first-run setup when no password is configured, and applies
// a same-origin check to unsafe methods (CSRF defence-in-depth on top of the
// SameSite=Strict session cookie).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil || !s.auth.enabled || authPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.auth.isConfigured() {
			s.authReject(w, r, "/setup")
			return
		}
		c, _ := r.Cookie(sessionCookie)
		if c == nil || !auth.ValidSession(s.auth.sessionSecret(), c.Value, time.Now()) {
			s.authReject(w, r, "/login")
			return
		}
		if isUnsafeMethod(r.Method) && !sameOrigin(r) {
			http.Error(w, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOrigin allows the request when no Origin header is present (same-origin
// navigations/forms often omit it) and, when present, requires its host to
// match the request Host. Combined with the SameSite=Strict cookie this blocks
// cross-site state changes.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// authReject sends the caller toward dest: an HX-Redirect for htmx, a 302 for
// top-level HTML navigations, and a plain 401 for fetch/XHR/SSE so client code
// can react.
func (s *Server) authReject(w http.ResponseWriter, r *http.Request, dest string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// setSession issues the signed session cookie (HttpOnly, SameSite=Strict,
// Secure when served over TLS, absolute 30-day max-age, no idle timeout).
func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    auth.NewSession(s.auth.sessionSecret(), time.Now()),
		Path:     "/",
		MaxAge:   int(auth.SessionMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isTLS(r),
	})
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isTLS(r),
	})
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
