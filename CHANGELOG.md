# Changelog

## 0.1.0 — unreleased

First release. Declarative lifecycle for ephemeral per-PR preview stacks.

### Core

- **`preview.yml` spec** — a stack identity plus ordered **isolation axes**, each with up to four
  hooks (`up`, `assert_live`, `down`, `assert_gone`). Axes provision in declaration order and
  destroy in reverse.
- **Leak verification** — `down` is best-effort (aborting halfway leaves more garbage), `verify` is
  strict and exits **2** when a resource survived teardown. Axes without `assert_gone` report
  `unverifiable` rather than passing silently.
- **Interpolation happens once**, at parse time. An undefined variable is a hard error, because
  `pr-${PR}` with `PR` unset silently becomes `pr-` — shared by every PR.
- **Compose integration** — `up` enables the selected profiles; `down` enables **every** profile,
  which is what stops one dead `<stack>_default` network accumulating per PR.
- **Assert linting** in `pstack validate` — flags a bare `! <probe>` with no reachability guard
  (it reports "gone" when the probe itself fails), `|| true` inside an assert, and any axis with
  `up` but no `assert_gone`.

### CLI

`pstack up | down | verify | status | validate | serve`, with `--dry-run`, `--set K=V`,
`--no-verify`, `-v/-q`. Exit codes: `0` ok · `1` failed · `2` leaked · `3` bad spec/usage.

### API + UI

- `pstack serve` — HTTP API over the same core. Mutating routes are asynchronous (`202` + job id,
  poll or subscribe to SSE) because `up`/`down` take minutes. One job per stack; a concurrent
  request gets `409`.
- Two security guards: a bearer token (`PSTACK_TOKEN`) on mutating routes, and a refusal to bind
  anything but loopback when no token is set.
- A single-file Vue 3 UI (no build step) for submitting, monitoring and tearing down stacks.

### Docs

`README.md` (design), `docs/usage.md` (task guide), `docs/bootstrap.md` (build a host from
scratch — Hetzner + cloud-init), `AGENTS.md` (working on this codebase), `skills/pstack/` (a skill
teaching an agent to use pstack).

### Known limitations

- **Bun-only** (`Bun.serve`/`file`/`YAML`/`spawn`). Not a Node package; bundling would not change that.
- **Not multi-tenant, not a sandbox.** Hooks are shell strings at CI trust level.
- **No memory isolation** — set `mem_limit` per service yourself; one greedy heap can OOM every
  stack on a shared host.
- The spec has not yet been proven against a second, independent project.
