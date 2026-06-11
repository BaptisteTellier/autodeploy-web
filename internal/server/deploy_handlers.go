package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
	"github.com/BaptisteTellier/autodeploy-web/internal/hypervisor"
	"github.com/BaptisteTellier/autodeploy-web/internal/topology"
)

// kindView feeds the topology picker: a catalog Kind plus its ordered role labels.
type kindView struct {
	Kind  string   `json:"kind"`
	Roles []string `json:"roles"`
}

// catalogViews builds the kind→roles catalog for the deploy form.
func catalogViews() []kindView {
	out := make([]kindView, 0, len(topology.AllKinds()))
	for _, k := range topology.AllKinds() {
		var roles []string
		for _, s := range topology.Catalog(k) {
			label := string(s.Role)
			if s.HA {
				label += " (HA)"
			}
			roles = append(roles, label)
		}
		out = append(out, kindView{Kind: string(k), Roles: roles})
	}
	return out
}

// handleDeployPage renders the launch form + recent deployments.
func (s *Server) handleDeployPage(w http.ResponseWriter, r *http.Request) {
	var deployments []deploy.View
	if s.deps.DeployManager != nil {
		deployments = s.deps.DeployManager.List()
	}
	s.render(w, r, "views/deploy.html", map[string]any{
		"Kinds":       catalogViews(),
		"Presets":     presetListOrEmpty(s.deps.Store),
		"Deployments": deployments,
	})
}

// handleDeployDetail renders one deployment's progress + live log.
func (s *Server) handleDeployDetail(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	d, ok := s.deps.DeployManager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "views/deploy_detail.html", map[string]any{
		"Deployment": d.View(),
		"Lines":      d.Snapshot(),
	})
}

// handleDeployStream streams a deployment's log over SSE (mirrors handleJobStream).
func (s *Server) handleDeployStream(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	d, ok := s.deps.DeployManager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	hist, ch, cancel := d.Subscribe(256)
	defer cancel()
	for _, line := range hist {
		writeSSE(w, "log", line)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				writeSSE(w, "state", string(d.View().State))
				flusher.Flush()
				return
			}
			writeSSE(w, "log", line)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-d.Done():
			writeSSE(w, "state", string(d.View().State))
			flusher.Flush()
			return
		}
	}
}

// handleDeployStart parses the form, builds the topology + hypervisor, and
// launches a deployment, then redirects to its detail page.
func (s *Server) handleDeployStart(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployManager == nil || s.deps.ISOBuilder == nil {
		http.Error(w, translate(lang, "deploy.err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}

	kind := topology.Kind(r.FormValue("kind"))
	specs := topology.Catalog(kind)
	if specs == nil {
		http.Error(w, translate(lang, "deploy.err_kind"), http.StatusBadRequest)
		return
	}

	// Per-node identities, indexed node_<i>_<field>.
	ids := make([]topology.Identity, len(specs))
	for i := range specs {
		p := fmt.Sprintf("node_%d_", i)
		ids[i] = topology.Identity{
			Hostname: strings.TrimSpace(r.FormValue(p + "hostname")),
			StaticIP: strings.TrimSpace(r.FormValue(p + "ip")),
			Subnet:   strings.TrimSpace(r.FormValue(p + "subnet")),
			Gateway:  strings.TrimSpace(r.FormValue(p + "gateway")),
		}
		if dns := strings.TrimSpace(r.FormValue(p + "dns")); dns != "" {
			ids[i].DNSServers = splitList(dns)
		}
	}

	top, err := topology.New(kind, ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if errs := top.Validate(); len(errs) > 0 {
		http.Error(w, errs[0].Error(), http.StatusUnprocessableEntity)
		return
	}

	// Base config: a saved preset, else defaults.
	base := config.Defaults()
	if preset := r.FormValue("base_preset"); preset != "" {
		if loaded, err := s.deps.Store.Load(preset); err == nil {
			base = loaded
		}
	}

	hv, err := hypervisor.NewProxmox(hypervisor.ProxmoxConfig{
		BaseURL:     strings.TrimSpace(r.FormValue("pve_url")),
		Node:        strings.TrimSpace(r.FormValue("pve_node")),
		Storage:     strings.TrimSpace(r.FormValue("pve_storage")),
		ISOStorage:  strings.TrimSpace(r.FormValue("pve_iso_storage")),
		Username:    strings.TrimSpace(r.FormValue("pve_user")),
		Password:    r.FormValue("pve_password"),
		TokenID:     strings.TrimSpace(r.FormValue("pve_token_id")),
		TokenSecret: strings.TrimSpace(r.FormValue("pve_token_secret")),
		Insecure:    r.FormValue("pve_insecure") != "",
	})
	if err != nil {
		http.Error(w, translate(lang, "deploy.err_destination")+err.Error(), http.StatusBadRequest)
		return
	}

	vmSpec := hypervisor.VMSpec{
		CPUs:      atoiDefault(r.FormValue("vm_cpus"), 4),
		MemoryMiB: atoiDefault(r.FormValue("vm_memory"), 8192),
		DiskGiB:   atoiDefault(r.FormValue("vm_disk"), 100),
		Bridge:    strDefault(strings.TrimSpace(r.FormValue("vm_bridge")), "vmbr0"),
		VLAN:      atoiDefault(r.FormValue("vm_vlan"), 0),
	}

	d, err := s.deps.DeployManager.Start(deploy.Spec{
		Topology: top,
		Base:     base,
		HV:       hv,
		Builder:  s.deps.ISOBuilder,
		VM:       vmSpec,
		PowerOn:  r.FormValue("power_on") != "",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/deploy/"+d.ID, http.StatusSeeOther)
}

// splitList splits a comma/newline-separated list into trimmed non-empty items.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
