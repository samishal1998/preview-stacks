# pstack — the control-plane image (HTTP API + web UI in one container).
#
# ── WHY THERE IS NO BUILD STAGE ──────────────────────────────────────────────────────────────
# Bun runs TypeScript directly, so there is nothing to compile: `bun src/cli.ts serve` executes
# the same files you edit and test. A bundling step here would buy nothing and cost the one
# property that makes this image debuggable — that the source in the container is byte-identical
# to the source in the repo, so a stack trace points at a real line you can read. pstack also has
# no runtime dependencies (package.json lists devDependencies only), so there is no `bun install`
# either. The only multi-stage part is stealing the Docker CLI, below.
# ─────────────────────────────────────────────────────────────────────────────────────────────

# The Docker CLI + Compose plugin, lifted from the official image rather than installed from apt:
# the layer is ~50 MB instead of ~200 MB and needs no key/repo dance. pstack drives `docker
# compose` by shelling out, so without these the API starts fine and every operation fails.
FROM docker:28-cli AS docker-cli

FROM oven/bun:1

# Copy both the binary and the plugin. Compose is a CLI *plugin*: `docker compose` resolves
# `docker-compose` out of the plugin directory, so copying only the binary yields
# "docker: 'compose' is not a docker command".
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose \
                       /usr/local/libexec/docker/cli-plugins/docker-compose

# Assert it at BUILD time. `docker compose version` is client-side and needs no daemon, so this
# costs a millisecond and turns a wrong upstream path into an obvious build failure instead of a
# runtime one on the host. If the upstream image ever moves the plugin, install from Docker's apt
# repository instead (packages `docker-ce-cli` + `docker-compose-plugin`, see docs/bootstrap.md §4).
RUN docker --version && docker compose version

WORKDIR /app

# `ui/` MUST land as a sibling of `src/`: the server resolves its static directory as `../ui`
# relative to the source file (`new URL('../ui', import.meta.url)` in src/cli.ts). Flatten this
# and every UI request 404s while /api/* keeps answering — a confusing half-broken container.
COPY package.json ./
COPY src ./src
COPY ui ./ui

# `templates/` is deliberately NOT copied. `pstack init` reads it, and `init` runs from the HOST,
# never from this container — the control plane must not be able to recreate the stack it runs in.

# An inert spec at the path `-f` defaults to. NOT a description of the control stack.
#
# WHY IT EXISTS: src/cli.ts loads the spec unconditionally, before dispatching any command, so
# `pstack serve` in a directory without one exits 3 ("spec not found: preview.yml") and this
# container crash-loops behind `restart: unless-stopped` — while `docker compose up -d` still
# returns 0, so `init` would report success over a dead control plane.
#
# It sits at the DEFAULT path rather than being passed with `-f` on purpose: once `serve` stops
# requiring a single spec (the registry supersedes it — see src/api.ts, whose deployments come from
# ${PSTACK_DATA}), this file is simply ignored and nothing in CMD has to change. Deleting it then
# is a one-line cleanup, not a coupling.
#
# It must never describe the control stack: that would hand the API the self-management footgun
# this architecture exists to prevent.
RUN printf '%s\n' \
      '# Inert placeholder — see the Dockerfile. NOT the control stack, and not a deployment.' \
      '# The control plane reads its deployments from the registry at $PSTACK_DATA/deployments.' \
      'version: 1' \
      'stack: pstack-control-placeholder' \
    > /app/preview.yml

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

CMD ["bun", "src/cli.ts", "serve"]
