# Secret exposure on unauthenticated reads — open, undecided

**Status: UNDECIDED. Nothing here is fixed.** This records what is exposed, how it was measured, and
what the options cost, so the decision can be made deliberately rather than under time pressure. It
is deliberately separate from `control-plane.md`: that document describes the model as intended, this
one describes where the implementation does not match it yet.

Written 2026-07-29 against **0.2.4**, immediately after closing the same class of hole on
`GET /api/specs/:name` (0.2.4 changelog).

## The rule this is measured against

Reads are unauthenticated on this API **by design**. An operator should be able to open the dashboard
and see what is running before pasting a token — a control plane you cannot look at during an
incident is a control plane you will work around. Only mutating routes require the token.

That trade is fine for *state* (what exists, what is running, which axes a spec declares) and wrong
for *credentials*. The line, stated as a rule:

> An unauthenticated read may expose the **shape** of a deployment. It must not expose a value that
> would let the reader authenticate to something.

0.2.4 applied that rule to a spec's source. The routes below still violate it.

## What is exposed

Measured by booting the real server with a token set, running a real `up`, and reading each route
**with no `Authorization` header**. The spec used the documented inter-axis output pattern and a
hook that fails after printing a credential to stderr:

```yaml
axes:
  - name: database
    up: "echo DATABASE_URL=postgres://admin:sup3rs3cret@db.internal/app"
    assert_gone: "true"
  - name: failing
    up: "echo 'connecting with api_key=EXAMPLE-PLACEHOLDER-NOT-A-KEY' >&2; exit 1"
    assert_gone: "true"
```

| Unauthenticated read | Result |
|---|---|
| `GET /api/jobs/:id` | **leaks** — full connection string *and* the api key |
| `GET /api/jobs` | **leaks the same, for every retained job, with no id needed** |
| `GET /api/jobs/:id/stream` (SSE) | clean |
| `GET /api/deployments/:id` | clean |
| `GET /api/deployments/:id/logs` | partially defended — see below |

Two distinct carriers, both inside `Job.outcome`:

**1. `outcome.outputs` — the designed credential channel.** `up` captures every `KEY=VALUE` line a
provision hook prints (`captureOutputs`, `src/exec.ts`) and merges it into the environment of later
axes. That is the *documented* way to pass a freshly-created resource's address to the axis that
needs it, and the unit test for it asserts precisely `DATABASE_URL=postgres://x`. So this field holds
connection strings **by design**, and `Job.outcome` is returned whole:

```
outcome.outputs → {"DATABASE_URL":"postgres://admin:sup3rs3cret@db.internal/app"}
```

This is the more serious of the two. It is not an edge case a careless hook trips over; it is the
feature working as specified, published to anyone who can reach the port.

**2. `outcome.steps[].message` — the first line of a failed hook's output.** On failure `stack.ts`
records `firstLine(r.stderr || r.stdout)`. A hook that dies while handling a credential puts that
line here — and `set -x` in a hook, or any tool that echoes its own invocation, makes it likely
rather than unlucky:

```
steps[1].message → "connecting with api_key=EXAMPLE-PLACEHOLDER-NOT-A-KEY"
```

### What is NOT exposed (so this is not re-litigated)

- **The job log stream is clean.** `sink.emit('step', …)` writes phase labels only — `→ up: database`,
  `→ compose up (…)`, `→ verify …`. No hook bodies, no hook output. The SSE route was the first thing
  suspected and it is not the problem; the JSON job reads are.
- **Deployment reads carry hook *names* only** (`hooks: ['up','down','assert_gone']`), never bodies,
  and their `env` goes through `displayDeclared`, which masks by name.
- **Container logs go through `redactText`** (`src/redact.ts`) — it masks this process's own token,
  rewrites `scheme://user:password@host`, and blanks `NAME=value` where `NAME` reads as a secret. That
  is heuristic, not a guarantee (an app printing `Bearer eyJ…` sails through), but the route is
  defended rather than naked, and it is a *lower* priority than the job reads.

The inconsistency is itself the tell: container logs were recognised as "the one place a secret shows
up as free text" and given `redactText`, while `outcome`, which carries credentials *structurally*,
got nothing.

## Why this is not a one-line fix

**The obvious move — require a token on the job reads — breaks the dashboard.** The job list and job
detail are the incident-response surface; making them token-only means an operator watching a
teardown must authenticate first, which is exactly the friction the unauthenticated-read rule exists
to avoid. Whatever is chosen has to keep *state* readable while withholding *values*.

**And `EventSource` cannot send an `Authorization` header.** It is a browser API with no header
support, which is why the SSE route is a bare `GET` (`JobDetailView.vue` says so at the call site).
So "just authenticate the streaming route" is not available either, without changing how the client
streams. The stream is currently clean, so this constrains a *uniform* fix, not the immediate leak.

## Options

Not ranked; each has a real cost.

**A. Redact `outcome` on the way out, like container logs.** Run `outputs` values and
`steps[].message` through `redactText` in the response path, plus mask by key name (`displayDeclared`
already knows what a secret-looking name is — `outputs` keys like `DATABASE_URL` are exactly its
input shape).
*For:* no auth model change, dashboard keeps working, matches the precedent already set one route
over. *Against:* heuristic — a credential in a key the heuristic does not recognise still escapes.
Defence in depth, not a boundary.

**B. Withhold `outcome.outputs` and failure messages unless authenticated**, the 0.2.4 pattern:
metadata always, values with a token, `withheld: true` when held back so the UI says why instead of
rendering an empty object.
*For:* an actual boundary, and consistent with the fix just shipped. *Against:* a failed job's error
text is the single most useful thing on that page, and hiding it from an unauthenticated reader makes
the no-token experience notably worse. Arguably correct anyway.

**C. Stop putting credentials in `outputs` at all** — have provision hooks write to a side channel
the API never serialises, and treat captured `KEY=VALUE` as opaque.
*For:* removes the class rather than the symptom. *Against:* the largest change, and `outputs` is a
published contract that specs in the wild already rely on.

**D. Do nothing, and document the exposure as a deployment constraint** — "the API port must not be
reachable by anyone you would not give the token to".
*For:* honest, free, and arguably already true of a box that also exposes Traefik and Portainer.
*Against:* it is not what the current design *says*, and per-PR preview hosts do end up reachable.
If this is the answer it belongs in `bootstrap.md` as a firewall requirement, not left implicit.

A likely shape is **A now, B for `outputs` specifically, D written down regardless** — but that is a
suggestion, not a decision.

## Reproducing

The harness is not committed; it is ~40 lines. Boot `createServer` with a token on port 0, `PUT` the
spec above, `POST …/up`, poll until the job is not `running`, then `fetch` each route **with no
`Authorization` header** and grep the raw body for the planted values. Assert on the whole response
text, not a field — a leak anywhere in it is a leak. The pattern to copy is the
`'a spec source is a secret; its metadata is not'` suite in `packages/pstack/test/stack.test.ts`,
which boots the real server for the same reason: the authorization decision is a property of a live
request, and a unit test of the underlying function passes either way.

Whatever is decided, it wants tests of that shape, with a planted credential and an assertion on the
whole body.
