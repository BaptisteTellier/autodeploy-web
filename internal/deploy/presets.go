package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// PresetStore persists named deploy templates (FormSnapshot) on disk — one
// JSON file per template, atomic-rename writes — mirroring config.Store. A
// template is the non-secret deploy-form state, so it can be reloaded into the
// form later (passwords / keys are never part of a FormSnapshot).
type PresetStore struct {
	dir string
	mu  sync.Mutex
}

// NewPresetStore returns a store rooted at dir (created if absent).
func NewPresetStore(dir string) *PresetStore {
	_ = os.MkdirAll(dir, 0o755)
	return &PresetStore{dir: dir}
}

var presetNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,64}$`)

// PresetInfo is one entry returned by List.
type PresetInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *PresetStore) path(name string) (string, error) {
	if !presetNameRe.MatchString(name) {
		return "", errors.New("invalid template name (allowed: alphanumeric, space, dash, dot, underscore — up to 64 chars)")
	}
	return filepath.Join(s.dir, name+".json"), nil
}

// List returns the stored templates sorted by name.
func (s *PresetStore) List() ([]PresetInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]PresetInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, PresetInfo{
			Name:      e.Name()[:len(e.Name())-len(".json")],
			UpdatedAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load deserialises template <name> into a FormSnapshot.
func (s *PresetStore) Load(name string) (FormSnapshot, error) {
	p, err := s.path(name)
	if err != nil {
		return FormSnapshot{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return FormSnapshot{}, err
	}
	var snap FormSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return FormSnapshot{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return snap, nil
}

// Save atomically writes template <name>.
func (s *PresetStore) Save(name string, snap FormSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.path(name)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
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
