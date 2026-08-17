// Package golden compares generated output against a checked-in fixture.
//
// Golden files pin the things this project generates and other software
// depends on: the argument vectors handed to host tools, and the cloud-init
// user-data handed to a guest. A diff in one of them is a contract change and
// has to be justified, which is exactly why they are checked in rather than
// recomputed.
//
// Regenerate them with `go test ./... -update-golden` after deciding the change
// is intended. Never hand-edit a file under test/golden/.
//
// This package is imported only from tests; the flag it registers therefore
// only ever exists in a test binary.
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var update = flag.Bool("update-golden", false, "rewrite golden files in test/golden/ instead of comparing against them")

// Dir is the golden file directory, located relative to this source file so
// that it resolves the same way from any package's tests.
func Dir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: cannot determine the location of the golden directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "test", "golden")
}

// Assert compares got against the golden file named name, failing the test with
// a readable diff when they differ.
func Assert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(Dir(), name)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating the golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", name, err)
		}
		t.Logf("updated golden file %s", name)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v\n"+
			"If this output is new and correct, create it with: go test ./... -update-golden", name, err)
	}
	if string(got) == string(want) {
		return
	}

	t.Errorf("output does not match the golden file %s.\n"+
		"This is a contract change: the generated output that other software depends on has changed.\n"+
		"If it is intended, justify it in the pull request and regenerate with `go test ./... -update-golden`.\n\n"+
		"--- want (%s)\n%s\n+++ got\n%s", name, name, want, got)
}
