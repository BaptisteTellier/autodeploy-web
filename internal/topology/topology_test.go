package topology

import (
	"testing"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

func TestCatalogNodeCounts(t *testing.T) {
	want := map[Kind]int{
		KindVSA:          1,
		KindVSAProxy:     2,
		KindVSAHR:        2,
		KindVSAProxyHR:   3,
		KindVSAHAHR:      3,
		KindVSAHAProxyHR: 4,
	}
	for k, n := range want {
		if got := len(Catalog(k)); got != n {
			t.Errorf("Catalog(%q): got %d nodes, want %d", k, got, n)
		}
	}
	if Catalog("nope") != nil {
		t.Error("Catalog(unknown) should return nil")
	}
	if len(AllKinds()) != len(want) {
		t.Errorf("AllKinds() = %d kinds, want %d", len(AllKinds()), len(want))
	}
}

func TestHAKindsHaveTwoVSANodes(t *testing.T) {
	for _, k := range []Kind{KindVSAHAHR, KindVSAHAProxyHR} {
		ha := 0
		for _, s := range Catalog(k) {
			if s.Role == RoleVSA && s.HA {
				ha++
			}
		}
		if ha != 2 {
			t.Errorf("%q: want 2 HA VSA nodes, got %d", k, ha)
		}
	}
}

func TestBuildConfigAppliesRoleAndIdentity(t *testing.T) {
	n := Node{
		Spec: NodeSpec{Role: RoleVIAHR},
		Identity: Identity{
			Hostname:   "hr-01",
			StaticIP:   "10.0.0.5",
			Subnet:     "255.255.255.0",
			Gateway:    "10.0.0.1",
			DNSServers: []string{"10.0.0.2"},
		},
	}
	c := n.BuildConfig(config.Defaults())

	if c.ApplianceType != "VIAHR" {
		t.Errorf("ApplianceType = %q, want VIAHR", c.ApplianceType)
	}
	if !c.VIASingleDisk {
		t.Error("VIAHR node should set VIASingleDisk")
	}
	if c.UseDHCP {
		t.Error("static identity should force UseDHCP=false")
	}
	if c.Hostname != "hr-01" || c.StaticIP != "10.0.0.5" || c.Gateway != "10.0.0.1" {
		t.Errorf("identity not applied: %+v", c)
	}
	if len(c.DNSServers) != 1 || c.DNSServers[0] != "10.0.0.2" {
		t.Errorf("DNSServers = %v, want [10.0.0.2]", c.DNSServers)
	}
}

func TestBuildConfigVSANotSingleDisk(t *testing.T) {
	n := Node{Spec: NodeSpec{Role: RoleVSA, HA: true}, Identity: Identity{Hostname: "vsa-01", StaticIP: "10.0.0.10"}}
	c := n.BuildConfig(config.Defaults())
	if c.ApplianceType != "VSA" {
		t.Errorf("ApplianceType = %q, want VSA", c.ApplianceType)
	}
	if !c.HighAvailabilityEnabled {
		t.Error("HA node should set HighAvailabilityEnabled")
	}
	if c.VIASingleDisk {
		t.Error("VSA must not set VIASingleDisk")
	}
}

func TestNewRejectsBadIdentityCount(t *testing.T) {
	if _, err := New(KindVSAProxy, []Identity{{Hostname: "x", StaticIP: "10.0.0.1"}}); err == nil {
		t.Error("New should reject a wrong identity count")
	}
	if _, err := New("bogus", nil); err == nil {
		t.Error("New should reject an unknown kind")
	}
}

func TestValidate(t *testing.T) {
	good := []Identity{
		{Hostname: "vsa-01", StaticIP: "10.0.0.10", Subnet: "255.255.255.0", Gateway: "10.0.0.1"},
		{Hostname: "proxy-01", StaticIP: "10.0.0.11", Subnet: "255.255.255.0", Gateway: "10.0.0.1"},
	}
	top, err := New(KindVSAProxy, good)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if errs := top.Validate(); len(errs) != 0 {
		t.Fatalf("Validate(good) = %v, want none", errs)
	}

	// Duplicate IP must be flagged.
	dup := []Identity{
		good[0],
		{Hostname: "proxy-01b", StaticIP: "10.0.0.10", Subnet: "255.255.255.0", Gateway: "10.0.0.1"},
	}
	top2, _ := New(KindVSAProxy, dup)
	if errs := top2.Validate(); len(errs) == 0 {
		t.Error("Validate should flag a duplicate IP")
	}

	// Invalid IP must be flagged.
	bad := []Identity{
		{Hostname: "vsa-01", StaticIP: "not-an-ip"},
	}
	top3, _ := New(KindVSA, bad)
	if errs := top3.Validate(); len(errs) == 0 {
		t.Error("Validate should flag an invalid IP")
	}
}
