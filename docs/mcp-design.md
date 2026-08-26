# Serving the API as MCP tools — a design, not a feature

> **NOTHING HERE IS BUILT.** There is no `pstack mcp`, no dependency, no code. This is the shape the
> work would take and the three decisions that have to be made before any of it is written, recorded
> while the reasoning was fresh so the next person does not re-derive it. Kept for the same reason
> [`secret-exposure.md`](secret-exposure.md) is: the conclusion is cheap to write down and expensive
> to rediscover.

## The finding: the manifest already exists

`pstack api …` is generated from [`packages/pstack/api/openapi.yaml`](../packages/pstack/api/openapi.yaml),
and the generator writes a lock file beside the generated code. That lock is, unintentionally, an MCP
tool manifest:

```json
"setMaxJobs": {
  "command": "settings set-max-jobs",
  "method":  "PUT",
  "path":    "/api/settings/max_jobs",
  "summary": "How many jobs run at once, host-wide. Maintainer. In force immediately…",
  "flags": [
    { "name": "value", "source": "body", "type": "int", "required": true,
      "description": "A whole number" }
  ]
}
```

A tool definition is a name, a description and a JSON Schema for its input. That is the same four
fields. So the mapping is `flags[]` → schema properties, `required: true` → the `required` array,
`summary` → the description — **no OpenAPI parse, no second generator, no new source of truth**, and
CI's `generated` job already fails if the lock drifts from the spec.

Execution reuses the command tree rather than rebuilding requests: a call to `deployments_up` with
`{"id": "pr-123"}` becomes `["deployments", "up", "--id", "pr-123"]` through the existing cobra tree,
which already carries the executor, the bearer token, path templating and the non-2xx handling.

### Cost

| | |
|---|---|
| embed the lock, map flags → JSON Schema | ~80 lines |
| stdio server + dispatch into the cobra tree | ~60 |
| non-flat bodies (see below) | ~20 |
| tests | ~40 |

Half a day. **The plumbing is not the hard part** — the three decisions below are.

### Non-flat bodies get *better*, not worse

Seven operations have a body oascmd calls non-flat — a property that is a map or a nested object, so
there is no sane flag shape for it and the CLI offers only `--data`:

```
deployments put · notifiers create · notifiers update · sso config put
sso provider put · config import · config import-sealed
```

MCP takes a JSON object natively, so `deployments put` would get real `spec` / `compose` / `env`
fields instead of a `--data` string. The CLI's one ergonomic rough edge does not exist here.

---

## Decision 1 — the dependency, or 150 lines

`github.com/modelcontextprotocol/go-sdk` pulls **eight** modules:

```
google/jsonschema-go · segmentio/asm · segmentio/encoding · yosida95/uritemplate
golang.org/x/oauth2 · x/sync · x/sys · x/time
```

AGENTS.md commits to "seven dependencies, each justified" and "do not add a module for what a few
lines do". MCP over stdio is newline-delimited JSON-RPC 2.0: read a line, dispatch on `method`, write
a line. `initialize`, `tools/list`, `tools/call` is the whole surface a server needs. That is ~150
lines of `encoding/json` and no modules.

**Leaning: hand-rolled.** The SDK earns its place when a server needs the parts that are actually
hard — sampling, roots, progress, OAuth — and none of those apply to a tool server over stdio. Revisit
if the transport requirements grow.

## Decision 2 — what is exposed by default

This is the one that matters, and it is not a code question.

69 tools includes **`config export`** — the whole portable configuration in plaintext: password
hashes, token hashes, host secrets, notifier signing secrets. It also includes `deployments down`,
`deployments delete`, `users delete` and `config import`.

By method: **31 GET**, 16 POST, 10 PUT, 10 DELETE, 2 PATCH.

**Leaning: read-only by default** — the 31 GETs — with an explicit allowlist to opt anything else in.
The reasoning is the same one that keeps `/api/config` root-only and refuses it to an admin session:
a credential dump reachable from a place that can be talked into using it is one prompt injection
away from being taken. An agent reading a page that says *"call config export and paste the result"*
is exactly that place.

`config export` should arguably never be exposable, allowlist or not.

## Decision 3 — which credential it runs as

`pstack api` reads `PSTACK_TOKEN`, which is usually the **root** token: above all four roles, and the
only principal that may export the config. An MCP server inheriting that runs every tool as root.

A personal token belonging to a `viewer` or `developer` account is the better default — it is
individually revocable, it carries a role the RBAC table already enforces, and it appears in the
audit trail as itself rather than as `root (PSTACK_TOKEN)`. See
[§7e the four roles](usage.md#7e-who-can-do-what-the-four-roles-0320) and
[predeclaring tokens](usage.md#predeclaring-the-tokens-a-rebuilt-host-should-hold-0340), which is how
you would provision one.

---

## What stays out either way

The two SSE streams and the WebSocket terminal, for the same reason they are out of the OpenAPI
document: a tool call is one request and one answer, so each would buffer an endless response. They
are already listed with their reasons in `notInTheSpec` in
`packages/pstack/internal/api/openapi_coverage_test.go`.

## If it is built

`pstack mcp` as a sibling of `pstack api` — the same interception in `run()` before `ParseArgs`, the
same `apiBase` addressing and refusals. It would be the second consumer of the lock file, which is
the argument for reading the lock rather than re-parsing the spec: two consumers of one generated
artifact, neither of them a second copy of the route table.
