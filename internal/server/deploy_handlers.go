package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
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

func roleLabel(s topology.NodeSpec) string {
	label := string(s.Role)
	if s.HA {
		label += " (HA)"
	}
	return label
}

// catalogViews builds the kind→roles catalog for the deploy form.
func catalogViews() []kindView {
	out := make([]kindView, 0, len(topology.AllKinds()))
	for _, k := range topology.AllKinds() {
		var roles []string
		for _, s := range topology.Catalog(k) {
			roles = append(roles, roleLabel(s))
		}
		out = append(out, kindView{Kind: string(k), Roles: roles})
	}
	return out
}

// outputSummary describes a built output folder for the picker + summary card.
type outputSummary struct {
	JobID     string `json:"job_id"`
	Name      string `json:"name"`
	ISOFile   string `json:"iso_file"`
	Appliance string `json:"appliance"`
	Hostname  string `json:"hostname"`
	Network   string `json:"network"`
	MFAAdmin  bool   `json:"mfa_admin"`
	SOEnabled bool   `json:"so_enabled"`
	HA        bool   `json:"ha"`
	Disks     string `json:"disks"`
}

// disksForConfig returns the per-role disk layout (sizes in GiB):
// VSA = 2×256, VIA = 2×128, VIA single-disk = 1×128.
func disksForConfig(c config.Config) []int {
	if c.ApplianceType == "VSA" {
		return []int{256, 256}
	}
	if c.VIASingleDisk {
		return []int{128}
	}
	return []int{128, 128}
}

func disksLabel(d []int) string {
	if len(d) == 0 {
		return "—"
	}
	return fmt.Sprintf("%d × %d GB", len(d), d[0])
}

// loadOutputConfig reads an output folder's job-config.json and locates its ISO.
func loadOutputConfig(dir string) (cfg config.Config, isoFile string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, jobConfigName))
	if err != nil {
		return config.Config{}, "", false
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return config.Config{}, "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return config.Config{}, "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".iso") {
			isoFile = e.Name()
			break
		}
	}
	return cfg, isoFile, true
}

// listOutputs returns every output folder that has both a config and an ISO.
func (s *Server) listOutputs() []outputSummary {
	base := filepath.Join(s.deps.DataDir, "output")
	entries, _ := os.ReadDir(base)
	var out []outputSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		c, iso, ok := loadOutputConfig(dir)
		if !ok || iso == "" {
			continue
		}
		net := "DHCP"
		if !c.UseDHCP {
			net = c.StaticIP
		}
		out = append(out, outputSummary{
			JobID:     e.Name(),
			Name:      friendlyJobName(dir, e.Name()),
			ISOFile:   iso,
			Appliance: c.ApplianceType,
			Hostname:  c.Hostname,
			Network:   net,
			MFAAdmin:  bool(c.VeeamAdminIsMfaEnabled),
			SOEnabled: bool(c.VeeamSoIsEnabled),
			HA:        c.HighAvailabilityEnabled,
			Disks:     disksLabel(disksForConfig(c)),
		})
	}
	return out
}

// handleDeployPage renders the launch form + recent deployments.
func (s *Server) handleDeployPage(w http.ResponseWriter, r *http.Request) {
	var deployments []deploy.View
	if s.deps.DeployManager != nil {
		deployments = s.deps.DeployManager.List()
	}
	outputs := s.listOutputs()
	outputsJSON, _ := json.Marshal(outputs)
	s.render(w, r, "views/deploy.html", map[string]any{
		"Kinds":       catalogViews(),
		"Outputs":     outputs,
		"OutputsJSON": template.JS(outputsJSON), //nolint:gosec — JSON of our own structs, rendered in a <script> JSON context
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

// resolveOutputNode turns a chosen output folder (jobid) + slot role into a
// deploy node: locates the prebuilt ISO and derives the disk layout from the
// output's own config.
func (s *Server) resolveOutputNode(jobid, role string) (deploy.NodeDeploy, error) {
	jobid = filepath.Base(jobid)
	dir := filepath.Join(s.deps.DataDir, "output", jobid)
	c, iso, ok := loadOutputConfig(dir)
	if !ok {
		return deploy.NodeDeploy{}, fmt.Errorf("output %q: missing config", jobid)
	}
	if iso == "" {
		return deploy.NodeDeploy{}, fmt.Errorf("output %q: no ISO file found", jobid)
	}
	name := c.Hostname
	if name == "" {
		name = jobid[:min(8, len(jobid))]
	}
	return deploy.NodeDeploy{
		Name:    name,
		Role:    role,
		ISOPath: filepath.Join(dir, iso),
		Disks:   disksForConfig(c),
	}, nil
}

// handleDeployStart maps the chosen output folders onto the topology slots and
// launches the deployment.
func (s *Server) handleDeployStart(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployManager == nil {
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

	nodes := make([]deploy.NodeDeploy, len(specs))
	for i, sp := range specs {
		jobid := strings.TrimSpace(r.FormValue(fmt.Sprintf("node_%d_output", i)))
		if jobid == "" {
			http.Error(w, translate(lang, "deploy.err_output_missing"), http.StatusUnprocessableEntity)
			return
		}
		n, err := s.resolveOutputNode(jobid, roleLabel(sp))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		nodes[i] = n
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
		Bridge:    strDefault(strings.TrimSpace(r.FormValue("vm_bridge")), "vmbr0"),
		VLAN:      atoiDefault(r.FormValue("vm_vlan"), 0),
	}

	d, err := s.deps.DeployManager.Start(deploy.Spec{
		Label:   string(kind),
		Nodes:   nodes,
		HV:      hv,
		VM:      vmSpec,
		PowerOn: r.FormValue("power_on") != "",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/deploy/"+d.ID, http.StatusSeeOther)
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
