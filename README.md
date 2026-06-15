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
## Preview
<img width="1878" height="783" alt="image" src="https://github.com/user-attachments/assets/5fbb4dd4-b067-4cdb-8d63-4632ce437293" />
<img width="989" height="750" alt="image" src="https://github.com/user-attachments/assets/a3eebd7c-dd69-48df-bc09-30d1143b79fd" />
<img width="1536" height="1139" alt="image" src="https://github.com/user-attachments/assets/c65b87a8-5a8b-4dcc-8acc-ed65e423f088" />
<img width="1559" height="474" alt="image" src="https://github.com/user-attachments/assets/085283c0-c799-45df-a417-c4f4bcb6566a" />

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
      # ── Persistent data root ──────────────────────────────────────────────
      # Everything the app reads/writes lives under /data. Mounting the whole
      # directory (instead of individual sub-folders) makes the SQLite job
      # database (/data/jobs.db) and the autodeploy.ps1 override survive a
      # container recreate / image rebuild — not just the ISOs and presets.
      #
      #   ./data/iso/      ← PUT YOUR VEEAM SOURCE ISOs HERE (15–20 GB each)
      #   ./data/output/   ← generated customised ISOs (downloadable from the UI)
      #   ./data/license/  ← Veeam .lic files (LicenseVBRTune / wiring install)
      #   ./data/conf/     ← unattended config-restore files (RestoreConfig)
      #   ./data/configs/  ← named JSON presets saved from the UI
      #   ./data/jobs.db   ← SQLite job history (survives restarts)
      - ./data:/data

###############################################################################
# FOLDER STRUCTURE (created automatically by the init container below)
#
#   ./data/
#     iso/        ← PUT YOUR VEEAM SOURCE ISOs HERE
#     output/     ← customised ISOs land here
#     license/    ← optional: .lic files
#     conf/       ← optional: restore config files
#     configs/    ← UI presets (auto-managed)
#     jobs.db     ← SQLite job history (survives restarts)
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
- ✅ **Auto-Deploy** — provision a whole multi-VM Veeam topology (VSA + VIA proxies / hardened repos, optional HA) straight onto Proxmox, with optional **remote kickstart** and automatic **Veeam REST wiring** — no Terraform, no Packer.
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

A single bind mount — `./data:/data` — persists everything (including the job
database and the autodeploy.ps1 override) across container recreates / rebuilds.
The app organises it into these sub-paths:

| Path under `./data/` | Purpose |
|---|---|
| `iso/` | **Source ISOs** — drop your Veeam ISO here (15–20 GB each) |
| `output/` | **Generated ISOs** — result of each build, downloadable from the UI |
| `license/` | Veeam `.lic` files (for `LicenseVBRTune` / wiring license install) |
| `conf/` | Restore config files (`unattended.xml`, `.bco`, …) |
| `configs/` | Saved JSON presets — also drop PS1-compatible JSONs here directly |
| `jobs.db` | SQLite **job history** — survives restarts; jobs can be deleted from the Jobs tab |

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

## Auto-Deploy — multi-VM topologies on Proxmox

Beyond building a single ISO, autodeploy-web can **provision and wire up a complete
Veeam topology** on a hypervisor in one shot — from the **Deploy** page (`/deploy`).
You pick a topology, point it at a Proxmox host, choose where each VM's customised
output came from, and click deploy. It is **Go-native** — no Terraform, no Packer.

> [!NOTE]
> **Proxmox VE is the only production-validated target.** The hypervisor layer is an
> interface (`internal/hypervisor`) with five back-ends. A **Hypervisor** dropdown on
> the Deploy page selects between them:
>
> | Back-end | Status | Remote kickstart |
> |---|---|---|
> | **Proxmox VE** | ✅ validated | ✅ QEMU `sendkey` |
> | **VMware vSphere / vCenter** | 🧪 experimental (untested on live infra) | ✅ USB scan codes |
> | **Microsoft Hyper-V** (WinRM) | 🧪 experimental (untested on live infra) | ✅ `Msvm_Keyboard` |
> | **Nutanix AHV** | 🧪 experimental (untested on live infra) | ❌ no key-injection API |
> | **XCP-ng** | 🧪 experimental (untested on live infra) | ❌ no key-injection API |
>
> The experimental back-ends compile and implement the full lifecycle but have **not**
> been run against real infrastructure — treat them as beta. On **Nutanix AHV** and
> **XCP-ng**, remote kickstart is unavailable (their APIs expose no console
> key-injection), so deploy a **pre-customised ISO** (classic mode) there. Most of the
> walkthrough below describes the Proxmox path; the other back-ends follow the same flow
> with their own connection fields.

### 1. Pick a topology

| # | Topology | VMs created |
|---|---|---|
| a | **VSA** | 1× VSA |
| b | **VSA + VIA Proxy** | VSA + 1× VMware backup proxy |
| c | **VSA + VIA HR** | VSA + 1× hardened repository |
| d | **VSA + VIA Proxy + HR** | VSA + proxy + hardened repo |
| e | **VSA HA + HR** | 2× VSA (HA pair) + hardened repo |
| f | **VSA HA + Proxy + HR** | 2× VSA (HA pair) + proxy + hardened repo |

### 2. Choose the destination Proxmox

Connection settings are entered **in the Deploy form** (nothing is stored on disk):

| Field | Example | Notes |
|---|---|---|
| `pve_url` | `https://192.168.1.181:8006/api2/json` | Proxmox API base URL |
| `pve_node` | `proxmox` | Target node name |
| `pve_storage` | `local-lvm` | Where VM disks land |
| `pve_iso_storage` | `local` | Where ISOs are uploaded / looked up |
| `pve_bridge` | `vmbr0` | Network bridge (+ optional VLAN) |
| auth | `root@pam` + password **or** API token (`root@pam!autodeploy` + secret) | API token recommended |

VMs are created with **UEFI/OVMF + an EFI disk** (`pre-enrolled-keys=0` so the custom
ISO boots without Secure Boot), `q35` machine type, and CPU/RAM you set in the form
(defaults: 4 vCPU / 8 GiB).

### 3. Pick each VM's output + verify

For every VM in the topology you select **which build output folder** it uses (the
customised ISO/config produced earlier by a normal job). A wizard-style **summary
card** lets you verify the whole plan before launch.

**Disks are derived from the role** (and can be raised above the minimum):

| Role | Disks |
|---|---|
| VSA | 2 × 256 GiB |
| VIA (proxy / HR) | 2 × 128 GiB |
| VIA + *Single Disk* | 1 × 128 GiB |

### 4. Two boot modes

- **Customised ISO (classic, most robust).** The per-VM ISO is attached and booted;
  the kickstart embedded in the ISO runs itself. **No keystrokes** are injected.
- **Remote kickstart (Packer-like).** Tick *Remote kickstart* and pick an **original**
  VSA/VIA ISO from the hypervisor library (it is uploaded automatically if missing).
  At boot, autodeploy-web injects the **role-aware GRUB command** through the
  hypervisor console (QEMU `sendkey` on Proxmox, USB scan codes on vSphere,
  `Msvm_Keyboard` on Hyper-V — **not available on Nutanix AHV / XCP-ng**) so the
  appliance fetches its kickstart over HTTP from autodeploy-web:
  - VSA → `inst.stage2=hd:LABEL=VeeamSA fips=1 inst.ks=<HTTP> ip=dhcp …`
  - VIA → `inst.stage2=hd:LABEL=VeeamJeOS inst.ks=<HTTP> ip=dhcp …`

  `c` is sent first to halt the GRUB countdown and open the console; a configurable
  **boot-wait** (default 10 s) gives slow OVMF POST time to reach GRUB before typing.
  Console keystroke injection is inherently best-effort — the classic ISO mode stays
  the most reliable choice when you don't want to avoid per-VM ISO uploads.

### 5. Optional post-boot wiring (Veeam REST)

If enabled, once the VMs are up autodeploy-web **registers the topology into the VSA**
over the Veeam B&R REST API (`:9419`): adds the VIA backup proxy, the hardened
repository, and — for HA topologies — builds the 2-node HA cluster.

- It **waits for each node to answer** on the network before wiring (no blind firing).
- Bounded by a **configurable timeout** (default **45 min**) so it never hangs forever.
- VSA REST credentials are taken from the chosen output's own config
  (`veeamadmin` + its admin password) — **never asked again** in the UI.
- The **HA cluster DNS name** is requested **only** for HA topologies.

### Live status

The deploy detail page streams a **live log (SSE)** and shows a **per-node step badge**
— `created` → `installing` → `ready`, or `failed` — so you can see exactly where each
VM is.

> [!WARNING]
> The Deploy form posts **Proxmox credentials** (and triggers Veeam REST calls with
> the appliance admin password). Like the rest of autodeploy-web this has **no
> authentication** — keep it on a trusted LAN only.

### Hyper-V — enabling WinRM on the host

The Hyper-V back-end drives the host over **WinRM**. By default it connects over
**HTTP on port 5985 using Basic auth**, so the host needs three things enabled. Run
in an **elevated PowerShell on the Hyper-V host**:

```powershell
# 1. Enable WinRM + its inbound firewall rule
Enable-PSRemoting -Force

# 2. Allow Basic auth + unencrypted HTTP — REQUIRED, the client uses Basic over 5985.
#    Without these the deploy fails with: 401 - invalid content type
winrm set winrm/config/service/auth '@{Basic="true"}'
winrm set winrm/config/service '@{AllowUnencrypted="true"}'
```

If the connection **times out** instead, a NIC is likely on the **Public** profile —
WinRM's firewall rule only opens on Domain/Private:

```powershell
Get-NetConnectionProfile                       # look for NetworkCategory : Public
Set-NetConnectionProfile -InterfaceAlias "Ethernet" -NetworkCategory Private
```

**Restrict access to autodeploy-web.** The container's traffic is NAT-ed to the **LAN
IP of the Docker host** (not the container's internal `172.x`), so scope the WinRM
rule to that IP:

```powershell
Set-NetFirewallRule -Name "WINRM-HTTP-In-TCP" -RemoteAddress 192.168.1.29  # your Docker host
```

Verify it works (the listener is up, and the Docker host can reach it):

```powershell
winrm enumerate winrm/config/listener          # Transport = HTTP, Port = 5985, Enabled = true
Test-NetConnection <hyperv-host> -Port 5985    # run from the Docker host → TcpTestSucceeded : True
```

Use a **local administrator** account in the Deploy form's user/password fields.

> [!WARNING]
> `AllowUnencrypted="true"` sends the WinRM payload (including credentials) **in clear
> text** over the LAN. It is acceptable on a trusted/isolated lab segment scoped to a
> single source IP, but for anything else use **HTTPS (5986)** — tick *Use HTTPS* in the
> form and configure an HTTPS WinRM listener with a certificate on the host.

> [!NOTE]
> WinRM streams the 15–20 GB ISO as base64, which is slow. **Pre-stage** the original
> ISO in the host's ISO path so `FindISO` finds it and skips the upload.

---

## Limitations

- 🚫 **No authentication** — designed for LAN use. Add a reverse proxy (Caddy, Traefik) for public exposure.
- 💾 **Jobs are persisted** in a SQLite database (`DATA_DIR/jobs.db`) and survive restarts; they can be deleted from the Jobs tab. **Deployments are still in-memory** and cleared on restart.
- 🧪 **Auto-Deploy: only Proxmox VE is production-validated.** vSphere, Hyper-V, Nutanix AHV and XCP-ng back-ends are implemented but **experimental / untested on live infrastructure** (see the Auto-Deploy note above). On AHV and XCP-ng, remote kickstart is unavailable — use a pre-customised ISO.
- ⚠️ **Hyper-V ISO upload over WinRM is slow** for 15–20 GB ISOs (base64 streaming) — **pre-stage** the ISO in the host's ISO path so `FindISO` skips the upload.
- ⚠️ **Remote-kickstart keystroke injection is best-effort** — there is no clean screenshot/console feedback over the Proxmox API, so the classic customised-ISO boot mode remains the most reliable.
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
