# preview-stacks

A **control plane for ephemeral preview stacks**, driven by a CLI. You describe the **isolation
axes** a preview needs — a database branch, a queue namespace, images, DNS — and `pstack`
provisions them, runs your Compose stack, tears it all down in reverse, and then **proves nothing
leaked**.

```bash
PR=123 pstack up        # provision axes in order, then compose up
PR=123 pstack down      # compose down (all profiles) → destroy axes in reverse → verify
PR=123 pstack verify    # assert every axis is gone; exit 2 if anything survived
```

## The control-plane model

One host runs three layers, and **which layer something is decides what may be done to it**:

```
  host  ──  pstack init  ──▶  CONTROL stack:  traefik + pstack (API + UI)
                                                        │ manages
                                    ┌───────────────────┴───────────────────┐
                                    ▼                                       ▼
                          SHARED deployments                      ISOLATED deployments
                          kind: shared                            kind: isolated
                          a DB, a queue, a mirror   ◀── requires ──  pr-123, pr-124, …
                          no axes · `down` guarded              axes · proven gone on `down`
```

| | control | shared | isolated |
|---|---|---|---|
| Managed by | the **CLI, on the host** | the API or the CLI | the API or the CLI |
| Axes | n/a | **none allowed** | yes — that is the point |
| `down` | not offered | refused unless forced | routine |

**The CLI owns the control stack; the API owns everything else.** The API must never run `up` on the
deployment containing the API — the process performing the upgrade is inside the stack being
replaced, so it is killed mid-operation, and a bad image leaves the host with no control plane and
no remote way to fix it. `pstack init` / `pstack self-upgrade` handle that from the host instead.

Full rationale, the registry contract, and the trust boundary:
**[docs/control-plane.md](docs/control-plane.md)**.

> Status: `kind`, `requires`, the `shared` `down` guard, the deployment registry, the
> registry-backed API (`/api/deployments/*`) and `pstack init` are all wired and reachable from the
> CLI. Everything has been exercised against `--dry-run` and the API end-to-end; **none of it has
> been run against a real Docker host yet** — `docs/control-plane.md` tracks the model.

## Why this exists

Self-hosted PaaSes already run a Compose file per PR and terminate TLS —
[Coolify](https://coolify.io), [Dokploy](https://dokploy.com), [Uffizzi](https://uffizzi.com) all do
that well, and **if that is all you need, use one of them.** This is not a PaaS and does not want to
be.

What they don't model is the *stateful* resources a preview needs around the containers:

- a **database branch** per PR, provisioned through a SaaS API, deleted-and-recreated rather than
  reused (a reused branch carries the previous commit's migrations)
- a **queue/workflow namespace** per PR on a shared cluster, because task-queue names are often
  compiled into the app and can't be overridden per deploy
- **per-PR images**, which `docker compose down -v` never removes
- **build-time-baked config** — a Next.js app with the backend URL compiled in needs its per-PR
  value as a *build arg*, not a runtime env var

Every team hand-rolls these as ad-hoc CI steps, and every hand-rolled version leaks, because the
scripts implement *destroy* and skip *verify*. `pstack` makes the axes declarative and makes the
leak check a first-class command.

## The spec

```yaml
version: 1
kind: isolated             # `isolated` (default) = one tenant · `shared` = a host singleton
stack: pr-${PR}            # → compose project name, hostname label, and $STACK in every hook

env:
  PREVIEW_DOMAIN: preview.example.com

compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]   # up enables these; down enables ALL of them

requires:                  # checked BEFORE anything is created — fails by name, not deep in a hook
  - name: ingress-network
    assert: docker network inspect preview-ingress >/dev/null 2>&1
    hint: bring the shared deployment up first

axes:                      # provisioned top→bottom, destroyed bottom→top
  - name: database
    up: |
      ./hooks/db.sh recreate "$STACK"
      echo "DATABASE_URL=$(./hooks/db.sh url "$STACK")"   # KEY=VALUE is captured into compose env
    assert_live: ./hooks/db.sh exists "$STACK"            # exit 0 ⇒ exists  (fail fast on up)
    down:        ./hooks/db.sh delete "$STACK"            # best-effort
    assert_gone: "! ./hooks/db.sh exists \"$STACK\""      # exit 0 ⇒ gone    (the leak gate)
```

See [`examples/preview.yml`](examples/preview.yml) for a fully-commented four-axis stack, and
[`examples/shared.yml`](examples/shared.yml) for a `kind: shared` singleton.

### `kind`, and why it is explicit

`kind: shared` marks a host singleton — a database, a queue cluster, a registry mirror. It may
declare **no axes** (a hard error if it does: axes exist to isolate one tenant from another and to
prove a tenant's resources are gone, and a singleton has neither concern), and its `down` is
**refused without `--force`**, because `compose down -v` there destroys the TLS store, the shared
database, the admin credentials — state every other deployment depends on.

The kind is declared rather than inferred from "does it have axes", because that guard is
load-bearing: an isolated deployment that forgot its axes must not silently inherit a singleton's
protection.

### The four hooks

| Hook | Contract | On failure |
|---|---|---|
| `up` | provision; must be **idempotent** (re-run on every redeploy) | **fatal** — stops the run |
| `assert_live` | exit 0 ⇒ resource exists | **fatal** — catches a provision that lied |
| `down` | destroy | **recorded, never fatal** |
| `assert_gone` | exit 0 ⇒ resource is gone | **fatal for `verify`** (exit 2) |

## The one idea: `down` is best-effort, `verify` is strict

This asymmetry is the whole design.

**`down` never aborts.** A resource may already be gone, an API may flake, a network may be busy.
Stopping halfway leaves *more* garbage than continuing, so every failure is recorded and teardown
carries on.

**`verify` refuses to be optimistic.** It asserts each axis is actually gone and exits non-zero if
not. An axis with no `assert_gone` is reported `unverifiable` rather than passing silently, so a
green `verify` means "everything that *can* be checked was checked".

Two leak classes it catches out of the box:

- **`down` omitting a profile.** Compose treats a non-enabled profile's services as absent, so
  tearing down with fewer profiles than you deployed leaves that profile's resources — most visibly
  one dead `<stack>_default` network per PR, forever. `pstack down` always passes **every** profile
  in the spec.
- **Images.** `down -v` removes containers, volumes and networks but never images, so per-PR layers
  accumulate until the disk fills — and disk pressure evicts the warm build cache, which is the real
  per-PR speed lever. That's an axis, not compose's job.

## Install

Requires [Bun](https://bun.sh) ≥ 1.3 (uses native `Bun.YAML`; no dependencies).

```bash
git clone <this repo> && cd preview-stacks && bun install
bun link                      # exposes `pstack`
# or run directly:  bun src/cli.ts --help
```

## Usage

```
pstack <up|down|verify|status|validate|serve> [flags]

  -f, --file <path>   spec file (default: preview.yml)
  -n, --dry-run       print what would run, change nothing
  -v, --verbose       echo commands and their output
  -q, --quiet         suppress per-step chatter
      --set K=V       override/define a variable (repeatable)
      --no-verify     down: skip the post-teardown leak check
      --force         down: allow tearing down a `kind: shared` deployment

serve env:  PSTACK_TOKEN (required to bind off-loopback) · PSTACK_PORT (7878)
            PSTACK_HOST (127.0.0.1) · PSTACK_VAR (PR)

Exit: 0 ok · 1 failed · 2 leaked · 3 bad spec/usage
```

`pstack validate` also **lints your asserts**, which is where the tool earns its keep:

```
! axis "queue": `assert_gone` is a bare `! <probe>` with no reachability guard — it will
  report "gone" if the probe itself fails. Fail closed instead:
      <probe-is-usable> || exit 1
      ! <probe-for-this-resource>
```

That mistake is the reason homegrown teardown checks lie: `! docker exec …` exits 0 when *docker
itself* is missing, so "I could not tell" is reported as "it is gone". `validate` also flags
`|| true` inside an assert (which makes it unable to ever fail) and any axis with `up` but no
`assert_gone`.

`2` is distinct from `1` on purpose: "torn down but something survived" and "teardown errored" are
different problems with different owners, and CI should be able to tell them apart.

Start with `--dry-run`. It prints every command in order without executing any of them, which is
also the fastest way to check your axis ordering.

### In CI

```yaml
- run: pstack up                       # on the deploy path
- run: pstack down                     # on PR close — fails the job if anything leaked
- run: pstack verify                   # or as a nightly sweep across stacks
```

## Design decisions

- **Hooks are shell strings, not a plugin API.** This tool orchestrates other people's CLIs
  (`docker`, `gcloud`, `curl`, `psql`); a structured interface would just move quoting into YAML. The
  spec lives in your repo at the same trust level as a CI workflow — it is **not** a sandbox
  boundary, and this tool is not safe to point at untrusted specs.
- **Interpolation happens exactly once**, at parse time, so a value containing `${...}` can never be
  re-expanded downstream.
- **An undefined variable is a hard error.** Expanding `pr-${PR}` to `pr-` when `PR` is unset gives
  every PR the same stack — collision instead of isolation. Failing loudly is the only safe default.
- **Axis order is declaration order**, reversed on teardown. Declare dependencies before dependents.
- **No state store.** The stack identity is derived from `${PR}`, and truth lives in Docker and in
  each axis's own `assert_*` probe. Nothing to get out of sync, nothing to migrate.

## API and UI

`pstack serve` exposes the same core over HTTP, plus a small Vue UI for submitting, monitoring and
tearing down stacks from a browser.

```bash
PSTACK_TOKEN=$(openssl rand -hex 16) PSTACK_HOST=0.0.0.0 pstack serve
```

Because `up`/`down` take minutes, mutating routes are **asynchronous**: a `POST` returns `202` with
a job id, and you poll `GET /api/jobs/:id` or subscribe to `GET /api/jobs/:id/stream` (SSE). One job
per stack at a time — a concurrent request gets `409` rather than being queued, because a `down`
racing an `up` over the same database branch is corruption, not contention.

| Route | Purpose |
|---|---|
| `GET /api/health` | liveness; auth mode, data dir, version |
| `GET /api/deployments` | every submitted deployment, with `busy` and `running` |
| `GET`/`PUT`/`DELETE` `/api/deployments/:id` | read · submit or replace (`{spec, compose?, env?}`) · forget (refused while containers exist) |
| `POST /api/deployments/:id/{up,down,verify}` | start a job → `202 {job}` / `409` if busy. `down` body: `{verify?, force?}` |
| `GET /api/jobs/:id` · `GET /api/jobs/:id/stream` | job transcript · live SSE log |

`:id` is a **registry id**, not a compose project name — the server owns the stored spec and
resolves `stack:` itself. A spec that interpolates `${PR}` needs it on every call
(`?PR=123`), and the *same* variables on `down` as on `up`, or teardown targets a different stack
than deploy created.

**Two security guards, because this API destroys infrastructure:** a bearer token
(`PSTACK_TOKEN`) on every mutating route, and — if no token is set — the server **refuses to bind
anything but `127.0.0.1`**, so an unauthenticated instance cannot be exposed by accident. Job
history is in-memory and unpersisted, consistent with the no-state-store rule.

[docs/control-plane.md §6](docs/control-plane.md#6-submitting-a-deployment) has the worked
`curl` flow and the five behaviours that surface is carrying — why variables are not persisted, why
`PUT` parses before it writes, and why `DELETE` fails closed.

See [docs/control-plane.md](docs/control-plane.md) for the architecture,
[docs/usage.md](docs/usage.md) for worked examples, and [docs/bootstrap.md](docs/bootstrap.md) to
build a host from scratch (Hetzner + cloud-init).

## Scope

**In:** the spec (`kind`, `requires`, axes), the CLI, leak verification, the HTTP API, the web UI,
and the **deployment registry** — a control plane holding many deployments addressed by id
(`src/registry.ts`; see [docs/control-plane.md](docs/control-plane.md)).

**Not built, deliberately:** a persistent job store, a plugin system, a reconciliation loop. The
registry is a *cache of intent* — truth lives in Docker and in each axis's `assert_*` probe — so
losing it means "I forgot what you asked for", not "the host is inconsistent". The spec has also not
yet been proven against a *second* project; until it expresses one without changes, extra surface is
a guess.

**Still out: multi-tenancy.** One spec, one Docker socket, one trust level; every caller who can
reach the API can do everything. Real multi-tenancy needs a per-tenant isolation boundary (separate
VMs/microVMs, or Kubernetes namespaces) plus a credential boundary — a different product. Tenant
ids, per-user auth or RBAC bolted onto `api.ts` would create the *appearance* of a boundary where
none exists, which is worse than having none.

**Probably never:** running untrusted user-supplied specs. Accepting a spec is accepting arbitrary
compose *and* arbitrary shell hooks — it is remote code execution by design, not by bug — and the
Docker socket mount is root-equivalent on the host. Note also that a shared Docker host has no
memory isolation unless you add `mem_limit` yourself; one greedy heap can OOM every stack on the box.

## Development

```bash
bun test           # 22 tests, incl. end-to-end leak detection against the real filesystem
bunx tsc --noEmit  # strict, noUncheckedIndexedAccess
bun run check      # both
```

MIT.
