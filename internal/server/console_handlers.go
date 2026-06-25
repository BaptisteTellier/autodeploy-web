package server

// VSA REST API console — backend handlers.
//
// Routes (registered in server.go):
//
//	POST /deploy/{id}/console/open    — authenticate & store session
//	POST /deploy/{id}/console/close   — logout & drop session
//	GET  /deploy/{id}/console/status  — {"open": bool}
//	POST /deploy/{id}/console/request — proxy an arbitrary REST call

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/veeam"
)

// consoleSession is the per-deployment REST-API console session.
type consoleSession struct {
	client   *veeam.Client
	lastUsed time.Time
}

// consoleIdleTimeout is how long a session may sit unused before the background
// sweeper logs it out and drops it (prevents leaked authenticated clients).
const consoleIdleTimeout = 30 * time.Minute

// consoleManager holds authenticated VSA sessions keyed by deployment ID.
type consoleManager struct {
	mu       sync.Mutex
	sessions map[string]*consoleSession
}

func newConsoleManager() *consoleManager {
	return &consoleManager{sessions: make(map[string]*consoleSession)}
}

func (cm *consoleManager) open(id string, c *veeam.Client) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.sessions[id] = &consoleSession{client: c, lastUsed: time.Now()}
}

// replace atomically installs c as the session for id and returns the client it
// displaced (nil if none), so the caller can log the old one out. Avoids the
// TOCTOU of a separate isOpen→close→open sequence.
func (cm *consoleManager) replace(id string, c *veeam.Client) *veeam.Client {
	cm.mu.Lock()
	old := cm.sessions[id]
	cm.sessions[id] = &consoleSession{client: c, lastUsed: time.Now()}
	cm.mu.Unlock()
	if old != nil {
		return old.client
	}
	return nil
}

func (cm *consoleManager) get(id string) (*veeam.Client, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if s, ok := cm.sessions[id]; ok {
		s.lastUsed = time.Now() // refresh idle timer
		return s.client, true
	}
	return nil, false
}

func (cm *consoleManager) close(id string) {
	cm.mu.Lock()
	s, ok := cm.sessions[id]
	delete(cm.sessions, id)
	cm.mu.Unlock()
	if ok {
		logoutClient(s.client)
	}
}

func (cm *consoleManager) isOpen(id string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	_, ok := cm.sessions[id]
	return ok
}

// sweep logs out and drops every session idle longer than maxIdle.
func (cm *consoleManager) sweep(maxIdle time.Duration) {
	cm.mu.Lock()
	var stale []*veeam.Client
	for id, s := range cm.sessions {
		if time.Since(s.lastUsed) > maxIdle {
			stale = append(stale, s.client)
			delete(cm.sessions, id)
		}
	}
	cm.mu.Unlock()
	for _, c := range stale {
		logoutClient(c)
	}
}

// startSweeper launches a background goroutine that expires idle sessions. It
// runs for the lifetime of the process (started once by the server).
func (cm *consoleManager) startSweeper() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			cm.sweep(consoleIdleTimeout)
		}
	}()
}

// logoutClient best-effort revokes a session's token with a bounded timeout.
func logoutClient(c *veeam.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.Logout(ctx)
}

// writeJSONStatus writes a JSON-encoded value with the given HTTP status code.
// (The existing writeJSON helper in handlers.go always uses HTTP 200; this variant
// lets callers set an explicit status code.)
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleConsoleOpen authenticates the container to the deployment's primary VSA
// and stores the session. If a session is already open it is closed and replaced
// (idempotent re-auth, e.g. after a container restart).
func (s *Server) handleConsoleOpen(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	id := r.PathValue("id")

	dep, ok := s.deps.DeployManager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	view := dep.View()

	// Find the primary VSA node (first node whose role starts with "VSA").
	vsaIdx := -1
	for i, n := range view.Nodes {
		if strings.HasPrefix(n.Role, "VSA") {
			vsaIdx = i
			break
		}
	}
	if vsaIdx < 0 {
		http.Error(w, "no VSA node in this deployment", http.StatusUnprocessableEntity)
		return
	}

	// The node's IP comes from NodeStatus.IP (set when the VM boots and reports its
	// address). If empty the VSA is not yet reachable.
	vsaIP := view.Nodes[vsaIdx].IP
	if vsaIP == "" {
		http.Error(w, "VSA node IP not yet known — wait for the VM to boot and report its IP", http.StatusUnprocessableEntity)
		return
	}

	// Resolve the admin password from the output config for the primary VSA node.
	// The form snapshot stores the job IDs in NodeOutputs (index matches Nodes).
	if vsaIdx >= len(view.Form.NodeOutputs) {
		http.Error(w, "cannot locate VSA output config (form snapshot incomplete)", http.StatusUnprocessableEntity)
		return
	}
	jobID := filepath.Base(view.Form.NodeOutputs[vsaIdx])
	dir := filepath.Join(s.deps.DataDir, "output", jobID)
	cfg, _, _, cfgOK := loadOutputConfig(dir)
	if !cfgOK {
		http.Error(w, fmt.Sprintf("output %q: missing or unreadable config", jobID), http.StatusUnprocessableEntity)
		return
	}
	if cfg.VeeamAdminPassword == "" {
		http.Error(w, translate(lang, "deploy.err_no_admin_pw"), http.StatusUnprocessableEntity)
		return
	}

	baseURL := "https://" + vsaIP + ":9419"
	client := veeam.New(veeam.Config{
		BaseURL:  baseURL,
		Username: "veeamadmin",
		Password: cfg.VeeamAdminPassword,
		Insecure: true,
	})
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Authenticate BEFORE installing the session so a failed re-auth never
	// drops a working one. Then swap atomically and log out any displaced client.
	if err := client.Authenticate(ctx); err != nil {
		http.Error(w, translate(lang, "deploy.console_err_open")+err.Error(), http.StatusBadGateway)
		return
	}
	if old := s.console.replace(id, client); old != nil {
		go logoutClient(old)
	}
	writeJSONStatus(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleConsoleClose logs out and removes the session.
func (s *Server) handleConsoleClose(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.console.close(id)
	writeJSONStatus(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleConsoleStatus returns whether a session is currently open.
func (s *Server) handleConsoleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSONStatus(w, http.StatusOK, map[string]bool{"open": s.console.isOpen(id)})
}

// consoleRequestBody is the body of POST /deploy/{id}/console/request.
type consoleRequestBody struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"` // optional raw JSON string
}

// handleConsoleRequest proxies an arbitrary authenticated REST call to the VSA.
func (s *Server) handleConsoleRequest(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	id := r.PathValue("id")

	client, ok := s.console.get(id)
	if !ok {
		http.Error(w, translate(lang, "deploy.console_no_session"), http.StatusConflict)
		return
	}

	var req consoleRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate method.
	allowed := map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true}
	upper := strings.ToUpper(req.Method)
	if !allowed[upper] {
		http.Error(w, "method must be one of GET, POST, PUT, DELETE", http.StatusBadRequest)
		return
	}

	// Validate path.
	if !strings.HasPrefix(req.Path, "/") {
		http.Error(w, "path must start with /", http.StatusBadRequest)
		return
	}

	var body []byte
	if strings.TrimSpace(req.Body) != "" {
		// Validate that body is valid JSON.
		var check any
		if err := json.Unmarshal([]byte(req.Body), &check); err != nil {
			http.Error(w, "request body is not valid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Compact the JSON (normalise whitespace).
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(req.Body)); err == nil {
			body = buf.Bytes()
		} else {
			body = []byte(req.Body)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	status, respBody, err := client.Raw(ctx, upper, req.Path, body)
	if err != nil {
		http.Error(w, translate(lang, "deploy.console_err_request")+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"status": status,
		"body":   string(respBody),
	})
}
