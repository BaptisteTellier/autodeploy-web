package server

import (
	"context"
	"encoding/json"
	"errors"
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
	JobID       string `json:"job_id"`
	Name        string `json:"name"`
	ISOFile     string `json:"iso_file"`
	CfgFile     string `json:"cfg_file"`
	Appliance   string `json:"appliance"`
	Hostname    string `json:"hostname"`
	Network     string `json:"network"`
	MFAAdmin    bool   `json:"mfa_admin"`
	SOEnabled   bool   `json:"so_enabled"`
	HA          bool   `json:"ha"`
	Disks       string `json:"disks"`
	SingleDisk  bool   `json:"single_disk"`
	GrubTimeout int    `json:"grub_timeout"` // GRUB menu timeout (s) baked into the ISO/cfg at build time
	Gateway     string `json:"gateway"`
	Netmask     string `json:"netmask"`
	// LicenseBaked is true when the output was built with a license baked into
	// its config (LicenseVBRTune + LicenseFile). In remote-kickstart deploys the
	// .lic cannot ride inside the ISO, so a baked-in license reference can break
	// the unattended install — surfaced as a warning in the deploy form.
	LicenseBaked bool `json:"license_baked"`
}

// Minimum per-role disk sizes (GiB). The user may request larger, never smaller.
const (
	minVSADiskGiB = 256
	minVIADiskGiB = 128
)

// buildNodeDisks assembles the per-node disk list. Disk COUNT is fixed by role
// (VSA 2, VIA 2, VIA single-disk 1); SIZE comes from the caller's per-disk
// values, floored at the role minimum.
func buildNodeDisks(c config.Config, sizes []int) []int {
	minSize := minVIADiskGiB
	count := 2
	if c.ApplianceType == "VSA" {
		minSize = minVSADiskGiB
	} else if c.VIASingleDisk {
		count = 1
	}
	result := make([]int, count)
	for i := range result {
		s := minSize
		if i < len(sizes) && sizes[i] > s {
			s = sizes[i]
		}
		result[i] = s
	}
	return result
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
	// VSA kickstart is always vbr-ks.cfg; all VIA variants (VIA/VIAHR/VIAiscsi) use proxy-ks.cfg.
	wantCfg := map[string]string{
		"VSA":      "vbr-ks.cfg",
		"VIA":      "proxy-ks.cfg",
		"VIAHR":    "proxy-ks.cfg",
		"VIAiscsi": "proxy-ks.cfg",
	}[cfg.ApplianceType]
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
			name := e.Name()
			if wantCfg != "" {
				if strings.EqualFold(name, wantCfg) {
					cfgFile = name // exact match — stop looking
				}
			} else if cfgFile == "" && !strings.HasPrefix(strings.ToLower(name), "grub") {
				cfgFile = name // fallback for unknown appliance types
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
		var gw, mask string
		if !c.UseDHCP {
			net = c.StaticIP
			gw = c.Gateway
			mask = c.Subnet
		}
		out = append(out, outputSummary{
			JobID:        e.Name(),
			Name:         friendlyJobName(dir, e.Name()),
			ISOFile:      iso,
			CfgFile:      cfgFile,
			Appliance:    c.ApplianceType,
			Hostname:     c.Hostname,
			Network:      net,
			MFAAdmin:     bool(c.VeeamAdminIsMfaEnabled),
			SOEnabled:    bool(c.VeeamSoIsEnabled),
			HA:           c.HighAvailabilityEnabled,
			Disks:        disksLabel(buildNodeDisks(c, nil)),
			SingleDisk:   c.VIASingleDisk,
			GrubTimeout:  c.GrubTimeout,
			LicenseBaked: c.LicenseVBRTune && c.LicenseFile != "",
			Gateway:      gw,
			Netmask:      mask,
		})
	}
	return out
}

// handleDeployPage renders the launch form + recent deployments. When the
// ?copy=<id> query parameter is present and resolves to a known deployment,
// the form's prefill JSON is injected so the JS can restore all non-secret
// field values.
func (s *Server) handleDeployPage(w http.ResponseWriter, r *http.Request) {
	var deployments []deploy.View
	if s.deps.DeployManager != nil {
		deployments = s.deps.DeployManager.List()
	}
	outputs := s.listOutputs()
	outputsJSON, _ := json.Marshal(outputs)

	// Build the prefill payload from either ?copy=<deployment-id> (a past run)
	// or ?preset=<name> (a saved deploy template). Both feed the same
	// DEPLOY_PREFILL mechanism in the form.
	prefillJSON := template.JS("null") //nolint:gosec
	presetName := ""
	if copyID := r.URL.Query().Get("copy"); copyID != "" && s.deps.DeployManager != nil {
		if d, ok := s.deps.DeployManager.Get(copyID); ok {
			if b, err := json.Marshal(d.View().Form); err == nil {
				prefillJSON = template.JS(b) //nolint:gosec — JSON of our own struct, rendered in a <script> JSON context
			}
		}
	} else if pn := strings.TrimSpace(r.URL.Query().Get("preset")); pn != "" && s.deps.DeployPresets != nil {
		if snap, err := s.deps.DeployPresets.Load(pn); err == nil {
			if b, err := json.Marshal(snap); err == nil {
				prefillJSON = template.JS(b) //nolint:gosec — JSON of our own struct, rendered in a <script> JSON context
				presetName = pn
			}
		}
	}

	var presets []deploy.PresetInfo
	if s.deps.DeployPresets != nil {
		presets, _ = s.deps.DeployPresets.List()
	}

	s.render(w, r, "views/deploy.html", map[string]any{
		"Kinds":         catalogViews(),
		"Outputs":       outputs,
		"OutputsJSON":   template.JS(outputsJSON), //nolint:gosec — JSON of our own structs, rendered in a <script> JSON context
		"Deployments":   deployments,
		"WorkspaceISOs": originalISOs(filepath.Join(s.deps.DataDir, "iso")),
		"LicenseFiles":  listDir(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}),
		"KSBaseURL":     "http://" + r.Host,
		"PrefillJSON":   prefillJSON,
		"DeployPresets": presets,
		"PresetName":    presetName,
	})
}

// handleListDeployPresets returns the saved deploy templates as JSON.
func (s *Server) handleListDeployPresets(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployPresets == nil {
		http.NotFound(w, r)
		return
	}
	items, err := s.deps.DeployPresets.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

// handleSaveDeployPreset saves the submitted deploy form as a named template.
// It builds the same non-secret FormSnapshot a launch would (deployFormSnapshot),
// so secrets (passwords / keys) are never persisted.
func (s *Server) handleSaveDeployPreset(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployPresets == nil {
		http.Error(w, translate(lang, "deploy.err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("preset_name"))
	if name == "" {
		http.Error(w, translate(lang, "deploy.tpl_name_required"), http.StatusBadRequest)
		return
	}
	specs := topology.Catalog(topology.Kind(r.FormValue("kind")))
	nodeCount := atoiDefault(r.FormValue("node_count"), len(specs))
	if nodeCount < len(specs) {
		nodeCount = len(specs)
	}
	if err := s.deps.DeployPresets.Save(name, deployFormSnapshot(r, nodeCount)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "name": name})
}

// handleDeleteDeployPreset removes a saved deploy template.
func (s *Server) handleDeleteDeployPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployPresets == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.deps.DeployPresets.Delete(r.PathValue("name")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		writeDeployLine(w, line)
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
			writeDeployLine(w, line)
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

// writeDeployLine routes a deployment log line to the right SSE event: a
// structured upload-progress event ("progress", data "<node>:<pct>") or a plain
// "log" line. Progress events drive the per-node upload bar without cluttering
// the text log.
func writeDeployLine(w http.ResponseWriter, line string) {
	if payload, ok := deploy.ParseNodeStatusLine(line); ok {
		writeSSE(w, "node", payload)
		return
	}
	if node, pct, ok := deploy.ParseProgressLine(line); ok {
		writeSSE(w, "progress", fmt.Sprintf("%d:%d", node, pct))
		return
	}
	writeSSE(w, "log", line)
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
// role ISO. diskSizes holds per-disk GiB values requested by the user; count
// and minimums are enforced by buildNodeDisks. The loaded config is also
// returned (the wiring step reads the VSA admin password from it).
func (s *Server) resolveOutputNode(jobid, role string, diskSizes []int, ks ksParams, bootCmd string) (deploy.NodeDeploy, config.Config, error) {
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
	// IP is left empty for DHCP outputs so the deploy step resolves the real
	// address from the hypervisor guest agent (GetVMIP) before wiring. Using the
	// config's leftover StaticIP here would make DHCP nodes collide on a stale IP.
	ip := ""
	var gw, netmask string
	if !c.UseDHCP {
		ip = c.StaticIP
		gw = c.Gateway
		netmask = c.Subnet
	}
	node := deploy.NodeDeploy{
		Name:        name,
		Role:        role,
		Disks:       buildNodeDisks(c, diskSizes),
		IP:          ip,
		Gateway:     gw,
		Netmask:     netmask,
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
		node.SingleDisk = c.VIASingleDisk
		node.BootCommand = strings.TrimSpace(bootCmd) // "" => role default typed at GRUB
		return node, c, nil
	}

	if iso == "" {
		return deploy.NodeDeploy{}, config.Config{}, fmt.Errorf("output %q: no ISO file found", jobid)
	}
	node.ISOPath = filepath.Join(dir, iso)
	return node, c, nil
}

// originalISOs lists the ORIGINAL Veeam ISOs in dir, excluding customised builds
// (those carry "_customized" in the name). Remote kickstart must always boot an
// unmodified appliance ISO and inject the kickstart over HTTP — never a
// pre-customised ISO.
func originalISOs(dir string) []string {
	all := listDir(dir, []string{".iso"})
	out := make([]string, 0, len(all))
	for _, name := range all {
		if strings.Contains(strings.ToLower(name), "_customized") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// buildHypervisor constructs the Hypervisor implementation for the selected
// provider, reading that provider's connection fields from the form. Each
// provider namespaces its inputs (pve_*, vs_*, hv_*, nx_*, xen_*) so the form
// can carry all field sets and submit only the active one.
func buildHypervisor(provider hypervisor.Provider, r *http.Request) (hypervisor.Hypervisor, error) {
	get := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	switch provider {
	case hypervisor.ProviderVSphere:
		return hypervisor.NewVSphere(hypervisor.VSphereConfig{
			URL:          get("vs_url"),
			Username:     get("vs_user"),
			Password:     r.FormValue("vs_password"),
			Insecure:     r.FormValue("vs_insecure") != "",
			Datacenter:   get("vs_datacenter"),
			Cluster:      get("vs_cluster"),
			ResourcePool: get("vs_resource_pool"),
			Datastore:    get("vs_datastore"),
			Network:      get("vs_network"),
			Folder:       get("vs_folder"),
		})
	case hypervisor.ProviderHyperV:
		return hypervisor.NewHyperV(hypervisor.HyperVConfig{
			Host:       get("hv_host"),
			Port:       atoiDefault(r.FormValue("hv_port"), 0),
			Username:   get("hv_user"),
			Password:   r.FormValue("hv_password"),
			HTTPS:      r.FormValue("hv_https") != "",
			Insecure:   r.FormValue("hv_insecure") != "",
			SwitchName: get("hv_switch"),
			VMPath:     get("hv_vm_path"),
			ISOPath:    get("hv_iso_path"),
		})
	case hypervisor.ProviderWorkstation:
		return hypervisor.NewWorkstation(hypervisor.WorkstationConfig{
			Host:        get("ws_host"),
			Port:        atoiDefault(r.FormValue("ws_port"), 0),
			Username:    get("ws_user"),
			Password:    r.FormValue("ws_password"),
			HTTPS:       r.FormValue("ws_https") != "",
			Insecure:    r.FormValue("ws_insecure") != "",
			InstallDir:  get("ws_install_dir"),
			VMBaseDir:   get("ws_vm_dir"),
			ISODir:      get("ws_iso_dir"),
			VNet:        get("ws_vnet"),
			VNCHost:     get("ws_vnc_host"),
			VNCPortBase: atoiDefault(r.FormValue("ws_vnc_port_base"), 0),
		})
	case hypervisor.ProviderNutanix:
		return hypervisor.NewNutanix(hypervisor.NutanixConfig{
			Endpoint:         get("nx_endpoint"),
			Port:             atoiDefault(r.FormValue("nx_port"), 9440),
			Username:         get("nx_user"),
			Password:         r.FormValue("nx_password"),
			Insecure:         r.FormValue("nx_insecure") != "",
			Cluster:          get("nx_cluster"),
			StorageContainer: get("nx_storage"),
			Subnet:           get("nx_subnet"),
		})
	case hypervisor.ProviderXCPng:
		return hypervisor.NewXCPng(hypervisor.XCPngConfig{
			Host:     get("xen_host"),
			Username: get("xen_user"),
			Password: r.FormValue("xen_password"),
			Insecure: r.FormValue("xen_insecure") != "",
			SR:       get("xen_sr"),
			ISOSR:    get("xen_iso_sr"),
			Network:  get("xen_network"),
		})
	default: // Proxmox
		return hypervisor.NewProxmox(hypervisor.ProxmoxConfig{
			BaseURL:     get("pve_url"),
			Node:        get("pve_node"),
			Storage:     get("pve_storage"),
			ISOStorage:  get("pve_iso_storage"),
			Username:    get("pve_user"),
			Password:    r.FormValue("pve_password"),
			TokenID:     get("pve_token_id"),
			TokenSecret: get("pve_token_secret"),
			Insecure:    r.FormValue("pve_insecure") != "",
		})
	}
}

// handleDeployStop cancels a running deployment (leaves the VMs in place).
func (s *Server) handleDeployStop(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	if !s.deps.DeployManager.Cancel(id) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/deploy/"+id, http.StatusSeeOther)
}

// handleDeployRemove stops the deployment and destroys all the VMs it created,
// then drops it from the registry and returns to the deploy index.
func (s *Server) handleDeployRemove(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if _, err := s.deps.DeployManager.Remove(ctx, id); err != nil {
		http.Error(w, translate(lang, "deploy.err_remove")+err.Error(), http.StatusInternalServerError)
		return
	}
	s.console.close(id) // drop any open REST console session for the destroyed VSA
	// The record is kept (state "removed") so the user can retry it.
	http.Redirect(w, r, "/deploy/"+id, http.StatusSeeOther)
}

// handleDeployRetry re-launches a finished/removed deployment with the same spec.
func (s *Server) handleDeployRetry(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.deps.DeployManager.Retry(r.PathValue("id"))
	if err != nil {
		http.Error(w, translate(lang, "deploy.err_retry")+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/deploy/"+d.ID, http.StatusSeeOther)
}

// handleDeployRewire re-runs only the wiring step of a finished/failed deployment.
func (s *Server) handleDeployRewire(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	wireTimeout := time.Duration(atoiMin(r.FormValue("wire_timeout"), 45, 5)) * time.Minute
	d, err := s.deps.DeployManager.RetryWire(r.PathValue("id"), wireTimeout)
	if err != nil {
		http.Error(w, translate(lang, "deploy.err_rewire")+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/deploy/"+d.ID, http.StatusSeeOther)
}

// deployFormSnapshot builds a FormSnapshot from the submitted form values so
// the deployment can later be copied back into the deploy form. Passwords and
// token secrets are deliberately excluded.
func deployFormSnapshot(r *http.Request, n int) deploy.FormSnapshot {
	text := map[string]string{}
	setIfNonEmpty := func(keys ...string) {
		for _, k := range keys {
			if v := strings.TrimSpace(r.FormValue(k)); v != "" {
				text[k] = v
			}
		}
	}
	setIfNonEmpty(
		// kickstart / boot
		"ks_base_url", "boot_wait", "vsa_base_iso", "via_base_iso",
		// global network
		"vm_bridge", "vm_vlan",
		// wiring
		"wire_timeout", "cluster_dns", "cluster_endpoint", "wire_license",
		// advanced wiring (secrets — keys/passwords — deliberately excluded)
		"wire_node_exporter_user", "wire_syslog_server", "wire_syslog_port", "wire_syslog_protocol",
		"wire_s3_name", "wire_s3_endpoint", "wire_s3_region", "wire_s3_bucket", "wire_s3_folder", "wire_s3_immutable_days", "wire_s3_mount_node",
		// Proxmox
		"pve_url", "pve_node", "pve_storage", "pve_iso_storage", "pve_user", "pve_token_id",
		// vSphere
		"vs_url", "vs_user", "vs_datacenter", "vs_cluster", "vs_resource_pool", "vs_datastore", "vs_network", "vs_folder",
		// Hyper-V
		"hv_host", "hv_port", "hv_user", "hv_switch", "hv_vm_path", "hv_iso_path",
		// Nutanix
		"nx_endpoint", "nx_port", "nx_user", "nx_cluster", "nx_storage", "nx_subnet",
		// XCP-ng
		"xen_host", "xen_user", "xen_sr", "xen_iso_sr", "xen_network",
		// VMware Workstation (ws_password is a secret — deliberately excluded)
		"ws_host", "ws_port", "ws_user", "ws_install_dir", "ws_vm_dir", "ws_iso_dir", "ws_vnet", "ws_vnc_host", "ws_vnc_port_base",
	)

	checks := map[string]bool{
		"pve_insecure":           r.FormValue("pve_insecure") != "",
		"vs_insecure":            r.FormValue("vs_insecure") != "",
		"hv_insecure":            r.FormValue("hv_insecure") != "",
		"hv_https":               r.FormValue("hv_https") != "",
		"nx_insecure":            r.FormValue("nx_insecure") != "",
		"xen_insecure":           r.FormValue("xen_insecure") != "",
		"ws_https":               r.FormValue("ws_https") != "",
		"ws_insecure":            r.FormValue("ws_insecure") != "",
		"wire_node_exporter":     r.FormValue("wire_node_exporter") != "",
		"wire_node_exporter_tls": r.FormValue("wire_node_exporter_tls") != "",
		"wire_s3":                r.FormValue("wire_s3") != "",
		"wire_s3_compatible":     r.FormValue("wire_s3_compatible") != "",
		"wire_s3_overwrite":      r.FormValue("wire_s3_overwrite") != "",
	}

	nodeOutputs := make([]string, n)
	nodeBoots := make([]string, n)
	for i := 0; i < n; i++ {
		nodeOutputs[i] = strings.TrimSpace(r.FormValue(fmt.Sprintf("node_%d_output", i)))
		nodeBoots[i] = r.FormValue(fmt.Sprintf("node_%d_bootcmd", i))
		// Per-node sizing + role (role lets "Copy to deploy" restore added VIAs)
		for _, k := range []string{
			fmt.Sprintf("node_%d_role", i),
			fmt.Sprintf("node_%d_cpus", i),
			fmt.Sprintf("node_%d_memory", i),
			fmt.Sprintf("node_%d_disk_0", i),
			fmt.Sprintf("node_%d_disk_1", i),
		} {
			if v := strings.TrimSpace(r.FormValue(k)); v != "" {
				text[k] = v
			}
		}
	}

	return deploy.FormSnapshot{
		Kind:        r.FormValue("kind"),
		Provider:    strings.TrimSpace(r.FormValue("provider")),
		RemoteKS:    r.FormValue("remote_ks") != "",
		Wire:        r.FormValue("wire") != "",
		PowerOn:     r.FormValue("power_on") != "",
		NodeOutputs: nodeOutputs,
		NodeBoots:   nodeBoots,
		Text:        text,
		Checks:      checks,
	}
}

// handleDeployDelete removes a deployment record from the registry and the
// database without touching any VMs. Returns 204 on success, 422 on error
// (e.g. the deployment is still running).
func (s *Server) handleDeployDelete(w http.ResponseWriter, r *http.Request) {
	if s.deps.DeployManager == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	if err := s.deps.DeployManager.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.console.close(id) // drop any open REST console session
	w.WriteHeader(http.StatusNoContent)
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

	provider := hypervisor.Provider(strDefault(strings.TrimSpace(r.FormValue("provider")), string(hypervisor.ProviderProxmox)))
	if !hypervisor.KnownProvider(provider) {
		http.Error(w, translate(lang, "deploy.err_provider"), http.StatusBadRequest)
		return
	}

	ks := ksParams{
		enabled: r.FormValue("remote_ks") != "",
		baseURL: strDefault(strings.TrimSpace(r.FormValue("ks_base_url")), "http://"+r.Host),
		vsaISO:  strings.TrimSpace(r.FormValue("vsa_base_iso")),
		viaISO:  strings.TrimSpace(r.FormValue("via_base_iso")),
	}
	// Remote kickstart types the GRUB command at the console; reject it up front
	// for providers with no keystroke-injection API (Nutanix AHV, XCP-ng).
	if ks.enabled && !hypervisor.SupportsKickstart(provider) {
		http.Error(w, translate(lang, "deploy.err_ks_unsupported"), http.StatusUnprocessableEntity)
		return
	}

	// Base roles come from the catalog; the form may APPEND extra VIA nodes
	// (node_count > len(specs)), each carrying its own node_<i>_role. The node
	// role (not the catalog) drives disk minimums and wiring classification.
	baseRoles := make([]string, len(specs))
	for i, sp := range specs {
		baseRoles[i] = roleLabel(sp)
	}
	nodeCount := atoiDefault(r.FormValue("node_count"), len(specs))
	if nodeCount < len(specs) {
		nodeCount = len(specs)
	}

	nodes := make([]deploy.NodeDeploy, nodeCount)
	var primaryCfg config.Config // first VSA's config — wiring creds come from it
	for i := 0; i < nodeCount; i++ {
		jobid := strings.TrimSpace(r.FormValue(fmt.Sprintf("node_%d_output", i)))
		if jobid == "" {
			http.Error(w, translate(lang, "deploy.err_output_missing"), http.StatusUnprocessableEntity)
			return
		}
		role := strings.TrimSpace(r.FormValue(fmt.Sprintf("node_%d_role", i)))
		if role == "" {
			if i < len(baseRoles) {
				role = baseRoles[i]
			} else {
				http.Error(w, translate(lang, "deploy.err_output_missing"), http.StatusUnprocessableEntity)
				return
			}
		}
		bootCmd := r.FormValue(fmt.Sprintf("node_%d_bootcmd", i))

		// Per-node disk sizing — up to 2 disks, each floored by buildNodeDisks.
		minDisk := minVIADiskGiB
		if strings.HasPrefix(role, "VSA") {
			minDisk = minVSADiskGiB
		}
		diskSizes := []int{
			atoiMin(r.FormValue(fmt.Sprintf("node_%d_disk_0", i)), minDisk, minDisk),
			atoiMin(r.FormValue(fmt.Sprintf("node_%d_disk_1", i)), minDisk, minDisk),
		}

		n, c, err := s.resolveOutputNode(jobid, role, diskSizes, ks, bootCmd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		n.CPUs = atoiDefault(r.FormValue(fmt.Sprintf("node_%d_cpus", i)), 4)
		n.MemoryMiB = atoiDefault(r.FormValue(fmt.Sprintf("node_%d_memory", i)), 8192)
		if i == 0 {
			primaryCfg = c
		}
		nodes[i] = n
	}

	hv, err := buildHypervisor(provider, r)
	if err != nil {
		http.Error(w, translate(lang, "deploy.err_destination")+err.Error(), http.StatusBadRequest)
		return
	}

	// Bridge and VLAN are shared across all nodes on the same host.
	vmSpec := hypervisor.VMSpec{
		Bridge: strDefault(strings.TrimSpace(r.FormValue("vm_bridge")), "vmbr0"),
		VLAN:   atoiDefault(r.FormValue("vm_vlan"), 0),
		UEFI:   true, // Veeam VSA/VIA appliances boot via UEFI/OVMF
	}

	// Remote kickstart and wiring both require the VM to boot; force power-on.
	powerOn := r.FormValue("power_on") != "" || ks.enabled

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
		// Optional license install over REST: a remote-kickstarted VSA cannot
		// carry the .lic inside the ISO, so it boots unlicensed. When a license
		// is selected, the wiring step pushes it once the VSA REST is up.
		licensePath := ""
		if lf := strings.TrimSpace(r.FormValue("wire_license")); lf != "" {
			licensePath = filepath.Join(s.deps.DataDir, "license", filepath.Base(lf))
		}
		wireCfg := wiring.Config{
			Username:        "veeamadmin",
			Password:        primaryCfg.VeeamAdminPassword,
			Insecure:        true,
			ClusterDNSName:  strings.TrimSpace(r.FormValue("cluster_dns")),
			ClusterEndpoint: strings.TrimSpace(r.FormValue("cluster_endpoint")),
			LicensePath:     licensePath,
		}
		// Advanced wiring options (revealed under the "Advanced" toggle).
		if r.FormValue("wire_node_exporter") != "" {
			wireCfg.NodeExporter = true
			wireCfg.NodeExporterTLS = r.FormValue("wire_node_exporter_tls") != ""
			wireCfg.NodeExporterUser = strings.TrimSpace(r.FormValue("wire_node_exporter_user"))
			wireCfg.NodeExporterPass = r.FormValue("wire_node_exporter_pass")
		}
		if sl := strings.TrimSpace(r.FormValue("wire_syslog_server")); sl != "" {
			wireCfg.SyslogServer = sl
			wireCfg.SyslogPort = atoiDefault(r.FormValue("wire_syslog_port"), 514)
			wireCfg.SyslogProtocol = strDefault(strings.TrimSpace(r.FormValue("wire_syslog_protocol")), "Udp")
		}
		if r.FormValue("wire_s3") != "" && strings.TrimSpace(r.FormValue("wire_s3_bucket")) != "" {
			wireCfg.S3 = &wiring.S3Config{
				Name:            strDefault(strings.TrimSpace(r.FormValue("wire_s3_name")), "Object Storage"),
				Compatible:      r.FormValue("wire_s3_compatible") != "",
				ServicePoint:    strings.TrimSpace(r.FormValue("wire_s3_endpoint")),
				Region:          strings.TrimSpace(r.FormValue("wire_s3_region")),
				Bucket:          strings.TrimSpace(r.FormValue("wire_s3_bucket")),
				Folder:          strDefault(strings.TrimSpace(r.FormValue("wire_s3_folder")), "backups"),
				AccessKey:       strings.TrimSpace(r.FormValue("wire_s3_access_key")),
				SecretKey:       r.FormValue("wire_s3_secret_key"),
				ImmutableDays:   atoiDefault(r.FormValue("wire_s3_immutable_days"), 0),
				MountServerNode: strings.TrimSpace(r.FormValue("wire_s3_mount_node")),
				OverwriteOwner:  r.FormValue("wire_s3_overwrite") != "",
			}
		}
		wirer = wiring.New(wireCfg)
	}

	snap := deployFormSnapshot(r, nodeCount)
	d, err := s.deps.DeployManager.Start(deploy.Spec{
		Label:       string(kind),
		Nodes:       nodes,
		HV:          hv,
		VM:          vmSpec,
		PowerOn:     powerOn,
		Wirer:       wirer,
		WireTimeout: time.Duration(atoiMin(r.FormValue("wire_timeout"), 45, 5)) * time.Minute,
		BootWait:    time.Duration(atoiMin(r.FormValue("boot_wait"), 10, 3)) * time.Second,
		Form:        snap,
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
