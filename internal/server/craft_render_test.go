package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleCraftAPIRenderMultipart posts the form the way the page does
// (FormData → multipart/form-data) and asserts the rendered PowerShell actually
// reflects the submitted nodes, derived BaseURL and password. Guards against the
// regression where the handler used ParseForm (which ignores a multipart body)
// and rendered an empty script.
func TestHandleCraftAPIRenderMultipart(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{
		"username": "veeamadmin", "password": "S3cr3t!pw", "api_version": "1.3-rev2",
		"node_0_role": "VSA", "node_0_ip": "192.168.1.169", "node_0_hostname": "vsa01",
		"node_1_role": "VIA-Proxy", "node_1_ip": "192.168.1.168", "node_1_hostname": "proxy01",
		"node_2_role": "VIA-HR", "node_2_ip": "192.168.1.148", "node_2_hostname": "hr01",
	} {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/craft-api/render", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()

	(&Server{}).handleCraftAPIRender(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ps := out["powershell"]
	if strings.Contains(ps, "<VSA_IP>") {
		t.Error("BaseURL not derived — multipart form not parsed")
	}
	for _, want := range []string{
		"https://192.168.1.169:9419", "S3cr3t!pw",
		"/api/v1/backupInfrastructure/managedServers",
		"updateComponents",
		"/api/v1/backupInfrastructure/repositories",
		"/api/v1/backupInfrastructure/proxies",
	} {
		if !strings.Contains(ps, want) {
			t.Errorf("rendered PowerShell missing %q", want)
		}
	}
}
