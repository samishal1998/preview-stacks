# AGENTS.md

Instructions for an AI agent changing **this codebase**. Using `pstack` is a different job; this is
about editing it.

**Current version: 0.26.0.** Three published packages, one workspace.

## Read this first

1. This file, end to end. The **Invariants** section is the part that matters — each entry is a rule
   plus the failure that produced it.
2. [`docs/README.md`](docs/README.md) — the documentation index, so you know what exists.
3. The **header comment of every file you are about to touch.** They are long on purpose and explain
   *why*, not *what*. Most questions you will have about a design decision are answered at the top of
   the file that made it. If a header contradicts this document, the header is newer — trust it and
   fix this file.

For a specific change, `docs/control-plane.md` explains the architecture and refuses several
plausible restructurings with reasons.

## What this is

A CLI + HTTP API + web UI that gives an ephemeral per-PR preview stack a **declarative lifecycle**:
named *isolation axes* (a database branch, a queue namespace, per-PR images, DNS) each with up to
four hooks, provisioned around a Docker Compose project, torn down in reverse, and then **proven
gone**.

**The differentiator is isolation-axis lifecycle + leak verification.** Everything else — the API,
the UI, notifiers, the terminal — is scaffolding around that. When a change makes the leak semantics
harder to state, it is a net loss even if it adds a feature.

## What this is not

| Not | Why it matters when you edit |
|---|---|
| **A PaaS.** Coolify / Dokploy / Uffizzi run a compose file per PR and terminate TLS. | Don't add git-webhook deploys (**inbound** — a push triggering a deploy; *outbound* notification webhooks are a different thing and shipped in 0.11.0) or a service catalog. pstack *does* now manage Traefik config and issue TLS, because a control plane that cannot explain why a hostname 404s is useless — but it does that for previews it owns, not as a general ingress product. |
| **Multi-tenant.** One spec set, one Docker socket, one trust level. | Every authenticated caller shares them. Accounts exist (0.10.0) for *attribution and credential hygiene*, not isolation. See *Scope discipline*. |
| **A sandbox.** Hooks are shell strings run via `bash -c` at CI trust level. | Sanitizing, escaping, or allow-listing hook content is a **category error**, not a hardening task. A spec is as trusted as a CI workflow file. Shell-quoting (`shq`) exists to stop *accidents* with spaces and quotes, not attackers. |
| **A reconciler.** No desired-state loop, no database *of stacks*. | Truth about the world lives in Docker and in each axis's `assert_*` probe — never in a row this process wrote. See invariant 10. |

## Repo map

```
packages/pstack/     @samyx/preview-stacks — the CLI, API and library. The product.
packages/client/     @samyx/preview-stacks-client — zero-dependency API client + verifyWebhook.
apps/ui/             @samyx/preview-stacks-ui — the advanced UI (Vue 3 SPA).
docs/                See docs/README.md.
```

### `packages/pstack/src` — by responsibility

**The core lifecycle** (read these first; the product is here):

| File | Responsibility |
|---|---|
| `spec.ts` | Parse + validate `preview.yml` → resolved `Stack`. Owns interpolation, the stack-name charset rule, axis dedupe, `warnings`. |
| `stack.ts` | `up` / `down` / `verify` / `status` / `report`. Owns the failure semantics — **the whole product is in this file**. |
| `compose.ts` | Builds `docker compose` command strings — or, when `spec.compose.orchestrator` is `swarm`, the `docker stack` ones from `swarm.ts`. Owns the all-profiles-on-down rule, `composeSleep` (down **without** `-v`) and `shq`. |
| `swarm.ts` | Docker Swarm: `swarmify` (plain compose → the v3 subset `docker stack deploy` accepts, faithfully, every change named), the `docker stack` command lines, node listing, and `joinMaterial`/`swarmReport` — shared by `GET /api/swarm/join` and `pstack swarm`, so the two cannot hand an operator different commands for one cluster. Its header states the ceilings (worker volumes, node-local exec). |
| `exec.ts` | The only place a process is spawned. Dry-run, output capture, `captureOutputs`, cancellation via `AbortSignal`. |
| `log.ts` | The `Sink` seam: `consoleSink` (CLI), `bufferSink` (API jobs), `nullSink` (tests). |

**The control plane:**

| File | Responsibility |
|---|---|
| `api.ts` | HTTP API + UI host. **Its header comment is the API's route list** — update it in the same edit as a route. Owns auth, the `:id` → spec-variable binding, SSE streams, the WebSocket upgrade. |
| `cli.ts` | Arg parsing, command dispatch, **exit codes**, the `serve` loopback interlock. Logic belongs in the module, not here. |
| `jobs.ts` | In-memory job registry: one in-flight job per stack, bounded to 50 transcripts, SSE fan-out, cancellation. |
| `registry.ts` | The deployment registry — a directory of YAML per deployment. Deliberately not a database (invariant 10). |
| `specs.ts` | Named specs: store once, reference from many deployments. |
| `scheduler.ts` | Sleep/wake: the `SleepIndex` (hostname → sleeping deployment, for the catch-all router), the `TrafficMeter` (Traefik's per-router counters → "last request"), the `Scheduler` tick (`idle`/`after`), and the spinning-up page. Everything it knows is in memory — invariant 10. |
| `share.ts` | Share links: an HS256 JWT signed with `PSTACK_TOKEN`. Sign, verify, and nothing stored. |
| `init.ts` | `pstack init` — stands up the control stack. CLI-only, permanently; its header explains why. |
| `upgrade.ts` | `pstack upgrade` and `pstack ui <mode>`. Reads back what `init` decided so nothing rotates. |
| `image.ts` | `pstack build-image` — builds the control image, pinned to the running CLI's version. |
| `cloudinit.ts` | `pstack cloud-init` — renders the boot user-data, multi-distro. |

**Observation and safety:**

| File | Responsibility |
|---|---|
| `inspect.ts` | What is actually running + what Traefik was told. Answers "why does the hostname 404". Never returns a raw `docker inspect` (it contains the container's whole environment). |
| `readiness.ts` | Post-deploy watch: containers → ready / failed / timedout. Observational only; it starts and repairs nothing. |
| `redact.ts` | Redaction for anything a human is shown. |
| `terminal.ts` | The container shell. **The most dangerous route in the codebase** — read its header before touching it. |
| `subdomains.ts`, `autolabel.ts`, `routing.ts` | Traefik wiring: wildcard routing, generated labels, dynamic-config files. |

**Persistence and delivery:**

| File | Responsibility |
|---|---|
| `store.ts` | SQLite (`<dataDir>/db/pstack.db`) + migrations. Append to `MIGRATIONS`; never edit a shipped one. |
| `auth.ts` | Accounts, sessions, personal tokens. Argon2id via `Bun.password`; sessions and tokens stored as SHA-256. |
| `hostvars.ts` | Host-level `${vars.*}` / `${secrets.*}`. |
| `registries.ts` | Private-registry credentials for image pulls. |
| `events.ts` | The domain event bus. `EVENTS` is a **public contract** — add, never rename. |
| `webhooks.ts` | Notifier registrations + the delivery log. |
| `notify.ts` | Delivery: the `NotifierType` seam, the per-notifier queue, retries, redelivery. |

`test/stack.test.ts` is the server suite (265 tests); `test/features.test.ts` covers swarm, sleep/wake
and share links (25, with fake `docker` shims that use `printf`, not `echo` — `sh`'s `echo` mangles
the backslashes in a `HostRegexp`). `packages/client/test/client.test.ts` drives the client against a
real in-process server — that is the anti-drift check for the SDK.

## Invariants — do not break these

Each has a reason. If you think one is wrong, say so in your response; don't quietly change it.

**1. `down` is best-effort; `verify` is strict.** `down`'s axis-hook failures are recorded with
`ok: true` and a `non-fatal:` message, and teardown continues. `verify`'s `assert_gone` failures are
fatal. *Why:* aborting teardown halfway leaves **more** garbage than continuing, but a teardown that
silently half-worked is the exact failure this tool exists to catch. Never make `down` throw; never
make `verify` lenient.

**2. Axes go forward on `up`, reverse on `down`.** `for (const axis of [...spec.axes].reverse())` in
`stack.ts`. Declaration order is dependency order. **The spread is load-bearing**: an in-place
`.reverse()` mutates the spec, and `api.ts` reuses one spec object across a request.

**3. `up` fails fast.** A half-provisioned stack must not proceed to deploy — an app started against
a missing database reports a confusing connection error instead of the real one.

**4. `down` passes EVERY compose profile.** Compose treats a non-enabled profile's services as
absent, so tearing down with fewer profiles than you deployed leaks that profile's resources — most
visibly one dead `<stack>_default` network per PR, forever. If you add per-invocation profile
selection, `down` must ignore it and use the spec's full list, **and that needs a new test** — the
current one would still pass.

**5. `down -v` never removes images.** Images are an axis, not compose's job.

**6. Interpolation happens exactly ONCE, at parse time.** `interpolate()` is called only from
`spec.ts` (verified: no other call sites). A resolved value containing `${...}` must never be
re-expanded. A hook's `$STACK` is expanded **by bash at run time** from the injected env — a
different mechanism. Do not "unify" the two; you'd double-expand.

**7. An undefined variable is a hard error.** `pr-${PR}` with `PR` unset would become `pr-`, which
**every** PR then shares — collision instead of isolation. **Empty string counts as undefined**;
that is deliberate.

**8. Exit code 2 means *leaked*, distinct from 1.**

| Code | Meaning | Owner |
|---|---|---|
| 0 | ok | — |
| 1 | operation failed | whoever broke the hook |
| 2 | **torn down, but something survived** | whoever owns the leaked resource |
| 3 | bad spec / usage | the spec author |

Leak detection is a **step scan**, not `outcome.ok`, and it appears in `cli.ts`, `jobs.ts` (twice)
and `stack.ts`: `steps.some(s => s.phase === 'assert_gone' && !s.ok)`. Add a leak-bearing phase and
you edit all of them.

**9. The API's loopback interlock has two halves** (`cli.ts`, `serve`). Without `PSTACK_TOKEN`: the
host is forced to `127.0.0.1`, **and** an explicit non-loopback `PSTACK_HOST` is a hard exit 3 rather
than a silent downgrade. An API that can delete databases must not be exposable by forgetting a flag.

**10. No state store FOR WHAT EXISTS.** Job records are transcripts of attempts, in memory,
unpersisted. Restarting the server loses history, not correctness.

*Amended in 0.10.0.* There **is** a SQLite database, and the line it holds is precise: accounts,
sessions, tokens, notifier registrations, delivery logs, terminal audit, host variables. Every one is
relational, secret-bearing, and has no source of truth in Docker to contradict. The deployment
registry stayed a directory of YAML an operator can read and repair over SSH. **If you find yourself
adding a table whose rows describe what is running, you are about to be wrong.**

**11. Tri-state fields never collapse to a boolean.** `busy`, `running`, `reachable`, `verified` are
`boolean | null`, and `null` means *could not determine* — an unresolved spec has no stack name to
look up, and a docker that did not answer is not the same fact as "nothing is running". Collapsing
either to `false` is how a UI reports a live stack as torn down.

**12. `init` and `upgrade` are CLI-only, permanently.** The API runs *inside* the control stack;
recreating that stack from a request kills the process mid-operation, and a broken image leaves the
host with no control plane and no remote way to repair it. Never add a route that calls them.

**13. The container name in a request is never trusted.** `docker exec`/`stop` accept any container
on the daemon — including Traefik (every preview on the host) and `pstack-control` itself (whose
filesystem is the database). Every route taking a container name matches it against the containers
that deployment owns and 404s otherwise. See `terminal.ts` and the container-action route.

**14. Event names and payload fields are add-only.** They live in stored notifier registrations;
renaming one silently stops deliveries for everyone subscribed. Same for the delivery envelope's four
fields — receivers verify a signature over those exact bytes.

**15. A secret's value has no read path.** Notifier signing secrets, host secrets and registry
passwords go in and never come back out. `Webhooks.get()`/`list()` mask; `rawConfigOf()` does not and
is for the delivery path only. Conflating them is how a masked value gets POSTed to a masked URL —
which happened.

**16. A share principal is closed by default.** `shareAllows` in `api.ts` runs right after the auth
gate and **before any route**: a `{ kind: 'share' }` principal reaches exactly the GETs its views
name, on its own deployment, with the stored variables only. A new route is unreachable to it until
someone lists it there. The raw `PSTACK_TOKEN` is never read from a query string — only a JWT is.

**17. Sleep never removes volumes; wake IS `up`.** `composeSleep` is `down` without `-v` (swarm:
`stack rm`, which never touches volumes), its own function rather than a flag so the `down -v` the
leak tests assert on cannot be weakened by a default. A wake runs `up()` exactly — axis hooks are
idempotent by contract and re-capture their outputs, so nothing is persisted between the two.

**18. Template substitution uses function replacements.** `String.replace(marker, string)` reads
`$$` in the replacement as one `$`, and the wake router's rule ends in `$$` precisely so compose
hands Traefik a literal `$`. `init.ts` passes `() => text`; keep it that way for every marker.

### Gotcha: dry-run proves ordering, never absence

Skipped steps carry `ok: true`, so a dry-run `down` prints ✓ for every `assert_gone`. That is
correct — nothing ran — but never read a green dry-run as "clean".

## Commands

```bash
bun install                    # required in a fresh clone

bun run check                  # THE GATE: build + test + typecheck, every package
bun test                       # all suites
bun run typecheck              # strict + noUncheckedIndexedAccess + noUnusedLocals/Parameters

cd packages/pstack && bun test # one package, faster loop
bun test test/stack.test.ts -t "readiness"   # one describe block

# Manual CLI runs. There is no root preview.yml, so pass -f; the example needs PR and GIT_SHA
# (an undefined variable is fatal — invariant 7).
PR=123 GIT_SHA=abc bun packages/pstack/src/cli.ts -f packages/pstack/examples/preview.yml validate
PR=123 GIT_SHA=abc bun packages/pstack/src/cli.ts -f packages/pstack/examples/preview.yml up -n -v
bun packages/pstack/src/cli.ts --help
bun packages/pstack/src/cli.ts --version

# A live server + UI, for driving the web interface.
PSTACK_TOKEN=dev PSTACK_DATA=/tmp/pstack-dev bun packages/pstack/src/cli.ts serve  # :7878
cd apps/ui && bun run dev                                                          # :5273, proxies /api
```

There is **no linter**. `bun run check` is the gate. No runtime dependencies in `packages/pstack` or
`packages/client` — keep it that way.

## Testing expectations

**Any change to lifecycle ordering, failure semantics, exit codes, or a security boundary needs a
test.** Patterns to copy, in order of how much they prove:

1. **Boot a real server** (`createServer({ dataDir, port: 0, … })`) and assert on whole response
   bodies. Most of `test/stack.test.ts` does this. Port 0 means tests run in parallel safely; always
   `server.stop(true)` in a `finally`.
2. **The real-filesystem leak test** — `touch` a file, declare an axis whose `down` lies (`"true"`)
   and whose `assert_gone` is `! test -e <file>`, assert `verify` fails, `rm` it, assert it passes.
   A fake runner cannot prove the gate catches a survivor.
3. **A fake `docker` on `PATH`** — a shell script writing scripted JSON, for anything reading
   container state. Several tests make it *mutable* between polls to exercise transitions.
4. **`fakeRunner(failPredicate?, stdout?)`** — records commands into `.log`. For ordering and flow.

Assert on `Outcome.steps` (phase / ok / message), not on printed output — `report()` is presentation.

### The rule that matters most

**Write the negative control.** After a test passes, break the code it covers and confirm the test
fails. Several bugs in this repo's history shipped behind green tests that asserted nothing:

- A scrub test whose fixture never contained the secret.
- A UI-detection test using a fixture with a service name **I invented**, while the generator emitted
  a different one — so `upgrade` removed the advanced UI on every host and the test said fine. The
  fix was to generate the fixture with the real `init`.

If a test cannot fail, it is documentation with a misleading name.

## How to add things

### A new hook type

The four-hook tuple is hardcoded in several places. All of them:

1. `spec.ts` — the `Axis` type, a `field('<hook>')` call, the empty-axis guard.
2. `stack.ts` — `StepResult['phase']`, and the call site **with explicit failure semantics**
   (fatal or recorded? invariant 1).
3. `cli.ts` — the hook list in `validate`.
4. `api.ts` — the same literal in the spec-summary mapping.
5. If it can indicate a leak: the step scan in `cli.ts`, `jobs.ts` **and** `stack.ts` (invariant 8).
6. Docs: `docs/usage.md`, `examples/preview.yml`.
7. A test.

### A new CLI command

`cli.ts`: add the `case`, add it to the `COMMANDS` set (an unknown command must fail as *unknown*,
not by hunting for a spec file — that bug shipped), add a line to `usage()`, add flags to `parseArgs`
**and** `usage()`, and exit with a code from the table. Keep the logic in a module; `cli.ts` is argv,
dispatch and exit codes only. Then `docs/usage.md`.

### A new API route

`api.ts`: add the route, **update the route list in the file header**, and put it inside the shared
`try` so its errors map to 400/409 rather than a 500 HTML page. Long operations return `202 { job }`,
never a held-open socket; one in-flight job per stack, 409 on conflict. Reads that start something
(a readiness watch) must not emit events — a page view must not manufacture a notification. Then the
UI if it consumes it, and `packages/client` if a script would want it.

### A new event

`events.ts` (`EVENTS`), a chat line in `notify.ts`'s `summarize`, the emit site, the catalogue in
`docs/webhook-events.md`, and a test. Add-only — invariant 14.

### A new lifecycle action

`sleep`/`wake` are the template. `JobAction` in `jobs.ts`; the branch in `startLifecycle` (`api.ts`)
— the ONE place jobs start, shared by the POST route, the wake dispatch and the scheduler; the
lifecycle regex on the `:id` route; `actionWord` in `notify.ts`; `LifecycleAction` + `ACTION_LABELS`
in both UIs; the client SDK's method and `JobAction`; `docs/webhook-events.md` (`job.started`'s
`action`). If it can leave a leak behind, the step scan in three files (invariant 8).

### A new notifier type

One entry in `TYPES` (`notify.ts`): `{ kind, label, signs, fields, validate, send }`. No schema
migration, no route change, no UI change — the UI renders `fields` from `/api/notifiers/meta`. Slack
and Discord cost one factory and two registrations; if a new type needs more than that, the seam is
being worked against.

### A database change

Append to `MIGRATIONS` in `store.ts`. **Never edit a shipped migration** — it will not re-run
anywhere it already ran. The array index is the version.

### UI work

Read [`docs/ui-rules.md`](docs/ui-rules.md) first — casing, one control height, one radius scale,
full-width pages, container queries for tables. Then **look at it in a browser**: several defects in
this UI's history were invisible in code review and obvious in a screenshot (a stale "Healthy" beside
"Exited", buttons that took a click and did nothing, columns painted over the panel beside them).

## Scope discipline

**Prefer deleting over adding.** Deliberate non-goals, and what each would actually require:

- **Multi-tenancy.** Needs a per-tenant isolation boundary — separate VMs/microVMs or Kubernetes
  namespaces — plus a credential boundary. That is a different product. Do not add tenant IDs or RBAC
  to `api.ts` as a substitute.
- **Untrusted specs.** Same boundary problem, plus hooks are shell strings by design.
- **Inbound git-webhook deploys, a service catalog.** Use a PaaS.
- **Persistence / reconciliation of what exists.** Invariant 10.

Before adding anything, check whether an existing axis hook already expresses it. Most requests
("clean up my registry tags", "warm a cache", "run migrations") are a spec's `up`/`down`, not code.

## Releasing

Lockstep across all three packages, from the repo root:

```bash
bunx publish-kit bump patch|minor    # bumps every package
bun run check                        # must be green
git commit && git tag vX.Y.Z && git push origin main --tags
```

**Publishing to npm is the maintainer's, not yours.** Never run `release:publish`. Version numbers
appear in code (`upgrade.ts` messages, docs referencing "since 0.X.0") — grep for the old version
after a bump.

## Working style in this repo

- **Verify, don't assume.** Read the generated output rather than the code that generates it; boot
  the server rather than reasoning about the route. Every serious bug here came from a confident
  assumption about a string somewhere else.
- **Say what you did not do.** A partial fix reported as complete is worse than a partial fix.
- **The file headers are the design record.** When you change a decision, change the header comment
  that explains it in the same edit — a stale header is worse than none, because the next reader
  trusts it.
