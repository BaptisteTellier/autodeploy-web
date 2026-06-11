package deploy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/hypervisor"
	"github.com/BaptisteTellier/autodeploy-web/internal/topology"
)

// mockHV records the calls made to it, in order, and hands out sequential VM IDs.
type mockHV struct {
	mu     sync.Mutex
	calls  []string
	nextID int
}

func (h *mockHV) log(s string) { h.mu.Lock(); h.calls = append(h.calls, s); h.mu.Unlock() }

func (h *mockHV) UploadISO(_ context.Context, p string) (string, error) {
	h.log("upload:" + p)
	return "local:iso/" + p, nil
}
func (h *mockHV) CreateVM(_ context.Context, spec hypervisor.VMSpec) (hypervisor.VMRef, error) {
	h.mu.Lock()
	h.nextID++
	id := fmt.Sprintf("%d", 100+h.nextID)
	h.mu.Unlock()
	h.log("create:" + spec.Name)
	return hypervisor.VMRef{ID: id, Node: "test"}, nil
}
func (h *mockHV) AttachISO(_ context.Context, vm hypervisor.VMRef, ref string) error {
	h.log("attach:" + vm.ID + ":" + ref)
	return nil
}
func (h *mockHV) DetachISO(_ context.Context, vm hypervisor.VMRef) error {
	h.log("detach:" + vm.ID)
	return nil
}
func (h *mockHV) SetBootFromCD(_ context.Context, vm hypervisor.VMRef) error {
	h.log("bootcd:" + vm.ID)
	return nil
}
func (h *mockHV) SetBootFromDisk(_ context.Context, vm hypervisor.VMRef) error {
	h.log("bootdisk:" + vm.ID)
	return nil
}
func (h *mockHV) PowerOn(_ context.Context, vm hypervisor.VMRef) error {
	h.log("poweron:" + vm.ID)
	return nil
}
func (h *mockHV) PowerOff(_ context.Context, vm hypervisor.VMRef) error {
	h.log("poweroff:" + vm.ID)
	return nil
}
func (h *mockHV) Status(_ context.Context, vm hypervisor.VMRef) (hypervisor.PowerState, error) {
	return hypervisor.PowerOff, nil
}
func (h *mockHV) Destroy(_ context.Context, vm hypervisor.VMRef) error {
	h.log("destroy:" + vm.ID)
	return nil
}

// mockBuilder returns a deterministic ISO path; failAt>=0 fails that node index.
type mockBuilder struct {
	mu     sync.Mutex
	n      int
	failAt int
}

func (b *mockBuilder) BuildISO(_ context.Context, cfg config.Config, onLine func(string)) (string, error) {
	b.mu.Lock()
	idx := b.n
	b.n++
	b.mu.Unlock()
	onLine("building " + cfg.Hostname)
	if b.failAt >= 0 && idx == b.failAt {
		return "", fmt.Errorf("synthetic build failure")
	}
	return cfg.Hostname + ".iso", nil
}

func waitDone(t *testing.T, d *Deployment) {
	t.Helper()
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not finish in time")
	}
}

func vsaProxy(t *testing.T) topology.Topology {
	t.Helper()
	top, err := topology.New(topology.KindVSAProxy, []topology.Identity{
		{Hostname: "vsa-01", StaticIP: "10.0.0.10", Subnet: "255.255.255.0", Gateway: "10.0.0.1"},
		{Hostname: "proxy-01", StaticIP: "10.0.0.11", Subnet: "255.255.255.0", Gateway: "10.0.0.1"},
	})
	if err != nil {
		t.Fatalf("topology.New: %v", err)
	}
	return top
}

func TestDeploySequenceHappyPath(t *testing.T) {
	hv := &mockHV{}
	m := NewManager()
	d, err := m.Start(Spec{
		Topology: vsaProxy(t),
		Base:     config.Defaults(),
		HV:       hv,
		Builder:  &mockBuilder{failAt: -1},
		VM:       hypervisor.VMSpec{CPUs: 2, MemoryMiB: 4096, DiskGiB: 32, Bridge: "vmbr0"},
		PowerOn:  false,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, d)

	v := d.View()
	if v.State != StateDone {
		t.Fatalf("state = %q, want done (err=%q)", v.State, v.Error)
	}
	for _, n := range v.Nodes {
		if n.Step != "ready" {
			t.Errorf("node %s step = %q, want ready", n.Hostname, n.Step)
		}
		if n.VMID == "" {
			t.Errorf("node %s has no VMID", n.Hostname)
		}
	}
	// Expect: upload, create, attach, bootcd for each of the 2 nodes; no poweron.
	want := []string{
		"upload:vsa-01.iso", "create:vsa-01", "attach:101:local:iso/vsa-01.iso", "bootcd:101",
		"upload:proxy-01.iso", "create:proxy-01", "attach:102:local:iso/proxy-01.iso", "bootcd:102",
	}
	if len(hv.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", hv.calls, want)
	}
	for i := range want {
		if hv.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, hv.calls[i], want[i])
		}
	}
}

func TestDeployPowerOnInvokesBoot(t *testing.T) {
	hv := &mockHV{}
	m := NewManager()
	d, _ := m.Start(Spec{
		Topology: vsaProxy(t),
		Base:     config.Defaults(),
		HV:       hv,
		Builder:  &mockBuilder{failAt: -1},
		PowerOn:  true,
	})
	waitDone(t, d)
	poweron := 0
	for _, c := range hv.calls {
		if len(c) >= 8 && c[:8] == "poweron:" {
			poweron++
		}
	}
	if poweron != 2 {
		t.Errorf("poweron calls = %d, want 2", poweron)
	}
}

func TestDeployStopsOnNodeFailure(t *testing.T) {
	hv := &mockHV{}
	m := NewManager()
	d, _ := m.Start(Spec{
		Topology: vsaProxy(t),
		Base:     config.Defaults(),
		HV:       hv,
		Builder:  &mockBuilder{failAt: 0}, // first node's ISO build fails
		PowerOn:  false,
	})
	waitDone(t, d)
	v := d.View()
	if v.State != StateFailed {
		t.Fatalf("state = %q, want failed", v.State)
	}
	if v.Nodes[0].Step != "failed" || v.Nodes[0].Error == "" {
		t.Errorf("node0 = %+v, want failed with error", v.Nodes[0])
	}
	// Second node must never have been touched.
	if v.Nodes[1].Step != "queued" {
		t.Errorf("node1 step = %q, want queued (deploy should stop on first failure)", v.Nodes[1].Step)
	}
	if len(hv.calls) != 0 {
		t.Errorf("hypervisor should not be called when the first ISO build fails, got %v", hv.calls)
	}
}

func TestStartRejectsInvalidTopology(t *testing.T) {
	m := NewManager()
	bad := topology.Topology{Kind: topology.KindVSA, Nodes: nil} // 0 nodes, expects 1
	if _, err := m.Start(Spec{Topology: bad, HV: &mockHV{}, Builder: &mockBuilder{failAt: -1}}); err == nil {
		t.Error("Start should reject an invalid topology")
	}
}
