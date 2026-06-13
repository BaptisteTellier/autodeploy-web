package hypervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// progressReader wraps an io.Reader and reports cumulative bytes read through a
// ProgressFunc, throttled so a multi-GB upload doesn't fire on every chunk.
type progressReader struct {
	r        io.Reader
	total    int64
	done     int64
	cb       ProgressFunc
	interval time.Duration
	last     time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.done += int64(n)
	if pr.cb != nil {
		// Emit on a fixed cadence, and always on the final read.
		if err == io.EOF || time.Since(pr.last) >= pr.interval {
			pr.last = time.Now()
			pr.cb(pr.done, pr.total)
		}
	}
	return n, err
}

// ProxmoxConfig holds connection + placement settings for a Proxmox VE target.
type ProxmoxConfig struct {
	BaseURL    string // full API base, e.g. "https://192.168.1.181:8006/api2/json"
	Node       string // PVE node name, e.g. "pve"
	Storage    string // storage for VM disks, e.g. "local-lvm"
	ISOStorage string // storage holding ISO content (defaults to Storage if empty), e.g. "local"

	// Auth — use either user/password OR an API token.
	Username    string // e.g. "root@pam"
	Password    string
	TokenID     string // e.g. "root@pam!mytoken"
	TokenSecret string

	Insecure bool // skip TLS verification (Proxmox ships a self-signed cert)
}

func (c ProxmoxConfig) isoStorage() string {
	if c.ISOStorage != "" {
		return c.ISOStorage
	}
	return c.Storage
}

// Proxmox is a Hypervisor implementation backed by the Proxmox VE REST API.
type Proxmox struct {
	cfg    ProxmoxConfig
	client *proxmox.Client
}

// compile-time assertion that *Proxmox satisfies the interface.
var _ Hypervisor = (*Proxmox)(nil)

// NewProxmox builds a Proxmox client from cfg. It does not perform any network
// call; the first request happens on the first operation.
func NewProxmox(cfg ProxmoxConfig) (*Proxmox, error) {
	if cfg.BaseURL == "" || cfg.Node == "" || cfg.Storage == "" {
		return nil, fmt.Errorf("proxmox: BaseURL, Node and Storage are required")
	}
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
	return &Proxmox{cfg: cfg, client: proxmox.NewClient(cfg.BaseURL, opts...)}, nil
}

// node fetches the configured PVE node handle.
func (p *Proxmox) node(ctx context.Context) (*proxmox.Node, error) {
	n, err := p.client.Node(ctx, p.cfg.Node)
	if err != nil {
		return nil, fmt.Errorf("proxmox: get node %q: %w", p.cfg.Node, err)
	}
	return n, nil
}

// vm resolves a VMRef to a live VirtualMachine handle.
func (p *Proxmox) vm(ctx context.Context, ref VMRef) (*proxmox.VirtualMachine, error) {
	n, err := p.node(ctx)
	if err != nil {
		return nil, err
	}
	id, err := strconv.Atoi(ref.ID)
	if err != nil {
		return nil, fmt.Errorf("proxmox: bad VM id %q: %w", ref.ID, err)
	}
	v, err := n.VirtualMachine(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("proxmox: get VM %d: %w", id, err)
	}
	return v, nil
}

// waitTask blocks until a Proxmox task completes (or ctx/timeout fires) and then
// verifies it actually SUCCEEDED. go-proxmox's Task.Wait returns as soon as the
// task stops running — it does NOT inspect the exit status — so a failed upload
// (e.g. the ISO storage filling up) would otherwise look like success. We
// re-check the exit status and surface a real error.
func waitTask(ctx context.Context, t *proxmox.Task, max time.Duration) error {
	if t == nil {
		return nil
	}
	if err := t.Wait(ctx, 2*time.Second, max); err != nil {
		return err
	}
	if err := t.Ping(ctx); err != nil {
		return err
	}
	if t.IsFailed || (t.ExitStatus != "" && t.ExitStatus != "OK") {
		status := t.ExitStatus
		if status == "" {
			status = "failed"
		}
		return fmt.Errorf("proxmox task %s: %s", t.Type, status)
	}
	return nil
}

// UploadISO uploads a local ISO to the ISO storage and returns its volume
// reference ("<storage>:iso/<filename>") usable as a CD-ROM source. When
// progress is non-nil it is called periodically with bytes sent / total.
//
// This mirrors go-proxmox's Storage.UploadWithName but streams the file through
// a progressReader (the library's UploadReader concatenates the multipart
// header + the body reader + trailer with io.MultiReader, so the file is never
// buffered in memory and our counting reader sees every byte as it is sent).
func (p *Proxmox) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	n, err := p.node(ctx)
	if err != nil {
		return "", err
	}
	store, err := n.Storage(ctx, p.cfg.isoStorage())
	if err != nil {
		return "", fmt.Errorf("proxmox: get storage %q: %w", p.cfg.isoStorage(), err)
	}
	name := filepath.Base(localPath)

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("proxmox: open ISO %q: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("proxmox: stat ISO %q: %w", name, err)
	}
	size := st.Size()

	body := io.Reader(f)
	if progress != nil {
		body = &progressReader{r: f, total: size, cb: progress, interval: time.Second}
	}

	var upid proxmox.UPID
	uploadPath := fmt.Sprintf("/nodes/%s/storage/%s/upload", store.Node, store.Name)
	if err := p.client.UploadReader(uploadPath, map[string]string{"content": "iso"}, name, body, size, &upid); err != nil {
		return "", fmt.Errorf("proxmox: upload ISO %q: %w", name, err)
	}
	// ISOs can be large; allow a generous ceiling for the server-side import.
	if err := waitTask(ctx, proxmox.NewTask(upid, p.client), 2*time.Hour); err != nil {
		return "", fmt.Errorf("proxmox: upload ISO %q: %w", name, err)
	}

	// Confirm the ISO is really in the library before we hand back a reference to
	// attach. This catches a silently-failed import (e.g. the storage ran out of
	// space) so we never create a VM with a dangling CD-ROM.
	ref := fmt.Sprintf("%s:iso/%s", p.cfg.isoStorage(), name)
	if content, err := store.GetContent(ctx); err == nil {
		present := false
		for _, c := range content {
			if c.Volid == ref {
				present = true
				break
			}
		}
		if !present {
			return "", fmt.Errorf("proxmox: ISO %q not found in storage %q after upload — the import likely failed (out of space?)", name, p.cfg.isoStorage())
		}
	}
	if progress != nil {
		progress(size, size) // ensure the bar lands on 100%
	}
	return ref, nil
}

// FindISO looks for an ISO by filename in the ISO storage and returns its
// volume reference, or "" when absent.
func (p *Proxmox) FindISO(ctx context.Context, name string) (string, error) {
	n, err := p.node(ctx)
	if err != nil {
		return "", err
	}
	store, err := n.Storage(ctx, p.cfg.isoStorage())
	if err != nil {
		return "", fmt.Errorf("proxmox: get storage %q: %w", p.cfg.isoStorage(), err)
	}
	content, err := store.GetContent(ctx)
	if err != nil {
		return "", fmt.Errorf("proxmox: list storage content: %w", err)
	}
	want := fmt.Sprintf("%s:iso/%s", p.cfg.isoStorage(), name)
	for _, c := range content {
		if c.Volid == want {
			return want, nil
		}
	}
	return "", nil
}

// SendKeys types QEMU key names on the VM console, one PUT /sendkey per key
// with a small inter-key delay so GRUB keeps up.
func (p *Proxmox) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%s/sendkey", p.cfg.Node, vm.ID)
	for _, k := range keys {
		if err := p.client.Put(ctx, path, map[string]string{"key": k}, nil); err != nil {
			return fmt.Errorf("proxmox: sendkey %q to VM %s: %w", k, vm.ID, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(60 * time.Millisecond):
		}
	}
	return nil
}

// CreateVM provisions a powered-off VM shell with a SCSI disk and one NIC.
func (p *Proxmox) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	n, err := p.node(ctx)
	if err != nil {
		return VMRef{}, err
	}
	cluster, err := p.client.Cluster(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("proxmox: cluster: %w", err)
	}
	vmid, err := cluster.NextID(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("proxmox: next VMID: %w", err)
	}

	net0 := fmt.Sprintf("virtio,bridge=%s", spec.Bridge)
	if spec.VLAN > 0 {
		net0 += fmt.Sprintf(",tag=%d", spec.VLAN)
	}
	opts := []proxmox.VirtualMachineOption{
		{Name: "name", Value: spec.Name},
		{Name: "cores", Value: spec.CPUs},
		{Name: "memory", Value: spec.MemoryMiB},
		{Name: "ostype", Value: "l26"},
		{Name: "scsihw", Value: "virtio-scsi-single"},
		{Name: "net0", Value: net0},
		{Name: "cpu", Value: "x86-64-v2-AES"},
		{Name: "vga", Value: "virtio"},
	}
	if spec.UEFI {
		// OVMF needs q35 + a dedicated EFI vars disk (Proxmox docs). Secure Boot
		// keys are NOT pre-enrolled (pre-enrolled-keys=0) so the custom-built
		// Veeam installer ISO boots without Secure Boot enforcement.
		opts = append(opts,
			proxmox.VirtualMachineOption{Name: "bios", Value: "ovmf"},
			proxmox.VirtualMachineOption{Name: "machine", Value: "q35"},
			proxmox.VirtualMachineOption{Name: "efidisk0", Value: fmt.Sprintf("%s:1,efitype=4m,pre-enrolled-keys=0", p.cfg.Storage)},
		)
	}
	disks := spec.Disks
	if len(disks) == 0 {
		disks = []int{32} // safety default
	}
	for i, size := range disks {
		opts = append(opts, proxmox.VirtualMachineOption{
			Name:  fmt.Sprintf("scsi%d", i),
			Value: fmt.Sprintf("%s:%d", p.cfg.Storage, size),
		})
	}
	task, err := n.NewVirtualMachine(ctx, vmid, opts...)
	if err != nil {
		return VMRef{}, fmt.Errorf("proxmox: create VM %d: %w", vmid, err)
	}
	if err := waitTask(ctx, task, 5*time.Minute); err != nil {
		return VMRef{}, fmt.Errorf("proxmox: create VM %d: %w", vmid, err)
	}
	return VMRef{ID: strconv.Itoa(vmid), Node: p.cfg.Node}, nil
}

// configVM applies a single config option to a VM and waits for the task.
func (p *Proxmox) configVM(ctx context.Context, ref VMRef, opt proxmox.VirtualMachineOption) error {
	v, err := p.vm(ctx, ref)
	if err != nil {
		return err
	}
	task, err := v.Config(ctx, opt)
	if err != nil {
		return fmt.Errorf("proxmox: config VM %s (%s): %w", ref.ID, opt.Name, err)
	}
	return waitTask(ctx, task, 5*time.Minute)
}

// AttachISO mounts isoRef as the VM's ide2 CD-ROM.
func (p *Proxmox) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	return p.configVM(ctx, vm, proxmox.VirtualMachineOption{Name: "ide2", Value: isoRef + ",media=cdrom"})
}

// DetachISO ejects the CD-ROM (keeps the empty drive).
func (p *Proxmox) DetachISO(ctx context.Context, vm VMRef) error {
	return p.configVM(ctx, vm, proxmox.VirtualMachineOption{Name: "ide2", Value: "none,media=cdrom"})
}

// SetBootFromCD makes the VM boot the CD-ROM first, then the disk.
func (p *Proxmox) SetBootFromCD(ctx context.Context, vm VMRef) error {
	return p.configVM(ctx, vm, proxmox.VirtualMachineOption{Name: "boot", Value: "order=ide2;scsi0"})
}

// SetBootFromDisk makes the VM boot the disk only.
func (p *Proxmox) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	return p.configVM(ctx, vm, proxmox.VirtualMachineOption{Name: "boot", Value: "order=scsi0"})
}

// PowerOn starts the VM.
func (p *Proxmox) PowerOn(ctx context.Context, vm VMRef) error {
	v, err := p.vm(ctx, vm)
	if err != nil {
		return err
	}
	task, err := v.Start(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: start VM %s: %w", vm.ID, err)
	}
	return waitTask(ctx, task, 5*time.Minute)
}

// PowerOff stops the VM.
func (p *Proxmox) PowerOff(ctx context.Context, vm VMRef) error {
	v, err := p.vm(ctx, vm)
	if err != nil {
		return err
	}
	if v.IsStopped() {
		return nil
	}
	task, err := v.Stop(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: stop VM %s: %w", vm.ID, err)
	}
	return waitTask(ctx, task, 5*time.Minute)
}

// Status returns the coarse power state of the VM.
func (p *Proxmox) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	v, err := p.vm(ctx, vm)
	if err != nil {
		return PowerUnknown, err
	}
	switch v.Status {
	case proxmox.StatusVirtualMachineRunning:
		return PowerRunning, nil
	case proxmox.StatusVirtualMachineStopped:
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// GetVMIP queries the QEMU Guest Agent for the VM's network interfaces and
// returns the first non-loopback, non-link-local IPv4 address found.
// Returns ("", nil) when the agent is not yet running.
func (p *Proxmox) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	v, err := p.vm(ctx, vm)
	if err != nil {
		return "", err
	}
	ifaces, err := v.AgentGetNetworkIFaces(ctx)
	if err != nil {
		// Agent not ready yet — not an error; caller will retry.
		return "", nil
	}
	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.IPAddressType != "ipv4" {
				continue
			}
			ip := net.ParseIP(addr.IPAddress)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			return addr.IPAddress, nil
		}
	}
	return "", nil
}

// Destroy stops (if needed) and deletes the VM and its disks.
func (p *Proxmox) Destroy(ctx context.Context, vm VMRef) error {
	v, err := p.vm(ctx, vm)
	if err != nil {
		return err
	}
	if !v.IsStopped() {
		if task, err := v.Stop(ctx); err == nil {
			_ = waitTask(ctx, task, 5*time.Minute)
		}
	}
	task, err := v.Delete(ctx)
	if err != nil {
		return fmt.Errorf("proxmox: delete VM %s: %w", vm.ID, err)
	}
	return waitTask(ctx, task, 5*time.Minute)
}
