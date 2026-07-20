# Running autodeploy-web natively in a VM (without Docker)

This guide explains how to build and run **autodeploy-web** directly inside a
virtual machine (or any bare server), instead of the recommended Docker
container. It is written for someone with **no prior Go, Linux or PowerShell
experience** — every command can be copy-pasted as-is.

> **Docker is still the easiest and supported path.** Use this guide only if you
> specifically cannot (or do not want to) run a container — e.g. a locked-down
> host, a policy that forbids Docker, or you want the service managed by
> `systemd`.

There are two routes:

- **[Part A — Debian / Ubuntu Linux VM](#part-a--debian--ubuntu-linux-vm-recommended)** — *recommended*. It mirrors exactly what the container does, so it is the most predictable.
- **[Part B — Windows VM](#part-b--windows-vm)** — the original environment `autodeploy.ps1` was written for (needs WSL).

---

## What the container normally provides for you

The Docker image bundles four things. A VM must supply the same four:

| # | Piece | Why it's needed |
|---|---|---|
| 1 | The **`autodeploy-web` Go binary** | The web server itself. Compiled statically — no runtime libraries. |
| 2 | **PowerShell 7 (`pwsh`)** | All ISO customisation is done by `autodeploy.ps1`, which is PowerShell. |
| 3 | **`xorriso` + `rsync`** | Called by `autodeploy.ps1` to unpack and rebuild the ISO. |
| 4 | **`autodeploy.ps1`** (the companion script) | The actual ISO logic, from [BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy). |

One non-obvious detail: since autodeploy **v2.8**, `autodeploy.ps1` detects Linux
and calls `xorriso` directly. The `wsl` / `cmd` **wrapper scripts** (installed by
Part A) are now only a **fallback** — needed if you import an older, Windows-only
version of the script.

Everything else — the web UI, fonts, JavaScript, HTML templates — is **embedded
inside the Go binary**, so there are no separate web assets to deploy.

---

## Part A — Debian / Ubuntu Linux VM (recommended)

Tested on **Debian 12 (bookworm)** and **Ubuntu 22.04 / 24.04**, on an
`x86_64 / amd64` machine. Commands assume you can use `sudo`.

### Step 0 — Provision the VM

Create a VM with:

- **OS:** Debian 12 or Ubuntu 22.04+ (minimal/server install is fine)
- **CPU/RAM:** 2 vCPU, 2 GB RAM minimum
- **Disk:** at least **20 GB free** — ISO builds are large (a source ISO can be 5–10 GB, and a build needs room for a working copy plus the output)
- **Network:** the VM must be reachable from wherever you'll open the web UI, and (if you use remote kickstart) from the VMs you deploy

Log in and update the package list:

```bash
sudo apt-get update
```

### Step 1 — Install the build and runtime tools

Install everything from the distro in one go:

```bash
sudo apt-get install -y \
    golang-go \
    git \
    curl \
    xorriso \
    rsync \
    ca-certificates \
    libicu72 \
    libssl3 \
    tzdata
```

> **Note on `golang-go`:** the project needs **Go 1.25 or newer**. Check your
> version with `go version`. If your distro ships an older Go (Debian 12 ships
> 1.19), install the current Go instead:
>
> ```bash
> sudo apt-get remove -y golang-go            # remove the old one if present
> cd /tmp
> curl -fsSLO https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
> sudo rm -rf /usr/local/go
> sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
> echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
> export PATH=$PATH:/usr/local/go/bin
> go version   # should print go1.25.0
> ```
>
> On Ubuntu 24.04, `libicu72` may be named `libicu74` — if the install fails on
> that package, replace `libicu72` with `libicu74`.

### Step 2 — Install PowerShell 7

We install the official PowerShell tarball (same method the container uses — no
extra apt repositories to configure):

```bash
PWSH_VERSION=7.4.6
cd /tmp
curl -fsSL "https://github.com/PowerShell/PowerShell/releases/download/v${PWSH_VERSION}/powershell-${PWSH_VERSION}-linux-x64.tar.gz" -o pwsh.tar.gz
sudo mkdir -p /opt/microsoft/powershell/7
sudo tar -xzf pwsh.tar.gz -C /opt/microsoft/powershell/7
sudo chmod +x /opt/microsoft/powershell/7/pwsh
sudo ln -sf /opt/microsoft/powershell/7/pwsh /usr/bin/pwsh
rm -f pwsh.tar.gz
```

Verify:

```bash
pwsh --version    # should print: PowerShell 7.4.6
```

> On **arm64** VMs (e.g. an ARM cloud instance), replace `linux-x64` with
> `linux-arm64` in the download URL.

### Step 3 — Get the source code

```bash
sudo mkdir -p /opt/autodeploy-web
sudo chown "$USER" /opt/autodeploy-web
git clone https://github.com/BaptisteTellier/autodeploy-web.git /opt/autodeploy-web
cd /opt/autodeploy-web
```

### Step 4 — Download the embedded web assets, then build

The web UI depends on a few third-party JS/CSS files that are fetched **before**
compiling and then baked into the binary:

```bash
cd /opt/autodeploy-web
sh scripts/fetch-vendor.sh      # downloads htmx / Alpine / Tailwind into static/
go mod tidy                     # resolve Go dependencies
go build -o /usr/local/bin/autodeploy-web ./cmd/autodeploy-web
```

(Alternatively, `make vendor && make build` does the first two steps and puts the
binary in `./bin/`.)

Verify the binary runs:

```bash
autodeploy-web --help 2>/dev/null; echo "binary present: $(command -v autodeploy-web)"
```

### Step 5 — Install the `wsl` and `cmd` wrapper shims

`autodeploy.ps1` calls `wsl …` and `cmd /c …`. On Linux we redirect those to the
native tools. The repo ships the two shims — install them into your `PATH`:

```bash
sudo cp /opt/autodeploy-web/scripts/wsl-wrapper.sh /usr/local/bin/wsl
sudo cp /opt/autodeploy-web/scripts/cmd-wrapper.sh /usr/local/bin/cmd
sudo chmod +x /usr/local/bin/wsl /usr/local/bin/cmd
```

Verify:

```bash
wsl --version     # prints "WSL version (container shim): 1.0" — this is expected
```

> Since autodeploy v2.8 these shims are **optional** (the script calls xorriso
> natively). Installing them anyway is recommended: it's free and covers the case
> where you import a pre-v2.8 script.

### Step 6 — Get `autodeploy.ps1`

Clone the companion script the app will run:

```bash
sudo git clone https://github.com/BaptisteTellier/autodeploy.git /opt/autodeploy
```

The app expects the script name `autodeploy.ps1` inside that folder (both are
configurable — see the env-var table below).

### Step 7 — Create the data directory

This is where ISOs, outputs, configs and the credential file live:

```bash
sudo mkdir -p /data/iso /data/output /data/license /data/conf /data/configs /data/work
sudo chown -R "$USER" /data
```

| Folder | Contents |
|---|---|
| `/data/iso/` | **Drop your Veeam source ISO here** |
| `/data/output/` | Generated ISOs and per-job config/kickstart files |
| `/data/license/` | Optional Veeam `.lic` file |
| `/data/conf/` | Optional RestoreConfig files (`unattended.xml`, …) |
| `/data/configs/` | Saved form configurations |
| `/data/work/` | Temporary build staging (auto-cleaned on start) |

### Step 8 — Run it once, by hand (to test)

```bash
LISTEN_ADDR=":8080" \
DATA_DIR="/data" \
AUTODEPLOY_DIR="/opt/autodeploy" \
PS_SCRIPT="autodeploy.ps1" \
WORKER_CONCURRENCY="1" \
TZ="Europe/Paris" \
autodeploy-web
```

You should see `listening on :8080`. Open **http://\<vm-ip\>:8080** in a browser —
you'll be sent to `/setup` to create an admin password. Once that works, stop it
with `Ctrl+C` and set up the service in the next step.

### Step 9 — Run it as a service (`systemd`)

So the app starts on boot and restarts on failure, create a service unit:

```bash
sudo tee /etc/systemd/system/autodeploy-web.service >/dev/null <<'EOF'
[Unit]
Description=autodeploy-web
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/autodeploy-web
Restart=on-failure
RestartSec=5

# --- Configuration ---
Environment=LISTEN_ADDR=:8080
Environment=DATA_DIR=/data
Environment=AUTODEPLOY_DIR=/opt/autodeploy
Environment=PS_SCRIPT=autodeploy.ps1
Environment=WORKER_CONCURRENCY=1
Environment=TZ=Europe/Paris
# Authentication is ON by default; on first launch open the UI to set a password.
# To pre-set it non-interactively, uncomment ONE of:
# Environment=ADMIN_PASSWORD_HASH=<bcrypt-hash>
# Environment=ADMIN_PASSWORD=<plaintext-password>
# To disable auth entirely (ONLY behind your own proxy / on localhost):
# Environment=AUTH_DISABLED=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now autodeploy-web
```

Check it's running and watch the log:

```bash
systemctl status autodeploy-web
journalctl -u autodeploy-web -f
```

Open **http://\<vm-ip\>:8080**, create your admin password, drop a Veeam ISO into
`/data/iso/`, and use the app exactly as you would the container.

### Updating later

```bash
cd /opt/autodeploy-web
git pull
sh scripts/fetch-vendor.sh
go mod tidy
go build -o /usr/local/bin/autodeploy-web ./cmd/autodeploy-web
sudo systemctl restart autodeploy-web

# and to update the ISO logic:
sudo git -C /opt/autodeploy pull
```

### Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Build fails: `wsl: command not found` in the job log | Step 5 skipped — install the `wsl`/`cmd` shims. |
| `pwsh: command not found` | Step 2 failed — re-check the symlink `/usr/bin/pwsh`. |
| `go: unknown … go1.25` / build errors about Go version | Go too old — install Go 1.25 (see note in Step 1). |
| ISO build stops with `xorriso`/`rsync` not found | Step 1 didn't install them, or they're not on the service's `PATH`. |
| Web UI unreachable from another machine | Open port 8080 in the VM firewall (`sudo ufw allow 8080/tcp`) and confirm `LISTEN_ADDR=:8080` (not `127.0.0.1:8080`). |
| Forgot the admin password | `sudo rm /data/auth.json && sudo systemctl restart autodeploy-web` → you'll be sent back to `/setup`. |

---

## Part B — Windows VM

On Windows you run `autodeploy.ps1` the way it was originally designed — with a
**real WSL** providing the Linux tools, so the Linux shims are **not** used.

### Requirements

1. **Windows 10/11 or Windows Server 2019+.**
2. **PowerShell** — Windows PowerShell 5.1 (built in) or PowerShell 7.
3. **WSL enabled** with a Linux distribution, and `xorriso` + `rsync` installed *inside* WSL:
   ```powershell
   wsl --install -d Ubuntu          # then reboot and set up the Ubuntu user
   wsl -- sudo apt-get update
   wsl -- sudo apt-get install -y xorriso rsync
   ```
4. **Go 1.25+** — download the Windows MSI from <https://go.dev/dl/>.
5. **Git** — <https://git-scm.com/download/win>.

### Build

Open **PowerShell** and run:

```powershell
git clone https://github.com/BaptisteTellier/autodeploy-web.git C:\autodeploy-web
cd C:\autodeploy-web
sh scripts\fetch-vendor.sh          # requires Git Bash's "sh"; or run the curl steps manually
go mod tidy
$env:GOOS="windows"
go build -o C:\autodeploy-web\autodeploy-web.exe .\cmd\autodeploy-web
```

Clone the ISO script:

```powershell
git clone https://github.com/BaptisteTellier/autodeploy.git C:\autodeploy
```

### Run

Create the data folders and launch, pointing the env vars at Windows paths:

```powershell
mkdir C:\data\iso, C:\data\output, C:\data\license, C:\data\conf, C:\data\configs, C:\data\work
$env:LISTEN_ADDR   = ":8080"
$env:DATA_DIR      = "C:\data"
$env:AUTODEPLOY_DIR= "C:\autodeploy"
$env:PS_SCRIPT     = "autodeploy.ps1"
C:\autodeploy-web\autodeploy-web.exe
```

Open **http://\<vm-ip\>:8080**. To run it as a background Windows service, wrap
the `.exe` with a tool such as [NSSM](https://nssm.cc/) and set the same
environment variables there.

> On Windows, `wsl xorriso …` and `cmd /c …` resolve to the *native* Windows
> `wsl.exe` and `cmd.exe`, so make sure `xorriso`/`rsync` are installed **inside
> your WSL distro** (not on the Windows side).

---

## Appendix — Environment variables

These are read by the server at startup (see `cmd/autodeploy-web/main.go`):

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Address/port the web server binds to. Use `:8080` to listen on all interfaces. |
| `DATA_DIR` | `/data` | Root of the data directory (iso/output/configs/…). |
| `AUTODEPLOY_DIR` | `/opt/autodeploy` | Folder containing `autodeploy.ps1`. |
| `PS_SCRIPT` | `autodeploy.ps1` | Name of the PowerShell script inside `AUTODEPLOY_DIR`. |
| `WORKER_CONCURRENCY` | `1` | Parallel ISO builds. Raise carefully — builds are disk-heavy. |
| `TZ` | `Europe/Paris` | Timezone for timestamps. |
| `AUTH_DISABLED` | `false` | Set `true` to turn off the built-in login (only behind your own proxy / on localhost). |
| `ADMIN_PASSWORD_HASH` | — | Pre-provision the admin login with a bcrypt hash (skips first-run setup). Generate with: `docker run --rm httpd:alpine htpasswd -nbBC 12 x 'yourpassword' \| cut -d: -f2`. |
| `ADMIN_PASSWORD` | — | Pre-provision with a plaintext password (hashed at startup; prefer the hash). |

---

*Back to the [main README](../README.md).*
