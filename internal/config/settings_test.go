package config

import (
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	original := Settings{MaxHistory: 42}
	if err := SaveSettings(path, original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	loaded := LoadSettings(path)
	if loaded.MaxHistory != original.MaxHistory {
		t.Errorf("got MaxHistory=%d, want %d", loaded.MaxHistory, original.MaxHistory)
	}
}

func TestSettingsMissingFile(t *testing.T) {
	s := LoadSettings(filepath.Join(t.TempDir(), "nonexistent.json"))
	if s.MaxHistory != DefaultMaxHistory {
		t.Errorf("missing file: got MaxHistory=%d, want %d (default)", s.MaxHistory, DefaultMaxHistory)
	}
}

func TestSettingsMaxHistoryZeroCoerced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// Write a settings file with MaxHistory=0 (invalid).
	if err := SaveSettings(path, Settings{MaxHistory: 0}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s := LoadSettings(path)
	if s.MaxHistory != DefaultMaxHistory {
		t.Errorf("MaxHistory=0 in file: got %d, want %d (default)", s.MaxHistory, DefaultMaxHistory)
	}
}

func TestSettingsMaxHistoryNegativeCoerced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if err := SaveSettings(path, Settings{MaxHistory: -5}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s := LoadSettings(path)
	if s.MaxHistory != DefaultMaxHistory {
		t.Errorf("MaxHistory=-5 in file: got %d, want %d (default)", s.MaxHistory, DefaultMaxHistory)
	}
}
