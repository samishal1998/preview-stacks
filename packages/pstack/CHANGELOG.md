# Changelog

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
