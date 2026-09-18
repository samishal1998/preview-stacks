# Loki logging — a design in three slices

> **Slices 1 (`--logging loki`) and 2 (Loki settings) are built (Unreleased); slice 3 is not.**
> Using it: [usage.md](usage.md), `pstack logging`. Slice 1's spec below is kept as approved section
> by section on 2026-09-14. Where the build differs from it:
>
> - The compose plugin check fails with one sentence; the install line is in the job log, because a
>   step message is cut at 300 runes.
> - `/api/swarm` carries a per-node `lokiPlugin` on every host (`null` when logging is off);
>   `lokiPluginInstall` only when logging is on. The host-fixture golden
>   (`golden/host/expected/swarm.json`) changed for the node field alone.
> - Masked userinfo is `://user:••••@`, the mask `redact` already used.
> - The `no-args` and `unknown-command` goldens changed too: both print the command list.
> - `init` also refuses a re-run that would mint a new push password: running containers' pushes
>   would be refused until redeployed.
> - Re-running the join script on a worker that joined earlier installs the plugin: the step sits
>   before the already-joined exit.
> - `/api/swarm` needed no `openapi.yaml` or `apicli` change: its response is untyped.
> - The plugin checks live in `compose`, the node-plugin reads in `swarm`, not in `inspect`.
> - `pstack logging off` counts a deployment's root `compose.generated.yml` only.
> - `pstack logging off` keeps `LOKI_PUSH_PASSWORD`; `pstack logging loki` reuses it.
>
> Slice 2 (Loki settings) was built from its own spec, task by task in
> [loki-logging-slice-2-plan.md](loki-logging-slice-2-plan.md). Where the build differs from that
> spec:
>
> - Two permission rows cover the three routes: `GET` and `PUT /api/logging` share one, like
>   `/api/domains`.
> - OpenAPI documents no 503 for the two PUTs: no 503 response component exists, and
>   `/api/control/restart` leaves its own out.
> - A one-way or fixed-field conflict answers 409 before an out-of-range field's 400: the merge
>   checks conflicts first.
> - The basic UI prints the job action raw (`loki-apply`); it has no label map.
> - A job has no `by`. Who saved is the transcript's first line (`by <actor>`) and
>   `logging.changed`'s `by`.
> - `loki.Writable` repeats routing's write probe: `internal/loki` does not import `routing`.
> - The apply, resume and boot reconcile are in `api/loki_apply.go`; `routes_logging.go` holds the
>   handlers.
> - `server.go`'s header has no route list, so it did not change.
> - pstack mounts `./loki:/etc/loki` in every mode, not only with logging on.
>   `pstack logging loki|off` leaves pstack's service unchanged; the next `pstack upgrade` recreates
>   pstack once on every host.
> - Nothing reconciles on `pstack logging loki`, since pstack is not recreated: a hand-deleted
>   `control/loki` comes back as defaults until the next pstack restart.
> - Without a writable `control/loki` the PUTs answer 409:
>   `Loki's config.yaml is missing or read-only — run pstack upgrade on the host`.
> - `PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS` and `PSTACK_LOKI_UID` are env only, like every
>   other tuning knob; `serve` has no flags for them.
> - Not fixed: slice 1's `DetectLogging` costs a `docker ps` and an inspect per compose verb, and
>   `GET /api/logging` adds one `ControlRuntime` per 10s poll per open Control page. The
>   `pstack-control_logs` network left by logging off is still not pruned.
>
> Slice 3 records the decisions already taken and gets its own spec before it is built.

## What it is

Loki as a logging option for a host. Turned on, three things happen:

1. The control stack gains a **Loki** container.
2. Every node gets the **Grafana Loki Docker plugin**.
3. Every service pstack deploys gets a **`logging:` block pointing at that Loki** — unless the
   service already has one — labelled `service_name=<stack>-<service>`.

Logs leave each host from the Docker daemon itself and arrive at `https://loki.<domain>` through
Traefik, behind basic auth.

| Slice | Ships | Depends on |
|---|---|---|
| 1 | `--logging loki`: Loki in the control stack, the plugin on every node, injection, push through Traefik. Fixed config. | — |
| 2 | Loki settings in the UI: chunking, retention, filesystem or S3. | 1 |
| 3 | Grafana at `grafana.<domain>`, signed in with pstack accounts. | 1 |

## Decisions (owner, 2026-09-14)

| Question | Decision | Why it was not the alternative |
|---|---|---|
| Where Loki runs | **Control stack**, behind an init flag | A `kind: shared` deployment cannot have a real settings form (config changes by redeploy); an external URL leaves chunking and storage to someone else, which the request asked pstack to own. |
| How logs are collected | **The Loki Docker driver plugin**, injected per service | Named in the request. Alloy (a collector reading json-file logs) avoids the plugin's stop-hang class — kept as the fallback if that ever bites despite the fixed options below. A daemon-wide default log driver would capture Traefik, pstack and Loki itself, and bypass swarm's plugin scheduling. |
| Stream label | `service_name=<stack>-<service>` | Loki and Grafana treat `service_name` as the service identity. Rendered statically by pstack, so it never trips the driver's `,`/`=` label parser. |
| Plugin's local file | `keep-file=false`, capped at 10m × 3 | `keep-file=true` leaves one folder per container ID in the plugin's rootfs **forever** (the protocol has no removal callback) — an unbounded leak outside anything pstack can see. Cost: the logs tab loses output of stopped containers; Loki still has it. |
| How nodes reach Loki | **Through Traefik**: `https://loki.<domain>/loki/api/v1/push`, basic auth | The plugin runs in the host's network namespace, so overlay DNS (`http://loki:3100`) is unreachable from it. The alternative — `<manager-ip>:3100` — needs a provider firewall rule per worker, and pstack manages no firewall. Cost: the credential is visible in `docker inspect`, and Traefik must be up for pushes to land. |
| Retention until slice 2 | **7 days** | The manager's disk is shared with Docker images. |
| Storage choices (slice 2) | **Filesystem or S3-compatible** | GCS/Azure add credential shapes nobody asked for. |
| Reading logs (slice 3) | **Grafana included** | pstack's logs tab stays docker-backed; a LogQL reader in pstack would have to rebuild redaction and the line format. |
| Grafana sign-in (slice 3) | **pstack accounts** | A second password to manage is what SSO was built to remove. |
| Slicing | **Three releases** | Grafana's sign-in is a new auth surface and deserves its own review. |

---

## Slice 1 — `--logging loki`

### Turning it on

- `pstack init --logging loki` (env `PSTACK_LOGGING`, per Go rule 18). Values: `none` (default) and
  `loki`.
- `pstack cloud-init --logging loki` passes the flag through to the rendered `init` call. The
  manager needs no step of its own: `init` installs the plugin.
- On a host that exists: **`pstack logging loki|off`**, modelled on `pstack ui basic|advanced`
  (`upgrade.PlanUISwitch`, `upgrade.go:332`). It re-runs `init` from the saved state — the token,
  the DNS token and the Loki push password travel as env — so nothing is retyped. `-n` prints the
  plan.
- **Survives upgrade.** `upgrade.ReadControlState` detects Loki from the rendered compose
  (`^\s{2}loki:`, the advanced-UI precedent) and reads `LOKI_PUSH_PASSWORD` from `control/.env`.
  `initguard` refuses a plain re-run of `init` that would drop `--logging loki`, like it refuses one
  that drops the challenge.

### Control stack

**No new lines in `templates/control/docker-compose.yml`.** Every existing marker that renders "off"
still leaves a line behind (an empty line, or a comment), so a new marker line would change the
rendered file for every existing host — and the 8 render goldens exist to prove an opt-in changes
nothing. Logging-on instead extends substitutions that already happen:

| What | How |
|---|---|
| the `loki` service | appended to the `#__ADVANCED_UI_SERVICE__` substitution: `AdvancedUIService(ui) + LokiService(...)` |
| Traefik joins the logs network | literal `    networks: [preview-ingress]\n` (first match — Traefik's; pstack's line differs) → `    networks: [preview-ingress, logs]\n` |
| the `loki` volume | literal `volumes:\n  letsencrypt:\n` → adds `  loki:\n` |
| the `logs` network | the file's last block `  preview-shared:\n    external: true\n` → adds `  logs: {}\n` |

Each literal replacement is checked before it is made; a template edit that removes an anchor fails
`init` (and its test) by name instead of silently rendering a Loki that Traefik cannot reach.

The service, rendered only when on:

```yaml
  loki:
    image: grafana/loki:3.7.7
    restart: unless-stopped
    mem_limit: 2g
    # Only Traefik shares this network. Never preview-ingress: Loki has no auth, and every preview
    # container sits on preview-ingress — any of them could read every stack's logs.
    networks: [logs]
    command: ["-config.file=/etc/loki/config.yaml"]
    environment:
      GOMEMLIMIT: 1600MiB
    volumes:
      - ./loki:/etc/loki:ro      # the directory, not the file: a renamed-in file is not seen through a file mount
      - loki:/loki
    healthcheck:
      test: ["CMD", "/usr/bin/loki", "-health"]   # distroless image: no shell, wget or curl
      start_period: 30s
      interval: 30s
      timeout: 10s
      retries: 3
    labels:
      - traefik.enable=true
      - traefik.docker.network=pstack-control_logs
      - traefik.http.routers.pstack-loki.rule=Host(`loki.${DOMAIN}`) && PathPrefix(`/loki/api/v1/push`)
      - traefik.http.routers.pstack-loki.entrypoints=websecure
      # TLS labels follow the challenge, like AcmeRouterLabels: tls=true under DNS-01 (the wildcard
      # covers loki.), plus tls.certresolver under HTTP-01. Getting this backwards orders a
      # separate certificate (DNS-01) or none at all (HTTP-01).
      - traefik.http.routers.pstack-loki.middlewares=pstack-loki-auth
      - traefik.http.middlewares.pstack-loki-auth.basicauth.users=pstack:{SHA}<base64 sha1 of the password>
      - traefik.http.services.pstack-loki.loadbalancer.server.port=3100
      # How deploys find Loki — see Injection.
      - pstack.logging.push-url=https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/loki/api/v1/push
```

- **Push password:** 32 hex characters from `crypto/rand`, generated on the first `init --logging
  loki`, written to `control/.env` as `LOKI_PUSH_PASSWORD` (the file is already 0600 because it holds
  the token). Hex so it never contains `$` (compose interpolation) or a URL-reserved character.
  `{SHA}` because Traefik checks the hash on every push — roughly one per container per second — and
  a 128-bit random secret needs no slow hash.
- **`loki.` is a control hostname.** `RoutingStore.IsControlHostname` (`routing/domains.go:255`)
  gains `loki.` beside `control.` and `api.`, for the primary and every added domain — always, not
  only when logging is on, so a host that turned it off never answers `loki.` with a waking page.
- The push router matches only the push path. Anything else on `loki.<domain>` falls to the wake
  router and is refused as a control hostname. Loki's query API is reachable from nowhere but the
  `logs` network.

### Loki config

`<DATA>/control/loki/config.yaml`, written by `init` (0644 — the image runs as uid 10001), mounted
read-only, holds no credential. Embedded as `templates/control/loki/config.yaml` and added to
`assets.go` by explicit path.

```yaml
auth_enabled: false

server:
  http_listen_port: 3100            # `loki -health` probes localhost:3100/ready

common:
  instance_addr: 127.0.0.1
  path_prefix: /loki                # WAL, compactor, tsdb-shipper dirs — on the volume
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

# storage_config, never common.storage: common.storage takes one backend, and slice 2's
# filesystem→S3 switch needs both configured at once.
storage_config:
  filesystem:
    directory: /loki/chunks

schema_config:
  configs:
    - from: "2024-04-01"            # a fixed past date, never "today": a future date means no active
      store: tsdb                   # schema (Loki stores nothing); a changed date makes data unreadable
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h                 # tsdb requires 24h

ingester:
  chunk_idle_period: 30m
  max_chunk_age: 2h                 # must stay <= querier.query_ingesters_within
  chunk_target_size: 1572864
  chunk_encoding: snappy
  wal:
    replay_memory_ceiling: 1GB      # default 4GB exceeds the 2g container limit

querier:
  query_ingesters_within: 3h

limits_config:
  retention_period: 168h            # 0s would keep forever
  max_query_lookback: 168h
  max_global_streams_per_user: 1000

compactor:
  retention_enabled: true
  delete_request_store: filesystem  # required once retention is on

analytics:
  reporting_enabled: false
```

### The plugin, on every node

One line, the same everywhere:

```sh
arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac
docker plugin inspect loki >/dev/null 2>&1 || docker plugin install \
  grafana/loki-docker-driver:3.7.7-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn
[ "$(docker plugin inspect -f '{{.Enabled}}' loki)" = true ] || docker plugin enable loki
```

- **The inspect guard** because `docker plugin install` is not idempotent (Conflict: "already
  exists"). **The enable** because swarm counts a disabled plugin as missing.
- **`LOG_LEVEL=warn`** because at the default `info` the driver logs its whole option map — the
  push URL with the password — into every node's Docker journal on every container start.
- **Per-architecture tag** because the image has no multi-arch manifest; `latest` was last pushed
  in 2021.
- **Pinned to Loki's version (3.7.7).** `pstack upgrade` never upgrades the plugin: that needs
  `disable --force`, `upgrade`, `enable` and a dockerd restart on each node, which interrupts every
  preview on it. `usage.md` documents doing it by hand.

| Where | Runs it | When it fails |
|---|---|---|
| Manager | `pstack init --logging loki` (and so cloud-init) | `init` fails: under compose every logged service would fail to create |
| New worker | `cloudinit.RenderWorkerCloudInit` (after Docker, before the join) and `swarm.JoinScript` — only when the host's logging is on | the worker still joins (`… \|\| echo "loki plugin not installed: logged services will not run here"`); swarm keeps those services elsewhere |
| Worker that joined before | nothing can: there is no remote exec to workers | the Swarm page and `pstack swarm` flag the node and show the line to run there |

The join material learns whether logging is on the way deploys do (below), so the CLI and the API
print the same thing.

### Injection

A new pure step in `autolabel.MaterializeCompose`, after the routing augmentation and before
`Swarmify` (`autolabel.go:497-505`) — one point for both orchestrators; `Swarmify` already keeps
`logging` (`swarm.go:150`).

**Discovery.** Before injecting, pstack reads the `pstack.logging.push-url` label from the running
control stack's `loki` container — the way `inspect.DetectChallenge` already reads Traefik's flags
(`inspect.go:767`). No container, no injection. So `pstack up` on the host and the API always agree,
with no new setting to keep in sync.

**Every service without a `logging` key** in `compose.file` gets:

```yaml
    logging:
      driver: loki
      options:
        loki-url: https://pstack:<password>@loki.<domain>/loki/api/v1/push
        loki-external-labels: service_name=<stack>-<service>
        loki-relabel-config: "[{action: labeldrop, regex: filename}]"
        loki-retries: "2"
        loki-timeout: 1s
        loki-max-backoff: 800ms
        mode: non-blocking
        keep-file: "false"
        max-size: 10m
        max-file: "3"
```

- `<stack>` is `spec.Stack.Stack` (the compose project / swarm stack name), `<service>` the key under
  `services:`.
- **Every option but the URL and the label is a constant, never a setting.** The retry, timeout,
  backoff and mode values are why it must stay that way: when Loki is
  unreachable the driver holds a node-wide lock while it retries, so `docker stop`/`rm` of *every*
  container on that node waits for the retry loop (open upstream: grafana/loki#2361, #18912).
  `non-blocking` protects the app's stdout, not the stop. Only small retries, timeout and backoff
  bound the wait — to seconds.
- **`filename` is dropped** because it is per container: every redeploy would start new streams,
  and a busy host would reach the 1000-stream limit and have new streams rejected.
- The custom external label replaces the driver's default `container_name={{.Name}}`, whose swarm
  value includes the task ID — the same churn.
- Streams keep `service_name`, the driver's `compose_project`/`compose_service` or
  `swarm_stack`/`swarm_service`, `host`, and `source`.

**Skipped:** any service that has a `logging` key — `json-file` counts — is left alone entirely and
listed in the job log, like a service with its own `traefik.*` labels is today
(`autolabel.go:257-261`). No merging into a user's block: pstack's options could be refused by the
user's driver. Known edges, documented: overlays are applied after the derived file, `extends` is
not resolved, and `<<: *anchor` counts as authored (yamlx expands it).

**Compose-mode stacks now get a derived file even without routing.** Today a compose stack with no
`pstack.routing.*` labels runs from its submitted file (`autolabel.go:475-477, 506-508`). With
logging on, the write gates open whenever a service was injected. Logging off keeps today's
behaviour exactly, and its tests.

**Plugin checks at deploy.**
- **Compose:** before `up`, `docker plugin inspect -f '{{.Enabled}}' loki` must print `true`, or the
  job fails with one sentence and the install line — not dockerd's container-create error.
- **Swarm:** the job log names every node whose `docker node inspect`
  `.Description.Engine.Plugins` has no `Log` plugin named `loki`/`loki:latest`. Swarm's scheduler
  already keeps the service off those nodes; if no node qualifies the stack times out after 180s
  (`readiness.go:69`), and the note is what says why.

**Not injected:** the control stack (it never passes through autolabel — `init.go:391`).
`kind: shared` stacks are injected like any other; a shared stack that must not log to Loki writes
its own `logging:`.

### Failure modes

| What goes wrong | What happens | Designed response |
|---|---|---|
| Loki down (restart, OOM, Traefik down) | Apps run and write normally. A batch is retried twice, then dropped — logs are lost while it is down. | The fixed options bound a `docker stop` to seconds. |
| Plugin missing, compose | The container would fail to create. | The pre-`up` check fails the job with a sentence and the install line. |
| Plugin missing on some swarm nodes | Swarm keeps logged services off them; none qualifies → the stack times out after 180s. | The job log names the nodes; the Swarm page flags them. |
| Service with its own `logging:` | Left alone. | Named in the job log. |
| `pstack logging off` | New deploys get nothing. Running containers still push to `loki.<domain>`, now a 404 — not retried, so logs drop immediately and stops are not delayed. | The command prints how many deployments still carry the driver (their `compose.generated.yml`); each switches on its next deploy, a sleeping one on wake. |
| Push password mismatch (401) | Every batch dropped, not retried. | Only reachable by hand-editing `control/.env`: `upgrade` and `pstack logging` reuse the stored password. Rotation is out of scope — it needs every stack redeployed. |
| Manager disk fills | Loki stops accepting writes at 90%. Previews unaffected. | 7-day retention; `keep-file=false` plus 10m × 3 per container. |
| A preview claims `loki.<domain>` via `pstack.routing.host` | It would receive every node's pushes, password included. | A deploy whose host equals `control.`, `api.` or `loki.` of the primary or an added domain is refused. This also closes the same, pre-existing hole for `control.`/`api.`. |
| Someone reads the password | Visible in `docker inspect` of preview containers and in `compose.generated.yml`. It can only push. | pstack masks URL userinfo (`://user:pass@` → `://user:••••@`) wherever it shows generated compose or container config; the plugin's `LOG_LEVEL=warn` keeps it out of dockerd's journal. |

### Testing

**Go unit tests** — each with its `// negative control:` line:
- Injection: a service without `logging` gets exactly the block above; any `logging` key (incl.
  `json-file`) is untouched and listed as skipped; the label is `service_name=<stack>-<svc>`; no
  discovery label → no injection and no derived file under compose; `Swarmify` keeps the block.
- Password: 32 hex chars; the `{SHA}` label matches; `ReadControlState` reads it back; `initguard`
  refuses a re-run that drops `--logging loki`.
- Control render: logging off is byte-identical to today for all 8 cells; logging on contains all
  four anchor edits; a missing anchor fails by name.
- Plugin line: the architecture mapping, the inspect guard, enable-if-disabled; worker cloud-init and
  `JoinScript` carry it only when logging is on, and its failure never blocks the join.
- Loki config: byte-exact; the schema `from` is the constant.
- `IsControlHostname` includes `loki.`; the reserved-host refusal covers the primary and added
  domains; userinfo masking.

**Conformance** (black-box, against the binary):
- **Opt-in changes nothing:** the 8 render cells, the cloud-init goldens and the swarm-join goldens
  stay byte-identical. Only the help goldens change (the new flag and command), regenerated with
  `bun run gen` and said so in the CHANGELOG.
- **New goldens:** render cells `http01-basic-compose-loki` and `dns01-advanced-swarm-loki` (both TLS
  label forms, both orchestrators) with their init dry runs; `cloud-init --logging loki`; a
  logging-on swarm join; `pstack logging loki -n` and `pstack logging off -n`.
- **Deploys:** the docker shim reports a running control `loki` container with the push-url label;
  PUT + up a compose stack and a swarm stack and assert the derived file's `logging` blocks and a
  user's own block untouched. With the shim reporting no plugin, the job fails with the install line.
- Every new test fails against the null server; pass counts only go up.

**Real host, before the release** — what the shim cannot prove:
1. A throwaway host from `cloud-init --logging loki`; deploy `examples/preview.yml`; query Loki from
   the `logs` network for `{service_name="<stack>-web"}` and see its lines.
2. Add a worker with the join script: the plugin is installed before the join, and a swarm task on
   the worker ships its logs.
3. Stop Loki, then `pstack down` a stack: container stops take seconds, not minutes.
4. `pstack logging off`: stops are not delayed; a redeploy removes the driver.
5. `pstack upgrade`: Loki and its password survive.
6. `journalctl -u docker` does not contain the password.

### Where the change lands

| Area | Files |
|---|---|
| Init and the control stack | `internal/initctl` (option, `LokiService`, the anchor edits, plugin step, `.env` line, config write), `templates/control/loki/config.yaml` (new, embedded), `assets.go` |
| CLI | `internal/cli` — `args.go` (flag + env), the help table, `run.go`, `completion.go`, `initguard.go`; the `logging` command |
| Upgrade | `internal/upgrade` — `ControlState` read-back, `initFlags`/`initEnv`, the logging switch plan |
| Injection | `internal/autolabel` (the step, the compose gates), `internal/inspect` (push-url discovery, plugin checks, node plugins) |
| Swarm and provisioning | `internal/swarm` (`JoinScript`, node plugin flag in `SwarmInfo`), `internal/cloudinit` (worker cloud-init, flag passthrough) |
| Hostnames and masking | `internal/routing/domains.go`, the deploy-time host check, `internal/redact` |
| API, client, UI | the swarm route's node field and `api/openapi.yaml` (+ regenerated `apicli`), `packages/client` types, `apps/ui` Swarm page |
| Tests | Go `_test.go` beside each, `packages/conformance` (`gen/goldens.table.ts`, the shim, new tests, `expected-pass.json`) |
| Docs | `usage.md` (init flag, `pstack logging`, adding a worker, env table, plugin upgrade by hand), `bootstrap.md` (cloud-init flag), `control-plane.md` (logging), `CHANGELOG.md` |

### Out of scope for slice 1

Settings UI, S3, Grafana, a Loki read path in pstack, push-password rotation, plugin upgrades,
installing on workers that joined before, a per-spec opt-out other than writing `logging:`,
multi-tenant Loki.

---

## Slice 2 — Loki settings in the UI (decisions recorded; built)

- **Surface:** a panel on the Control page beside Domains and Certificates; which role may write it
  is decided in slice 2's spec, against the role ladder in `usage.md` §7e. Chunking
  (`chunk_idle_period`, `max_chunk_age`, `chunk_target_size`, `chunk_encoding`), retention, and
  storage: filesystem or S3-compatible (endpoint, region, bucket, path-style, access key, secret).
- **Storage of the settings:** a table in the `sso_providers` shape — config JSON plus a secret
  column; reads return `secretSet`, never the secret; sending back the mask or an empty field keeps
  it.
- **Apply:** render `config.yaml.next` → `loki -verify-config` in a throwaway container
  (`--network none --volumes-from` the Loki container) → rename → restart only Loki → watch
  `/ready`. The pstack container needs a new read-write mount of `<DATA>/control/loki`.
- **Credentials** in an AWS shared-credentials file (0600, uid 10001) named by
  `AWS_SHARED_CREDENTIALS_FILE` — re-read on restart. Not `${VAR}` expansion: `docker restart`
  keeps the environment the container was created with, so a rotated secret would be ignored.
- **Filesystem → S3** appends a schema period with `object_store: s3` and a `from` date after today
  (UTC). Irreversible once active; the filesystem block stays while its data is inside retention.
  The UI says the switch is one-way before it happens.
- **Validation pstack must do itself:** Loki does not range-check idle period, max age or target
  size, nor `max_chunk_age <= query_ingesters_within` (3h). `-verify-config` does not test S3
  reachability or credentials — a wrong bucket passes and fails later at flush, so the apply watches
  Loki's log after `/ready`.
- **Copy:** retention changes are not retroactive; the UI must not promise that shortening it deletes
  existing logs.

## Slice 3 — Grafana at `grafana.<domain>`, signed in with pstack accounts

> Builds on slice 1 as planned in `docs/loki-logging-slice-1-plan.md`, using these names from that
> plan: `initctl.Logging`/`Loki`, `LokiService`, `LokiWiring`, `inspect.LokiPushURL` and its
> `idsByLabel`/`inspectIDs` path, `upgrade.ControlState.Logging`, `upgrade.SwitchLogging`,
> `autolabel.ControlHostname`, `IsControlHostname`, the `logs` network, `LOKI_SHIM`. Slice 2 lands
> first: it mounts `./loki` into pstack's service block in every mode, and its `init` writes the
> Loki config only if absent. Line numbers into `init.go`, `domains.go` and `upgrade.go`
> are from the tree before slice 1, so apply each edit by the text it quotes. Traefik citations are
> tag `v3.6.1` (the pinned `docker-compose.yml:32`) unless marked 3.7.13. Grafana citations are tag
> `v13.2.1`.

### Turning it on

- **No new flag, env var or command: `--logging loki` renders Grafana beside Loki.** The owner
  decided "Grafana included" as the way logs are read (§Decisions, "Reading logs"). Loki's
  query API is on the `logs` network and nowhere else (Slice 1 › Control stack), so without
  Grafana nothing can read Loki.
- `pstack logging loki|off` and `pstack cloud-init --logging loki` turn both on and off.
- **pstack is not recreated by the switch.** Slice 3 adds nothing to pstack's service block. pstack
  finds out about Grafana from docker, the same way it finds Loki (below). Slice 1's `LokiWiring`
  edits only Traefik's networks line, the volumes block and the networks block (plan:954-972).
  Compose leaves pstack running, so running jobs survive: they live in this process's memory
  (`server.go:829-830`, `usage.md:1483`). A render test pins this.
- **Upgrade carries it with nothing new to read back.**
  - `ReadControlState` already reads `Logging = loki` from `^\s{2}loki:` (plan:1797-1853).
  - `initFlags` already passes `--logging loki` (plan:1673-1674).
  - Grafana holds no secret pstack generates. There is no admin password
    (`GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION`).
  - The cookie MAC key is `PSTACK_TOKEN`, which `initEnv` already carries (`upgrade.go:318-322`).
- **initguard: nothing new.** A re-run that drops `--logging loki` is already refused (plan:91,
  plan:1679).
- **Cost.** A slice-1 host gains Grafana on its next `pstack upgrade`: 768m more on the manager and a
  download from grafana.com on first start. The CHANGELOG says so in those words.

### Control stack

**Still no new lines in `templates/control/docker-compose.yml`.** Grafana rides Loki's substitution
and anchors:

| What | How |
|---|---|
| the `grafana` service | the `#__ADVANCED_UI_SERVICE__` substitution (`init.go:370`) becomes `AdvancedUIService(ui) + LokiService(logging, challenge, lokiPassword) + GrafanaService(logging, challenge)` |
| the `grafana` volume | `LokiWiring`'s volume edit (plan:954-972) writes `volumes:\n  letsencrypt:\n  loki:\n  grafana:\n` |
| Traefik on `logs`, the `logs` network | slice 1's edits, unchanged |
| pstack's service block | **unchanged**: no env var, no network, no mount |

The Grafana block cannot collide with slice 1's anchors or its read-back:
- It says `networks: [logs]`, so it cannot match anchor 1 (`    networks: [preview-ingress]\n`,
  plan:100).
- Its `    volumes:` line is indented four spaces, so it cannot match `volumes:\n  letsencrypt:\n`.
- It has no `  loki:` line, so `ReadControlState`'s regex still means "Loki is on".

With logging off, `GrafanaService` returns `""` and `LokiWiring` returns the template unchanged, so
all 8 render cells stay byte-identical.

`initctl.GrafanaVersion = "13.2.1"` lives in `initctl`. `swarm.LokiVersion` is in `swarm` only
because the plugin line needs it, and nothing in swarm uses Grafana.

**Images are pulled by name before `up`.** `init.go:247-250` records that compose does not stop
early on a missing image: the pull fails and the whole control stack goes down, Traefik included.
An upgrade from slice 1 on a host that cannot reach Docker Hub takes exactly that path. So with
`logging == Loki`, two `reqs` entries are appended at `init.go:251-259`, shaped like the
advanced-UI image entry:

```go
lokiImage, grafanaImage := "grafana/loki:"+swarm.LokiVersion, "grafana/grafana:"+GrafanaVersion
reqs = append(reqs,
	req{name: "loki image", assert: "docker image inspect " + swarm.Shq(lokiImage) + " >/dev/null 2>&1 || docker pull -q " + swarm.Shq(lokiImage) + " >/dev/null",
		hint: lokiImage + " could not be pulled — --logging loki needs Docker Hub from this host"},
	req{name: "grafana image", assert: "docker image inspect " + swarm.Shq(grafanaImage) + " >/dev/null 2>&1 || docker pull -q " + swarm.Shq(grafanaImage) + " >/dev/null",
		hint: grafanaImage + " could not be pulled — --logging loki needs Docker Hub from this host"},
)
```

The Loki entry closes the same gap in slice 1. The Loki transcripts change in this slice anyway.

**Init summary.** After slice 1's `  logging   loki at https://loki.<domain> …` line (plan:1056), a
`--logging loki` init prints exactly:

```
  grafana   https://grafana.<domain>
```

#### The service (`GrafanaService`), rendered only when logging is loki

```yaml

  # Grafana, with --logging loki: the reader for Loki. Signed in with pstack accounts (forwardAuth).
  grafana:
    image: grafana/grafana:13.2.1    # not -slim (no bundled plugins), not -distroless (no curl for the health check)
    restart: unless-stopped
    mem_limit: 768m                  # 13.x idles ~330Mi and OOMed under 400Mi (grafana#123017)
    # The logs network ONLY. Never preview-ingress, never a published port: anything that reaches
    # :3000 directly can send X-WEBAUTH-USER and be anyone. Only Traefik and Loki share it.
    networks: [logs]
    volumes:
      - grafana:/var/lib/grafana      # users, preferences, and the Drilldown plugin download
      - ./grafana/datasources:/etc/grafana/provisioning/datasources:ro
    environment:
      GF_SERVER_DOMAIN: grafana.${DOMAIN}
      GF_SERVER_ROOT_URL: https://grafana.${DOMAIN}/
      # Sign-in: Traefik's forwardAuth asks pstack, and pstack's answer is these two headers.
      GF_AUTH_PROXY_ENABLED: "true"
      GF_AUTH_PROXY_HEADER_NAME: X-WEBAUTH-USER
      GF_AUTH_PROXY_HEADER_PROPERTY: username
      GF_AUTH_PROXY_HEADERS: Role:X-WEBAUTH-ROLE
      GF_AUTH_PROXY_AUTO_SIGN_UP: "true"
      # false: no grafana_session of Grafana's own, so pstack decides EVERY request and a pstack
      # sign-out applies on the next one.
      GF_AUTH_PROXY_ENABLE_LOGIN_TOKEN: "false"
      # Never GF_AUTH_DISABLE_LOGIN: it unregisters the proxy client and silently turns sign-in off.
      GF_AUTH_DISABLE_LOGIN_FORM: "true"
      GF_AUTH_DISABLE_SIGNOUT_MENU: "true"
      GF_AUTH_BASIC_ENABLED: "false"
      GF_AUTH_ANONYMOUS_ENABLED: "false"
      GF_USERS_ALLOW_SIGN_UP: "false"
      GF_USERS_ALLOW_ORG_CREATE: "false"
      GF_USERS_AUTO_ASSIGN_ORG_ROLE: Viewer
      # No built-in `admin` row, so a pstack user named admin is a user, not the server admin.
      GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION: "true"
      # Previews on *.${DOMAIN} are same-site with Grafana: SameSite does not stop their POSTs.
      GF_SECURITY_CSRF_ALWAYS_CHECK: "true"
      GF_SECURITY_COOKIE_SECURE: "true"
      GF_SECURITY_DISABLE_GRAVATAR: "true"
      # Off: a WebSocket passes forwardAuth once, at the upgrade, and would outlive a pstack sign-out.
      GF_LIVE_MAX_CONNECTIONS: "0"
      # Off: an Editor could publish log panels to snapshots.raintank.io, outside verify.
      GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"
      GF_ANALYTICS_REPORTING_ENABLED: "false"
      GF_ANALYTICS_CHECK_FOR_UPDATES: "false"
      GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES: "false"
      GF_NEWS_NEWS_FEED_ENABLED: "false"
      GF_PLUGINS_PREINSTALL_AUTO_UPDATE: "false"
      # Default preinstalls with no datasource here. Logs Drilldown (grafana-lokiexplore-app) stays.
      GF_PLUGINS_DISABLE_PLUGINS: grafana-pyroscope-app,grafana-exploretraces-app,grafana-metricsdrilldown-app,grafana-advisor-app
    healthcheck:
      test: ["CMD", "curl", "-fsS", "-o", "/dev/null", "http://localhost:3000/api/health"]
      start_period: 60s
      interval: 30s
      timeout: 5s
      retries: 3
    labels:
      - traefik.enable=true
      - traefik.docker.network=pstack-control_logs
      - traefik.http.routers.pstack-grafana.rule=Host(`grafana.${DOMAIN}`)
      # A deployment routed to grafana.<domain> before this release has a rule of the same length
      # (autolabel's Host(`…`)), and equal priority is a coin toss for who gets the Grafana cookie.
      - traefik.http.routers.pstack-grafana.priority=10000
      - traefik.http.routers.pstack-grafana.entrypoints=websecure
      # TLS follows the challenge, exactly like pstack-loki: tls=true alone under DNS-01 (the wildcard
      # covers grafana.), plus its own certresolver under HTTP-01.
      - traefik.http.routers.pstack-grafana.tls=true
      - traefik.http.routers.pstack-grafana.tls.certresolver=le
      - traefik.http.routers.pstack-grafana.middlewares=pstack-grafana-auth
      # pstack, by service name, over preview-ingress, the network Traefik already reaches pstack on
      # (the advanced UI's nginx dials the same name). pstack does not join `logs`.
      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.address=http://pstack:7878/api/auth/grafana/verify
      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.trustForwardHeader=false
      # EVERY header Grafana reads: header_name plus each GF_AUTH_PROXY_HEADERS value. Traefik deletes
      # a listed header from the client's request unconditionally and passes an unlisted one through.
      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.authResponseHeaders=X-WEBAUTH-USER,X-WEBAUTH-ROLE
      - traefik.http.services.pstack-grafana.loadbalancer.server.port=3000
```

- **`tls.certresolver=le`** is appended in code **only under HTTP-01**, as `LokiService` does
  (plan:893-940).
- **Env values are quoted strings**, because compose refuses a YAML boolean as an environment value.
- **Priority.** Traefik's default priority is the rule's length (`traefik rules-and-priority.md`
  :238-247). autolabel renders `Host(\`<host>\`)` (`autolabel.go:305`), the same length as
  `pstack-grafana`'s rule.
  - `10000` beats any rule length, and the ceiling is MaxInt64−1000.
  - `pstack-loki` needs no priority: its rule adds `&& PathPrefix(…)` (plan:924), so it is always
    longer.
- **Only three forwardAuth fields** (`address`, `trustForwardHeader`, `authResponseHeaders`). Each
  exists in v3.6.1 and 3.7.13.
  - `trustForwardHeader` is a plain `bool` in v3.6.1 (`forward.go:54`) and a tri-state in 3.7.13
    (`forward.go:56`). `=false` is valid on both.
  - `maxResponseBodySize` does not exist in v3.6.1.
  - **No `authRequestHeaders`.** It is hygiene, not a control: pstack is trusted with the whole
    request. In v3.6.1 the filter (`forward.go:361`) runs before the `X-Forwarded-*` headers are set
    (`:372-414`), so adding it later cannot strip them.
- **The Traefik behaviour this relies on, v3.6.1:**
  - **The auth client never follows a redirect** (`forward.go:96-98`, `http.ErrUseLastResponse`).
  - **On a non-2xx, Traefik copies every auth-response header to the client**, `Set-Cookie`
    included (`forward.go:237-262`: `CopyHeaders` at `:240`, `Location` at `:254`). A relative
    `Location` is resolved against the auth address (`:302-303`).
  - **On a 2xx, Traefik runs `req.Header.Del` for each listed header before it copies any**
    (`forward.go:266-272`).
  - **With `trustForwardHeader=false`, `X-Forwarded-Method`, `-Host` and `-Uri` come from the real
    request** (`forward.go:372-380`, `:396-404`, `:406-414`). A client's copies are also deleted at
    the entrypoint, because `X-Forwarded-Method` and `X-Forwarded-Uri` are in `xHeaders`
    (`forwarded_header.go:31-43`, deleted for untrusted clients at `:190-194`). 3.7.13 behaves the
    same (`forwarded_header.go:43-44`, `forward.go:455-456`). A real-host check forges it.
- **No `Name` or `Email` header.** pstack users have no display name. `users.email` is nullable
  with a non-unique index (`migrations.go:134-135`), and Grafana matches a proxy user by email
  before login.

#### The datasource file

`templates/control/grafana/datasources.yaml`, embedded as `pstack.GrafanaDatasources` and added to
`assets.go` by explicit path. `init` writes it to `<DATA>/control/grafana/datasources/loki.yaml`:
- **Mode 0644.** Grafana runs as uid 472, and `write` chmods to exactly that mode
  (`init.go:760-770`).
- **Directory:** `ensureDir(…, noMode)`, beside `loki`.
- **Mounted as a directory, read-only**, for slice 1's reason: a file replaced by rename is not seen
  through a file mount.
- **No credential.**

```yaml
# Written by `pstack init --logging loki`. Grafana re-applies it on every start.
apiVersion: 1
datasources:
  - name: Loki
    type: loki
    uid: loki
    access: proxy            # Grafana's server calls Loki over the logs network; Traefik is not involved
    url: http://loki:3100
    isDefault: true
    editable: false
    jsonData:
      maxLines: 1000
      timeout: 60
```

- **`logs` stays a plain bridge** (`logs: {}`), not `internal`, because Grafana downloads Logs
  Drilldown from grafana.com.
- **`pstack logging off`** recreates the stack with `--remove-orphans` (`init.go:391`). That removes
  the container and keeps the `pstack-control_grafana` volume, so turning logging back on restores
  users and preferences.

### How pstack knows Grafana is on

Slice 1's discovery path, pointed at a different service:

```go
// package inspect (control.go), beside LokiPushURL.
// GrafanaOn reports whether the control project has a `grafana` container. `-a`, as LokiPushURL: a
// Grafana that is restarting is still this host's, and `pstack logging off` removes the container.
func GrafanaOn(r exec.Runner) bool {
	ids, _ := idsByLabel(r, "com.docker.compose.project="+ControlProject)
	for _, raw := range inspectIDs(r, ids) {
		if raw.Config != nil && raw.Config.Labels["com.docker.compose.service"] == "grafana" {
			return true
		}
	}
	return false
}
```

- **Cached, never asked per request.**
  - `Server` gains `grafana atomic.Bool`. Its comment names the writers, Start and reindexLoop, and
    says reads need no mutex.
  - `Start` sets it once, **synchronously**, before `go s.reindexLoop()` (`server.go:802`). The
    listener does not serve until then, so the first `/api/health` is already right.
  - `reindexLoop` refreshes it on every tick, after `s.reindex()` (`server.go:815-826`). It is not
    added to `reindex()` itself, which request paths call (`server.go:678`, `routes_deploy.go:416`).
  - `server.go:29` already imports `inspect`. The cost is two docker calls every 30 s.
- **A switch shows up within 30 s**, with no pstack restart: the health key on the next tick, the nav
  link on the next page load.
- **`G` = `"https://grafana." + s.opts.Domain`**, built from `PSTACK_DOMAIN` (`docker-compose.yml:170`),
  never from a request header.
- **`C` = `baseURL(s.opts.Domain, r)`**, which is `https://control.<domain>` (`http.go:175-178`).

```go
// grafanaOn: this host runs Grafana and has what sign-in needs. Off, both routes 404 and
// /api/health has no `grafana` key.
func (s *Server) grafanaOn() bool {
	return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()
}

func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }
```

### Sign-in

**Where the pstack session is.** `sessionCookie` sets no `Domain` (`http.go:164-170`), so
`pstack_session` is host-only on the host that served the login. For a browser that host is
`control.<domain>`, because the UI calls `/api/…` relatively (`docker-compose.yml:183-187`). It is
never widened: previews on `*.<domain>` would receive it.

**Two new routes, both pre-gate** (`preGate`, `routes_auth.go:71-134`). They sit beside SSO's
routes, for SSO's reason: they run before anyone has a principal.

| Route | Called by | Does |
|---|---|---|
| `GET /api/auth/grafana/verify` | Traefik's forwardAuth, for every request to `grafana.<domain>` | answers 204 with the two headers, or 302, 401, 403 or 404 |
| `GET /api/auth/grafana/start` | a browser on `control.<domain>` | turns a pstack session into a one-time code, or sends the browser to the login page |

Both answer `404 Not found.` (plain text) unless `grafanaOn()`, so a host without Grafana has no
sign-in surface. There is no third route: the callback is a branch of verify.

Every plain-text answer is `http.Error`'s: `content-type: text/plain; charset=utf-8`,
`x-content-type-options: nosniff`, and the body with `\n` appended (`Not found.\n`). Verify's
no-cookie 401 is the exception: `w.WriteHeader(401)`, empty body.

**Route order** in `handle()` (`server.go:864-930`):
1. The service-host refusal (new, below).
2. The wake check (`:870-874`). Verify requests carry `X-Forwarded-Host: grafana.<domain>`, which
   `requestHost` reads (`http.go:198-205`). `IsControlHostname` gains `grafana.`, so `wakeFor`
   steps aside (`server.go:643-649`).
3. `/api/health`, probe, openapi.
4. `preGate` (`:909`): **verify is answered here**.

Verify never reaches `principal()` (`:914`). It reads only its own cookie, never `pstack_session`
and never a bearer. So neither `PSTACK_TOKEN` nor a pstack session sent to `grafana.<domain>` gives
Grafana access.

#### The flow

```
browser → G/d/x                      no __Host-pstack_grafana
  Traefik → verify   X-Forwarded-Uri=/d/x   Sec-Fetch-Mode=navigate   Sec-Fetch-Dest=document
  ← 302 C/api/auth/grafana/start?state=S&next=%2Fd%2Fx
    Set-Cookie: __Host-pstack_grafana_state=S; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=900
browser → C/api/auth/grafana/start?state=S&next=/d/x     (carries pstack_session, if any)
  signed in  ← 302 G/-/pstack/callback?code=K        K parked 60s: {session hash, S, next}
  signed out ← 302 /login?next=%2Fapi%2Fauth%2Fgrafana%2Fstart%3Fstate%3DS%26next%3D%252Fd%252Fx
               … password or SSO … → full navigation back to start → signed in, as above
browser → G/-/pstack/callback?code=K               carries __Host-pstack_grafana_state=S
  Traefik → verify   X-Forwarded-Uri=/-/pstack/callback?code=K
  ← 302 G/d/x
    Set-Cookie: __Host-pstack_grafana=<hash>.<mac>; Path=/; Secure; HttpOnly; SameSite=Lax
    Set-Cookie: __Host-pstack_grafana_state=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0
browser → G/d/x
  Traefik → verify   ← 204  X-WEBAUTH-USER: alice   X-WEBAUTH-ROLE: Editor
  Traefik → grafana:3000 with those two headers (the client's copies deleted)
```

**Each step, and why it looks that way:**

1. **No cookie, top-level navigation.**
   - **A navigation means `Sec-Fetch-Mode: navigate` and `Sec-Fetch-Dest: document`.** An
     `<iframe>` sends `navigate` with `Dest: iframe`. A same-site preview could frame a silent
     sign-in, and its state cookie would overwrite one from a real tab. So a frame gets a 401.
   - **The answer is an absolute 302 to start**, not to `/login`. The UI's router sends a signed-in
     user from `/login` to `/` and drops `next` (`router.ts:79-80`). Start checks the session on the
     server first, so a signed-in user never sees a login page.
   - **`Location` is absolute** because Traefik resolves a relative one against
     `http://pstack:7878`.
   - **The return is path-only:** `sso.SafeNext(u.RequestURI())` (`sso.go:785-790`).
   - **`S = sso.RandomB64URL(32)`** (`sso.go:758-764`).
   - **The state cookie's Max-Age is 900 s.** That outlasts a slowly typed password or an SSO
     consent screen (`SSOStateTTLS` defaults to 300 s per leg).
2. **No cookie, anything else: 401, empty body.**
   - A 302 would send Grafana's `fetch` to `control.<domain>` and a CORS failure.
   - On a 401, Grafana's frontend pings, then navigates the top-level window to `/logout`
     (`public/app/core/services/backend_srv.ts`). That navigation is step 1.
3. **Start.** Every failure answers plain text, because a browser navigated here
   (`routes_auth.go:52-53`).
   - `state` must match `^[A-Za-z0-9_-]{43}$`, or the answer is `400 Sign-in link is invalid.`
   - `next` goes through `SafeNext`.
   - The session is the first entry of `sessionCandidates(r)` (`http.go:147-159`) that
     `SessionUser` resolves.
   - With a session: `K = sso.RandomB64URL(32)`, then `Transient().Set("grafana:"+K, {"session":
     auth.HashToken(c), "state": S, "next": next}, 60)`. This is SSO's store and key-prefix style
     (`routes_auth.go:235-236`, `transient.go:40-47`). If the store fails, the error is logged and
     the answer is `500 Sign-in failed.`
   - With no session: a relative 302 to `/login?next=<start URL, EncodeURIComponent'd>`, the way
     `ssoFailed` redirects (`routes_auth.go:54-58`).
4. **Login returns to start.**
   - **SSO needs no change.** The provider button carries `next` (`LoginView.vue:58`), and the SSO
     callback server-redirects to `SafeNext(next)` (`routes_auth.go:444`).
   - **A password login needs one edit per UI.**
     - The advanced UI's `router.replace(next)` (`LoginView.vue:75`) is a client-side route, so
       `/api/…` would render NotFound.
     - The basic UI's `signIn()` ignores `next` (`ui/index.html:1679-1688`).
     - **Rule for both:** a `next` that starts with `/api/` gets a full `location.assign`.
5. **Callback, inside verify.** Verify answers any `X-Forwarded-Uri` whose path is
   `/-/pstack/callback` itself. Traefik relays the 302 and both `Set-Cookie`s, the request never
   reaches Grafana, and no second router is needed.
   - **`Take` runs first** (`transient.go:60-63`, one statement). Any presentation burns the code,
     including one that then fails the state check.
   - **The parked state must equal `__Host-pstack_grafana_state`**, compared with
     `subtle.ConstantTimeCompare`.
   - **Any failure (unknown, expired, replayed, wrong browser) restarts sign-in** at step 1 with
     `next=/`. A login-CSRF victim ends up signed in as themselves.
   - **A reloaded callback URL** from a browser that is already signed in gets a 302 to `G/`.
6. **Every request.** Verify checks, in order:
   - **The MAC** (`hmac.Equal`).
   - **The account:** `auth.SessionHashUser(hash)`, one statement.
   - **The role map.** An unrankable role gets `403 No Grafana access.`
   - **The Origin rule.** When `X-Forwarded-Method` is not GET or HEAD, `Origin` must equal `G`, or
     the answer is `403 Cross-origin request refused.` A request with no `X-Forwarded-Method`
     (a direct call, not Traefik) fails this rule too, so the check fails closed.
   - **Then 204** with the two headers.

#### The Grafana cookie: derived, no table

`__Host-pstack_grafana = <h>.<m>`:
- `h` is the parent session's stored `id_hash` (`auth.HashToken(cookie)`, `auth.go:958`).
- `m` is `base64url(HMAC-SHA256(PSTACK_TOKEN, "pstack-grafana\n" + h))`.

This follows the share-link precedent: signed with `PSTACK_TOKEN`, nothing stored (`control-plane.md`
§5e "Share links", `principal.go:39-42`). **The parent session's row is the Grafana session.** The
prefix keeps it apart from share JWTs, whose signing input starts with `eyJ`, so neither signature
can stand in for the other.

| Event | Grafana access | Why |
|---|---|---|
| pstack sign-out (`routes_auth.go:89-96` → `Logout`, `auth.go:485-488`) | ends on the next request | the join finds no row |
| password change (`SetPassword` deletes sessions, `auth.go:418-435`) | ends | same |
| account deleted (`DeleteUser`, cascade; `foreign_keys(ON)`, `store.go:76-79`) | ends | same |
| session expires (30 days, `auth.go:76-77`) | ends | `s.expires_at > now` in the query |
| role changed (`SetRole`, `auth.go:379-402`) | new role on the next request | the role is read fresh. Grafana re-syncs because the header value is part of its proxy cache key (`proxy.go#L305-L319`) |
| `PSTACK_TOKEN` rotated by re-running `init` | every Grafana cookie fails the MAC | transparent re-sign-in while the pstack session lives |
| SSO-created user | nothing special | `SsoSignIn` mints the same session row (`auth.go:3-16`, `control-plane.md` §5f) |

- **"The next request" is literal.** Grafana Live is off (`GF_LIVE_MAX_CONNECTIONS=0`; `0` disables
  it, `conf/defaults.ini:2237-2241`), so no WebSocket outlives the check. A request already in
  flight still finishes, bounded by the datasource's 60 s timeout.
- **`h` reaching Grafana is not a pstack credential.** The Grafana container sees `h` in the
  forwarded `Cookie` header. Nothing accepts a hash: `principal` and `Logout` hash whatever they are
  given (`principal.go:45-49`, `auth.go:485-488`).
- **The Grafana cookie authorizes verify and nothing else.**
- **Cookie mechanics.**
  - **No Max-Age.** The lifetime is the parent session's, checked on every request.
  - **`__Host-`.** A browser keeps such a cookie only host-only, `Secure` and `Path=/`, so a preview
    on `*.<domain>` cannot plant either name.
  - **Always `Secure`**, unlike `sessionCookie`'s `x-forwarded-proto` check (`http.go:164-170`).
    A browser drops a `__Host-` cookie without it, and Grafana is only reached over TLS.
  - **Read with `r.Cookie(name)`.** `__Host-` makes a same-name duplicate impossible, which is the
    problem `sessionCandidates` loops over.

**Performance.** Each Grafana request, static assets included, costs one HMAC-SHA256 and one
statement:
- The statement is a primary-key lookup on `sessions` (`id_hash TEXT PRIMARY KEY`,
  `migrations.go:22-28`) joined to `users` by primary key.
- It runs no argon2 (only `Login` does, `auth.go:461`) and no write, unlike `TokenUser`'s
  `last_used_at` (`auth.go:527`).
- Start does one `Set` and the callback one `Take`.
- **The ceiling is the single pooled SQLite connection** (`store.go:84`, Go rule 16). Verify waits
  for it behind any open `Store.Tx`, such as `SsoSignIn` (`auth.go:773`) or a host-variable import
  (`hostvars.go:132`). A CLI write in another process adds up to `busy_timeout` (5 s, `store.go:79`).
- A slow answer delays the page and never opens it. The code carries
  `// ponytail: one SQLite statement per Grafana request on the single pooled connection; if page
  loads stall behind transactions, cache hash→user for a few seconds (revocation then lags by that TTL).`

#### Roles

| pstack | Grafana (default org) |
|---|---|
| viewer | **403** — the owner's decision (2026-09-15): a Grafana Viewer still queries Loki's unredacted lines |
| developer | Editor |
| maintainer | Editor |
| admin | Admin |
| anything else | **403**, never an omitted header: Grafana silently keeps a user's old role when the header is missing or invalid |

- **Only a browser session has a Grafana identity.** Root (`PSTACK_TOKEN`) and personal tokens do
  not, because verify never calls `principal()`.
- **There is no Grafana server admin.** The proxy never sets `IsGrafanaAdmin`
  (`user_sync.go#L550-L553`), and no initial admin is created. Plugin installs and server settings
  are unavailable in Grafana's UI, deliberately, and usage.md says so.
- **Grafana login = pstack username** (`^[a-z0-9][a-z0-9._-]{1,31}$`, `auth.go:79`).
  - It never contains `@`, so `header_property=username` never also sets an email.
  - `admin` collides with nothing, because no built-in `admin` row exists.
- **A deleted-then-recreated username** inherits the old Grafana user row: preferences, stars, and
  the dashboards it owns. Documented.
- **pstack viewers are refused.** A Grafana Viewer is not a read-only log view: in OSS every Viewer
  holds `datasources:query` on all datasources (`pkg/api/accesscontrol.go:94-113`), so any panel and
  `/api/ds/query` run LogQL over Loki's unredacted lines. pstack shows viewers redacted logs only
  (`permissions.go:193`, invariant 15), so a viewer gets **403** at verify and never a Grafana
  identity. The pstack UI shows the Grafana link to developer and above only.

#### Sign-out

There is no Grafana-only sign-out. `GF_AUTH_DISABLE_SIGNOUT_MENU=true`, and **pstack's sign-out is
the sign-out**: it ends Grafana access on the next request.

Verify does not intercept `/logout`. That path arrives only from Grafana's 401 handling, and the
chain resolves itself:
1. It is a top-level navigation, so sign-in starts.
2. With a live pstack session, the browser comes straight back.
3. Grafana's own `/logout` redirects to `/login`.
4. The proxy authenticates, and Grafana redirects to `/`, or to the page its `redirect_to` cookie
   remembers.

Grafana has no session of its own to clean up (`ENABLE_LOGIN_TOKEN=false`).

#### The code

`packages/pstack/internal/api/routes_grafana.go` (new):

```go
// __Host- names: a browser keeps one only host-only, Secure and Path=/, so a preview on *.<domain>
// cannot plant either (cookie tossing).
const (
	grafanaSessionCookie = "__Host-pstack_grafana"
	grafanaStateCookie   = "__Host-pstack_grafana_state"
	// Answered INSIDE verify: forwardAuth relays a 302 and its Set-Cookie verbatim, so the callback
	// needs no router of its own and never reaches Grafana.
	grafanaCallbackPath = "/-/pstack/callback"
)

// A lookup table, never ranged into output (rule 5). A role missing here is refused, not defaulted.
// No auth.Viewer: viewers are refused (owner, 2026-09-15) — a Grafana Viewer can still query Loki's
// unredacted lines through panels and /api/ds/query.
var grafanaRoles = map[auth.Role]string{auth.Developer: "Editor", auth.Maintainer: "Editor", auth.Admin: "Admin"}

var grafanaStateRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// grafanaOn: this host runs Grafana (s.grafana, from docker) and has what sign-in needs.
func (s *Server) grafanaOn() bool {
	return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()
}

// grafanaURL is G. From config, never from a request header.
func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }

// hostCookie is a __Host- Set-Cookie. Secure ALWAYS, unlike sessionCookie: a browser drops a __Host-
// cookie without it, and Grafana is only reached through Traefik's TLS. maxAge < 0: none.
func hostCookie(name, value string, maxAge int) [2]string {
	v := name + "=" + value + "; Path=/; Secure; HttpOnly; SameSite=Lax"
	if maxAge >= 0 {
		v += "; Max-Age=" + strconv.Itoa(maxAge)
	}
	return [2]string{"set-cookie", v}
}

func (s *Server) grafanaMAC(hash string) string {
	m := hmac.New(sha256.New, []byte(s.opts.Token))
	m.Write([]byte("pstack-grafana\n" + hash))
	return js.B64URL(m.Sum(nil))
}

// grafanaUser is the account behind this browser's Grafana cookie, or nil. The parent session's row
// IS the Grafana session: delete it and this answers nil on the very next request.
// ponytail: one SQLite statement per Grafana request on the single pooled connection; if page loads
// stall behind transactions, cache hash→user for a few seconds (revocation then lags by that TTL).
func (s *Server) grafanaUser(r *http.Request) *auth.UserRow {
	c, err := r.Cookie(grafanaSessionCookie)
	if err != nil {
		return nil
	}
	hash, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(s.grafanaMAC(hash))) {
		return nil
	}
	u, _ := s.auth.SessionHashUser(hash)
	return u
}

// grafanaVerify is Traefik's forwardAuth for grafana.<domain>. Every Location is absolute and built
// from config: Traefik resolves a relative one against http://pstack:7878.
func (s *Server) grafanaVerify(w http.ResponseWriter, r *http.Request) {
	if !s.grafanaOn() {
		http.Error(w, "Not found.", 404)
		return
	}
	u, err := url.ParseRequestURI(r.Header.Get("x-forwarded-uri"))
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	user := s.grafanaUser(r)
	if u.Path == grafanaCallbackPath {
		code, _ := query(u.RawQuery, "code")
		if s.grafanaCallback(w, r, code) {
			return
		}
		if user != nil { // a reloaded callback: nothing to finish
			redirect(w, s.grafanaURL()+"/")
			return
		}
		u = &url.URL{Path: "/"} // expired, replayed or not this browser's: sign in afresh
	}
	if user == nil {
		// A top-level navigation only. fetch gets 401 (a 302 would be a CORS error in Grafana's
		// frontend), and so does an <iframe>: a same-site preview must not frame a silent sign-in.
		if r.Header.Get("sec-fetch-mode") != "navigate" || r.Header.Get("sec-fetch-dest") != "document" {
			w.WriteHeader(401)
			return
		}
		state := sso.RandomB64URL(32)
		redirect(w, baseURL(s.opts.Domain, r)+"/api/auth/grafana/start?state="+state+
			"&next="+js.EncodeURIComponent(sso.SafeNext(u.RequestURI())),
			hostCookie(grafanaStateCookie, state, 900))
		return
	}
	role, ok := grafanaRoles[auth.Role(user.Role)]
	if !ok {
		http.Error(w, "No Grafana access.", 403)
		return
	}
	// Every preview on *.<domain> is SAME-SITE with Grafana: SameSite=Lax does not stop its page
	// posting here with the cookie attached. Traefik sets X-Forwarded-Method from the real request.
	if m := r.Header.Get("x-forwarded-method"); m != http.MethodGet && m != http.MethodHead && r.Header.Get("origin") != s.grafanaURL() {
		http.Error(w, "Cross-origin request refused.", 403)
		return
	}
	w.Header().Set("X-WEBAUTH-USER", user.Username)
	w.Header().Set("X-WEBAUTH-ROLE", role)
	w.WriteHeader(204)
}

// grafanaCallback finishes a sign-in. Take FIRST: any presentation burns the code, including one
// that fails the state check, so a code read from Traefik's access log is already spent.
func (s *Server) grafanaCallback(w http.ResponseWriter, r *http.Request, code string) bool {
	if code == "" {
		return false
	}
	raw, found, err := s.auth.Transient().Take("grafana:" + code)
	if err != nil || !found {
		return false
	}
	parsed, _ := omap.Parse([]byte(raw))
	parked, _ := parsed.(*omap.Map)
	state, _ := getStr(parked, "state")
	session, _ := getStr(parked, "session")
	next, _ := getStr(parked, "next")
	c, err := r.Cookie(grafanaStateCookie)
	if err != nil || state == "" || session == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		return false
	}
	redirect(w, s.grafanaURL()+sso.SafeNext(next),
		hostCookie(grafanaSessionCookie, session+"."+s.grafanaMAC(session), -1),
		hostCookie(grafanaStateCookie, "", 0))
	return true
}

// grafanaStart runs on control.<domain>, where pstack_session lives: it turns that session into a
// 60-second, single-use code bound to the state cookie Grafana's host set. A browser navigated here,
// so every failure is plain text (the ssoFailed rule, routes_auth.go:52-53).
func (s *Server) grafanaStart(w http.ResponseWriter, r *http.Request) {
	if !s.grafanaOn() {
		http.Error(w, "Not found.", 404)
		return
	}
	state, _ := query(r.URL.RawQuery, "state")
	if !grafanaStateRe.MatchString(state) {
		http.Error(w, "Sign-in link is invalid.", 400)
		return
	}
	next, _ := query(r.URL.RawQuery, "next")
	next = sso.SafeNext(next)
	for _, c := range sessionCandidates(r) {
		if u, _ := s.auth.SessionUser(c); u != nil {
			code := sso.RandomB64URL(32)
			parked := jsonx.Must(jsonx.O("session", auth.HashToken(c), "state", state, "next", next))
			if err := s.auth.Transient().Set("grafana:"+code, string(parked), 60); err != nil {
				s.opts.Log("[grafana] sign-in code not stored: " + err.Error())
				http.Error(w, "Sign-in failed.", 500)
				return
			}
			redirect(w, s.grafanaURL()+grafanaCallbackPath+"?code="+code)
			return
		}
	}
	redirect(w, "/login?next="+js.EncodeURIComponent("/api/auth/grafana/start?state="+state+"&next="+js.EncodeURIComponent(next)))
}
```

**Edits outside the new file:**
- **`preGate`** (`routes_auth.go:125-132`) gains two `case`s: `path == "/api/auth/grafana/verify" &&
  GET` and `path == "/api/auth/grafana/start" && GET`.
- **`auth.SessionUser`** (`auth.go:477-482`) becomes
  `func (a *Auth) SessionUser(session string) (*UserRow, error) { return a.SessionHashUser(sha256Hex(session)) }`.
  Its query moves unchanged into `SessionHashUser(idHash string)`, whose comment says only the
  Grafana cookie reaches it.
- **`Server`** gains `grafana atomic.Bool`. `Start` stores `inspect.GrafanaOn(s.host)` before
  `go s.reindexLoop()` (`server.go:802`), and `reindexLoop`'s tick does the same after `s.reindex()`
  (`server.go:823`).
- **`/api/health`** (`routes_auth.go:36-43`) appends `jsonx.KV{K: "grafana", V: s.grafanaURL()}`
  (`jsonx.go:94-100`) **only when `grafanaOn()`**.
  - Off, the key is absent (Go rule 2: genuinely absent). `golden/host/expected/health.json` and
    every older host's answer stay byte-identical.
  - The route is unauthenticated, and the key reveals nothing DNS does not.
  - `packages/client/src/types.ts` `Health` (`:56-66`) gains `grafana?: string`.
- **`server.go`'s header comment** (`:5-9`, the route list and the pre-gate list) names both routes.

### Hostnames and the wake handler

- **`IsControlHostname`** (`domains.go:249-259`, as slice 1 leaves it, plan:6304-6316) gains
  `|| h == "grafana."+d`, **always**, for the primary and every added domain. That one clause
  covers:
  - the wake exclusion (`server.go:643-649`);
  - the deploy-time refusal of a `pstack.routing.host` naming `grafana.` (slice 1's
    `autolabel.ControlHostname` seam, plan:6216-6218);
  - the host refusal below.

  The header comment names `grafana.` beside `loki.`.
- **A service hostname never gets pstack's UI or API.**
  - When the Grafana container is stopped, starting or unhealthy, Traefik's docker provider drops
    its router. `grafana.<domain>` then matches `pstack-wake` (`init.go:643-655`).
  - Today `handle()` would then serve the embedded UI and the whole API there (`server.go:877-889`).
  - A password typed on that page sets `pstack_session` on `grafana.<domain>`, and every later
    Grafana request would forward it to Grafana.
  - On an added domain, `grafana.<d>` always lands here, because `domains.go:234-239` routes `*.<d>`
    to pstack and Grafana is served on the primary only.

  So this is the **first** check in `handle()`:

  ```go
  // A service hostname that reached THIS process came through the wake catch-all: its own router is
  // gone (container stopped, starting or unhealthy) or, on an added domain, never existed. r.Host,
  // not requestHost: forwardAuth's calls carry X-Forwarded-Host grafana.<domain> but Host pstack:7878.
  if h := strings.ToLower(portRe.ReplaceAllString(r.Host, "")); (strings.HasPrefix(h, "grafana.") || strings.HasPrefix(h, "loki.")) && s.routing.IsControlHostname(h, s.opts.Domain) {
  	switch d := strings.ToLower(s.opts.Domain); h {
  	case "grafana." + d:
  		http.Error(w, "Grafana is not running.", 503)
  	case "loki." + d:
  		http.Error(w, "Loki is not running.", 503)
  	default: // an added domain's: never served
  		http.Error(w, "Not found.", 404)
  	}
  	return
  }
  ```

  - **The prefix test comes first**, because `IsControlHostname` reads the domains file
    (`domains.go:99-104`).
  - **`loki.` is included** because it is the same fall-through. Slice 1 says a request there "is
    refused as a control hostname", and this check is what makes that true. A push that meets the
    503 is retried twice and dropped, instead of reading pstack's `200` HTML as delivered.

### The UI entry point

- **`apps/ui`, reading.** `useAuth.ts` gains `grafana: null as string | null` in `authState`
  (`:25-30`), and `authState.grafana = health.body.grafana ?? null` beside `hasUsers` (`:71-76`).
- **`apps/ui`, the link.** `App.vue` gets one nav link after Jobs (`:146-150`). `settings` and `can`
  are already imported (`:18-19`). `ScrollText` is added to the `lucide-vue-next` import list
  (`:23-40`).
  - **It is hidden when the SPA authenticates with a stored bearer**: `settings.token`, sent as
    `Authorization` (`client.ts:89`) and kept in localStorage (`useSettings.ts:46-49`). That session
    has no `pstack_session` (`useAuth.ts:9-12`), so start would bounce to `/login`, and the router
    would send an authed visitor to `/` (`router.ts:79-80`).
  - **Developer and above only** (`can('developer')`, as Submit's link at `:152`): verify refuses
    viewers.

  ```vue
  <a v-if="authState.grafana && !settings.token && can('developer')" :href="authState.grafana" class="navlink" target="_blank" rel="noopener">
    <ScrollText :size="17" aria-hidden="true" />
    <span>Grafana</span>
  </a>
  ```

- **`apps/ui`, login return.** `LoginView.vue:75` becomes:

  ```ts
  if (next.value.startsWith('/api/')) window.location.assign(api.url(next.value));
  else void router.replace(next.value);
  ```

- **Basic UI** (`ui/index.html:1676-1688`). No link, the same call slice 1 made for its swarm panel
  (plan:94). Its sign-in does honour `next`:
  - `signIn()`: after `r.ok`, `const next = new URLSearchParams(location.search).get('next') || '';
    if (next.startsWith('/api/')) { location.assign(next); return; }`.
  - `signInWithProvider()` passes that `next` when it starts with `/`.

### Failure modes

| What goes wrong | What happens | Designed response |
|---|---|---|
| pstack down or restarting | forwardAuth's call fails, so every Grafana request gets Traefik's 500 | Fail closed; nothing bypasses verify. The same outage takes `control.<domain>` down. |
| `pstack logging loki` or `off` while jobs run | pstack's service block is unchanged, so compose does not recreate pstack | Jobs survive. The health key follows within 30 s (reindexLoop); the nav link on the next page load. A render test pins pstack's block. |
| Grafana stopped, starting, unhealthy | Traefik drops its router, and `grafana.<domain>` reaches `pstack-wake` | `503 Grafana is not running.`, never pstack's UI or API |
| `grafana.<d>` on an added domain | the added domain's wake router sends it to pstack | `404 Not found.` |
| A deployment already routed to `grafana.<domain>` before upgrade | its `Host(…)` rule ties `pstack-grafana`'s on length | `priority=10000` gives pstack the tie. Its next deploy is refused (plan:6216-6218). CHANGELOG line. |
| Loki down | sign-in works; queries show Grafana's datasource error | Nothing new (slice 1's row) |
| No Docker Hub egress at `init` / `upgrade` | the image cannot be pulled | `init` fails at `requires grafana image`, by name, before anything is recreated |
| No grafana.com egress on Grafana's first start | Logs Drilldown is not installed; one error line | Explore still works. Known cost; an offline preinstall is out of scope. |
| pstack session ends (sign-out, password change, account deleted, 30 days) | the next Grafana request fails the join | 401 or 302 → pstack login → back to the page |
| An Explore tab left open after revocation | there is no WebSocket to keep streaming (Live off) | the next query gets 401 |
| Role changed | the next request carries the new header | Grafana re-syncs the default-org role |
| Hand-edited, unrankable role | — | 403, fail closed (`auth.go:119-122`) |
| `PSTACK_TOKEN` rotated | every Grafana cookie fails the MAC | transparent re-sign-in |
| A long `Store.Tx` or a CLI write | verify waits for the one SQLite connection (`store.go:84`) | a slower page load; nothing fails open |
| Clock skew | not applicable | every expiry is compared on pstack's clock (`transient.go:41-62`, `auth.go:471-481`); cookies use relative Max-Age; Grafana keeps no session |
| Cookie tossing: a preview sets `Domain=<domain>` cookies | a browser refuses `__Host-` names with `Domain`; unprefixed look-alikes are never read | `__Host-` names only |
| Login CSRF: a victim opens an attacker's callback URL | `Take` burns the code; state mismatch | the victim restarts into their own sign-in |
| A preview frames `grafana.<domain>` | `Sec-Fetch-Dest: iframe` | 401, no state cookie, no code |
| A code leaks (Traefik's access log records the URI, `docker-compose.yml:100`) | the victim's browser presents it first and burns it | single use, 60 s, bound to that browser's state cookie |
| Open redirect | `next` is `SafeNext`'d at verify and at start; every `Location` is `C` or `G` from config plus a path | `X-Forwarded-Host` is never read for a URL |
| A preview page POSTs to Grafana (same-site) | `Origin` ≠ `G` | 403 in verify; `CSRF_ALWAYS_CHECK` in Grafana behind it |
| A client forges `X-Forwarded-Method: GET` on a POST | the entrypoint deletes it, and forwardAuth sets it from `req.Method` (v3.6.1 `forwarded_header.go:190-194`, `forward.go:372-380`) | the Origin rule sees POST; real-host check |
| A client sends `X-WEBAUTH-USER: admin`, or an alias `X_WEBAUTH_USER` / `X.WEBAUTH.USER` | Traefik deletes the listed names; Grafana's `Header.Get` never reads an alias | render test on the label; real-host probe of both aliases |
| A preview container dials `grafana:3000` | not on `logs`; no published port | `networks: [logs]` only |
| A spec declares `pstack-control_logs` external, or aliases `pstack` on preview-ingress | could reach Grafana, or answer verify | Out of the threat model: a spec is CI-trusted (`AGENTS.md:41`, plan:86). Same exposure as nginx dialling `pstack` (`apps/ui/nginx.conf:49`). |
| `verify` called directly at `api.<domain>` | answers about the caller's own cookies only | no escalation; without `X-Forwarded-Method` an unsafe-method check fails closed |
| Two Grafana tabs start sign-in at once | the later state cookie overwrites the earlier | the first callback restarts; converges once one sets the session cookie |
| SPA signed in with a stored bearer | no `pstack_session` | nav link hidden; a direct visit to `grafana.<domain>` ends on pstack's home |
| Browser blocks cookies | redirect loop until the browser gives up | none: Grafana needs cookies |
| `pstack logging off` | Grafana container removed, volume kept; the health key goes within 30 s; the link on the next page load | turning it back on restores Grafana's users and preferences |

### Security — where each requirement lands

| Requirement | Lands in |
|---|---|
| Grafana and Loki's query API only on `logs`; no published port | `GrafanaService`: `networks: [logs]`, no `ports:`; tested |
| Every header Grafana reads is in `authResponseHeaders` | the label + `TestGrafanaService/Traefik_strips_every_header_Grafana_reads` |
| **Header aliases cannot impersonate.** v3.6.1 has no alias handling at all (no `aliasHeadersStrategy`; 3.7.13 warns about aliases, `forward.go:315-318`) | Grafana reads each proxy header with Go's `Header.Get` (`authn/clients/proxy.go` `getProxyHeader`), which looks up the canonical key `X-Webauth-User`. Go canonicalizes `X_WEBAUTH_USER` to `X_webauth_user` and folds neither `_` nor `.`, so an alias is a different header Grafana never reads. Real-host step 3 probes both aliases. |
| Login form off, basic auth off, no initial admin, never `disable_login` | the env block; a test asserts `GF_AUTH_DISABLE_LOGIN:` is absent |
| `CSRF_ALWAYS_CHECK` plus an Origin check for unsafe methods, on a method pstack can trust | env; `grafanaVerify`; v3.6.1 cites above; real-host forged-method check |
| `__Host-` cookies; code bound to the state cookie; top-level navigations only | `hostCookie`, `grafanaCallback`, the `Sec-Fetch-Dest` check |
| Absolute `Location` from the configured domain | `baseURL(s.opts.Domain, r)`, `grafanaURL()` |
| `grafana.` in `IsControlHostname` and the reserved-host refusal; pstack's router wins the host | `domains.go`, one clause; `priority=10000` |
| **No stream outlives revocation** | `GF_LIVE_MAX_CONNECTIONS: "0"`; tested |
| **No path off the host for log lines.** Grafana's default publishes external snapshots to `snapshots.raintank.io` (`conf/defaults.ini:582-588`) | `GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"`; tested. Local snapshots and public dashboards (`defaults.ini:2475-2477`) stay behind verify, because every request to `grafana.<domain>` passes it. |
| **Grafana lines are not redacted.** pstack's logs route and stream redact `PSTACK_TOKEN` and host secret values (`routes_deploy.go:645-650`, `sse.go:220`). Loki stores the driver's raw output, and Grafana serves it as stored. | Every Grafana role can read it: Editor and Admin in Explore, Viewer through panels and `/api/ds/query` (Roles). Developer and above gain little: they can already open a shell in those containers (`permissions.go:197`). **A pstack viewer gains unredacted reach**: pstack shows viewers redacted logs only (`permissions.go:193`, invariant 15). `usage.md` says so in the Grafana section. **So viewers are refused (403)** — the owner's decision, 2026-09-15. |

### Testing

**Go unit tests.** Each carries its `// negative control:` line. A plain-text body compares exactly,
with `http.Error`'s trailing `\n`.

- **`internal/api` `TestGrafanaVerify`.** httptest against a server with `Token` and `Domain`,
  `s.grafana.Store(true)`, and a bootstrapped user logged in.
  - **Top-level navigation without a cookie:** 302, `Location` exactly
    `https://control.preview.example.com/api/auth/grafana/start?state=<S>&next=%2Fd%2Fx`, and a
    Set-Cookie carrying the same `<S>` with `Secure`, `HttpOnly`, `SameSite=Lax`, `Max-Age=900`.
    Mutation: a relative Location.
  - **XHR without a cookie** (`Sec-Fetch-Mode: cors`): 401, no `Location`, no `Set-Cookie`.
    Mutation: drop the mode check.
  - **iframe without a cookie** (`navigate` + `Sec-Fetch-Dest: iframe`): 401, no `Set-Cookie`.
    Mutation: drop the dest check.
  - **`X-Forwarded-Host: evil.example`:** `Location` unchanged. Mutation: build it from
    `requestHost`.
  - **`X-Forwarded-Uri: //evil.example/x`:** `next=%2F`. Mutation: skip `SafeNext`.
  - **Roles:** developer, maintainer and admin map as in the table; viewer and a hand-set `superuser`
    get 403. Mutations: a default role for missing keys; restoring a viewer entry.
  - **Spoofed headers:** `X-WEBAUTH-USER: admin` with alice's cookie answers `alice`. Without a
    cookie the answer is 401 with no `X-WEBAUTH-*`. Mutation: copy request headers to the response.
  - **Forged cookie:** a wrong MAC behaves as no cookie. Mutation: skip `hmac.Equal`.
  - **`pstack_session` alone, and `Authorization: Bearer <PSTACK_TOKEN>` alone:** never 204.
    Mutation: fall back to `s.principal(r)`.
  - **Revocation:** after `Logout`, `SetPassword` and `DeleteUser`, the same cookie behaves as no
    cookie. After `SetRole(viewer)` it gets 403. Mutation: cache the user.
  - **Origin:**
    - POST with Origin G → 204.
    - POST with a preview Origin → 403.
    - POST with no Origin → 403.
    - No `X-Forwarded-Method` with a preview Origin → 403.
    - GET with a preview Origin → 204.

    Mutation: compare `Host` instead.
  - **Callback:**
    - A good code plus the state cookie → 302 to `G/d/x`, the session cookie, and the state cookie
      cleared.
    - The same code again → a fresh start redirect and no session cookie.
    - A mismatched state → no session cookie, **and the code is burned**.
    - A code parked with `Set(key, v, 0)` → no session cookie. `expires_at = now`, so `Take`'s
      `expires_at > ?` fails.

    Mutations: `Get` instead of `Take`; compare after the redirect.
  - **Off:** `Domain` or `Token` empty, or `s.grafana` false → `404 Not found.` as plain text.
- **`TestGrafanaStart`:**
  - signed in: an absolute 302 to `G/-/pstack/callback?code=<K>`, and `Transient().Get("grafana:"+K)`
    holds the session hash, the state and the `SafeNext`'d next;
  - signed out: 302 to `/login?next=` with a double-encoded return that decodes back to the start URL;
  - malformed state: `400 Sign-in link is invalid.`, `text/plain`.

  Mutation: `writeError` (JSON) for the 400.
- **`TestServiceHostnamesAreNeverServedByPstack`:**
  - `Host: grafana.preview.example.com` and `Host: loki.preview.example.com`, on `/` and on
    `/api/auth/me`, get 503 with `Grafana is not running.` / `Loki is not running.`;
  - `Host: grafana.added.example` (an added domain) gets `404 Not found.`;
  - verify with `Host: pstack:7878` and `X-Forwarded-Host: grafana.…` is not refused;
  - `Host: control.…` is served.

  Mutations: key on `requestHost`; drop the primary-domain case.
- **`TestHealthNamesGrafanaOnlyWhenOn`:** the key is absent (not null) with `s.grafana` false, and
  equals `https://grafana.preview.example.com` when true. Mutation: always append.
- **`internal/inspect` `TestGrafanaOn`**, with `LokiPushURL`'s fake-host helper (plan:3447-3480):
  - a `grafana` control container → true;
  - only `loki` → false;
  - `docker ps` failing → false;
  - no ids → false.

  Mutation: drop the service-name check.
- **`internal/auth` `TestSessionHashUser`:** `SessionUser(c)` and `SessionHashUser(HashToken(c))`
  return the same row, and an expired session gives nil. Mutation: drop `expires_at > ?`.
- **`internal/initctl`:**
  - `TestGrafanaService`, both challenges:
    - the `tls.certresolver` label appears only under HTTP-01;
    - the block has `networks: [logs]` and no `ports:` or `preview-ingress`;
    - no `GF_AUTH_DISABLE_LOGIN:`;
    - `GF_LIVE_MAX_CONNECTIONS: "0"` and `GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"` are present;
    - `traefik.http.routers.pstack-grafana.priority=10000` is present;
    - **the header names in `GF_AUTH_PROXY_HEADER_NAME` plus each `GF_AUTH_PROXY_HEADERS` value equal
      the set in `authResponseHeaders`**, parsed out of the rendered compose.

    Mutations: add `Email:X-WEBAUTH-EMAIL` to the env only; delete either GF_ line; delete the
    priority label.
  - `TestLokiWiring`, extended:
    - the `grafana:` volume;
    - **the `pstack:` service block is byte-identical with logging off and on.**

    Mutation: add an env line to pstack's block in `LokiWiring`.
  - `TestGrafanaDatasources`: byte-exact.
  - `Init`: writes `grafana/datasources/loki.yaml` at 0644, runs both `requires … image` lines, and
    prints `  grafana   https://grafana.preview.example.com\n`, all only with logging on (as
    plan:1111 asserts slice 1's line).
  - `TestInitGoldens`: logging off is still byte-identical.
- **`internal/routing`:** `grafana.` on the primary, on an added domain, and on a nil store.
  `grafana-pr-1.<d>` is not a control hostname.
- **`internal/autolabel`:** `pstack.routing.host: grafana.preview.example.com` is refused.
- **`internal/upgrade`:** a compose rendered by the real `init --logging loki`, Grafana included,
  still reads `Logging == loki`. That file is also the fixture, per AGENTS.md's "generate the fixture
  with the real init".
- **Route lists:**
  - `openapi_coverage_test.go`'s `notInTheSpec` gains `/api/auth/grafana/verify` ("Traefik's
    forwardAuth, for grafana.<domain>") and `/api/auth/grafana/start` ("a 302 for a browser").
  - `permissions_test.go`'s `preGatePaths` (`:197-201`) gains both.

**Conformance** (black-box, `test/api-grafana.test.ts`). No real Grafana is needed:
- `GRAFANA_SHIM` has two arms, shaped like `LOKI_SHIM` (plan:6744-6747):
  `"ps -aq --filter label=com.docker.compose.project=pstack-control"` prints `gr4f`, and
  `"inspect gr4f"` prints a container whose `com.docker.compose.service` is `grafana`.
- The server boots with `bootServer({ domain: 'preview.example.com', pathPrefix: dockerShim(GRAFANA_SHIM).dir })`
  (`harness/server.ts:34`, `:97`).
- Requests use `redirect: 'manual'` (precedent `api-sso.test.ts:42`).
- The test plays Traefik by sending `X-Forwarded-Uri/Method/Host` and `Sec-Fetch-Mode/Dest` itself.

1. Bootstrap and log in `sami`, keeping `pstack_session`.
2. Verify with no cookie and `navigate` + `document` → 302 to
   `https://control.preview.example.com/api/auth/grafana/start?…` plus a
   `__Host-pstack_grafana_state` cookie. `cors` → 401. `navigate` + `iframe` → 401.
3. `GET` start's path and query with `pstack_session` → 302 to
   `https://grafana.preview.example.com/-/pstack/callback?code=`. Without the session → 302 to
   `/login?next=`.
4. Verify with `X-Forwarded-Uri: /-/pstack/callback?code=…` and the state cookie → 302 to `…/d/x`
   plus `__Host-pstack_grafana`. The same code again → no session cookie.
5. Verify with the session cookie → 204, `x-webauth-user: sami`, `x-webauth-role: Admin`. A POST
   with a foreign Origin → 403.
6. `POST /api/auth/logout` with `pstack_session`, then verify with the Grafana cookie → 401.
7. `GET /` with `host: grafana.preview.example.com` → 503, body `Grafana is not running.\n`.
   - This sends Host through Bun's `fetch`, which `api-share-sleep-swarm.test.ts:173` already relies
     on: it sends only `host`, expects the wake 503, and `requestHost` can read that only from
     `r.Host` (`http.go:198-205`). That test is in `expected-pass.json:14`.
   - The Go httptest case stays the negative control.
8. `/api/health` has `grafana: "https://grafana.preview.example.com"`. A server booted with the
   `ALWAYS_OK` shim has no `grafana` key, and its verify and start answer 404.

Every step asserts a 302, 204, 401, 403, 404 or 503, so each fails against the null server's `200 {}`.

**Goldens:**
- **Byte-identical:**
  - the 8 logging-off render cells, and the cloud-init and swarm-join goldens;
  - `golden/host/**`: no migration, and the health key is absent with no grafana container;
  - the help, no-args and unknown-command goldens: no new flag or command.
- **Changed, regenerated with `bun gen/goldens.ts`, and said in the CHANGELOG:**
  - `render/control/http01-basic-compose-loki/` and `dns01-advanced-swarm-loki/`: `docker-compose.yml`,
    plus the new `grafana/datasources/loki.yaml`;
  - the `init-*-loki`, `init-dry-*-loki`, `logging-loki-dry-dns01-advanced-swarm`,
    `logging-off-dry-*-loki` and `upgrade-plan-*-loki` transcripts, wherever they print the init dry
    run. The diff is only the requires, mkdir, write and `  grafana   https://grafana.<domain>` lines.
  - `cli-goldens.test.ts:9-11`, whose header names the new render file.
- **Ratchet:** pass counts only go up.

**Real host, before the release:**
1. **Sign-in, password.** On a host from `cloud-init --logging loki`, signed out, open
   `grafana.<domain>/explore` → pstack login → back on Explore → `{service_name="<stack>-web"}` shows
   lines, as a maintainer.
2. **Sign-in, SSO.** The same through an SSO provider.
3. **Header spoofing.** Without a cookie, each of these → 401:
   - `curl -H 'X-WEBAUTH-USER: admin' https://grafana.<domain>/api/user`
   - the same with `-H 'X_WEBAUTH_USER: admin'`
   - the same with `-H 'X.WEBAUTH.USER: admin'`

   With a developer's cookie plus each header → the developer. With a viewer's cookie → 403.
4. **Forged method.** With a maintainer's cookie:
   `curl -X POST -H 'X-Forwarded-Method: GET' -H 'Origin: https://pr-1.<domain>' https://grafana.<domain>/api/dashboards/db`
   → 403 `Cross-origin request refused.`
5. **Network isolation.** From a preview container, `curl http://grafana:3000` does not connect.
6. **Sign-out.** Sign out in pstack → the Grafana tab's next request bounces to the login page.
7. **Demotion.** Demote a maintainer to viewer → the next Grafana request answers 403.
8. **Grafana stopped.** `docker stop pstack-control-grafana-1` → `grafana.<domain>` shows
   `Grafana is not running.`, not pstack's login.
9. **Switch with a job running.** Start a deploy, run `pstack logging off` then `pstack logging loki`
   → the pstack container's `StartedAt` is unchanged, the deploy finishes, the health key is present
   within 30 s, and the Grafana link after a page reload.
10. **Upgrade.** `pstack upgrade` from a slice-1 host → Grafana appears, and `LOKI_PUSH_PASSWORD` is
    unchanged. A deployment routed to `grafana.<domain>` before the upgrade: `grafana.<domain>`
    serves Grafana, and that deployment's redeploy is refused.
11. **Certificates.** HTTP-01 host: a certificate is issued for `grafana.<domain>`. DNS-01 host:
    none is ordered.
12. **Code replay.** Traefik's access log shows the callback code once; replaying the URL restarts
    sign-in.

### Where the change lands

| Area | Files |
|---|---|
| Init and the control stack | `internal/initctl/init.go`: `GrafanaVersion`, `GrafanaService`, `LokiWiring`'s volume edit, two `requires` image entries, the grafana dir + datasource write, the `  grafana   …` summary line. `templates/control/grafana/datasources.yaml` (new, embedded), `assets.go` |
| Discovery | `internal/inspect/control.go` (`GrafanaOn`, beside `LokiPushURL`) |
| Sign-in | `internal/api/routes_grafana.go` (new); `routes_auth.go` (two `preGate` cases, the health key); `server.go` (`grafana atomic.Bool`, `Start` + `reindexLoop` refresh, the service-host refusal in `handle()`, header route list); `internal/auth/auth.go` (`SessionHashUser`); `packages/client/src/types.ts` (`Health.grafana`) |
| Hostnames | `internal/routing/domains.go` (`grafana.` and the comment) |
| UI | `apps/ui/src/composables/useAuth.ts`, `App.vue` (the link, hidden with a stored bearer), `views/LoginView.vue` (full navigation for an `/api/` next), `packages/pstack/ui/index.html` (sign-in honours `next`) |
| Tests | Go `_test.go` beside each; `openapi_coverage_test.go`, `permissions_test.go`; `packages/conformance` (`test/api-grafana.test.ts` with `GRAFANA_SHIM`, regenerated loki goldens, `cli-goldens.test.ts` header, `expected-pass.json`) |
| Docs | **`usage.md`:** the Grafana section (URL, roles table, sign-in, **log lines unredacted and who can read them**, no server admin, no live tail, Drilldown egress, `logging off` keeps the volume) and the health `grafana` key. **`control-plane.md`:** new §5h "Grafana sign-in" after slice 1's §5g (plan:4498): forwardAuth, the derived cookie, callback inside verify, discovery from docker, the service-host refusal. **`bootstrap.md:132`:** `control stack (Traefik 512 MB + pstack 512 MB, + advanced UI 128 MB; with --logging loki + Loki 2 GB + Grafana 768 MB, all capped in the template)`, which also fixes the stale 256 MB (`docker-compose.yml:36`, `:108`; `init.go:688`; plan:905). **Also:** the `docs/README.md` row; this design's banner. **`CHANGELOG.md`:** upgrade adds Grafana (768m, a grafana.com download); a deployment on `grafana.<domain>` is refused on its next deploy; loki goldens change. |

### Out of scope for slice 3

- Grafana on added domains.
- Grafana's HTTP API for scripts: service-account tokens pass verify only with a browser cookie.
- Grafana for a bearer-token SPA session.
- Explore's live tail and any Grafana Live feature: Live is off, because a WebSocket passes verify
  once.
- Provisioned dashboards or alerting.
- A Grafana server admin.
- A Grafana-only sign-out.
- Deep links from the deployment logs tab into Explore.
- The basic embedded UI's nav link.
- `pattern_ingester` (Drilldown's Patterns view): the Loki config stays byte-for-byte slice 1's.
- Offline preinstall of Logs Drilldown.
- Redacting log lines in Grafana.
- Multiple Grafana orgs.

---

## Facts this design stands on

Checked against source, not docs, where it mattered; the load-bearing ones were independently
re-verified by a second reader trying to refute them.

- **Basic auth in `loki-url` works.** The driver has no auth option, but it builds requests from the
  unredacted URL, and Go's `net/http` `Client.send` sets `Authorization: Basic` from `URL.User`
  before any round-tripper runs (loki v3.7.7 `clients/cmd/docker-driver/config.go`,
  `clients/pkg/promtail/client/client.go`; go1.26 `net/http/client.go`). 401 is not retried; 429 and
  5xx are, up to `loki-retries`.
- **The password leaks at `LOG_LEVEL=info`:** `driver.go:76` logs the full option map; moby forwards
  plugin stdout into dockerd's log.
- **Swarm and missing plugins:** swarmkit's `PluginFilter` rejects a node lacking the log plugin
  (`!exists && typeFound`) — tasks stay pending ("missing plugin on N nodes"); an alias `loki` is
  reported as `loki:latest` and matches. Nodes refresh plugin lists every 20s.
- **The stop-hang:** `driver.go:67-127` holds the driver mutex across close, which waits for the
  retry loop; moby's 10s bound covers the log copier only.
- **`keep-file`:** `driver.go:119-135` removes the per-container folder only when `keep-file` is
  false — its only `RemoveAll`.
- **`docker logs` / `docker service logs` keep working** with `keep-file=false` and never `no-file`:
  the plugin reports `ReadLogs=true` and serves its own json file (Docker's dual-logging cache never
  applies to it).
- **Traefik basic auth** accepts bcrypt, `{SHA}` and MD5-crypt; `$` doubles to `$$` in compose labels
  (traefik v3.7.13 `pkg/middlewares/auth/basic_auth.go`, containous/go-http-auth `basic.go`).
- **Loki:** the image is distroless (`loki -health` since 3.7.0); every config block but
  `runtime_config` limits needs a restart; `-verify-config` builds no storage client; a tsdb index
  period must be 24h; a schema `from` in the future stores nothing.
