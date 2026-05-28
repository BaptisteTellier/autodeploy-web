package config

// Defaults returns a Config populated with the same defaults the upstream
// autodeploy.ps1 applies when a JSON key is absent. Kept aligned with v2.8.
func Defaults() Config {
	return Config{
		SourceISO:       "VeeamSoftwareAppliance_13.0.0.4967_20250822.iso",
		OutputISO:       "",
		ApplianceType:   "VSA",
		InPlace:         false,
		CreateBackup:    true,
		CleanupCFGFiles: true,
		CFGOnly:         false,
		GrubTimeout:     10,

		KeyboardLayout: "fr",
		Timezone:       "Europe/Paris",
		Hostname:       "veeam-server",

		UseDHCP:    false,
		StaticIP:   "192.168.1.166",
		Subnet:     "255.255.255.0",
		Gateway:    "192.168.1.1",
		DNSServers: FlexStringArray{"192.168.1.64", "8.8.4.4"},
		EnableIPv6: false,

		VeeamAdminPassword:     "123q123Q123!123",
		VeeamAdminMfaSecretKey: "JBSWY3DPEHPK3PXP",
		VeeamAdminIsMfaEnabled: true,
		VeeamSoPassword:        "123w123W123!123",
		VeeamSoMfaSecretKey:    "JBSWY3DPEHPK3PXP",
		VeeamSoIsMfaEnabled:    true,
		VeeamSoRecoveryToken:   "eb9fcbf4-2be6-e94d-4203-dded67c5a450",
		VeeamSoIsEnabled:       true,

		NtpServer:  FlexStringArray{"time.nist.gov"},
		NtpRunSync: true,

		ExternalManagersInstallationEnabled: false,
		ExternalManagersInstallationTimeout: 3600,
		HighAvailabilityEnabled:             false,
		HighAvailabilityTimeout:             3600,

		NodeExporter:           false,
		NodeExporterTLSEnabled: false,

		LicenseVBRTune: false,
		LicenseFile:    "Veeam-100instances-entplus-monitoring-nfr.lic",
		SyslogServer:   "",

		VCSPConnection: false,
		VCSPUrl:        "",
		VCSPLogin:      "",
		VCSPPassword:   "",

		RestoreConfig:    false,
		ConfigPasswordSo: "",

		VIASingleDisk: false,
		Debug:         false,
	}
}

// ApplianceTypes lists the values accepted by the PS1.
var ApplianceTypes = []string{"VSA", "VIA", "VIAiscsi", "VIAHR"}

// KeyboardLayouts is a curated list surfaced in the form. Not exhaustive —
// the PS1 ultimately accepts any string Anaconda accepts.
var KeyboardLayouts = []string{"fr", "us", "uk", "de", "es", "it", "be", "ch", "ca", "pl", "pt", "se", "no", "dk", "nl"}

// Timezones lists common IANA values for the autocomplete datalist. Users
// can type anything; the PS1 will pass it through to the kickstart.
var Timezones = []string{
	"Europe/Paris", "Europe/London", "Europe/Berlin", "Europe/Madrid", "Europe/Rome",
	"Europe/Amsterdam", "Europe/Brussels", "Europe/Zurich", "Europe/Stockholm",
	"UTC", "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Sao_Paulo", "Asia/Tokyo", "Asia/Shanghai", "Asia/Singapore", "Asia/Dubai",
	"Australia/Sydney",
}
