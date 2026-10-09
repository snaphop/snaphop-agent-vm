package cli

import (
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

// natConfig is the configuration this check is about: NAT mode on a local
// system connection.
func natConfig() *config.Config {
	return &config.Config{LibvirtURI: "qemu:///system", NATNetwork: "agent-vm-nat"}
}

// TestCheckGuestServices_WarnsWhenDHCPIsDropped is the regression this check
// exists for, and the host it was found on: ufw enabled with its default
// deny (incoming), no rule for the bridge, and a guest that boots, waits in
// systemd-networkd-wait-online forever, and never gets an address — while
// doctor reported the host as ready.
func TestCheckGuestServices_WarnsWhenDHCPIsDropped(t *testing.T) {
	t.Parallel()
	rules := "### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0\n-A ufw-user-input -p tcp --dport 22 -j ACCEPT\n"
	got := guestServicesCheckWithRules(t, enabledAndDropping, rules, natConfig(), "virbr1")

	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	for _, want := range []string{"virbr1", "DHCP (67/udp)", "DNS (53)", "dnsmasq"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
	// The bridge came from libvirt, so the remedy is a rule that can be run as
	// printed rather than a placeholder to resolve first.
	for _, want := range []string{
		"ufw allow in on virbr1 to any port 67 proto udp",
		"ufw allow in on virbr1 to any port 53",
		"never gets a DHCP lease",
		"docs/host-setup.md",
	} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy does not contain %q: %s", want, got.Remedy)
		}
	}
}

// Without a bridge — the NAT network is defined on the first create — the
// remedy cannot name an interface, so it must say how to find one instead of
// guessing virbr0.
func TestCheckGuestServices_TellsTheOperatorToLookUpAnUnknownBridge(t *testing.T) {
	t.Parallel()
	got := guestServicesCheckWithRules(t, enabledAndDropping, noRules, natConfig(), "")

	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	for _, want := range []string{"on <bridge>", "net-info", "agent-vm-nat", "do not assume virbr0"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy does not contain %q: %s", want, got.Remedy)
		}
	}
}

// TestCheckGuestServices_PassesOnceTheRulesExist covers the fix an operator
// applies after the warning: doctor must stop warning about a host that works.
func TestCheckGuestServices_PassesOnceTheRulesExist(t *testing.T) {
	t.Parallel()
	got := guestServicesCheckWithRules(t, enabledAndDropping, allRules, natConfig(), "virbr1")

	if got.Status != statusPass {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
	}
	if !strings.Contains(got.Detail, "virbr1") {
		t.Errorf("detail %q does not name the interface the rules cover", got.Detail)
	}
}

// TestCheckGuestServices_WarnsAboutOnlyTheMissingService keeps the report
// honest when one of the two rules is already there.
func TestCheckGuestServices_WarnsAboutOnlyTheMissingService(t *testing.T) {
	t.Parallel()
	rules := "-A ufw-user-input -i virbr1 -p udp --dport 67 -j ACCEPT\n"
	got := guestServicesCheckWithRules(t, enabledAndDropping, rules, natConfig(), "virbr1")

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

// An input rule on some other interface must not be credited to the bridge the
// guest is actually on.
func TestCheckGuestServices_DoesNotCreditAnotherInterfacesRules(t *testing.T) {
	t.Parallel()
	rules := `-A ufw-user-input -i docker0 -p udp --dport 67 -j ACCEPT
-A ufw-user-input -i docker0 -p udp --dport 53 -j ACCEPT
`
	got := guestServicesCheckWithRules(t, enabledAndDropping, rules, natConfig(), "virbr1")
	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
	if !strings.Contains(got.Detail, "virbr1") {
		t.Errorf("detail %q does not name the bridge the guest is on", got.Detail)
	}
}

// A rule naming no interface applies to every interface, so it covers the
// bridge too.
func TestCheckGuestServices_AcceptsInterfacelessInputRules(t *testing.T) {
	t.Parallel()
	rules := `-A ufw-user-input -p udp --dport 67 -j ACCEPT
-A ufw-user-input -p udp --dport 53 -j ACCEPT
`
	got := guestServicesCheckWithRules(t, enabledAndDropping, rules, natConfig(), "virbr1")
	if got.Status != statusPass {
		t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
	}
}

func TestCheckGuestServices_PassesWhenTheHostCannotBlockTheGuest(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		conf     string
		defaults string
	}{
		{"input policy is accept", "ENABLED=yes\n", "DEFAULT_INPUT_POLICY=\"ACCEPT\"\n"},
		{"ufw is disabled", "ENABLED=no\n", "DEFAULT_INPUT_POLICY=\"DROP\"\n"},
		{"ufw is not installed", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := guestServicesCheckWithRules(t, ufwFiles{tt.conf, tt.defaults}, "", natConfig(), "virbr1")
			if got.Status != statusPass {
				t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusPass, got.Detail)
			}
		})
	}
}

// An unreadable rules file must not be mistaken for a permissive one: on the
// distributions where it is root-only, saying so is the honest answer.
func TestCheckGuestServices_WarnsWhenTheRulesCannotBeRead(t *testing.T) {
	t.Parallel()
	got := guestServicesCheck(natConfig(), ufwState{
		Installed: true, Enabled: true, InputPolicy: "DROP", RulesReadable: false,
	}, "virbr1")

	if got.Status != statusWarn {
		t.Fatalf("status = %q, want %q", got.Status, statusWarn)
	}
	if !strings.Contains(got.Detail, "could not be confirmed") {
		t.Errorf("detail %q does not say the rules could not be read", got.Detail)
	}
}

// A bridged guest gets its lease and its resolver from the LAN, so the host's
// input hook never sees it.
func TestCheckGuestServices_SkipsBridgedMode(t *testing.T) {
	t.Parallel()
	cfg := natConfig()
	cfg.Network = config.NetworkBridge
	cfg.Bridge = "br0"

	got := guestServicesCheckWithRules(t, enabledAndDropping, "", cfg, "")
	if got.Status != statusSkip {
		t.Errorf("status = %q, want %q", got.Status, statusSkip)
	}
}

// A default bridge in the configuration does not mean VMs are created bridged,
// so the check must still report on a NAT host that has one.
func TestCheckGuestServices_ReportsNATEvenWithADefaultBridgeConfigured(t *testing.T) {
	t.Parallel()
	cfg := natConfig()
	cfg.Network = config.NetworkNAT
	cfg.Bridge = "br0"

	got := guestServicesCheckWithRules(t, enabledAndDropping, noRules, cfg, "virbr1")
	if got.Status != statusWarn {
		t.Errorf("status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
	}
}

// The firewall that matters for a remote hypervisor is that host's, and these
// files describe this one.
func TestCheckGuestServices_SkipsARemoteHypervisor(t *testing.T) {
	t.Parallel()
	got := checkGuestServices(natConfig(), &config.Connection{Remote: true, SSHDestination: "kvm-host"}, "", ufwState{})
	if got.Status != statusSkip {
		t.Fatalf("status = %q, want %q", got.Status, statusSkip)
	}
	if !strings.Contains(got.Remedy, "kvm-host") {
		t.Errorf("remedy %q does not name the hypervisor to run doctor on", got.Remedy)
	}
}

// noRules is a readable rules file with nothing in it but ufw's own header,
// which is what a host that has never been given a rule looks like.
const noRules = "*filter\n:ufw-user-input - [0:0]\n### RULES ###\nCOMMIT\n"

// ufwFiles is the pair of configuration files the check reads.
type ufwFiles struct{ conf, defaults string }

// enabledAndDropping is the stock ufw configuration: enabled, with both of the
// defaults that make this check necessary.
var enabledAndDropping = ufwFiles{
	conf:     "ENABLED=yes\n",
	defaults: "DEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_FORWARD_POLICY=\"DROP\"\n",
}

// guestServicesCheckWithRules runs the check against temporary ufw files, read
// through the same helper the check uses.
func guestServicesCheckWithRules(t *testing.T, files ufwFiles, rules string, cfg *config.Config, bridge string) check {
	t.Helper()
	dir := t.TempDir()
	state := readUFWState(
		write(t, dir, "ufw.conf", files.conf),
		write(t, dir, "ufw", files.defaults),
		write(t, dir, "user.rules", rules),
	)
	return guestServicesCheck(cfg, state, bridge)
}
