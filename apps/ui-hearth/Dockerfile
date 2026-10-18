# The Hearth pstack UI — a static SPA behind nginx.
#
# Built from the REPO ROOT, because the build needs the workspace lockfile:
#
#     docker build -f apps/ui-hearth/Dockerfile -t pstack-ui-hearth:local .
#
# `pstack-ui-hearth:local` is what `pstack init --ui hearth` expects (PSTACK_UI_HEARTH_IMAGE's default).
# Optional, like the advanced UI: the default control stack serves the basic UI from the API container.

# ── build ────────────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1 AS build
WORKDIR /repo

# The lockfile lives at the workspace root, so the install happens there. Every workspace manifest
# is copied so the frozen lockfile matches the tree it was written for.
COPY package.json bun.lock ./
COPY apps/docs/package.json ./apps/docs/package.json
COPY apps/ui/package.json ./apps/ui/package.json
COPY apps/ui-hearth/package.json ./apps/ui-hearth/package.json
COPY packages/client/package.json ./packages/client/package.json
COPY packages/conformance/package.json ./packages/conformance/package.json
COPY packages/pstack/package.json ./packages/pstack/package.json
RUN bun install --frozen-lockfile

# The client resolves through its built dist/, so it builds first.
COPY packages/client ./packages/client
RUN cd packages/client && bun run build

COPY apps/ui-hearth ./apps/ui-hearth
WORKDIR /repo/apps/ui-hearth
# `vue-tsc --build && vite build` — a type error fails the image rather than shipping a broken SPA.
RUN bun run build

# ── serve ────────────────────────────────────────────────────────────────────────────────────
FROM nginx:1.27-alpine

# Replace the default site: no SPA fallback and no /api/ proxy otherwise.
COPY apps/ui-hearth/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/ui-hearth/dist /usr/share/nginx/html

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1/index.html >/dev/null || exit 1
