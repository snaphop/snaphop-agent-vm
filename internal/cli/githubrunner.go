package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/github"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// githubRunnerConfigure is the in-guest wrapper the runner image installs.
// The token is not an argument of this command: it is written to stdin and
// the wrapper reads it with --token-file -.
const githubRunnerConfigure = "/usr/local/sbin/agent-vm-github-runner"

// githubRunnerConfigureTimeout bounds config.sh plus the service install.
// The default hostexec timeout is two minutes, and a first configure can
// spend most of that downloading the runner's action archives.
const githubRunnerConfigureTimeout = 3 * time.Minute

// githubOrgForCreate is the organization to register with, or "" when this
// create should leave the runner unconfigured. A configured organization on
// a non-runner image is ignored: a default org must not change an ordinary
// create. An explicit --github-org on a non-runner image is rejected by the
// caller before this is used.
func githubOrgForCreate(cfg *config.Config) string {
	if cfg.Distro.Variant != distro.Runner || cfg.GitHubOrg == "" {
		return ""
	}
	return cfg.GitHubOrg
}

// githubRunnerConfigureArgv is the guest command that registers the runner.
// --replace lets a recreated VM of the same name take the place of a runner
// GitHub still has. No labels, runner group, or ephemeral flag: those stay
// the operator's choice when they configure by hand.
func githubRunnerConfigureArgv(org, name string) []string {
	return []string{
		"sudo", "-n", githubRunnerConfigure, "configure",
		"--url", github.OrgURL(org),
		"--name", name,
		"--token-file", "-",
		"--replace",
	}
}

// registerGitHubRunner fetches a registration token and configures the guest.
// The VM already exists. A failure here is reported without removing it.
// The token crosses only on stdin (ADR-0015).
func (a *App) registerGitHubRunner(ctx context.Context, req createRequest, vm *state.VM, address string) error {
	if address == "" {
		return exitf(ExitTimeout,
			"%s did not become reachable, so it was not registered with GitHub.\n"+
				"  The VM is still there: look at it with `agent-vm info %s`.", vm.Name, vm.Name)
	}
	org := req.githubOrg
	client := github.New(a.runner)

	a.out.Progress("Registering %s as a GitHub Actions runner in %s\n", vm.Name, org)
	token, err := client.RegistrationToken(ctx, org)
	if err != nil {
		return runnerKept(vm.Name, fmt.Errorf("fetching a registration token for %s: %w", org, err))
	}

	_, err = a.runGuest(ctx, vm, address, githubRunnerConfigureArgv(org, vm.Name), token.Reader(), githubRunnerConfigureTimeout)
	if err != nil {
		return runnerKept(vm.Name, fmt.Errorf("configuring the GitHub Actions runner in %s: %w", vm.Name, token.RedactError(err)))
	}

	// The id is GitHub's, looked up by the VM's name. The guest's .runner
	// file is not read: a guest can write any id it likes.
	id, idErr := client.RunnerID(ctx, org, vm.Name)
	vm.GitHubRunner = &state.GitHubRunner{
		Org:  org,
		Name: vm.Name,
		ID:   id,
		URL:  github.OrgURL(org),
	}
	if saveErr := req.store.SaveVM(vm); saveErr != nil {
		return &CleanupError{
			Operation: fmt.Sprintf("recording the GitHub Actions runner registered for %s", vm.Name),
			Cause:     saveErr,
			Remaining: []string{runnerRemovalHint(org, vm.Name, id)},
		}
	}
	if idErr != nil {
		return runnerKept(vm.Name, fmt.Errorf("registered %s as a GitHub Actions runner in %s, but its id could not be read: %w\n"+
			"  destroy will look the runner up by name", vm.Name, org, idErr))
	}
	return nil
}

// runnerRemovalHint is the hand command for a runner that was registered but
// whose record was not written. With an id it is the DELETE. Without one it
// is the lookup that finds the id.
func runnerRemovalHint(org, name string, id int64) string {
	if id > 0 {
		return fmt.Sprintf("GitHub Actions runner %s/%s (id %d): remove it with `gh api --method DELETE orgs/%s/actions/runners/%d`",
			org, name, id, org, id)
	}
	return fmt.Sprintf("GitHub Actions runner %s/%s was registered. Look up its id with `gh api --paginate orgs/%s/actions/runners --jq %s` and remove it with `gh api --method DELETE orgs/%s/actions/runners/<id>`",
		org, name, org, fmt.Sprintf(`.runners[] | select(.name == %q) | .id`, name), org)
}

// runnerKept wraps a registration failure with the fact that the VM was left
// in place. The cause is wrapped, so a timeout still exits 6.
func runnerKept(name string, err error) error {
	return fmt.Errorf("%w\n  The VM %s is still there. Look at it with `agent-vm info %s`, or remove it with `agent-vm destroy %s`",
		err, name, name, name)
}

// removeGitHubRunner deletes the runner this VM's create registered. It runs
// before the domain is undefined, so a gh failure leaves a working VM whose
// record still names the runner. A runner that is already gone is success.
func (a *App) removeGitHubRunner(ctx context.Context, store *state.Store, vm *state.VM) error {
	rec := vm.GitHubRunner
	if rec == nil {
		return nil
	}
	client := github.New(a.runner)
	// GitHub answers 404 both for a runner that is gone and for a token
	// without the runner scope. The scope is checked first so that a 404
	// below can only mean the runner is not in this organization.
	if err := client.CheckRunnerAuth(ctx); err != nil {
		return err
	}

	id := rec.ID
	if id == 0 {
		lookedUp, err := client.RunnerID(ctx, rec.Org, rec.Name)
		if err != nil {
			var missing *github.RunnerNotFoundError
			if errors.As(err, &missing) {
				a.out.Progress("GitHub has no Actions runner named %q in %s. It was already removed.\n", rec.Name, rec.Org)
				return a.clearGitHubRunner(store, vm)
			}
			return err
		}
		id = lookedUp
	}

	a.out.Progress("Removing GitHub Actions runner %s/%s\n", rec.Org, rec.Name)
	removed, err := client.DeleteRunner(ctx, rec.Org, id)
	if err != nil {
		return err
	}
	if !removed {
		a.out.Progress("GitHub has no Actions runner %d in %s. It was already removed.\n", id, rec.Org)
	}
	return a.clearGitHubRunner(store, vm)
}

// clearGitHubRunner drops the runner from the record before the domain is
// removed. A later failure then retries a runner GitHub no longer has, which
// is not an error. A dry run prints the DELETE and leaves the record alone.
func (a *App) clearGitHubRunner(store *state.Store, vm *state.VM) error {
	vm.GitHubRunner = nil
	if a.dryRun {
		return nil
	}
	if err := store.SaveVM(vm); err != nil {
		return fmt.Errorf("removed the GitHub Actions runner for %s, but recording that failed: %w\n"+
			"  The VM is still there. Run destroy again; a runner that is already gone is not an error", vm.Name, err)
	}
	return nil
}

// githubRunnerDetail is the one-line description create and info show.
func githubRunnerDetail(runner *state.GitHubRunner) string {
	if runner == nil {
		return ""
	}
	if runner.ID > 0 {
		return fmt.Sprintf("%s/%s (id %d)", runner.Org, runner.Name, runner.ID)
	}
	return runner.Org + "/" + runner.Name
}

// printGitHubRunnerPlan writes the gh and ssh lines a registration would run.
// The token is fetched by the first gh command and is not printed.
func (a *App) printGitHubRunnerPlan(user, org, name string) {
	a.out.Printf("%s\n", a.runner.Render(hostexec.Command{
		Name: hostexec.GH.Name,
		Args: []string{"auth", "status", "--hostname", "github.com"},
	}))
	a.out.Printf("%s\n", a.runner.Render(hostexec.Command{
		Name: hostexec.GH.Name,
		Args: github.RegistrationTokenArgs(org),
	}))
	a.out.Printf("ssh%s %s@<guest address> %s\n", jumpArgs(a.sshJump()), user, strings.Join(githubRunnerConfigureArgv(org, name), " "))
	a.out.Printf("%s\n", a.runner.Render(hostexec.Command{
		Name: hostexec.GH.Name,
		Args: github.RunnerIDArgs(org, name),
	}))
	a.out.Printf("# GitHub registration token passed on the stdin of the configure command; the token is not printed\n")
}
