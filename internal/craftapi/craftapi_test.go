package craftapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/BaptisteTellier/autodeploy-web/internal/craftapi"
	"github.com/BaptisteTellier/autodeploy-web/internal/veeam"
)

// --------------------------------------------------------------------------
// Shared test spec helpers.
// --------------------------------------------------------------------------

func oneVSASpec() craftapi.Spec {
	return craftapi.Spec{
		BaseURL:  "https://192.168.1.10:9419",
		Username: "veeamadmin",
		Password: "secret",
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10", Hostname: "vsa1"},
			{Role: "VIA-HR", IP: "192.168.1.20", Hostname: "hr1"},
			{Role: "VIA-Proxy", IP: "192.168.1.30", Hostname: "proxy1"},
		},
		License:    true,
		LicenseB64: "",
		S3: craftapi.S3{
			Enabled:         true,
			Name:            "MyS3",
			Compatible:      true,
			ServicePoint:    "https://s3.example.com",
			Region:          "us-east-1",
			Bucket:          "mybucket",
			Folder:          "myfolder",
			AccessKey:       "AKID",
			SecretKey:       "SECRET",
			ImmutableDays:   14,
			MountServerNode: "192.168.1.30",
			OverwriteOwner:  true,
		},
		RepoPath:      "/var/lib/veeam/backups",
		ImmutableDays: 7,
	}
}

func twoVSASpec() craftapi.Spec {
	s := oneVSASpec()
	s.Nodes = append([]craftapi.Node{
		{Role: "VSA", IP: "192.168.1.10", Hostname: "vsa1"},
		{Role: "VSA-2", IP: "192.168.1.11", Hostname: "vsa2"},
	}, s.Nodes[1:]...)
	s.ClusterDNSName = "vbr-ha.local"
	s.ClusterEndpoint = "192.168.1.99"
	return s
}

// fullSpec builds a spec with 2×VSA (HA) + 1 HR + 1 proxy + S3 compatible
// w/ folder+mount+overwrite + license + node_exporter w/ basic auth + syslog.
func fullSpec() craftapi.Spec {
	return craftapi.Spec{
		BaseURL:          "https://192.168.1.10:9419",
		Username:         "veeamadmin",
		Password:         "s3cr3t!$pass",
		APIVersion:       "1.3-rev2",
		ClusterDNSName:   "vbr-ha.local",
		ClusterEndpoint:  "192.168.1.99",
		RepoPath:         "/var/lib/veeam/backups",
		ImmutableDays:    7,
		License:          true,
		LicenseB64:       "dGVzdA==",
		NodeExporter:     true,
		NodeExporterTLS:  false,
		NodeExporterUser: "metrics",
		NodeExporterPass: "ne$pass!",
		SyslogServer:     "192.168.1.50",
		SyslogPort:       514,
		SyslogProtocol:   "Udp",
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10", Hostname: "vsa1"},
			{Role: "VSA-2", IP: "192.168.1.11", Hostname: "vsa2"},
			{Role: "VIA-HR", IP: "192.168.1.20", Hostname: "hr1"},
			{Role: "VIA-Proxy", IP: "192.168.1.30", Hostname: "proxy1"},
		},
		S3: craftapi.S3{
			Enabled:         true,
			Name:            "MyS3",
			Compatible:      true,
			ServicePoint:    "https://s3.example.com",
			Region:          "us-east-1",
			Bucket:          "mybucket",
			Folder:          "myfolder",
			AccessKey:       "AK$ID",
			SecretKey:       "SK$ecret!",
			ImmutableDays:   14,
			MountServerNode: "192.168.1.30",
			OverwriteOwner:  true,
		},
	}
}

// --------------------------------------------------------------------------
// TestPlanSequence
// --------------------------------------------------------------------------

func TestPlanSequence(t *testing.T) {
	spec := oneVSASpec()
	steps := craftapi.Plan(spec)

	if len(steps) == 0 {
		t.Fatal("Plan returned no steps")
	}

	// Helper: index of a step matching the predicate; -1 if missing.
	indexOf := func(match func(craftapi.Step) bool) int {
		for i, s := range steps {
			if match(s) {
				return i
			}
		}
		return -1
	}

	// 1. License steps must be present and in order.
	iLicGet := indexOf(func(s craftapi.Step) bool {
		return s.Method == "GET" && s.Path == "/api/v1/license"
	})
	iLicPost := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/license/install"
	})
	if iLicGet < 0 {
		t.Error("missing GET /api/v1/license")
	}
	if iLicPost < 0 {
		t.Error("missing POST /api/v1/license/install")
	}
	if iLicGet >= 0 && iLicPost >= 0 && iLicGet >= iLicPost {
		t.Error("license GET must precede license POST")
	}

	// 2. License body contains the placeholder when LicenseB64 is empty.
	// We inspect the map directly (json.Marshal escapes < and > as </>,
	// so string-searching the marshalled JSON would not find the literal angle brackets).
	if iLicPost >= 0 {
		if m, ok := steps[iLicPost].Body.(map[string]any); ok {
			if v, ok2 := m["license"].(string); !ok2 || v != "<BASE64_OF_YOUR_LIC>" {
				t.Errorf("license install body[\"license\"] = %q, want <BASE64_OF_YOUR_LIC>", v)
			}
		} else {
			t.Error("license install body is not map[string]any")
		}
	}

	// 2a. connectionCertificate must appear before managedServers POST.
	iCertFetch := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/connectionCertificate"
	})
	if iCertFetch < 0 {
		t.Error("missing POST /api/v1/connectionCertificate before addHost")
	}

	// 3. managedServers POST (add host) must appear before updateComponents.
	iAddHost := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/backupInfrastructure/managedServers"
	})
	iUpdateComp := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && strings.HasSuffix(s.Path, "/managedServers/updateComponents")
	})
	if iAddHost < 0 {
		t.Error("missing POST managedServers")
	}
	if iUpdateComp < 0 {
		t.Error("missing POST managedServers/updateComponents")
	}
	if iCertFetch >= 0 && iAddHost >= 0 && iAddHost < iCertFetch {
		t.Error("connectionCertificate must come before addHost")
	}
	if iAddHost >= 0 && iUpdateComp >= 0 && iUpdateComp < iAddHost {
		t.Error("updateComponents must come after addHost")
	}

	// 4. updateComponents must precede repo and proxy creation.
	iRepo := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/backupInfrastructure/repositories"
	})
	iProxy := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/backupInfrastructure/proxies"
	})
	if iUpdateComp >= 0 && iRepo >= 0 && iRepo < iUpdateComp {
		t.Error("repository creation must come after updateComponents")
	}
	if iUpdateComp >= 0 && iProxy >= 0 && iProxy < iUpdateComp {
		t.Error("proxy creation must come after updateComponents")
	}

	// 5. S3 steps: cloudCredentials → newFolder → repositories?overwriteOwner=true.
	iS3Cred := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/cloudCredentials"
	})
	iNewFolder := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/cloudBrowser/newFolder"
	})
	iS3Repo := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && strings.Contains(s.Path, "?overwriteOwner=true")
	})
	if iS3Cred < 0 {
		t.Error("missing POST /api/v1/cloudCredentials")
	}
	if iNewFolder < 0 {
		t.Error("missing POST /api/v1/cloudBrowser/newFolder (S3-compatible + folder set)")
	}
	if iS3Repo < 0 {
		t.Error("missing repositories POST with ?overwriteOwner=true")
	}
	if iS3Cred >= 0 && iNewFolder >= 0 && iNewFolder < iS3Cred {
		t.Error("newFolder must come after cloudCredentials")
	}
	if iNewFolder >= 0 && iS3Repo >= 0 && iS3Repo < iNewFolder {
		t.Error("S3 repository must come after newFolder")
	}

	// 6. HA steps must NOT appear for a single-VSA spec.
	iHA := indexOf(func(s craftapi.Step) bool {
		return s.Method == "POST" && s.Path == "/api/v1/highAvailabilityCluster"
	})
	if iHA >= 0 {
		t.Error("highAvailabilityCluster step must not appear for a single-VSA spec")
	}

	// 7. HA steps MUST appear for a two-VSA spec.
	spec2 := twoVSASpec()
	steps2 := craftapi.Plan(spec2)
	iHA2 := -1
	for i, s := range steps2 {
		if s.Method == "POST" && s.Path == "/api/v1/highAvailabilityCluster" {
			iHA2 = i
			break
		}
	}
	if iHA2 < 0 {
		t.Error("highAvailabilityCluster step must appear for a two-VSA spec")
	}

	// 8. Mount-server GET must appear when MountServerNode is set.
	iMount := indexOf(func(s craftapi.Step) bool {
		return s.Method == "GET" && strings.Contains(s.Path, "managedServers?nameFilter=")
	})
	if iMount < 0 {
		t.Error("missing managed-server lookup for S3 mount server")
	}
}

// --------------------------------------------------------------------------
// TestRenderPowerShell
// --------------------------------------------------------------------------

func TestRenderPowerShell(t *testing.T) {
	spec := twoVSASpec()
	out, err := craftapi.RenderPowerShell(spec)
	if err != nil {
		t.Fatalf("RenderPowerShell error: %v", err)
	}

	checks := []struct {
		desc string
		want string
	}{
		{"auth call present", "/api/oauth2/token"},
		{"SkipCertificateCheck present", "-SkipCertificateCheck"},
		{"x-api-version header", "x-api-version"},
		{"Wait-VbrSession helper defined", "function Wait-VbrSession"},
		{"wait-session helper call", "Wait-VbrSession"},
		{"managedServers endpoint", "managedServers"},
		{"updateComponents endpoint", "updateComponents"},
		{"repositories endpoint", "repositories"},
		{"cloudBrowser/newFolder endpoint", "cloudBrowser/newFolder"},
		{"highAvailabilityCluster (HA spec)", "highAvailabilityCluster"},
		{"license how-to comment (Convert::ToBase64String)", "[Convert]::ToBase64String"},
		{"license placeholder", "<BASE64_OF_YOUR_LIC>"},
		{"no single-quoted here-string bodies", ""},
		{"hashtable body style", "ConvertTo-Json"},
	}

	for _, c := range checks {
		if c.desc == "no single-quoted here-string bodies" {
			if strings.Contains(out, "@'") {
				t.Errorf("RenderPowerShell: output contains single-quoted here-string @' — bodies must use hashtable+ConvertTo-Json")
			}
			continue
		}
		if c.want != "" && !strings.Contains(out, c.want) {
			t.Errorf("RenderPowerShell: %s — want %q in output", c.desc, c.want)
		}
	}
}

// --------------------------------------------------------------------------
// TestRenderCurl
// --------------------------------------------------------------------------

func TestRenderCurl(t *testing.T) {
	spec := twoVSASpec()
	out, err := craftapi.RenderCurl(spec)
	if err != nil {
		t.Fatalf("RenderCurl error: %v", err)
	}

	checks := []struct {
		desc string
		want string
	}{
		{"bash shebang", "#!/usr/bin/env bash"},
		{"auth call present", "/api/oauth2/token"},
		{"curl -k flag", "curl -sk"},
		{"x-api-version header", "x-api-version"},
		{"wait_session helper defined", "wait_session()"},
		{"wait_session called", "wait_session "},
		{"managedServers endpoint", "managedServers"},
		{"updateComponents endpoint", "updateComponents"},
		{"repositories endpoint", "repositories"},
		{"cloudBrowser/newFolder endpoint", "cloudBrowser/newFolder"},
		{"highAvailabilityCluster (HA spec)", "highAvailabilityCluster"},
		{"license how-to comment (base64 -w0)", "base64 -w0"},
		{"license placeholder", "<BASE64_OF_YOUR_LIC>"},
		{"jq usage", "jq"},
		{"heredoc body (no single-quoted -d '...{$var}')", "<<JSON"},
	}

	for _, c := range checks {
		if !strings.Contains(out, c.want) {
			t.Errorf("RenderCurl: %s — want %q in output", c.desc, c.want)
		}
	}
}

// --------------------------------------------------------------------------
// TestPlanMatchesVeeamClient (drift guard)
// --------------------------------------------------------------------------
// For each key veeam.Client method we spin up a recording httptest server,
// call the real method, then compare the path and body with what Plan() emits.

// recorded captures one HTTP request's method, path, and decoded body.
type recorded struct {
	method string
	path   string
	body   map[string]any
}

// newRecorder builds an httptest.Server that records the first non-auth POST/PUT
// it receives and returns a canned session-id response.
func newRecorder(t *testing.T) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always accept the auth token request.
		if r.URL.Path == "/api/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token"}`)
			return
		}
		// Session poll — return Succeeded immediately.
		if strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"state":"Stopped","result":{"result":"Success"}}`)
			return
		}
		// connectionCertificate stub — return a fake fingerprint.
		if r.URL.Path == "/api/v1/connectionCertificate" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"fingerprint":"AA:BB:CC","certificateUpload":{"certificate":""}}`)
			return
		}
		// Record the call.
		rec.method = r.Method
		rec.path = r.URL.RequestURI() // includes query string
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&rec.body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test-session-id"}`)
	}))
	return srv, rec
}

// newVeeamClient builds a veeam.Client that trusts the test TLS server.
func newVeeamClient(srv *httptest.Server) *veeam.Client {
	c := veeam.New(veeam.Config{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Insecure: true,
	})
	return c
}

func mustAuthenticate(t *testing.T, c *veeam.Client) {
	t.Helper()
	if err := c.Authenticate(context.Background()); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
}

// planStepFor returns the first Step in Plan(spec) whose Path matches the
// prefix (ignoring query string) or that contains pathSubstr.
func planStepForPath(spec craftapi.Spec, pathSubstr string) *craftapi.Step {
	for _, s := range craftapi.Plan(spec) {
		if strings.Contains(s.Path, pathSubstr) {
			cp := s
			return &cp
		}
	}
	return nil
}

// jsonMarshal is a helper to get comparable JSON bytes.
func jsonMarshal(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return m
}

// assertPathEqual checks that the real veeam client path equals the plan step path,
// ignoring any protocol+host prefix from the test server.
func assertPathEqual(t *testing.T, label, realPath, planPath string) {
	t.Helper()
	// planPath may contain $var_id placeholder text — strip it for comparison
	// by normalising to the static prefix up to the first '$'.
	staticPlan := planPath
	if idx := strings.IndexByte(planPath, '$'); idx >= 0 {
		staticPlan = planPath[:idx]
	}
	if !strings.HasPrefix(realPath, staticPlan) {
		t.Errorf("%s: path mismatch\n  veeam client sent: %s\n  plan step path:    %s", label, realPath, planPath)
	}
}

// assertBodyField checks that a top-level field in the real body matches the
// plan body (both decoded from JSON).
func assertBodyField(t *testing.T, label, field string, realBody, planBody map[string]any) {
	t.Helper()
	rv, rOK := realBody[field]
	pv, pOK := planBody[field]
	if !rOK && !pOK {
		return // both absent — fine
	}
	if rOK != pOK {
		if !pOK {
			t.Errorf("%s: field %q present in veeam body but absent in plan body", label, field)
		} else {
			t.Errorf("%s: field %q present in plan body but absent in veeam body", label, field)
		}
		return
	}
	// Compare as JSON strings for simplicity.
	rb, _ := json.Marshal(rv)
	pb, _ := json.Marshal(pv)
	if string(rb) != string(pb) {
		t.Errorf("%s: field %q mismatch\n  veeam: %s\n  plan:  %s", label, field, rb, pb)
	}
}

// --------------------------------------------------------------------------
// AddLinuxHost drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_AddLinuxHost(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	ip := "192.168.1.20"
	role := "VIA-HR"
	handshake := "000000"
	_, _ = c.AddLinuxHost(context.Background(), ip, role, handshake, "FAKEFP")

	spec := craftapi.Spec{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10"},
			{Role: role, IP: ip, PairingCode: handshake},
		},
	}

	st := planStepForPath(spec, "/backupInfrastructure/managedServers")
	if st == nil {
		t.Fatal("Plan: no managedServers step found")
	}

	assertPathEqual(t, "AddLinuxHost", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	planBody := jsonMarshal(t, st.Body)

	// sshFingerprint is now a $cert0_fp variable reference — skip asserting it.
	for _, field := range []string{"type", "credentialsStorageType", "handshakeCode"} {
		assertBodyField(t, "AddLinuxHost", field, rec.body, planBody)
	}
}

// --------------------------------------------------------------------------
// UpdateHostComponents drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_UpdateHostComponents(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	_, _ = c.UpdateHostComponents(context.Background(), []string{"host-uuid-1"})

	spec := craftapi.Spec{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10"},
			{Role: "VIA-HR", IP: "192.168.1.20"},
		},
	}
	st := planStepForPath(spec, "updateComponents")
	if st == nil {
		t.Fatal("Plan: no updateComponents step found")
	}

	assertPathEqual(t, "UpdateHostComponents", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	// Both must have an "ids" field.
	if _, ok := rec.body["ids"]; !ok {
		t.Error("UpdateHostComponents: real body missing 'ids' field")
	}
	planBody := jsonMarshal(t, st.Body)
	if _, ok := planBody["ids"]; !ok {
		t.Error("UpdateHostComponents: plan body missing 'ids' field")
	}
}

// --------------------------------------------------------------------------
// AddHardenedRepository drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_AddHardenedRepository(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	_, _ = c.AddHardenedRepository(context.Background(),
		"HR-hr1", "host-uuid", "/var/lib/veeam/backups", "", true, 7)

	spec := craftapi.Spec{
		BaseURL:       srv.URL,
		Username:      "veeamadmin",
		Password:      "secret",
		RepoPath:      "/var/lib/veeam/backups",
		ImmutableDays: 7,
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10"},
			{Role: "VIA-HR", IP: "192.168.1.20", Hostname: "hr1"},
		},
	}
	st := planStepForPath(spec, "/backupInfrastructure/repositories")
	if st == nil {
		t.Fatal("Plan: no repositories step found")
	}

	assertPathEqual(t, "AddHardenedRepository", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	planBody := jsonMarshal(t, st.Body)

	for _, field := range []string{"type", "name"} {
		assertBodyField(t, "AddHardenedRepository", field, rec.body, planBody)
	}
	// Verify "type" is "LinuxHardened" in both.
	if rec.body["type"] != "LinuxHardened" {
		t.Errorf("AddHardenedRepository: real body type=%v, want LinuxHardened", rec.body["type"])
	}
	if planBody["type"] != "LinuxHardened" {
		t.Errorf("AddHardenedRepository: plan body type=%v, want LinuxHardened", planBody["type"])
	}
}

// --------------------------------------------------------------------------
// AddVmwareProxy drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_AddVmwareProxy(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	_, _ = c.AddVmwareProxy(context.Background(), "host-uuid", 4)

	spec := craftapi.Spec{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Nodes: []craftapi.Node{
			{Role: "VSA", IP: "192.168.1.10"},
			{Role: "VIA-Proxy", IP: "192.168.1.30", Hostname: "proxy1"},
		},
	}
	st := planStepForPath(spec, "/backupInfrastructure/proxies")
	if st == nil {
		t.Fatal("Plan: no proxies step found")
	}

	assertPathEqual(t, "AddVmwareProxy", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	planBody := jsonMarshal(t, st.Body)

	for _, field := range []string{"type", "description"} {
		assertBodyField(t, "AddVmwareProxy", field, rec.body, planBody)
	}
	// Check nested server.maxTaskCount.
	realServer, rOK := rec.body["server"].(map[string]any)
	planServer, pOK := planBody["server"].(map[string]any)
	if rOK && pOK {
		rb, _ := json.Marshal(realServer["maxTaskCount"])
		pb, _ := json.Marshal(planServer["maxTaskCount"])
		if string(rb) != string(pb) {
			t.Errorf("AddVmwareProxy: server.maxTaskCount mismatch: real=%s plan=%s", rb, pb)
		}
	}
}

// --------------------------------------------------------------------------
// NewS3CompatibleFolder drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_NewS3CompatibleFolder(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	_ = c.NewS3CompatibleFolder(context.Background(),
		"cred-uuid", "https://s3.example.com", "us-east-1", "mybucket", "myfolder")

	spec := craftapi.Spec{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Nodes:    []craftapi.Node{{Role: "VSA", IP: "192.168.1.10"}},
		S3: craftapi.S3{
			Enabled:      true,
			Name:         "MyS3",
			Compatible:   true,
			ServicePoint: "https://s3.example.com",
			Region:       "us-east-1",
			Bucket:       "mybucket",
			Folder:       "myfolder",
			AccessKey:    "AKID",
			SecretKey:    "SECRET",
		},
	}
	st := planStepForPath(spec, "/cloudBrowser/newFolder")
	if st == nil {
		t.Fatal("Plan: no cloudBrowser/newFolder step found")
	}

	assertPathEqual(t, "NewS3CompatibleFolder", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	planBody := jsonMarshal(t, st.Body)

	for _, field := range []string{"serviceType", "bucketName", "newFolderName", "regionId"} {
		assertBodyField(t, "NewS3CompatibleFolder", field, rec.body, planBody)
	}
}

// --------------------------------------------------------------------------
// AddS3Repository drift guard.
// --------------------------------------------------------------------------
func TestPlanMatchesVeeamClient_AddS3Repository(t *testing.T) {
	srv, rec := newRecorder(t)
	defer srv.Close()
	c := newVeeamClient(srv)
	mustAuthenticate(t, c)

	_, _ = c.AddS3Repository(context.Background(), veeam.S3RepoSpec{
		Name:           "MyS3",
		Description:    "autodeploy object storage",
		CredentialsID:  "cred-uuid",
		Compatible:     true,
		ServicePoint:   "https://s3.example.com",
		RegionID:       "us-east-1",
		Bucket:         "mybucket",
		Folder:         "myfolder",
		ImmutableDays:  14,
		OverwriteOwner: true,
	})

	spec := craftapi.Spec{
		BaseURL:  srv.URL,
		Username: "veeamadmin",
		Password: "secret",
		Nodes:    []craftapi.Node{{Role: "VSA", IP: "192.168.1.10"}},
		S3: craftapi.S3{
			Enabled:        true,
			Name:           "MyS3",
			Compatible:     true,
			ServicePoint:   "https://s3.example.com",
			Region:         "us-east-1",
			Bucket:         "mybucket",
			Folder:         "myfolder",
			AccessKey:      "AKID",
			SecretKey:      "SECRET",
			ImmutableDays:  14,
			OverwriteOwner: true,
		},
	}
	st := planStepForPath(spec, "?overwriteOwner=true")
	if st == nil {
		t.Fatal("Plan: no S3 repository step with ?overwriteOwner=true found")
	}

	assertPathEqual(t, "AddS3Repository", rec.path, st.Path)

	if rec.body == nil {
		t.Fatal("veeam client sent no body")
	}
	planBody := jsonMarshal(t, st.Body)

	for _, field := range []string{"type", "name", "description"} {
		assertBodyField(t, "AddS3Repository", field, rec.body, planBody)
	}
	// Both must use S3Compatible type.
	if rec.body["type"] != "S3Compatible" {
		t.Errorf("AddS3Repository: real body type=%v, want S3Compatible", rec.body["type"])
	}
	if planBody["type"] != "S3Compatible" {
		t.Errorf("AddS3Repository: plan body type=%v, want S3Compatible", planBody["type"])
	}
}

// --------------------------------------------------------------------------
// TestRenderedScriptsHaveNoDanglingVars
// --------------------------------------------------------------------------
// Renders a full spec (2×VSA HA + 1 proxy + 1 HR + S3 compatible w/
// folder+mount+overwrite + license + node_exporter w/ basic auth + syslog)
// and verifies:
//
//   - PowerShell: no @' here-strings; every $varref is assigned before first use
//     (by line order), using a whitelist of preamble/builtin names.
//   - curl: every ${varref} is assigned before first use, similarly.
func TestRenderedScriptsHaveNoDanglingVars(t *testing.T) {
	spec := fullSpec()

	t.Run("PowerShell", func(t *testing.T) {
		out, err := craftapi.RenderPowerShell(spec)
		if err != nil {
			t.Fatalf("RenderPowerShell: %v", err)
		}

		// 1. No single-quoted here-string bodies.
		if strings.Contains(out, "@'") {
			t.Error("output contains @' (single-quoted here-string) — bodies must use hashtable style")
		}

		// 2. Collect all defined (assigned) variable base-names.
		//    Preamble/builtin whitelist.
		defined := map[string]bool{
			"BaseURL": true, "Username": true, "Password": true,
			"APIVersion": true, "Headers": true, "TokenResponse": true,
			"S3AccessKey": true, "S3SecretKey": true, "NeBasicPass": true,
			// function parameters and locals
			"SessionId": true, "PollSeconds": true, "TimeoutSeconds": true,
			"deadline": true, "s": true, "cfg": true, "resp": true, "body": true,
			// misc builtins
			"true": true, "false": true, "null": true, "_": true,
		}

		// Assignment pattern: $Var = ... (LHS) or param([type]$Var ...)
		assignRe := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)\s*=`)
		paramRe := regexp.MustCompile(`param\([^)]*\$([A-Za-z_][A-Za-z0-9_]*)`)

		lines := strings.Split(out, "\n")
		for _, line := range lines {
			for _, m := range assignRe.FindAllStringSubmatch(line, -1) {
				defined[m[1]] = true
			}
			for _, m := range paramRe.FindAllStringSubmatch(line, -1) {
				defined[m[1]] = true
			}
		}

		// 3. Check every reference.
		// Strip single-quoted string content first so $-signs inside 'literal values'
		// are not mistaken for variable references (PS single-quoted = no expansion).
		singleQuotedRe := regexp.MustCompile(`'[^']*'`)
		refRe := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
		for i, line := range lines {
			// Skip comment lines.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			// Remove single-quoted literals before scanning for variable refs.
			scanLine := singleQuotedRe.ReplaceAllString(line, "''")
			for _, m := range refRe.FindAllStringSubmatch(scanLine, -1) {
				varName := m[1]
				// Skip single-char loop vars and PS automatic variables.
				if len(varName) == 1 {
					continue
				}
				if !defined[varName] {
					t.Errorf("PowerShell line %d: reference to undefined variable $%s\n  line: %s", i+1, varName, line)
				}
			}
		}
	})

	t.Run("curl", func(t *testing.T) {
		out, err := craftapi.RenderCurl(spec)
		if err != nil {
			t.Fatalf("RenderCurl: %v", err)
		}

		// 1. Collect defined variable names.
		defined := map[string]bool{
			"BASE_URL": true, "USERNAME": true, "PASSWORD": true,
			// Password is defined as alias for ${PASSWORD} in the preamble.
			"Password":    true,
			"API_VERSION": true, "TOKEN": true,
			"S3AccessKey": true, "S3SecretKey": true, "NeBasicPass": true,
			// function locals and helpers
			"sid": true, "poll": true, "timeout": true, "elapsed": true,
			"resp": true, "result": true, "state": true,
			"cfg": true, "body": true,
		}

		// Assignment patterns: NAME=... or local NAME=... or NAME=$(...)
		assignRe := regexp.MustCompile(`(?:^|\s|local\s+)([A-Za-z_][A-Za-z0-9_]*)=`)

		lines := strings.Split(out, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, m := range assignRe.FindAllStringSubmatch(line, -1) {
				defined[m[1]] = true
			}
		}

		// 2. Check every ${VAR} reference.
		refRe := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, m := range refRe.FindAllStringSubmatch(line, -1) {
				varName := m[1]
				if !defined[varName] {
					t.Errorf("curl line %d: reference to undefined variable ${%s}\n  line: %s", i+1, varName, line)
				}
			}
		}
	})
}

// TestPlanNormalizesLicense verifies the license install body is normalised to
// canonical base64 (raw XML gets encoded), not embedded verbatim — the verbatim
// bug produced VBR "Data at the root level is invalid".
func TestPlanNormalizesLicense(t *testing.T) {
	xml := `<?xml version="1.0"?><Licenses><License>x</License></Licenses>`
	want := base64.StdEncoding.EncodeToString([]byte(xml))
	steps := craftapi.Plan(craftapi.Spec{
		License: true, LicenseB64: xml,
		Nodes: []craftapi.Node{{Role: "VSA", IP: "10.0.0.1"}},
	})
	var got string
	for _, st := range steps {
		if st.Path == "/api/v1/license/install" {
			got, _ = st.Body.(map[string]any)["license"].(string)
		}
	}
	if got == xml {
		t.Fatal("license embedded raw XML verbatim (the bug)")
	}
	if got != want {
		t.Errorf("license = %q, want canonical base64 %q", got, want)
	}
}
