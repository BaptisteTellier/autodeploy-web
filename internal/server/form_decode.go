package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
)

// configFromForm rebuilds a Config from an HTML form submission. Missing
// checkboxes default to false (HTML semantics). String arrays come in as
// comma- or newline-separated values.
func configFromForm(r *http.Request) (config.Config, error) {
	c := config.Defaults()

	get := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	getBool := func(k string) bool {
		v := strings.ToLower(r.FormValue(k))
		return v == "on" || v == "true" || v == "1" || v == "yes"
	}
	getInt := func(k string, def int) int {
		v := r.FormValue(k)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}
	getArray := func(k string) config.FlexStringArray {
		raw := r.FormValue(k)
		if raw == "" {
			return nil
		}
		raw = strings.ReplaceAll(raw, "\r", "")
		raw = strings.ReplaceAll(raw, "\n", ",")
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return config.FlexStringArray(out)
	}

	// Core
	c.SourceISO = get("SourceISO")
	c.OutputISO = get("OutputISO")
	if v := get("ApplianceType"); v != "" {
		c.ApplianceType = v
	}
	c.InPlace = getBool("InPlace")
	c.CreateBackup = getBool("CreateBackup")
	c.CleanupCFGFiles = getBool("CleanupCFGFiles")
	c.CFGOnly = getBool("CFGOnly")
	c.GrubTimeout = getInt("GrubTimeout", c.GrubTimeout)

	// Regional
	if v := get("KeyboardLayout"); v != "" {
		c.KeyboardLayout = v
	}
	if v := get("Timezone"); v != "" {
		c.Timezone = v
	}
	if v := get("Hostname"); v != "" {
		c.Hostname = v
	}

	// Network
	c.UseDHCP = getBool("UseDHCP")
	c.StaticIP = get("StaticIP")
	c.Subnet = get("Subnet")
	c.Gateway = get("Gateway")
	c.DNSServers = getArray("DNSServers")

	// Veeam accounts
	c.VeeamAdminPassword = get("VeeamAdminPassword")
	c.VeeamAdminMfaSecretKey = get("VeeamAdminMfaSecretKey")
	c.VeeamAdminIsMfaEnabled = config.FlexBool(getBool("VeeamAdminIsMfaEnabled"))
	c.VeeamSoPassword = get("VeeamSoPassword")
	c.VeeamSoMfaSecretKey = get("VeeamSoMfaSecretKey")
	c.VeeamSoIsMfaEnabled = config.FlexBool(getBool("VeeamSoIsMfaEnabled"))
	c.VeeamSoRecoveryToken = get("VeeamSoRecoveryToken")
	c.VeeamSoIsEnabled = config.FlexBool(getBool("VeeamSoIsEnabled"))

	// NTP
	c.NtpServer = getArray("NtpServer")
	c.NtpRunSync = config.FlexBool(getBool("NtpRunSync"))

	// VSA 13.1
	c.ExternalManagersInstallationEnabled = getBool("ExternalManagersInstallationEnabled")
	c.ExternalManagersInstallationTimeout = getInt("ExternalManagersInstallationTimeout", c.ExternalManagersInstallationTimeout)
	c.HighAvailabilityEnabled = getBool("HighAvailabilityEnabled")
	c.HighAvailabilityTimeout = getInt("HighAvailabilityTimeout", c.HighAvailabilityTimeout)

	// Monitoring
	c.NodeExporter = getBool("NodeExporter")
	c.NodeExporterTLSEnabled = getBool("NodeExporterTLSEnabled")

	// VBR
	c.LicenseVBRTune = getBool("LicenseVBRTune")
	c.LicenseFile = get("LicenseFile")
	c.SyslogServer = get("SyslogServer")

	// VCSP
	c.VCSPConnection = getBool("VCSPConnection")
	c.VCSPUrl = get("VCSPUrl")
	c.VCSPLogin = get("VCSPLogin")
	c.VCSPPassword = get("VCSPPassword")

	// Restore
	c.RestoreConfig = getBool("RestoreConfig")
	c.ConfigPasswordSo = get("ConfigPasswordSo")

	// VIA
	c.VIASingleDisk = getBool("VIASingleDisk")

	// Debug
	c.Debug = getBool("Debug")

	return c, nil
}
