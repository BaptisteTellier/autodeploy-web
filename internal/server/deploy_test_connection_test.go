package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// stubVBRMux returns an http.ServeMux that simulates the minimal VBR REST
// endpoint needed for the test-connection handler:
//   - POST /api/oauth2/token  — always returns access_token "t"
//   - POST /api/oauth2/logout — 200
func stubVBRMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	})
	mux.HandleFunc("/api/oauth2/logout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// testServerForConn builds a minimal Server with only the test-connection route.
func testServerForConn(t *testing.T) http.Handler {
	t.Helper()
	s := &Server{
		deps:    Deps{},
		console: newConsoleManager(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /deploy/test-connection", s.handleTestConnection)
	return mux
}

// TestHandleTestConnectionVBR_OK posts manual target_* fields pointing at a
// TLS test server and asserts the JSON response has ok=true.
func TestHandleTestConnectionVBR_OK(t *testing.T) {
	// Use a TLS test server so the veeam client uses HTTPS; set target_insecure=true.
	vbrSrv := httptest.NewTLSServer(stubVBRMux())
	t.Cleanup(vbrSrv.Close)

	// Parse host and port from the TLS server URL.
	u, err := url.Parse(vbrSrv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	routes := testServerForConn(t)

	form := url.Values{
		"kind":            {"vbr"},
		"target_address":  {host},
		"target_port":     {portStr},
		"target_user":     {"veeamadmin"},
		"target_password": {"TestPW1!"},
		"target_insecure": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost, "/deploy/test-connection", strings.NewReader(form.Encode()))
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
	if !out.OK {
		t.Errorf("expected ok=true, got ok=false (message=%q)", out.Message)
	}
}

// TestHandleTestConnectionVBR_BadTarget posts an empty target_address and
// asserts the JSON response has ok=false (validation error, no network call).
func TestHandleTestConnectionVBR_BadTarget(t *testing.T) {
	routes := testServerForConn(t)

	form := url.Values{
		"kind":           {"vbr"},
		"target_address": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/deploy/test-connection", strings.NewReader(form.Encode()))
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
		t.Errorf("expected ok=false for empty target_address, got ok=true")
	}
	if out.Message == "" {
		t.Error("expected a non-empty error message")
	}
}
