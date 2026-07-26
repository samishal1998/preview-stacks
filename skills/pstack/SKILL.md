---
name: pstack
description: Use when setting up, configuring, or debugging ephemeral per-PR preview stacks with pstack — installing it, standing up the control stack with pstack init, choosing an ACME challenge (HTTP-01 vs DNS-01) and the per-PR TLS labels each mode requires, writing a preview.yml, defining isolation axes and requires preconditions, choosing shared vs isolated kinds, wiring up/down/verify into CI, or diagnosing leaked preview resources.
---

# Using pstack

`pstack` gives a per-PR preview stack a declarative lifecycle: you list the **isolation axes** the
preview needs (a database branch, a queue namespace, images, DNS), it provisions them in order, runs
your Compose stack, tears everything down in reverse — and then **proves nothing leaked**.

## Install

It is a **global package** — a ~74 KB bundle, no runtime dependencies, Bun ≥ 1.3 required
(`engines.bun`):

```bash
bun add -g @samyx/preview-stacks     # or: npm i -g @samyx/preview-stacks
pstack --help
```

The published tarball is **8 files / 0.36 MB unpacked**: `dist/cli.js`, `dist/index.js`, their
sourcemaps, and the four metadata files. **No source, no docs, no examples, no skills, no templates,
no `ui/`.** Two things follow that matter when you are helping someone:

- **Do not tell a user to clone the repo, run `bun install`, or `bun link`.** `bun src/cli.ts …` is
  the **contributor** path, for changing pstack itself. Users run `pstack`.
- **Nothing is read from a path next to the source at runtime.** The web UI and the control-stack
  compose template are `with { type: 'text' }` imports inlined into the bundle, so "it works from a
  checkout but 404s once installed" cannot happen — and there is no `ui/` directory on an installed
  host to look for, edit, or mount.

It is a bundle rather than a `--compile`d binary because a standalone executable embeds the Bun
runtime (~60 MB) per platform — a five-platform `optionalDependencies` matrix or a postinstall
download. There is no Node fallback to preserve: `Bun.serve`, `Bun.YAML`, `Bun.spawn` and `Bun.file`
have no Node equivalent.

```bash
PR=123 pstack up        # assert `requires`, provision axes in order, then compose up
PR=123 pstack down      # compose down (all profiles) → destroy axes in reverse → verify
PR=123 pstack verify    # assert every axis is gone; exit 2 if anything survived
PR=123 pstack validate  # parse, resolve interpolation, print warnings
PR=123 pstack status    # compose ps for this stack
pstack init --domain … --acme-email …   # HOST ONLY: stand up the control stack (§0)
pstack serve            # HOST ONLY: the API + UI over the deployment registry
```

## 0. The control-plane model — three layers, three sets of rules

Before writing a spec, work out **which layer** you are describing. It decides what you may declare
and what may be done to it.

| Layer | What it is | Declared as | Axes? | `down` |
|---|---|---|---|---|
| **control** | Traefik + the `pstack` API/UI, compose project `pstack-control`. One per host, at `control.<domain>` / `api.<domain>`. | **`pstack init`** on the host — never a spec you submit | n/a | **not offered** |
| **shared** | a host singleton every preview borrows: a database, a queue cluster, a registry mirror | `kind: shared` | **none allowed** — hard error | **refused unless explicitly forced** |
| **isolated** | one tenant, normally one PR | `kind: isolated` (default) | yes — the point | routine |

Two rules that follow:

1. **Never point a spec at the control stack.** The API cannot run `up` on the deployment containing
   the API — the process doing the work is inside the container being replaced, so it is killed
   mid-operation, the job transcript dies with it (jobs are in-memory), and a bad image leaves no
   control plane and no remote way back. The control stack belongs to **`pstack init`**, run from the
   host (over SSH, from systemd, or from CI-with-a-key); `pstack self-upgrade` is not built yet — it
   is `init` re-run after a fetch. **Nothing in the code enforces this**: the API cannot reliably know
   its own deployment id, so it is on you.
2. **`down` on a `kind: shared` deployment is refused unless you force it explicitly** — see §2.

If you are asked to "tear down the shared database" or "restart the preview host's Traefik", stop
and check which layer it is before running anything.

### Standing a host up: `pstack init`

Run **on the host**, idempotently — re-running is the supported way to change the domain, rotate the
API token, switch challenge mode, or move to a new image. The minimum needs **no DNS credential**:

```bash
pstack init --domain preview.example.com --acme-email <you>@example.com
```

| Flag / variable | Required | Notes |
|---|---|---|
| `--domain` / `PSTACK_DOMAIN` | yes | every hostname derives from it |
| `--acme-email` / `PSTACK_ACME_EMAIL` | yes | Let's Encrypt expiry mail |
| `--challenge http01\|dns01` / `PSTACK_CHALLENGE` | no | **default `http01`** |
| `--dns-provider <lego-code>` / `PSTACK_DNS_PROVIDER` | **`dns01` only** | ignored by `http01` |
| `PSTACK_DNS_TOKEN` | `dns01`, unless tokenless | env-only, no flag; written to `control/dns.env` (`0600`) |
| `PSTACK_TOKEN` | no | the **API bearer token**. Generated and printed **once** when unset |
| `PSTACK_IMAGE` | no | control image, default `pstack:local` (build it: `docker build -t pstack:local .`) |
| `PSTACK_DATA` | no | default `/var/lib/pstack` |

It checks preconditions by name first (Docker socket, Compose plugin, control image), creates
`<data>/deployments` and the two external networks **`preview-ingress`** and **`preview-shared`**
(both must be declared `external: true` in every per-PR compose file), writes its config `0600`,
brings `pstack-control` up, and then **waits for the container's healthcheck** — `compose up -d` exits
0 as soon as containers are *created*, so success is not proof of a live control plane.

`PSTACK_DNS_TOKEN` and `PSTACK_TOKEN` are **two different secrets with two different blast radii**:
one edits a DNS zone, the other drives an API holding a read-write Docker socket (root on the host).
Never reuse one as the other.

### Hostnames

| Hostname | Serves |
|---|---|
| `control.<domain>` | the web UI (a browser) |
| `api.<domain>` | the API (CI, `curl`, scripts) |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
| `<surface>-pr-<n>.<domain>` | a per-PR surface, e.g. `backend-pr-123.<domain>` |

`control` and `api` are two routers on **one** container — the API process serves the UI, and the UI
calls the API with **relative** `/api/…` paths, so it is same-origin from `control.<domain>` and needs
no CORS. `api.<domain>` exists to give external callers an honest name.

**Flatten per-PR hostnames with dashes.** A wildcard matches exactly **one** label:
`backend-pr-1.<domain>` is covered by `*.<domain>`; `backend.pr-1.<domain>` is **not**.

### TLS: HTTP-01 by default, DNS-01 when the arithmetic says so

| | `http01` (default) | `dns01` |
|---|---|---|
| Credential | **none** | a DNS API token to obtain, store, rotate |
| Hard requirement | **port 80 reachable from the internet** | the provider's API reachable |
| Traefik flags | `httpchallenge=true` · `httpchallenge.entrypoint=web` | `dnschallenge=true` · `dnschallenge.provider=<code>` |
| Wildcards | **impossible** — one cert per hostname | one `*.<domain>` covers everything |
| Ceiling | ~50 new certs/week per registered domain ⇒ **~16 PRs/week** at 3 surfaces each | none |
| URL valid before deploy | **no** | **yes** |
| Per-PR router labels | `tls=true` **+** `tls.certresolver=le` | `tls=true` **and nothing else** |

The web→websecure redirect does **not** break HTTP-01: Traefik installs an internal ACME router at
maximum priority that bypasses the redirect for `/.well-known/acme-challenge/`. The only hard
requirement is that **port 80 is reachable from the internet**.

**The arithmetic — this is the reason to migrate:**

```
3 surfaces per PR (backend, frontend, admin)  ×  1 cert each  =  3 new certs per PR
~50 new certs per registered domain per week  ÷  3            ≈  16 new PRs per week
```

Renewals do **not** count against that limit (a separate duplicate-certificate limit caps 5 identical
certs/week). Past ~16 new PRs a week, issuance starts failing and the symptom is a browser TLS error
on a preview that deployed perfectly. It cannot be raised, and HTTP-01 cannot be made to cover many
hosts with one certificate — the only fix is DNS-01. Second reason: HTTP-01 **cannot certify a
hostname before its container exists**, so a preview URL is invalid until the stack is deployed (a
sequencing problem that presents as a certificate problem). DNS-01's wildcard is valid immediately.

Recommend `http01` to start; recommend switching with `--challenge dns01 --dns-provider <code>` when
PR volume approaches the ceiling or pre-deploy URLs are needed. Re-running `init` is the whole
migration.

#### The per-PR `certresolver` rule is OPPOSITE in the two modes

> **Read this before writing TLS labels on any per-PR router.** It is the most expensive mistake
> available in this system and it is one careless copy-paste away.
>
> | Mode | Every per-PR router carries | If you get it wrong |
> |---|---|---|
> | **`http01`** | `tls=true` **and** `tls.certresolver=le` | omit the resolver → that hostname never gets a certificate |
> | **`dns01`** | `tls=true` — **NOTHING else** | add `certresolver` → Traefik orders a **separate certificate per PR host** |
>
> Under **`dns01`**, **exactly ONE** always-on router requests the wildcard — the control router, with
> `tls.domains[0].main=<domain>` + `tls.domains[0].sans=*.<domain>`. Every other router, including
> every per-PR one, inherits that certificate **by SNI** with a bare `tls=true`.
>
> Adding `tls.certresolver=le` to a per-PR router under DNS-01 silently burns the ~50-new-certs-per-week
> budget and eventually takes TLS down for the **whole host — including the control plane**, which is
> also how you lose the UI you would have used to diagnose it. Copying a router-label block from an
> HTTP-01 host into a DNS-01 host is exactly how this ships.
>
> **Before writing per-PR TLS labels, check which mode the host was initialised with**
> (`--challenge` in the `init` command, or the rendered `certificatesresolvers.le.acme.*challenge`
> args in `<PSTACK_DATA>/control/docker-compose.yml`). Do not guess, and do not assume the labels in
> an example you found match this host's mode.

#### DNS-01 credentials

Only the verified providers are written for you. A wrong variable name surfaces as an ACME
**"propagation timeout"** — which sends you debugging DNS instead of a typo — so an unrecognised
provider gets a `CHANGEME_VARIABLE_NAME` line pointing at <https://go-acme.github.io/lego/dns/>
rather than a guess.

| Provider | `--dns-provider` | Credential |
|---|---|---|
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN` — **one** token, `Zone:Read` + `DNS:Edit` |
| Hetzner | `hetzner` | `HETZNER_API_TOKEN` (also `HETZNER_PROPAGATION_TIMEOUT`, `HETZNER_TTL`) |
| AWS Route 53 | `route53` | **none** — the instance's IAM profile |
| Google Cloud DNS | `gcloud` | **none** — the VM's attached service account |

Every lego variable also accepts a `_FILE` suffix. **Never write `HETZNER_API_KEY`** — it does not
exist, and the failure looks like a DNS propagation problem. Prefer the tokenless providers where
available: nothing to store, nothing to rotate.

DNS records, either mode: `*.<domain>` and `<domain>` A-records pointing at the host, so any per-PR
hostname resolves. Under DNS-01 that is also what the single wildcard *certificate* covers; under
HTTP-01 resolution and certification are separate problems and each hostname is certified on its
first HTTPS request.

## 1. When to reach for it

| Situation | Use |
|---|---|
| You need a Compose file per PR with TLS and nothing else | **A PaaS** (Coolify, Dokploy, Uffizzi). Not pstack. |
| Your preview also needs a **DB branch / queue namespace / per-PR images / object-storage prefix / DNS record** | pstack |
| Your CI already has teardown shell steps, and stale stacks/networks/images keep piling up | pstack — the leak check is the point |
| Build-time-baked config (a frontend with the API URL compiled in) needs a per-PR value as a **build arg** | pstack (axis output → compose env → `build.args`) |
| You want to run untrusted, user-supplied specs | **Nothing here.** Hooks are shell strings at CI trust level; this is not a sandbox. |

pstack is not a PaaS, not multi-tenant, and has no state store. Stack identity is derived from a
variable (`${PR}`); truth lives in Docker and in each axis's own probe.

## 2. Mental model — get this right first

### Isolation axes

An **axis** is one stateful resource Compose knows nothing about but the preview needs its own copy
of. Each axis has up to four hooks, all optional, all plain shell run through `bash -c`:

| Hook | Contract | On failure |
|---|---|---|
| `up` | provision; **must be idempotent** (re-run on every redeploy) | **fatal** — aborts the run, compose never starts |
| `assert_live` | exit 0 ⇒ the resource **exists** | **fatal** — catches a provision that exited 0 without creating anything |
| `down` | destroy | **recorded, never fatal** |
| `assert_gone` | exit 0 ⇒ the resource is **gone** | **fatal for `verify`/`down`** → exit 2 |

**The asymmetry is the product.** `down` is best-effort because aborting a teardown halfway leaves
*more* garbage than continuing — a resource may already be gone, an API may flake. `verify` is strict
because "torn down but something survived" is the exact failure hand-rolled teardown scripts ship,
and it is silent until the disk fills.

### Order

- Axes are provisioned in **declaration order** and destroyed in **reverse**. Declare a dependency
  before its dependents (database before the app that migrates it).
- `up`: **`requires` asserts** → all axes → then compose up. Fails fast.
- `down`: compose down → axes in reverse → `verify` (unless `--no-verify`).

### Kinds, and why `down` on a shared deployment needs `--force`

```yaml
version: 1
kind: shared              # default is `isolated`
stack: shared-db          # a fixed identity, not a template — one per host
compose:
  file: docker-compose.shared.yml
  profiles: []
```

`down` runs `docker compose down -v`. On an isolated stack the `-v` removes that tenant's own
volumes — routine, and the whole point. On a **shared** deployment the *same verb* destroys
Traefik's `acme.json` (every certificate for the host, re-issued under a per-week Let's Encrypt rate
limit), the shared database volume (every preview's state), and the admin credentials. Identical
command, completely different blast radius — so it is refused:

```
$ pstack -f shared.yml down
refusing to tear down shared deployment "shared-db"
  ✗ compose      shared-db  — refused: kind is `shared`. `down` removes volumes (-v), which on a
    shared deployment destroys state every tenant depends on. Re-run with --force if that is truly
    intended.        # exit 1 — nothing ran
```

- **Explicit force is the only thing that lifts it**: `--force` on the CLI, `{ "force": true }` in
  the `POST /api/deployments/:id/down` body. Over HTTP, omitting it is a **synchronous 409** — no
  job is started, so the reason is in the response rather than buried in a transcript. To *upgrade*
  a shared deployment, run `up`: it converges and removes nothing.
- **The guard keys off the declared `kind`, not "does it have axes".** An isolated deployment that
  forgot its axes must not silently inherit a singleton's protection. That is why the kind is
  written down rather than inferred.
- **`kind: shared` may declare no axes** — it is a parse error, not a warning:
  `kind: shared cannot declare axes (found 1) … Did you mean kind: isolated?` (exit 3). Axes exist
  to isolate one tenant from another and to prove a tenant's resources are gone; a singleton has
  neither concern.
- `kind: isolated` **with no axes** gets a warning: nothing per-tenant is provisioned or verified,
  so it is just a Compose project. Either add the axes or mark it `shared`.

### Preconditions: `requires`

An isolated deployment usually depends on shared ones. Without a preflight, a missing dependency
surfaces partway through an axis hook as whatever error that CLI printed — uninformative, and
*after* some resources already exist.

```yaml
requires:
  - name: shared-db
    assert: docker network inspect preview-shared >/dev/null 2>&1
    hint: bring the shared deployment up first — `pstack -f shared.yml up`
```

```
$ PR=123 pstack up
→ requires: shared-db
  ✗ requires     shared-db  — unmet — bring the shared deployment up first — `pstack -f shared.yml up`
                                                                                        # exit 1
```

Every `assert` runs **before the first axis**, in declaration order; the first failure aborts and
nothing has been created. Write `hint` as *how to fix it* — the assert already says what broke.
Same discipline as an `assert_gone`: an `assert` that cannot answer must exit non-zero, not pass.

### What each hook can see

| Hook | Environment |
|---|---|
| `up`, `assert_live` | process env + spec `env:` + `STACK` + **`KEY=VALUE` outputs captured from earlier `up` hooks** |
| `down`, `assert_gone` | process env + spec `env:` + `STACK` — **no captured outputs** |

**This is the correctness trap.** `assert_gone` and `down` do **not** receive what `up` printed. An
`assert_gone` that references `$DATABASE_URL` from the provision hook gets whatever was in the
ambient environment — usually nothing. Every teardown hook and every assert must be derivable from
`$STACK` plus `env:` alone.

### Exit codes

| Code | Meaning | Who owns it |
|---|---|---|
| 0 | ok | — |
| 1 | operation failed (`up`: an axis, an `assert_live`, or compose failed) | whoever broke the deploy |
| 2 | **leaked** — torn down, but an `assert_gone` says a resource survived | whoever owns the infra |
| 3 | bad spec or usage (undefined variable, bad stack name, unknown flag) | whoever edited the spec |

`2` is separate from `1` on purpose so CI can page the right person. Axis `down` failures are
recorded as `non-fatal: …` lines in the report and never reach the exit code, so **`down` normally
exits 0 or 2** and the leak gate is what actually fails the job. Two exceptions return 1: the
`kind: shared` refusal (nothing ran) and an unhandled crash.

`verify` exits **0 even when axes are `unverifiable`** (no `assert_gone` defined), and `validate`
exits 0 even with warnings. Green ≠ checked — read the report, not just the code:

```
$ PR=9 pstack down            # exit 2
  ✓ down         leftover
  ✗ assert_gone  leftover  — LEAKED: resource still present after teardown
  ? assert_gone  nocheck   — unverifiable: no assert_gone defined
  1 leaked resource(s), 1 unverifiable axis/axes
```

`✓` checked and clean · `✗` leaked (drives exit 2) · `?` **never checked** (does not affect the exit
code). An axis marked `?` is invisible to the leak gate forever.

### Flags

```
pstack <up|down|verify|status|validate|init|serve> [flags]
  -f, --file <path>   spec file (default: preview.yml)
  -n, --dry-run       print every command in order, execute nothing
  -v, --verbose       echo commands and their output
  -q, --quiet         suppress per-step chatter
      --set K=V       override/define a variable (repeatable)
      --no-verify     down: skip the post-teardown leak check
      --force         down: allow tearing down a `kind: shared` deployment

init flags: --domain <preview.example.com>  --acme-email <you@example.com>
            --challenge http01|dns01        (default http01 — no DNS credential needed)
            --dns-provider <lego-code>      (dns01 only; token via PSTACK_DNS_TOKEN)

serve env:  PSTACK_TOKEN (required to bind off-loopback) · PSTACK_PORT (7878)
            PSTACK_HOST (127.0.0.1) · PSTACK_DATA (/var/lib/pstack)
```

`init` and `serve` are **spec-free** — they act on the host and on the registry, so `-f` does not
apply and neither fails because `./preview.yml` is absent.

`-v` prints hook stdout verbatim and **nothing is masked** — do not use it in a public CI log for an
axis whose `up` emits a connection string.

Hook and compose-file paths are relative to **the directory you run `pstack` from**, not to the spec
file. Run it from the repo root, or use absolute paths.

## 3. Writing a preview.yml

### Step 1 — the minimum: identity + compose

```yaml
version: 1
kind: isolated                        # default; `shared` for a host singleton (§2)
stack: pr-${PR}                       # compose project name, hostname label, and $STACK in hooks

compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]       # MUST list every profile in the compose file — see below
```

That alone buys you: one namespacing primitive (`docker compose -p pr-123`), and a teardown that
cannot disagree with the deploy. `pstack up` runs

```
docker compose -p 'pr-123' -f 'docker-compose.preview.yml' --profile 'backend' --profile 'frontend' up -d --remove-orphans
```

and `down` runs the same profile set with `down -v --remove-orphans`.

**Why the profile list must be complete.** Compose treats a service whose profile is not enabled as
absent, so a teardown with fewer profiles than the deploy leaves that profile's resources behind —
most visibly one dead `<stack>_default` network per PR, forever. pstack removes that failure mode by
construction: one list drives both phases. Your job is to make the list exhaustive. **A profile that
appears in the compose file but not in `compose.profiles` is never brought up and never torn down.**

Two rules that follow:

- Put every service behind a profile, so a bare `docker compose up` (without pstack) starts nothing.
- `down -v` removes containers, volumes and networks — **never images**. That is an axis (§4c).

Constraints on `stack`: it must match `/^[a-z0-9][a-z0-9_-]*$/`. No uppercase, no dots, no `/`. It
becomes a compose project name, a DNS label and usually a namespace id, so it is validated up front
rather than five steps into a deploy.

### Step 2 — variables

```yaml
env:
  PR_NUMBER: ${PR}
  PREVIEW_DOMAIN: preview.example.com
  REGISTRY: <region>.pkg.example.com/<your-project>/<repo>
  IMAGE_TAG: ${PR}-${GIT_SHA}         # per-PR, not just the sha — see §4c
```

| Rule | Why |
|---|---|
| Interpolation happens **once**, at parse time | a value containing `${…}` can never be re-expanded downstream |
| An **undefined variable is a hard error** (exit 3) | `pr-${PR}` with `PR` unset becomes `pr-`, which every PR then shares — collision instead of isolation |
| **An empty string counts as undefined** | `PR=""` fails loudly instead of producing `pr-` |
| `env:` entries resolve **in order** and may reference ambient vars and earlier entries | lets you build `IMAGE=${REGISTRY}:${IMAGE_TAG}` |
| **`${STACK}` is NOT available inside `env:`** | `STACK` is bound *after* the `env:` block resolves. Use it in `compose.*` and `axes.*` only. |

Everything in `env:` (plus the whole ambient environment and `STACK`) is exported to compose and to
every hook. **Declare every variable your compose file interpolates** — then a missing one is a
pstack exit 3 instead of Compose silently substituting an empty string.

### Step 3 — the first axis, and passing its coordinates into compose

A provision hook hands the rest of the stack its coordinates by printing `KEY=VALUE` lines on
stdout. pstack captures them and merges them into the env for later axes and for compose up.

```yaml
axes:
  - name: database
    up: |
      set -euo pipefail
      ./hooks/db.sh recreate "$STACK"                     # idempotent: delete-then-create
      echo "DATABASE_URL=$(./hooks/db.sh url "$STACK")"   # ← captured
    assert_live: psql "$DATABASE_URL" -c 'select 1' >/dev/null   # outputs ARE visible here
    down: ./hooks/db.sh delete "$STACK"
    assert_gone: |
      ./hooks/db.sh ping || exit 1                        # can't reach the API ⇒ can't prove gone
      [ "$(./hooks/db.sh count "$STACK")" = "0" ]         # NONE remain, not "the first is gone"
```

Capture rules: only whole trimmed lines matching `^[A-Z_][A-Z0-9_]*=…$` are captured, so ordinary log
chatter on stdout is ignored. Values are single-line. Lowercase keys are ignored.

On the compose side the captured value is just an env var:

```yaml
# docker-compose.preview.yml
services:
  backend:
    profiles: [backend]
    image: ${REGISTRY}/backend:${IMAGE_TAG}
    environment:
      DATABASE_URL: ${DATABASE_URL}          # from the database axis's up hook
      TEMPORAL_NAMESPACE: ${STACK}           # STACK is always exported
  frontend:
    profiles: [frontend]
    build:
      context: ./frontend
      args:
        # baked at build time — a runtime env var would be too late for a compiled-in URL
        PUBLIC_API_URL: https://backend-${STACK}.${PREVIEW_DOMAIN}
```

**Why delete-and-recreate rather than reuse:** a reused database branch carries the previous commit's
migrations, which is how preview schemas silently drift from main. Recreate makes `up` idempotent in
the way that matters.

### Step 4 — add the remaining axes

Add one axis at a time, `pstack -n up && pstack -n down` after each, and keep declaration order =
dependency order. A four-axis stack (database → queue namespace → images → ingress) is the shape in
`examples/preview.yml`; the hooks are in §4.

## 4. Axis cookbook

Every `assert_gone` below follows §5: reachability guard first, then a list-style absence check.

### a. Branchable-Postgres branch (generic REST API)

```yaml
  - name: db-branch
    up: |
      set -euo pipefail
      auth="Authorization: Bearer $DB_API_TOKEN"
      # Idempotent by delete-then-create. Names are not unique, so delete ALL matches.
      for id in $(curl -fsS -H "$auth" "$DB_API/projects/$DB_PROJECT/branches" \
                  | jq -r --arg s "$STACK" '.branches[] | select(.name == $s) | .id'); do
        curl -fsS -X DELETE -H "$auth" "$DB_API/projects/$DB_PROJECT/branches/$id" >/dev/null
      done
      body=$(jq -n --arg s "$STACK" '{branch:{name:$s},endpoints:[{type:"read_write"}]}')
      uri=$(curl -fsS -X POST -H "$auth" -H 'content-type: application/json' -d "$body" \
              "$DB_API/projects/$DB_PROJECT/branches" | jq -r '.connection_uris[0].connection_uri')
      test -n "$uri"
      echo "DATABASE_URL=$uri"
    assert_live: |
      set -euo pipefail
      psql "$DATABASE_URL" -c 'select 1' >/dev/null
    down: |
      auth="Authorization: Bearer $DB_API_TOKEN"
      for id in $(curl -fsS -H "$auth" "$DB_API/projects/$DB_PROJECT/branches" \
                  | jq -r --arg s "$STACK" '.branches[] | select(.name == $s) | .id'); do
        curl -fsS -X DELETE -H "$auth" "$DB_API/projects/$DB_PROJECT/branches/$id" || true
      done
    assert_gone: |
      set -euo pipefail
      auth="Authorization: Bearer $DB_API_TOKEN"
      # 1. reachability: a 401/timeout must NOT read as "gone"
      list=$(curl -fsS -H "$auth" "$DB_API/projects/$DB_PROJECT/branches")
      # 2. count matches — several branches can share a name
      n=$(printf '%s' "$list" | jq --arg s "$STACK" '[.branches[] | select(.name == $s)] | length')
      test "$n" -eq 0
```

`|| true` on the `down` loop is correct: a branch already deleted must not abort teardown. It must
never appear in the assert.

### b. Queue / workflow namespace on a shared cluster

One shared cluster, one namespace per PR — because task-queue names are often compiled into the app
and cannot be overridden per deploy, and a cluster per PR costs 1–2 GB RAM each.

```yaml
  - name: queue-namespace
    up: |
      docker exec shared-temporal-1 temporal operator namespace create \
        --namespace "$STACK" --retention 24h 2>/dev/null || true   # already-exists ⇒ fine
    assert_live: |
      docker exec shared-temporal-1 temporal operator namespace describe --namespace "$STACK" >/dev/null
    down: |
      docker exec shared-temporal-1 temporal operator namespace delete \
        --namespace "$STACK" --yes 2>/dev/null || true
    assert_gone: |
      set -uo pipefail
      docker info >/dev/null 2>&1 || exit 1                        # no daemon ⇒ can't tell
      names=$(docker exec shared-temporal-1 temporal operator namespace list --output json \
              | jq -r '.[].namespaceInfo.name') || exit 1          # query broken ⇒ can't tell
      # exact whole-line match: a substring test would let pr-12 be masked by pr-123
      ! printf '%s\n' "$names" | grep -qxF "$STACK"
```

Adjust the `jq` path to your CLI's JSON shape. The `list`-then-match form is better than
`describe`, which exits non-zero for both "absent" and "your token expired".

### c. Per-PR images

`compose down -v` never removes images, so per-PR layers accumulate until the disk fills — and disk
pressure evicts the warm build cache, which is the real per-PR speed lever.

```yaml
  - name: images
    down: |
      for img in $(docker ps -a --filter "label=com.docker.compose.project=$STACK" \
                     --format '{{.Image}}' | sort -u); do
        docker rmi "$img" 2>/dev/null || true
      done
    assert_gone: |
      set -uo pipefail                                   # NOT -e — see the note below
      docker info >/dev/null 2>&1 || exit 1
      imgs=$(docker images --format '{{.Repository}}:{{.Tag}}') || exit 1   # query must succeed
      n=$(printf '%s\n' "$imgs" | grep -c -- ":$IMAGE_TAG\$")               # 0 matches ⇒ grep exits 1
      test "$n" -eq 0
```

Why `set -uo pipefail` and not `-euo pipefail`: `grep -c` exits 1 when the count is zero, which is
exactly the passing case. Under `set -e` that aborts the hook and `verify` reports a leak that isn't
there. Keep `-e` off wherever a non-zero exit is a legitimate answer, and make the *query* fail closed
explicitly with `|| exit 1`.

Tag per **stack**, not per commit: with `IMAGE_TAG=${GIT_SHA}` alone, a build of the same sha on
another branch makes this assert fail forever (and makes `down` delete an image someone else uses).
`IMAGE_TAG=${PR}-${GIT_SHA}` is safe. Note this axis has no `up` — building is compose's job.

### d. Object-storage prefix

```yaml
  - name: bucket-prefix
    # no `up`: prefixes are implicit, created by the first PUT
    down: |
      aws s3 rm "s3://$BUCKET/$STACK/" --recursive || true
    assert_gone: |
      set -euo pipefail
      aws s3api head-bucket --bucket "$BUCKET" >/dev/null        # creds/bucket reachable?
      # trailing slash is load-bearing: prefix "pr-12" also matches "pr-123/…"
      n=$(aws s3api list-objects-v2 --bucket "$BUCKET" --prefix "$STACK/" \
            --max-keys 1 --output json | jq '.KeyCount')
      test "$n" -eq 0
```

### e. DNS record

```yaml
  - name: dns
    up: |
      set -euo pipefail
      ./hooks/dns.sh upsert "backend-$STACK.$PREVIEW_DOMAIN" A "$HOST_IP"
    down: |
      ./hooks/dns.sh delete "backend-$STACK.$PREVIEW_DOMAIN" A || true
    assert_gone: |
      set -euo pipefail
      # Ask the PROVIDER, not a resolver: a wildcard *.preview.example.com makes every name
      # resolve, so `dig` can never prove this record is gone. Resolver caching lies too.
      n=$(./hooks/dns.sh list "$PREVIEW_DOMAIN" \
            | jq --arg n "backend-$STACK.$PREVIEW_DOMAIN" '[.[] | select(.name == $n)] | length')
      test "$n" -eq 0
```

If you have a wildcard record and label-based routing (Traefik/Caddy), you do not need this axis at
all — use (f) instead, which asserts the *route* is gone rather than the name.

### f. Ingress: "the hostname stops answering"

No `up` — routing comes from labels on the compose services and TLS from a pre-issued wildcard cert.
The axis exists purely to catch a router left registered by a half-removed container.

```yaml
  - name: ingress
    assert_gone: |
      set -uo pipefail
      # Prove the CHECK works before trusting its negative: if the shared host is down, every
      # preview would read as "cleaned up".
      curl -sf --max-time 5 "https://${PREVIEW_DOMAIN}/" >/dev/null || exit 1
      code=$(curl -s -o /dev/null -m 5 -w '%{http_code}' \
               "https://backend-${STACK}.${PREVIEW_DOMAIN}/health" || echo 000)
      case "$code" in
        404|000) exit 0 ;;   # no router / no listener ⇒ gone
        *)       exit 1 ;;   # 200 = still serving; 502/503 = router registered, backend dead ⇒ LEAK
      esac
```

The `|| echo 000` is legitimate — it converts a failure into a **value** that the `case` then
judges. That is the opposite of `|| true`, which converts a failure into an unconditional pass.

A bare `! curl -sf …/health` is acceptable only if your ingress returns nothing at all for an
unknown host; if it returns 503 for a registered-but-empty router, `-f` turns that into "gone".

## 5. Writing a good `assert_gone`

**Exit 0 means GONE.** This is the one hook that decides whether a leak is caught, so it is the one
place to spend care. Three rules, in priority order.

### Rule 1 — fail closed: if you cannot tell, exit non-zero

A probe that cannot answer must not answer "gone". Guard reachability first:

```yaml
assert_gone: |
  <probe-is-usable> || exit 1     # daemon up? token valid? API reachable?
  <assert-this-resource-is-absent>
```

A false "leaked" costs someone five minutes of looking. A false "clean" costs a full disk in three
weeks, and by then nobody remembers which PR.

Corollary: **a broken assert also reports as a leak.** A hook that dies on `set -u` (a variable you
forgot to declare under `env:`), a missing CLI, or an expired token exits non-zero, and the report
says `LEAKED`. That is the safe direction, but it means the first thing to check on a leak is the
hook's own stderr (`pstack verify -v`), not the resource.

### Rule 2 — prefer a list query to a describe query

| Query shape | "absent" | "your credentials expired" | Verdict |
|---|---|---|---|
| `provider describe "$STACK"` | non-zero | non-zero | **indistinguishable — do not negate it bare** |
| `provider list … ` + match | exit 0, empty output | non-zero | usable |

List queries separate "the question was answered" from "the answer was no". The model case:

```yaml
assert_gone: |
  set -euo pipefail
  docker info >/dev/null 2>&1 || exit 1
  # exits 0 with empty stdout when nothing matches ⇒ `test -z` is a sound assert
  test -z "$(docker ps -aq --filter "label=com.docker.compose.project=$STACK")"
```

When only a describe endpoint exists, branch on the exit code explicitly and require a genuine
not-found signal:

```yaml
assert_gone: |
  set -uo pipefail                 # NOT -e: we inspect the exit code ourselves
  out=$(provider describe "$STACK" 2>&1) && code=0 || code=$?
  case "$code" in
    0) exit 1 ;;                   # it answered and it exists ⇒ LEAKED
    *) printf '%s' "$out" | grep -qiE 'not ?found|does not exist' || exit 1
       exit 0 ;;                   # a real 404 is the only proof of absence
  esac
```

### Rule 3 — assert NONE remain, and match exactly

| Mistake | Failure | Fix |
|---|---|---|
| Checking the first result | APIs return several resources with the same name; the survivor is #2 | count matches, require `0` |
| Substring match (`grep -q "$STACK"`) | tearing down `pr-12` passes while `pr-123` exists | `grep -qxF "$STACK"`, or match on an exact JSON field |
| Prefix without a boundary (`--prefix "$STACK"`) | `pr-12` matches `pr-123/objects` | `--prefix "$STACK/"` |
| Asserting on a captured output (`$DATABASE_URL`) | `assert_gone` never receives `up`'s outputs — the var is empty | derive everything from `$STACK` + `env:` |

### The forbidden patterns

| Anti-pattern | Why it is wrong |
|---|---|
| `probe \|\| true` | forces exit 0 — the assert can never fail, and `verify` becomes decorative. `\|\| true` belongs on `down`, never on an assert. Flagged by `pstack validate`. |
| `! <probe>` alone, one line | exits 0 whenever the probe fails for *any* reason: missing CLI, expired token, DNS blip. "Cannot tell" reads as "gone". Flagged by `pstack validate`. |
| Inverting the sense | `assert_gone: provider exists "$STACK"` passes exactly when the resource survived. Read it aloud: "exit 0 means gone." |
| `2>/dev/null` on the assert | hides the reason a probe failed, which is the information you need at 2am |
| No `assert_gone` at all | reported `unverifiable` (`?` in the report) and **`verify` still exits 0** |

`pstack validate` lints the first two, but its `!`-check only fires on a *single-line* script with no
`exit`, `||` or `&&` — a multi-line `!` form is not flagged and is not automatically safe. Read it
yourself.

### Prove the assert works

An assert that has never failed has never been tested. See §9.

## 6. CI wiring

Three jobs. `pstack down` already runs `verify`, so the PR-close job is the primary gate; the
nightly sweep catches stacks whose close event was missed or whose teardown job never ran.

### Deploy

```yaml
- name: deploy preview
  env:
    PR: ${{ github.event.pull_request.number }}
    GIT_SHA: ${{ github.sha }}
  run: pstack up            # exit 1 = deploy broken; exit 3 = spec broken
```

### PR close — branch on the exit code

```yaml
- name: teardown preview
  env:
    PR: ${{ github.event.pull_request.number }}
    GIT_SHA: ${{ github.sha }}
  run: |
    set +e
    pstack down
    code=$?
    set -e
    case $code in
      0) echo "clean" ;;
      2) echo "::error::pr-$PR LEAKED after teardown — resources survived"
         gh issue create --label preview-leak \
           --title "Leaked preview resources: pr-$PR" \
           --body "\`pstack down\` exited 2. Run \`PR=$PR pstack verify -v\` on the preview host."
         exit 1 ;;                     # infra owner
      3) echo "::error::preview.yml is invalid"; exit 1 ;;   # spec owner
      *) exit $code ;;                 # deploy owner
    esac
```

Exit **2** and exit **1** have different owners: 2 means teardown ran and something survived
(infra/API problem, or a wrong `assert_gone`); 1 means the operation itself errored.

### Nightly sweep

```yaml
- name: sweep closed PRs
  run: |
    leaks=0
    for n in $(gh pr list --state closed --limit 100 --json number -q '.[].number'); do
      PR=$n pstack verify -q || { echo "leak: pr-$n"; leaks=$((leaks+1)); }
    done
    test "$leaks" -eq 0
```

Or enumerate what actually exists on the host — `docker compose ls --all` (the same source
`GET /api/deployments` uses for its `running` flag) — and verify anything whose PR is closed.

Notes:

- Install it in the job with **`bun add -g @samyx/preview-stacks`** after `oven-sh/setup-bun` — not by
  checking out the pstack repo. Pin the version if you want teardown to behave exactly like the deploy
  that created the stack.
- Pass variables as **env** (`PR=…`) or `--set PR=…`; both feed interpolation and reach hooks.
- Run `pstack` from the repo root so relative hook paths and `compose.file` resolve.
- `--no-verify` only when you are about to redeploy immediately and a resource is meant to survive.
- Do not use `-v` in a public log if an `up` hook emits credentials; nothing is masked.

## 7. The HTTP API (and the UI)

On a real host you do not run this by hand — `pstack init` runs it in the control stack behind
Traefik, at `control.<domain>` (UI) and `api.<domain>` (API). Run it directly only to develop against
it; there is **no `-f`**, because `serve` acts on the registry, not on a spec:

```bash
PSTACK_TOKEN=$(openssl rand -hex 32) PSTACK_HOST=0.0.0.0 PSTACK_PORT=7878 pstack serve
```

Use the API instead of the CLI when the caller is **not** on the preview host: a chat-ops command, a
dashboard, a workflow that wants to redeploy one PR without SSH. Use the CLI everywhere else — the
API is the same core with a job queue in front.

| Route | Notes |
|---|---|
| `GET /api/health` | liveness; auth mode, data dir, version |
| `GET /api/deployments` | every submitted deployment, with `busy` and `running` |
| `GET /api/deployments/:id` | meta + spec summary (axis **hook names**, never hook bodies) |
| `PUT /api/deployments/:id` | submit or replace: `{ spec, compose?, env? }` → `201` new / `200` replaced |
| `DELETE /api/deployments/:id` | forget it — **refused while containers still exist** |
| `POST /api/deployments/:id/up` | → `202 { job }` |
| `POST /api/deployments/:id/down` | → `202 { job }`; body `{ verify?, force? }` |
| `POST /api/deployments/:id/verify` | → `202 { job }` |
| `GET /api/jobs` · `GET /api/jobs/:jobId` | poll for `state` |
| `GET /api/jobs/:jobId/stream` | SSE: buffered log replayed, then live |

```bash
API=https://api.preview.example.com
job=$(curl -fsS -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
        "$API/api/deployments/pr-123/down?PR=123" | jq -r .job.id)
curl -fsS -N "$API/api/jobs/$job/stream"
```

Things to know:

- **`:id` is a registry id**, not a compose project name. The server owns the stored spec and
  resolves `stack:` itself, so a client cannot point it at an arbitrary compose project.
- **Spec variables ride on the query string and are NOT stored.** `stack: pr-${PR}` needs `?PR=123`
  on every call — and the *same* ones on `down` as on `up`, or teardown targets a different stack
  than deploy created. A missing variable is a `400` naming it.
- **Job state replaces the exit code**: `running` → `ok` | `failed` | **`leaked`**. Map `leaked` to
  whatever your exit-2 path is; a `202` only means the job started.
- **One job per stack.** A second request while one is in flight gets **409** rather than queueing —
  concurrent `up`/`down` would race over the same compose project and the same external resources.
- **Jobs are in-memory**, last 50, lost on restart (which also clears the busy locks).
- **`PSTACK_TOKEN` is required for every mutating route**, and without it the server refuses to bind
  anything but `127.0.0.1` (exit 3 if you set `PSTACK_HOST` anyway). `GET`s are unauthenticated —
  `GET /api/jobs/:id` returns a whole hook transcript, so ingress auth is the only gate on reads.
- **It is not multi-tenant and it is not a sandbox.** One spec, one Docker socket, one trust level:
  accepting a spec means accepting arbitrary compose *and* arbitrary shell hooks — remote code
  execution by design, not by bug. The socket mount is root-equivalent on the host. Gate *who may
  submit* (ingress auth, or an SSH tunnel); never try to sanitize what a spec contains.
- **`GET /` — and every other non-`/api/` path — serves the UI**, a single HTML document **embedded in
  the bundle**. There is no static-file directory to point at or mount, no filesystem lookup, and
  therefore no path traversal; a deep link renders instead of 404ing. It does what the CLI does with a
  live job log, and it calls the API with **relative** `/api/…` paths, so it is same-origin from
  `control.<domain>` and needs no CORS.

### Submitting deployments by id

The API is registry-backed: specs are stored under `$PSTACK_DATA/deployments/<id>/`
(`spec.yml` · `compose.yml` · `meta.json`, default `/var/lib/pstack`) and addressed by id.

```bash
curl -fsS -X PUT -H "Authorization: Bearer $PSTACK_TOKEN" \
     -H 'content-type: application/json' \
     -d "$(jq -n --rawfile s preview.yml --rawfile c docker-compose.preview.yml \
             '{spec:$s, compose:$c, env:{PR:"123"}}')" \
     https://api.preview.example.com/api/deployments/pr-123
```

Four constraints that decide whether a submitted spec works at all:

- **Hooks cannot use relative paths.** A deployment's runner runs with `cwd` set to its own
  directory, and only `spec.yml` and `compose.yml` live there. `up: ./hooks/db.sh …` works from a
  CLI checkout and **cannot** work over the API — inline the shell, or use absolute paths.
- **`put` validates before it commits** (and the route parses the string first, so a typo on a
  *replace* cannot delete a good record while its containers keep running). `kind` is read from the
  parsed spec, never from the caller.
- **On `PUT`, variables come from the body's `env` — the query string is NOT read.** The spec is
  parsed before it is stored, so `{"spec": "…stack: pr-${PR}…"}` without `env: {"PR": "123"}` is a
  400 naming the variable. `?PR=123` works on the *action* routes (`up`/`down`/`verify`) and on the
  `GET`s, not on the submit. They are still not persisted either way.
- **`remove` forgets only — it never tears anything down.** `DELETE` therefore refuses while
  containers exist, and refuses when Docker did not answer, because "could not tell" is not evidence
  of absence. Always `down` clean *before* forgetting.
- **`PUT` is refused (409) while that stack has a job in flight** — swapping the spec mid-job means
  the eventual `down` runs with different profiles and axes than `up` created.

For CI, prefer the CLI (`-f`, `--set`): no host access, no token, and the exit codes above.

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `undefined variable(s) ${PR}` (exit 3) | not in the environment, or set to `""` — empty counts as undefined | `PR=123 pstack …` or `--set PR=123`, or give it a default under `env:` |
| `undefined variable(s) ${STACK}` in an `env:` entry | `STACK` is bound *after* `env:` resolves | use `${STACK}` only in `compose.*` / `axes.*`; inside `env:` rebuild it from `${PR}` |
| `stack "PR-123/foo" must match /^[a-z0-9][a-z0-9_-]*$/` | uppercase, dot, or `/` — often a raw branch name | lowercase and slugify before passing it in: `--set PR=$(echo "$REF" \| tr 'A-Z/.' 'a-z--')` |
| `up` succeeds but starts nothing | every service is behind a profile and `compose.profiles` is empty or missing that profile | list every profile the compose file uses; confirm with `pstack -n up` |
| an image tag resolves to the literal string `unset` | the compose file used `${IMAGE_TAG:-unset}` and the var was never exported | declare `IMAGE_TAG` under `env:` so pstack hard-errors instead of Compose defaulting; `pstack validate` |
| stale `<stack>_default` networks / containers accumulate | a profile in the compose file is missing from `compose.profiles`, so `down` never selects its services | add it; one list drives both up and down |
| per-PR images pile up, disk fills, builds get slow | `down -v` never removes images | add the images axis (§4c) |
| `assert_gone` fails with an empty variable | it referenced a `KEY=VALUE` output from `up`; teardown hooks don't receive those | derive from `$STACK` + `env:` |
| `LEAKED` reported but the resource is definitely gone | the assert hook itself failed — `set -u` on a variable not declared under `env:`, a missing CLI, an expired token. Fail-closed, working as designed | `PR=… pstack verify -v` and read that hook's stderr **before** hunting for a survivor |
| `verify` is green but the resource is obviously there | inverted assert, or `\|\| true` / bare `!` swallowing the probe's failure | re-read §5; `pstack validate` flags both patterns |
| report shows `?` and `unverifiable: no assert_gone defined`, exit 0 | that axis has no `assert_gone`, so nothing was checked | add one, or accept it knowingly — `verify` will never catch that axis |
| `down` reports `non-fatal: …` but exits 0 | axis `down` failures are best-effort by design; only `assert_gone` fails the command | that is correct — if the resource survived, `assert_gone` would have said so. If it didn't, the assert is wrong. |
| `refusing to tear down shared deployment "…"` (exit 1) | `kind: shared` guard — `down -v` there destroys state every tenant depends on | to **upgrade** it run `up` (converges, removes nothing). Only `--force` if destroying that state is genuinely the goal. |
| `kind: shared cannot declare axes` (exit 3) | a singleton has nothing to isolate and nothing to prove gone | it is almost certainly `kind: isolated` |
| `! kind: isolated with no axes …` (warning) | the spec is just a Compose wrapper | add the axes it needs, or mark it `kind: shared` so `down` is guarded |
| `✗ requires  <name>  — unmet` (exit 1) | a `requires` assert failed; **nothing was created** | do what the `hint` says — usually bring the shared deployment up first |
| a `requires` assert passes while the dependency is down | same fail-open bug as a bare `! <probe>` — the assert command itself failed | guard it the way §5 guards an `assert_gone` |
| `409 already has a job in flight` | one job per stack | poll `GET /api/jobs/:id` to a terminal state, then retry |
| `401 unauthorized` on a POST | `PSTACK_TOKEN` is set server-side; `GET`s are open, mutations are not | send `Authorization: Bearer $PSTACK_TOKEN` |
| `refusing to bind 0.0.0.0 without PSTACK_TOKEN` (exit 3) | safety interlock — this API destroys infrastructure | set `PSTACK_TOKEN`, or leave it on loopback and tunnel |
| a hook can't find `./hooks/db.sh` | hook cwd is where you ran `pstack`, not the spec's directory | run from the repo root or use absolute paths |
| `init needs --domain` / `needs --acme-email` (exit 3) | neither the flag nor `PSTACK_DOMAIN` / `PSTACK_ACME_EMAIL` was given | pass them; **no DNS credential is needed** for the default HTTP-01 |
| `--challenge dns01 needs --dns-provider` (exit 3) | the provider is required for, and only for, `dns01` | `--dns-provider cloudflare` (or drop back to the default `http01`) |
| `control stack started but the API never became healthy` | `compose up -d` succeeded but the container is crash-looping | `docker compose -p pstack-control logs pstack` — usually a bad `PSTACK_IMAGE` |
| `image pstack:local not found` (precondition) | the control image was never built or pulled | `docker build -t pstack:local .` from a pstack checkout, or set `PSTACK_IMAGE` |
| ACME **"propagation timeout"** under `dns01` | almost always a wrong credential **variable name**, not DNS | check it against lego's list; Hetzner is `HETZNER_API_TOKEN` (**never** `HETZNER_API_KEY`), Cloudflare `CF_DNS_API_TOKEN` |
| no certificate ever arrives under `http01` | **port 80 is not reachable from the internet** (the web→websecure redirect is *not* the cause — Traefik bypasses it for the challenge path) | open :80 to the internet, or switch to `dns01` |
| TLS errors appear on *new* previews while old ones are fine | HTTP-01's ~50-new-certs-per-registered-domain-per-week limit — ~16 PRs at 3 surfaces each | switch to `dns01` (§0); it needs one wildcard, so there is no per-PR issuance |
| TLS suddenly broken **host-wide**, including `control.<domain>`, on a `dns01` host | a per-PR router carries `tls.certresolver=le`, so every PR ordered its own certificate and burned the limit | remove it — per-PR routers under `dns01` get `tls=true` and **nothing else** (§0) |
| a preview hostname 404s while its container is healthy | a per-PR compose file declared `preview-ingress` non-`external`, so Compose made `pr-N_preview-ingress` | declare **both** networks `external: true` |
| `https://pstack.<domain>` does not resolve or 404s | that hostname is gone — no router matches it | the UI is `control.<domain>`, the API `api.<domain>` |

## 9. Before you trust a spec in CI

1. **`pstack validate`** — parses, resolves interpolation, prints warnings. Read every warning; it
   exits 0 anyway. Zero `up`-without-`assert_gone` warnings is the target.
2. **`pstack -n up` and `pstack -n down`** — dry-run walks every step in order and executes nothing.
   It prints step labels (`→ up: database`, `→ compose up (backend, frontend)`), so it is the fastest
   check on axis order and on which profiles compose will get. To see the literal shell commands, run
   for real with `-v`.
3. **`pstack up` twice.** `up` is re-run on every redeploy; the second run must succeed and converge,
   not fail on "already exists".
4. **`pstack down` twice.** The second run must exit 0, not 1/2. Teardown of an already-gone resource
   is the normal case on a retry.
5. **Force a leak and confirm `verify` catches it.** Temporarily replace one axis's `down` with
   `true`, run `pstack down`, and require **exit 2** with `LEAKED` against that axis. An
   `assert_gone` that has never failed has never been tested.
6. **Break the probe and confirm it fails closed.** Unset the API token (or stop the daemon) and run
   `pstack verify`. It must exit 2, not 0. If it goes green, the assert is guessing.
7. **Check for false positives across neighbours.** With `pr-12` and `pr-123` both deployed, tearing
   down `pr-12` must leave `pr-123` running and still verify clean.
8. **Grep the CI report for `?`.** `verify` exits 0 with `unverifiable` axes; if you want coverage
   gated, fail the job when the report contains `unverifiable`.
9. **Confirm no secrets in the log** if any `up` emits a connection string — nothing is masked, so
   drop `-v` on those runs.
10. **Check the `kind` is right.** Run `pstack validate` and read the `kind:` in the first line. A
    host singleton declared `isolated` has no guard on `down`, and one `pstack down` then takes out
    the TLS store and every preview's shared state. Then break a `requires` on purpose and confirm
    `up` stops **before** creating anything.
11. **Check the host's TLS mode before you write per-PR TLS labels.** Read the rendered
    `certificatesresolvers.le.acme.*challenge` args in `<PSTACK_DATA>/control/docker-compose.yml` (or
    the `--challenge` the host was `init`ed with). Under `http01` each per-PR router needs
    `tls.certresolver=le`; under `dns01` it must carry `tls=true` and **nothing else**. Getting this
    backwards either leaves a host uncertified or burns the weekly certificate budget for everyone —
    see the warning in §0. Never copy router labels between hosts without checking their modes match.
12. **Confirm both external networks are `external: true`** in the per-PR compose file
    (`preview-ingress`, `preview-shared`). A non-external declaration yields a healthy, unreachable
    container and a 404 that looks like a routing bug.
