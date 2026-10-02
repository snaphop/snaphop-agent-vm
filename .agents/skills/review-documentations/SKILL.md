---
name: review-documentations
description: Audit and correct the repository's complete documentation set against current Go code, configuration, contracts, and operations. Use for requests to review, reconcile, update, complete, or validate all documentation; do not use for a narrow edit to one known document.
---

# Review Documentation

Audit the complete documentation set against implemented and configured
behavior.

## Establish The Documentation Set

1. Read [`AGENTS.md`](../../../AGENTS.md) first, then `SECURITY.md`,
   `CONTRIBUTING.md`, `CODE_REVIEW.md`, `README.md`, `docs/README.md`, the
   changelog, every ADR, and the CLI, architecture, and host-setup guides.
2. Inventory all tracked documentation. That includes Markdown outside `docs/`,
   `.agents/skills`, `.env.example`, GitHub workflow and Dependabot
   configuration, the pull-request template, Go module metadata, the Makefile
   and `scripts/`, embedded templates under `templates/`,
   `test/toolout/README.md`, `LICENSE`, and `NOTICE`.
3. Record the initial inventory and reconcile it with a final inventory so no
   artifact is silently skipped.

## Establish Current Facts

Use the nearest owning source: Go implementation and tests, `go.mod`, the
Makefile and `scripts/check.sh`, embedded templates, golden files, runtime
defaults, CI and release workflows, and version floors. Use history only to
understand intent. An accepted ADR records a decision; current guides must say
whether it has been delivered or remains an operator gate.

Pay particular attention to the Go language version and the host-tool floors
(libvirt, QEMU, `virt-install`, podman, libguestfs); command names, flags, and
exit codes; package boundaries; config keys, `AGENT_VM_*` variables, and the
default resources and NAT mode; `vm.json` and `manifest.json` schema versions;
the `virt-install` argument vector and cloud-init user-data; the distro and
variant matrix; network modes; where commands run for a `qemu+ssh://` URI;
logging prohibitions; and the difference between a merge and a `vX.Y.Z`
release. Preserve accurate historical ADR context and link a later superseding
decision instead of rewriting history.

## Reconcile And Complete

Fix contradictions, stale claims, broken commands and links, wrong paths or
casing, missing index entries, ambiguous status, and material language defects.
Keep `docs/cli.md`, `--help`, config keys, environment variables, golden files,
and the implementation synchronized when a documented contract is wrong and the
correction is documentation. Add documentation only when supported by
implementation, configuration, an accepted decision, or an explicit unresolved
requirement.

Update affected entry points together, including `README.md`, `docs/README.md`,
agent instructions, policies, and `[Unreleased]` notes when the correction is
notable. Add or supersede an ADR only for a difficult-to-reverse decision or a
material change to architecture, the public CLI, guest isolation, boot, image
cache, networking, or where host tools run. Documentation review does not
authorize implementation, dependency, CI, release, libvirt, or issue-tracker
changes.

## Validate And Finish

Check every relative Markdown link and referenced local path with
case-sensitive resolution. Check authoritative external links when freshness
matters. Search for scaffold placeholders, stale versions and names, duplicate
changelog headings, and claims contradicted by code or tests. Run
`git diff --check` and `make check`. When a correction changes a documented
lifecycle, image, or network claim, follow the extra checks in `AGENTS.md` §4.
Run the integration suite only on an authorized disposable KVM host, and say
which distros and network modes were not booted.

Review the final diff for implementation edits, generated output, secrets,
disk images, and unrelated user work. Report the artifacts reviewed,
corrections made, validation performed, and any skipped or uncertain claims.
Do not claim the complete documentation is correct if material evidence could
not be checked.
