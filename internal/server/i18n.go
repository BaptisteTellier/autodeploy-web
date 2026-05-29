package server

import "net/http"

// langCookie is the name of the cookie that persists the visitor's language.
const langCookie = "lang"

// defaultLang is used for new visitors (no cookie yet) and as the fallback
// when a translation key is missing in the requested language.
const defaultLang = "en"

// supportedLangs lists the languages we ship translations for. Order matters:
// it drives how many template sets are built at startup.
var supportedLangs = []string{"en", "fr"}

// isSupportedLang reports whether code is one of supportedLangs.
func isSupportedLang(code string) bool {
	for _, l := range supportedLangs {
		if l == code {
			return true
		}
	}
	return false
}

// langFromRequest resolves the active language from the "lang" cookie,
// falling back to defaultLang. We deliberately do NOT sniff Accept-Language:
// the default is English and the visitor opts into French via the flag.
func langFromRequest(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && isSupportedLang(c.Value) {
		return c.Value
	}
	return defaultLang
}

// translate returns the string for key in lang, falling back to defaultLang
// and finally to the key itself (so a missing key is visible, not silent).
func translate(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if m, ok := translations[defaultLang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// translations holds every UI string keyed by "<page>.<name>". English is the
// source of truth; French mirrors it. Job-state tokens (done/running/failed/
// pending/canceled) are intentionally NOT translated — the SSE live-log JS in
// job.html compares the rendered badge text against those literals.
var translations = map[string]map[string]string{
	"en": {
		// --- nav / layout ---
		"nav.new_job":   "New job",
		"nav.workspace": "Workspace",
		"nav.output":    "Output",
		"nav.licenses":  "Licenses",
		"nav.jobs":      "Jobs",
		"nav.settings":  "Settings",
		"footer.pre":    "autodeploy-web — web wrapper around the ",
		"footer.post":   " PowerShell tool.",

		// --- common (shared across pages) ---
		"common.delete":     "Delete",
		"common.selected":   "selected",
		"common.files_word": "file(s)",
		"common.actions":    "Actions",
		"th.id":             "ID",
		"th.appliance":      "Appliance",
		"th.hostname":       "Hostname",
		"th.state":          "State",
		"th.created":        "Created",
		"th.started":        "Started",
		"th.finished":       "Finished",
		"th.exit":           "Exit",
		"th.file":           "File",
		"th.size":           "Size",
		"th.modified":       "Modified",
		"th.modified_on":    "Modified on",
		"th.date":           "Date",
		"th.files":          "Files",
		"th.total_size":     "Total size",
		"th.job":            "Job",
		"th.file_name":      "File name",

		// --- shared JS literals ---
		"js.loading":             "Loading…",
		"js.load_error":          "Loading error",
		"js.error_prefix":        "Error: ",
		"js.rename_error_prefix": "Rename error: ",
		"js.deletions_failed":    "{n} deletion(s) failed.",
		"js.delete":              "Delete",
		"js.delete_q_pre":        "Delete «",
		"js.delete_q_post":       "» ?",

		// --- form.html ---
		"form.errors_title":         "Validation errors:",
		"form.load_preset":          "Load preset",
		"form.select_dash":          "— select —",
		"form.save_as_preset":       "Save current as preset",
		"form.preset_name_ph":       "preset name",
		"form.save":                 "💾 Save",
		"form.export_json":          "⬇️ Export JSON",
		"form.import_json":          "⬆️ Import JSON",
		"form.appliance":            "📦 Appliance",
		"form.appliance_type":       "Appliance type",
		"form.source_iso":           "Source ISO",
		"form.select_iso":           "— select ISO —",
		"form.files_in":             "Files in",
		"form.output_iso":           "Output ISO (optional)",
		"form.output_iso_ph":        "auto: _customized suffix",
		"form.grub_timeout":         "GRUB timeout (s)",
		"form.inplace":              "InPlace modification",
		"form.create_backup":        "Create backup (when InPlace)",
		"form.cleanup_cfg":          "Cleanup temp .cfg files",
		"form.cfg_only":             "CFG-only (no ISO write — for Packer/CloudInit)",
		"form.regional":             "🌍 Regional",
		"form.keyboard_layout":      "Keyboard layout",
		"form.custom_kbd_ph":        "ex: fr-latin9",
		"form.timezone":             "Timezone (IANA)",
		"form.custom_tz_ph":         "ex: America/Cayman",
		"form.hostname":             "Hostname (≤15 char)",
		"form.network":              "🌐 Network",
		"form.use_dhcp":             "Use DHCP",
		"form.static_ip":            "Static IP",
		"form.subnet":               "Subnet mask",
		"form.gateway":              "Gateway",
		"form.dns_servers":          "DNS servers (comma- or newline-separated)",
		"form.veeam_accounts":       "🔐 Veeam accounts",
		"form.admin_legend":         "Admin (veeamadmin)",
		"form.password_mixed15":     "Password (≥15 chars, mixed)",
		"form.mfa_enabled":          "MFA enabled",
		"form.mfa_secret_b32":       "MFA secret (Base32, 16-32 chars)",
		"form.so_legend":            "Security Officer (veeamso)",
		"form.so_enabled":           "SO account enabled",
		"form.password_so":          "Password (≥15 chars, mixed, ≠ admin)",
		"form.mfa_secret_b32_short": "MFA secret (Base32)",
		"form.recovery_token":       "Recovery token (GUID)",
		"form.ntp":                  "⏰ NTP",
		"form.ntp_servers":          "NTP servers (one per line)",
		"form.ntp_sync":             "Run NTP sync at boot (failure ⇒ customisation fails)",
		"form.vsa_options":          "⚙️ VSA 13.1+ options",
		"form.allow_external":       "Allow external managers",
		"form.external_timeout":     "External managers timeout (s, 60-86400)",
		"form.ha_mode":              "High Availability mode",
		"form.ha_timeout":           "HA timeout (s)",
		"form.monitoring":           "📈 Monitoring (VSA only)",
		"form.node_exporter":        "Enable node_exporter (built-in VSA 13.1+)",
		"form.node_exporter_tls":    "TLS on metrics endpoint",
		"form.vbr_tuning":           "🔧 VBR tuning (VSA only)",
		"form.license_vbr_tune":     "Install license + run VBR tuning",
		"form.license_file":         "License file",
		"form.none_dash":            "— none —",
		"form.syslog_server":        "Syslog server (IP/FQDN, optional)",
		"form.vcsp":                 "☁️ VCSP (VSA only)",
		"form.vcsp_connect":         "Connect to VCSP",
		"form.vcsp_url":             "VCSP URL",
		"form.vcsp_login":           "VCSP login",
		"form.vcsp_password":        "VCSP password",
		"form.restore":              "♻️ Configuration restore",
		"form.restore_enable":       "Enable unattended config restore",
		"form.so_config_password":   "SO config password",
		"form.restore_hint_pre":     "Place",
		"form.restore_hint_and":     "and",
		"form.restore_hint_post":    "under",
		"form.via_options":          "🧱 VIA options",
		"form.single_disk":          "Single Disk mode",
		"form.debug":                "🐞 Debug",
		"form.debug_enable":         "⚠️ Enable root + SSH (do not use in production)",
		"form.generate_iso":         "🚀 Generate ISO",
		"form.recent_jobs":          "Recent jobs",
		"form.open_arrow":           "open →",

		// --- jobs.html ---
		"jobs.all":        "All jobs",
		"jobs.open_job":   "open job",
		"jobs.output":     "📂 output",
		"jobs.none":       "No jobs yet.",
		"jobs.create_one": "Create one",

		// --- job.html ---
		"job.job":           "Job",
		"job.source":        "Source",
		"job.output":        "Output",
		"job.view_outputs":  "📂 View outputs",
		"job.import_config": "📥 Import config into new job",
		"job.all_jobs":      "All jobs",
		"job.live_log":      "Live log (autodeploy.ps1)",
		"job.autoscroll":    "autoscroll",

		// --- admin.html ---
		"admin.title":            "⚙️ Settings",
		"admin.ps1_source":       "📜 autodeploy.ps1 — active source",
		"admin.override_active":  "Override active",
		"admin.bakedin_active":   "Baked-in active",
		"admin.bakedin_image":    "🖼️ Baked-in (image)",
		"admin.override_runtime": "📥 Runtime override",
		"admin.version_unknown":  "unknown version",
		"admin.not_available":    "Not available",
		"admin.active":           "✅ active",
		"admin.apply":            "✅ Apply",
		"admin.in_progress":      "⏳ In progress…",
		"admin.download_github":  "🔄 Download from GitHub",
		"admin.downloading":      "⏳ Downloading…",
		"admin.import_ps1":       "⬆️ Import a .ps1",
		"admin.importing":        "⏳ Importing…",
		"admin.footer_pre":       "The override is stored in",
		"admin.footer_mid":       "(persistent volume, survives restarts). You can fetch it from GitHub (",
		"admin.footer_mid2":      ") or",
		"admin.footer_import":    "import your own autodeploy.ps1",
		"admin.footer_mid3":      "(e.g. a fork). The runner uses it first whenever it exists. After a",
		"admin.footer_mid4":      "the override stays intact — click « Apply » with",
		"admin.footer_post":      "selected to remove it and revert to the new image's script.",
		// admin JS literals (tjs)
		"admin.js_bakedin_already":   "Baked-in script already active — no change.",
		"admin.js_bakedin_activated": "Baked-in script activated. Override removed.",
		"admin.js_error":             "Error: ",
		"admin.js_network_error":     "Network error: ",
		"admin.js_no_override":       "No override available — download the latest version below first.",
		"admin.js_override_already":  "Runtime override already active — no change.",
		"admin.js_ps1_updated":       "autodeploy.ps1 updated.",
		"admin.js_http_error":        "HTTP error ",
		"admin.js_ps1_imported":      "autodeploy.ps1 imported.",

		// --- media_workspace.html ---
		"ws.title":            "🗂️ Workspace",
		"ws.upload_iso":       "⬆️ Upload source ISO",
		"ws.uploading":        "⏳ Uploading…",
		"ws.folder_hint_pre":  "Folder",
		"ws.folder_hint_post": "— source ISOs, configs and logs generated by jobs.",
		"ws.no_files_pre":     "No files in",
		"ws.dl":               "⬇️ DL",
		"ws.confirm_delete_n": "Delete {n} file(s)?",

		// --- media_output.html ---
		"out.title":               "📦 Output",
		"out.subtitle_pre":        "One folder per job — generated ISO, kickstart configs, logs. Root:",
		"out.none_pre":            "No output yet. Start a job from",
		"out.none_link":           "the main page",
		"out.none_post":           ".",
		"out.open":                "📂 Open",
		"out.job":                 "🔍 Job",
		"out.confirm_delete_dirs": "Delete {n} job folder(s) and all their files?",
		"out.confirm_delete_one":  "Delete this folder?",

		// --- media_output_job.html ---
		"outjob.view_job":   "🔍 View job",
		"outjob.folder_pre": "Folder",
		"outjob.none":       "No files in this folder (job still running?).",

		// --- media_license.html ---
		"lic.title":            "🔑 License management",
		"lic.upload":           "⬆️ Upload license",
		"lic.folder_hint_pre":  "Folder:",
		"lic.folder_hint_post": "— drop your Veeam .lic files here.",
		"lic.none_pre":         "No .lic file in",
		"lic.none_post":        ".",
		"lic.none_hint":        "Upload one with the button above, or copy it directly on the host into",
		"lic.confirm_delete":   "Permanently delete «",
	},

	"fr": {
		// --- nav / layout ---
		"nav.new_job":   "Nouveau job",
		"nav.workspace": "Workspace",
		"nav.output":    "Output",
		"nav.licenses":  "Licences",
		"nav.jobs":      "Jobs",
		"nav.settings":  "Paramètres",
		"footer.pre":    "autodeploy-web — interface web autour de l'outil PowerShell ",
		"footer.post":   ".",

		// --- common ---
		"common.delete":     "Supprimer",
		"common.selected":   "sélectionné(s)",
		"common.files_word": "fichier(s)",
		"common.actions":    "Actions",
		"th.id":             "ID",
		"th.appliance":      "Appliance",
		"th.hostname":       "Nom d'hôte",
		"th.state":          "État",
		"th.created":        "Créé",
		"th.started":        "Démarré",
		"th.finished":       "Terminé",
		"th.exit":           "Sortie",
		"th.file":           "Fichier",
		"th.size":           "Taille",
		"th.modified":       "Modifié",
		"th.modified_on":    "Modifié le",
		"th.date":           "Date",
		"th.files":          "Fichiers",
		"th.total_size":     "Taille totale",
		"th.job":            "Job",
		"th.file_name":      "Nom du fichier",

		// --- shared JS literals ---
		"js.loading":             "Chargement…",
		"js.load_error":          "Erreur de chargement",
		"js.error_prefix":        "Erreur : ",
		"js.rename_error_prefix": "Erreur renommage : ",
		"js.deletions_failed":    "{n} suppression(s) ont échoué.",
		"js.delete":              "Supprimer",
		"js.delete_q_pre":        "Supprimer «",
		"js.delete_q_post":       "» ?",

		// --- form.html ---
		"form.errors_title":         "Erreurs de validation :",
		"form.load_preset":          "Charger un preset",
		"form.select_dash":          "— sélectionner —",
		"form.save_as_preset":       "Enregistrer comme preset",
		"form.preset_name_ph":       "nom du preset",
		"form.save":                 "💾 Enregistrer",
		"form.export_json":          "⬇️ Exporter JSON",
		"form.import_json":          "⬆️ Importer JSON",
		"form.appliance":            "📦 Appliance",
		"form.appliance_type":       "Type d'appliance",
		"form.source_iso":           "ISO source",
		"form.select_iso":           "— sélectionner une ISO —",
		"form.files_in":             "Fichiers dans",
		"form.output_iso":           "ISO de sortie (optionnel)",
		"form.output_iso_ph":        "auto : suffixe _customized",
		"form.grub_timeout":         "Délai GRUB (s)",
		"form.inplace":              "Modification InPlace",
		"form.create_backup":        "Créer une sauvegarde (si InPlace)",
		"form.cleanup_cfg":          "Nettoyer les fichiers .cfg temporaires",
		"form.cfg_only":             "CFG uniquement (pas d'écriture ISO — pour Packer/CloudInit)",
		"form.regional":             "🌍 Régional",
		"form.keyboard_layout":      "Disposition clavier",
		"form.custom_kbd_ph":        "ex : fr-latin9",
		"form.timezone":             "Fuseau horaire (IANA)",
		"form.custom_tz_ph":         "ex : America/Cayman",
		"form.hostname":             "Nom d'hôte (≤15 car.)",
		"form.network":              "🌐 Réseau",
		"form.use_dhcp":             "Utiliser le DHCP",
		"form.static_ip":            "IP statique",
		"form.subnet":               "Masque de sous-réseau",
		"form.gateway":              "Passerelle",
		"form.dns_servers":          "Serveurs DNS (séparés par virgule ou retour à la ligne)",
		"form.veeam_accounts":       "🔐 Comptes Veeam",
		"form.admin_legend":         "Admin (veeamadmin)",
		"form.password_mixed15":     "Mot de passe (≥15 car., mixte)",
		"form.mfa_enabled":          "MFA activé",
		"form.mfa_secret_b32":       "Secret MFA (Base32, 16-32 car.)",
		"form.so_legend":            "Security Officer (veeamso)",
		"form.so_enabled":           "Compte SO activé",
		"form.password_so":          "Mot de passe (≥15 car., mixte, ≠ admin)",
		"form.mfa_secret_b32_short": "Secret MFA (Base32)",
		"form.recovery_token":       "Jeton de récupération (GUID)",
		"form.ntp":                  "⏰ NTP",
		"form.ntp_servers":          "Serveurs NTP (un par ligne)",
		"form.ntp_sync":             "Synchroniser NTP au démarrage (échec ⇒ la customisation échoue)",
		"form.vsa_options":          "⚙️ Options VSA 13.1+",
		"form.allow_external":       "Autoriser les managers externes",
		"form.external_timeout":     "Délai managers externes (s, 60-86400)",
		"form.ha_mode":              "Mode haute disponibilité",
		"form.ha_timeout":           "Délai HA (s)",
		"form.monitoring":           "📈 Supervision (VSA uniquement)",
		"form.node_exporter":        "Activer node_exporter (intégré VSA 13.1+)",
		"form.node_exporter_tls":    "TLS sur l'endpoint des métriques",
		"form.vbr_tuning":           "🔧 Réglages VBR (VSA uniquement)",
		"form.license_vbr_tune":     "Installer la licence + lancer le réglage VBR",
		"form.license_file":         "Fichier de licence",
		"form.none_dash":            "— aucun —",
		"form.syslog_server":        "Serveur Syslog (IP/FQDN, optionnel)",
		"form.vcsp":                 "☁️ VCSP (VSA uniquement)",
		"form.vcsp_connect":         "Se connecter au VCSP",
		"form.vcsp_url":             "URL VCSP",
		"form.vcsp_login":           "Identifiant VCSP",
		"form.vcsp_password":        "Mot de passe VCSP",
		"form.restore":              "♻️ Restauration de configuration",
		"form.restore_enable":       "Activer la restauration de config sans surveillance",
		"form.so_config_password":   "Mot de passe de config SO",
		"form.restore_hint_pre":     "Placez",
		"form.restore_hint_and":     "et",
		"form.restore_hint_post":    "dans",
		"form.via_options":          "🧱 Options VIA",
		"form.single_disk":          "Mode disque unique",
		"form.debug":                "🐞 Debug",
		"form.debug_enable":         "⚠️ Activer root + SSH (à ne pas utiliser en production)",
		"form.generate_iso":         "🚀 Générer l'ISO",
		"form.recent_jobs":          "Jobs récents",
		"form.open_arrow":           "ouvrir →",

		// --- jobs.html ---
		"jobs.all":        "Tous les jobs",
		"jobs.open_job":   "ouvrir le job",
		"jobs.output":     "📂 sortie",
		"jobs.none":       "Aucun job pour l'instant.",
		"jobs.create_one": "En créer un",

		// --- job.html ---
		"job.job":           "Job",
		"job.source":        "Source",
		"job.output":        "Sortie",
		"job.view_outputs":  "📂 Voir les sorties",
		"job.import_config": "📥 Importer la config dans un nouveau job",
		"job.all_jobs":      "Tous les jobs",
		"job.live_log":      "Log en direct (autodeploy.ps1)",
		"job.autoscroll":    "défilement auto",

		// --- admin.html ---
		"admin.title":            "⚙️ Paramètres",
		"admin.ps1_source":       "📜 autodeploy.ps1 — source active",
		"admin.override_active":  "Override actif",
		"admin.bakedin_active":   "Baked-in actif",
		"admin.bakedin_image":    "🖼️ Baked-in (image)",
		"admin.override_runtime": "📥 Override runtime",
		"admin.version_unknown":  "version inconnue",
		"admin.not_available":    "Non disponible",
		"admin.active":           "✅ actif",
		"admin.apply":            "✅ Appliquer",
		"admin.in_progress":      "⏳ En cours…",
		"admin.download_github":  "🔄 Télécharger depuis GitHub",
		"admin.downloading":      "⏳ Téléchargement…",
		"admin.import_ps1":       "⬆️ Importer un .ps1",
		"admin.importing":        "⏳ Import…",
		"admin.footer_pre":       "L'override est stocké dans",
		"admin.footer_mid":       "(volume persistant, survit aux redémarrages). Tu peux le récupérer depuis GitHub (",
		"admin.footer_mid2":      ") ou",
		"admin.footer_import":    "importer ton propre autodeploy.ps1",
		"admin.footer_mid3":      "(par exemple un fork). Le runner l'utilise en priorité dès qu'il existe. Après un",
		"admin.footer_mid4":      "l'override reste intact — clique « Appliquer » avec",
		"admin.footer_post":      "sélectionné pour le supprimer et revenir au script de la nouvelle image.",
		// admin JS literals
		"admin.js_bakedin_already":   "Script baked-in déjà actif — aucun changement.",
		"admin.js_bakedin_activated": "Script baked-in activé. Override supprimé.",
		"admin.js_error":             "Erreur : ",
		"admin.js_network_error":     "Erreur réseau : ",
		"admin.js_no_override":       "Aucun override disponible — téléchargez d'abord la dernière version ci-dessous.",
		"admin.js_override_already":  "Override runtime déjà actif — aucun changement.",
		"admin.js_ps1_updated":       "autodeploy.ps1 mis à jour.",
		"admin.js_http_error":        "Erreur HTTP ",
		"admin.js_ps1_imported":      "autodeploy.ps1 importé.",

		// --- media_workspace.html ---
		"ws.title":            "🗂️ Workspace",
		"ws.upload_iso":       "⬆️ Uploader une ISO source",
		"ws.uploading":        "⏳ Upload en cours…",
		"ws.folder_hint_pre":  "Dossier",
		"ws.folder_hint_post": "— ISOs sources, configs et logs générés par les jobs.",
		"ws.no_files_pre":     "Aucun fichier dans",
		"ws.dl":               "⬇️ DL",
		"ws.confirm_delete_n": "Supprimer {n} fichier(s) ?",

		// --- media_output.html ---
		"out.title":               "📦 Output",
		"out.subtitle_pre":        "Un dossier par job — ISO générée, configs kickstart, logs. Racine :",
		"out.none_pre":            "Aucun output pour l'instant. Lancez un job depuis",
		"out.none_link":           "la page principale",
		"out.none_post":           ".",
		"out.open":                "📂 Ouvrir",
		"out.job":                 "🔍 Job",
		"out.confirm_delete_dirs": "Supprimer {n} dossier(s) de job et tous leurs fichiers ?",
		"out.confirm_delete_one":  "Supprimer ce dossier ?",

		// --- media_output_job.html ---
		"outjob.view_job":   "🔍 Voir le job",
		"outjob.folder_pre": "Dossier",
		"outjob.none":       "Aucun fichier dans ce dossier (job encore en cours ?).",

		// --- media_license.html ---
		"lic.title":            "🔑 Gestion licences",
		"lic.upload":           "⬆️ Uploader une licence",
		"lic.folder_hint_pre":  "Dossier :",
		"lic.folder_hint_post": "— déposez vos fichiers .lic Veeam ici.",
		"lic.none_pre":         "Aucun fichier .lic dans",
		"lic.none_post":        ".",
		"lic.none_hint":        "Uploadez-en un via le bouton ci-dessus, ou copiez-le directement sur l'hôte dans",
		"lic.confirm_delete":   "Supprimer définitivement «",
	},
}
