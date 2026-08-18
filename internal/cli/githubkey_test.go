package cli

import (
	"errors"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// The key the guest generated for itself, as `ssh <vm> cat .ssh/id_ed25519.pub`
// returns it.
const guestPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx agent@agent-01\n"

// withGitHub answers gh, and the one ssh invocation that reads the guest's
// public key. The readiness probe runs ssh too, which is why this matches on
// the remote command rather than on the tool name.
func withGitHub(fake *hostexec.Fake, keyID string) *hostexec.Fake {
	fake.Respond("gh --version", hostexec.FakeResponse{Stdout: "gh version 2.62.0 (2024-11-14)\n"})
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{Stdout: "github.com\n  ✓ Logged in to github.com\n"})
	fake.RespondPrefix("gh api --method POST user/keys", hostexec.FakeResponse{Stdout: keyID + "\n"})

	previous := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == hostexec.SSH.Name && len(c.Args) > 0 && c.Args[len(c.Args)-1] == guestPublicKeyPath {
			return hostexec.FakeResponse{Stdout: guestPublicKey}, true
		}
		if previous != nil {
			return previous(c)
		}
		return hostexec.FakeResponse{}, false
	}
	return fake
}

func TestCreate_AddsTheGuestGeneratedKeyToGitHub(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--github-ssh-key")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argv := strings.Join(fake.Argvs(), "\n")
	for _, want := range []string{
		"gh auth status --hostname github.com",
		"-f key=" + strings.TrimSpace(guestPublicKey),
		"cat " + guestPublicKeyPath,
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("create did not run %q:\n%s", want, argv)
		}
	}

	// The id is what destroy removes the key by, so it has to survive in the
	// record rather than only in the output.
	vm := loadVM(t, stateDir, "agent-01")
	if vm.Guest.GitHubKey == nil {
		t.Fatal("the GitHub key was not recorded in vm.json")
	}
	if vm.Guest.GitHubKey.ID != 119548016 {
		t.Errorf("recorded id = %d, want 119548016", vm.Guest.GitHubKey.ID)
	}
	if !strings.HasPrefix(vm.Guest.GitHubKey.Title, "agent-vm agent-01") {
		t.Errorf("recorded title = %q, want it to name the VM", vm.Guest.GitHubKey.Title)
	}
	if !strings.Contains(stdout, "github key") {
		t.Errorf("create did not report the key it added:\n%s", stdout)
	}
}

// The unit that generates the key runs after cloud-final.service, so the file
// is normally still missing when SSH first answers. create has to wait for it
// rather than fail on the first read.
func TestCreate_WaitsForTheGuestToGenerateItsKey(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")

	var reads int
	generated := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == hostexec.SSH.Name && len(c.Args) > 0 && c.Args[len(c.Args)-1] == guestPublicKeyPath {
			reads++
			if reads == 1 {
				return hostexec.FakeResponse{
					ExitCode: 1,
					Stderr:   "cat: .ssh/id_ed25519.pub: No such file or directory\n",
					Err:      errors.New("exit status 1"),
				}, true
			}
		}
		return generated(c)
	}

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--github-ssh-key")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if reads < 2 {
		t.Errorf("the key was read %d time(s); create did not retry after it was missing", reads)
	}
	if !strings.Contains(strings.Join(fake.Argvs(), "\n"), "-f key="+strings.TrimSpace(guestPublicKey)) {
		t.Errorf("the key was not added after the retry succeeded:\n%s", strings.Join(fake.Argvs(), "\n"))
	}
}

func TestCreate_WithoutTheFlagTouchesGitHubAtAll(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")

	if code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...); code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "gh ") {
			t.Errorf("gh ran without --github-ssh-key: %s", argv)
		}
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.Guest.GitHubKey != nil {
		t.Error("a GitHub key was recorded without the flag")
	}
}

// The key only exists inside the guest, so a create that never waits for the
// guest cannot read it. That is a usage error, not a silent no-op.
func TestCreate_RejectsGitHubSSHKeyWithoutWaitingForSSH(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--github-ssh-key", "--wait-for-ssh", "0")...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitUsage, stderr)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("the host was touched by a rejected create:\n%s", fake)
	}
}

// gh failing is not the VM failing: the VM is usable and stays, and the error
// says what did not happen.
func TestCreate_AGitHubFailureLeavesTheVMInPlace(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")
	fake.RespondPrefix("gh api --method POST user/keys", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: key is already in use (HTTP 422)\n",
	})

	code, _, _ := cliRun(t, fake, stateDir, createArgs(keyPath, "--github-ssh-key")...)
	if code == ExitOK {
		t.Fatal("a failed key upload was reported as success")
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM was removed because the GitHub step failed")
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.Guest.GitHubKey != nil {
		t.Error("a key that was not added was recorded anyway")
	}
}

// createdVMWithGitHubKey is a VM created with --github-ssh-key, which is the
// only kind destroy --github-ssh-key has anything to remove.
func createdVMWithGitHubKey(t *testing.T, name string) string {
	t.Helper()
	stateDir, keyPath := createEnv(t)
	fake := withGitHub(createHost(t), "119548016")

	args := []string{"create", name, "--ssh-key", keyPath, "--github-ssh-key"}
	if code, _, stderr := cliRun(t, fake, stateDir, args...); code != ExitOK {
		t.Fatalf("create %s failed with %d: %s", name, code, stderr)
	}
	return stateDir
}

func TestDestroy_RemovesTheGitHubKeyItAdded(t *testing.T) {
	stateDir := createdVMWithGitHubKey(t, "agent-01")
	fake := withGitHub(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), "119548016")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force", "--github-ssh-key")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("gh api --method DELETE user/keys/119548016 --silent") {
		t.Errorf("the key was not removed from GitHub:\n%s", fake)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM state was left behind")
	}
}

// Removing the key is opt-in, so a destroy without the flag has to say that the
// key is still on the account rather than leave it there silently.
func TestDestroy_WithoutTheFlagSaysTheKeyRemains(t *testing.T) {
	stateDir := createdVMWithGitHubKey(t, "agent-01")
	fake := withGitHub(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), "119548016")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "gh api --method DELETE") {
			t.Errorf("the key was removed without the flag: %s", argv)
		}
	}
	if !strings.Contains(stderr, "still on your GitHub account") {
		t.Errorf("destroy did not mention the key left behind:\n%s", stderr)
	}
}

func TestDestroy_AKeyAlreadyRemovedOnGitHubIsNotAFailure(t *testing.T) {
	stateDir := createdVMWithGitHubKey(t, "agent-01")
	fake := withGitHub(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), "119548016")
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Not Found (HTTP 404)\n",
	})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force", "--github-ssh-key")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM state was left behind")
	}
}

// A gh failure has to stop the destroy while the record that names the key is
// still there, or the key would be orphaned on the account.
func TestDestroy_AGitHubFailureLeavesTheVMAlone(t *testing.T) {
	stateDir := createdVMWithGitHubKey(t, "agent-01")
	fake := withGitHub(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), "119548016")
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Bad credentials (HTTP 401)\n",
	})

	code, _, _ := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force", "--github-ssh-key")
	if code == ExitOK {
		t.Fatal("a failed key removal was reported as success")
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM was destroyed even though its GitHub key could not be removed")
	}
	if fake.Ran("virsh --connect qemu:///system undefine agent-01") {
		t.Error("the domain was undefined even though the GitHub step failed")
	}
}

func TestDestroy_GitHubSSHKeyOnAVMThatHasNoneIsNotFound(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := withGitHub(ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), "119548016")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force", "--github-ssh-key")
	if code != ExitNotFound {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitNotFound, stderr)
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Error("the VM was destroyed by a run that refused")
	}
}
