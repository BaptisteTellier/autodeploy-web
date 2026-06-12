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
	"github.com/BaptisteTellier/autodeploy-web/internal/wiring"
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
	CfgFile   string `json:"cfg_file"`
	Appliance string `json:"appliance"`
	Hostname  string `json:"hostname"`
	Network   string `json:"network"`
	MFAAdmin  bool   `json:"mfa_admin"`
	SOEnabled bool   `json:"so_enabled"`
	HA        bool   `json:"ha"`
	Disks     string `json:"disks"`
}

// Minimum per-role disk sizes (GiB). The user may request larger, never smaller.
const (
	minVSADiskGiB = 256
	minVIADiskGiB = 128
)

// disksForConfig returns the per-role disk layout (sizes in GiB). Disk COUNT is
// fixed by role — VSA 2 disks, VIA 2 disks, VIA single-disk 1 disk — while the
// SIZE is the caller's choice, floored at the role minimum.
func disksForConfig(c config.Config, vsaSize, viaSize int) []int {
	if c.ApplianceType == "VSA" {
		if vsaSize < minVSADiskGiB {
			vsaSize = minVSADiskGiB
		}
		return []int{vsaSize, vsaSize}
	}
	if viaSize < minVIADiskGiB {
		viaSize = minVIADiskGiB
	}
	if c.VIASingleDisk {
		return []int{viaSize}
	}
	return []int{viaSize, viaSize}
}

func disksLabel(d []int) string {
	if len(d) == 0 {
		return "—"
	}
	return fmt.Sprintf("%d × %d GB", len(d), d[0])
}

// loadOutputConfig reads an output folder's job-config.json and locates its
// ISO and kickstart .cfg files (either may be absent depending on the job mode).
func loadOutputConfig(dir string) (cfg config.Config, isoFile, cfgFile string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, jobConfigName))
	if err != nil {
		return config.Config{}, "", "", false
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return config.Config{}, "", "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return config.Config{}, "", "", false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".iso":
			if isoFile == "" {
				isoFile = e.Name()
			}
		case ".cfg":
			if cfgFile == "" {
				cfgFile = e.Name()
			}
		}
	}
	return cfg, isoFile, cfgFile, true
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
		c, iso, cfgFile, ok := loadOutputConfig(dir)
		if !ok || (iso == "" && cfgFile == "") {
			continue // needs at least an ISO (classic) or a .cfg (kickstart)
		}
		net := "DHCP"
		if !c.UseDHCP {
			net = c.StaticIP
		}
		out = append(out, outputSummary{
			JobID:     e.Name(),
			Name:      friendlyJobName(dir, e.Name()),
			ISOFile:   iso,
			CfgFile:   cfgFile,
			Appliance: c.ApplianceType,
			Hostname:  c.Hostname,
			Network:   net,
			MFAAdmin:  bool(c.VeeamAdminIsMfaEnabled),
			SOEnabled: bool(c.VeeamSoIsEnabled),
			HA:        c.HighAvailabilityEnabled,
			Disks:     disksLabel(disksForConfig(c, minVSADiskGiB, minVIADiskGiB)),
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
		"Kinds":         catalogViews(),
		"Outputs":       outputs,
		"OutputsJSON":   template.JS(outputsJSON), //nolint:gosec — JSON of our own structs, rendered in a <script> JSON context
		"Deployments":   deployments,
		"WorkspaceISOs": listDir(filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}),
		"KSBaseURL":     "http://" + r.Host,
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

// ksParams carries the remote-kickstart settings of one deployment request.
type ksParams struct {
	enabled bool
	baseURL string // autodeploy-web base URL reachable from the appliances
	vsaISO  string // original VSA ISO filename (in /data/iso and/or the library)
	viaISO  string // original VIA ISO filename
}

// resolveOutputNode turns a chosen output folder (jobid) + slot role into a
// deploy node. Classic mode points at the prebuilt customised ISO; kickstart
// mode points at the output's .cfg (served over /content) plus the original
// role ISO. The disk layout is derived from the output's own config, which is
// also returned (the wiring step reads the VSA admin password from it).
func (s *Server) resolveOutputNode(jobid, role string, vsaSize, viaSize int, ks ksParams) (deploy.NodeDeploy, config.Config, error) {
	jobid = filepath.Base(jobid)
	dir := filepath.Join(s.deps.DataDir, "output", jobid)
	c, iso, cfgFile, ok := loadOutputConfig(dir)
	if !ok {
		return deploy.NodeDeploy{}, config.Config{}, fmt.Errorf("output %q: missing config", jobid)
	}
	name := c.Hostname
	if name == "" {
		name = jobid[:min(8, len(jobid))]
	}
	node := deploy.NodeDeploy{
		Name:        name,
		Role:        role,
		Disks:       disksForConfig(c, vsaSize, viaSize),
		IP:          c.StaticIP,
		PairingCode: wiring.DefaultPairingCode,
	}

	if ks.enabled {
		if cfgFile == "" {
			return deploy.NodeDeploy{}, config.Config{}, fmt.Errorf("output %q: no .cfg kickstart file (build with CleanupCFGFiles off or CFG-only)", jobid)
		}
		baseISO := ks.viaISO
		if strings.HasPrefix(role, "VSA") {
			baseISO = ks.vsaISO
		}
		if baseISO == "" {
			return deploy.NodeDeploy{}, config.Config{}, fmt.Errorf("no base ISO selected for role %s", role)
		}
		node.KSUrl = strings.TrimRight(ks.baseURL, "/") + "/media/output/" + jobid + "/" + cfgFile + "/content"
		node.BaseISOPath = filepath.Join(s.deps.DataDir, "iso", filepath.Base(baseISO))
		return node, c, nil
	}

	if iso == "" {
		return deploy.NodeDeploy{}, config.Config{}, fmt.Errorf("output %q: no ISO file found", jobid)
	}
	node.ISOPath = filepath.Join(dir, iso)
	return node, c, nil
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

	vsaSize := atoiMin(r.FormValue("vsa_disk"), minVSADiskGiB, minVSADiskGiB)
	viaSize := atoiMin(r.FormValue("via_disk"), minVIADiskGiB, minVIADiskGiB)

	ks := ksParams{
		enabled: r.FormValue("remote_ks") != "",
		baseURL: strDefault(strings.TrimSpace(r.FormValue("ks_base_url")), "http://"+r.Host),
		vsaISO:  strings.TrimSpace(r.FormValue("vsa_base_iso")),
		viaISO:  strings.TrimSpace(r.FormValue("via_base_iso")),
	}

	nodes := make([]deploy.NodeDeploy, len(specs))
	var primaryCfg config.Config // first VSA's config — wiring creds come from it
	for i, sp := range specs {
		jobid := strings.TrimSpace(r.FormValue(fmt.Sprintf("node_%d_output", i)))
		if jobid == "" {
			http.Error(w, translate(lang, "deploy.err_output_missing"), http.StatusUnprocessableEntity)
			return
		}
		n, c, err := s.resolveOutputNode(jobid, roleLabel(sp), vsaSize, viaSize, ks)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if i == 0 {
			primaryCfg = c
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
		UEFI:      true, // Veeam VSA/VIA appliances boot via UEFI/OVMF
	}

	powerOn := r.FormValue("power_on") != ""

	// Optional post-boot wiring (register VIA/HA into the VSA via Veeam REST).
	// Wiring needs the VMs powered on; enabling it implies power-on. The VSA
	// REST credentials come from the chosen output's own config (veeamadmin +
	// VeeamAdminPassword) — never asked again in the UI.
	var wirer deploy.Wirer
	if r.FormValue("wire") != "" {
		if primaryCfg.VeeamAdminPassword == "" {
			http.Error(w, translate(lang, "deploy.err_no_admin_pw"), http.StatusUnprocessableEntity)
			return
		}
		powerOn = true
		wirer = wiring.New(wiring.Config{
			Username:       "veeamadmin",
			Password:       primaryCfg.VeeamAdminPassword,
			Insecure:       true,
			ClusterDNSName: strings.TrimSpace(r.FormValue("cluster_dns")),
		})
	}

	d, err := s.deps.DeployManager.Start(deploy.Spec{
		Label:       string(kind),
		Nodes:       nodes,
		HV:          hv,
		VM:          vmSpec,
		PowerOn:     powerOn,
		Wirer:       wirer,
		WireTimeout: time.Duration(atoiMin(r.FormValue("wire_timeout"), 45, 5)) * time.Minute,
		BootWait:    time.Duration(atoiMin(r.FormValue("boot_wait"), 10, 3)) * time.Second,
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

// atoiMin parses s, falling back to def, then floors the result at min.
func atoiMin(s string, def, min int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		n = def
	}
	if n < min {
		n = min
	}
	return n
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
