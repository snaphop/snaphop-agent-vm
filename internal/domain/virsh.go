package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// virtInstallTimeout bounds one define-and-start. It is generous because
// virt-install builds the cloud-init seed and waits for the domain to come up,
// and stingy enough that a wedged libvirt fails the command instead of hanging
// an agent supervisor forever.
const virtInstallTimeout = 5 * time.Minute

// pollInterval is how often a wait loop asks libvirt again. Addresses appear
// when the guest's DHCP lease or guest agent does, neither of which is worth
// polling faster than this.
const pollInterval = 2 * time.Second

// Manager runs virt-install and virsh against one libvirt connection.
type Manager struct {
	runner     hostexec.Runner
	libvirtURI string
}

// New returns a Manager bound to a libvirt connection.
func New(runner hostexec.Runner, libvirtURI string) *Manager {
	return &Manager{runner: runner, libvirtURI: libvirtURI}
}

// LibvirtURI is the connection this manager operates on.
func (m *Manager) LibvirtURI() string { return m.libvirtURI }

// State is a libvirt domain state, as `virsh domstate` names it.
type State string

// The states this tool acts on. Anything else virsh reports is passed through
// unchanged rather than collapsed into one of these, so an operator sees what
// libvirt actually said.
const (
	StateRunning    State = "running"
	StateShutOff    State = "shut off"
	StatePaused     State = "paused"
	StateInShutdown State = "in shutdown"
	StateCrashed    State = "crashed"
	// StateMissing is not a libvirt state: it is what this tool reports for a
	// VM that exists in the state directory but no longer in libvirt.
	StateMissing State = "missing"
)

// IsRunning reports whether the domain is executing.
func (s State) IsRunning() bool { return s == StateRunning }

// Create defines and starts one domain, returning the exact argument vector it
// ran so the caller can record it in vm.json — "what created this VM?" has to
// be answerable from the state directory alone.
//
// virt-install boots the guest as part of this call: a domain with cloud-init
// data is always started, because the generated seed is attached only to that
// first boot and is absent from the XML virt-install leaves defined.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) ([]string, error) {
	if opts.LibvirtURI == "" {
		opts.LibvirtURI = m.libvirtURI
	}
	args, err := VirtInstallArgs(opts)
	if err != nil {
		return nil, err
	}

	cmd := hostexec.Command{
		Name:    hostexec.VirtInstall.Name,
		Args:    args,
		Effect:  hostexec.Mutate,
		Timeout: virtInstallTimeout,
	}
	if _, err := m.runner.Run(ctx, cmd); err != nil {
		return cmd.Argv(), fmt.Errorf("defining the %s domain: %w", opts.Name, err)
	}
	return cmd.Argv(), nil
}

// Start boots a defined domain.
func (m *Manager) Start(ctx context.Context, name string) error {
	if _, err := m.run(ctx, hostexec.Mutate, "start", name); err != nil {
		return fmt.Errorf("starting the %s domain: %w", name, err)
	}
	return nil
}

// Shutdown requests a graceful ACPI shutdown. It returns as soon as the request
// is delivered; use WaitForShutdown to find out whether the guest acted on it.
// This never escalates to a force-off on its own — losing a guest's writes is
// the operator's decision to make (docs/cli.md).
func (m *Manager) Shutdown(ctx context.Context, name string) error {
	if _, err := m.run(ctx, hostexec.Mutate, "shutdown", name); err != nil {
		return fmt.Errorf("requesting shutdown of the %s domain: %w", name, err)
	}
	return nil
}

// ForceOff powers the domain off immediately: `virsh destroy`, which stops the
// guest without asking it. Despite the verb, nothing is deleted.
func (m *Manager) ForceOff(ctx context.Context, name string) error {
	if _, err := m.run(ctx, hostexec.Mutate, "destroy", name); err != nil {
		return fmt.Errorf("powering off the %s domain: %w", name, err)
	}
	return nil
}

// Undefine removes the domain definition from libvirt.
//
// --remove-all-storage is deliberately never passed: it would hand libvirt the
// decision of which files to delete, and this tool deletes only paths it has
// verified are inside its own state directory (SECURITY.md).
func (m *Manager) Undefine(ctx context.Context, name string) error {
	if _, err := m.run(ctx, hostexec.Mutate, "undefine", name); err != nil {
		return fmt.Errorf("undefining the %s domain: %w", name, err)
	}
	return nil
}

// State reports the domain's current state, or StateMissing if libvirt has no
// such domain.
func (m *Manager) State(ctx context.Context, name string) (State, error) {
	exists, err := m.Exists(ctx, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return StateMissing, nil
	}

	res, err := m.run(ctx, hostexec.Read, "domstate", name)
	if err != nil {
		return "", fmt.Errorf("reading the state of the %s domain: %w", name, err)
	}
	return State(strings.TrimSpace(string(res.Stdout))), nil
}

// Exists reports whether libvirt knows a domain by this name, running or not.
func (m *Manager) Exists(ctx context.Context, name string) (bool, error) {
	names, err := m.ListNames(ctx)
	if err != nil {
		return false, err
	}
	for _, got := range names {
		if got == name {
			return true, nil
		}
	}
	return false, nil
}

// ListNames returns every domain libvirt knows about on this connection,
// including ones this tool did not create. Callers filter by their own state
// directory: a domain that is not in state is not ours to touch.
func (m *Manager) ListNames(ctx context.Context) ([]string, error) {
	res, err := m.run(ctx, hostexec.Read, "list", "--all", "--name")
	if err != nil {
		return nil, fmt.Errorf("listing libvirt domains: %w", err)
	}
	names := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// StatesOf reports the state of several domains from one pass over libvirt's
// domain list. Listing VMs is the one operation that asks about many domains at
// once, and asking libvirt to enumerate them once per VM would make a state
// directory with twenty VMs twenty times slower for no more information.
//
// A name libvirt does not know is reported as StateMissing rather than omitted:
// a VM this tool recorded and libvirt has lost is exactly what an operator
// needs to see.
func (m *Manager) StatesOf(ctx context.Context, names []string) (map[string]State, error) {
	known, err := m.ListNames(ctx)
	if err != nil {
		return nil, err
	}
	defined := make(map[string]bool, len(known))
	for _, name := range known {
		defined[name] = true
	}

	states := make(map[string]State, len(names))
	for _, name := range names {
		if !defined[name] {
			states[name] = StateMissing
			continue
		}
		res, err := m.run(ctx, hostexec.Read, "domstate", name)
		if err != nil {
			return nil, fmt.Errorf("reading the state of the %s domain: %w", name, err)
		}
		states[name] = State(strings.TrimSpace(string(res.Stdout)))
	}
	return states, nil
}

// DumpXML returns libvirt's definition of the domain. It is captured as a
// record of what was defined; this tool never feeds it back in.
func (m *Manager) DumpXML(ctx context.Context, name string) ([]byte, error) {
	res, err := m.run(ctx, hostexec.Read, "dumpxml", name)
	if err != nil {
		return nil, fmt.Errorf("reading the XML of the %s domain: %w", name, err)
	}
	return res.Stdout, nil
}

// Interface is one guest network interface as virsh reports it.
type Interface struct {
	Name string
	MAC  string
	// Protocol is "ipv4" or "ipv6"; empty when the interface has no address.
	Protocol string
	// Address is the guest address without its prefix length.
	Address string
	// Prefix is the network prefix length, or 0 when virsh reported none.
	Prefix int
}

// addressSources are tried in order. The guest agent answers as soon as the
// guest is up, and is the only source that works in bridged mode, where the
// DHCP server belongs to the operator's LAN rather than to libvirt. The lease
// source is the fallback for a guest whose agent is not running yet.
var addressSources = []string{"agent", "lease"}

// Addresses returns the guest's addresses, trying each source in turn. An empty
// result is not an error: early in boot the guest genuinely has no address yet,
// which is what WaitForAddress polls on.
func (m *Manager) Addresses(ctx context.Context, name string) ([]Interface, error) {
	var lastErr error
	for _, source := range addressSources {
		res, err := m.run(ctx, hostexec.Read, "domifaddr", name, "--source", source)
		if err != nil {
			// A source that is unavailable — no guest agent channel, no lease
			// file — is reported by virsh as a failure of that query, not of
			// the domain, so the next source still gets a turn.
			lastErr = err
			continue
		}
		found, err := parseDomifaddr(res.Stdout)
		if err != nil {
			return nil, fmt.Errorf("reading the addresses of the %s domain: %w", name, err)
		}
		if len(found) > 0 {
			return found, nil
		}
		lastErr = nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("reading the addresses of the %s domain: %w", name, lastErr)
	}
	return nil, nil
}

// IPv4Address returns the first IPv4 address the guest has, or "" if it has
// none yet. IPv4 specifically: it is what the ssh convenience wrapper and the
// address column of `agent-vm list` report.
func (m *Manager) IPv4Address(ctx context.Context, name string) (string, error) {
	interfaces, err := m.Addresses(ctx, name)
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Protocol == "ipv4" && iface.Address != "" {
			return iface.Address, nil
		}
	}
	return "", nil
}

// MAC returns the MAC address of the domain's first interface, which is
// recorded in vm.json so a VM can be recognized in a DHCP log or on a bridge.
func (m *Manager) MAC(ctx context.Context, name string) (string, error) {
	res, err := m.run(ctx, hostexec.Read, "domiflist", name)
	if err != nil {
		return "", fmt.Errorf("listing the interfaces of the %s domain: %w", name, err)
	}
	rows, err := parseVirshTable(res.Stdout, 5)
	if err != nil {
		return "", fmt.Errorf("reading the interfaces of the %s domain: %w", name, err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0][4], nil
}

// DiskPaths returns the host files a domain's disks are backed by.
//
// It is how this tool answers "is this domain the one I created?" before
// undefining anything. A name is not proof of ownership — a domain called
// agent-01 on this host may be someone else's — but a domain whose root disk is
// the overlay inside our own state directory is ours (SECURITY.md).
func (m *Manager) DiskPaths(ctx context.Context, name string) ([]string, error) {
	res, err := m.run(ctx, hostexec.Read, "domblklist", name)
	if err != nil {
		return nil, fmt.Errorf("listing the disks of the %s domain: %w", name, err)
	}
	rows, err := parseVirshTable(res.Stdout, 2)
	if err != nil {
		return nil, fmt.Errorf("reading the disks of the %s domain: %w", name, err)
	}

	paths := []string{}
	for _, row := range rows {
		// virsh prints "-" for a device with no backing file, such as an empty
		// CDROM drive.
		if source := row[1]; source != "-" {
			paths = append(paths, source)
		}
	}
	return paths, nil
}

// TimeoutError is a wait that ran out. It is reported as exit 6, and — for the
// boot wait — deliberately leaves the VM in place so its console log can be
// read (docs/cli.md).
type TimeoutError struct {
	What    string
	Name    string
	Waited  time.Duration
	Remedy  string
	LastErr error
}

func (e *TimeoutError) Error() string {
	msg := fmt.Sprintf("%s did not %s within %s", e.Name, e.What, e.Waited)
	if e.LastErr != nil {
		msg += fmt.Sprintf(" (last error: %v)", e.LastErr)
	}
	if e.Remedy != "" {
		msg += "\n  " + e.Remedy
	}
	return msg
}

func (e *TimeoutError) Unwrap() error { return e.LastErr }

// WaitForAddress polls until the guest has an IPv4 address or timeout expires.
func (m *Manager) WaitForAddress(ctx context.Context, name string, timeout time.Duration) (string, error) {
	var lastErr error
	address, err := poll(ctx, timeout, func() (string, bool, error) {
		address, err := m.IPv4Address(ctx, name)
		if err != nil {
			// Address lookups fail transiently while a guest is booting — the
			// agent channel is not up, the lease file does not exist yet — so
			// the error is remembered and the loop continues.
			lastErr = err
			return "", false, nil
		}
		return address, address != "", nil
	})
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", &TimeoutError{
			What: "get an address", Name: name, Waited: timeout, LastErr: lastErr,
			Remedy: "Check the guest's console log for a boot failure, or run `virsh domifaddr " + name + "`.",
		}
	}
	return address, nil
}

// WaitForShutdown polls until the domain is no longer running. It never forces
// the guest off; the caller decides whether a timeout becomes a --force.
func (m *Manager) WaitForShutdown(ctx context.Context, name string, timeout time.Duration) error {
	var lastErr error
	done, err := poll(ctx, timeout, func() (bool, bool, error) {
		state, err := m.State(ctx, name)
		if err != nil {
			lastErr = err
			return false, false, nil
		}
		stopped := state == StateShutOff || state == StateCrashed || state == StateMissing
		return stopped, stopped, nil
	})
	if err != nil {
		return err
	}
	if !done {
		return &TimeoutError{
			What: "shut down", Name: name, Waited: timeout, LastErr: lastErr,
			Remedy: "The guest ignored the shutdown request. Pass --force to power it off, losing anything it had not written.",
		}
	}
	return nil
}

// poll calls check until it reports done, the deadline passes, or ctx is
// cancelled. It returns the last value check produced, so a caller can tell
// "timed out with nothing" from "timed out holding a partial answer".
func poll[T any](ctx context.Context, timeout time.Duration, check func() (T, bool, error)) (T, error) {
	var zero T

	value, done, err := check()
	if err != nil || done || timeout <= 0 {
		return value, err
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-deadline.C:
			return value, nil
		case <-ticker.C:
			value, done, err = check()
			if err != nil || done {
				return value, err
			}
		}
	}
}

// ConsoleCommand is the `virsh console` invocation for a VM. It is returned
// rather than run because the console replaces this process: the CLI execs it
// so the operator's terminal talks to the guest directly.
func (m *Manager) ConsoleCommand(name string) hostexec.Command {
	return hostexec.Command{
		Name:   hostexec.Virsh.Name,
		Args:   []string{"--connect", m.libvirtURI, "console", name},
		Effect: hostexec.Mutate,
	}
}

func (m *Manager) run(ctx context.Context, effect hostexec.Effect, args ...string) (*hostexec.Result, error) {
	return m.runner.Run(ctx, hostexec.Command{
		Name:   hostexec.Virsh.Name,
		Args:   append([]string{"--connect", m.libvirtURI}, args...),
		Effect: effect,
	})
}

// parseDomifaddr reads the table `virsh domifaddr` prints. virsh has no
// machine-readable mode for this query, so the table is parsed — narrowly, and
// against a fixture captured from the real tool (test/toolout/).
func parseDomifaddr(out []byte) ([]Interface, error) {
	rows, err := parseVirshTable(out, 4)
	if err != nil {
		return nil, err
	}

	interfaces := make([]Interface, 0, len(rows))
	for _, row := range rows {
		iface := Interface{Name: row[0], MAC: row[1], Protocol: row[2]}
		// virsh prints "N/A" for a column it has no value for, and the address
		// carries its prefix length: "192.168.171.42/24".
		if address := row[3]; address != "N/A" {
			value, prefix, hasPrefix := strings.Cut(address, "/")
			// The address originates with the guest agent or a DHCP lease, and
			// ends up in an ssh argument vector, so it is validated here rather
			// than trusted because virsh printed it (SECURITY.md).
			if err := ValidateAddress(value); err != nil {
				return nil, err
			}
			iface.Address = value
			if hasPrefix {
				if _, err := fmt.Sscanf(prefix, "%d", &iface.Prefix); err != nil {
					return nil, fmt.Errorf("unreadable address %q from virsh domifaddr", address)
				}
			}
		}
		interfaces = append(interfaces, iface)
	}
	return interfaces, nil
}

// parseVirshTable reads virsh's column output: a header line, a line of dashes,
// then one row per record. Rows with fewer than columns fields are an error
// rather than a partial record — a table this tool cannot read means virsh
// changed, and guessing at the difference is worse than saying so.
func parseVirshTable(out []byte, columns int) ([][]string, error) {
	lines := strings.Split(string(out), "\n")

	body := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			body = i + 1
			break
		}
	}
	if body < 0 {
		if strings.TrimSpace(string(out)) == "" {
			return nil, errors.New("virsh printed nothing where a table was expected")
		}
		return nil, fmt.Errorf("virsh printed no table header:\n%s", strings.TrimSpace(string(out)))
	}

	rows := [][]string{}
	for _, line := range lines[body:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < columns {
			return nil, fmt.Errorf("expected %d columns from virsh, got %d in %q", columns, len(fields), strings.TrimSpace(line))
		}
		rows = append(rows, fields[:columns])
	}
	return rows, nil
}
