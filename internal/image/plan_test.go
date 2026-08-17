package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/golden"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

func TestPlan_MatchesTheInvocationsABuildActuallyRuns(t *testing.T) {
	// The plan is what --dry-run prints, so it has to describe the real
	// pipeline. Comparing it against the golden file the build test produces
	// keeps the two from drifting apart.
	layout := state.NewLayout("$STATE_DIR")

	lines := []string{}
	for _, cmd := range Plan(layout, BuildOptions{Ref: ubuntuRef(t), Platform: "linux/amd64"}) {
		lines = append(lines, strings.Join(cmd.Argv(), " "))
	}
	planned := strings.Join(lines, "\n")

	built, err := os.ReadFile(filepath.Join(golden.Dir(), "image-build-ubuntu.argv"))
	if err != nil {
		t.Fatalf("reading the build golden file: %v", err)
	}

	// The build's golden file ends with the version probes, which a plan does
	// not perform, and carries real values where the plan carries placeholders.
	wantSteps := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(built)), "\n") {
		if strings.HasSuffix(line, "--version") {
			continue
		}
		wantSteps = append(wantSteps, toolAndSubcommand(line))
	}
	gotSteps := []string{}
	for _, line := range strings.Split(planned, "\n") {
		gotSteps = append(gotSteps, toolAndSubcommand(line))
	}

	if strings.Join(gotSteps, " | ") != strings.Join(wantSteps, " | ") {
		t.Errorf("the planned pipeline does not match the one a build runs:\n  plan:  %s\n  build: %s",
			strings.Join(gotSteps, " | "), strings.Join(wantSteps, " | "))
	}
}

// toolAndSubcommand reduces an invocation to the step it represents, so the
// comparison is about the pipeline rather than about paths that legitimately
// differ between a plan and a run.
func toolAndSubcommand(argv string) string {
	fields := strings.Fields(argv)
	if len(fields) == 0 {
		return ""
	}
	step := fields[0]
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") {
		step += " " + fields[1]
	}
	if len(fields) > 2 && step == "podman image" {
		step += " " + fields[2]
	}
	return step
}

func TestPlan_UsesConspicuousPlaceholdersForValuesItCannotKnow(t *testing.T) {
	// A printed plan must not look like it knows a digest it cannot know.
	lines := []string{}
	for _, cmd := range Plan(state.NewLayout("/state"), BuildOptions{Ref: ubuntuRef(t)}) {
		lines = append(lines, strings.Join(cmd.Argv(), " "))
	}
	plan := strings.Join(lines, "\n")

	for _, placeholder := range []string{PlaceholderDigest, PlaceholderContainerID, PlaceholderKernel, PlaceholderInitrd} {
		if !strings.Contains(plan, placeholder) {
			t.Errorf("plan does not use the placeholder %s:\n%s", placeholder, plan)
		}
	}
	if strings.Contains(plan, "sha256:") {
		t.Errorf("plan states a digest it cannot know:\n%s", plan)
	}
}

func TestPlan_TouchesNothingOnDisk(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")

	Plan(state.NewLayout(stateDir), BuildOptions{Ref: ubuntuRef(t)})
	PlanNotes(state.NewLayout(stateDir), ubuntuRef(t))

	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Errorf("planning created the state directory: %v", err)
	}
}

func TestPlanNotes_DescribeTheFileOperationsAndTheCommitStep(t *testing.T) {
	notes := strings.Join(PlanNotes(state.NewLayout("/state"), ubuntuRef(t)), "\n")

	for _, want := range []string{"manifest.json", "Containerfile", "/state/images/ubuntu/24.04"} {
		if !strings.Contains(notes, want) {
			t.Errorf("plan notes do not mention %q:\n%s", want, notes)
		}
	}
}
