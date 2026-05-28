package job

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Runner executes one invocation of autodeploy.ps1.
type Runner struct {
	AutodeployDir string // where the PS1 lives, e.g. /opt/autodeploy
	PSScript      string // autodeploy.ps1
	IsoDir        string // /data/iso (cwd for the PS1)
	OutputDir     string // /data/output  (job subfolder created here)
	LicenseDir    string // /data/license
	ConfDir       string // /data/conf
	JobID         string // per-job output subfolder: OutputDir/JobID/

	// ConfigPath is the absolute path to the JSON the PS1 reads.
	ConfigPath string

	// OnLine receives each captured stdout/stderr line, scrubbed.
	OnLine func(string)
}

// Run launches pwsh and blocks until completion. Returns the process exit
// code (or -1 on spawn failure) and any I/O error.
// All files created or modified in IsoDir during the run are moved to
// OutputDir/JobID/ afterwards, giving clean per-job isolation.
func (r *Runner) Run(ctx context.Context) (int, error) {
	if r.OnLine == nil {
		r.OnLine = func(string) {}
	}

	scriptPath := filepath.Join(r.AutodeployDir, r.PSScript)
	if _, err := os.Stat(scriptPath); err != nil {
		return -1, fmt.Errorf("autodeploy.ps1 not found at %s: %w", scriptPath, err)
	}
	if _, err := os.Stat(r.ConfigPath); err != nil {
		return -1, fmt.Errorf("config file missing: %w", err)
	}

	stageDir := r.IsoDir

	// Symlink companion folders the PS1 may consult.
	for _, m := range []struct{ Name, Source string }{
		{"license", r.LicenseDir},
		{"conf", r.ConfDir},
	} {
		dst := filepath.Join(stageDir, m.Name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if m.Source == "" {
			continue
		}
		_ = os.Symlink(m.Source, dst)
	}

	// Snapshot BEFORE staging anything — used later to detect new/changed files.
	beforeSnap := snapshotDir(stageDir)

	// Stage the PS1 into cwd so the PS1 can reference files with bare names.
	stagedScript := filepath.Join(stageDir, r.PSScript)
	if err := copyFile(scriptPath, stagedScript); err != nil {
		return -1, fmt.Errorf("stage script: %w", err)
	}
	defer os.Remove(stagedScript)

	// Stage the config under a hidden name to avoid collisions.
	stagedConfig := filepath.Join(stageDir, ".job-"+filepath.Base(r.ConfigPath))
	if err := copyFile(r.ConfigPath, stagedConfig); err != nil {
		return -1, fmt.Errorf("stage config: %w", err)
	}
	defer os.Remove(stagedConfig)

	// These ephemeral files must not be collected as job outputs.
	skipSet := map[string]bool{
		filepath.Base(stagedScript): true,
		filepath.Base(stagedConfig): true,
	}

	args := []string{
		"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", stagedScript,
		"-ConfigFile", filepath.Base(stagedConfig),
	}

	cmd := exec.CommandContext(ctx, "pwsh", args...)
	cmd.Dir = stageDir
	cmd.Env = append(os.Environ(), "POWERSHELL_TELEMETRY_OPTOUT=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}

	r.OnLine(fmt.Sprintf("[%s] $ pwsh %s", time.Now().Format(time.RFC3339), strings.Join(args, " ")))
	r.OnLine(fmt.Sprintf("[%s] cwd: %s", time.Now().Format(time.RFC3339), stageDir))

	if err := cmd.Start(); err != nil {
		return -1, err
	}

	go r.consume(stdout)
	go r.consume(stderr)

	err = cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil // exit code reported separately
		} else {
			exit = -1
		}
	}

	// Move all newly created / modified files to OutputDir/JobID/ .
	// This includes the customised ISO, kickstart configs, logs — everything.
	if r.JobID != "" {
		r.collectOutputs(stageDir, beforeSnap, skipSet)
	}

	return exit, err
}

// collectOutputs moves every file that appeared or changed in stageDir since
// beforeSnap into OutputDir/JobID/. Symlinks and directories are ignored.
func (r *Runner) collectOutputs(stageDir string, before map[string]time.Time, skip map[string]bool) {
	jobOut := filepath.Join(r.OutputDir, r.JobID)
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		r.OnLine(fmt.Sprintf("[output-error] mkdir %s: %v", jobOut, err))
		return
	}

	after := snapshotDir(stageDir)
	for name, modTime := range after {
		if skip[name] {
			continue
		}
		// Skip symlinks and directories.
		linfo, _ := os.Lstat(filepath.Join(stageDir, name))
		if linfo == nil || linfo.Mode()&os.ModeSymlink != 0 || linfo.IsDir() {
			continue
		}
		// Only collect new or modified files.
		prevTime, existed := before[name]
		if existed && !modTime.After(prevTime) {
			continue
		}

		src := filepath.Join(stageDir, name)
		dst := filepath.Join(jobOut, name)
		if mvErr := os.Rename(src, dst); mvErr != nil {
			// Cross-device fallback (different mount points).
			if cpErr := copyFile(src, dst); cpErr == nil {
				_ = os.Remove(src)
				r.OnLine(fmt.Sprintf("[output] %s", name))
			} else {
				r.OnLine(fmt.Sprintf("[output-error] %s: %v", name, cpErr))
			}
		} else {
			r.OnLine(fmt.Sprintf("[output] %s", name))
		}
	}
}

// snapshotDir returns a map of filename → modtime for non-directory entries.
func snapshotDir(dir string) map[string]time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return make(map[string]time.Time)
	}
	snap := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			snap[e.Name()] = info.ModTime()
		}
	}
	return snap
}

// consume reads a pipe line by line, scrubs secrets, forwards to OnLine.
func (r *Runner) consume(p io.Reader) {
	scanner := bufio.NewScanner(p)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		r.OnLine(scrub(scanner.Text()))
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// scrub masks anything that looks like a Veeam password or MFA secret.
var (
	rePwd    = regexp.MustCompile(`(?i)(password\s*[:=]\s*)("?[^"\s,;]+)`)
	reMfa    = regexp.MustCompile(`(?i)(mfasecretkey\s*[:=]\s*)("?[A-Z2-7]{16,32})`)
	reToken  = regexp.MustCompile(`(?i)(recoverytoken\s*[:=]\s*)("?[0-9a-f-]{36})`)
	reVCSPpw = regexp.MustCompile(`(?i)(VCSPPassword\s*[:=]\s*)("?[^"\s,;]+)`)
)

func scrub(s string) string {
	s = rePwd.ReplaceAllString(s, `${1}***`)
	s = reMfa.ReplaceAllString(s, `${1}***`)
	s = reToken.ReplaceAllString(s, `${1}***`)
	s = reVCSPpw.ReplaceAllString(s, `${1}***`)
	return s
}
