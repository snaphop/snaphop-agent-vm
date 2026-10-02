package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/github"
	"github.com/snaphop/snaphop-agent-vm/internal/guestinit"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// guestPublicKeyPath is where the base image's first-boot unit puts the key
// pair it generates for each account (docs/cli.md, "Per-account SSH keys"). It
// is relative because ssh runs the command in the account's home directory,
// which is the only place that path is known for certain.
const guestPublicKeyPath = ".ssh/id_ed25519.pub"

// readGuestPublicKey reads the public key the guest generated for itself,
// waiting for it: the first-boot unit that generates it runs after
// cloud-final.service, which is after SSH starts answering, so it is normally
// still missing at the moment `create` finishes waiting for the login.
//
// The guest is untrusted, so what comes back is validated as a single OpenSSH
// public key line before it is used as an argument to anything (SECURITY.md).
func (a *App) readGuestPublicKey(ctx context.Context, req createRequest, vm *state.VM, address string) (string, error) {
	a.out.Progress("Reading %s's SSH key, which it generates on first boot\n", vm.Name)
	out, err := req.manager.WaitForGuestFile(ctx, vm.Name, domain.SSHOptions{
		User:         vm.Guest.User,
		Address:      address,
		IdentityFile: privateKeyFor(vm),
		Jump:         a.sshJump(),
	}, guestPublicKeyPath, req.waitForSSH)
	if err != nil {
		return "", fmt.Errorf("reading %s from %s, which the guest generates on first boot: %w",
			guestPublicKeyPath, vm.Name, err)
	}

	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", exitf(ExitConflict,
			"%s has no %s yet, so there is no key to add to GitHub.\n"+
				"  The base image generates it on first boot; a VM built from an older image may not have one.",
			vm.Name, guestPublicKeyPath)
	}
	if strings.Contains(key, "\n") {
		return "", exitf(ExitConflict, "%s returned more than one line for %s", vm.Name, guestPublicKeyPath)
	}
	if err := guestinit.ValidatePublicKeyLine(key); err != nil {
		return "", fmt.Errorf("the key %s returned is not usable: %w", vm.Name, err)
	}
	return key, nil
}

// githubKeyTitle names the key on the account. It identifies the VM and the
// host it lives on, because an account collects keys from several machines and
// "agent-vm" alone would not say which one to remove.
func githubKeyTitle(vmName string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "agent-vm " + vmName
	}
	return fmt.Sprintf("agent-vm %s on %s", vmName, host)
}

// addGitHubKey uploads the guest's public key and records it in vm.json, so
// that destroy can remove exactly the key this create added.
//
// The VM already exists by the time this runs, and a failure here is reported
// without removing it: the VM is fine, only the GitHub step is not, and the
// operator can retry it by hand with the command named in the error.
func (a *App) addGitHubKey(ctx context.Context, req createRequest, vm *state.VM, address string) error {
	if address == "" {
		return exitf(ExitTimeout,
			"%s did not become reachable, so its key could not be read and nothing was added to GitHub.\n"+
				"  The VM is still there: look at it with `agent-vm info %s`.", vm.Name, vm.Name)
	}

	key, err := a.readGuestPublicKey(ctx, req, vm, address)
	if err != nil {
		return err
	}

	title := githubKeyTitle(vm.Name)
	a.out.Progress("Adding %s's SSH key to GitHub as %q\n", vm.Name, title)
	id, err := github.New(a.runner).AddKey(ctx, title, key)
	if err != nil {
		return err
	}

	vm.Guest.GitHubKey = &state.GitHubSSHKey{
		ID:        id,
		Title:     title,
		PublicKey: key,
		AddedAt:   time.Now().UTC(),
	}
	if err := req.store.SaveVM(vm); err != nil {
		// The key is on the account but the record that would remove it is
		// not, so the operator has to be told how to remove it by hand.
		return &CleanupError{
			Operation: fmt.Sprintf("recording the GitHub SSH key added for %s", vm.Name),
			Cause:     err,
			Remaining: []string{fmt.Sprintf("GitHub SSH key %d (%s): remove it with `gh api --method DELETE user/keys/%d`", id, title, id)},
		}
	}
	return nil
}

// removeGitHubKey deletes the key this VM's create added. It runs before
// anything is destroyed, so that a gh failure leaves a working VM whose record
// still names the key.
func (a *App) removeGitHubKey(ctx context.Context, vm *state.VM) error {
	key := vm.Guest.GitHubKey
	if key == nil {
		return exitf(ExitNotFound,
			"%s has no GitHub SSH key recorded, so there is nothing to remove.\n"+
				"  Only a VM created with `agent-vm create --github-ssh-key` has one.", vm.Name)
	}

	a.out.Progress("Removing GitHub SSH key %q\n", key.Title)
	removed, err := github.New(a.runner).DeleteKey(ctx, key.ID)
	if err != nil {
		return err
	}
	if !removed {
		// Someone removed it on github.com already. The end state is the one
		// that was asked for, so this is worth saying rather than failing on.
		a.out.Progress("GitHub SSH key %d was already gone\n", key.ID)
	}
	return nil
}
