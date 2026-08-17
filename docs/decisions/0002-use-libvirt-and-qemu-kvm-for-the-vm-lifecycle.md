# 2. Use libvirt and QEMU/KVM for the VM lifecycle

Date: 2026-08-17

## Status

Accepted. Amended by
[ADR-0009](./0009-orchestrate-existing-host-cli-tools.md): the libvirt/QEMU
choice stands, but we drive it through `virt-install` and `virsh` rather than
through the libvirt Go bindings.

## Context

The product is a per-task, disposable Linux machine for an AI coding agent. It
needs kernel-level isolation from the host, ordinary networking, and root inside
the guest. Something has to define, start, stop, and destroy those machines.

Options considered:

1. **libvirt + QEMU/KVM.** The standard Linux virtualization management layer.
   Declarative domain XML, a stable API and CLI (`virsh`), managed NAT networks
   with DHCP, host bridge attachment, guest agent integration, and
   SELinux/AppArmor confinement of the QEMU process out of the box.
2. **Raw QEMU processes managed by this tool.** Full control, no daemon
   dependency — but we would be reimplementing process supervision, network
   setup, DHCP, MAC allocation, console handling, and security labeling, and
   operators would lose every familiar tool.
3. **A higher-level VM manager (Vagrant, Multipass, Lima, Firecracker).** Faster
   to reach a demo. Vagrant and Multipass impose their own image formats and
   lifecycle conventions and are awkward to drive programmatically; Firecracker
   is excellent for microVMs but restricts device models, has no bridged
   networking story we want to own, and does not run on all the hardware our
   users have.
4. **Containers instead of VMs.** Rejected at the product level: a shared kernel
   is the specific risk this project exists to remove.

Constraints that mattered: operators must be able to inspect and intervene with
tools they already know; guests need both NAT and bridged networking; and the
project must not ship a daemon of its own.

## Decision

Use **QEMU/KVM as the hypervisor and libvirt as the management layer**. The tool
defines domains with `virt-install` and drives start/stop/undefine and inspection
with `virsh` — the same commands an operator would run (see ADR-0009 for why we
use these tools rather than the libvirt API bindings, and for the full tool
inventory). Minimum supported versions are libvirt 9.0, QEMU 8.0, and
`virt-install` 4.0. Hardware virtualization is required — we refuse to run under
software emulation rather than deliver a VM too slow to work in.

`agent-vm` remains a foreground CLI with no daemon of its own; libvirt is the
only long-running component.

## Consequences

Easier:

- Networking, DHCP, MAC allocation, console logging, and QEMU confinement come
  from libvirt instead of from us.
- Operators debug with `virsh list`, `virsh dumpxml`, `virsh console` — no
  bespoke tooling to learn, and no need for our tool to be running. Those are
  also the exact commands the tool itself runs.
- `virt-install` already knows what boots on a given host — machine type,
  firmware, virtio device models, console and guest agent wiring — so we do not
  maintain that knowledge ourselves.

Harder:

- libvirt becomes a hard runtime dependency, including its service, socket
  permissions, and group membership. Host readiness is now a real failure mode,
  which is why `agent-vm doctor` exists.
- Two access modes to support: `qemu:///system` (needed for bridges) and
  `qemu:///session` (unprivileged, NAT-only).
- `virt-install`, `virsh`, and libvirt behavior differ across versions and
  architectures, so the minimum-version floor has to be checked at runtime
  (`agent-vm doctor`) and exercised in the integration suite.
- Non-Linux hosts are out of scope; KVM is Linux-only.
