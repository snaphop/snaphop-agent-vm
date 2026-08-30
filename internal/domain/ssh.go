package domain

import (
	"context"
	"fmt"
	"net"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// sshProbeTimeout bounds one readiness probe. It is short because the probe is
// repeated: a guest that is still booting should fail fast and be asked again,
// not hold the connection open.
const sshProbeTimeout = 10 * time.Second

// SSHOptions describes a connection to a guest. This is a wrapper around the
// ssh binary, not an SSH implementation (docs/cli.md).
type SSHOptions struct {
	User string
	// Address is the guest's IP, as reported by libvirt. It is validated before
	// use: everything a guest or its DHCP lease tells us is untrusted
	// (SECURITY.md).
	Address string
	Port    int
	// IdentityFile is an optional private key path to offer. Empty means ssh
	// decides — agent keys and the operator's defaults. This tool never reads
	// the file; it only names it to ssh.
	IdentityFile string
	// Jump is the [user@]host[:port] of a machine to connect through, set when
	// the hypervisor is not this one. A guest sits on a network that exists on
	// the hypervisor — libvirt's NAT bridge is host-local by design — so it is
	// not routable from here and the connection is made through that host
	// instead (ADR-0010).
	Jump string
	// Command is run non-interactively when set; otherwise ssh opens a shell.
	Command []string
	// BatchMode fails instead of prompting. It is what a probe and a scripted
	// run want, and what an interactive session must not have.
	BatchMode bool
}

// SSHCommand builds the ssh invocation for a guest.
//
// Host key checking is deliberately disabled and no known_hosts entry is
// written. A VM here is disposable and generates a fresh host key on every
// create, so a per-VM address would collect a conflicting entry every time and
// the operator would be trained to clear them. The tradeoff is real but bounded
// in the default NAT mode, where the network is host-local; on a bridge, the
// guest shares the operator's LAN and this is weaker (SECURITY.md).
func SSHCommand(opts SSHOptions) (hostexec.Command, error) {
	if opts.User == "" {
		return hostexec.Command{}, fmt.Errorf("ssh needs a guest user")
	}
	if err := ValidateAddress(opts.Address); err != nil {
		return hostexec.Command{}, err
	}
	port := opts.Port
	if port == 0 {
		port = 22
	}

	args := []string{}
	if opts.Jump != "" {
		// The jump host is authenticated exactly the way `ssh <host>` would
		// authenticate it — the agent, ~/.ssh/config, the default identities —
		// which is already a working connection, because agent-vm runs every
		// host tool through it.
		args = append(args, "-J", opts.Jump)
	}
	args = append(args,
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		// Without this, every connection prints a warning about the discarded
		// host key and buries the guest's own output.
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
		"-p", fmt.Sprint(port),
	)
	if opts.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	if opts.IdentityFile != "" {
		// IdentitiesOnly stops ssh from offering every key in the agent first,
		// which a guest with a short MaxAuthTries would reject before reaching
		// the right one.
		args = append(args, "-i", opts.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, opts.User+"@"+opts.Address)
	args = append(args, opts.Command...)

	return hostexec.Command{
		Name: hostexec.SSH.Name,
		Args: args,
		// Whatever the operator runs in the guest is assumed to change it.
		Effect: hostexec.Mutate,
		// This runs here rather than on the hypervisor: it uses the operator's
		// keys and, for an interactive session, their terminal.
		Location: hostexec.Client,
	}, nil
}

// ValidateAddress refuses anything that is not a bare IP address. libvirt's
// answer comes from the guest agent or a DHCP lease — both of which are things
// an untrusted guest influences — and it ends up in an ssh argument vector.
func ValidateAddress(address string) error {
	if address == "" {
		return fmt.Errorf("no guest address")
	}
	if net.ParseIP(address) == nil {
		return fmt.Errorf("libvirt reported %q as the guest address, which is not an IP address", address)
	}
	return nil
}

// WaitForSSH polls until the guest accepts an SSH connection, which is the
// point at which cloud-init has finished enough to be useful: the account
// exists and the key is authorized.
//
// A timeout here is deliberately not a failure of create. The VM is left in
// place with its console log, because "it booted slowly" and "it failed to
// boot" need the same evidence (docs/cli.md).
func (m *Manager) WaitForSSH(ctx context.Context, name string, opts SSHOptions, timeout time.Duration) error {
	probe := opts
	probe.BatchMode = true
	// `true` is the smallest thing that proves a login succeeded.
	probe.Command = []string{"true"}

	cmd, err := SSHCommand(probe)
	if err != nil {
		return err
	}
	cmd.Effect = hostexec.Read
	cmd.Timeout = sshProbeTimeout

	var lastErr error
	ready, err := poll(ctx, timeout, func() (bool, bool, error) {
		if _, err := m.runner.Run(ctx, cmd); err != nil {
			// Connection refused, no route, authentication not ready yet: all
			// expected while a guest boots, all worth reporting if time runs out.
			lastErr = err
			return false, false, nil
		}
		return true, true, nil
	})
	if err != nil {
		return err
	}
	if !ready {
		return &TimeoutError{
			What: "accept SSH", Name: name, Waited: timeout, LastErr: lastErr,
			Remedy: "The VM is still running. Look at its console log, or connect with `agent-vm console " + name + "`.",
		}
	}
	return nil
}

// WaitForGuestFile reads a file from the guest, retrying until it appears.
//
// A file the guest itself creates on first boot is not there the moment SSH
// starts answering: sshd accepts a login as soon as cloud-init has created the
// account, while a unit ordered after cloud-final.service runs later still. A
// single read would therefore race the guest and fail on a VM that is merely a
// few seconds young, so this waits for the file the same way WaitForSSH waits
// for the login.
func (m *Manager) WaitForGuestFile(ctx context.Context, name string, opts SSHOptions, path string, timeout time.Duration) ([]byte, error) {
	read := opts
	read.BatchMode = true
	read.Command = []string{"cat", path}

	cmd, err := SSHCommand(read)
	if err != nil {
		return nil, err
	}
	cmd.Effect = hostexec.Read
	cmd.Timeout = sshProbeTimeout

	var lastErr error
	contents, err := poll(ctx, timeout, func() ([]byte, bool, error) {
		res, err := m.runner.Run(ctx, cmd)
		if err != nil {
			// "No such file yet" and "the guest stopped answering" look the
			// same here; both are expected while it boots, and both are worth
			// reporting if the time runs out.
			lastErr = err
			return nil, false, nil
		}
		return res.Stdout, true, nil
	})
	if err != nil {
		return nil, err
	}
	if contents == nil {
		return nil, &TimeoutError{
			What: "produce " + path, Name: name, Waited: timeout, LastErr: lastErr,
			Remedy: "The VM is still running. Check the first-boot setup with " +
				"`agent-vm ssh " + name + " -- systemctl status agent-vm-user-setup.service`.",
		}
	}
	return contents, nil
}
