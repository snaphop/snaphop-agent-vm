# 8. Implement the tool as a single Go binary with no daemon

Date: 2026-08-17

## Status

Accepted. Amended by
[ADR-0009](./0009-orchestrate-existing-host-cli-tools.md): the tool orchestrates
existing CLI tools rather than binding libvirt, so the build needs no cgo.

## Context

The tool has to run on other people's Linux hosts, drive `virt-install`, `virsh`,
`podman`, `qemu-img`, and the libguestfs tools, generate cloud-init data, and be
invoked both by humans and by agent supervisors that only get an exit code and
stdout.

Options considered:

1. **Go.** Compiles to a single binary with no runtime to install, has strong
   subprocess and concurrency handling, `embed` for templates, and a fast test
   story. Installation is copying a file.
2. **Python.** Fastest to write, and the ecosystem's scripting default. But it
   puts an interpreter and dependency environment on every host,
   in a project whose users are often mid-setup already; version skew across
   Ubuntu/Fedora/Arch is a support burden we would carry forever.
3. **Bash around `virsh`.** Minimal dependencies and very transparent, but error
   handling, structured JSON output, path containment checks, locking, and
   templating are all things shell does badly — and every one of those is
   load-bearing here, because this tool deletes disk images and defines domains.

A daemon was also considered, for tracking VM state and reaping abandoned VMs. It
was rejected: libvirt already is that daemon. Duplicating VM state outside libvirt
creates a reconciliation problem, and a second long-running privileged component
is a security surface we do not need.

## Decision

Implement `agent-vm` as a **single static Go binary (Go 1.22+) with no daemon**.

- `cmd/agent-vm` is a thin entry point: signal handling and exit status only.
  Flag parsing belongs to `internal/cli`.
- `internal/*` holds the logic, one package per concern (`cli`, `config`, `image`,
  `domain`, `network`, `guestinit`, `state`, `hostexec`).
- `internal/hostexec` is the single place processes are spawned: argv construction,
  logging, exit-status handling, and timeouts. Every tool sits behind an interface
  so unit tests substitute fakes at the process boundary, and `test/integration/`
  (build tag `integration`) covers the real thing.
- Pure Go, no cgo: the binary talks to libvirt through `virt-install`/`virsh`
  (ADR-0009), so there are no libvirt headers or shared libraries to link.
- Templates — per-distro `Containerfile`s, cloud-init user-data, the NAT network
  XML — are compiled in with `embed`; there is nothing to install alongside the
  binary.
- libvirt manages the long-running QEMU processes. Runtime VM state is read from libvirt
  and reconciled against `vm.json`, never cached as a second source of truth.
- Concurrency safety comes from per-VM and per-image file locks in the state
  directory, not from a coordinating process.

## Consequences

Easier:

- Installation is copying one binary onto a KVM host; no interpreter, no virtualenv,
  no package manager involvement.
- Structured errors, JSON output, path containment checks, and locking are
  straightforward and testable, which matters for a tool that deletes disks.
- Cross-compiling for x86_64 and aarch64 is a build flag, since the build is pure
  Go with no cgo.
- No daemon means no privileged background process of ours, and nothing to keep
  running for VMs to survive.

Harder:

- More code than a shell script for the same first milestone, and Go is more
  verbose than Python for the string and template work that remains.
- Orchestrating subprocesses is less ergonomic than calling a library, which is why
  `internal/hostexec` exists and why argument vectors are pinned by golden tests
  (ADR-0009).
- Without a daemon there is no reaper: a VM left running is left running.
  Advisory locks are released when their holding process exits; persistent lock
  files are not themselves evidence of a held lock (see ADR-0010 for remote
  lock lifetime).
- Contributors need Go plus a KVM-capable host for meaningful integration work.
