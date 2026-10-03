package network

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/templates"
)

// The NAT network's addressing. libvirt's own default network uses
// 192.168.122.0/24, so a different subnet is chosen to avoid colliding with it
// on a host that has both. Changing these values orphans existing VMs.
const (
	GatewayIP = "192.168.171.1"
	Netmask   = "255.255.255.0"
	DHCPStart = "192.168.171.2"
	DHCPEnd   = "192.168.171.254"
)

// namePattern constrains the network name before it is written into XML or
// passed to virsh. Validating rather than escaping keeps one rule for a value
// that is both an XML text node and a command-line argument.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

var natTemplate = template.Must(template.New("nat.xml.tmpl").ParseFS(templates.FS, "network/nat.xml.tmpl"))

// RenderNAT produces the network XML for a NAT network of the given name.
func RenderNAT(name string) ([]byte, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid NAT network name %q: names must match %s", name, namePattern)
	}

	var out bytes.Buffer
	err := natTemplate.Execute(&out, struct {
		Name, GatewayIP, Netmask, DHCPStart, DHCPEnd string
	}{name, GatewayIP, Netmask, DHCPStart, DHCPEnd})
	if err != nil {
		return nil, fmt.Errorf("rendering NAT network XML: %w", err)
	}
	return out.Bytes(), nil
}

// XMLWriter stores the generated network XML somewhere inside the state
// directory and returns its path. virsh net-define takes a file, and keeping
// the file is useful: it is the record of what we defined.
type XMLWriter interface {
	WriteNetworkXML(name string, xml []byte) (string, error)
}

// EnsureNAT makes sure the NAT network exists and is running, defining it from
// the embedded XML if it does not. It is idempotent: on a host where the
// network is already up, it only runs read-only queries.
//
// A network that already exists under the configured name is used only if it
// forwards by NAT. Another mode — bridge, route, open — would put the guest on
// the LAN while vm.json, list, and info all say "nat", so it is refused rather
// than used (SECURITY.md, "Networking Boundaries").
func EnsureNAT(ctx context.Context, runner hostexec.Runner, libvirtURI, name string, xml XMLWriter) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid NAT network name %q: names must match %s", name, namePattern)
	}

	defined, err := networkNames(ctx, runner, libvirtURI, true)
	if err != nil {
		return err
	}
	created := false
	if contains(defined, name) {
		mode, err := forwardMode(ctx, runner, libvirtURI, name)
		if err != nil {
			return err
		}
		if mode != "nat" {
			return &ModeError{Network: name, Mode: mode}
		}
	} else {
		rendered, err := RenderNAT(name)
		if err != nil {
			return err
		}
		path, err := xml.WriteNetworkXML(name, rendered)
		if err != nil {
			return err
		}
		if _, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Mutate, "net-define", path)); err != nil {
			return fmt.Errorf("defining the %s network: %w", name, err)
		}
		created = true
	}

	active, err := networkNames(ctx, runner, libvirtURI, false)
	if err != nil {
		return err
	}
	if !contains(active, name) {
		if _, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Mutate, "net-start", name)); err != nil {
			return fmt.Errorf("starting the %s network: %w", name, err)
		}
	}

	// Autostart makes the network survive a host reboot, which is what an
	// operator expects of a network this tool created. A network someone else
	// defined keeps the autostart setting they gave it.
	if created {
		if _, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Mutate, "net-autostart", name)); err != nil {
			return fmt.Errorf("enabling autostart for the %s network: %w", name, err)
		}
	}
	return nil
}

// ModeError is an existing libvirt network, named as the NAT network, that does
// not forward by NAT.
type ModeError struct {
	Network string
	// Mode is the network's <forward mode>, or "none" for an isolated network.
	Mode string
}

func (e *ModeError) Error() string {
	return fmt.Sprintf("the libvirt network %q is configured as the NAT network, but it forwards by %q, not NAT.\n"+
		"  Using it would attach guests somewhere other than a host-local NAT network while recording them as NAT.\n"+
		"  Set [network.nat] name to a network this tool may create, or change that network yourself.", e.Network, e.Mode)
}

// forwardMode reads a network's <forward mode> from net-dumpxml, the
// machine-readable form of its definition. libvirt omits mode for its default,
// "nat", and omits <forward> entirely for an isolated network.
func forwardMode(ctx context.Context, runner hostexec.Runner, libvirtURI, name string) (string, error) {
	res, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Read, "net-dumpxml", name))
	if err != nil {
		return "", fmt.Errorf("reading the definition of the %s network: %w", name, err)
	}
	var network struct {
		Forward *struct {
			Mode string `xml:"mode,attr"`
		} `xml:"forward"`
	}
	if err := xml.Unmarshal(res.Stdout, &network); err != nil {
		return "", fmt.Errorf("reading the definition of the %s network: %w", name, err)
	}
	switch {
	case network.Forward == nil:
		return "none", nil
	case network.Forward.Mode == "":
		return "nat", nil
	}
	return network.Forward.Mode, nil
}

// NATStatus reports whether the network is defined and active, for doctor.
func NATStatus(ctx context.Context, runner hostexec.Runner, libvirtURI, name string) (defined, active bool, err error) {
	all, err := networkNames(ctx, runner, libvirtURI, true)
	if err != nil {
		return false, false, err
	}
	running, err := networkNames(ctx, runner, libvirtURI, false)
	if err != nil {
		return false, false, err
	}
	return contains(all, name), contains(running, name), nil
}

// networkNames lists libvirt networks. `net-list --name` prints one name per
// line and nothing else, which is the closest thing virsh offers to structured
// output here.
func networkNames(ctx context.Context, runner hostexec.Runner, libvirtURI string, includeInactive bool) ([]string, error) {
	args := []string{"net-list", "--name"}
	if includeInactive {
		args = append(args, "--all")
	}

	res, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Read, args...))
	if err != nil {
		return nil, fmt.Errorf("listing libvirt networks: %w", err)
	}

	names := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// virsh builds a virsh invocation against the configured connection.
func virsh(libvirtURI string, effect hostexec.Effect, args ...string) hostexec.Command {
	return hostexec.Command{
		Name:   hostexec.Virsh.Name,
		Args:   append([]string{"--connect", libvirtURI}, args...),
		Effect: effect,
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// NATBridge returns the bridge device libvirt allocated for the network, or an
// empty name when the network is not defined yet.
//
// The device is not knowable from configuration: libvirt picks the next free
// virbrN when the network is first started, so a check or a remedy that named
// virbr0 would send an operator to the wrong interface on any host that has
// more than one network. net-dumpxml is used rather than net-info because it
// is the machine-readable form of the same answer.
func NATBridge(ctx context.Context, runner hostexec.Runner, libvirtURI, name string) (string, error) {
	defined, err := networkNames(ctx, runner, libvirtURI, true)
	if err != nil {
		return "", err
	}
	if !contains(defined, name) {
		return "", nil
	}

	res, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Read, "net-dumpxml", name))
	if err != nil {
		return "", fmt.Errorf("reading the definition of the %s network: %w", name, err)
	}

	var network struct {
		Bridge struct {
			Name string `xml:"name,attr"`
		} `xml:"bridge"`
	}
	if err := xml.Unmarshal(res.Stdout, &network); err != nil {
		return "", fmt.Errorf("reading the definition of the %s network: %w", name, err)
	}
	return network.Bridge.Name, nil
}
