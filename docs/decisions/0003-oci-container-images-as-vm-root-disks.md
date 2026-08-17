# 3. Build VM root disks from OCI container images

Date: 2026-08-17

## Status

Accepted

## Context

A VM needs a root filesystem. The traditional sources are a distro installer ISO
(minutes per VM, interactive or Kickstart/preseed-driven) or a vendor cloud image
(fast, but a large opaque qcow2 per distro/version, customized only through
cloud-init at boot).

Our users already have OCI container images cached locally and already know how
to customize them: a `Containerfile` is the most familiar, most reproducible way
in this ecosystem to say "this distro plus these tools." Container images are
also layer-shared, digest-addressed, and available for every distro we care
about, from registries our users already trust and mirror.

What container images lack for VM use: a kernel, an initramfs, `systemd` as PID 1
in some cases, `cloud-init`, an SSH server, and a partitioned bootable disk.
Those are additions, not blockers.

Options considered:

1. **Per-VM distro install from ISO.** Reproducible and conventional, but minutes
   per VM. Fatal for a tool whose value proposition is "create a VM per task."
2. **Vendor cloud images.** Fast and well-supported, but customization means
   either boot-time cloud-init on every VM (slow, fragile) or a separate image
   build pipeline anyway — and we would still need a caching layer.
3. **OCI container images as the root filesystem source, converted once per
   distro into a cached base disk.** Reuses the registry ecosystem, gives users a
   `Containerfile`-shaped customization story, and the conversion cost is paid
   once per distro rather than once per VM.
4. **`bootc` / `bootc-image-builder` / `podman-bootc`.** The purpose-built
   existing answer to "boot a container image as a system," and the first thing we
   should reach for under ADR-0009. Rejected *for now*, on three specific grounds:
   it requires bootc-enabled base images, which do not exist for all three distro
   families we committed to (notably Arch, ADR-0006); it produces bootloader-based
   disk images, which conflicts with direct kernel boot (ADR-0004) and its boot
   latency; and it adds a heavyweight dependency to reach an outcome the
   libguestfs tools already give us. It remains the strongest candidate for an
   opt-in second image backend, and adopting it would be a new ADR rather than a
   quiet change.

## Decision

**A VM's root filesystem comes from an OCI container image**, converted once into
an immutable cached base disk:

Each step delegates to an existing tool (ADR-0009); we own the recipe, not the
mechanics:

1. `podman pull` the source image, pinned and recorded by digest.
2. `podman build` an embedded per-distro `Containerfile` on top of it, adding what
   a VM needs and a container lacks: the distro kernel, an initramfs, `systemd`,
   `cloud-init`, `openssh-server`, `sudo`, and `qemu-guest-agent`. The
   `Containerfile` is the whole distro-specific recipe, in the format our users
   already know.
3. `podman create` + `podman export` to flatten the result to a root filesystem
   tarball — podman already knows how to squash layers, whiteouts included.
4. `virt-make-fs --type=ext4 --format=qcow2` to assemble the tarball into
   `base.qcow2`, with no root required on the host.
5. `virt-ls` and `virt-copy-out` to locate and extract `vmlinuz` and `initrd`
   from the image's `/boot`.
6. `virt-sysprep --operations machine-id,ssh-hostkeys,…` to generalize the base
   image, so VMs sharing it do not also share a machine ID or SSH host keys.
7. Write `manifest.json` recording the source digest, kernel version, kernel
   command line, and the versions of the tools that produced the image.

The base image is immutable and read-only once built; per-VM disks are overlays
on it (see ADR-0004). Builds go to a temporary directory and are renamed into
place only on success, so no VM can ever boot a half-built base image.

Initial supported distro families are Ubuntu, Fedora, and Arch Linux (ADR-0006).

## Consequences

Easier:

- VM creation no longer installs an operating system. Base image cost is paid
  once per distro; `create` becomes a disk-overlay operation.
- Customization is a container image build, which our users already know, already
  have CI for, and can pin by digest.
- Provenance is exact: every VM records the OCI digest its root filesystem came
  from.
- Registry mirrors and layer caching make repeat builds and offline work cheap.

Harder:

- We own an image build pipeline, including per-distro package names and
  initramfs generation. That knowledge lives in one place — the per-distro
  `Containerfile` under `templates/distro/` — rather than as code, which keeps it
  reviewable by anyone who can read a Dockerfile.
- libguestfs becomes a dependency, and its appliance is the most fragile piece of
  the stack — the usual source of confusing build failures.
- Container images are not built to be bootable. Guest packaging surprises (a
  kernel that expects a bootloader, `systemd` units disabled in the container
  build) are ours to absorb.
- `image build` requires network access to a registry, unlike everything else the
  tool does.
- Rolling-release images (Arch) mean "rebuild the base image" is not idempotent
  over time. Reproducibility comes from the recorded digest, not from the tag.
