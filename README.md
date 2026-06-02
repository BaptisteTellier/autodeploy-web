# autodeploy-web

> 🐳 Containerised web UI around [BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy) — generate customised Veeam Software Appliance ISOs from a browser, without PowerShell or WSL on the host.

[![CI](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml/badge.svg)](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-autodeploy--web-blue?logo=docker)](https://github.com/BaptisteTellier/autodeploy-web/pkgs/container/autodeploy-web)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

---

> [!WARNING]
> **No authentication — LAN / trusted network use only.**
>
> autodeploy-web ships with **zero authentication**. Anyone who can reach the port can:
> - create and trigger ISO build jobs,
> - read and download all output files, including generated kickstart/config files that may contain **passwords and sensitive data**,
> - manage uploaded media (ISOs, licence files, config archives).
>
> **Do NOT expose this service directly to the internet.**  
> If internet access is ever required, place it behind a reverse proxy with strong authentication (e.g. Caddy, Traefik, Nginx) and, ideally, a VPN. The `inst.ks=` direct-link feature (see below) is particularly sensitive, as it serves raw config files to any unauthenticated caller — which is by design for PXE/Anaconda use on a trusted LAN, but dangerous on a public network.

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

### `docker-compose.yml` — full content

You can copy-paste the file below directly instead of fetching it with `curl`:

```yaml
###############################################################################
# autodeploy-web — Veeam ISO customisation web UI
# https://github.com/BaptisteTellier/autodeploy-web
#
# QUICK START (only requirement: Docker with Compose plugin)
#
#   1. Copy your source Veeam ISO into  ./data/iso/
#   2. (optional) Copy a .lic file into ./data/license/
#   3. (optional) Copy unattended.xml + veeam_addsoconfpw.sh + conftoresto.bco
#      into ./data/conf/ if you use the Restore Config feature
#   4. docker compose up -d
#   5. Open http://localhost:8080
#
###############################################################################

services:
  autodeploy-web:
    # Pre-built image from GHCR (recommended — no build tools needed on the host).
    # If the image is not yet available (first deploy before CI runs), comment
    # the line below and uncomment the "build:" block instead.
    image: ghcr.io/baptistetellier/autodeploy-web:latest

    # ── Alternative: build locally from source ──────────────────────────────
    # Requires git + Docker BuildKit on the host. Uncomment if GHCR is not
    # yet available (e.g. before the first GitHub Actions run).
    #
    # image: autodeploy-web:local
    # build:
    #   context: https://github.com/BaptisteTellier/autodeploy-web.git
    #   args:
    #     AUTODEPLOY_VERSION: main
    # ────────────────────────────────────────────────────────────────────────

    container_name: autodeploy-web
    restart: unless-stopped

    ports:
      # Change left side to expose on a different host port, e.g. "9090:8080"
      - "${PORT:-8080}:8080"

    environment:
      LISTEN_ADDR: ":8080"
      DATA_DIR: "/data"
      # Maximum concurrent ISO builds — raise only if you have the disk IOPS
      WORKER_CONCURRENCY: "${WORKER_CONCURRENCY:-1}"

    volumes:
      # ── Source Veeam ISOs (15–20 GB each) ─────────────────────────────────
      # Drop VeeamSoftwareAppliance_*.iso or VeeamInfrastructureAppliance_*.iso here.
      - ./data/iso:/data/iso

      # ── Generated customised ISOs ──────────────────────────────────────────
      # After a successful build the ISO appears here and is downloadable from
      # the UI.
      - ./data/output:/data/output

      # ── Veeam licence files (.lic) ─────────────────────────────────────────
      # Required only when LicenseVBRTune = true in your config.
      - ./data/license:/data/license

      # ── Unattended Configuration Restore files ─────────────────────────────
      # Required only when RestoreConfig = true. Place these three files here:
      #   • unattended.xml   (edit BACKUP_PASSWORD inside)
      #   • veeam_addsoconfpw.sh
      #   • conftoresto.bco  (your .bco renamed exactly like this)
      - ./data/conf:/data/conf

      # ── Named JSON configuration presets ──────────────────────────────────
      # Presets saved from the UI are stored here and survive container restarts.
      # You can also drop existing autodeploy.ps1-compatible JSON files here
      # directly — they will appear in the "Load preset" dropdown.
      - ./data/configs:/data/configs

###############################################################################
# FOLDER STRUCTURE (created automatically by the init container below)
#
#   ./data/
#     iso/        ← PUT YOUR VEEAM SOURCE ISOs HERE
#     output/     ← customised ISOs land here
#     license/    ← optional: .lic files
#     conf/       ← optional: restore config files
#     configs/    ← UI presets (auto-managed)
#
###############################################################################

  # One-shot init container that creates the host directories on first run.
  # Harmless on subsequent runs. Runs before autodeploy-web starts.
  init:
    image: busybox:latest
    container_name: autodeploy-web-init
    restart: "no"
    entrypoint: >
      sh -c "
        mkdir -p /mnt/iso /mnt/output /mnt/license /mnt/conf /mnt/configs &&
        echo 'Directories ready.' &&
        echo '' &&
        echo '  → Drop your Veeam source ISO into ./data/iso/' &&
        echo '  → Then open http://localhost:${PORT:-8080}' &&
        echo ''
      "
    volumes:
      - ./data/iso:/mnt/iso
      - ./data/output:/mnt/output
      - ./data/license:/mnt/license
      - ./data/conf:/mnt/conf
      - ./data/configs:/mnt/configs
```

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

## Kickstart live (`inst.ks=`) — Packer-like mode

autodeploy-web can act as a live kickstart server for network boots (PXE / Anaconda), turning it into a lightweight Packer alternative for Veeam appliance deployments.

### How it works

On a job's output page (`/media/output/{jobid}`), every **text file** (e.g. a generated `.cfg` / kickstart file) gets a **🔗 Lien / Link** button next to the usual **DL** (download) button.

Clicking it reveals the file's absolute direct URL and lets you copy it. The URL form is:

```
http://<server>/media/output/<jobid>/<filename>.cfg/content
```

This endpoint serves the raw file as `text/plain` with no authentication required — making it directly consumable by a network bootloader.

### Using it in a GRUB / Anaconda boot

Append the URL to the kernel command line when booting the Veeam appliance installer over PXE:

```
# Modern Anaconda (RHEL 8+ based):
inst.ks=http://<server>/media/output/<jobid>/vbr-ks.cfg/content

# Older Anaconda:
ks=http://<server>/media/output/<jobid>/vbr-ks.cfg/content
```

Replace `<server>` with the hostname or IP of the machine running autodeploy-web (e.g. `192.168.1.10:8080`), and `<jobid>` with the job identifier shown in the UI.

### Worked example — booting from the ISO's GRUB shell (UEFI)

Boot the Veeam appliance ISO, press **`c`** at the GRUB menu to drop into the command shell, then type these **three** lines (press Enter after each):

```
linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS inst.ks=http://192.168.1.29:8080/media/output/bd68e20d-4c13-4fc6-a8e3-211fb6f15d6f/proxy-ks.cfg/content ip=dhcp quiet inst.assumeyes
initrdefi /images/pxeboot/initrd.img
boot
```

Key points:
- **`boot` on the third line is mandatory** — `linuxefi`/`initrdefi` only load the kernel and initrd into memory; nothing starts until you run `boot`. If "nothing happens", you almost certainly forgot it.
- **`ip=dhcp`** brings the network up early so Anaconda can actually fetch the HTTP kickstart. Without it the fetch fails silently. For a static address use e.g. `ip=192.168.1.50::192.168.1.1:255.255.255.0::eth0:none`.
- `linuxefi`/`initrdefi` are the UEFI commands; on a legacy BIOS boot use `linux`/`initrd` instead.
- Use the **🔗 Link** button in the UI to copy the exact URL (job ID + filename) and avoid typos.

### Notes

- Works regardless of job mode — whether the job ran in **config-only** mode (no ISO generated) or produced a **full custom ISO**, the output files always land in the same per-job folder and are reachable via the same URL pattern.
- The kickstart file is generated fresh per job, so you can create a new job for each deployment target with its own parameters (hostname, IP, credentials) and hand out a unique `inst.ks=` URL per machine.
- **Security reminder:** this endpoint intentionally requires no authentication, which is what makes it usable by Anaconda at boot time. This is safe on a trusted LAN and exactly why autodeploy-web must **never be exposed directly to the internet** (see the warning at the top of this page).

---

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
