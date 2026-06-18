package craftapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

// PresetStore persists named Craft API form templates on disk — one JSON file
// per template, atomic-rename writes. Only non-secret fields are stored (password
// and s3_secret_key are deliberately excluded).
type PresetStore struct {
	dir string
	mu  sync.Mutex
}

// NewPresetStore returns a store rooted at dir (created if absent).
func NewPresetStore(dir string) *PresetStore {
	_ = os.MkdirAll(dir, 0o755)
	return &PresetStore{dir: dir}
}

var craftPresetNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,64}$`)

func (s *PresetStore) path(name string) (string, error) {
	if !craftPresetNameRe.MatchString(name) {
		return "", errors.New("invalid template name (allowed: alphanumeric, space, dash, dot, underscore — up to 64 chars)")
	}
	return filepath.Join(s.dir, name+".json"), nil
}

// List returns the stored template names sorted alphabetically.
func (s *PresetStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		out = append(out, e.Name()[:len(e.Name())-len(".json")])
	}
	sort.Strings(out)
	return out, nil
}

// Load deserialises template <name> into a flat map of field values.
func (s *PresetStore) Load(name string) (map[string]string, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]string
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return fields, nil
}

// secretFields lists the field names that must never be persisted.
var secretFields = map[string]bool{
	"password":      true,
	"s3_secret_key": true,
}

// Save atomically writes template <name> with the non-secret subset of fields.
func (s *PresetStore) Save(name string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.path(name)
	if err != nil {
		return err
	}
	// Strip secrets before persisting.
	safe := make(map[string]string, len(fields))
	for k, v := range fields {
		if !secretFields[k] {
			safe[k] = v
		}
	}
	b, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes template <name>.
func (s *PresetStore) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}
