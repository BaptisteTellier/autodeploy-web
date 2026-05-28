# Architecture

`autodeploy-web` is a thin web layer around the existing PowerShell tool
[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy).
It does **not** re-implement any of the kickstart / GRUB / xorriso logic —
it generates a JSON file and runs the PS1 against it.

## Container layout

```
mcr.microsoft.com/powershell:7.4-debian-12-slim
  ├── /usr/local/bin/autodeploy-web      Go binary (HTTP server + worker)
  ├── /usr/local/bin/wsl                 shim that forwards `wsl <cmd>` → `<cmd>`
  ├── /opt/autodeploy/                   pinned clone of upstream repo
  │     ├── autodeploy.ps1
  │     ├── conf/
  │     └── powershell/
  └── /data/                             volumes
        ├── iso/        source ISOs
        ├── output/     generated ISOs
        ├── license/    .lic files
        ├── conf/       unattended.xml, .bco, ...
        └── configs/    saved presets + .jobs/ working dir
```

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
  │  jobs[id] = newJob;  config dumped to /data/configs/.jobs/<id>.json
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
- In-memory job registry only: a restart resets state. Saved presets on
  disk survive.

## Why we don't port the PS1 logic to Go

1. The PS1 is the authoritative source of behaviour. Re-implementing it
   means two sources of truth that drift apart.
2. Every release of `autodeploy.ps1` is automatically wrapped by the
   release-watcher workflow — zero porting effort.
3. The PS1 already deals with Veeam-specific quirks (build differences,
   GRUB regex, init wizard timing) that we'd otherwise reverse-engineer.

## Security notes

- No authentication. Intended for LAN use behind a reverse proxy if
  exposed.
- Passwords / MFA secrets pass through the PS1 unmodified. The Go runner
  scrubs them from captured log lines via regex before they reach SSE or
  the in-memory buffer.
- Saved presets are unencrypted JSON on disk — treat `/data/configs` as
  sensitive.
- The container needs no special privileges. xorriso runs in user space
  on the mounted volumes.
