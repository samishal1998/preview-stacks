# Loki logging, slice 3 (Grafana with pstack sign-in) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hosts running `--logging loki` also run Grafana at `grafana.<domain>`, signed in with pstack accounts through Traefik forwardAuth — developers and maintainers as Editor, admins as Admin, viewers refused.

**Architecture:** Init renders Grafana beside Loki with the slice-1 anchor approach and a provisioned Loki datasource. Traefik forwardAuth calls a pre-gate pstack verify endpoint that answers the redirect sign-in, the callback (inside verify) and the verified request (`X-WEBAUTH-USER`/`X-WEBAUTH-ROLE`) from a derived host-only cookie tied to the parent pstack session.

**Tech Stack:** Go 1.23 stdlib, bun:test conformance, Vue 3 advanced UI, GitButler.

**Spec:** `docs/loki-logging-design.md`, section *Slice 3* (landed by this plan's first task from the reviewed draft).

**Branch:** `claude/loki-logging-grafana`

## Global Constraints

- Spec of record: /private/tmp/claude-501/-Volumes-S1-code-preview-stacks/7cc16894-c17e-4570-af5f-bf665845e6dd/scratchpad/slice-3-spec.md. Task T1 copies it into docs/loki-logging-design.md, replacing the section that starts at `## Slice 3 — Grafana with pstack sign-in (decisions recorded; own spec before building)`. From T2 onward the design doc's `## Slice 3` section is the spec of record. Its exact values (env, labels, cookie attributes, error strings, TTLs) win over anything paraphrased here, except for the deviations listed in contractNotes C1-C4.
- Build order: slice 3 is built after slices 1 and 2 land, on GitButler branch `claude/loki-logging-grafana`, stacked on `claude/loki-logging-settings`. Edit files by the text you are replacing. Spec line numbers into init.go, domains.go and upgrade.go are from before slice 1, so treat them as hints only. Where slice-1/2 code exists in the tree, the tree wins over both plans.
- Every Go `Test…` and `t.Run` carries a `// negative control: <mutation that fails it>` line, and that mutation was actually run and failed (AGENTS.md Go rule 17).
- Go test command: `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/<pkg>/ -run <Name>`. -race is not optional. Also run `go vet ./...` and `gofmt -l internal` on every touched package.
- Conformance: build first with `cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`, then `cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/<file>.test.ts`. Every new test must fail under `PSTACK_IMPL=null` (`bun run vacuity`), and `expected-pass.json` counts only go up (`bun run ratchet`).
- Goldens: regenerate with `cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts` only. Never run gen/host-fixture.ts. Never commit packages/conformance/golden/host/db/* or anything under .superpowers/. After regenerating, `but diff` may show only the golden files the task names.
- UI: `cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck && bun run test`. Client: `cd /Volumes/S1/code/preview-stacks/packages/client && bun run check`. Full gate before the last commit: `cd /Volumes/S1/code/preview-stacks && bun run check`.
- Version control is GitButler only. No git writes. Commit step: run `but status` for file IDs, then `but commit -b claude/loki-logging-grafana -m "<type(scope): summary>" <ids>`. No Co-Authored-By, Claude-Session or Generated-with footer. Don't push, and don't commit files outside the task.
- No new Go modules or npm dependencies. No new migration, job action, event, CLI flag, env var, CLI command or openapi.yaml path, and no apicli regeneration: both new routes go in `notInTheSpec`, and /api/health is the untyped `Ok` response (openapi.yaml:829-835).
- No new lines in packages/pstack/templates/control/docker-compose.yml. With logging off, all 8 render cells, every non-loki CLI transcript, the cloud-init and swarm-join goldens, golden/host/** and the help/no-args/unknown-command goldens stay byte-identical.
- Template substitution is literal: `strings.Replace(s, old, new, 1)` with anchors checked before replacing. Never regexp.ReplaceAllString or text/template (invariant 18, Go rule 8).
- JSON goes through structs or jsonx/omap, never map[string]any. Never range a Go map into output. `grafanaRoles` is a lookup table only (Go rules 1, 5). The health `grafana` key is genuinely absent when off (Go rule 2), never null.
- UI and CLI copy is terse: state, not explanation (the owner has corrected this 3+ times). The only new visible copy is the nav label `Grafana`, the init summary line `  grafana   https://grafana.<domain>`, and the plain-text error strings fixed in contractNotes. UI work follows docs/ui-rules.md and is looked at in a browser.
- File headers record WHY. Change the header that records a decision in the same edit: server.go's route and pre-gate list, domains.go's IsControlHostname comment, assets.go's embed count, init.go's substitution comment, and inspect/control.go's header.
- Each task folds in the docs that describe its behaviour (docs/usage.md, docs/control-plane.md, docs/bootstrap.md, templates/control/README.md) and ends green on its own package tests. Execute in id order: dependsOn also serialises edits to shared files (docs/usage.md, init.go, server.go, routes_grafana.go).

## File map

| Path | Action | Responsibility |
|---|---|---|
| `docs/loki-logging-design.md` | modify | T1: the slice-3 spec replaces the 'Slice 3 — decisions recorded' section, with deviations C1-C4 applied. T12: the top banner says slice 3 is built. |
| `packages/pstack/internal/routing/domains.go` | modify | T2: IsControlHostname also matches grafana.<d> for the primary and every added domain; its comment names grafana. beside loki. |
| `packages/pstack/internal/routing/domains_test.go` | modify | T2: TestGrafanaIsAControlHostnameOnEveryDomain |
| `packages/pstack/internal/autolabel/autolabel_test.go` | modify | T2: a grafana. subtest in slice-1's TestAPreviewCannotClaimAControlHostname (no autolabel.go change: the refusal already routes through IsControlHostname) |
| `packages/pstack/internal/auth/auth.go` | modify | T3: SessionHashUser(idHash); SessionUser becomes a one-line wrapper |
| `packages/pstack/internal/auth/auth_test.go` | modify | T3: TestSessionHashUser |
| `packages/pstack/internal/inspect/control.go` | modify | T4: GrafanaOn beside LokiPushURL; the header names the read |
| `packages/pstack/internal/inspect/control_test.go` | modify | T4: TestGrafanaOn, using slice-1's lokiHost helper |
| `packages/pstack/templates/control/grafana/datasources.yaml` | create | T5: Grafana's provisioned Loki datasource, byte-for-byte the spec block |
| `packages/pstack/assets.go` | modify | T5: embeds GrafanaDatasources by explicit path; the header count goes up by one |
| `packages/pstack/internal/initctl/init.go` | modify | T5: GrafanaVersion, GrafanaService. T6: the substitution appends GrafanaService; LokiWiring's volume edit adds `  grafana:`; loki/grafana image requires; grafana/datasources dir + loki.yaml write; summary line |
| `packages/pstack/internal/initctl/init_test.go` | modify | T5: TestGrafanaService, TestGrafanaDatasources. T6: TestLokiWiring and TestInitLoki extensions, serviceBlock helper |
| `packages/pstack/internal/upgrade/upgrade_test.go` | modify | T6: the real-init loki fixture carries Grafana and still reads Logging == loki |
| `packages/pstack/internal/api/routes_grafana.go` | create | T7: header, grafanaOn, grafanaURL. T8: cookies, MAC, verify, callback, start |
| `packages/pstack/internal/api/routes_grafana_test.go` | create | T7: TestHealthNamesGrafanaOnlyWhenOn. T8: TestGrafanaVerify, TestGrafanaStart. T9: TestServiceHostnamesAreNeverServedByPstack |
| `packages/pstack/internal/api/server.go` | modify | T7: `grafana atomic.Bool`, Start + reindexLoop refresh. T8: header route and pre-gate lists. T9: the service-hostname refusal first in handle() |
| `packages/pstack/internal/api/routes_auth.go` | modify | T7: health appends `grafana` when grafanaOn(). T8: two preGate cases |
| `packages/pstack/internal/api/openapi_coverage_test.go` | modify | T8: notInTheSpec gains both Grafana routes |
| `packages/pstack/internal/api/permissions_test.go` | modify | T8: preGatePaths gains both Grafana routes |
| `packages/client/src/types.ts` | modify | T7: Health.grafana?: string |
| `apps/ui/src/composables/useAuth.ts` | modify | T10: authState.grafana, read from /api/health in checkAuth |
| `apps/ui/src/App.vue` | modify | T10: the Grafana nav link (developer and above, hidden with a stored bearer), ScrollText import |
| `apps/ui/src/views/LoginView.vue` | modify | T10: a next starting /api/ is a full navigation |
| `packages/pstack/ui/index.html` | modify | T10: signIn and signInWithProvider honour ?next |
| `packages/conformance/test/api-grafana.test.ts` | create | T11: black-box sign-in flow, sign-out, 503 service host, off-host 404 (4 tests), with a local GRAFANA_SHIM |
| `packages/conformance/gen/goldens.table.ts` | modify | T11: the LOKI_CELLS init rows' render.files gain control/grafana/datasources/loki.yaml |
| `packages/conformance/golden/render/control/http01-basic-compose-loki/` | modify | T11: docker-compose.yml regenerated; grafana/datasources/loki.yaml new |
| `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/` | modify | T11: docker-compose.yml regenerated; grafana/datasources/loki.yaml new |
| `packages/conformance/golden/cli/` | modify | T11: init-dry-*-loki and init-*-loki, plus any other *-loki transcript whose diff is only the requires/mkdir/write/grafana lines |
| `packages/conformance/test/cli-goldens.test.ts` | modify | T11: the header names grafana/datasources/loki.yaml |
| `packages/conformance/expected-pass.json` | modify | T11: "test/api-grafana.test.ts": 4 |
| `docs/usage.md` | modify | T2: Hostnames tables. T6: §7 init rows 0 and 3. T7: health row. T8: route table, the pre-gate sentence, the 7e matrix row, the `### Grafana` section. T9 and T10: one sentence each in it |
| `docs/control-plane.md` | modify | T5: embed count. T8: new `## 5h. Grafana sign-in` before `## 6. Submitting a deployment`. T9: its service-hostname subsection |
| `docs/bootstrap.md` | modify | T6: the control-stack RAM line (:132) |
| `packages/pstack/templates/control/README.md` | modify | T6: the tree gains grafana/datasources/loki.yaml |
| `docs/README.md` | modify | T12: the loki design row says slice 3 is built |
| `packages/pstack/CHANGELOG.md` | modify | T12: the ## Unreleased entry for Grafana, its cost, the grafana.<domain> refusal and the changed loki goldens |

## Decisions the plan fixed

- C1 — No deviation. Slice 2 mounts `      - ./loki:/etc/loki` into pstack's service block in every mode (`templates/control/docker-compose.yml`, not a `LokiWiring` anchor; `LokiWiring` keeps slice 1's three), so `pstack logging loki|off` leaves pstack's block unchanged. The slice-3 spec's claims hold as written: pstack is not recreated by the switch, running jobs survive, the `pstack:` service block is byte-identical with logging off and on, and real-host check 9's `StartedAt` is unchanged. T6's render test pins the block.
- C2 — Nav link visibility. Spec line 719 ('every signed-in role sees it') contradicts spec 457 and the owner decision of 2026-09-15. The link is `v-if="authState.grafana && !settings.token && can('developer')"`.
- C3 — App.vue already imports `settings` (App.vue:18) and `can` (:19). `ScrollText` is NOT imported, even though spec 715 says 'any lucide icon already imported' and then uses ScrollText. Add `ScrollText` to the lucide-vue-next named import list (the icon exists: node_modules/lucide-vue-next/dist/esm/icons/scroll-text.js).
- C4 — Every plain-text refusal goes through `http.Error`. That sets `content-type: text/plain; charset=utf-8` and `x-content-type-options: nosniff`, and appends `\n` to the body. Exact bodies: `Not found.\n` (404), `Sign-in link is invalid.\n` (400), `Sign-in failed.\n` (500), `No Grafana access.\n` (403), `Cross-origin request refused.\n` (403), `Grafana is not running.\n` (503), `Loki is not running.\n` (503). The no-cookie non-navigation answer is `w.WriteHeader(401)` with an empty body. The log line is `[grafana] sign-in code not stored: <err>`.
- C5 — Names fixed where the spec left them open. `initctl.GrafanaVersion = "13.2.1"`. `initctl.GrafanaService(logging Logging, challenge Challenge) string`. `pstack.GrafanaDatasources` embeds `templates/control/grafana/datasources.yaml`, written to `<DATA>/control/grafana/datasources/loki.yaml` at 0644. `inspect.GrafanaOn(r exec.Runner) bool`. `auth.(*Auth).SessionHashUser(idHash string) (*UserRow, error)`. The api file is `routes_grafana.go`, with tests in `routes_grafana_test.go` (package api) and the helper `grafanaServer(t *testing.T) *Server`. The Go test names are TestGrafanaService, TestGrafanaDatasources, TestGrafanaOn, TestSessionHashUser, TestGrafanaIsAControlHostnameOnEveryDomain, TestHealthNamesGrafanaOnlyWhenOn, TestGrafanaVerify, TestGrafanaStart and TestServiceHostnamesAreNeverServedByPstack. The conformance file is `test/api-grafana.test.ts` with 4 tests.
- C6 — Wire and cookie contract. Routes: `GET /api/auth/grafana/verify` and `GET /api/auth/grafana/start`, both pre-gate, both 404 unless grafanaOn() (Domain, Token and the cached docker flag). The callback path `/-/pstack/callback` is answered inside verify. Cookies: `__Host-pstack_grafana=<id_hash>.<b64url HMAC-SHA256(PSTACK_TOKEN, "pstack-grafana\n"+id_hash)>` with `Path=/; Secure; HttpOnly; SameSite=Lax` and no Max-Age. `__Host-pstack_grafana_state=<43-char b64url>` has the same attributes plus `Max-Age=900`, and is cleared with `Max-Age=0`. The transient key is `grafana:<K>`, TTL 60 s, value JSON `{"session":<id_hash>,"state":<S>,"next":<SafeNext'd path>}`. The verify 204 carries `X-WEBAUTH-USER: <username>` and `X-WEBAUTH-ROLE: Editor|Admin`. Role map: developer/maintainer → Editor, admin → Admin, anything else → 403. The Origin rule: when X-Forwarded-Method is not GET/HEAD (absent counts), Origin must equal `https://grafana.<domain>`. Every Location is absolute from config (`baseURL(s.opts.Domain, r)` = https://control.<domain>, and `grafanaURL()`), except start's signed-out `/login?next=…`, which is relative on control.<domain>. The health key `grafana` is appended last, only when on.
- C7 — GRAFANA_SHIM stays local to api-grafana.test.ts. R15's one-copy rule is about fixtures shared by several files, and this one has a single user. Its `ps -aq` arm uses the same pattern as LOKI_SHIM's, so the two cannot be concatenated into one shim. The container id is `gr4f`.
- C8 — Go api tests must not use `ssoTestServer` (http_test.go:148-158). It builds `&Server{}` with nil routing and sleepIndex, and handle() dereferences both (server.go:870, and T9's refusal). Use `New(Options{DataDir: t.TempDir(), Domain: "preview.example.com", Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})`, then `s.grafana.Store(true)`, and drive `s.handle`. Never call Start in unit tests: it runs the synchronous docker probe.
- C9 — Nothing new in: migrations (no table), jobs (no action), events (no name, so webhook-events.md is untouched), openapi.yaml/apicli (both routes in `notInTheSpec`; /api/health is the untyped `Ok` response at openapi.yaml:829-835), CLI flags/env/commands/initguard/cloud-init (so the help, no-args, unknown-command and cloud-init goldens stay), and golden/host (host-fixture.ts:38's control `ps -aq` arm lists only traefik, so health has no grafana key). The cli-goldens CASES count is unchanged; only render.files of the LOKI_CELLS init rows grows.
- C10 — Service-host refusal scope. It is keyed on `r.Host` (never requestHost, because forwardAuth calls carry Host pstack:7878) and runs before wake-on-call. grafana.<primary> → 503, loki.<primary> → 503, and grafana./loki. of an added domain → 404. It runs whether logging is on or off, because IsControlHostname is unconditional (T2).
- C11 — Golden transcript expectations. Non-dry init transcripts print no step lines (golden/cli/init-http01-basic-compose.json), so init-*-loki gains only `  grafana   https://grafana.preview.example.com`. init-dry-*-loki gains `  [dry-run] requires loki image`, `  [dry-run] requires grafana image`, `  [dry-run] mkdir -p <DATA>/control/grafana/datasources`, `  [dry-run] write <DATA>/control/grafana/datasources/loki.yaml (<n> bytes, mode 644)`, the changed compose byte count, and the grafana line. upgrade-plan-* prints no init dry run (golden/cli/upgrade-plan-http01-basic-compose.json), so upgrade-plan-*-loki should not change. Accept whatever logging-*-dry transcripts show only if their hunks are those lines.
- C12 — Spec line references into init.go (251-259, 370, 391, 760-770), domains.go (249-259) and upgrade.go are pre-slice-1. The real anchors are: reqs block init.go:264-299 (append after `if ui == Advanced {…}` at :290-298); loki ensureDir :327-331; substitution :433; LokiWiring call :434; loki config write :458-463; summary :509-511; LokiWiring entries :895-899. Edit by quoted text; slice 2 may shift these lines.
- C13 — Conformance cookie parsing. Bun's `headers.get('set-cookie')` joins several Set-Cookie headers with `, `. Extract with regexes that include the `=`, e.g. `/__Host-pstack_grafana=([^;,]*)/` and `/__Host-pstack_grafana_state=([^;,]*)/`, so the state cookie never matches the session pattern. Cookie values are base64url or hex with dots and contain no commas.
- C14 — Out of scope, and not planned: Grafana on added domains; the Grafana HTTP API for scripts; Grafana for bearer-token SPA sessions; Live/tail; provisioned dashboards/alerting; a Grafana server admin; a Grafana-only sign-out; deep links from the logs tab; a basic-UI nav link; pattern_ingester (the Loki config stays byte-for-byte slice 1's, and slice 2's render keeps it); offline Drilldown; redacting lines in Grafana; multiple orgs.
- C15 — Commit placement. Every task commits on `claude/loki-logging-grafana` with `but commit -b claude/loki-logging-grafana -m "<type(scope): summary>" <ids>` after `but status`. Suggested messages are in each task's acceptance. A later fix to an unpublished task commit is amended into it with GitButler, never a fixup commit.

## Order

Execute in task order.

| Task | Title | Depends on |
|---|---|---|
| 1 | docs(design): the slice-3 spec in the design doc | — |
| 2 | routing: grafana. is a control hostname; previews cannot claim it | 1 |
| 3 | auth: SessionHashUser — resolve a stored session hash | 1 |
| 4 | inspect: GrafanaOn — does the control project run a grafana container | 1 |
| 5 | initctl: render Grafana (pure functions + embedded datasource) | 1 |
| 6 | initctl: init --logging loki brings Grafana up | 5, 2 |
| 7 | api: Grafana discovery cache and the health key | 4, 6 |
| 8 | api: Grafana sign-in — forwardAuth verify, start, callback | 3, 7 |
| 9 | api: a service hostname is never served by pstack | 2, 8 |
| 10 | ui: the Grafana link and the login return to /api/ | 7, 8 |
| 11 | conformance: Grafana sign-in over HTTP; loki goldens regenerated | 6, 7, 8, 9 |
| 12 | CHANGELOG, doc status, full gate, real-host checklist | 10, 11 |

---

### Task 1: docs(design): the slice-3 spec in the design doc

**Files:**
- Modify: `docs/loki-logging-design.md`. Replace the section from its heading `## Slice 3 — Grafana with pstack sign-in (decisions recorded; own spec before building)` (today :381) up to, not including, the `---` line before `## Facts this design stands on` (today :414). Anchor on that heading and that `---`. Line numbers are only hints: slice 1's T14 and slice 2 edit this file first.
- Test: none in the repo. The check is a shell script in `$S`, outside the repo (Step 1).

**Interfaces:**
- Consumes: nothing from earlier tasks. The body is the slice-3 spec (scratchpad `slice-3-spec.md`, 997 lines) with contract notes C1-C4 applied. It is embedded in full in Step 3 because the scratchpad will not exist when this runs.
- Produces: the design doc section `## Slice 3 — Grafana at `grafana.<domain>`, signed in with pstack accounts`. From T2 onward it is the spec of record. It keeps every spec subsection, in order: Turning it on; Control stack (the `GrafanaService` block, the datasource file); How pstack knows Grafana is on; Sign-in (the flow, the cookie, roles, sign-out, the code, edits outside the new file); Hostnames and the wake handler; The UI entry point; Failure modes; Security; Testing; Where the change lands; Out of scope. The top banner is untouched; T12 changes it.

What the body changes from the scratchpad spec (every edit is already applied in Step 3's block):

| Spec line | Why | Now says |
|---|---|---|
| :3-9 | slice 2 lands first | the preamble names slice 2's unconditional `./loki` mount and its create-if-absent config write |
| :14, :15 | the text now lives in the design doc | `(§Decisions, "Reading logs")`, `(Slice 1 › Control stack)` |
| :241 | T4's nil guard | `if raw.Config != nil && raw.Config.Labels["com.docker.compose.service"] == "grafana" {` |
| :257 | `authState` reads health only in `checkAuth` | "A switch shows up within 30 s, with no pstack restart: the health key on the next tick, the nav link on the next page load." |
| :276-278 | this edit deletes the stale "host-only on `api.<domain>`" line it quoted | "It is never widened: previews on `*.<domain>` would receive it." |
| after :289 | C4 | `http.Error`: `text/plain; charset=utf-8`, `nosniff`, body plus `\n`; verify's no-cookie 401 is empty |
| :713-714 | C3 | `settings` and `can` already imported (`App.vue:18-19`); `ScrollText` added to the lucide import (`:23-40`) |
| :719, :722 | C2 | "Developer and above only"; `v-if="authState.grafana && !settings.token && can('developer')"` |
| :746 | `checkAuth` | the failure row: the health key within 30 s, the nav link on the next page load |
| :752 | the old section's "Known costs" goes away | "Known cost; an offline preinstall is out of scope." |
| :774 | `checkAuth` | `pstack logging off`: "the health key goes within 30 s; the link on the next page load" |
| :781 | T5's subtest name | `TestGrafanaService/Traefik_strips_every_header_Grafana_reads` |
| :794 | C4 | Go tests compare plain-text bodies exactly, `\n` included |
| :916 | C4 | conformance step 7 compares `Grafana is not running.\n` |
| :959-961 | `checkAuth` | real-host check 9: the health key within 30 s, the Grafana link after a page reload |
| :980 | the api line is gone; the banner is T12's | "this design's banner." |

- [ ] **Step 1: Write the failing check**

  Pick the scratch directory, outside the repo:

  ```bash
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  ```

  Save this as `"$S/t1-check.sh"`. Every check carries its negative control.

```bash
#!/usr/bin/env bash
# T1 check: the design doc's Slice 3 section is the slice-3 spec, with C1-C4 applied.
# DOC overrides the doc path. PLAN (the plan file holding T1's body) adds the byte-for-byte check.
set -u
doc=${DOC:-/Volumes/S1/code/preview-stacks/docs/loki-logging-design.md}
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1 (got '$2', want '$3')"; fail=1; fi; }
block() { awk -v m="<!-- $1 -->" 'g && $0 == "````" { exit } g { print } f && $0 == "````markdown" { g = 1 } $0 == m { f = 1 }' "$PLAN"; }
# The section: its heading up to, not including, the first `---` after it.
sec=$(awk 'index($0, "## Slice 3 — ") == 1 { f = 1 } f && $0 == "---" { exit } f' "$doc")
n() { printf '%s\n' "$sec" | grep -cF -- "$1"; }

# negative control: skip Step 3's splice, so the old heading stays.
check 'old slice-3 heading gone' "$(grep -cxF '## Slice 3 — Grafana with pstack sign-in (decisions recorded; own spec before building)' "$doc")" 0
# negative control: splice the body without its first line.
check 'new slice-3 heading, once' "$(grep -cxF '## Slice 3 — Grafana at `grafana.<domain>`, signed in with pstack accounts' "$doc")" 1
# negative control: delete the `---` line before `## Facts this design stands on`.
check 'Facts follows the section' "$(awk 'index($0, "## Slice 3 — Grafana at ") == 1 { f = 1 } f && $0 == "---" { g = 1; next } g && NF { print; exit }' "$doc")" '## Facts this design stands on'
# negative control: delete the `### Testing` heading from the body.
check 'every spec section, in order' "$(printf '%s\n' "$sec" | grep -E '^#{3,4} ' | tr '\n' '|')" '### Turning it on|### Control stack|#### The service (`GrafanaService`), rendered only when logging is loki|#### The datasource file|### How pstack knows Grafana is on|### Sign-in|#### The flow|#### The Grafana cookie: derived, no table|#### Roles|#### Sign-out|#### The code|### Hostnames and the wake handler|### The UI entry point|### Failure modes|### Security — where each requirement lands|### Testing|### Where the change lands|### Out of scope for slice 3|'

# negative control: keep "Otherwise every signed-in role sees it" and the old v-if.
check "C2: 'every signed-in role sees it' gone" "$(grep -cF 'every signed-in role sees it' "$doc")" 0
check "C2: the link's v-if" "$(n "<a v-if=\"authState.grafana && !settings.token && can('developer')\"")" 1
# negative control: keep "The icon is any lucide icon already imported."
check 'C3: no "already imported" icon' "$(n 'any lucide icon already imported')" 0
check 'C3: ScrollText joins the import' "$(n '`ScrollText` is added to the `lucide-vue-next` import list')" 1
# negative control: drop the http.Error paragraph after "the callback is a branch of verify."
check 'C4: http.Error appends \n' "$(n 'and the body with `\n` appended (`Not found.\n`)')" 1
check 'C4: conformance compares the \n' "$(n 'body `Grafana is not running.\n`')" 1
# negative control: leave spec :277 ("The design doc's … api.<domain>") or :752 ("Slice 3 › Known costs") in.
check 'stale roles line gone' "$(grep -cF 'viewer → Viewer' "$doc")" 0
check 'stale api.<domain> session line gone' "$(grep -cF 'host-only on `api.<domain>`' "$doc")" 0
check 'no self-reference to a section that is gone' "$(n 'Known costs'),$(n 'design doc'),$(n 'design,'),$(n 'design §')" 0,0,0,0

if [ -n "${PLAN:-}" ]; then
  # negative control: hand-edit one word of the spliced section.
  check 'section = the plan body, byte for byte' "$(diff <(printf '%s\n' "$sec") <(printf '%s\n' "$(block 'T1 section body')") >/dev/null && echo same)" same
fi
exit $fail
```

- [ ] **Step 2: Run it and watch it fail**

  ```bash
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  bash "$S/t1-check.sh"; echo "exit $?"
  ```

  Expected: exit 1. The FAIL lines below are what the tree before slice 1's T14 prints. Slices 1 and 2 may shift individual `got` values, but these checks must still FAIL:

  ```text
  FAIL old slice-3 heading gone (got '1', want '0')
  FAIL new slice-3 heading, once (got '0', want '1')
  FAIL Facts follows the section (got '', want '## Facts this design stands on')
  FAIL every spec section, in order (got '', want '### Turning it on|### Control stack|#### The service (`GrafanaService`), rendered only when logging is loki|#### The datasource file|### How pstack knows Grafana is on|### Sign-in|#### The flow|#### The Grafana cookie: derived, no table|#### Roles|#### Sign-out|#### The code|### Hostnames and the wake handler|### The UI entry point|### Failure modes|### Security — where each requirement lands|### Testing|### Where the change lands|### Out of scope for slice 3|')
  ok   C2: 'every signed-in role sees it' gone
  FAIL C2: the link's v-if (got '0', want '1')
  ok   C3: no "already imported" icon
  FAIL C3: ScrollText joins the import (got '0', want '1')
  FAIL C4: http.Error appends \n (got '0', want '1')
  FAIL C4: conformance compares the \n (got '0', want '1')
  FAIL stale roles line gone (got '1', want '0')
  FAIL stale api.<domain> session line gone (got '1', want '0')
  FAIL no self-reference to a section that is gone (got '1,0,0,0', want '0,0,0,0')
  ```

- [ ] **Step 3: Implement — replace the section**

  **3a. The text being replaced.** Everything from the heading down to the blank line before `---`:

<!-- T1 replaced section -->
````markdown
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
````

  **3b. The replacement.** It is 1006 lines. The last line is `- Multiple Grafana orgs.`, then the blank line that already comes before `---`:

<!-- T1 section body -->
````markdown
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
````

  **3c. Take both blocks out of this plan, check the anchors, then splice.** Take them by their `<!-- … -->` markers; don't retype 1006 lines. `PLAN` is the path of this plan file.

  ```bash
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  PLAN=/Volumes/S1/code/preview-stacks/docs/loki-logging-slice-3-plan.md   # this plan file; change it if the plan lives elsewhere
  block() { awk -v m="<!-- $1 -->" 'g && $0 == "````" { exit } g { print } f && $0 == "````markdown" { g = 1 } $0 == m { f = 1 }' "$PLAN"; }
  block 'T1 replaced section' > "$S/t1-old.md"
  block 'T1 section body' > "$S/t1-body.md"
  wc -l "$S/t1-old.md" "$S/t1-body.md"
  ```

  Expected: `32` and `1006`.

  **Fallback.** If `t1-body.md` is empty, the plan was assembled with indented fences, so the markers no longer match. Write 3a's fenced text to `"$S/t1-old.md"` and 3b's to `"$S/t1-body.md"` by hand: the lines between the fence lines, with the assembly's indent removed. Then run the splice below. In Step 4, run the check without `PLAN`. That skips only the byte-for-byte check; the other 13 checks still gate the commit.

  ```bash
  cd /Volumes/S1/code/preview-stacks
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  doc=docs/loki-logging-design.md
  old='## Slice 3 — Grafana with pstack sign-in (decisions recorded; own spec before building)'
  norm() { printf '%s\n' "$(cat "$1")"; }   # one trailing newline, so a hand-written copy compares the same

  # Preconditions. Stop and report on any failure; never splice over text you have not seen.
  [ -s "$S/t1-old.md" ] && [ -s "$S/t1-body.md" ] || { echo "a block file is empty: use the fallback above"; exit 1; }
  [ "$(grep -cxF "$old" "$doc")" = 1 ] || { echo "old heading not found exactly once"; exit 1; }
  [ "$(awk -v h="$old" '$0 == h { f = 1 } f && $0 == "---" { g = 1; next } g && NF { print; exit }' "$doc")" = '## Facts this design stands on' ] || { echo "no --- then ## Facts after the old section"; exit 1; }
  diff <(printf '%s\n' "$(awk -v h="$old" '$0 == h { f = 1 } f && $0 == "---" { exit } f' "$doc")") <(norm "$S/t1-old.md") && echo 'old section is as quoted in 3a' || { echo "old section differs from 3a: an earlier slice edited it, so stop and report"; exit 1; }
  norm "$S/t1-body.md" > "$S/t1-body.n" && mv "$S/t1-body.n" "$S/t1-body.md"
  [ "$(wc -l < "$S/t1-body.md" | tr -d ' ')" = 1006 ] || { echo "t1-body.md is not 1006 lines: not the body this task was written against"; exit 1; }

  # Splice: the body, one blank line, then the untouched `---` and everything after it.
  awk -v h="$old" -v body="$S/t1-body.md" '
    $0 == h { while ((getline l < body) > 0) print l; print ""; skip = 1; next }
    skip && $0 == "---" { skip = 0 }
    !skip
  ' "$doc" > "$S/t1-design.md"
  cat "$S/t1-design.md" > "$doc"
  ```

  `cat >` rather than `mv` keeps the file's inode and mode.

- [ ] **Step 4: Run it and watch it pass**

  ```bash
  cd /Volumes/S1/code/preview-stacks
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  PLAN=/Volumes/S1/code/preview-stacks/docs/loki-logging-slice-3-plan.md bash "$S/t1-check.sh"; echo "exit $?"
  grep -n 'decisions recorded; own spec before building' docs/loki-logging-design.md
  grep -c 'every signed-in role sees it' docs/loki-logging-design.md
  grep -c "can('developer')" docs/loki-logging-design.md
  ```

  Expected: every line `ok`, then `exit 0`:

  ```text
  ok   old slice-3 heading gone
  ok   new slice-3 heading, once
  ok   Facts follows the section
  ok   every spec section, in order
  ok   C2: 'every signed-in role sees it' gone
  ok   C2: the link's v-if
  ok   C3: no "already imported" icon
  ok   C3: ScrollText joins the import
  ok   C4: http.Error appends \n
  ok   C4: conformance compares the \n
  ok   stale roles line gone
  ok   stale api.<domain> session line gone
  ok   no self-reference to a section that is gone
  ok   section = the plan body, byte for byte
  ```

  After 3c's fallback, run `bash "$S/t1-check.sh"; echo "exit $?"` without `PLAN`: the last line is absent, and the other 13 are `ok` with `exit 0`.

  The first grep prints at most the `## Slice 2 — … (decisions recorded; own spec before building)` heading, and only if slice 2 left it there. It never prints slice 3's. The second prints `0` (and exits 1). The third prints `2` (the UI paragraph and the `v-if`).

  Negative controls, run on a copy so the doc is untouched. Each run must exit 1 with the named FAIL:

  ```bash
  cd /Volumes/S1/code/preview-stacks
  S=${S:-${TMPDIR:-/tmp}/t1}; mkdir -p "$S"
  grep -vxF '### Testing' docs/loki-logging-design.md > "$S/t1-mut1.md"
  DOC="$S/t1-mut1.md" bash "$S/t1-check.sh" | grep FAIL      # FAIL every spec section, in order
  sed "s/ \&\& can('developer')\" :href/\" :href/" docs/loki-logging-design.md > "$S/t1-mut2.md"
  DOC="$S/t1-mut2.md" bash "$S/t1-check.sh" | grep FAIL      # FAIL C2: the link's v-if
  ```

  `but diff` shows one changed file, `docs/loki-logging-design.md`. Its hunks sit only between the new slice-3 heading and the `---` before `## Facts this design stands on`.

- [ ] **Step 5: Commit**

  T1 is the first commit on the slice-3 branch, so create the branch stacked on slice 2 first. Skip `branch new` if `but status` already lists `claude/loki-logging-grafana` above `claude/loki-logging-settings`.

  ```bash
  but branch new claude/loki-logging-grafana --anchor claude/loki-logging-settings
  but status
  but commit -b claude/loki-logging-grafana -m "docs(design): Loki logging slice 3, Grafana sign-in" <id of docs/loki-logging-design.md>
  ```

  Commit that one file ID only. Never commit `packages/conformance/golden/host/db/*`, anything under `.superpowers/`, or the files in `$S`. No footer.

---

### Task 2: routing: grafana. is a control hostname; previews cannot claim it

**Files:**
- Modify: `packages/pstack/internal/routing/domains.go`: `IsControlHostname` and its doc comment, at :244-260 in today's tree. Slice-1 T12 (plan:6297-6320) rewrites this block first, so every "Existing" quote below is slice-1 T12's replacement text. If T12 landed with different wording, edit what the tree actually has.
- Modify: `packages/pstack/internal/autolabel/autolabel.go`: three copy edits that add `grafana.` to slice-1 T12's list of reserved names (plan:6430, :6453, :6489). The refusal logic does not change.
- Modify: `docs/usage.md`: the Hostnames table and its refusal sentence (:1488-1500 today, grown by slice-1 T12 Step 10a), and the Control-stack hostnames table (:3267-3272 today, grown by T12 Step 10b).
- Test: `packages/pstack/internal/routing/domains_test.go` (append one test)
- Test: `packages/pstack/internal/autolabel/autolabel_test.go` (one t.Run inside slice-1's `TestAPreviewCannotClaimAControlHostname`)

**Interfaces:**
- Consumes:
  - `func (s *RoutingStore) IsControlHostname(hostname, primary string) bool` as slice-1 T12 leaves it. It lowercases `d` once and compares `if h == "control."+d || h == "api."+d || h == "loki."+d {` (plan:6310-6311). `Domains()` returns nothing on a nil receiver (domains.go:95-101).
  - `func (s *RoutingStore) SetDomains(domains []string, o DomainOptions) ([]string, error)` (domains.go:140) and `DomainOptions{Primary, Mode}` (domains.go:69-76).
  - Slice-1 T12's `TestAPreviewCannotClaimAControlHostname`, which sets `PSTACK_ROUTING_DIR` and `PSTACK_DOMAIN=preview.example.com`, adds the domain `other.example.org`, and defines the closures `claiming(t, host)` and `refused(t, host)`. `refused` calls `t.Fatalf("%s must be refused as a control hostname, got %v", …)` unless the error is a spec error containing `pstack.routing.host=<host> — a control hostname` (plan:6361-6395).
  - Slice-1's `autolabel.ControlHostname` seam and the refusal in `AugmentComposeDoc` (plan:6453-6491, with R7's `control/.env` DOMAIN fallback). Both route through `IsControlHostname`, so neither needs a code change.
- Produces:
  - `func (s *RoutingStore) IsControlHostname(hostname, primary string) bool`. The signature is unchanged. It is now also true for `grafana.<d>` on the primary and on every added domain, whether logging is on or off. A nil store still answers for the primary. T9's service-host refusal and the wake exclusion (server.go:647) use it.
  - `func TestGrafanaIsAControlHostnameOnEveryDomain(t *testing.T)` in `internal/routing`.
  - `t.Run("grafana. of the primary domain is refused", …)` inside `TestAPreviewCannotClaimAControlHostname` in `internal/autolabel`.

---

- [ ] **Step 1: Write the failing tests.**

(a) Append this to the end of `packages/pstack/internal/routing/domains_test.go`. The file already imports `strings` and `testing` (domains_test.go:3-9), and this test needs nothing else.

```go
func TestGrafanaIsAControlHostnameOnEveryDomain(t *testing.T) {
	// negative control: drop `|| h == "grafana."+d` from IsControlHostname — every true-expecting
	// assertion below fails, and grafana.<domain> is left to the wake router and to any preview that
	// asks for it with pstack.routing.host.
	// negative control: compare with `strings.HasPrefix(h, "grafana")` instead of `h == "grafana."+d` —
	// the grafana-pr-1 assertion fails.
	s := New(t.TempDir())
	if _, err := s.SetDomains([]string{"added.example"}, DomainOptions{Primary: "preview.example.com", Mode: "http01"}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"grafana.preview.example.com", "grafana.added.example", "GRAFANA.Added.Example"} {
		if !s.IsControlHostname(h, "preview.example.com") {
			t.Errorf("%s must be a control hostname", h)
		}
	}
	// An exact name, not a prefix: a convention hostname that happens to start with grafana is a preview's.
	if s.IsControlHostname("grafana-pr-1.preview.example.com", "preview.example.com") {
		t.Error("a preview hostname is not the control plane's")
	}
	// The primary needs no file to be reserved — the nil store still answers for it.
	if !(*RoutingStore)(nil).IsControlHostname("grafana.preview.example.com", "preview.example.com") {
		t.Error("grafana.<primary> must be a control hostname on a nil store")
	}
}
```

(b) In `packages/pstack/internal/autolabel/autolabel_test.go`, insert a subtest into `TestAPreviewCannotClaimAControlHostname` just before slice-1's added-domain subtest. `refused` is a closure local to that function, so the new subtest has to go inside it.

Existing:

```go
	t.Run("api. of an added domain is refused", func(t *testing.T) {
```

Replacement:

```go
	t.Run("grafana. of the primary domain is refused", func(t *testing.T) {
		// negative control: drop `|| h == "grafana."+d` from routing.IsControlHostname —
		// grafana.preview.example.com gets a router and this fails with `got <nil>`.
		refused(t, "grafana.preview.example.com")
	})

	t.Run("api. of an added domain is refused", func(t *testing.T) {
```

No import changes. Slice-1 T12 already added `routing`, and `strings`/`spec`/`omap` were already there (autolabel_test.go:8-20).

- [ ] **Step 2: Run them and watch them fail.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/ -run TestGrafanaIsAControlHostnameOnEveryDomain
```

Expected:

```
--- FAIL: TestGrafanaIsAControlHostnameOnEveryDomain
    domains_test.go:…: grafana.preview.example.com must be a control hostname
    domains_test.go:…: grafana.added.example must be a control hostname
    domains_test.go:…: GRAFANA.Added.Example must be a control hostname
    domains_test.go:…: grafana.<primary> must be a control hostname on a nil store
FAIL
```

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ -run TestAPreviewCannotClaimAControlHostname
```

Expected. Only the new subtest fails. The loki, api and other-host subtests stay green.

```
--- FAIL: TestAPreviewCannotClaimAControlHostname
    --- FAIL: TestAPreviewCannotClaimAControlHostname/grafana._of_the_primary_domain_is_refused
        autolabel_test.go:…: grafana.preview.example.com must be refused as a control hostname, got <nil>
FAIL
```

- [ ] **Step 3: Implement.** In `packages/pstack/internal/routing/domains.go`, make two edits inside `IsControlHostname`.

(a) The doc comment.

Existing (slice-1 T12, plan:6305-6306):

```go
// `loki.` counts ALWAYS, not only on a host running `--logging loki`: a host that turned logging
// off must still never answer `loki.` with a waking page, and no preview can take the name meanwhile.
```

Replacement:

```go
// `loki.` and `grafana.` count ALWAYS, not only on a host running `--logging loki`: a host that
// turned logging off must still never answer either with a waking page, and no preview can take
// the name meanwhile. `grafana.` exists only on the primary; on an added domain it is reserved so
// that the wake catch-all never hands it to a preview.
```

(b) The comparison.

Existing (slice-1 T12, plan:6313):

```go
		if h == "control."+d || h == "api."+d || h == "loki."+d {
```

Replacement:

```go
		if h == "control."+d || h == "api."+d || h == "loki."+d || h == "grafana."+d {
```

- [ ] **Step 4: Run them and watch them pass, then run the negative controls.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/ ./internal/autolabel/
```

Expected:

```
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/routing	…s
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel	…s
```

Slice-1's `TestLokiIsAControlHostnameOnEveryDomain`, `TestEachDomainGetsConsoleAPIAndAWakeRouter` and the rest of `TestAPreviewCannotClaimAControlHostname` stay green.

Run each negative control, watch it fail, restore the code, and rerun to green:
1. Delete ` || h == "grafana."+d`. Then `-run TestGrafanaIsAControlHostnameOnEveryDomain` fails with the four Step 2 messages. `-run TestAPreviewCannotClaimAControlHostname` fails only `grafana._of_the_primary_domain_is_refused`, with `got <nil>`.
2. Replace ` || h == "grafana."+d` with ` || strings.HasPrefix(h, "grafana")`. Then `-run TestGrafanaIsAControlHostnameOnEveryDomain` fails only with `a preview hostname is not the control plane's`.

- [ ] **Step 5: Keep autolabel's copy true.** In `packages/pstack/internal/autolabel/autolabel.go`, update the three places where slice-1 T12 lists the reserved names. Anchor on the comment and message text only. R7 changed `ControlHostname`'s body (the `control/.env` DOMAIN fallback), so do not quote the body.

(a) Package header, the `── NOR DOES IT HAND OUT A CONTROL HOSTNAME` section.

Existing (plan:6430-6433):

```go
// A `pstack.routing.host` naming `control.`, `api.` or `loki.` of any domain this host answers on
// is refused (ControlHostname): its router would compete with the control plane's own for the
// console, the API, or every node's log pushes and the password on them. `loki.` is reserved even
// with logging off.
```

Replacement:

```go
// A `pstack.routing.host` naming `control.`, `api.`, `loki.` or `grafana.` of any domain this host
// answers on is refused (ControlHostname): its router would compete with the control plane's own
// for the console, the API, every node's log pushes and the password on them, or the Grafana
// sign-in cookie. `loki.` and `grafana.` are reserved even with logging off.
```

(b) The `ControlHostname` doc comment, first line.

Existing (plan:6453):

```go
// ControlHostname reports whether a hostname is the control plane's — `control.`, `api.` or `loki.`
```

Replacement:

```go
// ControlHostname reports whether a hostname is the control plane's — `control.`, `api.`, `loki.` or `grafana.`
```

(c) The refusal message, inside the `fmt.Sprintf` in `AugmentComposeDoc`.

Existing (plan:6489):

```go
(control., api. and loki. on every domain it answers on)
```

Replacement:

```go
(control., api., loki. and grafana. on every domain it answers on)
```

`refused` matches only the prefix `pstack.routing.host=<host> — a control hostname`, so no test changes. Rerun:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel	…s`.

- [ ] **Step 6: Docs.** Make two edits in `docs/usage.md`, both anchored on slice-1 T12 Step 10's text (plan:6582-6612). Slice 2 does not touch either table.

(a) The Hostnames table and the refusal sentence under it.

Existing:

```markdown
| `loki.<domain>` | Loki's push endpoint, for the nodes' log plugins (`--logging loki`); reserved even with logging off |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
```

Replacement:

```markdown
| `loki.<domain>` | Loki's push endpoint, for the nodes' log plugins (`--logging loki`); reserved even with logging off |
| `grafana.<domain>` | Grafana (`--logging loki`), signed in with pstack accounts; reserved even with logging off |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
```

Existing:

```markdown
A `pstack.routing.host` naming `control.`, `api.` or `loki.` on any domain this host answers on is
refused at deploy.
```

Replacement:

```markdown
A `pstack.routing.host` naming `control.`, `api.`, `loki.` or `grafana.` on any domain this host
answers on is refused at deploy.
```

(b) The Control-stack hostnames table. The router name is `pstack-grafana`, from the spec's GrafanaService labels.

Existing:

```markdown
| `loki.<domain>` | `pstack-loki` | Loki's push path, only with `--logging loki` |
```

Replacement:

```markdown
| `loki.<domain>` | `pstack-loki` | Loki's push path, only with `--logging loki` |
| `grafana.<domain>` | `pstack-grafana` | Grafana (`--logging loki`), signed in with pstack accounts; reserved even with logging off |
```

- [ ] **Step 7: Package gate.** api is included because `wakeFor` (server.go:647) reads `IsControlHostname`.

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/ ./internal/autolabel/ ./internal/api/ && go vet ./internal/routing/ ./internal/autolabel/ && gofmt -l internal
```

Expected: three `ok` lines, no vet output, and `gofmt -l` printing nothing.

Acceptance check: `grep -n 'grafana' docs/usage.md` shows the two new table rows and the refusal sentence.

- [ ] **Step 8: Commit.** Run `but status` for the file IDs of `packages/pstack/internal/routing/domains.go`, `packages/pstack/internal/routing/domains_test.go`, `packages/pstack/internal/autolabel/autolabel.go`, `packages/pstack/internal/autolabel/autolabel_test.go` and `docs/usage.md`. Do not include anything under `packages/conformance/golden/host/db/` or `.superpowers/`.

```bash
but commit -b claude/loki-logging-grafana -m "feat(routing): grafana. is a control hostname" <ids>
```

---

### Task 3: auth: SessionHashUser — resolve a stored session hash

**Files:**
- Modify: `packages/pstack/internal/auth/auth.go` (edit by the quoted `SessionUser` text. The line numbers are hints: 476-482 as of `21461d3`. Slices 1 and 2 don't touch this file.)
- Test: `packages/pstack/internal/auth/auth_test.go` (append at the end of the file)

**Interfaces:**
- Consumes (existing code, no earlier task):
  - `func sha256Hex(input string) string` (auth.go:181)
  - `func now() int64` (auth.go:195)
  - `func scanUserRow(row *sql.Row, extra ...any) (*UserRow, error)`, which gives nil, nil on no rows (auth.go:216-222)
  - `func (a *Auth) Bootstrap(username, password string) (*UserRow, error)` (auth.go:441)
  - `func (a *Auth) Login(username, password string) (session string, user *UserRow, err error)` (auth.go:453)
  - `func HashToken(token string) string`, which is `sha256Hex` (auth.go:958)
  - test helper `func open(t *testing.T) *Auth` (auth_test.go:17)
  - `a.store.DB *sql.DB` (auth.go:226, store.go:57)
- Produces (T8 relies on these):
  - `func (a *Auth) SessionHashUser(idHash string) (*UserRow, error)` runs the same statement `SessionUser` ran, with `idHash, now()` as its arguments.
  - `func (a *Auth) SessionUser(session string) (*UserRow, error)` is now a wrapper that returns `a.SessionHashUser(sha256Hex(session))`. Its behaviour does not change. Its only non-test caller stays `internal/api/principal.go:46`.

- [ ] **Step 1: Write the failing test.** Add this to the end of `packages/pstack/internal/auth/auth_test.go`, after the closing `}` of `TestSsoDefaultRoleIsLeastPrivilege`. No new imports: `open`, `HashToken`, `now` and `a.store` are all in package `auth`.

```go

// The Grafana cookie carries a session's stored hash, not the session, and resolves it through the
// same query as the cookie value — so expiry and revocation reach both on the next request.
func TestSessionHashUser(t *testing.T) {
	// negative control: drop `AND s.expires_at > ?` and its now() argument from SessionHashUser → the
	// expired session still resolves; or pass `session` unhashed in SessionUser → the lookups disagree.
	a := open(t)
	if _, err := a.Bootstrap("sami", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	session, _, err := a.Login("sami", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	byCookie, err := a.SessionUser(session)
	if err != nil || byCookie == nil || byCookie.Username != "sami" {
		t.Fatalf("SessionUser: %+v %v", byCookie, err)
	}
	byHash, err := a.SessionHashUser(HashToken(session))
	if err != nil || byHash == nil || byHash.ID != byCookie.ID || byHash.Username != byCookie.Username {
		t.Fatalf("SessionHashUser: %+v %v; SessionUser: %+v", byHash, err, byCookie)
	}
	if _, err := a.store.DB.Exec("UPDATE sessions SET expires_at = ?", now()); err != nil {
		t.Fatal(err)
	}
	if u, err := a.SessionUser(session); u != nil || err != nil {
		t.Fatalf("expired, SessionUser: %+v %v", u, err)
	}
	if u, err := a.SessionHashUser(HashToken(session)); u != nil || err != nil {
		t.Fatalf("expired, SessionHashUser: %+v %v", u, err)
	}
}
```

  Why `UPDATE … SET expires_at = now()` expires the row: the later lookup compares `expires_at > now()`, and that later `now()` is equal or greater, so the condition is false even within the same millisecond.

- [ ] **Step 2: Run it and watch it fail.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/auth/ -run TestSessionHashUser
```

  Expected: a build failure, twice (once per call):

```
internal/auth/auth_test.go:<n>:19: a.SessionHashUser undefined (type *Auth has no field or method SessionHashUser)
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/auth [build failed]
```

- [ ] **Step 3: Implement.** In `packages/pstack/internal/auth/auth.go`, replace this exact text:

```go
// SessionUser resolves a cookie value to its account, or nil.
func (a *Auth) SessionUser(session string) (*UserRow, error) {
	return scanUserRow(a.store.DB.QueryRow(
		`SELECT u.id, u.username, u.role, u.email, u.created_at FROM sessions s
         JOIN users u ON u.id = s.user_id
         WHERE s.id_hash = ? AND s.expires_at > ?`, sha256Hex(session), now()))
}
```

  with:

```go
// SessionUser resolves a cookie value to its account, or nil.
func (a *Auth) SessionUser(session string) (*UserRow, error) {
	return a.SessionHashUser(sha256Hex(session))
}

// SessionHashUser resolves a stored session hash (sessions.id_hash) to its account, or nil.
//
// Only the Grafana cookie reaches it: that cookie carries the hash under a MAC, never the session.
// A hash is not a credential anywhere else — principal and Logout hash whatever they are given, so
// an id_hash presented as pstack_session is hashed again and matches nothing. One query for both
// callers, so an expired or revoked pstack session is a dead Grafana one on the next request.
func (a *Auth) SessionHashUser(idHash string) (*UserRow, error) {
	return scanUserRow(a.store.DB.QueryRow(
		`SELECT u.id, u.username, u.role, u.email, u.created_at FROM sessions s
         JOIN users u ON u.id = s.user_id
         WHERE s.id_hash = ? AND s.expires_at > ?`, idHash, now()))
}
```

  Keep the wrapper as three lines. On one line it is 108 columns, and `gofmt -l` flags it and expands it.

  Don't edit the `SetRole` comment at auth.go:379 ("SessionUser and TokenUser join `users` and read the role on every request"). It stays true, because `SessionUser` still reaches that join through `SessionHashUser`. No doc names `SessionUser` (`grep -rn SessionUser docs AGENTS.md` finds nothing), so this task has no doc step.

- [ ] **Step 4: Run it and watch it pass.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/auth/ -run TestSessionHashUser -v
```

  Expected:

```
=== RUN   TestSessionHashUser
--- PASS: TestSessionHashUser (1.9s)
PASS
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/auth
```

- [ ] **Step 5: Run the negative controls.** Each one must fail. Revert after each.
  1. In `SessionHashUser`, change `` WHERE s.id_hash = ? AND s.expires_at > ?`, idHash, now())) `` to `` WHERE s.id_hash = ?`, idHash)) ``. Expected: `--- FAIL: TestSessionHashUser` with `expired, SessionUser: &{ID:1 Username:sami Role:admin …} <nil>`.
     - Drop the `now()` argument too. If only the text `expires_at > ?` goes, the leftover `AND` is a SQL syntax error. The test then fails at the first lookup, which is the wrong reason.
  2. In `SessionUser`, change `return a.SessionHashUser(sha256Hex(session))` to `return a.SessionHashUser(session)`. Expected: `--- FAIL: TestSessionHashUser` with `SessionUser: <nil> <nil>`.

- [ ] **Step 6: Run the whole package, vet and gofmt.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/auth/ && go vet ./internal/auth/ && gofmt -l internal/auth
```

  Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/auth` (about 60 s under -race), vet prints nothing, and gofmt lists nothing.
  - The existing `SessionUser` tests at auth_test.go:111, 141, 399, 462 and 646 pass unedited.

- [ ] **Step 7: Commit.**

```
cd /Volumes/S1/code/preview-stacks && but status
but commit -b claude/loki-logging-grafana -m "refactor(auth): SessionHashUser resolves a stored session hash" <id of packages/pstack/internal/auth/auth.go> <id of packages/pstack/internal/auth/auth_test.go>
```

  Commit only those two files. Never include `packages/conformance/golden/host/db/*` or `.superpowers/`. No attribution footer.

---

### Task 4: inspect: GrafanaOn — does the control project run a grafana container

**Files:**
- Modify: `packages/pstack/internal/inspect/control.go`. Two edits: the header paragraph slice-1 T7 added, and a new `GrafanaOn` right after `LokiPushURL`. Both are anchored by quoted text. Line numbers are only hints: slice-1 T7 moves them, and at the time of writing T7 has not landed (`claude/loki-logging` is at 7e05bda, T3).
- Test: `packages/pstack/internal/inspect/control_test.go` (append after slice-1's `TestLokiPushURLComesFromTheControlStacksLokiContainer`)

**Interfaces:**
- Consumes (slice-1 T7, docs/loki-logging-slice-1-plan.md:3447-3464 and :3527-3556):
  - `func LokiPushURL(r exec.Runner) string` in control.go.
  - The test helper `func lokiHost(ps exec.Result, inspect string) *exec.Fake` in control_test.go. It answers `docker ps -aq …pstack-control…` with `ps`, answers `docker inspect …` with `{OK: true, Stdout: inspect}`, and answers everything else with `{OK: true}`.
  - Existing: `idsByLabel(r exec.Runner, label string) ([]string, bool)` and `inspectIDs(r exec.Runner, ids []string) []rawInspect`. `idsByLabel` returns `nil, false` on `!res.OK`. `inspectIDs` returns nil for `len(ids) == 0` or a failed inspect. `rawInspect.Config *struct{…; Labels map[string]string}` (inspect.go:136-192). `ControlProject = initctl.ControlProject` (control.go:27).
- Produces: `package inspect; func GrafanaOn(r exec.Runner) bool`. It issues `docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'`, then `docker inspect '<id>' …`. It returns true if any inspected container has the label `com.docker.compose.service` = `grafana`. It returns false when docker does not answer or there are no control containers. T7 calls it as `inspect.GrafanaOn(s.host)`.

If T7 landed with different header wording, replace whichever header paragraph names `LokiPushURL`. If its helper is not named `lokiHost(ps exec.Result, inspect string) *exec.Fake`, use the name T7 gave it. Both are hand-offs, not design choices.

- [ ] **Step 1: Write the failing test.** Append to `packages/pstack/internal/inspect/control_test.go`. The imports `strings`, `testing` and `exec` are already there, and `lokiHost` comes from slice-1 T7.

```go
func TestGrafanaOn(t *testing.T) {
	// negative control: drop the `com.docker.compose.service == "grafana"` check in GrafanaOn — a host with only traefik and loki reads as Grafana on.
	traefik := `{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.6.1","Labels":{"com.docker.compose.service":"traefik"}}}`
	loki := `{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}}}`
	grafana := `{"Id":"g1","Name":"/pstack-control-grafana-1","Config":{"Image":"grafana/grafana:13.2.1","Labels":{"com.docker.compose.service":"grafana"}}}`

	t.Run("a grafana control container is Grafana on", func(t *testing.T) {
		// negative control: match `raw.Name == "grafana"` instead of the compose service label — the name is /pstack-control-grafana-1, so this reads false.
		if !GrafanaOn(lokiHost(exec.Result{OK: true, Stdout: "t1\nl1\ng1\n"}, "["+traefik+","+loki+","+grafana+"]")) {
			t.Error("got false")
		}
	})

	t.Run("only loki is Grafana off", func(t *testing.T) {
		// negative control: drop the `com.docker.compose.service == "grafana"` check — traefik reads as Grafana.
		if GrafanaOn(lokiHost(exec.Result{OK: true, Stdout: "t1\nl1\n"}, "["+traefik+","+loki+"]")) {
			t.Error("got true")
		}
	})

	t.Run("docker not answering is Grafana off", func(t *testing.T) {
		// negative control: drop idsByLabel's `!res.OK` return — the failed `docker ps` still printed g1, and its inspect reads grafana.
		failed := exec.Result{OK: false, Code: 1, Stdout: "g1\n", Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}
		if GrafanaOn(lokiHost(failed, "["+grafana+"]")) {
			t.Error("got true")
		}
	})

	t.Run("no control containers is Grafana off", func(t *testing.T) {
		// negative control: drop inspectIDs' `len(ids) == 0` return — a bare `docker inspect` is asked and this fake's grafana answer leaks through.
		if GrafanaOn(lokiHost(exec.Result{OK: true}, "["+grafana+"]")) {
			t.Error("got true")
		}
	})
}
```

The fixtures spell `com.docker.compose.service` as a literal, never through a Go constant, so a drifted key fails here.

- [ ] **Step 2: Run it and watch it fail**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/ -run TestGrafanaOn
```
Expected (four times, once per call site):
```
internal/inspect/control_test.go:NNN:N: undefined: GrafanaOn
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect [build failed]
```

- [ ] **Step 3: Implement `GrafanaOn`.** Two edits to `packages/pstack/internal/inspect/control.go`. `exec` is already imported (control.go:20). There are no new imports.

First, the header records the second read (AGENTS.md: the header is the design record). Replace the paragraph slice-1 T7 added (plan:3509-3513):
```go
// And one read that deploys depend on: LokiPushURL. Whether a deploy injects a loki logging block
// comes from the running loki container's label, not from a setting. So `pstack up` on the host and
// the API always agree, and nothing needs syncing when `pstack logging` switches.
package inspect
```
with:
```go
// And two reads of what the control stack runs, never of a setting. LokiPushURL: whether a deploy
// injects a loki logging block comes from the running loki container's label, so `pstack up` on the
// host and the API always agree, and nothing needs syncing when `pstack logging` switches. GrafanaOn:
// whether the API answers Grafana sign-in and names Grafana in /api/health comes from a grafana
// container being there, so no setting can claim a Grafana this host does not run.
package inspect
```

Second, insert the function right after `LokiPushURL`. Replace the end of `LokiPushURL` and the comment after it (plan:3551-3556):
```go
	return ""
}

// The three ways RestartControlService refuses, for the route to map onto statuses.
```
with:
```go
	return ""
}

// GrafanaOn reports whether the control project has a `grafana` container. `-a`, as LokiPushURL: a
// Grafana that is restarting is still this host's, and `pstack logging off` removes the container.
func GrafanaOn(r exec.Runner) bool {
	// nil when docker did not answer, and inspectIDs of nil asks nothing.
	ids, _ := idsByLabel(r, "com.docker.compose.project="+ControlProject)
	for _, raw := range inspectIDs(r, ids) {
		if raw.Config != nil && raw.Config.Labels["com.docker.compose.service"] == "grafana" {
			return true
		}
	}
	return false
}

// The three ways RestartControlService refuses, for the route to map onto statuses.
```
The spec's block (design doc `## Slice 3` › "How pstack knows Grafana is on") indexes `raw.Config.Labels` with no nil check. The `raw.Config != nil &&` guard is the contract's, and LokiPushURL has the same guard. Keep it: do not "fix" the code back to the spec's text.

- [ ] **Step 4: Run it and watch it pass**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/ -run TestGrafanaOn -v
```
Expected:
```
--- PASS: TestGrafanaOn (0.00s)
    --- PASS: TestGrafanaOn/a_grafana_control_container_is_Grafana_on (0.00s)
    --- PASS: TestGrafanaOn/only_loki_is_Grafana_off (0.00s)
    --- PASS: TestGrafanaOn/docker_not_answering_is_Grafana_off (0.00s)
    --- PASS: TestGrafanaOn/no_control_containers_is_Grafana_off (0.00s)
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect	…
```
Then run the whole package, vet and gofmt:
```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/ && go vet ./internal/inspect/ && gofmt -l internal/inspect
```
Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect	…`. `go vet` and `gofmt -l` print nothing.

- [ ] **Step 5: Run each negative control**

Mutations 3 and 4 touch inspect.go, which is outside this commit, so apply every mutation through `-overlay` and the tree never changes. For each mutation:
1. Copy the file into a scratch dir and edit the copy.
2. Write `{"Replace":{"/Volumes/S1/code/preview-stacks/packages/pstack/internal/inspect/<file>":"<abs path of the copy>"}}` to a JSON file.
3. Run:
```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s -overlay <that.json> ./internal/inspect/ -run TestGrafanaOn -v
```

Each result below was observed against slice-1 T7's plan code plus this task's code:

| # | Mutation (file) | Fails |
|---|---|---|
| 1 | control.go: `raw.Config != nil && raw.Config.Labels["com.docker.compose.service"] == "grafana"` → `raw.Name == "grafana"` | `a grafana control container is Grafana on` only |
| 2 | control.go: the same condition → `raw.Config != nil` (the service-name check dropped; a bare `true` does not compile because `raw` is then unused) | `only loki is Grafana off` only, which is also the parent's control |
| 3 | inspect.go `idsByLabel`: delete `if !res.OK { return nil, false }` | `docker not answering is Grafana off` only |
| 4 | inspect.go `inspectIDs`: delete `if len(ids) == 0 { return nil }` | `no control containers is Grafana off`, and also `docker not answering is Grafana off` (its nil ids then reach a bare inspect) |

Each mutation must print `--- FAIL` for the subtest named. Only mutation 4 fails two subtests. Do not claim that each control isolates exactly one subtest.

- [ ] **Step 6: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```
Pick the IDs for exactly `packages/pstack/internal/inspect/control.go` and `packages/pstack/internal/inspect/control_test.go`. `but status` must show no `internal/inspect/inspect.go` hunk. If it shows one, a mutation was left in the tree: revert it before committing. Never pick `packages/conformance/golden/host/db/*` or anything under `.superpowers/`.

```bash
but commit -b claude/loki-logging-grafana -m "feat(inspect): GrafanaOn reads the control stack for a grafana container" <control.go id> <control_test.go id>
```

---

### Task 5: initctl: render Grafana (pure functions + embedded datasource)

Init does not call anything added here; T6 wires it in. With logging off nothing renders differently, so TestInitGoldens stays byte-identical.

**Files:**
- Create: `packages/pstack/templates/control/grafana/datasources.yaml`
- Modify: `packages/pstack/assets.go` (anchor edits by quoted existing text: the `LokiConfig` embed and the package comment's `seven files`)
- Modify: `packages/pstack/internal/initctl/init.go` (anchors: `const PushURLLabel = "pstack.logging.push-url"` at :89 and `// traefikBlock isolates the "  traefik:" service…` at :851; line numbers are advisory, slices 1 and 2 move them)
- Modify: `docs/control-plane.md` ("### Assets are embedded, not read from disk", :1262-1268)
- Test: `packages/pstack/internal/initctl/init_test.go` (anchors: the import block; TestLokiWiring's doc comment; TestInitLoki's doc comment)

**Interfaces:**
- Consumes (slice 1, already in the tree):
  - `initctl.Logging`, `Loki`, `LoggingNone`, `Challenge`, `HTTP01`, `DNS01` (init.go:69-84).
  - `LokiService`'s shape (init.go:800-849): a leading `""`, `tls=true`, the certresolver only under HTTP01, a trailing `""`.
  - The byte-exact precedent `lokiConfigSpecLines` + `TestLokiConfig` (init_test.go:765-863, ruling R8).
  - `yamlx.ParseString` (yamlx.go:85); `(*omap.Map).Has/GetMap/GetString/GetSlice` (omap.go:48, :151-170); `(*omap.Map).MarshalJSON` (omap/json.go:10).
- Produces:
  - `const GrafanaVersion = "13.2.1"` (package initctl). T6 uses it in the `grafana image` requires entry.
  - `func GrafanaService(logging Logging, challenge Challenge) string` (package initctl). It returns `""` unless `logging == Loki`. T6 appends it to the `#__ADVANCED_UI_SERVICE__` substitution.
  - `var GrafanaDatasources string` (package pstack, `//go:embed templates/control/grafana/datasources.yaml`). T6 writes it to `<DATA>/control/grafana/datasources/loki.yaml` at 0644.
  - Test-only: `grafanaServiceSpecLines`, `grafanaDatasourcesSpecLines`, `TestGrafanaService`, `TestGrafanaDatasources` (package initctl_test). T1's design doc cites the subtest name `Traefik strips every header Grafana reads`; keep it verbatim.

Two notes on the tests:
- **The "must not appear" checks parse the block; they never grep it.** The block's own comments contain `Never GF_AUTH_DISABLE_LOGIN: it unregisters…` and `over preview-ingress, the network Traefik…`. So `!strings.Contains(block, "GF_AUTH_DISABLE_LOGIN:")` and `!strings.Contains(block, "preview-ingress")` would fail on the correct render. The test asserts on the parsed service map instead: `env.Has`, `networks`, `Has("ports")`, and `json.Marshal` of the map, which carries no comments.
- **The HTTP-01 block is also pinned byte for byte** against an in-test copy of the spec block, for R8's reason: T11's goldens are generated from the binary. Most lines in the block are sign-in controls that no other check reads, such as `GF_AUTH_PROXY_ENABLE_LOGIN_TOKEN: "false"`, `GF_AUTH_ANONYMOUS_ENABLED: "false"`, `trustForwardHeader=false` and the forwardAuth address.

- [ ] **Step 1: Write the failing datasource test**

In `packages/pstack/internal/initctl/init_test.go`, insert this directly above the line `// init --logging loki: the push password, its .env line, Loki's config and the plugin step. Logging off`. If slice 2 reworded that comment, insert it directly above the doc comment of `func TestInitLoki(t *testing.T) {`.

```go
// grafanaDatasourcesSpecLines is docs/loki-logging-design.md's datasource block (Slice 3, the ```yaml
// fence under "#### The datasource file"), copied for lokiConfigSpecLines' reason.
var grafanaDatasourcesSpecLines = []string{
	"# Written by `pstack init --logging loki`. Grafana re-applies it on every start.",
	"apiVersion: 1",
	"datasources:",
	"  - name: Loki",
	"    type: loki",
	"    uid: loki",
	"    access: proxy            # Grafana's server calls Loki over the logs network; Traefik is not involved",
	"    url: http://loki:3100",
	"    isDefault: true",
	"    editable: false",
	"    jsonData:",
	"      maxLines: 1000",
	"      timeout: 60",
	"",
}

// Grafana's only datasource. A wrong url or uid is an empty Explore, not an error.
func TestGrafanaDatasources(t *testing.T) {
	// negative control: change `maxLines: 1000` to 500 in templates/control/grafana/datasources.yaml — fails.
	if want := strings.Join(grafanaDatasourcesSpecLines, "\n"); pstack.GrafanaDatasources != want {
		t.Errorf("templates/control/grafana/datasources.yaml no longer matches the design doc's datasource block\n--- got\n%s\n--- want\n%s", pstack.GrafanaDatasources, want)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run TestGrafanaDatasources
```

Expected: the build fails with `undefined: pstack.GrafanaDatasources` (two sites), then `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl [build failed]`.

- [ ] **Step 3: Create the datasource file and embed it**

Create `packages/pstack/templates/control/grafana/datasources.yaml` with exactly these 13 lines. The file ends in a single `\n` after `      timeout: 60`, with no blank line after it, and it holds no credential.

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

In `packages/pstack/assets.go`, replace the package comment's first line:

```go
// Package pstack carries the seven files the binary embeds. Explicit paths, never a glob: the
```

with:

```go
// Package pstack carries the eight files the binary embeds. Explicit paths, never a glob: the
```

Then replace:

```go
//go:embed templates/control/loki/config.yaml
var LokiConfig string
```

with:

```go
//go:embed templates/control/loki/config.yaml
var LokiConfig string

// GrafanaDatasources is Grafana's provisioned Loki datasource, which init writes to
// control/grafana/datasources/loki.yaml with `--logging loki`. Embedded as-is for LokiConfig's
// reasons: nothing in it varies by host, and it holds no credential.
//
//go:embed templates/control/grafana/datasources.yaml
var GrafanaDatasources string
```

- [ ] **Step 4: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run TestGrafanaDatasources -v
```

Expected: `--- PASS: TestGrafanaDatasources`, then `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl`. Also, `grep -c '^//go:embed' /Volumes/S1/code/preview-stacks/packages/pstack/assets.go` prints `8`.

- [ ] **Step 5: Write the failing service test**

In `init_test.go`'s import block, replace:

```go
	"regexp"
	"strings"
```

with:

```go
	"regexp"
	"slices"
	"strings"
```

Insert this directly above the line `// Loki's plumbing is literal edits of lines the template already has. A template change that moves` (TestLokiWiring's doc comment). If slice 2 reworded that comment, insert it directly above the doc comment of `func TestLokiWiring(t *testing.T) {`. Don't anchor on TestLokiService's closing brace: slice 2 may extend that test.

```go
// grafanaServiceSpecLines is docs/loki-logging-design.md's GrafanaService block (Slice 3, the ```yaml
// fence under "#### The service (`GrafanaService`)"), which is the HTTP-01 render. Copied for
// lokiConfigSpecLines' reason: the render golden is generated from the binary, so only an independent
// copy catches a line transcribed wrong, and most of these lines are sign-in controls no other check reads.
var grafanaServiceSpecLines = []string{
	"",
	"  # Grafana, with --logging loki: the reader for Loki. Signed in with pstack accounts (forwardAuth).",
	"  grafana:",
	"    image: grafana/grafana:13.2.1    # not -slim (no bundled plugins), not -distroless (no curl for the health check)",
	"    restart: unless-stopped",
	"    mem_limit: 768m                  # 13.x idles ~330Mi and OOMed under 400Mi (grafana#123017)",
	"    # The logs network ONLY. Never preview-ingress, never a published port: anything that reaches",
	"    # :3000 directly can send X-WEBAUTH-USER and be anyone. Only Traefik and Loki share it.",
	"    networks: [logs]",
	"    volumes:",
	"      - grafana:/var/lib/grafana      # users, preferences, and the Drilldown plugin download",
	"      - ./grafana/datasources:/etc/grafana/provisioning/datasources:ro",
	"    environment:",
	"      GF_SERVER_DOMAIN: grafana.${DOMAIN}",
	"      GF_SERVER_ROOT_URL: https://grafana.${DOMAIN}/",
	"      # Sign-in: Traefik's forwardAuth asks pstack, and pstack's answer is these two headers.",
	`      GF_AUTH_PROXY_ENABLED: "true"`,
	"      GF_AUTH_PROXY_HEADER_NAME: X-WEBAUTH-USER",
	"      GF_AUTH_PROXY_HEADER_PROPERTY: username",
	"      GF_AUTH_PROXY_HEADERS: Role:X-WEBAUTH-ROLE",
	`      GF_AUTH_PROXY_AUTO_SIGN_UP: "true"`,
	"      # false: no grafana_session of Grafana's own, so pstack decides EVERY request and a pstack",
	"      # sign-out applies on the next one.",
	`      GF_AUTH_PROXY_ENABLE_LOGIN_TOKEN: "false"`,
	"      # Never GF_AUTH_DISABLE_LOGIN: it unregisters the proxy client and silently turns sign-in off.",
	`      GF_AUTH_DISABLE_LOGIN_FORM: "true"`,
	`      GF_AUTH_DISABLE_SIGNOUT_MENU: "true"`,
	`      GF_AUTH_BASIC_ENABLED: "false"`,
	`      GF_AUTH_ANONYMOUS_ENABLED: "false"`,
	`      GF_USERS_ALLOW_SIGN_UP: "false"`,
	`      GF_USERS_ALLOW_ORG_CREATE: "false"`,
	"      GF_USERS_AUTO_ASSIGN_ORG_ROLE: Viewer",
	"      # No built-in `admin` row, so a pstack user named admin is a user, not the server admin.",
	`      GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION: "true"`,
	"      # Previews on *.${DOMAIN} are same-site with Grafana: SameSite does not stop their POSTs.",
	`      GF_SECURITY_CSRF_ALWAYS_CHECK: "true"`,
	`      GF_SECURITY_COOKIE_SECURE: "true"`,
	`      GF_SECURITY_DISABLE_GRAVATAR: "true"`,
	"      # Off: a WebSocket passes forwardAuth once, at the upgrade, and would outlive a pstack sign-out.",
	`      GF_LIVE_MAX_CONNECTIONS: "0"`,
	"      # Off: an Editor could publish log panels to snapshots.raintank.io, outside verify.",
	`      GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"`,
	`      GF_ANALYTICS_REPORTING_ENABLED: "false"`,
	`      GF_ANALYTICS_CHECK_FOR_UPDATES: "false"`,
	`      GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES: "false"`,
	`      GF_NEWS_NEWS_FEED_ENABLED: "false"`,
	`      GF_PLUGINS_PREINSTALL_AUTO_UPDATE: "false"`,
	"      # Default preinstalls with no datasource here. Logs Drilldown (grafana-lokiexplore-app) stays.",
	"      GF_PLUGINS_DISABLE_PLUGINS: grafana-pyroscope-app,grafana-exploretraces-app,grafana-metricsdrilldown-app,grafana-advisor-app",
	"    healthcheck:",
	`      test: ["CMD", "curl", "-fsS", "-o", "/dev/null", "http://localhost:3000/api/health"]`,
	"      start_period: 60s",
	"      interval: 30s",
	"      timeout: 5s",
	"      retries: 3",
	"    labels:",
	"      - traefik.enable=true",
	"      - traefik.docker.network=pstack-control_logs",
	"      - traefik.http.routers.pstack-grafana.rule=Host(`grafana.${DOMAIN}`)",
	"      # A deployment routed to grafana.<domain> before this release has a rule of the same length",
	"      # (autolabel's Host(`…`)), and equal priority is a coin toss for who gets the Grafana cookie.",
	"      - traefik.http.routers.pstack-grafana.priority=10000",
	"      - traefik.http.routers.pstack-grafana.entrypoints=websecure",
	"      # TLS follows the challenge, exactly like pstack-loki: tls=true alone under DNS-01 (the wildcard",
	"      # covers grafana.), plus its own certresolver under HTTP-01.",
	"      - traefik.http.routers.pstack-grafana.tls=true",
	"      - traefik.http.routers.pstack-grafana.tls.certresolver=le",
	"      - traefik.http.routers.pstack-grafana.middlewares=pstack-grafana-auth",
	"      # pstack, by service name, over preview-ingress, the network Traefik already reaches pstack on",
	"      # (the advanced UI's nginx dials the same name). pstack does not join `logs`.",
	"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.address=http://pstack:7878/api/auth/grafana/verify",
	"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.trustForwardHeader=false",
	"      # EVERY header Grafana reads: header_name plus each GF_AUTH_PROXY_HEADERS value. Traefik deletes",
	"      # a listed header from the client's request unconditionally and passes an unlisted one through.",
	"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.authResponseHeaders=X-WEBAUTH-USER,X-WEBAUTH-ROLE",
	"      - traefik.http.services.pstack-grafana.loadbalancer.server.port=3000",
	"",
}

// Grafana's service block is its auth: the headers Traefik strips, the network, the router priority.
// Each is silent when wrong. Grafana still serves, to the wrong people.
func TestGrafanaService(t *testing.T) {
	// negative control: delete the priority label in GrafanaService — the byte-for-byte and priority subtests fail.
	challenges := []initctl.Challenge{initctl.HTTP01, initctl.DNS01}
	// grafana is the block as compose reads it. Parsed, never grepped: the block's own comments say
	// "Never GF_AUTH_DISABLE_LOGIN:" and "over preview-ingress", so a text search fails the right render.
	grafana := func(t *testing.T, c initctl.Challenge) *omap.Map {
		t.Helper()
		v, err := yamlx.ParseString("services:" + initctl.GrafanaService(initctl.Loki, c))
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		svc := v.(*omap.Map).GetMap("services").GetMap("grafana")
		if svc == nil {
			t.Fatalf("%s: no grafana service", c)
		}
		return svc
	}
	// label is the value of the `key=value` label, or "".
	label := func(svc *omap.Map, key string) string {
		for _, l := range svc.GetSlice("labels") {
			if s, _ := l.(string); strings.HasPrefix(s, key+"=") {
				return strings.TrimPrefix(s, key+"=")
			}
		}
		return ""
	}

	t.Run("the http01 block is the design doc's, byte for byte", func(t *testing.T) {
		// negative control: change `forwardauth.trustForwardHeader=false` to `=true` in GrafanaService — a line no other subtest reads.
		if got, want := initctl.GrafanaService(initctl.Loki, initctl.HTTP01), strings.Join(grafanaServiceSpecLines, "\n"); got != want {
			t.Errorf("GrafanaService no longer matches the design doc's block\n--- got\n%s\n--- want\n%s", got, want)
		}
	})

	t.Run("http01 orders grafana its own certificate, dns01 inherits the wildcard", func(t *testing.T) {
		// negative control: append the certresolver label for every challenge in GrafanaService — the dns01 check fails.
		const resolver = "traefik.http.routers.pstack-grafana.tls.certresolver"
		for _, c := range challenges {
			if got := label(grafana(t, c), "traefik.http.routers.pstack-grafana.tls"); got != "true" {
				t.Errorf("%s: tls=%q", c, got)
			}
		}
		if got := label(grafana(t, initctl.HTTP01), resolver); got != "le" {
			t.Errorf("http01: certresolver=%q, want le", got)
		}
		if got := label(grafana(t, initctl.DNS01), resolver); got != "" {
			t.Errorf("dns01 orders its own certificate: certresolver=%q", got)
		}
	})

	t.Run("the logs network only: no preview-ingress, no published port", func(t *testing.T) {
		// Anything that reaches :3000 directly can send X-WEBAUTH-USER and be anyone.
		// negative control: render `networks: [logs, preview-ingress]` in GrafanaService, or add `    ports: ["3000:3000"]` under it — each fails.
		for _, c := range challenges {
			svc := grafana(t, c)
			if n := fmt.Sprint(svc.GetSlice("networks")); n != "[logs]" {
				t.Errorf("%s: networks %s", c, n)
			}
			if svc.Has("ports") {
				t.Errorf("%s: a published port", c)
			}
			if b, err := json.Marshal(svc); err != nil || strings.Contains(string(b), "preview-ingress") {
				t.Errorf("%s: preview-ingress outside a comment (err %v): %s", c, err, b)
			}
		}
	})

	t.Run("proxy sign-in stays registered; Live and external snapshots stay off", func(t *testing.T) {
		// negative control: in GrafanaService delete the GF_LIVE_MAX_CONNECTIONS line, delete the GF_SNAPSHOTS_EXTERNAL_ENABLED line, unquote `"0"`, or add `GF_AUTH_DISABLE_LOGIN: "true"` — each fails.
		for _, c := range challenges {
			env := grafana(t, c).GetMap("environment")
			if env.Has("GF_AUTH_DISABLE_LOGIN") {
				t.Errorf("%s: GF_AUTH_DISABLE_LOGIN turns proxy sign-in off", c)
			}
			// GetString: an unquoted 0 or false parses as a number or boolean, which compose refuses.
			for _, kv := range [][2]string{{"GF_LIVE_MAX_CONNECTIONS", "0"}, {"GF_SNAPSHOTS_EXTERNAL_ENABLED", "false"}} {
				if got := env.GetString(kv[0]); got != kv[1] {
					t.Errorf("%s: %s = %q, want the string %q", c, kv[0], got, kv[1])
				}
			}
		}
	})

	t.Run("pstack-grafana outranks a deployment already routed to grafana.<domain>", func(t *testing.T) {
		// negative control: delete the priority label in GrafanaService — priority is "".
		for _, c := range challenges {
			if got := label(grafana(t, c), "traefik.http.routers.pstack-grafana.priority"); got != "10000" {
				t.Errorf("%s: priority=%q, want 10000", c, got)
			}
		}
	})

	t.Run("Traefik strips every header Grafana reads", func(t *testing.T) {
		// A header Grafana reads that authResponseHeaders does not list passes from the client to Grafana.
		// negative control: append ` Email:X-WEBAUTH-EMAIL` to GF_AUTH_PROXY_HEADERS only — the sets differ.
		for _, c := range challenges {
			svc := grafana(t, c)
			env := svc.GetMap("environment")
			reads := []string{env.GetString("GF_AUTH_PROXY_HEADER_NAME")}
			for _, f := range strings.Fields(env.GetString("GF_AUTH_PROXY_HEADERS")) {
				_, header, _ := strings.Cut(f, ":")
				reads = append(reads, header)
			}
			stripped := strings.Split(label(svc, "traefik.http.middlewares.pstack-grafana-auth.forwardauth.authResponseHeaders"), ",")
			slices.Sort(reads)
			slices.Sort(stripped)
			if slices.Contains(reads, "") || !slices.Equal(reads, stripped) {
				t.Errorf("%s: Grafana reads %q, Traefik strips %q", c, reads, stripped)
			}
		}
	})

	t.Run("logging off renders nothing", func(t *testing.T) {
		// negative control: drop the `logging != Loki` early return in GrafanaService — every case renders the service.
		for _, l := range []initctl.Logging{initctl.LoggingNone, ""} {
			for _, c := range challenges {
				if got := initctl.GrafanaService(l, c); got != "" {
					t.Errorf("%q/%s rendered:\n%s", l, c, got)
				}
			}
		}
	})
}
```

`GF_AUTH_PROXY_HEADERS: Role:X-WEBAUTH-ROLE` parses as one plain string because its colon is not followed by a space. TestLokiWiring already parses the push-url label `https://pstack:${LOKI_PUSH_PASSWORD}@…`, which is the same case.

- [ ] **Step 6: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run 'TestGrafanaService|TestGrafanaDatasources'
```

Expected: the build fails with `undefined: initctl.GrafanaService` (three sites), then `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl [build failed]`.

- [ ] **Step 7: Implement GrafanaVersion and GrafanaService**

In `packages/pstack/internal/initctl/init.go`, replace:

```go
const PushURLLabel = "pstack.logging.push-url"
```

with:

```go
const PushURLLabel = "pstack.logging.push-url"

// GrafanaVersion is the Grafana image tag. Here, not beside swarm.LokiVersion: swarm needs Loki's tag
// for the plugin install line, and nothing in swarm uses Grafana.
const GrafanaVersion = "13.2.1"
```

Then insert this directly above the line `// traefikBlock isolates the "  traefik:" service from the template: from its key to the line just`, so it follows LokiService:

```go
// GrafanaService is the Grafana container, appended after LokiService at the same marker and, like it,
// omitted entirely unless logging is loki: Loki's query API is on the logs network only, so Grafana is
// how its logs are read. Sign-in is pstack's. Traefik's forwardAuth asks pstack about every request and
// Grafana trusts the two headers pstack answers with, so the network, the stripped headers and the
// router priority below are the whole of its auth. The block's comments say why each one holds.
func GrafanaService(logging Logging, challenge Challenge) string {
	if logging != Loki {
		return ""
	}
	lines := []string{
		"",
		"  # Grafana, with --logging loki: the reader for Loki. Signed in with pstack accounts (forwardAuth).",
		"  grafana:",
		"    image: grafana/grafana:" + GrafanaVersion + "    # not -slim (no bundled plugins), not -distroless (no curl for the health check)",
		"    restart: unless-stopped",
		"    mem_limit: 768m                  # 13.x idles ~330Mi and OOMed under 400Mi (grafana#123017)",
		"    # The logs network ONLY. Never preview-ingress, never a published port: anything that reaches",
		"    # :3000 directly can send X-WEBAUTH-USER and be anyone. Only Traefik and Loki share it.",
		"    networks: [logs]",
		"    volumes:",
		"      - grafana:/var/lib/grafana      # users, preferences, and the Drilldown plugin download",
		"      - ./grafana/datasources:/etc/grafana/provisioning/datasources:ro",
		"    environment:",
		"      GF_SERVER_DOMAIN: grafana.${DOMAIN}",
		"      GF_SERVER_ROOT_URL: https://grafana.${DOMAIN}/",
		"      # Sign-in: Traefik's forwardAuth asks pstack, and pstack's answer is these two headers.",
		`      GF_AUTH_PROXY_ENABLED: "true"`,
		"      GF_AUTH_PROXY_HEADER_NAME: X-WEBAUTH-USER",
		"      GF_AUTH_PROXY_HEADER_PROPERTY: username",
		"      GF_AUTH_PROXY_HEADERS: Role:X-WEBAUTH-ROLE",
		`      GF_AUTH_PROXY_AUTO_SIGN_UP: "true"`,
		"      # false: no grafana_session of Grafana's own, so pstack decides EVERY request and a pstack",
		"      # sign-out applies on the next one.",
		`      GF_AUTH_PROXY_ENABLE_LOGIN_TOKEN: "false"`,
		"      # Never GF_AUTH_DISABLE_LOGIN: it unregisters the proxy client and silently turns sign-in off.",
		`      GF_AUTH_DISABLE_LOGIN_FORM: "true"`,
		`      GF_AUTH_DISABLE_SIGNOUT_MENU: "true"`,
		`      GF_AUTH_BASIC_ENABLED: "false"`,
		`      GF_AUTH_ANONYMOUS_ENABLED: "false"`,
		`      GF_USERS_ALLOW_SIGN_UP: "false"`,
		`      GF_USERS_ALLOW_ORG_CREATE: "false"`,
		"      GF_USERS_AUTO_ASSIGN_ORG_ROLE: Viewer",
		"      # No built-in `admin` row, so a pstack user named admin is a user, not the server admin.",
		`      GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION: "true"`,
		"      # Previews on *.${DOMAIN} are same-site with Grafana: SameSite does not stop their POSTs.",
		`      GF_SECURITY_CSRF_ALWAYS_CHECK: "true"`,
		`      GF_SECURITY_COOKIE_SECURE: "true"`,
		`      GF_SECURITY_DISABLE_GRAVATAR: "true"`,
		"      # Off: a WebSocket passes forwardAuth once, at the upgrade, and would outlive a pstack sign-out.",
		`      GF_LIVE_MAX_CONNECTIONS: "0"`,
		"      # Off: an Editor could publish log panels to snapshots.raintank.io, outside verify.",
		`      GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"`,
		`      GF_ANALYTICS_REPORTING_ENABLED: "false"`,
		`      GF_ANALYTICS_CHECK_FOR_UPDATES: "false"`,
		`      GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES: "false"`,
		`      GF_NEWS_NEWS_FEED_ENABLED: "false"`,
		`      GF_PLUGINS_PREINSTALL_AUTO_UPDATE: "false"`,
		"      # Default preinstalls with no datasource here. Logs Drilldown (grafana-lokiexplore-app) stays.",
		"      GF_PLUGINS_DISABLE_PLUGINS: grafana-pyroscope-app,grafana-exploretraces-app,grafana-metricsdrilldown-app,grafana-advisor-app",
		"    healthcheck:",
		`      test: ["CMD", "curl", "-fsS", "-o", "/dev/null", "http://localhost:3000/api/health"]`,
		"      start_period: 60s",
		"      interval: 30s",
		"      timeout: 5s",
		"      retries: 3",
		"    labels:",
		"      - traefik.enable=true",
		"      - traefik.docker.network=pstack-control_logs",
		"      - traefik.http.routers.pstack-grafana.rule=Host(`grafana.${DOMAIN}`)",
		"      # A deployment routed to grafana.<domain> before this release has a rule of the same length",
		"      # (autolabel's Host(`…`)), and equal priority is a coin toss for who gets the Grafana cookie.",
		"      - traefik.http.routers.pstack-grafana.priority=10000",
		"      - traefik.http.routers.pstack-grafana.entrypoints=websecure",
		"      # TLS follows the challenge, exactly like pstack-loki: tls=true alone under DNS-01 (the wildcard",
		"      # covers grafana.), plus its own certresolver under HTTP-01.",
		"      - traefik.http.routers.pstack-grafana.tls=true",
	}
	if challenge == HTTP01 {
		lines = append(lines, "      - traefik.http.routers.pstack-grafana.tls.certresolver=le")
	}
	return strings.Join(append(lines,
		"      - traefik.http.routers.pstack-grafana.middlewares=pstack-grafana-auth",
		"      # pstack, by service name, over preview-ingress, the network Traefik already reaches pstack on",
		"      # (the advanced UI's nginx dials the same name). pstack does not join `logs`.",
		"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.address=http://pstack:7878/api/auth/grafana/verify",
		"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.trustForwardHeader=false",
		"      # EVERY header Grafana reads: header_name plus each GF_AUTH_PROXY_HEADERS value. Traefik deletes",
		"      # a listed header from the client's request unconditionally and passes an unlisted one through.",
		"      - traefik.http.middlewares.pstack-grafana-auth.forwardauth.authResponseHeaders=X-WEBAUTH-USER,X-WEBAUTH-ROLE",
		"      - traefik.http.services.pstack-grafana.loadbalancer.server.port=3000",
		"",
	), "\n")
}
```

The render is the spec's block. Under HTTP-01 the block includes `tls.certresolver=le`, right after `tls=true`; under DNS-01 it has no certresolver line. It starts with `\n` (the blank line before the `# Grafana` comment) and ends with `\n`. Each GF_* value the spec quotes is a quoted string, which is why those lines are backtick literals.

- [ ] **Step 8: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run 'TestGrafanaService|TestGrafanaDatasources' -v
```

Expected:

```
--- PASS: TestGrafanaService
    --- PASS: TestGrafanaService/the_http01_block_is_the_design_doc's,_byte_for_byte
    --- PASS: TestGrafanaService/http01_orders_grafana_its_own_certificate,_dns01_inherits_the_wildcard
    --- PASS: TestGrafanaService/the_logs_network_only:_no_preview-ingress,_no_published_port
    --- PASS: TestGrafanaService/proxy_sign-in_stays_registered;_Live_and_external_snapshots_stay_off
    --- PASS: TestGrafanaService/pstack-grafana_outranks_a_deployment_already_routed_to_grafana.<domain>
    --- PASS: TestGrafanaService/Traefik_strips_every_header_Grafana_reads
    --- PASS: TestGrafanaService/logging_off_renders_nothing
--- PASS: TestGrafanaDatasources
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl
```

- [ ] **Step 9: Run every negative control**

For each row: apply the mutation, run the command with that row's `<Test>`, and confirm the listed subtests fail. Then revert. The drafter ran all 12 against a copy of this module. Each failed exactly the subtests listed. Row 8 is also TestGrafanaService's top-level negative control.

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run <Test> -v 2>&1 | grep -- '--- FAIL'
```

| # | Mutation (in `init.go`'s GrafanaService unless noted) | `<Test>` | Subtests that fail |
|---|---|---|---|
| 1 | `if challenge == HTTP01 {` → `if true {` (the certresolver branch) | TestGrafanaService | http01 orders grafana its own certificate… |
| 2 | GrafanaService's `"    networks: [logs]",` (the one after `Only Traefik and Loki share it.",`; LokiService has an identical line) → `"    networks: [logs, preview-ingress]",` | TestGrafanaService | byte for byte; the logs network only… |
| 3 | add ``` `    ports: ["3000:3000"]`, ``` after GrafanaService's networks line (the same line as row 2, not LokiService's) | TestGrafanaService | byte for byte; the logs network only… |
| 4 | delete ``` `      GF_LIVE_MAX_CONNECTIONS: "0"`, ``` | TestGrafanaService | byte for byte; proxy sign-in stays registered… |
| 5 | delete ``` `      GF_SNAPSHOTS_EXTERNAL_ENABLED: "false"`, ``` | TestGrafanaService | byte for byte; proxy sign-in stays registered… |
| 6 | ``` `      GF_LIVE_MAX_CONNECTIONS: "0"` ``` → `"      GF_LIVE_MAX_CONNECTIONS: 0"` | TestGrafanaService | byte for byte; proxy sign-in stays registered… |
| 7 | add ``` `      GF_AUTH_DISABLE_LOGIN: "true"`, ``` after the `GF_AUTH_DISABLE_LOGIN_FORM` line | TestGrafanaService | byte for byte; proxy sign-in stays registered… |
| 8 | delete `"      - traefik.http.routers.pstack-grafana.priority=10000",` | TestGrafanaService | byte for byte; pstack-grafana outranks… |
| 9 | `Role:X-WEBAUTH-ROLE"` → `Role:X-WEBAUTH-ROLE Email:X-WEBAUTH-EMAIL"` (env only) | TestGrafanaService | byte for byte; Traefik strips every header Grafana reads |
| 10 | delete the `if logging != Loki { return "" }` block | TestGrafanaService | logging off renders nothing |
| 11 | `forwardauth.trustForwardHeader=false` → `=true` | TestGrafanaService | byte for byte (only) |
| 12 | `maxLines: 1000` → `maxLines: 500` in `templates/control/grafana/datasources.yaml` | TestGrafanaDatasources | TestGrafanaDatasources |

After reverting, run Step 8's command again. It must pass.

- [ ] **Step 10: Whole package, vet, gofmt**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ && go vet . ./internal/initctl/ && gofmt -l assets.go internal
```

Expected:
- `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl`. TestInitGoldens and TestLokiWiring are included and still pass, because Init is untouched.
- No output from vet or gofmt.

- [ ] **Step 11: docs/control-plane.md — the embed list and count**

In "### Assets are embedded, not read from disk", slice 1 (plan step 10b) leaves these two lines:

```markdown
The web UI, the share page, `templates/control/docker-compose.yml` and the cloud-init template are
`//go:embed`ded (`packages/pstack/assets.go`, seven explicit paths — never a glob, so the READMEs
```

Replace them with:

```markdown
The web UI, the share page, the cloud-init template and the control templates
(`templates/control/docker-compose.yml`, `loki/config.yaml`, `grafana/datasources.yaml`) are
`//go:embed`ded (`packages/pstack/assets.go`, eight explicit paths — never a glob, so the READMEs
```

If the count word is not `seven` (slice 1's docs step did not land as planned), replace both lines anyway. The count is the number `grep -c '^//go:embed' packages/pstack/assets.go` prints, written as a word: `eight`.

- [ ] **Step 12: Commit**

```
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these five files:
- `packages/pstack/templates/control/grafana/datasources.yaml`
- `packages/pstack/assets.go`
- `packages/pstack/internal/initctl/init.go`
- `packages/pstack/internal/initctl/init_test.go`
- `docs/control-plane.md`

Never take `packages/conformance/golden/host/db/*` or anything under `.superpowers/`.

```
but commit -b claude/loki-logging-grafana -m "feat(initctl): render Grafana and its Loki datasource" <id> <id> <id> <id> <id>
```

---

### Task 6: initctl: init --logging loki brings Grafana up

**Files:**
- Modify: `packages/pstack/internal/initctl/init.go`. Edit by the quoted text: slices 1 and 2 have moved the lines, so the line numbers are hints only. The reqs loop `for _, r := range reqs {` is at :299. The loki `ensureDir` block is at :327-331. The substitution comment and line are at :430-433. The `// ── 4. Bring it up` line is at :465. The `  logging   loki at ` append is at :510. LokiWiring's doc comment is at :877-878 and its volume entry at :897.
- Modify: `packages/pstack/templates/control/README.md` (the tree, after the `├── dns.env` line, :25)
- Modify: `docs/usage.md` (§7 "What it does": row `| 0. Preconditions |` at :1313 and row `| 3. Config |` at :1318)
- Modify: `docs/bootstrap.md` (the sizing block, :132)
- Test: `packages/pstack/internal/initctl/init_test.go`
- Test: `packages/pstack/internal/upgrade/upgrade_test.go`

**Interfaces:**
- Consumes:
  - From T5: `initctl.GrafanaVersion = "13.2.1"`, `func GrafanaService(logging Logging, challenge Challenge) string`, and `pstack.GrafanaDatasources string` (assets.go). T6 does not compile without them.
  - From slice 1, already in the tree:
    - `swarm.LokiVersion = "3.7.7"` (swarm.go:746) and `swarm.Shq` (swarm.go:81, single-quotes).
    - `LokiService(logging, challenge, password)`, `LokiWiring(template, logging)`, `ensureDir`/`noMode`/`write` (init.go:800, :888, :943-977).
    - In init_test.go: `render`, `okRunner`, `read`, `substituted` (:29-69, :604-612), plus TestLokiWiring and TestInitLoki.
    - In upgrade_test.go: `lokiControl`, `lokiPassword`, `mustRead` (:75-129).
  - From slice 2: the template mounts `      - ./loki:/etc/loki\n` after pstack's `      - ./docker:/docker-config\n` in every mode (`templates/control/docker-compose.yml`, not a LokiWiring anchor; LokiWiring keeps 3). The loki `config.yaml` write becomes create-if-absent.
- Produces:
  - Init's substitution becomes `template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui)+LokiService(logging, challenge, lokiPassword)+GrafanaService(logging, challenge), 1)`.
  - LokiWiring's volume entry becomes `{"letsencrypt volume", "volumes:\n  letsencrypt:\n", "volumes:\n  letsencrypt:\n  loki:\n  grafana:\n", identity}`.
  - With `logging == Loki`, two reqs:
    - `requires loki image` asserts `docker image inspect 'grafana/loki:3.7.7' >/dev/null 2>&1 || docker pull -q 'grafana/loki:3.7.7' >/dev/null`.
    - `requires grafana image` asserts the same for `'grafana/grafana:13.2.1'`.
    - The hint is `<img> could not be pulled — --logging loki needs Docker Hub from this host`.
    - A dry run prints `  [dry-run] requires loki image` and `  [dry-run] requires grafana image`.
  - With `logging == Loki`: `<DATA>/control/grafana/datasources/` (ensureDir, noMode) and `<DATA>/control/grafana/datasources/loki.yaml` (== `pstack.GrafanaDatasources`, 0644, always overwritten, written just before `── 4.`).
  - The summary line `  grafana   https://grafana.<domain>`, right after the `  logging   loki at …` line.
  - The test helper `func serviceBlock(yaml, key string) string` in init_test.go.

- [ ] **Step 1: Write the failing tests**

In `packages/pstack/internal/initctl/init_test.go`, make these edits by quoted text.

1a. Update `substituted`'s doc comment. Replace the text `where Init appends LokiService.` with `where Init appends LokiService and GrafanaService.`

1b. Add the helper directly above the line `// The loki service. Its TLS labels are the part that varies by host, and getting them backwards is`:

```go
// serviceBlock is one service's text in a rendered compose file: its "  <key>:" line through the last
// line before the next non-blank line indented fewer than four spaces (the next service, its header
// comment, or a top-level key), trailing newlines trimmed. Not traefikBlock's rule: with logging off,
// pstack's block is followed by the empty advanced-UI marker line and `volumes:` at column 0, which that
// rule walks straight through.
func serviceBlock(yaml, key string) string {
	start := strings.Index(yaml, "\n  "+key+":\n")
	if start < 0 {
		return ""
	}
	rest := yaml[start+1:]
	lines := strings.SplitAfter(rest, "\n")
	n := len(lines[0])
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "    ") {
			break
		}
		n += len(l)
	}
	return strings.TrimRight(rest[:n], "\n")
}
```

1c. In TestLokiWiring's "each edit lands once" subtest, make four edits.

Replace:
```go
			// negative control: strings.ReplaceAll for the networks anchor in LokiWiring — advanced-ui joins logs too.
```
with:
```go
			// negative control: strings.ReplaceAll for the networks anchor in LokiWiring — advanced-ui joins logs too.
			// negative control: leave "  grafana:\n" out of LokiWiring's letsencrypt-volume `with` — the volumes edit appears 0 times and volumes has no grafana.
```

Replace:
```go
			got, err := initctl.LokiWiring(substituted(c, initctl.Advanced, initctl.LokiService(initctl.Loki, c, pw)), initctl.Loki)
```
with:
```go
			got, err := initctl.LokiWiring(substituted(c, initctl.Advanced, initctl.LokiService(initctl.Loki, c, pw)+initctl.GrafanaService(initctl.Loki, c)), initctl.Loki)
```

Replace:
```go
				"volumes:\n  letsencrypt:\n  loki:\n",
```
with:
```go
				"volumes:\n  letsencrypt:\n  loki:\n  grafana:\n",
```

Replace:
```go
			if svcs.GetMap("loki") == nil || !d.GetMap("volumes").Has("loki") || !d.GetMap("networks").Has("logs") {
				t.Errorf("%s: the loki service, volume or network is missing", c)
			}
```
with:
```go
			if svcs.GetMap("loki") == nil || !d.GetMap("volumes").Has("loki") || !d.GetMap("networks").Has("logs") {
				t.Errorf("%s: the loki service, volume or network is missing", c)
			}
			if svcs.GetMap("grafana") == nil || !d.GetMap("volumes").Has("grafana") {
				t.Errorf("%s: the grafana service or volume is missing", c)
			}
			// Anything that reaches :3000 can send X-WEBAUTH-USER, so only Traefik (and Loki) may share it.
			if n := fmt.Sprint(svcs.GetMap("grafana").GetSlice("networks")); n != "[logs]" {
				t.Errorf("%s: grafana networks %s", c, n)
			}
```

1d. Insert a new TestLokiWiring subtest directly above `	t.Run("only Traefik's own copy of the networks line counts, not the advanced UI's identical one", func(t *testing.T) {`:

```go
	t.Run("Grafana adds nothing to pstack's service block", func(t *testing.T) {
		// pstack finds Grafana from docker (inspect.GrafanaOn), so Grafana needs nothing here. Slice 2's
		// ./loki mount is in both modes, so the switch leaves pstack's block, and pstack, alone.
		// negative control: append {"pstack environment", "      DOCKER_CONFIG: /docker-config\n", "      DOCKER_CONFIG: /docker-config\n      PSTACK_GRAFANA: \"on\"\n", identity} to LokiWiring's entries — the blocks differ.
		t.Setenv("PSTACK_LOKI_PASSWORD", pw)
		_, off := render(t, nil)
		_, on := render(t, func(o *initctl.Options) { o.Logging = initctl.Loki })
		if got, want := serviceBlock(on, "pstack"), serviceBlock(off, "pstack"); got == "" || got != want {
			t.Errorf("pstack's block differs with loki on\n--- on\n%s\n--- off\n%s", got, want)
		}
	})
```

1e. In TestInitLoki, insert a new subtest directly above the comment line `	// R5 amends the plan: LOKI_PUSH_PASSWORD survives `pstack logging off` (spec 291 — `pstack upgrade``:

```go
	t.Run("Grafana's datasource, images and summary line come with loki", func(t *testing.T) {
		// negative control: drop the grafana/datasources/loki.yaml write from Init — read fails, no such file.
		// negative control: drop the `if logging == Loki` image reqs block from Init — no grafana/grafana pull runs.
		// negative control: drop the `  grafana   https://grafana.` append from Init — the summary check fails.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		r := okRunner("inactive", "")
		var out bytes.Buffer
		dir, _ := render(t, loki(r, &out))
		p := filepath.Join(dir, "control", "grafana", "datasources", "loki.yaml")
		if got := read(t, p); got != pstack.GrafanaDatasources {
			t.Errorf("loki.yaml differs from pstack.GrafanaDatasources:\n%s", got)
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("mode %o, want 644 (Grafana runs as uid 472)", st.Mode().Perm())
		}
		// Pulled by name before up: a pull that fails inside `up` takes the whole control stack down.
		grafana, lokiImage, up := -1, -1, -1
		for i, c := range r.Commands() {
			switch {
			case strings.Contains(c, "docker pull -q 'grafana/grafana:13.2.1'"):
				grafana = i
			case strings.Contains(c, "docker pull -q 'grafana/loki:3.7.7'"):
				lokiImage = i
			case strings.Contains(c, "-p pstack-control") && strings.HasSuffix(c, " up -d --remove-orphans"):
				up = i
			}
		}
		if grafana < 0 || lokiImage < 0 || up < 0 || grafana > up || lokiImage > up {
			t.Errorf("grafana pull at %d, loki pull at %d, up at %d:\n%s", grafana, lokiImage, up, strings.Join(r.Commands(), "\n"))
		}
		if !strings.Contains(out.String(), "  grafana   https://grafana.preview.example.com\n") {
			t.Errorf("no grafana summary line:\n%s", out.String())
		}
	})
```

1f. Extend the existing logging-off subtest. Keep its name: `t.TempDir()` folds the subtest name into the printed paths (:1011-1012), so a name containing "grafana" would trip the summary check. Make four edits.

Replace:
```go
		// negative control: re-gate `os.Getenv("PSTACK_LOKI_PASSWORD")` under `if logging == Loki` (pre-R5) — the .env line disappears.
```
with:
```go
		// negative control: re-gate `os.Getenv("PSTACK_LOKI_PASSWORD")` under `if logging == Loki` (pre-R5) — the .env line disappears.
		// negative control: move the grafana/datasources ensureDir out of `if logging == Loki` — control/grafana exists.
		// negative control: remove the `if logging == Loki` around the loki/grafana image reqs — a grafana/ image command runs.
		// negative control: append the `  grafana   ` summary line outside `if logging == Loki` — the summary names Grafana.
```

Replace:
```go
			t.Errorf("control/loki exists: %v", err)
		}
```
with:
```go
			t.Errorf("control/loki exists: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "control", "grafana")); !os.IsNotExist(err) {
			t.Errorf("control/grafana exists: %v", err)
		}
```

Replace:
```go
		if strings.Contains(yaml, "loki") {
			t.Error("compose mentions loki")
		}
		for _, c := range r.Commands() {
			if strings.Contains(c, "docker plugin") {
				t.Errorf("plugin command with logging off: %s", c)
			}
		}
```
with:
```go
		if strings.Contains(yaml, "loki") || strings.Contains(yaml, "grafana") {
			t.Error("compose mentions loki or grafana")
		}
		for _, c := range r.Commands() {
			if strings.Contains(c, "docker plugin") || strings.Contains(c, "grafana/") {
				t.Errorf("plugin or image command with logging off: %s", c)
			}
		}
```

Replace:
```go
			t.Errorf("summary mentions the loki logging line:\n%s", out.String())
		}
```
with:
```go
			t.Errorf("summary mentions the loki logging line:\n%s", out.String())
		}
		if strings.Contains(out.String(), "  grafana   ") {
			t.Errorf("summary names Grafana:\n%s", out.String())
		}
```

1g. Extend the existing dry-run subtest. It uses the real runner, so it sees the `[dry-run] <label>` lines (exec.go:118-119). Make two edits.

Replace:
```go
		// negative control: wrap the `── 1c.` block in `if !dryRun` like the health wait — `[dry-run] loki log plugin` is missing.
```
with:
```go
		// negative control: wrap the `── 1c.` block in `if !dryRun` like the health wait — `[dry-run] loki log plugin` is missing.
		// negative control: drop the grafana/datasources ensureDir from Init — the mkdir line is missing.
```

Replace:
```go
			"  [dry-run] loki log plugin\n",
```
with:
```go
			"  [dry-run] loki log plugin\n",
			"  [dry-run] requires loki image\n",
			"  [dry-run] requires grafana image\n",
			"  [dry-run] mkdir -p " + filepath.Join(dir, "control", "grafana", "datasources") + "\n",
			"  [dry-run] write " + filepath.Join(dir, "control", "grafana", "datasources", "loki.yaml") + " (",
```

1h. In `packages/pstack/internal/upgrade/upgrade_test.go`, insert into TestUpgrade directly above `	t.Run("a logging-off host reads none, is not refused for having no password, and plans no --logging", func(t *testing.T) {`. `os`, `filepath`, `strings` and `initctl` are already imported (upgrade_test.go:4-17).

```go
	t.Run("a loki host's compose carries Grafana and still reads loki", func(t *testing.T) {
		// lokiControl is the real init, so this is the file an upgraded host has. lokiSvc (`^\s{2}loki:`)
		// cannot match Grafana's block: it has no two-space `loki:` line.
		// negative control: drop `+GrafanaService(logging, challenge)` from Init's #__ADVANCED_UI_SERVICE__ substitution — the compose has no grafana service.
		dataDir := lokiControl(t, lokiPassword)
		b, err := os.ReadFile(filepath.Join(dataDir, "control", "docker-compose.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "\n  grafana:\n    image: grafana/grafana:13.2.1") {
			t.Fatalf("compose has no grafana service:\n%s", b)
		}
		if s := mustRead(t, dataDir); s.Logging != initctl.Loki {
			t.Errorf("logging = %q", s.Logging)
		}
	})
```

- [ ] **Step 2: Run the tests and watch them fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run 'TestLokiWiring|TestInitLoki'
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run TestUpgrade
```

Expected results:
- **TestLokiWiring, "each edit lands once":** FAIL for http01 and dns01 with `"volumes:\n  letsencrypt:\n  loki:\n  grafana:\n" appears 0 times` and `the grafana service or volume is missing`.
- **TestLokiWiring, "Grafana adds nothing to pstack's service block":** PASS. It is a guard, true before and after this task, and only its negative control (Step 5) shows it can fail.
- **TestInitLoki, "Grafana's datasource, images and summary line come with loki":** FAIL with `open …/control/grafana/datasources/loki.yaml: no such file or directory`.
- **TestInitLoki, the dry-run subtest:** FAIL with `missing "  [dry-run] requires loki image\n"`, and the same for `requires grafana image`, the `mkdir -p …/grafana/datasources` line and the `write …/loki.yaml (` line.
- **TestInitLoki, the logging-off subtest:** PASS (guards).
- **TestUpgrade, "a loki host's compose carries Grafana and still reads loki":** FAIL with `compose has no grafana service`.
- If the build fails on `GrafanaService` or `GrafanaDatasources`, T5 has not landed yet. Stop.

- [ ] **Step 3: Implement**

All edits are in `packages/pstack/internal/initctl/init.go`.

3a. The image preconditions. Replace:
```go
	for _, r := range reqs {
		res := runner.Run(r.assert, exec.RunOptions{Label: "requires " + r.name})
```
with:
```go
	// With --logging loki, both images by name, for the advanced UI's reason above. They come from Docker
	// Hub, so a missing one is pulled here, where a failure names it, not inside `up`.
	if logging == Loki {
		lokiImage, grafanaImage := "grafana/loki:"+swarm.LokiVersion, "grafana/grafana:"+GrafanaVersion
		reqs = append(reqs,
			req{name: "loki image", assert: "docker image inspect " + swarm.Shq(lokiImage) + " >/dev/null 2>&1 || docker pull -q " + swarm.Shq(lokiImage) + " >/dev/null",
				hint: lokiImage + " could not be pulled — --logging loki needs Docker Hub from this host"},
			req{name: "grafana image", assert: "docker image inspect " + swarm.Shq(grafanaImage) + " >/dev/null 2>&1 || docker pull -q " + swarm.Shq(grafanaImage) + " >/dev/null",
				hint: grafanaImage + " could not be pulled — --logging loki needs Docker Hub from this host"},
		)
	}
	for _, r := range reqs {
		res := runner.Run(r.assert, exec.RunOptions{Label: "requires " + r.name})
```

3b. The datasource directory. Replace:
```go
	if logging == Loki {
		if err := ensureDir(out, filepath.Join(controlDir, "loki"), dryRun, noMode); err != nil {
			return err
		}
	}
```
with:
```go
	if logging == Loki {
		if err := ensureDir(out, filepath.Join(controlDir, "loki"), dryRun, noMode); err != nil {
			return err
		}
		// Grafana's provisioned datasources, a directory mount for the same reason. Grafana runs as uid 472.
		if err := ensureDir(out, filepath.Join(controlDir, "grafana", "datasources"), dryRun, noMode); err != nil {
			return err
		}
	}
```

3c. The substitution and its comment. Replace:
```go
	// Loki rides on the last marker instead of adding one: every marker leaves a line behind when off, so
```
with:
```go
	// Loki and Grafana ride on the last marker instead of adding one: every marker leaves a line behind when off, so
```
Then replace:
```go
	template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui)+LokiService(logging, challenge, lokiPassword), 1)
```
with:
```go
	template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui)+LokiService(logging, challenge, lokiPassword)+GrafanaService(logging, challenge), 1)
```

3d. The datasource write, as its own block right before step 4. Slice 2 rewrites the loki config write above it, so it is not used as the anchor. Replace:
```go
	// ── 4. Bring it up ──────────────────────────────────────────────────────────────────────────
```
with:
```go
	// Grafana's Loki datasource. 0644 like Loki's config: Grafana runs as uid 472, and the file holds no
	// credential. Always written: nothing else owns it.
	if logging == Loki {
		if err := write(out, filepath.Join(controlDir, "grafana", "datasources", "loki.yaml"), pstack.GrafanaDatasources, 0o644, dryRun); err != nil {
			return err
		}
	}

	// ── 4. Bring it up ──────────────────────────────────────────────────────────────────────────
```

3e. The summary line. Replace:
```go
		lines = append(lines, "  logging   loki at https://loki."+domain+" (push only); services without `logging:` ship to it")
```
with:
```go
		lines = append(lines, "  logging   loki at https://loki."+domain+" (push only); services without `logging:` ship to it")
		lines = append(lines, "  grafana   https://grafana."+domain)
```
If slice 2 changed the logging line's wording, keep its wording and add the grafana append right after it, inside the same `if logging == Loki {`.

3f. The volume anchor and LokiWiring's doc comment. Replace:
```go
		{"letsencrypt volume", "volumes:\n  letsencrypt:\n", "volumes:\n  letsencrypt:\n  loki:\n", identity},
```
with:
```go
		{"letsencrypt volume", "volumes:\n  letsencrypt:\n", "volumes:\n  letsencrypt:\n  loki:\n  grafana:\n", identity},
```
In LokiWiring's doc comment, replace the text ``the `loki` volume`` with ``the `loki` and `grafana` volumes``.

- [ ] **Step 4: Run the tests and watch them pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ ./internal/upgrade/
cd /Volumes/S1/code/preview-stacks/packages/pstack && go vet ./internal/initctl/ ./internal/upgrade/ && gofmt -l internal
```

Expected results:
- `ok  	…/internal/initctl` and `ok  	…/internal/upgrade`.
- This includes TestInitGoldens: logging off is still byte-identical for all 8 cells, because every addition sits under `logging == Loki`. It also includes TestUpgradeGoldens.
- `go vet` prints nothing and `gofmt -l internal` prints nothing.
- For T11: the dry run prints `  [dry-run] write <DATA>/control/grafana/datasources/loki.yaml (<n> bytes, mode 644)`, with `<n>` derived from the spec block (377 bytes if T5 is byte-exact), not pinned by any test.

- [ ] **Step 5: Run every negative control**

Apply each mutation alone, run the command, confirm the named subtest FAILS, then revert. Use `go test -overlay <json>` with a mutated copy in the scratchpad, as `.superpowers/sdd/…/progress.md` Task 1 did. That leaves the tree untouched. Otherwise edit and revert, and check that `but diff` shows only Step 3's hunks.

| # | Mutation (init.go) | Command `-run` | Fails |
|---|---|---|---|
| 1 | volume `with` without `  grafana:\n` | `TestLokiWiring` | each edit lands once |
| 2 | append `{"pstack environment", "      DOCKER_CONFIG: /docker-config\n", "      DOCKER_CONFIG: /docker-config\n      PSTACK_GRAFANA: \"on\"\n", identity}` to LokiWiring's entries | `TestLokiWiring` | Grafana adds nothing to pstack's service block |
| 3 | delete the 3d datasource write | `TestInitLoki` | Grafana's datasource, images and summary line (no such file) |
| 4 | delete the 3a `if logging == Loki {…}` reqs block | `TestInitLoki` | Grafana's datasource… (pull at -1) |
| 5 | delete the 3e grafana append | `TestInitLoki` | Grafana's datasource… (no summary line) |
| 6 | move 3b's grafana ensureDir below the closing `}` of `if logging == Loki` | `TestInitLoki` | logging off… (control/grafana exists) |
| 7 | drop only the `if logging == Loki {` / `}` lines around 3a's append | `TestInitLoki` | logging off… (image command), and TestInitGoldens |
| 8 | move 3e's grafana append outside `if logging == Loki` | `TestInitLoki` | logging off… (summary names Grafana) |
| 9 | delete 3b's grafana ensureDir | `TestInitLoki` | dry-run… (mkdir line missing), and Grafana's datasource… (write fails) |
| 10 | drop `+GrafanaService(logging, challenge)` from 3c | `TestUpgrade` in `./internal/upgrade/` | a loki host's compose carries Grafana |

Command form: `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run '<Name>'`. Once everything is reverted, re-run Step 4 and confirm it is green.

- [ ] **Step 6: Docs**

6a. In `packages/pstack/templates/control/README.md`, insert after the line `    ├── dns.env          0600    the DNS-01 credential, under the variable name lego expects`:
```
    ├── grafana/datasources/loki.yaml 0644  Grafana's Loki datasource (only with --logging loki)
```

6b. In `docs/usage.md` §7 "What it does", row `| 0. Preconditions |`, replace:
```
and the control image present. Fails by name,
```
with:
```
and the control image present; with `--logging loki`, `grafana/loki` and `grafana/grafana`, pulled if missing. Fails by name,
```

6c. In `docs/usage.md` row `| 3. Config |`, replace the row's ending:
```
(`PSTACK_LOKI_PASSWORD`, or 32 generated hex characters) |
```
with:
```
(`PSTACK_LOKI_PASSWORD`, or 32 generated hex characters), and the `grafana` service with `control/grafana/datasources/loki.yaml` (`0644`) |
```
If slice 2 reworded that ending, add the same clause just before the row's final ` |`.

6d. In `docs/bootstrap.md`, replace:
```
             + control stack (Traefik 256 MB + pstack 512 MB, both capped in the template)
```
with:
```
             + control stack (Traefik 512 MB + pstack 512 MB, + advanced UI 128 MB; with --logging loki + Loki 2 GB + Grafana 768 MB, all capped in the template)
```
The new numbers match the template: `mem_limit: 512m` at docker-compose.yml:36 and :108, `128m` at init.go:778, `2g` at init.go:812, and T5's `768m`.

- [ ] **Step 7: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these files:
- `packages/pstack/internal/initctl/init.go`
- `packages/pstack/internal/initctl/init_test.go`
- `packages/pstack/internal/upgrade/upgrade_test.go`
- `packages/pstack/templates/control/README.md`
- `docs/usage.md`
- `docs/bootstrap.md`

Never take `packages/conformance/golden/host/db/*` or anything under `.superpowers/`. Then:

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-grafana -m "feat(initctl): init --logging loki brings Grafana up" <ids>
```

---

### Task 7: api: Grafana discovery cache and the health key

**Files:**
- Create: `packages/pstack/internal/api/routes_grafana.go`
- Modify: `packages/pstack/internal/api/server.go`. Edit by the quoted text: the imports (`"sync"` at :21), the `spec *openAPIDoc` paragraph (:154-155), `Start` (:801-802) and `reindexLoop` (:822-823). Line numbers are hints only, because slice 2 adds `Options.LokiDir`, `lokiMu` and a `reconcileLoki` call in `New`.
- Modify: `packages/pstack/internal/api/routes_auth.go` (`health`, :22-43; neither slice 1 nor slice 2 touches it)
- Modify: `packages/client/src/types.ts` (`Health`, :56-69)
- Modify: `docs/usage.md` (the HTTP API table's `/api/health` row, :3211)
- Test: `packages/pstack/internal/api/routes_grafana_test.go` (create)

**Interfaces:**
- Consumes:
  - `func inspect.GrafanaOn(r exec.Runner) bool` (T4).
  - `server.go:29` already imports `inspect`, and `s.host exec.Runner` is set in `New` (:257).
  - `func New(o Options) (*Server, error)` and `(*Server).Stop()`. An unstarted server is safe to stop: `Stop` checks `s.http != nil`, and TestMaxJobsOptionReachesTheRegistry (http_test.go:498-514) already does this.
  - `func mustParseObject(t *testing.T, s string) any` (http_test.go:1185, same package).
  - `(*omap.Map).Has`, `GetString` and `Keys` (omap.go:48, :158, :85).
  - `jsonx.Object` / `jsonx.KV{K, V}` / `jsonx.O` (jsonx.go:94-145). `Object` is `[]KV`, so `append` works on it.
  - `events.New()`.
- Produces:
  - `grafana atomic.Bool`, a field of `type Server`. Written by `Start` (synchronously, before `Serve`) and on every `reindexLoop` tick.
  - `func (s *Server) grafanaOn() bool { return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load() }` in routes_grafana.go.
  - `func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }` in routes_grafana.go.
  - `/api/health` appends `jsonx.KV{K: "grafana", V: s.grafanaURL()}` last, only when `grafanaOn()`. When off the key is absent, never null.
  - The test helper `func grafanaServer(t *testing.T) *Server`, for T8 and T9.
  - The test helper `func healthBody(t *testing.T, s *Server) *omap.Map`.
  - In `packages/client/src/types.ts`, `Health.grafana?: string`.

Not in this task:
- `apps/ui/src/api/types.ts:122-132` `Health` is a hand-typed, deliberately partial mirror: it already leaves out `hasUsers` and `sso`. Leave it alone. T10 reads the key through `useAuth`'s own generic.
- routes_grafana.go has no route literals yet, so `permissions_test.go`'s route-source scan and `openapi_coverage_test.go` see nothing new, and the full package stays green.
- `golden/host/expected/health.json` does not change. The host fixture's control `ps -aq` arm (`packages/conformance/harness/host-fixture.ts:38-39`) lists only the traefik container `cccc11cccc11`, so `GrafanaOn` is false there.

- [ ] **Step 1: Write the failing test**

Create `packages/pstack/internal/api/routes_grafana_test.go`:

```go
package api

import (
	"net/http/httptest"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// grafanaServer is a real New() server with a domain and a root token. Nothing listens. Not
// ssoTestServer: handle() dereferences routing and sleepIndex, and that one leaves both nil. Never
// Start it: Start runs the docker probe, so tests set s.grafana directly.
func grafanaServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Domain: "preview.example.com", Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// healthBody is GET /api/health through handle(), decoded as the ordered map a client sees.
func healthBody(t *testing.T, s *Server) *omap.Map {
	t.Helper()
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest("GET", "/api/health", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return mustParseObject(t, w.Body.String()).(*omap.Map)
}

// The UI finds Grafana through /api/health. When off the key is ABSENT: Has() is the check, because
// a null is still a present key (Go rule 2). Every host without Grafana answers byte-identically.
func TestHealthNamesGrafanaOnlyWhenOn(t *testing.T) {
	// negative control: `if s.grafanaOn() {` → `if true {` in health — the off, no-token and
	// no-domain subtests fail.
	t.Run("off: no grafana key", func(t *testing.T) {
		// negative control: `if s.grafanaOn() {` → `if true {` in health — Has("grafana") is true.
		s := grafanaServer(t)
		if b := healthBody(t, s); b.Has("grafana") {
			v, _ := b.Get("grafana")
			t.Fatalf("grafana is off, got grafana=%v", v)
		}
	})
	t.Run("on: the configured domain's URL, last", func(t *testing.T) {
		// negative control: delete the `body = append(body, jsonx.KV{K: "grafana", …})` line — got "";
		// and, run separately, prepend it (`body = append(jsonx.Object{{K: "grafana", V: s.grafanaURL()}}, body...)`) — the last key is "version".
		s := grafanaServer(t)
		s.grafana.Store(true)
		b := healthBody(t, s)
		if got := b.GetString("grafana"); got != "https://grafana.preview.example.com" {
			t.Fatalf("grafana = %q, want https://grafana.preview.example.com", got)
		}
		if keys := b.Keys(); keys[len(keys)-1] != "grafana" {
			t.Fatalf("grafana goes after version, got keys %v", keys)
		}
	})
	t.Run("no root token: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Token != "" &&` from grafanaOn — the key is present.
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Token = ""
		if healthBody(t, s).Has("grafana") {
			t.Fatal("no PSTACK_TOKEN means no sign-in, so no grafana key")
		}
	})
	t.Run("no domain: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Domain != "" &&` from grafanaOn — the key is "https://grafana.".
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Domain = ""
		if b := healthBody(t, s); b.Has("grafana") {
			t.Fatalf("no PSTACK_DOMAIN, got grafana=%q", b.GetString("grafana"))
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestHealthNamesGrafanaOnlyWhenOn
```

Expected: the build fails, with a line like
`internal/api/routes_grafana_test.go:52:5: s.grafana undefined (type *Server has no field or method grafana)`
followed by `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/api [build failed]`.

- [ ] **Step 3: Implement**

**3a. `packages/pstack/internal/api/server.go` imports.** Replace:

```go
	"sync"
	"time"
```

with:

```go
	"sync"
	"sync/atomic"
	"time"
```

(go.mod:3 is `go 1.23.5`, and `atomic.Bool` needs 1.19.)

**3b. The `Server` struct.** Add the field as its own paragraph after the `spec` paragraph. A separate paragraph keeps gofmt's alignment of the big field block unchanged, wherever slice 2 put `lokiMu`. Replace:

```go
	// spec is the OpenAPI document and its JSON conversion, the latter computed on first request.
	spec *openAPIDoc
```

with:

```go
	// spec is the OpenAPI document and its JSON conversion, the latter computed on first request.
	spec *openAPIDoc

	// grafana: the control project runs a grafana container (inspect.GrafanaOn). It comes from docker,
	// never a setting. Writers are Start (once, before Serve) and reindexLoop (every tick). Request
	// goroutines read it, and atomic means no mutex.
	grafana atomic.Bool
```

**3c. `Start`.** Replace:

```go
	s.scheduler.Start()
	go s.reindexLoop()
```

with:

```go
	s.scheduler.Start()
	// Before Serve, so the first /api/health is already right.
	s.grafana.Store(inspect.GrafanaOn(s.host))
	go s.reindexLoop()
```

**3d. `reindexLoop`.** Replace:

```go
		case <-t.C:
			s.reindex()
```

with:

```go
		case <-t.C:
			s.reindex()
			// Not inside reindex(): request paths call that, and this is two docker calls. Still needed
			// after Start: compose may start pstack before it creates the grafana container.
			s.grafana.Store(inspect.GrafanaOn(s.host))
```

**3e. Create `packages/pstack/internal/api/routes_grafana.go`.** It has no imports yet; T8 adds them and the sign-in code:

```go
// Grafana at G = https://grafana.<domain> (`pstack init --logging loki`), signed in with pstack
// accounts. Spec of record: docs/loki-logging-design.md, "Slice 3".
//
// ── IS IT ON ─────────────────────────────────────────────────────────────────────────────────────
//
// Read from docker (inspect.GrafanaOn) and cached in s.grafana by Start and reindexLoop. It is never a
// setting and never asked per request, so a `pstack logging` switch shows within 30 s of pstack's
// restart (compose may create grafana after pstack). Sign-in also needs PSTACK_DOMAIN (every URL
// here is built from config, never from a request header) and PSTACK_TOKEN (the cookie's MAC key).
// When off, /api/health has no `grafana` key.
package api

// grafanaOn: this host runs Grafana (s.grafana, from docker) and has what sign-in needs.
func (s *Server) grafanaOn() bool {
	return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()
}

// grafanaURL is G. From config, never from a request header.
func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }
```

**3f. `packages/pstack/internal/api/routes_auth.go` `health`.** Replace:

```go
	writeJSON(w, 200, jsonx.O(
		"ok", true,
		"authEnforced", s.opts.Token != "",
		"hasUsers", n > 0,
		"sso", ssoSummary,
		"dataDir", s.opts.DataDir,
		"version", s.opts.Version,
	))
}
```

with:

```go
	body := jsonx.O(
		"ok", true,
		"authEnforced", s.opts.Token != "",
		"hasUsers", n > 0,
		"sso", ssoSummary,
		"dataDir", s.opts.DataDir,
		"version", s.opts.Version,
	)
	// Last, and only when Grafana runs. Otherwise the key is absent (Go rule 2), so every other host's
	// answer stays byte-identical. Unauthenticated like the rest of health: it says nothing DNS doesn't.
	if s.grafanaOn() {
		body = append(body, jsonx.KV{K: "grafana", V: s.grafanaURL()})
	}
	writeJSON(w, 200, body)
}
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestHealthNamesGrafanaOnlyWhenOn -v
```

Expected:

```
--- PASS: TestHealthNamesGrafanaOnlyWhenOn (…)
    --- PASS: TestHealthNamesGrafanaOnlyWhenOn/off:_no_grafana_key (…)
    --- PASS: TestHealthNamesGrafanaOnlyWhenOn/on:_the_configured_domain's_URL,_last (…)
    --- PASS: TestHealthNamesGrafanaOnlyWhenOn/no_root_token:_no_grafana_key (…)
    --- PASS: TestHealthNamesGrafanaOnlyWhenOn/no_domain:_no_grafana_key (…)
PASS
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api
```

- [ ] **Step 5: Run every negative control**

Apply each mutation on its own, rerun the Step 4 command, confirm it fails as shown, then restore the code.

| Mutation | Subtests that must fail |
|---|---|
| In `health`, `if s.grafanaOn() {` → `if true {` | `off` fails with `grafana is off, got grafana=https://grafana.preview.example.com`; `no root token` and `no domain` fail too |
| Delete `body = append(body, jsonx.KV{K: "grafana", V: s.grafanaURL()})` | `on` fails with `grafana = "", want https://grafana.preview.example.com` |
| Replace that line with `body = append(jsonx.Object{{K: "grafana", V: s.grafanaURL()}}, body...)` | `on` fails with `grafana goes after version, got keys [grafana ok …]` |
| In `grafanaOn`, drop `s.opts.Token != "" &&` | `no root token` fails |
| In `grafanaOn`, drop `s.opts.Domain != "" &&` | `no domain` fails with `got grafana="https://grafana."` |

After restoring, run the Step 4 command again: PASS.

- [ ] **Step 6: The whole package, vet, gofmt**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ && go vet ./internal/api/ && gofmt -l internal
```

Expected:
- `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`.
- `go vet` prints nothing. `atomic.Bool` inside `Server` is fine for copylocks: `Server` already holds `sync.Mutex` and is only used through `*Server`.
- `gofmt -l` prints nothing. If it lists server.go (slice 2's `lokiMu` sits right next to the new paragraph), run `gofmt -w internal/api/server.go` and rerun.

There is no Go unit test for the two `Store` calls in `Start` and `reindexLoop`: `Start` runs the docker probe (C8), and the tick is 30 s. T11 test 4 proves the `Start` store black-box, because a GRAFANA_SHIM server's health has the key only if `Start` stored true. Real-host check 9 proves the tick.

- [ ] **Step 7: The host health golden is unchanged, with `Start` now probing docker**

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts test/harness.test.ts
```

Expected:
- Both files pass. The fixture's control `ps -aq` arm answers only the traefik container, so `health.json` has no `grafana` key.
- Do not regenerate anything.
- `but diff` shows no `golden/host/expected/*` change.
- The untracked `golden/host/db/pstack.db-shm` / `-wal` stay uncommitted.

- [ ] **Step 8: Client type**

In `packages/client/src/types.ts`, replace:

```ts
  dataDir: string;
  version: string;
};

export type DeploymentRow = {
```

with:

```ts
  dataDir: string;
  version: string;
  /** `https://grafana.<domain>`, present only on a host running Grafana. */
  grafana?: string;
};

export type DeploymentRow = {
```

```bash
cd /Volumes/S1/code/preview-stacks/packages/client && bun run check
```

Expected: `bun test` passes, `tsc --noEmit` is clean and the build succeeds. The build writes `dist/`, which `.gitignore:4` ignores, so it adds nothing to `but status`.

- [ ] **Step 9: Docs**

In `docs/usage.md`, in the `### HTTP API` table (:3211), replace:

```
| GET | `/api/health` | — | `{ ok, authEnforced, hasUsers, sso, dataDir, version }` — `sso` is `{ providers: [{ key, label, preset }…] }` (enabled providers, in key order) or `null`, read by the login page before authenticating |
```

with:

```
| GET | `/api/health` | — | `{ ok, authEnforced, hasUsers, sso, dataDir, version, grafana? }` — `sso` is `{ providers: [{ key, label, preset }…] }` (enabled providers, in key order) or `null`, read by the login page before authenticating; `grafana` is `https://grafana.<domain>`, present only on a host running Grafana |
```

Leave these alone:
- The 7e matrix row `| \`GET /api/health\` | none — it is how \`init\` waits for the container |` (:2651).
- The `curl …/api/health` example (:776-783). A host without Grafana is still a correct example.

- [ ] **Step 10: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these six files:
- `packages/pstack/internal/api/routes_grafana.go`
- `packages/pstack/internal/api/routes_grafana_test.go`
- `packages/pstack/internal/api/server.go`
- `packages/pstack/internal/api/routes_auth.go`
- `packages/client/src/types.ts`
- `docs/usage.md`

Don't take `packages/pstack/internal/upgrade/*`, `packages/conformance/golden/host/db/*`, anything under `.superpowers/`, or anything else another agent left uncommitted.

```bash
but commit -b claude/loki-logging-grafana -m "feat(api): /api/health names Grafana when it runs" <routes_grafana.go id> <routes_grafana_test.go id> <server.go id> <routes_auth.go id> <types.ts id> <usage.md id>
```

No attribution footer.

---

### Task 8: api: Grafana sign-in — forwardAuth verify, start, callback

**Files:**
- Modify: `packages/pstack/internal/api/routes_grafana.go` (T7 created it holding only the header, `grafanaOn` and `grafanaURL`. Anchor on `package api` and on T7's one-liner `func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }`.)
- Modify: `packages/pstack/internal/api/routes_auth.go` (preGate, after the `/api/auth/sso/callback` case, :129-132)
- Modify: `packages/pstack/internal/api/server.go` (header :5-9; the `// Pre-gate:` comment in `handle()`, :907-908)
- Modify: `packages/pstack/internal/api/openapi_coverage_test.go` (header :9-12; `notInTheSpec`, :24-44)
- Modify: `packages/pstack/internal/api/permissions_test.go` (`preGatePaths`, :197-203)
- Modify: `docs/control-plane.md` (new `## 5h. Grafana sign-in` directly before `## 6. Submitting a deployment`, which lands after slice 1's §5g and anything slice 2 adds)
- Modify: `docs/usage.md` (the HTTP API intro, :3203-3205; the route table after the `/api/auth/sso/callback` row; the 7e matrix after the `GET /api/health` row; a new `### Grafana` directly before `### Why \`init\` is CLI-only, and always will be`)
- Test: `packages/pstack/internal/api/routes_grafana_test.go` (append after T7's `TestHealthNamesGrafanaOnlyWhenOn`)

Line numbers are advisory. Slices 1-2 and T7 move them, so every edit below quotes the text it replaces.

**Interfaces:**
- Consumes:
  - `func (a *Auth) SessionHashUser(idHash string) (*UserRow, error)` (T3).
  - `func (a *Auth) SessionUser(session string) (*UserRow, error)`, `func HashToken(token string) string` (auth.go:477, :958).
  - `func (s *Server) grafanaOn() bool`, `func (s *Server) grafanaURL() string`, `Server.grafana atomic.Bool`, test helper `grafanaServer(t *testing.T) *Server` (T7).
  - Existing:
    - api: `redirect(w, to, headers ...[2]string)` (routes_auth.go:60), `query(rawQuery, key string) (string, bool)` (http.go:113), `sessionCandidates(r)` (http.go:151), `baseURL(domain, r)` (http.go:175), `getStr(m *omap.Map, k string)` (body.go:50), `writeError(w, status, msg)` (http.go:40, used only by a negative control).
    - other packages: `sso.RandomB64URL(n int)` (sso.go:758), `sso.SafeNext(raw)` (sso.go:785), `(*Auth).Transient() sso.TransientStore` with `Set(key, value string, ttlSeconds int64) error` / `Take(key) (string, bool, error)` / `Get` (auth.go:569, transient.go:18-26), `js.EncodeURIComponent`, `js.DecodeURIComponent`, `js.B64URL` (js.go:143, :161, :256), `jsonx.Must`, `jsonx.O` (jsonx.go:27, :139), `omap.Parse` (json.go:69).
    - auth: `auth.Developer`/`Maintainer`/`Admin`/`Viewer` (auth.go:131-134), `auth.CreateOpts{Role string; Email string}` (auth.go:252).
- Produces (later tasks rely on these):
  - In `routes_grafana.go`:
    - `const grafanaSessionCookie = "__Host-pstack_grafana"`, `grafanaStateCookie = "__Host-pstack_grafana_state"`, `grafanaCallbackPath = "/-/pstack/callback"`.
    - `var grafanaRoles = map[auth.Role]string{auth.Developer: "Editor", auth.Maintainer: "Editor", auth.Admin: "Admin"}`.
    - `var grafanaStateRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)`.
    - `func hostCookie(name, value string, maxAge int) [2]string`.
    - `func (s *Server) grafanaMAC(hash string) string`.
    - `func (s *Server) grafanaUser(r *http.Request) *auth.UserRow`.
    - `func (s *Server) grafanaVerify(w http.ResponseWriter, r *http.Request)`.
    - `func (s *Server) grafanaCallback(w http.ResponseWriter, r *http.Request, code string) bool`.
    - `func (s *Server) grafanaStart(w http.ResponseWriter, r *http.Request)`.
  - `preGate` cases `path == "/api/auth/grafana/verify" && GET` and `path == "/api/auth/grafana/start" && GET`.
  - Wire: every refusal goes through `http.Error`, so the body ends `\n` and content-type is `text/plain; charset=utf-8`.
    - `404 Not found.` when `!grafanaOn()`.
    - `400 Sign-in link is invalid.` and `500 Sign-in failed.` from start.
    - `403 No Grafana access.` and `403 Cross-origin request refused.` from verify.
    - `401` with an empty body for a cookieless non-navigation.
  - Test helpers in `routes_grafana_test.go` (package api; T9/T10/T11 must not redeclare these names):
    - `forwardAuth(s *Server, hdr ...string) *httptest.ResponseRecorder`: defaults to Host `pstack:7878`, `x-forwarded-host: grafana.preview.example.com`, `x-forwarded-uri: /d/x`, `x-forwarded-method: GET`, `sec-fetch-mode: navigate`, `sec-fetch-dest: document`. Pairs override; an empty value deletes. T9's verify case is `forwardAuth(s, "sec-fetch-mode", "cors")`.
    - `grafanaStartReq(s *Server, rawQuery, cookie string) *httptest.ResponseRecorder`.
    - `grafanaAccount(t, s, name string, role auth.Role) (*auth.UserRow, string, string)`.
    - `park(t, s, session, state, next string, ttl int64) string`.
    - `setCookies(w) string`.
  - Docs:
    - `docs/control-plane.md` `## 5h. Grafana sign-in`, whose last subsection is `### Roles, and why viewers are refused` (T9 appends its subsection after it).
    - `docs/usage.md` `### Grafana` (anchor `#grafana`), whose last bullet begins `- **A username deleted and created again**` (T9 and T10 each append one bullet after it, anchored on its last line `  the dashboards it owns.`). Step 10's check pins `grep -c '  the dashboards it owns.' docs/usage.md` = 1 before T9 runs; it is 0 in today's tree.

- [ ] **Step 1: Write the failing tests**

Append to `packages/pstack/internal/api/routes_grafana_test.go`, after T7's `TestHealthNamesGrafanaOnlyWhenOn`. Replace the file's import block with this one: T7's imports (`httptest`, `testing`, `events`, `omap`) plus T8's. If T7's code uses a package not listed here, keep that line as well.

```go
import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
)
```

Then append:

```go
// ── Grafana sign-in: verify, the callback inside it, and start ──────────────────────────────────

// forwardAuth is one call Traefik's forwardAuth makes for grafana.<domain>, sent through handle so
// preGate's routing is exercised: Host is pstack's service name, and the X-Forwarded-* headers are
// the real request's. The defaults are a top-level GET navigation to /d/x with no cookie. Each
// name/value pair in hdr overrides one header; an empty value deletes it.
func forwardAuth(s *Server, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/grafana/verify", nil)
	r.Host = "pstack:7878"
	r.Header.Set("x-forwarded-host", "grafana.preview.example.com")
	r.Header.Set("x-forwarded-uri", "/d/x")
	r.Header.Set("x-forwarded-method", "GET")
	r.Header.Set("sec-fetch-mode", "navigate")
	r.Header.Set("sec-fetch-dest", "document")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			r.Header.Del(hdr[i])
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	s.handle(w, r)
	return w
}

// grafanaStartReq is a browser's GET of start on control.<domain>, with the Cookie header it sends.
func grafanaStartReq(s *Server, rawQuery, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/grafana/start?"+rawQuery, nil)
	if cookie != "" {
		r.Header.Set("cookie", cookie)
	}
	w := httptest.NewRecorder()
	s.handle(w, r)
	return w
}

// grafanaAccount creates name at role (admin is the bootstrap, so create it first) and signs it in.
// It returns the account, its pstack session, and the Cookie header of a browser Grafana's callback
// signed in: exactly the value grafanaCallback sets. Each account is two argon2 runs, so tests share.
func grafanaAccount(t *testing.T, s *Server, name string, role auth.Role) (*auth.UserRow, string, string) {
	t.Helper()
	var err error
	if role == auth.Admin {
		_, err = s.auth.Bootstrap(name, "correct-horse")
	} else {
		_, err = s.auth.CreateUser(name, "correct-horse", auth.CreateOpts{Role: string(role)})
	}
	if err != nil {
		t.Fatal(err)
	}
	session, u, err := s.auth.Login(name, "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	h := auth.HashToken(session)
	return u, session, grafanaSessionCookie + "=" + h + "." + s.grafanaMAC(h)
}

// park is what grafanaStart leaves for session's browser: the code the callback presents.
func park(t *testing.T, s *Server, session, state, next string, ttl int64) string {
	t.Helper()
	code := sso.RandomB64URL(32)
	v := jsonx.Must(jsonx.O("session", auth.HashToken(session), "state", state, "next", next))
	if err := s.auth.Transient().Set("grafana:"+code, string(v), ttl); err != nil {
		t.Fatal(err)
	}
	return code
}

// setCookies is every Set-Cookie line, in order. Get would read only the first.
func setCookies(w *httptest.ResponseRecorder) string {
	return strings.Join(w.Header().Values("set-cookie"), "\n")
}

// negative control: delete grafanaVerify's `if !s.grafanaOn() {…}` — the off subtest fails (302, not 404).
func TestGrafanaVerify(t *testing.T) {
	s := grafanaServer(t)
	s.grafana.Store(true)
	_, _, sami := grafanaAccount(t, s, "sami", auth.Admin)
	_, devSession, dev := grafanaAccount(t, s, "dev", auth.Developer)
	_, _, maint := grafanaAccount(t, s, "maint", auth.Maintainer)
	_, _, viewer := grafanaAccount(t, s, "view", auth.Viewer)

	t.Run("a navigation without a cookie goes to start, absolute, with the state cookie", func(t *testing.T) {
		// negative control: redirect to "/api/auth/grafana/start?state=…" without baseURL(s.opts.Domain, r) — Location is relative.
		w := forwardAuth(s)
		loc := w.Header().Get("location")
		u, _ := url.Parse(loc)
		state := u.Query().Get("state")
		if w.Code != 302 || !grafanaStateRe.MatchString(state) {
			t.Fatalf("got %d %q", w.Code, loc)
		}
		if want := "https://control.preview.example.com/api/auth/grafana/start?state=" + state + "&next=%2Fd%2Fx"; loc != want {
			t.Fatalf("location %q, want %q", loc, want)
		}
		if got, want := setCookies(w), grafanaStateCookie+"="+state+"; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=900"; got != want {
			t.Fatalf("set-cookie %q, want %q", got, want)
		}
	})

	t.Run("a fetch without a cookie is 401 with no Location and no cookie", func(t *testing.T) {
		// negative control: drop the sec-fetch-mode clause — the cors request with dest=document gets a 302.
		// dest=document isolates the mode clause: a real fetch sends dest=empty, which the dest clause refuses too.
		for _, dest := range []string{"empty", "document"} {
			w := forwardAuth(s, "sec-fetch-mode", "cors", "sec-fetch-dest", dest)
			if w.Code != 401 || w.Header().Get("location") != "" || setCookies(w) != "" || w.Body.Len() != 0 {
				t.Fatalf("dest=%s: %d location=%q set-cookie=%q body=%q", dest, w.Code, w.Header().Get("location"), setCookies(w), w.Body.String())
			}
		}
	})

	t.Run("a frame without a cookie is 401 with no state cookie", func(t *testing.T) {
		// negative control: drop the sec-fetch-dest clause — the iframe gets a 302 and a state cookie.
		w := forwardAuth(s, "sec-fetch-dest", "iframe")
		if w.Code != 401 || setCookies(w) != "" {
			t.Fatalf("got %d set-cookie=%q", w.Code, setCookies(w))
		}
	})

	t.Run("X-Forwarded-Host never builds the Location", func(t *testing.T) {
		// negative control: build the start URL from "https://"+requestHost(r) instead of baseURL(s.opts.Domain, r) — Location names evil.example.
		w := forwardAuth(s, "x-forwarded-host", "evil.example")
		if loc := w.Header().Get("location"); !strings.HasPrefix(loc, "https://control.preview.example.com/api/auth/grafana/start?state=") {
			t.Fatalf("location %q", loc)
		}
	})

	t.Run("a protocol-relative X-Forwarded-Uri returns to the root", func(t *testing.T) {
		// negative control: EncodeURIComponent(u.RequestURI()) without sso.SafeNext — next is %2F%2Fevil.example%2Fx.
		w := forwardAuth(s, "x-forwarded-uri", "//evil.example/x")
		if loc := w.Header().Get("location"); !strings.HasSuffix(loc, "&next=%2F") {
			t.Fatalf("location %q", loc)
		}
	})

	t.Run("developer and maintainer are Editor, admin is Admin", func(t *testing.T) {
		// negative control: map auth.Maintainer to "Admin" in grafanaRoles — maint answers Admin.
		for _, c := range []struct{ cookie, user, role string }{{dev, "dev", "Editor"}, {maint, "maint", "Editor"}, {sami, "sami", "Admin"}} {
			w := forwardAuth(s, "cookie", c.cookie)
			if w.Code != 204 || w.Header().Get("x-webauth-user") != c.user || w.Header().Get("x-webauth-role") != c.role {
				t.Fatalf("%s: %d user=%q role=%q", c.user, w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
			}
		}
	})

	t.Run("a viewer is refused", func(t *testing.T) {
		// negative control: add `auth.Viewer: "Viewer"` to grafanaRoles — the viewer gets 204.
		w := forwardAuth(s, "cookie", viewer)
		if w.Code != 403 || w.Body.String() != "No Grafana access.\n" || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d %q user=%q", w.Code, w.Body.String(), w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("a hand-set role is refused, never defaulted", func(t *testing.T) {
		// negative control: `if !ok { role, ok = "Viewer", true }` after the grafanaRoles lookup — superuser gets 204.
		odd, _, cookie := grafanaAccount(t, s, "odd", auth.Viewer)
		// CreateUser and SetRole refuse an unrankable role; an operator's SQL does not.
		if _, err := s.store.DB.Exec("UPDATE users SET role = 'superuser' WHERE id = ?", odd.ID); err != nil {
			t.Fatal(err)
		}
		w := forwardAuth(s, "cookie", cookie)
		if w.Code != 403 || w.Body.String() != "No Grafana access.\n" {
			t.Fatalf("got %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("X-WEBAUTH-* sent by the client are never echoed", func(t *testing.T) {
		// negative control: at the top of grafanaVerify, copy a non-empty r.Header X-Webauth-User/-Role onto w.Header() — the cookieless 401 carries x-webauth-user: admin.
		w := forwardAuth(s, "cookie", dev, "x-webauth-user", "admin", "x-webauth-role", "Admin")
		if w.Code != 204 || w.Header().Get("x-webauth-user") != "dev" || w.Header().Get("x-webauth-role") != "Editor" {
			t.Fatalf("with dev's cookie: %d user=%q role=%q", w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
		}
		w = forwardAuth(s, "sec-fetch-mode", "cors", "x-webauth-user", "admin", "x-webauth-role", "Admin")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" || w.Header().Get("x-webauth-role") != "" {
			t.Fatalf("without a cookie: %d user=%q role=%q", w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
		}
	})

	t.Run("a forged MAC is no cookie", func(t *testing.T) {
		// negative control: drop `|| !hmac.Equal(…)` from grafanaUser — the forged cookie gets 204.
		forged := grafanaSessionCookie + "=" + auth.HashToken(devSession) + "." + js.B64URL(make([]byte, 32))
		if w := forwardAuth(s, "cookie", forged, "sec-fetch-mode", "cors"); w.Code != 401 {
			t.Fatalf("fetch: got %d", w.Code)
		}
		if w := forwardAuth(s, "cookie", forged); w.Code != 302 {
			t.Fatalf("navigation: got %d", w.Code)
		}
	})

	t.Run("pstack_session alone never passes", func(t *testing.T) {
		// negative control: first thing in grafanaUser, `if who := s.principal(r); who != nil && who.User != nil { return who.User }` — 204.
		w := forwardAuth(s, "cookie", "pstack_session="+devSession, "sec-fetch-mode", "cors")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d user=%q", w.Code, w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("the PSTACK_TOKEN bearer alone never passes", func(t *testing.T) {
		// negative control: first thing in grafanaUser, `if who := s.principal(r); who != nil && who.Kind == auth.KindRoot { return &auth.UserRow{Username: "root", Role: "admin"} }` — 204.
		w := forwardAuth(s, "authorization", "Bearer t0ken", "sec-fetch-mode", "cors")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d user=%q", w.Code, w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("sign-out, a password change and deletion end access; a demotion to viewer is 403", func(t *testing.T) {
		// negative control: cache the user — a package-level map from cookie value to the row SessionHashUser first returned, answered before the lookup — every revoked cookie still gets 204.
		_, outSession, outCookie := grafanaAccount(t, s, "out", auth.Developer)
		pw, _, pwCookie := grafanaAccount(t, s, "pw", auth.Developer)
		gone, _, goneCookie := grafanaAccount(t, s, "gone", auth.Developer)
		down, _, downCookie := grafanaAccount(t, s, "down", auth.Developer)
		for _, c := range []string{outCookie, pwCookie, goneCookie, downCookie} {
			if w := forwardAuth(s, "cookie", c); w.Code != 204 {
				t.Fatalf("before revoking: got %d", w.Code)
			}
		}
		if err := s.auth.Logout(outSession); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.SetPassword(pw.ID, "another-horse"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.DeleteUser(gone.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.SetRole(down.ID, auth.Viewer); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ after, cookie string }{{"sign-out", outCookie}, {"password change", pwCookie}, {"deletion", goneCookie}} {
			if w := forwardAuth(s, "cookie", c.cookie, "sec-fetch-mode", "cors"); w.Code != 401 {
				t.Errorf("after %s: got %d, want 401", c.after, w.Code)
			}
		}
		if w := forwardAuth(s, "cookie", downCookie); w.Code != 403 {
			t.Errorf("after demotion to viewer: got %d, want 403", w.Code)
		}
	})

	t.Run("an unsafe method needs Grafana's Origin", func(t *testing.T) {
		// negative control: compare requestHost(r) with "grafana."+s.opts.Domain instead of Origin with s.grafanaURL() — the preview POST gets 204.
		for _, c := range []struct {
			name, method, origin string
			code                 int
		}{
			{"POST from Grafana", "POST", "https://grafana.preview.example.com", 204},
			{"POST from a preview", "POST", "https://pr-1.preview.example.com", 403},
			{"POST with no Origin", "POST", "", 403},
			{"no X-Forwarded-Method, a preview Origin", "", "https://pr-1.preview.example.com", 403},
			{"GET from a preview", "GET", "https://pr-1.preview.example.com", 204},
		} {
			w := forwardAuth(s, "cookie", dev, "x-forwarded-method", c.method, "origin", c.origin)
			if w.Code != c.code || (c.code == 403 && w.Body.String() != "Cross-origin request refused.\n") {
				t.Errorf("%s: got %d %q, want %d", c.name, w.Code, w.Body.String(), c.code)
			}
		}
	})

	// The callback cases present a code parked for dev's session, with the navigation headers a
	// browser following Grafana's redirect sends.
	callback := func(code, state string) *httptest.ResponseRecorder {
		return forwardAuth(s, "x-forwarded-uri", grafanaCallbackPath+"?code="+code, "cookie", grafanaStateCookie+"="+state)
	}

	t.Run("callback: a good code and its state cookie sign the browser in", func(t *testing.T) {
		// negative control: drop `hostCookie(grafanaStateCookie, "", 0)` from the callback's redirect — one Set-Cookie, the state never cleared.
		state := sso.RandomB64URL(32)
		w := callback(park(t, s, devSession, state, "/d/x", 60), state)
		h := auth.HashToken(devSession)
		signedIn := grafanaSessionCookie + "=" + h + "." + s.grafanaMAC(h)
		want := signedIn + "; Path=/; Secure; HttpOnly; SameSite=Lax\n" + grafanaStateCookie + "=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0"
		if w.Code != 302 || w.Header().Get("location") != "https://grafana.preview.example.com/d/x" || setCookies(w) != want {
			t.Fatalf("got %d %q\n%s", w.Code, w.Header().Get("location"), setCookies(w))
		}
		if w := forwardAuth(s, "cookie", signedIn); w.Code != 204 || w.Header().Get("x-webauth-user") != "dev" {
			t.Fatalf("with the cookie it set: %d", w.Code)
		}
	})

	t.Run("callback: a replayed code restarts sign-in", func(t *testing.T) {
		// negative control: Get instead of Take in grafanaCallback — the second presentation signs in again.
		state := sso.RandomB64URL(32)
		code := park(t, s, devSession, state, "/d/x", 60)
		if w := callback(code, state); !strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("first presentation: %d %q", w.Code, setCookies(w))
		}
		w := callback(code, state)
		if loc := w.Header().Get("location"); w.Code != 302 || !strings.HasPrefix(loc, "https://control.preview.example.com/api/auth/grafana/start?state=") || !strings.HasSuffix(loc, "&next=%2F") || strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("replay: %d %q %q", w.Code, loc, setCookies(w))
		}
	})

	t.Run("callback: a state mismatch signs nobody in and burns the code", func(t *testing.T) {
		// negative control: redirect before comparing the state (compare after the redirect) — the mismatched browser gets the session cookie. Also run: Get, compare, Take only on a match — the retry with the right state signs in.
		state := sso.RandomB64URL(32)
		code := park(t, s, devSession, state, "/d/x", 60)
		if w := callback(code, sso.RandomB64URL(32)); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("a mismatched state signed in: %q", setCookies(w))
		}
		if w := callback(code, state); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("the code survived a mismatch: %q", setCookies(w))
		}
	})

	t.Run("callback: an expired code signs nobody in", func(t *testing.T) {
		// negative control: drop `AND expires_at > ?` (and its argument) from SqliteTransientStore.Take in sso/transient.go — the ttl-0 code signs in.
		// Set(…, 0) stores expires_at = now, and Take's `expires_at > now` is already false: no sleep.
		state := sso.RandomB64URL(32)
		if w := callback(park(t, s, devSession, state, "/d/x", 0), state); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("an expired code signed in: %q", setCookies(w))
		}
	})

	t.Run("callback: a reload by a signed-in browser goes to Grafana's home", func(t *testing.T) {
		// negative control: delete the `if user != nil { redirect(w, s.grafanaURL()+"/") … }` branch — the reload gets 204.
		w := forwardAuth(s, "x-forwarded-uri", grafanaCallbackPath+"?code="+sso.RandomB64URL(32), "cookie", dev)
		if w.Code != 302 || w.Header().Get("location") != "https://grafana.preview.example.com/" {
			t.Fatalf("got %d %q", w.Code, w.Header().Get("location"))
		}
	})

	t.Run("off: no domain, no token or no grafana container is a plain-text 404", func(t *testing.T) {
		// negative control: drop `if !s.grafanaOn() {…}` from grafanaVerify — each off server answers 302.
		for _, c := range []struct {
			name string
			set  func(*Server)
		}{
			{"no domain", func(o *Server) { o.grafana.Store(true); o.opts.Domain = "" }},
			{"no token", func(o *Server) { o.grafana.Store(true); o.opts.Token = "" }},
			{"no grafana container", func(o *Server) {}},
		} {
			o := grafanaServer(t)
			c.set(o)
			w := forwardAuth(o)
			if w.Code != 404 || w.Body.String() != "Not found.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
				t.Errorf("%s: got %d %q %q", c.name, w.Code, w.Body.String(), w.Header().Get("content-type"))
			}
		}
	})
}

// negative control: writeError(w, 400, "Sign-in link is invalid.") — the malformed-state subtest fails (JSON body).
func TestGrafanaStart(t *testing.T) {
	s := grafanaServer(t)
	s.grafana.Store(true)
	_, session, _ := grafanaAccount(t, s, "sami", auth.Admin)
	state := sso.RandomB64URL(32)

	t.Run("signed in: an absolute 302 to the callback, and the code parks the hash, the state and a safe next", func(t *testing.T) {
		// negative control: park `c` instead of `auth.HashToken(c)` — the parked session is the raw cookie. Also run: drop `next = sso.SafeNext(next)` (parks //evil.example/x); range only sessionCandidates(r)[:1] (the stale cookie ends on /login).
		for _, c := range []struct{ next, parked string }{{"%2Fd%2Fx", "/d/x"}, {"%2F%2Fevil.example%2Fx", "/"}} {
			// A stale cookie first: start takes the first candidate SessionUser resolves.
			w := grafanaStartReq(s, "state="+state+"&next="+c.next, "pstack_session=pstack_ses_stale; pstack_session="+session)
			loc := w.Header().Get("location")
			code := strings.TrimPrefix(loc, "https://grafana.preview.example.com/-/pstack/callback?code=")
			if w.Code != 302 || !grafanaStateRe.MatchString(code) {
				t.Fatalf("next=%s: got %d %q", c.next, w.Code, loc)
			}
			raw, found, err := s.auth.Transient().Get("grafana:" + code)
			if err != nil || !found {
				t.Fatalf("next=%s: nothing parked (%v)", c.next, err)
			}
			v, _ := omap.Parse([]byte(raw))
			m, _ := v.(*omap.Map)
			var got [3]string
			got[0], _ = getStr(m, "session")
			got[1], _ = getStr(m, "state")
			got[2], _ = getStr(m, "next")
			if want := [3]string{auth.HashToken(session), state, c.parked}; got != want {
				t.Fatalf("next=%s: parked %q, want %q", c.next, got, want)
			}
		}
	})

	t.Run("signed out: 302 to the login page, whose next decodes back to this start URL", func(t *testing.T) {
		// negative control: drop the inner js.EncodeURIComponent(next) — the decoded next ends &next=/d/x.
		w := grafanaStartReq(s, "state="+state+"&next=%2Fd%2Fx", "")
		loc := w.Header().Get("location")
		back, ok := js.DecodeURIComponent(strings.TrimPrefix(loc, "/login?next="))
		if w.Code != 302 || !strings.HasPrefix(loc, "/login?next=") || !ok || back != "/api/auth/grafana/start?state="+state+"&next=%2Fd%2Fx" {
			t.Fatalf("got %d %q (decodes to %q)", w.Code, loc, back)
		}
	})

	t.Run("a malformed state is a plain-text 400", func(t *testing.T) {
		// negative control: writeError(w, 400, "Sign-in link is invalid.") — the body is JSON and the content-type application/json.
		for _, q := range []string{"", "state=short&next=%2F", "state=" + strings.Repeat("a", 42) + ".&next=%2F"} {
			w := grafanaStartReq(s, q, "pstack_session="+session)
			if w.Code != 400 || w.Body.String() != "Sign-in link is invalid.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
				t.Errorf("%q: got %d %q %q", q, w.Code, w.Body.String(), w.Header().Get("content-type"))
			}
		}
	})

	t.Run("off: a plain-text 404", func(t *testing.T) {
		// negative control: drop `if !s.grafanaOn() {…}` from grafanaStart — the off server answers 302 to /login.
		o := grafanaServer(t)
		w := grafanaStartReq(o, "state="+state+"&next=%2F", "")
		if w.Code != 404 || w.Body.String() != "Not found.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
			t.Fatalf("got %d %q %q", w.Code, w.Body.String(), w.Header().Get("content-type"))
		}
	})
}
```

Subtest names contain no `/`, so `-run 'TestGrafanaVerify/^callback:_a_replayed'` targets one of them.

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestGrafanaVerify|TestGrafanaStart'
```

Expected: `FAIL … internal/api [build failed]`, with errors such as `undefined: grafanaSessionCookie`, `s.grafanaMAC undefined (type *Server has no field or method grafanaMAC)`, `undefined: grafanaStateRe`, `undefined: grafanaStateCookie`, `undefined: grafanaCallbackPath`.

- [ ] **Step 3: Implement**

3a. `packages/pstack/internal/api/routes_grafana.go`: first, insert these lines directly above `package api`. They continue T7's header comment, which becomes the sign-in design record:

```go
//
// Sign-in. Traefik's forwardAuth asks verify about EVERY request to grafana.<domain>, and verify
// answers from __Host-pstack_grafana alone: never pstack_session, never a bearer, never principal().
// That cookie is `<id_hash>.<mac>`, the parent pstack session's stored hash signed with PSTACK_TOKEN
// under a "pstack-grafana\n" prefix: the share-link precedent, so there is no table, and the session
// row IS the Grafana session (a pstack sign-out ends Grafana access on the next request). Getting the
// cookie is a redirect sign-in: verify sets a state cookie and sends a top-level navigation to start
// on control.<domain>; start parks a 60 s single-use code; the callback is a branch of verify, because
// forwardAuth relays a 302 and its Set-Cookie. Roles: developer and maintainer are Editor, admin is
// Admin, and viewers are refused (owner, 2026-09-15): a Grafana Viewer still queries Loki's
// unredacted lines.
```

Second, add this import block directly below `package api`. T7's file imports nothing; if T7 left an import block, replace it with this one:

```go
import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
)
```

Third, after the line `func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }`, append the spec's code, unchanged:

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

Nothing in this file matches the openapi coverage scan: no `path == "/api/`, no `case "/api/`, and `grafanaStateRe` does not start `^/api/`. So its routes are only the two preGate literals below.

3b. `packages/pstack/internal/api/routes_auth.go`, in `preGate`. Replace:

```go
	case path == "/api/auth/sso/callback" && r.Method == http.MethodGet:
		s.ssoCallback(w, r)
		return true
	}
```

with:

```go
	case path == "/api/auth/sso/callback" && r.Method == http.MethodGet:
		s.ssoCallback(w, r)
		return true

	// Grafana sign-in: Traefik's forwardAuth, and the browser leg on control.<domain>. Neither asks
	// principal(); see routes_grafana.go.
	case path == "/api/auth/grafana/verify" && r.Method == http.MethodGet:
		s.grafanaVerify(w, r)
		return true

	case path == "/api/auth/grafana/start" && r.Method == http.MethodGet:
		s.grafanaStart(w, r)
		return true
	}
```

3c. `packages/pstack/internal/api/server.go`, header. Replace:

```go
// every route but health/probe/openapi/login/logout/bootstrap/sso is behind the principal gate, and behind THAT
```

with:

```go
// every route but health/probe/openapi/login/logout/bootstrap/sso/grafana verify+start is behind the principal gate, and behind THAT
```

3d. `server.go`, in `handle()`. Replace:

```go
	// Pre-gate: login, logout, bootstrap, the SSO round trip. These run before the gate, or nobody
	// could ever log in.
```

with:

```go
	// Pre-gate: login, logout, bootstrap, the SSO round trip, and Grafana's verify and start
	// (routes_grafana.go). These run before the gate, or nobody could ever log in.
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run 'TestGrafanaVerify|TestGrafanaStart' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected: `--- PASS: TestGrafanaVerify` (20 subtests), `--- PASS: TestGrafanaStart` (4 subtests), `ok … internal/api`. Under -race each account costs about 1.7 s of argon2, so TestGrafanaVerify takes about 17 s and TestGrafanaStart about 2 s.

- [ ] **Step 5: Run every negative control**

For each row, apply the mutation to the named file and run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -count=1 -timeout 120s ./internal/api/ -run '<target>'`. Watch that subtest FAIL, then restore the file to its Step 3 text before the next row. After the last row, `but diff` must show no hunk in `packages/pstack/internal/sso/transient.go`. `V` = `TestGrafanaVerify/`, `S` = `TestGrafanaStart/`. Row 21 is also TestGrafanaVerify's top-level control, and row 27 is TestGrafanaStart's.

| # | Mutation (routes_grafana.go unless named) | Target |
|---|---|---|
| 1 | `redirect(w, baseURL(s.opts.Domain, r)+"/api/…` → `redirect(w, "/api/…` | `V^a_navigation_without` |
| 2 | the unauthenticated condition becomes `r.Header.Get("sec-fetch-dest") != "document"` | `V^a_fetch_without` |
| 3 | the unauthenticated condition becomes `r.Header.Get("sec-fetch-mode") != "navigate"` | `V^a_frame_without` |
| 4 | `baseURL(s.opts.Domain, r)+` → `"https://"+requestHost(r)+` | `V^X-Forwarded-Host_never` |
| 5 | `js.EncodeURIComponent(sso.SafeNext(u.RequestURI()))` → `js.EncodeURIComponent(u.RequestURI())` | `V^a_protocol-relative` |
| 6 | `auth.Maintainer: "Editor"` → `auth.Maintainer: "Admin"` | `V^developer_and_maintainer` |
| 7 | prepend `auth.Viewer: "Viewer", ` inside the grafanaRoles literal | `V^a_viewer_is_refused` |
| 8 | after the grafanaRoles lookup, insert `if !ok { role, ok = "Viewer", true }` | `V^a_hand-set_role` |
| 9 | first in grafanaVerify: `for _, k := range []string{"X-Webauth-User", "X-Webauth-Role"} { if v := r.Header.Get(k); v != "" { w.Header().Set(k, v) } }` | `V^X-WEBAUTH-` |
| 10 | `if !ok \|\| !hmac.Equal([]byte(mac), []byte(s.grafanaMAC(hash))) {` → `if !ok \|\| mac == "" {` | `V^a_forged_MAC` |
| 11 | first in grafanaUser: `if who := s.principal(r); who != nil && who.User != nil { return who.User }` | `V^pstack_session_alone` |
| 12 | first in grafanaUser: `if who := s.principal(r); who != nil && who.Kind == auth.KindRoot { return &auth.UserRow{Username: "root", Role: "admin"} }` | `V^the_PSTACK_TOKEN_bearer` |
| 13 | `var grafanaUserCache = map[string]*auth.UserRow{}`; in grafanaUser return `grafanaUserCache[c.Value]` when present, and store a non-nil `u` | `V^sign-out,` |
| 14 | `r.Header.Get("origin") != s.grafanaURL()` → `requestHost(r) != "grafana."+s.opts.Domain` | `V^an_unsafe_method` |
| 15 | `Transient().Take("grafana:" + code)` → `Transient().Get("grafana:" + code)` | `V^callback:_a_replayed` |
| 16 | move the success `redirect(…)` above the state-cookie check and always `return true` | `V^callback:_a_state_mismatch` |
| 17 | #15, plus `_, _, _ = s.auth.Transient().Take("grafana:" + code)` just before the success redirect | `V^callback:_a_state_mismatch` |
| 18 | `internal/sso/transient.go` Take becomes `"DELETE FROM sso_state WHERE key = ? RETURNING value", key` | `V^callback:_an_expired` |
| 19 | delete the `if user != nil { // a reloaded callback …}` block | `V^callback:_a_reload` |
| 20 | drop `,\n\t\thostCookie(grafanaStateCookie, "", 0)` from the success redirect | `V^callback:_a_good_code` |
| 21 | delete grafanaVerify's `if !s.grafanaOn() {…}` | `V^off:` |
| 22 | delete grafanaStart's `if !s.grafanaOn() {…}` | `S^off:` |
| 23 | `"session", auth.HashToken(c)` → `"session", c` | `S^signed_in:` |
| 24 | delete `next = sso.SafeNext(next)` | `S^signed_in:` |
| 25 | `range sessionCandidates(r) {` → `range sessionCandidates(r)[:1] {` | `S^signed_in:` |
| 26 | `"&next="+js.EncodeURIComponent(next)))` → `"&next="+next))` | `S^signed_out:` |
| 27 | `http.Error(w, "Sign-in link is invalid.", 400)` → `writeError(w, 400, "Sign-in link is invalid.")` | `S^a_malformed_state` |

While this plan was written, each of the 27 was run against this exact test and implementation code (T3's `SessionHashUser` and T7's pieces stood in), and each failed its target.

- [ ] **Step 6: Run the whole package and watch the route drift tests fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/
```

Expected FAIL, from the two literals Step 3b added to routes_auth.go. Both were observed in a scratch copy of the module:
- `TestEveryRouteIsInTheSpecOrExcusedByName`: `2 route(s) reachable over HTTP with no OpenAPI path, so no \`pstack api\` command:`, listing `/api/auth/grafana/start  (routes_auth.go)` and `/api/auth/grafana/verify  (routes_auth.go)`.
- `TestEveryDispatchedRouteHasARow`: `routes_auth.go dispatches on "/api/auth/grafana/verify" with no row in permissions.go`, and the same for `/api/auth/grafana/start`.

- [ ] **Step 7: Name both routes in the two route lists**

7a. `packages/pstack/internal/api/openapi_coverage_test.go`, in `notInTheSpec`. Replace:

```go
	"/api/auth/sso/callback": "the provider's redirect back, for a browser",
```

with:

```go
	"/api/auth/sso/callback": "the provider's redirect back, for a browser",

	// Grafana sign-in. Traefik calls verify for every request to grafana.<domain>; a browser follows
	// a redirect to start. Neither is a command.
	"/api/auth/grafana/verify": "Traefik's forwardAuth, for grafana.<domain>",
	"/api/auth/grafana/start":  "a 302 for a browser",
```

7b. The same file's header records the count. Replace:

```go
// probe, login, logout, bootstrap, the SSO round trip — never appears there at all. A coverage test
// built on it would pass while missing seven routes.
```

with:

```go
// probe, login, logout, bootstrap, the SSO round trip, Grafana's verify and start — never appears
// there at all. A coverage test built on it would pass while missing nine routes.
```

7c. `packages/pstack/internal/api/permissions_test.go`. Replace:

```go
var preGatePaths = map[string]bool{
	"/api/auth/login":        true,
	"/api/auth/logout":       true,
	"/api/auth/bootstrap":    true,
	"/api/auth/sso/start":    true,
	"/api/auth/sso/callback": true,
}
```

with:

```go
var preGatePaths = map[string]bool{
	"/api/auth/login":          true,
	"/api/auth/logout":         true,
	"/api/auth/bootstrap":      true,
	"/api/auth/sso/start":      true,
	"/api/auth/sso/callback":   true,
	"/api/auth/grafana/verify": true,
	"/api/auth/grafana/start":  true,
}
```

- [ ] **Step 8: Run the whole package and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ && go vet ./internal/api/ && gofmt -l internal/api
```

Expected:
- `ok … internal/api`. This was observed in a scratch copy of the module: about 26 s under -race with these tests included, well inside the timeout. The run includes TestEveryDispatchedRouteHasARow, TestEveryRouteIsInTheSpecOrExcusedByName, TestEveryExclusionStillNamesARealRoute and TestEverySpecPathIsARealRoute.
- `go vet` prints nothing.
- `gofmt -l` prints nothing.

- [ ] **Step 9: Docs — docs/control-plane.md §5h**

Insert directly before the line `## 6. Submitting a deployment`, with one blank line after the inserted block:

````markdown
## 5h. Grafana sign-in

With `--logging loki`, Grafana runs at `grafana.<domain>`, and people sign in with their pstack
accounts. Grafana never sees a password. Traefik's forwardAuth asks pstack about every request, and
Grafana's auth proxy trusts the two headers pstack answers with, `X-WEBAUTH-USER` and
`X-WEBAUTH-ROLE`. The design and its failure modes are in
[`loki-logging-design.md`](loki-logging-design.md); this section records where it plugs in.

### Discovery: a grafana container, read from docker

pstack learns that Grafana is on the way it learns about Loki (§5g). `inspect.GrafanaOn` looks for a
`grafana` service in the control project with `docker ps -a`. There is no setting and no env var. The
answer is cached in `Server.grafana`: stored once in `Start`, before the listener serves, and
refreshed on every reindex tick, never per request. With no container, no `PSTACK_DOMAIN` or no
`PSTACK_TOKEN`, both routes answer `404` and `/api/health` has no `grafana` key. A host without
Grafana has no sign-in surface.

### Two pre-gate routes, and verify never asks `principal()`

`GET /api/auth/grafana/verify` is forwardAuth's address. `GET /api/auth/grafana/start` is for a
browser on `control.<domain>`. Both run in `preGate`, for SSO's reason: nobody has a principal yet.
Verify reads one cookie of its own and nothing else, not `pstack_session` and not a bearer. So neither
a pstack session sent to `grafana.<domain>` nor `PSTACK_TOKEN` gives Grafana access. Only a browser
session has a Grafana identity.

### The Grafana cookie is derived: the share-link precedent again

`__Host-pstack_grafana` is `<id_hash>.<base64url HMAC-SHA256(PSTACK_TOKEN, "pstack-grafana\n" + id_hash)>`,
where `id_hash` is the parent pstack session's stored hash. There is no table, for §5e's reason.
**The parent session's row is the Grafana session.** Every Grafana request joins `sessions` to `users`
by primary key. So a sign-out, a password change, a deleted account or an expired session ends access
on the next request, and a role change applies on the next request. Rotating `PSTACK_TOKEN` fails
every MAC, and the browser signs in again. The prefix keeps this MAC apart from share JWTs, whose
signing input starts `eyJ`. The hash is not a pstack credential anywhere else: `principal` and
`Logout` hash whatever they are given.

Both cookies are `__Host-`, so a preview on `*.<domain>` cannot plant either. They are always
`Secure`, unlike `pstack_session`, because a browser drops a `__Host-` cookie without it.

### The redirect sign-in, and the callback inside verify

- A top-level navigation (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: document`) without the cookie
  gets a 302 to start and a fresh state in `__Host-pstack_grafana_state` (900 s).
- Anything else gets `401`. A fetch would turn a 302 into a CORS error, and a same-site preview must
  not frame a silent sign-in.
- Start turns the browser's `pstack_session` into a 60-second, single-use code bound to that state,
  kept in the SSO transient store under `grafana:<code>` (§5f). With no session, start sends the
  browser to `/login` and back.
- The callback is a branch of verify, on `/-/pstack/callback`. forwardAuth relays a 302 and its
  `Set-Cookie`, so the callback needs no router and never reaches Grafana.
- The callback `Take`s the code **before** comparing the state, so any presentation burns it. A
  failure starts sign-in afresh.
- Every `Location` is absolute and built from `PSTACK_DOMAIN`, because Traefik resolves a relative
  one against `http://pstack:7878`. `next` is `SafeNext`'d at both ends.

An unsafe method must carry `Origin: https://grafana.<domain>`. Every preview is same-site with
Grafana, so `SameSite=Lax` does not stop its POSTs. `X-Forwarded-Method` comes from Traefik, and a
request without it fails the check.

### Roles, and why viewers are refused

| pstack | Grafana |
|---|---|
| `viewer` | refused, `403` |
| `developer`, `maintainer` | Editor |
| `admin` | Admin |

Any other role gets `403`, never a default, because Grafana keeps a user's old role when the header
is missing. Viewers are refused because a Grafana Viewer can still run LogQL over Loki's unredacted
lines through panels and `/api/ds/query`, and pstack shows viewers redacted logs only (invariant 15).
There is no Grafana server admin.

````

- [ ] **Step 10: Docs — docs/usage.md**

10a. HTTP API intro. Replace:

```markdown
except `/api/health`, the login/bootstrap routes and the
two SSO legs, which are how you become one.
```

with:

```markdown
except `/api/health`, the login/bootstrap routes and the
two SSO legs, which are how you become one, and Grafana's two sign-in routes ([Grafana](#grafana)).
```

10b. Route table. Directly after the row that starts `| GET | \`/api/auth/sso/callback\` |`, insert:

```markdown
| GET | `/api/auth/grafana/verify` | Traefik's `X-Forwarded-Uri`/`-Method`/`-Host`, the browser's cookies | **204** with `X-WEBAUTH-USER`/`X-WEBAUTH-ROLE` · **302** to sign-in, or from `/-/pstack/callback` back to Grafana with the Grafana cookie · **401** a fetch or frame with no Grafana cookie · **403** `No Grafana access.` or `Cross-origin request refused.` · **404** on a host without Grafana. Traefik's forwardAuth for `grafana.<domain>`. No auth |
| GET | `/api/auth/grafana/start` | `?state=&next=` | **302** to Grafana's callback with a 60-second code, or to `/login?next=…` when signed out · **400** `Sign-in link is invalid.` · **404** on a host without Grafana. A 302 for a browser. No auth |
```

10c. The 7e matrix. Directly after `| \`GET /api/health\` | none — it is how \`init\` waits for the container |`, insert:

```markdown
| `GET /api/auth/grafana/verify` · `/start` | none — verify decides by its own cookie; viewers get `403` |
```

10d. Directly before the line `### Why \`init\` is CLI-only, and always will be`, insert this, with a blank line after it:

````markdown
### Grafana

With `--logging loki`, Grafana runs at `https://grafana.<domain>`. Sign in with your pstack account.
A visit without a Grafana session goes through pstack's sign-in (password or SSO) and returns to the
page you opened. A browser already signed in to pstack goes straight through.

| pstack role | Grafana |
|---|---|
| `viewer` | `403 No Grafana access.` |
| `developer` | Editor |
| `maintainer` | Editor |
| `admin` | Admin |

- **pstack's sign-out is Grafana's.** Signing out, changing your password or losing the account ends
  Grafana access on the next request. A role change applies on the next request. Grafana has no
  sign-out menu.
- **Only a browser session.** `PSTACK_TOKEN` and personal tokens are not Grafana access.
- **Log lines in Grafana are not redacted.** Loki stores what containers printed, secrets included,
  and Grafana shows it as stored. Editors and Admins read it in Explore. That is why viewers are
  refused: pstack shows them redacted logs only. Developers and above can already open a shell in
  those containers.
- **No Grafana server admin.** Plugin installs and server settings are unavailable in Grafana's UI.
- **No live tail.** Grafana Live is off, so nothing keeps streaming after a sign-out.
- **Logs Drilldown downloads from grafana.com** on Grafana's first start. Without that egress,
  Explore still works.
- **`pstack logging off` keeps Grafana's volume.** Turning logging back on restores users and
  preferences.
- **A username deleted and created again** inherits the old Grafana user: its preferences, stars and
  the dashboards it owns.

````

Check:
- `grep -c '^### Grafana$' docs/usage.md` is 1.
- `grep -c '  the dashboards it owns.' docs/usage.md` is 1. It is 0 before this step; T9 and T10 anchor their bullets on this line, so the count must be exactly 1 before T9 runs.
- `grep -n '## 5h. Grafana sign-in' docs/control-plane.md` comes after `## 5g.` and before `## 6. Submitting a deployment`. If slice 2 already took `## 5h`, number this `## 5i` and say so in the report.

- [ ] **Step 11: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these eight files:
- `packages/pstack/internal/api/routes_grafana.go`
- `packages/pstack/internal/api/routes_grafana_test.go`
- `packages/pstack/internal/api/routes_auth.go`
- `packages/pstack/internal/api/server.go`
- `packages/pstack/internal/api/openapi_coverage_test.go`
- `packages/pstack/internal/api/permissions_test.go`
- `docs/control-plane.md`
- `docs/usage.md`

Never `packages/conformance/golden/host/db/*`, anything under `.superpowers/`, or a file a mutation touched (`internal/sso/transient.go` must show no change). Then:

```bash
but commit -b claude/loki-logging-grafana -m "feat(api): Grafana sign-in through forwardAuth with pstack accounts" <routes_grafana.go-id> <routes_grafana_test.go-id> <routes_auth.go-id> <server.go-id> <openapi_coverage_test.go-id> <permissions_test.go-id> <control-plane.md-id> <usage.md-id>
```

---

### Task 9: api: a service hostname is never served by pstack

**Files:**
- Modify: `packages/pstack/internal/api/server.go` (anchor edits by quoted existing text; line numbers advisory — slices 1 and 2 are moving them). The edit is at the top of `handle()`, today server.go:864-874.
- Modify: `docs/control-plane.md`. The new subsection goes immediately before the heading `## 6. Submitting a deployment` (today :980, and the only match). That puts it at the end of T8's `## 5h. Grafana sign-in`. The anchor is that heading, not T8's prose.
- Modify: `docs/usage.md`. One bullet goes after the last bullet of T8's `### Grafana` section. The anchor is that bullet's last line, `  the dashboards it owns.`, which T8 introduces and which appears nowhere in docs/ today.
- Test: `packages/pstack/internal/api/routes_grafana_test.go` (created by T7, extended by T8)

**Interfaces:**
- Consumes:
  - `func (s *RoutingStore) IsControlHostname(hostname, primary string) bool`. It matches `grafana.<d>` (T2) and `loki.<d>` (slice-1 T12, plan:6295-6316) for the primary and every added domain. It is safe on a nil receiver, because `Domains()` checks `s == nil` (domains.go:99-104).
  - `func (s *RoutingStore) SetDomains(domains []string, o DomainOptions) ([]string, error)` (domains.go:140) and `routing.DomainOptions{Primary, Mode}` (domains.go:69-85). It writes `pstack-domains.yml` (`DomainsYAML`, domains.go:50), the file `Domains()` reads back. The write needs `Writable()` (routing.go:159-172), which a `t.TempDir()` RoutingDir satisfies.
  - `portRe` (http.go:194). `requestHost` (http.go:198-205) appears only in a negative control.
  - T7: `grafanaServer(t *testing.T) *Server`, which is `New(Options{DataDir, Domain: "preview.example.com", Token: "t0ken", RoutingDir: t.TempDir(), Bus, Log})` with `t.Cleanup(s.Stop)`, plus `Server.grafana atomic.Bool`.
  - T8: `(*Server).grafanaVerify`, reached through `preGate` for `GET /api/auth/grafana/verify`. With no `__Host-pstack_grafana` cookie and `sec-fetch-mode` other than `navigate`, it answers `w.WriteHeader(401)` with an empty body, before its Origin check.
  - T8: the usage.md `### Grafana` section, whose last bullet ends with the line `  the dashboards it owns.`
- Produces:
  - The service-host refusal, as the first statement of `handle()` after `path := r.URL.EscapedPath()`. It adds no identifier.
  - A contract for T11 test 3 and T12: `Host: grafana.<primary>` gets `503` with body `Grafana is not running.\n`, `Host: loki.<primary>` gets `503` with body `Loki is not running.\n`, and `grafana.`/`loki.` of an added domain get `404` with body `Not found.\n`. All three are `text/plain; charset=utf-8`, on every path, logging on or off.
  - usage.md: the `- **Grafana down.**` bullet, the last bullet of `### Grafana` until T10 adds its sentence before `### Why \`init\` is CLI-only, and always will be`.

- [ ] **Step 1: Write the failing test.** Append to `packages/pstack/internal/api/routes_grafana_test.go`. The import block must contain `"net/http/httptest"`, `"strings"`, `"testing"` and `"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"`. Add whichever of these T7/T8 left out (`routing` is almost certainly new). The closures `at` and `plain` are local, so they cannot collide with `get` in http_test.go:260-267.

```go
func TestServiceHostnamesAreNeverServedByPstack(t *testing.T) {
	// negative control: delete the whole service-host refusal block from handle() — grafana. and loki.
	// get the UI's 200 on / and the added domain's grafana. gets the UI too.
	s := grafanaServer(t)
	s.grafana.Store(true) // without it verify answers 404, and the verify case could not tell refusal from off
	if _, err := s.routing.SetDomains([]string{"added.example"}, routing.DomainOptions{Primary: "preview.example.com", Mode: "http01"}); err != nil {
		t.Fatal(err)
	}
	// at sends one request through handle() the way Traefik delivers it: Host as the browser sent it.
	// httptest.NewRequest defaults Host to example.com, so it is always set.
	at := func(host, path string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = host
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		s.handle(w, r)
		return w
	}
	// plain is an http.Error answer: exact status, exact body with its trailing newline, text/plain.
	plain := func(t *testing.T, host, path string, code int, body string) {
		t.Helper()
		w := at(host, path)
		if w.Code != code || w.Body.String() != body || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
			t.Fatalf("%s%s: %d %q (%s), want %d %q as text/plain", host, path, w.Code, w.Body.String(), w.Header().Get("content-type"), code, body)
		}
	}

	t.Run("grafana. of the primary domain is 503 on the UI and the API", func(t *testing.T) {
		// negative control: delete the `case "grafana." + d:` arm — every answer becomes 404 "Not found.\n".
		for _, host := range []string{"grafana.preview.example.com", "Grafana.Preview.Example.com:443"} {
			for _, path := range []string{"/", "/api/auth/me"} {
				plain(t, host, path, 503, "Grafana is not running.\n")
			}
		}
	})

	t.Run("loki. of the primary domain is 503 on the UI and the API", func(t *testing.T) {
		// negative control: delete the `case "loki." + d:` arm — both answers become 404 "Not found.\n".
		for _, path := range []string{"/", "/api/auth/me"} {
			plain(t, "loki.preview.example.com", path, 503, "Loki is not running.\n")
		}
	})

	t.Run("an added domain's grafana. and loki. are 404", func(t *testing.T) {
		// negative control: replace `s.routing.IsControlHostname(h, s.opts.Domain)` with the primary only,
		// `(h == "grafana."+strings.ToLower(s.opts.Domain) || h == "loki."+strings.ToLower(s.opts.Domain))`
		// — grafana.added.example falls through to the UI's 200.
		plain(t, "grafana.added.example", "/", 404, "Not found.\n")
		plain(t, "loki.added.example", "/", 404, "Not found.\n")
	})

	t.Run("forwardAuth's call to verify is not refused", func(t *testing.T) {
		// negative control: key the refusal on requestHost(r) instead of r.Host — X-Forwarded-Host
		// grafana.preview.example.com turns verify's 401 into 503 "Grafana is not running.\n".
		w := at("pstack:7878", "/api/auth/grafana/verify",
			"x-forwarded-host", "grafana.preview.example.com",
			"x-forwarded-uri", "/d/x",
			"x-forwarded-method", "GET",
			"sec-fetch-mode", "cors")
		if w.Code != 401 || w.Body.Len() != 0 {
			t.Fatalf("verify: %d %q, want 401 with no body", w.Code, w.Body.String())
		}
	})

	t.Run("control. is served", func(t *testing.T) {
		// negative control: drop `(strings.HasPrefix(h, "grafana.") || strings.HasPrefix(h, "loki.")) &&`
		// — control.preview.example.com is a control hostname too, and gets 404 "Not found.\n".
		w := at("control.preview.example.com", "/")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("content-type"), "text/html") {
			t.Fatalf("control.: %d (%s), want 200 text/html", w.Code, w.Header().Get("content-type"))
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestServiceHostnamesAreNeverServedByPstack
```

Expected: three subtests FAIL and two PASS. `grafanaServer` sets no `UIHTML`, so the UI answer is a 200 with an empty body.

```
--- FAIL: TestServiceHostnamesAreNeverServedByPstack
    --- FAIL: TestServiceHostnamesAreNeverServedByPstack/grafana._of_the_primary_domain_is_503_on_the_UI_and_the_API
        routes_grafana_test.go:…: grafana.preview.example.com/: 200 "" (text/html; charset=utf-8), want 503 "Grafana is not running.\n" as text/plain
    --- FAIL: TestServiceHostnamesAreNeverServedByPstack/loki._of_the_primary_domain_is_503_on_the_UI_and_the_API
        routes_grafana_test.go:…: loki.preview.example.com/: 200 "" (text/html; charset=utf-8), want 503 "Loki is not running.\n" as text/plain
    --- FAIL: TestServiceHostnamesAreNeverServedByPstack/an_added_domain's_grafana._and_loki._are_404
        routes_grafana_test.go:…: grafana.added.example/: 200 "" (text/html; charset=utf-8), want 404 "Not found.\n" as text/plain
    --- PASS: TestServiceHostnamesAreNeverServedByPstack/forwardAuth's_call_to_verify_is_not_refused
    --- PASS: TestServiceHostnamesAreNeverServedByPstack/control._is_served
FAIL
```

The two passing subtests guard against the refusal reaching too far. Their negative controls run in Step 4, against the implementation.

- [ ] **Step 3: Implement.** In `packages/pstack/internal/api/server.go`, replace this existing text (today :864-870):

```go
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()

	// wake-on-call FIRST: a request that reached this process through the catch-all router carries
	// a PREVIEW hostname, and if it belongs to a sleeping stack the whole request is the visitor's.
	// Cheap when nothing sleeps: one lookup.
	if s.sleepIndex.Size() > 0 {
```

with:

```go
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()

	// A service hostname that reached THIS process came through the wake catch-all: its own router is
	// gone (container stopped, starting or unhealthy) or, on an added domain, never existed. r.Host,
	// not requestHost: forwardAuth's calls carry X-Forwarded-Host grafana.<domain> but Host pstack:7878.
	// The prefix test comes first: IsControlHostname reads the domains file.
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

	// wake-on-call before the UI and the API: a request that reached this process through the
	// catch-all router carries a PREVIEW hostname, and if it belongs to a sleeping stack the whole
	// request is the visitor's. Cheap when nothing sleeps: one lookup.
	if s.sleepIndex.Size() > 0 {
```

- `strings`, `net/http` and `portRe` are already in package `api`, so there are no import changes.
- Add no nil guard on `s.routing`. The prefix `&&` short-circuits first, so `&Server{}` tests (ssoTestServer, wakingServer) never reach the call, and `IsControlHostname` is safe on a nil receiver anyway.
- The check stays in `handle()`, not `preGate` or `wakeFor`. It has to cover `/`, the UI serve at :878. `wakeFor` does not run at all when nothing sleeps.

- [ ] **Step 4: Run it and watch it pass, then run each negative control.**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ -run TestServiceHostnamesAreNeverServedByPstack -v
```

Expected: `--- PASS: TestServiceHostnamesAreNeverServedByPstack` with all 5 subtests PASS, then `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api`.

For each mutation: apply it to server.go, rerun the command above, confirm the named failure, then revert it exactly.

| Mutation | Fails | With |
|---|---|---|
| Delete the whole refusal block (the parent's control) | grafana, loki, added subtests | `grafana.preview.example.com/: 200 "" (text/html; charset=utf-8), want 503 …` (and the loki/added equivalents) |
| Delete `case "grafana." + d:` and its `http.Error` line | grafana subtest | `grafana.preview.example.com/: 404 "Not found.\n" (text/plain; charset=utf-8), want 503 "Grafana is not running.\n" as text/plain` |
| Delete `case "loki." + d:` and its `http.Error` line | loki subtest | `loki.preview.example.com/: 404 "Not found.\n" …, want 503 "Loki is not running.\n" as text/plain` |
| Replace `s.routing.IsControlHostname(h, s.opts.Domain)` with `(h == "grafana."+strings.ToLower(s.opts.Domain) \|\| h == "loki."+strings.ToLower(s.opts.Domain))` | added subtest | `grafana.added.example/: 200 "" (text/html; charset=utf-8), want 404 "Not found.\n" as text/plain` |
| Replace `strings.ToLower(portRe.ReplaceAllString(r.Host, ""))` with `requestHost(r)` | verify subtest | `verify: 503 "Grafana is not running.\n", want 401 with no body` |
| Drop `(strings.HasPrefix(h, "grafana.") \|\| strings.HasPrefix(h, "loki.")) &&` | control subtest | `control.: 404 (text/plain; charset=utf-8), want 200 text/html` |

After the last revert, `but diff` shows only Step 3's hunk in server.go. Then run the whole package, including the wake tests (TestWakeFor…, TestWakeVerdict), and vet and format:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/api/ && go vet ./... && gofmt -l internal
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/api	…`, no vet output, and no files listed by gofmt.

- [ ] **Step 5: Docs — control-plane.md.** In `docs/control-plane.md`, replace the heading line

```markdown
## 6. Submitting a deployment
```

with

```markdown
### A service hostname is never pstack's

`grafana.<domain>` and `loki.<domain>` reach pstack only through the wake catch-all (`pstack-wake`,
priority 1). That happens when the service's own router is gone (container stopped, starting or
unhealthy), or, on an added domain, never existed. `handle()` refuses them before wake-on-call, the
UI and the API, in plain text:

| Host | Answer |
|---|---|
| `grafana.<primary>` | `503 Grafana is not running.` |
| `loki.<primary>` | `503 Loki is not running.` |
| `grafana.` or `loki.` of an added domain | `404 Not found.` |

Serving the UI there would let a password typed on `grafana.<domain>` set `pstack_session` on that
host, and Traefik would then forward that cookie to Grafana on every request. A log push that meets
the 503 is retried and dropped, instead of reading pstack's `200` HTML as delivered. Loki's router
matches only the push path, so every other path on `loki.<primary>` gets this 503 even while Loki
runs.

The check reads `Host`, never `requestHost`. forwardAuth's calls arrive with `Host: pstack:7878` and
`X-Forwarded-Host: grafana.<domain>`, and verify must answer them. The check also runs with logging
off, because `IsControlHostname` always includes `grafana.` and `loki.`. The prefix test runs first
because `IsControlHostname` reads the domains file.

## 6. Submitting a deployment
```

- [ ] **Step 6: Docs — usage.md.** First confirm the anchor is unique:

```bash
cd /Volumes/S1/code/preview-stacks && grep -c '^  the dashboards it owns\.$' docs/usage.md
```

Expected: `1` (the last line of T8's last `### Grafana` bullet, `- **A username deleted and created again** …`).

In `docs/usage.md`, replace the line

```markdown
  the dashboards it owns.
```

with

```markdown
  the dashboards it owns.
- **Grafana down.** `grafana.<domain>` shows `Grafana is not running.`
```

The blank line after it and the `### Why \`init\` is CLI-only, and always will be` heading stay as T8 left them. Check:

```bash
cd /Volumes/S1/code/preview-stacks && grep -c '^- \*\*Grafana down.\*\*' docs/usage.md
```

Expected: `1`.

- [ ] **Step 7: Commit.**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these four changes: `packages/pstack/internal/api/server.go`, `packages/pstack/internal/api/routes_grafana_test.go`, `docs/control-plane.md` and `docs/usage.md`. Never take `packages/conformance/golden/host/db/*` or `.superpowers/`.

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging-grafana -m "fix(api): grafana. and loki. are never served pstack's UI or API" <id-server.go> <id-routes_grafana_test.go> <id-control-plane.md> <id-usage.md>
```

---

### Task 10: ui: the Grafana link and the login return to /api/

**Files:**
- Create: none
- Modify: `apps/ui/src/composables/useAuth.ts`, `apps/ui/src/App.vue`, `apps/ui/src/views/LoginView.vue`, `packages/pstack/ui/index.html`, `docs/usage.md`. Anchor every edit by the quoted existing text. Line numbers are advisory: slice 2 edits `packages/pstack/ui/index.html` (its action label), and T8/T9 insert the usage.md Grafana section.
- Test: none. This app has no component harness (`apps/ui/test/useJobQueue.test.ts:1-9`). The red check is `vue-tsc` plus a browser run against a shimmed `pstack serve`.

**Interfaces:**
- Consumes:
  - T7: `/api/health` carries `"grafana": "https://grafana.<domain>"`, appended last and only when `grafanaOn()` (Domain, Token and the cached docker flag). It is absent otherwise, so a loopback no-token server never has it (`principal.go:26-28`; `grafanaOn` needs `Token != ""`).
  - T8: `GET /api/auth/grafana/start?state=<43 b64url>&next=<path>`. Signed out: 302 `/login?next=` + `js.EncodeURIComponent("/api/auth/grafana/start?state="+S+"&next="+js.EncodeURIComponent(next))`. Signed in (a `pstack_session`): 302 `https://grafana.<domain>/-/pstack/callback?code=<K>`.
  - T9: the usage.md Grafana section's bullet `- **Grafana down.** \`grafana.<domain>\` shows \`Grafana is not running.\``.
  - Existing: `api.url(path)` (`apps/ui/src/api/client.ts:40-51`, exported at `:145`); `settings.token` (`useSettings.ts:46-49`); `can(min: Role)` (`useAuth.ts:63-67`); the router guard that sends an authed visitor from `/login` to `/` (`router.ts:79-80`); pstack serving the basic UI for every non-`/api/` path (`server.go:877-889`).
- Produces:
  - `authState.grafana: string | null` in `useAuth.ts`, read from `/api/health` in `checkAuth()`.
  - App.vue nav link: `<a v-if="authState.grafana && !settings.token && can('developer')" :href="authState.grafana" class="navlink" target="_blank" rel="noopener">`, with `ScrollText` and the label `Grafana`, right after the Jobs `RouterLink`.
  - LoginView `submit()`: `if (next.value.startsWith('/api/')) window.location.assign(api.url(next.value)); else void router.replace(next.value);`
  - Basic UI `signIn()` honours `?next=` starting `/api/`. `signInWithProvider()` passes `?next=` when it starts with `/`.
  - T12 names "the Grafana nav link" in the CHANGELOG from this task.

- [ ] **Step 1: Write the failing check (the link, before its state exists)**

  In `apps/ui/src/App.vue`, add the icon to the lucide import (`:32-33`). Replace:
  ```ts
    Plus,
    ServerCog,
  ```
  with:
  ```ts
    Plus,
    ScrollText,
    ServerCog,
  ```

  Add the link right after the Jobs entry (`:146-150`). The count span is written as the file has it, with the `▸` escape (`App.vue:149`). Replace:
  ```vue
          <RouterLink to="/jobs" class="navlink" title="g j">
            <ClockFading :size="17" aria-hidden="true" />
            <span>Jobs</span>
            <span class="count">{{ runningJobs ? `${runningJobs}▸` : state.jobs.length }}</span>
          </RouterLink>
  ```
  with:
  ```vue
          <RouterLink to="/jobs" class="navlink" title="g j">
            <ClockFading :size="17" aria-hidden="true" />
            <span>Jobs</span>
            <span class="count">{{ runningJobs ? `${runningJobs}▸` : state.jobs.length }}</span>
          </RouterLink>

          <!-- Developer and up: verify refuses viewers (Grafana reads unredacted lines). Hidden with a
               stored token: no pstack_session, so Grafana's sign-in would end on / (router.ts guard). -->
          <a
            v-if="authState.grafana && !settings.token && can('developer')"
            :href="authState.grafana"
            class="navlink"
            target="_blank"
            rel="noopener"
          >
            <ScrollText :size="17" aria-hidden="true" />
            <span>Grafana</span>
          </a>
  ```

  The template now reads `settings`, so the placeholder that kept the import alive is stale. Remove it (`:78-84`). Replace:
  ```ts
  const runningJobs = computed(() => state.jobs.filter((j) => j.state === 'running').length);

  // The old "token required" warning is gone with 0.10.0: nobody sees this rail without already
  // being authenticated (session, personal token, or the machine token), so the warning could only
  // ever appear when it was already false.
  void settings;
  </script>
  ```
  with:
  ```ts
  const runningJobs = computed(() => state.jobs.filter((j) => j.state === 'running').length);
  </script>
  ```

- [ ] **Step 2: Run it and watch it fail**

  `cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck`

  Expected: exit non-zero, with `src/App.vue:…: error TS2339: Property 'grafana' does not exist on type` (twice: `v-if` and `:href`). `authState` has no `grafana` yet.

- [ ] **Step 3: Implement authState.grafana**

  In `apps/ui/src/composables/useAuth.ts`, add the field to `authState` (`:39-40`). Replace:
  ```ts
    sso: null as { providers: Array<{ key: string; label: string; preset: string }> } | null,
  });
  ```
  with:
  ```ts
    sso: null as { providers: Array<{ key: string; label: string; preset: string }> } | null,
    /** From /api/health: `https://grafana.<domain>` on a host running Grafana, else null. */
    grafana: null as string | null,
  });
  ```

  Read it in `checkAuth()` (`:70-77`). Replace:
  ```ts
      sso?: { providers: Array<{ key: string; label: string; preset: string }> } | null;
    }>('/api/health');
    if (health.ok) {
      authState.hasUsers = health.body.hasUsers ?? null;
      authState.sso = health.body.sso ?? null;
    }
  ```
  with:
  ```ts
      sso?: { providers: Array<{ key: string; label: string; preset: string }> } | null;
      grafana?: string;
    }>('/api/health');
    if (health.ok) {
      authState.hasUsers = health.body.hasUsers ?? null;
      authState.sso = health.body.sso ?? null;
      authState.grafana = health.body.grafana ?? null;
    }
  ```
  `useControlPlane.loadHealth` is not touched. `checkAuth` already runs at startup and again after a password login (`LoginView.vue:73`).

- [ ] **Step 4: Run it and watch it pass**

  `cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck && bun run test`

  Expected: typecheck exits 0, and `bun test` reports `0 fail` (the existing `test/useJobQueue.test.ts` and `test/useVarFormats.test.ts`).

- [ ] **Step 5: Start a shimmed host for the browser checks**

  T7 and T8 are in the tree, so pstack serves the health key and start. Shell state does not carry between commands, so every block below sets `S` first. Commands:
  ```sh
  S=${TMPDIR:-/tmp}/t10
  rm -rf $S/pstack-g $S/pstack-g-shim && mkdir -p $S/pstack-g-shim
  cat > $S/pstack-g-shim/docker <<'EOF'
  #!/bin/sh
  case "$*" in
    "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\n' 'gr4f' ;;
    "inspect gr4f") printf '%s\n' '[{"Id":"gr4f","Name":"/pstack-control-grafana-1","Config":{"Image":"grafana/grafana:13.2.1","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"grafana"}},"State":{"Status":"running"}}]' ;;
    *) exit 0 ;;
  esac
  EOF
  chmod 755 $S/pstack-g-shim/docker
  ```
  The two arms are T11's `GRAFANA_SHIM`, and `*) exit 0` is `dockerShim`'s fallback (`harness/docker-shim.ts:30`).

  Then, in the background:
  ```sh
  S=${TMPDIR:-/tmp}/t10
  cd /Volumes/S1/code/preview-stacks && PSTACK_TOKEN=dev PSTACK_DOMAIN=preview.example.com PSTACK_DATA=$S/pstack-g PATH=$S/pstack-g-shim:$PATH go run ./packages/pstack/cmd/pstack serve
  ```
  `PSTACK_DATA` is `registry.go:474-479`. The port defaults to 7878 and the host to 127.0.0.1 (`serve.go:47-60`).

  ```sh
  curl -s http://127.0.0.1:7878/api/health
  # expect …,"grafana":"https://grafana.preview.example.com"}
  curl -s -X POST http://127.0.0.1:7878/api/auth/bootstrap -H 'authorization: Bearer dev' -d '{"username":"sami","password":"correct-horse"}'
  # expect {"user":{…"username":"sami","role":"admin"…}}
  curl -s -X POST http://127.0.0.1:7878/api/users -H 'authorization: Bearer dev' -d '{"username":"dana","password":"correct-horse","role":"developer"}'
  # expect {"user":{"id":2,"username":"dana","role":"developer",…}}
  ```

  Also in the background: `cd /Volumes/S1/code/preview-stacks/apps/ui && bun run dev`. It serves `http://localhost:5273` and proxies `/api` to 127.0.0.1:7878 (`vite.config.ts`).

  Session cookies are per host, not per port. Sign out before each sign-in check below. Without `x-forwarded-proto: https` the cookie has no `Secure`, so plain http works (`http.go:164-170`).

- [ ] **Step 6: Watch the /api/ return fail in both UIs (before the edits)**

  The sign-in URL (43 `A`s pass `^[A-Za-z0-9_-]{43}$`):
  `/login?next=%2Fapi%2Fauth%2Fgrafana%2Fstart%3Fstate%3DAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA%26next%3D%252F`

  - Advanced UI, signed out: open `http://localhost:5273` + that URL and sign in as `sami` / `correct-horse`. Expected (red): the SPA renders the `Not found` view at `/api/auth/grafana/start…`, and the network tab has NO `document` request to `/api/auth/grafana/start`. `router.replace` is client-side.
  - Basic UI, signed out: open `http://127.0.0.1:7878` + that URL and sign in from the header row. Expected (red): the page stays at `/login?next=…` showing `sami`, with no navigation.

- [ ] **Step 7: Implement both login returns**

  In `apps/ui/src/views/LoginView.vue` `submit()` (`:73-76`), replace:
  ```ts
    await checkAuth();
    // Back to wherever the guard bounced them from, or home.
    void router.replace(next.value);
  }
  ```
  with:
  ```ts
    await checkAuth();
    // Back to wherever the guard bounced them from, or home. An /api/ next (Grafana's sign-in) is a
    // server route the router would render as NotFound: a full navigation.
    if (next.value.startsWith('/api/')) window.location.assign(api.url(next.value));
    else void router.replace(next.value);
  }
  ```
  `api` is already imported (`LoginView.vue:16`). `next` already requires a leading `/` (`:30-32`). The SSO button already carries `next` (`:57-61`) and needs no change.

  In `packages/pstack/ui/index.html` (`:1672-1678`), replace:
  ```js
       * read. `next` is a same-origin path — the server refuses anything else.
       */
      signInWithProvider() {
        location.assign('/api/auth/sso/start?next=' + encodeURIComponent(location.pathname === '/login' ? '/' : location.pathname + location.hash));
      },
  ```
  with:
  ```js
       * read. `next` is a same-origin path — the server refuses anything else. A `?next=` on the page
       * (Grafana's sign-in sends one) wins over the page's own path.
       */
      signInWithProvider() {
        const next = new URLSearchParams(location.search).get('next') || '';
        location.assign('/api/auth/sso/start?next=' + encodeURIComponent(next.startsWith('/') ? next : (location.pathname === '/login' ? '/' : location.pathname + location.hash)));
      },
  ```

  And in `signIn()` (`:1683-1684`), replace:
  ```js
        if (!r.ok) { this.loginError = r.body.error || 'sign-in failed'; return; }
        this.me = { root: false, user: r.body.user };
  ```
  with:
  ```js
        if (!r.ok) { this.loginError = r.body.error || 'sign-in failed'; return; }
        // An /api/ next is Grafana's sign-in start, a server route: leave for it.
        const next = new URLSearchParams(location.search).get('next') || '';
        if (next.startsWith('/api/')) { location.assign(next); return; }
        this.me = { root: false, user: r.body.user };
  ```
  The basic UI routes on `location.hash` (`onHash`/`parseRoute`, `:1602-1603`), so `location.search` is intact at `/login?next=…`. `/api/` is a same-origin path prefix and can never be `//host`. The basic UI gets no nav link (spec, out of scope).

- [ ] **Step 8: Watch everything pass in the browser**

  Restart the Step 5 `go run … serve`: `ui/index.html` is embedded at build time (`assets.go:10`). The data dir keeps `sami` and `dana`. Vite hot-reloads the SPA. Look with the browser tool, following `docs/ui-rules.md`: the link uses the existing `.navlink` class (`app.css:187-215`), so its height, gap and focus ring match its neighbours.

  - (a) Signed in at `http://localhost:5273` as `sami` (admin): the rail shows `Grafana` right below `Jobs`, with `href="https://grafana.preview.example.com"`, `target="_blank"` and `rel="noopener"`. Clicking opens a new tab, which does not resolve locally. That is expected. **Take a screenshot of the rail for the report.**
  - (b) Sign out, then sign in as `dana` (developer): the link is visible. With the bearer, run `curl -s -X PATCH http://127.0.0.1:7878/api/users/2 -H 'authorization: Bearer dev' -d '{"role":"viewer"}'` and expect `{"updated":2,"role":"viewer"}`. The link stays until the page loads again. Reload: the link is hidden. Use a second account because the last admin cannot be demoted (`auth.go:324-367`).
  - (c) As `sami`, save `dev` as the token in Settings: the link is hidden right away (`settings.token` is reactive). Clear the token again: it is back.
  - (d) Sign out, open `http://localhost:5273/login?next=%2Fapi%2Fauth%2Fgrafana%2Fstart%3Fstate%3DAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA%26next%3D%252F`, and sign in as `sami`. The network tab shows a `document` request to `/api/auth/grafana/start?state=AAAA…&next=%2F` answered `302`, with `Location: https://grafana.preview.example.com/-/pstack/callback?code=…`. The onward load fails DNS locally, which is expected and not a bug.
  - Basic UI: sign out, open `http://127.0.0.1:7878/login?next=` plus the same encoded value, and sign in from the header row. The same `document` request to `/api/auth/grafana/start…` appears with a 302 to the callback.
  - Sanity: signed out at `http://localhost:5273/login?next=%2Fjobs`, a sign-in still lands on `/jobs` inside the SPA, with no document request.

  Report, with the screenshot of (a), this note: the link changes only on page load (`checkAuth`). A role change or Grafana starting or stopping shows after a reload; a token saved in Settings hides it at once.

  Stop both background processes. Then:
  ```sh
  S=${TMPDIR:-/tmp}/t10
  rm -rf $S/pstack-g $S/pstack-g-shim
  ```

- [ ] **Step 9: Docs**

  In `docs/usage.md`, add a bullet to the `### Grafana` section, right after T9's bullet. Replace:
  ```md
  - **Grafana down.** `grafana.<domain>` shows `Grafana is not running.`
  ```
  with:
  ```md
  - **Grafana down.** `grafana.<domain>` shows `Grafana is not running.`
  - **Nav link.** The advanced UI links to Grafana for developer and above; hidden with a stored token.
  ```
  Check the anchor first: `grep -c '^- \*\*Grafana down\.\*\*' docs/usage.md` prints `1`.

- [ ] **Step 10: UI gate**

  `cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck && bun run test && bun run build`

  Expected: all exit 0. `turbo check` depends on build (`turbo.json:47-53`). `apps/ui/dist` is gitignored (`.gitignore:4`). No Go test asserts on the embedded `ui/index.html` bytes.

- [ ] **Step 11: Commit**

  `but status` to get the file IDs for exactly these five files: `apps/ui/src/composables/useAuth.ts`, `apps/ui/src/App.vue`, `apps/ui/src/views/LoginView.vue`, `packages/pstack/ui/index.html`, `docs/usage.md`. Nothing under `packages/conformance/golden/host/db/` or `.superpowers/`.

  `but commit -b claude/loki-logging-grafana -m "feat(ui): Grafana link, and sign-in returns to an /api/ next" <ids>`

---

### Task 11: conformance: Grafana sign-in over HTTP; loki goldens regenerated

**Files:**
- Create: `packages/conformance/test/api-grafana.test.ts`
- Modify: `packages/conformance/gen/goldens.table.ts` (the `init-${name}` row inside `...LOKI_CELLS.flatMap`, as slice-1 T13 wrote it at plan:6795. Edit by quoted text: slices 1 and 2 move line numbers)
- Modify: `packages/conformance/test/cli-goldens.test.ts` (the header paragraph slice-1 T13 rewrote, plan:6837-6839)
- Modify: `packages/conformance/expected-pass.json` (one new key between `api-domains` and `api-host-vars`)
- Modify (generated by `bun gen/goldens.ts`, never by hand): `packages/conformance/golden/render/control/http01-basic-compose-loki/docker-compose.yml`, `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/docker-compose.yml`, `packages/conformance/golden/cli/init-dry-http01-basic-compose-loki.json`, `packages/conformance/golden/cli/init-http01-basic-compose-loki.json`, `packages/conformance/golden/cli/init-dry-dns01-advanced-swarm-loki.json`, `packages/conformance/golden/cli/init-dns01-advanced-swarm-loki.json`
- Create (generated): `packages/conformance/golden/render/control/http01-basic-compose-loki/grafana/datasources/loki.yaml`, `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/grafana/datasources/loki.yaml`
- Test: `packages/conformance/test/api-grafana.test.ts`, `packages/conformance/test/cli-goldens.test.ts` (replays the table; only its header changes)

Not in this task: anything under `golden/host/**`. The shim at host-fixture.ts:38-39 lists only traefik for the control project, so `/api/health` has no `grafana` key there. Never run gen/host-fixture.ts, and never commit `golden/host/db/pstack.db-shm` or `pstack.db-wal` (untracked, predate this work).

**Interfaces:**
- Consumes:
  - T8, the wire contract (C6, C4):
    - `GET /api/auth/grafana/verify` answers 302, 401 (empty body), 403 (`No Grafana access.\n` / `Cross-origin request refused.\n`), 404 (`Not found.\n`), or 204 with `X-WEBAUTH-USER` and `X-WEBAUTH-ROLE`.
    - It answers the callback path `/-/pstack/callback?code=K` itself.
    - `GET /api/auth/grafana/start?state=S&next=N` answers an absolute 302 to `https://grafana.<domain>/-/pstack/callback?code=K` when signed in, or a relative 302 to `/login?next=…` when not.
    - Cookies:
      - `__Host-pstack_grafana=<64-hex id_hash>.<43-char b64url MAC>; Path=/; Secure; HttpOnly; SameSite=Lax` (no Max-Age)
      - `__Host-pstack_grafana_state=<43 chars>; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=900`, cleared with `Max-Age=0`
    - `redirect` adds Set-Cookie headers in argument order with `Header.Add` (routes_auth.go:60-67), so the callback's two cookies arrive session first, then state.
  - T7: the `/api/health` `grafana` key (`https://grafana.<domain>`), present only when `grafanaOn()`. `Start` stores `inspect.GrafanaOn(s.host)` before `go s.reindexLoop()`, and `go s.http.Serve(ln)` is the last statement of Start (server.go:792-805). So the first health answer the harness sees is already right, and no `until` is needed.
  - T9: a request whose `r.Host` is `grafana.<primary>` answers `503 Grafana is not running.\n` (text/plain) before wake and before the UI.
  - T6, Init under `--logging loki`:
    - It writes `control/grafana/datasources/loki.yaml` (0644).
    - It adds reqs `loki image` and `grafana image`, which dry-run prints as `  [dry-run] requires loki image` / `  [dry-run] requires grafana image`.
    - It creates `grafana/datasources` with `ensureDir`, printed `  [dry-run] mkdir -p <DATA>/control/grafana/datasources`.
    - It prints the summary line `  grafana   https://grafana.<domain>`.
  - slice1-T13: the `LOKI_CELLS` flatMap's `init-${name}` row with `render.files` ending `'control/loki/config.yaml']`, and the cli-goldens.test.ts header naming `loki/config.yaml`.
  - Harness, existing:
    - `bootServer(o: BootOptions): Promise<Booted>`. Its `domain` becomes `PSTACK_DOMAIN` and its `pathPrefix` goes first on PATH (server.ts:21-38, :97, :103). The token defaults to `DEFAULT_TOKEN`, and `Booted.H` is its bearer plus JSON content-type (server.ts:64-65).
    - `dockerShim(arms): Shim` with `.dir` / `.remove()` (docker-shim.ts:24-47), and `ALWAYS_OK = ''` (docker-shim.ts:50).
    - gen/goldens.ts:53-60 copies each `render.files` entry with `mkdirSync(dirname(dst), { recursive: true })`, so a nested `grafana/datasources/` path needs no generator change.
    - cli-goldens.test.ts:56-66 compares each entry: existence must match, then content after masking.
- Produces:
  - `const GRAFANA_SHIM` (local to api-grafana.test.ts, C7):
    ```ts
    const GRAFANA_SHIM = [
      `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'gr4f' ;;`,
      `  "inspect gr4f") printf '%s\\n' '[{"Id":"gr4f","Name":"/pstack-control-grafana-1","Config":{"Image":"grafana/grafana:13.2.1","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"grafana"}},"State":{"Status":"running"}}]' ;;`,
    ].join('\n');
    ```
  - `describe('Grafana sign-in', …)` with 4 tests:
    - `verify → start → callback → 204: a pstack session becomes a Grafana sign-in`
    - `a pstack sign-out ends Grafana access on the next request`
    - `grafana.<domain> reaching pstack answers 503, never the UI`
    - `off: /api/health has no grafana key, and verify and start answer 404`
  - LOKI_CELLS `init-${name}` row: `files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env', 'control/loki/config.yaml', 'control/grafana/datasources/loki.yaml']`.
  - `expected-pass.json`: `"test/api-grafana.test.ts": 4`. `test/cli-goldens.test.ts` keeps whatever slices 1-2 left (94 per R3): no CASES row is added.
  - The changed-golden list T12's CHANGELOG names:
    - `render/control/{http01-basic-compose-loki,dns01-advanced-swarm-loki}/docker-compose.yml` (changed)
    - `…/grafana/datasources/loki.yaml` (new)
    - `cli/init-dry-{http01-basic-compose,dns01-advanced-swarm}-loki.json`
    - `cli/init-{http01-basic-compose,dns01-advanced-swarm}-loki.json`

Order of work: the goldens go first (Steps 1-6), because T6 has already turned the four `init*-loki` rows red. The HTTP test follows (Steps 7-10), then the ratchet and commit (Steps 11-13).

- [ ] **Step 1: Write the failing test — the table names the datasource file**

  In `/Volumes/S1/code/preview-stacks/packages/conformance/gen/goldens.table.ts`, inside `...LOKI_CELLS.flatMap((c) => {`, replace this row (as slice-1 T13 wrote it; if slice 2 changed anything else in the row, the tree wins and only `render.files` gains the entry):
  ```ts
        { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env', 'control/loki/config.yaml'] } } as Case,
  ```
  with:
  ```ts
        // negative control: drop Init's control/grafana/datasources/loki.yaml write and rebuild —
        // init-<cell>-loki fails: the file's golden exists and the live file does not.
        { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env', 'control/loki/config.yaml', 'control/grafana/datasources/loki.yaml'] } } as Case,
  ```

  In `/Volumes/S1/code/preview-stacks/packages/conformance/test/cli-goldens.test.ts`, replace the header paragraph slice-1 T13 wrote:
  ```ts
   * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells, plus
   * loki/config.yaml for the two Loki cells) is compared the same way: it is what `upgrade` reads
   * back, and what keeps the letsencrypt volume named the same across versions.
  ```
  with:
  ```ts
   * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells, plus
   * loki/config.yaml and grafana/datasources/loki.yaml for the two Loki cells) is compared the same
   * way: it is what `upgrade` reads back, and what keeps the letsencrypt volume named the same across
   * versions.
  ```

- [ ] **Step 2: Run it and watch it fail**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
  ```
  Expected: typecheck is clean. Exactly 4 rows fail, each on its first stdout comparison (`expect(mask(r.stdout, version)).toBe(golden.stdout)`):
  - `init-dry-http01-basic-compose-loki` and `init-dry-dns01-advanced-swarm-loki`: the requires, mkdir and write lines and the compose byte count.
  - `init-http01-basic-compose-loki` and `init-dns01-advanced-swarm-loki`: the `  grafana   https://grafana.preview.example.com` line.

  Every other row passes, including `upgrade-plan-*-loki`, `logging-loki-dry-dns01-advanced-swarm` and `logging-off-dry-*-loki`. They print step labels only (`[dry-run] re-run init with logging …`, SwitchLogging at plan:2233-2235, and the same shape as golden/cli/ui-switch-dry-http01-basic-compose.json), never init's own lines. These 4 have been red since T6 changed Init's output. This step proves nothing else is. If any other row fails, stop: the owning task (T5/T6) changed logging-off output or a non-init transcript.

- [ ] **Step 3: Snapshot the loki transcripts, then regenerate**

  Keep the old stdout so the line-level check in Step 4 has something to compare against. A golden's `stdout` is one JSON string, so `but diff` shows it as a single changed line.
  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance
  rm -rf "${TMPDIR:-/tmp}/t11-old-goldens" && mkdir -p "${TMPDIR:-/tmp}/t11-old-goldens" && cp golden/cli/*-loki*.json "${TMPDIR:-/tmp}/t11-old-goldens/"
  bun gen/goldens.ts
  ```
  Leave PSTACK_IMPL unset (gen/goldens.ts:15 throws for anything but `go`). Expected: one `  <name>: exit <code>` line per row, every loki row `exit 0`. The final `wrote <N> goldens` count is the same as before this task, because no row was added.

- [ ] **Step 4: Audit the diff — only Grafana's lines may change**

  ```sh
  cd /Volumes/S1/code/preview-stacks && but status
  ```
  Under `packages/conformance/golden/`, the only paths from this task are:
  - M `render/control/http01-basic-compose-loki/docker-compose.yml`
  - M `render/control/dns01-advanced-swarm-loki/docker-compose.yml`
  - A `render/control/http01-basic-compose-loki/grafana/datasources/loki.yaml`
  - A `render/control/dns01-advanced-swarm-loki/grafana/datasources/loki.yaml`
  - M `cli/init-dry-http01-basic-compose-loki.json`, M `cli/init-http01-basic-compose-loki.json`
  - M `cli/init-dry-dns01-advanced-swarm-loki.json`, M `cli/init-dns01-advanced-swarm-loki.json`

  Also expect the untracked `golden/host/db/pstack.db-shm` and `pstack.db-wal`, which were there before this work. `.env`, `dns.env` and `loki/config.yaml` in both loki dirs must not appear. Any other golden in the list is either logging-off output leaking or nondeterminism from slice 1 or 2. Stop, fix the owning task, and never commit over it.

  ```sh
  cd /Volumes/S1/code/preview-stacks && but diff
  ```
  Each compose diff is two hunks and nothing else:
  - the whole `  # Grafana, with --logging loki: …` / `  grafana:` service block, added after the loki service;
  - `  grafana:` added under `  loki:` in the top-level `volumes:`.

  Line-level transcript check:
  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun -e '
  const { readdirSync } = await import("node:fs");
  const old = `${process.env.TMPDIR ?? "/tmp"}/t11-old-goldens`;
  for (const f of readdirSync(old).filter((f) => f.endsWith(".json"))) {
    const a = (await Bun.file(`${old}/${f}`).json()).stdout.split("\n");
    const b = (await Bun.file(`golden/cli/${f}`).json()).stdout.split("\n");
    const gone = a.filter((l) => !b.includes(l)), added = b.filter((l) => !a.includes(l));
    if (gone.length || added.length) console.log(f, JSON.stringify({ gone, added }, null, 2));
  }
  for (const f of ["init-dry-http01-basic-compose-loki", "init-dry-dns01-advanced-swarm-loki"]) {
    const o = (await Bun.file(`golden/cli/${f}.json`).json()).stdout;
    console.log(f); console.log(o.split("\n").filter((l) => /requires|mkdir|write|  logging   |  grafana   /.test(l)).join("\n"));
  }'
  ```
  Expected:
  - Exactly four files are listed.
  - `init-http01-basic-compose-loki.json` and `init-dns01-advanced-swarm-loki.json`: `gone: []`, `added: ["  grafana   https://grafana.preview.example.com"]`.
  - `init-dry-http01-basic-compose-loki.json` and `init-dry-dns01-advanced-swarm-loki.json`:
    - `gone` is only the old `  [dry-run] write <DATA>/control/docker-compose.yml (<old> bytes, mode 644)`.
    - `added` is exactly:
      - `  [dry-run] requires loki image`
      - `  [dry-run] requires grafana image`
      - `  [dry-run] mkdir -p <DATA>/control/grafana/datasources`
      - `  [dry-run] write <DATA>/control/docker-compose.yml (<new> bytes, mode 644)`
      - `  [dry-run] write <DATA>/control/grafana/datasources/loki.yaml (<n> bytes, mode 644)`
      - `  grafana   https://grafana.preview.example.com`
    - `<new>` is greater than `<old>`.
  - In the printed order:
    - The two requires lines come right after `requires control image` (http01-basic) and `requires advanced UI image` (dns01-advanced).
    - The mkdir comes right after `mkdir -p <DATA>/control/loki`.
    - The loki.yaml write comes right after `write <DATA>/control/loki/config.yaml`.
    - `  grafana   …` comes right after `  logging   loki at https://loki.preview.example.com …`.

  If `logging-*-dry-*` or `upgrade-plan-*-loki` is listed, accept it only if its hunks are exactly these lines (C11). Anything else: stop.

  Render content:
  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance/golden/render/control
  cmp http01-basic-compose-loki/grafana/datasources/loki.yaml /Volumes/S1/code/preview-stacks/packages/pstack/templates/control/grafana/datasources.yaml && cmp http01-basic-compose-loki/grafana/datasources/loki.yaml dns01-advanced-swarm-loki/grafana/datasources/loki.yaml && echo datasource byte-exact
  grep -c 'routers.pstack-grafana.tls.certresolver=le' http01-basic-compose-loki/docker-compose.yml dns01-advanced-swarm-loki/docker-compose.yml   # :1, then :0
  grep -cE '^  grafana:$' http01-basic-compose-loki/docker-compose.yml dns01-advanced-swarm-loki/docker-compose.yml                          # :2 each (service + volume)
  grep -c 'image: grafana/grafana:13.2.1' http01-basic-compose-loki/docker-compose.yml dns01-advanced-swarm-loki/docker-compose.yml         # :1 each
  cd /Volumes/S1/code/preview-stacks && git check-ignore -v --no-index packages/conformance/golden/render/control/http01-basic-compose-loki/grafana/datasources/loki.yaml; echo "exit $?"   # exit 1 (not ignored)
  ```

- [ ] **Step 5: Run it and watch it pass**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts test/host-fixture.test.ts
  ```
  Expected: `0 fail`. cli-goldens passes the count slices 1-2 recorded (94), and host-fixture passes in full.

- [ ] **Step 6: Break the new render entry (negative control)**

  In `/Volumes/S1/code/preview-stacks/packages/pstack/internal/initctl/init.go`, delete the block T6 added right after the loki config write:
  ```go
  	if logging == Loki {
  		if err := write(out, filepath.Join(controlDir, "grafana", "datasources", "loki.yaml"), pstack.GrafanaDatasources, 0o644, dryRun); err != nil {
  ```
  Delete it through its closing braces (if T6 folded the write into the loki block, delete only the `write(… "loki.yaml" …)` statement and its error check). Then:
  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts -t 'init-http01-basic-compose-loki'
  ```
  Expected: `1 fail`. The stdout matches (a non-dry init prints no step lines), then `expect(existsSync(livePath)).toBe(existsSync(goldenPath))` gets `Expected: true` / `Received: false`. Restore the block by hand, rebuild, rerun: `1 pass`. `but status` shows nothing under `packages/pstack/`.

- [ ] **Step 7: Write the failing test — Grafana sign-in over HTTP**

  Create `/Volumes/S1/code/preview-stacks/packages/conformance/test/api-grafana.test.ts`:
  ```ts
  /**
   * Grafana sign-in over HTTP: forwardAuth's verify, start, and the callback verify answers itself.
   *
   * No Grafana runs. The docker shim plays the control stack's grafana container, which is how pstack
   * learns Grafana is on (inspect.GrafanaOn, read at Start before it serves). Each verify request plays
   * Traefik: it sends the X-Forwarded-* and Sec-Fetch-* headers forwardAuth passes on and reads the
   * answer with redirect: 'manual', as forwardAuth does.
   */
  import { describe, expect, test } from 'bun:test';
  import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';
  import { bootServer, type Booted } from '../harness/server.ts';

  /** The control stack's grafana container. Arms are the argv after bash unquotes it. */
  const GRAFANA_SHIM = [
    `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'gr4f' ;;`,
    `  "inspect gr4f") printf '%s\\n' '[{"Id":"gr4f","Name":"/pstack-control-grafana-1","Config":{"Image":"grafana/grafana:13.2.1","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"grafana"}},"State":{"Status":"running"}}]' ;;`,
  ].join('\n');

  const DOMAIN = 'preview.example.com';
  const G = `https://grafana.${DOMAIN}`;
  const C = `https://control.${DOMAIN}`;
  const SESSION = '__Host-pstack_grafana=';
  const STATE = '__Host-pstack_grafana_state=';
  const ATTRS = '; Path=/; Secure; HttpOnly; SameSite=Lax';
  /** A top-level navigation: the only request verify starts a sign-in for. */
  const NAV = { 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document' };
  /** A fetch from Grafana's frontend. */
  const XHR = { 'sec-fetch-mode': 'cors', 'sec-fetch-dest': 'empty' };

  describe('Grafana sign-in', () => {
    /** A server on a host whose docker answers `arms`. stop() removes the shim too. */
    const boot = async (arms: string): Promise<Booted> => {
      const docker = dockerShim(arms);
      const s = await bootServer({ tag: 'grafana', domain: DOMAIN, pathPrefix: docker.dir });
      return { ...s, stop: async () => { await s.stop(); docker.remove(); } };
    };

    /** What forwardAuth sends pstack for one request to grafana.<domain>. GET by default: with no method, the Origin rule refuses. */
    const verify = (s: Booted, uri: string, h: Record<string, string> = {}) =>
      fetch(`${s.base}/api/auth/grafana/verify`, {
        redirect: 'manual',
        headers: { 'x-forwarded-uri': uri, 'x-forwarded-method': 'GET', 'x-forwarded-host': `grafana.${DOMAIN}`, ...h },
      });

    /** Bootstrap sami (admin) with the bearer and log in: the pstack_session value. */
    const pstackSession = async (s: Booted): Promise<string> => {
      const made = await fetch(`${s.base}/api/auth/bootstrap`, { method: 'POST', headers: s.H, body: JSON.stringify({ username: 'sami', password: 'correct-horse' }) });
      expect(made.status).toBe(201);
      const login = await fetch(`${s.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'sami', password: 'correct-horse' }) });
      expect(login.status).toBe(200);
      return /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';
    };

    /** A browser at G/d/x with no Grafana cookie, signed in to pstack: verify → start → callback. Every answer, for the caller to assert on. */
    const grafanaSignIn = async (s: Booted, session: string) => {
      const first = await verify(s, '/d/x', NAV);
      const state = first.headers.getSetCookie().find((c) => c.startsWith(STATE))?.slice(STATE.length).split(';')[0] ?? '';
      const startAt = new URL(first.headers.get('location') ?? '/', C);
      const start = await fetch(`${s.base}${startAt.pathname}${startAt.search}`, { redirect: 'manual', headers: { cookie: `pstack_session=${session}` } });
      const code = new URL(start.headers.get('location') ?? '/', G).searchParams.get('code') ?? '';
      const callback = await verify(s, `/-/pstack/callback?code=${code}`, { ...NAV, cookie: `${STATE}${state}` });
      /** `__Host-pstack_grafana=<hash>.<mac>`, ready to send back. */
      const cookie = callback.headers.getSetCookie().find((c) => c.startsWith(SESSION))?.split(';')[0] ?? '';
      return { first, state, start, code, callback, cookie };
    };

    // negative control: in grafanaVerify, drop `baseURL(s.opts.Domain, r)+` from the start redirect (a
    // relative Location) — the first Location assertion fails.
    test('verify → start → callback → 204: a pstack session becomes a Grafana sign-in', async () => {
      const s = await boot(GRAFANA_SHIM);
      try {
        const session = await pstackSession(s);
        const f = await grafanaSignIn(s, session);

        // No Grafana cookie, a top-level navigation: an absolute 302 to start, and the state cookie.
        expect(f.first.status).toBe(302);
        expect(f.state).toMatch(/^[A-Za-z0-9_-]{43}$/);
        expect(f.first.headers.get('location')).toBe(`${C}/api/auth/grafana/start?state=${f.state}&next=%2Fd%2Fx`);
        expect(f.first.headers.getSetCookie()).toEqual([`${STATE}${f.state}${ATTRS}; Max-Age=900`]);

        // A fetch, and a navigation inside an <iframe>: a bare 401. Nothing to follow, no state cookie.
        for (const h of [XHR, { 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe' }]) {
          const r = await verify(s, '/d/x', h);
          expect(r.status).toBe(401);
          expect(r.headers.get('location')).toBeNull();
          expect(r.headers.getSetCookie()).toEqual([]);
        }

        // start, signed in: a one-time code for Grafana's callback. Signed out: the login page, returning to start.
        expect(f.start.status).toBe(302);
        expect(f.start.headers.get('location')).toStartWith(`${G}/-/pstack/callback?code=`);
        expect(f.code).toMatch(/^[A-Za-z0-9_-]{43}$/);
        const signedOut = await fetch(`${s.base}/api/auth/grafana/start?state=${f.state}&next=%2Fd%2Fx`, { redirect: 'manual' });
        expect(signedOut.status).toBe(302);
        expect(signedOut.headers.get('location')).toBe(`/login?next=${encodeURIComponent(`/api/auth/grafana/start?state=${f.state}&next=${encodeURIComponent('/d/x')}`)}`);

        // The callback, answered inside verify: back to /d/x with the Grafana cookie, the state cookie cleared.
        expect(f.callback.status).toBe(302);
        expect(f.callback.headers.get('location')).toBe(`${G}/d/x`);
        expect(f.cookie).toMatch(/^__Host-pstack_grafana=[0-9a-f]{64}\.[A-Za-z0-9_-]{43}$/);
        expect(f.callback.headers.getSetCookie()).toEqual([`${f.cookie}${ATTRS}`, `${STATE}${ATTRS}; Max-Age=0`]);

        // The same code again is spent: sign-in starts afresh (a new state cookie) and no Grafana cookie.
        const replay = await verify(s, `/-/pstack/callback?code=${f.code}`, { ...NAV, cookie: `${STATE}${f.state}` });
        expect(replay.status).toBe(302);
        expect(replay.headers.get('location')).toMatch(/^https:\/\/control\.preview\.example\.com\/api\/auth\/grafana\/start\?state=[A-Za-z0-9_-]{43}&next=%2F$/);
        expect(replay.headers.getSetCookie().some((c) => c.startsWith(STATE))).toBe(true);
        expect(replay.headers.getSetCookie().some((c) => c.startsWith(SESSION))).toBe(false);

        // Every request after: 204 with the two headers Grafana trusts. An unsafe method from a preview's origin: 403.
        const ok = await verify(s, '/api/user', { ...XHR, cookie: f.cookie });
        expect(ok.status).toBe(204);
        expect(ok.headers.get('x-webauth-user')).toBe('sami');
        expect(ok.headers.get('x-webauth-role')).toBe('Admin');
        const post = await verify(s, '/api/dashboards/db', { ...XHR, cookie: f.cookie, 'x-forwarded-method': 'POST', origin: `https://pr-1.${DOMAIN}` });
        expect(post.status).toBe(403);
        expect(await post.text()).toBe('Cross-origin request refused.\n');
      } finally {
        await s.stop();
      }
    }, 30_000);

    // negative control: in grafanaUser, cache hash → user in a package-level sync.Map (Load before
    // s.auth.SessionHashUser, Store after) — the verify after sign-out answers 204.
    test('a pstack sign-out ends Grafana access on the next request', async () => {
      const s = await boot(GRAFANA_SHIM);
      try {
        const session = await pstackSession(s);
        const { cookie } = await grafanaSignIn(s, session);
        // The cookie works first, so the 401 below is the sign-out's doing.
        expect((await verify(s, '/api/user', { ...XHR, cookie })).status).toBe(204);

        const out = await fetch(`${s.base}/api/auth/logout`, { method: 'POST', headers: { cookie: `pstack_session=${session}` } });
        expect(out.status).toBe(200);
        const after = await verify(s, '/api/user', { ...XHR, cookie });
        expect(after.status).toBe(401);
        expect(after.headers.get('x-webauth-user')).toBeNull();
      } finally {
        await s.stop();
      }
    }, 30_000);

    // negative control: delete the service-hostname refusal at the top of handle() — `/` serves the
    // embedded UI, 200 text/html.
    test('grafana.<domain> reaching pstack answers 503, never the UI', async () => {
      const s = await boot(GRAFANA_SHIM);
      try {
        // What the wake catch-all forwards once Grafana's own router is gone: the Host, nothing else.
        const res = await fetch(`${s.base}/`, { headers: { host: `grafana.${DOMAIN}` } });
        expect(res.status).toBe(503);
        expect(res.headers.get('content-type')).toStartWith('text/plain');
        expect(await res.text()).toBe('Grafana is not running.\n');
      } finally {
        await s.stop();
      }
    }, 30_000);

    // negative control: drop `&& s.grafana.Load()` from grafanaOn() — the ALWAYS_OK host's health names
    // Grafana and its verify answers 302.
    test('off: /api/health has no grafana key, and verify and start answer 404', async () => {
      const on = await boot(GRAFANA_SHIM);
      try {
        // Start reads docker before it serves, so the first health answer is already right.
        expect(((await (await fetch(`${on.base}/api/health`)).json()) as { grafana?: unknown }).grafana).toBe(G);
      } finally {
        await on.stop();
      }
      // Same domain and token: the 404s below come from docker listing no grafana container, nothing else.
      const off = await boot(ALWAYS_OK);
      try {
        const health = (await (await fetch(`${off.base}/api/health`)).json()) as Record<string, unknown>;
        expect('grafana' in health).toBe(false);
        for (const r of [
          await verify(off, '/d/x', NAV),
          await fetch(`${off.base}/api/auth/grafana/start?state=${'a'.repeat(43)}&next=%2F`, { redirect: 'manual' }),
        ]) {
          expect(r.status).toBe(404);
          expect(await r.text()).toBe('Not found.\n');
        }
      } finally {
        await off.stop();
      }
    }, 30_000);
  });
  ```
  Why these details:
  - Set-Cookie is read with `Headers.getSetCookie()` (Bun 1.3.12; typechecks under the conformance tsconfig), so each cookie is compared as an exact string. `headers.get('set-cookie')` joins them with `, `. Matching uses `startsWith` on the full `name=` (C13), so `__Host-pstack_grafana=` never matches `__Host-pstack_grafana_state=`.
  - The `host` header through Bun's fetch reaches `r.Host` (the precedent is api-share-sleep-swarm.test.ts:173).
  - `redirect: 'manual'` follows api-sso.test.ts:42.
  - The `pstack_session` regex follows api-config.test.ts:408.
  - `js.EncodeURIComponent` keeps the same set as JS's `encodeURIComponent` (js.go:143-150), so the double-encoded `/login?next=` compares exactly.
  - `auth.HashToken` is 64-hex sha256 (auth.go:958, :181-184). `js.B64URL` is unpadded (js.go:256), so a MAC or `sso.RandomB64URL(32)` is 43 characters (sso.go:758-764).

- [ ] **Step 8: Run it and watch it fail against the null server**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck && PSTACK_IMPL=null bun test test/api-grafana.test.ts
  ```
  Expected: typecheck is clean, and the run ends with `0 pass` / `4 fail`.
  - Tests 1 and 2 fail at `expect(made.status).toBe(201)` (received 200).
  - Test 3 fails at `expect(res.status).toBe(503)` (received 200).
  - Test 4 fails at `.grafana).toBe(G)` (received undefined).

- [ ] **Step 9: Run it and watch it pass against the binary**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-grafana.test.ts
  ```
  Expected: `4 pass`, `0 fail`. On a failure, fix the task that owns the route (T7 health, T8 verify/start/callback, T9 refusal) and amend it into that task's unpublished commit. Never loosen an assertion here.

- [ ] **Step 10: Break each test (negative controls)**

  Apply each mutation on its own, in `/Volumes/S1/code/preview-stacks/packages/pstack/internal/api/`. For each: rebuild (`cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`), run the one test, watch it fail, restore the code by hand, rebuild, and rerun to see `1 pass`.
  1. `routes_grafana.go` `grafanaVerify`: replace `redirect(w, baseURL(s.opts.Domain, r)+"/api/auth/grafana/start?state="+state+` with `redirect(w, "/api/auth/grafana/start?state="+state+`.
     Run `PSTACK_IMPL=go bun test test/api-grafana.test.ts -t 'a pstack session becomes'`.
     Expected: `1 fail` at the first Location `toBe`, where Received starts `/api/auth/grafana/start?state=`.
  2. `routes_grafana.go` `grafanaUser`: add `"sync"` to the imports and `var grafanaSeen sync.Map` at package level. Replace:
     ```go
     	u, _ := s.auth.SessionHashUser(hash)
     	return u
     ```
     with:
     ```go
     	if v, ok := grafanaSeen.Load(hash); ok {
     		return v.(*auth.UserRow)
     	}
     	u, _ := s.auth.SessionHashUser(hash)
     	if u != nil {
     		grafanaSeen.Store(hash, u)
     	}
     	return u
     ```
     Run `-t 'sign-out ends Grafana access'`.
     Expected: `1 fail` at `expect(after.status).toBe(401)` (received 204).
  3. `server.go` `handle()`: delete the whole `if h := strings.ToLower(portRe.ReplaceAllString(r.Host, "")); (strings.HasPrefix(h, "grafana.") || strings.HasPrefix(h, "loki.")) && s.routing.IsControlHostname(h, s.opts.Domain) { … return }` block.
     Run `-t 'answers 503, never the UI'`.
     Expected: `1 fail` at `toBe(503)` (received 200).
  4. `routes_grafana.go` `grafanaOn`: replace `return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()` with `return s.opts.Domain != "" && s.opts.Token != ""`.
     Run `-t 'off: '`.
     Expected: `1 fail` at `expect('grafana' in health).toBe(false)` (received true).

  After all four: `cd /Volumes/S1/code/preview-stacks && but status` lists nothing under `packages/pstack/`. Rebuild once more from the restored tree.

- [ ] **Step 11: Record the pass count**

  In `/Volumes/S1/code/preview-stacks/packages/conformance/expected-pass.json`, replace:
  ```json
      "test/api-domains.test.ts": 3,
      "test/api-host-vars.test.ts": 2,
  ```
  with:
  ```json
      "test/api-domains.test.ts": 3,
      "test/api-grafana.test.ts": 4,
      "test/api-host-vars.test.ts": 2,
  ```
  Do not use `bun run ratchet --write`: it rewrites every count from one run. Leave `test/cli-goldens.test.ts` at the value slices 1-2 recorded.

- [ ] **Step 12: Suite-wide controls**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck && bun run vacuity && bun run ratchet
  ```
  Expected:
  - `vacuity` prints `null mode: … 0 vacuous` and exits 0. All 4 api-grafana tests are among the failed.
  - `ratchet` exits 0 and prints `test/api-grafana.test.ts    4/4   (recorded 4) complete`. No file is `REGRESSED`, and `test/cli-goldens.test.ts` passes its recorded count.

- [ ] **Step 13: Commit**

  ```sh
  cd /Volumes/S1/code/preview-stacks && but status
  ```
  Take the IDs of exactly these 12 paths:
  - `packages/conformance/test/api-grafana.test.ts`
  - `packages/conformance/gen/goldens.table.ts`
  - `packages/conformance/test/cli-goldens.test.ts`
  - `packages/conformance/expected-pass.json`
  - the 2 `docker-compose.yml` and 2 `grafana/datasources/loki.yaml` files under `golden/render/control/*-loki/`
  - the 4 `golden/cli/init{,-dry}-{http01-basic-compose,dns01-advanced-swarm}-loki.json`

  Never include `golden/host/db/*`, anything under `.superpowers/`, or another agent's files.
  ```sh
  but commit -b claude/loki-logging-grafana -m "test(conformance): Grafana sign-in over HTTP; loki goldens gain Grafana" <ids>
  ```

---

### Task 12: CHANGELOG, doc status, full gate, real-host checklist

**Files:**
- Modify: `packages/pstack/CHANGELOG.md`. Add to the `## Unreleased` block if slices 1-2 left one. Otherwise create the block right after `# Changelog`. Find the anchors by heading text: slices 1-2 and any release in between will have rewritten the head of the file.
- Modify: `docs/loki-logging-design.md`. Edit the `>` blockquote between the H1 and `## What it is`. In the slices table (`| Slice | Ships | Depends on |`), row 3's `Depends on` cell goes from `1` to `1, 2`. Anchor by quoted text, never by line number: slices 1 and 2 each rewrote this banner.
- Modify: `docs/README.md`. Replace the `loki-logging-design.md` entry, from its `### [` heading to the end of its paragraph, and keep its plan-link sentences (R9).
- Test: none. This task only edits docs. Step 1 is fact greps against the tree T1-T11 built. The checks are `bun run check`, `bun run vacuity`, `bun run ratchet` and the C1 grep.

**Interfaces:**
- Consumes:
  - From T11: the golden files its commit `test(conformance): Grafana sign-in over HTTP; loki goldens gain Grafana` changed, listed by `but show`.
  - From T10: the nav link `v-if="authState.grafana && !settings.token && can('developer')"` in `apps/ui/src/App.vue`.
  - From T1: the design doc's `## Slice 3 — Grafana at \`grafana.<domain>\`, signed in with pstack accounts` section.
  - The code facts the entry states, each grepped in Step 1:
    - T2: `"grafana."+d` in `IsControlHostname`.
    - T3: `SessionHashUser`.
    - T4: `GrafanaOn`.
    - T5/T6: `GrafanaVersion = "13.2.1"`, `mem_limit: 768m`, `priority=10000`, `name: "grafana image"`.
    - T7: the health `grafana` key and `Health.grafana?: string`.
    - T8: `grafanaRoles` with no `auth.Viewer` entry.
    - T9: `Grafana is not running.` / `Loki is not running.`.
- Produces: no code identifiers.
  - CHANGELOG: the Grafana bullets under `## Unreleased`.
  - Design banner lead: `**All three slices are built.**`.
  - Design slices table: row 3 depends on `1, 2`.
  - README heading suffix: `— a design in three slices, all built`.

The house precedent (commit `6e365cc`, and slice-1 plan T14) is that a feature commit writes under `## Unreleased` and the maintainer's `chore(release)` renames it. Nothing parses the file: `.goreleaser.yaml:58-59` has `changelog: disable: true`.

- [ ] **Step 1: Check the facts the entry states (no test file: docs only)**

  The entry describes what T1-T11 actually built, not the contract. Run these read-only checks first. If any output differs from what is expected below, write Steps 2-4 to match the code and note the difference in the task report. Do not edit code in this task.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack
  grep -n '"grafana."+d' internal/routing/domains.go
  grep -n 'func (a \*Auth) SessionHashUser' internal/auth/auth.go
  grep -n 'func GrafanaOn' internal/inspect/control.go
  grep -n 'GrafanaVersion = "13.2.1"' internal/initctl/init.go
  grep -n 'name: "grafana image"' internal/initctl/init.go
  grep -n -e 'auth.Developer: *"Editor"' -e 'auth.Maintainer: *"Editor"' -e 'auth.Admin: *"Admin"' internal/api/routes_grafana.go
  grep -c 'auth.Viewer: *"' internal/api/routes_grafana.go
  grep -n -e '"Grafana is not running."' -e '"Loki is not running."' internal/api/server.go
  grep -n 'K: "grafana"' internal/api/routes_auth.go
  grep -c grafana api/openapi.yaml
  cd /Volumes/S1/code/preview-stacks
  grep -n 'authState.grafana && !settings.token && can(.developer.)' apps/ui/src/App.vue
  grep -n 'grafana?: string' packages/client/src/types.ts
  ```

  Expected:
  - Every `grep -n` prints at least one line. The roles grep prints the one `grafanaRoles` line: `var grafanaRoles = map[auth.Role]string{auth.Developer: "Editor", auth.Maintainer: "Editor", auth.Admin: "Admin"}`.
  - Both `grep -c` print `0`: there is no viewer entry, and `openapi.yaml` does not mention Grafana (C9). The viewer grep has to match `auth.Viewer:` followed by a quoted value. The spec comment T8 copies verbatim reads `// No auth.Viewer: viewers are refused (owner, 2026-09-15) …`, so a bare `'auth.Viewer:'` counts `1` on a correct file.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance
  ls golden/render/control/*-loki/grafana/datasources/loki.yaml
  grep -c -e 'image: grafana/grafana:13.2.1' -e 'mem_limit: 768m' -e 'routers.pstack-grafana.priority=10000' -e 'GF_LIVE_MAX_CONNECTIONS: "0"' -e 'GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION' golden/render/control/*-loki/docker-compose.yml
  grep -lE 'grafana/grafana|https://grafana\.' golden/cli/*.json
  grep -c 'requires grafana image' golden/cli/init-dry-http01-basic-compose-loki.json golden/cli/init-dry-dns01-advanced-swarm-loki.json
  grep -rlE 'grafana/grafana|grafana\.preview|"grafana"' golden/host golden/render | grep -v -- '-loki/'
  grep -n 'api-grafana' expected-pass.json
  ```

  Expected:
  - `ls` prints both `http01-basic-compose-loki/grafana/datasources/loki.yaml` and `dns01-advanced-swarm-loki/grafana/datasources/loki.yaml`.
  - The `grep -c` over the two compose files prints `5` for each.
  - The transcript list is exactly `init-dns01-advanced-swarm-loki.json`, `init-dry-dns01-advanced-swarm-loki.json`, `init-dry-http01-basic-compose-loki.json` and `init-http01-basic-compose-loki.json`.
    - It may also hold `logging-loki-dry-dns01-advanced-swarm.json`, if `SwitchLogging -n` echoes init's dry run (C11). Note which case holds, because Step 2's Goldens bullet depends on it.
    - `upgrade-plan-*-loki` and `logging-off-dry-*-loki` must not be listed: the first prints no init dry run, and the second renders with logging off. If either is listed, the change belongs to T11. Take it back there, don't write it up here.
  - `requires grafana image` is counted `1` in each init-dry file.
  - The `golden/host golden/render` grep prints nothing. If it prints anything, T7 or T11 broke C9. That belongs to the owning task, not to this commit.
  - The last grep prints `"test/api-grafana.test.ts": 4`.

  Then list what T11's commit actually changed:

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Take the change ID of `test(conformance): Grafana sign-in over HTTP; loki goldens gain Grafana`. Use the change ID, never a SHA: `but amend` rewrites SHAs. Then run:

  ```bash
  but show <T11-change-id>
  ```

  Expected: its golden files are the two render cells' `docker-compose.yml` and `grafana/datasources/loki.yaml`, plus the transcripts the grep above listed. There is nothing under `golden/host/**` and no other render cell. The other files are `test/api-grafana.test.ts`, `gen/goldens.table.ts`, `test/cli-goldens.test.ts` and `expected-pass.json`.

  Last, find out whether slices 1-2 are still unreleased:

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -n '^## Unreleased' packages/pstack/CHANGELOG.md
  sed -n '/^## Unreleased/,/^## [0-9]/p' packages/pstack/CHANGELOG.md | grep -c -e '--logging loki'
  ls docs/loki-logging-slice-*-plan.md
  ```

  Record two cases for Steps 2-4:
  - **Branch A**: `## Unreleased` exists. **Branch B**: it does not.
  - **Lead U**: the count is at least 1, so slice 1 (and therefore slice 2) is still unreleased. **Lead R**: the count is 0.

- [ ] **Step 2: Write the CHANGELOG entry**

  These are the bullets. Use the cost sentence exactly as written: the spec says "The CHANGELOG says so in those words".

  Added bullet:

  ```markdown
  - **Grafana at `grafana.<domain>`, with `--logging loki`, signed in with pstack accounts.**
    `init --logging loki` and `pstack logging loki` run `grafana/grafana:13.2.1` beside Loki, with Loki
    as its provisioned datasource. Traefik's forwardAuth asks pstack about every request: a pstack
    browser session becomes a Grafana sign-in, and a pstack sign-out, password change or deleted
    account ends it on the next request. developer and maintainer are Grafana Editors, admin is Admin,
    and viewers are refused (403), because Grafana shows log lines unredacted. There is no Grafana
    server admin and no live tail. The advanced UI links to it for developer and above (hidden when
    the UI uses a stored token), and `/api/health` gains `grafana` on a host running it.
  ```

  Changed bullets:

  ```markdown
  - **A host with `--logging loki` gains Grafana on its next `pstack upgrade`: 768m more on the
    manager and a download from grafana.com on first start.** Without that egress Grafana starts
    without Logs Drilldown. `init` now pulls `grafana/loki` and `grafana/grafana` before `up`, and a
    host that cannot reach Docker Hub fails at the `requires … image` step, by name, before anything
    is recreated.
  - **`grafana.<domain>` is a control hostname**, on the primary and every added domain, always —
    logging off included. A deployment already routed there loses the host to pstack's Grafana router
    (`priority=10000`), and its next deploy is refused.
  - **`grafana.` and `loki.` reaching pstack answer plain text, never pstack's UI or API:**
    `503 Grafana is not running.` or `503 Loki is not running.` on the primary domain while the
    container is down, and `404 Not found.` on an added domain. Before, pstack's login page was
    served there, and a password typed on it would set `pstack_session` on that host.
  - **Goldens.** Regenerated with `bun gen/goldens.ts`: the `http01-basic-compose-loki` and
    `dns01-advanced-swarm-loki` render cells (`docker-compose.yml` gains the `grafana` service and
    volume; `grafana/datasources/loki.yaml` is new); `init-http01-basic-compose-loki` and
    `init-dns01-advanced-swarm-loki` (the `grafana` summary line); and
    `init-dry-http01-basic-compose-loki` and `init-dry-dns01-advanced-swarm-loki` (the two
    `requires … image` lines, the datasource `mkdir` and `write`, the compose byte count and the
    summary line). Every logging-off render cell, `golden/host`, and the help, cloud-init and
    swarm-join goldens are unchanged.
  ```

  If Step 1 listed `logging-loki-dry-dns01-advanced-swarm.json`, replace this text in the Goldens bullet:

  ```markdown
    summary line). Every logging-off render cell, `golden/host`, and the help, cloud-init and
  ```

  with:

  ```markdown
    summary line), and `logging-loki-dry-dns01-advanced-swarm` (the same dry-run lines). Every
    logging-off render cell, `golden/host`, and the help, cloud-init and
  ```

  **Branch A** (`## Unreleased` exists):
  - Take the first `### Added` heading after `## Unreleased` and before the next `## ` heading. Insert the Added bullet as the last bullet of that subsection, directly before the next `### ` or `## ` heading, with one blank line before it.
  - If there is no such `### Added`, insert `### Added`, a blank line and the bullet directly after the `## Unreleased` line and its blank line.
  - Insert the three Changed bullets and the Goldens bullet as the last bullets of the first `### Changed` under `## Unreleased`, in the same way. If there is no `### Changed`, create one directly after the Added subsection.
  - If that `### Changed` already holds a bullet starting `- **Goldens.**` (slice 1's), write the new bullet's lead as `- **Goldens, for Grafana.**` instead of `- **Goldens.**`.

  **Branch B** (no `## Unreleased`): replace

  ```markdown
  # Changelog

  ```

  (the first two lines of the file, the H1 and the blank line after it) with:

  ```markdown
  # Changelog

  ## Unreleased

  ### Added

  <the Added bullet>

  ### Changed

  <the three Changed bullets, then the Goldens bullet>

  ```

  so the next line of the file is the release heading that used to follow `# Changelog`. `<the Added bullet>` and the other placeholders stand for the exact blocks above, pasted verbatim.

  Check:

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -c '^## Unreleased' packages/pstack/CHANGELOG.md                                                     # 1
  grep -c 'gains Grafana on its next `pstack upgrade`: 768m more on the' packages/pstack/CHANGELOG.md      # 1
  sed -n '/^## Unreleased/,/^## [0-9]/p' packages/pstack/CHANGELOG.md | grep -c -e 'grafana/datasources/loki.yaml' -e 'viewers are refused (403)'   # 2
  grep -c 'The advanced UI links to it for developer and above' packages/pstack/CHANGELOG.md                # 1
  ```

- [ ] **Step 3: Mark slice 3 built in the design doc**

  First the `>` blockquote between `# Loki logging — a design in three slices` and `## What it is`. Print it:

  ```bash
  cd /Volumes/S1/code/preview-stacks && sed -n '1,/^## What it is/p' docs/loki-logging-design.md
  ```

  (a) Replace the blockquote's first bold span. It is the `**…**` that starts the first `> ` line: slice 1 wrote `**Slice 1 (\`--logging loki\`) is built (Unreleased); slices 2 and 3 are not.**`, and slice 2 rewrote it. Under **Lead U** it becomes:

  ```markdown
  **All three slices are built.** Slice 3 is Unreleased; slices 1 and 2 are too.
  ```

  and under **Lead R**:

  ```markdown
  **All three slices are built.** Slice 3 is Unreleased.
  ```

  Keep the rest of that paragraph, and every "where the build differs" list slices 1 and 2 left, unchanged.

  (b) Delete each sentence in the blockquote that says slice 3 is unbuilt or not yet specified, for example `Slices 2 and 3 record the decisions already taken; each gets its own spec before it is built.` or slice 2's `Slice 3 records …`. Find them with:

  ```bash
  sed -n '1,/^## What it is/p' docs/loki-logging-design.md | grep -n -i -E 'nothing in it is built|is not built|are not built|are not\.\*\*|own spec before it is built|records? the decisions already taken'
  ```

  (c) Append this as the last lines of the blockquote, directly before the blank line that precedes `## What it is`:

  ```markdown
  >
  > Slice 3 (Grafana at `grafana.<domain>`): [usage.md](usage.md), `### Grafana`. Its section below is
  > its spec with one correction:
  >
  > - The Grafana nav link shows for developer and above, matching verify's 403 for viewers. The spec
  >   also said every signed-in role sees it.
  ```

  If Step 1 found any other difference between the build and slice 3's section, add one bullet for it in the same form.

  (d) The slices table under `| Slice | Ships | Depends on |`: slice 3 is stacked on slice 2. Replace this row:

  ```markdown
  | 3 | Grafana at `grafana.<domain>`, signed in with pstack accounts. | 1 |
  ```

  with:

  ```markdown
  | 3 | Grafana at `grafana.<domain>`, signed in with pstack accounts. | 1, 2 |
  ```

  Neither slice 1's plan nor slice 2's spec touches this table, so the row should still read as quoted. If its `Ships` cell was reworded anyway, take the row that starts `| 3 |` and change only its last cell, from `| 1 |` to `| 1, 2 |`.

  Check:

  ```bash
  sed -n '1,/^## What it is/p' docs/loki-logging-design.md | grep -c '^> \*\*All three slices are built.\*\*'   # 1
  sed -n '1,/^## What it is/p' docs/loki-logging-design.md | grep -n -i -E 'nothing in it is built|is not built|are not built|are not\.\*\*|own spec before it is built|records? the decisions already taken'   # nothing
  grep -c '^| 3 | .* | 1, 2 |$' docs/loki-logging-design.md                                                   # 1
  grep -c '^| 3 | .* | 1 |$' docs/loki-logging-design.md                                                      # 0
  grep -c "can('developer')" docs/loki-logging-design.md                                                        # >= 1 (T1's section)
  ```

- [ ] **Step 4: Match the docs index row**

  In `docs/README.md`, find the entry that starts with `### [\`loki-logging-design.md\`](loki-logging-design.md) — `. Print it and note each sentence of its paragraph that links a `loki-logging-slice-*-plan.md` file:

  ```bash
  cd /Volumes/S1/code/preview-stacks && sed -n '/^### \[`loki-logging-design.md`\]/,/^### \[/p' docs/README.md
  ```

  Replace everything from that heading to the end of its paragraph (the last line before the blank line that precedes the next `### [` heading) with:

  ```markdown
  ### [`loki-logging-design.md`](loki-logging-design.md) — a design in three slices, all built

  **All three slices are built.** Slice 3 is Unreleased. Loki as a logging option: a Loki container in
  the control stack, the Grafana Loki Docker plugin on every node, and a `logging:` block pstack adds
  to every deployed service that has none — pushed through Traefik with basic auth (slice 1); Loki's
  settings in the UI: chunking, retention, filesystem or S3 (slice 2); and Grafana at
  `grafana.<domain>`, signed in with pstack accounts through Traefik's forwardAuth (slice 3). Each
  slice's spec is kept, with where the build differs at the top. Read before touching logging, the
  control template, the join material, or Grafana sign-in.
  ```

  - Under **Lead U**, the second sentence reads `Slice 3 is Unreleased; slices 1 and 2 are too.` The bold sentence plus the next one match the design banner's lead word for word.
  - Then re-append each plan-link sentence you noted, verbatim, at the end of the paragraph (R9). An example is slice 1's `Slice 1's task-by-task build plan is [\`loki-logging-slice-1-plan.md\`](loki-logging-slice-1-plan.md).`
  - For any `docs/loki-logging-slice-*-plan.md` from Step 1's `ls` that no kept sentence links, add one sentence in the same form: `Slice <N>'s task-by-task build plan is [\`loki-logging-slice-<N>-plan.md\`](loki-logging-slice-<N>-plan.md).`

  Check:

  ```bash
  grep -c 'a design in three slices, all built' docs/README.md                          # 1
  grep -o 'loki-logging-slice-[0-9]-plan.md](' docs/README.md | sort -u               # one per plan file that exists
  ```

- [ ] **Step 5: The C1 grep — nothing says the switch recreates pstack**

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -n 'pstack is recreated\|recreates pstack\|job in flight is lost\|in-flight job is lost\|plus exactly slice 2' packages/pstack/CHANGELOG.md docs/usage.md docs/control-plane.md docs/loki-logging-design.md packages/pstack/internal/api/routes_grafana.go packages/pstack/internal/api/server.go
  ```

  Expected: no output (exit 1). Slice 2's mount is in both modes, so the switch never recreates pstack.

  A hit is a C1 miss in the task that wrote that text, and it gets amended there (Step 6's recipe):
  - `docs/loki-logging-design.md`: T1.
  - `docs/usage.md` or `docs/control-plane.md`: the task that wrote that section (T6, T8 or T9).
  - `internal/api/routes_grafana.go`: T7 or T8. `internal/api/server.go`: T7 (the `grafana` field, Start and reindexLoop comments) or T9 (the refusal comment).

- [ ] **Step 6: Run the full gate**

  ```bash
  cd /Volumes/S1/code/preview-stacks && bun run check
  ```

  This takes several minutes, so use Bash timeout 600000 or run_in_background. `turbo run check` first builds `packages/pstack/bin/pstack` (`check` depends on `build`). It then runs:
  - Go vet and `go test -race`;
  - every package's bun tests and typecheck;
  - the conformance suite in go mode (`PSTACK_IMPL` defaults to `go`, `harness/impl.ts:19`), including `test/api-grafana.test.ts`, `test/cli-goldens.test.ts` and `test/host-fixture.test.ts`;
  - `apps/ui`'s `vue-tsc --build`.

  Expected: turbo ends with `Tasks:    <n> successful, <n> total`, the two numbers equal, and the command exits 0.

  A failure belongs to the task that owns what failed:

  | What failed | Owner |
  |---|---|
  | `internal/routing`, `internal/autolabel` | T2 |
  | `internal/auth` | T3 |
  | `internal/inspect` | T4 |
  | `TestGrafanaService`, `TestGrafanaDatasources`, `assets.go` | T5 |
  | `internal/initctl` (other tests), `internal/upgrade` | T6 |
  | `TestHealthNamesGrafanaOnlyWhenOn`, `packages/client` | T7 |
  | `TestGrafanaVerify`, `TestGrafanaStart`, `openapi_coverage_test.go`, `permissions_test.go` | T8 |
  | `TestServiceHostnamesAreNeverServedByPstack` and the wake tests | T9 |
  | `apps/ui` | T10 |
  | `packages/conformance` | T11 |

  Fix it in that task's files. Run `but status -fv` to get the owning commit's **change ID** and the fixed file's ID, then `but amend -t <owning-change-id> <file-id>`. Do not put the fix in this docs commit or in a new fixup commit, and never pass a SHA to `-t`. Re-run `bun run check` until it exits 0.

- [ ] **Step 7: Vacuity — no new test passes against the null server**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run vacuity
  ```

  Expected: the last line is `null mode: <n> failed (good), <n> skipped (CLI-only), 0 vacuous`, and the command exits 0. If it lists `test/api-grafana.test.ts`, that test goes back to T11 and is amended into T11's commit.

- [ ] **Step 8: Ratchet — counts only go up, and are recorded**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run ratchet
  ```

  Expected: exit 0, no `REGRESSED` row, no `advanced` row, and no `run with --write to ratchet up` line.
  - The rows include `test/api-grafana.test.ts` with `4/4   (recorded 4) complete`.
  - `test/cli-goldens.test.ts` is still `complete`, and its recorded count is whatever slices 1-2 left: T11 adds no CASES rows (C9).

  If a row says `advanced`, T11 under-recorded `expected-pass.json`. Run `bun scripts/ratchet.ts --write`, then `but status -fv`, then `but amend -t <T11-change-id> <expected-pass.json-file-id>`. If `test/cli-goldens.test.ts`'s count moved, that is T11's defect too.

- [ ] **Step 9: Commit**

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status
  ```

  Take the file IDs of exactly three files: `packages/pstack/CHANGELOG.md`, `docs/loki-logging-design.md` and `docs/README.md`. Leave out:
  - the untracked `packages/conformance/golden/host/db/pstack.db-shm` and `pstack.db-wal`;
  - anything in `packages/conformance/.status/`;
  - anything under `.superpowers/`.

  No golden and no code file should show as uncommitted here. If one does, it was a gate fix that belongs in its owning commit (Step 6).

  ```bash
  but commit -b claude/loki-logging-grafana -m "docs(changelog): Grafana with pstack sign-in" <changelog-id> <design-id> <readme-id>
  ```

  No attribution footer. Do not push or tag.

- [ ] **Step 10: Hand the owner the real-host checks (in the task report, not a file)**

  Paste this into the final report as-is, under the heading **Real host, before the release**:

  1. **Sign-in, password.** On a host from `cloud-init --logging loki`, signed out, open `grafana.<domain>/explore` → pstack login → back on Explore → `{service_name="<stack>-web"}` shows lines, as a maintainer. — not run (needs a real host)
  2. **Sign-in, SSO.** The same through an SSO provider. — not run (needs a real host)
  3. **Header spoofing.** Without a cookie, each of these → 401: `curl -H 'X-WEBAUTH-USER: admin' https://grafana.<domain>/api/user`; the same with `-H 'X_WEBAUTH_USER: admin'`; the same with `-H 'X.WEBAUTH.USER: admin'`. With a developer's cookie plus each header → the developer. With a viewer's cookie → 403. — not run (needs a real host)
  4. **Forged method.** With a maintainer's cookie: `curl -X POST -H 'X-Forwarded-Method: GET' -H 'Origin: https://pr-1.<domain>' https://grafana.<domain>/api/dashboards/db` → 403 `Cross-origin request refused.` — not run (needs a real host)
  5. **Network isolation.** From a preview container, `curl http://grafana:3000` does not connect. — not run (needs a real host)
  6. **Sign-out.** Sign out in pstack → the Grafana tab's next request bounces to the login page. — not run (needs a real host)
  7. **Demotion.** Demote a maintainer to viewer → the next Grafana request answers 403. — not run (needs a real host)
  8. **Grafana stopped.** `docker stop pstack-control-grafana-1` → `grafana.<domain>` shows `Grafana is not running.`, not pstack's login. — not run (needs a real host)
  9. **Switch with a job running.** Start a deploy, run pstack logging off then pstack logging loki → the pstack container's `StartedAt` is unchanged, the deploy finishes, the health key is present within 30 s, and the Grafana link after a page reload. — not run (needs a real host)
  10. **Upgrade.** `pstack upgrade` from a slice-1 host → Grafana appears, and `LOKI_PUSH_PASSWORD` is unchanged. A deployment routed to `grafana.<domain>` before the upgrade: `grafana.<domain>` serves Grafana, and that deployment's redeploy is refused. — not run (needs a real host)
  11. **Certificates.** HTTP-01 host: a certificate is issued for `grafana.<domain>`. DNS-01 host: none is ordered. — not run (needs a real host)
  12. **Code replay.** Traefik's access log shows the callback code once; replaying the URL restarts sign-in. — not run (needs a real host)

  Add one line for the maintainer: "Unreleased" now appears in `packages/pstack/CHANGELOG.md`, `docs/loki-logging-design.md` and `docs/README.md`. At release, replace all three with the version (`grep -rn Unreleased docs/README.md docs/loki-logging-design.md packages/pstack/CHANGELOG.md`).

---
