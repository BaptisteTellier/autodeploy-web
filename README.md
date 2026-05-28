# autodeploy-web

> 🐳 Containerised web UI around [BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy) — generate customised Veeam Software Appliance ISOs from a browser, without PowerShell or WSL on the host.

[![CI](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml/badge.svg)](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-autodeploy--web-blue?logo=docker)](https://github.com/BaptisteTellier/autodeploy-web/pkgs/container/autodeploy-web)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

---

## Quick start — only requirement: Docker

```bash
# 1. Download the compose file
curl -O https://raw.githubusercontent.com/BaptisteTellier/autodeploy-web/main/docker-compose.yml

# 2. Drop your Veeam source ISO into ./data/iso/
#    (the init container creates the folders automatically on first run)
cp /path/to/VeeamSoftwareAppliance_*.iso ./data/iso/

# 3. Start
docker compose up -d

# 4. Open http://localhost:8080
```

> **That's it.** Fill the form, click **Generate ISO**, watch the live log, download the result.

Optional files (drop and forget):
| Folder | What to put there |
|---|---|
| `./data/license/` | Veeam `.lic` file — needed when *LicenseVBRTune* is enabled |
| `./data/conf/` | `unattended.xml` + `veeam_addsoconfpw.sh` + `conftoresto.bco` — needed when *RestoreConfig* is enabled |

Change the port or concurrency by copying `.env.example` → `.env` and editing it.

---

## What it does

`autodeploy-web` packages `autodeploy.ps1` inside a Linux container and exposes a web form to replace manual JSON editing. The two projects coexist and are kept in sync automatically:

- **[`autodeploy`](https://github.com/BaptisteTellier/autodeploy)** — the PowerShell script, authoritative source of all logic.
- **`autodeploy-web`** — Docker packaging + web UI. A daily workflow watches for new releases of the PS1 and opens a bump PR automatically.

## Why

- ✅ **No PowerShell / WSL on your host** — Docker is the only dependency.
- ✅ **Web form** instead of editing JSON by hand — with live validation, password generators, GUID generator, preset save/load, import/export.
- ✅ **Live build log** streamed in real time (SSE) — see xorriso progress line by line.
- ✅ **JSON round-trip 100% compatible** with `autodeploy.ps1` — export the form as JSON and run it directly with the PS1 on Windows.
- ✅ **Auto-updated** — each new release of `autodeploy.ps1` triggers a new image automatically.

## How it works

```
┌──────────────────────────────────────────────────────────────┐
│   container : ghcr.io/baptistetellier/autodeploy-web:latest  │
│                                                              │
│   ┌──────────┐  HTTP   ┌───────────┐  spawn   ┌────────────┐ │
│   │ browser  │───────▶ │ Go binary │────────▶ │  pwsh +    │ │
│   │  (form)  │  :8080  │  +HTMX UI │  exec    │ autodeploy │ │
│   │          │ ◀───SSE │           │ ◀─stdout │    .ps1    │ │
│   └──────────┘         └───────────┘          └────────────┘ │
│                              │                       │       │
│                              ▼                       ▼       │
│                       /data/configs/         xorriso (native)│
│                       /data/iso/         ──▶ /data/output/   │
└──────────────────────────────────────────────────────────────┘
```

The PS1 is **not modified** — it runs identically to on Windows + WSL. A tiny `/usr/local/bin/wsl` shim forwards `wsl xorriso ...` calls to the native binary.

## Volumes

| Host path | Container path | Purpose |
|---|---|---|
| `./data/iso/` | `/data/iso` | **Source ISOs** — drop your Veeam ISO here (15–20 GB each) |
| `./data/output/` | `/data/output` | **Generated ISOs** — result of each build, downloadable from the UI |
| `./data/license/` | `/data/license` | Veeam `.lic` files (for `LicenseVBRTune`) |
| `./data/conf/` | `/data/conf` | Restore config files (`unattended.xml`, `.bco`, …) |
| `./data/configs/` | `/data/configs` | Saved JSON presets — also drop PS1-compatible JSONs here directly |

## Environment variables

Configurable via `.env` (copy `.env.example`):

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Host port |
| `WORKER_CONCURRENCY` | `1` | Parallel ISO builds — raise carefully (disk-bound) |

## Limitations

- 🚫 **No authentication** — designed for LAN use. Add a reverse proxy (Caddy, Traefik) for public exposure.
- 🚫 **No job persistence** — restart clears in-memory jobs. Presets and ISOs on disk survive.
- The PS1 hard-coded behaviours (NTP failure aborts build, etc.) apply unchanged.

## Migrating from autodeploy (PS1)

Already have JSON configs? **⬆️ Import JSON** in the UI — the schema is identical.  
See [docs/migration-from-ps1.md](docs/migration-from-ps1.md).

## Development

```bash
make vendor    # download htmx / alpine / tailwind into static/
make build     # go build → bin/autodeploy-web
make test      # go test ./...
make image     # docker build
make dev-up    # docker compose up --build
```

## Acknowledgements

All ISO customisation logic (kickstart, GRUB, MFA, VCSP, license) is done by **[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy)**. This project is packaging only.

## License

MIT — see [LICENSE](LICENSE).

*Made by Baptiste TELLIER for the Veeam community.*
