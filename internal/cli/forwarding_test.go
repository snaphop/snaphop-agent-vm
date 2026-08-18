package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
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
	got := forwardingCheckFor(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
		Bridge:     "br0",
	})
	if got.Status != statusSkip {
		t.Errorf("status = %q, want %q", got.Status, statusSkip)
	}
}

// TestCheckForwarding_WarnsWhenThePolicyCannotBeRead keeps an unreadable file
// from being mistaken for a permissive one.
func TestCheckForwarding_WarnsWhenThePolicyCannotBeRead(t *testing.T) {
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

// TestCheckForwarding_WarnsWhenOnlyForwardingIsAllowed is the false pass this
// check used to give. Forwarding is only half of what NAT mode needs: the
// guest also asks the host's dnsmasq for a DHCP lease and for DNS, and that
// traffic is inbound rather than forwarded, so ufw's separate deny (incoming)
// default governs it. A host with just the route rule looks configured and
// still produces guests that never get an address.
func TestCheckForwarding_WarnsWhenOnlyForwardingIsAllowed(t *testing.T) {
	rules := `### tuple ### route:allow any any 0.0.0.0/0 any 0.0.0.0/0 in_virbr1
-A ufw-user-forward -i virbr1 -j ACCEPT
`
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", rules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	for _, want := range []string{"virbr1", "DHCP (67/udp)", "DNS (53)"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
	// The interface is known here, so the remedy gives rules that can be run
	// as printed rather than a placeholder to substitute.
	for _, want := range []string{"ufw allow in on virbr1 to any port 67 proto udp", "ufw allow in on virbr1 to any port 53", "docs/host-setup.md"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy does not contain %q: %s", want, got.Remedy)
		}
	}
}

// TestCheckForwarding_WarnsAboutOnlyTheMissingService keeps the report honest
// when one of the two rules is already there.
func TestCheckForwarding_WarnsAboutOnlyTheMissingService(t *testing.T) {
	rules := `-A ufw-user-input -i virbr1 -p udp --dport 67 -j ACCEPT
-A ufw-user-forward -i virbr1 -j ACCEPT
`
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", rules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q", got.Status, statusWarn)
	}
	if !strings.Contains(got.Detail, "DNS (53)") {
		t.Errorf("detail %q does not name the missing service", got.Detail)
	}
	if strings.Contains(got.Detail, "DHCP") {
		t.Errorf("detail %q reports DHCP as missing when its rule is present", got.Detail)
	}
}

// A rule naming no interface applies to every interface, so it satisfies the
// requirement for a bridge that is named by the forward rule.
func TestCheckForwarding_AcceptsInterfacelessInputRules(t *testing.T) {
	rules := `-A ufw-user-input -p udp --dport 67 -j ACCEPT
-A ufw-user-input -p udp --dport 53 -j ACCEPT
-A ufw-user-forward -i virbr1 -j ACCEPT
`
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", rules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusPass {
		t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
	}
}

// An input rule on a different interface must not be credited to the bridge
// that actually forwards.
func TestCheckForwarding_DoesNotCreditAnotherInterfacesRules(t *testing.T) {
	rules := `-A ufw-user-input -i docker0 -p udp --dport 67 -j ACCEPT
-A ufw-user-input -i docker0 -p udp --dport 53 -j ACCEPT
-A ufw-user-forward -i virbr1 -j ACCEPT
`
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", rules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	if !strings.Contains(got.Detail, "virbr1") {
		t.Errorf("detail %q does not name the forwarding interface", got.Detail)
	}
}

// TestCheckForwarding_WarnsWhenRulesAreReadableAndEmpty is the host this bug
// was found on: ufw enabled, dropping, and no forward rule at all.
func TestCheckForwarding_WarnsWhenRulesAreReadableAndEmpty(t *testing.T) {
	rules := "### tuple ### allow any 22 0.0.0.0/0 any 0.0.0.0/0\n-A ufw-user-input -p tcp --dport 22 -j ACCEPT\n"
	got := forwardingCheckWithRules(t, "ENABLED=yes\n", "DEFAULT_FORWARD_POLICY=\"DROP\"\n", rules, &config.Config{
		LibvirtURI: "qemu:///system",
		NATNetwork: "agent-vm-nat",
	})
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	if !strings.Contains(got.Detail, "no rule") {
		t.Errorf("detail %q does not say that no forwarding rule exists", got.Detail)
	}
}

// A rule that accepts forwarding on every interface has no -i flag.
func TestReadUFWRules_TreatsAnInterfacelessRuleAsAny(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "user.rules", "-A ufw-user-forward -j ACCEPT\n")

	forward, _, readable := readUFWRules(path)
	if !readable {
		t.Fatal("readUFWRules reported the file as unreadable")
	}
	if len(forward) != 1 || forward[0] != anyInterface {
		t.Errorf("forward = %v, want [any]", forward)
	}
}

// A DROP rule in the same chain must not be read as permission to forward.
func TestReadUFWRules_IgnoresRulesThatDoNotAccept(t *testing.T) {
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
	return forwardingCheck(cfg, state)
}
