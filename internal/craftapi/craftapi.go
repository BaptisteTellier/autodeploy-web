// Package craftapi turns a user-supplied wiring spec into the ordered Veeam
// REST call sequence and renders it as runnable PowerShell and curl (bash)
// scripts. It is a render-only package — no live execution happens here.
//
// The call order and JSON bodies mirror internal/wiring (Wire method) and
// internal/veeam (every client method) exactly so the rendered scripts stay
// in sync with the real deployer.
package craftapi

import "github.com/BaptisteTellier/autodeploy-web/internal/veeam"

// Node is one appliance in the topology.
type Node struct {
	// Role is one of "VSA", "VSA-2", "VIA-HR", "VIA-Proxy", etc.
	// isVSA / isHR / isProxy follow the same prefix/contains logic as wiring.go.
	Role        string
	IP          string
	Hostname    string
	PairingCode string // defaults to "000000" when empty
}

// S3 holds optional object-storage repository settings.
type S3 struct {
	Enabled         bool
	Name            string
	Compatible      bool   // true => S3Compatible; false => AmazonS3
	ServicePoint    string // S3Compatible only
	Region          string
	Bucket          string
	Folder          string
	AccessKey       string
	SecretKey       string
	ImmutableDays   int
	MountServerNode string // hostname/IP of the VIA-Proxy node to pin as mount server; "" = auto
	OverwriteOwner  bool   // ?overwriteOwner=true on repository POST
}

// Spec is the complete user-supplied wiring specification.
type Spec struct {
	BaseURL    string // e.g. "https://192.168.1.10:9419"; derived from first VSA when empty
	Username   string // defaults to "veeamadmin"
	Password   string
	APIVersion string // x-api-version header; defaults to "1.3-rev2"

	Nodes []Node

	License    bool   // whether to install a license
	LicenseB64 string // canonical base64 of the .lic file; if empty the placeholder is used

	NodeExporter     bool
	NodeExporterTLS  bool
	NodeExporterUser string
	NodeExporterPass string

	SyslogServer   string
	SyslogPort     int
	SyslogProtocol string

	S3 S3

	// HA fields — populated when there are >= 2 VSA nodes.
	ClusterDNSName  string
	ClusterEndpoint string

	// Hardened-repo settings.
	RepoPath      string // default "/var/lib/veeam/backups"
	ImmutableDays int    // default 7
}

// Capture describes one JSON-path extraction from the response of a Step.
// Var is the variable name to assign; Expr is the jq-style path (e.g. "id",
// "data[0].id", "fingerprint", "certificateUpload.certificate").
// The special Expr "__object__" captures the entire response object.
type Capture struct {
	Var  string
	Expr string
}

// Step is one REST call in the ordered sequence.
type Step struct {
	Comment      string    // human explanation, emitted as a script comment
	Method       string    // GET / POST / PUT / DELETE
	Path         string    // /api/v1/...  (may include ?query)
	Body         any       // JSON body or nil
	Captures     []Capture // variables to extract from the response
	WaitSession  bool      // response carries an async session id — emit a wait loop
	WaitVar      string    // variable holding the session id to wait on (e.g. "host0_id")
	OptionalWait bool      // when true, a failed session is logged but does not abort the script
	// Kind marks special rendering modes.
	// "configBackupRedirect" — rendered as a read-modify-write (GET+PUT).
	// "wireViaHr"            — high-level HR node wiring via helper functions.
	// "wireViaProxy"         — high-level proxy node wiring via helper functions.
	// ""                     — normal step.
	Kind string

	// Fields used by wireViaHr / wireViaProxy kinds.
	HostVar       string // PS/bash variable to assign the resolved managed-server id
	RepoVar       string // PS/bash variable to assign the resolved repo id (HR only)
	IP            string // node IP address
	Role          string // node role label (used as description on managed-server add)
	Pairing       string // handshake/pairing code
	RepoName      string // hardened repo name (HR only)
	RepoPath      string // repo filesystem path (HR only)
	ImmutableDays int    // immutability days (HR only)
}

// licenseB64Placeholder is emitted in the install body when LicenseB64 is
// not provided so the rendered script is syntactically valid but clearly
// requires the user to substitute their own value.
const licenseB64Placeholder = "<BASE64_OF_YOUR_LIC>"

const (
	defaultUsername      = "veeamadmin"
	defaultAPIVersion    = "1.3-rev2"
	defaultRepoPath      = "/var/lib/veeam/backups"
	defaultImmutableDays = 7
	defaultPairingCode   = "000000"
	defaultSyslogPort    = 514
	defaultSyslogProto   = "Udp"
)

func applyDefaults(s *Spec) {
	if s.Username == "" {
		s.Username = defaultUsername
	}
	if s.APIVersion == "" {
		s.APIVersion = defaultAPIVersion
	}
	if s.RepoPath == "" {
		s.RepoPath = defaultRepoPath
	}
	if s.ImmutableDays <= 0 {
		s.ImmutableDays = defaultImmutableDays
	}
	if s.SyslogPort <= 0 {
		s.SyslogPort = defaultSyslogPort
	}
	if s.SyslogProtocol == "" {
		s.SyslogProtocol = defaultSyslogProto
	}
	// Derive BaseURL from the first VSA node so every caller (Plan AND the
	// renderers) sees it — Plan operating on a by-value copy must not be the only
	// place this happens, or RenderPowerShell/RenderCurl emit the placeholder.
	if s.BaseURL == "" {
		for _, n := range s.Nodes {
			if isVSA(n.Role) && n.IP != "" {
				s.BaseURL = "https://" + n.IP + ":9419"
				break
			}
		}
	}
}

func isVSA(role string) bool {
	return len(role) >= 3 && role[:3] == "VSA"
}

func isHR(role string) bool {
	for i := 0; i+1 < len(role); i++ {
		if role[i] == 'H' && role[i+1] == 'R' {
			return true
		}
	}
	return false
}

func isProxy(role string) bool {
	const p = "Proxy"
	for i := 0; i+len(p) <= len(role); i++ {
		if role[i:i+len(p)] == p {
			return true
		}
	}
	return false
}

func pairingCode(n Node) string {
	if n.PairingCode != "" {
		return n.PairingCode
	}
	return defaultPairingCode
}

// Plan produces the ordered REST call sequence mirroring Wire() in
// internal/wiring/wiring.go.  The caller receives a []Step ready to be
// rendered by RenderPowerShell or RenderCurl.
func Plan(s Spec) []Step {
	applyDefaults(&s)

	var steps []Step

	add := func(st Step) { steps = append(steps, st) }

	// -------------------------------------------------------------------------
	// 1. License (optional) — GET first, POST only when not already valid.
	// -------------------------------------------------------------------------
	if s.License {
		add(Step{
			Comment: "Check current license status — skip install if already valid.",
			Method:  "GET",
			Path:    "/api/v1/license",
		})
		licB64 := licenseB64Placeholder
		if s.LicenseB64 != "" {
			// Normalise exactly like the live wiring: raw XML → base64, line-wrapped
			// base64 → canonical, BOM/whitespace stripped. Embedding the pasted value
			// verbatim is what causes VBR's "Data at the root level is invalid".
			if norm, err := veeam.EncodeLicensePayload([]byte(s.LicenseB64)); err == nil {
				licB64 = norm
			} else {
				licB64 = s.LicenseB64
			}
		}
		add(Step{
			Comment: "Install the Veeam license (base64-encoded .lic file content).",
			Method:  "POST",
			Path:    "/api/v1/license/install",
			Body:    map[string]any{"license": licB64},
		})
	}

	// -------------------------------------------------------------------------
	// 2. Per VIA node — one high-level step per node, mirroring the per-node
	//    goroutine in Wire(): waitNodeUp → find-or-add managed server →
	//    (HR) find-or-create repo with createWithComponents retry
	//    (Proxy) createWithComponents retry.
	// -------------------------------------------------------------------------
	// Separate VSA and VIA nodes, mirroring the wiring split.
	var vsas, vias []Node
	for _, n := range s.Nodes {
		if isVSA(n.Role) {
			vsas = append(vsas, n)
		} else {
			vias = append(vias, n)
		}
	}

	// Precompute HR hostname counts to reproduce wiring.go's disambiguation:
	// when >1 HR nodes share a hostname, append -IP to make the repo name unique.
	hrNameCount := map[string]int{}
	for _, n := range vias {
		if isHR(n.Role) {
			hrNameCount[nodeName(n)]++
		}
	}

	// Track the first HR node's vias index so the HA configBackup step can
	// reference its repo var — mirrors wiring.go's "lowest-index HR" logic.
	firstHRIdx := -1

	for i, n := range vias {
		hostVar := hostVarName(i)

		switch {
		case isHR(n.Role):
			// Disambiguate repo name exactly like wiring.go.
			repoName := "HR-" + nodeName(n)
			if hrNameCount[nodeName(n)] > 1 {
				repoName += "-" + n.IP
			}
			add(Step{
				Comment:       "Wire " + n.Role + " " + n.IP + " — wait for node, register host (find-before-add), create hardened repo (with component-update retry).",
				Kind:          "wireViaHr",
				HostVar:       hostVar + "_id",
				RepoVar:       repoVarName(i) + "_id",
				IP:            n.IP,
				Role:          n.Role,
				Pairing:       pairingCode(n),
				RepoName:      repoName,
				RepoPath:      s.RepoPath,
				ImmutableDays: s.ImmutableDays,
			})
			if firstHRIdx == -1 {
				firstHRIdx = i
			}
		case isProxy(n.Role):
			add(Step{
				Comment: "Wire " + n.Role + " " + n.IP + " — wait for node, register host (find-before-add), register VMware proxy (with component-update retry).",
				Kind:    "wireViaProxy",
				HostVar: hostVar + "_id",
				IP:      n.IP,
				Role:    n.Role,
				Pairing: pairingCode(n),
			})
		}
	}

	// -------------------------------------------------------------------------
	// 3. HA block — only when >= 2 VSA nodes.
	// -------------------------------------------------------------------------
	if len(vsas) >= 2 {
		primary := vsas[0]
		secondary := vsas[1]

		// Config backup target is the FIRST (lowest-index) HR repo var,
		// matching wiring.go's "pick the lowest-index HR as the HA config-backup target".
		haRepoVar := "$repo0_id" // fallback (should not happen if HR nodes exist)
		if firstHRIdx >= 0 {
			haRepoVar = "$" + repoVarName(firstHRIdx) + "_id"
		}

		// Config backup redirect — rendered as read-modify-write.
		add(Step{
			Comment: "Redirect VBR configuration backup to the hardened repository (read-modify-write).",
			Kind:    "configBackupRedirect",
			Method:  "PUT",
			Path:    "/api/v1/configBackup",
			// Body holds the target repo id expression for use by the renderer.
			Body: map[string]any{"backupRepositoryId": haRepoVar},
		})

		// Find & delete Default Backup Repository.
		add(Step{
			Comment:  "Find the Default Backup Repository id (needed to delete it before HA creation).",
			Method:   "GET",
			Path:     "/api/v1/backupInfrastructure/repositories?nameFilter=Default+Backup+Repository&limit=50",
			Captures: []Capture{{Var: "defaultRepo_id", Expr: "data[0].id"}},
		})
		add(Step{
			Comment: "Delete the Default Backup Repository (required by the HA cluster setup).",
			Method:  "DELETE",
			Path:    "/api/v1/backupInfrastructure/repositories/$defaultRepo_id",
		})

		// HA credentials for secondary.
		dns := s.ClusterDNSName
		if dns == "" && primary.Hostname != "" {
			dns = primary.Hostname
		}
		add(Step{
			Comment: "Create Linux credentials for the secondary VSA node (" + secondary.IP + ").",
			Method:  "POST",
			Path:    "/api/v1/credentials",
			Body: map[string]any{
				"type":               "Linux",
				"username":           s.Username,
				"password":           "$Password",
				"description":        "HA secondary node",
				"authenticationType": "Password",
			},
			Captures: []Capture{{Var: "haCred_id", Expr: "id"}},
		})

		// Connection certificate for secondary (cert for HA cluster body).
		add(Step{
			Comment: "Retrieve the SSH certificate for the secondary VSA (" + secondary.IP + ") — required by the HA cluster body.",
			Method:  "POST",
			Path:    "/api/v1/connectionCertificate",
			Body: map[string]any{
				"serverName":    secondary.IP,
				"type":          "LinuxHost",
				"credentialsId": "$haCred_id",
			},
			Captures: []Capture{{Var: "haCert_cert", Expr: "certificateUpload.certificate"}},
		})

		// Create HA cluster.
		add(Step{
			Comment:     "Create the 2-node HA cluster.",
			Method:      "POST",
			Path:        "/api/v1/highAvailabilityCluster",
			Body:        createHAClusterBody(primary.IP, secondary.IP, dns, s.ClusterEndpoint),
			Captures:    []Capture{{Var: "ha_sess", Expr: "id"}},
			WaitSession: true,
			WaitVar:     "ha_sess",
		})
	}

	// -------------------------------------------------------------------------
	// 4. Advanced: node_exporter.
	// -------------------------------------------------------------------------
	if s.NodeExporter {
		authType := "None"
		auth := map[string]any{"type": authType}
		if s.NodeExporterUser != "" {
			auth = map[string]any{
				"type":     "UsernamePassword",
				"username": s.NodeExporterUser,
				"password": "$NeBasicPass",
			}
		}
		add(Step{
			Comment: "Enable the Prometheus node_exporter metrics endpoint.",
			Method:  "PUT",
			Path:    "/api/v1/generalOptions/nodeExporterSettings",
			Body: map[string]any{
				"metricsSharingEnabled": true,
				"tlsEnabled":            s.NodeExporterTLS,
				"auth":                  auth,
			},
		})
		if s.NodeExporterUser != "" {
			add(Step{
				Comment: "Set basic-auth credentials for node_exporter.",
				Method:  "POST",
				Path:    "/api/v1/generalOptions/nodeExporterSettings/setBasicAuth",
				Body: map[string]any{
					"username": s.NodeExporterUser,
					"password": "$NeBasicPass",
				},
			})
		}
	}

	// -------------------------------------------------------------------------
	// 5. Advanced: syslog.
	// -------------------------------------------------------------------------
	if s.SyslogServer != "" {
		add(Step{
			Comment: "Configure syslog event forwarding to " + s.SyslogServer + ".",
			Method:  "PUT",
			Path:    "/api/v1/generalOptions/eventForwarding",
			Body: map[string]any{
				"syslogServer": map[string]any{
					"serverName":        s.SyslogServer,
					"port":              s.SyslogPort,
					"transportProtocol": s.SyslogProtocol,
				},
			},
		})
	}

	// -------------------------------------------------------------------------
	// 6. Advanced: S3 repository.
	// -------------------------------------------------------------------------
	if s.S3.Enabled {
		// 6a. Cloud credentials — secrets referenced via preamble variables.
		add(Step{
			Comment: "Create cloud (S3) credentials.",
			Method:  "POST",
			Path:    "/api/v1/cloudCredentials",
			Body: map[string]any{
				"type":        "Amazon",
				"accessKey":   "$S3AccessKey",
				"secretKey":   "$S3SecretKey",
				"description": "autodeploy S3 " + s.S3.Name,
			},
			Captures: []Capture{{Var: "s3Cred_id", Expr: "id"}},
		})

		// 6b. Create folder (S3-compatible + folder set).
		if s.S3.Compatible && s.S3.Folder != "" {
			add(Step{
				Comment: "Create the S3-compatible folder (VBR only opens existing folders; this is the equivalent of the GUI 'New Folder' button).",
				Method:  "POST",
				Path:    "/api/v1/cloudBrowser/newFolder",
				Body: map[string]any{
					"credentialsId":   "$s3Cred_id",
					"serviceType":     "S3Compatible",
					"newFolderName":   s.S3.Folder,
					"connectionPoint": s.S3.ServicePoint,
					"regionId":        s.S3.Region,
					"bucketName":      s.S3.Bucket,
				},
			})
		}

		// 6c. Resolve the mount-server host id — both for the repo body and for
		// the component-update retry below. Pin the chosen node when set;
		// otherwise fall back to the primary VSA, the implicit mount server on a
		// VSA-only deploy. Mirrors internal/wiring applyAdvanced.
		mountServerIDExpr := ""
		retryHostVar := ""
		if s.S3.MountServerNode != "" {
			mountServerIDExpr = "$mountSrv_id"
			retryHostVar = "mountSrv_id"
			add(Step{
				Comment:  "Find the managed server id for the S3 mount server node (" + s.S3.MountServerNode + ").",
				Method:   "GET",
				Path:     "/api/v1/backupInfrastructure/managedServers?nameFilter=" + s.S3.MountServerNode + "&limit=10",
				Captures: []Capture{{Var: "mountSrv_id", Expr: "data[0].id"}},
			})
		} else if len(vsas) > 0 && vsas[0].IP != "" {
			retryHostVar = "s3Host_id"
			add(Step{
				Comment:  "Find the primary VSA managed-server id (implicit S3 mount server) for the pending-components retry.",
				Method:   "GET",
				Path:     "/api/v1/backupInfrastructure/managedServers?nameFilter=" + vsas[0].IP + "&limit=10",
				Captures: []Capture{{Var: "s3Host_id", Expr: "data[0].id"}},
			})
		}

		// 6d. Add S3 repository, retrying on "pending components update" (a fresh
		// mount-server host may still be applying components) by refreshing the
		// host's components and re-POSTing — mirrors createWithComponents.
		repoPath := s3RepoPath(s.S3.OverwriteOwner)
		s3SessVar := "s3Repo_sess"
		add(Step{
			Comment:     "Add S3 object-storage repository " + s.S3.Name + " (retries on 'pending components update').",
			Method:      "POST",
			Path:        repoPath,
			Body:        s3RepoBody(s.S3, "$s3Cred_id", mountServerIDExpr),
			Captures:    []Capture{{Var: s3SessVar, Expr: "id"}},
			WaitSession: true,
			WaitVar:     s3SessVar,
			Kind:        "s3Repo",
			HostVar:     retryHostVar,
		})
	}

	return steps
}

// --------------------------------------------------------------------------
// Body builders — these mirror exactly what internal/veeam/veeam.go sends.
// --------------------------------------------------------------------------

func addLinuxHostBody(n Node, certFPExpr string) map[string]any {
	return map[string]any{
		"type":                   "LinuxHost",
		"name":                   n.IP,
		"description":            n.Role,
		"credentialsStorageType": "Certificate",
		"handshakeCode":          pairingCode(n),
		"sshFingerprint":         certFPExpr,
	}
}

func addHardenedRepoBody(name, hostIDExpr, path string, immutableDays int) map[string]any {
	return map[string]any{
		"type":        "LinuxHardened",
		"name":        name,
		"description": "",
		"hostId":      hostIDExpr,
		"repository": map[string]any{
			"path":                           path,
			"useFastCloningOnXFSVolumes":     true,
			"makeRecentBackupsImmutableDays": immutableDays,
		},
	}
}

func addVmwareProxyBody(hostIDExpr string) map[string]any {
	return map[string]any{
		"type":        "ViProxy",
		"description": "",
		"server": map[string]any{
			"hostId":       hostIDExpr,
			"maxTaskCount": 4,
		},
	}
}

func createHAClusterBody(primaryIP, secondaryIP, dns, endpoint string) map[string]any {
	body := map[string]any{
		"primaryNodeIpAddress":       primaryIP,
		"secondaryNodeIpAddress":     secondaryIP,
		"secondaryNodeCredentialsId": "$haCred_id",
		"clusterDnsName":             dns,
		"certificate": map[string]any{
			"certificate": "$haCert_cert",
			"formatType":  "Pem",
		},
	}
	if endpoint != "" {
		body["clusterEndpoint"] = endpoint
	}
	return body
}

func s3RepoPath(overwriteOwner bool) string {
	if overwriteOwner {
		return "/api/v1/backupInfrastructure/repositories?overwriteOwner=true"
	}
	return "/api/v1/backupInfrastructure/repositories"
}

func s3RepoBody(s S3, credIDExpr, mountServerIDExpr string) map[string]any {
	var immutability map[string]any
	if s.ImmutableDays > 0 {
		immutability = map[string]any{
			"isEnabled":        true,
			"daysCount":        s.ImmutableDays,
			"immutabilityMode": "RepositorySettings",
		}
	}

	var mountServer map[string]any
	if mountServerIDExpr != "" {
		mountServer = map[string]any{
			"mountServerSettingsType": "Linux",
			"linux": map[string]any{
				"mountServerId":    mountServerIDExpr,
				"vPowerNFSEnabled": false,
				"writeCacheFolder": "/tmp",
			},
		}
	}

	if s.Compatible {
		bucket := map[string]any{
			"bucketName": s.Bucket,
			"folderName": s.Folder,
		}
		if immutability != nil {
			bucket["immutability"] = immutability
		}
		body := map[string]any{
			"type":        "S3Compatible",
			"name":        s.Name,
			"description": "autodeploy object storage",
			"account": map[string]any{
				"servicePoint":  s.ServicePoint,
				"regionId":      s.Region,
				"credentialsId": credIDExpr,
				"connectionSettings": map[string]any{
					"connectionType": "Direct",
				},
			},
			"bucket": bucket,
		}
		if mountServer != nil {
			body["mountServer"] = mountServer
		}
		return body
	}

	// AmazonS3
	bucket := map[string]any{
		"regionId":   s.Region,
		"bucketName": s.Bucket,
		"folderName": s.Folder,
	}
	if immutability != nil {
		bucket["immutability"] = immutability
	}
	body := map[string]any{
		"type":        "AmazonS3",
		"name":        s.Name,
		"description": "autodeploy object storage",
		"account": map[string]any{
			"credentialsId": credIDExpr,
			"regionType":    "Global",
			"connectionSettings": map[string]any{
				"connectionType": "Direct",
			},
		},
		"bucket": bucket,
	}
	if mountServer != nil {
		body["mountServer"] = mountServer
	}
	return body
}

// --------------------------------------------------------------------------
// Naming helpers.
// --------------------------------------------------------------------------

func hostVarName(i int) string {
	return "host" + itoa(i)
}

func repoVarName(i int) string {
	return "repo" + itoa(i)
}

func nodeName(n Node) string {
	if n.Hostname != "" {
		return n.Hostname
	}
	return n.IP
}

// itoa is a minimal int-to-string without importing strconv/fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
