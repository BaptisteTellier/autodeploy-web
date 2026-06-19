package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/BaptisteTellier/autodeploy-web/internal/craftapi"
)

// handleCraftAPI renders the Craft API form page.
func (s *Server) handleCraftAPI(w http.ResponseWriter, r *http.Request) {
	kinds := catalogViews()
	kindsJSON, _ := json.Marshal(kinds)

	var presetNames []string
	if s.deps.CraftPresets != nil {
		presetNames, _ = s.deps.CraftPresets.List()
	}
	if presetNames == nil {
		presetNames = []string{}
	}

	s.render(w, r, "views/craft_api.html", map[string]any{
		"Kinds":        kinds,
		"KindsJSON":    template.JS(kindsJSON), //nolint:gosec — JSON of our own structs, rendered in a <script> JSON context
		"CraftPresets": presetNames,
	})
}

// handleCraftAPIRender parses the POSTed form into a craftapi.Spec, renders
// both PowerShell and curl scripts, and returns them as JSON.
func (s *Server) handleCraftAPIRender(w http.ResponseWriter, r *http.Request) {
	// The page submits via FormData → multipart/form-data, so ParseMultipartForm
	// is required: r.ParseForm() does NOT read a multipart body and (by setting
	// r.Form) would stop r.FormValue from lazily parsing it, leaving every field
	// empty. ErrNotMultipart is tolerated so a urlencoded POST still works.
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad form: " + err.Error()})
		return
	}

	spec := craftapi.Spec{
		Username:         craftStrDefault(r.FormValue("username"), "veeamadmin"),
		Password:         r.FormValue("password"),
		APIVersion:       craftStrDefault(strings.TrimSpace(r.FormValue("api_version")), "1.3-rev2"),
		RepoPath:         craftStrDefault(strings.TrimSpace(r.FormValue("repo_path")), "/var/lib/veeam/backups"),
		ImmutableDays:    craftAtoiDefault(r.FormValue("repo_immutable_days"), 7),
		ClusterDNSName:   strings.TrimSpace(r.FormValue("cluster_dns")),
		ClusterEndpoint:  strings.TrimSpace(r.FormValue("cluster_vip")),
		License:          r.FormValue("license") != "",
		LicenseB64:       strings.TrimSpace(r.FormValue("license_b64")),
		NodeExporter:     r.FormValue("node_exporter") != "",
		NodeExporterTLS:  r.FormValue("node_exporter_tls") != "",
		NodeExporterUser: strings.TrimSpace(r.FormValue("node_exporter_user")),
		NodeExporterPass: r.FormValue("node_exporter_pass"),
		SyslogServer:     strings.TrimSpace(r.FormValue("syslog_server")),
		SyslogPort:       craftAtoiDefault(r.FormValue("syslog_port"), 514),
		SyslogProtocol:   craftStrDefault(strings.TrimSpace(r.FormValue("syslog_protocol")), "Udp"),
		S3: craftapi.S3{
			Enabled:         r.FormValue("s3") != "",
			Compatible:      r.FormValue("s3_compatible") != "",
			Name:            strings.TrimSpace(r.FormValue("s3_name")),
			ServicePoint:    strings.TrimSpace(r.FormValue("s3_endpoint")),
			Region:          strings.TrimSpace(r.FormValue("s3_region")),
			Bucket:          strings.TrimSpace(r.FormValue("s3_bucket")),
			Folder:          strings.TrimSpace(r.FormValue("s3_folder")),
			AccessKey:       strings.TrimSpace(r.FormValue("s3_access_key")),
			SecretKey:       r.FormValue("s3_secret_key"),
			ImmutableDays:   craftAtoiDefault(r.FormValue("s3_immutable_days"), 0),
			MountServerNode: strings.TrimSpace(r.FormValue("s3_mount_node")),
			OverwriteOwner:  r.FormValue("s3_overwrite") != "",
		},
	}

	// Parse nodes: iterate i=0.. while node_<i>_role is non-empty.
	firstVSAIP := ""
	for i := 0; ; i++ {
		role := r.FormValue("node_" + strconv.Itoa(i) + "_role")
		if role == "" {
			break
		}
		ip := strings.TrimSpace(r.FormValue("node_" + strconv.Itoa(i) + "_ip"))
		hostname := strings.TrimSpace(r.FormValue("node_" + strconv.Itoa(i) + "_hostname"))
		pairing := craftStrDefault(strings.TrimSpace(r.FormValue("node_"+strconv.Itoa(i)+"_pairing")), "000000")

		spec.Nodes = append(spec.Nodes, craftapi.Node{
			Role:        role,
			IP:          ip,
			Hostname:    hostname,
			PairingCode: pairing,
		})

		// BaseURL is derived from the first VSA node's IP.
		if firstVSAIP == "" && len(role) >= 3 && role[:3] == "VSA" && ip != "" {
			firstVSAIP = ip
		}
	}

	if spec.BaseURL == "" && firstVSAIP != "" {
		spec.BaseURL = "https://" + firstVSAIP + ":9419"
	}

	ps, errPS := craftapi.RenderPowerShell(spec)
	if errPS != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": errPS.Error()})
		return
	}

	curl, errCurl := craftapi.RenderCurl(spec)
	if errCurl != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": errCurl.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"powershell": ps,
		"curl":       curl,
	})
}

// craftStrDefault returns s if non-empty, otherwise def.
func craftStrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// craftAtoiDefault parses s as an integer, returning def on error or zero.
func craftAtoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// handleListCraftPresets returns the saved Craft API templates as a JSON array
// of names.
func (s *Server) handleListCraftPresets(w http.ResponseWriter, r *http.Request) {
	if s.deps.CraftPresets == nil {
		writeJSON(w, []string{})
		return
	}
	names, err := s.deps.CraftPresets.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, names)
}

// handleSaveCraftPreset saves the POSTed craft form as a named template.
// The non-secret fields are stored; password and s3_secret_key are excluded.
func (s *Server) handleSaveCraftPreset(w http.ResponseWriter, r *http.Request) {
	lang := langFromRequest(r)
	if s.deps.CraftPresets == nil {
		http.Error(w, translate(lang, "deploy.err_unavailable"), http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, translate(lang, "craft.tpl_name_required"), http.StatusUnprocessableEntity)
		return
	}

	// Collect all non-secret craft form fields.
	fields := craftFormFields(r)

	if err := s.deps.CraftPresets.Save(name, fields); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLoadCraftPreset returns the saved fields for a named template as JSON.
func (s *Server) handleLoadCraftPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.CraftPresets == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	fields, err := s.deps.CraftPresets.Load(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, fields)
}

// handleDeleteCraftPreset removes a saved Craft API template.
func (s *Server) handleDeleteCraftPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.CraftPresets == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.deps.CraftPresets.Delete(r.PathValue("name")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// craftFormFields collects the non-secret Craft API form values into a flat
// map[string]string suitable for PresetStore.Save. Secrets (password,
// s3_secret_key) are deliberately excluded.
func craftFormFields(r *http.Request) map[string]string {
	get := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	fields := map[string]string{}

	setIfNonEmpty := func(keys ...string) {
		for _, k := range keys {
			if v := get(k); v != "" {
				fields[k] = v
			}
		}
	}
	setIfNonEmpty(
		"username",
		"api_version",
		"repo_path",
		"repo_immutable_days",
		"node_exporter_user",
		"syslog_server",
		"syslog_port",
		"syslog_protocol",
		"s3_name",
		"s3_endpoint",
		"s3_region",
		"s3_bucket",
		"s3_folder",
		"s3_access_key",
		"s3_immutable_days",
		"s3_mount_node",
		"cluster_dns",
		"cluster_vip",
		"license_b64",
	)

	// Boolean/checkbox fields — store "on" when checked, omit when off.
	for _, k := range []string{"license", "node_exporter", "node_exporter_tls", "s3", "s3_compatible", "s3_overwrite"} {
		if r.FormValue(k) != "" {
			fields[k] = "on"
		}
	}

	// Node fields — collect as many node_<i>_* as the form supplies.
	for i := 0; ; i++ {
		role := r.FormValue("node_" + strconv.Itoa(i) + "_role")
		if role == "" {
			break
		}
		fields["node_"+strconv.Itoa(i)+"_role"] = role
		for _, suffix := range []string{"ip", "hostname", "pairing"} {
			if v := get("node_" + strconv.Itoa(i) + "_" + suffix); v != "" {
				fields["node_"+strconv.Itoa(i)+"_"+suffix] = v
			}
		}
	}

	return fields
}
