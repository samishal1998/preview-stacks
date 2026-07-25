# AGENTS.md

Instructions for an AI agent changing **this codebase**. Using `pstack` is the skill's job; this is
about editing it.

Read [README.md](README.md) (design doc), then [`src/spec.ts`](src/spec.ts) and
[`src/stack.ts`](src/stack.ts). Every file has a header comment explaining *why*, not just what —
read it before editing that file.

## What this is

A CLI + HTTP API that gives an ephemeral per-PR preview stack a **declarative lifecycle**: named
*isolation axes* (a database branch, a queue namespace, per-PR images, DNS) each with up to four
hooks, provisioned around a Docker Compose project, torn down in reverse, and then **proven gone**.

**The differentiator is isolation-axis lifecycle + leak verification.** Nothing else here is novel.

## What this is not

| Not | Why it matters when you edit |
|---|---|
| **A PaaS.** Coolify / Dokploy / Uffizzi run a compose file per PR and terminate TLS. | Don't add ingress management, TLS issuance, git-webhook deploys, or a service catalog. If a user only needs that, the README tells them to use a PaaS. |
| **Multi-tenant.** One spec, one Docker socket, one trust level. | Every caller of the API shares them. See *Scope discipline*. |
| **A sandbox.** Hooks are shell strings run via `bash -c` at CI trust level. | Sanitizing, escaping, or allow-listing hook content is a **category error**, not a hardening task. A spec is as trusted as a CI workflow file. |
| **Stateful.** No database, no state file, no reconciliation loop. | Truth lives in Docker and in each axis's `assert_*` probe. |

## Repo map

| Path | Responsibility |
|---|---|
| `src/spec.ts` | Parse + validate `preview.yml` → resolved `Stack`. Owns interpolation, the stack-name charset rule, axis dedupe, and `warnings`. |
| `src/stack.ts` | Lifecycle orchestration: `up` / `down` / `verify` / `status` / `report`. Owns the failure semantics — the whole product is in this file. |
| `src/compose.ts` | Builds `docker compose` command strings. Owns the all-profiles-on-down rule and `shq` shell quoting. |
| `src/exec.ts` | The only place a process is spawned. Owns dry-run, output capture, `captureOutputs` (`KEY=VALUE` from hook stdout), `maskSecrets`. |
| `src/log.ts` | The `Sink` seam: `consoleSink` (CLI), `bufferSink` (API jobs), `nullSink` (tests). |
| `src/cli.ts` | Arg parsing, command dispatch, **exit codes**, and the `serve` loopback interlock. |
| `src/api.ts` | HTTP API + static file host. Owns auth, the `:id` → spec-variable binding, and route shapes (routes listed in its header). |
| `src/jobs.ts` | In-memory job registry: one in-flight job per stack, bounded to 50 transcripts, SSE subscriber fan-out. |
| `test/stack.test.ts` | The whole suite. |
| `examples/preview.yml` | Fully-commented four-axis spec. Doubles as the manual smoke-test fixture. |
| `ui/index.html` | Static dir served by `api.ts` behind a traversal guard. **Currently a 46-byte placeholder** — the serving seam exists, the UI does not. |
| `package.json` | Scripts (`test` / `typecheck` / `check`) and `bin: pstack` → `src/cli.ts`. No runtime dependencies; keep it that way. |
| `tsconfig.json` | The strictness settings the code is written against (see *Commands*). |
| `README.md` | The design doc and the user-facing surface. A sync target for any spec/CLI/API change. |

## Invariants — do not break these

Each has a reason. If you think one is wrong, say so in your response; don't quietly change it.

**1. `down` is best-effort; `verify` is strict.**
`down`'s axis-hook failures are recorded with `ok: true` and a `non-fatal:` message, and teardown
continues. `verify`'s `assert_gone` failures are fatal. *Why:* aborting teardown halfway leaves
**more** garbage than continuing, but a teardown that silently half-worked is the exact failure this
tool exists to catch. Never make `down` throw; never make `verify` lenient.

**2. Axes go forward on `up`, reverse on `down`.**
`for (const axis of [...spec.axes].reverse())` in `stack.ts`. *Why:* declaration order is dependency
order — database before the app that migrates it, so dependents die first. **The spread is
load-bearing**: an in-place `.reverse()` mutates the spec, and `api.ts` reuses one spec object across
a request.

**3. `up` fails fast.**
Unlike teardown, a half-provisioned stack must not proceed to deploy — an app started against a
missing database reports a confusing connection error instead of the real one.

**4. `down` passes EVERY compose profile.**
Compose treats a non-enabled profile's services as absent, so tearing down with fewer profiles than
you deployed leaks that profile's resources — most visibly one dead `<stack>_default` network per PR,
forever. Passing a profile with no matching service is a free no-op.
The existing test catches a direct removal (it asserts both `--profile` flags on the down command),
but the **asymmetry** — up with a subset, down with all — is unexercisable today, because
`composeUp` and `composeDown` both read the same `c.profiles` and nothing can make `up` pass a
subset. **If you add per-invocation selection (e.g. a `--profile` flag), `down` must ignore it and
use the spec's full list, and that needs a new test** — the current one would still pass.

**5. `down -v` never removes images.** Images are an axis, not compose's job. Don't "fix" this in
`compose.ts`.

**6. Interpolation happens exactly ONCE, at parse time.**
`interpolate()` is called only from `spec.ts`. *Why:* a resolved value containing `${...}` must never
be re-expanded downstream. A hook's `$STACK` is expanded **by bash at run time** from the injected
env — a different mechanism. Do not "unify" the two; you'd double-expand. Never call `interpolate`
outside `spec.ts`.

**7. An undefined variable is a hard error.**
`pr-${PR}` with `PR` unset would become `pr-`, which **every** PR then shares — collision instead of
isolation. Note **empty string counts as undefined** (`v === undefined || v === ''`); that is
deliberate, not a bug.

**8. Exit code 2 means *leaked*, distinct from 1.**

| Code | Meaning | Owner |
|---|---|---|
| 0 | ok | — |
| 1 | operation failed | whoever broke the hook |
| 2 | **torn down, but something survived** | whoever owns the leaked resource |
| 3 | bad spec / usage | the spec author |

CI must be able to tell 1 from 2. Leak detection is a **step scan**, not `outcome.ok`, and it is
duplicated in two places — `cli.ts` (`down` case) and `jobs.ts` (`job.state = 'leaked'`):
`steps.some(s => s.phase === 'assert_gone' && !s.ok)`. Add a leak-bearing phase and you edit both.

**9. The API's loopback interlock has two halves** (`cli.ts`, `serve` case). Without `PSTACK_TOKEN`:
the host is forced to `127.0.0.1`, **and** an explicit non-loopback `PSTACK_HOST` is a hard exit 3
rather than a silent downgrade. *Why:* an unauthenticated API that can delete databases must not be
exposable by forgetting a flag. `authed()` returning `true` when no token is set is the local-dev
mode, not a hole to harden away.

**10. No state store.** Job records are transcripts of attempts, in memory, unpersisted. Restarting
the server loses history, not correctness. Don't add a database, a lock file, or a "desired state"
table — truth is Docker + the `assert_*` probes, and there's nothing to get out of sync.

### Gotcha: dry-run proves ordering, never absence

Skipped steps carry `ok: true`, so a dry-run `down` prints ✓ for every `assert_gone`. That is
correct — nothing ran — but never read a green dry-run as "clean".

## Commands

```bash
bun test              # 22 tests, ~40ms, incl. a real-filesystem leak test
bunx tsc --noEmit     # strict + noUncheckedIndexedAccess + noUnusedLocals/Parameters
bun run check         # bun test && tsc --noEmit  ← the gate. No linter in this repo.

# Manual runs. There is no root preview.yml, so pass -f; the example needs PR and GIT_SHA
# (env.IMAGE_TAG is ${GIT_SHA}, and an undefined variable is fatal — invariant 7).
PR=123 GIT_SHA=abc123 bun src/cli.ts -f examples/preview.yml validate
PR=123 GIT_SHA=abc123 bun src/cli.ts -f examples/preview.yml down --dry-run
PR=123 GIT_SHA=abc123 bun src/cli.ts -f examples/preview.yml up -n -v
bun src/cli.ts --help

PSTACK_TOKEN=dev bun src/cli.ts -f examples/preview.yml serve   # API on 127.0.0.1:7878
```

`tsconfig` is strict with `noUncheckedIndexedAccess`, so indexing yields `T | undefined`. Prefer
narrowing (`const key = m?.[1]; if (key !== undefined)`) over `!` where a mistake is plausible; the
existing `!` uses are on regex groups guaranteed by a preceding match.

## Testing expectations

**Any change to lifecycle ordering, failure semantics, or exit codes needs a test in
`test/stack.test.ts`.** Two patterns to copy:

1. **`fakeRunner(failPredicate?, stdout?)`** — records every command into `.log` and returns scripted
   results. Use for ordering and flow: `expect(r.log).toEqual(['echo rm-images', 'echo rm-queue',
   'echo rm-db'])`. The failure predicate is how you assert best-effort vs fail-fast — that a failing
   step *did* or *did not* stop the ones after it.
2. **The real-filesystem leak test** (bottom of `test/stack.test.ts`) — `touch` a file, declare an
   axis whose `down` lies (`"true"`) and whose `assert_gone` is `! test -e <file>`, assert `verify`
   fails, `rm` the file, assert it passes. Copy this for anything touching leak semantics; a
   fake runner cannot prove the gate actually catches a survivor.

Assert on `Outcome.steps` (phase / ok / message), not on printed output — `report()` is presentation.

## How to add a feature

### A new hook type

The four-hook tuple is hardcoded in several places. All of them:

1. `src/spec.ts` — the `Axis` type, a `field('<hook>')` call in `parseSpec`, and the
   "defines no up/down/…" empty-axis guard.
2. `src/stack.ts` — `StepResult['phase']`, and its call site in `up`/`down`/`verify` **with explicit
   failure semantics** (fatal or recorded? invariant 1).
3. `src/cli.ts` — the `['up','down','assert_gone','assert_live'] as const` list in `validate`.
4. `src/api.ts` — the same literal in `/api/spec`'s `hooks` mapping.
5. If it can indicate a leak: the exit-2 step scan in **both** `cli.ts` and `jobs.ts` (invariant 8).
6. Docs: README's hook table, `examples/preview.yml`, and the external skill.
7. A test.

### A new CLI command

`src/cli.ts`: add the `case` to the `switch`, a line to `usage()`, and an explicit `process.exit`
with a code from the table above. Add flags to `parseArgs` **and** `usage()`. Then README's usage
block and the skill. Keep the logic in `stack.ts` — `cli.ts` is argv, dispatch, and exit codes only.

### A new API route

`src/api.ts`: add the route, **update the route list in the file header** (it's the API's
documentation), and decide auth — anything mutating must sit behind the `mutating && !authed(req)`
check. Long operations return a job (202 + job id), never a held-open socket; one in-flight job per
stack, 409 on conflict. Then the skill, and `ui/` if it consumes it.

## Scope discipline

**Prefer deleting over adding.** The value here is a small, comprehensible tool with sharp failure
semantics. A feature that makes the semantics harder to state is a net loss.

Deliberate non-goals, and what each would actually require:

- **Multi-tenancy.** Needs a per-tenant isolation boundary — separate VMs/microVMs or Kubernetes
  namespaces — plus a spec-per-tenant and a credential boundary. That is a different product. An
  HTTP API and a UI seam exist; **multi-tenancy does not, and must not be added without that
  boundary.** Do not add tenant IDs, per-user auth, or RBAC to `api.ts` as a substitute.
- **Untrusted specs.** Same boundary problem, plus hooks are shell strings by design (see *What this
  is not*).
- **PaaS features** — ingress config, TLS issuance, webhook-driven deploys, a service catalog.
- **Persistence / reconciliation.** See invariant 10.

Before adding anything, check whether an existing axis hook already expresses it. Most requests
("clean up my registry tags", "warm a cache", "run migrations") are a spec's `up`/`down`, not code.

## Known doc drift

README's **§Scope** still lists the HTTP API and web UI as "not yet, deliberately" and points at a
`ponytail:` note in `stack.ts` that `log.ts` has since replaced. `api.ts`/`jobs.ts`/`serve` exist;
the UI is a placeholder. Fix it when you next touch the README — and treat any change to spec keys,
CLI flags, or API routes as a sync target for README **and** the user-facing skill, which lives
outside this repo.
