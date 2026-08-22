# The Go port — record and status

pstack was a Bun/TypeScript package through 0.28.0 and is a Go binary from 0.29.0. This page is
the record of how the port was graded, and the home of the deviations that survive it.

## How it was graded

The port was a re-implementation against a **fixed contract**, not a redesign: the same registry
files, the same SQLite database, the same HTTP responses, the same CLI transcripts, the same docker
argv. Three instruments, all of which remain in the repo:

| Instrument | What it proved | Where |
|---|---|---|
| **Conformance suite** — 214 black-box tests that spawn the binary | every route group, 80 exact CLI transcripts (the help text, `init` across all eight challenge × UI × orchestrator cells, the control compose each rendered, six cloud-init distros, the upgrade plans), and a complete host fixture produced by the 0.28.0 reference that the binary opens unchanged (every login, every session, every route's bytes) | `packages/conformance/test`, `golden/` |
| **Vacuity gate** — every test must fail against a server answering `200 {}` | that no test passes by asserting nothing | `bun run vacuity` |
| **Differential runner** — nine API scenarios replayed on the reference and the port over one data path, traces compared after masking, the docker argv as a pseudo-step | byte-identical responses and identical commands, modulo the deviations below | `bun run diff` (now binary-vs-binary) |

Inside the binary, every package's tests are graded against `golden/facts` — the JavaScript
semantics measured once on Bun 1.3.12 (the YAML 1.2-core dialect, `Number()`, `.length` in UTF-16,
`JSON.stringify` number formatting, argon2 parameters) — and every test function names the
mutation that fails it (`// negative control:`). The Go rules every package follows are in
`AGENTS.md`.

Final state before cutover: every conformance file complete in Go mode, `bun run diff` (bun vs go)
identical across all nine scenarios with three documented wording deviations, `--self` clean in
both runtimes.

## Deviations that survive

Deliberate, each with its reason. None is observable by the UI or the client SDK.

| Where | 0.28.0 | 0.29.0 | Why |
|---|---|---|---|
| A malformed `%` escape in a request path | `500 {"error":"URI malformed"}` | `400` text/plain | Go's HTTP server rejects it before the handler runs |
| `PUT /api/routing/:name` with unparsable YAML | `not valid YAML: YAML Parse error: …` | `not valid YAML: [line:col] …` | the parser's own wording; only the prefix is contract |
| A notifier delivery that cannot connect (stored error, test result) | `Unable to connect. Is the computer able to access the url?` | `dial tcp …: connection refused` | the runtime's sentence; the URL inside it is redacted either way |
| Ordering of routing files and registry hosts | ICU `localeCompare` | byte order | no ICU in a static binary |
| `PUT`/`DELETE` deployment vs a lifecycle `POST` | a small check-then-act window | held under the stack's job lock → the ordinary 409 | strictly tighter |
| The terminal WebSocket | no Origin check | no Origin check | parity — a separate change if wanted |

## What stays TypeScript

`@samyx/preview-stacks-client` (the SDK) and `@samyx/preview-stacks-ui` (the advanced SPA) — both
talk to the binary over HTTP only. The SDK's test drives the spawned binary in CI
(`packages/client/test`); the SPA's proxy path (login, `/api/auth/me`, the deployments list, a job's
SSE stream through the Vite dev proxy) was exercised against the binary before 0.29.0; a click-through
of the advanced UI against a real Docker host is part of the release rehearsal, not CI.
