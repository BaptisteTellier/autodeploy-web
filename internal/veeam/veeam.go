// Package veeam is a thin client for the Veeam Backup & Replication REST API
// (exposed by the VSA appliance on :9419). It implements exactly the operations
// the deploy "wiring" step needs to register a freshly-deployed topology:
// create credentials, add Linux managed servers (VIA proxy / hardened repo),
// add a hardened repository, add a VMware backup proxy, and create a 2-node HA
// cluster.
//
// The calls and payloads are a 1:1 port of BaptisteTellier/vbr-ha-cluster
// (already REST) and BaptisteTellier/autodeploy Install-VeeamInfra.ps1 (cmdlets
// mapped to their REST equivalents). Most infrastructure operations are async
// and return a session id to poll via WaitSession.
package veeam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds connection settings for a VBR REST endpoint.
type Config struct {
	BaseURL    string // e.g. "https://192.168.1.10:9419"
	Username   string
	Password   string
	Insecure   bool   // skip TLS verification (VSA ships a self-signed cert)
	APIVersion string // x-api-version header; defaults to "1.3-rev2" (swagger default). Override to pin a different revision.
}

// Client talks to one VBR REST endpoint.
type Client struct {
	cfg   Config
	http  *http.Client
	token string
}

// New builds a client. No network call happens until Authenticate. If
// cfg.APIVersion is empty it is set to the version advertised by the swagger
// spec (1.3-rev2). The x-api-version header is required on every VBR REST
// call; override it only to target a different API revision.
func New(cfg Config) *Client {
	if cfg.APIVersion == "" {
		cfg.APIVersion = "1.3-rev2"
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	if cfg.Insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec — self-signed VSA cert, opt-in
	}
	return &Client{cfg: cfg, http: hc}
}

func (c *Client) url(path string) string { return strings.TrimRight(c.cfg.BaseURL, "/") + path }

// Authenticate performs the OAuth2 password grant and stores the bearer token.
func (c *Client) Authenticate(ctx context.Context) error {
	form := url.Values{
		"grant_type": {"Password"},
		"username":   {c.cfg.Username},
		"password":   {c.cfg.Password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/api/oauth2/token"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIVersion != "" {
		req.Header.Set("x-api-version", c.cfg.APIVersion)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError("POST", c.url("/api/oauth2/token"), resp)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return fmt.Errorf("veeam: empty access_token")
	}
	c.token = out.AccessToken
	return nil
}

// do performs an authenticated JSON request. body and out may be nil.
// On HTTP 401 it transparently re-authenticates and retries — once PER REQUEST,
// not once per run. Because the VBR access token lives only ~15 min, a long
// wiring (many VIAs, hours) re-auths as often as the token expires: every do()
// call (including each WaitSession poll) independently refreshes when it hits a
// 401, so the session self-heals at every 15-minute boundary indefinitely.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doOnce(ctx, method, path, body, out, true)
}

// doOnce is the inner implementation of do. When allowReauth is true and the
// server responds with HTTP 401, it re-authenticates and retries exactly once
// for THIS request (the retry passes allowReauth=false). The single-retry cap
// only prevents an infinite loop within one request: a freshly issued token
// rejected immediately is a real auth failure (bad creds), not expiry.
func (c *Client) doOnce(ctx context.Context, method, path string, body, out any, allowReauth bool) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rdr)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.cfg.APIVersion != "" {
		req.Header.Set("x-api-version", c.cfg.APIVersion)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && allowReauth && c.cfg.Username != "" {
		// Drain and discard the 401 body before re-authenticating.
		_, _ = io.Copy(io.Discard, resp.Body)
		if err := c.Authenticate(ctx); err != nil {
			return fmt.Errorf("veeam: re-authenticate after 401: %w", err)
		}
		return c.doOnce(ctx, method, path, body, out, false)
	}
	if resp.StatusCode/100 != 2 {
		return apiError(method, path, resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func apiError(method, path string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	detail := strings.TrimSpace(string(b))
	// Surface VBR's {"message":...} when present.
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		detail = m.Message
	}
	return fmt.Errorf("veeam: HTTP %d %s %s: %s", resp.StatusCode, method, path, detail)
}

// idResponse is the common {"id": "..."} body (credentials id or async session id).
type idResponse struct {
	ID string `json:"id"`
}

// --- sessions ----------------------------------------------------------------

// WaitSession polls an async session to completion. It mirrors VBR's dual model:
// backup/restore sessions finish on result.result ∈ {Success,Warning,Failed}
// (ESessionResult); infrastructure sessions finish on state == "Stopped"
// (treated as success when no result is set). Returns an error on Failed or
// timeout.
func (c *Client) WaitSession(ctx context.Context, sessionID string, poll, timeout time.Duration) error {
	if poll <= 0 {
		poll = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		// SessionModel: state (ESessionState) + result (SessionResultModel).
		// SessionResultModel.result is ESessionResult, JSON key "result".
		var s struct {
			State  string `json:"state"`
			Result struct {
				Result string `json:"result"`
			} `json:"result"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v1/sessions/"+url.PathEscape(sessionID), nil, &s); err != nil {
			return err
		}
		switch {
		case s.Result.Result == "Failed":
			return fmt.Errorf("veeam: session %s failed", sessionID)
		case s.Result.Result == "Success" || s.Result.Result == "Warning":
			return nil
		case s.State == "Stopped":
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("veeam: session %s did not finish within %s (state=%q)", sessionID, timeout, s.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// --- credentials -------------------------------------------------------------

// CreateCredentials creates a Linux password credential and returns its id.
func (c *Client) CreateCredentials(ctx context.Context, username, password, description string) (string, error) {
	body := map[string]any{
		"type":               "Linux",
		"username":           username,
		"password":           password,
		"description":        description,
		"authenticationType": "Password",
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/credentials", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// --- connection certificate --------------------------------------------------

// ConnectionCertificate calls POST /api/v1/connectionCertificate for a Linux
// host. It returns the base64 PEM certificate (for embedding in the HA cluster
// payload) and the SHA-256 fingerprint of the DER bytes (the value
// AddLinuxHost expects as sshFingerprint, == -ForceDeployerFingerprint).
// credentialsID may be "" for the initial pairing.
func (c *Client) ConnectionCertificate(ctx context.Context, ip, credentialsID string) (certB64, fingerprint string, err error) {
	body := map[string]any{"serverName": ip, "type": "LinuxHost"}
	if credentialsID != "" {
		body["credentialsId"] = credentialsID
	}
	var out struct {
		CertificateUpload struct {
			Certificate string `json:"certificate"`
		} `json:"certificateUpload"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/connectionCertificate", body, &out); err != nil {
		return "", "", err
	}
	certB64 = out.CertificateUpload.Certificate
	if certB64 != "" {
		der, derr := base64.StdEncoding.DecodeString(certB64)
		if derr != nil {
			return "", "", fmt.Errorf("veeam: decode certificate: %w", derr)
		}
		sum := sha256.Sum256(der)
		return certB64, strings.ToUpper(hex.EncodeToString(sum[:])), nil
	}
	if out.Fingerprint != "" {
		return "", out.Fingerprint, nil
	}
	return "", "", fmt.Errorf("veeam: no certificate/fingerprint for %s", ip)
}

// --- managed servers ---------------------------------------------------------

// AddLinuxHost adds a Linux managed server via certificate-based pairing
// (handshake/pairing code shown on the appliance at boot). If sshFingerprint is
// empty it is fetched via ConnectionCertificate (ForceDeployerFingerprint).
// Returns the async session id.
func (c *Client) AddLinuxHost(ctx context.Context, ip, description, handshakeCode, sshFingerprint string) (string, error) {
	if sshFingerprint == "" {
		_, fp, err := c.ConnectionCertificate(ctx, ip, "")
		if err != nil {
			return "", err
		}
		sshFingerprint = fp
	}
	body := map[string]any{
		"type":                   "LinuxHost",
		"name":                   ip,
		"description":            description,
		"credentialsStorageType": "Certificate",
		"handshakeCode":          handshakeCode,
		"sshFingerprint":         sshFingerprint,
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/backupInfrastructure/managedServers", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// FindManagedServerByName returns the managed server id whose name/IP matches, or "".
func (c *Client) FindManagedServerByName(ctx context.Context, name string) (string, error) {
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/backupInfrastructure/managedServers?nameFilter="+url.QueryEscape(name)+"&limit=10", nil, &out); err != nil {
		return "", err
	}
	for _, s := range out.Data {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", nil
}

// --- repositories ------------------------------------------------------------

// FindRepositoryByName returns the id of the repository with an exact name
// match, or "". Used for idempotency and to resolve the hardened repo / the
// "Default Backup Repository".
func (c *Client) FindRepositoryByName(ctx context.Context, name string) (string, error) {
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/backupInfrastructure/repositories?nameFilter="+url.QueryEscape(name)+"&limit=50", nil, &out); err != nil {
		return "", err
	}
	for _, r := range out.Data {
		if r.Name == name {
			return r.ID, nil
		}
	}
	return "", nil
}

// DeleteRepository removes a repository by id (synchronous).
func (c *Client) DeleteRepository(ctx context.Context, repoID string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/backupInfrastructure/repositories/"+url.PathEscape(repoID), nil, nil)
}

// --- config backup -----------------------------------------------------------

// RedirectConfigBackup points the VBR configuration backup at repoID using the
// reference read-modify-write: GET settings, swap backupRepositoryId, force
// notifications.SNMPEnabled=false (VBR validates it on every PUT), PUT back.
// No-op when it already targets repoID.
func (c *Client) RedirectConfigBackup(ctx context.Context, repoID string) error {
	var settings map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v1/configBackup", nil, &settings); err != nil {
		return err
	}
	if cur, _ := settings["backupRepositoryId"].(string); cur == repoID {
		return nil
	}
	settings["backupRepositoryId"] = repoID
	if n, ok := settings["notifications"].(map[string]any); ok {
		n["SNMPEnabled"] = false
	}
	return c.do(ctx, http.MethodPut, "/api/v1/configBackup", settings, nil)
}

// --- backups -----------------------------------------------------------------

// ListBackups returns the ids of backups stored in repoID.
func (c *Client) ListBackups(ctx context.Context, repoID string) ([]string, error) {
	var out struct {
		Data []struct {
			ID                 string `json:"id"`
			RepositoryID       string `json:"repositoryId"`
			BackupRepositoryID string `json:"backupRepositoryId"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/backups?limit=1000", nil, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, b := range out.Data {
		if b.RepositoryID == repoID || b.BackupRepositoryID == repoID {
			ids = append(ids, b.ID)
		}
	}
	return ids, nil
}

// DeleteBackup deletes a backup by id and returns the async session id.
func (c *Client) DeleteBackup(ctx context.Context, backupID string) (string, error) {
	// includeGFS=true so GFS (weekly/monthly/yearly) restore points are removed
	// too — otherwise the Default Backup Repository can't be emptied/deleted
	// (matches the vbr-ha-cluster reference).
	var out idResponse
	if err := c.do(ctx, http.MethodDelete, "/api/v1/backups/"+url.PathEscape(backupID)+"?includeGFS=true", nil, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// AddHardenedRepository adds a LinuxHardened repository on hostID with optional
// XFS fast clone and immutability. Returns the async session id.
func (c *Client) AddHardenedRepository(ctx context.Context, name, hostID, path, description string, xfsFastClone bool, immutabilityDays int) (string, error) {
	body := map[string]any{
		"type":        "LinuxHardened",
		"name":        name,
		"description": description,
		"hostId":      hostID,
		"repository": map[string]any{
			"path":                           path,
			"useFastCloningOnXFSVolumes":     xfsFastClone,
			"makeRecentBackupsImmutableDays": immutabilityDays,
		},
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/backupInfrastructure/repositories", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// --- proxies -----------------------------------------------------------------

// AddVmwareProxy registers a VMware backup proxy on the given Linux managed
// server (the REST equivalent of Add-VBRViLinuxProxy). Returns the async
// session id.
//
// Schema: ViProxySpec (discriminator type="ViProxy") extends ProxySpec which
// requires "description" and "type". ViProxySpec additionally requires "server"
// (ProxyServerSettingsModel) with required field "hostId". maxTaskCount lives
// inside the server object — there is no top-level maxTaskCount on ViProxySpec.
func (c *Client) AddVmwareProxy(ctx context.Context, hostID string, maxTasks int) (string, error) {
	if maxTasks <= 0 {
		maxTasks = 4
	}
	body := map[string]any{
		"type":        "ViProxy",
		"description": "",
		"server": map[string]any{
			"hostId":       hostID,
			"maxTaskCount": maxTasks,
		},
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/backupInfrastructure/proxies", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// --- license -----------------------------------------------------------------

// InstalledLicense is the subset of InstalledLicenseModel we surface.
type InstalledLicense struct {
	Status     string `json:"status"`     // "Valid", "Invalid", "Expired", …
	Type       string `json:"type"`       // "Subscription", "Perpetual", …
	Edition    string `json:"edition"`    // "EnterprisePlus", …
	LicensedTo string `json:"licensedTo"` // owner name
}

// GetLicense returns the currently installed license (accessible even on an
// unlicensed server via the NoLicense role). status == "" means none/empty.
func (c *Client) GetLicense(ctx context.Context) (InstalledLicense, error) {
	var out InstalledLicense
	if err := c.do(ctx, http.MethodGet, "/api/v1/license", nil, &out); err != nil {
		return InstalledLicense{}, err
	}
	return out, nil
}

// encodeLicensePayload normalises a .lic file's content into the value the
// API's "license" field expects: canonical base64 of the license XML.
//
// A Veeam .lic file may be stored two ways, and we must handle both:
//   - raw XML (<?xml …><Licenses>…) → base64-encode it,
//   - already base64-encoded (often line-wrapped) → strip whitespace, decode and
//     re-encode so the result is single-line canonical base64.
//
// Getting this wrong is what produced "Data at the root level is invalid"
// (double-encoding) and "Invalid licence format" (sending line-wrapped base64
// verbatim). A leading UTF-8 BOM and surrounding whitespace are stripped first.
func encodeLicensePayload(licenseBytes []byte) (string, error) {
	const utf8BOM = "\xef\xbb\xbf"
	s := strings.TrimSpace(strings.TrimPrefix(string(licenseBytes), utf8BOM))
	if s == "" {
		return "", fmt.Errorf("veeam: empty license file")
	}
	// Raw XML license file → encode as-is.
	if strings.HasPrefix(s, "<") {
		return base64.StdEncoding.EncodeToString([]byte(s)), nil
	}
	// Otherwise assume base64 (possibly line-wrapped): drop whitespace, decode,
	// then re-encode canonically. Try padded then unpadded alphabets.
	compact := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if xml, err := enc.DecodeString(compact); err == nil {
			return base64.StdEncoding.EncodeToString(xml), nil
		}
	}
	// Neither XML nor decodable base64 — encode the bytes and let VBR validate.
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}

// InstallLicense installs a Veeam license on the VBR server. The license file is
// normalised to canonical base64 of its XML (see encodeLicensePayload). This
// endpoint is reachable even on an unlicensed server (NoLicense role), i.e. the
// freshly-kickstarted case. Returns the resulting license status (e.g. "Valid").
func (c *Client) InstallLicense(ctx context.Context, licenseBytes []byte) (InstalledLicense, error) {
	payload, err := encodeLicensePayload(licenseBytes)
	if err != nil {
		return InstalledLicense{}, err
	}
	body := map[string]any{"license": payload}
	var out InstalledLicense
	if err := c.do(ctx, http.MethodPost, "/api/v1/license/install", body, &out); err != nil {
		return InstalledLicense{}, err
	}
	return out, nil
}

// --- HA cluster --------------------------------------------------------------

// HASpec describes the 2-node HA cluster to create.
type HASpec struct {
	PrimaryNodeIP             string
	SecondaryNodeIP           string
	SecondaryCredentialsID    string
	ClusterDNSName            string
	CertificatePEMBase64      string // from ConnectionCertificate(secondary, credsID)
	ClusterEndpoint           string // optional VIP
	CrossSubnet               bool
	PrimaryExternalEndpoint   string // required when CrossSubnet
	SecondaryExternalEndpoint string // required when CrossSubnet
}

// CreateHACluster creates the HA cluster and returns the async session id.
func (c *Client) CreateHACluster(ctx context.Context, spec HASpec) (string, error) {
	body := map[string]any{
		"primaryNodeIpAddress":       spec.PrimaryNodeIP,
		"secondaryNodeIpAddress":     spec.SecondaryNodeIP,
		"secondaryNodeCredentialsId": spec.SecondaryCredentialsID,
		"clusterDnsName":             spec.ClusterDNSName,
		"certificate": map[string]any{
			"certificate": spec.CertificatePEMBase64,
			"formatType":  "Pem",
		},
	}
	if spec.ClusterEndpoint != "" {
		body["clusterEndpoint"] = spec.ClusterEndpoint
	}
	if spec.CrossSubnet {
		body["isCrossSubnetMode"] = true
		body["primaryNodeExternalEndpoint"] = spec.PrimaryExternalEndpoint
		body["secondaryNodeExternalEndpoint"] = spec.SecondaryExternalEndpoint
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/highAvailabilityCluster", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// --- cloud credentials -------------------------------------------------------

// CreateCloudCredentials creates an Amazon S3 access-key/secret-key credential
// (POST /api/v1/cloudCredentials, type "Amazon") and returns its id.
func (c *Client) CreateCloudCredentials(ctx context.Context, accessKey, secretKey, description string) (string, error) {
	body := map[string]any{
		"type":        "Amazon",
		"accessKey":   accessKey,
		"secretKey":   secretKey,
		"description": description,
	}
	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/cloudCredentials", body, &out); err != nil {
		return "", fmt.Errorf("veeam: CreateCloudCredentials: %w", err)
	}
	return out.ID, nil
}

// --- object-storage repositories ---------------------------------------------

// S3RepoSpec describes an S3 / S3-compatible object-storage repository to add.
type S3RepoSpec struct {
	Name          string // repository display name
	Description   string
	CredentialsID string // from CreateCloudCredentials
	Compatible    bool   // true => S3Compatible (servicePoint required); false => AmazonS3
	ServicePoint  string // S3Compatible only: endpoint URL e.g. https://s3.example.com
	RegionID      string // AmazonS3: AWS region id (e.g. "us-east-1"); S3Compatible: provider region string
	Bucket        string
	Folder        string
	ImmutableDays int // 0 = immutability disabled
}

// AddS3Repository adds an Amazon S3 or S3-compatible object-storage repository
// (POST /api/v1/backupInfrastructure/repositories) and returns the async session id.
func (c *Client) AddS3Repository(ctx context.Context, spec S3RepoSpec) (string, error) {
	// Build the immutability sub-object only when requested.
	var immutability map[string]any
	if spec.ImmutableDays > 0 {
		immutability = map[string]any{
			"isEnabled":        true,
			"daysCount":        spec.ImmutableDays,
			"immutabilityMode": "RepositorySettings",
		}
	}

	var body map[string]any
	if spec.Compatible {
		// S3Compatible repository type.
		bucket := map[string]any{
			"bucketName": spec.Bucket,
			"folderName": spec.Folder,
		}
		if immutability != nil {
			bucket["immutability"] = immutability
		}
		body = map[string]any{
			"type":        "S3Compatible",
			"name":        spec.Name,
			"description": spec.Description,
			"account": map[string]any{
				"servicePoint":  spec.ServicePoint,
				"regionId":      spec.RegionID,
				"credentialsId": spec.CredentialsID,
				"connectionSettings": map[string]any{
					"connectionType": "Direct",
				},
			},
			"bucket": bucket,
		}
	} else {
		// AmazonS3 repository type.
		bucket := map[string]any{
			"regionId":   spec.RegionID,
			"bucketName": spec.Bucket,
			"folderName": spec.Folder,
		}
		if immutability != nil {
			bucket["immutability"] = immutability
		}
		body = map[string]any{
			"type":        "AmazonS3",
			"name":        spec.Name,
			"description": spec.Description,
			"account": map[string]any{
				"credentialsId": spec.CredentialsID,
				"regionType":    "Global",
				"connectionSettings": map[string]any{
					"connectionType": "Direct",
				},
			},
			"bucket": bucket,
		}
	}

	var out idResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/backupInfrastructure/repositories", body, &out); err != nil {
		return "", fmt.Errorf("veeam: AddS3Repository: %w", err)
	}
	return out.ID, nil
}

// --- general options: syslog -------------------------------------------------

// SetSyslog points VBR event forwarding at a syslog server
// (PUT /api/v1/generalOptions/eventForwarding). protocol is "Udp"|"Tcp"|"Tls".
func (c *Client) SetSyslog(ctx context.Context, serverName string, port int, protocol string) error {
	if protocol == "" {
		protocol = "Udp"
	}
	if port <= 0 {
		port = 514
	}
	body := map[string]any{
		"syslogServer": map[string]any{
			"serverName":        serverName,
			"port":              port,
			"transportProtocol": protocol,
		},
	}
	if err := c.do(ctx, http.MethodPut, "/api/v1/generalOptions/eventForwarding", body, nil); err != nil {
		return fmt.Errorf("veeam: SetSyslog: %w", err)
	}
	return nil
}

// --- general options: node exporter ------------------------------------------

// SetNodeExporter enables/disables the appliance Prometheus metrics endpoint
// (PUT /api/v1/generalOptions/nodeExporterSettings). When username != "" the
// auth type is UsernamePassword and credentials are also pushed via
// POST /api/v1/generalOptions/nodeExporterSettings/setBasicAuth.
func (c *Client) SetNodeExporter(ctx context.Context, enabled, tls bool, username, password string) error {
	authType := "None"
	auth := map[string]any{"type": authType}
	if username != "" {
		authType = "UsernamePassword"
		auth = map[string]any{
			"type":     authType,
			"username": username,
			"password": password,
		}
	}
	body := map[string]any{
		"metricsSharingEnabled": enabled,
		"tlsEnabled":            tls,
		"auth":                  auth,
	}
	if err := c.do(ctx, http.MethodPut, "/api/v1/generalOptions/nodeExporterSettings", body, nil); err != nil {
		return fmt.Errorf("veeam: SetNodeExporter: %w", err)
	}
	if username != "" {
		credsBody := map[string]any{
			"username": username,
			"password": password,
		}
		if err := c.do(ctx, http.MethodPost, "/api/v1/generalOptions/nodeExporterSettings/setBasicAuth", credsBody, nil); err != nil {
			return fmt.Errorf("veeam: SetNodeExporter setBasicAuth: %w", err)
		}
	}
	return nil
}

// Logout best-effort revokes the token.
func (c *Client) Logout(ctx context.Context) {
	_ = c.do(ctx, http.MethodPost, "/api/oauth2/logout", nil, nil)
}
