package hostsetup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
)

func toolout(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

func TestMatchRelease_AcceptsCapturedUbuntuAndArch(t *testing.T) {
	t.Parallel()
	ubuntu2604 := mustRelease(t, ParseOSRelease(toolout(t, "os-release-ubuntu-26.04.txt")))
	ubuntu2404 := mustRelease(t, ParseOSRelease(toolout(t, "os-release-ubuntu-24.04.txt")))
	arch := mustRelease(t, ParseOSRelease(toolout(t, "os-release-arch.txt")))

	if ubuntu2604.Family != "ubuntu" || ubuntu2604.Version != "26.04" {
		t.Errorf("26.04 fixture matched %+v", ubuntu2604)
	}
	if ubuntu2404.Version != "24.04" {
		t.Errorf("24.04 fixture matched version %q; a point release still reports VERSION_ID=24.04", ubuntu2404.Version)
	}
	if arch.Family != "arch" || arch.Version != "" {
		t.Errorf("Arch fixture matched %+v; a rolling build stamp is not a guest tag", arch)
	}
	if arch.Name != "Arch Linux" {
		t.Errorf("Arch name = %q", arch.Name)
	}
}

func TestMatchRelease_RejectsAnArchDerivative(t *testing.T) {
	t.Parallel()
	// Captured from this Omarchy host. ID_LIKE=arch is not Arch.
	_, err := MatchRelease(ParseOSRelease(toolout(t, "os-release-omarchy.txt")))
	if err == nil {
		t.Fatal("MatchRelease accepted Omarchy")
	}
	var unsupported *UnsupportedOSError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T %v, want *UnsupportedOSError", err, err)
	}
	if !strings.Contains(err.Error(), "Omarchy") || !strings.Contains(err.Error(), "Ubuntu") {
		t.Errorf("error does not name the host and the supported releases:\n%s", err)
	}
}

func TestMatchRelease_RejectsUnsupportedVersionsOfSupportedFamilies(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"ID=ubuntu\nVERSION_ID=22.04\nPRETTY_NAME=\"Ubuntu 22.04 LTS\"\n",
		"ID=ubuntu\nVERSION_ID=25.04\n",
		"ID=fedora\nVERSION_ID=42\n",
		"ID=fedora\nVERSION_ID=45\n",
		"ID=debian\nVERSION_ID=13\nPRETTY_NAME=\"Debian GNU/Linux 13\"\n",
		"ID=rhel\nVERSION_ID=10\n",
		"\n",
	} {
		if _, err := MatchRelease(ParseOSRelease(text)); err == nil {
			t.Errorf("MatchRelease accepted %q", text)
		}
	}
}

func TestMatchRelease_AcceptsFedoraSupportedVersions(t *testing.T) {
	t.Parallel()
	for _, version := range distro.Fedora.SupportedTags {
		got, err := MatchRelease(ParseOSRelease("ID=fedora\nVERSION_ID=" + version + "\n"))
		if err != nil {
			t.Errorf("fedora %s: %v", version, err)
			continue
		}
		if got.Family != "fedora" || got.Version != version {
			t.Errorf("fedora %s matched %+v", version, got)
		}
	}
}

func TestPackages_FollowTheGuestReleasesAndArchitectures(t *testing.T) {
	t.Parallel()
	x86, err := Packages("ubuntu", "26.04", "x86_64", false)
	if err != nil {
		t.Fatal(err)
	}
	arm, err := Packages("ubuntu", "24.04", "aarch64", false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(x86, "qemu-system-arm") || !slices.Contains(x86, "qemu-system-x86") {
		t.Errorf("x86 packages = %v", x86)
	}
	if slices.Contains(arm, "qemu-system-x86") || !slices.Contains(arm, "qemu-system-arm") {
		t.Errorf("arm packages = %v", arm)
	}
	if slices.Contains(x86, "qemu-kvm") || slices.Contains(x86, "dnsmasq") {
		t.Errorf("ubuntu packages name a virtual or conflicting package: %v", x86)
	}
	if !slices.Contains(x86, "dnsmasq-base") || !slices.Contains(x86, "guestfs-tools") {
		t.Errorf("ubuntu packages = %v", x86)
	}

	for _, name := range []string{"ubuntu", "fedora"} {
		d, _ := distro.Lookup(name)
		var first []string
		for i, tag := range d.SupportedTags {
			got, err := Packages(name, tag, "x86_64", false)
			if err != nil {
				t.Errorf("%s %s: %v", name, tag, err)
				continue
			}
			if i == 0 {
				first = got
				continue
			}
			if !slices.Equal(got, first) {
				t.Errorf("%s %s packages = %v, want %v", name, tag, got, first)
			}
		}
	}

	if _, err := Packages("ubuntu", "22.04", "x86_64", false); err == nil {
		t.Error("Packages accepted Ubuntu 22.04")
	}
	if _, err := Packages("fedora", "42", "x86_64", false); err == nil {
		t.Error("Packages accepted Fedora 42")
	}
	if _, err := Packages("ubuntu", "26.04", "ppc64le", false); err == nil {
		t.Error("Packages accepted ppc64le")
	}
}

func TestPackages_GitHubPackageNameDiffersOnArch(t *testing.T) {
	t.Parallel()
	ubuntu, err := Packages("ubuntu", "26.04", "x86_64", true)
	if err != nil {
		t.Fatal(err)
	}
	arch, err := Packages("arch", "", "x86_64", true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ubuntu, "gh") || slices.Contains(ubuntu, "github-cli") {
		t.Errorf("ubuntu github packages = %v", ubuntu)
	}
	if !slices.Contains(arch, "github-cli") || slices.Contains(arch, "gh") {
		t.Errorf("arch github packages = %v", arch)
	}
}

func TestInstallArgv_DoesNotUpgradeArchOrUseAShell(t *testing.T) {
	t.Parallel()
	archPkgs, err := Packages("arch", "", "x86_64", false)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := installArgv("arch", archPkgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0][0] != "pacman" || !slices.Contains(steps[0], "--needed") || slices.Contains(steps[0], "-Syu") {
		t.Errorf("arch install = %v", steps)
	}
	for _, word := range steps[0] {
		if strings.ContainsAny(word, "|&;<>$`") {
			t.Errorf("arch install contains a shell metacharacter: %q", word)
		}
	}

	ubuntuPkgs, err := Packages("ubuntu", "26.04", "amd64", false)
	if err != nil {
		t.Fatal(err)
	}
	usteps, err := installArgv("ubuntu", ubuntuPkgs)
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(usteps[1], " ")
	if !strings.Contains(flat, "DEBIAN_FRONTEND=noninteractive") || !strings.Contains(flat, "--no-install-recommends") {
		t.Errorf("ubuntu install = %v", usteps)
	}
	if slices.Contains(ubuntuPkgs, "qemu-system-arm") {
		t.Errorf("amd64 was not treated as x86_64: %v", ubuntuPkgs)
	}
}

func mustRelease(t *testing.T, fields map[string]string) Release {
	t.Helper()
	rel, err := MatchRelease(fields)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}
