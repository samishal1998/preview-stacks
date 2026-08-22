# preview-stacks

A monorepo. The product is **[`packages/pstack`](packages/pstack)** — a control plane for ephemeral
per-PR preview stacks, released as one static Go binary on
[GitHub Releases](https://github.com/samishal1998/preview-stacks/releases). Its README is the
design document; start there.

```
packages/pstack        the CLI, HTTP API, and the basic embedded UI   (Go; GitHub Releases)
packages/conformance   the black-box specification: goldens + tests that grade the binary
packages/client        the TypeScript API client                       (npm: @samyx/preview-stacks-client)
apps/ui                the optional advanced web UI                   (npm: @samyx/preview-stacks-ui; its own container)
docs/                  guides that span all of it — bootstrap, usage, control-plane architecture
skills/pstack/         a skill teaching an agent to use pstack
```

## Working in it

Turborepo drives every task, so a command at the root fans out across packages and caches results
— `go build`/`go test -race`/`go vet` for pstack, bun for the rest.

```bash
bun install
bun run build        # turbo run build   (packages/pstack/bin/pstack, the UI and client bundles)
bun run test         # turbo run test
bun run typecheck
bun run check        # build + test + typecheck, per package
```

Run a single package's task directly when iterating:

```bash
go test -race ./packages/pstack/internal/spec/     # one Go package
cd packages/conformance && bun test                 # the black-box suite against bin/pstack
```

## Releasing

A `v*` tag releases everything at that lockstep version: `.github/workflows/release.yml` asserts
the tag equals the three `package.json` versions, runs GoReleaser (`.goreleaser.yaml` — the binaries,
`checksums.txt`, `install.sh`), smoke-tests the published installer, then publishes the two npm
packages with publish-kit (`publish.config.ts` selects them by name).

```bash
bunx publish-kit bump minor     # the two npm packages; set packages/pstack/package.json (the version of
                                # record, what the binary reports) to the same number in the same edit
git tag v0.29.0 && git push --tags
```

The release workflow refuses a tag whose number differs from any of the three `package.json`s,
so a missed bump fails before anything is published.

## Docs

**[docs/README.md](docs/README.md) is the index** — what each document answers, and which to read
first. The short version:

- [Usage](docs/usage.md) — task-oriented guide
- [Bootstrap](docs/bootstrap.md) — build a host from nothing (Hetzner + cloud-init)
- [Control plane](docs/control-plane.md) — architecture, registry contract, trust boundary
- [Webhook events](docs/webhook-events.md) — every event a notifier receives: payloads, signature verification, delivery semantics
- [API client](packages/client/README.md) — `@samyx/preview-stacks-client`: typed calls, `waitForJob` / `waitForReady`, and webhook verification for your receiver
- [UI rules](docs/ui-rules.md) — the conventions the advanced UI is held to: casing, alignment, spacing, roundness, width
- [Secret exposure](docs/secret-exposure.md) — resolved in 0.10.0 (every route authenticated); kept as the design record
- [AGENTS.md](AGENTS.md) — **start here if you are an AI agent or new to the code**: invariants,
  repo map, testing expectations, scope discipline
