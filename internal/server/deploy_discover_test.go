package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testServerForDiscover builds a minimal Server with only the discover route.
func testServerForDiscover(t *testing.T) http.Handler {
	t.Helper()
	s := &Server{
		deps:    Deps{},
		console: newConsoleManager(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /deploy/hypervisor/discover", s.handleHypervisorDiscover)
	return mux
}

// TestHandleHypervisorDiscover_UnreachableProxmox posts proxmox connection
// fields pointing at an unreachable URL and asserts the response is ok=false
// with a non-empty message and no panic.
func TestHandleHypervisorDiscover_UnreachableProxmox(t *testing.T) {
	routes := testServerForDiscover(t)

	form := url.Values{
		"provider":     {"proxmox"},
		"pve_url":      {"https://192.0.2.1:8006/api2/json"}, // TEST-NET — unreachable
		"pve_user":     {"root@pam"},
		"pve_password": {"secret"},
		"pve_insecure": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost, "/deploy/hypervisor/discover", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK        bool             `json:"ok"`
		Message   string           `json:"message"`
		Resources map[string][]any `json:"resources"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.OK {
		t.Error("expected ok=false for unreachable host, got ok=true")
	}
	if out.Message == "" {
		t.Error("expected a non-empty error message")
	}
	if out.Resources == nil {
		t.Error("resources field should be present (empty object) on error")
	}
}

// TestHandleHypervisorDiscover_EmptyURL asserts that an empty pve_url returns
// ok=false with a clean error (no panic, no 500).
func TestHandleHypervisorDiscover_EmptyURL(t *testing.T) {
	routes := testServerForDiscover(t)

	form := url.Values{
		"provider": {"proxmox"},
		"pve_url":  {""},
		"pve_user": {"root@pam"},
	}
	req := httptest.NewRequest(http.MethodPost, "/deploy/hypervisor/discover", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.OK {
		t.Error("expected ok=false for empty pve_url, got ok=true")
	}
}

// TestHandleHypervisorDiscover_UnsupportedProvider asserts that a provider
// that does not yet support discovery returns ok=false with a helpful message.
func TestHandleHypervisorDiscover_UnsupportedProvider(t *testing.T) {
	routes := testServerForDiscover(t)

	form := url.Values{
		"provider": {"vsphere"},
	}
	req := httptest.NewRequest(http.MethodPost, "/deploy/hypervisor/discover", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.OK {
		t.Error("expected ok=false for unsupported provider, got ok=true")
	}
	if !strings.Contains(out.Message, "vsphere") {
		t.Errorf("expected message to mention provider name, got: %q", out.Message)
	}
}
