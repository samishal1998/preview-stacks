# Node signals — build plan

**What we are building:** `GET /api/signals` (what the swarm looks like right now), two events when
that changes, and three routes that drain, undrain and remove a node. Nothing else.

**Spec:** [node-signals-design.md](node-signals-design.md).

**Five tasks.** Each ends with tests passing and a commit. Roughly 250 lines of Go in total.

## Rules that apply to every task

- **Swarm only.** On a single-machine host the route answers `"swarm": false` with empty lists, and
  the three action routes answer 409, the way `GET /api/swarm/join` already does.
- **Nothing is stored.** The only thing kept in memory is "when did I first see this node empty",
  and losing it on restart is fine.
- Docker is run through `exec.Runner` command strings, like the rest of the codebase. No docker SDK,
  no new dependency.
- JSON responses are Go structs or `jsonx.Object`, never `map[string]any`, and every list is empty
  rather than `null`.
- Every test gets a `// negative control:` comment saying what change would make it fail, and that
  change gets tried once.
- Commit through GitButler with explicit change ids. No `Co-Authored-By` or similar footers. Never
  commit `packages/conformance/golden/host/db/pstack.db-{shm,wal}`.

---

## Task 1 — Ask docker what the swarm looks like

**Files:** create `packages/pstack/internal/signals/signals.go` and `signals_test.go`, plus
`testdata/` fixtures.

Three docker commands, run in order. Stop early if one fails.

```
docker node ls --format '{{json .}}'
docker service ls --format '{{json .}}'
docker service ps --no-trunc --filter desired-state=running --format '{{json .}}' <ids…>
```

Details that will otherwise waste an afternoon, all checked against docker 28:

- `--no-trunc` is required. Without it docker cuts the error message at 30 characters, so every
  reason reads `no suitable node (insufficie…`.
- The `Error` field arrives wrapped in literal double quotes. Strip them.
- A task's `Node` field is the machine's **hostname**, not its id, and it is empty while the task is
  unplaced. Join tasks to nodes on hostname.

```go
type Node struct {
	ID, Hostname, Role, State, Availability string
	Tasks int
}

type Stuck struct {
	Task, Service, Stack, Reason string
}

type View struct {
	Swarm, Reachable bool
	Nodes []Node
	Stuck []Stuck
}

func Look(r exec.Runner) View
```

- A task counts towards `Node.Tasks` when it is running on that hostname. Tasks of global services
  (one per machine, like a log collector) do not count — they follow the machine and would make
  every node look busy forever. `docker service ls --format '{{json .}}'` reports each service's
  mode, which is how you tell.
- A task is `Stuck` when its current state is `pending` and its error starts with `no suitable
  node`. `Stack` comes from the service name, which is `<stack>_<service>` under swarm.
- Docker not answering, or this host not being a manager, is `View{Swarm: false}` — not an error.

**Tests** use `exec.NewFake` with an `Answer` function keyed by the exact command string, the way
`internal/swarm/swarm_test.go` already does. Fixtures: one manager, two workers, one running task,
one stuck task with a quoted error, one global service.

Commit: `feat(signals): read the swarm's shape from docker`.

---

## Task 2 — The route

**Files:** create `packages/pstack/internal/api/routes_signals.go` and its test; edit `routes.go`,
`permissions.go`, `permissions_test.go` and `packages/pstack/api/openapi.yaml`.

`GET /api/signals` returns the JSON in the spec. Viewers and above can read it.

- Dispatch goes in `routes.go` itself, in the swarm block — a test scans that file for routes and
  refuses anything it cannot find there. The handler lives in the new file.
- `emptySince` comes from a small map on the server: node id → the first time it was seen with no
  tasks, cleared when it gets one. Guard it with a mutex.
- Add the path to `openapi.yaml` under the existing `swarm` tag (a new tag needs a description
  elsewhere and fails a test without one), then run
  `go generate ./packages/pstack/internal/apicli` and set `OperationCount` to whatever the
  regenerated lock file says.

**Test** builds a server with a fake docker and compares the whole response body.

Commit: `feat(api): GET /api/signals`.

---

## Task 3 — Tell people when it changes

**Files:** edit `packages/pstack/internal/api/server.go`, `internal/events/events.go` (+ test),
`internal/notify/notify.go` (+ test).

A ticker every 30 seconds: look, compare with what was true last time, send `signal.raised` for
anything new and `signal.cleared` for anything gone. Ids are `empty/<node id>` and
`stuck/<service>`.

- Add `"signal.raised"` and `"signal.cleared"` to the **end** of the event list — the order is part
  of the contract — and to the list the test pins.
- Add two lines to `notify.Summarize` so a Slack message reads like a sentence.
- Start the ticker next to the sleep scheduler in `Server.Start`, stop it in `Server.Stop`.
- The interval comes from `PSTACK_SIGNALS_TICK_MS`. **Zero turns it off**, and the default inside
  the api package is zero, so only `serve` turns it on. Read it with `os.LookupEnv` so an explicit
  `0` is not mistaken for "unset". Without this, every test server in the conformance suite would
  start shelling out to docker every 30 seconds.
- Never send an event while holding the lock: the event bus calls its listeners immediately, and one
  of them reads the database.
- If docker does not answer, change nothing. Silence is not "the problem went away".

**Tests:** a raise fires once, a clear fires once, an unreadable tick changes nothing.

Commit: `feat(signals): raise and clear events when the swarm changes`.

---

## Task 4 — Drain, undrain, remove

**Files:** edit `internal/swarm/swarm.go` (+ test), `internal/api/routes_signals.go`, `routes.go`,
`permissions.go`, `permissions_test.go`, `openapi.yaml`, apicli regenerated.

Two command builders beside the existing ones:

```go
func NodeAvailabilityCmd(id, availability string) string  // docker node update --availability 'drain' 'xk3f…'
func NodeRmCmd(id string) string                          // docker node rm 'xk3f…'
```

Routes, maintainer and above:

| Route | Behaviour |
|---|---|
| `POST /api/swarm/nodes/:id/drain` | run it, 200; already drained is also 200 |
| `POST /api/swarm/nodes/:id/undrain` | run it, 200 |
| `DELETE /api/swarm/nodes/:id` | 409 unless docker reports the node `down` **and** drained; then remove it, 200. Never `--force` |

The `down` check matters: a node that briefly loses the network reads as "not ready" while still
running everything it had. Removing it then orphans that work.

Write the path regex exactly like this, because a test expands this shape into the three OpenAPI
paths and silently collapses anything else:

```go
swarmNodeRe = regexp.MustCompile(`^/api/swarm/nodes/([^/]+)(?:/(drain|undrain))?$`)
```

Add it to the matcher list in `permissions_test.go`, add the permission rows, add the three paths to
`openapi.yaml`, regenerate apicli.

Commit: `feat(api): drain, undrain and remove a swarm node`.

---

## Task 5 — Tests through the real binary, and the docs

**Files:** create `packages/conformance/test/api-signals.test.ts`; edit
`packages/conformance/test/api-rbac.test.ts`, `expected-pass.json`, `docs/webhook-events.md`,
`docs/usage.md`, `docs/README.md`, `packages/pstack/CHANGELOG.md`.

Conformance tests, against a fake `docker` script on `PATH`:

1. Single-machine host → `"swarm": false`, empty lists.
2. Multi-machine host → the nodes, the stuck task, the empty worker.
3. Drain → 200, and `node update --availability drain n2` shows up in the recorded calls. (The fake
   records the command without the word `docker` and without quotes — match that.)
4. Delete a live node → 409. Then make the fake report it `down` and drained → 200.
5. An event reaches a webhook listener once.

Do **not** add these docker answers to the shared `SWARM_SHIM`: a shell `case` takes the first match,
and another suite already answers `node inspect` there. Make a separate one and combine the two in
this test only.

Docs, in plain language:
- `webhook-events.md`: the two events and their fields.
- `usage.md`: what the two answers mean and the drain → delete → remove sequence, with the caveats
  from the spec (worker disks, "empty" only counting swarm tasks).
- `README.md` index row, `CHANGELOG.md` entry.

Then run the whole gate: `bun run check` at the root, and in `packages/conformance`
`bun test && bun run vacuity && bun run ratchet && bun run diff -- --self`.

Commit: `docs: node signals`.

---

## What was dropped from the first version of this plan, and why

Kept here so nobody re-adds it by accident:

| Dropped | Why |
|---|---|
| Working out whether a sleeping stack would still fit somewhere | Swarm only counts reservations that people almost never write, so the sums were mostly zeros. The machine manager already decides how much spare capacity to keep. |
| A host default for CPU/memory reservations | It existed only to make those sums mean something. |
| Pinning stacks to workers automatically | A fair idea, but a separate change: it moves where every preview runs. |
| `init` flags, `.env` keys, a control-template change, upgrade read-back | All of it existed only to carry the two settings above. |
| Grouping stuck tasks by placement rules, and a vocabulary of failure kinds | The caller reads docker's sentence and decides. |
| Re-checking a node inside the drain route | Draining a busy node is safe — swarm moves the work. The caller re-reads `/api/signals` afterwards. |
