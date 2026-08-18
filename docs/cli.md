# CLI Contract

> This is the canonical reference for the `agent-vm` command line. It is a
> public contract: subcommands, flags, defaults, JSON output fields, and exit
> codes are consumed by humans, scripts, and agent supervisors. Changing any of
> them is a contract change under `AGENTS.md` §8.
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
| `--libvirt-uri <uri>` | `qemu:///system` | libvirt connection URI. |
| `--output <text\|json>` | `text` | Output format. `json` is machine-readable and stable. |
| `--verbose` | off | Debug-level logging to stderr. |
| `--quiet` | off | Suppress progress output; errors still go to stderr. |
| `--yes` | off | Skip interactive confirmation for destructive operations. |
| `--dry-run` | off | Print the exact tool invocations the operation would run, and exit 0 without changing anything. |
| `--version` | — | Print the `agent-vm` version, plus the detected version of every required tool: `virsh` (which reports libvirt's version), `virt-install`, `qemu-img`, `podman`, the libguestfs tools, `ip`, and `ssh`. |

Human-readable progress and logs go to **stderr**. Command results go to
**stdout**, so `--output json` can be piped safely.

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
libvirt_uri = "qemu:///system"

[defaults]
distro  = "ubuntu"
vcpus   = 2
memory  = "4G"
disk    = "50G"
network = "nat"

[network.nat]
name = "agent-vm-nat"

[network.bridge]
interface = "br0"

[guest]
user     = "agent"
ssh_keys = ["~/.ssh/id_ed25519.pub"]
```

### Environment Variables

| Variable | Equivalent to |
|---|---|
| `AGENT_VM_STATE_DIR` | `--state-dir` |
| `AGENT_VM_LIBVIRT_URI` | `--libvirt-uri` |
| `AGENT_VM_CONFIG` | `--config` |
| `AGENT_VM_DISTRO` | `create --distro` |
| `AGENT_VM_VCPUS` | `create --vcpus` |
| `AGENT_VM_MEMORY` | `create --memory` |
| `AGENT_VM_DISK` | `create --disk` |
| `AGENT_VM_NETWORK` | `create --network` |
| `AGENT_VM_BRIDGE` | `create --bridge` |
| `AGENT_VM_SSH_KEY` | `create --ssh-key` |

## Commands

### `agent-vm doctor`

Checks that the host can run VMs and reports each check as pass/fail with a
remedy. Exits `0` only if every required check passes.

Checks: `/dev/kvm` present and writable; libvirt connection succeeds; user is in
the `kvm` group, and in the `libvirt` group when the URI is `qemu:///system`;
state directory writable with sufficient free space; state directory reachable by
the account the hypervisor runs as; the configured NAT network is definable; the
host firewall does not drop the guest's forwarded traffic; and, when a bridge is
configured, that the bridge exists and is up. Bridged networking under
`qemu:///session` is reported as unsupported rather than attempted.

Each check reports `pass`, `warn`, `fail`, or `skip`, and only a `fail` makes
`doctor` exit non-zero. Group membership is a warning, because a host may grant
`/dev/kvm` and libvirt access another way and the checks that test those directly
are the ones that matter. Free space below 10 GiB is a warning: a cached base
image needs 2–3 GiB and thin overlays grow as guests write. A NAT network that is
not defined yet is a pass — `create` defines it on demand. `doctor` only inspects;
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

Forwarding is only half of what NAT mode needs, and the check covers both
halves. The guest also talks *to* the host — it asks the host's dnsmasq for a
DHCP lease and for every DNS answer — and that traffic is inbound rather than
forwarded, so `ufw`'s separate `deny (incoming)` default governs it. A host
carrying only the route rule looks configured and still produces guests that
never get an address, so the check reports it when an interface is allowed to
forward but is not allowed to answer DHCP (67/udp) or DNS (53); the remedy names
that interface directly, because the forward rule already established it.

The check reads `ufw`'s configuration only; it never runs `ufw` and never
changes a rule. It reports `pass` when `ufw` is absent, disabled, forwarding by
default, or has rules accepting forwarded traffic *and* the guest's DHCP and DNS
(naming the interfaces those rules cover, so you can confirm the right bridge is
among them). Only the IPv4 rules are read: the IPv6 twins `ufw` writes alongside
them never match on an IPv4-only NAT network. It reports
`warn` — never `fail` — when `ufw` is enabled and dropping, because the live
ruleset cannot be read without root and a false failure would exit non-zero on a
working host. The remedy names the NAT network to look the bridge up with, since
libvirt allocates the bridge (`virbrN`) and its name is not knowable from
configuration alone. The check is skipped for bridged mode, where guest traffic
is not routed through the host at all. Only `ufw` is understood, so a pass means
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
to `qemu:///session`, where QEMU runs as the invoking user. Because it only
inspects the filesystem it still runs under `--dry-run`.

It also verifies every tool this project delegates to, against its minimum
version:

| Tool | Minimum |
|---|---|
| libvirt (`libvirtd`/`virtqemud`, `virsh`) | 9.0 |
| QEMU (`qemu-system-*`, `qemu-img`) | 8.0 |
| `virt-install` | 4.0 |
| libguestfs (`virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`) | 1.50 |
| `podman` (or `skopeo`) | 4.0 (1.11) |
| `ip` (iproute2), `ssh` | any |

```bash
agent-vm doctor
agent-vm doctor --output json
```

### `agent-vm image build <distro>[:<tag>]`

Builds (or rebuilds) the cached base image for a distro. Each step is an existing
tool: `podman pull` the source image, `podman build` the embedded per-distro
`Containerfile` to add the guest packages a VM needs but a container does not
(kernel, `systemd`, `cloud-init`, `openssh-server`, `sudo`, `qemu-guest-agent`)
along with the tooling an agent expects to already be there (`ping`, `curl`,
`wget`, `git`, a C toolchain, Python, and Docker — see “Guest tooling” below),
`podman export` to flatten it, `virt-make-fs` to write `base.qcow2`,
`virt-ls`/`virt-copy-out` to extract `vmlinuz`/`initrd`, and `virt-sysprep` to
clear the machine ID and SSH host keys. Finishes by recording `manifest.json`,
including the tool versions used.

Use `--dry-run` to print the whole pipeline without running it.

| Flag | Default | Meaning |
|---|---|---|
| `--from <ref>` | distro's pinned default | Override the source OCI reference. |
| `--force` | off | Rebuild even if a cached image with the same source digest exists. |
| `--platform <os/arch>` | host platform | Image platform to pull. |

Building is the only operation that requires network access to a registry. It is
safe to run concurrently for different distros, and a second build of the same
one waits for the first to finish rather than racing it.

The whole build happens in a temporary directory under `images/` that is renamed
into place only on success, so a failed or interrupted build leaves no image
behind — and a rebuild keeps the previous image until the new one is complete.
The source image is pulled by tag, then pinned to the digest that was actually
fetched; everything after the pull is built on the digest, and the digest is what
`manifest.json` records.

Every base image makes the same promises to the VMs built on it, and those
promises are the guest contract: `sshd`, `systemd-networkd`, and the cloud-init
units start by themselves at first boot; cloud-init reads the NoCloud seed and
no other datasource, so a guest never contacts a metadata service on the
network; and the image carries no identity — `virt-sysprep` empties the machine
ID and removes SSH host keys, so no two VMs share either. Changing any of these
changes what every script that SSHes into these VMs can assume.

#### Guest tooling

Beyond the packages that make a container image boot as a VM, every base image
carries the tools an agent working inside the guest expects to find already
installed. They live in the base image rather than in per-VM cloud-init
packages so `create` stays fast: the download is paid once per cached image
instead of on every first boot, and a VM works the same way offline.

| Group | What is installed |
|---|---|
| Networking and diagnostics | `ping`, `traceroute`, `dig`/`nslookup`, `netcat`, `ip`, `ss` |
| Fetching and transferring | `curl`, `wget`, `rsync`, `ssh`, `ca-certificates` |
| Development | `git`, a C/C++ toolchain (`gcc`, `make`, `pkg-config`), Python 3 with `pip` |
| Shell workflow | `jq`, `zip`/`unzip`, `xz`, `tar`, `less`, `vim`, `nano`, `tmux`, `htop`, `tree`, `file`, `man` |
| Containers | Docker (`docker`, `docker compose`, `docker buildx`), started at boot |

Package names differ per family — Ubuntu takes `docker.io`, Fedora takes
`moby-engine`, Arch takes `docker` — but the commands above are present on all
three. Docker always comes from the distro's own repository, so a build needs
no third-party repository, GPG key, or installation script.

The Docker daemon is enabled, so it is running when the VM becomes reachable.
The login user is added to the `docker` group by a one-shot unit that runs after
cloud-init has created the account, so `docker` works without `sudo` — usually
by the time the first SSH session lands, and always for later ones. `sudo
docker` works regardless. This grants the login user nothing it did not already
have: that account has passwordless `sudo` by design (the guest is untrusted and
root inside it is expected — see [SECURITY.md](../SECURITY.md)).

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

### `agent-vm create <name>`

Creates and starts a VM. `<name>` must match `^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`
and must not already exist.

| Flag | Default | Meaning |
|---|---|---|
| `--distro <name>[:<tag>]` | `ubuntu` | Base image to use; built automatically if not cached. |
| `--vcpus <n>` | `2` | Virtual CPUs. |
| `--memory <size>` | `4G` | Guest RAM (`512M`, `4G`, `8G`). |
| `--disk <size>` | `50G` | Virtual root disk size (thin overlay). |
| `--network <nat\|bridge>` | `nat` | Network mode. |
| `--bridge <iface>` | config value | Host bridge to attach to; required with `--network bridge` unless configured. |
| `--ssh-key <path>` | config value | SSH **public** key(s) to authorize; repeatable. |
| `--cloud-init <path>` | none | Extra cloud-init user-data merged into the generated user-data. |
| `--virt-install-arg <arg>` | none | Extra argument passed through to `virt-install`; repeatable. The escape hatch for anything this CLI does not expose. |
| `--no-start` | off | **Not honored — rejected with exit `2`.** See below. |
| `--wait-for-ssh <duration>` | `90s` | How long to wait for the guest to accept SSH; `0` disables waiting. |

On success, prints the VM name, address, and SSH command; with `--output json`,
prints the same `vm.json` record the tool stored.

`create` is transactional. If a step through "define and start" fails, the tool
removes the domain, the overlay, the generated user-data, and the state directory
it created, and reports both the original failure and any cleanup problem.
`virsh undefine` is never given `--remove-all-storage`; the tool deletes its own
files after verifying they are inside the state directory.

The one deliberate exception is the guest-boot wait: a `--wait-for-ssh` timeout
exits `6` and **leaves the VM in place** with its `console.log`, because "it
booted slowly" and "it failed to boot" need the same evidence. Clean it up with
`agent-vm destroy <name>` once you have looked.

`--no-start` is rejected rather than approximated. `virt-install` always boots a
guest that has cloud-init data: it starts the domain with the generated NoCloud
seed attached, then defines the domain without it, so the seed exists for that
first boot only. A VM stopped before cloud-init finished would never receive its
SSH key and could not be reached afterwards. Create the VM and stop it instead:

```console
$ agent-vm create build-01 && agent-vm stop build-01
```

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
`missing`.

### `agent-vm info <name>`

Prints one VM's full record, including the base image digest it was created from,
the overlay path, the MAC address, the captured domain XML path, and the
`virt-install` version and argument vector that defined it.

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
`virsh domifaddr --source agent` (falling back to `--source lease`). This is a
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

### `agent-vm destroy <name>`

Powers off the VM, undefines the domain, and deletes its state directory,
overlay, and generated user-data. Prompts for confirmation unless `--yes` is given.

| Flag | Default | Meaning |
|---|---|---|
| `--keep-disk` | off | Keep the overlay and state directory; only remove the libvirt domain. |
| `--force` | off | Power off immediately instead of attempting graceful shutdown. |
| `--timeout <duration>` | `60s` | How long to wait for the graceful shutdown. |

`destroy` only ever touches domains and paths recorded in this state directory.
It refuses to remove a path that does not resolve inside the state directory,
and it refuses to undefine a libvirt domain it did not create — a domain whose
disk is not the overlay recorded here exits `5` and names the disks it found.

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

## Underlying Commands

The tool orchestrates standard host tools rather than reimplementing them
([ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md)), so every
operation can be reproduced by hand. `--dry-run` prints the exact invocations for
any command; the table below is the summary.

| Operation | Tools invoked |
|---|---|
| `image build` | `podman pull`, `podman image inspect` (to pin the digest), `podman build`, `podman create`, `podman export`, `podman rm`, `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| `create` | `qemu-img create`, `virsh net-list`/`net-define`/`net-start`/`net-autostart`, `ip -json link` (bridge mode), `virt-install --import --boot kernel=…,initrd=… --cloud-init user-data=…`, `virsh domifaddr`, `virsh domiflist`, `virsh dumpxml`, `ssh` (readiness probe) |
| `list` / `info` | `virsh list --all --name`, `virsh domstate`, `virsh domifaddr`, `qemu-img info -U --output=json` (`info` only) |
| `start` / `stop` / `restart` | `virsh start`, `virsh shutdown`, `virsh destroy` (for `--force`) |
| `ssh` | `virsh domstate`, `virsh domifaddr`, then `ssh` |
| `console` | `virsh domstate`, then `virsh console` |
| `destroy` | `virsh domblklist` (to confirm the domain is the one recorded here), `virsh shutdown` or `virsh destroy`, `virsh undefine` (never `--remove-all-storage`), then file removal inside the state directory |
| `doctor` | `virsh version`, plus `--version` on every required tool (`virt-install`, `qemu-img`, `podman`, `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`), `ip -V`, `ssh -V`, `virsh net-list` and — when a bridge is configured — `ip -json link` |

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
| `5` | Conflict: VM already exists, image build already in progress, wrong state for the operation. |
| `6` | Timeout: guest did not boot, become reachable, or shut down in time. |
| `7` | Cleanup incomplete: the primary operation finished but host state was left behind and needs attention. |

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
│       ├── user-data           # cloud-init user-data given to virt-install
│       ├── domain.xml          # captured `virsh dumpxml` output (a record, not an input)
│       ├── console.log         # serial console capture
│       └── vm.json             # VM record incl. virt-install version + argv, schemaVersion
├── networks/
│   └── <name>.xml              # network XML passed to `virsh net-define` (a record)
└── locks/                      # advisory file locks, one per VM and per base image
```

`networks/` and `locks/` hold the tool's own bookkeeping. Locks are released by
the kernel when the process holding one exits, so a crashed run never leaves a
lock that has to be cleared by hand.

Both `manifest.json` and `vm.json` carry a `schemaVersion`. The tool refuses to
operate on a version it does not understand and says what to rebuild instead of
guessing.
