// Package topology models multi-node Veeam deployment topologies — a VSA plus
// optional VIA proxy / hardened-repository appliances — on top of the existing
// single-VM config.Config. It maps the catalog of supported architectures to an
// ordered list of nodes, each carrying a role and a distinct network identity,
// and derives the per-node config.Config the existing job pipeline consumes.
package topology

import (
	"fmt"
	"net"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

// Role is the function a node plays in the topology.
type Role string

const (
	// RoleVSA is the Veeam Software Appliance — the VBR control plane.
	RoleVSA Role = "VSA"
	// RoleVIAProxy is a Veeam Infrastructure Appliance acting as proxy/gateway.
	RoleVIAProxy Role = "VIA-Proxy"
	// RoleVIAHR is a Veeam Infrastructure Appliance acting as a hardened repository.
	RoleVIAHR Role = "VIA-HR"
)

// applianceType maps a Role to the config.Config ApplianceType value consumed
// by autodeploy.ps1 (see internal/config defaults: VSA | VIA | VIAiscsi | VIAHR).
func (r Role) applianceType() string {
	switch r {
	case RoleVIAProxy:
		return "VIA"
	case RoleVIAHR:
		return "VIAHR"
	default:
		return "VSA"
	}
}

// Kind enumerates the supported catalog topologies (the a–f options).
type Kind string

const (
	// KindVSA is "a. VSA only".
	KindVSA Kind = "vsa"
	// KindVSAProxy is "b. VSA + VIA Proxy".
	KindVSAProxy Kind = "vsa+proxy"
	// KindVSAHR is "c. VSA + VIA Hardened Repository".
	KindVSAHR Kind = "vsa+hr"
	// KindVSAProxyHR is "d. VSA + VIA Proxy + VIA HR".
	KindVSAProxyHR Kind = "vsa+proxy+hr"
	// KindVSAHAHR is "e. VSA HA + VIA HR".
	KindVSAHAHR Kind = "vsa-ha+hr"
	// KindVSAHAProxyHR is "f. VSA HA + VIA Proxy + VIA HR".
	KindVSAHAProxyHR Kind = "vsa-ha+proxy+hr"

	// KindProxy is a single VIA Proxy added into an existing VBR (no VSA deployed).
	KindProxy Kind = "proxy"
	// KindHR is a single VIA Hardened Repository added into an existing VBR (no VSA deployed).
	KindHR Kind = "hr"
)

// AllKinds lists the catalog in display order (a–f + standalone kinds).
func AllKinds() []Kind {
	return []Kind{
		KindVSA,
		KindVSAProxy,
		KindVSAHR,
		KindVSAProxyHR,
		KindVSAHAHR,
		KindVSAHAProxyHR,
		KindProxy,
		KindHR,
	}
}

// IsStandalone reports whether this kind deploys only VIA nodes (no VSA),
// meaning it must wire into an existing VBR rather than one being deployed now.
func (k Kind) IsStandalone() bool {
	return k == KindProxy || k == KindHR
}

// NodeSpec is the role template for one node, independent of network identity.
type NodeSpec struct {
	Role Role
	HA   bool // VSA node participating in a high-availability cluster
}

// Catalog returns the ordered node templates for a Kind. VSA node(s) always
// come first — they are the control plane the VIA nodes register into. Returns
// nil for an unknown Kind.
func Catalog(k Kind) []NodeSpec {
	switch k {
	case KindVSA:
		return []NodeSpec{{Role: RoleVSA}}
	case KindVSAProxy:
		return []NodeSpec{{Role: RoleVSA}, {Role: RoleVIAProxy}}
	case KindVSAHR:
		return []NodeSpec{{Role: RoleVSA}, {Role: RoleVIAHR}}
	case KindVSAProxyHR:
		return []NodeSpec{{Role: RoleVSA}, {Role: RoleVIAProxy}, {Role: RoleVIAHR}}
	case KindVSAHAHR:
		return []NodeSpec{{Role: RoleVSA, HA: true}, {Role: RoleVSA, HA: true}, {Role: RoleVIAHR}}
	case KindVSAHAProxyHR:
		return []NodeSpec{{Role: RoleVSA, HA: true}, {Role: RoleVSA, HA: true}, {Role: RoleVIAProxy}, {Role: RoleVIAHR}}
	case KindProxy:
		return []NodeSpec{{Role: RoleVIAProxy}}
	case KindHR:
		return []NodeSpec{{Role: RoleVIAHR}}
	default:
		return nil
	}
}

// Identity is the per-node network + naming identity.
type Identity struct {
	Hostname   string
	StaticIP   string
	Subnet     string
	Gateway    string
	DNSServers []string
}

// Node is one VM to deploy: a role template plus its network identity.
type Node struct {
	Spec     NodeSpec
	Identity Identity
}

// Topology is an ordered set of nodes to deploy together.
type Topology struct {
	Kind  Kind
	Nodes []Node
}

// New builds a Topology for kind by zipping the catalog templates with the
// supplied per-node identities (same order). It errors if the identity count
// does not match the catalog.
func New(kind Kind, ids []Identity) (Topology, error) {
	specs := Catalog(kind)
	if specs == nil {
		return Topology{}, fmt.Errorf("unknown topology kind %q", kind)
	}
	if len(ids) != len(specs) {
		return Topology{}, fmt.Errorf("topology %q needs %d node identities, got %d", kind, len(specs), len(ids))
	}
	nodes := make([]Node, len(specs))
	for i := range specs {
		nodes[i] = Node{Spec: specs[i], Identity: ids[i]}
	}
	return Topology{Kind: kind, Nodes: nodes}, nil
}

// BuildConfig derives the per-node config.Config from a base config by applying
// the node's role and network identity. The base is typically config.Defaults()
// or a loaded preset; role and identity fields always override it.
func (n Node) BuildConfig(base config.Config) config.Config {
	c := base
	c.ApplianceType = n.Spec.Role.applianceType()
	c.HighAvailabilityEnabled = n.Spec.HA
	// A hardened repository uses a single-disk layout; VSA/proxy do not.
	c.VIASingleDisk = n.Spec.Role == RoleVIAHR

	id := n.Identity
	c.Hostname = id.Hostname
	c.UseDHCP = false
	c.StaticIP = id.StaticIP
	c.Subnet = id.Subnet
	c.Gateway = id.Gateway
	if id.DNSServers != nil {
		c.DNSServers = config.FlexStringArray(id.DNSServers)
	}
	return c
}

// Validate checks structural and identity constraints, returning all problems
// found (empty slice means valid).
func (t Topology) Validate() []error {
	specs := Catalog(t.Kind)
	if specs == nil {
		return []error{fmt.Errorf("unknown topology kind %q", t.Kind)}
	}

	var errs []error
	if len(t.Nodes) != len(specs) {
		errs = append(errs, fmt.Errorf("topology %q expects %d nodes, has %d", t.Kind, len(specs), len(t.Nodes)))
	}

	// HA implies exactly two VSA nodes.
	vsaHA := 0
	for _, n := range t.Nodes {
		if n.Spec.Role == RoleVSA && n.Spec.HA {
			vsaHA++
		}
	}
	if vsaHA != 0 && vsaHA != 2 {
		errs = append(errs, fmt.Errorf("HA topology needs exactly 2 VSA nodes, has %d", vsaHA))
	}

	seenHost := map[string]bool{}
	seenIP := map[string]bool{}
	for i, n := range t.Nodes {
		id := n.Identity
		if id.Hostname == "" {
			errs = append(errs, fmt.Errorf("node %d (%s): hostname required", i, n.Spec.Role))
		} else if seenHost[id.Hostname] {
			errs = append(errs, fmt.Errorf("duplicate hostname %q", id.Hostname))
		} else {
			seenHost[id.Hostname] = true
		}

		if id.StaticIP == "" {
			errs = append(errs, fmt.Errorf("node %d (%s): static IP required", i, n.Spec.Role))
		} else if net.ParseIP(id.StaticIP) == nil {
			errs = append(errs, fmt.Errorf("node %d (%s): invalid IP %q", i, n.Spec.Role, id.StaticIP))
		} else if seenIP[id.StaticIP] {
			errs = append(errs, fmt.Errorf("duplicate IP %q", id.StaticIP))
		} else {
			seenIP[id.StaticIP] = true
		}
	}
	return errs
}
