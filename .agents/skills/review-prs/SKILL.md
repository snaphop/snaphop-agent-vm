---
name: review-prs
description: Review and resolve the repository's complete open pull-request queue. Use when asked to review, test, merge, or clean up all outstanding PRs; do not use for one ordinary PR unless explicitly invoked.
---

# Review Pull Requests

Process every open pull request with evidence while preserving repository,
security, compatibility, and release rules.

## Establish Scope And Authority

1. Read [`AGENTS.md`](../../../AGENTS.md), [`SECURITY.md`](../../../SECURITY.md),
   [`CONTRIBUTING.md`](../../../CONTRIBUTING.md), [`CODE_REVIEW.md`](../../../CODE_REVIEW.md),
   and the documentation relevant to the changes.
2. Use GitHub metadata and its purpose-built connector or CLI to inventory all
   open pull requests, including Dependabot updates. Record the initial set and
   recheck it before finishing. The default branch is `master`.
3. Keep suspected vulnerability details private under `SECURITY.md`.
4. Review and test authority does not authorize merge, branch deletion, history
   rewriting, a release tag, or mutation of a host or its libvirt. Establish
   each authority before the corresponding action.

## Review Every Pull Request

Read the description, full diff, discussion, linked issues, acceptance
criteria, and author rationale. Review against the repository contracts and
follow the finding format in `CODE_REVIEW.md`. Give particular attention to:

- destroy, rollback, and path containment, including refusal to act on a domain
  this tool did not record;
- guest isolation, NAT as the default, explicit bridged mode, and no new host
  path, device, or credential reachable from a guest;
- image provenance, immutable bases, and copy-on-write overlays;
- subprocess invocation only through `internal/hostexec`, machine-readable tool
  output, and no reimplementation of an existing host tool without an ADR;
- package ownership across `internal/cli`, `internal/config`, `internal/image`,
  `internal/domain`, `internal/network`, `internal/guestinit`, `internal/state`,
  `internal/github`, and `internal/hostexec`;
- synchronization of `docs/cli.md`, `--help`, config, environment variables,
  golden files, schema versions, ADRs, and the changelog;
- SSH public keys only, `gh` remaining on the host, and no logged cloud-init
  contents or secrets;
- dependency necessity, pinned actions, a raised tool-version floor, debug
  output, generated drift, and test adequacy, including a unit-level regression
  for a bug fix.

A new dependency, a raised minimum tool version, a public-contract change, or a
CI or release-workflow change needs the explicit approval `AGENTS.md` requires.
Report that instead of treating the pull request as ready.

Check mergeability with `master`. Resolve conflicts only when authorized, by
merging `master` into the pull-request branch. Rebase only with explicit
history-rewrite authority. Never resolve a conflict by weakening a security,
containment, or compatibility invariant.

## Verify And Resolve

Run focused tests, then `make check` and `git diff --check`. Apply the extra
checks in `AGENTS.md` §4 for tool arguments, cloud-init, state, CLI, host-tool
versions, image recipes, networking, or command location. Run
`go test -tags integration ./test/integration/...` only when the change needs a
real boot and the user has authorized a disposable KVM host. Record the distros
and network modes exercised, and state the ones not verified. For a
`qemu+ssh://` change, verify local and remote or state the gap. Never bypass or
weaken a failing check. A missing `golangci-lint`, or an integration run that
was not authorized, is a verification gap.

Fix review findings and conflicts only within the authority granted. Report
findings in the `CODE_REVIEW.md` form before editing. Merge an approved pull
request only after review and the required verification pass, using the
repository's existing GitHub merge method. Verify the result on GitHub and on
`master`. Merging does not publish a release. A release happens only when a
`vX.Y.Z` tag runs `.github/workflows/release.yml`.

Delete remote or local branches only with explicit cleanup authority. Remove
temporary worktrees and disposable test VMs and state without disturbing
pre-existing user changes. Do not `virsh destroy` or `undefine` a domain this
tool did not create.

## Finish

Reconcile the final open queue with the initial inventory. For every pull
request, report its ID, title, branch, findings and resolution, compatibility
impact, conflict status, verification, and final state. State why any pull
request remains open or blocked.
