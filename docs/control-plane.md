# The control-plane model

`pstack` started as a CLI you point at one `preview.yml`. It is becoming a **control plane**: a
long-lived API that holds *many* submitted deployments and acts on them by id, so one host serves
several projects and many PRs at once.

That change introduces exactly one hard architectural rule, and this document exists to state it:

> **The CLI owns the control stack. The running API owns everything else.**

Read [`../README.md`](../README.md) first — this assumes you know what an axis is and why `down` is
best-effort while `verify` is strict.

### What is built today

This document describes the decided architecture, and the code is landing under it. Every section
marks anything not yet reachable. The short version:

| Piece | Where | State |
|---|---|---|
| `kind: shared \| isolated` | `src/spec.ts` | **built** — parsed, defaulted to `isolated`, `shared`+axes is a hard error |
| The `shared` guard on `down` | `src/stack.ts` | **built** — refuses without `force` |
| `--force` | `src/cli.ts` | **built** |
| `requires:` preflight | `src/spec.ts`, `src/stack.ts` | **built** — asserted before anything is created |
| The deployment registry | `src/registry.ts` | **built** |
| `/api/deployments/*` routes | `src/api.ts` | **built** — the API is registry-backed; the old single-spec `/api/spec` and `/api/stacks` routes are gone |
| `pstack init` | `src/init.ts` + `templates/control/` | **implemented and dispatched** — `pstack init --domain … --acme-email …`; DNS-01 adds `--challenge dns01 --dns-provider …` |
| HTTP-01 / DNS-01 challenge modes | `src/init.ts`, `templates/control/` | **built** — HTTP-01 is the default; `init` renders the two `#__MARKER__` blocks per mode (§TLS) |
| The published bundle | `scripts/build.ts`, `package.json` | **built** — `dist/cli.js` + `dist/index.js`, `bin` → `dist/cli.js` (§Distribution) |
| `pstack self-upgrade` | — | *not built* — it is `init` re-run after a fetch (§7) |

`src/api.ts`'s file header is the authoritative route list. Where this document and that header
disagree, the header is right.

---

## 1. The two layers

```
  host  ──  systemd unit, or an operator at a shell
    │
    │   pstack init          ← runs ON THE HOST. Never inside a container.
    ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  CONTROL stack                                                           │
│                                                                          │
│    traefik       owns :80/:443, terminates TLS for the whole host        │
│                  (one wildcard cert under DNS-01; one cert per           │
│                   hostname under the HTTP-01 default — see §TLS)         │
│    pstack        the HTTP API: holds the registry, runs the jobs,        │
│                  and serves the dashboard as static files                │
│                    control.<domain>   the web UI                         │
│                    api.<domain>       the API — same container           │
│                                                                          │
│  Lifecycle: created once by `pstack init`, upgraded from the host.       │
└────────────────────────────────┬─────────────────────────────────────────┘
                                 │ manages  (via the Docker socket)
             ┌───────────────────┴────────────────────┐
             ▼                                        ▼
  ┌────────────────────────┐            ┌──────────────────────────────┐
  │  SHARED deployments    │            │  ISOLATED deployments        │
  │  kind: shared          │◀── requires┤  kind: isolated              │
  │                        │            │                              │
  │  a database, a queue,  │            │  pr-123, pr-124, pr-131 …    │
  │  a registry mirror     │            │                              │
  │  <service>.<domain>    │            │  <surface>-pr-<n>.<domain>   │
  │  no axes               │            │  axes, proven gone on `down` │
  │  `down` is guarded     │            │  ephemeral by design         │
  └────────────────────────┘            └──────────────────────────────┘
```

Everything below the dashed line is data the API acts on. Everything above it is the API itself,
and the API must not touch it.

### Hostnames

| Hostname | Serves | Owned by |
|---|---|---|
| `control.<domain>` | the web UI | the control stack (`traefik.http.routers.pstack-ui`) |
| `api.<domain>` | the HTTP API — CI, `curl`, scripts | the control stack (`…routers.pstack-api`) |
| `<service-name>.<domain>` | a shared service's own hostname, by convention | the shared deployment's own compose labels |
| `<surface>-pr-<n>.<domain>` | one surface of one PR | the isolated deployment's own compose labels |
| `*.<surface>-pr-<n>.<domain>` | a whole subtree at one profile — **opt-in**, see below | your labels, using a rule pstack computes |

**Two routers, one container.** `control.` and `api.` both point at the same `pstack` service,
because the API process serves the UI. The routers are separate only so that external callers get an
honest name: `api.<domain>` is a stable handle for CI that does not read as "the UI host", and it can
be firewalled, rate-limited or fronted differently from the browser surface without moving anything.

**The UI calls the API with relative `/api/…` paths — deliberately.** A page loaded from
`control.<domain>` therefore talks to `control.<domain>/api/…`, which is **same-origin**: no CORS
preflight, no `Access-Control-Allow-*` to maintain, no cross-origin cookie rules, and nothing to
reconfigure when the domain changes or a second hostname is added. Hard-coding
`https://api.<domain>` into the UI would buy nothing and cost a CORS policy on an API that can
destroy infrastructure.

Per-PR hostnames are **flat and dash-separated**. A wildcard certificate — and a wildcard DNS
record — matches exactly **one** label: `backend-pr-1.<domain>` is covered, `backend.pr-1.<domain>`
is not. Nesting the labels is a silent break, because DNS resolves and only TLS fails.

*(The pre-0.1 UI hostname `pstack.<domain>` is gone.)*

### Wildcard subdomains under a surface

An app that routes by subdomain — per-tenant hosts, a preview inside a preview — needs everything
under `backend-pr-123.<domain>` to reach the backend. Declare it per profile:

```yaml
compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]
  subdomains: [backend]            # or: { backend: any }, or { backend: { host: … } }
```

pstack does not write your Traefik labels — your compose file owns those — so what this produces is
an **environment variable holding the rule**, which your own label interpolates:

```yaml
    labels:
      - traefik.http.routers.backend-wild.rule=${PSTACK_WILD_BACKEND}
      - traefik.http.routers.backend-wild.priority=2
      - traefik.http.routers.backend-wild.service=backend
```

Why an env var and not a generated overlay, a file-provider drop-in, or a sidecar: the reasoning is
in `src/subdomains.ts`. Short version — this seam needs no new mount, no re-`init`, and nothing
pstack writes can break another deployment's routing.

**A hardcoded host always wins.** Traefik's default router priority is the rule's *length*, so an
exact `Host(…)` scores in the dozens against the wildcard's pinned `2`. `2` rather than `1` so it also
clears the `preview-fallback` catch-all.

#### The two ceilings, which are not pstack's to lift

`depth` defaults to `one` — a single label — because that is what the rest of the stack can actually
deliver. `any` matches any depth and is available deliberately, but:

| | `depth: one` | `depth: any` |
|---|---|---|
| Traefik routing | works | works |
| **DNS** | a normal `*.<host>` record | needs a resolver that answers at arbitrary depth. `*.*.<host>` is **not a valid record** — most managed DNS cannot do this at all; a self-hosted authoritative server can |
| **TLS** | needs a cert for `*.backend-pr-123.<domain>` → **DNS-01 only** (HTTP-01 cannot issue wildcards), and **one cert per PR** against the ~50/registered-domain/week ceiling | **impossible.** `*.*.<host>` is not a legal SAN, so no certificate can ever cover it |
| Net result | HTTPS, at a real per-PR certificate cost | **HTTP only, permanently** |

So `any` is for a host whose DNS you control and where plain HTTP is acceptable. Reaching for it
because the wildcard "should" be recursive gets you hostnames that resolve, route, and then fail the
TLS handshake — the most confusing of the three failures to diagnose, because two thirds of the path
looks healthy.

---

## TLS: two challenge modes, opposite per-PR rules

Traefik terminates TLS for every hostname on the host, and how it *proves* the domain is an `init`
choice with architectural consequences. **HTTP-01 is the default**; DNS-01 is opt-in:

```bash
pstack init --domain preview.example.com --acme-email <you>@example.com
pstack init --domain preview.example.com --acme-email <you>@example.com \
            --challenge dns01 --dns-provider cloudflare      # token via PSTACK_DNS_TOKEN
```

`--dns-provider` is required **only** for `dns01`; `init` rejects the combination without it.
`init` renders the two `#__MARKER__` blocks in `templates/control/docker-compose.yml` per mode
(`acmeChallengeArgs` / `acmeRouterLabels` in `src/init.ts`) and leaves the rest byte-for-byte —
Compose cannot conditionally include CLI arguments, which is why these two blocks are generated and
nothing else is.

| | **HTTP-01** (default) | **DNS-01** |
|---|---|---|
| Credential | **none** | a DNS API token per provider (or an instance identity) |
| Hard requirement | **port 80 reachable from the internet** | the token can write TXT records in the zone |
| Wildcard | **impossible** — Let's Encrypt does not offer HTTP-01 for `*.<domain>` | **one** `*.<domain>` + apex covers everything |
| Certs issued | one **per hostname** | **one**, total |
| Valid before the stack is deployed | **no** — the challenge needs something already answering on that hostname | **yes** — the wildcard exists before any PR does |
| Practical ceiling | ~50 new certs / registered domain / week ÷ 3 surfaces per PR ≈ **16 new PRs/week** | none from issuance |

### HTTP-01 and the `web` → `websecure` redirect

The control stack redirects all of `:80` to HTTPS
(`--entrypoints.web.http.redirections.entrypoint.to=websecure`), which looks like it should break an
HTTP-01 challenge served over port 80. It does not: **Traefik installs an internal ACME router at
maximum priority that bypasses the redirect for `/.well-known/acme-challenge/`.** You do not add a
middleware, an exclusion, or a second entrypoint. The two flags are all of it:

```
- --certificatesresolvers.le.acme.httpchallenge=true
- --certificatesresolvers.le.acme.httpchallenge.entrypoint=web    # must be the :80 entrypoint
```

The one hard requirement is that **port 80 is reachable from the internet**. A firewall that admits
only 443 — a reasonable-looking hardening step — makes every issuance fail with a validation error
that says nothing about the firewall.

### Why the ceiling is the reason to move to DNS-01

Let's Encrypt allows roughly **50 new certificates per registered domain per week**. Renewals do not
count against it; the separate **duplicate-certificate limit is 5 per week** and applies to
re-issuing the *same* set of hostnames.

Because HTTP-01 cannot issue a wildcard, each preview hostname is a *new* certificate. With three
surfaces per PR — backend, frontend, admin — that is:

```
50 new certs/week ÷ 3 certs/PR ≈ 16 new PRs/week
```

PR 17 in a busy week gets a TLS error, not a preview. Nothing degrades gracefully: the limit is
per registered domain, it resets on a rolling week, and the failure surfaces as an unrelated-looking
handshake error on a stack that deployed fine. That arithmetic — not elegance — is when you switch.

The second reason is sequencing. HTTP-01 cannot certify a hostname **before its container exists**,
so a preview URL is not valid until the stack is actually deployed; a PR mid-build serves a TLS
error that reads like a certificate problem and is really an ordering problem. A DNS-01 wildcard is
valid immediately, so the URL can be posted to the PR before the deploy finishes.

### The per-PR router rules are OPPOSITE per mode

This is the easiest thing in the whole system to get wrong, because the correct label set in one
mode is the rate-limit bug in the other.

| | **HTTP-01** | **DNS-01** |
|---|---|---|
| Per-PR router labels | `tls=true` **and** `tls.certresolver=le` | `tls=true` **and nothing else** |
| Who requests a cert | **every** router, for its own hostname, on first HTTPS request | **exactly one** always-on router |
| `tls.domains[0].main` / `.sans` | not used | on that one router only: `${DOMAIN}` / `*.${DOMAIN}` |
| How other routers get TLS | they don't inherit — each resolves its own | **inherit the wildcard by SNI** |

Under **HTTP-01**, a per-PR router *without* `certresolver` has no certificate and no way to obtain
one — the hostname serves a TLS error.

Under **DNS-01**, a per-PR router *with* `certresolver` makes Traefik order a **separate**
certificate for that hostname, silently converting the one-wildcard design back into
one-cert-per-hostname and burning the ~50/week limit. The stack looks healthy the whole time. In
this template the single always-on requester is the `pstack-ui` router; every other router on the
host — shared and per-PR alike — sets `tls=true` and stops there.

If you switch modes on an existing host, **the per-PR compose labels must change too.** `init`
re-renders the control stack; it has no reach into your deployments' label sets.

### DNS records

Both modes want the same DNS: a wildcard `*.<domain>` plus the apex, pointed at the host. HTTP-01
changes what gets *certified*, never what *resolves* — without the wildcard record a per-PR
hostname does not resolve at all, and the challenge cannot even be attempted. Remember the wildcard
matches one label: flatten per-PR hostnames with dashes.

### DNS-01 credentials

`init` writes the token to `control/dns.env` (mode `0600`) under the variable name lego expects,
loaded by Traefik via `env_file:`. It is a **separate secret from `PSTACK_TOKEN`**, with a
deliberately different blast radius: this one can edit a DNS zone, `PSTACK_TOKEN` can start
privileged containers on the host.

| Provider (`--dns-provider`) | Variable | Notes |
|---|---|---|
| `cloudflare` | `CF_DNS_API_TOKEN` | one token, scoped **Zone:Read + DNS:Edit** |
| `hetzner` | `HETZNER_API_TOKEN` | optional `HETZNER_PROPAGATION_TIMEOUT`, `HETZNER_TTL`. **Never `HETZNER_API_KEY`** — that name is not read, and the symptom is an ACME "propagation timeout" that sends you debugging DNS instead of a typo |
| `route53` | *(none)* | tokenless — satisfied by the IAM **instance profile** |
| `gcloud` | *(none)* | tokenless — satisfied by the VM's **attached service account** |

Any lego variable also accepts a `_FILE` suffix if you would rather mount the secret than write it.
**Prefer the tokenless providers where you have the choice:** nothing to store, nothing to rotate,
nothing to leak. A provider `init` does not recognise gets a `CHANGEME_VARIABLE_NAME=` line pointing
at [lego's provider list](https://go-acme.github.io/lego/dns/) rather than a guessed variable name —
for the same reason as the `HETZNER_API_KEY` note above.

Traefik's `acme.json` lives in the `letsencrypt` volume. **Back it up.** Losing it means re-issuing,
and re-issuing into a rate limit at the wrong moment means no TLS at all for hours — a risk that is
per-hostname-sized under HTTP-01 and whole-host-sized under DNS-01.

---

## 2. The invariant: the API never manages its own stack

**The API must never run `up`, `down`, or `verify` against the deployment that contains it.** This
is not a policy that can be relaxed with a flag; it is a property of what the operation does.

### Why — the self-upgrade hazard, in full

Consider the API accepting `POST /api/deployments/pstack/up` on its own stack. Step by step:

1. The HTTP handler starts a job. The job's work function lives in the process inside the
   `pstack` container.
2. `up` runs `docker compose -p pstack-control … up -d`, which recreates changed services. The
   `pstack` service is one of them.
3. Docker stops the old container. **The process performing the upgrade is killed mid-operation**,
   at whatever step it had reached.
4. The job that was tracking the operation dies with it. Job state is in-memory and unpersisted by
   design (see `src/jobs.ts`) — so there is no record of how far it got, no transcript, and the
   stack lock it held is gone with the process that held it.
5. The HTTP connection streaming the log drops. The operator sees a truncated SSE stream and cannot
   tell "finished successfully" from "died halfway".

That is the *good* case, where the new image is fine. The bad case is worse:

6. If the new image is broken — bad config, a missing mount, a port already bound, a spec the new
   build rejects — the new container crash-loops. There is now **no API**. The thing you would use
   to roll back is the thing that is down.
7. And there is no remote path back. The control plane *is* the remote interface. Recovery requires
   SSH to the host, which is precisely the access the control plane existed to avoid needing.

A half-applied `compose up` on any other deployment is recoverable by asking the API to try again.
A half-applied `compose up` on the control stack removes the ability to ask.

The same argument applies with more force to `down`: `down` runs `compose down -v`, which would
delete the control stack's volumes — Traefik's `acme.json` (every certificate for the host, and
Let's Encrypt rate limits are per-week) and the registry's data directory (every submitted spec).

### The consequence

`pstack init` and `pstack self-upgrade` run **on the host**, from the CLI, as a systemd unit or an
operator at a shell. They are the only things that touch the control stack, and neither is reachable
over HTTP. The host keeps one capability the API does not have, and that asymmetry is the recovery
path.

Two rules follow — and note the first is **operator discipline, not an enforced guarantee**:

- **Never submit the control stack to the registry.** Nothing in the code stops you: a `PUT` whose
  spec resolves to compose project `pstack-control` is accepted like any other, and `POST …/up` on
  it would do exactly what §2 describes. It cannot be enforced because **the API cannot reliably
  know its own deployment id** — it is a process in a container, with no trustworthy handle on which
  registry entry (if any) produced it. So do not reach for a `protected: true` flag as the fix; a
  flag that can be omitted or mistyped would give the appearance of a guard over the same hazard.
  The real guard is that `init` lives on the host and the control stack has no reason to be
  submitted.
- **A `requires:` may assert the control plane is healthy; it may never manage it.** Asserting is a
  read.

---

## 3. Three layers, three lifecycles

| | **control** | **shared** | **isolated** |
|---|---|---|---|
| What it is | Traefik + the `pstack` API/UI container | a host singleton every tenant borrows: a database, a queue cluster, a registry mirror | one tenant — normally one PR |
| Hostnames | `control.<domain>`, `api.<domain>` | `<service-name>.<domain>` | `<surface>-pr-<n>.<domain>` |
| Declared as | not a spec the API holds — it is not in the registry at all | `kind: shared` | `kind: isolated` (the default) |
| Created by | `pstack init`, on the host (`src/init.ts`, dispatched from `src/cli.ts`) | the API, or the CLI | the API, or the CLI, usually from CI |
| Lifecycle | once per host; upgraded rarely, deliberately | once per host; lives indefinitely | constantly created and destroyed |
| Axes allowed? | n/a | **no** — a hard `SpecError`. Axes isolate one tenant from another and prove a tenant's resources are gone; a singleton has neither concern | **yes** — that is the point |
| `down` guarded? | **not offered at all** | **yes** — refused unless explicitly forced (`--force`, or `{"force":true}` in the request body) | no — routine |
| `verify` means | n/a | little: there is nothing you want to assert is gone | the leak gate |
| Who may destroy it | the host operator, by hand | anyone who says so **explicitly**: `--force` on the CLI, or `{ "force": true }` in the `POST …/down` body. Without it the API answers **409 synchronously** — no job is started, so the reason is in the response rather than buried in a transcript | anyone who can reach the API |

### Why the `shared` guard exists

`down` runs `docker compose down -v`. On an isolated deployment that is routine — the volumes are
the tenant's own. On a shared deployment the *same verb* destroys the TLS store, the shared
database, the admin credentials — state every other deployment on the host depends on.

The verb is identical, the blast radius is not. So `kind` is **explicit in the spec rather than
inferred from "does it have axes"**: a misconfigured isolated deployment that forgot to declare its
axes must not silently inherit a singleton's protection.

```
$ pstack -f shared.yml down
stack: shared-db
refusing to tear down shared deployment "shared-db"
  ✗ compose      shared-db  — refused: kind is `shared`. `down` removes volumes (-v), which on a
    shared deployment destroys state every tenant depends on. Re-run with --force if that is truly
    intended.                                                                    # exit 1
```

The guard lives in `down()` in `src/stack.ts`, so a direct library caller cannot bypass it either.
`src/api.ts` *also* checks it before starting a job — deliberate duplication, so the HTTP caller
gets a `409` with the reason instead of a job id that is going to fail — and then passes `force`
straight through rather than consuming it.

### Why `requires:` exists

An isolated deployment usually depends on shared ones — an ingress network, a reachable queue,
a database endpoint. Without a preflight, a missing dependency surfaces partway through an axis
hook as whatever error that CLI happened to print, which tells you nothing about what is actually
wrong.

`requires:` is asserted **before anything is created**, so the failure names the dependency:

```yaml
requires:
  - name: ingress-network
    assert: docker network inspect preview-ingress >/dev/null 2>&1
    hint: run `pstack up` on the shared deployment first, or `docker network create preview-ingress`
```

Write the `hint` as *how to fix it*, not *what broke*. The assert already says what broke.

---

## 4. The registry

> **Built and wired.** `src/registry.ts` backs the `/api/deployments/*` routes (§6).

The CLI acts on one spec file you point it at. The control plane holds many, addressed by id:

```
$PSTACK_DATA/deployments/<id>/
    spec.yml       the submitted spec
    compose.yml    the submitted compose file, when one was sent with it
    meta.json      { id, kind, createdAt, updatedAt }
```

`$PSTACK_DATA` comes from `PSTACK_DATA`, defaulting to `/var/lib/pstack`.

| Method | Contract |
|---|---|
| `list()` | `DeploymentMeta[]`, newest-updated first |
| `get(id)` | `Deployment \| null` — meta plus `dir` and `specPath` |
| `put(id, specYaml, { composeYaml?, env? })` | create or replace. **Parses before it commits**, and `rm -rf`s the directory if the spec is rejected, so a malformed submission never leaves a half-created deployment behind. `kind` is read from the parsed spec, never from the caller — the file is the authority. |
| `remove(id)` | **forgets only.** Never tears anything down. |
| `resolve(id, env)` | parse the stored spec with these variables → `Stack` |

Ids must match `/^[a-z0-9][a-z0-9._-]{0,63}$/` and may not contain `..`. They become directory names
and are echoed into shell hooks through the resolved stack, so the alphabet excludes traversal,
whitespace and shell metacharacters. `assertValidId` throws `RegistryError`; rejecting there is the
difference between a `400` and a path-traversal write.

### Why a directory of YAML and not a database

**The registry is a cache of intent, never the source of truth about what exists.** Truth lives in
Docker and in each axis's own `assert_*` probe — the same rule the CLI has always followed, and the
reason there is no reconciliation loop to write.

That reframes what a lost registry costs. It is not "the host is now inconsistent"; it is "I forgot
what you asked for". Nothing drifts, because nothing was claiming to be authoritative. Recovery is
re-submitting the spec, which CI can do unattended. A directory of YAML is also greppable,
diffable, `tar`-able and readable with `cat` at 2am — none of which is true of a sqlite file.

The one thing that *is* dangerous is forgetting a deployment while its containers still run:
`remove(id)` would orphan them beyond the control plane's view, which is exactly the leak this
project exists to prevent. Hence the rule the wiring must preserve:

> **`remove` requires a successful `down` first.** `Registry.remove` does not enforce it — it
> cannot, it has no runner. `DELETE /api/deployments/:id` does, and fails closed: it refuses while
> containers exist *and* when Docker did not answer (§6).

### What is not in the registry

Job records. They stay in memory, capped at 50, lost on restart (`src/jobs.ts`), because a job is
the transcript of an *attempt*, not a fact about the host. Restarting the API loses history, not
correctness — and it clears the per-stack busy locks, which is the desired behaviour after a crash.

---

## 4b. Traefik's dynamic configuration

Traefik reads two providers: **docker** (labels on containers — every per-PR router) and **file**, a
watched *directory*. The file provider is where everything that is not a container lives: middleware
(basicAuth, rate limits, IP allow-lists), TLS options, the catch-all fallback router, and routes to
something running outside compose. Several files rather than one is the point of watching a directory
— one file per concern, each editable on its own.

`init` creates the directory; from **0.4.0** the API also mounts it read-write, so it can be managed
from the UI (`GET/PUT/DELETE /api/routing[/:name]`) instead of over SSH. An existing host picks the
mount up by re-running `pstack init`, and until it does the API reports `writable: false` and the UI
says so rather than failing one write at a time.

### The blast radius, and the one thing it cannot touch

Traefik's documented behaviour: **an unparseable file is a parse error for the whole directory**, and
the rest of the directory can be discarded with it. So the symptom of a careless edit is not the file
you were editing — it is *other* routes disappearing. Three defences, all in `src/routing.ts`:

| Defence | Why |
|---|---|
| Parse + require a known top-level section before writing | `htttp:` is valid YAML that configures nothing, and Traefik will not tell you. A typo'd section is refused with the list of real ones. |
| Write a temp file and `rename` it into place | `writeFile` truncates then fills, and the watcher can fire in between. `rename` is atomic within a filesystem, so Traefik only ever sees a whole file. |
| Nothing but dynamic config in that directory — no `.bak`, no leftover temp | Traefik would try to parse it. This is why there is **no on-disk history**: the obvious place to keep it is the one place it must not go. A write returns the previous content instead, as an in-session undo. |

**It cannot lock you out.** `control.<domain>` and `api.<domain>` are docker labels on the pstack
container, not file config — so no file written here can break the surface you would use to undo it.
That is a property worth preserving deliberately, not a coincidence.

Reads of a file's *contents* need the token (basicAuth hashes, forwardAuth URLs); the file *list* does
not, so the page renders before you authenticate.

---

## 5. Trust boundary

**Accepting a spec over HTTP is accepting arbitrary shell, by design.**

A spec carries axis hooks and `requires` asserts. Every one of them is a shell string run through
`bash -c` with the resolved environment injected (`src/exec.ts`). That is deliberate: this tool
orchestrates other people's CLIs — `docker`, `gcloud`, `curl`, `psql` — and a structured arg-array
API would only move quoting into YAML. A spec sits at the same trust level as a CI workflow file.

Which means: **an endpoint that accepts a spec is a remote code execution endpoint.** Not
"potentially, if there is a parser bug" — *by construction, when working exactly as intended.*
Sanitizing, escaping or allow-listing hook content is a category error, not a hardening task.

The threat model this is safe under, and only this one:

- **One trusted operator**, or one trusted CI system, holding a bearer token.
- **Not multi-tenant.** One Docker socket, one host, one trust level. Every caller who can reach the
  API can do everything any other caller can. There are no tenant ids, no per-user auth, no RBAC —
  and adding them without a real per-tenant isolation boundary (separate VMs/microVMs, or Kubernetes
  namespaces) would create the *appearance* of a boundary where none exists, which is worse than
  having none.
- **The Docker socket mount is root-equivalent on the host.** `pstack` works by shelling out to
  `docker compose`; anyone who can drive it can start a privileged container and own the box. The
  service user being in the `docker` group *is* root, with extra steps.

Three guards follow, two of which are enforced in code:

| Guard | Where | Effect |
|---|---|---|
| Bearer token on every mutating route | `src/api.ts` | `POST`/`DELETE` without `Authorization: Bearer <PSTACK_TOKEN>` → `401` |
| Loopback interlock | `src/cli.ts`, `serve` | with no `PSTACK_TOKEN` the host is forced to `127.0.0.1`, **and** an explicit non-loopback `PSTACK_HOST` is a hard exit `3` rather than a silent downgrade |
| Ingress auth | yours | **`GET`s are unauthenticated even when a token is set.** `GET /api/jobs` — no id needed — returns every retained job transcript, including `outcome.outputs`, which holds **captured credentials by design**, and the first line of a failed hook's stderr; `GET /api/deployments` enumerates every deployment on the box. Writes are double-gated; reads are gated only by whatever sits in front. |

### The read side does not yet match this rule

The intended line is: an unauthenticated read may expose the **shape** of a deployment, never a value
that lets the reader authenticate to something. `GET /api/specs/:name` was brought in line in 0.2.4
(source needs a token; metadata does not). The **job reads have not been** — `outcome.outputs` is the
documented channel for passing a freshly-provisioned resource's connection string to a later axis, and
it is serialised in full to anyone who can reach the port.

Measured, options weighed, **decision deliberately deferred**:
[`secret-exposure.md`](secret-exposure.md). Until it is settled, the posture below is not merely
recommended — it is what the read side is relying on.

The default posture should be *not reachable from the internet*: keep `PSTACK_HOST=127.0.0.1`, set
`PSTACK_TOKEN` anyway (it protects against everything else already on the host), and tunnel:

```bash
ssh -N -L 7878:127.0.0.1:7878 preview@<host>
```

See [`bootstrap.md` §9](bootstrap.md#9-hardening-checklist) for the full checklist.

---

## 6. Submitting a deployment

`:id` is a **registry id**, not a compose project name. The server owns the stored spec and resolves
`stack:` itself, so a client can never point it at an arbitrary compose project on the host.

| Method | Route | Notes |
|---|---|---|
| `GET` | `/api/deployments` | every submitted deployment, plus `busy` and `running` |
| `GET` | `/api/deployments/:id` | meta + a **field-by-field** spec summary |
| `PUT` | `/api/deployments/:id` | submit or replace: `{ spec, compose?, env? }` → `201` new / `200` replaced |
| `DELETE` | `/api/deployments/:id` | forget it — **refused while containers still exist** |
| `POST` | `/api/deployments/:id/{up,down,verify}` | `202 { job }`; `409` if that stack is busy. `down` body: `{ verify?, force? }` |
| `GET` | `/api/jobs` · `/api/jobs/:id` · `/api/jobs/:id/stream` | transcripts, poll, SSE |

```bash
curl -sS -X PUT -H "Authorization: Bearer $PSTACK_TOKEN" \
     -H 'content-type: application/json' \
     -d "$(jq -n --rawfile s preview.yml --rawfile c docker-compose.preview.yml \
             '{spec:$s, compose:$c, env:{PR:"123"}}')" \
     http://localhost:7878/api/deployments/pr-123

curl -sS -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
     'http://localhost:7878/api/deployments/pr-123/up?PR=123'      # → 202 { job }
```

### Five behaviours the wiring is carrying, and why each exists

- **Variables ride on the query string, and are not persisted.** A spec resolves `${VAR}` once, so
  `stack: pr-${PR}` needs `?PR=123` on *every* call. Storing them would be worse: the value that was
  right at `up` time is not necessarily right months later, and a stale stored `PR` would make
  `down` tear down a different stack than `up` created. A missing variable is a `400` naming it —
  never a stack called `pr-` that every PR collides on.
- **`PUT` validates with `parseSpec` before anything touches disk.** `Registry.put` writes
  `spec.yml` first and `rm -rf`s the directory if the spec then fails to load — correct for a new
  deployment, catastrophic on a *replace*, where a typo would delete a good record while its
  containers keep running, now invisible to the control plane. Parsing the string first avoids that
  entirely.
- **`PUT` is refused (409) while that stack has a job in flight.** Swapping the spec mid-job means
  the eventual `down` tears down with different profiles and axes than `up` created — the same
  orphan class as deleting the record.
- **`DELETE` fails closed.** It refuses while containers exist, *and* refuses when Docker did not
  answer — "could not tell" is not evidence of absence. `Registry.remove` forgets only; it never
  tears anything down, and forgetting a live deployment orphans it beyond the control plane's view,
  which is precisely the leak this project exists to prevent.
- **Responses are built field by field, never spread.** `parseSpec` seeds a spec's variables from
  the whole ambient environment, so a resolved `Stack.env` holds every secret this process has —
  `PSTACK_TOKEN` included. The spec summary returns axis **hook names**, never hook bodies: a hook
  is a shell string that routinely carries an API token inline.

### Hooks in a submitted spec cannot use relative paths

A submitted deployment's runner has `cwd` set to its own directory — necessary, because the spec's
`compose: { file: compose.yml }` refers to the file `put` wrote next to `spec.yml`. That cwd applies
to **axis hooks too**, and only `spec.yml` and `compose.yml` ever live there. So `up: ./hooks/db.sh`
works from a CLI checkout and cannot work over the API: submitted hooks must be inline shell or
absolute paths.

The same cwd makes a relative bind mount (`./data:/data`) land *inside* the deployment directory —
the one `remove` deletes. That is safe only because `DELETE` refuses while containers exist, which
makes that guard load-bearing for data, not just for orphan visibility.

### The library path

```ts
import { Registry, dataDir } from './src/registry.ts';   // not re-exported from src/index.ts

const reg = new Registry(dataDir());                     // $PSTACK_DATA, default /var/lib/pstack
await reg.put('pr-123', specYaml, { composeYaml, env: { PR: '123' } });
const stack = await reg.resolve('pr-123', { PR: '123' });
```

---

## 7. Upgrade path

### `pstack init`

> **Implemented and reachable as `pstack init`** (`src/cli.ts` → `src/init.ts`). A host can still
> bring the control stack up by hand with `docker compose … up -d` — see
> [`bootstrap.md` §4](bootstrap.md#4-the-cloud-init-file) — but `init` is the supported path,
> because a hand-maintained compose file drifts from what the release expects.

`init` stands the control stack up **from the host**, into compose project **`pstack-control`**, and
is **idempotent** — re-running it is the supported way to change the domain, rotate the token, or
move to a new image, not a repair action. That matters because the alternative is a hand-maintained
compose file that drifts from what the release expects, which is how a control plane ends up
unupgradeable.

What it does, in order:

1. **Preconditions** — the Docker socket exists, the Compose v2 plugin is installed, the control
   image is present. Same shape and same reason as a spec's `requires:`: fail immediately, by name,
   before anything is created.
2. **State directories** — `<dataDir>/deployments` (the registry root) and
   `<dataDir>/control/traefik-dynamic` (the file provider's watched directory, created empty so the
   mount does not fail).
3. **The two external networks**, `preview-ingress` and `preview-shared`, `|| true` so a re-run is
   the idempotent path rather than an aborted one. **These names are a contract** with every per-PR
   compose file, which must declare both `external: true`.
4. **Configuration** — `templates/control/docker-compose.yml` copied byte for byte **except the two
   `#__MARKER__` lines**, which `init` replaces with the ACME challenge args and router TLS labels
   for the chosen mode (§TLS). Everything else is verbatim: the `${...}` are *Compose's*
   interpolation, resolved from `.env` at `up` time, and substituting them here would make pstack a
   second caller of interpolation. Plus `.env` and `dns.env`, both `0600`.
5. **`compose up -d --remove-orphans`**, then **wait for the container's HEALTHCHECK**. `up -d`
   exits 0 as soon as containers are *created*, so a crash-looping API would otherwise be reported
   as success.

Two secrets, deliberately separate: **`PSTACK_TOKEN`** (the API bearer token — it drives a
read-write Docker socket, i.e. root on the host) and the **DNS-01 credential** in `dns.env` (it can
edit one DNS zone). Reusing one for both would give whoever can edit DNS the ability to start
privileged containers. `init` generates `PSTACK_TOKEN` when unset and prints it once, because the
control stack binds `0.0.0.0` for Traefik and `serve` hard-exits on a non-loopback bind with no
token.

It re-runs safely for the same reasons `up` does anywhere else:

- `compose up -d` converges. Unchanged services are left alone; only changed ones are recreated.
- Host inputs are **mounted, not baked** — the DNS-01 credential, the Traefik dynamic directory.
  Re-running renders the stack, not your secrets.
- `<dataDir>/deployments` is untouched, so submitted deployments survive.

Nothing about `init` requires the API to be running, which is the whole point: it is the path that
still works when the API is down.

### Upgrading the control plane

```
1.  drain            GET /api/jobs — wait for terminal states
2.  install          bun add -g @samyx/preview-stacks  +  pull the new control image on the host
3.  pstack init …    re-render + `compose up -d` the control stack
4.  verify           curl -sf https://api.<domain>/api/health
```

Step 2 has **two** artifacts, and they upgrade independently: the globally-installed `pstack` CLI
(the bundle — §Distribution) and `PSTACK_IMAGE`, the image the control container runs. `init` is a
CLI command that starts a container from that image, so a new CLI against an old image, or the
reverse, is a supported-but-untested combination — move both.

Step 3 recreates the `pstack` container. Any in-flight job dies with it — jobs are in-memory —
hence step 1. There is no queue to preserve, and adding one would be a state store (see the
no-state-store rule in the README).

`pstack self-upgrade` *(not built)* is that sequence with the fetch folded in. It lives on the host
for the reason in §2 and will never be exposed over HTTP.

### Recovery when the control plane will not come up

The path that always exists:

```bash
ssh <host>
docker compose -p pstack-control ps            # what state is it actually in
docker compose -p pstack-control logs pstack   # why did the new image refuse to start
# roll back by re-running init from a known-good image, then re-check /api/health
```

The **shared and isolated deployments keep running** throughout. They are separate compose projects;
nothing about them depends on the API process being alive. A dead control plane costs you the
ability to *change* things, not the previews themselves — which is the property the two-layer split
was chosen to give you.

---

## Distribution: what ships, and why a bundle

`pstack` is a **globally installed CLI** — it runs on the host, over SSH or from systemd, precisely
where the API cannot reach (§2). So the artifact has to be installable without the repository:

```bash
bun add -g @samyx/preview-stacks     # or: npm i -g @samyx/preview-stacks
pstack --help
```

`bun scripts/build.ts` produces the two outputs, both `--target=bun`, minified, with linked
sourcemaps so a stack trace from an operator's box still names real functions:

| Output | Role | `package.json` |
|---|---|---|
| `dist/cli.js` (~74 KB) | the CLI + API + UI, one file | `bin.pstack` |
| `dist/index.js` | the library entry, for embedding the lifecycle in your own tooling | `exports["."]` |

The published tarball is **8 files, ~0.36 MB unpacked** (`files: [dist, README.md, LICENSE,
CHANGELOG.md]`): the two bundles, their two sourcemaps, and four metadata files. It contains **no
source, no docs, no examples, no skills** — verified by extracting the tarball and running it with
the repository unreachable. Working from a checkout (`bun src/cli.ts …`) is the **contributor** path,
not the user path.

### Why a bundle, and not the source tree

Shipping `src/` would make every invocation pay TypeScript parsing, download docs/examples/skills
nobody asked for, and turn the internals into the public surface — a module layout you can no longer
change without a major version.

### Why a bundle, and not `--compile`

A standalone executable bakes the Bun runtime (~60 MB) into the artifact, **per platform**. For an
npm package that means one of two bad shapes: a five-platform `optionalDependencies` matrix, or a
postinstall hook that downloads a binary (which breaks in exactly the locked-down CI environments
this tool is meant to run in).

A bundle is ~74 KB, and it costs nothing extra, because **Bun is already required**. The package
declares `engines.bun: ">=1.3.0"` and `bin` is a `#!/usr/bin/env bun` script — the shebang the
bundler does not emit and `build.ts` adds. There is no Node fallback being given up: the runtime APIs
this uses — `Bun.serve`, `Bun.YAML`, `Bun.spawn`, `Bun.file` — have no Node equivalent, so
`engines.bun` is load-bearing rather than advisory.

### Assets are embedded, not read from disk

The web UI and `templates/control/docker-compose.yml` are imported with `with { type: 'text' }`, so
the bundler **inlines them**. Nothing resolves a path relative to the source at runtime.

That is the whole point. `new URL('../templates/control/docker-compose.yml', import.meta.url)` works
perfectly from a checkout and does not exist in the published package — the failure mode where a tool
passes every local test and then `init` dies on a missing template on the one host that matters.
`build.ts` guards it the only way that proves anything: it runs `--help` against the **emitted
bundle**, not the sources, and fails the build if that exits non-zero.

The consequence to remember: **editing `templates/control/docker-compose.yml` requires a rebuild.**
The file on disk is the build input, not what a running `pstack` reads.

---

## See also

- [`../README.md`](../README.md) — the design rationale: axes, the `down`/`verify` asymmetry, scope
- [`usage.md`](usage.md) — the task guide, including the CLI workflows CI should use
- [`bootstrap.md`](bootstrap.md) — building a host from scratch
- [`../src/registry.ts`](../src/registry.ts) — the storage contract, heavily commented
- [`../src/spec.ts`](../src/spec.ts) — `kind`, `requires`, interpolation
