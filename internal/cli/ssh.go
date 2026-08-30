package cli

import (
	"context"
	"flag"
	"os"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/domain"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

func sshCommand() *command {
	return &command{
		name:    "ssh",
		summary: "open a shell on a VM, or run one command in it",
		usage:   "agent-vm ssh <name> [-- <command>...]",
		run:     runSSH,
	}
}

func consoleCommand() *command {
	return &command{
		name:    "console",
		summary: "attach to a VM's serial console",
		usage:   "agent-vm console <name>",
		run:     runConsole,
	}
}

func runSSH(ctx context.Context, app *App, args []string) error {
	// Everything after `--` is the guest's command line and must not be read as
	// flags of ours: `agent-vm ssh web -- ls -la` runs `ls -la` in the guest.
	args, remote := splitRemoteCommand(args)

	flags := flag.NewFlagSet("ssh", flag.ContinueOnError)
	flags.SetOutput(app.Stderr)
	name, err := parseNamed(flags, args, "agent-vm ssh <name> [-- <command>...]")
	if err != nil {
		return err
	}

	vm, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}
	address, err := app.reachableAddress(ctx, vm, manager)
	if err != nil {
		return err
	}

	cmd, err := domain.SSHCommand(domain.SSHOptions{
		User:         vm.Guest.User,
		Address:      address,
		IdentityFile: privateKeyFor(vm),
		Jump:         app.sshJump(),
		Command:      remote,
		// A command given on the command line is scripted, so it must fail
		// rather than stop at a prompt. An interactive session may prompt.
		BatchMode: len(remote) > 0,
	})
	if err != nil {
		return err
	}
	return app.become(cmd)
}

func runConsole(ctx context.Context, app *App, args []string) error {
	flags := flag.NewFlagSet("console", flag.ContinueOnError)
	flags.SetOutput(app.Stderr)
	name, err := parseNamed(flags, args, "agent-vm console <name>")
	if err != nil {
		return err
	}

	vm, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}
	current, err := manager.State(ctx, vm.Name)
	if err != nil {
		return err
	}
	switch {
	case current == domain.StateMissing:
		return errDomainMissing(vm)
	case !current.IsRunning():
		// The console of a stopped guest has nothing on it, but the log of its
		// last boot does — which is usually what the operator actually wants.
		return exitf(ExitConflict,
			"%s is not running (it is %s), so it has no console.\n"+
				"  Its last boot was logged to %s.", vm.Name, current, vm.Paths.ConsoleLog)
	}

	app.out.Progress("Attaching to %s. Escape character is ^] .\n", vm.Name)
	return app.become(manager.ConsoleCommand(vm.Name))
}

// splitRemoteCommand divides our arguments from the guest's at the first `--`.
func splitRemoteCommand(args []string) (ours, remote []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// reachableAddress resolves the guest's address, refusing when there is nothing
// to connect to. Both refusals name the state the VM is actually in, because
// "connection refused" from ssh would not.
func (a *App) reachableAddress(ctx context.Context, vm *state.VM, manager *domain.Manager) (string, error) {
	current, err := manager.State(ctx, vm.Name)
	if err != nil {
		return "", err
	}
	switch {
	case current == domain.StateMissing:
		return "", errDomainMissing(vm)
	case !current.IsRunning():
		return "", exitf(ExitConflict, "%s is not running (it is %s). Start it with `agent-vm start %s`.",
			vm.Name, current, vm.Name)
	}

	address, err := manager.IPv4Address(ctx, vm.Name)
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", exitf(ExitTimeout,
			"%s is running but has no address yet, so there is nothing to connect to.\n"+
				"  It may still be booting — watch it with `agent-vm console %s`.", vm.Name, vm.Name)
	}
	return address, nil
}

// privateKeyFor returns the private key matching one of the VM's authorized
// public keys, by the usual convention that `id_ed25519.pub` sits beside
// `id_ed25519`. Only the public key path is recorded — private key material
// never enters this tool's state (SECURITY.md) — so the private key is located,
// never read, and it is offered to ssh only when it actually exists. Without
// one, ssh falls back to the agent and the operator's defaults.
func privateKeyFor(vm *state.VM) string {
	for _, public := range vm.Guest.SSHKeyPaths {
		private, isPublic := strings.CutSuffix(public, ".pub")
		if !isPublic {
			continue
		}
		if info, err := os.Stat(private); err == nil && !info.IsDir() {
			return private
		}
	}
	return ""
}
