package hypervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	xenapi "github.com/terra-farm/go-xen-api-client"
)

// XCPngConfig holds connection + placement settings for an XCP-ng / XenServer
// pool, driven over the XenAPI (XML-RPC) interface.
type XCPngConfig struct {
	Host     string // pool master URL, e.g. "https://xcp.lab.local"
	Username string // e.g. "root"
	Password string
	Insecure bool // skip TLS verification

	SR      string // storage repository name/UUID for VM disks
	ISOSR   string // ISO storage repository name/UUID
	Network string // network name/UUID for the VIF
}

// XCPng is a Hypervisor implementation backed by the XenAPI (XML-RPC).
//
// XCP-ng exposes no keystroke-injection API, so SendKeys returns
// ErrKickstartUnsupported and remote kickstart is rejected up front for this
// provider (SupportsKickstart == false). Classic pre-customised-ISO deploys are
// fully supported.
type XCPng struct {
	cfg XCPngConfig

	mu         sync.Mutex
	client     *xenapi.Client
	session    xenapi.SessionRef
	httpClient *http.Client // shared client used for ISO upload
}

// compile-time assertion that *XCPng satisfies the interface.
var _ Hypervisor = (*XCPng)(nil)

// NewXCPng builds a XenAPI client from cfg.
func NewXCPng(cfg XCPngConfig) (*XCPng, error) {
	if cfg.Host == "" || cfg.Username == "" || cfg.SR == "" {
		return nil, fmt.Errorf("xcpng: Host, Username and SR are required")
	}

	// Build a shared http.Client for out-of-band HTTP requests (ISO upload).
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec — self-signed XCP-ng cert, opt-in
	}
	httpClient := &http.Client{Transport: transport}

	return &XCPng{cfg: cfg, httpClient: httpClient}, nil
}

// login creates a new XenAPI session and caches it on the struct.
// The caller must hold x.mu.
func (x *XCPng) login() error {
	// NewClient accepts a *http.Transport; passing nil uses the library's
	// default (InsecureSkipVerify: true). We always supply an explicit
	// transport so we honour cfg.Insecure correctly.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: x.cfg.Insecure}, //nolint:gosec — self-signed XCP-ng cert, opt-in
	}
	c, err := xenapi.NewClient(x.cfg.Host, transport)
	if err != nil {
		return fmt.Errorf("xcpng: connect %s: %w", x.cfg.Host, err)
	}
	sess, err := c.Session.LoginWithPassword(x.cfg.Username, x.cfg.Password, "1.0", "autodeploy-web")
	if err != nil {
		return fmt.Errorf("xcpng: login: %w", err)
	}
	x.client = c
	x.session = sess
	return nil
}

// ensureSession returns a valid (client, session) pair, re-logging in when
// needed.
func (x *XCPng) ensureSession() (*xenapi.Client, xenapi.SessionRef, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.client == nil || x.session == "" {
		if err := x.login(); err != nil {
			return nil, "", err
		}
	}
	return x.client, x.session, nil
}

// xenIsUUID returns true if s looks like a canonical UUID
// (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx, 36 chars).
func xenIsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// resolveSR returns a SRRef for cfg.SR or cfg.ISOSR.
func (x *XCPng) resolveSR(c *xenapi.Client, sess xenapi.SessionRef, nameOrUUID string) (xenapi.SRRef, error) {
	if xenIsUUID(nameOrUUID) {
		ref, err := c.SR.GetByUUID(sess, nameOrUUID)
		if err != nil {
			return "", fmt.Errorf("xcpng: resolve SR by UUID %q: %w", nameOrUUID, err)
		}
		return ref, nil
	}
	refs, err := c.SR.GetByNameLabel(sess, nameOrUUID)
	if err != nil {
		return "", fmt.Errorf("xcpng: resolve SR by name %q: %w", nameOrUUID, err)
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("xcpng: no SR found with name %q", nameOrUUID)
	}
	return refs[0], nil
}

// resolveNetwork returns a NetworkRef for cfg.Network.
func (x *XCPng) resolveNetwork(c *xenapi.Client, sess xenapi.SessionRef, nameOrUUID string) (xenapi.NetworkRef, error) {
	if xenIsUUID(nameOrUUID) {
		ref, err := c.Network.GetByUUID(sess, nameOrUUID)
		if err != nil {
			return "", fmt.Errorf("xcpng: resolve network by UUID %q: %w", nameOrUUID, err)
		}
		return ref, nil
	}
	refs, err := c.Network.GetByNameLabel(sess, nameOrUUID)
	if err != nil {
		return "", fmt.Errorf("xcpng: resolve network by name %q: %w", nameOrUUID, err)
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("xcpng: no network found with name %q", nameOrUUID)
	}
	return refs[0], nil
}

// resolveVM returns a VMRef from a VMRef.ID (expected to be a VM UUID).
func (x *XCPng) resolveVM(c *xenapi.Client, sess xenapi.SessionRef, vmID string) (xenapi.VMRef, error) {
	ref, err := c.VM.GetByUUID(sess, vmID)
	if err != nil {
		return "", fmt.Errorf("xcpng: resolve VM %q: %w", vmID, err)
	}
	return ref, nil
}

// findCDVBD returns the first CD-type VBD attached to the VM, or "" if none.
func (x *XCPng) findCDVBD(c *xenapi.Client, sess xenapi.SessionRef, vmRef xenapi.VMRef) (xenapi.VBDRef, error) {
	vbdRefs, err := c.VM.GetVBDs(sess, vmRef)
	if err != nil {
		return "", fmt.Errorf("xcpng: list VBDs: %w", err)
	}
	for _, vbdRef := range vbdRefs {
		t, err := c.VBD.GetType(sess, vbdRef)
		if err != nil {
			continue
		}
		if t == xenapi.VbdTypeCD {
			return vbdRef, nil
		}
	}
	return "", nil
}

// UploadISO uploads a local ISO to the ISO SR via the XenAPI HTTP import_raw_vdi
// endpoint and returns the new VDI's UUID as isoRef.
//
// The upload flow is:
//  1. Create a VDI in cfg.ISOSR sized to the file (type "user").
//  2. PUT the raw bytes to https://<host>/import_raw_vdi?session_id=<ref>&vdi=<uuid>&format=raw
//     streaming through a progressReader so progress() is called periodically.
//  3. SR.Scan the ISO SR so XenCenter / xe can see the new ISO immediately.
//
// NOTE: this assumes the ISO SR supports raw VDI import (e.g. ISO SR backed by
// a local directory or NFS). Pre-staged ISOs that already exist on the SR can be
// found more efficiently with FindISO, which skips the upload entirely.
func (x *XCPng) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	c, sess, err := x.ensureSession()
	if err != nil {
		return "", err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("xcpng: open ISO %q: %w", filepath.Base(localPath), err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("xcpng: stat ISO %q: %w", filepath.Base(localPath), err)
	}
	size := st.Size()
	name := filepath.Base(localPath)

	isoSR, err := x.resolveSR(c, sess, x.cfg.ISOSR)
	if err != nil {
		return "", err
	}

	// Step 1: create a placeholder VDI in the ISO SR.
	vdiRef, err := c.VDI.Create(sess, xenapi.VDIRecord{
		NameLabel:       name,
		NameDescription: "autodeploy-web upload",
		SR:              isoSR,
		VirtualSize:     int(size),
		Type:            xenapi.VdiTypeUser,
		Sharable:        false,
		ReadOnly:        false,
	})
	if err != nil {
		return "", fmt.Errorf("xcpng: create VDI for ISO %q: %w", name, err)
	}

	// Retrieve the VDI UUID so we can build the upload URL and return the ref.
	vdiUUID, err := c.VDI.GetUUID(sess, vdiRef)
	if err != nil {
		// Clean up the VDI we just created.
		_ = c.VDI.Destroy(sess, vdiRef)
		return "", fmt.Errorf("xcpng: get VDI UUID: %w", err)
	}

	// Step 2: stream the ISO to the XenAPI HTTP import_raw_vdi endpoint.
	uploadURL := fmt.Sprintf("%s/import_raw_vdi?session_id=%s&vdi=%s&format=raw",
		strings.TrimRight(x.cfg.Host, "/"),
		url.QueryEscape(string(sess)),
		url.QueryEscape(vdiUUID),
	)

	var body io.Reader = f
	if progress != nil {
		body = &progressReader{r: f, total: size, cb: progress, interval: time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		_ = c.VDI.Destroy(sess, vdiRef)
		return "", fmt.Errorf("xcpng: build upload request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := x.httpClient.Do(req)
	if err != nil {
		_ = c.VDI.Destroy(sess, vdiRef)
		return "", fmt.Errorf("xcpng: upload ISO %q: %w", name, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = c.VDI.Destroy(sess, vdiRef)
		return "", fmt.Errorf("xcpng: upload ISO %q: server returned HTTP %d", name, resp.StatusCode)
	}

	// Step 3: scan the ISO SR so the new ISO is visible.
	if scanErr := c.SR.Scan(sess, isoSR); scanErr != nil {
		// Non-fatal: the upload succeeded; log but don't fail.
		_ = scanErr
	}

	if progress != nil {
		progress(size, size) // ensure the bar reaches 100 %
	}
	return vdiUUID, nil
}

// FindISO searches cfg.ISOSR for a VDI whose name_label matches the ISO
// basename and returns its UUID, or "" when absent.
func (x *XCPng) FindISO(ctx context.Context, name string) (string, error) {
	c, sess, err := x.ensureSession()
	if err != nil {
		return "", err
	}

	isoSR, err := x.resolveSR(c, sess, x.cfg.ISOSR)
	if err != nil {
		return "", err
	}

	// Scan first so the SR index is fresh (e.g. files dropped directly on NFS).
	_ = c.SR.Scan(sess, isoSR)

	vdiRefs, err := c.SR.GetVDIs(sess, isoSR)
	if err != nil {
		return "", fmt.Errorf("xcpng: list ISO SR VDIs: %w", err)
	}

	base := filepath.Base(name)
	for _, vdiRef := range vdiRefs {
		label, err := c.VDI.GetNameLabel(sess, vdiRef)
		if err != nil {
			continue
		}
		if label == base {
			uuid, err := c.VDI.GetUUID(sess, vdiRef)
			if err != nil {
				return "", fmt.Errorf("xcpng: get VDI UUID for %q: %w", base, err)
			}
			return uuid, nil
		}
	}
	return "", nil
}

// SendKeys is unsupported on XCP-ng (no keystroke-injection API).
func (x *XCPng) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	return ErrKickstartUnsupported
}

// CreateVM provisions a powered-off VM shell per spec, creating one data VDI +
// VBD per spec.Disks entry, an empty CD VBD, and a single VIF on cfg.Network.
// Returns a VMRef whose ID is the VM UUID.
func (x *XCPng) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	c, sess, err := x.ensureSession()
	if err != nil {
		return VMRef{}, err
	}

	diskSR, err := x.resolveSR(c, sess, x.cfg.SR)
	if err != nil {
		return VMRef{}, err
	}
	network, err := x.resolveNetwork(c, sess, x.cfg.Network)
	if err != nil {
		return VMRef{}, err
	}

	memBytes := int(spec.MemoryMiB) * 1024 * 1024

	// Platform map and boot parameters differ between BIOS and UEFI.
	// For UEFI: XCP-ng uses platform key "device-model" with OVMF firmware and
	// sets HVM_boot_params firmware=uefi. Secure Boot is explicitly disabled by
	// setting secureboot=false in the platform map.
	// For BIOS: classic HVM with SeaBIOS (empty platform map is fine).
	platform := map[string]string{
		"nx":       "true",
		"acpi":     "1",
		"apic":     "true",
		"pae":      "true",
		"viridian": "false",
	}
	hvmBootParams := map[string]string{
		"order": "dc", // initial order; overridden by Set{BootFrom*}
	}
	if spec.UEFI {
		// XCP-ng UEFI: set firmware=uefi in boot params; disable Secure Boot.
		hvmBootParams["firmware"] = "uefi"
		platform["secureboot"] = "false"
	}

	vmRecord := xenapi.VMRecord{
		NameLabel:            spec.Name,
		NameDescription:      "Created by autodeploy-web",
		UserVersion:          1,
		IsATemplate:          false,
		MemoryStaticMin:      memBytes,
		MemoryStaticMax:      memBytes,
		MemoryDynamicMin:     memBytes,
		MemoryDynamicMax:     memBytes,
		VCPUsMax:             spec.CPUs,
		VCPUsAtStartup:       spec.CPUs,
		HVMBootPolicy:        "BIOS order",
		HVMBootParams:        hvmBootParams,
		Platform:             platform,
		ActionsAfterShutdown: xenapi.OnNormalExitDestroy,
		ActionsAfterReboot:   xenapi.OnNormalExitRestart,
		ActionsAfterCrash:    xenapi.OnCrashBehaviourRestart,
		OtherConfig:          map[string]string{"autodeploy-web": "true"},
	}

	vmRef, err := c.VM.Create(sess, vmRecord)
	if err != nil {
		return VMRef{}, fmt.Errorf("xcpng: create VM %q: %w", spec.Name, err)
	}

	// Retrieve the VM UUID early; used as the stable handle going forward.
	vmUUID, err := c.VM.GetUUID(sess, vmRef)
	if err != nil {
		_ = c.VM.Destroy(sess, vmRef)
		return VMRef{}, fmt.Errorf("xcpng: get VM UUID: %w", err)
	}

	// Create data disks.
	disks := spec.Disks
	if len(disks) == 0 {
		disks = []int{32} // safety default
	}
	for i, gib := range disks {
		vdiRef, err := c.VDI.Create(sess, xenapi.VDIRecord{
			NameLabel:   fmt.Sprintf("%s-disk%d", spec.Name, i),
			SR:          diskSR,
			VirtualSize: gib * 1024 * 1024 * 1024,
			Type:        xenapi.VdiTypeUser,
			Sharable:    false,
			ReadOnly:    false,
		})
		if err != nil {
			x.destroyVMBestEffort(c, sess, vmRef)
			return VMRef{}, fmt.Errorf("xcpng: create disk VDI %d for VM %q: %w", i, spec.Name, err)
		}
		_, err = c.VBD.Create(sess, xenapi.VBDRecord{
			VM:                 vmRef,
			VDI:                vdiRef,
			Userdevice:         fmt.Sprintf("%d", i),
			Bootable:           i == 0, // first disk is bootable
			Mode:               xenapi.VbdModeRW,
			Type:               xenapi.VbdTypeDisk,
			Unpluggable:        false,
			Empty:              false,
			OtherConfig:        map[string]string{},
			QosAlgorithmType:   "",
			QosAlgorithmParams: map[string]string{},
		})
		if err != nil {
			x.destroyVMBestEffort(c, sess, vmRef)
			return VMRef{}, fmt.Errorf("xcpng: create VBD for disk %d on VM %q: %w", i, spec.Name, err)
		}
	}

	// Create an empty CD-ROM VBD slot for later AttachISO.
	cdDevice := fmt.Sprintf("%d", len(disks))
	_, err = c.VBD.Create(sess, xenapi.VBDRecord{
		VM:                 vmRef,
		VDI:                "", // empty drive
		Userdevice:         cdDevice,
		Bootable:           false,
		Mode:               xenapi.VbdModeRO,
		Type:               xenapi.VbdTypeCD,
		Unpluggable:        true,
		Empty:              true,
		OtherConfig:        map[string]string{},
		QosAlgorithmType:   "",
		QosAlgorithmParams: map[string]string{},
	})
	if err != nil {
		x.destroyVMBestEffort(c, sess, vmRef)
		return VMRef{}, fmt.Errorf("xcpng: create CD VBD for VM %q: %w", spec.Name, err)
	}

	// Create a single VIF on cfg.Network.
	_, err = c.VIF.Create(sess, xenapi.VIFRecord{
		Device:             "0",
		Network:            network,
		VM:                 vmRef,
		MAC:                "", // auto-generate
		MTU:                1500,
		OtherConfig:        map[string]string{},
		QosAlgorithmType:   "",
		QosAlgorithmParams: map[string]string{},
	})
	if err != nil {
		x.destroyVMBestEffort(c, sess, vmRef)
		return VMRef{}, fmt.Errorf("xcpng: create VIF for VM %q: %w", spec.Name, err)
	}

	return VMRef{ID: vmUUID, Node: x.cfg.Host}, nil
}

// AttachISO inserts isoRef (a VDI UUID) into the VM's CD-ROM VBD and marks
// that VBD as bootable.
func (x *XCPng) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	cdVBD, err := x.findCDVBD(c, sess, vmRef)
	if err != nil {
		return fmt.Errorf("xcpng: find CD VBD on VM %s: %w", vm.ID, err)
	}
	if cdVBD == "" {
		return fmt.Errorf("xcpng: no CD VBD found on VM %s", vm.ID)
	}

	var vdiRef xenapi.VDIRef
	if xenIsUUID(isoRef) {
		vdiRef, err = c.VDI.GetByUUID(sess, isoRef)
	} else {
		// Treat as a name-label fallback.
		refs, nerr := c.VDI.GetByNameLabel(sess, isoRef)
		if nerr != nil || len(refs) == 0 {
			return fmt.Errorf("xcpng: resolve ISO VDI %q: %w", isoRef, nerr)
		}
		vdiRef = refs[0]
		err = nil
	}
	if err != nil {
		return fmt.Errorf("xcpng: resolve ISO VDI %q: %w", isoRef, err)
	}

	if err := c.VBD.Insert(sess, cdVBD, vdiRef); err != nil {
		return fmt.Errorf("xcpng: insert ISO into CD VBD on VM %s: %w", vm.ID, err)
	}
	return nil
}

// DetachISO ejects the CD-ROM from the VM's CD VBD.
func (x *XCPng) DetachISO(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	cdVBD, err := x.findCDVBD(c, sess, vmRef)
	if err != nil {
		return fmt.Errorf("xcpng: find CD VBD on VM %s: %w", vm.ID, err)
	}
	if cdVBD == "" {
		return nil // nothing to eject
	}
	if err := c.VBD.Eject(sess, cdVBD); err != nil {
		return fmt.Errorf("xcpng: eject CD from VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromCD sets the HVM boot order so the CD-ROM is tried first.
//
// XCP-ng / Xen HVM boot order letters:
//
//	d = CD-ROM / DVD drive
//	c = hard disk
//	n = network (PXE)
//
// "dc" means: try CD first, fall back to disk.
func (x *XCPng) SetBootFromCD(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	// "dc" — DVD then disk. Matches the Xen HVM boot order convention used by
	// XCP-ng and XenServer (same as the QEMU -boot order string but Xen-native).
	if err := c.VM.SetHVMBootParams(sess, vmRef, map[string]string{"order": "dc"}); err != nil {
		return fmt.Errorf("xcpng: set boot order cd-first on VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromDisk sets the HVM boot order so the disk boots unconditionally.
//
// "c" means: hard disk only.
func (x *XCPng) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	// "c" — hard disk only. After the installer has finished we never want to
	// boot the CD again, so we drop "d" from the order entirely.
	if err := c.VM.SetHVMBootParams(sess, vmRef, map[string]string{"order": "c"}); err != nil {
		return fmt.Errorf("xcpng: set boot order disk-first on VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootDiskThenCD sets the HVM boot order to disk first, CD-ROM second.
//
// XCP-ng / Xen HVM boot order letters: c = hard disk, d = CD-ROM / DVD.
// "cd" means: try hard disk first, fall back to CD-ROM.
// On a blank disk the firmware falls through to the CD installer; after
// install the disk has an OS and boots directly with no runtime change needed.
func (x *XCPng) SetBootDiskThenCD(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	if err := c.VM.SetHVMBootParams(sess, vmRef, map[string]string{"order": "cd"}); err != nil {
		return fmt.Errorf("xcpng: set boot order disk-then-cd on VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOn starts the VM.
func (x *XCPng) PowerOn(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	if err := c.VM.Start(sess, vmRef, false, false); err != nil {
		return fmt.Errorf("xcpng: start VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOff hard-shuts the VM. If the VM is already halted the call is a no-op.
func (x *XCPng) PowerOff(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}
	ps, err := c.VM.GetPowerState(sess, vmRef)
	if err != nil {
		return fmt.Errorf("xcpng: get power state for VM %s: %w", vm.ID, err)
	}
	if ps == xenapi.VMPowerStateHalted {
		return nil
	}
	if err := c.VM.HardShutdown(sess, vmRef); err != nil {
		return fmt.Errorf("xcpng: hard shutdown VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM.
func (x *XCPng) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	c, sess, err := x.ensureSession()
	if err != nil {
		return PowerUnknown, err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return PowerUnknown, err
	}
	ps, err := c.VM.GetPowerState(sess, vmRef)
	if err != nil {
		return PowerUnknown, fmt.Errorf("xcpng: get power state for VM %s: %w", vm.ID, err)
	}
	switch ps {
	case xenapi.VMPowerStateRunning:
		return PowerRunning, nil
	case xenapi.VMPowerStateHalted:
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// Destroy hard-shuts the VM (if running), destroys all data VDIs attached as
// RW disks, then destroys the VM record itself.
func (x *XCPng) Destroy(ctx context.Context, vm VMRef) error {
	c, sess, err := x.ensureSession()
	if err != nil {
		return err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return err
	}

	// Hard-shut if not already halted; ignore errors (e.g. already halted).
	if ps, err := c.VM.GetPowerState(sess, vmRef); err == nil && ps != xenapi.VMPowerStateHalted {
		_ = c.VM.HardShutdown(sess, vmRef)
	}

	// Collect VDIs to destroy: only RW disk VBDs (skip CD-ROM slots).
	vbdRefs, err := c.VM.GetVBDs(sess, vmRef)
	if err != nil {
		return fmt.Errorf("xcpng: list VBDs for VM %s: %w", vm.ID, err)
	}
	var vdisToDestroy []xenapi.VDIRef
	for _, vbdRef := range vbdRefs {
		rec, err := c.VBD.GetRecord(sess, vbdRef)
		if err != nil {
			continue
		}
		if rec.Type == xenapi.VbdTypeDisk && rec.Mode == xenapi.VbdModeRW && !rec.Empty {
			vdisToDestroy = append(vdisToDestroy, rec.VDI)
		}
	}

	// Destroy the VM; this also destroys VBD records automatically.
	if err := c.VM.Destroy(sess, vmRef); err != nil {
		return fmt.Errorf("xcpng: destroy VM %s: %w", vm.ID, err)
	}

	// Destroy the VDIs after the VM (VBDs are gone, VDIs remain until explicitly deleted).
	for _, vdiRef := range vdisToDestroy {
		_ = c.VDI.Destroy(sess, vdiRef)
	}

	return nil
}

// GetVMIP reads the XenServer/XCP-ng guest metrics and returns the first
// IPv4 address found in the Networks map (keys like "0/ip", "1/ip").
// Returns ("", nil) when the guest agent has not reported any address yet.
func (x *XCPng) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	c, sess, err := x.ensureSession()
	if err != nil {
		return "", err
	}
	vmRef, err := x.resolveVM(c, sess, vm.ID)
	if err != nil {
		return "", err
	}
	metricsRef, err := c.VM.GetGuestMetrics(sess, vmRef)
	if err != nil || metricsRef == "OpaqueRef:NULL" {
		// Guest agent not running yet.
		return "", nil
	}
	networks, err := c.VMGuestMetrics.GetNetworks(sess, metricsRef)
	if err != nil {
		return "", nil
	}
	// Keys are of the form "N/ip" for IPv4 and "N/ipv6/M" for IPv6.
	// Iterate in a stable order by checking index 0 first, then 1, …
	for i := 0; i < 16; i++ {
		key := fmt.Sprintf("%d/ip", i)
		if ip, ok := networks[key]; ok && ip != "" {
			return ip, nil
		}
	}
	return "", nil
}

// destroyVMBestEffort is used for rollback during CreateVM. It hard-shuts (if
// needed), then destroys the VM and all associated RW VDIs. Errors are ignored.
func (x *XCPng) destroyVMBestEffort(c *xenapi.Client, sess xenapi.SessionRef, vmRef xenapi.VMRef) {
	if ps, err := c.VM.GetPowerState(sess, vmRef); err == nil && ps != xenapi.VMPowerStateHalted {
		_ = c.VM.HardShutdown(sess, vmRef)
	}
	vbdRefs, err := c.VM.GetVBDs(sess, vmRef)
	var vdis []xenapi.VDIRef
	if err == nil {
		for _, vbdRef := range vbdRefs {
			rec, err := c.VBD.GetRecord(sess, vbdRef)
			if err != nil {
				continue
			}
			if rec.Type == xenapi.VbdTypeDisk && !rec.Empty {
				vdis = append(vdis, rec.VDI)
			}
		}
	}
	_ = c.VM.Destroy(sess, vmRef)
	for _, vdiRef := range vdis {
		_ = c.VDI.Destroy(sess, vdiRef)
	}
}
