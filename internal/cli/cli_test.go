package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/network"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// run invokes the CLI with an isolated state directory and no environment, and
// returns its exit code with whatever it wrote.
func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer

	app := &App{Stdout: &out, Stderr: &errOut, Env: func(string) string { return "" }}
	args = append([]string{"--state-dir", t.TempDir(), "--config", t.TempDir() + "/absent.toml"}, args...)

	code = app.Main(context.Background(), args)
	return code, out.String(), errOut.String()
}

func TestRun_NoCommandIsAUsageError(t *testing.T) {
	code, _, stderr := run(t)

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr does not show usage:\n%s", stderr)
	}
}

func TestRun_UnknownCommandIsAUsageError(t *testing.T) {
	code, _, _ := run(t, "teleport")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestRun_UnknownFlagIsAUsageError(t *testing.T) {
	code, _, _ := run(t, "--wat", "doctor")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestRun_InvalidOutputFormatIsAUsageError(t *testing.T) {
	code, _, stderr := run(t, "--output", "yaml", "doctor")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "text or json") {
		t.Errorf("stderr does not name the valid formats:\n%s", stderr)
	}
}

func TestRun_InvalidResourceValueIsAUsageError(t *testing.T) {
	// Configuration is validated before anything on the host is touched.
	var out, errOut bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errOut, Env: func(key string) string {
		if key == "AGENT_VM_MEMORY" {
			return "not-a-size"
		}
		return ""
	}}

	err := app.run(context.Background(), []string{"--state-dir", t.TempDir(), "doctor"})

	if got := exitCodeFor(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d (error: %v)", got, ExitUsage, err)
	}
}

func TestRun_UnimplementedCommandSaysSoInsteadOfUnknownCommand(t *testing.T) {
	// docs/cli.md specifies these commands; this build does not have them yet.
	// An operator following the docs deserves an accurate answer.
	code, _, stderr := run(t, "create", "agent-01")

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "not implemented") {
		t.Errorf("stderr does not explain the command is unimplemented:\n%s", stderr)
	}
	if strings.Contains(stderr, "unknown command") {
		t.Errorf("a documented command was reported as unknown:\n%s", stderr)
	}
}

func TestRun_VersionReportsAgentVMAndHostTools(t *testing.T) {
	code, stdout, _ := run(t, "--version")

	if code != ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "agent-vm "+Version) {
		t.Errorf("stdout does not report the agent-vm version:\n%s", stdout)
	}
	// Every tool the project delegates to must be listed, present or not,
	// because "which versions am I running against?" is the first question
	// when a host tool changes under us.
	for _, tool := range hostexec.RequiredTools() {
		if !strings.Contains(stdout, tool.Name) {
			t.Errorf("stdout does not mention %s:\n%s", tool.Name, stdout)
		}
	}
}

func TestExitCodeFor_MapsErrorsToTheDocumentedCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"no error", nil, ExitOK},
		{"generic", errors.New("something broke"), ExitFailure},
		{"bad input", &config.ValidationError{Field: "vcpus", Value: "x", Err: errors.New("bad")}, ExitUsage},
		{"unknown schema", &state.SchemaError{File: "vm.json", Found: 99, Known: 1}, ExitUsage},
		{"missing tool", &hostexec.NotFoundError{Tool: "virt-install"}, ExitHostNotReady},
		{"tool too old", &hostexec.VersionError{Tool: "virsh"}, ExitHostNotReady},
		{"bridge not ready", &network.BridgeError{Interface: "br0"}, ExitHostNotReady},
		{"unknown VM", &state.NotFoundError{Kind: "VM", Name: "agent-01"}, ExitNotFound},
		{"already exists", &state.ExistsError{Kind: "VM", Name: "agent-01"}, ExitConflict},
		{"lock held", &state.BusyError{Resource: "VM agent-01"}, ExitConflict},
		{"tool timed out", &hostexec.TimeoutError{Tool: "virt-install"}, ExitTimeout},
		{"cleanup incomplete", &CleanupError{Operation: "create", Cause: errors.New("undefine failed")}, ExitCleanup},
		{"explicit code", &ExitError{Code: ExitTimeout, Err: errors.New("guest never came up")}, ExitTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestCleanupError_ListsWhatRemainsOnTheHost(t *testing.T) {
	// Reporting success while host state is left behind is forbidden; the
	// operator must be told exactly what to clean up.
	err := &CleanupError{
		Operation: "create agent-01",
		Cause:     errors.New("virsh undefine exited 1"),
		Remaining: []string{"libvirt domain agent-01", "/state/vms/agent-01"},
	}

	msg := err.Error()
	for _, want := range []string{"agent-01", "libvirt domain agent-01", "/state/vms/agent-01"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message does not mention %q:\n%s", want, msg)
		}
	}
}
