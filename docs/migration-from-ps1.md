# Migrating from autodeploy.ps1 (CLI) to autodeploy-web

If you already use [autodeploy.ps1](https://github.com/BaptisteTellier/autodeploy)
with JSON configs, the migration is a no-op:

1. Start `autodeploy-web` (see [README](../README.md#quick-start)).
2. Open the form.
3. Click **⬆️ Import JSON**, pick your existing `production-config.json`.
4. The form is preloaded with every field. Submit.

## What is unchanged

- The set of JSON keys is **identical** — `autodeploy-web` consumes the same schema as the upstream PS1 v2.8.
- The output ISO is bit-for-bit identical to what you'd get running the PS1 locally with the same JSON (the same `autodeploy.ps1` is executed inside the container).
- All side files (`license/`, `conf/`, `offline_repo/` if needed in older versions) work the same — they're mounted as volumes.

## What is different

| Topic | CLI | Web |
|---|---|---|
| Where you run | Windows + PowerShell 7 + WSL | Any host with Docker |
| How you point at the ISO | put it next to the script | drop it in `./data/iso/` |
| How you give the config | `-ConfigFile production.json` | form in browser, or import JSON |
| How you read logs | tail `ISO_Customization.log` | live SSE in browser |
| Where the customised ISO lands | same directory as source | `./data/output/` (downloadable from UI) |

## Round-trip: web → CLI

The UI's **⬇️ Export JSON** button produces a file you can run with the
original PS1 unchanged:

```powershell
# On a Windows box with the original autodeploy.ps1
.\autodeploy.ps1 -ConfigFile exported-from-web.json
```

No conversion needed.

## Edge cases

- **`CFGOnly=true`** still works (the PS1 writes the .cfg files in
  `/data/iso/` instead of producing an ISO). Useful for piping into
  Packer.
- **`InPlace=true`** modifies the source ISO directly. Volumes are
  read-write so this works, but **prefer `false`** when running from the
  container so the original stays clean.
- **`Debug=true`** still enables root + SSH on the appliance. The web UI
  warns about it but does not block.
