package server

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/craftapi"
	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
)

// Deps bundles everything a handler may need.
type Deps struct {
	Version       string
	Commit        string // short git SHA baked at build time ("" for dev builds)
	BuildDate     string // build timestamp baked at build time ("" for dev builds)
	DataDir       string
	AutodeployDir string
	SettingsPath  string // path to settings.json on disk
	Store         *config.Store
	JobManager    *job.Manager
	DeployManager *deploy.Manager
	DeployPresets *deploy.PresetStore
	CraftPresets  *craftapi.PresetStore
}

type Server struct {
	deps      Deps
	templates map[string]map[string]*template.Template // lang → page-name → template
	static    fs.FS
	console   *consoleManager // in-memory VSA REST console sessions
}

func New(d Deps) *Server {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	s := &Server{
		deps:      d,
		templates: parseTemplates(),
		static:    staticSub,
		console:   newConsoleManager(),
	}
	s.console.startSweeper() // expire idle VSA console sessions
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/settings", s.handleAdminSettings)
	mux.HandleFunc("POST /admin/autodeploy/update", s.handleAdminUpdatePS1)
	mux.HandleFunc("POST /admin/autodeploy/upload", s.handleAdminUploadPS1)
	mux.HandleFunc("DELETE /admin/autodeploy/reset", s.handleAdminResetPS1)

	mux.HandleFunc("GET /lang/{code}", s.handleSetLang)
	mux.HandleFunc("GET /wizard", s.handleWizard)
	mux.HandleFunc("GET /mode/{mode}", s.handleSetMode)

	// Craft API = render-only REST call sequence generator.
	mux.HandleFunc("GET /craft-api", s.handleCraftAPI)
	mux.HandleFunc("POST /craft-api/render", s.handleCraftAPIRender)

	// Craft API templates (saved form snapshots — secrets excluded).
	mux.HandleFunc("GET /craft-api/presets", s.handleListCraftPresets)
	mux.HandleFunc("POST /craft-api/presets", s.handleSaveCraftPreset)
	mux.HandleFunc("GET /craft-api/presets/{name}", s.handleLoadCraftPreset)
	mux.HandleFunc("DELETE /craft-api/presets/{name}", s.handleDeleteCraftPreset)

	// Deploy = orchestrate a multi-VM Veeam topology onto a hypervisor.
	mux.HandleFunc("GET /deploy", s.handleDeployPage)
	mux.HandleFunc("POST /deploy", s.handleDeployStart)
	mux.HandleFunc("GET /deploy/{id}", s.handleDeployDetail)
	mux.HandleFunc("GET /deploy/{id}/stream", s.handleDeployStream)
	mux.HandleFunc("POST /deploy/{id}/stop", s.handleDeployStop)
	mux.HandleFunc("POST /deploy/{id}/remove", s.handleDeployRemove)
	mux.HandleFunc("POST /deploy/{id}/retry", s.handleDeployRetry)
	mux.HandleFunc("POST /deploy/{id}/rewire", s.handleDeployRewire)
	mux.HandleFunc("DELETE /deploy/{id}", s.handleDeployDelete)

	// VSA REST API console (sessions in-memory; proxied through this server).
	mux.HandleFunc("POST /deploy/{id}/console/open", s.handleConsoleOpen)
	mux.HandleFunc("POST /deploy/{id}/console/close", s.handleConsoleClose)
	mux.HandleFunc("GET /deploy/{id}/console/status", s.handleConsoleStatus)
	mux.HandleFunc("POST /deploy/{id}/console/request", s.handleConsoleRequest)

	mux.HandleFunc("POST /deploy/test-connection", s.handleTestConnection)
	mux.HandleFunc("POST /deploy/hypervisor/discover", s.handleHypervisorDiscover)

	// Deploy templates (named FormSnapshot presets). The literal "presets"
	// segment is matched ahead of the {id} wildcard by net/http's ServeMux.
	mux.HandleFunc("GET /deploy/presets", s.handleListDeployPresets)
	mux.HandleFunc("POST /deploy/presets", s.handleSaveDeployPreset)
	mux.HandleFunc("DELETE /deploy/presets/{name}", s.handleDeleteDeployPreset)

	mux.HandleFunc("GET /search", s.handleSearch)

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /new", s.handleNewJob)

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
