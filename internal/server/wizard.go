package server

import (
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

// modeCookie persists the visitor's UI preference: the step-by-step wizard or
// the classic expert form. Absent cookie = newcomer (gets the wizard).
const modeCookie = "ui_mode"

const (
	modeExpert = "expert" // classic full form
	modeWizard = "wizard" // guided wizard
)

// isSupportedMode reports whether v is a known UI mode.
func isSupportedMode(v string) bool {
	return v == modeExpert || v == modeWizard
}

// modeFromRequest returns the persisted UI mode, or "" for a first-time visitor
// (mirrors langFromRequest).
func modeFromRequest(r *http.Request) string {
	if c, err := r.Cookie(modeCookie); err == nil && isSupportedMode(c.Value) {
		return c.Value
	}
	return ""
}

// handleWizard renders the full-page onboarding wizard. It feeds the same option
// data the classic form needs so the wizard's inputs (which reuse the exact
// form field names) can be pre-filled and validated identically.
func (s *Server) handleWizard(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "views/wizard.html", map[string]any{
		"Config":          config.Defaults(),
		"ApplianceTypes":  config.ApplianceTypes,
		"KeyboardLayouts": config.KeyboardLayouts,
		"Timezones":       config.Timezones,
		"SourceISOs":      listDir(filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}),
		"LicenseFiles":    listDir(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}),
	})
}

// handleSetMode persists the chosen UI mode in a cookie and redirects. Expert
// mode always lands on the classic form ("/"); wizard mode returns to the
// referring page (same-origin only) or falls back to "/wizard". Same cookie
// shape as handleSetLang.
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	mode := r.PathValue("mode")
	if !isSupportedMode(mode) {
		mode = modeExpert
	}
	http.SetCookie(w, &http.Cookie{
		Name:     modeCookie,
		Value:    mode,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	dest := "/new"
	if mode == modeWizard {
		dest = "/wizard"
		if ref := r.Referer(); ref != "" {
			if u, err := url.Parse(ref); err == nil && (u.Host == "" || u.Host == r.Host) {
				dest = u.RequestURI()
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
