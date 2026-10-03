package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// destroyUsage is shared by the command table and the flag set, so the summary
// in `agent-vm --help` and the one `destroy --help` prints cannot disagree.
const destroyUsage = "agent-vm destroy <name> [--keep-disk] [--force] [--timeout <duration>] [--github-ssh-key]"

func destroyCommand() *command {
	return &command{
		name:    "destroy",
		summary: "power off a VM, undefine it, and delete its state",
		usage:   destroyUsage,
		run:     runDestroy,
	}
}

func runDestroy(ctx context.Context, app *App, args []string) (err error) {
	flags := newFlagSet("destroy", destroyUsage, app.Stderr)
	keepDisk := flags.Bool("keep-disk", false, "keep the overlay and state directory; only remove the libvirt domain")
	force := flags.Bool("force", false, "power off immediately instead of asking the guest; can lose guest writes")
	timeout := flags.Duration("timeout", defaultStopTimeout, "how long to wait for a graceful shutdown")
	githubSSHKey := flags.Bool("github-ssh-key", false, "also remove this VM's SSH key from your GitHub account, using gh")
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	// Loading the record first is what makes destroy safe: a VM this state
	// directory has no record of is a not-found error, and no libvirt domain or
	// path is ever touched on its behalf.
	vm, _, err := app.vmTarget(name)
	if err != nil {
		return err
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	lock, err := store.LockVM(ctx, name, "destroy")
	if err != nil {
		return err
	}
	// A lock we cannot release is host state the operator needs to know about,
	// so it is reported rather than dropped (AGENTS.md §6).
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	// The record is read again under the lock. While this destroy waited,
	// another may have removed the VM, and a create may then have made a new
	// one under the same name — whose overlay path, and so whose ownership
	// check, is the same as the old one's.
	current, manager, err := app.vmTarget(name)
	if err != nil {
		return err
	}
	if !current.CreatedAt.Equal(vm.CreatedAt) {
		return exitf(ExitConflict,
			"VM %q was destroyed and created again while this destroy waited for its lock, so nothing was changed.\n"+
				"  Run destroy again if the new VM is the one to remove.", name)
	}
	vm = current

	confirmed, err := app.confirm(destroyPrompt(vm, *keepDisk, *force, *githubSSHKey))
	if err != nil {
		return err
	}
	if !confirmed {
		app.out.Progress("Cancelled.\n")
		return nil
	}

	// The GitHub key is removed first, while the VM is still intact: a gh
	// failure then leaves a VM whose record still names the key, which is what
	// a retry needs. Doing it last would risk deleting the only record of a key
	// that is still on the account.
	if *githubSSHKey {
		if _, err := app.versions.Require(ctx, hostexec.GH); err != nil {
			return err
		}
		if err := app.removeGitHubKey(ctx, vm); err != nil {
			return err
		}
	} else if vm.Guest.GitHubKey != nil {
		app.out.Progress("Note: %s's SSH key %q is still on your GitHub account.\n"+
			"  Remove it with `agent-vm destroy --github-ssh-key`, or by hand: gh api --method DELETE user/keys/%d\n",
			vm.Name, vm.Guest.GitHubKey.Title, vm.Guest.GitHubKey.ID)
	}

	return app.destroyVM(ctx, destroyRequest{
		vm:       vm,
		store:    store,
		manager:  manager,
		keepDisk: *keepDisk,
		force:    *force,
		timeout:  *timeout,
	})
}

// destroyPrompt says exactly what is about to be removed. A confirmation that
// does not name the consequence is not a confirmation.
func destroyPrompt(vm *state.VM, keepDisk, force, githubSSHKey bool) string {
	what := fmt.Sprintf("Destroy VM %s: undefine the domain and delete %s?", vm.Name, vm.Paths.Dir)
	if keepDisk {
		what = fmt.Sprintf("Undefine the domain for VM %s, keeping %s?", vm.Name, vm.Paths.Dir)
	}
	if force {
		what += " It will be powered off immediately, losing anything the guest has not written."
	}
	if githubSSHKey && vm.Guest.GitHubKey != nil {
		what += fmt.Sprintf(" Its SSH key %q will also be removed from your GitHub account.", vm.Guest.GitHubKey.Title)
	}
	return what
}

type destroyRequest struct {
	vm       *state.VM
	store    *state.Store
	manager  *domain.Manager
	keepDisk bool
	force    bool
	timeout  time.Duration
}

// destroyVM powers the guest off, undefines the domain, and deletes the VM's
// state. Nothing is deleted until the domain is gone, so a run that stops early
// leaves a working VM rather than a half-removed one.
func (a *App) destroyVM(ctx context.Context, req destroyRequest) error {
	vm := req.vm

	current, err := req.manager.State(ctx, vm.Name)
	if err != nil {
		return err
	}

	if current != domain.StateMissing {
		if err := a.confirmOwnership(ctx, req); err != nil {
			return err
		}
		if err := a.powerOffForDestroy(ctx, req, current); err != nil {
			return err
		}
		a.out.Progress("Undefining %s\n", vm.Name)
		if err := req.manager.Undefine(ctx, vm.Name); err != nil {
			return err
		}
	} else {
		// The domain is already gone — undefined by hand, or a previous destroy
		// stopped after undefining. Removing the leftover state is exactly what
		// this command is for.
		a.out.Progress("libvirt has no domain %s; removing its leftover state\n", vm.Name)
	}

	if req.keepDisk {
		a.out.Printf("Removed the domain for %s. Its disk and state remain at %s\n", vm.Name, vm.Paths.Dir)
		return nil
	}

	// Store.Remove refuses any path that does not resolve inside the state
	// directory, so this cannot delete anything else even if vm.json were
	// tampered with.
	if err := a.removeVMState(vm); err != nil {
		return &CleanupError{
			Operation: fmt.Sprintf("destroying VM %s", vm.Name),
			Cause:     err,
			Remaining: []string{"state directory " + vm.Paths.Dir},
		}
	}

	a.out.Printf("Destroyed %s\n", vm.Name)
	return nil
}

// confirmOwnership refuses to undefine a domain that is not the one this tool
// created. The name alone is not proof: a domain called agent-01 may be
// someone else's. The overlay inside our state directory is.
func (a *App) confirmOwnership(ctx context.Context, req destroyRequest) error {
	vm := req.vm

	disks, err := req.manager.DiskPaths(ctx, vm.Name)
	if err != nil {
		return err
	}
	for _, disk := range disks {
		if disk == vm.Paths.Overlay {
			return nil
		}
	}
	return exitf(ExitConflict,
		"the libvirt domain %q does not use this VM's disk (%s), so it is not the VM recorded here.\n"+
			"  Its disks are: %v\n"+
			"  Refusing to undefine a domain this tool did not create. Remove the record with --keep-disk if it is stale.",
		vm.Name, vm.Paths.Overlay, disks)
}

// powerOffForDestroy stops the guest. Without --force it asks and waits, and a
// guest that ignores the request is reported rather than killed: destroy is
// about to delete the disk, so this is the last moment at which unwritten data
// can still be saved. Nothing has been removed when this fails.
func (a *App) powerOffForDestroy(ctx context.Context, req destroyRequest, current domain.State) error {
	vm := req.vm
	if !current.IsRunning() {
		return nil
	}

	if req.force {
		a.out.Progress("Powering off %s\n", vm.Name)
		return req.manager.ForceOff(ctx, vm.Name)
	}

	a.out.Progress("Asking %s to shut down (up to %s)\n", vm.Name, req.timeout)
	if err := req.manager.Shutdown(ctx, vm.Name); err != nil {
		return err
	}
	if a.dryRun {
		return nil
	}
	return req.manager.WaitForShutdown(ctx, vm.Name, req.timeout)
}

// removeVMState deletes the VM's directory: overlay, generated user-data,
// captured domain XML, console log, and record.
func (a *App) removeVMState(vm *state.VM) error {
	if a.dryRun {
		a.out.Printf("# remove the directory %s\n", vm.Paths.Dir)
		return nil
	}
	return a.store.Remove(vm.Paths.Dir)
}
