# Architecture

> A current high-level map of the system. Detailed, hard-to-reverse decisions
> and their rationale belong in [`decisions/`](./decisions/); the CLI contract
> belongs in [`cli.md`](./cli.md).

## Context

This project (`snaphop-agent-vm`, shipping the `agent-vm` command) solves one
problem: an AI coding agent needs a machine it can break. Running an agent
directly on a developer's host means every command it
runs — installs, `sudo`, network calls, file deletion — happens on a machine with
real credentials and real data on it. Containers reduce the risk but share the
host kernel and are routinely given host mounts, so the isolation is thin and
easy to erode by accident.

The system creates a real virtual machine per task, from a cached base image,
fast enough that no one is tempted to skip it. Users are developers running
agents locally and supervisors orchestrating agents on a shared Linux host.

Requirements that shape the design:

- **Creation latency** matters more than throughput. `create` to usable shell
  should be seconds, not minutes; that rules out per-VM distro installers and
  makes image caching central.
- **Disposability**: destroying a VM must reclaim everything it used, and
  creating one must never mutate a shared base image.
- **Isolation by default**: no host filesystem, no host credentials, and no LAN
  exposure unless explicitly asked for.
- **Operator legibility**: everything is standard libvirt/QEMU. An operator can
  `virsh list`, `virsh dumpxml`, and `virsh console` without this tool.

Deliberately outside this repository: guest-side agent runtime and tooling
(supplied through cloud-init or a customized base image), multi-host scheduling,
authentication of remote callers, and long-lived VM fleet management. This is a
single-host tool with no daemon of its own.

## High-Level Design

```text
[ operator / agent supervisor ]
        │  CLI arguments, config, env  (untrusted input, validated)
        ▼
┌───────────────────────── agent-vm (Go, no daemon, no cgo) ─────────────────────────┐
│  internal/cli        orchestration, confirmation, output, exit codes               │
│  internal/config     defaults → file → env → flags, validation                     │
│  internal/image      base image cache: podman → virt-make-fs → virt-copy-out       │
│  internal/guestinit  cloud-init user-data + meta-data (hostname, SSH public key)   │
│  internal/domain     virt-install argv construction; virsh lifecycle + inspection  │
│  internal/network    virsh net-* for NAT | ip -json link bridge validation         │
│  internal/state      state dir, vm.json, per-VM and per-image file locks           │
│  internal/github     gh api calls for --github-ssh-key, on the host only           │
│  internal/progress   step progress: a bar on a terminal, plain lines elsewhere     │
│  internal/hostexec   the ONLY place processes spawn: argv, logging, exit status,   │
│                      and the ssh transport for a hypervisor on another machine     │
└───────────────────────────────────────┬────────────────────────────────────────────┘
                                        │ argv + machine-readable output
        ┌───────────────────────────────┼────────────────────────────┐
        ▼                               ▼                            ▼
 podman                       virt-install, virsh              qemu-img, virt-make-fs
 (pull, build, export)        (define, lifecycle, query)       virt-ls, virt-copy-out
        │                               │                       virt-sysprep, ssh
        │                               ▼                            │
        │                     libvirtd / virtqemud                   ▼
        │                               │                     $STATE_DIR/images
        │                               ▼                     $STATE_DIR/vms
        ▼                        QEMU/KVM process ──── virtio-net ────┐
 container registry                     │                             │
 (only during `image build`)       guest kernel                       ▼
                                 (untrusted guest)     NAT network (virbr) │ host bridge (LAN)
```

The tool is an **orchestrator, not a reimplementation** (ADR-0009). It owns the
workflow, the cache, the safety checks, and the rollback; every mechanical step is
performed by the standard tool that already does it. `internal/hostexec` is the
only package that spawns a process, so argv construction, logging, and exit-status
handling exist in exactly one place.

Everything below the dashed line above is on **the machine libvirt runs on**. By
default that is the machine the command was typed on, and the two are the same. A
`qemu+ssh://` libvirt URI separates them: the tools, the state directory, and the
guests are on the hypervisor, and `agent-vm` drives them from the client by
wrapping each invocation in `ssh`
([ADR-0010](./decisions/0010-drive-a-remote-hypervisor-by-running-host-tools-over-ssh.md)).
Only two invocations stay on the client — `gh`, which uses the operator's GitHub
login, and the `ssh` into a guest, which uses their keys and terminal and reaches
the guest through the hypervisor with `-J`.

Trust boundaries, from most to least trusted:

1. **The operator's shell** — supplies arguments, config, and SSH public keys.
   Trusted to intend what it asks for, but its input is still validated (names,
   sizes, paths, network modes) because a mistake here becomes a host-level
   operation.
2. **`agent-vm` and libvirt/QEMU on the host** — the control plane. It runs
   with the operator's privileges (or `qemu:///system` access) and is the only
   component allowed to touch host state. With a `qemu+ssh://` URI this spans two
   machines, and the ssh connection between them is part of the control plane: it
   terminates on the hypervisor and is never reachable from a guest. The account
   the URI names can run host tools on that machine, which is a broader grant
   than a libvirt connection alone — hence the explicit `ssh` in the URI.
3. **The guest** — untrusted. Everything coming back from it (guest agent
   responses, DHCP leases, console output, exit statuses) is untrusted input and
   is validated before use, and nothing is exposed to it beyond its own virtual
   devices.
4. **Container registries** — untrusted content, pinned by digest and recorded
   in the base image manifest so what booted is always identifiable.

### Task-To-Tool Map

Every row is a thing we deliberately do **not** implement ourselves. Minimum
versions are enforced by `agent-vm doctor`.

| Task | Tool invoked |
|---|---|
| Define and start a domain | `virt-install --import --boot kernel=…,initrd=…,kernel_args=…` |
| Domain lifecycle | `virsh start` / `shutdown` / `destroy` / `undefine` |
| Domain inspection | `virsh list --all --name`, `domstate`, `dumpxml`, `domblklist`, `domiflist` |
| Hypervisor architecture | `virsh capabilities` |
| Guest address | `virsh domifaddr --source agent`, fallback `--source lease` |
| Serial console | `virsh console` (exec'd directly, not proxied) |
| NAT network | `virsh net-list` / `net-define` / `net-start` / `net-autostart` |
| Host bridge validation | `ip -json link show type bridge` |
| OCI pull / build / flatten | `podman pull`, `podman build`, `podman create`, `podman export` |
| Root filesystem → qcow2 | `virt-make-fs --type=ext4 --format=qcow2` |
| Kernel/initrd extraction | `virt-ls`, `virt-copy-out` |
| Base image generalization | `virt-sysprep --operations machine-id,ssh-hostkeys,…` |
| Copy-on-write overlay | `qemu-img create -f qcow2 -b … -F qcow2` |
| Disk facts | `qemu-img info -U --output=json` |
| cloud-init seed | `virt-make-fs --type=vfat --label=cidata seed/ seed.img` (ADR-0011) |
| Guest shell | `ssh` (exec'd with the recorded key and address) |
| Guest update (`update`) | `ssh` running the guest family's `apt-get`, `dnf`, or `pacman` under `sudo -n`, then `mise self-update`/`mise upgrade`, `codex update`, and `rustup update` |
| GitHub SSH keys | `gh api user/keys` (`POST` on `create --github-ssh-key`, `DELETE` on `destroy --github-ssh-key`) |

Two rules keep this maintainable. **Machine-readable output only** — `--output=json`,
`--format json`, `-json`, `--xml`, and structured `virsh` subcommands, never scraping
human-formatted text; where no structured mode exists, the parser stays narrow and is
tested against output captured from the real tool. **Recorded provenance** — `vm.json`
stores the `virt-install` version and argv that defined the domain, and
`manifest.json` stores the tool versions that built the base image, so "what created
this?" is always answerable from the state directory.

All work is synchronous and foreground; there is no queue, scheduler, or
background worker. The only long waits are image builds (minutes, once per
distro) and the guest boot wait during `create` (bounded by `--wait-for-ssh`).

## Components

### `internal/cli`

- **Responsibility:** Orchestration and nothing else. Parses flags, resolves
  configuration, calls the package that owns each concern, decides what the
  operator sees on stdout and stderr, asks for confirmation before a
  destructive operation, and maps every failure to a documented exit code. It
  does not render XML, build argument vectors, or spawn processes itself.
- **Public interface:** the whole command line — subcommands, flags, defaults,
  `--output json` fields, and exit codes ([`cli.md`](./cli.md)) — plus the
  hidden `__complete` helper the generated shell completions call.
- **Failure behavior:** classification lives in one place (`exitCodeFor`), so a
  new error type gets its code by declaring what kind of failure it is rather
  than by threading a number through call sites. Nothing panics out to `main`.
- **Compatibility constraints:** flags, `--help` text, and `cli.md` move
  together; renaming a flag or changing an exit code's meaning is a contract
  change.

### `internal/config`

- **Responsibility:** Resolve and validate configuration — defaults, config
  file, `AGENT_VM_*` environment variables, flags, in that precedence order.
  Owns the documented defaults: 2 vCPU, 4 GiB RAM, 50 GiB disk, NAT networking,
  `ubuntu` distro.
- **Public interface:** the config file schema and environment variable names
  ([`cli.md`](./cli.md)).
- **Key dependencies:** none beyond the standard library and a TOML decoder.
- **Failure behavior:** fails closed before any host state changes. An invalid
  size, name, network mode, or unreadable SSH key is a usage error (exit `2`).
- **Compatibility constraints:** config keys and env var names are public; adding
  a key is fine, renaming or repurposing one is a contract change.

### `internal/image`

- **Responsibility:** Turn an OCI reference into an immutable, cached base
  artifact, by sequencing existing tools: `podman pull` (digest-pinned) →
  `podman build` of the embedded per-distro `Containerfile`, which adds what a VM
  needs and a container lacks (kernel, `systemd`, `cloud-init`,
  `openssh-server`, `sudo`, `qemu-guest-agent`) plus the tooling an agent expects
  to find already installed (networking and diagnostic tools, `curl`/`wget`,
  `git`, a C toolchain, Python, Docker, language toolchains, and the coding
  agents themselves — the full inventory is in
  [`cli.md`](./cli.md#guest-tooling)) → `podman export` to flatten →
  `virt-make-fs` to produce `base.qcow2` → `virt-ls`/`virt-copy-out` to extract
  `vmlinuz`/`initrd` → `virt-sysprep` to clear the machine ID and SSH host keys →
  `manifest.json` with the source digest, kernel version, kernel command line, and
  builder tool versions.
- **Public interface:** the on-disk image layout, the `manifest.json` schema, and
  the per-distro `Containerfile`s — which are the readable, reviewable form of all
  distro-specific knowledge in the project.
- **Key dependencies:** `podman`, `qemu-img`, libguestfs
  (`virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`), each behind an
  interface so tests substitute a fake at the process boundary. Network access to a
  registry — the only external dependency in the system.
- **Failure behavior:** builds into a temporary directory and renames into place
  only on success, so a failed or interrupted build never leaves a partially
  written base image that a VM could boot from, and the workspace is removed
  whether the failure was ours or a tool's. Nothing is retried: a failed pull or
  build surfaces the tool's own error and the operator reruns `image build`. A
  digest that cannot be read back from what was pulled is fatal, and the build
  never falls back to an unpinned reference. A rebuild keeps the previous image
  until the new one is complete.
- **Compatibility constraints:** base images produced by older releases must stay
  bootable, or `schemaVersion` rises and the rebuild path is documented.

### `internal/guestinit`

- **Responsibility:** Generate the two files the NoCloud seed carries — the
  **user-data** (hostname, guest user, authorized SSH public keys, and any
  operator-supplied user-data merged in) and the **meta-data** (instance id and
  hostname). We own the content; writing it onto a filesystem is `virt-make-fs`'s
  job, driven from `internal/domain` (ADR-0011). `virt-install` always boots the
  guest it defines, so there is no "define without starting" mode:
  `create --no-start` is rejected rather than approximated
  ([`cli.md`](./cli.md)).
- **Public interface:** the guest contract — user name, `sudo` rights, and which
  services are expected up after first boot.
- **Failure behavior:** a malformed user-data file is rejected before the VM is
  defined. User-data contents are never logged, because operator-supplied data
  may contain configuration the operator considers sensitive.
- **Compatibility constraints:** changing the guest user or its privileges breaks
  every script that SSHes into these VMs; treat it as a contract change.

### `internal/domain`

- **Responsibility:** Build the `virt-install` argument vector that defines a VM,
  and drive the rest of the lifecycle through `virsh` (`start`, `shutdown`,
  `destroy`, `undefine`) and its query subcommands (`list --all --name`,
  `dumpxml`, `domstate`, `domblklist`, `domiflist`, `domifaddr`). `console` and `ssh` are exec'd directly so
  the operator gets a real terminal, not a proxied one.
- **Public interface:** the `virt-install` argument vector — direct kernel boot via
  `--boot kernel=,initrd=,kernel_args=`, `--import --disk … bus=virtio`,
  `--network`, `--memory`, `--vcpus`, `--cpu host-passthrough` (which is what
  gives a guest the host's virtualization features, so it can run VMs of its
  own), `--graphics none`, serial console with a log file, `--rng`,
  `--memballoon virtio`, and the guest agent channel. This is what golden tests
  pin.
- **Key dependencies:** `virt-install` (4.0+), `virsh`, libvirt (9.0+), QEMU (8.0+).
  Architecture differences (machine type, firmware) are `virt-install`'s job, not
  ours — a significant reason for ADR-0009.
- **Failure behavior:** define and start are separate steps, so a rejected
  configuration fails before anything runs. Graceful shutdown is bounded by a
  timeout and then reported — never silently escalated to a force-off. Failures
  surface the tool, its argv, its exit status, its stderr, and the tail of the guest
  console log, which is where boot problems are actually visible.
- **Compatibility constraints:** the argument vector is the contract we control and
  golden-test; the resulting XML belongs to libvirt and `virt-install`. We capture
  `virsh dumpxml` output to `domain.xml` as a record and do not hand-maintain it, so
  an upstream XML change is a diff in a record rather than a merge conflict in a
  template.

### `internal/network`

- **Responsibility:** NAT mode — ensure the libvirt `agent-vm-nat` network exists and
  is active (`virsh net-list`, then `net-define` from the embedded network XML,
  `net-start`, `net-autostart`). Bridge mode — validate with `ip -json link show
  type bridge` that the named bridge exists and is up, then pass
  `--network bridge=<iface>` to `virt-install`. MAC allocation is left to
  `virt-install`/libvirt and read back from `virsh dumpxml` into `vm.json`.
- **Failure behavior:** a missing or down bridge is a host-readiness failure
  (exit `3`) with the `nmcli`/`ip` command needed to fix it, not a half-created
  VM. The tool never creates, modifies, or deletes host bridges, and never edits
  host firewall rules.
- **Compatibility constraints:** the NAT network name and subnet are visible to
  operators through `virsh net-list`; changing them orphans existing VMs.

### `internal/state`

- **Responsibility:** Own `$STATE_DIR`, the per-VM directory, `vm.json`, and the
  locks that serialize concurrent operations on the same VM or base image.
  `$STATE_DIR` is on the machine the hypervisor is on — everything in it is
  something QEMU has to open — so file operations go through an `FS`: the `os`
  package locally, and coreutils, findutils, and `flock(1)` over the ssh
  transport for a remote hypervisor (ADR-0010).
- **Public interface:** the state layout and `vm.json` schema.
- **Failure behavior:** every path is resolved and checked to be inside
  `$STATE_DIR` before any write or delete, identically on either machine. A stale
  lock from a crashed process is detectable and reported rather than silently
  broken; a remote lock is held by a process whose stdin this tool keeps open, so
  it is released the same way a local one is when that process dies. A `vm.json`
  with an unknown `schemaVersion` is refused, not guessed at.
- **Compatibility constraints:** the layout is public; scripts and operators read
  it directly.

### `internal/github`

- **Responsibility:** Add and remove SSH **public** keys on the operator's
  GitHub account through `gh api`, for `create --github-ssh-key` and
  `destroy --github-ssh-key`. `gh api` is used rather than `gh ssh-key
  add`/`delete` because it returns the key's numeric id, which is the handle
  `destroy` needs later, and takes that id back without a prompt.
- **Public interface:** the `guest.githubKey` field in `vm.json` and the key
  title `agent-vm <name> on <host>`.
- **Failure behavior:** `gh auth status` is checked before a VM is created, so
  an expired login costs nothing. A `gh` failure after the VM exists leaves the
  VM in place and names the command to rerun; a key already deleted on
  github.com is not an error.
- **Compatibility constraints:** it runs on the **host**, with the operator's
  existing login. No GitHub credential ever enters a guest, and the guest's
  private key never leaves it (SECURITY.md).

### `internal/progress`

- **Responsibility:** Render the progress of a long, multi-step operation — today
  the image build, whose steps are minutes of other programs working silently.
  Presentation only: the package doing the work reports the step it reached, and
  this one decides what an operator sees.
- **Public interface:** internal, but its output is: progress goes to stderr,
  never stdout, so `--output json` stays pipeable.
- **Failure behavior:** none of its own. It writes to a stream and reports
  nothing when there is nothing to report — a cached image, or `--quiet`.
- **Compatibility constraints:** a redrawn line is written only to a terminal.
  A captured stream (a pipe, a log, a `--verbose` run sharing stderr with slog)
  gets one plain line per step and no terminal control characters.

### `internal/hostexec`

- **Responsibility:** The single place where a process is spawned. Builds argument
  vectors (never shell strings), applies timeouts and cancellation, captures stdout
  and stderr, logs each invocation with its exit status, and detects tool presence
  and version. Every other package asks it to run something. It also owns *where*
  a command runs: each command carries a location, and a remote hypervisor's
  commands are wrapped in ssh here rather than at each call site (ADR-0010). That
  wrapping is the one place an argument vector becomes text, because ssh has no
  way to pass one through untouched, so every argument is quoted such that the
  remote shell interprets nothing.
- **Public interface:** internal only, but its behavior is visible in two public
  ways: `--dry-run` output and the logged argv.
- **Failure behavior:** a non-zero exit becomes an error carrying the tool name,
  argv, exit status, the machine it ran on, and a bounded excerpt of stderr — so a
  failure is always attributable to a specific command an operator can rerun, on
  the right host. A missing or too-old tool is a host-readiness failure (exit `3`)
  naming the tool and required version. ssh failing to connect and a tool failing
  on the far side are different errors, because they need different remedies.
- **Compatibility constraints:** version floors live here and are surfaced by
  `doctor`. Output parsers live next to the caller that needs them, prefer
  machine-readable modes, and are tested against fixtures captured from real tools.

## Request And Data Flow

`agent-vm create agent-01 --distro fedora --network nat`:

1. **Resolve and validate.** Merge defaults, config, env, flags. Validate the VM
   name against `^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`, parse sizes, read the SSH
   public key. Any failure exits `2` with nothing changed.
2. **Host readiness.** Confirm the libvirt connection (`virsh connect`/`virsh
   version`), `/dev/kvm`, and the presence and versions of the helper tools.
   Failure exits `3`.
3. **Ensure the base image.** Cache hit → use it. Cache miss → build it under an
   image lock: `podman pull` → `podman build` → `podman export` → `virt-make-fs` →
   `virt-ls`/`virt-copy-out` → `virt-sysprep` → write `manifest.json`, all in a
   temporary directory that is renamed into place only on success. A registry or
   build failure exits without creating a VM.
4. **Claim the VM.** Take the per-VM lock and create `vms/agent-01/`. An existing
   VM of that name exits `5`.
5. **Create the overlay.**
   `qemu-img create -f qcow2 -b <base.qcow2> -F qcow2 root.qcow2 50G`. The base
   file is opened read-only; nothing in this step or later mutates it.
6. **Build the cloud-init seed.** Write `seed/user-data` with hostname, guest
   user and authorized keys, and `seed/meta-data` with the instance id, then
   `virt-make-fs --type=vfat --label=cidata seed/ seed.img`. The seed is attached
   as a read-only virtio disk, which is the only kind a guest sees before
   cloud-init chooses a datasource (ADR-0011).
7. **Ensure networking.** `virsh net-list`/`net-define`/`net-start` for the NAT
   network, or `ip -json link` validation of the bridge.
8. **Define and start.** Read the hypervisor's architecture with `virsh
   capabilities` — it decides the arguments that cannot be the same everywhere,
   currently `--features acpi=off` on aarch64, where libvirt refuses ACPI
   without UEFI and a directly booted kernel has none. Then one `virt-install
   --import --boot kernel=…,initrd=…` run defines and starts the domain, with
   the serial console logged to `console.log`. The exact argv is logged and
   recorded.
9. **Record.** Read the MAC with `virsh domiflist`, capture `virsh dumpxml` to
   `domain.xml`, and write `vm.json` (including the `virt-install` version and
   argv). Recording happens *before* the wait, so that a VM left in place by a
   boot timeout is still one `agent-vm destroy` knows how to remove.
10. **Wait for the guest.** Poll `virsh domifaddr --source agent` (falling back to
    `--source lease`) for an address, then wait for SSH, bounded by
    `--wait-for-ssh`. A timeout exits `6` — and by default leaves the VM in place
    with the console log, because "it booted slowly" and "it failed to boot" need
    the same evidence. Then print the result.

Rollback: steps 4–9 are undone in reverse on failure — `virsh destroy`, `virsh
undefine`, then delete the overlay, the seed, and the state directory. Step 10 is the
deliberate exception noted above: a boot-wait timeout preserves the VM and its
console log rather than destroying the evidence. `undefine` is
never given `--remove-all-storage`; the tool deletes its own files after the
containment check, so libvirt is never asked to remove storage it might interpret
differently than we do. If a cleanup step itself fails, the tool reports exactly
what remains on the host and exits `7`. It never reports success with host state
left behind, and never deletes anything outside `$STATE_DIR`.

`--dry-run` prints the full sequence of tool invocations for any operation without
running them, which doubles as the documentation of what the tool does and lets an
operator perform the same work by hand.

`destroy` is the same path in reverse, and only ever acts on domains and paths
recorded in `vm.json`.

## Public Contracts

- [`cli.md`](./cli.md) — subcommands, flags, defaults, JSON output, exit codes.
- Configuration file keys and `AGENT_VM_*` environment variables.
- `$STATE_DIR` layout, `vm.json`, and `manifest.json` (both versioned by
  `schemaVersion`).
- The `virt-install` argument vector and cloud-init user-data we generate, pinned by
  golden files in `test/golden/`. The resulting domain XML is libvirt's and
  `virt-install`'s output, captured as a record in `domain.xml`; we do not own its
  exact content, but a change in *our argv* that changes the guest's device topology
  is a contract change.
- The guest contract: guest user name, `sudo` rights, and the services expected
  running after first boot.

What must change together: implementation, `--help` text, `cli.md`, golden files,
and — when a stored schema changes — the `schemaVersion` constant plus a
documented upgrade or rebuild path. Versioning follows SemVer on the CLI; while
the project is `0.x`, breaking changes are allowed but must be called out in
`CHANGELOG.md`.

## Data Model And State

Entities:

- **Base image** — identity is `(distro, tag)`; provenance is the source OCI
  digest. Immutable once built. System of record: `images/<distro>/<tag>/`.
- **VM** — identity is its name, unique within a state directory. System of
  record for configuration and provenance: `vms/<name>/vm.json`. System of record
  for *runtime* state: libvirt. The tool reconciles the two rather than caching
  domain state, and reports a VM as `missing` when state has a record libvirt
  does not.
- **Overlay disk** — owned by exactly one VM, backed by exactly one base image.

Consistency: single-writer per VM and per base image, enforced by file locks.
Operations are idempotent or rolled back; there are no partial commits by design.
Deleting a base image while an overlay depends on it is refused, because a
backing file is not optional.

Sensitive data: none is stored by design. `vm.json` records the *path* of the
authorized public key, not private material. Operator-supplied cloud-init data
lives only in the per-VM seed and is deleted with the VM. Guest disk contents are
whatever the agent wrote and are destroyed with the VM; nothing is retained after
`destroy`.

## Runtime Profiles And Configuration

One profile, parameterized:

| Dimension | Options | Notes |
|---|---|---|
| libvirt URI | `qemu:///system` (default), `qemu:///session`, `qemu+ssh://[user@]host[:port]/{system,session}` | Session mode is unprivileged and NAT-only; bridged mode needs system mode. An `ssh` URI puts the tools, the state directory, and the guests on that host (ADR-0010); other remote transports are refused because they give no shell there. |
| Network mode | `nat` (default), `bridge` | Bridge requires a pre-existing host bridge. |
| State directory | user-local default, or a shared path | A shared path implies shared locks and shared images across users. With a remote URI it is a path on the hypervisor, defaulting to that account's home. |
| Guest distro | `ubuntu`, `fedora`, `arch` | Pinned by digest per base image. |

There are no feature flags, no build-time profiles, and no staging/production
distinction — the tool runs on whatever host invokes it. Secrets are not part of
configuration; the only key material referenced is an SSH public key path.

## External Dependencies

| Dependency | Minimum | Purpose | Failure Mode | Recovery/Owner |
|---|---|---|---|---|
| libvirt (`libvirtd`/`virtqemud`) | 9.0 | Domain and network management | Nothing works; every command fails at readiness check | Exit `3` with the service and group fix; host operator |
| QEMU/KVM (`/dev/kvm`) | 8.0 | Guest execution | VMs cannot start, or would fall back to unusably slow emulation (refused) | Exit `3`; host operator |
| `virt-install` (`virtinst`) | 4.0 | Define and start domains | `create` fails before a domain exists | Exit `3` or the tool's own error with argv; upstream virt-manager |
| `virsh` | 9.0 | Lifecycle, inspection, addresses, NAT network | Lifecycle and query commands fail | Exit `3`; shipped with libvirt |
| `qemu-img` | 8.0 | Overlay creation, disk facts | `create` fails before defining a domain | Retry after fixing the host; upstream QEMU |
| `podman` | 4.0 | Pull, build, flatten OCI images | `image build` fails; cached images still work offline | Rerun `image build` once the cause is fixed; upstream |
| libguestfs (`virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`) | 1.50 | Unprivileged rootfs → qcow2, kernel extraction, generalization, cloud-init seed | `image build` fails; appliance problems are the usual cause | Exit `3` with the libguestfs diagnostic; upstream |
| `iproute2` (`ip -json`) | any | Host bridge validation | Bridged `create` fails readiness | Exit `3` with the bridge to fix; host operator |
| `ssh` | any | `agent-vm ssh`, `agent-vm update` | Only those subcommands fail | Host operator |
| `gh` | 2.0 | Optional: add/remove a VM's SSH key on GitHub (`--github-ssh-key`) | Only that flag fails; every other command is unaffected | Exit `3` naming `gh`; host operator |
| Container registries | — | Source images | `image build` fails; unaffected once cached | Retry; pin digests to avoid surprise drift |
| `cloud-init` in the guest | — | First-boot configuration | VM boots but has no user or SSH key; surfaces as a `--wait-for-ssh` timeout | Console log shows cloud-init output; fix the base image |

Every one of these is a tool the host already has or installs from its own
repositories. That is the point of ADR-0009: the dependency list is long, but every
entry replaces code we would otherwise have to write and maintain.

## Deployment And Release

The build artifact is a single static Go binary per OS/architecture; there is no
container image, no server, and nothing to deploy. Installation is copying the
binary onto a KVM-capable host.

- Pull requests run `scripts/check.sh` — format, vet, lint, and unit tests.
  Integration tests run only on a KVM-capable runner and are not required for
  merge. This is the intended contract: the CI workflows are not written yet, so
  today the script is run locally.
- **Merging does not publish or deploy anything.** Tagged releases build and
  attach binaries; that workflow is the only publishing path.
- Rollback is running the previous binary. Because base images are versioned by
  `schemaVersion` and VMs record the image digest they came from, an older binary
  either understands existing state or refuses it explicitly.
- There is no database and no migration ordering. Stored-schema changes are
  handled by `schemaVersion` checks with a documented rebuild path.

## Observability

- Structured logs (`log/slog`) to stderr; `--verbose` for debug level. Every tool
  invocation is logged with its argument vector, duration, and exit status — the
  single most useful signal in the system, because nearly every failure is really a
  failure of `virt-install`, `podman`, `qemu-img`, or libguestfs, and the logged
  argv is a command the operator can rerun by hand.
- `--dry-run` prints the same invocations without executing them.
- Tool versions are resolved once per run, reported by `doctor`, and recorded in
  `vm.json`/`manifest.json` — so a "worked last month" regression can be traced to a
  host tool upgrade.
- Redaction: cloud-init user-data contents and the contents of any file passed by
  the operator are never logged; only paths and sizes are. Argv is logged, so no
  secret is ever passed as a command-line argument.
- The guest serial console is captured to `vms/<name>/console.log`, which is the
  primary artifact for diagnosing a VM that never became reachable.
- `agent-vm doctor` is the health check, machine-readable with
  `--output json`.
- There are no metrics, traces, or alerts: this is a foreground CLI, and its
  exit code is its status signal. Operational procedures live in
  [`host-setup.md`](./host-setup.md).

## Security Boundaries

The full, binding rules are in [`../SECURITY.md`](../SECURITY.md). In summary:

- The **guest is untrusted**. By default it gets no host filesystem, no host
  credentials, no host-only service access, and no LAN exposure. Bridged
  networking is opt-in per VM and documented as a trust-boundary change. Host
  path sharing is not implemented; adding it would require an ADR and the
  per-VM, explicit, default-read-only handling `SECURITY.md` mandates.
- Credentials enter in exactly one place: an SSH **public** key path, injected
  into the cloud-init seed. No private key ever enters an image, a seed, or this
  repository.
- Authorization decisions belong to the host: libvirt/QEMU enforce guest
  isolation, and the operator's own privileges determine what the tool may do.
  The tool adds its own guard rails on destructive operations — name validation,
  path containment inside `$STATE_DIR`, and refusal to act on libvirt domains it
  did not create.
- Data that must never cross into a guest: host SSH private keys, cloud
  credentials, the operator's home directory, and the state directory itself.

## Constraints And Risks

- **Single host, no scheduling.** Nothing prevents oversubscribing CPU, memory,
  or disk beyond what the host can serve; capacity is the operator's problem.
- **Thin overlays hide real usage.** A 50 GiB VM starts at a few megabytes but
  can grow to 50 GiB. Ten VMs can overcommit a disk that looked fine at creation.
- **Base image builds need root-ish capability somewhere.** libguestfs avoids
  requiring root directly, but its appliance is the most fragile dependency in
  the system and the most common source of confusing build failures.
- **Direct kernel boot means the host holds the kernel.** A guest that updates
  its own kernel package will not boot the new kernel until the base image is
  rebuilt. This is a deliberate trade for boot speed
  ([ADR-0004](./decisions/0004-direct-kernel-boot-with-copy-on-write-overlays.md)),
  and a documented surprise for users who expect a normal VM.
- **Arch Linux is a rolling target.** A rebuilt Arch base image is not the same
  image as last week's; reproducibility comes from the recorded digest, not from
  the tag.
- **Bridged mode removes a boundary.** A VM on the LAN is reachable by anything
  on the LAN, including other people's machines. It exists because some workflows
  need it, not because it is safe by default.
- **Integration coverage is optional and host-dependent**, so real boot
  regressions can reach `master` if no one runs the KVM suite. Unit and
  golden-file coverage exist specifically to narrow that gap.
- **Host tool drift is the cost of ADR-0009.** A `virt-install` flag rename, a
  changed `virsh` output format, or a distro shipping a version below our floor
  breaks us through no change of our own. Mitigations: version floors checked by
  `doctor`, machine-readable output modes wherever they exist, narrow parsers backed
  by fixtures captured from real tools, and an integration suite that exercises the
  actual commands. The residual risk is real and accepted — it is smaller than
  maintaining our own domain XML generator.
- **Long dependency list.** More things must be installed and correct on a host than
  a self-contained implementation would need, which is why `doctor` is a first-class
  command and exit `3` is a documented outcome rather than a generic failure.
