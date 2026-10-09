# 1. Record architecture decisions

Date: 2026-08-17

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

For this project specifically, an ADR is required for changes to the
virtualization stack, the boot method, the image cache format, guest-to-host
sharing, network modes, where host tools run (ADR-0010), the default resource
profile, adding a supported distro
family, adding a base image variant or changing where guest tooling comes from
(ADR-0012, ADR-0013, ADR-0016), the Actions runner account (ADR-0017),
joining a guest to an overlay network (ADR-0014),
registering a guest as a GitHub Actions runner (ADR-0015),
preparing a host with `agent-vm setup` (ADR-0018), and
implementing in our own code something a standard host tool
already does (ADR-0009).

## Consequences

- New contributors and AI agents get fast, durable context on past decisions.
- ADRs become required for non-trivial architectural changes (mentioned in
  `CONTRIBUTING.md` and `AGENTS.md`).
- Small overhead per significant decision; no overhead for routine work.
