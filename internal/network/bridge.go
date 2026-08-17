// Package network ensures a VM has something to attach to: libvirt's NAT
// network in the default mode, or a validated host bridge when the operator
// explicitly asks for one.
//
// This package never creates, modifies, or deletes a host bridge, and never
// touches routing or firewall rules (SECURITY.md). A missing or down bridge is
// reported with the command that fixes it.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// bridgeLink is the subset of `ip -json link` output we read. Using the
// machine-readable mode is deliberate: the human-readable output of `ip link`
// is not a stable interface.
type bridgeLink struct {
	IfName    string   `json:"ifname"`
	OperState string   `json:"operstate"`
	Flags     []string `json:"flags"`
}

// up reports whether the bridge is usable. operstate for a bridge with no
// carrier reads "DOWN" even while the interface is administratively UP, so both
// are required: a bridge with nothing plugged into it cannot carry guest
// traffic.
func (l bridgeLink) up() bool {
	if !strings.EqualFold(l.OperState, "UP") {
		return false
	}
	for _, flag := range l.Flags {
		if flag == "UP" {
			return true
		}
	}
	return false
}

// BridgeError is a host bridge that is missing or not up. It is a
// host-readiness failure (exit 3), not a half-created VM, and it carries the
// command the operator needs.
type BridgeError struct {
	Interface string
	Reason    string
	Available []string
}

func (e *BridgeError) Error() string {
	msg := fmt.Sprintf("host bridge %q %s", e.Interface, e.Reason)
	if len(e.Available) > 0 {
		msg += fmt.Sprintf("\n  bridges on this host: %s", strings.Join(e.Available, ", "))
	} else {
		msg += "\n  this host has no bridge interfaces"
	}
	msg += fmt.Sprintf("\n  create or bring one up with `ip link add %s type bridge && ip link set %s up`, or with your network manager",
		e.Interface, e.Interface)
	return msg
}

// ValidateBridge confirms that iface exists on the host and is up. It is the
// only check performed for bridged mode — attaching the guest is
// virt-install's job.
func ValidateBridge(ctx context.Context, runner hostexec.Runner, iface string) error {
	links, err := listBridges(ctx, runner)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(links))
	for _, link := range links {
		names = append(names, link.IfName)
	}
	sort.Strings(names)

	for _, link := range links {
		if link.IfName != iface {
			continue
		}
		if !link.up() {
			return &BridgeError{Interface: iface, Reason: "exists but is not up", Available: names}
		}
		return nil
	}
	return &BridgeError{Interface: iface, Reason: "does not exist", Available: names}
}

// ListBridgeNames returns the host's bridge interfaces, for doctor's output.
func ListBridgeNames(ctx context.Context, runner hostexec.Runner) ([]string, error) {
	links, err := listBridges(ctx, runner)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(links))
	for _, link := range links {
		names = append(names, link.IfName)
	}
	sort.Strings(names)
	return names, nil
}

func listBridges(ctx context.Context, runner hostexec.Runner) ([]bridgeLink, error) {
	res, err := runner.Run(ctx, hostexec.Command{
		Name:   hostexec.IP.Name,
		Args:   []string{"-json", "link", "show", "type", "bridge"},
		Effect: hostexec.Read,
	})
	if err != nil {
		return nil, err
	}
	return parseBridgeLinks(res.Stdout)
}

// parseBridgeLinks reads `ip -json link` output. A parse failure is an error
// rather than an empty list, so "this host has no bridges" and "ip changed its
// output" stay distinguishable (SECURITY.md).
func parseBridgeLinks(out []byte) ([]bridgeLink, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, &hostexec.ParseError{Tool: "ip", What: "bridge list", Output: "empty output"}
	}

	var links []bridgeLink
	if err := json.Unmarshal([]byte(trimmed), &links); err != nil {
		return nil, &hostexec.ParseError{Tool: "ip", What: "bridge list", Output: trimmed}
	}
	for _, link := range links {
		if link.IfName == "" {
			return nil, &hostexec.ParseError{Tool: "ip", What: "bridge list (an entry has no ifname)", Output: trimmed}
		}
	}
	return links, nil
}
