# Migrate and forwarding — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pstack migrate` copies deployments and their state between hosts; a `forward` setting makes a host pass its deploy API and unserved preview traffic to another host.

**Architecture:** One single-row SQLite table holds the setting. One Go file (`internal/api/forward.go`) owns the outbound transport (dial `via`, keep the hostname for Host/SNI, retry dials) and two `httputil.ReverseProxy` uses: the deploy API after A's role gate, and preview traffic before the wake block. Two CLI commands call the existing APIs.

**Tech Stack:** Go (stdlib `net/http/httputil`, `crypto/tls`), SQLite migrations, Vue (apps/ui, apps/ui-hearth), bun conformance.

**Spec:** `docs/migrate-forward-design.md`

## Global Constraints

- VCS writes through `but` only; commit with explicit file ids (`but commit -b <branch-id> -m … <ids>`). No attribution footers. Never commit `packages/conformance/golden/host/db/pstack.db-{shm,wal}`, `.dns_token`, `hetzner*`, `id_hou_dev`.
- `export GOROOT=/usr/lib/go-1.23` before Go commands. `bun run check` green at the end of every task.
- Every test starts with a `// negative control:` comment naming the change that would make it fail.
- Header name exactly `X-Pstack-Forwarded-By`. Retry: 3 attempts in total, waits 2 s then 5 s.
- Token: never returned by any read, never in events, logs or errors; from env vars only (`PSTACK_FORWARD_TOKEN`, `PSTACK_FROM_TOKEN`, `PSTACK_TO_TOKEN`), never flags.
- Tiers: `GET /api/forward` maintainer; `PUT`/`DELETE` admin. Forward token on B: not root, role ≥ developer.
- Copy is terse (docs/ui-rules.md). Events append to the END of `events.Names` only.
- Prefix matches (`strings.HasPrefix`) for the proxy, never a new `regexp.MustCompile(`^/api/…$`)` (openapi_coverage_test scans those).

## Review Focus

1. **A share-link principal** reaching the deploy proxy would read B with B's token. Expected: shares are served locally, never forwarded (Task 3 test).
2. **`migrate` against a forwarding A** would read B's list through A. Expected: every source call carries `X-Pstack-Forwarded-By: migrate` and reads A itself (Task 6 test).
3. **A request B received is never retried** (a `POST …/up` that timed out after reaching B must not start a second job). Expected: only dial failures retry (Task 3 test with a server that accepts then hangs).
4. **Control hostnames** (`control.`, `api.`, `loki.`, `grafana.`) must never be traffic-forwarded. Expected: served as today (Task 4 test).
5. **A named-spec deployment** (`specName`) must stay named on B. Expected: `PUT {specName, vars}` (Task 6 test).

## Plan rulings (controller, 2026-11-03)

- **P-R1 — the forward row is NOT in `pull config`'s export.** Loki's per-host row is the precedent (config.go:14-15), and an export applied to B would make B forward to B. The spec's "pull config carries it" line is amended in Task 8. Cost if wrong: re-entering one token after a host rebuild.
- **P-R2 — only TCP dial / DNS failures retry.** A TLS handshake failure is a certificate mismatch; retrying cannot fix it. Narrower than the spec's list. Cost if wrong: one failed CI call during a TLS blip.
- **P-R3 — `lastOkAt` is written at most once a minute** (in-memory last write), so visitor traffic is not one SQLite write per request. Cost if wrong: the Control line is up to a minute stale.
- **P-R4 — `migrate` acts on a deployment only when it copied it or the target's state differs from the source's,** so a second run does nothing. Cost if wrong: none found.
- **P-R5 — `migrate` keeps named specs named** (`PUT {specName, vars}`); `push config` already copied named specs. Cost if wrong: a 400 naming the missing spec, reported as a failure.
- **P-R6 — `--to` is shared with `upgrade`;** `migrate` requires `p.Typed["--to"]` so the `latest` default never reads as a URL.
- **P-R7 — the basic embedded UI and `packages/client` get nothing** (AGENTS.md:560: only if a consumer wants it).

---

### Task 1: The `forward_config` table

**Files:**
- Modify: `packages/pstack/internal/store/migrations.go` (append entry 11)
- Create: `packages/pstack/internal/store/forward.go`
- Test: `packages/pstack/internal/store/store_test.go`

**Interfaces:**
- Produces: `type Forward struct { To, Via, Token string; EstablishedAt, LastOkAt int64 }`; `GetForward(q Querier) (*Forward, error)` (nil, nil when unset); `SetForward(q Querier, f Forward) error` (upsert id = 1); `DeleteForward(q Querier) error`; `TouchForwardOK(q Querier, at int64) error`.

- [ ] **Step 1: Write the failing tests** — copy the migration-10 subtest (store_test.go:214-254) as `migration 11 adds an EMPTY forward_config table and touches nothing else`, and the loki_config one-row subtest (:256-271) as `forward_config holds one row: a second id violates CHECK (id = 1)`. Add `TestForwardRoundTrip`: Get → nil; Set{To:"https://api.b", Via:"10.0.0.2", Token:"t", EstablishedAt:1, LastOkAt:1}; Get equals it; TouchForwardOK(5) → LastOkAt 5, others unchanged; Delete → nil.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/store/ -run 'Forward|migration_11'` — FAIL (no table).
- [ ] **Step 3: Implement.** Migration 11 copies migration 9's shape: `CREATE TABLE forward_config (id INTEGER PRIMARY KEY CHECK (id = 1), url TEXT NOT NULL, via TEXT NOT NULL DEFAULT '', token TEXT NOT NULL, established_at INTEGER NOT NULL, last_ok_at INTEGER NOT NULL)` with a `-- STORED, NOT HASHED — A must present it to B` comment. Accessors copy `store/customroutes.go`'s Querier style.
- [ ] **Step 4: Run** the store tests and `go test ./packages/pstack/internal/auth/` (fixture reaches `len(Migrations)`) — PASS.
- [ ] **Step 5: Commit** `feat(store): forward_config table`.

### Task 2: `GET/PUT/DELETE /api/forward` and `forward.changed`

**Files:**
- Create: `packages/pstack/internal/api/forward.go` (transport + B checks), `packages/pstack/internal/api/routes_forward.go` (handlers)
- Modify: `routes.go` (three `path == "/api/forward"` literals beside `/api/domains`, :178), `permissions.go` (two rows after the Loki block, :196), `events/events.go` (append `"forward.changed", "forward.failed"`), `events/events_test.go` (`want`), `notify/notify.go` (`Summarize` case), `notify/notify_test.go`, `packages/pstack/api/openapi.yaml` (tag `forward`, three operations, `x-cli-name: get|set|off`), `apicli/` (regenerate; `OperationCount` 97 → 100), `cli/api.go` (`groupShort["forward"]`), `permissions_test.go`, `packages/conformance/test/api-rbac.test.ts`
- Create test: `packages/pstack/internal/api/routes_forward_test.go`, `packages/conformance/test/api-forward.test.ts`

**Interfaces:**
- Consumes: Task 1's store functions.
- Produces (forward.go): `func (s *Server) forwardRow() *store.Forward` (nil on unset or read error, logged); `func forwardTransport(f *store.Forward, retry bool, base *tls.Config) *http.Transport` — dials `f.Via` (default port 443) or the URL's host; keeps the request URL's host for Host and SNI; with `retry`, a dial error is retried per Global Constraints; `Server.forwardTLS *tls.Config` — nil in production, a test CA in tests (the seam, set after `New` like `s.host`).
- Produces (routes): `forwardGet` → `{to, via, establishedAt, lastOkAt}` or `null`; `forwardPut` body `{to, via?, token}`; `forwardDelete` → `{deleted: true}`.

- [ ] **Step 1: Write the failing tests** (two `New(...)` servers per crServer, routes_customroutes_test.go:29; B served by `httptest.NewServer(http.HandlerFunc(b.handle))`; a developer PAT on B via `b.auth.CreateUser` + `CreateToken`, routes_grafana_test.go:169-186):
  - `TestForwardPutEstablishes`: PUT with B's developer PAT → 200; GET has `to`, `establishedAt > 0`, and no `token` key anywhere in the body; one `forward.changed` with `{to, via}`.
  - `TestForwardPutRefuses` table, each → 400 naming the check and GET still `null`: B unreachable (closed port); B answers 401; root token (`b.opts.Token`); viewer PAT; `to` = `https://api.<A's domain>`.
  - `TestForwardViaWins`: `to: https://api.unresolvable.invalid`, `via` = B's `127.0.0.1:port` → 200.
  - `TestForwardDelete`: DELETE → GET `null`, one `forward.changed` with data `null`.
  - permissions_test rows `{"GET","/api/forward",auth.Maintainer}`, `{"PUT",…,auth.Admin}`, `{"DELETE",…,auth.Admin}`; events_test `want` ends `,forward.changed,forward.failed`.
  - conformance `api-forward.test.ts`: GET → `null`; PUT `{to:"https://127.0.0.1:1", token:"x"}` → 400 with an `error`; copy api-domains.test.ts's harness. Add api-rbac rows (`GET` maintainer 200; `PUT` admin 400 on an empty body).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/api/ -run Forward` — FAIL.
- [ ] **Step 3: Implement.** The establishing check calls B's `GET /api/auth/me` with the token through `forwardTransport(row, false, s.forwardTLS)`; accept only `root:false` and `auth.Role(user.role).Rank() >= auth.Developer.Rank()`. Own URL: `s.routing.IsControlHostname(u.Hostname(), s.opts.Domain)`. Save with `establishedAt = lastOkAt = now`. Emit outside any lock. Permissions comment: admin for blast radius, like `/api/tls/wildcard`.
- [ ] **Step 4: Regenerate and run:** `go generate ./packages/pstack/internal/apicli`; `go test ./packages/pstack/...`; `cd packages/conformance && bun test test/api-forward.test.ts test/api-rbac.test.ts && bun run vacuity && bun run ratchet` — PASS, ratchet raised.
- [ ] **Step 5: Commit** `feat(api): /api/forward`.

### Task 3: The deploy-API proxy

**Files:**
- Modify: `packages/pstack/internal/api/forward.go`, `routes.go` (one call after `permit`, :51-53)
- Test: `packages/pstack/internal/api/forward_test.go`

**Interfaces:**
- Consumes: `forwardRow`, `forwardTransport`, `s.forwardTLS` (Task 2).
- Produces: `func (s *Server) forwardAPI(w http.ResponseWriter, r *http.Request, path string, who *auth.Principal) bool` — true when it answered.

- [ ] **Step 1: Write the failing tests** (A and B as in Task 2, A forwarding to B):
  - `TestForwardedUpRunsOnB`: PUT + `POST …/up` on A with A's developer PAT → B's registry has it and B's job reaches `ok`; A's registry does not have it; `GET /api/jobs/:id` on A answers B's job.
  - `TestForwardChecksAsTierFirst`: a viewer PAT on A → `POST …/up` is 403 from A and B sees no request.
  - `TestForwardNeverForwardsShares`: a share principal's GET is served by A (B sees no request).
  - `TestForwardedHeaderIsServedLocally`: a request carrying `X-Pstack-Forwarded-By` is served by A.
  - `TestForwardSwapsCredentials`: the request B receives carries B's token and no `Cookie`; the response A returns carries no B token.
  - `TestForwardRetriesOnlyDials`: B's port closed → 3 dial attempts, then 502 `forward: <host> unreachable after 3 attempts` and one `forward.failed`; a B that accepts and never answers → one attempt only. Make the waits a package var (`forwardWaits = []time.Duration{2 * time.Second, 5 * time.Second}`) the test shortens.
  - `TestForwardLoop`: A→B and B→A both set, one PUT on A → registered on B only, no hang.
  - `TestForwardTouchesLastOk`: after a forwarded call, GET `/api/forward` `lastOkAt` advanced; two calls inside a minute write once (P-R3).
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/api/ -run 'Forward(ed|Checks|Never|Swaps|Retries|Loop|Touches)'` — FAIL.
- [ ] **Step 3: Implement.** Match `/api/deployments`, `/api/deployments/…`, `/api/jobs`, `/api/jobs/…` by prefix; skip when `who.Kind == auth.KindShare` or the header is present or the row is nil. `httputil.ReverseProxy` with `Rewrite`: target `to`, `Out.Host` = target host, `Authorization: Bearer <token>`, delete `Cookie`, set `X-Pstack-Forwarded-By: <s.opts.Domain>`; `ErrorHandler` → 502 JSON `{error}` + `forward.failed {to, error, attempts}`; `ModifyResponse` → touch `lastOkAt` (P-R3).
- [ ] **Step 4: Run** the tests and `go test -race ./packages/pstack/internal/api/` — PASS.
- [ ] **Step 5: Commit** `feat(api): forward the deploy API`.

### Task 4: Traffic forwarding

**Files:**
- Modify: `packages/pstack/internal/api/forward.go`, `server.go` (one block between :1085 and :1087)
- Test: `packages/pstack/internal/api/forward_traffic_test.go`

**Interfaces:**
- Consumes: Task 2's transport (`retry=false`).
- Produces: `func (s *Server) forwardTraffic(w http.ResponseWriter, r *http.Request, f *store.Forward)`.

- [ ] **Step 1: Write the failing tests.** B is `httptest.NewUnstartedServer` with `TLS` from `packages/conformance/harness/tls-wildcard.{crt,key}.pem` (read its SANs first; the preview domain must be one it covers), handler echoing Host; `a.forwardTLS` trusts that cert; A's `Domain` is that domain.
  - `TestTrafficReachesB`: Host `web-pr-1.<d>` on A → B's body, with Host `web-pr-1.<d>`.
  - `TestTrafficForwardsInsteadOfWaking`: a sleep-index entry for that host on A → B answers and A starts no wake job.
  - `TestTrafficSkips` table → A's own behaviour, B sees nothing: `control.<d>`, `api.<d>`, `grafana.<d>`, `loki.<d>`; a request with `X-Pstack-Forwarded-By`; a two-label host; forwarding off (byte-for-byte today's Down page).
  - `TestTrafficBadCertIsTheDownPage`: B with a cert not covering `<d>` → 503 Down page with `retry-after: 5`, exactly one dial (no retry).
  - `TestTrafficWebSocketUpgrade`: an `Upgrade: websocket` handshake reaches B and the 101 comes back.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/api/ -run Traffic` — FAIL.
- [ ] **Step 3: Implement.** In `handle()`, before the wake block: `if h := strings.ToLower(portRe.ReplaceAllString(r.Host, "")); s.previewHostname(h) && r.Header.Get("X-Pstack-Forwarded-By") == "" { if f := s.forwardRow(); f != nil { s.forwardTraffic(w, r, f); return } }` — the row is read only after `previewHostname` is true. Proxy target `https://<h>`, keep the request's Host, add the header; `ErrorHandler` writes the Down page exactly as :1101-1105 and logs one line; `ModifyResponse` touches `lastOkAt`.
- [ ] **Step 4: Run** the tests and the existing wake tests (`go test ./packages/pstack/internal/api/ -run 'Wake|Traffic'`) — PASS.
- [ ] **Step 5: Commit** `feat(api): forward unserved preview traffic`.

### Task 5: `pstack forward`

**Files:**
- Create: `packages/pstack/internal/cli/forward.go`, `packages/pstack/internal/cli/forward_test.go`
- Modify: `cli/args.go` (`Commands` += `forward`, `migrate`; Usage line + blocks; `--via`, `--from`, `--sleep-source` cases; `Typed["--to"]` in the `--to` case), `cli/run.go` (switch cases; `apiCall` gains a trailing `hdr http.Header` parameter, nil at existing call sites), `cli/commands.go` (help entries), `cli/completion.go` (`"forward": {"set", "off"}`), `packages/conformance/golden/cli/{help,help-h,no-args,unknown-command}.json` (regenerated)

**Interfaces:**
- Consumes: `/api/forward` (Task 2); `apiBaseAnyRole`, `apiCall`, `apiError`, `fail`.
- Produces: `func forwardCmd(args *Parsed, ios IO) *Exit`; `apiCall(method, endpoint, token string, body []byte, hdr http.Header) (int, []byte, error)`; `Parsed.From`, `Parsed.Via string`, `Parsed.SleepSource bool`. Task 6 stubs `migrateCmd` until it lands — this task adds `case "migrate": return migrateCmd(args, io)` with `migrateCmd` returning `fail("usage: pstack migrate …")`.

- [ ] **Step 1: Write the failing tests** (routeAPI pattern, route_test.go:18-56):
  - `TestForwardShow`: GET answer `null` → stdout `Not forwarding.`; a row → `Forwarding to <to> · last ok <ago>`.
  - `TestForwardSetSendsTheEnvToken`: `forward set https://api.b --via 10.0.0.2` with `PSTACK_FORWARD_TOKEN=ft` → one PUT, body `{"to":"https://api.b","via":"10.0.0.2","token":"ft"}`; `ft` absent from stdout and stderr.
  - `TestForwardUsageExits3BeforeAnyRequest`: `forward set` without a URL; `set` without `PSTACK_FORWARD_TOKEN`; `forward bogus` → exit 3, zero calls.
  - `TestForwardOff`: one DELETE.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/cli/ -run Forward` — FAIL.
- [ ] **Step 3: Implement** modelled on `route.go`; help body lists `PSTACK_API_URL`, `PSTACK_TOKEN`, `PSTACK_FORWARD_TOKEN`.
- [ ] **Step 4: Regenerate goldens and run:** `go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack && (cd packages/conformance && bun gen/goldens.ts && bun test test/cli-goldens.test.ts)`; `go test ./packages/pstack/internal/cli/` — PASS; `git diff --stat packages/conformance/golden` shows only the four goldens.
- [ ] **Step 5: Commit** `feat(cli): pstack forward`.

### Task 6: `pstack migrate`

**Files:**
- Create: `packages/pstack/internal/cli/migrate.go`, `packages/pstack/internal/cli/migrate_test.go`
- Modify: `cli/commands.go` (migrate help: `--from --to --sleep-source --help`; env `PSTACK_FROM_TOKEN`, `PSTACK_TO_TOKEN`)

**Interfaces:**
- Consumes: Task 5's `apiCall(…, hdr)` and `Parsed` fields; existing routes `GET /api/deployments` (rows: `id, updatedAt, specName?, vars, asleep, running`), `GET /api/deployments/:id/source` (`{spec, compose, specName}`), `PUT /api/deployments/:id`, `POST …/up|sleep` → 202 `{job}`, `GET /api/jobs/:id` → `{job}` (terminal = not `queued`/`running`).
- Produces: `func migrateCmd(args *Parsed, ios IO) *Exit`.

- [ ] **Step 1: Write the failing tests** (two routeAPI fakes; `env` maps `PSTACK_FROM_TOKEN`, `PSTACK_TO_TOKEN`; job GETs answer `ok` at once):
  - `TestMigrateCarriesState`: source rows running / asleep / down, target empty → target gets 3 PUTs (`{spec, compose, vars}` from source + list row); running → one `up`; asleep → `up` then `sleep`; down → none. Exit 0, three lines.
  - `TestMigrateKeepsNamedSpecs`: source row with `specName` → target PUT body `{specName, vars}` (P-R5).
  - `TestMigrateNewestWins`: target `updatedAt` newer → no PUT; older → PUT and `up`.
  - `TestMigrateSecondRunDoesNothing`: target rows equal-or-newer and in the source's state → zero writes (P-R4).
  - `TestMigrateReadsTheSourceItself`: every source request carries `X-Pstack-Forwarded-By: migrate`; no target request does.
  - `TestMigrateSleepSource`: `--sleep-source` → after the target's `up` is ok, `POST …/sleep` on the source with the header; a 409 (shared) is listed and exits 1; never a `down` anywhere.
  - `TestMigrateFailedUpExits1`: target job `failed` → line says failed, exit 1.
  - `TestMigrateUsage`: no `--from`; no typed `--to` (P-R6); missing either token → exit 3, zero calls.
  - `TestMigrateOnlyTheIdsGiven`: `migrate … pr-2` touches pr-2 only.
- [ ] **Step 2: Run** `go test ./packages/pstack/internal/cli/ -run Migrate` — FAIL.
- [ ] **Step 3: Implement.** Resolve both hosts with `apiBaseAnyRole` over a remapped env (`PSTACK_API_URL`←flag, `PSTACK_TOKEN`←`PSTACK_FROM_TOKEN`/`PSTACK_TO_TOKEN`), rewriting the env names in error text. Read both lists; per source row decide copy (P-R4/P-R5) and the state action; submit every `up` first, then poll all jobs (1 s); for asleep rows submit `sleep` after `up` is ok; with `--sleep-source`, sleep the source copy of each `running` row whose target is ok. Print one line per id: `<id>  <state>  <result>`.
  ```go
  // ponytail: newest updatedAt wins, comparing two hosts' clocks. Fine under NTP; a per-deployment
  // revision counter if clocks ever disagree by more than a deploy's length.
  ```
- [ ] **Step 4: Run** `go test ./packages/pstack/internal/cli/` — PASS.
- [ ] **Step 5: Commit** `feat(cli): pstack migrate`.

### Task 7: The Control-page line

**Files:**
- Modify: `apps/ui/src/api/types.ts` (`ForwardStatus`), `apps/ui/src/views/ControlView.vue` (load as `loadTls`, :102-107; line after the vitals div, :314), `apps/ui-hearth/src/api-control.ts` (type + `controlApi.forward`), `apps/ui-hearth/src/views/ControlView.vue` (`poll(loadForward, 30_000)`, add to `reloadAll`; `HText tone="muted"` after the HGrid, :85)

**Interfaces:**
- Consumes: `GET /api/forward` → `{ to, via, establishedAt, lastOkAt } | null`.

- [ ] **Step 1: Implement** — `Forwarding to <to> · last ok <ago(lastOkAt)>`, `to` in `mono`, hidden when `null` or the GET fails. Copy only from docs/ui-rules.md tokens.
- [ ] **Step 2: Verify** with the `pstack-ui-verify` skill: `serve.sh start forward swarm`. `PUT /api/forward` needs a reachable B, so seed the row directly: `sqlite3 <run>/data/pstack.db "INSERT INTO forward_config VALUES (1,'https://api.b.example','','x',1,<now ms>)"`. Shots of `/control` in both UIs at 1280 and 320, light and dark; `overflowX` false, no `errors`. Stop the run.
- [ ] **Step 3: Run** `bun run check` — PASS.
- [ ] **Step 4: Commit** `feat(ui): show forwarding on the Control page`.

### Task 8: Docs

**Files:**
- Modify: `docs/usage.md` (new `## 7f. Move stacks to another host (0.46.0)` before `## 8.` :3223 — forwarding, `migrate`, the cutover recipe, the never-`down` warning; "I want to…" row :12-26; §7d "Stays behind" cross-link :2916; Commands table :3598-3613; Environment table :3667-3703; HTTP API table :3826 and the `ANY <preview hostname>/*` row :3846; role matrix :3104-3108), `docs/webhook-events.md` (`forward.changed` / `forward.failed` after `node.pruned`, :542-560, ending "Never the token."), `docs/README.md` (task row, §7f row, event count 37 → 39 at :79), `skills/pstack/SKILL.md` (a `### Moving stacks to another host` section before `## 1.` :280; `/api/forward` row :892-903), `packages/pstack/CHANGELOG.md` (`## Unreleased` → `### Added`), `docs/migrate-forward-design.md` (P-R1, P-R2, P-R3 into a "Where the build differs" banner).

- [ ] **Step 1: Write** each section from the spec; every command shown is one Tasks 5–6 implement.
- [ ] **Step 2: Check** `bun scripts/sync-docs.ts` (apps/docs) runs clean; `grep -n 'forward' docs/usage.md` hits every table above.
- [ ] **Step 3: Commit** `docs: migrate and forwarding`.

### After Task 8

`bun run check` green. Not automatable, done by hand before release: a real cutover on preview / preview-temp (spec, Testing).
