package config

// Defaults returns a Config populated with the same defaults the upstream
// autodeploy.ps1 applies when a JSON key is absent. Kept aligned with v2.8.
func Defaults() Config {
	return Config{
		SourceISO:       "",
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
		LicenseFile:    "",
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

// NamedOption is a value/label pair used for <select> dropdowns.
type NamedOption struct {
	Value string
	Label string
}

// ApplianceTypes lists the values accepted by the PS1.
var ApplianceTypes = []string{"VSA", "VIA", "VIAiscsi", "VIAHR"}

// KeyboardLayouts lists common keyboard layouts accepted by Anaconda/kickstart.
var KeyboardLayouts = []NamedOption{
	{Value: "fr", Label: "fr — Français (AZERTY)"},
	{Value: "fr-latin9", Label: "fr-latin9 — Français avec €"},
	{Value: "be", Label: "be — Belge (AZERTY)"},
	{Value: "ch-fr", Label: "ch-fr — Suisse romand"},
	{Value: "us", Label: "us — English US (QWERTY)"},
	{Value: "uk", Label: "uk — English UK (QWERTY)"},
	{Value: "ca", Label: "ca — Canadien"},
	{Value: "de", Label: "de — Deutsch (QWERTZ)"},
	{Value: "de-nodeadkeys", Label: "de-nodeadkeys — Deutsch (sans accents)"},
	{Value: "ch", Label: "ch — Suisse alémanique (QWERTZ)"},
	{Value: "at", Label: "at — Österreich (QWERTZ)"},
	{Value: "es", Label: "es — Español"},
	{Value: "pt", Label: "pt — Português"},
	{Value: "it", Label: "it — Italiano"},
	{Value: "nl", Label: "nl — Nederlands"},
	{Value: "se", Label: "se — Svenska"},
	{Value: "no", Label: "no — Norsk"},
	{Value: "dk", Label: "dk — Dansk"},
	{Value: "fi", Label: "fi — Suomi"},
	{Value: "pl", Label: "pl — Polski"},
	{Value: "cz", Label: "cz — Čeština"},
	{Value: "sk", Label: "sk — Slovenčina"},
	{Value: "hu", Label: "hu — Magyar"},
	{Value: "ro", Label: "ro — Română"},
	{Value: "ru", Label: "ru — Русский"},
	{Value: "ua", Label: "ua — Українська"},
	{Value: "jp106", Label: "jp106 — 日本語"},
	{Value: "kr", Label: "kr — 한국어"},
	{Value: "cn", Label: "cn — 中文"},
	{Value: "ar", Label: "ar — العربية"},
}

// Timezones lists common IANA timezone values for the dropdown.
var Timezones = []NamedOption{
	// Europe
	{Value: "UTC", Label: "UTC"},
	{Value: "Europe/Paris", Label: "Europe/Paris (UTC+1/+2)"},
	{Value: "Europe/London", Label: "Europe/London (UTC+0/+1)"},
	{Value: "Europe/Brussels", Label: "Europe/Brussels (UTC+1/+2)"},
	{Value: "Europe/Amsterdam", Label: "Europe/Amsterdam (UTC+1/+2)"},
	{Value: "Europe/Berlin", Label: "Europe/Berlin (UTC+1/+2)"},
	{Value: "Europe/Zurich", Label: "Europe/Zurich (UTC+1/+2)"},
	{Value: "Europe/Vienna", Label: "Europe/Vienna (UTC+1/+2)"},
	{Value: "Europe/Madrid", Label: "Europe/Madrid (UTC+1/+2)"},
	{Value: "Europe/Lisbon", Label: "Europe/Lisbon (UTC+0/+1)"},
	{Value: "Europe/Rome", Label: "Europe/Rome (UTC+1/+2)"},
	{Value: "Europe/Stockholm", Label: "Europe/Stockholm (UTC+1/+2)"},
	{Value: "Europe/Oslo", Label: "Europe/Oslo (UTC+1/+2)"},
	{Value: "Europe/Copenhagen", Label: "Europe/Copenhagen (UTC+1/+2)"},
	{Value: "Europe/Helsinki", Label: "Europe/Helsinki (UTC+2/+3)"},
	{Value: "Europe/Warsaw", Label: "Europe/Warsaw (UTC+1/+2)"},
	{Value: "Europe/Prague", Label: "Europe/Prague (UTC+1/+2)"},
	{Value: "Europe/Budapest", Label: "Europe/Budapest (UTC+1/+2)"},
	{Value: "Europe/Bucharest", Label: "Europe/Bucharest (UTC+2/+3)"},
	{Value: "Europe/Athens", Label: "Europe/Athens (UTC+2/+3)"},
	{Value: "Europe/Moscow", Label: "Europe/Moscow (UTC+3)"},
	{Value: "Europe/Istanbul", Label: "Europe/Istanbul (UTC+3)"},
	// Americas
	{Value: "America/New_York", Label: "America/New_York (UTC-5/-4)"},
	{Value: "America/Chicago", Label: "America/Chicago (UTC-6/-5)"},
	{Value: "America/Denver", Label: "America/Denver (UTC-7/-6)"},
	{Value: "America/Los_Angeles", Label: "America/Los_Angeles (UTC-8/-7)"},
	{Value: "America/Toronto", Label: "America/Toronto (UTC-5/-4)"},
	{Value: "America/Vancouver", Label: "America/Vancouver (UTC-8/-7)"},
	{Value: "America/Mexico_City", Label: "America/Mexico_City (UTC-6/-5)"},
	{Value: "America/Bogota", Label: "America/Bogota (UTC-5)"},
	{Value: "America/Lima", Label: "America/Lima (UTC-5)"},
	{Value: "America/Santiago", Label: "America/Santiago (UTC-3/-4)"},
	{Value: "America/Sao_Paulo", Label: "America/Sao_Paulo (UTC-3)"},
	{Value: "America/Buenos_Aires", Label: "America/Buenos_Aires (UTC-3)"},
	// Asia / Middle East
	{Value: "Asia/Dubai", Label: "Asia/Dubai (UTC+4)"},
	{Value: "Asia/Karachi", Label: "Asia/Karachi (UTC+5)"},
	{Value: "Asia/Kolkata", Label: "Asia/Kolkata (UTC+5:30)"},
	{Value: "Asia/Dhaka", Label: "Asia/Dhaka (UTC+6)"},
	{Value: "Asia/Bangkok", Label: "Asia/Bangkok (UTC+7)"},
	{Value: "Asia/Jakarta", Label: "Asia/Jakarta (UTC+7)"},
	{Value: "Asia/Shanghai", Label: "Asia/Shanghai (UTC+8)"},
	{Value: "Asia/Hong_Kong", Label: "Asia/Hong_Kong (UTC+8)"},
	{Value: "Asia/Singapore", Label: "Asia/Singapore (UTC+8)"},
	{Value: "Asia/Tokyo", Label: "Asia/Tokyo (UTC+9)"},
	{Value: "Asia/Seoul", Label: "Asia/Seoul (UTC+9)"},
	// Africa
	{Value: "Africa/Cairo", Label: "Africa/Cairo (UTC+2/+3)"},
	{Value: "Africa/Johannesburg", Label: "Africa/Johannesburg (UTC+2)"},
	{Value: "Africa/Lagos", Label: "Africa/Lagos (UTC+1)"},
	{Value: "Africa/Nairobi", Label: "Africa/Nairobi (UTC+3)"},
	// Pacific
	{Value: "Australia/Sydney", Label: "Australia/Sydney (UTC+10/+11)"},
	{Value: "Australia/Perth", Label: "Australia/Perth (UTC+8)"},
	{Value: "Pacific/Auckland", Label: "Pacific/Auckland (UTC+12/+13)"},
}
