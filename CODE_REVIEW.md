# Code Review

> Review instructions for humans and AI coding agents. Follow this process when
> reviewing a commit, branch, pull request, or working-tree diff.

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
