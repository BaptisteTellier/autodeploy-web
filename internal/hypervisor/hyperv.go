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
	// Raise the WinRM envelope from the 150 KB default so the chunked ISO upload's
	// per-command payload isn't truncated client-side. The server still caps at
	// its MaxEnvelopeSizekb (default 500 KB), which is why UploadISO keeps chunks
	// small.
	params := winrm.NewParameters("PT60S", "en-US", 1024*1024)
	c, err := winrm.NewClientWithParameters(ep, h.cfg.Username, h.cfg.Password, params)
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
	// 64 KiB raw → ~235 KB per WinRM command (base64 + UTF-16 EncodedCommand),
	// well under the server's default 500 KB MaxEnvelopeSizekb (a 2 MiB chunk
	// produced a ~2.7 MB command → HTTP 413). ISO upload over WinRM is slow
	// regardless — pre-stage the ISO to skip it.
	const chunkSize = 64 * 1024

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
`, psLit(b64), psLit(destPath))
				first = false
			} else {
				// Append via a FileStream so the remote host never buffers the full file.
				script = fmt.Sprintf(`
$bytes = [Convert]::FromBase64String('%s')
$fs = [IO.File]::Open('%s', [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::None)
try { $fs.Write($bytes, 0, $bytes.Length) } finally { $fs.Close() }
`, psLit(b64), psLit(destPath))
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
		psLit(hostPath), psLit(hostPath),
	)
	out, err := h.runPS(ctx, script)
	if err != nil {
		return "", fmt.Errorf("hyperv: FindISO %q: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// CreateVM provisions a powered-off Generation 2 VM per spec and returns a
// VMRef whose ID is the VM's stable GUID (survives rename). The GUID is read
// straight off the object New-VM returns — never re-queried by name, which would
// match (and concatenate) multiple GUIDs when a same-named VM already exists.
//
// To avoid clashes with leftover VMs/files, a free name is chosen (a "-N" suffix
// is appended while a VM or folder of that name exists) and every VM gets its
// OWN folder under cfg.VMPath (config + VHDX files live there). UEFI (Gen 2) is
// used with Secure Boot off (the Veeam installer ISO is unsigned) and a virtual
// TPM is enabled. One dynamic VHDX per Disks entry is created in the VM folder.
func (h *HyperV) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	if len(spec.Disks) == 0 {
		spec.Disks = []int{32} // safety default
	}

	memBytes := int64(spec.MemoryMiB) * 1024 * 1024

	// Per-disk VHDX creation, inside the VM's own folder ($vmDir / $name resolved
	// in PowerShell once a free name is found). Files: <vmname>_<i>.vhdx.
	var diskPS strings.Builder
	for i, gib := range spec.Disks {
		sizeBytes := int64(gib) * 1024 * 1024 * 1024
		fmt.Fprintf(&diskPS,
			"$vhd = Join-Path $vmDir ('{0}_%d.vhdx' -f $name)\n"+
				"New-VHD -Path $vhd -SizeBytes %d -Dynamic | Out-Null\n"+
				"Add-VMHardDiskDrive -VM $vm -Path $vhd -ControllerType SCSI\n",
			i, sizeBytes,
		)
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$req  = '%s'
$base = '%s'
# Pick a free name so a leftover VM or folder never clashes (this is what made
# Get-VM -Name return two GUIDs, and New-VHD fail with "file exists").
$name = $req
$n = 2
while ((Get-VM -Name $name -ErrorAction SilentlyContinue) -or (Test-Path (Join-Path $base $name))) {
    $name = "$req-$n"; $n++
}
$vmDir = Join-Path $base $name
New-Item -ItemType Directory -Path $vmDir -Force | Out-Null
$vm = New-VM -Name $name -MemoryStartupBytes %d -Generation 2 -SwitchName '%s' -Path $vmDir
Set-VMProcessor -VM $vm -Count %d
Set-VM          -VM $vm -StaticMemory -AutomaticCheckpointsEnabled $false
Set-VMFirmware  -VM $vm -EnableSecureBoot Off
%s
# Enable a virtual TPM (local key protector works on standalone Hyper-V hosts).
Set-VMKeyProtector -VM $vm -NewLocalKeyProtector
Enable-VMTPM       -VM $vm
$vm.Id.Guid
`, psLit(spec.Name), psLit(h.cfg.VMPath), memBytes, psLit(h.cfg.SwitchName), spec.CPUs, diskPS.String())

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
	script := fmt.Sprintf(`(Get-VM -Id '%s').Name`, psLit(vm.ID))
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
`, psLit(name), psLit(name), psLit(isoRef), psLit(name), psLit(isoRef))
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
`, psLit(name), psLit(name))
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
`, psLit(name), name, psLit(name))
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
`, psLit(name), name, psLit(name))
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: SetBootFromDisk VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootDiskThenCD sets the explicit Gen 2 firmware boot order: hard disk
// first, DVD second, network adapter last. On a fresh empty disk the firmware
// falls through to the CD installer; after install the disk boots directly, and
// PXE is never attempted (NIC last).
func (h *HyperV) SetBootDiskThenCD(ctx context.Context, vm VMRef) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Id '%s'
$order = @()
$disk = Get-VMHardDiskDrive -VM $vm | Select-Object -First 1
if ($disk) { $order += $disk }
$dvd = Get-VMDvdDrive -VM $vm | Select-Object -First 1
if ($dvd) { $order += $dvd }
$net = Get-VMNetworkAdapter -VM $vm | Select-Object -First 1
if ($net) { $order += $net }
if ($order.Count -gt 0) { Set-VMFirmware -VM $vm -BootOrder $order }
`, psLit(vm.ID))
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: SetBootDiskThenCD VM %s: %w", vm.ID, err)
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
`, psLit(name))
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
`, psLit(name), psLit(name))
	if _, err := h.runPS(ctx, script); err != nil {
		return fmt.Errorf("hyperv: PowerOff VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM.
func (h *HyperV) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	script := fmt.Sprintf(`(Get-VM -Id '%s').State`, psLit(vm.ID))
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

// Destroy hard-stops the VM (tolerating already-off), removes it, deletes its
// VHDX files, and removes the VM's dedicated folder under VMPath so a later
// re-create doesn't fall back to a "-2" suffixed name.
func (h *HyperV) Destroy(ctx context.Context, vm VMRef) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$base = '%s'
$vm = Get-VM -Id '%s' -ErrorAction SilentlyContinue
if (-not $vm) { return }
# Capture name / folder / disks BEFORE the VM object is destroyed.
$vmName = $vm.Name
$vmDir  = $vm.Path
$vhds   = @(Get-VMHardDiskDrive -VM $vm | Select-Object -ExpandProperty Path)
# Hard-stop; ignore errors if already off.
if ($vm.State -ne 'Off') {
    Stop-VM -VM $vm -TurnOff -Force -ErrorAction SilentlyContinue
}
Remove-VM -VM $vm -Force
# Delete the VHDX files.
foreach ($v in $vhds) {
    if ($v -and (Test-Path $v)) { Remove-Item -Path $v -Force -ErrorAction SilentlyContinue }
}
# Remove a folder only when it is STRICTLY inside $base (never the shared VMPath
# root — protects older flat-layout VMs). Retry briefly: vmms may still hold
# file locks for a moment right after Remove-VM.
$root = [IO.Path]::GetFullPath($base).TrimEnd('\')
function Remove-VMFolder($p) {
    if (-not $p) { return }
    $full = [IO.Path]::GetFullPath($p).TrimEnd('\')
    if (-not $full.StartsWith($root + '\', [System.StringComparison]::OrdinalIgnoreCase)) { return }
    for ($i = 0; $i -lt 5 -and (Test-Path $full); $i++) {
        try { Remove-Item -Path $full -Recurse -Force -ErrorAction Stop; break }
        catch { Start-Sleep -Milliseconds 500 }
    }
}
# Both the path Hyper-V reports and the folder we create at <base>\<name>.
Remove-VMFolder $vmDir
Remove-VMFolder (Join-Path $base $vmName)
`, psLit(h.cfg.VMPath), psLit(vm.ID))
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

// maxScancodesPerCall bounds how many scancode bytes go into one TypeScancodes
// call. runPS ships the script via `powershell -EncodedCommand`, which the winrm
// library wraps in cmd.exe — and cmd.exe caps the command line at ~8 KB. Each
// byte renders as "0xXX," (5 chars) and the whole script is then UTF-16LE+base64
// encoded (~2.7x), so a full GRUB line in one call overflows the limit ("The
// command line is too long."). 384 bytes → ~2 KB of array text → comfortably
// under 8 KB after encoding.
const maxScancodesPerCall = 384

// SendKeys types a sequence of QEMU key tokens on the VM console via the
// Hyper-V WMI provider (root\virtualization\v2 → Msvm_ComputerSystem →
// Msvm_Keyboard.TypeScancodes). This mirrors the approach used by HashiCorp
// Packer's hyperv-iso builder.
//
// The scancodes are sent in bounded chunks (see maxScancodesPerCall) to keep
// each WinRM command line under cmd.exe's ~8 KB limit; chunks are injected in
// order, so GRUB sees one continuous keystroke stream. A brief pause after the
// last chunk gives the guest firmware time to process the injected scancodes.
func (h *HyperV) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	codes := buildScancodes(keys)
	if len(codes) == 0 {
		return nil
	}

	for start := 0; start < len(codes); start += maxScancodesPerCall {
		end := start + maxScancodesPerCall
		if end > len(codes) {
			end = len(codes)
		}
		if err := h.sendScancodes(ctx, vm, codes[start:end]); err != nil {
			return fmt.Errorf("hyperv: SendKeys VM %s: %w", vm.ID, err)
		}
	}

	// Brief pause so the guest GRUB menu has time to process the scancodes.
	select {
	case <-ctx.Done():
		return fmt.Errorf("hyperv: SendKeys VM %s: %w", vm.ID, ctx.Err())
	case <-time.After(60 * time.Millisecond):
	}
	return nil
}

// sendScancodes injects one chunk of PS/2 scancodes via a single TypeScancodes
// WMI call. The script:
//  1. Finds the Msvm_ComputerSystem for the VM's GUID in the Hyper-V WMI
//     namespace (Msvm_ComputerSystem.Name == the VM GUID without braces).
//  2. Gets the associated Msvm_Keyboard instance.
//  3. Calls TypeScancodes, which injects the chunk synchronously.
func (h *HyperV) sendScancodes(ctx context.Context, vm VMRef, codes []byte) error {
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
`, psLit(vm.ID), scArray)

	_, err := h.runPS(ctx, script)
	return err
}

// GetVMIP returns the first non-loopback, non-link-local IPv4 address of any
// NIC on the VM, as seen by the Hyper-V integration services.
// Returns ("", nil) when integration services are not yet running.
func (h *HyperV) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	// Resolve the VM by GUID first, then read its NICs' reported addresses.
	// (Get-VMNetworkAdapter has no -VMId parameter — using it errored silently
	// and made DHCP nodes poll forever.)
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Id '%s' -ErrorAction SilentlyContinue
if ($vm) {
    $addrs = @($vm.NetworkAdapters.IPAddresses) |
        Where-Object { $_ -match '^\d+\.\d+\.\d+\.\d+$' } |
        Where-Object { $_ -notlike '127.*' -and $_ -notlike '169.254.*' -and $_ -ne '0.0.0.0' }
    if ($addrs) { ($addrs | Select-Object -First 1).Trim() }
}
`, psLit(vm.ID))
	out, err := h.runPS(ctx, script)
	if err != nil {
		// Integration services not ready — not a hard error.
		return "", nil
	}
	return strings.TrimSpace(out), nil
}
