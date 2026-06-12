package deploy

import "strings"

// Remote-kickstart boot command, typed at the appliance ISO's GRUB menu. The
// command is ROLE-SPECIFIC — the VSA and the VIA/JeOS installers use different
// stage2 volume labels and kernel args (the VSA additionally needs fips=1):
//
//	VSA : linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamSA fips=1 inst.ks=<URL> ip=dhcp quiet inst.assumeyes
//	VIA : linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS  inst.ks=<URL> ip=dhcp quiet inst.assumeyes
//	(both) initrdefi /images/pxeboot/initrd.img
//	(both) boot
//
// We always serve the kickstart over HTTP (inst.ks=<URL>) + ip=dhcp so the
// per-node customised .cfg is fetched from autodeploy-web; only the stage2
// label and the fips flag vary by role. Values come from the user's validated
// manual boot commands.
const (
	labelVSA = "VeeamSA"
	labelVIA = "VeeamJeOS"
)

// linuxLine builds the role-specific `linuxefi …` GRUB line.
func linuxLine(role, ksURL string) string {
	if strings.HasPrefix(role, "VSA") {
		return "linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=" + labelVSA +
			" fips=1 inst.ks=" + ksURL + " ip=dhcp quiet inst.assumeyes"
	}
	return "linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=" + labelVIA +
		" inst.ks=" + ksURL + " ip=dhcp quiet inst.assumeyes"
}

// bootLines returns the three GRUB lines for a role + kickstart URL.
func bootLines(role, ksURL string) []string {
	return []string{
		linuxLine(role, ksURL),
		"initrdefi /images/pxeboot/initrd.img",
		"boot",
	}
}

// BootCommandText returns the full GRUB boot command (the three lines joined by
// newlines) for the given role and kickstart URL. This is what the deploy UI
// pre-fills in the editable "advanced boot command" box.
func BootCommandText(role, ksURL string) string {
	return strings.Join(bootLines(role, ksURL), "\n")
}

// bootKeysFromLines turns GRUB command lines into a QEMU sendkey sequence: it
// opens the GRUB console ("c", which also halts the menu countdown), types each
// non-empty line and presses Enter after it.
func bootKeysFromLines(lines []string) []string {
	keys := []string{"c"} // open the GRUB console (also stops the autoboot countdown)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		keys = append(keys, KeysForText(l)...)
		keys = append(keys, "ret")
	}
	return keys
}

// BootCommandKeys returns the QEMU sendkey sequence that types the role-specific
// remote-kickstart boot command at the GRUB menu.
func BootCommandKeys(role, ksURL string) []string {
	return bootKeysFromLines(bootLines(role, ksURL))
}

// BootCommandKeysFromText returns the sendkey sequence for a user-supplied,
// possibly edited boot command (one GRUB line per text line).
func BootCommandKeysFromText(text string) []string {
	return bootKeysFromLines(strings.Split(text, "\n"))
}

// keyNames maps non-alphanumeric characters to QEMU sendkey names.
var keyNames = map[rune]string{
	' ':  "spc",
	'/':  "slash",
	'.':  "dot",
	',':  "comma",
	';':  "semicolon",
	':':  "shift-semicolon",
	'=':  "equal",
	'-':  "minus",
	'_':  "shift-minus",
	'+':  "shift-equal",
	'?':  "shift-slash",
	'&':  "shift-7",
	'%':  "shift-5",
	'#':  "shift-3",
	'(':  "shift-9",
	')':  "shift-0",
	'~':  "shift-grave_accent",
	'\'': "apostrophe",
	'"':  "shift-apostrophe",
}

// KeysForText converts text into QEMU sendkey names (US layout). Characters
// without a mapping are skipped — kickstart URLs only use the safe set.
func KeysForText(s string) []string {
	keys := make([]string, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			keys = append(keys, string(r))
		case r >= 'A' && r <= 'Z':
			keys = append(keys, "shift-"+strings.ToLower(string(r)))
		default:
			if k, ok := keyNames[r]; ok {
				keys = append(keys, k)
			}
		}
	}
	return keys
}
