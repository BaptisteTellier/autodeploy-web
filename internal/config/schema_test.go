package config

import (
	"encoding/json"
	"testing"
)

const sampleProductionJSON = `{
    "SourceISO": "VeeamSoftwareAppliance_13.0.1.180_20251101.iso",
    "OutputISO": "",
    "ApplianceType": "VSA",
    "InPlace": false,
    "CreateBackup": true,
    "CleanupCFGFiles": true,
    "CFGOnly": false,
    "GrubTimeout": 0,
    "KeyboardLayout": "fr",
    "Timezone": "Europe/Paris",
    "Hostname": "veeam-backup",
    "UseDHCP": false,
    "StaticIP": "192.168.1.166",
    "Subnet": "255.255.255.0",
    "Gateway": "192.168.1.1",
    "DNSServers": ["192.168.1.64", "8.8.8.4", "8.8.8.8"],
    "VeeamAdminPassword": "123q123Q123!123",
    "VeeamAdminMfaSecretKey": "JBSWY3DPEHPK3PXP",
    "VeeamAdminIsMfaEnabled": "true",
    "VeeamSoPassword": "123w123W123!123",
    "VeeamSoMfaSecretKey": "JBSWY3DPEHPK3PXP",
    "VeeamSoIsMfaEnabled": "true",
    "VeeamSoRecoveryToken": "12345678-90ab-cdef-1234-567890abcdef",
    "VeeamSoIsEnabled": "true",
    "NtpServer": ["time.nist.gov", "0.fr.pool.ntp.org"],
    "NtpRunSync": "true",
    "ExternalManagersInstallationEnabled": false,
    "ExternalManagersInstallationTimeout": 3600,
    "HighAvailabilityEnabled": false,
    "HighAvailabilityTimeout": 3600,
    "NodeExporter": false,
    "NodeExporterTLSEnabled": false,
    "LicenseVBRTune": false,
    "LicenseFile": "Veeam-100instances-entplus-monitoring-nfr.lic",
    "SyslogServer": "",
    "VCSPConnection": false,
    "VCSPUrl": "",
    "VCSPLogin": "",
    "VCSPPassword": "",
    "RestoreConfig": false,
    "ConfigPasswordSo": "",
    "Debug": false
}`

func TestSampleProductionRoundtrip(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(sampleProductionJSON), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.ApplianceType != "VSA" {
		t.Fatalf("ApplianceType: want VSA, got %q", c.ApplianceType)
	}
	if !bool(c.VeeamAdminIsMfaEnabled) {
		t.Fatalf("VeeamAdminIsMfaEnabled: want true (parsed from string)")
	}
	if len(c.DNSServers) != 3 {
		t.Fatalf("DNSServers: want 3, got %d", len(c.DNSServers))
	}
	if len(c.NtpServer) != 2 {
		t.Fatalf("NtpServer: want 2, got %d", len(c.NtpServer))
	}
}

func TestFlexBoolString(t *testing.T) {
	cases := map[string]bool{
		`"true"`:  true,
		`"True"`:  true,
		`true`:    true,
		`"false"`: false,
		`false`:   false,
		`null`:    false,
	}
	for in, want := range cases {
		var f FlexBool
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("input %s: %v", in, err)
		}
		if bool(f) != want {
			t.Fatalf("input %s: want %v, got %v", in, want, f)
		}
	}
}

func TestFlexStringArrayScalar(t *testing.T) {
	var s FlexStringArray
	if err := json.Unmarshal([]byte(`"only.example"`), &s); err != nil {
		t.Fatal(err)
	}
	if len(s) != 1 || s[0] != "only.example" {
		t.Fatalf("want [only.example], got %v", s)
	}
}

func TestDefaultsValid(t *testing.T) {
	// Defaults should pass validation (admin/SO passwords meet complexity, etc.)
	errs := Validate(Defaults())
	if len(errs) != 0 {
		t.Fatalf("defaults should validate, got: %v", errs)
	}
}

func TestPasswordComplexity(t *testing.T) {
	cases := []struct {
		pw   string
		ok   bool
		name string
	}{
		{"short", false, "too short"},
		{"123q123Q123!123", true, "ok"},
		{"abcdefghijklmno", false, "no upper/digit/symbol"},
		{"AAAAaaaa1111!!!!", false, "4 of same class in a row"},
		{"AaaaAaaaBbbb1!2@", true, "mixed ok"},
	}
	for _, tc := range cases {
		err := checkVeeamPassword(tc.pw)
		if (err == "") != tc.ok {
			t.Errorf("%s: want ok=%v, got err=%q", tc.name, tc.ok, err)
		}
	}
}

func TestValidateRejectsVIAOnVSAFlag(t *testing.T) {
	c := Defaults()
	c.ApplianceType = "VSA"
	c.VIASingleDisk = true
	errs := Validate(c)
	if len(errs) == 0 {
		t.Fatal("expected error for VIASingleDisk on VSA")
	}
}

func TestValidateRejectsNodeExporterOnNonVSA(t *testing.T) {
	c := Defaults()
	c.ApplianceType = "VIA"
	c.NodeExporter = true
	errs := Validate(c)
	if len(errs) == 0 {
		t.Fatal("expected error for NodeExporter on VIA")
	}
}

func TestMarshalEmitsStringBooleans(t *testing.T) {
	c := Defaults()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// VeeamAdminIsMfaEnabled must serialise as "true" (string), not true
	if !contains2(b, `"VeeamAdminIsMfaEnabled":"true"`) {
		t.Fatalf("expected string boolean for VeeamAdminIsMfaEnabled, got: %s", string(b))
	}
}

func contains2(haystack []byte, needle string) bool {
	return indexOf(string(haystack), needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
