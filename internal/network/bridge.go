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
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// bridgeLink is the subset of `ip -d -json link` output we read. Using the
// machine-readable mode is deliberate: the human-readable output of `ip link`
// is not a stable interface.
type bridgeLink struct {
	IfName    string   `json:"ifname"`
	OperState string   `json:"operstate"`
	Flags     []string `json:"flags"`
	LinkInfo  linkInfo `json:"linkinfo"`
}

// linkInfo is the type-specific detail `ip -d` adds. Without -d there is no
// linkinfo at all, which is why the bridge listing asks for it: the spanning
// tree settings below live nowhere else, and they are worth 30 seconds of
// every VM's boot.
type linkInfo struct {
	InfoData bridgeInfo `json:"info_data"`
}

// bridgeInfo is the bridge's own configuration. ForwardDelay is in
// centiseconds, as the kernel keeps it and as `ip` prints it: the 1500 in
// `ip -d link` output is 15 seconds.
type bridgeInfo struct {
	STPState     int `json:"stp_state"`
	ForwardDelay int `json:"forward_delay"`
}

// forwardDelay is the configured delay as a duration. An older `ip`, or a
// listing taken without -d, reports nothing here, and zero is then both the
// honest answer and the one that raises no warning.
func (l bridgeLink) forwardDelay() time.Duration {
	return time.Duration(l.LinkInfo.InfoData.ForwardDelay) * 10 * time.Millisecond
}

// runsSTP reports whether the bridge participates in the spanning tree
// protocol, which is what makes forwardDelay apply to a guest's tap port.
func (l bridgeLink) runsSTP() bool {
	return l.LinkInfo.InfoData.STPState != 0
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

// BridgeWarning is a bridge that works but makes every guest on it boot
// slowly. It is not an error: the VM comes up and is reachable, just half a
// minute later than it needed to be.
//
// A bridge running STP holds a newly added port — which is what a guest's tap
// device is — in the listening and learning states for the forward delay each,
// so nothing the guest sends leaves the host for twice that time. The guest
// cannot complete DHCP until it does, so `systemd-networkd-wait-online` blocks,
// cloud-init's network stage waits behind it, and sshd starts last. With the
// 15-second default that is 30 seconds added to every single boot, and no
// guest-side setting can shorten it: the frames genuinely do not pass.
//
// The fix belongs to the operator, because host network configuration is
// theirs and this tool never changes it (SECURITY.md).
type BridgeWarning struct {
	Interface    string
	ForwardDelay time.Duration
}

// Detail says what is wrong, in the terms an operator can check themselves.
func (w *BridgeWarning) Detail() string {
	return fmt.Sprintf("%s runs STP with a %s forward delay, which adds about %s to every guest's boot",
		w.Interface, w.ForwardDelay, 2*w.ForwardDelay)
}

// Remedy is the command that fixes it. Turning STP off is the right answer for
// a bridge whose only ports are guest taps and one uplink — there is no loop
// for the protocol to prevent — and a zero forward delay is the conservative
// alternative for a bridge that has other ports on it.
func (w *BridgeWarning) Remedy() string {
	return fmt.Sprintf("Run `sudo ip link set %s type bridge stp_state 0`, or `forward_delay 0` to keep STP, "+
		"and make it persistent in whatever manages the bridge. agent-vm never modifies host network configuration.",
		w.Interface)
}

func (w *BridgeWarning) String() string { return w.Detail() + "\n  " + w.Remedy() }

// ValidateBridge confirms that iface exists on the host and is up. It is the
// only check performed for bridged mode — attaching the guest is
// virt-install's job.
//
// A non-nil warning alongside a nil error means the bridge is usable but will
// slow every boot on it; the caller reports it and carries on.
func ValidateBridge(ctx context.Context, runner hostexec.Runner, iface string) (*BridgeWarning, error) {
	links, err := listBridges(ctx, runner)
	if err != nil {
		return nil, err
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
			return nil, &BridgeError{Interface: iface, Reason: "exists but is not up", Available: names}
		}
		if delay := link.forwardDelay(); link.runsSTP() && delay > 0 {
			return &BridgeWarning{Interface: iface, ForwardDelay: delay}, nil
		}
		return nil, nil
	}
	return nil, &BridgeError{Interface: iface, Reason: "does not exist", Available: names}
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
		Args:   []string{"-d", "-json", "link", "show", "type", "bridge"},
		Effect: hostexec.Read,
	})
	if err != nil {
		return nil, err
	}
	return parseBridgeLinks(res.Stdout)
}

// parseBridgeLinks reads `ip -d -json link` output. A parse failure is an error
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
