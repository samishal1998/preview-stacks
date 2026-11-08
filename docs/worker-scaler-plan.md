# Worker scaler — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** an optional `scaler` control-stack service that adds a swarm worker through OpenTofu when previews do not fit, and removes one that has sat empty, using a preset and credentials stored by pstack.

**Architecture:** pstack stores the settings, credentials and custom presets (migration 12) and serves them to the root token. The scaler is the same binary (`pstack scaler`) in its own container: every 30 s it reads `/api/scaler/config`, `/api/signals` and `/api/swarm`, runs at most one Tofu action in `/state/tofu/<preset>/`, and posts a report. Ownership lives in `/state/scaler.json`; pstack keeps the last report so its 409s survive a restart.

**Tech Stack:** Go (stdlib `archive/zip`, `crypto/sha256`, `os/exec`, `syscall.Flock`), OpenTofu 1.13.9, HCL presets, SQLite migrations, Vue (apps/ui, apps/ui-hearth), bun conformance.

**Spec:** `docs/worker-scaler-design.md`

## Global Constraints

- VCS writes through `but` only, with explicit ids (`but commit -b <branch-id> -m … <ids>`). No attribution footers. Never commit `packages/conformance/golden/host/db/pstack.db*`, `.dns_token`, `hetzner*`, `id_hou_dev`, `.superpowers/`, any Tofu state or `.terraform/` directory.
- `export GOROOT=/usr/lib/go-1.23` before Go commands. `bun run check` green at the end of every task.
- Every test starts with a `// negative control:` comment naming the change that would make it fail.
- OpenTofu **1.13.9**. `tofu_1.13.9_linux_amd64.zip` sha256 `bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce`; `tofu_1.13.9_linux_arm64.zip` sha256 `04f7c93828b859d420269ed30f923ffebf09157b5d7fb95bf8d88a4d208b7fa3` (from `https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS`). Tasks 2–3 need a real `tofu` for lock files; this machine has none, so download that zip, check the sum, and unpack it under `~/.claude/projects/-home-samimishal-projects-go-preview-stacks/scaler-artifacts/bin/` (never into the repo).
- Loop numbers: tick 30 s; one Tofu command at a time; defaults `min` 0, `max` 3, `cooldown` 5 min, `grace` 15 min; never-joined after 10 min; lost after 10 min not `ready`; scale-ups stop after 2 never-joined in a row until the settings change; backoff 1, 2, 4 … 30 min.
- Names: worker `pstack-<6 hex>`, never reused; `host_id` 8 hex, generated once. Label `pstack-scaler=<host_id>`; on DigitalOcean the tags `pstack-scaler-<host_id>` and `pstack-scaler-<host_id>:<name>`.
- Files: `/state` 0700 (`${DATA_DIR}/scaler`), `scaler.json` and `workers.auto.tfvars.json` 0600, both written `.next` + rename. Compose: `image: ${PSTACK_IMAGE}`, `user: "0:0"`, `stop_grace_period: 5m`.
- Never in a report, event, log line or error: a credential value, a JSON credential's string leaves, `SWMTKN-…`, `user_data`. Tofu output is scrubbed whole, then tailed (last 20 lines).
- Roles: `GET /api/scaler`, `GET /api/scaler/presets[/:name]` maintainer; `PUT /api/scaler`, `PUT|DELETE /api/scaler/presets/:name` admin; `GET /api/scaler/config`, `POST /api/scaler/report` root only.
- Copy is terse (docs/ui-rules.md). Events append to the END of `events.Names` only. Invariant 20: no new `#__MARKER__`; scaler off renders byte-identical to today.
- `internal/scaler` never imports `internal/cli` or `internal/api`; it talks to pstack over HTTP only.

## Review Focus

1. **A create whose `apply` failed** is in the `workers` map but not in Tofu state. Expected: retried with backoff, never treated as lost or orphaned (Task 6 test).
2. **A crash between writing `scaler.json` and the apply**, or mid-write. Expected: the `.next` file never replaces a good record half-written; on restart the worker is an orphan or a retry, never a second server under a new name (Task 5, Task 7 tests).
3. **A second `pstack scaler` on the same `/state`** (an operator running it by hand beside the container). Expected: it exits at once naming the lock; the running one is untouched (Task 5 test).
4. **`max` lowered below the owned count while those workers are busy.** Expected: nothing is removed until a worker goes empty past `grace`; only `max: 0` evicts (Task 7 test).
5. **A stuck reason with a clause no machine can fix** mixed with `insufficient resources` (e.g. `scheduling constraints not satisfied on 1 node; insufficient resources on 2 nodes`). Expected: no scale-up (Task 6 test).

## Slices

| Slice | Tasks | Branch | Delivers |
|---|---|---|---|
| 1 | 1–4 | `claude/worker-scaler` | Storage, presets, the API. Nothing runs yet. |
| 2 | 5–8 | stacked on slice 1 | `pstack scaler`: the daemon, testable against a fake `tofu` and an `httptest` API. |
| 3 | 9–11 | stacked on slice 2 | `init --scaler on`, the Workers card, docs. |

## Plan rulings (controller, 2026-11-08)

- **P-R1 — "lost" applies only to applied workers.** A worker gets `appliedAt` when an apply that includes it succeeds. Missing from state with no `appliedAt` = a failed create, retried with backoff (spec: "a retry repeats the same intent"). The never-joined 10 min counts from `appliedAt`. Cost if wrong: none found.
- **P-R2 — refresh before every apply, not every tick:** `tofu apply -refresh-only -auto-approve` then `tofu state list`. A console deletion is still caught by the 10-min not-`ready` rule between applies. Cost if wrong: one extra provider read per apply.
- **P-R3 — pstack persists the whole last report** (minus `event`) in `scaler_config.report`, not only owned names and the locked config. One column; the card survives a pstack restart. A report of zero owned workers clears the lock, so a fresh `/state` cannot leave a permanent 409. Cost if wrong: a stale "last error" shown after a restart, with its time.
- **P-R4 — per-worker variables are the attribute names inside the `workers` object type, read by regex;** distro comes from a `# pstack-distro: <distro>` line in any `.tf` file, default `ubuntu`. The spec names no source for a custom preset's distro. `ponytail:` comment on the regex. Cost if wrong: a custom preset with a nested object type in `workers` is refused with a clear error.
- **P-R5 — while the last report shows owned workers or a pending old-preset destroy, `PUT /api/scaler` refuses (409) to delete any credential key.** The API cannot know which keys a preset needs. Cost if wrong: an operator deletes a stale key after the switch finishes.
- **P-R6 — variable values are strings;** one starting with `[` or `{` is written to tfvars as JSON (`ssh_keys = ["a"]`). Cost if wrong: a string variable whose value starts with `[`.
- **P-R7 — "shipped as a plain folder"** means `packages/pstack/internal/scaler/presets/<name>/` in the repo and `GET /api/scaler/presets/:name`. No release artifact. Cost if wrong: option-C users copy from GitHub.
- **P-R8 — Tofu's environment is exactly** `PATH`, `HOME=/state`, `TF_IN_AUTOMATION=1`, `TF_INPUT=0`, `TF_PLUGIN_CACHE_DIR=/state/plugins` and the credential map. Never `PSTACK_TOKEN`. Cost if wrong: none; a custom preset can still read files, as the spec says.
- **P-R9 — the scaler container sets `healthcheck: disable: true`.** The image's HEALTHCHECK polls the API on localhost, which the scaler does not serve. Cost if wrong: a permanently unhealthy container in the Containers card.
- **P-R10 — the basic embedded UI and `packages/client` get nothing** (forward plan P-R7 precedent).
- **P-R11 — a worker is "in state" when any `tofu state list` address contains `["<name>"]`.**

## Tofu commands (the whole set)

All run with cwd `/state/tofu/<preset>/`, P-R8's environment, and `-no-color`. Variables reach Tofu only through `workers.auto.tfvars.json` (auto-loaded), never argv.

| Step | Command |
|---|---|
| Preset files changed (hash differs from `State.Inited[preset]`) | `tofu init -input=false`, then `tofu validate` |
| Before every apply (P-R2) | `tofu apply -refresh-only -auto-approve -input=false`, then `tofu state list` |
| Create / remove a worker | `tofu apply -auto-approve -input=false` |
| Old preset after a switch | `tofu destroy -auto-approve -input=false` |
| Orphan check (first tick) | `tofu state list` |

**The fake `tofu`** (Tasks 5–7, `internal/scaler/faketofu_test.go`): the test binary re-executed. `TestMain` acts as Tofu when `PSTACK_FAKE_TOFU=<dir>` is set; `fakeTofu(t) (path, dir string)` writes a two-line `sh` wrapper that sets the variable and execs `os.Args[0]`. It appends each argv to `<dir>/calls.log` and its environment to `<dir>/env.log`; `apply` copies the `workers` keys of `./workers.auto.tfvars.json` into `<dir>/state.json`; `-refresh-only` drops names listed in `<dir>/deleted`; `state list` prints `hcloud_server.worker["<name>"]` per name; `destroy` empties the state; `<dir>/fail` makes `apply` print its content and exit 1.

---

### Task 1: The `scaler_config` and `scaler_presets` tables

**Files:**
- Modify: `packages/pstack/internal/store/migrations.go` (append entry 12 after :293)
- Create: `packages/pstack/internal/store/scaler.go`
- Test: `packages/pstack/internal/store/store_test.go`

**Interfaces:**
- Produces: `type ScalerConfig struct { Preset string; Vars map[string]string; Min, Max, CooldownMin, GraceMin int; Credentials map[string]string; Report string }`; `GetScalerConfig(q Querier) (*ScalerConfig, error)` (defaults 0/3/5/15 and empty maps when unset, never nil); `SetScalerSettings(q Querier, c ScalerConfig) error` (all but `Report`); `SetScalerReport(q Querier, report string) error`; `type ScalerPreset struct { Name string; Files map[string]string; UpdatedAt int64 }`; `ListScalerPresets(q Querier) ([]ScalerPreset, error)` (by name); `GetScalerPreset(q Querier, name string) (*ScalerPreset, error)` (nil, nil when absent); `PutScalerPreset(q Querier, p ScalerPreset) error`; `DeleteScalerPreset(q Querier, name string) (bool, error)`.

- [ ] **Step 1: Write the failing tests** — copy migration 11's subtests (store_test.go:256-300) as `migration 12 adds EMPTY scaler_config and scaler_presets tables and touches nothing else` and `scaler_config holds one row: a second id violates CHECK (id = 1)`. Add `TestScalerConfigRoundTrip`: Get on an empty db → `{Min:0, Max:3, CooldownMin:5, GraceMin:15}` with non-nil empty maps; SetScalerSettings then Get equals it; SetScalerReport(`{"x":1}`) leaves the settings unchanged; SetScalerSettings leaves the report unchanged. `TestScalerPresetRoundTrip`: Put two, List is name-ordered, Get absent → nil, Delete → true then false.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/store/ -run 'Scaler|migration_12'` — FAIL.
- [ ] **Step 3: Implement.** Migration 12, with migration 11's comment style: `scaler_config (id INTEGER PRIMARY KEY CHECK (id = 1), preset TEXT NOT NULL DEFAULT '', vars TEXT NOT NULL DEFAULT '{}', min INTEGER NOT NULL DEFAULT 0, max INTEGER NOT NULL DEFAULT 3, cooldown_min INTEGER NOT NULL DEFAULT 5, grace_min INTEGER NOT NULL DEFAULT 15, credentials TEXT NOT NULL DEFAULT '{}', report TEXT NOT NULL DEFAULT '')` with `-- CONFIGURATION AN OPERATOR CHOSE (invariant 10)` and `-- credentials: STORED, NOT HASHED — the scaler hands them to Tofu. Only GET /api/scaler/config (root) returns them; the protection is the 0700 db directory.`; `scaler_presets (name TEXT PRIMARY KEY, files TEXT NOT NULL, updated_at INTEGER NOT NULL)`. Accessors in `store/forward.go`'s Querier style; maps as JSON text.
- [ ] **Step 4: Run** the store tests and `go test ./packages/pstack/internal/auth/` — PASS.
- [ ] **Step 5: Commit** `feat(store): scaler_config and scaler_presets tables`.

### Task 2: The `internal/scaler` package: wire types, preset parsing, the Hetzner preset

**Files:**
- Create: `packages/pstack/internal/scaler/wire.go`, `preset.go`, `presets.go` (`//go:embed all:presets`), `presets/hetzner/main.tf`, `presets/hetzner/.terraform.lock.hcl`
- Test: `packages/pstack/internal/scaler/preset_test.go`

**Interfaces:**
- Produces (wire.go, the JSON both sides speak):
  - `type Settings struct { Preset string \`json:"preset"\`; Vars map[string]string \`json:"vars"\`; Min, Max int; CooldownMin, GraceMin int }` (json `min`, `max`, `cooldownMin`, `graceMin`).
  - `type Config struct { Settings; Credentials map[string]string \`json:"credentials"\`; Files map[string]string \`json:"files"\` }` — `GET /api/scaler/config`.
  - `type Worker struct { Name string; Vars map[string]string; State string; CreatedAt int64; Removing bool }` (json `name, vars, state, createdAt, removing`; `State` is `booting|ready|down|lost|removing`).
  - `type Locked struct { Preset, FilesHash string; Shared map[string]string }` (json `preset, filesHash, shared`).
  - `type Event struct { Name string; Data any }`.
  - `type Report struct { At int64; HostID string; Workers []Worker; Locked *Locked; LastAction, LastError string; NextRetryAt int64; DoesNotFit, Orphans []string; PendingDestroy string; Event *Event }` (json camelCase; `event` omitempty).
- Produces (preset.go): `type Preset struct { PerWorker, Shared []string; Distro, Hash string; LockFile bool }` (json `perWorker, shared, distro, hash, lockFile`); `ParsePreset(files map[string]string) (Preset, error)`; `ValidPresetName(name string) error` (calls `specs.AssertValidSpecName`, rewording "spec" to "preset"; refuses a built-in name); `Builtins() map[string]map[string]string`.

- [ ] **Step 1: Write the failing tests:**
  - `TestParsePresetHetzner`: `ParsePreset(Builtins()["hetzner"])` → PerWorker `[location server_type]` (sorted), Shared `[network_id ssh_keys]` (excludes `workers`, `manager_address`, `host_id`), Distro `debian`, LockFile true, Hash 64 hex and stable across two calls.
  - `TestEveryBuiltinEmbedsItsLockFile`: each `Builtins()` entry has `.terraform.lock.hcl` with a `provider "registry.opentofu.org/…"` block and at least two `h1:` hashes (amd64 and arm64; the dot-file is embedded only with `all:`).
  - `TestParsePresetRefuses` table, each an error naming the reason: no `variable "workers"`; a `workers` object without `user_data`; a `terraform { backend "s3" {} }` block; a `cloud {` block; a file named `x.tf.json`; a file named `../a.tf`.
  - `TestParsePresetOptionalAttrs`: a `workers` type with `size = optional(string, "cx22")` parses `size` as per-worker.
  - `TestPresetHashIgnoresMapOrder`: same files, two map insertions → same `Hash`; one byte changed → different.
  - `TestValidPresetName`: `hetzner` refused (built-in); `My Preset`, `../x` refused; `team-a` accepted.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/` — FAIL.
- [ ] **Step 3: Implement.** Hash = sha256 over the file names sorted, each `name\x00content\x00`. File names: `^[A-Za-z0-9_-]+\.tf$` or exactly `.terraform.lock.hcl`. Regexes (P-R4): `(?m)^\s*variable\s+"([A-Za-z0-9_]+)"`; the `workers` block's `object({ … })` body, attribute names `(?m)^\s*([A-Za-z0-9_]+)\s*=`; `(?m)^\s*(backend\s+"|cloud\s*\{)`; `(?m)^#\s*pstack-distro:\s*(\S+)`.
  The Hetzner preset follows the spec table and contract exactly: `hcloud_server.worker` `for_each = var.workers`, `name = each.key`, `image = "debian-12"`, `server_type`/`location` from `each.value`, `user_data = each.value.user_data`, `labels = { "pstack-scaler" = var.host_id }`, `firewall_ids`, `dynamic "network"` on `var.network_id`, `lifecycle { ignore_changes = [user_data, image] }`; `hcloud_firewall` named `pstack-scaler-${var.host_id}` applied by `label_selector = "pstack-scaler=${var.host_id}"` with the spec's rules and outbound tcp/udp/icmp to `0.0.0.0/0, ::/0`; `# pstack-distro: debian`. Lock file: `tofu providers lock -platform=linux_amd64 -platform=linux_arm64` in the preset dir (Global Constraints' `tofu`); then `tofu init -backend=false && tofu validate` passes; delete `.terraform/`.
- [ ] **Step 4: Run** `go test ./packages/pstack/internal/scaler/` — PASS.
- [ ] **Step 5: Commit** `feat(scaler): preset contract and the hetzner preset`.

### Task 3: The AWS, GCP, Azure and DigitalOcean presets

**Files:**
- Create: `packages/pstack/internal/scaler/presets/{aws,gcp,azure,digitalocean}/{main.tf,.terraform.lock.hcl}`
- Modify: `.github/workflows/ci.yml` (a `presets` job beside `go`, :44, added to the `ci` gate's `needs`, :198), `packages/pstack/internal/scaler/preset_test.go`

**Interfaces:**
- Consumes: `ParsePreset`, `Builtins` (Task 2).

- [ ] **Step 1: Write the failing test** `TestBuiltinPresets` table over all five: PerWorker, Shared and Distro exactly as the spec table (aws: `[instance_type]`, `[key_name region subnet_id]`, `ubuntu`; gcp: `[machine_type zone]`, `[project subnetwork]`, `ubuntu`; azure: `[vm_size]`, `[admin_ssh_public_key location resource_group subnet_id]`, `ubuntu`; digitalocean: `[region size]`, `[ssh_keys vpc_uuid]`, `ubuntu`); and per preset, text checks: the `ignore_changes` list names its user-data attribute (`user_data`, `custom_data`, `metadata["user-data"]`) and the image attribute; the label/tag form of Global Constraints; aws has `egress` to `0.0.0.0/0` and `cidr_block_associations`; azure has `base64encode(each.value.user_data)`; digitalocean has `outbound_rule` and a `digitalocean_tag`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/ -run BuiltinPresets` — FAIL.
- [ ] **Step 3: Implement** each preset from its spec-table row and the contract (as Task 2's Hetzner). AMIs and images by data source or family name (Canonical Ubuntu LTS, owner `099720109477` on AWS; `ubuntu-os-cloud/ubuntu-2404-lts-amd64` on GCP; Canonical `ubuntu-24_04-lts` `server` on Azure; `ubuntu-24-04-x64` on DigitalOcean). Lock files as in Task 2. CI job: `opentofu/setup-opentofu@v1` with `tofu_version: 1.13.9`, then for each `packages/pstack/internal/scaler/presets/*/`: `tofu init -backend=false -lockfile=readonly && tofu validate`.
- [ ] **Step 4: Run** the Go tests and, locally, the CI job's loop — PASS. Delete every `.terraform/`.
- [ ] **Step 5: Commit** `feat(scaler): aws, gcp, azure and digitalocean presets`.

### Task 4: `/api/scaler` routes and the `scaler.*` events

**Files:**
- Create: `packages/pstack/internal/api/routes_scaler.go`, `routes_scaler_test.go`, `packages/conformance/test/api-scaler.test.ts`
- Modify: `internal/api/routes.go` (literals beside `/api/forward`, :189-198; `scalerPresetRe` beside `swarmNodeRe`, :40), `permissions.go` (a `── worker scaler ──` block after forwarding, :202; the two root-only rows with no tier, as `/api/config` :125), `permissions_test.go`, `events/events.go` (append `"scaler.created", "scaler.removed", "scaler.failed"` after :126 with a comment), `events/events_test.go`, `notify/notify.go` (`Summarize` cases after :512), `notify/notify_test.go`, `config/config.go` (Skipped line after :275), `config/config_test.go`, `packages/pstack/api/openapi.yaml` (tag `scaler`, 8 operations, `x-cli-name`), `apicli/` (regenerate; `OperationCount` 100 → 108), `cli/api.go` (`groupShort["scaler"]`), `packages/conformance/test/api-rbac.test.ts`, `packages/conformance/expected-pass.json`

**Interfaces:**
- Consumes: Task 1's store functions; Task 2's wire types, `ParsePreset`, `ValidPresetName`, `Builtins`.
- Produces:
  - `GET /api/scaler` → `{settings: Settings, credentials: {"KEY": true}, report: Report|null, enabled: bool|null}`.
  - `PUT /api/scaler` body `Settings & {credentials?: {"KEY": "value"|""|"••••••••"|null}}` → the GET shape.
  - `GET /api/scaler/presets` → `[{name, builtin, preset: Preset, locked}]`; `GET /api/scaler/presets/:name` → `{name, builtin, files, preset, locked}`; `PUT` body `{files}` → that shape; `DELETE` → `{deleted: true}`.
  - `GET /api/scaler/config` → `Config`; `POST /api/scaler/report` body `Report` → `{ok: true}`.
  - `func scalerEnabled(view inspect.ControlView) *bool` (copy of `lokiEnabled`, service `scaler`).

- [ ] **Step 1: Write the failing tests** (`New(...)` server as routes_forward_test.go; principals root, admin, maintainer PATs as routes_grafana_test.go:169-186):
  - `TestScalerGetHidesCredentials`: PUT `{preset:"hetzner", credentials:{HCLOUD_TOKEN:"tok-123456789"}}` as admin → 200; GET as maintainer has `credentials: {HCLOUD_TOKEN: true}` and the body contains no `tok-123456789`.
  - `TestScalerCredentialKeepRules`: stored `A`; PUT with `A:""`, with `A:"••••••••"`, with `A` omitted → still stored; `A:null` → gone; `A:"new"` → replaced (read back through `/api/scaler/config` as root).
  - `TestScalerPutValidates` → 400 each: unknown preset; `min` 2 `max` 1; `min` -1; a var name not in the preset's PerWorker+Shared.
  - `TestScalerLockedConfig`: POST report with `locked {preset:"hetzner", filesHash:<hetzner hash>, shared:{ssh_keys:"[\"k\"]"}}` and one worker; PUT changing `ssh_keys` → 409; PUT changing `preset` → 409; PUT restoring the locked values with a new `min` → 200; PUT deleting a credential → 409 (P-R5). A fresh `New(...)` on the same db still answers 409 (P-R3). Then a report with zero workers and `locked: null` → the same PUT → 200.
  - `TestScalerPresetRoutes`: PUT custom `team-a` with a valid `main.tf` → 200 and listed with `builtin:false`; PUT `hetzner` → 400; PUT name `My X` → 400; PUT a `main.tf` without `variable "workers"` → 400 naming it; DELETE `team-a` → 200; with `team-a` locked → PUT and DELETE 409.
  - `TestScalerConfigIsRootOnly`: admin session and admin PAT → 403; root → 200 with credential values and the preset's files (built-in files come from `Builtins()`).
  - `TestScalerReport`: root POST with `event {name:"scaler.created", data:{name:"pstack-abc123", settings:{server_type:"cx22"}}}` → one `scaler.created` event; an event name outside the three → 400 and nothing emitted; the stored report has no `event`; admin POST → 403.
  - `TestScalerEnabled`: no `scaler` control container → `enabled:false`; one → `true`; docker unreachable → `null`.
  - permissions_test rows for every route above; events_test `want` ends `,scaler.created,scaler.removed,scaler.failed`; notify_test cases; config_test: a set scaler row → `Skipped` has `scaler: host-specific — re-enter it on the target` and the export has no credential value.
  - conformance `api-scaler.test.ts`: GET `/api/scaler` → `settings.max` 3, `enabled` false; PUT `{preset:"nope"}` → 400. api-rbac rows: `GET /api/scaler` maintainer 200; `PUT` admin 400 on an empty body; `GET /api/scaler/config` admin 403.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/api/ -run Scaler` — FAIL.
- [ ] **Step 3: Implement.** Root-only handlers check `who.Kind == auth.KindRoot` a second time, as `mayMoveConfig` (routes_config.go:62). The PUT's 409 compares against `report.locked` (`preset`, `ParsePreset(files).Hash`, and the `Shared` vars it names). Event names accepted from a report: exactly the three. Events emitted outside any lock. `Summarize`: `scaler.created` → `Worker <name> created`, `scaler.removed` → `Worker <name> removed (<reason>)`, `scaler.failed` → `Scaler <action> failed: <error>`.
- [ ] **Step 4: Regenerate and run:** `go generate ./packages/pstack/internal/apicli`; `go test ./packages/pstack/...`; `cd packages/conformance && bun test test/api-scaler.test.ts test/api-rbac.test.ts && bun run vacuity && bun run ratchet` — PASS, ratchet raised.
- [ ] **Step 5: Commit** `feat(api): /api/scaler`.

### Task 5: Scaler runtime pieces

**Files:**
- Create: `packages/pstack/internal/scaler/{tofu.go,state.go,client.go,hostname.go,scrub.go}`, `faketofu_test.go`, `runtime_test.go`

**Interfaces:**
- Consumes: Task 2's wire types; `redact.RedactText`.
- Produces:
  - `const TofuVersion = "1.13.9"`; `var tofuSums = map[string]string{"amd64": …, "arm64": …}`; `var tofuBase = "https://github.com/opentofu/opentofu/releases/download"`; `EnsureTofu(binDir string, c *http.Client) (string, error)` — present and executable → its path; else download `tofu_<v>_linux_<GOARCH>.zip`, check the sum, extract `tofu` at 0755. Other arch → error.
  - `type Tofu struct { Bin, Dir string; Creds map[string]string }`; `(t Tofu) Run(args ...string) (out string, err error)` — P-R8 env, cwd `Dir`, combined output scrubbed; error text is `Tail(Scrub(out, t.Creds), 20)`. No context: a started command runs to its end (spec, SIGTERM row).
  - `WriteTFVars(dir string, v map[string]any) error` (0600, `.next` + rename).
  - `type OwnedWorker struct { Name string; Vars map[string]string; UserData string; CreatedAt, AppliedAt, ReadyAt int64; Removing bool; Reason string; NodeID string; StuckServices []string }`; `type State struct { HostID string; Workers []OwnedWorker; Locked *Locked; LastPreset string; Inited map[string]string; LastScaleUp, NextTryAt int64; Failures, NeverJoined int; SettingsHash string; DoesNotFit, Orphans []string; PendingDestroy string }`; `LoadState(dir string) (*State, error)` (absent → new, with an 8-hex `HostID`, saved at once); `(s *State) Save(dir string) error` (0600, `.next` + rename).
  - `Lock(dir string) (release func(), err error)` — `syscall.Flock` `LOCK_EX|LOCK_NB` on `<dir>/scaler.lock`; held → `another pstack scaler holds <dir>`.
  - `type Client struct { Base, Token string; HTTP *http.Client }`; `(c Client) Do(method, path string, body, out any) error` (non-2xx → error with the API's `error` text).
  - `WithHostname(cloudConfig, name string) (string, error)` — inserts `hostname: <name>` and `preserve_hostname: false` after the `#cloud-config` first line; errors if the first line differs or a top-level `hostname:` exists.
  - `Scrub(text string, creds map[string]string) string`; `Tail(text string, n int) string`.

- [ ] **Step 1: Write the failing tests:**
  - `TestEnsureTofu`: an `httptest` server serving a zip with a `tofu` file; `tofuSums[runtime.GOARCH]` set to its sum → path exists, mode 0755, second call makes no request; a wrong sum → error, no file left.
  - `TestTofuEnv`: fake tofu `Run("state","list")` with `PSTACK_TOKEN=x` in the test's env → `env.log` has the creds and P-R8's keys and no `PSTACK_TOKEN`.
  - `TestTFVarsAndStateAre0600`: both files 0600; a stale `.next` from a crash is replaced; the target is never left empty when the write fails (make the dir read-only after the first write).
  - `TestLoadStateMakesHostID`: absent → `HostID` 8 hex, persisted; second load → same id.
  - `TestLockIsExclusive`: second `Lock` on the dir errors naming it; after `release()` it succeeds.
  - `TestWithHostname`: the real join cloud-config (`packages/conformance/golden/cli/swarm-join-cloud-config.json` stdout) plus the hostname → parses with goccy/go-yaml, top-level `hostname` = name, `preserve_hostname` false, every original key kept; a text already holding `hostname:` → error.
  - `TestScrub`: a credential value; `GOOGLE_CREDENTIALS` JSON whose `private_key` is `-----BEGIN…\n…` appears in the output decoded and JSON-escaped → both gone; `SWMTKN-1-abc-def` gone; a 7-char leaf kept. `Tail` keeps the last n lines of the scrubbed text (a secret spanning the cut is still gone).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/ -run 'Tofu|TFVars|State|Lock|Hostname|Scrub'` — FAIL.
- [ ] **Step 3: Implement** per the signatures. Scrub extras: each credential value; for a value that parses as a JSON object, each string leaf ≥ 8 chars, decoded and as `json.Marshal` emits it without the quotes.
- [ ] **Step 4: Run** the tests — PASS.
- [ ] **Step 5: Commit** `feat(scaler): tofu, state and api client`.

### Task 6: The loop — read, guards, scale up, report

**Files:**
- Create: `packages/pstack/internal/scaler/loop.go`, `loop_up_test.go`, `loop_helpers_test.go` (an `httptest` API serving `/api/scaler/config`, `/api/signals`, `/api/swarm`, `/api/swarm/join`, the node routes, recording `/api/scaler/report` bodies)

**Interfaces:**
- Consumes: Task 5's pieces.
- Produces: `const (Tick = 30 * time.Second; BootTimeout = 10 * time.Minute; LostAfter = 10 * time.Minute; BackoffMax = 30 * time.Minute)`; `type Options struct { API Client; StateDir, Tofu string; Now func() time.Time; Log io.Writer }`; `New(o Options) (*Loop, error)`; `(l *Loop) Tick() error` — one pass of spec steps 1–7, at most one apply/destroy; `Qualifies(reason string) bool`.

- [ ] **Step 1: Write the failing tests** (fake clock; one `Tick()` per step):
  - `TestQualifies` table: `no suitable node (insufficient resources on 2 nodes)` true; `… (1 node not available for new tasks)` true; both joined by `; ` true; `… (scheduling constraints not satisfied on 1 node; insufficient resources on 2 nodes)` false (Review Focus 5); `no suitable node` (no clause) false; `never allocated: …` false.
  - `TestScaleUpOnStuck`: one qualifying stuck entry, no owned workers → one apply; the tfvars `workers` has one `pstack-[0-9a-f]{6}` key whose `user_data` carries `hostname:` and the current per-worker vars; shared vars, `manager_address` (host part of `managerAddr`) and `host_id` present only if the preset declares them; the first report naming the worker is posted **before** the apply (order in the fake API's log vs `calls.log`); a `scaler.created` event without `user_data`.
  - `TestMinFill`: `min` 2, nothing stuck → one worker per tick while none is booting; the second waits for the first to be `ready`.
  - `TestScaleUpGuards` → no apply, reason in `lastAction`: cooldown not passed; a worker booting; `max` reached (one `scaler.failed {action:"max"}` per stuck episode, not per tick); 2 never-joined; a ready empty worker with the current settings exists; the service is does-not-fit; `swarm:false`; `reachable:false`; config 401; no preset set.
  - `TestFrozenClocks`: with `reachable:false` for 20 ticks, a booting worker is not declared never-joined and cooldown does not advance.
  - `TestNeverJoined`: worker applied, not in signals' nodes after `BootTimeout` → removing, `scaler.failed`, count 1; a second → scale-ups stop, card reason; a settings change resets the count.
  - `TestDoesNotFit`: worker ready, the services it was created for still stuck and it empty for a tick → those services in `DoesNotFit`, `scaler.failed {action:"does-not-fit"}`, no second scale-up for them; a per-worker var change clears it.
  - `TestFailedApplyRetries` (Review Focus 1, P-R1): `<dir>/fail` set → `scaler.failed` with Tofu's scrubbed tail, `NextTryAt` = now+1 min; fail removed, tick before then → no apply; after → apply with the same name; the worker never becomes lost or orphaned; backoff survives `New` on the same dir and doubles to 30 min max.
  - `TestLostBeforeApply` (P-R2): an applied worker listed in `<dir>/deleted` → on the next apply's refresh it becomes removing (`reason: lost`) and is absent from that apply's map.
  - `TestLockedConfigRefused`: owned worker locked to `ssh_keys` A; pulled config with B → no apply, `lastError` names it.
  - `TestPrivateManagerNeedsNetwork`: `managerAddr` `10.0.0.1:2377`, hetzner with empty `network_id` → no apply, reason names `network_id`; set → apply.
  - `TestTfvarsNeverOnArgv`: no `calls.log` line contains a var value or `SWMTKN`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/ -run 'Qualifies|Scale|Min|Frozen|Never|DoesNot|Failed|Lost|Locked|Private|Tfvars'` — FAIL.
- [ ] **Step 3: Implement.** `Tick` order: read (any failure → report, return) → preset dir sync and `init`/`validate` when the hash changed → step 2 (Task 7) → step 3's refresh-before-apply (here) and not-`ready` rule (Task 7) → step 4 → steps 5–6 (Task 7) → report. Task 6 only marks a worker removing; Task 7 carries out the removal. Every decision is written to `scaler.json` before its Tofu command. Settings hash = preset name + per-worker vars; a change resets `NeverJoined` and `DoesNotFit`. Booting = applied, not `ready`, younger than `BootTimeout`. Node match by hostname = worker name, role `worker` only.
- [ ] **Step 4: Run** the tests and `go test -race ./packages/pstack/internal/scaler/` — PASS.
- [ ] **Step 5: Commit** `feat(scaler): the loop scales up`.

### Task 7: The loop — removal, scale down, `max: 0`, orphans, switching presets

**Files:**
- Modify: `packages/pstack/internal/scaler/loop.go`
- Create: `packages/pstack/internal/scaler/loop_down_test.go`

**Interfaces:**
- Consumes: Task 6's `Loop`, helpers.

- [ ] **Step 1: Write the failing tests:**
  - `TestRemovalSteps`: a removing worker with a node → tick 1 drain; tick 2 apply without it; node `down` → tick 3 `DELETE /api/swarm/nodes/:id`; then forgotten (absent from state and nodes) and `scaler.removed {name, reason}`. A removing worker with no node → apply, forgotten, no node calls.
  - `TestRemovalSurvivesRestart`: kill (new `New` on the same dir) after each of the steps above → it resumes at the next undone step, never repeats a DELETE that succeeded, never creates the worker again.
  - `TestScaleDown`: ready, active, empty longer than `grace`, owned > `min` → re-read signals, then removing (`reason: empty`); `emptySince` moved or a task appeared on the re-read → skipped; owned = `min` → kept.
  - `TestMaxBelowOwnedKeepsBusy` (Review Focus 4): `max` 3 → 1 with three busy workers → nothing removed; as each goes empty past grace, removed down to `min`.
  - `TestStaleSettingsReplaced`: per-worker var changed → an empty-past-grace old worker is removed even at `min` (`reason: replaced`), then `min` fill creates one with the new vars.
  - `TestMaxZero`: three owned, one busy → one becomes removing per tick without grace (`reason: max`).
  - `TestOwnershipOnly`: signals list a manager and a hand-added worker, both empty for an hour → never drained, applied or deleted.
  - `TestOrphanCheck` (Review Focus 2): `scaler.json` holds a non-removing worker absent from `state list` on the first tick → `Orphans` has it, `scaler.failed {action:"orphan"}`, it becomes removing; a removing worker absent from state is not an orphan.
  - `TestPresetSwitch`: zero owned, `LastPreset` hetzner, config now aws → `tofu destroy` in `tofu/hetzner`, then `PendingDestroy` empty; destroy fails → reported (`action: destroy`), `tofu/hetzner` kept, aws proceeds.
  - `TestLostNotReady`: an applied worker whose node is `down` for `LostAfter` → removing (`reason: lost`).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/ -run 'Removal|ScaleDown|MaxBelow|Stale|MaxZero|Ownership|Orphan|PresetSwitch|LostNot'` — FAIL.
- [ ] **Step 3: Implement** spec steps 2, 3, 5, 6 and the orphan and switch rules in `Tick`. Drain an already-drained node and DELETE a missing node are both treated as done (404 on DELETE = gone).
- [ ] **Step 4: Run** `go test -race ./packages/pstack/internal/scaler/` — PASS.
- [ ] **Step 5: Commit** `feat(scaler): the loop removes workers`.

### Task 8: `pstack scaler`

**Files:**
- Create: `packages/pstack/internal/scaler/run.go`, `packages/pstack/internal/cli/scaler.go`, `scaler_test.go` (cli)
- Modify: `cli/args.go` (`Commands` += `scaler` before `healthcheck`, :362; Usage line), `cli/run.go` (`case "scaler"` beside `serve`, :375), `cli/commands.go` (help entry), `packages/conformance/golden/cli/{help,help-h,no-args,unknown-command}.json` (regenerated)

**Interfaces:**
- Consumes: Tasks 5–7.
- Produces: `scaler.Run(ctx context.Context, o RunOptions) error` with `RunOptions{API Client; StateDir string; Log io.Writer}` — `Lock`, `EnsureTofu(<StateDir>/bin)`, `New`, `Tick` every `Tick` until `ctx` is done; a tick in progress finishes first. `func scalerCmd(args *Parsed, ios IO) *Exit` — env `PSTACK_API_URL` (default `http://pstack:7878`), `PSTACK_TOKEN` (required), `PSTACK_SCALER_STATE` (default `/state`); SIGTERM/SIGINT cancel the context.

- [ ] **Step 1: Write the failing tests:**
  - `TestRunFinishesTheTickOnCancel`: a fake tofu whose `apply` sleeps 2 s; cancel mid-apply → `Run` returns after the apply ended, `state.json` written, `scaler.json` records `AppliedAt`.
  - `TestScalerCmdNeedsToken`: no `PSTACK_TOKEN` → exit 3 naming it, no request.
  - `TestScalerCmdSecondInstance` (Review Focus 3): `/state` locked by the test → exit 1 with `another pstack scaler holds`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/scaler/ ./packages/pstack/internal/cli/ -run 'RunFinishes|ScalerCmd'` — FAIL.
- [ ] **Step 3: Implement.** Help page opens `pstack scaler — ` and lists the three env vars; says it runs inside the control stack's `scaler` container (`pstack init --scaler on`).
- [ ] **Step 4: Regenerate goldens and run:** `cd packages/pstack && bun run build && cd ../conformance && bun gen/goldens.ts && bun test test/cli-goldens.test.ts`; `go test ./packages/pstack/internal/cli/` — PASS; `git diff --stat packages/conformance/golden` shows only the four goldens.
- [ ] **Step 5: Commit** `feat(cli): pstack scaler`.

### Task 9: `init --scaler on`

**Files:**
- Modify: `internal/initctl/init.go` (`Options.Scaler bool` after `Logging`, :205; refusal beside `--previews-on`'s, :440-442; `ensureDir(<data>/scaler, 0o700)` when on, after :401; `ScalerService` appended at :574 after `GrafanaService`; `ScalerWiring` after `LokiWiring`, :575), `internal/initctl/init_test.go`, `cli/args.go` (`Scaler string`, `or("PSTACK_SCALER", "off")`, `--scaler on|off` with `Typed`, validation beside :333), `cli/run.go` (pass to init), `cli/initguard.go` (row after :111), `cli/initguard_test.go`, `internal/upgrade/upgrade.go` (`scalerSvc` regex beside `lokiSvc` :101; readback :210; `--scaler on` in the flags builder after :283), `upgrade_test.go`, `internal/cloudinit/cloudinit.go` (`Scaler string` beside `PreviewsOn` :213; compose refusal beside :247; `--scaler on` beside :397), `cloudinit_test.go`, `cli/commands.go` (init and cloud-init help), `packages/conformance/gen/goldens.table.ts` (`SCALER_CELLS` like `LOKI_CELLS` :38-41, a `cloud-init-scaler` case beside :118), new goldens under `packages/conformance/golden/render/control/` and `golden/cli/`

**Interfaces:**
- Produces: `func ScalerService(on bool) string` — `""` when off, else exactly:
  ```yaml

    # Worker scaler, opt-in (`pstack init --scaler on`). Adds and removes swarm workers through
    # OpenTofu (docs/worker-scaler-design.md). No docker socket, no ports.
    scaler:
      image: ${PSTACK_IMAGE}
      command: ["pstack", "scaler"]
      user: "0:0"
      restart: unless-stopped
      stop_grace_period: 5m
  <controlLogCap>
      # Only pstack shares this network: the scaler sends the root token to pstack, and any preview
      # on preview-ingress could answer to that name.
      networks: [scaler]
      environment:
        PSTACK_API_URL: http://pstack:7878
        PSTACK_TOKEN: ${PSTACK_TOKEN}
      volumes:
        - ${DATA_DIR}/scaler:/state
      healthcheck:
        disable: true   # the image's check polls an API this container does not serve (P-R9)
  ```
  `func ScalerWiring(template string, on bool) (string, error)` — LokiWiring's shape: pstack's `    networks: [preview-ingress, preview-shared]\n` → `    networks: [preview-ingress, preview-shared, scaler]\n` (scoped to the pstack block by a `pstackBlock` haystack, as `traefikBlock`); `  preview-shared:\n    external: true\n` → the same plus `  scaler: {}\n`. Error text `the control template has no %s (%q) for --scaler on to extend`.

- [ ] **Step 1: Write the failing tests:**
  - `TestScalerOffIsByteIdentical`: the 8 `TestInitGoldens` cells unchanged (the existing test, run).
  - `TestScalerService`: on → the block above, parsed by goccy/go-yaml, with `image: ${PSTACK_IMAGE}`; pstack's networks line has `scaler`; top-level `scaler: {}`; with Loki also on, `scaler: {}` follows `preview-shared` and precedes `logs: {}`; a template without pstack's networks line → the named error.
  - `TestInitScalerDir`: on → `<data>/scaler` 0700; off → not created.
  - `TestInitScalerNeedsSwarm` and `cloudinit` `TestScalerNeedsSwarm`: compose → `--scaler needs swarm: under compose there is one node and no workers`.
  - upgrade: a compose with `  scaler:` → `State.Scaler` true and `--scaler on` in the init flags; `pstack logging loki` keeps it.
  - initguard: a run without `--scaler` on a scaler host → refused with fix `--scaler on`; `--scaler off` typed → allowed.
  - cloud-init: `--scaler on` → the init line carries `--scaler on`; no credential in the rendered text.
  - conformance: `SCALER_CELLS` `http01-basic-swarm` and `dns01-advanced-swarm` (the latter with `--logging loki`) render `init-<cell>-scaler` and `init-dns01-advanced-swarm-loki-scaler` goldens; `cloud-init-scaler`.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/initctl/ ./packages/pstack/internal/upgrade/ ./packages/pstack/internal/cloudinit/ ./packages/pstack/internal/cli/ -run Scaler` — FAIL.
- [ ] **Step 3: Implement** per the interfaces. `Scaler` and `Logging` both read back by `upgrade`; `SwitchLogging` and `SwitchUI` carry `Scaler` through unchanged.
- [ ] **Step 4: Regenerate and run:** build, `bun gen/goldens.ts`, `git diff --stat packages/conformance/golden` shows only new files plus the help goldens; `go test ./packages/pstack/...`; `bun run check` — PASS.
- [ ] **Step 5: Commit** `feat(init): --scaler on`.

### Task 10: The Workers card

**Files:**
- Create: `apps/ui-hearth/src/views/control/WorkersCard.vue`
- Modify: `apps/ui-hearth/src/api-control.ts` (types `ScalerView`, `ScalerPreset`; `controlApi.scaler`, `saveScaler`, `presets`, `preset`, `savePreset`, `deletePreset`), `apps/ui-hearth/src/views/ControlView.vue` (`<WorkersCard v-if="can('maintainer')" :can-admin="can('admin')" />` after `LoggingCard`, :99), `apps/ui/src/api/types.ts` (the same types after `ForwardStatus`, :781), `apps/ui/src/views/ControlView.vue` (a `section.panel.settings-form` after the Logging section, :530)

**Interfaces:**
- Consumes: Task 4's GET/PUT shapes.

- [ ] **Step 1: Implement** in both UIs: shown while `enabled !== false` or `report.workers` is non-empty; `enabled === null` → `Docker did not answer.` Fields: preset select; one input per variable of the chosen preset (per-worker and shared, labelled by name); min, max, cooldown (min), grace (min); credential inputs (admin only, `type=password`, `autocomplete=new-password`, hint `Write-only. Leave empty to keep.` when set, a key name input to add one); `host_id` in `mono`; a table of owned workers (name, settings, state, created); last action, last error, next retry; does-not-fit services; orphans; a pending destroy. Locked: preset and shared inputs disabled with the text `Locked while workers exist.` Custom presets (admin): name, one textarea per file, add file, Save, Delete (ActionButton confirm in apps/ui, HAlertDialog in Hearth); built-ins read-only; `No lock file: providers download unpinned.` when `preset.lockFile` is false. Poll every 10 s, refilling the draft only when the saved values change (LoggingCard.vue:29-30).
- [ ] **Step 2: Verify** with `pstack-ui-verify`: `serve.sh start scaler swarm`; add a `scaler` control container to `<run>/shim/docker` (the `loki` container line in goldens.table.ts:65 with service `scaler`); seed `scaler_config` with sqlite (preset `hetzner`, a credential, and a `report` JSON with two workers, a does-not-fit service and an orphan). Shots of `/control` in both UIs at 1280 and 320, light and dark; admin and a maintainer; `overflowX` false, no `errors`. Stop the run.
- [ ] **Step 3: Run** `bun run check` — PASS.
- [ ] **Step 4: Commit** `feat(ui): the Workers card`.

### Task 11: Docs

**Files:**
- Modify: `docs/usage.md` (a `## Worker scaler` section beside the swarm sections: turning it on, the preset table, network rules, the manager's firewall and the label or tag, reservations, the option-C rules, how to re-run `init` with `--scaler on` on an existing host; Commands, Environment, HTTP API and role tables; "I want to…" row), `docs/control-plane.md` (the service and its network), `AGENTS.md` (migration and table counts, event count, invariant 15's list plus "the ONE deliberate exception" sentence amended to name `GET /api/scaler/config`), `docs/webhook-events.md` (`scaler.created` / `scaler.removed` / `scaler.failed` after forward, :560), `docs/node-signals-design.md` (the spec's amendment), `docs/README.md` (event count, task row), `skills/pstack/SKILL.md` (a scaler section and route rows), `packages/pstack/CHANGELOG.md` (`## Unreleased` → `### Added`), `docs/worker-scaler-design.md` ("Where the build differs" banner: P-R1–P-R6, P-R9)

- [ ] **Step 1: Write** each section from the spec; every command and route shown is one Tasks 4, 8 and 9 implement.
- [ ] **Step 2: Check** `bun scripts/sync-docs.ts` (apps/docs) runs clean; `grep -n 'scaler' docs/usage.md` hits every table above.
- [ ] **Step 3: Commit** `docs: worker scaler`.

### After Task 11

`bun run check` green. Not automatable, done by hand before release: the spec's out-of-band run per provider (Testing).
