package cli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

const (
	runnerOrg   = "acme"
	runnerToken = "AABFregistrationCANARYTOKEN1234567890"
	runnerID    = "884422"
)

// cacheRunnerImage writes a cached ubuntu-runner image next to the ubuntu
// image createEnv already cached. create refuses to build when the test has
// no podman, so the runner image has to be a cache hit.
func cacheRunnerImage(t *testing.T, dir string) {
	t.Helper()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	for _, path := range []string{
		store.BaseDiskPath("ubuntu-runner", "26.04"),
		store.KernelPath("ubuntu-runner", "26.04"),
		store.InitrdPath("ubuntu-runner", "26.04"),
	} {
		if err := store.WriteFile(path, []byte("artifact\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	err = store.SaveManifest(&state.Manifest{
		Distro:        "ubuntu-runner",
		Tag:           "26.04",
		SourceRef:     "docker.io/library/ubuntu:26.04",
		SourceDigest:  "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
		KernelVersion: "6.8.0-31-generic",
		KernelCmdline: "root=/dev/vda1 console=ttyS0 rw",
		ToolVersions:  map[string]string{"podman": "4.9.3", "virt-make-fs": "1.50.1"},
	})
	if err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
}

func runnerEnv(t *testing.T) (stateDir, keyPath string) {
	t.Helper()
	stateDir, keyPath = createEnv(t)
	cacheRunnerImage(t, stateDir)
	return stateDir, keyPath
}

// withRunner answers gh for an organization runner registration. The auth
// status has no scope line, which is the login shape CheckRunnerAuth accepts.
func withRunner(fake *hostexec.Fake) *hostexec.Fake {
	fake.Respond("gh --version", hostexec.FakeResponse{Stdout: "gh version 2.62.0 (2024-11-14)\n"})
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{Stdout: "github.com\n  ✓ Logged in to github.com\n"})
	fake.RespondPrefix("gh api --method POST orgs/", hostexec.FakeResponse{Stdout: runnerToken + "\n"})
	fake.RespondPrefix("gh api --paginate orgs/", hostexec.FakeResponse{Stdout: runnerID + "\n"})
	return fake
}

func runnerCreateArgs(keyPath string, extra ...string) []string {
	return createArgs(keyPath, append([]string{"--distro", "ubuntu-runner", "--github-org", runnerOrg}, extra...)...)
}

func configureCommand(t *testing.T, fake *hostexec.Fake) hostexec.Command {
	t.Helper()
	for _, c := range fake.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "agent-vm-github-runner") {
			return c
		}
	}
	t.Fatal("the guest was not asked to configure the runner")
	return hostexec.Command{}
}

func treeContains(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

func TestGitHubRunnerLabels_NamesTheOrgTheLatestAliasAndTheRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		org, family, tag string
		want             string
	}{
		{name: "ubuntu default", org: "snaphop", family: "ubuntu", tag: "26.04", want: "snaphop,ubuntu-latest,ubuntu-26.04"},
		{name: "ubuntu older release", org: "acme", family: "ubuntu", tag: "24.04", want: "acme,ubuntu-latest,ubuntu-24.04"},
		{name: "fedora", org: "acme", family: "fedora", tag: "44", want: "acme,fedora-latest,fedora-44"},
		{name: "arch snapshot", org: "acme", family: "arch", tag: "base-20260927.0.600689", want: "acme,arch-latest,arch-base-20260927.0.600689"},
		{name: "arch rolling tag", org: "acme", family: "arch", tag: "base", want: "acme,arch-latest,arch-base"},
		{name: "latest tag is already the alias", org: "acme", family: "ubuntu", tag: "latest", want: "acme,ubuntu-latest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := githubRunnerLabels(tc.org, tc.family, tc.tag); got != tc.want {
				t.Errorf("githubRunnerLabels(%q, %q, %q) = %q, want %q", tc.org, tc.family, tc.tag, got, tc.want)
			}
		})
	}
}

func TestCreate_RegistersARunnerWithTheTokenOnlyOnStdin(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))

	code, stdout, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	cmd := configureCommand(t, fake)
	if got := commandStdin(t, cmd); got != runnerToken {
		t.Fatalf("configure stdin = %q, want the registration token", got)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"sudo -n /usr/local/sbin/agent-vm-github-runner configure",
		"--url https://github.com/" + runnerOrg,
		"--name agent-01",
		"--labels " + runnerOrg + ",ubuntu-latest,ubuntu-26.04",
		"--token-file -",
		"--replace",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("configure argv is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(strings.Join(fake.Argvs(), "\n"), runnerToken) {
		t.Error("the registration token was passed as an argument")
	}
	if strings.Contains(stdout, runnerToken) || strings.Contains(stderr, runnerToken) {
		t.Error("the registration token was printed")
	}
	if treeContains(t, stateDir, runnerToken) {
		t.Error("the registration token was written under the state directory")
	}

	vm := loadVM(t, stateDir, "agent-01")
	if vm.GitHubRunner == nil {
		t.Fatal("the runner was not recorded in vm.json")
	}
	if vm.GitHubRunner.Org != runnerOrg || vm.GitHubRunner.Name != "agent-01" || vm.GitHubRunner.ID != 884422 {
		t.Errorf("recorded runner = %+v", vm.GitHubRunner)
	}
	if vm.GitHubRunner.URL != "https://github.com/"+runnerOrg {
		t.Errorf("recorded url = %q", vm.GitHubRunner.URL)
	}
	if !strings.Contains(stdout, "github runner") || !strings.Contains(stdout, runnerOrg+"/agent-01 (id 884422)") {
		t.Errorf("create did not report the runner:\n%s", stdout)
	}

	_, infoOut, infoErr := cliRun(t, createHost(t), stateDir, "info", "agent-01")
	if !strings.Contains(infoOut, "github runner") || !strings.Contains(infoOut, runnerOrg+"/agent-01 (id 884422)") {
		t.Errorf("info did not report the runner:\n%s\n%s", infoOut, infoErr)
	}
}

func TestCreate_RejectsGitHubOrgOnANonRunnerImage(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--github-org", runnerOrg)...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "ubuntu-runner") {
		t.Errorf("stderr does not name a runner image:\n%s", stderr)
	}
	if strings.Contains(strings.Join(fake.Argvs(), "\n"), "virt-install") {
		t.Error("a refused create still ran virt-install")
	}
}

func TestCreate_ARunnerWithoutAnOrgIsNotRegistered(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--distro", "ubuntu-runner")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "registration-token") || strings.Contains(argv, "agent-vm-github-runner") {
			t.Errorf("a runner with no organization was registered: %s", argv)
		}
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.GitHubRunner != nil {
		t.Errorf("an unregistered runner was recorded: %+v", vm.GitHubRunner)
	}
}

func TestCreate_RegistersFromTheConfiguredOrg(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[github]\norg = \"acme\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := cliRunCustom(t, fake, nil, append([]string{"--state-dir", stateDir, "--config", path},
		createArgs(keyPath, "--distro", "ubuntu-runner")...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.GitHubRunner == nil || vm.GitHubRunner.Org != runnerOrg {
		t.Fatalf("configured org was not used: %+v", vm.GitHubRunner)
	}
}

func TestCreate_IgnoresAConfiguredOrgForANonRunnerImage(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	fake := withRunner(createHost(t))
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[github]\norg = \"acme\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := cliRunCustom(t, fake, nil, append([]string{"--state-dir", stateDir, "--config", path},
		createArgs(keyPath)...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "registration-token") {
			t.Errorf("a non-runner image was registered because the config names an org: %s", argv)
		}
	}
}

func TestCreate_RejectsAnInvalidGitHubOrgBeforeAnyCall(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--distro", "ubuntu-runner", "--github-org", "foo/bar")...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if len(fake.Argvs()) != 0 {
		t.Errorf("an invalid org still ran host tools:\n%s", fake)
	}
}

func TestCreate_AMissingGHCreatesNothing(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	fake.Missing["gh"] = true

	code, _, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath)...)
	if code != ExitHostNotReady {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitHostNotReady, stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("a missing gh still created the VM")
	}
}

func TestCreate_ATokenWithoutTheRunnerScopeCreatesNothing(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		Stdout: "github.com\n  ✓ Logged in to github.com account wensington (keyring)\n  - Token scopes: 'repo'\n",
	})

	code, _, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath)...)
	if code != ExitFailure {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "admin:org") {
		t.Errorf("stderr does not name the scope:\n%s", stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("a refused login still created the VM")
	}
}

func TestCreate_RegisteringNeedsABootWait(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath, "--wait-for-ssh", "0")...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--wait-for-ssh") {
		t.Errorf("stderr does not name the flag:\n%s", stderr)
	}
	if len(fake.Argvs()) != 0 {
		t.Errorf("a refused create still ran host tools:\n%s", fake)
	}
}

func TestCreate_AConfigureFailureRedactsTheTokenAndKeepsTheVM(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == hostexec.SSH.Name && strings.Contains(strings.Join(c.Args, " "), "agent-vm-github-runner") {
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "config.sh rejected " + runnerToken + "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath)...)
	if code == ExitOK {
		t.Fatal("a failed configure was reported as success")
	}
	if strings.Contains(stdout, runnerToken) || strings.Contains(stderr, runnerToken) {
		t.Error("the configure failure printed the registration token")
	}
	if !strings.Contains(stderr, "github-registration-token") {
		t.Errorf("the failure was not redacted:\n%s", stderr)
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Fatal("the VM was removed because registration failed")
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.GitHubRunner != nil {
		t.Errorf("a runner that was not configured was recorded: %+v", vm.GitHubRunner)
	}
}

func TestCreate_RecordsTheRunnerWhenItsIDCannotBeRead(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	fake.RespondPrefix("gh api --paginate orgs/", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Forbidden (HTTP 403)\n",
	})

	code, _, stderr := cliRun(t, fake, stateDir, runnerCreateArgs(keyPath)...)
	if code == ExitOK {
		t.Fatal("a missing runner id was reported as success")
	}
	if !strings.Contains(stderr, "look the runner up by name") {
		t.Errorf("stderr does not say destroy can look the runner up:\n%s", stderr)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.GitHubRunner == nil || vm.GitHubRunner.Org != runnerOrg || vm.GitHubRunner.Name != "agent-01" || vm.GitHubRunner.ID != 0 {
		t.Fatalf("recorded runner = %+v, want org and name with no id", vm.GitHubRunner)
	}
	if strings.Contains(stderr, runnerToken) {
		t.Error("the id failure printed the registration token")
	}
}

func TestCreate_DryRunPrintsThePlanWithoutTheToken(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := runnerEnv(t)
	fake := createHost(t)
	fake.Missing["gh"] = true

	code, stdout, stderr := cliRun(t, fake, stateDir, append([]string{"--dry-run"}, runnerCreateArgs(keyPath)...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, want := range []string{
		"gh api --method POST orgs/acme/actions/runners/registration-token --jq .token",
		"agent-vm-github-runner configure",
		"--labels acme,ubuntu-latest,ubuntu-26.04",
		"--token-file -",
		"--replace",
		`select(.name == "agent-01")`,
		"the token is not printed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run output is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, runnerToken) || strings.Contains(stderr, runnerToken) {
		t.Error("dry-run printed a registration token")
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("dry-run created the VM")
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "registration-token") || strings.Contains(argv, "gh ") {
			t.Errorf("dry-run fetched a token: %s", argv)
		}
	}
}

func createdRunner(t *testing.T, name string) string {
	t.Helper()
	stateDir, keyPath := runnerEnv(t)
	fake := withRunner(createHost(t))
	args := []string{"create", name, "--ssh-key", keyPath, "--distro", "ubuntu-runner", "--github-org", runnerOrg}
	if code, _, stderr := cliRun(t, fake, stateDir, args...); code != ExitOK {
		t.Fatalf("create %s failed with %d: %s", name, code, stderr)
	}
	return stateDir
}

func TestDestroy_RemovesTheRecordedRunnerBeforeTheDomain(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	argvs := fake.Argvs()
	deleted := argvIndex(argvs, "gh api --method DELETE orgs/acme/actions/runners/884422")
	undefined := argvIndex(argvs, "virsh --connect qemu:///system undefine agent-01")
	if deleted < 0 || undefined < 0 || deleted > undefined {
		t.Errorf("delete index %d, undefine index %d:\n%s", deleted, undefined, strings.Join(argvs, "\n"))
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM state was left behind")
	}
}

func TestDestroy_AFailedRunnerDeleteLeavesTheDomain(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Forbidden (HTTP 403)\n",
	})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code == ExitOK {
		t.Fatalf("a failed runner delete was reported as success: %s", stderr)
	}
	if argvIndex(fake.Argvs(), "undefine agent-01") >= 0 {
		t.Error("the domain was undefined after GitHub refused the delete")
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.GitHubRunner == nil || vm.GitHubRunner.ID != 884422 {
		t.Errorf("the runner record was dropped after a failed delete: %+v", vm.GitHubRunner)
	}
}

func TestDestroy_ARunnerAlreadyGoneIsNotAFailure(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Not Found (HTTP 404)\n",
	})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("a runner GitHub had already removed blocked destroy")
	}
}

func TestDestroy_DoesNotDeleteARunnerWhenConfirmationIsCancelled(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Actions runner "+runnerOrg+"/agent-01") {
		t.Errorf("the prompt did not name the runner:\n%s", stderr)
	}
	if argvIndex(fake.Argvs(), "actions/runners") >= 0 {
		t.Errorf("a cancelled destroy still called GitHub:\n%s", strings.Join(fake.Argvs(), "\n"))
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Error("a cancelled destroy removed the VM")
	}
}

func TestDestroy_KeepDiskStillRemovesTheRunner(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--keep-disk", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if argvIndex(fake.Argvs(), "gh api --method DELETE orgs/acme/actions/runners/884422") < 0 {
		t.Error("keep-disk left the GitHub runner in place")
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.GitHubRunner != nil {
		t.Errorf("the runner is still recorded: %+v", vm.GitHubRunner)
	}
}

func TestDestroy_LooksUpARunnerWithNoRecordedID(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	store, err := state.OpenExisting(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := store.LoadVM("agent-01")
	if err != nil {
		t.Fatal(err)
	}
	vm.GitHubRunner.ID = 0
	if err := store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}

	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))
	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if argvIndex(fake.Argvs(), "gh api --paginate orgs/acme/actions/runners") < 0 {
		t.Error("destroy did not look the runner up by name")
	}
	if argvIndex(fake.Argvs(), "gh api --method DELETE orgs/acme/actions/runners/884422") < 0 {
		t.Error("destroy did not delete the runner it looked up")
	}
}

func TestDestroy_RefusesToGuessWhenTwoRunnersShareTheName(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	store, err := state.OpenExisting(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := store.LoadVM("agent-01")
	if err != nil {
		t.Fatal(err)
	}
	vm.GitHubRunner.ID = 0
	if err := store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}

	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))
	fake.RespondPrefix("gh api --paginate orgs/", hostexec.FakeResponse{Stdout: "10\n11\n"})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code == ExitOK {
		t.Fatalf("two runners of the same name were accepted: %s", stderr)
	}
	if argvIndex(fake.Argvs(), "gh api --method DELETE") >= 0 || argvIndex(fake.Argvs(), "undefine agent-01") >= 0 {
		t.Errorf("destroy guessed which runner to delete:\n%s", strings.Join(fake.Argvs(), "\n"))
	}
}

func TestDestroy_AMissingRecordedRunnerIsAlreadyDone(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	store, err := state.OpenExisting(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := store.LoadVM("agent-01")
	if err != nil {
		t.Fatal(err)
	}
	vm.GitHubRunner.ID = 0
	if err := store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}

	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))
	fake.RespondPrefix("gh api --paginate orgs/", hostexec.FakeResponse{Stdout: "\n"})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if argvIndex(fake.Argvs(), "gh api --method DELETE") >= 0 {
		t.Error("destroy deleted a runner GitHub does not have")
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("a runner that was already gone blocked destroy")
	}
}

func TestDestroy_DryRunPrintsTheDeleteAndKeepsTheRecord(t *testing.T) {
	t.Parallel()
	stateDir := createdRunner(t, "agent-01")
	fake := withRunner(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"))

	code, stdout, stderr := cliRunStdin(t, fake, stateDir, "", "--dry-run", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "gh api --method DELETE orgs/acme/actions/runners/884422 --silent") {
		t.Errorf("dry-run did not print the delete:\n%s", stdout)
	}
	if argvIndex(fake.Argvs(), "gh api --method DELETE") >= 0 {
		t.Error("dry-run deleted the runner")
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.GitHubRunner == nil || vm.GitHubRunner.ID != 884422 {
		t.Errorf("dry-run changed the runner record: %+v", vm.GitHubRunner)
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Error("dry-run removed the VM")
	}
}

func argvIndex(argvs []string, substr string) int {
	for i, argv := range argvs {
		if strings.Contains(argv, substr) {
			return i
		}
	}
	return -1
}

// cliRunCustom is cliRun with the caller's environment and the caller's full
// argument vector, so a test can set AGENT_VM_GITHUB_ORG or point --config at
// a file it wrote. cliRun pins both.
func cliRunCustom(t *testing.T, fake *hostexec.Fake, env func(string) string, args ...string) (int, string, string) {
	t.Helper()
	if env == nil {
		env = func(string) string { return "" }
	}
	var stdout, stderr strings.Builder
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: env, Runner: fake, StateFS: state.Local(),
	}
	code := app.Main(context.Background(), args)
	return code, stdout.String(), stderr.String()
}
