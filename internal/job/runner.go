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
	IsoDir        string // /data/iso — where source ISOs live
	OutputDir     string // /data/output  (job subfolder created here)
	LicenseDir    string // /data/license
	ConfDir       string // /data/conf
	JobID         string // per-job output subfolder: OutputDir/JobID/
	SourceISO     string // bare filename of the source ISO (e.g. "veeam.iso")
	WorkDir       string // base dir for per-job staging dirs, e.g. /data/work

	// OverrideScript, if non-empty and the file exists, is used instead of
	// AutodeployDir/PSScript. Populated from /data/autodeploy/autodeploy.ps1
	// when the user triggers a runtime update from the admin page.
	OverrideScript string

	// ConfigPath is the absolute path to the JSON the PS1 reads.
	ConfigPath string

	// OnLine receives each captured stdout/stderr line, scrubbed.
	OnLine func(string)
}

// Run launches pwsh and blocks until completion. Returns the process exit
// code (or -1 on spawn failure) and any I/O error.
// Each job gets its own fresh staging dir under WorkDir/<jobID>; the whole
// dir is removed via defer after the run, giving clean per-job isolation.
func (r *Runner) Run(ctx context.Context) (int, error) {
	if r.OnLine == nil {
		r.OnLine = func(string) {}
	}

	scriptPath := filepath.Join(r.AutodeployDir, r.PSScript)
	// Prefer runtime override when it exists.
	if r.OverrideScript != "" {
		if _, err := os.Stat(r.OverrideScript); err == nil {
			scriptPath = r.OverrideScript
		}
	}
	if _, err := os.Stat(scriptPath); err != nil {
		return -1, fmt.Errorf("autodeploy.ps1 not found at %s: %w", scriptPath, err)
	}
	if _, err := os.Stat(r.ConfigPath); err != nil {
		return -1, fmt.Errorf("config file missing: %w", err)
	}

	// Per-job staging directory under /data/work/<jobID> — same filesystem as
	// /data/output so that moving large ISOs is an instant rename, not a copy.
	stageDir := filepath.Join(r.WorkDir, r.JobID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return -1, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Symlink the source ISO into the staging dir so the PS1 finds it by bare name.
	// Base the name so a crafted SourceISO can't point the symlink outside IsoDir.
	if r.SourceISO != "" {
		iso := filepath.Base(r.SourceISO)
		src := filepath.Join(r.IsoDir, iso)
		dst := filepath.Join(stageDir, iso)
		if err := os.Symlink(src, dst); err != nil {
			r.OnLine(fmt.Sprintf("[warn] symlink source ISO: %v", err))
		}
	}

	// Symlink companion folders the PS1 may consult.
	for _, m := range []struct{ Name, Source string }{
		{"license", r.LicenseDir},
		{"conf", r.ConfDir},
	} {
		if m.Source == "" {
			continue
		}
		_ = os.Symlink(m.Source, filepath.Join(stageDir, m.Name))
	}

	// Stage the PS1 into cwd so the PS1 can reference files with bare names.
	stagedScript := filepath.Join(stageDir, r.PSScript)
	if err := copyFile(scriptPath, stagedScript); err != nil {
		return -1, fmt.Errorf("stage script: %w", err)
	}

	// Stage the config under a hidden name to avoid collisions.
	stagedConfig := filepath.Join(stageDir, ".job-"+filepath.Base(r.ConfigPath))
	if err := copyFile(r.ConfigPath, stagedConfig); err != nil {
		return -1, fmt.Errorf("stage config: %w", err)
	}

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

	// Move all regular files in the staging dir (excluding staged inputs) to
	// OutputDir/JobID/. defer os.RemoveAll(stageDir) cleans the rest.
	if r.JobID != "" {
		r.collectOutputs(stageDir, skipSet)
	}

	return exit, err
}

// collectOutputs moves every regular file in stageDir (not in skip, not a
// symlink, not a directory) into OutputDir/JobID/. Because the staging dir is
// per-job and freshly created, every regular file that wasn't staged by us IS
// a job output. defer os.RemoveAll(stageDir) in the caller handles cleanup.
func (r *Runner) collectOutputs(stageDir string, skip map[string]bool) {
	jobOut := filepath.Join(r.OutputDir, r.JobID)
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		r.OnLine(fmt.Sprintf("[output-error] mkdir %s: %v", jobOut, err))
		return
	}

	entries, err := os.ReadDir(stageDir)
	if err != nil {
		r.OnLine(fmt.Sprintf("[output-error] readdir %s: %v", stageDir, err))
		return
	}
	for _, e := range entries {
		name := e.Name()
		if skip[name] {
			continue
		}
		// Skip symlinks and directories.
		linfo, lerr := os.Lstat(filepath.Join(stageDir, name))
		if lerr != nil || linfo.Mode()&os.ModeSymlink != 0 || linfo.IsDir() {
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
