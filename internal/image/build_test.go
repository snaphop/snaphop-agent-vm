package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/golden"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
	"github.com/snaphop/snaphop-agent-vm/templates"
)

// The digest a fake registry hands back. It is a real-shaped sha256 because
// anything else is refused before it can reach a manifest.
const ubuntuDigest = "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15"

// fakeHost stands in for podman and the libguestfs tools, and reproduces the
// side effects the next step in the pipeline depends on: virt-make-fs writes a
// disk, virt-copy-out writes a kernel and initramfs.
func fakeHost(t *testing.T) *hostexec.Fake {
	t.Helper()
	fake := hostexec.NewFake()

	fake.RespondPrefix("podman pull", hostexec.FakeResponse{})
	fake.RespondPrefix("podman image inspect", hostexec.FakeResponse{
		Stdout: fmt.Sprintf(`[{"Id":"c6348fa86ba0","Digest":%q}]`, ubuntuDigest),
	})
	fake.RespondPrefix("podman build", hostexec.FakeResponse{})
	fake.RespondPrefix("podman create", hostexec.FakeResponse{Stdout: "9b2c0a1d4e5f6a7b8c9d0e1f2a3b4c5d\n"})
	fake.RespondPrefix("podman export", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error { return touch(argAfter(c.Args, "--output")) },
	})
	fake.RespondPrefix("podman rm --force", hostexec.FakeResponse{})

	fake.RespondPrefix("virt-make-fs --type", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error { return touch(c.Args[len(c.Args)-1]) },
	})
	fake.RespondPrefix("virt-sysprep -a", hostexec.FakeResponse{})

	// The version probes are answered explicitly. Matching them with the
	// prefix rules above would hand `--version` to a rule that treats its last
	// argument as a path to write.
	for _, tool := range []string{"podman", "virt-make-fs", "virt-ls", "virt-copy-out", "virt-sysprep"} {
		fake.Respond(tool+" --version", hostexec.FakeResponse{Stdout: tool + " 1.50.1\n"})
	}
	fake.Respond("podman --version", hostexec.FakeResponse{Stdout: "podman version 4.9.3\n"})
	return fake
}

// ubuntuHost is fakeHost with the directory listings and extraction behavior of
// a successfully built Ubuntu image.
func ubuntuHost(t *testing.T) *hostexec.Fake {
	t.Helper()
	fake := fakeHost(t)

	// virt-ls is asked about two directories inside a disk whose path is only
	// known at run time, so the listings are matched on the directory argument
	// rather than on a literal argument vector. The contents are what a real
	// Ubuntu guest has after the build recipe runs.
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name != "virt-ls" || len(c.Args) == 0 {
			return hostexec.FakeResponse{}, false
		}
		switch c.Args[len(c.Args)-1] {
		case distro.ModulesDir:
			return hostexec.FakeResponse{Stdout: "6.8.0-31-generic\n"}, true
		case "/boot":
			return hostexec.FakeResponse{Stdout: strings.Join([]string{
				"config-6.8.0-31-generic",
				"initrd.img-6.8.0-31-generic",
				"vmlinuz-6.8.0-31-generic",
			}, "\n") + "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	fake.RespondPrefix("virt-copy-out", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error {
			dir := c.Args[len(c.Args)-1]
			for _, name := range []string{"vmlinuz-6.8.0-31-generic", "initrd.img-6.8.0-31-generic"} {
				if err := touch(filepath.Join(dir, name)); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return fake
}

func touch(path string) error {
	if path == "" {
		return errors.New("no output path in the argument vector")
	}
	return os.WriteFile(path, []byte("test artifact\n"), 0o644)
}

func argAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newBuilder(t *testing.T, fake *hostexec.Fake) (*Builder, *state.Store) {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the state directory: %v", err)
	}
	return &Builder{
		Runner:         fake,
		Versions:       hostexec.NewVersions(fake),
		Store:          store,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		AgentVMVersion: "0.1.0-test",
	}, store
}

func ubuntuRef(t *testing.T) distro.Ref {
	t.Helper()
	ref, err := distro.ParseRef("ubuntu")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	return ref
}

func TestBuild_RunsTheDocumentedToolPipeline(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t), Platform: "linux/amd64"}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The sequence of tools is the contract: it is what docs/cli.md's
	// "Underlying Commands" table promises and what --dry-run prints.
	got := []string{}
	for _, call := range fake.Calls() {
		argv := strings.Join(call.Argv(), " ")
		argv = strings.ReplaceAll(argv, store.Root(), "$STATE_DIR")
		got = append(got, redactWorkspace(argv))
	}
	golden.Assert(t, "image-build-ubuntu.argv", []byte(strings.Join(got, "\n")+"\n"))
}

// workspacePID matches the process ID that ends a temporary build directory
// name. The trailing delimiter is part of the match so that a version number
// inside the name (".build-ubuntu-26.04-1234") is not mistaken for the PID.
var workspacePID = regexp.MustCompile(`(\.build-[a-z0-9.\-]+?)-\d+(/|\s|$)`)

// redactWorkspace removes the PID from the temporary build directory so the
// golden file is stable across runs.
func redactWorkspace(argv string) string {
	return workspacePID.ReplaceAllString(argv, "${1}-PID${2}")
}

func TestBuild_PinsTheSourceImageByDigest(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, _ := newBuilder(t, fake)

	manifest, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if manifest.SourceDigest != ubuntuDigest {
		t.Errorf("SourceDigest = %s, want %s", manifest.SourceDigest, ubuntuDigest)
	}
	// The build must happen on top of the digest, not the tag it came from:
	// a tag can be rebuilt underneath us, and Arch's certainly will be.
	pinned := "BASE_IMAGE=docker.io/library/ubuntu@" + ubuntuDigest
	found := false
	for _, call := range fake.Calls() {
		if call.Name == "podman" && len(call.Args) > 0 && call.Args[0] == "build" {
			for _, arg := range call.Args {
				if arg == pinned {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("podman build was not given the digest-pinned base image %q\n%s", pinned, fake)
	}
}

func TestBuild_RecordsProvenanceInTheManifest(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	fake.Respond("podman --version", hostexec.FakeResponse{Stdout: "podman version 4.9.3\n"})
	for _, tool := range []string{"virt-make-fs", "virt-ls", "virt-copy-out", "virt-sysprep"} {
		fake.Respond(tool+" --version", hostexec.FakeResponse{Stdout: tool + " 1.50.1\n"})
	}
	builder, _ := newBuilder(t, fake)

	manifest, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if manifest.KernelVersion != "6.8.0-31-generic" {
		t.Errorf("KernelVersion = %q, want 6.8.0-31-generic", manifest.KernelVersion)
	}
	// Under direct kernel boot the command line lives on the host, so the
	// manifest is the only place it is recorded.
	if manifest.KernelCmdline != distro.KernelCmdline {
		t.Errorf("KernelCmdline = %q, want %q", manifest.KernelCmdline, distro.KernelCmdline)
	}
	if manifest.AgentVMVersion != "0.1.0-test" {
		t.Errorf("AgentVMVersion = %q", manifest.AgentVMVersion)
	}
	for _, tool := range []string{"podman", "virt-make-fs", "virt-sysprep"} {
		if manifest.ToolVersions[tool] == "" {
			t.Errorf("manifest does not record the %s version that built the image", tool)
		}
	}
}

func TestBuild_ProducesAllThreeArtifactsAndAManifest(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !hasImage(t, store, "ubuntu", "26.04") {
		t.Fatal("the built image is not reported as cached")
	}
	for _, path := range []string{
		store.BaseDiskPath("ubuntu", "26.04"),
		store.KernelPath("ubuntu", "26.04"),
		store.InitrdPath("ubuntu", "26.04"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing artifact %s: %v", filepath.Base(path), err)
		}
	}
	// The exported tar is large and useless once the disk exists.
	if _, err := os.Stat(filepath.Join(store.ImageDir("ubuntu", "26.04"), "rootfs.tar")); err == nil {
		t.Error("the exported root filesystem tar was left in the image cache")
	}
}

func TestBuild_FailureLeavesNoBootableImageBehind(t *testing.T) {
	t.Parallel()
	// No VM may ever boot a partially built base image (SECURITY.md), so a
	// failure part-way through must leave the cache untouched.
	fake := ubuntuHost(t)
	fake.RespondPrefix("virt-sysprep -a", hostexec.FakeResponse{
		Stderr:   "libguestfs: error: could not create appliance\n",
		ExitCode: 1,
	})
	builder, store := newBuilder(t, fake)

	_, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
	if err == nil {
		t.Fatal("Build succeeded despite virt-sysprep failing")
	}
	if !strings.Contains(err.Error(), "virt-sysprep") {
		t.Errorf("error %q does not name the tool that failed", err)
	}

	if hasImage(t, store, "ubuntu", "26.04") {
		t.Error("a failed build left a usable image in the cache")
	}
	entries, err := os.ReadDir(filepath.Dir(store.ImageDir("ubuntu", "26.04")))
	if err != nil {
		t.Fatalf("reading the image cache: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".build-") {
			t.Errorf("a failed build left its workspace behind: %s", entry.Name())
		}
	}
}

func TestBuild_UsesTheCacheUnlessForced(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, _ := newBuilder(t, fake)
	ctx := context.Background()

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	callsAfterFirst := len(fake.Calls())

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if len(fake.Calls()) != callsAfterFirst {
		t.Errorf("a cached image was rebuilt: %d extra tool invocations", len(fake.Calls())-callsAfterFirst)
	}

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t), Force: true}); err != nil {
		t.Fatalf("forced Build: %v", err)
	}
	if len(fake.Calls()) == callsAfterFirst {
		t.Error("--force did not rebuild the image")
	}
}

func TestBuild_RefusesAnUnpinnableDigest(t *testing.T) {
	t.Parallel()
	// Falling back to an unpinned reference is forbidden outright: it would
	// make the manifest's provenance a lie.
	for _, response := range []string{
		`[{"Digest":"not-a-digest"}]`,
		`[]`,
		`not json`,
		`[{"Digest":"sha256:short"}]`,
	} {
		fake := ubuntuHost(t)
		fake.RespondPrefix("podman image inspect", hostexec.FakeResponse{Stdout: response})
		builder, store := newBuilder(t, fake)

		_, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
		if err == nil {
			t.Errorf("Build accepted the digest response %q", response)
		}
		if hasImage(t, store, "ubuntu", "26.04") {
			t.Errorf("an image was cached despite an unusable digest (%q)", response)
		}
	}
}

func TestBuild_ReportsAMissingKernelAgainstTheBuildRecipe(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "virt-ls" && c.Args[len(c.Args)-1] == distro.ModulesDir {
			return hostexec.FakeResponse{Stdout: "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}
	builder, _ := newBuilder(t, fake)

	_, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
	if err == nil {
		t.Fatal("Build succeeded with no kernel in the image")
	}
	if !strings.Contains(err.Error(), "kernel") {
		t.Errorf("error %q does not say a kernel is missing", err)
	}
}

func TestBuild_HonoursTheFromOverride(t *testing.T) {
	t.Parallel()
	// --from is the supported escape hatch for a custom image within a
	// supported family (ADR-0006).
	fake := ubuntuHost(t)
	builder, _ := newBuilder(t, fake)

	manifest, err := builder.Build(context.Background(), BuildOptions{
		Ref:  ubuntuRef(t),
		From: "registry.example.com:5000/team/ubuntu-custom:2026-08",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if manifest.SourceRef != "registry.example.com:5000/team/ubuntu-custom:2026-08" {
		t.Errorf("SourceRef = %q, want the overridden reference", manifest.SourceRef)
	}
	if !fake.Ran("podman pull --platform linux/amd64 registry.example.com:5000/team/ubuntu-custom:2026-08") &&
		!fake.Ran("podman pull --platform linux/arm64 registry.example.com:5000/team/ubuntu-custom:2026-08") {
		t.Errorf("the overridden reference was not pulled\n%s", fake)
	}
}

func TestRepoOf_SeparatesATagFromARegistryPort(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"docker.io/library/ubuntu:26.04", "docker.io/library/ubuntu"},
		{"docker.io/library/ubuntu", "docker.io/library/ubuntu"},
		{"registry.example.com:5000/team/img:tag", "registry.example.com:5000/team/img"},
		{"registry.example.com:5000/team/img", "registry.example.com:5000/team/img"},
		{"docker.io/library/ubuntu@" + ubuntuDigest, "docker.io/library/ubuntu"},
	}
	for _, tt := range tests {
		if got := repoOf(tt.in); got != tt.want {
			t.Errorf("repoOf(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMatchOne_RefusesBootEntriesThatAreNotPlainFileNames(t *testing.T) {
	t.Parallel()
	// /boot listings come out of an image we did not write, and the chosen
	// name goes straight into another tool's argument vector.
	entries := []string{"../../etc/shadow", "-rf", "sub/vmlinuz-6.8.0", ".hidden"}
	if got, err := matchOne(entries, "*", "kernel", "ubuntu"); err == nil {
		t.Errorf("matchOne accepted %q from an untrusted listing", got)
	}
}

func TestRemove_RefusesWhileAVMStillUsesTheImage(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)
	ref := ubuntuRef(t)
	ctx := context.Background()

	if _, err := builder.Build(ctx, BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	vm := store.NewVM("agent-01")
	vm.BaseImage = state.BaseImageRef{Distro: "ubuntu", Tag: "26.04"}
	if err := store.SaveVM(vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	err := builder.Remove(ctx, ref, false)
	if err == nil {
		t.Fatal("Remove deleted a base image that a VM's overlay depends on")
	}
	if !strings.Contains(err.Error(), "agent-01") {
		t.Errorf("error %q does not name the VM that still depends on the image", err)
	}
	if !hasImage(t, store, "ubuntu", "26.04") {
		t.Error("the image was removed despite the refusal")
	}

	// --force names the consequence and goes through.
	if err := builder.Remove(ctx, ref, true); err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if hasImage(t, store, "ubuntu", "26.04") {
		t.Error("--force did not remove the image")
	}
}

// createInProgress stands in for a create that has made its VM directory and
// holds its lock, but has not written its record yet.
func createInProgress(t *testing.T, store *state.Store, name string) {
	t.Helper()
	if err := store.MkdirAll(store.VMDir(name)); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lock, err := store.TryLockVM(name, "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
}

func TestRemove_RefusesEvenWithForceWhileACreateIsInProgress(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ref := ubuntuRef(t)
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	createInProgress(t, store, "agent-01")

	err := builder.Remove(ctx, ref, true)
	var busy *state.BusyError
	if !errors.As(err, &busy) || !strings.Contains(err.Error(), "agent-01") {
		t.Fatalf("Remove = %v, want a BusyError naming the create in progress", err)
	}
	if !hasImage(t, store, "ubuntu", "26.04") {
		t.Error("the image was removed from under a create in progress")
	}
}

func TestBuild_ForceRefusesToReplaceAnImageAVMDependsOn(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)
	ref := ubuntuRef(t)
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	vm := store.NewVM("agent-01")
	vm.BaseImage = state.BaseImageRef{Distro: "ubuntu", Tag: "26.04"}
	if err := store.SaveVM(vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	callsBefore := len(fake.Calls())

	_, err := builder.Build(ctx, BuildOptions{Ref: ref, Force: true})
	if err == nil || !strings.Contains(err.Error(), "agent-01") {
		t.Fatalf("Build --force = %v, want a refusal naming the dependent VM", err)
	}
	if len(fake.Calls()) != callsBefore {
		t.Errorf("the refused rebuild still ran %d tool invocations", len(fake.Calls())-callsBefore)
	}
}

func TestBuild_ForceRefusesWhileACreateIsInProgress(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ref := ubuntuRef(t)
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	createInProgress(t, store, "agent-01")

	_, err := builder.Build(ctx, BuildOptions{Ref: ref, Force: true})
	var busy *state.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Build --force = %v, want a BusyError", err)
	}
}

func TestEnsureImage_WaitsForTheImageLock(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ref := ubuntuRef(t)
	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ref}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// An image rm holds the lock. A cache hit must not read past it, or the
	// create would go on to use an image that is being removed.
	held, err := store.LockImage(context.Background(), "ubuntu", "26.04", "image rm")
	if err != nil {
		t.Fatalf("LockImage: %v", err)
	}
	defer func() { _ = held.Release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var busy *state.BusyError
	if _, err := builder.EnsureImage(ctx, ref); !errors.As(err, &busy) {
		t.Fatalf("EnsureImage = %v, want it to wait on the held image lock", err)
	}
}

func TestRemove_UnknownImageIsNotFound(t *testing.T) {
	t.Parallel()
	builder, _ := newBuilder(t, ubuntuHost(t))

	err := builder.Remove(context.Background(), ubuntuRef(t), false)

	var notFound *state.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %v, want *NotFoundError", err)
	}
}

func parseRef(t *testing.T, s string) distro.Ref {
	t.Helper()
	ref, err := distro.ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", s, err)
	}
	return ref
}

// leaveWorkspace plants what a build killed part-way through leaves behind: a
// workspace directory holding a root filesystem tar.
func leaveWorkspace(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := touch(filepath.Join(dir, "rootfs.tar")); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_RemovesWorkspacesLeftByAKilledBuildOfTheSameImage(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ubuntu := filepath.Dir(store.ImageDir("ubuntu", "26.04"))

	// Two dead builds of ubuntu:26.04. This process's PID is not among them,
	// which is the case a crash leaves.
	dead := []string{filepath.Join(ubuntu, ".build-26.04-999991"), filepath.Join(ubuntu, ".build-26.04-7")}
	// Workspaces that look alike but belong to other images, whose builds may
	// be running right now under their own locks.
	others := []string{
		filepath.Join(ubuntu, ".build-26.04.1-999991"),
		filepath.Join(ubuntu, ".build-26.04-1-999991"),
		filepath.Join(filepath.Dir(store.ImageDir("ubuntu-slim", "26.04")), ".build-26.04-999991"),
	}
	for _, dir := range append(append([]string{}, dead...), others...) {
		leaveWorkspace(t, dir)
	}

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, dir := range dead {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the workspace of a killed build is still there: %s (%v)", dir, err)
		}
	}
	for _, dir := range others {
		if _, err := os.Stat(filepath.Join(dir, "rootfs.tar")); err != nil {
			t.Errorf("a workspace of another image was touched: %s (%v)", dir, err)
		}
	}
}

// backupOf is where a rebuild moves ubuntu:26.04 aside.
func backupOf(store *state.Store) string {
	return filepath.Join(filepath.Dir(store.ImageDir("ubuntu", "26.04")), ".26.04.previous")
}

func TestBuild_RestoresTheImageAKilledRebuildLeftOnlyAsItsBackup(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// A rebuild killed between moving the old image aside and installing the
	// new one.
	backup := backupOf(store)
	if err := os.Rename(store.ImageDir("ubuntu", "26.04"), backup); err != nil {
		t.Fatal(err)
	}
	if images, err := store.ListImages(); err != nil || len(images) != 0 {
		t.Errorf("ListImages = %v, %v; the backup must not be listed as an image", images, err)
	}
	calls := len(fake.Calls())

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build after the interrupted rebuild: %v", err)
	}
	if extra := len(fake.Calls()) - calls; extra != 0 {
		t.Errorf("the last good image was rebuilt instead of restored: %d tool invocations", extra)
	}
	if !hasImage(t, store, "ubuntu", "26.04") {
		t.Error("ubuntu:26.04 was not restored from its backup")
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the backup is still there after being restored: %v", err)
	}
}

func TestBuild_RemovesABackupLeftBesideAnInstalledImage(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// A rebuild killed after installing the new image but before removing the
	// old one.
	backup := backupOf(store)
	leaveWorkspace(t, backup)

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leftover backup is still there: %v", err)
	}
	if !hasImage(t, store, "ubuntu", "26.04") {
		t.Error("the installed image was lost")
	}
}

func TestRemove_ReachesAnImageAKilledRebuildLeftOnlyAsItsBackup(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ctx := context.Background()
	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	backup := backupOf(store)
	if err := os.Rename(store.ImageDir("ubuntu", "26.04"), backup); err != nil {
		t.Fatal(err)
	}

	if err := builder.Remove(ctx, ubuntuRef(t), false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, dir := range []string{store.ImageDir("ubuntu", "26.04"), backup} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after image rm: %v", dir, err)
		}
	}
}

func TestBuild_ForcedRebuildLeavesAnImageTaggedLikeABackupAlone(t *testing.T) {
	t.Parallel()
	builder, store := newBuilder(t, ubuntuHost(t))
	ctx := context.Background()

	// "26.04.previous" is a valid tag, so it is a separate cached image that a
	// rebuild of 26.04 must never treat as its own backup. The backup's name
	// is one no tag can take.
	if _, err := distro.ParseRef("ubuntu:.26.04.previous"); err == nil {
		t.Fatal("ParseRef accepted a tag naming a rebuild's backup directory")
	}
	for _, ref := range []string{"ubuntu:26.04", "ubuntu:26.04.previous"} {
		if _, err := builder.Build(ctx, BuildOptions{Ref: parseRef(t, ref)}); err != nil {
			t.Fatalf("Build %s: %v", ref, err)
		}
	}

	if _, err := builder.Build(ctx, BuildOptions{Ref: ubuntuRef(t), Force: true}); err != nil {
		t.Fatalf("forced Build: %v", err)
	}
	if !hasImage(t, store, "ubuntu", "26.04.previous") {
		t.Error("rebuilding ubuntu:26.04 deleted the separately cached ubuntu:26.04.previous")
	}
}

func slimRef(t *testing.T) distro.Ref {
	t.Helper()
	ref, err := distro.ParseRef("ubuntu-slim")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	return ref
}

func TestBuild_SlimImageIsCachedUnderItsOwnNameAndBuiltFromTheSlimRecipe(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)

	manifest, err := builder.Build(context.Background(), BuildOptions{Ref: slimRef(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The name is what every later operation addresses the image by: the cache
	// directory, `image rm`, and the vm.json of every VM built on it.
	if manifest.Distro != "ubuntu-slim" || manifest.Ref() != "ubuntu-slim:26.04" {
		t.Errorf("manifest names the image %s, want ubuntu-slim:26.04", manifest.Ref())
	}
	if !hasImage(t, store, "ubuntu-slim", "26.04") {
		t.Fatal("the built slim image is not reported as cached")
	}
	// A slim build must not be mistaken for, or overwrite, the full image of
	// the same family and tag.
	if hasImage(t, store, "ubuntu", "26.04") {
		t.Error("building ubuntu-slim also produced an image cached as ubuntu")
	}

	// The recipe that reached podman is the slim one, and it carries none of
	// the agent tooling the full recipe installs.
	var context string
	for _, call := range fake.Calls() {
		if call.Name == "podman" && len(call.Args) > 0 && call.Args[0] == "build" {
			context = argAfter(call.Args, "--file")
		}
	}
	if context == "" {
		t.Fatalf("no podman build in the pipeline\n%s", fake)
	}
	// The workspace is removed once the build commits, so the recipe is
	// compared against the embedded file podman was pointed at.
	wanted, err := templates.FS.ReadFile("distro/ubuntu-slim.Containerfile")
	if err != nil {
		t.Fatalf("reading the slim recipe: %v", err)
	}
	if !strings.Contains(string(wanted), "systemctl --root=/ enable") {
		t.Error("the slim recipe enables no units; a guest built from it would not be reachable")
	}
	// Comments name what slim leaves out, so only the instructions are
	// checked for the agent tooling they would install.
	for _, line := range strings.Split(string(wanted), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, tool := range []string{"mise", "rustup", "playwright", "docker"} {
			if strings.Contains(strings.ToLower(line), tool) {
				t.Errorf("the slim recipe installs %s; slim images carry no agent tooling: %s", tool, line)
			}
		}
	}
}

func TestBuild_SlimAndFullImagesOfOneFamilyCoexist(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)

	for _, ref := range []distro.Ref{ubuntuRef(t), slimRef(t)} {
		if _, err := builder.Build(context.Background(), BuildOptions{Ref: ref}); err != nil {
			t.Fatalf("Build(%s): %v", ref, err)
		}
	}

	for _, name := range []string{"ubuntu", "ubuntu-slim"} {
		if !hasImage(t, store, name, "26.04") {
			t.Errorf("%s:26.04 is not cached after building both", name)
		}
	}
}

func TestBuild_RunnerImageIsCachedUnderItsOwnNameAndBuiltFromTheRunnerRecipe(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	var recipe, install, configure []byte
	fake.RespondPrefix("podman build", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error {
			// The workspace is removed once the build commits, so the recipe
			// and the scripts beside it have to be read while podman is
			// invoked.
			file := argAfter(c.Args, "--file")
			dir := filepath.Dir(file)
			var err error
			if recipe, err = os.ReadFile(file); err != nil {
				return err
			}
			if install, err = os.ReadFile(filepath.Join(dir, "github-runner.sh")); err != nil {
				return err
			}
			configure, err = os.ReadFile(filepath.Join(dir, "github-runner-configure.sh"))
			return err
		},
	})
	builder, store := newBuilder(t, fake)
	ref, err := distro.ParseRef("ubuntu-runner")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}

	manifest, err := builder.Build(context.Background(), BuildOptions{Ref: ref})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if manifest.Distro != "ubuntu-runner" || manifest.Ref() != "ubuntu-runner:26.04" {
		t.Errorf("manifest names the image %s, want ubuntu-runner:26.04", manifest.Ref())
	}
	if !hasImage(t, store, "ubuntu-runner", "26.04") {
		t.Error("the built runner image is not reported as cached")
	}
	if hasImage(t, store, "ubuntu", "26.04") || hasImage(t, store, "ubuntu-slim", "26.04") {
		t.Error("building ubuntu-runner also cached ubuntu or ubuntu-slim")
	}

	wanted, err := templates.FS.ReadFile("distro/ubuntu-runner.Containerfile")
	if err != nil {
		t.Fatalf("reading the runner recipe: %v", err)
	}
	if string(recipe) != string(wanted) {
		t.Errorf("podman was given a recipe of %d bytes, want the embedded ubuntu-runner.Containerfile (%d bytes)", len(recipe), len(wanted))
	}
	for _, got := range []struct {
		name string
		got  []byte
	}{
		{"github-runner.sh", install},
		{"github-runner-configure.sh", configure},
	} {
		want, err := templates.FS.ReadFile("distro/" + got.name)
		if err != nil {
			t.Fatalf("reading %s: %v", got.name, err)
		}
		if string(got.got) != string(want) {
			t.Errorf("build context %s is %d bytes, want the embedded file (%d bytes)", got.name, len(got.got), len(want))
		}
	}
}

func TestNewestByVersion_ComparesVersionNumbersNotStrings(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		names []string
		want  string
	}{
		{[]string{"6.9.0-1-generic", "6.10.0-1-generic"}, "6.10.0-1-generic"},
		{[]string{"vmlinuz-6.10.0-31-generic", "vmlinuz-6.9.12-31-generic"}, "vmlinuz-6.10.0-31-generic"},
		{[]string{"6.8.0-31-generic", "6.8.0-100-generic", "6.8.0-9-generic"}, "6.8.0-100-generic"},
		{[]string{"6.12.4-arch1-1", "6.12.4-arch1-2"}, "6.12.4-arch1-2"},
		{[]string{"6.8.0", "6.8.0-1"}, "6.8.0-1"},
		{[]string{"6.08", "6.7"}, "6.08"},
	} {
		if got := newestByVersion(tt.names); got != tt.want {
			t.Errorf("newestByVersion(%q) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestBuild_LeavesAPlainKernelUntouched(t *testing.T) {
	t.Parallel()
	fake := ubuntuHost(t)
	builder, store := newBuilder(t, fake)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := os.ReadFile(store.KernelPath("ubuntu", "26.04"))
	if err != nil {
		t.Fatalf("reading the cached kernel: %v", err)
	}
	if string(got) != "test artifact\n" {
		t.Fatalf("cached kernel = %q, want the extracted file unchanged", got)
	}
	if commandRan(fake, "zstd") {
		t.Fatal("a plain kernel was passed to zstd")
	}
}

func TestBuild_UnpacksAGzipUnifiedKernel(t *testing.T) {
	t.Parallel()
	want := []byte("gzip-direct-boot-kernel")
	kernel := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("gzip", gzipBytes(t, want), 0)}})
	fake := ubuntuHostWithKernel(t, kernel)
	builder, store := newBuilder(t, fake)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := os.ReadFile(store.KernelPath("ubuntu", "26.04"))
	if err != nil {
		t.Fatalf("reading the cached kernel: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("cached kernel = %q, want the decompressed payload", got)
	}
	if commandRan(fake, "zstd") {
		t.Fatal("a gzip payload was passed to zstd")
	}
	assertNoUnpackLeftovers(t, store.ImageDir("ubuntu", "26.04"))
}

func TestBuild_UnpacksAZstdUnifiedKernelWithZstd(t *testing.T) {
	t.Parallel()
	const want = "zstd-direct-boot-kernel\n"
	kernel := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}, 0)}})
	fake := ubuntuHostWithKernel(t, kernel)
	fake.RespondPrefix("zstd", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error {
			if c.Location != hostexec.Hypervisor {
				return fmt.Errorf("zstd location = %d, want the hypervisor", c.Location)
			}
			out := argAfter(c.Args, "-o")
			if out == "" {
				return errors.New("zstd was not given -o")
			}
			return os.WriteFile(out, []byte(want), 0o644)
		},
	})
	builder, store := newBuilder(t, fake)

	if _, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := os.ReadFile(store.KernelPath("ubuntu", "26.04"))
	if err != nil {
		t.Fatalf("reading the cached kernel: %v", err)
	}
	if string(got) != want {
		t.Fatalf("cached kernel = %q, want %q", got, want)
	}
	assertNoUnpackLeftovers(t, store.ImageDir("ubuntu", "26.04"))
}

func TestBuild_RefusesAUnifiedKernelWhenZstdFails(t *testing.T) {
	t.Parallel()
	kernel := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}, 0)}})
	fake := ubuntuHostWithKernel(t, kernel)
	fake.Missing["zstd"] = true
	builder, store := newBuilder(t, fake)

	_, err := builder.Build(context.Background(), BuildOptions{Ref: ubuntuRef(t)})
	if err == nil {
		t.Fatal("Build succeeded without zstd")
	}
	if !strings.Contains(err.Error(), "zstd") {
		t.Fatalf("error = %v, want it to name zstd", err)
	}
	if hasImage(t, store, "ubuntu", "26.04") {
		t.Fatal("a failed unpack left a bootable image in the cache")
	}
}

func commandRan(fake *hostexec.Fake, name string) bool {
	for _, call := range fake.Calls() {
		if call.Name == name {
			return true
		}
	}
	return false
}

func ubuntuHostWithKernel(t *testing.T, kernel []byte) *hostexec.Fake {
	t.Helper()
	fake := ubuntuHost(t)
	fake.RespondPrefix("virt-copy-out", hostexec.FakeResponse{
		Do: func(c hostexec.Command) error {
			dir := c.Args[len(c.Args)-1]
			if err := os.WriteFile(filepath.Join(dir, "vmlinuz-6.8.0-31-generic"), kernel, 0o644); err != nil {
				return err
			}
			return touch(filepath.Join(dir, "initrd.img-6.8.0-31-generic"))
		},
	})
	return fake
}

func assertNoUnpackLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == "kernel.zstd" || strings.HasSuffix(entry.Name(), ".unpacked") {
			t.Errorf("unpacking left %s in the image cache", entry.Name())
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
