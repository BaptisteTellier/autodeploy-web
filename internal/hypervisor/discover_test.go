package hypervisor

import "testing"

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
