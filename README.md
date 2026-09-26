# snaphop-agent-vm

Disposable QEMU/KVM virtual machines for AI coding agents, built from OCI
container images and managed through libvirt.

An agent that can run arbitrary commands should not run them on your laptop. The
`agent-vm` command gives each agent task its own VM: full root access, a real
kernel, real networking, and a hard isolation boundary — created in seconds and
thrown away when the task is done.

> **Status: implementation in progress.** Every command in the documented
> contract is implemented — `doctor`, `image build`/`list`/`inspect`/`rm`,
> `create`, `list`, `info`, `start`, `stop`, `restart`, `ssh`, `update`,
> `console`, `destroy`, and `completion` — and the integration suite has built and booted
> all three supported distros on a real KVM host, in **both** network modes:
> the full lifecycle has been run for each distro under NAT and against a real
> host bridge. What remains is hardening rather than missing commands.
> [`docs/cli.md`](./docs/cli.md) remains the specification the implementation
> must satisfy; where the two disagree, one of them is a bug.

## Why VMs Instead Of Containers

A container shares the host kernel; a compromised or confused agent inside one is
one kernel bug or one misconfigured mount away from the host. A VM has its own
kernel and its own memory, separating guest processes from the host. Network
access still needs its own policy; the default NAT limits are described below.

The cost of a VM is usually setup time and boot time. This project removes both:
root filesystems come from OCI container images that are already cached and
layer-shared, each VM's disk is a copy-on-write overlay created instantly, and
guests boot QEMU directly into the kernel with no bootloader stage. See
[ADR-0003](./docs/decisions/0003-oci-container-images-as-vm-root-disks.md) and
[ADR-0004](./docs/decisions/0004-direct-kernel-boot-with-copy-on-write-overlays.md).

None of that is new virtualization machinery — it is `virt-install`, `virsh`,
`podman`, `qemu-img`, and libguestfs, driven in the right order.

## Requirements

- A Linux host with hardware virtualization (`/dev/kvm`)
- libvirt 9.0+ (`libvirtd` or `virtqemud`, plus `virsh`) and QEMU 8.0+
- `virt-install` 4.0+, `qemu-img`, `podman` 4.0+, libguestfs 1.50+, plus `ip`
  (iproute2) and `ssh`
- Go 1.22+ to build from source (pure Go — no cgo, no libvirt headers)
- Your user in the `kvm` and `libvirt` groups
- Optional: `gh` 2.0+, needed only by `--github-ssh-key` on `create` and
  `destroy`

Host preparation, including bridge setup, is in
[`docs/host-setup.md`](./docs/host-setup.md). Verify a host with:

```bash
agent-vm doctor
```

## Quick Start

```bash
# Build the base image for a distro (once; cached and reused afterwards)
agent-vm image build ubuntu

# Create and start a VM with the defaults: 2 vCPU, 4 GiB RAM, 50 GiB disk, NAT
agent-vm create agent-01

# Get a shell in it
agent-vm ssh agent-01

# See what is running
agent-vm list

# Throw it away
agent-vm destroy agent-01
```

Other distros and non-default sizing:

```bash
agent-vm create agent-02 --distro fedora --vcpus 4 --memory 8G --disk 100G
agent-vm create agent-03 --distro arch
```

## Defaults

| Setting | Default | Override |
|---|---|---|
| vCPUs | 2 | `--vcpus` |
| Memory | 4 GiB | `--memory` |
| Disk | 50 GiB (thin — an overlay, not preallocated) | `--disk` |
| Distro | `ubuntu` | `--distro` |
| Networking | NAT (libvirt `agent-vm-nat` network) | `--network` |
| Guest user | `agent`, passwordless `sudo`, your SSH public key | config file |

The disk size is the virtual size of a copy-on-write overlay. A fresh 50 GiB VM
consumes a few megabytes of host disk until the guest writes to it.

## Supported Guests

| Distro | Source image | Notes |
|---|---|---|
| Ubuntu | `docker.io/library/ubuntu:24.04` | default; cloud-init from the archive |
| Fedora | `registry.fedoraproject.org/fedora:42` | `kernel-core` + `dracut` initramfs |
| Arch Linux | `docker.io/library/archlinux:base` | rolling; rebuild the base image to update |

Each is pinned by digest in its base image manifest, so a rebuild is explicit
rather than something that happens behind your back. Adding a new distro
*family* requires an ADR; adding a new tag within a supported family does not.

Every family also has a **slim** variant, named by appending `-slim`
(`ubuntu-slim`, `fedora-slim`, `arch-slim`). It boots identically — same kernel
command line, same cloud-init contract, same SSH and clock behavior — and
carries the same common Linux tooling, but none of the agent tooling below: no
mise or language toolchains, no coding agents, no Chromium, no Docker, no
nested virtualization stack. It is a much smaller image and a much shorter
build, for a VM that only has to run a build, a shell, or a test suite:

```bash
agent-vm create build-01 --distro ubuntu-slim
```

A slim image is a separate base image with its own cache directory, manifest,
and name, so `ubuntu` and `ubuntu-slim` can be cached side by side and are
built and removed independently.

## What A VM Comes With

A base image is not a bare distro: it carries the tools an agent expects to
find already installed, so `create` stays fast and a guest works offline.
Beyond what makes a container image boot as a VM (kernel, `systemd`,
`cloud-init`, `openssh-server`, `sudo`, `qemu-guest-agent`), every full image ships
`git`, a C toolchain, Python, Node.js, Go, Rust, a JDK with Maven (via mise),
Docker, `gh` and `tea`, `wrangler` and `cf`, Playwright with a headless
Chromium, `tmux` with a session menu at login, the
[Herdr](https://herdr.dev) terminal workspace, and six coding agents —
`claude`, `codex`, `opencode`, `pi`, `agy`, and `grok` — each configured in its most
permissive mode, because the VM is the sandbox. Guests can also run VMs of
their own.

**No credentials are baked in.** The agents ship configured but
unauthenticated; logins arrive per VM. The full inventory, and why each piece
is where it is, is in [`docs/cli.md`](./docs/cli.md#guest-tooling).

## Networking

- **NAT (default).** The VM sits on a libvirt-managed NAT network. It can reach
  the internet, the host, other guests on that network, and the LAN, subject to
  host firewall rules. Unsolicited connections from the LAN are blocked. NAT
  does not isolate the host or LAN from guest-initiated connections; see the
  [host firewall guidance](./docs/host-setup.md#host-firewalls-and-the-virbrn-bridge).
- **Bridged.** The VM attaches to a host bridge and gets an address from your
  LAN's DHCP, like any other machine on the network. Useful when something else
  must connect *to* the agent's VM. It also removes the NAT boundary, so it is
  never the default and always requires `--network bridge`:

```bash
agent-vm create agent-04 --network bridge --bridge br0
```

See [ADR-0005](./docs/decisions/0005-support-nat-and-bridged-networking.md) and
the bridge setup section of [`docs/host-setup.md`](./docs/host-setup.md).

## How It Works

```text
OCI image (ubuntu:24.04)                             base image cache
   │  podman pull / build / export                        │
   ▼  virt-make-fs, virt-copy-out, virt-sysprep           │
flattened rootfs + kernel packages ──────► base.qcow2 + vmlinuz + initrd
                                                          │
agent-vm create ◄─────────────────────────────────────────┘
   │   qemu-img create -b base.qcow2 …        (copy-on-write overlay)
   │   virt-make-fs --label=cidata seed/      (NoCloud seed: hostname, SSH key)
   │   virt-install --import --boot kernel=…  (direct kernel boot)
   ▼
running VM ──► NAT network or host bridge      (virsh from here on)
```

Full detail, including failure behavior and trust boundaries, is in
[`docs/architecture.md`](./docs/architecture.md).

## Built On Standard Tools, Not Around Them

`agent-vm` is an orchestrator. It does not implement virtualization, generate its
own domain XML, or build its own container image handling — it runs the tools your
host already has, in the right order, with the safety checks and the caching that
make per-task VMs practical
([ADR-0009](./docs/decisions/0009-orchestrate-existing-host-cli-tools.md)):

| Job | Tool |
|---|---|
| Define and start a VM | `virt-install` |
| Lifecycle, inspection, addresses, console, NAT network | `virsh` |
| Copy-on-write overlays | `qemu-img` |
| Pull, build, and flatten OCI images | `podman` |
| Root filesystem, cloud-init seed, kernel extraction, image generalization | `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| Host bridge validation | `ip -d -json link` |
| Guest shell, and the package and tooling updates `agent-vm update` runs in a guest | `ssh` |

Two consequences worth knowing:

```bash
# See exactly what would run, without running it
agent-vm create agent-05 --dry-run

# Everything is an ordinary libvirt domain, so normal tooling works
virsh list --all
virsh dumpxml agent-01
virsh console agent-01
```

Anything the CLI does not expose can be passed straight through with
`--virt-install-arg`, and a defined domain can still be edited with `virsh edit`.
`agent-vm doctor` checks that each tool is present and new enough, since the
design depends on them.

## Documentation

- [`docs/cli.md`](./docs/cli.md) — the command-line contract
- [`docs/architecture.md`](./docs/architecture.md) — system map, data flow,
  runtime and failure behavior
- [`docs/host-setup.md`](./docs/host-setup.md) — preparing a KVM host
- [`docs/decisions/`](./docs/decisions/) — Architecture Decision Records
- [`AGENTS.md`](./AGENTS.md) — canonical instructions for AI coding agents
  working *on* this repository
- [`SECURITY.md`](./SECURITY.md) — private reporting process and the hard
  security boundaries this project enforces
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — contribution workflow
- [`CODE_REVIEW.md`](./CODE_REVIEW.md) — review process
- [`CHANGELOG.md`](./CHANGELOG.md) — notable changes

## Security Notes

The guest is treated as untrusted. By default no host filesystem is shared into
a VM, no host credentials are injected, and only your SSH public key reaches the
guest. Bridged networking is the one opt-in that removes a boundary, and it is
always explicit. Host-path sharing is not offered; adding it would require an ADR
and would have to be per-VM and explicit, per [`SECURITY.md`](./SECURITY.md).

A VM is an isolation boundary, not a guarantee. Do not run something in one of
these VMs that you would not run on a machine you are willing to lose.

## Building And Installing

```bash
make build                 # go build ./...
make install               # build the static binaries and install this host's
                           # into ~/.local/bin (override with PREFIX or BINDIR)
```

`make release` builds the static binaries for every architecture into `dist/`;
installing on another host is copying the right one onto it.

## Development

```bash
make check                 # gofmt, go vet, golangci-lint, go test — the
                           # pre-handoff bar (scripts/check.sh)
```

Unit tests need no KVM host: tools are faked at the process boundary, and
`virt-install` argument vectors and generated cloud-init user-data are pinned by
golden files. `--dry-run` lets you inspect what a change would actually run.

Integration tests create and destroy real VMs on the host that runs them:

```bash
go test -tags integration ./test/integration/...
```

See [`AGENTS.md`](./AGENTS.md) §3–§4 for the full command list and the
verification required before handing work off.

## License

[MIT](./LICENSE)
