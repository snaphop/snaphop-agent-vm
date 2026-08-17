# 4. Boot from copy-on-write overlays with direct kernel boot

Date: 2026-08-17

## Status

Accepted

## Context

Given a cached base disk built from an OCI image (ADR-0003), two things still
determine how fast a VM becomes usable: how its root disk is created, and how it
boots.

**Disk creation.** Copying a multi-gigabyte base image per VM takes seconds to
minutes and consumes real disk per VM. A qcow2 overlay with the base as a backing
file is created in milliseconds, occupies kilobytes until written, and keeps the
base immutable by construction — many VMs safely share one base file opened
read-only.

**Boot path.** A conventional VM boots firmware → bootloader → kernel. Getting
there from a container image means installing a bootloader and an EFI system
partition into the base image, and paying OVMF plus GRUB on every boot. QEMU can
instead be handed a kernel and initramfs directly (`-kernel`/`-initrd`/`-append`,
expressed in libvirt as `<kernel>`/`<initrd>`/`<cmdline>`), skipping firmware and
bootloader entirely — which also removes the need to make a container-derived
image bootloader-capable at all.

The trade is real: with direct kernel boot, the kernel that runs comes from the
host-side artifact, not from the guest's filesystem. A guest that installs a new
kernel package keeps booting the old one until the base image is rebuilt.

For disposable, task-scoped VMs that live minutes to hours, "the guest can
upgrade its own kernel and reboot into it" is close to worthless, and boot latency
is felt on every single `create`.

## Decision

**Per-VM disks are qcow2 copy-on-write overlays on the immutable base image:**

```bash
qemu-img create -f qcow2 -b <base.qcow2> -F qcow2 root.qcow2 50G
```

The base file is opened read-only. The `--disk` size is the overlay's virtual
size; nothing is preallocated. Deleting a base image while any overlay depends on
it is refused.

**Guests boot via direct kernel boot**, expressed to `virt-install` (ADR-0009) as:

```bash
virt-install --import --disk path=root.qcow2,bus=virtio \
  --boot kernel=<images>/vmlinuz,initrd=<images>/initrd,kernel_args="root=/dev/vda1 console=ttyS0"
```

The kernel and initramfs are the artifacts extracted during the image build, and
the kernel command line is recorded in the base image manifest. No bootloader, no
EFI system partition, no OVMF.

Deferred, not adopted: UEFI/GRUB boot as an opt-in per-VM mode, for guests that
genuinely need to manage their own kernel. Adding it would supersede this ADR in
part, not replace the overlay decision.

## Consequences

Easier:

- `create` is effectively instant on the disk side, and boot skips firmware and
  bootloader — seconds to a usable shell.
- Ten VMs from one base image cost one base image of disk plus whatever the guests
  write.
- The base image needs no bootloader, no ESP, and no partition table beyond a
  single root filesystem — much less to get right when converting a container
  image.
- The kernel command line is an explicit, inspectable artifact in the manifest
  rather than GRUB configuration inside the guest.

Harder:

- **A guest cannot change its own kernel.** `apt upgrade` installing a new kernel
  changes nothing until the base image is rebuilt. This surprises users who expect
  a normal VM, so it is documented in the README, the architecture risks section,
  and the host setup guide.
- The base image is load-bearing for every VM built on it. Corruption or accidental
  deletion breaks all of them at once, so base images are immutable, opened
  read-only, and protected by a refusal to remove them while in use.
- Overlay chains hide real disk consumption: a 50 GiB VM reports 50 GiB virtual
  and a few megabytes actual, and ten of them can overcommit a disk that looked
  fine at creation time.
- Kernel and initramfs live on the host, outside the guest disk, so backing up a
  VM means backing up the base artifacts too.
- Boot behavior now depends on a host-side kernel command line, which becomes part
  of the public contract via the manifest.
