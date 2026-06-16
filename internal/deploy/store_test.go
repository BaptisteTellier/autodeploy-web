package deploy

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestDeployStore opens a Store in a throwaway directory.
func newTestDeployStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "deployments.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDeployStoreRoundTrip(t *testing.T) {
	s := newTestDeployStore(t)

	created := time.Now()
	finished := created.Add(2 * time.Minute)

	rec := PersistedDeployment{
		View: View{
			ID:         "deploy-1",
			Kind:       "vsa+proxy",
			State:      StateDone,
			CreatedAt:  created,
			FinishedAt: finished,
			Error:      "",
			HasWirer:   true,
			Nodes: []NodeStatus{
				{Hostname: "vsa-01", Role: "VSA", Step: "created", VMID: "101"},
				{Hostname: "proxy-01", Role: "VIA-Proxy", Step: "created", VMID: "102"},
			},
			Form: FormSnapshot{
				Kind:     "vsa+proxy",
				Provider: "proxmox",
				Wire:     true,
				Text:     map[string]string{"hv_host": "192.168.1.10"},
				Checks:   map[string]bool{"hv_insecure": true},
			},
		},
		Lines: []string{"12:00:00 start", "12:01:00 uploading", "12:02:00 done"},
	}

	if err := s.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAll returned %d records, want 1", len(all))
	}
	got := all[0]

	// Scalar fields.
	if got.View.ID != "deploy-1" {
		t.Errorf("ID = %q, want deploy-1", got.View.ID)
	}
	if got.View.Kind != "vsa+proxy" {
		t.Errorf("Kind = %q, want vsa+proxy", got.View.Kind)
	}
	if got.View.State != StateDone {
		t.Errorf("State = %q, want done", got.View.State)
	}
	if !got.View.HasWirer {
		t.Error("HasWirer should be true")
	}

	// Time round-trip (nanosecond precision).
	if got.View.CreatedAt.UnixNano() != created.UnixNano() {
		t.Errorf("CreatedAt round-trip: got %v want %v", got.View.CreatedAt, created)
	}
	if got.View.FinishedAt.UnixNano() != finished.UnixNano() {
		t.Errorf("FinishedAt round-trip: got %v want %v", got.View.FinishedAt, finished)
	}

	// Nodes JSON round-trip.
	if len(got.View.Nodes) != 2 {
		t.Fatalf("Nodes len = %d, want 2", len(got.View.Nodes))
	}
	if got.View.Nodes[0].Hostname != "vsa-01" || got.View.Nodes[0].VMID != "101" {
		t.Errorf("Nodes[0] = %+v, want vsa-01/101", got.View.Nodes[0])
	}
	if got.View.Nodes[1].Hostname != "proxy-01" || got.View.Nodes[1].VMID != "102" {
		t.Errorf("Nodes[1] = %+v, want proxy-01/102", got.View.Nodes[1])
	}

	// Form JSON round-trip.
	if got.View.Form.Kind != "vsa+proxy" || got.View.Form.Provider != "proxmox" {
		t.Errorf("Form = %+v, want vsa+proxy/proxmox", got.View.Form)
	}
	if got.View.Form.Text["hv_host"] != "192.168.1.10" {
		t.Errorf("Form.Text[hv_host] = %q, want 192.168.1.10", got.View.Form.Text["hv_host"])
	}
	if !got.View.Form.Checks["hv_insecure"] {
		t.Error("Form.Checks[hv_insecure] should be true")
	}

	// Lines round-trip.
	if len(got.Lines) != 3 {
		t.Fatalf("Lines len = %d, want 3", len(got.Lines))
	}
	if got.Lines[0] != "12:00:00 start" || got.Lines[2] != "12:02:00 done" {
		t.Errorf("Lines = %v, unexpected content", got.Lines)
	}

	// Zero FinishedAt survives (stored as 0 nanos → zero time).
	rec2 := PersistedDeployment{
		View: View{
			ID:        "deploy-2",
			Kind:      "test",
			State:     StatePending,
			CreatedAt: created,
			// FinishedAt deliberately zero
		},
	}
	if err := s.Save(rec2); err != nil {
		t.Fatalf("Save rec2: %v", err)
	}
	all2, _ := s.LoadAll()
	var p2 *PersistedDeployment
	for i := range all2 {
		if all2[i].View.ID == "deploy-2" {
			p2 = &all2[i]
			break
		}
	}
	if p2 == nil {
		t.Fatal("deploy-2 not found after LoadAll")
	}
	if !p2.View.FinishedAt.IsZero() {
		t.Errorf("FinishedAt should be zero, got %v", p2.View.FinishedAt)
	}

	// Upsert: state change must update, not duplicate.
	rec.View.State = StateFailed
	rec.View.Error = "boom"
	if err := s.Save(rec); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	all3, _ := s.LoadAll()
	var p1 *PersistedDeployment
	for i := range all3 {
		if all3[i].View.ID == "deploy-1" {
			p1 = &all3[i]
			break
		}
	}
	if p1 == nil {
		t.Fatal("deploy-1 not found after upsert")
	}
	if p1.View.State != StateFailed || p1.View.Error != "boom" {
		t.Errorf("upsert did not update: state=%s error=%s", p1.View.State, p1.View.Error)
	}

	// Delete removes the record.
	if err := s.Delete("deploy-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	all4, _ := s.LoadAll()
	for _, p := range all4 {
		if p.View.ID == "deploy-1" {
			t.Error("deploy-1 still present after Delete")
		}
	}
}
