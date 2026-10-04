package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
	"github.com/snaphop/snaphop-agent-vm/internal/tailscale"
)

// tailscaleJoin is one requested join. The key is present only so it can be
// written to the guest's stdin; nothing in this struct is safe to print
// except opts and keyPath, and the key formats as a redacted marker.
type tailscaleJoin struct {
	opts    tailscale.Options
	key     tailscale.AuthKey
	keyPath string
}

// resolveTailscale turns the create flags into a join, or nil when the
// operator did not ask for one. The auth key is read here, before anything
// on the host changes, and a bad file is a usage error.
func resolveTailscale(vmName, guestUser, keyPath, hostname, loginServer string, ephemeral, sshOn bool, tags []string) (*tailscaleJoin, error) {
	requested := keyPath != "" || hostname != "" || loginServer != "" || ephemeral || sshOn || len(tags) > 0
	if !requested {
		return nil, nil
	}
	if keyPath == "" {
		return nil, exitf(ExitUsage,
			"joining a Tailscale network needs --tailscale-auth-key-file.\n"+
				"  The other --tailscale-* flags apply only once that file is named.")
	}

	key, err := tailscale.ReadAuthKey(keyPath)
	if err != nil {
		return nil, err
	}
	if hostname == "" {
		hostname = vmName
	}
	opts, err := tailscale.Prepare(tailscale.Options{
		Hostname:      hostname,
		Operator:      guestUser,
		LoginServer:   loginServer,
		AdvertiseTags: tags,
		Ephemeral:     ephemeral,
		SSH:           sshOn,
	})
	if err != nil {
		return nil, err
	}
	return &tailscaleJoin{opts: opts, key: key, keyPath: keyPath}, nil
}

// joinTailscale installs Tailscale in the guest and logs it in. The VM
// already exists; a failure here is reported without removing it.
func (a *App) joinTailscale(ctx context.Context, req createRequest, vm *state.VM, address string) error {
	if address == "" {
		return exitf(ExitTimeout,
			"%s did not become reachable, so it was not joined to Tailscale.\n"+
				"  The VM is still there: look at it with `agent-vm info %s`.", vm.Name, vm.Name)
	}
	join := req.tailscale

	script, err := tailscale.Script()
	if err != nil {
		return err
	}

	a.out.Progress("Installing Tailscale in %s\n", vm.Name)
	if _, err := a.runGuest(ctx, vm, address, tailscale.CopyCommand(), bytes.NewReader(script), 0); err != nil {
		return tailscaleKept(vm.Name, fmt.Errorf("copying the Tailscale join script into %s: %w", vm.Name, err))
	}
	if _, err := a.runGuest(ctx, vm, address, tailscale.ChmodCommand(), nil, 0); err != nil {
		return tailscaleKept(vm.Name, fmt.Errorf("making the Tailscale join script executable on %s: %w", vm.Name, err))
	}
	if _, err := a.runGuest(ctx, vm, address, tailscale.InstallCommand(), nil, tailscale.InstallTimeout); err != nil {
		return tailscaleKept(vm.Name, fmt.Errorf("installing Tailscale in %s: %w", vm.Name, err))
	}

	a.out.Progress("Joining %s to Tailscale as %s\n", vm.Name, join.opts.Hostname)
	res, err := a.runGuest(ctx, vm, address, tailscale.UpCommand(join.opts), join.key.Reader(), tailscale.UpTimeout)
	if err != nil {
		return tailscaleKept(vm.Name, fmt.Errorf("joining %s to Tailscale: %w", vm.Name, err))
	}

	vm.Tailscale = &state.VMTailscale{
		Hostname:      join.opts.Hostname,
		LoginServer:   join.opts.LoginServer,
		AdvertiseTags: join.opts.AdvertiseTags,
		Ephemeral:     join.opts.Ephemeral,
		SSH:           join.opts.SSH,
		IPv4:          tailscale.IPv4FromOutput(res.Stdout),
	}
	if err := req.store.SaveVM(vm); err != nil {
		addr := vm.Tailscale.IPv4
		if addr == "" {
			addr = "not reported"
		}
		return fmt.Errorf("joined %s to Tailscale as %s, but recording that failed: %w\n"+
			"  The VM is still there. Its tailnet address is %s", vm.Name, join.opts.Hostname, err, addr)
	}
	return nil
}

// tailscaleKept wraps a join failure with the fact that the VM was left in
// place. The cause is wrapped, so a timeout still exits 6.
func tailscaleKept(name string, err error) error {
	return fmt.Errorf("%w\n  The VM %s is still there. Look at it with `agent-vm info %s`, or remove it with `agent-vm destroy %s`",
		err, name, name, name)
}

// runGuest runs one command on the guest over SSH. stdin is the only channel
// a secret may use; argv is logged.
func (a *App) runGuest(ctx context.Context, vm *state.VM, address string, argv []string, stdin io.Reader, timeout time.Duration) (*hostexec.Result, error) {
	cmd, err := domain.SSHCommand(domain.SSHOptions{
		User:         vm.Guest.User,
		Address:      address,
		IdentityFile: privateKeyFor(vm),
		Jump:         a.sshJump(),
		Command:      argv,
		BatchMode:    true,
	})
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdin
	if timeout > 0 {
		cmd.Timeout = timeout
	}
	return a.runner.Run(ctx, cmd)
}

// tailscaleDetail is the one-line description list and info show. It carries
// no credential.
func tailscaleDetail(ts *state.VMTailscale) string {
	if ts == nil {
		return ""
	}
	parts := []string{}
	if ts.IPv4 != "" {
		parts = append(parts, ts.IPv4)
	}
	parts = append(parts, ts.Hostname)
	detail := strings.Join(parts, " ")
	extras := []string{}
	if ts.Ephemeral {
		extras = append(extras, "ephemeral")
	}
	if ts.SSH {
		extras = append(extras, "Tailscale SSH")
	}
	if len(ts.AdvertiseTags) > 0 {
		extras = append(extras, strings.Join(ts.AdvertiseTags, ","))
	}
	if ts.LoginServer != "" {
		extras = append(extras, ts.LoginServer)
	}
	if len(extras) > 0 {
		detail += " (" + strings.Join(extras, ", ") + ")"
	}
	return detail
}

// printTailscalePlan writes the guest commands a join would run. The auth
// key is named by its path and is not printed.
func (a *App) printTailscalePlan(user string, join *tailscaleJoin) {
	dest := fmt.Sprintf("ssh%s %s@<guest address>", jumpArgs(a.sshJump()), user)
	for _, argv := range [][]string{
		tailscale.CopyCommand(),
		tailscale.ChmodCommand(),
		tailscale.InstallCommand(),
		tailscale.UpCommand(join.opts),
	} {
		a.out.Printf("%s %s\n", dest, strings.Join(argv, " "))
	}
	a.out.Printf("# Tailscale auth key read from %s and passed on the stdin of the up command; the key is not printed\n", join.keyPath)
}
