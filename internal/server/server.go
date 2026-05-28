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
	DataDir       string
	AutodeployDir string
	Store         *config.Store
	JobManager    *job.Manager
}

type Server struct {
	deps      Deps
	templates map[string]*template.Template
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

	mux.HandleFunc("GET /media/iso", s.handleMediaISO)
	mux.HandleFunc("GET /media/output", s.handleMediaOutput)
	mux.HandleFunc("POST /media/iso/upload", s.handleUploadISO)
	mux.HandleFunc("DELETE /media/{kind}/{name}", s.handleDeleteMediaFile)
	mux.HandleFunc("POST /media/{kind}/{name}/rename", s.handleRenameMediaFile)
	mux.HandleFunc("GET /media/output/{name}/download", s.handleDownloadOutputFile)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return logging(recover_(mux))
}
