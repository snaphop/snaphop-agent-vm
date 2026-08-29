// Package distro holds the distro families agent-vm supports and the source
// images they are built from. It is the single place distro-specific knowledge
// lives (ADR-0006) — there are no `switch distro` blocks elsewhere, and
// everything a Containerfile can express belongs in templates/distro/ instead.
//
// Adding a tag within a supported family is routine. Adding a family requires
// an ADR, because it commits the project to ongoing integration coverage.
package distro

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Distro is a supported guest distro family.
type Distro struct {
	// Name is the family as named on the command line.
	Name string
	// Repo is the OCI repository its base images come from, without a tag.
	Repo string
	// DefaultTag is the tag used when the operator names no tag.
	DefaultTag string
	// KernelPackage and Initramfs document what the family's Containerfile
	// installs, so `image inspect` and docs can state it without reading the
	// build recipe.
	KernelPackage string
	Initramfs     string

	// Containerfile is the embedded build recipe under templates/distro/.
	// Everything a Containerfile can express belongs there rather than here.
	Containerfile string

	// PackageUpdate is what `agent-vm update` runs inside a guest of this
	// family to bring its packages up to date, in order. Each step is an
	// explicit argument vector run over ssh — no shell, so nothing here may
	// rely on a pipeline, a redirection, or an expansion.
	PackageUpdate []UpdateStep

	// KernelPattern and InitrdPattern match the artifacts to extract from
	// /boot after the build. They differ per family because each one names its
	// initramfs differently, and there is no way to express "find the kernel"
	// in a Containerfile.
	KernelPattern string
	InitrdPattern string
}

// UpdateStep is one command of a package update, with the name the operator
// sees while it runs.
type UpdateStep struct {
	Name string
	Argv []string
}

// ModulesDir is where every supported family keeps its kernel modules. The
// directory name under it is the kernel version, which is more reliable than
// parsing it out of a kernel filename.
const ModulesDir = "/usr/lib/modules"

// KernelCmdline is the command line every guest boots with. Under direct
// kernel boot (ADR-0004) this lives on the host, not in the guest, so it is
// recorded in each base image's manifest and is part of the public contract.
//
//   - root=/dev/vda1 matches the single partition virt-make-fs --partition
//     writes; the overlay is attached as the first virtio disk.
//   - console=ttyS0 is what makes the serial console — and therefore
//     console.log, the primary artifact for diagnosing a VM that never became
//     reachable — actually contain the boot.
//   - memhp_default_state=online_movable brings hotplugged memory blocks
//     online as they arrive, which is what makes a virtio-mem device (see
//     --max-memory) show up as usable RAM instead of offline blocks nobody
//     onlined. It costs a guest without such a device nothing, since no memory
//     is ever hotplugged into one.
//
// Changing this string changes what newly built base images record. Images
// already in the cache keep the command line they were built with — a VM boots
// the cmdline from its own image's manifest — so growing memory on a VM
// created from an older image needs that image rebuilt.
const KernelCmdline = "root=/dev/vda1 console=ttyS0 rw memhp_default_state=online_movable"

// SourceRef is the OCI reference for a tag of this family. It is a starting
// point only: a build resolves it to a digest and pins that (SECURITY.md,
// "Image Provenance").
func (d Distro) SourceRef(tag string) string {
	return d.Repo + ":" + tag
}

// The supported families, from ADR-0006. Keep in sync with that ADR and with
// docs/architecture.md's runtime profile table.
var (
	Ubuntu = Distro{
		Name:          "ubuntu",
		Repo:          "docker.io/library/ubuntu",
		DefaultTag:    "24.04",
		KernelPackage: "linux-image-virtual",
		Initramfs:     "initramfs-tools",
		Containerfile: "ubuntu.Containerfile",
		PackageUpdate: []UpdateStep{
			{Name: "refreshing package lists", Argv: []string{"apt-get", "update"}},
			// A guest is unattended, so apt may never stop at a prompt: the
			// frontend is non-interactive and a package whose config file the
			// image changed keeps the version already installed.
			{Name: "upgrading packages", Argv: []string{
				"env", "DEBIAN_FRONTEND=noninteractive", "apt-get",
				"-o", "Dpkg::Options::=--force-confdef",
				"-o", "Dpkg::Options::=--force-confold",
				"-y", "dist-upgrade",
			}},
			{Name: "removing packages nothing needs any more", Argv: []string{
				"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "--purge", "autoremove",
			}},
		},
		KernelPattern: "vmlinuz-*",
		InitrdPattern: "initrd.img-*",
	}
	Fedora = Distro{
		Name:          "fedora",
		Repo:          "registry.fedoraproject.org/fedora",
		DefaultTag:    "42",
		KernelPackage: "kernel-core",
		Initramfs:     "dracut",
		Containerfile: "fedora.Containerfile",
		PackageUpdate: []UpdateStep{
			{Name: "upgrading packages", Argv: []string{"dnf", "-y", "--refresh", "upgrade"}},
			{Name: "removing packages nothing needs any more", Argv: []string{"dnf", "-y", "autoremove"}},
		},
		KernelPattern: "vmlinuz-*",
		InitrdPattern: "initramfs-*.img",
	}
	Arch = Distro{
		Name:          "arch",
		Repo:          "docker.io/library/archlinux",
		DefaultTag:    "base",
		KernelPackage: "linux",
		Initramfs:     "mkinitcpio",
		Containerfile: "arch.Containerfile",
		// pacman has no separate refresh step and no autoremove: -Syu does the
		// whole upgrade, and removing orphans needs a pipeline we cannot run
		// without a shell.
		PackageUpdate: []UpdateStep{
			{Name: "upgrading packages", Argv: []string{"pacman", "-Syu", "--noconfirm"}},
		},
		KernelPattern: "vmlinuz-linux",
		InitrdPattern: "initramfs-linux.img",
	}
)

// Default is the family used when the operator names none.
var Default = Ubuntu

var families = map[string]Distro{
	Ubuntu.Name: Ubuntu,
	Fedora.Name: Fedora,
	Arch.Name:   Arch,
}

// Names lists the supported families in a stable order, for help text and
// error messages.
func Names() []string {
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup returns a family by name.
func Lookup(name string) (Distro, bool) {
	d, ok := families[name]
	return d, ok
}

// Ref names one base image: a supported family and a tag of it.
type Ref struct {
	Distro Distro
	Tag    string
}

// String renders the reference as the operator wrote it: "ubuntu:24.04".
func (r Ref) String() string { return r.Distro.Name + ":" + r.Tag }

// SourceRef is the OCI reference this base image is built from.
func (r Ref) SourceRef() string { return r.Distro.SourceRef(r.Tag) }

// tagPattern is deliberately stricter than the OCI spec: these values become
// path segments under the state directory, so anything that could traverse or
// collide is refused rather than sanitized.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ParseRef reads "<distro>" or "<distro>:<tag>", applying the family's default
// tag when none is given.
func ParseRef(s string) (Ref, error) {
	name, tag, hasTag := strings.Cut(s, ":")

	d, ok := Lookup(name)
	if !ok {
		return Ref{}, fmt.Errorf("unsupported distro %q: supported distros are %s (adding one requires an ADR, see docs/decisions/0006-initial-guest-distro-support.md)",
			name, strings.Join(Names(), ", "))
	}
	if !hasTag {
		tag = d.DefaultTag
	}
	if !tagPattern.MatchString(tag) {
		return Ref{}, fmt.Errorf("invalid tag %q for distro %q: tags must match %s", tag, name, tagPattern)
	}
	return Ref{Distro: d, Tag: tag}, nil
}
