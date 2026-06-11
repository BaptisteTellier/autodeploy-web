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

func TestAuthenticateAndBearer(t *testing.T) {
	mux := baseMux()
	var gotAuth string
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
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
		case "result": // result.status Success
			resp = map[string]any{"state": "Running", "result": map[string]any{"status": "Success"}}
		case "fail":
			resp = map[string]any{"state": "Running", "result": map[string]any{"status": "Failed"}}
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
