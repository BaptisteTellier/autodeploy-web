# Deployment

## Docker Compose (recommended)

```yaml
services:
  autodeploy-web:
    image: ghcr.io/baptistetellier/autodeploy-web:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    # Mount the WHOLE /data directory as one volume. The app keeps its
    # credential file (auth.json), job/deployment history (jobs.db,
    # deployments.db) and settings.json directly under /data — mounting only
    # the sub-folders would lose your admin password and history on every
    # container recreate.
    volumes:
      - ./data:/data
    environment:
      # Auth is ON by default; open the UI once to set the password at /setup.
      # - AUTH_DISABLED=true              # only behind your own proxy / localhost
      # - ADMIN_PASSWORD_HASH=<bcrypt>    # pre-provision (skips /setup)
      # - ADMIN_PASSWORD=<plaintext>      # pre-provision (hashed at startup)
      - TZ=Europe/Paris
```

## Plain Docker

```bash
docker run -d --name autodeploy-web \
  -p 8080:8080 \
  -v $(pwd)/data:/data \
  ghcr.io/baptistetellier/autodeploy-web:latest
```

> **Persistence matters:** `-v $(pwd)/data:/data` maps the entire data
> directory. If you prefer to mount individual sub-folders, you **must** also
> persist `auth.json`, `jobs.db`, `deployments.db` and `settings.json` (all at
> the `/data` root) or you'll be sent back to `/setup` and lose history after
> any recreate.

## Behind a reverse proxy (HTTPS)

Caddy snippet:

```caddyfile
veeam-builder.example.lan {
    reverse_proxy localhost:8080 {
        flush_interval -1   # required for SSE
    }
}
```

Traefik labels:

```yaml
labels:
  - "traefik.enable=true"
  - "traefik.http.routers.adw.rule=Host(`veeam-builder.example.lan`)"
  - "traefik.http.services.adw.loadbalancer.server.port=8080"
  # SSE needs no buffering:
  - "traefik.http.middlewares.no-buffer.buffering.maxResponseBodyBytes=0"
```

## Sizing

- **Disk**: source ISO + ~2× space during build (working copy + output). For a 20 GB Veeam ISO budget ~60 GB free under `data/`.
- **RAM**: 1–2 GB is plenty. xorriso is I/O bound, not memory bound.
- **CPU**: 2 vCPU is fine for one build at a time; raise `WORKER_CONCURRENCY` only if you can also raise `data/` IOPS.

## Updating

```bash
docker compose pull
docker compose up -d
```

A new image is published automatically when:
1. A new release of `autodeploy.ps1` is tagged upstream → the
   `release-watcher` workflow opens a PR here.
2. The change lands on `master` → the `build-image` workflow rebuilds the
   image and tags `latest` + a semver tag matching the upstream version.

> Not running Docker? See [native-vm-install.md](native-vm-install.md) to build
> and run the binary directly on a Linux or Windows VM.

## Backups

What to back up (all under `data/`):
- `auth.json` — admin password hash + session secret. Losing it means resetting the password at `/setup`.
- `configs/`, `deploy-presets/`, `craft-presets/` — your saved presets/topologies.
- `settings.json` — app settings (history limit, language).
- `jobs.db`, `deployments.db` — build & deployment history (optional; safe to lose).
- `output/` — generated ISOs (only if you don't want to regenerate them).

Everything else (`iso/`, `license/`, `conf/`) is user-provided and can
be re-uploaded. The simplest approach is to back up the whole `data/` directory.
