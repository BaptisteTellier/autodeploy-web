package config

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

// Store persists named JSON presets on disk. One file per preset, lock-free
// reads thanks to atomic rename writes.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,64}$`)

// PresetInfo is returned by List.
type PresetInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) path(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", errors.New("invalid preset name (allowed: alphanumeric, space, dash, dot, underscore — up to 64 chars)")
	}
	return filepath.Join(s.dir, name+".json"), nil
}

// List returns sorted (by name) presets currently stored.
func (s *Store) List() ([]PresetInfo, error) {
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
			Size:      info.Size(),
			UpdatedAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load deserialises preset <name> into a Config. Defaults are applied first
// so any key missing from the JSON keeps the built-in value (same behaviour
// as the PS1 since v2.7).
func (s *Store) Load(name string) (Config, error) {
	p, err := s.path(name)
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Config{}, err
	}
	cfg := Defaults()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return cfg, nil
}

// Save atomically writes preset <name>.
func (s *Store) Save(name string, c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.path(name)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// EnsureDataLayout creates the volume sub-directories the PS1 expects.
func EnsureDataLayout(root string) error {
	subs := []string{"iso", "output", "license", "conf", "configs", "work", "deploy-presets"}
	for _, s := range subs {
		if err := os.MkdirAll(filepath.Join(root, s), 0o755); err != nil {
			return err
		}
	}
	return nil
}
