package deploy

import "strings"

// Remote-kickstart boot command, typed at the appliance ISO's GRUB menu. The
// command is ROLE-SPECIFIC — the VSA and the VIA/JeOS installers use different
// stage2 volume labels and kernel args (the VSA additionally needs fips=1;
// a VIA single-disk build additionally needs inst.vsingledisk). The ip= arg is
// ip=dhcp for DHCP nodes; static nodes use ip=<ip>::<gw>:<netmask>:<host>::none
// so the installer can reach the remote kickstart on a network without DHCP:
//
//	VSA (DHCP)   : linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamSA fips=1 inst.ks=<URL> ip=dhcp quiet inst.assumeyes
//	VSA (static) : linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamSA fips=1 inst.ks=<URL> ip=<ip>::<gw>:<mask>:<host>::none quiet inst.assumeyes
//	VIA (DHCP)   : linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS inst.ks=<URL> ip=dhcp quiet inst.assumeyes
//	VIA+SD (DHCP): linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=VeeamJeOS inst.ks=<URL> inst.vsingledisk ip=dhcp quiet inst.assumeyes
//	(all)        : initrdefi /images/pxeboot/initrd.img
//	(all)        : boot
const (
	labelVSA = "VeeamSA"
	labelVIA = "VeeamJeOS"
)

// ipKernelArg builds the dracut/anaconda `ip=` boot argument. A node with a
// static IP pins its address (so the installer can fetch the remote kickstart
// on a network without DHCP); an empty ip yields the DHCP form.
func ipKernelArg(ip, gateway, netmask, hostname string) string {
	if ip == "" {
		return "ip=dhcp"
	}
	return "ip=" + ip + "::" + gateway + ":" + netmask + ":" + hostname + "::none"
}

// linuxLine builds the role-specific `linuxefi …` GRUB line.
// singleDisk is only meaningful for VIA roles (ignored for VSA).
func linuxLine(role, ksURL, ipArg string, singleDisk bool) string {
	if strings.HasPrefix(role, "VSA") {
		return "linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=" + labelVSA +
			" fips=1 inst.ks=" + ksURL + " " + ipArg + " quiet inst.assumeyes"
	}
	line := "linuxefi /images/pxeboot/vmlinuz inst.stage2=hd:LABEL=" + labelVIA +
		" inst.ks=" + ksURL
	if singleDisk {
		line += " inst.vsingledisk"
	}
	return line + " " + ipArg + " quiet inst.assumeyes"
}

// bootLines returns the three GRUB lines for a role + kickstart URL.
func bootLines(role, ksURL, ipArg string, singleDisk bool) []string {
	return []string{
		linuxLine(role, ksURL, ipArg, singleDisk),
		"initrdefi /images/pxeboot/initrd.img",
		"boot",
	}
}

// BootCommandText returns the full GRUB boot command (the three lines joined by
// newlines) for the given role and kickstart URL. This is what the deploy UI
// pre-fills in the editable "advanced boot command" box.
func BootCommandText(role, ksURL, ipArg string, singleDisk bool) string {
	return strings.Join(bootLines(role, ksURL, ipArg, singleDisk), "\n")
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
func BootCommandKeys(role, ksURL, ipArg string, singleDisk bool) []string {
	return bootKeysFromLines(bootLines(role, ksURL, ipArg, singleDisk))
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
