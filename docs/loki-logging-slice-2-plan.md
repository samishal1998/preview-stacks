# Loki logging, slice 2 (Loki settings in the UI) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admins and maintainers change Loki chunking and retention from the Control page, and admins move storage from filesystem to S3-compatible, applied by rendering, verifying, swapping and restarting only Loki — with rollback.

**Architecture:** A singleton `loki_config` table (migration 9) holds what an operator saved; `internal/loki` validates and renders it over slice 1 fixed config; a `loki-apply` job verifies the render in a throwaway Loki container, swaps the files, restarts Loki through the existing control-restart path and rolls back on failure. After slice 2, `init` only creates the config when absent; the API owns its content and reconciles at boot.

**Tech Stack:** Go 1.23 stdlib (SigV4 probe included), SQLite migrations, bun:test conformance, Vue 3 advanced UI, TypeScript client, GitButler.

**Spec:** `docs/loki-logging-design.md`, section *Slice 2* (landed by this plan's first task from the reviewed draft).

**Branch:** `claude/loki-logging-settings`

## Global Constraints

- Spec of record: scratchpad/slice-2-spec.md (all 891 lines). Exact values, messages, routes, clamps and tests there win over any paraphrase here. Context: docs/loki-logging-design.md:352-380 (the owner's slice-2 decisions).
- Slice 1 is not finished in the tree. `but status` shows claude/loki-logging with 3 commits (plan T1-T3). Names from slice-1 T4-T14 come from docs/loki-logging-slice-1-plan.md Produces, amended by rulings R1-R21 in .superpowers/sdd/loki-logging-slice-1-plan/progress.md: inspect.LokiPushURL (plan:3409), upgrade.SwitchLogging (plan:1676), LOKI_SHIM in gen/goldens.table.ts (R15), the loki render cells (T13), the `pstack logging` section in usage.md (T6, plan:3339), control-plane.md §5g (T8, plan:4498), `## Unreleased` in CHANGELOG (T14). Re-verify each against the tree at build time; the real code wins.
- Branch: claude/loki-logging-settings, stacked on claude/loki-logging. Before the first commit, run `but status` and confirm the stack order. If it is wrong, restack with `but move` (gitbutler skill). Never recreate the branch.
- Commit form: run `but status` for file IDs, then `but commit -b claude/loki-logging-settings -m "<type(scope): summary>" <ids>`. No Co-Authored-By, Claude-Session or Generated-with footer. Never commit packages/conformance/golden/host/db/* or .superpowers/. Don't push.
- Go tests: `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/<pkg>/ -run <Name>`. Every Test…/t.Run carries a `// negative control: <mutation>` line (AGENTS.md rule 17), and the mutation is actually run.
- Conformance: `cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`, then `cd packages/conformance && PSTACK_IMPL=go bun test test/<file>.test.ts`. Every new test fails under PSTACK_IMPL=null (`bun run vacuity`). expected-pass.json only goes up.
- Goldens: `cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts`. `but diff` must then show only the goldens named in T7.
- Full gate before the last commit: `cd /Volumes/S1/code/preview-stacks && bun run check`.
- Invariant 12: the API edits the bind-mounted control/loki files and restarts only the loki container, through inspect.RestartControlService (inspect/control.go:95-113). It never renders compose and never recreates a container. The mount and the env line are init's job (T7).
- Invariant 18 / rule 8: every template edit is `strings.Contains` then `strings.Replace(s, anchor, with, 1)` over an ORDERED slice. Anchor bytes are copied from templates/control/loki/config.yaml and packages/pstack/templates/control/docker-compose.yml, never retyped from the spec.
- JSON goes through structs (field order = the spec's JSON order) or jsonx.O; never map[string]any. Tri-states are pointers without omitempty (`enabled`, `updatedAt`, `s3`, `cutover`). Every []string in a response or event is non-nil (`encodings`, `changed`). Rule 14: no sink, Emit or jobs.Start call while lokiMu is held.
- Secrets: the S3 secret has no read path (invariant 15). A secret never arrives by flag or query, only in a PUT body. The transcript is scrubbed with redact.RedactText(text, secret, previousSecret, accessKeyId) (redact.go:153). config.yaml never holds a credential.
- Copy is terse, stating state not explanation (owner rule, R18). The UI follows docs/ui-rules.md. The only UI strings are those listed in spec lines 637-674.
- No new Go module and no npm dependency. The probe uses stdlib only (crypto/hmac, crypto/sha256, net/http).
- Each task folds in the docs for its own behaviour: usage.md, control-plane.md, webhook-events.md, templates/control/README.md, and its own CHANGELOG bullet under `## Unreleased` (create the heading if slice 1's T14 has not). Each task ends green on its own package tests.
- Do not plan or implement slice 3. Keep the seams in contractNotes stable for it.

## File map

| Path | Action | Responsibility |
|---|---|---|
| `packages/pstack/internal/store/migrations.go` | modify | Migrations[8] = version 9: the loki_config table (T1) |
| `packages/pstack/internal/store/store_test.go` | modify | migration 9 test: empty, additive, CHECK(id=1) (T1) |
| `packages/pstack/internal/loki/loki.go` | create | header = the design record; Settings/Chunks/Storage/S3, Defaults, Limits, EarliestCutover, Lead, Validate, Error/IsError, sentinel conflicts, Credentials, CredentialsOwner, WriteFile, Writable, Dir, var now/chown/geteuid (T2); Row, Read/Save/Finish/Revert, ChunksPatch/StoragePatch, Merge, Changed (T4) |
| `packages/pstack/internal/loki/render.go` | create | Render via ordered literal anchors over pstack.LokiConfig (T2) |
| `packages/pstack/internal/loki/periods.go` | create | Period, Periods (yamlx), CheckPeriods (T3) |
| `packages/pstack/internal/loki/probe.go` | create | Probe: SigV4 PUT+DELETE of pstack-probe-<16hex> (T5) |
| `packages/pstack/internal/loki/loki_test.go` | create | Validate, limits, credentials, chown seam (T2); Row store + Merge/Changed transitions (T4) |
| `packages/pstack/internal/loki/render_test.go` | create | Render byte-equality and anchors (T2) |
| `packages/pstack/internal/loki/periods_test.go` | create | CheckPeriods rules (T3) |
| `packages/pstack/internal/loki/probe_test.go` | create | SigV4 KAT, httptest path/virtual-hosted, 403, 302, DELETE refusal (T5) |
| `packages/pstack/internal/jobs/jobs.go` | modify | LokiApply action; noAssertGone map in emitTerminal (T6) |
| `packages/pstack/internal/jobs/jobs_test.go` | modify | verified:null for loki-apply case (T6) |
| `packages/pstack/internal/events/events.go` | modify | append logging.changed (T6) |
| `packages/pstack/internal/events/events_test.go` | modify | TestNames string gains logging.changed (T6) |
| `packages/pstack/internal/notify/notify.go` | modify | actionWord loki-apply; Summarize logging.changed (T6) |
| `packages/pstack/internal/notify/notify_test.go` | modify | Summarize/actionWord cases (T6) |
| `docs/webhook-events.md` | modify | loki-apply in both action rows, verified null, logging.changed catalogue (T6) |
| `packages/pstack/internal/initctl/init.go` | modify | create-if-absent config write + [dry-run] keep; LokiService env line; LokiWiring anchor 4 (T7) |
| `packages/pstack/internal/initctl/init_test.go` | modify | keep, env line, anchor 4 tests (T7) |
| `packages/pstack/templates/control/README.md` | modify | s3-credentials 0600 row; pstack mounts ./loki when logging is on (T7) |
| `packages/conformance/golden/render/control/http01-basic-compose-loki/docker-compose.yml` | modify | regenerated: pstack mount + env line (T7) |
| `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml` | modify | regenerated (T7) |
| `packages/conformance/golden/cli/init-dry-http01-basic-compose-loki.json` | modify | regenerated compose byte count (T7); same for init-dry-dns01-advanced-swarm-loki.json and any *-loki dry run printing the compose write line |
| `packages/pstack/internal/api/server.go` | modify | Options.LokiDir/LokiReadyTimeoutMs/LokiUID + New defaults, lokiMu/lokiGen/pending fields and test seams, fail() gains loki.IsError (T8); reconcileLoki call after reconcileDomains (T11) |
| `packages/pstack/internal/api/http.go` | modify | Tuning LokiReadyTimeoutMs, LokiUID (T8) |
| `packages/pstack/internal/api/http_test.go` | modify | TuningFromEnv + option plumbing tests (T8) |
| `packages/pstack/internal/cli/serve.go` | modify | LokiDir: loki.Dir(dataDir), the two knobs (T8) |
| `packages/pstack/internal/config/config.go` | modify | Assemble Skipped line when a loki row exists (T4) |
| `packages/pstack/internal/config/config_test.go` | modify | Skipped line test (T4) |
| `packages/pstack/internal/api/loki_apply.go` | create | pending entries, startLokiApply, the apply work (steps 1-10), rollback, resume, reconcileLoki (T9-T11) |
| `packages/pstack/internal/api/loki_apply_test.go` | create | exec.Fake command order, failures, rollback, cancel, pending patches, resume, boot (T9-T11) |
| `packages/pstack/internal/api/routes_logging.go` | create | header = design record; GET /api/logging, PUT /api/logging, PUT /api/logging/storage, body parsing (T12) |
| `packages/pstack/internal/api/routes_logging_test.go` | create | handler step order, 409/503/400/200/202 (T12) |
| `packages/pstack/internal/api/routes.go` | modify | three literals after the TLS block (T12) |
| `packages/pstack/internal/api/permissions.go` | modify | two rows after the TLS rows (T12) |
| `packages/pstack/internal/api/permissions_test.go` | modify | expectations for the three routes (T12) |
| `packages/pstack/api/openapi.yaml` | modify | tag logging + 3 operations (T12) |
| `packages/pstack/internal/apicli/zz_generated.go` | modify | regenerated (T12) |
| `packages/pstack/internal/apicli/oascmd.lock.json` | modify | regenerated (T12) |
| `packages/pstack/internal/apicli/apicli.go` | modify | OperationCount 79 → 82 (T12) |
| `packages/conformance/test/api-rbac.test.ts` | modify | three bodiless rows (T12) |
| `packages/client/src/types.ts` | modify | JobAction + loki-apply; LokiChunks, LokiS3, LokiStorage, LokiLimits, LokiSettings, LokiStorageInput (T13) |
| `packages/client/src/index.ts` | modify | logging block after settings (T13) |
| `packages/client/test/client.test.ts` | modify | logging.get + set refusal (T13) |
| `apps/ui/src/api/types.ts` | modify | mirror types; JobAction (T14) |
| `apps/ui/src/composables/useFormat.ts` | modify | ACTION_LABELS 'loki-apply': 'Loki settings' (T14) |
| `apps/ui/src/views/ControlView.vue` | modify | Logging panel after Certificates (T14) |
| `packages/conformance/test/api-loki-settings.test.ts` | create | black-box tests 1-8 (T15) |
| `packages/conformance/expected-pass.json` | modify | new file count (T15) |
| `docs/usage.md` | modify | env rows (T8); Loki settings section, matrix rows, curl, config.yaml is pstack's (T12); upgrade keeps config.yaml (T7) |
| `docs/control-plane.md` | modify | §5g additions: period guard (T3), probe (T5), apply (T9), rollback (T10), resume/boot + ownership (T11) |
| `docs/README.md` | modify | loki design row: slice 2 built (T16) |
| `docs/loki-logging-design.md` | modify | banner: slice 2 built, deviations listed (T16) |
| `packages/pstack/CHANGELOG.md` | modify | ## Unreleased bullets per task; T16 orders them and names every golden change |

## Decisions the plan fixed

- Real init.go lines are post-slice-1, and the spec's init.go cites are pre-slice-1: write() is at init.go:967-976, the config write at :459-463, GOMEMLIMIT inside LokiService at :812, LokiWiring at :888-906 (anchor slice :893-898), and identity at :875. The anchor 4 line is docker-compose.yml:149, unique.
- Render anchor bytes, checked with `cat -vet`: `    directory: /loki/chunks\n`; `        period: 24h                 # tsdb requires 24h\n` (17 spaces before #); `  chunk_idle_period: 30m`; `  max_chunk_age: 2h`; `  chunk_target_size: 1572864`; `  chunk_encoding: snappy`; `  query_ingesters_within: 3h`; `  retention_period: 168h`; `  max_query_lookback: 168h`; `  delete_request_store: filesystem`. They are prefixes, so trailing comments survive, and each is unique in the file.
- Names fixed here (the spec left them open): package files loki.go, render.go, periods.go, probe.go. Row{Settings, Secret, UpdatedAt, InFlight, Previous *Row}, where Read returns nil when empty. ChunksPatch, StoragePatch, Merge, Changed, LimitsAt, Range, Limits, EarliestCutover, Lead, CredentialsOwner, WriteFile, Writable, Dir. ConfigFile, CredentialsFile, NextSuffix. StorageFilesystem, StorageS3. ErrOneWay, ErrFixed, ErrNeedsRoot. api: loki_apply.go (lokiEntry, lokiPut, lokiTake, lokiPending, startLokiApply, lokiRollback, reconcileLoki, lokiLead) and routes_logging.go (loggingGet, loggingPut, loggingStoragePut, parseChunks, parseStorage, loggingView, loggingStorageView, loggingS3View). Server seams: lokiRunner, lokiPoll, lokiNow.
- Error taxonomy: *loki.Error → 400 through fail() (server.go:942-944). ErrOneWay, ErrFixed and ErrNeedsRoot are plain sentinel errors that handlers map to 409 with errors.Is, like inspect.ErrNoDocker. A 503 is written by the handler (routes_control.go:51).
- jobs.Work has no job id and jobs.Job has no by (jobs.go:156-234). The work gets its id through a cap-1 channel sent after Start returns ok. `by` appears only as the transcript's first line (`by <actor>`, or `by pstack (boot)`) and in logging.changed. No wire field is added.
- Test seams for api: after New, tests set s.host (the GET and PUT step 1) and s.lokiRunner (job and post runners) to an exec.Fake with Answer (exec.go:257-300; http_test.go:341-356 precedent). They set s.lokiPoll to milliseconds and pin s.lokiNow. loki has package vars now, chown, geteuid and probeClient.
- Exact docker argv from the API (Fake sees quotes; the shim sees them stripped): `docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'`, `docker inspect 'l1'`, `docker run --rm --network none --volumes-from 'l1:ro' 'grafana/loki:3.7.7' -config.file=/etc/loki/config.yaml.next -verify-config`, `docker restart 'l1'`, `docker exec 'l1' /usr/bin/loki -health`, `docker logs --since '<RFC3339>' 'l1'`. The container path /etc/loki is Loki's mount (init.go:820) and pstack's anchor-4 mount.
- The image, id and StartedAt for steps 1 and 3 come from inspect.ControlRuntime (ContainerInfo.ID, Image, StartedAt ms, Service; inspect.go:61-80). No new inspect function is needed, and inspect.LokiPushURL (slice-1 T7) is not used by slice 2.
- No AssertNoNilCollections helper exists in Go or conformance (grep found none, despite AGENTS.md:347). Tests assert `[]` and never `null` on the marshalled bytes for encodings and changed.
- Permissions: two rows cover three routes (the domains precedent, permissions.go:158). TestEveryDispatchedRouteHasARow scans routes.go literals (permissions_test.go:190-192).
- OpenAPI: components have no 503 response (openapi.yaml:901-908), and /api/control/restart doesn't document its 503, so none is added. Adding 3 operations bumps apicli.OperationCount 79 → 82 (apicli.go:27; cli/api_test.go:50). The help goldens print no count, so they don't move. cli-api.test.ts:106 checks 5 group names only.
- server.go's header (server.go:1-9) holds no per-route list, despite AGENTS.md:88 and spec:321. Nothing to add unless one exists at build time.
- Basic embedded UI: jobs render `{{ j.action }}` raw (ui/index.html:409, 1231). There is no label map, so spec:428/876's basic-UI copy is a no-op.
- Tuning's num() treats 0 as unset (http.go:61-71): PSTACK_LOKI_UID=0 means 10001. Harmless, because euid 0 chowns to 10001 regardless.
- New defaults an empty LokiDir to <DataDir>/control/loki (like routingDir, server.go:204-207). Otherwise every existing New(Options{DataDir: t.TempDir()}) test would stat /config.yaml. serve.go passes loki.Dir(dataDir) (env → /etc/loki → fallback).
- Slice-1 dependencies not yet in the tree (verify at build time against the tree): control-plane.md §5g (T8, plan:4498), usage.md `pstack logging` section (T6, plan:3339), the two *-loki render cells and init-dry-*-loki transcripts (T13), CHANGELOG `## Unreleased` (T14; create it if absent), and docs/README.md and the design banner as T14 leaves them. The goldens in T7 exist only after slice-1 T13 lands; slice 2 starts after slice 1 lands.
- Permissions and job queue: the apply shares the pstack-control key. A waiting preempting `down` from a squatting deployment refuses Start (jobs.go:597-605), which the PUT answers with 409 `pstack-control is busy with a teardown — retry`. The pending entry stays and is carried by the next save.
- A swap failure after the step-4 commit (rename error) enters the rollback path. The spec's table has no row for it, and this is the only safe direction: the row already holds the save and previous_*.
- Step 9 records a `log` step only when N > 0 level=error lines, but `docker logs` always runs (spec:763-764 command order).
- Probe message formats: `S3 refused the probe: 403 InvalidAccessKeyId` (the code is omitted when it fails the charset check), `S3 unreachable: <err>`, `S3 refused the probe delete: 403 AccessDenied — pstack-probe-<hex> is left in the bucket`. The PUT uses r.Context() under the client's 10s timeout, so the two requests take at most 20s.
- Order and dependency: T1, T2, T6 and T7 are independent roots. T3, T4 and T5 need T2. T8 needs T2. T9 needs T3, T4, T6 and T8. Then T10 → T11 → T12, which also needs T5. T13 and T14 need T12. T15 needs T7 and T12. T16 closes. dependsOn also serializes shared-file edits: loki.go (T2 → T4), loki_apply.go (T9 → T11), control-plane.md (T3, T5, T9-T11), usage.md (T7, T8, T12), CHANGELOG (most tasks). Execute in id order.

## Order

Execute in task order.

| Task | Title | Depends on |
|---|---|---|
| 1 | store: migration 9 — the loki_config table | — |
| 2 | loki: settings, clamps, limits, render, credentials, files | — |
| 3 | loki: schema periods and the period guard | 2 |
| 4 | loki: the row (Read/Save/Finish/Revert), patches, Merge, Changed; config Skipped | 1, 2 |
| 5 | loki: the S3 probe | 2 |
| 6 | jobs, events, notify: the loki-apply action and logging.changed | — |
| 7 | initctl: config.yaml is kept; pstack mounts ./loki; Loki reads AWS_SHARED_CREDENTIALS_FILE | — |
| 8 | api: Loki options, tuning knobs, the directory, fail() | 2 |
| 9 | api: the loki-apply job — pending patches and the forward path | 3, 4, 6, 8 |
| 10 | api: rollback, leave-in-place, and cancellation after the swap | 9 |
| 11 | api: resume and the boot reconcile | 10 |
| 12 | api: GET /api/logging, PUT /api/logging, PUT /api/logging/storage | 5, 11 |
| 13 | client: logging.get/set/setStorage | 12 |
| 14 | UI: the Logging panel on the Control page | 12 |
| 15 | conformance: api-loki-settings.test.ts | 7, 12 |
| 16 | docs status, CHANGELOG, full gate, real-host checklist | 13, 14, 15 |

---

### Task 1: store: migration 9 — the loki_config table

**Files:**
- Modify: `packages/pstack/internal/store/migrations.go` (append after the closing `` `, `` of the `// 8 — host settings` entry, before the slice's `}`; today :230-234. Slice 1 adds no migration, spec:60)
- Test: `packages/pstack/internal/store/store_test.go` (the import block :3-7, the table list :42, and a new pair of `t.Run`s after the migration-8 subtest that ends at :168)

**Interfaces:**
- Consumes: `store.Open(dataDir string) (*Store, error)` (store.go:64), `(*Store).migrate` (store.go:102-120: version `v` runs `Migrations[v-1]`), `var Migrations []string` (migrations.go:9).
- Produces:
  - `store.Migrations[8]`, so `len(store.Migrations) == 9` and `PRAGMA user_version` is 9 on a fresh or migrated database.
  - The table T4 binds to: `loki_config (id INTEGER PRIMARY KEY CHECK (id = 1), config TEXT NOT NULL, secret TEXT NOT NULL, previous_config TEXT, previous_secret TEXT, updated_at INTEGER NOT NULL)`.
  - Its meaning: an empty table is slice 1's `pstack.LokiConfig`. `previous_config IS NOT NULL` means an apply is in flight. `previous_config = ''` means the table was empty before that save.
  - The fixture idiom for the next migration: `Migrations = saved[:9]` builds a real v9 database.

- [ ] **Step 1: Write the failing tests**

In `packages/pstack/internal/store/store_test.go`, replace the import block:

```go
import (
	"os"
	"path/filepath"
	"testing"
)
```

with:

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

In the first subtest, replace the table list (it checks every table the migrations declare):

```go
		for _, tbl := range []string{"users", "sessions", "tokens", "notifiers", "deliveries", "terminal_sessions", "host_vars", "sso_providers", "sso_links", "sso_state", "settings"} {
```

with:

```go
		for _, tbl := range []string{"users", "sessions", "tokens", "notifiers", "deliveries", "terminal_sessions", "host_vars", "sso_providers", "sso_links", "sso_state", "settings", "loki_config"} {
```

Then replace the end of the migration-8 subtest:

```go
			t.Fatalf("the v7 data did not survive migration 8: %q %v", region, err)
		}
	})
```

with:

```go
			t.Fatalf("the v7 data did not survive migration 8: %q %v", region, err)
		}
	})

	t.Run("migration 9 adds an EMPTY loki_config table and touches nothing else", func(t *testing.T) {
		// negative control: delete the CREATE TABLE from migration 9's string, leaving the entry (so
		// user_version still reaches 9 and the failure is about the table, not the version) → the
		// SELECT COUNT(*) FROM loki_config errors with "no such table" and this fails.
		//
		// An empty loki_config is slice 1's fixed config, so every host that migrates through this —
		// the checked-in host fixture included — runs Loki exactly as before.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:8]
		s, err := Open(dir) // a real v8 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='loki_config'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v8 already has a loki_config table (%d, %v)", n, err)
		}
		// A row that predates the migration, to prove the hop is additive.
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 9
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM loki_config").Scan(&n); err != nil || n != 0 {
			t.Fatalf("loki_config after migration 9: %d rows, %v — it must exist and be empty", n, err)
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v8 data did not survive migration 9: %q %v", region, err)
		}
	})

	t.Run("loki_config holds one row: a second id violates CHECK (id = 1)", func(t *testing.T) {
		// negative control: drop `CHECK (id = 1)` from migration 9 → the id = 2 INSERT succeeds and this fails
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		// id 1 with previous_* NULL is the row at rest: the same column list, accepted.
		if _, err := s.DB.Exec("INSERT INTO loki_config (id, config, secret, updated_at) VALUES (1, '{}', '', 1)"); err != nil {
			t.Fatal(err)
		}
		_, err = s.DB.Exec("INSERT INTO loki_config (id, config, secret, updated_at) VALUES (2, '{}', '', 1)")
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("a second loki_config row: %v, want a CHECK constraint failure", err)
		}
	})
```

The id=1 insert succeeds with the same column list, so the id=2 failure can only come from the constraint. The substring match pins that. modernc.org/sqlite v1.39.0's message contains `CHECK constraint failed`, checked while drafting.

- [ ] **Step 2: Run the tests and watch them fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/store/ -run TestOpen
```

Expected: `FAIL`, with exactly these three lines (line numbers are advisory):

```
store_test.go:46: table loki_config missing
store_test.go:201: loki_config after migration 9: 0 rows, SQL logic error: no such table: loki_config (1) — it must exist and be empty
store_test.go:223: SQL logic error: no such table: loki_config (1)
```

- [ ] **Step 3: Implement migration 9**

In `packages/pstack/internal/store/migrations.go`, replace the tail of entry 8 and the slice's closing brace. The anchor is unique: only `settings` has `updated_at INTEGER` with a single space, and the `}` follows it.

```go
    updated_at INTEGER NOT NULL
  );
  `,
}
```

with:

```go
    updated_at INTEGER NOT NULL
  );
  `,
	// 9 — Loki's settings (logging slice 2).
	`
  -- 9 — Loki's settings (logging slice 2). ONE row: one Loki per host.
  --
  -- CONFIGURATION AN OPERATOR SAVED, not a record of what is running (invariant 10). What Loki runs
  -- is control/loki/config.yaml plus its container, and every decision about Loki's schema periods
  -- reads that file, never this row. An EMPTY table means slice 1's fixed config.
  CREATE TABLE loki_config (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    -- JSON, loki.Settings WITHOUT the secret.
    config          TEXT NOT NULL,
    --
    -- The S3 secret access key, '' on filesystem. STORED, NOT HASHED — the notifier-secret precedent:
    -- pstack must WRITE it into the credentials file Loki reads. No route returns it; a read answers
    -- secretSet. The protection is the 0700 db directory and the absence of a read path.
    secret          TEXT NOT NULL,
    --
    -- WRITE-AHEAD FOR ONE APPLY. The row as it stood before this save, stored in the same statement
    -- as the save and cleared when the apply finishes or is undone. Non-NULL when a job starts means
    -- an apply was cut off; the job finishes or undoes it. '' means the table was empty before.
    previous_config TEXT,
    previous_secret TEXT,
    updated_at      INTEGER NOT NULL
  );
  `,
}
```

The SQL and its comments are spec:63-84, verbatim. The raw string contains no backtick. Entries 1-8 are untouched, because a shipped migration is never edited (AGENTS.md "A database change"). No CHANGELOG edit here: nothing reads the table until T4, and T16's Added bullet names the migration.

- [ ] **Step 4: Run the tests and watch them pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/store && go vet ./internal/store/ && go test -race -timeout 120s ./internal/store/
```

Expected: `gofmt -l` and `go vet` print nothing, then `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/store` (followed by the elapsed time). The module is the repo root (go.mod:1 `module github.com/samishal1998/preview-stacks`), so the package path carries `packages/pstack/`.

- [ ] **Step 5: Run the negative controls against the real file**

Control 1: in `migrations.go`, delete the block from `  CREATE TABLE loki_config (` through its `  );`, keeping entry 9's comments and the `` ` `` / `` `, `` lines. Then run:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/store/ -run 'TestOpen/migration_9'
```

Expected: `FAIL` with `store_test.go:201: loki_config after migration 9: 0 rows, SQL logic error: no such table: loki_config (1) — it must exist and be empty`. SQLite accepts an entry made only of comments, so `user_version` still reaches 9. Restore the block.

Control 2: change `    id              INTEGER PRIMARY KEY CHECK (id = 1),` to `    id              INTEGER PRIMARY KEY,`. Then run:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/store/ -run 'TestOpen/loki_config_holds'
```

Expected: `FAIL` with `store_test.go:227: a second loki_config row: <nil>, want a CHECK constraint failure`. Restore the line.

Re-run Step 4's command and expect `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/store`. Then run `but diff`: it must show only the `migrations.go` hunk from Step 3 and the three `store_test.go` hunks from Step 1.

- [ ] **Step 6: The golden host still opens**

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts
```

Expected: 34 pass, 0 fail, which matches `"test/host-fixture.test.ts": 34` in `expected-pass.json`. The v6 fixture migrates through 7, 8 and 9. `expected-pass.json` doesn't change in this task.

- [ ] **Step 7: Commit**

This is slice 2's first commit, so the branch has to be created stacked. `but commit -b` alone would create an unstacked branch.

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

- If `claude/loki-logging-settings` is absent, run `but branch new claude/loki-logging-settings -a claude/loki-logging`, then `but status`. Confirm that `claude/loki-logging-settings` sits above `claude/loki-logging` in the same stack.
- If it exists but is not stacked there, run `but move claude/loki-logging-settings --above claude/loki-logging`.
- If `claude/loki-logging` shows `(merged upstream)`, run `but pull` first, then `but branch new claude/loki-logging-settings` with no anchor.

Commit by file ID. Never commit bare: the uncommitted area also holds `packages/conformance/golden/host/db/pstack.db-shm` and `-wal`, which are never committed.

```bash
but commit -b claude/loki-logging-settings -m "feat(store): migration 9 — the loki_config table" <migrations.go id> <store_test.go id>
```

---

### Task 2: loki: settings, clamps, limits, render, credentials, files

**Files:**
- Create: `packages/pstack/internal/loki/loki.go`
- Create: `packages/pstack/internal/loki/render.go`
- Modify: `packages/pstack/CHANGELOG.md` (anchor edit by quoted text; slice-1 T14 may already have added `## Unreleased`)
- Test: `packages/pstack/internal/loki/loki_test.go`
- Test: `packages/pstack/internal/loki/render_test.go`

**Interfaces:**
- Consumes: `pstack.LokiConfig` (`packages/pstack/assets.go:23-28`, built by slice-1 T2). The render anchors are copied from `packages/pstack/templates/control/loki/config.yaml` lines 18, 28, 31-34, 39, 42-43 and 48. `cat -vet` confirmed each is unique in the file, and the period line has 17 spaces before `#`.
- Produces (exported):
  - Types: `Settings{RetentionDays; Chunks; Storage}`, `Chunks{IdlePeriodMinutes; MaxAgeMinutes; TargetSizeKiB; Encoding}`, `Storage{Type; S3 *S3}` (no omitempty), `S3{Endpoint; Region; Bucket; PathStyle; AccessKeyID; Cutover}`, with the JSON tags from spec:109-133.
  - Constants: `StorageFilesystem`, `StorageS3`, `ConfigFile = "config.yaml"`, `CredentialsFile = "s3-credentials"`, `NextSuffix = ".next"`.
  - `func Defaults() Settings`
  - `type Range{Min; Max}`, `type Limits{RetentionDays; IdlePeriodMinutes; MaxAgeMinutes; TargetSizeKiB Range; Encodings []string; EarliestCutover string}`
  - `func LimitsAt(t time.Time, lead time.Duration) Limits`, `func EarliestCutover(t time.Time, lead time.Duration) string`, `func Lead(readyTimeout time.Duration) time.Duration`
  - Errors: `type Error struct{ Msg string }`, `func IsError(err error) bool` (errors.As, so a wrapped `*Error` counts). The sentinels `ErrOneWay`, `ErrFixed` and `ErrNeedsRoot` are plain errors, not `*Error`.
  - `func Validate(s Settings, secret string, addingS3 bool, lead time.Duration) error`
  - `func Render(s Settings) (string, error)`, `func Credentials(keyID, secret string) string`
  - `func CredentialsOwner(lokiUID int) error`, `func WriteFile(path, body string, mode os.FileMode, lokiUID int) error` (it can return `ErrNeedsRoot`), `func Writable(dir string) bool`, `func Dir(dataDir string) string`
- Produces (package-internal, for T3/T4/T5 in the same package):
  - Seams: `var now = time.Now`, `var chown = os.Chown`, `var geteuid = os.Geteuid`.
  - Values and helpers: `bounds` (the one Limits value Validate enforces), `check(s Settings) error` (Validate minus the secret and the cutover floor, which Render also runs), `endpoint(raw string) (host string, insecure, ok bool)`, `iniValue(v string, min int) bool`, `render(template string, s Settings) (string, error)`, `dur(m int) string`.
  - Constants: `lokiMount = "/etc/loki"`, `chunksDirLine`, `periodLine`, `deleteStoreKey`.
  - Test helpers in `loki_test.go`: `pin(t, at)`, `asEuid(t, euid) *[]string`, `s3Settings() Settings`, `testSecret`.

Both test files are `package loki` (internal), not `loki_test`, because they pin `now`, `geteuid` and `chown` and call unexported `render`. No test in this package may call `t.Parallel`, because the seams are package vars. The top-level `Test…` functions carry no control line of their own, and every `t.Run` has one. That is the accepted precedent (`swarm_test.go:377`, ruled in `.superpowers/sdd/loki-logging-slice-1-plan/progress.md:220`).

- [ ] **Step 1: Write the failing test for settings, clamps, limits, credentials and files**

Create `packages/pstack/internal/loki/loki_test.go`:

```go
package loki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

// Internal tests (package loki): they pin now, geteuid and chown and call render. None runs in
// parallel, because the seams are package vars.

const testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

// s3Settings is a valid S3 save: AWS, virtual-hosted, cutover 2026-09-16. A fresh pointer each call.
func s3Settings() Settings {
	s := Defaults()
	s.Storage = Storage{Type: StorageS3, S3: &S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE", Cutover: "2026-09-16",
	}}
	return s
}

// pin sets the package clock for one test.
func pin(t *testing.T, at time.Time) {
	t.Helper()
	was := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = was })
}

// asEuid pins the euid and records every chown as "path uid gid".
func asEuid(t *testing.T, euid int) *[]string {
	t.Helper()
	wasEuid, wasChown := geteuid, chown
	calls := []string{}
	geteuid = func() int { return euid }
	chown = func(path string, uid, gid int) error {
		calls = append(calls, fmt.Sprintf("%s %d %d", path, uid, gid))
		return nil
	}
	t.Cleanup(func() { geteuid, chown = wasEuid, wasChown })
	return &calls
}

func TestJSON(t *testing.T) {
	t.Run("settings marshal in spec order, s3 null on filesystem", func(t *testing.T) {
		// negative control: add omitempty to Storage.S3's tag — `"s3":null` disappears.
		for _, c := range []struct {
			in   any
			want string
		}{
			{Defaults(), `{"retentionDays":7,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"},"storage":{"type":"filesystem","s3":null}}`},
			{s3Settings().Storage, `{"type":"s3","s3":{"endpoint":"https://s3.eu-central-1.amazonaws.com","region":"eu-central-1","bucket":"pstack-logs","pathStyle":false,"accessKeyId":"AKIAIOSFODNN7EXAMPLE","cutover":"2026-09-16"}}`},
		} {
			if got := string(jsonx.Must(c.in)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		}
	})

	t.Run("limits marshal in spec order, encodings an array", func(t *testing.T) {
		// negative control: set l.Encodings = nil in LimitsAt — `"encodings":null`.
		noon := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
		want := `{"retentionDays":{"min":1,"max":365},"idlePeriodMinutes":{"min":5,"max":60},"maxAgeMinutes":{"min":30,"max":180},"targetSizeKiB":{"min":512,"max":1536},"encodings":["snappy","gzip","lz4","zstd"],"earliestCutover":"2026-09-16"}`
		if got := string(jsonx.Must(LimitsAt(noon, Lead(5*time.Minute)))); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})

	t.Run("a caller editing its limits does not edit what Validate reads", func(t *testing.T) {
		// negative control: drop slices.Clone in LimitsAt — "snappy" stops validating.
		LimitsAt(time.Now(), 0).Encodings[0] = "none"
		if err := Validate(Defaults(), "", false, 0); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEarliestCutover(t *testing.T) {
	t.Run("Lead is the ready timeout plus 10m", func(t *testing.T) {
		// negative control: return readyTimeout from Lead — 5m, not 15m.
		if got := Lead(5 * time.Minute); got != 15*time.Minute {
			t.Fatalf("got %s", got)
		}
	})

	lead := Lead(5 * time.Minute) // 15m, so the window is 2×15m + 10m = 40m
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"noon UTC: tomorrow", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), "2026-09-16"},
		{"23:40 UTC: the day after", time.Date(2026, 9, 15, 23, 40, 0, 0, time.UTC), "2026-09-17"},
		{"23:30 UTC: the window is 2×lead + 10m, not lead + 10m", time.Date(2026, 9, 15, 23, 30, 0, 0, time.UTC), "2026-09-17"},
		{"23:20 UTC: a window ending at midnight is not strictly before it", time.Date(2026, 9, 15, 23, 20, 0, 0, time.UTC), "2026-09-17"},
		{"a -05:00 clock is read in UTC", time.Date(2026, 9, 15, 20, 0, 0, 0, time.FixedZone("EST", -5*3600)), "2026-09-17"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// negative control, per row: drop AddDate (noon → 2026-09-15); use lead for 2*lead (23:30 → 2026-09-16);
			// add the day only when the window's end is past its midnight (23:20 → 2026-09-16); drop .UTC() (-05:00 → 2026-09-16).
			if got := EarliestCutover(c.at, lead); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	t.Run("*Error is a 400, wrapped or not; the 409 sentinels are not", func(t *testing.T) {
		// negative control: declare ErrNeedsRoot as &Error{"the S3 credentials file needs root"} — IsError reports it.
		if !IsError(&Error{"x"}) || !IsError(fmt.Errorf("render: %w", &Error{"x"})) {
			t.Error("a *Error is not recognised")
		}
		for _, err := range []error{ErrOneWay, ErrFixed, ErrNeedsRoot, errors.New("x"), nil} {
			if IsError(err) {
				t.Errorf("%v reads as a 400", err)
			}
		}
	})
}

func TestValidate(t *testing.T) {
	pin(t, time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	lead := Lead(5 * time.Minute)

	// refused asserts err is a *Error with exactly msg.
	refused := func(t *testing.T, what string, err error, msg string) {
		t.Helper()
		if !IsError(err) || err.Error() != msg {
			t.Errorf("%s: got %v, want %q", what, err, msg)
		}
	}
	// withS3 is s3Settings with one change.
	withS3 := func(edit func(*S3)) Settings {
		s := s3Settings()
		edit(s.Storage.S3)
		return s
	}

	t.Run("every range at min-1, min, max and max+1", func(t *testing.T) {
		// negative control: `f.v > f.r.Max` → `f.v >= f.r.Max` in check — every max is refused.
		for _, c := range []struct {
			field string
			r     Range
			set   func(*Settings, int)
		}{
			{"retentionDays", Range{1, 365}, func(s *Settings, v int) { s.RetentionDays = v }},
			{"chunks.idlePeriodMinutes", Range{5, 60}, func(s *Settings, v int) { s.Chunks.IdlePeriodMinutes, s.Chunks.MaxAgeMinutes = v, 180 }},
			{"chunks.maxAgeMinutes", Range{30, 180}, func(s *Settings, v int) { s.Chunks.MaxAgeMinutes, s.Chunks.IdlePeriodMinutes = v, 5 }},
			{"chunks.targetSizeKiB", Range{512, 1536}, func(s *Settings, v int) { s.Chunks.TargetSizeKiB = v }},
		} {
			for _, v := range []int{c.r.Min - 1, c.r.Min, c.r.Max, c.r.Max + 1} {
				s := Defaults()
				c.set(&s, v)
				err := Validate(s, "", false, lead)
				if v >= c.r.Min && v <= c.r.Max {
					if err != nil {
						t.Errorf("%s=%d: %v", c.field, v, err)
					}
					continue
				}
				refused(t, fmt.Sprintf("%s=%d", c.field, v), err, fmt.Sprintf("%s must be %d–%d", c.field, c.r.Min, c.r.Max))
			}
		}
	})

	t.Run("idle above the max age is refused, equal is not", func(t *testing.T) {
		// negative control: drop the IdlePeriodMinutes > MaxAgeMinutes check — 31/30 is accepted.
		s := Defaults()
		s.Chunks.IdlePeriodMinutes, s.Chunks.MaxAgeMinutes = 30, 30
		if err := Validate(s, "", false, lead); err != nil {
			t.Fatal(err)
		}
		s.Chunks.IdlePeriodMinutes = 31
		refused(t, "31/30", Validate(s, "", false, lead), "chunks.idlePeriodMinutes must not exceed chunks.maxAgeMinutes")
	})

	t.Run("encodings: the four and nothing else", func(t *testing.T) {
		// negative control: add "none" to bounds.Encodings — it is accepted.
		for _, e := range []string{"snappy", "gzip", "lz4", "zstd"} {
			s := Defaults()
			s.Chunks.Encoding = e
			if err := Validate(s, "", false, lead); err != nil {
				t.Errorf("%s: %v", e, err)
			}
		}
		for _, e := range []string{"none", "lz4-1M", "SNAPPY", ""} {
			s := Defaults()
			s.Chunks.Encoding = e
			refused(t, e, Validate(s, "", false, lead), "chunks.encoding must be one of snappy, gzip, lz4, zstd")
		}
	})

	t.Run("storage type and shape", func(t *testing.T) {
		// negative control: return nil from check's default case — type "gcs" is accepted.
		gcs := Defaults()
		gcs.Storage.Type = "gcs"
		fsWithS3 := s3Settings()
		fsWithS3.Storage.Type = StorageFilesystem
		s3NoFields := Defaults()
		s3NoFields.Storage.Type = StorageS3
		refused(t, "gcs", Validate(gcs, "", false, lead), "type must be filesystem or s3")
		refused(t, "filesystem with s3", Validate(fsWithS3, "", false, lead), "type filesystem takes no s3 fields")
		refused(t, "s3 without s3", Validate(s3NoFields, testSecret, true, lead), "type s3 needs its s3 fields")
		if err := Validate(s3Settings(), testSecret, true, lead); err != nil {
			t.Fatalf("the valid S3 save: %v", err)
		}
	})

	t.Run("endpoint: http(s)://host[:port], nothing else", func(t *testing.T) {
		// negative control: drop `(u.Path != "" && u.Path != "/")` from endpoint — the path case is accepted.
		for _, e := range []string{"https://s3.eu-central-1.amazonaws.com", "http://minio:9000", "http://10.0.0.5:9000/"} {
			if err := Validate(withS3(func(c *S3) { c.Endpoint = e }), testSecret, true, lead); err != nil {
				t.Errorf("%s: %v", e, err)
			}
		}
		for _, e := range []string{
			"s3.eu-central-1.amazonaws.com",           // no scheme
			"https://s3.amazonaws.com/pstack-logs",    // a path
			"https://AKIA:secret@s3.amazonaws.com",    // userinfo
			"ftp://s3.amazonaws.com",                  // scheme
			"https://s3.amazonaws.com?x=1",            // query
			"https://s3.amazonaws.com#",               // an empty fragment
			"https://[::1]:9000",                      // unquotable in YAML
			"https://minio:",                          // an empty port
			"https://",                                // no host
			"https://s3.amazonaws.com\n  insecure: x", // injection
		} {
			refused(t, e, Validate(withS3(func(c *S3) { c.Endpoint = e }), testSecret, true, lead), "endpoint must be http(s)://host[:port]")
		}
	})

	t.Run("region", func(t *testing.T) {
		// negative control: regionRe `^[a-z0-9-]{1,32}$` → `^[A-Za-z0-9_-]{1,32}$` — "eu_central" is accepted.
		for _, r := range []string{"", "EU", "eu_central", strings.Repeat("a", 33)} {
			refused(t, r, Validate(withS3(func(c *S3) { c.Region = r }), testSecret, true, lead), "region must be 1–32 characters of a-z, 0-9 and -")
		}
		if err := Validate(withS3(func(c *S3) { c.Region = "garage" }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("bucket: S3 naming without a comma; a dot only path-style", func(t *testing.T) {
		// negative control: drop the `strings.Contains(c.Bucket, ".") && !c.PathStyle` check — "logs.v1" is accepted virtual-hosted.
		for _, b := range []string{"ab", "pstack,logs", "-logs", "logs-", "Logs", strings.Repeat("a", 64)} {
			refused(t, b, Validate(withS3(func(c *S3) { c.Bucket = b }), testSecret, true, lead),
				"bucket must be 3–63 characters of a-z, 0-9, - and ., starting and ending with a letter or digit")
		}
		refused(t, "logs.v1", Validate(withS3(func(c *S3) { c.Bucket = "logs.v1" }), testSecret, true, lead), "bucket may contain . only with pathStyle")
		if err := Validate(withS3(func(c *S3) { c.Bucket, c.PathStyle = "logs.v1", true }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("accessKeyId and secret: the INI charset, 1–256 and 8–256 bytes", func(t *testing.T) {
		// negative control: iniValue(secret, 7) in Validate — the 7-byte secret is accepted.
		for _, id := range []string{"", "AKIA EXAMPLE", "AKIA#1", "AKIA;1", "AKIAé", strings.Repeat("A", 257)} {
			refused(t, id, Validate(withS3(func(c *S3) { c.AccessKeyID = id }), testSecret, true, lead), "accessKeyId must be 1–256 printable ASCII characters, no space, # or ;")
		}
		for _, secret := range []string{"", "1234567", "with space!", "tab\tsecret", strings.Repeat("s", 257)} {
			refused(t, fmt.Sprintf("%q", secret), Validate(s3Settings(), secret, true, lead), "secretAccessKey must be 8–256 printable ASCII characters, no space, # or ;")
		}
		for _, secret := range []string{"12345678", strings.Repeat("s", 256)} {
			if err := Validate(s3Settings(), secret, true, lead); err != nil {
				t.Errorf("%d bytes: %v", len(secret), err)
			}
		}
		if err := Validate(withS3(func(c *S3) { c.AccessKeyID = "A" }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("cutover is a real YYYY-MM-DD", func(t *testing.T) {
		// negative control: drop the time.Parse check in check — "2026-02-30" is accepted.
		for _, d := range []string{"", "2026-9-16", "16-09-2026", "2026-02-30", "2026-09-16T00:00:00Z"} {
			refused(t, d, Validate(withS3(func(c *S3) { c.Cutover = d }), testSecret, false, lead), "cutover must be YYYY-MM-DD")
		}
	})

	t.Run("adding S3: a cutover before earliestCutover is refused, equal accepted", func(t *testing.T) {
		// negative control: `Cutover < earliest` → `Cutover <= earliest` in Validate — 2026-09-16 is refused.
		refused(t, "2026-09-15", Validate(withS3(func(c *S3) { c.Cutover = "2026-09-15" }), testSecret, true, lead), "cutover must be 2026-09-16 or later")
		if err := Validate(withS3(func(c *S3) { c.Cutover = "2026-09-16" }), testSecret, true, lead); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an S3 host rotating keys after its cutover is not held to the floor", func(t *testing.T) {
		// negative control: drop `addingS3 &&` from Validate — the rotation is refused.
		if err := Validate(withS3(func(c *S3) { c.Cutover = "2026-01-01" }), testSecret, false, lead); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCredentials(t *testing.T) {
	t.Run("exact bytes", func(t *testing.T) {
		// negative control: drop the final "\n" from Credentials — the bytes differ.
		want := "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = " + testSecret + "\n"
		if got := Credentials("AKIAIOSFODNN7EXAMPLE", testSecret); got != want {
			t.Errorf("got %q", got)
		}
	})

	t.Run("CredentialsOwner: root or Loki's own uid, nobody else", func(t *testing.T) {
		// negative control: return nil at the end of CredentialsOwner — euid 1000 is not refused.
		for _, c := range []struct {
			euid int
			want error
		}{{0, nil}, {10001, nil}, {1000, ErrNeedsRoot}} {
			asEuid(t, c.euid)
			if err := CredentialsOwner(10001); !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Errorf("euid %d: got %v", c.euid, err)
			}
		}
	})

	t.Run("root writes 0600 and chowns it to Loki's uid", func(t *testing.T) {
		// negative control: drop the chown call from WriteFile — nothing is recorded.
		calls := asEuid(t, 0)
		p := filepath.Join(t.TempDir(), CredentialsFile+NextSuffix)
		body := Credentials("AKIAIOSFODNN7EXAMPLE", testSecret)
		if err := WriteFile(p, body, 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(*calls, ","); got != p+" 10001 10001" {
			t.Errorf("chown calls %q", got)
		}
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, %v", st, err)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("body %q", got)
		}
	})

	t.Run("Loki's own uid writes as is, and config.yaml is never chowned", func(t *testing.T) {
		// negative control: drop `geteuid() == 0 &&` from WriteFile's chown condition — the euid-10001 write chowns.
		dir := t.TempDir()
		calls := asEuid(t, 10001)
		if err := WriteFile(filepath.Join(dir, CredentialsFile), "x", 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		rootCalls := asEuid(t, 0)
		if err := WriteFile(filepath.Join(dir, ConfigFile), "x", 0o644, 10001); err != nil {
			t.Fatal(err)
		}
		if len(*calls)+len(*rootCalls) != 0 {
			t.Errorf("chowned: %v %v", *calls, *rootCalls)
		}
		if st, _ := os.Stat(filepath.Join(dir, ConfigFile)); st.Mode().Perm() != 0o644 {
			t.Errorf("config.yaml mode %v", st.Mode())
		}
	})

	t.Run("any other euid is refused before a byte is written", func(t *testing.T) {
		// negative control: drop the CredentialsOwner check from WriteFile — the file is written.
		asEuid(t, 1000)
		p := filepath.Join(t.TempDir(), CredentialsFile)
		if err := WriteFile(p, "x", 0o600, 10001); !errors.Is(err, ErrNeedsRoot) {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the file exists: %v", err)
		}
	})

	t.Run("a leftover symlink is replaced, never written through", func(t *testing.T) {
		// negative control: drop the os.Remove from WriteFile — the secret lands in the link's target.
		asEuid(t, 10001)
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, CredentialsFile+NextSuffix)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(p, testSecret, 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(target); len(got) != 0 {
			t.Errorf("the target holds %q", got)
		}
		if st, err := os.Lstat(p); err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s is not a regular file: %v", p, err)
		}
	})
}

func TestWritable(t *testing.T) {
	t.Run("config.yaml present and the directory writable", func(t *testing.T) {
		// negative control: drop the config.yaml stat from Writable — the empty directory reads as writable.
		dir := t.TempDir()
		if Writable(dir) || Writable(filepath.Join(dir, "absent")) {
			t.Error("a directory without config.yaml is writable")
		}
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !Writable(dir) {
			t.Error("not writable")
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("the probe was left behind: %v", entries)
		}
	})

	t.Run("a read-only mount is not writable", func(t *testing.T) {
		// negative control: return true right after the stat in Writable — the 0555 directory reads as writable.
		if os.Geteuid() == 0 {
			t.Skip("root ignores the directory mode this test needs")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if Writable(dir) {
			t.Error("a 0555 directory is writable")
		}
	})
}

func TestDir(t *testing.T) {
	t.Run("PSTACK_LOKI_DIR by presence, then /etc/loki, then <data>/control/loki", func(t *testing.T) {
		// negative control: `if d := os.Getenv("PSTACK_LOKI_DIR"); d != ""` in Dir — the empty value falls through.
		t.Setenv("PSTACK_LOKI_DIR", "/srv/loki")
		if got := Dir("/data"); got != "/srv/loki" {
			t.Errorf("set: %q", got)
		}
		t.Setenv("PSTACK_LOKI_DIR", "")
		if got := Dir("/data"); got != "" {
			t.Errorf("empty: %q", got)
		}
		os.Unsetenv("PSTACK_LOKI_DIR")
		if st, err := os.Stat(lokiMount); err == nil && st.IsDir() {
			t.Skip("/etc/loki exists on this machine")
		}
		if got := Dir("/data"); got != filepath.Join("/data", "control", "loki") {
			t.Errorf("unset: %q", got)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/
```

Expected: a compile failure. The package has test files only so far:

```
# github.com/samishal1998/preview-stacks/packages/pstack/internal/loki [github.com/samishal1998/preview-stacks/packages/pstack/internal/loki.test]
internal/loki/loki_test.go:21:19: undefined: Settings
internal/loki/loki_test.go:22:7: undefined: Defaults
internal/loki/loki_test.go:23:14: undefined: Storage
```

- [ ] **Step 3: Implement `loki.go`**

Create `packages/pstack/internal/loki/loki.go`. Its header is the design record. T3 and T4 extend it, and T4 appends `Row`/`Read`/`Save`/`Finish`/`Revert`/`Merge`/`Changed` to this file.

```go
// Package loki is Loki's settings: what an operator may change, the ranges pstack holds them to, and
// the bytes they become in control/loki/config.yaml and control/loki/s3-credentials.
//
// ── PSTACK CLAMPS, BECAUSE LOKI DOES NOT ─────────────────────────────────────────────────────────
//
//	retentionDays             1–365     retention_period AND max_query_lookback, <d×24>h
//	chunks.idlePeriodMinutes  5–60      chunk_idle_period; never above the max age
//	chunks.maxAgeMinutes      30–180    max_chunk_age; query_ingesters_within = max age + 60m
//	chunks.targetSizeKiB      512–1536  chunk_target_size; the ceiling is slice 1's value
//	chunks.encoding           snappy gzip lz4 zstd
//
// Loki range-checks none of the chunk values but the encoding. A max age at or above
// query_ingesters_within hides unflushed chunks from queries, so the window is derived, not set. The
// size ceiling is slice 1's because memory grows with it and mem_limit/GOMEMLIMIT are not settings.
// The ranges live in ONE value (bounds): GET /api/logging serves it as `limits`, Validate enforces
// it, and the two cannot drift.
//
// ── RENDERING IS LITERAL ─────────────────────────────────────────────────────────────────────────
//
// Render starts from slice 1's embedded file and walks an ordered anchor list: strings.Contains, then
// strings.Replace(n=1) (invariant 18, rule 8). An anchor is the value half of a line, so trailing
// comments survive. All ten are checked on every render — on filesystem the three S3 anchors write
// themselves back — so a template edit that moves one fails the next save by name, not the first S3
// save months later. With Defaults() every replacement writes the text it found: the default bytes
// ARE slice 1's, and a slice-1 host reconciles to nothing.
//
// Nothing is quoted or escaped. Render runs Validate's value checks before it writes a byte, so every
// string it renders passed a charset rule and every number is an int, whoever the caller is.
//
// ── S3 IS A SECOND PERIOD; CREDENTIALS NEVER TOUCH config.yaml ───────────────────────────────────
//
// Filesystem → S3 appends a schema period from the cutover and never edits one: a period whose date
// has passed cannot change without making its data unreadable. The keys go in an AWS
// shared-credentials file the SDK re-reads on every restart (AWS_SHARED_CREDENTIALS_FILE, set by
// init). `docker restart` keeps a container's environment, so a key in the env would never rotate.
// config.yaml stays 0644 and credential-free. s3-credentials is 0600 and must be Loki's: root chowns
// it, Loki's own uid writes it as is, any other euid is refused (ErrNeedsRoot). A root-owned 0600
// file would verify, go ready and fail only at the first flush after the cutover.
//
// ── ERRORS ───────────────────────────────────────────────────────────────────────────────────────
//
// *Error is a refused value: 400 with its sentence. ErrOneWay, ErrFixed and ErrNeedsRoot conflict
// with the host's state: handlers map them to 409 with errors.Is (the inspect.ErrNoDocker shape), and
// one that escapes to fail() is a 500, never a mislabelled 400.
//
// Pure: it may import pstack, store, yamlx, omap/jsonx and stdlib, nothing that reaches a server or
// docker, so its tests need neither.
package loki

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Settings is what an operator saved. Field order is the JSON order (rule 1).
type Settings struct {
	RetentionDays int     `json:"retentionDays"`
	Chunks        Chunks  `json:"chunks"`
	Storage       Storage `json:"storage"`
}

// Chunks is the ingester's chunk shape.
type Chunks struct {
	IdlePeriodMinutes int    `json:"idlePeriodMinutes"`
	MaxAgeMinutes     int    `json:"maxAgeMinutes"`
	TargetSizeKiB     int    `json:"targetSizeKiB"`
	Encoding          string `json:"encoding"`
}

// Storage is filesystem, or filesystem then S3 from a cutover.
type Storage struct {
	Type string `json:"type"` // StorageFilesystem | StorageS3
	S3   *S3    `json:"s3"`   // null on filesystem — a pointer without omitempty (rule 2)
}

// S3 is an S3-compatible store. The secret is not here: it is its own column and has no read path.
type S3 struct {
	Endpoint    string `json:"endpoint"` // http(s)://host[:port]
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	Cutover     string `json:"cutover"` // YYYY-MM-DD UTC: the S3 period's `from`
}

// The storage types.
const (
	StorageFilesystem = "filesystem"
	StorageS3         = "s3"
)

// The files, in <LokiDir>. A write goes to <name>.next and is renamed over <name>.
const (
	ConfigFile      = "config.yaml"
	CredentialsFile = "s3-credentials"
	NextSuffix      = ".next"
)

// Defaults is slice 1's config: what an empty loki_config table means.
func Defaults() Settings {
	return Settings{
		RetentionDays: 7,
		Chunks:        Chunks{IdlePeriodMinutes: 30, MaxAgeMinutes: 120, TargetSizeKiB: 1536, Encoding: "snappy"},
		Storage:       Storage{Type: StorageFilesystem},
	}
}

// Range is an inclusive bound.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Limits is GET /api/logging's `limits`, so the UI never hard-codes a range or a date.
type Limits struct {
	RetentionDays     Range    `json:"retentionDays"`
	IdlePeriodMinutes Range    `json:"idlePeriodMinutes"`
	MaxAgeMinutes     Range    `json:"maxAgeMinutes"`
	TargetSizeKiB     Range    `json:"targetSizeKiB"`
	Encodings         []string `json:"encodings"`
	EarliestCutover   string   `json:"earliestCutover"`
}

// bounds is every range Validate enforces. Never handed out: LimitsAt copies it.
var bounds = Limits{
	RetentionDays:     Range{1, 365},
	IdlePeriodMinutes: Range{5, 60},
	MaxAgeMinutes:     Range{30, 180},
	TargetSizeKiB:     Range{512, 1536},
	Encodings:         []string{"snappy", "gzip", "lz4", "zstd"},
}

// LimitsAt is the limits at t. Encodings is a fresh non-nil slice (rule 3), so a caller cannot edit
// what Validate reads.
func LimitsAt(t time.Time, lead time.Duration) Limits {
	l := bounds
	l.Encodings = slices.Clone(bounds.Encodings)
	l.EarliestCutover = EarliestCutover(t, lead)
	return l
}

// EarliestCutover is the first UTC date whose 00:00 is strictly after t + 2×lead + 10m: the swap, a
// failed ready wait and a whole rollback all finish before the S3 period starts. Truncating to 24h
// lands on a UTC midnight at or before the window's end, so the next one is strictly after it.
func EarliestCutover(t time.Time, lead time.Duration) string {
	return t.Add(2*lead+10*time.Minute).UTC().Truncate(24*time.Hour).AddDate(0, 0, 1).Format(time.DateOnly)
}

// Lead is one restart plus a ready wait, with slack.
func Lead(readyTimeout time.Duration) time.Duration { return readyTimeout + 10*time.Minute }

// Error is a refused value. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is, or wraps, a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// The conflicts with the host's state. Not *Error: handlers answer 409 with errors.Is.
var (
	ErrOneWay    = errors.New("S3 is one-way on this host")
	ErrFixed     = errors.New("S3 storage is fixed once saved — only accessKeyId and secretAccessKey change")
	ErrNeedsRoot = errors.New("the S3 credentials file needs root")
)

// Seams: tests pin the clock and the process identity. Never t.Parallel in a test that sets one.
var (
	now     = time.Now
	chown   = os.Chown
	geteuid = os.Geteuid
)

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	regionRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// Validate is every rule a save must pass. addingS3 is true on the save that turns S3 on: only that
// save's cutover is held to EarliestCutover; a key rotation after the cutover is not.
func Validate(s Settings, secret string, addingS3 bool, lead time.Duration) error {
	if err := check(s); err != nil {
		return err
	}
	if s.Storage.Type != StorageS3 {
		return nil
	}
	if !iniValue(secret, 8) {
		return &Error{"secretAccessKey must be 8–256 printable ASCII characters, no space, # or ;"}
	}
	if earliest := EarliestCutover(now(), lead); addingS3 && s.Storage.S3.Cutover < earliest {
		return &Error{"cutover must be " + earliest + " or later"} // YYYY-MM-DD compares as a string
	}
	return nil
}

// check is Validate without the secret and the cutover floor: the values Render writes. Storage field
// names are the storage PUT body's, which is flat.
func check(s Settings) error {
	for _, f := range []struct {
		name string
		v    int
		r    Range
	}{
		{"retentionDays", s.RetentionDays, bounds.RetentionDays},
		{"chunks.idlePeriodMinutes", s.Chunks.IdlePeriodMinutes, bounds.IdlePeriodMinutes},
		{"chunks.maxAgeMinutes", s.Chunks.MaxAgeMinutes, bounds.MaxAgeMinutes},
		{"chunks.targetSizeKiB", s.Chunks.TargetSizeKiB, bounds.TargetSizeKiB},
	} {
		if f.v < f.r.Min || f.v > f.r.Max {
			return &Error{fmt.Sprintf("%s must be %d–%d", f.name, f.r.Min, f.r.Max)}
		}
	}
	if s.Chunks.IdlePeriodMinutes > s.Chunks.MaxAgeMinutes {
		return &Error{"chunks.idlePeriodMinutes must not exceed chunks.maxAgeMinutes"}
	}
	if !slices.Contains(bounds.Encodings, s.Chunks.Encoding) {
		return &Error{"chunks.encoding must be one of " + strings.Join(bounds.Encodings, ", ")}
	}
	switch s.Storage.Type {
	case StorageFilesystem:
		if s.Storage.S3 != nil {
			return &Error{"type filesystem takes no s3 fields"}
		}
		return nil
	case StorageS3:
		if s.Storage.S3 == nil {
			return &Error{"type s3 needs its s3 fields"}
		}
	default:
		return &Error{"type must be filesystem or s3"}
	}
	c := s.Storage.S3
	if _, _, ok := endpoint(c.Endpoint); !ok {
		return &Error{"endpoint must be http(s)://host[:port]"}
	}
	if !regionRe.MatchString(c.Region) {
		return &Error{"region must be 1–32 characters of a-z, 0-9 and -"}
	}
	if !bucketRe.MatchString(c.Bucket) {
		return &Error{"bucket must be 3–63 characters of a-z, 0-9, - and ., starting and ending with a letter or digit"}
	}
	// A dotted bucket in virtual-hosted form is a host name its TLS certificate does not cover.
	if strings.Contains(c.Bucket, ".") && !c.PathStyle {
		return &Error{"bucket may contain . only with pathStyle"}
	}
	if !iniValue(c.AccessKeyID, 1) {
		return &Error{"accessKeyId must be 1–256 printable ASCII characters, no space, # or ;"}
	}
	if _, err := time.Parse(time.DateOnly, c.Cutover); err != nil {
		return &Error{"cutover must be YYYY-MM-DD"}
	}
	return nil
}

// endpoint is the host[:port] Loki takes and whether the scheme is http. The host is DNS or IPv4
// characters only: it is written into YAML unquoted, where `[` opens a flow sequence and a trailing
// `:` a mapping, so a bracketed IPv6 literal is refused.
func endpoint(raw string) (host string, insecure, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		strings.ContainsAny(raw, "?#") || (u.Path != "" && u.Path != "/") ||
		!hostRe.MatchString(u.Hostname()) || strings.HasSuffix(u.Host, ":") {
		return "", false, false
	}
	return u.Host, u.Scheme == "http", true
}

// iniValue is the credentials file's value syntax: min–256 bytes of printable ASCII without space,
// # or ; (INI comment starts). The charset is ASCII, so bytes and characters are the same count.
func iniValue(v string, min int) bool {
	if len(v) < min || len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x21 || c > 0x7e || c == '#' || c == ';' {
			return false
		}
	}
	return true
}

// Credentials is s3-credentials. Both values passed iniValue, so nothing needs escaping.
func Credentials(keyID, secret string) string {
	return "[default]\naws_access_key_id = " + keyID + "\naws_secret_access_key = " + secret + "\n"
}

// CredentialsOwner is nil when this process can give Loki (lokiUID) a 0600 file it can read: as root,
// by chowning, or as Loki's own uid.
func CredentialsOwner(lokiUID int) error {
	if e := geteuid(); e == 0 || e == lokiUID {
		return nil
	}
	return ErrNeedsRoot
}

// WriteFile writes body at exactly mode. A 0600 file is refused unless CredentialsOwner passes, and as
// root it is chowned to lokiUID.
//
// Created at mode, not 0o666-then-chmod: the secret is never world-readable, even for an instant. A
// leftover (a .next from a killed apply, or a symlink) is removed first: a truncating write keeps an
// old file's mode and follows a link. The Chmod still runs, because umask may strip bits a 0644
// config.yaml needs for Loki's uid to read it.
func WriteFile(path, body string, mode os.FileMode, lokiUID int) error {
	if mode == 0o600 {
		if err := CredentialsOwner(lokiUID); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if mode == 0o600 && geteuid() == 0 && lokiUID != 0 {
		return chown(path, lokiUID, lokiUID)
	}
	return nil
}

// Writable is true when dir holds config.yaml and this process may write beside it. A mount can be
// present but `:ro`, which only a write shows (routing.RoutingStore.Writable, copied: loki does not
// import routing).
func Writable(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, ConfigFile)); err != nil || !st.Mode().IsRegular() {
		return false
	}
	probe := filepath.Join(dir, fmt.Sprintf(".pstack-write-probe-%d", os.Getpid()))
	if err := os.WriteFile(probe, nil, 0o666); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// lokiMount is where the pstack container sees control/loki: the same path as Loki's own mount, so a
// file name means the same thing in both containers.
const lokiMount = "/etc/loki"

// Dir is control/loki for THIS process (routing.DynamicDir's resolution): PSTACK_LOKI_DIR by presence
// (rule 11), then the in-container mount, then the host-side path under the data dir.
func Dir(dataDir string) string {
	if d, ok := os.LookupEnv("PSTACK_LOKI_DIR"); ok {
		return d
	}
	if st, err := os.Stat(lokiMount); err == nil && st.IsDir() {
		return lokiMount
	}
	return filepath.Join(dataDir, "control", "loki")
}
```

Sources: `Error`/`IsError` follow `routing.Error` (`internal/routing/routing.go:56-64`, errors.As). The sentinels follow `inspect.ErrNoDocker` (`internal/inspect/control.go:87-91`). `Writable` copies `routing.RoutingStore.Writable` (`internal/routing/routing.go:159-172`), and `Dir` copies `routing.DynamicDir` (`internal/routing/wildcard.go:254-262`). The secret's 8-byte floor matches where `redact.RedactText` starts scrubbing extras (`internal/redact/redact.go:158`).

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/loki	1.0s`.

- [ ] **Step 5: Write the failing render test**

Create `packages/pstack/internal/loki/render_test.go`. It uses `s3Settings` from `loki_test.go`. The ten missing-anchor subtests carry their own copy of the anchor bytes, and they run on `Defaults()`, which proves filesystem renders check the S3 anchors too.

```go
package loki

import (
	"strings"
	"testing"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

func TestRender(t *testing.T) {
	t.Run("the defaults render slice 1's file byte for byte", func(t *testing.T) {
		// negative control: in dur, return strconv.Itoa(m)+"m" for every m — `max_chunk_age: 120m` differs.
		got, err := Render(Defaults())
		if err != nil {
			t.Fatal(err)
		}
		if got != pstack.LokiConfig {
			t.Errorf("Render(Defaults()) is not pstack.LokiConfig:\n%s", got)
		}
	})

	t.Run("each field lands exactly once, comments kept", func(t *testing.T) {
		// negative control: drop the max_query_lookback entry from render's list — `max_query_lookback: 336h` appears 0 times.
		s := Defaults()
		s.RetentionDays = 14
		s.Chunks = Chunks{IdlePeriodMinutes: 45, MaxAgeMinutes: 180, TargetSizeKiB: 512, Encoding: "zstd"}
		got, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{
			"  chunk_idle_period: 45m\n",
			"  max_chunk_age: 3h                 # must stay <= querier.query_ingesters_within\n",
			"  chunk_target_size: 524288\n",
			"  chunk_encoding: zstd\n",
			"  query_ingesters_within: 4h\n",
			"  retention_period: 336h            # 0s would keep forever\n",
			"  max_query_lookback: 336h\n",
			"  delete_request_store: filesystem  # required once retention is on\n",
		} {
			if n := strings.Count(got, line); n != 1 {
				t.Errorf("%q appears %d times", line, n)
			}
		}
		if _, err := yamlx.ParseString(got); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("minutes are hours only when whole; the query window is max age + 60m", func(t *testing.T) {
		// negative control: render query_ingesters_within from dur(MaxAgeMinutes) — `4h` becomes `3h`.
		for _, c := range []struct {
			maxAge      int
			age, window string
		}{{30, "30m", "90m"}, {90, "90m", "150m"}, {120, "2h", "3h"}, {180, "3h", "4h"}} {
			s := Defaults()
			s.Chunks.MaxAgeMinutes = c.maxAge
			got, err := Render(s)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "  max_chunk_age: "+c.age+" ") || !strings.Contains(got, "  query_ingesters_within: "+c.window+"\n") {
				t.Errorf("maxAgeMinutes %d: want max_chunk_age %s, query_ingesters_within %s", c.maxAge, c.age, c.window)
			}
		}
	})

	t.Run("S3 adds the aws block, a second period and the s3 delete store", func(t *testing.T) {
		// negative control: leave deleteStore as deleteStoreKey on S3 — `delete_request_store` reads filesystem.
		got, err := Render(s3Settings())
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range []string{
			"    directory: /loki/chunks\n" +
				"  aws:\n" +
				"    endpoint: s3.eu-central-1.amazonaws.com\n" +
				"    region: eu-central-1\n" +
				"    bucketnames: pstack-logs\n" +
				"    s3forcepathstyle: false         # exact spelling: the field has no yaml tag\n" +
				"    insecure: false\n" +
				"    # No keys here: the SDK reads AWS_SHARED_CREDENTIALS_FILE, again on every restart.\n" +
				"\nschema_config:\n",
			"        period: 24h                 # tsdb requires 24h\n" +
				"    - from: \"2026-09-16\"            # S3 from here on; once this date passes it cannot be removed\n" +
				"      store: tsdb\n" +
				"      object_store: s3\n" +
				"      schema: v13\n" +
				"      index:\n" +
				"        prefix: index_\n" +
				"        period: 24h\n" +
				"\ningester:\n",
			"  delete_request_store: s3  # required once retention is on\n",
		} {
			if n := strings.Count(got, block); n != 1 {
				t.Errorf("%q appears %d times", block, n)
			}
		}
		v, err := yamlx.ParseString(got)
		if err != nil {
			t.Fatal(err)
		}
		cfg := v.(*omap.Map)
		configs := cfg.GetMap("schema_config").GetSlice("configs")
		if len(configs) != 2 {
			t.Fatalf("%d schema configs, want 2", len(configs))
		}
		first, _ := configs[0].(*omap.Map)
		second, _ := configs[1].(*omap.Map)
		if first.GetString("from") != "2024-04-01" || first.GetString("object_store") != "filesystem" ||
			second.GetString("from") != "2026-09-16" || second.GetString("object_store") != "s3" {
			t.Errorf("periods %v, %v", first, second)
		}
		if cfg.GetMap("compactor").GetString("delete_request_store") != "s3" {
			t.Error("delete_request_store is not s3")
		}
	})

	t.Run("path-style over http: the host, s3forcepathstyle true, insecure true", func(t *testing.T) {
		// negative control: render insecure as strconv.FormatBool(!insecure) — `insecure` reads false.
		s := s3Settings()
		s.Storage.S3.Endpoint, s.Storage.S3.PathStyle, s.Storage.S3.Bucket = "http://minio.internal:9000/", true, "logs.v1"
		got, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		v, err := yamlx.ParseString(got)
		if err != nil {
			t.Fatal(err)
		}
		aws := v.(*omap.Map).GetMap("storage_config").GetMap("aws")
		style, _ := aws.Get("s3forcepathstyle")
		insecure, _ := aws.Get("insecure")
		if aws.GetString("endpoint") != "minio.internal:9000" || aws.GetString("bucketnames") != "logs.v1" || style != true || insecure != true {
			t.Errorf("aws %v", aws)
		}
		if !strings.Contains(got, "    s3forcepathstyle: true          # exact spelling") {
			t.Error("the s3forcepathstyle comment is not at column 36")
		}
	})

	t.Run("config.yaml never holds a credential", func(t *testing.T) {
		// negative control: add `"    access_key_id: " + c.AccessKeyID + "\n"` to the aws block — the key id appears.
		got, err := Render(s3Settings())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(got, "access_key") || strings.Contains(got, "secret") {
			t.Errorf("config.yaml carries a credential:\n%s", got)
		}
	})

	t.Run("a value Validate refuses is never rendered", func(t *testing.T) {
		// negative control: drop the check call at the top of render — retentionDays 0 renders `retention_period: 0h`.
		zero := Defaults()
		zero.RetentionDays = 0
		injected := s3Settings()
		injected.Storage.S3.Bucket = "pstack-logs\n    access_key_id: x"
		for _, s := range []Settings{zero, injected} {
			if got, err := Render(s); !IsError(err) || got != "" {
				t.Errorf("got %q, %v", got, err)
			}
		}
	})

	for _, a := range []struct{ what, anchor string }{
		{"chunk_idle_period", "  chunk_idle_period: 30m"},
		{"max_chunk_age", "  max_chunk_age: 2h"},
		{"chunk_target_size", "  chunk_target_size: 1572864"},
		{"chunk_encoding", "  chunk_encoding: snappy"},
		{"query_ingesters_within", "  query_ingesters_within: 3h"},
		{"retention_period", "  retention_period: 168h"},
		{"max_query_lookback", "  max_query_lookback: 168h"},
		{"chunks directory", "    directory: /loki/chunks\n"},
		{"schema period", "        period: 24h                 # tsdb requires 24h\n"},
		{"delete_request_store", "  delete_request_store: filesystem"},
	} {
		t.Run("a template without "+a.what+" fails by name, on filesystem too", func(t *testing.T) {
			// negative control: skip the strings.Contains check in render — no error.
			_, err := render(strings.Replace(pstack.LokiConfig, a.anchor, "", 1), Defaults())
			if !IsError(err) || !strings.Contains(err.Error(), "has no "+a.what+" (") {
				t.Fatalf("got %v", err)
			}
		})
	}
}
```

- [ ] **Step 6: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/
```

Expected:

```
# github.com/samishal1998/preview-stacks/packages/pstack/internal/loki [github.com/samishal1998/preview-stacks/packages/pstack/internal/loki.test]
internal/loki/render_test.go:15:15: undefined: Render
internal/loki/render_test.go:29:15: undefined: Render
internal/loki/render_test.go:60:16: undefined: Render
```

- [ ] **Step 7: Implement `render.go`**

Create `packages/pstack/internal/loki/render.go`. The `%-36s` puts each inserted comment at column 36, the column the template uses (`  http_listen_port: 3100            #`). The S3 blocks are spec:182-200 byte for byte.

```go
package loki

import (
	"fmt"
	"strconv"
	"strings"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
)

// Render is config.yaml for s.
func Render(s Settings) (string, error) { return render(pstack.LokiConfig, s) }

// The three S3 anchors: whole lines, because S3 inserts after them.
const (
	chunksDirLine  = "    directory: /loki/chunks\n"
	periodLine     = "        period: 24h                 # tsdb requires 24h\n"
	deleteStoreKey = "  delete_request_store: filesystem"
)

// render walks the anchors in order over template. Ordered, never a map (rule 5): the first missing
// anchor is the one named. See the package header.
func render(template string, s Settings) (string, error) {
	if err := check(s); err != nil {
		return "", err
	}
	days := strconv.Itoa(s.RetentionDays*24) + "h"
	aws, period, deleteStore := chunksDirLine, periodLine, deleteStoreKey
	if c := s.Storage.S3; c != nil { // check passed: type is s3
		host, insecure, _ := endpoint(c.Endpoint)
		aws = chunksDirLine +
			"  aws:\n" +
			"    endpoint: " + host + "\n" +
			"    region: " + c.Region + "\n" +
			"    bucketnames: " + c.Bucket + "\n" +
			fmt.Sprintf("%-36s# exact spelling: the field has no yaml tag\n", "    s3forcepathstyle: "+strconv.FormatBool(c.PathStyle)) +
			"    insecure: " + strconv.FormatBool(insecure) + "\n" +
			"    # No keys here: the SDK reads AWS_SHARED_CREDENTIALS_FILE, again on every restart.\n"
		period = periodLine +
			fmt.Sprintf("%-36s# S3 from here on; once this date passes it cannot be removed\n", `    - from: "`+c.Cutover+`"`) +
			"      store: tsdb\n" +
			"      object_store: s3\n" +
			"      schema: v13\n" +
			"      index:\n" +
			"        prefix: index_\n" +
			"        period: 24h\n"
		deleteStore = "  delete_request_store: s3"
	}
	for _, e := range []struct{ what, anchor, with string }{
		{"chunk_idle_period", "  chunk_idle_period: 30m", "  chunk_idle_period: " + dur(s.Chunks.IdlePeriodMinutes)},
		{"max_chunk_age", "  max_chunk_age: 2h", "  max_chunk_age: " + dur(s.Chunks.MaxAgeMinutes)},
		{"chunk_target_size", "  chunk_target_size: 1572864", "  chunk_target_size: " + strconv.Itoa(s.Chunks.TargetSizeKiB*1024)},
		{"chunk_encoding", "  chunk_encoding: snappy", "  chunk_encoding: " + s.Chunks.Encoding},
		{"query_ingesters_within", "  query_ingesters_within: 3h", "  query_ingesters_within: " + dur(s.Chunks.MaxAgeMinutes+60)},
		{"retention_period", "  retention_period: 168h", "  retention_period: " + days},
		{"max_query_lookback", "  max_query_lookback: 168h", "  max_query_lookback: " + days},
		{"chunks directory", chunksDirLine, aws},
		{"schema period", periodLine, period},
		{"delete_request_store", deleteStoreKey, deleteStore},
	} {
		if !strings.Contains(template, e.anchor) {
			return "", &Error{fmt.Sprintf("the loki config template has no %s (%q)", e.what, e.anchor)}
		}
		template = strings.Replace(template, e.anchor, e.with, 1)
	}
	return template, nil
}

// dur is m minutes in the template's spelling: whole hours as `2h`, anything else as `45m`.
func dur(m int) string {
	if m%60 == 0 {
		return strconv.Itoa(m/60) + "h"
	}
	return strconv.Itoa(m) + "m"
}
```

- [ ] **Step 8: Run it and watch it pass; vet and gofmt**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -v 2>&1 | grep -c -- '--- PASS' && go test -race -timeout 120s ./internal/loki/ && go vet ./internal/loki/ && gofmt -l internal/loki
```

Expected:
- `55`: 8 tests and 47 subtests.
- `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/loki`
- No output from `go vet` or `gofmt -l`.

Two subtests skip in some environments. `TestWritable/a_read-only_mount_is_not_writable` skips as root. `TestDir`'s last assertion skips on a machine that has a `/etc/loki` directory.

- [ ] **Step 9: Run every negative control**

Each `t.Run` names its mutation on its `// negative control:` line. That makes 37 distinct mutations: the ten missing-anchor subtests share one, and the `TestEarliestCutover` rows share one comment that names four per-row mutations. The 23:40 row fails under the AddDate mutation.

For each mutation:
1. Apply it to `loki.go` or `render.go`.
2. Run only its subtest, for example `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/loki/ -run '^TestValidate$/^every_range'`.
3. Confirm `--- FAIL` for that subtest.
4. Revert the mutation.

`-run` patterns (subtest-name prefixes):
- TestRender: `^the_defaults`, `^each_field`, `^minutes`, `^S3_adds`, `^path-style`, `^config.yaml_never`, `^a_value_Validate`, `^a_template_without`
- TestJSON: `^settings`, `^limits`, `^a_caller`
- TestEarliestCutover: `^Lead`, `^noon`, `^23:30`, `^23:20`, `^a_-05:00`
- TestErrors: `^TestErrors$`
- TestValidate: `^every_range`, `^idle_above`, `^encodings`, `^storage_type`, `^endpoint`, `^region`, `^bucket`, `^accessKeyId`, `^cutover_is`, `^adding_S3`, `^an_S3_host`
- TestCredentials: `^exact`, `^CredentialsOwner`, `^root_writes`, `^Loki's_own`, `^any_other`, `^a_leftover`
- TestWritable: `^config.yaml_present`, `^a_read-only`
- TestDir: `^TestDir$`

All 37 were run against this exact code while the plan was drafted, and each failed its subtest. Finish by confirming `git diff --stat -- packages/pstack/internal/loki` shows nothing beyond the four new files, i.e. no mutation is left behind.

- [ ] **Step 10: CHANGELOG**

Re-read `packages/pstack/CHANGELOG.md` first; slice-1 T14 owns the `## Unreleased` heading. Add one feature-level bullet. Later slice-2 tasks add their own bullets, and T16 merges them.

The bullet:

```markdown
- **Loki settings.** Retention, chunks and storage (filesystem, or S3 from a cutover date) are held
  to pstack's ranges and rendered into `control/loki/config.yaml`. S3 keys go in a 0600 credentials
  file owned by Loki's uid, never in the config.
```

- If `## Unreleased` has an `### Added` list (the slice-1 T14 layout, `docs/loki-logging-slice-1-plan.md:7240-7274` at plan time), insert the bullet as the last item of that list: after its last bullet, with one blank line before the `### Changed` heading that follows it.
- If `## Unreleased` does not exist (the case in the tree today: the file opens `# Changelog` then `## 0.39.1 — 2026-09-14`), replace:

  ```markdown
  # Changelog

  ## 0.39.1 — 2026-09-14
  ```

  with:

  ```markdown
  # Changelog

  ## Unreleased

  ### Added

  - **Loki settings.** Retention, chunks and storage (filesystem, or S3 from a cutover date) are held
    to pstack's ranges and rendered into `control/loki/config.yaml`. S3 keys go in a 0600 credentials
    file owned by Loki's uid, never in the config.

  ## 0.39.1 — 2026-09-14
  ```

No goldens change in this task: nothing calls `loki.Render` yet, and `pstack.LokiConfig` is untouched.

- [ ] **Step 11: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

1. Confirm that `claude/loki-logging-settings` is stacked on `claude/loki-logging`. T1 created it; if the order is wrong, restack with `but move` and never recreate the branch.
2. Take the IDs of exactly these five files: `packages/pstack/internal/loki/loki.go`, `packages/pstack/internal/loki/render.go`, `packages/pstack/internal/loki/loki_test.go`, `packages/pstack/internal/loki/render_test.go` and `packages/pstack/CHANGELOG.md`.
3. Never include `packages/conformance/golden/host/db/pstack.db-shm`, `pstack.db-wal` or anything under `.superpowers/`.

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-settings -m "feat(loki): settings, clamps, render and the credentials file" <loki.go id> <render.go id> <loki_test.go id> <render_test.go id> <CHANGELOG.md id>
```

No attribution footer.

---

### Task 3: loki: schema periods and the period guard

**Files:**
- Create: `packages/pstack/internal/loki/periods.go`
- Modify: `docs/control-plane.md` (a new `### The period guard` subsection at the end of §5g, inserted before the quoted `## 6. Submitting a deployment` heading. Line numbers are advisory because slice 1 is moving them.)
- Test: `packages/pstack/internal/loki/periods_test.go`

**Interfaces:**
- Consumes (from T2, `packages/pstack/internal/loki/loki.go`): `type Error struct{ Msg string }`, `func (e *Error) Error() string`, `func IsError(err error) bool`. Also `pstack.LokiConfig` (`packages/pstack/assets.go`, slice-1 T2, built), `yamlx.ParseString` (`internal/yamlx/yamlx.go:85`), and `omap.(*Map).GetMap/GetString/GetSlice` (`internal/omap/omap.go:151-170`, all nil-safe through `Get` at :39-42).
- Produces:
  - `type Period struct { From, Store, ObjectStore, Schema, IndexPrefix, IndexPeriod string }`: compared with `==`.
  - `func Periods(configYAML string) ([]Period, error)`: `schema_config.configs[*]` in file order, as a non-nil slice. When the text doesn't parse, isn't a mapping, or has no configs, an empty list or a non-mapping entry, it returns `nil, &Error{"config.yaml has no schema periods"}`.
  - `func CheckPeriods(loaded, next []Period, now time.Time, lead time.Duration, adding bool) error`:
    - Rule 1: every `loaded[i]` whose `from` 00:00 UTC is at or before `now+lead` must equal `next[i]`. Otherwise it returns `*Error` `config.yaml has a live schema period from <from> that this change would drop — not written`.
    - Rule 2, only when `adding`: every `next` period not in `loaded` must start after `now+2×lead`. Otherwise it returns `*Error` `cutover <from> is too close — save again`.
    - A `from` that is not `YYYY-MM-DD` counts as started.
  - Callers: T9 step 2 passes `adding=true`, T10's rollback `adding=false`, and T11's resume `adding=true` with `loaded` taken from `Render(previous)` when Loki never loaded the files.

- [ ] **Step 0: Preconditions.** T3 builds on T2 and on slice-1 T8's §5g:

```bash
cd /Volumes/S1/code/preview-stacks
grep -n "^type Error struct\|^func IsError" packages/pstack/internal/loki/loki.go
grep -n "^## 5g\. Loki logging\|^## 6\. Submitting a deployment" docs/control-plane.md
```

Expected: both loki.go lines, and both headings, with `## 5g.` before `## 6.`. If `## 5g.` is missing, slice 1 has not landed. Stop, because slice 2 starts after slice 1.

- [ ] **Step 1: Write the failing test.** Create `packages/pstack/internal/loki/periods_test.go`. Helper names carry a `period` prefix so they cannot collide with T2/T4's `loki_test.go`.

```go
package loki

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
)

// periodBase is the one period in pstack.LokiConfig; periodS3 is the one an S3 save appends.
var periodBase = Period{From: "2024-04-01", Store: "tsdb", ObjectStore: "filesystem", Schema: "v13", IndexPrefix: "index_", IndexPeriod: "24h"}

func periodS3(from string) Period {
	return Period{From: from, Store: "tsdb", ObjectStore: "s3", Schema: "v13", IndexPrefix: "index_", IndexPeriod: "24h"}
}

func periodClock(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func wantPeriodError(t *testing.T, err error, msg string) {
	t.Helper()
	if !IsError(err) || err.Error() != msg {
		t.Fatalf("err = %v, want *Error %q", err, msg)
	}
}

// negative control: see the subtests.
func TestPeriods(t *testing.T) {
	t.Run("the template has one period", func(t *testing.T) {
		// negative control: read IndexPrefix with m.GetString("prefix") instead of from the index map.
		got, err := Periods(pstack.LokiConfig)
		if err != nil {
			t.Fatal(err)
		}
		if want := []Period{periodBase}; !slices.Equal(got, want) {
			t.Fatalf("Periods(LokiConfig) = %+v, want %+v", got, want)
		}
	})

	t.Run("an S3 config has both periods, in file order", func(t *testing.T) {
		// negative control: range over configs[:1] instead of configs.
		const anchor = "        period: 24h                 # tsdb requires 24h\n"
		if !strings.Contains(pstack.LokiConfig, anchor) {
			t.Fatalf("the template has no %q", anchor)
		}
		s3 := strings.Replace(pstack.LokiConfig, anchor, anchor+
			"    - from: \"2026-09-16\"            # S3 from here on; once this date passes it cannot be removed\n"+
			"      store: tsdb\n"+
			"      object_store: s3\n"+
			"      schema: v13\n"+
			"      index:\n"+
			"        prefix: index_\n"+
			"        period: 24h\n", 1)
		got, err := Periods(s3)
		if err != nil {
			t.Fatal(err)
		}
		if want := []Period{periodBase, periodS3("2026-09-16")}; !slices.Equal(got, want) {
			t.Fatalf("Periods = %+v, want %+v", got, want)
		}
	})

	t.Run("no periods is an *Error", func(t *testing.T) {
		// negative control: drop the len(configs) == 0 check — "" and a config without schema_config read as no periods, no error.
		for _, text := range []string{
			"",
			"not: [yaml",
			"- a list\n",
			"auth_enabled: false\n",
			"schema_config:\n  configs: []\n",
			"schema_config:\n  configs:\n    - 2024\n",
		} {
			got, err := Periods(text)
			if got != nil {
				t.Errorf("Periods(%q) = %+v, want nil", text, got)
			}
			wantPeriodError(t, err, "config.yaml has no schema periods")
		}
	})
}

// negative control: see the subtests.
func TestCheckPeriods(t *testing.T) {
	const lead = 15 * time.Minute
	noon := periodClock(t, "2026-09-15T12:00:00Z")

	t.Run("dropping a started period is refused", func(t *testing.T) {
		// negative control: return nil before rule 1's loop.
		err := CheckPeriods([]Period{periodBase, periodS3("2026-09-15")}, []Period{periodBase}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2026-09-15 that this change would drop — not written")
	})

	t.Run("dropping a period that starts within lead is refused", func(t *testing.T) {
		// negative control: compare against now instead of now.Add(lead) — 2026-09-16 00:00 is 10m away, not yet started.
		late := periodClock(t, "2026-09-15T23:50:00Z")
		err := CheckPeriods([]Period{periodBase, periodS3("2026-09-16")}, []Period{periodBase}, late, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2026-09-16 that this change would drop — not written")
	})

	t.Run("a rollback may drop a period that starts after now+lead", func(t *testing.T) {
		// negative control: drop the start comparison, so every loaded period is live.
		if err := CheckPeriods([]Period{periodBase, periodS3("2026-09-16")}, []Period{periodBase}, noon, lead, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a changed live period is refused", func(t *testing.T) {
		// negative control: compare next[i].From != p.From instead of the whole Period.
		changed := periodBase
		changed.IndexPeriod = "168h"
		err := CheckPeriods([]Period{periodBase}, []Period{changed}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2024-04-01 that this change would drop — not written")
	})

	t.Run("a reordered live period is refused", func(t *testing.T) {
		// negative control: accept a live period anywhere in next (slices.Contains(next, p)) instead of at next[i].
		loaded := []Period{periodBase, periodS3("2026-09-01")}
		err := CheckPeriods(loaded, []Period{loaded[1], loaded[0]}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2024-04-01 that this change would drop — not written")
	})

	t.Run("adding a period that starts within 2×lead is refused", func(t *testing.T) {
		// negative control: compare against now.Add(lead) instead of now.Add(2*lead) — 00:00 is 20m away, past lead but within 2×lead.
		late := periodClock(t, "2026-09-15T23:40:00Z")
		err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, late, lead, true)
		wantPeriodError(t, err, "cutover 2026-09-16 is too close — save again")
	})

	t.Run("rule 2 only runs when adding", func(t *testing.T) {
		// negative control: run rule 2 whatever adding says.
		late := periodClock(t, "2026-09-15T23:40:00Z")
		if err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, late, lead, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("adding a period that starts after 2×lead is allowed", func(t *testing.T) {
		// negative control: refuse every period not in loaded, whatever its start.
		if err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, noon, lead, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a period already loaded is not re-checked by rule 2", func(t *testing.T) {
		// negative control: check every next period against 2×lead, not only the ones missing from loaded.
		late := periodClock(t, "2026-09-15T23:50:00Z")
		both := []Period{periodBase, periodS3("2026-09-16")}
		if err := CheckPeriods(both, both, late, lead, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a from that is not a date counts as started", func(t *testing.T) {
		// negative control: skip a period whose from does not parse (if err != nil { continue }) in both loops.
		bad := periodBase
		bad.From = "soon"
		wantPeriodError(t, CheckPeriods([]Period{bad}, []Period{}, noon, lead, false),
			"config.yaml has a live schema period from soon that this change would drop — not written")
		wantPeriodError(t, CheckPeriods([]Period{periodBase}, []Period{periodBase, bad}, noon, lead, true),
			"cutover soon is too close — save again")
	})
}
```

Why the clocks: a `from` of *today* is started under both `now` and `now+lead`, so it cannot catch the "compare against `now`" mutation. Only a `from` inside `(now, now+lead]` can (23:50Z, from tomorrow). Rule 2 works the same way: only a `from` inside `(now+lead, now+2×lead]` (23:40Z, from tomorrow) catches "compare against `now+lead`". The "already loaded" case is the credentials rotation on an S3 host: T9's step 2 always passes `adding=true`, and a stored S3 period must not trip rule 2 once its cutover is near.

- [ ] **Step 2: Run it and watch it fail.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -run 'TestPeriods|TestCheckPeriods'
```

Expected: a build failure, not a test failure:

```
./periods_test.go:13:18: undefined: Period
./periods_test.go:39:15: undefined: Periods
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/loki [build failed]
```

- [ ] **Step 3: Implement.** Create `packages/pstack/internal/loki/periods.go`. There is no `// Package` line, because the package doc is `loki.go`'s header (T2).

```go
package loki

import (
	"slices"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

/*
 * The period guard. A schema period whose `from` has passed can never be removed or changed: data
 * written under it becomes unreadable. The row can lag or lead the files (a queued save, a stop
 * mid-apply, hand SQL), so every write of config.yaml — apply, rollback, resume — checks periods
 * read from FILES, never from the row. The caller picks `loaded` (config.yaml's, or
 * Render(previous)'s when a resumed apply's files were never loaded) and `adding` (true for an
 * apply and a resume, false for a rollback).
 */

// Period is one schema_config.configs entry. Compared with ==: any changed field is a changed period.
type Period struct {
	From, Store, ObjectStore, Schema, IndexPrefix, IndexPeriod string
}

// Periods reads schema_config.configs from a config.yaml text, in file order. `from` is quoted in
// every file pstack writes (templates/control/loki/config.yaml:22); a `from` that is not a string
// reads as "", which CheckPeriods treats as started.
func Periods(configYAML string) ([]Period, error) {
	none := &Error{Msg: "config.yaml has no schema periods"}
	v, err := yamlx.ParseString(configYAML)
	if err != nil {
		return nil, none
	}
	doc, _ := v.(*omap.Map) // the omap getters are nil-safe
	configs := doc.GetMap("schema_config").GetSlice("configs")
	if len(configs) == 0 {
		return nil, none
	}
	out := make([]Period, 0, len(configs))
	for _, c := range configs {
		m, ok := c.(*omap.Map)
		if !ok {
			return nil, none
		}
		out = append(out, Period{
			From:        m.GetString("from"),
			Store:       m.GetString("store"),
			ObjectStore: m.GetString("object_store"),
			Schema:      m.GetString("schema"),
			IndexPrefix: m.GetMap("index").GetString("prefix"),
			IndexPeriod: m.GetMap("index").GetString("period"),
		})
	}
	return out, nil
}

// CheckPeriods refuses writing `next` over `loaded`.
//
// Rule 1: every loaded period starting at or before now+lead stays in next, unchanged, at its index.
// Rule 2 (adding): every next period not in loaded starts after now+2×lead, so the swap, a failed
// ready wait and a whole rollback all finish before it. A rollback passes adding=false: a period
// that has not started stores nothing, so undoing a failed apply may drop it.
//
// A `from` that is not YYYY-MM-DD parses to the zero time, so it counts as started: live for
// rule 1, too close for rule 2.
func CheckPeriods(loaded, next []Period, now time.Time, lead time.Duration, adding bool) error {
	for i, p := range loaded {
		start, _ := time.Parse(time.DateOnly, p.From) // 00:00 UTC
		if !start.After(now.Add(lead)) && (i >= len(next) || next[i] != p) {
			return &Error{Msg: "config.yaml has a live schema period from " + p.From + " that this change would drop — not written"}
		}
	}
	if !adding {
		return nil
	}
	for _, p := range next {
		start, _ := time.Parse(time.DateOnly, p.From)
		if !slices.Contains(loaded, p) && !start.After(now.Add(2*lead)) {
			return &Error{Msg: "cutover " + p.From + " is too close — save again"}
		}
	}
	return nil
}
```

`time.DateOnly` and `slices` need Go 1.21, and `packages/pstack/go.mod:3` is `go 1.23.5`.

- [ ] **Step 4: Run it and watch it pass.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -run 'TestPeriods|TestCheckPeriods' -v 2>&1 | grep -E '^\s*--- |^ok|^FAIL'
go vet ./internal/loki/ && gofmt -l internal/loki/
```

Expected: 13 subtest `--- PASS` lines under `--- PASS: TestPeriods` and `--- PASS: TestCheckPeriods`, then `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/loki`. `go vet` and `gofmt -l` print nothing. Then run the whole package, including T2's tests: `go test -race -timeout 120s ./internal/loki/` → `ok`.

- [ ] **Step 5: Run every negative control.** Apply each mutation to `periods.go`, run the Step 4 command, confirm the named subtest shows `--- FAIL`, then revert (`but diff` must show the original file again). All 13 were run against this exact code, and each fails the subtest listed:

| Mutation in `periods.go` | Fails |
|---|---|
| `IndexPrefix: m.GetString("prefix"),` | `the template has one period` |
| `for _, c := range configs[:1] {` | `an S3 config has both periods, in file order` |
| delete `if len(configs) == 0 { return nil, none }` | `no periods is an *Error` |
| `for i, p := range loaded[:0] {` | `dropping a started period is refused` |
| `!start.After(now)` in rule 1 | `dropping a period that starts within lead is refused` |
| `(true \|\| !start.After(now.Add(lead)))` in rule 1 | `a rollback may drop a period that starts after now+lead` |
| `next[i].From != p.From)` | `a changed live period is refused` |
| `(i < 0 \|\| !slices.Contains(next, p))` | `a reordered live period is refused` |
| `now.Add(lead)` in place of `now.Add(2*lead)` | `adding a period that starts within 2×lead is refused` |
| `if false {` in place of `if !adding {` | `rule 2 only runs when adding` |
| `(true \|\| !start.After(now.Add(2*lead)))` | `adding a period that starts after 2×lead is allowed` |
| `(true \|\| !slices.Contains(loaded, p)) && !start…` | `a period already loaded is not re-checked by rule 2` |
| `start, err := time.Parse(…); if err != nil { continue }` in both loops | `a from that is not a date counts as started` |

- [ ] **Step 6: Document it.** In `docs/control-plane.md`, replace the heading line that ends §5g:

```markdown

## 6. Submitting a deployment
```

with:

```markdown

### The period guard

A schema period whose `from` has passed cannot be removed or changed: its data becomes unreadable.
The row can lag or lead the files, so every write of `config.yaml` (apply, rollback, resume) reads
the periods from the files, never from the row, and runs `loki.CheckPeriods`. `lead` is the ready
timeout + 10m.

- **Rule 1:** a period starting at or before now + `lead` stays, unchanged, at its position.
- **Rule 2** (apply, resume): a new period starts after now + 2×`lead`.

A rollback checks rule 1 only: a period that has not started stores nothing. A refusal names the
period; nothing is written.

## 6. Submitting a deployment
```

Seam: T5 (`The probe`) and T9-T11 (`The apply`, rollback, `Who owns config.yaml`, `Resume`, `Boot`) insert at the same `## 6. Submitting a deployment` anchor, so §5g's subsections end up in task order.

- [ ] **Step 7: Commit.**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Confirm that `claude/loki-logging-settings` sits on `claude/loki-logging`, then take the file IDs of `packages/pstack/internal/loki/periods.go`, `packages/pstack/internal/loki/periods_test.go` and `docs/control-plane.md`. Leave out `packages/conformance/golden/host/db/*` and `.superpowers/`.

```bash
but commit -b claude/loki-logging-settings -m "feat(loki): schema periods and the period guard" <periods.go id> <periods_test.go id> <control-plane.md id>
```

---

### Task 4: loki: the row (Read/Save/Finish/Revert), patches, Merge, Changed; config Skipped

**Files:**
- Create: none
- Modify: `packages/pstack/internal/loki/loki.go` (T2's file. Append the row section at the end and add any missing imports.)
- Modify: `packages/pstack/internal/config/config.go` (every edit is anchored on quoted existing text; line numbers are advisory only, because slice 1 is still moving them)
- Modify: `docs/usage.md` (the "What travels, and what deliberately does not" table, usage.md:2477-2489)
- Modify: `packages/pstack/CHANGELOG.md` (`## Unreleased`)
- Test: `packages/pstack/internal/loki/loki_test.go` (T2's file. Append.)
- Test: `packages/pstack/internal/config/config_test.go`

**Interfaces:**
- Consumes:
  - `loki_config` table, `store.Migrations[8]` (T1): `id INTEGER PRIMARY KEY CHECK (id = 1), config TEXT NOT NULL, secret TEXT NOT NULL, previous_config TEXT, previous_secret TEXT, updated_at INTEGER NOT NULL`.
  - From T2:
    - types `loki.Settings`, `Chunks`, `Storage` and `S3`, and the constants `StorageFilesystem` and `StorageS3`
    - `func Defaults() Settings`
    - `func Validate(s Settings, secret string, addingS3 bool, lead time.Duration) error`
    - `type Error struct{ Msg string }` and `func IsError(err error) bool`
    - `var ErrOneWay` and `var ErrFixed`
    - the package clock `var now = time.Now`
    - the test helper `func pin(t *testing.T, at time.Time)` in `loki_test.go`, which pins `now` for one test and restores it through `t.Cleanup`
  - `store.Store{DB *sql.DB}` (store.go:58-62) and `store.Open` (store.go:67).
  - `jsonx.Marshal` (jsonx.go:21). `jsonx` has no Unmarshal, so decoding uses `encoding/json`, as webhooks.go:116 does.
  - `config.Sources.Assemble` (config.go:203), and the test helpers `newHost` (config_test.go:47) and `assemble` (config_test.go:155).
- Produces:
  - `type Row struct { Settings Settings; Secret string; UpdatedAt int64; InFlight bool; Previous *Row }`
  - `func Read(st *store.Store) (*Row, error)`: returns `nil, nil` on an empty table.
    - `InFlight` means `previous_config IS NOT NULL`.
    - `InFlight && Previous == nil` means the table was empty before this apply, so rollback and resume render `Defaults()` (T10, T11).
    - `Previous.UpdatedAt` is always 0, because no column stores it.
  - `func Save(st *store.Store, s Settings, secret string, previous *Row) error`: one upsert statement. `previous` is the row read at step 2; `nil` is written as `''`.
  - `func Finish(st *store.Store) error`
  - `func Revert(st *store.Store) error`
  - `type ChunksPatch struct { RetentionDays int; Chunks Chunks }`
  - `type StoragePatch struct { Storage Storage; Secret string; KeepSecret bool }`
  - `func Merge(row *Row, c *ChunksPatch, sp *StoragePatch, lead time.Duration) (Settings, string, error)`. The checks run in this order: `ErrOneWay`, then `ErrFixed`, then `*Error` `secretAccessKey is required`, then `Validate`. A filesystem patch on a filesystem base changes nothing and ignores `KeepSecret`.
  - `func Changed(before *Row, after Settings, afterSecret string) []string`: never nil. `len(Changed(row, merged, secret)) == 0` is true exactly when the merge equals the row, or `Defaults()` when `row` is nil. T9 step 2 and T12 step 5 use it as their no-op test.
  - `config.Assemble` appends `loki: host-specific — re-enter it on the target` to `Skipped` when a row exists, and `loki: <err>` when `loki.Read` fails.

- [ ] **Step 1: Write the failing loki tests**

Append the code below to `packages/pstack/internal/loki/loki_test.go`. Add these imports to the file's import block if T2 has not already added them: `"errors"`, `"reflect"`, `"strings"`, `"time"`, `"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"` and `"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"`. T2's draft already imports errors, strings, time and jsonx, so normally only `reflect` and `store` are new.

The clock is pinned with T2's `pin(t, at)`, which lives in the same file; this task declares no clock helper of its own. The new helper names carry a prefix (`row…`, `patchS3`, `s3Row`) so they cannot collide with T2's `s3Settings`, `testSecret`, `pin` and `asEuid`.

```go
// ── the row, Merge, Changed (T4) ─────────────────────────────────────────────────────────────────

const (
	rowLead   = 15 * time.Minute // Lead(5m): the default ready timeout's lead
	rowKeyID  = "AKIAOLDKEY01"
	rowSecret = "old-secret-0001"
)

// rowNoon is the day before patchS3's cutover: EarliestCutover(rowNoon, rowLead) is 2026-09-16.
var rowNoon = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func openRowStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func readRow(t *testing.T, st *store.Store) *Row {
	t.Helper()
	r, err := Read(st)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// patchS3 is a complete, valid S3 storage save. Every call returns fresh pointers.
func patchS3(bucket string) *StoragePatch {
	return &StoragePatch{
		Storage: Storage{Type: StorageS3, S3: &S3{
			Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: bucket,
			AccessKeyID: rowKeyID, Cutover: "2026-09-16",
		}},
		Secret: rowSecret,
	}
}

// s3Row is a stored S3 row: the defaults plus patchS3("pstack-logs").
func s3Row() *Row {
	s := Defaults()
	s.Storage = patchS3("pstack-logs").Storage
	return &Row{Settings: s, Secret: rowSecret}
}

func TestRow(t *testing.T) {
	t.Run("an empty table reads as nil", func(t *testing.T) {
		// negative control: return &Row{Settings: Defaults()}, nil on sql.ErrNoRows → Read is non-nil and this fails
		if r := readRow(t, openRowStore(t)); r != nil {
			t.Fatalf("an empty table read as %+v", r)
		}
	})

	t.Run("a save over an empty table is in flight with no Previous, and Revert empties it again", func(t *testing.T) {
		// negative control: delete Revert's DELETE statement → the first save survives the revert and the last Read is non-nil
		st := openRowStore(t)
		s := s3Row().Settings
		if err := Save(st, s, rowSecret, nil); err != nil {
			t.Fatal(err)
		}
		r := readRow(t, st)
		if r == nil || !r.InFlight || r.Previous != nil {
			t.Fatalf("after a first save: %+v", r)
		}
		if !reflect.DeepEqual(r.Settings, s) || r.Secret != rowSecret || r.UpdatedAt == 0 {
			t.Fatalf("saved %+v / %q / %d", r.Settings, r.Secret, r.UpdatedAt)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		if r := readRow(t, st); r != nil {
			t.Fatalf("reverting a first save left %+v", r)
		}
	})

	t.Run("a save over a stored row carries it as Previous, and Revert restores it", func(t *testing.T) {
		// negative control: make Revert's UPDATE set config = config, secret = secret → the save survives and storage reads s3
		st := openRowStore(t)
		first := Defaults()
		first.RetentionDays = 14
		if err := Save(st, first, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		stored := readRow(t, st)
		second := s3Row().Settings
		if err := Save(st, second, rowSecret, stored); err != nil {
			t.Fatal(err)
		}
		inFlight := readRow(t, st)
		if inFlight == nil || !inFlight.InFlight || inFlight.Previous == nil ||
			!reflect.DeepEqual(inFlight.Previous.Settings, first) || inFlight.Previous.Secret != "" ||
			!reflect.DeepEqual(inFlight.Settings, second) || inFlight.Secret != rowSecret {
			t.Fatalf("in flight: %+v", inFlight)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		back := readRow(t, st)
		if back == nil || back.InFlight || back.Previous != nil || !reflect.DeepEqual(back.Settings, first) || back.Secret != "" {
			t.Fatalf("after Revert: %+v", back)
		}
	})

	t.Run("Finish keeps the save and clears previous_*; a Revert after it changes nothing", func(t *testing.T) {
		// negative control: make Finish set only previous_secret = NULL → previous_config stays '' and InFlight stays true
		st := openRowStore(t)
		s := s3Row().Settings
		if err := Save(st, s, rowSecret, nil); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		done := readRow(t, st)
		if done == nil || done.InFlight || done.Previous != nil || !reflect.DeepEqual(done.Settings, s) || done.Secret != rowSecret {
			t.Fatalf("after Finish: %+v", done)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		if again := readRow(t, st); !reflect.DeepEqual(again, done) {
			t.Fatalf("Revert with no apply in flight changed the row: %+v → %+v", done, again)
		}
	})
}

func TestMerge(t *testing.T) {
	t.Run("a nil row merges onto Defaults()", func(t *testing.T) {
		// negative control: start Merge from Settings{} when row is nil → Validate refuses retentionDays 0 and the empty storage type
		c := &ChunksPatch{RetentionDays: 14, Chunks: Chunks{IdlePeriodMinutes: 15, MaxAgeMinutes: 60, TargetSizeKiB: 1024, Encoding: "zstd"}}
		got, secret, err := Merge(nil, c, nil, rowLead)
		want := Defaults()
		want.RetentionDays, want.Chunks = c.RetentionDays, c.Chunks
		if err != nil || secret != "" || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v %q %v, want %+v", got, secret, err, want)
		}
	})

	t.Run("a chunks patch keeps the stored storage and secret", func(t *testing.T) {
		// negative control: start Merge from Defaults() even when a row is given → storage reads filesystem and the secret ""
		row := s3Row()
		got, secret, err := Merge(row, &ChunksPatch{RetentionDays: 30, Chunks: row.Settings.Chunks}, nil, rowLead)
		if err != nil || secret != rowSecret || got.RetentionDays != 30 || !reflect.DeepEqual(got.Storage, row.Settings.Storage) {
			t.Fatalf("got %+v %q %v", got, secret, err)
		}
	})

	t.Run("filesystem to S3 needs every field and a secret", func(t *testing.T) {
		// negative control: return merged without calling Validate → the patch with no region merges
		pin(t, rowNoon)
		row := &Row{Settings: Defaults()}
		got, secret, err := Merge(row, nil, patchS3("pstack-logs"), rowLead)
		if err != nil || secret != rowSecret || !reflect.DeepEqual(got.Storage, patchS3("pstack-logs").Storage) {
			t.Fatalf("a complete S3 add: %+v %q %v", got, secret, err)
		}
		noRegion := patchS3("pstack-logs")
		noRegion.Storage.S3.Region = ""
		if _, _, err := Merge(row, nil, noRegion, rowLead); !IsError(err) || !strings.Contains(err.Error(), "region") {
			t.Fatalf("no region: %v", err)
		}
		noSecret := patchS3("pstack-logs")
		noSecret.Secret = ""
		if _, _, err := Merge(row, nil, noSecret, rowLead); !IsError(err) {
			t.Fatalf("no secret: %v", err)
		}
		keep := patchS3("pstack-logs")
		keep.Secret, keep.KeepSecret = "", true
		if _, _, err := Merge(row, nil, keep, rowLead); !IsError(err) || !strings.Contains(err.Error(), "secretAccessKey is required") {
			t.Fatalf("keepSecret with nothing stored: %v", err)
		}
	})

	t.Run("only the save that adds S3 checks the cutover against earliestCutover", func(t *testing.T) {
		// negative control: compute addingS3 as merged.Storage.Type == StorageS3 → the rotation on a host past its cutover is refused
		pin(t, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
		if _, _, err := Merge(&Row{Settings: Defaults()}, nil, patchS3("pstack-logs"), rowLead); !IsError(err) || !strings.Contains(err.Error(), "cutover") {
			t.Fatalf("an S3 add with a past cutover: %v", err)
		}
		rotate := patchS3("pstack-logs")
		rotate.Secret = "new-secret-0002"
		if _, _, err := Merge(s3Row(), nil, rotate, rowLead); err != nil {
			t.Fatalf("a rotation on a host past its cutover: %v", err)
		}
	})

	t.Run("S3 to filesystem is ErrOneWay, before the cutover too", func(t *testing.T) {
		// negative control: delete the ErrOneWay case → the filesystem patch reaches Validate and merges
		pin(t, rowNoon) // the stored cutover, 2026-09-16, has not come
		_, _, err := Merge(s3Row(), nil, &StoragePatch{Storage: Storage{Type: StorageFilesystem}}, rowLead)
		if !errors.Is(err, ErrOneWay) || IsError(err) {
			t.Fatalf("S3 → filesystem: %v (a *Error would be a 400)", err)
		}
	})

	t.Run("a changed endpoint, region, bucket, pathStyle or cutover is ErrFixed", func(t *testing.T) {
		// negative control: delete the ErrFixed case → every changed patch merges
		for name, change := range map[string]func(*S3){
			"endpoint":  func(s *S3) { s.Endpoint = "https://minio.internal:9000" },
			"region":    func(s *S3) { s.Region = "us-east-1" },
			"bucket":    func(s *S3) { s.Bucket = "other-logs" },
			"pathStyle": func(s *S3) { s.PathStyle = true },
			"cutover":   func(s *S3) { s.Cutover = "2026-09-17" },
		} {
			p := patchS3("pstack-logs")
			change(p.Storage.S3)
			if _, _, err := Merge(s3Row(), nil, p, rowLead); !errors.Is(err, ErrFixed) || IsError(err) {
				t.Errorf("changed %s: %v", name, err)
			}
		}
	})

	t.Run("credentials rotate, and KeepSecret keeps the stored secret", func(t *testing.T) {
		// negative control: resolve KeepSecret to sp.Secret → the kept secret comes back "" and Validate refuses it
		rotate := patchS3("pstack-logs")
		rotate.Storage.S3.AccessKeyID, rotate.Secret = "AKIANEWKEY02", "new-secret-0002"
		got, secret, err := Merge(s3Row(), nil, rotate, rowLead)
		if err != nil || secret != "new-secret-0002" || got.Storage.S3.AccessKeyID != "AKIANEWKEY02" {
			t.Fatalf("rotation: %+v %q %v", got.Storage.S3, secret, err)
		}
		keep := patchS3("pstack-logs")
		keep.Secret, keep.KeepSecret = "", true
		if _, secret, err := Merge(s3Row(), nil, keep, rowLead); err != nil || secret != rowSecret {
			t.Fatalf("keepSecret: %q %v", secret, err)
		}
	})

	t.Run("KeepSecret with a changed accessKeyId is refused", func(t *testing.T) {
		// negative control: drop the key-id comparison from the KeepSecret case → the old secret is kept under the new key id
		keep := patchS3("pstack-logs")
		keep.Storage.S3.AccessKeyID = "AKIANEWKEY02"
		keep.Secret, keep.KeepSecret = "", true
		if _, _, err := Merge(s3Row(), nil, keep, rowLead); !IsError(err) || !strings.Contains(err.Error(), "secretAccessKey is required") {
			t.Fatalf("a new key id with the old secret: %v", err)
		}
	})

	t.Run("a filesystem patch on a filesystem host changes nothing", func(t *testing.T) {
		// negative control: delete the filesystem-to-filesystem case → KeepSecret finds no stored secret and refuses
		row := &Row{Settings: Defaults()}
		row.Settings.RetentionDays = 14
		got, secret, err := Merge(row, nil, &StoragePatch{Storage: Storage{Type: StorageFilesystem}, KeepSecret: true}, rowLead)
		if err != nil || secret != "" || !reflect.DeepEqual(got, row.Settings) {
			t.Fatalf("got %+v %q %v", got, secret, err)
		}
	})

	t.Run("two queued storage saves with different buckets: the second is ErrFixed once the first is stored", func(t *testing.T) {
		// negative control: delete the ErrFixed case → the second bucket merges over the first
		pin(t, rowNoon)
		st := openRowStore(t)
		fs := readRow(t, st) // nil: the empty table is slice 1's filesystem config
		a, b := patchS3("pstack-logs"), patchS3("other-logs")
		mergedA, secretA, err := Merge(fs, nil, a, rowLead)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Merge(fs, nil, b, rowLead); err != nil {
			t.Fatalf("both saves are accepted while the row is filesystem: %v", err)
		}
		if err := Save(st, mergedA, secretA, fs); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Merge(readRow(t, st), nil, b, rowLead); !errors.Is(err, ErrFixed) {
			t.Fatalf("the second save against the stored first: %v", err)
		}
	})
}

func TestChanged(t *testing.T) {
	t.Run("nothing changed is [] and never null", func(t *testing.T) {
		// negative control: declare `var changed []string` in Changed → it marshals as null
		b, err := jsonx.Marshal(Changed(nil, Defaults(), ""))
		if err != nil || string(b) != "[]" {
			t.Fatalf("no change marshals as %s (%v)", b, err)
		}
		if got := Changed(s3Row(), s3Row().Settings, rowSecret); len(got) != 0 {
			t.Fatalf("an equal S3 row: %v", got)
		}
	})

	t.Run("a retention-only change is retention", func(t *testing.T) {
		// negative control: drop the RetentionDays comparison → []
		after := Defaults()
		after.RetentionDays = 14
		if got := Changed(nil, after, ""); !reflect.DeepEqual(got, []string{"retention"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a secret rotation and a key-id rotation are credentials, not storage", func(t *testing.T) {
		// negative control: compare the S3 structs whole for storage → the key-id rotation reads [storage credentials]
		row := s3Row()
		if got := Changed(row, row.Settings, "new-secret-0002"); !reflect.DeepEqual(got, []string{"credentials"}) {
			t.Fatalf("secret rotation: %v", got)
		}
		after := s3Row().Settings
		after.Storage.S3.AccessKeyID = "AKIANEWKEY02"
		if got := Changed(row, after, rowSecret); !reflect.DeepEqual(got, []string{"credentials"}) {
			t.Fatalf("key-id rotation: %v", got)
		}
	})

	t.Run("filesystem to S3 with new chunks and retention is all four, in order", func(t *testing.T) {
		// negative control: append "credentials" before "storage" → the order differs
		after := s3Row().Settings
		after.RetentionDays = 30
		after.Chunks.Encoding = "zstd"
		want := []string{"chunks", "retention", "storage", "credentials"}
		if got := Changed(nil, after, rowSecret); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}
```

- [ ] **Step 2: Run the loki tests and watch them fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -run 'TestRow|TestMerge|TestChanged'
```

Expected: the build fails. The compiler reports `undefined: Read`, `undefined: Save`, `undefined: Finish`, `undefined: Revert`, `undefined: Row`, `undefined: Merge`, `undefined: ChunksPatch`, `undefined: StoragePatch` and `undefined: Changed`, followed by `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/loki [build failed]`.

- [ ] **Step 3: Implement the row, the patches, Merge and Changed**

In `packages/pstack/internal/loki/loki.go`, add these imports to the import block if T2 has not already added them: `"database/sql"`, `"encoding/json"`, `"errors"`, `"time"`, `"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"` and `"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"`. Then append this at the end of the file:

```go
// ── THE ROW ──────────────────────────────────────────────────────────────────────────────────────
//
// loki_config (migration 9) is what an operator SAVED, never what Loki runs: every schema-period
// decision reads config.yaml. previous_* is one apply's write-ahead undo record, written in the same
// statement as the save. NULL: no apply in flight. '': the table was empty before it.
//
// Everything here uses st.DB, so none of it may run inside store.Tx (one connection, Go rule 16).

// Row is the stored settings. Previous is set only while an apply is in flight over an earlier row;
// InFlight with a nil Previous means the table was empty before, so undoing renders Defaults().
// Previous.UpdatedAt is 0: no column keeps it.
type Row struct {
	Settings  Settings
	Secret    string
	UpdatedAt int64
	InFlight  bool
	Previous  *Row
}

// Read is the stored row, or nil when the table is empty (slice 1's config exactly).
func Read(st *store.Store) (*Row, error) {
	var config string
	var previous, previousSecret sql.NullString
	r := &Row{}
	err := st.DB.QueryRow("SELECT config, secret, previous_config, previous_secret, updated_at FROM loki_config WHERE id = 1").
		Scan(&config, &r.Secret, &previous, &previousSecret, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(config), &r.Settings); err != nil {
		return nil, err
	}
	r.InFlight = previous.Valid
	if previous.Valid && previous.String != "" {
		r.Previous = &Row{Secret: previousSecret.String}
		if err := json.Unmarshal([]byte(previous.String), &r.Previous.Settings); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Save stores a save and its undo record in ONE statement, so no crash leaves one without the
// other. previous is the row the apply read (nil: the table was empty).
func Save(st *store.Store, s Settings, secret string, previous *Row) error {
	config, err := jsonx.Marshal(s)
	if err != nil {
		return err
	}
	previousConfig, previousSecret := "", ""
	if previous != nil {
		b, err := jsonx.Marshal(previous.Settings)
		if err != nil {
			return err
		}
		previousConfig, previousSecret = string(b), previous.Secret
	}
	_, err = st.DB.Exec(
		"INSERT INTO loki_config (id, config, secret, previous_config, previous_secret, updated_at) VALUES (1, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET config = excluded.config, secret = excluded.secret, "+
			"previous_config = excluded.previous_config, previous_secret = excluded.previous_secret, updated_at = excluded.updated_at",
		string(config), secret, previousConfig, previousSecret, now().UnixMilli())
	return err
}

// Finish ends an apply: the save stays, the undo record goes.
func Finish(st *store.Store) error {
	_, err := st.DB.Exec("UPDATE loki_config SET previous_config = NULL, previous_secret = NULL WHERE id = 1")
	return err
}

// Revert undoes an apply: the row becomes previous, or goes when previous is ''. The two WHEREs
// are disjoint and NULL matches neither, so the pair needs no transaction and leaves a row with no
// apply in flight untouched.
func Revert(st *store.Store) error {
	if _, err := st.DB.Exec("DELETE FROM loki_config WHERE previous_config = ''"); err != nil {
		return err
	}
	_, err := st.DB.Exec("UPDATE loki_config SET config = previous_config, secret = previous_secret, " +
		"previous_config = NULL, previous_secret = NULL WHERE previous_config <> ''")
	return err
}

// ChunksPatch is PUT /api/logging's body: the whole chunks-and-retention section.
type ChunksPatch struct {
	RetentionDays int
	Chunks        Chunks
}

// StoragePatch is PUT /api/logging/storage's body. KeepSecret is an empty or masked
// secretAccessKey, resolved against the row Merge is given, never the one the request saw.
type StoragePatch struct {
	Storage    Storage
	Secret     string
	KeepSecret bool
}

// Merge lays the patches over the row (Defaults() when there is none) and resolves KeepSecret.
// The storage rules run against THIS row, so a second save that queued behind a first is refused
// once the first is stored. ErrOneWay and ErrFixed (409) come before *Error (400), and Validate
// runs last. addingS3 is filesystem → s3 only, so a rotation on a host past its cutover passes.
func Merge(row *Row, c *ChunksPatch, sp *StoragePatch, lead time.Duration) (Settings, string, error) {
	base, secret := Defaults(), ""
	if row != nil {
		base, secret = row.Settings, row.Secret
	}
	merged := base
	if c != nil {
		merged.RetentionDays, merged.Chunks = c.RetentionDays, c.Chunks
	}
	if sp != nil {
		switch {
		case base.Storage.Type == StorageS3 && sp.Storage.Type == StorageFilesystem:
			return Settings{}, "", ErrOneWay
		case base.Storage.Type == StorageS3 && sp.Storage.Type == StorageS3 && fixed(base.Storage) != fixed(sp.Storage):
			return Settings{}, "", ErrFixed
		case base.Storage.Type == StorageFilesystem && sp.Storage.Type == StorageFilesystem:
			// Filesystem has no fields and no secret: nothing to change.
		default:
			merged.Storage = sp.Storage
			switch {
			case !sp.KeepSecret:
				secret = sp.Secret
			case secret == "" || keyID(base.Storage) != keyID(sp.Storage):
				// A new key id with the old secret is never right.
				return Settings{}, "", &Error{Msg: "secretAccessKey is required"}
			}
		}
	}
	addingS3 := base.Storage.Type == StorageFilesystem && merged.Storage.Type == StorageS3
	if err := Validate(merged, secret, addingS3, lead); err != nil {
		return Settings{}, "", err
	}
	return merged, secret, nil
}

// Changed is logging.changed's `changed`, in this order: chunks, retention, storage (the type or
// any S3 field but the key id), credentials (the key id or the secret). Never nil. Empty means
// after equals the row, which is how the apply and the PUT spot a no-op.
func Changed(before *Row, after Settings, afterSecret string) []string {
	b, secret := Defaults(), ""
	if before != nil {
		b, secret = before.Settings, before.Secret
	}
	changed := []string{}
	if b.Chunks != after.Chunks {
		changed = append(changed, "chunks")
	}
	if b.RetentionDays != after.RetentionDays {
		changed = append(changed, "retention")
	}
	if b.Storage.Type != after.Storage.Type || fixed(b.Storage) != fixed(after.Storage) {
		changed = append(changed, "storage")
	}
	if keyID(b.Storage) != keyID(after.Storage) || secret != afterSecret {
		changed = append(changed, "credentials")
	}
	return changed
}

// fixed is what an S3 save makes permanent: every S3 field but the key id.
func fixed(st Storage) S3 {
	if st.S3 == nil {
		return S3{}
	}
	f := *st.S3
	f.AccessKeyID = ""
	return f
}

func keyID(st Storage) string {
	if st.S3 == nil {
		return ""
	}
	return st.S3.AccessKeyID
}
```

- [ ] **Step 4: Run the loki tests and watch them pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -run 'TestRow|TestMerge|TestChanged' -v
```

Expected: `--- PASS` for all 18 subtests (4 under `TestRow`, 10 under `TestMerge` and 4 under `TestChanged`), then `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/loki`.

- [ ] **Step 5: Write the failing config test**

In `packages/pstack/internal/config/config_test.go`, add `"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"` to the import block, between the `internal/jsonx` and `internal/notify` lines. Then insert this test directly above the existing line `// negative control: drop the \`d.Version != FormatVersion\` check in Parse — the unknown document`:

```go
// negative control: delete the loki block in Assemble → a stored row goes unnamed and the second check
// fails. (Appending the line whether or not a row exists fails the first check instead.)
func TestAssembleSkipsTheLokiRow(t *testing.T) {
	const line = "loki: host-specific — re-enter it on the target"
	h := newHost(t)
	before := assemble(t, h).Skipped
	for _, s := range before {
		if strings.HasPrefix(s, "loki:") {
			t.Fatalf("an empty loki_config table was named: %v", before)
		}
	}
	// An in-flight first save is still a stored row.
	if err := loki.Save(h.Store, loki.Defaults(), "", nil); err != nil {
		t.Fatal(err)
	}
	after := assemble(t, h).Skipped
	n := 0
	for _, s := range after {
		if s == line {
			n++
		}
	}
	if n != 1 || len(after) != len(before)+1 {
		t.Fatalf("skipped after a save: %v — want exactly one more line, %q", after, line)
	}
}
```

None of the existing `sum.Skipped` counts (config_test.go:286, :335, :511) move. They count Apply's `Summary`, not `Document.Skipped`, and `populate` stores no Loki row.

- [ ] **Step 6: Run the config test and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/config/ -run TestAssembleSkipsTheLokiRow
```

Expected: `--- FAIL: TestAssembleSkipsTheLokiRow`, followed by `skipped after a save: [] — want exactly one more line, "loki: host-specific — re-enter it on the target"` and `FAIL`. The printed list holds whatever a fresh host already skips.

- [ ] **Step 7: Implement the Skipped line and its header sentence**

In `packages/pstack/internal/config/config.go`:

(a) Import block: add `"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"` between the `internal/jsonx` and `internal/notify` lines.

(b) Header. Replace:

```go
// restored session id is a live credential nobody minted.
```

with:

```go
// restored session id is a live credential nobody minted. Loki's settings stay behind too: a bucket
// and a cutover date belong to one host's Loki. Assemble names them in Skipped.
```

(c) In `Assemble`, replace:

```go
		d.Routing = append(d.Routing, RoutingFile{Name: f.Name, Content: content})
	}
	if d.Specs, d.Skipped, err = s.specList(d.Skipped); err != nil {
```

with:

```go
		d.Routing = append(d.Routing, RoutingFile{Name: f.Name, Content: content})
	}
	// Not carried either way, so a read error is a skip line rather than a failed export (the routing
	// precedent above): the export is a recovery tool.
	if row, err := loki.Read(s.Store); err != nil {
		d.Skipped = append(d.Skipped, "loki: "+err.Error())
	} else if row != nil {
		d.Skipped = append(d.Skipped, "loki: host-specific — re-enter it on the target")
	}
	if d.Specs, d.Skipped, err = s.specList(d.Skipped); err != nil {
```

- [ ] **Step 8: Run the config test and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/config/ -run TestAssembleSkipsTheLokiRow -v
```

Expected: `--- PASS: TestAssembleSkipsTheLokiRow`, then `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/config`.

- [ ] **Step 9: Run every negative control**

For each row in the table below:

1. Apply the mutation by hand.
2. Run the command for that package.
3. Confirm the named test fails.
4. Undo the edit.

The commands:
- **loki:** `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/loki/ -run 'TestRow|TestMerge|TestChanged'`
- **config:** the Step 6 command with `-count=1` added.

| Mutation | Must fail |
|---|---|
| `Read`: `return &Row{Settings: Defaults()}, nil` on `sql.ErrNoRows` | `TestRow/an_empty_table_reads_as_nil` |
| `Revert`: delete the `DELETE` statement | `TestRow/a_save_over_an_empty_table_is_in_flight…` |
| `Revert`'s `UPDATE`: `SET config = config, secret = secret, …` | `TestRow/a_save_over_a_stored_row_carries_it…` |
| `Finish`: `SET previous_secret = NULL` only | `TestRow/Finish_keeps_the_save…` |
| `Merge`: `base, secret := Settings{}, ""` | `TestMerge/a_nil_row_merges_onto_Defaults()` |
| `Merge`: ignore `row` (always `Defaults()`) | `TestMerge/a_chunks_patch_keeps…` |
| `Merge`: `return merged, secret, nil` before `Validate` | `TestMerge/filesystem_to_S3_needs…` |
| `addingS3 := merged.Storage.Type == StorageS3` | `TestMerge/only_the_save_that_adds_S3…` |
| delete the `ErrOneWay` case | `TestMerge/S3_to_filesystem_is_ErrOneWay…` |
| delete the `ErrFixed` case | `TestMerge/a_changed_endpoint…` and `TestMerge/two_queued_storage_saves…` |
| `case !sp.KeepSecret:` → `case true:` | `TestMerge/credentials_rotate…` |
| drop `\|\| keyID(base.Storage) != keyID(sp.Storage)` | `TestMerge/KeepSecret_with_a_changed_accessKeyId…` |
| delete the filesystem-to-filesystem case | `TestMerge/a_filesystem_patch_on_a_filesystem_host…` |
| `Changed`: `var changed []string` | `TestChanged/nothing_changed_is_[]…` |
| `Changed`: drop the `RetentionDays` comparison | `TestChanged/a_retention-only_change…` |
| `Changed`: compare `*b.Storage.S3 != *after.Storage.S3` for storage (nil-guarded) | `TestChanged/a_secret_rotation_and_a_key-id_rotation…` |
| `Changed`: move the credentials check above storage | `TestChanged/filesystem_to_S3…all_four,_in_order` |
| `Assemble`: delete the loki block | `TestAssembleSkipsTheLokiRow` (second check) |
| `Assemble`: append the line unconditionally | `TestAssembleSkipsTheLokiRow` (first check) |

- [ ] **Step 10: Docs and CHANGELOG**

(a) In `docs/usage.md`, under `### What travels, and what deliberately does not`, replace:

```
| the SSO providers and their client secrets | |
```

with:

```
| the SSO providers and their client secrets | Loki's settings and its S3 secret |
```

Then replace:

```
none of it is in the file, and nothing in the file names it.
```

with:

```
none of it is in the file. Only Loki's settings are named, in `skipped`: re-enter them on the target.
```

(b) In `packages/pstack/CHANGELOG.md`, find `## Unreleased`. If slice 1's T14 has not created it, insert it between `# Changelog` and `## 0.39.1 — 2026-09-14`. Add a `### Changed` subsection under it if there is none, and put this bullet there:

```
- **A config export names Loki's settings in `skipped`** (`loki: host-specific — re-enter it on the target`). They are not carried.
```

- [ ] **Step 11: Package gates**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ ./internal/config/ ./internal/store/ && go vet ./internal/loki/ ./internal/config/ && gofmt -l internal/loki internal/config
```

Expected: three `ok` lines, and no output from vet or gofmt.

- [ ] **Step 12: Commit**

Run `but status` and take the file IDs for:
- `packages/pstack/internal/loki/loki.go`
- `packages/pstack/internal/loki/loki_test.go`
- `packages/pstack/internal/config/config.go`
- `packages/pstack/internal/config/config_test.go`
- `docs/usage.md`
- `packages/pstack/CHANGELOG.md`

Leave out `packages/conformance/golden/host/db/*` and `.superpowers/`. Then run:

```
but commit -b claude/loki-logging-settings -m "feat(loki): the settings row, Merge and Changed; config export skips it" <ids>
```

---

### Task 5: loki: the S3 probe

**Files:**
- Create: `packages/pstack/internal/loki/probe.go`
- Modify: `docs/control-plane.md` (a new `### The probe` subsection goes in just before the unique heading `## 6. Submitting a deployment`. That heading is control-plane.md:980 today. Once slice-1 T8 lands, §5g sits right above it, with T3's period-guard text at its end. Line numbers are only a guide.)
- Test: `packages/pstack/internal/loki/probe_test.go`

**Interfaces:**
- Consumes (T2, `packages/pstack/internal/loki/loki.go`):
  - `type S3 struct { Endpoint, Region, Bucket string; PathStyle bool; AccessKeyID, Cutover string }`. Probe reads Endpoint, Region, Bucket, PathStyle and AccessKeyID, and never reads Cutover.
  - `type Error struct{ Msg string }` and `func IsError(err error) bool`.
  - `var now = time.Now`, which gives the signing time. Do not declare it again in probe.go.
- Produces:
  - `func Probe(ctx context.Context, s S3, secret string) error`. It returns nil on success. Every refusal and transport failure is a `*Error` (400 through `fail()`). A failed `crypto/rand` read comes back raw, so it is a 500.
  - `var probeClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}`
  - `func sign(req *http.Request, payloadHash, keyID, secret, region string, t time.Time)`. It signs `host` plus every header already on `req`. The canonical query is always empty.
  - Package-private names (T12 and anyone else reading the package will see them): `const emptySHA256`, `var s3Code = regexp.MustCompile(`^[A-Za-z]{1,64}$`)`, `func probeSend(ctx context.Context, method, target string, s S3, secret string) (string, error)`, `func hmacSHA256(key []byte, data string) []byte`.
  - Test-only names in `probe_test.go`: `probeFake`, `probeKeyRe`, `probePutThenDelete`, `probeS3`.
  - Message formats T12 passes through unchanged:
    - `S3 unreachable: <err>`
    - `S3 refused the probe: <status>[ <Code>]`
    - `S3 unreachable: <err> — <key> is left in the bucket`, when the DELETE fails in transport
    - `S3 refused the probe delete: <status>[ <Code>] — <key> is left in the bucket`
- No test in this file may call `t.Parallel()`: `TestProbeVirtualHosted` swaps the package-level `probeClient`, and T2-T4 tests share the package.

- [ ] **Step 1: Write the failing test**

Create `packages/pstack/internal/loki/probe_test.go`:

```go
package loki

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeFake records every request and answers PUT with put and DELETE with del; a non-2xx carries
// body. verified[i] is whether request i's Authorization re-signs from the request as received.
type probeFake struct {
	put, del int
	body     string

	mu       sync.Mutex
	calls    []string // "<METHOD> <host><path>"
	verified []bool
}

func (f *probeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.EscapedPath(), nil)
	at, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	sign(again, r.Header.Get("X-Amz-Content-Sha256"), "AKID", "secret-key", "us-east-1", at)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.Host+r.URL.Path)
	f.verified = append(f.verified, again.Header.Get("Authorization") == r.Header.Get("Authorization") &&
		r.Header.Get("X-Amz-Content-Sha256") == emptySHA256)
	f.mu.Unlock()
	status := f.put
	if r.Method == http.MethodDelete {
		status = f.del
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if status/100 != 2 {
		_, _ = io.WriteString(w, f.body)
	}
}

func (f *probeFake) snapshot() ([]string, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]bool(nil), f.verified...)
}

var probeKeyRe = regexp.MustCompile(`^pstack-probe-[0-9a-f]{16}$`)

// probePutThenDelete asserts exactly PUT then DELETE of one pstack-probe-<16 hex> key under prefix
// (`<host><path up to the key>`), both signed, and returns the key.
func probePutThenDelete(t *testing.T, f *probeFake, prefix string) string {
	t.Helper()
	calls, verified := f.snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want PUT then DELETE", calls)
	}
	key := strings.TrimPrefix(calls[0], "PUT "+prefix)
	if !probeKeyRe.MatchString(key) || calls[1] != "DELETE "+prefix+key {
		t.Fatalf("calls = %q, want PUT then DELETE of %spstack-probe-<16 hex>", calls, prefix)
	}
	if !verified[0] || !verified[1] {
		t.Fatalf("signatures verified = %v, want both", verified)
	}
	return key
}

func probeS3(endpoint string, pathStyle bool) S3 {
	return S3{Endpoint: endpoint, Region: "us-east-1", Bucket: "logs", PathStyle: pathStyle, AccessKeyID: "AKID"}
}

// negative control: sign only host and x-amz-* (skip the other headers on req) → range is not
// signed and the Authorization differs from AWS's published one.
func TestSignMatchesAWSGetObjectExample(t *testing.T) {
	// AWS's S3 SigV4 example "GET Object" (sig-v4-header-based-auth): examplebucket, test.txt,
	// Range bytes=0-9, 2013-05-24, the documented example key pair.
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")
	sign(req, emptySHA256, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20130524T000000Z" {
		t.Fatalf("x-amz-date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// negative control: build the path-style URL without the bucket → the server sees
// /pstack-probe-… with no /logs/ segment.
func TestProbePathStyle(t *testing.T) {
	f := &probeFake{put: 200, del: 204}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	if err := Probe(context.Background(), probeS3(srv.URL+"/", true), "secret-key"); err != nil {
		t.Fatal(err)
	}
	probePutThenDelete(t, f, strings.TrimPrefix(srv.URL, "http://")+"/logs/")
}

// negative control: ignore PathStyle (always path-style) → the server sees host minio.test:9000
// and /logs/pstack-probe-….
func TestProbeVirtualHosted(t *testing.T) {
	f := &probeFake{put: 200, del: 200}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	c := *probeClient
	c.Transport = tr
	old := probeClient
	probeClient = &c
	t.Cleanup(func() { probeClient = old })

	if err := Probe(context.Background(), probeS3("http://minio.test:9000", false), "secret-key"); err != nil {
		t.Fatal(err)
	}
	probePutThenDelete(t, f, "logs.minio.test:9000/")
}

// negative control: append the response body to the refusal (read it, then unmarshal) → the
// message carries "does not exist in our records".
func TestProbeRefusedPutNeverEchoesTheBody(t *testing.T) {
	f := &probeFake{put: 403, body: `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Error><Code>InvalidAccessKeyId</Code><Message>The AWS Access Key Id you provided does not exist in our records.</Message><RequestId>4442587FB7D0A2F9</RequestId></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 403 InvalidAccessKeyId" {
		t.Fatalf("err = %v, want *Error `S3 refused the probe: 403 InvalidAccessKeyId`", err)
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("the body leaked: %v", err)
	}
	if calls, _ := f.snapshot(); len(calls) != 1 {
		t.Fatalf("calls = %q, want the PUT alone (no DELETE after a refused PUT)", calls)
	}
}

// negative control: drop the ^[A-Za-z]{1,64}$ check (append any non-empty Code) → the message
// carries "<script>".
func TestProbeRefusedPutOmitsACodeOutsideTheCharset(t *testing.T) {
	f := &probeFake{put: 400, body: `<Error><Code>&lt;script&gt;</Code></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 400" {
		t.Fatalf("err = %v, want *Error `S3 refused the probe: 400`", err)
	}
}

// negative control: delete CheckRedirect from probeClient → the client follows the 302 to target,
// which records a request, and the probe passes.
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	target := &probeFake{put: 200, del: 200}
	tsrv := httptest.NewServer(target)
	t.Cleanup(tsrv.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tsrv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	err := Probe(context.Background(), probeS3(redirect.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 302" {
		t.Fatalf("err = %v, want `S3 refused the probe: 302`", err)
	}
	if calls, _ := target.snapshot(); len(calls) != 0 {
		t.Fatalf("the redirect was followed: %q", calls)
	}
}

// negative control: drop the key from the delete refusal → the message no longer names the object.
func TestProbeRefusedDeleteNamesTheObject(t *testing.T) {
	f := &probeFake{put: 200, del: 403, body: `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	key := probePutThenDelete(t, f, strings.TrimPrefix(srv.URL, "http://")+"/logs/")
	want := "S3 refused the probe delete: 403 AccessDenied — " + key + " is left in the bucket"
	if !IsError(err) || err.Error() != want {
		t.Fatalf("err = %v, want *Error %q", err, want)
	}
}

// negative control: return the client error without unwrapping *url.Error → the message carries
// `Put "http://…"`.
func TestProbeUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()
	err := Probe(context.Background(), probeS3(endpoint, true), "secret-key")
	if !IsError(err) || !strings.HasPrefix(err.Error(), "S3 unreachable: ") || strings.Contains(err.Error(), `Put "`) {
		t.Fatalf("err = %v, want *Error `S3 unreachable: <dial error>`", err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ -run 'TestSign|TestProbe'
```

Expected: the build fails because `probe.go` does not exist yet. The errors include `undefined: sign`, `undefined: emptySHA256`, `undefined: Probe` and `undefined: probeClient`, and the result is `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/loki [build failed]`.

- [ ] **Step 3: Implement**

Create `packages/pstack/internal/loki/probe.go`:

```go
package loki

// ── THE PROBE ────────────────────────────────────────────────────────────────────────────────────
//
// -verify-config builds no storage client, and before the cutover Loki writes nothing to S3, so a
// wrong bucket would first surface at the first flush after midnight UTC, with nobody watching.
// Probe runs inside PUT /api/logging/storage instead: a SigV4-signed PUT of an empty object, then a
// DELETE of it (the compactor deletes chunks for retention). That proves the endpoint, TLS, the
// region, the bucket, the credentials, and write and delete permission.
//
// It does NOT refuse loopback, link-local or private addresses, unlike notify: an internal MinIO,
// Ceph or Garage is the normal case, and the caller is an admin. A redirect is a refusal, never a
// hop, and the response body is never echoed — only S3's <Code>, charset-checked.
//
// Ceiling: the probe runs from pstack's networks, not Loki's. An endpoint only Loki can reach fails
// the probe; one only pstack can reach passes it and fails in Loki after the cutover.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// emptySHA256 is hex(SHA-256("")): the probe object is empty.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// probeClient: 10s per request, and a 3xx is answered, not followed (Go rule 12). A var so a test
// can swap the Transport's dialer. http.Client parses Location before asking CheckRedirect, so a 3xx
// with an unparsable Location comes back as `S3 unreachable: …` — still a refusal.
var probeClient = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var s3Code = regexp.MustCompile(`^[A-Za-z]{1,64}$`)

// Probe PUTs then DELETEs an empty pstack-probe-<16 hex> object in s.Bucket. Every failure is a
// *Error (400).
func Probe(ctx context.Context, s S3, secret string) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	key := "pstack-probe-" + hex.EncodeToString(b[:])
	u, err := url.Parse(strings.TrimSuffix(s.Endpoint, "/"))
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error()}
	}
	if s.PathStyle {
		u.Path += "/" + s.Bucket + "/" + key
	} else {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	target := u.String()
	refused, err := probeSend(ctx, http.MethodPut, target, s, secret)
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error()}
	}
	if refused != "" {
		return &Error{"S3 refused the probe: " + refused}
	}
	left := " — " + key + " is left in the bucket"
	refused, err = probeSend(ctx, http.MethodDelete, target, s, secret)
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error() + left}
	}
	if refused != "" {
		return &Error{"S3 refused the probe delete: " + refused + left}
	}
	return nil
}

// probeSend returns "" on a 2xx, else "<status>" or "<status> <Code>". err is a transport error
// with Go's `Put "<url>": ` prefix removed.
func probeSend(ctx context.Context, method, target string, s S3, secret string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return "", err
	}
	sign(req, emptySHA256, s.AccessKeyID, secret, s.Region, now())
	resp, err := probeClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return "", nil
	}
	var e struct{ Code string }
	_ = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	refused := strconv.Itoa(resp.StatusCode)
	if s3Code.MatchString(e.Code) {
		refused += " " + e.Code
	}
	return refused, nil
}

// sign sets x-amz-date, x-amz-content-sha256 and a SigV4 Authorization for service s3. It signs
// host plus every header already on req. The canonical query is empty: probe requests carry none.
func sign(req *http.Request, payloadHash, keyID, secret, region string, t time.Time) {
	stamp := t.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, vs := range req.Header {
		n := strings.ToLower(k)
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		names = append(names, n)
		values[n] = strings.Join(trimmed, ",")
	}
	sort.Strings(names)
	var headers strings.Builder
	for _, n := range names {
		headers.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	scope := day + "/" + region + "/s3/aws4_request"
	canonical := req.Method + "\n" + req.URL.EscapedPath() + "\n\n" + headers.String() + "\n" + signed + "\n" + payloadHash
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+
		",SignedHeaders="+signed+",Signature="+hex.EncodeToString(hmacSHA256(k, toSign)))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
```

Notes for the builder:
- The map in `sign` is never ranged into output. The names are sorted before they are written (rule 5).
- For a PUT with a nil body, Go sends `Content-Length: 0`, which S3 requires. The header is left unsigned, and SigV4 allows that.
- The secret only ever feeds the HMAC, so it cannot appear in any error string.

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/loki/ && go vet ./internal/loki/ && gofmt -l internal/loki/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/loki`, then no output from `go vet` or `gofmt -l`. The T2-T4 tests in the package still pass.

- [ ] **Step 5: Run every negative control**

Apply each mutation below to `probe.go` on its own, run the named test, confirm that it FAILS, then revert. Use the Step 4 command with `-run '<Test>$'`. All eight were checked against a copy of this code, and each failed only its own test.

| Test | Mutation in probe.go |
|---|---|
| `TestSignMatchesAWSGetObjectExample` | In `sign`'s header loop, `continue` on any key without the `x-amz-` prefix |
| `TestProbePathStyle` | `u.Path += "/" + s.Bucket + "/" + key` → `u.Path += "/" + key` |
| `TestProbeVirtualHosted` | `if s.PathStyle {` → `if true {` |
| `TestProbeRefusedPutNeverEchoesTheBody` | Replace the `xml.NewDecoder(...).Decode(&e)` line with `raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); _ = xml.Unmarshal(raw, &e)`, and append `" " + string(raw)` to `refused` |
| `TestProbeRefusedPutOmitsACodeOutsideTheCharset` | `if s3Code.MatchString(e.Code) {` → `if e.Code != "" {` |
| `TestProbeDoesNotFollowRedirects` | Delete the `CheckRedirect:` line from `probeClient` |
| `TestProbeRefusedDeleteNamesTheObject` | `"S3 refused the probe delete: " + refused + left` → `"S3 refused the probe delete: " + refused` |
| `TestProbeUnreachable` | `err = ue.Err` → `_ = ue` |

Caution for the body control: if the body is drained after `Decode`, the test still passes, because the decoder has already consumed it. Read the body first, exactly as the table says.

- [ ] **Step 6: Document the probe in control-plane.md**

In `docs/control-plane.md`, replace this unique heading line:

```markdown
## 6. Submitting a deployment
```

with:

```markdown
### The probe

With S3, `PUT /api/logging/storage` first runs `loki.Probe`: a SigV4-signed `PUT` of an empty
`pstack-probe-<16 hex>` object, then its `DELETE`. `-verify-config` builds no storage client and
Loki writes nothing to S3 before the cutover, so this is the only save-time check of endpoint, TLS,
region, bucket, credentials and delete permission. 10s per request. A refusal is a 400 with the
status and S3's `<Code>` when it is letters only; the body is never echoed. A 3xx is a refusal, not
followed. Loopback and private addresses are allowed: an internal MinIO is the normal case, and the
§4d guard exists for typos.

The probe runs from pstack's networks, not Loki's `logs` network. An endpoint that only
`preview-shared` reaches passes the probe and fails in Loki after the cutover.

## 6. Submitting a deployment
```

Before editing, confirm that `grep -n '^## 6. Submitting a deployment' docs/control-plane.md` prints exactly one line. Also confirm that `## 5g. Loki logging` (slice-1 T8) and T3's period-guard text sit above it.

- [ ] **Step 7: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the file IDs for `packages/pstack/internal/loki/probe.go`, `packages/pstack/internal/loki/probe_test.go` and `docs/control-plane.md`. Leave out `packages/conformance/golden/host/db/*` and `.superpowers/`. Then run:

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-settings -m "feat(loki): the S3 probe, a signed PUT and DELETE before a storage save" <probe.go id> <probe_test.go id> <control-plane.md id>
```

There is no CHANGELOG bullet in this task. The probe cannot be reached until T12, and T12's bullet covers the 400 the probe produces.

---

### Task 6: jobs, events, notify: the loki-apply action and logging.changed

**Files:**
- Modify: `packages/pstack/internal/jobs/jobs.go`: the `Action` const block after `Wake   Action = "wake"`, and the `verified` lines in `emitTerminal` (jobs.go:793-816)
- Modify: `packages/pstack/internal/events/events.go`: `Names`, after `"job.superseded",` (events.go:98)
- Modify: `packages/pstack/internal/notify/notify.go`: `actionWord` after `case "wake":` (:477), and `Summarize` after the `share.created` case (:453-458)
- Modify: `docs/webhook-events.md`: the `job.started` action row (:171), the terminal action row (:200), the `verified` row (:207), and a new `### \`logging.changed\`` section before `### \`stack.slept\`` (:373)
- Modify: `packages/pstack/CHANGELOG.md`: `## Unreleased` → `### Added`
- Test: `packages/pstack/internal/jobs/jobs_test.go` (`TestStateFromOutcomeAndEventPayload`)
- Test: `packages/pstack/internal/events/events_test.go` (`TestNames`)
- Test: `packages/pstack/internal/notify/notify_test.go` (`TestSummarize`)

Edits are anchored on quoted existing text. Line numbers are only a guide: slice 1 doesn't touch these three packages, but T2 and T4 add CHANGELOG bullets before this task.

**Interfaces:**
- Consumes: nothing from slice 1 or T1-T5 (root task). From existing code:
  - `jobs.Action` and `Up` (jobs.go:117-123)
  - `emitTerminal` (jobs.go:793-816)
  - `events.Names` (events.go:33-99)
  - `Summarize`'s local `by` closure (notify.go:342) and `strs` (notify.go:311)
  - `actionWord` (notify.go:466-480)
- Produces:
  - `jobs.LokiApply Action = "loki-apply"`. T9 calls `s.jobs.Start(inspect.ControlProject, jobs.LokiApply, work, scrub)`. It is not in `preempts` (jobs.go:128-132) and gets no `startLifecycle` branch.
  - `emitTerminal` gets `noAssertGone := map[Action]bool{Up: true, LokiApply: true}`, one lookup that sets `verified` to `nil`. A `loki-apply` terminal payload carries `"verified":null` on both the outcome path and the crash path.
  - `events.Names` gets `"logging.changed"` as its last entry. T9 emits it in this key order, which is the table documented in Step 6: `jsonx.O("by", by, "job", jobID, "changed", changed, "storage", storageType, "cutover", cutoverOrNil, "retentionDays", n)`.
  - `notify.actionWord("loki-apply") == "Loki settings"`, so `Summarize(job.started)` reads `Loki settings started on pstack-control.`
  - `notify.Summarize(logging.changed)` reads `Loki settings changed by <by>: <changed, joined ", ">.`
  - Handled elsewhere:
    - The emit site from AGENTS.md's "A new event" checklist is T9.
    - `JobAction` and `ACTION_LABELS` are T13 and T14.
    - The basic UI prints `j.action` raw (ui/index.html:409, 1231) and needs no change.

- [ ] **Step 1: Write the failing tests**

`packages/pstack/internal/jobs/jobs_test.go`, in `TestStateFromOutcomeAndEventPayload`'s `cases`. Replace:

```go
		{Verify, stack.Outcome{OK: true}, OK, "job.succeeded", []any{}, false},
	}
```

with:

```go
		{Verify, stack.Outcome{OK: true}, OK, "job.succeeded", []any{}, false},
		// An apply has no teardown to prove: `verified` is null, as for up.
		// negative control: drop LokiApply from noAssertGone → "case 6: verified false want <nil>".
		{LokiApply, stack.Outcome{OK: true}, OK, "job.succeeded", []any{}, nil},
	}
```

The loop's existing assertions cover the rest of the payload: the prefix `…"action":"loki-apply","state":"ok","startedAt":`, `leakedAxes` `[]`, `unverifiable` 0, and no `error`.

`packages/pstack/internal/events/events_test.go`, in `TestNames` → `t.Run("the EVENTS list, in order, add-only", …)`. Replace:

```go
		// negative control: swap "job.failed" and "job.cancelled" — the order assertion fails.
```

with:

```go
		// negative control: swap "job.failed" and "job.cancelled" — the order assertion fails.
		// negative control: file "logging.changed" beside "routing.changed" in Names — the order assertion fails.
```

Then, in the same `want` string, replace:

```go
config.exported,config.imported,job.superseded"
```

with:

```go
config.exported,config.imported,job.superseded,logging.changed"
```

`packages/pstack/internal/notify/notify_test.go`. First replace:

```go
// negative control: change "Teardown LEAKED" → the job.leaked line differs.
func TestSummarize(t *testing.T) {
```

with:

```go
// negative control: change "Teardown LEAKED" → the job.leaked line differs.
// negative control: delete `case "loki-apply"` in actionWord → got "loki-apply started on pstack-control.".
// negative control: delete `case "logging.changed"` in Summarize → got "logging.changed on this host.".
func TestSummarize(t *testing.T) {
```

Then, at the end of the `cases` map, replace:

```go
Summarize(ev("job.started", `{"stack":"sleepy","action":"wake"}`)),
	}
```

with the lines below. Values start at gofmt's column 96, which is 51 and 40 spaces after the keys. If the padding is off, `gofmt -w internal/notify/notify_test.go` fixes it.

```go
Summarize(ev("job.started", `{"stack":"sleepy","action":"wake"}`)),
		"Loki settings started on pstack-control.":                                                   Summarize(ev("job.started", `{"stack":"pstack-control","action":"loki-apply"}`)),
		"Loki settings changed by alice: retention, storage.":                                        Summarize(ev("logging.changed", `{"by":"alice","job":"j1","changed":["retention","storage"],"storage":"s3","cutover":"2026-10-01","retentionDays":14}`)),
	}
```

The first entry tests `actionWord("loki-apply") == "Loki settings"` through the `job.started` line, the same way the existing `Wake started on sleepy.` entry covers `wake`.

- [ ] **Step 2: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/jobs/ -run TestStateFromOutcomeAndEventPayload
```

Expected: the build fails with `internal/jobs/jobs_test.go:…: undefined: LokiApply`, then `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs [build failed]`.

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/events/ -run TestNames
```

Expected: `--- FAIL: TestNames/the_EVENTS_list,_in_order,_add-only` with `Names = deployment.created,…,config.exported,config.imported,job.superseded`. There is no trailing `,logging.changed`.

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/notify/ -run TestSummarize
```

Expected: `--- FAIL: TestSummarize` with two errors, in either order (the map is unordered):

```
 got "loki-apply started on pstack-control."
want "Loki settings started on pstack-control."
```

```
 got "logging.changed on this host."
want "Loki settings changed by alice: retention, storage."
```

- [ ] **Step 3: Implement**

`packages/pstack/internal/jobs/jobs.go`, the const block. Replace:

```go
	Wake   Action = "wake"
)
```

with the block below. gofmt leaves the first five lines aligned as they are, because the comment line ends the alignment run (checked with `gofmt -d`).

```go
	Wake   Action = "wake"
	// Applies Loki's saved settings to pstack-control (logging slice 2). Not a lifecycle action:
	// no startLifecycle branch, no `:id` route, not in preempts.
	LokiApply Action = "loki-apply"
)
```

`emitTerminal`, first edit. Replace:

```go
	unverifiable := 0
	verified := any(nil)
```

with:

```go
	unverifiable := 0
	// `verified: null` = not applicable: these actions run no assert_gone by design.
	noAssertGone := map[Action]bool{Up: true, LokiApply: true}
	verified := any(false)
```

`emitTerminal`, second edit. Replace:

```go
		if snapshot.Action != Up {
			verified = sawAssert
		}
	} else if snapshot.Action != Up {
		verified = false
	}
```

with:

```go
		verified = sawAssert
	}
	if noAssertGone[snapshot.Action] {
		verified = nil
	}
```

The truth table is unchanged for every existing action:
- `up` is `nil` on both paths.
- Any other action is `sawAssert` with an outcome, and `false` without one.

With a single lookup, the one test case covers the crash path too.

`packages/pstack/internal/events/events.go`. Replace:

```go
	"job.superseded",
}
```

with:

```go
	"job.superseded",
	// Loki's settings changed through the API and Loki answered ready on them (logging slice 2).
	// Appended, like `job.superseded`: the order is the contract. Which sections changed and the
	// storage type — never the endpoint, the bucket, the key id or the secret.
	"logging.changed",
}
```

`packages/pstack/internal/notify/notify.go`, `actionWord`. Replace:

```go
	case "wake":
		return "Wake"
	}
```

with:

```go
	case "wake":
		return "Wake"
	case "loki-apply":
		return "Loki settings"
	}
```

`Summarize`. Replace:

```go
		return "A read-only link to " + stack + " was created" + byStr("by") + " (" + views + ")."
	default:
```

with the code below. The `changed` join is guarded like `stack.failed`: an empty list reads `Loki settings changed by alice.` and not `…: .`.

```go
		return "A read-only link to " + stack + " was created" + byStr("by") + " (" + views + ")."
	case "logging.changed":
		s := "Loki settings changed" + by("by")
		if xs := strs(d["changed"]); len(xs) > 0 {
			s += ": " + strings.Join(xs, ", ")
		}
		return s + "."
	default:
```

- [ ] **Step 4: Run it and watch it pass**

`./internal/webhooks/` is in the run because its error text joins `events.Names`. Its test asserts only a prefix and a suffix (webhooks_test.go:100-104), so it stays green.

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/jobs/ ./internal/events/ ./internal/notify/ ./internal/webhooks/ && go vet ./internal/jobs/ ./internal/events/ ./internal/notify/ && gofmt -l internal/jobs internal/events internal/notify
```

Expected: four `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/{jobs,events,notify,webhooks}` lines, no vet output, and no `gofmt -l` output.

- [ ] **Step 5: Run every negative control, then revert it**

Apply one mutation at a time, run the named test, check the failure, then restore the line.

1. In `jobs.go`, change `map[Action]bool{Up: true, LokiApply: true}` to `map[Action]bool{Up: true}`. Run `go test -race -timeout 120s ./internal/jobs/ -run TestStateFromOutcomeAndEventPayload`. It fails with `case 6: verified false want <nil>`. Restore.
2. In `events.go`, move `"logging.changed",` and its comment to just after `"routing.changed",`. Run `go test -race -timeout 120s ./internal/events/ -run TestNames`. It fails with `Names = …,routing.changed,logging.changed,healthcheck.started,…`. Restore.
3. In `notify.go`, delete the two `case "loki-apply":` lines. Run `go test -race -timeout 120s ./internal/notify/ -run TestSummarize`. It fails with `got "loki-apply started on pstack-control."`. Restore.
4. In `notify.go`, delete the `case "logging.changed":` block. The same test fails with `got "logging.changed on this host."`. Restore.

Then re-run the Step 4 command. It is green.

- [ ] **Step 6: Document it in `docs/webhook-events.md`**

(a) The `job.started` table (:171). Replace:

```
| `action` | `"up"` \| `"down"` \| `"verify"` \| `"sleep"` \| `"wake"` | `sleep` takes the compose project down and keeps its volumes and axes; `wake` is `up` recorded under its own name (0.26.0). |
```

with:

```
| `action` | `"up"` \| `"down"` \| `"verify"` \| `"sleep"` \| `"wake"` \| `"loki-apply"` | `sleep` takes the compose project down and keeps its volumes and axes; `wake` is `up` recorded under its own name (0.26.0). `loki-apply` applies Loki's saved settings, on `pstack-control`. |
```

(b) The terminal-events table (:200). Do (a) first; this old text then appears exactly once. Replace:

```
| `action` | `"up"` \| `"down"` \| `"verify"` | |
```

with:

```
| `action` | `"up"` \| `"down"` \| `"verify"` \| `"loki-apply"` | |
```

(c) The `verified` row (:207). Replace:

```
`null` = not applicable (`up` runs no `assert_gone` by design).
```

with:

```
`null` = not applicable (`up` and `loki-apply` run no `assert_gone` by design).
```

(d) Add the catalogue entry after `routing.changed`. Replace:

```
| `action` | `"created"` \| `"replaced"` \| `"deleted"` | |

### `stack.slept`
```

with:

```
| `action` | `"created"` \| `"replaced"` \| `"deleted"` | |

### `logging.changed`

Fires when a Loki settings save (`PUT /api/logging`, `PUT /api/logging/storage`) is applied and Loki
answered ready on it. Not for a rollback, a resume or a boot apply. The apply itself is the `job.*`
family: `action: "loki-apply"`, `stack: "pstack-control"`.

| `data.` field | Type | Meaning |
|---|---|---|
| `by` | string | Who saved (the newest save the job applied): a username, or `root (PSTACK_TOKEN)`. |
| `job` | string | The apply job. |
| `changed` | string[] | Any of `chunks`, `retention`, `storage`, `credentials`. Non-nil. |
| `storage` | `"filesystem"` \| `"s3"` | After the change. |
| `cutover` | string \| null | The S3 period's `from`, `YYYY-MM-DD`; `null` on filesystem. |
| `retentionDays` | number | After the change. |

Never the endpoint, the bucket, the key id or the secret.

### `stack.slept`
```

To check, run `grep -n 'loki-apply\|logging.changed' /Volumes/S1/code/preview-stacks/docs/webhook-events.md`. It lists :171, :200, :207 and the new section.

- [ ] **Step 7: Add the CHANGELOG bullet**

In `packages/pstack/CHANGELOG.md`, add the bullet below as the last item of the `### Added` list under `## Unreleased`.
- If `## Unreleased` is absent (slice-1 T14 creates it), insert `## Unreleased`, a blank line, `### Added` and a blank line between `# Changelog` and the first `## 0.` version heading.
- If `## Unreleased` exists without `### Added`, put `### Added` directly under it.
- `### Changed` alone is not a unique Edit anchor, because every version has one. Read the section first and anchor on the final line of the last `### Added` bullet.

```
- **`logging.changed`**, a webhook event: a Loki settings save was applied and Loki answered
  ready. `data.changed` names the sections (`chunks`, `retention`, `storage`, `credentials`), never
  the endpoint, bucket, key id or secret. The apply runs as a `loki-apply` job on `pstack-control`,
  and its terminal event sends `verified: null`.
```

- [ ] **Step 8: Commit**

```
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these eight files. Leave out anything under `packages/conformance/golden/host/db/` or `.superpowers/`, and any other agent's changes. If `CHANGELOG.md` also holds another task's uncommitted hunk, use `but status -fv` and take only this bullet's hunk.

```
but commit -b claude/loki-logging-settings -m "feat(jobs): loki-apply action and logging.changed event" <jobs.go> <jobs_test.go> <events.go> <events_test.go> <notify.go> <notify_test.go> <webhook-events.md> <CHANGELOG.md>
```

No attribution footer. Don't push.

---

### Task 7: initctl: config.yaml is kept; pstack mounts ./loki; Loki reads AWS_SHARED_CREDENTIALS_FILE

**Files:**
- Modify: `packages/pstack/internal/initctl/init.go`. Anchor edits by quoted text. The line numbers are from the tree at `066ebd0`: the config write at :457-463, `LokiService`'s environment at :811-813, and the `LokiWiring` comment and slice at :877-906.
- Modify: `packages/pstack/templates/control/README.md` (the `loki/config.yaml` row of the tree, :26)
- Modify: `docs/usage.md`. Two edits: the `init` table row `| 3. Config |` (:1318), and the `### Turn Loki logging on or off: \`pstack logging\`` section that slice-1 T6 writes (plan:3339-3371).
- Modify: `packages/pstack/CHANGELOG.md` (`## Unreleased` → `### Changed`)
- Modify (regenerated, never hand-edited): `packages/conformance/golden/render/control/http01-basic-compose-loki/docker-compose.yml`, `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml`, `packages/conformance/golden/cli/init-dry-http01-basic-compose-loki.json`, `packages/conformance/golden/cli/init-dry-dns01-advanced-swarm-loki.json`
- Test: `packages/pstack/internal/initctl/init_test.go`

**Interfaces:**
- Consumes (slice 1, already built):
  - `initctl.LokiService(logging Logging, challenge Challenge, password string) string` (init.go:800)
  - `initctl.LokiWiring(template string, logging Logging) (string, error)` with its ordered `[]struct{what, anchor, with string; haystack func(string) string}` (init.go:888-906)
  - `identity` (init.go:875)
  - `write(out io.Writer, path, body string, mode os.FileMode, dryRun bool) error` (init.go:967-976)
  - `pstack.LokiConfig` (assets.go)
  - Test helpers in `init_test.go`: `okRunner`, `read`, `substituted`, `render`, `cells`
  - The slice-1 T13 golden rows `init-dry-<cell>-loki` and `init-<cell>-loki` (render dir `control/<cell>-loki`), for cells `http01-basic-compose` and `dns01-advanced-swarm`
- Produces (T11, T12 and T15 rely on these; slice 3 extends them):
  - **Create-if-absent config write.** Under `--logging loki`, `Init` writes `<data>/control/loki/config.yaml` only when `os.Stat` fails. If the file exists, a dry run prints `  [dry-run] keep <path>\n` and a real run prints nothing. A kept file is neither rewritten nor re-chmodded.
  - **Env line.** `LokiService` renders `      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials` directly after `      GOMEMLIMIT: 1600MiB`. The signature is unchanged.
  - **Anchor 4.** `LokiWiring` gains `{"pstack docker-config mount", "      - ./docker:/docker-config\n", "      - ./docker:/docker-config\n      - ./loki:/etc/loki\n", identity}` as the LAST entry. The failure message keeps its format, `the control template has no pstack docker-config mount ("      - ./docker:/docker-config\n") for --logging loki to extend`. The slice stays append-only.
  - **Container path.** pstack sees Loki's directory at `/etc/loki`, read-write. T8's `loki.Dir` resolves it there.

- [ ] **Step 1: Write the failing tests**

  All edits are in `/Volumes/S1/code/preview-stacks/packages/pstack/internal/initctl/init_test.go`. No new imports: `bytes`, `fmt`, `os`, `filepath`, `strings`, `pstack`, `exec`, `initctl`, `omap`, `spec` and `yamlx` are all imported already.

  **1a. `TestInitLoki`: the kept file.** Insert this subtest immediately after the subtest `t.Run("control/loki/config.yaml is the embedded config at 0644", …)`, which ends with `t.Errorf("mode %o, want 644", st.Mode().Perm())` / `}` / `})`:

  ```go
  	// After a slice-2 save the file is the API's: an S3 schema period lives only there. init re-runs on
  	// every upgrade and `pstack logging loki`, and a rewrite would drop that period.
  	t.Run("an existing loki config.yaml is kept, and a dry run says keep", func(t *testing.T) {
  		// negative control: drop the os.Stat guard in Init so write always runs — the file is overwritten and the dry run prints `write`.
  		t.Setenv("PSTACK_LOKI_PASSWORD", "")
  		dir := t.TempDir()
  		p := filepath.Join(dir, "control", "loki", "config.yaml")
  		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
  			t.Fatal(err)
  		}
  		saved := strings.Replace(pstack.LokiConfig, "  retention_period: 168h", "  retention_period: 72h", 1)
  		if saved == pstack.LokiConfig {
  			t.Fatal("templates/control/loki/config.yaml has no `  retention_period: 168h` line")
  		}
  		if err := os.WriteFile(p, []byte(saved), 0o644); err != nil {
  			t.Fatal(err)
  		}
  		opts := func(dryRun bool, r exec.Runner, out *bytes.Buffer) initctl.Options {
  			return initctl.Options{
  				DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
  				Orchestrator: spec.Compose, Logging: initctl.Loki, DryRun: dryRun, Runner: r, Out: out,
  			}
  		}

  		var out bytes.Buffer
  		if err := initctl.Init(opts(false, okRunner("inactive", ""), &out)); err != nil {
  			t.Fatal(err)
  		}
  		if got := read(t, p); got != saved {
  			t.Errorf("init rewrote config.yaml:\n%s", got)
  		}
  		// "keep "+p, not a bare "keep": t.TempDir() folds this subtest's name into every printed path.
  		if strings.Contains(out.String(), "keep "+p) {
  			t.Errorf("a real run printed a keep line:\n%s", out.String())
  		}

  		// The real runner, not a Fake: only it prints `[dry-run]` lines.
  		var dry bytes.Buffer
  		if err := initctl.Init(opts(true, exec.New(exec.Options{DryRun: true, Out: &dry}), &dry)); err != nil {
  			t.Fatal(err)
  		}
  		if !strings.Contains(dry.String(), "  [dry-run] keep "+p+"\n") {
  			t.Errorf("no keep line:\n%s", dry.String())
  		}
  		if strings.Contains(dry.String(), "  [dry-run] write "+p+" (") {
  			t.Errorf("the dry run would write config.yaml:\n%s", dry.String())
  		}
  	})
  ```

  **1b. `TestLokiService`: the env line.** Insert this subtest immediately before `t.Run("logging off renders nothing", func(t *testing.T) {`:

  ```go
  	t.Run("Loki reads S3 credentials from its mount, the line after GOMEMLIMIT", func(t *testing.T) {
  		// negative control: delete the AWS_SHARED_CREDENTIALS_FILE line from LokiService — the contiguous substring is missing.
  		// A container's env is fixed at creation and `docker restart` keeps it, so init sets this, not the API.
  		const want = "      GOMEMLIMIT: 1600MiB\n      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials\n"
  		for _, c := range []initctl.Challenge{initctl.HTTP01, initctl.DNS01} {
  			if block := initctl.LokiService(initctl.Loki, c, pw); strings.Count(block, want) != 1 {
  				t.Errorf("%s: no %q:\n%s", c, want, block)
  			}
  		}
  	})
  ```

  **1c. `TestLokiWiring`: anchor 4 lands once.** In the subtest `"each edit lands once: Traefik's networks, the top-level volumes and networks"`, replace:

  ```go
  	t.Run("each edit lands once: Traefik's networks, the top-level volumes and networks", func(t *testing.T) {
  ```
  with:
  ```go
  	t.Run("each edit lands once: Traefik's networks, the top-level volumes and networks, pstack's loki mount", func(t *testing.T) {
  ```
  and replace:
  ```go
  				"  preview-shared:\n    external: true\n  logs: {}\n",
  			} {
  ```
  with:
  ```go
  				"  preview-shared:\n    external: true\n  logs: {}\n",
  				"      - ./docker:/docker-config\n      - ./loki:/etc/loki\n",
  			} {
  ```

  **1d. `TestLokiWiring`: the mount is read-write, at Loki's path, in pstack's block.** Insert this subtest immediately before `t.Run("logging off returns the template byte-identical", func(t *testing.T) {`. That existing subtest already proves logging none returns the template unchanged, so it is not duplicated here.

  ```go
  	t.Run("pstack mounts ./loki read-write, at Loki's own container path", func(t *testing.T) {
  		// negative control: make anchor 4's replacement `      - ./docker:/docker-config\n      - ./loki:/etc/loki:ro\n` in LokiWiring — pstack's mounts read [./loki:/etc/loki:ro].
  		got, err := initctl.LokiWiring(substituted(initctl.DNS01, initctl.Basic, initctl.LokiService(initctl.Loki, initctl.DNS01, pw)), initctl.Loki)
  		if err != nil {
  			t.Fatal(err)
  		}
  		v, err := yamlx.ParseString(got)
  		if err != nil {
  			t.Fatal(err)
  		}
  		svcs := v.(*omap.Map).GetMap("services")
  		lokiMounts := func(svc string) (out []string) {
  			for _, m := range svcs.GetMap(svc).GetSlice("volumes") {
  				if s, _ := m.(string); strings.HasPrefix(s, "./loki:") {
  					out = append(out, s)
  				}
  			}
  			return out
  		}
  		// Same container path: a filename means the same file to both (the traefik-dynamic precedent).
  		if m := fmt.Sprint(lokiMounts("pstack")); m != "[./loki:/etc/loki]" {
  			t.Errorf("pstack's loki mounts %s", m)
  		}
  		if m := fmt.Sprint(lokiMounts("loki")); m != "[./loki:/etc/loki:ro]" {
  			t.Errorf("loki's own mounts %s", m)
  		}
  	})
  ```

  **1e. `TestLokiWiring`: a template without anchor 4 fails by name.** Replace:
  ```go
  		{"preview-shared network", "  preview-shared:\n    external: true\n"},
  	} {
  		t.Run("a template without the "+c.what+" fails by name", func(t *testing.T) {
  ```
  with:
  ```go
  		{"preview-shared network", "  preview-shared:\n    external: true\n"},
  		{"pstack docker-config mount", "      - ./docker:/docker-config\n"},
  	} {
  		t.Run("a template without the "+c.what+" fails by name", func(t *testing.T) {
  ```
  The `ReplaceAll` in that loop is safe for this anchor because its bytes occur once in the template (docker-compose.yml:149). pstack's `      DOCKER_CONFIG: /docker-config` env line (:153) is a different string. That is also why this entry's haystack is `identity`: R16's `traefikBlock` was needed only because the networks line has a twin under the advanced UI.

- [ ] **Step 2: Run them and watch them fail**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/initctl/ -run 'TestInitLoki|TestLokiService|TestLokiWiring'
  ```
  Expected: `FAIL`. It compiles, since no new identifier is used. Exactly these subtests fail:
  - `TestInitLoki/an_existing_loki_config.yaml_is_kept,_and_a_dry_run_says_keep`: `init rewrote config.yaml:` (the file holds `168h`), then `no keep line:`, then `the dry run would write config.yaml:`.
  - `TestLokiService/Loki_reads_S3_credentials_from_its_mount,_the_line_after_GOMEMLIMIT`: `http01: no "      GOMEMLIMIT: 1600MiB\n      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials\n"`, then the same for `dns01`.
  - `TestLokiWiring/each_edit_lands_once:_…_pstack's_loki_mount`: `http01: "      - ./docker:/docker-config\n      - ./loki:/etc/loki\n" appears 0 times`, then the same for `dns01`.
  - `TestLokiWiring/pstack_mounts_./loki_read-write,_at_Loki's_own_container_path`: `pstack's loki mounts []`.
  - `TestLokiWiring/a_template_without_the_pstack_docker-config_mount_fails_by_name`: `got <nil>`.

  Every other subtest passes.

- [ ] **Step 3: Implement**

  All edits are in `/Volumes/S1/code/preview-stacks/packages/pstack/internal/initctl/init.go`. `fmt` and `os` are already imported.

  **3a. Create-if-absent (init.go:457-463).** Replace:
  ```go
  	// 0644, not 0600 like its neighbours: the Loki image runs as uid 10001 and must read it, and it holds
  	// no credential. The password stays in .env and reaches Traefik only as a hash.
  	if logging == Loki {
  		if err := write(out, filepath.Join(controlDir, "loki", "config.yaml"), pstack.LokiConfig, 0o644, dryRun); err != nil {
  			return err
  		}
  	}
  ```
  with:
  ```go
  	// 0644, not 0600 like its neighbours: the Loki image runs as uid 10001 and must read it, and it holds
  	// no credential. The password stays in .env and reaches Traefik only as a hash.
  	//
  	// Written only when absent. Once it exists it is the API's file (PUT /api/logging renders it): a
  	// rewrite here would drop a saved S3 schema period and make every log in S3 unreadable. So a kept
  	// file also skips write's re-chmod; the API writes it 0644.
  	if logging == Loki {
  		path := filepath.Join(controlDir, "loki", "config.yaml")
  		if _, err := os.Stat(path); err == nil {
  			if dryRun {
  				fmt.Fprintf(out, "  [dry-run] keep %s\n", path)
  			}
  		} else if err := write(out, path, pstack.LokiConfig, 0o644, dryRun); err != nil {
  			return err
  		}
  	}
  ```

  **3b. The env line (inside `LokiService`, init.go:811-813).** Replace:
  ```go
  		"    environment:",
  		"      GOMEMLIMIT: 1600MiB",
  		"    volumes:",
  ```
  with:
  ```go
  		"    environment:",
  		"      GOMEMLIMIT: 1600MiB",
  		// Read only with S3 storage, from the file the API writes. The env is fixed when the container is
  		// created and survives `docker restart`, so init sets it, not the API's apply.
  		"      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials",
  		"    volumes:",
  ```
  The rendered line carries no YAML comment, so the render goldens gain exactly this line and anchor 4's line.

  **3c. `LokiWiring`'s doc comment (init.go:877-880).** Replace:
  ```go
  // LokiWiring is the rest of Loki's plumbing: Traefik joins the `logs` network, and the file declares
  // the `loki` volume and that network. Literal edits of lines the template already has, not new
  // markers — every marker leaves a line behind when it renders "off", so a new one would change the
  // compose file of every existing host.
  ```
  with:
  ```go
  // LokiWiring is the rest of Loki's plumbing: Traefik joins the `logs` network, the file declares
  // the `loki` volume and that network, and pstack mounts ./loki read-write at Loki's own path, where
  // the API writes Loki's config. Literal edits of lines the template already has, not new markers —
  // every marker leaves a line behind when it renders "off", so a new one would change the compose
  // file of every existing host.
  ```
  Also replace:
  ```go
  // removed, passing the check and then getting edited in Traefik's place. Ordered, never a map (rule
  // 5): the first missing anchor is the one named.
  ```
  with:
  ```go
  // removed, passing the check and then getting edited in Traefik's place. Ordered, never a map (rule
  // 5): the first missing anchor is the one named. New anchors go last.
  ```

  **3d. Anchor 4 (the slice, init.go:897-898).** Replace:
  ```go
  		{"preview-shared network", "  preview-shared:\n    external: true\n", "  preview-shared:\n    external: true\n  logs: {}\n", identity},
  	} {
  ```
  with:
  ```go
  		{"preview-shared network", "  preview-shared:\n    external: true\n", "  preview-shared:\n    external: true\n  logs: {}\n", identity},
  		// Unique in the template: pstack's `DOCKER_CONFIG: /docker-config` env line is a different string.
  		{"pstack docker-config mount", "      - ./docker:/docker-config\n", "      - ./docker:/docker-config\n      - ./loki:/etc/loki\n", identity},
  	} {
  ```

- [ ] **Step 4: Run them and watch them pass**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/initctl/ -run 'TestInitLoki|TestLokiService|TestLokiWiring'
  ```
  Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl`.

  Then run the whole package, plus the two packages that call the real `Init` with loki: `upgrade` (`lokiControl`, TestLoggingSwitch) and `cli`. Also check `TestInitGoldens`, whose 8 logging-off cells must stay byte-identical.
  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/initctl/ ./internal/upgrade/ ./internal/cli/ && go vet ./internal/initctl/ && gofmt -l internal/initctl/
  ```
  Expected: three `ok` lines. `go vet` and `gofmt -l` print nothing. `TestInitGoldens` passes, and so does upgrade's `lokiSvc` detection: `(?m)^\s{2}loki:` (upgrade.go:100) does not match `      - ./loki:/etc/loki`.

- [ ] **Step 5: Run the negative controls**

  Make each mutation by hand, run the command, confirm the failure, then revert it by hand. All runs use `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/initctl/ -run <Name>`.
  1. **3a, drop the guard.** Replace the `if _, err := os.Stat(path); err == nil { … } else if err := write(…)` chain with a plain `if err := write(out, path, pstack.LokiConfig, 0o644, dryRun); err != nil { return err }`. Run `-run TestInitLoki`. Expect `init rewrote config.yaml:` and `no keep line:`.
  2. **3a, drop only the `fmt.Fprintf` keep line.** Run `-run TestInitLoki`. Expect `no keep line:` only.
  3. **3b, delete the `AWS_SHARED_CREDENTIALS_FILE` line.** Run `-run TestLokiService`. Expect `http01: no "…"` and `dns01: no "…"`.
  4. **3d, render the mount as `:ro`** (anchor 4's `with` ends `      - ./loki:/etc/loki:ro\n`). Run `-run TestLokiWiring`. Expect `pstack's loki mounts [./loki:/etc/loki:ro]` and `appears 0 times`.
  5. **Skip the `strings.Contains` check in `LokiWiring`.** Run `-run TestLokiWiring`. Expect `a_template_without_the_pstack_docker-config_mount_fails_by_name` to fail with `got <nil>`, along with the three existing anchors' subtests.

  After reverting, the Step 4 command is green again.

- [ ] **Step 6: Regenerate the goldens**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts
  ```
  Expected: `pstack <version>`, one `  <name>: exit <code>` line per row, and a final `wrote N goldens`. N is the count slice-1 T13 left (94 per R3). No row changes its exit code.

  ```sh
  cd /Volumes/S1/code/preview-stacks && but diff
  ```
  Expected: exactly these four files change. The untracked `packages/conformance/golden/host/db/pstack.db-{shm,wal}` stay out of the commit.
  - `golden/render/control/http01-basic-compose-loki/docker-compose.yml` and `golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml` each gain two lines and nothing else. One is `+      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials`, after `      GOMEMLIMIT: 1600MiB`. The other is `+      - ./loki:/etc/loki`, after `      - ./docker:/docker-config`.
  - `golden/cli/init-dry-http01-basic-compose-loki.json` and `golden/cli/init-dry-dns01-advanced-swarm-loki.json` each change one line: `  [dry-run] write <DATA>/control/docker-compose.yml (N bytes, mode 644)`, where N rises by exactly **85**. That is 25 for the mount line and 60 for the env line, newlines included. All of it is ASCII, so `js.Len`'s UTF-16 count equals bytes. Their `config.yaml` write line is unchanged: those rows run on `freshData` (cli-goldens.test.ts:45-48), so the keep path is never reached.

  These goldens do **not** change, and any diff in them is a bug to fix before committing:
  - `init-<cell>-loki`: a real run prints no write lines.
  - `upgrade-plan-<cell>-loki`: it prints steps only.
  - `logging-off-dry-<cell>-loki` and `logging-loki-dry-dns01-advanced-swarm`: SwitchLogging runs `pstack init …` through the runner (upgrade.go:410-413), and a dry-run runner prints the label and returns `Skipped` without running it (exec.go:113-121). No init write or keep line is echoed.
  - Both `loki/config.yaml` render goldens: a fresh host still writes `pstack.LokiConfig`.
  - The 8 logging-off cells and their transcripts.
  - The help goldens: no new flag.

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
  ```
  Expected: every row passes. The count equals `expected-pass.json`'s `"test/cli-goldens.test.ts"` entry, unchanged.

- [ ] **Step 7: Docs**

  **7a. `/Volumes/S1/code/preview-stacks/packages/pstack/templates/control/README.md`.** Replace:
  ```
      ├── loki/config.yaml 0644    Loki's fixed config (only with --logging loki)
  ```
  with:
  ```
      ├── loki/                    only with --logging loki; Loki mounts it read-only, pstack read-write
      │   ├── config.yaml    0644  written once, then pstack's
      │   └── s3-credentials 0600  uid 10001; S3 storage only
  ```

  **7b. `/Volumes/S1/code/preview-stacks/docs/usage.md`, the `init` table (row `| 3. Config |`).** Replace:
  ```
  With `--logging loki`: the `loki` service, `control/loki/config.yaml` (`0644` — Loki runs as uid 10001; no credential) and
  ```
  with:
  ```
  With `--logging loki`: the `loki` service, `control/loki/config.yaml` (`0644` — Loki runs as uid 10001; no credential; written only when absent), `./loki` mounted read-write into pstack at `/etc/loki`, and
  ```

  **7c. `docs/usage.md`, the `pstack logging` section (slice-1 T6).** Verify the anchor against the tree at build time. Replace:
  ```
  `pstack upgrade` keeps whichever mode the host is in, and the push password with it. Workers get the
  plugin from the join material; see [Swarm mode](#swarm-mode).
  ```
  with:
  ```
  `pstack upgrade` keeps whichever mode the host is in, and the push password with it. Workers get the
  plugin from the join material; see [Swarm mode](#swarm-mode).

  `control/loki/config.yaml` is kept on `init`, `upgrade` and `logging loki`. pstack mounts
  `control/loki` in both modes, so `pstack logging` never recreates the pstack container.
  ```

  **7d. `/Volumes/S1/code/preview-stacks/packages/pstack/CHANGELOG.md`.** If `## Unreleased` has a `### Changed` subsection (slice-1 T14), append the bullet below as that subsection's last bullet, after the `**Goldens.**` bullet. Otherwise insert `## Unreleased`, a blank line, `### Changed` and a blank line before it, between `# Changelog` and the newest release heading (`## 0.39.1 — 2026-09-14` today).
  ```markdown
  - **`init` keeps `control/loki/config.yaml`.** It writes the file only when absent; a dry run prints
    `[dry-run] keep <path>`. pstack mounts `control/loki` read-write at `/etc/loki` in every mode,
    and Loki gets `AWS_SHARED_CREDENTIALS_FILE=/etc/loki/s3-credentials`. The next `pstack upgrade`
    recreates pstack once, and Loki where it runs; `pstack logging loki|off` never recreates pstack. Regenerated with
    `bun gen/goldens.ts`: `render/control/{http01-basic-compose-loki,dns01-advanced-swarm-loki}/docker-compose.yml`
    and `cli/init-dry-{http01-basic-compose,dns01-advanced-swarm}-loki.json` (compose write +85 bytes).
  ```

- [ ] **Step 8: Commit**

  ```sh
  cd /Volumes/S1/code/preview-stacks && but status -fv
  ```
  Pick the IDs for exactly these 9 files:
  - `packages/pstack/internal/initctl/init.go` and `init_test.go`
  - `packages/pstack/templates/control/README.md`
  - `docs/usage.md` (only this task's hunks, if another task's edit is still uncommitted there)
  - `packages/pstack/CHANGELOG.md` (only this task's hunk)
  - the two `golden/render/control/*-loki/docker-compose.yml`
  - the two `golden/cli/init-dry-*-loki.json`

  Exclude `packages/conformance/golden/host/db/*`, `.superpowers/` and `packages/pstack/bin/pstack`.
  ```sh
  but commit -b claude/loki-logging-settings -m "feat(initctl): keep loki/config.yaml; pstack mounts ./loki; Loki reads AWS_SHARED_CREDENTIALS_FILE" <ids>
  ```

---

### Task 8: api: Loki options, tuning knobs, the directory, fail()

**Files:**
- Create: `packages/pstack/internal/api/loki_apply.go` (only `lokiEntry` and `lokiLead`. T9 extends this file.)
- Modify: `packages/pstack/internal/api/server.go` (anchor edits by quoted existing text. Line numbers are advisory because slice 1 is moving them.)
- Modify: `packages/pstack/internal/api/http.go`
- Modify: `packages/pstack/internal/cli/serve.go`
- Modify: `docs/usage.md`
- Modify: `packages/pstack/CHANGELOG.md`
- Test: `packages/pstack/internal/api/http_test.go`

**Interfaces:**
- Consumes:
  - T2: `func loki.Dir(dataDir string) string`, `func loki.Lead(readyTimeout time.Duration) time.Duration` (= d + 10m), `type loki.Error struct{ Msg string }` with `Error()` returning `Msg` (the routing.go:56-64 pattern), `func loki.IsError(err error) bool`, `var loki.ErrOneWay` (a plain sentinel, not a `*Error`).
  - T4: `type loki.ChunksPatch`, `type loki.StoragePatch`.
  - Existing code: `exec.New(exec.Options{Level, BaseEnv, Ctx}) exec.Runner` (exec.go:65-95), `Runner.Context() context.Context` (exec.go:52), `num()` inside `TuningFromEnv` (http.go:61-71), `(*Server).fail` (server.go:935-950).
- Produces:
  - `api.Options`: `LokiDir string; LokiReadyTimeoutMs int64; LokiUID int`. `New` writes the resolved values back into `s.opts`: `LokiDir` "" → `<DataDir>/control/loki`, `LokiReadyTimeoutMs` ≤0 → `300000`, `LokiUID` ≤0 → `10001`.
  - `api.Tuning`: `LokiReadyTimeoutMs float64` (`PSTACK_LOKI_READY_TIMEOUT_MS`) and `LokiUID float64` (`PSTACK_LOKI_UID`). Both go through `num()`, so 0, negative, non-numeric and empty all read as 0.
  - `Server` fields: `lokiMu sync.Mutex; lokiGen uint64; lokiChunks, lokiStorage *lokiEntry; lokiRunner func(context.Context) exec.Runner; lokiPoll time.Duration; lokiNow func() time.Time`. `New` sets `lokiRunner` to a Quiet runner on the given ctx and New's env, `lokiPoll = 2*time.Second` and `lokiNow = time.Now`.
  - `type lokiEntry struct { gen uint64; by string; chunks *loki.ChunksPatch; storage *loki.StoragePatch }` in loki_apply.go.
  - `func (s *Server) lokiLead() time.Duration` in loki_apply.go.
  - `fail()`: `loki.IsError(err)` joins the 400 list. The sentinels stay 500 if one ever reaches `fail()`.
  - serve.go: `LokiDir: loki.Dir(dataDir)`, `LokiReadyTimeoutMs: int64(tuning.LokiReadyTimeoutMs)`, `LokiUID: int(tuning.LokiUID)`.

- [ ] **Step 1: Write the failing tests**

  In `packages/pstack/internal/api/http_test.go`, add the loki import to the import block. Replace:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
  ```

  with:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
  ```

  Then append at the end of the file:

  ```go
  // ── Loki settings: the knobs, the options, fail() (logging slice 2) ─────────────────────────────

  func TestTuningReadsTheLokiKnobs(t *testing.T) {
  	// negative control: read "PSTACK_LOKI_UID" for LokiReadyTimeoutMs in TuningFromEnv — the first
  	// assertion reads 0. (Also run: drop `LokiUID: num("PSTACK_LOKI_UID")` — the second reads 0.)
  	read := func(k, v string) Tuning {
  		return TuningFromEnv(func(key string) (string, bool) {
  			if key == k {
  				return v, true
  			}
  			return "", false
  		})
  	}
  	if tu := read("PSTACK_LOKI_READY_TIMEOUT_MS", "4000"); tu.LokiReadyTimeoutMs != 4000 || tu.LokiUID != 0 {
  		t.Fatalf("PSTACK_LOKI_READY_TIMEOUT_MS=4000 read as %+v", tu)
  	}
  	if tu := read("PSTACK_LOKI_UID", "501"); tu.LokiUID != 501 || tu.LokiReadyTimeoutMs != 0 {
  		t.Fatalf("PSTACK_LOKI_UID=501 read as %+v", tu)
  	}
  	// 0 is unset, so PSTACK_LOKI_UID=0 is 10001: a root pstack chowns to 10001 either way.
  	for _, k := range []string{"PSTACK_LOKI_READY_TIMEOUT_MS", "PSTACK_LOKI_UID"} {
  		for _, bad := range []string{"0", "-1", "nope", ""} {
  			if tu := read(k, bad); tu.LokiReadyTimeoutMs != 0 || tu.LokiUID != 0 {
  				t.Fatalf("%s=%q must read as unset, got %+v", k, bad, tu)
  			}
  		}
  	}
  }

  func TestNewResolvesTheLokiOptions(t *testing.T) {
  	// negative control: delete the three Loki default blocks from New — the first subtest fails.
  	t.Run("zero values take the defaults", func(t *testing.T) {
  		// negative control: delete `o.LokiUID = 10001` from New — LokiUID reads 0, and a root pstack
  		// would chown s3-credentials to root. (Also run: drop `s.lokiPoll = 2 * time.Second`.)
  		dir := t.TempDir()
  		s, err := New(Options{DataDir: dir, Bus: events.New(), Log: func(string) {}})
  		if err != nil {
  			t.Fatal(err)
  		}
  		defer s.Stop()
  		if got, want := s.opts.LokiDir, filepath.Join(dir, "control", "loki"); got != want {
  			t.Errorf("LokiDir = %q, want %q", got, want)
  		}
  		if s.opts.LokiReadyTimeoutMs != 300_000 || s.opts.LokiUID != 10001 {
  			t.Errorf("LokiReadyTimeoutMs %d, LokiUID %d; want 300000, 10001", s.opts.LokiReadyTimeoutMs, s.opts.LokiUID)
  		}
  		if s.lokiPoll != 2*time.Second || s.lokiNow == nil || s.lokiRunner == nil {
  			t.Errorf("seams: poll %v, now set %v, runner set %v", s.lokiPoll, s.lokiNow != nil, s.lokiRunner != nil)
  		}
  		if got := s.lokiLead(); got != 15*time.Minute {
  			t.Errorf("lokiLead = %v, want 15m", got)
  		}
  	})
  	t.Run("set values survive, and the runner takes the ctx it is given", func(t *testing.T) {
  		// negative control: build lokiRunner's runner with `Ctx: s.ctx` instead of its argument — the
  		// post-swap runner could not outlive a job's cancel. (Also run: make lokiLead read
  		// ReadinessTimeoutMs — 10m9s; set `o.LokiUID = 10001` unconditionally — 501 is lost.)
  		lokiDir := t.TempDir()
  		s, err := New(Options{DataDir: t.TempDir(), Bus: events.New(), Log: func(string) {},
  			LokiDir: lokiDir, LokiReadyTimeoutMs: 4000, LokiUID: 501, ReadinessTimeoutMs: 9000})
  		if err != nil {
  			t.Fatal(err)
  		}
  		defer s.Stop()
  		if s.opts.LokiDir != lokiDir || s.opts.LokiReadyTimeoutMs != 4000 || s.opts.LokiUID != 501 {
  			t.Errorf("options overridden: %q %d %d", s.opts.LokiDir, s.opts.LokiReadyTimeoutMs, s.opts.LokiUID)
  		}
  		if got := s.lokiLead(); got != 10*time.Minute+4*time.Second {
  			t.Errorf("lokiLead = %v, want 10m4s", got)
  		}
  		ctx, cancel := context.WithCancel(context.Background())
  		defer cancel()
  		if s.lokiRunner(ctx).Context() != ctx {
  			t.Error("lokiRunner must run on the ctx it is given, not the server's")
  		}
  	})
  }

  func TestFailMapsALokiErrorTo400(t *testing.T) {
  	// negative control: remove loki.IsError(err) from fail()'s list — the first subtest reads 500.
  	t.Run("a *loki.Error is the caller's: 400 with its text", func(t *testing.T) {
  		// negative control: remove loki.IsError(err) from fail()'s list — 500.
  		w := httptest.NewRecorder()
  		(&Server{}).fail(w, &loki.Error{Msg: "chunks.maxAgeMinutes must be 30–180"})
  		if w.Code != 400 || !strings.Contains(w.Body.String(), "chunks.maxAgeMinutes must be 30–180") {
  			t.Fatalf("%d %s", w.Code, w.Body.String())
  		}
  	})
  	t.Run("a sentinel that escapes a handler is a 500, never a mislabelled 400", func(t *testing.T) {
  		// negative control: add `errors.Is(err, loki.ErrOneWay)` to fail()'s 400 list — 400.
  		w := httptest.NewRecorder()
  		(&Server{}).fail(w, loki.ErrOneWay)
  		if w.Code != 500 {
  			t.Fatalf("%d %s", w.Code, w.Body.String())
  		}
  	})
  }
  ```

  (`fail` reads nothing from `s`, so a zero `Server` is enough. jsonx sets `SetEscapeHTML(false)` (jsonx.go:38), so the en dash reaches the body as written.)

- [ ] **Step 2: Run them and watch them fail**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestTuningReadsTheLokiKnobs|TestNewResolvesTheLokiOptions|TestFailMapsALokiErrorTo400'
  ```

  Expected: `FAIL ... [build failed]`, with errors including `tu.LokiReadyTimeoutMs undefined (type Tuning has no field or method LokiReadyTimeoutMs)`, `unknown field LokiDir in struct literal of type Options`, `s.lokiPoll undefined` and `s.lokiLead undefined`.

- [ ] **Step 3: Implement**

  **3a. `packages/pstack/internal/api/http.go`: the Tuning fields.** Replace:

  ```go
  	MaxJobs float64
  }
  ```

  with:

  ```go
  	MaxJobs float64
  	// PSTACK_LOKI_READY_TIMEOUT_MS and PSTACK_LOKI_UID. 0 means the default (300000, 10001), so
  	// PSTACK_LOKI_UID=0 is 10001: a root pstack chowns s3-credentials to 10001 either way.
  	LokiReadyTimeoutMs float64
  	LokiUID            float64
  }
  ```

  Then replace:

  ```go
  		MaxJobs:              num("PSTACK_MAX_JOBS"),
  	}
  ```

  with:

  ```go
  		MaxJobs:              num("PSTACK_MAX_JOBS"),
  		LokiReadyTimeoutMs:   num("PSTACK_LOKI_READY_TIMEOUT_MS"),
  		LokiUID:              num("PSTACK_LOKI_UID"),
  	}
  ```

  **3b. `packages/pstack/internal/api/server.go`: imports.** Replace:

  ```go
  	"os"
  	"regexp"
  ```

  with:

  ```go
  	"os"
  	"path/filepath"
  	"regexp"
  ```

  and replace:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
  ```

  with:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
  ```

  **3c. server.go: the Options fields.** Replace:

  ```go
  	ReadinessRestartLoop int64
  	// MaxJobs is how many lifecycle jobs RUN AT ONCE across every stack (PSTACK_MAX_JOBS); zero
  ```

  with:

  ```go
  	ReadinessRestartLoop int64
  	// LokiDir holds Loki's config.yaml and s3-credentials, which the Loki settings routes write.
  	// Default: <DataDir>/control/loki. serve passes loki.Dir (PSTACK_LOKI_DIR, then /etc/loki).
  	LokiDir string
  	// LokiReadyTimeoutMs is how long an apply waits for Loki's /ready. Zero means 300000.
  	LokiReadyTimeoutMs int64
  	// LokiUID owns s3-credentials, so Loki can read it. Zero means 10001, grafana/loki's uid.
  	LokiUID int
  	// MaxJobs is how many lifecycle jobs RUN AT ONCE across every stack (PSTACK_MAX_JOBS); zero
  ```

  **3d. server.go: the Server fields.** Replace:

  ```go
  	ctx    context.Context
  	cancel context.CancelFunc
  	http   *http.Server
  	ln     net.Listener
  	reidx  chan struct{}
  }
  ```

  with:

  ```go
  	ctx    context.Context
  	cancel context.CancelFunc
  	http   *http.Server
  	ln     net.Listener
  	reidx  chan struct{}

  	// lokiMu owns lokiGen and the two pending Loki saves, one per section (loki_apply.go). Its own
  	// mutex: never held with writeMu, and never across jobs.Start or an Emit (Go rule 14).
  	lokiMu      sync.Mutex
  	lokiGen     uint64
  	lokiChunks  *lokiEntry
  	lokiStorage *lokiEntry
  	// The apply's seams: its runner on a given ctx, the ready-poll interval, the clock.
  	lokiRunner func(ctx context.Context) exec.Runner
  	lokiPoll   time.Duration
  	lokiNow    func() time.Time
  }
  ```

  **3e. server.go: defaults in New, written back into `o` so `s.opts` carries them.** This follows the `o.TerminalArgv` pattern. It does NOT follow `routingDir`, which is a local. Replace:

  ```go
  	if o.TerminalArgv == nil {
  		o.TerminalArgv = terminal.ExecArgv
  	}
  ```

  with:

  ```go
  	if o.TerminalArgv == nil {
  		o.TerminalArgv = terminal.ExecArgv
  	}
  	if o.LokiDir == "" {
  		o.LokiDir = filepath.Join(o.DataDir, "control", "loki")
  	}
  	if o.LokiReadyTimeoutMs <= 0 {
  		o.LokiReadyTimeoutMs = 300_000
  	}
  	if o.LokiUID <= 0 {
  		o.LokiUID = 10001
  	}
  ```

  **3f. server.go: the seams in New.** Replace:

  ```go
  	s.host = exec.New(exec.Options{Level: exec.Quiet, BaseEnv: env, Ctx: ctx})
  ```

  with:

  ```go
  	s.host = exec.New(exec.Options{Level: exec.Quiet, BaseEnv: env, Ctx: ctx})
  	// Not s.host: an apply's runner stops with the ctx it is given (a job's, or the post-swap one).
  	s.lokiRunner = func(runCtx context.Context) exec.Runner {
  		return exec.New(exec.Options{Level: exec.Quiet, BaseEnv: env, Ctx: runCtx})
  	}
  	s.lokiPoll = 2 * time.Second
  	s.lokiNow = time.Now
  ```

  **3g. server.go: fail().** Replace:

  ```go
  		settings.IsError(err):
  		writeError(w, 400, err.Error())
  ```

  with:

  ```go
  		settings.IsError(err), loki.IsError(err):
  		writeError(w, 400, err.Error())
  ```

  **3h. Create `packages/pstack/internal/api/loki_apply.go`:**

  ```go
  package api

  import (
  	"time"

  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
  )

  // lokiEntry is one pending Loki save: a section's patch and who sent it. gen orders the saves; a
  // job takes every entry with gen <= its own, so a superseded save is carried, never lost.
  type lokiEntry struct {
  	gen     uint64
  	by      string
  	chunks  *loki.ChunksPatch
  	storage *loki.StoragePatch
  }

  // lokiLead is the period guard's lead: one restart plus a ready wait, with slack.
  func (s *Server) lokiLead() time.Duration {
  	return loki.Lead(time.Duration(s.opts.LokiReadyTimeoutMs) * time.Millisecond)
  }
  ```

- [ ] **Step 4: Run them and watch them pass**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestTuningReadsTheLokiKnobs|TestNewResolvesTheLokiOptions|TestFailMapsALokiErrorTo400' -v
  ```

  Expected: `--- PASS` for all three tests and their four subtests, then `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/api`.

- [ ] **Step 5: Run every negative control**

  Apply each mutation, rerun the Step 4 command, confirm the named failure, then restore:
  1. http.go: change `LokiReadyTimeoutMs:   num("PSTACK_LOKI_READY_TIMEOUT_MS")` to `num("PSTACK_LOKI_UID")`. Expected: `TestTuningReadsTheLokiKnobs` fails with `PSTACK_LOKI_READY_TIMEOUT_MS=4000 read as {… LokiReadyTimeoutMs:0 …}`.
  2. http.go: delete the `LokiUID:` line. Expected: `PSTACK_LOKI_UID=501 read as …`.
  3. server.go: delete `o.LokiUID = 10001`. Expected: `zero_values_take_the_defaults` fails with `LokiUID 0`.
  4. server.go: delete `s.lokiPoll = 2 * time.Second`. Expected: `seams: poll 0s`.
  5. server.go: change the lokiRunner body to `Ctx: s.ctx`. Expected: `lokiRunner must run on the ctx it is given`.
  6. loki_apply.go: make lokiLead use `s.opts.ReadinessTimeoutMs`. Expected: `lokiLead = 10m9s, want 10m4s`.
  7. server.go: set `o.LokiUID = 10001` without the `if`. Expected: `options overridden: … 10001`.
  8. server.go: remove `loki.IsError(err)` from fail(). Expected: the first `TestFailMapsALokiErrorTo400` subtest fails with `500 {…}`.
  9. server.go: append `errors.Is(err, loki.ErrOneWay)` to fail()'s list. Expected: the sentinel subtest fails with `400 {…}`.

- [ ] **Step 6: Map the knobs in serve**

  In `packages/pstack/internal/cli/serve.go`, replace:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
  ```

  with:

  ```go
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
  	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
  ```

  and replace:

  ```go
  		MaxJobs:              int(tuning.MaxJobs),
  ```

  with:

  ```go
  		MaxJobs:              int(tuning.MaxJobs),
  		LokiDir:              loki.Dir(dataDir), // env override > in-container /etc/loki > host path
  		LokiReadyTimeoutMs:   int64(tuning.LokiReadyTimeoutMs),
  		LokiUID:              int(tuning.LokiUID),
  ```

  `Serve` blocks on signals and has no unit test; the `MaxJobs` line has the same gap. T15 checks this mapping black-box. It boots the real binary with `PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS=4000` and `PSTACK_LOKI_UID=<getuid>`, and a dropped line fails there: the PUT gets 409 (wrong directory or needs root), or the rollback tests wait 300s.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go vet ./internal/api/ ./internal/cli/ && go build -o bin/pstack ./cmd/pstack
  ```

  Expected: no output, exit 0.

- [ ] **Step 7: Environment rows in usage.md**

  In `docs/usage.md`, `### Environment` table, find this row (unique; slice 1 inserts its rows after `PSTACK_ORCHESTRATOR`, not here):

  ```markdown
  | `PSTACK_SSO_STATE_TTL_S` · `PSTACK_SSO_DISCOVERY_TTL_S` | `serve` | `300` · `3600` | how long a half-finished SSO sign-in is remembered, and how long a provider's discovery document and JWKS are trusted. Same audience. |
  ```

  Insert directly after it:

  ```markdown
  | `PSTACK_LOKI_DIR` | `serve` | `/etc/loki` if mounted, else `<PSTACK_DATA>/control/loki` | Loki's `config.yaml` and `s3-credentials`, written by Loki settings. |
  | `PSTACK_LOKI_READY_TIMEOUT_MS` | `serve` | `300000` | how long a Loki settings apply waits for Loki to be ready. Test harness tuning; a host never needs it. |
  | `PSTACK_LOKI_UID` | `serve` | `10001` | owner of `s3-credentials`, so Loki can read it. `0` reads as unset. |
  ```

- [ ] **Step 8: CHANGELOG bullet**

  In `packages/pstack/CHANGELOG.md`, under `## Unreleased` → `### Added`, add this as the last bullet, directly above the first `### Changed` line in the file. If `## Unreleased` does not exist, insert `## Unreleased`, a blank line, `### Added`, a blank line and this bullet between `# Changelog` and `## 0.39.1 — 2026-09-14`.

  ```markdown
  - **`PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS` and `PSTACK_LOKI_UID` on `serve`**: Loki's
    config directory (`/etc/loki`, else `<PSTACK_DATA>/control/loki`), the apply's ready wait
    (`300000`) and the owner of `s3-credentials` (`10001`).
  ```

- [ ] **Step 9: Package gate**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ ./internal/cli/
  ```

  Expected: `ok` for both packages. Existing `New(Options{DataDir: t.TempDir()})` callers are unaffected, because `LokiDir` defaults under their temp dir and nothing reads it until T11.

- [ ] **Step 10: Commit**

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Take the IDs of `packages/pstack/internal/api/server.go`, `packages/pstack/internal/api/http.go`, `packages/pstack/internal/api/http_test.go`, `packages/pstack/internal/api/loki_apply.go`, `packages/pstack/internal/cli/serve.go`, `docs/usage.md` and `packages/pstack/CHANGELOG.md`. Skip `packages/conformance/golden/host/db/*` and `.superpowers/`.

  ```bash
  but commit -b claude/loki-logging-settings -m "feat(api): Loki options, tuning knobs and the directory" <ids>
  ```

---

### Task 9: api: the loki-apply job — pending patches and the forward path

**Files:**
- Modify: `packages/pstack/internal/api/loki_apply.go`. T8 created this file with only `lokiEntry` and `func (s *Server) lokiLead()` in it. Leave both exactly as T8 wrote them and do not declare them again. T9 puts its header comment above `package api`, replaces T8's two-line import block with the full one, and appends its code after `lokiLead`.
- Modify: `docs/control-plane.md`. Insert a new subsection right before the heading `## 6. Submitting a deployment`, which ends §5g after whatever T3 and T5 appended. Line numbers are only a guide, because slice 1 is still moving them.
- Test: `packages/pstack/internal/api/loki_apply_test.go` (create)

**Interfaces:**
- Consumes:
  - From T4: `loki.Read(st *store.Store) (*loki.Row, error)`, `loki.Save(st *store.Store, s loki.Settings, secret string, previous *loki.Row) error`, `loki.Finish(st *store.Store) error`, `loki.Merge(row *loki.Row, c *loki.ChunksPatch, sp *loki.StoragePatch, lead time.Duration) (loki.Settings, string, error)`, `loki.Changed(before *loki.Row, after loki.Settings, afterSecret string) []string`.
  - From T2: `loki.Render(s loki.Settings) (string, error)`, `loki.Credentials(keyID, secret string) string`, `loki.WriteFile(path, body string, mode os.FileMode, lokiUID int) error`, `loki.ConfigFile`, `loki.CredentialsFile`, `loki.NextSuffix`, `loki.StorageS3`, `loki.Defaults()`.
  - From T3: `loki.Periods(configYAML string) ([]loki.Period, error)`, `loki.CheckPeriods(loaded, next []loki.Period, now time.Time, lead time.Duration, adding bool) error`.
  - From T6: `jobs.LokiApply` and the `logging.changed` event name.
  - From T8:
    - fields `lokiMu`, `lokiGen`, `lokiChunks`, `lokiStorage`, `lokiRunner func(ctx context.Context) exec.Runner`, `lokiPoll time.Duration`, `lokiNow func() time.Time`
    - `type lokiEntry struct { gen uint64; by string; chunks *loki.ChunksPatch; storage *loki.StoragePatch }` and `func (s *Server) lokiLead() time.Duration`, both in loki_apply.go
    - `Options.LokiDir`, `Options.LokiReadyTimeoutMs int64` and `Options.LokiUID int`, with defaults written back into `s.opts`
  - Existing code:
    - `inspect.ControlRuntime(r exec.Runner) inspect.ControlView` (inspect/control.go:53)
    - `inspect.ControlView{Containers []ControlContainer; Reachable bool}` (control.go:45-50); `ControlContainer` embeds `ContainerInfo{ID, Name, Service *string, Image, StartedAt *int64}` (inspect.go:60-72)
    - `inspect.RestartControlService(r exec.Runner, service string) (string, error)` (control.go:95) and `inspect.ControlProject` (control.go:27)
    - `compose.Shq` (compose/compose.go:42)
    - `(*jobs.Registry).Start(stackName string, action Action, work Work, scrub func(string) string) (Job, bool)` (jobs.go:552), `(*jobs.Registry).Get` (jobs.go:359), `jobs.State.Terminal` (jobs.go:153)
    - `redact.RedactText(text string, extraSecrets ...string) string` (redact.go:153). It skips extras shorter than 8, so an empty secret is harmless.
    - `s.secretValues()` (server.go:338), `scheduler.FormatDuration(ms int64) string` (scheduler.go:39), `js.Truncate` (js.go:52)
    - `stack.StepResult` and `stack.Outcome` (stack.go:39-56)
    - `exec.NewFake`, `(*exec.Fake).Answer` and `(*exec.Fake).Commands` (exec.go:257-310)
    - `events.Bus.On` and `events.Bus.Emit` (events.go:177, :211), `jobs.Job.Log []log.Event` with its `Message` field (log.go:25-31)
- Produces. Later tasks rely on these names.
  - `func (s *Server) lokiPut(by string, c *loki.ChunksPatch, sp *loki.StoragePatch) uint64`
  - `func (s *Server) lokiTake(g uint64) (c *loki.ChunksPatch, sp *loki.StoragePatch, by string)`
  - `func (s *Server) lokiPending() bool`
  - `func (s *Server) startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool)`. When g == 0 it takes no patch, which is a boot apply.
  - `const lokiService = "loki"`
  - `func lokiContainer(view inspect.ControlView) (inspect.ControlContainer, bool)` returns the first container whose `Service` is `lokiService`. T12 uses it for GET's `enabled` and for PUT step 1.
  - `func lokiRender(set loki.Settings, secret string) (config, creds string, err error)` is `loki.Render`, plus `loki.Credentials` when the storage is S3. Without S3, `creds` is `""`.
  - `func (s *Server) lokiDisk() (config, creds string, credsHere bool, err error)` reads `<LokiDir>/config.yaml` and returns os.ReadFile's error unwrapped, so `errors.Is(err, fs.ErrNotExist)` holds when the file is absent. A missing `s3-credentials` gives `credsHere=false` and no error.
  - `func (s *Server) lokiWriteNext(config, creds string) error` writes `config.yaml.next` at 0644. When `creds != ""` it also writes `s3-credentials.next` at 0600. Both go through `loki.WriteFile` with `s.opts.LokiUID`. On error it removes both `.next` files.
  - `func (s *Server) lokiSwap(withCreds bool) error` renames `s3-credentials.next` and then `config.yaml.next` into place. When `!withCreds` it removes `s3-credentials` instead, and an absent file is not an error.
  - Phase constants of type `stack.Phase`: `phaseFind`, `phaseRender`, `phaseVerify`, `phaseCommit`, `phaseSwap`, `phaseRestart`, `phaseReady`, `phaseFinish`, `phaseLog` (`"find"` … `"log"`). They are named `phase*` and not `loki*` because a package-level `const lokiRender` would clash with `func lokiRender`.
  - `type lokiApply struct { s *Server; g uint64; by string; boot bool; idc chan string; scrubs []string; sink log.Sink; container inspect.ControlContainer; steps []stack.StepResult }`, with these methods:
    - `run(ctx context.Context) stack.Outcome`
    - `step(phase stack.Phase, ok bool, message string)`
    - `end(phase stack.Phase, ok bool, message string) stack.Outcome`
    - `outcome() stack.Outcome`
    - `ready(post exec.Runner) bool`. This is the only ready loop, and T10 reuses it.
  - `type lokiSink struct { inner log.Sink; scrub func(string) string }` and `func lastLine(stderr string, code int) string`
  - The transcript's first line is `by <by>`, emitted at `log.Info` right after `if by == "" { by = a.by }`. A boot apply's first line is `by pstack (boot)` (spec: "The job runs with By: pstack (boot)"). T11 drops its step 8b.
  - **Seam for T10.** Three returns in `run` end the job after the step-4 commit, leaving the row holding the save and `previous_*`. T9 ships them as plain failures, with no stub, no `lokiRollback`, no `Server.lokiReady` and no `lokiFiles`. T10 replaces exactly these three lines:
    - `return a.end(phaseSwap, false, err.Error())`
    - `return a.end(phaseRestart, false, err.Error())`
    - `return a.end(phaseReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))`

    In scope at those lines: `post` (the post runner), `lead`, `row` (the row read at step 2, nil when the table was empty), `a.container.ID`, `a.sink` and `a.ready(post)`. T10's rollback files come from `lokiRender(previous settings, previous secret)`, `s.lokiWriteNext` and `s.lokiSwap`.
  - **Seam for T11.** The resume hook goes directly after `row, err := loki.Read(s.store)` and its `return a.end(phaseRender, false, err.Error())` in step 2. At that point:
    - `c, sp, by` are already taken, because `lokiTake` runs at the top of `run`, and the `by` line is already emitted.
    - `a.container` is set. `a.container.StartedAt` is Loki's start time in ms, as an `*int64` that can be nil.
    - `runner`, `lead` and `a.sink` are in scope.

    `reconcileLoki` reuses `s.lokiDisk` (absent file: `errors.Is(err, fs.ErrNotExist)`, return silently) and `lokiRender`.

- [ ] **Step 1: Write the failing test**

Create `packages/pstack/internal/api/loki_apply_test.go`:

```go
package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
)

// lokiInspect is `docker inspect` of a running loki container, l1: the id, image, service label and
// start time the apply reads (inspect.go:229-330).
const lokiInspect = `[{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}},"State":{"Status":"running","StartedAt":"2026-09-15T10:00:00Z"}}]`

const lokiPS = "docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'"

// lokiFixture is a server whose LokiDir holds slice 1's config, with every loki-apply command sent to
// one exec.Fake. override answers first. Anything it declines gets a running l1 and OK.
func lokiFixture(t *testing.T, override func(cmd string) (exec.Result, bool)) (*Server, *exec.Fake) {
	t.Helper()
	data := t.TempDir()
	dir := filepath.Join(data, "control", "loki")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(pstack.LokiConfig), 0o666); err != nil {
		t.Fatal(err)
	}
	// LokiUID is this process's euid: loki.WriteFile checks CredentialsOwner before a 0600 write, and
	// the default 10001 would refuse the S3 test's s3-credentials.next at verify.
	s, err := New(Options{DataDir: data, Bus: events.New(), Log: func(string) {}, LokiDir: dir, LokiReadyTimeoutMs: 300, LokiUID: os.Geteuid()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if override != nil {
			if r, ok := override(cmd); ok {
				return r, true
			}
		}
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: lokiInspect}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f
	s.lokiRunner = func(context.Context) exec.Runner { return f }
	s.lokiPoll = time.Millisecond
	return s, f
}

// waitLokiJob polls the registry until the job is terminal. No such helper exists in package api;
// jobs_test.go's is package jobs'.
func waitLokiJob(t *testing.T, s *Server, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if j, ok := s.jobs.Get(id); ok && j.State.Terminal() {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is not terminal after 10s", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// loggingChanged collects logging.changed events. The mutex exists because the job goroutine emits
// and the test reads.
func loggingChanged(s *Server) func() []events.Event {
	var mu sync.Mutex
	got := []events.Event{}
	s.bus.On(func(e events.Event) {
		if e.Event == "logging.changed" {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
	})
	return func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event{}, got...)
	}
}

func retention(days int) *loki.ChunksPatch {
	return &loki.ChunksPatch{RetentionDays: days, Chunks: loki.Defaults().Chunks}
}

func readLoki(t *testing.T, s *Server, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.opts.LokiDir, name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func lastStep(t *testing.T, j jobs.Job) (phase string, ok bool, message string) {
	t.Helper()
	if j.Outcome == nil || len(j.Outcome.Steps) == 0 {
		t.Fatalf("job %s has no steps: %s", j.ID, jsonx.Must(j))
	}
	st := j.Outcome.Steps[len(j.Outcome.Steps)-1]
	if st.Message != nil {
		message = *st.Message
	}
	return string(st.Phase), st.OK, message
}

func assertNoNext(t *testing.T, s *Server) {
	t.Helper()
	for _, n := range []string{loki.ConfigFile, loki.CredentialsFile} {
		if _, err := os.Stat(filepath.Join(s.opts.LokiDir, n+loki.NextSuffix)); !os.IsNotExist(err) {
			t.Errorf("%s%s is left behind (%v)", n, loki.NextSuffix, err)
		}
	}
}

func restarted(f *exec.Fake) bool {
	for _, c := range f.Commands() {
		if strings.HasPrefix(c, "docker restart") {
			return true
		}
	}
	return false
}

func TestLokiApplyRunsTheStepsInOrder(t *testing.T) {
	// negative control: move the RestartControlService call above the `docker run … -verify-config`
	// line — command 2 is `docker ps`, not the verify run, and the order check fails. (Also run:
	// delete the logging.changed Emit — the event check fails; delete `a.sink.Emit(log.Info, "by "+by)`
	// — the first-log-line check fails.)
	s, f := lokiFixture(t, nil)
	changed := loggingChanged(s)
	job, ok := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if !ok || job.Action != jobs.LokiApply || job.Stack != inspect.ControlProject {
		t.Fatalf("a loki-apply job on the control key: %+v %v", job.Stub(), ok)
	}
	j := waitLokiJob(t, s, job.ID)
	if j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if len(j.Log) == 0 || j.Log[0].Message != "by alice" {
		t.Errorf("the transcript's first line must be `by alice`: %s", jsonx.Must(j.Log))
	}
	want := []string{
		lokiPS, "docker inspect 'l1'",
		"docker run --rm --network none --volumes-from 'l1:ro' 'grafana/loki:3.7.7' -config.file=/etc/loki/config.yaml.next -verify-config",
		lokiPS, "docker inspect 'l1'", "docker restart 'l1'",
		"docker exec 'l1' /usr/bin/loki -health",
	}
	cmds := f.Commands()
	if len(cmds) != len(want)+1 {
		t.Fatalf("commands:\n%s", strings.Join(cmds, "\n"))
	}
	for i, w := range want {
		if cmds[i] != w {
			t.Errorf("command %d = %q, want %q", i, cmds[i], w)
		}
	}
	if logs := cmds[len(want)]; !strings.HasPrefix(logs, "docker logs --since '") || !strings.HasSuffix(logs, "' 'l1'") {
		t.Errorf("last command = %q, want docker logs --since '<RFC3339>' 'l1'", logs)
	}
	phases := []string{}
	for _, st := range j.Outcome.Steps {
		phases = append(phases, string(st.Phase))
		if st.Axis != "loki" || !st.OK {
			t.Errorf("step %+v", st)
		}
	}
	if got := strings.Join(phases, " "); got != "find render verify commit swap restart ready finish" {
		t.Errorf("phases %q", got)
	}
	config, _ := readLoki(t, s, loki.ConfigFile)
	if !strings.Contains(config, "  retention_period: 336h") || !strings.Contains(config, "  max_query_lookback: 336h") {
		t.Errorf("config.yaml:\n%s", config)
	}
	if _, here := readLoki(t, s, loki.CredentialsFile); here {
		t.Error("a filesystem render writes no s3-credentials")
	}
	assertNoNext(t, s)
	row, err := loki.Read(s.store)
	if err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 14 {
		t.Fatalf("row: %+v (%v)", row, err)
	}
	evs := changed()
	wantData := `{"by":"alice","job":"` + job.ID + `","changed":["retention"],"storage":"filesystem","cutover":null,"retentionDays":14}`
	if len(evs) != 1 || string(evs[0].Data) != wantData {
		t.Fatalf("logging.changed: got %d events, want exactly one %s", len(evs), wantData)
	}
}

func TestLokiApplyFindsTheLokiContainer(t *testing.T) {
	// negative control: end the no-container arm `failed` for a boot apply too — the boot case reads
	// failed and the check fails. (Also run: move the lokiTake call below the find step — the save's
	// patch stays pending and the lokiPending check fails.)
	traefik := `[{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.5","Labels":{"com.docker.compose.service":"traefik"}},"State":{"Status":"running"}}]`
	for _, c := range []struct {
		name      string
		reachable bool
		boot      bool
		ok        bool
		message   string
	}{
		{"docker does not answer", false, false, false, "docker did not answer"},
		{"a save on a host without loki", true, false, false, "Loki is not running on this host"},
		{"a boot apply on a host without loki", true, true, true, "no loki container"},
	} {
		s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
			switch {
			case strings.HasPrefix(cmd, "docker ps -aq"):
				return exec.Result{OK: c.reachable, Stdout: "t1\n"}, true
			case strings.HasPrefix(cmd, "docker inspect"):
				return exec.Result{OK: true, Stdout: traefik}, true
			}
			return exec.Result{}, false
		})
		g := uint64(0)
		if !c.boot {
			g = s.lokiPut("alice", retention(14), nil)
		}
		job, _ := s.startLokiApply(g, "alice", c.boot)
		j := waitLokiJob(t, s, job.ID)
		phase, ok, msg := lastStep(t, j)
		if phase != "find" || ok != c.ok || msg != c.message || len(j.Outcome.Steps) != 1 {
			t.Errorf("%s: %s %v %q (%d steps)", c.name, phase, ok, msg, len(j.Outcome.Steps))
		}
		for _, cmd := range f.Commands() {
			if !strings.HasPrefix(cmd, "docker ps") && !strings.HasPrefix(cmd, "docker inspect") {
				t.Errorf("%s: ran %q", c.name, cmd)
			}
		}
		if s.lokiPending() {
			t.Errorf("%s: a failed job must not leave its save pending", c.name)
		}
		if got, _ := readLoki(t, s, loki.ConfigFile); got != pstack.LokiConfig {
			t.Errorf("%s: config.yaml changed", c.name)
		}
	}
}

func TestLokiApplyVerifyFailureChangesNothing(t *testing.T) {
	// negative control: move loki.Save above the verify run — the row is no longer empty and the row
	// check fails. (Also run: delete the two deferred os.Remove calls — assertNoNext fails.)
	s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker run ") {
			return exec.Result{OK: false, Code: 1, Stderr: "level=info msg=\"loading config\"\nlevel=error msg=\"invalid config\" err=\"ingester: bad chunk encoding\"\n\n"}, true
		}
		return exec.Result{}, false
	})
	changed := loggingChanged(s)
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	j := waitLokiJob(t, s, job.ID)
	phase, ok, msg := lastStep(t, j)
	if j.State != jobs.Failed || phase != "verify" || ok || msg != `level=error msg="invalid config" err="ingester: bad chunk encoding"` {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
		t.Error("config.yaml changed")
	}
	assertNoNext(t, s)
	if row, err := loki.Read(s.store); err != nil || row != nil {
		t.Errorf("the row must stay empty: %+v (%v)", row, err)
	}
	if restarted(f) {
		t.Error("a failed verify must not restart Loki")
	}
	if n := len(changed()); n != 0 {
		t.Errorf("%d logging.changed events for a failed apply", n)
	}
}

func TestLokiApplyRowWriteFailureRestartsNothing(t *testing.T) {
	// negative control: ignore loki.Save's error (`_ = loki.Save(…)` and carry on) — the job swaps
	// and issues `docker restart 'l1'`, and both checks fail.
	var s *Server
	s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker run ") {
			// Step 2 has already read the row. Every later statement now fails, as on a full disk.
			_ = s.store.Close()
			return exec.Result{OK: true}, true
		}
		return exec.Result{}, false
	})
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	j := waitLokiJob(t, s, job.ID)
	phase, ok, msg := lastStep(t, j)
	if j.State != jobs.Failed || phase != "commit" || ok || !strings.HasPrefix(msg, "could not record the save: ") {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	if restarted(f) {
		t.Error("a failed commit must not restart Loki")
	}
	if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
		t.Error("config.yaml changed")
	}
	assertNoNext(t, s)
}

func TestLokiApplySavesTheRowBeforeTheRestart(t *testing.T) {
	// negative control: move loki.Save (the commit step) below the ready wait — the row read at the
	// restart is still empty and the check fails.
	var s *Server
	var atRestart *loki.Row
	var readErr error
	s, _ = lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker restart ") {
			// The job is between statements here, never inside store.Tx, so the one pooled connection
			// is free (rule 16).
			atRestart, readErr = loki.Read(s.store)
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		}
		return exec.Result{}, false
	})
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if j := waitLokiJob(t, s, job.ID); j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if readErr != nil || atRestart == nil || !atRestart.InFlight || atRestart.Settings.RetentionDays != 14 || atRestart.Previous != nil {
		t.Fatalf("at the restart the row must hold the save and its write-ahead record: %+v (%v)", atRestart, readErr)
	}
}

func TestLokiApplyCarriesASupersededSave(t *testing.T) {
	// negative control: make lokiTake take an entry only when `e.gen == g` — the successor applies
	// retention alone, the row stays filesystem, and the row check fails. (Also run: drop `secret`
	// from the scrub values appended at render — the job record carries the secret and the record
	// check fails.)
	const secret = "wJalrXUtnFEMI-K7MDENG-bPxRfiCY"
	const keyID = "AKIAIOSFODNN7EXAMPLE"
	release := make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	var s *Server
	s, _ = lokiFixture(t, func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker exec "):
			once.Do(func() { <-release }) // the first apply waits here, after its commit and swap
			return exec.Result{OK: true}, true
		case strings.HasPrefix(cmd, "docker logs "):
			// Loki echoes the credential it was given into its own log. The record must not keep it.
			if _, err := os.Stat(filepath.Join(s.opts.LokiDir, loki.CredentialsFile)); err == nil {
				return exec.Result{OK: true, Stderr: "level=info msg=\"Loki started\"\nlevel=error msg=\"flush failed\" detail=\"denied for " + secret + "\"\n"}, true
			}
		}
		return exec.Result{}, false
	})
	t.Cleanup(free)
	changed := loggingChanged(s)
	cutover := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	storage := &loki.StoragePatch{Storage: loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: keyID, Cutover: cutover,
	}}, Secret: secret}

	first, _ := s.startLokiApply(s.lokiPut("alice", retention(10), nil), "alice", false)
	queued, _ := s.startLokiApply(s.lokiPut("root (PSTACK_TOKEN)", nil, storage), "root (PSTACK_TOKEN)", false)
	successor, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if first.State != jobs.Running || queued.State != jobs.Queued || successor.State != jobs.Queued {
		t.Fatalf("want running, queued, queued: %q %q %q", first.State, queued.State, successor.State)
	}
	free()
	if j := waitLokiJob(t, s, first.ID); j.State != jobs.OK {
		t.Fatalf("first %q: %s", j.State, jsonx.Must(j))
	}
	j := waitLokiJob(t, s, successor.ID)
	if j.State != jobs.OK {
		t.Fatalf("successor %q: %s", j.State, jsonx.Must(j))
	}
	if q, _ := s.jobs.Get(queued.ID); q.State != jobs.Superseded || q.StartedAt != nil {
		t.Fatalf("the storage save's own job must never run: %q, startedAt %v", q.State, q.StartedAt)
	}
	row, err := loki.Read(s.store)
	if err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 14 || row.Settings.Storage.Type != loki.StorageS3 || row.Secret != secret {
		t.Fatalf("the successor must apply both saves: %+v (%v)", row, err)
	}
	config, _ := readLoki(t, s, loki.ConfigFile)
	for _, want := range []string{"  retention_period: 336h", "      object_store: s3", `    - from: "` + cutover + `"`} {
		if !strings.Contains(config, want) {
			t.Errorf("config.yaml lacks %q", want)
		}
	}
	if creds, here := readLoki(t, s, loki.CredentialsFile); !here || creds != loki.Credentials(keyID, secret) {
		t.Errorf("s3-credentials = %q (present %v)", creds, here)
	}
	if st, err := os.Stat(filepath.Join(s.opts.LokiDir, loki.CredentialsFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("s3-credentials must be 0600: %v (%v)", st, err)
	}
	assertNoNext(t, s)
	if phase, ok, msg := lastStep(t, j); phase != "log" || !ok || !strings.HasPrefix(msg, "non-fatal: Loki logged 1 errors after restart: ") {
		t.Errorf("log step: %s %v %q", phase, ok, msg)
	}
	if strings.Contains(string(jsonx.Must(j)), secret) {
		t.Error("the S3 secret reached the job record")
	}
	evs := changed()
	if len(evs) != 2 {
		t.Fatalf("one logging.changed per applying job, got %d", len(evs))
	}
	d := string(evs[1].Data)
	if !strings.Contains(d, `"by":"alice"`) || !strings.Contains(d, `"changed":["retention","storage","credentials"]`) ||
		!strings.Contains(d, `"cutover":"`+cutover+`"`) || strings.Contains(d, keyID) || strings.Contains(d, secret) {
		t.Errorf("successor's logging.changed: %s", d)
	}
}

func TestLokiApplyWhoseSaveWasReplacedChangesNothing(t *testing.T) {
	// negative control: drop `e.gen <= g` from both lokiTake branches — the older job takes bob's
	// newer save and applies it, and the `nothing to change` check fails.
	s, f := lokiFixture(t, nil)
	older := s.lokiPut("alice", retention(10), nil)
	newer := s.lokiPut("bob", retention(14), nil)
	job, _ := s.startLokiApply(older, "alice", false)
	j := waitLokiJob(t, s, job.ID)
	if phase, ok, msg := lastStep(t, j); j.State != jobs.OK || phase != "render" || !ok || msg != "nothing to change" {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	for _, c := range f.Commands() {
		if c != lokiPS && c != "docker inspect 'l1'" {
			t.Fatalf("a job with nothing to change ran %q", c)
		}
	}
	if !s.lokiPending() {
		t.Fatal("bob's save must still be pending")
	}
	job2, _ := s.startLokiApply(newer, "bob", false)
	if j := waitLokiJob(t, s, job2.ID); j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if row, err := loki.Read(s.store); err != nil || row == nil || row.Settings.RetentionDays != 14 {
		t.Errorf("row: %+v (%v)", row, err)
	}
	if s.lokiPending() {
		t.Error("nothing is pending once bob's save is applied")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiApply
```

Expected: a build failure. The output includes lines like `s.lokiPut undefined (type *Server has no field or method lokiPut)` and `s.startLokiApply undefined`, and ends with `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/api [build failed]`.

- [ ] **Step 3: Implement**

In `packages/pstack/internal/api/loki_apply.go`, make two edits. Leave T8's `lokiEntry` and `lokiLead` in between exactly as they are.

**3a.** Replace T8's file head, which is this exact text:

```go
package api

import (
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
)
```

with:

```go
// The loki-apply job: Loki's saved settings, onto control/loki and the loki container.
//
// ONE JOB ON THE CONTROL PROJECT'S KEY (inspect.ControlProject). Applies run one at a time, one
// waits, and each counts against PSTACK_MAX_JOBS. A save's job and a boot job are the same job.
//
// PENDING PATCHES. A PUT stores its section's patch with a generation under lokiMu, then starts a job
// that holds only that number. The job takes every patch at or below it. jobs.Start keeps one waiting
// job per key and supersedes the rest, so a superseded save is carried by its successor, and a running
// job never takes a newer save.
//
// THE STEPS. find → render → verify → commit → swap → restart → ready → finish → log, then
// logging.changed when the row changed. Nothing live changes before the swap: a failure up to the
// commit removes the .next files and leaves the row. The row is saved with previous_* BEFORE the swap
// and finished after ready, so a pstack that dies in between leaves its own undo record.
//
// THE POST RUNNER. From the swap on, commands run on a runner with its own 2×lead deadline. It is not
// the job's runner, which refuses every command once cancelled (exec.go:110), and not s.host, which
// Stop cancels. A cancel after the swap still restarts and waits.
//
// SCRUBBING. The secret, the previous secret and the key id are known only once the patches are
// merged. One scrub serves the sink and the record, and it reads them when it is called, on the job's
// goroutine.
package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)
```

**3b.** After the closing brace of T8's `func (s *Server) lokiLead() time.Duration { … }`, at the end of the file, append:

```go

// lokiService is the compose service name init gives Loki.
const lokiService = "loki"

// The apply's step phases. They are local because package stack's phases belong to the lifecycle
// hooks. None of these is PhaseAssertGone, so Outcome.Leaked() never fires for an apply.
const (
	phaseFind    stack.Phase = "find"
	phaseRender  stack.Phase = "render"
	phaseVerify  stack.Phase = "verify"
	phaseCommit  stack.Phase = "commit"
	phaseSwap    stack.Phase = "swap"
	phaseRestart stack.Phase = "restart"
	phaseReady   stack.Phase = "ready"
	phaseFinish  stack.Phase = "finish"
	phaseLog     stack.Phase = "log"
)

// lokiPut makes a save's patch its section's pending entry and returns the entry's gen. A newer save
// of the same section replaces the entry, because a PUT body is the whole section.
func (s *Server) lokiPut(by string, c *loki.ChunksPatch, sp *loki.StoragePatch) uint64 {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	s.lokiGen++
	e := &lokiEntry{gen: s.lokiGen, by: by, chunks: c, storage: sp}
	if c != nil {
		s.lokiChunks = e
	}
	if sp != nil {
		s.lokiStorage = e
	}
	return e.gen
}

// lokiTake removes and returns the pending patches saved at or before gen g. by is the newest taken
// save's principal, or "" when nothing was taken.
func (s *Server) lokiTake(g uint64) (c *loki.ChunksPatch, sp *loki.StoragePatch, by string) {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	var newest uint64
	if e := s.lokiChunks; e != nil && e.gen <= g {
		c, newest, by = e.chunks, e.gen, e.by
		s.lokiChunks = nil
	}
	if e := s.lokiStorage; e != nil && e.gen <= g {
		sp = e.storage
		if e.gen > newest {
			by = e.by
		}
		s.lokiStorage = nil
	}
	return c, sp, by
}

// lokiPending reports whether any save is waiting for a job.
func (s *Server) lokiPending() bool {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	return s.lokiChunks != nil || s.lokiStorage != nil
}

// lokiContainer is the control stack's loki container, if it has one.
func lokiContainer(view inspect.ControlView) (inspect.ControlContainer, bool) {
	for _, c := range view.Containers {
		if c.Service != nil && *c.Service == lokiService {
			return c, true
		}
	}
	return inspect.ControlContainer{}, false
}

// lokiRender is what a settings value writes: config.yaml, and s3-credentials ("" without S3).
func lokiRender(set loki.Settings, secret string) (config, creds string, err error) {
	if config, err = loki.Render(set); err != nil {
		return "", "", err
	}
	if set.Storage.Type == loki.StorageS3 {
		creds = loki.Credentials(set.Storage.S3.AccessKeyID, secret)
	}
	return config, creds, nil
}

// lokiDisk is what control/loki holds now. config.yaml's read error is returned as is, so a caller
// can tell absent (fs.ErrNotExist) from unreadable. An absent s3-credentials is credsHere=false.
func (s *Server) lokiDisk() (config, creds string, credsHere bool, err error) {
	b, err := os.ReadFile(filepath.Join(s.opts.LokiDir, loki.ConfigFile))
	if err != nil {
		return "", "", false, err
	}
	c, err := os.ReadFile(filepath.Join(s.opts.LokiDir, loki.CredentialsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return string(b), "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return string(b), string(c), true, nil
}

// lokiWriteNext writes config.yaml.next and, with S3, s3-credentials.next. A failed write leaves
// neither.
func (s *Server) lokiWriteNext(config, creds string) error {
	nextCfg := filepath.Join(s.opts.LokiDir, loki.ConfigFile+loki.NextSuffix)
	nextCreds := filepath.Join(s.opts.LokiDir, loki.CredentialsFile+loki.NextSuffix)
	err := loki.WriteFile(nextCfg, config, 0o644, s.opts.LokiUID)
	if err == nil && creds != "" {
		err = loki.WriteFile(nextCreds, creds, 0o600, s.opts.LokiUID)
	}
	if err != nil {
		_ = os.Remove(nextCreds)
		_ = os.Remove(nextCfg)
	}
	return err
}

// lokiSwap moves the .next files into place. Credentials go first, so a config that names S3 never
// lands without them. Without S3, s3-credentials is removed.
func (s *Server) lokiSwap(withCreds bool) error {
	cfg := filepath.Join(s.opts.LokiDir, loki.ConfigFile)
	creds := filepath.Join(s.opts.LokiDir, loki.CredentialsFile)
	if withCreds {
		if err := os.Rename(creds+loki.NextSuffix, creds); err != nil {
			return err
		}
	} else if err := os.Remove(creds); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(cfg+loki.NextSuffix, cfg)
}

// lokiApply is one loki-apply job. Owner: the job's goroutine. startLokiApply sets s, g, by, boot, idc
// and scrubs before Start. The scrub closure Start holds also runs on the job's goroutine, after the
// work returns (jobs.go:689-719).
type lokiApply struct {
	s         *Server
	g         uint64
	by        string
	boot      bool
	idc       chan string
	scrubs    []string
	sink      log.Sink
	container inspect.ControlContainer
	steps     []stack.StepResult
}

// lokiSink scrubs each line with the job's values as they stand when the line is emitted.
type lokiSink struct {
	inner log.Sink
	scrub func(string) string
}

func (k lokiSink) Emit(level log.Level, message string) { k.inner.Emit(level, k.scrub(message)) }

// startLokiApply accepts a loki-apply job for the saves up to gen g. g 0 takes none (a boot apply).
// by names who asked. Call it holding no lock, because Start emits (rule 14). ok=false is Start's
// refusal: a waiting preempting `down` on the control key.
func (s *Server) startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool) {
	a := &lokiApply{s: s, g: g, by: by, boot: boot, idc: make(chan string, 1), scrubs: append([]string{}, s.secretValues()...)}
	scrub := func(text string) string { return redact.RedactText(text, a.scrubs...) }
	work := func(rawSink log.Sink, ctx context.Context) (stack.Outcome, error) {
		a.sink = lokiSink{inner: rawSink, scrub: scrub}
		return a.run(ctx), nil
	}
	job, ok := s.jobs.Start(inspect.ControlProject, jobs.LokiApply, work, scrub)
	if ok {
		// The work reads its id at step 10. Start may already have dispatched it (pump runs inline),
		// so that receive may wait a moment for this send. The work of a refused or superseded job
		// never runs, so no receive ever waits for a send that never comes.
		a.idc <- job.ID
	}
	return job, ok
}

// run is the work. Every return records the step that ended it.
func (a *lokiApply) run(ctx context.Context) stack.Outcome {
	s := a.s
	lead := s.lokiLead()
	runner := s.lokiRunner(ctx)
	// Taken before anything can fail, so a failed job reports the saves it took and none stays
	// pending. The set is the same as at render: gen ≤ g.
	c, sp, by := s.lokiTake(a.g)
	if by == "" {
		by = a.by
	}
	a.sink.Emit(log.Info, "by "+by)

	// 1. find
	view := inspect.ControlRuntime(runner)
	if !view.Reachable {
		return a.end(phaseFind, false, "docker did not answer")
	}
	container, found := lokiContainer(view)
	if !found && a.boot {
		return a.end(phaseFind, true, "no loki container")
	}
	if !found {
		return a.end(phaseFind, false, "Loki is not running on this host")
	}
	a.container = container
	a.step(phaseFind, true, "")

	// 2. render: the row as read now, the patches over it, every save rule again, then the period guard.
	row, err := loki.Read(s.store)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	merged, secret, err := loki.Merge(row, c, sp, lead)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	a.scrubs = append(a.scrubs, secret)
	if row != nil {
		a.scrubs = append(a.scrubs, row.Secret)
	}
	cutover := any(nil)
	if merged.Storage.Type == loki.StorageS3 {
		a.scrubs = append(a.scrubs, merged.Storage.S3.AccessKeyID)
		cutover = merged.Storage.S3.Cutover
	}
	config, creds, err := lokiRender(merged, secret)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	disk, diskCreds, credsHere, err := s.lokiDisk()
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	loaded, err := loki.Periods(disk)
	if err == nil {
		var next []loki.Period
		if next, err = loki.Periods(config); err == nil {
			err = loki.CheckPeriods(loaded, next, s.lokiNow(), lead, true)
		}
	}
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	changed := loki.Changed(row, merged, secret)
	if disk == config && credsHere == (creds != "") && diskCreds == creds && len(changed) == 0 {
		return a.end(phaseRender, true, "nothing to change")
	}
	a.step(phaseRender, true, "")

	// 3. verify. The .next files are removed on every exit. Once swapped they no longer exist; before
	// that, a failure or a cancel mid-verify leaves none behind.
	defer os.Remove(filepath.Join(s.opts.LokiDir, loki.ConfigFile+loki.NextSuffix))
	defer os.Remove(filepath.Join(s.opts.LokiDir, loki.CredentialsFile+loki.NextSuffix))
	if err := s.lokiWriteNext(config, creds); err != nil {
		return a.end(phaseVerify, false, err.Error())
	}
	res := runner.Run("docker run --rm --network none --volumes-from "+compose.Shq(a.container.ID+":ro")+" "+
		compose.Shq(a.container.Image)+" -config.file=/etc/loki/"+loki.ConfigFile+loki.NextSuffix+" -verify-config",
		exec.RunOptions{Label: "loki -verify-config"})
	if !res.OK {
		if out := strings.TrimSpace(res.Stderr); out != "" {
			a.sink.Emit(log.Error, out)
		}
		return a.end(phaseVerify, false, lastLine(res.Stderr, res.Code))
	}
	a.step(phaseVerify, true, "")
	// The last point where a cancel stops the job with nothing changed. From the commit on, the work
	// ignores ctx.
	if ctx.Err() != nil {
		return a.outcome()
	}

	// 4. commit: the save and, in the same statement, the row it replaces.
	if err := loki.Save(s.store, merged, secret, row); err != nil {
		return a.end(phaseCommit, false, "could not record the save: "+err.Error())
	}
	a.step(phaseCommit, true, "")

	// 5. swap
	postCtx, cancel := context.WithTimeout(context.Background(), 2*lead)
	defer cancel()
	post := s.lokiRunner(postCtx)
	if err := s.lokiSwap(creds != ""); err != nil {
		return a.end(phaseSwap, false, err.Error())
	}
	a.step(phaseSwap, true, "")

	// 6. restart Loki only, through the control restart path.
	restartedAt := time.Now()
	if _, err := inspect.RestartControlService(post, lokiService); err != nil {
		return a.end(phaseRestart, false, err.Error())
	}
	a.step(phaseRestart, true, "")

	// 7. ready
	if !a.ready(post) {
		return a.end(phaseReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
	}
	a.step(phaseReady, true, "")

	// 8. finish
	if err := loki.Finish(s.store); err != nil {
		return a.end(phaseFinish, false, "could not record the finished apply")
	}
	a.step(phaseFinish, true, "")

	// 9. log: what Loki has said since the restart. Non-fatal (invariant 1's convention).
	logs := post.Run("docker logs --since "+compose.Shq(restartedAt.UTC().Format(time.RFC3339))+" "+compose.Shq(a.container.ID),
		exec.RunOptions{Label: "docker logs"})
	errs := []string{}
	for _, l := range strings.Split(logs.Stdout+"\n"+logs.Stderr, "\n") {
		if strings.Contains(l, "level=error") {
			errs = append(errs, strings.TrimSpace(l))
		}
	}
	if len(errs) > 0 {
		a.sink.Emit(log.Warn, strings.Join(errs, "\n"))
		a.step(phaseLog, true, "non-fatal: Loki logged "+strconv.Itoa(len(errs))+" errors after restart: "+js.Truncate(errs[0], 300))
	}

	// 10. the event, outside every mutex (rule 14). A job that changed no row (every boot apply) has
	// nothing to announce.
	if len(changed) > 0 {
		s.bus.Emit("logging.changed", jsonx.O("by", by, "job", <-a.idc, "changed", changed,
			"storage", merged.Storage.Type, "cutover", cutover, "retentionDays", merged.RetentionDays))
	}
	return a.outcome()
}

// ready polls Loki's own health check every lokiPoll until it answers or LokiReadyTimeoutMs has
// passed. exec is the only way to ask, because pstack is not on the logs network. It uses the wall
// clock, never lokiNow.
func (a *lokiApply) ready(post exec.Runner) bool {
	cmd := "docker exec " + compose.Shq(a.container.ID) + " /usr/bin/loki -health"
	deadline := time.Now().Add(time.Duration(a.s.opts.LokiReadyTimeoutMs) * time.Millisecond)
	for {
		if post.Run(cmd, exec.RunOptions{Label: "loki -health"}).OK {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(a.s.lokiPoll)
	}
}

// step records one step.
func (a *lokiApply) step(phase stack.Phase, ok bool, message string) {
	r := stack.StepResult{Axis: "loki", Phase: phase, OK: ok}
	if message != "" {
		r.Message = &message
	}
	a.steps = append(a.steps, r)
}

// end records the step that ends the job and returns the outcome.
func (a *lokiApply) end(phase stack.Phase, ok bool, message string) stack.Outcome {
	a.step(phase, ok, message)
	return a.outcome()
}

// outcome is ok when every recorded step is.
func (a *lokiApply) outcome() stack.Outcome {
	o := stack.Outcome{OK: true, Steps: a.steps}
	for _, r := range a.steps {
		o.OK = o.OK && r.OK
	}
	return o
}

// lastLine returns a failed command's last non-empty stderr line, which is where Loki says why,
// capped like every step message. When stderr is empty it returns the exit code.
func lastLine(stderr string, code int) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return js.Truncate(l, 300)
	}
	return "exit " + strconv.Itoa(code)
}
```

- [ ] **Step 4: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiApply -v
```

Expected:
- `--- PASS` for all seven tests: `TestLokiApplyRunsTheStepsInOrder`, `TestLokiApplyFindsTheLokiContainer`, `TestLokiApplyVerifyFailureChangesNothing`, `TestLokiApplyRowWriteFailureRestartsNothing`, `TestLokiApplySavesTheRowBeforeTheRestart`, `TestLokiApplyCarriesASupersededSave` and `TestLokiApplyWhoseSaveWasReplacedChangesNothing`.
- Then `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/api`.
- No `WARNING: DATA RACE` anywhere in the output.

Then run the whole package and the static checks:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ && go vet ./internal/api/ && gofmt -l internal/api
```

Expected: `ok`, and nothing printed by vet or gofmt.

- [ ] **Step 5: Run every negative control**

Apply each mutation to `loki_apply.go` on its own. Run `go test -race -timeout 120s ./internal/api/ -run <Test>` and confirm it fails with the symptom in the table, then undo the edit. To leave the tree untouched, use `go test -overlay` instead.

| Mutation | Test | Expected failure |
|---|---|---|
| Move the `inspect.RestartControlService(post, lokiService)` block above `res := runner.Run("docker run …` | `TestLokiApplyRunsTheStepsInOrder` | `command 2 = "docker ps -aq …"` |
| Delete the `s.bus.Emit("logging.changed", …)` call | `TestLokiApplyRunsTheStepsInOrder` | `logging.changed: got 0 events` |
| Delete `a.sink.Emit(log.Info, "by "+by)` | `TestLokiApplyRunsTheStepsInOrder` | ``the transcript's first line must be `by alice` `` |
| Change `if !found && a.boot { return a.end(phaseFind, true, …` to `false` | `TestLokiApplyFindsTheLokiContainer` | the boot case reports `find false "no loki container"` |
| Move `c, sp, by := s.lokiTake(a.g)`, its `if by == ""` and the `by` Emit to just after `a.step(phaseFind, true, "")` | `TestLokiApplyFindsTheLokiContainer` | `a failed job must not leave its save pending` |
| Move the `loki.Save` block above `res := runner.Run("docker run …` | `TestLokiApplyVerifyFailureChangesNothing` | `the row must stay empty` |
| Delete both `defer os.Remove(...)` lines in `run` | `TestLokiApplyVerifyFailureChangesNothing` | `config.yaml.next is left behind` |
| Replace the commit block with `_ = loki.Save(s.store, merged, secret, row)` | `TestLokiApplyRowWriteFailureRestartsNothing` | `"finish"` instead of `"commit"`, or `a failed commit must not restart Loki` |
| Move the commit block below `a.step(phaseReady, true, "")` | `TestLokiApplySavesTheRowBeforeTheRestart` | `at the restart the row must hold the save` |
| In `lokiTake`, change both `e.gen <= g` to `e.gen == g` | `TestLokiApplyCarriesASupersededSave` | `the successor must apply both saves` |
| Delete `a.scrubs = append(a.scrubs, secret)` | `TestLokiApplyCarriesASupersededSave` | `the S3 secret reached the job record` |
| In `lokiTake`, delete `&& e.gen <= g` from both branches | `TestLokiApplyWhoseSaveWasReplacedChangesNothing` | `"ok" finish true ""` instead of `nothing to change` |

- [ ] **Step 6: Document the apply**

1. Check that slice-1 T8 has landed:

   ```
   grep -n "^## 5g\|^## 6\." /Volumes/S1/code/preview-stacks/docs/control-plane.md
   ```

   `## 5g. Loki logging` must come before `## 6. Submitting a deployment`.
2. In `docs/control-plane.md`, insert this text right before `## 6. Submitting a deployment`, followed by one blank line:

```markdown
### The apply

A save is a `loki-apply` job on the `pstack-control` key: one runs, one waits, each counts against
`PSTACK_MAX_JOBS`. A boot apply is the same job. Its transcript opens with `by <actor>`.

| Step | What |
|---|---|
| `find` | The `loki` container's id and image. None: a save fails, a boot apply is `ok`. |
| `render` | The row, the pending patches over it, every save rule again, then the period guard. Files and row already equal: `ok`, nothing to change. |
| `verify` | `config.yaml.next` (0644) and `s3-credentials.next` (0600), then `-verify-config` in `docker run --rm --network none --volumes-from <loki>:ro <loki's image>`. |
| `commit` | The row, with `previous_*` set to the row it replaces. |
| `swap` | Credentials, then config, by rename. No S3: `s3-credentials` removed. |
| `restart` | `loki` only, through the control restart path. |
| `ready` | `docker exec <loki> /usr/bin/loki -health` until `PSTACK_LOKI_READY_TIMEOUT_MS`. |
| `finish` | `previous_*` cleared. |
| `log` | `level=error` lines since the restart: a non-fatal step. |

Then `logging.changed`, when the row changed. Before the swap nothing live changes: a failure
removes the `.next` files and leaves the row.

**Pending patches.** A PUT stores its section's patch with a generation, then starts a job holding
only that number. The job takes every patch at or below it. A superseded job's patch goes to its
successor; a running job never takes a newer one.

**The post runner.** From the swap on, commands run on a runner with its own deadline (2 × lead),
not the job's. A cancel after the swap still restarts and waits.

```

- [ ] **Step 7: Commit**

```
cd /Volumes/S1/code/preview-stacks && but status -fv
```

Before committing:
1. Note the file IDs of `packages/pstack/internal/api/loki_apply.go`, `packages/pstack/internal/api/loki_apply_test.go` and `docs/control-plane.md`.
2. Confirm that `claude/loki-logging-settings` is stacked on `claude/loki-logging`.
3. Leave out the uncommitted `packages/conformance/golden/host/db/*` files and anything under `.superpowers/`.

Then run:

```
but commit -b claude/loki-logging-settings -m "feat(api): the loki-apply job applies Loki's settings" <loki_apply.go id> <loki_apply_test.go id> <control-plane.md id>
```

---

### Task 10: api: rollback, leave-in-place, and cancellation after the swap

**Files:**
- Modify: `packages/pstack/internal/api/loki_apply.go`. Replace T9's three failure returns in `(a *lokiApply) run`, each quoted in Step 3, and append `(a *lokiApply) rollback` at the end of the file. Nothing else in T9 changes. `post`, `row` and every helper `rollback` calls already exist.
- Modify: `docs/control-plane.md`. Insert `### Rollback` immediately before the heading `## 6. Submitting a deployment`, which is after T9's `### The apply`. Line numbers are advisory because slices 1 and 2 are moving them.
- Test: `packages/pstack/internal/api/loki_apply_test.go`. Append one runner type and one test. The test reuses T9's helpers and needs no new import.

**Interfaces:**
- Consumes:
  - From T9, `packages/pstack/internal/api/loki_apply.go`, as the reviewer amended it:
    - `type lokiApply struct { s *Server; …; sink log.Sink; container inspect.ControlContainer; steps []stack.StepResult }` and its methods `step(phase stack.Phase, ok bool, message string)`, `end(phase stack.Phase, ok bool, message string) stack.Outcome`, `outcome() stack.Outcome` and `ready(post exec.Runner) bool`. `ready` is the only ready loop: it polls `docker exec <id> /usr/bin/loki -health` through `post` every `s.lokiPoll`, on the wall clock, up to `LokiReadyTimeoutMs`.
    - `func lokiRender(set loki.Settings, secret string) (config, creds string, err error)`
    - `func (s *Server) lokiDisk() (config, creds string, credsHere bool, err error)`
    - `func (s *Server) lokiWriteNext(config, creds string) error`. It removes both `.next` files on error.
    - `func (s *Server) lokiSwap(withCreds bool) error`. It renames credentials then config. With `!withCreds` it removes `s3-credentials`, and a file that is already absent is not an error.
    - `const lokiService = "loki"`, `func (s *Server) lokiPut(by string, c *loki.ChunksPatch, sp *loki.StoragePatch) uint64` and `func (s *Server) startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool)`
    - In `run`: the locals `ctx`, `lead`, `row` (step 2's `loki.Read`) and `post`, which is `s.lokiRunner(postCtx)` with `postCtx, cancel := context.WithTimeout(context.Background(), 2*lead)`. Also the three failure returns after the commit (swap, restart, ready).
    - Test helpers: `const lokiInspect`, `func waitLokiJob(t, s, id) jobs.Job`, `func lastStep(t, j) (phase string, ok bool, message string)`, `func readLoki(t, s, name) (string, bool)`, `func assertNoNext(t, s)`, `func retention(days int) *loki.ChunksPatch`
  - From T8: `Options.LokiDir`, `Options.LokiReadyTimeoutMs`, `Options.LokiUID` (New defaults `<= 0`), `s.lokiRunner func(ctx context.Context) exec.Runner`, `s.lokiPoll`, `s.lokiNow func() time.Time`, `func (s *Server) lokiLead() time.Duration`, `s.store`.
  - From T2-T4 (`internal/loki`): `Defaults`, `Render`, `EarliestCutover`, `Row`, `Read`, `Save`, `Finish`, `Revert`, `Period`, `Periods(configYAML string) ([]Period, error)`, `CheckPeriods(loaded, next []Period, now time.Time, lead time.Duration, adding bool) error`, `ChunksPatch`, `StoragePatch`, `Storage`, `S3`, `StorageS3`, `ConfigFile`, `CredentialsFile`.
  - Existing:
    - `inspect.RestartControlService(r exec.Runner, service string) (string, error)` (`inspect/control.go:95-113`)
    - `(*jobs.Registry).List() []Job` (`jobs/jobs.go:339`) and `Cancel(id, by string) bool` (`jobs/jobs.go:407-428`)
    - `jobs.Failed`/`jobs.Cancelled` (`jobs/jobs.go:144-146`). The registry records `cancelled` whatever the outcome (`jobs/jobs.go:709-731`).
    - `exec.NewFake`, `Fake.Answer`, `Fake.Commands` (`exec/exec.go:257-305`). `Run` records the command, then answers.
    - The real runner refuses after a cancel (`exec/exec.go:110-112`).
    - `scheduler.FormatDuration` (`scheduler/scheduler.go:39-56`; 50 ms prints `0s`)
    - `stack.Outcome`/`stack.StepResult` (`stack/stack.go:38-55`)
- Produces:
  - `func (a *lokiApply) rollback(post exec.Runner, row *loki.Row) stack.Outcome`. `row` is the row the apply REPLACED: nil means the table was empty, so previous is `Defaults()` with secret `""`. The function records one step through `a.end`, `{Axis: "loki", Phase: "rollback", OK: false, Message}`, and returns `a.outcome()`. The message is one of:
    - `rolled back`: rule 1 passed. The `Render(previous)` files are back, with `s3-credentials` removed when previous has no S3. Then `loki.Revert`, a restart and ready.
    - `rollback did not come up`: the rollback's restart or ready wait failed after the files and row were reverted. A restart error is also written to the sink as `rollback: <err>` at `log.Error`.
    - `not ready — S3 from <date> starts too soon to undo; left in place`: rule 1 refused. `loki.Finish` ran, the files stay, and Loki is not restarted.
    - `could not record the finished apply`: rule 1 refused and `loki.Finish` failed.
    - `rollback failed: <err>`: render, `lokiDisk`, `Periods`, `lokiWriteNext`, `lokiSwap` or `loki.Revert` failed. `previous_*` stays set, so the next job resumes.
  - T9's swap, restart and ready failures now return `a.rollback(post, row)` after recording their own failed step.
  - T11: resume calls `a.rollback(post, row.Previous)`, which is the row being returned to, never the in-flight save. `loki.Revert` reads `previous_*` from the database, not from this argument.

- [ ] **Step 0: Preconditions.** T9 must be committed with the reviewer's helpers.

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack/internal/api && grep -nF -e 'func (a *lokiApply) ready(post exec.Runner) bool' -e 'func lokiRender(' -e 'func (s *Server) lokiDisk(' -e 'func (s *Server) lokiWriteNext(' -e 'func (s *Server) lokiSwap(' -e 'context.WithTimeout(context.Background(), 2*lead)' -e 'func (a *lokiApply) rollback' loki_apply.go; grep -nE 'return a\.end\([A-Za-z]*(Swap|Restart|Ready), false' loki_apply.go; grep -nF -e 'const lokiInspect' -e 'func waitLokiJob(' -e 'func lastStep(' -e 'func readLoki(' -e 'func assertNoNext(' -e 'func retention(' loki_apply_test.go; grep -n '^### The apply\|^## 6\. Submitting a deployment' /Volumes/S1/code/preview-stacks/docs/control-plane.md
```

Expected:
- One line each for `ready`, `lokiRender`, `lokiDisk`, `lokiWriteNext`, `lokiSwap` and the `post` context, and no `rollback` line.
- Exactly three `return a.end(` lines, in this order: swap `err.Error()`, restart `err.Error()`, ready `"not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs)`.
- The six test helpers.
- `### The apply` above `## 6. Submitting a deployment`.

Then read `lokiSwap`'s body. With `withCreds == false`, an absent `s3-credentials` must give nil. Subtest 3 below rolls back a filesystem host that never had the file.

If T9 renamed a helper or its phase consts, use T9's names in Steps 1-5 and change nothing else. The phase const `lokiRender` and `func lokiRender` cannot both exist, so one of them was renamed. If a signature differs, or there is no `post` built on `context.Background()`, stop and reconcile with T9.

- [ ] **Step 1: Write the failing test.** Append to `packages/pstack/internal/api/loki_apply_test.go`:

```go
// ── rollback, leave-in-place, and a cancel after the swap ─────────────────────────────────────────

// cancellableFake refuses every command once its context is done, as the real runner does
// (exec.go:110-112), and records none it refuses. The job's runner and post both come from
// s.lokiRunner, so only this tells their two contexts apart.
type cancellableFake struct {
	*exec.Fake
	ctx context.Context
}

func (c cancellableFake) Run(cmd string, o exec.RunOptions) exec.Result {
	if c.ctx.Err() != nil {
		return exec.Result{OK: false, Code: 130, Stderr: "cancelled"}
	}
	return c.Fake.Run(cmd, o)
}

func (c cancellableFake) Context() context.Context { return c.ctx }

func TestLokiApplyRollback(t *testing.T) {
	// negative control: make rollback's first statement `return a.end("rollback", false, "rolled back")`
	// — the first three subtests fail.
	const (
		restart = "docker restart 'l1'"
		health  = "docker exec 'l1' /usr/bin/loki -health"
	)
	// host differs from lokiFixture in two ways. config.yaml is written after New, so a boot reconcile
	// never sees it, and a failed ready wait lasts 50ms, which keeps lead at about 10m. ready says whether
	// -health answers after n restarts. onRestart runs inside the `docker restart` answer.
	host := func(t *testing.T, config string, ready func(n int) bool, onRestart func(s *Server)) (*Server, *exec.Fake) {
		t.Helper()
		dir := t.TempDir()
		// LokiUID is this process's euid, so loki.WriteFile writes s3-credentials 0600 without root.
		s, err := New(Options{DataDir: t.TempDir(), LokiDir: dir, LokiReadyTimeoutMs: 50, LokiUID: os.Geteuid(), Bus: events.New(), Log: func(string) {}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Stop)
		if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		restarts := 0 // only the job's goroutine touches it
		f := exec.NewFake(nil, "")
		f.Answer = func(cmd string) (exec.Result, bool) {
			switch c := strings.TrimSpace(cmd); {
			case strings.HasPrefix(c, "docker ps -aq"):
				return exec.Result{OK: true, Stdout: "l1\n"}, true
			case strings.HasPrefix(c, "docker inspect"):
				return exec.Result{OK: true, Stdout: lokiInspect}, true
			case c == restart:
				restarts++
				if onRestart != nil {
					onRestart(s)
				}
				return exec.Result{OK: true, Stdout: "l1\n"}, true
			case c == health:
				if ready(restarts) {
					return exec.Result{OK: true, Stdout: "ready\n"}, true
				}
				return exec.Result{OK: false, Code: 1, Stderr: "Ingester not ready"}, true
			}
			return exec.Result{OK: true}, true // -verify-config, docker logs
		}
		s.lokiRunner = func(ctx context.Context) exec.Runner { return cancellableFake{f, ctx} }
		s.lokiPoll = time.Millisecond
		return s, f
	}
	apply := func(t *testing.T, s *Server, c *loki.ChunksPatch, sp *loki.StoragePatch) jobs.Job {
		t.Helper()
		job, ok := s.startLokiApply(s.lokiPut("alice", c, sp), "alice", false)
		if !ok {
			t.Fatal("the apply was refused")
		}
		return waitLokiJob(t, s, job.ID)
	}
	count := func(f *exec.Fake, want string) int {
		n := 0
		for _, c := range f.Commands() {
			if c == want {
				n++
			}
		}
		return n
	}
	s3 := func(cutover string) *loki.StoragePatch {
		return &loki.StoragePatch{Storage: loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
			Endpoint: "https://s3.example.com", Region: "eu-central-1", Bucket: "pstack-logs",
			AccessKeyID: "AKIAEXAMPLE0001", Cutover: cutover,
		}}, Secret: "s3cret-access-key-0001"}
	}

	t.Run("not ready: the previous files and row come back, and Loki restarts on them", func(t *testing.T) {
		// negative control: delete rollback's `if err == nil { err = loki.Revert(s.store) }` — the row
		// still holds the S3 save. (Also run: `s.lokiSwap(creds != "")` → `s.lokiSwap(true)` — the
		// message is `rollback failed: rename …` and s3-credentials is left behind.)
		s, f := host(t, pstack.LokiConfig, func(n int) bool { return n >= 2 }, nil)
		// The real clock throughout: this cutover passes Validate and rule 2 and is not live for rule 1.
		j := apply(t, s, nil, s3(loki.EarliestCutover(time.Now(), s.lokiLead())))
		if phase, ok, msg := lastStep(t, j); j.State != jobs.Failed || phase != "rollback" || ok || msg != "rolled back" {
			t.Errorf("state %q, last step %s %v %q", j.State, phase, ok, msg)
		}
		if cfg, _ := readLoki(t, s, loki.ConfigFile); cfg != pstack.LokiConfig {
			t.Errorf("config.yaml was not restored:\n%s", cfg)
		}
		if _, here := readLoki(t, s, loki.CredentialsFile); here {
			t.Error("s3-credentials is left behind")
		}
		assertNoNext(t, s)
		if row, err := loki.Read(s.store); err != nil || row != nil {
			t.Errorf("the table was empty before the save, so it is empty again: %+v, %v", row, err)
		}
		if n := count(f, restart); n != 2 {
			t.Errorf("restarts: %d, want 2 (the apply, then the rollback)", n)
		}
	})

	t.Run("not ready, and undoing would drop an S3 period starting within lead: left in place", func(t *testing.T) {
		// negative control: in rollback pass `0` instead of `s.lokiLead()` to CheckPeriods (now, not
		// now+lead) — it rolls back and restarts twice.
		late := false // set and read on the job's goroutine only
		s, f := host(t, pstack.LokiConfig, func(int) bool { return false }, func(*Server) { late = true })
		cutover := loki.EarliestCutover(time.Now(), s.lokiLead())
		at, err := time.Parse(time.DateOnly, cutover)
		if err != nil {
			t.Fatal(err)
		}
		// Step 2 sees the cutover an hour out, so rule 2 passes. From the restart on, it is 5 minutes
		// out, inside lead, so rule 1 refuses the undo. Validate's floor reads the real clock, which is
		// what the cutover was computed from.
		s.lokiNow = func() time.Time {
			if late {
				return at.Add(-5 * time.Minute)
			}
			return at.Add(-time.Hour)
		}
		j := apply(t, s, nil, s3(cutover))
		want := "not ready — S3 from " + cutover + " starts too soon to undo; left in place"
		if phase, _, msg := lastStep(t, j); j.State != jobs.Failed || phase != "rollback" || msg != want {
			t.Errorf("state %q, last step %s %q", j.State, phase, msg)
		}
		if cfg, _ := readLoki(t, s, loki.ConfigFile); !strings.Contains(cfg, "object_store: s3") {
			t.Errorf("the S3 period must stay:\n%s", cfg)
		}
		if _, here := readLoki(t, s, loki.CredentialsFile); !here {
			t.Error("s3-credentials must stay")
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.Settings.Storage.Type != loki.StorageS3 || row.InFlight {
			t.Errorf("the save stays, with previous_* cleared: %+v, %v", row, err)
		}
		if n := count(f, restart); n != 1 {
			t.Errorf("restarts: %d, want 1", n)
		}
	})

	t.Run("the rollback's own ready wait fails: rollback did not come up, with the row already reverted", func(t *testing.T) {
		// negative control: delete rollback's `if !a.ready(post) { … }` block — it says `rolled back`.
		s14 := loki.Defaults()
		s14.RetentionDays = 14
		before, err := loki.Render(s14)
		if err != nil {
			t.Fatal(err)
		}
		s, f := host(t, before, func(int) bool { return false }, nil)
		if err := loki.Save(s.store, s14, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := loki.Finish(s.store); err != nil {
			t.Fatal(err)
		}
		j := apply(t, s, retention(30), nil)
		if phase, _, msg := lastStep(t, j); j.State != jobs.Failed || phase != "rollback" || msg != "rollback did not come up" {
			t.Errorf("state %q, last step %s %q", j.State, phase, msg)
		}
		if cfg, _ := readLoki(t, s, loki.ConfigFile); cfg != before {
			t.Errorf("config.yaml must be the 14-day render again:\n%s", cfg)
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.Settings.RetentionDays != 14 || row.InFlight {
			t.Errorf("the row is reverted before the restart: %+v, %v", row, err)
		}
		if n := count(f, restart); n != 2 {
			t.Errorf("restarts: %d, want 2", n)
		}
	})

	t.Run("a cancel after the swap: Loki still restarts, answers ready, and the apply finishes", func(t *testing.T) {
		// negative control: in run, change `context.WithTimeout(context.Background(), 2*lead)` to
		// `context.WithTimeout(ctx, 2*lead)` — the -health after the restart is refused and never
		// recorded, and the rollback's own restart is refused too.
		s, f := host(t, pstack.LokiConfig, func(int) bool { return true }, func(s *Server) {
			for _, j := range s.jobs.List() {
				if j.Action == jobs.LokiApply && !j.State.Terminal() {
					s.jobs.Cancel(j.ID, "alice")
				}
			}
		})
		j := apply(t, s, retention(14), nil)
		if j.State != jobs.Cancelled {
			t.Errorf("state %q, want cancelled", j.State)
		}
		if cmds := strings.Join(f.Commands(), "\n"); !strings.Contains(cmds, restart+"\n"+health) {
			t.Errorf("no -health after the restart:\n%s", cmds)
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.Settings.RetentionDays != 14 || row.InFlight {
			t.Errorf("the apply must finish: %+v, %v", row, err)
		}
		if cfg, _ := readLoki(t, s, loki.ConfigFile); !strings.Contains(cfg, "  retention_period: 336h") {
			t.Errorf("config.yaml:\n%s", cfg)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiApplyRollback -v 2>&1 | grep -E '^\s*(--- |ok|FAIL|PASS)|loki_apply_test.go'
```

Expected against T9, which ends each failure at its own step and has no rollback yet. The test compiles, because it uses only T9's names.
- `not_ready:_the_previous_files_and_row_come_back…` FAILS with:
  - `state "failed", last step ready false "not ready within 0s"`
  - `config.yaml was not restored`
  - `s3-credentials is left behind`
  - `the table was empty before the save, so it is empty again`, because the row holds S3 and is InFlight
  - `restarts: 1, want 2`
- `not_ready,_and_undoing_would_drop…` FAILS with `last step ready "not ready within 0s"` and `the save stays, with previous_* cleared` (InFlight is still true).
- `the_rollback's_own_ready_wait_fails…` FAILS on the last step, on `config.yaml must be the 14-day render again` (the file holds `720h`), on the row (30 days, InFlight) and on `restarts: 1, want 2`.
- `a_cancel_after_the_swap…` PASSES. T9 already builds `post` on a background context, and this subtest guards that; Step 5 runs its negative control.
- The run ends `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`.

- [ ] **Step 3: Implement.** Three edits in `packages/pstack/internal/api/loki_apply.go`, then one append.

3a. In `run`'s swap block, replace:

```go
		return a.end(lokiSwap, false, err.Error())
```

with:

```go
		a.step(lokiSwap, false, err.Error())
		return a.rollback(post, row)
```

3b. In `run`'s restart block, replace:

```go
		return a.end(lokiRestart, false, err.Error())
```

with:

```go
		a.step(lokiRestart, false, err.Error())
		return a.rollback(post, row)
```

3c. In `run`'s ready block, replace:

```go
		return a.end(lokiReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
```

with:

```go
		a.step(lokiReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
		return a.rollback(post, row)
```

3d. Append at the end of the file:

```go

// rollback undoes an apply whose swap, restart or ready wait failed after the commit. row is the row
// the apply replaced; nil means the table was empty, so the defaults. The files Render(previous) gives
// go back (an s3-credentials the save created is removed), the row goes back (loki.Revert reads
// previous_*), and Loki restarts on them.
//
// Rule 1 first, against the files on disk, never the row: when undoing would drop a period starting
// at or before now+lead, the save stays, previous_* is cleared, and Loki is left to come up on it.
//
// Every command runs on post, never the job's runner, so a cancel cannot strand swapped files. An
// error before the revert leaves previous_* set, and the next job resumes.
func (a *lokiApply) rollback(post exec.Runner, row *loki.Row) stack.Outcome {
	s := a.s
	done := func(message string) stack.Outcome { return a.end("rollback", false, message) }
	prev, secret := loki.Defaults(), ""
	if row != nil {
		prev, secret = row.Settings, row.Secret
	}
	config, creds, err := lokiRender(prev, secret)
	disk := ""
	if err == nil {
		disk, _, _, err = s.lokiDisk()
	}
	var loaded, next []loki.Period
	if err == nil {
		loaded, err = loki.Periods(disk)
	}
	if err == nil {
		next, err = loki.Periods(config)
	}
	if err != nil {
		return done("rollback failed: " + err.Error())
	}
	if loki.CheckPeriods(loaded, next, s.lokiNow(), s.lokiLead(), false) != nil {
		// The refused period: the first on-disk one the previous config does not carry at its index.
		from := ""
		for i, p := range loaded {
			if i >= len(next) || next[i] != p {
				from = p.From
				break
			}
		}
		if loki.Finish(s.store) != nil {
			return done("could not record the finished apply")
		}
		return done("not ready — S3 from " + from + " starts too soon to undo; left in place")
	}
	err = s.lokiWriteNext(config, creds)
	if err == nil {
		err = s.lokiSwap(creds != "")
	}
	if err == nil {
		err = loki.Revert(s.store)
	}
	if err != nil {
		return done("rollback failed: " + err.Error())
	}
	if _, err := inspect.RestartControlService(post, lokiService); err != nil {
		a.sink.Emit(log.Error, "rollback: "+err.Error())
		return done("rollback did not come up")
	}
	if !a.ready(post) {
		return done("rollback did not come up")
	}
	return done("rolled back")
}
```

`rollback` uses `exec`, `inspect`, `log`, `loki` and `stack`, which T9 already imports. `scheduler` is still used by 3c. The `"rollback"` phase is an untyped constant passed to `a.end`, so T9's phase const block does not change.

- [ ] **Step 4: Run it and watch it pass.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiApplyRollback -v 2>&1 | grep -E '^\s*(--- |ok|FAIL|PASS)'
```

Expected: `--- PASS: TestLokiApplyRollback` with all four subtests `--- PASS`, then `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`. There must be no `WARNING: DATA RACE`.

Then run the whole package, T9's tests included, plus the static checks:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ ./internal/loki/ && go vet ./internal/api/ && gofmt -l internal/api
```

Expected: two `ok` lines. vet and gofmt print nothing.

- [ ] **Step 5: Run every negative control** (AGENTS.md rule 17). Make each mutation on its own and run the command below. Confirm the named subtest FAILS with the symptom shown, then undo the mutation.

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiApplyRollback
```

| # | Mutation | Fails | Symptom |
|---|---|---|---|
| 1 | In `rollback`, delete `if err == nil { err = loki.Revert(s.store) }` | `not_ready:_the_previous_files…` | `the table was empty before the save, so it is empty again` |
| 2 | In `rollback`, `s.lokiSwap(creds != "")` → `s.lokiSwap(true)` | `not_ready:_the_previous_files…` | last step `"rollback failed: rename …"`, `s3-credentials is left behind` |
| 3 | In `rollback`, `s.lokiLead(), false)` → `0, false)` | `not_ready,_and_undoing_would_drop…` | last step `"rolled back"`, `the S3 period must stay`, `restarts: 2, want 1` |
| 4 | In `rollback`, delete `if !a.ready(post) { return done("rollback did not come up") }` | `the_rollback's_own_ready_wait_fails…` | last step `"rolled back"` |
| 5 | Make `rollback`'s first statement `return a.end("rollback", false, "rolled back")` | subtests 1-3 (the parent's control) | `config.yaml was not restored`; the left-in-place and `rollback did not come up` messages are missing |
| 6 | In T9's `run`, `context.WithTimeout(context.Background(), 2*lead)` → `context.WithTimeout(ctx, 2*lead)` | `a_cancel_after_the_swap…` | `no -health after the restart`, `the apply must finish: <nil>` |

After the last undo, rerun both Step 4 commands. Both must be green.

- [ ] **Step 6: Document it.** In `docs/control-plane.md`, replace the heading line:

```markdown
## 6. Submitting a deployment
```

with:

```markdown
### Rollback

A failed swap, restart or ready wait after the commit undoes the apply, through `post`.

| Fails at | Files, row, Loki | Job |
|---|---|---|
| swap, restart, ready | `Render(previous)` back; `s3-credentials` removed if previous had no S3; row reverted; Loki restarted | `failed: rolled back` |
| …and undoing drops a period starting within `lead` (rule 1, files on disk) | new files and save kept; `previous_*` cleared; no second restart | `failed: not ready — S3 from <date> starts too soon to undo; left in place` |
| the rollback's restart or ready | previous files and row; Loki down | `failed: rollback did not come up` |
| the rollback's render, read, write or revert | `previous_*` set | `failed: rollback failed: <error>`; the next job resumes |
| finish | new config live; `previous_*` set | `failed: could not record the finished apply`; the next job resumes |

**Cancel.** Before the commit, a cancel stops the job with nothing changed. After it, the work ignores
the cancel: it restarts, waits, and finishes or rolls back. The job still reads `cancelled`.

## 6. Submitting a deployment
```

T11 inserts its `Who owns config.yaml`, `Resume` and `Boot` paragraphs at the same anchor, after this subsection.

- [ ] **Step 7: Commit.**

```bash
cd /Volumes/S1/code/preview-stacks && but status -fv
```

Confirm that `claude/loki-logging-settings` is stacked on `claude/loki-logging`. Note the IDs of `packages/pstack/internal/api/loki_apply.go`, `packages/pstack/internal/api/loki_apply_test.go` and `docs/control-plane.md`. Leave out anything under `packages/conformance/golden/host/db/` or `.superpowers/`.

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-settings -m "feat(api): roll back a Loki apply that does not come up" <loki_apply.go id> <loki_apply_test.go id> <control-plane.md id>
```

---

### Task 11: api: resume and the boot reconcile

**Files:**
- Modify: `packages/pstack/internal/api/loki_apply.go`. T8 created it, T9 filled it in and T10 extended it. The anchors below are quoted from T9's `run`: the step-2 row read, and the step-3 verify block.
- Modify: `packages/pstack/internal/api/server.go`. Anchor: `s.reconcileDomains()` followed by `return s, nil` (server.go:306-307 today).
- Modify: `docs/control-plane.md`. Anchor: the heading `## 6. Submitting a deployment`. Slice-1 T8 and slice-2 T3, T5, T9 and T10 insert before it too, so this task's sections land after theirs.
- Test: `packages/pstack/internal/api/loki_apply_test.go`

**Interfaces:**
- Consumes:
  - T4: `loki.Row{Settings, Secret, UpdatedAt, InFlight, Previous *Row}`, `loki.Read(st) (*Row, error)` (nil when the table is empty), `loki.Save(st, s, secret, previous *Row) error`, `loki.Finish(st) error`, `loki.Revert(st) error`
  - T2: `loki.Defaults()`, `loki.Render`, `loki.Credentials`, `loki.CredentialsOwner(lokiUID) error`, `loki.ErrNeedsRoot`, `loki.ConfigFile`, `loki.CredentialsFile`, `loki.NextSuffix`, `loki.S3`, `loki.Storage`, `loki.StorageS3`. `Render` runs `check(s)` first, so a row with `retentionDays: 0` fails to render.
  - T3: `loki.Periods(configYAML) ([]Period, error)`, `loki.CheckPeriods(loaded, next, now, lead, adding) error`
  - T8: `s.opts.LokiDir`, `s.opts.LokiReadyTimeoutMs`, `s.opts.LokiUID` (resolved in New); `s.lokiRunner`, `s.lokiPoll`, `s.lokiNow`, `s.lokiLead()`
  - T9, as the reviewer fixed its names:
    - `const lokiService`; phase consts `lokiRender`, `lokiVerify`, `lokiCommit`, `lokiSwap`, `lokiRestart`, `lokiReady`, `lokiFinish`
    - `type lokiApply` with `s`, `scrubs`, `sink`, `container inspect.ControlContainer`, `steps`
    - `(a *lokiApply) run(ctx) stack.Outcome`, `step(phase, ok, message)`, `end(phase, ok, message) stack.Outcome`, `outcome() stack.Outcome`, `ready(post exec.Runner) bool`
    - `lastLine(stderr string, code int) string`; `s.startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool)`; `s.lokiPut`
    - `func lokiRenderFiles(set loki.Settings, secret string) (config, creds string, err error)`. The name is not `lokiRender`, which is already the phase const (see amendments).
    - `func (s *Server) lokiDisk() (config, creds string, credsHere bool, err error)`. It returns os.ReadFile's raw error for config.yaml; an absent s3-credentials gives credsHere=false and no error.
    - `func (s *Server) lokiWriteNext(config, creds string) error`, `func (s *Server) lokiSwap(withCreds bool) error`
    - T9's `run` locals `runner` and `row, err := loki.Read(s.store)`, and T9's transcript line `a.sink.Emit(log.Info, "by "+by)`
    - T9's test helpers: `lokiFixture(t, override) (*Server, *exec.Fake)` (LokiReadyTimeoutMs 300, LokiUID os.Geteuid(), slice 1's config.yaml written before New), `lokiInspect` (StartedAt `2026-09-15T10:00:00Z`), `lokiPS`, `waitLokiJob`, `readLoki`, `lastStep`, `retention`
  - T10: `func (a *lokiApply) rollback(post exec.Runner, row *loki.Row) stack.Outcome`. `row` is the state to go back to: nil means Defaults.
  - Existing: `inspect.RestartControlService` (control.go:95), `ContainerInfo.StartedAt *int64` in ms (inspect.go:72), `compose.Shq`, `scheduler.FormatDuration` (scheduler.go:39), `exec.Fake.Commands` (exec.go:305), `jobs.Registry.List` (jobs.go:339), `Job.Log []log.Event` with `.Message` (log.go:30), `store.Open(dataDir)` (store.go:64), `Store.DB` (store.go:57), `Store.Close` (store.go:145), `pstack.LokiConfig`. `redact.RedactText` skips values shorter than 8 bytes, so an empty scrub value is harmless (redact.go:156-160).
- Produces:
  - `const lokiBootBy = "pstack (boot)"`
  - `func (a *lokiApply) resume(runner exec.Runner, row *loki.Row) bool`. Its step messages: `unfinished apply` (render, ok), `swapped` (swap, ok), `resumed` (finish, ok), `cutover <date> passed while pstack was down — save again` (render, failed), `could not record the undo: <err>` (commit, failed), `could not record the finished apply` (finish, failed). On a failed swap, restart or ready it records T9's failure step, then calls `a.rollback(post, row.Previous)`.
  - `func (a *lokiApply) verify(runner exec.Runner, config, creds string) bool`, factored out of T9's step 3.
  - The resume hook in `run`, directly after step 2's row read.
  - `func (s *Server) reconcileLoki()`, called in `New` right after `s.reconcileDomains()`. Its log lines are prefixed `loki: `.
  - Test helpers: `lokiStarted`, `writeLoki`, `mustRender`, `lokiCount`, `lokiJobs`, `lokiS3`, `bootNew`.

- [ ] **Step 1: Write the failing resume tests**

Append to `packages/pstack/internal/api/loki_apply_test.go`. T9's import block already has `context`, `os`, `path/filepath`, `strings`, `sync`, `testing`, `time`, `pstack`, `events`, `exec`, `inspect`, `jobs`, `jsonx` and `loki`. Add `"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"`.

```go
// ── resume and boot (Task 11) ───────────────────────────────────────────────────────────────────

// lokiStarted is lokiInspect's State.StartedAt. A file whose mtime is later is one Loki has not loaded.
var lokiStarted = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

// writeLoki puts config.yaml, plus s3-credentials when creds is not "", in s's loki directory, both
// with mtime at.
func writeLoki(t *testing.T, s *Server, config, creds string, at time.Time) {
	t.Helper()
	files := map[string]string{loki.ConfigFile: config}
	if creds != "" {
		files[loki.CredentialsFile] = creds
	}
	for name, body := range files {
		path := filepath.Join(s.opts.LokiDir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func mustRender(t *testing.T, set loki.Settings) string {
	t.Helper()
	out, err := loki.Render(set)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lokiCount is how many recorded commands start with prefix.
func lokiCount(f *exec.Fake, prefix string) int {
	n := 0
	for _, c := range f.Commands() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func lokiJobs(s *Server) []jobs.Job {
	out := []jobs.Job{}
	for _, j := range s.jobs.List() {
		if j.Action == jobs.LokiApply {
			out = append(out, j)
		}
	}
	return out
}

// lokiS3 is an S3 save whose second period starts on cutover.
func lokiS3(cutover string) loki.Settings {
	set := loki.Defaults()
	set.Storage = loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint: "https://s3.example.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: "AKIAEXAMPLE", Cutover: cutover,
	}}
	return set
}

func TestLokiResume(t *testing.T) {
	// negative control: delete the `if row != nil && row.InFlight { … }` hook from run. No subtest sees
	// `unfinished apply`, and the too-close one ends ok instead of undone.
	retention14 := loki.Defaults()
	retention14.RetentionDays = 14
	boot := func(t *testing.T, s *Server) jobs.Job {
		t.Helper()
		job, ok := s.startLokiApply(0, "pstack (boot)", true)
		if !ok {
			t.Fatal("the apply was refused")
		}
		return waitLokiJob(t, s, job.ID)
	}
	resumed := func(t *testing.T, j jobs.Job, want jobs.State) {
		t.Helper()
		if j.State != want || !strings.Contains(string(jsonx.Must(j)), `"unfinished apply"`) {
			t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
		}
	}

	t.Run("files equal the row, Loki started before them: restart, ready, finish", func(t *testing.T) {
		// negative control: change `restart := !loaded` to `restart := false` in resume. There are 0
		// restarts, so Loki keeps the config from before the save.
		s, f := lokiFixture(t, nil)
		if err := loki.Save(s.store, retention14, "", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, mustRender(t, retention14), "", lokiStarted.Add(time.Minute))
		resumed(t, boot(t, s), jobs.OK)
		if n := lokiCount(f, "docker restart"); n != 1 {
			t.Errorf("%d docker restarts, want 1", n)
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 14 {
			t.Errorf("want the save kept and previous_* cleared: %+v (%v)", row, err)
		}
	})

	t.Run("files equal the row, Loki started after them: ready and finish, no restart", func(t *testing.T) {
		// negative control: change `restart := !loaded` to `restart := true` in resume. Loki already runs
		// the saved files and gets one needless restart.
		s, f := lokiFixture(t, nil)
		if err := loki.Save(s.store, retention14, "", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, mustRender(t, retention14), "", lokiStarted.Add(-time.Minute))
		resumed(t, boot(t, s), jobs.OK)
		if n := lokiCount(f, "docker restart"); n != 0 {
			t.Errorf("%d docker restarts, want 0", n)
		}
		if lokiCount(f, "docker exec") == 0 {
			t.Error("the resume must still wait for ready")
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.InFlight {
			t.Errorf("want previous_* cleared: %+v (%v)", row, err)
		}
	})

	t.Run("files that differ from the row are verified, swapped and restarted, whatever StartedAt says", func(t *testing.T) {
		// negative control: delete `restart = true` after the swap in resume. Loki started after the old
		// file's mtime, so it is never restarted onto the new one.
		s, f := lokiFixture(t, nil)
		if err := loki.Save(s.store, retention14, "", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, pstack.LokiConfig, "", lokiStarted.Add(-time.Minute))
		resumed(t, boot(t, s), jobs.OK)
		if n := lokiCount(f, "docker run --rm --network none"); n != 1 {
			t.Errorf("%d verify runs, want 1", n)
		}
		if n := lokiCount(f, "docker restart"); n != 1 {
			t.Errorf("%d docker restarts, want 1", n)
		}
		if config, _ := readLoki(t, s, loki.ConfigFile); !strings.Contains(config, "  retention_period: 336h") {
			t.Errorf("config.yaml must be the row's render:\n%s", config)
		}
	})

	t.Run("a cutover within 2×lead that Loki never loaded is undone: previous files, row reverted, no restart", func(t *testing.T) {
		// negative control: replace `running := beforeCfg` and its `if loaded` with `running := diskCfg`.
		// The guard passes, and the resume restarts Loki onto an S3 period that starts before a rollback
		// could finish.
		s, f := lokiFixture(t, nil)
		saved := lokiS3("2030-01-15")
		// lokiFixture's ready timeout is 300ms, so lead is 10m0.3s and now+2×lead is past midnight.
		s.lokiNow = func() time.Time { return time.Date(2030, 1, 14, 23, 50, 0, 0, time.UTC) }
		if err := loki.Save(s.store, saved, "s3cretKEY1", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, mustRender(t, saved), loki.Credentials("AKIAEXAMPLE", "s3cretKEY1"), lokiStarted.Add(time.Minute))
		j := boot(t, s)
		resumed(t, j, jobs.Failed)
		if phase, ok, msg := lastStep(t, j); phase != "render" || ok || msg != "cutover 2030-01-15 passed while pstack was down — save again" {
			t.Fatalf("last step %s %v %q", phase, ok, msg)
		}
		if n := lokiCount(f, "docker restart"); n != 0 {
			t.Errorf("%d docker restarts, want 0: Loki still runs the previous files", n)
		}
		if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
			t.Error("config.yaml must be the previous render, the defaults")
		}
		if _, here := readLoki(t, s, loki.CredentialsFile); here {
			t.Error("s3-credentials did not exist before the save")
		}
		if row, err := loki.Read(s.store); err != nil || row != nil {
			t.Errorf("the table was empty before the save: %+v (%v)", row, err)
		}
	})

	t.Run("a save's job resumes first, then applies its own save", func(t *testing.T) {
		// negative control: in the hook, replace the re-read `if row, err = loki.Read(s.store); err != nil
		// { … }` with `return a.outcome()`. config.yaml stays at 336h and the 21-day save is never applied.
		s, f := lokiFixture(t, nil)
		if err := loki.Save(s.store, retention14, "", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, mustRender(t, retention14), "", lokiStarted.Add(-time.Minute))
		job, ok := s.startLokiApply(s.lokiPut("alice", retention(21), nil), "alice", false)
		if !ok {
			t.Fatal("the apply was refused")
		}
		resumed(t, waitLokiJob(t, s, job.ID), jobs.OK)
		if config, _ := readLoki(t, s, loki.ConfigFile); !strings.Contains(config, "  retention_period: 504h") {
			t.Errorf("config.yaml must carry the 21-day save:\n%s", config)
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 21 {
			t.Errorf("row %+v (%v)", row, err)
		}
		if n := lokiCount(f, "docker restart"); n != 1 {
			t.Errorf("%d docker restarts, want 1: the resume found Loki on the saved files", n)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestLokiResume
```

Expected: `FAIL` in all five subtests, each at `resumed` with `state "ok": {…}`. T9's step 2 ignores `previous_*`, so no step reads `unfinished apply`. The equal-file subtests and the cutover subtest end `nothing to change`.

- [ ] **Step 3: Implement resume, verify and the hook**

3a. In `packages/pstack/internal/api/loki_apply.go`, add `"errors"` and `"io/fs"` to the import block. Step 8 uses them, and adding them now saves a second edit. T9 already imports `context`, `os`, `path/filepath`, `strings`, `time`, `compose`, `exec`, `inspect`, `log`, `loki`, `scheduler` and `stack`. Until step 8, go vet reports the two new imports as unused. Add them in step 8 instead if you run vet in between.

3b. In T9's `run`, step 3, replace the verify block. It runs from the `s.lokiWriteNext(config, creds)` call through `a.step(lokiVerify, true, "")`. In T9's code it reads:

```go
	if err := s.lokiWriteNext(config, creds); err != nil {
		return a.end(lokiVerify, false, err.Error())
	}
	res := runner.Run("docker run --rm --network none --volumes-from "+compose.Shq(a.container.ID+":ro")+" "+
		compose.Shq(a.container.Image)+" -config.file=/etc/loki/"+loki.ConfigFile+loki.NextSuffix+" -verify-config",
		exec.RunOptions{Label: "loki -verify-config"})
	if !res.OK {
		if out := strings.TrimSpace(res.Stderr); out != "" {
			a.sink.Emit(log.Error, out)
		}
		return a.end(lokiVerify, false, lastLine(res.Stderr, res.Code))
	}
	a.step(lokiVerify, true, "")
```

Replace it with:

```go
	if !a.verify(runner, config, creds) {
		return a.outcome()
	}
```

T9's two `defer os.Remove(...)` lines above it stay.

3c. In T9's `run`, step 2, replace:

```go
	row, err := loki.Read(s.store)
	if err != nil {
		return a.end(lokiRender, false, err.Error())
	}
```

with:

```go
	row, err := loki.Read(s.store)
	if err != nil {
		return a.end(lokiRender, false, err.Error())
	}
	// An apply pstack stopped in: finish or undo it, then this job's saves as a second pass. A failed
	// resume fails this job, and with it the saves it took at the top of run.
	if row != nil && row.InFlight {
		if !a.resume(runner, row) {
			return a.outcome()
		}
		if row, err = loki.Read(s.store); err != nil {
			return a.end(lokiRender, false, err.Error())
		}
	}
```

The rest of step 2 merges onto the re-read row and computes `loki.Changed` against it. The resume half therefore changes nothing that step 10 announces, and fires no `logging.changed`.

3d. Append to `packages/pstack/internal/api/loki_apply.go`:

```go
// ── resume and boot ─────────────────────────────────────────────────────────────────────────────
//
// previous_* is written with the save (step 4) and cleared by finish or undo. If it is set when a job
// reaches step 2, pstack stopped mid-apply: a stop, a recreate, an OOM kill or a reboot. The job goes
// FORWARD to the row. The one way back is when rule 2 refuses and Loki never loaded the files.

// lokiBootBy is who a boot apply runs as, in its transcript's first line.
const lokiBootBy = "pstack (boot)"

// verify writes the .next files and has the running image's Loki parse them, recording the verify
// step. A failure leaves no .next file.
func (a *lokiApply) verify(runner exec.Runner, config, creds string) bool {
	s := a.s
	if err := s.lokiWriteNext(config, creds); err != nil {
		a.step(lokiVerify, false, err.Error())
		return false
	}
	res := runner.Run("docker run --rm --network none --volumes-from "+compose.Shq(a.container.ID+":ro")+" "+
		compose.Shq(a.container.Image)+" -config.file=/etc/loki/"+loki.ConfigFile+loki.NextSuffix+" -verify-config",
		exec.RunOptions{Label: "loki -verify-config"})
	if res.OK {
		a.step(lokiVerify, true, "")
		return true
	}
	for _, name := range []string{loki.ConfigFile, loki.CredentialsFile} {
		os.Remove(filepath.Join(s.opts.LokiDir, name+loki.NextSuffix))
	}
	if out := strings.TrimSpace(res.Stderr); out != "" {
		a.sink.Emit(log.Error, out)
	}
	a.step(lokiVerify, false, lastLine(res.Stderr, res.Code))
	return false
}

// resume finishes or undoes the cut-off apply that row records. false means it recorded the step that
// ends the job. No event.
func (a *lokiApply) resume(runner exec.Runner, row *loki.Row) bool {
	s := a.s
	a.step(lokiRender, true, "unfinished apply")
	for _, r := range []*loki.Row{row, row.Previous} {
		if r != nil {
			a.scrubs = append(a.scrubs, r.Secret)
			if r.Settings.Storage.S3 != nil {
				a.scrubs = append(a.scrubs, r.Settings.Storage.S3.AccessKeyID)
			}
		}
	}
	fail := func(phase stack.Phase, msg string) bool {
		a.step(phase, false, msg)
		return false
	}
	prev, prevSecret := loki.Defaults(), ""
	if row.Previous != nil {
		prev, prevSecret = row.Previous.Settings, row.Previous.Secret
	}
	beforeCfg, beforeCreds, err := lokiRenderFiles(prev, prevSecret)
	if err != nil {
		return fail(lokiRender, err.Error())
	}
	config, creds, err := lokiRenderFiles(row.Settings, row.Secret)
	if err != nil {
		return fail(lokiRender, err.Error())
	}
	diskCfg, diskCreds, credsHere, err := s.lokiDisk()
	if err != nil {
		return fail(lokiRender, err.Error())
	}
	// Has Loki loaded the files on disk? Only if it started after both were written. The mtimes come
	// from the host's clock, and pstack's rename is the only writer. No StartedAt means no.
	var written time.Time
	for _, name := range []string{loki.ConfigFile, loki.CredentialsFile} {
		if st, err := os.Stat(filepath.Join(s.opts.LokiDir, name)); err == nil && st.ModTime().After(written) {
			written = st.ModTime()
		}
	}
	loaded := a.container.StartedAt != nil && *a.container.StartedAt > written.UnixMilli()
	// The period guard reads what Loki runs: the files if it loaded them, else the previous render.
	running := beforeCfg
	if loaded {
		running = diskCfg
	}
	live, err := loki.Periods(running)
	if err != nil {
		return fail(lokiRender, err.Error())
	}
	next, err := loki.Periods(config)
	if err != nil {
		return fail(lokiRender, err.Error())
	}
	now, lead := s.lokiNow(), s.lokiLead()
	if guard := loki.CheckPeriods(live, next, now, lead, true); guard != nil {
		if loaded || loki.CheckPeriods(live, next, now, lead, false) != nil {
			return fail(lokiRender, guard.Error())
		}
		// Rule 2 alone, and Loki still runs the previous files: put them back. Files go first, then the
		// row, so a stop between the two resumes into this same branch. No restart.
		if err := s.lokiWriteNext(beforeCfg, beforeCreds); err != nil {
			return fail(lokiSwap, err.Error())
		}
		if err := s.lokiSwap(beforeCreds != ""); err != nil {
			return fail(lokiSwap, err.Error())
		}
		if err := loki.Revert(s.store); err != nil {
			return fail(lokiCommit, "could not record the undo: "+err.Error())
		}
		msg := guard.Error()
		if s3 := row.Settings.Storage.S3; s3 != nil {
			msg = "cutover " + s3.Cutover + " passed while pstack was down — save again"
		}
		return fail(lokiRender, msg)
	}
	// From here on it is the forward path's post runner: a cancel still restarts, waits and finishes.
	postCtx, cancel := context.WithTimeout(context.Background(), 2*s.lokiLead())
	defer cancel()
	post := s.lokiRunner(postCtx)
	restart := !loaded
	if diskCfg != config || credsHere != (creds != "") || diskCreds != creds {
		if !a.verify(runner, config, creds) {
			return false
		}
		if err := s.lokiSwap(creds != ""); err != nil {
			a.step(lokiSwap, false, err.Error())
			a.rollback(post, row.Previous)
			return false
		}
		a.step(lokiSwap, true, "swapped")
		restart = true
	}
	if restart {
		if _, err := inspect.RestartControlService(post, lokiService); err != nil {
			a.step(lokiRestart, false, err.Error())
			a.rollback(post, row.Previous)
			return false
		}
		a.step(lokiRestart, true, "")
	}
	if !a.ready(post) {
		a.step(lokiReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
		a.rollback(post, row.Previous)
		return false
	}
	a.step(lokiReady, true, "")
	if err := loki.Finish(s.store); err != nil {
		return fail(lokiFinish, "could not record the finished apply")
	}
	a.step(lokiFinish, true, "resumed")
	return true
}
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestLokiResume|TestLokiApply'
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`, with no `WARNING: DATA RACE`. T9's `TestLokiApply…` stays green through the `verify` refactor.

- [ ] **Step 5: Run the resume negative controls**

Apply each mutation, run the Step 4 command with `-run TestLokiResume`, confirm the named subtest fails, then restore it:
1. Delete the `if row != nil && row.InFlight { … }` hook. All five subtests fail at `resumed`.
2. `restart := !loaded` → `restart := false`. Subtest 1 fails with `0 docker restarts, want 1`.
3. `restart := !loaded` → `restart := true`. Subtest 2 fails with `1 docker restarts, want 0`.
4. Delete `restart = true` after the swap. Subtest 3 fails with `0 docker restarts, want 1`.
5. Replace `running := beforeCfg` and its `if loaded { … }` with `running := diskCfg`. Subtest 4 fails with `state "ok"`.
6. In the hook, replace `if row, err = loki.Read(s.store); err != nil { … }` with `return a.outcome()`. Subtest 5 fails on `504h`.

- [ ] **Step 6: Write the failing boot tests**

Append to `packages/pstack/internal/api/loki_apply_test.go`:

```go
// bootNew runs New over a loki directory that holds slice 1's config.yaml (none when !withConfig) and a
// database that seed has written. It returns New's `loki: ` lines. No job may start here, because
// lokiRunner is still the real one.
func bootNew(t *testing.T, withConfig bool, uid int, seed func(*store.Store) error) (*Server, []string) {
	t.Helper()
	data := t.TempDir()
	dir := filepath.Join(data, "control", "loki")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if withConfig {
		if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(pstack.LokiConfig), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if seed != nil {
		st, err := store.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		err = seed(st)
		if cerr := st.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	lines := []string{}
	s, err := New(Options{DataDir: data, LokiDir: dir, LokiUID: uid, Bus: events.New(), Log: func(l string) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(l, "loki: ") {
			lines = append(lines, l)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	mu.Lock()
	defer mu.Unlock()
	return s, append([]string{}, lines...)
}

func TestReconcileLoki(t *testing.T) {
	// negative control: delete both `s.startLokiApply(0, lokiBootBy, true)` calls from reconcileLoki. The
	// three subtests that expect a job see none.
	retention72 := strings.Replace(pstack.LokiConfig, "  retention_period: 168h", "  retention_period: 72h", 1)
	onlyJob := func(t *testing.T, s *Server) jobs.Job {
		t.Helper()
		js := lokiJobs(s)
		if len(js) != 1 {
			t.Fatalf("%d loki-apply jobs, want 1", len(js))
		}
		return waitLokiJob(t, s, js[0].ID)
	}

	// lokiFixture's New already ran reconcileLoki over slice 1's config and an empty table, which are
	// equal bytes and start no job. These subtests set up the state, then call it again.
	t.Run("equal bytes: no docker command, no job", func(t *testing.T) {
		// negative control: delete the `if config == diskCfg && … { return }` line. A job starts and runs
		// docker ps.
		s, f := lokiFixture(t, nil)
		s.reconcileLoki()
		if js := lokiJobs(s); len(js) != 0 {
			t.Fatalf("jobs %+v, want none", js)
		}
		if c := f.Commands(); len(c) != 0 {
			t.Errorf("commands %q, want none", c)
		}
	})

	t.Run("different bytes: exactly one job, by pstack (boot), that renders the defaults", func(t *testing.T) {
		// negative control: delete T9's `a.sink.Emit(log.Info, "by "+by)`. The transcript has no by line.
		s, _ := lokiFixture(t, nil)
		writeLoki(t, s, retention72, "", lokiStarted.Add(-time.Minute))
		s.reconcileLoki()
		j := onlyJob(t, s)
		if j.State != jobs.OK {
			t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
		}
		if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
			t.Error("config.yaml must be the defaults' render")
		}
		said := false
		for _, e := range j.Log {
			said = said || e.Message == "by pstack (boot)"
		}
		if !said {
			t.Errorf("the transcript must say who ran it: %+v", j.Log)
		}
	})

	t.Run("previous_config set: one job even when the bytes are equal", func(t *testing.T) {
		// negative control: delete the `if row != nil && row.InFlight { … }` branch. Equal bytes return
		// with no job.
		s, _ := lokiFixture(t, nil)
		if err := loki.Save(s.store, loki.Defaults(), "", nil); err != nil {
			t.Fatal(err)
		}
		writeLoki(t, s, pstack.LokiConfig, "", lokiStarted.Add(-time.Minute))
		s.reconcileLoki()
		if j := onlyJob(t, s); j.State != jobs.OK {
			t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
		}
		if row, err := loki.Read(s.store); err != nil || row == nil || row.InFlight {
			t.Errorf("want previous_* cleared: %+v (%v)", row, err)
		}
	})

	t.Run("no loki container: the job runs ps and inspect and ends ok", func(t *testing.T) {
		// negative control: end T9's no-container find `failed` for a boot apply too (ignore a.boot). The
		// job is failed.
		traefik := `[{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.5","Labels":{"com.docker.compose.service":"traefik"}},"State":{"Status":"running"}}]`
		s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
			if strings.HasPrefix(cmd, "docker inspect") {
				return exec.Result{OK: true, Stdout: traefik}, true
			}
			return exec.Result{}, false
		})
		writeLoki(t, s, retention72, "", lokiStarted.Add(-time.Minute))
		s.reconcileLoki()
		j := onlyJob(t, s)
		if phase, ok, msg := lastStep(t, j); j.State != jobs.OK || phase != "find" || !ok || msg != "no loki container" {
			t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
		}
		if c := f.Commands(); len(c) != 2 || c[0] != lokiPS || !strings.HasPrefix(c[1], "docker inspect") {
			t.Errorf("commands %q, want ps then inspect", c)
		}
	})

	// The no-job cases go through New itself, which also proves New calls reconcileLoki.
	t.Run("no config.yaml: no job, no line", func(t *testing.T) {
		// negative control: delete the `errors.Is(err, fs.ErrNotExist)` return. One `loki: open …` line.
		s, lines := bootNew(t, false, os.Geteuid(), nil)
		if len(lines) != 0 {
			t.Errorf("lines %q, want none", lines)
		}
		if js := lokiJobs(s); len(js) != 0 {
			t.Errorf("jobs %+v, want none", js)
		}
	})

	t.Run("a saved row that no longer renders: one line, no job", func(t *testing.T) {
		// negative control: empty the `if err != nil { s.opts.Log(…); return }` after lokiRenderFiles. The
		// render is "", it differs from config.yaml, and a job starts with no line. (Also run: delete
		// `s.reconcileLoki()` from New. No line.)
		s, lines := bootNew(t, true, os.Geteuid(), func(st *store.Store) error {
			_, err := st.DB.Exec(`INSERT INTO loki_config (id, config, secret, updated_at) VALUES (1, ?, '', 1)`,
				`{"retentionDays":0,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"},"storage":{"type":"filesystem","s3":null}}`)
			return err
		})
		if len(lines) != 1 {
			t.Errorf("lines %q, want one", lines)
		}
		if js := lokiJobs(s); len(js) != 0 {
			t.Errorf("jobs %+v, want none", js)
		}
	})

	t.Run("an S3 row this process cannot give Loki: one line, no job", func(t *testing.T) {
		// negative control: delete the loki.CredentialsOwner check. A job starts.
		if os.Geteuid() == 0 {
			t.Skip("root can always chown the credentials file")
		}
		s, lines := bootNew(t, true, os.Geteuid()+1, func(st *store.Store) error {
			if err := loki.Save(st, lokiS3("2030-01-15"), "s3cretKEY1", nil); err != nil {
				return err
			}
			return loki.Finish(st)
		})
		if len(lines) != 1 || lines[0] != "loki: "+loki.ErrNeedsRoot.Error() {
			t.Errorf("lines %q, want the needs-root line", lines)
		}
		if js := lokiJobs(s); len(js) != 0 {
			t.Errorf("jobs %+v, want none", js)
		}
	})
}
```

- [ ] **Step 7: Run them and watch them fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestReconcileLoki
```

Expected: a build failure, `s.reconcileLoki undefined (type *Server has no field or method reconcileLoki)`, ending `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/api [build failed]`.

- [ ] **Step 8: Implement the boot reconcile and the call in New**

8a. Append to `packages/pstack/internal/api/loki_apply.go`:

```go
// reconcileLoki starts a boot loki-apply job when an apply was cut off, or when the files are not what
// the row (or the defaults) renders. It runs no docker command. The job does, on its own goroutine, so
// a wedged dockerd cannot stall New.
func (s *Server) reconcileLoki() {
	diskCfg, diskCreds, credsHere, err := s.lokiDisk()
	if errors.Is(err, fs.ErrNotExist) {
		return // logging off, or control/loki is not mounted here
	}
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	row, err := loki.Read(s.store)
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	if row != nil && row.InFlight {
		s.startLokiApply(0, lokiBootBy, true)
		return
	}
	set, secret := loki.Defaults(), ""
	if row != nil {
		set, secret = row.Settings, row.Secret
	}
	// Render runs the save rules, so a row this release rejects (a later release wrote it, or hand SQL)
	// is a line, never a job.
	config, creds, err := lokiRenderFiles(set, secret)
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	if config == diskCfg && credsHere == (creds != "") && creds == diskCreds {
		return
	}
	if creds != "" {
		if err := loki.CredentialsOwner(s.opts.LokiUID); err != nil {
			s.opts.Log("loki: " + err.Error())
			return
		}
	}
	s.startLokiApply(0, lokiBootBy, true)
}
```

8b. In `packages/pstack/internal/api/server.go`, replace:

```go
	s.reconcileDomains()
	return s, nil
```

with:

```go
	s.reconcileDomains()
	// Finish or undo a Loki apply this process died in, and apply a saved row the files do not match.
	// No docker command here: a job runs them, on its own goroutine.
	s.reconcileLoki()
	return s, nil
```

- [ ] **Step 9: Run them and watch them pass, then the package**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestReconcileLoki|TestLokiResume|TestLokiApply'
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ && go vet ./internal/api/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api` both times, and no output from `go vet`. Existing `New(Options{DataDir: t.TempDir()})` tests stay green: T8's default LokiDir, `<DataDir>/control/loki`, has no `config.yaml`, so reconcileLoki returns silently. T9's `lokiFixture` writes slice 1's config before New over an empty table, which is equal bytes, so New starts no job there either.

- [ ] **Step 10: Run the boot negative controls**

Apply each mutation, run the first Step 9 command, confirm the named subtest fails, then restore it:
1. Delete `if config == diskCfg && credsHere == (creds != "") && creds == diskCreds { return }`. "equal bytes" fails.
2. Delete T9's `a.sink.Emit(log.Info, "by "+by)`. "different bytes" fails on the by line.
3. Delete the final `s.startLokiApply(0, lokiBootBy, true)`. "different bytes" and "no loki container" fail.
4. Delete the `if row != nil && row.InFlight { … }` branch. "previous_config set" fails.
5. Delete the `errors.Is(err, fs.ErrNotExist)` return. "no config.yaml" fails.
6. Empty the `if err != nil { … }` body after `lokiRenderFiles`. "no longer renders" fails.
7. Delete `s.reconcileLoki()` from New. "no longer renders" fails with `want one`.
8. Delete the `loki.CredentialsOwner` check. "cannot give Loki" fails. It is skipped as root.
9. Make T9's find ignore `a.boot`. "no loki container" fails.

Controls 5-8 start a job inside New on the real runner (`docker ps`). That is harmless, `s.Stop` cancels it, and the job count assertion catches it.

- [ ] **Step 11: Document it in control-plane.md §5g**

In `docs/control-plane.md`, replace:

```markdown
## 6. Submitting a deployment
```

with:

```markdown
### Who owns `config.yaml`

`init` writes `control/loki/config.yaml` once. After that its content is the API's, as
`pstack-domains.yml` is. An apply and a pstack start render the saved row over it, so a hand edit is
reverted, except a live schema period, which is never dropped. `s3-credentials` is pstack's too: 0600,
Loki's uid.

### Resume

`previous_*` is written with the save and cleared by finish or undo. A job that finds it set picks up
the apply pstack stopped in, and goes forward to the row:

- Loki has loaded the files only if its container started after their mtime. If not, the period
  guard reads `Render(previous)` as loaded.
- Rule 2 refuses and Loki never loaded the files: the previous files go back, the row reverts, no
  restart. `cutover <date> passed while pstack was down — save again`.
- Files differ from the row: verify, swap, restart. Files equal: restart only if Loki has not loaded
  them.
- Then ready and finish, or roll back. No `logging.changed`.
- A save's job resumes first, then applies its own save. A failed resume fails the save with it.

### Boot

`New` calls `reconcileLoki` after `reconcileDomains`. It runs no docker command.

1. No `config.yaml`: nothing.
2. `previous_*` set: a `loki-apply` job.
3. The row, or the defaults, does not render: one `loki:` line, no job.
4. The render differs from `config.yaml` or `s3-credentials`: a job, `by pstack (boot)`. An S3 render
   pstack cannot chown for Loki: one line, no job.

## 6. Submitting a deployment
```

- [ ] **Step 12: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of `packages/pstack/internal/api/loki_apply.go`, `packages/pstack/internal/api/loki_apply_test.go`, `packages/pstack/internal/api/server.go` and `docs/control-plane.md`. Never take `packages/conformance/golden/host/db/*` or anything under `.superpowers/`.

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-settings -m "feat(api): resume a cut-off Loki apply and reconcile at boot" <id-loki_apply.go> <id-loki_apply_test.go> <id-server.go> <id-control-plane.md>
```

---

### Task 12: api: GET /api/logging, PUT /api/logging, PUT /api/logging/storage

**Files:**
- Create: `packages/pstack/internal/api/routes_logging.go`
- Modify: `packages/pstack/internal/api/routes.go` (after the `/api/tls/redeploy` if-block, before `// ---- the swarm ----`; routes.go:175-179 today)
- Modify: `packages/pstack/internal/api/permissions.go` (after the `/api/tls/redeploy` row; permissions.go:173 today)
- Modify: `packages/pstack/api/openapi.yaml` (tag after the `tls` tag entry, openapi.yaml:60-61; two path blocks before `  /api/control/runtime:`, openapi.yaml:779)
- Modify: `packages/pstack/internal/apicli/zz_generated.go`, `packages/pstack/internal/apicli/oascmd.lock.json` (regenerated, never hand-edited)
- Modify: `packages/pstack/internal/apicli/apicli.go` (`const OperationCount = 79`, apicli.go:28)
- Modify: `packages/pstack/internal/cli/api.go` (`groupShort` gains `logging`, after the `"tls"` line, api.go:141). Not in the contract's file list: `TestEveryAPIGroupIsDescribed` (cli/api_test.go:62-82) fails without it.
- Modify: `packages/conformance/test/api-rbac.test.ts` (three rows after the `DELETE /api/tls/wildcard` row)
- Modify: `docs/usage.md` (new `### Loki settings` before `### Why \`init\` is CLI-only, and always will be`; two §7e matrix rows after the `PUT`/`DELETE /api/tls/wildcard` row)
- Modify: `packages/pstack/CHANGELOG.md` (one bullet under `## Unreleased` → `### Added`)
- Test: `packages/pstack/internal/api/routes_logging_test.go` (create)
- Test: `packages/pstack/internal/api/permissions_test.go` (four cases, and one more negative control sentence)

Line numbers are advisory, because slice 1 and T1-T11 move them. Every edit below is anchored on quoted existing text.

**Interfaces:**
- Consumes:
  - T2: `loki.Settings`, `loki.Chunks`, `loki.Storage`, `loki.S3`, `loki.StorageFilesystem`, `loki.StorageS3`, `loki.Defaults() Settings`, `loki.Limits`, `loki.LimitsAt(t time.Time, lead time.Duration) Limits`, `loki.EarliestCutover(t time.Time, lead time.Duration) string`, `type loki.Error struct{ Msg string }`, `loki.ErrOneWay`, `loki.ErrFixed`, `loki.ErrNeedsRoot`, `loki.CredentialsOwner(lokiUID int) error`, `loki.Writable(dir string) bool`, `loki.ConfigFile`.
  - T4: `loki.Row{Settings, Secret, UpdatedAt, InFlight, Previous}`, `loki.Read(*store.Store) (*Row, error)`, `loki.Save(st, s, secret, previous *Row) error`, `loki.Finish(st) error`, `loki.ChunksPatch{RetentionDays, Chunks}`, `loki.StoragePatch{Storage, Secret, KeepSecret}`, `loki.Merge(row *Row, c *ChunksPatch, sp *StoragePatch, lead time.Duration) (Settings, string, error)`, `loki.Changed(before *Row, after Settings, afterSecret string) []string`.
  - T5: `loki.Probe(ctx context.Context, s S3, secret string) error`.
  - T8: `Options.LokiDir`, `Options.LokiUID`, both resolved into `s.opts` by `New`. `Server.lokiMu`, `Server.lokiStorage *lokiEntry`, and the seams `s.lokiRunner`, `s.lokiPoll`, `s.lokiNow`. `s.lokiLead() time.Duration`. `loki.IsError` in `fail()`.
  - T9: `lokiEntry{gen, by, chunks, storage}`, `s.lokiPut(by string, c *loki.ChunksPatch, sp *loki.StoragePatch) uint64`, `s.lokiPending() bool`, `s.startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool)`, `const lokiService = "loki"`, and `func lokiContainer(view inspect.ControlView) (inspect.ControlContainer, bool)`: the control stack's container whose `Service` is `lokiService`, the same lookup the apply's `find` step uses.
  - T11: `s.reconcileLoki()` runs inside `New`. Over a LokiDir that holds exactly `pstack.LokiConfig` and an empty table, it starts no job.
  - Existing: `inspect.ControlRuntime(exec.Runner) inspect.ControlView` (control.go:53), `inspect.ControlView{Containers, Reachable}` (control.go:44-49), `inspect.ControlProject` (control.go:25), `s.jobs.IsBusy` (jobs.go:373), `s.jobs.Hold` (jobs.go:383), `job.Stub()` (jobs.go:221), `terminal.ActorOf(auth.Principal) string` (terminal.go:51), `secretMask` (routes_auth.go:19), `bodyOrEmpty`/`getBool` (body.go:43, :63), `writeJSON`/`writeError` (http.go:20, :40), `s.fail` (server.go:935), `jsonx.O` (jsonx.go:139).
- Produces:
  - `func (s *Server) loggingGet(w http.ResponseWriter) error`
  - `func (s *Server) loggingPut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error`
  - `func (s *Server) loggingStoragePut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error`
  - `func (s *Server) lokiSave(w http.ResponseWriter, r *http.Request, who *auth.Principal, parse func(*omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error)) error`: the PUT order, shared by both PUTs
  - `func lokiEnabled(view inspect.ControlView) *bool`
  - `func parseChunks(body *omap.Map) (loki.ChunksPatch, error)`, `func parseStorage(body *omap.Map) (loki.StoragePatch, error)`, `func wholeNumber(m *omap.Map, key, name string) (int, error)`
  - `type loggingView`, `type loggingStorageView`, `type loggingS3View`. The wire shape T13/T14/T15 read, in this order: `enabled, source, updatedAt, retentionDays, chunks, storage{type, s3{endpoint, region, bucket, pathStyle, accessKeyId, secretSet, cutover}}, limits`.
  - Answers: 200 GET body; PUT 202 `{"job": <stub>}` · 200 `{"changed": false}` · 400 `{error}` · 409 `{error}` · 503 `{"error": "docker did not answer"}`.
  - Exact 409 texts: `Loki is not running on this host — run pstack logging loki on the host`; `the loki directory is not mounted here — run pstack upgrade on the host`; `S3 is one-way on this host` / `S3 storage is fixed once saved — only accessKeyId and secretAccessKey change` / `the S3 credentials file needs root` (the sentinels' `.Error()`); `pstack-control is busy with a teardown — retry`.
  - Parse-time 400 texts: `<field> must be a whole number` (field is `retentionDays` or `chunks.idlePeriodMinutes|maxAgeMinutes|targetSizeKiB`); `type must be filesystem or s3`; `pathStyle must be true or false`. Everything else comes from `loki.Validate`/`Merge`/`Probe`.
  - CLI: `pstack api logging get`, `pstack api logging set --data '…'`, `pstack api logging storage-set …`; `apicli.OperationCount = 82`.

- [ ] **Step 1: Write the failing tests**

Create `packages/pstack/internal/api/routes_logging_test.go`. Every helper is prefixed `logging`, so none can collide with T9-T11's helpers in `loki_apply_test.go`, which is in the same package.

```go
package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

// loggingDocker is what the fake docker says about the control stack.
type loggingDocker int

const (
	loggingDockerSilent loggingDocker = iota // `docker ps` fails: Reachable false
	loggingNoLoki                            // reachable, no control containers
	loggingLokiUp                            // one running `loki` container, l1
)

const loggingInspect = `[{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}},"State":{"Status":"running","StartedAt":"2026-09-15T10:00:00Z"}}]`

// Slice 1's settings, and one change, as PUT /api/logging bodies.
const (
	loggingDefaults    = `{"retentionDays":7,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`
	loggingRetention14 = `{"retentionDays":14,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`
	loggingSecret      = "wJalrXUtnFEMIK7MDENGbPxRfiCY"
)

// loggingFixture is a Server over a LokiDir holding slice 1's config.yaml (when mounted), with the
// host runner and the apply's runners on one exec.Fake.
func loggingFixture(t *testing.T, docker loggingDocker, mounted bool, lokiUID int) (*Server, *exec.Fake) {
	t.Helper()
	dir := t.TempDir()
	if mounted {
		// Written BEFORE New, so reconcileLoki finds slice 1's bytes and starts nothing.
		if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(pstack.LokiConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{DataDir: t.TempDir(), LokiDir: dir, LokiUID: lokiUID, Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			switch docker {
			case loggingDockerSilent:
				return exec.Result{OK: false, Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
			case loggingNoLoki:
				return exec.Result{OK: true}, true
			}
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: loggingInspect}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f
	s.lokiRunner = func(context.Context) exec.Runner { return f }
	s.lokiPoll = time.Millisecond
	return s, f
}

// loggingCall drives the gated chain as root, so dispatch and the gate are in the path.
func loggingCall(s *Server, method, path, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, rd)
	if err := s.routes(w, r, path, &auth.Principal{Kind: auth.KindRoot}, map[string]string{}); err != nil {
		s.fail(w, err)
	}
	return w
}

func loggingBody(t *testing.T, w *httptest.ResponseRecorder) *omap.Map {
	t.Helper()
	v, err := omap.Parse(w.Body.Bytes())
	m, ok := v.(*omap.Map)
	if err != nil || !ok {
		t.Fatalf("not a JSON object: %d %s", w.Code, w.Body.String())
	}
	return m
}

func loggingKeys(m *omap.Map) string {
	var ks []string
	m.Each(func(k string, _ any) { ks = append(ks, k) })
	return strings.Join(ks, ",")
}

// loggingUntilDone waits for a job a PUT started, so nothing writes into a t.TempDir being removed.
func loggingUntilDone(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := s.jobs.Get(id); ok && j.State.Terminal() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %q never finished", id)
}

func loggingS3Body(endpoint, cutover, secret string) string {
	return `{"type":"s3","endpoint":"` + endpoint + `","region":"us-east-1","bucket":"pstack-logs","pathStyle":true,"accessKeyId":"AKIDEXAMPLE","secretAccessKey":"` + secret + `","cutover":"` + cutover + `"}`
}

// loggingSaveS3 stores a finished S3 save, the state after a successful apply.
func loggingSaveS3(t *testing.T, s *Server) {
	t.Helper()
	saved := loki.Defaults()
	saved.Storage = loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: "AKIDEXAMPLE", Cutover: "2026-09-16",
	}}
	if err := loki.Save(s.store, saved, loggingSecret, nil); err != nil {
		t.Fatal(err)
	}
	if err := loki.Finish(s.store); err != nil {
		t.Fatal(err)
	}
}

// loggingFakeS3 answers PUT 200 and DELETE 204, or 403 InvalidAccessKeyId to everything while refusing.
func loggingFakeS3(t *testing.T) (endpoint string, calls func() []string, refuse func(bool)) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	no := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		refused := no
		mu.Unlock()
		switch {
		case refused:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidAccessKeyId</Code><Message>body-text-never-echoed</Message></Error>`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL,
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) },
		func(v bool) { mu.Lock(); no = v; seen = nil; mu.Unlock() }
}

// TestLoggingPutAnswersBeforeReadingTheBody covers steps 1 and 2 of both PUTs. The rbac conformance
// rows rely on a bodiless PUT on a logging-off host being a 409.
//
// negative control: move `parse(bodyOrEmpty(r))` above step 1 in lokiSave. The bodiless no-loki PUTs
// then answer 400. Also: delete the loki.Writable check, and the unmounted PUT answers 202.
func TestLoggingPutAnswersBeforeReadingTheBody(t *testing.T) {
	for _, c := range []struct {
		docker loggingDocker
		status int
		msg    string
	}{
		{loggingDockerSilent, 503, "docker did not answer"},
		{loggingNoLoki, 409, "Loki is not running on this host — run pstack logging loki on the host"},
	} {
		s, _ := loggingFixture(t, c.docker, true, os.Geteuid())
		for _, path := range []string{"/api/logging", "/api/logging/storage"} {
			w := loggingCall(s, http.MethodPut, path, "")
			if w.Code != c.status || loggingBody(t, w).GetString("error") != c.msg {
				t.Errorf("PUT %s = %d %s, want %d %q", path, w.Code, w.Body.String(), c.status, c.msg)
			}
		}
	}
	s, _ := loggingFixture(t, loggingLokiUp, false, os.Geteuid())
	w := loggingCall(s, http.MethodPut, "/api/logging", loggingRetention14)
	if w.Code != 409 || loggingBody(t, w).GetString("error") != "the loki directory is not mounted here — run pstack upgrade on the host" {
		t.Errorf("unmounted: %d %s", w.Code, w.Body.String())
	}
}

// TestLoggingPutRefusesABadBodyWith400 checks that a malformed or out-of-range field is the
// caller's to fix, names the field, and starts nothing.
//
// negative control: drop `n == math.Trunc(n)` from wholeNumber. `retentionDays: 1.5` then saves as
// 1 and answers 202.
func TestLoggingPutRefusesABadBodyWith400(t *testing.T) {
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	for _, c := range []struct{ path, body, msg string }{
		{"/api/logging", `{"retentionDays":0,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`, "retentionDays"},
		{"/api/logging", `{"retentionDays":1.5,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`, "retentionDays must be a whole number"},
		{"/api/logging", `{"retentionDays":7}`, "chunks.idlePeriodMinutes must be a whole number"},
		{"/api/logging/storage", `{"type":"gcs"}`, "type must be filesystem or s3"},
		{"/api/logging/storage", `{"type":"s3","endpoint":"https://s3.example.com","region":"us-east-1","bucket":"pstack-logs","accessKeyId":"AKIDEXAMPLE","secretAccessKey":"` + loggingSecret + `","cutover":"2099-01-01"}`, "pathStyle must be true or false"},
	} {
		w := loggingCall(s, http.MethodPut, c.path, c.body)
		if w.Code != 400 || !strings.Contains(loggingBody(t, w).GetString("error"), c.msg) {
			t.Errorf("PUT %s %s = %d %s, want 400 naming %q", c.path, c.body, w.Code, w.Body.String(), c.msg)
		}
	}
	if n := len(s.jobs.List()); n != 0 {
		t.Errorf("a refused body started %d job(s)", n)
	}
}

// TestLoggingPutAnswersAnUnchangedBodyByState: an unchanged body is a 200 only on an idle key.
// Otherwise the job decides.
func TestLoggingPutAnswersAnUnchangedBodyByState(t *testing.T) {
	t.Run("idle: 200 changed:false, nothing restarted, no job", func(t *testing.T) {
		// negative control: delete step 5's branch in lokiSave. Both calls then answer 202.
		s, f := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		for _, c := range []struct{ path, body string }{
			{"/api/logging", loggingDefaults},
			{"/api/logging/storage", `{"type":"filesystem"}`},
		} {
			w := loggingCall(s, http.MethodPut, c.path, c.body)
			if w.Code != 200 || strings.Join(strings.Fields(w.Body.String()), "") != `{"changed":false}` {
				t.Errorf("PUT %s = %d %s, want 200 {\"changed\":false}", c.path, w.Code, w.Body.String())
			}
		}
		for _, cmd := range f.Commands() {
			if strings.HasPrefix(cmd, "docker restart") {
				t.Errorf("an unchanged save restarted something: %s", cmd)
			}
		}
		if n := len(s.jobs.List()); n != 0 {
			t.Errorf("an unchanged save started %d job(s)", n)
		}
	})

	t.Run("busy: 202 with a queued loki-apply stub", func(t *testing.T) {
		// negative control: drop `!s.jobs.IsBusy(inspect.ControlProject)` from step 5. This then answers 200.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		done := make(chan struct{})
		block := func(log.Sink, context.Context) (stack.Outcome, error) { <-done; return stack.Outcome{OK: true}, nil }
		if _, ok := s.jobs.Start(inspect.ControlProject, jobs.Verify, block, nil); !ok {
			close(done)
			t.Fatal("could not occupy the control key")
		}
		w := loggingCall(s, http.MethodPut, "/api/logging", loggingDefaults)
		close(done)
		if w.Code != 202 {
			t.Fatalf("busy key: %d %s, want 202", w.Code, w.Body.String())
		}
		job := loggingBody(t, w).GetMap("job")
		if job.GetString("action") != "loki-apply" || job.GetString("stack") != inspect.ControlProject || job.GetString("state") != "queued" {
			t.Errorf("stub = %s", w.Body.String())
		}
		loggingUntilDone(t, s, job.GetString("id"))
	})

	t.Run("a refused Start: 409, and the save stays pending", func(t *testing.T) {
		// negative control: write 202 on `!ok` in lokiSave. This then reads 202.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		release, ok := s.jobs.Hold(inspect.ControlProject)
		if !ok {
			t.Fatal("the control key was already busy")
		}
		defer release()
		w := loggingCall(s, http.MethodPut, "/api/logging", loggingRetention14)
		if w.Code != 409 || loggingBody(t, w).GetString("error") != "pstack-control is busy with a teardown — retry" {
			t.Errorf("held key: %d %s", w.Code, w.Body.String())
		}
		if !s.lokiPending() {
			t.Error("a refused save must stay pending, for the next save's job to carry")
		}
	})
}

// TestLoggingStoragePutKeepsS3OneWayAndFixed checks that a stored S3 save cannot go back to
// filesystem or move, and that both refusals are 409s.
//
// negative control: remove the errors.Is(ErrOneWay/ErrFixed) branch in lokiSave. Both refusals then
// reach fail() and read 500.
func TestLoggingStoragePutKeepsS3OneWayAndFixed(t *testing.T) {
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	loggingSaveS3(t, s)
	for _, c := range []struct {
		body string
		want error
	}{
		{`{"type":"filesystem"}`, loki.ErrOneWay},
		{`{"type":"s3","endpoint":"https://s3.eu-central-1.amazonaws.com","region":"eu-central-1","bucket":"other-logs","pathStyle":false,"accessKeyId":"AKIDEXAMPLE","secretAccessKey":"","cutover":"2026-09-16"}`, loki.ErrFixed},
	} {
		w := loggingCall(s, http.MethodPut, "/api/logging/storage", c.body)
		if w.Code != 409 || loggingBody(t, w).GetString("error") != c.want.Error() {
			t.Errorf("PUT %s = %d %s, want 409 %q", c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if n := len(s.jobs.List()); n != 0 {
		t.Errorf("a refused storage save started %d job(s)", n)
	}
}

// TestLoggingStoragePutNeedsAnOwnableCredentialsFile covers step 4. It sets LokiUID one above this
// process's euid, because the loki package's geteuid seam is unexported.
//
// negative control: delete step 4 in lokiSave. The probe then reaches the fake S3, and the PUT
// answers 202.
func TestLoggingStoragePutNeedsAnOwnableCredentialsFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("euid 0 can always chown the credentials file to Loki's uid")
	}
	endpoint, calls, _ := loggingFakeS3(t)
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid()+1)
	w := loggingCall(s, http.MethodPut, "/api/logging/storage", loggingS3Body(endpoint, loki.EarliestCutover(time.Now(), s.lokiLead()), loggingSecret))
	if w.Code != 409 || loggingBody(t, w).GetString("error") != loki.ErrNeedsRoot.Error() {
		t.Errorf("non-root: %d %s, want 409 %q", w.Code, w.Body.String(), loki.ErrNeedsRoot)
	}
	if n := len(calls()); n != 0 {
		t.Errorf("the probe sent %d request(s) before the refusal", n)
	}
}

// TestLoggingStoragePutProbesBeforeItRecordsAnything covers step 6, before step 7: a refused probe
// records nothing, and a good one is a PUT, then a DELETE, of the same object.
//
// negative control: move step 6 (loki.Probe) below step 7 in lokiSave. The refused probe then
// leaves a pending save and a job behind.
func TestLoggingStoragePutProbesBeforeItRecordsAnything(t *testing.T) {
	endpoint, calls, refuse := loggingFakeS3(t)
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	body := loggingS3Body(endpoint, loki.EarliestCutover(time.Now(), s.lokiLead()), loggingSecret)

	refuse(true)
	w := loggingCall(s, http.MethodPut, "/api/logging/storage", body)
	if msg := loggingBody(t, w).GetString("error"); w.Code != 400 || !strings.Contains(msg, "403 InvalidAccessKeyId") || strings.Contains(msg, "body-text-never-echoed") {
		t.Fatalf("refused probe: %d %s", w.Code, w.Body.String())
	}
	if s.lokiPending() || len(s.jobs.List()) != 0 {
		t.Fatal("a refused probe must record nothing and start nothing")
	}

	refuse(false)
	w = loggingCall(s, http.MethodPut, "/api/logging/storage", body)
	if w.Code != 202 {
		t.Fatalf("good probe: %d %s, want 202", w.Code, w.Body.String())
	}
	got := calls()
	if len(got) != 2 || !strings.HasPrefix(got[0], "PUT /pstack-logs/pstack-probe-") || got[1] != "DELETE"+strings.TrimPrefix(got[0], "PUT") {
		t.Errorf("S3 saw %q, want PUT then DELETE of one pstack-probe-<hex> key", got)
	}
	loggingUntilDone(t, s, loggingBody(t, w).GetMap("job").GetString("id"))
}

func TestLoggingGet(t *testing.T) {
	t.Run("defaults: spec order, s3 and updatedAt null, encodings an array", func(t *testing.T) {
		// negative control: add `,omitempty` to loggingStorageView.S3's json tag. "s3" then drops out
		// of the storage keys.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		s.lokiNow = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
		w := loggingCall(s, http.MethodGet, "/api/logging", "")
		if w.Code != 200 {
			t.Fatalf("GET: %d %s", w.Code, w.Body.String())
		}
		m := loggingBody(t, w)
		if got := loggingKeys(m); got != "enabled,source,updatedAt,retentionDays,chunks,storage,limits" {
			t.Errorf("keys = %s", got)
		}
		if v, _ := m.Get("enabled"); v != true {
			t.Errorf("enabled = %v, want true", v)
		}
		if m.GetString("source") != "default" {
			t.Errorf("source = %q", m.GetString("source"))
		}
		if v, ok := m.Get("updatedAt"); !ok || v != nil {
			t.Errorf("updatedAt = %v (present %v), want null", v, ok)
		}
		if v, _ := m.Get("retentionDays"); v != int64(7) {
			t.Errorf("retentionDays = %v", v)
		}
		st := m.GetMap("storage")
		if got := loggingKeys(st); got != "type,s3" || st.GetString("type") != "filesystem" {
			t.Errorf("storage = %s / %q", got, st.GetString("type"))
		}
		if v, ok := st.Get("s3"); !ok || v != nil {
			t.Errorf("storage.s3 = %v (present %v), want null", v, ok)
		}
		lim := m.GetMap("limits")
		if got := loggingKeys(lim); got != "retentionDays,idlePeriodMinutes,maxAgeMinutes,targetSizeKiB,encodings,earliestCutover" {
			t.Errorf("limits keys = %s", got)
		}
		if enc := lim.GetSlice("encodings"); len(enc) != 4 {
			t.Errorf("encodings = %v, want an array of 4", enc)
		}
		// 12:00 + 2×15m + 10m is 12:40 UTC, so the first midnight strictly after it is tomorrow's.
		if lim.GetString("earliestCutover") != "2026-09-16" {
			t.Errorf("earliestCutover = %q", lim.GetString("earliestCutover"))
		}
	})

	t.Run("enabled is what docker says: null when silent, false with no loki", func(t *testing.T) {
		// negative control: make lokiEnabled return a pointer to false when !Reachable. The silent
		// case then reads false.
		for _, c := range []struct {
			docker loggingDocker
			want   any
		}{{loggingDockerSilent, nil}, {loggingNoLoki, false}} {
			s, _ := loggingFixture(t, c.docker, true, os.Geteuid())
			w := loggingCall(s, http.MethodGet, "/api/logging", "")
			if v, ok := loggingBody(t, w).Get("enabled"); w.Code != 200 || !ok || v != c.want {
				t.Errorf("docker %d: %d enabled=%v (present %v), want %v", c.docker, w.Code, v, ok, c.want)
			}
		}
	})

	t.Run("a stored S3 row: source db, secretSet, never the secret or the mask", func(t *testing.T) {
		// negative control: add `Secret string `json:"secretAccessKey"`` to loggingS3View and fill it
		// with secretMask. The mask then shows in the body.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		loggingSaveS3(t, s)
		w := loggingCall(s, http.MethodGet, "/api/logging", "")
		if text := w.Body.String(); strings.Contains(text, loggingSecret) || strings.Contains(text, secretMask) {
			t.Fatalf("the GET leaks the secret or its mask: %s", text)
		}
		m := loggingBody(t, w)
		if m.GetString("source") != "db" {
			t.Errorf("source = %q", m.GetString("source"))
		}
		if v, _ := m.Get("updatedAt"); v == nil {
			t.Error("updatedAt must be set on a stored row")
		}
		s3 := m.GetMap("storage").GetMap("s3")
		if got := loggingKeys(s3); got != "endpoint,region,bucket,pathStyle,accessKeyId,secretSet,cutover" {
			t.Errorf("s3 keys = %s", got)
		}
		if v, _ := s3.Get("secretSet"); v != true {
			t.Errorf("secretSet = %v", v)
		}
	})
}
```

In `packages/pstack/internal/api/permissions_test.go`, replace

```go
// /api/settings/default_role the same maintainer tier as /api/settings/max_jobs → the default_role
// case fails, which is the per-key half of the settings contract. All four were run.
```

with

```go
// /api/settings/default_role the same maintainer tier as /api/settings/max_jobs → the default_role
// case fails, which is the per-key half of the settings contract. All four were run. Also: give the
// /api/logging/storage row auth.Maintainer → the storage case fails.
```

and replace

```go
		// the control stack
		{"GET", "/api/control", auth.Viewer},
```

with

```go
		// the control stack
		{"GET", "/api/control", auth.Viewer},

		// Loki's settings: the read and chunks with host configuration, storage a tier up
		{"GET", "/api/logging", auth.Maintainer},
		{"PUT", "/api/logging", auth.Maintainer},
		{"PUT", "/api/logging/storage", auth.Admin},
		{"GET", "/api/logging/storage", rootOnly},
```

- [ ] **Step 2: Run the tests and watch them fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestLogging|TestPermissionTableIsTheSpecification'
```

Expected: the file compiles, because it uses only T2-T11 names, and then it FAILs.
- Every `loggingCall` answers the chain's `404 not found`, so you see lines such as `PUT /api/logging = 404 … want 503 "docker did not answer"` and `busy key: 404 … want 202`.
- `TestPermissionTableIsTheSpecification` reports `GET /api/logging = "<root only>", want "maintainer"` and the two PUT lines.
- `TestLoggingStoragePutNeedsAnOwnableCredentialsFile` SKIPs only when euid is 0.

- [ ] **Step 3: Implement the handlers, the dispatch and the rows**

Create `packages/pstack/internal/api/routes_logging.go`:

```go
// `/api/logging` — Loki's settings: retention and chunks, and where chunks are stored.
//
// THE ROW IS WHAT AN OPERATOR SAVED, NOT WHAT LOKI RUNS (invariant 10). Loki runs
// control/loki/config.yaml and its container. Once a save exists the API owns that file's content,
// the way it owns pstack-domains.yml; init only creates it. A hand edit is reverted by the next
// apply or pstack start, except that no live schema period is ever dropped (loki.CheckPeriods).
//
// A save is a `loki-apply` job on the pstack-control key (loki_apply.go): render, -verify-config,
// rename, restart Loki only, wait for ready, roll back when it does not come up. Nothing here
// renders compose or recreates a container (invariant 12); the mount and the env line are init's.
//
// ── A PUT, IN ORDER ─────────────────────────────────────────────────────────────────────────────
//
//  1. Loki here? Docker silent → 503; no `loki` container → 409. BEFORE the body is read, so a
//     bodiless PUT on a logging-off host is a 409.
//  2. No writable config.yaml in LokiDir → 409: pstack's ./loki mount comes from init.
//  3. Parse, then Merge over the stored row with any queued storage save on top. One-way and
//     fixed-field → 409 (sentinels, mapped here); a malformed or out-of-range field → 400 (fail()).
//  4. Storage with S3: pstack must be able to hand Loki's uid the 0600 credentials file → 409.
//  5. Nothing running or queued on the key, nothing pending, nothing changed → 200
//     {changed: false}. Otherwise the job decides, against the row it reads.
//  6. Storage with S3: the probe, before anything is recorded → 400 when S3 refuses.
//  7. Record the patch (lokiPut) and start the job → 202 {job}.
//
// Steps 3–4 are early refusals, not the authority: the job re-runs them against the row it reads.
//
// ── ROLES (permissions.go) ──────────────────────────────────────────────────────────────────────
//
// Retention and chunks are host configuration: maintainer, who can already restart Loki. Storage is
// ADMIN for BLAST RADIUS, the TLS-wildcard argument: one save sends every node's logs to an outside
// bucket, and it cannot be undone. Not an access boundary — viewers read logs through the logs
// routes. Two paths rather than a role check inside a handler, per the settings precedent. The
// `loki-apply` transcript is viewer-readable and may name the endpoint and bucket in Loki's error
// lines; only the secret and key id are scrubbed.
//
// The S3 secret has no read path (invariant 15). GET answers `secretSet`, never the mask; an empty,
// omitted or masked `secretAccessKey` keeps the stored one.
package api

import (
	"errors"
	"math"
	"net/http"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
)

// loggingView is GET /api/logging. Field order is the JSON order.
type loggingView struct {
	// Enabled is tri-state: nil when docker did not answer.
	Enabled       *bool              `json:"enabled"`
	Source        string             `json:"source"`
	UpdatedAt     *int64             `json:"updatedAt"`
	RetentionDays int                `json:"retentionDays"`
	Chunks        loki.Chunks        `json:"chunks"`
	Storage       loggingStorageView `json:"storage"`
	Limits        loki.Limits        `json:"limits"`
}

type loggingStorageView struct {
	Type string         `json:"type"`
	S3   *loggingS3View `json:"s3"`
}

// loggingS3View is loki.S3 plus secretSet. There is no field that could carry the secret.
type loggingS3View struct {
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	SecretSet   bool   `json:"secretSet"`
	Cutover     string `json:"cutover"`
}

// lokiEnabled is GET's `enabled` and step 1 of both PUTs: nil when docker did not answer, else
// whether the control stack has a loki container. The lookup is the apply's own (lokiContainer).
func lokiEnabled(view inspect.ControlView) *bool {
	if !view.Reachable {
		return nil
	}
	_, on := lokiContainer(view)
	return &on
}

func (s *Server) loggingGet(w http.ResponseWriter) error {
	row, err := loki.Read(s.store)
	if err != nil {
		return err
	}
	v := loggingView{
		Enabled: lokiEnabled(inspect.ControlRuntime(s.host)),
		Source:  "default",
		Limits:  loki.LimitsAt(s.lokiNow(), s.lokiLead()),
	}
	set, secret := loki.Defaults(), ""
	if row != nil {
		set, secret = row.Settings, row.Secret
		v.Source = "db"
		v.UpdatedAt = &row.UpdatedAt
	}
	v.RetentionDays = set.RetentionDays
	v.Chunks = set.Chunks
	v.Storage.Type = set.Storage.Type
	if c := set.Storage.S3; c != nil {
		v.Storage.S3 = &loggingS3View{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, PathStyle: c.PathStyle,
			AccessKeyID: c.AccessKeyID, SecretSet: secret != "", Cutover: c.Cutover}
	}
	writeJSON(w, 200, v)
	return nil
}

func (s *Server) loggingPut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	return s.lokiSave(w, r, who, func(body *omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error) {
		c, err := parseChunks(body)
		return &c, nil, err
	})
}

func (s *Server) loggingStoragePut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	return s.lokiSave(w, r, who, func(body *omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error) {
		sp, err := parseStorage(body)
		return nil, &sp, err
	})
}

// lokiSave is both PUTs, in the order the file header gives.
func (s *Server) lokiSave(w http.ResponseWriter, r *http.Request, who *auth.Principal,
	parse func(*omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error)) error {
	// 1 and 2, before the body.
	on := lokiEnabled(inspect.ControlRuntime(s.host))
	if on == nil {
		writeError(w, 503, "docker did not answer")
		return nil
	}
	if !*on {
		writeError(w, 409, "Loki is not running on this host — run pstack logging loki on the host")
		return nil
	}
	if !loki.Writable(s.opts.LokiDir) {
		writeError(w, 409, "the loki directory is not mounted here — run pstack upgrade on the host")
		return nil
	}

	// 3
	c, sp, err := parse(bodyOrEmpty(r))
	if err != nil {
		return err
	}
	row, err := loki.Read(s.store)
	if err != nil {
		return err
	}
	lead := s.lokiLead()
	base := row
	s.lokiMu.Lock()
	var queued *loki.StoragePatch
	if s.lokiStorage != nil {
		queued = s.lokiStorage.storage
	}
	s.lokiMu.Unlock()
	if queued != nil {
		// A storage save no job has taken yet. The fixed-field rules check against it. One that no
		// longer merges (its cutover came too close) is its job's to refuse, not this request's.
		if set, secret, err := loki.Merge(row, nil, queued, lead); err == nil {
			base = &loki.Row{Settings: set, Secret: secret}
		}
	}
	merged, secret, err := loki.Merge(base, c, sp, lead)
	if errors.Is(err, loki.ErrOneWay) || errors.Is(err, loki.ErrFixed) {
		writeError(w, 409, err.Error())
		return nil
	}
	if err != nil {
		return err
	}

	// 4
	s3 := sp != nil && merged.Storage.S3 != nil
	if s3 {
		if err := loki.CredentialsOwner(s.opts.LokiUID); err != nil {
			writeError(w, 409, err.Error())
			return nil
		}
	}

	// 5
	if !s.jobs.IsBusy(inspect.ControlProject) && !s.lokiPending() && len(loki.Changed(row, merged, secret)) == 0 {
		writeJSON(w, 200, jsonx.O("changed", false))
		return nil
	}

	// 6
	if s3 {
		if err := loki.Probe(r.Context(), *merged.Storage.S3, secret); err != nil {
			return err
		}
	}

	// 7
	by := terminal.ActorOf(*who)
	job, ok := s.startLokiApply(s.lokiPut(by, c, sp), by, false)
	if !ok {
		// A waiting teardown of this key outranks the apply (jobs.go:597-605). The patch stays
		// pending; the next save's job carries it.
		writeError(w, 409, "pstack-control is busy with a teardown — retry")
		return nil
	}
	writeJSON(w, 202, jsonx.O("job", job.Stub()))
	return nil
}

// parseChunks is PUT /api/logging's body. Every number is required. A missing encoding is left ""
// for loki.Validate to name.
func parseChunks(body *omap.Map) (loki.ChunksPatch, error) {
	var p loki.ChunksPatch
	chunks := body.GetMap("chunks")
	for _, f := range []struct {
		m         *omap.Map
		key, name string
		into      *int
	}{
		{body, "retentionDays", "retentionDays", &p.RetentionDays},
		{chunks, "idlePeriodMinutes", "chunks.idlePeriodMinutes", &p.Chunks.IdlePeriodMinutes},
		{chunks, "maxAgeMinutes", "chunks.maxAgeMinutes", &p.Chunks.MaxAgeMinutes},
		{chunks, "targetSizeKiB", "chunks.targetSizeKiB", &p.Chunks.TargetSizeKiB},
	} {
		n, err := wholeNumber(f.m, f.key, f.name)
		if err != nil {
			return p, err
		}
		*f.into = n
	}
	p.Chunks.Encoding = chunks.GetString("encoding")
	return p, nil
}

// parseStorage is PUT /api/logging/storage's body. `{type: filesystem}` needs nothing else. For S3,
// a missing string is left "" for loki.Validate to name.
func parseStorage(body *omap.Map) (loki.StoragePatch, error) {
	var p loki.StoragePatch
	switch body.GetString("type") {
	case loki.StorageFilesystem:
		p.Storage.Type = loki.StorageFilesystem
		return p, nil
	case loki.StorageS3:
	default:
		return p, &loki.Error{Msg: "type must be filesystem or s3"}
	}
	pathStyle, ok := getBool(body, "pathStyle")
	if !ok {
		return p, &loki.Error{Msg: "pathStyle must be true or false"}
	}
	p.Storage = loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint:    body.GetString("endpoint"),
		Region:      body.GetString("region"),
		Bucket:      body.GetString("bucket"),
		PathStyle:   pathStyle,
		AccessKeyID: body.GetString("accessKeyId"),
		Cutover:     body.GetString("cutover"),
	}}
	// The SSO convention: the mask round-tripped, or nothing typed, means keep the stored secret.
	if secret := body.GetString("secretAccessKey"); secret == "" || secret == secretMask {
		p.KeepSecret = true
	} else {
		p.Secret = secret
	}
	return p, nil
}

// wholeNumber is a required integer field: JSON 14 or 14.0, never 14.5 or "14".
func wholeNumber(m *omap.Map, key, name string) (int, error) {
	v, _ := m.Get(key)
	switch n := v.(type) {
	case int64:
		return int(n), nil
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return int(n), nil
		}
	}
	return 0, &loki.Error{Msg: name + " must be a whole number"}
}
```

In `packages/pstack/internal/api/routes.go`, replace

```go
	if path == "/api/tls/redeploy" && r.Method == http.MethodPost {
		return s.tlsRedeploy(w, who)
	}
```

with

```go
	if path == "/api/tls/redeploy" && r.Method == http.MethodPost {
		return s.tlsRedeploy(w, who)
	}

	// ---- Loki's settings ----
	// Two tiers over three literals: chunks and retention are maintainer's, storage admin's
	// (permissions.go). routes_logging.go's header has the PUT order.
	if path == "/api/logging" && r.Method == http.MethodGet {
		return s.loggingGet(w)
	}
	if path == "/api/logging" && r.Method == http.MethodPut {
		return s.loggingPut(w, r, who)
	}
	if path == "/api/logging/storage" && r.Method == http.MethodPut {
		return s.loggingStoragePut(w, r, who)
	}
```

In `packages/pstack/internal/api/permissions.go`, replace

```go
	{path: "/api/tls/redeploy", methods: mPost, min: auth.Maintainer},
```

with

```go
	{path: "/api/tls/redeploy", methods: mPost, min: auth.Maintainer},

	// ── Loki's settings ─────────────────────────────────────────────────────────────────────────
	// Retention and chunks are host configuration: maintainer, who can already restart Loki through
	// /api/control/restart. Storage is ADMIN for BLAST RADIUS, the wildcard argument above: one save
	// sends every node's logs to an outside bucket, and it cannot be undone. Not an access boundary —
	// viewers read logs through the logs routes. The read sits with the other host-configuration
	// reads; it never carries the secret.
	{path: "/api/logging", methods: []string{http.MethodGet, http.MethodPut}, min: auth.Maintainer},
	{path: "/api/logging/storage", methods: mPut, min: auth.Admin},
```

- [ ] **Step 4: Run the tests and watch them pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestLogging|TestPermissionTableIsTheSpecification|TestEveryDispatchedRouteHasARow'
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`. No race report. `TestLoggingStoragePutNeedsAnOwnableCredentialsFile` passes, or skips under euid 0.

- [ ] **Step 5: Watch the OpenAPI coverage fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestEveryRouteIsInTheSpecOrExcusedByName
```

Expected: FAIL with `2 route(s) reachable over HTTP with no OpenAPI path, so no \`pstack api\` command:` naming `/api/logging  (routes.go)` and `/api/logging/storage  (routes.go)`.

- [ ] **Step 6: OpenAPI, the generated CLI, the count and the group line**

In `packages/pstack/api/openapi.yaml`, replace

```yaml
  - name: tls
    description: 'The host''s certificate mode, and the bring-your-own wildcard (dns-persist-01).'
```

with

```yaml
  - name: tls
    description: 'The host''s certificate mode, and the bring-your-own wildcard (dns-persist-01).'
  - name: logging
    description: 'Loki''s settings: chunks, retention, storage.'
```

and replace the line

```yaml
  /api/control/runtime:
```

with

```yaml
  # ── Loki's settings ─────────────────────────────────────────────────────────────────────────────
  /api/logging:
    get:
      tags: [logging]
      operationId: getLogging
      x-cli-name: get
      summary: 'Loki''s settings as saved, and the limits pstack enforces. `enabled` is null when docker did not answer. Never the S3 secret: `secretSet` only. Maintainer.'
      responses: { '200': { $ref: '#/components/responses/Ok' } }
    put:
      tags: [logging]
      operationId: setLogging
      x-cli-name: set
      summary: 'Retention and chunks, every field. 202 carries the loki-apply job, which restarts Loki. An unchanged body on an idle host is 200 `{changed: false}`. 409 when Loki is not on this host. Maintainer.'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [retentionDays, chunks]
              properties:
                retentionDays: { type: integer, description: 'Days, within limits.retentionDays.' }
                chunks: { type: object, description: '{ idlePeriodMinutes, maxAgeMinutes, targetSizeKiB, encoding }, every field, within limits.' }
      responses: { '200': { $ref: '#/components/responses/Ok' }, '202': { $ref: '#/components/responses/Accepted' }, '400': { $ref: '#/components/responses/BadRequest' }, '409': { $ref: '#/components/responses/Conflict' } }

  /api/logging/storage:
    put:
      tags: [logging]
      operationId: setLoggingStorage
      x-cli-name: storage-set
      summary: 'Filesystem, or S3 from a future cutover date. One-way: once S3 is saved, only the keys change. The keys are probed with a PUT and a DELETE first. Admin.'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [type]
              properties:
                type: { type: string, enum: [filesystem, s3] }
                endpoint: { type: string, description: 'https://host[:port] or http://host[:port].' }
                region: { type: string }
                bucket: { type: string }
                pathStyle: { type: boolean }
                accessKeyId: { type: string }
                secretAccessKey: { type: string, description: 'Write-only. Empty, omitted or the mask keeps the stored one.' }
                cutover: { type: string, description: 'YYYY-MM-DD, UTC: the first day on S3. No earlier than limits.earliestCutover.' }
      responses: { '200': { $ref: '#/components/responses/Ok' }, '202': { $ref: '#/components/responses/Accepted' }, '400': { $ref: '#/components/responses/BadRequest' }, '409': { $ref: '#/components/responses/Conflict' } }

  /api/control/runtime:
```

No 503 is documented. `components.responses` has none (openapi.yaml:901-908), and `/api/control/restart` does not document its 503 either. `chunks` is `type: object` (the notifier `config` precedent, openapi.yaml:400), so `logging set` takes `--data`. The storage body is flat, so each of its fields is also a flag.

Regenerate:

```bash
cd /Volumes/S1/code/preview-stacks && go generate ./packages/pstack/internal/apicli
jq '.operations | length' packages/pstack/internal/apicli/oascmd.lock.json
jq -r '.operations[] | select(.path | startswith("/api/logging")) | .command' packages/pstack/internal/apicli/oascmd.lock.json
```

Expected:
- `go generate` exits 0 with no drift refusal, because it only adds operations.
- `82`.
- `logging get`, `logging set` and `logging storage-set`.

In `packages/pstack/internal/apicli/apicli.go`, replace `const OperationCount = 79` with `const OperationCount = 82`.

In `packages/pstack/internal/cli/api.go`, replace

```go
	"tls":         "The host's certificate mode, and the bring-your-own wildcard.",
```

with

```go
	"tls":         "The host's certificate mode, and the bring-your-own wildcard.",
	"logging":     "Loki's settings: chunks, retention, storage.",
```

No help golden moves: `grep -c 79` over `golden/cli/help.json`, `help-h.json` and `no-args.json` is 0 in each, and `pstack api --help` has no golden.

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/api internal/cli internal/apicli && go vet ./internal/api/ ./internal/cli/ ./internal/apicli/ && go test -race -timeout 120s ./internal/api/ ./internal/cli/ ./internal/apicli/
```

Expected:
- `gofmt -l` prints nothing, and vet is clean.
- `ok` for all three packages, including `TestEveryRouteIsInTheSpecOrExcusedByName`, `TestEverySpecPathIsARealRoute`, `TestOperationCountMatchesTheGeneratedTree`, `TestEveryAPIGroupIsDescribed` and `TestTheGeneratedTreeMatchesTheLock`.

- [ ] **Step 7: Run every negative control**

Make each edit, run the named test, see it FAIL, then revert the edit before the next one. All runs start with `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run <Name>`.

1. In `permissions.go`, give the `/api/logging/storage` row `min: auth.Maintainer`. Run `TestPermissionTableIsTheSpecification`: `PUT /api/logging/storage = "maintainer", want "admin"`.
2. In `lokiSave`, move `c, sp, err := parse(bodyOrEmpty(r))` and its `if` above step 1. Run `TestLoggingPutAnswersBeforeReadingTheBody`: the no-loki PUTs answer 400.
3. Delete the `loki.Writable` block. Run `TestLoggingPutAnswersBeforeReadingTheBody`: `unmounted: 202`.
4. In `wholeNumber`, change the float64 case to `return int(n), nil`. Run `TestLoggingPutRefusesABadBodyWith400`: the 1.5 case is not 400.
5. Delete step 5's `if` block. Run `TestLoggingPutAnswersAnUnchangedBodyByState/idle`: 202s and a job.
6. Remove `!s.jobs.IsBusy(inspect.ControlProject) && ` from step 5. Run `TestLoggingPutAnswersAnUnchangedBodyByState/busy`: `busy key: 200`.
7. Replace the `!ok` branch body with `writeJSON(w, 202, jsonx.O("job", job.Stub())); return nil`. Run `TestLoggingPutAnswersAnUnchangedBodyByState/a_refused_Start`: `held key: 202`.
8. Delete the `errors.Is(err, loki.ErrOneWay) || errors.Is(err, loki.ErrFixed)` block. Run `TestLoggingStoragePutKeepsS3OneWayAndFixed`: 500s.
9. Delete step 4's block. Run `TestLoggingStoragePutNeedsAnOwnableCredentialsFile`: 202, and the probe sent 2 requests. Under euid 0, report that this control could not run.
10. Move step 6's block below step 7's `writeJSON(w, 202, …)`, directly before `return nil`. Run `TestLoggingStoragePutProbesBeforeItRecordsAnything`: the refused probe answers 202, or leaves a job and a pending save.
11. Add `,omitempty` to `loggingStorageView.S3`'s tag. Run `TestLoggingGet/defaults`: `storage = type /`.
12. In `lokiEnabled`, change `return nil` to `f := false; return &f`. Run `TestLoggingGet/enabled`: the silent case reads false.
13. Add `Secret string \`json:"secretAccessKey"\`` to `loggingS3View`, and set `Secret: secretMask` in `loggingGet`. Run `TestLoggingGet/a_stored_S3_row`: `the GET leaks the secret or its mask`.

After reverting all 13, re-run Step 6's full command. Expected: `ok` for all three packages.

- [ ] **Step 8: The rbac conformance rows**

In `packages/conformance/test/api-rbac.test.ts`, replace

```ts
  { method: 'DELETE', path: '/api/tls/wildcard', min: 'admin', ok: 404 },
```

with

```ts
  { method: 'DELETE', path: '/api/tls/wildcard', min: 'admin', ok: 404 },
  // Loki's settings. The shim lists no control containers, so the allowed role gets the handler's
  // own answers: the read (enabled: false), and a PUT refused before its body is read — 409.
  // negative control: give the /api/logging/storage row auth.Maintainer in permissions.go — the
  // maintainer cell goes 403 → 409 and the matrix test reports it.
  { method: 'GET', path: '/api/logging', min: 'maintainer', ok: 200 },
  { method: 'PUT', path: '/api/logging', min: 'maintainer', ok: 409 },
  // Storage is admin: one save sends every node's logs to an outside bucket, for good.
  { method: 'PUT', path: '/api/logging/storage', min: 'admin', ok: 409 },
```

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-rbac.test.ts test/cli-api.test.ts test/api-openapi.test.ts
```

Expected: all pass. The rows iterate inside the existing `every route in the matrix…` test, which already fails under `PSTACK_IMPL=null`, so `expected-pass.json` does not change.

Run the TS negative control. Apply mutation 1 from Step 7, rebuild the binary, and re-run `PSTACK_IMPL=go bun test test/api-rbac.test.ts`. Expected: `wrong` lists `PUT /api/logging/storage as maintainer → want 403, got 409`. Revert, rebuild, and re-run to green.

- [ ] **Step 9: Docs — docs/usage.md**

In `docs/usage.md`, insert the following immediately before the line `### Why \`init\` is CLI-only, and always will be`. Slice 1's T6 section `### Turn Loki logging on or off: \`pstack logging\`` sits directly above that line, so this lands right after it. The anchor `#loki-settings` is the one T14's `?` link uses.

````markdown
### Loki settings

On a host with `pstack logging loki`, the Control page has a **Logging** panel. Behind it:

| Route | Least role | Body |
|---|---|---|
| `GET /api/logging` | `maintainer` | — |
| `PUT /api/logging` | `maintainer` | `{ retentionDays, chunks: { idlePeriodMinutes, maxAgeMinutes, targetSizeKiB, encoding } }` |
| `PUT /api/logging/storage` | `admin` | `{ type: "filesystem" }` or `{ type: "s3", endpoint, region, bucket, pathStyle, accessKeyId, secretAccessKey, cutover }` |

- A save that changes something answers `202 { job }`: a `loki-apply` job that restarts Loki. An unchanged save on an idle host answers `200 { "changed": false }`.
- Ranges and the earliest cutover come from `GET /api/logging` → `limits`.
- **S3 is one-way.** Once saved, `endpoint`, `region`, `bucket`, `pathStyle` and `cutover` are fixed; only the keys change. `cutover` is the first UTC day on S3.
- The secret is write-only. An empty, omitted or `••••••••` `secretAccessKey` keeps the stored one.
- The `loki-apply` transcript is viewer-readable and may name the endpoint and bucket in Loki's error lines; only the secret and key id are scrubbed.
- An S3 save first writes and deletes a `pstack-probe-<hex>` object. The probe runs from pstack's networks, not Loki's: an endpoint only Loki can reach fails after the cutover, not at save.
- **`config.yaml` is pstack's: hand edits are reverted** by the next save or pstack start. A live schema period is never dropped.
- `409 Loki is not running on this host`: run `pstack logging loki`. `409 the loki directory is not mounted here`: run `pstack upgrade`.

```console
$ curl -s -X PUT https://api.preview.example.com/api/logging \
    -H "authorization: Bearer $PSTACK_TOKEN" \
    -d '{"retentionDays":14,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}'
{
  "job": { "id": "loki-apply-pstack-control-3-…", "stack": "pstack-control", "action": "loki-apply", "state": "running" }
}
$ curl -s -X PUT https://api.preview.example.com/api/logging/storage \
    -H "authorization: Bearer $PSTACK_TOKEN" \
    -d '{"type":"s3","endpoint":"https://s3.eu-central-1.amazonaws.com","region":"eu-central-1","bucket":"pstack-logs","pathStyle":false,"accessKeyId":"AKIA…","secretAccessKey":"…","cutover":"2026-09-16"}'
{
  "error": "S3 refused the probe: 403 InvalidAccessKeyId"
}
```

```bash
pstack api logging get
pstack api logging set --data '{"retentionDays":14,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}'
```
````

In the §7e matrix, replace

```markdown
| `PUT`/`DELETE /api/tls/wildcard` | `admin` |
```

with

```markdown
| `PUT`/`DELETE /api/tls/wildcard` | `admin` |
| `GET`/`PUT /api/logging` | `maintainer` |
| `PUT /api/logging/storage` | `admin` |
```

- [ ] **Step 10: CHANGELOG**

In `packages/pstack/CHANGELOG.md`, under `## Unreleased`, append this bullet as the last bullet of `### Added`. T2, T6 and T8 have added bullets there already. If `### Added` is missing, add it as the first subsection under `## Unreleased`.

```markdown
- **Loki settings over the API.** `GET`/`PUT /api/logging` (maintainer) for retention and chunks,
  `PUT /api/logging/storage` (admin) for filesystem or S3; `pstack api logging get|set|storage-set`.
  A save that changes something answers `202 { job }`; an unchanged one on an idle host, `200`.
```

- [ ] **Step 11: Final package check**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal && go vet ./internal/api/ ./internal/cli/ ./internal/apicli/ && go test -race -timeout 120s ./internal/api/ ./internal/cli/ ./internal/apicli/
cd /Volumes/S1/code/preview-stacks && but diff
```

Expected:
- `gofmt -l` prints nothing, and all three packages report `ok`.
- `but diff` shows only this task's files: routes_logging.go, routes_logging_test.go, routes.go, permissions.go, permissions_test.go, openapi.yaml, zz_generated.go, oascmd.lock.json, apicli.go, cli/api.go, api-rbac.test.ts, and this task's hunks in usage.md and CHANGELOG.md. No golden changes.

- [ ] **Step 12: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs for exactly these, and only this task's hunks in the two docs:
- `packages/pstack/internal/api/routes_logging.go`, `routes_logging_test.go`, `routes.go`, `permissions.go`, `permissions_test.go`
- `packages/pstack/api/openapi.yaml`
- `packages/pstack/internal/apicli/zz_generated.go`, `oascmd.lock.json`, `apicli.go`
- `packages/pstack/internal/cli/api.go`
- `packages/conformance/test/api-rbac.test.ts`
- `docs/usage.md`, `packages/pstack/CHANGELOG.md`

Never commit `packages/conformance/golden/host/db/*`, `.superpowers/` or `packages/pstack/bin/pstack`.

```bash
but commit -b claude/loki-logging-settings -m "feat(api): GET/PUT /api/logging and PUT /api/logging/storage" <ids>
```

---

### Task 13: client: logging.get/set/setStorage

**Files:**
- Modify: `packages/client/src/types.ts` (anchor edits by quoted existing text; line numbers advisory, slice 1 is moving them)
- Modify: `packages/client/src/index.ts`
- Modify: `packages/pstack/CHANGELOG.md`
- Test: `packages/client/test/client.test.ts`

**Interfaces:**
- Consumes:
  - `GET /api/logging` (maintainer), `PUT /api/logging` (maintainer), `PUT /api/logging/storage` (admin) from T12. The GET body is the T12 `loggingView` (spec:326-344). A PUT on a host with no Loki answers before reading the body: 503 `{"error":"docker did not answer"}` when `inspect.ControlRuntime` is not `Reachable`, and 409 `{"error":"Loki is not running on this host — run pstack logging loki on the host"}` when no `loki` container exists (spec:14, spec:385). Both go through `writeError` → `errorBody{Error}` (`packages/pstack/internal/api/http.go:40-42`), so `PstackError.message` is the server's sentence (`packages/client/src/index.ts:65`).
  - An unmatched route answers 404 `not found` (`packages/pstack/internal/api/routes.go:601`). The negative controls rely on this.
  - Existing client pieces: `get`/`put`/`post` (`index.ts:142-144`), `JobStub` (`types.ts:159`), `PstackError` (`index.ts:59-71`), and `bootServer` from the conformance harness (`packages/conformance/harness/server.ts`). That harness uses the Go binary at `packages/pstack/bin/pstack` (`harness/impl.ts` `GO_BIN`), with the inherited `PATH` and no docker shim.
- Produces (nothing later in slice 2 imports these; T14 mirrors the types by hand into `apps/ui/src/api/types.ts`):
  - `export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake' | 'loki-apply';`
  - `export type LokiChunks = { idlePeriodMinutes: number; maxAgeMinutes: number; targetSizeKiB: number; encoding: string };`
  - `export type LokiS3 = { endpoint: string; region: string; bucket: string; pathStyle: boolean; accessKeyId: string; secretSet: boolean; cutover: string };`
  - `export type LokiLimits = { retentionDays: { min: number; max: number }; idlePeriodMinutes: { min: number; max: number }; maxAgeMinutes: { min: number; max: number }; targetSizeKiB: { min: number; max: number }; encodings: string[]; earliestCutover: string };`
  - `export type LokiSettings = { enabled: boolean | null; source: 'db' | 'default'; updatedAt: number | null; retentionDays: number; chunks: LokiChunks; storage: { type: 'filesystem' | 's3'; s3: LokiS3 | null }; limits: LokiLimits };`
  - `export type LokiStorageInput = { type: 'filesystem' } | { type: 's3'; endpoint: string; region: string; bucket: string; pathStyle: boolean; accessKeyId: string; secretAccessKey?: string; cutover: string };`
  - `client.logging`:
    - `get: () => Promise<LokiSettings>`
    - `set: (body: { retentionDays: number; chunks: LokiChunks }) => Promise<{ job: JobStub } | { changed: false }>`
    - `setStorage: (body: LokiStorageInput) => Promise<{ job: JobStub } | { changed: false }>`

**On this machine:** there is no `docker` binary (`command -v docker` prints nothing), so the spawned server takes the 503 path. A CI runner has docker but no `pstack-control` project, so `idsByLabel` returns ok with no ids (`packages/pstack/internal/inspect/inspect.go:187-193`) and the server takes the 409 path. The test accepts either, the way the DELETE assertion does (`client.test.ts:76-93`).

---

- [ ] **Step 1: Write the failing test**

  In `packages/client/test/client.test.ts`, insert the block below directly before this existing line (currently :373):

  ```ts
  describe('verifyWebhook — the half that lives in the receiver', () => {
  ```

  Insert:

  ```ts
  describe('loki settings: the logging block', () => {
    // negative control: in src/index.ts, point logging.get at '/api/settings' → fails at the `enabled` assertion (expected [null, false] to contain undefined)
    test('get: the saved settings and the server limits, never a secret', async () => {
      const s = await client.logging.get();
      // No docker here, or docker with no control stack: never `true`.
      expect([null, false]).toContain(s.enabled);
      expect(s).toMatchObject({
        source: 'default',
        updatedAt: null,
        retentionDays: 7,
        chunks: { idlePeriodMinutes: 30, maxAgeMinutes: 120, targetSizeKiB: 1536, encoding: 'snappy' },
        storage: { type: 'filesystem', s3: null },
      });
      expect(Array.isArray(s.limits.encodings)).toBe(true);
      expect(s.limits.encodings).toEqual(['snappy', 'gzip', 'lz4', 'zstd']);
      expect(s.limits.retentionDays).toEqual({ min: 1, max: 365 });
      expect(s.limits.earliestCutover).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(JSON.stringify(s)).not.toContain('••••••••');
    });

    // negative control: in src/index.ts, make logging.set call `post` instead of `put` → 404, `expect(e.status).toBe(409)` fails; point logging.setStorage at '/api/logging/storages' → 404, the setStorage status assertion fails
    test('set and setStorage are refused with the server\'s reason on a host without Loki', async () => {
      const err = await client.logging
        .set({ retentionDays: 14, chunks: { idlePeriodMinutes: 30, maxAgeMinutes: 120, targetSizeKiB: 1536, encoding: 'snappy' } })
        .catch((e: PstackError) => e);
      expect(err).toBeInstanceOf(PstackError);
      const e = err as PstackError;
      /*
       * Which refusal you get depends on the machine, as with DELETE above:
       *
       *   docker absent   → 503, "docker did not answer"
       *   docker present  → 409, no control stack here, so no loki container
       */
      if (e.status === 503) {
        expect(e.message).toContain('docker did not answer');
      } else {
        expect(e.status).toBe(409);
        expect(e.message).toContain('Loki is not running on this host');
      }
      // The storage route refuses at the same step, before it reads the body.
      await expect(client.logging.setStorage({ type: 'filesystem' })).rejects.toMatchObject({ status: e.status });
    });
  });

  ```

  The `// negative control:` comments sit above each test, in the form used at `client.test.ts:249-252` and :314. `tsconfig.json` `include` covers `test/`, so the test must type-check under `strict` + `noUnusedLocals`. It uses no new import: the chunks object is inlined and checked against `set`'s parameter type.

- [ ] **Step 2: Run it and watch it fail**

  Rebuild the binary first. A stale binary answers 404 on `/api/logging`, and the red step would then show the wrong failure.

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/client && bun test --test-name-pattern 'logging'
  ```

  Expected: 2 fail, each with `TypeError: undefined is not an object (evaluating 'client.logging.get')` (the second with `'client.logging.set'`). `bun test` does not type-check. Separately:

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/client && bun run typecheck
  ```

  Expected: `test/client.test.ts(...): error TS2339: Property 'logging' does not exist on type ...`.

- [ ] **Step 3: Implement the types**

  In `packages/client/src/types.ts`, replace:

  ```ts
  /** `sleep` takes the compose project down (volumes and axes stay); `wake` is `up` recorded under its own name. */
  export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake';
  ```

  with:

  ```ts
  /**
   * `sleep` takes the compose project down (volumes and axes stay); `wake` is `up` recorded under its own name.
   * `loki-apply` writes Loki's settings and restarts Loki, on `pstack-control`.
   */
  export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake' | 'loki-apply';
  ```

  Then append the Loki types after `SsoConfigResponse`, the last type in the file (next to `SsoProviderEntry`, per spec:629-631). Replace:

  ```ts
    /** Register THIS with every provider, byte for byte. Built server-side; never guess it. */
    callbackUrl: string;
    presets: SsoPreset[];
  };
  ```

  with:

  ```ts
    /** Register THIS with every provider, byte for byte. Built server-side; never guess it. */
    callbackUrl: string;
    presets: SsoPreset[];
  };

  /** Loki's chunk settings: read from `GET /api/logging`, sent by `PUT /api/logging`. */
  export type LokiChunks = {
    idlePeriodMinutes: number;
    maxAgeMinutes: number;
    targetSizeKiB: number;
    encoding: string;
  };

  /** The saved S3 storage. */
  export type LokiS3 = {
    endpoint: string;
    region: string;
    bucket: string;
    pathStyle: boolean;
    accessKeyId: string;
    /** All a read learns about the secret. The value has no read path. */
    secretSet: boolean;
    /** `YYYY-MM-DD`, UTC. The first day Loki writes to the bucket. */
    cutover: string;
  };

  /** The server's ranges. Read these; never hard-code them. */
  export type LokiLimits = {
    retentionDays: { min: number; max: number };
    idlePeriodMinutes: { min: number; max: number };
    maxAgeMinutes: { min: number; max: number };
    targetSizeKiB: { min: number; max: number };
    encodings: string[];
    /** The earliest `cutover` a switch to S3 accepts. */
    earliestCutover: string;
  };

  /** `GET /api/logging`: what an operator saved, not what Loki runs. */
  export type LokiSettings = {
    /** `null`: docker did not answer. `false`: no Loki on this host. */
    enabled: boolean | null;
    source: 'db' | 'default';
    /** `null` when `source` is `default`. */
    updatedAt: number | null;
    retentionDays: number;
    chunks: LokiChunks;
    storage: { type: 'filesystem' | 's3'; s3: LokiS3 | null };
    limits: LokiLimits;
  };

  /** `PUT /api/logging/storage`. S3 is one-way. Omit `secretAccessKey`, or send `''`, to keep the stored one. */
  export type LokiStorageInput =
    | { type: 'filesystem' }
    | {
        type: 's3';
        endpoint: string;
        region: string;
        bucket: string;
        pathStyle: boolean;
        accessKeyId: string;
        secretAccessKey?: string;
        cutover: string;
      };
  ```

- [ ] **Step 4: Implement the client block**

  In `packages/client/src/index.ts`, extend the type import. Replace:

  ```ts
    Job,
    Kind,
    Logs,
    Me,
  ```

  with:

  ```ts
    Job,
    JobStub,
    Kind,
    Logs,
    LokiChunks,
    LokiSettings,
    LokiStorageInput,
    Me,
  ```

  `JobStub` is exported at `types.ts:159` but index.ts does not import it today. `LokiS3` and `LokiLimits` are not referenced here: they reach callers through `export * from './types.ts'` (index.ts:56), and importing them would trip `noUnusedLocals`.

  Then add the block after `settings`. Replace:

  ```ts
        set: (key: SettingKey, value: number | Role) =>
          put<SettingWritten>(`/api/settings/${enc(key)}`, { value }),
      },
  ```

  with:

  ```ts
        set: (key: SettingKey, value: number | Role) =>
          put<SettingWritten>(`/api/settings/${enc(key)}`, { value }),
      },

      logging: {
        /** Loki's settings, as saved. Never the S3 secret — `secretSet` is all a read learns. */
        get: () => get<LokiSettings>('/api/logging'),
        /** Chunks and retention. Maintainer. A 202 is a job: it restarts Loki. */
        set: (body: { retentionDays: number; chunks: LokiChunks }) =>
          put<{ job: JobStub } | { changed: false }>('/api/logging', body),
        /** Storage. Admin. S3 is one-way; omit `secretAccessKey` to keep the stored one. */
        setStorage: (body: LokiStorageInput) =>
          put<{ job: JobStub } | { changed: false }>('/api/logging/storage', body),
      },
  ```

  The block and its doc comments are spec:616-627 verbatim. The file header (index.ts:1-27), `package.json` version and `README.md` are not touched.

- [ ] **Step 5: Run it and watch it pass**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/client && bun test --test-name-pattern 'logging'
  ```

  Expected: `2 pass`, `0 fail`.

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/client && bun run typecheck
  ```

  Expected: exit 0, no output.

- [ ] **Step 6: Run the negative controls**

  Run each mutation, watch the named assertion fail, then revert it and confirm green again. Every run is `cd /Volumes/S1/code/preview-stacks/packages/client && bun test --test-name-pattern 'logging'`.

  1. In `src/index.ts` change `get<LokiSettings>('/api/logging')` to `get<LokiSettings>('/api/settings')`. Expected: `get: the saved settings…` fails at `expect([null, false]).toContain(s.enabled)`, because the settings body has no `enabled`. Revert.
  2. In `src/index.ts`, `logging.set`, change `put<{ job: JobStub } | { changed: false }>('/api/logging', body)` to `post<{ job: JobStub } | { changed: false }>('/api/logging', body)`. Expected: the refusal test fails at `expect(e.status).toBe(409)` with `Received: 404`, because routes.go dispatches only GET and PUT on `/api/logging` and falls through to `routes.go:601`. Revert.
  3. In `src/index.ts`, `logging.setStorage`, change `'/api/logging/storage'` to `'/api/logging/storages'`. Expected: the last `rejects.toMatchObject({ status: e.status })` fails, with 404 received against 503 or 409. Revert.

- [ ] **Step 7: CHANGELOG**

  In `packages/pstack/CHANGELOG.md`, under `## Unreleased` → `### Added`, add this as the last bullet of that list, directly above that section's `### Changed` heading. Slice 1's T14 creates the section and earlier slice-2 tasks append to it. If `## Unreleased` is absent, insert `## Unreleased\n\n### Added\n\n` between `# Changelog` and the first `## 0.` heading.

  ```markdown
  - **`logging` in the client SDK:** `logging.get()`, `logging.set()` and `logging.setStorage()`,
    with the `LokiSettings`, `LokiChunks`, `LokiS3`, `LokiLimits` and `LokiStorageInput` types.
    `JobAction` gains `loki-apply`.
  ```

- [ ] **Step 8: Full client gate**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/client && bun run check
  ```

  Expected: every test passes (the new `loki settings: the logging block` describe included), `tsc --noEmit` exits 0, and `bun run build` writes `dist/`. `dist/` is ignored (`.gitignore:4`), so nothing new appears in `but status`.

- [ ] **Step 9: Commit**

  ```sh
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Take the file IDs for `packages/client/src/index.ts`, `packages/client/src/types.ts`, `packages/client/test/client.test.ts` and `packages/pstack/CHANGELOG.md` only. Never take `packages/conformance/golden/host/db/pstack.db-shm`, `pstack.db-wal` or anything under `.superpowers/`.

  ```sh
  but commit -b claude/loki-logging-settings -m "feat(client): logging.get/set/setStorage and the loki-apply job action" <index.ts-id> <types.ts-id> <client.test.ts-id> <CHANGELOG.md-id>
  ```

---

### Task 14: UI: the Logging panel on the Control page

**Files:**
- Create: none
- Modify: `apps/ui/src/api/types.ts` (anchor edits by quoted existing text; line numbers advisory, since slice 1 is moving them)
- Modify: `apps/ui/src/composables/useFormat.ts`
- Modify: `apps/ui/src/views/ControlView.vue`
- Modify: `packages/pstack/CHANGELOG.md`
- Test: `apps/ui/test/useFormat.test.ts` (new; see contractAmendments)

**Interfaces:**
- Consumes:
  - `GET /api/logging`, unwrapped: `{ enabled, source, updatedAt, retentionDays, chunks, storage: { type, s3 }, limits }` (T12 `loggingView`).
  - `PUT /api/logging` with `{ retentionDays, chunks }`, and `PUT /api/logging/storage` with `LokiStorageInput`. Both answer 202 `{ job: JobStub }`, 200 `{ changed: false }`, or 400/409/503 `{ error }` (T12).
  - `jobs.LokiApply = "loki-apply"` (T6).
  - `GET /api/jobs/:id` → `{ job }` (`JobResponse`, types.ts:278).
  - Existing:
    - `api` (client.ts:123-146)
    - `can(min)` (useAuth.ts:63)
    - `state.jobs` (useControlPlane.ts:17-25, polled by App.vue:44-49 every 7s)
    - `isTerminal` and `supersededBy` (useJobQueue.ts:22, :56)
    - `toast(kind, text, { to, toLabel })` (useToasts.ts:31)
    - `usePolling` (usePolling.ts:15)
    - `ActionButton`'s `confirm` + `@run` (ActionButton.vue:37-40, :71-76)
    - `ErrorNote` (ErrorNote.vue:14)
    - `SelectMenu` `{ id, label, options, v-model }` (SelectMenu.vue:34-50)
  - The `Loki settings` heading T12 adds to usage.md.
- Produces:
  - `apps/ui/src/api/types.ts`: `LokiChunks`, `LokiS3`, `LokiLimits`, `LokiSettings`, `LokiStorageInput` (the same shapes as T13), and `JobAction` gains `'loki-apply'`.
  - `ACTION_LABELS['loki-apply'] = 'Loki settings'`.
  - In ControlView.vue: `logging`, `loadLogging`, `usePolling(loadLogging, 10_000)` and `applyingJob`. Nothing is exported from the view.
  - The basic UI (`packages/pstack/ui/index.html:409, 1231`) prints `j.action` raw and is unchanged.

- [ ] **Step 1: Write the failing test**

This app has no component harness (`apps/ui/test/useJobQueue.test.ts:1-9`). Logic that can be proven is proven from `.ts`. The label is the one pure piece of this task. Create `apps/ui/test/useFormat.test.ts`:

```ts
/**
 * Action labels, tested directly — there is no component harness in this app (useJobQueue.test.ts).
 *
 * Outside `src/` on purpose, like the other two: a `bun:test` import under `src/` fails `vue-tsc`.
 * Run with `cd apps/ui && bun test test/useFormat.test.ts`.
 */
import { expect, test } from 'bun:test';
import { actionLabel } from '../src/composables/useFormat';

test('a loki-apply job reads as Loki settings', () => {
  // negative control: delete the 'loki-apply' row from ACTION_LABELS — actionLabel falls back to the
  // raw verb, and the Jobs page lists a job called `loki-apply`.
  expect(actionLabel('loki-apply')).toBe('Loki settings');
});
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/apps/ui && bun test test/useFormat.test.ts
```

Expected: `0 pass`, `1 fail`, with:

```
error: expect(received).toBe(expected)

Expected: "Loki settings"
Received: "loki-apply"
```

This red run is the negative control's own state, because the row does not exist yet.

- [ ] **Step 3: Implement the label**

In `apps/ui/src/composables/useFormat.ts`, replace:

```ts
  sleep: 'Sleep',
  wake: 'Wake',
};
```

with:

```ts
  sleep: 'Sleep',
  wake: 'Wake',
  'loki-apply': 'Loki settings',
};
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/apps/ui && bun test test/useFormat.test.ts
```

Expected: `1 pass`, `0 fail`.

- [ ] **Step 5: Write the panel**

In `apps/ui/src/views/ControlView.vue`, make four edits.

**5a. Imports.** Replace:

```ts
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { ControlRuntime, DomainsStatus, TlsRedeploy, TlsStatus } from '../api/types';
import { usePolling } from '../composables/usePolling';
import { can } from '../composables/useAuth';
import { ago, sentence, stamp } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SkeletonList from '../components/SkeletonList.vue';
```

with:

```ts
import { computed, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type {
  ControlRuntime,
  DomainsStatus,
  JobResponse,
  JobStub,
  LokiSettings,
  LokiStorageInput,
  TlsRedeploy,
  TlsStatus,
} from '../api/types';
import { usePolling } from '../composables/usePolling';
import { can } from '../composables/useAuth';
import { state } from '../composables/useControlPlane';
import { ago, sentence, stamp } from '../composables/useFormat';
import { isTerminal, supersededBy } from '../composables/useJobQueue';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SelectMenu from '../components/SelectMenu.vue';
import SkeletonList from '../components/SkeletonList.vue';
```

**5b. Script.** Replace the end of `redeployAll` and the closing tag:

```ts
  toast('ok', `Redeploying ${r.body.started.length}, skipped ${r.body.skipped.length}.`);
}
</script>
```

with:

```ts
  toast('ok', `Redeploying ${r.body.started.length}, skipped ${r.body.skipped.length}.`);
}

// ── Loki's settings ───────────────────────────────────────────────────────────────────────────────
const logging = ref<LokiSettings | null>(null);
const loggingError = ref('');
const savingLogging = ref(false);
/** The apply job a save here started — or the successor carrying it — until it ends. */
const applyingJob = ref<string | null>(null);

/** `type="number"` hands back a number once the box parses and '' while it is empty (useJobQueue.ts:64-71). */
type Box = number | string;
const draft = ref<{ retentionDays: Box; idlePeriodMinutes: Box; maxAgeMinutes: Box; targetSizeKiB: Box; encoding: string }>({
  retentionDays: '',
  idlePeriodMinutes: '',
  maxAgeMinutes: '',
  targetSizeKiB: '',
  encoding: '',
});
const storageType = ref<'filesystem' | 's3'>('filesystem');
const s3Draft = ref({ endpoint: '', region: '', bucket: '', pathStyle: false, accessKeyId: '' });
const secretDraft = ref('');

// Refill only when the SAVED values change (a rollback included): the 10s poll must not wipe typing.
watch(
  () => logging.value && JSON.stringify([logging.value.retentionDays, logging.value.chunks, logging.value.storage]),
  () => {
    const l = logging.value;
    if (!l) return;
    draft.value = { retentionDays: l.retentionDays, ...l.chunks };
    storageType.value = l.storage.type;
    const s3 = l.storage.s3;
    s3Draft.value = {
      endpoint: s3?.endpoint ?? '',
      region: s3?.region ?? '',
      bucket: s3?.bucket ?? '',
      pathStyle: s3?.pathStyle ?? false,
      accessKeyId: s3?.accessKeyId ?? '',
    };
  },
);

const onS3 = computed(() => logging.value?.storage.type === 's3');
/** S3 is one-way and fixed once saved; below admin nothing in storage is writable. */
const storageLocked = computed(() => onS3.value || !can('admin'));
const encodings = computed(() => (logging.value?.limits.encodings ?? []).map((e) => ({ value: e, label: e })));
const storageBadge = computed(() => {
  const s3 = logging.value?.storage.s3;
  if (!s3) return 'Filesystem';
  // The cutover is 00:00 UTC of its day; before it Loki still writes to the filesystem.
  return s3.cutover > new Date().toISOString().slice(0, 10) ? `S3 from ${s3.cutover}` : 'S3';
});

/** One read of the followed job: keep following, follow its successor, or end with a toast. */
async function follow(id: string): Promise<void> {
  const r = await api.get<JobResponse>(`/api/jobs/${encodeURIComponent(id)}`);
  if (applyingJob.value !== id) return; // a newer save took over during the read
  if (r.status === 404) {
    applyingJob.value = null; // the record is gone
    return;
  }
  if (!r.ok || !isTerminal(r.body.job.state)) return;
  const job = r.body.job;
  if (job.state === 'superseded') {
    // No toast: the successor carries this save. Keep this id until the shell's job list shows it.
    applyingJob.value = supersededBy(job, state.jobs)?.id ?? id;
    return;
  }
  applyingJob.value = null;
  const to = { to: `/jobs/${encodeURIComponent(id)}` };
  if (job.state === 'ok') toast('ok', 'Applied.', to);
  else if (job.state === 'cancelled') toast('warn', 'Cancelled.', to);
  else toast('error', 'Apply failed.', to); // `leaked` cannot happen: an apply has no assert_gone
}

async function loadLogging(): Promise<void> {
  if (!can('maintainer')) return; // the read is maintainer's; below it every tick would 403
  if (applyingJob.value) await follow(applyingJob.value);
  const r = await api.get<LokiSettings>('/api/logging');
  if (r.ok) logging.value = r.body;
}
usePolling(loadLogging, 10_000);

async function saveLogging(path: string, body: unknown): Promise<void> {
  savingLogging.value = true;
  const r = await api.put<{ job: JobStub } | { changed: false }>(path, body);
  savingLogging.value = false;
  if (!r.ok) {
    loggingError.value = r.body.error ?? `HTTP ${r.status}`;
    return;
  }
  loggingError.value = '';
  secretDraft.value = '';
  const res = r.body;
  if ('job' in res) {
    applyingJob.value = res.job.id;
    toast('info', 'Applying.', { to: `/jobs/${encodeURIComponent(res.job.id)}`, toLabel: 'Follow' });
  } else {
    toast('ok', 'No change.');
  }
  await loadLogging();
}

const saveChunks = () =>
  saveLogging('/api/logging', {
    retentionDays: Number(draft.value.retentionDays),
    chunks: {
      idlePeriodMinutes: Number(draft.value.idlePeriodMinutes),
      maxAgeMinutes: Number(draft.value.maxAgeMinutes),
      targetSizeKiB: Number(draft.value.targetSizeKiB),
      encoding: draft.value.encoding,
    },
  });

async function saveStorage(): Promise<void> {
  const l = logging.value;
  if (!l) return;
  const body: LokiStorageInput = {
    type: 's3',
    ...s3Draft.value,
    secretAccessKey: secretDraft.value, // '' keeps the stored secret
    cutover: l.storage.s3?.cutover ?? l.limits.earliestCutover, // the date the confirm named
  };
  await saveLogging('/api/logging/storage', body);
}
</script>
```

The code makes no range check of its own. The inputs carry `min` and `max` from `limits`, and the server's 400 is the enforcement, shown through ErrorNote.

**5c. Template.** Replace the end of the Certificates panel and the page's closing tags:

```html
        — watch them under Jobs.
      </p>
    </section>
  </div>
</template>
```

with:

```html
        — watch them under Jobs.
      </p>
    </section>

    <!-- ============================ Loki's settings ============================ -->
    <!-- Hidden when this host runs no Loki. The body is what was saved; the apply job says what ran. -->
    <section v-if="can('maintainer') && logging && logging.enabled !== false" class="panel settings-form">
      <div class="phead">
        <h2 class="section">Logging</h2>
        <a
          class="hint-btn"
          href="https://github.com/samishal1998/preview-stacks/blob/main/docs/usage.md#loki-settings"
          target="_blank"
          rel="noreferrer"
          aria-label="Help"
          >?</a
        >
        <span class="grow" />
        <RouterLink v-if="applyingJob" class="badge running" :to="`/jobs/${encodeURIComponent(applyingJob)}`">Applying</RouterLink>
        <span class="badge info">{{ storageBadge }}</span>
      </div>

      <p v-if="logging.enabled === null" class="mute">Docker did not answer.</p>
      <template v-else>
        <ErrorNote v-if="loggingError" :text="loggingError" />

        <div class="field">
          <label for="loki-retention">Retention</label>
          <div class="row">
            <input
              id="loki-retention"
              v-model="draft.retentionDays"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.retentionDays.min"
              :max="logging.limits.retentionDays.max"
              style="width: 8rem"
            />
            <span class="mute">days</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-idle">Chunk idle</label>
          <div class="row">
            <input
              id="loki-idle"
              v-model="draft.idlePeriodMinutes"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.idlePeriodMinutes.min"
              :max="logging.limits.idlePeriodMinutes.max"
              style="width: 8rem"
            />
            <span class="mute">min</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-max-age">Chunk max age</label>
          <div class="row">
            <input
              id="loki-max-age"
              v-model="draft.maxAgeMinutes"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.maxAgeMinutes.min"
              :max="logging.limits.maxAgeMinutes.max"
              style="width: 8rem"
            />
            <span class="mute">min</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-size">Chunk size</label>
          <div class="row">
            <input
              id="loki-size"
              v-model="draft.targetSizeKiB"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.targetSizeKiB.min"
              :max="logging.limits.targetSizeKiB.max"
              style="width: 8rem"
            />
            <span class="mute">KiB</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-encoding">Encoding</label>
          <SelectMenu id="loki-encoding" v-model="draft.encoding" label="Encoding" :options="encodings" />
        </div>
        <div class="row" style="margin-top: var(--s4)">
          <ActionButton variant="primary" :pending="savingLogging" @click="saveChunks">Save</ActionButton>
          <span class="mute">Restarts Loki.</span>
        </div>

        <!-- Toggles, not a tablist (ui-rules "No ARIA is better than bad ARIA"). -->
        <div class="row" role="group" aria-label="Storage" style="margin-top: var(--s5)">
          <button :aria-pressed="storageType === 'filesystem'" :disabled="storageLocked" @click="storageType = 'filesystem'">
            Filesystem
          </button>
          <button :aria-pressed="storageType === 's3'" :disabled="storageLocked" @click="storageType = 's3'">S3</button>
        </div>
        <!-- The reason sits beside the disabled controls as text: a disabled control leaves the tab order. -->
        <p v-if="storageLocked" class="hint">{{ can('admin') ? 'Fixed once saved.' : 'Admin only.' }}</p>

        <template v-if="storageType === 's3'">
          <div class="field">
            <label for="loki-endpoint">Endpoint</label>
            <input id="loki-endpoint" v-model="s3Draft.endpoint" type="url" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label for="loki-region">Region</label>
            <input id="loki-region" v-model="s3Draft.region" type="text" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label for="loki-bucket">Bucket</label>
            <input id="loki-bucket" v-model="s3Draft.bucket" type="text" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label class="check"><input v-model="s3Draft.pathStyle" type="checkbox" :disabled="storageLocked" /> Path-style</label>
          </div>
          <div class="field">
            <label for="loki-key-id">Access key ID</label>
            <input
              id="loki-key-id"
              v-model="s3Draft.accessKeyId"
              type="text"
              class="mono"
              spellcheck="false"
              autocomplete="off"
              :disabled="!can('admin')"
            />
          </div>
          <template v-if="can('admin')">
            <div class="field">
              <label for="loki-secret">Secret access key</label>
              <input id="loki-secret" v-model="secretDraft" type="password" spellcheck="false" autocomplete="new-password" />
              <p class="hint">Write-only.{{ logging.storage.s3?.secretSet ? ' Leave empty to keep.' : '' }}</p>
            </div>
            <!-- Two buttons, not one with a conditional `confirm`: with `confirm` set, a parent @click
                 still fires on the arming click (ActionButton.vue:71). -->
            <div class="row" style="margin-top: var(--s4)">
              <ActionButton v-if="onS3" :pending="savingLogging" @click="saveStorage">Save keys</ActionButton>
              <ActionButton
                v-else
                :pending="savingLogging"
                :confirm="`One-way. S3 from ${logging.limits.earliestCutover} UTC?`"
                @run="saveStorage"
              >
                Switch to S3
              </ActionButton>
            </div>
          </template>
        </template>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* A pressed toggle. app.css styles one only inside EquivalentCommand's own scope (:131). */
button[aria-pressed='true'] {
  background: var(--accent-soft);
  color: var(--accent);
}
/* app.css has no rule for a disabled text input (SettingsView.vue:309-315). */
input:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
</style>
```

- [ ] **Step 6: Typecheck and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck
```

Expected: exit non-zero, with `src/views/ControlView.vue:…: error TS2305: Module '"../api/types"' has no exported member 'LokiSettings'.` and the same error for `'LokiStorageInput'`.

- [ ] **Step 7: Add the types**

In `apps/ui/src/api/types.ts`, make two edits.

**7a.** Replace:

```ts
export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake';
```

with:

```ts
export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake' | 'loki-apply';
```

**7b.** Replace the end of `DomainsStatus`, the last type in the file:

```ts
  /** The host's certificate mode, since it decides what an added domain costs. */
  mode: string;
  note: string;
};
```

with:

```ts
  /** The host's certificate mode, since it decides what an added domain costs. */
  mode: string;
  note: string;
};

// ── Loki's settings ──────────────────────────────────────────────────────────────────────────────

export type LokiChunks = { idlePeriodMinutes: number; maxAgeMinutes: number; targetSizeKiB: number; encoding: string };

/** S3 as saved. `secretSet` is all a read learns: the secret has no read path. */
export type LokiS3 = {
  endpoint: string;
  region: string;
  bucket: string;
  pathStyle: boolean;
  accessKeyId: string;
  secretSet: boolean;
  /** `YYYY-MM-DD`, 00:00 UTC: when Loki starts writing to S3. */
  cutover: string;
};

/** The server's ranges and the first cutover date. Never hard-coded here. */
export type LokiLimits = {
  retentionDays: { min: number; max: number };
  idlePeriodMinutes: { min: number; max: number };
  maxAgeMinutes: { min: number; max: number };
  targetSizeKiB: { min: number; max: number };
  encodings: string[];
  earliestCutover: string;
};

/** What `GET /api/logging` answers, unwrapped. Maintainer. */
export type LokiSettings = {
  /** Tri-state: null ⇒ docker did not answer; false ⇒ no loki container on this host. */
  enabled: boolean | null;
  source: 'db' | 'default';
  /** null on `default`. */
  updatedAt: number | null;
  retentionDays: number;
  chunks: LokiChunks;
  storage: { type: 'filesystem' | 's3'; s3: LokiS3 | null };
  limits: LokiLimits;
};

/** `PUT /api/logging/storage`. Admin. An empty or absent `secretAccessKey` keeps the stored one. */
export type LokiStorageInput =
  | { type: 'filesystem' }
  | {
      type: 's3';
      endpoint: string;
      region: string;
      bucket: string;
      pathStyle: boolean;
      accessKeyId: string;
      secretAccessKey?: string;
      cutover: string;
    };
```

- [ ] **Step 8: Run typecheck and the tests, and watch them pass**

```bash
cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck && bun run test
```

Expected: `vue-tsc --build` exits 0 with no output. `bun test` reports every file passing, `test/useFormat.test.ts` among them, with `0 fail`.

If vue-tsc rejects `'job' in res`, narrow with `if (r.status === 202)` and cast `res as { job: JobStub }`. Change nothing else.

- [ ] **Step 9: Look at it in a browser**

AGENTS.md:452-457 requires this. Build a docker shim that lists one loki container:

```bash
SCRATCH=/private/tmp/claude-501/-Volumes-S1-code-preview-stacks/7cc16894-c17e-4570-af5f-bf665845e6dd/scratchpad
mkdir -p "$SCRATCH/loki-ui/bin" "$SCRATCH/loki-ui/data/control/loki"
cp /Volumes/S1/code/preview-stacks/packages/pstack/templates/control/loki/config.yaml "$SCRATCH/loki-ui/data/control/loki/config.yaml"
cat > "$SCRATCH/loki-ui/bin/docker" <<'EOF'
#!/bin/sh
D=$(dirname "$0")
case "$*" in
  "ps -aq --filter label=com.docker.compose.project=pstack-control")
    [ -f "$D/down" ] && exit 1
    [ -f "$D/none" ] && exit 0
    printf 'l1\n' ;;
  "inspect l1")
    printf '%s\n' '[{"Id":"l1","Name":"/pstack-control-loki-1","RestartCount":0,"Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}},"State":{"Status":"running","StartedAt":"2026-09-15T00:00:00Z"},"HostConfig":{"Memory":0}}]' ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$SCRATCH/loki-ui/bin/docker"
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
PATH="$SCRATCH/loki-ui/bin:$PATH" PSTACK_TOKEN=dev PSTACK_DATA="$SCRATCH/loki-ui/data" \
  PSTACK_LOKI_DIR="$SCRATCH/loki-ui/data/control/loki" packages/pstack/bin/pstack serve     # background, :7878
cd /Volumes/S1/code/preview-stacks/apps/ui && bun run dev                                  # background, :5273
```

`ps` exits 1 while a `down` file exists and lists nothing while a `none` file exists. Every other docker call (`run … -verify-config`, `restart`, `exec … -health`, `logs`) exits 0, so an apply succeeds.

Open `http://127.0.0.1:5273/settings`, set the bearer token to `dev`, then open `/control`. Check each item:

1. The `Logging` panel renders after Certificates, with the badge `Filesystem` and the values 7 / 30 / 120 / 1536 / snappy. `Restarts Loki.` sits beside `Save`.
2. Set Retention to `14` and click Save. You get the `Applying.` toast and an `Applying` badge linking `/jobs/<id>`. Within about 10s the `Applied.` toast appears and the badge goes. `/jobs` lists the job as `Loki settings`. Click Save again with nothing changed: `No change.`
3. Set Retention to `0` and click Save. The ErrorNote shows the server's 400 naming `retentionDays`.
4. Click `S3`. Fill Endpoint `https://s3.example.com`, Region `us-east-1`, Bucket `pstack-logs`, Access key ID `AKIAEXAMPLE`, Secret access key `example-secret-1`. The hint reads `Write-only.`. Click `Switch to S3`: the label becomes `One-way. S3 from <limits.earliestCutover> UTC?`. Click again. The ErrorNote shows the 409 `the S3 credentials file needs root` (a non-root dev session; PUT step 4 comes before the probe).
5. `touch "$SCRATCH/loki-ui/bin/down"`. Within 10s the panel shows `Docker did not answer.` and no form. `rm "$SCRATCH/loki-ui/bin/down"`.
6. Negative control for the gate: `touch "$SCRATCH/loki-ui/bin/none"`. Within 10s the panel is gone (`enabled: false`). `rm "$SCRATCH/loki-ui/bin/none"` and it returns.
7. Roles. Create two users:
   ```bash
   curl -s -X POST http://127.0.0.1:7878/api/users -H 'authorization: Bearer dev' -H 'content-type: application/json' -d '{"username":"maint","password":"maintainer-pass-1","role":"maintainer"}'
   curl -s -X POST http://127.0.0.1:7878/api/users -H 'authorization: Bearer dev' -H 'content-type: application/json' -d '{"username":"view","password":"viewer-pass-12345","role":"viewer"}'
   ```
   Clear the token in Settings, then sign in as `maint`. The chunk form is live. The storage toggles are disabled with `Admin only.`, and there is no secret field and no `Switch to S3`. Sign in as `view`: no Logging panel.
8. In DevTools' device toolbar at 320px wide, `document.documentElement.scrollWidth <= document.documentElement.clientWidth` is `true`.
9. Confirm the `?` link's anchor exists once T12 has landed: `grep -nE '^#+ Loki settings$' /Volumes/S1/code/preview-stacks/docs/usage.md` prints exactly one line. If T12 named the heading differently, change the `#loki-settings` fragment to match.

Save a screenshot of the panel (1) as `$SCRATCH/loki-ui/panel.png` and of the ErrorNote (4) as `$SCRATCH/loki-ui/error-note.png`, and name both paths in the task report. Stop both servers.

- [ ] **Step 10: CHANGELOG**

In `packages/pstack/CHANGELOG.md`, under `## Unreleased` → `### Added`, append this as the last bullet. Create either heading if it is absent: `## Unreleased` goes between `# Changelog` and the first dated heading, and `### Added` directly under it.

```markdown
- **A Logging panel on the Control page.** Retention and chunks for maintainers; storage for
  admins, S3 one-way behind a confirm. It follows the `loki-apply` job, listed as `Loki settings`.
```

- [ ] **Step 11: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status -fv
```

Take the IDs of `apps/ui/test/useFormat.test.ts`, `apps/ui/src/composables/useFormat.ts`, `apps/ui/src/api/types.ts` and `apps/ui/src/views/ControlView.vue`, plus this task's CHANGELOG hunk only (not another task's uncommitted CHANGELOG hunk). Never take `packages/conformance/golden/host/db/*` or `.superpowers/`.

```bash
but commit -b claude/loki-logging-settings -m "feat(ui): the Logging panel on the Control page" <useFormat.test.ts id> <useFormat.ts id> <types.ts id> <ControlView.vue id> <CHANGELOG hunk id>
```

---

### Task 15: conformance: api-loki-settings.test.ts

**Files:**
- Create: `packages/conformance/test/api-loki-settings.test.ts`
- Modify: `packages/conformance/expected-pass.json` (insert one line before `    "test/api-openapi.test.ts": 4,`. Line numbers are advisory, because slice-1 T13 adds `"test/api-logging.test.ts": 3` just above it.)
- Test: `packages/conformance/test/api-loki-settings.test.ts`

**Interfaces:**
- Consumes:
  - **Routes (T12).** `GET /api/logging` returns the body in spec order `enabled, source, updatedAt, retentionDays, chunks, storage, limits`. `PUT /api/logging` and `PUT /api/logging/storage` answer 202 `{ job: { id, stack, action, state } }`, or 200 `{ changed: false }`, or `{ error }` with 400/409. Step 1 runs before the body is read.
  - **Error strings, used as exact assertions.** `retentionDays must be 1–365` (T2 Validate, en dash). `Loki is not running on this host — run pstack logging loki on the host` (T12). `S3 refused the probe: 403 InvalidAccessKeyId` (T5). `S3 is one-way on this host` (loki.ErrOneWay) and `S3 storage is fixed once saved — only accessKeyId and secretAccessKey change` (loki.ErrFixed), both from T2.
  - **The job (T6, T9-T11).** `action: "loki-apply"` and `stack: "pstack-control"`. Step messages are `nothing to change` and `rolled back`. The `verify` step carries Loki's last non-empty stderr line. The transcript line is `by pstack (boot)`. Boot starts one job when config.yaml differs from the render (T11, `reconcileLoki` in `api.New`).
  - **Environment (T8).** `PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS`, `PSTACK_LOKI_UID`. The poll interval is fixed at 2s (`lokiPoll`, no knob).
  - **Docker argv as the shim sees it (contractNotes).** `ps -aq --filter label=com.docker.compose.project=pstack-control`, `inspect l1`, `run --rm --network none --volumes-from l1:ro grafana/loki:3.7.7 -config.file=/etc/loki/config.yaml.next -verify-config`, `restart l1`, `exec l1 /usr/bin/loki -health`, `logs --since <RFC3339> l1`.
  - **Render bytes (T2).** `  retention_period: <h>h`, `  max_query_lookback: <h>h`, `    - from: "<cutover>"`, `      object_store: s3`, `    endpoint: <host:port>`, `    insecure: true`, `  delete_request_store: s3`. Credentials are exactly `[default]\naws_access_key_id = <id>\naws_secret_access_key = <secret>\n` at 0600.
  - **Default config bytes.** `packages/conformance/golden/render/control/http01-basic-compose-loki/loki/config.yaml`, created by slice-1 T13 (plan:6636) and left unchanged by T7. It is read through `GOLDEN` (`harness/goldens.ts:9`).
  - **Harness.** `bootServer`, `tmpd`, `until`, `type Booted` (`harness/server.ts:18-19, 61, 157-165`). `arm`, `casePattern`, `dockerShim`, `sq`, `type Shim` (`harness/docker-shim.ts:14-69`).
- Produces:
  - `packages/conformance/test/api-loki-settings.test.ts`: one `describe('Loki settings over HTTP')` with 8 tests. Each fails under `PSTACK_IMPL=null`.
  - `expected-pass.json`: `"test/api-loki-settings.test.ts": 8`.
  - T16's full gate relies on both.

This task comes after T12, so the Go binary already implements everything the file checks. The red step is the vacuity control: every test fails against `PSTACK_IMPL=null`. The green step is `PSTACK_IMPL=go`. Each test also has a black-box negative control, run in Step 5.

- [ ] **Step 1: Write the failing test.** Create `/Volumes/S1/code/preview-stacks/packages/conformance/test/api-loki-settings.test.ts`:

```ts
/**
 * Loki's settings over HTTP: the routes, the apply job, its rollback, and the boot reconcile.
 *
 * The docker shim plays the control stack's `loki` container (`l1`) and the four commands an apply
 * issues: verify, restart, the health probe, logs. PSTACK_LOKI_DIR is a temp copy of the rendered
 * loki/config.yaml golden. PSTACK_LOKI_UID is this process's uid, so a non-root runner reaches the
 * S3 path. A Bun server on 127.0.0.1 plays S3 for the probe and does not check signatures.
 */
import { describe, expect, test } from 'bun:test';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { bootServer, tmpd, until, type Booted } from '../harness/server.ts';
import { arm, casePattern, dockerShim, sq, type Shim } from '../harness/docker-shim.ts';
import { GOLDEN } from '../harness/goldens.ts';

/** Loki's config as `pstack init --logging loki` writes it: the default render. */
const DEFAULT = readFileSync(join(GOLDEN, 'render', 'control', 'http01-basic-compose-loki', 'loki', 'config.yaml'), 'utf8');

const PS = 'ps -aq --filter label=com.docker.compose.project=pstack-control';
const INSPECT = JSON.stringify([
  {
    Id: 'l1',
    Name: '/pstack-control-loki-1',
    Config: {
      Image: 'grafana/loki:3.7.7',
      Labels: {
        'com.docker.compose.service': 'loki',
        'pstack.logging.push-url': 'https://pstack:x@loki.example.com/loki/api/v1/push',
      },
    },
    State: { Status: 'running', StartedAt: '2026-09-15T08:00:00Z' },
  },
]);
const RUN = 'run --rm --network none --volumes-from l1:ro grafana/loki:3.7.7 -config.file=/etc/loki/config.yaml.next -verify-config';
const HEALTH = 'exec l1 /usr/bin/loki -health';

/** The whole case body. `rewrite` replaces every arm, so a test flips one by building the list again. */
const arms = (o: { ps?: string; run?: string; health?: 0 | 1 } = {}): string =>
  [
    arm(PS, o.ps ?? 'l1'),
    arm('inspect l1', INSPECT),
    o.run ?? arm(RUN, ''),
    arm('restart l1', 'l1'),
    arm(HEALTH, o.health ? 'Unhealthy' : 'ready', o.health ?? 0),
    arm('logs --since * l1', ''),
  ].join('\n');

const CHUNKS = { idlePeriodMinutes: 30, maxAgeMinutes: 120, targetSizeKiB: 1536, encoding: 'snappy' };

type Step = { axis: string; phase: string; ok: boolean; message?: string };
type Job = {
  id: string;
  stack: string;
  action: string;
  state: string;
  outcome?: { ok: boolean; steps: Step[] };
  log?: Array<{ level: string; message: string }>;
};
type View = {
  enabled: boolean | null;
  source: string;
  updatedAt: number | null;
  retentionDays: number;
  storage: { type: string; s3: Record<string, unknown> | null };
  limits: { earliestCutover: string };
};
type Loki = { s: Booted; shim: Shim; dir: string; stop: () => Promise<void> };

/** A server whose loki directory holds `config`, with a recording shim on PATH. */
async function boot(armText: string, config = DEFAULT): Promise<Loki> {
  const shim = dockerShim(armText, { record: true });
  const dir = tmpd('loki-dir');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'config.yaml'), config);
  const s = await bootServer({
    tag: 'loki-settings',
    pathPrefix: shim.dir,
    env: {
      PSTACK_LOKI_DIR: dir,
      PSTACK_LOKI_READY_TIMEOUT_MS: '4000',
      // Our own uid: the credentials file is then Loki's without a chown.
      PSTACK_LOKI_UID: String(process.getuid?.() ?? 0),
    },
  });
  return {
    s,
    shim,
    dir,
    stop: async () => {
      await s.stop();
      shim.remove();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

const put = (s: Booted, path: string, body: unknown) =>
  fetch(`${s.base}${path}`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });
const saveRetention = (s: Booted, days: number) => put(s, '/api/logging', { retentionDays: days, chunks: CHUNKS });
const getLogging = async (s: Booted): Promise<View> => (await (await fetch(`${s.base}/api/logging`, { headers: s.H })).json()) as View;
const jobOf = async (r: Response): Promise<string> => ((await r.json()) as { job: { id: string } }).job.id;
const errorOf = async (r: Response): Promise<string> => ((await r.json()) as { error: string }).error;
const restarts = (shim: Shim): number => shim.calls().filter((c) => c === 'restart l1').length;
const messages = (job: Job): Array<string | undefined> => (job.outcome?.steps ?? []).map((st) => st.message);
const configOf = (dir: string): string => readFileSync(join(dir, 'config.yaml'), 'utf8');

/** Until the job is terminal. The harness's waitJob stops at `queued` (server.ts:171), and test 6 queues one. */
async function waitTerminal(s: Booted, id: string, ms = 30_000): Promise<Job> {
  const live = (j?: Job) => !j || j.state === 'queued' || j.state === 'running';
  const job = await until(
    async () => ((await (await fetch(`${s.base}/api/jobs/${id}`, { headers: s.H })).json()) as { job?: Job }).job,
    (j) => !live(j),
    ms,
    50,
  );
  if (live(job)) throw new Error(`job ${id} never finished`);
  return job!;
}

const KEY_ID = 'AKIACONFORMANCE01';
const SECRET = 'conformance-s3-secret-0123456789';
const EMPTY_SHA256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
const DENIED =
  '<?xml version="1.0" encoding="UTF-8"?>\n<Error><Code>InvalidAccessKeyId</Code><Message>The AWS Access Key Id you provided does not exist in our records.</Message></Error>';

/** S3 for the probe: PUT answers `state.putStatus`, DELETE answers 204, and every request is recorded. */
function fakeS3() {
  const seen: Array<{ method: string; path: string; auth: string; sha: string }> = [];
  const state = { putStatus: 200 };
  const srv = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch: (req) => {
      seen.push({
        method: req.method,
        path: new URL(req.url).pathname,
        auth: req.headers.get('authorization') ?? '',
        sha: req.headers.get('x-amz-content-sha256') ?? '',
      });
      if (req.method === 'PUT' && state.putStatus !== 200) {
        return new Response(DENIED, { status: state.putStatus, headers: { 'content-type': 'application/xml' } });
      }
      return new Response(null, { status: req.method === 'DELETE' ? 204 : 200 });
    },
  });
  return { seen, state, port: srv.port as number, stop: () => srv.stop(true) };
}

describe('Loki settings over HTTP', () => {
  test('GET on an empty table: the defaults, the limits, enabled', async () => {
    // negative control: tag loggingStorageView.S3 `json:"s3,omitempty"` — `s3` leaves the body and both assertions fail.
    const { s, stop } = await boot(arms());
    try {
      const r = await fetch(`${s.base}/api/logging`, { headers: s.H });
      expect(r.status).toBe(200);
      const v = (await r.json()) as Record<string, unknown>;
      expect(Object.keys(v)).toEqual(['enabled', 'source', 'updatedAt', 'retentionDays', 'chunks', 'storage', 'limits']);
      expect(v).toEqual({
        enabled: true,
        source: 'default',
        updatedAt: null,
        retentionDays: 7,
        chunks: CHUNKS,
        storage: { type: 'filesystem', s3: null },
        limits: {
          retentionDays: { min: 1, max: 365 },
          idlePeriodMinutes: { min: 5, max: 60 },
          maxAgeMinutes: { min: 30, max: 180 },
          targetSizeKiB: { min: 512, max: 1536 },
          encodings: ['snappy', 'gzip', 'lz4', 'zstd'],
          earliestCutover: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
        },
      });
      // Always a later day: a period from today would already be live.
      expect((v.limits as { earliestCutover: string }).earliestCutover > new Date().toISOString().slice(0, 10)).toBe(true);
    } finally {
      await stop();
    }
  }, 20_000);

  test('a retention save renders both keys, restarts Loki once, in order, and reads back as db', async () => {
    // negative control: in loki.Render's anchor slice, make the max_query_lookback entry's replacement the anchor itself — the file keeps `max_query_lookback: 168h`.
    const { s, shim, dir, stop } = await boot(arms());
    try {
      const from = shim.calls().length;
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const { job } = (await r.json()) as { job: { id: string; stack: string; action: string } };
      expect(job).toMatchObject({ stack: 'pstack-control', action: 'loki-apply' });
      expect((await waitTerminal(s, job.id)).state).toBe('ok');
      expect(configOf(dir)).toContain('  retention_period: 336h');
      expect(configOf(dir)).toContain('  max_query_lookback: 336h');
      // ps and inspect come before the handler, the find and the restart; only the verbs are pinned exactly.
      const verbs = shim.calls().slice(from).filter((c) => !c.startsWith('ps ') && !c.startsWith('inspect '));
      expect(verbs.slice(0, 3)).toEqual([RUN, 'restart l1', HEALTH]);
      expect(verbs).toHaveLength(4);
      expect(verbs[3]).toMatch(/^logs --since \d{4}-\d{2}-\d{2}T\S+ l1$/);
      const v = await getLogging(s);
      expect(v).toMatchObject({ source: 'db', retentionDays: 14 });
      expect(typeof v.updatedAt).toBe('number');
    } finally {
      await stop();
    }
  }, 30_000);

  test('a config Loki refuses changes nothing: no restart, no .next file, the row untouched', async () => {
    // negative control: in the apply's verify step, write the render to config.yaml instead of config.yaml.next — the file no longer equals the default.
    const LAST = 'failed parsing config: chunk_encoding: unknown codec';
    const refusal = `  ${casePattern(RUN)}) printf '%s\\n' ${sq(`level=info msg="reading config"\n${LAST}`)} >&2; exit 1 ;;`;
    const { s, shim, dir, stop } = await boot(arms({ run: refusal }));
    try {
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const done = await waitTerminal(s, await jobOf(r));
      expect(done.state).toBe('failed');
      expect(done.outcome?.steps.find((st) => st.phase === 'verify')).toMatchObject({ ok: false, message: LAST });
      expect(configOf(dir)).toBe(DEFAULT);
      expect(readdirSync(dir)).toEqual(['config.yaml']);
      expect(restarts(shim)).toBe(0);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7, updatedAt: null });
    } finally {
      await stop();
    }
  }, 30_000);

  test('a Loki that never answers ready is rolled back to the previous file and row', async () => {
    // negative control: in the apply's ready step, return the failed step on a timeout without calling s.lokiRollback — one restart, config.yaml keeps 336h, no `rolled back`.
    const { s, shim, dir, stop } = await boot(arms({ health: 1 }));
    try {
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const id = await jobOf(r);
      // The first ready wait times out after 4s; the second restart is the rollback's. Let it come up.
      await until(async () => restarts(shim), (n) => n >= 2, 20_000);
      shim.rewrite(arms());
      const done = await waitTerminal(s, id);
      expect(done.state).toBe('failed');
      expect(messages(done)).toContain('rolled back');
      expect(configOf(dir)).toBe(DEFAULT);
      expect(restarts(shim)).toBe(2);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7, updatedAt: null });
    } finally {
      await stop();
    }
  }, 60_000);

  test('refusals: an unchanged save is 200, out of range is 400, no loki container is 409 before the body', async () => {
    // negative control: in loggingPut, read and parseChunks the body before the ControlRuntime check — the out-of-range save on the loki-less host answers 400.
    const { s, shim, stop } = await boot(arms());
    try {
      const same = await saveRetention(s, 7);
      expect(same.status).toBe(200);
      expect(await same.json()).toEqual({ changed: false });
      expect(shim.calls().filter((c) => c === RUN || c === 'restart l1')).toEqual([]);

      const bad = { retentionDays: 0, chunks: CHUNKS };
      const low = await put(s, '/api/logging', bad);
      expect(low.status).toBe(400);
      expect(await errorOf(low)).toBe('retentionDays must be 1–365');

      shim.rewrite(arms({ ps: '' }));
      expect((await getLogging(s)).enabled).toBe(false);
      const gone = await put(s, '/api/logging', bad);
      expect(gone.status).toBe(409);
      expect(await errorOf(gone)).toBe('Loki is not running on this host — run pstack logging loki on the host');
    } finally {
      await stop();
    }
  }, 30_000);

  test('a revert saved during an apply is queued as a job, never answered as a no-op', async () => {
    // negative control: in loggingPut's no-op step, delete `!s.jobs.IsBusy(inspect.ControlProject) && ` — the revert equals the still-empty row and answers 200.
    // A 2s verify holds job A after it took its patch and before it commits.
    const slow = `  ${casePattern(RUN)}) sleep 2; exit 0 ;;`;
    const { s, shim, dir, stop } = await boot(arms({ run: slow, health: 1 }));
    try {
      const first = await saveRetention(s, 14);
      expect(first.status).toBe(202);
      const a = await jobOf(first);
      await until(async () => shim.calls().includes(RUN), (seen) => seen, 10_000);
      const second = await saveRetention(s, 7);
      expect(second.status).toBe(202);
      const b = await jobOf(second);
      expect(b).not.toBe(a);
      // A commits 14, never goes ready, and rolls back: the second restart is the rollback's.
      await until(async () => restarts(shim), (n) => n >= 2, 30_000);
      shim.rewrite(arms());
      const doneA = await waitTerminal(s, a);
      expect(doneA.state).toBe('failed');
      expect(messages(doneA)).toContain('rolled back');
      // B runs on the reverted row: 7 is the default, and the file already is the default render.
      const doneB = await waitTerminal(s, b);
      expect(doneB.state).toBe('ok');
      expect(messages(doneB)).toContain('nothing to change');
      expect(restarts(shim)).toBe(2);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7 });
      expect(configOf(dir)).toBe(DEFAULT);
    } finally {
      await stop();
    }
  }, 60_000);

  test('S3: a refused probe is 400, a save writes 0600 credentials, the secret has no read path, S3 is one-way', async () => {
    // negative control: in loki.Probe, append the response body to the PUT refusal error — the 400 carries `does not exist in our records`.
    const s3 = fakeS3();
    const { s, shim, dir, stop } = await boot(arms());
    try {
      const { limits } = await getLogging(s);
      const cutover = limits.earliestCutover;
      const body = {
        type: 's3',
        endpoint: `http://127.0.0.1:${s3.port}`,
        region: 'us-east-1',
        bucket: 'pstack-logs',
        pathStyle: true,
        accessKeyId: KEY_ID,
        secretAccessKey: SECRET,
        cutover,
      };
      const creds = join(dir, 's3-credentials');

      s3.state.putStatus = 403;
      const refused = await put(s, '/api/logging/storage', body);
      expect(refused.status).toBe(400);
      const said = await refused.text();
      expect((JSON.parse(said) as { error: string }).error).toBe('S3 refused the probe: 403 InvalidAccessKeyId');
      expect(said).not.toContain('does not exist in our records');
      expect(s3.seen.map((r) => r.method)).toEqual(['PUT']);
      expect(existsSync(creds)).toBe(false);

      s3.state.putStatus = 200;
      const saved = await put(s, '/api/logging/storage', body);
      expect(saved.status).toBe(202);
      expect((await waitTerminal(s, await jobOf(saved))).state).toBe('ok');
      // The probe: PUT then DELETE of one key, SigV4-signed over an empty body.
      expect(s3.seen).toHaveLength(3);
      const [probe, cleanup] = s3.seen.slice(1);
      expect(probe).toMatchObject({ method: 'PUT', sha: EMPTY_SHA256 });
      expect(probe!.path).toMatch(/^\/pstack-logs\/pstack-probe-[0-9a-f]{16}$/);
      expect(probe!.auth).toStartWith(`AWS4-HMAC-SHA256 Credential=${KEY_ID}/`);
      expect(cleanup).toMatchObject({ method: 'DELETE', path: probe!.path });

      const cfg = configOf(dir);
      expect(cfg).toContain(`    - from: "${cutover}"`);
      expect(cfg).toContain('      object_store: s3\n');
      expect(cfg).toContain(`    endpoint: 127.0.0.1:${s3.port}\n`);
      expect(cfg).toContain('    insecure: true\n');
      expect(cfg).toContain('  delete_request_store: s3');
      expect(cfg).not.toContain(SECRET);
      expect((statSync(creds).mode & 0o777).toString(8)).toBe('600');
      expect(readFileSync(creds, 'utf8')).toBe(`[default]\naws_access_key_id = ${KEY_ID}\naws_secret_access_key = ${SECRET}\n`);

      // No read path: not the secret, not a mask of any length.
      const read = await (await fetch(`${s.base}/api/logging`, { headers: s.H })).text();
      expect(read).not.toContain(SECRET);
      expect(read).not.toContain('•');
      expect((JSON.parse(read) as View).storage).toEqual({
        type: 's3',
        s3: { endpoint: body.endpoint, region: 'us-east-1', bucket: 'pstack-logs', pathStyle: true, accessKeyId: KEY_ID, secretSet: true, cutover },
      });

      // The mask keeps the stored secret. Every other field is fixed, so it resolves to the row: 200, no probe.
      const masked = await put(s, '/api/logging/storage', { ...body, secretAccessKey: '••••••••' });
      expect(masked.status).toBe(200);
      expect(await masked.json()).toEqual({ changed: false });
      expect(readFileSync(creds, 'utf8')).toContain(`aws_secret_access_key = ${SECRET}\n`);
      expect(s3.seen).toHaveLength(3);

      const back = await put(s, '/api/logging/storage', { type: 'filesystem' });
      expect(back.status).toBe(409);
      expect(await errorOf(back)).toBe('S3 is one-way on this host');
      const moved = await put(s, '/api/logging/storage', { ...body, bucket: 'pstack-logs-2' });
      expect(moved.status).toBe(409);
      expect(await errorOf(moved)).toBe('S3 storage is fixed once saved — only accessKeyId and secretAccessKey change');
      expect(restarts(shim)).toBe(1);
    } finally {
      await stop();
      s3.stop();
    }
  }, 60_000);

  test('boot renders a drifted config.yaml back through one loki-apply job', async () => {
    // negative control: delete the s.reconcileLoki() call from api.New — no loki-apply job is listed and the file keeps 72h.
    const drifted = DEFAULT.replace('  retention_period: 168h', '  retention_period: 72h');
    expect(drifted).not.toBe(DEFAULT);
    const { s, shim, dir, stop } = await boot(arms(), drifted);
    try {
      const { jobs } = (await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: Job[] };
      const applies = jobs.filter((j) => j.action === 'loki-apply');
      expect(applies.map((j) => j.stack)).toEqual(['pstack-control']);
      const done = await waitTerminal(s, applies[0]!.id);
      expect(done.state).toBe('ok');
      expect((done.log ?? []).map((e) => e.message)).toContain('by pstack (boot)');
      expect(configOf(dir)).toBe(DEFAULT);
      expect(restarts(shim)).toBe(1);
    } finally {
      await stop();
    }
  }, 30_000);
});
```

- [ ] **Step 2: Run it and watch it fail.** This is the vacuity control. The null server answers `200 {}` to everything.

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=null bun test test/api-loki-settings.test.ts
```

Expected: `0 pass`, `8 fail`, and each test fails within seconds.
- Test 1 fails at `Object.keys(v)` (received `[]`).
- Tests 2, 3, 4 and 6 fail at `expect(r.status).toBe(202)` (received 200).
- Test 5 fails at `toEqual({ changed: false })` (received `{}`).
- Test 7 fails with `TypeError` reading `earliestCutover` of undefined.
- Test 8 fails with `TypeError` on `jobs.filter`.

If the run instead throws `ENOENT … http01-basic-compose-loki/loki/config.yaml`, slice-1 T13 has not landed. Stop, because this task depends on it.

- [ ] **Step 3: Implement.** The behaviour belongs to T2-T12, so this task adds no Go code. Build the binary those tasks produced. Then record the new file in the ratchet by hand. Never use `ratchet --write`, which rewrites every count from one local run.

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
```

In `/Volumes/S1/code/preview-stacks/packages/conformance/expected-pass.json`, replace:

```json
    "test/api-openapi.test.ts": 4,
```

with:

```json
    "test/api-loki-settings.test.ts": 8,
    "test/api-openapi.test.ts": 4,
```

Key order: `test/api-logging.test.ts` (slice-1 T13, `g` < `k`) sorts before `test/api-loki-settings.test.ts`, which sorts before `test/api-openapi.test.ts`. No other line changes.

- [ ] **Step 4: Run it and watch it pass.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-loki-settings.test.ts
```

Expected: `8 pass`, `0 fail`. Tests 4 and 6 take about 8-12s each: a 4s ready wait, then the rollback, polled every 2s.

If a test fails, the bug is in the owning task's Go code (T2/T5/T9-T12), not in this file. Fix it there and amend the fix into that task's commit with GitButler's amend recipe. Rebuild and rerun.

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-loki-settings.test.ts test/api-rbac.test.ts test/host-fixture.test.ts
```

Expected: `49 pass` (8 + 7 + 34), `0 fail`. The `api-rbac` count stays 7, because T12's three rows iterate inside existing tests. `host-fixture` still opens the v6 database.

- [ ] **Step 5: Run every negative control.** For each mutation: make the edit, rebuild, run the one test, see it fail at the named assertion, then undo the edit. `but diff` must show no hunk in that file afterwards.

The command, with `<file-and-name>` taken from the table:

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack && cd packages/conformance && PSTACK_IMPL=go bun test test/api-loki-settings.test.ts -t '<name>'
```

| # | `-t` name | Mutation | Expected failure |
|---|---|---|---|
| 1 | `GET on an empty table` | `packages/pstack/internal/api/routes_logging.go`, `loggingStorageView`: `S3 *loggingS3View \`json:"s3"\`` → `\`json:"s3,omitempty"\`` | `Object.keys(v)` toEqual: `storage` lacks `s3` in the deep toEqual |
| 2 | `a retention save renders both keys` | `packages/pstack/internal/loki/render.go`, Render's ordered anchor slice: the `max_query_lookback` entry's `with` becomes the literal `"  max_query_lookback: 168h"` | `toContain('  max_query_lookback: 336h')` |
| 3 | `a config Loki refuses changes nothing` | `packages/pstack/internal/api/loki_apply.go`, step 3: `filepath.Join(s.opts.LokiDir, loki.ConfigFile+loki.NextSuffix)` → `filepath.Join(s.opts.LokiDir, loki.ConfigFile)` | `expect(configOf(dir)).toBe(DEFAULT)` |
| 4 | `a Loki that never answers ready is rolled back` | `loki_apply.go`, step 7's timeout branch: return the failed `ready` step without calling `s.lokiRollback(...)` | `expect(messages(done)).toContain('rolled back')` |
| 5 | `refusals: an unchanged save is 200` | `routes_logging.go`, `loggingPut`: move the body read and `parseChunks` call above the `inspect.ControlRuntime(s.host)` check | `expect(gone.status).toBe(409)`, received 400 |
| 6 | `a revert saved during an apply` | `routes_logging.go`, `loggingPut` step 5: delete `!s.jobs.IsBusy(inspect.ControlProject) && ` from the no-op condition | `expect(second.status).toBe(202)`, received 200 |
| 7 | `S3: a refused probe is 400` | `packages/pstack/internal/loki/probe.go`: append `+ ": " + string(body)` (the read response body) to the PUT refusal `*Error` message | `toBe('S3 refused the probe: 403 InvalidAccessKeyId')` |
| 8 | `boot renders a drifted config.yaml` | `packages/pstack/internal/api/server.go`, `New`: delete the `s.reconcileLoki()` line | `applies.map(...)` toEqual `['pstack-control']`, received `[]` |

After the last undo, rebuild and rerun Step 4's first command. Expected: `8 pass`.

- [ ] **Step 6: Typecheck, vacuity and ratchet over the whole suite.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck && bun run vacuity && bun run ratchet
```

Expected:
- `tsc --noEmit` exits 0.
- vacuity prints `null mode: … failed (good), … skipped (CLI-only), 0 vacuous`.
- ratchet prints `test/api-loki-settings.test.ts   8/8   (recorded 8) complete`, and no line reads `REGRESSED`.

- [ ] **Step 7: Commit.**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Pick the IDs of exactly `packages/conformance/test/api-loki-settings.test.ts` and `packages/conformance/expected-pass.json`. Never pick `packages/conformance/golden/host/db/pstack.db-shm`, `pstack.db-wal` or anything under `.superpowers/`. Then:

```bash
but commit -b claude/loki-logging-settings -m "test(conformance): Loki settings routes, apply, rollback and boot, black-box" <test-file-id> <expected-pass-id>
```

---

### Task 16: docs status, CHANGELOG, full gate, real-host checklist

**Files:**
- Modify: `packages/pstack/CHANGELOG.md` (the `## Unreleased` section: delete the per-task slice-2 bullets T2–T14 wrote, then add the merged block at the end of `### Added` and `### Changed`)
- Modify: `docs/loki-logging-design.md` (the banner's bold lead `**Slice 1 (`--logging loki`) is built (Unreleased); slices 2 and 3 are not.**`, its closing line `> Slices 2 and 3 record the decisions already taken; each gets its own spec before it is built.`, and the heading `## Slice 2 — Loki settings in the UI (decisions recorded; own spec before building)`, today design.md:356). Edits are anchored by quoted text. Slice-1 T14 moves the line numbers (R9, R11).
- Modify: `docs/README.md` (the `loki-logging-design.md` row, from its `### [` heading up to the next `### ` line; today README.md:91-100)
- Test: none. This task only edits docs. Its checks are the fact greps in Step 1, then `bun run check`, the `apps/ui` typecheck and test, `bun run vacuity` and `bun run ratchet`.

**Interfaces:**
- Consumes (T16 checks each one in Step 1 and does not edit any of them):
  - T1: migration 9 `loki_config`.
  - T2: `loki.Writable`, and the CHANGELOG bullet `**Loki settings.**`.
  - T4: `loki.Merge`'s check order (ErrOneWay, ErrFixed, the KeepSecret `*Error`, then Validate).
  - T6: `jobs.LokiApply` (`"loki-apply"`) and `logging.changed`.
  - T7: the `      - ./loki:/etc/loki` anchor, `AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials`, `  [dry-run] keep <path>`, and exactly four regenerated goldens.
  - T8: `PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS` and `PSTACK_LOKI_UID`.
  - T9–T11: `startLokiApply`, `reconcileLoki` and the `"by " + by` first transcript line, all in `internal/api/loki_apply.go`.
  - T12: three dispatch literals, two permission rows, and OpenAPI tag `logging` with `x-cli-name` `get`/`set`/`storage-set`.
  - T13: the client's `logging` block.
  - T14: the Logging panel and `ACTION_LABELS['loki-apply']`.
  - T15: `test/api-loki-settings.test.ts` and its `expected-pass.json` row.
  - Slice-1 T14: `## Unreleased`, the banner lead and closing line, and the README row that ends by linking `loki-logging-slice-1-plan.md`.
- Produces: the slice-2 CHANGELOG block, and the "slice 2 built" status in both docs. No code identifiers.

- [ ] **Step 1: Check the facts the docs state (docs only, so there is no test file)**

  The docs must describe what T1–T15 built, not the contract. Run these read-only checks first. If any output differs from what is expected, change the prose in Steps 2–4 to match the code and note the difference in the task report. Do not edit code in this task.

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Expected: one stack in which `[claude/loki-logging-settings]` is listed above `[claude/loki-logging]`, with T1–T15's commits under `claude/loki-logging-settings`. If the branch sits in its own stack, restack it with `but move` (gitbutler skill) before going on. Never recreate it.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack
  grep -c '"/api/logging' internal/api/permissions.go
  grep -c 'path == "/api/logging' internal/api/routes.go
  grep -c "'503'" api/openapi.yaml
  grep -c 'loki-apply' ui/index.html
  grep -c '{{ j.action }}' ui/index.html
  grep -n '"by " + by' internal/api/loki_apply.go
  grep -c 'json:"by"' internal/jobs/jobs.go
  grep -n 'func Writable' internal/loki/loki.go
  grep -rln 'internal/routing"' internal/loki
  grep -ln -e 'func (s \*Server) startLokiApply' -e 'func (s \*Server) reconcileLoki' internal/api/*.go
  sed -n 1,9p internal/api/server.go | grep -c '/api/'
  grep -n -e 'ErrOneWay' -e 'ErrFixed' -e 'Validate(' internal/loki/loki.go | sed -n '/func Merge/,$p'
  awk '/^func Merge/,/^}/' internal/loki/loki.go | grep -n -e 'ErrOneWay' -e 'ErrFixed' -e 'Validate('
  ```

  Expected, in order:
  - `2`: two permission rows cover three routes.
  - `3`.
  - `0`: no 503 in OpenAPI.
  - `0`, then `2`: the basic UI prints the action raw, at `ui/index.html:409` and `:1231`.
  - One line.
  - `0`: a job has no `by`.
  - One line.
  - Nothing, exit 1: loki does not import routing.
  - Exactly `internal/api/loki_apply.go`.
  - `0`: the server.go header has no route list.
  - The first Merge grep may print nothing; ignore it.
  - The `awk` grep prints `ErrOneWay`, then `ErrFixed`, then `Validate(`, in rising line order: conflicts are checked before ranges.

  Each of these is a bullet in the Step 3b banner. Drop or reword any bullet whose check came out differently.

  ```bash
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack && env -u PSTACK_API_URL -u PSTACK_TOKEN ./packages/pstack/bin/pstack api logging --help
  ```

  Expected: exit 0 and a list containing `get`, `set` and `storage-set`. If the group or command names differ, use the real names in Step 2.

  ```bash
  cd /Volumes/S1/code/preview-stacks && but show claude/loki-logging-settings --verbose | grep 'golden/'
  ```

  Expected: these four paths, each possibly with a status prefix, and no other `golden/` path:
  - `packages/conformance/golden/render/control/http01-basic-compose-loki/docker-compose.yml`
  - `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml`
  - `packages/conformance/golden/cli/init-dry-http01-basic-compose-loki.json`
  - `packages/conformance/golden/cli/init-dry-dns01-advanced-swarm-loki.json`

  The following do not appear, because none of them prints init's compose write line:
  - `logging-loki-dry-dns01-advanced-swarm` and `logging-off-dry-*-loki`. `SwitchLogging` shares `SwitchUI`'s step runner, and `ui-switch-dry-dns01-advanced-swarm.json` has no `[dry-run] write` line.
  - `init-*-loki`. A real `write` prints nothing (`init.go:967-976`).
  - `upgrade-plan-*-loki`.

  Both `loki/config.yaml` goldens and the help goldens are absent too. Any extra golden goes back to its task (Step 5's table). A missing one means T7 did not regenerate it.

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -n '^## \|^### ' packages/pstack/CHANGELOG.md | head -8
  grep -n -F -e 'Loki settings' -e '/api/logging' -e 'loki-apply' -e 'logging.changed' -e 'PSTACK_LOKI_DIR' -e 'PSTACK_LOKI_READY_TIMEOUT_MS' -e 'PSTACK_LOKI_UID' -e 'loki_config' -e 's3-credentials' -e 'AWS_SHARED_CREDENTIALS_FILE' -e 'dry-run] keep' -e '/etc/loki' -e 'host-specific' -e 'logging.get' -e 'Logging panel' packages/pstack/CHANGELOG.md
  ```

  Expected:
  - The first command prints `## Unreleased` first, then its `### Added` and `### Changed` (and slice 1's `### Fixed`), then `## 0.39.1 — 2026-09-14` or a later release.
  - The second prints only lines above that first release heading. They are the per-task bullets T2–T14 wrote, including T2's `**Loki settings.**` bullet that T6/T8/T12/T14 extended in place. Step 2 deletes the bullet each line belongs to, so the merged block does not repeat it. Today the file has no `Loki settings` line (checked), so a match below the first release heading is not slice 2's: leave it and name it in the report.

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -n -F 'is built (' docs/loki-logging-design.md docs/README.md
  grep -n -F 'Slices 2 and 3 record the decisions already taken; each gets its own spec before it is built.' docs/loki-logging-design.md
  grep -n '^## Slice 2' docs/loki-logging-design.md
  grep -rn -F '#slice-2' docs apps/ui/src packages/pstack/ui AGENTS.md
  sed -n '/^### \[`loki-logging-design.md`\]/,/^### /p' docs/README.md
  ls docs/loki-logging-slice-2-plan.md
  ```

  Expected:
  - **`is built (`:** one line in each file containing `**Slice 1 (`--logging loki`) is built (Unreleased); slices 2 and 3 are not.**`. If `(Unreleased)` reads a version, slice 1 was released in the meantime, so use the "released" forms in Steps 3 and 4.
  - **The closing line:** exactly one match, in the banner.
  - **The slice 2 heading:** `## Slice 2 — Loki settings in the UI (decisions recorded; own spec before building)`. If it reads otherwise (slice 2's spec landed in this doc), skip Step 3c.
  - **`#slice-2`:** nothing. Otherwise Step 3c also updates those links to the new anchor.
  - **The README row:** ends with the next `### [` heading, and holds the bold lead plus a sentence linking `loki-logging-slice-1-plan.md`.
  - **`ls`:** the plan exists. If it was committed under another name, use that name in Steps 3 and 4.

  If either anchor quoted above is missing, print the banner (`sed -n 1,30p docs/loki-logging-design.md`). Apply the Step 3 edit to the line that plays that role, and name the difference in the report.

- [ ] **Step 2: Write the slice-2 CHANGELOG block**

  In `packages/pstack/CHANGELOG.md`, under `## Unreleased`, delete every bullet that holds a line Step 1's second grep printed. A bullet runs from its `- ` line to the line before the next `- `, `### ` or `## ` line. Delete any `###` subsection that is left empty. If a deleted bullet stated a fact missing from the text below, keep that sentence and name it in the report.

  ```bash
  cd /Volumes/S1/code/preview-stacks && awk '/^## /{n++} n==1' packages/pstack/CHANGELOG.md | grep -c -F 'Loki settings'
  ```

  Expected: `0` before the inserts below.

  At the end of `### Added` (after slice 1's last Added bullet), insert:

  ```markdown
  - **Loki settings, on the Control page and the API.** `GET /api/logging` and `PUT /api/logging`
    (maintainer) read and set retention (1–365 days) and chunking: idle period 5–60 min, max age
    30–180 min, target size 512–1536 KiB, encoding `snappy`, `gzip`, `lz4` or `zstd`.
    `PUT /api/logging/storage` (admin) moves storage from the filesystem to an S3-compatible bucket.
    The move is one-way: the bucket gets a schema period from a future UTC date, and after the save
    only the access key and secret change. A save probes the bucket with a signed PUT and DELETE and
    answers 400 when that fails. The secret is write-only (reads return `secretSet`) and lives in
    `control/loki/s3-credentials` (0600, uid 10001), never in `config.yaml`. Also
    `pstack api logging get|set|storage-set` and the client's `logging.get|set|setStorage`. The
    settings are stored in a new `loki_config` table (migration 9); the portable config skips them.
  - **The `loki-apply` job.** A save renders `config.yaml.next`, checks it with `loki -verify-config`
    in a throwaway container, swaps it in, restarts only Loki and waits for its health check. A
    failed restart or wait restores the previous files and settings and restarts Loki again, unless
    that would drop an S3 period that starts too soon: then the change is left in place and the job
    fails saying so. A save made while another applies is carried by the next job, never dropped.
    `verified` is `null`. At boot, pstack finishes or undoes an apply it died in, and applies again
    when `config.yaml` differs from the settings.
  - **`logging.changed`**, once an apply is ready: `by`, `job`, `changed` (`chunks`, `retention`,
    `storage`, `credentials`), `storage`, `cutover`, `retentionDays`. Never the endpoint, bucket, key
    id or secret.
  - **`PSTACK_LOKI_DIR`, `PSTACK_LOKI_READY_TIMEOUT_MS` (300000) and `PSTACK_LOKI_UID` (10001)** on
    `serve`. Without `PSTACK_LOKI_DIR` the directory is `/etc/loki` when it exists, else
    `<PSTACK_DATA>/control/loki`.
  ```

  At the end of `### Changed` (after slice 1's Goldens bullet, before `### Fixed`), insert:

  ```markdown
  - **`init` keeps `control/loki/config.yaml`.** It writes the file only when it is absent, and a dry
    run prints `[dry-run] keep <path>`. `init`, `pstack upgrade` and `pstack logging loki` no longer
    overwrite settings saved through the API. The file is the API's: a hand edit is reverted by the
    next apply or pstack restart, except that a live schema period is never dropped.
  - **pstack mounts `control/loki` read-write at `/etc/loki` in every mode, and Loki gets
    `AWS_SHARED_CREDENTIALS_FILE=/etc/loki/s3-credentials`.** The next `pstack upgrade` recreates
    pstack once on every host, and Loki where it runs. `pstack logging loki|off` never recreates
    pstack.
  - **Goldens (Loki settings).** Regenerated with `bun gen/goldens.ts` for the mount and the env line:
    `golden/render/control/http01-basic-compose-loki/docker-compose.yml`,
    `golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml`, and the compose byte count
    in `golden/cli/init-dry-http01-basic-compose-loki.json` and
    `golden/cli/init-dry-dns01-advanced-swarm-loki.json`. Every other golden, both `loki/config.yaml`
    cells included, is unchanged.
  ```

  If `## Unreleased` has no `### Added` or `### Changed` (slice 1 was released), create `### Added` and then `### Changed` directly under `## Unreleased`, with a blank line around each heading, and put the blocks there.

  If Step 1's help or goldens check came out differently, correct the matching words now: the `pstack api` names, or the four golden paths.

  ```bash
  cd /Volumes/S1/code/preview-stacks && awk '/^## /{n++} n==1' packages/pstack/CHANGELOG.md | grep -c -F -e '**Loki settings' -e 'Goldens (Loki settings)'
  ```

  Expected: `2`. One Added bullet and one Goldens bullet; a `3` means a T2 bullet survived.

- [ ] **Step 3: Mark slice 2 built in the design doc**

  **3a.** In `docs/loki-logging-design.md`, replace:

  ```markdown
  **Slice 1 (`--logging loki`) is built (Unreleased); slices 2 and 3 are not.**
  ```

  with:

  ```markdown
  **Slices 1 (`--logging loki`) and 2 (Loki settings) are built (Unreleased); slice 3 is not.**
  ```

  Released form: if Step 1 showed a version `X.Y.Z` in place of `Unreleased`, the replacement is `**Slice 1 (`--logging loki`) is built (X.Y.Z), slice 2 (Loki settings) is built (Unreleased), slice 3 is not.**`.

  Then re-wrap that blockquote paragraph at 100 columns. Every line keeps its `> ` prefix, and no word changes.

  **3b.** Replace the closing line:

  ```markdown
  > Slices 2 and 3 record the decisions already taken; each gets its own spec before it is built.
  ```

  with:

  ```markdown
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
  >
  > Slice 3 records the decisions already taken and gets its own spec before it is built.
  ```

  **3c.** Replace the heading:

  ```markdown
  ## Slice 2 — Loki settings in the UI (decisions recorded; own spec before building)
  ```

  with:

  ```markdown
  ## Slice 2 — Loki settings in the UI (decisions recorded; built)
  ```

  Skip 3c if Step 1 showed a different heading. If Step 1's `#slice-2` grep printed links, change each `#slice-2--loki-settings-in-the-ui-decisions-recorded-own-spec-before-building` to `#slice-2--loki-settings-in-the-ui-decisions-recorded-built`.

- [ ] **Step 4: Match the docs index row**

  In `docs/README.md`, replace the whole loki row that Step 1's `sed` printed: from `### [`loki-logging-design.md`](loki-logging-design.md) — …` through the blank line before the next `### ` heading. The replacement is:

  ```markdown
  ### [`loki-logging-design.md`](loki-logging-design.md) — a design in three slices, slices 1 and 2 built

  **Slices 1 (`--logging loki`) and 2 (Loki settings) are built (Unreleased); slice 3 is not.** Loki
  as a logging option: a Loki container in the control stack, the Grafana Loki Docker plugin on every
  node, and a `logging:` block pstack adds to every deployed service that has none — pushed through
  Traefik with basic auth. Slice 2 sets Loki's chunking, retention and storage (filesystem or S3) from
  the Control page. Slice 1's spec is kept as approved; where each build differs is at the top.
  Slice 3 (Grafana with pstack sign-in) records its decisions and gets its own spec. Read before
  touching logging, the control template, or the join material. Build plans, task by task:
  [`loki-logging-slice-1-plan.md`](loki-logging-slice-1-plan.md) and
  [`loki-logging-slice-2-plan.md`](loki-logging-slice-2-plan.md).

  ```

  The bold first sentence is the design banner's lead, word for word. In the released case, use Step 3a's released sentence here too.

  If the old row held a sentence whose fact is not in this text, keep that sentence and name it in the report.

  ```bash
  cd /Volumes/S1/code/preview-stacks && grep -c -F '**Slices 1 (`--logging loki`) and 2 (Loki settings) are built (Unreleased); slice 3 is not.**' docs/README.md docs/loki-logging-design.md
  ```

  Expected: `docs/README.md:1` and `docs/loki-logging-design.md:1`. In the released case, grep for the released sentence instead.

  If the re-wrap in 3a split the sentence across lines, the design count is 0. Then check with `tr '\n' ' ' < docs/loki-logging-design.md | grep -c -F 'Loki settings) are built'`, which prints `1`.

- [ ] **Step 5: Run the full gate**

  ```bash
  cd /Volumes/S1/code/preview-stacks && bun run check
  ```

  This takes several minutes: use Bash timeout 600000, or run_in_background. `bun run check` is `turbo run check` (package.json:20). It runs each workspace's `check` script, after `build`, `test` and `typecheck` (turbo.json `check.dependsOn`):
  - `packages/pstack`: `go vet ./...`, `go test -race -timeout 120s ./...`, `go build -o bin/pstack ./cmd/pstack`.
  - `packages/client`: `bun test`, `tsc --noEmit`, `bun run build`.
  - `packages/conformance`: `bun test`, `tsc --noEmit`.

  Expected: turbo ends with `Tasks:    <n> successful, <n> total`, with the two numbers equal, and the command exits 0.

  `apps/ui` has no `check` script (apps/ui/package.json:13-20; `typecheck` and `test` are :16-17), so do not count on turbo to run it. Run it explicitly:

  ```bash
  cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck && bun run test
  ```

  Expected: `vue-tsc --build` prints nothing and exits 0, then `bun test` ends with `0 fail` (T14's `test/useFormat.test.ts` among the passes) and exits 0.

  A failure belongs to the task that owns the failing package or file. Fix it there, never in this docs commit:

  | Failing | Owning task |
  |---|---|
  | `internal/store` | T1 |
  | `internal/loki` — settings, Validate, render, credentials | T2 |
  | `internal/loki` — periods | T3 |
  | `internal/loki` — Row/Merge/Changed; `internal/config` | T4 |
  | `internal/loki` — probe | T5 |
  | `internal/jobs`, `internal/events`, `internal/notify`, `docs/webhook-events.md` | T6 |
  | `internal/initctl`, the four goldens, `templates/control/README.md` | T7 |
  | `internal/api` options/tuning, `internal/cli/serve.go` | T8 |
  | `internal/api/loki_apply_test.go` — forward path / rollback + cancel / resume + boot | T9 / T10 / T11 |
  | `routes_logging*`, `permissions*`, `openapi.yaml`, `internal/apicli`, `cli/api.go`, `cli/api_test.go`, `api-rbac.test.ts` | T12 |
  | `packages/client` | T13 |
  | `apps/ui` (typecheck or `bun test`) | T14 |
  | `test/api-loki-settings.test.ts`, `expected-pass.json` | T15 |

  Recipe: make the fix, then run `but status -fv` to get the fixed files' IDs and the owning commit's ID on `claude/loki-logging-settings`. Then run `but amend -t <owning-commit-id> <file-id> [<file-id>…]` and rerun both gate commands.

- [ ] **Step 6: Vacuity — no new test passes against the null server**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run vacuity
  ```

  Expected: the last line is `null mode: <n> failed (good), <n> skipped (CLI-only), 0 vacuous`, and the command exits 0. A test it lists from `test/api-loki-settings.test.ts` goes back to T15; a row in `test/api-rbac.test.ts` goes back to T12. Amend as in Step 5.

- [ ] **Step 7: Ratchet — counts only go up**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run ratchet
  ```

  Expected:
  - Exit 0.
  - A row `test/api-loki-settings.test.ts … <n>/<n> (recorded <n>) complete`.
  - No `REGRESSED` row, no `advanced` row, and no `run with --write to ratchet up` line.

  If a row says `advanced`, T15 under-recorded. Run `bun scripts/ratchet.ts --write`. Then `but diff` must show only `packages/conformance/expected-pass.json`, with the `advanced` file's count raised. Amend it into T15's commit (`but status -fv`, then `but amend -t <T15-commit-id> <expected-pass.json-file-id>`).

- [ ] **Step 8: Commit**

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Take the file IDs of exactly three files: `packages/pstack/CHANGELOG.md`, `docs/README.md` and `docs/loki-logging-design.md`. Leave out:
  - the untracked `packages/conformance/golden/host/db/pstack.db-shm` and `pstack.db-wal`
  - anything under `.superpowers/` or `packages/conformance/.status/`

  ```bash
  but commit -b claude/loki-logging-settings -m "docs(changelog): Loki logging, slice 2" <changelog-id> <readme-id> <design-id>
  ```

  No attribution footer. Do not push or tag.

- [ ] **Step 9: Hand the owner the real-host checks (in the task report, not a file)**

  Paste this into the final report as-is, under the heading **Real host, before the release — NOT RUN (pre-release)**:

  1. Save retention 14 days: Loki restarts once, the file says `336h`, and pushes resume.
  2. Save S3 with a wrong secret → 400. With the right one → `ok`. The day of the cutover, after the idle period, objects appear in the bucket, and a query spanning the cutover returns lines from both sides.
  3. Rotate the secret, and prove `AWS_SHARED_CREDENTIALS_FILE` is re-read on restart. The facts rest on AWS SDK docs, not Loki source.
  4. `pstack upgrade` after the S3 save: `config.yaml` unchanged, both periods present, Loki ready.
  5. `pstack logging off`, then `loki`: settings back, no second restart.
  6. `docker kill` pstack during the ready wait: after it restarts, the resume finishes without a second Loki restart and `previous_config` is NULL.
  7. `ls -ln control/loki`: `s3-credentials` is `-rw------- 10001`.

  Add a line for the owner:
  - "Unreleased" appears in `packages/pstack/CHANGELOG.md`, `docs/loki-logging-design.md` and `docs/README.md`. At release, replace it with the version (`grep -rn Unreleased docs packages/pstack/CHANGELOG.md`).

---
