package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/craftapi"
	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
	"github.com/BaptisteTellier/autodeploy-web/internal/server"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// shortCommit returns the first 7 chars of the build commit SHA (or "").
func shortCommit() string {
	if len(commit) >= 7 {
		return commit[:7]
	}
	return commit
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("autodeploy-web %s (commit %s, built %s) starting", version, shortCommit(), date)

	addr := envDefault("LISTEN_ADDR", ":8080")
	dataDir := envDefault("DATA_DIR", "/data")
	autodeployDir := envDefault("AUTODEPLOY_DIR", "/opt/autodeploy")
	psScript := envDefault("PS_SCRIPT", "autodeploy.ps1")
	concurrency := envInt("WORKER_CONCURRENCY", 1)

	if err := config.EnsureDataLayout(dataDir); err != nil {
		log.Fatalf("data layout: %v", err)
	}

	settingsPath := filepath.Join(dataDir, "settings.json")
	settings := config.LoadSettings(settingsPath)

	// Clear any stale per-job staging dirs left over from a crash, then
	// recreate the empty work directory so it's ready for new jobs.
	workDir := filepath.Join(dataDir, "work")
	_ = os.RemoveAll(workDir)
	_ = os.MkdirAll(workDir, 0o755)

	store := config.NewStore(dataDir + "/configs")

	jobStore, err := job.OpenStore(filepath.Join(dataDir, "jobs.db"))
	if err != nil {
		log.Fatalf("job store: %v", err)
	}
	defer jobStore.Close()

	mgr := job.NewManager(job.Options{
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		PSScript:      psScript,
		MaxConcurrent: concurrency,
		KeepCompleted: settings.MaxHistory,
		Store:         jobStore,
	})

	deployStore, err := deploy.OpenStore(filepath.Join(dataDir, "deployments.db"))
	if err != nil {
		log.Fatalf("deploy store: %v", err)
	}
	defer deployStore.Close()

	deployMgr := deploy.NewManager(deployStore)
	deployMgr.SetKeepCompleted(settings.MaxHistory)
	deployPresets := deploy.NewPresetStore(filepath.Join(dataDir, "deploy-presets"))
	craftPresets := craftapi.NewPresetStore(filepath.Join(dataDir, "craft-presets"))

	srv := server.New(server.Deps{
		Version:       version,
		Commit:        shortCommit(),
		BuildDate:     date,
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		SettingsPath:  settingsPath,
		Store:         store,
		JobManager:    mgr,
		DeployManager: deployMgr,
		DeployPresets: deployPresets,
		CraftPresets:  craftPresets,

		AuthDisabled:      envBool("AUTH_DISABLED", false),
		AdminPasswordHash: os.Getenv("ADMIN_PASSWORD_HASH"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
	})

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	deployMgr.Shutdown(ctx)
	mgr.Shutdown(ctx)
	log.Println("bye")
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envBool parses a boolean env var (1/t/true/0/f/false…) with a default.
func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
