package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
)

// isoFile is a Source ISO entry for the dashboard "Source ISOs" card / KPI.
type isoFile struct {
	Name string
	Size int64
}

// firstN returns the first n elements of s (or all of them if shorter).
func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// listSourceISOs returns the .iso files staged under <DataDir>/iso, sorted by
// name, with their byte sizes (for the dashboard card + the "GB staged" KPI).
func listSourceISOs(dir string) []isoFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]isoFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".iso") {
			continue
		}
		var size int64
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, isoFile{Name: e.Name(), Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// handleIndex renders the dashboard at "/" — the launcher (Launchpad) and an
// ops overview (Console), toggled client-side. The expert build form lives at
// /new (handleNewJob).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	jobs := s.deps.JobManager.List() // newest first
	var deps []deploy.View
	if s.deps.DeployManager != nil {
		deps = s.deps.DeployManager.List() // newest first
	}
	isos := listSourceISOs(filepath.Join(s.deps.DataDir, "iso"))

	// --- KPIs -------------------------------------------------------------
	jobsDone := 0
	for _, j := range jobs {
		if string(j.State) == "done" {
			jobsDone++
		}
	}
	depsDone, depsFinished, depsRunning := 0, 0, 0
	for _, d := range deps {
		switch d.State {
		case deploy.StateDone:
			depsDone++
			depsFinished++
		case deploy.StateFailed, deploy.StateCanceled, deploy.StateRemoved:
			depsFinished++
		case deploy.StateRunning, deploy.StatePending:
			depsRunning++
		}
	}
	successRate := 0
	if depsFinished > 0 {
		successRate = depsDone * 100 / depsFinished
	}
	var isoBytes int64
	for _, f := range isos {
		isoBytes += f.Size
	}

	s.render(w, r, "views/dashboard.html", map[string]any{
		"RecentJobs":  firstN(jobs, 5),
		"RecentDeps":  firstN(deps, 5),
		"SourceISOs":  isos,
		"KPIIsos":     len(jobs),
		"KPIIsosMeta": fmt.Sprintf("%d succeeded", jobsDone),
		"KPIDeploys":  len(deps),
		"KPIRunning":  depsRunning,
		"KPISuccess":  successRate,
		"KPISuccessMeta": func() string {
			if depsFinished == 0 {
				return "no runs yet"
			}
			return fmt.Sprintf("%d of %d done", depsDone, depsFinished)
		}(),
		"KPISources":  len(isos),
		"KPISourceSz": isoBytes,
	})
}
