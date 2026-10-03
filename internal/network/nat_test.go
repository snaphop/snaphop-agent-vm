package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// xmlDir writes generated network XML into a temp directory, standing in for
// the state directory.
type xmlDir struct{ dir string }

func (x xmlDir) WriteNetworkXML(name string, xml []byte) (string, error) {
	path := filepath.Join(x.dir, name+".xml")
	return path, os.WriteFile(path, xml, 0o644)
}

const (
	listAll    = "virsh --connect qemu:///system net-list --name --all"
	listActive = "virsh --connect qemu:///system net-list --name"
	dumpXML    = "virsh --connect qemu:///system net-dumpxml agent-vm-nat"
)

func TestEnsureNAT_DefinesStartsAndAutostartsAMissingNetwork(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "default\n"}).
		Respond(listActive, hostexec.FakeResponse{Stdout: "default\n"})
	dir := t.TempDir()

	if err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{dir}); err != nil {
		t.Fatalf("EnsureNAT: %v", err)
	}

	want := []string{
		"virsh --connect qemu:///system net-define " + filepath.Join(dir, "agent-vm-nat.xml"),
		"virsh --connect qemu:///system net-start agent-vm-nat",
		"virsh --connect qemu:///system net-autostart agent-vm-nat",
	}
	for _, argv := range want {
		if !fake.Ran(argv) {
			t.Errorf("EnsureNAT did not run %q\n%s", argv, fake)
		}
	}
}

func TestEnsureNAT_DoesNotRedefineANetworkThatIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "default\nagent-vm-nat\n"}).
		Respond(listActive, hostexec.FakeResponse{Stdout: "default\nagent-vm-nat\n"}).
		Respond(dumpXML, hostexec.FakeResponse{Stdout: toolout(t, "virsh-net-dumpxml.xml")})

	if err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{t.TempDir()}); err != nil {
		t.Fatalf("EnsureNAT: %v", err)
	}

	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "net-define") || strings.Contains(argv, "net-start") {
			t.Errorf("EnsureNAT redefined or restarted an existing network: %q", argv)
		}
	}
}

func TestEnsureNAT_StartsANetworkThatIsDefinedButInactive(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "agent-vm-nat\n"}).
		Respond(listActive, hostexec.FakeResponse{Stdout: "\n"}).
		Respond(dumpXML, hostexec.FakeResponse{Stdout: toolout(t, "virsh-net-dumpxml.xml")})

	if err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{t.TempDir()}); err != nil {
		t.Fatalf("EnsureNAT: %v", err)
	}

	if !fake.Ran("virsh --connect qemu:///system net-start agent-vm-nat") {
		t.Errorf("EnsureNAT did not start an inactive network\n%s", fake)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "net-define") {
			t.Errorf("EnsureNAT redefined an already-defined network: %q", argv)
		}
	}
}

func TestEnsureNAT_LeavesTheAutostartOfANetworkItDidNotDefine(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "agent-vm-nat\n"}).
		Respond(listActive, hostexec.FakeResponse{Stdout: "agent-vm-nat\n"}).
		Respond(dumpXML, hostexec.FakeResponse{Stdout: toolout(t, "virsh-net-dumpxml.xml")})

	if err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{t.TempDir()}); err != nil {
		t.Fatalf("EnsureNAT: %v", err)
	}
	if fake.Ran("virsh --connect qemu:///system net-autostart agent-vm-nat") {
		t.Error("EnsureNAT changed the autostart setting of a network it did not define")
	}
}

func TestEnsureNAT_RefusesAnExistingNetworkThatDoesNotForwardByNAT(t *testing.T) {
	t.Parallel()
	nat := toolout(t, "virsh-net-dumpxml.xml")
	natForward := "<forward mode='nat'>\n    <nat>\n      <port start='1024' end='65535'/>\n    </nat>\n  </forward>"
	if !strings.Contains(nat, natForward) {
		t.Fatalf("the fixture no longer has the expected <forward> element:\n%s", nat)
	}
	for _, tt := range []struct {
		name, forward, want string
	}{
		// A bridged or routed network puts the guest on the LAN.
		{"bridged", "<forward mode='bridge'/>", "bridge"},
		{"routed", "<forward mode='route'/>", "route"},
		{"isolated", "", "none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := hostexec.NewFake().
				Respond(listAll, hostexec.FakeResponse{Stdout: "agent-vm-nat\n"}).
				Respond(listActive, hostexec.FakeResponse{Stdout: "\n"}).
				Respond(dumpXML, hostexec.FakeResponse{Stdout: strings.Replace(nat, natForward, tt.forward, 1)})

			err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{t.TempDir()})
			var mode *ModeError
			if !errors.As(err, &mode) || mode.Mode != tt.want {
				t.Fatalf("EnsureNAT = %v, want a ModeError naming %q", err, tt.want)
			}
			for _, argv := range fake.Argvs() {
				if strings.Contains(argv, "net-start") || strings.Contains(argv, "net-autostart") {
					t.Errorf("EnsureNAT changed a network it refused: %q", argv)
				}
			}
		})
	}
}

func TestEnsureNAT_TouchesOnlyTheNetworkItManages(t *testing.T) {
	t.Parallel()
	// The tool must never modify or delete a network it did not create.
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "default\nlibvirt-routed\n"}).
		Respond(listActive, hostexec.FakeResponse{Stdout: "default\n"})

	if err := EnsureNAT(context.Background(), fake, "qemu:///system", "agent-vm-nat", xmlDir{t.TempDir()}); err != nil {
		t.Fatalf("EnsureNAT: %v", err)
	}

	for _, argv := range fake.Argvs() {
		for _, foreign := range []string{"default", "libvirt-routed"} {
			if strings.HasSuffix(argv, " "+foreign) {
				t.Errorf("EnsureNAT acted on a network it does not own: %q", argv)
			}
		}
		if strings.Contains(argv, "net-destroy") || strings.Contains(argv, "net-undefine") {
			t.Errorf("EnsureNAT ran a destructive network command: %q", argv)
		}
	}
}

func TestEnsureNAT_RejectsAnInvalidNetworkName(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()

	err := EnsureNAT(context.Background(), fake, "qemu:///system", "../../etc/passwd", xmlDir{t.TempDir()})
	if err == nil {
		t.Fatal("EnsureNAT accepted a name that is not a valid network name")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("EnsureNAT ran commands before validating the name: %s", fake)
	}
}

func TestRenderNAT_ProducesLibvirtStandardNATOnly(t *testing.T) {
	t.Parallel()
	xml, err := RenderNAT("agent-vm-nat")
	if err != nil {
		t.Fatalf("RenderNAT: %v", err)
	}
	got := string(xml)

	for _, want := range []string{
		"<name>agent-vm-nat</name>",
		`<forward mode="nat">`,
		`<ip address="192.168.171.1" netmask="255.255.255.0">`,
		`<range start="192.168.171.2" end="192.168.171.254"/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("network XML is missing %s:\n%s", want, got)
		}
	}

	// Nothing here may widen the guest's reach beyond libvirt's standard NAT.
	for _, forbidden := range []string{"<forward mode=\"bridge\"", "<forward mode=\"route\"", "<portgroup", "<dev "} {
		if strings.Contains(got, forbidden) {
			t.Errorf("network XML contains %s, which changes the guest's exposure:\n%s", forbidden, got)
		}
	}
}

func TestRenderNAT_RejectsANameThatWouldNeedEscaping(t *testing.T) {
	t.Parallel()
	for _, name := range []string{`net"><forward mode="bridge`, "net with spaces", "../escape", ""} {
		if _, err := RenderNAT(name); err == nil {
			t.Errorf("RenderNAT(%q) = nil, want an error", name)
		}
	}
}

// TestNATBridge_ReadsTheDeviceLibvirtAllocated covers the lookup doctor's
// firewall checks depend on: the device is virbr1 here, not virbr0, which is
// exactly why it is asked for rather than assumed.
func TestNATBridge_ReadsTheDeviceLibvirtAllocated(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "default\nagent-vm-nat\n"}).
		Respond("virsh --connect qemu:///system net-dumpxml agent-vm-nat",
			hostexec.FakeResponse{Stdout: toolout(t, "virsh-net-dumpxml.xml")})

	bridge, err := NATBridge(context.Background(), fake, "qemu:///system", "agent-vm-nat")
	if err != nil {
		t.Fatalf("NATBridge: %v", err)
	}
	if bridge != "virbr1" {
		t.Errorf("bridge = %q, want %q", bridge, "virbr1")
	}
}

// A network that has never been created has no bridge, and that is the normal
// state of a fresh host rather than an error.
func TestNATBridge_ReportsNoBridgeForAnUndefinedNetwork(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake().
		Respond(listAll, hostexec.FakeResponse{Stdout: "default\n"})

	bridge, err := NATBridge(context.Background(), fake, "qemu:///system", "agent-vm-nat")
	if err != nil {
		t.Fatalf("NATBridge: %v", err)
	}
	if bridge != "" {
		t.Errorf("bridge = %q, want no bridge", bridge)
	}
}
