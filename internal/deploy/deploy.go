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
	"path/filepath"
	"strings"
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

// NodeDeploy is one VM to create from a prebuilt ISO — or, in remote-kickstart
// mode, from an original Veeam ISO plus a kickstart URL injected into GRUB.
type NodeDeploy struct {
	Name        string // VM name (the appliance hostname baked into the ISO/cfg)
	Role        string // display label (VSA / VIA-Proxy / VIA-HR)
	ISOPath     string // absolute path to the already-built customised ISO ("" in kickstart mode)
	Disks       []int  // disk sizes in GiB (role/config-derived)
	IP          string // static IP baked into the ISO (used by the wiring step)
	PairingCode string // appliance pairing/handshake code (default "000000")

	// Remote kickstart (both set => kickstart mode for this node):
	KSUrl       string // kickstart URL (…/media/output/<job>/<file>.cfg/content)
	BaseISOPath string // local path of the ORIGINAL Veeam ISO (uploaded only if absent from the library)
	BootCommand string // optional override of the GRUB boot command (one line per row); "" = role default
}

// kickstart reports whether this node deploys via remote kickstart.
func (n NodeDeploy) kickstart() bool { return n.KSUrl != "" }

// Wirer registers the booted appliances into the VSA after deployment (the
// Veeam-REST "wiring" step). Implemented by internal/wiring; nil = skip wiring.
type Wirer interface {
	Wire(ctx context.Context, nodes []NodeDeploy, log func(string)) error
}

// NodeStatus is the per-node progress within a deployment.
type NodeStatus struct {
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	Step     string `json:"step"`     // uploading | creating-vm | attaching | booting | ready | failed
	VMID     string `json:"vm_id"`    // populated once the VM is created
	Progress int    `json:"progress"` // 0–100: ISO upload percent (only meaningful during "uploading")
	Error    string `json:"error,omitempty"`
}

// progressPrefix marks a log line as a structured upload-progress event rather
// than human-readable text. The SSE handler routes these to a "progress" event
// (and the client renders a bar) instead of printing them to the log. Riding
// the existing log channel avoids a second fan-out path.
const progressPrefix = "\x00progress\x00"

// ProgressLine formats a progress event for a node index and percent.
func ProgressLine(node, pct int) string {
	return fmt.Sprintf("%s%d %d", progressPrefix, node, pct)
}

// ParseProgressLine reports whether line is a progress event and, if so, the
// node index and percent it carries.
func ParseProgressLine(line string) (node, pct int, ok bool) {
	if !strings.HasPrefix(line, progressPrefix) {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(line[len(progressPrefix):], "%d %d", &node, &pct); err != nil {
		return 0, 0, false
	}
	return node, pct, true
}

// Spec is everything needed to run one deployment.
type Spec struct {
	Label   string       // topology label, for display (e.g. "vsa+proxy")
	Nodes   []NodeDeploy // ordered nodes (VSA first)
	HV      hypervisor.Hypervisor
	VM      hypervisor.VMSpec // base sizing (CPUs/MemoryMiB/Bridge/VLAN); Name+Disks set per node
	PowerOn bool              // power VMs on after attaching the ISO (default: false)
	Wirer   Wirer             // optional: wire the topology into the VSA after boot (nil = skip)

	// WireTimeout bounds the whole wiring step (waiting for the appliances to
	// install/boot + the REST registrations). 0 = DefaultWireTimeout.
	WireTimeout time.Duration

	// BootWait is how long to wait after power-on before typing the GRUB boot
	// command (kickstart mode). 0 = DefaultBootWait.
	BootWait time.Duration
}

// DefaultWireTimeout caps the wiring step so it never waits forever.
const DefaultWireTimeout = 45 * time.Minute

// DefaultBootWait gives GRUB time to appear before keystrokes are sent.
const DefaultBootWait = 8 * time.Second

// Deployment is the tracked state of one running/finished deployment.
type Deployment struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	State      State        `json:"state"`
	Nodes      []NodeStatus `json:"nodes"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt time.Time    `json:"finished_at,omitempty"`
	Error      string       `json:"error,omitempty"`

	mu       sync.Mutex
	lines    []string
	subs     []chan string
	done     chan struct{}
	hv       hypervisor.Hypervisor // retained so Remove can destroy the created VMs
	cancel   context.CancelFunc    // cancels the run goroutine (set once running)
	canceled bool                  // true when a STOP was requested (vs. a real failure)
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

// emit fans a line out to live subscribers WITHOUT buffering it. Used for
// ephemeral progress events: they drive the live upload bar but must not enter
// the replayable log (otherwise the static render on page reload dumps the raw
// sentinel lines into the log view).
func (d *Deployment) emit(line string) {
	d.mu.Lock()
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

// isCanceled reports whether a STOP was requested for this deployment.
func (d *Deployment) isCanceled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.canceled
}

func (d *Deployment) setNode(i int, mutate func(*NodeStatus)) {
	d.mu.Lock()
	mutate(&d.Nodes[i])
	d.mu.Unlock()
}

// uploadProgress returns a hypervisor.ProgressFunc that records the node's
// upload percent (for the live bar) and emits a human log line every 25%. The
// percent rides the log channel as a structured progress event (see
// ProgressLine); it is throttled upstream by the hypervisor's progressReader.
func (d *Deployment) uploadProgress(i int, host string) hypervisor.ProgressFunc {
	lastPct, lastMilestone := -1, -1
	return func(done, total int64) {
		pct := 0
		if total > 0 {
			pct = int(done * 100 / total)
		}
		if pct == lastPct {
			return
		}
		lastPct = pct
		d.setNode(i, func(ns *NodeStatus) { ns.Progress = pct })
		d.emit(ProgressLine(i, pct)) // ephemeral: live bar only, never buffered
		if m := pct / 25; m > lastMilestone {
			lastMilestone = m
			d.AppendLine(fmt.Sprintf("[%s] uploading… %d%% (%s / %s)", host, pct, humanGiB(done), humanGiB(total)))
		}
	}
}

// humanGiB renders a byte count as a compact GiB string (e.g. "6.4 GB").
func humanGiB(b int64) string {
	return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
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
		if n.kickstart() {
			if n.BaseISOPath == "" {
				return nil, fmt.Errorf("deploy: node %d (%s) is kickstart but has no base ISO", i, n.Name)
			}
		} else if n.ISOPath == "" {
			return nil, fmt.Errorf("deploy: node %d (%s) has no ISO selected", i, n.Name)
		}
	}

	d := &Deployment{
		ID:        uuid.NewString(),
		Kind:      spec.Label,
		State:     StatePending,
		CreatedAt: time.Now(),
		done:      make(chan struct{}),
		hv:        spec.HV,
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
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	go func() {
		select {
		case <-m.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	for i, node := range spec.Nodes {
		if err := m.deployNode(ctx, d, i, node, spec); err != nil {
			if d.isCanceled() {
				d.setNode(i, func(ns *NodeStatus) { ns.Step = "canceled" })
				d.AppendLine("Deployment stopped by user.")
				d.setState(StateCanceled)
				return
			}
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
	// meaningful once the VMs are actually powered on. Bounded by WireTimeout
	// so it can never wait forever.
	if spec.Wirer != nil && spec.PowerOn {
		wireTimeout := spec.WireTimeout
		if wireTimeout <= 0 {
			wireTimeout = DefaultWireTimeout
		}
		wctx, wcancel := context.WithTimeout(ctx, wireTimeout)
		defer wcancel()
		d.AppendLine(fmt.Sprintf("Wiring topology into the VSA (Veeam REST, timeout %s)…", wireTimeout))
		if err := spec.Wirer.Wire(wctx, spec.Nodes, d.AppendLine); err != nil {
			if d.isCanceled() {
				d.AppendLine("Deployment stopped by user.")
				d.setState(StateCanceled)
				return
			}
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

// deployNode runs the per-node sequence: resolve/upload the ISO → create VM →
// attach ISO → set boot order → (optionally) power on → (kickstart mode) type
// the GRUB boot command pointing at the node's kickstart URL.
func (m *Manager) deployNode(ctx context.Context, d *Deployment, i int, node NodeDeploy, spec Spec) error {
	host := node.Name

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "uploading"; ns.Progress = 0 })
	onProgress := d.uploadProgress(i, host)
	var isoRef string
	var err error
	if node.kickstart() {
		// Original Veeam ISO: reuse the library copy when present, upload once
		// otherwise (shared by every node of the same role).
		name := filepath.Base(node.BaseISOPath)
		isoRef, err = spec.HV.FindISO(ctx, name)
		if err != nil {
			return fmt.Errorf("find base ISO: %w", err)
		}
		if isoRef == "" {
			d.AppendLine(fmt.Sprintf("[%s] base ISO %s not in library — uploading…", host, name))
			isoRef, err = spec.HV.UploadISO(ctx, node.BaseISOPath, onProgress)
			if err != nil {
				return fmt.Errorf("upload base ISO: %w", err)
			}
		} else {
			d.AppendLine(fmt.Sprintf("[%s] base ISO already in library: %s", host, isoRef))
		}
	} else {
		d.AppendLine(fmt.Sprintf("[%s] uploading ISO %s…", host, node.ISOPath))
		isoRef, err = spec.HV.UploadISO(ctx, node.ISOPath, onProgress)
		if err != nil {
			return fmt.Errorf("upload ISO: %w", err)
		}
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

	if spec.PowerOn || node.kickstart() {
		d.setNode(i, func(ns *NodeStatus) { ns.Step = "booting" })
		d.AppendLine(fmt.Sprintf("[%s] powering on VM %s…", host, vm.ID))
		if err := spec.HV.PowerOn(ctx, vm); err != nil {
			return fmt.Errorf("power on: %w", err)
		}
	}

	if node.kickstart() {
		bootWait := spec.BootWait
		if bootWait <= 0 {
			bootWait = DefaultBootWait
		}
		d.setNode(i, func(ns *NodeStatus) { ns.Step = "kickstarting" })
		d.AppendLine(fmt.Sprintf("[%s] waiting %s for GRUB, then typing the boot command (inst.ks=%s)…", host, bootWait, node.KSUrl))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bootWait):
		}
		keys := BootCommandKeys(node.Role, node.KSUrl)
		if node.BootCommand != "" {
			keys = BootCommandKeysFromText(node.BootCommand) // user-edited override
		}
		if err := spec.HV.SendKeys(ctx, vm, keys); err != nil {
			return fmt.Errorf("type boot command: %w", err)
		}
	}

	// Honest final state: a powered-on node is still INSTALLING (the unattended
	// install runs after boot); only a node left off is merely "created". We
	// never claim "ready" here — readiness is what the wiring step waits for.
	final, msg := "created", "created (powered off)"
	if spec.PowerOn || node.kickstart() {
		final, msg = "installing", "booted — OS installing"
	}
	d.setNode(i, func(ns *NodeStatus) { ns.Step = final })
	d.AppendLine(fmt.Sprintf("[%s] %s (VM %s).", host, msg, vm.ID))
	return nil
}

// Get returns a deployment by ID.
func (m *Manager) Get(id string) (*Deployment, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.deployments[id]
	return d, ok
}

// Cancel requests a running deployment to stop. It cancels the run context so
// the orchestration goroutine unwinds at the next cancellation point and the
// deployment lands in StateCanceled. No-op for an unknown or finished
// deployment. Created VMs are left in place — use Remove to also destroy them.
func (m *Manager) Cancel(id string) bool {
	d, ok := m.Get(id)
	if !ok {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.canceled {
		return true
	}
	d.canceled = true
	if d.cancel != nil {
		d.cancel()
	}
	return true
}

// Remove stops the deployment (if still running), destroys every VM it created
// on the target hypervisor (Destroy powers the VM off first), and drops it from
// the registry. It returns the number of VMs destroyed. The work is bounded by
// ctx. The deployment is removed from the registry even if some VMs fail to
// destroy, so a partially-failed removal can be retried manually on the
// hypervisor.
func (m *Manager) Remove(ctx context.Context, id string) (int, error) {
	d, ok := m.Get(id)
	if !ok {
		return 0, fmt.Errorf("deploy: deployment %q not found", id)
	}

	// Stop the run first and wait (bounded) for the goroutine to unwind so we
	// don't race against in-flight VM creation.
	m.Cancel(id)
	select {
	case <-d.Done():
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}

	v := d.View()
	var errs []string
	removed := 0
	if d.hv != nil {
		for _, n := range v.Nodes {
			if n.VMID == "" {
				continue // never got created
			}
			if err := d.hv.Destroy(ctx, hypervisor.VMRef{ID: n.VMID}); err != nil {
				errs = append(errs, fmt.Sprintf("VM %s: %v", n.VMID, err))
				continue
			}
			removed++
		}
	}

	m.mu.Lock()
	delete(m.deployments, id)
	m.mu.Unlock()

	if len(errs) > 0 {
		return removed, fmt.Errorf("destroyed %d VM(s); errors: %s", removed, strings.Join(errs, "; "))
	}
	return removed, nil
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
