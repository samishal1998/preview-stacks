# pstack — the control-plane image (HTTP API + web UI in one container), from a checkout.
#
#     docker build -t pstack:local .          # `pstack:local` is PSTACK_IMAGE's default
#
# This is the contributor's and CI's path. An INSTALLED pstack builds the same image from itself
# (`pstack build-image` copies the running binary into the context) — that is what a host runs, and
# what `pstack dockerfile` prints. The two must agree on the runtime layer below; the generated one
# is in packages/pstack/internal/image/image.go.

# The Docker CLI + Compose plugin, lifted from the official image rather than installed from apt:
# ~50 MB instead of ~200 MB and no key/repo dance. pstack drives `docker compose` by shelling out,
# so without these the API starts fine and every operation fails.
FROM docker:28-cli AS docker-cli

# ── build: source → one static binary ────────────────────────────────────────────────────────
FROM golang:1.23-bookworm AS build
WORKDIR /build
# Modules first, so a source edit does not re-download them.
COPY go.mod go.sum ./
RUN go mod download
COPY packages/pstack ./packages/pstack
# CGO_ENABLED=0: the sqlite driver is pure Go, so the binary is static and runs on any libc — or
# none. -trimpath -s -w: no build paths, no DWARF; the version is read from the embedded
# package.json (version.Get), so no -X is needed here.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /pstack ./packages/pstack/cmd/pstack

# ── runtime ──────────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# bash: axis hooks are `bash -c`. ca-certificates + curl: ACME, webhooks, the installer.
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
# Compose is a CLI *plugin*: `docker compose` resolves `docker-compose` out of the plugin
# directory, so copying only the binary yields "docker: 'compose' is not a docker command".
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose \
                       /usr/local/libexec/docker/cli-plugins/docker-compose

# Assert it at BUILD time: client-side, needs no daemon, and turns a moved upstream path into an
# obvious build failure instead of a runtime one on the host.
RUN docker --version && docker compose version

COPY --from=build /pstack /usr/local/bin/pstack
RUN /usr/local/bin/pstack --version

# Non-root by default: this image is also useful as a plain CLI (`validate`, `--dry-run`), where
# root buys nothing. The control stack deliberately overrides it with `user: "0:0"` because it
# mounts the Docker socket read-write — see templates/control/docker-compose.yml.
RUN useradd --uid 1000 --create-home --shell /bin/bash pstack
WORKDIR /app
USER pstack

EXPOSE 7878

# Liveness straight from the API's own route, via the CLI's own `healthcheck` command — so it depends
# on nothing the base image might drop and reads PSTACK_PORT like the server does. `init` waits for
# this to report `healthy` before claiming the control stack is up.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["pstack", "healthcheck"]

CMD ["pstack", "serve"]
