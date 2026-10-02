package image

import (
	"context"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// recordingProgress captures what a build reported, in order.
type recordingProgress struct {
	steps    []string
	totals   []int
	numbers  []int
	finished bool
	err      error
}

func (r *recordingProgress) Step(number, total int, name string) {
	r.numbers = append(r.numbers, number)
	r.totals = append(r.totals, total)
	r.steps = append(r.steps, name)
}

func (r *recordingProgress) Finish(err error) {
	r.finished = true
	r.err = err
}

func TestBuild_ReportsEveryStepInOrder(t *testing.T) {
	fake := ubuntuHost(t)
	builder, _ := newBuilder(t, fake)
	reported := &recordingProgress{}
	builder.Progress = reported

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Every step the pipeline has is reported, once, in the order it runs.
	// A step that stops being reported leaves the operator watching a bar that
	// stalls on the step before it.
	if len(reported.steps) != len(buildStepNames) {
		t.Fatalf("reported %d steps, want %d: %v", len(reported.steps), len(buildStepNames), reported.steps)
	}
	for i, name := range buildStepNames {
		if reported.steps[i] != name {
			t.Errorf("step %d = %q, want %q", i+1, reported.steps[i], name)
		}
		if reported.numbers[i] != i+1 {
			t.Errorf("step %q reported as number %d, want %d", name, reported.numbers[i], i+1)
		}
		if reported.totals[i] != len(buildStepNames) {
			t.Errorf("step %q reported a total of %d, want %d", name, reported.totals[i], len(buildStepNames))
		}
	}

	if !reported.finished || reported.err != nil {
		t.Errorf("Finish(%v), finished = %v; want Finish(nil) after a successful build", reported.err, reported.finished)
	}
}

func TestBuild_ReportsTheFailureThatEndedTheBuild(t *testing.T) {
	fake := ubuntuHost(t)
	fake.RespondPrefix("podman pull", hostexec.FakeResponse{ExitCode: 125, Stderr: "no such host"})
	builder, _ := newBuilder(t, fake)
	reported := &recordingProgress{}
	builder.Progress = reported

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err == nil {
		t.Fatal("Build succeeded with a failing podman pull")
	}

	if len(reported.steps) != 1 || reported.steps[0] != buildStepNames[stepPull] {
		t.Errorf("reported steps = %v, want only %q", reported.steps, buildStepNames[stepPull])
	}
	if !reported.finished || reported.err == nil {
		t.Errorf("Finish(%v), finished = %v; want Finish(err) after a failed build", reported.err, reported.finished)
	}
}

// A cache hit does no work, so there is nothing to report: a bar that appeared
// and vanished would suggest a rebuild that never happened.
func TestBuild_ReportsNoStepsForACachedImage(t *testing.T) {
	fake := ubuntuHost(t)
	builder, _ := newBuilder(t, fake)
	ref := ubuntuRef(t)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	reported := &recordingProgress{}
	builder.Progress = reported
	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("second Build: %v", err)
	}

	if len(reported.steps) != 0 || reported.finished {
		t.Errorf("a cached build reported steps %v and finished = %v; want neither", reported.steps, reported.finished)
	}
}
