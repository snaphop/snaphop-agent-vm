package cli

import (
	"context"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// defaultStopTimeout is how long a graceful shutdown is given before the tool
// reports that the guest ignored it. It never turns into a force-off on its
// own: losing a guest's unwritten data is the operator's decision (docs/cli.md).
const defaultStopTimeout = 60 * time.Second

func startCommand() *command {
	return &command{
		name:    "start",
		summary: "start a stopped VM",
		usage:   "agent-vm start <name>",
		run:     runStart,
	}
}

func stopCommand() *command {
	return &command{
		name:    "stop",
		summary: "shut down a running VM",
		usage:   "agent-vm stop <name> [--timeout <duration>] [--force]",
		run:     runStop,
	}
}

func restartCommand() *command {
	return &command{
		name:    "restart",
		summary: "shut a VM down and start it again",
		usage:   "agent-vm restart <name> [--timeout <duration>] [--force]",
		run:     runRestart,
	}
}

// guestNIC is the host's own account of a VM's network interface, which is what
// its address is looked up from.
func guestNIC(vm *state.VM) domain.NIC {
	return domain.NIC{Bridged: vm.Network.Mode == config.NetworkBridge, MAC: vm.Network.MAC}
}

// vmTarget loads a recorded VM and returns a manager bound to the connection it
// was created on. Every command that acts on a VM goes through here, which is
// what enforces the rule that this tool only touches domains it has a record
// of: a name with no record is a not-found error, never a libvirt lookup.
func (a *App) vmTarget(name string) (*state.VM, *domain.Manager, error) {
	store, err := a.Store()
	if err != nil {
		return nil, nil, err
	}
	vm, err := store.LoadVM(name)
	if err != nil {
		return nil, nil, err
	}
	return vm, domain.New(a.runner, a.uriFor(vm)), nil
}

func runStart(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("start", "agent-vm start <name>", app.Stderr)
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	vm, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}
	if err := app.startVM(ctx, vm, manager); err != nil {
		return err
	}
	return app.reportState(ctx, vm, manager)
}

func runStop(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("stop", "agent-vm stop <name> [--timeout <duration>] [--force]", app.Stderr)
	timeout := flags.Duration("timeout", defaultStopTimeout, "how long to wait for a graceful shutdown")
	force := flags.Bool("force", false, "power off immediately instead of asking the guest; can lose guest writes")
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	vm, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}
	if err := app.stopVM(ctx, vm, manager, *timeout, *force); err != nil {
		return err
	}
	return app.reportState(ctx, vm, manager)
}

func runRestart(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("restart", "agent-vm restart <name> [--timeout <duration>] [--force]", app.Stderr)
	timeout := flags.Duration("timeout", defaultStopTimeout, "how long to wait for a graceful shutdown")
	force := flags.Bool("force", false, "power off immediately instead of asking the guest; can lose guest writes")
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	vm, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}

	// A restart that could not stop the guest must not start anything: the
	// operator is left with the VM exactly as it was, and the timeout to act on.
	if err := app.stopVM(ctx, vm, manager, *timeout, *force); err != nil {
		return err
	}
	if err := app.startVM(ctx, vm, manager); err != nil {
		return err
	}
	return app.reportState(ctx, vm, manager)
}

func (a *App) startVM(ctx context.Context, vm *state.VM, manager *domain.Manager) error {
	current, err := manager.State(ctx, vm.Name)
	if err != nil {
		return err
	}
	switch {
	case current == domain.StateMissing:
		return errDomainMissing(vm)
	case current.IsRunning():
		return exitf(ExitConflict, "%s is already running.", vm.Name)
	}

	a.out.Progress("Starting %s\n", vm.Name)
	return manager.Start(ctx, vm.Name)
}

// stopVM shuts a guest down. Without --force it asks the guest and waits; a
// guest that ignores the request is reported as a timeout (exit 6) with the VM
// still running, rather than being powered off on its behalf.
func (a *App) stopVM(ctx context.Context, vm *state.VM, manager *domain.Manager, timeout time.Duration, force bool) error {
	current, err := manager.State(ctx, vm.Name)
	if err != nil {
		return err
	}
	switch {
	case current == domain.StateMissing:
		return errDomainMissing(vm)
	case !current.IsRunning():
		return exitf(ExitConflict, "%s is not running (it is %s).", vm.Name, current)
	}

	if force {
		a.out.Progress("Powering off %s\n", vm.Name)
		return manager.ForceOff(ctx, vm.Name)
	}

	a.out.Progress("Asking %s to shut down (up to %s)\n", vm.Name, timeout)
	if err := manager.Shutdown(ctx, vm.Name); err != nil {
		return err
	}
	if a.dryRun {
		// Nothing was actually asked to stop, so there is nothing to wait for.
		return nil
	}
	return manager.WaitForShutdown(ctx, vm.Name, timeout)
}

// errDomainMissing is a VM this tool recorded that libvirt no longer has. It is
// reported rather than repaired: redefining someone's domain from a stale
// record is not this tool's call.
func errDomainMissing(vm *state.VM) error {
	return exitf(ExitNotFound,
		"%s is recorded here but libvirt has no such domain on %s.\n"+
			"  It was probably undefined by hand. Remove the record with `agent-vm destroy %s`.",
		vm.Name, vm.LibvirtURI, vm.Name)
}

// reportState prints where the VM ended up. Under --dry-run it prints nothing:
// the state on the host did not change, and reporting the unchanged one as the
// outcome would be a lie.
func (a *App) reportState(ctx context.Context, vm *state.VM, manager *domain.Manager) error {
	if a.dryRun {
		return nil
	}

	current, err := manager.State(ctx, vm.Name)
	if err != nil {
		return err
	}
	status := vmStatus{VM: vm, State: string(current)}
	if current.IsRunning() {
		address, err := manager.IPv4Address(ctx, vm.Name, guestNIC(vm))
		if err != nil {
			a.logger.Debug("could not read guest address", "vm", vm.Name, "error", err)
		}
		status.Address = address
	}

	if a.out.format == OutputJSON {
		return a.out.JSON(status)
	}
	if status.Address != "" {
		a.out.Printf("%s is %s at %s\n", vm.Name, status.State, status.Address)
		return nil
	}
	a.out.Printf("%s is %s\n", vm.Name, status.State)
	return nil
}
