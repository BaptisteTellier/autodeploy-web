package job

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a Store in a throwaway directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreRoundTrip(t *testing.T) {
	s := newTestStore(t)

	created := time.Now()
	v := JobView{
		ID:        "job-1",
		State:     StateDone,
		Hostname:  "vsa01",
		Appliance: "VSA",
		SourceISO: "veeam.iso",
		OutputISO: "veeam_customized.iso",
		CreatedAt: created,
		// StartedAt left zero on purpose to exercise the zero-time path.
		FinishedAt:   created.Add(time.Minute),
		ExitCode:     0,
		ErrorMessage: "",
	}
	if err := s.SaveJob(v, "/data/configs/.jobs/job-1.json"); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	got, err := s.LoadJobs()
	if err != nil {
		t.Fatalf("LoadJobs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LoadJobs returned %d records, want 1", len(got))
	}
	p := got[0]
	if p.View.ID != "job-1" || p.View.State != StateDone || p.View.Hostname != "vsa01" {
		t.Errorf("scalar fields mismatch: %+v", p.View)
	}
	if p.ConfigPath != "/data/configs/.jobs/job-1.json" || p.View.ConfigPath != p.ConfigPath {
		t.Errorf("config path mismatch: %q / %q", p.ConfigPath, p.View.ConfigPath)
	}
	if p.View.CreatedAt.UnixNano() != created.UnixNano() {
		t.Errorf("CreatedAt round-trip: got %v want %v", p.View.CreatedAt, created)
	}
	if !p.View.StartedAt.IsZero() {
		t.Errorf("StartedAt should be zero, got %v", p.View.StartedAt)
	}

	// Upsert: changing state must update, not duplicate.
	v.State = StateFailed
	v.ErrorMessage = "boom"
	if err := s.SaveJob(v, "/data/configs/.jobs/job-1.json"); err != nil {
		t.Fatalf("SaveJob (update): %v", err)
	}
	got, _ = s.LoadJobs()
	if len(got) != 1 {
		t.Fatalf("after upsert: %d records, want 1", len(got))
	}
	if got[0].View.State != StateFailed || got[0].View.ErrorMessage != "boom" {
		t.Errorf("upsert did not update: %+v", got[0].View)
	}

	// Delete.
	if err := s.DeleteJob("job-1"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	got, _ = s.LoadJobs()
	if len(got) != 0 {
		t.Fatalf("after delete: %d records, want 0", len(got))
	}
}

// TestManagerLoadsAndNormalizesCrashedJobs verifies that a job persisted as
// running/pending (a process that died mid-flight) is reloaded as failed with a
// clear message — and that the normalisation is written back to the store.
func TestManagerLoadsAndNormalizesCrashedJobs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	// Seed one "running" (crashed) job and one clean "done" job.
	mustSave(t, s, JobView{ID: "crashed", State: StateRunning, Hostname: "a", Appliance: "VSA", CreatedAt: time.Now()})
	mustSave(t, s, JobView{ID: "ok", State: StateDone, Hostname: "b", Appliance: "VIA", CreatedAt: time.Now()})

	m := NewManager(Options{DataDir: dir, Store: s})

	j, ok := m.Get("crashed")
	if !ok {
		t.Fatal("crashed job not loaded")
	}
	v := j.View()
	if v.State != StateFailed {
		t.Errorf("crashed job state = %s, want failed", v.State)
	}
	if v.ErrorMessage == "" {
		t.Error("crashed job should carry a normalisation error message")
	}
	if v.FinishedAt.IsZero() {
		t.Error("normalised job should have a FinishedAt")
	}

	if ok2, ok := m.Get("ok"); !ok || ok2.StateString() != string(StateDone) {
		t.Error("clean done job should load unchanged")
	}

	// Normalisation must be persisted.
	got, _ := s.LoadJobs()
	for _, p := range got {
		if p.View.ID == "crashed" && p.View.State != StateFailed {
			t.Errorf("normalisation not persisted: %s", p.View.State)
		}
	}
	_ = s.Close()
}

// TestManagerDeleteRemovesFromStore verifies Delete removes the DB row too.
func TestManagerDeleteRemovesFromStore(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	mustSave(t, s, JobView{ID: "del", State: StateDone, Hostname: "h", Appliance: "VSA", CreatedAt: time.Now()})

	m := NewManager(Options{DataDir: dir, Store: s})
	if err := m.Delete("del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := m.Get("del"); ok {
		t.Error("job still in registry after Delete")
	}
	got, _ := s.LoadJobs()
	if len(got) != 0 {
		t.Errorf("job row still in store after Delete: %d rows", len(got))
	}
}

func mustSave(t *testing.T, s *Store, v JobView) {
	t.Helper()
	if err := s.SaveJob(v, "/tmp/"+v.ID+".json"); err != nil {
		t.Fatalf("SaveJob %s: %v", v.ID, err)
	}
}
