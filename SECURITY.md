# Security Policy

This file has two roles:

1. It defines the private vulnerability-reporting process.
2. It defines hard security boundaries for humans and AI coding agents
   generating or refactoring code in this repository.

The MUST/MUST NOT rules below are binding. Do not weaken, bypass, or remove
them without explicit maintainer approval.

## Reporting A Vulnerability

Please do not open a public issue or discussion for a suspected vulnerability.

Email <security@example.com> <!-- TODO(template): replace with a monitored private channel. -->
with:

- A description of the issue and potential impact.
- Reproduction steps or a proof of concept.
- Affected files, versions, or commits, if known.
- Your name/handle for credit (optional).

Never include real credentials, tokens, customer data, or PII in the report.
Use redacted examples and coordinate a secure transfer if maintainers need
additional evidence.

<!-- TODO(template): state response and coordinated-disclosure expectations. -->

You should receive an acknowledgement within three business days.

## Hard Security Boundaries

Customize this section for the project's actual trust model. Remove examples
that do not apply, and add concrete invariants for authentication, data
handling, browsers, networking, storage, and deployment before feature work
begins.

### Secrets, Credentials, And Sensitive Data

- MUST NOT commit real credentials, access/refresh tokens, private keys,
  session identifiers, customer data, production data, or PII.
- MUST obtain secrets from the approved environment or secret-management
  mechanism. Example files contain blank values or obvious placeholders.
- MUST NOT log secrets, authorization headers, cookies, full sensitive request
  or response payloads, or PII.
- Types that carry secrets MUST redact them from string/debug
  representations.
- Build and deployment scripts MUST NOT echo secrets or pass passwords as
  command-line arguments when a stdin/file/secret-store mechanism exists.

### Authentication And Authorization

<!-- TODO(template): replace these generic rules with the project's exact invariants. -->

- Authentication and authorization checks MUST be enforced at a trusted server
  boundary, not only in a client or browser.
- Tokens, authorization codes, password-reset links, and session identifiers
  MUST be generated with a cryptographically secure random source and have
  appropriate entropy, expiry, audience/scope, and single-use semantics.
- Redirect and callback destinations MUST be allow-listed or exactly matched;
  never redirect to an unchecked caller-supplied URL.
- Credentials and tokens for different trust domains MUST remain separated and
  MUST NOT be exchanged or returned across those boundaries unless the design
  explicitly requires it.
- Missing credentials and insufficient permissions MUST fail closed with
  documented, non-sensitive errors.

### Unauthenticated Endpoints And Resource Use

- Unauthenticated endpoints that validate credentials or trigger expensive or
  upstream work MUST be rate-limited.
- Unauthenticated endpoints that create state MUST bound that state by size,
  lifetime, and ownership.
- Uploads, archives, parsers, regular expressions, and pagination limits MUST
  have explicit resource bounds appropriate to the threat model.

### Browser-Facing Surfaces

- MUST NOT load scripts, styles, redirects, or API/specification URLs from
  attacker-controlled input.
- Every untrusted value interpolated into HTML, JavaScript, CSS, or URLs MUST be
  encoded for its output context.
- Pages that collect credentials or sensitive data MUST prevent caching and
  framing and use an explicit Content Security Policy appropriate to the
  application.
- State-changing browser requests MUST use the project's approved CSRF defense.

### Input, Output, And Transport

- Treat HTTP, messaging, files, environment variables, databases, subprocess
  output, and upstream service responses as untrusted.
- Validate input type, length, range, format, and authorization before use.
- Encode URL path/query components and database/shell arguments using safe APIs;
  do not concatenate raw input.
- Sensitive errors MUST NOT reflect credentials, internal state, stack traces,
  or private network details.
- Production traffic carrying credentials or sensitive data MUST be protected
  in transit. Document trusted proxy/TLS boundaries explicitly.

### Dependencies, Build, And Deployment

- New top-level dependencies, package registries, build plugins, base images, or
  external services require explicit approval and security consideration.
- Dependency and image versions SHOULD be pinned according to the project's
  update policy.
- CI and deployment workflows MUST use least-privilege credentials and avoid
  exposing secrets to untrusted pull-request code.
- Production deployment and mutating live tests MUST require an explicit,
  auditable action.

### Tests And Review

- MUST NOT disable, skip, or weaken security-relevant tests to make a change
  pass.
- Changes to authentication, authorization, token/session handling, sensitive
  logging, external input, browser-facing surfaces, dependencies, or deployment
  SHOULD receive a security-focused review under `CODE_REVIEW.md`.
- Security regression tests SHOULD assert both the intended success path and
  the prohibited behavior.

## Supported Versions

<!-- TODO(template): replace with the real support policy. -->

| Version | Supported |
|---|---|
| Latest release | Yes |
| Older releases | No |

## Scope

<!-- TODO(template): customize in-scope and out-of-scope systems. -->

In scope: code in this repository and the way it directly uses its
dependencies.

Out of scope: unrelated third-party services, social engineering, and purely
volumetric denial-of-service reports.
