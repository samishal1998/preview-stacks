# Loki logging, as built (0.40.0)

> **Read this first if you are new to Loki logging.** It records what shipped, how the pieces connect,
> every decision behind them, and what is still open.
> The spec is [loki-logging-design.md](loki-logging-design.md), with each slice's build differences
> in a banner at its top. The task-by-task plans are
> [slice 1](loki-logging-slice-1-plan.md), [slice 2](loki-logging-slice-2-plan.md) and
> [slice 3](loki-logging-slice-3-plan.md). Operator docs:
> [usage.md](usage.md) (`### Turn Loki logging on or off: pstack logging`, `### Loki settings`,
> `### Grafana`). Architecture: [control-plane.md](control-plane.md) §5g and §5h.
>
> **State as of main `7081b1a` (v0.40.0, 2026-09-19):**
>
> - **GitHub release published.** Binaries for linux and darwin on amd64 and arm64, plus
>   `checksums.txt` and `install.sh`.
> - **npm not published.** `npm view` still shows 0.39.1 for both `@samyx/preview-stacks-client` and
>   `@samyx/preview-stacks-ui`, so "shipped in 0.40.0" is true of the binaries only. See
>   [follow-ups](#5-open-follow-ups).
> - **One test-only fix is local and unpushed:** commit `7c9f386` on
>   `claude/fix-flaky-loki-apply-test` (find it in `but status` by its message).
> - **None of the real-host checklists has been run.**

Line numbers drift, so this document cites paths and symbols. A path with no prefix is relative to
the repo root, except `golden/…` and `gen/…` (in `packages/conformance`) and `internal/…` (in
`packages/pstack`). Some sources are not in any clone; [the last section](#sources-that-are-not-in-a-clone)
lists them.

---

## 1. On one screen

Loki logging adds three things to a host:

- a Loki container in the control stack;
- the Grafana Loki Docker **driver plugin** on every node;
- a `logging:` block that pstack adds, at deploy time, to every service that has none.

The driver runs in dockerd and pushes to `https://loki.<domain>/loki/api/v1/push`, through Traefik
and behind basic auth. Loki's settings (chunking, retention, and filesystem or S3 storage) are edited
from the Control page, and each save restarts Loki only. Logs are read in Grafana at
`https://grafana.<domain>`, signed in with pstack accounts. pstack's own logs tab still reads from
docker.

| To… | Do |
|---|---|
| Turn it on for a new host | `pstack init --logging loki` (env `PSTACK_LOGGING`). With cloud-init: `pstack cloud-init --logging loki`. |
| Turn it on or off on an existing host | `pstack logging loki` or `pstack logging off` (`-n` prints the plan). This re-runs `init` from the saved state; nothing is retyped and the push password does not rotate. |
| Keep it across upgrades | Nothing to do. `pstack upgrade` reads Loki back from the rendered compose file and `LOKI_PUSH_PASSWORD` from `control/.env`. |
| Change retention (1–365 days) and chunking | The Control page's Logging panel (maintainer and up), or `pstack api logging set` / `PUT /api/logging`. |
| Move storage to S3 (one-way) | The Logging panel (admin), or `pstack api logging storage-set` / `PUT /api/logging/storage`. |
| Read logs | Grafana at `grafana.<domain>`, for developer and up (viewers get 403). The advanced UI shows a `Grafana` link in the rail. |
| Add a worker | The join script and the worker cloud-config install the plugin before joining. Re-running the join script on an old worker installs it too. |
| See which nodes lack the plugin | `pstack swarm`, or the Swarm page (`No loki plugin` badge). Both print the install line. |

**What turning it off does and does not do.** `pstack logging off` removes the Loki and Grafana
containers. It keeps:

- their volumes, so turning logging back on restores the logs and Grafana's users;
- the plugin on every node;
- `LOKI_PUSH_PASSWORD` in `control/.env`.

Deployments keep their driver block until they are redeployed. Until then their pushes get a 404,
which the driver does not retry, so container stops are not delayed. The `pstack-control_logs`
network stays behind.

---

## 2. As built

### 2a. Slice 1: `--logging loki` (PR #60)

| Path | Responsibility | Key symbols |
|---|---|---|
| `packages/pstack/internal/initctl/init.go` | Flag, push password, plugin install at step 1c, render, wiring, `.env`, summary lines | `Logging`, `LoggingNone`, `Loki`, `PushURLLabel` (`pstack.logging.push-url`), `GrafanaVersion` (13.2.1), `LokiService`, `LokiWiring`, `traefikBlock` |
| `packages/pstack/templates/control/loki/config.yaml` | Loki's fixed config. Written only when absent; after that the API owns it. | embedded as `pstack.LokiConfig` (`packages/pstack/assets.go`) |
| `packages/pstack/templates/control/docker-compose.yml` | Unchanged for slice 1: Loki is appended at `#__ADVANCED_UI_SERVICE__`. Slice 2 adds pstack's `./loki:/etc/loki` mount. | — |
| `packages/pstack/internal/swarm/swarm.go` | Plugin version and install line, per-node plugin reads, join material | `LokiVersion` (3.7.7, the image and plugin tag), `LokiPluginInstall`, `Node.LokiPlugin *bool`, `MarkLokiPlugins`, `JoinArgs.Logging` |
| `packages/pstack/internal/cloudinit/cloudinit.go` | Manager: appends `--logging loki` to the init call. Worker: an unnumbered `Loki log plugin` step before the join. | `Answers.Logging`, `WorkerAnswers.Logging` |
| `packages/pstack/internal/inspect/control.go` | Discovers Loki from docker; this is never a setting | `LokiPushURL` (the label on the control project's `loki` container, read with `ps -a`) |
| `packages/pstack/internal/autolabel/autolabel.go` | Injection at materialize time, the reserved-host refusal, and the 0600 generated file | `DetectLogging` (a var seam), `InjectLogging`, `MaterializeCompose`, `ControlHostname` |
| `packages/pstack/internal/routing/domains.go` | `control.`, `api.`, `loki.` and `grafana.` of every domain are control hostnames, whether or not logging is on | `IsControlHostname` |
| `packages/pstack/internal/compose/compose.go` | Plugin check before a logged deploy | `ComposeUp` (swarm: a note only; compose: refuses with `loki log plugin not installed`) |
| `packages/pstack/internal/upgrade/upgrade.go` | Reads Loki back, and implements `pstack logging` | `ControlState.Logging` / `.LokiPassword`, `SwitchLogging`, `LoggedDeployments` |
| `packages/pstack/internal/cli/{args,run,initguard}.go` | `--logging none\|loki`, `pstack logging <loki\|off>`, and two guard cases that stop an `init` re-run from reverting Loki | `initguard` password case and `--logging` case |
| `packages/pstack/internal/api/routes.go` | `GET /api/swarm` adds the per-node `lokiPlugin` and, when logging is on, `lokiPluginInstall` | — |
| `apps/ui/src/views/SwarmView.vue` | The `No loki plugin` badge and a banner with the install line. A scoped `@container` style fixes the card-mode overflow. | — |
| `packages/pstack/internal/redact/redact.go` | Masks the push URL as `user:••••@`. **This mask predates Loki**; slice 1 only added a test for it. | `urlPassword` |

**The rendered pieces.**

- **Loki service:**
  - `grafana/loki:3.7.7`, `mem_limit 2g`, `GOMEMLIMIT 1600MiB`.
  - On the `logs` network **only**, because Loki has no auth.
  - Mounts `./loki:/etc/loki:ro` (a directory, so a renamed-in file is seen).
  - Env `AWS_SHARED_CREDENTIALS_FILE=/etc/loki/s3-credentials`.
  - Health check is `/usr/bin/loki -health` (the image is distroless).
- **Traefik router `pstack-loki`:**
  - Rule `Host(loki.${DOMAIN}) && PathPrefix(/loki/api/v1/push)`.
  - Basic auth `pstack:{SHA}<base64 sha1>`, hashed in Go because compose cannot hash.
- **The push-URL label** is `https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/…`, so compose fills
  the password in from `.env`.
- **Anchor edits.** `LokiWiring` edits three existing template lines, and checks each anchor before
  editing it:
  - Traefik's networks line, matched inside the Traefik block only (R16);
  - the volumes, which gain `loki:` and `grafana:`;
  - the networks, which gain `logs: {}`.
- **The injected block:**
  - driver `loki`;
  - `service_name=<stack>-<svc>`;
  - `relabel` drops `filename`;
  - retries 2, timeout 1s, max backoff 800ms;
  - mode non-blocking;
  - `keep-file` false, `max-size` 10m, `max-file` 3.

  The options are constants on purpose. A service with any `logging:` key, including `json-file` or
  null, is left alone.

### 2b. Slice 2: Loki settings (PR #62)

| Path | Responsibility | Key symbols |
|---|---|---|
| `packages/pstack/internal/store/migrations.go` | Migration 9 creates `loki_config`: one row (`id=1`), `config` JSON without the secret, `secret` stored as-is (there is no read path), and `previous_config`/`previous_secret` as the undo record. `NULL` means no apply is in flight; `''` means the table was empty before. | migration 9 |
| `packages/pstack/internal/loki/loki.go` | Pure package (no server, no docker). Holds the settings, clamps, row I/O, merge, credentials file and directory resolution. | `Settings`, `Defaults`, `LimitsAt`, `EarliestCutover`, `Lead`, `Read`/`Save`/`Finish`/`Revert`, `Merge`, `Changed`, `WriteFile`, `Writable`, `Dir`, `Credentials`, `CredentialsOwner`, `ErrOneWay`/`ErrFixed`/`ErrNeedsRoot`, `*Error` |
| `packages/pstack/internal/loki/render.go` | Renders over slice 1's embedded file through 10 ordered literal anchors. With `Defaults()` the output is exactly slice 1's bytes. | `Render` |
| `packages/pstack/internal/loki/periods.go` | The period guard, which always reads periods from the files, never from the row | `Periods`, `CheckPeriods` |
| `packages/pstack/internal/loki/probe.go` | A SigV4 PUT, then a DELETE, of `pstack-probe-<hex>`, with a 10s timeout. Redirects are refused. Private endpoints are allowed. | `Probe` |
| `packages/pstack/internal/api/loki_apply.go` | The `loki-apply` job, rollback, resume and the boot reconcile. The header explains the design. | `lokiPut`/`lokiTake`/`lokiPending`, `startLokiApply`, `(*lokiApply).run`/`verify`/`ready`/`rollback`/`resume`, `reconcileLoki`, `phaseFind…phaseLog`, `lokiBootBy` (`pstack (boot)`) |
| `packages/pstack/internal/api/routes_logging.go` | The handlers. The header gives the PUT order. | `loggingGet`, `loggingPut`, `loggingStoragePut`, `lokiSave`, `parseChunks`, `parseStorage`, `wholeNumber` |
| `packages/pstack/internal/api/{routes,permissions}.go`, `packages/pstack/api/openapi.yaml` | Routes, two permission rows (maintainer for `/api/logging`, admin for `/storage`), and the operationIds `getLogging`, `setLogging`, `setLoggingStorage` | — |
| `packages/pstack/internal/api/{server,http}.go`, `packages/pstack/internal/cli/serve.go` | The `LokiDir`, `LokiReadyTimeoutMs` (default 300000) and `LokiUID` (default 10001) options, set by `PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS` and `PSTACK_LOKI_UID` (env only) | `TuningFromEnv` |
| `packages/pstack/internal/{jobs,events,notify,config}` | The `loki-apply` job action (not a lifecycle action), the `logging.changed` event (appended; 30 names now), the notifier line `Loki settings changed by X`, and a config export that skips it | `jobs.LokiApply` |
| `packages/client/src/{index,types}.ts` | `client.logging.get/set/setStorage` and the `Loki*` types. `JobAction` gains `'loki-apply'`. | — |
| `apps/ui/src/views/ControlView.vue` | The Logging panel: polls every 10s, bounds come from the server's `limits`, storage is locked below admin or once on S3, and the S3 confirm reads `One-way. S3 from <cutover> UTC?` | — |
| `packages/pstack/templates/control/docker-compose.yml` | pstack mounts `./loki:/etc/loki` **in every mode** (X1) | — |

### 2c. Slice 3: Grafana with pstack sign-in (PR #63)

| Path | Responsibility | Key symbols |
|---|---|---|
| `packages/pstack/internal/initctl/init.go` | Renders the Grafana service and creates `control/grafana/datasources` (0755, loki only) | `GrafanaService` |
| `packages/pstack/templates/control/grafana/datasources.yaml` | Provisioned datasource: uid `loki`, `http://loki:3100` over the `logs` network, not editable | embedded as `pstack.GrafanaDatasources` |
| `packages/pstack/internal/inspect/control.go` | Is a `grafana` container present? `ok=false` when docker did not answer. | `GrafanaOnChecked` (and `GrafanaOn`, which is dead in production; see follow-ups) |
| `packages/pstack/internal/api/server.go` | The `s.grafana` atomic, set by `grafanaCheckIn` in `Start` and on each 30s `reindexLoop` tick; a failed answer keeps the last value (fix `9c569ff`). `handle()` refuses service hosts first. | `grafanaCheckIn`, `handle` |
| `packages/pstack/internal/api/routes_grafana.go` | forwardAuth verify, the callback inside verify, and start on `control.` | `grafanaOn`, `grafanaURL`, `grafanaVerify`, `grafanaCallback`, `grafanaStart`, `grafanaUser`, `grafanaMAC`, `hostCookie`, `grafanaRoles` |
| `packages/pstack/internal/api/routes_auth.go` | Both Grafana routes are pre-gate. `/api/health` adds `grafana: https://grafana.<domain>` last, and only when Grafana is on. | — |
| `packages/pstack/internal/auth/auth.go` | Resolves a stored `sessions.id_hash`. `SessionUser` delegates to it. | `SessionHashUser` |
| `apps/ui/src/{App.vue,composables/useAuth.ts,views/LoginView.vue}` | The rail link (`authState.grafana && !settings.token && can('developer')`), and a full navigation for an `/api/` next | — |
| `packages/pstack/ui/index.html` | The basic UI honours an `/api/` next. It has no Grafana link. | — |

**The rendered Grafana service.**

- **Image and resources:** `grafana/grafana:13.2.1` (not `-slim`: the health check needs curl),
  `mem_limit 768m`, on the `logs` network **only**.
- **Auth proxy:** header `X-WEBAUTH-USER`, role from `X-WEBAUTH-ROLE`, auto sign-up, and no Grafana
  login token.
- **Turned off:**
  - the login form, basic auth, anonymous access and the sign-out menu;
  - Grafana Live (`GF_LIVE_MAX_CONNECTIONS 0`);
  - external snapshots, analytics and update checks;
  - four default plugins (Logs Drilldown is kept).
- **Not set:** `GF_AUTH_DISABLE_LOGIN`, because it silently turns sign-in off. There is no initial
  admin.
- **Router `pstack-grafana`:** priority 10000. forwardAuth goes to
  `http://pstack:7878/api/auth/grafana/verify` with `trustForwardHeader=false` and
  `authResponseHeaders=X-WEBAUTH-USER,X-WEBAUTH-ROLE`; Traefik strips client copies of those headers.

### 2d. Data flows

**Push path (slice 1).**

1. At deploy, `MaterializeCompose` calls `DetectLogging`, which is `inspect.LokiPushURL`, to read the
   label on the control project's `loki` container.
2. If the label is set, `InjectLogging` adds the driver block after the routing labels and before
   `Swarmify`, so one step serves both orchestrators. `compose.generated.yml` is then chmodded to 0600.
3. `ComposeUp` checks the plugin. Under compose, a missing plugin refuses the deploy. Under swarm it
   only writes a note; the scheduler keeps logged tasks off nodes without the plugin.
4. The container runs, and the driver plugin inside dockerd pushes over HTTPS to
   `loki.<domain>/loki/api/v1/push`.
5. Traefik routes `pstack-loki` and checks basic auth.
6. Traefik reaches Loki on the `logs` network at `:3100`.

   If Loki is down, the host falls to pstack's catch-all and `handle()` answers `404 Not found.` The
   driver does not retry a 404.

**Settings save to `loki-apply` (slice 2).**

1. `PUT /api/logging` (or `/storage`) goes to `lokiSave`, which checks in this order:
   - docker silent → 503;
   - no Loki container → 409;
   - `config.yaml` missing or not writable → 409;
   - parse the body;
   - `Merge` → 409 for a one-way or fixed-field change, 400 for a bad value;
   - S3 without a usable credentials owner → 409;
   - no change → `200 {changed:false}`;
   - S3 probe fails → 400;
   - otherwise `lokiPut` and `startLokiApply` → `202 {job}`.
2. The job runs on key `pstack-control`. `jobs.Start` keeps one waiting job and supersedes the rest,
   and a superseded save's patch is carried by its successor, because `lokiTake` takes every entry
   whose gen is at or below its own.
3. The job's phases:
   - **find:** the loki container.
   - **render:** read the row; resume if `InFlight`; `Merge`; `Render`; `CheckPeriods(adding=true)`.
   - **verify:** write the `.next` files, then run `loki -verify-config` in a throwaway
     `--network none` container.
   - **commit:** `loki.Save`, with the `previous_*` undo record in the same statement.
   - **swap:** rename the credentials file first, then the config.
   - **restart:** restart the Loki service only.
   - **ready:** poll `loki -health` until `LokiReadyTimeoutMs`.
   - **finish:** clear `previous_*`.
   - **log:** `level=error` lines are reported but do not fail the job.
   - Then emit `logging.changed` if anything changed.
4. A cancel before the swap changes nothing. From the swap on, a post runner with a 2×lead deadline
   ignores cancellation.
5. **Rollback** (Loki not ready): render the previous settings, check periods with `adding=false`,
   swap, `Revert`, restart and wait for ready. If undoing would drop a period that is already live, the
   job finishes and leaves the change in place.
6. **Resume** (pstack died mid-apply): finish forward. Whether Loki loaded the files is judged from the
   container's `StartedAt` against the files' mtimes.
7. **Boot reconcile** (`reconcileLoki`, called from `New`): start a job by `pstack (boot)` when the
   row is in flight or the disk differs from the render.

**Grafana request (slice 3).**

1. A browser asks `grafana.<domain>`. Traefik's forwardAuth calls `GET /api/auth/grafana/verify`; the
   request carries `Host pstack:7878` and `X-Forwarded-Uri`.
2. On the callback path `/-/pstack/callback`, verify takes the single-use `grafana:<code>`, checks the
   state cookie in constant time, and sets `__Host-pstack_grafana` with a 302 to the page asked for.
   forwardAuth relays that 302 and its `Set-Cookie`.
3. With no valid cookie:
   - a top-level document navigation gets a 302 to
     `control.<domain>/api/auth/grafana/start?state&next`, plus the state cookie (900s);
   - fetches and iframes get 401.
4. `grafanaStart` needs a live `pstack_session`. It parks `{session hash, state, next}` for 60s and
   redirects to the callback. Without a session it sends the browser to `/login?next=…`.
5. With a valid cookie, `grafanaUser` makes one SQLite lookup (`SessionHashUser`). Then:
   - a viewer, or a role not in `grafanaRoles`, gets 403;
   - a non-GET/HEAD request whose `Origin` is not `https://grafana.<domain>` gets 403;
   - otherwise verify answers 204 with `X-WEBAUTH-USER` and `X-WEBAUTH-ROLE`, and Grafana signs the
     user in by header. developer and maintainer become Editor; admin becomes Admin.
6. The cookie is `<sessions.id_hash>.<HMAC-SHA256(PSTACK_TOKEN, "pstack-grafana\n"+hash)>`, with no
   table behind it. Signing out of pstack, session expiry or a deleted account ends Grafana access on
   the next request.

---

## 3. Decisions

### 3a. Owner decisions

| Decision | Why | Cost |
|---|---|---|
| Loki runs **in the control stack**, behind an init flag. The UI edits chunking and retention and restarts only Loki. | A `kind: shared` deployment cannot have a real settings form, and an external URL leaves storage to someone else. | Loki shares the manager. |
| The collector is **the Loki Docker driver plugin**, injected per service unless the service has `logging:`. | The request named it. A daemon-wide default driver would capture Traefik, pstack and Loki itself, and bypass swarm's plugin scheduling. Alloy is the fallback if the driver's stop-hang ever bites. | The plugin must be on every node. |
| Streams are labelled `service_name=<stack>-<service>`, rendered statically. | It is Loki's and Grafana's service identity, and a static render never trips the driver's `,`/`=` label parser. | — |
| `keep-file=false`, capped at 10m × 3. | `keep-file=true` leaks one folder per container ID in the plugin rootfs forever. | The logs tab loses stopped containers' output; Loki still has it. |
| Nodes push **through Traefik** with basic auth in the URL userinfo. | The plugin cannot resolve overlay DNS, and pstack manages no firewall (the provider does), so a published `:3100` would need a rule per worker. | The password is visible in `docker inspect`, and Traefik must be up for pushes to land. |
| 7-day retention until slice 2; slice 2 allows 1–365 days. | The manager's disk is shared with images. | — |
| Storage is filesystem or S3-compatible only. | GCS and Azure add credential shapes nobody asked for. | — |
| Logs are read **in Grafana**; pstack's logs tab stays docker-backed. | A LogQL reader in pstack would have to rebuild redaction and the line format. | Grafana shows lines unredacted. |
| Grafana signs in **with pstack accounts**, shipped as its own slice. | A second password is what SSO removed, and a new auth surface deserves its own review. | — |
| (2026-09-15) **Viewers get no Grafana:** viewer → 403; developer and maintainer → Editor; admin → Admin; the link shows for developer and up. | Even a Grafana Viewer can query unredacted lines through panels or `/api/ds/query`. | Viewers read only pstack's redacted logs tab. |
| (2026-09-15) "Proceed with all slices." Slices 2 and 3 were specced, planned and built the same orchestrated way. | Delegated. | Slice 1's spec was approved section by section (2026-09-14). The slice-2 and slice-3 specs were written by agents and not approved section by section. |
| Slice-2 spec (delegated): the API **owns `control/loki/config.yaml`**. `init` writes it only when absent; a hand edit is reverted by the next apply. | Otherwise an `init`, `upgrade` or `logging loki` after an S3 save would write the one-period filesystem config back, and every log in S3 would become unreadable. | Hand edits do not stick. |
| Slice-2 spec (delegated): S3 credentials go in an AWS shared-credentials file, not `${VAR}`. | `docker restart` keeps the environment from creation time. | Relies on the SDK re-reading the file (see real-host check 3 in slice 2's list). |
| Slice-2 spec (delegated): moving to S3 adds a **future-dated schema period** and is one-way. The probe allows private and loopback endpoints. | Loki can only change storage by period. An internal MinIO is the normal case, and the caller is an admin. | There is no way back to filesystem. |
| Slice-3 spec (delegated): no new flag, env var or command. Grafana holds no secret that pstack generates; the cookie MAC key is `PSTACK_TOKEN`. | `upgrade` has nothing new to read back. | Rotating `PSTACK_TOKEN` breaks every Grafana cookie. Signing back in is silent while the pstack session lives. |

### 3b. Cross-slice controller rulings

| Id | Decision | Why | Cost if wrong |
|---|---|---|---|
| X1 | pstack's `./loki:/etc/loki` mount is **unconditional** (logging off too), and `init` always creates `control/loki`. | pstack's environment is an explicit map, so a `pstack logging` toggle leaves pstack's service byte-identical and compose never recreates it. A mount only when logging is on would recreate pstack on every toggle and kill in-flight jobs. | 20 goldens regenerated once. Every host's pstack is recreated once, on its upgrade to 0.40.0. |
| X2 | Slice 3's contract note C1 is reversed by X1: the switch does not recreate pstack, jobs survive, and real-host check 9 keeps "StartedAt unchanged". C1 now reads "No deviation" so the numbering holds. | It follows from X1. | The slice-3 spec would state a reversed claim. |
| X3 | Slice-3 contract notes C2–C15 stand as written. Examples: C2 (link for `can('developer')`), C4 (plain-text bodies through `http.Error`, which appends `\n`), C7 (`GRAFANA_SHIM` stays local to its test), C8 (Go tests build `New(Options{…})`, set `s.grafana`, and never call `Start`), C9 (slice 3 adds no migration, job, event, OpenAPI path, flag, env var or cloud-init change), C10 (the service-host refusal keys on `r.Host`, never `requestHost`). | Each was checked against the tree in pre-flight. | — |

### 3c. Slice 1 rulings (R1–R21)

Source: the L1 ledger ([not in a clone](#sources-that-are-not-in-a-clone)), "Pre-flight rulings".

| Id | Decision | Why | Cost if wrong |
|---|---|---|---|
| R1 | Hand-edit and commit `golden/host/expected/swarm.json`. The rule becomes: never regenerate `golden/host` with `gen/host-fixture.ts`, and never commit `golden/host/db/*`. | The route output really changed, and an uncommitted golden leaves the suite red. | One golden reverts. |
| R2 | `/api/swarm`: every node carries a tri-state `lokiPlugin` (no omitempty). The top-level `lokiPluginInstall` appears only when logging is on. | The spec approved only the node field. | The SPA banner would need the line from somewhere else. |
| R3 | 13 new CLI golden rows, including `swarm-status-loki`. **Corrected at T6:** the generator prints `wrote 93 goldens` (80 + 13), and `cli-goldens` runs 94 tests (93 plus the version test). Write "93 written / 94 tested". | CASES had 80 rows, not 81. | One count line. |
| R4 | A failed compose plugin check's Stderr is one sentence; the install line goes in the job-log note. | The 344-rune line would be cut at 300 by `stack.firstLine`. | The line would move into the step message and be truncated. |
| R5 | `LOKI_PUSH_PASSWORD` survives `pstack logging off`, and `pstack logging loki` reuses it. Ratified at T3: a supplied `PSTACK_LOKI_PASSWORD` is validated as 32 lowercase hex under `none` too. | The plan contradicted the spec's failure table. Compose expands `$` in `.env`. | A logging-off host keeps one unused 0600 secret. A hand-edited non-hex password must be fixed before `off` or `upgrade`. |
| R6 | The mask stays redact's 4-bullet form, and the spec line was amended in place. | The 8 bullets were illustrative, and 4 is pinned product-wide. | Cosmetic. The owner never confirmed (see follow-ups). |
| R7 | With `PSTACK_DOMAIN` unset, autolabel reads `DOMAIN` from `<data>/control/.env` itself; it does not import `upgrade`. | Importing `upgrade` would be a cycle. | One extra file read per materialize. |
| R8 | A Go test compares `pstack.LokiConfig` byte for byte with an in-test literal of the spec block. | The render golden is generated from the binary, so it cannot check the binary. | A duplicate literal to maintain. |
| R9 | T14 re-quotes `docs/README.md`'s actual lines. | The plan quoted stale text. | The edit fails loudly. |
| R10 | Tools extract the design doc's Loki config by content (the yaml fence after `### Loki config`), never by line numbers. | Line numbers shift. | Nothing. |
| R11 | Each slice's design banner lists every surviving deviation; the spec body is kept as approved. | A stale banner misleads. | A doc edit. |
| R12 | `-race` is always used. | Global constraint. | Nothing. |
| R13 | The rendered Loki service's comments may differ from the spec, as long as every value matches. | Comments are not values. | A golden regen. |
| R14 | The swarm deploy test also asserts that a service's own `logging: {driver: json-file}` survives. | The spec says so. | One assertion. |
| R15 | `LOKI_SHIM` and `NODE_PLUGINS` are defined once, in `packages/conformance/gen/goldens.table.ts`. | One copy, no drift. | An import to undo. |
| R16 | `LokiWiring`'s networks anchor must match inside the `  traefik:` block, and a test removes only Traefik's copy. | The advanced UI has an identical line. Without this, a moved Traefik line passes `init` and every push fails. | A slightly stricter check. |
| R17 | The plugin checks live in `compose` and the node reads in `swarm`, not in `inspect`. | `swarm` is the import-graph leaf. | A package move. |
| R18 | All new CLI and UI copy is terse state, fixed before the goldens were generated. | The owner corrected verbosity three or more times (`docs/ui-rules.md`). | Goldens regenerate. |
| R19 | `pstack logging off`'s count reads each deployment's root `compose.generated.yml` only. | Registry PUTs store compose at the root. | Nested compose paths are undercounted (documented in usage.md). |
| R20 | The masking test keeps only the JSON-quoted `compose.generated.yml` form. | The plain-URL half duplicated `redact_test.go`. | One redundant test missing. |
| R21 | The dash-portability negative control uses `t.Log` when dash is absent. | Global constraint 16: report honestly. | Nothing. |

Task-level slice-1 rulings that shaped the tree:

- **T2:** `.gitignore`'s blanket `config.yaml` rule is negated by `!**/loki/config.yaml`, which covers
  the template and its rendered goldens.
- **T7:** a report prints one `no loki plugin: <node>` row per missing node, then the install line
  once.
- **T11:** the card-mode badge overflow is fixed with a scoped `<style>` in `SwarmView.vue`. At 320px,
  scrollWidth went from 358 to 320.
- **Final fix wave** (`dd16f7e`, `ce632cb`, `3250d43`):
  - the generated file is chmodded to 0600;
  - `control/loki` is 0755;
  - the leftover `logs` network is documented.

  The final review's further ruling, **set the mode before the bytes land**, was never applied (see
  §4).

### 3d. Slice 2 rulings (S2-R1…S2-R11)

Source: the L2 ledger.

| Id | Decision | Why | Cost if wrong |
|---|---|---|---|
| S2-R1 | X1 made concrete. The mount goes after `- ./docker:/docker-config` in the template, with no Go change. The `control/loki` ensureDir is unconditional at 0755. `config.yaml` is written only with loki, and only when absent. `LokiWiring` anchor 4 was deleted. The golden set is 20 files. | See X1. | 20 goldens regenerate. Every pstack gains one idle mount. |
| S2-R1b | Every "the switch recreates pstack" line in the plans and specs is corrected, and both instructions to amend slice 3's spec are deleted. Discharged by `e9c8504` on main, and by a supersession banner on the external slice-2 spec. | X2. | The slice-3 spec would be corrupted by a stale instruction. |
| S2-R1c | Residual risk, recorded but not fixed: nothing reconciles on `pstack logging loki`. A hand-deleted `control/loki` comes back as defaults only at the next pstack restart. | pstack is never recreated by the switch (X1). | One sentence of docs. |
| S2-R1i | Without a writable `control/loki`, the PUTs answer 409 `Loki's config.yaml is missing or read-only — run pstack upgrade on the host`, not the spec's "the loki directory is not mounted here". **The ruling's own text is missing from the L2 ledger;** this is reconstructed from the design banner and the ledger's T12 entries. | It names what `loki.Writable` actually detects. | The external slice-2 spec and the slice-2 plan still carry the old string. |
| S2-R2 | `loki_apply.go` uses one naming scheme: `phaseFind…phaseLog` constants and `lokiRender`. | Plan tasks T10 and T11 claimed amendments that never reached T9's code. | Edit anchors miss, and three conventions land in one file. |
| S2-R3 | The fourth `permissions_test` row is kept only if `rootOnly` exists. | — | One test row. |
| S2-R4 | Two permission rows, because GET and PUT share `/api/logging`, not the spec's three. | The `/api/domains` precedent. | One row. |
| S2-R5 | No slice-2 task prunes `pstack-control_logs`. The `DetectLogging` cost and the per-poll `ControlRuntime` are named in the banner, not fixed. | Slice 1 already documented them. | A reviewer re-raises a known cost. |
| S2-R6 | The "`but diff` shows only T7's goldens" constraint is updated to the 20-file set. | — | The constraint would block a correct build. |
| S2-R7 | notify's `Summarize` uses whichever of `by()`/`byStr()` gives the leading " by ". It shipped with `by()`. | An empty `by` must add nothing. | One red step. The deciding test does not actually tell the two apart (see follow-ups). |
| S2-R8 | Cosmetic plan corrections: `apicli.go:27`, `a.rollback`, the expected PASS count, `go.mod` at the repo root, and two file-map rows. | — | — |
| S2-R9 | `PSTACK_LOKI_DIR`, `_READY_TIMEOUT_MS` and `_UID` are env only, with no `serve` flags. | Every existing tuning knob is env only; this is a deviation from the letter of AGENTS.md Go rule 18. | Someone asks for three flags nobody needs. |
| S2-R10 | The `scrubs` closure's thread safety rests on `go test -race`. If it ever races, copy the value before `jobs.Start` or give scrubs its own mutex. | jobs.Registry runs scrub only on the job goroutine, after work returns. | One race, found at the first `-race` run. |
| S2-R11 | Plan preconditions that say slice 1 is unfinished are stale. Golden counts are whatever `bun gen/goldens.ts` prints (93 files); 94 is a test count. Every edit anchors on quoted text, never on a line number. | Line numbers in the plan drifted. | Edits miss. |

Task-level slice-2 rulings:

- **T9:** a cancel after verify yields an all-OK Outcome, but the job is filed as `cancelled`,
  because the registry checks the context first.
- A **swap failure after the commit goes to rollback.** The spec's failure table has no row for it,
  and rollback is the only way to make the row and the files agree again. The cost is one extra
  restart.
- **Shared job key:** a waiting preempting `down` on `pstack-control` makes the PUT answer 409
  `pstack-control is busy with a teardown — retry`. The pending patch stays and is carried by the next
  save.

### 3e. Slice 3 rulings (S3-R1…S3-R14)

Source: the L3 ledger.

| Id | Decision | Why | Cost if wrong |
|---|---|---|---|
| S3-R1 | T1's body is 1006 lines, not 1010, and its fallback checks 13 things, not 23. The plan was fixed in `198298d`. | The plan's own awk gives 1006. | T1's splice aborts on its precondition. |
| S3-R2 | `control/grafana/datasources` is created only under `logging == Loki`, right after the unconditional `control/loki` block. | Keeps logging-off dry runs unchanged. | The mkdir lands in logging-off dry runs, or out of order. |
| S3-R3 | That directory is 0755, not left to the umask as the spec said. | Under umask 027 or 077, a root-owned 0700 directory is unreadable by Grafana's uid 472. Slice 1 fixed `control/loki` the same way. | None; the umask form is the broken one. |
| S3-R4 | T6's logging-off subtest anchors on the current text and adds a `grafana` check. | The plan quoted older text. | The step's edits fail. |
| S3-R5 | The App.vue edit anchor quotes the file's escape sequence exactly, never the rendered glyph. | An exact-match edit. | The edit fails. |
| S3-R6 | The `routes_grafana.go` header and the `reindexLoop` comment say a `pstack logging` switch shows within 30s, with no pstack restart. | The plan's wording predated X1. | A design header would state a reversed claim. |
| S3-R7 | The release gate's grep for stale prose is narrowed to `logging.*recreates pstack`. | The CHANGELOG's correct line "recreates pstack once on every host" would trip the broad grep. | T12 fails its own gate. |
| S3-R8 | `loki.<primary>` answers **404** `Not found.` while Loki is not running, not the spec's 503. `grafana.<primary>` keeps 503 `Grafana is not running.` | The driver does not retry a 404 but retries a 503 twice, which would delay every container stop, and slice 1 already promised 404. | A machine-only hostname answers 404 instead of a sentence. |
| S3-R9 | `server.go`'s header has no route list, so only the pre-gate comment changes (it now names Grafana's verify and start). | — | None. |
| S3-R10 | Conformance may be red between T6 and T11, because the `init*-loki` goldens regenerate in T11. | Each task keeps its own package green. | A bisect in that range lands on red conformance that is not a regression. |
| S3-R11 | Cookie assertions use `Headers.getSetCookie()`, not regexes over the joined header. | Exact per-cookie comparison. | None. One regex remains in `pstackSession`. |
| S3-R12 | `docs/README.md`'s one "Build plans" sentence names all three plans. | — | Cosmetic. |
| S3-R13 | Guard tests that pass before the code exists are allowed if each has a working negative control. The LoginView and basic-UI changes are verified in a browser only. | — | — |
| S3-R14 | The plan's Global Constraints point at a wiped scratchpad. The real spec is `loki-artifacts/slice-3-spec.md`. | The scratchpad is wiped on session restart. | — |

### 3f. Deviations from the approved specs

Each is also in the [design doc's banner](loki-logging-design.md), except where marked.

| Slice | Deviation | Ruling |
|---|---|---|
| 1 | The compose plugin check fails with one sentence; the install line is in the job log. | R4 |
| 1 | `/api/swarm` carries `lokiPlugin` on every host (`null` when off or unchecked); `lokiPluginInstall` only when on. | R1, R2 |
| 1 | The mask is `://user:••••@` (4 bullets); the spec line was edited in place. | R6 |
| 1 | The `no-args` and `unknown-command` goldens changed too. | — |
| 1 | `initguard` refuses an `init --logging loki` re-run that would mint a new password. | plan |
| 1 | The join script's plugin step sits before the already-joined exit. | plan |
| 1 | No `openapi.yaml` or `apicli` change, because `/api/swarm` is untyped. | plan |
| 1 | The plugin checks are in `compose` and `swarm`, not `inspect`. | R17 |
| 1 | `logging off` counts root `compose.generated.yml` files only. | R19 |
| 1 | `logging off` keeps the push password. | R5 |
| 1 | Extra conformance: `upgrade-plan-*-loki`, `logging` dry runs over both cells, `swarm-join-cloud-config-loki`, and the `/api/swarm` flag test. *(not in the banner)* | plan |
| 2 | Two permission rows. | S2-R4 |
| 2 | No 503 documented in OpenAPI for the PUTs. | plan |
| 2 | A one-way or fixed-field 409 comes before a range 400. | plan |
| 2 | The basic UI prints `loki-apply` raw, and a job has no `by` field. | plan |
| 2 | `loki.Writable` repeats routing's write probe. | plan |
| 2 | The unconditional mount. | X1, S2-R1 |
| 2 | Nothing reconciles on `pstack logging loki`. | S2-R1c |
| 2 | The 409 wording `Loki's config.yaml is missing or read-only — run pstack upgrade on the host`. | S2-R1i |
| 2 | Env-only knobs. | S2-R9 |
| 2 | A swap failure goes to rollback. *(not in the banner)* | T9/T11 ledger |
| 3 | The Grafana link shows for developer and up; the spec also said every signed-in role. | C2, owner |
| 3 | `loki.<primary>` answers 404, not 503. | S3-R8 |
| 3 | The datasources directory is 0755. | S3-R3 |

The body of the slice-3 spec (in the design doc) and the slice-3 plan (C4, C10) still show the 503
for `loki.`. By convention the body is kept as approved, so read the banner first.

---

## 4. Known limitations and caveats

**Runtime (slice 1).**

- **Batches are dropped, not queued.** If Loki is down (restart, OOM, or Traefik down), each batch is
  retried twice and then dropped. A 401 (password mismatch) drops every batch.
- **Swarm placement.** Swarm keeps logged services off nodes without the plugin. If no node
  qualifies, the stack times out after 180s.
- **No password rotation.** Rotating the push password would need every stack redeployed, so it is
  out of scope.
- **The generated file's permission window.** `compose.generated.yml` carries the push password. It is
  written at 0644 (or refilled at its existing 0644) and only then chmodded to 0600
  (`MaterializeCompose`). A local user watching `deployments/<id>/` can open it inside that window,
  and the chmod does not revoke an open file descriptor. Bounded: the credential can only push, and
  the attacker must already be a local user.
- **The reserved-host refusal can lose the primary domain.** A non-root, host-side `pstack up` with
  `PSTACK_DOMAIN` unset cannot read the 0600 `control/.env`, and so drops the primary-domain half of
  the refusal (R7 edge). Added domains still apply.
- **`lokiPluginInstall` has a second condition.** `/api/swarm` needs nodes **and** a push URL, so a
  compose-only host with logging on carries neither field. The CHANGELOG's "when logging is on" is
  necessary, not sufficient.
- **Docker cost on every compose verb.** `DetectLogging` costs a `docker ps -a` plus an inspect on
  every verb that resolves `-f`, on every host, including logging-off ones. The order is required,
  because `down` must resolve the same files `up` did. Accepted.
- **`pstack-control_logs` is never pruned** after `logging off`. The `loki` and `grafana` volumes
  are kept on purpose.

**Apply (slice 2).**

- **Docker cost per poll.** `GET /api/logging` adds one `ControlRuntime` per 10s poll for every open
  Control page viewed by a maintainer or above. Accepted.
- **The probe runs from the wrong network.** It runs from pstack's networks, not Loki's `logs`
  network. An endpoint that only pstack can reach passes the probe, then fails every flush after the
  cutover: the WAL grows, and writes throttle at 90% disk. There is no background S3 health check.
- **Only one undo record.** `loki.Save` overwrites `previous_*` unconditionally. The undo record
  survives only because a save's job resumes any in-flight apply first; remove that hook and two
  overlapping applies destroy it.
- **`updated_at` after a rollback.** `loki.Revert` leaves `updated_at` at the failed apply's time.
- **Two PUTs can race past the no-change check.** They share no lock at that step. By design, the job
  re-checks against the row, and a save made during an apply answers 202.
- **`PSTACK_LOKI_UID=0` counts as unset** (10001). This is harmless, because root chowns to 10001
  anyway.
- **The probe drops path prefixes.** With virtual-hosted addressing it drops any path prefix on the
  endpoint; path-style keeps it. This is undocumented.
- **An image bump can crash-loop Loki.** A bump that recreates Loki on a kept `config.yaml` can
  crash-loop until the boot apply re-renders it.
- **The transcript can name the bucket.** The `loki-apply` transcript is viewer-readable and may name
  the endpoint or bucket in Loki's error lines.
- **Retention changes are not retroactive.**
- **The upgrade to 0.40.0 recreates pstack.** Upgrading a host from 0.39.x or earlier recreates its
  pstack container once (X1), so jobs running during that upgrade die.

**Grafana (slice 3).**

- **Lines are unredacted.** That is why viewers are refused.
- **Missing features:**
  - no Grafana Live or live tail;
  - no Grafana server admin;
  - no Grafana-only sign-out;
  - no service accounts for scripts;
  - no access for a bearer-token SPA session (the link is hidden).
- **The browser must allow cookies.**
- **Logs Drilldown needs grafana.com egress** on first start.
- **Upgrading a loki host adds Grafana.** That is 768m more on the manager, plus two image pulls
  before `up`; a host without Docker Hub egress fails at `requires … image`. A deployment already
  routed to `grafana.<domain>` loses the host, and its next deploy is refused.
- **Startup waits on docker.** `Start`'s first `grafanaCheckIn` runs before `Serve` and has no
  timeout. If docker fails at that moment, `s.grafana` stays false until a tick succeeds.

**Build and bisect.**

- **Conformance is red inside slice 3.** It is red between slice-3 commits T6 and T11 (S3-R10), which
  is not a regression.
- **The basic UI lacks two features.** It has neither the Swarm plugin flag nor a Grafana link, on
  purpose.

**Out of scope** (not built):

- **Slice 1:** a Loki read path in pstack, push-password rotation, plugin upgrades, multi-tenant Loki.
- **Slice 2:** S3 → filesystem, moving buckets, runtime_config reloads, GCS or Azure, IAM roles,
  per-stack retention, carrying the settings in the portable config.
- **Slice 3:** Grafana on added domains, dashboards and alerting, multiple orgs, offline Drilldown.

---

## 5. Open follow-ups

Each row was checked against the tree on 2026-09-19 unless marked "per ledger".

| # | Item | Exact next action | Who |
|---|---|---|---|
| 1 | **npm 0.40.0 is unpublished.** CI's `NPM_TOKEN` (set 2026-08-24) is rejected, so `bun publish` falls back to web auth, hangs about 5 minutes and fails. The same happened for 0.38.0–0.39.1, which were published by hand. | Create a granular **automation** token, then run `gh secret set NPM_TOKEN` and `gh workflow run release.yml --ref v0.40.0 -f npm_only=true`. Verify with the job log's `N package(s)` line (expect 2, not "0 package(s), 2 skipped") **and** `npm view @samyx/preview-stacks-client version` → 0.40.0. A green job alone proves nothing. | **Owner only.** Agents never handle credentials. |
| 2 | **Flaky `TestLokiApplyCarriesASupersededSave`** ("one logging.changed per applying job, got 1"). It broke v0.40.0's first release run. Root cause: the pending map is per section, so a later same-section save can replace the first job's entry before that job's `lokiTake` runs. The fix is test-only: wait for the first job's first docker command before the next two PUTs. It failed 49 of 50 runs with `-cpu=1` before the fix and passed 50 of 50 after. | With the owner's go-ahead: `but push claude/fix-flaky-loki-apply-test`, then `but pr new claude/fix-flaky-loki-apply-test --draft -F <file>`. | Agent, after owner OK |
| 3 | **`inspect.GrafanaOn` is dead in production.** Since `9c569ff`, every caller uses `GrafanaOnChecked`; only `control_test.go` calls `GrafanaOn`. | Delete `GrafanaOn`. Point `TestGrafanaOn` at `GrafanaOnChecked`. Fix the comments that name it: the `grafana` field in `server.go`, the `routes_grafana.go` header, the `control.go` header, `init_test.go` and `docs/control-plane.md` §5h (line ~1160). | Agent |
| 4 | **Real-host checklists** (6 + 7 + 12 checks) have never been run. | Run [§6a](#6a-real-host-checklists-never-run) on a throwaway VPS with DNS and Docker. | Owner, or an agent with a host |
| 5 | **Pre-write mode for `compose.generated.yml`** (the §4 risk). | When `pushURL != ""`, chmod the existing file to 0600 first (ignore ErrNotExist) and create it at 0600. Move the test's negative control to "drop the pre-write mode set". | Agent |
| 6 | **Stale doc text.** Slice-1 plan: the pre-R5 password text and the long `…still carry the loki driver; each drops it…` string. `packages/pstack/templates/control/README.md`: `LOKI_PUSH_PASSWORD (only with --logging loki)` (false since R5), plus a misaligned `grafana/datasources` line. `usage.md`'s two hostname tables word `loki.` differently ("reserved even with logging off" vs "only with `--logging loki`"). | Edit the text. The plans are linked from `docs/README.md` and so get trusted. | Agent |
| 7 | **`packages/pstack/internal/loki/render.go` anchors** are checked with `Contains` and then `Replace(n=1)`, so a duplicated anchor passes silently. | Use `strings.Count(template, e.anchor) != 1`. | Agent |
| 8 | **`loki.WriteFile`** does `Remove` then `WriteFile`, which is not atomic (a symlink race only root can win), and it infers "secret" from mode 0600. | After the remove, open with `O_CREATE\|O_EXCL`, and pass an explicit secret flag. | Agent |
| 9 | **The Logging panel's `loggingError`** is cleared only by a later successful PUT (`saveLogging`), so the ErrorNote survives polls and rollbacks. | Clear it in the refill watch. | Agent |
| 10 | **`loki.Merge` copies the `*S3` pointer,** so the merged settings alias the caller's patch (per ledger). | Copy the value. | Agent |
| 11 | **Cosmetic lines** from the slice-3 final review: CHANGELOG 0.40.0's "Goldens, for Grafana" bullet (128 columns); `autolabel.go`'s package header (a 157-column line) and the `ControlHostname` doc (114 columns); a line in `init_test.go` (~119 columns). | Rewrap them when tidying. | Agent |
| 12 | **The `pstack-control_logs` network** is left after `logging off`. | Decide whether to prune it. If yes, add a best-effort `docker network rm pstack-control_logs` to `SwitchLogging`'s off path, plus a golden update. It is documented as harmless today. | Owner decides |
| 13 | **`DetectLogging`'s cost** (§4). | Accepted. Act only if compose verbs get measurably slow. | — |
| 14 | **Duplicated strings and guards.** The `loki plugin not installed: logged services will not run here` echo is in both `swarm.go` and `cloudinit.go`. The `len(info.Nodes) > 0 && LokiPushURL != ""` guard is in both `packages/pstack/internal/api/routes.go` and `packages/pstack/internal/cli/run.go`. The compose job-log label differs from swarm's. | Hoist a constant beside `LokiPluginInstall`. Optional. | Agent |
| 15 | **Tests to tighten** (per ledger). `api-logging` should use `toBe`, not `toStartWith`. The R7 fallback subtest and its comment. `SessionHashUser` revocation is untested. The probe's refused-PUT early return and its DELETE and `url.Parse` branches. A re-chmodded kept `config.yaml` would still pass `init_test`. `config.go`'s loki read error. The S2-R7 `by:""` case. The regex in `api-grafana`'s `pstackSession`. `api-loki-settings`' 409-before-body order. | Add a case for each. | Agent |
| 16 | **Older UI defects** (not from this work). Card mode breaks ControlView's `This API` and UsersView's `you` badges the same way T11 fixed on the Swarm page. At 1280×713, the rail's last item is cut off. The Logging panel's `?` hard-codes a GitHub URL, the only one in `apps/ui`, on a `.hint-btn` whose typed glyph sits off-centre. | Move T11's scoped rule into `app.css` if the owner wants it global. | Owner decides |
| 17 | **Owner confirmation of the 4-bullet mask** (R6) was asked for and never answered. | Ask once. | Owner |
| 18 | **Tracked sealed export in a public repo.** `.config.aug31.yaml` (added in `8bb3196`) is a scrypt-sealed full credential export (`pstack pull config`), and the repo is public. | Treat every credential of that host as exposed and rotate it (`PSTACK_TOKEN`, DNS token, notifier secrets, registry passwords, SSO secrets); delete the file (a history purge is optional, since forks may already have it). | **Owner only.** Agents never handle credentials. |
| 19 | **Tracked plaintext password in a public repo.** `hetzner.yml` (a generated cloud-config for one real host) carries a plaintext, low-entropy Traefik dashboard password for `admin`; it is on `main` and in GitHub's source archives for ~46 tags, and the same literal is in older history paths (`hetzner_4.yml`, GitButler conflict copies). `.gitignore:22-23` wrongly says the committed cloud-config "carried no credential". | Change that dashboard password wherever it is still used or reused; remove the file from the tree; correct the `.gitignore` comment. A history rewrite needs every path and tag scrubbed and GitHub Support to purge cached commits. | **Owner only.** |
| 20 | **`.gitignore` misses the `.config.<x>.yaml` shape.** Its patterns match `.config.yaml` and `.config-*.yaml`, not dot-separated names like `.config.aug31.yaml`, so a re-created export would show as untracked instead of ignored. `temp.yml` and `temp2.yml` are harmless tracked scratch files. | Add `.config.*.yaml` (and `config.*.yaml`) globs alongside rows 18-19; remove the two scratch files. | Owner decides (bundle with 18-19) |

---

## 6. Verification status

### 6a. Real-host checklists (never run)

A docker shim cannot prove any of these checks. None has been run for any slice.

- **Slice 1 (6 checks):** design doc, slice 1, `### Testing`, "Real host, before the release". They
  cover:
  - cloud-init end to end;
  - a worker join plus the plugin;
  - stop latency with Loki down;
  - `logging off`;
  - surviving an upgrade;
  - no password in `journalctl -u docker`.
- **Slice 3 (12 checks):** design doc, slice 3, `### Testing`, "Real host, before the release". They
  cover:
  - sign-in with a password and with SSO;
  - header-alias spoofing and a forged `X-Forwarded-Method`;
  - network isolation;
  - sign-out and demotion;
  - Grafana stopped;
  - a switch with a job running (`StartedAt` unchanged);
  - upgrade;
  - certificates;
  - code replay.
- **Slice 2 (7 checks):** these lived only as plan Step 9 text, a git-ignored task report and the
  external spec, so they are kept here:
  1. Save retention 14 days: Loki restarts once, the file says `336h`, and pushes resume.
  2. Save S3 with a wrong secret → 400; with the right one → `ok`. On the cutover day, after the idle
     period, objects appear in the bucket, and a query spanning the cutover returns lines from both
     sides.
  3. Rotate the secret, and prove `AWS_SHARED_CREDENTIALS_FILE` is re-read on restart. This rests on
     AWS SDK docs, not Loki source.
  4. `pstack upgrade` after the S3 save: `config.yaml` is unchanged, both periods are present, and Loki
     is ready.
  5. `pstack logging off`, then `loki`: the settings come back, with no second restart.
  6. `docker kill` pstack during the ready wait: after pstack restarts, the resume finishes without a
     second Loki restart, and `previous_config` is NULL.
  7. `ls -ln control/loki`: `s3-credentials` is `-rw------- 10001`.

### 6b. Browser evidence that was run

All of these ran in headless Chrome over CDP against `pstack serve`, with a fake `docker` built from
the conformance shims: `LOKI_SHIM` and `NODE_PLUGINS` in `packages/conformance/gen/goldens.table.ts`,
and `GRAFANA_SHIM` in `packages/conformance/test/api-grafana.test.ts`. None ran against a real
host.

| Check | What it showed | Evidence | Durable? |
|---|---|---|---|
| Slice 1, T11: Swarm page at 320px, dark and light | The banner wraps correctly. The node badge overflowed (scrollWidth 358 at 320) and was fixed with a scoped style. This was a render of the real CSS with SwarmView's markup, not the running app. | L1 ledger, T11 | No; the screenshots are gone |
| Slice 2, T14: Logging panel | The Filesystem panel with its inputs, `Save` and `Restarts Loki.`, and an ErrorNote showing a server 409 over the S3 form with the secret masked | `panel.png`, `error-note.png` in the session scratchpad (`…/scratchpad/loki-ui/`) | **No.** `/private/tmp` is wiped on session restart |
| Slice 3, T10 rounds 1–5: the rail | The Grafana link sits directly below `Jobs`, styled like its neighbours, `target=_blank` | `t10-r{2..5}-rail-grafana.png` in the scratchpad | No |
| Slice 3, T10 out of band: role gating and next handling | **admin, developer, maintainer:** link href is `https://grafana.preview.example.com`. **viewer:** no link. **A developer demoted to viewer:** the link disappears. **A stored token, with or without a session:** no link. **An `/api/` next:** full document navigations to `/api/auth/grafana/start` and then `grafana…/-/pstack/callback?code=…`; the final `chrome-error://` is expected, because the fake Grafana host does not resolve. **An SPA next:** stays in the app (`/deployments`). **A protocol-relative next:** stays same-origin. **Basic UI:** loads and signs in (`basic-before.png`, `basic-after.png`). | `loki-artifacts/t10-browser/out.json` and 13 PNGs in `shots/` | **Yes** (outside the repo) |

### 6c. Automated coverage

- **Conformance:** 303 tests across 26 files (`packages/conformance/expected-pass.json`). Loki
  logging added:
  - `api-logging` (3): injection under compose, the missing-plugin refusal, and swarm naming the node.
  - `api-loki-settings` (8): defaults, a save with restart, a refused config, ready timeout →
    rollback, the refusal codes, a revert queued during an apply, S3, and the boot reconcile.
  - `api-grafana` (4): the verify → start → callback round trip, sign-out, the `grafana.` 503, and the
    logging-off 404s.
  - One `/api/swarm` plugin test in `api-share-sleep-swarm`, and RBAC rows for `/api/logging`.
  - 13 CLI goldens (`cli-goldens` runs 94 tests) and two render cells:
    `golden/render/control/{http01-basic-compose-loki,dns01-advanced-swarm-loki}`.
- **Go:**
  - `internal/loki`: `loki_test`, `probe_test`, `periods_test`, `render_test`.
  - `api`: `loki_apply_test`, `routes_logging_test`, `routes_grafana_test`.
  - Loki cases in the `initctl`, `upgrade`, `autolabel`, `compose`, `swarm`, `cloudinit`, `inspect`,
    `cli`, `jobs`, `notify`, `store` and `redact` tests.

---

## 7. History

| What | Where |
|---|---|
| Design and plans | PR #59 (`claude/loki-logging-design`): `d499bea`, `5780904`, `ae87a02`. Later plan fixes: `e9c8504`, `198298d`. |
| Slice 1 | PR #60 (`claude/loki-logging`): `e7e98b2` … `cb00485`, then `7b635ba`, `dab98bf`, `d5edcaa`, `0edd107`, `dd16f7e`, `ce632cb`, `3250d43`. |
| Slice 2 | PR #62 (`claude/loki-logging-settings`): `0ca46d2` … `bf2985f`, then `986d468` (`.next` cleanup on every exit). |
| Slice 3 | PR #63 (`claude/loki-logging-grafana`): `20f7135` … `5365074`, then `9c569ff` (Grafana stays on across a missed `docker ps`). |
| Release | PR #64 (`claude/release-0.40.0`): `7081b1a`. Tag `v0.40.0` was created with `gh api`. |

The PRs were stacked with GitButler and rebase-merged. SHAs quoted in the ledgers from before the
merge (for example `7ebfa66`) are not on main; find commits by their message.

---

## Sources that are not in a clone

| Name used here | Path | Holds |
|---|---|---|
| L1, L2, L3 ledgers | `.superpowers/sdd/loki-logging-slice-{1,2,3}-plan/progress.md` | Every ruling, with the task reviews, fix rounds and parks. Git-ignored through `.git/info/exclude`, so they exist in the owner's checkout only. |
| loki-artifacts | `~/.claude/projects/-Volumes-S1-code-preview-stacks/loki-artifacts/` | `slice-2-spec.md` and `slice-3-spec.md` (the binding specs for slices 2 and 3), `loki-decisions.md`, `pr/*.md` (PR bodies), and `t10-browser/` (§6b). Also stale Sep 16 copies of the slice-2/3 plans (`loki-logging-slice-{2,3}-plan.md`); the `docs/` plans win. |

If a ledger and this document disagree, the tree wins, then this document, then the ledger.
