# Plan d'implémentation — `autodeploy-web` (v2 — révisé)

> Refonte conteneurisée du projet [`autodeploy`](https://github.com/BaptisteTellier/autodeploy) en une appli web autonome. **Le PS1 tourne tel quel dans le conteneur** — `autodeploy-web` est un *front-end* (formulaire + worker) qui pilote `autodeploy.ps1` sans dupliquer sa logique.

---

## 0. Décisions actées

| # | Décision |
|---|---|
| Repo | **Nouveau repo GitHub** `BaptisteTellier/autodeploy-web` — projet séparé qui cohabite avec `autodeploy` |
| Stack | **Go monolithique** — 1 binaire + frontend embedded (`//go:embed`) |
| Frontend | **HTMX + Alpine.js + Tailwind (CDN)** — pas de build Node, idéal pour Go mono |
| Logique métier | **AUCUN port** de la logique PS1 — on appelle `pwsh autodeploy.ps1 -ConfigFile <json>` en subprocess |
| API | **Pas de "REST API" formelle** — juste 5-6 handlers HTTP internes au service (form POST, SSE logs, download). Pas d'OpenAPI, pas de versioning. |
| Persistance | **Aucune DB** — jobs en mémoire (perdus au restart, acceptable). Configs JSON nommées sauvées sur volume → dropdown de réutilisation. |
| Auth | **Aucune** — usage LAN local |
| Intégrité ISO | **Pas de validation** — on fait confiance à xorriso (comme le PS1) |
| Distribution | Image **GHCR publique** : `ghcr.io/baptistetellier/autodeploy-web:latest` |
| Périmètre v1 | **Full P0 → P7** |

---

## 1. Architecture cible

```
┌─────────────────────────────────────────────────────────────┐
│  Image: ghcr.io/baptistetellier/autodeploy-web:latest        │
│  Base:  mcr.microsoft.com/powershell:7.4-debian-12-slim      │
│  + xorriso, rsync, git                                       │
│  + autodeploy.ps1 (cloné au build-time, version pinned)      │
│  + binaire Go autodeploy-web                                 │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │           autodeploy-web (Go binary)                  │  │
│  │                                                       │  │
│  │  HTTP server (port 8080)                              │  │
│  │   ├── GET  /            → page formulaire (HTMX)     │  │
│  │   ├── POST /jobs        → écrit job.json, spawn PS1  │  │
│  │   ├── GET  /jobs/{id}/stream → SSE stdout pwsh       │  │
│  │   ├── GET  /jobs/{id}/download → stream ISO sortie   │  │
│  │   ├── GET  /jobs        → liste in-memory            │  │
│  │   ├── GET  /configs     → liste JSON sauvegardés     │  │
│  │   ├── POST /configs     → save preset                │  │
│  │   └── GET  /configs/{name} → load JSON pour preload  │  │
│  │                                                       │  │
│  │  Worker pool (Go goroutines, concurrence=1 par déf)  │  │
│  │   └── exec.Command("pwsh", "autodeploy.ps1", ...)    │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  Volumes hôte montés :                                       │
│   /data/iso      ← ISO sources Veeam (15-20 GB chacune)     │
│   /data/output   ← ISO customisées générées                  │
│   /data/license  ← .lic Veeam                                │
│   /data/conf     ← unattended.xml, .bco pour restore        │
│   /data/configs  ← presets JSON (l'utilisateur en garde N)  │
└─────────────────────────────────────────────────────────────┘
```

**Pourquoi ça marche :**
- `autodeploy.ps1` veut être exécuté "dans le même dossier que l'ISO source" → on `cd /data/iso` avant le spawn.
- Le PS1 v2.8+ ne prend que `-ConfigFile` en CLI → 100% compat avec notre formulaire qui écrit un JSON.
- WSL n'existe plus, mais le PS1 appelle `wsl xorriso ...` → **petit patch container-only** : un wrapper `/usr/local/bin/wsl` qui forward vers le vrai binaire (`exec "$@"`). Le PS1 est inchangé.

---

## 2. Stratégie de sync avec le projet `autodeploy`

Trois options évaluées :

| Option | Avantages | Inconvénients |
|---|---|---|
| **A. Git submodule** | Version explicite | `git submodule update` lourd, contributeurs doivent connaître |
| **B. `ARG VERSION` + `git clone` dans Dockerfile** ✅ | Simple, build reproductible, 1 ligne à bumper | Rebuild manuel à chaque release PS1 |
| **C. Workflow "release-watcher"** | 100% automatique | Plus de YAML CI à maintenir |

**Choix : B + C combinés.**
- `Dockerfile` : `ARG AUTODEPLOY_VERSION=v2.8.0` puis `RUN git clone --branch ${AUTODEPLOY_VERSION} ...`
- Workflow GH Actions `release-watcher.yml` : checke la dernière release du repo `autodeploy` chaque jour (cron), si nouveau tag → ouvre une PR qui bump `AUTODEPLOY_VERSION` dans le Dockerfile + déclenche le build d'image.

**Bénéfice :** chaque release de `autodeploy.ps1` produit automatiquement une nouvelle image `autodeploy-web:2.8.0`, `:2.8.1`, etc. + tag `:latest`.

---

## 3. Modules à créer — Structure du repo

```
autodeploy-web/
├── README.md
├── LICENSE                        # MIT (cohérent avec autodeploy)
├── Dockerfile                     # multi-stage Go + runtime PS
├── docker-compose.yml             # exemple de déploiement
├── .dockerignore
├── .gitignore
├── go.mod / go.sum
├── Makefile                       # build, run, image, test
│
├── cmd/
│   └── autodeploy-web/
│       └── main.go                # entrypoint binaire
│
├── internal/
│   ├── server/
│   │   ├── server.go              # net/http server + routes
│   │   ├── handlers.go            # 6 handlers HTTP
│   │   ├── sse.go                 # Server-Sent Events pour logs live
│   │   └── middleware.go          # logging, recovery
│   ├── job/
│   │   ├── manager.go             # registry in-memory des jobs
│   │   ├── job.go                 # struct Job + état (pending/running/done/failed)
│   │   └── runner.go              # exec pwsh, capture stdout, gestion timeout
│   ├── config/
│   │   ├── schema.go              # struct Go = miroir 1:1 du JSON PS1
│   │   ├── defaults.go            # mêmes defaults que le PS1
│   │   ├── validate.go            # validation côté serveur (IP, password, MFA, GUID)
│   │   └── store.go               # CRUD presets sur disque (/data/configs/*.json)
│   └── views/                     # templates HTMX
│       ├── layout.html
│       ├── form.html              # le formulaire principal
│       ├── job.html               # détail job + log live
│       ├── jobs.html              # liste
│       └── partials/              # fragments HTMX (chargés à la demande)
│           ├── network.html       # change selon UseDHCP
│           ├── via-options.html   # visible si ApplianceType ∈ VIA*
│           └── log-line.html      # ligne SSE
│
├── web/
│   ├── static/                    # CSS, JS minimal, favicon
│   │   ├── tailwind.css           # build CDN-free optionnel
│   │   ├── app.js                 # Alpine.js bundlé
│   │   └── htmx.min.js
│   └── assets/                    # screenshots, logo
│
├── scripts/
│   └── wsl-wrapper.sh             # shim "wsl" → forward au binaire natif
│
├── samples/
│   ├── production-config.json     # copie depuis autodeploy/
│   ├── vsa-13.1-ha.json
│   └── via-hardened-repo.json
│
├── docs/
│   ├── migration-from-ps1.md      # "j'ai un JSON, comment je l'importe ?"
│   ├── architecture.md            # diagramme container + flux
│   ├── deployment.md              # docker compose, traefik, etc.
│   └── screenshots/
│
└── .github/
    └── workflows/
        ├── build.yml              # build + push image sur tag
        ├── ci.yml                 # tests Go + go vet
        └── release-watcher.yml    # cron daily : check autodeploy release
```

---

## 4. Le formulaire web (remplace l'édition JSON)

Sections groupées (toggle pliables), validation HTML5 + Alpine.js + re-validation serveur.

| Section | Champs | Particularités UI |
|---|---|---|
| **Appliance** | ApplianceType (radio VSA/VIA/VIAVMware/VIAHR), SourceISO (dropdown ← `ls /data/iso`), OutputISO, InPlace, CFGOnly, GrubTimeout, CreateBackup, CleanupCFGFiles | Le choix d'ApplianceType pilote la visibilité de plusieurs sections (HTMX swap) |
| **Régional** | KeyboardLayout (datalist fr/en/de/es/it/...), Timezone (datalist IANA), Hostname | Hostname ≤15 char |
| **Réseau** | UseDHCP (toggle) → masque/affiche StaticIP/Subnet/Gateway/DNSServers (tag input multi) | Switch HTMX qui change le partial network.html |
| **Comptes Veeam** | VeeamAdminPassword + VeeamSoPassword (eye toggle + bouton "Generate compliant"), VeeamAdminIsMfaEnabled + secret (bouton "Generate base32"), idem SO, VeeamSoRecoveryToken (bouton "New GUID"), VeeamSoIsEnabled | Générateurs côté JS (pas besoin de round-trip serveur) |
| **NTP** | NtpServer (tag input → array), NtpRunSync | Au moins 1 serveur |
| **VSA 13.1** *(visible si ApplianceType=VSA)* | ExternalManagersInstallationEnabled + Timeout, HighAvailabilityEnabled + Timeout | Timeout 60-86400s, désactivé si parent off |
| **Monitoring** | NodeExporter, NodeExporterTLSEnabled | Champs grisés + tooltip si !VSA |
| **VBR Tuning** *(VSA only)* | LicenseVBRTune, LicenseFile (dropdown ← `ls /data/license`), SyslogServer | Upload .lic depuis l'UI si dossier vide |
| **VCSP** *(VSA only)* | VCSPConnection + URL + login + password | URL validée |
| **Restore Config** | RestoreConfig, ConfigPasswordSo | Vérifie présence `/data/conf/conftoresto.bco` |
| **VIA only** *(visible si ApplianceType ∈ VIA*)* | VIASingleDisk | Warning rouge "wipe entier du disque" |
| **Debug** | Debug | Bandeau rouge "ne pas utiliser en prod" |

**Boutons (haut du form) :**
- 📂 `Load preset` → dropdown peuplé depuis `GET /configs` → précharge le form
- 💾 `Save as preset` → POST /configs avec nom
- ⬇️ `Export JSON` → télécharge la config (compat 100% avec `autodeploy.ps1`)
- ⬆️ `Import JSON` → upload un JSON existant → précharge le form
- 👁️ `Preview kickstart` → lance le PS1 avec `CFGOnly=true` dans un job éphémère, affiche le `vbr-ks.cfg` généré
- 🚀 `Generate ISO` → crée le job → redirige vers `/jobs/{id}`

---

## 5. HTTP handlers (les 6 routes — pas de REST formel)

| Méthode + Route | Rôle | Réponse |
|---|---|---|
| `GET /` | Page formulaire principale | HTML complet (layout.html + form.html) |
| `POST /jobs` | Crée un job depuis le form | Redirige (303) vers `/jobs/{id}` |
| `GET /jobs/{id}` | Page détail job + iframe SSE | HTML |
| `GET /jobs/{id}/stream` | Stream stdout live du pwsh | `text/event-stream` |
| `GET /jobs/{id}/download` | Stream l'ISO produite | `application/octet-stream` (sendfile, pas chargé en RAM) |
| `GET /configs` / `POST /configs` / `GET /configs/{name}` / `DELETE /configs/{name}` | Gestion presets sur disque | JSON ou HTMX fragment |

**Note :** ces routes servent l'UI HTMX. Si quelqu'un veut scripter, il appelle directement le PS1 — c'est l'avantage de garder le PS1 inchangé.

---

## 6. Phases d'implémentation

| # | Phase | Livrable | Critère "done" |
|---|---|---|---|
| **P0** | Repo bootstrap | Squelette Go + Dockerfile + CI | `docker compose up` sert un "hello" sur 8080 |
| **P1** | Schema config | `internal/config/schema.go` + `defaults.go` + `validate.go` + tests | Roundtrip JSON (samples du PS1) → struct Go → JSON identique |
| **P2** | Form HTMX + presets | `views/form.html` + handlers `/configs` | Form rendu, save/load preset OK |
| **P3** | Job runner | `internal/job/runner.go` + handler `POST /jobs` | Spawn `pwsh autodeploy.ps1` réussi avec un JSON test, ISO générée |
| **P4** | SSE logs live | `internal/server/sse.go` + page `jobs/{id}` | Build d'une VSA visible en temps réel dans le navigateur |
| **P5** | Download ISO + import/export JSON | Stream sendfile + boutons import/export | Round-trip JSON → form → JSON = identité, download 20 GB OK |
| **P6** | Wrappers + release-watcher | `scripts/wsl-wrapper.sh` + `.github/workflows/release-watcher.yml` | Bump auto AUTODEPLOY_VERSION via PR |
| **P7** | Docs + release v1.0 | README, screenshots, image GHCR signée, samples | Tag `v1.0.0`, image dispo, doc migration PS1 |

---

## 7. Risques et points d'attention

1. **WSL dans le PS1** : le PS1 appelle `wsl xorriso ...`. Solution = wrapper `/usr/local/bin/wsl` qui exécute juste `exec "$@"` — le PS1 ne le sait pas et continue à fonctionner. ✅
2. **Taille ISO (15-20 GB)** : aucun stockage dans le filesystem du conteneur. Tout passe par volumes montés. Le download utilise `http.ServeFile` (sendfile syscall) → pas de copie mémoire.
3. **Concurrence builds** : un seul build à la fois (semaphore Go, taille=1) → évite contention disque et collision xorriso. Surchargeable via `WORKER_CONCURRENCY` env var.
4. **PowerShell sur Linux** : confirmé que MS publie `mcr.microsoft.com/powershell` (officiel). Image debian-12-slim ≈ 280 MB, + xorriso/rsync ≈ +50 MB, + binaire Go ≈ +15 MB → image finale ~350 MB.
5. **Permissions volumes** : le PS1 doit pouvoir écrire dans `/data/output`. Le container tourne sous un UID non-root configurable (`PUID/PGID` à la LinuxServer.io).
6. **Logs sensibles** : passwords/MFA secrets pourraient apparaître dans le log PS1. Le runner Go applique un regex de scrubbing avant SSE (mask `*****`).
7. **Jobs perdus au restart** : assumé par l'utilisateur. Pas de DB. Si un build crash, l'utilisateur relance simplement.
8. **Presets JSON sur disque** : pas chiffrés. Dossier `/data/configs` doit être traité comme sensible (peut contenir passwords). Documenté dans le README.

---

## 8. Diff vs plan v1

Ce qui a **disparu** :
- ~~Backend Python FastAPI~~ → Go mono
- ~~React + Vite~~ → HTMX + Alpine.js (no Node build)
- ~~Worker Celery + Redis~~ → goroutines + semaphore
- ~~SQLite~~ → in-memory + filesystem
- ~~Port logique métier PS1 → Python~~ → on appelle le PS1 directement (zéro porting)
- ~~Golden file tests kickstart~~ → inutile, c'est le même PS1
- ~~Auth multi-user~~ → aucune
- ~~Validation intégrité ISO sortie~~ → inutile

**Résultat : ~70% de complexité en moins** par rapport au plan v1, et chaque release du PS1 est automatiquement disponible côté web.

---

## 9. Prochaine étape

Si tu approuves ce plan révisé, je commence **P0 (bootstrap repo)** :
1. Crée la structure de dossiers dans `C:\Users\PleXi-PC\autodeploy-web\`
2. Initialise `go mod`, `.gitignore`, `LICENSE`, `README.md` initial
3. Écrit le `Dockerfile` multi-stage de base
4. Écrit le `docker-compose.yml` d'exemple
5. Crée un `main.go` minimal qui sert "hello" sur 8080
6. Crée un `Makefile` (`make build`, `make run`, `make image`)
7. Crée `.github/workflows/build.yml` (build image sur tag)

Donne-moi le 👍 et je lance P0.
