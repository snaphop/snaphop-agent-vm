# Contributing

This project is designed for collaboration between humans and AI coding agents.
Both follow the same repository rules and quality bar.

## Prerequisites

<!-- TODO(template): replace with the real prerequisites and setup link. -->

- `<runtime and version>`
- `<build/package tool>`
- `<optional container or local service>`

See `AGENTS.md` for complete setup and common commands, and `README.md` for the
project overview and operational walkthrough.

## Before You Start

1. Read [`AGENTS.md`](./AGENTS.md), the canonical guide for architecture,
   commands, code style, testing, and protected areas.
2. Read [`SECURITY.md`](./SECURITY.md). Its hard security boundaries are
   binding for code generation and refactoring.
3. Read recent ADRs in [`docs/decisions/`](./docs/decisions/). Significant,
   hard-to-reverse architectural changes require a new ADR in the same change.
4. For non-trivial product or architecture work, discuss the approach in an
   issue before implementation.
5. Report suspected vulnerabilities privately through `SECURITY.md`; do not
   open a normal public issue.

## Development Workflow

<!-- TODO(template): replace the verification commands. -->

```bash
git checkout -b feat/short-description

<lint-command>
<typecheck-command>
<test-command>
<package-command-if-relevant>

git commit -m "feat(scope): what changed"
git push --set-upstream origin feat/short-description
```

CI should run the same core checks documented in `AGENTS.md`. Document whether
a merge publishes artifacts or deploys; contributors must not have to infer
delivery behavior from workflow YAML.

## Pull Request Checklist

- [ ] Required lint, format, type, test, and package checks pass.
- [ ] New behavior has tests; bug fixes have regression coverage.
- [ ] Public contracts, reference documentation, and generated artifacts are
      synchronized.
- [ ] `CHANGELOG.md` contains an `[Unreleased]` entry for notable behavior,
      security, dependency, compatibility, or deployment changes.
- [ ] A new ADR records any significant architectural decision.
- [ ] The PR explains what changed, why, verification performed, and
      operational/compatibility impact.
- [ ] No credentials, PII, customer data, generated build output, or unrelated
      formatting changes are included.
- [ ] Security-sensitive changes received focused review under
      [`CODE_REVIEW.md`](./CODE_REVIEW.md).

## Code Review

Reviewers check correctness, regressions, security, failure modes, public
contracts, and adherence to `AGENTS.md` and `SECURITY.md`. Follow
[`CODE_REVIEW.md`](./CODE_REVIEW.md) for structured reviews, especially for
authentication, external input, browser-facing surfaces, dependencies,
deployment, or other trust-boundary changes.

## Working With AI Agents

- Give the agent a focused task and explicit acceptance criteria.
- Point it to any issue, specification, or external contract that is not stored
  in the repository.
- Require it to report the commands it ran and any verification it could not
  perform.
- Review agent diffs to the same standard as human contributions; do not
  auto-merge.
- Reject unrelated changes unless their necessity is clear and documented.
