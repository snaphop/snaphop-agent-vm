package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

func TestInfo_PrintsTheProvenanceOfAVM(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.RespondPrefix("qemu-img info --output=json", hostexec.FakeResponse{
		Stdout: readToolout(t, "qemu-img-info-json-overlay.json"),
	})

	code, stdout, stderr := cliRun(t, fake, stateDir, "info", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	// info answers "what is this VM, and what made it?" from the state
	// directory: the digest that booted, the disk, and the exact invocation.
	for _, want := range []string{
		"agent-01",
		"running",
		"sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
		"aa:bb:cc:dd:ee:ff",
		"root.qcow2",
		"domain.xml",
		"virt-install --connect qemu:///system --name agent-01",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("info output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestInfo_ReportsWhatTheOverlayActuallyCosts(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.RespondPrefix("qemu-img info --output=json", hostexec.FakeResponse{
		Stdout: readToolout(t, "qemu-img-info-json-overlay.json"),
	})

	code, stdout, stderr := cliRun(t, fake, stateDir, "info", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	// A thin 50G overlay costs almost nothing on the host, and the difference
	// is the point: both numbers are reported.
	if !strings.Contains(stdout, "disk (virtual)") || !strings.Contains(stdout, "disk (on host)") {
		t.Errorf("info should report both disk sizes:\n%s", stdout)
	}
	if !strings.Contains(stdout, "50.0G") {
		t.Errorf("the virtual size is not reported:\n%s", stdout)
	}
}

func TestInfo_AnUnreadableOverlayStillPrintsTheRecord(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.RespondPrefix("qemu-img info", hostexec.FakeResponse{
		ExitCode: 1, Stderr: "qemu-img: Could not open 'root.qcow2': No such file or directory",
	})

	code, stdout, stderr := cliRun(t, fake, stateDir, "info", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "agent-01") {
		t.Errorf("the stored record should still be printed:\n%s", stdout)
	}
}

func TestInfo_ReportsAMissingVMAsNotFound(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	code, _, stderr := cliRun(t, runningHost(t, "agent-01"), stateDir, "info", "agent-99")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
	if !strings.Contains(stderr, "agent-99") {
		t.Errorf("the error should name the VM asked for:\n%s", stderr)
	}
}

func TestInfo_RejectsAnInvalidVMName(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	// A name that is not a valid VM name is refused before it is used as a
	// path segment.
	if code, _, _ := cliRun(t, createHost(t), stateDir, "info", "../../etc"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestInfo_RequiresExactlyOneName(t *testing.T) {
	if code, _, _ := cliRun(t, createHost(t), t.TempDir(), "info"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestInfo_JSONOutputIsTheRecordWithLiveState(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")
	fake.RespondPrefix("qemu-img info --output=json", hostexec.FakeResponse{
		Stdout: readToolout(t, "qemu-img-info-json-overlay.json"),
	})

	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr, Env: func(string) string { return "" }, Runner: fake}
	code := app.Main(context.Background(), []string{
		"--state-dir", stateDir, "--config", t.TempDir() + "/absent.toml",
		"--output", "json", "info", "agent-01",
	})
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr.String())
	}

	var detail struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		CreatedBy struct {
			VirtInstallArgv []string `json:"virtInstallArgv"`
		} `json:"createdBy"`
		Disk struct {
			VirtualSize string `json:"virtualSize"`
			ActualSize  string `json:"actualSize"`
			BackingFile string `json:"backingFile"`
		} `json:"disk"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &detail); err != nil {
		t.Fatalf("stdout is not the VM detail: %v\n%s", err, stdout.String())
	}
	if detail.Name != "agent-01" || detail.State != "running" {
		t.Errorf("detail = %+v, want the record with live state", detail)
	}
	if len(detail.CreatedBy.VirtInstallArgv) == 0 {
		t.Error("the JSON form should carry the recorded virt-install argv")
	}
	if detail.Disk.BackingFile == "" {
		t.Error("the JSON form should report the overlay's backing file")
	}
}
