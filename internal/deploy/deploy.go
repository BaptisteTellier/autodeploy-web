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
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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
	StateRemoved  State = "removed" // VMs destroyed; record kept so it can be retried
)

const maxBufferedLines = 5000

// NodeDeploy is one VM to create from a prebuilt ISO — or, in remote-kickstart
// mode, from an original Veeam ISO plus a kickstart URL injected into GRUB.
type NodeDeploy struct {
	Name        string // VM name (the appliance hostname baked into the ISO/cfg)
	Role        string // display label (VSA / VIA-Proxy / VIA-HR)
	CPUs        int    // per-node vCPU count; 0 = use Spec.VM.CPUs
	MemoryMiB   int    // per-node RAM in MiB; 0 = use Spec.VM.MemoryMiB
	SingleDisk  bool   // VIA only: adds inst.vsingledisk to the generated GRUB boot line
	ISOPath     string // absolute path to the already-built customised ISO ("" in kickstart mode)
	Disks       []int  // disk sizes in GiB (role/config-derived)
	IP          string // static IP baked into the ISO; "" = DHCP (wiring resolves it via GetVMIP)
	PairingCode string // appliance pairing/handshake code (default "000000")

	// Remote kickstart (both set => kickstart mode for this node):
	KSUrl       string // kickstart URL (…/media/output/<job>/<file>.cfg/content)
	BaseISOPath string // local path of the ORIGINAL Veeam ISO (uploaded only if absent from the library)
	BootCommand string // optional override of the GRUB boot command (one line per row); "" = role default

	// Ref is populated by the orchestrator after VM creation. Used by the
	// DHCP IP-resolution step to query the hypervisor guest agent.
	Ref hypervisor.VMRef
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

// nodeStatusPrefix marks a log line as a structured per-node status change
// (step / VM id / error) rather than human-readable text. The SSE handler
// routes these to a "node" event so the detail page updates the step badge and
// VM id live, without a full page reload. Like progress events, these ride the
// log channel but are emitted ephemerally (never buffered).
const nodeStatusPrefix = "\x00node\x00"

// NodeStatusLine encodes a node's current status as a structured event line.
func NodeStatusLine(idx int, ns NodeStatus) string {
	b, _ := json.Marshal(struct {
		Idx   int    `json:"i"`
		Step  string `json:"step"`
		VMID  string `json:"vm_id"`
		Error string `json:"error,omitempty"`
	}{idx, ns.Step, ns.VMID, ns.Error})
	return nodeStatusPrefix + string(b)
}

// ParseNodeStatusLine reports whether line is a node-status event and, if so,
// returns its JSON payload (forwarded verbatim as the SSE event data).
func ParseNodeStatusLine(line string) (payload string, ok bool) {
	if !strings.HasPrefix(line, nodeStatusPrefix) {
		return "", false
	}
	return line[len(nodeStatusPrefix):], true
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

// FormSnapshot captures the non-secret deploy-form inputs so a finished
// deployment can be copied back into the form ("Copy to deploy"). Passwords and
// token secrets are deliberately NOT captured.
type FormSnapshot struct {
	Kind        string            `json:"kind"`
	Provider    string            `json:"provider"`
	RemoteKS    bool              `json:"remote_ks"`
	Wire        bool              `json:"wire"`
	PowerOn     bool              `json:"power_on"`
	NodeOutputs []string          `json:"node_outputs"`         // per slot: chosen output job id
	NodeBoots   []string          `json:"node_boots,omitempty"` // per slot: edited GRUB bootcmd ("" if default)
	Text        map[string]string `json:"text"`                 // text/number/select inputs by form name
	Checks      map[string]bool   `json:"checks"`               // checkbox inputs by form name (e.g. *_insecure, hv_https)
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

	// Form is the form snapshot captured at submission time; stored on the
	// Deployment so a finished run can be copied back into the deploy form.
	Form FormSnapshot
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
	spec     Spec                  // retained so a removed deployment can be retried
	form     FormSnapshot          // non-secret form snapshot for "Copy to deploy"
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
	HasWirer   bool         `json:"has_wirer"` // true when the deployment had wiring configured
	Form       FormSnapshot `json:"form"`      // non-secret form snapshot for "Copy to deploy"
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
		HasWirer:   d.spec.Wirer != nil,
		Form:       d.form,
	}
}

// Done is closed when the deployment finishes (done/failed/canceled).
func (d *Deployment) Done() <-chan struct{} { return d.done }

// AppendLine records a log line and fans it out to live subscribers.
// The timestamp is prepended here — at the single choke point shared by both
// the buffered history (replayed on page reload) and the live SSE stream —
// so every viewer always sees the same wall-clock prefix regardless of when
// they (re)connect. Structured sentinel events (progress, node-status) travel
// through emit, never through AppendLine, so they are never timestamped.
func (d *Deployment) AppendLine(line string) {
	line = time.Now().Format("15:04:05") + " " + line
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
	ns := d.Nodes[i]
	d.mu.Unlock()
	// Stream the new status so the detail page updates the step badge / VM id
	// live (ephemeral: the authoritative state is re-rendered on reload).
	d.emit(NodeStatusLine(i, ns))
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
		spec:      spec,
		form:      spec.Form,
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
		ref, err := m.deployNode(ctx, d, i, node, spec)
		if err != nil {
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
		spec.Nodes[i].Ref = ref
		// Persist VMRef in d.spec so a wire-only retry can query GetVMIP.
		d.mu.Lock()
		d.spec.Nodes[i].Ref = ref
		d.mu.Unlock()
	}
	d.AppendLine("All nodes deployed.")

	// DHCP IP resolution: if wiring is requested and any node has no static IP,
	// poll the hypervisor guest agent until all IPs are known. Bounded by
	// WireTimeout so it can never wait forever.
	if spec.Wirer != nil && spec.PowerOn {
		for i := range spec.Nodes {
			if spec.Nodes[i].IP == "" && spec.Nodes[i].Ref.ID != "" {
				d.AppendLine(fmt.Sprintf("[%s] DHCP — polling hypervisor for IP…", spec.Nodes[i].Name))
			}
		}
		if err := m.resolveNodeIPs(ctx, d, spec); err != nil {
			if d.isCanceled() {
				d.AppendLine("Deployment stopped by user.")
				d.setState(StateCanceled)
				return
			}
			d.AppendLine(fmt.Sprintf("IP RESOLUTION FAILED: %v", err))
			d.mu.Lock()
			d.Error = "ip resolution: " + err.Error()
			d.mu.Unlock()
			d.setState(StateFailed)
			return
		}
		// Persist resolved IPs in d.spec so a wire-only retry uses fresh values.
		d.mu.Lock()
		for i, n := range spec.Nodes {
			if n.IP != "" {
				d.spec.Nodes[i].IP = n.IP
			}
		}
		d.mu.Unlock()
	}

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
// It returns the VMRef of the created VM so the caller can store it for later
// DHCP IP resolution via GetVMIP.
func (m *Manager) deployNode(ctx context.Context, d *Deployment, i int, node NodeDeploy, spec Spec) (hypervisor.VMRef, error) {
	host := node.Name

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "uploading"; ns.Progress = 0 })
	onProgress := d.uploadProgress(i, host)
	var isoRef string
	var err error
	var zero hypervisor.VMRef
	if node.kickstart() {
		// Original Veeam ISO: reuse the library copy when present, upload once
		// otherwise (shared by every node of the same role).
		name := filepath.Base(node.BaseISOPath)
		isoRef, err = spec.HV.FindISO(ctx, name)
		if err != nil {
			return zero, fmt.Errorf("find base ISO: %w", err)
		}
		if isoRef == "" {
			d.AppendLine(fmt.Sprintf("[%s] base ISO %s not in library — uploading…", host, name))
			isoRef, err = spec.HV.UploadISO(ctx, node.BaseISOPath, onProgress)
			if err != nil {
				return zero, fmt.Errorf("upload base ISO: %w", err)
			}
		} else {
			d.AppendLine(fmt.Sprintf("[%s] base ISO already in library: %s", host, isoRef))
		}
	} else {
		d.AppendLine(fmt.Sprintf("[%s] uploading ISO %s…", host, node.ISOPath))
		isoRef, err = spec.HV.UploadISO(ctx, node.ISOPath, onProgress)
		if err != nil {
			return zero, fmt.Errorf("upload ISO: %w", err)
		}
	}

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "creating-vm" })
	d.AppendLine(fmt.Sprintf("[%s] creating VM (%d disk(s))…", host, len(node.Disks)))
	vmSpec := spec.VM
	vmSpec.Name = host
	vmSpec.Disks = node.Disks
	if node.CPUs > 0 {
		vmSpec.CPUs = node.CPUs
	}
	if node.MemoryMiB > 0 {
		vmSpec.MemoryMiB = node.MemoryMiB
	}
	vm, err := spec.HV.CreateVM(ctx, vmSpec)
	if err != nil {
		return zero, fmt.Errorf("create VM: %w", err)
	}
	d.setNode(i, func(ns *NodeStatus) { ns.VMID = vm.ID })

	d.setNode(i, func(ns *NodeStatus) { ns.Step = "attaching" })
	d.AppendLine(fmt.Sprintf("[%s] attaching ISO %s to VM %s…", host, isoRef, vm.ID))
	if err := spec.HV.AttachISO(ctx, vm, isoRef); err != nil {
		return zero, fmt.Errorf("attach ISO: %w", err)
	}
	// Disk first, CD second: on a blank disk the firmware falls through to the
	// CD installer automatically. After install the disk has an OS and boots
	// directly — no runtime boot-order change is needed after PowerOn.
	// DetachISO is intentionally NOT called — QEMU reads the ISO on demand
	// throughout the Anaconda installation; ejecting mid-install aborts it.
	if err := spec.HV.SetBootDiskThenCD(ctx, vm); err != nil {
		return zero, fmt.Errorf("set boot order: %w", err)
	}

	if spec.PowerOn || node.kickstart() {
		d.setNode(i, func(ns *NodeStatus) { ns.Step = "booting" })
		d.AppendLine(fmt.Sprintf("[%s] powering on VM %s…", host, vm.ID))
		if err := spec.HV.PowerOn(ctx, vm); err != nil {
			return zero, fmt.Errorf("power on: %w", err)
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
			return zero, ctx.Err()
		case <-time.After(bootWait):
		}
		keys := BootCommandKeys(node.Role, node.KSUrl, node.SingleDisk)
		if node.BootCommand != "" {
			keys = BootCommandKeysFromText(node.BootCommand) // user-edited override
		}
		if err := spec.HV.SendKeys(ctx, vm, keys); err != nil {
			return zero, fmt.Errorf("type boot command: %w", err)
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
	return vm, nil
}

// resolveNodeIPs resolves DHCP IPs for every node in spec whose IP is "".
// It polls spec.HV.GetVMIP in parallel (one goroutine per node) until all IPs
// are known or ctx is done. On success the IPs are written back into spec.Nodes.
func (m *Manager) resolveNodeIPs(ctx context.Context, d *Deployment, spec Spec) error {
	type result struct {
		idx int
		ip  string
	}
	// Collect nodes that need resolution.
	var pending []int
	for i, n := range spec.Nodes {
		if n.IP == "" && n.Ref.ID != "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	resultCh := make(chan result, len(pending))
	errCh := make(chan error, 1)

	for _, idx := range pending {
		idx := idx
		node := spec.Nodes[idx]
		go func() {
			for {
				ip, err := spec.HV.GetVMIP(ctx, node.Ref)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("node %s: GetVMIP: %w", node.Name, err):
					default:
					}
					return
				}
				if ip != "" {
					resultCh <- result{idx: idx, ip: ip}
					return
				}
				select {
				case <-ctx.Done():
					select {
					case errCh <- fmt.Errorf("node %s: waiting for IP: %w", node.Name, ctx.Err()):
					default:
					}
					return
				case <-time.After(15 * time.Second):
					d.AppendLine(fmt.Sprintf("[%s] waiting for guest agent to report IP…", node.Name))
				}
			}
		}()
	}

	resolved := 0
	for resolved < len(pending) {
		select {
		case r := <-resultCh:
			spec.Nodes[r.idx].IP = r.ip
			d.AppendLine(fmt.Sprintf("[%s] guest IP resolved: %s", spec.Nodes[r.idx].Name, r.ip))
			resolved++
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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

// Remove stops the deployment (if still running) and destroys every VM it
// created on the target hypervisor (Destroy powers the VM off first). It returns
// the number of VMs destroyed. The work is bounded by ctx.
//
// The deployment record is KEPT (state → "removed", VM ids cleared) rather than
// dropped, so it can be retried (Retry) or copied back into the deploy form. A
// partially-failed destroy still marks the record removed; leftover VMs can be
// cleaned up manually on the hypervisor.
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

	// Keep the record so it stays listed and can be retried.
	d.mu.Lock()
	d.State = StateRemoved
	for i := range d.Nodes {
		d.Nodes[i].Step = "removed"
		d.Nodes[i].VMID = ""
	}
	d.mu.Unlock()
	d.AppendLine(fmt.Sprintf("Removed deployment: destroyed %d VM(s).", removed))

	if len(errs) > 0 {
		return removed, fmt.Errorf("destroyed %d VM(s); errors: %s", removed, strings.Join(errs, "; "))
	}
	return removed, nil
}

// Retry re-launches a finished/removed deployment with the same spec, returning
// the new deployment. The original record is left untouched. The stored spec
// reuses the same hypervisor client (its credentials live in memory), so no
// re-entry of connection details is needed.
func (m *Manager) Retry(id string) (*Deployment, error) {
	d, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("deploy: deployment %q not found", id)
	}
	d.mu.Lock()
	spec := d.spec
	d.mu.Unlock()
	if spec.HV == nil || len(spec.Nodes) == 0 {
		return nil, fmt.Errorf("deploy: deployment %q cannot be retried (no stored spec)", id)
	}
	return m.Start(spec)
}

// RetryWire creates a new deployment that runs only the wiring step against the
// VMs from an existing (failed) deployment. wireTimeout overrides the original
// WireTimeout when > 0.
func (m *Manager) RetryWire(id string, wireTimeout time.Duration) (*Deployment, error) {
	orig, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("deploy: deployment %q not found", id)
	}
	orig.mu.Lock()
	if orig.State == StateRunning {
		orig.mu.Unlock()
		return nil, fmt.Errorf("deploy: deployment %q is still running", id)
	}
	spec := orig.spec
	orig.mu.Unlock()

	if spec.Wirer == nil {
		return nil, fmt.Errorf("deploy: deployment %q has no wirer configured", id)
	}
	hasRefs := false
	for _, n := range spec.Nodes {
		if n.Ref.ID != "" {
			hasRefs = true
			break
		}
	}
	if !hasRefs {
		return nil, fmt.Errorf("deploy: no VMs were created in deployment %q — cannot retry wiring only", id)
	}
	if wireTimeout > 0 {
		spec.WireTimeout = wireTimeout
	} else if spec.WireTimeout <= 0 {
		spec.WireTimeout = DefaultWireTimeout
	}
	return m.startWireOnly(spec)
}

// startWireOnly registers a wire-only deployment (no VM creation) and launches
// its goroutine.
func (m *Manager) startWireOnly(spec Spec) (*Deployment, error) {
	d := &Deployment{
		ID:        uuid.NewString(),
		Kind:      spec.Label + " — wire retry",
		State:     StatePending,
		CreatedAt: time.Now(),
		done:      make(chan struct{}),
		hv:        spec.HV,
		spec:      spec,
		form:      spec.Form,
	}
	d.Nodes = make([]NodeStatus, len(spec.Nodes))
	for i, n := range spec.Nodes {
		d.Nodes[i] = NodeStatus{Hostname: n.Name, Role: n.Role, VMID: n.Ref.ID, Step: "ready"}
	}

	m.mu.Lock()
	m.deployments[d.ID] = d
	m.mu.Unlock()

	m.wg.Add(1)
	go m.runWireOnly(d, spec)
	return d, nil
}

// runWireOnly resolves any outstanding DHCP IPs and runs the wiring step.
func (m *Manager) runWireOnly(d *Deployment, spec Spec) {
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

	// Re-resolve IPs for any DHCP node (IP="" but Ref.ID set from the original run).
	if err := m.resolveNodeIPs(ctx, d, spec); err != nil {
		if d.isCanceled() {
			d.AppendLine("Wiring retry stopped by user.")
			d.setState(StateCanceled)
			return
		}
		d.AppendLine(fmt.Sprintf("IP RESOLUTION FAILED: %v", err))
		d.mu.Lock()
		d.Error = "ip resolution: " + err.Error()
		d.mu.Unlock()
		d.setState(StateFailed)
		return
	}

	wireTimeout := spec.WireTimeout
	if wireTimeout <= 0 {
		wireTimeout = DefaultWireTimeout
	}
	wctx, wcancel := context.WithTimeout(ctx, wireTimeout)
	defer wcancel()
	d.AppendLine(fmt.Sprintf("Wiring topology into the VSA (Veeam REST, timeout %s)…", wireTimeout))
	if err := spec.Wirer.Wire(wctx, spec.Nodes, d.AppendLine); err != nil {
		if d.isCanceled() {
			d.AppendLine("Wiring retry stopped by user.")
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
	d.setState(StateDone)
}

// List returns snapshots of all deployments, sorted by creation time
// (newest first).
func (m *Manager) List() []View {
	m.mu.RLock()
	ds := make([]*Deployment, 0, len(m.deployments))
	for _, d := range m.deployments {
		ds = append(ds, d)
	}
	m.mu.RUnlock()
	sort.Slice(ds, func(i, j int) bool { return ds[i].CreatedAt.After(ds[j].CreatedAt) })
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
