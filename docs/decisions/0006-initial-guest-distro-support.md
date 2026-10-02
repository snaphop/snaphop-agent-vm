# 6. Support Ubuntu, Fedora, and Arch Linux initially

Date: 2026-08-17

## Status

Accepted. Extended by
[ADR-0012](./0012-nix-provided-guest-tooling.md), which adds a Nix tooling
variant alongside each family's full and slim images.

## Context

Every supported distro family costs real, ongoing work: package names for the
kernel and guest tooling, initramfs generation, how `systemd` is enabled in the
container image, whether `cloud-init` is packaged and how it behaves, and a place
in the integration suite that must actually boot. Supporting "any Linux" is not a
feature we can honestly claim.

We want coverage of the three package-management and release-model families our
users actually ask for, chosen so that each one exercises a genuinely different
part of the image build:

- **Ubuntu** — `apt`, `linux-image-virtual`, `cloud-init` from the archive, the
  most common target and the safest default.
- **Fedora** — `dnf`, `kernel-core` with a `dracut`-generated initramfs, current
  upstream `systemd`. Exercises a different initramfs path than Debian's.
- **Arch Linux** — `pacman`, rolling release, `mkinitcpio`. Exercises the "no
  stable tag" case and is popular with the developer audience.

Notably absent: Debian (very close to Ubuntu, so low marginal coverage for the
maintenance cost), Alpine (musl and BusyBox init would be a genuinely different
guest contract), and enterprise RHEL derivatives (subscription and lifecycle
concerns).

## Decision

Support three guest distro families initially, each pinned by digest in its base
image manifest:

| Distro | Default source image | Kernel / initramfs |
|---|---|---|
| `ubuntu` (default) | `docker.io/library/ubuntu:24.04` | `linux-image-virtual`, `initramfs-tools` |
| `fedora` | `registry.fedoraproject.org/fedora:42` | `kernel-core`, `dracut` |
| `arch` | `docker.io/library/archlinux:base` | `linux`, `mkinitcpio` |

Distro-specific build recipes live in `templates/distro/`, with definitions in
`internal/image/distro/` for source references, artifact names, and behavior a
Containerfile cannot express. They are not spread across packages as `switch`
statements. Each family also has a separately cached `-slim` variant, sharing its
boot and cloud-init contract while omitting the full image's heavier tooling. Adding a
tag within a supported family is routine. **Adding a new distro family requires an
ADR**, because it commits the project to ongoing maintenance and integration
coverage.

Every supported distro must boot in the integration suite. A distro whose base
image cannot be built and booted is not supported, regardless of whether code for
it exists.

Users who need something else can point `--from` at their own OCI image within a
supported family's build recipe; that is a supported escape hatch, not a new
family.

## Consequences

Easier:

- Three well-tested guests instead of a vague promise of universality.
- Each family covers a distinct initramfs and package-management path, so the
  image build abstraction is validated by real difference rather than by three
  variations of the same thing.
- Ubuntu as the default gives new users the most predictable experience.

Harder:

- Three distro families, each with full and slim recipes, to maintain against
  upstream changes: package renames,
  `cloud-init` behavior changes, container images dropping something we depend on.
- Arch is a rolling target — a rebuilt base image is not last week's base image.
  The digest records the source OCI image; subsequently installed packages
  and tools can still change. Arch base rebuilds are more likely to break
  than the others.
- Integration runtime grows with each family, and the suite already requires a
  KVM-capable host.
- Users of Debian, Alpine, or RHEL derivatives are not served out of the box, and
  the `--from` escape hatch only helps within a supported family.
