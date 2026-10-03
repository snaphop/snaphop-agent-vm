# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Add entries under `[Unreleased]` in the same change that introduces an
observable behavior, security, dependency, compatibility, or deployment
change. Describe what changed and why for a human reader.

While the project is `0.x`, breaking changes to the command line, configuration,
or on-disk state require explicit approval under `AGENTS.md` and must be called
out here explicitly, with the
migration or rebuild step a user has to take.

## [Unreleased]

### Added

- A base image can be a self-hosted GitHub Actions runner. Append `-runner` to
  a family: `agent-vm image build ubuntu-runner`, then `agent-vm create ci
  --distro ubuntu-runner`. The image is that family's slim image plus the
  runner, installed under `/opt/actions-runner` and not registered. After the
  VM boots, register it from inside the guest with `sudo
  agent-vm-github-runner configure --url <https-url> --token
  <registration-token>`. `--replace` clears a local configuration with no
  token and then registers again, so a runner of the same name still present
  at GitHub is replaced. Jobs run as the `runner` account, which has no sudo.
  Destroying the VM leaves the runner registered at GitHub. Unregister with
  `sudo agent-vm-github-runner remove --token <removal-token>` — a removal
  token — or in the GitHub UI. On Arch the image installs only the native
  libraries the runner needs and does not upgrade the rest of the system.
  The runner release pinned in the image is 2.337.0.

### Changed

- The default Ubuntu release is 26.04. `agent-vm create` and `agent-vm image
  build ubuntu`, with no tag, use `docker.io/library/ubuntu:26.04`. Ubuntu
  24.04 is still available as its own image: `agent-vm create work --distro
  ubuntu:24.04`, and the same `:24.04` suffix on `ubuntu-slim`, `ubuntu-nix`,
  and `ubuntu-runner`. An image already cached as 24.04, and VMs built from
  it, stay as they are. A create that names no tag builds 26.04 into a
  different cache directory.
- The default Fedora release is 44. `agent-vm create --distro fedora` and
  `agent-vm image build fedora`, with no tag, use
  `registry.fedoraproject.org/fedora:44`. Fedora 43 is available as its own
  image: `agent-vm create work --distro fedora:43`, and the same `:43`
  suffix on `fedora-slim`, `fedora-nix`, and `fedora-runner`. An image
  already cached as 42, and VMs built from it, stay as they are. A create
  that names no tag builds 44 into a different cache directory.
- The default Arch image tag is `base-20260927.0.600689`, the current
  official snapshot on `docker.io/library/archlinux`. `agent-vm create
  --distro arch` and `agent-vm image build arch`, with no tag, use that
  tag. The rolling tag is still available: `agent-vm create work --distro
  arch:base`, and the same `:base` suffix on `arch-slim`, `arch-nix`, and
  `arch-runner`. An image already cached as `base`, and VMs built from it,
  stay as they are. A create that names no tag builds this snapshot into a
  different cache directory. Building still upgrades packages from the Arch
  mirrors, so the tag names the starting image.

### Security

- Vulnerability reports are accepted only at <security@snaphop.com>.
- `agent-vm ssh`, `create`'s boot wait, `list`, and `info` no longer let a guest
  choose the address they connect to. The address used to come from the QEMU
  guest agent, which runs as root inside the untrusted guest, and anything it
  reported was accepted. Because ssh into a guest skips host-key checking, a
  malicious guest could redirect the operator's session — and the keys it
  offers — to the hypervisor, another VM, or a LAN host. NAT VMs now use only
  the address libvirt's own DHCP server leased. Bridged VMs still ask the guest
  agent, but only the interface libvirt defined, by the MAC recorded in
  `vm.json`, is used.
- `create` no longer uses an existing libvirt network that has the NAT
  network's name but does not forward by NAT. A bridged or routed network by
  that name would have put the guest on the LAN while `vm.json`, `list`, and
  `info` reported it as NAT; `create` now refuses it (exit `5`). It also no
  longer turns on autostart for a network it did not define.
- `update` no longer holds everything a guest prints in memory. A compromised
  guest could stream output for the whole of a step's timeout and exhaust the
  host's memory; only the last 64 KiB of each stream is now kept for error
  reports, while the full output still reaches the terminal.
- On a remote hypervisor, files in the state directory are now written through
  a temporary file that `mktemp` creates readable only by its owner, under a
  unique name. Cloud-init user-data used to be briefly readable by other users
  on the hypervisor while it was written, and two creates could collide on the
  same temporary file for the network definition.

- agent-vm no longer follows a broken shortcut (symlink) inside its state
  directory to a location outside it. Such a link used to pass the check that
  keeps every file the tool writes or deletes inside the state directory.
- A damaged or hand-edited VM record (`vm.json`) that points at another VM's
  directory, or at the whole state directory, is now refused, so `destroy`
  cannot be made to wipe other VMs or base images. The state directory itself
  is never deleted.
- The release process now pins every third-party build step to an exact
  version, and only the final publishing step can write to the repository.

### Fixed

- A `create` that is interrupted with Ctrl-C or `SIGTERM` now cleans up after
  itself. The cleanup used to run on the already-cancelled command, so it could
  not power off or undefine anything. It could leave a running domain with its
  state directory already deleted, which `agent-vm destroy` could no longer
  remove.
- A `create` whose `virt-install` fails, times out, or is interrupted after it
  has already defined the domain no longer leaves that domain behind, untracked
  and blocking the name.
- A `create` whose cleanup leaves host state behind now always exits `7`. It
  used to exit with the code of the original failure when that was a timeout,
  invalid input, or a missing tool.
- `image rm` and `image build --force` no longer remove or replace a base image
  that a `create` is still using. The image used to be removable between the
  moment `create` found it and the moment the VM was recorded, leaving that VM
  on a backing file that was gone. Both now refuse while a create is in
  progress (exit `5`).
- `image build --force` now refuses to rebuild an image that a recorded VM
  still uses, naming the VMs. Rebuilding in place left their disks on a
  different base, which corrupts them. Destroy them first, or replace the image
  deliberately with `image rm --force` followed by `image build`.
- `destroy --github-ssh-key` checks that `gh`'s token can manage SSH keys
  before deleting one. Without that scope GitHub answers with the same "not
  found" as a key that is already gone, so the destroy went ahead and deleted
  the only record of a key that was still on the account. When a key really is
  not there, the message now names the account `gh` is logged in to, since a
  key added from another account looks the same, along with the command that
  removes it.
- On a remote hypervisor, a `create`, `destroy`, or `image build` waiting for a
  lock no longer fails when the other process releases it at just the wrong
  moment.
- `destroy` no longer removes a VM that was destroyed and re-created under the
  same name while it waited for the VM's lock. It now reads the record again
  once it holds the lock, and leaves such a VM alone (exit `5`).
- `create` now checks your `--cloud-init` and `--opencode-config` files before
  doing anything else, including with `--dry-run`. A bad file no longer waits
  behind a base image build or leaves work half-done. A `--cloud-init` file
  with a line that begins with `--agent-vm-cloud-init` is refused, because that
  line would split the file in two.
- `create` now checks up front that `virt-make-fs` (libguestfs 1.50 or newer)
  is installed, instead of failing partway through.
- An SSH key whose comment contains invisible control characters is now refused
  with a clear message. Before, the VM booted with no user account and no way
  to log in.
- `destroy` no longer refuses to remove a VM this tool created when the state
  directory's path contains spaces.
- `agent-vm list` now shows your other VMs when one VM's record cannot be read,
  and warns about that one. It also ignores stray folders that are not VMs,
  such as `lost+found`. Removing a base image still refuses if any record
  cannot be read.
- A state directory reached through a different path than the one it was
  created under (a symlinked home, or `/home` against `/var/home`) keeps
  working.
- When a check for an existing VM or image fails (for example, a dropped
  connection to a remote hypervisor), `create` and `image build` now stop with
  the error instead of treating the VM or image as absent.
- Sizes written with a space, such as `4 G`, are accepted, and nonsense values
  such as `NaN` are rejected.
- When `HOME` is unset or relative, agent-vm now stops with a clear message
  asking for an explicit path, instead of creating files relative to the
  current folder. A relative `XDG_DATA_HOME` or `XDG_CONFIG_HOME` is ignored.
- If an image build is killed outright (for example by running out of memory
  or a host reboot), the next build, removal, or VM creation using that image
  deletes the gigabytes of temporary files it left behind.
- If an image rebuild is killed at the moment the new image replaces the old
  one, the old image is now restored automatically instead of becoming
  invisible to `image rm`. `image list` no longer shows such leftovers.
- Rebuilding an image no longer deletes a separately cached image whose tag
  ends in `.previous` (rebuilding `ubuntu:24.04` could delete
  `ubuntu:24.04.previous`).
- When a base image contains more than one kernel, the newest is now chosen by
  version: 6.10 used to lose to 6.9.
- `agent-vm doctor` no longer reports the host firewall as allowing VM internet
  access just because ufw has a rule for some other network interface, such as
  a VPN. It now checks that the rule covers the VMs' own network.
- Tab completion in bash now works for image names like `ubuntu:24.04` and for
  options written as `--output=json`.
- `agent-vm destroy --help` now lists the `--timeout` option, and
  `destroy --dry-run` no longer takes the VM's lock.
- `scripts/check.sh` no longer says "all checks passed" when golangci-lint was
  not installed and lint was skipped.

### Changed

- Misspelled or unknown settings in `config.toml`, and `vcpus = 0`, are now
  reported as errors instead of being silently ignored.
- A state directory whose path contains a quote or a backslash, such as
  `/home/o'brien`, is now refused with an explanation. Before, VM creation
  could fail with a confusing error or use the wrong file.
- Temporary build directories now sit inside each image's own folder
  (`images/<image>/.build-<tag>-<pid>`), and a rebuild keeps the image it
  replaces as `images/<image>/.<tag>.previous` until the new one is in place.
  Leftovers under the old names (`images/.build-*`) from earlier versions are
  not removed automatically; delete them by hand when no build is running.
- The CLI documentation now says that, on a remote hypervisor, Tab completion
  connects to that host; that `doctor` can warn about an outdated `gh`; and
  that `create` and `destroy` wait for another operation on the same VM to
  finish instead of failing.

## [0.1.0] - 2026-10-02

Entries record changes during development, including intermediate designs later
superseded in this section. For current behavior use [the CLI reference](./docs/cli.md).
In particular, full-image tooling is absent from `-slim` variants, mise installs
now share `/usr/local/lib/mise`, and the cloud-init seed remains attached for the
VM's lifetime (ADR-0011).

Image rebuild instructions below refresh the cache for **new** VMs. Destroy VMs
that depend on a base before rebuilding it, then recreate them to use the new
image and host-side kernel; rebuilding a backing file in place is unsafe for
existing overlays.

### Added

- **Repository review workflows.** A review of the complete bug, documentation,
  enhancement, or pull-request queue follows the workflows in `.agents/skills/`.
  Claude Code reaches the same files through symlinks in `.claude/skills/`.

- **License notices travel with the program.** `agent-vm licenses` prints the
  MIT license and the notices for the other software included in the binary
  (the TOML library and the Go runtime). A release includes `LICENSE` and
  `NOTICE` next to the binaries, and the same text is built into the binary,
  so copying the binary alone still carries the notices.

- **GitHub Actions and Dependabot.** Pull requests and pushes to `master` run
  `scripts/check.sh` (format, vet, lint, and unit tests). Pushing a `vX.Y.Z`
  tag builds the static Linux binaries and attaches them, with checksums, to
  a GitHub Release; a tag containing a hyphen is published as a pre-release.
  Merging still publishes nothing. Dependabot opens a weekly pull request for
  Go module updates and another for GitHub Actions updates.

- **Grok CLI, and `agy` installed with mise.** Full and nix base images install
  `agy` with mise (`mise use -g agy`, mise's registry name for the Antigravity
  CLI) instead of Antigravity's installer script, and install xAI's CLI as
  `grok` with `mise use -g npm:@xai-official/grok`. Both land in the shared
  mise store with a `/usr/local/bin` shim, so `ssh <vm> grok -p '…'` finds the
  command and `agent-vm update` moves them with `mise upgrade`. `grok` ships
  set to always-approve tool calls, and with its own updater turned off so it
  does not replace the binary mise owns. Images already in the cache keep the
  old `agy` and have no `grok`; rebuild with `agent-vm image build <distro>
  --force`.

- **Nix base images.** Every supported family now has a nix variant, named by
  appending `-nix` to the family: `agent-vm image build ubuntu-nix`,
  `agent-vm create work --distro arch-nix`. It boots exactly like the full
  image — same kernel command line, same cloud-init contract, same SSH and
  clock behavior — and is built from the same distro packages for everything
  that makes it boot. What changed is where the guest tooling comes from: one
  shared nix file, `templates/distro/agent-tools.nix`, instead of the family's
  own package manager and mise. The same file is used for all three families,
  so a tool set that used to be written out three times in three different
  package managers is now written once.

  A nix guest carries the common Linux tooling, the language toolchains (Go,
  Rust, Node.js, Python, the JDK and Maven, `golangci-lint`), `gh`, `tea`,
  `wrangler`, Chromium and Playwright's browsers, and `nix` itself, and can
  install more with `nix` as any account. Docker and the nested virtualization
  stack still come from the distro, because those are background services the
  system has to start and a nix profile only supplies programs.

  The coding agents are there and the per-account herdr servers start at
  boot, as on a full image. `claude` and `opencode` come from nixpkgs, `codex`
  from OpenAI's installer, and `pi`, `herdr`, `agy` and `grok` from mise —
  the tools nixpkgs does not package. mise installs no toolchain and no
  runtime here.

  One difference worth knowing: Rust comes as `rustc` and `cargo` from nixpkgs
  rather than through `rustup`, so `cargo build` works as usual but
  `rustup toolchain install` is not available; use `nix` for a second
  toolchain.

  A nix image is a separate base image with its own cache directory, manifest,
  and name, so `ubuntu`, `ubuntu-slim` and `ubuntu-nix` can be cached side by
  side and are built, listed, and removed independently. It is the largest of
  the three. `agent-vm update` refreshes a nix guest's distro packages and
  skips the steps for tooling it does not have; to change what the nix profile
  holds, edit `agent-tools.nix` and rebuild the image with `--force`.

  Which versions a nix guest gets is decided by the nixpkgs the file fetches.
  It ships following a release branch, which moves over time; run
  `scripts/pin-nixpkgs.sh` to lock it to an exact revision, after which two
  builds for the same architecture use the same nixpkgs-provided versions.
  Distro packages, Codex, mise-managed tools, and the Nix installer remain
  outside that pin. Rebuild the `agent-vm` binary after editing its embedded
  expression, before rebuilding the images.

- **Slim base images.** Every supported family now has a slim variant, named by
  appending `-slim` to the family: `agent-vm image build ubuntu-slim`,
  `agent-vm create work --distro fedora-slim`. It boots exactly like the full
  image — same kernel command line, same cloud-init contract, same SSH and
  clock behavior — and carries the same common Linux tooling (shells, editors,
  `git`, `curl`, a C toolchain, Python, network utilities), but leaves out
  everything the full recipe installs for a coding agent: mise and the language
  toolchains, the agents themselves, Chromium and Playwright, Docker, and the
  nested virtualization stack. For a VM that only has to run a build, a shell,
  or a test suite, that is a much smaller image and a much shorter build. A
  slim image is a separate base image with its own cache directory, manifest,
  and name, so `ubuntu` and `ubuntu-slim` can be cached side by side and are
  built, listed, and removed independently. `agent-vm update` skips the steps
  for tooling a slim guest does not have.

- **`agent-vm` now works on hosts whose own kernel cannot boot a libguestfs
  appliance.** libguestfs does all of its work — building the base image,
  writing the cloud-init seed — inside a small VM it boots for the purpose, and
  `supermin` builds that VM around *the host's own kernel*. A kernel built for
  one machine rather than for machines in general may not be able to boot it: an
  Apple Silicon (Asahi) kernel, for instance, has neither the serial port nor
  the PCIe host bridge QEMU's board provides, so the appliance came up with no
  console and no disks and every image build died with libguestfs saying only
  that the appliance "closed the connection unexpectedly" — which names neither
  the cause nor the fix. Set the new `appliance_kernel` option (or
  `AGENT_VM_APPLIANCE_KERNEL`) to a directory holding a general-purpose kernel,
  and libguestfs builds its appliance from that instead. The kernel is never
  booted by the host, only inside QEMU, so it does not have to support the
  host's hardware. Hosts with an ordinary distribution kernel need none of this
  and are unaffected. See "Hosts Whose Kernel Cannot Boot The libguestfs
  Appliance" in `docs/host-setup.md`.

- `doctor` gained a **libguestfs appliance** check, so the failure above is
  reported during setup rather than partway through an image build. On aarch64
  hosts it reads the kernel's configuration and fails when the options the
  appliance depends on are missing, naming them and the setting that fixes it;
  where `appliance_kernel` is configured it confirms that directory holds what
  it should instead. It reports `skip`, not a failure, when the kernel publishes
  no configuration to read.

- `doctor` and `create` now warn when the configured host bridge runs the
  spanning tree protocol with a non-zero forward delay. Such a bridge holds each
  guest's tap port in listening and learning before it forwards anything, so the
  guest cannot finish DHCP for twice that delay — about 30 seconds with the
  usual default — and every VM on that bridge takes that much longer to accept
  SSH. Nothing inside the guest can shorten it: the frames genuinely do not
  pass, which is why the wait looks like a slow image rather than a network
  setting. The warning names the command that removes it
  (`ip link set <bridge> type bridge stp_state 0`), and the VM is still created.
  `agent-vm` never changes host network configuration itself. See
  "Turn Off The Spanning Tree Forward Delay" in `docs/host-setup.md`.

- **`agent-vm` can now drive a hypervisor on another machine.** Point
  `--libvirt-uri` at `qemu+ssh://[user@]host[:port]/system` (or set
  `libvirt_uri` in the config file, or `AGENT_VM_LIBVIRT_URI`) and everything
  runs there: base images are built there, disks are created there, domains are
  defined there, and the state directory lives there. Your laptop needs nothing
  but `ssh` — no KVM, no libvirt, no podman, no libguestfs. `agent-vm ssh` and
  `agent-vm update` reach a guest through the hypervisor, because a NAT guest
  sits on a network that only exists on that host. Two things still run on your
  own machine and are unchanged: `gh`, which uses your GitHub login, and the
  connection into a guest, which uses your keys and your terminal.

  Requirements: `ssh <host> true` has to succeed without a prompt — use an SSH
  agent or a default key, or name one with `?keyfile=<path>` in the URI — and
  the hypervisor needs the tools `doctor` already lists, plus `flock`, `find`,
  and coreutils, which any Linux host running libvirt has. `--state-dir` now
  names a path *on the hypervisor*; left unset it defaults to
  `~/.local/share/agent-vm` in the home directory of the account the URI names.
  `--dry-run` prints the `ssh` invocations it would run, transport and all.
  See "Remote Hypervisors" in `docs/cli.md` and
  [Driving This Host From Another Machine](./docs/host-setup.md#10-driving-this-host-from-another-machine).

- `doctor` reports a remote hypervisor rather than the machine you ran it on:
  `/dev/kvm` and group membership are checked over the transport, and a new
  `hypervisor host <destination>` check runs first — if the host cannot be
  reached, that is reported on its own instead of eight checks failing for the
  same reason. Two checks cannot be answered from a client and now say so
  rather than guessing: the host firewall, and whether the account QEMU runs as
  can reach the state directory. Both point at running `agent-vm doctor` on the
  hypervisor itself.

- **`doctor` now checks that the host firewall lets a guest reach the host's
  DHCP and DNS**, as a new **host firewall guest services** check. `ufw`
  defaults to `deny (incoming)` and sends anything arriving on port 67 to that
  policy, so on a host with no rule for the NAT bridge the guest's DHCP request
  never reaches libvirt's dnsmasq: the VM boots, waits in
  `systemd-networkd-wait-online` forever, and `create` fails with "did not get
  an address" on a host `doctor` had just called ready. `doctor` previously
  noticed this only on a host that had already been given a forwarding rule, and
  its remaining warning described the milder forwarding problem instead
  ("a guest will boot, accept SSH and resolve DNS"), which pointed at the wrong
  fix. The check names the missing service — DHCP (67/udp), DNS (53), or both —
  and asks libvirt which bridge it allocated, so the remedy is a rule you can
  run as printed rather than one naming a `virbr0` that may not be yours. Like
  the forwarding check it only reads `ufw`'s configuration, never runs `ufw`,
  and warns rather than fails.

- Every base image now carries [Herdr](https://herdr.dev), a terminal workspace
  manager for coding agents, and starts a server for `root` and every
  interactive account at boot. The server owns the panes the agents run in, so
  an agent left working in one keeps working while nobody is attached: run
  `herdr` in an SSH session on the guest, or `herdr --remote <ip>` from your own
  machine, to attach to it and detach again. It is installed with `mise`
  (`mise use -g herdr`) like the `claude`, `opencode` and `pi` agents, so
  `agent-vm update` keeps it current and an account can move to another release
  itself. Each account's server is its own `agent-vm-herdr@<account>` systemd
  unit, started by `agent-vm-herdr.service`, and uses only that account's
  configuration and socket under `~/.config/herdr`; `systemctl status
  agent-vm-herdr@<account>` is where one that will not start says why, and
  `sudo systemctl stop agent-vm-herdr@<account>` turns it off. Accounts with no
  login shell, and accounts with no `herdr` of their own — the account a
  distribution bakes into its own image, such as Ubuntu's `ubuntu` — are
  skipped, with the reason in the journal. Existing base
  images do not have it — rebuild with `agent-vm image build <distro> --force` to get it.

- `agent-vm update <name>...` brings a running VM's Linux packages up to date
  from the host, without opening a shell in it: `apt-get update` and a
  non-interactive `apt-get dist-upgrade` on Ubuntu, `dnf --refresh upgrade` on
  Fedora, `pacman -Syu` on Arch, each followed by the family's cleanup step.
  `agent-vm update --all` does the same for every VM in the state directory,
  skipping the ones that are not running rather than starting them, and
  attempting every VM even after one fails.

  It also updates everything in a guest that its package manager does not know
  about, which is most of what the base images actually carry: `mise` itself,
  every tool `mise` manages — `node`, the `claude`, `opencode` and `pi` agents,
  `java`, `maven`, `go`, `golangci-lint`, `wrangler`, `playwright` and `cf` — for
  both root and the guest user, then `codex` and the shared Rust toolchain. A
  guest that does not have one of them, because it was built from an older or a
  hand-built image, has that step skipped and reported rather than failing the
  update. `agy` and the Playwright browser downloads are left alone: the first
  self-updates in the background, and the second is refreshed with `playwright
  install`.

  The guest's own output is streamed as it runs, `--timeout` (default 45m, up
  from 30m now that the toolchains are updated too) bounds each VM, and
  `--output json` reports per-VM results. Because guests boot their kernel from
  the base image on the host, this updates the software inside a VM but not the
  kernel it boots — a newer guest kernel still comes from `agent-vm image build
  <distro> --force`.

- Base images now carry `cf`, Cloudflare's newer CLI, installed with `mise` from npm
  (`mise use -g npm:cf`) on the same Node runtime as `wrangler` and Playwright,
  with a `/usr/local/bin` symlink so `ssh <vm> cf ...` works without a login
  shell. It ships with no credentials. Existing base images do not gain it —
  rebuild with `agent-vm image build <distro> --force`.

- Docker in a VM can now build for a foreign architecture. `docker build
  --platform linux/arm64 .` works on an x86_64 host, and `--platform
  linux/amd64` on an aarch64 one, with no setup inside the guest. Every base
  image installs the distro's `qemu-user-static` packages, and systemd
  registers their `binfmt_misc` rules on every boot with the fix-binary flag
  that makes the emulator reachable from inside a build container. This
  replaces the usual `docker run --privileged --rm tonistiigi/binfmt --install
  arm64`, which needs a registry round trip and is lost on reboot. Existing
  base images do not gain this — rebuild with `agent-vm image build
  <distro> --force` to pick it up. Building a single multi-platform manifest still
  needs `docker buildx create --use --bootstrap` first.

- `create --opencode-config <path>` installs your own `opencode.json` in the
  VM, replacing the permissive default the base image ships. The file is
  written to `/etc/skel/.config/opencode/opencode.json` and
  `/root/.config/opencode/opencode.json` at first boot, so both the login user
  and `root` start with it — no image rebuild, and no hand-editing after
  logging in. It must be valid JSON, which is checked before anything is
  created; like `--cloud-init` data, its contents are never logged.

- `create --max-memory <size>` gives a VM memory it can grow into. `--memory`
  stays what the guest boots with; `--max-memory` is the ceiling it may reach
  while it runs, and the difference between the two becomes a `virtio-mem`
  device that starts with nothing plugged in. A VM created with
  `--memory 4G --max-memory 16G` boots with exactly 4 GiB and can be grown
  afterwards without a reboot:

  ```console
  $ virsh update-memory-device agent-01 --requested-size 8G --live
  ```

  Shrinking is the same command with a smaller size, though a guest may refuse
  to hand a block back. The ceiling is also settable as `max_memory` under
  `[defaults]` in the configuration file and as `AGENT_VM_MAX_MEMORY`, it is
  recorded in `vm.json` as `resources.maxMemory`, and `list` and `info` show it
  next to the boot memory. Growth room must be a multiple of 2 MiB, and
  `--max-memory` must exceed `--memory`; both are checked before anything on
  the host changes.

  **A VM created without `--max-memory` is defined exactly as before** — no
  `maxMemory`, no guest NUMA topology, no memory device — so nothing changes
  for anyone not asking for this.


- Building a base image now shows its progress. On a terminal it is one line,
  redrawn in place, with a bar, the step number, what the step is doing, and the
  elapsed time — so a build that spends minutes inside `podman build` or
  `virt-sysprep` no longer looks like a hung terminal. Where the output is
  captured instead of displayed — a pipe, a log file, a CI job, or a
  `--verbose` run whose logs share stderr — each step is one plain
  `[4/10] exporting the root filesystem` line and no terminal control
  characters are written. It goes to stderr like every other progress message,
  so `--output json` is still safe to pipe, `--quiet` silences it, and a cached
  image that needs no build reports nothing. `create` reports a base image it
  has to build the same way.

- Base images now carry `wrangler`, Cloudflare's CLI, installed with `mise`
  from npm (`mise use -g npm:wrangler`) on the Node runtime the image installs
  for it and Playwright — npm is the only place Cloudflare publishes it, and it
  and Playwright are the only npm packages in the image. Like the agents, it has
  a `/usr/local/bin` symlink to the `mise` binary, so `ssh <vm> wrangler deploy`
  works without a login shell. It ships with no credentials — `wrangler login`
  is an OAuth flow and an API credential is per-VM — and
  `WRANGLER_SEND_METRICS=false` is set in `/etc/environment`, so a guest reports
  no anonymous usage metrics unless its operator unsets it.
  **Run `agent-vm image build <distro> --force` to pick this up.**

- Base images now carry a Rust toolchain: `rustup` with the stable toolchain,
  so `rustc`, `cargo`, `rustfmt`, and `clippy` are in every VM. It is installed
  once into `/usr/local/rustup` and shared by every account rather than
  downloaded per user at first use, which a guest without outbound network
  access could not do — so `rustup update` needs `sudo`, while `cargo install` still writes
  into the account's own `~/.cargo`. The entry points are in `/usr/local/bin`,
  so they work in a non-interactive `ssh <vm> cargo build` and not only in a
  login shell. `RUSTUP_HOME` is set in `/etc/environment` so the proxies find
  the shared toolchain from a non-interactive command too. Rust is deliberately
  not managed by `mise` the way Go and the JDK are: mise's `rust` is `rustup`
  underneath and re-reads `RUSTUP_HOME`/`CARGO_HOME` from the environment of
  whoever runs `cargo`, so a shared installation makes it re-run `rustup-init`
  as each account and fail, and a per-account one costs roughly 1.5 GiB per
  account. **Run `agent-vm image build <distro> --force` to pick this
  up** — existing cached images have no Rust.

- `claude` in a VM now starts with Remote Control enabled in every session. Base
  images set `remoteControlAtStartup` in the per-account settings file, which is
  the settings-file equivalent of `claude --remote-control`; claude ignores that
  setting from project or local settings, so the per-account file is the only
  place that can turn it on. It needs credentials that arrive per-VM, so on a VM
  where nobody has run `claude login` a session starts without Remote Control
  connected. **Run `agent-vm image build <distro> --force` to pick this up.**

- Every VM now starts Codex's remote-control daemon at boot, for `root` and for
  every interactive account, through the new
  `agent-vm-codex-remote-control.service` unit. Remote control needs
  credentials, which are per-VM and never baked into a base image, so
  on a VM where nobody has run `codex login` the attempt fails and is reported
  in `journalctl -u agent-vm-codex-remote-control` — deliberately without
  failing the boot. After logging in inside the VM, `sudo systemctl start
  agent-vm-codex-remote-control` starts it.

- `agent-vm create` no longer needs `--ssh-key` on a host where you already
  have an SSH key. When no key is named by the flag, `AGENT_VM_SSH_KEY`, or the
  config file, it authorizes the public halves of this account's OpenSSH
  identities — `~/.ssh/id_ed25519.pub`, `id_ed25519_sk.pub`, `id_ecdsa.pub`,
  `id_ecdsa_sk.pub`, `id_dsa.pub`, `id_rsa.pub`, and `id_xmss.pub` — the same
  files `ssh` offers when run without `-i`. Every one that exists is used;
  files that are absent or that do not hold a public key are skipped. Naming a
  key turns the fallback off, and a host with no key and none of these
  identities is still a usage error, now naming the files that were tried.

- `agent-vm create --host-authorized-keys` also authorizes the keys that
  already log in to this host account inside the new VM, so whoever can log in
  to the host can log in to the VMs it creates. It reads both
  `~/.ssh/authorized_keys` and `~/.ssh/authorized-keys/authorized_keys` — sshd
  is routinely pointed at the second — and skips whichever is absent. It
  combines with `--ssh-key`: the guest gets the keys named by the flag and the
  host's keys, with a key present in more than one of them authorized once.
  Either source alone is enough.

  Only plain key lines are used — comments and blank lines are skipped, and an
  entry carrying OpenSSH options (`command=`, `restrict`, `from=`) is refused
  rather than silently stripped of its restriction or silently dropped. Finding
  no host key at all is reported as a usage error before anything is created.
  Every host key file that was read is recorded in `vm.json` as a path; the
  host public key contents are not copied into that record. The separate
  `--github-ssh-key` feature records the guest-generated public key there.

- `agent-vm create --github-ssh-key` adds the SSH key a VM generates for itself
  on first boot to your GitHub account, so an agent inside the VM can push
  without a key being pasted in by hand. The key is titled
  `agent-vm <name> on <host>`, and `agent-vm destroy --github-ssh-key` removes
  that same key again when the VM goes away.

  `gh` does the talking, on the host, with your existing login: no GitHub
  credential ever enters the untrusted guest, and the guest's private key never
  leaves it. `gh` is optional — `doctor` reports it as `skip` when it is absent,
  and every other command works without it.

  Removing the key is opt-in on both ends. A `destroy` without the flag says the
  key is still on your account and prints the `gh` command that removes it,
  rather than deleting anything you did not ask it to.

- An interactive SSH login now lands on a tmux session menu: start a session,
  attach to one (by number, or by name with TAB completion), list them, or quit
  to a plain shell. Initially detaching returned to the menu; the later menu
  change closes the SSH session instead. Run `agent-vm-menu` to open the menu
  from a shell.

  It runs only for a person at a terminal — an interactive shell with a
  terminal on both ends, not already inside tmux — so `agent-vm ssh <vm> --
  <command>`, `rsync`, and anything else scripted is untouched. Set
  `AGENT_VM_NO_MENU=1`, or remove `/etc/profile.d/zz-agent-vm-tmux-menu.sh`
  through `--cloud-init`, for a plain shell at every login.

- Base images now carry the virtualization stack, so a guest can run VMs of its
  own — including another `agent-vm`: `qemu-kvm`, libvirt (enabled at boot),
  `virsh`, `virt-install`, `guestfs-tools`, `dnsmasq`, and `podman`. Nested
  virtualization is enabled inside the guest as well, and every VM already ran
  with the host CPU, so `/dev/kvm` works inside one. This needs nested
  virtualization enabled on the host; `docs/host-setup.md` says how to check.
  On Ubuntu the image takes `dnsmasq-base` rather than `dnsmasq`: libvirt runs
  its own instance per network, and the full package's system-wide resolver
  would contend with it.

- Base images now carry a Go toolchain and `golangci-lint`, installed with
  `mise` (`mise use -g go@latest golangci-lint@latest`) rather than from a
  distribution package. Every family packages a different and often years-old
  Go, and a guest whose Go is older than the `go` directive of the repository an
  agent was handed cannot build it at all; all three families now carry the same
  release, and Ubuntu does not package `golangci-lint` at all. `go`, `gofmt`,
  and `golangci-lint` each have a `/usr/local/bin` symlink to the `mise` binary,
  so a non-interactive `ssh <vm> go build` works without a login shell, and they
  are installed into `/etc/skel` like the JDK, so an account can move to another
  release with `mise use -g go@1.25` without `sudo`. `~/go/bin` and
  `~/.cargo/bin` are added to the path of a login shell.

- Base images now carry a JDK and Maven, installed with `mise`
  (`mise use -g java@temurin maven@latest`), so `java` and `mvn` work in a guest
  without downloading anything, including when outbound network access is
  unavailable. The newest Temurin JDK mise offers and the current Maven are installed
  during the build; neither version is pinned. The JDK is requested as
  `java@temurin` rather than `java@latest`, which would be an Oracle build of
  OpenJDK. Both are reached through mise's shims, which
  `/etc/profile.d/agent-vm-mise.sh` puts on the path of a login shell only, so
  from a script use `ssh <vm> bash -lc 'mvn -version'` — or put
  `~/.local/share/mise/shims` on `PATH` in the script itself, since the shims
  are ordinary executables and there is nothing to source. Switching versions is
  `mise use java@21`, per account and without `sudo`.

- Every interactive account in a guest now gets an `ed25519` SSH key pair at
  `~/.ssh/id_ed25519`, generated on first boot if that path does not already
  exist. It is generated inside the guest and never leaves it — no private key
  goes into a base image or a cloud-init seed — and an existing key
  generated inside the guest is left alone. Host private keys must not be
  supplied through `--cloud-init` (`SECURITY.md`).

- The generated cloud-init user-data now creates the login user in the
  `libvirt` and `kvm` groups as well as `docker`, so `virsh` works without
  `sudo` in the very first SSH session. This is a change to the generated
  user-data, which is a public contract: the golden files record it. A base
  image built before this change has no such groups, and still boots — the
  user-data declares them, and cloud-init creates groups before users.

  The base image's one-shot account unit, which is the backstop for accounts
  the seed does not create, was renamed from `agent-vm-docker-group.service` to
  `agent-vm-user-setup.service` and now does both jobs above.

- Base images now also carry `gh` and `tea` for GitHub and Gitea, and Playwright
  with a headless `chromium`.

  `gh` comes from each distribution's repository. `tea` does not — only Arch
  packages it, and on Ubuntu the name `tea` belongs to an unrelated text editor,
  so it is fetched from Gitea's release server on all three families instead.

  There is one browser in the image, the build Playwright pins, and it is also
  exposed as plain `chromium`. Playwright will not drive a browser it did not
  install, and Ubuntu's chromium package is a snap stub a VM cannot run, so a
  second one would be several hundred megabytes that nothing uses. The browsers
  are shared from `/opt/ms-playwright` rather than downloaded per user, and
  `PLAYWRIGHT_BROWSERS_PATH` is set in `/etc/environment` so that
  non-interactive commands like `ssh <vm> node script.js` find them too.

  A build now runs each of these once — including launching headless Chromium —
  and fails if any of them cannot work. Base images are correspondingly larger.

  Existing cached base images are unaffected and still boot. They will not have
  these tools until rebuilt with `agent-vm image build <distro> --force`.

- Base images now ship five coding agents — `claude`, `codex`, `opencode`, `pi`,
  and `agy` — so a new VM is usable by an agent the moment it becomes reachable,
  instead of starting with an install. All five are vendor-built native
  binaries. `claude`, `opencode`, and `pi` are installed with `mise`
  (`mise use -g claude opencode pi`), into `/etc/skel` so each account inherits
  its own copy and can move one to another release with
  `mise use -g claude@<version>` without `sudo`; each also has a
  `/usr/local/bin` symlink to the `mise` binary, which resolves the version from
  the calling account's own configuration, so a non-interactive
  `ssh <vm> claude -p '…'` finds the command. `codex` and `agy` come from their
  vendors' own installer scripts into `/usr/local/bin`, where every account on
  the VM finds them. Versions are not pinned:
  a guest gets whatever was current when its base image was built, and
  `agent-vm image build <distro> --force` is how you get newer ones.

  Each agent is configured in its **most permissive mode**, so it works
  unattended rather than blocking on an approval prompt nobody is there to
  answer. This depends on the VM boundary and the host's network policy holding.
  Default NAT does not by itself prevent access to host services, the LAN, or
  other guests; see `docs/host-setup.md` for the limits of that boundary. `agy` is the exception to the mechanism: it has no configuration
  file for permissions, so it gets a shell alias instead, which reaches
  interactive shells only. A non-interactive caller such as
  `ssh <vm> agy -p '…'` has to pass `--dangerously-skip-permissions` itself.

  The agents ship **configured but unauthenticated**. A base image is shared by
  every VM built on it, so no credentials are baked in; API keys or logins have
  to reach each VM separately, through `--cloud-init` or by authenticating inside
  the guest.

  A build now runs each agent once before finishing and fails if any of them
  cannot start, so an agent that installs but does not work is a failed build
  rather than a surprise inside a VM days later.

  Existing cached base images are unaffected and still boot. They will not have
  the agents until rebuilt with `agent-vm image build <distro> --force`.

- `agent-vm completion <bash|zsh|fish>` prints a tab-completion script for the
  shell you name, so `agent-vm ` and Tab offers command names, a command's own
  flags, the recorded VM names for the commands that take one, the cached base
  images for `image inspect`/`image rm`/`--distro`, and the supported distro
  families for `image build`. Install it with, for example, `agent-vm
  completion bash > ~/.local/share/bash-completion/completions/agent-vm`;
  `docs/cli.md` has the line for each shell. The scripts ask the binary for
  candidates as you type, so they keep working as commands and flags change.

- Base images now ship the tools an agent expects to find on a working
  machine, so a new VM is usable immediately instead of starting with a package
  install. That means networking and diagnostic tools (`ping`, `traceroute`,
  `dig`, `netcat`), `curl`, `wget`, `rsync`, `git`, a C/C++ toolchain, Python 3
  with `pip`, the usual shell tooling (`jq`, `unzip`, `less`, `vim`, `tmux`,
  `htop`, `tree`, `man`), and Docker with `compose` and `buildx`. Docker starts
  at boot, and the login user is placed in the `docker` group when cloud-init
  creates the account, so `docker` needs no `sudo` from the first SSH session
  on; a one-shot unit in the image adds any other interactive account to the
  group after cloud-init finishes. Everything comes from each distro's own
  repositories — Ubuntu's `docker.io`, Fedora's `moby-engine`, Arch's `docker`
  — so no third-party repository, key, or install script enters a build. The
  packages live in the cached base image rather than in per-VM cloud-init, so
  `create` stays fast and works offline; the cost is a larger base image
  (roughly 1.2 GB for Ubuntu, 1.7 GB for Fedora and 2.1 GB for Arch, against
  0.2-0.8 GB before) and a longer one-off `image build`. Existing base images
  stay valid and bootable and simply lack the new tools — run `agent-vm image
  build <distro> --force` to refresh one.

- Base images now ship a tmux configuration, so tmux behaves the same way in
  every guest without anyone pasting a config in. It is installed as
  `~/.tmux.conf` for root and for the login user cloud-init creates, and turns
  on mouse support and a 100,000-line scrollback, vi-style copy-mode keys, `|`
  and `-` for splits, new windows and panes opening in the current pane's
  directory, and no status bar. Supplying your own `~/.tmux.conf` — by hand or
  through `--cloud-init` — replaces it without rebuilding the image. Existing
  base images stay valid and simply lack the file; run `agent-vm image build
  <distro> --force` to refresh one.

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
  default, or has rules accepting forwarded traffic, naming the interfaces
  those rules cover. It warns rather than fails, because the live ruleset needs
  root to read and a false failure would exit non-zero on a working host.
  Forwarding is only half of what NAT mode needs, so the check covers the other
  half too: a guest asks the host's dnsmasq for its DHCP lease and its DNS, and
  that traffic is inbound rather than forwarded, governed by `ufw`'s separate
  `deny (incoming)` default. A host carrying only the route rule looks
  configured and still produces guests that never get an address, so an
  interface allowed to forward but not allowed to answer DHCP (67/udp) or DNS
  (53) is reported, with the rules to add for that specific interface.

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

- **The GitHub Actions workflow for pull requests and pushes to `master` is
  named Verify** (`.github/workflows/verify.yml`). It still runs
  `scripts/check.sh`.

- The official name is SnapHop Agent VM. The command is still `agent-vm`, and
  the repository is still <https://github.com/snaphop/snaphop-agent-vm>.
- The source repository is <https://github.com/snaphop/snaphop-agent-vm>,
  released under the MIT License (copyright 2026 SnapHop). The Go module path
  is `github.com/snaphop/snaphop-agent-vm`. A checkout that still imports
  `git.snaphop.xyz/snaphop/snaphop-agent-vm` needs that import path updated
  before it will build.

- **A VM's first boot is about nine seconds shorter, and writes 1.7 GiB less to
  its disk.** The base images kept the whole `mise`-managed toolchain — the JDK,
  Maven, Node, Go, and the coding agents — in `/etc/skel`, so `useradd` copied
  all of it into the login user's home before cloud-init could get to the SSH
  keys. It now lives in `/usr/local/lib/mise`, one store every account shares,
  and each account's `~/.local/share/mise` is a symlink to it.

  Nothing an account can do changes: every directory in the store is writable
  with the sticky bit, the way `/tmp` and the Playwright browser directory
  already are, so `mise use -g node@24` still works without `sudo` and still
  affects no other account — the installs are shared, but *which* version an
  account uses stays in its own `~/.config/mise`. No account may remove
  another's tool.

  This takes effect when a base image is rebuilt (`agent-vm image build
  --force`). Existing VMs and existing base images are untouched: an account
  that already has its own copy of the toolchain keeps it, and first boot leaves
  it alone rather than replacing it with a link.

- **The cloud-init seed is now built by `agent-vm` and attached as a read-only
  virtio disk**, instead of being built and attached by
  `virt-install --cloud-init` (ADR-0011). This fixes VMs on ARM64 hosts, where
  every `create` produced a guest that booted and could never be logged into:
  virt-install attaches its seed as a USB CD-ROM on a machine type with no SATA
  bus, and USB storage is enumerated about a second after cloud-init has already
  chosen its datasource, so the guest came up with no login user and no
  authorized key and `create` failed at `Waiting for SSH` with
  `Permission denied (publickey)`. A virtio disk is probed with the root disk,
  long before cloud-init looks for it.

  Visible effects: a VM's state directory gains a `seed/` directory holding the
  `user-data` (moved there from the top level) and a new `meta-data`, plus the
  `seed.img` built from them; `vm.json` records all four paths; the guest has a
  second, read-only virtio disk (`vdb`) that stays attached for the life of the
  VM rather than vanishing after the first boot; and `virt-make-fs` — already
  required for building base images — is now also used by `create`. Existing VMs
  are unaffected. Host credentials and private keys remain prohibited. Any
  operator-supplied user-data is retained in the seed for the VM's lifetime
  and must follow `SECURITY.md`.


- `create --no-start`'s refusal message no longer says the seed is attached only
  to the first boot; since ADR-0011 the seed disk stays attached for the life of
  the VM. The flag is still rejected, for the reason that still holds:
  `virt-install` always boots the guest it defines.

- A failing tool now says which machine it ran on, so an error from a remote
  hypervisor cannot be mistaken for one from your own host. ssh failing to
  connect is reported separately from a tool failing on the far side, because
  they need different fixes.

- `doctor`'s `libvirt` group check and its refusal of bridged networking now
  key off whether the connection is a `/session` one rather than matching the
  literal string `qemu:///session`, so they behave correctly for
  `qemu+ssh://host/session` too.

- The tmux session menu an interactive login lands on is quicker to get through
  and no longer leaves you at a menu after a session ends. Pressing Enter at the
  menu starts a session straight away under a generated name, with no second
  prompt; generated names are now two short words (`cooker-opines`) instead of
  `agent-<hex>`; every image now installs a word list (`wamerican` on Ubuntu,
  `words` on Fedora and Arch) for them, and a guest without one falls back to
  `session-<hex>`.
  Attaching now replaces the menu process rather than running underneath it, and
  sets `detach-on-destroy on` and `exit-empty on`, so detaching or ending the
  last session closes the SSH connection instead of dropping back to the menu.
  Each session also gets `TMUX_SESSION_NAME` in its environment. Existing base
  images keep the old menu; rebuild with `agent-vm image build <distro> --force`.

- The Fedora and Arch base images now install `growpart`
  (`cloud-utils-growpart` / `cloud-guest-utils`), so cloud-init grows the root
  partition to the VM's disk size at first boot the way it already did on
  Ubuntu. Without it a guest was limited to the base filesystem plus 1 GiB of
  slack; with the larger `/etc/skel` this release ships, first boot filled that
  filesystem and the VM came up without SSH.

- The Fedora base image now installs `libatomic`, which the official Node
  binaries `mise` downloads are linked against and Fedora's base image does not
  carry. Without it the image build fails at the Node install.

- Base images now install **Node.js with mise** (`mise use -g node@latest`)
  rather than from a distro package or a third-party APT repository — no build
  adds an external repository or GPG key, and Ubuntu 24.04's packaged Node.js 18
  is past end of life. `node`, `npm`, and `npx` are versioned the way the JDK
  and the agents are: installed into `/etc/skel` during the build so every account
  inherits them, with a `/usr/local/bin` symlink each so a non-interactive
  `ssh <vm> node script.js` still finds them. An account can move to another
  release with `mise use -g node@<version>` without `sudo`, where a packaged
  `/usr/bin/node` needed root. The build still fails outright if the Node.js it
  ends up with is older than 22.19. Existing base images are unaffected —
  rebuild an image (`agent-vm image build <distro> --force`) to pick this up.

- `codex` in a base image comes from OpenAI's own installer
  (`https://chatgpt.com/codex/install.sh`) rather than from npm or `mise`.
  `codex remote-control` only runs against the standalone package that
  installer produces — it starts its app-server from a fixed path under the
  account's `CODEX_HOME` and refuses to run when that directory is missing,
  which is what an npm install leaves behind. The package is installed once
  into `/usr/local/lib/codex` and shared by every account (it is around
  300 MiB), with the command in `/usr/local/bin` as before, and each account
  gets a symlink to it under `~/.codex` at first boot so credentials and
  configuration stay per-account. **Run `agent-vm image build <distro> --force`
  to pick this up** — existing cached images still have the npm build.

- Base images are no longer built purely from their distribution's own
  repositories. The coding agents, the language toolchains, `tea`, and
  Playwright are not packaged by any distro, so a build now also reaches `mise`
  and its backends, `rustup`, and two vendor install scripts. A build fails
  outright if the Node.js it ends up with is older than 22.19, rather than
  producing an image whose agents silently cannot start. Building a base image
  already required network access to a registry; it now also requires reaching
  these sources.

- The state directory now also contains `networks/`, holding the network XML
  passed to `virsh net-define` as a record, and `locks/`. Both are documented in
  `docs/cli.md`.

- The NAT network is defined on `192.168.171.0/24` rather than colliding with
  libvirt's own `default` network on `192.168.122.0/24`, and it does not name a
  bridge device, so libvirt allocates one. Documented in `docs/host-setup.md`.

- `create --no-start` is now rejected with exit `2` and an explanation, instead
  of being listed as a working flag. The original rationale relied on
  `virt-install --cloud-init` attaching a transient seed only for first boot.
  ADR-0011 later replaced that seed with a persistent virtio disk; the flag
  remains rejected because the current create invocation starts the domain. Create the VM and stop it instead:
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
  `virt-sysprep`; cloud-init seeds were initially built by `virt-install`
  (replaced by `virt-make-fs` in ADR-0011); and `ssh` and
  `virsh console` are exec'd directly. The build is now pure Go with no cgo, and
  the required host tools and their minimum versions are documented and checked by
  `agent-vm doctor`.

- `create`, `destroy`, `image build`, and `image rm` now report a lock they
  could not release instead of discarding the failure. Kernel advisory locks
  are released when their holding process exits; persistent lock files are
  reusable and must not be deleted to bypass a running operation.

- Base images now boot with `memhp_default_state=online_movable` on the kernel
  command line, so memory added to a running guest becomes usable RAM instead
  of offline blocks nobody brought online. It has no effect on a VM without a
  `--max-memory` ceiling, since nothing is ever added to one. Each base image
  records the command line it was built with, so an image cached before this
  change keeps the old one — **run `agent-vm image build <distro> --force` to
  rebuild an image you want to grow VMs from.**

### Security

- **Vulnerability reports stay off the public tracker.** This repository is
  public, so a suspected vulnerability is reported by email to
  security@snaphop.com, not by a GitHub issue, pull request, or discussion.
  `SECURITY.md` now states the
  same fail-closed, credential, logging, and release boundaries in the form
  used by the other SnapHop security policies, without changing the guest
  isolation rules.

- Documented the project's hard security boundaries in `SECURITY.md`: the guest
  is untrusted, no host filesystem or credential reaches a VM by default, only
  SSH public keys are injected, bridged networking (which removes the NAT
  boundary) is always opt-in, and destructive operations are confined to state
  this tool created.

### Removed

- The template setup checklist (`docs/project-setup.md`), which no longer applies
  now that the repository is a real project; it became `docs/host-setup.md`.

### Fixed

- Corrected the documentation for image variants and updates, Nix pinning,
  remote image platforms, doctor checks, NAT isolation limits, and safe base
  image rebuilds. Added the missing Nix ADR index entry and consolidated
  duplicate changelog categories. Runtime behavior is unchanged.

- `agent-vm update` can update `mise` again. Root's `mise self-update` was
  given `TMPDIR=/root/.cache/mise-tmp`, a directory no guest has, and `mise`
  writes the downloaded release to a file directly in that directory without
  creating it. The step stopped with "No such file or directory" at
  `/root/.cache/mise-tmp/.tmp…` and left `mise` at the version the image
  shipped. The update now creates that directory before running `mise`.

- **The nix base images build again.** `agent-vm image build ubuntu-nix` (and
  the arch and fedora variants) failed four different ways, all of them in the
  recipe rather than in nix. The installer refused to run because nix defaults
  its build-user group to `nixbld` when it runs as root and the recipe created
  that group only later, so the build now writes single-user settings to
  `/etc/nix/nix.conf` first and the multi-user block still replaces them for the
  guest. `nix-store --optimise` was dropped: it deduplicates by renaming a
  temporary hardlink over a store path, which fails with a stale file handle on
  the overlay filesystem every container build layer lives on, so the step could
  never succeed — the guest's store, on a real root filesystem, is where
  deduplication belongs. `java`, `javac`, `mvn`, `cargo-fmt` and `cargo-clippy`
  were missing from the commands the recipe links into `/usr/local/bin`, so the
  JDK, Maven, `cargo fmt` and `cargo clippy` were installed but unreachable to
  anything that does not read a login shell's profile — an `ssh <vm> mvn
  package` included.

- **Multi-user nix works in a nix guest.** The daemon was enabled but never
  started: its socket unit requires `/nix/var/nix/daemon-socket` to exist, the
  single-user install used during the image build does not create it, and
  systemd silently skipped the unit at every boot while reporting it enabled.
  `/nix/store` was also left root-owned, so the build users had nowhere to
  write. The `agent` account could not install anything with nix -- the failure
  a base image is supposed to make impossible. Both are now set up during the
  build, and an unprivileged `nix build` works in a fresh VM.

- **arch-nix guests run Arch's python scripts on Arch's python.** The nix
  profile's `python3` is linked into `/usr/local/bin`, which precedes `/usr/bin`
  on the default PATH, and Arch is the one family shipping `#!/usr/bin/env
  python3` scripts. `virt-install` failed to build the image outright, and
  `cloud-init` would have failed at first boot -- as a guest that never received
  its SSH key. Those shebangs are now pinned to `/usr/bin/python3`.

- **`chromium` finds its browser in a nix guest.** The wrapper searched
  `/opt/ms-playwright` with `find`, which does not descend a symlink unless
  asked to. In a nix image every entry there is a symlink into the nix store, so
  the search came up empty and `chromium` reported no browser next to a browser
  that was plainly installed. The full and slim images were unaffected, and the
  fix (`find -L`) changes nothing for them.

- **Guests no longer come up with a clock weeks in the past.** On aarch64 —
  which in practice means an Apple Silicon host running Asahi Linux — the VM had
  no working real-time clock: QEMU's `virt` machine provides a PL031, but
  Ubuntu's `linux-image-virtual` ships `rtc-pl031` only in
  `linux-modules-extra`, which it does not install, so `/dev/rtc0` never
  appeared and nothing set the clock from hardware at boot. systemd fell back to
  its own build date and the guest started weeks behind its host (37 days, on
  the host this was found on), with nothing running afterwards to correct it.
  The consequences looked unrelated to time: `apt-get update` rejected
  repository metadata as "not valid yet", TLS handshakes failed against
  certificates that had not started yet, and build tools wrote timestamps from
  the wrong month. x86_64 hosts were unaffected, because the CMOS driver is
  built in there. The base images now ship and enable `chrony`, so a guest
  corrects its clock within seconds of the network coming up.
  `systemd-timesyncd` was tried first and rejected: started at boot before
  `systemd-resolved` could answer, it never acquired a server address and never
  retried, leaving a guest 37 days behind with the service reported "active" —
  chrony retries, carries several pools rather than one name, and steps the
  clock however large the offset is. This needs a rebuilt base image: run `agent-vm image
  build <distro> --force`, or `agent-vm image rm <distro>` and let the next
  `create` rebuild it. Existing VMs keep the old behavior until they are
  recreated on a rebuilt image. `chrony-wait` is enabled too and `sshd` is
  ordered behind `time-sync.target`, so a VM is not handed over until its clock
  has been stepped — `create` calls a VM ready when SSH answers, and without
  this it answered about five seconds into a boot while chrony stepped the clock
  at about seven, leaving an agent working against a clock weeks out for the two
  seconds in between. `chrony-wait`'s start timeout is shortened from three
  minutes to thirty seconds so that a guest with no route to an NTP server
  becomes reachable half a minute late with a wrong clock, rather than not
  before `create` stops waiting for it. `chronyd` is ordered behind
  `network-online.target` so that it can synchronize inside that thirty seconds:
  started earlier, its one attempt to resolve a pool address failed and the
  retry backed off, so on Fedora and Arch a source was not selected until about
  thirty-three seconds in — past the bound, which released `sshd` on a timeout
  rather than on a correct clock, and added about twenty seconds to every boot.
  Ordering it behind the network moved that to about five seconds. Finally,
  `systemd-timesyncd` is masked wherever a family enables it by default (Arch
  does), so that two NTP clients no longer step the same clock — `chronyd`
  reported `System clock interference detected (another NTP client?)` when they
  ran together.

- **A rejected flag no longer takes the command's flag listing with it.**
  Reporting a bad flag in the tool's own spelling also stopped printing the
  usage listing that came with it, so `agent-vm create --test` answered with one
  line and a pointer to `--help`. The error is now followed by that command's
  invocation and every flag it takes, with each flag's default where it has one
  — the detail the flag package's listing used to carry, spelled the way this
  tool accepts. A wrong or missing argument prints the same listing.

- **A rejected flag was reported in a spelling `agent-vm` does not accept.**
  `agent-vm image build --test` answered `flag provided but not defined:
  -test` — one dash, and printed twice, once by Go's flag package and once by
  `agent-vm` — followed by a `Usage of image build:` listing that spelled every
  flag with one dash as well. None of those are flags this tool takes, so the
  message quietly disagreed with `docs/cli.md`, `--help`, and the shell
  completions. A bad flag is now reported once, named the way it was typed and
  the way it is documented (`--test`).

- **`agent-vm <command> --help` failed instead of helping.** It printed the
  flag package's usage dump and then exited `2` with `flag: help requested`.
  Asking for help now prints the command's documented invocation and its own
  flags, with two dashes, and exits `0` — the way the global `--help` already
  did.

- **`agent-vm create` ignored its own flags.** `--vcpus`, `--memory`, `--disk`,
  `--max-memory`, `--distro`, `--network`, `--bridge`, and `--ssh-key` were all
  silently dropped, and every VM was created with the configured defaults
  instead — at the default size, on the default network, authorizing the default
  keys. Setting up a run reads configuration before the subcommand does, to work
  out which machine the hypervisor is on, and the result was cached and handed
  back to `create` with none of its flags applied. Nothing reported the loss;
  the VM was simply not the one that was asked for. `--dry-run` showed the same
  wrong values, so the printed plan matched what would really have run.

- **`create` now works on an aarch64 host.** `virt-install` failed with
  `unsupported configuration: ACPI requires UEFI on this architecture`: it
  turns ACPI on by default, and libvirt refuses ACPI on aarch64 unless the
  domain also has UEFI firmware, which a directly booted kernel does not have.
  `create` now asks libvirt for the hypervisor's architecture (`virsh
  capabilities` — the hypervisor's, not your machine's, so a `qemu+ssh://`
  connection to an aarch64 host is answered correctly) and passes
  `--features acpi=off` there. QEMU's `virt` machine describes the guest's
  devices with a device tree instead, so nothing is lost. x86_64 guests are
  unchanged and keep ACPI.

- **The serial console of an aarch64 guest is captured again.** Base images
  recorded a `console=ttyS0` kernel command line, and the aarch64 `virt`
  machine has no such device — its serial port is `ttyAMA0` — so `console.log`
  and `agent-vm console` stayed empty on that architecture. Newly built base
  images name both consoles (`console=ttyS0 console=ttyAMA0`), which each
  architecture resolves to the one it actually has. Images already in the cache
  keep the command line they were built with; rebuild with
  `agent-vm image build <distro> --force` to pick this up.

- `playwright install` in a guest no longer fails with `EACCES: permission
  denied, mkdir '/opt/ms-playwright/__dirlock'` for every account but root. The
  shared browser directory the image fills at build time was left owned by root
  and unwritable by anyone else, so the command Playwright's own "browser not
  found" error tells you to run — the one an agent needs after `mise` moves
  `playwright` to a release pinning a newer browser build — could not run as the
  guest user. It is now world-writable with the sticky bit, like `/tmp`: any
  account may add a browser build, none may remove another's. Existing base
  images keep the old permissions; rebuild with
  `agent-vm image build <distro> --force`, or run
  `sudo chmod 1777 /opt/ms-playwright /opt/ms-playwright/.links` in a guest.

- Installing an npm-backed tool with `mise` inside a guest works for the agent
  account again. `mise`'s npm backend takes a lock under `/tmp/fslock`, a
  directory owned by whichever account creates it first and not writable by any
  other, so root creating it — during the image build, or during the root half
  of `agent-vm update` — left the guest user's `mise use -g npm:wrangler` (and
  the unelevated half of `agent-vm update`) failing with "failed to acquire
  project lock: Permission denied" before it downloaded anything. Base images
  no longer ship the build's copy of that directory and now recreate it at
  every boot with `/tmp`'s own permissions, and `agent-vm update` gives root's
  `mise` steps a `TMPDIR` of their own so VMs created from an existing image
  can be updated without rebuilding. Rebuild a base image (`agent-vm image
  build <distro> --force`) to pick up the boot-time rule.

- `agent-vm image build <distro> --force` and `agent-vm image rm <distro>
  --force` work again. Both commands stopped reading their own flags at the
  distro name, so a `--force` written after it — the spelling this changelog and
  `docs/cli.md` have always used — exited 2 with "flag provided but not
  defined" instead of running. Flags on either side of the distro are now
  accepted, matching every other subcommand; a second distro argument is still
  refused.

- `agent-vm doctor` no longer skips the **host firewall forwarding** check on a
  host that has a default bridge configured but creates NAT VMs. The check
  keyed on `[network.bridge] interface` being set at all rather than on the
  network mode, so following the advice in `docs/host-setup.md` to configure a
  default bridge silently turned off the one check that catches a `ufw` host
  dropping guest traffic — the failure where a VM boots, answers SSH, resolves
  DNS, and hangs on every outbound connection. It now skips only in bridged
  mode, where guest traffic genuinely never reaches the host's forward hook.

- `ping` works for the guest's own user again. In a guest the command failed
  with `socket: Operation not permitted ... missing cap_net_raw+p capability`
  for anything but root, which looks exactly like a VM with no network even
  though its lease, route, and TCP traffic were all fine. The capability distro
  packaging puts on `/usr/bin/ping` is an extended attribute, and it does not
  survive the `podman export` tar that `virt-make-fs` turns into the disk;
  Ubuntu and Fedora then leave `net.ipv4.ping_group_range` at the kernel's
  empty default, so the fallback to an ICMP socket was closed too. Base images
  now ship a sysctl drop-in that opens that range. **Run
  `agent-vm image build <distro> --force` to pick this up** — existing cached
  images still produce guests with the old behavior.

- `agent-vm create --github-ssh-key` no longer fails with `cat:
  .ssh/id_ed25519.pub: No such file or directory` on a VM that has only just
  booted. The guest generates that key from a first-boot unit that runs after
  cloud-init's final stage, which is later than the point where SSH starts
  answering, so the read raced the guest. It is now retried for up to
  `--wait-for-ssh` and, if the key never appears, the error names the
  `agent-vm-user-setup.service` unit to look at.

- Arch guests boot again. The image ships no `/etc/machine-id`, which systemd
  reads as a first boot, and Arch enables `systemd-firstboot.service` — which,
  with a serial console attached, prompted for a timezone and waited forever.
  Boot stopped there, so cloud-init never ran and the guest never got a network;
  from the outside `agent-vm create --distro arch` simply timed out waiting for
  an address. The unit is now masked, and the build fails if the mask is
  missing. Every VM still gets its own machine ID, which systemd initializes
  from the SMBIOS UUID independently of that unit. Rebuild a cached arch image
  with `agent-vm image build --force arch`.

- Fedora guests are named after the VM again, instead of all being named
  `fedora`. cloud-init's Fedora distro class prefers the FQDN over the hostname,
  and since a disposable VM has no domain, it fell back to the system FQDN —
  systemd's compiled-in fallback, the literal string `fedora` — and applied that
  to every guest. The image now turns that preference off. Ubuntu and Arch were
  never affected.

- Fedora guests boot again. `container-selinux`, which arrived with the
  virtualization stack podman and libvirt pull in, installs
  `selinux-policy-targeted` and its `/etc/selinux/config` set to enforcing. The
  root filesystem is built from a flattened container export and carries no
  SELinux labels at all, so systemd tried to relabel an unlabeled filesystem on
  first boot, failed, and froze PID 1 about three seconds in — before
  networking. From the outside this looked like `agent-vm create --distro
  fedora` timing out waiting for an address, with nothing to say init had died.
  The image now sets SELinux to disabled and the build fails if it is not, so
  this cannot ship again unnoticed. Rebuild a cached fedora image with
  `agent-vm image build --force fedora`.

- A bridged VM's `vm.json` no longer claims it is on the NAT network. The record
  named the libvirt network `agent-vm-nat` alongside the bridge for every
  bridged VM, even though the guest was attached only to the host bridge — the
  field is there so a guest's network exposure is auditable after the fact, and
  it was describing an isolation the guest did not have. The two attachments are
  now mutually exclusive in the record: bridged VMs carry `network.bridge` and
  no `network.name`, NAT VMs carry `network.name` and no `network.bridge` (a
  configured `AGENT_VM_BRIDGE` was previously recorded on NAT VMs too). No
  rebuild is needed; existing records are corrected the next time a VM is
  created.

- The base image's one-shot account unit never ran. It was wanted by
  `multi-user.target` and ordered after `cloud-final.service`, which cloud-init
  itself orders *after* that target — an ordering cycle, which systemd breaks by
  deleting a job. The unit sat enabled and inactive for the life of every VM,
  with nothing in the journal to say so, so accounts the seed did not create
  never reached the `docker` group. It is now pulled in by `cloud-final.service`
  directly. Rebuild base images (`agent-vm image build <distro> --force`) to
  pick this up.

- A VM only reached the network's metadata service, and took four minutes to
  become reachable over SSH, on every boot after its first. Ubuntu's own
  cloud-init configuration lists `Ec2` and every other network datasource, and
  it was being read *after* this project's `NoCloud`-only pin — cloud-init reads
  `/etc/cloud/cloud.cfg.d` in sorted order and the file was named
  `90-agent-vm-datasource.cfg`, which sorts before Ubuntu's `90_dpkg.cfg`
  because `-` sorts before `_`. The pin had no effect there.

  Before ADR-0011, the first boot always looked correct, because the transient
  cloud-init seed was attached then and `NoCloud` matched immediately. Later
  boots had no seed, so
  cloud-init fell through to the network datasources and spent four minutes
  probing `169.254.169.254` before `sshd` started, which is longer than
  `agent-vm start` waits.

  The pin now sorts last, and a build checks that it really is the last word on
  the subject rather than trusting the file name — a distribution adding a
  later-sorting datasource list would otherwise take the guarantee away again,
  as silently as it was lost the first time.

  This affected Ubuntu, the default family. Existing base images still carry the
  old file name; rebuild them with `agent-vm image build <distro> --force` to
  pick up the fix.

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

[Unreleased]: https://github.com/snaphop/snaphop-agent-vm/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/snaphop/snaphop-agent-vm/releases/tag/v0.1.0
