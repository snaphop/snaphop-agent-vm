package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

// write puts one configuration file in a temporary directory and returns its
// path. An empty body means "this file does not exist".
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if body == "" {
		return path
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestReadUFWState_ReadsTheRealFileFormat(t *testing.T) {
	t.Parallel()
	// The bodies here are the shape ufw actually ships: a commented header, a
	// quoted policy, and no spaces around the assignment.
	tests := []struct {
		name     string
		conf     string
		defaults string
		want     ufwState
	}{
		{
			name:     "enabled and dropping",
			conf:     "# /etc/ufw/ufw.conf\nENABLED=yes\nLOGLEVEL=low\n",
			defaults: "# comment\nDEFAULT_FORWARD_POLICY=\"DROP\"\n",
			want:     ufwState{Installed: true, Enabled: true, ForwardPolicy: "DROP"},
		},
		{
			name:     "enabled and forwarding",
			conf:     "ENABLED=yes\n",
			defaults: "DEFAULT_FORWARD_POLICY=\"ACCEPT\"\n",
			want:     ufwState{Installed: true, Enabled: true, ForwardPolicy: "ACCEPT"},
		},
		{
			name:     "installed but off",
			conf:     "ENABLED=no\n",
			defaults: "DEFAULT_FORWARD_POLICY=\"DROP\"\n",
			want:     ufwState{Installed: true, Enabled: false, ForwardPolicy: "DROP"},
		},
		{
			name:     "not installed at all",
			conf:     "",
			defaults: "",
			want:     ufwState{},
		},
		{
			name:     "a commented-out policy is not a policy",
			conf:     "ENABLED=yes\n",
			defaults: "#DEFAULT_FORWARD_POLICY=\"ACCEPT\"\n",
			want:     ufwState{Installed: true, Enabled: true, ForwardPolicy: ""},
		},
		{
			name:     "both policies are recorded",
			conf:     "ENABLED=yes\n",
			defaults: "DEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_FORWARD_POLICY=\"DROP\"\n",
			want:     ufwState{Installed: true, Enabled: true, ForwardPolicy: "DROP", InputPolicy: "DROP"},
		},
		{
			name:     "an inline comment is not part of the value",
			conf:     "ENABLED=yes\n",
			defaults: "DEFAULT_FORWARD_POLICY=\"DROP\" # set by the installer\n",
			want:     ufwState{Installed: true, Enabled: true, ForwardPolicy: "DROP"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			conf := write(t, dir, "ufw.conf", tt.conf)
			defaults := write(t, dir, "ufw", tt.defaults)

			got := readUFWState(conf, defaults, filepath.Join(dir, "user.rules"))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readUFWState() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestCheckForwarding_WarnsWhenTheHostFirewallDropsForwardedTraffic is the
// regression this check exists for: a guest that boots, accepts SSH and
// resolves DNS while every outbound connection hangs.
func TestCheckForwarding_WarnsWhenTheHostFirewallDropsForwardedTraffic(t *testing.T) {
	t.Parallel()
	got := forwardingCheckFor(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})

	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q", got.Status, statusWarn)
	}
	if !strings.Contains(got.Detail, "DROP") {
		t.Errorf("detail %q does not name the policy that causes the problem", got.Detail)
	}
	// The remedy has to be actionable: the symptom, how to find the bridge
	// (never a guessed name), the rule to add, and where the caveats are
	// written down.
	for _, want := range []string{"hang", "net-info", "agent-vm-nat", "ufw route allow", "docs/host-setup.md"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy does not mention %q: %s", want, got.Remedy)
		}
	}
	// libvirt allocates the bridge, so the example rule has to carry a
	// placeholder the operator substitutes rather than a name that would send
	// some hosts to the wrong interface.
	if !strings.Contains(got.Remedy, "on <bridge>") {
		t.Errorf("remedy's example rule does not use a placeholder for the bridge: %s", got.Remedy)
	}
}

func TestCheckForwarding_PassesWhenForwardingIsAllowed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		conf     string
		defaults string
	}{
		{"policy is accept", "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"ACCEPT\"\n"},
		{"ufw is disabled", "ENABLED=no\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n"},
		{"ufw is not installed", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := forwardingCheckFor(t, tt.conf, tt.defaults, &config.Config{
				LibvirtURI: "qemu:///system",
				NATNetwork: "agent-vm-nat",
			})
			if got.Status != statusPass {
				t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
			}
		})
	}
}

// TestCheckForwarding_SkipsBridgedMode is the case the check must not report
// on: a bridged guest sits on the LAN and its traffic never reaches the host's
// forward hook.
func TestCheckForwarding_SkipsBridgedMode(t *testing.T) {
	t.Parallel()
	got := forwardingCheckFor(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
		Network:    config.NetworkBridge,
		Bridge:     "br0",
	})
	if got.Status != statusSkip {
		t.Errorf("status = %q, want %q", got.Status, statusSkip)
	}
}

// TestCheckForwarding_ReportsNATEvenWithADefaultBridgeConfigured is the
// regression: docs/host-setup.md tells operators to set a default bridge so
// `--bridge` need not be repeated, and that is orthogonal to the mode a VM is
// created in. A host doing that while creating NAT VMs still routes guest
// traffic through the forward hook, so skipping it there hid the one failure
// this check exists to catch.
func TestCheckForwarding_ReportsNATEvenWithADefaultBridgeConfigured(t *testing.T) {
	t.Parallel()
	got := forwardingCheckFor(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
		Network:    config.NetworkNAT,
		Bridge:     "br0",
	})
	if got.Status != statusWarn {
		t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
}

// TestCheckForwarding_WarnsWhenThePolicyCannotBeRead keeps an unreadable file
// from being mistaken for a permissive one.
func TestCheckForwarding_WarnsWhenThePolicyCannotBeRead(t *testing.T) {
	t.Parallel()
	got := forwardingCheckFor(t, "ENABLED=yes\n", "", &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q", got.Status, statusWarn)
	}
	if !strings.Contains(got.Detail, "unknown") {
		t.Errorf("detail %q does not say the policy is unknown", got.Detail)
	}
}

// allRules is what ufw writes for the three rules NAT mode needs, in the form
// `ufw status` produces them: forwarding past the host, plus the guest's DHCP
// and DNS inbound to the host's dnsmasq.
const allRules = `### tuple ### allow udp 67 0.0.0.0/0 any 0.0.0.0/0 in_virbr1
-A ufw-user-input -i virbr1 -p udp --dport 67 -j ACCEPT
### tuple ### allow any 53 0.0.0.0/0 any 0.0.0.0/0 in_virbr1
-A ufw-user-input -i virbr1 -p tcp --dport 53 -j ACCEPT
-A ufw-user-input -i virbr1 -p udp --dport 53 -j ACCEPT
### tuple ### route:allow any any 0.0.0.0/0 any 0.0.0.0/0 in_virbr1
-A ufw-user-forward -i virbr1 -j ACCEPT
`

// TestCheckForwarding_PassesOnceAllRulesExist covers the fix an operator
// applies after this check reports the problem: doctor must stop warning about
// a host that now works.
func TestCheckForwarding_PassesOnceAllRulesExist(t *testing.T) {
	t.Parallel()
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", allRules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusPass {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
	}
	// Naming the interface lets an operator confirm it is the right bridge,
	// which this check cannot determine on its own.
	if !strings.Contains(got.Detail, "virbr1") {
		t.Errorf("detail %q does not name the interface the rules cover", got.Detail)
	}
}

// A rule that accepts forwarding on every interface has no -i flag.
func TestReadUFWRules_TreatsAnInterfacelessRuleAsAny(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := write(t, dir, "user.rules", "-A ufw-user-forward -j ACCEPT\n")

	forward, _, readable := readUFWRules(path)
	if !readable {
		t.Fatal("readUFWRules reported the file as unreadable")
	}
	if len(forward) != 1 || !forward[0].coversAll() {
		t.Errorf("forward = %+v, want one rule naming no interface", forward)
	}
}

// A DROP rule in the same chain must not be read as permission to forward.
func TestReadUFWRules_IgnoresRulesThatDoNotAccept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := write(t, dir, "user.rules", "-A ufw-user-forward -i virbr1 -j DROP\n")

	forward, _, readable := readUFWRules(path)
	if !readable {
		t.Fatal("readUFWRules reported the file as unreadable")
	}
	if len(forward) != 0 {
		t.Errorf("forward = %v, want none", forward)
	}
}

// forwardingCheckFor runs the check against temporary ufw files. The check
// reads fixed paths, so the bodies are staged in a temp dir and the state is
// read through the same helper the check uses.
func forwardingCheckFor(t *testing.T, conf, defaults string, cfg *config.Config) check {
	t.Helper()
	return forwardingCheckWithRules(t, conf, defaults, "", cfg)
}

// forwardingCheckWithRules also stages the operator's own ufw rules.
func forwardingCheckWithRules(t *testing.T, conf, defaults, rules string, cfg *config.Config) check {
	t.Helper()
	dir := t.TempDir()
	confPath := write(t, dir, "ufw.conf", conf)
	defaultsPath := write(t, dir, "ufw", defaults)
	rulesPath := write(t, dir, "user.rules", rules)

	state := readUFWState(confPath, defaultsPath, rulesPath)
	return forwardingCheck(cfg, state, "")
}

// forwardingCheckOnBridge stages a dropping ufw with the given rules and runs
// the check as doctor does once libvirt has allocated bridge for the NAT
// network.
func forwardingCheckOnBridge(t *testing.T, rules, bridge string) check {
	t.Helper()
	dir := t.TempDir()
	state := readUFWState(
		write(t, dir, "ufw.conf", "ENABLED=yes\n"),
		write(t, dir, "ufw", "DEFAULT_FORWARD_POLICY=\"DROP\"\n"),
		write(t, dir, "user.rules", rules),
	)
	return forwardingCheck(&config.Config{LibvirtURI: "qemu:///system", NATNetwork: "agent-vm-nat"}, state, bridge)
}

// TestCheckForwarding_WarnsWhenTheOnlyRuleIsForAnotherInterface is the
// regression: a route rule for a VPN interface was taken as permission to
// forward, so doctor passed while every guest connection past the host hung.
func TestCheckForwarding_WarnsWhenTheOnlyRuleIsForAnotherInterface(t *testing.T) {
	t.Parallel()
	got := forwardingCheckOnBridge(t, "-A ufw-user-forward -i wg0 -j ACCEPT\n", "virbr1")
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	for _, want := range []string{"in on wg0", "virbr1"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
	if !strings.Contains(got.Remedy, "ufw route allow") {
		t.Errorf("remedy does not say which rule to add: %s", got.Remedy)
	}
}

// TestCheckForwarding_WarnsWhenTheRuleOnlyAllowsTrafficTowardTheGuest covers
// `ufw route allow out on virbr1`: it accepts traffic leaving by the bridge,
// not the guest's own traffic arriving on it, and it used to be read as a rule
// covering every interface.
func TestCheckForwarding_WarnsWhenTheRuleOnlyAllowsTrafficTowardTheGuest(t *testing.T) {
	t.Parallel()
	for _, bridge := range []string{"virbr1", ""} {
		t.Run("bridge="+bridge, func(t *testing.T) {
			t.Parallel()
			got := forwardingCheckOnBridge(t, "-A ufw-user-forward -o virbr1 -j ACCEPT\n", bridge)
			if got.Status != statusWarn {
				t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
			}
			if !strings.Contains(got.Detail, "out on virbr1") {
				t.Errorf("detail %q does not name the rule that was found", got.Detail)
			}
		})
	}
}

func TestCheckForwarding_PassesForARuleOnTheNATBridgeOrEveryInterface(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		rules string
		want  string
	}{
		{"rule on the bridge", "-A ufw-user-forward -i wg0 -j ACCEPT\n-A ufw-user-forward -i virbr1 -j ACCEPT\n", "in on virbr1"},
		{"rule on the bridge toward one output", "-A ufw-user-forward -i virbr1 -o eth0 -j ACCEPT\n", "in on virbr1 out on eth0"},
		{"rule naming no interface", "-A ufw-user-forward -j ACCEPT\n", "any"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := forwardingCheckOnBridge(t, tt.rules, "virbr1")
			if got.Status != statusPass {
				t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
			}
			if !strings.Contains(got.Detail, tt.want) || strings.Contains(got.Detail, "wg0") {
				t.Errorf("detail %q should name only the matching rule %q", got.Detail, tt.want)
			}
		})
	}
}

// TestCheckForwarding_SaysWhenTheBridgeIsNotKnownYet keeps the behaviour on a
// fresh host, where the NAT network is defined only on the first create: a
// rule on any input interface is accepted, and the detail says it could not be
// tied to the bridge.
func TestCheckForwarding_SaysWhenTheBridgeIsNotKnownYet(t *testing.T) {
	t.Parallel()
	got := forwardingCheckOnBridge(t, "-A ufw-user-forward -i virbr1 -j ACCEPT\n", "")
	if got.Status != statusPass {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
	}
	if !strings.Contains(got.Detail, "not known yet") {
		t.Errorf("detail %q does not say the bridge is unknown", got.Detail)
	}
}

// A rule limited to an output interface is recorded with it, rather than as a
// rule covering every interface.
func TestReadUFWRules_RecordsTheOutputInterface(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := write(t, dir, "user.rules", "-A ufw-user-forward -o virbr1 -j ACCEPT\n")

	forward, _, readable := readUFWRules(path)
	if !readable {
		t.Fatal("readUFWRules reported the file as unreadable")
	}
	if want := []forwardRule{{Out: "virbr1"}}; !reflect.DeepEqual(forward, want) {
		t.Errorf("forward = %+v, want %+v", forward, want)
	}
}
