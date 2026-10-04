# Documentation

Documentation for SnapHop Agent VM.

- [`cli.md`](./cli.md) — the canonical `agent-vm` command-line contract:
  subcommands, flags, defaults, configuration, exit codes, and state layout.
- [`architecture.md`](./architecture.md) — system overview, components, data
  flow, runtime and failure behavior, trust boundaries, and known risks.
- [`host-setup.md`](./host-setup.md) — preparing a KVM host, bridge setup,
  storage, and troubleshooting.
- [`decisions/`](./decisions/) — Architecture Decision Records.

## Decision Records

| ADR | Decision |
|---|---|
| [0001](./decisions/0001-record-architecture-decisions.md) | Record architecture decisions |
| [0002](./decisions/0002-use-libvirt-and-qemu-kvm-for-the-vm-lifecycle.md) | Use libvirt and QEMU/KVM for the VM lifecycle |
| [0003](./decisions/0003-oci-container-images-as-vm-root-disks.md) | Build VM root disks from OCI container images |
| [0004](./decisions/0004-direct-kernel-boot-with-copy-on-write-overlays.md) | Boot from copy-on-write overlays with direct kernel boot |
| [0005](./decisions/0005-support-nat-and-bridged-networking.md) | Support NAT and bridged networking, NAT by default |
| [0006](./decisions/0006-initial-guest-distro-support.md) | Support Ubuntu, Fedora, and Arch Linux initially |
| [0007](./decisions/0007-default-vm-resource-profile.md) | Default to 2 vCPU, 4 GiB RAM, 50 GiB thin disk |
| [0008](./decisions/0008-go-single-binary-cli-with-no-daemon.md) | Implement the tool as a single Go binary with no daemon |
| [0009](./decisions/0009-orchestrate-existing-host-cli-tools.md) | Orchestrate existing host CLI tools instead of reimplementing them (amends 0002, 0008) |
| [0010](./decisions/0010-drive-a-remote-hypervisor-by-running-host-tools-over-ssh.md) | Drive a remote hypervisor by running host tools over ssh |
| [0011](./decisions/0011-build-the-cloud-init-seed-and-attach-it-as-a-virtio-disk.md) | Build the cloud-init seed and attach it as a virtio disk (narrows 0009) |
| [0012](./decisions/0012-nix-provided-guest-tooling.md) | Provide guest tooling through Nix as a third image variant |
| [0013](./decisions/0013-self-hosted-github-actions-runner-variant.md) | Add a self-hosted GitHub Actions runner variant of each slim image |
| [0014](./decisions/0014-join-a-tailscale-network-from-the-guest.md) | Join a Tailscale network from inside the guest |

Add operational runbooks, contract references, and design notes here, and link
them from `README.md` and `AGENTS.md` so both humans and agents can find them.
When an ADR is superseded, keep the file, set its status, and add the successor to
the table above.

Contributor policy lives in [`AGENTS.md`](../AGENTS.md),
[`CONTRIBUTING.md`](../CONTRIBUTING.md), [`CODE_REVIEW.md`](../CODE_REVIEW.md),
and [`SECURITY.md`](../SECURITY.md). Repository-wide bug, documentation,
enhancement, and pull-request review workflows live under
[`.agents/skills/`](../.agents/skills/) and are exposed to Claude through
[`.claude/skills/`](../.claude/skills/) symlinks.
