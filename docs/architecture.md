# Architecture

> Keep this document as a current high-level map of the system. Detailed,
> hard-to-reverse decisions belong in [`decisions/`](./decisions/).

## Context

<!-- TODO(template):
- What problem does the system solve?
- Who uses or operates it?
- What are the latency, throughput, availability, privacy, and compliance needs?
- Which responsibilities deliberately live outside this repository?
-->

## High-Level Design

<!-- TODO(template): replace with the real request/job/event flow. -->

```text
[ client ]
    |
    | public protocol
    v
[ application boundary ]
    |
    +----> [ durable state ]
    |
    +----> [ external service / queue / worker ]
```

Explain trust boundaries, synchronous versus asynchronous work, timeout/retry
behavior, and where validation, authentication, authorization, and persistence
occur.

## Components

<!-- TODO(template): add one section per major module or deployable. -->

### `<component>`

- **Responsibility:** `<what it owns>`
- **Public interface:** `<API, events, CLI, library surface>`
- **Key dependencies:** `<internal and external dependencies>`
- **Failure behavior:** `<timeouts, retries, fallback, partial failure>`
- **Compatibility constraints:** `<consumers, runtime, schema, wire format>`

## Request And Data Flow

<!-- TODO(template): describe the important path step by step, including errors. -->

1. `<entry point>`
2. `<validation/authentication>`
3. `<business or orchestration step>`
4. `<persistence/external interaction>`
5. `<response/event/side effect>`

## Public Contracts

<!-- TODO(template): link canonical API specs, schemas, generated clients, event
definitions, file formats, environment variables, and versioning policy. -->

Document what must be updated together when a contract changes.

## Data Model And State

<!-- TODO(template):
- Key entities and relationships.
- System of record for each entity.
- Transactions, consistency, idempotency, and migration behavior.
- Sensitive-data classification and retention.
-->

## Runtime Profiles And Configuration

<!-- TODO(template): local/test/staging/production differences, feature flags,
configuration sources, secret sources, ports, and required services. -->

## External Dependencies

| Dependency | Purpose | Failure Mode | Recovery/Owner |
|---|---|---|---|
| `<service>` | `<why it is needed>` | `<timeout/error impact>` | `<retry/fallback/team>` |

## Deployment And Release

<!-- TODO(template):
- Build artifact/container/serverless package.
- Deployment target and promotion flow.
- Whether merging deploys or only verifies.
- Rollback method.
- Configuration and secret injection.
- Database/schema migration ordering.
-->

## Observability

<!-- TODO(template):
- Structured logs and redaction rules.
- Metrics, traces, health/readiness endpoints.
- Alerts and dashboards.
- Correlation/request IDs.
- Operational runbooks.
-->

## Security Boundaries

Summarize the trust model and link to [`../SECURITY.md`](../SECURITY.md). Name
where credentials enter, where authorization decisions occur, and which data
must never cross a boundary.

## Constraints And Risks

<!-- TODO(template): list known scaling limits, blocking paths, legacy
compatibility, optional integration tests, single points of failure, and
intentional technical debt. -->
