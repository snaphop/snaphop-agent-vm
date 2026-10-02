---
name: review-bugs
description: Review and resolve the repository's complete open bug queue. Use when asked to triage, validate, fix, resolve, or close every bug or all bug-classified issues; do not use for a single ordinary defect unless the user explicitly invokes this skill.
---

# Review Bugs

Resolve every open bug with evidence while preserving the product contract and
the guest-isolation, image-provenance, orchestration, and destroy-path
invariants in [`AGENTS.md`](../../../AGENTS.md) and
[`SECURITY.md`](../../../SECURITY.md).

## Establish Scope And Authority

1. Read `AGENTS.md`, `SECURITY.md`, `CONTRIBUTING.md`, `CODE_REVIEW.md`, and
   the documentation relevant to the reports, especially `docs/cli.md`,
   `docs/architecture.md`, and `docs/host-setup.md`.
2. Identify the issue host and repository from local Git metadata. The default
   branch is `master`. Use the host's purpose-built connector or CLI to
   inventory every open Bug issue type and every issue carrying the
   repository's bug label. Use the live taxonomy; do not infer the queue from
   titles. The conventional label is `bug`.
3. Record the initial set and recheck it before finishing. Route suspected
   vulnerabilities through the private process in `SECURITY.md`. Do not open a
   public issue for them.
4. Separate review authority from mutation authority. Triage alone does not
   authorize comments, labels, issue closure, commits, merges, releases, or
   host or libvirt changes.

## Validate Every Report

Read the full report, discussion, linked work, acceptance criteria, and affected
versions. Inspect the current default branch and the history needed to
distinguish a current defect from an already-fixed or unreleased change. Trace
the owning path through `internal/cli`, `internal/config`, `internal/image`
(including `templates/distro`), `internal/domain`, `internal/network`,
`internal/guestinit`, `internal/state`, `internal/github`, `internal/hostexec`,
and the integration tests when the report is about a real boot.

Reproduce safely when practical. Unit tests fake host tools at the process
boundary and do not need `/dev/kvm`. A live reproduction starts with
`--dry-run`, then uses a dedicated state directory and the `agent-vm-test-`
name prefix. Never reproduce against a libvirt host that has VMs someone cares
about, and never against a production host without explicit approval. Compare
the result with `docs/cli.md`, `SECURITY.md`, configuration, the golden
`virt-install` argument vector and cloud-init user-data, and the tests.
Classify each report:

- **Valid:** supported behavior violates a contract or repository invariant.
- **Invalid:** evidence establishes intended behavior, unsupported use,
  operator misconfiguration, a duplicate, a defect in libvirt, QEMU/KVM,
  podman, or libguestfs rather than in this tool's invocation of them, or a
  fix already present in a released version.
- **Blocked:** essential evidence or authority is unavailable; do not guess.

## Resolve Valid And Invalid Reports

For a valid bug, implement the smallest complete fix when authorized. Preserve
the guest as untrusted, NAT as the default network mode, immutable
digest-verified base images, copy-on-write overlays, and orchestration of
existing host tools through `internal/hostexec`. `destroy` and rollback act
only on the domain and paths recorded for that VM, after path containment
checks, and a failed cleanup reports what remains. Only SSH public keys enter
a guest. Older base images stay bootable, or `schemaVersion` is raised with a
documented rebuild path. The precise rules are in `SECURITY.md`.

Add regression coverage at the process boundary. Do not use the integration
suite as the only regression test. Synchronize `docs/cli.md`, `--help`, golden
files, config keys, environment variables, ADRs, and `[Unreleased]` notes when
the fix affects them. Regenerate golden files with
`go test ./... -update-golden`; do not hand-edit them.

Run focused tests, then `make check` and `git diff --check`. Apply the extra
checks in `AGENTS.md` §4 when the fix changes tool arguments, cloud-init,
state, the CLI, a host-tool version floor, an image recipe, networking, or
where commands run. Run `go test -tags integration ./test/integration/...`
only when a real boot is required and the user has authorized a disposable KVM
host. Name the distros and network modes actually exercised. Do not weaken a
check to obtain a pass. A missing `golangci-lint` is a verification gap, not a
pass. Land a fix or close an issue only to the extent the user authorized; a
local fix is not grounds to call an issue resolved. Merging does not publish a
release.

For an invalid report, provide the decision, concrete evidence, applicable
contract or configuration, verification, and an actionable correction. Confirm
duplicate scope before linking the canonical issue. Keep vulnerability details
off the public tracker, and never expose private keys, registry credentials,
GitHub tokens, operator-supplied cloud-init contents, or guest filesystem
contents.

## Finish

Reconcile the final bug queue with the initial inventory. Report each issue's
classification, evidence, action, verification, tracker state, and release
status. Do not claim all bugs are resolved while a report is unreviewed,
blocked, only fixed locally, or still awaiting release.
