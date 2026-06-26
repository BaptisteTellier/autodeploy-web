package hypervisor

import "testing"

// TestExtractVMnet verifies the vmnetN extraction helper used by
// DiscoverWorkstation to parse adapter descriptions into canonical vmnetN names.
func TestExtractVMnet(t *testing.T) {
	cases := []struct {
		description string
		want        string
	}{
		{"VMware Virtual Ethernet Adapter for VMnet8", "vmnet8"},
		{"VMware Virtual Ethernet Adapter for VMnet1", "vmnet1"},
		{"VMware Virtual Ethernet Adapter for VMnet0", "vmnet0"},
		// Case-insensitive match
		{"VMware Virtual Ethernet Adapter for vmnet8", "vmnet8"},
		// Mixed case in token
		{"VMware Virtual Ethernet Adapter for VMNET2", "vmnet2"},
		// No vmnet token → empty
		{"Intel(R) Ethernet Connection", ""},
		{"", ""},
		// Description with extra surrounding text
		{"Some adapter (VMnet10) on host", "vmnet10"},
	}
	for _, tc := range cases {
		got := extractVMnet(tc.description)
		if got != tc.want {
			t.Errorf("extractVMnet(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}

// TestSplitLines verifies the multi-line output splitter used by
// DiscoverHyperV and DiscoverWorkstation to parse PowerShell stdout.
func TestSplitLines(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"External\r\nInternal\r\n", []string{"External", "Internal"}},
		{"External\nInternal\n", []string{"External", "Internal"}},
		{"  External  \n  Internal  \n", []string{"External", "Internal"}},
		// Blank lines are skipped
		{"External\n\nInternal\n", []string{"External", "Internal"}},
		// Single value, no trailing newline
		{"C:\\VMs", []string{"C:\\VMs"}},
	}
	for _, tc := range cases {
		got := splitLines(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitLines(%q) = %v (len %d), want %v (len %d)", tc.input, got, len(got), tc.want, len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitLines(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

// TestStorageContentHas verifies the content-type classification helper used
// by DiscoverProxmox to route storages into pve_storage vs pve_iso_storage.
func TestStorageContentHas(t *testing.T) {
	cases := []struct {
		content string
		kind    string
		want    bool
	}{
		// Basic matches
		{"images,iso", "images", true},
		{"images,iso", "iso", true},
		{"images,iso", "rootdir", false},
		// Single type
		{"iso", "iso", true},
		{"iso", "images", false},
		// Spaces around commas
		{"images, iso, vztmpl", "iso", true},
		{"images, iso, vztmpl", "vztmpl", true},
		// rootdir (used by ZFS/LVM-thin for disk images)
		{"rootdir,images", "rootdir", true},
		// A storage that holds both iso and images lands in BOTH lists
		{"iso,images", "iso", true},
		{"iso,images", "images", true},
		// Empty content
		{"", "iso", false},
		// No match
		{"backup,snippets", "iso", false},
	}
	for _, tc := range cases {
		got := storageContentHas(tc.content, tc.kind)
		if got != tc.want {
			t.Errorf("storageContentHas(%q, %q) = %v, want %v", tc.content, tc.kind, got, tc.want)
		}
	}
}
