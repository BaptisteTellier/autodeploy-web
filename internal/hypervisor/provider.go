package hypervisor

import "errors"

// Provider identifies a supported hypervisor backend selected in the deploy UI.
type Provider string

const (
	ProviderProxmox     Provider = "proxmox"
	ProviderVSphere     Provider = "vsphere"
	ProviderHyperV      Provider = "hyperv"
	ProviderNutanix     Provider = "nutanix"
	ProviderXCPng       Provider = "xcpng"
	ProviderWorkstation Provider = "workstation"
)

// ErrKickstartUnsupported is returned by SendKeys on providers that expose no
// keystroke-injection API (Nutanix AHV, XCP-ng). The deploy orchestrator and the
// launch handler use SupportsKickstart to reject a remote-kickstart request for
// such a provider up front, so this is a defensive backstop.
var ErrKickstartUnsupported = errors.New("hypervisor: remote kickstart (key injection) is not supported by this provider")

// SupportsKickstart reports whether the provider can type the GRUB boot command
// at the console for remote kickstart. Proxmox (QEMU sendkey), vSphere
// (PutUsbScanCodes) and Hyper-V (Msvm_Keyboard) can; Nutanix AHV and XCP-ng have
// no such API, so on those the user must deploy a pre-customised ISO (classic
// mode) instead.
func SupportsKickstart(p Provider) bool {
	switch p {
	case ProviderProxmox, ProviderVSphere, ProviderHyperV, ProviderWorkstation:
		return true
	default:
		return false
	}
}

// KnownProvider reports whether p is one of the supported backends.
func KnownProvider(p Provider) bool {
	switch p {
	case ProviderProxmox, ProviderVSphere, ProviderHyperV, ProviderNutanix, ProviderXCPng, ProviderWorkstation:
		return true
	default:
		return false
	}
}
