# pstack usage guide

Task-oriented. Each section is something you actually do, with the commands and the output they
produce. For *why* the tool is shaped this way — the `down`/`verify` asymmetry, the profile rule, the
no-state-store decision — read [`../README.md`](../README.md) first; this guide assumes it.

Every `pstack` block below was produced by running the command against the code in this repo; the
output is what it printed. The exceptions, marked where they appear: the CI workflow in section 8
(GitHub-side, not runnable here), the `init` walkthrough in section 7 (it needs a real host with a
real domain), and the abridged JSON in section 6.

| I want to… | Go to |
|---|---|
| get `pstack` on my PATH | [1. Install & first run](#1-install--first-run) |
| write the smallest spec that works | [2. Write your first spec](#2-write-your-first-spec) |
| add a database branch / queue namespace / images | [3. Add your first axis](#3-add-your-first-axis) |
| deploy, inspect, tear down | [4. Up, status, down](#4-up-status-down) |
| know my teardown actually works | [5. Prove your teardown works](#5-prove-your-teardown-works) |
| a dashboard and an HTTP API | [6. Run the API and UI](#6-run-the-api-and-ui) |
| **stand up the control stack on a host** | [7 → `pstack init`](#stand-up-the-host-pstack-init) |
| **choose a TLS mode (HTTP-01 or DNS-01)** | [7 → Choose a TLS mode](#choose-a-tls-mode) |
| shared vs isolated, `requires`, `--force`, submitting deployments | [7. The control plane](#7-the-control-plane) |
| deploy from GitHub Actions | [8. Wire it into CI](#8-wire-it-into-ci) |
| a nightly leak sweep, orphan hunting | [9. Day-2 operations](#9-day-2-operations) |
| every flag, route, env var, exit code | [10. Reference](#10-reference) |

---

## 1. Install & first run

`pstack` ships as a **global package**. Requires [Bun](https://bun.sh) **≥ 1.3** (declared as
`engines.bun`) — the spec parser is `Bun.YAML`, the server is `Bun.serve`, hooks run through
`Bun.spawn`, and there are no runtime dependencies at all. `docker` with the Compose v2 plugin must
be on the PATH of whatever box runs `up` / `down` / `init`, but not to `validate` or dry-run a spec.

```bash
bun add -g @samyx/preview-stacks     # or: npm i -g @samyx/preview-stacks
pstack --help
```

```console
$ which pstack
/Users/<you>/.bun/bin/pstack

$ pstack --help
pstack — declarative lifecycle for ephemeral preview stacks

Usage: pstack <up|down|verify|status|validate|init|serve> [flags]

Flags:
  -f, --file <path>   spec file (default: preview.yml)
  -n, --dry-run       print what would run, change nothing
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

Exit: 0 ok · 1 failed · 2 leaked · 3 bad spec/usage
```

### What actually gets installed

| | |
|---|---|
| Contents | `dist/cli.js` (~74 KB, the `bin`) · `dist/index.js` (the library entry) · both `.map` files · `package.json` · `README.md` · `LICENSE` · `CHANGELOG.md` |
| Tarball | **8 files, 0.36 MB unpacked** (~118 KB packed) |
| Not shipped | `src/`, `docs/`, `examples/`, `skills/`, `templates/`, `ui/`, tests |

Both entrypoints are **bundles**, built by `bun scripts/build.ts` with `--target=bun`, minified, with
linked sourcemaps so a stack trace from your box still names real functions. Two consequences worth
knowing:

- **Nothing is read from a path relative to the source at runtime.** The web UI and the control-stack
  compose template are `with { type: 'text' }` imports, inlined into the bundle. That removes the
  whole class of bug where a tool works from a checkout and 404s once installed — verified by
  extracting the tarball and running it with the repo unreachable.
- **It is a bundle, not a `--compile`d executable, on purpose.** A standalone binary bakes in the Bun
  runtime (~60 MB) *per platform*, which for npm means either a five-platform
  `optionalDependencies` matrix or a postinstall download. A 74 KB bundle needs neither, and Bun is
  already a hard requirement — there is no Node fallback being preserved, because `Bun.serve`,
  `Bun.YAML`, `Bun.spawn` and `Bun.file` have no Node equivalent.

### Working from a checkout (contributors)

Everywhere in this guide, `bun src/cli.ts <command>` is equivalent to `pstack <command>` — that is
the **contributor** path, not the user path. Use it when you are changing pstack itself:

```console
$ git clone <this-repo> && cd preview-stacks
$ bun install
$ bun src/cli.ts --help          # run from source, no build step
$ bun scripts/build.ts           # build dist/ and smoke-test the real artifact
$ bun test
 ...
 0 fail
Ran N tests across 1 file.

$ bunx tsc --noEmit              # strict, noUncheckedIndexedAccess; silent on success
```

The suite includes end-to-end leak detection against the real filesystem, so a green run means the
`down`/`verify` asymmetry itself is working, not just that the parser compiles. `scripts/build.ts`
finishes by running `--help` out of the built bundle: a bundle that cannot start is not a build.

---

## 2. Write your first spec

Start with no axes at all. A spec that only wraps Compose is already useful: it gives you a stable
per-PR project name and the all-profiles teardown rule.

`preview.yml`:

```yaml
version: 1
stack: pr-${PR}

compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]
```

Three things are happening:

| Field | What it does |
|---|---|
| `stack: pr-${PR}` | the stack **identity**. Becomes the Compose project name (`-p pr-123`), and is exported to every hook as `$STACK`. |
| `compose.file` | passed as `-f`. |
| `compose.profiles` | `up` enables **these**; `down` enables **all of them**, always. Put every service behind a profile so a bare `docker compose up` starts nothing. |

### Validate it

`validate` parses, resolves all interpolation, and reports what it found. It touches nothing.

```console
$ PR=123 pstack validate
stack: pr-123
✓ spec parses — kind: isolated, 0 axis/axes, stack "pr-123"
  compose: docker-compose.preview.yml [backend, frontend]
  ! kind: isolated with no axes — nothing per-tenant is provisioned or verified, so this is just a compose project. If it is a host singleton, mark it `kind: shared` so `down` is guarded.
```

The first line is the resolved identity. Read it — it is the single most common thing to get wrong.

The `!` is the warning you should expect at this stage, and it says something true: with no axes
this is a Compose wrapper, and `verify` has nothing to check. It goes away when you add the first
axis in section 3. If this spec is *not* a per-PR stack but a host singleton — a shared database, a
queue cluster — mark it `kind: shared` instead, which is [section 7](#7-the-control-plane).

### Dry-run it

```console
$ PR=123 pstack up --dry-run
stack: pr-123  (dry-run)
→ compose up (backend, frontend)
  [dry-run] compose up
  ✓ compose      (compose)
```

(`-q` pairs usefully with `-n`: it drops the header and the `[dry-run]` lines, leaving just the step
sequence. On its own `-q` only removes the `stack:` header.)

`--dry-run` prints each **step** in order and executes nothing. It is the fastest way to check axis
ordering. It does *not* echo the underlying shell command — for that, use `-v` on a real run
(section 4).

### The unset-variable guard

Forget `PR` and you get a hard error, not a stack named `pr-`:

```console
$ pstack validate
pstack: stack: undefined variable(s) ${PR}. Pass them in the environment or under `env:` in the spec.
$ echo $?
3
```

This is deliberate and it is load-bearing. `pr-` with `PR` unset is a name **every** PR shares, so
every PR would deploy into one stack and tear down each other's resources — collision instead of
isolation. An empty string counts as undefined too (`PR= pstack validate` fails identically).

---

## 3. Add your first axis

An axis is one *stateful* resource Compose knows nothing about. Four hooks, and the asymmetry between
them is the whole product:

| Hook | Contract | On failure |
|---|---|---|
| `up` | provision. **Must be idempotent** — it re-runs on every redeploy. | **fatal**, stops the run |
| `assert_live` | exit 0 ⇒ the resource **exists** | **fatal** — catches an `up` that lied |
| `down` | destroy | **recorded, never fatal** |
| `assert_gone` | exit 0 ⇒ the resource is **gone** | **fatal for `verify`** → exit 2 |

`up` fails fast because a half-provisioned stack should not proceed to deploy. `down` never aborts
because stopping halfway leaves *more* garbage than continuing. `verify` is strict because a teardown
that silently half-worked is the failure this tool exists to catch.

### A database-branch axis, hook by hook

Delete-and-recreate rather than reuse: a reused branch still carries the previous commit's
migrations, which is how preview schemas silently drift from `main`.

```yaml
version: 1
stack: pr-${PR}

compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]

axes:
  - name: database
    up: |
      ./hooks/db-branch.sh recreate "$STACK"
      echo "DATABASE_URL=$(./hooks/db-branch.sh url "$STACK")"
    assert_live: ./hooks/db-branch.sh count "$STACK" | grep -qx 1
    down: ./hooks/db-branch.sh delete "$STACK"
    assert_gone: |
      ./hooks/db-branch.sh ping || exit 1
      [ "$(./hooks/db-branch.sh count "$STACK")" = "0" ]
```

Two mechanics to notice:

- **`$STACK` is exported to every hook.** A hook never reconstructs `pr-${PR}` itself.
- **`KEY=VALUE` on `up`'s stdout is captured** and passed to Compose (and to later axes' hooks) as an
  env var. That's how a freshly created database hands over its connection string with no temp file.
  Only `SHOUT_CASE` keys are captured, so ordinary log chatter on stdout is ignored.

```console
$ PR=123 pstack validate
stack: pr-123
✓ spec parses — kind: isolated, 1 axis/axes, stack "pr-123"
  compose: docker-compose.preview.yml [backend, frontend]
  - database: up, down, assert_gone, assert_live
```

Each axis line lists the hooks it actually defines. A missing hook here is a gap in coverage — read
this line as a checklist.

### `validate` warns about the three ways an axis lies

These are the mistakes that produce a green teardown over a leaking host. `validate` still exits 0 —
they are warnings, not errors — but treat them as a to-do list.

```yaml
axes:
  - name: dns-record                                    # (1) no assert_gone
    up: ./hooks/dns.sh create "$STACK"
    down: ./hooks/dns.sh delete "$STACK"

  - name: queue-namespace                               # (2) bare `! <probe>`
    up: ./hooks/queue.sh create "$STACK"
    down: ./hooks/queue.sh delete "$STACK"
    assert_gone: '! ./hooks/queue.sh describe "$STACK"'

  - name: images                                        # (3) `|| true` in an assert
    down: ./hooks/images.sh prune "$STACK"
    assert_gone: |
      docker info >/dev/null 2>&1 || exit 1
      ! docker images | grep -q "$STACK" || true
```

```console
$ PR=123 pstack validate
stack: pr-123
✓ spec parses — kind: isolated, 3 axis/axes, stack "pr-123"
  - dns-record: up, down
  - queue-namespace: up, down, assert_gone
  - images: down, assert_gone
  ! axis "dns-record" has `up` but no `assert_gone` — `pstack verify` cannot prove it was cleaned up.
  ! axis "queue-namespace": `assert_gone` is a bare `! <probe>` with no reachability guard — it will report "gone" if the probe itself fails. Fail closed instead:
      <probe-is-usable> || exit 1
      ! <probe-for-this-resource>
  ! axis "images": `assert_gone` contains `|| true`, which makes it always pass. Tolerate failure in `down`, never in an assert.
```

**(1) `up` without `assert_gone`** — the axis can be created but never proven gone. `verify` reports
it `unverifiable` rather than passing it silently (section 4).

**(2) A bare `! <probe>` fails open**, and this is the subtle one. `!` inverts the exit code, so the
assert passes whenever the probe fails *for any reason* — CLI not installed, token expired, API
unreachable. "I could not tell" becomes "it is gone". Watch it happen: the hook below does not even
exist, and `verify` still reports the axis clean.

```console
$ PR=123 pstack verify -f lie.yml          # assert_gone: '! ./hooks/does-not-exist.sh describe "$STACK"'
stack: pr-123
→ verify (asserting resources are gone)
  ✓ assert_gone  queue-namespace
$ echo $?
0
```

Fail **closed** instead — prove the probe can answer, then assert absence:

```yaml
    assert_gone: |
      ./hooks/queue.sh ping >/dev/null 2>&1 || exit 1     # can't tell ⇒ FAIL, don't guess
      ! ./hooks/queue.sh describe "$STACK"
```

Same broken hook, honest answer:

```console
$ PR=123 pstack verify -f closed.yml
stack: pr-123
→ verify (asserting resources are gone)
  ✗ assert_gone  queue-namespace  — LEAKED: resource still present after teardown
  1 leaked resource(s)
$ echo $?
2
```

The message says "still present"; read it as "**could not be proven gone**", which covers both cases.
Erring toward "leaked" is the correct bias — a false alarm costs you a look, a false all-clear costs
you the host.

**(3) `|| true` forces exit 0**, so the assert can never fail. It belongs on `down` (best-effort) and
never on an assert.

Warning (2) is deliberately narrow, so a guarded assert does not trip it: a multi-line script, or a
single line containing `exit`, `||` or `&&`, is assumed to handle its own failure modes. Warnings (1)
and (3) have no such escape — (1) is purely "is there an `assert_gone` at all", and (3) matches
`|| true` anywhere in the script.

### `assert_live` catches an `up` that lied

Worth wiring up even when it feels redundant. A multi-line `up` exits with the status of its **last**
command, so a failure mid-block is invisible unless you add `|| exit 1` — which is exactly the shape
`assert_live` catches:

```console
$ PR=6 pstack up -f faillive.yml
stack: pr-6
→ up: database
  ✓ up           database
  ✗ assert_live  database  — provisioned but assert_live failed — resource missing?
$ echo $?
1
```

Without it, that stack deploys and surfaces as an opaque connection error inside the app ten minutes
later.

### More axes

[`../examples/preview.yml`](../examples/preview.yml) is a fully-commented four-axis stack — database,
queue namespace, images, ingress — with the fail-closed pattern applied to each. Two of those axes
exist because Compose won't do the job:

- **images** — `docker compose down -v` removes containers, volumes and networks but **never**
  images, so per-PR layers accumulate until the disk fills, and disk pressure then evicts the warm
  build cache that is your real per-PR speed lever.
- **ingress** — nothing to provision (routing comes from labels, TLS from a wildcard cert), so the
  axis is `assert_gone` **only**: it exists purely to prove the hostname stopped answering.

Axes are provisioned **top to bottom** and destroyed **bottom to top**. Declare a dependency before
whatever depends on it.

---

## 4. Up, status, down

### Up

```console
$ PR=123 pstack up
stack: pr-123
→ up: database
→ compose up (backend, frontend)
  ✓ up           database
  ✓ assert_live  database
  ✓ compose      (compose)
```

The `→` lines are progress, printed as work happens. The `✓` block is the **step report**, printed
once at the end. In a long deploy the arrows are what you watch; the report is what you read.

Add `-v` to see the actual commands and their output:

```console
$ PR=8 pstack up -v
stack: pr-8
→ compose up (backend)
  $ docker compose -p 'pr-8' -f 'docker-compose.preview.yml' -f 'docker-compose.preview.tls.yml' --profile 'backend' up -d --remove-orphans
```

That is the whole Compose contract, visible: `-p <stack>` for namespacing, one `-f` per file
(`compose.file` then each `compose.overlays` entry, in order), the selected profiles, and
`--remove-orphans` so relabelling from `backend+frontend` to `backend` actually stops the frontend
instead of orphaning it.

### Reading the step report

```
  ✓ assert_gone  database
  │ │            └─ axis name, or `(compose)` for the compose step
  │ └─ phase: up · assert_live · down · assert_gone · compose
  └─ mark
```

| Mark | Meaning | Where it comes from |
|---|---|---|
| `✓` | step passed | any phase |
| `?` | **unverifiable** — the axis defines no `assert_gone`, so nothing was checked. **Not a pass.** | `assert_gone` phase only |
| `✗` | step failed. On `assert_gone` this is a **leak**. | any phase |

A trailing summary line counts what matters: `1 leaked resource(s)`, `1 unverifiable axis/axes`.

A `down` step can pass **and** carry a message — teardown is best-effort by design, so a failed
`down` hook is recorded as non-fatal rather than failing the run. Here the whole Compose teardown
failed (this host has no `docker` at all) and the run still continued, exactly as intended:

```console
  ✓ compose      (compose)  — non-fatal: bash: docker: command not found
```

A `✓` with a `non-fatal:` message means *this step errored and we carried on deliberately*. Read
those messages — they are how you notice a `down` hook that has been silently failing for weeks. The
thing that would actually fail the run is the `assert_gone` that follows.

And here is `?` in the wild — the three-axis spec from section 3, whose `dns-record` axis has no
`assert_gone`:

```console
$ PR=123 pstack verify -f warn.yml
stack: pr-123
→ verify (asserting resources are gone)
  ? assert_gone  dns-record  — unverifiable: no assert_gone defined
  ✓ assert_gone  queue-namespace
  ✓ assert_gone  images
  1 unverifiable axis/axes
$ echo $?
0
```

Exit 0 with a `?` means: *everything I could check was clean.* It does **not** mean the host is
clean. Every `?` is a resource class you are trusting on faith.

### Status

```console
$ PR=123 pstack status
stack: pr-123
(nothing running)
```

`status` passes `docker compose ps` through unmodified — when containers exist you get Compose's own
table verbatim. Two synthetic replies stand in for an empty result: `(nothing running)` when `ps`
produces no output, and `(no compose section in spec)` when the spec has no `compose:` block at all.

### Verify (before teardown, as a sanity check)

Run it against a **live** stack and it should fail. If it passes while the stack is up, your
`assert_gone` is lying — go back to section 3.

```console
$ PR=123 pstack verify
stack: pr-123
→ verify (asserting resources are gone)
  ✗ assert_gone  database  — LEAKED: resource still present after teardown
  1 leaked resource(s)
$ echo $?
2
```

### Down

```console
$ PR=123 pstack down
stack: pr-123
→ compose down (all profiles)
→ down: database
→ verify (asserting resources are gone)
  ✓ compose      (compose)
  ✓ down         database
  ✓ assert_gone  database
$ echo $?
0
```

Order: Compose down first (**every** profile), then axes in **reverse** declaration order, then
`verify` — which runs by default, because a teardown that half-worked is precisely what you need to
hear about. Confirm with `-v`:

```console
$ PR=456 pstack down -v
stack: pr-456
→ compose down (all profiles)
  $ docker compose -p 'pr-456' -f 'docker-compose.preview.yml' --profile 'backend' --profile 'frontend' down -v --remove-orphans
```

All profiles from the spec appear, whatever you brought up. Compose treats a service whose profile
isn't enabled as **absent**, so tearing down with fewer profiles than you deployed leaves that
profile's resources behind — most visibly one dead `<stack>_default` network per PR, forever.
Enumerating every profile costs nothing (a profile with no matching service is a no-op).

`down -v` also removes volumes, but **never images**. That is an axis, not Compose's job.

---

## 5. Prove your teardown works

**Do this drill once per axis, before you trust it.** Nobody does and everybody should — the entire
failure mode this tool addresses is a teardown that reports success while resources survive.

The drill: break a `down` hook on purpose, tear down, and confirm the tool catches it.

### Sabotage one hook

```diff
   - name: database
-    down: ./hooks/db-branch.sh delete "$STACK"
+    down: ./hooks/db-branch.sh delete "$STACK-typo"
```

A plausible bug: wrong name, wrong region, wrong project. `delete` on a name that doesn't exist
exits **0**, because deleting nothing is not an error.

### Bring it up, then tear it down

```console
$ PR=456 pstack up
stack: pr-456
→ up: database
→ compose up (backend, frontend)
  ✓ up           database
  ✓ assert_live  database
  ✓ compose      (compose)

$ PR=456 pstack down
stack: pr-456
→ compose down (all profiles)
→ down: database
→ verify (asserting resources are gone)
  ✓ compose      (compose)
  ✓ down         database
  ✗ assert_gone  database  — LEAKED: resource still present after teardown
  1 leaked resource(s)
$ echo $?
2
```

**Read those last two step lines together — this is the whole point of the tool.**

```
  ✓ down         database        ← the teardown hook SUCCEEDED
  ✗ assert_gone  database        ← the resource SURVIVED
```

A hand-rolled teardown script sees the first line and reports success. It has no second line. That
contradiction — a clean `down` over a surviving resource — is what leaks look like in the wild, and
`✓ down` + `✗ assert_gone` is its signature.

Confirm the resource really is still there, and use `-v` to see which hook ran:

```console
$ PR=456 pstack down -v
stack: pr-456
→ compose down (all profiles)
  $ docker compose -p 'pr-456' -f 'docker-compose.preview.yml' --profile 'backend' --profile 'frontend' down -v --remove-orphans
→ down: database
  $ ./hooks/db-branch.sh delete "$STACK-typo"
→ verify (asserting resources are gone)
  $ ./hooks/db-branch.sh ping || exit 1
[ "$(./hooks/db-branch.sh count "$STACK")" = "0" ]

  ✓ compose      (compose)
  ✓ down         database
  ✗ assert_gone  database  — LEAKED: resource still present after teardown
  1 leaked resource(s)
```

There it is: `"$STACK-typo"`. (A multi-line hook is echoed across multiple lines — only the first
carries the `$` prefix.)

### Now see what `--no-verify` costs you

```console
$ PR=456 pstack down --no-verify
stack: pr-456
→ compose down (all profiles)
→ down: database
  ✓ compose      (compose)
  ✓ down         database
$ echo $?
0
```

Exit **0**. All green. The resource is still there. `--no-verify` turns pstack into exactly the
homegrown script you were trying to replace — use it only when you are deliberately deferring the
check to a separate `verify` step, never to make a red pipeline green.

### Repair and confirm

```console
$ PR=456 pstack down          # hook fixed
stack: pr-456
→ compose down (all profiles)
→ down: database
→ verify (asserting resources are gone)
  ✓ compose      (compose)
  ✓ down         database
  ✓ assert_gone  database
$ echo $?
0
```

Now you have evidence, not hope: you've seen this axis's `assert_gone` return both answers. An
`assert_gone` you have only ever seen pass may simply be incapable of failing.

**Drill checklist per axis** — you have tested it when you have seen all three:

| State | Expected |
|---|---|
| resource live | `verify` → `✗` on that axis, exit 2 |
| teardown sabotaged | `down` → `✓ down` + `✗ assert_gone`, exit 2 |
| teardown correct | `down` → `✓ assert_gone`, exit 0 |

---

## 5b. Route a whole subtree at one profile

An app that dispatches on subdomain — a tenant per host, a branch per host — wants every name under
its surface, not one router per tenant. Declare it per profile:

```yaml
compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]
  subdomains: [backend]
```

`validate` shows what that resolved to:

```console
$ PR=123 pstack validate
stack: pr-123
✓ spec parses — kind: isolated, 1 axis/axes, stack "pr-123"
  compose: docker-compose.preview.yml [backend, frontend]
  subdomains: *.backend-pr-123.preview.example.com → backend  (one label)
              PSTACK_WILD_BACKEND — interpolate it into a router label
  - db: up, assert_gone
```

Check that hostname. pstack derived it from the profile, the stack and `PREVIEW_DOMAIN`, so a wrong
domain here is a router that parses, deploys, and never matches anything. (`DOMAIN` is accepted as a
legacy alias; a name declared in the spec beats either one picked up from the ambient environment.)

pstack does not write your Traefik labels — your compose file owns them — so it hands you the rule in
an environment variable and you spend it on a router:

```yaml
  backend:
    profiles: [backend]
    labels:
      - traefik.enable=true
      # Your existing exact host. Unchanged, and it still wins: Traefik's default priority is the
      # rule's LENGTH, so this scores in the dozens against the wildcard's 2.
      - traefik.http.routers.backend.rule=Host(`backend-${STACK}.${PREVIEW_DOMAIN}`)
      - traefik.http.routers.backend.tls=true
      # The wildcard. Same service, lower priority.
      - traefik.http.routers.backend-wild.rule=${PSTACK_WILD_BACKEND}
      - traefik.http.routers.backend-wild.priority=2
      - traefik.http.routers.backend-wild.service=backend
      - traefik.http.routers.backend-wild.tls=true
      - traefik.http.services.backend.loadbalancer.server.port=3000
```

No `$$` escaping: compose interpolates the *file* from its environment and does not re-scan what it
substituted, so the rule's backticks and its `$` anchor reach Traefik intact.

The variable is exported on **every** compose subcommand, not just `up`. Compose interpolates the file
each time, so on `down` an unset variable would substitute empty and compose would be reasoning about
a differently-labelled project than the one it created.

### Before you promise anyone this works

Routing is the easy third. `subdomains: [backend]` defaults to **one label** — `api.backend-pr-123.…`
yes, `a.b.backend-pr-123.…` no — because that is the depth DNS and TLS can deliver:

- **DNS:** you need `*.backend-pr-123.<domain>` to resolve. A wildcard record covers exactly one
  label, so a single `*.<domain>` record does **not** cover this — you need a wildcard at that level,
  or a resolver you control.
- **TLS:** you need a certificate for `*.backend-pr-123.<domain>`. That is **DNS-01 only** (HTTP-01
  cannot issue wildcards) and it is **one certificate per PR**, against Let's Encrypt's ~50 per
  registered domain per week — the same ceiling described in
  [control-plane.md](control-plane.md#why-the-ceiling-is-the-reason-to-move-to-dns-01).

`{ backend: any }` matches any depth. Traefik will route it, and **no certificate can ever cover it**
— `*.*.host` is not a legal SAN. That is HTTP-only, permanently, by construction rather than by
omission. Use it for a host whose DNS you control and where plain HTTP is fine; reach for it because
a wildcard "should" be recursive and you get hostnames that resolve, route, and then fail the
handshake, which is the most confusing of the three failures to debug.

---

## 6. Run the API and UI

`pstack serve` exposes the same core over HTTP, with a single-page dashboard. Use it when a human
wants to click, or when something other than a shell needs to drive a deploy. On a real host you do
not run it by hand — [`pstack init`](#stand-up-the-host-pstack-init) runs it in the control stack,
behind Traefik, at `control.<domain>` and `api.<domain>`. Run it directly to develop against it.

### Start it

`serve` is **registry-backed and spec-free**: it acts on the deployments submitted to
`$PSTACK_DATA/deployments/` ([section 7](#submitting-a-deployment)), so it needs no `preview.yml` and
no stack variable to boot. Variables arrive per request.

```console
$ pstack serve
pstack api  http://127.0.0.1:7878
  registry: /var/lib/pstack/deployments
  auth: NONE — bound to loopback only (set PSTACK_TOKEN to expose)
```

`init` and `serve` are the two commands that skip spec loading entirely — they operate on the host
and on the registry, so neither should fail because `./preview.yml` happens to be absent.

### The safety interlock

This API can delete databases. Without a token it binds **127.0.0.1** and refuses to be exposed —
the interlock is not a warning you can click past:

```console
$ PSTACK_HOST=0.0.0.0 pstack serve
pstack: refusing to bind 0.0.0.0 without PSTACK_TOKEN set — this API can destroy
        infrastructure. Set PSTACK_TOKEN=<secret> to listen off-loopback.
$ echo $?
3
```

Set a token and it will listen anywhere. Export it rather than inlining it, so the same shell can
authenticate the `curl` calls below:

```console
$ export PSTACK_TOKEN=$(openssl rand -hex 16)
$ PSTACK_HOST=0.0.0.0 pstack serve
pstack api  http://0.0.0.0:7878
  registry: /var/lib/pstack/deployments
  auth: bearer token required for mutating routes
```

`pstack init` generates this token for you and stores it `0600` in the control stack's `.env`; it is
a **different secret** from the DNS-01 credential, with a different blast radius.

The token gates **POST/DELETE only**; GETs are always open (that is also what lets the log stream use
`EventSource`, which cannot send headers). It is **not multi-tenant** — one spec, one Docker socket,
every caller equal. Put it behind your ingress' auth or an SSH tunnel
(`ssh -L 7878:127.0.0.1:7878 preview-host`) before anyone but you can reach it.

### Drive it with curl

Against a deployed control stack the API's own hostname is **`api.<domain>`** — the honest name for
external callers (CI, `curl`, scripts). `control.<domain>` serves the same container; it is the name a
browser loads. Substitute `http://127.0.0.1:7878` when you are driving a local `serve`.

`:id` is a **registry id**, not a compose project name. The server owns the stored spec and resolves
`stack:` itself, so a client can never ask it to act on an arbitrary Compose project on the host.

```console
$ curl -s https://api.preview.example.com/api/health
{
  "ok": true,
  "authEnforced": true,
  "dataDir": "/data",
  "version": "0.1.0"
}
```

A deployment's meta plus its resolved spec summary. Spec variables ride on the **query string** —
`stack: pr-${PR}` needs `?PR=123`, and axes come back as hook *names*, never hook bodies:

```console
$ curl -s 'https://api.preview.example.com/api/deployments/pr-123?PR=123'
{
  "id": "pr-123",
  "kind": "isolated",
  "createdAt": 1785014255903,
  "updatedAt": 1785014255903,
  "stack": "pr-123",
  "busy": false,
  "compose": {
    "file": "compose.yml",
    "profiles": [
      "backend",
      "frontend"
    ],
    "overlays": []
  },
  "requires": [
    "shared-db"
  ],
  "axes": [
    {
      "name": "database",
      "hooks": [
        "up",
        "down",
        "assert_gone",
        "assert_live"
      ]
    }
  ]
}
```

Mutating without the bearer header gets nowhere:

```console
$ curl -s -X POST https://api.preview.example.com/api/deployments/pr-123/up
{
  "error": "unauthorized"
}
```

With it, you get **202 + a job id** — `up` and `down` take minutes, so nothing is answered
synchronously:

```console
$ curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
       'https://api.preview.example.com/api/deployments/pr-123/up?PR=123'
{
  "job": {
    "id": "up-pr-123-1-apeq0d",
    "stack": "pr-123",
    "action": "up",
    "state": "running"
  }
}
```

`down` takes a body; omit it and `verify` defaults to true. `force` is what lifts the `kind: shared`
guard ([section 7](#7-the-control-plane)):

```bash
curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
     -H 'content-type: application/json' -d '{"verify":true}' \
     'https://api.preview.example.com/api/deployments/pr-123/down?PR=123'
```

### Read a job

Poll the job for state, or stream its log. The job carries the same `outcome.steps` the CLI renders,
plus any captured `outputs` (whitespace compacted below; the server pretty-prints one field per
line):

```console
$ curl -s https://api.preview.example.com/api/jobs/up-pr-123-1-apeq0d
{
  "job": {
    "id": "up-pr-123-1-apeq0d",
    "stack": "pr-123",
    "action": "up",
    "state": "ok",
    "startedAt": 1785014255903,
    "log": [
      { "seq": 1, "at": 1785014255903, "level": "step", "message": "→ up: database" },
      { "seq": 2, "at": 1785014255938, "level": "step", "message": "→ compose up (backend, frontend)" }
    ],
    "outcome": {
      "ok": true,
      "steps": [
        { "axis": "database", "phase": "up", "ok": true, "code": 0, "skipped": false },
        { "axis": "database", "phase": "assert_live", "ok": true, "code": 0, "skipped": false },
        { "axis": "(compose)", "phase": "compose", "ok": true, "code": 0, "skipped": false }
      ],
      "outputs": {
        "DATABASE_URL": "postgres://user:pw@db.example.com/pr-123"
      }
    },
    "endedAt": 1785014255950
  }
}
```

`state` is the field to branch on: **`running` · `ok` · `failed` · `leaked`**. `leaked` is its own
state for the same reason exit 2 is its own code.

The SSE stream replays the buffered log from the beginning, then streams live, then closes with a
terminal frame — so attaching late still gets the whole story:

```console
$ curl -sN https://api.preview.example.com/api/jobs/down-pr-123-3-a3zn01/stream
data: {"seq":1,"at":1785014283186,"level":"step","message":"→ compose down (all profiles)"}

data: {"seq":2,"at":1785014283193,"level":"step","message":"→ down: database"}

data: {"seq":3,"at":1785014283206,"level":"step","message":"→ verify (asserting resources are gone)"}

data: {"done":true,"state":"ok"}
```

### One job per stack

A second action on a busy stack is rejected, not queued — a `down` deleting the database branch an
`up` just created is the kind of corruption that is very hard to diagnose afterwards:

```console
$ curl -s -w " [%{http_code}]\n" -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
       'https://api.preview.example.com/api/deployments/pr-777/up?PR=777'
{
  "error": "stack pr-777 already has a job in flight",
  "stack": "pr-777"
} [409]
```

On 409, attach to the running job instead of retrying. Jobs are **in-memory, capped at 50** — a
restart loses history, not correctness. Truth about what exists lives in Docker and in each axis's
`assert_*` probe.

### The UI

Open **`https://control.<domain>/`** (or `http://127.0.0.1:7878/` against a local `serve`). The UI is a
single HTML document **embedded in the bundle**, not a directory of static files: `/api/*` is the API,
and *every* other path serves that one document — so a deep link like `/deployments/pr-123` renders
instead of 404ing, and there is no filesystem lookup and therefore no path traversal to contain. No
build step; Vue 3 comes from unpkg, so first load needs internet.

The UI calls the API with **relative** `/api/…` paths. That is deliberate: loaded from
`control.<domain>` it talks to `control.<domain>/api/…` — same origin, so no CORS preflight, no
cross-origin cookie rules, and nothing to reconfigure when the hostnames change. `api.<domain>` exists
for callers that are not the UI.

It does what the CLI does, with a live log: enter a deployment id, **Load**, then `up` / `down` /
`verify`, watching the job stream and the step table. Also worth knowing:

- The token goes in the header field and is kept in `localStorage`. When `authEnforced` is false the
  header says so.
- The axis list flags an axis with `up` and no `assert_gone` as **unverifiable** — the same warning
  `validate` prints, where you are about to press the button.
- **Untick "verify after down" and the UI tells you** teardown will not check for leaks. Same trap as
  `--no-verify`.
- The step table uses **four** marks where the CLI report uses three: `✓` ok, `?` unverifiable, `!`
  leaked, `✗` otherwise failed. The CLI folds `!` into `✗` and puts the count in the summary line.

[`../ui/README.md`](../ui/README.md) documents the UI's internals and the exact routes it consumes.

---

## 7. The control plane

Everything above drives **one** spec you point `pstack` at. A host that serves several projects and
many PRs needs a second idea: deployments with identities, a kind that decides what may be done to
them, and preconditions between them.

[`control-plane.md`](control-plane.md) has the architecture. This section is the operator's view.

### Stand up the host: `pstack init`

> The blocks in this subsection are the shape `init` prints; unlike the rest of the guide they were
> not captured here, because `init` needs a real host, a real domain and a running Docker.

`pstack init` creates the **control stack** — Traefik plus the `pstack` API/UI container, in compose
project `pstack-control` — from the host. The minimum is a domain and an ACME address. **No DNS
credential**: HTTP-01 is the default.

```bash
pstack init --domain preview.example.com --acme-email <you>@example.com
```

DNS-01 is opt-in, and the provider is required only then:

```bash
PSTACK_DNS_TOKEN=<token> pstack init \
  --domain preview.example.com --acme-email <you>@example.com \
  --challenge dns01 --dns-provider cloudflare
```

Everything `init` reads:

| Flag | Environment | Default | Notes |
|---|---|---|---|
| `--domain <apex>` | `PSTACK_DOMAIN` | — | **required.** Hostnames are derived from it: `control.<domain>`, `api.<domain>`, `<service>.<domain>`, `<surface>-pr-<n>.<domain>` |
| `--acme-email <addr>` | `PSTACK_ACME_EMAIL` | — | **required.** Let's Encrypt mails expiry warnings here |
| `--challenge http01\|dns01` | `PSTACK_CHALLENGE` | `http01` | see [Choose a TLS mode](#choose-a-tls-mode). Anything else exits 3 |
| `--dns-provider <lego-code>` | `PSTACK_DNS_PROVIDER` | — | **required for `dns01` only**, ignored by `http01` |
| — | `PSTACK_DNS_TOKEN` | *unset* | the DNS-01 credential, written to `dns.env`. Omit for the tokenless providers |
| — | `PSTACK_TOKEN` | *generated* | the API bearer token. Supply it to keep or rotate a known one; leave it unset to be handed a fresh one **printed exactly once** |
| — | `PSTACK_IMAGE` | `pstack:local` | the control image. A property of the installation, not the host |
| — | `PSTACK_DATA` | `/var/lib/pstack` | where the registry and the control stack's config live |

`-n` / `--dry-run`, `-v` and `-q` behave exactly as they do everywhere else. **Two secrets, two blast
radii:** `PSTACK_DNS_TOKEN` can edit a DNS zone; `PSTACK_TOKEN` drives an API holding a read-write
Docker socket, i.e. root on the host. They are never the same value and live in different files.

What it does, in order:

| Step | Detail |
|---|---|
| 0. Preconditions | Docker socket at `/var/run/docker.sock`, the Compose v2 plugin, and the control image present. Fails by name, before anything is created — run `pstack build-image` if the image is missing — it builds from the installed package, no checkout needed |
| 1. State dirs | `<data>/deployments` (the registry) and `<data>/control/traefik-dynamic` (Traefik's file provider, created empty so the mount does not fail) |
| 2. Networks | `preview-ingress` and `preview-shared`, created idempotently. **Both must be declared `external: true` in every per-PR compose file** — declare one non-external and Compose silently makes `pr-123_preview-ingress` instead, so the container comes up healthy and unreachable |
| 3. Config | `control/docker-compose.yml` (the template, with the two challenge-dependent blocks rendered), `control/.env` (`0600`, holds `PSTACK_TOKEN`), `control/dns.env` (`0600`, holds the DNS credential; written either way so switching modes needs no extra step) |
| 4. Up | `docker compose -p pstack-control -f <path> up -d --remove-orphans` |
| 5. **Prove it** | polls the container's `HEALTHCHECK` for ~60s. `up -d` exits 0 as soon as containers are *created*, so a crash-looping API would otherwise be reported as success |
| 6. Next steps | the URLs, the rotation recipe, and the generated `PSTACK_TOKEN` — **the only time it is printed** |

It is **idempotent**: re-running it is the supported way to change the domain, rotate `PSTACK_TOKEN`,
switch challenge mode, or move to a new image. (`chmod` is re-applied unconditionally, so a `.env`
someone loosened to `0644` is tightened back on the next run.)

```bash
PSTACK_TOKEN=<new> pstack init --domain preview.example.com --acme-email <you>@example.com
```

### Why `init` is CLI-only, and always will be

`init` — and the `self-upgrade` that will follow it — are CLI-only and will never be HTTP routes,
because the API cannot recreate the stack that contains it: the process running the upgrade is inside
the container being replaced, so it is killed mid-operation, the request never returns, the job
transcript dies with the process (job history is in-memory), and a bad image leaves you with no
control plane and no remote way back. The host keeps one capability the API doesn't, and that
asymmetry *is* the recovery path.

Practically: **never submit a spec whose compose project is the one running Traefik and `pstack`.**
Nothing in the code can stop you — the API cannot reliably know its own deployment id.

### Hostnames

| Hostname | Serves |
|---|---|
| `control.<domain>` | the web UI (an operator's browser) |
| `api.<domain>` | the API (CI, `curl`, scripts) |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
| `<surface>-pr-<n>.<domain>` | a per-PR surface, e.g. `backend-pr-123.<domain>` |

`control` and `api` are **two routers pointing at one container** — the API process serves the UI. The
UI calls the API with relative `/api/…` paths, so it is same-origin from `control.<domain>` and needs
no CORS; `api.<domain>` exists to give external callers an honest name that is not "the UI host".

**Flatten per-PR hostnames with dashes.** A wildcard matches exactly **one** label:
`backend-pr-1.<domain>` is covered by `*.<domain>`, `backend.pr-1.<domain>` is not.

### Choose a TLS mode

Both modes get certificates from Let's Encrypt through Traefik's `le` resolver. They differ in what
proves you own the domain, and that difference decides how many PRs a week the host can certify.

| | `--challenge http01` (default) | `--challenge dns01` |
|---|---|---|
| Credential | **none** | a DNS API token to obtain, store and rotate |
| Hard requirement | **port 80 reachable from the internet** | the provider's API reachable |
| Traefik flags | `httpchallenge=true`, `httpchallenge.entrypoint=web` | `dnschallenge=true`, `dnschallenge.provider=<code>` |
| Wildcards | **cannot issue them** — one certificate per hostname | one `*.<domain>` covers every present and future host |
| New-cert budget | ~50/week per registered domain ⇒ **~16 PRs/week** at 3 surfaces each | one certificate, so no per-PR issuance and no ceiling |
| URL valid before deploy | **no** — the hostname must already have a container answering | **yes** — the wildcard exists before the stack does |
| Per-PR router labels | `tls=true` **and** `tls.certresolver=le` | `tls=true` **and nothing else** |

**The redirect is not a problem for HTTP-01.** The control stack redirects `web` → `websecure`, and
Traefik still answers the challenge: it installs an internal ACME router at maximum priority that
bypasses the redirect for `/.well-known/acme-challenge/`. The one thing that *does* break HTTP-01 is
port 80 not being reachable from the internet.

#### The arithmetic that makes you move to DNS-01

Let's Encrypt allows roughly **50 new certificates per registered domain per week**. Renewals do not
count against it; a separate duplicate-certificate limit caps 5 identical certificates per week.
HTTP-01 cannot issue a wildcard, so **every hostname is a new certificate**:

```
3 surfaces per PR (backend, frontend, admin)  ×  1 certificate each  =  3 new certs per PR
50 new certs per week  ÷  3  ≈  16 new PRs per week
```

Past that, issuance starts failing and the symptom is a browser TLS error on a preview that deployed
fine. There is no way to raise it and no way to make HTTP-01 issue one certificate for many hosts —
the only fix is DNS-01. The second reason to move: HTTP-01 cannot certify a hostname **before its
container exists**, so a preview URL is invalid until the stack is actually deployed, which surfaces
as a certificate error that is really a sequencing problem. DNS-01's wildcard is valid immediately.

Start on `http01`; switch with `--challenge dns01 --dns-provider <code>` when PR volume or pre-deploy
URLs demand it. Re-running `init` is the whole migration.

#### The per-PR `certresolver` rule is OPPOSITE per mode

This is the easiest thing in the whole system to get wrong, and it is expensive in one direction.

| Mode | Every per-PR router | Why |
|---|---|---|
| `http01` | `tls=true` **+** `tls.certresolver=le` | there is no wildcard to inherit; each hostname must resolve its own certificate on first request. Omit the resolver and that host has no certificate, ever |
| `dns01` | `tls=true` — **nothing else** | **exactly one** always-on router requests the wildcard (`tls.domains[0].main=<domain>` + `.sans=*.<domain>`, on the control router). Every other router inherits it by SNI |

> **Under DNS-01, adding `tls.certresolver=le` to a per-PR router orders a SEPARATE certificate for
> that hostname** — which is how a fleet of previews silently burns the ~50-per-week budget and takes
> TLS down for the whole host, including the control plane. Copying a router label block from an
> HTTP-01 host into a DNS-01 host is exactly how it happens.

#### DNS-01 credentials

Provider codes and variable names are lego's, and only the verified ones are written for you. A wrong
variable name surfaces as an ACME **"propagation timeout"**, which sends you debugging DNS instead of
a typo — so an unrecognised provider gets a `CHANGEME_VARIABLE_NAME` line pointing at
[lego's provider list](https://go-acme.github.io/lego/dns/) rather than a guess.

| Provider | `--dns-provider` | Credential in `dns.env` |
|---|---|---|
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN` — **one** token with `Zone:Read` + `DNS:Edit` |
| Hetzner | `hetzner` | `HETZNER_API_TOKEN` (optionally `HETZNER_PROPAGATION_TIMEOUT`, `HETZNER_TTL`) |
| AWS Route 53 | `route53` | **none** — satisfied by the instance's IAM profile |
| Google Cloud DNS | `gcloud` | **none** — satisfied by the VM's attached service account |

Every lego variable also accepts a `_FILE` suffix if you would rather mount the secret than store it.
It is `HETZNER_API_TOKEN` — **never `HETZNER_API_KEY`**, which does not exist and fails as a
propagation timeout. Prefer the tokenless providers where you have the choice: nothing to store,
nothing to rotate.

#### DNS records you need either way

Point a wildcard at the box so *any* per-PR hostname resolves:

```
*.preview.example.com.   A   <host-ip>
preview.example.com.     A   <host-ip>
```

Under DNS-01 that wildcard record plus the apex is also what the single wildcard **certificate**
covers. Under HTTP-01 you still want the wildcard record — resolution and certification are separate
problems — but each hostname is certified individually, on its first HTTPS request.

### `shared` vs `isolated`

`kind` is the second field in a spec and it decides how the deployment may be treated:

| | `kind: isolated` (default) | `kind: shared` |
|---|---|---|
| What | one tenant — normally one PR | a host singleton every preview borrows: a database, a queue cluster, a registry mirror |
| Axes | yes — the point of the tool | **none allowed**; declaring one is a hard error |
| `down` | routine | **refused without `--force`** |
| Over the API | `down` works | refused with **409** unless the body says `{ "force": true }` |

```yaml
version: 1
kind: shared
stack: shared-db          # a fixed identity, not a template — there is only one per host

compose:
  file: docker-compose.shared.yml
  profiles: []            # nothing is profiled: a shared stack is always fully on
```

```console
$ pstack -f shared.yml validate
stack: shared-db
✓ spec parses — kind: shared, 0 axis/axes, stack "shared-db"
  compose: docker-compose.shared.yml [no profiles]
```

Axes on a shared deployment are rejected at parse time, not warned about — it is almost always a
spec that meant `isolated`:

```console
$ pstack -f axes-on-shared.yml validate
pstack: kind: shared cannot declare axes (found 1). Axes exist to isolate one tenant from another and to prove a tenant's resources were cleaned up; a shared singleton has neither concern. Did you mean `kind: isolated`?
$ echo $?
3
```

### `--force`, and why the guard exists

`down` runs `docker compose down -v`. On a PR stack the `-v` removes that tenant's own volumes —
routine. On a shared deployment the *same verb* removes Traefik's `acme.json` (every certificate for
the host, re-issued under a per-week rate limit), the shared database volume (every preview's
state), and your admin credentials. Same command, entirely different blast radius:

```console
$ pstack -f shared.yml down
stack: shared-db
refusing to tear down shared deployment "shared-db"
  ✗ compose      shared-db  — refused: kind is `shared`. `down` removes volumes (-v), which on a shared deployment destroys state every tenant depends on. Re-run with --force if that is truly intended.
$ echo $?
1
```

`--force` lifts it, and nothing else does — over HTTP that is `{ "force": true }` in the
`POST /api/deployments/:id/down` body, and without it the API answers **409 synchronously** rather
than handing back a job id that is going to fail. Reach for it only when destroying that state is
the actual goal; to *upgrade* a shared deployment, run `up` — it converges and never removes:

```console
$ pstack -f shared.yml down --force --dry-run
stack: shared-db  (dry-run)
→ compose down (all profiles)
  [dry-run] compose down
→ verify (asserting resources are gone)
  ✓ compose      (compose)
$ echo $?
0
```

Note the guard keys off the **declared kind**, not off "does it have axes". An isolated deployment
that forgot its axes must not silently inherit a singleton's protection — which is exactly why the
no-axes warning in section 2 tells you to consider `kind: shared`.

### `requires:` — fail by name, before anything is created

An isolated deployment usually depends on shared ones: an ingress network, a reachable queue, a
database endpoint. Without a preflight, a missing dependency surfaces partway through an axis hook
as whatever error that CLI printed — which tells you nothing about what is actually wrong, *after*
some resources already exist.

```yaml
requires:
  - name: shared-db
    assert: docker network inspect preview-shared >/dev/null 2>&1
    hint: bring the shared deployment up first — `pstack -f shared.yml up`
```

`validate` lists them; `up` asserts every one **before** the first axis runs:

```console
$ PR=123 pstack -f req.yml validate
stack: pr-123
✓ spec parses — kind: isolated, 1 axis/axes, stack "pr-123"
  requires: shared-db
  compose: docker-compose.preview.yml [backend]
  - database: up, down, assert_gone
```

```console
$ PR=123 pstack -f req.yml up
stack: pr-123
→ requires: shared-db
  ✗ requires     shared-db  — unmet — bring the shared deployment up first — `pstack -f shared.yml up`
$ echo $?
1
```

Nothing was provisioned. Write the `hint` as **how to fix it** — the assert already says what broke.
Requirements are asserted in declaration order and the first failure stops the run.

### Submitting a deployment

The API is **registry-backed**: it holds many deployments under `$PSTACK_DATA/deployments/<id>/`
(default `/var/lib/pstack`) and acts on them by id.

```
/var/lib/pstack/deployments/pr-123/
    spec.yml       the submitted spec
    compose.yml    the submitted compose file, when one was sent with it
    meta.json      { id, kind, createdAt, updatedAt }
```

Plain files on purpose: the registry is a **cache of intent**, never the source of truth about what
exists — truth stays in Docker and in each axis's `assert_*` probe. Losing it means "I forgot what
you asked for", not "the host is inconsistent", and the fix is to re-submit. It is also greppable,
diffable and `tar`-able, which a database is not.

```bash
API=https://api.preview.example.com

# submit or replace  → 201 new, 200 replaced
curl -sS -X PUT -H "Authorization: Bearer $PSTACK_TOKEN" \
     -H 'content-type: application/json' \
     -d "$(jq -n --rawfile s preview.yml --rawfile c docker-compose.preview.yml \
             '{spec:$s, compose:$c, env:{PR:"123"}}')" \
     "$API/api/deployments/pr-123"

curl -sS  "$API/api/deployments"                     # list, with busy + running
curl -sS  "$API/api/deployments/pr-123?PR=123"       # meta + spec summary

curl -sS -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
     "$API/api/deployments/pr-123/up?PR=123"         # → 202 { job }

curl -sS -X DELETE -H "Authorization: Bearer $PSTACK_TOKEN" \
     "$API/api/deployments/pr-123?PR=123"            # forget, after a clean down
```

Five things that will bite you if you skip them:

- **Variables ride on the query string, and are not stored.** A spec resolving `stack: pr-${PR}`
  needs `?PR=123` on *every* call — `GET`, `up`, and the later `down`. Pass different ones to `down`
  than you did to `up` and teardown targets a different stack than deploy created. A missing
  variable is a `400` naming it, never a stack called `pr-`.
- **`PUT` is refused (409) while that stack has a job in flight.** Swapping the spec mid-job means
  the eventual `down` runs with different profiles and axes than `up` created.
- **`DELETE` fails closed.** It refuses while containers exist, *and* refuses when Docker did not
  answer — "could not tell" is not evidence of absence. Forgetting a live deployment orphans it
  beyond the control plane's view, which is exactly the leak this tool exists to prevent. Always
  `down` first.
- **Submitted hooks cannot use relative paths.** A deployment's runner runs with `cwd` set to its
  own directory (so `compose: { file: compose.yml }` finds the file `PUT` wrote), and only
  `spec.yml` and `compose.yml` live there. `up: ./hooks/db.sh …` works from a CLI checkout and
  cannot work over the API — use inline shell or absolute paths.
- **`:id` is a registry id, not a compose project name.** The server owns the stored spec and
  resolves `stack:` itself, so a client can never ask it to act on an arbitrary compose project.

The lifecycle itself is importable — `dist/index.js` is the package's `exports` entry, so embedding it
in your own tooling is a normal import:

```ts
import { loadSpec, createRunner, down } from '@samyx/preview-stacks';

const spec = await loadSpec('preview.yml', { ...process.env, PR: '123' });
const result = await down(spec, createRunner({ dryRun: false }));
if (!result.ok) throw new Error('something leaked');
```

The registry is **not** part of that surface — `src/index.ts` deliberately does not re-export it, so
`Registry` / `dataDir` are reachable only from a checkout (`./src/registry.ts`). Drive stored
deployments over the API instead:

```ts
const reg = new Registry(dataDir());                                       // checkout only
await reg.put('pr-123', specYaml, { composeYaml, env: { PR: '123' } });    // validates first
const stack = await reg.resolve('pr-123', { PR: '123' });
```

`put` parses before it commits and deletes the directory if the spec is rejected, so a bad
submission never leaves a half-created deployment behind. Ids must match
`/^[a-z0-9][a-z0-9._-]{0,63}$/` — they become directory names and reach shell hooks, so no
traversal, no spaces, no metacharacters.

For CI, prefer the CLI with `-f` and `--set` (section 8): it needs no host access and no token.

---

## 8. Wire it into CI

Two jobs: bring the preview up on demand, tear it down when the PR closes. Keep previews **opt-in by
label** — most PRs don't need one, and every live preview costs a database branch and a slice of a
shared host.

```yaml
name: preview

on:
  pull_request:
    types: [labeled, synchronize, closed]

jobs:
  up:
    # Deploy on the label, and on new commits once labelled.
    if: >-
      github.event.action != 'closed' &&
      contains(github.event.pull_request.labels.*.name, 'preview')
    runs-on: [self-hosted, preview-host]
    concurrency:                       # never two lifecycle runs for one PR
      group: preview-${{ github.event.pull_request.number }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v4          # your repo: preview.yml, hooks, compose file
      - uses: oven-sh/setup-bun@v2
        with: { bun-version: 1.3.12 }
      - run: bun add -g @samyx/preview-stacks

      - name: Bring the preview up
        env:
          PR: ${{ github.event.pull_request.number }}
          GIT_SHA: ${{ github.event.pull_request.head.sha }}
          DB_API_TOKEN: ${{ secrets.DB_API_TOKEN }}
        run: pstack up -v

  down:
    if: github.event.action == 'closed'
    runs-on: [self-hosted, preview-host]
    concurrency:
      group: preview-${{ github.event.pull_request.number }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v4
      - uses: oven-sh/setup-bun@v2
        with: { bun-version: 1.3.12 }
      - run: bun add -g @samyx/preview-stacks

      - name: Tear the preview down
        env:
          PR: ${{ github.event.pull_request.number }}
          GIT_SHA: ${{ github.event.pull_request.head.sha }}
          DB_API_TOKEN: ${{ secrets.DB_API_TOKEN }}
        run: |
          set +e
          pstack down -v
          code=$?
          set -e
          case $code in
            0) echo "preview torn down clean" ;;
            2) echo "::error::preview LEAKED — resources survived teardown, fix the axis down hook"
               exit 1 ;;
            *) echo "::error::teardown failed (exit $code)"
               exit 1 ;;
          esac
```

**The trap that eats exit codes.** GitHub Actions runs `run:` blocks under `bash -e`, so a bare
`pstack down` returning 2 aborts the step *before* your `case` can read `$?`. Wrap the call in
`set +e` / `set -e` (or `pstack down || code=$?`). Get this wrong and every leak looks like a generic
step failure — which throws away the one distinction the exit codes exist to make.

Why the codes are worth branching on at all:

| Exit | Meaning | Who fixes it |
|---|---|---|
| 0 | torn down, nothing survived | nobody |
| 1 | teardown errored | whoever broke the hook or the runner |
| 2 | **torn down but something survived** | whoever owns that axis — and it needs cleaning up **now**, nothing else will |
| 3 | bad spec or usage | whoever edited `preview.yml` |

Notes on the rest of it:

- **`if: always()` on teardown, if the job does anything before it.** A teardown that only runs on
  success is a leak generator.
- **`labeled` fires for *any* label**, so adding an unrelated label to a PR that already carries
  `preview` re-triggers the deploy. Harmless — `up` hooks are idempotent — but it is the answer to
  "why did my preview redeploy when I didn't push anything".
- **`concurrency` per PR** mirrors the API's one-job-per-stack lock. Without it, a `closed` event
  arriving during a redeploy races over the same resources. Do **not** set `cancel-in-progress: true`
  on teardown — a cancelled teardown is a half-teardown.
- **Pass every variable the spec interpolates** (`PR`, `GIT_SHA`, …) *and* every secret the hooks
  need. A missing one is exit 3 at parse time, before anything is touched — which is the good failure.
- **`-v` in CI**, always. The log is the only forensics you get after the runner is gone.
- **Validate the spec on every PR** — a cheap job on its own, needing neither Docker nor runner
  privileges: `PR=0 pstack validate` (add `GIT_SHA=0000000` or whatever else it interpolates).
- **`bun add -g` is the whole install**, so this workflow runs on a hosted runner too if your hooks
  and Docker do. Pin the pstack version (`@samyx/preview-stacks@<x.y.z>`) if you want teardown to
  behave identically to the deploy that created the stack.

---

## 9. Day-2 operations

### The nightly sweep

Teardown runs on PR close. The sweep is what catches the closes that never ran — a cancelled
workflow, a runner that died mid-teardown, a PR closed while the box was down. Iterate the PRs that
*should* be gone and let `verify` answer:

`sweep.sh`:

```bash
#!/usr/bin/env bash
# Verify every given PR is really gone. Exit 1 if anything leaked.
leaked=()
for pr in "$@"; do
  pstack --set PR="$pr" verify -q >/dev/null 2>&1
  case $? in
    0) echo "  pr-$pr clean" ;;               # clean, or unverifiable — see below
    2) echo "  pr-$pr LEAKED"; leaked+=("$pr") ;;
    *) echo "  pr-$pr verify errored" ;;
  esac
done
if (( ${#leaked[@]} )); then
  printf 'LEAKED: %s\n' "${leaked[*]}"
  exit 1
fi
echo "sweep clean"
```

```console
$ ./sweep.sh $(gh pr list --state closed --limit 100 --json number --jq '.[].number')
  pr-201 LEAKED
  pr-202 clean
  pr-203 LEAKED
LEAKED: 201 203
$ echo $?
1
```

Once the leaks are dealt with, the same sweep is the proof:

```console
$ ./sweep.sh 201 202 203
  pr-201 clean
  pr-202 clean
  pr-203 clean
sweep clean
$ echo $?
0
```

`--set PR=<n>` is the flag that makes a sweep possible: it defines or overrides a spec variable for
one invocation without touching the environment (overrides win over the ambient env). It is also the
right tool for any one-off — re-verifying a single old stack, testing a spec change against a
throwaway number:

```console
$ PR=1 pstack --set PR=42 validate
stack: pr-42
```

Remember `verify` exits **0** for an axis with no `assert_gone`. A sweep is only as complete as your
`assert_gone` coverage — run `pstack validate` in the sweep too and treat new `!` warnings as sweep
blind spots.

### Finding orphans

The sweep only checks stacks you can enumerate. Orphans are the ones nobody remembers — the PR
number is lost, so `verify` never gets pointed at them. Ask the host instead:

```bash
docker compose ls --all                       # every compose project on the box
docker network ls --filter name=_default      # one dead <stack>_default per leaked teardown
docker images                                 # per-PR tags compose never removes
docker system df                              # is the disk actually the problem?
```

Or over the API. `GET /api/deployments` lists everything ever *submitted*, cross-referenced against
`docker compose ls --all`, so you can tell intent from reality:

```console
$ curl -s https://api.preview.example.com/api/deployments
{
  "deployments": [
    {
      "id": "pr-123",
      "kind": "isolated",
      "createdAt": 1785014255903,
      "updatedAt": 1785014255903,
      "stack": "pr-123",
      "busy": false,
      "running": true
    }
  ]
}
```

- **`busy`** — a job holds that stack's lock. Don't act on it; attach to the job instead.
- **`running`** — a compose project by that name exists on the host.
- **Both are `null`, never `false`, when they could not be determined**: an unresolved spec has no
  stack name to look up, and a Docker that did not answer is not the same fact as "nothing is
  running". A row whose variables were not supplied on the query string carries `unresolved` with the
  reason, instead of failing the whole listing — one broken deployment must not hide the other twenty.

`running: true` with a closed PR is your orphan list. The reverse — a compose project on the host with
**no** row here — is a stack submitted before the registry was wiped, or one created by hand; the
`docker` commands above are the only way to see those.

A cluster of `<stack>_default` networks with no containers is the signature of the profile bug: a
teardown that passed fewer profiles than the deploy did. `pstack down` cannot produce it; a
hand-rolled script can.

### When `verify` reports a leak

```
  ✗ assert_gone  database  — LEAKED: resource still present after teardown
```

1. **Believe it, but confirm it by hand.** Run the axis's probe yourself. You are distinguishing
   *the resource survived* from *the assert is wrong* — a false alarm from a broken probe is common
   and sends you hunting for nothing.
2. **Re-run `pstack down`.** `down` hooks must be idempotent, so this is safe, and a transient API
   failure clears on the retry. Most leaks end here.
3. **Still leaking? Read the hook with `-v`.** `pstack down -v` prints the exact hook that ran
   (section 5). Wrong name, wrong region, wrong credentials, or a resource that must be drained
   before deletion are the usual four.
4. **Clean it up manually, then fix the hook.** Manual cleanup makes `verify` green and hides the
   bug; the next PR leaks identically. Do both, in that order.
5. **Confirm the fix with the drill.** Sabotage the repaired hook and make sure the leak is still
   caught (section 5). A leak that reappears silently is worse than the first one.

### Routine hygiene

| Cadence | Do |
|---|---|
| every PR | `validate` in CI; keep `!` warnings at zero |
| every teardown | let `verify` run — don't reach for `--no-verify` |
| nightly | the sweep above; alert on exit 1 |
| weekly | `docker system df` on the host; a growing disk means an axis you haven't modelled yet |
| per new axis | the section 5 drill before it goes near a shared host |

---

## 10. Reference

Derived from `src/cli.ts`, `src/init.ts`, `src/api.ts`, `src/spec.ts`, `src/compose.ts`,
`src/registry.ts` and `templates/control/docker-compose.yml`.

### Commands

```
pstack <up|down|verify|status|validate|init|serve> [flags]
```

| Command | Does | Exits |
|---|---|---|
| `up` | assert every `requires` **first**, then provision axes in declaration order (each `up`, then its `assert_live`), then `compose up -d --remove-orphans` with the **selected** profiles. **Fails fast** — stops at the first failure. | 0 · 1 |
| `down` | `compose down -v --remove-orphans` with **all** profiles → each axis's `down` in **reverse** order (best-effort, never fatal) → `verify` unless `--no-verify`. **Refused on `kind: shared` without `--force`** (exit 1). | 0 · 1 · 2 |
| `verify` | run every `assert_gone`. Axes without one are reported `unverifiable`, not passed. | 0 · 2 |
| `status` | `compose ps` for this stack, passed through | 0 |
| `validate` | parse, resolve interpolation, list axes and hooks, print warnings. Touches nothing. | 0 · 3 |
| `init` | stand up the control stack (`pstack-control`: Traefik + the API/UI) on **this host**. Idempotent. Preconditions → dirs → networks → config → `compose up -d` → wait for the healthcheck. **Never an HTTP route.** | 0 · 1 · 3 |
| `serve` | HTTP API + UI over the deployment registry. Runs until killed. | 3 on refusal |

`init` and `serve` are the two **spec-free** commands: they act on the host and on the registry, so
neither loads `preview.yml` and neither fails because it is absent.

Every teardown step is recorded non-fatally, so `down` in practice returns 0 or 2 — a failed
`assert_gone` is the only thing that moves the needle. The two ways it still returns 1: the
`kind: shared` refusal (nothing ran at all), and an unhandled crash.

### Flags

| Flag | Applies to | Effect |
|---|---|---|
| `-f`, `--file <path>` | all but `init` `serve` | spec file. Default `preview.yml`. |
| `-n`, `--dry-run` | `up` `down` `verify` `init` | print each step, execute nothing. Prints step **labels**, not shell commands. **A green dry-run proves ordering, never absence** — `requires` asserts and `init`'s preconditions are skipped and report ok, so `init -n` says nothing about whether Docker or the control image exist. |
| `-v`, `--verbose` | all | echo each command as `$ <cmd>` plus its stdout/stderr, indented `\|`. Ignored under `--dry-run`. |
| `-q`, `--quiet` | all | narrower than it sounds: on its own it suppresses only the `stack: …` header. Step lines (`→ …`) and the final report **always print**. Combined with `-n` it also drops the `[dry-run]` lines. |
| `--set K=V` | all | define/override a spec variable; repeatable. Wins over the ambient environment, but a key the spec's own `env:` defines still wins over `--set`. |
| `--no-verify` | `down` | skip the post-teardown leak check. Turns a leak into exit 0. |
| `--force` | `down` | allow tearing down a `kind: shared` deployment. Nothing else lifts that guard, and the HTTP API never passes it. |
| `--domain <apex>` | `init` | **required** (or `PSTACK_DOMAIN`). Every hostname is derived from it. |
| `--acme-email <addr>` | `init` | **required** (or `PSTACK_ACME_EMAIL`). |
| `--challenge http01\|dns01` | `init` | default **`http01`** (or `PSTACK_CHALLENGE`). Any other value exits 3. |
| `--dns-provider <lego-code>` | `init` | required for `dns01` only (or `PSTACK_DNS_PROVIDER`); ignored by `http01`. |
| `-h`, `--help` | — | usage, exit 0 |

An unknown flag, an unknown command, no command, a malformed `--set`, a bad `--challenge`, a missing
`--domain`/`--acme-email`, or `dns01` without a provider all exit **3**.

### Exit codes

| Code | Name | Meaning |
|---|---|---|
| **0** | ok | operation succeeded; nothing checkable leaked |
| **1** | failed | an operation errored — `up` hook, `assert_live`, compose up |
| **2** | leaked | teardown ran but `assert_gone` says something survived, **or** could not be proven gone |
| **3** | usage | bad spec, bad flag, missing variable, spec not found, `serve` refusing to bind |

2 is distinct from 1 on purpose: "torn down but something survived" and "teardown errored" are
different problems with different owners.

### Environment

| Variable | Used by | Default | Meaning |
|---|---|---|---|
| `PSTACK_TOKEN` | `serve` `init` | *unset* / *generated* | bearer token for mutating routes. **Required** to bind off-loopback. `init` generates one when unset and prints it once. |
| `PSTACK_PORT` | `serve` | `7878` | listen port |
| `PSTACK_HOST` | `serve` | `127.0.0.1` | listen address. Forced to `127.0.0.1` without a token; a non-loopback value without a token is refused with exit 3. |
| `PSTACK_DATA` | `serve` `init` | `/var/lib/pstack` | registry + control-stack config root. The registry lives at `<PSTACK_DATA>/deployments`. |
| `PSTACK_DOMAIN` | `init` | — | same as `--domain` |
| `PSTACK_ACME_EMAIL` | `init` | — | same as `--acme-email` |
| `PSTACK_CHALLENGE` | `init` | `http01` | same as `--challenge` |
| `PSTACK_DNS_PROVIDER` | `init` | — | same as `--dns-provider` |
| `PSTACK_DNS_TOKEN` | `init` | *unset* | the DNS-01 credential; written to `control/dns.env` (`0600`) under the provider's own variable name. **Flag-less on purpose** — a secret does not belong in a shell history. |
| `PSTACK_IMAGE` | `init` | `pstack:local` | the control-stack image |

`serve` needs **no** spec and no stack variable to start.

### HTTP API

`:id` is a **registry id** (e.g. `pr-123`), never the resolved stack name. Auth applies to
**POST/PUT/DELETE** only; every `GET` is open, which is also what lets the log stream use
`EventSource` (it cannot send headers). `GET /api/jobs/:id` returns a whole hook transcript, so the
ingress in front of this is the only gate on reads.

| Method | Route | Body / query | Returns |
|---|---|---|---|
| GET | `/api/health` | — | `{ ok, authEnforced, dataDir, version }` |
| GET | `/api/deployments` | spec variables as `?K=V`, **optional** | `{ deployments: [{ …meta, stack, busy, running, unresolved? }] }`. A row whose variables were not supplied degrades to `stack: null` + `unresolved: <reason>` rather than failing the listing; `busy`/`running` are `null` when undeterminable |
| GET | `/api/deployments/:id` | spec variables as `?K=V`, **required** | `{ id, kind, createdAt, updatedAt, stack, busy, compose, requires, axes[{name,hooks}] }` — hook **names**, never bodies |
| PUT | `/api/deployments/:id` | `{ spec, compose?, env? }` — **body only**, the query string is *not* read here | `{ id, kind, stack, createdAt, updatedAt }` · **201** new · **200** replaced · 400 bad spec/body · 409 while a job is in flight. The spec is **parsed before it is stored**, so its variables must be in the body's `env` or the submit is a 400 |
| DELETE | `/api/deployments/:id` | spec variables as `?K=V` | forget it. Refused while containers exist **and** when Docker did not answer |
| POST | `/api/deployments/:id/up` | spec variables as `?K=V` | **202** `{ job }` · 409 if busy |
| POST | `/api/deployments/:id/down` | `{ verify?, force? }` (`verify` defaults true) | **202** `{ job }` · 409 if busy · 409 on `kind: shared` without `force` |
| POST | `/api/deployments/:id/verify` | spec variables as `?K=V` | **202** `{ job }` · 409 if busy |
| GET | `/api/jobs` | — | `{ jobs: [...] }`, newest first, max 50, in-memory |
| GET | `/api/jobs/:jobId` | — | `{ job }` · 404 |
| GET | `/api/jobs/:jobId/stream` | — | SSE: buffered log replay, then live, then `{done:true,state}` |
| GET | `/` and **any** non-`/api/` path | — | the embedded single-page UI. No filesystem lookup, so a deep link renders rather than 404s |

Status codes: **202** accepted · **400** bad spec or missing variable · **401** unauthorized ·
**404** unknown deployment/job · **405** wrong method on an action · **409** job in flight for that
stack, or a `kind: shared` `down` without `force` · **500** unexpected.

Job `state`: `running` · `ok` · `failed` · `leaked`.

### Control-stack hostnames

| Hostname | Router | Serves |
|---|---|---|
| `control.<domain>` | `pstack-ui` | the web UI |
| `api.<domain>` | `pstack-api` | the API |

Both point at the **same** container on port `7878`; the UI calls `/api/…` relatively, so it is
same-origin and needs no CORS. Under `dns01` the `pstack-ui` router is the **one** router carrying
`tls.domains[0].main` + `.sans` — the wildcard every other router inherits by SNI.

### Spec schema

```yaml
version: 1                        # required, must be 1
kind: isolated                    # optional; `isolated` (default) or `shared`
stack: pr-${PR}                   # required — identity; must match /^[a-z0-9][a-z0-9_-]*$/

env:                              # optional; interpolated in order, later entries may use earlier
  PREVIEW_DOMAIN: preview.example.com
  IMAGE_TAG: ${GIT_SHA}

compose:                          # optional
  file: docker-compose.preview.yml   # required within `compose`
  profiles: [backend, frontend]      # up: these · down: ALL of them
  overlays: [docker-compose.tls.yml] # extra -f files, applied in order after `file`

requires:                         # optional; asserted BEFORE anything is created, in order
  - name: shared-db               # required — what the failure is reported as
    assert: <shell>               # required; exit 0 ⇒ the precondition holds
    hint: <text>                  # optional; shown on failure — say how to fix it

axes:                             # optional; up in order, down in REVERSE
  - name: database                # required, unique
    up: <shell>                   # provision, idempotent, fatal on failure
    assert_live: <shell>          # exit 0 ⇒ exists; fatal on failure
    down: <shell>                 # destroy; best-effort, never fatal
    assert_gone: <shell>          # exit 0 ⇒ gone; fatal for verify (exit 2)
```

| Rule | Detail |
|---|---|
| `kind: shared` | a host singleton. **May declare no axes** — a hard error, not a warning. `down` is refused unless `--force`, and the HTTP API never passes `force`, so it cannot be torn down over HTTP at all. |
| `kind: isolated` | the default. With no axes you get a warning: nothing per-tenant is provisioned or verified, so it is just a Compose project. |
| `requires` | every `assert` runs before the first axis, in declaration order; the first failure aborts `up` with exit 1 and nothing has been created. `hint` is appended to the failure message. |
| interpolation | `${VAR}` only, resolved **once** at parse time, so a value containing `${…}` is never re-expanded |
| precedence | spec `env:` **wins over** `--set`, which wins over the ambient environment. `--set` cannot override a key the spec's `env:` also defines — that is a spec edit, not a CLI override. |
| undefined variable | **hard error**, exit 3. Empty string counts as undefined. |
| resolution order | `env:` entries (in declaration order, each able to use earlier ones) → `stack:` → `compose.*` → axis hooks |
| `${STACK}` in the spec | available in `compose.*` and axis hooks, **not** in `env:` — `env:` is resolved before `stack:` is. `env: X: ${STACK}` fails with `undefined variable(s) ${STACK}`. |
| `$STACK` in a hook | always exported at runtime, so `$STACK` and `${STACK}` both work inside a hook body |
| hook environment | resolved spec `env` + `STACK` + everything captured from earlier `up` hooks |
| `KEY=VALUE` capture | `SHOUT_CASE` lines on an `up` hook's stdout become env vars for compose and later hooks |
| empty axis | an axis with none of the four hooks is an error |
| duplicate axis name | error |
| stack name | must match `/^[a-z0-9][a-z0-9_-]*$/` — it becomes a Compose project name, a hostname label and usually a namespace id |
| shell | every hook runs via `bash -c`. Not a sandbox — treat the spec at CI trust level. |

### `validate` warnings

All non-fatal; `validate` still exits 0. Each one is a way an axis can lie to you.

| Warning | Trigger | Fix |
|---|---|---|
| `kind: isolated` with no axes | an isolated spec that has a `compose:` block and no axes | add the axes it needs, or mark it `kind: shared` if it is a host singleton so `down` is guarded |
| has `up` but no `assert_gone` | an axis defines `up` and no `assert_gone` | add a probe, or accept a permanent `?` in every report |
| `assert_gone` is a bare `! <probe>` | a single-line `assert_gone` starting with `!` and containing no `exit`, `\|\|` or `&&` | guard it: `<probe-is-usable> \|\| exit 1`, then `! <probe>` on the next line |
| `assert_gone` contains `\|\| true` | `\|\| true` anywhere in the `assert_gone` script | remove it; tolerate failure in `down`, never in an assert |

### The deployment registry

| | |
|---|---|
| Location | `$PSTACK_DATA/deployments/<id>/` — `PSTACK_DATA` defaults to `/var/lib/pstack` |
| Files | `spec.yml` · `compose.yml` (when submitted) · `meta.json` (`{ id, kind, createdAt, updatedAt }`) |
| Id charset | `/^[a-z0-9][a-z0-9._-]{0,63}$/`, no `..` — ids become directory names and reach shell hooks |
| `put` | validates before committing; `rm -rf`s the directory and throws `RegistryError` if the spec is rejected. `kind` is read from the parsed spec, never from the caller. |
| `remove` | **forgets only, never tears down.** Callers must `down` first — removing the record while containers run orphans them beyond the control plane's view. |
| Why files | it is a cache of *intent*; truth is Docker + each axis's `assert_*`. Losing it is recoverable by re-submitting, and a directory of YAML is greppable and diffable. |

### The two compose rules

| Rule | Why |
|---|---|
| `down` passes **every** profile in the spec | Compose treats a non-enabled profile's services as absent, so a narrower `down` leaves that profile's resources — visibly one dead `<stack>_default` network per PR, forever. Passing a profile with no matching service is a no-op. |
| `down -v` never removes **images** | Compose's job ends at containers, volumes and networks. Per-PR images accumulate until the disk fills — and disk pressure evicts the warm build cache. Model images as an axis. |
