package image

import (
	"slices"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/templates"
)

// TestContainerfiles_ForceTheResolvConfSymlink guards the fix for guests that
// booted with working networking and no working DNS.
//
// podman bind-mounts /etc/resolv.conf for the duration of every RUN, so
// systemd-resolved's packaging never gets to install its symlink and the empty
// regular file from the OCI base is what lands in the image. systemd's vendor
// rule is an `L`, which will not replace an existing path, so the guest boots
// with an empty resolv.conf and every name lookup fails while the lease, the
// route and resolved itself all look healthy. Only the forcing form (`L+`)
// repairs it, and it has to be there for every family.
func TestContainerfiles_ForceTheResolvConfSymlink(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// The rule must both point at resolved's stub and use the forcing
		// form; an `L` here would silently reproduce the original bug.
		if !strings.Contains(recipe, "L+! /etc/resolv.conf - - - - ../run/systemd/resolve/stub-resolv.conf") {
			t.Errorf("%s does not force /etc/resolv.conf to systemd-resolved's stub with an `L+` tmpfiles rule; guests built from it will have no DNS", d.Containerfile)
		}

		// Writing it anywhere but /etc/tmpfiles.d/systemd-resolve.conf leaves
		// the vendor `L` rule in place and in conflict with ours.
		if !strings.Contains(recipe, "/etc/tmpfiles.d/systemd-resolve.conf") {
			t.Errorf("%s does not write the rule to /etc/tmpfiles.d/systemd-resolve.conf, so it does not mask systemd's own `L` rule for the same path", d.Containerfile)
		}

		// The stub is only answered while resolved is running.
		if !strings.Contains(recipe, "systemd-resolved.service") {
			t.Errorf("%s points resolv.conf at systemd-resolved's stub without enabling systemd-resolved.service", d.Containerfile)
		}
	}
}

// TestUbuntuContainerfile_InstallsLsbRelease guards a guest whose apt sources
// all pointed at a suite named "UNAVAILABLE".
//
// cloud-init's apt module rewrites the Ubuntu sources on first boot and asks
// the lsb_release command for the codename to write. cloud-init only
// *recommends* the package that provides it, so --no-install-recommends leaves
// it out and cloud-init falls back to the literal string UNAVAILABLE — every
// repository then 404s. The recipe has to name it explicitly.
func TestUbuntuContainerfile_InstallsLsbRelease(t *testing.T) {
	contents, err := templates.FS.ReadFile("distro/" + distro.Ubuntu.Containerfile)
	if err != nil {
		t.Fatalf("reading %s: %v", distro.Ubuntu.Containerfile, err)
	}
	recipe := string(contents)

	// Only meaningful while the recipe suppresses recommends; if that ever
	// changes the dependency comes back on its own and this test should be
	// revisited rather than silently passing for the wrong reason.
	if !strings.Contains(recipe, "--no-install-recommends") {
		t.Skip("ubuntu.Containerfile no longer uses --no-install-recommends; lsb-release arrives as a recommended dependency again")
	}
	if !strings.Contains(recipe, "lsb-release") {
		t.Errorf("%s installs cloud-init with --no-install-recommends but does not install lsb-release; cloud-init will write \"UNAVAILABLE\" as the suite in every guest's apt sources", distro.Ubuntu.Containerfile)
	}
}

// TestContainerfiles_InstallCommonGuestTooling guards the guest contract
// documented under "Guest tooling" in docs/cli.md: an agent that lands in a VM
// finds the ordinary tools already there instead of having to install them,
// which is only true if every family's recipe installs them.
//
// The package names differ per family, so this checks the name each family
// actually uses; a family that grows a fourth spelling belongs in this table
// rather than in a loosened assertion.
func TestContainerfiles_InstallCommonGuestTooling(t *testing.T) {
	packages := map[string][]string{
		distro.Ubuntu.Containerfile: {
			"iputils-ping", "curl", "wget", "git", "build-essential",
			"python3", "jq", "docker.io", "docker-compose-v2",
		},
		distro.Fedora.Containerfile: {
			"iputils", "curl", "wget", "git", "gcc", "make",
			"python3", "jq", "moby-engine", "docker-compose",
		},
		distro.Arch.Containerfile: {
			"iputils", "curl", "wget", "git", "base-devel",
			"python", "jq", "docker", "docker-compose",
		},
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		wanted, ok := packages[d.Containerfile]
		if !ok {
			t.Fatalf("%s has no expected package list here; a new family must state the tooling it installs", d.Containerfile)
		}
		for _, pkg := range wanted {
			if !strings.Contains(recipe, pkg) {
				t.Errorf("%s does not install %q; guests built from it will be missing tooling docs/cli.md promises", d.Containerfile, pkg)
			}
		}

		// A Docker daemon that is installed but never started is the same
		// thing as no Docker at all from inside the guest.
		if !strings.Contains(recipe, "enable docker.service") {
			t.Errorf("%s installs Docker without enabling docker.service; the daemon will not be running when the VM becomes reachable", d.Containerfile)
		}

		// The login user only exists after cloud-init has run, so socket
		// access is granted by the unit the image ships rather than by the
		// generated user-data — which must keep working against base images
		// built before Docker was added.
		if !strings.Contains(recipe, "enable agent-vm-docker-group.service") {
			t.Errorf("%s does not enable agent-vm-docker-group.service; the login user will need sudo for every docker command", d.Containerfile)
		}
	}
}

// TestContainerfiles_InstallTheGuestTmuxConfig guards the tmux configuration
// every guest is supposed to come up with.
//
// tmux is only useful to an agent if it behaves the same way in every VM, so
// the configuration ships in the base image rather than being pasted in by
// hand per VM. It has to reach two places: /etc/skel, which useradd copies
// into the login user cloud-init creates, and /root, whose home directory
// already exists by then and so never consults skel.
func TestContainerfiles_InstallTheGuestTmuxConfig(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// The file has to be installed by the same family that installs tmux;
		// a configuration without the program is nothing.
		if !strings.Contains(recipe, "tmux") {
			t.Errorf("%s does not install tmux", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY tmux.conf /etc/skel/.tmux.conf") {
			t.Errorf("%s does not copy tmux.conf into /etc/skel; the login user cloud-init creates will have no tmux configuration", d.Containerfile)
		}
		if !strings.Contains(recipe, "/root/.tmux.conf") {
			t.Errorf("%s does not install tmux.conf for root; skel is only consulted when a home directory is created and /root already exists", d.Containerfile)
		}
	}
}

// TestTmuxConfig_IsShippedInTheBuildContext ties the COPY above to the file the
// builder actually writes next to the Containerfile. A COPY of a file that is
// not in the build context fails the build minutes in, after the package
// installation has already been paid for.
func TestTmuxConfig_IsShippedInTheBuildContext(t *testing.T) {
	if !slices.Contains(buildContextFiles, "tmux.conf") {
		t.Fatalf("tmux.conf is not in buildContextFiles %v, so podman's build context will not contain it", buildContextFiles)
	}
	for _, name := range buildContextFiles {
		contents, err := templates.FS.ReadFile("distro/" + name)
		if err != nil {
			t.Fatalf("build context file %q is not embedded: %v", name, err)
		}
		if len(contents) == 0 {
			t.Errorf("embedded build context file %q is empty", name)
		}
	}
}
