---
name: pstack
description: Use when setting up, configuring, or debugging ephemeral per-PR preview stacks with pstack — writing a preview.yml, defining isolation axes, wiring up/down/verify into CI, or diagnosing leaked preview resources.
---

# Using pstack

`pstack` gives a per-PR preview stack a declarative lifecycle: you list the **isolation axes** the
preview needs (a database branch, a queue namespace, images, DNS), it provisions them in order, runs
your Compose stack, tears everything down in reverse — and then **proves nothing leaked**.

```bash
PR=123 pstack up        # provision axes in order, then compose up
PR=123 pstack down      # compose down (all profiles) → destroy axes in reverse → verify
PR=123 pstack verify    # assert every axis is gone; exit 2 if anything survived
PR=123 pstack validate  # parse, resolve interpolation, print warnings
PR=123 pstack status    # compose ps for this stack
```

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
- `up`: all axes → then compose up. Fails fast.
- `down`: compose down → axes in reverse → `verify` (unless `--no-verify`).

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

`2` is separate from `1` on purpose so CI can page the right person. Note in this build **`down`
can only exit 0 or 2**: axis `down` failures are recorded as `non-fatal: …` lines in the report and
never reach the exit code, so the leak gate is what actually fails the job.

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
pstack <up|down|verify|status|validate|serve> [flags]
  -f, --file <path>   spec file (default: preview.yml)
  -n, --dry-run       print every command in order, execute nothing
  -v, --verbose       echo commands and their output
  -q, --quiet         suppress per-step chatter
      --set K=V       override/define a variable (repeatable)
      --no-verify     down: skip the post-teardown leak check
```

`-v` prints hook stdout verbatim and **nothing is masked** — do not use it in a public CI log for an
axis whose `up` emits a connection string.

Hook and compose-file paths are relative to **the directory you run `pstack` from**, not to the spec
file. Run it from the repo root, or use absolute paths.

## 3. Writing a preview.yml

### Step 1 — the minimum: identity + compose

```yaml
version: 1
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
`GET /api/stacks` uses) — and verify anything whose PR is closed.

Notes:

- Pass variables as **env** (`PR=…`) or `--set PR=…`; both feed interpolation and reach hooks.
- Run `pstack` from the repo root so relative hook paths and `compose.file` resolve.
- `--no-verify` only when you are about to redeploy immediately and a resource is meant to survive.
- Do not use `-v` in a public log if an `up` hook emits credentials; nothing is masked.

## 7. The HTTP API (and the UI)

```bash
PSTACK_TOKEN=$(openssl rand -hex 32) PSTACK_HOST=0.0.0.0 PSTACK_PORT=7878 \
  pstack -f preview.yml serve
```

Use the API instead of the CLI when the caller is **not** on the preview host: a chat-ops command, a
dashboard, a workflow that wants to redeploy one PR without SSH. Use the CLI everywhere else — the
API is the same core with a job queue in front.

| Route | Notes |
|---|---|
| `GET /api/health` | liveness; reports whether auth is enforced |
| `GET /api/spec?pr=123` | the resolved spec for that stack (query key = the var name, or `id`) |
| `GET /api/stacks` | compose projects on the host, with a `busy` flag |
| `GET /api/stacks/:id/status` | compose ps for one stack |
| `POST /api/stacks/:id/up` | → `202 { job }` |
| `POST /api/stacks/:id/down` | → `202 { job }`; body `{ "verify": false }` to skip the leak check |
| `POST /api/stacks/:id/verify` | → `202 { job }` |
| `GET /api/jobs` · `GET /api/jobs/:jobId` | poll for `state` |
| `GET /api/jobs/:jobId/stream` | SSE: buffered log replayed, then live |

```bash
job=$(curl -fsS -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
        http://host:7878/api/stacks/123/down | jq -r .job.id)
curl -fsS -N -H "Authorization: Bearer $PSTACK_TOKEN" http://host:7878/api/jobs/$job/stream
```

Things to know:

- **`:id` is the value of the stack variable** (`PR` by default, set `PSTACK_VAR`), not the resolved
  stack name. The server owns the spec and resolves `pr-${PR}` itself, so a client cannot point it at
  an arbitrary compose project.
- **Job state replaces the exit code**: `running` → `ok` | `failed` | **`leaked`**. Map `leaked` to
  whatever your exit-2 path is; a `202` only means the job started.
- **One job per stack.** A second request while one is in flight gets **409** rather than queueing —
  concurrent `up`/`down` would race over the same compose project and the same external resources.
- **Jobs are in-memory**, last 50, lost on restart (which also clears the busy locks).
- **`PSTACK_TOKEN` is required for every mutating route**, and without it the server refuses to bind
  anything but `127.0.0.1` (exit 3 if you set `PSTACK_HOST` anyway). `GET`s are unauthenticated. It
  is not multi-tenant — one spec, one Docker socket — so put it behind your ingress' auth or an SSH
  tunnel.
- `GET /` serves the web UI, which is a **placeholder in this build**. Treat the API as the surface.

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
| `409 already has a job in flight` | one job per stack | poll `GET /api/jobs/:id` to a terminal state, then retry |
| `401 unauthorized` on a POST | `PSTACK_TOKEN` is set server-side; `GET`s are open, mutations are not | send `Authorization: Bearer $PSTACK_TOKEN` |
| `refusing to bind 0.0.0.0 without PSTACK_TOKEN` (exit 3) | safety interlock — this API destroys infrastructure | set `PSTACK_TOKEN`, or leave it on loopback and tunnel |
| a hook can't find `./hooks/db.sh` | hook cwd is where you ran `pstack`, not the spec's directory | run from the repo root or use absolute paths |

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
