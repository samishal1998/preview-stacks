# Conformance — the black-box specification of pstack

Every test here drives a **spawned** `pstack` — the real CLI, the real `serve` — over HTTP, the
filesystem and a fake `docker` on PATH. Nothing imports `packages/pstack/src`. That is the point:
this suite grades ANY implementation that answers the same contract, and it is what the Go port is
measured against while the TypeScript reference still exists and what defines the contract once it
does not.

Three rules, enforced mechanically:

1. **No import from `packages/pstack/src`.** A server or CLI is obtained only through `harness/`.
2. **Every server and CLI is spawned** (`harness/server.ts`, `harness/cli.ts`), selected by
   `PSTACK_IMPL=bun|go|null`. `bun` runs `packages/pstack/src/cli.ts`; `go` runs the binary at
   `$PSTACK_BIN` (default `packages/pstack/bin/pstack`); `null` is a server that answers `200 {}`
   to everything.
3. **Every test must fail against `PSTACK_IMPL=null`.** `bun run vacuity` proves it: a test that
   passes against a server that asserts nothing is not a test.

Layout:

| Dir | What |
|---|---|
| `harness/` | spawn + discover, docker shims (`printf`, never `echo`), webhook receivers, the fake IdP, the null server |
| `test/` | the suite, one file per route group, runnable in any mode |
| `gen/` | produces `golden/` from the Bun reference (`bun run gen`) — run only while the TS source exists |
| `golden/` | checked in: `cli/` exact CLI transcripts, `render/` generated documents, `facts/` measured Bun semantics, `host/` a complete data dir |
| `diff/` | differential mode: the same scenario on impl A then B, traces compared after masking |
| `scripts/` | `vacuity`, `ratchet` (go-mode pass counts only go up), `status` (the port matrix) |

Run: `PSTACK_IMPL=bun bun test` · `PSTACK_IMPL=go bun test` · `bun run vacuity` · `bun run diff -- --self`.
