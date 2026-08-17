# Project Setup Checklist

Use this checklist immediately after creating a repository from the template.
The project is not ready for feature work until the required items are complete.

## Identity And Ownership

- [ ] Rename the project in `README.md`, `AGENTS.md`, package/build metadata,
      deployment configuration, and badges.
- [ ] Replace the project description, users, interfaces, runtime, frameworks,
      and deployment target.
- [ ] Replace `security@example.com` with a monitored private reporting
      channel.
- [ ] Set supported versions, repository owners, and licensing expectations.

## Repository Knowledge

- [ ] Replace the example repository tree in `AGENTS.md`.
- [ ] Document module responsibilities, public contracts, compatibility
      constraints, runtime profiles, and failure behavior.
- [ ] Complete `docs/architecture.md`.
- [ ] Add or link canonical API/schema/reference documentation.
- [ ] Record the first project-specific architectural decisions as ADRs.

## Developer Workflow

- [ ] Replace every placeholder command in `README.md`, `AGENTS.md`, and
      `CONTRIBUTING.md`.
- [ ] Make setup work from a fresh clone without undocumented local state.
- [ ] Populate `.env.example` with names and safe placeholder values only.
- [ ] Customize `.gitignore` for the selected stack and generated artifacts.
- [ ] Add repeatable scripts for common tasks where practical.

## Quality And Delivery

- [ ] Add CI that runs the same core checks documented for local development.
- [ ] Document whether merges publish or deploy, and require explicit approval
      for production or other destructive actions.
- [ ] Define the minimum handoff checks and additional checks for packaging,
      contracts, migrations, authentication, and deployment.
- [ ] Configure dependency/update policy and artifact retention.
- [ ] Verify the pull request template matches the actual checks.

## Security

- [ ] Replace generic security rules with concrete trust boundaries and
      authentication/session/token invariants.
- [ ] Define sensitive-data classes, allowed logs, retention, and redaction.
- [ ] Document TLS/proxy boundaries, unauthenticated endpoints, resource
      limits, browser defenses, and secret injection.
- [ ] Verify CI cannot expose secrets to untrusted pull-request code.
- [ ] Arrange an independent security-focused review before production use.

## Agent Compatibility

- [ ] Keep `AGENTS.md` canonical and delete tool shims the team does not use.
- [ ] Add tool-specific rules only when they genuinely differ.
- [ ] Test the repository instructions with at least one supported coding
      agent on a small, reversible task.
- [ ] Search for unresolved template markers:

```bash
rg -n 'TODO\(template\)|<[^>]+>|security@example\.com|YYYY-MM-DD'
```
