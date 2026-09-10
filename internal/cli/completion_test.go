package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// completeLines runs the hidden completion helper and returns its candidates.
func completeLines(t *testing.T, stateDir string, words ...string) []string {
	t.Helper()
	args := append([]string{completeCommandName}, words...)

	code, stdout, stderr := cliRun(t, createHost(t), stateDir, args...)
	if code != ExitOK {
		t.Fatalf("%s exited %d: %s", completeCommandName, code, stderr)
	}
	if stdout == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(stdout, "\n"), "\n")
}

func TestComplete_OffersCommandNamesForTheFirstWord(t *testing.T) {
	got := completeLines(t, t.TempDir(), "")

	for _, want := range []string{"create", "destroy", "image", "ssh"} {
		if !contains(got, want) {
			t.Errorf("candidates do not include %q: %v", want, got)
		}
	}
}

func TestComplete_FiltersCommandNamesByWhatIsTyped(t *testing.T) {
	got := completeLines(t, t.TempDir(), "de")

	if len(got) != 1 || got[0] != "destroy" {
		t.Errorf("candidates for \"de\" = %v, want [destroy]", got)
	}
}

func TestComplete_DoesNotOfferTheHiddenHelperItself(t *testing.T) {
	got := completeLines(t, t.TempDir(), "")

	if contains(got, completeCommandName) {
		t.Errorf("the shell-only helper is offered to operators: %v", got)
	}
}

func TestComplete_OffersRecordedVMNames(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	for _, command := range []string{"ssh", "info", "start", "stop", "restart", "destroy", "console"} {
		got := completeLines(t, stateDir, command, "")
		if !contains(got, "agent-01") {
			t.Errorf("%s does not complete the recorded VM: %v", command, got)
		}
	}
}

func TestComplete_OffersNoVMNameOnceOneIsGiven(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	if got := completeLines(t, stateDir, "ssh", "agent-01", ""); len(got) != 0 {
		t.Errorf("a second VM name was offered: %v", got)
	}
}

func TestComplete_OffersACommandsOwnFlags(t *testing.T) {
	got := completeLines(t, t.TempDir(), "stop", "-")

	want := []string{"--force", "--timeout"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("stop flags = %v, want %v", got, want)
	}
}

func TestComplete_OffersGlobalFlagsBeforeACommand(t *testing.T) {
	got := completeLines(t, t.TempDir(), "--dry")

	if len(got) != 1 || got[0] != "--dry-run" {
		t.Errorf("global flag candidates = %v, want [--dry-run]", got)
	}
}

func TestComplete_OffersTheValuesOfAClosedFlag(t *testing.T) {
	cases := []struct {
		words []string
		want  []string
	}{
		{[]string{"create", "web", "--network", ""}, []string{"bridge", "nat"}},
		{[]string{"--output", ""}, []string{"json", "text"}},
		{[]string{"image", "build", ""}, []string{"arch", "arch-nix", "arch-slim", "fedora", "fedora-nix", "fedora-slim", "ubuntu", "ubuntu-nix", "ubuntu-slim"}},
		{[]string{"completion", ""}, []string{"bash", "fish", "zsh"}},
	}
	for _, testCase := range cases {
		got := completeLines(t, t.TempDir(), testCase.words...)
		if strings.Join(got, " ") != strings.Join(testCase.want, " ") {
			t.Errorf("candidates for %v = %v, want %v", testCase.words, got, testCase.want)
		}
	}
}

func TestComplete_OffersImageSubcommands(t *testing.T) {
	got := completeLines(t, t.TempDir(), "image", "")

	want := []string{"build", "inspect", "list", "rm"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("image subcommands = %v, want %v", got, want)
	}
}

func TestComplete_OffersNothingAfterTheGuestCommandSeparator(t *testing.T) {
	stateDir, _ := createdVM(t, "agent-01")

	// Everything after `--` runs in the guest, so completing it with host
	// candidates would be actively misleading.
	if got := completeLines(t, stateDir, "ssh", "agent-01", "--", ""); len(got) != 0 {
		t.Errorf("candidates were offered for the guest command: %v", got)
	}
}

func TestComplete_AnUnknownCommandLineSucceedsWithNoCandidates(t *testing.T) {
	// A completion helper that failed would print its error where the operator
	// is typing.
	if got := completeLines(t, t.TempDir(), "teleport", ""); len(got) != 0 {
		t.Errorf("candidates for an unknown command: %v", got)
	}
}

func TestComplete_UsesTheStateDirectoryWithoutCreatingIt(t *testing.T) {
	stateDir := t.TempDir() + "/absent"

	if got := completeLines(t, stateDir, "ssh", ""); len(got) != 0 {
		t.Errorf("candidates from a state directory that does not exist: %v", got)
	}
	if _, err := os.Stat(stateDir); err == nil {
		t.Errorf("completing a command line created %s", stateDir)
	}
}

func TestCompletion_PrintsAScriptForEachSupportedShell(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		code, stdout, stderr := cliRun(t, createHost(t), t.TempDir(), "completion", shell)
		if code != ExitOK {
			t.Fatalf("completion %s exited %d: %s", shell, code, stderr)
		}
		if !strings.Contains(stdout, completeCommandName) {
			t.Errorf("the %s script does not call the completion helper:\n%s", shell, stdout)
		}
	}
}

func TestCompletion_RejectsAnUnsupportedShell(t *testing.T) {
	code, _, stderr := cliRun(t, createHost(t), t.TempDir(), "completion", "csh")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "bash") {
		t.Errorf("the error does not name the shells that work:\n%s", stderr)
	}
}

func TestCompletion_RequiresExactlyOneShell(t *testing.T) {
	if code, _, _ := cliRun(t, createHost(t), t.TempDir(), "completion"); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

// TestCompletion_DocumentsTheHiddenHelper covers what the contract test in
// cli_test.go cannot: the shell-facing helper has no heading of its own, so
// this is what keeps it from being undocumented.
func TestCompletion_DocumentsTheHiddenHelper(t *testing.T) {
	contract, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatalf("reading the CLI contract: %v", err)
	}
	if !strings.Contains(string(contract), completeCommandName) {
		t.Errorf("docs/cli.md does not describe `agent-vm %s`", completeCommandName)
	}
}

// helpFlagPattern matches the flag names in a command's usage listing, which
// spells them the way the tool accepts them: with two dashes.
var helpFlagPattern = regexp.MustCompile(`(?m)^\s+--([a-z][a-z0-9-]*)`)

// TestCompletionSpecs_MatchTheFlagsCommandsRegister is the guard that keeps the
// completion table honest: it asks each command for its own help and compares
// the flags it prints with the ones completion offers. A flag added without a
// completion entry fails here rather than being silently uncompletable.
func TestCompletionSpecs_MatchTheFlagsCommandsRegister(t *testing.T) {
	for name, spec := range completionSpecs() {
		checkSpecFlags(t, []string{name}, spec)
	}
}

func checkSpecFlags(t *testing.T, path []string, spec *completionSpec) {
	t.Helper()

	for name, sub := range spec.subcommands {
		checkSpecFlags(t, append(append([]string{}, path...), name), sub)
	}

	args := append(append([]string{}, path...), "--help")
	_, stdout, stderr := cliRun(t, createHost(t), t.TempDir(), args...)

	registered := []string{}
	for _, match := range helpFlagPattern.FindAllStringSubmatch(stdout+stderr, -1) {
		registered = append(registered, match[1])
	}
	sort.Strings(registered)

	offered := append([]string{}, spec.flags...)
	sort.Strings(offered)

	if strings.Join(registered, " ") != strings.Join(offered, " ") {
		t.Errorf("%s registers flags %v but completion offers %v",
			strings.Join(path, " "), registered, offered)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
