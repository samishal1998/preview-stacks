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

## Slice 3 — Grafana with pstack sign-in (decisions recorded; own spec before building)

- **Service:** `grafana/grafana:13.2.1` (not `-slim`: no bundled plugins; not `-distroless`: no
  health check), `mem_limit: 768m`, named volume on `/var/lib/grafana`, Loki datasource provisioned
  (`access: proxy`, `http://loki:3100` over the `logs` network), `GF_SERVER_ROOT_URL=https://grafana.<domain>/`.
- **Sign-in:** Traefik `forwardAuth` on `grafana.<domain>` calls a new pstack verify endpoint.
  pstack's session cookie is host-only on `api.<domain>` and **must not be widened** — previews live
  on `*.<domain>` and would receive it. So Grafana gets its own cookie through a redirect sign-in:
  no cookie → 302 (navigations only; 401 for XHR) to the pstack UI login with a path-only return →
  a short-lived one-time code → `grafana.<domain>/<reserved path>?code=…`, routed to pstack → a
  `__Host-` cookie for `grafana.<domain>`, tied to the parent pstack session so logout revokes it.
  The verify endpoint answers `X-WEBAUTH-USER` and `X-WEBAUTH-ROLE`.
- **Roles:** viewer → Viewer, maintainer → Editor, admin → Admin. Grafana syncs the default org's
  role from the header; it can never grant Grafana server admin.
- **Hard security requirements** (each verified against Traefik 3.7.13 / Grafana 13.2.1 source):
  - Grafana and Loki's query API only on the `logs` network — never `preview-ingress`, never a
    published port. Anything that reaches Grafana directly can send `X-WEBAUTH-USER: admin`.
  - Every header Grafana reads is listed in `authResponseHeaders`: Traefik strips listed headers
    from the client request unconditionally, and passes unlisted ones through.
  - `GF_AUTH_DISABLE_LOGIN_FORM=true`, `GF_AUTH_BASIC_ENABLED=false`,
    `GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION=true` — never `disable_login=true`, which silently
    turns auth proxy off.
  - `GF_SECURITY_CSRF_ALWAYS_CHECK=true`, plus an Origin check in verify for unsafe methods: preview
    apps are same-site with `grafana.<domain>`, so SameSite does not stop them.
  - `__Host-` cookie names (a preview can plant `Domain=<domain>` cookies); the callback code is
    bound to a state cookie set by the first 302 (login CSRF).
  - Absolute `Location` headers built from the configured domain: Traefik resolves a relative one
    against the verify endpoint's internal address.
  - `grafana.` joins `IsControlHostname` and the reserved-host refusal.
- **Known costs:** Logs Drilldown is downloaded from grafana.com on first start (a host without that
  egress gets Grafana without it); Drilldown's Patterns view needs `pattern_ingester.enabled` in the
  Loki config.

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
