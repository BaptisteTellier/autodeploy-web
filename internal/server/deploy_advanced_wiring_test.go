package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/BaptisteTellier/autodeploy-web/internal/wiring"
)

func formRequest(advancedTicked bool) *url.Values {
	v := &url.Values{}
	if advancedTicked {
		v.Set("wire_advanced", "on")
	}
	// Always submitted, ticked or not:
	v.Set("wire_node_exporter", "on")
	v.Set("wire_node_exporter_tls", "on")
	v.Set("wire_node_exporter_user", "metrics")
	v.Set("wire_node_exporter_pass", "s3cr3t")
	v.Set("wire_syslog_server", "syslog.lab.local")
	v.Set("wire_syslog_port", "1514")
	v.Set("wire_syslog_protocol", "Tcp")
	v.Set("wire_s3", "on")
	v.Set("wire_s3_bucket", "veeam-backups")
	v.Set("wire_s3_endpoint", "s3.lab.local")
	v.Set("wire_s3_access_key", "AKIA")
	v.Set("wire_s3_secret_key", "SECRET")
	return v
}

func applyFor(t *testing.T, advancedTicked, standalone bool) wiring.Config {
	t.Helper()
	body := formRequest(advancedTicked).Encode()
	r := httptest.NewRequest("POST", "/deploy", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var cfg wiring.Config
	applyAdvancedWiring(r, &cfg, standalone)
	return cfg
}

// The bug: unticking "Advanced" still applied node_exporter, syslog and S3,
// because the hidden fields keep being submitted.
func TestAdvancedWiringIgnoredWhenToggleOff(t *testing.T) {
	cfg := applyFor(t, false, false)
	if cfg.NodeExporter {
		t.Error("node_exporter applied while the Advanced toggle was off")
	}
	if cfg.SyslogServer != "" {
		t.Errorf("syslog applied while the Advanced toggle was off: %q", cfg.SyslogServer)
	}
	if cfg.S3 != nil {
		t.Errorf("S3 repository applied while the Advanced toggle was off: %+v", cfg.S3)
	}
}

// ... and it must still work normally when the toggle is on.
func TestAdvancedWiringAppliedWhenToggleOn(t *testing.T) {
	cfg := applyFor(t, true, false)
	if !cfg.NodeExporter {
		t.Error("node_exporter not applied with the Advanced toggle on")
	}
	if cfg.NodeExporterUser != "metrics" || !cfg.NodeExporterTLS {
		t.Errorf("node_exporter details lost: user=%q tls=%v", cfg.NodeExporterUser, cfg.NodeExporterTLS)
	}
	if cfg.SyslogServer != "syslog.lab.local" || cfg.SyslogPort != 1514 || cfg.SyslogProtocol != "Tcp" {
		t.Errorf("syslog details lost: %q %d %q", cfg.SyslogServer, cfg.SyslogPort, cfg.SyslogProtocol)
	}
	if cfg.S3 == nil {
		t.Fatal("S3 not applied with the Advanced toggle on")
	}
	if cfg.S3.Bucket != "veeam-backups" || cfg.S3.Folder != "backups" {
		t.Errorf("S3 details wrong: bucket=%q folder=%q", cfg.S3.Bucket, cfg.S3.Folder)
	}
}

// standalone (add-to-existing) must never apply these, toggle or not: they are
// global VBR settings on someone else's server.
func TestAdvancedWiringSkippedForStandalone(t *testing.T) {
	cfg := applyFor(t, true, true)
	if cfg.NodeExporter || cfg.SyslogServer != "" || cfg.S3 != nil {
		t.Errorf("advanced options leaked into a standalone deploy: %+v", cfg)
	}
}
