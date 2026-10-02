package cli

import (
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// stoppedHost answers as libvirt would for a VM that is defined and shut off.
func stoppedHost(t *testing.T, names ...string) *hostexec.Fake {
	t.Helper()
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: strings.Join(names, "\n") + "\n"})
	fake.RespondPrefix("virsh --connect qemu:///system domstate",
		hostexec.FakeResponse{Stdout: "shut off\n"})
	return fake
}

func TestStart_StartsAStoppedVM(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "start", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("virsh --connect qemu:///system start agent-01") {
		t.Errorf("start was not requested:\n%s", fake)
	}
}

func TestStart_RefusesAVMThatIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "start", "agent-01")
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(stderr, "already running") {
		t.Errorf("the refusal should say what state the VM is in:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, " start ") {
			t.Errorf("a running VM must not be started again: %v", argv)
		}
	}
}

func TestStart_ReportsAVMLibvirtHasLost(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "\n"})

	code, _, stderr := cliRun(t, fake, stateDir, "start", "agent-01")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
	// The record is not silently repaired by redefining someone's domain.
	if !strings.Contains(stderr, "destroy agent-01") {
		t.Errorf("the error should say how to clear the stale record:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "define") {
			t.Errorf("a lost domain must not be redefined: %v", argv)
		}
	}
}

func TestStart_ReportsAVMThatIsNotRecordedHere(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")

	code, _, _ := cliRun(t, runningHost(t, "agent-01"), stateDir, "start", "someone-elses-vm")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
}

func TestStop_AsksTheGuestAndWaitsForIt(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "agent-01\n"})
	// Running when asked, off by the time the wait looks again.
	states := []string{"running\n", "shut off\n", "shut off\n"}
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if len(c.Args) >= 3 && c.Args[2] == "domstate" {
			next := states[0]
			if len(states) > 1 {
				states = states[1:]
			}
			return hostexec.FakeResponse{Stdout: next}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, stderr := cliRun(t, fake, stateDir, "stop", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("virsh --connect qemu:///system shutdown agent-01") {
		t.Errorf("a graceful shutdown was not requested:\n%s", fake)
	}
	if !strings.Contains(stdout, "shut off") {
		t.Errorf("the final state is not reported:\n%s", stdout)
	}
}

func TestStop_NeverEscalatesToAForceOffOnItsOwn(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	// The guest ignores the request and keeps running.
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "stop", "agent-01", "--timeout", "1ns")
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d", code, ExitTimeout)
	}
	// Powering a guest off loses whatever it had not written, so it stays the
	// operator's decision — the error says so rather than doing it.
	if !strings.Contains(stderr, "--force") {
		t.Errorf("the timeout should name the operator's next move:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, " destroy ") {
			t.Errorf("a graceful stop must never escalate by itself: %v", argv)
		}
	}
}

func TestStop_ForcePowersOffImmediately(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "stop", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("virsh --connect qemu:///system destroy agent-01") {
		t.Errorf("--force did not power the domain off:\n%s", fake)
	}
	// A forced stop does not ask the guest first; that is the whole point.
	if fake.Ran("virsh --connect qemu:///system shutdown agent-01") {
		t.Errorf("--force should not also request a graceful shutdown:\n%s", fake)
	}
}

func TestStop_RefusesAVMThatIsNotRunning(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := stoppedHost(t, "agent-01")

	code, _, stderr := cliRun(t, fake, stateDir, "stop", "agent-01")
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(stderr, "shut off") {
		t.Errorf("the refusal should name the state it found:\n%s", stderr)
	}
}

func TestRestart_StopsThenStarts(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "agent-01\n"})
	// Running, then off after the shutdown, then running again after the start.
	states := []string{"running\n", "shut off\n", "shut off\n", "running\n"}
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if len(c.Args) >= 3 && c.Args[2] == "domstate" {
			next := states[0]
			if len(states) > 1 {
				states = states[1:]
			}
			return hostexec.FakeResponse{Stdout: next}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, _, stderr := cliRun(t, fake, stateDir, "restart", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argvs := fake.Argvs()
	shutdownAt, startAt := -1, -1
	for i, argv := range argvs {
		switch {
		case strings.Contains(argv, "shutdown agent-01"):
			shutdownAt = i
		case strings.Contains(argv, " start agent-01"):
			startAt = i
		}
	}
	if shutdownAt < 0 || startAt < 0 || shutdownAt > startAt {
		t.Errorf("restart must shut down and then start, got:\n%s", fake)
	}
}

func TestRestart_DoesNotStartAGuestItCouldNotStop(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, _, _ := cliRun(t, fake, stateDir, "restart", "agent-01", "--timeout", "1ns")
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d", code, ExitTimeout)
	}
	// The VM is left exactly as it was, for the operator to decide about.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, " start agent-01") || strings.Contains(argv, " destroy ") {
			t.Errorf("a restart that could not stop the guest must change nothing else: %v", argv)
		}
	}
}

func TestLifecycle_RejectsAMissingName(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"start", "stop", "restart"} {
		if code, _, _ := cliRun(t, createHost(t), t.TempDir(), verb); code != ExitUsage {
			t.Errorf("%s with no name: exit code = %d, want %d", verb, code, ExitUsage)
		}
	}
}

func TestLifecycle_AcceptsFlagsAfterTheName(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	// docs/cli.md spells it `agent-vm stop <name> [--force]`, so that has to work.
	code, _, stderr := cliRun(t, fake, stateDir, "stop", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
}

func TestStop_DryRunPrintsTheCommandAndWaitsForNothing(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	code, stdout, stderr := cliRun(t, fake, stateDir, "--dry-run", "stop", "agent-01", "--timeout", "1h")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "shutdown agent-01") {
		t.Errorf("the plan should print the shutdown it would run:\n%s", stdout)
	}
	// Nothing was asked to stop, so there is nothing to wait for — a dry run
	// must not sit for the timeout waiting on a guest that was never told.
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "virsh --connect qemu:///system shutdown") {
			t.Errorf("--dry-run must not run the shutdown: %v", argv)
		}
	}
}
