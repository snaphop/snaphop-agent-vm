# CLAUDE.md

@AGENTS.md

## Notes for Claude Code

The line above imports `AGENTS.md` using Claude Code's `@` import syntax.
Treat `AGENTS.md` as the **canonical source of truth** for setup, commands,
code style, testing, git conventions, and forbidden actions. Follow
`SECURITY.md` for hard security boundaries and `CODE_REVIEW.md` when reviewing
code. **If this file conflicts with `AGENTS.md`, `AGENTS.md` wins.**

For requests covering every open bug, use the repository-local
`.agents/skills/review-bugs/SKILL.md` workflow when it is available in the
client. For reviewing, validating, or clarifying every enhancement request,
use `.agents/skills/review-enhancements/SKILL.md`. For reviewing, testing,
merging, and cleaning up outstanding pull requests, use
`.agents/skills/review-prs/SKILL.md`. For complete repository documentation
audits, use `.agents/skills/review-documentations/SKILL.md`.

Add Claude-Code-specific overrides below this line *only* if they genuinely
diverge from `AGENTS.md` — otherwise leave it empty so behavior stays
consistent across tools.

<!-- Claude Code overrides (optional): -->
