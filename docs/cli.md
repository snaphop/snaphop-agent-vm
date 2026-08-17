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
| `--version` | — | Print the `agent-vm` version, plus the detected versions of `virt-install`, `virsh`, `libvirt`, `qemu-img`, `podman`, and libguestfs. |

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
state directory writable with sufficient free space; the configured NAT network
is definable; and, when a bridge is configured, that the bridge exists and is up.
Bridged networking under `qemu:///session` is reported as unsupported rather than
attempted.

Each check reports `pass`, `warn`, `fail`, or `skip`, and only a `fail` makes
`doctor` exit non-zero. Group membership is a warning, because a host may grant
`/dev/kvm` and libvirt access another way and the checks that test those directly
are the ones that matter. Free space below 10 GiB is a warning: a cached base
image needs 2–3 GiB and thin overlays grow as guests write. A NAT network that is
not defined yet is a pass — `create` defines it on demand. `doctor` only inspects;
it never changes host state.

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
(kernel, `systemd`, `cloud-init`, `openssh-server`, `sudo`, `qemu-guest-agent`),
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
safe to run concurrently for different distros and refuses to run twice for the
same one.

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
| `--no-start` | off | Define the domain without starting it. |
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
force-off. `restart` is `stop` followed by `start`.

Flags for `stop` and `restart`:

| Flag | Default | Meaning |
|---|---|---|
| `--timeout <duration>` | `60s` | How long to wait for a graceful shutdown. |
| `--force` | off | Power off immediately (`virsh destroy`) instead of requesting a graceful shutdown. Can lose guest writes. |

### `agent-vm ssh <name> [-- <command>...]`

Execs `ssh` to the VM as the guest user, or runs a command non-interactively and
forwards its exit status. Resolves the address with
`virsh domifaddr --source agent` (falling back to `--source lease`) and uses the
key recorded for the VM. This is a convenience wrapper around `ssh`, not an SSH
implementation — `--dry-run` prints the `ssh` command so you can use it directly.

### `agent-vm destroy <name>`

Powers off the VM, undefines the domain, and deletes its state directory,
overlay, and generated user-data. Prompts for confirmation unless `--yes` is given.

| Flag | Default | Meaning |
|---|---|---|
| `--keep-disk` | off | Keep the overlay and state directory; only remove the libvirt domain. |
| `--force` | off | Power off immediately instead of attempting graceful shutdown. |

`destroy` only ever touches domains and paths recorded in this state directory.
It refuses to remove a path that does not resolve inside the state directory,
and it refuses to undefine a libvirt domain it did not create.

### `agent-vm console <name>`

Execs `virsh console` for the VM. The console is also logged to
`vms/<name>/console.log` for post-mortem debugging of a boot failure.

## Underlying Commands

The tool orchestrates standard host tools rather than reimplementing them
([ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md)), so every
operation can be reproduced by hand. `--dry-run` prints the exact invocations for
any command; the table below is the summary.

| Operation | Tools invoked |
|---|---|
| `image build` | `podman pull`, `podman build`, `podman create`, `podman export`, `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| `create` | `qemu-img create`, `virsh net-list`/`net-define`/`net-start`, `ip -json link` (bridge mode), `virt-install --import --boot kernel=…,initrd=… --cloud-init user-data=…`, `virsh domifaddr`, `virsh dumpxml` |
| `list` / `info` | `virsh list --all`, `virsh dominfo`, `virsh domifaddr`, `virsh domblklist`, `qemu-img info --output=json` |
| `start` / `stop` / `restart` | `virsh start`, `virsh shutdown`, `virsh destroy` (for `--force`) |
| `ssh` | `virsh domifaddr`, then `ssh` |
| `console` | `virsh console` |
| `destroy` | `virsh destroy`, `virsh undefine` (never `--remove-all-storage`), then file removal inside the state directory |
| `doctor` | `virsh version`, `virt-install --version`, `qemu-img --version`, `podman --version`, `virt-make-fs --version`, `ip -json link` |

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
