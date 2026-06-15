package deploy

import "testing"

func TestPresetStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewPresetStore(dir)

	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty List = %v, %v; want [] nil", list, err)
	}

	snap := FormSnapshot{
		Kind:        "vsa-ha+hr",
		Provider:    "hyperv",
		RemoteKS:    true,
		Wire:        true,
		NodeOutputs: []string{"job-a", "job-b", "job-c"},
		Text:        map[string]string{"cluster_endpoint": "192.168.1.200", "node_2_role": "VIA-HR"},
		Checks:      map[string]bool{"wire_node_exporter": true},
	}
	if err := s.Save("My Template", snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load("My Template")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Kind != snap.Kind || got.Provider != snap.Provider || !got.Wire ||
		len(got.NodeOutputs) != 3 || got.Text["cluster_endpoint"] != "192.168.1.200" ||
		!got.Checks["wire_node_exporter"] {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if list, err := s.List(); err != nil || len(list) != 1 || list[0].Name != "My Template" {
		t.Fatalf("List = %v, %v; want one 'My Template'", list, err)
	}

	// Invalid names are rejected (path traversal / odd chars).
	if err := s.Save("../evil", snap); err == nil {
		t.Error("Save(../evil) = nil error, want rejection")
	}

	if err := s.Delete("My Template"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("after Delete, List = %v; want empty", list)
	}
}
