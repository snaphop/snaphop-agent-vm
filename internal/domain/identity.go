package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// x86Arches are the names under which an x86 hypervisor shows up. `virsh
// capabilities` says "x86_64" (or "i686"). A dry-run plan cannot ask libvirt,
// so it passes runtime.GOARCH, which is "amd64" or "386" for the same machines.
var x86Arches = map[string]bool{
	"x86_64": true,
	"amd64":  true,
	"i686":   true,
	"i386":   true,
	"386":    true,
}

// Firmware identity presented to the guest. QEMU's own defaults — manufacturer
// QEMU, product "Standard PC", BIOS vendor SeaBIOS, chassis asset tag QEMU —
// are the strings systemd-detect-virt and virt-what match. An unconfigured AMI
// desktop board reports these instead, and those checks do not treat them as a
// virtual machine.
//
// The strings are fixed rather than copied from the host. A guest is untrusted,
// and the host's serial number is the host's (SECURITY.md). Each VM still gets
// its own serial, derived from its name, so two guests do not share one and a
// recreated VM of the same name presents the same firmware identity.
const (
	firmwareBIOSVendor  = "American Megatrends Inc."
	firmwareBIOSVersion = "5.17"
	firmwareBIOSDate    = "01/15/2024"
	firmwareBIOSRelease = "5.17"
	firmwareOEM         = "To Be Filled By O.E.M."
)

// hardwareSerial is the SMBIOS serial for a VM. It is a function of the name
// only, so it does not depend on the state directory.
func hardwareSerial(name string) string {
	sum := sha256.Sum256([]byte("snaphop-agent-vm smbios serial " + name))
	return strings.ToUpper(hex.EncodeToString(sum[:8]))
}

// hardwareMAC is the guest NIC address. It is locally administered and unicast,
// so it is not the QEMU prefix 52:54:00, which is itself a virtualization tell.
// The disk path is part of the input so two hosts that both bridge a VM of the
// same name onto one LAN do not pick the same address when their state
// directories differ. The same name and path always produce the same address,
// so recreating a VM in place keeps it.
func hardwareMAC(name, overlayPath string) string {
	sum := sha256.Sum256([]byte("snaphop-agent-vm mac\n" + name + "\n" + overlayPath))
	octet := sum[:6]
	// Clear the multicast bit and set the local-admin bit.
	first := (octet[0] & 0xfe) | 0x02
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		first, octet[1], octet[2], octet[3], octet[4], octet[5])
}

// sysinfoArg renders --sysinfo. virt-install splits this on commas with POSIX
// shlex, so a value may contain spaces and must not contain a comma, an
// equals sign, or a quote. Every field a DMI check reads is set: an unset
// field keeps QEMU's default.
func (o CreateOptions) sysinfoArg() string {
	serial := hardwareSerial(o.Name)
	oem := firmwareOEM
	return strings.Join([]string{
		"type=smbios",
		"bios.vendor=" + firmwareBIOSVendor,
		"bios.version=" + firmwareBIOSVersion,
		"bios.date=" + firmwareBIOSDate,
		"bios.release=" + firmwareBIOSRelease,
		"system.manufacturer=" + oem,
		"system.product=" + oem,
		"system.version=" + oem,
		"system.serial=" + serial,
		"system.sku=" + oem,
		"system.family=" + oem,
		"baseBoard.manufacturer=" + oem,
		"baseBoard.product=" + oem,
		"baseBoard.version=" + oem,
		"baseBoard.serial=" + serial,
		"baseBoard.asset=" + oem,
		"chassis.manufacturer=" + oem,
		"chassis.version=" + oem,
		"chassis.serial=" + serial,
		"chassis.asset=" + oem,
		"chassis.sku=" + oem,
	}, ",")
}
