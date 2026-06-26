package hypervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/luthermonson/go-proxmox"
	"github.com/masterzen/winrm"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/soap"
)

// Option is a single selectable value returned by a discovery call. Label
// defaults to Value when empty; callers may set Label for a friendlier display.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// ProxmoxConnConfig is the subset of ProxmoxConfig that is known at connect
// time, before the user has picked a node/storage/bridge. Node is optional: if
// set it scopes the storages and networks listing; otherwise the first node
// returned by the cluster is used.
type ProxmoxConnConfig struct {
	BaseURL     string // full API base, e.g. "https://192.168.1.10:8006/api2/json"
	Username    string // e.g. "root@pam" — mutually exclusive with TokenID
	Password    string
	TokenID     string // e.g. "root@pam!mytoken" — takes priority over Username/Password
	TokenSecret string
	Insecure    bool   // skip TLS verification
	Node        string // optional — scopes storages/bridges listing
}

// newProxmoxClient builds a go-proxmox client using the same auth + TLS logic
// as NewProxmox, but without requiring Node/Storage/ISOStorage.
func newProxmoxClient(cfg ProxmoxConnConfig) (*proxmox.Client, error) {
	httpClient := http.DefaultClient
	if cfg.Insecure {
		httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec — self-signed PVE cert, opt-in
		}
	}
	opts := []proxmox.Option{proxmox.WithHTTPClient(httpClient)}
	switch {
	case cfg.TokenID != "":
		opts = append(opts, proxmox.WithAPIToken(cfg.TokenID, cfg.TokenSecret))
	case cfg.Username != "":
		opts = append(opts, proxmox.WithCredentials(&proxmox.Credentials{Username: cfg.Username, Password: cfg.Password}))
	default:
		return nil, fmt.Errorf("proxmox: provide either Username/Password or TokenID/TokenSecret")
	}
	return proxmox.NewClient(cfg.BaseURL, opts...), nil
}

// storageContentHas reports whether the comma-separated content string includes
// the given content type.
func storageContentHas(content, kind string) bool {
	for _, part := range strings.Split(content, ",") {
		if strings.TrimSpace(part) == kind {
			return true
		}
	}
	return false
}

// DiscoverProxmox connects to a Proxmox VE cluster and returns available
// resources keyed by deploy form field name:
//
//   - "pve_node":        all cluster nodes
//   - "pve_storage":     storages whose content includes "images" or "rootdir"
//   - "pve_iso_storage": storages whose content includes "iso"
//   - "vm_bridge":       network interfaces of type "bridge"
//
// Authentication failure or unreachable host returns an error.
// A missing method / empty list for a sub-category is silently omitted from the
// result rather than failing the whole discovery.
func DiscoverProxmox(ctx context.Context, cfg ProxmoxConnConfig) (map[string][]Option, error) {
	client, err := newProxmoxClient(cfg)
	if err != nil {
		return nil, err
	}

	// Listing nodes doubles as the connection + auth test.
	nodeStatuses, err := client.Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("proxmox: connect: %w", err)
	}

	result := make(map[string][]Option)

	// pve_node — one option per cluster node.
	nodes := make([]Option, 0, len(nodeStatuses))
	for _, ns := range nodeStatuses {
		name := ns.Node
		if name == "" {
			continue
		}
		nodes = append(nodes, Option{Value: name})
	}
	if len(nodes) > 0 {
		result["pve_node"] = nodes
	}

	// Resolve the node to use for storages + bridges.
	targetNode := cfg.Node
	if targetNode == "" && len(nodeStatuses) > 0 {
		targetNode = nodeStatuses[0].Node
	}
	if targetNode == "" {
		// Nothing to enumerate further.
		return result, nil
	}

	nodeHandle, err := client.Node(ctx, targetNode)
	if err != nil && len(nodeStatuses) > 0 && targetNode != nodeStatuses[0].Node {
		// cfg.Node was stale/wrong (e.g. the deploy form's default placeholder) —
		// fall back to the first cluster node so storages/bridges still list.
		targetNode = nodeStatuses[0].Node
		nodeHandle, err = client.Node(ctx, targetNode)
	}
	if err != nil {
		// Node lookup failed — return what we have so far.
		return result, nil //nolint:nilerr — partial results are OK
	}

	// pve_storage + pve_iso_storage — enumerate node storages by content type.
	storages, err := nodeHandle.Storages(ctx)
	if err == nil {
		var diskStores, isoStores []Option
		for _, s := range storages {
			name := s.Name
			if name == "" {
				continue
			}
			opt := Option{Value: name}
			if storageContentHas(s.Content, "images") || storageContentHas(s.Content, "rootdir") {
				diskStores = append(diskStores, opt)
			}
			if storageContentHas(s.Content, "iso") {
				isoStores = append(isoStores, opt)
			}
		}
		if len(diskStores) > 0 {
			result["pve_storage"] = diskStores
		}
		if len(isoStores) > 0 {
			result["pve_iso_storage"] = isoStores
		}
	}

	// vm_bridge — keep network interfaces of type "bridge".
	networks, err := nodeHandle.Networks(ctx)
	if err == nil {
		var bridges []Option
		for _, n := range networks {
			if n.Type == "bridge" {
				iface := n.Iface
				if iface == "" {
					continue
				}
				bridges = append(bridges, Option{Value: iface})
			}
		}
		if len(bridges) > 0 {
			result["vm_bridge"] = bridges
		}
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// vSphere discovery
// ---------------------------------------------------------------------------

// VSphereConnConfig is the subset of connection details needed for discovery.
// Datacenter is optional; when non-empty a second round-trip scopes cluster,
// resource pool, datastore, network and folder listings to that datacenter.
type VSphereConnConfig struct {
	URL        string // vCenter SDK URL, e.g. "https://vc.lab.local/sdk"
	Username   string
	Password   string
	Insecure   bool
	Datacenter string // optional; when set, enables cascade resource listing
}

// DiscoverVSphere connects to a vCenter / ESXi host and returns resource
// options keyed by deploy-form field name:
//
//   - "vs_datacenter": all datacenters (always returned)
//   - "vs_cluster":        compute clusters in the chosen datacenter
//   - "vs_resource_pool":  resource pools in the chosen datacenter
//   - "vs_datastore":      datastores in the chosen datacenter
//   - "vs_network":        port groups / networks in the chosen datacenter
//   - "vs_folder":         VM folders in the chosen datacenter
//
// The last five are only populated when cfg.Datacenter is non-empty (cascade).
// A successful connect + datacenter list doubles as the connection/auth test.
func DiscoverVSphere(ctx context.Context, cfg VSphereConnConfig) (map[string][]Option, error) {
	if cfg.URL == "" || cfg.Username == "" {
		return nil, fmt.Errorf("vsphere: URL and Username are required")
	}

	u, err := soap.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("vsphere: parse URL: %w", err)
	}
	u.User = url.UserPassword(cfg.Username, cfg.Password)

	c, err := govmomi.NewClient(ctx, u, cfg.Insecure)
	if err != nil {
		return nil, fmt.Errorf("vsphere: connect: %w", err)
	}
	defer c.Logout(ctx) //nolint:errcheck — best-effort logout

	f := find.NewFinder(c.Client, true)

	result := make(map[string][]Option)

	// Always list all datacenters — this is the connection test.
	dcs, err := f.DatacenterList(ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("vsphere: list datacenters: %w", err)
	}
	dcOpts := make([]Option, 0, len(dcs))
	for _, dc := range dcs {
		name := dc.Name()
		if name == "" {
			continue
		}
		dcOpts = append(dcOpts, Option{Value: name})
	}
	if len(dcOpts) > 0 {
		result["vs_datacenter"] = dcOpts
	}

	// Cascade: scope remaining resources to the chosen datacenter.
	if cfg.Datacenter == "" {
		return result, nil
	}

	dc, err := f.Datacenter(ctx, cfg.Datacenter)
	if err != nil {
		// Datacenter not found — return what we have.
		return result, nil //nolint:nilerr — partial results are OK
	}
	f.SetDatacenter(dc)

	// vs_cluster — DRS clusters AND standalone hosts (the field accepts either,
	// so a hostless/standalone-ESXi vCenter still gets a dropdown).
	{
		var opts []Option
		seen := map[string]bool{}
		add := func(n string) {
			if n != "" && !seen[n] {
				seen[n] = true
				opts = append(opts, Option{Value: n})
			}
		}
		if clusters, err := f.ClusterComputeResourceList(ctx, "*"); err == nil {
			for _, cl := range clusters {
				add(cl.Name())
			}
		}
		if hosts, err := f.HostSystemList(ctx, "*"); err == nil {
			for _, h := range hosts {
				add(h.Name())
			}
		}
		if len(opts) > 0 {
			result["vs_cluster"] = opts
		}
	}

	// vs_resource_pool
	if pools, err := f.ResourcePoolList(ctx, "*"); err == nil {
		var opts []Option
		for _, p := range pools {
			if n := p.Name(); n != "" {
				opts = append(opts, Option{Value: n})
			}
		}
		if len(opts) > 0 {
			result["vs_resource_pool"] = opts
		}
	}

	// vs_datastore
	if datastores, err := f.DatastoreList(ctx, "*"); err == nil {
		var opts []Option
		for _, ds := range datastores {
			if n := ds.Name(); n != "" {
				opts = append(opts, Option{Value: n})
			}
		}
		if len(opts) > 0 {
			result["vs_datastore"] = opts
		}
	}

	// vs_network — NetworkList returns object.NetworkReference (interface);
	// the inventory name is the base name of the inventory path.
	if nets, err := f.NetworkList(ctx, "*"); err == nil {
		var opts []Option
		for _, n := range nets {
			name := path.Base(n.GetInventoryPath())
			if name == "" || name == "." {
				continue
			}
			opts = append(opts, Option{Value: name})
		}
		if len(opts) > 0 {
			result["vs_network"] = opts
		}
	}

	// vs_folder
	if folders, err := f.FolderList(ctx, "*"); err == nil {
		var opts []Option
		for _, fl := range folders {
			if n := fl.Name(); n != "" {
				opts = append(opts, Option{Value: n})
			}
		}
		if len(opts) > 0 {
			result["vs_folder"] = opts
		}
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Hyper-V discovery
// ---------------------------------------------------------------------------

// HyperVConnConfig is the subset of HyperVConfig needed for discovery.
type HyperVConnConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	HTTPS    bool
	Insecure bool
}

// hyperVWinRMPort resolves the effective WinRM port.
func hyperVWinRMPort(cfg HyperVConnConfig) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	if cfg.HTTPS {
		return 5986
	}
	return 5985
}

// DiscoverHyperV connects to a Hyper-V host over WinRM and returns:
//
//   - "hv_switch":  all virtual switches (Get-VMSwitch)
//   - "hv_vm_path": the default VM path from Get-VMHost
//
// Running these PS commands doubles as the connection + auth test.
func DiscoverHyperV(ctx context.Context, cfg HyperVConnConfig) (map[string][]Option, error) {
	if cfg.Host == "" || cfg.Username == "" {
		return nil, fmt.Errorf("hyperv: Host and Username are required")
	}

	ep := winrm.NewEndpoint(
		cfg.Host,
		hyperVWinRMPort(cfg),
		cfg.HTTPS,
		cfg.Insecure,
		nil, nil, nil,
		0,
	)
	client, err := winrm.NewClient(ep, cfg.Username, cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("hyperv: build winrm client: %w", err)
	}

	// hv_switch — also serves as the connection + auth + Hyper-V module test.
	switchScript := `Get-VMSwitch | Select-Object -ExpandProperty Name`
	switchOut, switchErr := runDiscoverPS(ctx, client, switchScript, "hyperv")
	if switchErr != nil {
		return nil, switchErr
	}

	result := make(map[string][]Option)

	var switchOpts []Option
	for _, line := range splitLines(switchOut) {
		switchOpts = append(switchOpts, Option{Value: line})
	}
	if len(switchOpts) > 0 {
		result["hv_switch"] = switchOpts
	}

	// hv_vm_path — best-effort; don't fail discovery if this errors.
	vmPathScript := `(Get-VMHost).VirtualMachinePath`
	if vmPathOut, err := runDiscoverPS(ctx, client, vmPathScript, "hyperv"); err == nil {
		vmPathOut = strings.TrimSpace(vmPathOut)
		if vmPathOut != "" {
			result["hv_vm_path"] = []Option{{Value: vmPathOut}}
		}
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// VMware Workstation discovery
// ---------------------------------------------------------------------------

// WorkstationConnConfig is the subset of WorkstationConfig needed for discovery.
type WorkstationConnConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	HTTPS    bool
	Insecure bool
}

// workstationWinRMPort resolves the effective WinRM port.
func workstationWinRMPort(cfg WorkstationConnConfig) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	if cfg.HTTPS {
		return 5986
	}
	return 5985
}

// vmnetRe matches the "VMnetN" token inside a network adapter description.
// Case-insensitive; captures the vmnet name, e.g. "VMnet1" or "vmnet8".
var vmnetRe = regexp.MustCompile(`(?i)(vmnet\d+)`)

// extractVMnet extracts the normalised "vmnetN" identifier from an adapter
// description such as "VMware Virtual Ethernet Adapter for VMnet8". Returns
// the lowercase canonical form (e.g. "vmnet8") or "" if none found.
func extractVMnet(description string) string {
	m := vmnetRe.FindStringSubmatch(description)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// DiscoverWorkstation connects to a Windows host running VMware Workstation
// over WinRM and returns, best-effort:
//
//   - "ws_install_dir": VMware Workstation install path from the registry
//   - "ws_vnet":        distinct vmnetN names from VMware virtual adapters
//
// Running PowerShell also serves as the connection test. If the WinRM connect
// itself fails, that error is returned. Individual PS failures are silently
// swallowed (manual fallback).
func DiscoverWorkstation(ctx context.Context, cfg WorkstationConnConfig) (map[string][]Option, error) {
	if cfg.Host == "" || cfg.Username == "" {
		return nil, fmt.Errorf("workstation: Host and Username are required")
	}

	ep := winrm.NewEndpoint(
		cfg.Host,
		workstationWinRMPort(cfg),
		cfg.HTTPS,
		cfg.Insecure,
		nil, nil, nil,
		0,
	)
	client, err := winrm.NewClient(ep, cfg.Username, cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("workstation: build winrm client: %w", err)
	}

	result := make(map[string][]Option)

	// ws_install_dir — read from registry; try WOW6432Node first, fall back to
	// the non-WOW6432 path. Serves as the first (connection test) PS command.
	installScript := `
$p = (Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\VMware, Inc.\VMware Workstation' -ErrorAction SilentlyContinue).InstallPath
if (-not $p) { $p = (Get-ItemProperty 'HKLM:\SOFTWARE\VMware, Inc.\VMware Workstation' -ErrorAction SilentlyContinue).InstallPath }
if ($p) { $p.TrimEnd('\') }
`
	if installOut, err := runDiscoverPS(ctx, client, installScript, "workstation"); err == nil {
		installOut = strings.TrimSpace(installOut)
		if installOut != "" {
			result["ws_install_dir"] = []Option{{Value: installOut}}
		}
	} else {
		// If even the first PS call fails, WinRM connectivity is broken — surface.
		return nil, err
	}

	// ws_vnet — list VMware virtual network adapters and extract vmnetN names.
	vnetScript := `Get-NetAdapter | Where-Object { $_.InterfaceDescription -like '*VMware Virtual Ethernet*' } | Select-Object -ExpandProperty InterfaceDescription`
	if vnetOut, err := runDiscoverPS(ctx, client, vnetScript, "workstation"); err == nil {
		seen := make(map[string]bool)
		var opts []Option
		for _, line := range splitLines(vnetOut) {
			if vmnet := extractVMnet(line); vmnet != "" && !seen[vmnet] {
				seen[vmnet] = true
				opts = append(opts, Option{Value: vmnet})
			}
		}
		if len(opts) > 0 {
			result["ws_vnet"] = opts
		}
	}
	// Best-effort: vnet failure is silently swallowed; manual fallback applies.

	return result, nil
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// runDiscoverPS runs a PowerShell script via an already-built winrm.Client.
// On transport error or non-zero exit, it returns a prefixed error.
func runDiscoverPS(ctx context.Context, client *winrm.Client, script, prefix string) (string, error) {
	stdout, stderr, code, err := client.RunPSWithContext(ctx, script)
	if err != nil {
		return "", fmt.Errorf("%s: winrm transport: %w", prefix, err)
	}
	if code != 0 {
		return "", fmt.Errorf("%s: powershell exit %d: %s", prefix, code, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
