# AGENTS.md

> **Canonical instruction file for AI coding agents working in this repository.**
> Tool-specific files (`CLAUDE.md`, `GEMINI.md`, `.cursor/rules/`,
> `.windsurf/rules/`, `.github/copilot-instructions.md`) defer here. If
> anything conflicts, this file wins.

---

## 1. Project Overview

- **Name:** `snaphop-agent-vm`
- **What it does:** Creates and manages short-lived QEMU/KVM virtual machines on
  a Linux host — via libvirt — so AI coding agents get a disposable
  machine with root access and NAT networking by default instead of
  running on the host. NAT alone does not isolate host services or the LAN.
- **Primary language / runtime:** Go 1.22+ (single static binary, `agent-vm`).
- **Key dependencies / frameworks:** existing host CLI tools, orchestrated rather
  than reimplemented (ADR-0009) — `virt-install` (defines domains), `virsh`
  (lifecycle, inspection, NAT network, addresses, console), QEMU/KVM,
  `qemu-img` (copy-on-write overlays), `podman` (OCI
  pull, build, flatten), libguestfs (`virt-make-fs`, which writes both the base
  image and the cloud-init seed, plus `virt-ls`, `virt-copy-out`,
  `virt-sysprep`), `ip` (bridge validation), and `ssh`. The Go code is pure Go —
  no cgo, no libvirt bindings.
- **Deployment target:** A single Linux host with hardware virtualization
  (`/dev/kvm`) and a running `libvirtd`/`virtqemud`. Distributed as a binary and
  invoked by humans or agent supervisors; there is no server component and no
  hosted service.
- **Canonical product/API reference:** [`docs/cli.md`](./docs/cli.md) for the
  command-line contract, [`docs/architecture.md`](./docs/architecture.md) for
  the system map, and [`docs/decisions/`](./docs/decisions/) for the decisions
  that produced both.

**Status: implementation in progress.** The documentation in this repository
describes the full intended design and public contract; the code implements part
of it. Landed so far: `internal/hostexec`, `internal/config`, `internal/state`,
`internal/network`, `internal/image` (including the per-distro
`Containerfile`s), `internal/guestinit`, `internal/domain`, `internal/github`,
`internal/progress`, and the `doctor`, `image`, `create`, `list`, `info`,
`start`, `stop`, `restart`, `ssh`, `update`, `console`, `destroy`, `completion`,
`--version`, and `--dry-run` surfaces in `internal/cli` — every command in the
documented contract. What remains is hardening: the integration
suite in `test/integration/` now covers the image build **and** the VM
lifecycle — create, boot, SSH, stop/start/restart, destroy, rollback after a
failed create, the refusal to destroy a domain this tool did not create, and
growable memory (a VM created with `--max-memory` is booted, grown with `virsh
update-memory-device`, and checked from inside the guest) —
and the whole suite has been run to completion against a real KVM host
(libvirt 12.6.0, QEMU 11.1.0, `virt-install` 5.1.0), booting all three
supported distros. The lifecycle suite now takes a
`-lifecycle-network`/`-lifecycle-bridge` pair, and the full lifecycle has been
run for all three distros in **both** network modes — six combinations, NAT and
bridged against a real host bridge — so neither mode is unexercised any more.
Treat
`docs/cli.md` and `docs/architecture.md` as the specification to satisfy, and
update them in the same change if the implementation must diverge.

Users are developers and agent supervisors who need a throwaway machine per
task: a VM is created in seconds from a cached base image, the agent works
inside it as the `agent` user with passwordless `sudo` by default, and the VM
is destroyed afterwards. The three invariants that
matter most are **the guest is untrusted** (an agent inside it may run arbitrary
code, so nothing the host cares about may be reachable by default), **base
images are immutable and their source digest is recorded** (a VM's root disk
is a copy-on-write overlay on a cached base, so creation reuses the same
artifact; the cache is keyed by distro and tag, and unpinned package and tool
versions mean rebuilding is not bit-for-bit reproducible), and **we orchestrate
existing tools rather than reimplementing them** (ADR-0009 — if `virt-install`, `virsh`, `podman`, `qemu-img`, or libguestfs
already does something, we call it; writing our own version of it requires an ADR
saying why the tool could not be used).

Compatibility constraints agents could otherwise discover only by breaking
them: the CLI surface, the on-disk state layout under the state directory, the
`vm.json`/`manifest.json` files, and the generated libvirt domain XML are public
contracts (see §8). Base images built by an older version must remain bootable
by a newer one, or the manifest schema version must be raised and a rebuild
path documented.

## 2. Repository Layout

```text
.
├── cmd/agent-vm/           # CLI entry point (signals and exit status only)
├── internal/
│   ├── cli/                # subcommand implementations
│   ├── config/             # config file, env vars, defaults, validation
│   ├── image/              # base image cache: podman + libguestfs pipeline
│   ├── domain/             # virt-install argv, virsh lifecycle and queries
│   ├── network/            # virsh net-* for NAT, ip -json bridge validation
│   ├── guestinit/          # cloud-init user-data and meta-data generation
│   ├── state/              # state directory, vm.json, locking
│   ├── github/             # gh api calls for --github-ssh-key, host-side only
│   ├── progress/           # terminal progress rendering for long operations
│   ├── golden/             # golden-file comparison helper, used only by tests
│   └── hostexec/           # the only place processes spawn: argv, logs, versions
├── templates/              # embedded: per-distro Containerfiles, cloud-init
│                           # user-data and meta-data, NAT network XML
├── test/
│   ├── golden/             # golden tool argv and cloud-init user-data fixtures
│   ├── toolout/            # output captured from real tools, for parser tests
│   └── integration/        # KVM-requiring tests (build tag `integration`)
├── scripts/                # repeatable development and operational helpers
├── docs/                   # architecture, CLI contract, runbooks, ADRs
├── Makefile                # thin wrapper around scripts/, plus `make install`
├── .github/                # contribution metadata (pull request template). The
│                           # CI workflows described in §5 are not written yet.
├── AGENTS.md               # canonical agent instructions
├── CODE_REVIEW.md          # code-review process
├── CONTRIBUTING.md         # contribution workflow
├── SECURITY.md             # reporting policy and hard security boundaries
└── CHANGELOG.md            # human-readable history of notable changes
```

Where new code belongs:

- A new subcommand: `internal/cli/<verb>.go`, plus `internal/cli/<verb>_test.go`.
  `cmd/agent-vm` stays a thin shell — no business logic there.
- Distro-specific behavior (package names, kernel path, init flavor): the
  per-distro `Containerfile` under `templates/distro/`, plus a small distro
  definition under `internal/image/distro/` for anything a `Containerfile` cannot
  express. Do not scatter `switch distro` blocks across packages. Each family
  has three recipes — `<family>.Containerfile`, `<family>-slim.Containerfile`
  and `<family>-nix.Containerfile`, the latter two built as the separate base
  images `<family>-slim` and `<family>-nix` — and they share the boot,
  cloud-init, clock, and networking blocks verbatim: a change to one of those
  blocks belongs in all three files in the same edit, which
  `TestSlimContainerfiles_KeepTheBootAndCloudInitContract` and
  `TestNixContainerfiles_KeepTheBootAndCloudInitContract` enforce. The nix
  recipes take their guest tooling from the shared
  `templates/distro/agent-tools.nix` instead of the family's package manager
  (ADR-0012).
- A new tool invocation: the package that owns the concern (`internal/image`,
  `internal/domain`, `internal/network`), always executed through
  `internal/hostexec` and behind an interface so tests can substitute a fake at the
  process boundary. Nothing else may call `os/exec`.
- A parser for a tool's output: next to its caller, using the tool's
  machine-readable mode, with a fixture in `test/toolout/` captured from the real
  tool.

Unit tests live beside the code they cover. Tests that need `/dev/kvm`, a
running libvirt, or network access to a registry live in `test/integration/` and
are guarded by the `integration` build tag.

Compatibility-sensitive areas that must not drift: the `virt-install` argument
vector and cloud-init user-data we generate (public contract, pinned by golden
files), `internal/state` (on-disk layout and schema versions), and `docs/cli.md`
(must match the actual flags). Golden files in `test/golden/` are generated —
regenerate them with the documented command rather than hand-editing. Fixtures in
`test/toolout/` are captures of real tool output; refresh them by re-running the
tool and noting its version, never by editing them to make a parser pass.

## 3. Setup

Prerequisites:

- Go 1.22 or newer
- A Linux host with KVM: `/dev/kvm` present, and your user in the `kvm` and
  `libvirt` groups
- libvirt 9.0+ (`libvirtd` or `virtqemud`, plus `virsh`) and QEMU 8.0+
- `virt-install` 4.0+ (`virtinst` on Debian/Ubuntu)
- `qemu-img`, `podman` 4.0+, and libguestfs 1.50+
  (`virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep`)
- `gh` 2.0+ — optional, and needed only by `--github-ssh-key` on `create` and
  `destroy`
- `golangci-lint` for linting

No libvirt development headers are needed — the build is pure Go and talks to
libvirt through `virt-install` and `virsh`.

```bash
git clone https://github.com/snaphop/snaphop-agent-vm.git
cd snaphop-agent-vm
go mod download
cp .env.example .env          # optional; source it into your shell to override
                              # defaults. The binary reads the environment, not
                              # this file; config.toml is the persistent option.
go build ./...
go run ./cmd/agent-vm doctor
```

`doctor` is the setup verification command: it checks KVM availability, the
libvirt connection, group membership, every required tool **and its minimum
version**, free space in the state directory, and — for bridged mode — the presence
of the configured host bridge. It must exit non-zero (`3`) with an actionable
message naming the tool and version when the host is not ready. Because the design
delegates to host tools, this check is load-bearing, not a nicety.

`--dry-run` prints planned tool invocations and skips mutations. Read-only
queries may still run; build and create plans use placeholders for values that
only exist after execution. Use it to review a change without creating a VM.

Host preparation beyond package installation (bridge creation, libvirt NAT
network definition, storage location, unprivileged access) is documented in
[`docs/host-setup.md`](./docs/host-setup.md). No credentials are required for
development. Image builds use public registries, package mirrors, and tool
download services; offline VM creation is possible once a base image is cached.

## 4. Common Commands

Prefer repository scripts over ad hoc commands so local work and CI stay
aligned. `scripts/check.sh` runs the full pre-handoff verification and
`scripts/build-release.sh` builds the release binaries; where a script does not
exist yet, create it rather than substituting an ad hoc invocation for it. The
`Makefile` is a convenience wrapper around those scripts — `make check`, `make
release`, `make build`, `make test`, `make clean` — and adds the two steps with no
script of their own: `make install`, which installs this host's binary into
`$(BINDIR)` (default `~/.local/bin`), and `make uninstall`, which removes it.
The scripts stay the source of truth.

| Task | Command |
|---|---|
| Install dependencies | `go mod download` |
| Build | `go build ./...` |
| Build the binary | `go build -o bin/agent-vm ./cmd/agent-vm` |
| Run locally | `go run ./cmd/agent-vm <subcommand>` |
| Run all tests | `go test ./...` |
| Run one test | `go test ./internal/domain -run TestVirtInstallArgs_NAT` |
| Lint | `golangci-lint run` |
| Format | `gofmt -w .` (`gofmt -l .` must be empty) |
| Typecheck | `go vet ./...` (the compiler is the type checker) |
| Package/container build | `scripts/build-release.sh` or `make release` (static binary per arch) |
| Install on this host | `make install` (into `~/.local/bin` by default) |
| Regenerate golden files | `go test ./... -update-golden` |
| Live/integration test | `go test -tags integration ./test/integration/...` |

**Minimum verification before handoff:** `scripts/check.sh`, which runs
`gofmt -l .` (must be empty), `go vet ./...`, `golangci-lint run`, and
`go test ./...`. It warns loudly instead of passing silently when
`golangci-lint` is not installed. Several doctor unit tests currently inspect
the real `/dev/kvm` and can fail without it; see `CONTRIBUTING.md`.

Additional checks required for specific changes:

- Tool invocations (`virt-install` argv, `virsh` usage), generated cloud-init
  user-data, state layout, `vm.json`/`manifest.json`, CLI flags, config keys, or
  environment variables: regenerate golden files, update `docs/cli.md` (including
  its **Underlying Commands** table), and run the integration suite.
- A new tool, a new flag on an existing tool, or a raised minimum version: update
  `doctor`, the dependency tables in `AGENTS.md` §3 and `docs/architecture.md`,
  `docs/host-setup.md` package lists, and `docs/cli.md`.
- Image build pipeline, `Containerfile`s, or distro definitions: run
  `go test -tags integration ./test/integration/...` for every supported distro
  and record which distro/tag combinations you actually booted.
- Networking changes: exercise both NAT and bridged modes, or state explicitly
  which one you could not verify and why.
- Anything touching where commands run — `internal/hostexec` locations, the ssh
  transport, `internal/state`'s `FS`, or the libvirt URI: verify against both a
  local and a `qemu+ssh://` connection, or say which you could not.

**The integration suite creates and destroys real VMs and disk images on its
target host.** VM names use the `agent-vm-test-` prefix and state belongs in a
dedicated test directory. NAT lifecycle tests use the shared `agent-vm-nat`
network, creating it if absent and leaving it in place after the run. Never point
it at a state directory holding VMs someone cares about, and never run it against a production libvirt host without
explicit approval.

## 5. Architecture And Runtime Notes

Major modules and responsibilities:

- `internal/config` — resolves configuration from defaults, the config file,
  environment variables, and flags (in that precedence order) and validates the
  result. Defaults: **2 vCPU, 4 GiB RAM, 50 GiB disk, NAT networking.**
- `internal/image` — turns an OCI container image reference into a cached,
  immutable base artifact by sequencing `podman pull`/`build`/`export`,
  `virt-make-fs`, `virt-ls`/`virt-copy-out`, and `virt-sysprep`, then writing a
  `manifest.json` recording the source digest, kernel version, kernel command line,
  and builder tool versions.
- `internal/guestinit` — generates the two files the NoCloud seed carries: the
  **user-data** (hostname, SSH public key, agent user, optional user-supplied
  user-data) and the **meta-data** (instance id and hostname). We own their
  content; writing them onto a `cidata`-labelled filesystem is `virt-make-fs`'s
  job, driven from `internal/domain` (ADR-0011).
- `internal/domain` — builds the `virt-install` argument vector and drives
  `virsh` for lifecycle and inspection.
- `internal/network` — ensures the NAT network exists via `virsh net-*`, or
  validates an existing host bridge with `ip -json link`.
- `internal/state` — owns the state directory, per-VM `vm.json`, and the file
  locks that keep concurrent `create`/`destroy` calls from racing. The state
  directory is on the machine the hypervisor is on, so its file operations go
  through an `FS`: the `os` package locally, and coreutils plus `flock(1)` over
  the ssh transport for a remote hypervisor.
- `internal/github` — adds and removes SSH **public** keys on the operator's
  GitHub account through `gh api`, for `--github-ssh-key` on `create` and
  `destroy`. It runs on the host with the operator's existing login; no GitHub
  credential ever enters a guest.
- `internal/progress` — renders the progress of a long, multi-step operation: a
  bar redrawn in place on a terminal, one plain line per step anywhere else.
  Presentation only; the package doing the work reports which step it reached.
- `internal/hostexec` — the only package that spawns processes: argv construction,
  timeouts, logging with exit status, tool version detection, and *where* a
  command runs. Each command carries a location; with a `qemu+ssh://` libvirt URI
  the hypervisor-located ones — everything touching a disk, an image, or a domain
  — are wrapped in `ssh` here rather than at each call site (ADR-0010). Only `gh`
  and the `ssh` into a guest stay on the client.

Flow for `agent-vm create`: resolve config and validate input files → check
required tool versions (not the full `doctor` report) → lock and check the VM
name → ensure base image (build if the cache misses) → allocate the VM's state
directory → generate cloud-init user-data and meta-data and build the NoCloud
seed with `virt-make-fs` → `qemu-img create` the overlay with the base as backing
file → ensure the network → one `virt-install --import --boot
kernel=…` run to define and start the domain, with the seed attached as a
read-only virtio disk (ADR-0011) → capture `virsh dumpxml` and write `vm.json`
→ poll `virsh domifaddr` and wait for SSH (unless waiting is disabled)
→ optionally register the guest public key with GitHub. Failures through recording
roll back the domain and per-VM directory; a boot-wait or GitHub registration
failure retains the recorded VM for inspection. Built base images and the shared
NAT network remain reusable. Cleanup failures must report what remains.

Runtime profiles and configuration: a single profile, parameterized by the
libvirt URI (`qemu:///system` by default, `qemu:///session` accepted with
NAT mode but requiring a managed network the connection can use, and `qemu+ssh://[user@]host/system` to drive a
hypervisor on another machine — other remote transports are refused, because
they reach libvirt but give no shell to build images and disks with), the state
directory, and the network mode. There is
no database or queue; the base image cache and VM records live on the
hypervisor. Image builds contact container registries, distro package mirrors,
and, for full images, tool and vendor download services. `--github-ssh-key`
contacts GitHub from the client; `update` downloads packages and tools from
inside guests.

Canonical contracts and what must change together: `docs/cli.md` (flags,
subcommands, exit codes, underlying commands), `test/golden/` (`virt-install` argv
and generated user-data), `internal/state` schemas plus their `schemaVersion`
constants, and `docs/host-setup.md` when host prerequisites change. A change to any
one of these usually requires the others in the same commit.

Compatibility constraints: libvirt 9.0+, QEMU 8.0+, and `virt-install` 4.0+ are the
floor; x86_64 and aarch64 hosts are supported, and `virt-install` selects
architecture-specific machine and device defaults;
we explicitly disable ACPI for direct kernel boot on aarch64; base
images built by an earlier release must stay bootable, and a breaking manifest
change requires a `schemaVersion` bump plus a documented rebuild path.

CI and release behavior — the intended contract; the workflows themselves are
not written yet (§2). Pull requests run `scripts/check.sh`: format, vet, lint,
and unit tests. Integration tests run only on a KVM-capable runner and are not
required for merge. **Merging does not deploy or publish anything.** Tagged
releases build and attach static binaries; that workflow is the only publishing
path.

Require an ADR in [`docs/decisions/`](./docs/decisions/) for decisions that are
hard to reverse, affect multiple components, or change the security/deployment
boundary — specifically the virtualization stack, the boot method, the image
cache format, guest-to-host sharing, network modes, where host tools run
(ADR-0010), the default resource profile,
adding a supported distro family, adding a base image variant or changing where
guest tooling comes from (ADR-0012), or **implementing something a standard host
tool already does** (ADR-0009). ADR-0001 carries the same list.

Document observable or operational effects in `CHANGELOG.md` under
`[Unreleased]` in the same change, using Keep a Changelog categories and
language a non-developer can follow. Pure refactors, test-only changes, and
formatting-only changes may be omitted.

## 6. Code Style

- **Reach for the existing tool first.** Before writing code that generates domain
  XML, builds an ISO, extracts container layers, partitions a disk, or parses
  human-readable output, check whether `virt-install`, `virsh`, `podman`,
  `qemu-img`, `cloud-localds`, or a libguestfs tool already does it — and check for
  a machine-readable output flag while you are there. Reimplementation needs an ADR.
- All process execution goes through `internal/hostexec` with an explicit argument
  vector. No `os/exec` elsewhere, no shell strings, no `sh -c`.
- Match the surrounding style of every file you edit. Do not reformat unrelated
  code.
- Standard Go idiom: `gofmt`, wrapped errors with `%w`, `context.Context` as the
  first parameter for anything that can block, no panics outside `main`
  initialization.
- Errors reaching the user must say what failed, what the host state was, and
  what to do next. Because nearly every failure is another program's failure, an
  error must carry the tool name, its argv, its exit status, and a bounded excerpt
  of its stderr. "Operation failed" is not acceptable output from a tool that drives
  another program.
- Keep functions and modules focused. Preserve established boundaries instead of
  creating convenience dependencies across them — `internal/cli` orchestrates,
  it does not render XML or shell out.
- Prefer descriptive names over abbreviations or cleverness.
- Comments explain non-obvious decisions and constraints — why a kernel
  command-line argument is required, why an operation is retried — not what the
  code already says.
- Do not leave dead code, commented-out replacements, or debugging output.
- Handle errors at the correct boundary; do not silently swallow failures. A
  failed cleanup step is reported, never hidden.
- Keep public interfaces and user-facing behavior documented: a flag change
  updates `docs/cli.md` and the command's help text together.
- Do not hand-edit generated files (`test/golden/`, any generated mocks). Update
  the source and regenerate.

## 7. Testing

- Add or update tests for every behavior change. A bug fix requires a regression
  test unless the behavior cannot be exercised automatically; if so, explain the
  gap in the pull request.
- Name tests after behavior: `TestCreate_RejectsInvalidVMName`, not
  `TestCreate3`.
- Prefer realistic values — real distro names, real sizes, real domain XML — and
  test through public boundaries (`internal/cli` entry points, not private
  helpers).
- Mock external systems at the process boundary: `virt-install`, `virsh`,
  `qemu-img`, `podman`, and the libguestfs tools each sit behind an
  interface that tests can substitute. Do not mock inside your own packages.
- Output parsers are tested against fixtures in `test/toolout/` captured from real
  tools, with the producing tool's version recorded alongside. Hand-written fake
  output that no tool ever emitted is not coverage.
- Cover success, expected failure, validation/authorization, and edge cases in
  proportion to risk. Required coverage for this project: VM name validation,
  path containment inside the state directory, resource-limit parsing, network
  mode selection, rollback after a failed `create`, and refusal to destroy
  anything not recorded in state.
- Tool argument vectors (notably `virt-install`) and generated cloud-init user-data
  are covered by golden-file tests. A diff in a golden file is a contract change and
  must be justified in the pull request.
- Do not disable, skip, or weaken tests to make a change pass.
- Do not rely on the optional integration suite as the only regression coverage
  for a bug — reproduce it in a unit test wherever the failure can be modeled at
  a process boundary.
- Tests requiring `/dev/kvm`, libvirt, or registry access are documented as such
  and carry the `integration` build tag.
- When asked to review code, follow [`CODE_REVIEW.md`](./CODE_REVIEW.md).

## 8. Public Contracts And Data

Public — consumed outside this repository, by humans, scripts, and agent
supervisors:

- The `agent-vm` command line: subcommands, flags, defaults, stdout format
  (including `--output json`), and exit codes. Specified in
  [`docs/cli.md`](./docs/cli.md).
- Configuration: `~/.config/agent-vm/config.toml` keys and the
  `AGENT_VM_*` environment variables.
- On-disk state layout under the state directory, and the `vm.json` and
  `manifest.json` schemas.
- The `virt-install` argument vector and cloud-init user-data the tool generates.
  The resulting domain XML belongs to libvirt and `virt-install`; we capture it to
  `domain.xml` as a record, and operators may still `virsh edit` a defined domain.
- The set of host tools required and their minimum versions — raising a floor can
  make the tool stop working on a host where it worked yesterday.

Treat as contract changes: renaming or removing a subcommand or flag, changing a
default (resources, network mode, distro tag), changing JSON output fields,
changing exit-code meanings, changing the state directory layout, changing the
guest's device topology through the `virt-install` arguments (disk bus, NIC model,
console), and adding a required tool or raising a minimum version.

Keep implementation, tests, golden files, `docs/cli.md`, and `--help` text
synchronized in the same change. Do not make breaking contract or schema changes
without explicit approval and a documented migration path — for state and
manifests, that means reading the old `schemaVersion` and either upgrading it in
place or telling the user exactly what to rebuild. Preserve backward
compatibility unless the task explicitly authorizes a break.

## 9. Security And Secrets

[`SECURITY.md`](./SECURITY.md) defines this repository's hard security
boundaries, and its MUST/MUST NOT rules are binding. Read it before touching
guest isolation, networking, image provenance, SSH key handling, host
filesystem sharing, subprocess invocation, or the destroy path.

The essentials, which `SECURITY.md` states precisely:

- **The guest is untrusted.** An agent inside a VM may run arbitrary code. No
  host path, credential, or host-only service is reachable from a guest by
  default.
- Never commit real credentials, tokens, private keys, or PII. SSH **public**
  keys are injected at first boot via cloud-init; private keys never enter an
  image, a seed, or the repository.
- Never bake secrets into a base image or a cloud-init seed that outlives the
  VM, and never log the contents of user-supplied cloud-init data.
- Treat everything crossing a boundary as untrusted: CLI arguments, config
  files, environment variables, registry metadata, guest agent responses, DHCP
  leases, and the stdout of every helper process.
- Bridged networking puts the guest directly on the operator's LAN. It is never
  the default and always requires an explicit flag.
- Report vulnerabilities through the private process in `SECURITY.md`; do not
  open a public issue.

## 10. Git, Commits, And Pull Requests

- Use focused branches such as `feat/<short-desc>`, `fix/<short-desc>`,
  `chore/<short-desc>`, or `docs/<short-desc>`.
- Use Conventional Commits, for example:
  - `feat(create): add --network bridge`
  - `fix(image): reject manifests with an unknown schema version`
  - `docs(adr): record the direct kernel boot decision`
  - `chore(deps): raise the minimum virt-install version to 4.1`
- Keep changes small and reviewable. Separate unrelated refactors from behavior
  changes.
- Pull requests must explain what changed, why it changed, how it was verified,
  and any operational or compatibility impact.
- Include the exact commands you ran, and for VM-affecting changes the relevant
  `virsh dumpxml` excerpt, guest console output, or `agent-vm doctor` result
  that shows the behavior. State plainly which distros and network modes you did
  **not** verify.

## 11. Things Agents Must Not Do Without Explicit Approval

- Force-push, rewrite shared history, delete branches, or run destructive Git
  commands.
- Add or replace top-level dependencies, frameworks, or external services —
  including swapping the OCI tooling or the root filesystem builder, adding a
  required host tool, or raising a minimum tool version.
- Reimplement in Go what a standard host tool already does, or bypass
  `internal/hostexec` to spawn a process directly. If a tool cannot do what is
  needed, say so in an ADR and get approval before writing the replacement.
- Change public contracts (§8), authentication or SSH key handling,
  compatibility targets, deployment behavior, or CI/release workflows.
- Modify `LICENSE`, weaken `SECURITY.md`, or bypass repository protections.
- Disable tests, validation, linting, type checks, security checks, or hooks.
- Commit generated build output, base images, disk images, credentials, or large
  speculative abstractions.
- Run destructive libvirt or host operations outside the tool's own state: no
  `virsh destroy`/`undefine` on domains this tool did not create, no editing or
  deleting host networks or bridges, no `rm -rf` outside the state directory,
  and no changes to host firewall rules.
- Perform a mutating live test on a host running VMs that matter, or run the
  integration suite against a production libvirt host.

## 12. When In Doubt

1. Read nearby source, tests, `docs/cli.md`, `docs/architecture.md`, and recent
   ADRs.
2. Preserve existing module and trust boundaries.
3. Ask before making an irreversible decision about the CLI contract, state
   layout, boot method, guest isolation, dependencies, or release process.
4. Prefer the smallest change that fully solves the stated problem.
