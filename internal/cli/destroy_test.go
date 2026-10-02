package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// ownedBy answers `virsh domblklist` with the VM's own overlay, which is what
// proves to destroy that the domain is the one this tool created.
func ownedBy(fake *hostexec.Fake, stateDir, name string) *hostexec.Fake {
	overlay := filepath.Join(stateDir, "vms", name, state.OverlayFile)
	fake.RespondPrefix("virsh --connect qemu:///system domblklist", hostexec.FakeResponse{
		Stdout: " Target   Source\n-----------------------------\n vda      " + overlay + "\n\n",
	})
	return fake
}

// cliRunStdin is cliRun with a terminal to answer the confirmation on.
func cliRunStdin(t *testing.T, fake *hostexec.Fake, stateDir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer

	app := &App{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(stdin),
		Env:    func(string) string { return "" },
		Runner: fake,
	}
	full := append([]string{"--state-dir", stateDir, "--config", t.TempDir() + "/absent.toml"}, args...)

	code := app.Main(context.Background(), full)
	return code, stdout.String(), stderr.String()
}

func vmDirExists(t *testing.T, stateDir, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(stateDir, "vms", name))
	return err == nil
}

func TestDestroy_PowersOffUndefinesAndRemovesTheState(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("virsh --connect qemu:///system destroy agent-01") {
		t.Errorf("--force did not power the domain off:\n%s", fake)
	}
	if !fake.Ran("virsh --connect qemu:///system undefine agent-01") {
		t.Errorf("the domain was not undefined:\n%s", fake)
	}
	// The overlay is deleted by removing the directory it lives in, never by
	// handing libvirt --remove-all-storage.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "--remove-all-storage") {
			t.Errorf("libvirt must not be asked to delete storage: %v", argv)
		}
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("the VM's state directory is still there")
	}
}

func TestDestroy_AsksTheGuestBeforeDeletingItsDisk(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(createHost(t), stateDir, "agent-01")
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

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argvs := fake.Argvs()
	shutdownAt, undefineAt := -1, -1
	for i, argv := range argvs {
		switch {
		case strings.Contains(argv, "shutdown agent-01"):
			shutdownAt = i
		case strings.Contains(argv, "undefine agent-01"):
			undefineAt = i
		}
	}
	if shutdownAt < 0 || undefineAt < 0 || shutdownAt > undefineAt {
		t.Errorf("destroy must ask the guest to shut down before undefining it:\n%s", fake)
	}
	// Without --force the guest is asked, not killed: destroy is about to delete
	// the disk, so this is the last moment unwritten data can be saved.
	if fake.Ran("virsh --connect qemu:///system destroy agent-01") {
		t.Errorf("a graceful destroy must not power the guest off:\n%s", fake)
	}
}

func TestDestroy_ReportsAGuestThatIgnoredTheShutdown(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	// The guest keeps running however long it is given.
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--timeout", "1ns")
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d: %s", code, ExitTimeout, stderr)
	}
	// Nothing has been removed, so the operator still has both the VM and the
	// choice to force it.
	if fake.Ran("virsh --connect qemu:///system undefine agent-01") {
		t.Errorf("a guest that would not stop must not be undefined:\n%s", fake)
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("the VM's state directory was removed after a failed shutdown")
	}
}

func TestDestroy_RefusesADomainItDidNotCreate(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	// A domain with this name exists, but its disk is someone else's.
	fake.RespondPrefix("virsh --connect qemu:///system domblklist", hostexec.FakeResponse{
		Stdout: readToolout(t, "virsh-domblklist.txt"),
	})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--force")
	if code != ExitConflict {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitConflict, stderr)
	}
	if !strings.Contains(stderr, "/guest/diskimage1") {
		t.Errorf("the refusal should name the disks it found instead:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") || strings.Contains(argv, " destroy ") {
			t.Errorf("a domain this tool did not create must not be touched: %v", argv)
		}
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("nothing may be deleted when ownership could not be confirmed")
	}
}

func TestDestroy_RefusesAVMThatIsNotRecordedHere(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01", "someone-elses-vm"), stateDir, "agent-01")

	code, _, _ := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "someone-elses-vm", "--force")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
	// A name with no record here is answered from state alone: libvirt is never
	// even asked about it.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "someone-elses-vm") {
			t.Errorf("an unrecorded VM must not reach libvirt: %v", argv)
		}
	}
}

func TestDestroy_KeepDiskRemovesOnlyTheDomain(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	code, stdout, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01", "--keep-disk", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !fake.Ran("virsh --connect qemu:///system undefine agent-01") {
		t.Errorf("the domain was not undefined:\n%s", fake)
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("--keep-disk must leave the state directory in place")
	}
	if !strings.Contains(stdout, filepath.Join(stateDir, "vms", "agent-01")) {
		t.Errorf("the output should say where the disk was kept:\n%s", stdout)
	}
}

func TestDestroy_RemovesTheLeftoverStateOfALostDomain(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	// libvirt no longer knows the domain — it was undefined by hand, or a
	// previous destroy stopped after undefining it.
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "\n"})

	code, _, stderr := cliRunStdin(t, fake, stateDir, "", "--yes", "destroy", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("the leftover state directory was not removed")
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") {
			t.Errorf("there is no domain to undefine: %v", argv)
		}
	}
}

func TestDestroy_WithoutATerminalRefusesInsteadOfAssumingConsent(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	// cliRun leaves Stdin nil, which is a non-interactive run.
	code, _, stderr := cliRun(t, fake, stateDir, "destroy", "agent-01")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal should name the flag that grants consent:\n%s", stderr)
	}
	if vmDirExists(t, stateDir, "agent-01") == false {
		t.Errorf("an unconfirmed destroy must change nothing")
	}
}

func TestDestroy_ANegativeAnswerChangesNothing(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	code, _, stderr := cliRunStdin(t, fake, stateDir, "n\n", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	// The prompt has to say what is about to be removed, not just ask.
	if !strings.Contains(stderr, filepath.Join(stateDir, "vms", "agent-01")) {
		t.Errorf("the prompt should name what would be deleted:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") || strings.Contains(argv, " destroy ") {
			t.Errorf("a declined destroy must change nothing: %v", argv)
		}
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("a declined destroy deleted the state directory")
	}
}

func TestDestroy_DryRunPrintsThePlanAndChangesNothing(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01")

	code, stdout, stderr := cliRunStdin(t, fake, stateDir, "", "--dry-run", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, want := range []string{"undefine agent-01", filepath.Join(stateDir, "vms", "agent-01")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, stdout)
		}
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") || strings.Contains(argv, " destroy ") {
			t.Errorf("--dry-run must not run the destructive commands: %v", argv)
		}
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Errorf("--dry-run removed the state directory")
	}
}

func TestDestroy_RejectsAMissingName(t *testing.T) {
	if code, _, _ := cliRun(t, createHost(t), t.TempDir(), "destroy"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}
