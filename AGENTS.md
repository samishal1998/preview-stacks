# AGENTS.md

Instructions for an AI agent changing **this codebase**. Using `pstack` is a different job; this is
about editing it.

**Current version: 0.40.0.** One workspace: the Go binary (`packages/pstack`, released on GitHub),
its black-box specification (`packages/conformance`), and two npm packages (the client SDK, the
advanced UI). The control plane was a Bun/TypeScript package until 0.28.0; 0.29.0 is the Go port,
byte-compatible with it (`docs/port-status.md`). 0.40.0 added Loki logging and Grafana
(`docs/loki-logging-design.md`).

## Read this first

1. This file, end to end. The **Invariants** section is the part that matters — each entry is a rule
   plus the failure that produced it.
2. [`docs/README.md`](docs/README.md) — the documentation index, so you know what exists.
   Open follow-ups: [`docs/loki-logging-as-built.md`](docs/loki-logging-as-built.md) §5.
3. The **header comment of every file you are about to touch.** They are long on purpose and explain
   *why*, not *what*. Most questions you will have about a design decision are answered at the top of
   the file that made it. If a header contradicts this document, the header is newer — trust it and
   fix this file.

For a specific change, `docs/control-plane.md` explains the architecture and refuses several
plausible restructurings with reasons. For logging, the control template, the join material, Loki's
settings or Grafana, read `docs/loki-logging-as-built.md` first (state, decisions, caveats, open
follow-ups in §5). Then read control-plane.md §5g/§5h, and `docs/loki-logging-design.md` for what
was approved and why.

## What this is

A CLI + HTTP API + web UI that gives an ephemeral per-PR preview stack a **declarative lifecycle**:
named *isolation axes* (a database branch, a queue namespace, per-PR images, DNS) each with up to
four hooks, provisioned around a Docker Compose project, torn down in reverse, and then **proven
gone**.

**The differentiator is isolation-axis lifecycle + leak verification.** Everything else — the API,
the UI, notifiers, the terminal, logging — is scaffolding around that. When a change makes the leak
semantics harder to state, it is a net loss even if it adds a feature.

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
                       cmd/pstack (main), internal/<pkg> (everything), assets.go (the eight embeds),
                       api/openapi.yaml (generates `pstack api`; served at /api/openapi.{yaml,json}),
                       ui/, templates/ (control compose, loki/, grafana/, cloud-init), examples/,
                       tools/yamlcat (a parser-diff tool, not shipped), CHANGELOG.md,
                       package.json (the lockstep version of record).
packages/conformance/  the black-box specification (bun:test): goldens + tests that spawn bin/pstack.
packages/client/       @samyx/preview-stacks-client — zero-dependency API client + verifyWebhook.
apps/ui/               @samyx/preview-stacks-ui — the advanced UI (Vue 3 SPA).
docs/                  See docs/README.md.
skills/pstack/         a skill for USING pstack (tracked).
.claude/skills/        project skills for agents CHANGING pstack — see *Agent workflow*.
Dockerfile, .goreleaser.yaml, install.sh, publish.config.ts (the two npm packages), turbo.json.
.superpowers/          build ledgers; ignored only via .git/info/exclude, so a fresh clone has none.
```

The root `go.mod` (`module github.com/samishal1998/preview-stacks`, go 1.23.5) has five direct
requires, each justified: `modernc.org/sqlite` (pure Go, so `CGO_ENABLED=0` is a static binary),
`github.com/goccy/go-yaml` (a PARSER only — `internal/yamlx` resolves scalars itself),
`golang.org/x/crypto` (argon2), and — for `pstack api` only — `github.com/samishal1998/openapi-commands`
with `cobra`. `github.com/coder/websocket` is imported by `internal/api/ws.go` but listed
`// indirect` (go.mod is not tidy); `pflag` arrives with cobra.

`net/http` only; the CLI parser is hand-rolled and **cobra never owns the root command**, because
flags may appear anywhere, `--ui` peeks ahead, and the usage text is a golden. Cobra owns the `api`
subtree and nothing else. Its generator (`openapi-commands/cmd/oascmd-gen`, which pulls libopenapi,
run by the `go:generate` line in `internal/apicli`) is a TOOL dependency: the generated file is
checked in, so the binary never links it.

Do not add a module for what a few lines do.

### `packages/pstack/internal` — by responsibility

One package per responsibility (41 of them), named as the reference's files were:

**The core lifecycle** (read these first; the product is here):

| File | Responsibility |
|---|---|
| `spec` | Parse + validate `preview.yml` → resolved `Stack`. Owns interpolation, the stack-name charset rule, axis dedupe, `Warnings` (on the result — there is no module global). `subdomains.go` is the wildcard routing. |
| `stack` | `Up` / `Down` / `Verify` / `Status` / `Report`. Owns the failure semantics — **the whole product is in this package**. `Outcome.Leaked()` is THE leak scan, the one copy. |
| `compose` | Builds `docker compose` command strings — or, when `spec.compose.orchestrator` is `swarm`, the `docker stack` ones from `swarm`. Owns the all-profiles-on-down rule, `ComposeSleep` (down **without** `-v`), `Shq`, and the loki-plugin check before an `up` of a logged deployment. |
| `swarm` | Docker Swarm: `Swarmify` (plain compose → the v3 subset `docker stack deploy` accepts, faithfully, every change named), the `docker stack` command lines, node listing, and `JoinMaterial`/`SwarmReport` — shared by `GET /api/swarm/join` and `pstack swarm`, so the two cannot hand an operator different commands for one cluster. Owns `LokiVersion`, the one-line `LokiPluginInstall` and `MarkLokiPlugins` (per-node plugin reads). The leaf of the compose/autolabel/swarm triangle. |
| `exec` | The only place a hook is spawned (`bash -c`, env as a REPLACEMENT, SIGTERM on cancel). Dry-run, output capture, `CaptureOutputs`, the `Runner` seam and its `Fake`. |
| `log` | The `Sink` seam: `Writer` (CLI), `Buffer` (API jobs), `Null` (tests). |

**The control plane:**

| File | Responsibility |
|---|---|
| `api` | HTTP API + UI host. Routes live in `routes_<area>.go` (auth, config, control, deploy, domains, grafana, logging, openapi, probe, settings, tls) plus the ordered if-chain in `routes.go`; `principal.go` is the gate, `permissions.go` the role table, `sse.go`/`ws.go` the streams, `loki_apply.go` the Loki settings job. **The route inventory is `api/openapi.yaml`** (`openapi_coverage_test.go` fails in both directions) **plus `permissions.go`** (`permissions_test.go` walks the chain). `server.go`'s header has no route list and still names the deleted `api.ts` — fix it when you next touch that file. Owns the `:id` → spec-variable binding. |
| `apicli` | The generated `pstack api` tree (`zz_generated.go`, `oascmd.lock.json`). Regenerate with `go generate ./packages/pstack/internal/apicli`, never with `--on-drift` in an unrelated edit; a deliberate breaking change uses `--on-drift=all` once (apicli.go header). `OperationCount` (82) is a constant a test checks against the lock file. |
| `cli` + `cmd/pstack` | Arg parsing (`args.go`, the usage text byte for byte), command dispatch (`run.go`), per-command help and completion (`commands.go`, `completion.go`), the init silent-revert guard (`initguard.go`), **exit codes**, the `serve` loopback interlock, `healthcheck`. `main.go` is one call. Logic belongs in the package, not here. |
| `jobs` | In-memory job registry: one RUNNING job per key plus a queue one deep where the newest supersedes the queued one, a global concurrency cap (`PSTACK_MAX_JOBS`, 4), bounded to 50 transcripts, subscriber fan-out outside the lock, cancellation per job and per stack. `loki-apply` is its one non-deployment action. |
| `registry` | The deployment registry — a directory of YAML per deployment. Deliberately not a database (invariant 10). |
| `specs` | Named specs: store once, reference from many deployments. |
| `scheduler` | Sleep/wake: the `SleepIndex`, the `TrafficMeter` (Traefik's per-router counters → "last request"), the `Scheduler` tick (`idle`/`after`), and the spinning-up page. Everything it knows is in memory — invariant 10. |
| `share` | Share links: an HS256 JWT signed with `PSTACK_TOKEN`. Sign, verify, and nothing stored. |
| `settings` | Runtime knobs (`max_jobs`, `default_role`): a closed key list, env as the default not the authority, readers that never fail and resolve downward. |
| `config` | The portable host configuration (`GET`/`POST /api/config`, `pstack pull config`/`push config`). `Assemble` is a full credential dump; `Apply` creates or skips, never updates or deletes. Loki settings stay behind. |
| `initctl` | `pstack init` — stands up the control stack, including the Loki and Grafana services (`LokiService`, `GrafanaService`, `LokiWiring`). CLI-only, permanently; its header explains why. |
| `upgrade` | `pstack upgrade`, `pstack ui <mode>` and `pstack logging loki`/`off`. Reads back what `init` decided so nothing rotates. |
| `image` | `pstack build-image` — builds the control image, pinned to the running CLI's version. |
| `cloudinit` | `pstack cloud-init` — renders the boot user-data, multi-distro. |
| `loki` | Loki's settings, pure (no server, no docker): clamped `bounds` served as `limits`, `Render` by literal anchors over the embedded config, schema-period guard, the S3 credentials file, the signed S3 probe. |

**Observation and safety:**

| File | Responsibility |
|---|---|
| `inspect` | What is actually running + what Traefik was told. Answers "why does the hostname 404". Discovers the control stack's `loki` (`LokiPushURL`) and `grafana` (`GrafanaOnChecked`; `ok=false` when docker did not answer) containers. Never returns a raw `docker inspect` (it contains the container's whole environment). |
| `readiness` | Post-deploy watch: containers → ready / failed / timedout. Observational only; it starts and repairs nothing. |
| `redact` | Redaction for anything a human is shown. |
| `terminal` | The container shell. **The most dangerous route in the codebase** — read its header before touching it. |
| `spec/subdomains.go`, `autolabel`, `routing` | Traefik wiring: wildcard routing, generated labels, dynamic-config files. `autolabel` also injects the Loki `logging:` block (`InjectLogging`, inside `MaterializeCompose`) and refuses a preview naming a control hostname. |

**Persistence and delivery:**

| File | Responsibility |
|---|---|
| `store` | SQLite (`<dataDir>/db/pstack.db`) + 9 migrations, 13 tables. Append to `Migrations`; never edit a shipped one. Inside `Tx` use only the handed `Querier` (one pooled connection). |
| `auth` | Accounts, sessions, personal tokens — and the SSO side of accounts: the stored provider, the `(provider, subject)` links, and `SsoSignIn`. Argon2id with a PHC codec that PARSES m/t/p (`phc.go`); sessions and tokens stored as SHA-256. `SessionHashUser` serves the Grafana cookie. |
| `sso` | The OIDC/OAuth2 protocol, and only that: presets, discovery (cached per `Client`), PKCE, the token exchange, ID-token verification (RS256/ES256, stdlib), claim mapping, the `TransientStore`. Touches no accounts. |
| `hostvars` | Host-level `${vars.*}` / `${secrets.*}`. |
| `registries` | Private-registry credentials for image pulls. |
| `events` | The domain event bus. `Names` (30) is a **public contract** — append, never rename, never regroup. Listeners run inline, in registration order; `Data` is marshalled once. |
| `webhooks` | Notifier registrations + the delivery log. |
| `notify` | Delivery: the `NotifierType` seam, the per-notifier queue, retries, redelivery. |

Foundations under `internal/` with no product logic: `omap` (the ordered map every document is),
`yamlx` (the YAML-1.2-core parser), `jsonx` (`JSON.stringify` semantics), `js` (`.length`,
`Number()`, `encodeURIComponent`, `URLSearchParams`…), `version`, `testfacts`. Every one is tested
against `packages/conformance/golden/facts` — what the reference runtime measurably did.

Every package has a `_test.go` beside it (~330 Go test functions). The black-box suite is
`packages/conformance` (303 tests in 26 files: every route group, 93 CLI transcripts, a complete
host fixture). `packages/client/test/client.test.ts` drives the client against the spawned binary —
that is the anti-drift check for the SDK.

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
step with `Phase == PhaseAssertGone && !OK` — and it is the ONE copy (the reference had four).
`cli`, `jobs` and the wake page all call it. Add a leak-bearing phase and you edit it.

**9. The API's loopback interlock has two halves** (`internal/cli`, `Serve`). Without `PSTACK_TOKEN`: the
host is forced to `127.0.0.1`, **and** an explicit non-loopback `PSTACK_HOST` is a hard exit 3 rather
than a silent downgrade. An API that can delete databases must not be exposable by forgetting a flag.

**10. No state store FOR WHAT EXISTS.** Job records are transcripts of attempts, in memory,
unpersisted. Restarting the server loses history, not correctness.

*Amended in 0.10.0.* There **is** a SQLite database, and the line it holds is precise: accounts,
sessions, tokens, notifier registrations and deliveries, terminal audit, host variables, SSO
(`sso_config`, `sso_links`, `sso_state`, `sso_providers`), runtime `settings`, and `loki_config`
(one row: what an operator SAVED, not what Loki runs, plus a `previous_*` undo record for one
apply). Every table is justified in its migration as configuration an operator chose, with no
source of truth in Docker to contradict. The deployment registry stayed a directory of YAML an
operator can read and repair over SSH. **If you find yourself adding a table whose rows describe
what is running, you are about to be wrong.** Whether Loki or Grafana runs is read from docker for
the same reason (invariant 23).

**11. Tri-state fields never collapse to a boolean.** `busy`, `running`, `reachable`, `verified`,
`enabled` (logging), a node's `lokiPlugin` are `boolean | null`, and `null` means *could not
determine* — an unresolved spec has no stack name to look up, and a docker that did not answer is
not the same fact as "nothing is running". Collapsing either to `false` is how a UI reports a live
stack as torn down.

**12. `init` and `upgrade` are CLI-only, permanently.** The API runs *inside* the control stack;
recreating that stack from a request kills the process mid-operation, and a broken image leaves the
host with no control plane and no remote way to repair it. Never add a route that calls them.
`POST /api/control/restart` and the `loki-apply` job are not exceptions but the boundary drawn
exactly: they restart a control container *other than pstack's own* (refused by name, whoever asks),
they never recreate one, and the same image comes back.

**13. The container name in a request is never trusted.** `docker exec`/`stop` accept any container
on the daemon — including Traefik (every preview on the host) and `pstack-control` itself (whose
filesystem is the database). Every route taking a container name matches it against the containers
that deployment owns and 404s otherwise. See `internal/terminal` and the container-action route.

**14. Event names and payload fields are add-only.** They live in stored notifier registrations;
renaming one silently stops deliveries for everyone subscribed. Same for the delivery envelope's four
fields — receivers verify a signature over those exact bytes.

**15. A secret's value has no read path.** Notifier signing secrets, host secrets, registry
passwords and the Loki S3 secret go in and never come back out. `Webhooks.get()`/`list()` mask;
`rawConfigOf()` does not and is for the delivery path only. Conflating them is how a masked value
gets POSTed to a masked URL — which happened. The S3 secret is stored unhashed (the probe and Loki
need it) in `loki_config.secret`; `GET /api/logging` answers `secretSet`, and an empty, omitted or
masked `secretAccessKey` keeps the stored one.

The ONE deliberate exception is `GET /api/config`: every secret and hash in plaintext, to the root
`PSTACK_TOKEN` **only**, never an admin session (`routes_config.go`'s header — "the inconsistency is
the security control"). In tokenless loopback mode invariant 9 is its only guard.

**16. A share principal is closed by default.** `shareAllows` in `internal/api` runs right after the auth
gate and **before any route**: a `{ kind: 'share' }` principal reaches exactly the GETs its views
name, on its own deployment, with the stored variables only. A new route is unreachable to it until
someone lists it there. The raw `PSTACK_TOKEN` is never read from a query string — only a JWT is.

**17. Sleep never removes volumes; wake IS `up`.** `compose.ComposeSleep` is `down` without `-v`
(swarm: `stack rm`, which never touches volumes), its own function rather than a flag so the
`down -v` the leak tests assert on cannot be weakened by a default. A wake runs `up()` exactly —
axis hooks are idempotent by contract and re-capture their outputs, so nothing is persisted between
the two.

**18. Template substitution is literal.** The wake router's rule ends in `$$` precisely so compose
hands Traefik a literal `$`. `internal/initctl` substitutes each marker with
`strings.Replace(s, marker, block, 1)` and cloud-init with `strings.ReplaceAll` over an ordered
list — never `regexp.ReplaceAllString` or `text/template`, to both of which `$` means something.

**19. An SSO identity is `(providerKey, subject)`, and an email only ever ADOPTS.** `internal/auth`
`SsoSignIn` looks up the link first; the email branch exists solely to take over a *pre-existing
local* account and is gated on `emailVerified === true` and on exactly one match. Emails move
between people and subjects do not, so keying on the address — or relaxing the verified check, or
adopting the first of several matches — is how one person signs in as another. `emailAllowed` fails
CLOSED for the same reason: a non-empty allow-list plus no address is a refusal.

**20. Logging off adds nothing to the control compose except pstack's unconditional
`./loki:/etc/loki` mount (invariant 21).** `LokiService`/`GrafanaService` return `""` without
`--logging loki` and ride the EXISTING `#__ADVANCED_UI_SERVICE__` marker; `LokiWiring` extends three
existing lines through anchors checked with `Contains` before `Replace(…, 1)` and fails init by name
if one moved; `loki.Render` walks 10 anchors the same way. *Why:* every `#__MARKER__` leaves a line
behind when rendered off, so a new one changes every host's compose file. **Never add a marker.**
0.40.0 changed every logging-off cell once, for that mount; that change is not a precedent.

**21. pstack's `./loki:/etc/loki` mount is unconditional**, and init creates `control/loki` at 0755
in every mode. *Why (ruling X1):* pstack's env is an explicit list, so its service block is then
identical on and off and `pstack logging loki|off` never recreates pstack — a logging-on-only mount
would, killing running jobs. Without a writable `control/loki` the Loki PUTs answer 409.

**22. `control.`, `api.`, `loki.` and `grafana.` are reserved on every domain, ALWAYS** — logging
off too. `autolabel` refuses a `pstack.routing.host` naming one (a service with its own `traefik.*`
labels is the escape hatch); `IsControlHostname` keeps the wake catch-all off them; pstack itself
answers `grafana.<primary>` 503 and every other `loki.`/`grafana.` host 404 (the driver does not
retry a 404, so `docker stop` is not delayed), and never serves its UI on them. `control.` and
`api.` are pstack's own. The Grafana router's `priority=10000` beats an older preview's
equal-length rule for its cookie.

**23. The Loki driver's options are constants.** Only `loki-url` and `service_name=<stack>-<service>`
vary. *Why:* with Loki unreachable the driver holds a node-wide lock while it retries, so every
`docker stop` on the node waits — retries 2, timeout 1s and max-backoff 800ms bound it.
`mode: non-blocking` protects the app's stdout, not the stop. A service with ANY `logging` key opts
out whole. Whether Loki runs is the `pstack.logging.push-url` label on the control `loki` container,
never a setting, so the CLI and the API inject the same thing.

**24. Grafana sign-in trusts one cookie, and viewers are refused.** Verify reads
`__Host-pstack_grafana` only — never `pstack_session`, a bearer or `principal()`; the cookie is the
session hash MAC'd with `PSTACK_TOKEN`, with no table. Developer/maintainer → Editor, admin → Admin,
viewer → 403 (owner, 2026-09-15: a Grafana Viewer still queries Loki's unredacted lines). Grafana
and Loki sit on the `logs` network only, no published port: anything reaching :3000 can send
`X-WEBAUTH-USER`; `authResponseHeaders` lists every header Grafana trusts. On/off comes from docker,
cached by `reindexLoop` (≤30 s), the last value kept when docker does not answer.

**25. A Loki settings save restarts Loki and nothing else, and never strands data.** A `loki-apply`
job on the `pstack-control` key: render → `-verify-config` → rename → restart Loki → ready → roll
back on failure; `reconcileLoki` finishes a cut-off apply at boot. A live schema period is never
dropped (checked against the FILES, never the row); filesystem→S3 is one-way. S3 keys go in a 0600
file owned by uid 10001, never the env (`docker restart` keeps env). Retention/chunks are
maintainer, storage is admin. Pending patches are per section with a generation: a later save
supersedes the waiting job and rides it.

**26. `init` refuses an omission that would silently change a host** (`cli/initguard.go`): token,
`--ui`, DNS token, `--challenge`, `--dns-provider`, `--orchestrator`, `--logging loki`,
`PSTACK_LOKI_PASSWORD` (a new push password breaks every running container's pushes). The password
is env-only, 32 lowercase hex; `logging off` keeps `LOKI_PUSH_PASSWORD` so `loki` reuses it. A
spelled flag passes; `upgrade` reads everything back and never trips it.

### Gotcha: dry-run proves ordering, never absence

Skipped steps carry `ok: true`, so a dry-run `down` prints ✓ for every `assert_gone`. That is
correct — nothing ran — but never read a green dry-run as "clean".

### Known Loki minors, left on purpose

`DetectLogging` costs a `docker ps` + inspect per compose verb even with logging off; `GET
/api/logging` adds a `ControlRuntime` per 10 s poll per open Control page; `pstack-control_logs`
survives `logging off`; nothing reconciles a hand-deleted `control/loki` until pstack restarts.

## Commands

```bash
bun install                    # required in a fresh clone (the UI, the client, the conformance suite)

bun run check                  # THE GATE: build + test + typecheck, every package (Go and bun)
go test -race -timeout 120s ./...            # the Go suite — -race is not optional (CI uses 180s)
go vet ./...
go test -race ./packages/pstack/internal/jobs/ -run TestCancel   # one package / one test, faster loop
go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
go generate ./packages/pstack/internal/apicli                     # after any openapi.yaml change
cd packages/conformance && bun test          # the black-box suite against bin/pstack
cd packages/conformance && bun test test/api-sso.test.ts        # one route group
cd packages/conformance && bun gen/goldens.ts  # CLI + render goldens; the diff is the contract

# Manual CLI runs. There is no root preview.yml, so pass -f; the example needs PR and GIT_SHA
# (an undefined variable is fatal — invariant 7).
PR=123 GIT_SHA=abc go run ./packages/pstack/cmd/pstack -f packages/pstack/examples/preview.yml validate
PR=123 GIT_SHA=abc go run ./packages/pstack/cmd/pstack -f packages/pstack/examples/preview.yml up -n -v
go run ./packages/pstack/cmd/pstack --help

# A live server + UI. With a token the SPA needs an account; the first admin comes from
# PSTACK_ADMIN_USER/PASSWORD only while no account exists. Leave the SPA's Settings → apiBase
# EMPTY (an override breaks login); PSTACK_API retargets vite's /api proxy.
PSTACK_TOKEN=dev PSTACK_DATA=/tmp/pstack-dev PSTACK_ADMIN_USER=admin PSTACK_ADMIN_PASSWORD=… \
  go run ./packages/pstack/cmd/pstack serve                                  # 127.0.0.1:7878
cd apps/ui && PSTACK_API=http://127.0.0.1:7878 bun run dev                   # :5273, proxies /api
```

Anything reading docker needs a fake `docker` first on `PATH`: `SWARM_SHIM`, `LOKI_SHIM`,
`NODE_PLUGINS` in `packages/conformance/gen/goldens.table.ts`, `GRAFANA_SHIM` in
`test/api-grafana.test.ts`, the shape in `harness/docker-shim.ts`. `PSTACK_LOKI_DIR`,
`PSTACK_LOKI_READY_TIMEOUT_MS`, `PSTACK_LOKI_UID` are env-only, per the rule-18 carve-out.

Linting is `go vet` (`gofmt` is not enforced in CI), plus CI's `shellcheck install.sh` and its
`generated` job (`go generate` + `git diff --exit-code` on `internal/apicli`). `bun run check` is
the gate. No runtime dependencies in `packages/client`, only the five direct requires in `go.mod`.

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
A test racing an async job orders its inputs, never sleeps: `TestLokiApplyCarriesASupersededSave`
failed 49/50 under `-cpu=1` until it waited for the first job's first docker command (fix on
`claude/fix-flaky-loki-apply-test`, not yet merged).

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
byte-identical; the reference is gone and the goldens are the specification. Rules, each enforced by
a script:

- **`PSTACK_IMPL=go|null`** selects what is spawned (`harness/impl.ts`; `go` is the default). `go`
  runs `$PSTACK_BIN` (default `packages/pstack/bin/pstack`); `null` is a server answering `200 {}`
  to everything.
- **Every test must fail against `null`** — `bun run vacuity` lists any that do not. A test that
  passes against a server that asserts nothing is the class of bug above, mechanised.
- **Goldens are checked in and ARE the contract** (`golden/cli` exact CLI transcripts, `golden/render`
  the control compose for all ten init cells — eight logging-off, two `-loki` — `golden/facts` the
  JavaScript semantics the port reproduces, `golden/host` a complete data directory every binary
  must open unchanged). A change to a golden is a deliberate contract change: regenerate with
  `bun gen/goldens.ts` (it prints `wrote 93 goldens`), commit the diff with the code, and say so in
  the CHANGELOG. `bun run gen` also runs `gen/host-fixture.ts`, which deletes and rebuilds
  `golden/host`. Run it only for a deliberate on-disk format change (a migration), reviewed as one.
  When a route's output changes, hand-edit `golden/host/expected/<route>.json` (R1). A run leaves
  untracked `golden/host/db/pstack.db-shm` and `-wal` behind — never commit them.
- **Differential mode** (`bun run diff --a <binary>`) replays nine scenarios on binary A then B over
  one data path and compares traces after masking; the docker argv the API issued is a step too.
  `--self` (the same binary twice) must be empty — it is the mask list's own control, and CI runs it.
- **Pass counts are a ratchet** (`bun run ratchet`, `expected-pass.json`): they only go up.
  `bun run status` renders the file → package matrix from `port-map.json`.

A new HTTP-level test belongs in `packages/conformance`; a unit test beside the package.

## The Go rules — every package follows these

The binary is a re-implementation of a fixed contract — the conformance suite, the goldens and
the exact docker argv — and the contract has JavaScript semantics baked in (JSON key order,
`.length` in UTF-16, `Number()`, last-wins query parsing). These rules are what keep forty-one
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
   `null` where the UI expects `[]` is a blank page. Assert `[]`, not `null`, in every response test.
4. Route on `r.URL.EscapedPath()` and decode per segment; query via `js.ParseQuery` (last value
   wins; numerics via `js.ParseNumber`, kept as float64 — `?tail=1.5` is accepted and echoed).
5. Wherever JS iterated an object/Map/Set into output (notes, label lists, the missing-variable
   message, `requiredVars`), use a slice or `omap` — never range a Go map into output.
6. `sort.SliceStable`; string sorts are byte order (a documented divergence from `localeCompare`).
7. `.length` that is served or gated → `js.Len`; 300-char truncation → `js.Truncate`;
   `String(number)` → `js.NumberString`; `Number()` → `js.ParseNumber`.
8. Templates: init markers `strings.Replace(s, marker, block, 1)`; cloud-init `strings.ReplaceAll`
   over an ordered list; never `regexp.ReplaceAllString` or `text/template` for either (invariant 18
   inverts in Go: `$` is nothing to `strings.Replace` and everything to `regexp`). Extend the
   control template by checked anchors, never a new marker (invariant 20).
9. `escapeHostRegexp` is `regexp.QuoteMeta` (verified: the same fourteen bytes as the JS class);
   JS flags become `(?i)`/`(?m)` prefixes; a compile error is swallowed where the TS had try/catch.
10. Files: `0o666`/`0o777` and let umask apply; an explicit `Chmod` only where the TS site chmods —
    or where a non-root container must read it (`control/loki`, Grafana's datasources, 0755);
    temp-then-rename where the TS does. `meta.json` keeps unknown fields (`Extra map[string]json.RawMessage`).
11. Env: `??` sites use `os.LookupEnv` presence (empty is a value); `||` sites treat empty as unset;
    a child process ALWAYS gets an explicit `cmd.Env` (nil inherits — the opposite of Bun).
12. HTTP clients: notify uses `CheckRedirect = http.ErrUseLastResponse` + a 5 s per-attempt
    context (a 3xx is a failure, never a hop — that is the SSRF control); the S3 probe refuses a
    redirect the same way; sso follows; no client without a timeout.
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
18. **Every input control is settable BOTH ways — a flag and an environment variable — except a
    secret, which never takes a literal flag.** An operator scripting a host should not have to
    discover which half of a pair happens to be env-only, so a new knob gets both, named for the
    same thing (`--dns-provider` / `PSTACK_DNS_PROVIDER`), and the env is the `??`-style fallback
    (rule 11 decides presence vs emptiness).

    The exception is not negotiable and is not about taste: **argv is world-readable** through `ps`
    and `/proc/<pid>/cmdline` for as long as the process lives, and a value in `Parsed` also lands
    in the `t.Errorf("got %+v", p)` every parser test does. So a credential comes from the
    environment, a no-echo prompt, or a `--…-file <path>` flag — the PATH may be an argument
    because a path is not a secret. `PSTACK_CONFIG_KEY` (no flag, prompt or env),
    `PSTACK_LOKI_PASSWORD` (env only) and `--dns-token-file` are the shapes; copy whichever fits.

    Carve-out: `serve`'s tuning knobs are env-only by precedent (S2-R9) — the `api.Tuning` fields
    `TuningFromEnv` reads (`PSTACK_MAX_JOBS`, the `PSTACK_READINESS_*` knobs, the SSO TTLs,
    `PSTACK_LOKI_READY_TIMEOUT_MS`/`_UID`) plus `PSTACK_LOKI_DIR` (`loki.Dir`). Rule 18 applies to
    command inputs such as `init`, `cloud-init` and `swarm` flags.

## How to add things

### A new hook type

The four-hook tuple is hardcoded in several places. All of them:

1. `internal/spec` — the `Axis` struct, its field read, `Hooks()`, the empty-axis guard.
2. `internal/stack` — the `Phase` constant, and the call site **with explicit failure semantics**
   (fatal or recorded? invariant 1).
3. `internal/cli` and `internal/api` both print `Axis.Hooks()` — nothing to add there.
4. If it can indicate a leak: `Outcome.Leaked()` in `internal/stack` — the ONE scan (invariant 8).
5. Docs: `docs/usage.md`, `examples/preview.yml`.
6. A test.

### A new CLI command

`internal/cli`: the `case` in `run.go`; an entry in `Commands` (an unknown command must fail as
*unknown*, not by hunting for a spec file — that bug shipped); a `commandHelps` entry in
`commands.go` (`commands_test.go` fails without one; its `flags` drive completion); subcommands in
`completion.go`; a line in `Usage()`; flags in `ParseArgs` **and** `Usage()`; an `Exit` with a code
from the table. Keep the logic in a package; `cli` is argv, dispatch and exit codes only. Four
goldens list commands and move together — `help.json`, `help-h.json`, `no-args.json`,
`unknown-command.json` — regenerate them deliberately. Then `docs/usage.md`.

### A new API route

The handler in the right `routes_<area>.go`, dispatched from the if-chain in `routes.go` (in order —
a greedy pattern later in the chain is reachable only if nothing above it matched). A row in
`permissions.go`: the table is default-deny, so an unlisted route is root-only, and
`permissions_test.go` fails on a matcher with no row; `shareAllows` only if a share principal needs
it (invariant 16). Add it to `packages/pstack/api/openapi.yaml`, run `go generate
./packages/pstack/internal/apicli` and bump `apicli.OperationCount` (or list it in `notInTheSpec`
with a reason — `openapi_coverage_test.go` fails either way, and CI's `generated` job fails if the
spec moved without the generated file). Return domain errors so `fail()` maps them to 400/409, not
500. Long operations return `202 { job }`, never a held-open socket; a busy stack answers
202-queued, not 409. Reads that start something (a readiness watch) must not emit events. A
conformance test that fails against `null` (`bun run vacuity`), the ratchet raised, `api-rbac`
rows. Then the UI if it consumes it, and `packages/client` if a script would want it.

### A new event

Append to `Names` in `internal/events` — at the END: the order is the contract (`events_test.go`
pins it), so never regroup. A chat line in `internal/notify`'s `Summarize`, the emit site, the
catalogue in `docs/webhook-events.md`, and a test. Add-only — invariant 14.

### A new lifecycle action

`sleep`/`wake` are the template. `jobs.Action`; the branch in `startLifecycle` (`internal/api`,
`server.go`) — the one place *deployment* jobs start (POST route, wake dispatch, scheduler); the
lifecycle regex on the `:id` route; `actionWord` in `internal/notify`; `LifecycleAction`
(`useDeploymentActions.ts`) and `ACTION_LABELS` (`useFormat.ts`) in `apps/ui` only — the basic UI
prints the action raw; `JobAction` in `apps/ui/src/api/types.ts` and `packages/client/src/types.ts`
plus the client method; `docs/webhook-events.md`. If it can leave a leak behind, `Outcome.Leaked()`.
A job not about a deployment copies `internal/api/loki_apply.go`: `s.jobs.Start` on its own key
(`inspect.ControlProject`), no `startLifecycle` branch, no `:id` route, `verified: null`.

### A runtime setting

`internal/settings`: an arm in `Set`'s switch (unknown keys are refused), a reader beside
`MaxJobs`, and a permissions row. An env var, if any, is the default, not the authority. Readers
never return an error and resolve downward — never up to admin. Never call a reader inside
`store.Tx` (one connection: a permanent self-deadlock).

### A control-template change or a new init input

Anchors, never a marker (invariant 20). The eight logging-off render cells and the init, upgrade,
`ui`-switch, cloud-init and swarm-join transcripts stay byte-identical: `bun gen/goldens.ts` (never
`bun run gen`, which rebuilds `golden/host`), then read the golden diff first. A new init input
also needs the `upgrade` readback (which `pstack ui` and `pstack logging` re-run init from), an
`initguard` row (invariant 26), cloud-init passthrough, and a flag + env pair (Go rule 18).

### A new notifier type

One entry in `Types` (`internal/notify`): `{Kind, Label, Signs, Fields, Validate, Send}`. No schema
migration, no route change, no UI change — the UI renders `fields` from `/api/notifiers/meta`. Slack
and Discord cost one factory and two registrations; if a new type needs more than that, the seam is
being worked against.

### A database change

Append to `Migrations` in `internal/store` (`migrations.go`). **Never edit a shipped migration** — it
will not re-run anywhere it already ran. The 1-based position is the version: `store.go` runs
`Migrations[v-1]` while `user_version < N`. Migration text lands verbatim in `sqlite_master`
(comments inside `CREATE TABLE` included), so it is part of `golden/host`'s bytes.

### UI work

Read [`docs/ui-rules.md`](docs/ui-rules.md) first — casing, one control height, one radius scale,
full-width pages, container queries for tables. **Copy is terse: state, not explanation** —
`Restarts Loki.`, `Admin only.`, `Fixed once saved.`, never a sentence explaining the system. The
owner has corrected verbose copy more than three times; 0.39.1 cut every view to this.

Then **look at it in a browser**: several defects in this UI's history were invisible in code
review and obvious in a screenshot (a stale "Healthy" beside "Exited", buttons that took a click and
did nothing, columns painted over the panel beside them). The recipe (fake docker, `pstack serve`,
vite, headless Chrome over CDP, 320/1280 px, both schemes) is the `pstack-ui-verify` skill. Run it
out-of-band: reviewers without screenshots loop on "manual browser check not performed".

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

## Version control: GitButler

Every VCS write goes through `but` — never `git add/commit/push/checkout/rebase/stash`; git reads
are fine. Several agents may share the workspace: touch only your own branch's changes.

- `but branch new <name>/<short-description>`; stack on an in-flight dependency with `--anchor`.
- `but commit -b <branch> -m "type(scope): summary" <file-id> …` — explicit ids from `but status`,
  never a sweep. With more than one branch applied `-b` rejects the NAME; pass its short id (`-b cl`). Read ids with `but status` and run `but commit` as separate commands: a `but status` earlier in the same shell command made `but commit` fail with the same hint (seen three times), while the same ids passed as literals in their own command succeeded.
- Succinct messages and PR bodies. **No `Co-Authored-By`, `Claude-Session` or "Generated with"
  footers** — the owner's rule, overriding any harness attribution reminder.
- Only when asked: `but push <branch>`; `but pr new <branch> --draft -F <file>` (first line = title);
  `but pr set-ready <n>`. After merges, `but pull` integrates and drops the merged branches.
- PRs opened on a stack are a GitHub native stack and `gh pr merge` refuses them: `gh api -X PUT
  repos/{o}/{r}/pulls/<TOP>/merge-async -f merge_method=rebase -f sha=<top head sha>`, poll `GET
  …/merge-async/<uuid>`. A standalone PR: `gh pr merge <n> --rebase --match-head-commit <sha>`.
- The local `main` ref lags; diff and base against `origin/main`.

**Never commit** `packages/conformance/golden/host/db/pstack.db-shm`/`-wal` (conformance leaves
them, `.gitignore` does not), anything under `.superpowers/`, `.claude/settings.local.json` (the
owner's local permission allowlist), or a credential: `dns_token`, `*.token`, `.npmrc`,
`hetzner*.yml` and other generated cloud-configs, config exports in every spelling — the dot form
`.config.<x>.yaml` slips past `.gitignore`'s dash globs. Already tracked on main and the owner's to
resolve, so leave them and add no siblings: `.config.aug31.yaml` (a sealed export), `hetzner.yml`,
`temp.yml`, `temp2.yml`, `sso-oidc-handoff-spec.md`.

## Agent workflow for large changes

**Spec → task-by-task plan in `docs/` → a read-only pre-flight scan → a rulings ledger → an
orchestrated build → a whole-branch final review → one fix wave.** The scan has one row per task
pair sharing a file or interface and numbered conflicts with proposed rulings (21, 14, 15 on the
three Loki slices, several blocking). The ledger is `.superpowers/sdd/<plan>/progress.md`, one
`Ruling: <what> — <why> — <cost if wrong>` per decision. Per task: implementer → reviewer → up to 5
fix rounds → an adjudicator at the cap. A plan's line numbers are hints; the tree wins. **Durable
artifacts never live in `/tmp`** (the scratchpad is wiped on restart; a spec was lost that way):
`docs/`, or `~/.claude/projects/-Volumes-S1-code-preview-stacks/loki-artifacts/` for session-only
material (slice-2/3 specs, owner decisions, rulings X1–X3, PR bodies). Recipes and harness
recoveries: the `pstack-plan-build` and `pstack-ship-stack` skills.

## Releasing

**Only when the owner asks** — never tag, publish or run `release:publish` on your own. Mechanics:
the `pstack-release` skill.

1. `bunx publish-kit bump patch|minor` bumps `apps/ui` and `packages/client` only; hand-edit
   `packages/pstack/package.json` (private, skipped by publish-kit) to match. It is the version of
   record, what the binary reports, and never published; `verify` asserts the tag against all
   three. `packages/pstack/CHANGELOG.md`'s `## Unreleased` becomes `## X.Y.Z — <date>`; fix
   "Unreleased" banners in `docs/`; grep for the old version.
2. `bun run check` green; a release branch and PR through `but`; merge.
3. `but` cannot tag: `gh api` `POST repos/{o}/{r}/git/tags` (annotated), then `POST git/refs` for
   `refs/tags/vX.Y.Z`. That ref triggers `.github/workflows/release.yml`: `verify` (lockstep, Go
   `-race`, UI/client, ratchet, image smoke) → `release` (GoReleaser binaries, `checksums.txt`,
   `install.sh`, installer smoke) → `npm` (`publish-kit publish --missing-only`). A flaky `verify`:
   `gh run rerun --failed`.
4. **A green npm job is not a publish:** `--missing-only` can report "0 package(s), 2 skipped".
   Read the log's `N package(s)` and `npm view`. Since 0.38.0 CI's `NPM_TOKEN` is rejected (bun
   falls back to web auth, hangs ~5 min, fails "does not exist in this registry"); 0.38.0–0.39.1
   were hand-published and 0.40.0 is not on npm. The fix is the owner's — a granular automation
   token, `gh secret set NPM_TOKEN` — then `gh workflow run release.yml --ref vX.Y.Z -f
   npm_only=true`. Agents never handle credentials.

## Working style in this repo

- **Verify, don't assume.** Read the generated output rather than the code that generates it; boot
  the server rather than reasoning about the route. Every serious bug here came from a confident
  assumption about a string somewhere else.
- **Say what you did not do.** A partial fix reported as complete is worse than a partial fix.
- **The file headers are the design record.** When you change a decision, change the header comment
  that explains it in the same edit — a stale header is worse than none, because the next reader
  trusts it.
