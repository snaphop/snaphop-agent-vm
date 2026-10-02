package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/network"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
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

func TestRun_EveryCommandInTheDocumentedContractExists(t *testing.T) {
	// docs/cli.md is the specification this package must satisfy, so a command
	// documented there and missing here is a bug in one of the two. Reading the
	// document is what keeps them from drifting apart quietly.
	contract, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatalf("reading docs/cli.md: %v", err)
	}

	// Headings name one command, or several sharing a description:
	//   ### `agent-vm info <name>`
	//   ### `agent-vm start <name>` / `stop <name>` / `restart <name>`
	heading := regexp.MustCompile("(?m)^### (`agent-vm .*)$")
	verb := regexp.MustCompile("`(?:agent-vm )?([a-z]+)")

	documented := map[string]bool{}
	for _, line := range heading.FindAllStringSubmatch(string(contract), -1) {
		for _, match := range verb.FindAllStringSubmatch(line[1], -1) {
			documented[match[1]] = true
		}
	}
	if len(documented) < 5 {
		t.Fatalf("found only %d documented commands; the heading pattern no longer matches docs/cli.md", len(documented))
	}

	implemented := commands()
	for name := range documented {
		if _, ok := implemented[name]; !ok {
			t.Errorf("docs/cli.md documents `agent-vm %s`, but it is not in the dispatch table", name)
		}
	}
	for name, cmd := range implemented {
		if cmd.hidden {
			// Hidden commands are interfaces for other programs — the shell
			// completion helper — and have no heading of their own. The
			// section that describes them is checked in completion_test.go.
			continue
		}
		if !documented[name] {
			t.Errorf("`agent-vm %s` is implemented but not documented in docs/cli.md", name)
		}
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

// Setting up a run reads configuration before any subcommand does — deciding
// which machine the hypervisor is on needs the libvirt URI — so a config
// resolved and cached at that point has none of the running command's flags in
// it. Handing that cache back is how every `create` flag came to be silently
// ignored: the VM was created, and at the default size whatever was asked for.
func TestConfigWith_ResolvesAgainAfterConfigHasAlreadyCachedOne(t *testing.T) {
	app := &App{Env: func(string) string { return "" }}

	if _, err := app.Config(); err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg, err := app.ConfigWith(config.Overrides{VCPUs: "8", Memory: "2G"})
	if err != nil {
		t.Fatalf("ConfigWith: %v", err)
	}

	if cfg.VCPUs != 8 {
		t.Errorf("VCPUs = %d, want the flag's 8", cfg.VCPUs)
	}
	if cfg.Memory != 2*config.GiB {
		t.Errorf("Memory = %v, want the flag's 2G", cfg.Memory)
	}
	// Later lookups see what the command actually ran with, not the earlier
	// flagless resolution.
	again, err := app.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if again.VCPUs != 8 {
		t.Errorf("Config() after ConfigWith gave VCPUs = %d, want 8", again.VCPUs)
	}
}

// A rejected flag must be named the way docs/cli.md spells it and the operator
// typed it. Go's flag package writes "-test", and writes it itself as well as
// through the error returned to us, so an unknown flag used to be reported
// twice in a spelling this tool does not accept.
func TestRun_UnknownFlagIsReportedOnceWithTwoDashes(t *testing.T) {
	code, _, stderr := run(t, "image", "build", "--test")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "not defined: --test") {
		t.Errorf("stderr does not spell the flag as --test:\n%s", stderr)
	}
	if strings.Contains(stderr, " -test") {
		t.Errorf("stderr still spells the flag with one dash:\n%s", stderr)
	}
	if got := strings.Count(stderr, "not defined"); got != 1 {
		t.Errorf("the message appears %d times, want 1:\n%s", got, stderr)
	}
	// The flags that would have worked belong to that command, so they are
	// listed with the error rather than left behind a second invocation.
	for _, want := range []string{
		"agent-vm image build <distro>[-slim|-nix][:<tag>] [flags]",
		"--force",
		"--platform <string>",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not list the command's flags (%q missing):\n%s", want, stderr)
		}
	}
}

// A wrong argument gets the same listing: the operator is told what the
// command takes, not only that what they typed was wrong.
func TestRun_UsageErrorListsTheCommandsFlags(t *testing.T) {
	code, _, stderr := run(t, "destroy")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	for _, want := range []string{
		"agent-vm destroy <name>",
		"--keep-disk",
		"--timeout <duration>",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not list the command's flags (%q missing):\n%s", want, stderr)
		}
	}
}

// A flag with a real default says so, the way the flag package's own listing
// did; one whose value this tool resolves from configuration does not, because
// its flag default is the empty string and "(default )" says nothing.
func TestCommandHelp_ShowsDefaultsOnlyWhereThereIsOne(t *testing.T) {
	_, _, stderr := run(t, "stop", "--help")

	if !strings.Contains(stderr, "(default 1m0s)") {
		t.Errorf("--timeout does not show its default:\n%s", stderr)
	}
	if strings.Contains(stderr, "(default )") {
		t.Errorf("a flag with no default still claims one:\n%s", stderr)
	}
}

// The same respelling applies to the flag package's other complaints.
func TestRun_FlagValueErrorsAreSpelledWithTwoDashes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a missing value", []string{"stop", "agent-01", "--timeout"}, "flag needs an argument: --timeout"},
		{"an unparsable value", []string{"stop", "agent-01", "--timeout=soon"}, `invalid value "soon" for flag --timeout`},
		{"a global flag", []string{"--output"}, "flag needs an argument: --output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := run(t, tc.args...)

			if code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr does not carry %q:\n%s", tc.want, stderr)
			}
		})
	}
}

// A quoted value keeps whatever dashes it was given: only the flag's own name
// is respelled.
func TestRespellFlags_LeavesAValueAlone(t *testing.T) {
	got := respellFlags(errors.New(`invalid value "-1s" for flag -timeout: parse error`))

	if want := `invalid value "-1s" for flag --timeout: parse error`; got.Error() != want {
		t.Errorf("respellFlags = %q, want %q", got.Error(), want)
	}
}

// --help is an answer, not a failure: it prints the command's documented
// invocation and its own flags, and exits 0.
func TestRun_CommandHelpPrintsThatCommandsFlagsAndExitsZero(t *testing.T) {
	code, _, stderr := run(t, "image", "build", "--help")

	if code != ExitOK {
		t.Errorf("exit code = %d, want %d:\n%s", code, ExitOK, stderr)
	}
	for _, want := range []string{
		"agent-vm image build <distro>[-slim|-nix][:<tag>] [flags]",
		"--force",
		"--platform <string>",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("help does not carry %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "Usage of image build") {
		t.Errorf("help still uses the flag package's own heading:\n%s", stderr)
	}
	if strings.Contains(stderr, "agent-vm: ") {
		t.Errorf("asking for help was reported as an error:\n%s", stderr)
	}
}

// Help works after the positional argument too, the way the flags themselves
// do (parseNamed).
func TestRun_CommandHelpWorksAfterTheName(t *testing.T) {
	code, _, stderr := run(t, "create", "agent-01", "--help")

	if code != ExitOK {
		t.Errorf("exit code = %d, want %d:\n%s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "agent-vm create <name> [flags]") {
		t.Errorf("help does not show the command's usage:\n%s", stderr)
	}
}

func TestRun_GlobalHelpPrintsUsageAndExitsZero(t *testing.T) {
	code, _, stderr := run(t, "--help")

	if code != ExitOK {
		t.Errorf("exit code = %d, want %d:\n%s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "Commands:") || !strings.Contains(stderr, "--dry-run") {
		t.Errorf("stderr is not the usage message:\n%s", stderr)
	}
}
