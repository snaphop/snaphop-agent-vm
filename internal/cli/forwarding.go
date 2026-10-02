package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

// In NAT mode a guest reaches the internet only if the host forwards its
// packets, and libvirt accepting them in its own nftables table is not enough:
// every base chain registered on the forward hook runs, so a host firewall with
// a drop policy on that hook silently discards traffic libvirt already
// accepted. ufw ships exactly that configuration — DEFAULT_FORWARD_POLICY is
// DROP by default — and the result is a VM that looks entirely healthy. It
// boots, SSH works, and DNS works, because the resolver is dnsmasq on the host
// bridge and that is delivered locally rather than forwarded. Only outbound
// connections past the host fail, and because the packets are dropped rather
// than rejected they fail by hanging: `apt update` sits at 0% until it times
// out. doctor reports the configuration up front instead.

const (
	// ufwConfPath records whether ufw is enabled, and ufwDefaultsPath holds
	// its forward policy. Both are only read, never written: agent-vm does not
	// modify host firewall rules.
	ufwConfPath     = "/etc/ufw/ufw.conf"
	ufwDefaultsPath = "/etc/default/ufw"

	// ufwRulesPath holds the rules an operator has added. It is readable on
	// some distributions and root-only on others, so it refines the verdict
	// when available and is simply absent otherwise.
	ufwRulesPath = "/etc/ufw/user.rules"
)

// ufwState is what could be learned about ufw without privileges. The live
// ruleset needs root to read, so this deliberately reports only what the
// configuration files say.
type ufwState struct {
	// Installed is whether ufw configuration exists on this host at all.
	Installed bool
	// Enabled is ufw's own ENABLED setting.
	Enabled bool
	// ForwardPolicy is DEFAULT_FORWARD_POLICY, uppercased, or empty when the
	// file could not be read or does not set it.
	ForwardPolicy string
	// InputPolicy is DEFAULT_INPUT_POLICY, uppercased, under the same rule. It
	// governs the guest's DHCP and DNS requests, which arrive on the host's
	// input hook rather than its forward hook.
	InputPolicy string
	// RulesReadable is whether the operator's own rules could be read at all.
	RulesReadable bool
	// ForwardAccepts are the interfaces named by route rules that accept
	// forwarded traffic. A rule with no interface appears as "any".
	ForwardAccepts []string
	// InputAccepts maps an interface to the destination ports it accepts
	// inbound to the host. Only the ports NAT mode depends on are meaningful
	// here; see acceptsPort.
	InputAccepts map[string]map[string]bool
}

// readUFWState reads ufw's configuration. A file that cannot be read is
// reported as unknown rather than assumed permissive, because assuming the
// permissive case would hide the very problem this check exists to find.
func readUFWState(confPath, defaultsPath, rulesPath string) ufwState {
	state := ufwState{}

	if value, ok := readShellVar(confPath, "ENABLED"); ok {
		state.Installed = true
		state.Enabled = strings.EqualFold(value, "yes")
	}
	if value, ok := readShellVar(defaultsPath, "DEFAULT_FORWARD_POLICY"); ok {
		state.Installed = true
		state.ForwardPolicy = strings.ToUpper(value)
	}
	if value, ok := readShellVar(defaultsPath, "DEFAULT_INPUT_POLICY"); ok {
		state.Installed = true
		state.InputPolicy = strings.ToUpper(value)
	}
	state.ForwardAccepts, state.InputAccepts, state.RulesReadable = readUFWRules(rulesPath)
	return state
}

// anyInterface is the key used for a rule that names no interface and so
// applies to all of them.
const anyInterface = "any"

// Ports the guest must be able to reach *on the host* for NAT mode to work at
// all: dnsmasq answers DHCP on 67 and DNS on 53. These are inbound to the host
// rather than forwarded, which is why allowing forwarding does not cover them.
const (
	dhcpServerPort = "67"
	dnsPort        = "53"
)

// readUFWRules reads the operator's own ufw rules, which ufw renders as
// iptables-restore lines. Both chains that matter are collected in one pass:
//
//	-A ufw-user-forward -i virbr1 -j ACCEPT               # ufw route allow in on virbr1
//	-A ufw-user-input -i virbr1 -p udp --dport 67 -j ACCEPT
//	-A ufw-user-input -i virbr1 -p udp --dport 53 -j ACCEPT
//
// Only the IPv4 file is read. The v6 twins ufw writes alongside them are
// identical in shape and never match on an IPv4-only NAT network, so they
// would add nothing but noise to the verdict.
func readUFWRules(rulesPath string) (forward []string, input map[string]map[string]bool, readable bool) {
	file, err := os.Open(rulesPath)
	if err != nil {
		return nil, nil, false
	}
	defer func() { _ = file.Close() }()

	input = map[string]map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "-A" || !hasFlag(fields, "-j", "ACCEPT") {
			continue
		}
		iface, ok := flagValue(fields, "-i")
		if !ok {
			iface = anyInterface
		}

		switch fields[1] {
		case "ufw-user-forward":
			forward = append(forward, iface)
		case "ufw-user-input":
			port, ok := flagValue(fields, "--dport")
			if !ok {
				// A rule accepting everything on the interface covers both
				// services without naming either.
				port = ""
			}
			if input[iface] == nil {
				input[iface] = map[string]bool{}
			}
			if port == "" {
				input[iface][dhcpServerPort] = true
				input[iface][dnsPort] = true
				continue
			}
			input[iface][port] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, false
	}
	return forward, input, true
}

// acceptsPort reports whether the guest may reach port on the host over iface.
// A rule that names no interface covers every interface; conversely, when the
// forward rule itself names none, a rule on any single interface is taken as
// covering it, since there is no bridge to tie the two together.
func (s ufwState) acceptsPort(iface, port string) bool {
	if s.InputAccepts[anyInterface][port] {
		return true
	}
	if iface != anyInterface {
		return s.InputAccepts[iface][port]
	}
	for _, ports := range s.InputAccepts {
		if ports[port] {
			return true
		}
	}
	return false
}

// missingGuestServices returns the services a guest on iface cannot reach on
// the host, in the order an operator meets them: no DHCP lease means the VM
// never gets an address at all, and no DNS means it resolves nothing.
func (s ufwState) missingGuestServices(iface string) []string {
	var missing []string
	for _, port := range []string{dhcpServerPort, dnsPort} {
		if !s.acceptsPort(iface, port) {
			missing = append(missing, port)
		}
	}
	return missing
}

// hasFlag reports whether fields contains flag immediately followed by value.
func hasFlag(fields []string, flag, value string) bool {
	got, ok := flagValue(fields, flag)
	return ok && got == value
}

// flagValue returns the argument that follows flag.
func flagValue(fields []string, flag string) (string, bool) {
	for i, field := range fields {
		if field == flag && i+1 < len(fields) {
			return fields[i+1], true
		}
	}
	return "", false
}

// readShellVar pulls one NAME=value assignment out of a shell-style
// configuration file. Both files are sourced by ufw's own shell scripts, so
// this handles the quoting they use and nothing more elaborate.
func readShellVar(path, name string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	// Nothing is written, so a failed close has nothing to report.
	defer func() { _ = file.Close() }()

	prefix := name + "="
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimPrefix(line, prefix)
		// Strip an inline comment before unquoting, so `DROP" # note` cannot
		// be mistaken for a value.
		if hash := strings.Index(value, "#"); hash >= 0 {
			value = value[:hash]
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		return value, true
	}
	return "", false
}

// checkForwarding reports a host firewall that will drop the guest's forwarded
// traffic.
//
// It warns rather than fails: the live ruleset cannot be read without root, so
// an operator may have a route rule this check cannot see, and a false failure
// would exit non-zero on a host that actually works.
func checkForwarding(cfg *config.Config, conn *config.Connection) check {
	if conn.Remote {
		// The files below describe this machine's firewall, which has nothing
		// to do with whether the hypervisor forwards its guests' packets.
		// Reading them here would produce a confident verdict about the wrong
		// host.
		return check{
			Name: "host firewall forwarding", Status: statusSkip,
			Detail: "not checked for a remote hypervisor: the firewall that matters is that host's",
			Remedy: "Run `agent-vm doctor` on " + conn.SSHDestination + " if guests boot but their outbound connections hang.",
		}
	}
	return forwardingCheck(cfg, readUFWState(ufwConfPath, ufwDefaultsPath, ufwRulesPath))
}

// forwardingCheck is the decision alone, separated from the fixed paths so it
// can be exercised against every ufw configuration a host might have.
func forwardingCheck(cfg *config.Config, state ufwState) check {
	const name = "host firewall forwarding"

	// A bridged guest is on the operator's LAN directly and its traffic is not
	// routed through the host, so the forward hook never sees it. This asks
	// about the network *mode*, not whether a bridge is configured: a host that
	// sets a default bridge and still creates NAT VMs needs this check, and
	// keying it on cfg.Bridge silently skipped exactly that host.
	if cfg.BridgeMode() {
		return check{
			Name: name, Status: statusSkip,
			Detail: "skipped: only NAT mode routes guest traffic through the host",
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
	case state.ForwardPolicy == "ACCEPT":
		return check{
			Name: name, Status: statusPass,
			Detail: "ufw is enabled and forwards by default",
		}
	// The default policy drops, so the guest gets out only if the operator
	// added a route rule. Where those rules are readable they settle it; where
	// they are not, the check says what it could not determine rather than
	// guessing either way.
	case len(state.ForwardAccepts) > 0:
		return check{
			Name: name, Status: statusPass,
			Detail: fmt.Sprintf("ufw forwards by rule for %s", strings.Join(state.ForwardAccepts, ", ")),
		}
	case state.RulesReadable && state.ForwardPolicy != "":
		return check{
			Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("ufw is enabled with DEFAULT_FORWARD_POLICY=%s and has no rule allowing forwarded traffic", state.ForwardPolicy),
			Remedy: forwardingRemedy(cfg),
		}
	case state.ForwardPolicy == "":
		return check{
			Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("ufw is enabled but %s could not be read, so its forward policy is unknown", ufwDefaultsPath),
			Remedy: forwardingRemedy(cfg),
		}
	default:
		return check{
			Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("ufw is enabled with DEFAULT_FORWARD_POLICY=%s, and its rules are not readable without root, so forwarding could not be confirmed", state.ForwardPolicy),
			Remedy: forwardingRemedy(cfg),
		}
	}
}

// forwardingRemedy names the bridge indirectly: libvirt allocates it (virbrN)
// when the network is first started, so it is not knowable from configuration
// alone and a guessed name would send the operator to the wrong interface.
func forwardingRemedy(cfg *config.Config) string {
	return fmt.Sprintf(
		"A guest will boot, accept SSH and resolve DNS, but outbound connections will hang. "+
			"Find the bridge with `virsh --connect %s net-info %s | grep Bridge` — libvirt allocates it, so do not assume virbr0 — "+
			"then allow forwarding from it, for example `sudo ufw route allow in on <bridge>`. "+
			"That rule also lets the guest reach your LAN; see \"Host Firewalls And The virbrN Bridge\" in docs/host-setup.md "+
			"for a variant that keeps it to the internet only, and for why the rule can stop matching if the bridge is renamed. "+
			"agent-vm never changes host firewall rules itself.",
		cfg.LibvirtURI, cfg.NATNetwork)
}
