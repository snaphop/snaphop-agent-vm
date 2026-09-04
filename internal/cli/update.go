package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/domain"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// defaultUpdateTimeout bounds one VM's whole update. A distribution upgrade
// downloads and unpacks hundreds of packages over the guest's NAT link, and the
// toolchains that follow it are downloads of their own, so the budget is
// generous: the point of the limit is that a guest waiting forever on a mirror
// is reported rather than hanging the operator's terminal.
const defaultUpdateTimeout = 45 * time.Minute

// sshConnectionFailed is the status ssh exits with when its own connection
// failed, as opposed to passing on the remote command's status.
const sshConnectionFailed = 255

func updateCommand() *command {
	return &command{
		name:    "update",
		summary: "bring a running VM's packages and tooling up to date",
		usage:   "agent-vm update <name>... | --all [--timeout <duration>]",
		run:     runUpdate,
	}
}

// updateResult is what one VM's update produced. It is the `--output json`
// shape, so its fields are a public contract (docs/cli.md).
type updateResult struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Updated is true only when every step of the update succeeded.
	Updated bool `json:"updated"`
	// Skipped names why a VM was passed over — it is not running — and is set
	// only under --all, where skipping is the documented behavior.
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

func runUpdate(ctx context.Context, app *App, args []string) error {
	const usage = "agent-vm update <name>... | --all [--timeout <duration>]"

	flags := newFlagSet("update", usage, app.Stderr)
	all := flags.Bool("all", false, "update every running VM in this state directory")
	timeout := flags.Duration("timeout", defaultUpdateTimeout, "how long one VM's update may take")
	names, err := flags.parseNames(args)
	if err != nil {
		return err
	}

	switch {
	case *all && len(names) > 0:
		return flags.usagef("--all takes no VM names")
	case !*all && len(names) == 0:
		return flags.usagef("name a VM to update, or pass --all")
	}

	targets, err := app.updateTargets(names, *all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		// Only reachable under --all: naming a VM that is not recorded here is
		// a not-found error, not an empty run.
		app.out.Progress("No VMs. Create one with `agent-vm create <name>`.\n")
		if app.out.format == OutputJSON {
			return app.out.JSON([]updateResult{})
		}
		return nil
	}

	results := make([]updateResult, 0, len(targets))
	failures := []error{}
	for _, target := range targets {
		result, err := app.updateOne(ctx, target, *all, *timeout)
		if err != nil {
			// Every VM is attempted: one guest with a broken mirror must not
			// leave the rest of the fleet un-updated. The failure is reported
			// as it happens, and again in the exit code at the end.
			app.out.Progress("agent-vm: %v\n", err)
			failures = append(failures, err)
		}
		results = append(results, result)
	}

	if err := app.reportUpdates(results); err != nil {
		return err
	}
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		return exitf(ExitFailure, "%d of %d VMs did not update: %s\n  Each failure is reported above.",
			len(failures), len(results), strings.Join(failedNames(results), ", "))
	}
}

// updateTarget is one VM to update, with a manager bound to the connection it
// was created on.
type updateTarget struct {
	vm      *state.VM
	manager *domain.Manager
}

// updateTargets resolves the VMs to act on. Named VMs must be recorded here;
// --all takes every record in this state directory, in name order, and never
// looks at domains this tool did not create (SECURITY.md).
func (a *App) updateTargets(names []string, all bool) ([]updateTarget, error) {
	if !all {
		targets := make([]updateTarget, 0, len(names))
		for _, name := range names {
			vm, manager, err := a.vmTarget(name)
			if err != nil {
				return nil, err
			}
			targets = append(targets, updateTarget{vm: vm, manager: manager})
		}
		return targets, nil
	}

	store, err := a.Store()
	if err != nil {
		return nil, err
	}
	vms, err := store.ListVMs()
	if err != nil {
		return nil, err
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })

	targets := make([]updateTarget, 0, len(vms))
	for _, vm := range vms {
		targets = append(targets, updateTarget{vm: vm, manager: domain.New(a.runner, a.uriFor(vm))})
	}
	return targets, nil
}

// updateOne runs the distro's package update inside one guest. It returns the
// record of what happened along with the failure, so a VM that failed is still
// reported in `--output json` rather than disappearing from the results.
func (a *App) updateOne(ctx context.Context, target updateTarget, skipStopped bool, timeout time.Duration) (updateResult, error) {
	vm := target.vm
	result := updateResult{Name: vm.Name}

	current, err := target.manager.State(ctx, vm.Name)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.State = string(current)

	switch {
	case current == domain.StateMissing:
		err := errDomainMissing(vm)
		result.Error = err.Error()
		return result, err
	case !current.IsRunning() && skipStopped:
		// Under --all a stopped VM is not a failure: it is simply not part of
		// "every running VM". Starting it to update it would be a decision the
		// operator did not ask for.
		result.Skipped = "not running"
		a.out.Progress("Skipping %s: it is %s\n", vm.Name, current)
		return result, nil
	}

	address, err := a.reachableAddress(ctx, vm, target.manager)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	steps, err := updateSteps(vm)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	sshOptions := domain.SSHOptions{
		User:         vm.Guest.User,
		Address:      address,
		IdentityFile: privateKeyFor(vm),
		Jump:         a.sshJump(),
		// An update is unattended by definition, so it must fail rather than
		// stop at a prompt.
		BatchMode: true,
	}

	// The whole update shares one budget rather than giving each step its own,
	// because --timeout is the answer to "how long may this VM take", and a
	// guest that spends it all downloading is as stuck as one that hangs.
	deadline := time.Now().Add(timeout)
	for number, step := range steps {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			err := exitf(ExitTimeout, "%s did not finish updating within %s; it stopped at %q.",
				vm.Name, timeout, step.Name)
			result.Error = err.Error()
			return result, err
		}

		if step.Requires != "" {
			present, err := a.guestHas(ctx, sshOptions, step.Requires, remaining)
			if err != nil {
				result.Error = err.Error()
				return result, err
			}
			if !present {
				a.out.Progress("[%d/%d] %s: skipping %q — no %s in this guest\n",
					number+1, len(steps), vm.Name, step.Name, step.Requires)
				continue
			}
		}

		a.out.Progress("[%d/%d] %s: %s\n", number+1, len(steps), vm.Name, step.Name)
		run := sshOptions
		run.Command = elevated(vm.Guest.User, step)
		cmd, err := domain.SSHCommand(run)
		if err != nil {
			result.Error = err.Error()
			return result, err
		}
		cmd.Timeout = remaining
		cmd.Output = a.updateOutput()

		if _, err := a.runner.Run(ctx, cmd); err != nil {
			err = fmt.Errorf("updating %s: %w", vm.Name, err)
			result.Error = err.Error()
			return result, err
		}
	}

	if a.dryRun {
		// Nothing ran, so nothing was updated; saying otherwise would be a lie
		// about the guest.
		return result, nil
	}
	result.Updated = true
	return result, nil
}

// updateOutput is where the guest's own output goes while a step runs: stderr,
// like every other kind of progress, so `--output json` on stdout stays
// machine-readable. Under --quiet it is discarded.
func (a *App) updateOutput() io.Writer {
	if a.quiet {
		return nil
	}
	return a.Stderr
}

// updateSteps is everything one VM's update runs: the package update for the
// family it was built from, then the tooling the base images install from
// outside that family's repositories — mise and the tools it manages, codex,
// and the shared Rust toolchain. A VM whose base image predates this command,
// or whose family is no longer supported, is reported rather than guessed at.
func updateSteps(vm *state.VM) ([]distro.UpdateStep, error) {
	family, _, ok := distro.LookupImage(vm.BaseImage.Distro)
	if !ok {
		return nil, exitf(ExitUsage, "%s was built from %q, which is not a distro this build of agent-vm knows how to update.",
			vm.Name, vm.BaseImage.Distro)
	}
	if len(family.PackageUpdate) == 0 {
		return nil, exitf(ExitUsage, "there is no package update defined for %s guests.", family.Name)
	}
	steps := append([]distro.UpdateStep{}, family.PackageUpdate...)
	return append(steps, distro.ToolingUpdate(vm.Guest.User)...), nil
}

// guestHas reports whether a command exists in the guest, so a step for
// software this image does not carry is skipped instead of failing the whole
// update. `command -v` is a shell builtin every guest's login shell has, and
// ssh runs what it is given through that shell.
//
// Under --dry-run nothing is probed: the point of a dry run is to print the
// plan, and a probe would either connect to the guest anyway or silently drop
// every optional step from what it prints.
func (a *App) guestHas(ctx context.Context, opts domain.SSHOptions, command string, timeout time.Duration) (bool, error) {
	if a.dryRun {
		return true, nil
	}

	probe := opts
	probe.Command = []string{"command", "-v", command}
	cmd, err := domain.SSHCommand(probe)
	if err != nil {
		return false, err
	}
	// Asking whether a command exists changes nothing.
	cmd.Effect = hostexec.Read
	cmd.Timeout = timeout

	if _, err := a.runner.Run(ctx, cmd); err != nil {
		var toolErr *hostexec.ToolError
		// `command -v` exiting non-zero is the answer "no such command", not a
		// failure of the update. ssh's own 255 is not an answer at all — a
		// connection that dropped mid-update must be reported, not read as a
		// guest without the tool.
		if errors.As(err, &toolErr) && toolErr.ExitCode != sshConnectionFailed {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// elevated runs a step as root when it asked for root. The guest user is an
// ordinary account with a passwordless sudoers drop-in from cloud-init, so sudo
// is asked not to prompt: a guest where that grant is missing must fail loudly
// rather than sit on a password prompt no one is watching. A step that is not
// marked Root runs as the guest user, because it updates that account's own
// per-user installation.
func elevated(user string, step distro.UpdateStep) []string {
	if !step.Root || user == "root" {
		return step.Argv
	}
	return append([]string{"sudo", "-n"}, step.Argv...)
}

func (a *App) reportUpdates(results []updateResult) error {
	if a.out.format == OutputJSON {
		return a.out.JSON(results)
	}
	if a.dryRun {
		// The only thing that happened is the plan the dry-run runner printed.
		return nil
	}

	rows := [][]string{{"NAME", "STATE", "RESULT"}}
	for _, result := range results {
		rows = append(rows, []string{result.Name, result.State, updateOutcome(result)})
	}
	a.out.Table(rows)
	return nil
}

func updateOutcome(result updateResult) string {
	switch {
	case result.Updated:
		return "updated"
	case result.Skipped != "":
		return "skipped: " + result.Skipped
	default:
		return "failed"
	}
}

func failedNames(results []updateResult) []string {
	names := []string{}
	for _, result := range results {
		if result.Error != "" {
			names = append(names, result.Name)
		}
	}
	return names
}
