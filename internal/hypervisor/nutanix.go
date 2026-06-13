package hypervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	prismgoclient "github.com/nutanix-cloud-native/prism-go-client"
	v3client "github.com/nutanix-cloud-native/prism-go-client/v3"
)

// NutanixConfig holds connection + placement settings for a Nutanix AHV target
// (Prism Central or Prism Element v3 API).
type NutanixConfig struct {
	Endpoint string // Prism IP/FQDN (no scheme), e.g. "10.0.0.10"
	Port     int    // Prism port (default 9440)
	Username string
	Password string
	Insecure bool // skip TLS verification

	Cluster          string // target cluster name or UUID (required on Prism Central)
	StorageContainer string // storage container name/UUID for VM disks
	Subnet           string // subnet/network name or UUID for the NIC
}

// Nutanix is a Hypervisor implementation backed by the Prism v3 REST API.
//
// AHV exposes no keystroke-injection API, so SendKeys returns
// ErrKickstartUnsupported and remote kickstart is rejected up front for this
// provider (SupportsKickstart == false). Classic pre-customised-ISO deploys are
// fully supported.
type Nutanix struct {
	cfg NutanixConfig

	// lazily initialised by client()
	once    sync.Once
	svc     v3client.Service
	initErr error

	// resolved UUIDs — populated on first use by resolve*()
	mu          sync.Mutex
	clusterUUID string
	subnetUUID  string
}

// compile-time assertion that *Nutanix satisfies the interface.
var _ Hypervisor = (*Nutanix)(nil)

// NewNutanix builds a Nutanix Prism client from cfg.
func NewNutanix(cfg NutanixConfig) (*Nutanix, error) {
	if cfg.Endpoint == "" || cfg.Username == "" || cfg.StorageContainer == "" {
		return nil, fmt.Errorf("nutanix: Endpoint, Username and StorageContainer are required")
	}
	return &Nutanix{cfg: cfg}, nil
}

// client returns the lazily-initialised v3 Service. The first call builds the
// HTTP client; subsequent calls reuse it.
func (nx *Nutanix) client() (v3client.Service, error) {
	nx.once.Do(func() {
		port := nx.cfg.Port
		if port == 0 {
			port = 9440
		}
		creds := prismgoclient.Credentials{
			Endpoint: nx.cfg.Endpoint,
			Port:     fmt.Sprintf("%d", port),
			Username: nx.cfg.Username,
			Password: nx.cfg.Password,
			Insecure: nx.cfg.Insecure,
		}
		c, err := v3client.NewV3Client(creds)
		if err != nil {
			nx.initErr = fmt.Errorf("nutanix: build client: %w", err)
			return
		}
		nx.svc = c.V3
	})
	return nx.svc, nx.initErr
}

// isUUID returns true when s looks like a UUID (36 chars with hyphens in the
// right places). This avoids an unnecessary list call when the user already
// supplied a UUID.
func isUUID(s string) bool {
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

// resolveCluster resolves cfg.Cluster (name or UUID) to a UUID. The result is
// cached after the first successful lookup.
func (nx *Nutanix) resolveCluster(ctx context.Context) (string, error) {
	nx.mu.Lock()
	cached := nx.clusterUUID
	nx.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	svc, err := nx.client()
	if err != nil {
		return "", err
	}

	if isUUID(nx.cfg.Cluster) {
		nx.mu.Lock()
		nx.clusterUUID = nx.cfg.Cluster
		nx.mu.Unlock()
		return nx.cfg.Cluster, nil
	}

	// Name → UUID: list clusters and match by name.
	filter := fmt.Sprintf("name==%s", nx.cfg.Cluster)
	resp, err := svc.ListAllCluster(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("nutanix: list clusters: %w", err)
	}
	for _, e := range resp.Entities {
		if e.Metadata == nil || e.Metadata.UUID == nil {
			continue
		}
		if e.Spec != nil && e.Spec.Name == nx.cfg.Cluster {
			uuid := *e.Metadata.UUID
			nx.mu.Lock()
			nx.clusterUUID = uuid
			nx.mu.Unlock()
			return uuid, nil
		}
	}
	return "", fmt.Errorf("nutanix: cluster %q not found", nx.cfg.Cluster)
}

// resolveSubnet resolves cfg.Subnet (name or UUID) to a UUID. Cached.
func (nx *Nutanix) resolveSubnet(ctx context.Context) (string, error) {
	nx.mu.Lock()
	cached := nx.subnetUUID
	nx.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	svc, err := nx.client()
	if err != nil {
		return "", err
	}

	if isUUID(nx.cfg.Subnet) {
		nx.mu.Lock()
		nx.subnetUUID = nx.cfg.Subnet
		nx.mu.Unlock()
		return nx.cfg.Subnet, nil
	}

	filter := fmt.Sprintf("name==%s", nx.cfg.Subnet)
	resp, err := svc.ListAllSubnet(ctx, filter, nil)
	if err != nil {
		return "", fmt.Errorf("nutanix: list subnets: %w", err)
	}
	for _, e := range resp.Entities {
		if e.Metadata == nil || e.Metadata.UUID == nil {
			continue
		}
		if e.Spec != nil && e.Spec.Name != nil && *e.Spec.Name == nx.cfg.Subnet {
			uuid := *e.Metadata.UUID
			nx.mu.Lock()
			nx.subnetUUID = uuid
			nx.mu.Unlock()
			return uuid, nil
		}
	}
	return "", fmt.Errorf("nutanix: subnet %q not found", nx.cfg.Subnet)
}

// waitTask polls the Prism task API until the task reaches a terminal state
// (SUCCEEDED or FAILED) or ctx/timeout fires.
func (nx *Nutanix) waitTask(ctx context.Context, taskUUID string) error {
	svc, err := nx.client()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Hour)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("nutanix: task %s timed out", taskUUID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		t, err := svc.GetTask(ctx, taskUUID)
		if err != nil {
			return fmt.Errorf("nutanix: poll task %s: %w", taskUUID, err)
		}
		if t.Status == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		switch *t.Status {
		case "SUCCEEDED":
			return nil
		case "FAILED", "ABORTED":
			detail := ""
			if t.ErrorDetail != nil {
				detail = ": " + *t.ErrorDetail
			}
			return fmt.Errorf("nutanix: task %s %s%s", taskUUID, *t.Status, detail)
		}
		// QUEUED / RUNNING — keep polling
		time.Sleep(2 * time.Second)
	}
}

// taskUUIDFromExecCtx extracts the task UUID from the ExecutionContext field
// returned by mutating API calls. The field is typed as interface{} in the SDK
// (it can be a string or []interface{} of strings).
func taskUUIDFromExecCtx(ctx interface{}) (string, bool) {
	if ctx == nil {
		return "", false
	}
	switch v := ctx.(type) {
	case string:
		if v != "" {
			return v, true
		}
	case []interface{}:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// ptr returns a pointer to v (generic convenience).
func ptr[T any](v T) *T { return &v }

// UploadISO uploads a local ISO to the Prism image service and returns the
// image UUID as the isoRef. It calls CreateImage to register the image
// metadata, then UploadImage to stream the file. Progress is reported as
// 0 → size (two coarse ticks) since the SDK upload call is opaque.
func (nx *Nutanix) UploadISO(ctx context.Context, localPath string, progress ProgressFunc) (string, error) {
	svc, err := nx.client()
	if err != nil {
		return "", err
	}

	name := filepath.Base(localPath)

	// Stat the file so we can report total bytes to the progress callback.
	fi, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("nutanix: stat ISO %q: %w", name, err)
	}
	size := fi.Size()

	// Register the image record.
	imageInput := &v3client.ImageIntentInput{
		Metadata: &v3client.Metadata{
			Kind: ptr("image"),
			Name: ptr(name),
		},
		Spec: &v3client.Image{
			Name: ptr(name),
			Resources: &v3client.ImageResources{
				ImageType: ptr("ISO_IMAGE"),
			},
		},
	}
	imageResp, err := svc.CreateImage(ctx, imageInput)
	if err != nil {
		return "", fmt.Errorf("nutanix: create image record for %q: %w", name, err)
	}
	if imageResp.Metadata == nil || imageResp.Metadata.UUID == nil {
		return "", fmt.Errorf("nutanix: create image for %q: no UUID in response", name)
	}
	imageUUID := *imageResp.Metadata.UUID

	// Wait for the image record task (CreateImage is async).
	if imageResp.Status != nil && imageResp.Status.ExecutionContext != nil {
		if taskID, ok := taskUUIDFromExecCtx(imageResp.Status.ExecutionContext.TaskUUID); ok {
			if err := nx.waitTask(ctx, taskID); err != nil {
				return "", fmt.Errorf("nutanix: create image %q: %w", name, err)
			}
		}
	}

	if progress != nil {
		progress(0, size)
	}

	// Upload the binary content.
	if err := svc.UploadImage(ctx, imageUUID, localPath); err != nil {
		return "", fmt.Errorf("nutanix: upload ISO %q: %w", name, err)
	}

	if progress != nil {
		progress(size, size)
	}

	return imageUUID, nil
}

// FindISO searches the image library for an ISO with the given name and
// returns its UUID, or "" when not found.
func (nx *Nutanix) FindISO(ctx context.Context, name string) (string, error) {
	svc, err := nx.client()
	if err != nil {
		return "", err
	}

	filter := fmt.Sprintf("name==%s", name)
	resp, err := svc.ListAllImage(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("nutanix: list images: %w", err)
	}
	for _, e := range resp.Entities {
		if e.Metadata == nil || e.Metadata.UUID == nil {
			continue
		}
		if e.Spec == nil || e.Spec.Name == nil || *e.Spec.Name != name {
			continue
		}
		if e.Spec.Resources == nil || e.Spec.Resources.ImageType == nil {
			continue
		}
		if *e.Spec.Resources.ImageType == "ISO_IMAGE" {
			return *e.Metadata.UUID, nil
		}
	}
	return "", nil
}

// SendKeys is unsupported on AHV (no keystroke-injection API).
func (nx *Nutanix) SendKeys(ctx context.Context, vm VMRef, keys []string) error {
	return ErrKickstartUnsupported
}

// CreateVM provisions a powered-off VM shell per spec and returns its VMRef.
// The VM is placed on cfg.Cluster, uses cfg.StorageContainer for every disk,
// and attaches one NIC on cfg.Subnet. UEFI boot is configured when spec.UEFI
// is true.
func (nx *Nutanix) CreateVM(ctx context.Context, spec VMSpec) (VMRef, error) {
	svc, err := nx.client()
	if err != nil {
		return VMRef{}, err
	}

	clusterUUID, err := nx.resolveCluster(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("nutanix: resolve cluster: %w", err)
	}
	subnetUUID, err := nx.resolveSubnet(ctx)
	if err != nil {
		return VMRef{}, fmt.Errorf("nutanix: resolve subnet: %w", err)
	}

	// Build disk list — one DISK device per spec.Disks entry.
	disks := spec.Disks
	if len(disks) == 0 {
		disks = []int{32} // safety default
	}
	diskList := make([]*v3client.VMDisk, 0, len(disks))
	for _, sizeGiB := range disks {
		sizeMiB := int64(sizeGiB) * 1024
		diskList = append(diskList, &v3client.VMDisk{
			DeviceProperties: &v3client.VMDiskDeviceProperties{
				DeviceType: ptr("DISK"),
				DiskAddress: &v3client.DiskAddress{
					AdapterType: ptr("SCSI"),
					DeviceIndex: ptr(int64(0)),
				},
			},
			DiskSizeMib: &sizeMiB,
			StorageConfig: &v3client.VMStorageConfig{
				StorageContainerReference: &v3client.StorageContainerReference{
					Kind: "storage_container",
					UUID: nx.cfg.StorageContainer,
				},
			},
		})
	}

	// Boot config.
	bootType := "LEGACY"
	if spec.UEFI {
		bootType = "UEFI"
	}
	bootConfig := &v3client.VMBootConfig{
		BootType: ptr(bootType),
		BootDeviceOrderList: []*string{
			ptr("CDROM"),
			ptr("DISK"),
		},
	}

	cpus := int64(spec.CPUs)
	if cpus < 1 {
		cpus = 1
	}
	memMiB := int64(spec.MemoryMiB)

	input := &v3client.VMIntentInput{
		Metadata: &v3client.Metadata{
			Kind: ptr("vm"),
			Name: ptr(spec.Name),
		},
		Spec: &v3client.VM{
			Name: ptr(spec.Name),
			ClusterReference: &v3client.Reference{
				Kind: ptr("cluster"),
				UUID: ptr(clusterUUID),
			},
			Resources: &v3client.VMResources{
				NumSockets:        ptr(int64(1)),
				NumVcpusPerSocket: ptr(cpus),
				MemorySizeMib:     ptr(memMiB),
				PowerState:        ptr("OFF"),
				DiskList:          diskList,
				NicList: []*v3client.VMNic{
					{
						SubnetReference: &v3client.Reference{
							Kind: ptr("subnet"),
							UUID: ptr(subnetUUID),
						},
					},
				},
				BootConfig: bootConfig,
			},
		},
	}

	vmResp, err := svc.CreateVM(ctx, input)
	if err != nil {
		return VMRef{}, fmt.Errorf("nutanix: create VM %q: %w", spec.Name, err)
	}

	// Wait for the async create task.
	if vmResp.Status != nil && vmResp.Status.ExecutionContext != nil {
		if taskID, ok := taskUUIDFromExecCtx(vmResp.Status.ExecutionContext.TaskUUID); ok {
			if err := nx.waitTask(ctx, taskID); err != nil {
				return VMRef{}, fmt.Errorf("nutanix: create VM %q: %w", spec.Name, err)
			}
		}
	}

	if vmResp.Metadata == nil || vmResp.Metadata.UUID == nil {
		return VMRef{}, fmt.Errorf("nutanix: create VM %q: no UUID in response", spec.Name)
	}
	vmUUID := *vmResp.Metadata.UUID
	return VMRef{ID: vmUUID, Node: nx.cfg.Cluster}, nil
}

// getVM fetches the current intent for the VM identified by ref.
func (nx *Nutanix) getVM(ctx context.Context, ref VMRef) (*v3client.VMIntentResponse, error) {
	svc, err := nx.client()
	if err != nil {
		return nil, err
	}
	vm, err := svc.GetVM(ctx, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("nutanix: get VM %s: %w", ref.ID, err)
	}
	return vm, nil
}

// updateVM PUTs a VM intent and waits for the async task to complete.
// input.Metadata must be populated from the most-recent GET to carry
// spec_version / entity_version and avoid 409 conflicts.
func (nx *Nutanix) updateVM(ctx context.Context, ref VMRef, input *v3client.VMIntentInput) error {
	svc, err := nx.client()
	if err != nil {
		return err
	}
	resp, err := svc.UpdateVM(ctx, ref.ID, input)
	if err != nil {
		return fmt.Errorf("nutanix: update VM %s: %w", ref.ID, err)
	}
	if resp.Status != nil && resp.Status.ExecutionContext != nil {
		if taskID, ok := taskUUIDFromExecCtx(resp.Status.ExecutionContext.TaskUUID); ok {
			if err := nx.waitTask(ctx, taskID); err != nil {
				return fmt.Errorf("nutanix: update VM %s: %w", ref.ID, err)
			}
		}
	}
	return nil
}

// AttachISO mounts the ISO image (identified by its image UUID) as a CDROM
// on the VM. It GETs the current spec, appends a CDROM VMDisk pointing at the
// image, then PUTs the updated spec.
func (nx *Nutanix) AttachISO(ctx context.Context, vm VMRef, isoRef string) error {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return fmt.Errorf("nutanix: attach ISO to VM %s: %w", vm.ID, err)
	}

	cdrom := &v3client.VMDisk{
		DeviceProperties: &v3client.VMDiskDeviceProperties{
			DeviceType: ptr("CDROM"),
			DiskAddress: &v3client.DiskAddress{
				AdapterType: ptr("IDE"),
				DeviceIndex: ptr(int64(0)),
			},
		},
		DataSourceReference: &v3client.Reference{
			Kind: ptr("image"),
			UUID: ptr(isoRef),
		},
	}

	spec := current.Spec
	if spec == nil {
		spec = &v3client.VM{}
	}
	if spec.Resources == nil {
		spec.Resources = &v3client.VMResources{}
	}
	spec.Resources.DiskList = append(spec.Resources.DiskList, cdrom)

	input := &v3client.VMIntentInput{
		Metadata: current.Metadata,
		Spec:     spec,
	}
	if err := nx.updateVM(ctx, vm, input); err != nil {
		return fmt.Errorf("nutanix: attach ISO to VM %s: %w", vm.ID, err)
	}
	return nil
}

// DetachISO removes all CDROM devices from the VM's disk list.
func (nx *Nutanix) DetachISO(ctx context.Context, vm VMRef) error {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return fmt.Errorf("nutanix: detach ISO from VM %s: %w", vm.ID, err)
	}

	spec := current.Spec
	if spec == nil || spec.Resources == nil {
		return nil // nothing to remove
	}
	filtered := spec.Resources.DiskList[:0]
	for _, d := range spec.Resources.DiskList {
		if d.DeviceProperties != nil && d.DeviceProperties.DeviceType != nil &&
			strings.EqualFold(*d.DeviceProperties.DeviceType, "CDROM") {
			continue // drop CDROM devices
		}
		filtered = append(filtered, d)
	}
	spec.Resources.DiskList = filtered

	input := &v3client.VMIntentInput{
		Metadata: current.Metadata,
		Spec:     spec,
	}
	if err := nx.updateVM(ctx, vm, input); err != nil {
		return fmt.Errorf("nutanix: detach ISO from VM %s: %w", vm.ID, err)
	}
	return nil
}

// setBootOrder applies bootDeviceOrderList to the VM's boot_config while
// preserving the existing boot_type (LEGACY or UEFI).
func (nx *Nutanix) setBootOrder(ctx context.Context, vm VMRef, order []string) error {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return err
	}

	spec := current.Spec
	if spec == nil {
		spec = &v3client.VM{}
	}
	if spec.Resources == nil {
		spec.Resources = &v3client.VMResources{}
	}

	orderPtrs := make([]*string, len(order))
	for i, o := range order {
		o := o
		orderPtrs[i] = &o
	}

	if spec.Resources.BootConfig == nil {
		spec.Resources.BootConfig = &v3client.VMBootConfig{}
	}
	spec.Resources.BootConfig.BootDeviceOrderList = orderPtrs

	input := &v3client.VMIntentInput{
		Metadata: current.Metadata,
		Spec:     spec,
	}
	return nx.updateVM(ctx, vm, input)
}

// SetBootFromCD makes the VM boot the CD-ROM first, then the disk.
func (nx *Nutanix) SetBootFromCD(ctx context.Context, vm VMRef) error {
	if err := nx.setBootOrder(ctx, vm, []string{"CDROM", "DISK"}); err != nil {
		return fmt.Errorf("nutanix: set boot from CD on VM %s: %w", vm.ID, err)
	}
	return nil
}

// SetBootFromDisk makes the VM boot the disk first (post-install).
func (nx *Nutanix) SetBootFromDisk(ctx context.Context, vm VMRef) error {
	if err := nx.setBootOrder(ctx, vm, []string{"DISK"}); err != nil {
		return fmt.Errorf("nutanix: set boot from disk on VM %s: %w", vm.ID, err)
	}
	return nil
}

// setPowerState sets the VM power_state field to the given value and waits
// for the resulting async task.
func (nx *Nutanix) setPowerState(ctx context.Context, vm VMRef, state string) error {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return err
	}

	spec := current.Spec
	if spec == nil {
		spec = &v3client.VM{}
	}
	if spec.Resources == nil {
		spec.Resources = &v3client.VMResources{}
	}
	spec.Resources.PowerState = ptr(state)

	input := &v3client.VMIntentInput{
		Metadata: current.Metadata,
		Spec:     spec,
	}
	return nx.updateVM(ctx, vm, input)
}

// PowerOn starts the VM.
func (nx *Nutanix) PowerOn(ctx context.Context, vm VMRef) error {
	if err := nx.setPowerState(ctx, vm, "ON"); err != nil {
		return fmt.Errorf("nutanix: power on VM %s: %w", vm.ID, err)
	}
	return nil
}

// PowerOff stops the VM.
func (nx *Nutanix) PowerOff(ctx context.Context, vm VMRef) error {
	if err := nx.setPowerState(ctx, vm, "OFF"); err != nil {
		return fmt.Errorf("nutanix: power off VM %s: %w", vm.ID, err)
	}
	return nil
}

// Status returns the coarse power state of the VM.
func (nx *Nutanix) Status(ctx context.Context, vm VMRef) (PowerState, error) {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return PowerUnknown, fmt.Errorf("nutanix: status VM %s: %w", vm.ID, err)
	}
	if current.Status == nil || current.Status.Resources == nil || current.Status.Resources.PowerState == nil {
		return PowerUnknown, nil
	}
	switch strings.ToUpper(*current.Status.Resources.PowerState) {
	case "ON":
		return PowerRunning, nil
	case "OFF":
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// GetVMIP returns the first IPv4 address from the VM's NIC list as reported
// by the Nutanix Prism API (populated by the ACPI/guest tools).
// Returns ("", nil) when no IP has been assigned yet.
func (nx *Nutanix) GetVMIP(ctx context.Context, vm VMRef) (string, error) {
	current, err := nx.getVM(ctx, vm)
	if err != nil {
		return "", fmt.Errorf("nutanix: GetVMIP %s: %w", vm.ID, err)
	}
	if current.Status == nil || current.Status.Resources == nil {
		return "", nil
	}
	for _, nic := range current.Status.Resources.NicList {
		if nic == nil {
			continue
		}
		for _, ep := range nic.IPEndpointList {
			if ep == nil || ep.IP == nil || *ep.IP == "" {
				continue
			}
			return *ep.IP, nil
		}
	}
	return "", nil
}

// Destroy powers off (if needed) and deletes the VM and its disks.
func (nx *Nutanix) Destroy(ctx context.Context, vm VMRef) error {
	svc, err := nx.client()
	if err != nil {
		return err
	}

	// Best-effort power off before delete.
	if st, err := nx.Status(ctx, vm); err == nil && st == PowerRunning {
		_ = nx.setPowerState(ctx, vm, "OFF") // ignore error; delete will fail loudly if needed
	}

	delResp, err := svc.DeleteVM(ctx, vm.ID)
	if err != nil {
		return fmt.Errorf("nutanix: delete VM %s: %w", vm.ID, err)
	}
	if delResp != nil && delResp.Status != nil && delResp.Status.ExecutionContext != nil {
		if taskID, ok := taskUUIDFromExecCtx(delResp.Status.ExecutionContext.TaskUUID); ok {
			if err := nx.waitTask(ctx, taskID); err != nil {
				return fmt.Errorf("nutanix: delete VM %s: %w", vm.ID, err)
			}
		}
	}
	return nil
}
