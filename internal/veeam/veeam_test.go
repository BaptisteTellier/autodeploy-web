package veeam

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient spins up a mock VBR server with the given handler and returns an
// authenticated client pointed at it.
func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, Username: "admin", Password: "pw"})
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return c, srv
}

// baseMux returns a mux that already handles the token endpoint.
func baseMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("grant_type") != "Password" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-123"})
	})
	return mux
}

func decode(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode body: %v (%s)", err, b)
	}
	return m
}

func TestAPIVersionDefault(t *testing.T) {
	// New() must set APIVersion to "1.3-rev2" when not provided.
	c := New(Config{BaseURL: "http://localhost", Username: "u", Password: "p"})
	if c.cfg.APIVersion != "1.3-rev2" {
		t.Errorf("default APIVersion = %q, want 1.3-rev2", c.cfg.APIVersion)
	}
}

func TestAuthenticateAndBearer(t *testing.T) {
	mux := baseMux()
	var gotAuth, gotAPIVersion string
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIVersion = r.Header.Get("x-api-version")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cred-1"})
	})
	c, _ := newTestClient(t, mux)

	id, err := c.CreateCredentials(context.Background(), "veeamadmin", "pw", "desc")
	if err != nil {
		t.Fatalf("CreateCredentials: %v", err)
	}
	if id != "cred-1" {
		t.Errorf("id = %q, want cred-1", id)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	// x-api-version must default to the swagger spec value.
	if gotAPIVersion != "1.3-rev2" {
		t.Errorf("x-api-version = %q, want 1.3-rev2", gotAPIVersion)
	}
}

func TestInstallLicense(t *testing.T) {
	// An already-base64 .lic blob is normalised to canonical base64 (see
	// encodeLicensePayload); a clean single-line blob round-trips to itself, with
	// the leading BOM / surrounding whitespace stripped.
	const want = "PD94bWwgdmVyc2lvbj0iMS4wIj8+"
	raw := []byte("\xef\xbb\xbf  " + want + "\n") // BOM + padding the client must strip
	mux := baseMux()
	var got string
	mux.HandleFunc("/api/v1/license/install", func(w http.ResponseWriter, r *http.Request) {
		m := decode(t, r)
		got, _ = m["license"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "Valid", "edition": "EnterprisePlus", "licensedTo": "ACME",
		})
	})
	c, _ := newTestClient(t, mux)

	lic, err := c.InstallLicense(context.Background(), raw)
	if err != nil {
		t.Fatalf("InstallLicense: %v", err)
	}
	if got != want {
		t.Errorf("license body = %q, want %q", got, want)
	}
	if lic.Status != "Valid" || lic.Edition != "EnterprisePlus" {
		t.Errorf("license = %+v, want Valid/EnterprisePlus", lic)
	}

	// Empty / whitespace-only content must error without hitting the network.
	if _, err := c.InstallLicense(context.Background(), []byte("  \n")); err == nil {
		t.Error("InstallLicense(blank) = nil error, want error")
	}
}

func TestEncodeLicensePayload(t *testing.T) {
	xml := `<?xml version="1.0"?><Licenses><License>x</License></Licenses>`
	want := base64.StdEncoding.EncodeToString([]byte(xml))

	cases := map[string][]byte{
		"raw XML":                  []byte(xml),
		"raw XML with BOM + space": []byte("\xef\xbb\xbf  " + xml + "\n"),
		"canonical base64":         []byte(want),
		"line-wrapped base64":      []byte(want[:8] + "\r\n" + want[8:16] + "\n" + want[16:] + "\n"),
	}
	for name, in := range cases {
		got, err := encodeLicensePayload(in)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: payload = %q, want %q", name, got, want)
		}
	}

	if _, err := encodeLicensePayload([]byte("  \n")); err == nil {
		t.Error("encodeLicensePayload(blank) = nil error, want error")
	}
}

func TestConnectionCertificateFingerprint(t *testing.T) {
	raw := []byte("fake-der-certificate-bytes")
	certB64 := base64.StdEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	wantFP := strings.ToUpper(hex.EncodeToString(sum[:]))

	mux := baseMux()
	mux.HandleFunc("/api/v1/connectionCertificate", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		if body["serverName"] != "10.0.0.5" || body["type"] != "LinuxHost" {
			t.Errorf("unexpected cert request body: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"certificateUpload": map[string]any{"certificate": certB64},
		})
	})
	c, _ := newTestClient(t, mux)

	gotCert, gotFP, err := c.ConnectionCertificate(context.Background(), "10.0.0.5", "")
	if err != nil {
		t.Fatalf("ConnectionCertificate: %v", err)
	}
	if gotCert != certB64 {
		t.Errorf("cert mismatch")
	}
	if gotFP != wantFP {
		t.Errorf("fingerprint = %q, want %q", gotFP, wantFP)
	}
}

func TestAddLinuxHostPayload(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/managedServers", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		for k, want := range map[string]any{
			"type": "LinuxHost", "name": "10.0.0.6",
			"credentialsStorageType": "Certificate", "handshakeCode": "ABC123",
			"sshFingerprint": "DEADBEEF",
		} {
			if body[k] != want {
				t.Errorf("payload[%q] = %v, want %v", k, body[k], want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-host"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddLinuxHost(context.Background(), "10.0.0.6", "Proxy", "ABC123", "DEADBEEF")
	if err != nil {
		t.Fatalf("AddLinuxHost: %v", err)
	}
	if sess != "sess-host" {
		t.Errorf("session = %q, want sess-host", sess)
	}
}

func TestAddHardenedRepositoryPayload(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		if body["type"] != "LinuxHardened" || body["hostId"] != "host-1" {
			t.Errorf("repo payload = %v", body)
		}
		repo, _ := body["repository"].(map[string]any)
		if repo["path"] != "/backups" || repo["useFastCloningOnXFSVolumes"] != true {
			t.Errorf("repo sub-payload = %v", repo)
		}
		if repo["makeRecentBackupsImmutableDays"].(float64) != 30 {
			t.Errorf("immutability = %v, want 30", repo["makeRecentBackupsImmutableDays"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-repo"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddHardenedRepository(context.Background(), "HR-01", "host-1", "/backups", "", true, 30)
	if err != nil {
		t.Fatalf("AddHardenedRepository: %v", err)
	}
	if sess != "sess-repo" {
		t.Errorf("session = %q", sess)
	}
}

func TestAddVmwareProxyPayload(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/proxies", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		// ViProxySpec: type + description (required by ProxySpec) + server (required by ViProxySpec).
		if body["type"] != "ViProxy" {
			t.Errorf("proxy type = %v, want ViProxy", body["type"])
		}
		if _, ok := body["description"]; !ok {
			t.Errorf("proxy payload missing required 'description' field")
		}
		// top-level maxTaskCount must NOT be present (not part of ViProxySpec).
		if _, ok := body["maxTaskCount"]; ok {
			t.Errorf("proxy payload must not contain top-level maxTaskCount")
		}
		// maxTaskCount lives inside server (ProxyServerSettingsModel).
		server, _ := body["server"].(map[string]any)
		if server == nil {
			t.Fatalf("proxy payload missing 'server' object")
		}
		if server["hostId"] != "host-proxy-1" {
			t.Errorf("server.hostId = %v, want host-proxy-1", server["hostId"])
		}
		if server["maxTaskCount"].(float64) != 4 {
			t.Errorf("server.maxTaskCount = %v, want 4", server["maxTaskCount"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-proxy"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddVmwareProxy(context.Background(), "host-proxy-1", 4)
	if err != nil {
		t.Fatalf("AddVmwareProxy: %v", err)
	}
	if sess != "sess-proxy" {
		t.Errorf("session = %q, want sess-proxy", sess)
	}
}

func TestCreateHAClusterPayload(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/highAvailabilityCluster", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		if body["primaryNodeIpAddress"] != "10.0.0.10" || body["secondaryNodeIpAddress"] != "10.0.0.11" {
			t.Errorf("HA payload IPs = %v", body)
		}
		if body["secondaryNodeCredentialsId"] != "cred-9" || body["clusterDnsName"] != "vbr.local" {
			t.Errorf("HA payload = %v", body)
		}
		// clusterEndpoint (floating VIP) must be present in standard mode.
		if body["clusterEndpoint"] != "10.0.0.12" {
			t.Errorf("HA clusterEndpoint = %v, want 10.0.0.12", body["clusterEndpoint"])
		}
		cert, _ := body["certificate"].(map[string]any)
		if cert["formatType"] != "Pem" || cert["certificate"] != "CERTB64" {
			t.Errorf("HA cert = %v", cert)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-ha"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.CreateHACluster(context.Background(), HASpec{
		PrimaryNodeIP: "10.0.0.10", SecondaryNodeIP: "10.0.0.11",
		SecondaryCredentialsID: "cred-9", ClusterDNSName: "vbr.local",
		ClusterEndpoint:      "10.0.0.12",
		CertificatePEMBase64: "CERTB64",
	})
	if err != nil {
		t.Fatalf("CreateHACluster: %v", err)
	}
	if sess != "sess-ha" {
		t.Errorf("session = %q", sess)
	}
}

func TestWaitSessionDualModel(t *testing.T) {
	mux := baseMux()
	// /api/v1/sessions/<id> — behaviour keyed by id.
	calls := map[string]int{}
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
		calls[id]++
		var resp map[string]any
		switch id {
		case "infra": // Running once, then Stopped (no result.status)
			if calls[id] < 2 {
				resp = map[string]any{"state": "Running"}
			} else {
				resp = map[string]any{"state": "Stopped"}
			}
		case "result": // SessionResultModel.result == "Success" (ESessionResult, JSON key "result")
			resp = map[string]any{"state": "Running", "result": map[string]any{"result": "Success"}}
		case "fail":
			resp = map[string]any{"state": "Running", "result": map[string]any{"result": "Failed"}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	c, _ := newTestClient(t, mux)
	ctx := context.Background()

	if err := c.WaitSession(ctx, "infra", 5*time.Millisecond, 2*time.Second); err != nil {
		t.Errorf("infra session: %v", err)
	}
	if err := c.WaitSession(ctx, "result", 5*time.Millisecond, 2*time.Second); err != nil {
		t.Errorf("result session: %v", err)
	}
	if err := c.WaitSession(ctx, "fail", 5*time.Millisecond, 2*time.Second); err == nil {
		t.Error("fail session: expected error")
	}
}

func TestRedirectConfigBackupReadModifyWrite(t *testing.T) {
	mux := baseMux()
	var put map[string]any
	mux.HandleFunc("/api/v1/configBackup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"backupRepositoryId": "old-repo",
				"notifications":      map[string]any{"SNMPEnabled": true},
			})
			return
		}
		put = decode(t, r) // PUT
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newTestClient(t, mux)
	if err := c.RedirectConfigBackup(context.Background(), "new-repo"); err != nil {
		t.Fatalf("RedirectConfigBackup: %v", err)
	}
	if put["backupRepositoryId"] != "new-repo" {
		t.Errorf("backupRepositoryId = %v, want new-repo", put["backupRepositoryId"])
	}
	n, ok := put["notifications"].(map[string]any)
	if !ok || n["SNMPEnabled"] != false {
		t.Errorf("SNMPEnabled must be forced false, got %v", put["notifications"])
	}
}

func TestRedirectConfigBackupNoopWhenSame(t *testing.T) {
	mux := baseMux()
	puts := 0
	mux.HandleFunc("/api/v1/configBackup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"backupRepositoryId": "repo-1"})
			return
		}
		puts++
	})
	c, _ := newTestClient(t, mux)
	if err := c.RedirectConfigBackup(context.Background(), "repo-1"); err != nil {
		t.Fatalf("RedirectConfigBackup: %v", err)
	}
	if puts != 0 {
		t.Errorf("PUT issued %d times, want 0 (already targeted)", puts)
	}
}

func TestListBackupsFiltersByRepo(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backups", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "b1", "repositoryId": "r1"},
			{"id": "b2", "repositoryId": "r2"},
			{"id": "b3", "backupRepositoryId": "r1"},
		}})
	})
	c, _ := newTestClient(t, mux)
	ids, err := c.ListBackups(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %v, want 2 (b1,b3)", ids)
	}
}

func TestCreateCloudCredentials(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/cloudCredentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body := decode(t, r)
		for k, want := range map[string]any{
			"type":        "Amazon",
			"accessKey":   "AKIAIOSFODNN7EXAMPLE",
			"secretKey":   "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"description": "test creds",
		} {
			if body[k] != want {
				t.Errorf("body[%q] = %v, want %v", k, body[k], want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cloud-cred-1"})
	})
	c, _ := newTestClient(t, mux)

	id, err := c.CreateCloudCredentials(context.Background(), "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "test creds")
	if err != nil {
		t.Fatalf("CreateCloudCredentials: %v", err)
	}
	if id != "cloud-cred-1" {
		t.Errorf("id = %q, want cloud-cred-1", id)
	}
}

func TestAddS3RepositoryAmazon(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body := decode(t, r)
		if body["type"] != "AmazonS3" {
			t.Errorf("type = %v, want AmazonS3", body["type"])
		}
		if body["name"] != "MyS3Repo" || body["description"] != "desc" {
			t.Errorf("name/description = %v / %v", body["name"], body["description"])
		}
		account, _ := body["account"].(map[string]any)
		if account == nil {
			t.Fatal("missing account object")
		}
		if account["credentialsId"] != "cloud-cred-1" {
			t.Errorf("account.credentialsId = %v, want cloud-cred-1", account["credentialsId"])
		}
		if account["regionType"] != "Global" {
			t.Errorf("account.regionType = %v, want Global", account["regionType"])
		}
		connSettings, _ := account["connectionSettings"].(map[string]any)
		if connSettings["connectionType"] != "Direct" {
			t.Errorf("connectionSettings.connectionType = %v, want Direct", connSettings["connectionType"])
		}
		bucket, _ := body["bucket"].(map[string]any)
		if bucket == nil {
			t.Fatal("missing bucket object")
		}
		if bucket["regionId"] != "us-east-1" || bucket["bucketName"] != "my-bucket" || bucket["folderName"] != "vbr" {
			t.Errorf("bucket = %v", bucket)
		}
		// immutability must be present with 30 days.
		imm, _ := bucket["immutability"].(map[string]any)
		if imm == nil {
			t.Fatal("missing immutability object")
		}
		if imm["isEnabled"] != true || imm["daysCount"].(float64) != 30 || imm["immutabilityMode"] != "RepositorySettings" {
			t.Errorf("immutability = %v", imm)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-s3"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "MyS3Repo", Description: "desc",
		CredentialsID: "cloud-cred-1", Compatible: false,
		RegionID: "us-east-1", Bucket: "my-bucket", Folder: "vbr",
		ImmutableDays: 30,
	})
	if err != nil {
		t.Fatalf("AddS3Repository (Amazon): %v", err)
	}
	if sess != "sess-s3" {
		t.Errorf("session = %q, want sess-s3", sess)
	}
}

func TestAddS3RepositoryAmazonNoImmutability(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		bucket, _ := body["bucket"].(map[string]any)
		if bucket == nil {
			t.Fatal("missing bucket object")
		}
		// When ImmutableDays==0 the immutability key must be absent.
		if _, ok := bucket["immutability"]; ok {
			t.Errorf("immutability must be absent when ImmutableDays=0, got %v", bucket["immutability"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-s3-noim"})
	})
	c, _ := newTestClient(t, mux)

	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "Repo", Description: "", CredentialsID: "cred-1",
		Compatible: false, RegionID: "eu-west-1", Bucket: "b", Folder: "f",
		ImmutableDays: 0,
	})
	if err != nil {
		t.Fatalf("AddS3Repository (no immutability): %v", err)
	}
}

func TestAddS3RepositoryCompatible(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body := decode(t, r)
		if body["type"] != "S3Compatible" {
			t.Errorf("type = %v, want S3Compatible", body["type"])
		}
		account, _ := body["account"].(map[string]any)
		if account == nil {
			t.Fatal("missing account object")
		}
		if account["servicePoint"] != "https://s3.example.com" {
			t.Errorf("account.servicePoint = %v, want https://s3.example.com", account["servicePoint"])
		}
		if account["regionId"] != "default" {
			t.Errorf("account.regionId = %v, want default", account["regionId"])
		}
		if account["credentialsId"] != "cred-2" {
			t.Errorf("account.credentialsId = %v, want cred-2", account["credentialsId"])
		}
		connSettings, _ := account["connectionSettings"].(map[string]any)
		if connSettings["connectionType"] != "Direct" {
			t.Errorf("connectionSettings.connectionType = %v, want Direct", connSettings["connectionType"])
		}
		bucket, _ := body["bucket"].(map[string]any)
		if bucket == nil {
			t.Fatal("missing bucket object")
		}
		if bucket["bucketName"] != "compat-bucket" || bucket["folderName"] != "backups" {
			t.Errorf("bucket = %v", bucket)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-compat"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "CompatRepo", Description: "s3-compat",
		CredentialsID: "cred-2", Compatible: true,
		ServicePoint: "https://s3.example.com", RegionID: "default",
		Bucket: "compat-bucket", Folder: "backups",
		ImmutableDays: 0,
	})
	if err != nil {
		t.Fatalf("AddS3Repository (S3Compatible): %v", err)
	}
	if sess != "sess-compat" {
		t.Errorf("session = %q, want sess-compat", sess)
	}
}

func TestAddS3RepositoryMountServerPresent(t *testing.T) {
	// When MountServerID is set the body must contain a mountServer block with
	// mountServerSettingsType=="Linux", the correct mountServerId, vPowerNFSEnabled==false,
	// and writeCacheFolder defaulting to "/tmp" when MountWriteCache is empty.
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body := decode(t, r)
		ms, ok := body["mountServer"].(map[string]any)
		if !ok || ms == nil {
			t.Fatal("mountServer block missing from body")
		}
		if ms["mountServerSettingsType"] != "Linux" {
			t.Errorf("mountServerSettingsType = %v, want Linux", ms["mountServerSettingsType"])
		}
		linux, _ := ms["linux"].(map[string]any)
		if linux == nil {
			t.Fatal("mountServer.linux sub-object missing")
		}
		if linux["mountServerId"] != "managed-uuid-1" {
			t.Errorf("mountServerId = %v, want managed-uuid-1", linux["mountServerId"])
		}
		if linux["vPowerNFSEnabled"] != false {
			t.Errorf("vPowerNFSEnabled = %v, want false", linux["vPowerNFSEnabled"])
		}
		if linux["writeCacheFolder"] != "/tmp" {
			t.Errorf("writeCacheFolder = %v, want /tmp", linux["writeCacheFolder"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-s3-mount"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "MountedRepo", Description: "with mount server",
		CredentialsID: "cred-m", Compatible: false,
		RegionID: "eu-west-1", Bucket: "b", Folder: "f",
		MountServerID: "managed-uuid-1",
		// MountWriteCache deliberately left empty — must default to "/tmp"
	})
	if err != nil {
		t.Fatalf("AddS3Repository (with mount server): %v", err)
	}
	if sess != "sess-s3-mount" {
		t.Errorf("session = %q, want sess-s3-mount", sess)
	}
}

func TestAddS3RepositoryMountServerCustomCache(t *testing.T) {
	// When MountWriteCache is explicitly set it must be used verbatim.
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		ms, _ := body["mountServer"].(map[string]any)
		if ms == nil {
			t.Fatal("mountServer block missing")
		}
		linux, _ := ms["linux"].(map[string]any)
		if linux == nil {
			t.Fatal("mountServer.linux missing")
		}
		if linux["writeCacheFolder"] != "/mnt/cache" {
			t.Errorf("writeCacheFolder = %v, want /mnt/cache", linux["writeCacheFolder"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-cache"})
	})
	c, _ := newTestClient(t, mux)

	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "CacheRepo", CredentialsID: "cred-c", Compatible: false,
		RegionID: "us-west-2", Bucket: "b", Folder: "f",
		MountServerID:   "managed-uuid-2",
		MountWriteCache: "/mnt/cache",
	})
	if err != nil {
		t.Fatalf("AddS3Repository (custom cache): %v", err)
	}
}

func TestAddS3RepositoryMountServerAbsentWhenEmpty(t *testing.T) {
	// When MountServerID is "" the mountServer key must be absent from the body.
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		if _, ok := body["mountServer"]; ok {
			t.Errorf("mountServer must be absent when MountServerID is empty, got %v", body["mountServer"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-no-mount"})
	})
	c, _ := newTestClient(t, mux)

	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "NoMountRepo", CredentialsID: "cred-n", Compatible: false,
		RegionID: "ap-southeast-1", Bucket: "b", Folder: "f",
		// MountServerID deliberately empty
	})
	if err != nil {
		t.Fatalf("AddS3Repository (no mount server): %v", err)
	}
}

func TestAddS3RepositoryOverwriteOwner(t *testing.T) {
	// When OverwriteOwner is true the POST path must carry ?overwriteOwner=true;
	// when false the query parameter must be absent. The mux ignores the query
	// string, so both calls land on the same handler — we capture the raw query.
	mux := baseMux()
	var capturedQuery [2]string
	call := 0
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		capturedQuery[call] = r.URL.Query().Get("overwriteOwner")
		call++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-overwrite"})
	})
	c, _ := newTestClient(t, mux)

	// First call: OverwriteOwner true — query param must be "true".
	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "TakeOverRepo", Description: "overwrite test",
		CredentialsID: "cred-ow", Compatible: true,
		ServicePoint: "https://s3.lab.local", RegionID: "default",
		Bucket: "taken-bucket", Folder: "vbr",
		OverwriteOwner: true,
	})
	if err != nil {
		t.Fatalf("AddS3Repository (OverwriteOwner=true): %v", err)
	}
	if capturedQuery[0] != "true" {
		t.Errorf("call 1: overwriteOwner query param = %q, want \"true\"", capturedQuery[0])
	}

	// Second call: OverwriteOwner false — query param must be absent ("").
	_, err = c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "NormalRepo", Description: "no overwrite",
		CredentialsID: "cred-ow2", Compatible: true,
		ServicePoint: "https://s3.lab.local", RegionID: "default",
		Bucket: "normal-bucket", Folder: "vbr",
		OverwriteOwner: false,
	})
	if err != nil {
		t.Fatalf("AddS3Repository (OverwriteOwner=false): %v", err)
	}
	if capturedQuery[1] != "" {
		t.Errorf("call 2: overwriteOwner query param = %q, want \"\" (absent)", capturedQuery[1])
	}
}

func TestAddS3RepositoryCompatibleMountServer(t *testing.T) {
	// S3Compatible branch must also carry the mountServer block when set.
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		if body["type"] != "S3Compatible" {
			t.Errorf("type = %v, want S3Compatible", body["type"])
		}
		ms, _ := body["mountServer"].(map[string]any)
		if ms == nil {
			t.Fatal("mountServer block missing from S3Compatible body")
		}
		if ms["mountServerSettingsType"] != "Linux" {
			t.Errorf("mountServerSettingsType = %v, want Linux", ms["mountServerSettingsType"])
		}
		linux, _ := ms["linux"].(map[string]any)
		if linux == nil {
			t.Fatal("mountServer.linux missing")
		}
		if linux["mountServerId"] != "managed-compat-1" {
			t.Errorf("mountServerId = %v, want managed-compat-1", linux["mountServerId"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-compat-mount"})
	})
	c, _ := newTestClient(t, mux)

	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "CompatMount", CredentialsID: "cred-cm", Compatible: true,
		ServicePoint: "https://s3.lab.local", RegionID: "default",
		Bucket: "b", Folder: "f",
		MountServerID: "managed-compat-1",
	})
	if err != nil {
		t.Fatalf("AddS3Repository (S3Compatible with mount server): %v", err)
	}
}

func TestSetSyslog(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/generalOptions/eventForwarding", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		body := decode(t, r)
		srv, _ := body["syslogServer"].(map[string]any)
		if srv == nil {
			t.Fatal("missing syslogServer object")
		}
		if srv["serverName"] != "syslog.example.com" {
			t.Errorf("serverName = %v, want syslog.example.com", srv["serverName"])
		}
		if srv["port"].(float64) != 514 {
			t.Errorf("port = %v, want 514", srv["port"])
		}
		if srv["transportProtocol"] != "Tcp" {
			t.Errorf("transportProtocol = %v, want Tcp", srv["transportProtocol"])
		}
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newTestClient(t, mux)

	if err := c.SetSyslog(context.Background(), "syslog.example.com", 514, "Tcp"); err != nil {
		t.Fatalf("SetSyslog: %v", err)
	}
}

func TestSetSyslogDefaults(t *testing.T) {
	// When protocol=="" and port<=0 the method must default to Udp/514.
	mux := baseMux()
	mux.HandleFunc("/api/v1/generalOptions/eventForwarding", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		srv, _ := body["syslogServer"].(map[string]any)
		if srv["port"].(float64) != 514 {
			t.Errorf("default port = %v, want 514", srv["port"])
		}
		if srv["transportProtocol"] != "Udp" {
			t.Errorf("default protocol = %v, want Udp", srv["transportProtocol"])
		}
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newTestClient(t, mux)

	if err := c.SetSyslog(context.Background(), "syslog.example.com", 0, ""); err != nil {
		t.Fatalf("SetSyslog defaults: %v", err)
	}
}

func TestSetNodeExporterNoAuth(t *testing.T) {
	mux := baseMux()
	puts := 0
	mux.HandleFunc("/api/v1/generalOptions/nodeExporterSettings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		puts++
		body := decode(t, r)
		if body["metricsSharingEnabled"] != true {
			t.Errorf("metricsSharingEnabled = %v, want true", body["metricsSharingEnabled"])
		}
		if body["tlsEnabled"] != false {
			t.Errorf("tlsEnabled = %v, want false", body["tlsEnabled"])
		}
		auth, _ := body["auth"].(map[string]any)
		if auth == nil {
			t.Fatal("missing auth object")
		}
		if auth["type"] != "None" {
			t.Errorf("auth.type = %v, want None", auth["type"])
		}
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newTestClient(t, mux)

	if err := c.SetNodeExporter(context.Background(), true, false, "", ""); err != nil {
		t.Fatalf("SetNodeExporter (no auth): %v", err)
	}
	// Ensure setBasicAuth was NOT called (no extra handler registered above).
	if puts != 1 {
		t.Errorf("PUT count = %d, want 1", puts)
	}
}

func TestSetNodeExporterWithBasicAuth(t *testing.T) {
	mux := baseMux()
	var putBody, postBody map[string]any
	mux.HandleFunc("/api/v1/generalOptions/nodeExporterSettings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putBody = decode(t, r)
			w.WriteHeader(http.StatusOK)
			return
		}
		// POST to setBasicAuth lands here because the mux matches the prefix.
		// (The setBasicAuth handler below is more specific and takes priority.)
		t.Errorf("unexpected method %s on /nodeExporterSettings", r.Method)
	})
	mux.HandleFunc("/api/v1/generalOptions/nodeExporterSettings/setBasicAuth", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("setBasicAuth method = %s, want POST", r.Method)
		}
		postBody = decode(t, r)
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newTestClient(t, mux)

	if err := c.SetNodeExporter(context.Background(), true, true, "prometheus", "s3cr3t"); err != nil {
		t.Fatalf("SetNodeExporter (with auth): %v", err)
	}
	// PUT assertions.
	if putBody == nil {
		t.Fatal("PUT was not called")
	}
	auth, _ := putBody["auth"].(map[string]any)
	if auth["type"] != "UsernamePassword" {
		t.Errorf("auth.type = %v, want UsernamePassword", auth["type"])
	}
	if auth["username"] != "prometheus" || auth["password"] != "s3cr3t" {
		t.Errorf("auth credentials = %v", auth)
	}
	if putBody["tlsEnabled"] != true {
		t.Errorf("tlsEnabled = %v, want true", putBody["tlsEnabled"])
	}
	// POST assertions.
	if postBody == nil {
		t.Fatal("POST setBasicAuth was not called")
	}
	if postBody["username"] != "prometheus" || postBody["password"] != "s3cr3t" {
		t.Errorf("setBasicAuth body = %v", postBody)
	}
}

func TestAPIErrorSurfacesMessage(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "boom"})
	})
	c, _ := newTestClient(t, mux)
	_, err := c.CreateCredentials(context.Background(), "u", "p", "")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to contain 'boom'", err)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"s3.example.com", "https://s3.example.com"},
		{"https://s3.example.com", "https://s3.example.com"},
		{"http://h", "http://h"},
		{"  s3.x  ", "https://s3.x"},
	}
	for _, tc := range cases {
		got := normalizeEndpoint(tc.in)
		if got != tc.want {
			t.Errorf("normalizeEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAddS3RepositoryCompatibleNormalizesServicePoint(t *testing.T) {
	// When ServicePoint is a bare host (no scheme), AddS3Repository must send
	// account.servicePoint with an https:// prefix — VBR rejects the bare form.
	mux := baseMux()
	mux.HandleFunc("/api/v1/backupInfrastructure/repositories", func(w http.ResponseWriter, r *http.Request) {
		body := decode(t, r)
		account, _ := body["account"].(map[string]any)
		if account == nil {
			t.Fatal("missing account object")
		}
		if account["servicePoint"] != "https://s3.example.com" {
			t.Errorf("account.servicePoint = %v, want https://s3.example.com", account["servicePoint"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-norm"})
	})
	c, _ := newTestClient(t, mux)

	_, err := c.AddS3Repository(context.Background(), S3RepoSpec{
		Name: "NormRepo", CredentialsID: "cred-n", Compatible: true,
		ServicePoint: "s3.example.com", // bare host — must be normalized
		RegionID:     "eu-west-4", Bucket: "veeam", Folder: "backups",
	})
	if err != nil {
		t.Fatalf("AddS3Repository (normalize servicePoint): %v", err)
	}
}

func TestNewS3CompatibleFolder(t *testing.T) {
	mux := baseMux()
	var gotBody map[string]any
	mux.HandleFunc("/api/v1/cloudBrowser/newFolder", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotBody = decode(t, r)
		w.WriteHeader(http.StatusCreated) // 201 with empty body
	})
	c, _ := newTestClient(t, mux)

	err := c.NewS3CompatibleFolder(context.Background(), "cred-uuid", "s3.eu-west-4.idrivee2.com", "eu-west-4", "veeam", "backups")
	if err != nil {
		t.Fatalf("NewS3CompatibleFolder: %v", err)
	}
	for k, want := range map[string]any{
		"credentialsId":   "cred-uuid",
		"serviceType":     "S3Compatible",
		"newFolderName":   "backups",
		"connectionPoint": "https://s3.eu-west-4.idrivee2.com",
		"regionId":        "eu-west-4",
		"bucketName":      "veeam",
	} {
		if gotBody[k] != want {
			t.Errorf("body[%q] = %v, want %v", k, gotBody[k], want)
		}
	}
}

func TestUpdateHostComponents(t *testing.T) {
	mux := baseMux()
	var gotIDs []any
	mux.HandleFunc("/api/v1/backupInfrastructure/managedServers/updateComponents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body := decode(t, r)
		ids, _ := body["ids"].([]any)
		gotIDs = ids
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-x"})
	})
	c, _ := newTestClient(t, mux)

	sess, err := c.UpdateHostComponents(context.Background(), []string{"host-uuid-1"})
	if err != nil {
		t.Fatalf("UpdateHostComponents: %v", err)
	}
	if sess != "sess-x" {
		t.Errorf("session = %q, want sess-x", sess)
	}
	if len(gotIDs) != 1 || gotIDs[0] != "host-uuid-1" {
		t.Errorf("request body ids = %v, want [host-uuid-1]", gotIDs)
	}
}

// TestClientConcurrentReauth fires many goroutines that all hit a protected
// endpoint simultaneously. The mock server returns 401 for any bearer token it
// has seen before (simulating expiry), then 200 on subsequent calls with the
// newly issued token. The test asserts no data race (passes under -race) and
// that the token endpoint was hit only a small number of times — proof that the
// reauthenticate coalescing worked and not every goroutine re-authed.
func TestClientConcurrentReauth(t *testing.T) {
	const goroutines = 20

	var tokenCalls atomic.Int32
	// issuedToken holds the most-recently issued token value so the protected
	// endpoint can know which token is "current".
	var mu sync.Mutex
	currentTok := "tok-init"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("grant_type") != "Password" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		tokenCalls.Add(1)
		mu.Lock()
		newTok := fmt.Sprintf("tok-%d", tokenCalls.Load())
		currentTok = newTok
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": newTok})
	})
	// Protected endpoint: returns 401 if the bearer token is NOT the current
	// token (i.e. it's a stale token), 200 otherwise.
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		cur := currentTok
		mu.Unlock()
		if bearer != cur {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "stale token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cred-ok"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, Username: "admin", Password: "pw"})
	// Initial authenticate — sets the first token.
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	initialTokenCalls := tokenCalls.Load()

	// Now invalidate the token so all goroutines see a 401 on their first try.
	mu.Lock()
	currentTok = "tok-new-expected"
	mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.CreateCredentials(context.Background(), "u", "p", "desc")
			errs[i] = err
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// The token endpoint should have been called only a small number of extra
	// times (coalesced), not 20×. Any value <= goroutines/2 is fine; in practice
	// the coalescing brings it to 1 or 2.
	extra := tokenCalls.Load() - initialTokenCalls
	if extra > int32(goroutines/2) {
		t.Errorf("token endpoint called %d extra times after concurrent 401s; expected coalescing (want <= %d)", extra, goroutines/2)
	}
}

// newReauthMux returns a fresh mux with a token endpoint that counts its calls.
// This is used by the reauth tests to avoid double-registration with baseMux.
func newReauthMux(tokenCalls *int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		*tokenCalls++
		_ = r.ParseForm()
		if r.PostFormValue("grant_type") != "Password" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-fresh"})
	})
	return mux
}

// TestReauthOn401 verifies that the client transparently re-authenticates
// when the server returns HTTP 401 and retries the request exactly once.
func TestReauthOn401(t *testing.T) {
	tokenCalls := 0
	mux := newReauthMux(&tokenCalls)

	credCalls := 0
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		credCalls++
		// First call: simulate expired token → 401.
		// Second call: succeed.
		if credCalls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "token expired"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cred-reauth"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, Username: "admin", Password: "pw"})
	// Initial authenticate (counts as tokenCalls=1).
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	tokenCallsBefore := tokenCalls

	id, err := c.CreateCredentials(context.Background(), "u", "p", "desc")
	if err != nil {
		t.Fatalf("CreateCredentials after 401: %v", err)
	}
	if id != "cred-reauth" {
		t.Errorf("id = %q, want cred-reauth", id)
	}
	// Exactly one re-auth should have happened.
	if tokenCalls != tokenCallsBefore+1 {
		t.Errorf("token endpoint called %d extra times, want 1", tokenCalls-tokenCallsBefore)
	}
	// The credentials endpoint must have been hit twice (401 then 200).
	if credCalls != 2 {
		t.Errorf("credentials endpoint called %d times, want 2", credCalls)
	}
}

// TestReauthOn401NoPersistentLoop verifies that a persistent 401 (server always
// returns 401) propagates as an error and does not loop infinitely.
func TestReauthOn401NoPersistentLoop(t *testing.T) {
	tokenCalls := 0
	mux := newReauthMux(&tokenCalls)

	credCalls := 0
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		credCalls++
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "always 401"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, Username: "admin", Password: "pw"})
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	_, err := c.CreateCredentials(context.Background(), "u", "p", "desc")
	if err == nil {
		t.Error("expected error on persistent 401, got nil")
	}
	// With one re-auth the credential endpoint is hit at most twice (initial + retry).
	if credCalls > 2 {
		t.Errorf("credentials endpoint called %d times, want <= 2 (no infinite loop)", credCalls)
	}
}
