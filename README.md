# SnapHop AI Project Template

A stack-neutral starting point for software projects developed by humans and AI
coding agents. The template centers repository knowledge in `AGENTS.md`, makes
security boundaries explicit, and keeps tool-specific instruction files as
small shims.

## Start A New Project

1. Create a repository from this template.
2. Follow [`docs/project-setup.md`](./docs/project-setup.md) and replace every
   `TODO(template)`, `<placeholder>`, and example address.
3. Add the real source tree, build configuration, and CI workflow.
4. Run the documented setup and verification commands from a fresh clone.
5. Ask a second person or independent agent to review the customized
   instructions before the first feature change.

Do not begin feature work while required placeholders remain in `AGENTS.md`,
`README.md`, `CONTRIBUTING.md`, or `SECURITY.md`.

## What The Template Provides

- [`AGENTS.md`](./AGENTS.md) — canonical project context and coding-agent
  instructions.
- [`CODE_REVIEW.md`](./CODE_REVIEW.md) — an independent, findings-first review
  process.
- [`SECURITY.md`](./SECURITY.md) — private vulnerability reporting plus
  customizable hard security boundaries.
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — a shared workflow for humans and
  agents.
- [`CHANGELOG.md`](./CHANGELOG.md) — Keep a Changelog structure with an
  `[Unreleased]` section.
- [`docs/architecture.md`](./docs/architecture.md) — a system map that captures
  runtime and failure behavior.
- [`docs/decisions/`](./docs/decisions/) — Architecture Decision Records.
- [`.github/pull_request_template.md`](./.github/pull_request_template.md) — a
  review checklist aligned with the repository rules.
- [`.env.example`](./.env.example) and [`.gitignore`](./.gitignore) — safe
  starting defaults for local configuration.

## Agent Instruction Files

| File | Tool | Role |
|---|---|---|
| `AGENTS.md` | Codex and tools supporting the AGENTS.md convention | Canonical |
| `CLAUDE.md` | Claude Code | Imports/defers to `AGENTS.md` |
| `GEMINI.md` | Gemini CLI | Defers to `AGENTS.md` |
| `.github/copilot-instructions.md` | GitHub Copilot | Defers to `AGENTS.md` |
| `.cursor/rules/project.mdc` | Cursor | Defers to `AGENTS.md` |
| `.windsurf/rules/project.md` | Windsurf | Defers to `AGENTS.md` |
| `.cursorrules` | Legacy Cursor | Defers to `AGENTS.md` |
| `.windsurfrules` | Legacy Windsurf | Defers to `AGENTS.md` |

`AGENTS.md` is the source of truth. Put tool-specific instructions in a shim
only when that tool genuinely needs different behavior.

## Suggested Repository Shape

```text
.
├── src/                    # application source
├── tests/                  # automated tests
├── scripts/                # repeatable development/operations helpers
├── docs/                   # architecture, ADRs, and runbooks
├── .github/                # CI and contribution metadata
├── AGENTS.md
├── CODE_REVIEW.md
├── CONTRIBUTING.md
├── SECURITY.md
└── CHANGELOG.md
```

Adapt the shape to the project instead of forcing every stack into `src/` and
`tests/`.

## License

[MIT](./LICENSE)
