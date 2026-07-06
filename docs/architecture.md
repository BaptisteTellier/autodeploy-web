# Architecture

`autodeploy-web` is a thin web layer around the existing PowerShell tool
[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy).
It does **not** re-implement any of the kickstart / GRUB / xorriso logic —
it generates a JSON file and runs the PS1 against it.

## Container layout

```
debian:bookworm-slim  (+ PowerShell 7.4 from the official GitHub tarball)
  ├── /usr/local/bin/autodeploy-web      Go binary (HTTP server + worker)
  ├── /usr/bin/pwsh                      PowerShell 7 (symlink to /opt/microsoft/powershell/7)
  ├── /usr/local/bin/wsl                 shim that forwards `wsl <cmd>` → `<cmd>`
  ├── /usr/local/bin/cmd                 shim that forwards `cmd /c <cmd>` → bash
  ├── /opt/autodeploy/                   pinned clone of upstream repo
  │     ├── autodeploy.ps1
  │     ├── conf/
  │     └── powershell/
  └── /data/                             volume(s) — mount the whole /data
        ├── iso/            source ISOs
        ├── output/         generated ISOs + per-job .cfg/kickstart files
        ├── license/        .lic files
        ├── conf/           unattended.xml, .bco, ...
        ├── configs/        saved ISO-form presets
        ├── deploy-presets/ saved Deploy topologies
        ├── craft-presets/  saved Craft-API topologies
        ├── work/           transient build staging (cleared on start)
        ├── jobs.db         ISO-build job history (SQLite)
        ├── deployments.db  deployment history (SQLite)
        ├── settings.json   app settings (history limit, language, ...)
        └── auth.json       bcrypt password hash + session secret (mode 0600)
```

> We deliberately do **not** use `mcr.microsoft.com/powershell`: MCR's
> anonymous-pull token endpoint rate-limits CI runners and broke builds. The
> runtime is plain Debian with the PowerShell release tarball unpacked on top.

## Request flow (POST /jobs)

```
browser POST /jobs (form)
       │
       ▼
handlers.handleCreateJob
  │  configFromForm  ────▶ config.Config (Go struct)
  │  config.Validate ────▶ ValidationErrors? → re-render form
  │  jobManager.Submit
  │         │
  │         ▼
  │  job persisted to jobs.db;  config written under /data/work/<id>/
  │         │
  │         ▼  (goroutine, semaphore-gated)
  │  runner.Run
  │     ├── stage autodeploy.ps1 + companion folders into /data/iso/
  │     ├── exec.CommandContext("pwsh", "-File", ..., "-ConfigFile", ...)
  │     ├── stream stdout/stderr → job.AppendLine (scrubbed)
  │     └── move output ISO to /data/output/
       │
       ▼
303 redirect → /jobs/<id>
       │
       ▼
browser opens SSE /jobs/<id>/stream → live log
browser fetches /jobs/<id>/download → ISO (http.ServeContent, sendfile)
```

## Concurrency model

- One job runner per goroutine, gated by a buffered channel sized at
  `WORKER_CONCURRENCY` (default 1).
- Subscribers (SSE clients) receive lines via non-blocking sends — slow
  consumers drop lines rather than stalling the runner.
- **Job and deployment history is persisted to SQLite** (`jobs.db`,
  `deployments.db`, via the pure-Go `modernc.org/sqlite` driver — no cgo). The
  history survives restarts and is pruned to the configured limit. Live log
  buffers for an *in-flight* job are in memory, so a restart mid-build ends that
  build; completed jobs and their output files remain.
- Saved presets and settings on disk survive.

## Why we don't port the PS1 logic to Go

1. The PS1 is the authoritative source of behaviour. Re-implementing it
   means two sources of truth that drift apart.
2. Every release of `autodeploy.ps1` is automatically wrapped by the
   release-watcher workflow — zero porting effort.
3. The PS1 already deals with Veeam-specific quirks (build differences,
   GRUB regex, init wizard timing) that we'd otherwise reverse-engineer.

## Security notes

- **Single-admin authentication is on by default.** A bcrypt-hashed password
  and a random HMAC secret live in `/data/auth.json` (mode 0600); sessions are
  stateless signed cookies (`SameSite=Strict`, 30-day absolute lifetime).
  State-changing requests are protected by a same-origin check + session-derived
  CSRF token. Opt out with `AUTH_DISABLED=true` only behind your own proxy /
  localhost. See the README "Authentication" section.
- **One route is intentionally unauthenticated:** `GET /ks/<output-id>/<file>.cfg`.
  A netbooting appliance (Anaconda `inst.ks=`) cannot sign in, so this capability
  URL serves *only* `.cfg` files under the unguessable job UUID and refuses the
  credential-bearing config snapshot. The authenticated `/media/output/.../content`
  route is for humans; `/ks/` is for the installer.
- Passwords / MFA secrets pass through the PS1 unmodified. The Go runner
  scrubs them from captured log lines via regex before they reach SSE or
  the log buffer.
- Values interpolated into PowerShell single-quoted literals are escaped
  (`psLit`) to prevent command injection via config fields.
- Saved presets and job configs are unencrypted JSON on disk — treat `/data`
  as sensitive (it holds credentials baked for the appliance).
- The container needs no special privileges. xorriso runs in user space
  on the mounted volumes.
