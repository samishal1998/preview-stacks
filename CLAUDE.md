# CLAUDE.md

**Read [AGENTS.md](AGENTS.md) first.** It is the full working context for changing this codebase:
invariants, repo map, testing expectations, how to add a hook / command / route / event, and scope
discipline.

This file exists only so an agent that looks for `CLAUDE.md` finds its way there. It is deliberately
a pointer and not a copy — two documents describing the same rules drift, and the stale one is the
one that gets trusted. What follows is only what is specific to Claude Code.

Doc index: [docs/README.md](docs/README.md). Version control, never-commit and durable-artifact
rules: AGENTS.md (§Version control, §Agent workflow).

## Skills

Project skills live in `.claude/skills/`. Each `SKILL.md` is the authority; this is when to load one.

| Skill | Load when |
|---|---|
| `pstack-release` | the owner asks for a release: version bump, tag, `release.yml`, and proof npm published |
| `pstack-ship-stack` | committing, pushing, opening draft PRs for, or merging a branch or a stack of branches |
| `pstack-plan-build` | a feature too big for one PR: spec, task plan in `docs/`, pre-flight conflict scan, rulings ledger, task-by-task build with review |
| `pstack-ui-verify` | an `apps/ui` or embedded-UI change needs a real browser: fake `docker`, `pstack serve`, headless Chrome |

[`skills/pstack/SKILL.md`](skills/pstack/SKILL.md) is a different kind: the published skill that
teaches an agent to *use* pstack, not to change it.
