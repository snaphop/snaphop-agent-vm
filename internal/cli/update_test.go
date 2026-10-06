package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// sshArgvs returns the guest command of every ssh invocation, joined, so a test
// can assert on what was run inside the guest rather than on the ssh options.
func sshArgvs(fake *hostexec.Fake) []string {
	guest := []string{}
	for _, call := range fake.Calls() {
		if call.Name != "ssh" {
			continue
		}
		for i, arg := range call.Args {
			if strings.Contains(arg, "@") {
				guest = append(guest, strings.Join(call.Args[i+1:], " "))
				break
			}
		}
	}
	return guest
}

func TestUpdate_RunsTheDistrosPackageUpdateInTheGuest(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, stdout, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	guest := sshArgvs(fake)
	want := []string{
		"sudo -n apt-get update",
		"sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -y dist-upgrade",
		"sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -y --purge autoremove",
	}
	for _, step := range want {
		if !contains(guest, step) {
			t.Errorf("the guest was never asked to run %q:\n%s", step, fake)
		}
	}
	if !strings.Contains(stdout, "updated") {
		t.Errorf("the outcome is not reported:\n%s", stdout)
	}
}

func TestUpdate_RunsUnattendedSoItCannotStopAtAPrompt(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	if code, _, stderr := cliRun(t, fake, stateDir, "update", "agent-01"); code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, call := range fake.Calls() {
		if call.Name != "ssh" {
			continue
		}
		if !contains(call.Args, "BatchMode=yes") {
			t.Errorf("an update must not be able to sit on an ssh prompt: %v", call.Argv())
		}
	}
}

func TestUpdate_ReportsAFailingStepAndStopsThatVM(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "ssh" && contains(c.Args, "update") {
			return hostexec.FakeResponse{ExitCode: 100, Stderr: "Could not resolve 'archive.ubuntu.com'\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, _, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "archive.ubuntu.com") {
		t.Errorf("the guest's own diagnosis should reach the operator:\n%s", stderr)
	}
	for _, step := range sshArgvs(fake) {
		if strings.Contains(step, "dist-upgrade") {
			t.Errorf("a failed refresh must not be followed by an upgrade: %q", step)
		}
	}
}

func TestUpdate_RefusesAVMThatIsNotRunning(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(stderr, "not running") {
		t.Errorf("the refusal should say what state the VM is in:\n%s", stderr)
	}
	if len(sshArgvs(fake)) != 0 {
		t.Errorf("a stopped VM must not be connected to:\n%s", fake)
	}
}

func TestUpdate_ReportsAVMThatIsNotRecordedHere(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")

	code, _, _ := cliRun(t, runningHost(t, "agent-01"), stateDir, "update", "someone-elses-vm")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
}

func TestUpdate_AllSkipsStoppedVMsInsteadOfStartingThem(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, stdout, stderr := cliRun(t, fake, stateDir, "update", "--all")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "skipped: not running") {
		t.Errorf("a skipped VM should be reported as skipped:\n%s", stdout)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, " start ") {
			t.Errorf("--all must not start a stopped VM: %v", argv)
		}
	}
}

func TestUpdate_AllAttemptsEveryVMEvenAfterOneFails(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	if code, _, stderr := cliRun(t, createHost(t), stateDir, "create", "agent-02",
		"--ssh-key", keyFile(t)); code != ExitOK {
		t.Fatalf("creating the second VM failed with %d: %s", code, stderr)
	}

	fake := runningHost(t, "agent-01", "agent-02")
	// The two guests answer on different addresses, so the ssh invocations of
	// one can be failed without touching the other's.
	fake.RespondPrefix("virsh --connect qemu:///system domifaddr agent-02",
		hostexec.FakeResponse{Stdout: strings.ReplaceAll(readToolout(t, "virsh-domifaddr.txt"), "192.168.122.3", "192.168.122.4")})
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "ssh" && contains(c.Args, "agent@192.168.122.3") {
			return hostexec.FakeResponse{ExitCode: 100, Stderr: "mirror unreachable\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, _ := cliRun(t, fake, stateDir, "update", "--all")
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	// The second VM is still updated, and both outcomes are reported.
	if !contains(sshArgvs(fake), "sudo -n apt-get update") {
		t.Errorf("the VM after the failing one was never updated:\n%s", fake)
	}
	for _, want := range []string{"agent-01", "failed", "agent-02", "updated"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report is missing %q:\n%s", want, stdout)
		}
	}
}

// keyFile writes a public key for a create that does not need its own state
// directory.
func keyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(path, []byte(publicKey), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	return path
}

func TestUpdate_JSONReportsEachVM(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")

	code, stdout, stderr := cliRun(t, runningHost(t, "agent-01"), stateDir,
		"--output", "json", "update", "--all")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	var results []updateResult
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("decoding the result: %v\n%s", err, stdout)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %s", len(results), stdout)
	}
	if results[0].Name != "agent-01" || !results[0].Updated || results[0].State != "running" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestUpdate_WithoutATargetIsAUsageError(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")

	code, _, _ := cliRun(t, runningHost(t, "agent-01"), stateDir, "update")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}

	code, _, stderr := cliRun(t, runningHost(t, "agent-01"), stateDir, "update", "agent-01", "--all")
	if code != ExitUsage {
		t.Errorf("naming a VM alongside --all: exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--all takes no VM names") {
		t.Errorf("the usage error should say why:\n%s", stderr)
	}
}

func TestUpdate_DryRunPrintsTheGuestCommandsAndRunsNone(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, stdout, stderr := cliRun(t, fake, stateDir, "--dry-run", "update", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "apt-get update") {
		t.Errorf("the plan does not name the commands it would run:\n%s", stdout)
	}
	if len(sshArgvs(fake)) != 0 {
		t.Errorf("a dry run must not touch the guest:\n%s", fake)
	}
}

func TestUpdate_UpdatesTheToolingTheDistroDoesNotPackage(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	guest := sshArgvs(fake)
	want := []string{
		// self-update writes a tempfile directly into this directory and does
		// not create it, so the directory has to exist before that step.
		// A bare 0 left in /etc/environment makes the next mise self-update refuse
		// to start. The file is created if an older image never had it, then
		// rewritten only when the line is exactly that bare 0.
		"sudo -n touch -- /etc/environment",
		"sudo -n sed -i -e s/^MISE_MINIMUM_RELEASE_AGE=0$/MISE_MINIMUM_RELEASE_AGE=0s/ -- /etc/environment",
		"sudo -n mkdir -p -- /root/.cache/mise-tmp",
		"sudo -n env HOME=/root TMPDIR=/root/.cache/mise-tmp MISE_MINIMUM_RELEASE_AGE=0s mise self-update --yes",
		"sudo -n env HOME=/root TMPDIR=/root/.cache/mise-tmp MISE_MINIMUM_RELEASE_AGE=0s mise upgrade --yes",
		// Root's upgrade owns the selector symlinks it rewrites. The guest
		// user's upgrade removes one it needs to retarget, and a sticky store
		// refuses that with EPERM unless the link is theirs.
		"sudo -n find /usr/local/lib/mise/installs -mindepth 2 -maxdepth 2 -type l -exec chown -h agent {} +",
		// The guest user's own mise data directory, so not through sudo: root's
		// copy and this account's copy are separate installations. The release
		// age is named here too: an image built before /etc/environment carried
		// it would otherwise skip a release for a day.
		"env MISE_MINIMUM_RELEASE_AGE=0s mise upgrade --yes",
		"sudo -n env CODEX_HOME=/usr/local/lib/codex codex update",
		"sudo -n env RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo rustup update",
	}
	for _, step := range want {
		if !contains(guest, step) {
			t.Errorf("the guest was never asked to run %q:\n%s", step, fake)
		}
	}
}

func TestUpdate_SkipsToolingTheGuestDoesNotHave(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	// A base image built before codex was part of it: every other probe answers
	// as usual, so only codex's steps are dropped.
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "ssh" && contains(c.Args, "command") && contains(c.Args, "codex") {
			return hostexec.FakeResponse{ExitCode: 1}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code != ExitOK {
		t.Fatalf("a guest without codex is not a failed update: exit %d: %s", code, stderr)
	}
	for _, step := range sshArgvs(fake) {
		if strings.Contains(step, "codex update") {
			t.Errorf("codex was updated in a guest that does not have it: %q", step)
		}
	}
	if !strings.Contains(stderr, "no codex in this guest") {
		t.Errorf("a skipped step should say why:\n%s", stderr)
	}
	if !strings.Contains(stdout, "updated") {
		t.Errorf("the rest of the update still counts as one:\n%s", stdout)
	}
	// The tools that are there are still updated.
	if !contains(sshArgvs(fake), "sudo -n env HOME=/root TMPDIR=/root/.cache/mise-tmp MISE_MINIMUM_RELEASE_AGE=0s mise self-update --yes") {
		t.Errorf("one missing tool must not stop the others being updated:\n%s", fake)
	}
}

func TestUpdate_ReportsAConnectionThatDropsDuringAProbe(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	// 255 is ssh's own failure, not a guest answering "no such command": it
	// must not be read as a tool this image does not carry.
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "ssh" && contains(c.Args, "command") {
			return hostexec.FakeResponse{ExitCode: 255, Stderr: "Connection closed by 192.168.122.3\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, _, stderr := cliRun(t, fake, stateDir, "update", "agent-01")
	if code == ExitOK {
		t.Errorf("a dropped connection was reported as a successful update:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Connection closed") {
		t.Errorf("the failure should carry ssh's own diagnosis:\n%s", stderr)
	}
}
