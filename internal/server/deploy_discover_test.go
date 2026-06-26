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

// discoverResult is the expected shape of every discover response.
type discoverResult struct {
	OK        bool             `json:"ok"`
	Message   string           `json:"message"`
	Resources map[string][]any `json:"resources"`
}

// postDiscover issues a POST to the discover endpoint and decodes the response.
func postDiscover(t *testing.T, routes http.Handler, form url.Values) discoverResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/deploy/hypervisor/discover", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out discoverResult
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// assertFailure checks that a discover response is a clean failure (ok=false,
// non-empty message, resources present).
func assertFailure(t *testing.T, out discoverResult) {
	t.Helper()
	if out.OK {
		t.Error("expected ok=false, got ok=true")
	}
	if out.Message == "" {
		t.Error("expected a non-empty error message")
	}
	if out.Resources == nil {
		t.Error("resources field should be present (empty object) on error")
	}
}

// ---------------------------------------------------------------------------
// Proxmox
// ---------------------------------------------------------------------------

// TestHandleHypervisorDiscover_UnreachableProxmox posts proxmox connection
// fields pointing at an unreachable URL and asserts the response is ok=false
// with a non-empty message and no panic.
func TestHandleHypervisorDiscover_UnreachableProxmox(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider":     {"proxmox"},
		"pve_url":      {"https://127.0.0.1:19996/api2/json"}, // connection refused — nothing listens there
		"pve_user":     {"root@pam"},
		"pve_password": {"secret"},
		"pve_insecure": {"true"},
	})
	assertFailure(t, out)
}

// TestHandleHypervisorDiscover_EmptyURL asserts that an empty pve_url returns
// ok=false with a clean error (no panic, no 500).
func TestHandleHypervisorDiscover_EmptyURL(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider": {"proxmox"},
		"pve_url":  {""},
		"pve_user": {"root@pam"},
	})
	if out.OK {
		t.Error("expected ok=false for empty pve_url, got ok=true")
	}
}

// ---------------------------------------------------------------------------
// vSphere
// ---------------------------------------------------------------------------

// TestHandleHypervisorDiscover_VSphere_EmptyURL asserts that an empty vs_url
// (missing required field) returns ok=false cleanly.
func TestHandleHypervisorDiscover_VSphere_EmptyURL(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider": {"vsphere"},
		"vs_url":   {""},
		"vs_user":  {"administrator@vsphere.local"},
	})
	assertFailure(t, out)
}

// TestHandleHypervisorDiscover_VSphere_Unreachable posts vsphere fields pointing
// at a localhost port that is not listening (connection refused immediately) and
// asserts the response is ok=false without a panic.
func TestHandleHypervisorDiscover_VSphere_Unreachable(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider":    {"vsphere"},
		"vs_url":      {"https://127.0.0.1:19999/sdk"}, // connection refused — nothing listens there
		"vs_user":     {"administrator@vsphere.local"},
		"vs_password": {"secret"},
		"vs_insecure": {"true"},
	})
	assertFailure(t, out)
}

// ---------------------------------------------------------------------------
// Hyper-V
// ---------------------------------------------------------------------------

// TestHandleHypervisorDiscover_HyperV_EmptyHost asserts that an empty hv_host
// (missing required field) returns ok=false cleanly.
func TestHandleHypervisorDiscover_HyperV_EmptyHost(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider": {"hyperv"},
		"hv_host":  {""},
		"hv_user":  {"administrator"},
	})
	assertFailure(t, out)
}

// TestHandleHypervisorDiscover_HyperV_Unreachable posts hyperv fields pointing
// at a localhost port that is not listening (connection refused immediately) and
// asserts the response is ok=false without a panic.
func TestHandleHypervisorDiscover_HyperV_Unreachable(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider":    {"hyperv"},
		"hv_host":     {"127.0.0.1"},
		"hv_port":     {"19998"}, // connection refused — nothing listens there
		"hv_user":     {"administrator"},
		"hv_password": {"secret"},
		"hv_insecure": {"true"},
	})
	assertFailure(t, out)
}

// ---------------------------------------------------------------------------
// VMware Workstation
// ---------------------------------------------------------------------------

// TestHandleHypervisorDiscover_Workstation_EmptyHost asserts that an empty
// ws_host (missing required field) returns ok=false cleanly.
func TestHandleHypervisorDiscover_Workstation_EmptyHost(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider": {"workstation"},
		"ws_host":  {""},
		"ws_user":  {"administrator"},
	})
	assertFailure(t, out)
}

// TestHandleHypervisorDiscover_Workstation_Unreachable posts workstation fields
// pointing at a localhost port that is not listening (connection refused
// immediately) and asserts ok=false without a panic.
func TestHandleHypervisorDiscover_Workstation_Unreachable(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider":    {"workstation"},
		"ws_host":     {"127.0.0.1"},
		"ws_port":     {"19997"}, // connection refused — nothing listens there
		"ws_user":     {"administrator"},
		"ws_password": {"secret"},
		"ws_insecure": {"true"},
	})
	assertFailure(t, out)
}

// ---------------------------------------------------------------------------
// Unknown provider
// ---------------------------------------------------------------------------

// TestHandleHypervisorDiscover_UnknownProvider asserts that an unknown provider
// returns ok=false with a helpful message (no panic, no 500).
func TestHandleHypervisorDiscover_UnknownProvider(t *testing.T) {
	routes := testServerForDiscover(t)
	out := postDiscover(t, routes, url.Values{
		"provider": {"nutanix"},
	})
	if out.OK {
		t.Error("expected ok=false for unsupported provider, got ok=true")
	}
	if !strings.Contains(out.Message, "nutanix") {
		t.Errorf("expected message to mention provider name, got: %q", out.Message)
	}
}
