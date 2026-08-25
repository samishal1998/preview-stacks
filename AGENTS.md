# AGENTS.md

Instructions for an AI agent changing **this codebase**. Using `pstack` is a different job; this is
about editing it.

**Current version: 0.29.0.** One workspace: the Go binary (`packages/pstack`, released on GitHub),
its black-box specification (`packages/conformance`), and two npm packages (the client SDK, the
advanced UI). The control plane was a Bun/TypeScript package until 0.28.0; 0.29.0 is the Go port,
byte-compatible with it (`docs/port-status.md`).

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
packages/pstack/       the CLI, API and embedded basic UI — one static Go binary. The product.
                       cmd/pstack (main), internal/<pkg> (everything), assets.go (the five embeds),
                       ui/, templates/, examples/, package.json (the lockstep version of record).
packages/conformance/  the black-box specification (bun:test): goldens + tests that spawn bin/pstack.
packages/client/       @samyx/preview-stacks-client — zero-dependency API client + verifyWebhook.
apps/ui/               @samyx/preview-stacks-ui — the advanced UI (Vue 3 SPA).
docs/                  See docs/README.md.
```

The root `go.mod` (`module github.com/samishal1998/preview-stacks`, go 1.23) holds four
dependencies, each justified: `modernc.org/sqlite` (pure Go, so `CGO_ENABLED=0` is a static
binary), `github.com/goccy/go-yaml` (a PARSER only — `internal/yamlx` resolves scalars itself),
`github.com/coder/websocket`, `golang.org/x/crypto` (argon2). `net/http` only; the CLI parser is
hand-rolled. Do not add a module for what a few lines do.

### `packages/pstack/internal` — by responsibility

One package per responsibility, named as the reference's files were (the port kept the map):

**The core lifecycle** (read these first; the product is here):

| File | Responsibility |
|---|---|
| `spec` | Parse + validate `preview.yml` → resolved `Stack`. Owns interpolation, the stack-name charset rule, axis dedupe, `Warnings` (on the result — there is no module global). `subdomains.go` is the wildcard routing. |
| `stack` | `Up` / `Down` / `Verify` / `Status` / `Report`. Owns the failure semantics — **the whole product is in this package**. `Outcome.Leaked()` is THE leak scan, the one copy. |
| `compose` | Builds `docker compose` command strings — or, when `spec.compose.orchestrator` is `swarm`, the `docker stack` ones from `swarm`. Owns the all-profiles-on-down rule, `ComposeSleep` (down **without** `-v`) and `Shq`. |
| `swarm` | Docker Swarm: `Swarmify` (plain compose → the v3 subset `docker stack deploy` accepts, faithfully, every change named), the `docker stack` command lines, node listing, and `JoinMaterial`/`SwarmReport` — shared by `GET /api/swarm/join` and `pstack swarm`, so the two cannot hand an operator different commands for one cluster. The leaf of the compose/autolabel/swarm triangle. |
| `exec` | The only place a hook is spawned (`bash -c`, env as a REPLACEMENT, SIGTERM on cancel). Dry-run, output capture, `CaptureOutputs`, the `Runner` seam and its `Fake`. |
| `log` | The `Sink` seam: `Writer` (CLI), `Buffer` (API jobs), `Null` (tests). |

**The control plane:**

| File | Responsibility |
|---|---|
| `api` | HTTP API + UI host. **`server.go`'s header comment is the API's route list** — update it in the same edit as a route. `routes*.go` is the ordered if-chain, `principal.go` the gate, `sse.go`/`ws.go` the streams. Owns the `:id` → spec-variable binding. |
| `cli` + `cmd/pstack` | Arg parsing (`args.go`, the usage text byte for byte), command dispatch (`run.go`), **exit codes**, the `serve` loopback interlock, `healthcheck` (the container HEALTHCHECK — one GET, exit 0/1). `main.go` is one call. Logic belongs in the package, not here. |
| `jobs` | In-memory job registry: one RUNNING job per stack (a real mutex held across the check-then-act) plus a queue one deep where the newest replaces the queued one, a global concurrency cap (`PSTACK_MAX_JOBS`, 4), bounded to 50 transcripts, subscriber fan-out outside the lock, cancellation per job and per stack. |
| `registry` | The deployment registry — a directory of YAML per deployment. Deliberately not a database (invariant 10). |
| `specs` | Named specs: store once, reference from many deployments. |
| `scheduler` | Sleep/wake: the `SleepIndex` (hostname → sleeping deployment, for the catch-all router), the `TrafficMeter` (Traefik's per-router counters → "last request"), the `Scheduler` tick (`idle`/`after`), and the spinning-up page. Everything it knows is in memory — invariant 10. |
| `share` | Share links: an HS256 JWT signed with `PSTACK_TOKEN`. Sign, verify, and nothing stored. |
| `initctl` | `pstack init` — stands up the control stack. CLI-only, permanently; its header explains why. |
| `upgrade` | `pstack upgrade` and `pstack ui <mode>`. Reads back what `init` decided so nothing rotates. |
| `image` | `pstack build-image` — builds the control image, pinned to the running CLI's version. |
| `cloudinit` | `pstack cloud-init` — renders the boot user-data, multi-distro. |

**Observation and safety:**

| File | Responsibility |
|---|---|
| `inspect` | What is actually running + what Traefik was told. Answers "why does the hostname 404". Never returns a raw `docker inspect` (it contains the container's whole environment). |
| `readiness` | Post-deploy watch: containers → ready / failed / timedout. Observational only; it starts and repairs nothing. |
| `redact` | Redaction for anything a human is shown. |
| `terminal` | The container shell. **The most dangerous route in the codebase** — read its header before touching it. |
| `spec/subdomains.go`, `autolabel`, `routing` | Traefik wiring: wildcard routing, generated labels, dynamic-config files. |

**Persistence and delivery:**

| File | Responsibility |
|---|---|
| `store` | SQLite (`<dataDir>/db/pstack.db`) + migrations. Append to `Migrations`; never edit a shipped one. Inside `Tx` use only the handed `Querier` (one pooled connection). |
| `auth` | Accounts, sessions, personal tokens — and the SSO side of accounts: the stored provider, the `(provider, subject)` links, and `SsoSignIn`. Argon2id with a PHC codec that PARSES m/t/p (`phc.go`); sessions and tokens stored as SHA-256. |
| `sso` | The OIDC/OAuth2 protocol, and only that: presets, discovery (cached per `Client`), PKCE, the token exchange, ID-token verification (RS256/ES256, stdlib), claim mapping, the `TransientStore`. Touches no accounts. |
| `hostvars` | Host-level `${vars.*}` / `${secrets.*}`. |
| `registries` | Private-registry credentials for image pulls. |
| `events` | The domain event bus. `Names` is a **public contract** — add, never rename. Listeners run inline, in registration order; `Data` is marshalled once. |
| `webhooks` | Notifier registrations + the delivery log. |
| `notify` | Delivery: the `NotifierType` seam, the per-notifier queue, retries, redelivery. |

Foundations under `internal/` with no product logic: `omap` (the ordered map every document is),
`yamlx` (the YAML-1.2-core parser), `jsonx` (`JSON.stringify` semantics), `js` (`.length`,
`Number()`, `encodeURIComponent`, `URLSearchParams`…), `version`, `testfacts`. Every one is tested
against `packages/conformance/golden/facts` — what the reference runtime measurably did.

Every package has a `_test.go` beside it (the former in-process suite, ~330 tests). The black-box
suite is `packages/conformance` (214 tests: every route group, 80 CLI transcripts, a complete host
fixture). `packages/client/test/client.test.ts` drives the client against the spawned binary — that
is the anti-drift check for the SDK.

## Invariants — do not break these

Each has a reason. If you think one is wrong, say so in your response; don't quietly change it.

**1. `down` is best-effort; `verify` is strict.** `down`'s axis-hook failures are recorded with
`ok: true` and a `non-fatal:` message, and teardown continues. `verify`'s `assert_gone` failures are
fatal. *Why:* aborting teardown halfway leaves **more** garbage than continuing, but a teardown that
silently half-worked is the exact failure this tool exists to catch. Never make `down` throw; never
make `verify` lenient.

**2. Axes go forward on `up`, reverse on `down`.** `Down` walks a reversed COPY of `st.Axes` in
`internal/stack`. Declaration order is dependency order. **The copy is load-bearing**: reversing in
place would mutate the spec, and `internal/api` reuses one resolved spec across a request.

**3. `up` fails fast.** A half-provisioned stack must not proceed to deploy — an app started against
a missing database reports a confusing connection error instead of the real one.

**4. `down` passes EVERY compose profile.** Compose treats a non-enabled profile's services as
absent, so tearing down with fewer profiles than you deployed leaks that profile's resources — most
visibly one dead `<stack>_default` network per PR, forever. If you add per-invocation profile
selection, `down` must ignore it and use the spec's full list, **and that needs a new test** — the
current one would still pass.

**5. `down -v` never removes images.** Images are an axis, not compose's job.

**6. Interpolation happens exactly ONCE, at parse time.** `Interpolate()` is called only from
`internal/spec` (verified: no other call sites). A resolved value containing `${...}` must never be
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

Leak detection is a **step scan**, not `Outcome.OK`: `Outcome.Leaked()` in `internal/stack` — a
step with `Phase == PhaseAssertGone && !OK` — and it is the ONE copy (the reference had four; one
function agrees with itself). `cli`, `jobs` and the wake page all call it. Add a leak-bearing
phase and you edit it.

**9. The API's loopback interlock has two halves** (`internal/cli`, `Serve`). Without `PSTACK_TOKEN`: the
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
that deployment owns and 404s otherwise. See `internal/terminal` and the container-action route.

**14. Event names and payload fields are add-only.** They live in stored notifier registrations;
renaming one silently stops deliveries for everyone subscribed. Same for the delivery envelope's four
fields — receivers verify a signature over those exact bytes.

**15. A secret's value has no read path.** Notifier signing secrets, host secrets and registry
passwords go in and never come back out. `Webhooks.get()`/`list()` mask; `rawConfigOf()` does not and
is for the delivery path only. Conflating them is how a masked value gets POSTed to a masked URL —
which happened.

**16. A share principal is closed by default.** `shareAllows` in `internal/api` runs right after the auth
gate and **before any route**: a `{ kind: 'share' }` principal reaches exactly the GETs its views
name, on its own deployment, with the stored variables only. A new route is unreachable to it until
someone lists it there. The raw `PSTACK_TOKEN` is never read from a query string — only a JWT is.

**17. Sleep never removes volumes; wake IS `up`.** `composeSleep` is `down` without `-v` (swarm:
`stack rm`, which never touches volumes), its own function rather than a flag so the `down -v` the
leak tests assert on cannot be weakened by a default. A wake runs `up()` exactly — axis hooks are
idempotent by contract and re-capture their outputs, so nothing is persisted between the two.

**18. Template substitution is literal.** The wake router's rule ends in `$$` precisely so compose
hands Traefik a literal `$`. `internal/initctl` substitutes each marker with
`strings.Replace(s, marker, block, 1)` and cloud-init with `strings.ReplaceAll` over an ordered
list — never `regexp.ReplaceAllString` or `text/template`, to both of which `$` means something.
(The reference had the inverse hazard: JS `String.replace` reads `$$` as one `$`.)

**19. An SSO identity is `(providerKey, subject)`, and an email only ever ADOPTS.** `internal/auth`
`SsoSignIn` looks up the link first; the email branch exists solely to take over a *pre-existing
local* account and is gated on `emailVerified === true` and on exactly one match. Emails move
between people and subjects do not, so keying on the address — or relaxing the verified check, or
adopting the first of several matches — is how one person signs in as another. `emailAllowed` fails
CLOSED for the same reason: a non-empty allow-list plus no address is a refusal.

### Gotcha: dry-run proves ordering, never absence

Skipped steps carry `ok: true`, so a dry-run `down` prints ✓ for every `assert_gone`. That is
correct — nothing ran — but never read a green dry-run as "clean".

## Commands

```bash
bun install                    # required in a fresh clone (the UI, the client, the conformance suite)

bun run check                  # THE GATE: build + test + typecheck, every package (Go and bun)
go test -race -timeout 120s ./...            # the Go suite — -race is not optional, it is in the script
go vet ./...
go test -race ./packages/pstack/internal/jobs/ -run TestCancel   # one package / one test, faster loop
go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd packages/conformance && bun test          # the black-box suite against bin/pstack
cd packages/conformance && bun test test/api-sso.test.ts        # one route group

# Manual CLI runs. There is no root preview.yml, so pass -f; the example needs PR and GIT_SHA
# (an undefined variable is fatal — invariant 7).
PR=123 GIT_SHA=abc go run ./packages/pstack/cmd/pstack -f packages/pstack/examples/preview.yml validate
PR=123 GIT_SHA=abc go run ./packages/pstack/cmd/pstack -f packages/pstack/examples/preview.yml up -n -v
go run ./packages/pstack/cmd/pstack --help

# A live server + UI, for driving the web interface.
PSTACK_TOKEN=dev PSTACK_DATA=/tmp/pstack-dev go run ./packages/pstack/cmd/pstack serve   # :7878
cd apps/ui && bun run dev                                                               # :5273, proxies /api
```

There is **no linter** beyond `go vet` and `gofmt`. `bun run check` is the gate. No runtime
dependencies in `packages/client`, and only the four listed modules in `go.mod` — keep it that way.

## Testing expectations

**Any change to lifecycle ordering, failure semantics, exit codes, or a security boundary needs a
test.** Patterns to copy, in order of how much they prove:

1. **Boot the real server** (`bootServer()` in `packages/conformance/harness`) and assert on whole
   response bodies. Every HTTP-level test belongs there. A free port per server means tests run in
   parallel safely; always `stop()` in a `finally`.
2. **The real-filesystem leak test** — `touch` a file, declare an axis whose `down` lies (`"true"`)
   and whose `assert_gone` is `! test -e <file>`, assert `verify` fails, `rm` it, assert it passes
   (`internal/stack`). A fake runner cannot prove the gate catches a survivor.
3. **A fake `docker` on `PATH`** — a shell script writing scripted JSON (`printf`, never `echo`),
   for anything reading container state. Several tests make it *mutable* between polls.
4. **`exec.NewFake(fail, stdout)`** — records commands (`Commands()`). For ordering and flow.

Assert on `Outcome.Steps` (phase / ok / message), not on printed output — `Report()` is presentation.
Every Go test function carries a `// negative control: <the mutation that fails it>` line (rule 17).

### The rule that matters most

**Write the negative control.** After a test passes, break the code it covers and confirm the test
fails. Several bugs in this repo's history shipped behind green tests that asserted nothing:

- A scrub test whose fixture never contained the secret.
- A UI-detection test using a fixture with a service name **I invented**, while the generator emitted
  a different one — so `upgrade` removed the advanced UI on every host and the test said fine. The
  fix was to generate the fixture with the real `init`.

If a test cannot fail, it is documentation with a misleading name.

### The conformance suite — the black-box specification

`packages/conformance` is the HTTP/CLI contract as tests that **spawn the real `pstack`** and never
import the implementation. It graded the Go port against the TypeScript reference until the two were
byte-identical; the reference is gone and the goldens are the specification. Three rules, each
enforced by a script:

- **`PSTACK_IMPL=go|null`** selects what is spawned (`harness/impl.ts`; `go` is the default). `go`
  runs `$PSTACK_BIN` (default `packages/pstack/bin/pstack`); `null` is a server answering `200 {}`
  to everything.
- **Every test must fail against `null`** — `bun run vacuity` lists any that do not. A test that
  passes against a server that asserts nothing is the class of bug above, mechanised.
- **Goldens are checked in and ARE the contract** (`golden/cli` exact CLI transcripts, `golden/render`
  the control compose for all eight init cells, `golden/facts` the JavaScript semantics the port
  reproduces, `golden/host` a complete data directory every binary must open unchanged). A change
  to a golden is a deliberate contract change: regenerate with `bun run gen`, commit the diff with
  the code, and say so in the CHANGELOG.
- **Differential mode** (`bun run diff --a <binary>`) replays nine scenarios on binary A then B over
  one data path and compares traces after masking; the docker argv the API issued is a step too.
  `--self` (the same binary twice) must be empty — it is the mask list's own control, and CI runs it.
- **Pass counts are a ratchet** (`bun run ratchet`, `expected-pass.json`): they only go up.
  `bun run status` renders the file → package matrix from `port-map.json`.

A new HTTP-level test belongs in `packages/conformance`; a unit test beside the package.

## The Go rules — every package follows these

The binary is a re-implementation of a fixed contract — the conformance suite, the goldens and
the exact docker argv — and the contract has JavaScript semantics baked in (JSON key order,
`.length` in UTF-16, `Number()`, last-wins query parsing). These rules are what keep thirty
packages byte-compatible with it, and they stay after the port because the contract stays:

1. **JSON is structs (field order) or `*omap.Map`/`jsonx.Object` — never `map[string]any`**, and
   every response, stored payload and signed body goes through `jsonx`. Stored payloads and event
   `data` are `json.RawMessage`.
2. **omitempty audit.** Tri-states and null-not-absent fields (`busy`, `running`, `reachable`,
   `verified`, `stack`, `orchestrator`, `asleep`, `service`, `health`, `exitCode`, `node`, …) are
   pointers **without** omitempty. Genuinely-absent fields (`unresolved`, `stackSharedWith`,
   `endedAt`, `outcome`, `error`, `cancelledBy`, `reason`, `hostPort`, …) are pointers **with**
   omitempty. Never omitempty on a plain bool or int — Go would delete `ok:false`.
3. **Every `[]T` and map in a response, event payload or stored meta is non-nil at construction.**
   `null` where the UI expects `[]` is a blank page. `AssertNoNilCollections` in every response test.
4. Route on `r.URL.EscapedPath()` and decode per segment; query via `js.ParseQuery` (last value
   wins; numerics via `js.ParseNumber`, kept as float64 — `?tail=1.5` is accepted and echoed).
5. Wherever JS iterated an object/Map/Set into output (notes, label lists, the missing-variable
   message, `requiredVars`), use a slice or `omap` — never range a Go map into output.
6. `sort.SliceStable`; string sorts are byte order (a documented divergence from `localeCompare`).
7. `.length` that is served or gated → `js.Len`; 300-char truncation → `js.Truncate`;
   `String(number)` → `js.NumberString`; `Number()` → `js.ParseNumber`.
8. Templates: init markers `strings.Replace(s, marker, block, 1)`; cloud-init `strings.ReplaceAll`
   over an ordered list; never `regexp.ReplaceAllString` or `text/template` for either (invariant 18
   inverts in Go: `$` is nothing to `strings.Replace` and everything to `regexp`).
9. `escapeHostRegexp` is `regexp.QuoteMeta` (verified: the same fourteen bytes as the JS class);
   JS flags become `(?i)`/`(?m)` prefixes; a compile error is swallowed where the TS had try/catch.
10. Files: `0o666`/`0o777` and let umask apply; an explicit `Chmod` only where the TS site chmods;
    temp-then-rename where the TS does. `meta.json` keeps unknown fields (`Extra map[string]json.RawMessage`).
11. Env: `??` sites use `os.LookupEnv` presence (empty is a value); `||` sites treat empty as unset;
    a child process ALWAYS gets an explicit `cmd.Env` (nil inherits — the opposite of Bun).
12. HTTP clients: notify uses `CheckRedirect = http.ErrUseLastResponse` + a 5 s per-attempt
    context (a 3xx is a failure, never a hop — that is the SSRF control); sso follows; no client
    without a timeout.
13. Hosts: parse with `net/netip`, never compare bracketed strings; WHATWG normalisation
    (lowercase, empty path → `/`) is explicit where the TS relied on `new URL()`.
14. Concurrency: every shared struct names its owner or its mutex in a comment; **no method calls a
    sink, a subscriber or a bus listener while holding a mutex**; goroutines only where listed (the
    job runner, a delivery send, stream pumps, the watchers), each with `recover`. `events.Emit`
    dispatches synchronously, in registration order — never `go fn(e)`.
15. Streams: pumps read to EOF → `wg.Wait()` → `cmd.Wait()` → the terminal frame. Never `Wait`
    while a pump is still reading. `exec.Command("bash", "-c", cmd)` with `cmd.Cancel` sending
    SIGTERM (a hook may trap it); no `Setpgid`.
16. SQLite: one pooled connection. Inside `Store.Tx` use only the `*sql.Tx`; read `Rows` fully and
    close them before the next statement; a nested `db.*` call is a permanent self-deadlock.
17. **Every `Test…`/`t.Run` carries a `// negative control:` line naming the mutation that fails
    it**, and it was run. `go test -race -timeout 120s` is the test command, not an option.

## How to add things

### A new hook type

The four-hook tuple is hardcoded in several places. All of them:

1. `internal/spec` — the `Axis` struct, its field read, `Hooks()`, the empty-axis guard.
2. `internal/stack` — the `Phase` constant, and the call site **with explicit failure semantics**
   (fatal or recorded? invariant 1).
3. `internal/cli` and `internal/api` both print `Axis.Hooks()` — nothing to add there.
4. If it can indicate a leak: `Outcome.Leaked()` in `internal/stack` — the ONE scan (invariant 8).
6. Docs: `docs/usage.md`, `examples/preview.yml`.
7. A test.

### A new CLI command

`internal/cli`: add the `case` in `run.go`, add it to `Commands` (an unknown command must fail as
*unknown*, not by hunting for a spec file — that bug shipped), add a line to `Usage()`, add flags
to `ParseArgs` **and** `Usage()`, and return an `Exit` with a code from the table. Keep the logic in
a package; `cli` is argv, dispatch and exit codes only. The usage text is a golden
(`golden/cli/help.json`) — regenerate it deliberately. Then `docs/usage.md`.

### A new API route

`internal/api`: add the route to the if-chain in `routes.go` (in order — a greedy pattern later in
the chain is reachable only if nothing above it matched), **update the route list in `server.go`'s
header**, and return domain errors so `fail()` maps them to 400/409 rather than a 500. Long operations return `202 { job }`,
never a held-open socket; one RUNNING job per stack with a queue one deep, so a busy stack answers 202-queued rather than 409. Reads that start something
(a readiness watch) must not emit events — a page view must not manufacture a notification. Then the
UI if it consumes it, and `packages/client` if a script would want it.

### A new event

`internal/events` (`Names`), a chat line in `internal/notify`'s `Summarize`, the emit site, the
catalogue in `docs/webhook-events.md`, and a test. Add-only — invariant 14.

### A new lifecycle action

`sleep`/`wake` are the template. `jobs.Action`; the branch in `startLifecycle` (`internal/api`,
`server.go`) — the ONE place jobs start, shared by the POST route, the wake dispatch and the
scheduler; the lifecycle regex on the `:id` route; `actionWord` in `internal/notify`;
`LifecycleAction` + `ACTION_LABELS` in both UIs; the client SDK's method and `JobAction`;
`docs/webhook-events.md` (`job.started`'s `action`). If it can leave a leak behind, `Outcome.Leaked()`
(invariant 8).

### A new notifier type

One entry in `Types` (`internal/notify`): `{Kind, Label, Signs, Fields, Validate, Send}`. No schema
migration, no route change, no UI change — the UI renders `fields` from `/api/notifiers/meta`. Slack
and Discord cost one factory and two registrations; if a new type needs more than that, the seam is
being worked against.

### A database change

Append to `Migrations` in `internal/store` (`migrations.go`). **Never edit a shipped migration** — it will not re-run
anywhere it already ran. The array index is the version.

### UI work

Read [`docs/ui-rules.md`](docs/ui-rules.md) first — casing, one control height, one radius scale,
full-width pages, container queries for tables. Then **look at it in a browser**: several defects in
this UI's history were invisible in code review and obvious in a screenshot (a stale "Healthy" beside
"Exited", buttons that took a click and did nothing, columns painted over the panel beside them).

## Scope discipline

**Prefer deleting over adding.** Deliberate non-goals, and what each would actually require:

- **Multi-tenancy.** Needs a per-tenant isolation boundary — separate VMs/microVMs or Kubernetes
  namespaces — plus a credential boundary. That is a different product. Do not add tenant IDs to
  `internal/api` as a substitute.

  There IS role-based access control now (`internal/api/permissions.go`: viewer, developer,
  maintainer, admin) and it is **not** the substitute this line used to warn against. It divides
  what one trusted team may do on one host — it does not isolate anyone from anyone. Every role
  shares one Docker socket, one spec set and one set of secrets, and a developer runs arbitrary
  shell through a hook or `up`. If you find yourself reaching for a per-tenant scope on a role,
  that is the boundary above, and it is still a different product.
- **Untrusted specs.** Same boundary problem, plus hooks are shell strings by design.
- **Inbound git-webhook deploys, a service catalog.** Use a PaaS.
- **Persistence / reconciliation of what exists.** Invariant 10.

Before adding anything, check whether an existing axis hook already expresses it. Most requests
("clean up my registry tags", "warm a cache", "run migrations") are a spec's `up`/`down`, not code.

## Releasing

Lockstep across all three `package.json`s, from the repo root; `packages/pstack/package.json` is
the version of record and what the binary reports (`internal/version`). A `v*` tag runs
`.github/workflows/release.yml`: it asserts the tag equals every package version, runs the Go
suite, the conformance ratchet and an image smoke test, then GoReleaser (the binaries,
`checksums.txt`, the version-stamped `install.sh`), then publish-kit for the UI and the client.

```bash
bunx publish-kit bump patch|minor    # the two npm packages (publish-kit skips the private one)
# set packages/pstack/package.json "version" to the same number — the release workflow asserts it
bun run check                        # must be green
git commit && git tag vX.Y.Z && git push origin main --tags
```

**Tagging and publishing are the maintainer's, not yours.** Never push a tag or run
`release:publish`. Version numbers appear in docs ("since 0.X.0") — grep for the old version after
a bump.

## Working style in this repo

- **Verify, don't assume.** Read the generated output rather than the code that generates it; boot
  the server rather than reasoning about the route. Every serious bug here came from a confident
  assumption about a string somewhere else.
- **Say what you did not do.** A partial fix reported as complete is worse than a partial fix.
- **The file headers are the design record.** When you change a decision, change the header comment
  that explains it in the same edit — a stale header is worse than none, because the next reader
  trusts it.
