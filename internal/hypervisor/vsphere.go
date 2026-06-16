package hypervisor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/progress"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// VSphereConfig holds connection + placement settings for a vCenter/ESXi target.
type VSphereConfig struct {
	URL      string // vCenter SDK URL, e.g. "https://vc.lab.local/sdk"
	Username string // e.g. "administrator@vsphere.local"
	Password string
	Insecure bool // skip TLS verification

	Datacenter   string // datacenter name (empty = first/only)
	Cluster      string // compute cluster or host; resource pool is derived
	ResourcePool string // explicit resource pool (optional; overrides Cluster default)
	Datastore    string // datastore for VM disks + ISO upload
	Network      string // port group the NIC attaches to
	Folder       string // VM folder (optional)
}

// VSphere is a Hypervisor implementation backed by the vCenter SOAP API (govmomi).
type VSphere struct {
	cfg    VSphereConfig
	mu     sync.Mutex
	client *govmomi.Client // lazy-initialised, guarded by mu
}

// compile-time assertion that *VSphere satisfies the interface.
var _ Hypervisor = (*VSphere)(nil)

// NewVSphere builds a vSphere client from cfg.
func NewVSphere(cfg VSphereConfig) (*VSphere, error) {
	if cfg.URL == "" || cfg.Username == "" || cfg.Datastore == "" {
		return nil, fmt.Errorf("vsphere: URL, Username and Datastore are required")
	}
	return &VSphere{cfg: cfg}, nil
}

// connect returns a connected govmomi client, creating it on the first call.
func (v *VSphere) connect(ctx context.Context) (*govmomi.Client, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.client != nil {
		return v.client, nil
	}
	u, err := soap.ParseURL(v.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("vsphere: parse URL: %w", err)
	}
	u.User = url.UserPassword(v.cfg.Username, v.cfg.Password)
	c, err := govmomi.NewClient(ctx, u, v.cfg.Insecure)
	if err != nil {
		return nil, fmt.Errorf("vsphere: connect: %w", err)
	}
	v.client = c
	return c, nil
}

// finder returns a Finder pointed at the configured datacenter.
func (v *VSphere) finder(ctx context.Context) (*find.Finder, error) {
	c, err := v.connect(ctx)
	if err != nil {
		return nil, err
	}
	f := find.NewFinder(c.Client, true)
	if v.cfg.Datacenter != "" {
		dc, err := f.Datacenter(ctx, v.cfg.Datacenter)
		if err != nil {
			return nil, fmt.Errorf("vsphere: find datacenter %q: %w", v.cfg.Datacenter, err)
		}
		f.SetDatacenter(dc)
	} else {
		dc, err := f.DefaultDatacenter(ctx)
		if err != nil {
			return nil, fmt.Errorf("vsphere: default datacenter: %w", err)
		}
		f.SetDatacenter(dc)
	}
	return f, nil
}

// datastore resolves the configured datastore via the Finder.
func (v *VSphere) datastore(ctx context.Context) (*object.Datastore, error) {
	f, err := v.finder(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := f.Datastore(ctx, v.cfg.Datastore)
	if err != nil {
		return nil, fmt.Errorf("vsphere: find datastore %q: %w", v.cfg.Datastore, err)
	}
	return ds, nil
}

// resourcePool resolves the resource pool to use for VM creation. If
// cfg.ResourcePool is set it is used directly; otherwise the default pool of
// cfg.Cluster (or the datacenter default) is returned.
func (v *VSphere) resourcePool(ctx context.Context) (*object.ResourcePool, error) {
	f, err := v.finder(ctx)
	if err != nil {
		return nil, err
	}
	if v.cfg.ResourcePool != "" {
		rp, err := f.ResourcePool(ctx, v.cfg.ResourcePool)
		if err != nil {
			return nil, fmt.Errorf("vsphere: find resource pool %q: %w", v.cfg.ResourcePool, err)
		}
		return rp, nil
	}
	if v.cfg.Cluster != "" {
		// Find the cluster's default "Resources" pool.
		rp, err := f.ResourcePool(ctx, v.cfg.Cluster+"/Resources")
		if err != nil {
			// Fall back to looking up by cluster name alone.
			rp, err = f.DefaultResourcePool(ctx)
			if err != nil {
				return nil, fmt.Errorf("vsphere: default resource pool: %w", err)
			}
		}
		return rp, nil
	}
	rp, err := f.DefaultResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("vsphere: default resource pool: %w", err)
	}
	return rp, nil
}

// vmFolder resolves the VM folder to create VMs in.
func (v *VSphere) vmFolder(ctx context.Context) (*object.Folder, error) {
	f, err := v.finder(ctx)
	if err != nil {
		return nil, err
	}
	if v.cfg.Folder != "" {
		folder, err := f.Folder(ctx, v.cfg.Folder)
		if err != nil {
			return nil, fmt.Errorf("vsphere: find folder %q: %w", v.cfg.Folder, err)
		}
		return folder, nil
	}
	folder, err := f.DefaultFolder(ctx)
	if err != nil {
		return nil, fmt.Errorf("vsphere: default VM folder: %w", err)
	}
	return folder, nil
}

// vmObject converts a VMRef back to an *object.VirtualMachine using its MoRef value.
func (v *VSphere) vmObject(ctx context.Context, ref VMRef) (*object.VirtualMachine, error) {
	c, err := v.connect(ctx)
	if err != nil {
		return nil, err
	}
	return object.NewVirtualMachine(c.Client, types.ManagedObjectReference{
		Type:  "VirtualMachine",
		Value: ref.ID,
	}), nil
}

// vsWaitTask blocks until an object.Task completes (or ctx fires) and returns any
// task-level error. Named with a "vs" prefix to avoid collision with the
// Proxmox waitTask helper in the same package.
func vsWaitTask(ctx context.Context, t *object.Task) error {
	return t.Wait(ctx)
}

// --------------------------------------------------------------------------
// progress.Sinker adapter
// --------------------------------------------------------------------------

// progressSinker adapts a ProgressFunc to the govmomi progress.Sinker
// interface so it can be passed to soap.Upload.Progress.
type progressSinker struct {
	total int64
	fn    ProgressFunc
}

// Sink returns a channel that forwards each progress.Report to our callback.
// The caller (govmomi) is responsible for closing the channel.
func (s *progressSinker) Sink() chan<- progress.Report {
	ch := make(chan progress.Report, 4)
	go func() {
		var last time.Time
		for r := range ch {
			if r.Error() != nil {
				return
			}
			now := time.Now()
			if now.Sub(last) >= time.Second || r.Percentage() >= 100 {
				last = now
				done := int64(float64(s.total) * float64(r.Percentage()) / 100.0)
				s.fn(done, s.total)
			}
		}
	}()
	return ch
}

// --------------------------------------------------------------------------
// UploadISO
// --------------------------------------------------------------------------

// UploadISO streams a local ISO file to the datastore under "iso/<basename>"
// and returns a datastore reference of the form "[<datastore>] iso/<name>".
// When progress is non-nil it is called periodically with bytes sent / total.
func (v *VSphere) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("vsphere: open ISO %q: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("vsphere: stat ISO %q: %w", localPath, err)
	}
	size := st.Size()

	ds, err := v.datastore(ctx)
	if err != nil {
		return "", err
	}

	name := filepath.Base(localPath)
	dsPath := "iso/" + name

	param := soap.DefaultUpload
	param.ContentLength = size
	if progress != nil {
		param.Progress = &progressSinker{total: size, fn: progress}
	}

	if err := ds.Upload(ctx, f, dsPath, &param); err != nil {
		return "", fmt.Errorf("vsphere: upload ISO %q: %w", name, err)
	}
	if progress != nil {
		progress(size, size) // ensure the bar lands on 100%
	}
	return fmt.Sprintf("[%s] iso/%s", ds.Name(), name), nil
}

// --------------------------------------------------------------------------
// FindISO
// --------------------------------------------------------------------------

// FindISO checks whether an ISO named "name" already exists under "iso/<name>"
// in the configured datastore, and returns the datastore reference if present
// or "" if absent.
func (v *VSphere) FindISO(ctx context.Context, name string) (string, error) {
	ds, err := v.datastore(ctx)
	if err != nil {
		return "", err
	}
	_, err = ds.Stat(ctx, "iso/"+name)
	if err != nil {
		// object.DatastoreNoSuchFileError means the file simply isn't there.
		if _, notFound := err.(object.DatastoreNoSuchFileError); notFound {
			return "", nil
		}
		// Any other error is a real failure.
		return "", fmt.Errorf("vsphere: stat ISO %q: %w", name, err)
	}
	return fmt.Sprintf("[%s] iso/%s", ds.Name(), name), nil
}

// --------------------------------------------------------------------------
// CreateVM
// --------------------------------------------------------------------------

// CreateVM provisions a powered-off VM shell per spec. It builds a
// VirtualMachineConfigSpec with a ParaVirtual SCSI controller, one thin disk
// per spec.Disks entry, a VMXNET3 NIC on cfg.Network, an IDE controller with
// an empty CD-ROM, and a USB controller (required by PutUsbScanCodes).
// UEFI is set when spec.UEFI is true (no Secure Boot enforcement).
// Returns a VMRef whose ID is the vSphere MoRef value (e.g. "vm-123").
func (v *VSphere) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	f, err := v.finder(ctx)
	if err != nil {
		return VMRef{}, err
	}

	// Resolve target objects.
	ds, err := v.datastore(ctx)
	if err != nil {
		return VMRef{}, err
	}
	rp, err := v.resourcePool(ctx)
	if err != nil {
		return VMRef{}, err
	}
	folder, err := v.vmFolder(ctx)
	if err != nil {
		return VMRef{}, err
	}

	// Resolve network backing.
	netRef, err := f.Network(ctx, v.cfg.Network)
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: find network %q: %w", v.cfg.Network, err)
	}
	netBacking, err := netRef.EthernetCardBackingInfo(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: network backing %q: %w", v.cfg.Network, err)
	}

	// Build device list.
	var devList object.VirtualDeviceList

	// 1. ParaVirtual SCSI controller.
	scsiCtrl, err := devList.CreateSCSIController("pvscsi")
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create SCSI controller: %w", err)
	}
	devList = append(devList, scsiCtrl)

	// 2. One thin VirtualDisk per spec.Disks entry (GiB → KiB).
	disks := spec.Disks
	if len(disks) == 0 {
		disks = []int{32}
	}
	for i, sizeGiB := range disks {
		disk := devList.CreateDisk(scsiCtrl.(types.BaseVirtualController), ds.Reference(), "")
		disk.CapacityInKB = int64(sizeGiB) * 1024 * 1024
		// Thin provisioning is already set by CreateDisk (ThinProvisioned=true).
		_ = i
		devList = append(devList, disk)
	}

	// 3. VMXNET3 NIC.
	nic, err := devList.CreateEthernetCard("vmxnet3", netBacking)
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create NIC: %w", err)
	}
	devList = append(devList, nic)

	// 4. IDE controller (needed for CD-ROM).
	ideCtrl, err := devList.CreateIDEController()
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create IDE controller: %w", err)
	}
	devList = append(devList, ideCtrl)

	// 5. Empty CD-ROM on the IDE controller.
	cdrom, err := devList.CreateCdrom(ideCtrl.(types.BaseVirtualController))
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create CD-ROM: %w", err)
	}
	devList = append(devList, cdrom)

	// 6. USB controller (required for PutUsbScanCodes).
	usbCtrl := &types.VirtualUSBController{
		EhciEnabled: types.NewBool(false),
	}
	devList = append(devList, usbCtrl)

	// Build DeviceChange add specs.
	var deviceChange []types.BaseVirtualDeviceConfigSpec
	for _, d := range devList {
		fop := types.VirtualDeviceConfigSpecFileOperationCreate
		if _, isDisk := d.(*types.VirtualDisk); !isDisk {
			fop = ""
		}
		deviceChange = append(deviceChange, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: fop,
			Device:        d,
		})
	}

	firmware := "bios"
	if spec.UEFI {
		firmware = "efi"
	}

	cfgSpec := types.VirtualMachineConfigSpec{
		Name:     spec.Name,
		NumCPUs:  int32(spec.CPUs),
		MemoryMB: int64(spec.MemoryMiB),
		GuestId:  "rhel9_64Guest",
		Version:  "vmx-15",
		Firmware: firmware,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", ds.Name()),
		},
		DeviceChange: deviceChange,
	}

	if spec.UEFI {
		// Explicitly disable Secure Boot so the unsigned Veeam appliance ISO
		// boots under UEFI — mirrors Proxmox pre-enrolled-keys=0 and Hyper-V
		// Secure Boot Off. SetBootDiskThenCD only sets BootOrder so this value
		// persists across subsequent reconfigures.
		// Note: a vTPM is intentionally omitted — it requires a vCenter Key
		// Provider / KMS that we cannot assume is present.
		cfgSpec.BootOptions = &types.VirtualMachineBootOptions{
			EfiSecureBootEnabled: types.NewBool(false),
		}
	}

	task, err := folder.CreateVM(ctx, cfgSpec, rp, nil)
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create VM %q: %w", spec.Name, err)
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("vsphere: create VM %q: %w", spec.Name, err)
	}
	moref, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return VMRef{}, fmt.Errorf("vsphere: create VM %q: unexpected result type %T", spec.Name, info.Result)
	}
	return VMRef{ID: moref.Value, Node: v.cfg.Cluster}, nil
}

// --------------------------------------------------------------------------
// AttachISO
// --------------------------------------------------------------------------

// AttachISO reconfigures the VM's existing CD-ROM device to back it with the
// given ISO datastore reference (e.g. "[datastore] iso/foo.iso") and marks it
// connected and start-connected.
func (v *VSphere) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	devices, err := vmObj.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: attach ISO: get devices for VM %s: %w", vm.ID, err)
	}
	cdrom, err := devices.FindCdrom("")
	if err != nil {
		return fmt.Errorf("vsphere: attach ISO: find CD-ROM on VM %s: %w", vm.ID, err)
	}
	cdrom.Backing = &types.VirtualCdromIsoBackingInfo{
		VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
			FileName: isoRef,
		},
	}
	cdrom.Connectable = &types.VirtualDeviceConnectInfo{
		AllowGuestControl: true,
		Connected:         true,
		StartConnected:    true,
	}
	if err := vmObj.EditDevice(ctx, cdrom); err != nil {
		return fmt.Errorf("vsphere: attach ISO to VM %s: %w", vm.ID, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// DetachISO
// --------------------------------------------------------------------------

// DetachISO removes the ISO from the CD-ROM (reverts to passthrough/empty
// backing) and disconnects it.
func (v *VSphere) DetachISO(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	devices, err := vmObj.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: detach ISO: get devices for VM %s: %w", vm.ID, err)
	}
	cdrom, err := devices.FindCdrom("")
	if err != nil {
		return fmt.Errorf("vsphere: detach ISO: find CD-ROM on VM %s: %w", vm.ID, err)
	}
	devices.EjectIso(cdrom)
	cdrom.Connectable = &types.VirtualDeviceConnectInfo{
		AllowGuestControl: true,
		Connected:         false,
		StartConnected:    false,
	}
	if err := vmObj.EditDevice(ctx, cdrom); err != nil {
		return fmt.Errorf("vsphere: detach ISO from VM %s: %w", vm.ID, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// SetBootFromCD / SetBootFromDisk
// --------------------------------------------------------------------------

// SetBootFromCD configures the VM to boot the CD-ROM first (for OS install).
func (v *VSphere) SetBootFromCD(ctx context.Context, vm VMRef) error {
	spec := types.VirtualMachineConfigSpec{
		BootOptions: &types.VirtualMachineBootOptions{
			BootOrder: []types.BaseVirtualMachineBootOptionsBootableDevice{
				&types.VirtualMachineBootOptionsBootableCdromDevice{},
			},
		},
	}
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	task, err := vmObj.Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("vsphere: set boot-from-CD on VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: set boot-from-CD on VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromDisk configures the VM to boot the first disk (after install).
func (v *VSphere) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	devices, err := vmObj.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: set boot-from-disk: get devices for VM %s: %w", vm.ID, err)
	}
	// Find the first VirtualDisk.
	disks := devices.SelectByType((*types.VirtualDisk)(nil))
	if len(disks) == 0 {
		return fmt.Errorf("vsphere: set boot-from-disk: no disk found on VM %s", vm.ID)
	}
	spec := types.VirtualMachineConfigSpec{
		BootOptions: &types.VirtualMachineBootOptions{
			BootOrder: []types.BaseVirtualMachineBootOptionsBootableDevice{
				&types.VirtualMachineBootOptionsBootableDiskDevice{
					DeviceKey: disks[0].GetVirtualDevice().Key,
				},
			},
		},
	}
	task, err := vmObj.Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("vsphere: set boot-from-disk on VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: set boot-from-disk on VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootDiskThenCD configures the VM to boot disk first, CD-ROM second.
// On a fresh VM the disk is empty so UEFI/BIOS falls through to the CD;
// after install the disk has an OS and boots directly without any runtime change.
func (v *VSphere) SetBootDiskThenCD(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	devices, err := vmObj.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: set boot disk-then-cd: get devices for VM %s: %w", vm.ID, err)
	}
	disks := devices.SelectByType((*types.VirtualDisk)(nil))
	if len(disks) == 0 {
		return fmt.Errorf("vsphere: set boot disk-then-cd: no disk found on VM %s", vm.ID)
	}
	spec := types.VirtualMachineConfigSpec{
		BootOptions: &types.VirtualMachineBootOptions{
			// Re-assert CreateVM's EfiSecureBootEnabled=false so it survives this
			// reconfigure regardless of whether vSphere merges or resets BootOptions.
			EfiSecureBootEnabled: types.NewBool(false),
			BootOrder: []types.BaseVirtualMachineBootOptionsBootableDevice{
				&types.VirtualMachineBootOptionsBootableDiskDevice{
					DeviceKey: disks[0].GetVirtualDevice().Key,
				},
				&types.VirtualMachineBootOptionsBootableCdromDevice{},
			},
		},
	}
	task, err := vmObj.Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("vsphere: set boot disk-then-cd on VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: set boot disk-then-cd on VM %s: %w", vm.ID, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// PowerOn / PowerOff / Status
// --------------------------------------------------------------------------

// PowerOn starts the VM and waits for the task to complete.
func (v *VSphere) PowerOn(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	task, err := vmObj.PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: power on VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: power on VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOff stops the VM and waits for the task to complete.
// An "already powered off" condition is silently ignored.
func (v *VSphere) PowerOff(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	state, err := vmObj.PowerState(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: power off VM %s: get state: %w", vm.ID, err)
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		return nil
	}
	task, err := vmObj.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: power off VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: power off VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM.
func (v *VSphere) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return PowerUnknown, err
	}
	state, err := vmObj.PowerState(ctx)
	if err != nil {
		return PowerUnknown, fmt.Errorf("vsphere: get power state of VM %s: %w", vm.ID, err)
	}
	switch state {
	case types.VirtualMachinePowerStatePoweredOn:
		return PowerRunning, nil
	case types.VirtualMachinePowerStatePoweredOff:
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// --------------------------------------------------------------------------
// Destroy
// --------------------------------------------------------------------------

// Destroy powers off (if needed) then deletes the VM and its disks.
func (v *VSphere) Destroy(ctx context.Context, vm VMRef) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}
	// Power off if running; ignore errors (the VM may already be off).
	if state, err := vmObj.PowerState(ctx); err == nil &&
		state == types.VirtualMachinePowerStatePoweredOn {
		if task, err := vmObj.PowerOff(ctx); err == nil {
			_ = vsWaitTask(ctx, task)
		}
	}
	task, err := vmObj.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: destroy VM %s: %w", vm.ID, err)
	}
	if err := vsWaitTask(ctx, task); err != nil {
		return fmt.Errorf("vsphere: destroy VM %s: %w", vm.ID, err)
	}
	return nil
}

// --------------------------------------------------------------------------
// SendKeys — USB HID scan code translation
// --------------------------------------------------------------------------

// usbHIDCode encodes a USB HID usage ID into the int32 expected by
// UsbScanCodeSpecKeyEvent.UsbHidCode: (usageID << 16) | 0x0007.
func usbHIDCode(usageID int32) int32 {
	return (usageID << 16) | 0x0007
}

// usbShiftMods is a pointer to a modifier struct with LeftShift=true.
var usbShiftMods = &types.UsbScanCodeSpecModifierType{
	LeftShift: types.NewBool(true),
}

// usbEvent builds a key event with no modifiers.
func usbEvent(usageID int32) types.UsbScanCodeSpecKeyEvent {
	return types.UsbScanCodeSpecKeyEvent{UsbHidCode: usbHIDCode(usageID)}
}

// usbShiftEvent builds a key event with left-shift held.
func usbShiftEvent(usageID int32) types.UsbScanCodeSpecKeyEvent {
	return types.UsbScanCodeSpecKeyEvent{
		UsbHidCode: usbHIDCode(usageID),
		Modifiers:  usbShiftMods,
	}
}

// qemuToHID maps QEMU key names (as produced by bootcmd.go) to USB HID
// key events. Letters a-z (0x04-0x1d), digits 0-9, and common punctuation
// are all covered.
//
// HID usage IDs reference: USB HID Usage Tables §10 Keyboard/Keypad Page.
var qemuToHID map[string]types.UsbScanCodeSpecKeyEvent

func init() {
	m := make(map[string]types.UsbScanCodeSpecKeyEvent)

	// Letters a-z → HID 0x04-0x1d (unshifted)
	for i := 0; i < 26; i++ {
		ch := string(rune('a' + i))
		m[ch] = usbEvent(int32(0x04 + i))
	}
	// Uppercase letters → same key code, left-shift modifier
	for i := 0; i < 26; i++ {
		ch := "shift-" + string(rune('a'+i))
		m[ch] = usbShiftEvent(int32(0x04 + i))
	}

	// Digits 1-9 → HID 0x1e-0x26; 0 → 0x27
	for i := 1; i <= 9; i++ {
		m[string(rune('0'+i))] = usbEvent(int32(0x1e + i - 1))
	}
	m["0"] = usbEvent(0x27)

	// Shifted digit symbols (US keyboard).
	m["shift-1"] = usbShiftEvent(0x1e) // !
	m["shift-2"] = usbShiftEvent(0x1f) // @
	m["shift-3"] = usbShiftEvent(0x20) // #
	m["shift-4"] = usbShiftEvent(0x21) // $
	m["shift-5"] = usbShiftEvent(0x22) // %
	m["shift-6"] = usbShiftEvent(0x23) // ^
	m["shift-7"] = usbShiftEvent(0x24) // &
	m["shift-8"] = usbShiftEvent(0x25) // *
	m["shift-9"] = usbShiftEvent(0x26) // (
	m["shift-0"] = usbShiftEvent(0x27) // )

	// Function / navigation keys.
	m["ret"] = usbEvent(0x28) // Enter
	m["esc"] = usbEvent(0x29) // Escape
	m["backspace"] = usbEvent(0x2a)
	m["tab"] = usbEvent(0x2b)
	m["spc"] = usbEvent(0x2c) // Space

	// Punctuation (unshifted).
	m["minus"] = usbEvent(0x2d) // -
	m["equal"] = usbEvent(0x2e) // =
	m["bracket_left"] = usbEvent(0x2f)
	m["bracket_right"] = usbEvent(0x30)
	m["backslash"] = usbEvent(0x31)  // backslash
	m["semicolon"] = usbEvent(0x33)  // ;
	m["apostrophe"] = usbEvent(0x34) // '
	m["grave_accent"] = usbEvent(0x35)
	m["comma"] = usbEvent(0x36) // ,
	m["dot"] = usbEvent(0x37)   // .
	m["slash"] = usbEvent(0x38) // /

	// Punctuation (shifted).
	m["shift-minus"] = usbShiftEvent(0x2d)        // _
	m["shift-equal"] = usbShiftEvent(0x2e)        // +
	m["shift-semicolon"] = usbShiftEvent(0x33)    // :
	m["shift-apostrophe"] = usbShiftEvent(0x34)   // "
	m["shift-slash"] = usbShiftEvent(0x38)        // ?
	m["shift-comma"] = usbShiftEvent(0x36)        // <
	m["shift-dot"] = usbShiftEvent(0x37)          // >
	m["shift-grave_accent"] = usbShiftEvent(0x35) // ~

	qemuToHID = m
}

// SendKeys translates QEMU key-name tokens (as produced by bootcmd.go) into
// USB HID scan codes and delivers them to the VM via PutUsbScanCodes. All
// tokens are sent in a single API call.
func (v *VSphere) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return err
	}

	events := make([]types.UsbScanCodeSpecKeyEvent, 0, len(keys))
	var unknown []string
	for _, k := range keys {
		ev, ok := qemuToHID[strings.ToLower(k)]
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		events = append(events, ev)
	}
	if len(unknown) > 0 {
		// Log but don't abort; unknown keys are silently dropped (bootcmd.go
		// documents that characters without a mapping are skipped).
		_ = unknown
	}
	if len(events) == 0 {
		return nil
	}

	_, err = vmObj.PutUsbScanCodes(ctx, types.UsbScanCodeSpec{KeyEvents: events})
	if err != nil {
		return fmt.Errorf("vsphere: send keys to VM %s: %w", vm.ID, err)
	}
	return nil
}

// GetVMIP returns the primary guest IP address reported by VMware Tools.
// Returns ("", nil) when tools are not running or no IP is assigned yet.
func (v *VSphere) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	vmObj, err := v.vmObject(ctx, vm)
	if err != nil {
		return "", err
	}
	var mobj mo.VirtualMachine
	if err := vmObj.Properties(ctx, vmObj.Reference(), []string{"guest"}, &mobj); err != nil {
		return "", fmt.Errorf("vsphere: get guest properties VM %s: %w", vm.ID, err)
	}
	if mobj.Guest == nil || mobj.Guest.IpAddress == "" {
		return "", nil
	}
	return mobj.Guest.IpAddress, nil
}
