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

- `agent-vm doctor` now checks whether the host firewall will drop the guest's
  outbound traffic. libvirt accepting a packet in its own nftables table is not
  the last word on it: every base chain registered on the forward hook runs, so
  a firewall with a drop policy there discards traffic libvirt already accepted.
  `ufw` ships exactly that (`DEFAULT_FORWARD_POLICY="DROP"`), and the result is
  a VM that looks healthy in every way an operator normally checks — it boots,
  accepts SSH, and resolves DNS, because the resolver is dnsmasq on the host
  bridge and that traffic is never forwarded — while every outbound connection
  hangs instead of failing, because the packets are dropped rather than
  rejected. The check reads `ufw`'s configuration only, never running `ufw` or
  changing a rule, and passes when `ufw` is absent, disabled, forwarding by
  default, or has a rule accepting forwarded traffic, naming the interfaces
  those rules cover. It warns rather than fails, because the live ruleset needs
  root to read and a false failure would exit non-zero on a working host.
- `agent-vm doctor` now reports whether the account the hypervisor runs as can
  reach the state directory. Under `qemu:///system` QEMU runs as libvirt's own
  user (`libvirt-qemu`, `qemu`, or whatever `/etc/libvirt/qemu.conf` sets), which
  must be able to search every directory from `/` down to a VM's disk. The
  default state directory sits under `~/.local/share`, and home directories are
  commonly `0700`, so this was the most likely reason a `create` failed — and it
  failed late, after the overlay and the domain were already built, with
  `Cannot access storage file ... Permission denied` from `virt-install`. The
  new check reports it up front, names the shallowest directory that blocks the
  path, and prints the `setfacl` command that grants search access without
  granting read. It reads POSIX ACLs as well as permission bits, so a host that
  has already been fixed with `setfacl` is reported as passing; it is skipped
  rather than guessed when the hypervisor's account cannot be identified, and it
  does not apply to `qemu:///session`, where QEMU runs as the invoking user.
- The first working `agent-vm` binary. It builds as a single static Go binary
  with no cgo, and implements the `doctor` command, the `--version` report, and
  the global flags (`--config`, `--state-dir`, `--libvirt-uri`, `--output`,
  `--verbose`, `--quiet`, `--yes`, `--dry-run`).
- `agent-vm doctor` checks whether a host can run VMs: `/dev/kvm`, the libvirt
  connection, group membership, the state directory and its free space, whether
  the hypervisor's own account can reach the state directory, every required
  host tool and its minimum version, the NAT network, and a configured
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
- The base image build pipeline and the `agent-vm image build`, `image list`,
  `image inspect`, and `image rm` commands. A build pulls the source image,
  pins it to the digest that was actually fetched, builds the embedded
  per-distro `Containerfile` on top of that digest, flattens the result,
  writes a partitioned ext4 `base.qcow2`, extracts the kernel and initramfs,
  and generalizes the image with `virt-sysprep` so no two VMs share a machine
  ID or SSH host key. It records the source digest, kernel version, kernel
  command line, and the version of every tool that took part.
- Per-distro build recipes for Ubuntu, Fedora, and Arch Linux in
  `templates/distro/`, which are the readable form of all distro-specific
  knowledge in the project. Each installs a kernel, an initramfs generator,
  systemd, cloud-init, sshd, sudo, and the QEMU guest agent, and restricts
  cloud-init to the NoCloud datasource so a guest never probes a metadata
  service on the network.
- Golden-file testing (`internal/golden`, `test/golden/`), regenerated with
  `go test ./... -update-golden`. The first golden file pins the tool
  invocations an Ubuntu base image build performs.
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

- `agent-vm create <name>`, which is the command the whole tool exists for: it
  resolves configuration, builds the base image if it is not cached, creates a
  copy-on-write overlay on it, generates cloud-init user-data authorizing your
  SSH public keys, ensures the NAT network (or validates the host bridge you
  named), defines and starts the domain with one `virt-install` run, and waits
  for the guest to accept SSH. It records the result in `vm.json`, including the
  base image digest, the MAC address, and the exact `virt-install` version and
  argument vector that created the VM, so "what made this, from what?" is
  answerable from the state directory alone.
- `create` is transactional: a failure anywhere through "define and start"
  removes the domain, the overlay, and the state directory, and reports both the
  original failure and anything the cleanup could not remove (exit `7`). The one
  exception is the boot wait — a `--wait-for-ssh` timeout exits `6` and leaves
  the VM in place with its `console.log`, because a slow boot and a failed boot
  need the same evidence.
- Generated cloud-init user-data (`internal/guestinit`), pinned by golden files.
  It authorizes your public keys, creates the guest user with password login
  disabled and root login disabled, and nothing else. User-data you supply with
  `--cloud-init` is merged as a separate MIME part with explicit merge rules, so
  a mistake in your file cannot quietly replace the keys that let you in, and its
  contents are never logged or echoed in an error.
- Domain management (`internal/domain`): the `virt-install` argument vector,
  golden-pinned for both network modes, and the `virsh` calls behind lifecycle
  and inspection. `virsh undefine` is never given `--remove-all-storage` — this
  tool deletes only files it has verified are inside its own state directory.

- `agent-vm list` and `agent-vm info <name>`. `list` shows every VM this state
  directory recorded, with its live state, distro, resources, network mode,
  address, and creation time; a VM whose libvirt domain has vanished is reported
  as `missing` rather than silently dropped, and a domain that exists in libvirt
  but has no record here is never listed, because it is not this tool's to act
  on. `info` prints one VM's full record — the base image digest that actually
  booted, the MAC address, the overlay and its real cost on the host, the
  captured `domain.xml`, and the exact `virt-install` version and argument
  vector that created it, formatted so it can be copied and rerun.

- `agent-vm start`, `stop`, and `restart`. `stop` asks the guest to shut down
  and waits `--timeout` (60s by default); if the guest ignores it, the command
  exits `6` and says so, and the VM keeps running — escalating to a force-off
  would lose whatever the guest had not written, so it stays your decision and
  needs `--force`. `restart` will not start a guest it could not stop. Starting
  a running VM, or stopping a stopped one, exits `5` and names the state it
  found rather than doing nothing quietly.

- `agent-vm ssh <name> [-- <command>...]` and `agent-vm console <name>`. Both
  replace the `agent-vm` process with the tool they wrap, so your terminal,
  signals, window size, and exit status belong to `ssh` or `virsh console`
  directly — running a command in a guest forwards its exit status because it
  *is* that process. Arguments after `--` reach the guest untouched, even ones
  that look like `agent-vm` flags. `--dry-run` prints the exact `ssh` or
  `virsh console` command instead of running it, so you can use it by hand.
- `ssh` authenticates with the private key that sits beside the public key you
  authorized (`id_ed25519` next to `id_ed25519.pub`), naming it to `ssh` only
  when it exists and never reading it. `create`'s readiness probe offers the
  same key, so "create says it is ready" and "ssh works" cannot disagree.

- `agent-vm destroy <name>`, which asks the guest to shut down, undefines the
  domain, and deletes the VM's overlay, generated user-data, and state
  directory. It prompts first, naming exactly what it is about to delete, and a
  run with no terminal to answer on refuses rather than assuming consent
  (`--yes` skips the question). `--force` powers the guest off immediately and
  `--keep-disk` removes only the libvirt domain, leaving the disk and the state
  directory behind.
- `destroy` will not remove anything that is not this tool's: a name with no
  record in this state directory is a not-found error that never reaches
  libvirt, and a domain whose disk is not the overlay recorded here exits `5`
  and names the disks it found instead of undefining someone else's VM. A guest
  that ignores the shutdown request exits `6` with the VM intact, because
  destroy is about to delete the disk and that is the last moment unwritten
  data can still be saved. A VM whose domain has already vanished from libvirt
  can still have its leftover state removed.

### Changed

- The state directory now also contains `networks/`, holding the network XML
  passed to `virsh net-define` as a record, and `locks/`. Both are documented in
  `docs/cli.md`.
- The NAT network is defined on `192.168.171.0/24` rather than colliding with
  libvirt's own `default` network on `192.168.122.0/24`, and it does not name a
  bridge device, so libvirt allocates one. Documented in `docs/host-setup.md`.
- `create --no-start` is now rejected with exit `2` and an explanation, instead
  of being listed as a working flag. `virt-install` always boots a guest that
  has cloud-init data — the generated seed is attached to that first boot only
  and is absent from the domain it leaves defined — so a VM stopped before
  cloud-init finished would never receive its SSH key and could not be reached
  afterwards. Create the VM and stop it instead:
  `agent-vm create <name> && agent-vm stop <name>`. Documented in
  `docs/cli.md`.
- A cached base image directory also holds the `Containerfile` it was built
  from, as a record of the recipe that produced it. Documented in
  `docs/cli.md`.
- `--dry-run` now guarantees that nothing changes, including the state
  directory itself: it is not created, and commands that would delete or write
  print what they would do instead. `image build` prints its whole pipeline
  with `<placeholders>` for the values that only exist once a build has run,
  rather than executing the read-only half of a pipeline whose earlier steps
  were skipped.
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

- `create`, `destroy`, `image build`, and `image rm` now report a lock they
  could not release instead of discarding the failure. A stuck lock is host
  state an operator has to clear before the next run, so it is no longer
  silent.

### Fixed

- Ubuntu guests can install packages again. Every Ubuntu base image produced a
  guest whose apt sources named a suite called `UNAVAILABLE`, so `apt update`
  returned `404 Not Found` for every repository and nothing could be installed.
  cloud-init rewrites `/etc/apt/sources.list.d/ubuntu.sources` on first boot and
  asks the `lsb_release` command for the codename to write into it; cloud-init
  only *recommends* the package providing that command, and the image is built
  with `--no-install-recommends`, so it was absent and cloud-init substituted
  the literal string `UNAVAILABLE`. The Ubuntu image now installs `lsb-release`
  explicitly. Requires the same image rebuild as the DNS fix below.
- Guests can resolve DNS names again. Every base image was built with an empty
  `/etc/resolv.conf`, so a VM came up with a working DHCP lease, a working
  default route and `systemd-resolved` running and holding the correct DNS
  server, and still failed every name lookup — `ping 1.1.1.1` worked while
  `ping github.com` did not, and anything an agent tried to install or clone
  inside the VM failed. The cause is that `podman` bind-mounts
  `/etc/resolv.conf` over the image's own copy for the duration of each build
  step, so `systemd-resolved`'s packaging can never replace that path with the
  symlink it normally installs, and the empty file from the upstream container
  image is what gets committed. Nothing repaired it at boot either, because
  systemd's own rule for the path refuses to overwrite a file that already
  exists. All three base images now ship a rule that forces `/etc/resolv.conf`
  to `systemd-resolved`'s stub on every boot.

  **Rebuild step:** base images are cached by distro and tag, and an image that
  is already cached is not rebuilt just because the recipe changed. Existing
  cached images still produce guests without DNS, so rebuild each one you use
  with `agent-vm image build --force <distro>:<tag>`. VMs created from a
  rebuilt image pick the fix up on their next boot; VMs already created from an
  old image do not, and need to be recreated.
- `create`, `ssh`, and `list` no longer report a VM's address as `127.0.0.1`.
  Once the QEMU guest agent starts answering, `virsh domifaddr --source agent`
  lists the guest's loopback interface first, and the tool took that address at
  face value — so the boot wait, and any `agent-vm ssh` afterwards, connected to
  the host's own SSH server instead of the guest. On a host running sshd this
  surfaced as `create` timing out with "did not accept SSH" on a VM that had in
  fact booted correctly. Loopback addresses are now skipped.
- `agent-vm info` reports disk sizes for a **running** VM again. A running
  domain holds a write lock on its overlay, which made `qemu-img info` refuse to
  open it, so the virtual size, on-host size, and backing file silently
  disappeared from both the text and JSON output for every VM that was actually
  in use. The tool now passes `-U`, which overrides the lock check while still
  opening the image read-only.

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
