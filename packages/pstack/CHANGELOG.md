# Changelog

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
  `build-image` runs exactly this through `docker build -`, so what it prints is what it builds.


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
