# 12. Nix-provided guest tooling as a third image variant

Date: 2026-09-10

## Status

Accepted

Extends [ADR-0006](./0006-initial-guest-distro-support.md) (initial guest distro
support) with a variant rather than a family, and is bounded by
[ADR-0003](./0003-oci-container-images-as-vm-root-disks.md) (OCI container
images as VM root disks), which is what fixes the boot layer this decision
leaves alone.

See also [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md), which
adds a self-hosted GitHub Actions runner variant of each family's slim image.

## Context

A base image is two layers with nothing in common but a filesystem.

The **boot layer** is what makes a container image bootable and reachable as a
VM: a kernel and initramfs, an init system, cloud-init, sshd, sudo, chrony and
the guest agent. It is small, it is distro-specific by nature, and it is the
guest contract `docs/cli.md` promises. Nothing about it is under discussion
here.

The **tooling layer** is everything else — the CLI tools an agent expects on a
working machine, the language toolchains, the coding agents, the browser — and
it is most of the build time and almost all of the image. Today it is stated
four times: once in `apt`, once in `dnf`, once in `pacman`, and once more in
mise for the toolchains. `templates/distro/ubuntu.Containerfile` and its two
siblings are about 59 KB each, and the great majority of that is the same tool
set spelled in three dialects, kept in step by hand and by the contract tests in
`internal/image/containerfile_test.go`.

That is a real maintenance cost with a real failure mode: a tool added to one
family and forgotten in another produces three base images that claim the same
guest contract and do not keep it, and the difference only surfaces inside a VM
long after the image was built.

Nix states a tool set once, distro-independently, and resolves it from a single
pinned tree. It is the obvious answer to the duplication — but it is not an
answer to the boot layer, because a nix profile is a directory of packages, not
a bootable operating system, and replacing the boot layer with it would mean
building NixOS instead.

## Decision

Add a **nix variant** of every supported family: `ubuntu-nix`, `fedora-nix`,
`arch-nix`, alongside the existing `-slim` variant.

- **Nix replaces the tooling layer only.** The kernel, initramfs, init,
  cloud-init, sshd, sudo, chrony and qemu-guest-agent still come from the
  family's package manager, and those blocks are word for word the ones the
  full and slim recipes carry.
  `TestNixContainerfiles_KeepTheBootAndCloudInitContract` enforces it.
- **The tool set is one shared expression**, `templates/distro/agent-tools.nix`,
  embedded in the binary and written into the build context like the guest
  dotfiles. All three nix recipes build the same expression; they differ only in
  the packages that make the image boot.
- **A variant, not a family.** `ubuntu-nix` is a base image of its own — its own
  cache directory, manifest, and name in `image list` and `vm.json` — reusing
  the mechanism `-slim` already established (`distro.Variant`). It is not a
  fourth distro family, so it commits the project to no new source image and no
  new boot path.
- **Multi-user nix in the guest.** The installer runs single-user during
  `podman build`, because there is no systemd there to start `nix-daemon` with.
  The rest of a multi-user install — a build-user group, a `nix.conf`, and the
  two unit files the nix package already ships — is assembled in the recipe and
  enabled offline, so an unprivileged account in the guest can install packages
  without paying for it at first boot.

Three deliberate exceptions, documented in the recipes:

- **Docker and the nested virtualization stack stay on the distro's package
  manager.** Both are system daemons with kernel-side state and distro-owned
  units. Nix can supply the binaries but not a running, socket-activated service
  integrated with the guest's init, and a nix guest that quietly lost `docker`
  and `libvirtd` would not keep the contract `docs/cli.md` states.
  `TestNixContainerfiles_KeepTheDaemonsNixCannotSupply` enforces it.
- **codex comes from OpenAI's installer**, exactly as it does in the full
  recipe. `codex remote-control` runs only against the standalone package that
  installer lays down.
- **mise survives for the tools nixpkgs does not package: `pi`, `herdr`,
  `agy`, and `grok`.** `pi`, `herdr` and `agy` come from its registry (`agy`
  resolves to `aqua:google-antigravity/antigravity-cli`); `grok` comes from
  its npm backend (`npm:@xai-official/grok`), which is the only place that
  package is published. herdr is not optional: the image starts a server for
  every account at boot, so an image without it would come up with a failed
  unit every time. mise installs no toolchain and no runtime — node, which
  the npm backend needs, comes from the nix profile — so the "which copy of
  Go am I running" ambiguity this variant removes does not come back with it.
  `TestNixContainerfiles_LeaveTheToolingToTheNixFile` enforces that the list
  is exactly those four.

## Consequences

**The boot contract is unchanged.** A nix guest has the same coding agents as
a full guest — `claude`, `codex`, `opencode`, `pi`, `agy`, and `grok` — and
the per-account herdr servers, while tooling sources and interfaces differ as
described below. That is what the narrow mise exception buys, and it is why
the exception exists: the alternative was an image whose boot always carried a
failed unit.

**Reproducibility is better here than anywhere else in the project, but not
yet complete.** `agent-tools.nix` ships following a nixpkgs release branch,
which moves. `scripts/pin-nixpkgs.sh` resolves it to a commit revision and its
`sha256`; once run, the nixpkgs-provided packages use the same versions for a
given architecture, which is a stronger guarantee than the full recipes can
make — there the source digest is pinned but every package and tool version
floats (ADR-0006). The pin does not cover distro packages, the Nix installer,
Codex, or mise-managed tools. Until it is run, the nixpkgs package versions
also float.

**Updating a nix guest means rebuilding its image.** `agent-vm update`
refreshes the distro packages as it does for any guest, and upgrades pi,
herdr, agy, and grok through mise, which is present. Its codex step runs,
because codex is installed the same way here. Its rustup step is skipped,
because a nix image ships `rustc` and `cargo` from nixpkgs and no `rustup` for
the `Requires` probe to find — which is deliberate: that step is hard-coded to
the shared `/usr/local/rustup` the full recipe creates, and a rustup in a nix
guest would point it at a directory that does not exist. What the nix profile
holds is fixed by the expression the image was built from, so changing it
requires rebuilding the `agent-vm` binary with the edited expression, then
rebuilding the base image with `image build --force` after destroying
dependent VMs. Create replacement VMs from that image.

**Rust has no `rustup` in a nix guest.** `rustc`, `cargo`, `rustfmt` and
`clippy` come from the pinned tree like every other toolchain, so `cargo build`
and `cargo clippy` work and `rustup toolchain install nightly` does not. A guest
that needs a second toolchain has `nix` for it. This is the only toolchain whose
interface differs from the full image, and it is stated in `docs/cli.md`.

**Image size.** A nix store carrying four toolchains and a browser is larger
than the equivalent distro packages, because closures are complete and shared
system libraries are not. The recipes run `nix-collect-garbage` to reclaim
unused paths. `nix-store --optimise` was removed after it failed on container
overlay filesystems (see the Nix build fix in [the
changelog](../../CHANGELOG.md)); the nix images are still the largest of the
three variants.

**Two recipes became three per family.** The duplication this decision reduces
is in the tooling layer; the boot layer is now stated three times instead of
twice. That is the tradeoff the contract tests exist to make safe, and it is
cheaper than the alternative: the boot blocks change rarely, and the tooling
layer changes constantly.

**A guest dependency only.** Nothing new is required on
the host: nix is downloaded and installed inside the container build, like mise
and the vendor installers already are. `doctor` is unchanged.
