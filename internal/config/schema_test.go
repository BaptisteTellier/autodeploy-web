package config

import (
	"encoding/json"
	"strings"
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
    "StaticIP": "192.168.1.100",
    "Subnet": "255.255.255.0",
    "Gateway": "192.168.1.1",
    "DNSServers": ["192.168.1.1", "8.8.8.4", "8.8.8.8"],
    "VeeamAdminPassword": "Aa1!Bb2!Cc3!Dd4!",
    "VeeamAdminMfaSecretKey": "JBSWY3DPEHPK3PXP",
    "VeeamAdminIsMfaEnabled": "true",
    "VeeamSoPassword": "Ee5!Ff6!Gg7!Hh8!",
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
	// SourceISO has no meaningful default — it is always provided by the user at
	// runtime via the form dropdown, so we supply a placeholder here.
	c := Defaults()
	c.SourceISO = "source.iso"
	errs := Validate(c)
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
		{"Aa1!Bb2!Cc3!Dd4!", true, "ok"},
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

// HostsEntries must survive the same shapes autodeploy.ps1 v2.9 accepts:
// an array, a bare string (one entry), and absent/null (feature off).
func TestHostsEntriesFlexParsing(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"array", `{"HostsEntries":["10.0.0.10 vbr01","# note","10.0.0.11 repo01 hr"]}`,
			[]string{"10.0.0.10 vbr01", "# note", "10.0.0.11 repo01 hr"}},
		{"bare string", `{"HostsEntries":"10.0.0.10 vbr01"}`, []string{"10.0.0.10 vbr01"}},
		{"empty array", `{"HostsEntries":[]}`, nil},
		{"absent", `{}`, nil},
		{"null", `{"HostsEntries":null}`, nil},
		{"empty string", `{"HostsEntries":""}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if err := json.Unmarshal([]byte(tc.json), &c); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(c.HostsEntries) != len(tc.want) {
				t.Fatalf("len: want %d, got %d (%q)", len(tc.want), len(c.HostsEntries), c.HostsEntries)
			}
			for i := range tc.want {
				if c.HostsEntries[i] != tc.want[i] {
					t.Fatalf("entry %d: want %q, got %q", i, tc.want[i], c.HostsEntries[i])
				}
			}
		})
	}
}

// The generated JSON is consumed by autodeploy.ps1, so HostsEntries must always
// marshal back as a JSON array -- never as a bare string, even when it was read
// from one.
func TestHostsEntriesMarshalsAsArray(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"HostsEntries":"10.0.0.10 vbr01"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"HostsEntries":["10.0.0.10 vbr01"]`) {
		t.Fatalf("HostsEntries did not marshal as an array: %s", b)
	}
}

// Validate must reject exactly what autodeploy.ps1 rejects -- an embedded line
// break, and an entry that is the heredoc terminator -- and nothing else.
func TestValidateHostsEntries(t *testing.T) {
	base := Defaults()
	base.ApplianceType = "VSA"

	hasHostsErr := func(c Config) bool {
		for _, e := range Validate(c) {
			if e.Field == "HostsEntries" {
				return true
			}
		}
		return false
	}

	ok := base
	ok.HostsEntries = FlexStringArray{"10.0.0.10 vbr01 vbr01.lab.local", "# a comment", "not-an-ip somehost"}
	if hasHostsErr(ok) {
		t.Fatalf("valid entries (incl. comment and odd shape) must not be rejected")
	}

	nl := base
	nl.HostsEntries = FlexStringArray{"10.0.0.1 ok\nrm -rf /"}
	if !hasHostsErr(nl) {
		t.Fatalf("entry with an embedded line break must be rejected")
	}

	eof := base
	eof.HostsEntries = FlexStringArray{"10.0.0.1 ok", "EOF", "echo pwned"}
	if !hasHostsErr(eof) {
		t.Fatalf("entry equal to the heredoc terminator must be rejected")
	}
}
