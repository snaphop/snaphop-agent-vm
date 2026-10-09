package hostsetup

import (
	"fmt"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
)

// packageTimeout is how long a package install may run. libguestfs and podman
// are large, and a cold mirror is slower than a warm one.
const packageTimeout = 30 * time.Minute

// QEMUUserCandidates are the account names distributions use for the QEMU
// process when qemu.conf does not set one. Debian and Ubuntu use
// libvirt-qemu, Fedora and RHEL use qemu, and some hosts fall back to nobody.
// doctor probes the same names; a test keeps the two lists equal.
var QEMUUserCandidates = []string{"libvirt-qemu", "qemu", "nobody"}

// Packages is what setup installs on a supported release. Ubuntu names the
// concrete QEMU package for the architecture: on 26.04 qemu-kvm is a virtual
// package with two providers, and apt will not choose. The same concrete
// package is a real package on 24.04. Both Ubuntu releases, and both Fedora
// releases, install the same set.
func Packages(family, version, arch string, github bool) ([]string, error) {
	canonical, err := normalizeArch(arch)
	if err != nil {
		return nil, err
	}
	switch family {
	case distro.Ubuntu.Name:
		if !supportedVersion(family, version) {
			return nil, fmt.Errorf("ubuntu %s is not a supported release", version)
		}
		qemu, err := ubuntuQEMU(canonical)
		if err != nil {
			return nil, err
		}
		return withGitHub(github, "gh", []string{
			qemu,
			"qemu-utils",
			"libvirt-daemon-system",
			"libvirt-clients",
			"virtinst",
			// dnsmasq-base rather than dnsmasq: libvirt starts its own dnsmasq
			// per network, and the full package would also enable a resolver
			// on port 53.
			"dnsmasq-base",
			"guestfs-tools",
			"podman",
			"iproute2",
			"openssh-client",
			"acl",
		}), nil
	case distro.Fedora.Name:
		if !supportedVersion(family, version) {
			return nil, fmt.Errorf("fedora %s is not a supported release", version)
		}
		return withGitHub(github, "gh", []string{
			"qemu-kvm",
			"qemu-img",
			"libvirt",
			"libvirt-client",
			"virt-install",
			"dnsmasq",
			"guestfs-tools",
			"podman",
			"iproute",
			"openssh-clients",
			"acl",
		}), nil
	case distro.Arch.Name:
		return withGitHub(github, "github-cli", []string{
			"qemu-base",
			"qemu-img",
			"libvirt",
			"virt-install",
			"iptables-nft",
			"dnsmasq",
			"guestfs-tools",
			"podman",
			"iproute2",
			"openssh",
			"acl",
		}), nil
	default:
		return nil, fmt.Errorf("no host packages for %s", family)
	}
}

// installArgv is the package-manager invocations for one family, in order.
// Each element is a complete argument vector. Nothing here is a shell.
func installArgv(family string, packages []string) ([][]string, error) {
	switch family {
	case distro.Ubuntu.Name:
		// env sets DEBIAN_FRONTEND for apt-get. sudo clears the environment,
		// so the variable has to be an argument rather than an inherited one,
		// or apt stops at a prompt on an unattended host.
		front := []string{"env", "DEBIAN_FRONTEND=noninteractive"}
		update := append(append([]string{}, front...), "apt-get", "update")
		install := append(append([]string{}, front...), "apt-get", "install", "-y", "--no-install-recommends")
		install = append(install, packages...)
		return [][]string{update, install}, nil
	case distro.Fedora.Name:
		return [][]string{append([]string{"dnf", "-y", "install"}, packages...)}, nil
	case distro.Arch.Name:
		// -Sy installs these packages. -Syu would upgrade every other package
		// on the host, which setup was not asked to do. --needed leaves an
		// already-installed package alone.
		return [][]string{append([]string{"pacman", "-Sy", "--noconfirm", "--needed"}, packages...)}, nil
	default:
		return nil, fmt.Errorf("no package manager for %s", family)
	}
}

func withGitHub(github bool, pkg string, packages []string) []string {
	if github {
		packages = append(packages, pkg)
	}
	return packages
}

func ubuntuQEMU(arch string) (string, error) {
	switch arch {
	case "x86_64":
		return "qemu-system-x86", nil
	case "aarch64":
		return "qemu-system-arm", nil
	default:
		return "", &UnsupportedArchError{Arch: arch}
	}
}

// normalizeArch accepts the names uname -m and the container build both use.
func normalizeArch(arch string) (string, error) {
	switch arch {
	case "x86_64", "amd64":
		return "x86_64", nil
	case "aarch64", "arm64":
		return "aarch64", nil
	default:
		return "", &UnsupportedArchError{Arch: arch}
	}
}
