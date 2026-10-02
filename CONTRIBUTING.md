# Contributing

SnapHop Agent VM is designed for collaboration between humans and AI coding agents.
Both follow the same repository rules and quality bar.

## Prerequisites

- Go 1.22 or newer (pure Go build — no cgo, no libvirt headers)
- A Linux host with KVM (`/dev/kvm`), libvirt 9.0+, and QEMU 8.0+
- `virt-install` 4.0+, `qemu-img`, `podman` 4.0+, libguestfs 1.50+
- `golangci-lint`
- Membership in the `kvm` and `libvirt` groups

Host preparation is documented in [`docs/host-setup.md`](./docs/host-setup.md);
`agent-vm doctor` verifies it. See [`AGENTS.md`](./AGENTS.md) for complete setup
and command reference, and [`README.md`](./README.md) for the project overview.

You can build, unit-test, and lint without KVM — tools are faked at the process
boundary, and `--dry-run` shows what a change would actually run. VM lifecycle
integration and guest boot require KVM. Remote transport integration tests
require SSH rather than KVM.

One rule shapes most contributions: **this tool orchestrates existing tools rather
than reimplementing them** ([ADR-0009](./docs/decisions/0009-orchestrate-existing-host-cli-tools.md)).
Before writing code that generates domain XML, builds an ISO, extracts container
layers, or parses human-readable output, check whether `virt-install`, `virsh`,
`podman`, `qemu-img`, `cloud-localds`, or a libguestfs tool already does it — and
whether it has a machine-readable output mode. Reimplementation needs an ADR
explaining why the tool could not be used.

## Before You Start

1. Read [`AGENTS.md`](./AGENTS.md), the canonical guide for architecture,
   commands, code style, testing, and protected areas.
2. Read [`SECURITY.md`](./SECURITY.md). Its hard security boundaries are binding
   for code generation and refactoring — particularly around guest isolation,
   networking, and the destroy path.
3. Read [`docs/cli.md`](./docs/cli.md) if your change touches the command line,
   configuration, on-disk state, or domain XML. Those are public contracts.
4. Read recent ADRs in [`docs/decisions/`](./docs/decisions/). Changes to the
   virtualization stack, boot method, image cache format, guest-to-host sharing,
   network modes, default resource profile, or supported distro families — and any
   reimplementation of what a host tool already does — require a new ADR in the
   same change.
5. For non-trivial product or architecture work, discuss the approach in an issue
   before implementation.
6. Report suspected vulnerabilities privately through `SECURITY.md`; do not open a
   normal public issue.

## Development Workflow

```bash
git checkout -b feat/short-description

gofmt -l .                 # must print nothing
go vet ./...
golangci-lint run
go test ./...
# or all four at once:
make check

# Only on a KVM-capable host you are willing to have VMs created on:
go test -tags integration ./test/integration/... -timeout 90m

git commit -m "feat(scope): what changed"
git push --set-upstream origin feat/short-description
```

The first four commands are the minimum verification before handing work off, and
`scripts/check.sh` (or `make check`) runs all four in one go. GitHub Actions
runs that script on every pull request and on pushes to `master`. **Merging
does not publish or deploy anything.** Pushing a `vX.Y.Z` tag builds the
static binaries and attaches them, with `LICENSE` and `NOTICE`, to a GitHub
Release. `scripts/build-release.sh` builds the same binaries locally and does
not publish them.

### About The Integration Suite

It creates and destroys real VMs and disk images on its target host, using the
`agent-vm-test-` VM name prefix and a dedicated state directory. NAT lifecycle
tests use `agent-vm-nat`, create it if absent, and leave it in place; the VM
prefix does not apply to this shared network.
Never run it against a libvirt host with VMs someone cares about, and never
against a production host without explicit approval. It is not required for merge,
which is exactly why unit and golden-file coverage matter.

## Pull Request Checklist

- [ ] `gofmt -l .` is empty; `go vet ./...`, `golangci-lint run`, and
      `go test ./...` pass.
- [ ] New behavior has tests; bug fixes have regression coverage that does not
      depend on the optional integration suite.
- [ ] Golden files regenerated intentionally, with any `virt-install` argv or
      cloud-init user-data diff explained in the description.
- [ ] No new reimplementation of something a host tool already does, and no
      `os/exec` outside `internal/hostexec`.
- [ ] Any new tool dependency or raised version floor is reflected in `doctor`, the
      dependency tables, `docs/host-setup.md`, and `docs/cli.md`.
- [ ] Public contracts synchronized: `docs/cli.md` (including **Underlying
      Commands**), `--help` text, config keys, env vars, and any `schemaVersion`
      bump with its rebuild/upgrade path.
- [ ] `CHANGELOG.md` has an `[Unreleased]` entry for notable behavior, security,
      dependency, compatibility, or deployment changes.
- [ ] A new ADR records any significant architectural decision.
- [ ] The PR states what changed, why, the exact commands run, and which distros
      and network modes were **not** verified.
- [ ] No credentials, private keys, PII, disk images, base images, generated build
      output, or unrelated formatting changes are included.
- [ ] Security-sensitive changes received focused review under
      [`CODE_REVIEW.md`](./CODE_REVIEW.md).

## Code Review

Reviewers check correctness, regressions, security, failure modes, public
contracts, and adherence to `AGENTS.md` and `SECURITY.md`. Follow
[`CODE_REVIEW.md`](./CODE_REVIEW.md) for structured reviews, and treat these areas
as always warranting focused attention: the destroy and rollback paths, path
containment inside the state directory, subprocess invocation and output parsing,
network mode selection, cloud-init user-data contents, and anything that shares host
resources into a guest.

## Working With AI Agents

- Give the agent a focused task and explicit acceptance criteria.
- Point it to any issue, specification, or external contract not stored in the
  repository.
- Require it to report the commands it ran, the distros and network modes it
  actually exercised, and any verification it could not perform — a change that
  was never booted should say so.
- Review agent diffs to the same standard as human contributions; do not
  auto-merge.
- Reject unrelated changes unless their necessity is clear and documented.
- Be specific about the host an agent may use. Anything touching VM lifecycle
  should be exercised on a disposable host, never on one running VMs that matter.
