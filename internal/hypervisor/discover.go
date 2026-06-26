package hypervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/luthermonson/go-proxmox"
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
