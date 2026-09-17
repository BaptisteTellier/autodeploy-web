package hypervisor

import "testing"

// UploadISO and FindISO both resolve through isoDatastoreName. An empty
// ISODatastore must fall back to the VM datastore, which is the behaviour every
// deploy saved before the two were split — a regression here would silently
// upload to the wrong datastore, or report existing ISOs as missing.
func TestISODatastoreNameFallback(t *testing.T) {
	cases := []struct {
		name string
		cfg  VSphereConfig
		want string
	}{
		{"dedicated ISO datastore wins", VSphereConfig{Datastore: "vm-ds", ISODatastore: "iso-ds"}, "iso-ds"},
		{"empty falls back to the VM datastore", VSphereConfig{Datastore: "vm-ds"}, "vm-ds"},
		{"both empty stays empty", VSphereConfig{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.isoDatastoreName(); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// humanBytes labels the datastore dropdown, so a wrong unit would misreport free
// space on the screen people use to decide where a 20 GB image fits.
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                             "0 B",
		512:                           "512 B",
		1024:                          "1.0 KB",
		1536:                          "1.5 KB",
		1024 * 1024:                   "1.0 MB",
		3 * 1024 * 1024 * 1024:        "3.0 GB",
		2 * 1024 * 1024 * 1024 * 1024: "2.0 TB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d): want %q, got %q", in, want, got)
		}
	}
}

// Every discovery list is alphabetised through one deferred call, so the
// dropdowns read consistently whatever order the hypervisor returned — and so
// the partial results handed back by discovery's early-return paths are sorted
// too, which a sort placed before each return would have missed.
func TestSortDiscovered(t *testing.T) {
	m := map[string][]Option{
		// Mixed case on purpose: vCenter inventories routinely mix them, and a
		// byte-order sort would put every capitalised name before every
		// lowercase one instead of interleaving them as a reader expects.
		"vs_network":   {{Value: "vlan-30"}, {Value: "Management"}, {Value: "app-tier"}, {Value: "VLAN-10"}},
		"vs_datastore": {{Value: "ssd-02", Label: "ssd-02 — 1.0 TB free / 2.0 TB"}, {Value: "nvme-01"}},
		"single":       {{Value: "only"}},
		"empty":        {},
	}
	sortDiscovered(m)

	wantNet := []string{"app-tier", "Management", "VLAN-10", "vlan-30"}
	for i, w := range wantNet {
		if m["vs_network"][i].Value != w {
			t.Errorf("vs_network[%d]: want %q, got %q", i, w, m["vs_network"][i].Value)
		}
	}
	if m["vs_datastore"][0].Value != "nvme-01" {
		t.Errorf("vs_datastore not sorted: got %q first", m["vs_datastore"][0].Value)
	}
	// Sorting must carry the capacity label with its value, not just reorder names.
	if m["vs_datastore"][1].Label == "" {
		t.Error("capacity label lost while sorting")
	}
	if len(m["single"]) != 1 || len(m["empty"]) != 0 {
		t.Error("single-element and empty lists must survive untouched")
	}
}
