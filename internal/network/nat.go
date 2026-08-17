package network

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/templates"
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
// network is already up, it only runs the two read-only queries.
func EnsureNAT(ctx context.Context, runner hostexec.Runner, libvirtURI, name string, xml XMLWriter) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid NAT network name %q: names must match %s", name, namePattern)
	}

	defined, err := networkNames(ctx, runner, libvirtURI, true)
	if err != nil {
		return err
	}
	if !contains(defined, name) {
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
	// operator expects of a network this tool created.
	if _, err := runner.Run(ctx, virsh(libvirtURI, hostexec.Mutate, "net-autostart", name)); err != nil {
		return fmt.Errorf("enabling autostart for the %s network: %w", name, err)
	}
	return nil
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
