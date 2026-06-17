package config

import (
	"encoding/json"
	"os"
)

// DefaultMaxHistory is the default cap for finished jobs and deployments kept
// in the history (in-memory registry + SQLite store).
const DefaultMaxHistory = 20

// Settings holds user-configurable runtime preferences persisted to disk.
type Settings struct {
	MaxHistory int `json:"max_history"`
}

// LoadSettings reads the settings JSON at path. On any error (missing file,
// parse failure) it returns Settings with the compiled-in defaults. A loaded
// MaxHistory <= 0 is also coerced to DefaultMaxHistory.
func LoadSettings(path string) Settings {
	b, err := os.ReadFile(path)
	if err != nil {
		return Settings{MaxHistory: DefaultMaxHistory}
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{MaxHistory: DefaultMaxHistory}
	}
	if s.MaxHistory <= 0 {
		s.MaxHistory = DefaultMaxHistory
	}
	return s
}

// SaveSettings atomically writes s to path (tmp + rename, 0o644), matching
// the atomic-write pattern used by config.Store.Save.
func SaveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
