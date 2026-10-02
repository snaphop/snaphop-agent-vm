package cli

import (
	"fmt"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

// Forwarding is only half of what NAT mode needs, and it is the half whose
// failure is survivable. The other half is the guest talking *to* the host: it
// asks the host's dnsmasq for a DHCP lease and for every DNS answer, and that
// traffic arrives on the input hook rather than the forward hook, so ufw's
// separate `deny (incoming)` default governs it. ufw's ufw-before-input chain
// accepts DHCP *replies* (sport 67 → dport 68, the host acting as a DHCP
// client) and its ufw-after-input chain then sends anything arriving on port
// 67 to the drop policy, so on a host with no rule for the bridge the guest's
// DHCPDISCOVER never reaches dnsmasq.
//
// That failure is total rather than partial: the guest boots, sits in
// systemd-networkd-wait-online forever, and never gets an address, so `create`
// times out waiting for one and the host had looked ready. It is reported
// separately from forwarding because it is a different fix, a different
// symptom, and — unlike forwarding — it is not conditional on the operator
// having already added a route rule.
const guestServicesCheckName = "host firewall guest services"

// checkGuestServices reports a host firewall that will drop the guest's DHCP
// and DNS requests to the host. bridge is the device libvirt allocated for the
// NAT network, or empty when it could not be determined.
func checkGuestServices(cfg *config.Config, conn *config.Connection, bridge string) check {
	if conn.Remote {
		// As with forwarding: these files describe this machine's firewall,
		// not the hypervisor's.
		return check{
			Name: guestServicesCheckName, Status: statusSkip,
			Detail: "not checked for a remote hypervisor: the firewall that matters is that host's",
			Remedy: "Run `agent-vm doctor` on " + conn.SSHDestination + " if a guest boots but never gets an address.",
		}
	}
	return guestServicesCheck(cfg, readUFWState(ufwConfPath, ufwDefaultsPath, ufwRulesPath), bridge)
}

// guestServicesCheck is the decision alone, separated from the fixed paths so
// it can be exercised against every ufw configuration a host might have.
func guestServicesCheck(cfg *config.Config, state ufwState, bridge string) check {
	name := guestServicesCheckName

	// A bridged guest gets its lease and its resolver from the LAN, so it
	// never asks the host for either.
	if cfg.BridgeMode() {
		return check{
			Name: name, Status: statusSkip,
			Detail: "skipped: only NAT mode makes the guest depend on the host's dnsmasq",
		}
	}

	switch {
	case !state.Installed:
		return check{
			Name: name, Status: statusPass,
			Detail: "no ufw configuration on this host",
		}
	case !state.Enabled:
		return check{
			Name: name, Status: statusPass,
			Detail: "ufw is installed but not enabled",
		}
	case state.InputPolicy == "ACCEPT":
		return check{
			Name: name, Status: statusPass,
			Detail: "ufw is enabled and accepts incoming traffic by default",
		}
	case !state.RulesReadable:
		return check{
			Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("ufw is enabled and %s is not readable without root, so the guest's DHCP and DNS could not be confirmed", ufwRulesPath),
			Remedy: guestServicesRemedy(cfg, bridge, []string{dhcpServerPort, dnsPort}),
		}
	}

	// The bridge is what the rules must name. When libvirt has not allocated
	// one yet — the network is defined on the first create — any interface's
	// rules are accepted rather than warning about a bridge that does not
	// exist, which would be a warning no operator could act on.
	iface := bridge
	if iface == "" {
		iface = anyInterface
	}
	missing := state.missingGuestServices(iface)
	if len(missing) == 0 {
		return check{
			Name: name, Status: statusPass,
			Detail: fmt.Sprintf("ufw accepts the guest's DHCP and DNS on %s", describeInterface(bridge)),
		}
	}
	return check{
		Name: name, Status: statusWarn,
		Detail: fmt.Sprintf("ufw is enabled and does not accept the guest's %s on %s, so a guest cannot reach the host's dnsmasq",
			describePorts(missing), describeInterface(bridge)),
		Remedy: guestServicesRemedy(cfg, bridge, missing),
	}
}

// describeInterface names the bridge when it is known, and says so plainly
// when it is not, rather than printing an "any" that reads like a rule.
func describeInterface(bridge string) string {
	if bridge == "" {
		return "any interface"
	}
	return bridge
}

// describePorts names the missing services the way an operator thinks of them,
// rather than as bare port numbers.
func describePorts(ports []string) string {
	names := make([]string, 0, len(ports))
	for _, port := range ports {
		switch port {
		case dhcpServerPort:
			names = append(names, "DHCP (67/udp)")
		case dnsPort:
			names = append(names, "DNS (53)")
		default:
			names = append(names, "port "+port)
		}
	}
	return strings.Join(names, " or ")
}

// guestServicesRemedy prints the rules that let a guest reach the host's
// dnsmasq. The bridge is named when libvirt has already allocated it, because
// a rule an operator can paste is worth more than one they must first resolve;
// where it is not known yet the lookup comes first, since libvirt picks the
// device and a guessed virbr0 would send them to the wrong interface.
func guestServicesRemedy(cfg *config.Config, bridge string, missing []string) string {
	iface := bridge
	if iface == "" {
		iface = "<bridge>"
	}

	rules := make([]string, 0, len(missing))
	for _, port := range missing {
		switch port {
		case dhcpServerPort:
			rules = append(rules, fmt.Sprintf("`sudo ufw allow in on %s to any port 67 proto udp`", iface))
		case dnsPort:
			rules = append(rules, fmt.Sprintf("`sudo ufw allow in on %s to any port 53`", iface))
		}
	}

	remedy := fmt.Sprintf(
		"Without this a guest never gets a DHCP lease: it boots, waits in systemd-networkd-wait-online, and `create` fails with no address. "+
			"libvirt does not add these rules: it manages its own nftables table and knows nothing about ufw. Add %s. ",
		strings.Join(rules, " and "))
	if bridge == "" {
		remedy += fmt.Sprintf("Find the bridge with `virsh --connect %s net-info %s | grep Bridge` — libvirt allocates it, so do not assume virbr0. ",
			cfg.LibvirtURI, cfg.NATNetwork)
	} else {
		remedy += fmt.Sprintf("%s is the bridge libvirt allocated for %s; it can change if the network is redefined. ", bridge, cfg.NATNetwork)
	}
	return remedy + "See \"Host Firewalls And The virbrN Bridge\" in docs/host-setup.md."
}
