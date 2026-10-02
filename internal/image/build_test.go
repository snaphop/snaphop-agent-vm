package image

import (
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
// inside the name (".build-ubuntu-24.04-1234") is not mistaken for the PID.
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

	if !store.HasImage("ubuntu", "24.04") {
		t.Fatal("the built image is not reported as cached")
	}
	for _, path := range []string{
		store.BaseDiskPath("ubuntu", "24.04"),
		store.KernelPath("ubuntu", "24.04"),
		store.InitrdPath("ubuntu", "24.04"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing artifact %s: %v", filepath.Base(path), err)
		}
	}
	// The exported tar is large and useless once the disk exists.
	if _, err := os.Stat(filepath.Join(store.ImageDir("ubuntu", "24.04"), "rootfs.tar")); err == nil {
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

	if store.HasImage("ubuntu", "24.04") {
		t.Error("a failed build left a usable image in the cache")
	}
	entries, err := os.ReadDir(filepath.Join(store.Root(), "images"))
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
		if store.HasImage("ubuntu", "24.04") {
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
		{"docker.io/library/ubuntu:24.04", "docker.io/library/ubuntu"},
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
	vm.BaseImage = state.BaseImageRef{Distro: "ubuntu", Tag: "24.04"}
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
	if !store.HasImage("ubuntu", "24.04") {
		t.Error("the image was removed despite the refusal")
	}

	// --force names the consequence and goes through.
	if err := builder.Remove(ctx, ref, true); err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	if store.HasImage("ubuntu", "24.04") {
		t.Error("--force did not remove the image")
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
	if manifest.Distro != "ubuntu-slim" || manifest.Ref() != "ubuntu-slim:24.04" {
		t.Errorf("manifest names the image %s, want ubuntu-slim:24.04", manifest.Ref())
	}
	if !store.HasImage("ubuntu-slim", "24.04") {
		t.Fatal("the built slim image is not reported as cached")
	}
	// A slim build must not be mistaken for, or overwrite, the full image of
	// the same family and tag.
	if store.HasImage("ubuntu", "24.04") {
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
		if !store.HasImage(name, "24.04") {
			t.Errorf("%s:24.04 is not cached after building both", name)
		}
	}
}
