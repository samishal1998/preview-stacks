# ui/

The web UI the control plane hosts. One file — [`index.html`](index.html) — Vue 3 from the CDN, no
build step, no bundler, no other dependencies.

It drives the **deployment registry**: list what has been submitted to this host, open one, submit
or replace a spec, run `up` / `verify` / `down`, and watch the job stream. Four screens, switched by
a single `screen` value — no router.

| Screen | What it is for |
|---|---|
| **list** | every deployment: id, **kind**, state, updated. Row click → detail. |
| **detail** | resolved stack name, compose, `requires`, axes + their hooks, and the three actions. |
| **submit** | a textarea that `PUT`s a spec (+ optional compose) with an `env` key/value editor. |
| **job** | live SSE log beside the detail screen, then the `outcome.steps` table. |

## Why there is no build step

The API's static branch serves this directory verbatim. A bundler would add a toolchain, a
`node_modules`, a watch mode and a CI step to produce one HTML file that a `<script src>` already
produces — and it would put a build artifact between an operator and a hotfix. Editing
`index.html` and reloading the tab is the whole loop.

The tradeoffs are real and accepted: no components, no TypeScript, no tests, one global stylesheet,
and the CDN must be reachable on first load (it is cached afterwards). At this size that is cheaper
than the toolchain.

## Routes it consumes

| Route | Used for |
|---|---|
| `GET /api/health` | `authEnforced` — the header says so when the server is loopback-only |
| `GET /api/deployments` | the list. Polled every 5s while that screen is open |
| `GET /api/deployments/:id` | detail: kind, resolved `stack`, `compose`, `requires`, `axes` |
| `PUT /api/deployments/:id` | submit — body `{ spec, compose?, env? }` |
| `POST /api/deployments/:id/{up,down,verify}` | start a job → `202 {job}` / `409` if busy. `down` body `{ verify, force }` |
| `GET /api/jobs` | used only to find the running job behind a `409`, so you can attach to it |
| `GET /api/jobs/:jobId` | the finished job, for `outcome.steps` |
| `GET /api/jobs/:jobId/stream` | SSE live log; a final `{done:true,state}` frame ends it |

Details worth knowing before you edit:

- **`:id` is the deployment id**, the registry's directory name — not the resolved stack name. The
  server owns the spec and resolves `pr-${PR}` itself.
- **`202` means accepted, not done.** The response carries `{ job: { id, … } }`; the work happens in
  the background and the log arrives over SSE.
- **`409` means a job is already in flight for that stack.** One lock per stack, so an `up` and a
  `down` cannot race over the same database branch. The UI shows it and offers to attach to the
  running job — it never retries. Jobs are keyed by the *resolved stack*, but a `409` body may name
  either the stack or the deployment id, so the attach lookup tries every name it holds
  (`body.stack`, `body.id`, the id, `detail.stack`). Attaching is the whole remedy on that screen;
  matching loosely beats offering no link.
- **Auth goes on every non-GET request** (`Authorization: Bearer`, kept in `localStorage`) — `PUT`
  destroys as thoroughly as `POST`. GETs are open, which is also why the log can use `EventSource`:
  it cannot send headers.
- **Every failure names the call it came from.** A bare `404` is ambiguous — "no such deployment" or
  "this API predates this UI" — so the message says both rather than the page crashing on a missing
  field.
- **Response shapes are read defensively.** `GET /api/deployments` accepts an envelope or a bare
  array; detail is read from either a flat body or `{deployment, spec}`; an axis may arrive
  pre-summarised (`hooks: [...]`) or raw, in which case the hook list is derived from the four hook
  keys. Add a field, don't rename one, and the UI keeps working.

## `busy` is server-provided — the UI never guesses it

On the **list**, `busy` is rendered only if the server sends it; otherwise the cell shows `—`.
That is deliberate. The registry stores just `{id, kind, createdAt, updatedAt}`, and the job lock is
keyed by the **resolved stack name** (`pr-123`), which an id alone cannot produce. Joining
`GET /api/jobs` to rows by id would silently mislabel every deployment whose id ≠ stack. On the
**detail** screen the resolved stack *is* known, so `job.stack === detail.stack` is a sound
correlation and is used for the badge.

Never render "idle" as a guess. Unknown is a state; say so.

## The kind badge, and the shared-`down` blast radius

`kind` is the most consequential thing on the list, so `shared` gets a *filled* amber badge while
`isolated` is a quiet blue outline — different at a glance, not just different in wording.

`down` runs `docker compose down -v`, and `-v` removes **volumes**. On a `kind: shared` deployment
that destroys state every tenant depends on — TLS certificates, the shared database or queue, admin
credentials — using the identical verb that is routine for a preview. So the detail screen:

1. explains the blast radius in a banner before the button is usable,
2. keeps `down` **disabled** until a confirmation checkbox is ticked,
3. sends `force: true` only for a shared deployment that was explicitly confirmed, and
4. re-arms the checkbox after every job, so the next teardown is a fresh decision.

This is **defence in depth, not the guard.** The server refuses a shared `down` without `force`
(`src/stack.ts`) and returns the refusal as a failed step whose message explains why — that text is
the second teaching surface after `SpecError`, and the step table renders it in full.

## The four step states

The step table distinguishes states the CLI's `report()` also separates. Do not collapse them:

| State | Means | Colour |
|---|---|---|
| `ok` ✓ | the step passed. A `down` step may still show a `non-fatal:` message — teardown is best-effort by design, so it really did pass | green |
| `failed` ✗ | a non-zero step other than `assert_gone` — including a refused shared `down` | red |
| `leaked` ! | `assert_gone` failed: the resource **survived teardown** | amber, named in a banner |
| `unverifiable` ? | no `assert_gone` defined for that axis — **not** a pass | neutral grey |

`leaked` is the whole reason this tool exists (it maps to CLI exit code `2`, distinct from `1`), so
it gets its own colour and its own banner listing the surviving axes. An `ok` job that contains
unverifiable axes says so rather than implying proof. The `unverifiable` test is
`message.startsWith('unverifiable')`, matching `report()` — not `skipped` alone, which is also true
of a dry run.

The column is headed **axis / requirement** because `requires` steps put a *requirement* name there,
not an axis.

## Errors are the documentation

A `SpecError` from `PUT` is rendered verbatim in a `<pre>`, newlines and indentation intact. It is
the tool's main teaching surface — it names the key, the variable, or the assert that is wrong, and
often prints the corrected form:

```
spec rejected: axis "queue": `assert_gone` is a bare `! <probe>` with no reachability guard — it
will report "gone" if the probe itself fails. Fail closed instead:
      <probe-is-usable> || exit 1
      ! <probe-for-this-resource>
```

Never truncate it, never collapse it to one line, never replace it with "invalid spec".

## Not built here

- **Forget / `DELETE`.** The registry can forget a deployment, but the UI does not offer it:
  forgetting while containers still run orphans them beyond the control plane's view, which is the
  exact leak this project exists to prevent. Use the CLI once the semantics ("must be down first")
  are settled.
- **Recent-jobs list.** `GET /api/jobs` is called only to resolve a `409` into an attachable id.
- **Hash routing**, deep links, and per-deployment dashboards.

## Swapping in a Vite + Vue SPA later

Nothing in the server is coupled to this file: it serves `ui/` as static files and 404s anything
missing, so replacing the contents is the entire migration.

```bash
npm create vite@latest ui-src -- --template vue-ts
# vite.config.ts:
#   build: { outDir: '../ui' }        # NOT emptyOutDir — it would delete this README
#   server: { proxy: { '/api': 'http://127.0.0.1:7878' } }   # dev server, real API
npm --prefix ui-src run build     # writes ui/index.html + ui/assets/*
```

Worth porting first, because they are the parts that carry meaning rather than markup:

1. the four step states above, and the leaked banner,
2. the shared-`down` confirmation gate and its blast-radius copy,
3. `202` / `409` handling — accept-then-stream, and never retry a conflict,
4. `SpecError` rendered whole,
5. `busy` from the server only, `—` when unknown,
6. autoscroll that stops when the operator scrolls up.

Do the swap when this file stops fitting on a screen or two. Not before.
