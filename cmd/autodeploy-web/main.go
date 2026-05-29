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
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
	"github.com/BaptisteTellier/autodeploy-web/internal/server"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("autodeploy-web %s starting", version)

	addr := envDefault("LISTEN_ADDR", ":8080")
	dataDir := envDefault("DATA_DIR", "/data")
	autodeployDir := envDefault("AUTODEPLOY_DIR", "/opt/autodeploy")
	psScript := envDefault("PS_SCRIPT", "autodeploy.ps1")
	concurrency := envInt("WORKER_CONCURRENCY", 1)

	if err := config.EnsureDataLayout(dataDir); err != nil {
		log.Fatalf("data layout: %v", err)
	}

	// Clear any stale per-job staging dirs left over from a crash, then
	// recreate the empty work directory so it's ready for new jobs.
	workDir := filepath.Join(dataDir, "work")
	_ = os.RemoveAll(workDir)
	_ = os.MkdirAll(workDir, 0o755)

	store := config.NewStore(dataDir + "/configs")

	mgr := job.NewManager(job.Options{
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		PSScript:      psScript,
		MaxConcurrent: concurrency,
		KeepCompleted: 50,
	})

	srv := server.New(server.Deps{
		Version:       version,
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		Store:         store,
		JobManager:    mgr,
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
	mgr.Shutdown(ctx)
	log.Println("bye")
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
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
