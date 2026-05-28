# Task Checklist — autodeploy-web

## P0 — Bootstrap ✅
- [x] Structure dossiers
- [x] go.mod (uuid only dep)
- [x] .gitignore / .dockerignore
- [x] LICENSE (MIT)
- [x] README.md
- [x] Dockerfile multi-stage (gobuild + fetch + runtime)
- [x] docker-compose.yml
- [x] Makefile (vendor, build, run, image, test, dev-up)
- [x] main.go (HTTP server + worker bootstrap)
- [x] .github/workflows/build.yml (GHCR multi-arch)
- [x] .github/workflows/ci.yml

## P1 — Config schema ✅
- [x] internal/config/schema.go (FlexBool, FlexStringArray, 40+ fields)
- [x] internal/config/defaults.go (defaults + datalists)
- [x] internal/config/validate.go (IP, password, MFA, GUID, cross-field)
- [x] internal/config/store.go (preset CRUD on disk)
- [x] internal/config/schema_test.go (sample roundtrip, FlexBool, defaults validity)

## P2 — Form HTMX + presets ✅
- [x] internal/server/views/layout.html
- [x] internal/server/views/form.html (all 11 sections)
- [x] internal/server/views/jobs.html
- [x] internal/server/static/app.js (Alpine component + generators + import/export)
- [x] internal/server/static/tailwind.{css,js} placeholder
- [x] internal/server/static/htmx.min.js placeholder
- [x] internal/server/static/alpine.min.js placeholder
- [x] scripts/fetch-vendor.sh (downloads real bundles at build time)
- [x] Handlers GET / POST / GET-by-name / DELETE /configs

## P3 — Job runner ✅
- [x] internal/job/job.go (state + ring buffer + pub/sub)
- [x] internal/job/manager.go (semaphore-gated worker pool)
- [x] internal/job/runner.go (pwsh subprocess, scrubbing, staging)
- [x] Handler POST /jobs
- [x] Handler GET /jobs
- [x] Handler GET /jobs/{id}
- [x] scripts/wsl-wrapper.sh (forwards `wsl xorriso ...` to native)

## P4 — SSE logs ✅
- [x] internal/server/sse.go (event-stream + heartbeat + replay)
- [x] internal/server/views/job.html (EventSource client + autoscroll)
- [x] Log scrubbing in runner.go (password/MFA/recovery token/VCSP)

## P5 — Download + import/export ✅
- [x] Handler GET /jobs/{id}/download (http.ServeContent — sendfile)
- [x] Import JSON form preload (client-side, applyConfigToForm)
- [x] Export JSON download (client-side, formToConfigJSON)

## P6 — Release watcher ✅
- [x] .github/workflows/release-watcher.yml (cron daily + PR bump)
- [x] ARG AUTODEPLOY_VERSION dans Dockerfile

## P7 — Docs + release ✅
- [x] README final (quick start, architecture diagram, env vars)
- [x] docs/architecture.md (container layout + request flow)
- [x] docs/migration-from-ps1.md (round-trip docs)
- [x] docs/deployment.md (compose, plain docker, reverse proxy, sizing)
- [x] samples/production-config.json
- [x] samples/vsa-13.1-ha.json
- [x] samples/via-hardened-repo.json
