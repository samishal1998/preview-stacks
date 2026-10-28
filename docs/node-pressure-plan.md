# Node pressure: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a reporter on every node, pressure guards in the control plane (alert, prune, auto-sleep, refuse), log caps, and a Stats page in both UIs.

**Architecture:** `pstack-agent` is a POSIX shell script in `docker:29-cli`, printing one batch per 30 s to stdout. `statsLoop` in the API reads its logs, keeps the newest complete batch per node and a 120-sample ring in memory, evaluates pressure with hysteresis, and acts on edges: events, prunes (swarm job services or the host runner), one auto-sleep per tick, and refusal of `up`/`wake` inside `startLifecycle`'s `work`. The pure parts (parse, store, pressure, command strings) live in a new leaf package `internal/nodestats`.

**Tech Stack:** Go 1.23 (stdlib only), POSIX sh + awk, bun:test conformance, Vue 3 (apps/ui, apps/ui-hearth with @hearth-ui/vue 0.8.0).

**Spec:** docs/node-pressure-design.md (binding, owner-approved 2026-10-28).

**Branch:** `claude/node-pressure`, GitButler short id `cl` (two stacks are applied, so `-b` takes the id, not the name).

## Global Constraints

- The spec binds. Where this plan and the spec disagree, follow the spec and say so in the report. Line numbers here are hints; the tree wins.
- Read AGENTS.md (Invariants, Go rules, "How to add things") and the header comment of every file you touch. Change a header when you change the decision it records.
- Every shell runs `export GOROOT=/usr/lib/go-1.23` before any `go` command. Go tests that must not reach a real docker run with `DOCKER_HOST=unix:///nonexistent`. `chmod` and `cp` are aliased on this machine: use `command chmod` / `command cp` (a non-executable fake docker once let real docker run).
- Go: `go vet ./packages/pstack/...` clean, `go test -race -timeout 120s ./packages/pstack/...` green. Every `Test…`/`t.Run` carries a `// negative control: <mutation>` line, and the mutation was tried once.
- Go rules that bite here: JSON is structs or `jsonx`, never `map[string]any` (1); tri-states/unknowns are pointers WITHOUT omitempty, so unknown is `null`, never `0`/`false` (2, invariant 11); every slice in a response or payload is non-nil (3); never range a Go map into output (5); `sort.SliceStable` (6); `js.NumberString` for numbers in text (7); every shared field names its mutex; **no Emit, sink or `startLifecycle` call while holding a mutex** (14); every new goroutine has `recover` (14).
- Invariants: stats, pressure and prune state live in memory only, never a table (10). Unknown is never pressure: no guard fires on a stale or missing node (11). The agent is created by `init` (and so `upgrade`, `pstack ui`, `pstack logging`) only, never by the API (12). Events are appended at the END of `events.Names` (14). No new `#__MARKER__` in the control template (20).
- `PSTACK_AGENT_IMAGE` and `PSTACK_STATS_TICK_MS` are env-only (rule 18's serve-knob carve-out). `PSTACK_STATS_TICK_MS` copies `PSTACK_SIGNALS_TICK_MS` exactly: api default 0 (off), `serve` default 30000, explicit `0` is off.
- Spec values, verbatim: agent image `docker:29-cli`; batch every 30 s; logs `--since 90s`; stale after 120000 ms; ring 120 samples; settings `pressure_disk_pct` 90, `pressure_mem_pct` 10, `pressure_cpu_load` 2.0, `pressure_sleep_idle_min` 30, `image_prune_after_h` 168, each `0` = off; recovery margin 5 points (disk, memory), 0.25 (cpu); emergency prune ≤ 1 per node per 10 min; scheduled prune every 6 h; Traefik log cap `max-size: 10m`/`max-file: "2"`, other control services `50m`/`"3"`, previews `10m`/`"3"` when Loki is off.
- The control-plane node ("self": `swarm.SwarmInfo(...).NodeID` on swarm, the only node on compose) is always served by a local container and the host runner, never by a swarm task. A drained manager (`--previews-on workers`) runs no swarm tasks, global or job, and it is the node whose disk caused the incident.
- New API surface follows AGENTS.md "A new API route": literal `if path == "/api/…"` arm in `routes.go`, a `permissions.go` row, `packages/pstack/api/openapi.yaml`, `go generate ./packages/pstack/internal/apicli`, bump `apicli.OperationCount` to the lock file's count (91 today), a conformance test that fails against `PSTACK_IMPL=null`, `expected-pass.json` raised, `api-rbac` rows. No `port-map.json` change (recent routes added none).
- Goldens: build the binary first (`go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`), then `cd packages/conformance && bun gen/goldens.ts`. Never `bun run gen`. Read the golden diff and name every changed file in the report.
- Conformance: `cd packages/conformance && bun test` green, `bun run vacuity` clean, `bun run ratchet` passes. A docker answer goes in a per-feature shim constant (`STATS_SHIM`), never added to `SWARM_SHIM`. Shim arms use `printf '%s\n'`, never `echo`.
- UIs: `bun run --cwd apps/ui-hearth build && bun test --cwd apps/ui-hearth`; the same for `apps/ui`. Copy is terse: state, not explanation. No `window.confirm`. Advanced UI follows docs/ui-rules.md.
- VCS writes through `but` only. Read ids with `but status` in its own command, then commit in a separate command: `but commit -b cl -m "type(scope): summary" <id> <id> …`, explicit ids only, no attribution footers. Never commit `.dns_token`, `hetzner_42.yaml`, anything under `.superpowers/`, `packages/conformance/golden/host/**` (including `db/pstack.db`, `db/pstack.db-shm`, `db/pstack.db-wal`), `packages/conformance/gen/host-fixture.ts`, `bun.lock`, credentials. Never push or open PRs.

## Review Focus

1. **A batch cut by the `--since 90s` window or by a dying agent** (a `node` line without its `end`, or an `end` without its `node`): it is discarded, and the node keeps its previous complete batch. Test in Task 4.
2. **An agent restarted inside the window** resets `seq` to 1, so "highest seq" would pick the dead agent's batch: the newest batch is the last complete one in stream order. Test in Task 4.
3. **A threshold set to `0` while a node is in pressure for that kind**: the kind drops, and `node.recovered` fires once if no kind is left. A node going stale while in pressure fires nothing ("silence is not recovery"). Tests in Task 7.
4. **A prune that never finishes, fails, or frees `0B`**: a 10-minute deadline; the job service is removed on every path; a failed run emits nothing; `0B` emits `freedBytes: 0`. Tests in Task 8.
5. **A host with no agent, or every node stale**: `/api/stats` answers `agent: false` with `null` values, and deploys, wakes, sleeps and prunes behave exactly as today. Tests in Tasks 6 and 9.

---

### Task 1: Log caps for the control stack and for previews

**Files:**
- Modify: `packages/pstack/templates/control/docker-compose.yml` (traefik and pstack service blocks)
- Modify: `packages/pstack/internal/initctl/init.go` (`AdvancedUIService`, `LokiService`, `GrafanaService`)
- Modify: `packages/pstack/internal/autolabel/autolabel.go` (`MaterializeCompose`, new `CapLogs`), `autolabel_test.go`, `initctl/init_test.go`
- Modify: `packages/conformance/test/api-logging.test.ts` (and any suite whose body moves), regenerated `packages/conformance/golden/render/*` and `golden/cli/init-*`, `logging-*`, `ui-switch-*`
- Commit also: `docs/node-pressure-design.md`, `docs/node-pressure-plan.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `func CapLogs(doc *omap.Map) (out *omap.Map, capped []string)` in `autolabel`, the json-file twin of `InjectLogging`: clone, skip any service that `Has("logging")`, otherwise set `logging: {driver: json-file, options: {max-size: "10m", max-file: "3"}}`; `capped` in file order, non-nil.

- [ ] **Step 1: Failing tests.** `TestCapLogs` (web gets the block, db with `logging: {driver: json-file}` is untouched and absent from `capped`, input doc unchanged). `TestMaterializeCapsLogsWhenLokiIsOff` (DetectLogging pinned to `""`, a compose file with no `pstack.routing.*`: result `File == GeneratedCompose`, the written file carries the cap). `TestControlServicesCapTheirLogs` in `init_test.go`: every service in the rendered template (all `UI` × logging cells) has `logging:`; traefik's is `10m`/`"2"`, the rest `50m`/`"3"`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/autolabel/ ./packages/pstack/internal/initctl/`, expect FAIL.
- [ ] **Step 3: Implement.** Template: a `logging:` block in traefik (after `oom_score_adj`, before `ports:`, so `LokiWiring`'s `"    networks: [preview-ingress]\n"` anchor and `traefikBlock` still match) and in pstack; a literal edit of existing service blocks, not a marker. The three Go blocks append the 50m/"3" block as lines. `MaterializeCompose`: delete the `!wantsRouting && !isSwarm && pushURL == ""` early return; when `pushURL == ""` run `CapLogs` where `InjectLogging` runs; the "nothing generated" return also requires `len(capped) == 0`. No new `MaterializeResult` field. Dry-run is unaffected (`compose.filesFor` never materializes under `-n`).
- [ ] **Step 4: Run** the Go tests (PASS), rebuild the binary, `bun gen/goldens.ts`. Expect every render golden and every `init`/`logging-*`/`ui-switch-*` transcript whose `write …/docker-compose.yml (N bytes` line moves to change, and the `up-dry-*` goldens NOT to change. Update `api-logging.test.ts` (and any other body that now has a json-file block); `bun test` green.
- [ ] **Step 5: Commit** `feat(control): cap control and preview container logs` with the code, tests, goldens, the design doc and this plan.

### Task 2: Five pressure settings

**Files:**
- Modify: `packages/pstack/internal/settings/settings.go` (+ `_test.go`), `internal/api/routes_settings.go`, `routes.go`, `permissions.go`, `permissions_test.go`, `packages/pstack/api/openapi.yaml`, `internal/apicli/*` (regenerated), `apicli.go` (`OperationCount` 91 → 96)
- Modify: `packages/client/src/types.ts` (`SettingKey`), `packages/conformance/test/api-settings.test.ts`, `api-rbac.test.ts`, `expected-pass.json`

**Interfaces:**
- Produces, in `settings`:
  - `const KeyPressureDiskPct = "pressure_disk_pct"`, `KeyPressureMemPct = "pressure_mem_pct"`, `KeyPressureCPULoad = "pressure_cpu_load"`, `KeyPressureSleepIdleMin = "pressure_sleep_idle_min"`, `KeyImagePruneAfterH = "image_prune_after_h"`.
  - `func (s *Settings) PressureDiskPct() int` (default 90), `PressureMemPct() int` (10), `PressureCPULoad() float64` (2.0), `PressureSleepIdleMin() int` (30), `ImagePruneAfterH() int` (168). Readers never error: a missing or unparsable row is the default. No env layer.
- `Set`: disk and mem are integers 0–100; cpu a finite number ≥ 0 (stored `strconv.FormatFloat(v, 'f', -1, 64)`, so `2.0` stores `"2"`); idle min and prune hours integers ≥ 0. Each refusal is a `*Error` naming the key.
- Routes: `PUT /api/settings/<key>` for each, through the existing `putSetting`, dispatched by five literal `path ==` arms (the coverage and permissions tests read literals). Permission rows `mPut` at `auth.Admin`. `settingRow` gains the five keys with source `db`/`default`; `getSettings` lists seven rows, the five after `default_role`.
- openapi: five paths in the `settings` tag, operationIds `setPressureDiskPct`, `setPressureMemPct`, `setPressureCpuLoad`, `setPressureSleepIdleMin`, `setImagePruneAfterH`, `value` `integer` for four keys and `number` for `pressure_cpu_load`, the range in `description`; update the tag's "two settings" description.
- Client: `SettingKey` adds the five; the `value` doc comment names them.

- [ ] **Step 1: Failing tests.** `TestPressureSettingsDefaults` (fresh store: 90, 10, 2, 30, 168). `TestPressureSettingsSetAndRefuse` table: `pressure_disk_pct` "0" ok / "101" refused / "9.5" refused; `pressure_cpu_load` "2.5" reads 2.5 / "-1" refused; `image_prune_after_h` "0" ok. Permissions table rows at admin. Conformance: a maintainer's PUT is 403, an admin's PUT `{value: 85}` answers the row with `source: "db"`, and GET lists all seven keys in order (whole body).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/settings/ ./packages/pstack/internal/api/` (FAIL).
- [ ] **Step 3: Implement**, then `go generate ./packages/pstack/internal/apicli` and set `OperationCount` to the lock file's count (96).
- [ ] **Step 4: Run** Go tests, rebuild, `bun gen/goldens.ts` (read any moved golden), `bun test` in conformance and `bun test --cwd packages/client`.
- [ ] **Step 5: Commit** `feat(settings): pressure thresholds and image prune age`.

### Task 3: The agent script

**Files:**
- Create: `packages/pstack/templates/agent/agent.sh`, `packages/pstack/agent_test.go`
- Create: `packages/pstack/templates/agent/testdata/proc/meminfo`, `proc/loadavg`, `bin/docker`, `bin/df`, `bin/nproc`, `agent-output.txt`
- Modify: `packages/pstack/assets.go` (ninth embed; fix the "eight files" header)

**Interfaces:**
- Produces: `var AgentScript string` (`//go:embed templates/agent/agent.sh`) in package `pstack`.
- Output contract, one line each, keys in this order (spec §1, plus `node` on `ctr` and `end`: `docker service logs --raw` interleaves nodes line by line, so each line must name its node):
  ```
  pstack-agent v1 node {"seq":1,"node":"<swarm node id or empty>","hostname":"<docker info .Name>","cores":8,"load1":1.02,"load5":1.1,"memTotal":<bytes>,"memAvailable":<bytes>,"diskPath":"<DockerRootDir>","diskTotal":<bytes>,"diskFree":<bytes>}
  pstack-agent v1 ctr {"seq":1,"node":"…","id":"<12-char id>","stack":"pr-42","cpuPct":3.1,"mem":<bytes>}
  pstack-agent v1 end {"seq":1,"node":"…"}
  ```
- Env: `PSTACK_AGENT_ROOT` (prefix for `/proc/meminfo` and `/proc/loadavg`, default empty), `PSTACK_AGENT_ONCE=1` (print one batch and exit). Interval is 30 s, not a knob.

- [ ] **Step 1: Write the fixtures and the failing test.** Fake `docker` answers `info --format {{.Swarm.NodeID}}|{{.Name}}|{{.DockerRootDir}}`, `ps --format {{.ID}}|{{.Label "com.docker.stack.namespace"}}|{{.Label "com.docker.compose.project"}}` (one swarm-stack container, one compose-project container, one with neither label) and `stats --no-stream --format {{.ID}}|{{.CPUPerc}}|{{.MemUsage}}` (MiB, GiB and kB values). Fake `df` records its argv to a file and prints a `df -P` table; fake `nproc` prints 8. `TestAgentScriptPrintsOneBatch`: run `sh -c AgentScript` with an explicit `cmd.Env` (`PATH=<testdata/bin>:/usr/bin:/bin`, root, once); stdout equals `agent-output.txt` byte for byte; df was called with `-P /host/var/lib/docker`. `TestAgentScriptPrintsNothingWhenDockerFails` (fake docker exits 1 on `info`): stdout empty.
- [ ] **Step 2: Run** `go test ./packages/pstack/ -run TestAgent` (FAIL).
- [ ] **Step 3: Implement `agent.sh`.** POSIX sh and POSIX awk only (busybox in the image, dash and mawk on CI): no bashisms, no gawk extensions, no jq. Memory from `MemTotal`/`MemAvailable` (kB × 1024); loads from `/proc/loadavg`; cores from `nproc`; disk from `df -P "/host$root"` (1024-blocks total, Available as free, × 1024). `stack` is the stack namespace, else the compose project, else `""`. Join `ps` to `stats` on the short id. Mem units: B, KiB, MiB, GiB, TiB (1024) and kB, MB, GB (1000). Byte values print with `%.0f`, never `%d` (mawk's `%d` truncates past 2^31; a 32 GB `memTotal` must survive). A cycle where any read fails prints nothing, so no `end` reaches the manager. `seq` starts at 1 and increments per cycle.
- [ ] **Step 4: Run** the test (PASS). Read `agent-output.txt` against the contract above line by line.
- [ ] **Step 5: Commit** `feat(agent): pstack-agent node reporter script`.

### Task 4: `internal/nodestats`: parse and store

**Files:**
- Create: `packages/pstack/internal/nodestats/parse.go`, `store.go`, `parse_test.go`, `store_test.go`, `testdata/` (cut and restart fixtures)

**Interfaces:**
- Consumes: `packages/pstack/templates/agent/testdata/agent-output.txt` (Task 3), read from the test as `../../templates/agent/testdata/agent-output.txt`.
- Produces:
  ```go
  type NodeLine struct { Seq int64 `json:"seq"`; Node string `json:"node"`; Hostname string `json:"hostname"`; Cores int `json:"cores"`; Load1 float64 `json:"load1"`; Load5 float64 `json:"load5"`; MemTotal int64 `json:"memTotal"`; MemAvailable int64 `json:"memAvailable"`; DiskPath string `json:"diskPath"`; DiskTotal int64 `json:"diskTotal"`; DiskFree int64 `json:"diskFree"` }
  type CtrLine struct { Seq int64 `json:"seq"`; Node string `json:"node"`; ID string `json:"id"`; Stack string `json:"stack"`; CPUPct float64 `json:"cpuPct"`; Mem int64 `json:"mem"` }
  type Batch struct { Key string; Raw string; Node NodeLine; Ctrs []CtrLine } // Raw: the node line's JSON text, the batch's identity
  func Key(n NodeLine) string            // n.Node, or n.Hostname when Node is ""
  func Parse(logs string) []Batch        // newest complete batch per Key, sorted by Key; Ctrs non-nil
  const StaleMs = 120_000
  const HistoryLen = 120
  type Sample struct { At int64 `json:"at"`; Load5 float64 `json:"load5"`; MemAvailable int64 `json:"memAvailable"`; DiskFree int64 `json:"diskFree"` }
  type Entry struct { Batch Batch; At int64; History []Sample } // At: when this Raw was first seen
  type Store struct{ … } // no mutex: the owner holds one (Task 6: Server.statsMu)
  func NewStore() *Store
  func (s *Store) Record(now int64, batches []Batch) // new Raw for a key: At = now, append a Sample, drop the oldest past HistoryLen; same Raw: no-op
  func (s *Store) Get(key string) (Entry, bool)      // a copy: History and Ctrs are fresh slices, never Record's backing arrays
  func (s *Store) Fresh(key string, now int64) (Batch, bool) // now - At <= StaleMs
  func (s *Store) Keys() []string                    // sorted
  ```
- Parse rules: only lines beginning `pstack-agent v1 ` count (anything else, including a docker prefix, is skipped); a batch is the `node` line, then the `ctr` lines with the same `(node, seq)`, closed by the matching `end`; a `ctr` or `end` with no open batch is dropped; the newest batch is the LAST complete one in stream order, never the highest `seq`.

- [ ] **Step 1: Failing tests.** `TestParseAgentOutput` (the Task 3 file → one batch, three ctrs, values equal to the fixture). `TestParseDropsAnIncompleteBatch` (two complete batches then a `node` + `ctr` with no `end` → the second complete one). `TestParseNewestIsStreamOrder` (seq 900 complete, then seq 1 complete for the same node → seq 1). `TestParseInterleavedNodes` (two nodes' lines alternating → two batches, each with its own ctrs). `TestStoreRecordAndStale` (Record twice with the same Raw keeps At; at At+120001 `Fresh` is false; 121 distinct batches leave 120 samples, the oldest gone).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/nodestats/` (FAIL).
- [ ] **Step 3: Implement** with `encoding/json` into the structs above; no map ranged into output.
- [ ] **Step 4: Run** (PASS, `-race`).
- [ ] **Step 5: Commit** `feat(nodestats): parse agent batches and keep an hour of samples`.

### Task 5: Agent lifecycle in `init`

**Files:**
- Create: `packages/pstack/internal/nodestats/commands.go` (+ `commands_test.go`)
- Modify: `packages/pstack/internal/initctl/init.go` (+ `init_test.go`), `internal/cli/` (read `PSTACK_AGENT_IMAGE` into the init options)
- Modify: regenerated `packages/conformance/golden/cli/init-*`, and whatever else `bun gen/goldens.ts` moves

**Interfaces:**
- Consumes: `pstack.AgentScript` (Task 3), `swarm.Shq`.
- Produces, in `nodestats`:
  ```go
  const AgentImage = "docker:29-cli"
  const AgentName = "pstack-agent"
  func AgentContainerCmd(script, image string) string
  // docker rm -f pstack-agent >/dev/null 2>&1; docker run -d --name pstack-agent --restart unless-stopped --label pstack.role=agent
  //   -v /var/run/docker.sock:/var/run/docker.sock:ro -v /:/host:ro --log-driver json-file --log-opt max-size=1m
  //   --entrypoint sh <image> -c '<script>'
  func AgentServiceCmd(script, image, selfNodeID string) string
  // docker service rm pstack-agent >/dev/null 2>&1; docker service create --detach --mode global --name pstack-agent
  //   --label pstack.role=agent --constraint 'node.id!=<self>' --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly
  //   --mount type=bind,src=/,dst=/host,readonly --log-driver json-file --log-opt max-size=1m --restart-condition any
  //   --entrypoint sh <image> -c '<script>'     (no --constraint when selfNodeID is "", as under --dry-run)
  func AgentLogsCmds(swarm bool) []string
  // ["docker logs --since 90s pstack-agent 2>/dev/null"], plus "docker service logs --since 90s --raw pstack-agent 2>/dev/null" on swarm
  ```
- `initctl.Options` gains `AgentImage string`; cli fills it from `PSTACK_AGENT_IMAGE` with `||` semantics (empty is unset → `nodestats.AgentImage`). No flag, no `.env` key, no `initguard` row, no upgrade readback: an operator who overrides it re-exports it on every `init`/`upgrade`. (Verified: `upgrade`'s `initEnv` is merged OVER the runner's base env, `exec.RunOptions.Env`, so an exported value reaches the re-run init.)

- [ ] **Step 1: Failing tests.** `TestAgentCommands`: exact strings for both builders (script `echo hi`, image `docker:29-cli`, self `n1`), and `AgentLogsCmds(false)`/`(true)`. `TestInitStartsTheAgent` (fake runner, compose): the container command runs after `control stack up`, label `node agent`. `TestInitStartsTheAgentOnSwarm`: the container command AND the service command with `node.id!=<id from docker info>`. `TestInitAgentFailureIsANote`: the container command fails → init still returns nil and prints `  note      pstack-agent did not start: <first stderr line>`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/nodestats/ ./packages/pstack/internal/initctl/` (FAIL).
- [ ] **Step 3: Implement.** A new init step "4b" after the health proof: on swarm read the self id with `docker info --format '{{.Swarm.NodeID}}'` (label `swarm node id`), run the container command (label `node agent`), and on swarm the service command (label `node agent service`). Non-fatal on purpose: a host without the agent keeps today's behaviour, since every guard is inert on missing data. Add one summary line after `previews`: `  agent     pstack-agent (<image>)`. `upgrade`, `pstack ui` and `pstack logging` re-run init, so they recreate it with no change of their own.
- [ ] **Step 4: Run** Go tests, rebuild, `bun gen/goldens.ts`. Expect the `init-*` transcripts to move (new dry-run labels and the summary line) and `upgrade-plan-*` not to. The spec expected the cloud-init and swarm-join goldens to change: workers need nothing (the global service follows them), so expect them unchanged, and report it if they are.
- [ ] **Step 5: Commit** `feat(init): run pstack-agent on every node`.

### Task 6: `statsLoop` and `GET /api/stats`

**Files:**
- Create: `packages/pstack/internal/api/routes_stats.go` (+ `routes_stats_test.go`)
- Modify: `internal/api/server.go` (fields, `Start`), `http.go` (`Tuning`), `internal/cli/serve.go`, `routes.go`, `permissions.go`, `permissions_test.go`, `packages/pstack/api/openapi.yaml`, `internal/apicli/*`, `apicli.go` (96 → 97)
- Modify: `packages/client/src/types.ts`, `index.ts`, `packages/client/test/client.test.ts`
- Create: `packages/conformance/test/api-stats.test.ts`; modify `api-rbac.test.ts`, `expected-pass.json`

**Interfaces:**
- Consumes: `nodestats.Parse`, `Store`, `AgentLogsCmds` (Tasks 4, 5); `swarm.SwarmInfo`.
- Produces, on `*Server` (owner `statsMu` for every field below; comment says so):
  ```go
  stats    *nodestats.Store
  nodeMeta map[string]nodeMeta // key → from SwarmInfo; kept when docker does not answer
  selfKey  string              // SwarmInfo.NodeID on swarm; the only batch's key on compose
  onSwarm  bool
  type nodeMeta struct { Hostname, Role, Availability string } // Role "" on compose → null in JSON
  statsNow func() int64                 // clock seam, default time.Now().UnixMilli; tests in Tasks 6–10 pin it
  func (s *Server) statsTick()          // one read: SwarmInfo, the log commands (outputs concatenated), Record. Tasks 7, 8, 10 append calls here, after the lock is released.
  func (s *Server) statsLoop()          // ticker on Options.StatsTickMs, recover per tick, stops on s.ctx
  func (s *Server) statsBody(now int64) jsonx.Object // the route and later tests share it
  ```
- `Options.StatsTickMs int64`; `Tuning.StatsTickMs float64`, `StatsTickSet bool` parsed like the signals pair; `serve`'s `statsTick(t)` defaults to `api.StatsTickDefaultMs = 30_000`.
- Body, keys in this order: `{v:1, at, agent, nodes:[{node, hostname, role, at, stale, cores, load1, load5, memTotal, memAvailable, diskTotal, diskFree, pressure:[], prune, history:[{at, load5, memAvailable, diskFree}]}], stacks:[{stack, cpuPct, mem, containers, nodes:[hostname]}], prune:{lastAt, lastFreed}}`. Node rows: swarm → every `nodeMeta` key, sorted by hostname, joined to its entry; compose → every store key. A stale or never-seen node has `at`/values `null` and `stale: true`; `history` keeps its samples. `agent` is true when any node is fresh. `stacks`: fresh batches only, `stack != ""`, summed, sorted by `mem` descending then name; `pstack-control` is included (it is real use of the node; the UIs link only stacks that match a deployment). Per-node `prune` (`{at, freedBytes, reason}` or `null`) and the top-level `prune` are filled by Task 8: until then node `prune: null` and top-level `{lastAt: null, lastFreed: null}`. `pressure` is filled by Task 7: `[]` until then. Reads emit nothing.
- Route: `GET /api/stats`, viewer, openapi tag `stats` (new tag entry with a description), operationId `getStats`, `x-cli-name: get`.
- Client: `type NodeStats`, `StackStats`, `Stats`, and `stats: { get: () => get<Stats>('/api/stats') }`.

- [ ] **Step 1: Failing tests.** Go, with `exec.NewFake` answering the exact command strings: `TestStatsComposeHost` (`docker info --format '{{json .Swarm}}'` inactive; `docker logs …` returns `agent-output.txt` → one node, `role: null`, three-container fixture aggregated into stacks, whole body compared with `at` masked). `TestStatsSwarmJoinsNodeLs` (two nodes in `node ls`, a batch for one → the other is `stale: true` with nulls). `TestStatsNoAgent` (logs fail → `agent: false`, every node value `null`, `stacks: []`, never `null` lists). `TestStatsDockerSilentKeepsNodes` (second tick with `docker info` failing keeps the first tick's nodes). Conformance `api-stats.test.ts` with `STATS_SHIM` and `PSTACK_STATS_TICK_MS: '100'`: poll until one node, assert hostname, `memAvailable` and the stacks; a second server with no shim answers `agent: false` and `nodes: []`. RBAC row viewer → 200. Client test: `stats.get()` against the spawned binary returns `v: 1` and array `nodes`.
- [ ] **Step 2: Run** the Go and conformance tests (FAIL).
- [ ] **Step 3: Implement**; start `go s.statsLoop()` in `Start` only when `StatsTickMs > 0`, beside `signalsLoop`. `go generate`, `OperationCount` = 97.
- [ ] **Step 4: Run** Go, conformance (`bun test`, `bun run vacuity`), client tests; rebuild and `bun gen/goldens.ts` if the help or API goldens move.
- [ ] **Step 5: Commit** `feat(api): GET /api/stats from the node agent`.

### Task 7: Pressure, and `node.pressure` / `node.recovered`

**Files:**
- Create: `packages/pstack/internal/nodestats/pressure.go` (+ `pressure_test.go`), `packages/pstack/internal/api/pressure.go` (+ `pressure_test.go`)
- Modify: `internal/api/routes_stats.go` (call from `statsTick`, `pressure` in the body), `server.go` (field), `internal/events/events.go` (+ test), `internal/notify/notify.go` (+ test), `docs/webhook-events.md`

**Interfaces:**
- Consumes: `Store.Fresh`, `nodeMeta`, `selfKey` (Task 6); the five readers (Task 2).
- Produces, in `nodestats`:
  ```go
  type Kind string
  const ( Disk Kind = "disk"; Memory Kind = "memory"; CPU Kind = "cpu" )
  type Thresholds struct { DiskPct, MemPct int; CPULoad float64 }
  type Reading struct { DiskUsedPct, MemFreePct, LoadPerCore float64 }
  func Read(n NodeLine) (Reading, bool)              // false when DiskTotal, MemTotal or Cores is 0
  func Evaluate(prev []Kind, r Reading, t Thresholds) []Kind // disk, memory, cpu order; non-nil
  func Describe(k Kind, r Reading, t Thresholds) string
  ```
  Enter: used ≥ DiskPct; free ≤ MemPct; load5/cores ≥ CPULoad. Stay (kind in `prev`): used ≥ DiskPct−5; free ≤ MemPct+5; load ≥ CPULoad−0.25. A threshold of 0 never holds the kind. `Describe` rounds percents to integers and load to one decimal (`js.NumberString`): at or past the line `disk 94% ≥ 90%`, `memory 7% free ≤ 10%`, `load 2.4 per core ≥ 2`; held by the margin `disk 87%, clears below 85%`, `memory 12% free, clears above 15%`, `load 1.9 per core, clears below 1.75`.
- On `*Server` (under `statsMu`): `pressure map[string][]nodestats.Kind` (current state, fresh nodes only; a stale node keeps its entry untouched; a node gone from both `nodeMeta` and the store is dropped silently). `func (s *Server) evaluatePressure(now int64)`: diff under the lock, emit after. `func (s *Server) inPressure(key string, now int64) ([]nodestats.Kind, nodestats.Reading, bool)` for Tasks 8–10: false unless the node is fresh.
- Events (append to the END of `events.Names`, after `route.unassigned`): `node.pressure` when the set goes from empty to non-empty, `node.recovered` from non-empty to empty, nothing when the kinds change inside pressure. Payload struct, fields in order: `id` (`pressure/<key>`), `node`, `hostname`, `kinds`, `disk` (used %), `memory` (free %), `cpu` (load per core), each rounded to one decimal, and `thresholds: {disk, memory, cpu}`.
- `Summarize`: `worker-2 is under pressure: disk 94% ≥ 90%.` (Describe of each kind, joined with `, `); `Resolved — worker-2 is no longer under pressure.`
- `/api/stats` node `pressure`: the current kinds for a fresh node, `[]` otherwise.

- [ ] **Step 1: Failing tests.** `TestEvaluateHysteresis` table: disk 89.9 stays out, 90 enters, 85 stays, 84.9 clears; memory 10 enters, 15 stays, 15.1 clears; cpu 2.0 enters, 1.75 stays, 1.74 clears; threshold 0 with the kind in `prev` → cleared. `TestDescribe` (the six strings above). Server, fake batches via `exec.NewFake`: `TestPressureEmitsOnEdgesOnly` (95 % disk on three ticks → one `node.pressure`; 80 % → one `node.recovered`). `TestPressureThresholdZeroRecovers` (in pressure, set `pressure_disk_pct` 0 → one `node.recovered`). `TestPressureStaleIsSilent` (in pressure, then no batch for 121 s on a pinned clock → no event, `inPressure` false). Events order test and `Summarize` cases updated.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/nodestats/ ./packages/pstack/internal/api/ ./packages/pstack/internal/events/ ./packages/pstack/internal/notify/` (FAIL).
- [ ] **Step 3: Implement**; the stale test pins `s.statsNow` (Task 6). `docs/webhook-events.md`: a section per event with its fields.
- [ ] **Step 4: Run** (PASS, `-race`).
- [ ] **Step 5: Commit** `feat(pressure): detect node pressure and raise node.pressure/node.recovered`.

### Task 8: Pruning and `node.pruned`

**Files:**
- Modify: `packages/pstack/internal/nodestats/commands.go` (+ test)
- Create: `packages/pstack/internal/api/prune.go` (+ `prune_test.go`)
- Modify: `internal/api/routes_stats.go` (call, body), `server.go` (fields, runner seam), `internal/events/events.go` (+ test), `internal/notify/notify.go` (+ test), `docs/webhook-events.md`

**Interfaces:**
- Consumes: `inPressure`, `selfKey`, `onSwarm`, `nodeMeta` (Tasks 6, 7); `ImagePruneAfterH` (Task 2).
- Produces, in `nodestats`:
  ```go
  func PruneScript(untilH int, builder bool) string
  // per prune command, prints `pstack-prune v1 <node id, or - on compose> <reclaimed>` from docker's
  // "Total reclaimed space: <size>" line. Scheduled: image prune -af --filter until=<h>h, then builder prune -af --filter until=<h>h.
  // Emergency (untilH 0, builder false): image prune -af only, any age.
  func PruneJobCmd(name, script, constraint string, global bool) string
  // docker service create --name <name> --mode global-job|replicated-job (--replicas 1) [--constraint <c>]
  //   --restart-condition none --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock --entrypoint sh
  //   "$(docker service inspect -f '{{.Spec.TaskTemplate.ContainerSpec.Image}}' pstack-agent)" -c '<script>'
  // Not --detach: a job create returns when the job completes. The image is the agent's own, so a
  // PSTACK_AGENT_IMAGE override reaches prunes without the pstack container knowing it.
  type Reclaimed struct { Node string; Bytes int64 }
  func ParsePrune(out string) []Reclaimed // summed per node, "-" → "", sorted by Node
  func ParseSize(s string) (int64, bool)  // docker's decimal units: B, kB, MB, GB, TB ("0B", "1.234GB")
  ```
- On `*Server`: `pruneRunning atomic.Bool`; under `statsMu`: `lastPrune map[string]pruneRecord{At, Freed int64; Reason string}`, `lastEmergency map[string]int64`, `lastScheduled int64` (set to boot time, so the first scheduled prune is 6 h after boot). `pruneRunner func(ctx context.Context) exec.Runner` (seam, default quiet runner on `s.env`). `func (s *Server) maybePrune(now int64)` from `statsTick`: when no prune runs, a disk-pressured fresh node whose last emergency is ≥ 10 min old starts an emergency prune for that node; else, when `ImagePruneAfterH() > 0` and 6 h have passed, a scheduled one. `func (s *Server) prune(reason string, keys []string)` runs on its own goroutine (`recover`, `pruneRunning` cleared on exit) under a 10-minute context.
- Where each node is pruned: the self node always by `PruneScript` through the runner directly; on swarm, scheduled also runs one `global-job` with `--constraint node.id!=<self>`; an emergency on another node runs a `replicated-job` with `node.id==<id>`. A job: create, `docker service logs --raw <name>`, `docker service rm <name>` on every path, including a failed or timed-out create. Name `pstack-prune-<unix ms>`.
- Event (appended after `node.recovered`): `node.pruned {node, hostname, reason: "scheduled"|"pressure", freedBytes}`, one per node that reported. A failed run emits nothing. `Summarize`: `Pruned worker-2 (pressure): 12.3 GB freed.`
- `/api/stats`: node `prune` from `lastPrune`; top-level `{lastAt, lastFreed}` is the newest run's time and its total across nodes.

- [ ] **Step 1: Failing tests.** `TestParseSize` (`0B` → 0, `512kB` → 512000, `1.234GB` → 1234000000, `junk` → false). `TestParsePrune` (two lines for n2 summed, a `-` line → `""`, a non-prune line skipped). `TestPruneJobCmd` (exact strings, global and replicated). Server, fake runner: `TestEmergencyPruneOncePerTenMinutes` (disk 95 % on three ticks within 10 min → one replicated-job for n2; at +10 min → a second). `TestScheduledPruneAfterSixHours` (pinned clock: nothing at +5 h 59 m; at +6 h the self node through the runner and one global-job excluding self; `image_prune_after_h` 0 → nothing). `TestPruneAlwaysRemovesTheJob` (create fails → `docker service rm pstack-prune-…` still runs, no event). `TestPruneZeroBytes` (`0B` → `node.pruned` with `freedBytes: 0`).
- [ ] **Step 2: Run** (FAIL).
- [ ] **Step 3: Implement.** Tests call `s.prune` synchronously; only `maybePrune` uses `go`.
- [ ] **Step 4: Run** (PASS, `-race`).
- [ ] **Step 5: Commit** `feat(pressure): scheduled and emergency image prunes`.

### Task 9: Refuse deploys and wakes under pressure

**Files:**
- Modify: `packages/pstack/internal/stack/stack.go` (`PhaseCapacity`), `internal/api/server.go` (`startLifecycle`), `internal/api/pressure.go` (+ test)
- Modify: `packages/client/src/types.ts:148` and `apps/ui/src/api/types.ts:123` (add `'capacity'` to the phase unions)
- Modify: `packages/conformance/test/api-stats.test.ts`, `expected-pass.json`

**Interfaces:**
- Consumes: `inPressure`, `nodeMeta`, `selfKey` (Tasks 6, 7).
- Produces: `const PhaseCapacity Phase = "capacity"` in `stack`; `func (s *Server) capacityRefusal(o spec.Orchestrator, now int64) (why string, refused bool)`:
  - `spec.Swarm`: refused when at least one node has availability `active` and every such node is fresh and in pressure. `why` names the first by hostname: `worker-2: disk 94% ≥ 90%` (`<hostname>: ` + Describe of its first kind).
  - otherwise (a compose deployment, on either host): refused when the self node is fresh and in pressure.
- In `work`, for `jobs.Up` and `jobs.Wake`, BEFORE the `stack.woken` emit and `stack.Up`: when refused, return `stack.Outcome{OK: false, Steps: []stack.StepResult{{Axis: st.Stack, Phase: stack.PhaseCapacity, OK: false, Code: 1, Message: &why}}, Outputs: map[string]string{}}` (the job fails, `job.failed` fires, `failedWhy` on the wake page reads `capacity <stack>: worker-2: disk 94% ≥ 90%`). Down, sleep and verify are never refused.

- [ ] **Step 1: Failing tests.** `TestCapacityRefusalSwarmRule` table: two active nodes, one in pressure → allowed; both → refused naming the first hostname; the free node drained → refused; the free node stale → allowed; no node data at all → allowed. `TestCapacityRefusalCompose` (self in pressure → refused; a worker in pressure only → allowed). `TestUpRefusedUnderPressure` (fake runner: the job's outcome has the capacity step, no `docker compose` command ran, `stack.woken` not emitted for a refused wake). `TestDownAllowedUnderPressure`. Conformance: `STATS_SHIM` with a 95 % disk batch → `POST /api/deployments/<id>/up` → the job fails with `phase: "capacity"`; the same server with `pressure_disk_pct` set to 0, then poll `GET /api/stats` until the node's `pressure` is `[]` (order the inputs, never sleep) → the next up is not refused at the capacity step.
- [ ] **Step 2: Run** (FAIL).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** Go, conformance, both UI builds (the phase unions).
- [ ] **Step 5: Commit** `feat(pressure): refuse deploys and wakes when no node has room`.

### Task 10: Auto-sleep the least recently used stack

**Files:**
- Modify: `packages/pstack/internal/api/pressure.go` (+ test), `routes_stats.go` (call from `statsTick`)

**Interfaces:**
- Consumes: `inPressure`, `Store.Fresh` (ctr lines give each node's stacks), `PressureSleepIdleMin`, `s.meter.OK()`, `s.meter.LastActivity`, `s.scheduler.BootAt`, `s.runtimeView`, `s.registry.List`, `s.resolveDep`, `s.jobs.IsBusy`, `s.startLifecycle`.
- Produces: `func (s *Server) pressureSleep(now int64)`. Does nothing when `PressureSleepIdleMin() == 0` or `!s.meter.OK()`. Otherwise, for fresh nodes in `memory` or `cpu` pressure (key order), collect deployments whose stack has a container on that node and that are: not asleep, not `kind: shared`, `st.Compose != nil`, not `IsBusy`, and idle ≥ the setting, where recency = max(`BootAt`, newest container `StartedAt`, `LastActivity(routers)`) from `runtimeView`. `st.Sleep` is NOT required (a wake runs `up` off the record). Sleep the one with the oldest recency (ties by stack name) via `startLifecycle(id, dep, st, jobs.Sleep, lifecycleOptions{By: "pressure", Reason: "<hostname> <Describe of the node's first memory/cpu kind>", OnlyIfIdle: true})`. At most ONE sleep per tick across all nodes. Collect under `statsMu`, release, then call docker and `startLifecycle`.

- [ ] **Step 1: Failing tests** (fake runner, pinned meter): `TestPressureSleepsTheLeastRecentlyUsed` (three stacks on n2, memory pressure → only the oldest sleeps; `stack.slept` reason starts `worker-2 memory`). `TestPressureSleepSkipsWhenMeterNotOK`. `TestPressureSleepEligibility` table: shared, busy, already asleep, used 10 min ago with the setting 30 → none slept; disk-only pressure → none. `TestPressureSleepOnePerTick` (two nodes in pressure → one sleep). `TestPressureSleepOffAtZero`.
- [ ] **Step 2: Run** (FAIL).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** (PASS, `-race`).
- [ ] **Step 5: Commit** `feat(pressure): auto-sleep the least recently used stack on a full node`.

### Task 11: Hearth Stats page and settings

**Files:**
- Create: `apps/ui-hearth/src/api-stats.ts`, `src/lib/stats.ts`, `test/stats.test.ts`, `src/views/StatsView.vue`
- Modify: `apps/ui-hearth/src/router.ts`, `src/App.vue` (`all` nav array), `src/views/SettingsView.vue`, `src/lib/settings.ts` if the drafts live there

**Interfaces:**
- Consumes: `Stats`, `NodeStats`, `StackStats`, `SettingKey` from `@samyx/preview-stacks-client` (Tasks 2, 6).
- Produces: `statsApi.read(): Promise<…Stats>` (`call('GET', '/api/stats')`). `lib/stats.ts`: `diskUsedPct(n: NodeStats): number | null`, `memFreePct(n): number | null`, `loadPerCore(n): number | null`, `fmtBytes(b: number | null): string` (decimal, `—` for null), `sortStacks(s: StackStats[]): StackStats[]` (mem desc, then name), `deploymentFor(stack: string, deployments): string | null`.
- Page `/stats`, nav `{ id: 'stats', label: 'Stats', icon: <an icon the set already has> }` after Swarm. Nodes: `HStatCard` per node, an `HProgress` each for disk used, memory used and load against its threshold (thresholds from `GET /api/settings`), an `HSparkline` of `history`, an `HBadge` per pressure kind. Stacks: a table sorted by memory, the stack linking to its deployment when one matches. Last prune per node and freed bytes. `agent: false` shows exactly `No agent reports. Run \`pstack upgrade\`.` Poll every 10 s. Works with one node (compose).
- Settings view: the five keys as number inputs (admin to write, per `minRole`), each with its unit and `0 = off`.

- [ ] **Step 1: Failing unit tests** for every `lib/stats.ts` function, nulls included.
- [ ] **Step 2: Run** `bun test --cwd apps/ui-hearth` (FAIL), implement the lib, PASS.
- [ ] **Step 3: Build the page, nav, route and settings fields**; `bun run --cwd apps/ui-hearth build`.
- [ ] **Step 4: Browser check** with the `pstack-ui-verify` skill (headless Firefox on this machine; harness under `~/.claude/projects/-home-samimishal-projects-go-preview-stacks/ui-verify/`, with a fake `docker` answering `STATS_SHIM`-style output and one node in disk pressure): Stats at 1280 and 320 px, light and dark, plus the no-agent state and the Settings fields. No sideways overflow, no console errors. Screenshot paths in the report.
- [ ] **Step 5: Commit** `feat(ui-hearth): Stats page and pressure settings`.

### Task 12: Advanced UI Stats page and settings

**Files:**
- Create: `apps/ui/src/composables/stats.ts`, `apps/ui/test/stats.test.ts`, `apps/ui/src/views/StatsView.vue`
- Modify: `apps/ui/src/api/types.ts` (stats types, `SettingKey`), `src/router.ts`, `src/App.vue` (nav link after Swarm), `src/components/CommandPalette.vue` (`p.stats`), `src/views/SettingsView.vue`

**Interfaces:**
- Consumes: the `/api/stats` body (Task 6), the settings rows (Task 2).
- Produces: the same pure helpers as Task 11 in `composables/stats.ts` (same names and null rules).
- Page `/stats`, the same three sections as Task 11. Bars are plain CSS (`<div>` with a width and a threshold tick), each with a text value beside it, no chart library. Tables are `table.cards` with `data-label` on every cell. Pressure badges render the kind through `sentence()`. Same no-agent copy, same 10 s poll (`usePolling`).
- Settings view: the five keys in the host settings panel, showing `source` like the existing two.

- [ ] **Step 1: Failing unit tests** for `composables/stats.ts`.
- [ ] **Step 2: Run** `bun test --cwd apps/ui` (FAIL), implement, PASS.
- [ ] **Step 3: Build the page, route, nav, palette entry and settings fields**; `bun run --cwd apps/ui build`.
- [ ] **Step 4: Browser check** as in Task 11 (1280/320, light/dark, no-agent state, Settings, keyboard focus ring on the deployment links). Screenshot paths in the report.
- [ ] **Step 5: Commit** `feat(ui): Stats page and pressure settings`.

### Task 13: Docs and CHANGELOG

**Files:**
- Modify: `docs/usage.md` (a "Node pressure" section under §9 Day-2 operations: the agent, `/api/stats`, the five settings, the guards and their rules, prune timing, the sleeping-image caveat, `PSTACK_AGENT_IMAGE`, `PSTACK_STATS_TICK_MS`), `docs/control-plane.md` (the agent beside the control stack, why logs are the transport, the drained-manager rule), `docs/webhook-events.md` (check Tasks 7–8), `docs/README.md` (rows for the design and this plan), `packages/pstack/CHANGELOG.md`, `AGENTS.md`, `docs/node-pressure-design.md` (status banner)

**Interfaces:**
- Consumes: everything above; documents it, adds no behaviour.
- CHANGELOG: a `## Unreleased` above 0.44.0 with Added (agent, `/api/stats`, pressure guards and events, prunes, Stats pages, settings) and Changed (log caps: every render golden and init transcript changed; previews with Loki off now get a json-file cap, so every deploy writes `compose.generated.yml`, including a CLI `up` beside the user's compose file).
- AGENTS.md: `Names (34)` → 37, `OperationCount (91)` → 97, "the eight embeds" → nine, 42 → 43 packages, a `nodestats` row, `statsLoop` and the prune runner in Go rule 14's goroutine list, `PSTACK_AGENT_IMAGE`/`PSTACK_STATS_TICK_MS` in rule 18's carve-out.
- Design banner: "Built; where the build differs:" listing the decisions in this plan's report (the `node` key on every agent line, the self-node rule, per-node prune in `/api/stats`, the hysteresis wording of `Describe`).

- [ ] **Step 1: Write the docs.**
- [ ] **Step 2: Run** `bun run --cwd apps/docs build` (the sync fails on a broken relative link) and `bun run check` from the repo root.
- [ ] **Step 3: Commit** `docs: node pressure`.
