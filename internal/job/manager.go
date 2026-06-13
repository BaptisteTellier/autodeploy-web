package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/google/uuid"
)

type Options struct {
	DataDir       string // /data
	AutodeployDir string // /opt/autodeploy
	PSScript      string // autodeploy.ps1
	MaxConcurrent int
	KeepCompleted int
	Store         *Store // optional; nil disables persistence
}

// Manager owns the in-memory job registry and the worker pool.
type Manager struct {
	opts  Options
	store *Store // nil when persistence is disabled

	mu   sync.RWMutex
	jobs map[string]*Job

	sem    chan struct{} // semaphore for concurrency cap
	wg     sync.WaitGroup
	stopCh chan struct{}
}

func NewManager(opts Options) *Manager {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.KeepCompleted <= 0 {
		opts.KeepCompleted = 50
	}
	m := &Manager{
		opts:   opts,
		store:  opts.Store,
		jobs:   make(map[string]*Job),
		sem:    make(chan struct{}, opts.MaxConcurrent),
		stopCh: make(chan struct{}),
	}
	if m.store != nil {
		m.loadFromStore()
	}
	return m
}

// loadFromStore hydrates the in-memory job registry from persisted records.
// Running or pending states from a previous crash are normalised to failed.
func (m *Manager) loadFromStore() {
	records, err := m.store.LoadJobs()
	if err != nil {
		log.Printf("job store: load failed: %v", err)
		return
	}
	for _, p := range records {
		st := p.View.State
		if st == StateRunning || st == StatePending {
			// Worker is gone — mark as failed and re-persist the normalised record.
			p.View.State = StateFailed
			if p.View.FinishedAt.IsZero() {
				p.View.FinishedAt = time.Now()
			}
			p.View.ErrorMessage = "interrupted by restart"
			if serr := m.store.SaveJob(p.View, p.ConfigPath); serr != nil {
				log.Printf("job store: normalise %s: %v", p.View.ID, serr)
			}
		}
		m.jobs[p.View.ID] = newPersistedJob(p)
	}
	log.Printf("job store: loaded %d job(s)", len(records))
}

// persist saves the current snapshot of j to the store, logging on error.
// It is a no-op when the store is nil.
func (m *Manager) persist(j *Job) {
	if m.store == nil {
		return
	}
	if err := m.store.SaveJob(j.View(), j.ConfigPath); err != nil {
		log.Printf("job store: save %s: %v", j.ID, err)
	}
}

// Submit persists the config JSON next to the job ID and queues a worker.
func (m *Manager) Submit(c config.Config) (*Job, error) {
	id := uuid.NewString()

	// Persist the config file inside DATA_DIR/configs/.jobs/<id>.json
	// (separate from the user-named presets folder).
	jobsConfigDir := filepath.Join(m.opts.DataDir, "configs", ".jobs")
	if err := os.MkdirAll(jobsConfigDir, 0o755); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(jobsConfigDir, id+".json")

	// Compute output ISO name (same logic as PS1: append _customized if empty).
	out := c.OutputISO
	if out == "" {
		base := c.SourceISO
		ext := filepath.Ext(base)
		out = base[:len(base)-len(ext)] + "_customized" + ext
	}

	// Override SourceISO/OutputISO paths to point to the container volumes,
	// then write the config the PS1 will consume.
	override := c
	// SourceISO and OutputISO in the PS1 are bare filenames (no path) —
	// the PS1 cd's into the ISO directory. We keep the bare filename in the
	// JSON; the runner cd's into /data/iso, output lands there, we move it
	// to /data/output post-run.
	override.OutputISO = out

	b, err := json.MarshalIndent(override, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		return nil, err
	}

	j := newJob(id, c.Hostname, c.ApplianceType, c.SourceISO, out, cfgPath)

	m.mu.Lock()
	m.jobs[id] = j
	m.pruneLocked()
	m.mu.Unlock()

	// Persist the new job record immediately (state=pending).
	m.persist(j)

	m.wg.Add(1)
	go m.runWorker(j)

	return j, nil
}

// pruneLocked drops oldest finished jobs above KeepCompleted.
func (m *Manager) pruneLocked() {
	type finishedJob struct {
		job        *Job
		finishedAt time.Time
	}
	finished := make([]finishedJob, 0)
	for _, j := range m.jobs {
		// Read State/FinishedAt under j.mu — these fields are written by the
		// worker goroutine, which does not hold m.mu.
		st, fa := j.statusSnapshot()
		if st == StateDone || st == StateFailed || st == StateCanceled {
			finished = append(finished, finishedJob{job: j, finishedAt: fa})
		}
	}
	if len(finished) <= m.opts.KeepCompleted {
		return
	}
	sort.Slice(finished, func(i, k int) bool { return finished[i].finishedAt.Before(finished[k].finishedAt) })
	drop := len(finished) - m.opts.KeepCompleted
	for i := 0; i < drop; i++ {
		pruned := finished[i].job
		delete(m.jobs, pruned.ID)
		_ = os.Remove(pruned.ConfigPath)
		if m.store != nil {
			if err := m.store.DeleteJob(pruned.ID); err != nil {
				log.Printf("job store: prune delete %s: %v", pruned.ID, err)
			}
		}
	}
}

func (m *Manager) runWorker(j *Job) {
	defer m.wg.Done()

	// Acquire concurrency slot.
	select {
	case m.sem <- struct{}{}:
	case <-m.stopCh:
		j.markCanceled()
		close(j.done)
		return
	}
	defer func() { <-m.sem }()

	j.markRunning()
	m.persist(j) // state=running

	r := Runner{
		AutodeployDir:  m.opts.AutodeployDir,
		PSScript:       m.opts.PSScript,
		OverrideScript: filepath.Join(m.opts.DataDir, "autodeploy", "autodeploy.ps1"),
		IsoDir:         filepath.Join(m.opts.DataDir, "iso"),
		OutputDir:      filepath.Join(m.opts.DataDir, "output"),
		LicenseDir:     filepath.Join(m.opts.DataDir, "license"),
		ConfDir:        filepath.Join(m.opts.DataDir, "conf"),
		ConfigPath:     j.ConfigPath,
		JobID:          j.ID,
		SourceISO:      j.SourceISO,
		WorkDir:        filepath.Join(m.opts.DataDir, "work"),
		OnLine:         j.AppendLine,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exit, err := r.Run(ctx)
	var finalState State
	var errMsg string
	if err != nil {
		finalState = StateFailed
		errMsg = err.Error()
	} else if exit != 0 {
		finalState = StateFailed
		errMsg = fmt.Sprintf("pwsh exited with code %d", exit)
	} else {
		finalState = StateDone
	}
	j.markResult(finalState, exit, errMsg)
	m.persist(j) // state=done/failed

	// Snapshot the job config JSON into the output folder so it can be
	// reimported from the "Import config into new job" button and used to
	// display human-readable names on the output index page.
	jobOut := filepath.Join(m.opts.DataDir, "output", j.ID)
	_ = os.MkdirAll(jobOut, 0o755)
	_ = copyFile(j.ConfigPath, filepath.Join(jobOut, "job-config.json"))

	j.closeSubs()
	close(j.done)
}

// Get returns a job by ID.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns snapshots of all jobs sorted by creation time (newest first).
func (m *Manager) List() []JobView {
	m.mu.RLock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.RUnlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].CreatedAt.After(jobs[k].CreatedAt) })
	out := make([]JobView, len(jobs))
	for i, j := range jobs {
		out[i] = j.View()
	}
	return out
}

// Delete removes a finished job from the registry (and its config file).
// Generated ISO files are kept on disk under /data/output.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return errors.New("not found")
	}
	// Read State under j.mu — written by the worker goroutine without m.mu.
	if st, _ := j.statusSnapshot(); st == StateRunning || st == StatePending {
		return errors.New("cannot delete a running or pending job")
	}
	delete(m.jobs, id)
	_ = os.Remove(j.ConfigPath)
	if m.store != nil {
		if err := m.store.DeleteJob(id); err != nil {
			log.Printf("job store: delete %s: %v", id, err)
		}
	}
	return nil
}

// Shutdown waits for in-flight jobs to finish or for ctx to expire.
func (m *Manager) Shutdown(ctx context.Context) {
	close(m.stopCh)
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
