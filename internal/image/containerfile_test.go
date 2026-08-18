package image

import (
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
