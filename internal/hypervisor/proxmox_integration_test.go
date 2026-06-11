package hypervisor

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is an INTEGRATION test that talks to a real Proxmox VE. It is skipped
// unless credentials are provided, so `go test ./...` stays green in CI.
//
// Provide credentials either as environment variables or — easier — in a
// gitignored file next to this test: internal/hypervisor/.pvetest.env
// (KEY=VALUE per line). See README / the value names in envCreds() below.
//
// Per scope: this test only creates a VM and uploads+attaches the ISO. It does
// NOT power the VM on. It cleans up the VM (and the uploaded test ISO) on exit.

func loadPVEEnv(t *testing.T) map[string]string {
	t.Helper()
	env := map[string]string{}
	// 1) file (next to this test) — optional.
	if f, err := os.Open(".pvetest.env"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if ok {
				// Strip an inline " # comment" (space before #) so a value like
				// `proxmox   # node name` becomes just `proxmox`.
				if i := strings.Index(v, " #"); i >= 0 {
					v = v[:i]
				}
				env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	// 2) process env overrides the file.
	for _, k := range []string{
		"PVE_URL", "PVE_NODE", "PVE_STORAGE", "PVE_ISO_STORAGE", "PVE_BRIDGE",
		"PVE_USER", "PVE_PASSWORD", "PVE_TOKEN_ID", "PVE_TOKEN_SECRET",
		"PVE_INSECURE", "PVE_TEST_ISO",
	} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// makeDummyISO writes a tiny placeholder .iso so the upload path can be
// exercised without shipping a 20 GB image. Proxmox stores ISO content by
// extension and does not validate ISO9660 structure on upload.
func makeDummyISO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "autodeploy-itest.iso")
	if err := os.WriteFile(path, []byte("autodeploy-web proxmox integration test placeholder\n"), 0o644); err != nil {
		t.Fatalf("write dummy iso: %v", err)
	}
	return path
}

func TestProxmoxCreateAndAttachISO(t *testing.T) {
	env := loadPVEEnv(t)
	if env["PVE_URL"] == "" {
		t.Skip("no PVE_URL — set credentials in internal/hypervisor/.pvetest.env or env vars to run this integration test")
	}

	cfg := ProxmoxConfig{
		BaseURL:     env["PVE_URL"],
		Node:        env["PVE_NODE"],
		Storage:     env["PVE_STORAGE"],
		ISOStorage:  env["PVE_ISO_STORAGE"],
		Username:    env["PVE_USER"],
		Password:    env["PVE_PASSWORD"],
		TokenID:     env["PVE_TOKEN_ID"],
		TokenSecret: env["PVE_TOKEN_SECRET"],
		Insecure:    env["PVE_INSECURE"] != "false", // self-signed by default
	}
	bridge := env["PVE_BRIDGE"]
	if bridge == "" {
		bridge = "vmbr0"
	}

	hv, err := NewProxmox(cfg)
	if err != nil {
		t.Fatalf("NewProxmox: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- Upload ISO (real one if provided, else a tiny placeholder) ---
	isoPath := env["PVE_TEST_ISO"]
	if isoPath == "" {
		isoPath = makeDummyISO(t)
	}
	isoRef, err := hv.UploadISO(ctx, isoPath)
	if err != nil {
		t.Fatalf("UploadISO: %v", err)
	}
	t.Logf("uploaded ISO: %s", isoRef)
	// Best-effort ISO cleanup (only the placeholder we created).
	if env["PVE_TEST_ISO"] == "" {
		t.Cleanup(func() {
			n, err := hv.client.Node(context.Background(), cfg.Node)
			if err != nil {
				return
			}
			store, err := n.Storage(context.Background(), cfg.isoStorage())
			if err != nil {
				return
			}
			if task, err := store.DeleteContent(context.Background(), isoRef); err == nil {
				_ = waitTask(context.Background(), task, time.Minute)
			}
		})
	}

	// --- Create VM (powered off) ---
	spec := VMSpec{
		Name:      "autodeploy-itest",
		CPUs:      1,
		MemoryMiB: 1024,
		DiskGiB:   1,
		Bridge:    bridge,
	}
	vm, err := hv.CreateVM(ctx, spec)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	t.Logf("created VM %s on node %s", vm.ID, vm.Node)
	t.Cleanup(func() {
		if err := hv.Destroy(context.Background(), vm); err != nil {
			t.Logf("cleanup: destroy VM %s: %v", vm.ID, err)
		}
	})

	// --- Attach ISO + set boot order (config only, NO power on) ---
	if err := hv.AttachISO(ctx, vm, isoRef); err != nil {
		t.Fatalf("AttachISO: %v", err)
	}
	if err := hv.SetBootFromCD(ctx, vm); err != nil {
		t.Fatalf("SetBootFromCD: %v", err)
	}

	// The VM must remain powered off — this test never boots it.
	state, err := hv.Status(ctx, vm)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state != PowerOff {
		t.Errorf("VM state = %q, want %q (test must not boot the VM)", state, PowerOff)
	}
	t.Logf("OK: VM %s created with ISO %s attached, left powered off", vm.ID, isoRef)
}
