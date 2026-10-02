# Code Review

> Review instructions for humans and AI coding agents. Follow this process when
> reviewing a commit, branch, pull request, or working-tree diff. A request
> covering the complete bug, documentation, enhancement, or pull-request queue
> uses the workflow under [`.agents/skills/`](./.agents/skills/) instead.

## Process

1. Use an independent reviewer when the tooling supports it. A fresh agent or
   person should receive the repository path, review target, and acceptance
   criteria without inheriting the author's private reasoning.
2. The reviewer gathers context from `AGENTS.md`, `SECURITY.md`, canonical
   contracts/reference documentation, nearby implementation and tests, and
   relevant ADRs.
3. Review for correctness, regressions, security, failure handling, public
   contract compatibility, concurrency/state behavior, resource bounds,
   observability, and adequate tests.
4. Report findings before proposing edits. Each finding includes:
   - severity: critical, high, medium, low, or nit;
   - a precise `file:line`;
   - the concrete failure mode and conditions that trigger it;
   - why existing tests or safeguards do not already cover it.
5. End with an overall verdict and any verification gaps. If there are no
   findings, say so explicitly rather than inventing marginal issues.

The reviewer must not modify code unless the task separately asks for fixes.

## What To Look At First In This Repository

This tool defines virtual machines, deletes disk images, and puts untrusted
guests on a network. Those are the places where a bug is expensive, so review
them first and hardest:

- **Destroy and rollback paths.** Does a failed `create` leave a defined domain,
  an orphaned overlay, or a half-written state directory? Does a cleanup failure
  get reported (exit `7`) rather than swallowed? Does `destroy` act only on the
  domain and paths recorded in `vm.json`?
- **Path containment.** Every write and delete must resolve inside the state
  directory, symlinks included. A missing check here means the tool can delete
  something it never created.
- **Reimplementation.** The first question for any new code: does `virt-install`,
  `virsh`, `podman`, `qemu-img`, `cloud-localds`, or a libguestfs tool already do
  this? Hand-rolled domain XML, ISO building, layer extraction, or partitioning
  needs an ADR, not a review approval (ADR-0009).
- **Subprocess invocation.** Everything goes through `internal/hostexec` with an
  explicit argument vector — no `os/exec` elsewhere, no shell strings, no untrusted
  value a tool could read as an option. Are exit status and stderr actually checked,
  or is a silent tool failure treated as success? Does the error name the tool and
  argv so a user can rerun it?
- **Output parsing.** Is a machine-readable mode available and used
  (`--output=json`, `--format json`, a structured `virsh` subcommand)? Is the parser
  as narrow as possible, and backed by a fixture captured from the real tool rather
  than invented output?
- **Tool dependencies and versions.** A new tool or a raised version floor is a
  contract change: `doctor`, the dependency tables, `docs/host-setup.md`, and
  `docs/cli.md` must all move together.
- **Generated artifacts.** cloud-init user-data must be valid and correctly escaped.
  Compare golden `virt-install` argv and user-data diffs against the intended
  change — an unexplained device or `kernel_args` change is a contract change.
- **Network mode selection.** Bridged mode must never be reachable implicitly,
  including as a fallback when NAT setup fails.
- **Guest isolation.** Any new host path, device, or channel exposed to a guest
  needs an ADR and maintainer approval, not a review approval.
- **Image provenance.** Digest verification must be enforced, with no fallback to
  an unpinned tag, and base images must be immutable and atomically published.
- **Untrusted guest data.** Guest agent responses, DHCP leases, and console output
  are attacker-influenced input; check they are validated before being used in
  paths, commands, or identity decisions.
- **Concurrency.** Per-VM and per-image locking around create/destroy and image
  builds, and no second source of truth for runtime state — libvirt owns that.
- **Contract sync.** Flags, `--help`, `docs/cli.md`, config keys, env vars, and
  `schemaVersion` handling must move together, with a stated rebuild or upgrade
  path.
- **Verification honesty.** The PR should say which distros and network modes were
  actually booted. "Tests pass" without an integration run is not evidence that a
  VM still boots; say so if that gap exists.

## Severity Guide

- **Critical:** likely credential/data compromise, destructive behavior, or
  broad production outage.
- **High:** material correctness/security failure in a common or reachable
  path.
- **Medium:** real bug or operational failure with narrower preconditions.
- **Low:** limited-impact issue worth fixing but unlikely to affect normal use.
- **Nit:** optional clarity/style improvement with no meaningful behavior
  impact.

## Acting On Findings

- Fix findings with substantive correctness, security, compatibility, or
  operational impact after weighing them against the intended change.
- Favor the author's deliberate judgment on low-severity and nit findings; do
  not churn code only to satisfy stylistic preference.
- If a finding comes from an intentionally mirrored pattern, fix every affected
  occurrence together or document why the scope is narrower.
- Add regression coverage for corrected behavior where practical.
- Report which findings were fixed, declined, or deferred, with concise
  reasoning.
