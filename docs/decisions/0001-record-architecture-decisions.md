# 1. Record architecture decisions

Date: YYYY-MM-DD

## Status

Accepted

## Context

We need to capture significant architectural decisions — the ones that are
hard to reverse, affect multiple components, or whose rationale would be
useful to a future maintainer (human or AI agent) trying to understand "why
on earth was it built this way?"

Tribal knowledge and Slack archeology don't survive turnover.

## Decision

We will record architecturally significant decisions as Architecture Decision
Records (ADRs) in `docs/decisions/`, following Michael Nygard's lightweight
template:

- **Title:** numbered, short, imperative.
- **Status:** Proposed / Accepted / Deprecated / Superseded by ADR-NNNN.
- **Context:** the forces at play.
- **Decision:** what we agreed to do.
- **Consequences:** what becomes easier and what becomes harder.

Files are named `NNNN-kebab-case-title.md`, numbered sequentially, never
renumbered.

## Consequences

- New contributors and AI agents get fast, durable context on past decisions.
- ADRs become required for non-trivial architectural changes (mentioned in
  `CONTRIBUTING.md` and `AGENTS.md`).
- Small overhead per significant decision; no overhead for routine work.
