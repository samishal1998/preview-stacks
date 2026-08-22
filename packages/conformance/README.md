# Conformance — the black-box specification of pstack

Every test here drives a **spawned** `pstack` — the real CLI, the real `serve` — over HTTP, the
filesystem and a fake `docker` on PATH. Nothing imports the implementation. That is the point:
this suite grades ANY binary that answers the same contract. It graded the Go port against the
TypeScript reference until the two were byte-identical (`docs/port-status.md`); the reference is
gone and these transcripts ARE the contract now.

Three rules, enforced mechanically:

1. **Nothing imports the implementation.** A server or CLI is obtained only through `harness/`.
2. **Every server and CLI is spawned** (`harness/server.ts`, `harness/cli.ts`), selected by
   `PSTACK_IMPL=go|null` (default `go`). `go` runs the binary at `$PSTACK_BIN` (default
   `packages/pstack/bin/pstack`); `null` is a server that answers `200 {}` to everything.
3. **Every test must fail against `PSTACK_IMPL=null`.** `bun run vacuity` proves it: a test that
   passes against a server that asserts nothing is not a test.

Layout:

| Dir | What |
|---|---|
| `harness/` | spawn + discover, docker shims (`printf`, never `echo`), webhook receivers, the fake IdP, the null server |
| `test/` | the suite, one file per route group, runnable in any mode |
| `gen/` | regenerates `golden/` from the binary (`bun run gen`) — after a DELIBERATE contract change, reviewed with the code |
| `golden/` | checked in: `cli/` exact CLI transcripts, `render/` generated documents, `facts/` the JavaScript semantics the port reproduces (measured once on Bun 1.3.12), `host/` a complete data dir |
| `diff/` | differential mode: the same scenario on binary A then B (a release vs the working tree), traces compared after masking |
| `scripts/` | `vacuity`, `ratchet` (pass counts only go up), `status` (the file → package matrix) |

Run: `bun test` · `bun run vacuity` · `bun run diff -- --self` · `bun run diff -- --a /usr/local/bin/pstack`.
