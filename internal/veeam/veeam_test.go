package veeam

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
