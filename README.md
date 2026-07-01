# autodeploy-web

> 🐳 A containerised web UI for deploying Veeam — from customising a Software Appliance ISO, to provisioning a full multi-VM topology on a hypervisor, to generating the exact REST wiring script. Docker is the only dependency.

[![CI](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml/badge.svg)](https://github.com/BaptisteTellier/autodeploy-web/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-autodeploy--web-blue?logo=docker)](https://github.com/BaptisteTellier/autodeploy-web/pkgs/container/autodeploy-web)
[![Veeam](https://img.shields.io/badge/Veeam-v13.1-00B336.svg)](https://www.veeam.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

---

> [!WARNING]
> **No authentication — LAN / trusted network use only.**
>
> autodeploy-web ships with **zero authentication**. Anyone who can reach the port can create build jobs, download all output files (including generated kickstart/config files that may contain **passwords**), manage uploaded media, and trigger deployments with hypervisor + appliance credentials.
>
> **Do NOT expose this service directly to the internet.** If remote access is needed, put it behind a reverse proxy with strong auth (Caddy, Traefik, Nginx) and ideally a VPN. The `inst.ks=` direct-link feature serves raw config files to any unauthenticated caller by design (for PXE/Anaconda on a trusted LAN) — which is dangerous on a public network.

---

## What it does

autodeploy-web bundles three Veeam deployment workflows behind one web UI:

1. **🛠️ Customise a Veeam ISO** — a browser front-end for the [BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy) PowerShell script. Fill a form (hostname, network, accounts, MFA, license, …) and generate a customised Veeam Software Appliance / VIA ISO with the kickstart and GRUB tweaks baked in — **no PowerShell or WSL on your host**, just Docker.

2. **🚀 Deploy a topology** — provision a whole multi-VM Veeam architecture (VSA + VIA proxies + hardened repositories, optionally HA) onto **Proxmox, Hyper-V, vSphere, Nutanix AHV or XCP-ng**, with optional **remote kickstart** and automatic **Veeam REST wiring** (proxies, repositories, S3, license, HA cluster) — no Terraform, no Packer.

3. **🔌 Craft the REST API** — describe a topology in a form and get the exact, **runnable Veeam REST wiring sequence as PowerShell or curl**. Render-only: copy/paste and run it yourself against appliances you deployed by hand. Same call sequence the Deploy page uses.

---

## Preview
<img width="1878" height="783" alt="image" src="https://github.com/user-attachments/assets/5fbb4dd4-b067-4cdb-8d63-4632ce437293" />
<img width="989" height="750" alt="image" src="https://github.com/user-attachments/assets/a3eebd7c-dd69-48df-bc09-30d1143b79fd" />
<img width="1536" height="1139" alt="image" src="https://github.com/user-attachments/assets/c65b87a8-5a8b-4dcc-8acc-ed65e423f088" />
<img width="1559" height="474" alt="image" src="https://github.com/user-attachments/assets/085283c0-c799-45df-a417-c4f4bcb6566a" />
<img width="1668" height="863" alt="image" src="https://github.com/user-attachments/assets/f387e6bf-d32c-4441-8741-2ad9bb103569" />
<img width="1676" height="1107" alt="image" src="https://github.com/user-attachments/assets/6d736fbb-c50b-48d8-be1d-e3b02c94aa10" />
<img width="874" height="961" alt="image" src="https://github.com/user-attachments/assets/47856eee-91c4-44b8-99e9-a8a202508db9" />
<img width="870" height="1196" alt="image" src="https://github.com/user-attachments/assets/90bb8a7c-50f2-46f8-bca5-316b20e39a0d" />
<img width="882" height="1123" alt="image" src="https://github.com/user-attachments/assets/66f23210-a7c1-44a0-ba99-539a22361528" />
<img width="874" height="1198" alt="image" src="https://github.com/user-attachments/assets/aad771b7-79a6-4466-8c34-0f3169c6f189" />


---

## Quick start — only requirement: Docker

```bash
# 1. Download the compose file
curl -O https://raw.githubusercontent.com/BaptisteTellier/autodeploy-web/main/docker-compose.yml

# 2. Drop your Veeam source ISO into ./data/iso/
#    (the app creates the other sub-folders on first start)
mkdir -p ./data/iso && cp /path/to/VeeamSoftwareAppliance_*.iso ./data/iso/

# 3. Start
docker compose up -d

# 4. Open http://localhost:8080
```

> **That's it.** Fill the form, click **Generate ISO**, watch the live log, download the result.

**Optional drop-in files:**

| Folder | What to put there |
|---|---|
| `./data/license/` | Veeam `.lic` file — needed for *LicenseVBRTune* and for license install during Deploy / Craft API |
| `./data/conf/` | `unattended.xml` + `veeam_addsoconfpw.sh` + `conftoresto.bco` — needed for the *RestoreConfig* feature |

Change the host port or build concurrency by copying `.env.example` → `.env`:

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Host port |
| `WORKER_CONCURRENCY` | `1` | Parallel ISO builds — raise carefully (disk-bound) |
| `AUTH_DISABLED` | `false` | Turn off the built-in admin login (only if bound to localhost or behind your own authenticating proxy) |
| `ADMIN_PASSWORD_HASH` | — | Pre-provision the admin login with a bcrypt hash (skips the first-run setup screen) |
| `ADMIN_PASSWORD` | — | Pre-provision with a plaintext password (hashed at startup; prefer `ADMIN_PASSWORD_HASH`) |

### Authentication

A single-admin session login is **on by default**. On first load you're taken to a
one-time **setup** page (`/setup`) to create an admin password; after that you sign
in at `/login` and log out from the top-bar menu. Sessions last **30 days with no
idle timeout** (a signed cookie that survives restarts), so you rarely re-enter it.

**What it protects.** Without a login, anyone who can reach the web UI — or a
malicious web page you visit while the app is reachable (a "drive-by" / CSRF) — can
drive the tool: upload and run an arbitrary `autodeploy.ps1` (**remote code
execution on the server**), start ISO builds and hypervisor deployments, and read
the credentials baked into saved configs. The login plus a `SameSite=Strict`
session cookie and a same-origin check on state-changing requests close that off.

**Where the credential lives.** A single file, `<data>/auth.json` (with the compose
setup, `./data/auth.json` on the host — mode `0600`). It holds a **bcrypt** hash of
the password and a random HMAC secret used to sign sessions. It is never in the
image and never leaves the box.

**Headless / air-gapped provisioning.** Pre-set the password with
`ADMIN_PASSWORD_HASH` (a bcrypt hash — generate with
`docker run --rm httpd:alpine htpasswd -nbBC 12 x 'yourpassword' | cut -d: -f2`) or,
less securely, `ADMIN_PASSWORD` (plaintext, hashed at startup). Either skips the
setup screen.

**Turning it off.** Set `AUTH_DISABLED=true` **only** when the app is bound to
`localhost` or sits behind your own authenticating reverse proxy. Never expose
autodeploy-web to an untrusted network without authentication.

### Resetting a lost admin password

There is no email recovery (it's a single-operator tool with no mail server) — you
reset it from the host, which you control:

1. **Delete the credential file and restart** — the app returns to first-run setup:
   ```bash
   docker compose down
   rm ./data/auth.json        # the bcrypt hash + session secret
   docker compose up -d
   ```
   Open the app and you'll be sent to `/setup` to create a new password. This also
   rotates the session secret, so every existing session is invalidated.
2. **Or provision a new password via the environment** and restart — put
   `ADMIN_PASSWORD=new-password` (or `ADMIN_PASSWORD_HASH=<bcrypt>`) in your `.env` /
   compose `environment:` and `docker compose up -d`. The environment value takes
   precedence over `auth.json`.

(If auth is running under a bare `docker run`, do the same against the host path you
mounted at `/data`, e.g. `rm /srv/autodeploy/data/auth.json`.)

To pull a new version later: `docker compose pull && docker compose up -d`.

---

## Security considerations

autodeploy-web is a **single-operator infrastructure tool**: by design it runs
PowerShell, builds bootable ISOs, and drives hypervisors. Treat it like the
credential-bearing admin console it is.

- **Run it on a trusted network, authenticated.** The admin login (above) is the
  primary control. Only disable it (`AUTH_DISABLED=true`) behind `localhost` or your
  own authenticating proxy.
- **Prefer HTTPS.** Serve it behind a TLS-terminating reverse proxy (or set
  `X-Forwarded-Proto: https`); the session cookie is then flagged `Secure`
  automatically. Over plain HTTP on a LAN the cookie stays usable but unencrypted.
- **Secrets are stored in cleartext under `/data`** because the build must bake them
  into the appliance: Veeam admin/SO passwords, MFA secrets, recovery token, VCSP
  and S3 keys live in `./data/configs/*.json`, in each job's
  `./data/output/<id>/job-config.json`, and (transiently) in the build config. Keep
  `./data` on a protected volume, restrict its filesystem permissions, and don't back
  it up to untrusted storage. (The job-config snapshot is not downloadable through
  the UI, but it is on disk.) Saved **deploy/Craft templates never contain secrets.**
- **The `autodeploy.ps1` override (Settings → upload/update) is remote code
  execution by design** — an uploaded script runs on the server for every build. Only
  trusted operators should have access (hence the login).
- **The VSA REST API console** proxies arbitrary calls to a deployed appliance using
  a server-side session — a deliberate power tool, scoped to that deployment.
- **Remote-kickstart `.cfg` is served unauthenticated** at `/ks/<output-id>/<file>.cfg`
  — a netbooting appliance (anaconda `inst.ks=`) cannot sign in. Access control is the
  unguessable output UUID in the URL, and only `.cfg` files are served (never the
  config snapshot or any other artefact). Because a `.cfg` contains the credentials it
  bakes into the appliance, treat the deployment network as trusted during boot. This
  is the single route exempt from the login.
- **Hypervisor / VBR TLS.** Connections to Proxmox, vSphere, Hyper-V, Workstation,
  Nutanix, XCP-ng and a target VBR verify TLS unless you tick that connection's
  *Skip TLS verification* box (default on for typical self-signed lab certs).
  Connections to a freshly-deployed VSA always skip verification because its
  self-signed certificate cannot be known in advance.
- **Web fonts are self-hosted** (`static/fonts/`, no external CDN), so the UI works
  fully offline / air-gapped.

---

## How the project works

The container packages the Go web server **and** the unmodified `autodeploy.ps1` together, so the PowerShell script runs identically to how it would on Windows + WSL:

```
┌──────────────────────────────────────────────────────────────┐
│   container : ghcr.io/baptistetellier/autodeploy-web:latest  │
│                                                              │
│   ┌──────────┐  HTTP   ┌───────────┐  spawn   ┌────────────┐ │
│   │ browser  │───────▶ │ Go binary │────────▶ │  pwsh +    │ │
│   │  (form)  │  :8080  │  + web UI │  exec    │ autodeploy │ │
│   │          │ ◀───SSE │           │ ◀─stdout │    .ps1    │ │
│   └──────────┘         └───────────┘          └────────────┘ │
│                              │                       │       │
│                              ▼                       ▼       │
│                       /data/configs/         xorriso (native)│
│                       /data/iso/         ──▶ /data/output/   │
└──────────────────────────────────────────────────────────────┘
```

- **ISO customisation** is delegated to **[`autodeploy`](https://github.com/BaptisteTellier/autodeploy)** — the PowerShell script is the authoritative source of all build logic (kickstart, GRUB, MFA, VCSP, license). The Go server just renders the form, writes the JSON config, and spawns `pwsh autodeploy.ps1`, streaming its output to the browser over SSE. A tiny `/usr/local/bin/wsl` shim forwards the script's `wsl xorriso …` calls to the native `xorriso`. The PS1 is **never modified**, and the form's JSON export is **100 % compatible** with running the script directly on Windows.
- **Deploy & Craft API** are pure-Go: the hypervisor drivers (`internal/hypervisor`) and the Veeam REST client (`internal/veeam`) + wiring orchestration (`internal/wiring`) talk directly to your infrastructure — no PowerShell involved.
- **Auto-updated** — a daily workflow watches for new releases of `autodeploy.ps1` and opens a bump PR, which rebuilds the image automatically.

### Persistence — one bind mount

Everything lives under a single `./data:/data` mount, so all state survives a container recreate / image upgrade:

| Path under `./data/` | Purpose |
|---|---|
| `iso/` | **Source ISOs** — drop your Veeam ISO here (15–20 GB each) |
| `output/` | **Generated ISOs** + per-job config/kickstart files, downloadable from the UI |
| `license/` | Veeam `.lic` files |
| `conf/` | Restore-config files (`unattended.xml`, `.bco`, …) |
| `configs/` | Saved ISO-build JSON presets (drop PS1-compatible JSONs here too) |
| `deploy-presets/` | Saved Deploy / Craft templates |
| `jobs.db` | SQLite **ISO-job history** (survives restarts) |
| `deployments.db` | SQLite **deployment history** (survives restarts) |
| `settings.json` | App settings (history limit) |

---

## Pages

The top navigation exposes everything the app does. Each page is detailed below.

### ✨ New job (`/`)

The main **ISO-build form** (expert mode) — every `autodeploy.ps1` configuration field on one page:

- Appliance type (VSA / VIA-Proxy / VIA-HR / …), hostname, network (DHCP or static IP / subnet / gateway / DNS), NTP, timezone.
- Veeam accounts (admin / SO) with **password generators** and complexity validation, **MFA**, GUID generator.
- License baking (*LicenseVBRTune*), config restore (*RestoreConfig*), High-Availability, single-disk, GRUB timeout, and the rest of the PS1 options.
- **Preset save/load**, **⬆️ Import / ⬇️ Export JSON** (round-trips with the PowerShell script), and live field validation.
- Choose **full custom ISO** or **config-only** (just the `.cfg`/kickstart, no ISO rebuild). Click **Generate** → watch the **live build log** (SSE, `xorriso` line-by-line) → download from Output.

### 🧙 Wizard (`/wizard`)

A **guided, step-by-step** version of the same configuration, with inline help tips on every field. Produces the identical job as *New job* — friendlier for first-time users; switch to *New job* for the dense all-in-one form.

### 📁 Workspace (`/media/workspace`)

Manage the files the app works with: **upload source ISOs**, license files, and restore-config files into `./data`, and see what's present. Saves copying files onto the host by hand.

### 📦 Output (`/media/output`)

Browse and **download** every job's results — the generated ISO and all per-job config/kickstart files.

It also doubles as a **live kickstart server** (a lightweight Packer alternative). Every text file gets a **🔗 Link** button exposing a no-auth direct URL:

```
http://<server>/media/output/<jobid>/<filename>.cfg/content
```

…which you append to an Anaconda boot to install over the network:

```
# Modern Anaconda (RHEL 8+):  inst.ks=http://<server>/media/output/<jobid>/vbr-ks.cfg/content
# Older Anaconda:             ks=http://<server>/media/output/<jobid>/vbr-ks.cfg/content
```

**Worked example — from the ISO's GRUB shell (UEFI):** boot the appliance ISO, press **`c`**, type three lines (Enter after each):

```
linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS inst.ks=http://192.168.1.10:8080/media/output/<jobid>/proxy-ks.cfg/content ip=dhcp quiet inst.assumeyes
initrdefi /images/pxeboot/initrd.img
boot
```

- **`boot` (line 3) is mandatory** — the first two lines only load kernel + initrd; nothing starts until `boot`.
- **`ip=dhcp`** brings networking up so Anaconda can fetch the HTTP kickstart. For a static address use `ip=<ip>::<gw>:<mask>:<host>::none`. (The **Deploy** page generates this automatically for fixed-IP nodes.)
- `linuxefi`/`initrdefi` are UEFI; on legacy BIOS use `linux`/`initrd`.
- This endpoint is unauthenticated by design (so Anaconda can read it) — another reason to keep autodeploy-web on a trusted LAN.

### 🔑 Licenses (`/media/license`)

Upload and manage Veeam `.lic` files. They're used when *LicenseVBRTune* bakes a license into an ISO, and picked from a dropdown for **license install over REST** during Deploy and Craft API.

### 📋 Jobs (`/jobs`)

Two histories, both persisted in SQLite and survive restarts:

- **ISO-creation jobs** — every build with state, timestamps, and a per-row delete.
- **Deployments** — every Auto-Deploy run, with **Created** / **Finished** date-time columns, a **↻ retry of …** link when a run is a retry of another, and per-row actions: **retry** (re-run end-to-end), **re-wire** (re-run only the REST wiring against existing VMs), and **🗑 delete** (removes the *record* only — does not touch the VMs). A run interrupted by a restart is reloaded as *failed ("interrupted by a restart")*. Opening a deployment shows its **live log** and a **node table with each machine's IP** (static IPs immediately; DHCP IPs stream in as they resolve).

### 🚀 Deploy (`/deploy`)

Provision and wire a complete topology in one shot.

**Hypervisor support** (a dropdown selects the back-end):

| Back-end | Status | Remote kickstart |
|---|---|---|
| **Proxmox VE** | ✅ validated | ✅ QEMU `sendkey` |
| **Microsoft Hyper-V** (WinRM) | ✅ validated | ✅ `Msvm_Keyboard` |
| **VMware vSphere / vCenter** | 🧪 experimental | ✅ USB scan codes |
| **Nutanix AHV** | 🧪 experimental | ❌ no key-injection API |
| **XCP-ng** | 🧪 experimental | ❌ no key-injection API |
| **VMware Workstation** (WinRM + vmrun) | 🧪 experimental | ✅ VNC |

> The experimental back-ends implement the full lifecycle but haven't been validated on real infrastructure. On AHV / XCP-ng (no console key-injection) you must deploy a **pre-customised ISO** rather than remote kickstart.

**Flow:**
1. **Pick a topology** — VSA · VSA+Proxy · VSA+HR · VSA+Proxy+HR · VSA-HA+HR · VSA-HA+Proxy+HR. Click **＋** on any VIA slot to add more proxy/HR nodes.
2. **Connection** — hypervisor host/credentials, entered in the form (nothing stored). VMs are created UEFI/OVMF (Secure Boot off for the unsigned ISO), with role-derived disks (VSA 2×256 GiB, VIA 2×128 GiB; single-disk 1×128 GiB) and editable CPU/RAM.
3. **Per-node output** — choose which build output each VM uses; a summary card verifies the plan. Each node needs a distinct fixed IP (or DHCP) — the form blocks launch on duplicate IPs.
4. **Boot mode** — *Customised ISO* (most robust; embedded kickstart self-runs) or *Remote kickstart* (boots an original ISO and injects the role-aware GRUB command over the console; fixed-IP nodes get a static `ip=` arg automatically).
5. **Post-boot wiring (Veeam REST)** — registers every proxy & hardened repo, installs the license, and builds the HA cluster, waiting for each node to answer; bounded by a configurable timeout. VSA credentials come from the chosen output.

**Pre-flight checks** warn before launch when config and options don't match (e.g. GRUB timeout ≤ boot-wait under remote kickstart; a VSA output not built with HA on an HA topology).

**Advanced options & behaviour:**

| Feature | Behaviour |
|---|---|
| **Customised-ISO boot** | Per-VM ISO attached & booted; embedded kickstart self-runs; no keystrokes. Most robust. |
| **Remote kickstart** | Injects a role-aware GRUB command over the console to fetch the kickstart over HTTP. `c` halts the GRUB countdown; a boot-wait (default 10 s) lets slow OVMF reach GRUB. Not on AHV/XCP-ng. |
| **Static IP vs DHCP** | Fixed-IP → static `ip=<ip>::<gw>:<mask>:<host>::none` auto-generated (works without DHCP). DHCP → IP resolved from the guest agent before wiring. |
| **Post-boot wiring** | Registers proxies & hardened repos over REST (`:9419`), waits per node, bounded timeout, re-auths on token expiry, parallelised across nodes. |
| **License install (REST)** | Installs a `/data/license/*.lic` after boot (needed under remote kickstart, which boots unlicensed); warns if a license was baked into the output. |
| **HA cluster** | HA topologies only — needs a DNS name + a free VIP. Config backup is redirected to the first hardened repo, the Default repo removed, then the 2-node cluster is formed. |
| **node_exporter** | Enables the Prometheus metrics endpoint (optional TLS + basic auth). |
| **Syslog** | Forwards VBR events to a syslog target (host / port / UDP·TCP·TLS). |
| **S3 / object storage** | Creates the cloud credential then the repo (Amazon S3 or S3-compatible). Endpoint auto-prefixed with `https://`; the bucket folder is created first (idempotent); optional **take-over** of a bucket already used by another server; optional **Linux mount-server pin** (one of the VIA-Proxy nodes). |
| **Copy to deploy / presets** | Prefill the whole form from a past deployment (*Copy*) or a saved deploy template. |

> Adding a Cloud Connect **service provider** is not exposed by the VBR REST API (1.3-rev2) — use `Add-VBRCloudServiceProvider` on the appliance.

The deploy detail page streams a **live log** with a per-node step badge (`created` → `installing` → `ready` / `failed`).

#### Hyper-V — enabling WinRM on the host

The Hyper-V back-end drives the host over **WinRM** (HTTP 5985, Basic auth by default). On the Hyper-V host, in an **elevated PowerShell**:

```powershell
Enable-PSRemoting -Force
# Required — the client uses Basic over 5985 (without these: "401 - invalid content type"):
winrm set winrm/config/service/auth '@{Basic="true"}'
winrm set winrm/config/service '@{AllowUnencrypted="true"}'
```

If the connection **times out**, a NIC is probably on the **Public** profile (WinRM's rule only opens on Domain/Private):

```powershell
Get-NetConnectionProfile                       # NetworkCategory : Public ?
Set-NetConnectionProfile -InterfaceAlias "Ethernet" -NetworkCategory Private
```

Scope the WinRM rule to the **Docker host's LAN IP** (container traffic is NAT-ed to it), and verify:

```powershell
Set-NetFirewallRule -Name "WINRM-HTTP-In-TCP" -RemoteAddress 192.168.1.50   # your Docker host
Test-NetConnection <hyperv-host> -Port 5985                                  # from the Docker host
```

Use a **local administrator** in the Deploy form.

> [!WARNING]
> `AllowUnencrypted="true"` sends WinRM payloads (incl. credentials) in clear text. Acceptable on an isolated lab segment scoped to one source IP; otherwise use **HTTPS (5986)** — tick *Use HTTPS* and configure an HTTPS listener with a certificate.

> [!NOTE]
> WinRM streams the 15–20 GB ISO as base64 (slow). **Pre-stage** the original ISO in the host's ISO path so `FindISO` skips the upload.

#### VMware Workstation — prerequisites & gotchas

The VMware Workstation back-end drives a **Windows host** over **WinRM** (same transport as Hyper-V — see the WinRM setup steps above). The container authors a `.vmx`, calls `vmrun.exe` and `vmware-vdiskmanager.exe` to create and start VMs, then injects the remote-kickstart GRUB command over **VNC** (`RemoteDisplay.vnc`). Status: 🧪 experimental.

**Prerequisites on the Windows host:**

- **VMware Workstation 17 / "26H1"** installed. Default path: `C:\Program Files\VMware\VMware Workstation` (older installs may be under `C:\Program Files (x86)\...`). The form's **Install directory** field drives both `vmrun.exe` and `vmware-vdiskmanager.exe` — adjust it if your install is non-standard.
- **WinRM enabled** — follow the same elevated-PowerShell steps as the Hyper-V section.
- A **bridged** virtual network (e.g. `VMnet0`). On hosts with multiple NICs (Wi-Fi, Ethernet, other VMware/Hyper-V virtual adapters) **do not** leave VMnet0 on *Automatic* bridging — open **Virtual Network Editor** and pin VMnet0 to the physical NIC on your deploy LAN. Auto-bridging frequently picks the wrong adapter, which shows up as the installer failing to reach the kickstart server with `curl: (7) … No route to host`.

**Deploy-form fields** (the `ws_*` panel, shown when *Provider = VMware Workstation*): WinRM host / port / user / password, HTTPS + skip-TLS toggles, install directory, VM base directory, ISO directory, virtual network (vmnet), VNC host, VNC base port.

**Two settings that matter most:**

- **VNC host** — must be the host's **LAN IP** (reachable from outside Docker), *not* `localhost` / `127.0.0.1`. The container connects to VMware's VNC listener from a different network namespace. Open the host firewall for the VNC port range (base port **5910**, +1 per VM).
- **Kickstart base URL** (`ks_base_url`) — must be reachable **from the bridged VM** (i.e. the Docker host's LAN IP + the web UI port). Allow inbound on that port in the host firewall.

**Black screen in the Workstation GUI (expected behaviour):** VMs are started headless via WMI so they survive the WinRM session — they run in Windows **session 0**, which has no interactive desktop. If you open the VM through *"Open all background virtual machines"* in the Workstation tray, the console window renders **black**. This does not mean the install failed. To watch the real console, point a VNC viewer at `<host-LAN-IP>:5910` (base 5910 + VM index).

<img width="296" height="181" alt="image" src="https://github.com/user-attachments/assets/dfe6ed8c-8f73-4af5-8af3-ef7d875034d0" />

<img width="1498" height="921" alt="image" src="https://github.com/user-attachments/assets/10094904-650a-4aec-9589-b63d73e4ee04" />


#### REST API console — talk to a deployed VSA

On a deployment's **detail page** (`/deploy/{id}`), the header button **Open REST API** opens an interactive REPL against that deployment's primary VSA: pick a method (**GET / POST / PUT / DELETE**), type a path (e.g. `/api/v1/serverInfo`) and an optional JSON body, hit **Send**, and see the status code + pretty-printed response. The button toggles to **Close REST API session**.

- **No credentials to re-enter** — the container authenticates to the VSA using the admin password already baked into the deployment's output config (same source as the wiring).
- **Server-side proxy** — your browser never talks to the VSA directly (it has a self-signed cert and may be on a different network); every call is proxied through the container, which already reaches the VSA.
- **Persistent** — the request/response history is kept in `localStorage` and the session survives closing the modal, reloading the page, and a container restart (it silently re-authenticates).
- **Self-cleaning** — sessions live in memory, expire after 30 min idle, and are dropped automatically when the deployment is removed or deleted.

> ⚠️ This is a power/debug tool: it issues **authenticated** calls against the **live** VSA with **all** HTTP methods (including `DELETE`). Consistent with the app's LAN-only, no-auth model — use with care.

### 🔌 Craft API (`/craft-api`)

The same wiring as the Deploy page, but **render-only** — for appliances you deployed by hand. Fill a Deploy-style form (pick a topology, ＋ add proxy/HR nodes, enter each node's IP/hostname/pairing code, connection, and the advanced options), click **Generate**, and get the **exact REST call sequence as a runnable PowerShell or curl script** (toggle, copy, download `wire.ps1` / `wire.sh`).

The generated script is genuinely runnable: OAuth token capture, wait-for-session loops, per-host `connectionCertificate` → computed SSH fingerprint → add-host, license install (paste base64 — normalised automatically; the field explains how to produce it), node_exporter / syslog / S3 / HA. It mirrors the live `internal/veeam` client call-for-call (guarded by tests). **Save/load templates** (secrets excluded) are stored in `/data/deploy-presets`.

> Craft API never makes live requests itself — it only generates a script you run yourself. The script is one-shot (no idempotent re-runs); generate it against freshly deployed appliances.

### ⚙️ Settings (`/admin`)

App-wide settings: the **history limit** (max finished ISO jobs *and* deployments kept, default **20**, auto-pruned), updating the bundled `autodeploy.ps1` from GitHub, and language (EN / FR).

---

## Limitations

- 🚫 **No authentication** — LAN use only; add a reverse proxy for any public exposure.
- 🧪 **Deploy:** only **Proxmox VE** and **Hyper-V** are production-validated. vSphere / Nutanix AHV / XCP-ng are implemented but untested on live infrastructure; AHV/XCP-ng can't do remote kickstart (use a pre-customised ISO).
- ⚠️ **Hyper-V/Workstation Pro ISO upload over WinRM is slow** (base64) — pre-stage the ISO so `FindISO` skips the upload.
- ⚠️ **Remote-kickstart keystroke injection is best-effort** — no console feedback over the hypervisor API; the customised-ISO boot mode is the most reliable.
- 🔌 **Craft API scripts are one-shot** — they don't include the live wiring's find-before-add idempotency, so run them against fresh appliances.
- The PS1's hard-coded behaviours (e.g. NTP failure aborts the build) apply unchanged.

## Migrating from autodeploy (PS1)

Already have JSON configs? **⬆️ Import JSON** in the UI — the schema is identical. See [docs/migration-from-ps1.md](docs/migration-from-ps1.md).

## Development

```bash
make vendor    # download htmx / alpine / tailwind into static/
make build     # go build → bin/autodeploy-web
make test      # go test ./...
make image     # docker build
make dev-up    # docker compose up --build
```

## Acknowledgements

All ISO customisation logic (kickstart, GRUB, MFA, VCSP, license) is done by **[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy)**. This project provides the Docker packaging, web UI, and the Deploy / Craft API layers.

## License

MIT — see [LICENSE](LICENSE).

*Made by Baptiste TELLIER for the Veeam community.*
