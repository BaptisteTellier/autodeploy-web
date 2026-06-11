// Package deploy orchestrates a multi-VM Veeam topology deployment (Layer 2):
// for each node it uploads an already-built ISO to the target hypervisor,
// creates the VM (with role-derived disks), attaches the ISO and sets the boot
// order. It deliberately stops short of powering on / wiring by default — those
// are gated by Spec.PowerOn and a later Veeam-REST wiring step.
//
// The ISOs are produced beforehand by the Wizard/job pipeline and selected as
// output folders in the UI — deploy never (re)builds an ISO. The orchestrator
// depends only on hypervisor.Hypervisor, so it is unit-testable with a mock.
package deploy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/hypervisor"
	"github.com/google/uuid"
)

// State is the coarse lifecycle of a deployment.
type State string

const (
	StatePending  State = "pending"
	StateRunning  State = "running"
	StateDone     State = "done"
	StateFailed   State = "failed"
	StateCanceled State = "canceled"
)

const maxBufferedLines = 5000

// NodeDeploy is one VM to create from a prebuilt ISO.
type NodeDeploy struct {
	Name        string // VM name (the appliance hostname baked into the ISO)
	Role        string // display label (VSA / VIA-Proxy / VIA-HR)
	ISOPath     string // absolute path to the already-built customised ISO
	Disks       []int  // disk sizes in GiB (role/config-derived)
	IP          string // static IP baked into the ISO (used by the wiring step)
	PairingCode string // appliance pairing/handshake code (default "000000")
}

// Wirer registers the booted appliances into the VSA after deployment (the
// Veeam-REST "wiring" step). Implemented by internal/wiring; nil = skip wiring.
type Wirer interface {
	Wire(ctx context.Context, nodes []NodeDeploy, log func(string)) error
}

// NodeStatus is the per-node progress within a deployment.
type NodeStatus struct {
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	Step     string `json:"step"`  // uploading | creating-vm | attaching | booting | ready | failed
	VMID     string `json:"vm_id"` // populated once the VM is created
	Error    string `json:"error,omitempty"`
}

// Spec is everything needed to run one deployment.
type Spec struct {
	Label   string       // topology label, for display (e.g. "vsa+proxy")
	Nodes   []NodeDeploy // ordered nodes (VSA first)
	HV      hypervisor.Hypervisor
	VM      hypervisor.VMSpec // base sizing (CPUs/MemoryMiB/Bridge/VLAN); Name+Disks set per node
	PowerOn bool              // power VMs on after attaching the ISO (default: false)
	Wirer   Wirer             // optional: wire the topology into the VSA after boot (nil = skip)
}

// Deployment is the tracked state of one running/finished deployment.
type Deployment struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	State      State        `json:"state"`
	Nodes      []NodeStatus `json:"nodes"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt time.Time    `json:"finished_at,omitempty"`
	Error      string       `json:"error,omitempty"`

	mu    sync.Mutex
	lines []string
	subs  []chan string
	done  chan struct{}
}

// View is a race-free snapshot of a Deployment's public fields.
type View struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	State      State        `json:"state"`
	Nodes      []NodeStatus `json:"nodes"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt time.Time    `json:"finished_at,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// View returns a snapshot safe to hand to HTTP handlers / templates.
func (d *Deployment) View() View {
	d.mu.Lock()
	defer d.mu.Unlock()
	nodes := make([]NodeStatus, len(d.Nodes))
	copy(nodes, d.Nodes)
	return View{
		ID:         d.ID,
		Kind:       d.Kind,
		State:      d.State,
		Nodes:      nodes,
		CreatedAt:  d.CreatedAt,
		FinishedAt: d.FinishedAt,
		Error:      d.Error,
	}
}

// Done is closed when the deployment finishes (done/failed/canceled).
func (d *Deployment) Done() <-chan struct{} { return d.done }

// AppendLine records a log line and fans it out to live subscribers.
func (d *Deployment) AppendLine(line string) {
	d.mu.Lock()
	if len(d.lines) >= maxBufferedLines {
		d.lines = d.lines[1:]
	}
	d.lines = append(d.lines, line)
	subs := append([]chan string(nil), d.subs...)
	d.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- line:
		default:
		}
	}
}

// Snapshot returns a copy of the buffered log lines.
func (d *Deployment) Snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.lines))
	copy(out, d.lines)
	return out
}

// Subscribe registers a live line channel and returns the buffered history plus
// a cancel func. The channel is closed when the deployment finishes.
func (d *Deployment) Subscribe(buf int) (history []string, ch chan string, cancel func()) {
	if buf <= 0 {
		buf = 64
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c := make(chan string, buf)
	d.subs = append(d.subs, c)
	hist := make([]string, len(d.lines))
	copy(hist, d.lines)
	cancel = func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for i, x := range d.subs {
			if x == c {
				d.subs = append(d.subs[:i], d.subs[i+1:]...)
				close(c)
				return
			}
		}
	}
	return hist, c, cancel
}

func (d *Deployment) closeSubs() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.subs {
		close(c)
	}
	d.subs = nil
}

func (d *Deployment) setState(s State) {
	d.mu.Lock()
	d.State = s
	d.mu.Unlock()
}

func (d *Deployment) setNode(i int, mutate func(*NodeStatus)) {
	d.mu.Lock()
	mutate(&d.Nodes[i])
	d.mu.Unlock()
}

// Manager owns the in-memory deployment registry.
type Manager struct {
	mu          sync.RWMutex
	deployments map[string]*Deployment
	wg          sync.WaitGroup
	stopCh      chan struct{}
}

// NewManager returns an empty deployment manager.
func NewManager() *Manager {
	return &Manager{
		deployments: make(map[string]*Deployment),
		stopCh:      make(chan struct{}),
	}
}

// Start validates the spec and launches the deployment asynchronously.
func (m *Manager) Start(spec Spec) (*Deployment, error) {
	if len(spec.Nodes) == 0 {
		return nil, fmt.Errorf("deploy: no nodes to deploy")
	}
	if spec.HV == nil {
		return nil, fmt.Errorf("deploy: hypervisor is required")
	}
	for i, n := range spec.Nodes {
		if n.ISOPath == "" {
			return nil, fmt.Errorf("deploy: node %d (%s) has no ISO selected", i, n.Name)
		}
	}

	d := &Deployment{
		ID:        uuid.NewString(),
		Kind:      spec.Label,
		State:     StatePending,
		CreatedAt: time.Now(),
		done:      make(chan struct{}),
	}
	d.Nodes = make([]NodeStatus, len(spec.Nodes))
	for i, n := range spec.Nodes {
		d.Nodes[i] = NodeStatus{Hostname: n.Name, Role: n.Role, Step: "queued"}
	}

	m.mu.Lock()
	m.deployments[d.ID] = d
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(d, spec)
	return d, nil
}

// run executes the deployment sequence node by node.
func (m *Manager) run(d *Deployment, spec Spec) {
	defer m.wg.Done()
	defer func() {
		d.mu.Lock()
		d.FinishedAt = time.Now()
		d.mu.Unlock()
		d.closeSubs()
		close(d.done)
	}()

	d.setState(StateRunning)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-m.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	for i, node := range spec.Nodes {
		if err := m.deployNode(ctx, d, i, node, spec); err != nil {
			d.setNode(i, func(ns *NodeStatus) { ns.Step = "failed"; ns.Error = err.Error() })
			d.AppendLine(fmt.Sprintf("[%s] FAILED: %v", node.Name, err))
			d.mu.Lock()
			d.Error = fmt.Sprintf("node %s: %v", node.Name, err)
			d.mu.Unlock()
			d.setState(StateFailed)
			return
		}
	}
	d.AppendLine("All nodes deployed.")

	// Optional wiring step: register the booted appliances into the VSA. Only
	// meaningful once the VMs are actually powered on.
	if spec.Wirer != nil && spec.PowerOn {
		d.AppendLine("Wiring topology into the VSA (Veeam REST)…")
		if err := spec.Wirer.Wire(ctx, spec.Nodes, d.AppendLine); err != nil {
			d.AppendLine(fmt.Sprintf("WIRING FAILED: %v", err))
			d.mu.Lock()
			d.Error = "wiring: " + err.Error()
			d.mu.Unlock()
			d.setState(StateFailed)
			return
		}
		d.AppendLine("Topology wired.")
	}
	d.setState(StateDone)
}

// deployNode runs the per-node sequence: upload ISO → create VM → attach ISO →
// set boot order → (optionally) power on.
func (m *Manager) deployNode(ctx context.Context, d *Deployment, i int, node NodeDeploy, spec Spec) error {
	host := node.Name

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "uploading" })
	d.AppendLine(fmt.Sprintf("[%s] uploading ISO %s…", host, node.ISOPath))
	isoRef, err := spec.HV.UploadISO(ctx, node.ISOPath)
	if err != nil {
		return fmt.Errorf("upload ISO: %w", err)
	}

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "creating-vm" })
	d.AppendLine(fmt.Sprintf("[%s] creating VM (%d disk(s))…", host, len(node.Disks)))
	vmSpec := spec.VM
	vmSpec.Name = host
	vmSpec.Disks = node.Disks
	vm, err := spec.HV.CreateVM(ctx, vmSpec)
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}
	d.setNode(i, func(ns *NodeStatus) { ns.VMID = vm.ID })

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "attaching" })
	d.AppendLine(fmt.Sprintf("[%s] attaching ISO %s to VM %s…", host, isoRef, vm.ID))
	if err := spec.HV.AttachISO(ctx, vm, isoRef); err != nil {
		return fmt.Errorf("attach ISO: %w", err)
	}
	if err := spec.HV.SetBootFromCD(ctx, vm); err != nil {
		return fmt.Errorf("set boot order: %w", err)
	}

	if spec.PowerOn {
		d.setNode(i, func(ns *NodeStatus) { ns.Step = "booting" })
		d.AppendLine(fmt.Sprintf("[%s] powering on VM %s…", host, vm.ID))
		if err := spec.HV.PowerOn(ctx, vm); err != nil {
			return fmt.Errorf("power on: %w", err)
		}
	}

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "ready" })
	d.AppendLine(fmt.Sprintf("[%s] ready (VM %s).", host, vm.ID))
	return nil
}

// Get returns a deployment by ID.
func (m *Manager) Get(id string) (*Deployment, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.deployments[id]
	return d, ok
}

// List returns snapshots of all deployments, newest first is not guaranteed.
func (m *Manager) List() []View {
	m.mu.RLock()
	ds := make([]*Deployment, 0, len(m.deployments))
	for _, d := range m.deployments {
		ds = append(ds, d)
	}
	m.mu.RUnlock()
	out := make([]View, len(ds))
	for i, d := range ds {
		out[i] = d.View()
	}
	return out
}

// Shutdown signals running deployments to cancel and waits (bounded by ctx).
func (m *Manager) Shutdown(ctx context.Context) {
	close(m.stopCh)
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
