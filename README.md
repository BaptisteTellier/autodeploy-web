# autodeploy-web

> 🐳 Containerised web UI around [BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy) — generate customised Veeam Software Appliance ISOs from a browser, without PowerShell or WSL on the host.

[![CI](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml/badge.svg)](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-autodeploy--web-blue?logo=docker)](https://github.com/BaptisteTellier/autodeploy-web/pkgs/container/autodeploy-web)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## What it does

`autodeploy-web` packages the upstream PowerShell script `autodeploy.ps1` inside a Linux container, exposes a small web UI to fill the configuration form in a browser, and runs the PS1 to produce a customised Veeam ISO. The two projects coexist:

- **`autodeploy`** — authoritative source of the PS1 logic, keeps shipping releases.
- **`autodeploy-web`** — packaging + UI layer. Each release of `autodeploy` is automatically picked up via a release-watcher workflow.

## Why use it

- ✅ **No PowerShell / WSL on your host** — Docker (or Podman) is the only dependency.
- ✅ **Web form** instead of editing JSON by hand.
- ✅ **Live build log** (SSE) — see `xorriso` and the kickstart generation in real time.
- ✅ **Multi-host friendly** — runs anywhere you have a container runtime.
- ✅ **JSON round-trip 100% compatible** with `autodeploy.ps1` — export the form to JSON and you can still run it locally with the PS1.

## Quick start

```bash
# 1. Get the compose file
curl -O https://raw.githubusercontent.com/BaptisteTellier/autodeploy-web/main/docker-compose.yml

# 2. Drop your source ISO(s) into ./data/iso/
mkdir -p data/iso data/output data/license data/conf data/configs
cp /path/to/VeeamSoftwareAppliance_*.iso ./data/iso/

# 3. (optional) drop a Veeam .lic into ./data/license/
# 4. (optional) drop unattended.xml + veeam_addsoconfpw.sh + conftoresto.bco into ./data/conf/

# 5. Start
docker compose up -d

# 6. Open http://localhost:8080
```

The container runs the latest published `autodeploy.ps1`. Pin a specific upstream version by rebuilding:

```bash
docker build --build-arg AUTODEPLOY_VERSION=v2.8.0 -t autodeploy-web:custom .
```

## How it works

```
┌──────────────────────────────────────────────────────────────┐
│   container : ghcr.io/baptistetellier/autodeploy-web:latest  │
│                                                              │
│   ┌──────────┐  HTTP   ┌───────────┐  spawn   ┌────────────┐ │
│   │ browser  │───────▶ │ Go binary │────────▶ │  pwsh +    │ │
│   │  (form)  │  :8080  │  +HTMX UI │  exec    │  autodep   │ │
│   │          │ ◀───SSE │           │ ◀─stdout │  loy.ps1   │ │
│   └──────────┘         └───────────┘          └────────────┘ │
│                              │                       │       │
│                              ▼                       ▼       │
│                       /data/configs/         xorriso (native)│
│                       /data/iso/         ──▶ /data/output/   │
└──────────────────────────────────────────────────────────────┘
```

The Go binary:
1. Receives the form POST.
2. Validates it (mirror of the PS1 rules).
3. Writes a JSON file in `/data/configs/.jobs/<id>.json`.
4. Spawns `pwsh autodeploy.ps1 -ConfigFile <id>.json` in `/data/iso`.
5. Streams stdout/stderr over SSE to the browser.
6. Moves the resulting ISO to `/data/output/<name>.iso` and exposes a download link.

The PS1 itself is **not modified** — it runs identically to how it would on Windows + WSL. A tiny `/usr/local/bin/wsl` shim translates the PS1's `wsl xorriso ...` calls into native `xorriso` calls.

## Volumes

| Path inside container | Purpose |
|---|---|
| `/data/iso` | Source ISOs the user drops in (15–20 GB each) |
| `/data/output` | Customised ISOs produced by jobs |
| `/data/license` | Veeam `.lic` files referenced by `LicenseVBRTune` |
| `/data/conf` | `unattended.xml`, `veeam_addsoconfpw.sh`, `conftoresto.bco` |
| `/data/configs` | Saved JSON presets (selectable from the form dropdown) |

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `DATA_DIR` | `/data` | Volume root |
| `AUTODEPLOY_DIR` | `/opt/autodeploy` | Where the PS1 is staged in the image |
| `PS_SCRIPT` | `autodeploy.ps1` | Script filename |
| `WORKER_CONCURRENCY` | `1` | Parallel ISO builds (raise carefully — disk-bound) |

## Limitations

- 🚫 **No authentication** — designed for LAN/local usage. Put a reverse proxy (Caddy, Traefik) in front if you expose it publicly.
- 🚫 **No job persistence** — restart loses the in-memory job list (running jobs are killed). Saved presets on disk survive.
- 🚫 **One build at a time by default** — to avoid xorriso disk contention.
- The PS1 contains hard-coded behaviours (NTP failure → customisation failure, etc.) that the UI surfaces but does not override.

## Local development

```bash
make vendor        # fetches htmx / alpine / tailwind
make build         # builds bin/autodeploy-web
make test          # runs go test
make image         # builds the Docker image locally
make dev-up        # docker compose up --build
```

## Migrating from autodeploy (PS1)

Already have JSON configs from the PS1? Just upload them via **⬆️ Import JSON** in the UI — the schema is identical. See [docs/migration-from-ps1.md](docs/migration-from-ps1.md).

## Acknowledgements

- The whole heavy lifting — kickstart generation, GRUB injection, MFA wiring, VCSP, license install — is done by **[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy)**. This project is just packaging.
- xorriso (libisoburn).
- Microsoft PowerShell on Linux.
- HTMX + Alpine.js + Tailwind.

## License

MIT — see [LICENSE](LICENSE).

— *Made by Baptiste TELLIER for the Veeam community.*
