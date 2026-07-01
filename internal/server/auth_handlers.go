package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/auth"
)

// renderStandalone renders a full-page template (login/setup) WITHOUT the app
// shell. The page files are raw HTML (no {{define "content"}}), so executing
// the set's root template yields just that page.
func (s *Server) renderStandalone(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	lang := langFromRequest(r)
	data["Lang"] = lang
	data["Version"] = s.deps.Version
	set, ok := s.templates[lang]
	if !ok {
		set = s.templates[defaultLang]
	}
	t, ok := set[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		_, _ = w.Write([]byte("<pre>template error: " + err.Error() + "</pre>"))
	}
}

// safeNext returns a same-site relative redirect target from the ?next= param,
// defaulting to "/". Prevents open-redirects.
func safeNext(r *http.Request) string {
	n := r.URL.Query().Get("next")
	if n == "" {
		n = r.FormValue("next")
	}
	if strings.HasPrefix(n, "/") && !strings.HasPrefix(n, "//") {
		return n
	}
	return "/"
}

func (s *Server) hasValidSession(r *http.Request) bool {
	c, _ := r.Cookie(sessionCookie)
	return c != nil && auth.ValidSession(s.auth.sessionSecret(), c.Value, time.Now())
}

// handleLogin renders the login form (GET) and verifies the password (POST).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	// Auth off, or already signed in → nothing to do here.
	if s.auth == nil || !s.auth.enabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !s.auth.isConfigured() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		if s.hasValidSession(r) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		s.renderStandalone(w, r, "views/login.html", map[string]any{"Next": safeNext(r)})
		return
	}
	// POST
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request blocked", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	ok, retry := s.auth.checkLogin(r.FormValue("password"))
	if !ok {
		msg := translate(lang, "auth.err_bad_password")
		if retry > 0 {
			msg = translate(lang, "auth.err_locked")
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.renderStandalone(w, r, "views/login.html", map[string]any{"Error": msg, "Next": safeNext(r)})
		return
	}
	s.setSession(w, r)
	http.Redirect(w, r, safeNext(r), http.StatusSeeOther)
}

// handleSetup is the first-run "create admin password" flow. Only reachable
// when no password is configured and the hash is not pinned by the environment.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.auth == nil || !s.auth.enabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.auth.isConfigured() {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		s.renderStandalone(w, r, "views/setup.html", map[string]any{})
		return
	}
	// POST
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request blocked", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	pw := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if pw != confirm {
		w.WriteHeader(http.StatusBadRequest)
		s.renderStandalone(w, r, "views/setup.html", map[string]any{"Error": translate(lang, "auth.err_mismatch")})
		return
	}
	if err := s.auth.setPassword(pw); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		s.renderStandalone(w, r, "views/setup.html", map[string]any{"Error": translate(lang, "auth.err_weak")})
		return
	}
	s.setSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session. Accepts POST (from the UI) or GET.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.clearSession(w, r)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
