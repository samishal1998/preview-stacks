# ui/

The web UI the control plane serves. **One file** — [`index.html`](index.html) — Vue 3 from the CDN,
a hash router in about thirty lines, no build step, no bundler, no other dependencies.

It drives the **deployment registry**: see the control stack, list what has been submitted to this
host, submit or replace a spec, run `up` / `verify` / `down`, follow the job log live, and read the
step-by-step outcome — including the one outcome that matters most, **leaked**.

## Why no build step

The API serves this document and nothing else. A bundler would add a toolchain, a `node_modules`, a
watch mode and a CI step to produce one HTML file that a `<script src>` already produces — and it
would put a build artifact between an operator and a hotfix. Editing `index.html` and reloading the
tab is the whole loop.

The tradeoffs are real and accepted: no components, no TypeScript, no tests, one global stylesheet,
and the CDN must be reachable on first load (it is cached afterwards). At this size that is cheaper
than the toolchain.

## Why it is embedded into the bundle

`src/api.ts` inlines it at **build** time:

```ts
import UI_HTML from '../ui/index.html' with { type: 'text' };
```

The published package is a single bundled file, so reading `ui/` from a path relative to the source
would point at something that does not ship. Inlining also removes a whole class of "works from the
repo, 404s once installed" bug, and leaves no filesystem lookup — hence no path traversal to contain.

Two consequences for anything you add here:

- **No external assets.** No separate `.js` or `.css`, no images, no icon fonts. Inline SVG if you
  need a glyph.
- **Every non-`/api/` path serves this document**, so a deep link (`/deployments/pr-7/actions`) must
  *render*, not 404. That is why routing is by `#hash` and why an unrecognised hash renders a
  "no such view" panel instead of a blank page.

## Views

Routed by hash, so deep links work and the back button behaves.

| Route | What it is for |
|---|---|
| `#/` | **Overview** — the control stack's services beside deployment counts and recent jobs |
| `#/deployments` | **list**: id, **kind**, resolved stack, state, updated |
| `#/deployments/<id>/<tab>` | **detail** — `overview` · `config` · `axes` · `requires` · `logs` · `actions` |
| `#/submit`, `#/submit/<id>` | **Submit** — spec + optional compose + an `env` editor, and the example spec |
| `#/jobs`, `#/jobs/<id>` | **Jobs** — history, then one job's live SSE log and its step table |

## Routes it consumes

| Route | Used for |
|---|---|
| `GET /api/health` | `version`, `dataDir`, and `authEnforced` (loopback-only mode) |
| `GET /api/control` | the read-only control-stack panel |
| `GET /api/deployments` | the list. Polled every 5s while Overview or Deployments is open |
| `GET /api/deployments/:id?VAR=…` | detail: kind, resolved `stack`, `compose`, `requires`, `axes`, `env` |
| `GET /api/deployments/:id/logs?tail=N` | the Logs tab. On demand only — never polled |
| `PUT /api/deployments/:id` | submit — body `{ spec, compose?, env? }` → `201` created / `200` replaced |
| `POST /api/deployments/:id/{up,down,verify}` | `202 { job }`, then the UI navigates to that job |
| `DELETE /api/deployments/:id` | forget the record |
| `GET /api/jobs`, `GET /api/jobs/:id` | history, and the finished job's `outcome.steps` |
| `GET /api/jobs/:id/stream` | SSE live log; a final `{done:true,state}` frame ends it |

One `fetch` wrapper handles all of them: it attaches the bearer token to **every non-GET** request
(`PUT` and `DELETE` destroy as thoroughly as `POST`; GETs stay open, which is also why the log can
use `EventSource` — it cannot send headers), parses the body, and turns a non-2xx into one sentence
that **names the call**. A bare `404` says both possibilities out loud — "no such resource, or this
build of the API has no such route" — rather than the page crashing on a missing field. A response
that is not JSON says so instead of throwing: that means a proxy answered, not this API.

---

# The safety rules this file encodes

Everything below is load-bearing. If you refactor, port these first — they are the parts that carry
meaning rather than markup.

## 1. The control stack is shown and is never actionable

`GET /api/control` returns `actionable: false`, always. There is deliberately no `up`/`down`/`verify`
for it: **the API runs inside that stack**, so acting on it would kill the process doing the work,
and a failed self-upgrade leaves the host with no control plane and no remote way to fix it. But
being unable to *see* it is just a blind spot — an operator debugging "why is my preview
unreachable" needs to know whether Traefik is up.

So the panel renders the services table, and puts the server's `note` **exactly where an operator's
eye goes looking for those buttons**. Never add an action there, not even a "safe" one.

`reachable: false` means *docker did not answer* — which is **not** "nothing is running". The panel
says so, and distinguishes three states that must not be collapsed: unreachable, `parseError`, and a
genuinely empty service list.

## 2. `busy` / `running` / `stack` are tri-state — unknown is a state

The server sends `null`, never `false`, when it could not determine them: an unresolved spec has no
stack name to look up, and a docker that did not answer is not the same fact as "nothing is running".
The UI normalises with `?? null` (never `|| null`) and renders **unknown**, with its own count on the
Overview. Rendering unknown as "idle" is how a live stack gets reported as torn down.

A row may also carry `unresolved` — the resolve error, as a string. It is printed verbatim, with the
reason it appears: the listing resolves every spec with the *same* (empty) variable set, so anything
with a `${VAR}` in it always lands there.

## 3. Variables must match between `up` and `down`

A spec interpolates `${VAR}` **once**, at resolve time, and the registry stores the spec but **not**
the variables — every route layers the request's query parameters over `process.env`. So `up` with
`PR=7` and a later `down` without it resolve to two *different* stacks, and the teardown quietly
misses everything the deploy created.

The per-deployment editor persists its pairs in `localStorage`, keyed by deployment id, and sends
them as query parameters on every GET/POST/DELETE. A successful submit seeds them from the `env`
block used to validate. The warning stays visible because this is convenience, not a guarantee: a
different browser, a CI job, or the CLI must pass the same ones itself.

A missing variable is a **400 naming it**. That is not a dead end — the load handler sends the
operator to the `config` tab so the editor is in reach, and everything else on the page stays
honestly unavailable until the spec resolves (including `DELETE`, which needs the stack name to count
containers).

The `env` table lists what the spec **declares**, which is not what it **needs** — `stack: pr-${PR}`
consumes `PR` without declaring it. Hence a free-form editor, and the 400 text as the real work order.

## 4. Masked values have nothing to reveal

`visibility: 'masked'` means the host redacted it **by design**, deny-by-default by name
(`src/redact.ts`). The plaintext was never in the response. So masked values render muted with their
`length` ("19 chars"), and a note that the real value never left the host.

**Never add a reveal affordance.** There is nothing to reveal, and building one would mean sending
secrets into a browser tab, a screenshot, and a support ticket. `length: 0` on a masked variable is
the interesting case — declared but never set — and gets its own badge.

## 5. `kind: shared` — the blast radius, and a typed confirmation

`down` runs `docker compose down -v`, and `-v` removes **volumes**. On a shared singleton that
destroys state every tenant depends on — the TLS store, the shared database or queue, admin
credentials — using the identical verb that is routine for a preview.

`shared` therefore gets a *filled* amber badge everywhere (`isolated` is a quiet blue outline), and
the Actions tab:

1. states the blast radius before the button is usable,
2. keeps `down` **disabled** until the operator types the resolved stack name exactly — a checkbox is
   one stray click, typing the name is a decision,
3. sends `force: true` only then, and
4. re-arms after every attempt.

This is **defence in depth, not the guard.** `down()` in `src/stack.ts` refuses without `force`, and
the API answers `409` before starting a job at all.

## 6. Not every 409 is "busy" — discriminate by shape, never by text

Four different conditions answer `409`, and guessing wrong is misleading:

| Payload carries | Means | UI does |
|---|---|---|
| `kind` | shared `down` refused without `force` | route to the typed confirmation |
| `containers` | `DELETE` refused, containers still exist | "run `down` first", explain the orphan risk |
| neither | a job is in flight **or** docker would not answer | print the message verbatim; offer *attach* only if a running job with that stack really exists in `GET /api/jobs` |

The last two are shape-identical, so they are not classified — the server's own message is shown.
**Nothing is ever retried.** One job per stack is deliberate, not a queue: a `down` racing an `up`
over the same database branch is corruption, not contention.

## 7. The four step states must never blur

`outcome.steps[]` distinguishes states the CLI's `report()` also separates. Do not collapse them:

| State | Means | Colour |
|---|---|---|
| `ok` ✓ | passed. A `down` step is **always** `ok:true` and may still carry a `non-fatal:` note — teardown is best-effort by design, so it really did pass | green |
| `failed` ✗ | any other non-zero step, including a refused shared `down` | red |
| `leaked` ! | `assert_gone` failed: the resource **survived teardown** | amber, **filled**, named in a banner |
| `unverifiable` ? | no `assert_gone` defined — **not** a pass | neutral grey |

`leaked` is the whole reason this tool exists (CLI exit code `2`, distinct from `1`), so its badge is
filled rather than outlined: it must be unmistakable next to `failed` and `ok` at a glance. An `ok`
job containing unverifiable steps says so rather than implying proof, and the Axes tab flags
`verifiable: false` up front.

The test is `message.startsWith('unverifiable')`, matching `report()` — **not** `skipped` alone, which
is also true of a dry run. The step column is headed *axis / requirement* because `requires` steps put
a requirement name there.

## 8. Errors are the documentation

A `SpecError` from `PUT` is rendered verbatim in a `<pre>`, newlines and indentation intact. It is the
tool's main teaching surface — it names the key, the variable, or the assert that is wrong, carries
the assert-lint warnings about an `assert_gone` that fails open, and often prints the corrected form:

```
spec rejected: axis "queue": `assert_gone` is a bare `! <probe>` with no reachability guard — it
will report "gone" if the probe itself fails. Fail closed instead:
      <probe-is-usable> || exit 1
      ! <probe-for-this-resource>
```

Never truncate it, never collapse it to one line, never replace it with "invalid spec".

## 9. Streams are closed on navigation; a dropped stream falls back

The `EventSource` is closed in the **route watcher**, not only on the `{done}` frame — otherwise
navigating away from a running job leaves the stream open, appending to an array nobody is watching.
A stream can also end *without* `done` (server restart, proxy timeout, a job outliving the server's
240s idle window), so `onerror` re-fetches the job record and says the tail may be missing, rather
than leaving the page pinned on "running" for ever. Job history is in memory: if the record is gone,
the UI says the server probably restarted.

Autoscroll disengages the moment the operator scrolls up, and re-arms at the bottom.

## 10. Polling is deliberate and narrow

Deployments and jobs poll every 5s **only** while a view that shows them is open, because `busy` is
useless if it is stale and the server holds the lock. Compose logs are **never** polled — an
auto-refresh against a chatty stack would hammer the host — so the Logs tab has a tail selector and a
manual Fetch, guarded on `compose !== null` rather than calling and interpreting the failure.

---

## Verifying a change

There are no tests here. What was actually done for the current version: run
`PSTACK_DATA=… PSTACK_PORT=7979 bun src/cli.ts serve`, submit a `shared` spec, an `isolated` spec
needing `${PR}`, and one with no `compose:` section plus an axis lacking `assert_gone`; then walk
every route and drive `up` → `verify` → `down`. The middle one leaks on purpose (`verify` after `up`
finds the resource still present), which is the only way to see the `leaked` banner and the `!`/`?`
rows side by side. Check the browser console is silent — a Vue template error is otherwise a blank
panel with no other symptom.

## Swapping in a Vite + Vue SPA later

Nothing in the server is coupled to this file beyond the `import … with { type: 'text' }`, so
replacing its contents is the entire migration — but the import means the *output must stay a single
self-contained HTML file* with no sibling assets. Do the swap when this file stops fitting on a screen
or two. Not before.
