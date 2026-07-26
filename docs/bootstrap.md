# Bootstrapping a preview host

How to go from an empty cloud account to a host that runs `pstack`. The worked example is **Hetzner
Cloud + cloud-init**; every step names what it does so the AWS / GCP / bare-metal equivalents are
mechanical (see [Adapting to other providers](#8-adapting-to-other-providers)).

Read [`../README.md`](../README.md) first — this document assumes you know what an axis is and why
`down` is best-effort while `verify` is strict — then
[`control-plane.md`](control-plane.md), which explains why the **control stack is brought up from
the host and never by the API**. That rule is what shapes this bootstrap: cloud-init's job is to
install Docker, Bun and `pstack`, lay down the host inputs, and hand over to `pstack init`.

---

## 1. What you are building

One host, three layers.

**Control** — Traefik owns 80/443 and terminates the wildcard cert; `pstack` serves the API. This is
the layer `pstack init` creates, from the host, and the only layer the API may not manage.
**Shared** — the always-on singletons previews borrow (a database, a queue cluster, a registry
mirror), each a `kind: shared` deployment. **Isolated** — one Compose project per PR
(`docker compose -p pr-123`), attached to two **external** Docker networks.

```
                        DNS:  preview.example.com      A → 203.0.113.10
                              *.preview.example.com    A → 203.0.113.10
                                        │
                                    :80 │ :443
┌───────────────────────────────────────▼──────────────────────────────────────────┐
│  preview host (one VM)                                                           │
│                                                                                  │
│  ┌─ CONTROL stack — `pstack init`, from the host, never from the API ───────┐    │
│  │  traefik  ── docker provider (labels) + file provider (/etc/traefik/…)   │    │
│  │             ── ONE router requests *.preview.example.com via DNS-01      │    │
│  │  pstack   ── the API (systemd today; a service here once `init` ships)   │    │
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
│  pstack serve  →  127.0.0.1:7878   (systemd; talks to the Docker socket)         │
└──────────────────────────────────────────────────────────────────────────────────┘

per-PR resources Compose does NOT own — these are pstack axes:
   database branch · queue/workflow namespace · per-PR images · ingress assertion
```

Two networks, not one, because they answer different questions: `preview-ingress` is
"who may Traefik route to", `preview-shared` is "who may reach shared infrastructure". A per-PR
service that needs the database but must not be publicly routable joins only the second.

Both are **external** — created once by this bootstrap, referenced (never created) by the shared
stack *and* by every per-PR compose file. If one side declares them and the other doesn't, you get
the 404 in [§10](#10-troubleshooting).

---

## 2. Prerequisites

| Need | Why |
|---|---|
| A Hetzner Cloud project + API token | `hcloud` creates the server |
| `hcloud` CLI, authenticated (`hcloud context create preview`) | server + firewall management |
| A domain you control the DNS for | the wildcard record and the DNS-01 challenge |
| A **DNS provider API token** scoped to that zone | Traefik answers DNS-01 by writing TXT records |
| An SSH public key uploaded to the project (`hcloud ssh-key create`) | you will not be using a password |
| A git remote holding your `preview.yml` + `hooks/` | pstack reads them from disk on the host |

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
             + shared stack (Traefik ~128 MB, plus any shared DB/queue)
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

## 3. DNS: one wildcard record, one wildcard certificate

Create exactly two records, both pointing at the server's public IP:

| Record | Type | Value |
|---|---|---|
| `*.preview.example.com` | A | `203.0.113.10` |
| `preview.example.com` | A | `203.0.113.10` |

Add `AAAA` records too if you enable IPv6 on the server.

**Why a wildcard makes per-PR hostnames free.** Without it, every deploy has a DNS step and a
certificate step: create `backend-pr-123.preview.example.com`, wait for propagation, issue a cert,
and remember to delete both on teardown (two more axes, two more things to leak). With a wildcard
record plus a wildcard cert, a new hostname needs **no** provisioning at all — DNS already resolves
it and TLS already covers it. Routing becomes a Compose label, which is the one thing Compose is
genuinely good at. That is why `ingress` in the example spec has no `up` hook: there is nothing to
create, only something to assert is gone afterwards.

**Why DNS-01 and not HTTP-01.** HTTP-01 proves control by serving a token at
`http://<host>/.well-known/acme-challenge/…` for the *specific* hostname being certified. That fails
here twice over:

1. **A wildcard cert can only be issued via DNS-01.** Let's Encrypt does not offer HTTP-01 for
   `*.example.com` — there is no single host to answer for.
2. **Chicken-and-egg per PR.** If you fell back to per-hostname HTTP-01, the certificate for
   `backend-pr-123…` could only be issued *after* a container answering that hostname exists. A PR
   that has not deployed yet, or whose deploy is mid-build, would serve a TLS error instead of a
   page — and the failure looks like a certificate problem when it is a sequencing problem.

DNS-01 needs nothing running. Traefik writes a TXT record, Let's Encrypt reads it, the cert exists
before the first container does.

### The hostname scheme is constrained, not stylistic

`*.preview.example.com` matches **exactly one** DNS label:

| Hostname | Covered by `*.preview.example.com`? |
|---|---|
| `backend-pr-123.preview.example.com` | ✅ yes |
| `backend.pr-123.preview.example.com` | ❌ **no** — two labels deep |

So flatten with dashes. [`../examples/preview.yml`](../examples/preview.yml) already does
(`backend-${STACK}.${PREVIEW_DOMAIN}` → `backend-pr-123.preview.example.com`). Invent a dotted
scheme and every PR misses the cert, Traefik falls back to its self-signed default, and browsers
throw `ERR_CERT_AUTHORITY_INVALID` on a setup that otherwise looks correct.

---

## 4. The cloud-init file

Save as `cloud-init.yaml`. Replace every `example.com`, `<your-…>` and `CHANGEME` placeholder.

The file does four things, in this order: install **Docker**, install **Bun + pstack**, write the
**host inputs**, then **bring the control stack up**. The split matters — the host inputs are
mounted by the control stack, never baked into it, so re-rendering the stack never touches your
secrets, and rotating a secret never means re-rendering the stack.

> **`pstack init` is implemented (`src/init.ts` + `templates/control/`), but `src/cli.ts` does not
> dispatch an `init` command yet.** What it does and why it is host-side:
> [control-plane.md §7](control-plane.md#7-upgrade-path). Until the dispatch lands, cloud-init
> writes the control stack's compose file itself — the block marked **`init` will own this** below —
> and brings it up with `docker compose up -d`. When it lands, delete that one block and swap the
> final command; everything else in this file (the DNS token, the Traefik dynamic config,
> `/etc/pstack.env`, the two external networks) is a **host input** `init` mounts rather than owns,
> and stays exactly as it is. Note `init` uses its own project name, `pstack-control`, and creates
> the same two external networks — so on a host it has run, `docker network create … || true` below
> is the no-op it is written to be.

> **Secret handling.** The DNS token and `PSTACK_TOKEN` below are written into the instance's
> user-data, which the provider stores and which is readable from the instance's metadata service —
> any process on the box can recover it. That is acceptable for a **scoped, rotatable** token that
> can only edit one DNS zone. If your threat model says otherwise, leave the two token files empty
> here and `scp` them in after boot (`chmod 0600`), then
> `systemctl restart pstack` + `docker compose … up -d traefik`.

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
  - openssl

write_files:
  # ── DNS-01 credential, mounted into Traefik as a file ──────────────────────────────────
  # lego (which Traefik embeds) accepts a `_FILE` suffix on every provider variable, so the
  # token never has to appear in an environment listing or in `docker inspect`.
  - path: /etc/traefik/secrets/hetzner_dns_token
    permissions: '0600'
    owner: root:root
    content: |
      CHANGEME_HETZNER_DNS_API_TOKEN

  # ── Traefik dynamic configuration (file provider) ──────────────────────────────────────
  # This directory is WATCHED. Only real dynamic config may live here — see §5 for the
  # htpasswd trap.
  - path: /etc/traefik/dynamic/dashboard.yml
    permissions: '0644'
    owner: root:root
    content: |
      # The ONE router that requests the wildcard certificate.
      #
      # Specifying `certResolver` inside an individual router's TLS block makes Traefik request a
      # certificate for THAT router's domain. If every per-PR router did this, each PR would
      # trigger its own issuance and you would hit Let's Encrypt rate limits. Instead: exactly
      # one always-on router asks for the wildcard (below), and every other router sets only
      # `tls: true` and inherits it by SNI.
      #
      # This router doubles as the dashboard because it is always on — one router, one cert
      # request, nothing extra to keep alive.
      http:
        routers:
          dashboard:
            rule: "Host(`traefik.preview.example.com`)"
            entryPoints: [websecure]
            service: api@internal
            middlewares: [dashboard-auth]
            tls:
              certResolver: letsencrypt
              domains:
                - main: "preview.example.com"
                  sans:
                    - "*.preview.example.com"
        middlewares:
          dashboard-auth:
            basicAuth:
              # NOTE the path: /etc/traefik/auth, *not* /etc/traefik/dynamic.
              usersFile: /etc/traefik/auth/dashboard.htpasswd

  # ── The control stack ──  ⚠ `init` WILL OWN THIS BLOCK ─────────────────────────────────
  # This is the one hand-written thing `pstack init` replaces: it renders exactly this and
  # `compose up -d`s it. Until then, cloud-init writes it. Nothing else in write_files is
  # affected — the token, the dynamic config and /etc/pstack.env are host inputs it mounts.
  - path: /opt/preview/shared/docker-compose.yml
    permissions: '0644'
    owner: root:root
    content: |
      # `name:` pins the compose project name so volume names are deterministic
      # (`shared_letsencrypt`) instead of being derived from the directory. Keep it stable —
      # per-PR hooks reference container names derived from it (`shared-temporal-1`), and
      # renaming it silently breaks every preview. (The project is still called `shared` for
      # that reason, even though in control-plane terms this is the CONTROL stack.)
      name: shared

      services:
        traefik:
          # Pin ≥ v3.6.1: earlier 3.x releases fail to talk to Docker Engine 29's API and the
          # docker provider comes up empty (every route 404s while Traefik itself looks healthy).
          image: traefik:v3.6.1
          restart: unless-stopped
          mem_limit: 256m
          ports:
            - "80:80"
            - "443:443"
          networks: [preview-ingress]
          environment:
            HETZNER_API_TOKEN_FILE: /run/secrets/hetzner_dns_token
            # Generous: a zone with slow propagation will otherwise fail the challenge.
            HETZNER_PROPAGATION_TIMEOUT: "600"
          volumes:
            # Read-only, but still a privilege — see §9.
            - /var/run/docker.sock:/var/run/docker.sock:ro
            - letsencrypt:/letsencrypt
            - /etc/traefik/dynamic:/etc/traefik/dynamic:ro
            - /etc/traefik/auth:/etc/traefik/auth:ro
            - /etc/traefik/secrets/hetzner_dns_token:/run/secrets/hetzner_dns_token:ro
          command:
            # providers
            - --providers.docker=true
            - --providers.docker.exposedbydefault=false
            - --providers.docker.network=preview-ingress
            - --providers.file.directory=/etc/traefik/dynamic
            - --providers.file.watch=true
            # entrypoints (all plaintext redirected to TLS)
            - --entrypoints.web.address=:80
            - --entrypoints.web.http.redirections.entrypoint.to=websecure
            - --entrypoints.web.http.redirections.entrypoint.scheme=https
            - --entrypoints.websecure.address=:443
            # dashboard (exposed only through the basic-auth router above)
            - --api.dashboard=true
            # ACME over DNS-01
            - --certificatesresolvers.letsencrypt.acme.email=you@example.com
            - --certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json
            - --certificatesresolvers.letsencrypt.acme.dnschallenge=true
            - --certificatesresolvers.letsencrypt.acme.dnschallenge.provider=hetzner
            - --certificatesresolvers.letsencrypt.acme.dnschallenge.resolvers=1.1.1.1:53
            - --certificatesresolvers.letsencrypt.acme.dnschallenge.propagation.delaybeforechecks=30s
            - --log.level=INFO
            - --accesslog=true

      volumes:
        letsencrypt:

      networks:
        # Declared external here AND in every per-PR compose file. Created in runcmd.
        preview-ingress:
          external: true

  # ── pstack environment ─────────────────────────────────────────────────────────────────
  # Read by the systemd unit as EnvironmentFile. This is also how credentials reach axis
  # hooks: pstack hands its own process environment to every `bash -c` it runs, so the DB /
  # registry tokens your hooks shell out with belong in this same file.
  - path: /etc/pstack.env
    permissions: '0600'
    owner: root:root
    content: |
      # --- pstack serve ---
      PSTACK_TOKEN=CHANGEME_LONG_RANDOM_STRING
      PSTACK_PORT=7878
      PSTACK_HOST=127.0.0.1
      PSTACK_VAR=PR

      # --- consumed by the spec's env: block and by axis hooks ---
      PREVIEW_DOMAIN=preview.example.com
      # DB_API_TOKEN=…
      # REGISTRY_TOKEN=…

  # ── systemd unit for the API ───────────────────────────────────────────────────────────
  - path: /etc/systemd/system/pstack.service
    permissions: '0644'
    owner: root:root
    content: |
      [Unit]
      Description=pstack preview-stack API
      Requires=docker.service
      After=docker.service network-online.target

      [Service]
      Type=simple
      User=preview
      Group=docker
      # REQUIRED, not cosmetic. pstack resolves `-f preview.yml` and every hook path
      # (`./hooks/db-branch.sh`) relative to the process working directory — it never chdirs.
      # This must be the checkout that holds preview.yml and hooks/.
      WorkingDirectory=/opt/preview/config
      EnvironmentFile=/etc/pstack.env
      # Absolute path to bun: systemd's PATH does not include ~/.bun/bin.
      ExecStart=/usr/local/bin/bun /opt/preview/pstack/src/cli.ts serve -f preview.yml
      Restart=on-failure
      RestartSec=5
      StandardOutput=journal
      StandardError=journal

      [Install]
      WantedBy=multi-user.target

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

  # 3. The two external networks. `network create` errors if the network exists, and a failing
  #    runcmd step aborts the rest of cloud-init — hence `|| true` (this is the idempotent path
  #    on a re-run).
  - docker network create preview-ingress || true
  - docker network create preview-shared || true

  # 4. Dashboard credentials. Kept OUT of /etc/traefik/dynamic (see §5).
  - mkdir -p /etc/traefik/auth
  - bash -c 'echo "admin:$(openssl passwd -apr1 CHANGEME_DASHBOARD_PASSWORD)" > /etc/traefik/auth/dashboard.htpasswd'
  - chmod 0640 /etc/traefik/auth/dashboard.htpasswd

  # 5. Check out pstack and your preview config.
  #    pstack has NO runtime dependencies (its package.json lists only devDependencies), so no
  #    `bun install` is needed to run `serve`. Keep ui/ as a sibling of src/ — the server resolves
  #    its static directory as `../ui` relative to the source file.
  - git clone --depth 1 https://github.com/<you>/preview-stacks.git /opt/preview/pstack
  - git clone --depth 1 https://github.com/<you>/<your-project>.git /opt/preview/config
  - chown -R preview:preview /opt/preview

  # 6. Bring the control stack up FROM THE HOST. This is the step `pstack init` becomes:
  #
  #        cd /opt/preview/config && /usr/local/bin/bun /opt/preview/pstack/src/cli.ts init
  #
  #    …and it is deliberately the host's job, not the API's: the API cannot recreate the stack
  #    that contains it without killing the process doing the work (control-plane.md §2).
  #    `init` is implemented but not yet dispatched from the CLI, so today it is this line plus
  #    the systemd unit below.
  - docker compose -f /opt/preview/shared/docker-compose.yml up -d
  - systemctl daemon-reload
  - systemctl enable --now pstack
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

`cloud-init-output.log` is the only file that shows you the apt/curl/docker output. Read it before
theorising.

---

## 5. The control stack, explained

The compose file above is complete; these are the four decisions inside it. They are also the
inputs `pstack init` will need, so they do not become obsolete when it ships — they move from a
file you maintain to a file it renders.

| Decision | Why |
|---|---|
| **Both providers enabled** (`docker` + `file`) | The docker provider turns per-PR container labels into routes automatically. The file provider holds the things no container owns: the wildcard-issuing router, middlewares, the dashboard. |
| **`exposedbydefault=false`** | Opt-in routing. A container is only routed if it carries `traefik.enable=true`. Without this, every service in every PR — including internal ones — gets a public route derived from its name. |
| **`providers.docker.network=preview-ingress`** | Tells Traefik which of a container's networks to dial. A per-PR service sits on `preview-ingress`, `preview-shared`, *and* its project's `default`; without this Traefik may pick one it cannot reach. Per-service overrides use the `traefik.docker.network` label. |
| **One router owns the wildcard** | Restated because it is the mistake that hurts: `certResolver` in a router's TLS block requests a cert for *that* router's domain. One always-on router asks for `main: preview.example.com` + `sans: ["*.preview.example.com"]`; everything else sets `tls: true` and inherits by SNI. N per-PR routers each requesting their own certificate is how you meet Let's Encrypt's rate limits. |

### The htpasswd trap

`--providers.file.directory=/etc/traefik/dynamic` **watches that directory and parses every file in
it as dynamic configuration.** Put `dashboard.htpasswd` there and Traefik tries to read
`admin:$apr1$…` as YAML/TOML, logs a parse error, and — depending on version and timing — discards
the rest of the directory's configuration along with it. The dashboard router disappears, the
wildcard cert is never requested, and the visible symptom is "no certificate", which points you at
ACME instead of at a misplaced file.

Keep credentials in a **sibling** directory (`/etc/traefik/auth/`), mounted separately. The rule
generalises: nothing but dynamic config in the watched directory.

### Shared deployments live *beside* the control stack, not inside it

A shared Postgres, a queue cluster, a registry mirror — anything every preview borrows — is its own
`kind: shared` deployment, not another service in the file above. Two reasons:

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
      # tls=true and NOTHING else: inherit the wildcard, do not request a certificate.
      - traefik.http.routers.${STACK}-backend.tls=true
      - traefik.http.services.${STACK}-backend.loadbalancer.server.port=3000

networks:
  preview-ingress:
    external: true             # must match the shared stack's declaration
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

# ── both external networks exist ──────────────────────────────────────────────────────
docker network ls --filter name=preview-
#   NETWORK ID   NAME              DRIVER   SCOPE
#   ...          preview-ingress   bridge   local
#   ...          preview-shared    bridge   local

# ── the shared stack is up ────────────────────────────────────────────────────────────
docker compose ls --all
#   NAME     STATUS       CONFIG FILES
#   shared   running(1)   /opt/preview/shared/docker-compose.yml

# ── the certificate was issued: read the ACME conversation ────────────────────────────
docker compose -f /opt/preview/shared/docker-compose.yml logs traefik | grep -iE 'acme|certificate|error'
#   want: "Trying to challenge using DNS-01" → "Served certificate"
#   a "propagation" or "timeout" line here is §10

# ── the certificate is actually stored (non-trivial acme.json) ─────────────────────────
docker run --rm -v shared_letsencrypt:/le alpine \
  sh -c 'ls -l /le/acme.json && wc -c < /le/acme.json'
#   a few hundred bytes = empty skeleton; several KB = a real cert

# ── TLS answers for a hostname that has NO container — the whole point of the wildcard ─
openssl s_client -connect 203.0.113.10:443 \
  -servername nothing-is-deployed-here.preview.example.com </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject -ext subjectAltName
#   want: issuer = Let's Encrypt, SAN includes DNS:*.preview.example.com
#   "self-signed"/"TRAEFIK DEFAULT CERT" = the wildcard was never issued or never matched

# ── the dashboard is protected, not open ──────────────────────────────────────────────
curl -sfI https://traefik.preview.example.com/dashboard/ ; echo "exit=$?"   # expect 401
curl -sfI -u admin:… https://traefik.preview.example.com/dashboard/         # expect 200
```

---

## 7. Install and run pstack

### Layout

Two checkouts, because they answer to two different constraints:

| Path | Holds | Constraint |
|---|---|---|
| `/opt/preview/pstack` | this repo: `src/`, `ui/` | `ui/` must stay a **sibling of `src/`** — `serve` resolves its static directory as `../ui` relative to the source file |
| `/opt/preview/config` | your `preview.yml`, `hooks/`, per-PR compose file | must be the process **working directory** — `-f preview.yml` and hook paths like `./hooks/db-branch.sh` are cwd-relative and pstack never chdirs |

That is why the unit sets **both** `WorkingDirectory=/opt/preview/config` and an absolute
`ExecStart=/usr/local/bin/bun /opt/preview/pstack/src/cli.ts …`. Omit `WorkingDirectory` and you
get `spec not found: preview.yml`, or — worse — a spec that loads while every hook fails with
`./hooks/db-branch.sh: No such file or directory`.

**Both constraints belong to the systemd path specifically.** Once `pstack init` runs the API as a
service inside the control stack, they become container concerns rather than unit concerns: the
config checkout is a bind mount and the process cwd is set in the image, and `ui/` ships beside
`src/` in the image rather than on the host. Nothing about them disappears — they move. Until then,
these two lines are the ones that break the service if you edit the unit.

Sanity-check the spec by hand before trusting the service:

```bash
cd /opt/preview/config
PR=123 /usr/local/bin/bun /opt/preview/pstack/src/cli.ts validate
PR=123 /usr/local/bin/bun /opt/preview/pstack/src/cli.ts up --dry-run   # prints every command, runs none
```

`--dry-run` is the fastest check of axis ordering, and it is free.

For interactive use, `bun link` inside `/opt/preview/pstack` puts `pstack` on `PATH`. The systemd
unit deliberately does not depend on that.

### Environment

`pstack serve` reads four variables — all defaults come from `src/cli.ts`:

| Variable | Default | Meaning |
|---|---|---|
| `PSTACK_TOKEN` | *unset* | Bearer token. **Required to bind off-loopback.** |
| `PSTACK_PORT` | `7878` | listen port |
| `PSTACK_HOST` | `127.0.0.1` | listen address |
| `PSTACK_VAR` | `PR` | the spec variable bound to `:id` in the API path |

The safety interlock is enforced, not advisory: with no `PSTACK_TOKEN` and `PSTACK_HOST` set to
anything but `127.0.0.1`, pstack **refuses to start** and exits `3`. An API that can delete
production-adjacent databases does not get to be accidentally public.

`/etc/pstack.env` does double duty. pstack passes its own process environment down to every hook
(`bash -c`, with the spec's `env:` layered on top), so the DB / registry / cloud credentials your
axes shell out with belong in the same `0600` root-owned file as `PSTACK_TOKEN`. One file to
protect, one file to rotate.

### Drive it

```bash
systemctl status pstack
journalctl -u pstack -f

# on the host (or through a tunnel), against ?<var>= / :id — the *variable value*, not `pr-123`
curl -s localhost:7878/api/health | jq
#   { "ok": true, "authEnforced": true, "spec": "preview.yml", "varName": "PR" }

curl -s localhost:7878/api/deployments | jq            # every submitted deployment
curl -s 'localhost:7878/api/deployments/pr-123?PR=123' | jq   # meta + resolved spec summary

# submit or replace: the spec and compose file go in the body as strings
curl -s -X PUT -H "Authorization: Bearer $PSTACK_TOKEN" \
  -H 'content-type: application/json' \
  -d "$(jq -n --rawfile s preview.yml --rawfile c docker-compose.preview.yml \
          '{spec:$s, compose:$c, env:{PR:"123"}}')" \
  localhost:7878/api/deployments/pr-123

# mutating routes need the bearer token; they return 202 + a job id
JOB=$(curl -s -X POST -H "Authorization: Bearer $PSTACK_TOKEN" \
        'localhost:7878/api/deployments/pr-123/up?PR=123' | jq -r .job.id)
curl -s localhost:7878/api/jobs/$JOB | jq .state       # running | ok | failed | leaked
curl -N  localhost:7878/api/jobs/$JOB/stream           # SSE live log

curl -s -X POST localhost:7878/api/deployments/pr-123/down?PR=123 \
  -H "Authorization: Bearer $PSTACK_TOKEN" \
  -H 'content-type: application/json' -d '{"verify":true}'
```

`:id` is a **registry id**, never a compose project name: the server owns the stored spec and
resolves `stack:` itself, so a client cannot ask it to act on an arbitrary compose project. Spec
variables ride on the query string and are **not stored** — pass the same `?PR=` to `down` as you
did to `up`, or teardown targets a different stack than deploy created. A second job for a stack
that already has one in flight returns **409** rather than queueing — concurrent `up`/`down` on one
stack would race over the same database branch.

### Upgrading it

From the host, never over HTTP — the API cannot recreate the stack containing itself
([control-plane.md §2](control-plane.md#2-the-invariant-the-api-never-manages-its-own-stack)).

```bash
cd /opt/preview/pstack && git pull        # new pstack
curl -s localhost:7878/api/jobs | jq '[.jobs[] | select(.state == "running")]'   # drain first
sudo systemctl restart pstack             # → `pstack init` once it ships
curl -s localhost:7878/api/health         # confirm it came back
```

Drain before restarting: jobs are in-memory, so an in-flight `up` is truncated and must be re-run.
There is no queue to preserve — adding one would be a state store.

The shared and isolated deployments keep running throughout. They are separate compose projects and
nothing about them depends on the API process being alive, so a broken control plane costs you the
ability to *change* things, not the previews themselves.

### Exposing it: prefer the SSH tunnel

**The service user is in the `docker` group, which is root-equivalent on this host.** pstack works
by shelling out to `docker compose`; anyone who can drive the API can start a privileged container
and own the box. So the default should be *not reachable from the internet*:

```bash
# from your laptop / CI runner — nothing listens publicly
ssh -N -L 7878:127.0.0.1:7878 preview@203.0.113.10
curl -s localhost:7878/api/health
```

Keep `PSTACK_HOST=127.0.0.1`, and still set `PSTACK_TOKEN` — it protects against anything else
already on the host.

If you must publish it, put it behind Traefik with auth *and* keep the bearer token:

```yaml
# /etc/traefik/dynamic/pstack.yml
http:
  routers:
    pstack:
      rule: "Host(`pstack.preview.example.com`)"
      entryPoints: [websecure]
      service: pstack
      middlewares: [dashboard-auth]      # reuse the basic-auth middleware
      tls: true                          # inherit the wildcard — no certResolver here
  services:
    pstack:
      loadBalancer:
        servers:
          # pstack runs under systemd, not compose, so Traefik reaches it via the network's
          # gateway address — which is the host. Find it, don't guess it:
          #   docker network inspect preview-ingress -f '{{(index .IPAM.Config 0).Gateway}}'
          - url: "http://172.18.0.1:7878"
```

Then set `PSTACK_HOST` to **that same gateway address** rather than `0.0.0.0`: the API becomes
reachable from containers on `preview-ingress` (i.e. from Traefik) and from nothing else, so the
basic-auth middleware is genuinely in front of it. Two caveats before you do this:

- **The bearer token gates mutations only.** `pstack` requires it for `POST`/`DELETE`; every `GET`
  is unauthenticated even with `PSTACK_TOKEN` set. That includes `GET /api/jobs/:id`, which returns
  the whole job transcript — hook step messages and the first line of a failed hook's stderr — and
  `GET /api/deployments`, which enumerates every deployment on the box. So the ingress middleware is
  the **only** thing protecting your logs and your stack list. Writes are double-gated; reads are
  not.
- **Binding to a bridge gateway ties the unit to that network's lifetime.** `172.18.x.x` is whatever
  subnet Docker handed out; delete and recreate `preview-ingress` and it may change, at which point
  `pstack` fails to bind (`EADDRNOTAVAIL`) and `Restart=on-failure` loops. The `localhost:7878`
  examples above also stop working once `PSTACK_HOST` leaves loopback.

Both are reasons the SSH tunnel is the better default, not just the more paranoid one.

---

## 8. Adapting to other providers

The shape is identical; four things change.

| | **Hetzner** | **AWS (EC2)** | **GCP (Compute Engine)** | **Bare metal** |
|---|---|---|---|---|
| Pass cloud-init | `hcloud server create --user-data-from-file cloud-init.yaml` | `aws ec2 run-instances --user-data file://cloud-init.yaml` | `gcloud compute instances create … --metadata-from-file user-data=cloud-init.yaml` | NoCloud seed ISO / PXE, or run the `runcmd` steps by hand |
| DNS-01 provider code | `hetzner` | `route53` | `gcloud` | whichever hosts the zone (`cloudflare` is the common answer) |
| Credential | `HETZNER_API_TOKEN` (or `…_FILE`) on disk | IAM — an **instance profile** satisfies the AWS credential chain, so no stored key | the VM's **attached service account** via Application Default Credentials — **no stored key** | a token file, same as Hetzner |
| Firewall | Hetzner Cloud Firewall | security group | VPC firewall rules | `nftables` / `ufw` |

Check [lego's DNS provider list](https://go-acme.github.io/lego/dns/) for the exact variable names
of any provider other than the two verified here (`hetzner` → `HETZNER_API_TOKEN`; `cloudflare` →
`CF_DNS_API_TOKEN`, a single token with **Zone:Read + DNS:Edit**). Every lego variable also accepts
a `_FILE` suffix.

**The one difference worth designing around: GCP can hold zero DNS secrets.** With Cloud DNS and
provider `gcloud`, Traefik authenticates as the instance's attached service account — grant it
`roles/dns.admin` scoped to the zone and there is **no token in user-data, no token on disk, and
nothing to rotate**. That deletes an entire class of incident (leaked token in an image, in a
snapshot, in a metadata dump). AWS gets most of the way there with an instance profile for Route 53.
Hetzner has **no equivalent** — the token must exist somewhere on the box, which is why §4 spends a
paragraph on how to hold it and §9 insists you rotate it. If your DNS is portable and this property
matters more than your host provider choice, that is a legitimate reason to host DNS elsewhere from
the VM.

---

## 9. Hardening checklist

- [ ] **Firewall down to 80 / 443 / 22.** Nothing else needs to be reachable — including
      `pstack serve`.
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
- [ ] **SSH: keys only.** `PasswordAuthentication no`, `PermitRootLogin no`.
- [ ] **Never publish the Docker socket.** No `-p 2375`, no socket-over-TCP, no
      socket-mounted container that accepts external input. Traefik's mount is `:ro`, which stops
      accidents, not an attacker — the read-only Docker API still enumerates every container, its
      env, and its labels. Consider a socket proxy that whitelists the container/network endpoints
      Traefik actually needs.
- [ ] **`pstack serve` is never exposed without `PSTACK_TOKEN`.** pstack enforces this (exit `3`);
      do not defeat it by setting a token and then skipping the ingress auth. Remember: `docker`
      group ⇒ root. And note the token gates **mutations only** — `GET /api/jobs/:id` (full hook
      transcripts, including stderr) and `GET /api/deployments` answer without it, so ingress auth is the
      only gate on reads.
- [ ] **Rotate the DNS token.** Scope it to the one zone, note that a copy lives in user-data, and
      rotate on a schedule and on any suspicion. Rotation = new token in
      `/etc/traefik/secrets/hetzner_dns_token` + `docker compose … up -d --force-recreate traefik`.
- [ ] **`mem_limit` on every service, in every stack.** Repeated because it is the difference
      between one PR breaking and the host breaking.
- [ ] **Back up the `letsencrypt` volume.** Losing `acme.json` means re-issuing, and re-issuing
      under a rate limit at the wrong moment means no TLS at all for hours.
      ```bash
      docker run --rm -v shared_letsencrypt:/le -v /root/backup:/out \
        alpine tar czf /out/acme-$(date +%F).tgz -C /le .
      ```
- [ ] **Unattended security upgrades on**, and reboot on kernel updates during a quiet window.
- [ ] **Run `pstack verify` on a schedule.** A nightly sweep is how leaks get found while they are
      still cheap. `verify` resolves one `${PR}` per invocation, so the sweep is a loop:
      ```bash
      cd /opt/preview/config
      for pr in $(gh pr list --state all --limit 50 --json number -q '.[].number'); do
        PR=$pr /usr/local/bin/bun /opt/preview/pstack/src/cli.ts verify -q || echo "PR $pr: exit $?"
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
- [ ] **Never point `pstack` at the control stack.** No spec should name the compose project that
      runs Traefik and the API. Upgrade it from the host (§7); a `down` there takes out
      `acme.json` — every certificate, re-issued under a per-week rate limit.

---

## 10. Troubleshooting

### Certificate never issued

Check in this order; each rules out the layer above.

| Check | Command / expectation |
|---|---|
| Did the file provider load at all? | `docker compose -f /opt/preview/shared/docker-compose.yml logs traefik \| grep -i 'provider\|dynamic'` — a parse error here usually means something non-config in `/etc/traefik/dynamic` (the htpasswd trap, §5) |
| Is any router requesting a cert? | Dashboard → HTTP → Routers; exactly one should show a resolver |
| Is the token readable inside the container? | `docker compose … exec traefik wc -c /run/secrets/hetzner_dns_token` |
| Did ACME even try? | `… logs traefik \| grep -i 'challenge\|acme'` — no challenge line at all ⇒ no router asked |
| Rate limited? | The log says so explicitly. Cause is almost always many routers each with `certResolver`. Fix the config, then use Let's Encrypt's **staging** CA (`--certificatesresolvers.letsencrypt.acme.caserver=…`) while iterating. |
| `acme.json` permissions | must be `0600`; Traefik refuses a world-readable store |

Also: a router whose `Host()` rule is **not** covered by the wildcard (two labels deep — §3) gets
Traefik's self-signed default. The log is quiet; only the browser complains.

### DNS propagation timeouts

Symptom: `time limit exceeded: last error: NS … did not return the expected TXT record`.

| Cause | Fix |
|---|---|
| Traefik checks before the zone has converged | raise `--certificatesresolvers.<name>.acme.dnschallenge.propagation.delaybeforechecks` (start `30s`, try `120s`) |
| Provider API is slow to publish | raise `HETZNER_PROPAGATION_TIMEOUT` (seconds) |
| Precheck resolver is serving stale/split-horizon answers | set `--certificatesresolvers.<name>.acme.dnschallenge.resolvers=1.1.1.1:53` (or your provider's authoritative NS) |
| Token lacks write scope on the zone | confirm by hand: `dig +short TXT _acme-challenge.preview.example.com @1.1.1.1` during a challenge — nothing appearing means the write never happened |

A `CNAME` on `_acme-challenge` that points somewhere the token cannot write is a classic silent
failure.

### cloud-init failures

```bash
cloud-init status --long                       # which module failed
sudo tail -n 300 /var/log/cloud-init-output.log  # the real command output
sudo cloud-init schema --system --annotate     # YAML that cloud-init rejected
```

| Symptom | Cause |
|---|---|
| `usermod: group 'docker' does not exist` | you dropped the top-level `groups:` block — the `users` stage runs before `runcmd` installs Docker |
| `bun: command not found` in the unit | Bun installed to `/root/.bun`; use `BUN_INSTALL=/usr/local` and an absolute `ExecStart` |
| Steps after `docker network create` never ran | the network already existed and a non-zero `runcmd` step aborts the rest — that is what `|| true` is for |
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
| Does the router exist? | Dashboard → Routers. Missing ⇒ `traefik.enable=true` absent (remember `exposedbydefault=false`) |
| Is the container on `preview-ingress`? | `docker inspect -f '{{json .NetworkSettings.Networks}}' <ctr> \| jq keys` |
| Does the network match on both sides? | `preview-ingress` must be `external: true` in **both** the shared stack and the per-PR file. Declare it non-external in the per-PR file and Compose creates a *different* network named `pr-123_preview-ingress` — the container is up, healthy, and unreachable. **Same root cause as a missing `traefik.docker.network` label.** |
| Multi-network container? | add `traefik.docker.network=preview-ingress`, else Traefik may dial the project `default` network it isn't on |
| Router name collision? | router names are global — namespace with `${STACK}` or one PR silently wins |
| `502` instead of `404`? | route found, backend refused: check `loadbalancer.server.port` against what the app actually listens on, and that it binds `0.0.0.0` rather than `127.0.0.1` |

`404` = Traefik has no route. `502` = Traefik has a route and cannot reach it. Do not debug the
first as if it were the second.
