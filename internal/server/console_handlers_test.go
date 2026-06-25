package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/veeam"
)

// stubVSAMux returns an http.ServeMux that simulates a minimal VBR REST endpoint:
//   - POST /api/oauth2/token  — always returns access_token "test-tok"
//   - GET  /api/v1/serverInfo — returns a JSON VBR info blob (200)
//   - GET  /api/v1/notfound   — returns 422 with a JSON error body
//   - POST /api/oauth2/logout — 200
func stubVSAMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-tok"})
	})
	mux.HandleFunc("/api/v1/serverInfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"productName":"Veeam Backup & Replication","version":"12.2"}`))
	})
	mux.HandleFunc("/api/v1/notfound", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"entity not found"}`))
	})
	mux.HandleFunc("/api/oauth2/logout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// newAuthenticatedClient builds a veeam.Client already authenticated against srv.
func newAuthenticatedClient(t *testing.T, srv *httptest.Server) *veeam.Client {
	t.Helper()
	c := veeam.New(veeam.Config{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "TestPW1!",
		Insecure: false,
	})
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return c
}

// TestConsoleManager verifies the consoleManager open/get/isOpen/close cycle.
func TestConsoleManager(t *testing.T) {
	srv := httptest.NewServer(stubVSAMux())
	t.Cleanup(srv.Close)
	client := newAuthenticatedClient(t, srv)

	cm := newConsoleManager()
	const id = "deploy-abc"

	if cm.isOpen(id) {
		t.Error("expected isOpen=false before open")
	}
	got, ok := cm.get(id)
	if ok || got != nil {
		t.Error("expected get to return nil,false before open")
	}

	cm.open(id, client)
	if !cm.isOpen(id) {
		t.Error("expected isOpen=true after open")
	}
	got, ok = cm.get(id)
	if !ok || got == nil {
		t.Error("expected get to return client after open")
	}

	cm.close(id)
	if cm.isOpen(id) {
		t.Error("expected isOpen=false after close")
	}
	got, ok = cm.get(id)
	if ok || got != nil {
		t.Error("expected get to return nil,false after close")
	}
}

// TestConsoleSweepAndReplace verifies the atomic replace (returns the displaced
// client) and the idle-session sweeper (drops stale sessions, keeps fresh ones).
func TestConsoleSweepAndReplace(t *testing.T) {
	srv := httptest.NewServer(stubVSAMux())
	t.Cleanup(srv.Close)

	cm := newConsoleManager()
	c1 := newAuthenticatedClient(t, srv)
	c2 := newAuthenticatedClient(t, srv)

	if old := cm.replace("d1", c1); old != nil {
		t.Error("replace on an empty slot should return nil")
	}
	if old := cm.replace("d1", c2); old != c1 {
		t.Error("replace should return the displaced client")
	}
	if got, _ := cm.get("d1"); got != c2 {
		t.Error("replace should install the new client")
	}

	// Force d1 idle; a fresh session must survive the sweep.
	cm.mu.Lock()
	cm.sessions["d1"].lastUsed = time.Now().Add(-time.Hour)
	cm.mu.Unlock()
	cm.open("fresh", c1)

	cm.sweep(30 * time.Minute)
	if cm.isOpen("d1") {
		t.Error("idle session should have been swept")
	}
	if !cm.isOpen("fresh") {
		t.Error("fresh session should survive the sweep")
	}
}

// minimalDeployment is a lightweight stand-in for a deploy.Deployment used only
// within the consoleHandlers test. It implements the subset of the manager API
// that handleConsoleRequest / handleConsoleStatus / handleConsoleClose use.
//
// Instead of fighting the real deploy.Manager (which requires a hypervisor and
// ISO paths to Start a deployment), we test the three handler methods that only
// need a cached veeam.Client in the consoleManager:
//
//   - handleConsoleStatus  — reads consoleManager only
//   - handleConsoleRequest — reads consoleManager + proxies via client.Raw
//   - handleConsoleClose   — writes consoleManager
//
// handleConsoleOpen is tested indirectly: if the session is pre-seeded, the open
// handler path through loadOutputConfig / Authenticate is covered by the veeam
// package's own tests; here we focus on what the server layer does with the session.
func testServerWithPreseededSession(t *testing.T, deployID string, client *veeam.Client) (http.Handler, *consoleManager) {
	t.Helper()
	cm := newConsoleManager()
	cm.open(deployID, client)

	// We don't need DeployManager for status/request/close (they only touch
	// consoleManager), so we build a minimal Server with an empty DeployManager.
	s := &Server{
		deps:    Deps{},
		console: cm,
	}

	// Build a minimal mux with only the console routes wired up.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /deploy/{id}/console/status", s.handleConsoleStatus)
	mux.HandleFunc("POST /deploy/{id}/console/request", s.handleConsoleRequest)
	mux.HandleFunc("POST /deploy/{id}/console/close", s.handleConsoleClose)

	return mux, cm
}

// TestConsoleStatusAndRequest exercises handleConsoleStatus and handleConsoleRequest
// against a pre-authenticated session pointed at a stub VSA.
func TestConsoleStatusAndRequest(t *testing.T) {
	vsaSrv := httptest.NewServer(stubVSAMux())
	t.Cleanup(vsaSrv.Close)

	client := newAuthenticatedClient(t, vsaSrv)
	const deployID = "deploy-test-001"
	routes, cm := testServerWithPreseededSession(t, deployID, client)
	_ = cm

	// --- status: open ---
	t.Run("status_open", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/deploy/"+deployID+"/console/status", nil)
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]bool
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !out["open"] {
			t.Error("expected open=true")
		}
	})

	// --- request 200: GET /api/v1/serverInfo ---
	t.Run("request_200", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]string{
			"method": "GET",
			"path":   "/api/v1/serverInfo",
		})
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/request", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("handler status %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if int(out["status"].(float64)) != http.StatusOK {
			t.Errorf("proxied status = %v, want 200", out["status"])
		}
		if !strings.Contains(out["body"].(string), "Veeam") {
			t.Errorf("proxied body = %q, want to contain 'Veeam'", out["body"])
		}
	})

	// --- request non-2xx (422): handler must return HTTP 200; proxied status in JSON ---
	t.Run("request_422_not_go_error", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]string{
			"method": "GET",
			"path":   "/api/v1/notfound",
		})
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/request", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("handler status %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if int(out["status"].(float64)) != http.StatusUnprocessableEntity {
			t.Errorf("proxied status = %v, want 422", out["status"])
		}
		if !strings.Contains(out["body"].(string), "not found") {
			t.Errorf("proxied body = %q, want to contain 'not found'", out["body"])
		}
	})

	// --- request validation: bad method ---
	t.Run("request_bad_method", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]string{
			"method": "PATCH",
			"path":   "/api/v1/serverInfo",
		})
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/request", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for bad method, got %d", rr.Code)
		}
	})

	// --- request validation: path must start with / ---
	t.Run("request_bad_path", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]string{
			"method": "GET",
			"path":   "api/v1/serverInfo",
		})
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/request", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for path without leading /, got %d", rr.Code)
		}
	})

	// --- close ---
	t.Run("close", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/close", nil)
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("close status %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]bool
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !out["ok"] {
			t.Error("expected ok=true from close")
		}
	})

	// --- status after close ---
	t.Run("status_after_close", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/deploy/"+deployID+"/console/status", nil)
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		var out map[string]bool
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out["open"] {
			t.Error("expected open=false after close")
		}
	})

	// --- request with no session returns 409 ---
	t.Run("request_no_session", func(t *testing.T) {
		reqBody, _ := json.Marshal(map[string]string{
			"method": "GET",
			"path":   "/api/v1/serverInfo",
		})
		req := httptest.NewRequest(http.MethodPost, "/deploy/"+deployID+"/console/request", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict {
			t.Errorf("expected 409 (no session), got %d", rr.Code)
		}
	})
}

// TestConsoleManagerConcurrency verifies the consoleManager is safe for concurrent access.
func TestConsoleManagerConcurrency(t *testing.T) {
	srv := httptest.NewServer(stubVSAMux())
	t.Cleanup(srv.Close)

	cm := newConsoleManager()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "dep-concurrent"
			client := veeam.New(veeam.Config{
				BaseURL:  srv.URL,
				Username: "u",
				Password: "p",
			})
			cm.open(id, client)
			_ = cm.isOpen(id)
			_, _ = cm.get(id)
			time.Sleep(time.Millisecond)
			cm.close(id)
		}(i)
	}
	wg.Wait()
}
