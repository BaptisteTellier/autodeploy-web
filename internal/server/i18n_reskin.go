package server

// Reskin i18n keys reported by the redesign page-reskin pass. They are added
// here (rather than inline in i18n.go) so they can never collide with keys the
// reskin already wrote into the main maps: addReskinKeys only sets a key when it
// is not already present. Added to both en and fr for full parity.
func init() {
	addReskinKeys("en", reskinEN)
	addReskinKeys("fr", reskinFR)
}

func addReskinKeys(lang string, m map[string]string) {
	dst, ok := translations[lang]
	if !ok {
		return
	}
	for k, v := range m {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
}

var reskinEN = map[string]string{
	// media / library pages
	"common.rename":          "Rename",
	"ws.card_title":          "Source ISOs",
	"ws.upload_label":        "Upload source ISO",
	"ws.dl_label":            "Download",
	"out.card_title":         "Job folders",
	"out.open_label":         "Open",
	"out.job_label":          "View job",
	"outjob.card_title":      "Files",
	"outjob.view_job_label":  "View job",
	"outjob.show_link_label": "Direct link",
	"lic.card_title":         "License files",
	"lic.upload_label":       "Upload license",

	// deploy detail / job detail
	"th.role":                 "Role",
	"deploy.elapsed":          "Elapsed",
	"deploy.console_subtitle": "proxied server-side",

	// wizard (6-step rebuild)
	"wiz.page_title":         "Guided ISO wizard",
	"wiz.page_subtitle":      "Same job as the expert form, six clear steps with inline help.",
	"wiz.steplabel.1":        "Source",
	"wiz.steplabel.2":        "System",
	"wiz.steplabel.3":        "Network",
	"wiz.steplabel.4":        "Accounts",
	"wiz.steplabel.5":        "Advanced",
	"wiz.steplabel.6":        "Review",
	"wiz.step_of6":           "Step {s} of 6",
	"wiz.step5_configure":    " · Configure",
	"wiz.required":           "Required",
	"wiz.optional":           "Optional",
	"wiz.disable_option":     "Disable this option",
	"wiz.change_selection":   "Change selection",
	"wiz.build_options":      "Build & output options",
	"wiz.next_label":         "Next",
	"wiz.back_label":         "Back",
	"wiz.skip_label":         "Skip",
	"wiz.switch_expert":      "Switch to expert form",
	"wiz.step2.title":        "System & locale",
	"wiz.step2.subtitle":     "Keyboard, timezone and the appliance hostname.",
	"wiz.step4.title":        "Accounts & security",
	"wiz.step4.subtitle":     "Admin and Security Officer credentials, with MFA.",
	"wiz.so_required_note":   "MFA and a recovery token are required for the Security Officer.",
	"wiz.step5.title":        "Advanced options",
	"wiz.step5.select_hint":  "Pick what you need — Next shows just those to configure. Everything has a safe default, so you can skip.",
	"wiz.step5.config_title": "Configure selected options",
	"wiz.step5.config_hint":  "Only the {n} option(s) you enabled — set their values.",
	"wiz.step6.title":        "Review & generate",
	"wiz.step6.subtitle":     "Confirm the configuration, name a preset, and build.",
	"wiz.adv.ntp.label":      "NTP sync at build",
	"wiz.adv.ntp.desc":       "Run a time sync during the build",
	"wiz.adv.ha.label":       "External managers + High availability",
	"wiz.adv.ha.desc":        "Allow external VBR managers; enable HA mode",
	"wiz.adv.mon.label":      "node_exporter",
	"wiz.adv.mon.desc":       "Expose Prometheus metrics (optional TLS)",
	"wiz.adv.lic.label":      "LicenseVBRTune",
	"wiz.adv.lic.desc":       "Bake a Veeam license into the ISO",
	"wiz.adv.vcsp.label":     "VCSP connection",
	"wiz.adv.vcsp.desc":      "Register with a Veeam Cloud Service Provider",
	"wiz.adv.restore.label":  "Restore config",
	"wiz.adv.restore.desc":   "Restore a saved VBR configuration on first boot",

	// global search
	"search.type.iso_template":    "ISO template",
	"search.type.deploy_template": "Deploy template",
	"search.type.vm":              "Deployed VM",
	"search.type.iso":             "Source ISO",
	"search.type.license":         "License",
	"search.no_results":           "No results",
}

var reskinFR = map[string]string{
	// media / library pages
	"common.rename":          "Renommer",
	"ws.card_title":          "ISOs sources",
	"ws.upload_label":        "Uploader une ISO source",
	"ws.dl_label":            "Télécharger",
	"out.card_title":         "Dossiers de job",
	"out.open_label":         "Ouvrir",
	"out.job_label":          "Voir le job",
	"outjob.card_title":      "Fichiers",
	"outjob.view_job_label":  "Voir le job",
	"outjob.show_link_label": "Lien direct",
	"lic.card_title":         "Fichiers de licence",
	"lic.upload_label":       "Uploader une licence",

	// deploy detail / job detail
	"th.role":                 "Rôle",
	"deploy.elapsed":          "Écoulé",
	"deploy.console_subtitle": "relayé côté serveur",

	// wizard (6-step rebuild)
	"wiz.page_title":         "Assistant ISO guidé",
	"wiz.page_subtitle":      "Le même travail que le formulaire expert, en six étapes claires avec aide intégrée.",
	"wiz.steplabel.1":        "Source",
	"wiz.steplabel.2":        "Système",
	"wiz.steplabel.3":        "Réseau",
	"wiz.steplabel.4":        "Comptes",
	"wiz.steplabel.5":        "Avancé",
	"wiz.steplabel.6":        "Vérification",
	"wiz.step_of6":           "Étape {s} sur 6",
	"wiz.step5_configure":    " · Configurer",
	"wiz.required":           "Requis",
	"wiz.optional":           "Optionnel",
	"wiz.disable_option":     "Désactiver cette option",
	"wiz.change_selection":   "Modifier la sélection",
	"wiz.build_options":      "Options de build et de sortie",
	"wiz.next_label":         "Suivant",
	"wiz.back_label":         "Retour",
	"wiz.skip_label":         "Passer",
	"wiz.switch_expert":      "Passer au formulaire expert",
	"wiz.step2.title":        "Système & localisation",
	"wiz.step2.subtitle":     "Clavier, fuseau horaire et nom d'hôte de l'appliance.",
	"wiz.step4.title":        "Comptes & sécurité",
	"wiz.step4.subtitle":     "Identifiants administrateur et Security Officer, avec MFA.",
	"wiz.so_required_note":   "Le MFA et un jeton de récupération sont requis pour le Security Officer.",
	"wiz.step5.title":        "Options avancées",
	"wiz.step5.select_hint":  "Choisissez ce dont vous avez besoin — Suivant n'affiche que cela à configurer. Tout a une valeur par défaut sûre, vous pouvez passer.",
	"wiz.step5.config_title": "Configurer les options sélectionnées",
	"wiz.step5.config_hint":  "Uniquement les {n} option(s) activée(s) — définissez leurs valeurs.",
	"wiz.step6.title":        "Vérification & génération",
	"wiz.step6.subtitle":     "Confirmez la configuration, nommez un preset et lancez la génération.",
	"wiz.adv.ntp.label":      "Synchronisation NTP au build",
	"wiz.adv.ntp.desc":       "Lancer une synchronisation de l'heure pendant le build",
	"wiz.adv.ha.label":       "Managers externes + Haute disponibilité",
	"wiz.adv.ha.desc":        "Autoriser les managers VBR externes ; activer le mode HA",
	"wiz.adv.mon.label":      "node_exporter",
	"wiz.adv.mon.desc":       "Exposer les métriques Prometheus (TLS optionnel)",
	"wiz.adv.lic.label":      "LicenseVBRTune",
	"wiz.adv.lic.desc":       "Injecter une licence Veeam dans l'ISO",
	"wiz.adv.vcsp.label":     "Connexion VCSP",
	"wiz.adv.vcsp.desc":      "S'enregistrer auprès d'un Veeam Cloud Service Provider",
	"wiz.adv.restore.label":  "Restaurer la config",
	"wiz.adv.restore.desc":   "Restaurer une configuration VBR sauvegardée au premier démarrage",

	// global search
	"search.type.iso_template":    "Modèle ISO",
	"search.type.deploy_template": "Modèle de déploiement",
	"search.type.vm":              "VM déployée",
	"search.type.iso":             "ISO source",
	"search.type.license":         "Licence",
	"search.no_results":           "Aucun résultat",
}
