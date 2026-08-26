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
| **give my team accounts that cannot do everything** | [7e. The four roles](#7e-who-can-do-what-the-four-roles-0320) |
| deploy from GitHub Actions | [8. Wire it into CI](#8-wire-it-into-ci) |
| a nightly leak sweep, orphan hunting | [9. Day-2 operations](#9-day-2-operations) |
| every flag, route, env var, exit code | [10. Reference](#10-reference) |

---

## 1. Install & first run

`pstack` ships as **one static binary** (Go, `CGO_ENABLED=0`) for Linux and macOS, amd64 and
arm64, on [GitHub Releases](https://github.com/samishal1998/preview-stacks/releases). Nothing else
is installed: no runtime, no package manager, no dependencies. `docker` with the Compose v2 plugin
must be on the PATH of whatever box runs `up` / `down` / `init`, but not to `validate` or dry-run
a spec.

```bash
curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh | sh
pstack --help
```

The installer downloads `pstack_<os>_<arch>`, verifies it against the release's `checksums.txt`,
and moves it atomically into `/usr/local/bin` (override with `PSTACK_INSTALL_DIR`; pin with
`PSTACK_VERSION=0.29.0`). It never edits your PATH and never uses sudo — run it as a user that
can write the target directory. Alternatives: download the binary from the release page by hand,
or `go install github.com/samishal1998/preview-stacks/packages/pstack/cmd/pstack@latest`.

```console
$ which pstack
/usr/local/bin/pstack

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

One file, ~17 MB, statically linked. The web UI, the share page, the control-stack compose
template and the cloud-init template are **embedded** (`//go:embed`), so nothing is read from a
path relative to a source tree at runtime — the whole class of bug where a tool works from a
checkout and 404s once installed cannot occur. The version is read from the embedded
`package.json`, which is the lockstep version of record for the three packages in this repo.

Until 0.28.0 pstack was a Bun/TypeScript package on npm; `@samyx/preview-stacks` is deprecated
there and stops at that release. The one-time move for an existing host is in
[Upgrading a host](#upgrading-a-host). The two npm packages that remain are the advanced UI
(`@samyx/preview-stacks-ui`, fetched *inside* the image build) and the client SDK
(`@samyx/preview-stacks-client`).

### Working from a checkout (contributors)

```console
$ git clone <this-repo> && cd preview-stacks
$ go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
$ packages/pstack/bin/pstack --help
$ go test -race -timeout 120s ./...          # the Go suite, every package
$ bun install && bun run check               # everything: Go, the conformance suite, the UI, the client
```

`go run ./packages/pstack/cmd/pstack <command>` is equivalent to `pstack <command>` — the
**contributor** path, not the user path. The conformance suite (`packages/conformance`) is the
black-box specification: it spawns the built binary and compares every CLI transcript and API
response against checked-in goldens, and it is what a change to the contract is reviewed against.

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

The token gates **every route, reads included** — since 0.10.0 the only things you can reach without
a credential are `/api/health` and the login page. A browser gets in with a session cookie instead,
which is also what lets the log stream use `EventSource` (it cannot send headers).

The token itself is **`root`** and passes everything. Accounts are weaker than it and differ from
each other: each carries a **role**, and what each role may reach is
[7e](#7e-who-can-do-what-the-four-roles-0320).

It is still **not multi-tenant** — one spec set, one Docker socket, one host. Roles narrow what a
colleague can do to that host; they are not an isolation boundary and do not pretend to be. Put the
API behind your ingress' auth or an SSH tunnel (`ssh -L 7878:127.0.0.1:7878 preview-host`) before
anyone but you can reach it.

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

`state` is the field to branch on. There are **seven**, and only the first two are not final:

| State | Final | Means |
|---|---|---|
| `queued` | no | accepted, not started — waiting for its own stack's running job, or for a slot under `max_jobs` ([the queue](#one-job-per-stack-and-a-queue-of-one)) |
| `running` | no | executing now |
| `ok` | yes | every step succeeded |
| `failed` | yes | a step failed |
| `cancelled` | yes | a person stopped it mid-run — partial state may exist, and the transcript says so |
| `leaked` | yes | teardown ran and something survived it. The one to page on |
| `superseded` | yes | a newer request replaced it **before it ever ran**. `startedAt` is `null`, so there is nothing to check |

`leaked` is its own state for the same reason exit 2 is its own code. `cancelled` and `superseded`
are deliberately not the same state: a cancelled job may have left half a deploy behind, a
superseded one did nothing at all.

**A poller must stop on all five final states, not on `ok`/`failed` alone.** `queued` and
`superseded` both arrived in 0.32.0 — a loop written against the older five treats `queued` as
unrecognised and waits forever on a `superseded` job that is already finished with.

### Use it from a script

There is a client package, so a CI job does not have to hand-roll `fetch` calls and a job-polling
loop:

```ts
import { createClient } from '@samyx/preview-stacks-client';

const pstack = createClient({ baseUrl: process.env.PSTACK_API!, token: process.env.PSTACK_TOKEN });
const job = await pstack.deployments.up(`pr-${pr}`, { PR: pr });
const done = await pstack.waitForJob(job.id);          // returns the finished job, failure included
const ready = await pstack.waitForReady(`pr-${pr}`, { vars: { PR: pr } });
```

Zero dependencies, and it ships `verifyWebhook` for the receiving end.
See [packages/client/README.md](../packages/client/README.md).

### `pstack api` — every route as a command (0.34.0)

The CLI carries the whole API. Sixty-nine commands, **generated** from
[`packages/pstack/api/openapi.yaml`](../packages/pstack/api/openapi.yaml), so they cannot describe a
route the server does not serve:

```bash
export PSTACK_API_URL=https://api.preview.example.com
export PSTACK_TOKEN=…                       # the host's, or one from `pstack api tokens create`

pstack api --help                           # the groups
pstack api deployments --help               # the commands in one
pstack api deployments list
pstack api deployments up --id pr-123
pstack api jobs get --job-id up-pr-123-1-apeq0d
pstack api settings set-max-jobs --value 8
```

Parameters are flags, typed and validated from the schema: `--value` is an integer, `--action` is
one of `start|stop|restart`, and a missing required flag is refused before anything is sent. Every
command takes `--json` for the raw response, and a **non-2xx is a non-zero exit**, so
`pstack api … || rollback` works.

A **request body** is `--data '<json>'`. When a body is *flat* — every field a scalar — each field is
also its own flag, which is why `settings set-max-jobs --value 8` works but
`deployments put` takes `--data '{"spec":"…"}'`: its body carries `env`, a map, and there is no sane
flag shape for one.

`PSTACK_API_URL` has **no default**, the same refusal `pull config` makes: a guess would talk to the
wrong host. `pstack api --help` needs neither variable — asking what the commands are does not
depend on having a host.

**Three routes are deliberately absent**: the two SSE streams and the WebSocket terminal. A command
runs one request and prints the answer, so each would buffer an endless response. Use
`curl -N` for the streams, and the UI for the terminal.

### Stop a running job

```console
$ curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
       https://api.preview.example.com/api/jobs/up-pr-123-1-apeq0d/cancel
{
  "cancelled": "up-pr-123-1-apeq0d",
  "stack": "pr-123",
  "action": "up",
  "by": "alice",
  "warning": "Nothing was undone. Whatever this job created or destroyed before it stopped is still that way — run verify to see what exists."
}
```

The command in flight is killed (SIGTERM) and every later hook is refused — a teardown is
best-effort and keeps going past failures, so without that second half pressing stop would leave the
remaining axes running. **Nothing is rolled back.** Run `verify` afterwards; that is the whole point
of having it. A job that has already finished answers 409, not 404: the id is fine, the request is
out of date.

The SSE stream replays the buffered log from the beginning, then streams live, then closes with a
terminal frame — so attaching late still gets the whole story:

```console
$ curl -sN https://api.preview.example.com/api/jobs/down-pr-123-3-a3zn01/stream
data: {"seq":1,"at":1785014283186,"level":"step","message":"→ compose down (all profiles)"}

data: {"seq":2,"at":1785014283193,"level":"step","message":"→ down: database"}

data: {"seq":3,"at":1785014283206,"level":"step","message":"→ verify (asserting resources are gone)"}

data: {"done":true,"state":"ok"}
```

### Stop, start or restart one container

Not the stack and not the service — one container out of a replica set:

```console
$ curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
       'https://api.preview.example.com/api/deployments/pr-123/containers/pr-123-worker-1/restart?PR=123'
{
  "container": "pr-123-worker-1",
  "service": "worker",
  "action": "restart",
  "by": "alice",
  "note": "Docker has started it. Whether it comes back healthy is what readiness reports."
}
```

`start` · `stop` · `restart`. Synchronous, unlike a deploy: one `docker restart` takes seconds, and
putting it behind the per-stack job lock would make it collide with a deploy for no benefit.
`?grace=<seconds>` (1–120, default 10) is how long docker waits before SIGKILL on a stop or restart.

**The container name is checked against what this deployment owns**, and anything else is a 404 — the
same boundary the terminal has, for a bigger reason: `docker stop traefik` would take down every
preview on the host, and `pstack-control` is the thing being asked.

A **stop cancels the readiness watch** (a watch left running would report the stack failed about a
container you meant to stop); a start or restart (re)starts it, so whether it came back healthy is
answered without asking. Each verb also emits its own event — `container.started` / `stopped` /
`restarted`, carrying `by`.

### Wait for it to actually be serving

A green `up` means the commands ran: `compose up -d` returns as soon as the containers are
*created*. It returns exactly the same way when the app boots, throws, and exits two seconds later.
So a CI step that posts the preview URL right after the job is racing the thing it is advertising.

`readiness` is the second half. A watch starts on every successful `up`; poll it, or hand it a
`wait` and let it answer when there is an answer:

```console
$ curl -s -H "Authorization: Bearer $PSTACK_TOKEN" \
       'https://api.preview.example.com/api/deployments/pr-123/readiness?wait=60&PR=123'
{
  "id": "pr-123",
  "stack": "pr-123",
  "state": "ready",
  "containers": [
    { "name": "pr-123-app-1", "service": "app", "state": "running",
      "health": "healthy", "hasHealthcheck": true, "exitCode": 0, "restartCount": 0,
      "ready": true, "failed": false },
    { "name": "pr-123-migrate-1", "service": "migrate", "state": "exited",
      "health": null, "hasHealthcheck": false, "exitCode": 0, "restartCount": 0,
      "ready": true, "failed": false }
  ],
  "startedAt": 1785014256010,
  "endedAt": 1785014271044,
  "reachable": true,
  "timeoutMs": 180000
}
```

`state` is **`watching` · `ready` · `failed` · `timedout`**. `?wait=<seconds>` (max 60) returns the
moment it leaves `watching`, or hands back `watching` when the wait expires — so the CI loop is
"re-issue while `watching`", with no edge to miss:

```bash
until [ "$(curl -sf -H "Authorization: Bearer $PSTACK_TOKEN" \
    "$API/api/deployments/pr-$PR/readiness?wait=60&PR=$PR" | jq -r .state)" != watching ]; do :; done
```

Two things it is careful about. A container **without** a healthcheck is "ready" when it is merely
running — nothing here knows what serving means for that image, so `hasHealthcheck: false` says so
rather than implying a probe passed. And a container that **exited 0** is ready, not failed: one-shot
migrations are supposed to finish. `failed` is an exit with a non-zero code, a crash loop (3+
restarts — with a `restart:` policy no single sample of `state` can tell a loop from a slow start,
but the counter can), a dead container, or an `unhealthy` healthcheck; `reason` names which.

Reading this endpoint for a stack that has no watch starts one, and that watch is **silent** — a
page view must not put "did not become ready in time" in a notifier about a deploy nobody ran. Only
a watch started by an actual `up` emits events.

Add `?refresh=1` to re-run a watch that already settled, `?timeout=<seconds>` to override the
deadline (default 180s, or the host's `PSTACK_READINESS_TIMEOUT_MS`; floored at 5s — an unusable
value defers to the host default rather than silently shortening the watch). The same convergence is pushed to notifiers as `healthcheck.*`, `container.*`
and `stack.*` events — see [webhook-events.md](webhook-events.md).

### Probe a preview without a token (0.34.0)

A CI job that polls `https://app-pr-123.example.com` right after a deploy is often not waiting on
the app at all — under HTTP-01 it is waiting on **that hostname's certificate**, which Let's Encrypt
issues on the first HTTPS request and which every new stack needs separately. The probe asks the
same question on a hostname whose certificate has been warm since `init` ran:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://api.preview.example.com/api/probe/pr-123
# → 200
```

**No token.** That is the point: the thing polling is usually a shell loop in a pipeline that has no
business holding a credential which can start privileged containers.

The status is the **container's own**, so a 404 means the app answered and does not serve `/` —
which is still "it is serving". `x-pstack-probe` says what kind of answer it is:

| Header | Status | Means |
|---|---|---|
| `upstream` | the app's | the container answered |
| `unknown` | 404 | no such deployment |
| `asleep` | 503 | sleeping — **and left that way**; the probe never wakes anything |
| `no-target` | 503 | nothing to dial: no router, no port, or not on `preview-ingress` |
| `unresolved` | 503 | the spec needs request variables, which this route does not take |
| `unreachable` | 502 | the dial failed or took longer than 3s |
| `busy` | 503 | too many probes in flight (4 at once) |

`?service=web` picks one when a stack publishes several; without it the alphabetically first router
with an address wins, the same one every time.

**What it deliberately cannot do**, because it has no auth: return a body — not the app's, not an
error message, ever; forward the upstream's headers; follow a redirect; or fetch any path but `/`.
It does disclose whether a deployment id exists and whether it is up, which is the question being
asked. `PSTACK_PROBE=off` removes the route on a host that does not want even that.

### One job per stack, and a queue of one

A stack still runs exactly one job at a time — a `down` deleting the database branch an `up` just
created is the kind of corruption that is very hard to diagnose afterwards. What changed is what
happens to the SECOND request: it is queued rather than refused.

```console
$ curl -s -w " [%{http_code}]\n" -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
       'https://api.preview.example.com/api/deployments/pr-777/up?PR=777'
{
  "job": { "id": "up-pr-777-8-a1b2c3", "state": "queued", "stack": "pr-777", … }
} [202]
```

**The queue is one deep, and the newest wins.** A third request replaces the queued one rather than
stacking behind it, so five pushes in a minute run the first deploy and then exactly one more
carrying the newest spec. The replaced job is not forgotten: it reaches `superseded` under its own
id, so a script polling the id it was given always gets an answer.

**`down` does not wait.** A teardown cancels whatever is running, drops anything queued, and starts
immediately — the one thing you never want stuck behind a deploy. Cancelling mid-flight leaves
partial state, and the job's transcript says so.

`POST /api/deployments/:id/cancel` stops everything for one stack: the running job and the queued
one together.

Across stacks, **the `max_jobs` setting (default 4) bounds how many run at once**. Beyond it a job
waits for a slot rather than being refused — twenty PRs deploying at nine in the morning would
otherwise put twenty `docker compose up` on one Docker socket.

That cap is changeable **while the server runs** — `PUT /api/settings/max_jobs` (maintainer), no
restart — and `PSTACK_MAX_JOBS` is now its *default* rather than the authority
([Runtime settings](#runtime-settings-0330)). Raising it dispatches whatever was already waiting, on
that request. **Lowering it cancels nothing**: jobs already running run to completion, and the new
cap applies to the next job that starts. An operator who types `1` while four jobs are in flight has
not stopped three of them.

Jobs are **in-memory, capped at 50** — a restart loses history, not correctness, and it loses the
queue too. Truth about what exists lives in Docker and in each axis's `assert_*` probe.

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

### Copy a variable list out, paste one back

Three places in the **advanced UI** hold a list of `NAME` / `value` pairs: the **Variables & secrets**
page (host-level `${vars.NAME}` and `${secrets.NAME}`), the submit form, and a deployment's **Config**
tab. All of them carry the same two controls — copy the list out, paste one back — in `.env`, CSV or
TSV.

- **Copying** renders the list in the format you pick. `.env` quotes only what needs it (a space, a
  `#`, a quote, a newline); CSV and TSV always write a `name,value` header row, so a variable really
  called `name` survives the round trip rather than being eaten as a label.
- **Pasting** parses as you type and shows what it read **before** any button that changes anything
  appears: the pairs, and every line it could not read, with the line number, the text and the
  reason. A paste that half-worked in silence is how a deployment loses the one variable its stack
  name is built from. The format is detected — `.env` shape first, then a quote-aware split on comma
  and tab — and there is a manual override for the input that reads as the wrong one.
- **Merge** adds and overwrites by name. **Replace all** is offered only in the deployment's variable
  editor, which owns its whole list; on the Variables page a replace would have to *delete* every
  name the paste happened to omit, and that is not a thing to put behind a two-word button.
- A name a spec cannot reference comes back as a problem instead of a row: `foo-bar` is a legal
  thing to type into the editor, but `${foo-bar}` is not something the interpolator can resolve, so
  importing it would add a variable that could never do anything.

**What an export does with a secret: it writes the name, an empty value, and never the mask.**

```
# Secret VALUES are not included: this server has no route that returns one.
# Each name is listed with an empty value for you to fill in.
DB_PASSWORD=
STRIPE_KEY=
```

There is no other honest option. A secret's value has no read path at all — `GET /api/host-vars`
returns names and timestamps — so there is nothing to copy. Omitting the names would make the file
quietly incomplete wherever it lands. And writing `••••••••` would produce a file that *deploys*,
with the mask as the password. An empty value is not a value: pstack treats an empty variable as
**undefined** and refuses to resolve a spec that needs it, so a pasted secret line that was never
filled in fails loudly at deploy time instead of running against a wrong credential.

That leaves one loop, and it is closed on the way back in: on the Variables page an entry with **no
value is skipped**. Copy this page's secrets, paste them straight back, and nothing changes — the
preview counts them, names them, and refuses to apply if that is all there was. CSV and TSV have
nowhere to put a comment, so the warning about withheld values is on the page itself and not only
inside the copied text.

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
| `--orchestrator swarm\|compose` | `PSTACK_ORCHESTRATOR` | `swarm` | how previews deploy. `swarm` makes this daemon a one-node manager (overlay networks, the swarm provider in Traefik); `compose` is what every host before 0.26.0 ran. `upgrade` keeps whatever the host has — see [Swarm mode](#swarm-mode) |
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
| 1b. Swarm | under `--orchestrator swarm`: `docker swarm init` if this daemon is not already a manager. Never leaves a swarm |
| 2. Networks | `preview-ingress` and `preview-shared`, created idempotently — `bridge` under compose, `overlay --attachable` under swarm. A network that exists with the other driver is swapped only when nothing but the control stack is on it; a preview still attached is a hard stop naming it. **Both must be declared `external: true` in every per-PR compose file** — declare one non-external and Compose silently makes `pr-123_preview-ingress` instead, so the container comes up healthy and unreachable |
| 3. Config | `control/docker-compose.yml` (the template, with the challenge, UI, swarm-provider and wake-router blocks rendered), `control/.env` (`0600`, holds `PSTACK_TOKEN` and `PSTACK_ORCHESTRATOR`), `control/dns.env` (`0600`, holds the DNS credential; written either way so switching modes needs no extra step). Traefik gains a metrics entrypoint on `:8082` (unpublished; the API reads it for `sleep.idle`) and the pstack container the catch-all `pstack-wake` router |
| 4. Up | `docker compose -p pstack-control -f <path> up -d --remove-orphans` |
| 5. **Prove it** | polls the container's `HEALTHCHECK` for ~60s. `up -d` exits 0 as soon as containers are *created*, so a crash-looping API would otherwise be reported as success |
| 6. Next steps | the URLs, the rotation recipe, and the generated `PSTACK_TOKEN` — **the only time it is printed** |

It is **idempotent**: re-running it is the supported way to change the domain, rotate `PSTACK_TOKEN`,
switch challenge mode, or move to a new image. (`chmod` is re-applied unconditionally, so a `.env`
someone loosened to `0644` is tightened back on the next run.)

```bash
PSTACK_TOKEN=<new> pstack init --domain preview.example.com --acme-email <you>@example.com
```

### Upgrading a host

```console
$ ssh preview-host
$ pstack upgrade                 # to the latest published version
$ pstack upgrade --to 0.25.1     # or an exact one
$ pstack upgrade -n              # print the plan, change nothing
```

`pstack upgrade` runs that release's installer (checksum-verified, into the directory the running
binary lives in), then re-executes itself as the new version for the rebuild and the re-init.

#### ⚠️ The one-time move from the Bun runtime (≤ 0.28.0 → 0.29.0)

Until 0.28.0 pstack was an npm package running on Bun; from 0.29.0 it is one static Go binary on
GitHub Releases, and `bun install -g` cannot install it. On a host provisioned before 0.29.0:

```console
$ pstack upgrade                         # first, to 0.28.0 if not there yet (the bridge release)
$ curl -fsSL https://github.com/samishal1998/preview-stacks/releases/download/v0.29.0/install.sh | sh \
    && pstack upgrade --resume           # installs the binary, then phase 2: rebuild + re-init, token intact
```

0.28.0's `pstack upgrade --to 0.29.0` prints exactly that line and exits 3, so there is no way to
miss it. Everything on disk is read unchanged — the registry, the SQLite database (every password
hash keeps verifying), `control/.env` and `control/dns.env` — and nothing rotates. Things that DO
change, and are worth a glance before you run it:

- The control container no longer carries `bun`, `bunx` or a JavaScript runtime. **Axis hooks run
  inside that container**, so a hook that called `bun`/`npx`/`node` must bring its own: grep your
  specs (`grep -E '\b(bun|bunx|node|npm|npx)\b' deployments/*/spec.yml`) before the hop. `bash`,
  `curl`, `docker` and `docker compose` are there as before.
- `build-image` no longer fetches from npm: the generated Dockerfile runs the release's own
  `install.sh`, pinned to this version, in an empty context. It therefore needs the release to be
  reachable at build time; `PSTACK_BINARY=<path>` copies a local binary in instead. The previous
  image is kept as `pstack:local-previous` first, every time.
- Rendered cloud-configs from ≤ 0.28.0 install the old runtime; re-render with `pstack cloud-init`
  before provisioning a new host.

Rollback, should the new image come up unhealthy:

```console
$ docker tag pstack:local-previous pstack:local
$ docker compose -p pstack-control -f /var/lib/pstack/control/docker-compose.yml up -d
```

**The first hop has to be by hand on very old hosts**, because a host cannot run a command it does
not have yet: on a box older than 0.25.1, `pstack upgrade` is simply not installed. Install the
binary with the line above, then use the command from then on.

#### If an upgrade dropped your advanced UI

0.25.1 and 0.25.2 detected the UI mode by looking for a compose service named `pstack-ui` — a name
that only ever appears as a Traefik *router*, so **every host read as `basic`** and the upgrade
removed the advanced UI container it was meant to preserve. Fixed in 0.25.3, which reads the real
service name (`advanced-ui`). To put a flipped host back:

```console
$ pstack ui advanced
```

That switches which UI `control.<domain>` serves, reusing the stored token and domain, and building
the SPA image on the way in. It does not touch the version — that is `upgrade` — and it recreates
nothing when the host is already in the mode you asked for. `pstack ui basic` goes back; it builds
nothing, since the API already carries the embedded UI. `pstack upgrade --ui advanced` does the same
override as part of an upgrade.

It reads `<DATA_DIR>/control/.env` for the **existing token, domain and ACME email**, and reads the
generated `control/docker-compose.yml` for the **challenge mode and whether the advanced UI is
running** — then installs the new version, rebuilds the image (or both images), and re-runs `init`
with exactly those settings.

Reading them back is the point. The upgrade was always three commands, and the third had a trap:
`init` takes `PSTACK_TOKEN` from the environment and **mints a fresh one when it is absent**, so a
hand-typed `pstack init --domain … --acme-email …` silently rotates the machine token and every CI
job starts getting 401s — from a command whose purpose was "change nothing but the version".

Two things worth knowing:

- **It runs in two phases**, and the second is a real new process (`pstack upgrade --resume`, which
  you can also run by hand). `build-image` pins the image to the *running* CLI's version, so a single
  process would install the new version and then faithfully build an image of the old one — an
  upgrade that reports success and moves nothing.
- **It is CLI-only**, like `init`, and for the same reason: the API is a container inside the stack
  being recreated, so a self-upgrade kills the process mid-request and a broken image leaves the host
  with no control plane and no remote way to repair it.

Your data survives — deployments and the SQLite database are host-path mounts under `DATA_DIR`, not
container filesystem. In-flight **job history does not**: it is in memory by design, so do not upgrade
mid-deploy.

### Why `init` is CLI-only, and always will be

`init` — and `upgrade` — are CLI-only and will never be HTTP routes,
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

**Under swarm, the PUT tells you what the conversion will change** (0.30.0). The response carries
`swarmNotes`, produced by the same conversion the deploy runs, so a submission and the job log can
never name different keys:

```json
{
  "id": "pr-123", "kind": "isolated", "stack": "pr-123",
  "swarmNotes": [
    "swarm: service api: depends_on: dropped — swarm has no start-order dependency; wait in the container's own entrypoint",
    "swarm: service api: restart: always → deploy.restart_policy.condition: any"
  ]
}
```

They name what **you** wrote, not the routing labels pstack generates, because those are the ones you
can act on — so `up`'s own list may be longer. Absent, never `[]`, when nothing was checked: under
compose there is no conversion, and a compose file that does not parse is reported far better by
`up` than by a guess here. They are advisory — a submission is never rejected for them.

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

There is no importable library: the binary is the product, and the programmatic surface is the
HTTP API — `@samyx/preview-stacks-client` from TypeScript, or any HTTP client. Ids must match
`/^[a-z0-9][a-z0-9._-]{0,63}$/` — they become directory names and reach shell hooks, so no
traversal, no spaces, no metacharacters.

For CI, prefer the CLI with `-f` and `--set` (section 8): it needs no host access and no token.

---

## 7b. Scale out, sleep, and share (0.26.0)

Three things a preview host grows into once it has more than a handful of PRs on it: a second
machine, previews that cost nothing while nobody looks at them, and a way to show one person one
stack's logs without making them an account.

### The two orchestrators, and how to choose

Previews deploy one of two ways. It is a **host-level** decision with a **per-deployment** escape
hatch, and nothing about the spec you write changes between them.

| | `swarm` (the default since 0.26.0) | `compose` |
|---|---|---|
| Deploys with | `docker stack deploy`, converting the compose file on every deploy | `docker compose up -d`, byte for byte what you submitted |
| Networks | `overlay --attachable` | `bridge` |
| More than one machine | **yes** — that is the whole point | no, one box |
| `mem_limit`, `privileged`, `devices`, `depends_on`, `profiles` | dropped or converted, and named in the notes | work as written |
| `docker exec`, container start/stop, the terminal | manager-local; a task on a worker is listed and reachable for logs, not for a shell | always available |
| Volumes on teardown | removed on the **manager**; one a task created on a worker stays there | removed |

**Choose `compose` when the host will only ever be one machine and a service needs something swarm
cannot express.** Choose `swarm` — or just take the default — if a second machine is plausible: it
costs nothing on one node, and adding a worker later is one command rather than a rebuild.

Three places set it, most specific first:

```yaml
compose:
  file: docker-compose.yml
  orchestrator: compose      # 1. this deployment only
```

2. `PSTACK_ORCHESTRATOR` on the server — what `pstack init --orchestrator` writes into
   `control/.env`, and the host default every deployment inherits.
3. Neither: `compose`. A spec run straight from the CLI on a laptop is not secretly a swarm.

**Switching an existing host** is `pstack init --orchestrator <the other one>` on the host — the
same idempotent command that stood it up. One precondition, and it is not a soft one: the two
networks change driver (`bridge` ↔ `overlay`), and a driver swap is only possible when nothing but
the control stack is attached. **Tear every preview down first.** A preview still attached is a hard
stop that names the network rather than a half-migrated host. `pstack upgrade` never changes the
mode — it keeps whatever the host already has.

`GET /api/swarm` answers which mode is live: `active: false` means previews run with compose, and
`reachable: false` means docker did not answer at all — which is not the same as "no nodes".

### Swarm mode

A new host stood up with `pstack init` is a **one-node Docker Swarm manager**, and previews deploy as
swarm stacks. You will not notice until you need a second box — then it is one command on the new
machine (below) and previews start landing on it.

**You write plain compose.** `docker stack deploy` reads the compose v3 schema strictly — `mem_limit`,
`cpus`, `profiles` and `pull_policy` are hard errors, `restart:` is silently ignored and swarm's own
default (`any`) would loop a one-shot migration container forever. pstack converts the submitted file
on **every** deploy and names what it changed in the job log (`swarm: service app: restart: always →
deploy.restart_policy.condition: any`). The file in the registry is never modified.

| In the file you submit | In the stack that deploys |
|---|---|
| `profiles:` | services behind a profile the spec does not select are left out; `--prune` removes a service that was in last time (the `--remove-orphans` equivalent) |
| `restart: always` / `unless-stopped` | `deploy.restart_policy.condition: any` |
| `restart: on-failure[:N]` | `condition: on-failure` (+ `max_attempts: N`) |
| no `restart:` / `restart: no` | `condition: none` — what compose does; not swarm's default |
| `mem_limit`, `cpus`, `pids_limit`, `mem_reservation` | `deploy.resources.limits.memory` / `.cpus` / `.pids`, `reservations.memory` |
| `labels: traefik.*` | moved to `deploy.labels` (the swarm provider reads **service** labels); `traefik.docker.network` → `traefik.swarm.network` |
| `build`, `container_name`, `privileged`, `devices`, `network_mode`, `depends_on` | dropped, with a note saying why and what to do instead |
| any key outside the v3 schema (`pull_policy`, `platform`, `extends`, …) | dropped, with a note |
| top-level `name:`, `version: "2.x"` | dropped |
| a `deploy:` block you wrote yourself | kept; only missing keys are inferred |

A service that genuinely cannot run under swarm (`privileged`, a device mount) can stay on compose on
the manager: set `compose.orchestrator: compose` in that spec. The host default comes from
`PSTACK_ORCHESTRATOR`, which `init` writes into the control stack.

```yaml
compose:
  file: docker-compose.yml
  orchestrator: compose        # this one deployment only; everything else follows the host
```

Three ceilings, stated rather than hidden:

- `docker stack rm` never removes volumes. Teardown removes the stack's labelled volumes on the
  **manager**; a volume a task created on a worker stays there. Use named volumes only for data you
  can lose, and an axis (with its `assert_gone`) for data you cannot.
- `docker exec`, `stop`, `start` are node-local. A task on a worker is listed on the Containers tab
  (with its node, marked *remote*), logs reach it through the manager, but the terminal and the
  per-container buttons refuse it with a 409 that names the node.
- A relative bind mount (`./data:/data`) resolves against the compose file's directory **on each
  node**. It works on the manager and fails on a worker that has no such directory.

**Adding a worker.** Open the ports first, between the new machine and every other node:

| Port | For |
|---|---|
| `2377/tcp` | cluster management (worker → manager) |
| `7946/tcp+udp` | node discovery (every node ↔ every node) |
| `4789/udp` | overlay network traffic, VXLAN (every node ↔ every node) |

Then take the join material — from the host with `pstack swarm join`, from the **Swarm** page (Add a
worker → Reveal), or from the API — in whichever shape the new machine wants.

On the host, over SSH:

```bash
pstack swarm                       # the node table, the manager address, the ports
pstack swarm join                  # → docker swarm join --token SWMTKN-1-… 203.0.113.10:2377
pstack swarm join --format script > join.sh
pstack swarm join --format cloud-config --distro debian -o worker.yaml
```

`pstack swarm` exits **1** when this host is not a manager (or docker did not answer), so a script
can ask without parsing the table. The join material goes to **stdout** and everything else to
stderr, so `pstack swarm join --format token` pipes cleanly.

Or over the API, from anywhere:

```bash
# one line, for a machine that already runs Docker
curl -s https://api.preview.example.com/api/swarm/join?format=command -H "Authorization: Bearer $PSTACK_TOKEN"
# → docker swarm join --token SWMTKN-1-… 203.0.113.10:2377

# a script that installs Docker (get.docker.com) if missing, then joins
curl -s "…/api/swarm/join?format=script" -H "Authorization: Bearer $PSTACK_TOKEN" > join.sh

# cloud-init user-data for a fresh machine: Docker from the distro's repositories, then join
curl -s "…/api/swarm/join?format=cloud-config&distro=debian" -H "Authorization: Bearer $PSTACK_TOKEN" > worker.yaml
hcloud server create --name worker-1 --image debian-12 --user-data-from-file worker.yaml
```

The token is a **secret**: whoever holds it can add a node that runs any task on the cluster. The
route is admin-only, the Swarm page fetches it only when you click Reveal and forgets it when you
leave, and `docker swarm join-token --rotate worker` on the manager invalidates it. `GET /api/swarm`
(the polled view) never carries it.

```bash
curl -s https://api.preview.example.com/api/swarm -H "Authorization: Bearer $PSTACK_TOKEN"
```
```json
{ "reachable": true, "active": true, "nodeId": "n1…", "managerAddr": "203.0.113.10:2377",
  "nodes": [ { "id": "n1…", "hostname": "preview-host", "role": "manager", "status": "ready",
               "availability": "active", "managerStatus": "leader", "engineVersion": "28.0.1", "self": true } ],
  "ports": [ … ], "note": "…" }
```

`reachable: false` means docker did not answer — nothing is known, which is not "no nodes".
`active: false` means the host runs previews with compose; `pstack init --orchestrator swarm` (on
the host, with every preview torn down first — the networks have to be recreated) switches it.

### Sleep and wake-on-call

Most previews are deployed to be looked at once, then sit there until the PR merges. **Sleep** takes
the compose project down and **keeps its volumes and every axis** — the database branch, the seeded
data, the images. The next request to any of its hostnames brings it back: the visitor sees *"Your
preview is spinning up…"* for the length of a deploy, and then the app.

Declare it in the spec:

```yaml
sleep:
  idle: 2h      # no request reached any of its routers for 2 hours
  after: 3d     # 3 days after the last deploy, whether anyone looked or not
```

Either, both, or neither. Durations are `90s`, `30m`, `2h`, `3d`, `1h30m`. A spec without a `sleep:`
block is never put to sleep by the scheduler.

- **`idle`** reads Traefik's per-router request counters (a metrics entrypoint the control stack
  exposes to the API container only — nothing in the request path). The idle clock starts at the
  later of the last deploy and the API's own start: a restarted control plane forgets when a stack
  was last visited, so the cost of a restart is one extra `idle` period awake, never an early sleep.
  If the metrics endpoint cannot be read (a control stack from before 0.26.0), `idle` never triggers
  — the server logs that once — and `after` still works.
- **`after`** reads the newest container's start time from docker. No clock to lose.
- Never for `kind: shared` (every preview that depends on it would go with it), never while a job is
  in flight, never twice.

What sleep runs is `docker compose down --remove-orphans` — **without `-v`** — or `docker stack rm`
under swarm, which never touches volumes. That is the whole difference from tearing down. Waking is
`up`, exactly: axis `up` hooks are idempotent by contract and re-capture their outputs, so nothing has
to be remembered between the two. Readiness watches the wake like any deploy.

By hand, the same two verbs:

```bash
curl -s -X POST https://api.preview.example.com/api/deployments/pr-123/sleep -H "Authorization: Bearer $PSTACK_TOKEN"
# → 202 { "job": { "id": "sleep-pr-123-…", "action": "sleep", … } }
curl -s -X POST https://api.preview.example.com/api/deployments/pr-123/wake  -H "Authorization: Bearer $PSTACK_TOKEN"
# → 202 { "job": { "action": "wake", … } }
```

`GET /api/deployments/:id` carries the policy and the state, separately — `sleep` is what the spec
asks for, `asleep` is the record written when it happened:

```json
"orchestrator": "swarm",
"sleep":  { "idle": "2h", "after": "3d" },
"asleep": { "since": 1787000000000, "reason": "idle 2h",
            "hosts": ["app-pr-123.preview.example.com"], "rules": [] }
```

`hosts` and `rules` are what the catch-all router recognises as this deployment's: they are captured
from its live Traefik labels the moment before teardown, so a router you wrote by hand is recognised
exactly like a generated one, and a wildcard subdomain (`*.app-pr-123.…`) still wakes it. A deploy
or a teardown clears the record; replacing the spec keeps it. Clearing the record does **not** hand
the hostname back — read the next paragraph before assuming it does.

How a request finds its way back: `init` renders a **catch-all router** on the pstack container
(priority 1, `HostRegexp` over the whole `*.<domain>`). While a preview's containers run, its own
router wins; when they do not, the request lands on the API with the original `Host`. If that
hostname is one this control plane is still holding, **every** stage below answers `503` with
`Retry-After: 5` and an `x-pstack-wake: 1` header — so a script sees "not yet" rather than a
misleading 200 — and the page polls itself, reloading the moment that header stops coming.

What the visitor reads changes with the stage, and there are three:

| Stage | The page says | It ends when |
|---|---|---|
| **asleep** | *"`pr-123` was asleep. It is being brought back now"* | `up` returns — the containers now exist |
| **starting** | *"`pr-123` is awake and its containers are starting — it has not answered a request yet"* | readiness settles, either way |
| **failed** | *"Your preview could not start"*, quoting the reason (`app: exited with code 1`) | not on its own |

**The hostname stays this control plane's for all three, not just the first.** The sleep record is
cleared the moment `up` reports success — which is when the containers are *created*, not when the
app answers — and a control plane that let go of the hostname there had nothing left mapping it to a
deployment, so the request fell through to "any non-`/api/` path is the UI" and the visitor got the
**pstack dashboard on their preview's own URL**. That is why `starting` is its own stage and not a
longer `asleep`: it is the true sentence, and it keeps the hostname.

What ends `starting` is the **readiness watch** ([§6](#wait-for-it-to-actually-be-serving)), not a
timer of its own — `ready` hands the hostname back to Traefik, `failed` or `timedout` turns the page
into the failure row. So the spinner's bound is readiness's own deadline
(`PSTACK_READINESS_TIMEOUT_MS`, 180 s), and what replaces it names the failure and then **keeps**
answering: a preview that cannot start must say so on every reload, not fall through to the
dashboard on the second one.

Two failures, and they retry differently, because one of them is not really a failure of the wake:

- **The wake job failed** — an image that no longer pulls, an axis hook that threw. The stack is
  still asleep, the page quotes the failing step, and a reload retries it after a minute.
- **The wake job succeeded and the containers died.** `docker compose up` returns when containers
  are *created*, so this is an `ok` job and a crashed app. The page quotes the container's own
  reason, and reloading does **not** try again — there is no sleep record left for a request to act
  on. `POST …/wake`, or a deploy, is what tries again.

Two things this page cannot do, and both are worth knowing before you go looking:

- **It cannot replace the `502`.** Once Traefik has the container's route, the request reaches the
  container and never reaches pstack at all. The window this page covers is the one *before* that.
- **A script cannot tell the stages apart from the headers.** All three are `503` +
  `x-pstack-wake: 1`; only the body differs. Poll for the header to disappear, which is what the
  page's own script does.

Under HTTP-01, Traefik serves the certificate it already issued for the hostname while the stack
was live. Under DNS-01 the wildcard covers it. A hostname that was never live has no certificate
under HTTP-01 — that is the one case the spinning-up page appears behind a browser warning.

Two events narrate it: `stack.slept { stack, deployment, reason, hosts }` and
`stack.woken { stack, deployment, by }` — `by` is `request:<hostname>` when traffic woke it, or who
asked. One thing to know about swarm logs: `docker service logs` has no `--until`, so
`GET …/logs?until=` answers 400 on a swarm stack rather than quietly widening the read.

### Share links

"Can you look at the logs of PR 123?" used to mean creating an account for someone who will sign in
once. A share link is a URL that opens **that deployment's details and log view, and nothing else**,
until it expires:

```bash
curl -s -X POST https://api.preview.example.com/api/deployments/pr-123/share \
  -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
  -d '{ "views": ["details", "logs"], "ttl": "24h" }'
```
```json
{ "url": "https://control.preview.example.com/deployments/pr-123/public-logs-view?token=eyJ…",
  "token": "eyJ…", "views": ["details", "logs"], "expiresAt": 1787086400000 }
```

`views` default to both; `ttl` defaults to `7d` and caps at `30d`. The same panel is on the
deployment's **Danger** tab (it mints a credential, so that is where it lives): pick the views and the
expiry, copy the link once.

What the token is: an HS256 JWT signed with `PSTACK_TOKEN`, carrying `{ dep, views, exp }`. It
travels as `?token=` in the query string — by design, because the log view follows logs over an
`EventSource`, and a browser cannot put a header on one. It also works as a bearer for a script:

```bash
curl -s "https://api.preview.example.com/api/deployments/pr-123/logs?tail=200&token=eyJ…"
curl -s https://api.preview.example.com/api/deployments/pr-123 -H "Authorization: Bearer eyJ…"
```

What a link can reach, enforced before any route: `GET /api/deployments/:id` (+ `/runtime`,
`/readiness`) with the `details` view; `GET …/logs` and `…/logs/stream` with the `logs` view; its
own deployment only; stored variables only (a `?PR=8` from a link holder does not resolve another
stack). Everything else answers 403. An expired or tampered token answers 401. The raw `PSTACK_TOKEN`
is **never** accepted from a query string — only a JWT is.

There is no per-link revocation: nothing about a link is stored (a table of issued links would be a
row describing a credential), so the TTL is what bounds a leaked one. Rotating `PSTACK_TOKEN`
(`PSTACK_TOKEN=<new> pstack init …`) invalidates every link at once; `pstack upgrade` deliberately
does not rotate it, so links survive upgrades. The event `share.created { deployment, views,
expiresAt, by }` says a link was minted — never the token.

## 7c. Sign in with your identity provider (0.27.0)

Accounts are created by hand, one at a time, by an admin or by whoever holds `PSTACK_TOKEN`. That
does not scale past a couple of people. Point this host at the identity provider your organisation
already runs and anyone who can authenticate against it can sign in — **no per-user setup here at
all**. Decide what that provider's `defaultRole` is before you do
([7e](#7e-who-can-do-what-the-four-roles-0320)): it decides what everyone who walks through the door
can reach. Leaving it empty inherits the host's `default_role`
([Runtime settings](#runtime-settings-0330)) — which is `viewer` until somebody raises it, and is
**capped below `admin`** on the way in, so an inheriting provider can never mint an administrator
however the host default is set. To have this provider mint admins, choose `admin` here.

You are configuring your *own* OAuth/OIDC application. Nothing is registered with anyone, no
directory is copied, and nothing is synchronised.

A host holds **as many providers as you like** — GitHub for the engineers and Google for everyone
else is the ordinary case, not a special one. Each is stored under a short **key** you choose
(lowercase letters, digits and dashes: `github`, `corp`, `okta-staging`), the login page draws one
button per enabled provider, and everything keyed — the button, `/start`, the delete — names a
provider by that key.

### Set it up

1. **Copy the callback URL** from **Sign-on** in the UI (or `GET /api/sso/config`). It is always:

   ```
   https://control.<your-domain>/api/auth/sso/callback
   ```

   One fixed path for every provider. It must match what you register on their side **exactly** — a
   mismatch is the most common failure in this protocol, and the error the provider returns rarely
   says so.

2. **Create the application** in your provider, with that callback URL. Where exactly, per
   provider, is the next section.

3. **Paste the client id and secret** into the Sign-on page, or:

   ```bash
   # A preset knows its own mode — {"provider":"google"} is enough to mean OIDC,
   # and everything else is discovered from the issuer.
   curl -s -X PUT https://api.preview.example.com/api/sso/config \
     -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
     -d '{ "key": "google", "provider": "google", "clientId": "…", "clientSecret": "…" }'

   # OAuth 2.0 — the preset fills in the endpoints, the scopes and the claim mapping
   curl -s -X PUT https://api.preview.example.com/api/sso/config \
     -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
     -d '{ "key": "github", "provider": "github", "clientId": "…", "clientSecret": "…" }'
   ```

   The issuer is **fetched while you save**, so a typo is refused there rather than discovered at
   somebody's first login attempt.

   `key` is optional exactly as far as compatibility needs it to be: a keyless save on an empty
   host derives the key from the config (the provider name, or `oidc`), and on a one-provider host
   replaces that one provider — so a script written before keys existed keeps working. With several
   providers configured a keyless save is refused, naming the keys, because which one to replace is
   not this API's guess to make.

A **Continue with …** button per enabled provider then appears on the login page of both UIs.

### The two modes

| | **OIDC** (prefer this) | **OAuth 2.0** |
|---|---|---|
| You provide | `issuer` (or `discoveryUrl`), `clientId`, `clientSecret` | `provider` preset, `clientId`, `clientSecret` |
| Endpoints | discovered from `/.well-known/openid-configuration` | from the preset, or typed for `custom` |
| Identity from | the **ID token's** claims, signature-verified against the provider's JWKS | the user-info endpoint, mapped by `claimMap` |
| Use it for | Google Workspace, Okta, Entra, Auth0, Keycloak, Authentik, … | GitHub, GitLab, Bitbucket, anything without user-facing OIDC |

Presets ship for **GitHub, GitLab, Bitbucket** (OAuth 2.0), **Google, Microsoft, Okta, Auth0,
Keycloak** (OIDC) and `custom`. A preset is only a set of defaults — every field stays editable, so
a self-hosted GitLab is the `gitlab` preset with three URLs replaced, not a `custom` provider typed
out by hand.

> **GitHub does publish an OIDC discovery document, and it is a trap.** It signs GitHub *Actions*
> job tokens for cloud workload identity — it is not a user login endpoint. GitHub user login is
> OAuth 2.0, here and everywhere.

### Where to create the app, per provider

Every walkthrough ends the same way: paste this host's callback URL into the field the provider
calls its redirect URI, whatever it names that field. The same hints ship in the API (`presets[]`
carries `setupUrl` and `setupHint`), so the Sign-on form shows them beside the fields.

- **GitHub** — an OAuth App under **[Developer settings](https://github.com/settings/developers)**;
  an org-owned app works the same way. The callback URL goes in as the *Authorization callback
  URL*. The preset's `read:user user:email` scopes cover sign-in — `user:email` because a profile
  email is private by default and served from a second endpoint. Add `read:org` only for a group
  rule (below).
- **GitLab** — an application under your **[user or group settings](https://gitlab.com/-/user_settings/applications)**;
  the callback URL is the *Redirect URI*. `read_user` covers sign-in; add `read_api` for a group
  rule. A self-hosted GitLab is this preset with the three endpoint URLs pointed at your host.
- **Bitbucket** — an **OAuth consumer** in your workspace settings
  ([Atlassian's walkthrough](https://support.atlassian.com/bitbucket-cloud/docs/use-oauth-on-bitbucket-cloud/));
  the callback URL is the *Callback URL*. `account email` is all sign-in needs — Bitbucket serves
  the address from a second endpoint always, so `email` is not optional.
- **Google** — an OAuth client ID of type *Web application* on the Google Cloud console's
  **[Credentials page](https://console.cloud.google.com/apis/credentials)** (the console walks you
  through the consent screen first if you have none); the callback URL is an *Authorized redirect
  URI*. The issuer is `https://accounts.google.com` and the default `openid email profile` scopes
  are all sign-in needs. To restrict sign-in to your Workspace domain, use `allowedEmailDomains` —
  the provider-side alternative is an *Internal* consent screen, and both is fine.
- **Microsoft** — register an application in **[Microsoft Entra ID](https://entra.microsoft.com)**
  (*App registrations*), create a client secret under *Certificates & secrets*, and put the
  callback URL in as a *Web* redirect URI. The preset's issuer is a template —
  `https://login.microsoftonline.com/<tenant-id>/v2.0` — and the tenant id is **required, not a
  nicety**: Microsoft's multi-tenant `common`/`organizations` discovery documents publish the
  literal string `{tenantid}` as their issuer, and this host verifies an id token's issuer against
  the discovery document *verbatim*, so the placeholder issuer would rightly refuse every login. A
  single-tenant URL has a real issuer and works. Your *Directory (tenant) ID* is on the app's
  overview page.
- **Okta** — an *OIDC Web Application* in the admin console (*Applications → Create App
  Integration*; [Okta's guide](https://developer.okta.com/docs/guides/sign-into-web-app-redirect/));
  the callback URL is the *Sign-in redirect URI*. Replace `<your-domain>` in the template with your
  Okta domain — `/oauth2/default` is the org's default authorization server, which most orgs use.
- **Auth0** — a *Regular Web Application* in the
  **[dashboard](https://manage.auth0.com/#/applications)**; the callback URL is an *Allowed
  Callback URL*. Replace `<your-tenant>` with your tenant — copy the *Domain* field from the
  application's settings, because a region suffix (`.eu.auth0.com`) may be part of it.
- **Keycloak** — an OpenID Connect client in your realm's admin console with **client
  authentication on** (a confidential client, so it has a secret); the callback URL is a *Valid
  redirect URI*. Replace `<host>` and `<realm>` in the template with your own.

**Four of those issuers are templates, and the honesty is deliberate.** Microsoft, Okta, Auth0 and
Keycloak have no single issuer — every tenant, domain or realm gets its own — so the preset ships
the URL's *shape* with a `<placeholder>` where your value goes. Saving one with the placeholder
still in it is refused at save time with a sentence saying exactly what to replace; the UI renders
the placeholder as a field to fill in rather than a value to accept. A preset that quietly pointed
everyone at a "default" that cannot work would cost each operator the same afternoon.

### Who gets an account

By default, **anyone who successfully authenticates**. Your provider already owns that decision —
Workspace restricts to internal users, a GitHub OAuth app can be org-approved — and duplicating it
here would only be a second list to keep in step.

If you want a second list anyway: three allow rules, the endpoint one of them needs, and a
placeholder.

| Field | Effect |
|---|---|
| `allowedEmailDomains: []` | Non-empty ⇒ a login whose email is outside the list is refused. **Fails closed**: a provider that returns *no* address (a private GitHub profile) is refused too, not waved through |
| `allowedUsernames: []` | Non-empty ⇒ a login whose username matches none of these **glob** patterns is refused. `*`, `?` and character classes (`qa-[0-9]*`), matched case-insensitively; a malformed pattern (`qa-[0-9`) is refused when you save rather than left silently matching nobody. **Fails closed the same way** — see the warning below, because this one has a sharp edge |
| `requiredGroups: []` | Non-empty ⇒ the provider is asked which groups/orgs this user belongs to, and a login in none of them is refused. **Exact** names, case-insensitive — not globs, because a GitLab group is a path (`acme/backend`) and `*` would not mean what you'd expect across the `/`. Needs a preset and a scope: see below |
| `groupsUrl` | Where that group list is read from. Filled in by the preset (`https://api.github.com/user/orgs`, `https://gitlab.com/api/v4/groups`); type your own for a self-hosted provider |
| `defaultRole` | The [role](#7e-who-can-do-what-the-four-roles-0320) an auto-provisioned account is created with — one of `viewer`, `developer`, `maintainer`, `admin`, or **empty to inherit the host's `default_role`** ([Runtime settings](#runtime-settings-0330)), which is `viewer` until somebody sets it. Inheriting is resolved when an account is provisioned, not frozen when you save, so changing the host default changes what every inheriting provider mints. It is **never `admin` by omission**, and that is enforced rather than merely intended: an inheriting provider is **capped below `admin`** even if the host default IS `admin`, because otherwise two individually-sane settings compose into "any stranger who completes the OAuth flow is an administrator". In 0.27.0 an omitted value meant `admin` outright, and with all three allow-lists empty (how every preset saves) that is exactly what happened. To have a provider mint admins you must say `admin` **on that provider**, deliberately |

The rules **and** together: each list is any-of, and every rule you set has to pass. They are
**per provider**: each stored provider carries its own three lists, and a login is checked against
the rules of the provider it came through — a GitHub org rule says nothing about who may sign in
with Google.

> **A username rule on a provider that supplies no username locks out everyone, not no one.** The
> username is whatever `claimMap.username` names in the provider's answer — `login` on GitHub,
> `username` on GitLab, `preferred_username` on OIDC. Plenty of OIDC providers send no such claim,
> and there is nothing to match against, so the rule refuses (`this provider returned no username,
> and sign-in is restricted to specific usernames`) rather than quietly passing. That is the same
> direction `allowedEmailDomains` fails in, and it is deliberate — but on a provider you have not
> checked, `allowedEmailDomains` is the safer rule to reach for. Log in once with the rule unset and
> look at the username you were given.

`requiredGroups` asks more of the configuration than the other two, and **all of it is checked while
you save**, not at somebody's first login:

- **An OAuth 2.0 preset — `github` or `gitlab`.** The group list is a second call to a
  provider-specific endpoint, and only the preset knows which field of the answer names a group
  (`login` for GitHub, `full_path` for GitLab, so a GitLab rule is written `acme/backend`).
  `custom`, `bitbucket` and OIDC are refused outright: a discovery document declares no groups
  endpoint and there is no groups claim in the mapping, so an OIDC group rule could only ever fail
  every login.
- **The scope that endpoint needs, in `scopes`** — `read:org` for GitHub, `read_api` for GitLab.
  Without it the answer omits private memberships or is refused outright, and a legitimate member is
  locked out with no diagnostic anywhere. The save refuses and names the scope. Adding it changes
  what `/start` asks the provider for, so expect a consent screen the next time people sign in.
- **`groupsUrl`, if you moved `userInfoUrl` off the preset's host.** A self-hosted GitLab's access
  token is not sent to gitlab.com to ask what groups it is in, so the preset's URL is not inherited
  once the endpoints stop being the preset's. The response shape is still assumed to be the
  preset's.

**A group rule fails closed, in two deliberately different ways.** Not a member is `you are not in
a group this host allows to sign in (…)`. A group list that never arrived — a revoked scope, a
rate limit, an outage — is `your group memberships could not be determined, …`, and carries what
the provider actually answered. The login is refused either way; the sentence is the difference
between adding somebody to a group and fixing your OAuth app.

An account is keyed on **`(provider, subject)`**, never the email — someone changing their address
keeps their account, their history and their personal tokens. An address is used for exactly one
thing: adopting a local account that already exists with the same address, and only when the
provider says the address is verified. Give a local account an address when you create it
(`POST /api/users { username, password, email }`) if you want that to happen.

### What a login actually does

`GET /api/auth/sso/start?provider=<key>` mints a `state` and a PKCE verifier, parks them — the
provider's key alongside — for five minutes, and redirects to that provider.
`GET /api/auth/sso/callback` spends that state **once**, reads back **which provider the login
started against** (never "the" provider — a login begun against one row is completed against that
row's endpoints, secret and allow rules, whatever changed meanwhile), exchanges the code (with the
verifier and the client secret), verifies the ID token or fetches the user, and sets the same
session cookie a password login sets. Failures land back on `/login?sso_error=…` with the
provider's own words — prefixed with the provider's label when the host has several, because two
buttons failing with identical sentences is undiagnosable.

`?provider=` is optional exactly where it can be: with **one** enabled provider a keyless `/start`
picks it — every login link that predates keys keeps working — and with several it lands on the
login page naming the keys, because which directory vouches for you is not a guess this host makes.

`?next=/deployments/pr-7` on `/start` is carried through and returned to — same-origin paths only.

One subtlety worth knowing before you configure two rows against the *same* upstream: an account is
keyed on the upstream **directory** (the OIDC issuer, or the preset for OAuth 2.0) plus the
subject — not on your key. Two providers on the same GitHub preset therefore share accounts, which
is correct: the same subject from the same directory is the same person, and renaming or
re-creating a provider under a different key never orphans anybody.

### Removing one

```bash
curl -X DELETE https://api.preview.example.com/api/sso/config/github -H "Authorization: Bearer $PSTACK_TOKEN"
```

That provider is forgotten and its button disappears; every other provider keeps answering. A bare
`DELETE /api/sso/config` still works on a host with exactly one provider — what it meant before
keys existed — and refuses on a host with several, naming the keys. **Nobody is deleted** either
way — those accounts keep their personal tokens, and pointing the same provider back at this host
re-links them by subject (under whatever key — see above). Set a password on one
(`PUT /api/users/:id/password`) if someone needs to get in meanwhile.

## 7d. Move a host's configuration to another host (0.30.0)

Rebuilding a host, moving to a bigger box, or standing up a staging twin used to mean recreating
every account, secret, notifier and registry login by hand. `pull config` seals the whole lot into
one file; `push config` applies it somewhere else.

```bash
# on the host you are copying FROM
export PSTACK_API_URL=https://api.preview.example.com
export PSTACK_TOKEN=…
pstack pull config -o host.sealed          # asks for a passphrase, twice

# on the host you are copying TO
export PSTACK_API_URL=https://api.staging.example.com
export PSTACK_TOKEN=…                      # ITS root token, not the first host's
pstack push config -i host.sealed          # asks for the same passphrase
```

`host.sealed` is written `0600`, and it is a scrypt + AES-256-GCM envelope sealed on **your**
machine — never on the server, because sealing server-side would mean sending the passphrase to the
server, which is strictly worse. The passphrase comes from `PSTACK_CONFIG_KEY`, or is prompted for
without echo. **There is no passphrase flag on purpose**: argv is world-readable through `ps`.

### Applying one from the UI

The advanced UI has an **Apply config** page: pick the sealed file, type the passphrase, press
Preview to see what the file will make this host trust, then Apply. It posts to
`POST /api/config/sealed` and needs an **admin** — a session is enough.

**It can apply a config and cannot produce one, on purpose.** Export is exfiltration: one request
and every credential on this host is in the caller's hands, so `GET /api/config` refuses a browser
session and answers only to the `PSTACK_TOKEN` bearer. A download button would mean an XSS, a
borrowed laptop or a stolen cookie yields the lot in one click. Import runs the other way — whoever
uses that page must already hold the file *and* its passphrase, so it shows them nothing they could
not read for themselves.

That asymmetry is also why the passphrase is sent to the server here while `pull config` keeps it
local: every secret in the file is about to be plaintext on this host anyway, so there is nothing
left for it to protect from *this* host. It is used once and never stored.

Preview exists because this is the widest-reaching write in the API — a config can create an
administrator and choose where the host pulls its images from — and a browser has no equivalent of
the CLI's confirmation prompt unless the server offers it one. Nothing is written until you have
seen the list.

`PSTACK_API_URL` has no default. A guess would silently talk to the wrong host, and this is the one
command where the payload is every credential you have. Plain `http://` is refused unless the host
is loopback or a private address, for the same reason.

### What travels, and what deliberately does not

| Travels | Stays behind |
|---|---|
| accounts, with their password hashes — people keep the passwords they already have | deployments: they are per-PR and ephemeral, and belong to the host's Docker |
| API tokens (hashes), so scripts keep working — and a document you author may [name the token itself](#predeclaring-the-tokens-a-rebuilt-host-should-hold-0340) | login sessions and half-finished SSO sign-ins |
| host variables **and secrets** | notifier delivery history |
| notifiers, with their signing secrets and URLs | terminal sessions |
| the SSO providers and their client secrets | |
| registry logins, routing files, named specs | |

Restoring the right-hand column into a *different* host would be wrong, not merely useless — so
none of it is in the file, and nothing in the file names it.

#### Predeclaring the tokens a rebuilt host should hold (0.34.0)

**A migration already preserves API tokens.** They travel as SHA-256 digests, which is what the
`tokens` table stores anyway, so every script and CI secret holding one keeps working on the other
side without anything being re-issued. That is the round trip, and it needs nothing from you.

The other direction is a document **nobody exported** — one you author, declaring the credentials a
rebuilt host should come up holding. A token row may name the token itself instead of its digest:

```json
{
  "version": 1,
  "users":  [{ "username": "ci", "role": "developer", "passwordHash": "$argon2id$…", "createdAt": 1 }],
  "tokens": [{ "username": "ci", "name": "pipeline", "token": "pstack_pat_…", "createdAt": 1 }]
}
```

`token` is hashed on apply with the same function that mints one, so the value authenticates exactly
as an issued token would. A row may carry **either** `token` or `tokenHash`; carrying both with
different values is refused rather than guessed at, and named in the skip list.

Three things to know before you use it:

- **A token belongs to an account.** There is no host-wide machine token but `PSTACK_TOKEN` (below),
  so the document must create — or the host must already have — the user the token is for. The
  token inherits **that account's role**, and follows it: promote the account and every token it
  holds is promoted with it.
- **An export never emits `token`.** The host does not have the plaintext to emit; that is the point
  of storing a digest. Round-tripping an export is still hash-only.
- **`pull config`'s pre-write summary calls it out** — "carried in PLAINTEXT, so whoever wrote this
  file holds it" — because a digest proves nothing about its author and a plaintext token proves
  they hold the credential. Seal the file (`push config` requires it) and treat it as the secret it
  is.

**`PSTACK_TOKEN` is not one of these and cannot be a list.** It is a single value, compared directly,
and it is also the **HMAC key share links are signed with** — which is what makes rotating it the
only way to revoke every outstanding link. A second accepted value would silently break that, so
per-machine credentials are personal tokens, named and individually revocable, not extra root ones.

### Only `PSTACK_TOKEN` can do this

`/api/config` answers for the **root token** and nothing else. An admin browser session gets `403`,
and so does an admin's personal API token — the two credentials the UI hands out. The cost is
deliberate and permanent: there is no button for this in the UI and there should never be one. A
full credential dump reachable from a cookie means one XSS, or one stolen laptop with a live
session, empties the host from the victim's own browser.

Both directions emit an event — `config.exported` and `config.imported` — so a credential dump
reaches your notifiers instead of happening silently. The payloads carry counts and identities
(which registries, which notifier names), never contents.

### `push` creates; it never overwrites

Everything is matched by its natural key — username, variable name, notifier name, registry host —
and anything already on the target is **skipped**, not updated:

```
created  user alice
created  secret DB_PASSWORD
created  notifier "ops"
created  registry ghcr.io as bot
```

Run it again and every line becomes a `skipped` with the reason:

```
skipped  user alice: an account with that name already exists here
skipped  registry ghcr.io: a credential for it is already stored here
```

So this is a *merge onto* a host, not a *restore over* one: to change a value that is already
there, change it the normal way afterwards. Applying the same file twice creates nothing the second
time, which is also the repair procedure — **apply is not transactional.** A step that fails
part-way leaves whatever it had already written, so if `push` errors, fix the cause and run it
again; the entries that landed the first time are simply skipped.

Before it writes anything, `push` names every registry and notifier URL the file would make this
host trust, and asks:

```
Applying host.sealed would make this host trust:
  - pull images from ghcr.io as bot
  - send slack notifications to webhookUrl=https://hooks.slack.com/services/… ("ops")
Apply it? [y/N]:
```

Those strings **are** credentials, so they are printed to a terminal only. With `-y`, or anywhere
stderr is a pipe, you get a count instead and the list never reaches a log file. `-y` is also
mandatory when there is no terminal to ask: `push` will not apply a file unattended without it.

### Onto a machine that does not exist yet

`pstack cloud-init` can carry the export into a first boot:

```bash
pstack cloud-init --domain … --acme-email … --config host.sealed          # embed the file
pstack cloud-init --domain … --acme-email … --config-url https://…        # fetch it at boot
```

Both need `PSTACK_CONFIG_KEY` set when you render, because the machine being provisioned is what
has to open the file. That is the tension, and the rendered file states it in its own SECRETS
header rather than papering over it:

- `--config` puts **the sealed export and its passphrase in the same file**, which your provider
  stores as instance metadata. The seal protects that export everywhere except exactly there.
- `--config-url` embeds only the passphrase. Opening the export then needs the metadata **and**
  access to the URL — the combination that actually buys something. Serve it from somewhere only
  the new host can reach, and take it down afterwards.

The apply runs as the **last** boot step, after `init`, reaching the API on the control container's
Docker bridge address rather than `https://api.<domain>` — this early in a first boot your DNS may
not have propagated and Traefik may hold no certificate, and a config that silently failed to apply
is the one outcome that step must not have. It reads `PSTACK_TOKEN` out of `control/.env` itself, so
that token is not carried in the file. The sealed copy is deleted whether the apply worked or not,
and a failure shouts in the boot log rather than leaving you with a host that looks finished.

### What this costs you

Two risks come with the feature itself. Neither is a bug, and neither goes away.

1. **A leaked export is offline-crackable against every account.** It contains argon2 password
   hashes for every user on the host — that is *why* logins keep working after a move. Someone who
   gets both the file and the passphrase does not need to crack anything; someone who gets only the
   file can grind at the hashes with no rate limit and nobody watching. Keep the file and the
   passphrase apart, commit neither, and delete the file when the move is done. Rotate the source
   host's credentials if you cannot account for a copy.
2. **`push` writes registry credentials**, which is to say a config file decides where a host pulls
   its images from — and its notifiers decide where that host's events are sent. A file you did not
   produce yourself can repoint both. That is what the pre-write summary above is for; read it, and
   do not pipe `-y` at a file whose origin you cannot name.

## 7e. Who can do what: the four roles (0.32.0)

Until 0.32.0 every account on a host could do everything every other account could do. Roles fix
that. There are four, they are **ordered**, and each one includes everything below it:

| Role | What it adds |
|---|---|
| `viewer` | **Reads.** Deployments, runtime, logs and log streams, source, readiness, jobs, specs, routing, registries, host variables (never a secret's *value*), notifiers and their deliveries, the control stack, the swarm, terminal-session history, and the list of accounts |
| `developer` | **Stacks.** Submit and delete deployments, `up` / `down` / `verify` / `sleep` / `wake`, start / stop / restart a container, cancel a job, mint a share link, open a container shell, write and delete named specs |
| `maintainer` | **The host's configuration.** Host variables and secrets, private-registry logins, Traefik dynamic files, notifiers (create, edit, test, redeliver), the swarm join token, and *reading* the SSO configuration |
| `admin` | **People.** Create, delete and re-role accounts, set someone else's password, *write* the SSO configuration, and apply a sealed config |

Two things sit outside that ladder:

- **`root` — whoever holds `PSTACK_TOKEN`.** Not a role, not an account, and above all four. It is
  also the only principal that may `GET`/`POST /api/config`, the plaintext export of every
  credential on the host ([7d](#7d-move-a-hosts-configuration-to-another-host-0300)); an admin session is
  `403` there, deliberately.
- **A share link is not a weak role.** It reaches exactly the views it was minted with, on exactly
  its own deployment, `GET` only ([share links](#share-links)). Roles changed nothing about it.

### The matrix

The table below is the whole of it. Anything not listed — a route that does not exist, a method a
route does not answer — is **root's alone**: the gate is default-deny, so a new route is closed to
every account until someone puts it in the table.

| Route | Least role |
|---|---|
| `GET /api/health` | none — it is how `init` waits for the container |
| `GET /api/auth/me` · `POST /api/auth/logout` | any account |
| `GET`/`POST /api/tokens` · `DELETE /api/tokens/:id` | any account (they are already scoped to the caller) |
| `PUT /api/users/:id/password` **for yourself** | any account |
| every `GET` in the `viewer` row above, including `GET /api/users` | `viewer` |
| `PUT`/`DELETE /api/deployments/:id` | `developer` |
| `POST /api/deployments/:id/{up,down,verify,sleep,wake}` | `developer` |
| `POST /api/deployments/:id/cancel` — stop the running and queued jobs for a stack | `developer` |
| `POST /api/deployments/:id/containers/:name/{start,stop,restart}` | `developer` |
| `POST /api/deployments/:id/share` · `POST /api/jobs/:id/cancel` | `developer` |
| `WS /api/deployments/:id/terminal` | `developer` |
| `PUT`/`DELETE /api/specs/:name` | `developer` |
| `PUT`/`DELETE /api/host-vars/:name` · `/api/registries/:host` · `/api/routing/:file` | `maintainer` |
| `POST`/`PATCH`/`DELETE /api/notifiers`… (incl. `/test`, `/redeliver`) | `maintainer` |
| `GET /api/swarm/join` | `maintainer` |
| `GET /api/sso/config` | `maintainer` |
| `GET /api/settings` | `viewer` |
| `PUT /api/settings/max_jobs` | `maintainer` |
| `PUT /api/settings/default_role` | `admin` |
| `POST /api/users` · `PATCH`/`DELETE /api/users/:id` | `admin` |
| `PUT /api/users/:id/password` **for anybody else** | `admin` |
| `PUT`/`DELETE /api/sso/config` · `/api/sso/config/:key` | `admin` |
| `POST /api/config/sealed` | `admin` |
| `GET`/`POST /api/config` | **root token only** |

Four of those rows are decisions rather than deductions, and each looks wrong until you hold it
next to the thing beside it:

- **The container shell is a developer's, not an admin's.** A developer can already `up` an
  arbitrary compose file on this host — including a service that mounts the Docker socket — so
  refusing them a shell would be theatre while the larger door stands open. If that bothers you, the
  door to close first is `up`.
- **`GET /api/swarm/join` dropped from admin to maintainer.** It is a host-configuration read and it
  belongs with the others. It is still a real credential — the token joins a machine to your
  cluster — so it stops at maintainer and goes no lower.
- **Writing the SSO configuration is admin, though every other host-configuration write is
  maintainer.** A provider's `defaultRole` *mints accounts* at whatever role it names, so a
  maintainer who could point this host at an identity provider they control would be able to sign in
  through it as an admin. That is a promotion path, and promotion paths live with people. Reading
  the configuration stays maintainer — it returns a mask, never the client secret.
- **The two runtime settings split by KEY, not by route.** `max_jobs` is operational and sits with
  host configuration at maintainer; `default_role` decides what every account created without an
  explicit role can reach, which is user management by another name, so it sits with the promotion
  paths at admin. Reading either is a viewer's — the page that shows the cap is the page everybody
  sees, and each row carries the role it takes to change it.

A refusal says which role was wanted and which one you hold, because an operator staring at a bare
`forbidden` cannot tell a wrong role from a wrong URL:

```console
$ curl -s -X PUT https://api.preview.example.com/api/host-vars/DSN \
    -H "cookie: $SESSION" -d '{"value":"…","secret":true}'
{
  "error": "this route requires the maintainer role or higher — you are a developer"
}
```

### Giving somebody a role

A role is set when the account is created and changed with `PATCH`. Both are admin's (or root's):

```console
$ curl -s -X POST https://api.preview.example.com/api/users \
    -H "authorization: Bearer $PSTACK_TOKEN" \
    -d '{"username":"dana","password":"…","email":"dana@example.com","role":"developer"}'
{ "user": { "id": 7, "username": "dana", "role": "developer", … } }

$ curl -s -X PATCH https://api.preview.example.com/api/users/7 \
    -H "authorization: Bearer $PSTACK_TOKEN" -d '{"role":"maintainer"}'
{ "updated": 7, "role": "maintainer" }
```

- A **personal access token inherits its owner's role** and can never exceed it. The CI job holding
  one is that person, with that person's permissions — so a `viewer`'s token cannot deploy.
- A promotion or demotion **takes effect on that account's next request**. Sessions and tokens are
  deliberately not revoked: every request reads the role fresh, and logging somebody out is a worse
  signal, not a stronger one.
- **The last admin cannot be deleted or demoted** — not by another admin, and not by the root token
  either. `PSTACK_TOKEN` may live only in a CI secret store or have been rotated away from every
  human on the team, and a host with zero admin accounts cannot create one through the API; the
  repair is a hand-edited database over SSH. Promote a replacement first:
  `cannot demote the last admin — promote another account first`.

### Upgrading a host that already has accounts

**Nothing breaks and nobody is locked out.** The `role` column has carried a default of `admin`
since it was added, so every account that exists on your host today is already an `admin` and keeps
doing exactly what it did before. There is no migration to run and no window in which somebody
cannot log in. Roles only start to matter for the accounts you create *after* the upgrade — and for
the ones you deliberately demote.

Demote deliberately, on your own schedule. `PATCH /api/users/:id` one at a time, leaving at least
one admin behind.

> **⚠️ Breaking: `POST /api/users` no longer creates an admin.**
>
> It takes an optional `role`, and **an absent `role` now means the least privilege**, where it used
> to silently mean the most. A script or a provisioning step that relied on the old behaviour has to
> say `"role": "admin"` and mean it.
>
> Since 0.33.0 an absent `role` means the host's **`default_role`** setting — `viewer` until an admin
> sets it, and never `admin` by omission ([Runtime settings](#runtime-settings-0330)). So it can be
> raised, but only on purpose, and only by somebody who could already create the account.
>
> The same route is now **admin-only**. Previously it was reachable by *any* authenticated
> principal and always created an admin, which meant any account on the host could mint itself a
> second, fully privileged one. That was the hole this release closes.
>
> An unknown role is a `400` naming the four, rather than a stored value that quietly reaches
> nothing.

### What this is, and what it is not

This is **coarse, role-based access control**: four fixed tiers, one ordered comparison, one table.
It is deliberately the smallest thing that removes "every account can do everything", and it buys
you the ordinary team shape — a contractor who can look, a developer who can deploy, an operator who
holds the host's secrets.

It is **not** a policy engine and not multi-tenancy. There is no "developer, but only on `pr-*`", no
"maintainer of these three registries", no per-deployment ownership. Those are attribute-based
questions, and answering them properly means a real isolation boundary underneath — separate VMs or
namespaces, a credential boundary, a per-tenant Docker — not more rows in this table.
[ABAC](https://en.wikipedia.org/wiki/Attribute-based_access_control) is the direction this would
grow in if it grows; adding attributes to *these* four tiers without the boundary underneath would
buy the appearance of isolation and none of it. Until then: one host, one trust level per tier, and
a developer who can `up` an arbitrary compose file is trusted with the host.

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
      - name: Install pstack
        run: |
          mkdir -p "$HOME/.local/bin" && echo "$HOME/.local/bin" >> "$GITHUB_PATH"
          curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh \
            | PSTACK_INSTALL_DIR="$HOME/.local/bin" sh

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
      - name: Install pstack
        run: |
          mkdir -p "$HOME/.local/bin" && echo "$HOME/.local/bin" >> "$GITHUB_PATH"
          curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh \
            | PSTACK_INSTALL_DIR="$HOME/.local/bin" sh

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
- **The installer is the whole install** — one static binary, no runtime — so this workflow runs on
  a hosted runner too if your hooks and Docker do. Pin the version (`PSTACK_VERSION=<x.y.z>` in
  front of `sh`) if you want teardown to behave identically to the deploy that created the stack.

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

Derived from `internal/cli`, `internal/initctl`, `internal/api`, `internal/spec`,
`internal/compose`, `internal/registry` and `templates/control/docker-compose.yml` (all under
`packages/pstack`).

### Commands

```
pstack <up|down|verify|status|validate|init|serve|swarm|…> [flags]
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
| `swarm [status]` | the swarm's nodes, the manager address and the ports a worker needs. Read-only; reads docker every time. **Exit 1 when this host is not a manager**, so a script need not parse it. | 0 · 1 |
| `swarm join` | what a new worker runs — `--format command` (default), `script`, `cloud-config` (+`--distro`) or `token`. **The output is a secret**: every shape embeds the join token. To stdout, or `-o <file>`. | 0 · 1 · 3 |
| `api <group> <command>` | call any API route. Generated from `api/openapi.yaml`; `--help` at any level lists what is under it. Needs `PSTACK_API_URL` and `PSTACK_TOKEN`. Non-2xx exits non-zero | 0 · 1 |
| `pull config` | seal this host's whole portable configuration — accounts, tokens, host secrets, notifiers, SSO, registry logins, routing files, named specs — into one `0600` file (`-o`, never stdout). Talks to `PSTACK_API_URL` as the **root token**. | 0 · 1 · 3 |
| `push config` | apply such a file onto the host at `PSTACK_API_URL`. **Creates, never overwrites**; names every registry and notifier URL it would trust and asks first (`-y` to skip the question and the list). Not transactional — a failure leaves what it already wrote. | 0 · 1 · 3 |

`init`, `serve`, `swarm` and `pull`/`push` are **spec-free**: they act on the host and on the registry, so none of
them loads `preview.yml` or fails because it is absent.

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
| `--orchestrator swarm\|compose` | `init` `cloud-init` `upgrade` | default **`swarm`** for `init`/`cloud-init` (or `PSTACK_ORCHESTRATOR`); `upgrade` keeps the host's current one unless the flag is typed. |
| `--format <shape>` | `swarm join` | `command` (default), `script`, `cloud-config` or `token`. An unknown one exits 3. |
| `--distro <name>` | `cloud-init` `swarm join` | which Docker install steps the rendered cloud-config uses: `ubuntu` `debian` `fedora` `suse` `arch` `alpine`. Ignored by the other formats. |
| `-o`, `--out <file>` | `cloud-init` `swarm join` `pull config` | write the rendered file instead of printing it. **Required** for `pull config`, which never writes an export to stdout and creates the file `0600`. |
| `-i`, `--in <file>` | `push config` | the sealed export to apply. **Required** — there is no stdin form. |
| `--admin-user <name>` | `cloud-init` | the first UI account, created on first boot. **Prompted** when absent unless `-y`. Its password comes from `--admin-password`, or is generated and printed beside the username |
| `--admin-password <pw>` | `cloud-init` | that account's password. Refused without `--admin-user`. Never prompted — an absent one is generated, so the value exists nowhere else and can be discarded after the first sign-in |
| `--api-token <token>` | `cloud-init` | `PSTACK_TOKEN`, written into the rendered file. Omitting it is preferred: `init` then mints one on the host and prints it once into the boot log, keeping it out of instance metadata |
| `--config <file>` | `cloud-init` | embed a sealed export in the rendered file, applied on first boot. The **passphrase is embedded too**, so both sit in instance metadata. |
| `--config-url <url>` | `cloud-init` | fetch the export at boot instead, so only the passphrase is embedded. Mutually exclusive with `--config`. |
| `-y` | `cloud-init` `push config` | never prompt. On `push config` it also replaces the list of registries and notifier URLs with a count — those strings are credentials, and a log is not a terminal. |
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
| `PSTACK_API_URL` | `pull config` `push config` | — | which pstack to talk to, e.g. `https://api.preview.example.com`. **No default on purpose** — a guess would silently talk to the wrong host. Plain `http://` is refused unless the host is loopback or a private address. |
| `PSTACK_CONFIG_KEY` | `pull config` `push config` `cloud-init` | *prompted* | the passphrase the export is sealed with. Prompted without echo when unset and on a terminal; **flag-less on purpose**, because argv is world-readable through `ps`. |
| `PSTACK_HOST` | `serve` | `127.0.0.1` | listen address. Forced to `127.0.0.1` without a token; a non-loopback value without a token is refused with exit 3. |
| `PSTACK_DATA` | `serve` `init` | `/var/lib/pstack` | registry + control-stack config root. The registry lives at `<PSTACK_DATA>/deployments`. |
| `PSTACK_DOMAIN` | `init` | — | same as `--domain` |
| `PSTACK_ACME_EMAIL` | `init` | — | same as `--acme-email` |
| `PSTACK_CHALLENGE` | `init` | `http01` | same as `--challenge` |
| `PSTACK_DNS_PROVIDER` | `init` | — | same as `--dns-provider` |
| `PSTACK_DNS_TOKEN` | `init` | *unset* | the DNS-01 credential; written to `control/dns.env` (`0600`) under the provider's own variable name. **Flag-less on purpose** — a secret does not belong in a shell history. |
| `PSTACK_IMAGE` | `init` | `pstack:local` | the control-stack image |
| `PSTACK_ORCHESTRATOR` | `serve` `init` | `compose` / `swarm` | `serve`: the default for a spec that does not say (`compose`); `init`: same as `--orchestrator` (`swarm`). The control stack sets it for the API from what `init` decided. |
| `PSTACK_DOMAIN` | `serve` | — | lets the API build absolute share-link URLs on `control.<domain>`. Set by the control stack. |
| `PSTACK_PROBE` | `serve` | *on* | `off` removes `GET /api/probe/:id`, the unauthenticated [probe](#probe-a-preview-without-a-token-0340); the path then 404s like any unknown one. Any other value, including a misspelling, leaves it on — a typo should not silently turn a CI pipeline into one that polls a 404 forever |
| `PSTACK_MAX_JOBS` | `serve` | `4` | lifecycle jobs running at once, across every stack. **The default for the `max_jobs` setting, not the authority** (0.33.0): a value stored through `PUT /api/settings/max_jobs` outranks it and survives a restart, so setting it here is how a host that never opens the UI is configured — see [Runtime settings](#runtime-settings-0330). |
| `PSTACK_TRAEFIK_METRICS` | `serve` | — | Traefik's Prometheus endpoint, what `sleep.idle` reads. `http://traefik:8082/metrics` inside the control stack; unset means `idle` never triggers. |
| `PSTACK_READINESS_POLL_MS` · `PSTACK_READINESS_TIMEOUT_MS` | `serve` | `2000` · `180000` | how often the readiness watcher re-reads docker, and how long before it calls a stack timed out. Tuning for a test harness driving `serve` black-box; a host never needs them. |
| `PSTACK_READINESS_RESTART_LOOP` | `serve` | `3` | restarts tolerated before the readiness watcher calls a container a crash loop. Unlike the two above, this is operator-facing: raise it on a swarm host — swarm has no `depends_on`, so a dependent service legitimately restarts a few times while its database converges. |
| `PSTACK_SSO_STATE_TTL_S` · `PSTACK_SSO_DISCOVERY_TTL_S` | `serve` | `300` · `3600` | how long a half-finished SSO sign-in is remembered, and how long a provider's discovery document and JWKS are trusted. Same audience. |
| `PSTACK_PORT` | `healthcheck` | `7878` | the port `pstack healthcheck` probes — the container HEALTHCHECK, exit 0 or 1 on `GET /api/health`. |
| `PSTACK_BINARY` | `build-image` | *unset — the image installs this version from its release* | path to a `pstack` binary to copy into the control image instead, for a version that is not published yet or a host with no network at build time. |
| `PSTACK_VERSION` · `PSTACK_INSTALL_DIR` | `install.sh` | *the script's release* · `/usr/local/bin` | pin the version the installer fetches; where it puts the binary. `pstack upgrade` sets both. |

`serve` needs **no** spec and no stack variable to start.

### Runtime settings (0.33.0)

Two host knobs are changeable **while the server runs**, without restarting the control container.
They live in the database, and the list is closed — this is not a general key-value store, and a key
nobody named is refused rather than stored.

| Key | Value | Read | Write | What it decides |
|---|---|---|---|---|
| `max_jobs` | whole number ≥ 1 | `viewer` | `maintainer` | how many lifecycle jobs run at once across every stack. **In force on the request that sets it** — the registry takes the new cap immediately |
| `default_role` | one of `viewer`, `developer`, `maintainer`, `admin` | `viewer` | `admin` | the role an account created with **no `role` named** gets: `POST /api/users` without one, and any SSO provider whose own `defaultRole` is empty |

**Precedence: database > environment > built-in default.** A stored value wins; without one the
environment variable is used; without that, what the binary ships with. `PSTACK_MAX_JOBS` is
therefore the *default* for `max_jobs`, not the authority — a host whose operator never opens the UI
behaves exactly as it always did, and one who sets the value here is **not overridden on the next
restart**. `default_role` has no environment variable at all: it is `viewer` until somebody sets it.

**Which layer won is readable**, because a box that says `4` when you typed `8` is otherwise
unexplainable:

```bash
curl -s https://api.preview.example.com/api/settings -H "Authorization: Bearer $PSTACK_TOKEN"
```
```json
{
  "settings": [
    { "key": "max_jobs",     "value": 4,        "source": "default", "minRole": "maintainer" },
    { "key": "default_role", "value": "viewer", "source": "default", "minRole": "admin" }
  ],
  "env": { "PSTACK_MAX_JOBS": null },
  "precedence": "database > environment > built-in default"
}
```

`source` is `db`, `env` or `default`; `env` reports what this process was started with (`null` when
nothing usable was set), so an override you cannot see from the UI is still visible in the answer.
`minRole` comes from the permission table itself, so it cannot drift from what the route enforces.

```bash
curl -sS -X PUT https://api.preview.example.com/api/settings/max_jobs \
  -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
  -d '{"value": 8}'
```

A write answers with the row it stored, `"stored": true`, and — for `max_jobs` — a note repeating
what lowering the cap does not do. **Lowering it kills nothing**: jobs already running run to
completion and the new cap applies to the next dispatch. A refused value is a `400` naming the key.

**Storing a value is a decision to stop following the environment, and there is no API to undo it.**
`PUT` is the only write these keys have — there is no `DELETE`, and an empty value is refused — so a
host that has stored a `max_jobs` goes back to obeying `PSTACK_MAX_JOBS` only by removing the row
itself (`DELETE FROM settings WHERE key = 'max_jobs'` against `<PSTACK_DATA>/db/pstack.db`, with the
control container stopped). Changing your mind about the *value* is a second `PUT`; changing your
mind about the *layer* is not something the API does.

The two tiers are different on purpose. `max_jobs` is operational and sits with the rest of the
host's configuration, at `maintainer`. `default_role` is **user management by another name** — it
decides what every account created without an explicit role can reach — so it sits with the
promotion paths, at `admin`. It never widens what the *caller* may do: creating accounts is already
admin-only, and `POST /api/auth/bootstrap` is deliberately unaffected (the first account on a host is
an admin because there is nobody to promote it).

### HTTP API

`:id` is a **registry id** (e.g. `pr-123`), never the resolved stack name. **Every route requires a
principal** — reads included, since 0.10.0 — except `/api/health`, the login/bootstrap routes and the
two SSO legs, which are how you become one. A session cookie is what lets the log stream use
`EventSource`, which cannot send headers. Since 0.31.0 a principal is not enough on its own: each
route also names a **least role** — [7e](#7e-who-can-do-what-the-four-roles-0320) is the matrix, and
anything it does not list is the root token's alone.

| Method | Route | Body / query | Returns |
|---|---|---|---|
| GET | `/api/health` | — | `{ ok, authEnforced, hasUsers, sso, dataDir, version }` — `sso` is `{ providers: [{ key, label, preset }…] }` (enabled providers, in key order) or `null`, read by the login page before authenticating |
| GET | `/api/deployments` | spec variables as `?K=V`, **optional** | `{ deployments: [{ …meta, stack, busy, running, unresolved? }] }`. A row whose variables were not supplied degrades to `stack: null` + `unresolved: <reason>` rather than failing the listing; `busy`/`running` are `null` when undeterminable |
| GET | `/api/probe/:id` | — | **No token.** The upstream's own status, **no body ever**, and `x-pstack-probe: upstream\|unknown\|asleep\|no-target\|unresolved\|unreachable\|busy`. `?service=` names which one on a stack that publishes several. See [Probe a preview without a token](#probe-a-preview-without-a-token-0340) |
| GET | `/api/deployments/:id` | spec variables as `?K=V`, **required** | `{ id, kind, createdAt, updatedAt, stack, busy, compose, requires, axes[{name,hooks}] }` — hook **names**, never bodies |
| PUT | `/api/deployments/:id` | `{ spec, compose?, env? }` — **body only**, the query string is *not* read here | `{ id, kind, stack, createdAt, updatedAt }`, plus `swarmNotes` under swarm — what the conversion will change, [named at submit time](#submitting-a-deployment) rather than in the deploy transcript. Omitted, never `[]`, when nothing was checked · **201** new · **200** replaced · 400 bad spec/body · 409 while a job is in flight. The spec is **parsed before it is stored**, so its variables must be in the body's `env` or the submit is a 400 |
| DELETE | `/api/deployments/:id` | spec variables as `?K=V` | forget it. Refused while containers exist **and** when Docker did not answer |
| POST | `/api/deployments/:id/up` | spec variables as `?K=V` | **202** `{ job }` — `state: "queued"` when the stack is busy |
| POST | `/api/deployments/:id/down` | `{ verify?, force? }` (`verify` defaults true) | **202** `{ job }` — **preempts**: cancels what is running, drops what is queued · 409 on `kind: shared` without `force` |
| POST | `/api/deployments/:id/verify` | spec variables as `?K=V` | **202** `{ job }` — `state: "queued"` when the stack is busy |
| POST | `/api/deployments/:id/sleep` | spec variables as `?K=V` | **202** `{ job }` — queued when busy · 409 on `kind: shared` · 400 without a compose section. Compose project down, volumes and axes kept |
| POST | `/api/deployments/:id/wake` | spec variables as `?K=V` | **202** `{ job }` — `up`, recorded as a wake |
| POST | `/api/deployments/:id/cancel` | — | **200** `{ cancelled: [job…], by, warning }` — stops the running job AND the queued one. `cancelled` is `[]`, never null. The warning appears only when something had actually started |
| GET | `/api/config` | — | the whole portable configuration in **plaintext**: password hashes, token hashes, host secrets, notifier secrets, the SSO client secrets, registry logins. **Root token only** — an admin session or personal token is `403`. `cache-control: no-store`. Emits `config.exported` |
| POST | `/api/config` | that document | applies it create-or-skip → `{ trusts, created, skipped }` · 400 on a document this build does not understand · 403 for anything but the root token · 413 over 8 MiB. Emits `config.imported`, **including when it fails part-way** |
| GET | `/api/settings` | — | `{ settings: [{ key, value, source, minRole }…], env, precedence }` — the two runtime settings, resolved, each saying which layer it came from. `viewer`. See [Runtime settings](#runtime-settings-0330) |
| PUT | `/api/settings/max_jobs` | `{ value }` | the row + `stored` + a `note`. **`maintainer`.** In force immediately — no restart; lowering it cancels nothing already running · 400 on anything but a whole number ≥ 1 |
| PUT | `/api/settings/default_role` | `{ value }` | the row + `stored`. **`admin`** — it decides the role of accounts created without one · 400 on an unknown role |
| POST | `/api/users` | `{ username, password, email?, role? }` | **201** `{ user }`. **Admin only** (0.31.0), and an absent `role` means the host's `default_role` setting — `viewer` until somebody sets one. This route honours `admin` if that is the host default, unlike an inheriting SSO provider which is capped below it: creating accounts is already admin-only, so an admin minting at a level they chose is exercising authority they have, not a stranger acquiring it. See [7e](#7e-who-can-do-what-the-four-roles-0320). An unknown role is a 400. The optional `email` is what lets an SSO login adopt this account instead of creating a second one |
| PATCH | `/api/users/:id` | `{ role }` | `{ updated, role }` · **admin only** · 400 on an unknown role or on demoting the last admin · 404. Takes effect on that account's next request |
| DELETE | `/api/users/:id` | — | `{ deleted }` · **admin only** · 400 on the last user or the last admin · 404 |
| PUT | `/api/users/:id/password` | `{ password }` | `{ ok, revokedSessions }` — **your own** at any role, **anybody else's** is admin. Revokes that account's sessions and personal tokens |
| POST | `/api/deployments/:id/share` | `{ views?: ["details","logs"], ttl?: "7d" }` | **201** `{ url, token, views, expiresAt }` — a read-only link; 400 with no `PSTACK_TOKEN` to sign with, or a ttl over `30d` |
| GET | `/api/auth/sso/start` | `?provider=<key>&next=<same-origin path>` | **302** to that provider, with PKCE. Keyless: one enabled provider is picked, several land on `/login?sso_error=…` naming the keys, none says so too. No auth — this *is* how you sign in |
| GET | `/api/auth/sso/callback` | `?code=&state=` (the provider's redirect) | **302** with a session cookie, or **302** to `/login?sso_error=…` — completed against the provider the state was minted for. No auth |
| GET | `/api/sso/config` | — | `{ providers: [{ key, config, secretSet, updatedAt }…], callbackUrl, presets[] }` — the secret has **no read path**, a read learns `secretSet` and nothing else |
| PUT | `/api/sso/config` | `{ key, …config, clientSecret }` | `{ ok, key, config, callbackUrl }` · 400 on a bad field, an unreachable issuer, or a keyless body when several providers exist. Submitting the mask keeps that key's stored secret |
| DELETE | `/api/sso/config` · `/api/sso/config/<key>` | — | forget that provider; the accounts it created stay · bare with several providers is 400 naming the keys · 404 if none |
| GET | `/api/swarm` | — | `{ reachable, active, nodeId, managerAddr, nodes[], ports[], note }` — never the join token |
| GET | `/api/swarm/join` | `?format=token\|command\|script\|cloud-config[&distro=]` | `text/plain`, **maintainer and up** (it was admin before 0.31.0); 409 when this daemon is not a manager |
| GET | `/deployments/:id/public-logs-view` | `?token=<jwt>` | the page a share link opens (no auth — the token is on the page's own API calls) |
| ANY | `<preview hostname>/*` | the `Host` header, via the catch-all router | a sleeping **or waking** stack's hostname: **503** + `Retry-After: 5` + `x-pstack-wake: 1` + the wake page. A sleeping one also starts a wake job; a waking one is held until readiness settles ([7b](#sleep-and-wake-on-call)) |
| GET | `/api/jobs` | — | `{ jobs: [...] }`, newest first, max 50, in-memory |
| GET | `/api/jobs/:jobId` | — | `{ job }` · 404 |
| GET | `/api/jobs/:jobId/stream` | — | SSE: buffered log replay, then live, then `{done:true,state}` |
| GET | `/` and **any** non-`/api/` path | — | the embedded single-page UI. No filesystem lookup, so a deep link renders rather than 404s |

Status codes: **202** accepted · **400** bad spec or missing variable · **401** unauthorized ·
**403** a credential the route admits, doing something it may not — a role below the route's least
one (the body names both), a share link outside its views, or an admin at `/api/config` ·
**404** unknown deployment/job · **405** wrong method on an action · **409** job in flight for that
stack, or a `kind: shared` `down` without `force` · **500** unexpected.

Job `state`: `queued` · `running` · `ok` · `failed` · `leaked` · `cancelled` · `superseded` — the
[seven](#read-a-job), of which only the first two are not final. Job `action`: `up` · `down` ·
`verify` · `sleep` · `wake`.

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
  orchestrator: swarm                # optional; `swarm` or `compose`. Default: PSTACK_ORCHESTRATOR, else compose

sleep:                            # optional; the scheduler puts the compose project to sleep (volumes + axes stay)
  idle: 2h                        # no request through Traefik for this long
  after: 3d                       # this long after the last deploy, unconditionally

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
| `compose.orchestrator` | `compose` or `swarm`; anything else is an error. Resolution: the spec, then `PSTACK_ORCHESTRATOR`, then `compose`. Under `swarm` the file is converted on every invocation — see [Swarm mode](#swarm-mode). |
| `sleep` | a mapping with `idle` and/or `after` (durations `90s` `30m` `2h` `3d` `1h30m`); anything else is an error, and an empty block is too. Never honoured on `kind: shared`. Without a `compose:` section it is a warning — there is nothing to put to sleep. |
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
