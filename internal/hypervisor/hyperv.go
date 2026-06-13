package hypervisor

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/masterzen/winrm"
)

// HyperVConfig holds WinRM connection + placement settings for a Hyper-V host.
// autodeploy-web runs in a Linux container, so Hyper-V is driven by PowerShell
// remoting (WinRM) against the Windows host rather than a native API.
type HyperVConfig struct {
	Host     string // Hyper-V host reachable over WinRM
	Port     int    // WinRM port (5985 HTTP / 5986 HTTPS); 0 = default for HTTPS flag
	Username string
	Password string
	HTTPS    bool // use HTTPS WinRM transport
	Insecure bool // skip TLS verification when HTTPS

	SwitchName string // virtual switch the NIC connects to
	VMPath     string // host path where VM config + VHDs are created
	ISOPath    string // host path (or SMB share) where ISOs are uploaded/staged
}

// HyperV is a Hypervisor implementation that drives a Windows Hyper-V host over
// WinRM (PowerShell Hyper-V cmdlets + Msvm_Keyboard for key injection).
type HyperV struct {
	cfg    HyperVConfig
	mu     sync.Mutex
	client *winrm.Client // lazily initialised; guarded by mu
}

// compile-time assertion that *HyperV satisfies the interface.
var _ Hypervisor = (*HyperV)(nil)

// NewHyperV builds a Hyper-V (WinRM) client from cfg.
func NewHyperV(cfg HyperVConfig) (*HyperV, error) {
	if cfg.Host == "" || cfg.Username == "" || cfg.SwitchName == "" {
		return nil, fmt.Errorf("hyperv: Host, Username and SwitchName are required")
	}
	return &HyperV{cfg: cfg}, nil
}

// winrmPort returns the WinRM port to connect on.
func (h *HyperV) winrmPort() int {
	if h.cfg.Port > 0 {
		return h.cfg.Port
	}
	if h.cfg.HTTPS {
		return 5986
	}
	return 5985
}

// getClient lazily builds and caches the WinRM client. The client itself does
// not hold an open TCP connection; shells (and therefore connections) are
// created per-command inside RunPSWithContext.
func (h *HyperV) getClient() (*winrm.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil {
		return h.client, nil
	}
	ep := winrm.NewEndpoint(
		h.cfg.Host,
		h.winrmPort(),
		h.cfg.HTTPS,
		h.cfg.Insecure,
		nil, nil, nil, // no custom CA/cert/key
		0, // use library default 60 s timeout
	)
	c, err := winrm.NewClient(ep, h.cfg.Username, h.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("hyperv: build winrm client: %w", err)
	}
	h.client = c
	return c, nil
}

// runPS runs a PowerShell script on the remote Hyper-V host. It delegates to
// the winrm library's RunPSWithContext, which UTF-16LE base64-encodes the
// script and invokes `powershell.exe -EncodedCommand <b64>` — this eliminates
// all shell-quoting hazards when the script embeds paths or quoted strings.
//
// On a non-zero exit code the stderr output is included in the returned error
// so callers get actionable diagnostics.
func (h *HyperV) runPS(ctx context.Context, script string) (string, error) {
	c, err := h.getClient()
	if err != nil {
		return "", err
	}
	stdout, stderr, code, err := c.RunPSWithContext(ctx, script)
	if err != nil {
		return "", fmt.Errorf("hyperv: winrm transport: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("hyperv: powershell exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}

// UploadISO transfers a local ISO to the host's ISOPath via WinRM by streaming
// the file in base64-encoded chunks (~2 MiB each). The first chunk creates (or
// truncates) the destination file; subsequent chunks append. The ProgressFunc is
// called after each chunk so the deploy UI progress bar stays live.
//
// NOTE: Streaming a 15–20 GiB Veeam ISO over WinRM with base64 encoding is
// slow (typically 35–60 min on a LAN). Administrators are strongly encouraged
// to pre-stage ISOs directly at cfg.ISOPath on the Windows host; FindISO will
// then short-circuit and skip the upload entirely.
func (h *HyperV) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	const chunkSize = 2 * 1024 * 1024 // 2 MiB raw; ~2.7 MiB base64

	name := filepath.Base(localPath)
	destPath := h.cfg.ISOPath + `\` + name

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("hyperv: UploadISO: open %q: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("hyperv: UploadISO: stat %q: %w", localPath, err)
	}
	total := st.Size()

	buf := make([]byte, chunkSize)
	var done int64
	first := true

	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("hyperv: UploadISO: %w", ctx.Err())
		}

		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			b64 := base64.StdEncoding.EncodeToString(chunk)

			var script string
			if first {
				// Create (or truncate) the destination file with the first chunk.
				script = fmt.Sprintf(`
$bytes = [Convert]::FromBase64String('%s')
$dest  = '%s'
$dir   = Split-Path $dest
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
[IO.File]::WriteAllBytes($dest, $bytes)
`, b64, destPath)
				first = false
			} else {
				// Append via a FileStream so the remote host never buffers the full file.
				script = fmt.Sprintf(`
$bytes = [Convert]::FromBase64String('%s')
$fs = [IO.File]::Open('%s', [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::None)
try { $fs.Write($bytes, 0, $bytes.Length) } finally { $fs.Close() }
`, b64, destPath)
			}

			if _, err := h.runPS(ctx, script); err != nil {
				return "", fmt.Errorf("hyperv: UploadISO: write chunk at offset %d: %w", done, err)
			}

			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return "", fmt.Errorf("hyperv: UploadISO: read %q: %w", localPath, readErr)
		}
	}

	if progress != nil {
		progress(total, total) // guarantee 100 %
	}
	return destPath, nil
}

// FindISO returns the host path of an ISO already present under cfg.ISOPath,
// or "" when absent. This allows the remote-kickstart flow to skip re-uploading
// Veeam base ISOs that have been pre-staged by the administrator.
func (h *HyperV) FindISO(ctx context.Context, name string) (string, error) {
	hostPath := h.cfg.ISOPath + `\` + name
	// Write-Output emits the path only when the file exists; an empty stdout
	// signals absence.
	script := fmt.Sprintf(
		`if (Test-Path '%s') { Write-Output '%s' }`,
		hostPath, hostPath,
	)
	out, err := h.runPS(ctx, script)
	if err != nil {
		return "", fmt.Errorf("hyperv: FindISO %q: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// CreateVM provisions a powered-off Generation 2 VM per spec and returns a
// VMRef whose ID is the VM's stable GUID (survives rename). UEFI (Gen 2) is
// always used; Secure Boot is disabled so the unsigned Veeam installer ISO
// boots. One dynamic VHDX per Disks entry is created at cfg.VMPath.
func (h *HyperV) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	if len(spec.Disks) == 0 {
		spec.Disks = []int{32} // safety default
	}

	memBytes := int64(spec.MemoryMiB) * 1024 * 1024

	// Build the per-disk VHD creation + attachment fragment.
	var diskPS strings.Builder
	for i, gib := range spec.Disks {
		sizeBytes := int64(gib) * 1024 * 1024 * 1024
		vhdPath := fmt.Sprintf(`%s\%s_%d.vhdx`, h.cfg.VMPath, spec.Name, i)
		fmt.Fprintf(&diskPS,
			"\nNew-VHD -Path '%s' -SizeBytes %d -Dynamic | Out-Null\n"+
				"Add-VMHardDiskDrive -VMName '%s' -Path '%s' -ControllerType SCSI\n",
			vhdPath, sizeBytes, spec.Name, vhdPath,
		)
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
New-VM -Name '%s' -MemoryStartupBytes %d -Generation 2 -SwitchName '%s' -Path '%s' | Out-Null
Set-VMProcessor      -VMName '%s' -Count %d
Set-VM               -VMName '%s' -StaticMemory -AutomaticCheckpointsEnabled $false
Set-VMFirmware       -VMName '%s' -EnableSecureBoot Off
%s
(Get-VM -Name '%s').Id.Guid
`, spec.Name, memBytes, h.cfg.SwitchName, h.cfg.VMPath,
		spec.Name, spec.CPUs,
		spec.Name,
		spec.Name,
		diskPS.String(),
		spec.Name,
	)

	out, err := h.runPS(ctx, script)
	if err != nil {
		return VMRef{}, fmt.Errorf("hyperv: CreateVM %q: %w", spec.Name, err)
	}
	guid := strings.TrimSpace(out)
	if guid == "" {
		return VMRef{}, fmt.Errorf("hyperv: CreateVM %q: empty GUID returned", spec.Name)
	}
	return VMRef{ID: guid, Node: h.cfg.Host}, nil
}

// vmName resolves a VMRef GUID to the VM display name via Get-VM -Id. The name
// is required by cmdlets that don't accept -Id directly.
func (h *HyperV) vmName(ctx context.Context, vm VMRef) (string, error) {
	script := fmt.Sprintf(`(Get-VM -Id '%s').Name`, vm.ID)
	out, err := h.runPS(ctx, script)
	if err != nil {
		return "", fmt.Errorf("hyperv: resolve VM name for %s: %w", vm.ID, err)
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", fmt.Errorf("hyperv: VM %s not found", vm.ID)
	}
	return name, nil
}

// AttachISO mounts isoRef (a host UNC/local path returned by UploadISO or
// FindISO) as the VM's DVD drive. If a DVD drive already exists its Path is
// updated; otherwise a new drive is added.
func (h *HyperV) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: AttachISO: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$dvd = Get-VMDvdDrive -VMName '%s' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($dvd) {
    Set-VMDvdDrive -VMName '%s' -ControllerNumber $dvd.ControllerNumber `+
		`-ControllerLocation $dvd.ControllerLocation -Path '%s'
} else {
    Add-VMDvdDrive -VMName '%s' -Path '%s'
}
`, name, name, isoRef, name, isoRef)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: AttachISO VM %s: %w", vm.ID, err)
	}
	return nil
}

// DetachISO removes the ISO path from the VM's DVD drive, leaving the virtual
// drive slot empty. Called by the orchestrator after the OS install completes.
func (h *HyperV) DetachISO(ctx context.Context, vm VMRef) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: DetachISO: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$dvd = Get-VMDvdDrive -VMName '%s' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($dvd) {
    Set-VMDvdDrive -VMName '%s' -ControllerNumber $dvd.ControllerNumber `+
		`-ControllerLocation $dvd.ControllerLocation -Path $null
}
`, name, name)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: DetachISO VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromCD places the DVD drive first in the Gen 2 firmware boot order so
// the ISO boots on the next power-on.
func (h *HyperV) SetBootFromCD(ctx context.Context, vm VMRef) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: SetBootFromCD: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$dvd = Get-VMDvdDrive -VMName '%s' | Select-Object -First 1
if (-not $dvd) { throw "no DVD drive on VM '%s'" }
Set-VMFirmware -VMName '%s' -FirstBootDevice $dvd
`, name, name, name)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: SetBootFromCD VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromDisk places the first SCSI hard disk drive first in the Gen 2
// firmware boot order so the installed OS boots after the install is complete.
func (h *HyperV) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: SetBootFromDisk: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$disk = Get-VMHardDiskDrive -VMName '%s' | Select-Object -First 1
if (-not $disk) { throw "no hard disk on VM '%s'" }
Set-VMFirmware -VMName '%s' -FirstBootDevice $disk
`, name, name, name)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: SetBootFromDisk VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOn starts the VM.
func (h *HyperV) PowerOn(ctx context.Context, vm VMRef) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: PowerOn: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
Start-VM -Name '%s'
`, name)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: PowerOn VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOff hard-stops the VM (equivalent to pulling the power cord). If the VM
// is already off the call is silently ignored.
func (h *HyperV) PowerOff(ctx context.Context, vm VMRef) error {
	name, err := h.vmName(ctx, vm)
	if err != nil {
		return fmt.Errorf("hyperv: PowerOff: %w", err)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s'
if ($vm.State -ne 'Off') {
    Stop-VM -Name '%s' -TurnOff -Force
}
`, name, name)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: PowerOff VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM.
func (h *HyperV) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	script := fmt.Sprintf(`(Get-VM -Id '%s').State`, vm.ID)
	out, err := h.runPS(ctx, script)
	if err != nil {
		return PowerUnknown, fmt.Errorf("hyperv: Status VM %s: %w", vm.ID, err)
	}
	switch strings.TrimSpace(out) {
	case "Running":
		return PowerRunning, nil
	case "Off":
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// Destroy hard-stops the VM (tolerating already-off), removes it, and deletes
// all VHDX files that were attached as SCSI hard disk drives.
func (h *HyperV) Destroy(ctx context.Context, vm VMRef) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Id '%s' -ErrorAction SilentlyContinue
if (-not $vm) { return }
# Collect attached VHDX paths before destroying the VM object.
$vhds = @(Get-VMHardDiskDrive -VMName $vm.Name | Select-Object -ExpandProperty Path)
# Hard-stop; ignore errors if already off.
if ($vm.State -ne 'Off') {
    Stop-VM -Name $vm.Name -TurnOff -Force -ErrorAction SilentlyContinue
}
Remove-VM -Name $vm.Name -Force
# Delete the VHDX files.
foreach ($v in $vhds) {
    if ($v -and (Test-Path $v)) { Remove-Item -Path $v -Force }
}
`, vm.ID)
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: Destroy VM %s: %w", vm.ID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SendKeys — PS/2 scancode injection via WMI Msvm_Keyboard
// ---------------------------------------------------------------------------

// qemuToScancode maps QEMU sendkey token names (as produced by
// internal/deploy/bootcmd.go KeysForText) to PS/2 set-1 make scancodes.
// Break codes are derived as make | 0x80 at call site.
// LeftShift wrapping (0x2A / 0xAA) for shifted tokens is handled in
// buildScancodes, not here.
var qemuToScancode = map[string]byte{
	// control
	"ret": 0x1C,
	"spc": 0x39,

	// letters a–z
	"a": 0x1E, "b": 0x30, "c": 0x2E, "d": 0x20, "e": 0x12,
	"f": 0x21, "g": 0x22, "h": 0x23, "i": 0x17, "j": 0x24,
	"k": 0x25, "l": 0x26, "m": 0x32, "n": 0x31, "o": 0x18,
	"p": 0x19, "q": 0x10, "r": 0x13, "s": 0x1F, "t": 0x14,
	"u": 0x16, "v": 0x2F, "w": 0x11, "x": 0x2D, "y": 0x15,
	"z": 0x2C,

	// digits 0–9
	"0": 0x0B, "1": 0x02, "2": 0x03, "3": 0x04, "4": 0x05,
	"5": 0x06, "6": 0x07, "7": 0x08, "8": 0x09, "9": 0x0A,

	// unshifted punctuation
	"slash":        0x35,
	"dot":          0x34,
	"comma":        0x33,
	"semicolon":    0x27,
	"equal":        0x0D,
	"minus":        0x0C,
	"apostrophe":   0x28,
	"grave_accent": 0x29,
}

// scancodeForShifted returns the base scancode for a "shift-X" token.  For
// "shift-<letter>" tokens the bare letter scancode is returned (the caller
// wraps with LeftShift).  For named shifted punctuation the underlying key's
// scancode is returned (e.g. "shift-semicolon" → semicolon scancode 0x27).
func scancodeForShifted(token string) (byte, bool) {
	bare := strings.TrimPrefix(token, "shift-")
	// Single lowercase letter → look up directly.
	if len(bare) == 1 {
		sc, ok := qemuToScancode[bare]
		return sc, ok
	}
	// Named punctuation: look up the unshifted base key.
	// The map below lists every shifted punctuation token from bootcmd.go and
	// maps it to the base key name whose scancode should be emitted.
	shiftedPunct := map[string]string{
		"semicolon":    "semicolon",    // : → ;
		"minus":        "minus",        // _ → -
		"equal":        "equal",        // + → =
		"slash":        "slash",        // ? → /
		"7":            "7",            // & → 7
		"5":            "5",            // % → 5
		"3":            "3",            // # → 3
		"9":            "9",            // ( → 9
		"0":            "0",            // ) → 0
		"apostrophe":   "apostrophe",   // " → '
		"grave_accent": "grave_accent", // ~ → `
	}
	if baseKey, ok := shiftedPunct[bare]; ok {
		sc, found := qemuToScancode[baseKey]
		return sc, found
	}
	return 0, false
}

const (
	scLeftShiftMake  byte = 0x2A
	scLeftShiftBreak byte = 0xAA
)

// buildScancodes converts a slice of QEMU key tokens into a flat byte slice of
// PS/2 set-1 make+break scancode pairs. LeftShift (0x2A/0xAA) is wrapped
// around any "shift-X" token. Unknown tokens are silently skipped (matching
// bootcmd.go KeysForText behaviour).
func buildScancodes(keys []string) []byte {
	var codes []byte
	for _, k := range keys {
		if strings.HasPrefix(k, "shift-") {
			sc, ok := scancodeForShifted(k)
			if !ok {
				continue
			}
			codes = append(codes,
				scLeftShiftMake, sc, sc|0x80, scLeftShiftBreak,
			)
			continue
		}
		sc, ok := qemuToScancode[k]
		if !ok {
			continue
		}
		codes = append(codes, sc, sc|0x80)
	}
	return codes
}

// SendKeys types a sequence of QEMU key tokens on the VM console via the
// Hyper-V WMI provider (root\virtualization\v2 → Msvm_ComputerSystem →
// Msvm_Keyboard.TypeScancodes). This mirrors the approach used by HashiCorp
// Packer's hyperv-iso builder.
//
// The full scancode sequence is sent in a single WMI call to minimise WinRM
// round trips. A brief pause after the call gives the guest firmware time to
// process the injected scancodes.
func (h *HyperV) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	codes := buildScancodes(keys)
	if len(codes) == 0 {
		return nil
	}

	// Render the scancode slice as a PowerShell byte-array literal.
	var sb strings.Builder
	sb.WriteString("[byte[]]@(")
	for i, b := range codes {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "0x%02X", b)
	}
	sb.WriteByte(')')
	scArray := sb.String()

	// The script:
	//  1. Finds the Msvm_ComputerSystem for the VM's GUID in the Hyper-V WMI
	//     namespace.
	//  2. Gets the associated Msvm_Keyboard instance.
	//  3. Calls TypeScancodes, which injects all scancodes synchronously.
	//
	// Msvm_ComputerSystem.Name == the VM GUID (without braces) for VM instances.
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$ns     = 'root\virtualization\v2'
$vmGuid = '%s'
$cs = Get-WmiObject -Namespace $ns -Class Msvm_ComputerSystem `+
		`-Filter "Name='$vmGuid'" | Select-Object -First 1
if (-not $cs) { throw "Msvm_ComputerSystem not found for GUID $vmGuid" }
$kb = ($cs.GetRelated('Msvm_Keyboard') | Select-Object -First 1)
if (-not $kb) { throw "Msvm_Keyboard not found for GUID $vmGuid" }
$ret = $kb.TypeScancodes(%s)
if ($ret.ReturnValue -ne 0) {
    throw "TypeScancodes returned $($ret.ReturnValue)"
}
`, vm.ID, scArray)

	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: SendKeys VM %s: %w", vm.ID, err)
	}

	// Brief pause so the guest GRUB menu has time to process the scancodes.
	select {
	case <-ctx.Done():
		return fmt.Errorf("hyperv: SendKeys VM %s: %w", vm.ID, ctx.Err())
	case <-time.After(60 * time.Millisecond):
	}
	return nil
}
