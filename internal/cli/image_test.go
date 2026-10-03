package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// imageHost answers the tools an image build uses. It does not reproduce the
// build's side effects — the tests here are about the command surface; the
// pipeline itself is covered in internal/image.
func imageHost() *hostexec.Fake {
	fake := healthyHost()
	fake.RespondPrefix("podman", hostexec.FakeResponse{Stdout: "[]"})
	return fake
}

// cliRun runs one command against a fake host in a fresh state directory, and
// returns the exit code with what was written.
func cliRun(t *testing.T, fake *hostexec.Fake, stateDir string, args ...string) (int, string, string) {
	t.Helper()
	return cliRunContext(context.Background(), t, fake, stateDir, args...)
}

// cliRunContext is cliRun under a context the test controls, for the paths that
// begin with an interrupted command.
func cliRunContext(ctx context.Context, t *testing.T, fake *hostexec.Fake, stateDir string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer

	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake,
		// The state directory stays on this disk even when the test points at
		// a hypervisor on another machine (see App.StateFS).
		StateFS: state.Local(),
	}
	full := append([]string{"--state-dir", stateDir, "--config", t.TempDir() + "/absent.toml"}, args...)

	code := app.Main(ctx, full)
	return code, stdout.String(), stderr.String()
}

func TestImage_RequiresASubcommand(t *testing.T) {
	t.Parallel()
	code, _, stderr := cliRun(t, imageHost(), t.TempDir(), "image")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "build") {
		t.Errorf("stderr does not list the subcommands:\n%s", stderr)
	}
}

func TestImage_RejectsAnUnknownSubcommand(t *testing.T) {
	t.Parallel()
	code, _, _ := cliRun(t, imageHost(), t.TempDir(), "image", "publish")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestImageBuild_RejectsAnUnsupportedDistro(t *testing.T) {
	t.Parallel()
	code, _, stderr := cliRun(t, imageHost(), t.TempDir(), "image", "build", "alpine")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "ubuntu") {
		t.Errorf("stderr does not name the supported distros:\n%s", stderr)
	}
}

func TestImageList_EmptyCacheIsNotAnError(t *testing.T) {
	t.Parallel()
	code, stdout, _ := cliRun(t, imageHost(), t.TempDir(), "image", "list")

	if code != ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "No base images cached") {
		t.Errorf("stdout does not explain the empty cache:\n%s", stdout)
	}
}

func TestImageList_EmptyCacheEmitsAnEmptyJSONArray(t *testing.T) {
	t.Parallel()
	// A supervisor parsing this must get [] rather than null.
	code, stdout, _ := cliRun(t, imageHost(), t.TempDir(), "--output", "json", "image", "list")

	if code != ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	var manifests []state.Manifest
	if err := json.Unmarshal([]byte(stdout), &manifests); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", strings.TrimSpace(stdout))
	}
}

// cachedImage writes a complete base image into a state directory, as a
// finished build would leave it.
func cachedImage(t *testing.T, dir string) *state.Store {
	t.Helper()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	for _, path := range []string{
		store.BaseDiskPath("ubuntu", "24.04"),
		store.KernelPath("ubuntu", "24.04"),
		store.InitrdPath("ubuntu", "24.04"),
	} {
		if err := store.WriteFile(path, []byte("artifact\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	err = store.SaveManifest(&state.Manifest{
		Distro:        "ubuntu",
		Tag:           "24.04",
		SourceRef:     "docker.io/library/ubuntu:24.04",
		SourceDigest:  "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
		KernelVersion: "6.8.0-31-generic",
		KernelCmdline: "root=/dev/vda1 console=ttyS0 rw",
		ToolVersions:  map[string]string{"podman": "4.9.3", "virt-make-fs": "1.50.1"},
	})
	if err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	return store
}

func TestImageList_ShowsACachedImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachedImage(t, dir)

	code, stdout, _ := cliRun(t, imageHost(), dir, "image", "list")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"ubuntu:24.04", "6.8.0-31-generic", "sha256:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestImageInspect_ShowsProvenanceAndTheKernelCommandLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachedImage(t, dir)

	code, stdout, _ := cliRun(t, imageHost(), dir, "image", "inspect", "ubuntu")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// The full digest and the kernel command line are the two things an
	// operator comes to inspect for: with direct kernel boot the command line
	// is not discoverable from inside the guest.
	for _, want := range []string{
		"sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
		"root=/dev/vda1 console=ttyS0 rw",
		"6.8.0-31-generic",
		"podman",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestImageInspect_UnknownImageIsNotFound(t *testing.T) {
	t.Parallel()
	code, _, _ := cliRun(t, imageHost(), t.TempDir(), "image", "inspect", "fedora")

	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
}

func TestImageRm_RefusesWithoutConfirmationWhenThereIsNoTerminal(t *testing.T) {
	t.Parallel()
	// Assuming consent for a destructive operation in a non-interactive run is
	// exactly the mistake that loses someone's cached image.
	dir := t.TempDir()
	cachedImage(t, dir)

	code, _, stderr := cliRun(t, imageHost(), dir, "image", "rm", "ubuntu")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("stderr does not tell the operator how to proceed:\n%s", stderr)
	}

	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if !hasImage(t, store, "ubuntu", "24.04") {
		t.Error("the image was removed without confirmation")
	}
}

func TestImageRm_RemovesWithYes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := cachedImage(t, dir)

	code, _, stderr := cliRun(t, imageHost(), dir, "--yes", "image", "rm", "ubuntu")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if hasImage(t, store, "ubuntu", "24.04") {
		t.Error("the image is still cached after `image rm --yes`")
	}
}

func TestImageRm_DeclinedAtThePromptRemovesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := cachedImage(t, dir)

	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Stdin:  strings.NewReader("n\n"),
		Env:    func(string) string { return "" },
		Runner: imageHost(),
	}
	code := app.Main(context.Background(), []string{
		"--state-dir", dir, "--config", t.TempDir() + "/absent.toml",
		"image", "rm", "ubuntu",
	})

	if code != ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !hasImage(t, store, "ubuntu", "24.04") {
		t.Error("the image was removed after the operator declined")
	}
}

func TestImageRm_RefusesWhileAVMDependsOnTheImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := cachedImage(t, dir)

	vm := store.NewVM("agent-01")
	vm.BaseImage = state.BaseImageRef{Distro: "ubuntu", Tag: "24.04"}
	if err := store.SaveVM(vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	code, _, stderr := cliRun(t, imageHost(), dir, "--yes", "image", "rm", "ubuntu")

	if code == ExitOK {
		t.Fatal("image rm removed a base image a VM still depends on")
	}
	if !strings.Contains(stderr, "agent-01") {
		t.Errorf("stderr does not name the dependent VM:\n%s", stderr)
	}
	if !hasImage(t, store, "ubuntu", "24.04") {
		t.Error("the image was removed despite the refusal")
	}
}

func TestImageBuild_DryRunPrintsThePlanAndCreatesNothing(t *testing.T) {
	t.Parallel()
	// --dry-run must exit 0 having changed nothing at all — not even the state
	// directory it would otherwise create.
	dir := filepath.Join(t.TempDir(), "state")

	code, stdout, _ := cliRun(t, imageHost(), dir, "--dry-run", "image", "build", "fedora")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"podman pull", "virt-make-fs", "virt-sysprep", "manifest.json"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan does not mention %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("--dry-run created the state directory: %v", err)
	}
}

func TestImageRm_DryRunRemovesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := cachedImage(t, dir)

	code, stdout, _ := cliRun(t, imageHost(), dir, "--yes", "--dry-run", "image", "rm", "ubuntu")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "ubuntu/24.04") {
		t.Errorf("plan does not say what it would remove:\n%s", stdout)
	}
	if !hasImage(t, store, "ubuntu", "24.04") {
		t.Error("--dry-run removed the image")
	}
}

// Build progress is progress output, so --quiet silences it and it never
// appears on stdout, where `--output json` results are parsed (docs/cli.md).
func TestBuildProgress_WritesStepsToStderr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}

	app.buildProgress().Step(1, 10, "pulling the source image")

	if !strings.Contains(stderr.String(), "[1/10] pulling the source image") {
		t.Errorf("stderr does not carry the step: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout carried progress output: %q", stdout.String())
	}
}

func TestBuildProgress_IsSuppressedWhenThereIsNothingToReport(t *testing.T) {
	t.Parallel()
	cases := map[string]*App{
		"--quiet":   {Stderr: &bytes.Buffer{}, quiet: true},
		"--dry-run": {Stderr: &bytes.Buffer{}, dryRun: true},
	}
	for name, app := range cases {
		if got := app.buildProgress(); got != nil {
			t.Errorf("%s still reports build progress (%T)", name, got)
		}
	}

	if (&App{Stderr: &bytes.Buffer{}}).buildProgress() == nil {
		t.Error("a plain run reports no build progress")
	}
}

// The documented spelling puts a subcommand's own flags after the positional
// argument — `agent-vm image build ubuntu --force` — which Go's flag package
// stops parsing at. Both orderings have to work, or the command in the docs
// exits 2 with "flag provided but not defined".
func TestImageBuild_AcceptsItsFlagsOnEitherSideOfTheDistro(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flags after the distro", []string{"--dry-run", "image", "build", "fedora", "--force"}},
		{"flags before the distro", []string{"--dry-run", "image", "build", "--force", "fedora"}},
		{"flags on both sides", []string{"--dry-run", "image", "build", "--platform", "linux/amd64", "fedora", "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := cliRun(t, imageHost(), filepath.Join(t.TempDir(), "state"), tc.args...)
			if code != ExitOK {
				t.Fatalf("exit code = %d, want 0: %s", code, stderr)
			}
			if !strings.Contains(stdout, "podman pull") {
				t.Errorf("the plan was not printed:\n%s", stdout)
			}
		})
	}
}

func TestImageRm_AcceptsItsFlagsOnEitherSideOfTheDistro(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flags after the distro", []string{"--yes", "--dry-run", "image", "rm", "ubuntu", "--force"}},
		{"flags before the distro", []string{"--yes", "--dry-run", "image", "rm", "--force", "ubuntu"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cachedImage(t, dir)

			code, stdout, stderr := cliRun(t, imageHost(), dir, tc.args...)
			if code != ExitOK {
				t.Fatalf("exit code = %d, want 0: %s", code, stderr)
			}
			if !strings.Contains(stdout, "ubuntu/24.04") {
				t.Errorf("the plan does not say what it would remove:\n%s", stdout)
			}
		})
	}
}

// A second positional is still a usage error: accepting flags after the name
// must not turn a typo into a silently ignored argument.
func TestImage_RejectsASecondDistroArgument(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"image", "build", "fedora", "ubuntu"},
		{"--yes", "image", "rm", "fedora", "ubuntu"},
	} {
		code, _, stderr := cliRun(t, imageHost(), t.TempDir(), args...)
		if code != ExitUsage {
			t.Errorf("%v exited %d, want %d: %s", args, code, ExitUsage, stderr)
		}
		if !strings.Contains(stderr, "ubuntu") {
			t.Errorf("%v: the error does not name the unexpected argument: %s", args, stderr)
		}
	}
}

func hasImage(t *testing.T, store *state.Store, distro, tag string) bool {
	t.Helper()
	cached, err := store.HasImage(distro, tag)
	if err != nil {
		t.Fatalf("HasImage(%s, %s): %v", distro, tag, err)
	}
	return cached
}
