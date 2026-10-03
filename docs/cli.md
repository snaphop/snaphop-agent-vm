# CLI Contract

> This is the canonical reference for SnapHop Agent VM, the `agent-vm`
> command line. It is a public contract: subcommands, flags, defaults, JSON
> output fields, and exit codes are consumed by humans, scripts, and agent
> supervisors. Changing any of them is a contract change under `AGENTS.md` §8.
>
> The implementation must match this document. If they disagree, one of the two
> is a bug — fix them together in the same change.

## Synopsis

```text
agent-vm [global flags] <command> [subcommand] [arguments] [flags]
```

## Global Flags

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | `~/.config/agent-vm/config.toml` | Configuration file. |
| `--state-dir <path>` | `~/.local/share/agent-vm` | Root of all VM and image state. |
| `--libvirt-uri <uri>` | `qemu:///system` | libvirt connection URI. `qemu+ssh://[user@]host[:port]/system` drives a hypervisor on another machine — see [Remote Hypervisors](#remote-hypervisors). |
| `--output <text\|json>` | `text` | Output format. `json` is machine-readable and stable. |
| `--verbose` | off | Debug-level logging to stderr. |
| `--quiet` | off | Suppress progress output; errors and warnings still go to stderr. |
| `--yes` | off | Skip interactive confirmation for destructive operations. |
| `--dry-run` | off | Print planned invocations without mutations; values that require execution use placeholders. Input validation and read-only checks can still fail. |
| `--help` | — | Print usage and exit `0`. After a command — `agent-vm create --help` — print that command's invocation and its own flags instead. |
| `--version` | — | Print the `agent-vm` version, plus the detected version of every required tool: `virsh` (which reports libvirt's version), `virt-install`, `qemu-img`, `podman`, the libguestfs tools, `ip`, and `ssh`. |

Human-readable progress and logs go to **stderr**. Command results go to
**stdout**, so `--output json` can be piped safely.

Every flag is spelled with two dashes, and that is the spelling `agent-vm` uses
when it rejects one: an unknown or malformed flag is reported once, on stderr,
naming the flag as `--flag` and followed by that command's own usage listing —
its documented invocation and every flag it takes, with defaults — so the flags
that would have worked are on screen already. A missing or unexpected argument
is answered the same way. A value the command cannot use — an invalid VM name,
an unparsable size — is reported on its own and points at `agent-vm --help`.

## Remote Hypervisors

`--libvirt-uri qemu+ssh://[user@]host[:port]/system` drives libvirt on another
machine. That URI selects more than a connection: **every host tool runs on that
machine, and the state directory lives there**
([ADR-0010](./decisions/0010-drive-a-remote-hypervisor-by-running-host-tools-over-ssh.md)).
`podman` and libguestfs build base images there, `qemu-img` creates overlays
there, and `virt-install` and `virsh` run there against that host's own
`qemu:///system`. This machine becomes a thin driver.

Two things stay here, because they are the operator's rather than the
hypervisor's: `gh`, which uses your GitHub login, and the `ssh` into a guest,
which uses your keys and your terminal. A guest sits on a network that exists
only on the hypervisor, so `agent-vm ssh` and `agent-vm update` reach it with
`ssh -J <hypervisor>`.

| Aspect | With a remote URI |
|---|---|
| Base images, overlays, kernels, `vm.json`, locks | On the hypervisor, under its state directory. |
| `--state-dir` | A path **on the hypervisor**. Unset, it defaults to `~/.local/share/agent-vm` in the home directory of the account the URI names, which `agent-vm` asks that host for. |
| `--dry-run` | Prints the `ssh` invocations that would run, transport and all. |
| `vm.json`'s `libvirtUri` | The URI you gave, transport included. `virsh` is invoked with `qemu:///system`, as that machine reads it. |
| `agent-vm ssh`, `agent-vm update` | `ssh -J <hypervisor> <guest>`. |
| `agent-vm console` | `ssh -t <hypervisor> virsh --connect qemu:///system console <name>`. |
| Locks | `flock` on the hypervisor, so two operators driving the same host contend correctly. |

Requirements:

- **`ssh <destination> true` must succeed without a prompt.** `agent-vm` runs ssh
  in `BatchMode`, because it captures ssh's output and a prompt would be
  invisible. Use an SSH agent, a default identity, or name a key with
  `?keyfile=<path>` in the URI. This is the same condition libvirt's own
  `qemu+ssh` transport needs.
- The hypervisor needs every tool `doctor` lists **except `ssh` and `gh`**, which
  are needed here instead. It also needs `flock` (util-linux), `find`
  (findutils), and coreutils — all of which a Linux host running libvirt already
  has.
- Connections are shared for the run (`ControlMaster`), so a create or an image
  build does not pay for a key exchange per invocation.

URI parameters honored: `keyfile=<path>` (offered to ssh as `-i`) and
`no_verify=1` (skips host key checking, matching what you already told libvirt).
A password in the URI is refused: it cannot be handed to ssh, and ignoring it
would leave you wondering why you are prompted.

Local and remote `/session` URIs are accepted with NAT mode only. They still
require the host to support a managed libvirt NAT network; accepting the URI
does not provide a user-mode networking fallback. See
[host setup](./host-setup.md) for connection and network prerequisites.

Only `qemu+ssh://` is accepted for a remote hypervisor. `qemu+tls://`,
`qemu+tcp://`, and the `libssh` transports reach libvirt but give `agent-vm` no
shell there, so they are refused as usage errors (exit `2`) rather than failing
partway through a `create`.

A remote URI is a real grant: it lets `agent-vm` run host tools as that account
on that machine, which is more than a libvirt connection alone. That is why it
has to be spelled out rather than inferred.

## Configuration Precedence

Later sources win:

1. Built-in defaults
2. Configuration file
3. Environment variables (`AGENT_VM_*`)
4. Command-line flags

### Configuration File

```toml
# ~/.config/agent-vm/config.toml
state_dir   = "~/.local/share/agent-vm"
libvirt_uri = "qemu:///system"   # or "qemu+ssh://kvm@hypervisor.lan/system"

# Only needed on a host whose own kernel cannot boot a libguestfs appliance —
# `doctor` says so if yours cannot. See docs/host-setup.md.
# appliance_kernel = "~/.local/share/agent-vm/appliance-kernel/6.12.4-arch1-1"

[defaults]
distro  = "ubuntu"
vcpus   = 2
memory  = "4G"
# max_memory = "16G"   # optional: ceiling the guest can be grown to at runtime
disk    = "50G"
network = "nat"

[network.nat]
name = "agent-vm-nat"   # an existing network by this name must forward by NAT

[network.bridge]
interface = "br0"

[guest]
user     = "agent"
ssh_keys = ["~/.ssh/id_ed25519.pub"]
```

A key the file does not support — including a misspelled key or table name —
is a usage error (exit `2`) naming the key, never silently ignored. A key that
is present is used as written: `vcpus = 0` is rejected, not treated as unset.

Local default paths honor `XDG_CONFIG_HOME` and `XDG_DATA_HOME` when they are
set to absolute paths; a relative value is ignored, as the XDG Base Directory
specification requires, and the default falls back to `$HOME`. When a default
path is needed and neither an absolute XDG directory nor an absolute `HOME` is
available, `agent-vm` exits `2` and asks for an explicit path (`--state-dir`,
`--config`, or their environment variables) rather than resolving against the
current directory.
For a remote hypervisor, an unset state directory uses that account's
`~/.local/share/agent-vm`. Explicit paths beginning with `~` expand against
the **client** home directory, so use absolute remote paths.

### Environment Variables

| Variable | Equivalent to |
|---|---|
| `AGENT_VM_STATE_DIR` | `--state-dir` |
| `AGENT_VM_LIBVIRT_URI` | `--libvirt-uri` |
| `AGENT_VM_CONFIG` | `--config` |
| `AGENT_VM_APPLIANCE_KERNEL` | `appliance_kernel` (no flag) |
| `AGENT_VM_DISTRO` | `create --distro` |
| `AGENT_VM_VCPUS` | `create --vcpus` |
| `AGENT_VM_MEMORY` | `create --memory` |
| `AGENT_VM_MAX_MEMORY` | `create --max-memory` |
| `AGENT_VM_DISK` | `create --disk` |
| `AGENT_VM_NETWORK` | `create --network` |
| `AGENT_VM_BRIDGE` | `create --bridge` |
| `AGENT_VM_SSH_KEY` | `create --ssh-key` |

## Commands

### `agent-vm doctor`

Checks host readiness and reports each check as `pass`, `warn`, `fail`, or
`skip`, with a remedy where needed. Exits `0` when no check reports `fail`;
warnings and skipped checks do not establish that every prerequisite is met.

Checks: `/dev/kvm` present and writable; libvirt connection succeeds; user is in
the `kvm` group, and in the `libvirt` group when the URI is not a `/session` one;
state directory writable with sufficient free space; state directory reachable by
the account the hypervisor runs as; libguestfs can boot its appliance;
the configured NAT network is definable; the
host firewall does not drop the guest's forwarded traffic, nor its DHCP and DNS
requests to the host; and, when a bridge is
configured, that the bridge exists and is up. Bridged networking on a `/session`
connection is reported as unsupported rather than attempted.

The bridge check also warns about a bridge running the spanning tree protocol
with a non-zero forward delay. Such a bridge holds each guest's tap port in
listening and learning before it forwards anything, so the guest cannot finish
DHCP for twice the delay — about 30 seconds with the default — and every
`create` on that bridge waits that much longer for SSH. It is a warning rather
than a failure because the VM does come up, and the remedy names the exact
command (`ip link set <bridge> type bridge stp_state 0`); see
[`docs/host-setup.md`](./host-setup.md#turn-off-the-spanning-tree-forward-delay).
`create` prints the same warning and carries on.

With a [remote URI](#remote-hypervisors), the checks describe the hypervisor
rather than this machine: `/dev/kvm` and group membership are that host's, asked
over the transport, and a `hypervisor host <destination>` check runs first. If
that check fails nothing else is reported, because everything below it would fail
for the same reason. Some checks cannot be answered from here at all — the two host
firewall checks, and whether the account QEMU runs as can traverse to the state
directory — because they need that machine's configuration files and passwd
database; they report `skip` and point at running `agent-vm doctor` on the
hypervisor itself.

The **libguestfs appliance** check exists because libguestfs does all of its
work — building the base image, writing the cloud-init seed — inside a small VM
it boots for the purpose, and `supermin` builds that VM around the *host's own
kernel*. A general-purpose distribution kernel boots it fine. A kernel built for
one machine rather than for machines in general may not: an Apple Silicon
(Asahi) kernel, for instance, has neither the PL011 serial port nor the generic
PCIe host bridge QEMU's `virt` board provides, so the appliance comes up with no
console and no disks and libguestfs reports only that it "closed the connection
unexpectedly". The check reads the host kernel's configuration (`/boot/config-$(uname -r)`,
else `/proc/config.gz`) and fails when `CONFIG_SERIAL_AMBA_PL011` or
`CONFIG_PCI_HOST_GENERIC` is missing, naming `appliance_kernel` as the remedy.
It looks only at aarch64 hosts, where those options exist and where such kernels
are actually in use, and reports `skip` rather than guessing when the kernel
publishes no configuration. When `appliance_kernel` *is* set, the host kernel
stops mattering and the check confirms instead that the configured directory
holds the kernel and module tree supermin will be pointed at. See
[`docs/host-setup.md`](./host-setup.md#8-hosts-whose-kernel-cannot-boot-the-libguestfs-appliance).

`gh` is checked too, but never fails the report: it is needed solely by
`--github-ssh-key`, so a host without it is still a ready host. It reports
`skip` when `gh` is not installed, and `warn` when it is older than the minimum
or its version cannot be read, since `--github-ssh-key` would then fail.

Each check reports `pass`, `warn`, `fail`, or `skip`, and only a `fail` makes
`doctor` exit non-zero. Group membership is a warning, because a host may grant
`/dev/kvm` and libvirt access another way and the checks that test those directly
are the ones that matter. Free space below 10 GiB is a warning: base-image
size and build workspace requirements vary substantially, especially for full
tooling images, and thin overlays grow as guests write. A NAT network that is
not defined yet is a pass — `create` defines it on demand, on
`192.168.171.0/24` with libvirt allocating the bridge device (see
[`docs/host-setup.md`](./host-setup.md#5-nat-networking-default)). `doctor` only inspects;
it never changes host state.

The **host firewall forwarding** check exists because libvirt accepting the
guest's packets is not the last word on them. Every nftables base chain
registered on the forward hook runs, so a host firewall with a drop policy there
silently discards traffic libvirt already accepted, and `ufw` ships exactly that
configuration (`DEFAULT_FORWARD_POLICY="DROP"`). The resulting VM looks healthy
in every way an operator normally checks — it boots, accepts SSH, and resolves
DNS, because the resolver is dnsmasq on the host bridge and that traffic is
delivered locally rather than forwarded — while every outbound connection hangs
rather than failing, because the packets are dropped rather than rejected.

Forwarding is only half of what NAT mode needs, and the **host firewall guest
services** check is the other half. The guest also talks *to* the host — it asks
the host's dnsmasq for a DHCP lease and for every DNS answer — and that traffic
is inbound rather than forwarded, so `ufw`'s separate `deny (incoming)` default
governs it. `ufw` accepts DHCP *replies* (`sport 67 → dport 68`, the host acting
as a DHCP client) and drops anything arriving on port 67 or 53, so unless the
operator added a rule for the bridge, the guest's `DHCPDISCOVER` never reaches
dnsmasq.

That failure is total rather than partial, which is why it is reported
separately: the guest boots, waits in `systemd-networkd-wait-online` forever,
and never gets an address, so `create` fails at `Waiting for the guest to boot`
on a host `doctor` had called ready. The check reports the missing services by
name — DHCP (67/udp), DNS (53), or both — and asks libvirt for the bridge
(`virsh net-dumpxml`), so when the network already exists the remedy is a rule
that can be run as printed; before the first `create` there is no bridge yet, so
it prints the lookup instead of guessing `virbr0`. It is skipped in bridged
mode, where the guest gets its lease and resolver from the LAN.

Both checks read `ufw`'s configuration only; neither ever runs `ufw` and
neither ever changes a rule. They report `pass` when `ufw` is absent, disabled, permissive by
default on the hook in question, or carries rules covering it (naming the
interfaces those rules cover, so you can confirm the right bridge is among
them). Only the IPv4 rules are read: the IPv6 twins `ufw` writes alongside
them never match on an IPv4-only NAT network. A forwarding rule counts only if
it accepts traffic arriving *from* the NAT network's bridge (`route allow in on
<bridge>`) or names no interface at all; a rule for another interface, such as
a VPN, or one limited to traffic going out toward the bridge does not let the
guest out. Before the first `create` the bridge does not exist yet, so a rule
on any input interface is accepted and the detail says it could not be matched
to the bridge. They report
`warn` — never `fail` — when `ufw` is enabled and dropping, because the live
ruleset cannot be read without root and a false failure would exit non-zero on a
working host. Both are skipped for a
[remote hypervisor](#remote-hypervisors), whose firewall is the one that matters
and is not this machine's, and both ask about
the network *mode*, not whether a bridge is configured, so a host that sets a
default `[network.bridge] interface` and still creates NAT VMs is checked. Only `ufw` is understood, so a pass means
"no `ufw` problem" rather than "no firewall problem";
[`docs/host-setup.md`](./host-setup.md#host-firewalls-and-the-virbrn-bridge)
covers the rule to add, why it can stop matching when libvirt allocates a
different `virbrN`, and how to keep the guest off your LAN while allowing it out.

The **state directory access** check is separate from the writability check
because they ask about different users. Under `qemu:///system` the QEMU process
runs as libvirt's own account, which must be able to search every directory from
`/` down to a VM's disk — a state directory you can write yourself may still be
unreachable to it, and the resulting `create` fails only once `virt-install` tries
to open the overlay. The check reads permission bits and any POSIX ACL, so a
`setfacl` grant is recognised, and it names the shallowest directory that blocks
the path along with the `setfacl` command that fixes it. It is skipped rather than
guessed when the hypervisor's account cannot be identified, and it does not apply
to a `/session` connection, where QEMU runs as the invoking user. It is also
skipped for a [remote hypervisor](#remote-hypervisors), whose accounts and
directory permissions cannot be judged from here. Because it only
inspects the filesystem it still runs under `--dry-run`.

It checks helper versions against the floors below. `virsh --version` and
`qemu-img --version` are the version probes for libvirt and QEMU; the
connection check runs `virsh version` but does not separately enforce the
connected daemon or emulator version. Keep those components above the stated
floors too. An unreadable helper version is reported as a warning.

| Tool | Minimum |
|---|---|
| libvirt (`libvirtd`/`virtqemud`, `virsh`) | 9.0 |
| QEMU (`qemu-system-*`, `qemu-img`) | 8.0 |
| `virt-install` | 4.0 |
| libguestfs (`virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`) | 1.50 |
| `podman` | 4.0 |
| `ip` (iproute2), `ssh` | any |

```bash
agent-vm doctor
agent-vm doctor --output json
```

### `agent-vm image build <distro>[-slim|-nix][:<tag>]`

Builds (or rebuilds) the cached base image for a distro. `<distro>` is a
supported family — `ubuntu`, `fedora`, or `arch` — or that family's
`<family>-slim` or `<family>-nix` variant (see below). Each step is an existing
tool: `podman pull` the source image, `podman build` the embedded per-distro
`Containerfile` to add the guest packages a VM needs but a container does not
(kernel, `systemd`, `cloud-init`, `openssh-server`, `sudo`, `qemu-guest-agent`,
and `chrony` — see “Guest clock” below)
along with the tooling an agent expects to already be there (`ping`, `curl`,
`wget`, `git`, a C toolchain, Python, Docker, and the coding agents themselves
— see “Guest tooling” below),
`podman export` to flatten it, `virt-make-fs` to write `base.qcow2`,
`virt-ls`/`virt-copy-out` to extract `vmlinuz`/`initrd`, and `virt-sysprep` to
clear the machine ID and SSH host keys. Finishes by recording `manifest.json`,
including the tool versions used.

Use `--dry-run` to print the whole pipeline without running it.

A build is several minutes of other programs working silently, so it reports
where it is. On a terminal that is one line, redrawn in place, showing a bar,
the step number, the step, and the elapsed time:

```
[████████░░░░░░░░░░░░] 4/10 exporting the root filesystem 2m18s
```

Anywhere the output is captured rather than displayed — a pipe, a log file, a
CI job, or a `--verbose` run whose logs share the stream — each step is one
plain `[4/10] exporting the root filesystem` line instead, and no terminal
control characters are written. Like all progress, it goes to stderr and is
silenced by `--quiet`. A cached image reports nothing, because nothing is
built. `create` builds a missing image the same way and reports it the same way.

| Flag | Default | Meaning |
|---|---|---|
| `--from <ref>` | distro's default source tag | Override the source OCI reference. |
| `--force` | off | Rebuild even if this image name and tag are already cached. Without it, the cache is returned without checking the source again. |
| `--platform <os/arch>` | platform of the machine running `agent-vm` | Image platform to pull. Set explicitly when driving a hypervisor with a different architecture; prebuild the image before `create`, which has no platform flag. |

Building is the only operation that requires network access to a registry. It is
safe to run concurrently for different distros, and a second build of the same
one waits for the first to finish rather than racing it.

Before `image build --force`, destroy VMs backed by that image name and tag.
A rebuild replaces the backing disk and host-side kernel at the same paths, and
an existing overlay on a different base disk is corrupt. So `image build
--force` refuses an image that a recorded VM still uses, naming the VMs, and
refuses while any `create` is in progress (exit `5`). Destroy the dependent VMs,
rebuild, and create replacement VMs after the rebuild. `image rm --force`
followed by `image build` is the deliberate way to replace an image regardless.

#### Slim images

Every family also has a slim variant, named by appending `-slim` to the family:
`agent-vm image build ubuntu-slim`, `agent-vm create work --distro
fedora-slim:42`. It boots identically — same kernel command line, same
cloud-init contract, same SSH and clock guarantees — and carries the same
common Linux tooling described under “Guest tooling” below, but none of the
agent tooling: no mise and no language toolchains, no coding agents, no
Chromium or Playwright, no Docker, and no nested virtualization stack. Pick it
for a VM that only has to run a build, a shell, or a test suite; it is a much
smaller image and a much shorter build.

A slim image is a separate base image rather than a mode of the full one. It
has its own cache directory (`images/ubuntu-slim/24.04/`), its own manifest,
and its own name in `image list`, `image inspect`, `image rm`, and `vm.json` —
so `ubuntu` and `ubuntu-slim` can both be cached at once, are built and removed
independently, and neither one's rebuild disturbs VMs backed by the other.
`agent-vm update` works on a slim guest exactly as on a full one: the distro
packages are updated, and the steps for tooling a slim guest does not carry are
skipped.

#### Nix images

Every family also has a nix variant, named by appending `-nix`: `agent-vm image
build ubuntu-nix`, `agent-vm create work --distro arch-nix`. It boots
identically to the full image — same kernel command line, same cloud-init
contract, same SSH and clock guarantees — and is built from the same distro
packages for everything that makes it boot. What differs is where the guest
*tooling* comes from: one shared nix expression,
`templates/distro/agent-tools.nix`, instead of the family's package manager and
mise (ADR-0012).

The point is that the expression is written once and means the same thing on
all three families, where the full recipes state the same tool set in `apt`,
`dnf` and `pacman` separately. Pick a nix image when you want to manage the
tool set in one file and optionally pin its nixpkgs-provided versions across
distros.

A nix guest carries the common Linux tooling, the language toolchains (Go,
Rust, Node.js, Python, the JDK and Maven, `golangci-lint`), `gh`, `tea`,
`wrangler`, Chromium and Playwright's browsers, and `nix` itself. Rust is the
one toolchain whose interface differs from a full image: `rustc`, `cargo`,
`rustfmt` and `clippy` come from nixpkgs and there is no `rustup`, so a guest
that wants a second toolchain uses `nix` rather than `rustup toolchain
install`. `agent-vm update` skips its rustup step on these guests accordingly. It keeps
Docker and the nested virtualization stack, which still come from the distro:
both are system daemons with distro-owned units, and a nix profile can supply
the binaries but not a running service.

The same coding agents are present, and the per-account herdr servers start at
boot exactly as they do on a full image. `claude` and `opencode` come from
nixpkgs, `codex` from OpenAI's installer, and `pi`, `herdr`, `agy` and `grok`
from mise — which is what a nix image still installs outside the nix file,
because nixpkgs packages none of them. `agy` is mise's registry name for the
Antigravity CLI; `grok` is `npm:@xai-official/grok` on the Node runtime the
nix profile already provides. mise installs no toolchain and no runtime, so
there is never a question of which copy of Go or Node a guest is running.
The nixpkgs pin does not cover these tools, Codex, the Nix installer, or
the packages installed by the distro package manager.

The shared Nix expression does not provide the `cf` or `playwright` CLI,
or Python `pip`. They include the Playwright browser artifacts in a read-only
Nix store, exposed through `/opt/ms-playwright`, and the `chromium` wrapper.
The full-image instructions for updating browsers in that directory do not
apply to this variant.

Nix is installed multi-user, so any account in the guest can `nix profile
install` for itself. What the image shipped lives in one profile every account
shares, `/nix/var/nix/profiles/default`, which is on the default `PATH`.

`agent-vm update` updates a nix guest's distro packages, mise itself, its
mise-managed tools (`pi`, `herdr`, `agy`, and `grok`), and Codex. It skips
rustup because the image does not install it. The tooling the nix profile
holds is fixed by the expression the image was built from — to change it, edit
`agent-tools.nix`, rebuild and install the `agent-vm` binary (the expression
is embedded), then rebuild the base image with `agent-vm image build
<distro>-nix --force`. Destroy dependent VMs before rebuilding the base, and
create replacements afterwards.

The nixpkgs the expression fetches is what decides which versions a guest
gets. It ships following a release branch, which moves;
`scripts/pin-nixpkgs.sh` resolves it to an exact revision and hash, after
which the nixpkgs-provided packages use the same versions for the same
architecture. This does not pin the other installers or distro packages.

A nix image is a separate base image, like a slim one: its own cache directory
(`images/ubuntu-nix/24.04/`), its own manifest, and its own name in `image
list`, `image inspect`, `image rm`, and `vm.json`. All three variants of a
family can be cached at once and are built and removed independently. It is the
largest of the three — a nix store carrying four toolchains and a browser is
bigger than the equivalent distro packages, because closures are complete.

The build prepares artifacts in a temporary directory beside the image
(`images/<image>/.build-<tag>-<pid>/`) and installs them on success. During a
rebuild the previous image is moved aside to `images/<image>/.<tag>.previous/`
before the new directory is installed, then removed; failed installation
attempts restore it. Neither name can be a tag, so neither is ever mistaken for
a cached image. If a build is killed outright (SIGKILL, out of memory, a host
reboot), the next `image build`, `image rm`, or `create` that uses the same
image cleans up after it: it removes the dead build's temporary directory, and
puts the previous image back if the rebuild was killed between moving it aside
and installing the new one.
The source image is pulled by its requested reference, then pinned to the digest that was actually
fetched; everything after the pull is built on the digest, and the digest is what
`manifest.json` records. The cache is keyed by image name and tag, not digest.
Packages and agent tools installed by the recipe can change independently, so
the source digest does not guarantee a byte-for-byte reproducible rebuild.

Every base image makes the same promises to the VMs built on it, and those
promises are the guest contract: `sshd`, `systemd-networkd`, and the cloud-init
units start by themselves at first boot; cloud-init reads the NoCloud seed and
no other datasource, so a guest never contacts a metadata service on the
network — the pin is written to `99-agent-vm-datasource.cfg` so that it sorts
after any `datasource_list` the distribution ships, and a build fails if some
other file in `/etc/cloud/cloud.cfg.d` would be read after it; and the image
carries no identity — `virt-sysprep` empties the machine
ID and removes SSH host keys, so no two VMs share either. Changing any of these
changes what every script that SSHes into these VMs can assume.

#### Guest tooling

The table and installation details below describe **full images**. Slim
images keep the common Linux packages but omit the agent and service tooling;
Nix images use the different sources and interfaces described above. Slim
images include `tmux` but do not install the custom session menu, tmux
configuration, or per-account setup service.

Beyond the packages that make a container image boot as a VM, a full image
carries the tools an agent working inside the guest expects to find already
installed. They live in the base image rather than in per-VM cloud-init
packages so `create` stays fast: the download is paid once per cached image
instead of on every first boot, and a VM works the same way offline.

| Group | What is installed |
|---|---|
| Networking and diagnostics | `ping`, `traceroute`, `dig`/`nslookup`, `netcat`, `ip`, `ss` |
| Fetching and transferring | `curl`, `wget`, `rsync`, `ssh`, `ca-certificates` |
| Development | `git`, a C/C++ toolchain (`gcc`, `make`, `pkg-config`), Python 3 with `pip` |
| Shell workflow | `jq`, `zip`/`unzip`, `xz`, `tar`, `less`, `vim`, `nano`, `tmux` (with a session menu at login), `htop`, `tree`, `file`, `man` |
| Containers | Docker (`docker`, `docker compose`, `docker buildx`), started at boot, able to build for `linux/arm64` as well as the host's own architecture |
| Coding agents | `claude`, `codex`, `opencode`, `pi`, `agy`, `grok`; `claude`, `opencode`, `pi`, `agy` and `grok` are managed by `mise` |
| Terminal workspace | `herdr`, managed by `mise`, with a server started at boot for every account |
| Forge CLIs | `gh` (GitHub), `tea` (Gitea) |
| Cloud CLIs | `wrangler` and `cf` (both Cloudflare), managed by `mise` |
| Browser automation | `playwright`, managed by `mise`, with a headless `chromium` |
| JVM toolchain | `mise` with the latest Temurin JDK and Maven (`java`, `mvn`) |
| Go toolchain | `mise` with `go`, `gofmt`, and `golangci-lint` |
| Rust toolchain | `rustup` with the stable toolchain: `rustc`, `cargo`, `rustfmt`, `clippy` |
| Virtualization | `qemu-kvm`, `libvirt` (started at boot), `virsh`, `virt-install`, `guestfs-tools`, `dnsmasq`, `podman` |

Package names differ per family — Ubuntu takes `docker.io`, Fedora takes
`moby-engine`, Arch takes `docker` — but the commands above are present on all
three. The common Linux packages come from the distro's own repository. Coding
agents, Herdr, tea, mise, Node.js, the JVM and Go toolchains, golangci-lint,
Rust, and the Cloudflare and Playwright tools use the upstream installation
paths described below.

Docker can build for a foreign architecture out of the box: `docker build
--platform linux/arm64 .` works on an x86_64 host, and `--platform
linux/amd64` works on an aarch64 one. The base image installs the distro's
`qemu-user-static` packages, whose `binfmt_misc` rules systemd registers at
every boot with the fix-binary flag, so the emulator is reachable from inside
a build container. Running `docker run --privileged --rm tonistiigi/binfmt
--install arm64` in the guest is therefore unnecessary; it needs a registry
round trip and its registration lasts only until the VM reboots. Multi-platform
manifests in a single build (`docker buildx build --platform
linux/amd64,linux/arm64`) still need a `docker-container` builder, which
`docker buildx create --use --bootstrap` sets up by pulling BuildKit.

#### Guest clock

Every base image ships and enables `chrony`. It is not a convenience. On
aarch64 the guest has no real-time clock it can read — QEMU's `virt` machine provides a PL031, but the
kernel flavours these images ship do not carry the driver, so `/dev/rtc0` never
appears — and systemd falls back to its own build date, starting the guest weeks
behind its host. What that breaks does not look like a clock problem: package
managers reject repository metadata as "not valid yet", TLS handshakes fail
against certificates that have not started yet, and build tools record
timestamps from the wrong month.

`chrony-wait` is enabled alongside it, so `time-sync.target` is reached only
once the clock is actually correct, and `sshd` is ordered behind that target.
That matters for more than tidiness: `create` calls a VM ready when SSH answers,
so ordering `sshd` there makes the readiness wait mean "the clock is right" too.
Without it a VM is handed over about five seconds into its boot and chrony steps
the clock at about seven, leaving an agent logged in and working against a clock
weeks out.

The ordering is `After=`, never `Requires=`, and `chrony-wait`'s start timeout is
shortened to 30 seconds. A guest whose network has no route to an NTP server
therefore becomes reachable half a minute later than it otherwise would, with
the same wrong clock it would have had anyway — rather than being held for
`chrony-wait`'s stock three minutes, which is longer than `create` waits at all.
A guest that can never reach an NTP server never synchronizes.

#### Coding agents

Every full base image carries six coding agents, so a VM is usable by an agent the
moment it is reachable: `claude`, `codex`, `opencode`, `pi`, `agy`, and `grok`.
Their versions are not pinned — they are whatever was current when the image
was built. `agent-vm update` moves the mise-managed ones without a rebuild.

`claude`, `opencode`, `pi`, and `agy` are installed with `mise` (`mise use -g
claude opencode pi agy`), the same tool-version manager the JDK and Maven come
from. Those registry names resolve to each vendor's own release archive; `agy`
is the registry name for `aqua:google-antigravity/antigravity-cli`. The install
lands in the shared `mise` store described below, and an account can still move
an agent to another release with `mise use -g claude@<version>` without root.
Each has a symlink in `/usr/local/bin` pointing at the `mise` binary — a shim,
which resolves the version from the calling account's own configuration — so
`ssh <vm> claude -p '…'` finds the command even though an ssh command runs no
login shell.

`grok`, xAI's CLI, is installed the same way from npm (`mise use -g
npm:@xai-official/grok`), which is the only place it is published, and has the
same `/usr/local/bin` shim. The package's command is `grok`. It carries no
credentials.

`codex` is installed by OpenAI's installer (`https://chatgpt.com/codex/install.sh`)
rather than from npm or mise, because `codex remote-control` runs only against the
standalone package that installer produces: it starts its app-server from a fixed
path under the account's `CODEX_HOME`, and an npm install leaves no such
directory. The package is installed once into `/usr/local/lib/codex` and shared —
it is around 300 MiB — with the command itself in `/usr/local/bin`. Each account
gets a symlink to it at `~/.codex/packages/standalone/current` on first boot, so
credentials and configuration stay per-account.

Every VM built from a full image starts Codex's remote-control daemon at boot for `root` and every
interactive account, through the `agent-vm-codex-remote-control.service` unit
(`codex remote-control start`). That daemon needs credentials, which are per-VM
and never come from a base image or a seed, so on a VM where nobody has run
`codex login` the attempt fails and says so in the journal — deliberately without
failing the boot. Log in inside the VM and run
`sudo systemctl start agent-vm-codex-remote-control` (or `codex remote-control
start` as that account) to start it then; `journalctl -u
agent-vm-codex-remote-control` is where a failure is reported.

#### The Herdr terminal workspace

Every full base image also carries [Herdr](https://herdr.dev), a terminal workspace
manager built for coding agents: the server owns the panes the agents run in, so
an agent left working in a pane keeps working while nobody is attached. It is
installed with `mise` (`mise use -g herdr`) like `claude`, `opencode`, and `pi`,
with the same `/usr/local/bin` shim, and its version is unpinned for the same
reason theirs are.

A server is started at boot for `root` and every interactive account, by the
`agent-vm-herdr.service` unit — which starts one `agent-vm-herdr@<account>`
instance per account, so each server is supervised by systemd and uses that
account's own configuration and socket under `~/.config/herdr`. Unlike the codex
daemon it needs no credentials, so it comes up on a fresh VM. Attach to it by
running `herdr` in an SSH session on the guest, or from the host with `herdr
--remote <ip>` against the VM's address; `systemctl status
agent-vm-herdr@<account>` and `journalctl -u agent-vm-herdr@<account>` are where
a server that will not start reports why. An account that would rather not have
one runs `sudo systemctl stop agent-vm-herdr@<account>`.

Accounts without a `herdr` of their own are skipped, with a line in
`agent-vm-herdr.service`'s journal saying so: an account that has neither the
`~/.local/share/mise` link to the shared store nor a store of its own has no
`mise` data directory at all. Running `mise use -g herdr` as that account and
then `sudo systemctl start agent-vm-herdr@<account>` gives it one.

#### Forge CLIs, wrangler, browsers, and the JVM toolchain

`gh` comes from each distribution's own repository. `tea` does not: only Arch
packages it, and on Ubuntu the name `tea` belongs to an unrelated text editor,
so installing it from apt would put the wrong program at the right command.
It is fetched from Gitea's release server on all three families instead, which
also keeps the version identical everywhere.

`wrangler`, Cloudflare's CLI, is installed with `mise` from npm
(`mise use -g npm:wrangler`) on all three families — no distro packages it, and
npm is the only place Cloudflare publishes it. It rides on the Node runtime the
image installs for it and Playwright.
It carries no credentials: `wrangler login` is an OAuth flow and an API
credential is per-VM, so a fresh guest has the command and no Cloudflare
account attached to it. `WRANGLER_SEND_METRICS=false` is set in
`/etc/environment`, because a disposable VM an agent drives is not a machine
whose operator chose to opt into anonymous usage reporting; unset it in the
guest if you want the default behaviour back.

`cf`, Cloudflare's newer CLI, is installed the same way
(`mise use -g npm:cf`), on the same Node runtime, for the same reason: npm is
the only place Cloudflare publishes it. It ships with no credentials either.
`grok` is installed in that same npm step (`mise use -g npm:@xai-official/grok`).

There is exactly one browser in the image: the Chromium build Playwright pins.
Playwright will not drive a browser it did not install, so a distribution
chromium next to it would be several hundred megabytes that nothing uses — and
on Ubuntu the chromium package is a snap stub, which a VM cannot run at all.
That browser is also exposed as plain `chromium`, so it is usable without going
through Playwright.

The browsers live in `/opt/ms-playwright`, shared by every account rather than
downloaded per user into `~/.cache` — so accounts do not need separate downloads. That directory is world-writable with the sticky bit, the way `/tmp`
is, because every `playwright install` after the build runs as an account that
is not root and Playwright writes a `__dirlock` and a `.links` entry into it: a
root-owned directory would fail those installs with `EACCES`. Any account may
add a browser build; none may remove another account's. `PLAYWRIGHT_BROWSERS_PATH` is set in `/etc/environment` rather than a
profile script, so it applies to non-interactive commands such as
`ssh <vm> node script.js`, which is how an agent actually drives a browser.

The JVM toolchain — and `claude`, `opencode`, `pi`, `agy`, `grok`, and `herdr`,
described above — comes from [mise](https://mise.jdx.dev). The `mise` binary itself is in
`/usr/local/bin`, so every account has the command, and what it installs goes
into `/usr/local/lib/mise`, one store every account shares. Each account's
`~/.local/share/mise` is a symlink to it: from `/etc/skel` for an account
cloud-init creates, and from `agent-vm-user-setup.service` for one that was
created without skel.

The store holds tool *installs*, keyed by name and version, while *which*
version an account uses is its own `~/.config/mise` — so two accounts wanting
different Node releases get two directories in the store and one config file
each, and never contend. Every directory in the store is writable with the
sticky bit, the way `/tmp` and the Playwright browser directory are: any account
may install a tool, none may remove another's. That is defensible for the same
reason those are — the VM is the sandbox, single-tenant and disposable, and the
accounts inside it are not a security boundary.

Sharing the store is also what keeps first boot fast. The toolchain is around
1.7 GiB, and when it lived in `/etc/skel` `useradd` copied all of it into the
new account before cloud-init could get to the SSH keys: about nine seconds
added to every VM's first boot, and 1.7 GiB written into its copy-on-write
overlay. A symlink costs neither.

The newest Temurin JDK mise offers and Maven are installed into that store
during the build, so every account has `java` and `mvn` without downloading anything —
which also works when the guest cannot reach download servers. Neither version is pinned:
they are whatever was current when the image was built. The JDK is requested as
`java@temurin` rather than `java@latest`, which would be an Oracle build of
OpenJDK: mise names a distribution by prefix.

`java` and `mvn` are reached through mise's shims in
`~/.local/share/mise/shims`, which `/etc/profile.d/agent-vm-mise.sh` puts on the
path of a login shell. They therefore resolve in a login shell and not in a
non-interactive `ssh <vm> mvn -version`. Use `ssh <vm> bash -lc 'mvn -version'`,
or put that directory on the path in the script itself — the shims are ordinary
executables, so unlike a shell function there is nothing to source.

The mise-installed agents are not reached that way: each has a symlink in
`/usr/local/bin` pointing at the `mise` binary, which is itself a shim — it
dispatches on the name it was called by and reads the calling account's own
configuration — so `ssh <vm> claude -p '…'` works without a login shell.

To use a different version inside a guest, run `mise use java@21` in a project
or `mise use -g java@21` for the account, and likewise `mise use -g claude@2.1.0`
for an agent. Those need no `sudo`: the new version lands beside the existing
one in the shared store, and the choice is recorded in that account's own
configuration, so no other account's `java` changes.

#### Go and Rust

Neither comes from the distribution. Every family packages some Go and the
versions are years apart, so a guest whose Go is older than the `go` directive
of the repository an agent was given cannot build it at all. Go and
`golangci-lint` are installed with `mise`
(`mise use -g go@latest golangci-lint@latest`), so all three families carry the
same toolchain and an account can move to another release with
`mise use -g go@1.25` without `sudo`. Rust comes from `rustup`. No version is
pinned — they are whatever was current when the image was built.

`go`, `gofmt`, and `golangci-lint` each have a symlink in `/usr/local/bin`
pointing at the `mise` binary, the same arrangement the agents use, and the
rustup proxies (`cargo`, `rustc`, `rustup`, `rustfmt`, `clippy`) are symlinked
into the same directory. That directory is on the default path, so unlike the
JVM toolchain these work in a non-interactive `ssh <vm> cargo build` and not
only in a login shell.

Rust is shared rather than per account: it is installed once into
`/usr/local/rustup` with `CARGO_HOME=/usr/local/cargo`, so no account downloads
its own toolchain at first use, and `rustup update` needs `sudo`. It is
deliberately not managed by `mise` — `mise`'s `rust` is `rustup` underneath and
re-reads `RUSTUP_HOME`/`CARGO_HOME` from the environment of whoever runs
`cargo`, so it would re-run `rustup-init` as each account rather than use what the
shared store already holds, at roughly 1.5 GiB per account.
`RUSTUP_HOME` is set in `/etc/environment` for the same reason
`PLAYWRIGHT_BROWSERS_PATH` is: the rustup proxies find their toolchain through
it, and a non-interactive command reads that file but no profile script.
`CARGO_HOME` is left unset, so `cargo install` writes into the account's own
`~/.cargo`.

The Rust installation is shared, so `rustup update` and `rustup toolchain
install` need `sudo`. What a user installs is not shared: `CARGO_HOME` is
deliberately left unset, so `cargo install` writes into that account's own
`~/.cargo`, as `go install` writes into `~/go`. Both `~/.cargo/bin` and
`~/go/bin` are added to the path of a login shell by
`/etc/profile.d/agent-vm-toolchains.sh`.

A build runs `gh`, `tea`, `wrangler`, `cf`, `playwright`, and `mise`, launches headless Chromium against
`about:blank`, runs `java` and `mvn` in a login shell, runs each virtualization
tool once, runs `go`, `gofmt`, `golangci-lint`, `rustc`, `cargo`, `rustup`,
`cargo fmt`, and `cargo clippy`, and fails if any of it does not work. Chromium is the reason that step exists: a browser missing one shared
library installs perfectly and exits the moment it is launched.

Beyond the coding agents, `tea`, `wrangler`, `cf`, Playwright's browsers, mise,
Node.js, Go, Rust, and `golangci-lint` are the software in a base image that
does not come from the distro's own repository. `tea` comes from its release server, the `mise` binary from its installer,
Rust from `rustup`, and browser binaries from `playwright install`; the other
tools in that list are installed through `mise`. Node.js is installed with `mise`
(`mise use -g node@latest`) on all three distros rather than from a distro
package or a third-party repository — no build adds an APT repository or GPG
key any more — so `node`, `npm`, and `npx` are versioned the way the JDK is and
an account can move to another release with `mise use -g node@<version>`
without `sudo`. Each of the three has a `/usr/local/bin` symlink so a
non-interactive `ssh <vm> node script.js` finds it. `wrangler`, `playwright`,
`cf`, and `grok` are the npm packages in the image, and all four are
installed through `mise`'s npm backend rather than with `npm install -g`.

A build fails outright if the Node.js it ends up with is older than 22.19, checks
that the codex installer really produced its standalone package, and runs each of
the six agents once at the end and fails if any of them cannot
start. Installing an agent and having a working agent are different things, and
the difference would otherwise only surface inside a VM long after the image was
built and cached.

Each agent is configured in its **most permissive mode**, so it acts without
stopping to ask a human to approve individual tool calls: `claude` defaults to
`bypassPermissions`, `codex` to `approval_policy = "never"` with
`sandbox_mode = "danger-full-access"`, and `opencode` allows `edit`, `bash`, and
`webfetch`. `pi` does not gate tool calls at all. `agy` has no configuration file
for permissions, so the image ships a shell alias that adds
`--dangerously-skip-permissions`; that alias reaches interactive shells only, and
a non-interactive caller such as `ssh <vm> agy -p '…'` must pass the flag itself.
`grok` defaults to `permission_mode = "always-approve"` in `~/.grok/config.toml`
and turns its own updater off (`auto_update = false`), so it does not replace
the binary `mise` installed; `agent-vm update` moves it with `mise upgrade`.

`claude` also starts its Remote Control bridge in every session — the
`remoteControlAtStartup` setting, which is what `claude --remote-control` does
from the command line. It is set in the per-account settings file because claude
ignores that setting from project or local settings, and like Codex's daemon it
needs credentials that only arrive per-VM: on a VM where nobody has run
`claude login` a session simply starts without Remote Control connected.

This is deliberate and depends on the VM boundary and the host network policy
holding (see [SECURITY.md](../SECURITY.md)). Default NAT blocks unsolicited
LAN connections to the guest, but does not prevent guest-initiated access to
host services, the LAN, or other guests. See the
[host firewall guidance](./host-setup.md#host-firewalls-and-the-virbrn-bridge).

These configuration files land in `/etc/skel`, so the login user cloud-init
creates gets them, and in `/root`. They can be replaced per VM through
`--cloud-init` without rebuilding the image.

`opencode` has a flag of its own for that:
`create --opencode-config <path>` reads an `opencode.json` from the host and
writes it into both `/etc/skel/.config/opencode/opencode.json` and
`/root/.config/opencode/opencode.json` at first boot, so the login user and
`root` both get it in place of the image's copy. The file must be valid JSON —
otherwise `opencode` would refuse to start, minutes after `create` reported
success — and it is carried base64-encoded inside the generated user-data so
that JSON quoting cannot be reshaped by YAML. Its contents are never logged or
echoed in an error; only its path is.

**No credentials are baked in.** A base image is shared by every VM built on it
and cached indefinitely, so the agents ship configured but unauthenticated. API
keys or logins have to reach each VM separately — through `--cloud-init`, or by
authenticating inside the guest.

The Docker daemon is enabled, so it is running when the VM becomes reachable.
The login user is placed in the `docker`, `libvirt`, and `kvm` groups by the
generated cloud-init user-data, at the moment the account is created, so
`docker` and `virsh` work without `sudo` in the very first SSH session. The base
image also carries a one-shot unit that adds any other interactive account to
those groups after cloud-init has finished, which covers accounts an operator's
own `--cloud-init` file creates. This grants the login user nothing it did not
already have: that account has passwordless `sudo` by design (the guest is
untrusted and root inside it is expected — see [SECURITY.md](../SECURITY.md)).

A base image built before that software was installed into it has none of those
groups. Such an image still boots: the generated user-data declares them, and
cloud-init creates groups before users, so the account is never left uncreated
by a missing group.

#### Nested virtualization

A guest can run VMs of its own, including another `agent-vm`. Every VM is given
the host CPU (`--cpu host-passthrough`), so the guest sees the host's VMX or SVM
feature and `/dev/kvm` works inside it; the base image sets `nested=1` for both
KVM modules so a VM inside that VM can nest once more. This needs nested
virtualization enabled on the **host** — see
[host-setup.md](./host-setup.md) — and it is off on some hosts, in which case
nested `agent-vm` operations cannot use KVM and fail. Other VM tools may
support software emulation, but `agent-vm` requests KVM explicitly.

libvirt is enabled in the guest, so a nested `agent-vm create` finds a running
daemon. The `dnsmasq` in the image is the binary libvirt starts per network; no
system-wide resolver is enabled, which would contend with those instances.

#### Per-account SSH keys

The one-shot unit above also generates an `ed25519` key pair at
`~/.ssh/id_ed25519` for every interactive account, on first boot, if that path
does not already exist. It is generated inside the guest and never leaves it:
no private key is ever placed in a base image or a cloud-init seed (see
[SECURITY.md](../SECURITY.md)). An existing key generated inside the guest is
left alone. Host private keys must not be supplied through `--cloud-init` (see
`SECURITY.md`). The public half is not added to `authorized_keys`; logging in
still requires a key passed to `create`. `agent-vm create --github-ssh-key`
adds the public half to your GitHub account, and `agent-vm destroy
--github-ssh-key` removes it again.

#### The tmux session menu

An interactive login lands on a small menu rather than a bare prompt, because
work in a VM is nearly always work in tmux: a dropped connection loses the run
unless what it was doing lives in a session that outlived the shell.

```
tmux — 2 session(s) on build-01
  n) new session
  a) attach to a session
  l) list sessions
  q) exit to the shell
Choice:
```

`n` asks for a name — letters, numbers, underscore and dash, or empty for a
generated two-word name such as `cooker-opines` (or `session-<short id>`
without a word list) — and attaches to that name if it already exists. Enter
at the main menu starts a generated session without a second prompt.
`a` lists the sessions and takes either a number from that list or a name, with
TAB completing it and listing the candidates when the prefix is ambiguous. `q`
leaves a plain shell, and so does `Ctrl-D`. Attaching replaces the menu;
detaching or ending the session closes the SSH login. Each new session receives
`TMUX_SESSION_NAME` in its environment. Run `agent-vm-menu` from a shell to
open the menu again.

It is deliberately invisible to everything that is not a person at a terminal:
it runs only for an interactive shell with a terminal on both ends, never
inside tmux, and never for `agent-vm ssh <vm> -- <command>`, which is what an
agent driving the VM uses. Set `AGENT_VM_NO_MENU=1` in the environment, or
remove `/etc/profile.d/zz-agent-vm-tmux-menu.sh` through `--cloud-init`, to get
a plain shell at every login.

Full and Nix base images also ship a tmux configuration at `~/.tmux.conf` for
root and for the login user cloud-init creates: mouse mode and a large
scrollback, vi copy-mode keys, `|` and `-` for splits, new windows and panes
opening in the current pane's directory, and no status bar. It is installed
into `/etc/skel` and `/root`, so an operator can replace it per VM with their
own `--cloud-init` file without rebuilding the image. The clipboard bindings
pipe to `xclip` or `pbcopy`, neither of which is present in a headless guest;
the selection still reaches tmux's own paste buffer, so copy and paste work
inside tmux.

Base images built before this tooling was added remain valid and bootable; they
simply lack these packages. Run `agent-vm image build <distro> --force` to
refresh one.

Under `--dry-run`, `image build` prints the pipeline and the file operations it
would perform and exits without touching anything, including the state
directory. Values that only exist once a build has run — the source digest, the
kernel file name — appear as `<placeholders>` rather than as guesses.

### `agent-vm image list`

Lists cached base images: distro, tag, source digest, kernel version, size, and
build time. `--output json` emits the manifests.

### `agent-vm image inspect <distro>[:<tag>]`

Prints one base image's manifest, including the exact source digest and kernel
command line used at boot.

### `agent-vm image rm <distro>[:<tag>]`

Removes a cached base image. Refuses while any VM's overlay still uses it as a
backing file. Prompts for confirmation unless `--yes` is given.

| Flag | Default | Meaning |
|---|---|---|
| `--force` | off | Remove even while VMs still use it as a backing file. Destructive: those VMs' disks become unreadable. |

It also refuses, even with `--force`, while any `create` is in progress (exit
`5`): until that create records its VM, the record that would show it depends on
the image does not exist yet. Run it again once the create finishes.

### `agent-vm create <name>`

Creates and starts a VM. `<name>` must match `^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`
and must not already exist.

| Flag | Default | Meaning |
|---|---|---|
| `--distro <name>[:<tag>]` | `ubuntu` | Base image to use; built automatically if not cached. Append `-slim` (`ubuntu-slim`) or `-nix` (`ubuntu-nix`) to the family for that variant. |
| `--vcpus <n>` | `2` | Virtual CPUs, from 1 to 255. |
| `--memory <size>` | `4G` | Guest RAM at boot (`512M`, `4G`, `8G`), from `256M` to `1024G`. |
| `--max-memory <size>` | unset | Ceiling the guest's RAM can be grown to while it runs, using a `virtio-mem` device. Unset means a fixed-size guest. See [Growable Memory](#growable-memory). |
| `--disk <size>` | `50G` | Virtual root disk size (thin overlay), from `1G` to `8T`. |
| `--network <nat\|bridge>` | `nat` | Network mode. |
| `--bridge <iface>` | config value | Host bridge to attach to; required with `--network bridge` unless configured. |
| `--ssh-key <path>` | config value, else this account's `~/.ssh` identities | SSH **public** key(s) to authorize; repeatable. |
| `--host-authorized-keys` | off | Also authorize every key in this host account's `~/.ssh/authorized_keys` and `~/.ssh/authorized-keys/authorized_keys`. Combines with `--ssh-key`. |
| `--cloud-init <path>` | none | Extra cloud-init user-data merged into the generated user-data. |
| `--opencode-config <path>` | none | Your own `opencode.json`, installed in the guest in place of the one the base image ships. Must be valid JSON. |
| `--virt-install-arg <arg>` | none | Extra argument passed through to `virt-install`; repeatable. The escape hatch for anything this CLI does not expose. |
| `--no-start` | off | **Not honored — rejected with exit `2`.** See below. |
| `--wait-for-ssh <duration>` | `90s` | How long to wait for the guest to accept SSH; `0` disables waiting. |
| `--github-ssh-key` | off | Add the SSH public key the guest generated for itself to your GitHub account, using `gh`. Requires a wait. |

On success, prints the VM name, address, and SSH command; with `--output json`,
prints the same `vm.json` record the tool stored.

Inputs are checked before anything on the host is touched. The SSH keys, the
`--cloud-init` file (it must begin with a header cloud-init recognizes), and the
`--opencode-config` file (it must be valid JSON) are read and validated first,
and a bad one exits `2` — under `--dry-run` too. Next, the minimum versions of
`virt-install`, `virsh`, `qemu-img`, and `virt-make-fs` (plus `gh` with
`--github-ssh-key`) are checked, and a missing or too-old tool exits `3`. Only
then is the VM locked and its base image looked up or built.

`create` is transactional. If a step through recording `vm.json` fails, the tool
removes the domain, the overlay, the generated seed, and the state directory
it created, and reports both the original failure and any cleanup problem.
This includes a `virt-install` that fails, times out, or is interrupted after it
has already defined the domain, and a create interrupted with Ctrl-C or
`SIGTERM`: the cleanup still runs. Whenever the cleanup leaves anything behind,
`create` exits `7` and lists it, whatever the original failure was.
`virsh undefine` is never given `--remove-all-storage`; the tool deletes its own
files after verifying they are inside the state directory.

#### Growable Memory

`--memory` is what the guest boots with; `--max-memory` is how large it may
become without a reboot. Giving both attaches a
[`virtio-mem`](https://www.qemu.org/docs/master/system/devices/virtio-mem.html)
device sized to the difference, which starts with nothing plugged in — a VM
created with `--memory 4G --max-memory 16G` boots with exactly 4 GiB and can be
grown to 16 GiB later.

```console
$ agent-vm create agent-01 --memory 4G --max-memory 16G
$ virsh --connect qemu:///system update-memory-device agent-01 --requested-size 8G --live
```

The guest then has 12 GiB: its 4 GiB of boot memory plus the 8 GiB requested
from the device. Shrinking works the same way — lower `--requested-size` — but
the guest is free to refuse to give a block back, so the size after a shrink is
a request, not a guarantee. `agent-vm` does not resize a running VM itself;
`virsh update-memory-device` is the supported way to do it.

Constraints, all of which are checked before anything on the host changes:

- `--max-memory` must be greater than `--memory`, and both are bounded by the
  same limits (`256M` to `1024G`).
- The growth room — the difference between the two — must be a multiple of
  2 MiB, the block size the device is defined with. libvirt requires a block
  size to be stated, so this is a fixed 2 MiB rather than something QEMU picks
  per host; a host with 64 KiB pages (some aarch64 kernels) needs a 512 MiB
  block and will reject the device with QEMU's own message naming the size it
  wanted.
- The guest kernel must bring hotplugged blocks online. Base images built by
  this tool boot with `memhp_default_state=online_movable`, which does that;
  a VM created from a base image cached before that command line existed will
  see the memory as offline blocks until the image is rebuilt with
  `agent-vm image build <distro> --force`.

A VM created without `--max-memory` gets exactly the domain it always did: no
`maxMemory`, no guest NUMA topology, and no memory device.

After the VM is recorded, boot-wait and GitHub registration failures retain
it for inspection. A `--wait-for-ssh` timeout
exits `6` and **leaves the VM in place** with its `console.log`, because "it
booted slowly" and "it failed to boot" need the same evidence. Clean it up with
`agent-vm destroy <name>` once you have looked.

#### Default SSH keys

When no key is named — no `--ssh-key`, no `AGENT_VM_SSH_KEY`, and no
`[guest] ssh_keys` in the config file — `create` authorizes the public halves of
this account's OpenSSH identities, the same files `ssh` itself offers when it is
run without `-i`:

| Path |
|---|
| `~/.ssh/id_ed25519.pub` |
| `~/.ssh/id_ed25519_sk.pub` |
| `~/.ssh/id_ecdsa.pub` |
| `~/.ssh/id_ecdsa_sk.pub` |
| `~/.ssh/id_dsa.pub` |
| `~/.ssh/id_rsa.pub` |
| `~/.ssh/id_xmss.pub` |

Every one of them that exists is authorized, in that order, because the operator
may reach the guest from any host holding any of those keys. A file that is
absent, empty, or not a public key is skipped without complaint: these paths are
a fallback the tool guessed at, not paths the operator asked for. Each file that
was used is recorded in `vm.json`, key material never is.

Naming a key with `--ssh-key`, `AGENT_VM_SSH_KEY`, or the config file turns the
fallback off entirely — the guest gets exactly the keys that were named (plus
the host's, with `--host-authorized-keys`). If no key is named and none of the
defaults exist, `create` exits `2` before anything is created, naming the files
it looked for.

#### `--host-authorized-keys`

Authorizes the keys that already log in to *this* host account in the guest as
well, so whoever can reach the host can reach the VMs it creates without their
keys being listed a second time. Two files are read, in this order:

| Path | |
|---|---|
| `~/.ssh/authorized_keys` | The default location. |
| `~/.ssh/authorized-keys/authorized_keys` | Read as well, because sshd is routinely pointed at it with `AuthorizedKeysFile`. |

Either file may be absent — that is skipped, not an error. Finding no file at
all, or finding files with no key in them, is a usage error (exit `2`) reported
before anything is created: the flag asked for keys and produced none.

It combines with `--ssh-key`: the guest's `authorized_keys` holds the keys named
by the flag first, then the host's, with a key that appears in both authorized
once. Either source alone is enough — a VM created with only
`--host-authorized-keys` is reachable by the host's keys. Every file that was
read is recorded as a path in `vm.json`; key material itself never goes there.

Only plain key lines are accepted. Comments and blank lines are skipped, and an
entry carrying OpenSSH options (`command=`, `restrict`, `from=`) is refused
rather than stripped or dropped: the restriction is one the operator wrote down,
and applying it to a guest or discarding it are both decisions this tool leaves
to them. Pass such a key explicitly with `--ssh-key` if you want it in the VM.

`--no-start` is rejected rather than approximated. `virt-install` always boots
the guest it defines, and a VM stopped before cloud-init finished would never
receive its SSH key and could not be reached afterwards. Create the VM and stop
it instead:

```console
$ agent-vm create build-01 && agent-vm stop build-01
```

#### `--github-ssh-key`

Full and Nix images generate an `ed25519` key pair on first boot (see
[Per-account SSH keys](#per-account-ssh-keys)). Slim images omit that setup
service; using this flag with a slim image requires arranging key generation
inside the guest yourself before the read times out. With `--github-ssh-key`,
`create` reads the **public** half back over SSH once the guest is reachable
and adds it to your GitHub account as an authentication key titled `agent-vm
<name> on <host>`, so an agent in the VM can push without a key being pasted
in by hand.

`gh` runs on the host, with your existing login; no GitHub credential ever
enters the guest, and the private key never leaves it. `gh auth status` is
checked before anything is created, so an expired login costs nothing. The key's
numeric id is recorded in `vm.json` under `guest.githubKey`, which is what
`destroy --github-ssh-key` removes it by.

The flag needs a boot wait: with `--wait-for-ssh 0` there is no reachable guest
to read the key from, so the combination exits `2`. The key is generated by a
first-boot unit ordered after `cloud-final.service`, which finishes *after*
sshd starts accepting logins, so the file is usually still missing at the
moment the boot wait ends. `create` therefore retries the read for up to
another `--wait-for-ssh` and exits `6` naming the unit if it never appears. If `gh` fails after the VM
exists, the VM is **left in place** — the VM is not what failed — and the error
names the `gh` command to rerun. Adding the key to a forge other than GitHub is
not supported; do it by hand with the key `agent-vm ssh <name> cat
.ssh/id_ed25519.pub` prints.

The readiness probe and `agent-vm ssh` both run `ssh` with
`StrictHostKeyChecking=no` and `UserKnownHostsFile=/dev/null`. A VM here is
disposable and generates a fresh host key every time, so recording host keys
would leave the operator with a `known_hosts` file full of conflicts for reused
addresses. In the default NAT mode the network is host-local; with
`--network bridge` the guest is on the LAN and this is a weaker guarantee.

### `agent-vm list`

Lists VMs known to this state directory with state, distro, resources, network
mode, address, and creation time. Domains that exist in libvirt but not in state
are not listed; domains in state that have vanished from libvirt are reported as
`missing`. With `--output json`, emits an array of stored VM records augmented
with live `state` and an optional `address`.

A VM whose `vm.json` cannot be read — another `schemaVersion`, a parse error,
or a record that does not match its directory — is left out and named in a
warning on stderr, and the other VMs are still listed. Subdirectories of `vms/`
whose names are not valid VM names (`lost+found`, for example) are not VMs and
are ignored. `image rm`, by contrast, refuses to proceed past a record it
cannot read, because that VM may depend on the image.

### `agent-vm info <name>`

Prints one VM's full record, including the base image digest it was created from,
the overlay path, the MAC address, the captured domain XML path, and the
`virt-install` version and argument vector that defined it.

With `--output json`, the stored VM fields are augmented with live `state`, an
optional `address`, and an optional `disk` object containing `virtualSize`,
`actualSize`, and, when available, `backingFile`. Disk sizes are size strings;
`disk` is omitted when the overlay cannot be inspected.

### `agent-vm start <name>` / `stop <name>` / `restart <name>`

Lifecycle control. `stop` requests a graceful ACPI shutdown and waits
`--timeout`, then reports failure (exit `6`) — it never silently escalates to a
force-off. `restart` is `stop` followed by `start`, and does not start a guest
it could not stop: a `restart` that times out leaves the VM running, exactly as
it was.

All three act only on VMs recorded in this state directory. An operation whose
state does not allow it — starting a running VM, stopping a stopped one — exits
`5` and names the state it found. A VM that is recorded here but whose libvirt
domain has been undefined by hand exits `4`; the record is reported, never
repaired by redefining someone else's domain.

Flags for `stop` and `restart`:

| Flag | Default | Meaning |
|---|---|---|
| `--timeout <duration>` | `60s` | How long to wait for a graceful shutdown. |
| `--force` | off | Power off immediately (`virsh destroy`) instead of requesting a graceful shutdown. Can lose guest writes. |

### `agent-vm ssh <name> [-- <command>...]`

Execs `ssh` to the VM as the guest user, or runs a command non-interactively and
forwards its exit status. Resolves the address with
`virsh domifaddr --source lease` for a NAT VM — the address libvirt's own DHCP
server handed out — and `virsh domifaddr --source agent` for a bridged VM, whose
DHCP server is the LAN's. Either way only the interface libvirt defined, by the
MAC recorded in `vm.json`, is used. The guest is untrusted and is never asked
where it is on NAT. This is a
convenience wrapper around `ssh`, not an SSH implementation — `--dry-run` prints
the `ssh` command so you can use it directly.

Everything after `--` is the guest's command line and is passed through
untouched, including anything that looks like an `agent-vm` flag. A command given
this way runs in batch mode so it fails instead of stopping at a prompt; an
interactive session may prompt.

Only the **public** key paths are recorded for a VM, so the key to authenticate
with is located by the usual convention that `id_ed25519` sits beside
`id_ed25519.pub`. When that file exists it is named to `ssh` with `-i`; when it
does not, `ssh` falls back to your agent and defaults as usual. This tool never
reads private key material.

A VM that is not running exits `5`, and one that is running but has no address
yet exits `6` — `ssh` would otherwise report a connection failure that says
nothing about which of the two happened.

### `agent-vm update <name>... | --all`

Brings a running guest's software up to date over SSH — both its distribution
packages and the tooling the base images install from outside the distro's
repositories, which no package manager knows about.

The distribution packages first, using the package-manager commands for the
family the VM's base image was built from: `apt-get update`, then a
non-interactive `apt-get dist-upgrade` and `apt-get --purge autoremove` on
Ubuntu; `dnf --refresh upgrade` and `dnf autoremove` on Fedora;
`pacman -Syu --noconfirm` on Arch.

Then the rest of what a guest carries, in this order:

| Step | What it runs | As |
|---|---|---|
| root's mise temporary directory | `mkdir -p -- /root/.cache/mise-tmp` | root |
| `mise` itself | `mise self-update --yes`, which also refreshes its plugins, with `HOME=/root` and `TMPDIR=/root/.cache/mise-tmp` | root |
| root's mise-managed tools | `mise upgrade --yes` with `HOME=/root` and `TMPDIR=/root/.cache/mise-tmp` | root |
| the guest user's mise-managed tools | `mise upgrade --yes` | the guest user |
| `codex` | `codex update` with `CODEX_HOME=/usr/local/lib/codex` | root |
| the Rust toolchain | `rustup update` with `RUSTUP_HOME=/usr/local/rustup` and `CARGO_HOME=/usr/local/cargo` | root |

`mise upgrade` covers everything `mise` manages in that account: `node`, the
`claude`, `opencode` and `pi` agents, `herdr`, `java` and `maven`, `go` and
`golangci-lint`, `agy`, and the npm-backed `wrangler`, `playwright`, `cf` and
`grok`. It is run
for both accounts because `mise`'s configuration is per account — the installs
are in one shared store, but which version each account uses is recorded under
its own home, and both accounts are in use. The second run is cheap: whatever
the first one installed is already in the store, so only the configuration
moves. `codex` and Rust are shared, root-owned installations, so each is updated
once for the whole VM.

Steps run as root go through `sudo -n` as the guest user, which cloud-init grants
passwordless sudo, and the guest's own output is streamed to stderr as it runs.
Root's `mise` steps are given their own `TMPDIR` because `mise`'s npm backend
locks each install under `$TMPDIR/fslock`, a directory owned by whichever
account creates it: left at the default, root's steps would take `/tmp/fslock`
and the guest user's step after them would fail to acquire its lock. The
directory is created first. `mise self-update` downloads the replacement
binary into a file directly under `$TMPDIR` and does not create that
directory, so an update against a guest that has never had the path stops
with "No such file or directory" before replacing anything.
Before each tooling step the guest is asked whether it has the command at all; a
VM built from an image that predates one — or from an image you built yourself —
skips that step and reports it, rather than failing the update.

The Playwright browser downloads are deliberately left alone. They are
refreshed with `playwright install` rather than by upgrading a package.
`agy` and `grok` are not: both are mise installs, so the `mise upgrade` steps
move them with everything else in the store.

Takes one or more VM names, or `--all` for every VM recorded in this state
directory. The two spellings are mutually exclusive, and giving neither exits `2`.

| Flag | Default | Meaning |
|---|---|---|
| `--all` | off | Update every running VM in this state directory instead of naming them. |
| `--timeout <duration>` | `45m` | How long one VM's whole update may take — every step above shares the one budget. Exceeding it exits `6`. |

Under `--all`, a VM that is not running is **skipped**, not started: reported as
`skipped: not running` and left alone. A VM named explicitly that is not running
exits `5`, and one that is running with no address yet exits `6` — the same
distinction `ssh` makes. A VM whose libvirt domain has been undefined by hand
exits `4`.

Every target is attempted even after one fails, so a single guest with an
unreachable mirror does not leave the rest of the fleet un-updated. Each failure
is reported to stderr as it happens; one failure exits with that failure's own
code, and several exit `1` naming the VMs that did not update. `--output json`
returns one object per VM with `name`, `state`, `updated`, an optional `skipped`
reason, and an optional `error`.

This updates the software **inside** a guest's overlay only. Guests boot the
kernel and initramfs from their base image on the host ([direct kernel
boot](./decisions/0004-direct-kernel-boot-with-copy-on-write-overlays.md)), so a kernel package upgraded
here is not the kernel the VM boots next time: a newer guest kernel comes from
`agent-vm image build <distro> --force` and a VM created from the rebuilt image.
Nothing is rebooted, and no base image is modified — an update lives and dies
with the VM it ran in.

### `agent-vm destroy <name>`

Powers off the VM, undefines the domain, and deletes its state directory,
overlay, and generated seed. Prompts for confirmation unless `--yes` is given.

| Flag | Default | Meaning |
|---|---|---|
| `--keep-disk` | off | Keep the overlay and state directory; only remove the libvirt domain. |
| `--force` | off | Power off immediately instead of attempting graceful shutdown. |
| `--timeout <duration>` | `60s` | How long to wait for the graceful shutdown. |
| `--github-ssh-key` | off | Also remove this VM's SSH key from your GitHub account, using `gh`. |

`--github-ssh-key` removes the key `create --github-ssh-key` added, by the id
recorded in `vm.json`. It runs **first**, while the VM is still intact: if `gh`
fails, nothing is destroyed and the record that names the key is still there to
retry with. `gh auth status` is checked first, because GitHub answers a token
without the `admin:public_key` scope with the same 404 as a key that does not
exist. A key someone already deleted on github.com is not an error; the message
names the account `gh` is logged in to, since a key added from another account
looks the same, along with the command that removes it. A
VM with no recorded key exits `4`. Without the flag, a destroy of a VM that has
one says so and prints the `gh` command that removes it — the key is never
deleted implicitly.

`destroy` only ever touches domains and paths recorded in this state directory.
It refuses to remove a path that does not resolve inside the state directory,
and it refuses to undefine a libvirt domain it did not create — a domain whose
disk is not the overlay recorded here exits `5` and names the disks it found.
The record is read again once the VM's lock is held. A VM that another command
destroyed while this one waited exits `4`. One that was destroyed and created
again under the same name exits `5` with nothing changed.

A guest that ignores the shutdown request exits `6` with the VM intact and
nothing removed; `destroy` never escalates to a force-off on its own, because
the disk is about to be deleted and that is the last moment unwritten data can
still be saved. A VM whose libvirt domain has already been undefined by hand is
not an error: its leftover state is removed, which is what this command is for.

### `agent-vm console <name>`

Execs `virsh console` for the VM. The console is also logged to
`vms/<name>/console.log` for post-mortem debugging of a boot failure — so a VM
that is not running exits `5` and names that log, which is what you actually
want when a guest failed to boot.

### `agent-vm licenses`

Prints the license for SnapHop Agent VM and the third-party notices for the
software linked into the binary. The text form writes the `LICENSE` file, a
blank line, and the `NOTICE` file. `--output json` writes one object:

| Field | Meaning |
|---|---|
| `license` | The MIT license, exactly the `LICENSE` file. |
| `notice` | The copyright and permission notices for BurntSushi/toml and for the Go standard library and runtime, exactly the `NOTICE` file. |

An unexpected argument exits `2`.

The same text is embedded in the binary, so a copied binary still carries the
notices. `scripts/build-release.sh` also places `LICENSE` and `NOTICE` in
`dist/` next to the binaries, and a GitHub Release attaches those files.

### `agent-vm completion <bash|zsh|fish>`

Prints a tab-completion script for the named shell to stdout. Naming an
unsupported shell, or no shell at all, exits `2`.

```bash
# bash, for one user
mkdir -p ~/.local/share/bash-completion/completions
agent-vm completion bash > ~/.local/share/bash-completion/completions/agent-vm

# zsh, into the first directory on $fpath (compinit must run in ~/.zshrc)
agent-vm completion zsh > "${fpath[1]}/_agent-vm"

# fish
mkdir -p ~/.config/fish/completions
agent-vm completion fish > ~/.config/fish/completions/agent-vm.fish
```

Completion covers command and subcommand names, each command's own flags, the
global flags, the values of flags whose set of values is closed (`--output`,
`--network`, `--platform`), the supported distro families and variants for
`image build`, and — read from the state directory — the recorded VM names for
`info`, `start`, `stop`, `restart`, `ssh`, `update`, `console`, and `destroy`,
and the cached images for `image inspect`, `image rm`, and `--distro`. Where
`agent-vm` offers nothing, the shell falls back to filenames, which is what
`--config`, `--ssh-key`, and `--cloud-init` want. Nothing is offered after
`--` in `agent-vm ssh <name> --`, because what follows runs in the guest.

The scripts call `agent-vm __complete <word>...`, a hidden command that takes
the words typed so far — the last being the word under the cursor — and prints
one candidate per line. It is an interface for shells, not for operators: it
reads the state directory without creating it, never changes anything, and
always exits `0`, because an error printed by a completion helper would land in
the middle of what the operator is typing. With a local libvirt URI it spawns
no process. With a `qemu+ssh://` URI the state directory is on the hypervisor,
so each Tab that completes a VM name or cached image runs `ssh` and
read-only commands such as `find`, `cat`, and `test`
there; that is slower than a local read, but reading this machine instead would
offer nothing. A flag written as `--output=j` completes its value, and the bash
script rebuilds the words from `COMP_LINE` rather than bash's own word list,
which splits at `:` and `=`, so image refs such as `ubuntu:24.04` complete
too. Its output format is not a stable contract; the shell scripts are
generated from the same build, so the two cannot drift apart.

## Underlying Commands

The tool orchestrates standard host tools rather than reimplementing them
([ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md)), so every
operation can be inspected and reproduced by hand. `--dry-run` prints planned
invocations, with placeholders for values unavailable without execution; the
table below is the summary.

| Operation | Tools invoked |
|---|---|
| `image build` | `podman pull`, `podman image inspect` (to pin the digest), `podman build`, `podman create`, `podman export`, `podman rm`, `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| `create --github-ssh-key` | the `create` tools, plus `gh auth status`, `ssh <guest> cat .ssh/id_ed25519.pub`, `gh api --method POST user/keys` |
| `destroy --github-ssh-key` | the `destroy` tools, plus `gh auth status`, `gh api --method DELETE user/keys/<id>`, and `gh api user` (to name the account when the key is not there) |
| `create --max-memory` | the `create` tools; `virt-install` additionally gets `--memory <boot>,maxMemory=<ceiling>,maxMemory.slots=16`, a single-cell guest NUMA topology on `--cpu`, and `--memdev model=virtio-mem,target.node=0,target.block=2048,target.size=<growth>,target.requested=0` |
| `create` | `virt-make-fs --type=vfat --label=cidata` (the cloud-init seed), `qemu-img create`, `virsh net-list`/`net-define`/`net-start`/`net-autostart` (autostart only for a network it defined), `virsh net-dumpxml` (an existing network must forward by NAT), `ip -d -json link` (bridge mode), `virsh capabilities`, `virt-install --import --boot kernel=…,initrd=… --disk …seed.img,bus=virtio,readonly=on`, `virsh domifaddr`, `virsh domiflist`, `virsh dumpxml`, `ssh` (readiness probe) |
| `list` / `info` | `virsh list --all --name`, `virsh domstate`, `virsh domifaddr`, `qemu-img info -U --output=json` (`info` only) |
| `start` / `stop` / `restart` | `virsh start`, `virsh shutdown`, `virsh destroy` (for `--force`) |
| `ssh` | `virsh domstate`, `virsh domifaddr`, then `ssh` |
| `update` | `virsh domstate`, `virsh domifaddr`, then one `ssh <guest> …` per step: the guest family's package manager (`apt-get`, `dnf`, or `pacman`), then `mkdir -p` (root's mise temporary directory), `mise`, `codex`, and `rustup`, each preceded by an `ssh <guest> command -v <tool>` probe and run under `sudo -n` where it needs root |
| `console` | `virsh domstate`, then `virsh console` |
| `destroy` | `virsh domblklist` (to confirm the domain is the one recorded here), `virsh shutdown` or `virsh destroy`, `virsh undefine` (never `--remove-all-storage`), then file removal inside the state directory |
| `completion` / `__complete` | none with a local libvirt URI — completion reads the state directory and spawns no process; with `qemu+ssh://…`, reading the state directory runs `ssh` and read-only commands such as `find`, `cat`, `test`, and `readlink` on the hypervisor on each Tab |
| `licenses` | none — prints the embedded license texts and spawns no process |
| any command, with `--libvirt-uri qemu+ssh://…` | every invocation above that touches a disk, an image, or a domain, wrapped as `ssh -- <destination> <quoted-command>`; the state directory is managed there with `mkdir`, `dd`, `chmod`, `mv`, `cat`, `rm`, `find`, `readlink`, `stat`, `df`, `du`, and `flock`. `gh` and the `ssh` into a guest still run here, the latter as `ssh -J <destination> …` |
| `doctor` | `virsh version`, plus `--version` on every required tool (`virt-install`, `qemu-img`, `podman`, `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`), `ip -V`, `ssh -V`, `gh --version` (optional), `virsh net-list`, `virsh net-dumpxml` (to name the NAT bridge the two firewall checks match rules against), `uname -m` and `uname -r` with `cat /boot/config-<release>` or `zcat /proc/config.gz` (to judge whether the host kernel can boot a libguestfs appliance), and — when a bridge is configured — `ip -d -json link` |

Because these are the same commands documented in every libvirt guide, anything
this CLI does not expose can still be done directly: `--virt-install-arg` passes
arguments through, and `virsh edit` works on a defined domain as usual. Structured
output (`--output=json`, `--format json`, `virsh` query subcommands) is used
wherever a tool provides it, so the tool does not depend on human-readable text
staying stable.

## Exit Codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Generic failure. |
| `2` | Usage error: unknown flag, bad argument, invalid name or size. |
| `3` | Host not ready: no KVM, no libvirt connection, missing helper binary. |
| `4` | Not found: unknown VM or base image. |
| `5` | Conflict: VM already exists, wrong state for the operation, or a base image is in use. A held lock is never itself a conflict: `create` and `destroy` wait for the VM's lock, and `image build` and `image rm` wait for the image's lock (see [State Layout](#state-layout)). |
| `6` | Timeout: guest did not boot, become reachable, or shut down in time. |
| `7` | Cleanup incomplete: an operation or its rollback left host state behind that needs attention. |

## State Layout

```text
$STATE_DIR/
├── images/
│   └── <distro>/<tag>/
│       ├── base.qcow2          # immutable, read-only backing file
│       ├── vmlinuz
│       ├── initrd
│       ├── Containerfile       # the recipe this image was built from (a record)
│       └── manifest.json       # source digest, kernel version, cmdline,
│                               # builder tool versions, schemaVersion
├── vms/
│   └── <name>/
│       ├── root.qcow2          # copy-on-write overlay on base.qcow2
│       ├── seed/               # exactly what cloud-init reads, and nothing else
│       │   ├── user-data       # generated cloud-init user-data
│       │   └── meta-data       # instance id and hostname
│       ├── seed.img            # the NoCloud seed disk built from seed/ (vdb)
│       ├── domain.xml          # captured `virsh dumpxml` output (a record, not an input)
│       ├── console.log         # serial console capture
│       └── vm.json             # VM record incl. virt-install version + argv, schemaVersion
├── networks/
│   └── <name>.xml              # network XML passed to `virsh net-define` (a record)
└── locks/                      # advisory file locks, one per VM and per base image
```

`networks/` and `locks/` hold the tool's own bookkeeping. `create` and
`destroy` take the VM's lock, and `image build` and `image rm` take the base
image's lock; each waits for a lock another process holds rather than failing,
until that process finishes or the command is interrupted. Other commands take
no lock. Locks are released by the kernel when the process holding one exits,
so a crashed run never leaves a lock that has to be cleared by hand.

Both `manifest.json` and `vm.json` carry a `schemaVersion`. The tool refuses to
operate on a version it does not understand and says what to rebuild instead of
guessing. Both are at version `1`.

A `vm.json` is also refused (exit `1`) when its `name` differs from the
directory it is in, when `paths.dir` is not exactly that directory, or when any
other recorded path lies outside it: commands act on those paths, and
`destroy` deletes `paths.dir`, so a corrupted or hand-edited record is never
trusted to point elsewhere.

## Stored Records

These two files are a public contract: scripts and agent supervisors read them
directly. With `--output json`, `create` emits the `vm.json` record; `info`
and `list` add live fields as described above. `image inspect` emits one
manifest, and `image list` emits an array of them. Fields are added compatibly;
renaming or removing one,
or changing what a value means, raises `schemaVersion`.

### `vms/<name>/vm.json`

The system of record for a VM's configuration and provenance. Runtime state is
**not** here — libvirt owns that, and the tool asks it rather than caching an
answer that goes stale.

```json
{
  "schemaVersion": 1,
  "name": "agent-01",
  "createdAt": "2026-08-19T09:14:03Z",
  "libvirtUri": "qemu:///system",
  "distro": "ubuntu:24.04",
  "baseImage": {
    "distro": "ubuntu",
    "tag": "24.04",
    "sourceRef": "docker.io/library/ubuntu:24.04",
    "sourceDigest": "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
    "path": "/home/you/.local/share/agent-vm/images/ubuntu/24.04/base.qcow2"
  },
  "resources": { "vcpus": 2, "memory": "4G", "disk": "50G" },
  "network": { "mode": "nat", "name": "agent-vm-nat", "mac": "52:54:00:1a:2b:3c" },
  "guest": {
    "user": "agent",
    "sshKeyPaths": ["/home/you/.ssh/id_ed25519.pub"]
  },
  "paths": {
    "dir": "/home/you/.local/share/agent-vm/vms/agent-01",
    "overlay": "/home/you/.local/share/agent-vm/vms/agent-01/root.qcow2",
    "seedDir": "/home/you/.local/share/agent-vm/vms/agent-01/seed",
    "userData": "/home/you/.local/share/agent-vm/vms/agent-01/seed/user-data",
    "metaData": "/home/you/.local/share/agent-vm/vms/agent-01/seed/meta-data",
    "seedImage": "/home/you/.local/share/agent-vm/vms/agent-01/seed.img",
    "domainXml": "/home/you/.local/share/agent-vm/vms/agent-01/domain.xml",
    "consoleLog": "/home/you/.local/share/agent-vm/vms/agent-01/console.log"
  },
  "createdBy": {
    "agentVmVersion": "0.1.0",
    "virtInstallVersion": "5.1.0",
    "virtInstallArgv": ["virt-install", "--connect", "qemu:///system", "…"]
  }
}
```

| Field | Meaning |
|---|---|
| `schemaVersion` | Schema of this file. A file with an unknown version is refused, never guessed at. |
| `name`, `createdAt`, `libvirtUri` | Identity, creation time (UTC), and the connection the domain was defined on. |
| `distro` | The resolved `<distro>:<tag>` reference, including the default tag when omitted. |
| `baseImage` | The image the overlay is backed by, **by digest as well as by name** — a tag can be rebuilt, a digest cannot. `path` is the backing file. |
| `resources` | What the domain was defined with. `memory` and `disk` are size strings (`4G`, `50G`), not byte counts. `maxMemory` is present only for a VM created with `--max-memory`, and is the ceiling its `virtio-mem` device can grow it to. |
| `network` | `mode` is `nat` or `bridge`; `name` is the libvirt network in NAT mode, `bridge` the host interface in bridge mode, and `mac` is what libvirt allocated. Recorded so exposure stays auditable after the fact. |
| `guest.user` | The account to SSH in as. |
| `guest.sshKeyPaths` | Paths of the **public** keys that were authorized; their contents are not stored in this field. |
| `guest.githubKey` | Present only for a VM created with `--github-ssh-key`: `id`, `title`, `publicKey`, `addedAt`. The `id` is what `destroy --github-ssh-key` removes the key by. |
| `paths` | Absolute paths inside the state directory: the VM's directory, its overlay, the seed directory and the two files in it, the seed disk built from them, the captured `domain.xml`, and `console.log`. |
| `createdBy` | Provenance: the `agent-vm` and `virt-install` versions, and the exact argument vector that defined the domain. |

### `images/<distro>/<tag>/manifest.json`

What a cached base image is and how it was produced.

```json
{
  "schemaVersion": 1,
  "distro": "ubuntu",
  "tag": "24.04",
  "builtAt": "2026-08-19T09:14:03Z",
  "platform": "linux/amd64",
  "sourceRef": "docker.io/library/ubuntu:24.04",
  "sourceDigest": "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
  "kernelVersion": "6.8.0-31-generic",
  "kernelCmdline": "root=/dev/vda1 console=ttyS0 console=ttyAMA0 rw memhp_default_state=online_movable",
  "baseDiskBytes": 1502576640,
  "toolVersions": {
    "podman": "6.1.0",
    "virt-make-fs": "1.56.0",
    "virt-ls": "1.56.0",
    "virt-copy-out": "1.56.0",
    "virt-sysprep": "1.56.0"
  },
  "agentVmVersion": "0.1.0"
}
```

| Field | Meaning |
|---|---|
| `schemaVersion` | Schema of this file. Base images built by an older release stay bootable, or this rises with a documented rebuild path. |
| `distro`, `tag`, `builtAt`, `platform` | Identity, build time (UTC), and the OS/arch that was pulled. |
| `sourceRef` | The OCI reference that was named. |
| `sourceDigest` | What was actually pulled — the authoritative identity, and what everything after the pull was built on. |
| `kernelVersion` | The kernel in the image, read from `/usr/lib/modules`. |
| `kernelCmdline` | The command line every VM on this image boots with. Under direct kernel boot it lives on the host, not in the guest, so it is only discoverable here ([ADR-0004](./decisions/0004-direct-kernel-boot-with-copy-on-write-overlays.md)). |
| `baseDiskBytes` | Size of `base.qcow2` in bytes. |
| `toolVersions` | The version of each tool that produced the image, so an artifact built by a known-bad version can be found later. |
| `agentVmVersion` | The `agent-vm` build that ran the pipeline. |
