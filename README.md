# preview-stacks

A monorepo. The product is **[`packages/pstack`](packages/pstack)** — a control plane for ephemeral
per-PR preview stacks, published as [`@samyx/preview-stacks`](https://www.npmjs.com/package/@samyx/preview-stacks).
Its README is the design document; start there.

```
packages/pstack     the CLI, HTTP API, and the basic embedded UI   (published to npm)
apps/ui             the optional advanced web UI                   (opt-in, its own container)
docs/               guides that span both — bootstrap, usage, control-plane architecture
skills/pstack/      a skill teaching an agent to use pstack
```

## Working in it

Turborepo drives every task, so a command at the root fans out across packages and caches results.

```bash
bun install
bun run build        # turbo run build
bun run test         # turbo run test
bun run typecheck
bun run check        # build + test + typecheck, per package
```

Run a single package's task directly when iterating:

```bash
cd packages/pstack && bun test
```

## Releasing

`publish.config.ts` at the root selects `@samyx/preview-stacks` **by name** — a root-level package
cannot be selected by path glob, since its relative directory is the empty string.

```bash
bun run release:dry       # transform + pack, publish nothing
bun run release:publish   # requires an npm OTP
```

## Docs

- [Usage](docs/usage.md) — task-oriented guide
- [Bootstrap](docs/bootstrap.md) — build a host from nothing (Hetzner + cloud-init)
- [Control plane](docs/control-plane.md) — architecture, registry contract, trust boundary
- [Secret exposure](docs/secret-exposure.md) — **open, undecided**: unauthenticated job reads
  publish captured credentials
- [AGENTS.md](AGENTS.md) — working on this codebase
