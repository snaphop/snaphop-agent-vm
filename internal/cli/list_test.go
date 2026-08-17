package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// createdVM runs a real create against a fake host, so the tests below read
// exactly what create writes rather than a hand-built record.
func createdVM(t *testing.T, name string) (stateDir string, fake *hostexec.Fake) {
	t.Helper()
	stateDir, keyPath := createEnv(t)
	fake = createHost(t)

	if code, _, stderr := cliRun(t, fake, stateDir, "create", name, "--ssh-key", keyPath); code != ExitOK {
		t.Fatalf("create %s failed with %d: %s", name, code, stderr)
	}
	return stateDir, fake
}

// runningHost answers as libvirt would for a VM that is defined and up.
func runningHost(t *testing.T, names ...string) *hostexec.Fake {
	t.Helper()
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: strings.Join(names, "\n") + "\n"})
	fake.RespondPrefix("virsh --connect qemu:///system domstate",
		hostexec.FakeResponse{Stdout: "running\n"})
	return fake
}

func TestList_ReportsStateAndAddressForARecordedVM(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	code, stdout, stderr := cliRun(t, runningHost(t, "agent-01"), stateDir, "list")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, want := range []string{"agent-01", "running", "ubuntu:24.04", "nat", "192.168.122.3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestList_AVMLibvirtHasLostIsReportedAsMissing(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	// libvirt no longer knows the domain — someone undefined it by hand.
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "\n"})

	code, stdout, stderr := cliRun(t, fake, stateDir, "list")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "missing") {
		t.Errorf("a VM libvirt has lost must be reported as missing:\n%s", stdout)
	}
}

func TestList_DoesNotListDomainsThisToolDidNotCreate(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01", "someone-elses-vm")

	code, stdout, stderr := cliRun(t, fake, stateDir, "list")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	// A domain with no record here belongs to someone else. Listing it would
	// invite acting on it.
	if strings.Contains(stdout, "someone-elses-vm") {
		t.Errorf("list showed a domain this tool did not create:\n%s", stdout)
	}
}

func TestList_AnEmptyStateDirectoryListsNothingAndSucceeds(t *testing.T) {
	code, stdout, stderr := cliRun(t, createHost(t), t.TempDir(), "list")

	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "No VMs") {
		t.Errorf("an empty state directory should say so:\n%s", stdout)
	}
}

func TestList_StoppedVMsHaveNoAddress(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "agent-01\n"})
	fake.RespondPrefix("virsh --connect qemu:///system domstate",
		hostexec.FakeResponse{Stdout: "shut off\n"})

	code, stdout, stderr := cliRun(t, fake, stateDir, "list")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "192.168.122.3") {
		t.Errorf("a stopped VM has no address to report:\n%s", stdout)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "domifaddr") {
			t.Errorf("a stopped VM should not be asked for an address: %v", argv)
		}
	}
}

func TestList_QueriesLibvirtOncePerConnection(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	for _, name := range []string{"agent-01", "agent-02"} {
		if code, _, stderr := cliRun(t, createHost(t), stateDir, "create", name, "--ssh-key", keyPath); code != ExitOK {
			t.Fatalf("create %s failed with %d: %s", name, code, stderr)
		}
	}

	fake := runningHost(t, "agent-01", "agent-02")
	if code, _, stderr := cliRun(t, fake, stateDir, "list"); code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	// Enumerating domains once per VM would make a large state directory
	// needlessly slow without telling the operator anything more.
	listings := 0
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "list --all --name") {
			listings++
		}
	}
	if listings != 1 {
		t.Errorf("libvirt was enumerated %d times for 2 VMs, want 1:\n%s", listings, fake)
	}
}

func TestList_JSONOutputCarriesTheStoredRecordPlusLiveState(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr, Env: func(string) string { return "" }, Runner: runningHost(t, "agent-01")}
	code := app.Main(context.Background(), []string{
		"--state-dir", stateDir, "--config", t.TempDir() + "/absent.toml",
		"--output", "json", "list",
	})
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr.String())
	}

	var listed []struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Address   string `json:"address"`
		BaseImage struct {
			SourceDigest string `json:"sourceDigest"`
		} `json:"baseImage"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout.String())
	}
	if len(listed) != 1 {
		t.Fatalf("got %d records, want 1", len(listed))
	}
	if listed[0].Name != "agent-01" || listed[0].State != "running" || listed[0].Address == "" {
		t.Errorf("record = %+v, want the stored fields plus live state", listed[0])
	}
	if listed[0].BaseImage.SourceDigest == "" {
		t.Error("the JSON form should carry the same fields as vm.json")
	}
}

func TestList_RejectsAnArgument(t *testing.T) {
	if code, _, _ := cliRun(t, createHost(t), t.TempDir(), "list", "agent-01"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}
