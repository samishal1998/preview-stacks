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
| `kind: shared \| isolated` | `internal/spec` | **built** — parsed, defaulted to `isolated`, `shared`+axes is a hard error |
| The `shared` guard on `down` | `internal/stack` | **built** — refuses without `force` |
| `--force` | `internal/cli` | **built** |
| `requires:` preflight | `internal/spec`, `internal/stack` | **built** — asserted before anything is created |
| The deployment registry | `internal/registry` | **built** |
| `/api/deployments/*` routes | `internal/api` | **built** — the API is registry-backed; the old single-spec `/api/spec` and `/api/stacks` routes are gone |
| `pstack init` | `internal/initctl` + `templates/control/` | **implemented and dispatched** — `pstack init --domain … --acme-email …`; DNS-01 adds `--challenge dns01 --dns-provider …` |
| HTTP-01 / DNS-01 challenge modes | `internal/initctl`, `templates/control/` | **built** — HTTP-01 is the default; `init` renders the two `#__MARKER__` blocks per mode (§TLS) |
| The binary | `cmd/pstack`, `.goreleaser.yaml` | **built** — one static executable per platform on GitHub Releases (§Distribution) |
| `pstack upgrade` | `internal/upgrade` | CLI-only, two phases: the installer, then re-exec `--resume` which rebuilds the image and re-runs `init` with the stored token and DNS credential |

`internal/api/server.go`'s file header is the authoritative route list. Where this document and
that header disagree, the header is right.

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
in `internal/spec (subdomains.go)`. Short version — this seam needs no new mount, no re-`init`, and nothing
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
(`AcmeChallengeArgs` / `AcmeRouterLabels` in `internal/initctl`) and leaves the rest byte-for-byte —
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
   design (see `internal/jobs`) — so there is no record of how far it got, no transcript, and the
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

`pstack init` and `pstack upgrade` run **on the host**, from the CLI, as a systemd unit or an
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
| Created by | `pstack init`, on the host (`internal/initctl`, dispatched from `internal/cli`) | the API, or the CLI | the API, or the CLI, usually from CI |
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

The guard lives in `down()` in `internal/stack`, so a direct library caller cannot bypass it either.
`internal/api` *also* checks it before starting a job — deliberate duplication, so the HTTP caller
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

> **Built and wired.** `internal/registry` backs the `/api/deployments/*` routes (§6).

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

Job records. They stay in memory, capped at 50, lost on restart (`internal/jobs`), because a job is
the transcript of an *attempt*, not a fact about the host. Restarting the API loses history, not
correctness — and it clears the per-stack busy locks, which is the desired behaviour after a crash.

---

## 4a. How a URL reaches a container port

Worth spelling out, because every "the hostname does not work" traces to one of these four steps.

Traefik's **docker provider** watches the socket and reads labels off every container. A request is
served by walking labels, not by DNS:

| Step | What decides it |
|---|---|
| 1. Is this container visible at all? | `traefik.enable=true`. The control stack runs `--providers.docker.exposedbydefault=false`, so **without it the container is invisible** — the hostname 404s with nothing logged. |
| 2. Which URL? | `traefik.http.routers.<r>.rule=Host(…)` — the router. |
| 3. Which backend? | `traefik.http.routers.<r>.service=<s>`, and `traefik.http.services.<s>.loadbalancer.server.port=<port>`. |
| 4. What address does it dial? | **The container's IP on a docker network, plus that port.** |

Step 4 is the one that surprises people: Traefik does **not** resolve `service_name:port` over compose
DNS. The docker provider reads the container's IP from its network attachments and dials
`http://<ip>:<port>`. Which network is chosen comes from `--providers.docker.network=preview-ingress`
(set in the control stack), overridable per container with `traefik.docker.network`.

Three consequences follow directly:

- **The service must be attached to `preview-ingress`**, or there is no IP for Traefik to dial. Not
  attached ⇒ up, healthy, and unreachable. And if the compose file declares that network without
  `external: true`, compose makes `<project>_preview-ingress` instead — which looks right everywhere.
- **The port is the container-internal one.** `loadbalancer.server.port=80` means port 80 *inside* the
  container. Publishing a host port (`ports:`) is unnecessary and does nothing for routing.
- **Router and service names are global across the daemon**, not scoped per compose project. Two
  deployments both naming a router `app` overwrite each other and one hostname serves the other's
  container — which is why the names carry `${STACK}`.

A deployment's **Containers & routes** tab renders exactly this chain, with the address Traefik
assembled in a `forwards to` column, and names whichever step is missing.

### Generated labels: declare the port, not the boilerplate

All six pieces above are boilerplate that differs only by service name and port, and each has a silent
failure mode. So declare the part that is actually yours:

```yaml
services:
  app:
    image: nginx:alpine
    profiles: [app]
    labels:
      - pstack.routing.port=80            # the port INSIDE the container
      # - pstack.routing.service_name=web # optional; defaults to the compose service name
      # - pstack.routing.host=custom.example.com  # optional; overrides the convention
```

The domain comes from **`PREVIEW_DOMAIN`** in the spec's `env:` — the name every example here uses.
`DOMAIN` is accepted as a legacy alias (0.3.0–0.7.0 read only that), and a name *declared* by the spec
beats one picked up from the ambient environment, so a stray exported `DOMAIN` cannot silently anchor
your hostnames.

pstack writes `traefik.enable`, `traefik.docker.network`, the router rule for
`<name>-<stack>.<domain>`, the entrypoint, `tls` (plus `certresolver` **only** on an HTTP-01 host —
read from the running Traefik, so it cannot disagree), the `loadbalancer.server.port`, the wildcard
router when `compose.subdomains` names that profile, and the network wiring on both the service and
the file root — with `external: true`, which is the part that otherwise bites.

**It never overrides you.** A service carrying *any* `traefik.*` label is left completely alone —
labels, networks and all. That is the escape hatch for anything this cannot express, and the presence
of your own label *is* the opt-out; there is no flag. A service with neither kind of label is also left
alone, because a database should not get a hostname.

**It writes a derived file, not an overlay.** pstack reads the submitted compose file and writes a
complete `compose.generated.yml` beside it, which compose then reads on its own. An overlay would put
Compose's merge semantics for list-form `labels` in charge of whether *your* routers survive — and
getting that wrong deletes them silently. The derived file is JSON (valid YAML 1.2, and what every YAML
parser agrees on: emitting YAML would mean trusting a stringifier's quoting to match Go's parser on
values like ``Host(`app.example.com`)``). **Your file is never modified.** It is regenerated on every
compose subcommand, so `up` and `down` cannot disagree about what a router was called.

`pstack validate` prints what would be generated, and `examples/docker-compose.minimal.yml` is the same
stack as the hand-written example with the boilerplate removed.

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
you were editing — it is *other* routes disappearing. Three defences, all in `internal/routing`:

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

## 4c. Private registries — the client authenticates the pull

An image pull is authenticated by the **client**, not the daemon. `docker pull` reads its *own*
`config.json`, finds the entry for that registry, and hands it to the daemon in an `X-Registry-Auth`
header; the daemon never consults the client's config.

pstack shells out to `docker compose` from **inside the control container**, so the client that matters
is the docker CLI in there. A `docker login` on the host writes the *host's* `config.json`, which that
container cannot see — so a private image fails with `pull access denied` on a host that is
demonstrably logged in, and nothing in the error points at why.

From **0.7.0** the control stack mounts a `DOCKER_CONFIG` directory (`<DATA_DIR>/control/docker` →
`/docker-config`) into the API. Two ways in, both landing in the same file:

```bash
# from the host
docker login --config /var/lib/pstack/control/docker ghcr.io

# or over the API / the Registries page
curl -X PUT https://api.<domain>/api/registries/ghcr.io \
  -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
  -d '{"username":"…","password":"…"}'
```

**On demand, with no restart.** The CLI re-reads `config.json` on every invocation, so a credential
added now applies to the next pull. Nothing to recreate, no cache to bust.

**Write-only.** A `config.json` entry is `base64("user:password")` — reversible, not encrypted. So
there is no read path for it anywhere in the API: `GET /api/registries` returns hostnames and
usernames only, and nothing in `internal/registries` can return a password. The file is written `0600`,
atomically (compose may read it at any moment, and a truncated config parses as *no* credentials).

Two traps it handles explicitly:

- **Docker Hub's key is `https://index.docker.io/v1/`**, not `docker.io`. A credential stored under the
  friendly name is silently never used for `nginx:alpine`, so every alias is normalized to the
  canonical key on the way in *and* on the way out.
- **Credential helpers do not transplant.** A `config.json` copied from a laptop usually carries
  `credsStore: "desktop"` or `"osxkeychain"` and **no** `auths`, because the secrets live in the OS
  keychain. That helper binary does not exist in the container, so every pull fails with `error getting
  credentials` while an empty `auths` looks like the cause. Helpers found in the file are reported so
  the UI can say so.

---

## 4d. Notifications — telling something else what happened

`job.leaked` is the event this product exists to produce, and until 0.11.0 the only way to see it was
to be looking at the page. Notifiers push it somewhere.

### The seam, and what "composable" actually buys

A registration is `{ type, name, events[], config{} }`. `type` selects a **notifier type** — a
`{ kind, label, fields, validate, send }` in `internal/notify` — and that type OWNS the shape of
`config`. Adding Slack or Discord later is one entry in that map:

- **no migration** — `config` is opaque JSON in the `notifiers` table;
- **no route change** — every route is type-agnostic;
- **no UI change** — the form is rendered from `GET /api/notifiers/meta`, which returns each type's
  `fields`. Hard-coding the URL field in the page would have made the seam a lie, so the page does
  not know what a webhook is.

Slack and Discord (shipped in 0.20.0 as exactly such entries — one `chatType` factory, since the
whole difference between them is the URL's owner and the JSON key the text goes under) ignore the
signing secret entirely, because for an incoming-webhook URL the
*URL* is the credential. That is the seam working, not a gap in it.

### Delivery

| Property | Choice | Why |
|---|---|---|
| Timing | fire-and-forget | Nothing awaits a delivery. A webhook endpoint that is down, slow, or hanging must not be able to slow a deploy, let alone fail one. |
| Retries | 3 attempts, 1s then 5s | Bounded and short. The delivery id is **stable across attempts**, so a receiver can dedupe; a fresh id per retry would make at-least-once undedupable. |
| Timeout | 5s per attempt | `AbortSignal.timeout`. |
| Concurrency | 8 in flight, **1 per notifier** | This process also runs `docker`; an event burst across many notifiers would otherwise exhaust its file descriptors. The per-notifier limit is what stops one broken endpoint taking the whole process down to its speed: a delivery costs up to 21s, so without it eight events put all eight slots on ONE dead notifier and every healthy one's event is logged as dropped. Over either cap a delivery is **recorded as dropped**, not queued — a queue that grows during an outage is a disk leak wearing a different hat. |
| Deletion | retries stop | `DELETE` returns 200 at once, and the loop re-checks before each retry, so an unregistered endpoint stops receiving signed POSTs instead of getting two more over the next 6s. |
| Log | last 200 per notifier | Pruned on write — **finished rows only**, so a delivery still retrying is never deleted out from under its own result. Unbounded, it is the lowest-value data in the database growing on the same disk as the registry. |
| Test deliveries | logged like any other | `POST /api/notifiers/:id/test` writes a delivery row and updates `lastStatus`. It carries `data.test: true`, which a per-event formatter **must** check — the synthesized event name is a real one and would otherwise render a green "job succeeded" nobody earned. |

### The signature, and what a receiver must do

```
x-pstack-event:      job.leaked
x-pstack-delivery:   evt_…          ← stable across retries; DEDUPE ON THIS
x-pstack-timestamp:  1754000000000
x-pstack-signature:  sha256=<hex of HMAC-SHA256(secret, `${timestamp}.${rawBody}`)>
```

Verify over the **raw** body — re-serialising the JSON changes the bytes and the signature will not
match, which is the single most common way this goes wrong on the receiving end. Reject anything whose
timestamp is more than five minutes old; that is the replay protection, and it works because the
timestamp is *inside* the signed material.

### What is deliberately not in a payload

Job outcomes carry credentials **by design** — `outcome.outputs` is the inter-axis env channel, so a
provisioned database's connection string lives there — and a webhook URL is outside the auth gate that
protects every other consumer of job data. So payloads are built field by field: ids, stack, action,
state, timings, the *names* of leaked axes. Never the outcome object, never `spec.env`, never
`job.log`.

`verified` is in the payload for a reason worth knowing: `down` with `verify: false` emits no
`assert_gone` steps, and a spec whose axes declare none yields all-skipped ones. In both cases the job
succeeds having proven nothing. Without that flag, `job.succeeded` would read as "clean" when it means
"nobody looked".

### The URL guard is not a privilege boundary

Loopback, link-local and private addresses are refused, and a redirect is treated as a failure rather
than followed (a public host that 302s to `169.254.169.254` would otherwise defeat any
registration-time check). But **anyone who can register a notifier can already run shell on this host**
— a spec's hooks are `bash -c` with the Docker socket mounted. The guard exists so a *typo* aimed at
an internal address fails loudly, not because it stops an attacker. `PSTACK_NOTIFY_ALLOW_PRIVATE=1`
escapes it for the legitimate case of a collector on the same box.

---

## 5. Trust boundary

**Accepting a spec over HTTP is accepting arbitrary shell, by design.**

A spec carries axis hooks and `requires` asserts. Every one of them is a shell string run through
`bash -c` with the resolved environment injected (`internal/exec`). That is deliberate: this tool
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
| Bearer token on every mutating route | `internal/api` | `POST`/`DELETE` without `Authorization: Bearer <PSTACK_TOKEN>` → `401` |
| Loopback interlock | `internal/cli`, `serve` | with no `PSTACK_TOKEN` the host is forced to `127.0.0.1`, **and** an explicit non-loopback `PSTACK_HOST` is a hard exit `3` rather than a silent downgrade |
| Ingress auth | yours | **`GET`s are unauthenticated even when a token is set.** `GET /api/jobs` — no id needed — returns every retained job transcript, including `outcome.outputs`, which holds **captured credentials by design**, and the first line of a failed hook's stderr; `GET /api/deployments` enumerates every deployment on the box. Writes are double-gated; reads are gated only by whatever sits in front. |

### The read side (resolved in 0.10.0)

The intended line was: an unauthenticated read may expose the **shape** of a deployment, never a value
that lets the reader authenticate to something. The job reads violated it — `outcome.outputs` is the
documented channel for passing a provisioned resource's connection string to a later axis, serialised
in full. **0.10.0 closed this by requiring auth on every route, reads included** (see
[`secret-exposure.md`](secret-exposure.md), now marked resolved). The posture below is therefore
defence in depth rather than the sole barrier — still worth keeping, because the API commands a
read-write Docker socket.

The default posture should be *not reachable from the internet*: keep `PSTACK_HOST=127.0.0.1`, set
`PSTACK_TOKEN` anyway (it protects against everything else already on the host), and tunnel:

```bash
ssh -N -L 7878:127.0.0.1:7878 preview@<host>
```

See [`bootstrap.md` §9](bootstrap.md#9-hardening-checklist) for the full checklist.

### 5c. The container terminal (0.12.0)

`GET /api/deployments/:id/terminal?container=…&shell=sh`, upgraded to a WebSocket. The **Terminal**
tab in the advanced UI is the client.

**The container name is not trusted, and that is the whole security story.** `docker exec` accepts
any container the daemon knows — Traefik, another PR's stack, and the pstack control container
itself, whose filesystem holds `pstack.db`: every password hash, every session, every notifier
signing secret. So the requested name is matched against the containers **this deployment owns**
(`com.docker.compose.project=<stack>`), and anything else is a 404. Quoting is not the defence and
never was: the command is an argv array with no shell in it, and the wrong container would still be
the wrong container.

| Property | Choice | Why |
|---|---|---|
| Auth | session cookie (or bearer) | A browser cannot put an `Authorization` header on a WebSocket — which is exactly why sessions became cookies in 0.10.0. The handshake is a same-origin GET and carries it automatically. **No token ever goes in the URL**: URLs land in proxy logs and browser history. |
| Who | root or an `admin` user | Everyone is `admin` today, so the check is currently a no-op — it is in the code path now so that when roles become real, the decision about who gets a shell on the host is already where it belongs. |
| Audit | a row per session, written at OPEN | `GET /api/terminal-sessions`. Written when the shell opens, not when it closes, so a session ended by process death still leaves a record. The actor is denormalized: deleting the account must not erase the fact that it opened a shell. |
| **No pty** | `docker exec -i`, not `-it` | A real terminal needs `script` (or a pty binding) whose flags differ across distributions, and it was never run on a real host — so pstack ships the half it can stand behind. |

**What "no pty" costs, concretely:** no prompt, no job control, no `Ctrl-C`, and no full-screen
programs (`top`, `vim`). What still works is everything an operator usually opens a shell for —
`ls`, `cat`, `env`, `psql`, `redis-cli`, a migration command. The UI says this on the page rather
than letting an operator conclude the terminal is broken.

**The line discipline is emulated, because that is what a pty actually was.** A pty does two
translations that nothing else does, and missing either one looks like a bug rather than a missing
feature:

- **Input, CR → NL** (`crToNl` in `api.ts`). A terminal emulator sends `\r` for Enter. A shell
  reading a *pipe* never treats that as end-of-line, so without this every keystroke arrives, the
  socket stays healthy, and nothing ever runs — the terminal looks dead while being perfectly
  connected.
- **Output, NL → CR-NL** (`convertEol` in the UI). Otherwise `\n` moves down a row without
  returning the carriage and `ls` renders as a staircase marching off the right edge.

#### Known limits of the notifier seam (deliberate, as of 0.14.0)

Recorded so the next person does not have to rediscover that they were considered:

- **Retry policy and timeout are the dispatcher's, not the type's.** A provider with its own
  backoff contract — Slack's `429` + `Retry-After` — cannot express it. Real, and left until a
  second type actually needs it: a per-type knob with exactly one implementation is the abstraction
  that gets built wrong, because there is nothing to check the shape against.
- **`assertDeliverableUrl` is called by the type, not enforced by the seam.** A type that forgets it
  gets no URL check. The dispatcher cannot enforce it — it does not know which config field is an
  address, and `secret` marks credentials rather than URLs.
- **`POST /:id/test` bypasses `MAX_IN_FLIGHT`.** It is one operator pressing one button, bounded by
  how fast a person can click; queueing it behind the delivery cap would make "test this endpoint"
  fail while the endpoint being tested was busy failing.
- **`webhooks.ts` holds all notifiers, not only webhooks** — named for the first type extracted from
  it. A rename is pure churn against every import.

**Proxies must forward the upgrade.** The UI container's nginx sends `Connection ""` to keep the
upstream alive for the SSE job log; a `map` now picks `upgrade` per-request when the client asked
for one. Without it the handshake degrades to an ordinary GET and the browser reports a bare
code-1006 close that looks like the server refusing it.

---

## 5d. Host variables & secrets (0.19.0)

The GitHub model, scoped to one host. A **variable** is configuration anyone may read back (a
region, an image tag); a **secret**'s value goes in through `PUT /api/host-vars/:name` and never
comes back out — `GET /api/host-vars` returns its name and timestamp only, and there is no other
read path.

Specs reference them **explicitly**: `${vars.REGION}`, `${secrets.DB_PASSWORD}` — anywhere a spec
interpolates (the `env:` block, hooks, compose settings). The namespace is the point: a plain
`${REGION}` keeps meaning "whatever the request or the deployment supplied", so no precedence rule
between request variables and host variables can ever need explaining — they are spelled
differently. A missing reference fails the resolve loudly, naming the Variables page.

| Rule | Why |
|---|---|
| `${secrets.*}` is refused in `stack:` | The stack name is identity — a compose project name, a hostname label, a value in every log line. Those surfaces cannot be redacted, so the reference is refused rather than the value scrubbed after the fact. |
| Secret values are scrubbed from job logs, job records and compose logs **by content** | Hooks echo whatever they like (`echo $TOKEN` is one debugging session away), and a failing hook's stderr lands in the outcome record that `GET /api/jobs/:id` serves forever. By-name redaction cannot catch a value; content matching can. |
| secret → variable conversion is refused | Flipping the flag would turn a write-only value into a readable one with no re-entry — an information-flow downgrade wearing an UPDATE's clothes. Variable → secret only tightens and is allowed. |
| The bare CLI errors on `${vars.*}`/`${secrets.*}` | Host values live in the control plane's database. The error names the boundary ("submit this spec to a pstack server") instead of pretending the variable is merely undefined. |
| Stored plainly, like notifier signing secrets | The server must hand the plaintext to hooks and compose, so one-way storage is impossible. The protection is the 0700 directory, the 0600 database file, and the absence of a read path. |

## 5e. Swarm, the scheduler, and share links (0.26.0)

Three features, each with one structural decision that is refused below because it would have been
the obvious one.

### Swarm: the control stack stays a compose project on the manager

Previews deploy as swarm stacks; the control stack (Traefik + the API) does not. It is a plain
compose project on the manager, exactly as before, with the two external networks recreated as
**attachable overlays** so its containers reach tasks on any node, and Traefik running **both**
providers — `docker` for the control stack's own routers, `swarm` for the stacks. The obvious
alternative, deploying the control stack as a swarm stack too, was refused: §2 is the reason. The
API must never depend on the orchestrator it manages, `init`'s health wait and `detectChallenge`
read the control containers by compose label, and a broken swarm must still leave a control plane
that can say so.

**Conversion happens at invocation time, not at store time.** The registry keeps the submitted
compose file byte for byte (§4) — `source()` hands it back to the replace form, and a named spec's
copy lands in every referencing deployment. Converting it on the way in would have to happen in two
places and would make the stored file disagree with what the author wrote. Instead the same
pipeline that generates Traefik labels (`autolabel.ts`, §4a) converts on **every** `docker stack`
invocation and writes `compose.generated.yml` beside the original: `up`, `down`, `logs` and `ps`
cannot disagree about what was deployed, and a dry run shows the original because nothing is
written under it. The rules are faithful (a missing `restart:` is `none`, which is what compose
does, not swarm's `any`) and everything dropped is named in the job log, because "Additional
property mem_limit is not allowed" teaches nothing.

Discovery (`inspect.ts`) answers for both: a swarm stack's containers carry
`com.docker.stack.namespace`, its routers live on **service** labels, and a task on another node is
listed from `docker stack ps` with `remote: true` — honestly out of reach of `docker exec` and
`stop`, which are node-local, and refused by name on the routes that would need them.

### The scheduler: policy in the spec, the record in the registry, activity in memory

**Where the policy lives** — `sleep: { idle, after }` in the spec. It is the author's intent about
their preview, it travels with CI like everything else in the spec, and a named spec gives every PR
the same policy for free.

**Where the record lives** — `meta.json`, as `sleep: { since, reason, hosts, rules }`. This is the
one record in the registry that looks like state, so the line is drawn precisely: it is **not** a
claim that nothing is running (docker answers that, and `up` clears the record regardless). It is
the intent "wake this on a request", plus the one fact that cannot be recovered from docker once the
containers are gone — which hostnames are this deployment's. Those are captured from the live
Traefik labels the moment before teardown, which is why a hand-written router is recognised exactly
like a generated one. A SQLite table of sleeping stacks was refused by the amended invariant 10: it
would be a row describing what is running.

**Where activity lives** — memory. `idle` reads Traefik's per-router request counters (a Prometheus
entrypoint the control stack exposes to the API container only; nothing in the request path) and
notes, per stack, the last tick on which any of its routers moved. A restart forgets that, and the
idle clock restarts from the process start — the cost is one extra `idle` period awake, never a
sleep that comes early. A `last_seen` table was refused for the same reason job records are in
memory: it is observation history, and losing it loses nothing about correctness.

**Why the catch-all is labels on the pstack container, not a file.** The wake router (priority 1,
`HostRegexp` over the whole domain) used to exist only on cloud-init hosts, as `fallback.yml` in
Traefik's file directory. It is now rendered by `init` as labels on the pstack container, for the
reason §4b gives for `control.<domain>`: nothing written to the dynamic directory — by an operator,
by the routing page, by a bad edit — can take it away, and the docker provider resolves the
container's IP directly rather than trusting a DNS alias across an overlay network. A request that
reaches the API through it carries the preview's `Host`; the `SleepIndex` (rebuilt on every record
change) answers in one Map lookup, so the dispatch costs nothing when nothing sleeps.

**Why wake is `up`.** Axis `up` hooks are idempotent by contract ("re-run on every redeploy") and
re-capture their outputs, so a wake needs nothing remembered from before the sleep — no persisted
`outputs`, no second code path. It runs under the same per-stack lock as every other job, so a wake
racing a `down` over one database branch cannot run beside it. A `down` does not queue behind a
wake, either: teardown preempts, cancelling what runs and dropping what waits. The wake **trigger**
is separately suppressed while that stack has anything outstanding, so a burst of requests to a
sleeping hostname still produces exactly one wake job rather than one per request — the queue is not
a place to put nine redundant wakes.

### Share links: a JWT signed with PSTACK_TOKEN, and no table

A share link is an HS256 JWT — `{ sub: 'share', dep, views, iat, exp }` — signed with
`PSTACK_TOKEN`. Three refusals:

- **No table of issued links.** It would be a row describing a credential (invariant 10 again), and
  the only thing it would buy is per-link revocation. The TTL bounds a leak (7 days by default, 30 at
  most), and rotating `PSTACK_TOKEN` revokes everything at once; `pstack upgrade` deliberately does
  not rotate it, so links survive upgrades.
- **No separate signing key.** `PSTACK_TOKEN` is the one secret every host already has, 192 bits when
  `init` generates it, and a leaked link reveals nothing about it — verifying one needs the key, but
  holding one does not yield it.
- **No header-only transport.** The token travels as `?token=` because the log view follows logs over
  an `EventSource`, and a browser cannot put a header on one — the same constraint that made
  sessions cookies (§5). The raw `PSTACK_TOKEN` is never accepted from a query string; a JWT is a
  bounded grant, the bearer is root.

What a link reaches is decided **before any route** — right after the auth gate, `shareAllows`
checks method, deployment and view, so a route added next year is closed to share principals until
someone lists it. Request variables are ignored for them (a `?PR=8` would otherwise resolve another
stack's logs), `mayOpenTerminal` answers by kind, and `actorOf` names the link so the audit rows
stay honest.

## 5f. Single sign-on (0.27.0)

The operator registers **one** OAuth/OIDC application in their own org and pastes the client id and
secret in. Everyone who can authenticate against that directory can sign in; the account appears on
first login. We are the relying party and own nothing about their directory.

### It ends in an ordinary session, and that is the whole integration

`ssoSignIn` mints exactly the row `login()` mints. Nothing downstream — the gate, `shareAllows`, the
terminal, personal tokens, the audit log — has an "is this an SSO user" branch, because there is no
such thing at that layer. `PSTACK_TOKEN`, manually created accounts and personal tokens keep working
unchanged: SSO is strictly additive.

The account row is ordinary too. An SSO user's password is 32 random bytes nobody holds, hashed like
any other — **not** a null. A nullable password would be a state some future code path could read as
"no password required"; a hash of an unknown secret cannot be.

### Identity is `(providerKey, subject)`. The email only ever adopts

Addresses move between people; a provider subject does not. So the link table is keyed on the pair,
and the email is consulted in exactly one place: taking over an account that **already existed
locally**, gated on the provider having said the address is verified and on there being exactly one
match. That branch is the only path in the feature that can hand someone an existing account's
privileges, which is why `emailVerified: null` — the provider never said, GitHub's normal answer —
is not good enough, and why two rows sharing an address is an ambiguity rather than a coin flip.

`providerKey` stays in the table even though only one provider can be configured at a time (multiple
simultaneous providers are out of scope): swapping providers must not silently re-link one org's
subjects onto another org's accounts.

### The callback is under `/api/`, and that is not cosmetic

`/api/auth/sso/callback` — because in advanced-UI mode `control.<domain>` is **nginx serving the
SPA**, and `location /api/` is the only prefix it proxies to this process (§4a's sibling problem).
A callback on `/auth/sso/callback` would be answered with `index.html`: the login would hang, the
provider would report success, and nothing would appear in any log. One helper builds the URL for
the authorize leg, the token exchange **and** the value the config screen tells the operator to
register, so the three cannot drift — a `redirect_uri` mismatch is the most common failure in this
protocol and the provider's error rarely names it.

The base URL comes from `PSTACK_DOMAIN`, not from the request's headers, for the same reason share
links do: a forwarded header is caller-controlled, and a `redirect_uri` that differs between the two
legs simply fails.

### What is refused

- **A nonce.** PKCE plus single-use state covers the replay this is exposed to, and a nonce that is
  sent but never checked reads as a defence in review while being none. If an ID token ever arrives
  anywhere but straight from the token endpoint, add it *and* verify it in one change.
- **A `none`/HMAC ID token.** `alg` is an allow-list (RS256, ES256) read before any key is imported —
  `alg: none` and `HS256`-signed-with-the-public-key are the two classic JWT forgeries.
- **Skipping signature verification.** OIDC Core permits it when the token comes straight from the
  token endpoint over TLS. It is done anyway: JWKS via WebCrypto is ~60 lines and no dependency, and
  it is the difference between trusting the channel and verifying the claim. `iss` must equal what
  the *discovery document* declares, `aud` must contain the client id (string or array), `exp` gets
  60s of skew, and an unknown `kid` refetches the JWKS once — no sooner than a 30s cooldown, so a
  junk `kid` cannot turn every request into a fetch against the provider.
- **A JSONPath evaluator.** Claim mapping is flat key lookups, plus one dotted path because two
  providers nest the avatar and nothing else.
- **A provider registry with lifecycle hooks.** A preset is a row in `PRESETS`. Adding GitHub
  Enterprise, Gitea or Zitadel is one entry, no code.
- **Refresh tokens, SCIM, group/role sync, multiple providers, SAML, back-channel logout.** Named
  out of scope and left there.

### The transient store

The PKCE verifier is the only thing that needs storing, for the length of one round trip. It sits
behind a four-method interface (`set`/`get`/`delete`/`take`) with a SQLite implementation, because
SQLite is what this service already wires up — Redis or Postgres slot in behind it if this ever goes
multi-instance, which is a config change rather than a rewrite. `take` reads and deletes in **one**
statement: single-use state is what stops a replayed callback, and a get-then-delete pair has a
window where two requests find the same row. Expired rows are swept on write, not by a timer — that
table only grows when someone starts a login.

### The secret

Stored, not hashed, for the notifier-secret reason (§4d): the token exchange must *present* it. It
has no read path — the config endpoint answers with a mask, submitting the mask back keeps what is
stored, and the protection is the 0700 directory and the 0600 file, as with every other secret here.

## 6. Submitting a deployment

`:id` is a **registry id**, not a compose project name. The server owns the stored spec and resolves
`stack:` itself, so a client can never point it at an arbitrary compose project on the host.

| Method | Route | Notes |
|---|---|---|
| `GET` | `/api/deployments` | every submitted deployment, plus `busy` and `running` |
| `GET` | `/api/deployments/:id` | meta + a **field-by-field** spec summary |
| `PUT` | `/api/deployments/:id` | submit or replace: `{ spec, compose?, env? }` → `201` new / `200` replaced |
| `DELETE` | `/api/deployments/:id` | forget it — **refused while containers still exist** |
| `POST` | `/api/deployments/:id/{up,down,verify}` | `202 { job }` — the stub's `state` reads `running`, or `queued` behind that stack's current job. **A busy stack is no longer a `409`**; the two that remain are the record being replaced or deleted, and `down` on a `kind: shared` stack without `force` (§3), both decided before any job is started. `down` body: `{ verify?, force? }` |
| `POST` | `/api/deployments/:id/cancel` | stop everything outstanding for that stack — the running job **and** the one queued behind it → `200 { stack, cancelled[], by, warning }` |
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
- **`PUT` is refused (409) while that stack has a job in flight *or waiting to start*.** Swapping the
  spec mid-job means the eventual `down` tears down with different profiles and axes than `up`
  created — the same orphan class as deleting the record — and a *queued* job would deploy a spec
  chosen after the decision to run it was already made. `DELETE` refuses on the same rule. The escape
  hatch is `POST /api/deployments/:id/cancel`, which the 409 body names by route: a `PUT` that
  cancelled on your behalf would silently kill a teardown somebody else was waiting on.
- **`DELETE` fails closed.** It refuses while containers exist, *and* refuses when Docker did not
  answer — "could not tell" is not evidence of absence. `Registry.remove` forgets only; it never
  tears anything down, and forgetting a live deployment orphans it beyond the control plane's view,
  which is precisely the leak this project exists to prevent.
- **Responses are built field by field, never spread.** `parseSpec` seeds a spec's variables from
  the whole ambient environment, so a resolved `Stack.env` holds every secret this process has —
  `PSTACK_TOKEN` included. The spec summary returns axis **hook names**, never hook bodies: a hook
  is a shell string that routinely carries an API token inline.

### The per-stack queue: depth one, last write wins

**One job per stack at a time** has not changed — it is the guarantee the whole product rests on,
and a `down` deleting the database branch an `up` just created is the failure it prevents. What
changed is what happens to the *second* one. It used to be refused with a `409`; it is now accepted
and **queued**, and a third **replaces** the queued one. Depth is one, always: five rapid pushes to a
PR run the first deploy and then exactly one more, carrying the newest spec. The middle three are
stale before they could start, and deploying them in order would spend minutes building things
nobody will look at while the newest commit waits behind them.

A replaced job reaches **`superseded` under its own id**. Its caller was handed that id in a 202 and
is polling it or streaming it; a record that silently vanished would leave that client waiting
forever. `superseded` is its own state rather than a flavour of `cancelled` for the reason
[webhook-events.md](webhook-events.md) gives: a cancelled job stopped **part-way** and left whatever
it had already done in place, while a superseded one never ran at all — reporting it as cancelled
sends an operator hunting for partial state that cannot exist.

**`down` preempts.** A teardown never waits behind a deploy: it cancels the running job, drops the
queued one, and takes the stack as soon as the cancelled shell returns. The deploy it stops is
building exactly what the operator just asked to destroy, and every second it keeps running is more
to clean up. Which actions preempt is a **table** in `internal/jobs` (`preempts`), one row per
action, not an `if` in `Start`. A preempted `up` is `cancelled` and its transcript still ends with
the line that matters — *whatever ran before this point was NOT undone* — because a half-built stack
is a half-built stack whether a person or a teardown stopped it.

**The cap is global, not just per-stack.** At most `max_jobs` jobs (default 4) run at once across
every stack; over the cap a job **waits**, it is never refused. This process also runs `docker`: each
job is a compose invocation plus its hooks, against one socket and one set of file descriptors, so
forty stacks deploying at once is how the control plane becomes the outage. Dispatch is FIFO by
acceptance order, skipping stacks that are already busy — a busy stack cannot use a free slot, so
skipping it is not queue-jumping, it is the only thing that stops one stack's backlog starving every
other. The [notifier dispatcher](#delivery) has the same shape one tier up (8 in flight, 1 per
notifier), for the same reason.

**The cap is settable at RUNTIME** (0.32.0). It was `PSTACK_MAX_JOBS`, read once at boot, so changing
it meant restarting the control container — which kills every job in flight to change a number about
jobs. It is now the `max_jobs` [setting](usage.md#runtime-settings-0320): the stored value outranks
the environment variable (which becomes its default) and `PUT /api/settings/max_jobs` calls
`Registry.SetMaxRunning`, so the new cap is in force for the next dispatch on that request.

**Raising it pumps. Lowering it kills nothing.** Raising has to dispatch immediately, because there
may be jobs sitting in the queue waiting for a slot that now exists and nothing else would start them
until some unrelated job happened to finish. Lowering is the half worth being explicit about:
**jobs already running run to completion**, so `inFlight` legitimately sits *above* `maxRunning`
until they do, and the new cap applies to the next dispatch. Killing the excess to make the number
fit would mean an operator who typed `1` had silently torn down three deployments half-way — the
config box would be a destructive control, and the transcripts would say "cancelled" with no person
attached. The 200 says so in words, and every surface that exposes the box must repeat it, because
"cancelled" is what an operator would otherwise assume from a number that got smaller.

**It is not a hole in invariant 10 either.** The row is a knob an operator *chose*, like a host
variable; nothing in the settings table describes what is running.

**Everything outstanding for a stack stops at once.** `POST /api/deployments/:id/cancel` cancels the
running job and terminates the queued one in **one** registry call, so there is no window in which a
cancelled job's successor dispatches into the gap. It is what `down`'s preemption uses, so there is
one implementation rather than two. It is not a teardown: it stops work and touches nothing on the
host, and it undoes nothing the running job had already done.

**Why this is not the state store invariant 10 refuses.** The queue is in memory, one entry deep per
stack, and dies with the process exactly as the running job does. The refusal was always about
*durability* — a backlog written down, surviving a restart, and replaying work against a world that
moved on while the control plane was down. There is nothing to replay here and nothing to reconcile;
a restart costs history, not correctness, which is what invariant 10 has always said about job
records. §4d's refusal to queue *deliveries* is the same line drawn from the other side: what it
declines to grow is an unbounded per-endpoint backlog during an outage, not one slot.

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

There is no published library: every package is `internal/`, and the supported programmatic
surface is the HTTP API — through `@samyx/preview-stacks-client` from TypeScript, or any HTTP
client. Inside this repository, `internal/registry` is the storage contract:

```go
reg := registry.New(registry.DataDir())                 // $PSTACK_DATA, default /var/lib/pstack
dep, err := reg.Put("pr-123", specYaml, registry.PutOptions{ComposeYaml: &composeYaml, Env: map[string]string{"PR": "123"}})
st, err := reg.Resolve("pr-123", map[string]string{"PR": "123"}, nil)
```

---

## 7. Upgrade path

### `pstack init`

> **Implemented and reachable as `pstack init`** (`internal/cli` → `internal/initctl`). A host can still
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
2.  install          the release's install.sh (checksum-verified), into where this binary lives
3.  build-image      the control image, from the binary just installed (the old one kept as pstack:local-previous)
4.  pstack init …    re-render + `compose up -d` the control stack, same token, same DNS credential
5.  verify           curl -sf https://api.<domain>/api/health
```

`pstack upgrade` is steps 2–4, in **two phases** that are two processes: phase 1 runs the installer
and then re-executes `pstack upgrade --resume`, because `build-image` copies the *running* binary
into the image — a single process would install the new version and then faithfully build an image
of the old one. Step 2 and 3 are **two** artifacts that upgrade together: the `pstack` binary and
`PSTACK_IMAGE`, the image the control container runs. A new CLI against an old image, or the
reverse, is a supported-but-untested combination — `upgrade` moves both.

Step 4 recreates the `pstack` container. Any in-flight job dies with it — jobs are in-memory —
hence step 1. The queue dies with them, and that is the point: it is one job deep and lives in
memory beside the jobs themselves, so it is not the state store the no-state-store rule is about —
nothing is promised to survive a restart, and an upgrade drains rather than replays. A queue that
persisted, or one that grew without bound, would be that state store; depth one in memory is not.

It lives on the host for the reason in §2 and will never be exposed over HTTP. A host still on the
Bun runtime (≤ 0.28.0) takes the one-time hop in usage.md §9 — the same `--resume` phase, entered
by running the installer by hand once.

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

## Distribution: one static binary

`pstack` is a **host-installed CLI** — it runs on the host, over SSH or from systemd, precisely
where the API cannot reach (§2). So the artifact has to be installable without the repository, and
without a runtime the host may not have:

```bash
curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh | sh
pstack --help
```

A release (GoReleaser, `.goreleaser.yaml`) is `pstack_{linux,darwin}_{amd64,arm64}` — raw static
executables, `CGO_ENABLED=0`, ~17 MB — plus `checksums.txt` and the installer. The installer
verifies the checksum before the binary is moved into place, atomically. `go install …/cmd/pstack`
works too.

### Why Go, and why static

The control plane is a long-lived process on a small host that shells out to docker. Until 0.28.0
it was a Bun/TypeScript bundle: small, but it put a JavaScript runtime on every host and inside
every control image, and its concurrency was the event loop's. The Go binary (0.29.0, ported
against the black-box conformance suite until byte-identical — docs/port-status.md) has no runtime
beside it, a smaller control image, goroutines where the reference had implicit single-threading
(with real mutexes where the product's guarantees live: the per-stack job lock, the delivery
queues), and `-race` in its test run. `CGO_ENABLED=0` is what makes "static" true: the SQLite
driver is pure Go, so the binary runs on any libc — or none (Alpine).

### The control image is built from the binary, on the host

`pstack build-image` writes a Dockerfile (`pstack dockerfile` prints it) on
`debian:bookworm-slim` with `bash`, `ca-certificates`, `curl` and the docker CLI + compose plugin
lifted from `docker:28-cli`. Its only application step is the release's own checksum-verifying
`install.sh`, **pinned to the version that generated the file**, so the context is EMPTY — the build
needs nothing on the host but docker, and it works from macOS because the build itself runs Linux.
The image that was running is retagged `pstack:local-previous` first, every time — the one-line
rollback. `PSTACK_BINARY=<path>` copies a local binary in instead (a version that is not published
yet, or no network at build time). Nothing is published to a registry: `init` wants an image that
exists locally, and the host can make one from what it has.

### Assets are embedded, not read from disk

The web UI, the share page, `templates/control/docker-compose.yml` and the cloud-init template are
`//go:embed`ded (`packages/pstack/assets.go`, five explicit paths — never a glob, so the READMEs
beside them do not ship). Nothing resolves a path relative to a source tree at runtime; the
failure mode where a tool passes every local test and then `init` dies on a missing template on
the one host that matters cannot occur.

The consequence to remember: **editing `templates/control/docker-compose.yml` requires a rebuild.**
The file on disk is the build input, not what a running `pstack` reads.

---

## See also

- [`../README.md`](../README.md) — the design rationale: axes, the `down`/`verify` asymmetry, scope
- [`usage.md`](usage.md) — the task guide, including the CLI workflows CI should use
- [`bootstrap.md`](bootstrap.md) — building a host from scratch
- [`../packages/pstack/internal/registry/registry.go`](../packages/pstack/internal/registry/registry.go) — the storage contract, heavily commented
- [`../packages/pstack/internal/spec/spec.go`](../packages/pstack/internal/spec/spec.go) — `kind`, `requires`, interpolation
