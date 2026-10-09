package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
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
	// some distributions and root-only on others. A direct read is tried
	// first, and a permission error is retried with `sudo -n -- cat --`,
	// which refuses a password prompt instead of waiting on one.
	ufwRulesPath = "/etc/ufw/user.rules"
)

// ufwState is what could be learned about ufw from its configuration files.
// The live nftables ruleset is not consulted: these files are what ufw itself
// would load, and reading them does not change a rule.
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
	// RulesSudoFailed is true when the rules file denied a direct read and
	// `sudo -n` could not read it either.
	RulesSudoFailed bool
	// ConfigSudoFailed is true when any of the three ufw files denied a direct
	// read and `sudo -n` could not read it. A password prompt is never waited on.
	ConfigSudoFailed bool
	// ForwardAccepts are the route rules that accept forwarded traffic, with
	// the interfaces each one is limited to.
	ForwardAccepts []forwardRule
	// InputAccepts maps an interface to the destination ports it accepts
	// inbound to the host. Only the ports NAT mode depends on are meaningful
	// here; see acceptsPort.
	InputAccepts map[string]map[string]bool
}

// readUFWState reads ufw's configuration. A file that cannot be read is
// reported as unknown rather than assumed permissive, because assuming the
// permissive case would hide the very problem this check exists to find.
func readUFWState(confPath, defaultsPath, rulesPath string) ufwState {
	conf, confOK := readFileIfPossible(confPath)
	defaults, defaultsOK := readFileIfPossible(defaultsPath)
	rules, rulesOK := readFileIfPossible(rulesPath)
	return assembleUFWState(conf, confOK, defaults, defaultsOK, rules, rulesOK)
}

// readHostUFW reads the fixed paths on this machine. A file that is not
// readable is retried through sudo, so a root-only user.rules still settles
// the check when the operator can sudo without a password prompt.
func readHostUFW(ctx context.Context, run hostexec.Runner) ufwState {
	return readUFWFiles(ctx, run, os.ReadFile, ufwConfPath, ufwDefaultsPath, ufwRulesPath)
}

// readUFWFiles is readHostUFW with its filesystem read and paths supplied, so
// a test can refuse one file and answer the sudo retry without being root.
func readUFWFiles(ctx context.Context, run hostexec.Runner, read func(string) ([]byte, error), confPath, defaultsPath, rulesPath string) ufwState {
	conf := readConfigFile(ctx, run, read, confPath)
	defaults := readConfigFile(ctx, run, read, defaultsPath)
	rules := readConfigFile(ctx, run, read, rulesPath)
	state := assembleUFWState(conf.data, conf.ok, defaults.data, defaults.ok, rules.data, rules.ok)
	state.RulesSudoFailed = rules.sudoFailed
	state.ConfigSudoFailed = conf.sudoFailed || defaults.sudoFailed || rules.sudoFailed
	return state
}

// configRead is one configuration file, either read directly or through sudo.
type configRead struct {
	data []byte
	ok   bool
	// sudoFailed is a permission error that `sudo -n` did not overcome.
	sudoFailed bool
}

// readConfigFile reads path. On a permission error it reads the same path
// with `sudo -n -- cat --`. Any other failure, including sudo needing a
// password, leaves the file unread.
func readConfigFile(ctx context.Context, run hostexec.Runner, read func(string) ([]byte, error), path string) configRead {
	data, err := read(path)
	if err == nil {
		return configRead{data: data, ok: true}
	}
	if run == nil || !os.IsPermission(err) {
		return configRead{}
	}
	res, sudoErr := run.Run(ctx, hostexec.Command{
		Name:   "sudo",
		Args:   []string{"-n", "--", "cat", "--", path},
		Effect: hostexec.Read,
	})
	if sudoErr != nil || res == nil {
		return configRead{sudoFailed: true}
	}
	return configRead{data: res.Stdout, ok: true}
}

func readFileIfPossible(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// assembleUFWState turns the three configuration files into a verdict. A file
// that was not read contributes nothing, which is the unknown case rather
// than a guessed permissive one.
func assembleUFWState(conf []byte, confOK bool, defaults []byte, defaultsOK bool, rules []byte, rulesOK bool) ufwState {
	state := ufwState{}
	if confOK {
		if value, ok := parseShellVar(bytes.NewReader(conf), "ENABLED"); ok {
			state.Installed = true
			state.Enabled = strings.EqualFold(value, "yes")
		}
	}
	if defaultsOK {
		if value, ok := parseShellVar(bytes.NewReader(defaults), "DEFAULT_FORWARD_POLICY"); ok {
			state.Installed = true
			state.ForwardPolicy = strings.ToUpper(value)
		}
		if value, ok := parseShellVar(bytes.NewReader(defaults), "DEFAULT_INPUT_POLICY"); ok {
			state.Installed = true
			state.InputPolicy = strings.ToUpper(value)
		}
	}
	if rulesOK {
		state.ForwardAccepts, state.InputAccepts, state.RulesReadable = parseUFWRules(bytes.NewReader(rules))
	}
	return state
}

// anyInterface is the key used for a rule that names no interface and so
// applies to all of them.
const anyInterface = "any"

// forwardRule is one accepting route rule. In is the interface the traffic
// must arrive on (`-i`) and Out the one it must leave by (`-o`); either is
// empty when the rule does not restrict it. Both matter: the guest's outbound
// traffic arrives on the NAT bridge, so a rule limited to another input
// interface, or only to traffic leaving toward the bridge, does nothing for
// it.
type forwardRule struct {
	In  string
	Out string
}

// coversAll reports whether the rule names no interface at all and so accepts
// forwarded traffic everywhere.
func (r forwardRule) coversAll() bool {
	return r.In == "" && r.Out == ""
}

// String renders the rule the way an operator would write it for ufw.
func (r forwardRule) String() string {
	switch {
	case r.coversAll():
		return anyInterface
	case r.Out == "":
		return "in on " + r.In
	case r.In == "":
		return "out on " + r.Out
	default:
		return "in on " + r.In + " out on " + r.Out
	}
}

// forwardRulesFrom returns the rules that let a guest on bridge reach past the
// host: those arriving on the bridge, and those naming no interface. With no
// bridge known, a rule on any input interface is accepted as well, since there
// is nothing to tie it to. A rule limited only to an output interface never
// counts, because it governs traffic toward that interface rather than from
// it.
func (s ufwState) forwardRulesFrom(bridge string) []forwardRule {
	var matching []forwardRule
	for _, rule := range s.ForwardAccepts {
		switch {
		case rule.coversAll():
		case rule.In == "":
			continue
		case bridge != "" && rule.In != bridge:
			continue
		}
		matching = append(matching, rule)
	}
	return matching
}

// describeRules joins rules for a check's detail.
func describeRules(rules []forwardRule) string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.String())
	}
	return strings.Join(names, ", ")
}

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
func readUFWRules(rulesPath string) (forward []forwardRule, input map[string]map[string]bool, readable bool) {
	file, err := os.Open(rulesPath)
	if err != nil {
		return nil, nil, false
	}
	defer func() { _ = file.Close() }()
	return parseUFWRules(file)
}

// parseUFWRules reads the operator's rules from r. A scan error reports the
// rules as unreadable rather than as a partial list that could pass the check.
func parseUFWRules(r io.Reader) (forward []forwardRule, input map[string]map[string]bool, readable bool) {
	input = map[string]map[string]bool{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "-A" || !hasFlag(fields, "-j", "ACCEPT") {
			continue
		}
		in, _ := flagValue(fields, "-i")

		switch fields[1] {
		case "ufw-user-forward":
			out, _ := flagValue(fields, "-o")
			forward = append(forward, forwardRule{In: in, Out: out})
		case "ufw-user-input":
			iface := in
			if iface == "" {
				iface = anyInterface
			}
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

// parseShellVar pulls one NAME=value assignment out of a shell-style
// configuration file. Both files are sourced by ufw's own shell scripts, so
// this handles the quoting they use and nothing more elaborate.
func parseShellVar(r io.Reader, name string) (string, bool) {
	prefix := name + "="
	scanner := bufio.NewScanner(r)
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
// traffic. bridge is the device libvirt allocated for the NAT network, or empty
// when it could not be determined.
//
// It warns rather than fails. A host can allow the traffic somewhere other
// than these files, and a false failure would exit non-zero on a host that
// actually works.
func checkForwarding(cfg *config.Config, conn *config.Connection, bridge string, state ufwState) check {
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
	return forwardingCheck(cfg, state, bridge)
}

// forwardingCheck is the decision alone, separated from the fixed paths so it
// can be exercised against every ufw configuration a host might have.
func forwardingCheck(cfg *config.Config, state ufwState, bridge string) check {
	const name = "host firewall forwarding"
	matching := state.forwardRulesFrom(bridge)

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
	// added a route rule for traffic arriving from the NAT bridge. Where those
	// rules are readable they settle it; where they are not, the check says
	// what it could not determine rather than guessing either way.
	case len(matching) > 0 && bridge == "":
		// libvirt allocates the bridge when the network is first started, so
		// on a fresh host there is nothing to tie the rule to yet and any
		// input interface's rule is accepted.
		return check{
			Name: name, Status: statusPass,
			Detail: fmt.Sprintf("ufw forwards by rule for %s; the NAT network's bridge is not known yet, so the rule could not be matched to it",
				describeRules(matching)),
		}
	case len(matching) > 0:
		return check{
			Name: name, Status: statusPass,
			Detail: fmt.Sprintf("ufw forwards by rule for %s, covering the NAT bridge %s", describeRules(matching), bridge),
		}
	case len(state.ForwardAccepts) > 0:
		return check{
			Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("ufw is enabled with DEFAULT_FORWARD_POLICY=%s, and none of its route rules (%s) accepts traffic arriving from %s",
				state.ForwardPolicy, describeRules(state.ForwardAccepts), describeInterface(bridge)),
			Remedy: forwardingRemedy(cfg),
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
			Remedy: withSudoReadHint(state, forwardingRemedy(cfg)),
		}
	default:
		detail := fmt.Sprintf("ufw is enabled with DEFAULT_FORWARD_POLICY=%s, and its rules are not readable without root, so forwarding could not be confirmed", state.ForwardPolicy)
		if state.RulesSudoFailed {
			detail = fmt.Sprintf("ufw is enabled with DEFAULT_FORWARD_POLICY=%s, and `sudo -n` could not read %s, so forwarding could not be confirmed", state.ForwardPolicy, ufwRulesPath)
		}
		return check{
			Name: name, Status: statusWarn,
			Detail: detail,
			Remedy: withSudoReadHint(state, forwardingRemedy(cfg)),
		}
	}
}

// withSudoReadHint tells the operator how to let the next doctor read a
// root-only rules file. sudo -n refuses a password prompt, so a cached
// credential from `sudo -v` is what makes the retry succeed.
func withSudoReadHint(state ufwState, remedy string) string {
	if !state.ConfigSudoFailed {
		return remedy
	}
	return "Run `sudo -v`, then `agent-vm doctor` again, so the rules can be read. " + remedy
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
