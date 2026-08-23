# The advanced pstack UI — a static SPA behind nginx.
#
# Built from the REPO ROOT, because the build needs the workspace lockfile:
#
#     docker build -f apps/ui/Dockerfile -t pstack-ui:local .
#
# `pstack-ui:local` is what `pstack init --ui advanced` expects (PSTACK_UI_IMAGE's default).
#
# This image is entirely optional. The default control stack serves the basic UI from inside the API
# container, so a host that never runs `--ui advanced` never builds or pulls this.

# ── build ────────────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1 AS build
WORKDIR /repo

# The lockfile lives at the workspace root, so the install must happen there — installing from
# apps/ui alone would resolve a different tree than CI tested. `--frozen-lockfile` makes that a
# build failure rather than a silent difference.
COPY package.json bun.lock ./
COPY apps/ui/package.json ./apps/ui/package.json
RUN bun install --frozen-lockfile

COPY apps/ui ./apps/ui
WORKDIR /repo/apps/ui
# `vue-tsc --build && vite build` — a type error fails the image rather than shipping a broken SPA.
RUN bun run build

# ── serve ────────────────────────────────────────────────────────────────────────────────────
FROM nginx:1.27-alpine

# Replace the default site: the stock one has no SPA fallback and no /api/ proxy, so deep links
# would 404 and every API call would leave the origin.
COPY apps/ui/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/ui/dist /usr/share/nginx/html

EXPOSE 80

# Static files plus a proxy — if nginx answers for the entry document at all, the container is
# doing its job. The API's own health is a separate concern, reported by the pstack container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1/index.html >/dev/null || exit 1
