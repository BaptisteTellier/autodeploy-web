package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestKickstartPublicAndServed verifies the /ks/ capability endpoint: it is on
// the auth allow-list (a netbooting appliance can't sign in), serves .cfg files
// from the output folder, and refuses the credential-bearing config snapshot and
// any non-.cfg artefact.
func TestKickstartPublicAndServed(t *testing.T) {
	if !authPublic("/ks/abc/vbr-ks.cfg") {
		t.Error("/ks/ must be on the auth allow-list")
	}

	dir := t.TempDir()
	jobDir := filepath.Join(dir, "output", "job1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(jobDir, "vbr-ks.cfg"), []byte("KS-BODY"), 0o644)
	_ = os.WriteFile(filepath.Join(jobDir, jobConfigName), []byte("SECRET"), 0o644)

	s := &Server{deps: Deps{DataDir: dir}}
	serve := func(name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/ks/job1/"+name, nil)
		r.SetPathValue("jobid", "job1")
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		s.handleKickstart(w, r)
		return w
	}

	if w := serve("vbr-ks.cfg"); w.Code != http.StatusOK || w.Body.String() != "KS-BODY" {
		t.Errorf("cfg serve = %d %q, want 200 KS-BODY", w.Code, w.Body.String())
	}
	if w := serve(jobConfigName); w.Code != http.StatusNotFound {
		t.Errorf("config snapshot must 404, got %d", w.Code)
	}
	if w := serve("evil.txt"); w.Code != http.StatusNotFound {
		t.Errorf("non-.cfg must 404, got %d", w.Code)
	}
}
