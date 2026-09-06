# 7. Default to 2 vCPU, 4 GiB RAM, and a 50 GiB thin disk

Date: 2026-08-17

## Status

Accepted

## Context

Most VMs will be created with no sizing flags at all, so the defaults are the
product for most users. They have to be big enough for real agent work — cloning a
repository, installing a toolchain, running a build and a test suite — and small
enough that several VMs coexist on a developer laptop with 16–32 GiB of RAM.

Memory is the binding constraint: it is committed per running VM and cannot be
oversubscribed casually. Two vCPUs allow parallelism without letting one VM
monopolize a 4–8 core laptop; more vCPUs mostly help builds, which is exactly the
case where a user knows to ask for more.

Disk is different, because overlays are thin (ADR-0004). A generous virtual size
costs nothing until written, and the failure mode of a too-small disk — a build
dying halfway with `ENOSPC` — is far more annoying than the failure mode of a
generous one, which is that the operator must watch actual usage.

## Decision

Defaults for `agent-vm create`:

| Resource | Default | Flag |
|---|---|---|
| vCPUs | 2 | `--vcpus` |
| Memory | 4 GiB | `--memory` |
| Root disk | 50 GiB virtual, thin overlay | `--disk` |

All three are overridable per VM and settable as defaults in the config file or
via `AGENT_VM_*`. The guest gets what was asked for: no CPU pinning, no automatic
sizing from host capacity, and no reclaiming memory from a running guest. (The
domain does carry the standard `--memballoon virtio` device, as any virtio guest
does, but the tool never inflates it.) Capacity planning stays with the operator.

Changing any of these defaults is a public contract change (`AGENTS.md` §8): the
same command would produce a different machine.

## Consequences

Easier:

- `agent-vm create <name>` produces a machine that can do real work without
  anyone reading the flag list first.
- Several VMs can share a developer host, provided its capacity leaves room for
  the host OS and QEMU overhead; four defaults alone request 16 GiB of guest RAM.
- The 50 GiB disk costs a few megabytes at creation, so the generous default is
  effectively free until used.

Harder:

- Heavy builds and large toolchains will need `--vcpus`/`--memory`, and the
  symptom of not raising them is a slow or OOM-killed build inside the guest.
- Thin overlays make disk pressure invisible at creation time: ten 50 GiB VMs can
  overwrite a host's real capacity. `docs/host-setup.md` documents how to check
  actual consumption.
- No oversubscription support means the host can be exhausted by enough
  simultaneous VMs, and the tool will not stop that.
- These numbers will age. Revisiting them means a new ADR superseding this one,
  plus a `CHANGELOG.md` entry, because the change is user-visible.
