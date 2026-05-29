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
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

// --- Index --------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Preload form with defaults; the user can switch presets via the
	// dropdown without a full reload (HTMX swap).
	presetName := r.URL.Query().Get("preset")
	cfg := config.Defaults()
	if presetName != "" {
		if loaded, err := s.deps.Store.Load(presetName); err == nil {
			cfg = loaded
		}
	}

	// ?import=jobID pre-fills the form from the job's stored config JSON.
	if importID := r.URL.Query().Get("import"); importID != "" {
		cfgPath := filepath.Join(s.deps.DataDir, "output", importID, "job-config.json")
		if raw, err := os.ReadFile(cfgPath); err == nil {
			var imported config.Config
			if json.Unmarshal(raw, &imported) == nil {
				cfg = imported
			}
		}
	}

	presets, _ := s.deps.Store.List()

	data := map[string]any{
		"Version":         s.deps.Version,
		"Config":          cfg,
		"Presets":         presets,
		"PresetName":      presetName,
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
	// Output ISO is now in /data/output/{jobID}/{filename}
	path := filepath.Join(s.deps.DataDir, "output", j.ID, j.OutputISO)
	serveFileDownload(w, r, path, j.OutputISO)
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
		// fetch() sends FormData as multipart/form-data. r.ParseForm() sets
		// r.Form to non-nil but empty for multipart bodies, so subsequent
		// r.FormValue() calls skip re-parsing. ParseMultipartForm internally
		// calls ParseForm first (populating r.Form from the URL query), then
		// parses the multipart body. For url-encoded bodies it returns
		// ErrNotMultipart but r.Form is still correctly populated.
		if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
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

// --- Admin ------------------------------------------------------------------

const autodeployRawURL = "https://raw.githubusercontent.com/BaptisteTellier/autodeploy/dev/autodeploy.ps1"

// handleAdmin renders the admin/settings page.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	bakedPS1 := filepath.Join(s.deps.AutodeployDir, "autodeploy.ps1")
	overridePS1 := filepath.Join(s.deps.DataDir, "autodeploy", "autodeploy.ps1")

	bakedVer := extractPS1Version(bakedPS1)

	overrideVer := ""
	overrideMod := ""
	overrideActive := false
	if info, err := os.Stat(overridePS1); err == nil {
		overrideActive = true
		overrideMod = info.ModTime().Format("2006-01-02 15:04:05")
		overrideVer = extractPS1Version(overridePS1)
	}

	s.render(w, "views/admin.html", map[string]any{
		"Version":            s.deps.Version,
		"BakedPS1Version":    bakedVer,
		"OverridePS1Version": overrideVer,
		"OverrideModTime":    overrideMod,
		"OverrideActive":     overrideActive,
	})
}

// handleAdminUpdatePS1 downloads the latest autodeploy.ps1 from GitHub and
// saves it to /data/autodeploy/autodeploy.ps1 (used by the runner as override).
func (s *Server) handleAdminUpdatePS1(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(autodeployRawURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "téléchargement échoué : " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "GitHub a répondu : " + resp.Status})
		return
	}

	dir := filepath.Join(s.deps.DataDir, "autodeploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "mkdir : " + err.Error()})
		return
	}
	dst := filepath.Join(dir, "autodeploy.ps1")
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "création fichier : " + err.Error()})
		return
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "écriture : " + err.Error()})
		return
	}
	_ = f.Close() // flush before reading version

	ver := extractPS1Version(dst)
	now := time.Now().Format("2006-01-02 15:04:05")

	label := "autodeploy.ps1"
	if ver != "" {
		label = "autodeploy.ps1 v" + ver
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"bytes":      n,
		"version":    ver,
		"updated_at": now,
		"message":    fmt.Sprintf("%s téléchargé (%.1f KB)", label, float64(n)/1024),
	})
}

// handleAdminResetPS1 deletes the runtime override so the baked-in script is used again.
func (s *Server) handleAdminResetPS1(w http.ResponseWriter, r *http.Request) {
	dst := filepath.Join(s.deps.DataDir, "autodeploy", "autodeploy.ps1")
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "suppression échouée : "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Media managers (ISO source + output) --------------------------------

// MediaFile holds metadata about a file in a managed directory.
type MediaFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

func listDirInfo(dir string, exts []string) []MediaFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []MediaFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(exts) > 0 {
			match := false
			for _, ext := range exts {
				if strings.EqualFold(filepath.Ext(name), ext) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Skip symlinks — they are runner staging artifacts, not real workspace files.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		out = append(out, MediaFile{Name: name, Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- Workspace (all files in /data/iso) ---------------------------------

func (s *Server) handleMediaWorkspace(w http.ResponseWriter, r *http.Request) {
	s.render(w, "views/media_workspace.html", map[string]any{
		"Version": s.deps.Version,
		"Files":   listDirInfo(filepath.Join(s.deps.DataDir, "iso"), nil), // all extensions
	})
}

func (s *Server) handleWorkspaceContent(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	serveTextContent(w, filepath.Join(s.deps.DataDir, "iso", name))
}

func (s *Server) handleWorkspaceDownload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	serveFileDownload(w, r, filepath.Join(s.deps.DataDir, "iso", name), name)
}

// --- Output (per-job subfolders in /data/output) -----------------------

// OutputJobInfo holds metadata shown on the output index page.
type OutputJobInfo struct {
	JobID        string
	FriendlyName string // ApplianceType-Hostname[-OutputISO] or short UUID fallback
	ModTime      time.Time
	FileCount    int
	TotalSize    int64
}

func (s *Server) handleMediaOutput(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(s.deps.DataDir, "output")
	entries, _ := os.ReadDir(dir)
	var jobs []OutputJobInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, _ := e.Info()
		modTime := time.Time{}
		if info != nil {
			modTime = info.ModTime()
		}
		// Count files and total size (excluding the config snapshot).
		files := listDirInfo(filepath.Join(dir, e.Name()), nil)
		var total int64
		for _, f := range files {
			total += f.Size
		}
		// Build human-readable name from stored config JSON.
		friendlyName := e.Name()[:min(8, len(e.Name()))] + "…"
		cfgPath := filepath.Join(dir, e.Name(), "job-config.json")
		if raw, err := os.ReadFile(cfgPath); err == nil {
			var meta struct {
				ApplianceType string `json:"ApplianceType"`
				Hostname      string `json:"Hostname"`
				OutputISO     string `json:"OutputISO"`
			}
			if json.Unmarshal(raw, &meta) == nil && meta.Hostname != "" {
				friendlyName = meta.ApplianceType + "-" + meta.Hostname
				if meta.OutputISO != "" {
					friendlyName += "-" + meta.OutputISO
				}
			}
		}
		jobs = append(jobs, OutputJobInfo{
			JobID:        e.Name(),
			FriendlyName: friendlyName,
			ModTime:      modTime,
			FileCount:    len(files),
			TotalSize:    total,
		})
	}
	// Sort newest first.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ModTime.After(jobs[j].ModTime) })
	s.render(w, "views/media_output.html", map[string]any{
		"Version": s.deps.Version,
		"Jobs":    jobs,
	})
}

// handleDeleteOutputJob removes an entire job output folder.
func (s *Server) handleDeleteOutputJob(w http.ResponseWriter, r *http.Request) {
	jobID := filepath.Base(r.PathValue("jobid"))
	dir := filepath.Join(s.deps.DataDir, "output", jobID)
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteOutputJobFile removes a single file from a job output folder.
func (s *Server) handleDeleteOutputJobFile(w http.ResponseWriter, r *http.Request) {
	jobID := filepath.Base(r.PathValue("jobid"))
	name := filepath.Base(r.PathValue("name"))
	path := filepath.Join(s.deps.DataDir, "output", jobID, name)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMediaOutputJob(w http.ResponseWriter, r *http.Request) {
	jobID := filepath.Base(r.PathValue("jobid"))
	dir := filepath.Join(s.deps.DataDir, "output", jobID)
	if _, err := os.Stat(dir); err != nil {
		http.NotFound(w, r)
		return
	}
	// Compute a human-readable name from the stored config snapshot.
	friendlyName := jobID[:min(8, len(jobID))] + "…"
	cfgPath := filepath.Join(dir, "job-config.json")
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var meta struct {
			ApplianceType string `json:"ApplianceType"`
			Hostname      string `json:"Hostname"`
			OutputISO     string `json:"OutputISO"`
		}
		if json.Unmarshal(raw, &meta) == nil && meta.Hostname != "" {
			friendlyName = meta.ApplianceType + "-" + meta.Hostname
			if meta.OutputISO != "" {
				friendlyName += "-" + meta.OutputISO
			}
		}
	}
	s.render(w, "views/media_output_job.html", map[string]any{
		"Version":      s.deps.Version,
		"JobID":        jobID,
		"FriendlyName": friendlyName,
		"Files":        listDirInfo(dir, nil),
	})
}

func (s *Server) handleOutputJobContent(w http.ResponseWriter, r *http.Request) {
	jobID := filepath.Base(r.PathValue("jobid"))
	name := filepath.Base(r.PathValue("name"))
	serveTextContent(w, filepath.Join(s.deps.DataDir, "output", jobID, name))
}

func (s *Server) handleOutputJobDownload(w http.ResponseWriter, r *http.Request) {
	jobID := filepath.Base(r.PathValue("jobid"))
	name := filepath.Base(r.PathValue("name"))
	serveFileDownload(w, r, filepath.Join(s.deps.DataDir, "output", jobID, name), name)
}

// --- Shared helpers -----------------------------------------------------

// serveTextContent reads up to 1 MB of a text file and sends it as plain text.
func serveTextContent(w http.ResponseWriter, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, io.LimitReader(f, 1<<20)) // 1 MB cap
}

// serveFileDownload sends a file as an attachment.
func serveFileDownload(w http.ResponseWriter, r *http.Request, path, name string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) handleMediaLicense(w http.ResponseWriter, r *http.Request) {
	s.render(w, "views/media_license.html", map[string]any{
		"Version": s.deps.Version,
		"Files":   listDirInfo(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}),
	})
}

func (s *Server) handleUploadLicense(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB — licence files are tiny
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()

	name := filepath.Base(hdr.Filename)
	if name == "" || name == "." {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(filepath.Ext(name), ".lic") {
		http.Error(w, "only .lic files are accepted", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(s.deps.DataDir, "license")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dst := filepath.Join(dir, name)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, f); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/media/license", http.StatusSeeOther)
}

func (s *Server) handleUploadWorkspace(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 25<<30) // 25 GB limit
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()

	name := filepath.Base(hdr.Filename)
	if name == "" || name == "." {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(filepath.Ext(name), ".iso") {
		http.Error(w, "only .iso files are accepted", http.StatusBadRequest)
		return
	}
	dst := filepath.Join(s.deps.DataDir, "iso", name)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, f); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/media/workspace", http.StatusSeeOther)
}

func (s *Server) handleDeleteMediaFile(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	name := filepath.Base(r.PathValue("name"))

	var dir string
	switch kind {
	case "workspace":
		dir = filepath.Join(s.deps.DataDir, "iso")
	case "license":
		dir = filepath.Join(s.deps.DataDir, "license")
	default:
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/media/"+kind, http.StatusSeeOther)
}

func (s *Server) handleRenameMediaFile(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	oldName := filepath.Base(r.PathValue("name"))

	var dir string
	switch kind {
	case "workspace":
		dir = filepath.Join(s.deps.DataDir, "iso")
	case "license":
		dir = filepath.Join(s.deps.DataDir, "license")
	default:
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newName := filepath.Base(strings.TrimSpace(r.FormValue("new_name")))
	if newName == "" || newName == "." {
		http.Error(w, "invalid new name", http.StatusBadRequest)
		return
	}
	if err := os.Rename(filepath.Join(dir, oldName), filepath.Join(dir, newName)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/media/"+kind, http.StatusSeeOther)
}

// --- Library (ISO/license/conf inventory) --------------------------------

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

// extractPS1Version reads the first 8 KB of a PowerShell script and returns
// the version string found on a ".VERSION x.y" line (PSScriptInfo block).
func extractPS1Version(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > 8192 {
		data = data[:8192]
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), ".VERSION") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
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
