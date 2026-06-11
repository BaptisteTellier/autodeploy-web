package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/hypervisor"
)

// mockHV records the calls made to it, in order, and hands out sequential VM IDs.
// uploadErr, if set, makes UploadISO fail for the matching ISO path.
type mockHV struct {
	mu        sync.Mutex
	calls     []string
	nextID    int
	uploadErr string // ISO path that should fail to upload ("" = none)
}

func (h *mockHV) log(s string) { h.mu.Lock(); h.calls = append(h.calls, s); h.mu.Unlock() }

func (h *mockHV) UploadISO(_ context.Context, p string) (string, error) {
	h.log("upload:" + p)
	if h.uploadErr != "" && p == h.uploadErr {
		return "", fmt.Errorf("synthetic upload failure")
	}
	return "local:iso/" + p, nil
}
func (h *mockHV) CreateVM(_ context.Context, spec hypervisor.VMSpec) (hypervisor.VMRef, error) {
	h.mu.Lock()
	h.nextID++
	id := fmt.Sprintf("%d", 100+h.nextID)
	h.mu.Unlock()
	h.log(fmt.Sprintf("create:%s:disks=%v", spec.Name, spec.Disks))
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

func waitDone(t *testing.T, d *Deployment) {
	t.Helper()
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not finish in time")
	}
}

func twoNodes() []NodeDeploy {
	return []NodeDeploy{
		{Name: "vsa-01", Role: "VSA", ISOPath: "/out/a/vsa.iso", Disks: []int{256, 256}},
		{Name: "proxy-01", Role: "VIA-Proxy", ISOPath: "/out/b/via.iso", Disks: []int{128, 128}},
	}
}

func TestDeploySequenceHappyPath(t *testing.T) {
	hv := &mockHV{}
	m := NewManager()
	d, err := m.Start(Spec{
		Label:   "vsa+proxy",
		Nodes:   twoNodes(),
		HV:      hv,
		VM:      hypervisor.VMSpec{CPUs: 2, MemoryMiB: 4096, Bridge: "vmbr0"},
		PowerOn: false,
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
		if n.Step != "ready" || n.VMID == "" {
			t.Errorf("node %s = %+v, want ready with VMID", n.Hostname, n)
		}
	}
	want := []string{
		"upload:/out/a/vsa.iso", "create:vsa-01:disks=[256 256]", "attach:101:local:iso//out/a/vsa.iso", "bootcd:101",
		"upload:/out/b/via.iso", "create:proxy-01:disks=[128 128]", "attach:102:local:iso//out/b/via.iso", "bootcd:102",
	}
	if len(hv.calls) != len(want) {
		t.Fatalf("calls = %v\nwant %v", hv.calls, want)
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
	d, _ := m.Start(Spec{Nodes: twoNodes(), HV: hv, PowerOn: true})
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
	hv := &mockHV{uploadErr: "/out/a/vsa.iso"} // first node's upload fails
	m := NewManager()
	d, _ := m.Start(Spec{Nodes: twoNodes(), HV: hv})
	waitDone(t, d)
	v := d.View()
	if v.State != StateFailed {
		t.Fatalf("state = %q, want failed", v.State)
	}
	if v.Nodes[0].Step != "failed" || v.Nodes[0].Error == "" {
		t.Errorf("node0 = %+v, want failed with error", v.Nodes[0])
	}
	if v.Nodes[1].Step != "queued" {
		t.Errorf("node1 step = %q, want queued (deploy stops on first failure)", v.Nodes[1].Step)
	}
}

// mockWirer records whether/with-what it was called and can force an error.
type mockWirer struct {
	called bool
	nodes  []NodeDeploy
	err    error
}

func (w *mockWirer) Wire(_ context.Context, nodes []NodeDeploy, log func(string)) error {
	w.called = true
	w.nodes = nodes
	log("mock wiring")
	return w.err
}

func TestWirerRunsAfterDeployWhenPoweredOn(t *testing.T) {
	w := &mockWirer{}
	m := NewManager()
	d, _ := m.Start(Spec{Nodes: twoNodes(), HV: &mockHV{}, PowerOn: true, Wirer: w})
	waitDone(t, d)
	if d.View().State != StateDone {
		t.Fatalf("state = %q, want done", d.View().State)
	}
	if !w.called || len(w.nodes) != 2 {
		t.Errorf("wirer called=%v nodes=%d, want true/2", w.called, len(w.nodes))
	}
}

func TestWirerSkippedWhenNotPoweredOn(t *testing.T) {
	w := &mockWirer{}
	m := NewManager()
	d, _ := m.Start(Spec{Nodes: twoNodes(), HV: &mockHV{}, PowerOn: false, Wirer: w})
	waitDone(t, d)
	if w.called {
		t.Error("wirer must not run when PowerOn is false")
	}
}

func TestWirerFailureMarksDeploymentFailed(t *testing.T) {
	w := &mockWirer{err: fmt.Errorf("boom")}
	m := NewManager()
	d, _ := m.Start(Spec{Nodes: twoNodes(), HV: &mockHV{}, PowerOn: true, Wirer: w})
	waitDone(t, d)
	v := d.View()
	if v.State != StateFailed || !strings.Contains(v.Error, "wiring") {
		t.Errorf("state=%q err=%q, want failed with wiring error", v.State, v.Error)
	}
}

func TestStartValidation(t *testing.T) {
	m := NewManager()
	if _, err := m.Start(Spec{Nodes: nil, HV: &mockHV{}}); err == nil {
		t.Error("Start should reject an empty node list")
	}
	if _, err := m.Start(Spec{Nodes: []NodeDeploy{{Name: "x"}}, HV: &mockHV{}}); err == nil {
		t.Error("Start should reject a node with no ISO path")
	}
	if _, err := m.Start(Spec{Nodes: twoNodes(), HV: nil}); err == nil {
		t.Error("Start should reject a nil hypervisor")
	}
}
