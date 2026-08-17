# AGENTS.md

> **Canonical instruction file for AI coding agents working in this repository.**
> Tool-specific files (`CLAUDE.md`, `GEMINI.md`, `.cursor/rules/`,
> `.windsurf/rules/`, `.github/copilot-instructions.md`) defer here. If
> anything conflicts, this file wins.

---

## 1. Project Overview

<!-- TODO(template): replace every placeholder in this section. -->

- **Name:** `<project-name>`
- **What it does:** `<one-sentence description>`
- **Primary language / runtime:** `<language and supported runtime versions>`
- **Key dependencies / frameworks:** `<frameworks, libraries, protocols>`
- **Deployment target:** `<where and how the project runs>`
- **Canonical product/API reference:** `<path or "not applicable">`

Describe the users, public interfaces, and most important invariants in one or
two short paragraphs. Name any compatibility constraints that agents could
otherwise discover only after breaking them.

## 2. Repository Layout

<!-- TODO(template): replace this example with the real repository tree. -->

```text
.
├── src/                    # application source
├── tests/                  # automated tests
├── scripts/                # repeatable development and operational helpers
├── docs/                   # architecture notes, ADRs, and runbooks
├── .github/                # CI and contribution metadata
├── AGENTS.md               # canonical agent instructions
├── CODE_REVIEW.md          # code-review process
├── CONTRIBUTING.md         # contribution workflow
├── SECURITY.md             # reporting policy and hard security boundaries
└── CHANGELOG.md            # human-readable history of notable changes
```

Document where new modules belong and how test paths relate to source paths.
Call out generated, vendored, mirrored, or compatibility-sensitive areas that
must not drift.

## 3. Setup

<!-- TODO(template): replace this block with commands that work from a fresh clone. -->

Prerequisites:

- `<runtime/tool and version>`
- `<package manager/build tool>`
- `<optional local service>`

```bash
git clone <repository-url>
cd <repository-directory>
<install-command>
cp .env.example .env
<initial-verification-command>
```

List any private registry access, local services, seed data, or environment
configuration needed for a complete development environment. Never put real
credentials in this file.

## 4. Common Commands

Prefer repository scripts over ad hoc commands so local work and CI stay
aligned. If a check does not exist, say so explicitly rather than leaving an
agent to invent one.

<!-- TODO(template): replace or remove every placeholder row. -->

| Task | Command |
|---|---|
| Install dependencies | `<install-command>` |
| Build | `<build-command>` |
| Run locally | `<run-command>` |
| Run all tests | `<test-command>` |
| Run one test | `<single-test-command>` |
| Lint | `<lint-command or "not configured">` |
| Format | `<format-command or "not configured">` |
| Typecheck | `<typecheck-command or "not configured">` |
| Package/container build | `<package-command or "not configured">` |
| Live/integration test | `<command, prerequisites, and mutation warning>` |

State the minimum verification required before handoff and any broader checks
required for packaging, deployment, schema, authentication, or public-contract
changes.

## 5. Architecture And Runtime Notes

<!-- TODO(template): replace these prompts with project-specific facts. -->

- Identify each major module and its responsibility.
- Describe the request/job/event flow across module boundaries.
- Document runtime profiles, feature flags, queues, databases, caches, and
  external services.
- Name canonical contracts such as OpenAPI files, schemas, protocol
  definitions, generated clients, or reference documentation, and say what
  must be updated together.
- Record compatibility constraints such as older consumers, supported runtime
  versions, wire formats, or database migration ordering.
- Explain CI and release behavior, especially whether merging deploys,
  publishes artifacts, or only verifies a build.
- Require an ADR in `docs/decisions/` for significant decisions that are hard
  to reverse, affect multiple components, or change security/deployment
  boundaries.
- Document observable or operational effects in `CHANGELOG.md` under
  `[Unreleased]` in the same change. Use Keep a Changelog categories and
  explain what changed and why in language a non-developer can follow. Pure
  refactors, test-only changes, and formatting-only changes may be omitted.

## 6. Code Style

- Match the surrounding style of every file you edit. Do not reformat unrelated
  code.
- Keep functions and modules focused. Preserve established boundaries instead
  of creating convenience dependencies across them.
- Prefer descriptive names over abbreviations or cleverness.
- Comments explain non-obvious decisions and constraints, not what the code
  already says.
- Do not leave dead code, commented-out replacements, or debugging output.
- Handle errors at the correct boundary; do not silently swallow failures.
- Keep public interfaces and user-facing behavior documented.
- Do not hand-edit generated or vendored files unless this repository
  explicitly requires it. Update the source and regenerate when possible.

<!-- TODO(template): add language/framework conventions that are actually enforced. -->

## 7. Testing

- Add or update tests for every behavior change. A bug fix requires a
  regression test unless the behavior cannot be exercised automatically; if
  so, explain the gap.
- Name tests after behavior, not implementation details.
- Prefer realistic values and test through public boundaries. Mock external
  systems at network, process, clock, filesystem, or service boundaries.
- Cover success, expected failure, authorization/validation, and edge cases
  proportional to the risk of the change.
- Do not disable, skip, or weaken tests to make a change pass.
- Do not rely on an optional integration test as the only regression coverage.
- Document tests that require credentials, containers, external services, or
  a running deployment.
- When asked to review code, follow `CODE_REVIEW.md`.

## 8. Public Contracts And Data

<!-- TODO(template): customize this section or state that it is not applicable. -->

- Identify which APIs, events, schemas, command-line interfaces, file formats,
  and environment variables are public or consumed outside this repository.
- Keep implementation, tests, generated artifacts, and canonical reference
  documentation synchronized.
- Treat changes to paths, methods, fields, defaults, status/error semantics,
  authentication, ordering, and pagination as contract changes.
- Do not make breaking contract or schema changes without explicit approval and
  a documented migration/versioning plan.
- Preserve backward compatibility unless the task explicitly authorizes a
  break.

## 9. Security And Secrets

- `SECURITY.md` defines this repository's hard security boundaries. Its
  MUST/MUST NOT rules are binding. Read it before touching authentication,
  authorization, credentials, logging, browser-facing pages, external input,
  deployment, or dependency policy.
- Never commit real credentials, tokens, private keys, customer data, or PII.
  Use environment variables or the approved secret-management mechanism.
- Do not log secrets, authorization headers, full sensitive payloads, or PII.
- Treat data from HTTP, messaging, files, environment variables, databases,
  subprocesses, and upstream services as untrusted.
- Report vulnerabilities using the private process in `SECURITY.md`; do not
  expose them in a normal public issue or discussion.

## 10. Git, Commits, And Pull Requests

- Use focused branches such as `feat/<short-desc>`, `fix/<short-desc>`,
  `chore/<short-desc>`, or `docs/<short-desc>`.
- Use Conventional Commits, for example:
  - `feat(auth): rotate refresh tokens`
  - `fix(api): reject expired credentials`
  - `chore(deps): update the HTTP client`
- Keep changes small and reviewable. Separate unrelated refactors from behavior
  changes.
- Pull requests must explain what changed, why it changed, how it was verified,
  and any operational or compatibility impact.
- Include screenshots, logs, or example requests when they materially help a
  reviewer verify behavior.

## 11. Things Agents Must Not Do Without Explicit Approval

- Force-push, rewrite shared history, delete branches, or run destructive Git
  commands.
- Add or replace top-level dependencies, frameworks, databases, or service
  providers.
- Change public contracts, schemas, authentication behavior, compatibility
  targets, deployment behavior, or CI/release workflows.
- Modify `LICENSE`, weaken `SECURITY.md`, or bypass repository protections.
- Disable tests, validation, linting, type checks, security checks, or hooks.
- Commit generated build output, credentials, production data, or large
  speculative abstractions.
- Perform a mutating live/production test unless the task explicitly authorizes
  it and the target is confirmed safe.

## 12. When In Doubt

1. Read nearby source, tests, canonical reference docs, and recent ADRs.
2. Preserve existing module and trust boundaries.
3. Ask before making an irreversible API, schema, auth, dependency, data, or
   deployment decision.
4. Prefer the smallest change that fully solves the stated problem.
