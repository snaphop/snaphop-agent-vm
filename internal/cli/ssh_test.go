package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// becameArgv returns the single command this process was asked to be replaced
// by, failing the test if it was asked for anything else.
func becameArgv(t *testing.T, fake *hostexec.Fake) string {
	t.Helper()
	became := fake.Became()
	if len(became) != 1 {
		t.Fatalf("got %d exec'd commands, want 1:\n%s", len(became), fake)
	}
	return strings.Join(became[0].Argv(), " ")
}

func TestSSH_ExecsSSHToTheGuestAsTheGuestUser(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argv := becameArgv(t, fake)
	if !strings.HasPrefix(argv, "ssh ") {
		t.Errorf("did not exec ssh: %s", argv)
	}
	if !strings.Contains(argv, "agent@192.168.122.3") {
		t.Errorf("ssh was not pointed at the guest user and address: %s", argv)
	}
	// An interactive session must be able to prompt — for a passphrase, or to
	// answer sudo — so BatchMode is not forced on it.
	if strings.Contains(argv, "BatchMode=yes") {
		t.Errorf("an interactive session must not run in batch mode: %s", argv)
	}
}

func TestSSH_ConnectsToTheAddressLibvirtLeasedNotOneTheGuestReports(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	// The guest agent runs as root in the untrusted guest. Pointed at the
	// hypervisor, it would send the operator's session — and keys — there.
	fake.RespondPrefix("virsh --connect qemu:///system domifaddr agent-01 --source agent", hostexec.FakeResponse{
		Stdout: readToolout(t, "virsh-domifaddr-source-agent.txt"),
	})

	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if argv := becameArgv(t, fake); !strings.Contains(argv, "agent@192.168.122.3") {
		t.Errorf("ssh was not pointed at the leased address: %s", argv)
	}
	if fake.Ran("virsh --connect qemu:///system domifaddr agent-01 --source agent") {
		t.Error("a NAT guest's address must not be taken from its guest agent")
	}
}

func TestSSH_RunsACommandInTheGuest(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01", "--", "ls", "-la", "/tmp")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argv := becameArgv(t, fake)
	if !strings.HasSuffix(argv, "agent@192.168.122.3 ls -la /tmp") {
		t.Errorf("the guest command was not passed through: %s", argv)
	}
	// A scripted run must fail rather than stop at a prompt nobody will answer.
	if !strings.Contains(argv, "BatchMode=yes") {
		t.Errorf("a non-interactive run should be batch mode: %s", argv)
	}
}

func TestSSH_DoesNotReadOurOwnFlagsAfterTheSeparator(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	// `--output json` here belongs to the guest's command, not to agent-vm.
	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01", "--", "some-tool", "--output", "json")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.HasSuffix(becameArgv(t, fake), "some-tool --output json") {
		t.Errorf("arguments after -- must reach the guest untouched: %s", becameArgv(t, fake))
	}
}

func TestSSH_OffersThePrivateKeyThatMatchesTheAuthorizedOne(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	// The usual pair: id_ed25519.pub beside id_ed25519.
	private := strings.TrimSuffix(keyPath, ".pub")
	if err := os.WriteFile(private, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("writing the private key: %v", err)
	}
	if code, _, stderr := cliRun(t, createHost(t), stateDir, "create", "agent-01", "--ssh-key", keyPath); code != ExitOK {
		t.Fatalf("create failed with %d: %s", code, stderr)
	}

	fake := runningHost(t, "agent-01")
	if code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01"); code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argv := becameArgv(t, fake)
	if !strings.Contains(argv, "-i "+private) {
		t.Errorf("the matching private key was not offered: %s", argv)
	}
	// The key is located and named to ssh, never read by this tool.
	if strings.Contains(argv, "BEGIN OPENSSH PRIVATE KEY") {
		t.Errorf("private key material must never appear in an argument vector: %s", argv)
	}
}

func TestSSH_FallsBackToTheOperatorsDefaultKeys(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	// No private key sits beside the authorized public key, so ssh decides.
	if code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01"); code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if strings.Contains(becameArgv(t, fake), " -i ") {
		t.Errorf("no key should be named when none was found: %s", becameArgv(t, fake))
	}
}

func TestSSH_RefusesAVMThatIsNotRunning(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01")
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(stderr, "agent-vm start agent-01") {
		t.Errorf("the refusal should name the next move:\n%s", stderr)
	}
	if len(fake.Became()) != 0 {
		t.Errorf("nothing should be exec'd for a stopped VM:\n%s", fake)
	}
}

func TestSSH_ReportsAGuestWithNoAddressYet(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.RespondPrefix("virsh --connect qemu:///system domifaddr", hostexec.FakeResponse{
		Stdout: " Name       MAC address         Protocol   Address\n" +
			"-------------------------------------------------------------\n\n",
	})

	code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01")
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d", code, ExitTimeout)
	}
	if !strings.Contains(stderr, "console") {
		t.Errorf("the error should point at the console for a booting guest:\n%s", stderr)
	}
}

func TestSSH_ReportsAVMThatIsNotRecordedHere(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")

	code, _, _ := cliRun(t, runningHost(t, "agent-01"), stateDir, "ssh", "someone-elses-vm")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
}

func TestSSH_DryRunPrintsTheCommandInsteadOfRunningIt(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, stdout, stderr := cliRun(t, fake, stateDir, "--dry-run", "ssh", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	// The printed command is documented as one you can paste and run yourself.
	if !strings.Contains(stdout, "ssh ") || !strings.Contains(stdout, "agent@192.168.122.3") {
		t.Errorf("--dry-run should print the ssh command:\n%s", stdout)
	}
}

func TestConsole_ExecsVirshConsole(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "console", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if want := "virsh --connect qemu:///system console agent-01"; becameArgv(t, fake) != want {
		t.Errorf("exec'd %q, want %q", becameArgv(t, fake), want)
	}
}

func TestConsole_RefusesAStoppedVMAndNamesItsLog(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "console", "agent-01")
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	// A stopped guest has no console, but the log of its last boot is usually
	// what the operator actually wanted.
	if !strings.Contains(stderr, filepath.Join("vms", "agent-01", "console.log")) {
		t.Errorf("the refusal should name the console log:\n%s", stderr)
	}
}

func TestConsole_RequiresAName(t *testing.T) {
	t.Parallel()
	if code, _, _ := cliRun(t, createHost(t), t.TempDir(), "console"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}
