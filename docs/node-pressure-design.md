# Node pressure — a reporter on every machine, guards in the control plane, a stats page

> **Built.** Using it: [usage.md](usage.md#node-pressure-0450), `GET /api/stats`, the Stats page.
> The build plan is [node-pressure-plan.md](node-pressure-plan.md). The spec below is kept as
> approved on 2026-10-28. Where the build differs from it:
>
> - **Every agent line carries `node`**, `ctr` and `end` included. A batch is matched on
>   (node, seq), because two workers' lines interleave in `service logs` and can share a seq.
> - **The control-plane node is served locally, never by a swarm task.** A drained manager
>   (`--previews-on workers`) runs no swarm task, global or job, and it is the node whose disk
>   filled. So `init` runs the agent there as a container, the global service gets
>   `--constraint node.id!=<self>`, and that node's prunes go through the host runner. When `init`
>   cannot read this node's swarm id, it skips the service with a note rather than create it
>   unconstrained, which would make the manager report twice.
> - **`/api/stats` has two fields the spec does not list:** `stale` per node (why its readings are
>   `null`) and `prune` per node (`{at, freedBytes, reason}` or `null`), for "last prune per node".
> - **`agent: false` is not the upgrade prompt.** The UIs say to run `pstack upgrade` only when no
>   node has ever reported. Otherwise they show the nodes as `Stale`, since a crash-looping agent is not
>   an un-upgraded host.
> - **`Describe` states free memory**, because `MemAvailable` is what is measured:
>   `memory 6% free ≤ 10%`, not the spec's `memory 93%`. Load reads `load 2.4 per core ≥ 2`. While
>   only the recovery margin holds a kind, it says where it clears: `disk 87%, clears below 85%`.
>   The auto-sleep reason is `<hostname> <Describe>`.
> - **The event id is `pressure/<key>`:** the node id on swarm, as specified, and the hostname on a
>   compose host, where `pressure/<node>` would read `pressure/`. `node` is `null` on compose in
>   `/api/stats` and in all three events.
> - **Refusal on swarm needs evidence.** It refuses only when every active node is fresh and in
>   pressure. With no active node, or no data, the deploy goes ahead: unknown is never pressure.
> - **One auto-sleep per tick in total**, across every node, not one per node.
> - **The scheduled prune does not wait for agent data.** It runs every 6 hours from the first tick.
>   Only the emergency prune needs a fresh report.
> - **A prune job needs a node that takes tasks.** A swarm job no ready, active node can run waits
>   forever, so the global job is skipped when no node but the control plane's is ready and active,
>   and an emergency prune skips a node that is not.
> - **Cloud-init and swarm-join goldens are unchanged.** Workers need nothing: the global service
>   follows them. Only the init transcripts moved.
> - **The preview log cap means every deploy writes `compose.generated.yml`,** a CLI `pstack up`
>   beside the user's compose file included.
> - **`PSTACK_AGENT_IMAGE` is an env-only init input, never read back** (like
>   `PSTACK_NO_OOM_PROTECT`). An `upgrade` run without it returns the agent to `docker:29-cli`.
>   Cloud-init does not pass it.
> - **Invariant 20 is amended, not broken:** the caps are inline blocks in the template and the
>   service builders, and no marker was added.
> - **One sparkline per node, of load**, in both UIs, since disk free over an hour is usually flat.
>
> Asked for 2026-10-28, after preview.hou-clou.dev filled
> its 601 GB disk with images, Traefik's restart failed with `no space left on device`, and nothing
> served for 3½ days. Owner's picks: a pstack node agent; on pressure refuse deploys and wakes, alert,
> auto-sleep idle stacks and prune; design then build.

## Decide first: where this differs from what you picked

1. **The agent is a shell script in a stock `docker:<ver>-cli` image, not the pstack binary.**
   Workers have no `pstack:local` and no pstack binary, by design (the worker cloud-config: "no
   pstack, no control stack"), and no pstack image is published to a registry. A global service
   needs an image every node can pull. `docker:29-cli` (pinned, `PSTACK_AGENT_IMAGE` overrides) is
   pullable like any preview image and carries `sh`, `awk`, `df` and the docker CLI. The script is
   embedded in the binary (`templates/agent/agent.sh`) and passed as the service command.
   The other way, building `pstack:local` on every worker, means the join script and the worker
   cloud-config install pstack, plus their goldens. Rejected: a lot more on every worker to save a
   ~60-line script.
2. **The agent only reports. Every decision is made in the control plane.** Thresholds, prune
   timing, which stack sleeps: all of it lives in settings on the manager, so changing a threshold
   needs no redeploy of anything. Prunes run as swarm job services that the control plane starts
   (below), not inside the agent.

## Pieces

### 1. The agent: `pstack-agent`

- **Swarm:** `docker service create --mode global --name pstack-agent`, with the docker socket and
  the host's `/` mounted read-only at `/host`, `--log-driver json-file --log-opt max-size=1m`,
  `--restart-condition any`, and the label `pstack.role=agent`.
- **Compose host:** the same image and script, as one container (`docker run -d --name pstack-agent
  --restart unless-stopped …`).
- **Who creates it:** `pstack init` and `pstack upgrade` create or update it (invariant 12: lifecycle
  is CLI-only; the API never creates it). It is not part of the control compose file, so no new
  template marker is needed (invariants 20/21).
- **Every 30 s it prints one batch to stdout:**
  ```
  pstack-agent v1 node {"seq":812,"node":"<swarm node id|"">","hostname":"…","cores":8,
    "load1":1.0,"load5":1.1,"memTotal":…,"memAvailable":…,"diskPath":"/var/lib/docker",
    "diskTotal":…,"diskFree":…}
  pstack-agent v1 ctr {"seq":812,"id":"…","stack":"pr-42","cpuPct":3.1,"mem":123456789}
  …
  pstack-agent v1 end {"seq":812}
  ```
  - There is one line per container, so no line comes near docker's 16 KB split.
  - The manager uses only batches that have their `end` line.
  - `stack` comes from the `com.docker.stack.namespace` or `com.docker.compose.project` label (from
    `docker ps` joined to `docker stats --no-stream`), never from parsing a name.
- **What it measures:**
  - Memory is **`MemAvailable`** from `/proc/meminfo`. "Used" counts the page cache: the incident
    host showed 27 of 31 GB used while 4 GB was available.
  - CPU is the **1- and 5-minute load average divided by cores**, from `/proc/loadavg` and `nproc`.
    It is not an instantaneous percentage.
  - Disk is **`df -P /host<DockerRootDir>`**: the host's docker data root, not the container's
    overlay.
  - In a container `/proc/meminfo`, `/proc/loadavg` and `/proc/stat` show the host's values (no
    lxcfs), so `/host/proc` is not needed.

### 2. Transport: logs, pulled by the manager

- Every 30 s the manager runs `docker service logs --since 90s --raw pstack-agent` (on swarm) or
  `docker logs --since 90s pstack-agent` (on a compose host) through the quiet host runner, and keeps
  the newest complete batch per node.
- **Why logs:**
  - pstack listens only on networks local to the manager. An agent on a worker would otherwise need
    a credential and a trip through Traefik to post.
  - Logs need no network path, no secret and no new route, and work the same on both orchestrators.
- **Stale rule:** no complete batch in 2 minutes means the node is unknown. Its values are `null`
  (invariant 11), and **unknown is never pressure**: no guard fires on missing data.
- **Memory only** (invariant 10): the latest batch per node, plus a ring of 120 node samples (1 hour)
  for sparklines. A restart forgets the ring.
- A new goroutine, `statsLoop`, is added to Go rule 14's list, with `recover`.
- **On hosts without the agent** (not upgraded yet): every value is `null`, the page says "No agent
  reports. Run `pstack upgrade`.", and the guards stay inert.

### 3. Pressure

**When a node is in pressure.** A node is in pressure when any of these holds on its latest fresh
batch:

| Kind | Rule | Setting (default) |
|---|---|---|
| `disk` | used ≥ N % of the data-root filesystem | `pressure_disk_pct` (90) |
| `memory` | `MemAvailable` ≤ N % of total | `pressure_mem_pct` (10) |
| `cpu` | 5-min load ÷ cores ≥ N | `pressure_cpu_load` (2.0) |

- Setting a threshold to `0` turns that kind off.
- **Recovery** needs the value back past the threshold by a margin: 5 points for disk and memory,
  0.25 for CPU. A value sitting on the line therefore does not flap.
- **Settings** are new keys in `internal/settings`. Each has a `Set` arm, a reader and a permissions
  row (PUT at admin), and both Settings views edit them.

**Actions, while a node is in pressure.** These run on the `statsLoop` tick, edge-triggered like
signals:

1. **Alert.** `node.pressure` is emitted on entering pressure and `node.recovered` on leaving it.
   The payload is `{id: "pressure/<node>", node, hostname, kinds, disk, memory, cpu, thresholds}`.
   - Both events are appended at the end of `events.Names`, with a `notify.Summarize` line and a
     `webhook-events.md` section.
   - State is in memory, so a restart raises again whatever is still true. Receivers dedupe on `id`,
     as they do for signals.
2. **Emergency prune** (disk only). The manager prunes that node's unused images, all of them and of
   any age, then reads the node again on the next batch.
   - At most one emergency prune per node every 10 minutes, so a disk that is full of something
     other than images is not pruned in a loop.
   - Emits `node.pruned`.
3. **Auto-sleep** (memory or cpu).
   - Each tick, the manager sleeps the **least recently used** stack that has a container on that
     node. Recency is `max(last request, latest container start, boot)`, the same formula the idle
     rule uses.
   - Only stacks that are awake, not `kind: shared`, not busy, and unused for at least
     `pressure_sleep_idle_min` (30) minutes are eligible.
   - One stack per tick (30 s). Pressure is read again before the next.
   - **It skips the whole action when the traffic meter is not OK** ("silence is not idle").
   - It goes through `startLifecycle(…, jobs.Sleep, {By: "pressure", Reason: "worker-2 memory 93%"})`,
     so `stack.slept` fires as usual.
   - Every sleep record wakes on request whether or not the spec has a `sleep:` policy. A wake is
     `up` off the record, not off the policy.
   - Setting `pressure_sleep_idle_min` to `0` turns auto-sleep off.
4. **Refuse new work.** Inside `startLifecycle`'s `work`, for `jobs.Up` (deploy and redeploy) and
   `jobs.Wake`, so the POST route, wake-on-request, TLS redeploys and queued jobs are all covered.
   - The job fails with a step `{Phase: "capacity", OK: false, Message: "worker-2: disk 94% ≥ 90%"}`.
     The waking page shows it, and `job.failed` fires.
   - Down, sleep and verify are always allowed. They are how pressure goes away.
   - **Swarm rule** (owner, 2026-10-28: "no place for new deployments"): refuse when **no** node
     with `availability: active` is free of pressure. Which node a deploy lands on can't be known in
     advance.
     - Known gap: swarm can still place a task on a node that is in pressure when another node is
       free.
     - Not in this build: labelling a node in pressure and adding a placement constraint through
       autolabel.
   - **Compose rule:** refuse when the host is in pressure.

### 4. Pruning

- **Scheduled:** every 6 hours the control plane prunes every node with
  `docker image prune -af --filter until=<N>h` and `docker builder prune -af --filter until=<N>h`.
  `N` is `image_prune_after_h` (168, or 7 days); `0` turns scheduled pruning off.
- **How a prune runs on swarm:** a job service,
  `docker service create --mode global-job --name pstack-prune-<seq> …` using the agent image and
  socket. For one node it is `--mode replicated-job` with `--constraint node.id==<id>`. The manager
  reads the job's logs for "Total reclaimed space", then removes the service.
  - Job services need Engine 20.10 or newer; the incident host runs 29.x.
- **How a prune runs on a compose host:** the same commands, through the host runner.
- **Event:** `node.pruned {node, hostname, reason: "scheduled"|"pressure", freedBytes}`.
- **What `until` means:** image age, not last use.
  - Images in use by any container, stopped ones included, are never removed.
  - **A sleeping stack's image can be pruned**, and its next wake pulls or builds it again, so that
    wake is slower.
- **Not pruned:** containers and volumes.
  - The swarm's task history limit stays at 5 (owner, 2026-10-28).
  - The cost: a stopped container keeps its image from being pruned. Each service keeps its last
    five exited tasks, and after a redeploy those can hold older image versions until newer tasks
    push them out.
  - `container prune` is not the fix, because it would also remove a stopped container someone
    meant to keep.

### 5. Log caps (the incident's other cause)

- Traefik's own `-json.log` was 3.6 GB. No control container caps its log, and neither does the
  docker daemon.
- **Control template:** add a `logging: {driver: json-file, options: {…}}` cap to each control
  service that has no Loki logging set:
  - **Traefik:** `max-size: 10m`, `max-file: "2"`. Its logs matter only for a short while (owner,
    2026-10-28).
  - **Every other control service:** `max-size: 50m`, `max-file: "3"`.
  - This changes every render golden. Regenerate them with `bun gen/goldens.ts`, read the diff, and
    note it in the CHANGELOG.
- **Previews:** when Loki is off, autolabel gives every service that has no `logging:` a
  `json-file` driver with `max-size: 10m` and `max-file: "3"`. The precedent is
  `autolabel.go:521`, which already does this for Loki.

### 6. API and pages

- **`GET /api/stats`** (viewer) returns
  `{v:1, at, agent: bool, nodes:[{node, hostname, role, at, stale, cores, load1, load5, memTotal,
  memAvailable, diskTotal, diskFree, pressure:[kinds], history:[{at, load5, memAvailable,
  diskFree}]}], stacks:[{stack, cpuPct, mem, containers, nodes}], prune:{lastAt, lastFreed}}`.
  - Unknown values are `null`.
  - Reads emit no events.
  - This is a sibling of `/api/signals`: `signals.Look` stays untouched, and its header forbids
    capacity arithmetic.
  - It also needs the openapi entry, `apicli`, the client SDK type and method, a conformance test
    that fails against `null`, and RBAC rows.
- **A Stats page in both UIs**, `/stats`, with a nav entry:
  - **Nodes:** per node, disk, memory and load as bars against their thresholds, a 1-hour
    sparkline, and a pressure badge.
  - **Stacks:** CPU and memory per stack, sorted by memory, linking to the deployment.
  - **Last prune** per node and how much it freed.
  - It works on compose hosts, with one node.
  - Hearth uses `HStatCard`, `HProgress` and `HSparkline`. The advanced UI gets plain CSS bars (it
    has no chart components). Each UI follows its own rules (docs/ui-rules.md).
  - The basic UI is out of scope.
- **Settings view:** the five new keys.

## Rules this touches (for the pre-flight scan)

- Invariant 10: stats and pressure state live in memory, never in a table.
- Invariant 11: unknown is `null`, never `0` or `false`.
- Invariant 12: the agent's lifecycle is init and upgrade only.
- Invariant 14: events are added only.
- Invariants 20/21: no new marker in the control template.
- Go rules: rule 1 (structs or `jsonx`); rule 3 (non-nil slices); rule 14 (`statsLoop` and the
  prune runner listed, each with `recover`, no emit while holding a lock); rule 17 (negative
  controls); rule 18 (`PSTACK_AGENT_IMAGE` and `PSTACK_STATS_TICK_MS` are env-only, like the other
  `serve` knobs).
- Init and upgrade transcripts and the cloud-init and swarm-join goldens change. Regenerate them and
  read the diff.
- Tests:
  - The agent script is tested against `/proc` and `df` fixtures through a root-path override.
  - The manager's parsing is tested against log fixtures.
  - Guards are tested with fake batches through `exec.NewFake`.
  - Conformance uses its own `STATS_SHIM`.
- No browser check is skipped: both pages go through `pstack-ui-verify` at 1280 and 320 px, in light
  and dark.

## Settled (owner, 2026-10-28)

1. **Refusal on swarm:** refuse only when no node has room for new deployments.
2. **Task history limit:** stays at 5.
3. **Log caps:** in this build. Traefik gets the smallest cap.
