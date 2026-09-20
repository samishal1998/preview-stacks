# Documentation index

Every document in this repo, what it answers, and when to read it. Start with the row that matches
your question rather than reading in order — these are references, not a manual.

**For agents:** [`../AGENTS.md`](../AGENTS.md) is the working context. [`../CLAUDE.md`](../CLAUDE.md)
adds the project skills in `.claude/skills/` (release, ship a stack, plan a build, check the UI in a
browser); the version-control rules are in AGENTS.md.

## Start here

| If you want to… | Read |
|---|---|
| Understand what pstack **is** and why it exists | [`../README.md`](../README.md) — the design doc |
| **Use** it: write a spec, deploy, tear down, prove it's clean | [`usage.md`](usage.md) |
| Stand up a **host** from an empty cloud account | [`bootstrap.md`](bootstrap.md) |
| **Change the code** (you are an agent, or new to the repo) | [`../AGENTS.md`](../AGENTS.md) |
| Call the API **from a script** | [`../packages/client/README.md`](../packages/client/README.md) |
| Receive **events** in your own service | [`webhook-events.md`](webhook-events.md) |
| **Move a host from HTTP-01 to DNS-01** (certificates are slow, or the weekly limit bites) | [`tls-challenge.md`](tls-challenge.md) |
| Give a team **accounts that cannot do everything** | [`usage.md` §7e](usage.md#7e-who-can-do-what-the-four-roles-0320) |
| Let people sign in with **GitHub, Google, Okta…** | [`usage.md` §7c](usage.md#7c-sign-in-with-your-identity-provider-0270) |
| **Copy a host's configuration** onto another host | [`usage.md` §7d](usage.md#7d-move-a-hosts-configuration-to-another-host-0300) |
| Send deployed services' logs to **Loki** and read them in **Grafana** | [`usage.md` §7](usage.md#turn-loki-logging-on-or-off-pstack-logging) |
| Know how **Loki logging** works as built, before changing it | [`loki-logging-as-built.md`](loki-logging-as-built.md) |
| Add and remove **worker machines** from what pstack reports | [`node-signals-design.md`](node-signals-design.md) |
| Know how the **Go binary** (0.29.0) was proven a drop-in for the TypeScript one, and what still differs | [`port-status.md`](port-status.md) |

## The documents

### [`usage.md`](usage.md) — the task-oriented guide (~3500 lines)

The longest document and the one to search first. Written as a walk-through: install, write a spec,
add an isolation axis, deploy, read the step report, tear down, and then **deliberately sabotage a
teardown hook** to watch the leak gate catch it — that last section is the fastest way to understand
what this tool is actually for.

Later sections cover the control plane: `pstack init`, hostnames and TLS modes, `shared` vs
`isolated`, wildcard subdomains, the API with worked `curl` calls, jobs and their seven states, the
per-stack queue and the host-wide job cap, readiness, container actions, upgrading a host, the
control stack's runtime view and `dns-persist-01` (0.35.0), Loki logging and Grafana (0.40.0), and
the web UI. Then, one section per thing a host grows into:

| § | Covers | Since |
|---|---|---|
| 7b | **the two orchestrators** and how to switch, **swarm mode** (what the compose→swarm conversion changes, adding a worker), **sleep and wake-on-call** (the `sleep:` block, the catch-all router, the spinning-up page), and **share links** | 0.26.0 |
| 7c | **single sign-on** — several providers at once, the presets, who gets an account and with which role | 0.27.0 |
| 7d | **moving a host's configuration** to another host, sealed, including onto a machine that does not exist yet | 0.30.0 |
| 7e | **the four roles**, what each adds, and what sits outside the ladder | 0.32.0 |
| 10 | **runtime settings** — the job cap and the default role, changeable without a restart | 0.33.0 |

### [`control-plane.md`](control-plane.md) — the architecture (~1500 lines)

Why the control plane is shaped the way it is: the CLI/API split (and why `init` can never move into
the API), the registry as a cache of intent rather than a state store, what SQLite is allowed to
hold, the job model, the delivery rules for notifiers, and (§5e) why the sleep record is in the
registry, activity is in memory, the control stack stays a compose project under swarm, and a share
link is a JWT with no table; and (§5g/§5h, 0.40.0) how Loki is discovered from docker, where the
driver block is injected, the `loki-apply` job with its rollback and resume, and Grafana sign-in
through forwardAuth. Read this before proposing a structural change — most "obvious" restructurings
are refused here with a reason.

### [`bootstrap.md`](bootstrap.md) — from nothing to a running host (~1100 lines)

The worked example is Hetzner + cloud-init, but the reasoning is provider-agnostic: DNS records,
which ports must be open, HTTP-01 vs DNS-01 and the rate limit that decides between them, the
socket-exposure tradeoff, and what to check when the certificate never arrives.

### [`webhook-events.md`](webhook-events.md) — every event a notifier receives (~450 lines)

The envelope, headers, and a worked signature-verification receiver. Delivery semantics: at-least-
once, the retry schedule, per-notifier queueing, and redelivery. Then a catalogue of **all 30 events** with
every payload field — deployments, jobs (including `job.leaked`, the one to page on), specs, routing,
Loki's settings, readiness, container actions, sleep and wake, share links, and configuration
import/export.

### [`tls-challenge.md`](tls-challenge.md) — switching HTTP-01 ↔ DNS-01 on a host that exists

A playbook, not a reference: what each mode *is* stays in `usage.md`. Four steps, the two footguns
(`init` re-renders from its arguments alone, and per-PR routers keep the labels they were deployed
with), and the rollback.

### [`ui-rules.md`](ui-rules.md) — the advanced UI's conventions (~130 lines)

Casing, alignment, spacing, roundness, width, tables, buttons. Read before touching
`apps/ui/` — every rule exists because its absence produced a specific visible defect.

### [`mcp-design.md`](mcp-design.md) — an unbuilt design, kept as a record

**Nothing in it is built.** What serving the API as MCP tools would take (the generated command
tree's lock file is already the tool manifest), and the three decisions that gate it: the
dependency, what is exposed by default, and which credential it runs as. Read before proposing it,
so the reasoning is not re-derived.

### [`tls-modes-design.md`](tls-modes-design.md) — a design, mostly built

**Shipped in 0.35.0:** the runtime view of the control stack, and `dns-persist-01`'s manual half —
you store a wildcard pair you obtained yourself, certificates reach Traefik as files, and the mode
is derived from what is stored. **Unbuilt:** the sidecar that would issue and renew that pair with
the DNS token. The load-bearing finding: the file provider is already the API's writing hand, so
most of it needed no init and no Traefik restart.

### [`loki-logging-design.md`](loki-logging-design.md) — a design in three slices, all built

**All three slices shipped in 0.40.0.** Loki as a logging
option: a Loki container in the control stack, the Grafana Loki Docker plugin on every node, and a
`logging:` block pstack adds to every deployed service that has none — pushed through Traefik with
basic auth (slice 1); Loki's settings in the UI: chunking, retention, filesystem or S3 (slice 2); and
Grafana at `grafana.<domain>`, signed in with pstack accounts through Traefik's forwardAuth (slice 3).
Each slice's spec is kept, with where the build differs at the top. The current state is
[`loki-logging-as-built.md`](loki-logging-as-built.md); read this one for what was approved and
why. Build plans, task by task:
[`loki-logging-slice-1-plan.md`](loki-logging-slice-1-plan.md),
[`loki-logging-slice-2-plan.md`](loki-logging-slice-2-plan.md) and
[`loki-logging-slice-3-plan.md`](loki-logging-slice-3-plan.md).

### [`loki-logging-as-built.md`](loki-logging-as-built.md) — Loki logging as it stands

What 0.40.0 actually ships for logging, across all three slices, in one place: the parts, the
decisions behind them, and the caveats a change can trip over. Read it first before touching
logging, the control template, the join material, Loki's settings, or Grafana sign-in; go to the
design doc and the plans for the reasoning behind a specific ruling.

### [`secret-exposure.md`](secret-exposure.md) — a closed finding, kept as a record

Unauthenticated reads used to be a credential feed, because job outcomes carry captured credentials
by design. **Resolved in 0.10.0** by requiring auth on every route. Kept because the *reason*
`outcome.outputs` holds credentials is still load-bearing, and anyone proposing to relax auth needs
to read why it was tightened.

## Package READMEs

| Package | What it is |
|---|---|
| [`packages/pstack`](../packages/pstack/README.md) | The CLI and the API — one static Go binary, released on GitHub (not npm since 0.29.0). |
| [`packages/client`](../packages/client/README.md) | `@samyx/preview-stacks-client` — a zero-dependency typed API client, plus `verifyWebhook` for your receiver. |
| `apps/ui` | The advanced UI (Vue 3 SPA), published as `@samyx/preview-stacks-ui`. Conventions in [`ui-rules.md`](ui-rules.md). |

## Where the docs are NOT

Three surfaces are documented in the code rather than here, deliberately — a doc that
duplicates a route table drifts from it:

- **The API's own description** is [`packages/pstack/api/openapi.yaml`](../packages/pstack/api/openapi.yaml),
  which generates `pstack api` and is served by every host at `/api/openapi.yaml` and
  `/api/openapi.json`. It is the route list of record: `internal/api/openapi_coverage_test.go` reads
  the route table out of the source and fails on a route missing from the document (unless
  `notInTheSpec` gives a reason) and on a path in it that no route serves.
- **Route order and who may call what** live in `packages/pstack/internal/api`: `routes.go` is the
  ordered if-chain (a greedy pattern is reachable only if nothing above it matched), and
  `permissions.go` the default-deny role table, whose test fails on a dispatched route with no row.
- **Why any given file is the way it is** lives in that file's header comment. They are long on
  purpose and explain *why*, not *what*. Read the header before editing the file.
