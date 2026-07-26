# Bootstrapping a preview host

How to go from an empty cloud account to a host that runs `pstack`. The worked example is **Hetzner
Cloud + cloud-init**; every step names what it does so the AWS / GCP / bare-metal equivalents are
mechanical (see [Adapting to other providers](#8-adapting-to-other-providers)).

Read [`../README.md`](../README.md) first — this document assumes you know what an axis is and why
`down` is best-effort while `verify` is strict — then
[`control-plane.md`](control-plane.md), which explains why the **control stack is brought up from
the host and never by the API**. That rule is what shapes this bootstrap: cloud-init's job is to
install Docker, install `pstack` as a global package, get a control image onto the box, and hand
over to `pstack init`.

**The default path needs no DNS credential.** `pstack init --domain … --acme-email …` uses
**HTTP-01**, which Traefik answers on port 80. There is no API token to create, store, or rotate.
DNS-01 (one wildcard certificate) is still there — it is `--challenge dns01 --dns-provider <code>`
— and [§3](#3-dns-and-tls-two-records-then-one-choice) tells you exactly when the arithmetic says
to switch.

---

## 1. What you are building

One host, three layers.

**Control** — Traefik owns 80/443 and terminates TLS; one `pstack` container serves both the API
and the UI. This is the layer `pstack init` creates, from the host, and the only layer the API may
not manage. **Shared** — the always-on singletons previews borrow (a database, a queue cluster, a
registry mirror), each a `kind: shared` deployment. **Isolated** — one Compose project per PR
(`docker compose -p pr-123`), attached to two **external** Docker networks.

```
                        DNS:  preview.example.com      A → 203.0.113.10
                              *.preview.example.com    A → 203.0.113.10
                                        │
                                    :80 │ :443      (:80 is NOT optional — HTTP-01 answers there)
┌───────────────────────────────────────▼──────────────────────────────────────────┐
│  preview host (one VM)                                                           │
│                                                                                  │
│  ┌─ CONTROL stack — `pstack init`, from the host, never from the API ───────┐    │
│  │  traefik  ── docker provider (labels) + file provider (traefik-dynamic/) │    │
│  │             ── HTTP-01 (default): one cert per hostname, on demand       │    │
│  │             ── DNS-01 (opt-in):   ONE router requests *.preview.example… │    │
│  │  pstack   ── the API *and* the UI, one container, port 7878, no host port│    │
│  │               control.preview.example.com → UI                          │    │
│  │               api.preview.example.com     → same container, honest name  │    │
│  └────────────┬──────────────────────────────────────┬─────────────────────┘    │
│               │                                      │                           │
│   network: preview-ingress (external)    network: preview-shared (external)      │
│      Traefik ⇄ per-PR web services         per-PR services ⇄ shared infra        │
│               │                                      │                           │
│               │        ┌─ SHARED deployments (kind: shared, no axes) ─────┐      │
│               │        │  postgres · queue cluster · registry mirror …    │      │
│               │        └──────────────────┬──────────────────────────────┘      │
│               │                           │ referenced by `requires:`            │
│      ┌────────┴────────┐  ┌───────────────┴─┐  ┌─────────────────┐               │
│      │ project pr-123  │  │ project pr-124  │  │ project pr-131  │  …            │
│      │  backend        │  │  backend        │  │  backend        │  ISOLATED     │
│      │  frontend       │  │  frontend       │  │  frontend       │  deployments  │
│      │  + its own      │  │  + its own      │  │  + its own      │               │
│      │    default net  │  │    default net  │  │    default net  │               │
│      └─────────────────┘  └─────────────────┘  └─────────────────┘               │
│                                                                                  │
│  host state:  /var/lib/pstack/control/…   (compose file, .env, dns.env, dynamic) │
│               /var/lib/pstack/deployments/…  (the registry)                      │
└──────────────────────────────────────────────────────────────────────────────────┘

per-PR resources Compose does NOT own — these are pstack axes:
   database branch · queue/workflow namespace · per-PR images · ingress assertion
```

Two networks, not one, because they answer different questions: `preview-ingress` is
"who may Traefik route to", `preview-shared` is "who may reach shared infrastructure". A per-PR
service that needs the database but must not be publicly routable joins only the second.

Both are **external** — created by `pstack init`, referenced (never created) by the shared stack
*and* by every per-PR compose file. If one side declares them and the other doesn't, you get the
404 in [§10](#10-troubleshooting).

### The three hostnames

| Hostname | Serves | Notes |
|---|---|---|
| `control.preview.example.com` | the web UI | what an operator opens |
| `api.preview.example.com` | the HTTP API | for CI, `curl`, scripts |
| `<service>.preview.example.com` | a shared service's own hostname | the convention for `kind: shared` deployments |
| `<surface>-pr-<n>.preview.example.com` | one per-PR surface | e.g. `backend-pr-123.…` |

`control.` and `api.` point at the **same container** — the API process serves the UI — and the UI
calls the API with **relative** `/api/…` paths. That is deliberate: a UI loaded from `control.` talks
to `control./api/…`, so it is same-origin and needs no CORS, no cross-origin cookie rules, and no
reconfiguration if the hostnames change. `api.` exists so external callers have a stable, honest
name that is not "the UI host". (The older `pstack.<domain>` is gone.)

---

## 2. Prerequisites

| Need | Why |
|---|---|
| A Hetzner Cloud project + API token | `hcloud` creates the server |
| `hcloud` CLI, authenticated (`hcloud context create preview`) | server + firewall management |
| A domain you control the DNS for | the apex + wildcard A records (§3) |
| **Port 80 reachable from the internet** | HTTP-01 answers the ACME challenge there. This is the one hard requirement of the default path — and it is needed for **renewals**, not just first issuance |
| A **control image** on the box | `pstack init` refuses to run without one, and it does **not** pull. See [§4](#the-control-image-the-one-thing-a-global-install-does-not-give-you) |
| An SSH public key uploaded to the project (`hcloud ssh-key create`) | you will not be using a password |
| ~~A DNS provider API token~~ | **not needed.** Only the opt-in `--challenge dns01` path wants one (§3) |
| A git remote holding your `preview.yml` + `hooks/` | only if you drive the CLI **on the host**; specs submitted over HTTP travel as strings |

Bun is a runtime requirement (`engines.bun >= 1.3.0`) — cloud-init installs it in §4. There is no
Node fallback: pstack uses `Bun.serve`, `Bun.YAML`, `Bun.spawn` and `Bun.file`, which have no Node
equivalent.

### Sizing: previews are RAM-hungry and Docker gives you no protection by default

**A container has no memory limit unless you set one.** One runaway heap in PR #123 can trigger the
kernel OOM killer, which will happily reap a container belonging to PR #124 — or Traefik. This is
the single most common way a shared preview host becomes flaky.

Set `mem_limit` on **every** service in your per-PR compose file:

```yaml
services:
  backend:
    mem_limit: 768m          # a limit that OOMs one stack beats one that OOMs the box
    mem_reservation: 256m
```

Then size the host from the numbers you just wrote down:

```
host RAM  ≳  (Σ mem_limit per stack) × (peak concurrent stacks)
             + control stack (Traefik 256 MB + pstack 512 MB, both capped in the template)
             + any shared DB / queue
             + 2 GB for the kernel, Docker, and image builds
```

Builds are the spike you forget: a Next.js production build can transiently want more than the
service it produces. If you build on the host rather than in CI, budget for
`concurrent builds × build peak` on top.

Disk matters for a second-order reason. Per-PR images accumulate — `docker compose down -v` never
removes them — and once the disk fills, Docker evicts the build cache, which is the single biggest
lever on per-PR deploy time. That is precisely what the `images` axis in
[`../examples/preview.yml`](../examples/preview.yml) exists to prevent. Give yourself headroom
anyway.

Pick a concrete server type from live data rather than a number in a doc:

```bash
hcloud server-type list      # current vCPU / RAM / disk per type
hcloud location list
```

A shared 8-vCPU / 16 GB class machine (e.g. `cpx41` at the time of writing) comfortably holds a
handful of concurrent two-service stacks with limits set. Verify against the CLI output.

---

## 3. DNS and TLS: two records, then one choice

### The two records

Create exactly two, both pointing at the server's public IP:

| Record | Type | Value |
|---|---|---|
| `*.preview.example.com` | A | `203.0.113.10` |
| `preview.example.com` | A | `203.0.113.10` |

Add `AAAA` records too if you enable IPv6 on the server.

**You want the wildcard in both modes**, for different reasons. Under DNS-01 it pairs with a
wildcard *certificate*, so a new hostname needs no provisioning at all. Under HTTP-01 it still means
a new per-PR hostname **resolves** the moment you invent it — only the certificate is per-host. Without
the wildcard record, every deploy grows a DNS step and a teardown step: two more axes, two more
things to leak.

### The wildcard matches exactly ONE label

| Hostname | Covered by `*.preview.example.com`? |
|---|---|
| `backend-pr-123.preview.example.com` | ✅ yes |
| `backend.pr-123.preview.example.com` | ❌ **no** — two labels deep |

So flatten with dashes. [`../examples/preview.yml`](../examples/preview.yml) already does
(`backend-${STACK}.${PREVIEW_DOMAIN}` → `backend-pr-123.preview.example.com`). Invent a dotted
scheme and the name does not even resolve — and under DNS-01 it also misses the certificate, so
Traefik falls back to its self-signed default and browsers throw `ERR_CERT_AUTHORITY_INVALID` on a
setup that otherwise looks correct.

### HTTP-01: the default, and the whole reason this box needs no secrets

`pstack init --domain preview.example.com --acme-email ops@example.com` renders these two Traefik
flags and nothing else:

```
--certificatesresolvers.le.acme.httpchallenge=true
--certificatesresolvers.le.acme.httpchallenge.entrypoint=web      # the :80 entrypoint
```

What you get, and what it costs:

| | HTTP-01 |
|---|---|
| Credentials | **none** — nothing to create, store, mount, or rotate |
| Requirement | **port 80 reachable from the internet** |
| Works behind the `web`→`websecure` redirect? | **Yes.** Traefik installs an internal ACME router at maximum priority that bypasses the redirect for `/.well-known/acme-challenge/`. You do not need to poke a hole in the redirect |
| Wildcards | **impossible.** HTTP-01 cannot issue a wildcard; every hostname gets its own certificate |
| A URL before its stack is deployed | **no certificate.** HTTP-01 proves control by answering *on that hostname*, so a host with no container cannot be certified. The preview link is only valid once the stack is actually up |
| Ceiling | Let's Encrypt allows **~50 new certificates per registered domain per week** |

**Do the arithmetic before you scale.** Renewals do not count against that 50; only *new*
certificates do (and the separate duplicate-certificate limit is 5/week). With three surfaces per PR
— backend, frontend, admin — each new PR asks for three new certificates:

```
50 new certs / registered domain / week  ÷  3 surfaces per PR  ≈  16 new PRs per week
```

Past roughly sixteen new PRs in a rolling week, issuance starts failing and previews come up with
Traefik's self-signed certificate. That number — not concurrency, not host size — is the reason to
move to DNS-01.

While you are iterating on ingress config, point the resolver at Let's Encrypt's **staging** CA so
you are not spending that budget on experiments:

```
--certificatesresolvers.le.acme.caserver=https://acme-staging-v02.api.letsencrypt.org/directory
```

Staging certificates are untrusted by browsers (expected) and have far looser limits. Note this is a
hand edit to the rendered compose file — see the warning in [§5](#init-owns-the-compose-file) about
`init` rewriting it.

### Switching to DNS-01: opt in when the arithmetic says so

> **Switch when:** you exceed ~16 new PRs/week (above), or you need a preview URL to present valid
> TLS **before** its stack is deployed. DNS-01's wildcard is valid immediately, for every present
> and future hostname, because Traefik proves control of the *zone* rather than of a hostname.
> The price is exactly one thing: a DNS API credential you must obtain, store, and rotate.

```bash
PSTACK_DNS_TOKEN=<zone-scoped-token> pstack init \
  --domain preview.example.com \
  --acme-email ops@example.com \
  --challenge dns01 \
  --dns-provider hetzner          # required for, and only used by, dns01
```

`init` then renders DNS-01 flags instead — including `resolvers=1.1.1.1:53` (ask the authoritative
side, so a stale local cache cannot fail the precheck) and
`propagation.delaybeforechecks=30s` — and writes the credential to
`/var/lib/pstack/control/dns.env` at mode `0600`, which Traefik reads via `env_file:`.

**The mode is not remembered.** `init` reads it from flags or the environment on **every** run and
defaults to `http01` when neither is present — nothing on disk is consulted. So on a DNS-01 host,
pin it in the environment rather than relying on anyone retyping the flags:

| Flag | Environment fallback | Default if neither |
|---|---|---|
| `--domain` | `PSTACK_DOMAIN` | *(fails: `init needs --domain`)* |
| `--acme-email` | `PSTACK_ACME_EMAIL` | *(fails: `init needs --acme-email`)* |
| `--challenge http01\|dns01` | `PSTACK_CHALLENGE` | **`http01`** |
| `--dns-provider <lego-code>` | `PSTACK_DNS_PROVIDER` | *(fails on dns01 only)* |
| *(no flag)* | `PSTACK_DNS_TOKEN` | empty `dns.env` |
| *(no flag)* | `PSTACK_IMAGE` | `pstack:local` |
| *(no flag)* | `PSTACK_TOKEN` | generated, printed once |

Forget `--challenge dns01` on a later run and `init` silently re-renders the stack as HTTP-01 —
the wildcard router is gone while every per-PR file still carries DNS-01-shaped labels. See
[§7](#upgrading-and-rotating-re-run-init).

Verified credential names (do not guess others — a wrong variable name surfaces as an ACME
"propagation timeout", which sends you debugging DNS instead of a typo):

| Provider | lego code | Variable | Notes |
|---|---|---|---|
| Hetzner DNS | `hetzner` | `HETZNER_API_TOKEN` | plus optional `HETZNER_PROPAGATION_TIMEOUT` (seconds) and `HETZNER_TTL`. **Never** `HETZNER_API_KEY` — no such variable |
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN` | one token, scoped **Zone:Read + DNS:Edit** |
| Route 53 | `route53` | *none* | tokenless: the EC2 **instance profile** satisfies the AWS credential chain |
| Cloud DNS | `gcloud` | *none* | tokenless: the VM's **attached service account** via Application Default Credentials |

Every lego variable also accepts a `_FILE` suffix if you would rather mount the secret than set it.
Anything not in this table: look it up in
[lego's provider list](https://go-acme.github.io/lego/dns/) — `init` writes a `CHANGEME_VARIABLE_NAME`
line rather than guessing.

### The per-router rule is the OPPOSITE in each mode

This is the easiest thing in the whole setup to get wrong, because the correct label under one mode
is the bug under the other.

| | **HTTP-01 (default)** | **DNS-01** |
|---|---|---|
| Who requests certificates | **every router, for itself** | **exactly one** always-on router |
| Per-PR router TLS labels | `tls=true` **and** `tls.certresolver=le` — it must resolve its own cert | `tls=true` and **nothing else** — inherit the wildcard by SNI |
| Adding `certresolver` to a per-PR router | **required** | **the rate-limit trap**: Traefik orders a separate certificate and burns the ~50/week |
| Where the wildcard is requested | nowhere — wildcards are impossible | the control router: `tls.domains[0].main=${DOMAIN}` + `tls.domains[0].sans=*.${DOMAIN}` |

`init` renders the control stack's own labels correctly for the mode you chose. **Your per-PR compose
files are yours** — see the contract in [§5](#what-a-per-pr-compose-file-must-declare) and change
those labels when you switch modes.

---

## 4. The cloud-init file

Save as `cloud-init.yaml`. Replace every `example.com`, `<you>` and `CHANGEME` placeholder.

The file does four things, in this order: install **Docker**, install **Bun + pstack (a global
package)**, get a **control image** onto the box, then hand over to **`pstack init`**. The split
matters — `init` owns the control stack's compose file, `.env` and `dns.env`; everything else on the
box is a *host input* it mounts rather than owns, so re-rendering the stack never touches your
inputs and rotating a secret never means re-rendering the stack.

### `pstack` installs globally

There is no `git clone` of pstack and no `bun install`. What ships on npm is a **bundle**:
`dist/cli.js` (~74 KB) and `dist/index.js`, both `--target=bun`, minified, with sourcemaps. The
published tarball is **8 files / 0.36 MB** and contains **no source, no docs, no examples, no
skills** — the UI and the control-stack compose template are `with { type: 'text' }` imports inlined
into the bundle, so nothing is read from a path relative to the source at runtime.

```bash
bun add -g @samyx/preview-stacks     # or: npm i -g @samyx/preview-stacks
pstack --help
```

Working from a checkout (`bun src/cli.ts …`) is now the **contributor** path, not the user path.

> **Why a bundle and not `bun build --compile`?** A standalone executable bakes in the Bun runtime
> (~60 MB) *per platform*, which for npm means either a 5-platform `optionalDependencies` matrix or a
> postinstall download. A bundle is ~74 KB and Bun is already required (`engines.bun`), and there is
> no Node fallback to preserve because `Bun.serve` / `Bun.YAML` / `Bun.spawn` / `Bun.file` have no
> Node equivalent.

**Mind the global bin directory.** `bun add -g` links its shims into `$BUN_INSTALL/bin`; the
cloud-init below installs Bun with `BUN_INSTALL=/usr/local`, so the shim lands at
`/usr/local/bin/pstack` — on `PATH` for every user, including a systemd unit or a `runcmd` step with
no `HOME`. It prints `bun pm bin -g` and then runs `/usr/local/bin/pstack --help`, so a wrong bin
directory fails loudly at boot instead of quietly later.

### The control image: the one thing a global install does not give you

`pstack init` **requires the control image to already exist locally**. Its precondition is
`docker image inspect <image>`, which **does not pull** — so on a fresh box the default
`pstack:local` does not exist and `init` stops with `image pstack:local not found` before creating
anything. This is a real gap in the credential-free happy path; pick one of two answers explicitly:

| Option | What you do | Cost |
|---|---|---|
| **Build on the host** *(what the cloud-init below does)* | `git clone` this repo, `docker build -t pstack:local .`, then `init` with **no** `PSTACK_IMAGE` — `pstack:local` is already the default | needs a pstack **source checkout** on the box, plus ~1–2 min of build time and some RAM per upgrade |
| **Pull from a registry** | `docker pull <registry>/pstack:<tag>`, then run `init` with `PSTACK_IMAGE=<registry>/pstack:<tag>` | you must publish the image somewhere the box can pull. **No source on the host**, and boots are seconds instead of minutes |

**Build-on-host is the default here** because it needs nothing published: the image always matches
the source on that box, and the same checkout supplies both the `Dockerfile` and — until the package
is on a registry — the `pstack` binary itself. The checkout is not wasted; `git pull && bun
scripts/build.ts` upgrades the CLI, and re-running `docker build` upgrades the control image.

Switch to a registry pull when build time per boot starts to hurt, or when you want boxes that carry
no source at all.

`PSTACK_IMAGE` is read from the environment on **every** `init` run and written into
`control/.env`. If you *do* move to a registry, pass it on every run — re-running `init` without it
falls back to `pstack:local`, which is how an upgrade turns into "image not found" on a box that was
working. (Or tag your pull as `pstack:local` and keep the default.)

> **Secret handling.** On the default HTTP-01 path there is **no DNS secret at all** — nothing in
> user-data, nothing on disk, nothing to rotate. What remains is `PSTACK_TOKEN`, the API's bearer
> token: set it below and it lands in the instance's user-data, which the provider stores and which
> is readable from the instance metadata service. Prefer to **omit it** — `init` generates one,
> stores it `0600` in `/var/lib/pstack/control/.env`, and prints it once (into
> `/var/log/cloud-init-output.log`, so read it and then treat that log as sensitive). On the DNS-01
> path, `PSTACK_DNS_TOKEN` is a **separate** secret with a smaller blast radius — it can edit one DNS
> zone; `PSTACK_TOKEN` can start privileged containers. Never reuse one for both. If user-data is too
> exposed for your threat model, leave both out, boot, then `scp` in and re-run `init`.

```yaml
#cloud-config

# The `docker` group must exist before the `users` stage, because a user cannot be added to a
# group that does not exist yet — and Docker is not installed until `runcmd`. Docker's postinst
# reuses this group rather than creating its own.
groups:
  - docker

users:
  - default
  - name: preview
    gecos: preview stack operator
    shell: /bin/bash
    groups: [docker]
    sudo: 'ALL=(ALL) NOPASSWD:ALL'
    lock_passwd: true
    ssh_authorized_keys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... you@laptop

package_update: true
packages:
  - ca-certificates
  - curl
  - git
  - unzip          # the Bun installer needs it
  - jq
  - openssl       # for a basic-auth hash, if you add one (§5)

write_files:
  # `pstack init` renders /var/lib/pstack/control/{docker-compose.yml,.env,dns.env} itself — do not
  # hand-write those, they would be silently overwritten on the next run. It also creates an empty
  # control/traefik-dynamic/ and mounts it into Traefik read-only; anything you drop in THERE is
  # watched and hot-reloaded, and is the supported place to extend Traefik.
  #
  # NOTHING but dynamic configuration may live in that directory — see the htpasswd trap in §5.

  # ── Optional: a fallback router (DNS-01 setups only) ────────────────────────────────────
  # Serves a stable upstream for `<surface>-pr-<n>` hostnames whose stack is torn down (or not
  # deployed yet), so a stale preview link degrades to something useful instead of a 404.
  #
  # It sets only `tls: {}` — it requests NO certificate, it borrows one. Under DNS-01 the wildcard
  # already covers those names, so the handshake succeeds. Under the default HTTP-01 setup there is
  # no wildcard and a regexp rule names no single host to certify, so this router can only serve
  # Traefik's self-signed default: useful as a plain-HTTP courtesy at best. Skip it on HTTP-01.
  - path: /var/lib/pstack/control/traefik-dynamic/fallback.yml
    permissions: '0644'
    owner: root:root
    content: |
      http:
        routers:
          preview-fallback:
            # Anchored Go regexp with escaped dots — Traefik v3 dropped v2's `{name:...}` syntax.
            rule: "HostRegexp(`^[a-z]+-pr-[0-9]+\\.preview\\.example\\.com$`)"
            # Priority 1 loses to every per-PR router (which are exact Host() matches at 100),
            # so it only answers when that PR's stack is not running.
            priority: 1
            entryPoints: [websecure]
            service: fallback
            tls: {}
        services:
          fallback:
            loadBalancer:
              servers:
                - url: "https://api.dev.example.com"
              # The upstream should see its own Host, not backend-pr-7.preview.example.com.
              passHostHeader: false

runcmd:
  # 1. Docker CE + the compose plugin, from the official apt repository.
  - install -m 0755 -d /etc/apt/keyrings
  - curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  - chmod a+r /etc/apt/keyrings/docker.asc
  - >
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc]
    https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable"
    > /etc/apt/sources.list.d/docker.list
  - apt-get update
  - DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io
      docker-buildx-plugin docker-compose-plugin
  - systemctl enable --now docker

  # 2. Bun, installed system-wide.
  #    runcmd runs as root, so a default install lands in /root/.bun where the `preview` user
  #    cannot execute it. BUN_INSTALL=/usr/local puts the binary at /usr/local/bin/bun.
  - BUN_INSTALL=/usr/local bash -c 'curl -fsSL https://bun.sh/install | bash'
  - /usr/local/bin/bun --version

  # 3. pstack, as a GLOBAL PACKAGE — a ~74 KB bundle, no checkout, no `bun install`.
  #    BUN_INSTALL again, so the shim lands at /usr/local/bin/pstack instead of /root/.bun/bin.
  #    The last two lines are the check: print where the shim went, then run it by absolute path so
  #    a wrong bin directory fails HERE, in cloud-init-output.log, and not later under a different
  #    HOME. (Installing a local tarball — `npm i -g ./samyx-preview-stacks-<v>.tgz` — is the same
  #    8-file artifact if you are on a pre-release build.)
  # `bun add -g @samyx/preview-stacks` is the path once the package is published. Until then —
  # and whenever you want the image built on the box rather than pulled — install from the
  # checkout, which is also what supplies the Dockerfile in step 4.
  - git clone --depth 1 https://github.com/<you>/preview-stacks.git /opt/preview/pstack
  - cd /opt/preview/pstack && bun install --frozen-lockfile && bun scripts/build.ts
  # Expose it on PATH with a symlink, NOT `bun link`: `bun link` registers the package so another
  # project can depend on it, but it does not install the `bin` shim, so `pstack` stays unknown to
  # the shell. The bundle carries `#!/usr/bin/env bun` and the exec bit (scripts/build.ts sets
  # both), so a symlink is all that is needed — and it keeps pointing at the checkout, so a later
  # `git pull && bun scripts/build.ts` upgrades the CLI with no reinstall.
  - ln -sf /opt/preview/pstack/dist/cli.js /usr/local/bin/pstack
  - BUN_INSTALL=/usr/local /usr/local/bin/bun pm bin -g
  - /usr/local/bin/pstack --help

  # 4. The control image. `init`'s precondition is `docker image inspect`, which does NOT pull, so
  #    it must be here BEFORE init runs (§4). Registry option shown; the alternative is to clone
  #    this repo and `docker build -t pstack:local .` — see the table above.
  #    BUILD ON THE BOX from the checkout above. The tag `pstack:local` is PSTACK_IMAGE's default,
  #    so `init` needs no PSTACK_IMAGE at all — nothing to publish, no registry login, and the
  #    image always matches the source on this host. Costs ~1–2 min of build time per boot; switch
  #    to a registry pull when that matters (§4).
  - cd /opt/preview/pstack && docker build -t pstack:local .

  # 5. Optional: your preview config, for driving the CLI from the host or from a cron sweep.
  #    Not needed for the API path — a submitted spec travels over HTTP as a string.
  - git clone --depth 1 https://github.com/<you>/<your-project>.git /opt/preview/config
  - chown -R preview:preview /opt/preview

  # 6. Stand up the control stack — FROM THE HOST, via `pstack init`.
  #
  #    This is deliberately the host's job and never the API's: the API cannot recreate the stack
  #    that contains it without killing the process doing the work, and a bad image would leave
  #    the box with no control plane and no remote way in (control-plane.md §2).
  #
  #    `init` is idempotent — it renders /var/lib/pstack/control/docker-compose.yml, creates both
  #    external networks, writes .env and dns.env at mode 0600, `compose up -d`s the
  #    `pstack-control` project, and then WAITS for the API's healthcheck. Re-running it after an
  #    image bump is the upgrade path.
  #
  #    No --challenge, so HTTP-01: no DNS provider, no DNS token, nothing to rotate. For a wildcard
  #    instead, add `--challenge dns01 --dns-provider hetzner` and PSTACK_DNS_TOKEN=… (§3).
  #
  #    PSTACK_TOKEN: supply your own to keep a known value; OMIT it (preferred) and `init` generates
  #    one, stores it 0600 in control/.env and prints it once — into cloud-init-output.log.
  - >
    PSTACK_DATA=/var/lib/pstack
    PSTACK_TOKEN=CHANGEME_PSTACK_API_TOKEN
    /usr/local/bin/pstack init
    --domain preview.example.com
    --acme-email ops@example.com

  # 7. Verify before you walk away — HOST-LOCAL only. There is no published host port (the pstack
  #    container is reached through Traefik), and DNS/TLS may not be ready this early, so check the
  #    container's own healthcheck — the same probe `init` waits on. Public checks are §6.
  - docker compose -p pstack-control ps
  - docker inspect -f '{{.State.Health.Status}}' "$(docker compose -p pstack-control ps -q pstack)"

  # NOTE: no systemd unit for `pstack serve`. The API runs as a CONTAINER in the control stack that
  # `init` just created (restart: unless-stopped, so Docker restarts it on boot). A systemd unit
  # would be a second, competing copy fighting over the same registry directory. Run `serve` under
  # systemd only if you deliberately choose NOT to use `init`.
```

### Create the server

```bash
hcloud server create \
  --name preview-host \
  --type cpx41 \
  --image ubuntu-24.04 \
  --location fsn1 \
  --ssh-key my-key \
  --user-data-from-file cloud-init.yaml
```

Then watch it finish. cloud-init is asynchronous — SSH works long before it is done, which is why
"it didn't work" usually means "it wasn't finished".

```bash
ssh preview@203.0.113.10

cloud-init status --wait          # blocks until done; prints `status: done` or `error`
cloud-init status --long          # which stage failed
sudo tail -n 200 /var/log/cloud-init-output.log   # the actual command output — read this
```

`cloud-init-output.log` is the only file that shows you the apt/curl/docker output, **and** the
generated `PSTACK_TOKEN` if you let `init` create one. Read it before theorising, then treat it as
sensitive.

---

## 5. The control stack, explained

`init` renders [`../templates/control/docker-compose.yml`](../templates/control/docker-compose.yml)
to `/var/lib/pstack/control/docker-compose.yml`, substituting exactly two markers — the ACME
challenge flags and the control router's TLS labels — and leaving everything else byte-for-byte.
These are the decisions inside it.

| Decision | Why |
|---|---|
| **Both providers enabled** (`docker` + `file`) | The docker provider turns per-PR container labels into routes automatically. The file provider holds the things no container owns: middlewares, static routes you add by hand. |
| **`exposedbydefault=false`** | Opt-in routing. A container is only routed if it carries `traefik.enable=true`. Without this, every service in every PR — including internal ones — gets a public route derived from its name. |
| **`providers.docker.network=preview-ingress`** | Tells Traefik which of a container's networks to dial. A per-PR service sits on `preview-ingress`, `preview-shared`, *and* its project's `default`; without this Traefik may pick one it cannot reach and you get a 502 that looks like an app bug. Per-service overrides use the `traefik.docker.network` label. |
| **All of `:80` redirected to `:443`** | …and it does **not** break HTTP-01: Traefik's internal ACME router sits at maximum priority and bypasses the redirect for `/.well-known/acme-challenge/`. |
| **Traefik's socket mount is `:ro`, pstack's is read-write** | Traefik only enumerates containers, labels and networks. pstack must create and destroy containers, networks and volumes — that is what `PSTACK_TOKEN` protects. |
| **Certificate ownership depends on the mode** | Under HTTP-01 each router resolves its own certificate (`tls.certresolver=le` everywhere). Under DNS-01 exactly one always-on router asks for `main: preview.example.com` + `sans: *.preview.example.com` and everything else sets `tls=true` alone. See the table in [§3](#the-per-router-rule-is-the-opposite-in-each-mode). |
| **Pinned `traefik:v3.6.1` or newer** | Earlier 3.x releases cannot talk to Docker Engine 29's API; the docker provider then comes up empty and every route 404s while Traefik looks perfectly healthy. |

### `init` owns the compose file

`init` rewrites `control/docker-compose.yml` **unconditionally on every run**. Any hand edit —
`--api.dashboard=true`, a raised `propagation.delaybeforechecks`, the staging CA server, an extra
volume mount — is lost the next time you run it (which is also the upgrade and token-rotation path).
Two consequences worth internalising:

- **There is no Traefik dashboard.** The template passes no `--api.dashboard`, so "check the
  dashboard" is not a debugging step on this host — use container labels and Traefik's logs
  ([§10](#10-troubleshooting)). Enabling it means a hand edit you will have to re-apply.
- **Extend through `traefik-dynamic/`, not through the compose file.** That directory is a host
  input `init` creates and mounts but never rewrites. Anything expressible as *dynamic*
  configuration (routers, services, middlewares) belongs there; anything that needs a *static* CLI
  flag does not, and you should think twice.

### The htpasswd trap

The file provider **watches its directory and parses every file in it as dynamic configuration** —
`/var/lib/pstack/control/traefik-dynamic/` on the host, `/etc/traefik/dynamic/` in the container.
Put a `dashboard.htpasswd` there and Traefik tries to read `admin:$apr1$…` as YAML/TOML, logs a
parse error, and — depending on version and timing — discards the rest of the directory's
configuration along with it. Routers disappear, certificates are never requested, and the visible
symptom is "no certificate", which points you at ACME instead of at a misplaced file.

The rule generalises: **nothing but dynamic config in the watched directory.** If you add a
basic-auth middleware, note that the rendered stack mounts *only* the socket, the `letsencrypt`
volume and `traefik-dynamic/` — so a `usersFile` would need a mount `init` does not create. Inline
the hashes in the middleware YAML instead:

```yaml
# /var/lib/pstack/control/traefik-dynamic/auth.yml   — dynamic config, so it belongs here
http:
  middlewares:
    ops-auth:
      basicAuth:
        users:
          # openssl passwd -apr1 '…'  — a literal $ must be written $$ in a compose env, but this
          # file is read by Traefik, not by Compose, so paste the hash as-is.
          - "admin:$apr1$CHANGEME"
```

### Shared deployments live *beside* the control stack, not inside it

A shared Postgres, a queue cluster, a registry mirror — anything every preview borrows — is its own
`kind: shared` deployment, not another service in the control stack. Two reasons:

- **Blast radius.** The control stack is upgraded from the host and never torn down; a shared
  database is a normal deployment you may legitimately want to `down --force` one day. Folding it
  into the control stack means every change to it recreates Traefik.
- **`requires:` needs something to point at.** An isolated deployment declares
  `requires: [{ name: shared-queue, assert: … }]` and fails by name before it creates anything. That
  only reads cleanly when the dependency is a deployment with an identity.

See [`../examples/shared.yml`](../examples/shared.yml) — and note its `down` is refused without
`--force`, because `compose down -v` there destroys the state every tenant depends on.

### What a per-PR compose file must declare

The `${...}` values below are **Compose's own** interpolation, resolved from the process environment
that pstack hands to `docker compose`: the spec's `env:` block, plus `STACK`, plus every `KEY=VALUE`
line an axis printed on stdout (that is how `DATABASE_URL` arrives from the database axis). You do
not need — and should not add — a `.env` file for them.

```yaml
services:
  backend:
    image: ${REGISTRY}/backend:${IMAGE_TAG}
    profiles: [backend]        # every service behind a profile: a bare `up` starts nothing
    mem_limit: 768m
    networks: [preview-ingress, preview-shared]
    environment:
      DATABASE_URL: ${DATABASE_URL}   # captured from the database axis' KEY=VALUE stdout
    labels:
      - traefik.enable=true
      - traefik.docker.network=preview-ingress
      - traefik.http.routers.${STACK}-backend.rule=Host(`backend-${STACK}.${PREVIEW_DOMAIN}`)
      - traefik.http.routers.${STACK}-backend.entrypoints=websecure
      - traefik.http.routers.${STACK}-backend.tls=true
      # ── HTTP-01 (default): this router resolves its OWN certificate, so it NEEDS the resolver.
      - traefik.http.routers.${STACK}-backend.tls.certresolver=le
      # ── DNS-01: DELETE the line above. `tls=true` alone inherits the wildcard by SNI; adding a
      #    certresolver here orders a separate certificate and burns the ~50-per-week limit.
      - traefik.http.services.${STACK}-backend.loadbalancer.server.port=3000

networks:
  preview-ingress:
    external: true             # must match the control stack's declaration
  preview-shared:
    external: true
```

Router names are namespaced with `${STACK}`: Traefik router names are global, so two PRs using
`backend` collide and one silently wins.

---

## 6. Verify the host

Run these in order; each one isolates a different failure.

```bash
# ── cloud-init actually finished ──────────────────────────────────────────────────────
cloud-init status --long

# ── pstack is on PATH, from the global bundle ─────────────────────────────────────────
pstack --help | head -3
bun pm bin -g          # where `bun add -g` put the shim; expect /usr/local/bin

# ── both external networks exist (created by `init`) ──────────────────────────────────
docker network ls --filter name=preview-
#   NETWORK ID   NAME              DRIVER   SCOPE
#   ...          preview-ingress   bridge   local
#   ...          preview-shared    bridge   local

# ── the control stack is up, and the API is HEALTHY (not merely created) ──────────────
docker compose -p pstack-control ps
#   NAME                      SERVICE   STATUS
#   pstack-control-traefik-1  traefik   Up
#   pstack-control-pstack-1   pstack    Up (healthy)
docker inspect -f '{{.State.Health.Status}}' "$(docker compose -p pstack-control ps -q pstack)"
#   healthy   — `up -d` exits 0 as soon as containers are CREATED, so this is the real check

# ── the public surfaces answer (this is also the DNS + TLS check) ──────────────────────
curl -sf https://control.preview.example.com/api/health | jq
curl -sf https://api.preview.example.com/api/health | jq
#   { "ok": true, "authEnforced": true, ... }
#   The UI is the same origin: open https://control.preview.example.com in a browser.

# ── read the ACME conversation ────────────────────────────────────────────────────────
docker compose -p pstack-control logs traefik | grep -iE 'acme|certificate|error'
#   HTTP-01: "Trying to challenge using HTTP-01" → "Served certificate", once per hostname
#   DNS-01:  "Trying to challenge using DNS-01"  → one certificate, with the wildcard SAN

# ── the certificate store is non-trivial ──────────────────────────────────────────────
docker run --rm -v pstack-control_letsencrypt:/le alpine \
  sh -c 'ls -l /le/acme.json && wc -c < /le/acme.json'
#   a few hundred bytes = empty skeleton; several KB = real certs
#   (volume name is pinned by `name: pstack-control` in the template)
```

### The certificate check depends on your mode

**HTTP-01 (default).** Check a hostname that **has** a container:

```bash
openssl s_client -connect 203.0.113.10:443 \
  -servername control.preview.example.com </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject -ext subjectAltName
#   want: issuer = Let's Encrypt, SAN = DNS:control.preview.example.com   (exactly one name)
```

A hostname that has **never been deployed** serving `TRAEFIK DEFAULT CERT` here is **correct, not a
failure** — HTTP-01 cannot certify a host with nothing answering on it. There is no wildcard to
look for.

**DNS-01.** Then, and only then, a never-deployed hostname is the interesting test — it is the whole
point of the wildcard:

```bash
openssl s_client -connect 203.0.113.10:443 \
  -servername nothing-is-deployed-here.preview.example.com </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject -ext subjectAltName
#   want: issuer = Let's Encrypt, SAN includes DNS:*.preview.example.com
#   "self-signed" / "TRAEFIK DEFAULT CERT" = the wildcard was never issued or never matched (§10)
```

---

## 7. Operating pstack on the host

### Where things live

`init` puts everything under `PSTACK_DATA` (default `/var/lib/pstack`):

| Path | Owner | What |
|---|---|---|
| `control/docker-compose.yml` | **`init`** (rewritten every run) | the control stack |
| `control/.env` | **`init`**, mode `0600` | `DOMAIN`, `ACME_EMAIL`, `PSTACK_IMAGE`, and `PSTACK_TOKEN` |
| `control/dns.env` | **`init`**, mode `0600` | the DNS-01 credential; written even on HTTP-01 (empty) so switching modes needs no extra step |
| `control/traefik-dynamic/` | **you** | host input: dynamic config, mounted read-only. `init` creates it empty and never touches its contents |
| `deployments/` | the API | the registry — the only thing bind-mounted into the pstack container |

`control/.env` is `0600` because `PSTACK_TOKEN` drives an API holding a read-write Docker socket:
anyone who can read that file owns the box. Note the pstack container mounts **only**
`deployments/` — the DNS credential is Traefik's business and the API has no reason to be able to
read it.

### Upgrading and rotating: re-run `init`

From the host, never over HTTP — the API cannot recreate the stack containing itself
([control-plane.md §2](control-plane.md#2-the-invariant-the-api-never-manages-its-own-stack)).

```bash
# 1. drain: jobs are in-memory, so an in-flight `up` is truncated by a restart
curl -s https://api.preview.example.com/api/jobs | jq '[.jobs[] | select(.state == "running")]'

# 2. new pstack CLI + control image, both from the checkout (build-on-host, §4).
#    /usr/local/bin/pstack is a symlink INTO this checkout, so rebuilding the bundle upgrades the
#    CLI in place — there is nothing to reinstall.
cd /opt/preview/pstack && git pull
bun install --frozen-lockfile && bun scripts/build.ts   # the CLI
docker build -t pstack:local .                          # the control image

# 3. re-run init — idempotent, and the ONLY supported way to change any of this.
#    Re-pass the FULL configuration, including the challenge mode (see the warning below).
#    No PSTACK_IMAGE needed: `pstack:local` is the default, and the build above just replaced it.
pstack init --domain preview.example.com --acme-email ops@example.com
#    …and on a DNS-01 host, that same command must carry the mode too:
# PSTACK_DNS_TOKEN=… pstack init --domain … --acme-email … \
#   --challenge dns01 --dns-provider hetzner

# 4. it waits for the healthcheck itself; confirm from outside too
curl -sf https://api.preview.example.com/api/health
```

The same command rotates secrets: `PSTACK_TOKEN=<new> pstack init …` rewrites `control/.env` and
recreates the container; `PSTACK_DNS_TOKEN=<new> pstack init … --challenge dns01 --dns-provider <code>`
rewrites `dns.env`. Omit `PSTACK_TOKEN` entirely and you are handed a fresh one, printed once.

> **`init` remembers nothing — it re-renders from its arguments.** Every value comes from a flag or
> an environment variable on *that* run; the existing `control/docker-compose.yml` and `.env` are
> overwritten, never read back. Two ways that bites during a routine image bump:
>
> - **Omit `PSTACK_IMAGE`** → it falls back to `pstack:local`, which does not exist, and the
>   precondition stops you (loud, harmless).
> - **Omit `--challenge dns01 --dns-provider <code>`** → it falls back to **HTTP-01** and rewrites
>   the control router's TLS labels: the wildcard is no longer requested, while every per-PR compose
>   file still carries DNS-01-shaped `tls=true`-only labels with no resolver. Every preview then
>   serves Traefik's self-signed certificate, from a command that looked like an image bump. **Silent
>   and site-wide** — this is the one to guard.
>
> Guard it by pinning the mode in the environment (`PSTACK_CHALLENGE`, `PSTACK_DNS_PROVIDER`,
> `PSTACK_DOMAIN`, `PSTACK_ACME_EMAIL`, `PSTACK_IMAGE` — see the table in
> [§3](#switching-to-dns-01-opt-in-when-the-arithmetic-says-so)) rather than trusting anyone to
> retype flags at 2am: put them in a root-owned `/etc/default/pstack-init` and
> `set -a; . /etc/default/pstack-init; set +a; pstack init`. Then `init` takes no flags at all and
> cannot drift.

Shared and isolated deployments keep running throughout. They are separate compose projects and
nothing about them depends on the API process being alive, so a broken control plane costs you the
ability to *change* things, not the previews themselves.

### The CLI, on the host

Everything except `init` and `serve` reads a spec, and `-f preview.yml` plus hook paths like
`./hooks/db-branch.sh` are **cwd-relative** — pstack never chdirs. So run it from your config
checkout:

```bash
cd /opt/preview/config
PR=123 pstack validate
PR=123 pstack up --dry-run    # prints every command, runs none
```

`--dry-run` is the fastest check of axis ordering, and it is free. This host-side CLI is for
operators and cron sweeps ([§9](#9-hardening-checklist)); it is **not** how the control plane runs
deployments — a spec submitted to the API travels over HTTP as a string and is executed inside the
pstack container, which mounts only the registry.

### Driving the API

```bash
API=https://api.preview.example.com

curl -s $API/api/health | jq
#   { "ok": true, "authEnforced": true, "dataDir": "/data", "version": "0.1.0" }
#   authEnforced:false means PSTACK_TOKEN is unset — every route is open. Fix that before anything else.

curl -s $API/api/deployments | jq                      # every submitted deployment
curl -s "$API/api/deployments/pr-123?PR=123" | jq      # meta + resolved spec summary

# submit or replace: the spec and compose file go in the body as strings
curl -s -X PUT -H "Authorization: Bearer $PSTACK_TOKEN" \
  -H 'content-type: application/json' \
  -d "$(jq -n --rawfile s preview.yml --rawfile c docker-compose.preview.yml \
          '{spec:$s, compose:$c, env:{PR:"123"}}')" \
  $API/api/deployments/pr-123

# mutating routes need the bearer token; they return 202 + a job id
JOB=$(curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
        "$API/api/deployments/pr-123/up?PR=123" | jq -r .job.id)
curl -s $API/api/jobs/$JOB | jq .state       # running | ok | failed | leaked
curl -N  $API/api/jobs/$JOB/stream           # SSE live log

curl -s -X POST "$API/api/deployments/pr-123/down?PR=123" \
  -H "Authorization: Bearer $PSTACK_TOKEN" \
  -H 'content-type: application/json' -d '{"verify":true}'
```

`:id` is a **registry id**, never a compose project name: the server owns the stored spec and
resolves `stack:` itself, so a client cannot ask it to act on an arbitrary compose project. Spec
variables ride on the query string and are **not stored** — pass the same `?PR=` to `down` as you
did to `up`, or teardown targets a different stack than deploy created. A second job for a stack
that already has one in flight returns **409** rather than queueing — concurrent `up`/`down` on one
stack would race over the same database branch.

### Both hostnames are on the public internet — and reads are unauthenticated

This is the changed default, so read it twice. `init` publishes `control.<domain>` and
`api.<domain>` through Traefik with **no middleware in front of them**. `PSTACK_TOKEN` gates
`POST`/`DELETE` only: every `GET` answers without it, including `GET /api/jobs/:id` (a whole job
transcript, hook step messages and the first line of a failed hook's stderr) and
`GET /api/deployments` (every deployment on the box). Writes are double-gated; **reads are gated by
nothing**.

Put a middleware in front of them — a `basicAuth` or an `ipAllowList` defined in
`traefik-dynamic/` — or accept that your stack inventory and hook logs are public. It is the first
item in [§9](#9-hardening-checklist) for a reason.

---

## 8. Adapting to other providers

The shape is identical; three things change — and on the default HTTP-01 path, the credential row
disappears entirely.

| | **Hetzner** | **AWS (EC2)** | **GCP (Compute Engine)** | **Bare metal** |
|---|---|---|---|---|
| Pass cloud-init | `hcloud server create --user-data-from-file cloud-init.yaml` | `aws ec2 run-instances --user-data file://cloud-init.yaml` | `gcloud compute instances create … --metadata-from-file user-data=cloud-init.yaml` | NoCloud seed ISO / PXE, or run the `runcmd` steps by hand |
| Firewall | Hetzner Cloud Firewall | security group | VPC firewall rules | `nftables` / `ufw` |
| Reach `:80` from the internet | required | required | required | required |
| DNS-01 provider code *(only if you opt in)* | `hetzner` | `route53` | `gcloud` | whichever hosts the zone (`cloudflare` is the common answer) |
| DNS-01 credential *(only if you opt in)* | `HETZNER_API_TOKEN` (or `…_FILE`) in `dns.env` | IAM — an **instance profile** satisfies the AWS credential chain, so no stored key | the VM's **attached service account** via Application Default Credentials — **no stored key** | a token, same as Hetzner |

Exact variable names for anything beyond the four verified in [§3](#switching-to-dns-01-opt-in-when-the-arithmetic-says-so):
[lego's DNS provider list](https://go-acme.github.io/lego/dns/). Every lego variable also accepts a
`_FILE` suffix.

**Where this used to be a design constraint, it now is not.** Under DNS-01, Hetzner has no way to
avoid a token on the box, while GCP (Cloud DNS + `gcloud`, service account granted `roles/dns.admin`
on the zone) and AWS (Route 53 + instance profile) hold **zero DNS secrets** — which used to be a
legitimate reason to move your DNS to match your host. HTTP-01 gives every provider that property by
default: no token in user-data, no token on disk, nothing to rotate. Consider that a reason to stay
on HTTP-01 as long as the rate-limit arithmetic in [§3](#http-01-the-default-and-the-whole-reason-this-box-needs-no-secrets) allows.

---

## 9. Hardening checklist

- [ ] **Put auth in front of the control plane.** `control.<domain>` and `api.<domain>` are public
      and every `GET` is unauthenticated — job transcripts (including hook stderr) and the full
      deployment list. Add a `basicAuth` or `ipAllowList` middleware in `traefik-dynamic/`, or an
      SSH-tunnel-only workflow with the hostnames firewalled off. And remember: the pstack container
      holds a read-write Docker socket, so anyone who can drive the *mutating* API can start a
      privileged container and own the box.
- [ ] **Firewall down to 80 / 443 / 22.**
      ```bash
      hcloud firewall create --name preview-host
      hcloud firewall add-rule preview-host --direction in --protocol tcp --port 80 \
        --source-ips 0.0.0.0/0 --source-ips ::/0
      hcloud firewall add-rule preview-host --direction in --protocol tcp --port 443 \
        --source-ips 0.0.0.0/0 --source-ips ::/0
      hcloud firewall add-rule preview-host --direction in --protocol tcp --port 22 \
        --source-ips <your.office.ip>/32
      hcloud firewall apply-to-resource preview-host --type server --server preview-host
      ```
      **Do not "tidy up" port 80.** It looks unused once everything redirects to HTTPS, but HTTP-01
      answers there — and the failure is delayed: existing certificates keep working, then
      **renewals** start failing ~60 days later, long after anyone remembers the firewall change.
      (Only a DNS-01 host may safely close it, and only if nothing needs the redirect.)
- [ ] **SSH: keys only.** `PasswordAuthentication no`, `PermitRootLogin no`.
- [ ] **Never publish the Docker socket.** No `-p 2375`, no socket-over-TCP, no socket-mounted
      container that accepts external input. Traefik's mount is `:ro`, which stops accidents, not an
      attacker — the read-only Docker API still enumerates every container, its env, and its labels.
      Consider a socket proxy that whitelists the container/network endpoints Traefik actually needs.
- [ ] **Protect `control/.env` (`0600`).** It holds `PSTACK_TOKEN`. If `init` generated it, the only
      other copy is in `/var/log/cloud-init-output.log` — read it once, then treat that log
      accordingly.
- [ ] **Rotate the DNS token — DNS-01 only.** Scope it to the one zone. Rotation is
      `PSTACK_DNS_TOKEN=<new> pstack init … --challenge dns01 --dns-provider <code>`, which rewrites
      `dns.env` and recreates Traefik. On HTTP-01 this whole line is moot: there is no token.
- [ ] **`mem_limit` on every service, in every stack.** Repeated because it is the difference
      between one PR breaking and the host breaking.
- [ ] **Back up the `letsencrypt` volume.** Losing `acme.json` means re-issuing — and under HTTP-01
      that is *one new certificate per hostname*, straight into the ~50/week limit. A restore is
      minutes; a rate-limited re-issue is hours with no TLS.
      ```bash
      docker run --rm -v pstack-control_letsencrypt:/le -v /root/backup:/out \
        alpine tar czf /out/acme-$(date +%F).tgz -C /le .
      ```
- [ ] **Unattended security upgrades on**, and reboot on kernel updates during a quiet window.
- [ ] **Run `pstack verify` on a schedule.** A nightly sweep is how leaks get found while they are
      still cheap. `verify` resolves one `${PR}` per invocation, so the sweep is a loop:
      ```bash
      cd /opt/preview/config
      for pr in $(gh pr list --state all --limit 50 --json number -q '.[].number'); do
        PR=$pr pstack verify -q || echo "PR $pr: exit $?"
      done
      ```
      Exit `2` means "torn down but something survived" — page a human differently from exit `1`,
      "teardown errored". Different owners.
- [ ] **The spec is trusted input.** Hooks are shell strings executed at CI trust level. Treat
      `preview.yml` with the same review discipline as a CI workflow; never point this host at a
      spec from an untrusted fork. Accepting a spec — over HTTP or otherwise — is accepting
      arbitrary compose **and** arbitrary shell: that is remote code execution by design, not by
      bug, so the gate is *who may submit*, never what the spec contains
      ([control-plane.md §5](control-plane.md#5-trust-boundary)).
- [ ] **Mark host singletons `kind: shared`.** A shared database or queue declared as the default
      `isolated` gets no guard on `down`, and `compose down -v` then destroys the state every
      preview depends on. With `kind: shared` the teardown is refused unless someone types
      `--force` — and the API never passes it, so a shared deployment cannot be destroyed over
      HTTP at all.
- [ ] **Never point `pstack` at the control stack.** No spec should name `pstack-control`. Upgrade it
      from the host ([§7](#upgrading-and-rotating-re-run-init)); a `down` there takes out
      `acme.json` — every certificate, re-issued under a per-week rate limit.

---

## 10. Troubleshooting

### HTTP-01: certificate never issued

The default mode has three failure modes, and only one of them is a bug.

| Symptom | Cause | Fix |
|---|---|---|
| ACME log says the challenge was not reachable / timed out fetching `/.well-known/acme-challenge/…` | **Port 80 is not reachable from the internet** — the one hard requirement | Open `:80` in the cloud firewall *and* the host firewall. Prove it from **off-box**: `curl -sv http://backend-pr-123.preview.example.com/.well-known/acme-challenge/probe` should reach Traefik (a 404 from Traefik is fine — a timeout is not) |
| The hostname does not resolve, or resolves elsewhere | wildcard/apex A record missing or stale | `dig +short backend-pr-123.preview.example.com` must return the server IP (§3) |
| `TRAEFIK DEFAULT CERT` on a hostname that was **never deployed** | **not a bug.** HTTP-01 cannot certify a host with no container answering on it | Deploy the stack. If you need URLs valid before deploy, that is the DNS-01 switch (§3) |
| `too many certificates already issued` / rate limit | you crossed **~50 new certs per registered domain per week** (~16 new PRs at 3 surfaces) | Short term: stop creating new hostnames and wait out the window. Real fix: `--challenge dns01` — one wildcard, no per-PR issuance. While iterating, use the **staging** CA (§3) |
| A per-PR host gets the default cert while `control.<domain>` is fine | the per-PR router has `tls=true` but **no** `tls.certresolver=le` — required under HTTP-01 | Add it (§5). This is the label that must be **absent** under DNS-01 — check which mode you are in before "fixing" it |
| No ACME line in the logs at all | no router asked for a certificate | `docker compose -p pstack-control logs traefik \| grep -iE 'acme\|certificate'`, then check the container's labels (below) |

There is **no Traefik dashboard** on this host (§5), so "is the router there?" is answered with
labels and logs:

```bash
# Traefik reads its routing from container LABELS, so inspect the container, not the compose file:
docker inspect -f '{{json .Config.Labels}}' "$(docker compose -p pr-7 ps -q backend)" | jq
docker compose -p pstack-control logs traefik | grep -iE 'router|provider|error'
```

A parse error mentioning the file provider usually means something non-config landed in
`control/traefik-dynamic/` — the htpasswd trap (§5).

### DNS-01: propagation timeouts

Symptom: `time limit exceeded: last error: NS … did not return the expected TXT record`.

| Cause | Fix |
|---|---|
| Traefik checks before the zone has converged | raise `--certificatesresolvers.le.acme.dnschallenge.propagation.delaybeforechecks` (`init` renders `30s`; try `120s`) — a hand edit to `control/docker-compose.yml`, which `init` rewrites on its next run (§5) |
| Provider API is slow to publish | raise `HETZNER_PROPAGATION_TIMEOUT` (seconds) in `control/dns.env` |
| Wrong credential variable name | this is the classic: a wrong name looks exactly like a propagation failure. Confirm the token is visible under the name lego expects: `docker compose -p pstack-control exec traefik printenv HETZNER_API_TOKEN` (it is `HETZNER_API_TOKEN`, never `HETZNER_API_KEY`). An unknown provider gets a literal `CHANGEME_VARIABLE_NAME` line in `dns.env` |
| Token lacks write scope on the zone | confirm by hand during a challenge: `dig +short TXT _acme-challenge.preview.example.com @1.1.1.1` — nothing appearing means the write never happened |
| Precheck resolver serving stale/split-horizon answers | `init` already renders `dnschallenge.resolvers=1.1.1.1:53`; if your zone is not public from there, change it (hand edit, same caveat as above) |

A `CNAME` on `_acme-challenge` pointing somewhere the token cannot write is a classic silent
failure. And a router whose `Host()` rule is **not** covered by the wildcard (two labels deep — §3)
gets Traefik's self-signed default: the log is quiet, only the browser complains.

### `init` refuses to run

`init` checks its preconditions before creating anything, and each failure names itself.

| Message | Cause |
|---|---|
| `no Docker socket at /var/run/docker.sock` | Docker Engine is not installed or not started — cloud-init step 1 (§4) |
| `the Compose v2 plugin is missing` | `docker-compose-plugin` not installed; `docker compose version` is the check |
| `image pstack:local not found` | **the control-image gap** (§4): `docker image inspect` never pulls. Either `docker pull` your image and pass `PSTACK_IMAGE=…`, or build `pstack:local` on the host. Also the symptom of *re-running* `init` without re-passing `PSTACK_IMAGE` |
| `init needs --domain` / `--acme-email` | pass the flags, or `PSTACK_DOMAIN` / `PSTACK_ACME_EMAIL` |
| `--challenge dns01 needs --dns-provider` | the provider is required **only** for dns01 |
| `control stack started but the API never became healthy` | containers were created but the healthcheck never passed: `docker compose -p pstack-control logs pstack`. A crash-looping container still gives `up -d` exit 0, which is exactly why `init` waits |

### cloud-init failures

```bash
cloud-init status --long                       # which module failed
sudo tail -n 300 /var/log/cloud-init-output.log  # the real command output
sudo cloud-init schema --system --annotate     # YAML that cloud-init rejected
```

| Symptom | Cause |
|---|---|
| `usermod: group 'docker' does not exist` | you dropped the top-level `groups:` block — the `users` stage runs before `runcmd` installs Docker |
| `bun: command not found` | Bun installed to `/root/.bun`; use `BUN_INSTALL=/usr/local` and absolute paths |
| `pstack: command not found`, or it works for root and not for `preview` | `bun add -g` linked the shim into `$BUN_INSTALL/bin` of whatever `BUN_INSTALL`/`HOME` was in effect. Check `bun pm bin -g`; the cloud-init sets `BUN_INSTALL=/usr/local` on the `add -g` line for exactly this reason |
| Steps after a failing line never ran | a non-zero `runcmd` step aborts the rest of cloud-init. That is what `\|\| true` is for on idempotent steps |
| Whole file ignored | missing `#cloud-config` first line, or a tab character anywhere in the YAML |

Re-running is cheap and usually clearer than debugging: `hcloud server delete` and recreate. That is
the point of putting the whole build in one file.

### Compose starts nothing

Expected, if you ran plain `docker compose up`: **every service is behind a `profiles:` entry** so a
bare `up` is a no-op, and the enabled profiles come from your spec.

```bash
# what compose actually resolves for this stack
cd /opt/preview/config
STACK=pr-123 docker compose -p pr-123 -f docker-compose.preview.yml \
  --profile backend --profile frontend config --services
```

Empty output ⇒ profile names in `preview.yml` do not match the `profiles:` in the compose file. And
the asymmetry to remember: `pstack up` enables only the profiles you selected; `pstack down` enables
**every** profile in the spec, because Compose treats a non-enabled profile's services as absent and
would otherwise leave that profile's `<stack>_default` network behind — one dead network per PR,
forever.

### Traefik 404s a per-PR hostname

Walk it from the outside in:

| Check | Fix |
|---|---|
| Does the router exist? | `docker inspect -f '{{json .Config.Labels}}' <ctr> \| jq` — no `traefik.enable=true`, no route (remember `exposedbydefault=false`) |
| Is the container on `preview-ingress`? | `docker inspect -f '{{json .NetworkSettings.Networks}}' <ctr> \| jq keys` |
| Does the network match on both sides? | `preview-ingress` must be `external: true` in **both** the control stack and the per-PR file. Declare it non-external in the per-PR file and Compose creates a *different* network named `pr-123_preview-ingress` — the container is up, healthy, and unreachable. **Same root cause as a missing `traefik.docker.network` label.** |
| Multi-network container? | add `traefik.docker.network=preview-ingress`, else Traefik may dial the project `default` network it isn't on |
| Router name collision? | router names are global — namespace with `${STACK}` or one PR silently wins |
| `502` instead of `404`? | route found, backend refused: check `loadbalancer.server.port` against what the app actually listens on, and that it binds `0.0.0.0` rather than `127.0.0.1` |

`404` = Traefik has no route. `502` = Traefik has a route and cannot reach it. Do not debug the
first as if it were the second. A TLS error before either is a certificate problem — start at the
top of this section, in the mode you are actually running.
