# Documentation index

Every document in this repo, what it answers, and when to read it. Start with the row that matches
your question rather than reading in order — these are references, not a manual.

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
| Know how the **Go binary** (0.29.0) was proven a drop-in for the TypeScript one, and what still differs | [`port-status.md`](port-status.md) |

## The documents

### [`usage.md`](usage.md) — the task-oriented guide (~3000 lines)

The longest document and the one to search first. Written as a walk-through: install, write a spec,
add an isolation axis, deploy, read the step report, tear down, and then **deliberately sabotage a
teardown hook** to watch the leak gate catch it — that last section is the fastest way to understand
what this tool is actually for.

Later sections cover the control plane: `pstack init`, hostnames and TLS modes, `shared` vs
`isolated`, wildcard subdomains, the API with worked `curl` calls, jobs and their seven states, the
per-stack queue and the host-wide job cap, readiness, container actions, upgrading a host, and the
web UI. Then, one section per thing a host grows into:

| § | Covers | Since |
|---|---|---|
| 7b | **the two orchestrators** and how to switch, **swarm mode** (what the compose→swarm conversion changes, adding a worker), **sleep and wake-on-call** (the `sleep:` block, the catch-all router, the spinning-up page), and **share links** | 0.26.0 |
| 7c | **single sign-on** — several providers at once, the presets, who gets an account and with which role | 0.27.0 |
| 7d | **moving a host's configuration** to another host, sealed, including onto a machine that does not exist yet | 0.30.0 |
| 7e | **the four roles**, what each adds, and what sits outside the ladder | 0.32.0 |
| 10 | **runtime settings** — the job cap and the default role, changeable without a restart | 0.33.0 |

### [`control-plane.md`](control-plane.md) — the architecture (~1300 lines)

Why the control plane is shaped the way it is: the CLI/API split (and why `init` can never move into
the API), the registry as a cache of intent rather than a state store, what SQLite is allowed to
hold, the job model, the delivery rules for notifiers, and (§5e) why the sleep record is in the
registry, activity is in memory, the control stack stays a compose project under swarm, and a share
link is a JWT with no table. Read this before proposing a structural
change — most "obvious" restructurings are refused here with a reason.

### [`bootstrap.md`](bootstrap.md) — from nothing to a running host (~1100 lines)

The worked example is Hetzner + cloud-init, but the reasoning is provider-agnostic: DNS records,
which ports must be open, HTTP-01 vs DNS-01 and the rate limit that decides between them, the
socket-exposure tradeoff, and what to check when the certificate never arrives.

### [`webhook-events.md`](webhook-events.md) — every event a notifier receives (~450 lines)

The envelope, headers, and a worked signature-verification receiver. Delivery semantics: at-least-
once, the retry schedule, per-notifier queueing, and redelivery. Then a catalogue of **all 29 events** with
every payload field — deployments, jobs (including `job.leaked`, the one to page on), specs, routing,
readiness, container actions, sleep and wake, share links, and configuration import/export.

### [`tls-challenge.md`](tls-challenge.md) — switching HTTP-01 ↔ DNS-01 on a host that exists

A playbook, not a reference: what each mode *is* stays in `usage.md`. Four steps, the two footguns
(`init` re-renders from its arguments alone, and per-PR routers keep the labels they were deployed
with), and the rollback.

### [`ui-rules.md`](ui-rules.md) — the advanced UI's conventions (~100 lines)

Casing, alignment, spacing, roundness, width, tables, buttons. Read before touching
`apps/ui/` — every rule exists because its absence produced a specific visible defect.

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

Two important surfaces are documented in the code rather than here, deliberately — a doc that
duplicates a route table drifts from it:

- **The API's route list** lives in the header comment of
  [`packages/pstack/internal/api/server.go`](../packages/pstack/internal/api/server.go). It is the
  API's own documentation and is updated in the same edit as a route.
- **Why any given file is the way it is** lives in that file's header comment. They are long on
  purpose and explain *why*, not *what*. Read the header before editing the file.
