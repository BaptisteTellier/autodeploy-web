package craftapi

import (
	"errors"
	"os"
	"testing"
)

func TestPresetStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ps := NewPresetStore(dir)

	// List on empty store must return nil, not an error.
	names, err := ps.List()
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty list, got %v", names)
	}

	// Save a template.
	fields := map[string]string{
		"api_version":         "1.3-rev2",
		"username":            "veeamadmin",
		"password":            "s3cr3t",      // must be stripped
		"s3_secret_key":       "supersecret", // must be stripped
		"repo_path":           "/backups",
		"repo_immutable_days": "7",
	}
	if err := ps.Save("my template", fields); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// List must return the saved name.
	names, err = ps.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "my template" {
		t.Fatalf("List: got %v", names)
	}

	// Load and verify secrets were stripped.
	loaded, err := ps.Load("my template")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded["api_version"] != "1.3-rev2" {
		t.Errorf("api_version: got %q", loaded["api_version"])
	}
	if loaded["username"] != "veeamadmin" {
		t.Errorf("username: got %q", loaded["username"])
	}
	if _, ok := loaded["password"]; ok {
		t.Error("password must not be stored")
	}
	if _, ok := loaded["s3_secret_key"]; ok {
		t.Error("s3_secret_key must not be stored")
	}
	if loaded["repo_path"] != "/backups" {
		t.Errorf("repo_path: got %q", loaded["repo_path"])
	}

	// Save a second template.
	if err := ps.Save("alpha", map[string]string{"api_version": "1.2"}); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}

	// List must be sorted.
	names, err = ps.List()
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "my template" {
		t.Fatalf("sorted list: got %v", names)
	}

	// Delete.
	if err := ps.Delete("alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	names, err = ps.List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(names) != 1 || names[0] != "my template" {
		t.Fatalf("after delete: got %v", names)
	}

	// Delete non-existent must return ErrNotExist.
	if err := ps.Delete("alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Delete missing: expected ErrNotExist, got %v", err)
	}
}

func TestPresetStoreInvalidName(t *testing.T) {
	dir := t.TempDir()
	ps := NewPresetStore(dir)

	unsafe := []string{
		"../escape",
		"foo/bar",
		"",
		"a very long name that exceeds sixty four characters xxxxxxxxxxxxxxxxxxxxxxxxxx",
		"name\x00with\nnull",
	}
	for _, name := range unsafe {
		if err := ps.Save(name, map[string]string{"k": "v"}); err == nil {
			t.Errorf("Save(%q): expected error for unsafe name", name)
		}
	}
}
