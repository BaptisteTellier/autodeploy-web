package deploy

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
)

// JobISOBuilder implements ISOBuilder on top of the existing job.Manager: it
// submits a build job, streams its log lines, waits for completion and returns
// the path to the produced ISO under <dataDir>/output/<jobID>/<OutputISO>.
type JobISOBuilder struct {
	Mgr     *job.Manager
	DataDir string
}

// NewJobISOBuilder wires an ISOBuilder to the job manager.
func NewJobISOBuilder(mgr *job.Manager, dataDir string) *JobISOBuilder {
	return &JobISOBuilder{Mgr: mgr, DataDir: dataDir}
}

// BuildISO submits cfg as a build job, forwards its output to onLine, waits for
// it to finish and returns the local path of the generated ISO.
func (b *JobISOBuilder) BuildISO(ctx context.Context, cfg config.Config, onLine func(string)) (string, error) {
	j, err := b.Mgr.Submit(cfg)
	if err != nil {
		return "", err
	}

	_, ch, cancel := j.Subscribe(256)
	defer cancel()
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for line := range ch {
			onLine(line)
		}
	}()

	select {
	case <-j.Done():
	case <-ctx.Done():
		return "", ctx.Err()
	}
	<-streamDone // drain remaining lines

	v := j.View()
	if v.State != job.StateDone {
		if v.ErrorMessage != "" {
			return "", fmt.Errorf("ISO build failed: %s", v.ErrorMessage)
		}
		return "", fmt.Errorf("ISO build did not complete (state %s)", v.State)
	}
	return filepath.Join(b.DataDir, "output", v.ID, v.OutputISO), nil
}
