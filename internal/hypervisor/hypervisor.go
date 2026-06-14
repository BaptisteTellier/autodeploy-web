// Package hypervisor abstracts the operations the deploy orchestrator needs
// from a target hypervisor: make the customised ISO available, create a VM
// shell, attach the ISO as a boot CD-ROM, control boot order and power, and
// tear the VM down. The MVP implementation targets Proxmox VE; vSphere
// (govmomi) and Hyper-V (WinRM) can be added later behind this same interface.
//
// All methods take a context for cancellation/timeout and are expected to be
// driven from a single orchestration goroutine per deployment.
package hypervisor

import "context"

// PowerState is the coarse runtime state of a VM.
type PowerState string

const (
	PowerOff     PowerState = "off"
	PowerRunning PowerState = "running"
	PowerUnknown PowerState = "unknown"
)

// VMSpec describes the VM to create. Sizes are coarse; each provider maps them
// to its native units.
type VMSpec struct {
	Name      string
	CPUs      int
	MemoryMiB int
	Disks     []int  // one entry per disk, size in GiB (e.g. [256,256] for a VSA)
	Bridge    string // host network bridge / port group the NIC attaches to
	VLAN      int    // 0 = untagged
	UEFI      bool   // boot via OVMF/UEFI (adds q35 machine + an EFI vars disk)
}

// ProgressFunc reports transfer progress during a long upload: done and total
// bytes (total is 0 when the size is unknown). It is invoked periodically
// (throttled by the implementation), so it is safe to update UI state from it.
// Pass nil to opt out of progress reporting.
type ProgressFunc func(done, total int64)

// VMRef identifies a created VM within a provider. Its fields are opaque to the
// orchestrator and only meaningful to the implementation that produced it.
type VMRef struct {
	ID   string // provider-native id (e.g. Proxmox VMID)
	Node string // provider node/host the VM lives on (e.g. Proxmox cluster node)
}

// Hypervisor is the set of operations the deploy orchestrator drives against a
// target. Implementations must be safe for sequential use within one
// deployment; the orchestrator does not call a single VMRef concurrently.
type Hypervisor interface {
	// UploadISO makes a local ISO available to the hypervisor's storage and
	// returns a provider-native reference (e.g. "local:iso/foo.iso"). progress,
	// if non-nil, is called periodically with bytes sent / total during the
	// transfer (ISOs are 15–20 GB, so this drives the deploy progress bar).
	UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (isoRef string, err error)

	// FindISO returns the provider-native reference of an ISO already present
	// in the hypervisor's library ("" if absent). Used by the remote-kickstart
	// flow to avoid re-uploading the original Veeam ISOs.
	FindISO(ctx context.Context, name string) (isoRef string, err error)

	// SendKeys types a sequence of keys on the VM console (QEMU sendkey names:
	// "c", "spc", "slash", "shift-semicolon", "ret", …). This is the
	// Packer-style mechanism used to edit GRUB at boot for remote kickstart;
	// vSphere (PutUsbScanCodes) and Hyper-V (Msvm_Keyboard) offer equivalents.
	SendKeys(ctx context.Context, vm VMRef, keys []string) error

	// CreateVM provisions a powered-off VM shell per spec.
	CreateVM(ctx context.Context, spec VMSpec) (VMRef, error)

	// AttachISO mounts isoRef as the VM's boot CD-ROM.
	AttachISO(ctx context.Context, vm VMRef, isoRef string) error

	// DetachISO removes the CD-ROM, called once the unattended install is done.
	DetachISO(ctx context.Context, vm VMRef) error

	// SetBootFromCD makes the VM boot the CD-ROM first (for the install).
	SetBootFromCD(ctx context.Context, vm VMRef) error

	// SetBootFromDisk makes the VM boot the disk first (after the install).
	SetBootFromDisk(ctx context.Context, vm VMRef) error

	// SetBootDiskThenCD sets the boot order to disk first, CD-ROM second.
	// On a fresh VM the disk is empty so the firmware falls through to the CD;
	// after the installer finishes the disk has an OS and boots directly.
	// This avoids any runtime boot-order change after PowerOn.
	SetBootDiskThenCD(ctx context.Context, vm VMRef) error

	// PowerOn starts the VM.
	PowerOn(ctx context.Context, vm VMRef) error

	// PowerOff stops the VM.
	PowerOff(ctx context.Context, vm VMRef) error

	// Status returns the coarse power state of the VM.
	Status(ctx context.Context, vm VMRef) (PowerState, error)

	// Destroy removes the VM and its disks (used for cleanup / rollback).
	Destroy(ctx context.Context, vm VMRef) error

	// GetVMIP returns the first non-loopback, non-link-local IPv4 address
	// reported by the guest agent / VMware tools / guest metrics for the VM.
	// Returns ("", nil) when the agent is not yet ready or no IP is known.
	GetVMIP(ctx context.Context, vm VMRef) (string, error)
}
