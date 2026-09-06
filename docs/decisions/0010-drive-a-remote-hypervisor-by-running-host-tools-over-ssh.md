# 10. Drive a remote hypervisor by running host tools over SSH

Date: 2026-08-29

## Status

Accepted

Extends [ADR-0009](./0009-orchestrate-existing-host-cli-tools.md) (orchestrate
existing host CLI tools) and [ADR-0004](./0004-direct-kernel-boot-with-copy-on-write-overlays.md)
(direct kernel boot with copy-on-write overlays). Neither decision changes; this
records where those tools run when libvirt is not on the machine the operator
typed the command on.

## Context

`agent-vm` has always taken a `--libvirt-uri`, and libvirt has always been able
to speak to a remote daemon over `qemu+ssh://`. But the flag only ever selected
a *connection*. Everything else in the design assumes the hypervisor is this
machine:

- `podman` and libguestfs build a base image into the state directory, and QEMU
  has to be able to open the result.
- `qemu-img` creates the per-VM overlay as a file backed by that base image.
- `virt-install` is handed absolute paths to the overlay, the kernel, the
  initramfs, and the cloud-init user-data. libvirt resolves those on the
  hypervisor's filesystem.
- `agent-vm ssh` connects to an address on libvirt's NAT bridge, which is
  host-local by design and has no route from anywhere else.
- `doctor` checks `/dev/kvm`, group membership, free space, the host bridge, and
  the host firewall.

Pointing the existing flag at `qemu+ssh://` therefore produced a connection that
worked and a tool that did not: paths named files on the wrong machine, and the
guest was unreachable even when it booted.

Three arrangements could fix that.

**Build here and copy the artifacts over.** Keep `podman` and libguestfs on the
client, then transfer the base disk, kernel, and initramfs to the hypervisor's
cache. It keeps `list` and `info` fast and offline, but it splits the state
directory across two machines, adds a multi-gigabyte transfer per base image,
and leaves two implementations of "where do artifacts live" to keep in step.

**Let `virt-install` and `virsh` talk to the remote libvirt themselves.** This is
what those tools' remote support is for, and it is the smallest change. But it
only moves the *control* path: the disks still have to exist on the hypervisor,
so something has to create them there anyway. It also puts weight on
`virt-install`'s remote-connection handling — storage volume uploads, pools,
remote path resolution — which is far less exercised than the local path this
project already depends on.

**Run the tools on the machine that owns the hypervisor.** The client becomes a
thin driver: every invocation that touches a disk, an image, or a domain is
executed there over ssh, and the state directory lives there too.

## Decision

When the libvirt URI names another machine, run every host tool on that machine
over ssh, and keep the state directory there.

Concretely:

- `internal/hostexec` gains a `Location` on each command and a `Remote` runner.
  Hypervisor-located commands — the default, and correct for everything that
  touches a disk, an image, or a domain — are wrapped in `ssh -- <destination>
  <quoted-command>`. Client-located commands stay here: `gh`, which uses the operator's
  GitHub login, and the `ssh` into a guest, which uses their keys and terminal.
- `virsh` and `virt-install` run **on the hypervisor**, with the URI as that
  machine reads it: `qemu+ssh://kvm@host/system` becomes `qemu:///system` at the
  point of invocation. Handing them the remote URI would send them back over ssh
  to the machine they are already on, and would still leave every path argument
  pointing at the wrong filesystem. The URI the operator gave is what is recorded
  in `vm.json`, because that is the connection they named.
- `internal/state` gains an `FS`, with the same containment rule enforced on
  either machine. The remote implementation is coreutils, findutils, and
  `flock(1)` — `mkdir`, `dd`, `chmod`, `mv`, `cat`, `rm`, `find`, `readlink`,
  `stat`, `df`, `du` — each as one argument vector, in keeping with ADR-0009.
- The advisory lock keeps the property that makes it safe: `flock -x -n <file>
  cat`, held by a process whose standard input this tool keeps open. The lock
  lives exactly as long as that process, so a killed `agent-vm` or a dropped
  connection releases it with nothing to clean up by hand — and two operators
  driving the same hypervisor from their own laptops contend for the same lock,
  which is the point.
- A guest is reached with `ssh -J <hypervisor>`. A NAT guest sits on a bridge
  that exists only on the hypervisor, so there is no route to it from here;
  jumping through that host uses the connection ssh already knows how to make,
  rather than a tunnel or a forwarded port of our own.
- Only `qemu+ssh://` is accepted for a remote hypervisor. `+tls`, `+tcp`, and the
  `libssh` transports reach libvirt but give no shell there, so they are refused
  as usage errors up front rather than failing halfway through a `create`.
- ssh connection sharing (`ControlMaster`) is enabled for the run. A create makes
  a dozen invocations and an image build many more; a fresh key exchange for each
  would dominate the time.
- ssh runs in `BatchMode`. This tool captures ssh's streams, so a prompt would be
  invisible and read as a hang. `ssh <destination> true` must succeed without a
  prompt, which is the same condition libvirt's own `qemu+ssh` transport needs.

## Consequences

**What this buys.** A laptop can drive a rack machine with no agent, daemon, or
package installed on either beyond what `doctor` already checks. The build
pipeline, the overlay, the domain definition, and the state layout are byte-for-
byte the ones this project already exercises locally — they simply run somewhere
else — so there is no second code path to keep correct. A local run is unchanged:
the `Location` default is the hypervisor, and when that is this machine nothing
wraps anything.

**What it costs.** Every state directory operation is now a round trip, and a
few operations that were one syscall are three or four commands. Connection
sharing keeps that in the milliseconds, but an image build over a slow link is
slower than one on the console. The state directory is on the hypervisor, so
`list` and `info` need it reachable; there is no local cache of VM records.

**What is not checked.** `doctor` cannot judge the hypervisor's firewall or the
permissions on the ancestors of its state directory from here — both would need
that machine's configuration files and passwd database. Those checks report that
they did not run, and point at `agent-vm doctor` on the hypervisor itself, rather
than producing a confident verdict about the wrong host.

**Security.** The guest is untrusted, and this does not change what it can reach:
the ssh connection is between the operator and the hypervisor, terminates there,
and is never exposed to a guest. No GitHub credential leaves this machine —
`gh` is client-located for exactly that reason. The remote command is a string
the hypervisor's shell re-splits, which is the one place in this project where an
argument vector becomes text; every argument is quoted so that the shell
interprets nothing, and that quoting is covered by its own tests.

**A remote shell is a real grant.** `qemu+ssh://` gives `agent-vm` the ability to
run arbitrary host tools as that account on that machine, which is strictly more
than a libvirt connection grants. That is the price of building images and disks
there, and it is why the URI has to say `ssh` explicitly rather than being
inferred.
