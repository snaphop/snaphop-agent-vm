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
	// family to bring its distro packages up to date, in order. It is followed
	// by ToolingUpdate, which is the same for every family. Each step is an
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

// UpdateStep is one command of an update, with the name the operator sees
// while it runs.
type UpdateStep struct {
	Name string
	Argv []string
	// Root runs the step through sudo. A step that writes only inside the
	// invoking account's home leaves this off, because running it as root
	// would update root's copy of a per-account tool and leave the account
	// that asked for the update untouched.
	Root bool
	// Requires names the command the step needs, when the step applies only to
	// guests that have it. It is probed in the guest first, and a step whose
	// command is absent is skipped rather than failing the update: a VM built
	// from a base image that predates a tool, or from an image an operator
	// built themselves, is not broken — it simply has nothing to update.
	Requires string
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
//   - console=ttyS0 console=ttyAMA0 is what makes the serial console — and
//     therefore console.log, the primary artifact for diagnosing a VM that
//     never became reachable — actually contain the boot. Both are named
//     because the device differs by architecture: x86_64 guests get an 8250 at
//     ttyS0, and the aarch64 virt machine a pl011 at ttyAMA0. A kernel ignores
//     a console it has no driver for, so each architecture uses the one it
//     has, and the same command line boots on both.
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
const KernelCmdline = "root=/dev/vda1 console=ttyS0 console=ttyAMA0 rw memhp_default_state=online_movable"

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
			{Name: "refreshing package lists", Root: true, Argv: []string{"apt-get", "update"}},
			// A guest is unattended, so apt may never stop at a prompt: the
			// frontend is non-interactive and a package whose config file the
			// image changed keeps the version already installed.
			{Name: "upgrading packages", Root: true, Argv: []string{
				"env", "DEBIAN_FRONTEND=noninteractive", "apt-get",
				"-o", "Dpkg::Options::=--force-confdef",
				"-o", "Dpkg::Options::=--force-confold",
				"-y", "dist-upgrade",
			}},
			{Name: "removing packages nothing needs any more", Root: true, Argv: []string{
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
			{Name: "upgrading packages", Root: true, Argv: []string{"dnf", "-y", "--refresh", "upgrade"}},
			{Name: "removing packages nothing needs any more", Root: true, Argv: []string{"dnf", "-y", "autoremove"}},
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
			{Name: "upgrading packages", Root: true, Argv: []string{"pacman", "-Syu", "--noconfirm"}},
		},
		KernelPattern: "vmlinuz-linux",
		InitrdPattern: "initramfs-linux.img",
	}
)

// The paths the base images install shared, root-owned tooling into. They are
// set by the per-distro Containerfiles under templates/distro/, and an update
// has to name them because sudo resets the environment that would otherwise
// carry them: /etc/environment is read by sshd through PAM, not by sudo.
const (
	codexHome  = "/usr/local/lib/codex"
	rustupHome = "/usr/local/rustup"
	cargoHome  = "/usr/local/cargo"
)

// rootMiseTmpDir keeps root's mise out of the lock directory the guest user's
// mise needs. mise's npm backend locks each install under $TMPDIR/fslock, and
// that directory belongs to whichever account created it, at mode 0755 — so
// root's upgrade, which runs first here, would otherwise create /tmp/fslock
// and the guest user's upgrade after it would fail with "failed to acquire
// project lock: Permission denied" before installing anything. Base images
// built after this now ship a tmpfiles.d rule that gives /tmp/fslock /tmp's
// own permissions, but VMs created from an older image do not, and an update
// is exactly what they need to run. mise creates the directory if it is
// missing, so nothing has to exist in the guest beforehand.
const rootMiseTmpDir = "/root/.cache/mise-tmp"

// ToolingUpdate is what `agent-vm update` runs in every guest, whatever its
// family, after that family's PackageUpdate. It covers the software the base
// images install from outside the distro's repositories — which no package
// manager knows about and which therefore stays at the version the image was
// built with unless something updates it explicitly.
//
// user is the account the update connects as. It matters because mise's
// configuration is per account (see templates/distro/mise.sh): the installs
// live in one shared store, but which version each account uses is recorded
// under its own home, and both accounts are in use — an agent supervisor runs
// commands as the guest user, while the boot-time services run as root. So
// both are upgraded, unless the guest user is root and there is only one
// configuration to bump. The second run is cheap: the version the first one
// installed is already in the shared store, and only the config moves.
//
// Not covered: agy, which self-updates in the background and cannot write
// /usr/local/bin as a non-root user, and the Playwright browser downloads,
// which are refreshed by `playwright install` rather than by upgrading a
// package.
func ToolingUpdate(user string) []UpdateStep {
	steps := []UpdateStep{
		// mise first, so the newer binary is the one that resolves and installs
		// everything below it. --yes because an update is unattended; HOME is
		// named explicitly because sudo's env_reset decides it otherwise, and
		// self-update also refreshes the plugins under that home. TMPDIR is
		// named for the reason rootMiseTmpDir documents.
		{
			Name:     "updating mise",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"env", "HOME=/root", "TMPDIR=" + rootMiseTmpDir, "mise", "self-update", "--yes"},
		},
		{
			Name:     "upgrading root's mise-managed tools",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"env", "HOME=/root", "TMPDIR=" + rootMiseTmpDir, "mise", "upgrade", "--yes"},
		},
	}
	if user != "root" {
		// Unelevated on purpose: this bumps the account's own mise
		// configuration, and running it through sudo would bump root's twice
		// and leave this account on the versions the image shipped.
		steps = append(steps, UpdateStep{
			Name:     "upgrading " + user + "'s mise-managed tools",
			Requires: "mise",
			Argv:     []string{"mise", "upgrade", "--yes"},
		})
	}
	return append(steps,
		// codex is installed once into a shared, root-owned CODEX_HOME and
		// symlinked into each account's ~/.codex, so updating it once as root
		// updates it for everyone. `codex update` replaces the standalone
		// package the remote-control daemon starts from.
		UpdateStep{
			Name:     "updating codex",
			Root:     true,
			Requires: "codex",
			Argv:     []string{"env", "CODEX_HOME=" + codexHome, "codex", "update"},
		},
		// The Rust toolchain is shared out of /usr/local (docs/cli.md), which
		// is why this needs root at all; CARGO_HOME is named so the update
		// writes the proxies back where the image put them.
		UpdateStep{
			Name:     "updating the Rust toolchain",
			Root:     true,
			Requires: "rustup",
			Argv: []string{
				"env", "RUSTUP_HOME=" + rustupHome, "CARGO_HOME=" + cargoHome,
				"rustup", "update",
			},
		},
	)
}

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
