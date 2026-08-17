# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Add entries under `[Unreleased]` in the same change that introduces an
observable behavior, security, dependency, compatibility, or deployment
change. Describe what changed and why for a human reader.

While the project is `0.x`, breaking changes to the command line, configuration,
or on-disk state are permitted but must be called out here explicitly, with the
migration or rebuild step a user has to take.

## [Unreleased]

### Added

- The first working `agent-vm` binary. It builds as a single static Go binary
  with no cgo, and implements the `doctor` command, the `--version` report, and
  the global flags (`--config`, `--state-dir`, `--libvirt-uri`, `--output`,
  `--verbose`, `--quiet`, `--yes`, `--dry-run`).
- `agent-vm doctor` checks whether a host can run VMs: `/dev/kvm`, the libvirt
  connection, group membership, the state directory and its free space, every
  required host tool and its minimum version, the NAT network, and a configured
  bridge. Each check reports pass, warn, fail, or skip with a remedy; only a
  failure exits non-zero (exit `3`). It is available as `--output json` for
  scripts, and it never changes host state.
- `agent-vm --version` reports the tool's version together with the detected
  versions of `virsh`, `virt-install`, `qemu-img`, `podman`, the libguestfs
  tools, `ip`, and `ssh`, so "which versions am I running against?" is
  answerable before something breaks.
- Configuration resolution with the documented precedence — defaults, then
  `config.toml`, then `AGENT_VM_*` environment variables, then flags — with
  validation that fails before any host state changes. The documented defaults
  (2 vCPU, 4 GiB RAM, 50 GiB disk, NAT, `ubuntu`, guest user `agent`) are
  enforced by tests so a change to one is visible as a contract change.
- The state directory, `vm.json`, and `manifest.json`, both carrying a
  `schemaVersion` that the tool refuses to guess at, plus advisory file locks
  that keep two `agent-vm` processes from racing on the same VM or base image.
  A crashed process never leaves a lock behind for a human to clear.
- NAT network management (`virsh net-define`/`net-start`/`net-autostart` from
  embedded network XML) and host bridge validation via `ip -json link`.
- `scripts/check.sh` (format, vet, lint, unit tests) and
  `scripts/build-release.sh` (static binary per architecture).
- Design documentation for `snaphop-agent-vm`: a tool that creates disposable
  QEMU/KVM virtual machines for AI coding agents, so an agent can run commands
  with root access on a throwaway machine instead of on the developer's host.
- The `agent-vm` command-line contract in `docs/cli.md`, covering VM lifecycle
  (`create`, `list`, `info`, `start`, `stop`, `restart`, `ssh`, `console`,
  `destroy`), base image management (`image build`, `list`, `inspect`, `rm`),
  host verification (`doctor`), configuration precedence, exit codes, and the
  on-disk state layout.
- A system map in `docs/architecture.md` describing components, the `create`
  data flow and its rollback behavior, trust boundaries, external dependencies,
  and known operational risks.
- A host preparation and troubleshooting guide in `docs/host-setup.md`, including
  bridge setup and how to check real disk consumption of thin VM disks.
- Architecture Decision Records for the choices that shape the product:
  libvirt + QEMU/KVM as the virtualization stack (ADR-0002), OCI container
  images as the source of VM root filesystems (ADR-0003), copy-on-write overlays
  with direct kernel boot for fast creation and boot (ADR-0004), NAT and bridged
  networking with NAT as the default (ADR-0005), initial support for Ubuntu,
  Fedora, and Arch Linux (ADR-0006), the default 2 vCPU / 4 GiB / 50 GiB VM
  profile (ADR-0007), a single Go binary with no daemon (ADR-0008), and
  orchestrating existing host CLI tools instead of reimplementing them (ADR-0009).
- A `--dry-run` global flag that prints the exact tool invocations an operation
  would perform without running them, a `--virt-install-arg` pass-through for
  anything the CLI does not expose, and an "Underlying Commands" table in
  `docs/cli.md` so every operation can be reproduced by hand.

### Changed

- The state directory now also contains `networks/`, holding the network XML
  passed to `virsh net-define` as a record, and `locks/`. Both are documented in
  `docs/cli.md`.
- The NAT network is defined on `192.168.171.0/24` rather than colliding with
  libvirt's own `default` network on `192.168.122.0/24`, and it does not name a
  bridge device, so libvirt allocates one. Documented in `docs/host-setup.md`.
- Subcommands specified in `docs/cli.md` but not implemented yet (`create`,
  `list`, `info`, `start`, `stop`, `restart`, `ssh`, `console`, `destroy`,
  `image`) report that they are unimplemented in this build instead of being
  reported as unknown commands.
- Replaced the repository's generic project-template documentation with
  project-specific instructions: `AGENTS.md` now describes the real layout,
  commands, contracts, and prohibited actions; `SECURITY.md` states the trust
  boundaries that apply when an untrusted agent runs inside a VM; and
  `CONTRIBUTING.md` and `CODE_REVIEW.md` reflect the actual verification steps.
- Repository layout now follows Go conventions (`cmd/`, `internal/`,
  `templates/`, `test/`) in place of the placeholder `src/` and `tests/`
  directories.
- Reworked the design to drive existing tools rather than reimplement them
  (ADR-0009): domains are defined with `virt-install` and managed with `virsh`
  instead of through libvirt Go bindings and a hand-maintained domain XML template;
  base images are built with `podman` plus `virt-make-fs`/`virt-copy-out`/
  `virt-sysprep`; cloud-init seeds are built by `virt-install`; and `ssh` and
  `virsh console` are exec'd directly. The build is now pure Go with no cgo, and
  the required host tools and their minimum versions are documented and checked by
  `agent-vm doctor`.

### Security

- Documented the project's hard security boundaries in `SECURITY.md`: the guest
  is untrusted, no host filesystem or credential reaches a VM by default, only
  SSH public keys are injected, bridged networking (which removes the NAT
  boundary) is always opt-in, and destructive operations are confined to state
  this tool created.

### Removed

- The template setup checklist (`docs/project-setup.md`), which no longer applies
  now that the repository is a real project; it became `docs/host-setup.md`.

## [0.1.0] - 2026-08-17

### Added

- Initial project scaffold.
