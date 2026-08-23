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

## The documents

### [`usage.md`](usage.md) — the task-oriented guide (~1900 lines)

The longest document and the one to search first. Written as a walk-through: install, write a spec,
add an isolation axis, deploy, read the step report, tear down, and then **deliberately sabotage a
teardown hook** to watch the leak gate catch it — that last section is the fastest way to understand
what this tool is actually for.

Later sections cover the control plane: `pstack init`, hostnames and TLS modes, `shared` vs
`isolated`, wildcard subdomains, the API with worked `curl` calls, jobs and their states, readiness,
container actions, stopping a job, upgrading a host, and the web UI. §7b (0.26.0) covers **swarm
mode** (what the automatic compose→swarm conversion changes, adding a worker), **sleep and
wake-on-call** (the `sleep:` block, the catch-all router, the spinning-up page) and **share links**.

### [`control-plane.md`](control-plane.md) — the architecture (~1000 lines)

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

### [`webhook-events.md`](webhook-events.md) — every event a notifier receives (~370 lines)

The envelope, headers, and a worked signature-verification receiver. Delivery semantics: at-least-
once, the retry schedule, per-notifier queueing, and redelivery. Then a catalogue of all ~20 events
with every payload field — deployments, jobs (including `job.leaked`, the one to page on), specs,
routing, readiness, and container actions.

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
| [`packages/pstack`](../packages/pstack/README.md) | The CLI, the API, and the library. Published as `@samyx/preview-stacks`. |
| [`packages/client`](../packages/client/README.md) | `@samyx/preview-stacks-client` — a zero-dependency typed API client, plus `verifyWebhook` for your receiver. |
| `apps/ui` | The advanced UI (Vue 3 SPA), published as `@samyx/preview-stacks-ui`. Conventions in [`ui-rules.md`](ui-rules.md). |

## Where the docs are NOT

Two important surfaces are documented in the code rather than here, deliberately — a doc that
duplicates a route table drifts from it:

- **The API's route list** lives in the header comment of
  [`packages/pstack/src/api.ts`](../packages/pstack/src/api.ts). It is the API's own documentation
  and is updated in the same edit as a route.
- **Why any given file is the way it is** lives in that file's header comment. They are long on
  purpose and explain *why*, not *what*. Read the header before editing the file.
