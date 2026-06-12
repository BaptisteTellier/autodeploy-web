package deploy

import "strings"

// Remote-kickstart boot command. At the appliance ISO's GRUB menu we open the
// GRUB console ("c") and type the three proven lines (the same ones used
// manually and documented in the README "Kickstart live" section):
//
//	linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS inst.ks=<URL> ip=dhcp quiet inst.assumeyes
//	initrdefi /images/pxeboot/initrd.img
//	boot
//
// ip=dhcp brings the network up early so Anaconda can fetch the kickstart over
// HTTP; the static identity from the kickstart applies afterwards.
const stage2Label = "VeeamJeOS"

// BootCommandKeys returns the QEMU sendkey sequence that types the remote
// kickstart boot command at the GRUB menu.
func BootCommandKeys(ksURL string) []string {
	lines := []string{
		"linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=" + stage2Label +
			" inst.ks=" + ksURL + " ip=dhcp quiet inst.assumeyes",
		"initrdefi /images/pxeboot/initrd.img",
		"boot",
	}
	keys := []string{"c"} // open the GRUB console (also stops the menu countdown)
	for _, l := range lines {
		keys = append(keys, KeysForText(l)...)
		keys = append(keys, "ret")
	}
	return keys
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
