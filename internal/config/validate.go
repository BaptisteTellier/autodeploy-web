package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// ValidationError aggregates per-field validation issues.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	if len(v) == 0 {
		return ""
	}
	parts := make([]string, 0, len(v))
	for _, e := range v {
		parts = append(parts, e.Field+": "+e.Message)
	}
	return strings.Join(parts, "; ")
}

var (
	hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,13}[a-zA-Z0-9])?$`)
	base32Re   = regexp.MustCompile(`^[A-Z2-7]{16,32}$`)
	guidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Validate replicates the validation rules of autodeploy.ps1. It is intended
// to give the user immediate feedback in the web UI — the PS1 itself still
// performs the authoritative checks at run-time.
func Validate(c Config) ValidationErrors {
	var errs ValidationErrors
	add := func(field, msg string) { errs = append(errs, ValidationError{field, msg}) }

	// Appliance
	if !contains(ApplianceTypes, c.ApplianceType) {
		add("ApplianceType", "must be one of VSA, VIA, VIAVMware, VIAHR")
	}
	if c.SourceISO == "" {
		add("SourceISO", "required")
	}
	if c.GrubTimeout < 0 || c.GrubTimeout > 300 {
		add("GrubTimeout", "must be between 0 and 300 seconds")
	}

	// Regional
	if len(c.Hostname) == 0 || len(c.Hostname) > 15 {
		add("Hostname", "1–15 characters (Windows domain compatibility)")
	} else if !hostnameRe.MatchString(c.Hostname) {
		add("Hostname", "alphanumeric and hyphen only, cannot start/end with hyphen")
	}
	if c.KeyboardLayout == "" {
		add("KeyboardLayout", "required")
	}
	if c.Timezone == "" {
		add("Timezone", "required")
	}

	// Network
	if !c.UseDHCP {
		if !isIPv4(c.StaticIP) {
			add("StaticIP", "invalid IPv4 address")
		}
		if !isIPv4Mask(c.Subnet) {
			add("Subnet", "invalid IPv4 subnet mask")
		}
		if !isIPv4(c.Gateway) {
			add("Gateway", "invalid IPv4 address")
		}
		if len(c.DNSServers) == 0 {
			add("DNSServers", "at least one DNS server is required")
		} else {
			for i, d := range c.DNSServers {
				if !isIPv4(d) {
					add(fmt.Sprintf("DNSServers[%d]", i), "invalid IPv4 address: "+d)
				}
			}
		}
	}

	// Veeam credentials
	if err := checkVeeamPassword(c.VeeamAdminPassword); err != "" {
		add("VeeamAdminPassword", err)
	}
	if err := checkVeeamPassword(c.VeeamSoPassword); err != "" {
		add("VeeamSoPassword", err)
	}
	if c.VeeamAdminPassword == c.VeeamSoPassword {
		add("VeeamSoPassword", "admin and SO passwords must differ")
	}
	if bool(c.VeeamAdminIsMfaEnabled) && !base32Re.MatchString(c.VeeamAdminMfaSecretKey) {
		add("VeeamAdminMfaSecretKey", "must be 16–32 base32 characters (A–Z, 2–7)")
	}
	if bool(c.VeeamSoIsMfaEnabled) && !base32Re.MatchString(c.VeeamSoMfaSecretKey) {
		add("VeeamSoMfaSecretKey", "must be 16–32 base32 characters (A–Z, 2–7)")
	}
	if c.VeeamSoRecoveryToken != "" && !guidRe.MatchString(c.VeeamSoRecoveryToken) {
		add("VeeamSoRecoveryToken", "must be a GUID (hex 8-4-4-4-12)")
	}

	// NTP
	if len(c.NtpServer) == 0 {
		add("NtpServer", "at least one NTP server is required")
	}

	// VSA 13.1 timeouts
	if c.ExternalManagersInstallationTimeout < 60 || c.ExternalManagersInstallationTimeout > 86400 {
		add("ExternalManagersInstallationTimeout", "must be between 60 and 86400 seconds")
	}
	if c.HighAvailabilityTimeout < 60 || c.HighAvailabilityTimeout > 86400 {
		add("HighAvailabilityTimeout", "must be between 60 and 86400 seconds")
	}

	// VSA-only options on non-VSA appliances are rejected by the PS1 itself.
	// We surface the same constraint early in the UI.
	if c.ApplianceType != "VSA" {
		if c.NodeExporter {
			add("NodeExporter", "VSA-only feature; the PS1 will throw on "+c.ApplianceType)
		}
		if c.VCSPConnection {
			add("VCSPConnection", "VSA-only feature")
		}
		if c.LicenseVBRTune {
			add("LicenseVBRTune", "VSA-only feature")
		}
	}
	if c.NodeExporterTLSEnabled && !c.NodeExporter {
		add("NodeExporterTLSEnabled", "only effective when NodeExporter=true")
	}
	if c.VIASingleDisk && c.ApplianceType == "VSA" {
		add("VIASingleDisk", "VIA-only flag; the PS1 will throw on VSA")
	}

	// VCSP
	if c.VCSPConnection {
		if c.VCSPUrl == "" {
			add("VCSPUrl", "required when VCSPConnection=true")
		}
		if c.VCSPLogin == "" {
			add("VCSPLogin", "required when VCSPConnection=true")
		}
		if c.VCSPPassword == "" {
			add("VCSPPassword", "required when VCSPConnection=true")
		}
	}

	// Licence
	if c.LicenseVBRTune && c.LicenseFile == "" {
		add("LicenseFile", "required when LicenseVBRTune=true")
	}

	// Restore
	if c.RestoreConfig && c.ConfigPasswordSo == "" {
		add("ConfigPasswordSo", "required when RestoreConfig=true")
	}

	return errs
}

// checkVeeamPassword mirrors the requirements documented in the PS1 README:
// 15 chars min, 1 upper, 1 lower, 1 digit, 1 special, no 4-of-same-class in a row.
func checkVeeamPassword(p string) string {
	if len(p) < 15 {
		return "must be at least 15 characters"
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	if !hasUpper {
		return "must contain at least one uppercase letter"
	}
	if !hasLower {
		return "must contain at least one lowercase letter"
	}
	if !hasDigit {
		return "must contain at least one digit"
	}
	if !hasSymbol {
		return "must contain at least one special character"
	}
	if maxRunOfSameClass(p) > 3 {
		return "no more than 3 characters of the same class in a row"
	}
	return ""
}

func classOf(r rune) int {
	switch {
	case r >= 'A' && r <= 'Z':
		return 1
	case r >= 'a' && r <= 'z':
		return 2
	case r >= '0' && r <= '9':
		return 3
	default:
		return 4
	}
}

func maxRunOfSameClass(s string) int {
	if s == "" {
		return 0
	}
	max := 1
	cur := 1
	prev := classOf(rune(s[0]))
	for i := 1; i < len(s); i++ {
		c := classOf(rune(s[i]))
		if c == prev {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur = 1
			prev = c
		}
	}
	return max
}

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

func isIPv4Mask(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return false
	}
	mask := net.IPv4Mask(ip[12], ip[13], ip[14], ip[15])
	ones, bits := mask.Size()
	return bits == 32 && ones > 0 && ones <= 32
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
