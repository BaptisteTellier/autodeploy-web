# Deployment

## Docker Compose (recommended)

```yaml
services:
  autodeploy-web:
    image: ghcr.io/baptistetellier/autodeploy-web:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./data/iso:/data/iso
      - ./data/output:/data/output
      - ./data/license:/data/license
      - ./data/conf:/data/conf
      - ./data/configs:/data/configs
```

## Plain Docker

```bash
docker run -d --name autodeploy-web \
  -p 8080:8080 \
  -v $(pwd)/data/iso:/data/iso \
  -v $(pwd)/data/output:/data/output \
  -v $(pwd)/data/license:/data/license \
  -v $(pwd)/data/conf:/data/conf \
  -v $(pwd)/data/configs:/data/configs \
  ghcr.io/baptistetellier/autodeploy-web:latest
```

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
2. The PR merges to `main` → the `build-image` workflow rebuilds the
   image and tags `latest` + a semver tag matching the upstream version.

## Backups

What to back up:
- `data/configs/` — your saved JSON presets.
- `data/output/` — generated ISOs (only if you don't want to regenerate them).

Everything else (`data/iso/`, `data/license/`) is user-provided and can
be re-uploaded.
