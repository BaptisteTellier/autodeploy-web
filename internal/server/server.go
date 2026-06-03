package server

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
)

// Deps bundles everything a handler may need.
type Deps struct {
	Version       string
	Commit        string // short git SHA baked at build time ("" for dev builds)
	BuildDate     string // build timestamp baked at build time ("" for dev builds)
	DataDir       string
	AutodeployDir string
	Store         *config.Store
	JobManager    *job.Manager
}

type Server struct {
	deps      Deps
	templates map[string]map[string]*template.Template // lang → page-name → template
	static    fs.FS
}

func New(d Deps) *Server {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return &Server{
		deps:      d,
		templates: parseTemplates(),
		static:    staticSub,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/autodeploy/update", s.handleAdminUpdatePS1)
	mux.HandleFunc("POST /admin/autodeploy/upload", s.handleAdminUploadPS1)
	mux.HandleFunc("DELETE /admin/autodeploy/reset", s.handleAdminResetPS1)

	mux.HandleFunc("GET /lang/{code}", s.handleSetLang)
	mux.HandleFunc("GET /wizard", s.handleWizard)
	mux.HandleFunc("GET /mode/{mode}", s.handleSetMode)

	mux.HandleFunc("GET /", s.handleIndex)

	mux.HandleFunc("POST /jobs", s.handleCreateJob)
	mux.HandleFunc("GET /jobs", s.handleListJobs)
	mux.HandleFunc("GET /jobs/{id}", s.handleJobDetail)
	mux.HandleFunc("GET /jobs/{id}/stream", s.handleJobStream)
	mux.HandleFunc("GET /jobs/{id}/download", s.handleJobDownload)
	mux.HandleFunc("DELETE /jobs/{id}", s.handleDeleteJob)

	mux.HandleFunc("GET /configs", s.handleListConfigs)
	mux.HandleFunc("POST /configs", s.handleSaveConfig)
	mux.HandleFunc("GET /configs/{name}", s.handleLoadConfig)
	mux.HandleFunc("DELETE /configs/{name}", s.handleDeleteConfig)

	mux.HandleFunc("GET /library/{kind}", s.handleListLibrary)

	// Workspace = /data/iso (all files: source ISOs, generated cfg/logs)
	mux.HandleFunc("GET /media/workspace", s.handleMediaWorkspace)
	mux.HandleFunc("POST /media/workspace/upload", s.handleUploadWorkspace)
	mux.HandleFunc("GET /media/workspace/{name}/download", s.handleWorkspaceDownload)
	mux.HandleFunc("GET /media/workspace/{name}/content", s.handleWorkspaceContent)

	// Output = /data/output/{jobID}/ (per-job folders)
	mux.HandleFunc("GET /media/output", s.handleMediaOutput)
	mux.HandleFunc("GET /media/output/{jobid}", s.handleMediaOutputJob)
	mux.HandleFunc("DELETE /media/output/{jobid}", s.handleDeleteOutputJob)
	mux.HandleFunc("GET /media/output/{jobid}/{name}/download", s.handleOutputJobDownload)
	mux.HandleFunc("GET /media/output/{jobid}/{name}/content", s.handleOutputJobContent)
	mux.HandleFunc("DELETE /media/output/{jobid}/{name}", s.handleDeleteOutputJobFile)

	// Licenses
	mux.HandleFunc("GET /media/license", s.handleMediaLicense)
	mux.HandleFunc("POST /media/license/upload", s.handleUploadLicense)

	// Generic delete/rename for workspace + license
	mux.HandleFunc("DELETE /media/{kind}/{name}", s.handleDeleteMediaFile)
	mux.HandleFunc("POST /media/{kind}/{name}/rename", s.handleRenameMediaFile)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return logging(recover_(mux))
}
