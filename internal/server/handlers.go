package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

// --- Index --------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Preload form with defaults; the user can switch presets via the
	// dropdown without a full reload (HTMX swap).
	preset := r.URL.Query().Get("preset")
	cfg := config.Defaults()
	if preset != "" {
		if loaded, err := s.deps.Store.Load(preset); err == nil {
			cfg = loaded
		}
	}

	presets, _ := s.deps.Store.List()

	data := map[string]any{
		"Version":         s.deps.Version,
		"Config":          cfg,
		"Presets":         presets,
		"ApplianceTypes":  config.ApplianceTypes,
		"KeyboardLayouts": config.KeyboardLayouts,
		"Timezones":       config.Timezones,
		"SourceISOs":      listDir(filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}),
		"LicenseFiles":    listDir(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}),
		"Jobs":            s.deps.JobManager.List(),
	}
	s.render(w, "views/form.html", data)
}

// --- Jobs ---------------------------------------------------------------

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := configFromForm(r)
	if err != nil {
		http.Error(w, "form parsing: "+err.Error(), http.StatusBadRequest)
		return
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		// Re-render the form with errors.
		data := map[string]any{
			"Version":         s.deps.Version,
			"Config":          cfg,
			"Presets":         presetListOrEmpty(s.deps.Store),
			"ApplianceTypes":  config.ApplianceTypes,
			"KeyboardLayouts": config.KeyboardLayouts,
			"Timezones":       config.Timezones,
			"SourceISOs":      listDir(filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}),
			"LicenseFiles":    listDir(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}),
			"Jobs":            s.deps.JobManager.List(),
			"Errors":          errs,
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "views/form.html", data)
		return
	}

	j, err := s.deps.JobManager.Submit(cfg)
	if err != nil {
		http.Error(w, "submit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/jobs/"+j.ID, http.StatusSeeOther)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if accept := r.Header.Get("Accept"); strings.Contains(accept, "application/json") {
		writeJSON(w, s.deps.JobManager.List())
		return
	}
	s.render(w, "views/jobs.html", map[string]any{
		"Version": s.deps.Version,
		"Jobs":    s.deps.JobManager.List(),
	})
}

func (s *Server) handleJobDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.deps.JobManager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "views/job.html", map[string]any{
		"Version": s.deps.Version,
		"Job":     j,
		"Lines":   j.Snapshot(),
	})
}

func (s *Server) handleJobDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.deps.JobManager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.deps.DataDir, "output", j.OutputISO)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "ISO not available yet", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, j.OutputISO))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	http.ServeContent(w, r, j.OutputISO, info.ModTime(), f)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.JobManager.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("")) // HTMX swap empties the row
		return
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

// --- Presets ------------------------------------------------------------

func (s *Server) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

type savePresetReq struct {
	Name   string        `json:"name"`
	Config config.Config `json:"config"`
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	var req savePresetReq
	if strings.HasPrefix(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, err := configFromForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(r.FormValue("preset_name"))
		req.Config = cfg
	}
	if req.Name == "" {
		http.Error(w, "preset name required", http.StatusBadRequest)
		return
	}
	if err := s.deps.Store.Save(req.Name, req.Config); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Trigger", "preset-saved")
		_, _ = fmt.Fprintf(w, `<span class="text-emerald-600">Saved preset "%s"</span>`, req.Name)
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "name": req.Name})
}

func (s *Server) handleLoadConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.deps.Store.Load(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Used by JS "Import preset" feature to refill the form via fetch.
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cfg)
}

func (s *Server) handleDeleteConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.deps.Store.Delete(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Library (ISO/license/conf inventory) -------------------------------

func (s *Server) handleListLibrary(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	var dir string
	var exts []string
	switch kind {
	case "iso":
		dir, exts = filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}
	case "license":
		dir, exts = filepath.Join(s.deps.DataDir, "license"), []string{".lic"}
	case "output":
		dir, exts = filepath.Join(s.deps.DataDir, "output"), []string{".iso"}
	case "conf":
		dir, exts = filepath.Join(s.deps.DataDir, "conf"), nil
	default:
		http.Error(w, "unknown library kind", http.StatusBadRequest)
		return
	}
	writeJSON(w, listDir(dir, exts))
}

// --- Helpers ------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func listDir(dir string, exts []string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if len(exts) > 0 {
			match := false
			for _, ext := range exts {
				if strings.EqualFold(filepath.Ext(e.Name()), ext) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func presetListOrEmpty(s *config.Store) []config.PresetInfo {
	items, _ := s.List()
	if items == nil {
		return []config.PresetInfo{}
	}
	return items
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		_, _ = io.WriteString(w, "<pre>template error: "+err.Error()+"</pre>")
	}
}
