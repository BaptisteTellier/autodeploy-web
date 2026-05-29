package job

import (
	"sync"
	"time"
)

type State string

const (
	StatePending  State = "pending"
	StateRunning  State = "running"
	StateDone     State = "done"
	StateFailed   State = "failed"
	StateCanceled State = "canceled"
)

// Job represents one PS1 invocation.
type Job struct {
	ID           string    `json:"id"`
	State        State     `json:"state"`
	Hostname     string    `json:"hostname"`  // from config, for display
	Appliance    string    `json:"appliance"` // VSA/VIA/...
	SourceISO    string    `json:"source_iso"`
	OutputISO    string    `json:"output_iso"`
	ConfigPath   string    `json:"config_path"` // path inside /data/configs/<id>.json
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	ExitCode     int       `json:"exit_code"`
	ErrorMessage string    `json:"error,omitempty"`

	mu    sync.Mutex
	lines []string // captured stdout/stderr lines (ring-buffered)
	subs  []chan string
	done  chan struct{}
}

const maxBufferedLines = 5000

func newJob(id, hostname, appliance, src, out, cfgPath string) *Job {
	return &Job{
		ID:         id,
		State:      StatePending,
		Hostname:   hostname,
		Appliance:  appliance,
		SourceISO:  src,
		OutputISO:  out,
		ConfigPath: cfgPath,
		CreatedAt:  time.Now(),
		lines:      make([]string, 0, 256),
		done:       make(chan struct{}),
	}
}

// AppendLine stores one log line and fans it out to live subscribers.
func (j *Job) AppendLine(line string) {
	j.mu.Lock()
	if len(j.lines) >= maxBufferedLines {
		j.lines = j.lines[1:]
	}
	j.lines = append(j.lines, line)
	subs := j.subs
	j.mu.Unlock()

	for _, ch := range subs {
		// Non-blocking send — drop on slow subscribers rather than stalling the runner.
		select {
		case ch <- line:
		default:
		}
	}
}

// Snapshot returns the buffered lines (copy) for late subscribers.
func (j *Job) Snapshot() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, len(j.lines))
	copy(out, j.lines)
	return out
}

// Subscribe registers a live channel and returns the buffered history plus
// a cancel function. The channel is closed when the job finishes.
func (j *Job) Subscribe(buf int) (history []string, ch chan string, cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if buf <= 0 {
		buf = 64
	}
	c := make(chan string, buf)
	j.subs = append(j.subs, c)
	hist := make([]string, len(j.lines))
	copy(hist, j.lines)

	cancel = func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		for i, x := range j.subs {
			if x == c {
				j.subs = append(j.subs[:i], j.subs[i+1:]...)
				close(c)
				return
			}
		}
	}
	return hist, c, cancel
}

func (j *Job) closeSubs() {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.subs {
		close(c)
	}
	j.subs = nil
}

func (j *Job) Done() <-chan struct{} { return j.done }

// markRunning sets State=StateRunning and StartedAt=now under the mutex.
func (j *Job) markRunning() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = StateRunning
	j.StartedAt = time.Now()
}

// markResult sets FinishedAt, ExitCode, State, and ErrorMessage under the mutex.
func (j *Job) markResult(state State, exit int, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.FinishedAt = time.Now()
	j.ExitCode = exit
	j.State = state
	j.ErrorMessage = errMsg
}

// markCanceled sets State=StateCanceled and FinishedAt=now under the mutex.
func (j *Job) markCanceled() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = StateCanceled
	j.FinishedAt = time.Now()
}

// StateString returns the current state as a string, safely under the mutex.
func (j *Job) StateString() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return string(j.State)
}

// JobView is a race-free snapshot of a Job's public fields, safe to pass to
// HTTP handlers and templates without holding the job mutex.
type JobView struct {
	ID           string    `json:"id"`
	State        State     `json:"state"`
	Hostname     string    `json:"hostname"`
	Appliance    string    `json:"appliance"`
	SourceISO    string    `json:"source_iso"`
	OutputISO    string    `json:"output_iso"`
	ConfigPath   string    `json:"config_path"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	ExitCode     int       `json:"exit_code"`
	ErrorMessage string    `json:"error,omitempty"`
}

// View returns a race-free snapshot of the job's public fields.
func (j *Job) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobView{
		ID:           j.ID,
		State:        j.State,
		Hostname:     j.Hostname,
		Appliance:    j.Appliance,
		SourceISO:    j.SourceISO,
		OutputISO:    j.OutputISO,
		ConfigPath:   j.ConfigPath,
		CreatedAt:    j.CreatedAt,
		StartedAt:    j.StartedAt,
		FinishedAt:   j.FinishedAt,
		ExitCode:     j.ExitCode,
		ErrorMessage: j.ErrorMessage,
	}
}
