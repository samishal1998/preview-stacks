# ui/

The web UI `pstack serve` hosts. One file — [`index.html`](index.html) — Vue 3 from the CDN, no
build step, no bundler, no other dependencies.

```bash
PSTACK_VAR=PR pstack serve -f preview.yml     # → http://127.0.0.1:7878
```

It does what the CLI does, with a live log: load a target, run `up` / `down` / `verify`, watch the
job stream, read the step table. `down` sends `verify: true` unless you untick the box.

## Why there is no build step

`pstack serve` is a single Bun process whose static branch serves this directory verbatim
(`src/api.ts`). A bundler would add a toolchain, a `node_modules`, a watch mode and a CI step to
produce one HTML file that a `<script src>` already produces — and it would put a build artifact
between an operator and a hotfix. Editing `index.html` and reloading the tab is the whole loop.

The tradeoffs are real and accepted: no components, no TypeScript, no tests, one global stylesheet,
and the CDN must be reachable on first load (it is cached afterwards). At this size that is cheaper
than the toolchain.

## Routes it consumes

Everything comes from `src/api.ts`. Nothing else is called.

| Route | Used for |
|---|---|
| `GET /api/health` | the spec variable name (`varName`, default `PR`), spec path, and `authEnforced` |
| `GET /api/spec?<var>=<value>` | resolved stack name, compose file + profiles, axes and the hooks each defines |
| `GET /api/stacks` | compose projects on the host, with a `busy` flag per stack |
| `GET /api/stacks/:id/status` | `compose ps` for the loaded target |
| `POST /api/stacks/:id/up` | start an up job |
| `POST /api/stacks/:id/down` | start a down job — body `{ "verify": true \| false }` |
| `POST /api/stacks/:id/verify` | start a verify job |
| `GET /api/jobs` | recent transcripts, so a reload can re-attach to a running job |
| `GET /api/jobs/:jobId` | the finished job, for `outcome.steps` |
| `GET /api/jobs/:jobId/stream` | SSE live log; a final `{done:true,state}` frame ends it |

Details worth knowing before you edit:

- **`:id` is the variable value, not the stack name.** You POST to `/api/stacks/123/up`, not
  `/api/stacks/pr-123/up`; the server owns the spec and resolves `pr-${PR}` itself. That is why the
  stack list is informational — a project name cannot be turned back into a target.
- **The query key on `/api/spec` is the lowercased `varName`** (`?pr=123`), per api.ts.
- **`202` means accepted, not done.** The response carries `{ job: { id, … } }`; the work happens in
  the background and the log arrives over SSE.
- **`409` means a job is already in flight for that stack.** The server keeps one lock per stack so
  an `up` and a `down` cannot race over the same database branch. The UI shows it and offers to
  attach to the running job — it never retries.
- **Only POST/DELETE are authenticated.** The token goes out as `Authorization: Bearer <token>` on
  POSTs and is kept in `localStorage`. GETs are open, which is also why the log can use
  `EventSource` — it cannot send headers. When `authEnforced` is `false` the server is bound to
  127.0.0.1 and the header says so.
- **Jobs are in-memory and capped at 50.** A server restart loses the history, not correctness —
  truth about what exists lives in Docker and in each axis's `assert_*` probe.

## The four step states

The step table deliberately distinguishes states the CLI's `report()` also separates. Do not collapse
them:

| State | Means | Colour |
|---|---|---|
| `ok` ✓ | the step passed. A `down` step may still show a `non-fatal:` message — teardown is best-effort by design, so it really did pass | green |
| `failed` ✗ | a non-zero step other than `assert_gone` | red |
| `leaked` ! | `assert_gone` failed: the resource **survived teardown** | amber, named in a banner |
| `unverifiable` ? | no `assert_gone` defined for that axis — **not** a pass | neutral grey |

`leaked` is the whole reason this tool exists (it maps to CLI exit code `2`, distinct from `1`), so
it gets its own colour and its own banner listing the surviving axes. An `ok` job that contains
unverifiable axes says so rather than implying proof.

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

Then: keep `ui/` build output out of git (`.gitignore`) and build it in CI before packaging, or keep
committing it so `pstack serve` works from a fresh clone. The static branch resolves any path under
`uiDir`, so hashed `assets/*.js` filenames work as-is.

Worth porting first, because they are the parts that carry meaning rather than markup:

1. the four step states above, and the leaked banner,
2. `202` / `409` handling — accept-then-stream, and never retry a conflict,
3. re-attaching to a running job from `GET /api/jobs` after a reload,
4. autoscroll that stops when the operator scrolls up.

Do the swap when this file stops fitting on a screen or two — a client-side router, multiple views,
or per-stack dashboards are the honest triggers. Not before.
