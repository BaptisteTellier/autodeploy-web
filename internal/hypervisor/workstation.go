package hypervisor

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/masterzen/winrm"
)

// WorkstationConfig holds WinRM connection settings for a Windows host running
// VMware Workstation, plus placement and network options.
type WorkstationConfig struct {
	// WinRM target — the Windows machine running VMware Workstation.
	Host     string
	Username string
	Password string
	Port     int  // 0 = auto (5985 HTTP / 5986 HTTPS)
	HTTPS    bool // use HTTPS WinRM transport
	Insecure bool // skip TLS verification when HTTPS

	// InstallDir is the VMware Workstation install directory on the Windows
	// host. vmrun.exe and vmware-vdiskmanager.exe live side by side in it, so
	// a single directory is enough — the executables are derived from it.
	// Default is applied by NewWorkstation when empty.
	InstallDir string // default: C:\Program Files\VMware\VMware Workstation

	// Storage layout on the Windows host.
	VMBaseDir string // parent dir where per-VM folders are created, e.g. C:\VMs
	ISODir    string // dir where ISOs are uploaded / looked up

	// Networking.
	VNet string // Workstation virtual network name (e.g. vmnet0); required, no default

	// VNC access — the container uses these to inject keystrokes.
	VNCHost     string // hostname/IP the container can reach for VNC (default = Host)
	VNCPortBase int    // first VNC port to assign (default 5910)
}

// Workstation is a Hypervisor implementation that drives VMware Workstation on
// a Windows host over WinRM. vmrun.exe and vmware-vdiskmanager.exe are invoked
// via PowerShell's call operator; VNC is used for keystroke injection (remote
// kickstart).
type Workstation struct {
	cfg      WorkstationConfig
	mu       sync.Mutex
	client   *winrm.Client  // lazily initialised; guarded by mu
	vncPorts map[string]int // vmxPath → VNC port; guarded by mu
	nextPort int            // next VNC port to hand out; guarded by mu
}

// compile-time assertion that *Workstation satisfies the Hypervisor interface.
var _ Hypervisor = (*Workstation)(nil)

// NewWorkstation constructs a Workstation backend from cfg, applying defaults
// and validating required fields.
func NewWorkstation(cfg WorkstationConfig) (*Workstation, error) {
	// Apply defaults.
	if cfg.InstallDir == "" {
		cfg.InstallDir = `C:\Program Files\VMware\VMware Workstation`
	}
	if cfg.VNCPortBase == 0 {
		cfg.VNCPortBase = 5910
	}
	if cfg.VNCHost == "" {
		cfg.VNCHost = cfg.Host
	}

	// Validate required fields.
	if cfg.Host == "" {
		return nil, fmt.Errorf("workstation: Host is required")
	}
	if cfg.VMBaseDir == "" {
		return nil, fmt.Errorf("workstation: VMBaseDir is required")
	}
	if cfg.VNet == "" {
		return nil, fmt.Errorf("workstation: VNet is required")
	}

	return &Workstation{
		cfg:      cfg,
		vncPorts: make(map[string]int),
		nextPort: cfg.VNCPortBase,
	}, nil
}

// vmRun returns the full path to vmrun.exe on the Windows host, derived from
// the configured install directory.
func (w *Workstation) vmRun() string {
	return joinWin(w.cfg.InstallDir, "vmrun.exe")
}

// vdiskManager returns the full path to vmware-vdiskmanager.exe on the Windows
// host, derived from the configured install directory.
func (w *Workstation) vdiskManager() string {
	return joinWin(w.cfg.InstallDir, "vmware-vdiskmanager.exe")
}

// joinWin joins a Windows directory and filename with a single backslash,
// trimming any trailing separators from dir. filepath.Join is unsafe here
// because the container runs on Linux and would use "/".
func joinWin(dir, name string) string {
	return strings.TrimRight(dir, `\/`) + `\` + name
}

// winrmPort returns the WinRM port to connect on.
func (w *Workstation) winrmPort() int {
	if w.cfg.Port > 0 {
		return w.cfg.Port
	}
	if w.cfg.HTTPS {
		return 5986
	}
	return 5985
}

// getClient lazily builds and caches the WinRM client. The client itself does
// not hold an open TCP connection; shells (and connections) are created per-
// command inside RunPSWithContext.
func (w *Workstation) getClient() (*winrm.Client, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil {
		return w.client, nil
	}
	ep := winrm.NewEndpoint(
		w.cfg.Host,
		w.winrmPort(),
		w.cfg.HTTPS,
		w.cfg.Insecure,
		nil, nil, nil,
		0,
	)
	c, err := winrm.NewClient(ep, w.cfg.Username, w.cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("workstation: build winrm client: %w", err)
	}
	w.client = c
	return c, nil
}

// runPS runs a PowerShell script on the remote Windows host. It uses the winrm
// library's RunPSWithContext, which UTF-16LE base64-encodes the script and
// invokes `powershell.exe -EncodedCommand <b64>`, eliminating quoting hazards.
//
// Non-zero exit codes produce an error that includes both stderr and stdout.
// vmrun.exe writes its error messages to stdout rather than stderr, so stderr
// alone is often empty on failure — both streams are surfaced.
func (w *Workstation) runPS(ctx context.Context, script string) (string, error) {
	c, err := w.getClient()
	if err != nil {
		return "", err
	}
	stdout, stderr, code, err := c.RunPSWithContext(ctx, script)
	if err != nil {
		return "", fmt.Errorf("workstation: winrm transport: %w", err)
	}
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if out := strings.TrimSpace(stdout); out != "" {
			if detail != "" {
				detail += "; "
			}
			detail += out
		}
		if detail == "" {
			detail = "(no output)"
		}
		return "", fmt.Errorf("workstation: powershell exit %d: %s", code, detail)
	}
	return strings.TrimSpace(stdout), nil
}

// UploadISO transfers a local ISO to the host's ISODir via WinRM using
// base64-chunked upload (same approach as hyperv.go). The first chunk creates
// (or truncates) the destination file; subsequent chunks append.
func (w *Workstation) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	const chunkSize = 2 * 1024 * 1024 // 2 MiB raw (~2.7 MiB base64)

	name := filepath.Base(localPath)
	destPath := w.cfg.ISODir + `\` + name

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("workstation: UploadISO: open %q: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("workstation: UploadISO: stat %q: %w", localPath, err)
	}
	total := st.Size()

	buf := make([]byte, chunkSize)
	var done int64
	first := true

	for {
		if ctx.Err() != nil {
			return "", fmt.Errorf("workstation: UploadISO: %w", ctx.Err())
		}

		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			b64 := base64.StdEncoding.EncodeToString(chunk)

			var script string
			if first {
				script = fmt.Sprintf(`
$bytes = [Convert]::FromBase64String('%s')
$dest  = '%s'
$dir   = Split-Path $dest
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
[IO.File]::WriteAllBytes($dest, $bytes)
`, b64, destPath)
				first = false
			} else {
				script = fmt.Sprintf(`
$bytes = [Convert]::FromBase64String('%s')
$fs = [IO.File]::Open('%s', [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::None)
try { $fs.Write($bytes, 0, $bytes.Length) } finally { $fs.Close() }
`, b64, destPath)
			}

			if _, err := w.runPS(ctx, script); err != nil {
				return "", fmt.Errorf("workstation: UploadISO: write chunk at offset %d: %w", done, err)
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
			return "", fmt.Errorf("workstation: UploadISO: read %q: %w", localPath, readErr)
		}
	}

	if progress != nil {
		progress(total, total)
	}
	return destPath, nil
}

// FindISO returns the host path of an ISO already present under cfg.ISODir,
// or "" when absent.
func (w *Workstation) FindISO(ctx context.Context, name string) (string, error) {
	hostPath := w.cfg.ISODir + `\` + name
	script := fmt.Sprintf(
		`if (Test-Path '%s') { Write-Output '%s' }`,
		hostPath, hostPath,
	)
	out, err := w.runPS(ctx, script)
	if err != nil {
		return "", fmt.Errorf("workstation: FindISO %q: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// buildVMX returns the .vmx file contents for a new Workstation VM. It is a
// pure function (no WinRM calls) so it can be unit-tested without a host.
func buildVMX(name string, spec VMSpec, isoPath, vnet string, vncPort int) string {
	var sb strings.Builder

	sb.WriteString(`.encoding = "UTF-8"` + "\n")
	sb.WriteString(`config.version = "8"` + "\n")
	sb.WriteString(`virtualHW.version = "19"` + "\n")
	fmt.Fprintf(&sb, `displayName = "%s"`+"\n", name)
	sb.WriteString(`guestOS = "rhel8-64"` + "\n")
	sb.WriteString(`firmware = "efi"` + "\n")
	// Auto-answer Workstation's first-power-on questions (the UUID
	// moved/copied prompt, hardware-upgrade prompt) so vmrun can start the VM
	// headless (nogui) without a GUI dialog blocking it — otherwise PowerOn
	// fails with "Error: The operation was canceled".
	sb.WriteString(`msg.autoAnswer = "TRUE"` + "\n")
	fmt.Fprintf(&sb, `numvcpus = "%d"`+"\n", spec.CPUs)
	fmt.Fprintf(&sb, `memsize = "%d"`+"\n", spec.MemoryMiB)

	// SCSI controller.
	sb.WriteString(`scsi0.present = "TRUE"` + "\n")
	sb.WriteString(`scsi0.virtualDev = "lsilogic"` + "\n")

	// One scsi0:<i> block per disk.
	disks := spec.Disks
	if len(disks) == 0 {
		disks = []int{32}
	}
	for i := range disks {
		fmt.Fprintf(&sb, `scsi0:%d.present = "TRUE"`+"\n", i)
		fmt.Fprintf(&sb, `scsi0:%d.fileName = "disk%d.vmdk"`+"\n", i, i)
	}

	// PCIe root ports. Without these a UEFI VM has no PCIe slot for the e1000e
	// NIC and power-on fails with "No PCIe slot available for Ethernet0"
	// (followed by a VMX crash). This mirrors the bridge block VMware writes
	// into its own .vmx files.
	sb.WriteString(`pciBridge0.present = "TRUE"` + "\n")
	for _, n := range []int{4, 5, 6, 7} {
		fmt.Fprintf(&sb, `pciBridge%d.present = "TRUE"`+"\n", n)
		fmt.Fprintf(&sb, `pciBridge%d.virtualDev = "pcieRootPort"`+"\n", n)
		fmt.Fprintf(&sb, `pciBridge%d.functions = "8"`+"\n", n)
	}

	// SATA controller + CD-ROM.
	sb.WriteString(`sata0.present = "TRUE"` + "\n")
	sb.WriteString(`sata0:1.present = "TRUE"` + "\n")
	sb.WriteString(`sata0:1.deviceType = "cdrom-image"` + "\n")
	fmt.Fprintf(&sb, `sata0:1.fileName = "%s"`+"\n", isoPath)
	sb.WriteString(`sata0:1.startConnected = "TRUE"` + "\n")

	// Network adapter.
	sb.WriteString(`ethernet0.present = "TRUE"` + "\n")
	sb.WriteString(`ethernet0.connectionType = "custom"` + "\n")
	fmt.Fprintf(&sb, `ethernet0.vnet = "%s"`+"\n", vnet)
	sb.WriteString(`ethernet0.virtualDev = "e1000e"` + "\n")
	sb.WriteString(`ethernet0.addressType = "generated"` + "\n")

	// Default boot order: disk first, CD-ROM second.
	// Note: on EFI/UEFI VMs, bios.bootOrder is a best-effort hint on
	// VMware Workstation; the actual EFI boot order may differ.
	sb.WriteString(`bios.bootOrder = "hdd,cdrom"` + "\n")

	// VNC.
	sb.WriteString(`RemoteDisplay.vnc.enabled = "TRUE"` + "\n")
	fmt.Fprintf(&sb, `RemoteDisplay.vnc.port = "%d"`+"\n", vncPort)

	return sb.String()
}

// patchVMXLine reads the .vmx file on the remote host and replaces or appends
// a single key = value line. The replacement is done via a PowerShell regex
// so no intermediate file is needed.
//
// Note: the PowerShell script uses "`r`n" for CRLF line endings inside the
// PowerShell string; these must be in an interpreted Go string (not a raw
// string literal) to avoid terminating the backtick-delimited Go string early.
func (w *Workstation) patchVMXLine(ctx context.Context, vmxPath, key, value string) error {
	// Build the script using concatenation so that PowerShell's backtick
	// escapes do not accidentally terminate the Go raw string literal.
	script := fmt.Sprintf(
		"$ErrorActionPreference = 'Stop'\n"+
			"$path = '%s'\n"+
			"$key  = '%s'\n"+
			"$val  = '%s'\n"+
			"$content = Get-Content -LiteralPath $path -Raw\n"+
			"$line = $key + ' = \"' + $val + '\"'\n"+
			"if ($content -match [regex]::Escape($key + ' =')) {\n"+
			"    $pattern = '(?m)^' + [regex]::Escape($key) + '\\s*=.*'\n"+
			"    $content = [regex]::Replace($content, $pattern, [System.Text.RegularExpressions.MatchEvaluator]{ param($m) $line })\n"+
			"} else {\n"+
			"    $content = $content.TrimEnd() + \"`r`n\" + $line + \"`r`n\"\n"+
			"}\n"+
			"Set-Content -LiteralPath $path -Value $content -NoNewline -Encoding UTF8\n",
		vmxPath, key, value,
	)
	if _, err := w.runPS(ctx, script); err != nil {
		return fmt.Errorf("workstation: patchVMXLine %q key %q: %w", vmxPath, key, err)
	}
	return nil
}

// CreateVM provisions a powered-off VM per spec. It:
//  1. Picks a collision-free folder name under VMBaseDir.
//  2. Builds vmdk files via vmware-vdiskmanager.exe.
//  3. Writes the .vmx file via Set-Content.
//
// To stop two nodes that share a hostname (e.g. two VIA-Proxies) from clobbering
// each other's folder/.vmx, a free name is chosen by appending "-N" while a
// folder of that name already exists — mirroring the Hyper-V backend. The pick
// and the create happen in one host-side script, and the resolved .vmx path is
// returned as the VMRef ID (so every later op targets the right VM).
func (w *Workstation) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	if len(spec.Disks) == 0 {
		spec.Disks = []int{32}
	}

	// Allocate a VNC port up front; it is bound to the resolved .vmx path once
	// the host has chosen a collision-free folder name below.
	w.mu.Lock()
	vncPort := w.nextPort
	w.nextPort++
	w.mu.Unlock()

	// Build the .vmx with a display-name placeholder; the host substitutes the
	// actual (possibly "-N" suffixed) name it picks. The placeholder is the only
	// occurrence of the name in buildVMX output (displayName).
	const namePlaceholder = "__AUTODEPLOY_VMNAME__"
	vmxContent := buildVMX(namePlaceholder, spec, "", w.cfg.VNet, vncPort)

	// Per-disk vmware-vdiskmanager commands. Disk paths are resolved against
	// $vmDir on the host (relative names, so they live in the VM's own folder).
	var diskCmds strings.Builder
	vdisk := w.vdiskManager()
	for i, gib := range spec.Disks {
		fmt.Fprintf(&diskCmds,
			"& '%s' -c -s %dGB -a lsilogic -t 0 (Join-Path $vmDir 'disk%d.vmdk') | Out-Null\n"+
				"if ($LASTEXITCODE -ne 0) { throw 'vmware-vdiskmanager failed for disk%d.vmdk' }\n",
			vdisk, gib, i, i,
		)
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$base = '%s'
$req  = '%s'
# Pick a folder name that does not collide with an existing VM folder, so two
# nodes that share a hostname each get their own VM (mirrors the Hyper-V "-N"
# scheme). Pick + create live in one script so the chosen name is used at once.
$name = $req
$n = 2
while (Test-Path (Join-Path $base $name)) { $name = "$req-$n"; $n++ }
$vmDir = Join-Path $base $name
New-Item -ItemType Directory -Path $vmDir -Force | Out-Null
%s
$vmx = Join-Path $vmDir ($name + '.vmx')
$vmxContent = @'
%s
'@
$vmxContent = $vmxContent.Replace('%s', $name)
Set-Content -LiteralPath $vmx -Value $vmxContent -Encoding UTF8
Write-Output $vmx
`, w.cfg.VMBaseDir, spec.Name, diskCmds.String(), vmxContent, namePlaceholder)

	out, err := w.runPS(ctx, script)
	if err != nil {
		return VMRef{}, fmt.Errorf("workstation: CreateVM %q: %w", spec.Name, err)
	}
	vmxPath := strings.TrimSpace(out)
	if vmxPath == "" {
		return VMRef{}, fmt.Errorf("workstation: CreateVM %q: empty .vmx path returned", spec.Name)
	}

	// Bind the allocated VNC port to the resolved .vmx path.
	w.mu.Lock()
	w.vncPorts[vmxPath] = vncPort
	w.mu.Unlock()

	return VMRef{ID: vmxPath, Node: w.cfg.Host}, nil
}

// AttachISO sets the CD-ROM image path in the .vmx and marks it present.
// The VM must be powered off.
func (w *Workstation) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	if err := w.patchVMXLine(ctx, vm.ID, "sata0:1.fileName", isoRef); err != nil {
		return fmt.Errorf("workstation: AttachISO: %w", err)
	}
	if err := w.patchVMXLine(ctx, vm.ID, "sata0:1.present", "TRUE"); err != nil {
		return fmt.Errorf("workstation: AttachISO: %w", err)
	}
	return nil
}

// DetachISO marks the CD-ROM drive not present in the .vmx.
// The VM must be powered off.
func (w *Workstation) DetachISO(ctx context.Context, vm VMRef) error {
	if err := w.patchVMXLine(ctx, vm.ID, "sata0:1.present", "FALSE"); err != nil {
		return fmt.Errorf("workstation: DetachISO: %w", err)
	}
	return nil
}

// SetBootFromCD sets the bios.bootOrder to "cdrom,hdd".
// Note: EFI boot order on VMware Workstation is best-effort via bios.bootOrder.
func (w *Workstation) SetBootFromCD(ctx context.Context, vm VMRef) error {
	if err := w.patchVMXLine(ctx, vm.ID, "bios.bootOrder", "cdrom,hdd"); err != nil {
		return fmt.Errorf("workstation: SetBootFromCD: %w", err)
	}
	return nil
}

// SetBootFromDisk sets the bios.bootOrder to "hdd,cdrom".
// Note: EFI boot order on VMware Workstation is best-effort via bios.bootOrder.
func (w *Workstation) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	if err := w.patchVMXLine(ctx, vm.ID, "bios.bootOrder", "hdd,cdrom"); err != nil {
		return fmt.Errorf("workstation: SetBootFromDisk: %w", err)
	}
	return nil
}

// SetBootDiskThenCD sets the bios.bootOrder to "hdd,cdrom" (disk first, then
// CD-ROM). On a fresh empty disk the firmware falls through to the CD-ROM
// installer; after install the disk boots directly.
// Note: EFI boot order on VMware Workstation is best-effort via bios.bootOrder.
func (w *Workstation) SetBootDiskThenCD(ctx context.Context, vm VMRef) error {
	if err := w.patchVMXLine(ctx, vm.ID, "bios.bootOrder", "hdd,cdrom"); err != nil {
		return fmt.Errorf("workstation: SetBootDiskThenCD: %w", err)
	}
	return nil
}

// PowerOn starts the VM and waits for it to appear in `vmrun list`.
//
// It does NOT run `vmrun start` inline. Over WinRM, vmrun starts vmware-vmx
// inside the WinRM command's job object; when the command returns the job is
// torn down and vmware-vmx is killed — the VM powers on then immediately stops
// (vmware.log shows the automation pipe closing with error 10054). Instead we
// spawn vmrun via WMI Win32_Process.Create: the new process is owned by
// WmiPrvSE, not the WinRM job, so the VM survives the command returning. We
// then poll `vmrun list` to confirm it actually came up (the WMI create only
// reports that the process was launched, not vmrun's own exit status).
func (w *Workstation) PowerOn(ctx context.Context, vm VMRef) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vmrun = '%s'
$vmx   = '%s'
$cmd = '"' + $vmrun + '" -T ws start "' + $vmx + '" nogui'
$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $cmd }
if ($r.ReturnValue -ne 0) { throw "Win32_Process.Create returned $($r.ReturnValue)" }
$ok = $false
$deadline = (Get-Date).AddSeconds(20)
while (-not $ok -and (Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 1000
    $list = & $vmrun -T ws list 2>$null
    if ($list -match [regex]::Escape($vmx)) { $ok = $true }
}
if (-not $ok) { throw "VM did not power on within 20s (not listed by vmrun)" }
`, w.vmRun(), vm.ID)
	if _, err := w.runPS(ctx, script); err != nil {
		return fmt.Errorf("workstation: PowerOn VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOff hard-stops the VM using vmrun -T ws stop. If the VM is already off
// the error from vmrun is silently ignored.
func (w *Workstation) PowerOff(ctx context.Context, vm VMRef) error {
	script := fmt.Sprintf(`
& '%s' -T ws stop '%s' hard 2>&1 | Out-Null
`, w.vmRun(), vm.ID)
	if _, err := w.runPS(ctx, script); err != nil {
		return fmt.Errorf("workstation: PowerOff VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM by checking whether the .vmx
// path appears in `vmrun list` output.
func (w *Workstation) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	script := fmt.Sprintf(`& '%s' -T ws list`, w.vmRun())
	out, err := w.runPS(ctx, script)
	if err != nil {
		return PowerUnknown, fmt.Errorf("workstation: Status VM %s: %w", vm.ID, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), strings.TrimSpace(vm.ID)) {
			return PowerRunning, nil
		}
	}
	return PowerOff, nil
}

// Destroy stops the VM (ignoring errors) then calls vmrun deleteVM. It also
// best-effort removes the VM directory via Remove-Item.
func (w *Workstation) Destroy(ctx context.Context, vm VMRef) error {
	// Stop (ignore error — VM may already be off).
	stopScript := fmt.Sprintf(`
& '%s' -T ws stop '%s' hard 2>&1 | Out-Null
`, w.vmRun(), vm.ID)
	_, _ = w.runPS(ctx, stopScript)

	// Delete the VM via vmrun.
	vmDir := vmxDir(vm.ID)
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
& '%s' -T ws deleteVM '%s'
if (Test-Path '%s') {
    Remove-Item -Path '%s' -Recurse -Force -ErrorAction SilentlyContinue
}
`, w.vmRun(), vm.ID, vmDir, vmDir)
	if _, err := w.runPS(ctx, script); err != nil {
		return fmt.Errorf("workstation: Destroy VM %s: %w", vm.ID, err)
	}

	// Clean up local VNC port mapping.
	w.mu.Lock()
	delete(w.vncPorts, vm.ID)
	w.mu.Unlock()

	return nil
}

// vmxDir returns the directory containing the .vmx file.
func vmxDir(vmxPath string) string {
	// Use forward-slash safe split: find last backslash or forward slash.
	for i := len(vmxPath) - 1; i >= 0; i-- {
		if vmxPath[i] == '\\' || vmxPath[i] == '/' {
			return vmxPath[:i]
		}
	}
	return vmxPath
}

// GetVMIP returns the IP address reported by vmrun getGuestIPAddress.
// Returns ("", nil) when VMware Tools are not yet ready or vmrun reports an
// error.
func (w *Workstation) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	script := fmt.Sprintf(`& '%s' -T ws getGuestIPAddress '%s'`, w.vmRun(), vm.ID)
	out, err := w.runPS(ctx, script)
	if err != nil {
		// Tools not ready — not a hard error.
		return "", nil
	}
	ip := strings.TrimSpace(out)
	// vmrun prints "Error: ..." when not ready.
	if strings.HasPrefix(ip, "Error") || ip == "" {
		return "", nil
	}
	// Validate that the output looks like an IPv4 address.
	if !isIPv4(ip) {
		return "", nil
	}
	return ip, nil
}

// isIPv4 reports whether s is a dotted-decimal IPv4 address.
var ipv4Re = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)

func isIPv4(s string) bool {
	return ipv4Re.MatchString(s)
}

// vncPortForVM returns the VNC port for the given .vmx path. It first checks
// the in-memory map; if absent, it reads the port from the .vmx file on the
// remote host.
func (w *Workstation) vncPortForVM(ctx context.Context, vmxPath string) (int, error) {
	w.mu.Lock()
	if p, ok := w.vncPorts[vmxPath]; ok {
		w.mu.Unlock()
		return p, nil
	}
	w.mu.Unlock()

	// Read from .vmx file.
	script := fmt.Sprintf(`
$content = Get-Content -LiteralPath '%s' -Raw
if ($content -match 'RemoteDisplay\.vnc\.port\s*=\s*"(\d+)"') {
    $matches[1]
}
`, vmxPath)
	out, err := w.runPS(ctx, script)
	if err != nil {
		return 0, fmt.Errorf("workstation: read VNC port from %q: %w", vmxPath, err)
	}
	portStr := strings.TrimSpace(out)
	if portStr == "" {
		return 0, fmt.Errorf("workstation: RemoteDisplay.vnc.port not found in %q", vmxPath)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("workstation: parse VNC port %q: %w", portStr, err)
	}

	// Cache it.
	w.mu.Lock()
	w.vncPorts[vmxPath] = port
	w.mu.Unlock()
	return port, nil
}

// SendKeys types a sequence of QEMU key tokens on the VM console via VNC (RFB
// protocol). The VNC server is the one embedded in VMware Workstation, enabled
// via the RemoteDisplay.vnc.* .vmx keys.
func (w *Workstation) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	port, err := w.vncPortForVM(ctx, vm.ID)
	if err != nil {
		return fmt.Errorf("workstation: SendKeys VM %s: %w", vm.ID, err)
	}
	addr := net.JoinHostPort(w.cfg.VNCHost, strconv.Itoa(port))
	if err := sendVNCKeys(ctx, addr, keys); err != nil {
		return fmt.Errorf("workstation: SendKeys VM %s: %w", vm.ID, err)
	}
	return nil
}
