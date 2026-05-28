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
	OutputDir     string // /data/output
	LicenseDir    string // /data/license
	ConfDir       string // /data/conf

	// ConfigPath is the absolute path to the JSON the PS1 reads.
	ConfigPath string

	// OnLine receives each captured stdout/stderr line, scrubbed.
	OnLine func(string)
}

// Run launches pwsh and blocks until completion. Returns the process exit
// code (or -1 on spawn failure) and any I/O error.
func (r *Runner) Run(ctx context.Context, outputISO string) (int, error) {
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

	// The PS1 expects to run "in the same directory as the source ISO".
	// We copy the script & companion folders into IsoDir for the duration of
	// the run, so xorriso (and any side-files like /license, /conf,
	// /offline_repo) are resolved relative to where the user mounted them.
	stageDir := r.IsoDir

	// Symlink companion folders the PS1 may consult.
	for _, mapping := range []struct{ Name, Source string }{
		{"license", r.LicenseDir},
		{"conf", r.ConfDir},
	} {
		dst := filepath.Join(stageDir, mapping.Name)
		// Best-effort: ignore if already linked or directory exists.
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if mapping.Source == "" {
			continue
		}
		_ = os.Symlink(mapping.Source, dst)
	}

	// Copy the PS1 (and its powershell/conf helper folders if needed) into
	// stage. We use bind-friendly copy because xorriso writes into cwd.
	stagedScript := filepath.Join(stageDir, r.PSScript)
	if err := copyFile(scriptPath, stagedScript); err != nil {
		return -1, fmt.Errorf("stage script: %w", err)
	}
	defer os.Remove(stagedScript)

	// Stage the config under the cwd so paths in the PS1 stay simple.
	stagedConfig := filepath.Join(stageDir, ".job-"+filepath.Base(r.ConfigPath))
	if err := copyFile(r.ConfigPath, stagedConfig); err != nil {
		return -1, fmt.Errorf("stage config: %w", err)
	}
	defer os.Remove(stagedConfig)

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

	go r.consume("stdout", stdout)
	go r.consume("stderr", stderr)

	err = cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil // exit code != 0 is reported separately, not as Go error
		} else {
			exit = -1
		}
	}

	// Move the customised ISO from IsoDir to OutputDir for clean separation.
	src := filepath.Join(stageDir, outputISO)
	dst := filepath.Join(r.OutputDir, outputISO)
	if _, statErr := os.Stat(src); statErr == nil {
		if mvErr := os.Rename(src, dst); mvErr != nil {
			// Fallback: cross-device rename → copy + remove.
			if cpErr := copyFile(src, dst); cpErr == nil {
				_ = os.Remove(src)
			} else {
				r.OnLine(fmt.Sprintf("[move-error] %v", mvErr))
			}
		}
		r.OnLine(fmt.Sprintf("[output] %s", dst))
	}

	return exit, err
}

// consume reads a pipe line by line, scrubs secrets, forwards to OnLine.
func (r *Runner) consume(tag string, p io.Reader) {
	scanner := bufio.NewScanner(p)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scrub(scanner.Text())
		r.OnLine(line)
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

// scrub masks anything that looks like a Veeam password or MFA secret in PS1
// log output. The PS1 may emit them when echoing the loaded config back.
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
