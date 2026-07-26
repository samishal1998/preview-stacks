# pstack — the control-plane image (HTTP API + web UI in one container).
#
# Built ON THE HOST from a checkout:
#
#     docker build -t pstack:local .          # `pstack:local` is PSTACK_IMAGE's default
#
# so `pstack init` finds it with no registry, no login and no image to publish. When you outgrow
# that, push it somewhere and set PSTACK_IMAGE=<registry>/pstack:<tag>.
#
# ── WHY A BUILD STAGE ────────────────────────────────────────────────────────────────────────
# The runtime layer gets `dist/` and nothing else — no src/, no ui/, no node_modules. Same reason
# the npm package ships a bundle: TypeScript is parsed once at build time instead of on every
# container start, and the image's contents stop being a second copy of the repo that can drift
# from it. Sourcemaps are emitted alongside, so a stack trace still names real functions and lines.
# ─────────────────────────────────────────────────────────────────────────────────────────────

# The Docker CLI + Compose plugin, lifted from the official image rather than installed from apt:
# the layer is ~50 MB instead of ~200 MB and needs no key/repo dance. pstack drives `docker compose`
# by shelling out, so without these the API starts fine and every operation fails.
FROM docker:28-cli AS docker-cli

# ── build: source → bundle ───────────────────────────────────────────────────────────────────
FROM oven/bun:1 AS build
WORKDIR /build

# devDependencies only (typescript, the publisher). `--frozen-lockfile` so an image build can never
# silently resolve a different tree than CI tested.
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile

# `templates/` and `ui/` are needed AT BUILD TIME even though neither is copied into the runtime
# layer: both are `with { type: 'text' }` imports, so the bundler inlines their contents. Omit them
# here and the build fails outright rather than producing a half-working image.
COPY src ./src
COPY ui ./ui
COPY templates ./templates
COPY scripts ./scripts
RUN bun scripts/build.ts

# ── runtime ──────────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1

COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
# Compose is a CLI *plugin*: `docker compose` resolves `docker-compose` out of the plugin
# directory, so copying only the binary yields "docker: 'compose' is not a docker command".
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose \
                       /usr/local/libexec/docker/cli-plugins/docker-compose

# Assert it at BUILD time. `docker compose version` is client-side and needs no daemon, so this
# costs a millisecond and turns a wrong upstream path into an obvious build failure instead of a
# runtime one on the host. If the upstream image ever moves the plugin, install from Docker's apt
# repository instead (packages `docker-ce-cli` + `docker-compose-plugin`, see docs/bootstrap.md §4).
RUN docker --version && docker compose version

WORKDIR /app

# The whole application: one bundle plus its sourcemap. The UI and the control-stack template are
# inlined into it, so there is nothing to keep as a sibling and no path resolved relative to source
# — the failure mode where an image works from a checkout and 404s once built.
COPY --from=build /build/dist ./dist

# NOTE: no placeholder spec. `serve` and `init` do not read a spec file (see SPEC_FREE in
# src/cli.ts) — the API's deployments come from the registry at $PSTACK_DATA/deployments. An
# earlier revision needed one here because the CLI loaded ./preview.yml before dispatching any
# command, which crash-looped this container behind `restart: unless-stopped` while
# `docker compose up -d` still exited 0 — a dead control plane reported as a success.

# Non-root by default: this image is also useful as a plain CLI (`validate`, `--dry-run`), where
# root buys nothing. The control stack deliberately overrides it with `user: "0:0"` because it
# mounts the Docker socket read-write — see the comment in templates/control/docker-compose.yml.
USER bun

EXPOSE 7878

# Liveness straight from the API's own route. Written in Bun rather than curl/wget so it depends on
# nothing the base image might drop, and it reads PSTACK_PORT so a non-default port still works.
# `init` waits for this to report `healthy` before claiming the control stack is up.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["bun", "--eval", "const r = await fetch('http://127.0.0.1:' + (process.env.PSTACK_PORT ?? 7878) + '/api/health'); process.exit(r.ok ? 0 : 1)"]

CMD ["bun", "dist/cli.js", "serve"]
