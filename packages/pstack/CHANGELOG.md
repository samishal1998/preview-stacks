# Changelog

## 0.29.0 — 2026-08-23

### ⚠️ Breaking: runtime — pstack is one static Go binary

The control plane is now a Go program, released as `pstack_{linux,darwin}_{amd64,arm64}` on
[GitHub Releases](https://github.com/samishal1998/preview-stacks/releases) with an `install.sh`
that verifies the checksum. `@samyx/preview-stacks` on npm is deprecated and stops at 0.28.0. The
client SDK (`@samyx/preview-stacks-client`) and the advanced UI (`@samyx/preview-stacks-ui`) are
unchanged and still on npm.

**It is a drop-in replacement.** The registry files, the SQLite database (every argon2 hash keeps
verifying), `control/.env`, `control/dns.env` and the rendered control compose are read unchanged;
the HTTP contract is byte-identical, so the UI and the client SDK work as they are; every CLI
flag, usage line and exit code is the same. This was proven, not asserted: a black-box conformance
suite (`packages/conformance` — 214 tests, 80 exact CLI transcripts, a complete host fixture) graded
the Go binary against the TypeScript reference until the two answered identically, and a
differential runner replayed nine API scenarios on both over one data path — including the docker
argv each issued — until the traces matched step for step. The three remaining wording differences
are listed in `docs/port-status.md`.

**The one-time move**, on a host provisioned before 0.29.0 (first upgrade to 0.28.0 if you are
older; its `pstack upgrade --to 0.29.0` prints exactly this line and exits 3):

```
curl -fsSL https://github.com/samishal1998/preview-stacks/releases/download/v0.29.0/install.sh | sh && pstack upgrade --resume
```

Rollback: `docker tag pstack:local-previous pstack:local && docker compose -p pstack-control -f <PSTACK_DATA>/control/docker-compose.yml up -d`.

What changes around the binary:

- **The control image carries no JavaScript runtime.** It is `debian:bookworm-slim` + `bash`,
  `ca-certificates`, `curl`, the docker CLI and compose plugin, and the binary. **Axis hooks run
  inside that container**: a hook that called `bun`, `bunx`, `node`, `npm` or `npx` must bring its
  own — grep your specs before the hop. The previous image (`oven/bun:1`) also lacked `curl` and
  `ca-certificates`; those are new.
- **`pstack build-image`'s generated Dockerfile installs this version with the release's own
  `install.sh`** instead of `bun add`ing a package — an empty build context, so the host needs
  nothing but docker, and it works from macOS since the build runs Linux. It does need the release
  to be reachable at build time; `PSTACK_BINARY=<path>` copies a local binary in instead (an
  unpublished build, or no network). The `-previous` retag is unchanged, and the advanced UI image
  still fetches `@samyx/preview-stacks-ui` from npm inside the build.
- **`pstack upgrade`'s first phase is the installer** (`PSTACK_VERSION` pinned, into the directory
  the running binary lives in), then the unchanged `pstack upgrade --resume`.
- **`pstack cloud-init` renders the installer instead of the Bun install block**; re-render saved
  cloud-configs before provisioning a new host. The distro list and everything else is the same.
- **`go install github.com/samishal1998/preview-stacks/packages/pstack/cmd/pstack@latest`** works.
- There is no importable library; the programmatic surface is the HTTP API.

### Fixed (strictly tighter; not observable from a correct client)

- `PUT` and `DELETE` on a deployment hold the stack's job lock across their check-then-act, so a
  lifecycle `POST` racing them gets the ordinary 409 instead of a window.
- The job stream, opened between a job's last line and its end, now sends `done` immediately
  instead of hanging (the reference could miss the end between `GET` and subscribe).

### Known wording deviations from 0.28.0

- A malformed `%` escape in a path is `400 text/plain` from the HTTP stack, where 0.28.0 answered
  `500 {"error":"URI malformed"}`.
- A YAML parse error on a routing file carries the Go parser's message after `not valid YAML: `.
- A notifier delivery that cannot connect stores Go's sentence (`dial tcp …: connection refused`)
  where Bun said `Unable to connect. Is the computer able to access the url?`.
- String orderings of routing files and registries are byte order, not locale order.

## 0.28.0 — 2026-08-22

The **last TypeScript release**. From 0.29.0 pstack is a single static Go binary released on GitHub
(`github.com/samishal1998/preview-stacks`), and this release is the bridge to it: nothing here
changes how a host behaves, and everything here is what the hop needs.

### Fixed

- **`pstack upgrade` no longer blanks the DNS-01 credential.** `init` rewrites `control/dns.env`
  from `PSTACK_DNS_TOKEN` on every run, and `upgrade` — whose whole purpose is "read back what init
  decided so nothing rotates" — never carried it. On a dns01 host the Cloudflare/Hetzner token was
  zeroed, Traefik recreated with no credential, and the wildcard's renewal failed silently weeks
  later. `readControlState` now reads the token back from `dns.env` and the re-init step receives
  it; the dry-run plan says `(with the existing PSTACK_TOKEN and PSTACK_DNS_TOKEN)`.
- **`pstack build-image` keeps the image it replaces** as `<tag>-previous` (`pstack:local-previous`),
  so a build that comes up unhealthy after `init` recreates the stack has a one-line rollback:
  `docker tag pstack:local-previous pstack:local && docker compose -p pstack-control -f <PSTACK_DATA>/control/docker-compose.yml up -d`.

### Added

- **`pstack healthcheck`** — one GET against `/api/health` on `PSTACK_PORT`, exit 0 or 1. The three
  Dockerfiles' `HEALTHCHECK` lines call it instead of carrying a `bun --eval` one-liner each, so the
  check depends on nothing but the binary. `init` blocks on this verdict, and Traefik drops every
  router on an unhealthy control container (the wake catch-all included), so it stays boring.
- **`serve` tuning knobs** for a harness that drives the real process black-box:
  `PSTACK_READINESS_POLL_MS`, `PSTACK_READINESS_TIMEOUT_MS`, `PSTACK_SSO_STATE_TTL_S`,
  `PSTACK_SSO_DISCOVERY_TTL_S`. Unset means the default; a host never needs them.
- **`pstack upgrade --to 0.29.0` (or later) refuses** and prints the one-time hop instead of handing
  a version npm never gets to `bun install -g`:
  ```
  curl -fsSL https://github.com/samishal1998/preview-stacks/releases/download/v0.29.0/install.sh | sh && pstack upgrade --resume
  ```
  `--resume` is never refused by version — it is phase 2 of that hop.

## 0.10.0 — 2026-07-30

### ⚠️ Breaking: the API now requires authentication on every route

Reads included. To upgrade an existing host:

1. **Create the first account.** Set `PSTACK_ADMIN_USER` / `PSTACK_ADMIN_PASSWORD` and re-run
   `pstack init` (the control compose now passes them through), or once the stack is up:
   ```
   curl -X POST https://api.<domain>/api/auth/bootstrap \
     -H "Authorization: Bearer $PSTACK_TOKEN" -H 'content-type: application/json' \
     -d '{"username":"you","password":"…"}'
   ```
2. **Re-run `pstack init`** regardless — the control stack gains a `${DATA_DIR}/db` mount (the SQLite
   home) and the admin-env passthrough. `init` is idempotent.
3. **CI is unaffected.** `PSTACK_TOKEN` stays valid as the machine credential on every route; nothing
   in a pipeline changes. For finer-grained access, mint a personal token in the UI.

### Added

- **Accounts, sessions, and personal API tokens**, backed by `bun:sqlite` (`<dataDir>/db/pstack.db`,
  0600 — one file, so "back up the host" stays "copy the directory"; the deployment registry remains
  the file-based cache-of-intent it was). Passwords are argon2id via `Bun.password`; sessions and
  tokens are stored as SHA-256, never plaintext — a database read is not a session hijack.
- **Three ways to authenticate**, mirrored in both UIs:
  - a **session cookie** from username/password login — httpOnly, and the only form a browser can
    attach to `EventSource` and (soon) `WebSocket`, which is *why* sessions are cookies;
  - a **personal token** (`pstack_pat_…`), shown once, for scripts that should not hold root;
  - **`PSTACK_TOKEN`**, unchanged, as the root/machine credential.
- Login page + auth guard in the advanced UI; a sign-in row in the basic UI. A signed-out visitor is
  redirected to `/login` carrying their destination, and told how to bootstrap when no account exists
  yet (a dead login form with no way to make the account is the worst version of that page).

### Security

- **This resolves `docs/secret-exposure.md`.** Job outcomes carry captured credentials *by design*
  (`outcome.outputs` is the inter-axis env channel), so the previous "reads are open" posture was a
  credential feed to anyone who could reach the port. Every data route is now behind the gate; the
  firewall/tunnel guidance becomes defence in depth rather than the only barrier.
- Bootstrap is honoured **only while no account exists**, so a leaked compose file carrying the admin
  env pair cannot mint admins later. Wrong-password and unknown-user return an identical refusal (no
  username oracle). The last account cannot be deleted.

## 0.9.0 — 2026-07-30

### Added

- **`pstack cloud-init --distro ubuntu|debian|fedora|suse|arch|alpine`** (default ubuntu). What
  actually differs per distro is smaller than it looks — how Docker is installed and how its service
  is enabled — so a distro is a table row in the generator, not a second template:

  - **ubuntu / debian / fedora**: Docker's own repositories (the vendor-recommended path). Fedora
    downloads the `.repo` file directly instead of using `dnf config-manager`, whose flag syntax
    changed between dnf4 and dnf5.
  - **suse / arch / alpine**: the distro's own packages — Docker publishes no repository for these.
    Because their compose package may ship a standalone `docker-compose` binary rather than the CLI
    plugin (and pstack shells out to `docker compose`, the plugin form), every render carries a
    plugin-symlink fallback.
  - **alpine**: OpenRC (`rc-update add docker boot`) instead of systemd, and `bash` + `sudo` added to
    the package list — the file's own runcmd lines depend on both and the base image has neither.
  - suse/arch/alpine renders carry a **DISTRO NOTE in the file itself** (cloud-init support varies by
    image, musl, distro-repo Docker) — the file is what gets saved and reused; a terminal warning
    scrolls away.

### Fixed

- **The generated cloud-config now pins bun and pstack** to the versions that rendered it
  (`@samyx/preview-stacks@<version>`, `bash -s "bun-vX.Y.Z"` — the same stamp-own-version pattern the
  generated Dockerfiles use). Unpinned, a saved file reused months later installed whatever was
  latest that day — a control plane the rest of the file was never written for. To be precise about
  the exposure: a machine *restart* never re-runs `runcmd` (cloud-init is once per instance); the
  risk is **re-provisioning from a saved copy**. Docker is deliberately left repo-latest — the
  distro's security updates are wanted, and the file says so where the decision lives.

## 0.8.0 — 2026-07-30

No host change needed. Upgrade the CLI, rebuild the images.

### Added

- **Logs for one container.** `GET /api/deployments/:id/logs?service=<name>` narrows to a single compose
  service, and the Logs tab gets a picker. On a stack with a chatty sidecar the interleaved output is
  unreadable and the lines you want are already past the tail — narrowing spends the whole tail on the
  service you care about. The picker is built from what is actually **running** rather than from the
  compose file, because a declared-but-never-started service has no logs and offering it would produce
  an empty pane that reads like a bug. The service name is validated against compose's own alphabet
  before it reaches a shell, and shell-quoted after that.

- **Duplicate a deployment** — `/submit?from=<id>`, reachable from a deployment's Overview. Copies the
  spec, the compose file and the variables into a **new** submission with an empty id, so nothing is
  written over.

  With a guard, because duplicating has one sharp edge: copy a spec whose `stack:` is a literal and two
  records drive the *same* compose project — `down` on either stops the other's containers and `verify`
  on either reports the other's leaks. Two defences: the form says so before you submit, and the server
  independently resolves every other deployment and reports `stackSharedWith` on a new submission. It is
  a **warning, not a refusal** — it can be deliberate, the check cannot know, and refusing over a guess
  would be worse. Absent rather than `[]` when there is no clash, so "checked and clear" cannot be
  confused with "not checked".

### Fixed

- The Logs tab's service picker never appeared: its watcher ran `immediate` and touched a `const`
  declared further down the file, which is a temporal-dead-zone `ReferenceError` during setup — so the
  whole tab failed to render, not just the picker. It also now waits for the *resolved* deployment
  rather than only the id, since the tab mounts before the parent has resolved it.

## 0.7.1 — 2026-07-30

### Fixed

- **`PREVIEW_DOMAIN` is now the variable generated hostnames read.** `compose.subdomains` (0.3.0) and
  the generated labels (0.6.0) looked only at `DOMAIN` — which is the **control stack's** variable,
  written to `control/.env` to interpolate the control compose file, and carried into the *deployment
  spec* surface by mistake. Every example, doc, the skill and the basic UI in this repo use
  `PREVIEW_DOMAIN`, so a spec copied from `examples/preview.yml` was refused with "needs a domain to
  anchor its rules to".

  `DOMAIN` is still accepted, so a spec written against 0.3.0–0.7.0 keeps working. Precedence, most
  specific first:

  1. `PREVIEW_DOMAIN` declared by the spec's own `env:`
  2. `DOMAIN` declared by the spec's own `env:`
  3. ambient `PREVIEW_DOMAIN`
  4. ambient `DOMAIN`

  **A declaration beating an ambient value is the part that matters.** A spec's `env` is seeded from the
  whole process environment before its own `env:` is layered on, so a stray exported `DOMAIN` could
  otherwise anchor every generated hostname — silently, on the wrong domain, producing a router that
  deploys and never matches. `DOMAIN` is generic enough for that to happen by accident; `PREVIEW_DOMAIN`
  is not, which is the other reason it is preferred.

  Both set and disagreeing is a **warning**, not an error: an ambient `DOMAIN` beside a declared
  `PREVIEW_DOMAIN` is a normal accident and refusing to parse over it would be hostile. `validate`
  prints it and names which one was used.

- Docs, the minimal example and the label snippet in the UI corrected to `PREVIEW_DOMAIN`; the two
  error messages now name it and say `DOMAIN` is a legacy alias.

## 0.7.0 — 2026-07-30

### ⚠️ Upgrading an existing host

**Re-run `pstack init`** if you pull from a private registry. The control stack now mounts a
`DOCKER_CONFIG` directory into the API container, which is where the credentials live. `init` is
idempotent. Until then the Registries page reports the directory as not writable and says so.

### Added

- **Private registry credentials** — a *Registries* page over `GET /api/registries`,
  `PUT|DELETE /api/registries/:host`.

  The answer to "are the daemon's creds mounted" was **no**, and the reason it matters is
  counterintuitive: **an image pull is authenticated by the client, not the daemon.** `docker pull`
  reads its *own* `config.json` and hands the credential over in an `X-Registry-Auth` header; the daemon
  never consults the client's config. pstack shells out to compose from *inside* the control container,
  so a `docker login` on the host writes a file that client cannot see — and a private image fails with
  `pull access denied` on a host that is demonstrably logged in, with nothing in the error pointing at
  why.

  The control stack now mounts `<DATA_DIR>/control/docker` → `/docker-config` with `DOCKER_CONFIG` set.
  Add credentials from the host (`docker login --config <DATA_DIR>/control/docker <registry>`) or over
  the API — both land in the same file.

  **On demand, with no restart:** the CLI re-reads `config.json` on every invocation, so a credential
  added now applies to the next pull. Nothing to recreate and no cache to bust.

  **Write-only.** A `config.json` entry is `base64("user:password")` — reversible, not encrypted — so
  there is no read path for it anywhere in the API. `GET /api/registries` returns hostnames and
  usernames only, and nothing in `src/registries.ts` can return a password. The file is written `0600`
  and atomically, because compose may read it at any moment and a truncated config parses as *no*
  credentials.

  Two traps handled explicitly:

  - **Docker Hub's key is `https://index.docker.io/v1/`**, not `docker.io` — a credential stored under
    the friendly name is silently never used for `nginx:alpine`. Every alias normalizes to the canonical
    key on the way in *and* on the way out, so you can delete what you created.
  - **Credential helpers do not transplant.** A `config.json` copied from a laptop usually carries
    `credsStore: "desktop"` and **no** `auths`, the secrets being in the OS keychain. That binary does
    not exist in the container, so pulls fail with `error getting credentials` while an empty `auths`
    looks like the cause. Helpers found in the file are reported, and the UI says they will not work.

- `docs/control-plane.md` §4c covers all of the above.

### Fixed

- `init` applies an explicit mode to the credential directory rather than relying on `mkdir`'s, which is
  masked by the process umask — a 0700 request could land as 0755 on a directory holding credentials.

## 0.6.0 — 2026-07-30

No host change needed. Upgrade the CLI, rebuild the images.

### Added

- **`pstack.routing.port` — pstack generates the Traefik labels and network wiring.** A reachable
  preview service needed four labels and two network declarations, all boilerplate differing only by
  name and port, and every one with a silent failure mode. Now:

  ```yaml
  services:
    app:
      image: nginx:alpine
      profiles: [app]
      labels:
        - pstack.routing.port=80              # the port INSIDE the container
        # - pstack.routing.service_name=web   # optional; defaults to the compose service name
        # - pstack.routing.host=…             # optional; overrides the convention
  ```

  generates `traefik.enable=true`, `traefik.docker.network=preview-ingress`, the router rule for
  `<name>-<stack>.<domain>`, the entrypoint, `tls`, the `loadbalancer.server.port`, the wildcard router
  when `compose.subdomains` names that profile, and the networks — on the service *and* at the file
  root, with `external: true`.

  `tls.certresolver` is included **only** when the host is not DNS-01, read from the running Traefik's
  own flags rather than a setting, so the generated labels cannot disagree with the host they land on.
  The stack is in every router and service id because Traefik's namespace is global across the daemon.

  **It never overrides you.** A service with *any* `traefik.*` label is left completely alone — labels,
  networks and all. The presence of your own label *is* the opt-out; there is no flag to remember. A
  service with neither kind of label is also left alone, because a database should not get a hostname.

  **A derived file, not an overlay.** pstack reads the submitted file and writes a complete
  `compose.generated.yml` beside it — your file is never modified. An overlay would put Compose's merge
  semantics for list-form `labels` in charge of whether *your* routers survive a merge, and getting that
  wrong deletes them silently. The derived file is **JSON**, which is valid YAML 1.2 and what every
  parser agrees on; emitting YAML would mean trusting a stringifier's quoting to match Go's parser on
  values like `` Host(`app.example.com`) `` — a mismatch that would only surface on your host.
  Regenerated on every compose subcommand, so `up` and `down` cannot disagree about a router's name.

- `pstack validate` prints the hostname and container port it would generate per service — pstack
  derives the hostname, so seeing what it derived is how a wrong `DOMAIN` is caught before Traefik
  silently never matches.
- **`examples/docker-compose.minimal.yml`** — the same stack as the hand-written example with the
  boilerplate gone, including a `postgres` that is deliberately *not* routed.

### Changed

- `Runner` now exposes its `cwd`. Compose needs it to find the submitted file and write the derived one;
  the alternative was threading a `dir` argument through `up`/`down`/`verify` and four compose helpers.
- A dry run writes no derived file — a dry run must have no side effects.

## 0.5.0 — 2026-07-30

No host change needed — 0.4.0's `pstack init` is enough. Upgrade the CLI, rebuild the images.

### Added

- **`Containers & routes` tab on a deployment** (`GET /api/deployments/:id/runtime`) — the tab that
  answers "the hostname does not work", which nothing here could answer before. The registry knows
  what you *submitted* and the logs know what the container *says*; the reason a request 404s is in
  neither. It is in the container's Traefik **labels**, which pstack writes none of and until now read
  none of, so the answer was always "SSH in and run `docker inspect`".

  Shows containers with their state, health, **container-internal ports**, networks and ingress IP;
  and a routing table whose `forwards to` column is the address Traefik actually assembled —
  `<ingress-ip>:<port>`. That column *is* the URL→port mapping, and a missing half shows as
  "no port declared" or "not on the ingress network" instead of being inferred.

  Each of the five rules in `examples/docker-compose.preview.yml` became a finding, so the prose
  nobody re-reads is now on the page:

  - **no Traefik labels at all** → nothing routes here, with the four labels needed;
  - **labels but no `traefik.enable=true`** → the host runs `exposedbydefault=false`, so Traefik
    ignores the container entirely and the hostname 404s silently;
  - **on `<project>_preview-ingress` instead of `preview-ingress`** → the compose file is missing
    `external: true`; the container is healthy and unreachable and every listing looks correct;
  - **several networks and no `traefik.docker.network`** → Traefik has to guess which to dial;
  - **`tls` with no `certresolver` on an HTTP-01 host** (and the inverse on DNS-01) → the route exists
    and the handshake fails. The challenge mode is read from the running Traefik's own flags, so the
    check cannot disagree with the host;
  - **a router name declared by two containers** → Traefik's router namespace is global across the
    daemon, so they overwrite each other and one hostname serves the wrong container. That one cannot
    be seen from a single deployment, so the check reads every container on the host.

- **The Routing page shows live routes**, from container labels, above the config files. Previously it
  showed only Traefik's *file* provider — so a deployment could go up, its hostname appear, and
  nothing on the page called "Routing" would change. Per-PR routers are the *docker* provider; both
  are now on the page, and each live route links to its deployment.

- `docs/control-plane.md` §4a spells out how a URL reaches a port — including the step that surprises
  people: Traefik does **not** resolve `service_name:port` over compose DNS. Its docker provider reads
  the container's IP on `preview-ingress` and dials `<ip>:<container-port>`, which is why the ingress
  attachment matters, why the port is the internal one, and why publishing a host port does nothing
  for routing.

### Security

- `docker inspect` returns `Config.Env` — the container's whole environment, where database passwords
  live. Nothing in the new code passes an inspect payload through: fields are picked out by name, only
  `traefik.*` labels are kept, and their values go through `redactText` (a
  `middlewares.*.basicauth.users` label is a credential too). A test plants a secret in `Env` and
  asserts it appears nowhere in the response.

### Notes

- "Docker did not answer" stays distinct from "nothing is running" throughout: `reachable: false` with
  no findings invented from an absence. Collapsing those is how a UI reports a live stack as torn down.

## 0.4.0 — 2026-07-29

### ⚠️ Upgrading an existing host

**Re-run `pstack init`.** The control stack now mounts Traefik's dynamic-config directory into the API
container, and that mount is what the routing editor needs. `init` is idempotent; it rewrites the
compose file and recreates the control containers. Until you do, the API reports the directory as
`writable: false` and the UI says exactly that, rather than failing one save at a time.

### Added

- **Traefik dynamic config is editable from the UI** — a *Routing* page over
  `GET /api/routing`, `GET|PUT|DELETE /api/routing/:name`. The file provider watches a *directory*, so
  this is a list of files, one per concern: middleware (basicAuth, rate limits, IP allow-lists), TLS
  options, the fallback router, routes to anything running outside compose. Previously that directory
  was created by `init` and then only reachable over SSH.

  It is the highest-blast-radius surface in the product, so the guards are the feature:

  - **Validated before anything is written.** Not just "is it YAML" — the top-level keys must be
    Traefik's (`http`, `tcp`, `udp`, `tls`). `htttp:` is perfectly good YAML that configures nothing,
    and Traefik will not tell you; it is now refused by name with the real sections listed.
  - **Atomic writes.** `writeFile` truncates then fills and the watcher can fire in between, so the
    content goes to a temp file and is `rename`d into place — Traefik only ever sees a whole file.
  - **Nothing but config in that directory**, since Traefik parses whatever is there. Hence **no
    on-disk history**: the obvious place to keep it is the one place it must not go. A write returns
    the previous content instead, and the UI offers it back as an in-session undo.
  - **It cannot lock you out.** `control.<domain>` and `api.<domain>` are docker labels on the pstack
    container, not file config, so no save here can break the page you would use to undo it. The UI
    says so, because "this can break everything" without that just makes people avoid the page.

  Why it matters: Traefik's documented behaviour is that one unparseable file is a parse error for the
  whole *directory*, and the rest can be discarded with it — the symptom is *other* routes
  disappearing, not the file you edited.

- **`GET /api/deployments/:id/source`** returns a deployment's stored spec and compose file, so the
  **Edit form pre-fills instead of opening empty**. That was a real footgun: replacing is
  whole-record, so retyping a spec from memory silently dropped whatever you forgot — and a dropped
  axis stops being tracked while the resources it created keep running. Being unable to read your own
  submission is what made that likely rather than careless. This is the "edit a shared service" path:
  add via Submit, edit here, remove via Tear down + Forget.

  Restricted like a named spec's source, and for a wider reason — a `kind: shared` deployment declares
  no axes, so its credentials live in the **compose file** (`POSTGRES_PASSWORD`, …). Both files are
  withheld without a token, explicitly, so a pre-filled-empty form is never mistaken for an empty
  spec. If the deployment references a stored spec, the form now **warns that saving forks it** from
  the spec every other deployment shares, and links to the spec instead.

- Editing is reachable from a deployment's **Overview** tab, not only from the bottom of *Danger* —
  and the link reads "Edit spec & compose", because that is now what it does.

### Fixed

- `RoutingError` was mapped to 400 inside the *deployments* branch instead of the global handler, so a
  rejected routing file answered **500**. Caught by a test asserting the status, not by review — the
  anchor line it was inserted after appears twice in `api.ts`.
- The `serve` command probed for the in-container Traefik path with `Bun.file(dir).exists()`, which is
  **`false` for a directory** (verified). That would have sent the containerised process to the
  host-side path, where the directory does not exist — the feature would have reported itself
  unavailable on exactly the deployment it exists for. It uses `stat().isDirectory()` now.

## 0.3.0 — 2026-07-29

### Added

- **`compose.subdomains` — wildcard subdomain routing per profile.** Anything under
  `<profile>-<stack>.<domain>` reaches the same service the bare host does, for apps that dispatch on
  subdomain (a tenant per host, a branch per host) instead of one hand-written router per name.

  ```yaml
  compose:
    profiles: [backend, frontend]
    subdomains: [backend]          # or { backend: any }, or { backend: { host: … } }
  ```

  pstack does not own your Traefik labels — your compose file does — so this exports the rule as
  **`PSTACK_WILD_<PROFILE>`** for a label of yours to interpolate:

  ```yaml
  - traefik.http.routers.backend-wild.rule=${PSTACK_WILD_BACKEND}
  - traefik.http.routers.backend-wild.priority=2
  - traefik.http.routers.backend-wild.service=backend
  ```

  No new mount, no re-`init`, no capability added to the API, and nothing pstack writes can break
  another deployment's routing. `src/subdomains.ts` records the three designs rejected to get there.

  **A hardcoded host always wins**: Traefik's default priority is the rule's length, so an exact
  `Host(…)` scores in the dozens against the wildcard's pinned `2` (`2`, not `1`, so it also clears the
  `preview-fallback` catch-all).

  The variable is exported on **every** compose subcommand, not just `up` — compose interpolates the
  file each time, and an unset variable on `down` would have it reasoning about a differently-labelled
  project than the one it created.

  **`depth` defaults to `one` because that is what DNS and TLS can deliver.** `any` matches any depth,
  and is honest about being routing-only: a DNS wildcard covers exactly one label (`*.*.host` is not a
  valid record), and **no certificate can ever cover `any`** — `*.*.host` is not a legal SAN. Even
  `depth: one` needs DNS-01 and one certificate per PR, against Let's Encrypt's ~50 per registered
  domain per week. All of it is tabulated in
  [`control-plane.md`](../../docs/control-plane.md#wildcard-subdomains-under-a-surface) and worked
  through in [`usage.md`](../../docs/usage.md).

- `pstack validate` prints the resolved wildcard host, its profile, the depth and the variable name —
  because pstack derives the hostname, so seeing what it derived is how a wrong `DOMAIN` gets caught
  before Traefik silently never matches. The resolved spec over HTTP carries the same, and the
  advanced UI shows it on a deployment's Overview.

### Changed

- `SpecError` moved to `src/errors.ts` and is **re-exported from `src/spec.ts`**, so every existing
  import and `instanceof` check is unaffected. `subdomains.ts` throws it and `spec.ts` calls into
  `subdomains.ts`; keeping the class in `spec.ts` would have been an import cycle over a `class`
  binding, which is not hoisted — it would have "worked" only for as long as every use stayed inside a
  function body.

## 0.2.4 — 2026-07-29

### Security

- **`GET /api/specs/:name` served a spec's source to anyone who asked.** Reads are unauthenticated on
  this API by design, but a spec's hook bodies are shell strings that routinely carry a registry
  password or an API token inline — and the resolved-spec view already withholds hook bodies for
  exactly that reason, so serving the whole file one route over handed them out anyway.

  The metadata (name, kind, description, and the *names* of required variables — none of them a
  credential) is still public; `source` now requires a token, and is withheld **explicitly** via
  `sourceWithheld: true` rather than sent empty, so a page can say why instead of rendering a blank
  editor that reads as an empty spec. A wrong token is treated as no token rather than a 401, so this
  route cannot be used to probe token validity. Covered by tests that boot the real server and assert
  the credential appears nowhere in the unauthenticated response body.

  The deployment reads were checked and are clean — they carry hook *names* only.

### Added

- **Specs pages in the advanced UI**: a list (kind, required variables, which deployments use each
  one, last updated, search) and a detail view. “Used by” is joined from the deployment list already
  in memory, and it is the fact you need *before* trying to delete a spec — that is refused while a
  deployment still references it. The detail view is also where the withheld-source notice appears.
  Storing and deleting a spec are still API-only.

### Changed

- **Icons come from lucide** (`lucide-vue-next`, per-glyph imports so only what is used ships)
  instead of six hand-drawn SVG paths. The info affordance was a typed letter `i` — a text glyph
  inherits font metrics and italics, so it never sat centred in its own circle; it is now lucide's
  `Info`, with no border of its own, since the glyph already draws a circle.

### Fixed

- `PUT /api/specs/:name` answered **500** for a malformed spec. `SpecStoreError` was mapped to 400
  only inside the deployments branch, so the spec route fell through to the generic handler — a
  caller's bad input reported as a server fault.

## 0.2.3 — 2026-07-29

### Changed

- **The UI says what things do, not which request it sends.** HTTP verbs, status codes and route
  paths are the tool's internals, not the operator's vocabulary, so they are gone from the page:
  buttons read **Deploy / Verify / Tear down**, a refusal reads “Refused — nothing was started”, and
  error notices name the intent (“load the logs”) rather than the call. The spec's own vocabulary —
  `up`, `down`, `assert_gone`, axes, hooks — stays, because anyone on those screens writes a spec.
- **Detail moved behind an `i` hint instead of crowding the page.** A new `InfoHint` reveals context
  on hover, focus or click (click pins it, Escape closes) — so the “why” is one character of layout
  instead of a permanent paragraph. Roughly a dozen always-on explanations moved into one, and the
  duplicate shared-deployment warning at the top of a detail page collapsed to a single line, since
  the warning that matters lives beside the button that does the damage.
- **Dark theme is lifted and saturated.** Panels were near-black on a near-black page, which read as
  one flat sheet and left the status colours muddy: the greys now start above `#0f` and step up per
  layer, and the status hues are the vivid end of each family. Body type is 15px (was 14), the whole
  scale moved up with it, and radii went up one step per nesting level.
- **The sidebar is a floating panel** — rounded on all four corners and inset from the viewport edge,
  rather than a flush rail with a divider. On mobile the bottom bar floats the same way.
- **Monospace is for code again**, not for names, dates, durations or action words — it stays on
  logs, YAML, hooks, paths and encoded values, which is what it was for.

### Fixed

- A hint anchored near the bottom of the viewport (the sidebar footer) opened downward and was
  clipped off-screen; it opens upward there now.
- The control-stack note and the compose-file placeholder showed their markdown backticks literally.
- “1 axis/axes cannot be verified:dns” — a missing space (Vue's `condense` whitespace mode drops a
  whitespace-only text node containing a newline between two elements) and an unpluralised count.
- The teardown panel claimed “no variables” whenever none were set in the browser, when the server
  actually falls back to the ones stored with the deployment — it now says so.

## 0.2.2 — 2026-07-29

### Fixed

- **`build-image` produced a corrupt Dockerfile and could not build anything.** 0.2.1 passed the
  generated Dockerfile to a shell as `printf '%s' <json-quoted> | docker build -`. JSON escaping is
  not shell escaping: inside double quotes `\n` stays two literal characters, and every backtick in
  the Dockerfile's own comments is command substitution — so `` `docker compose` `` ran and its help
  text was spliced into the file, which then died on `unknown instruction: Define`. With the control
  image unbuildable, `init` correctly refused and the host came up with nothing.

  The Dockerfile is now **written into the build context** and never interpolated into a command. A
  build context is a directory; docker gets a directory. A test asserts the file docker is handed is
  byte-identical to what was generated, and that the command line does not contain the document.
- **`cloud-init --ui advanced` no longer installs the UI package on the host.** It is a build-time
  input fetched inside the image (see 0.2.1), so the extra `bun install -g @samyx/preview-stacks-ui`
  step was left over — and it failed with "No global directory found", because unlike the pstack
  install above it lacked the `BUN_INSTALL=/usr/local` prefix. Same fix in the README examples.

## 0.2.1 — 2026-07-29

### Fixed

- **A missing advanced-UI image no longer takes the whole host down.** Compose does not fail fast
  on an absent image: it tries to *pull* `pstack-ui:local`, gets "pull access denied", and aborts
  the entire control stack — Traefik included. `init --ui advanced` now checks the image as a
  precondition and names `pstack build-image --ui` in the error. An optional UI must not be able to
  kill the host.
- **`build-image` no longer needs anything installed on the host.** The generated Dockerfiles now
  install the published package *inside the build*, with an empty context, so the UI package does
  not have to be globally installed just to build an image that was going to fetch it anyway —
  which mattered because `bun install -g` can fail outright ("No global directory found") and that
  turned an optional UI into a boot failure. `--dist-dir` / `--ui-dist` still build from local
  files for unpublished work.
- **The control image runs the package entry point rather than a global bin**, for the same reason:
  `bun install -g` needs a writable global directory that not every image or host provides.
- **`cloud-init` no longer requires an SSH key.** Providers inject their own at boot
  (`hcloud server create --ssh-key`), so demanding a second copy was friction. Given one, it is
  still validated. Omitted, the key list is left out entirely rather than emitted empty — an empty
  `ssh_authorized_keys:` parses fine and yields a user nobody can log in as.
- **`/opt/preview` is created before it is chowned.** With no config repo there was no clone, so
  the directory never existed and the step failed on a real boot.

### Added

- **`pstack dockerfile [--ui]`** — prints the image Dockerfile for you to build, tag, push or edit.
  `build-image` writes exactly this into its build context, so what it prints is what it builds.


## 0.2.0 — 2026-07-29

### Added

- **Named specs.** Store a spec once (`PUT /api/specs/:name`) and reference it from many
  deployments with `{ specName, vars }`. Previously every deployment carried its own
  byte-identical copy, so fixing a teardown hook meant re-submitting it per PR. `requiredVars` is
  detected at store time, so a deployment missing one is rejected up front naming all of them, and
  deleting a spec that deployments still reference returns 409 — an unresolvable deployment could
  never be torn down.
- **Variables are stored with a deployment.** They used to travel as `?query` params on every call,
  so `up` with `PR=7` and a later `down` with `PR=8` tore down a *different stack* and orphaned the
  first. `down` now resolves the same stack `up` created with nothing passed. Precedence is
  process env < stored vars < request vars.
- **`pstack cloud-init`** — asks for what a host needs and prints a ready-to-boot cloud-config.
  Flags first, prompts second, `-y` for scripts. Refuses a malformed SSH key (a booted host you
  cannot log into is the one mistake with no cheap recovery) and escapes every dot in the domain
  for the fallback router's regexp.
- **`pstack build-image --ui`** — builds the advanced UI image from the installed
  `@samyx/preview-stacks-ui` package. No checkout, no registry, same trick as the control image.
- **`pstack init --ui advanced`** — adds the SPA container to the control stack and repoints
  `control.<domain>` at it. The default adds no container at all, and the API keeps serving the
  basic UI on `api.<domain>` either way, so a broken advanced image degrades to a working
  interface rather than none.
- **Read-only control-stack view** (`GET /api/control`), **per-deployment logs**
  (`GET /api/deployments/:id/logs`), and declared variables with **server-side redaction** —
  secret-looking values are masked before the response leaves the host, so there is nothing for a
  UI to reveal.

### Changed

- The repository is a Turborepo workspace: `packages/pstack` (this package) and `apps/ui`.
- The web UI grew from one action panel to five views; the advanced SPA ships separately as
  `@samyx/preview-stacks-ui`.

### Notes

Both packages are versioned in lockstep. `@samyx/preview-stacks-ui@0.2.0` is its first release.


## 0.1.1 — 2026-07-28

### Fixed

- **`pstack build-image`** — a globally-installed pstack could not produce the control image
  `pstack init` requires. The package ships only `dist/`, so there was no Dockerfile to build from
  and no registry to pull from, and `init` deliberately does not pull: the install was a dead end
  whose only escape was cloning the source. `build-image` assembles a build context from the
  installed package (the bundle *is* the whole application) and tags `pstack:local`, the same
  default `PSTACK_IMAGE` uses — so `build-image` then `init` need no flags.
- `init`'s precondition hint now names `pstack build-image` rather than telling the operator to
  clone the repository.

## 0.1.0 — 2026-07-28

First release. Declarative lifecycle for ephemeral per-PR preview stacks.

### Core

- **`preview.yml` spec** — a stack identity plus ordered **isolation axes**, each with up to four
  hooks (`up`, `assert_live`, `down`, `assert_gone`). Axes provision in declaration order and
  destroy in reverse.
- **Leak verification** — `down` is best-effort (aborting halfway leaves more garbage), `verify` is
  strict and exits **2** when a resource survived teardown. Axes without `assert_gone` report
  `unverifiable` rather than passing silently.
- **Interpolation happens once**, at parse time. An undefined variable is a hard error, because
  `pr-${PR}` with `PR` unset silently becomes `pr-` — shared by every PR.
- **Compose integration** — `up` enables the selected profiles; `down` enables **every** profile,
  which is what stops one dead `<stack>_default` network accumulating per PR.
- **Assert linting** in `pstack validate` — flags a bare `! <probe>` with no reachability guard
  (it reports "gone" when the probe itself fails), `|| true` inside an assert, and any axis with
  `up` but no `assert_gone`.

### CLI

`pstack up | down | verify | status | validate | serve`, with `--dry-run`, `--set K=V`,
`--no-verify`, `-v/-q`. Exit codes: `0` ok · `1` failed · `2` leaked · `3` bad spec/usage.

### API + UI

- `pstack serve` — HTTP API over the same core. Mutating routes are asynchronous (`202` + job id,
  poll or subscribe to SSE) because `up`/`down` take minutes. One job per stack; a concurrent
  request gets `409`.
- Two security guards: a bearer token (`PSTACK_TOKEN`) on mutating routes, and a refusal to bind
  anything but loopback when no token is set.
- A single-file Vue 3 UI (no build step) for submitting, monitoring and tearing down stacks.

### Docs

`README.md` (design), `docs/usage.md` (task guide), `docs/bootstrap.md` (build a host from
scratch — Hetzner + cloud-init), `AGENTS.md` (working on this codebase), `skills/pstack/` (a skill
teaching an agent to use pstack).

### Distribution

Installs globally as a **bundle**, not source: `bun add -g @samyx/preview-stacks`. The published
tarball is 8 files / 0.36 MB — `dist/cli.js` (~74 KB) plus `dist/index.js` for embedding, with the
UI and control-stack template inlined as text imports. No source, docs, examples or skills ship.
Not a compiled executable: that would bake the Bun runtime in per platform (~60 MB each).

### TLS

**HTTP-01 is the default** — `pstack init --domain … --acme-email …` needs no DNS credential at all;
Traefik answers on port 80 (which must be reachable). It cannot issue wildcards, so every hostname
gets its own certificate: with Let's Encrypt's ~50 new certs per registered domain per week and ~3
surfaces per PR, that is roughly 16 new PRs/week, and a preview URL is not valid until its container
exists. `--challenge dns01 --dns-provider <code>` issues one wildcard instead, lifting both limits at
the cost of a DNS credential. Note the rules invert: under HTTP-01 each per-PR router needs
`tls.certresolver`; under DNS-01 exactly one always-on router requests the wildcard and the rest must
have `tls=true` only.

### Hostnames

`control.<domain>` (UI), `api.<domain>` (API), `<service>.<domain>` (a shared service), and
`<surface>-pr-<n>.<domain>` per preview. Both control and api routers point at one container and the
UI calls the API with relative paths, so it is same-origin and needs no CORS.

### Known limitations

- **Bun-only** (`Bun.serve`/`file`/`YAML`/`spawn`). Not a Node package.
- `PSTACK_IMAGE` defaults to `pstack:local`, which does not exist on a fresh host — build it from the
  repo (`docker build -t pstack:local .`) or point it at a registry you can pull.
- **Not multi-tenant, not a sandbox.** Hooks are shell strings at CI trust level.
- **No memory isolation** — set `mem_limit` per service yourself; one greedy heap can OOM every
  stack on a shared host.
- The spec has not yet been proven against a second, independent project.
