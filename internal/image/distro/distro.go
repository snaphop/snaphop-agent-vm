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
	// SlimContainerfile is the recipe for this family's slim variant: the same
	// bootable base and the same common Linux tooling, without the language
	// toolchains, coding agents, browser, Docker, and nested virtualization
	// stack the full recipe installs. It is a second recipe rather than a
	// build argument because the two differ by which blocks run, and podman
	// has no way to skip a block.
	SlimContainerfile string
	// NixContainerfile is the recipe for this family's nix variant: the same
	// bootable base, from the same distro packages, but with the guest tooling
	// installed from the shared templates/distro/agent-tools.nix expression
	// rather than restated in this family's package manager (ADR-0012). It is
	// a separate recipe for the same reason slim is — the blocks differ, and
	// podman has no way to skip a block.
	NixContainerfile string
	// RunnerContainerfile is the slim recipe plus the GitHub Actions
	// self-hosted runner (ADR-0013) and Docker (ADR-0016). The runner section
	// is the same on every family; the copy of the slim recipe is not, so
	// each family has its own file.
	RunnerContainerfile string

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
		Name:                "ubuntu",
		Repo:                "docker.io/library/ubuntu",
		DefaultTag:          "26.04",
		KernelPackage:       "linux-image-virtual",
		Initramfs:           "initramfs-tools",
		Containerfile:       "ubuntu.Containerfile",
		SlimContainerfile:   "ubuntu-slim.Containerfile",
		NixContainerfile:    "ubuntu-nix.Containerfile",
		RunnerContainerfile: "ubuntu-runner.Containerfile",
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
		Name:                "fedora",
		Repo:                "registry.fedoraproject.org/fedora",
		DefaultTag:          "44",
		KernelPackage:       "kernel-core",
		Initramfs:           "dracut",
		Containerfile:       "fedora.Containerfile",
		SlimContainerfile:   "fedora-slim.Containerfile",
		NixContainerfile:    "fedora-nix.Containerfile",
		RunnerContainerfile: "fedora-runner.Containerfile",
		PackageUpdate: []UpdateStep{
			{Name: "upgrading packages", Root: true, Argv: []string{"dnf", "-y", "--refresh", "upgrade"}},
			{Name: "removing packages nothing needs any more", Root: true, Argv: []string{"dnf", "-y", "autoremove"}},
		},
		KernelPattern: "vmlinuz-*",
		InitrdPattern: "initramfs-*.img",
	}
	Arch = Distro{
		Name:                "arch",
		Repo:                "docker.io/library/archlinux",
		DefaultTag:          "base-20260927.0.600689",
		KernelPackage:       "linux",
		Initramfs:           "mkinitcpio",
		Containerfile:       "arch.Containerfile",
		SlimContainerfile:   "arch-slim.Containerfile",
		NixContainerfile:    "arch-nix.Containerfile",
		RunnerContainerfile: "arch-runner.Containerfile",
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
	// miseStore is the one toolchain store every account shares. The
	// Containerfiles create it, and each account's ~/.local/share/mise is a
	// symlink to it. Selector links (latest, a version prefix) live directly
	// inside installs/<tool>/, beside the version directories.
	miseStore = "/usr/local/lib/mise"
)

// rootMiseTmpDir keeps root's mise out of the lock directory the guest user's
// mise needs. mise's npm backend locks each install under $TMPDIR/fslock, and
// that directory belongs to whichever account created it, at mode 0755 — so
// root's upgrade, which runs first here, would otherwise create /tmp/fslock
// and the guest user's upgrade after it would fail with "failed to acquire
// project lock: Permission denied" before installing anything. Base images
// built after this now ship a tmpfiles.d rule that gives /tmp/fslock /tmp's
// own permissions, but VMs created from an older image do not, and an update
// is exactly what they need to run.
//
// The directory is created by the first tooling step, because nothing in a
// guest has it yet. mise self-update writes its download to a file directly
// in $TMPDIR — the self_update crate's tempfile, named .tmpXXXXXX — and does
// not create that directory, so a missing one fails the update with "No such
// file or directory" at $TMPDIR/.tmpXXXXXX before the binary is replaced.
// mise upgrade's npm backend would create $TMPDIR/fslock, but self-update
// runs first.
const rootMiseTmpDir = "/root/.cache/mise-tmp"

// miseMinimumReleaseAge turns off the 24 hours mise otherwise waits before it
// will install a release. 0s is a zero duration, which is the value mise
// treats as no cutoff. A bare 0 is not: mise 2026.10.2's self-update parser
// rejects it ("Invalid date or duration: 0") and stops before replacing the
// binary, and the mise.run installer rejects it the same way. The image also
// writes 0s to /etc/environment, which sshd applies through PAM, but sudo's
// env_reset drops that, and a guest built before the line existed has nothing
// to drop. Naming it here is what makes an update of either account take the
// newest release.
const miseMinimumReleaseAge = "MISE_MINIMUM_RELEASE_AGE=0s"

// bareMiseReleaseAgeLine is the /etc/environment entry written before mise
// started rejecting a bare 0. The rewrite matches that line and no other, so
// a second update does not turn 0s into 0ss.
const bareMiseReleaseAgeLine = "MISE_MINIMUM_RELEASE_AGE=0"
const miseReleaseAgeFile = "/etc/environment"

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
// Not covered: the Playwright browser downloads, which are refreshed by
// `playwright install` rather than by upgrading a package. agy and grok are
// covered: both are mise installs, so `mise upgrade` moves them with the
// other tools in the shared store.
func ToolingUpdate(user string) []UpdateStep {
	steps := []UpdateStep{
		// An image built with the first form of this setting has a bare 0 in
		// /etc/environment. sshd hands that to every session through PAM, and
		// mise self-update then refuses to start. touch first: sed will not
		// edit a path that is not there, and an image from before the line
		// existed may not have the file. The anchored expression leaves a 0s
		// line alone. Skipped with the rest of the mise steps in a guest that
		// has no mise.
		{
			Name:     "preparing mise's release-age setting",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"touch", "--", miseReleaseAgeFile},
		},
		{
			Name:     "correcting mise's release-age setting",
			Root:     true,
			Requires: "mise",
			Argv: []string{
				"sed", "-i", "-e",
				"s/^" + bareMiseReleaseAgeLine + "$/" + miseMinimumReleaseAge + "/",
				"--", miseReleaseAgeFile,
			},
		},
		// Created before either mise invocation. See rootMiseTmpDir: self-update
		// does not create a missing TMPDIR, and this is the path it is given.
		{
			Name:     "preparing mise's temporary directory",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"mkdir", "-p", "--", rootMiseTmpDir},
		},
		// mise first, so the newer binary is the one that resolves and installs
		// everything below it. --yes because an update is unattended; HOME is
		// named explicitly because sudo's env_reset decides it otherwise, and
		// self-update also refreshes the plugins under that home. TMPDIR is
		// named for the reason rootMiseTmpDir documents.
		// MISE_MINIMUM_RELEASE_AGE is named for the reason
		// miseMinimumReleaseAge documents: without it, self-update and the
		// upgrades below skip a release for a day.
		{
			Name:     "updating mise",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"env", "HOME=/root", "TMPDIR=" + rootMiseTmpDir, miseMinimumReleaseAge, "mise", "self-update", "--yes"},
		},
		{
			Name:     "upgrading root's mise-managed tools",
			Root:     true,
			Requires: "mise",
			Argv:     []string{"env", "HOME=/root", "TMPDIR=" + rootMiseTmpDir, miseMinimumReleaseAge, "mise", "upgrade", "--yes"},
		},
	}
	if user != "root" {
		// Root's upgrade just above rewrites the store's selector symlinks
		// (latest, a version prefix such as temurin-25) and owns whatever it
		// recreates. mise 2026.10.3 does that rewrite again for the account
		// that is upgrading, and exits if any unlink fails. The store is
		// sticky, so the guest user cannot remove a link root owns — "Operation
		// not permitted", os error 1 — and the whole update stops after the
		// tools themselves have already moved. Handing the links over, and
		// only the links, lets this account retarget or drop one. find stays
		// at that one directory level, and chown -h changes the symlink rather
		// than the version directory it names, so the sticky bit still stops
		// one account from removing another's install.
		steps = append(steps, UpdateStep{
			Name:     "handing mise's runtime symlinks to " + user,
			Root:     true,
			Requires: "mise",
			Argv: []string{
				"find", miseStore + "/installs",
				"-mindepth", "2", "-maxdepth", "2",
				"-type", "l",
				"-exec", "chown", "-h", user, "{}", "+",
			},
		})
		// Unelevated on purpose: this bumps the account's own mise
		// configuration, and running it through sudo would bump root's twice
		// and leave this account on the versions the image shipped.
		steps = append(steps, UpdateStep{
			Name:     "upgrading " + user + "'s mise-managed tools",
			Requires: "mise",
			Argv:     []string{"env", miseMinimumReleaseAge, "mise", "upgrade", "--yes"},
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

// Variant is which of a family's recipes a base image is built from. Its
// value is the suffix that variant adds to the family name, because that
// suffix is how a variant is named everywhere a base image is named: on the
// command line, in the image cache directory, and in the manifest and vm.json
// records that carry the name back (AGENTS.md §8). "ubuntu", "ubuntu-slim",
// "ubuntu-nix" and "ubuntu-runner" are four base images, not four views of one.
type Variant string

const (
	// Full is the family's complete image: the bootable base plus all the
	// agent tooling its recipe installs.
	Full Variant = ""
	// Slim keeps the bootable base and the common Linux tooling and drops the
	// agent tooling around them.
	Slim Variant = "-slim"
	// Nix keeps the same bootable base, built from the same distro packages,
	// but installs the guest tooling from the shared agent-tools.nix
	// expression instead of from the family's package manager (ADR-0012).
	Nix Variant = "-nix"
	// Runner is the slim image plus the GitHub Actions self-hosted runner,
	// installed unconfigured (ADR-0013).
	Runner Variant = "-runner"
)

// variants is every recipe a family has, in the order image names are listed.
// Full is first because a bare family name means it.
var variants = []Variant{Full, Slim, Nix, Runner}

// LookupImage returns the family and variant a base image name refers to,
// accepting "ubuntu", "ubuntu-slim", "ubuntu-nix" and "ubuntu-runner" alike. It is what reads a
// name back after the fact — a recorded manifest or vm.json — where only the
// image name survives.
func LookupImage(name string) (d Distro, v Variant, ok bool) {
	for _, variant := range variants {
		if variant == Full {
			continue
		}
		if family, found := strings.CutSuffix(name, string(variant)); found {
			d, ok = Lookup(family)
			return d, variant, ok
		}
	}
	d, ok = Lookup(name)
	return d, Full, ok
}

// ImageNames lists every base image name that can be built, families and all
// their variants alike, in a stable order — for help text and completion.
func ImageNames() []string {
	names := make([]string, 0, len(variants)*len(families))
	for _, name := range Names() {
		for _, variant := range variants {
			names = append(names, name+string(variant))
		}
	}
	return names
}

// Ref names one base image: a supported family, a tag of it, and which of the
// family's recipes it is built from.
type Ref struct {
	Distro Distro
	Tag    string
	// Variant selects the family's recipe. Because each variant is a different
	// base image rather than a different way of using one — a different disk,
	// kernel, and manifest — each gets its own name and its own place in the
	// cache, and no two ever share either.
	Variant Variant
}

// ImageName is the name this base image is known by wherever one is named:
// "ubuntu", "ubuntu-slim", "ubuntu-nix", or "ubuntu-runner".
func (r Ref) ImageName() string { return r.Distro.Name + string(r.Variant) }

// Containerfile is the embedded build recipe this base image is built from.
func (r Ref) Containerfile() string {
	switch r.Variant {
	case Slim:
		return r.Distro.SlimContainerfile
	case Nix:
		return r.Distro.NixContainerfile
	case Runner:
		return r.Distro.RunnerContainerfile
	default:
		return r.Distro.Containerfile
	}
}

// String renders the reference as the operator wrote it: "ubuntu:26.04".
func (r Ref) String() string { return r.ImageName() + ":" + r.Tag }

// SourceRef is the OCI reference this base image is built from.
func (r Ref) SourceRef() string { return r.Distro.SourceRef(r.Tag) }

// tagPattern is deliberately stricter than the OCI spec: these values become
// path segments under the state directory, so anything that could traverse or
// collide is refused rather than sanitized.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ParseRef reads "<distro>" or "<distro>:<tag>", applying the family's default
// tag when none is given. A "-slim", "-nix", or "-runner" suffix on the
// distro selects that variant of the family, so "ubuntu-runner:26.04" is the
// runner build of the same tag. An explicit tag, including Ubuntu "24.04",
// Fedora "43", and Arch "base", stays a separate image.
func ParseRef(s string) (Ref, error) {
	name, tag, hasTag := strings.Cut(s, ":")

	d, variant, ok := LookupImage(name)
	if !ok {
		return Ref{}, fmt.Errorf("unsupported distro %q: supported distros are %s (adding one requires an ADR, see docs/decisions/0006-initial-guest-distro-support.md)",
			name, strings.Join(ImageNames(), ", "))
	}
	if !hasTag {
		tag = d.DefaultTag
	}
	if !tagPattern.MatchString(tag) {
		return Ref{}, fmt.Errorf("invalid tag %q for distro %q: tags must match %s", tag, name, tagPattern)
	}
	return Ref{Distro: d, Tag: tag, Variant: variant}, nil
}
