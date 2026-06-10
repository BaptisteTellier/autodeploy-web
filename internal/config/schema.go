// Package config holds the JSON schema consumed by autodeploy.ps1.
// Field names and types are a 1:1 mirror of the keys documented in the
// upstream README (v2.8). Renaming or removing a field is a breaking change.
package config

import (
	"encoding/json"
	"fmt"
)

// Config matches the JSON consumed by autodeploy.ps1 -ConfigFile <json>.
//
// FlexBool / FlexStringArray exist because the upstream PS1 historically
// accepts both "true"/"false" strings and real booleans for some keys, and
// both a single string and an array for NtpServer / DNSServers.
//
// ⚠ ADDING OR RENAMING A FIELD — the JSON tag doubles as the HTML form
// `name=` attribute, an undocumented contract spanning 8 files. Update ALL of:
//  1. this file (struct + tag)
//  2. defaults.go             (default value)
//  3. validate.go             (server-side rules)
//  4. server/form_decode.go   (configFromForm parsing + VSA/VIA guards)
//  5. server/views/form.html  (expert-form input)
//  6. server/views/wizard.html (wizard-step input)
//  7. server/static/app.js    (STRING_BOOL_KEYS / REAL_BOOL_KEYS / INT_KEYS /
//     ARRAY_KEYS sets if the field is not a plain string)
//  8. server/i18n.go          (form.* label + wiz.help.* tooltip, EN AND FR)
// A missed file fails silently: the field decodes to its zero value.
type Config struct {
	// --- Core
	SourceISO       string `json:"SourceISO"`
	OutputISO       string `json:"OutputISO"`
	ApplianceType   string `json:"ApplianceType"` // VSA | VIA | VIAVMware | VIAHR
	InPlace         bool   `json:"InPlace"`
	CreateBackup    bool   `json:"CreateBackup"`
	CleanupCFGFiles bool   `json:"CleanupCFGFiles"`
	CFGOnly         bool   `json:"CFGOnly"`
	GrubTimeout     int    `json:"GrubTimeout"`

	// --- Regional
	KeyboardLayout string `json:"KeyboardLayout"`
	Timezone       string `json:"Timezone"`
	Hostname       string `json:"Hostname"`

	// --- Network
	UseDHCP    bool            `json:"UseDHCP"`
	StaticIP   string          `json:"StaticIP"`
	Subnet     string          `json:"Subnet"`
	Gateway    string          `json:"Gateway"`
	DNSServers FlexStringArray `json:"DNSServers"`

	// --- Veeam accounts
	VeeamAdminPassword     string   `json:"VeeamAdminPassword"`
	VeeamAdminMfaSecretKey string   `json:"VeeamAdminMfaSecretKey"`
	VeeamAdminIsMfaEnabled FlexBool `json:"VeeamAdminIsMfaEnabled"`
	VeeamSoPassword        string   `json:"VeeamSoPassword"`
	VeeamSoMfaSecretKey    string   `json:"VeeamSoMfaSecretKey"`
	VeeamSoIsMfaEnabled    FlexBool `json:"VeeamSoIsMfaEnabled"`
	VeeamSoRecoveryToken   string   `json:"VeeamSoRecoveryToken"`
	VeeamSoIsEnabled       FlexBool `json:"VeeamSoIsEnabled"`

	// --- NTP
	NtpServer  FlexStringArray `json:"NtpServer"`
	NtpRunSync FlexBool        `json:"NtpRunSync"`

	// --- VSA 13.1+
	ExternalManagersInstallationEnabled bool `json:"ExternalManagersInstallationEnabled"`
	ExternalManagersInstallationTimeout int  `json:"ExternalManagersInstallationTimeout"`
	HighAvailabilityEnabled             bool `json:"HighAvailabilityEnabled"`
	HighAvailabilityTimeout             int  `json:"HighAvailabilityTimeout"`

	// --- Monitoring (VSA-only)
	NodeExporter           bool `json:"NodeExporter"`
	NodeExporterTLSEnabled bool `json:"NodeExporterTLSEnabled"`

	// --- VBR tuning (VSA-only)
	LicenseVBRTune bool   `json:"LicenseVBRTune"`
	LicenseFile    string `json:"LicenseFile"`
	SyslogServer   string `json:"SyslogServer"`

	// --- VCSP (VSA-only)
	VCSPConnection bool   `json:"VCSPConnection"`
	VCSPUrl        string `json:"VCSPUrl"`
	VCSPLogin      string `json:"VCSPLogin"`
	VCSPPassword   string `json:"VCSPPassword"`

	// --- Config restore
	RestoreConfig    bool   `json:"RestoreConfig"`
	ConfigPasswordSo string `json:"ConfigPasswordSo"`

	// --- VIA-only
	VIASingleDisk bool `json:"VIASingleDisk"`

	// --- Debug
	Debug bool `json:"Debug"`
}

// FlexBool accepts both real booleans and "true"/"false" strings (the PS1
// uses string booleans for MFA / NtpRunSync / VeeamSoIsEnabled).
type FlexBool bool

func (f *FlexBool) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	switch string(b) {
	case "true", `"true"`, `"True"`, `"TRUE"`:
		*f = true
		return nil
	case "false", `"false"`, `"False"`, `"FALSE"`, "null":
		*f = false
		return nil
	}
	// Fallback: try generic bool unmarshal.
	var v bool
	if err := json.Unmarshal(b, &v); err == nil {
		*f = FlexBool(v)
		return nil
	}
	return fmt.Errorf("FlexBool: cannot parse %s", string(b))
}

// MarshalJSON always emits string form to stay byte-compatible with samples
// shipped by the upstream PS1 README.
func (f FlexBool) MarshalJSON() ([]byte, error) {
	if f {
		return []byte(`"true"`), nil
	}
	return []byte(`"false"`), nil
}

// FlexStringArray accepts either a single string or an array of strings.
// Always serialises as an array.
type FlexStringArray []string

func (s *FlexStringArray) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	if one == "" {
		*s = nil
	} else {
		*s = []string{one}
	}
	return nil
}

func (s FlexStringArray) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal([]string(s))
}
