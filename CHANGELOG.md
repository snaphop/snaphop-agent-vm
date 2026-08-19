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

### Changed

- Base images now install the `claude`, `opencode`, and `pi` CLIs with **mise**
  (`mise use -g claude opencode pi`) instead of a global `npm install -g`. The
  registry names resolve to each vendor's own native release, so the three no
  longer run on Node — and `npm` is no longer held to the 11 line, because
  nothing left in the image depends on an npm postinstall script. The commands
  work exactly as before, including over a non-interactive
  `ssh <vm> claude -p '…'`: each has a symlink in `/usr/local/bin` pointing at
  the `mise` binary, which resolves the version from the calling account's own
  configuration. What changes inside a guest: the agents are per account,
  installed into `/etc/skel` during the build the same way the JDK is, so an
  account can move one to another release with `mise use -g claude@<version>`
  without `sudo` — where a global npm prefix previously needed root. Node.js is
  still installed (`wrangler`, Playwright, and any JavaScript work in the guest
  need it), and `codex` and `agy` are unchanged, still from their vendors'
  installers. Existing base images are unaffected — rebuild an image
  (`agent-vm image build --force`) to pick this up.

- `wrangler` and `playwright` are now installed with **mise** as well
  (`mise use -g npm:wrangler npm:playwright`) rather than `npm install -g`.
  They are the only npm packages left in the image — npm is the only place
  either is published — and, like the agents, each has a `/usr/local/bin`
  symlink to the `mise` binary so `ssh <vm> wrangler deploy` keeps working
  without a login shell. Playwright's browsers still live in
  `/opt/ms-playwright`, shared by every account rather than copied per user.

- Base images now install the JDK and Maven with **mise** instead of SDKMAN.
  `java` and `mvn` work exactly as before — the newest Temurin JDK and Maven,
  installed during the build so a network-isolated guest needs no download, put
  on the path of a login shell by `/etc/profile.d/agent-vm-mise.sh`. What
  changes inside a guest: the `sdk` command is gone and `mise` replaces it, so
  switching versions is `mise use java@21` rather than `sdk install java 21`;
  the toolchain lives in `~/.local/share/mise` instead of `~/.sdkman`; and
  because mise's shims are ordinary executables rather than a shell function, a
  script that needs `mvn` non-interactively can put `~/.local/share/mise/shims`
  on `PATH` instead of sourcing anything. Existing base images are unaffected —
  rebuild an image (`agent-vm image build --force`) to pick this up.

### Fixed

- `agent-vm doctor` no longer skips the **host firewall forwarding** check on a
  host that has a default bridge configured but creates NAT VMs. The check
  keyed on `[network.bridge] interface` being set at all rather than on the
  network mode, so following the advice in `docs/host-setup.md` to configure a
  default bridge silently turned off the one check that catches a `ufw` host
  dropping guest traffic — the failure where a VM boots, answers SSH, resolves
  DNS, and hangs on every outbound connection. It now skips only in bridged
  mode, where guest traffic genuinely never reaches the host's forward hook.

### Added

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

- Base images now carry `wrangler`, Cloudflare's CLI, installed from npm on
  the Node runtime the coding agents already need. It ships with no
  credentials — `wrangler login` is an OAuth flow and an API credential is
  per-VM — and `WRANGLER_SEND_METRICS=false` is set in `/etc/environment`, so a
  guest reports no anonymous usage metrics unless its operator unsets it.
  **Run `agent-vm image build <distro> --force` to pick this up.**

- Base images now carry a Rust toolchain: `rustup` with the stable toolchain,
  so `rustc`, `cargo`, `rustfmt`, and `clippy` are in every VM. It is installed
  once into `/usr/local/rustup` and shared by every account rather than
  downloaded per user at first use, which a network-isolated guest could not do
  at all — so `rustup update` needs `sudo`, while `cargo install` still writes
  into the account's own `~/.cargo`. The entry points are in `/usr/local/bin`,
  so they work in a non-interactive `ssh <vm> cargo build` and not only in a
  login shell. **Run `agent-vm image build <distro> --force` to pick this
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
  credentials, which are per-VM and never come from a base image or a seed, so
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
  Every key file that was read is recorded in `vm.json` as a path; key material
  is never written there.

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
  to a plain shell. Detaching returns to the menu, and the menu is
  `agent-vm-menu` if you want it again later.

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

- Base images now carry a Go toolchain and `golangci-lint`, which is installed
  from its own installer on all three families — Ubuntu does not package it, and
  elsewhere the version differs per family.

- SDKMAN in a base image now comes with the newest Temurin JDK it offers and
  with Maven already installed, so `java` and `mvn` work in a guest without
  downloading anything. As before they are on the path of a login shell only;
  use `ssh <vm> bash -lc '…'` from a script.

- Every interactive account in a guest now gets an `ed25519` SSH key pair at
  `~/.ssh/id_ed25519`, generated on first boot if that path does not already
  exist. It is generated inside the guest and never leaves it — no private key
  goes into a base image or a cloud-init seed — and an existing key, such as one
  an operator supplied through `--cloud-init`, is left alone.

- The generated cloud-init user-data now creates the login user in the
  `libvirt` and `kvm` groups as well as `docker`, so `virsh` works without
  `sudo` in the very first SSH session. This is a change to the generated
  user-data, which is a public contract: the golden files record it. A base
  image built before this change has no such groups, and still boots — the
  user-data declares them, and cloud-init creates groups before users.

  The base image's one-shot account unit, which is the backstop for accounts
  the seed does not create, was renamed from `agent-vm-docker-group.service` to
  `agent-vm-user-setup.service` and now does both jobs above.

### Changed

- The Go toolchain in a base image now comes from the current go.dev release
  instead of the distribution's package. Every family packaged a different and
  often years-old Go, and a guest whose Go is older than the `go` directive of
  the repository an agent was handed cannot build it at all; all three families
  now carry the same release. `go` and `gofmt` are in `/usr/local/bin` as
  before, and `~/go/bin` and `~/.cargo/bin` are added to the path of a login
  shell. Existing cached images keep their packaged Go until rebuilt with
  `agent-vm image build <distro> --force`.

- `codex` in a base image now comes from OpenAI's own installer
  (`https://chatgpt.com/codex/install.sh`) instead of the `@openai/codex` npm
  package. `codex remote-control` only runs against the standalone package that
  installer produces — it starts its app-server from a fixed path under the
  account's `CODEX_HOME` and refuses to run when that directory is missing,
  which is what an npm install leaves behind. The package is installed once
  into `/usr/local/lib/codex` and shared by every account (it is around
  300 MiB), with the command in `/usr/local/bin` as before, and each account
  gets a symlink to it under `~/.codex` at first boot so credentials and
  configuration stay per-account. **Run `agent-vm image build <distro> --force`
  to pick this up** — existing cached images still have the npm build.

- Base images are no longer built purely from their distribution's own
  repositories. The coding agents are not packaged by any distro, so a build now
  reaches npm and one vendor install script, and on Ubuntu it also adds the
  NodeSource repository and GPG key — Ubuntu 24.04 ships Node.js 18 and the
  agents need 22.19 or newer. Fedora and Arch use their own Node.js. A build
  fails outright if the Node.js it ends up with is older than 22.19, rather than
  producing an image whose agents silently cannot start, and holds `npm` to the
  11 line because npm 12 skips the postinstall scripts two of the agents need. Building a base image
  already required network access to a registry; it now also requires reaching
  these sources.

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

### Removed

- The template setup checklist (`docs/project-setup.md`), which no longer applies
  now that the repository is a real project; it became `docs/host-setup.md`.

### Fixed

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

- Base images now also carry `gh` and `tea` for GitHub and Gitea, Playwright
  with a headless `chromium`, and SDKMAN for installing JDKs in the guest.

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

  SDKMAN is installed per account rather than shared, because installing a JDK
  writes into its directory. No JDK is preinstalled: run `sdk install java` in
  the guest. `sdk` is a shell function, so it exists in an interactive login
  shell only.

  A build now runs each of these once — including launching headless Chromium —
  and fails if any of them cannot work. Base images are correspondingly larger.

  Existing cached base images are unaffected and still boot. They will not have
  these tools until rebuilt with `agent-vm image build --force`.

- Base images now ship five coding agents — `claude`, `codex`, `opencode`, `pi`,
  and `agy` — so a new VM is usable by an agent the moment it becomes reachable,
  instead of starting with an install. Four come from npm and bring a Node.js 24
  runtime with them; `agy` is installed from its vendor's script into
  `/usr/local/bin`, so every account on the VM finds it. Versions are not pinned:
  a guest gets whatever was current when its base image was built, and
  `agent-vm image build --force` is how you get newer ones.

  Each agent is configured in its **most permissive mode**, so it works
  unattended rather than blocking on an approval prompt nobody is there to
  answer. This is safe only because the VM is itself the sandbox — disposable and
  network-isolated by default, with nothing the host cares about reachable from
  inside it. `agy` is the exception to the mechanism: it has no configuration
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
  the agents until rebuilt with `agent-vm image build --force`.

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

- A VM only reached the network's metadata service, and took four minutes to
  become reachable over SSH, on every boot after its first. Ubuntu's own
  cloud-init configuration lists `Ec2` and every other network datasource, and
  it was being read *after* this project's `NoCloud`-only pin — cloud-init reads
  `/etc/cloud/cloud.cfg.d` in sorted order and the file was named
  `90-agent-vm-datasource.cfg`, which sorts before Ubuntu's `90_dpkg.cfg`
  because `-` sorts before `_`. The pin had no effect there.

  The first boot always looked correct, because the cloud-init seed is attached
  then and `NoCloud` matches immediately. Later boots have no seed, so
  cloud-init fell through to the network datasources and spent four minutes
  probing `169.254.169.254` before `sshd` started, which is longer than
  `agent-vm start` waits.

  The pin now sorts last, and a build checks that it really is the last word on
  the subject rather than trusting the file name — a distribution adding a
  later-sorting datasource list would otherwise take the guarantee away again,
  as silently as it was lost the first time.

  This affected Ubuntu, the default family. Existing base images still carry the
  old file name; rebuild them with `agent-vm image build --force` to pick up the
  fix.

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

## [0.1.0] - 2026-08-17

### Added

- Initial project scaffold.
