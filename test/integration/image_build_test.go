//go:build integration

// Package integration holds the tests that need real host tools: podman,
// libguestfs, and — for the VM lifecycle tests — /dev/kvm and a running
// libvirt. They are guarded by the `integration` build tag and are not part of
// `go test ./...`.
//
//	go test -tags integration ./test/integration/...
//
// These tests pull from a container registry and write real disk images. They
// use their own state directory and the agent-vm-test- prefix, and they never
// touch domains, networks, or volumes they did not create.
package integration

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// distros selects which images to build. Building all three takes tens of
// minutes and several gigabytes, so a run can narrow it, and a slim variant is
// named the way the command line names it:
//
//	go test -tags integration ./test/integration/... -distros=ubuntu
//	go test -tags integration ./test/integration/... -distros=ubuntu-slim
//	go test -tags integration ./test/integration/... -distros=ubuntu-nix
//
// The default is the three full images. The slim and nix variants are separate
// base images with their own recipes, so building one proves nothing about the
// others and each has to be named to be covered.
var distros = flag.String("distros", "ubuntu,fedora,arch", "comma-separated base images to build, e.g. ubuntu,fedora-slim,arch-nix")

// requireTools skips the test when a tool it needs is absent, rather than
// failing: a developer without libguestfs installed has not broken anything.
func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; skipping the integration test", tool)
		}
	}
}

func newBuilder(t *testing.T) (*image.Builder, *state.Store) {
	t.Helper()

	// A dedicated state directory: the integration suite must never be pointed
	// at one holding images or VMs someone cares about.
	store, err := state.Open(filepath.Join(t.TempDir(), "agent-vm-test-state"))
	if err != nil {
		t.Fatalf("opening the state directory: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	runner := hostexec.New(logger)

	return &image.Builder{
		Runner:         runner,
		Versions:       hostexec.NewVersions(runner),
		Store:          store,
		Logger:         logger,
		AgentVMVersion: "integration-test",
	}, store
}

// TestImageBuild_ProducesABootableBaseImage builds a real base image for every
// selected distro and checks the artifacts a VM would boot from.
//
// ADR-0006 is explicit that a distro whose base image cannot be built is not
// supported, regardless of whether code for it exists — this is the test that
// makes that claim true.
func TestImageBuild_ProducesABootableBaseImage(t *testing.T) {
	requireTools(t, "podman", "virt-make-fs", "virt-ls", "virt-copy-out", "virt-sysprep")

	for _, name := range strings.Split(*distros, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			ref, err := distro.ParseRef(name)
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", name, err)
			}
			builder, store := newBuilder(t)

			// Pulling a distro image and installing a kernel takes minutes.
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()

			manifest, err := builder.Build(ctx, image.BuildOptions{Ref: ref})
			if err != nil {
				t.Fatalf("building %s: %v", ref, err)
			}

			if !strings.HasPrefix(manifest.SourceDigest, "sha256:") {
				t.Errorf("SourceDigest = %q, want a pinned digest", manifest.SourceDigest)
			}
			if manifest.KernelVersion == "" {
				t.Error("the manifest records no kernel version")
			}
			if manifest.KernelCmdline != distro.KernelCmdline {
				t.Errorf("KernelCmdline = %q, want %q", manifest.KernelCmdline, distro.KernelCmdline)
			}

			// A kernel and initramfs of a plausible size: an empty or truncated
			// artifact would produce a VM that fails to boot with no obvious
			// cause.
			for _, artifact := range []struct {
				path    string
				minimum int64
			}{
				{store.BaseDiskPath(ref.ImageName(), ref.Tag), 200 << 20},
				{store.KernelPath(ref.ImageName(), ref.Tag), 1 << 20},
				{store.InitrdPath(ref.ImageName(), ref.Tag), 1 << 20},
			} {
				info, err := os.Stat(artifact.path)
				if err != nil {
					t.Errorf("missing artifact %s: %v", filepath.Base(artifact.path), err)
					continue
				}
				if info.Size() < artifact.minimum {
					t.Errorf("%s is %d bytes, which is too small to be real", filepath.Base(artifact.path), info.Size())
				}
			}

			assertGuestContract(t, store.BaseDiskPath(ref.ImageName(), ref.Tag))
		})
	}
}

// assertGuestContract checks the promises a base image makes to every VM built
// on it: the services that must be enabled, and the identity that must have
// been stripped.
func assertGuestContract(t *testing.T, diskPath string) {
	t.Helper()

	// sshd is the guest contract: agent-vm ssh, and the boot wait during
	// create, both depend on it starting by itself.
	wants := virtLs(t, diskPath, "/etc/systemd/system/multi-user.target.wants")
	if !containsAny(wants, "sshd.service", "ssh.service") {
		t.Errorf("no ssh unit is enabled in the image; a VM built on it would never become reachable.\n  enabled: %v", wants)
	}
	if !contains(wants, "systemd-networkd.service") {
		t.Errorf("systemd-networkd is not enabled; the guest would not configure its network.\n  enabled: %v", wants)
	}

	// cloud-init applies the SSH key and hostname, so its units have to be
	// enabled — but which units exist depends on the version. 24.3 renamed
	// cloud-init.service to cloud-init-network.service and added
	// cloud-init-main.service, and the three families ship different versions.
	cloudInit := virtLs(t, diskPath, "/etc/systemd/system/cloud-init.target.wants")
	for _, unit := range []string{"cloud-init-local.service", "cloud-config.service", "cloud-final.service"} {
		if !contains(cloudInit, unit) {
			t.Errorf("%s is not enabled; the SSH key and hostname would never be applied.\n  enabled: %v", unit, cloudInit)
		}
	}
	if !containsAny(cloudInit, "cloud-init.service", "cloud-init-network.service", "cloud-init-main.service") {
		t.Errorf("no cloud-init network stage is enabled under either the old or the new unit naming.\n  enabled: %v", cloudInit)
	}

	// A shared base image must not carry an identity: duplicated machine IDs
	// and SSH host keys across guests are exactly what virt-sysprep removes.
	for _, entry := range virtLs(t, diskPath, "/etc/ssh") {
		if strings.HasPrefix(entry, "ssh_host_") {
			t.Errorf("the base image contains the SSH host key %s; every VM built on it would share one identity", entry)
		}
	}
	if size := virtFileSize(t, diskPath, "/etc/machine-id"); size > 0 {
		t.Errorf("/etc/machine-id is %d bytes, want empty so systemd regenerates it per VM", size)
	}
}

func virtLs(t *testing.T, diskPath, dir string) []string {
	t.Helper()
	out, err := exec.Command("virt-ls", "-a", diskPath, dir).Output()
	if err != nil {
		t.Errorf("virt-ls %s: %v", dir, err)
		return nil
	}
	entries := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if entry := strings.TrimSpace(line); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// virtFileSize returns the size of a file inside the image, or -1 if it is
// absent — which for machine-id is an acceptable outcome, since systemd
// creates it at boot.
//
// virt-ls has no machine-readable mode, so this parses `ls -l` style output:
// permissions, link count, owner, group, size, then the name.
func virtFileSize(t *testing.T, diskPath, path string) int64 {
	t.Helper()
	out, err := exec.Command("virt-ls", "-l", "-a", diskPath, filepath.Dir(path)).Output()
	if err != nil {
		return -1
	}

	name := filepath.Base(path)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[len(fields)-1] != name {
			continue
		}
		size, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			t.Errorf("could not read the size of %s from virt-ls output: %q", path, line)
			return -1
		}
		return size
	}
	return -1
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsAny(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		if contains(haystack, needle) {
			return true
		}
	}
	return false
}
