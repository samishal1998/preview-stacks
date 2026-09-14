# Loki logging, slice 1 (`--logging loki`) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pstack init --logging loki` runs Loki in the control stack, installs the Grafana Loki Docker plugin on every node, and gives every deployed service without its own `logging:` block a Loki logging block labelled `service_name=<stack>-<service>`, pushed through Traefik with basic auth.

**Architecture:** Init renders Loki by extending substitutions that already exist (no new template lines, so logging-off renders stay byte-identical) and records the push URL as a label on the Loki container. Deploys discover that label from the running control stack — the way they already detect the ACME challenge — and inject the logging block inside `autolabel.MaterializeCompose`, one point for compose and swarm. Upgrade reads Loki back from the rendered compose and `control/.env`; `pstack logging loki|off` re-runs init from saved state like `pstack ui`.

**Tech Stack:** Go 1.23 (`packages/pstack`, stdlib only — no new modules), bun:test conformance suite (`packages/conformance`), Vue 3 advanced UI (`apps/ui`), TypeScript client (`packages/client`), GitButler for version control.

**Spec:** [`docs/loki-logging-design.md`](loki-logging-design.md), section *Slice 1*. The plan argues from it; read both.

## Global Constraints

- Spec of record: /Volumes/S1/code/preview-stacks/docs/loki-logging-design.md, Slice 1 (lines 42-352). Exact values there win over anything paraphrased here.
- Every Go Test…/t.Run carries a `// negative control: <mutation that fails it>` line, and the mutation was actually run (AGENTS.md Go rule 17).
- Go test command: `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/<pkg>/ -run <Name>`. -race is not optional.
- Build for conformance: `cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`; run one file: `cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/<file>.test.ts`.
- Golden regeneration: `cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts` (the golden half of `bun run gen`). Then `but diff` must show only the intended golden files. Never commit golden/host/** or the untracked golden/host/db/pstack.db-shm / pstack.db-wal.
- Full gate before the last commit: `cd /Volumes/S1/code/preview-stacks && bun run check`.
- Version control is GitButler only, never git writes. Commit: run `but status` for file IDs, then `but commit -b claude/loki-logging -m "<type(scope): summary>" <ids>`. Add no Co-Authored-By, Claude-Session or Generated-with footer. Don't push, and don't touch files outside the task.
- Template substitution is literal: init markers and anchors use `strings.Replace(s, old, new, 1)`, cloud-init uses `strings.ReplaceAll` over an ordered list. Never regexp.ReplaceAllString or text/template (invariant 18, Go rule 8).
- No new lines in packages/pstack/templates/control/docker-compose.yml. With logging off, all 8 render goldens, all init/upgrade/ui-switch transcripts, cloud-init goldens and swarm-join goldens stay byte-identical.
- Flag + env for every input (Go rule 18): `--logging none|loki` / `PSTACK_LOGGING` (`||` semantics, default `none`). The push password is a secret, so it's env only: `PSTACK_LOKI_PASSWORD`. No flag, ever.
- Exact values: image `grafana/loki:3.7.7`; plugin `grafana/loki-docker-driver:3.7.7-<amd64|arm64>`, `--alias loki --grant-all-permissions LOG_LEVEL=warn`; label key `pstack.logging.push-url`; label value `https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/loki/api/v1/push`; .env key `LOKI_PUSH_PASSWORD` = 32 lowercase hex from crypto/rand; basic auth `pstack:{SHA}<base64(sha1(password))>`; config at `<DATA>/control/loki/config.yaml` mode 0644.
- Injected block, key order fixed: driver loki; options loki-url=<push url>, loki-external-labels=`service_name=<stack>-<service>`, loki-relabel-config=`[{action: labeldrop, regex: filename}]`, loki-retries="2", loki-timeout=1s, loki-max-backoff=800ms, mode=non-blocking, keep-file="false", max-size=10m, max-file="3". Every option except the URL and the label is a constant, never a setting.
- JSON through structs or omap/jsonx, never map[string]any. Every []T in a response or result is non-nil at construction. Tri-states are pointers without omitempty (`lokiPlugin`). Never range a Go map into output (Go rules 1-3, 5).
- File header comments explain WHY. Change the header that records a decision in the same edit, and match the surrounding comment density.
- UI and CLI copy is terse: state, not explanation (owner has corrected this 3+ times). UI work follows docs/ui-rules.md.
- No new Go modules or npm dependencies.
- Each task folds in the docs describing its behaviour (docs/usage.md, docs/bootstrap.md, docs/control-plane.md) and ends green on its own package tests.

## File map

| Path | Action | Responsibility |
|---|---|---|
| `packages/pstack/internal/swarm/swarm.go` | modify | LokiVersion, LokiPluginInstall (T1); Node.LokiPlugin, MarkLokiPlugins, SwarmReport flag (T7); JoinScript/JoinArgs/CloudInit seam with logging (T10) |
| `packages/pstack/internal/swarm/swarm_test.go` | modify | plugin line behaviour under bash; node plugin marking; join script with logging |
| `packages/pstack/templates/control/loki/config.yaml` | create | the fixed Loki config, byte-for-byte the spec block (design doc lines 132-182) |
| `packages/pstack/assets.go` | modify | embed LokiConfig by explicit path |
| `packages/pstack/internal/initctl/init.go` | modify | Logging type, PushURLLabel, LokiService, LokiWiring (T2); Options.Logging, password, .env line, config write, plugin step, summary line (T3) |
| `packages/pstack/internal/initctl/init_test.go` | modify | render and Init tests for loki; TestInitGoldens keeps proving logging-off byte identity |
| `packages/pstack/internal/upgrade/upgrade.go` | modify | ControlState.Logging/LokiPassword read-back, initFlags/initEnv, SwitchLogging, LoggedDeployments |
| `packages/pstack/internal/upgrade/upgrade_test.go` | modify | read-back from a real loki init, plan carries the password, switch, count |
| `packages/pstack/internal/cli/args.go` | modify | Parsed.Logging, --logging parse, Usage text, Commands gains logging |
| `packages/pstack/internal/cli/args_test.go` | modify | flag/env parse tests |
| `packages/pstack/internal/cli/commands.go` | modify | init/cloud-init help + flags; logging command page |
| `packages/pstack/internal/cli/commands_test.go` | modify | completion offers --logging; logging command usage refusal |
| `packages/pstack/internal/cli/completion.go` | modify | enumFlags --logging; subcommands logging |
| `packages/pstack/internal/cli/initguard.go` | modify | refuse dropping --logging loki and minting a new push password |
| `packages/pstack/internal/cli/initguard_test.go` | modify | guard tests; existing call sites gain the new argument |
| `packages/pstack/internal/cli/run.go` | modify | init passthrough + guard arg (T5); logging command (T6); swarm join Logging (T10); swarm status MarkLokiPlugins (T11); cloud-init Answers.Logging (T5) |
| `packages/pstack/internal/cloudinit/cloudinit.go` | modify | Answers.Logging -> `--logging loki` on the init call (T5); WorkerAnswers.Logging plugin step + seam (T10) |
| `packages/pstack/internal/cloudinit/cloudinit_test.go` | modify | passthrough and worker plugin step tests |
| `packages/pstack/internal/inspect/control.go` | modify | LokiPushURL discovery |
| `packages/pstack/internal/inspect/control_test.go` | modify | discovery tests |
| `packages/pstack/internal/autolabel/autolabel.go` | modify | DetectLogging seam, InjectLogging, MaterializeResult.Logged/LogNotes, gates (T8); ControlHostname seam + reserved-host refusal (T12) |
| `packages/pstack/internal/autolabel/autolabel_test.go` | modify | injection and reserved-host tests |
| `packages/pstack/internal/compose/compose.go` | modify | filesFor returns the MaterializeResult; ComposeUp emits log notes, compose plugin check, swarm node notes |
| `packages/pstack/internal/compose/compose_test.go` | modify | TestMain pinning DetectLogging off (T8); plugin-check tests (T9) |
| `packages/pstack/internal/routing/domains.go` | modify | IsControlHostname includes loki. |
| `packages/pstack/internal/routing/domains_test.go` | modify | loki. on primary and added domains |
| `packages/pstack/internal/redact/redact_test.go` | modify | push URL userinfo is masked (no code change) |
| `packages/pstack/internal/api/routes.go` | modify | /api/swarm/join passes Logging (T10); /api/swarm marks node plugins and adds lokiPluginInstall (T11) |
| `packages/client/src/types.ts` | modify | SwarmNode.lokiPlugin, SwarmInfo.lokiPluginInstall |
| `apps/ui/src/api/types.ts` | modify | same two fields for the SPA |
| `apps/ui/src/views/SwarmView.vue` | modify | badge + banner with the install line for nodes without the plugin |
| `packages/conformance/test/api-share-sleep-swarm.test.ts` | modify | one new test: /api/swarm flag + join script with logging on (T11) |
| `packages/conformance/gen/goldens.table.ts` | modify | LOKI_PASSWORD, LOKI_SHIM, 12 new CASES rows |
| `packages/conformance/test/api-logging.test.ts` | create | deploy-time injection over HTTP: compose, swarm, plugin missing |
| `packages/conformance/expected-pass.json` | modify | ratchet counts up |
| `packages/conformance/golden/cli/` | modify | help/help-h/no-args (T5, T6), unknown-command (T6), 12 new transcripts (T13) |
| `packages/conformance/golden/render/control/http01-basic-compose-loki/` | create | docker-compose.yml, .env, dns.env, loki/config.yaml |
| `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/` | create | docker-compose.yml, .env, dns.env, loki/config.yaml |
| `docs/usage.md` | modify | init steps (T3), flag/env rows + guard (T5), pstack logging + plugin upgrade by hand (T6), adding a worker (T10), Swarm JSON (T11), Hostnames (T12) |
| `docs/bootstrap.md` | modify | cloud-init --logging loki (T5) |
| `docs/control-plane.md` | modify | new logging section: discovery, injection point, skip rule, fixed options (T8) |
| `docs/README.md` | modify | loki design row: slice 1 built (T14) |
| `docs/loki-logging-design.md` | modify | top banner: slice 1 built (T14) |
| `packages/pstack/CHANGELOG.md` | modify | ## Unreleased entry incl. the golden changes (T14) |

## Decisions the plan fixed (the spec left them open)

- Names the spec left open, fixed here: initctl.Logging, LoggingNone ("none"), Loki ("loki"), PushURLLabel, LokiService, LokiWiring; pstack.LokiConfig; swarm.LokiVersion, LokiPluginInstall, MarkLokiPlugins, Node.LokiPlugin (json lokiPlugin); inspect.LokiPushURL; autolabel.DetectLogging, InjectLogging, ControlHostname, MaterializeResult.Logged/LogNotes; upgrade.SwitchLogging, SwitchLoggingOptions, LoggedDeployments, ControlState.Logging/LokiPassword; swarm.JoinArgs.Logging; cloudinit.Answers.Logging, WorkerAnswers.Logging; cli Parsed.Logging; /api/swarm key lokiPluginInstall.
- Two vocabularies, both from the spec: the flag and env take none|loki, the command takes loki|off. SwitchLogging prints `off` for LoggingNone.
- Discovery reads `docker ps -a` (idsByLabel) and picks the control-project container whose compose service is `loki` and whose push-url label is non-empty. `-a` keeps injection steady while Loki restarts; `pstack logging off` recreates the stack with --remove-orphans, which removes the container. A spec that squats the pstack-control label is the same trust exposure DetectChallenge already has: specs are CI-trusted (AGENTS.md scope).
- Discovery runs inside MaterializeCompose after the compose file is read and parsed, so a missing/unreadable file still returns untouched() with no docker call. It never runs under dry-run (compose.filesFor returns early), so the up-dry/down-dry goldens are unchanged. Cost: one `docker ps` + `docker inspect` per compose invocation (up/down/logs/ps), the same pattern as DetectChallenge.
- compose_test.go gets a TestMain pinning autolabel.DetectLogging to "" (T8). Without it, the existing materialize subtest (compose_test.go:222-282) sees a discovery `docker ps` at Commands()[0]. No existing assertion is weakened.
- swarm is the import-graph leaf: initctl→swarm, inspect→initctl, so swarm cannot import inspect. Callers (cli run.go, api routes.go, compose.ComposeUp) read inspect.LokiPushURL and pass JoinArgs.Logging or call swarm.MarkLokiPlugins.
- The push password is written to .env only when logging is loki. `pstack logging off` drops the line and re-enabling generates a new one. Harmless: containers from before already get 404 while off, and 401 is equally dropped without retry.
- initguard gains a check the spec text doesn't list: refuse `init --logging loki` on a loki host when PSTACK_LOKI_PASSWORD is absent. Without it a bare re-run rotates the password and every running container's pushes fail. That contradicts the failure-mode table's 'only reachable by hand-editing control/.env'. Both new checks are asymmetric (they fire only when the host HAS loki / a password), so existing guard tests keep their counts.
- Masking: redact.RedactText's urlPassword already gives `://user:••••@` (4 bullets). The spec's 8-bullet form is illustrative, and the 4-bullet shape is pinned by redact_test.go:110-114. T12 only adds a test. Verified: no route shows compose.generated.yml or container LogConfig (/source returns the submitted compose.yml; inspect exposes traefik.* labels, redacted); job and log text already pass through RedactText.
- OpenAPI: /api/swarm is documented as the untyped `$ref '#/components/responses/Ok'`, so adding `lokiPlugin`/`lokiPluginInstall` needs no openapi.yaml edit and no `go generate ./packages/pstack/internal/apicli`. The spec's '+ regenerated apicli' has nothing to regenerate.
- Skipped on purpose: the basic embedded UI's swarm panel (packages/pstack/ui/index.html:1054). The spec names apps/ui only; `pstack swarm` also flags the node. Add it if the owner wants basic-UI parity.
- Golden churn beyond 'only the help goldens': no-args.json prints Usage() (changes in T5 and T6); unknown-command.json lists Commands (changes in T6). Every other existing golden must stay byte-identical, and T13 adds 12 transcripts plus two render cells. cli-goldens count: 81 → 93.
- Additions beyond the spec's conformance list, one table row each: upgrade-plan-<cell>-loki (black-box 'survives upgrade'), logging-off/loki dry runs over both loki cells, swarm-join-cloud-config-loki (the YAML `: ` hazard), and the /api/swarm flag test in api-share-sleep-swarm.test.ts (T11).
- Golden regeneration uses `bun gen/goldens.ts`, the first half of `bun run gen`. The second half (gen/host-fixture.ts) rewrites golden/host, which this change doesn't touch, and untracked golden/host/db/pstack.db-shm/-wal already exist from elsewhere. Never commit those.
- Reserved-host refusal reads the primary domain from PSTACK_DOMAIN in the process environment, matching DetectChallenge's 'resolve from the process env' rule. The API container always has it. A host-side CLI without it checks the added domains only (such a caller holds the docker socket anyway). Only an explicit pstack.routing.host can collide: generated hosts `<name>-<stack>.<domain>` always contain a dash.
- Worker cloud-init's plugin step is a YAML literal block (the echo contains `: `), wrapped `{ …; } || echo`, under an UNNUMBERED section header, so the logging-off file stays byte-identical with no renumbering. JoinScript places the line before the 'already part of a swarm' early exit, so re-running the script on a worker that joined earlier installs the plugin: a free partial answer to 'workers that joined before'.
- LokiWiring runs after the six marker substitutions. Anchor 1 `    networks: [preview-ingress]\n` first matches Traefik (template line 40); pstack's line is `[preview-ingress, preview-shared]` and advanced-ui's identical line comes later. Anchor 3 is the file's last block. The loki block has `networks: [logs]`, so it cannot match anchor 1.
- Compose plugin failure is returned as exec.Result{OK:false, Stderr: one line}. stack.Up turns firstLine(Stderr) into the (compose) step message, so the sentence and install line must stay on ONE line (LokiPluginInstall is single-line by construction).
- dependsOn also serializes edits to shared files even where no API flows between tasks: run.go (T5→T6→T10→T11), autolabel.go (T8→T12), swarm.go (T1→T7→T10), cloudinit.go (T5→T10), docs/usage.md (many). Execute in id order.
- CHANGELOG uses `## Unreleased`: the file has only dated version headings and releasing is the maintainer's (AGENTS.md), so no version number is guessed.

## Order

Execute in task order — `dependsOn` also serialises edits to shared files (`run.go`, `autolabel.go`, `swarm.go`, `cloudinit.go`, `docs/usage.md`).

| Task | Title | Depends on |
|---|---|---|
| 1 | swarm: the loki plugin install line | — |
| 2 | initctl: render Loki (pure functions + embedded config) | 1 |
| 3 | initctl: Init --logging loki (password, .env, config, plugin step) | 1, 2 |
| 4 | upgrade: read Loki back; SwitchLogging; LoggedDeployments | 3 |
| 5 | cli: --logging flag, env, help, completion, initguard, cloud-init passthrough | 3, 4 |
| 6 | cli: pstack logging loki\|off | 4, 5 |
| 7 | inspect + swarm: discover Loki; read each node's log plugins | 1, 2 |
| 8 | autolabel: inject the loki logging block; open the compose write gates | 7 |
| 9 | compose: check the plugin before a logged deploy; name what was skipped | 8 |
| 10 | swarm + cloudinit: join material installs the plugin when logging is on | 1, 6, 7 |
| 11 | Swarm page + pstack swarm: flag nodes without the plugin | 7, 10 |
| 12 | hostnames: loki. is a control hostname; previews cannot claim one; masking proof | 8 |
| 13 | conformance: loki goldens, deploy injection over HTTP, ratchet | 3, 4, 5, 6, 9, 10, 11, 12 |
| 14 | CHANGELOG, doc status, full gate, real-host checklist | 13 |

---

### Task 1: swarm: the loki plugin install line

**Files:**
- Modify: `packages/pstack/internal/swarm/swarm.go:1-2` (the package header's first sentence)
- Modify: `packages/pstack/internal/swarm/swarm.go:49-51` (the import-graph note, just before `package swarm`)
- Modify: `packages/pstack/internal/swarm/swarm.go:737-740` (insert after `JoinCommand`)
- Test: `packages/pstack/internal/swarm/swarm_test.go` (append after line 534, the closing `}` of the file's last test)

**Interfaces:**
- Consumes: nothing
- Produces:
  - `package swarm; const LokiVersion = "3.7.7"`
  - `package swarm; const LokiPluginInstall = "arch=$(uname -m); case \"$arch\" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac; docker plugin inspect loki >/dev/null 2>&1 || docker plugin install grafana/loki-docker-driver:" + LokiVersion + "-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn; [ \"$(docker plugin inspect -f '{{.Enabled}}' loki)\" = true ] || docker plugin enable loki"`
  - In the source it is split over three concatenated literals to fit the line width. A throwaway comparison confirmed the value matches the contract literal byte for byte.
  - It is one line, POSIX sh, with no trailing newline. T3 runs it on the manager. T10 wraps it as `{ <line>; } || echo …`. T7, T9 and T11 print it.

Facts this relies on:
- The test file is `package swarm` (internal), so it uses `LokiPluginInstall` unqualified.
- It already imports `os`, `osexec "os/exec"`, `path/filepath`, `strings` and `testing` (swarm_test.go:8-19), so no import changes.
- The bash-in-a-test precedent is `osexec.LookPath("bash")` + `t.Skip("no bash")` (swarm_test.go:291-294).
- The fakes use `printf`, never `echo` (AGENTS.md:287), and mode `0o777`, letting umask apply (Go rule 10, AGENTS.md:360).
- `initctl/init.go`, `cloudinit/cloudinit.go` and `compose/compose.go` already import swarm, which the header sentence below says.

- [ ] **Step 1: Write the failing test**

Append to the end of `packages/pstack/internal/swarm/swarm_test.go`, after line 534:

```go

// pluginLog runs LokiPluginInstall against a fake `uname -m` that prints arch and a fake `docker` that
// records every call, exits 0 from `plugin inspect loki` only when installed, and prints enabled for
// `plugin inspect -f {{.Enabled}} loki`. It returns what docker was asked, one call per line.
//
// It runs the line under bash and, where present, dash: a worker's cloud-config runs it under `sh`,
// which is dash on Debian and Ubuntu, and a bashism there fails silently into a different branch.
func pluginLog(t *testing.T, arch string, installed bool, enabled string) string {
	t.Helper()
	bash, err := osexec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	code := "1"
	if installed {
		code = "0"
	}
	dir := t.TempDir()
	uname := "#!/bin/sh\nprintf '%s\\n' " + arch + "\n"
	docker := `#!/bin/sh
printf '%s\n' "$*" >>"$DOCKER_LOG"
case "$*" in
  "plugin inspect loki") exit ` + code + ` ;;
  "plugin inspect -f {{.Enabled}} loki") printf '%s\n' ` + enabled + ` ;;
esac
`
	for _, f := range [][2]string{{"uname", uname}, {"docker", docker}} {
		if err := os.WriteFile(filepath.Join(dir, f[0]), []byte(f[1]), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	shells := []string{bash}
	if dash, err := osexec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}
	logs := make([]string, len(shells))
	for i, sh := range shells {
		log := filepath.Join(dir, filepath.Base(sh)+".log")
		cmd := osexec.Command(sh, "-c", LokiPluginInstall)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DOCKER_LOG="+log)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(sh), err, out)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		logs[i] = string(b)
	}
	for i := 1; i < len(logs); i++ {
		if logs[i] != logs[0] {
			t.Fatalf("%s asked docker:\n%s\nbash asked:\n%s", filepath.Base(shells[i]), logs[i], logs[0])
		}
	}
	return logs[0]
}

// The plugin line is shell, so comparing it to a string proves nothing about what it does. These run
// it and read back what docker was asked.
func TestLokiPluginInstall(t *testing.T) {
	calls := func(c ...string) string { return strings.Join(c, "\n") + "\n" }
	const (
		inspect = "plugin inspect loki"
		enabled = "plugin inspect -f {{.Enabled}} loki"
		enable  = "plugin enable loki"
	)

	t.Run("x86_64 installs the amd64 tag, aliased loki, at LOG_LEVEL=warn", func(t *testing.T) {
		// negative control: drop `x86_64|` from the case — docker is asked for `3.7.7-x86_64`.
		want := calls(inspect, "plugin install grafana/loki-docker-driver:3.7.7-amd64 --alias loki --grant-all-permissions LOG_LEVEL=warn", enabled)
		if got := pluginLog(t, "x86_64", false, "true"); got != want {
			t.Errorf("docker was asked:\n%s", got)
		}
	})

	t.Run("aarch64 installs the arm64 tag", func(t *testing.T) {
		// negative control: drop `aarch64|` from the case — docker is asked for `3.7.7-aarch64`.
		want := calls(inspect, "plugin install grafana/loki-docker-driver:3.7.7-arm64 --alias loki --grant-all-permissions LOG_LEVEL=warn", enabled)
		if got := pluginLog(t, "aarch64", false, "true"); got != want {
			t.Errorf("docker was asked:\n%s", got)
		}
	})

	t.Run("an installed plugin is not installed again", func(t *testing.T) {
		// negative control: drop `docker plugin inspect loki >/dev/null 2>&1 || ` — install runs a
		// second time, which docker refuses as a Conflict.
		if got := pluginLog(t, "x86_64", true, "true"); got != calls(inspect, enabled) {
			t.Errorf("docker was asked:\n%s", got)
		}
	})

	t.Run("a disabled plugin is enabled", func(t *testing.T) {
		// negative control: replace `|| docker plugin enable loki` with `|| true` — the plugin stays
		// disabled, and swarm counts it as missing.
		if got := pluginLog(t, "x86_64", true, "false"); got != calls(inspect, enabled, enable) {
			t.Errorf("docker was asked:\n%s", got)
		}
	})

	t.Run("an enabled plugin is left alone", func(t *testing.T) {
		// negative control: drop `[ "$(docker plugin inspect -f '{{.Enabled}}' loki)" = true ] || ` —
		// every run re-enables. (Spelling it `[[ … == true ]]` fails too, under dash: no `[[` there.)
		if got := pluginLog(t, "x86_64", true, "true"); got != calls(inspect, enabled) {
			t.Errorf("docker was asked:\n%s", got)
		}
	})

	t.Run("it is one line", func(t *testing.T) {
		// negative control: break the constant after a `;` with "\n".
		// Callers print it inside one-line messages (a failed step, a node note) and wrap it in `{ …; }`.
		if strings.Contains(LokiPluginInstall, "\n") {
			t.Errorf("LokiPluginInstall has a newline:\n%s", LokiPluginInstall)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ -run TestLokiPluginInstall
```

Expected: a build failure.

```
# github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm [github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm.test]
internal/swarm/swarm_test.go:573:35: undefined: LokiPluginInstall
internal/swarm/swarm_test.go:645:23: undefined: LokiPluginInstall
internal/swarm/swarm_test.go:646:53: undefined: LokiPluginInstall
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm [build failed]
FAIL
```

- [ ] **Step 3: Implement**

**3a.** In `packages/pstack/internal/swarm/swarm.go`, replace lines 1-2:

```go
// Package swarm is Docker Swarm: the compose→swarm conversion, the `docker stack` command lines, and
// the node/join helpers behind the swarm panel.
```

with:

```go
// Package swarm is Docker Swarm: the compose→swarm conversion, the `docker stack` command lines, the
// node/join helpers behind the swarm panel, and the line that puts the Loki log plugin on a node.
```

**3b.** Replace the end of the import-graph note (lines 50-51):

```go
// worker cloud-config — goes through the CloudInit seam, which cloudinit fills in at init.
package swarm
```

with:

```go
// worker cloud-config — goes through the CloudInit seam, which cloudinit fills in at init.
// LokiVersion and LokiPluginInstall live here for the same reason: initctl, cloudinit and compose all
// need them, and all three already import this package.
package swarm
```

**3c.** Insert the two constants after `JoinCommand`. Replace lines 737-740:

```go
// JoinCommand is the `docker swarm join` line.
func JoinCommand(token, managerAddr string) string {
	return "docker swarm join --token " + token + " " + managerAddr
}
```

with:

```go
// JoinCommand is the `docker swarm join` line.
func JoinCommand(token, managerAddr string) string {
	return "docker swarm join --token " + token + " " + managerAddr
}

// LokiVersion is the Loki release pstack runs: the control stack's `grafana/loki` image and the log
// driver plugin's tag. The driver is built from Loki's own tree, so the two move together.
const LokiVersion = "3.7.7"

// LokiPluginInstall is the line that puts the Loki log driver on a node, installed, aliased `loki`
// and enabled, and does nothing on a node that already has it. init runs it on the manager; the join
// script and the worker cloud-config run it on a new worker. It is ONE line of POSIX sh: the worker
// cloud-config runs it under `sh`, and failures print it inside one-line messages. A caller that must
// not fail wraps it: `{ <line>; } || echo …`.
//
//   - The inspect guard, because `docker plugin install` is not idempotent: a second run is a
//     Conflict ("already exists").
//   - The enable, because swarm counts a disabled plugin as missing.
//   - LOG_LEVEL=warn, because at the default `info` the driver logs its whole option map on every
//     container start — the push URL with its password — into the node's Docker journal.
//   - The per-architecture tag, because the image has no multi-arch manifest (`latest` was last
//     pushed in 2021).
//   - Pinned to LokiVersion. `pstack upgrade` never upgrades the plugin: that takes `disable --force`,
//     `upgrade`, `enable` and a dockerd restart on each node, which interrupts every preview on it.
const LokiPluginInstall = "arch=$(uname -m); case \"$arch\" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac; " +
	"docker plugin inspect loki >/dev/null 2>&1 || docker plugin install grafana/loki-docker-driver:" + LokiVersion +
	"-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn; " +
	"[ \"$(docker plugin inspect -f '{{.Enabled}}' loki)\" = true ] || docker plugin enable loki"
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ -run TestLokiPluginInstall -v
```

Expected (timings vary):

```
--- PASS: TestLokiPluginInstall (0.19s)
    --- PASS: TestLokiPluginInstall/x86_64_installs_the_amd64_tag,_aliased_loki,_at_LOG_LEVEL=warn (0.04s)
    --- PASS: TestLokiPluginInstall/aarch64_installs_the_arm64_tag (0.04s)
    --- PASS: TestLokiPluginInstall/an_installed_plugin_is_not_installed_again (0.03s)
    --- PASS: TestLokiPluginInstall/a_disabled_plugin_is_enabled (0.04s)
    --- PASS: TestLokiPluginInstall/an_enabled_plugin_is_left_alone (0.03s)
    --- PASS: TestLokiPluginInstall/it_is_one_line (0.00s)
PASS
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm	1.2s
```

Then run the whole package (existing tests unchanged), vet and gofmt:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ && go vet ./internal/swarm/ && gofmt -l internal/swarm/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm	1.3s`, no vet output, and no gofmt output.

- [ ] **Step 5: Run every negative control (Go rule 17)**

Apply each mutation below to the `LokiPluginInstall` constant in `swarm.go`, one at a time. Run `go test -race -timeout 120s ./internal/swarm/ -run TestLokiPluginInstall -v`, confirm the named subtest fails, then revert before the next one. All seven were run while drafting this plan and failed as listed.

| # | Mutation in `LokiPluginInstall` | Fails (at least) |
|---|---|---|
| 1 | `x86_64\|amd64)` → `amd64)` | `x86_64_installs_the_amd64_tag…` (docker asked for `3.7.7-x86_64`) |
| 2 | `aarch64\|arm64)` → `arm64)` | `aarch64_installs_the_arm64_tag` (asked for `3.7.7-aarch64`) |
| 3 | delete `docker plugin inspect loki >/dev/null 2>&1 \|\| ` | `an_installed_plugin_is_not_installed_again` (the two install subtests fail too) |
| 4 | `\|\| docker plugin enable loki` → `\|\| true` | `a_disabled_plugin_is_enabled` |
| 5 | delete `[ \"$(docker plugin inspect -f '{{.Enabled}}' loki)\" = true ] \|\| ` (leaving `"docker plugin enable loki"`) | `an_enabled_plugin_is_left_alone` (all five shell subtests fail, under bash alone) |
| 6 | `[ \"$(…)\" = true ]` → `[[ \"$(…)\" == true ]]` | `an_enabled_plugin_is_left_alone`, via `dash asked docker: … bash asked: …`. Only fires where `dash` is on PATH (CI's ubuntu-latest, macOS 15+ `/bin/dash`). |
| 7 | `LOG_LEVEL=warn; " +` → `LOG_LEVEL=warn;\n" +` | `it_is_one_line` |

After the last revert, re-run Step 4's whole-package command. It must be green.

- [ ] **Step 6: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of `packages/pstack/internal/swarm/swarm.go` and `packages/pstack/internal/swarm/swarm_test.go` only (never `packages/conformance/golden/host/db/pstack.db-shm` / `-wal`), then:

```bash
but commit -b claude/loki-logging -m "feat(swarm): the loki log plugin install line" <swarm.go id> <swarm_test.go id>
```

No Co-Authored-By or other footer. Don't push.

---

### Task 2: initctl: render Loki (pure functions + embedded config)

**Files:**
- Create: `packages/pstack/templates/control/loki/config.yaml`
- Modify: `packages/pstack/internal/initctl/init.go:27-44` (imports), `:66-77` (types and consts), insert after `:699` (end of `AdvancedUIService`, before `dnsEnvFile`)
- Modify: `packages/pstack/assets.go:1-3` (header count), `:18-21` (add `LokiConfig` after `ControlTemplate`)
- Test: `packages/pstack/internal/initctl/init_test.go:3-18` (imports), append after `:596` (end of `TestInitGoldens`)

**Interfaces:**
- Consumes: `swarm.LokiVersion` (T1): `package swarm; const LokiVersion = "3.7.7"`. Until T1 lands, this task fails to compile with `undefined: swarm.LokiVersion`.
- Produces:
  - `package initctl; type Logging string; const ( LoggingNone Logging = "none"; Loki Logging = "loki" )`. `""` counts as `LoggingNone`.
  - `package initctl; const PushURLLabel = "pstack.logging.push-url"`
  - `package initctl; func LokiService(logging Logging, challenge Challenge, password string) string`: returns `""` unless `logging == Loki`. The block starts and ends with `""`, like `AdvancedUIService`, so `AdvancedUIService(ui)+LokiService(…)` (T3) leaves one blank line between services and one before `volumes:`.
  - `package initctl; func LokiWiring(template string, logging Logging) (string, error)`: `(template, nil)` unless Loki. A missing anchor returns `("", error)` that names it.
  - `package pstack; //go:embed templates/control/loki/config.yaml; var LokiConfig string`

Nothing calls these from `Init` yet (T3 does). Logging-off output is untouched by construction.

---

- [ ] **Step 1: Write the failing tests**

In `packages/pstack/internal/initctl/init_test.go`, replace the import block (lines 3-18):

```go
import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)
```

with:

```go
import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)
```

Then append to the end of the file, after `TestInitGoldens`'s closing `}` at line 596:

```go
// substituted is the control template after Init's six marker substitutions (init.go, "── 3.
// Configuration"), with `loki` appended after the advanced UI where Init appends LokiService. It is
// LokiWiring's input; its first subtest proves it still matches the file Init writes.
func substituted(challenge initctl.Challenge, ui initctl.UI, loki string) string {
	s := pstack.ControlTemplate
	s = strings.Replace(s, "      #__ACME_CHALLENGE__", initctl.AcmeChallengeArgs(challenge, "cloudflare"), 1)
	s = strings.Replace(s, "      #__ACME_ROUTER_TLS__", initctl.AcmeRouterLabels(challenge), 1)
	s = strings.Replace(s, "      #__CONTROL_UI_SERVICE__", initctl.ControlUIService(ui), 1)
	s = strings.Replace(s, "      #__SWARM_PROVIDER__", initctl.SwarmProviderArgs(spec.Compose), 1)
	s = strings.Replace(s, "      #__WAKE_ROUTER__", initctl.WakeRouterLabels("preview.example.com"), 1)
	return strings.Replace(s, "#__ADVANCED_UI_SERVICE__", initctl.AdvancedUIService(ui)+loki, 1)
}

// The loki service. Its TLS labels are the part that varies by host, and getting them backwards is
// silent until the first push: DNS-01 would order a second certificate, HTTP-01 would serve none.
func TestLokiService(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("http01 orders loki its own certificate, dns01 inherits the wildcard", func(t *testing.T) {
		// negative control: append the certresolver label for every challenge — the dns01 check fails.
		http := initctl.LokiService(initctl.Loki, initctl.HTTP01, pw)
		dns := initctl.LokiService(initctl.Loki, initctl.DNS01, pw)
		for _, block := range []string{http, dns} {
			if !strings.Contains(block, "      - traefik.http.routers.pstack-loki.tls=true\n") {
				t.Errorf("no tls=true:\n%s", block)
			}
		}
		if !strings.Contains(http, "      - traefik.http.routers.pstack-loki.tls.certresolver=le\n") {
			t.Error("http01 has no certresolver")
		}
		if strings.Contains(dns, "- traefik.http.routers.pstack-loki.tls.certresolver") {
			t.Error("dns01 orders its own certificate")
		}
	})

	t.Run("basic auth is pstack:{SHA} of the password; deploys find the push URL on a label", func(t *testing.T) {
		// negative control: hash with sha256.Sum256 in LokiService — the users label differs.
		sum := sha1.Sum([]byte(pw))
		block := initctl.LokiService(initctl.Loki, initctl.HTTP01, pw)
		for _, want := range []string{
			"      - traefik.http.middlewares.pstack-loki-auth.basicauth.users=pstack:{SHA}" + base64.StdEncoding.EncodeToString(sum[:]) + "\n",
			// ${…} stays for compose to fill from .env.
			"      - pstack.logging.push-url=https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/loki/api/v1/push\n",
		} {
			if !strings.Contains(block, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(block, pw) {
			t.Error("the password itself is in the compose file")
		}
	})

	t.Run("loki is on the logs network only", func(t *testing.T) {
		// Every preview container sits on preview-ingress, and Loki has no auth of its own.
		// negative control: render `networks: [preview-ingress, logs]` in LokiService — the list has two entries.
		v, err := yamlx.ParseString("services:\n" + initctl.LokiService(initctl.Loki, initctl.DNS01, pw))
		if err != nil {
			t.Fatal(err)
		}
		if n := fmt.Sprint(v.(*omap.Map).GetMap("services").GetMap("loki").GetSlice("networks")); n != "[logs]" {
			t.Errorf("networks %s", n)
		}
	})

	t.Run("logging off renders nothing", func(t *testing.T) {
		// negative control: drop the `logging != Loki` early return — both cases render the service.
		for _, l := range []initctl.Logging{initctl.LoggingNone, ""} {
			if got := initctl.LokiService(l, initctl.HTTP01, pw); got != "" {
				t.Errorf("%q rendered:\n%s", l, got)
			}
		}
	})
}

// Loki's plumbing is literal edits of lines the template already has. A template change that moves
// one must fail by name, never render a Loki that Traefik cannot reach.
func TestLokiWiring(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("substituted is the file Init writes", func(t *testing.T) {
		// negative control: drop the WAKE_ROUTER replacement from substituted — it differs from Init's file.
		if _, yaml := render(t, nil); substituted(initctl.HTTP01, initctl.Basic, "") != yaml {
			t.Error("substituted no longer mirrors Init's marker substitutions")
		}
	})

	t.Run("each edit lands once: Traefik's networks, the top-level volumes and networks", func(t *testing.T) {
		// negative control: strings.ReplaceAll for the networks anchor in LokiWiring — advanced-ui joins logs too.
		for _, c := range []initctl.Challenge{initctl.HTTP01, initctl.DNS01} {
			got, err := initctl.LokiWiring(substituted(c, initctl.Advanced, initctl.LokiService(initctl.Loki, c, pw)), initctl.Loki)
			if err != nil {
				t.Fatal(err)
			}
			for _, edit := range []string{
				"    networks: [preview-ingress, logs]\n",
				"volumes:\n  letsencrypt:\n  loki:\n",
				"  preview-shared:\n    external: true\n  logs: {}\n",
			} {
				if n := strings.Count(got, edit); n != 1 {
					t.Errorf("%s: %q appears %d times", c, edit, n)
				}
			}
			v, err := yamlx.ParseString(got)
			if err != nil {
				t.Fatalf("%s: %v", c, err)
			}
			d := v.(*omap.Map)
			svcs := d.GetMap("services")
			if n := fmt.Sprint(svcs.GetMap("traefik").GetSlice("networks")); n != "[preview-ingress logs]" {
				t.Errorf("%s: traefik networks %s", c, n)
			}
			// The advanced UI's identical line comes after Traefik's and must stay off `logs`.
			if n := fmt.Sprint(svcs.GetMap("advanced-ui").GetSlice("networks")); n != "[preview-ingress]" {
				t.Errorf("%s: advanced-ui networks %s", c, n)
			}
			if svcs.GetMap("loki") == nil || !d.GetMap("volumes").Has("loki") || !d.GetMap("networks").Has("logs") {
				t.Errorf("%s: the loki service, volume or network is missing", c)
			}
		}
	})

	t.Run("logging off returns the template byte-identical", func(t *testing.T) {
		// negative control: drop the `logging != Loki` early return — the file gains the logs network.
		in := substituted(initctl.HTTP01, initctl.Basic, "")
		for _, l := range []initctl.Logging{initctl.LoggingNone, ""} {
			if got, err := initctl.LokiWiring(in, l); err != nil || got != in {
				t.Errorf("%q changed the template (err %v)", l, err)
			}
		}
	})

	for _, c := range []struct{ what, anchor string }{
		{"Traefik networks line", "    networks: [preview-ingress]\n"},
		{"letsencrypt volume", "volumes:\n  letsencrypt:\n"},
		{"preview-shared network", "  preview-shared:\n    external: true\n"},
	} {
		t.Run("a template without the "+c.what+" fails by name", func(t *testing.T) {
			// negative control: skip the strings.Contains check in LokiWiring — no error.
			// ReplaceAll: the advanced UI carries a second copy of the networks line.
			in := strings.ReplaceAll(substituted(initctl.HTTP01, initctl.Advanced, ""), c.anchor, "")
			if _, err := initctl.LokiWiring(in, initctl.Loki); err == nil || !strings.Contains(err.Error(), c.what) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// Loki's config is fixed, and some of its values fail silently when changed: a moved schema `from`
// makes stored logs unreadable, a retention of 0s keeps everything forever. Byte-exactness against
// the design is the render golden's job.
func TestLokiConfig(t *testing.T) {
	t.Run("the load-bearing values", func(t *testing.T) {
		// negative control: change the schema `from` in templates/control/loki/config.yaml to "2026-09-15" — fails.
		v, err := yamlx.ParseString(pstack.LokiConfig)
		if err != nil {
			t.Fatal(err)
		}
		cfg := v.(*omap.Map)
		configs := cfg.GetMap("schema_config").GetSlice("configs")
		if len(configs) != 1 {
			t.Fatalf("%d schema configs, want 1", len(configs))
		}
		schema, _ := configs[0].(*omap.Map)
		port, _ := cfg.GetMap("server").Get("http_listen_port")
		for _, c := range []struct {
			what      string
			got, want any
		}{
			{"schema from", schema.GetString("from"), "2024-04-01"},
			{"schema store", schema.GetString("store"), "tsdb"},
			{"index period", schema.GetMap("index").GetString("period"), "24h"},
			{"retention", cfg.GetMap("limits_config").GetString("retention_period"), "168h"},
			{"WAL replay ceiling", cfg.GetMap("ingester").GetMap("wal").GetString("replay_memory_ceiling"), "1GB"},
			{"listen port", port, int64(3100)},
			{"delete request store", cfg.GetMap("compactor").GetString("delete_request_store"), "filesystem"},
		} {
			if c.got != c.want {
				t.Errorf("%s = %#v, want %#v", c.what, c.got, c.want)
			}
		}
	})
}
```

Notes the executor must not "fix":
- A bare `strings.Contains(block, "preview-ingress")` check would always fail, because the networks comment in the block names preview-ingress. The parse-based `[logs]` check is the real assertion.
- A bare `"certresolver"` check on dns01 would also always fail, because the TLS comment mentions certresolver. The check matches the label line.
- yamlx follows the YAML 1.2 core schema: `3100` parses as `int64`, while `24h`, `168h`, `1GB` and the quoted date parse as strings.

- [ ] **Step 2: Run the tests and confirm they fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run 'TestLokiService|TestLokiWiring|TestLokiConfig'
```

The failure is a compile error, so it takes the whole test package down, `TestInitGoldens` included. `-run` can't narrow a build failure. Expected:

```
# github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl_test [...]
internal/initctl/init_test.go:6xx:19: undefined: initctl.LokiService
internal/initctl/init_test.go:6xx:39: undefined: initctl.Loki
...
internal/initctl/init_test.go:6xx:31: undefined: initctl.Logging
internal/initctl/init_test.go:6xx:47: undefined: initctl.LoggingNone
internal/initctl/init_test.go:6xx:47: too many errors
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl [build failed]
```

- [ ] **Step 3: Create the Loki config and embed it**

Produce the file from the spec itself. Don't retype it: its comments carry `—` and `→`.

```
mkdir -p /Volumes/S1/code/preview-stacks/packages/pstack/templates/control/loki
sed -n '132,182p' /Volumes/S1/code/preview-stacks/docs/loki-logging-design.md > /Volumes/S1/code/preview-stacks/packages/pstack/templates/control/loki/config.yaml
diff <(sed -n '132,182p' /Volumes/S1/code/preview-stacks/docs/loki-logging-design.md) /Volumes/S1/code/preview-stacks/packages/pstack/templates/control/loki/config.yaml && echo byte-identical
```

Expected: `byte-identical`, with a 51-line file ending in `  reporting_enabled: false\n`. The resulting content is:

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

In `packages/pstack/assets.go`, replace line 1:

```go
// Package pstack carries the six files the binary embeds. Explicit paths, never a glob: the
```

with:

```go
// Package pstack carries the seven files the binary embeds. Explicit paths, never a glob: the
```

and replace lines 18-21:

```go
// ControlTemplate is the control stack's compose file, rendered by init.
//
//go:embed templates/control/docker-compose.yml
var ControlTemplate string
```

with:

```go
// ControlTemplate is the control stack's compose file, rendered by init.
//
//go:embed templates/control/docker-compose.yml
var ControlTemplate string

// LokiConfig is Loki's config file, which init writes beside the control compose file with
// `--logging loki`. Embedded as-is, never rendered: nothing in it varies by host, and it holds no
// credential.
//
//go:embed templates/control/loki/config.yaml
var LokiConfig string
```

- [ ] **Step 4: Implement the types, label key, service and wiring in init.go**

In `packages/pstack/internal/initctl/init.go`, replace the first import lines (28-30):

```go
	"crypto/rand"
	"encoding/hex"
	"errors"
```

with:

```go
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
```

Replace lines 69-77:

```go
// UI is which web UI `control.<domain>` serves.
type UI string

const (
	HTTP01   Challenge = "http01"
	DNS01    Challenge = "dns01"
	Basic    UI        = "basic"
	Advanced UI        = "advanced"
)
```

with the following. gofmt realigns the four existing lines, and that whitespace change is expected:

```go
// UI is which web UI `control.<domain>` serves.
type UI string

// Logging is where preview containers' logs go. "" counts as LoggingNone.
type Logging string

const (
	HTTP01      Challenge = "http01"
	DNS01       Challenge = "dns01"
	Basic       UI        = "basic"
	Advanced    UI        = "advanced"
	LoggingNone Logging   = "none"
	Loki        Logging   = "loki"
)

// PushURLLabel is the label on the control stack's loki container that carries the push URL. Deploys
// read it off the RUNNING container, the way DetectChallenge reads Traefik's flags, so `pstack up` on
// the host and the API always agree and there is no setting to keep in step. The one spelling of the key.
const PushURLLabel = "pstack.logging.push-url"
```

Then replace the tail of `AdvancedUIService` (lines 696-699):

```go
		"      - traefik.http.services.advanced-ui.loadbalancer.server.port=80",
		"",
	}, "\n")
}
```

with:

```go
		"      - traefik.http.services.advanced-ui.loadbalancer.server.port=80",
		"",
	}, "\n")
}

// LokiService is the opt-in Loki container, appended after the advanced UI at the same marker.
// Omitted entirely unless logging is loki: the template gains no line of its own, so every host
// without it renders byte-identical.
//
// Pushes arrive through Traefik's websecure entrypoint, and Traefik is the only other member of the
// `logs` network. The password is hashed HERE because compose cannot hash: `{SHA}`, not bcrypt,
// because Traefik checks it on every push — about one per container per second — and a 128-bit
// random secret needs no slow hash. The discovery label keeps `${LOKI_PUSH_PASSWORD}` for compose to
// fill from .env, so the password itself is never in this file.
func LokiService(logging Logging, challenge Challenge, password string) string {
	if logging != Loki {
		return ""
	}
	sum := sha1.Sum([]byte(password))
	lines := []string{
		"",
		"  # Loki, opt-in (`pstack init --logging loki`). Every node's loki log plugin pushes the logs of",
		"  # each preview container here, through Traefik at loki.${DOMAIN}.",
		"  loki:",
		"    image: grafana/loki:" + swarm.LokiVersion,
		"    restart: unless-stopped",
		"    mem_limit: 2g",
		"    # Only Traefik shares this network. Never preview-ingress: Loki has no auth, and every preview",
		"    # container sits on preview-ingress — any of them could read every stack's logs.",
		"    networks: [logs]",
		`    command: ["-config.file=/etc/loki/config.yaml"]`,
		"    environment:",
		"      GOMEMLIMIT: 1600MiB",
		"    volumes:",
		"      - ./loki:/etc/loki:ro      # the directory, not the file: a renamed-in file is not seen through a file mount",
		"      - loki:/loki",
		"    healthcheck:",
		`      test: ["CMD", "/usr/bin/loki", "-health"]   # distroless image: no shell, wget or curl`,
		"      start_period: 30s",
		"      interval: 30s",
		"      timeout: 10s",
		"      retries: 3",
		"    labels:",
		"      - traefik.enable=true",
		"      - traefik.docker.network=pstack-control_logs",
		"      - traefik.http.routers.pstack-loki.rule=Host(`loki.${DOMAIN}`) && PathPrefix(`/loki/api/v1/push`)",
		"      - traefik.http.routers.pstack-loki.entrypoints=websecure",
		"      # TLS follows the challenge: tls=true alone under DNS-01 (the wildcard covers loki.), plus",
		"      # its own certresolver under HTTP-01. Backwards orders a second certificate, or none at all.",
		"      - traefik.http.routers.pstack-loki.tls=true",
	}
	if challenge == HTTP01 {
		lines = append(lines, "      - traefik.http.routers.pstack-loki.tls.certresolver=le")
	}
	return strings.Join(append(lines,
		"      - traefik.http.routers.pstack-loki.middlewares=pstack-loki-auth",
		"      - traefik.http.middlewares.pstack-loki-auth.basicauth.users=pstack:{SHA}"+base64.StdEncoding.EncodeToString(sum[:]),
		"      - traefik.http.services.pstack-loki.loadbalancer.server.port=3100",
		"      # How deploys find Loki: pstack reads this label off the running container and hands it to",
		"      # every service it injects as loki-url.",
		"      - "+PushURLLabel+"=https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/loki/api/v1/push",
		"",
	), "\n")
}

// LokiWiring is the rest of Loki's plumbing: Traefik joins the `logs` network, and the file declares
// the `loki` volume and that network. Literal edits of lines the template already has, not new
// markers — every marker leaves a line behind when it renders "off", so a new one would change the
// compose file of every existing host.
//
// Each anchor is checked before it is replaced (strings.Replace with n=1, rule 8), so a template edit
// that moves one fails init by name instead of rendering a Loki that Traefik cannot reach. The
// networks anchor's FIRST match is Traefik's: pstack's line also lists preview-shared, and the
// advanced UI's identical line comes later. Ordered, never a map (rule 5): the first missing anchor
// is the one named.
func LokiWiring(template string, logging Logging) (string, error) {
	if logging != Loki {
		return template, nil
	}
	for _, e := range []struct{ what, anchor, with string }{
		{"Traefik networks line", "    networks: [preview-ingress]\n", "    networks: [preview-ingress, logs]\n"},
		{"letsencrypt volume", "volumes:\n  letsencrypt:\n", "volumes:\n  letsencrypt:\n  loki:\n"},
		{"preview-shared network", "  preview-shared:\n    external: true\n", "  preview-shared:\n    external: true\n  logs: {}\n"},
	} {
		if !strings.Contains(template, e.anchor) {
			return "", fmt.Errorf("the control template has no %s (%q) for --logging loki to extend", e.what, e.anchor)
		}
		template = strings.Replace(template, e.anchor, e.with, 1)
	}
	return template, nil
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/initctl assets.go && go vet ./internal/initctl/ . && go test -race -timeout 120s ./internal/initctl/ -run 'TestLokiService|TestLokiWiring|TestLokiConfig' -v 2>&1 | grep -E -- '^(--- |ok|FAIL)'
```

Expected: `gofmt -l` and `go vet` print nothing, followed by:

```
--- PASS: TestLokiService (0.00s)
--- PASS: TestLokiWiring (0.01s)
--- PASS: TestLokiConfig (0.00s)
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl	…
```

Then run the whole package, which proves `TestInitGoldens` (all 8 cells, files and transcripts) is unchanged:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl	…`

- [ ] **Step 6: Run every negative control (AGENTS.md Go rule 17)**

Apply each mutation by hand, run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -timeout 120s ./internal/initctl/ -run 'TestLokiService|TestLokiWiring|TestLokiConfig' -v 2>&1 | grep -- '--- FAIL'`, see the named subtest fail, then undo the edit by hand. Don't use git. Each mutation must fail only through its assertion, never through a build error.

| Mutation | Subtest that must FAIL |
|---|---|
| `if challenge == HTTP01 {` in LokiService → `if true {` | `TestLokiService/http01_orders_loki_its_own_certificate,…` |
| `sha1.Sum(` → `sha256.Sum256(` and import `crypto/sha256` in place of `crypto/sha1` | `TestLokiService/basic_auth_is_pstack:{SHA}…` |
| `"    networks: [logs]",` → `"    networks: [preview-ingress, logs]",` | `TestLokiService/loki_is_on_the_logs_network_only` (also `TestLokiWiring/each_edit_lands_once…`) |
| delete LokiService's `if logging != Loki { return "" }` | `TestLokiService/logging_off_renders_nothing` |
| delete the `#__WAKE_ROUTER__` line from `substituted` | `TestLokiWiring/substituted_is_the_file_Init_writes` |
| `strings.Replace(template, e.anchor, e.with, 1)` → `…, -1)` | `TestLokiWiring/each_edit_lands_once…` |
| delete LokiWiring's `if logging != Loki { return template, nil }` | `TestLokiWiring/logging_off_returns_the_template_byte-identical` |
| `if !strings.Contains(template, e.anchor) {` → `if false && !strings.Contains(template, e.anchor) {` | all three `TestLokiWiring/a_template_without_the_…_fails_by_name` |
| `from: "2024-04-01"` → `from: "2026-09-15"` in config.yaml | `TestLokiConfig/the_load-bearing_values` |

After undoing the last mutation, re-run the Step 3 `diff` (expect `byte-identical`) and the Step 5 full-package command (expect `ok`).

- [ ] **Step 7: Commit**

```
cd /Volumes/S1/code/preview-stacks && but status
```

Pick the IDs of exactly these four files: `packages/pstack/internal/initctl/init.go`, `packages/pstack/internal/initctl/init_test.go`, `packages/pstack/assets.go` and `packages/pstack/templates/control/loki/config.yaml`. Never pick `packages/conformance/golden/host/db/pstack.db-shm` / `pstack.db-wal`.

```
but commit -b claude/loki-logging -m "feat(initctl): render Loki into the control stack" <id-init.go> <id-init_test.go> <id-assets.go> <id-config.yaml>
```

---

### Task 3: initctl: Init --logging loki (password, .env, config, plugin step)

**Files:**
- Modify: `packages/pstack/internal/initctl/init.go:148-150` (Options.Logging), `:165-172` (randomToken → randomHex), `:184` and `:205` (destructure, token call), `:218-220` (push password), `:283-285` (loki dir), `:287-303` (new 1c block before `── 2.`), `:370-373` (substitution + LokiWiring), `:380` (envValues literal), `:386-388` (config.yaml write), `:431-433` (summary line), `:520-522` (envValues struct), `:558-567` (envFile block)
- Modify: `docs/usage.md:1315-1317`
- Modify: `packages/pstack/templates/control/README.md:24-26` (the "What `pstack init` writes" tree)
- Test: `packages/pstack/internal/initctl/init_test.go`

Line numbers are from the tree before T2. T2 adds `Logging`/`Loki` near `:66-77` and `LokiService`/`LokiWiring` beside `AdvancedUIService`, so later numbers shift. Every edit below quotes the exact existing text it replaces, so apply it by that text.

**Interfaces:**
- Consumes:
  - `swarm.LokiPluginInstall` (T1). A single-line `const string` with no leading or trailing whitespace, so it is also exactly what `exec.Fake` logs.
  - `initctl.Logging`, `initctl.LoggingNone`, `initctl.Loki` (T2).
  - `func LokiService(logging Logging, challenge Challenge, password string) string` (T2).
  - `func LokiWiring(template string, logging Logging) (string, error)` (T2).
  - `pstack.LokiConfig string` (T2, `assets.go`).
- Produces:
  - `initctl.Options.Logging Logging`. Only `== Loki` turns anything on, so `""` (every existing caller) means off.
  - `PSTACK_LOKI_PASSWORD`, read by `Init` only when `Logging == Loki`, with `||` semantics (empty means generate). A non-empty value that is not `^[0-9a-f]{32}$` makes `Init` return, before any command runs, an error starting `PSTACK_LOKI_PASSWORD must be 32 lowercase hex characters`. The error never echoes the value.
  - `control/.env` block after the admin block, only with loki. T4 reads the value back and T13's render goldens pin these exact bytes:
    ```
    # Loki's push password. Every node's log plugin sends it; Traefik checks its {SHA} hash.
    # `pstack upgrade` keeps it; `pstack logging off` removes it, and turning Loki back on makes a new one.
    LOKI_PUSH_PASSWORD=<32 hex>

    ```
  - Plugin step `runner.Run(swarm.LokiPluginInstall, exec.RunOptions{Label: "loki log plugin"})` in section `── 1c.`. When the step fails, `Init` returns an error containing `the loki log plugin did not install` and `swarm.LokiPluginInstall`. Under dry-run the transcript shows `  [dry-run] loki log plugin`.
  - `<DATA>/control/loki/` (ensureDir, step 1) and `<DATA>/control/loki/config.yaml` (== `pstack.LokiConfig`, mode 0644, written after dns.env).
  - Summary line `  logging   loki at https://loki.<domain> (push only); services without `logging:` ship to it`.
  - `randomHex(n int) string` (unexported), replacing `randomToken`. The token stays 24 bytes and the password is 16.

- [ ] **Step 1: Write the failing test**

Make sure the import block of `packages/pstack/internal/initctl/init_test.go` contains all of the following. T2's `TestLokiService`/`TestLokiWiring` may already have added some; add only the missing ones, because a duplicate import does not compile, and keep any other import T2 added:

```go
import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)
```

Append this test at the end of the file. It reuses the existing helpers `render` (init_test.go:48-63), `okRunner` (:22-36) and `read` (:38-45):

```go
// init --logging loki: the push password, its .env line, Loki's config and the plugin step. Logging off
// is today's bytes — TestInitGoldens proves that for all eight cells, so it is not repeated here.
func TestInitLoki(t *testing.T) {
	loki := func(r *exec.Fake, out *bytes.Buffer) func(*initctl.Options) {
		return func(o *initctl.Options) { o.Logging, o.Runner, o.Out = initctl.Loki, r, out }
	}
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("a generated push password is 32 hex in .env and its {SHA} hash is the basicauth label", func(t *testing.T) {
		// negative control: pass "" instead of lokiPassword to LokiService in Init — the label hash no longer matches.
		t.Setenv("PSTACK_LOKI_PASSWORD", "") // empty is unset (`||`), and it keeps the operator's shell out of the test
		var out bytes.Buffer
		dir, yaml := render(t, loki(okRunner("inactive", ""), &out))
		env := read(t, filepath.Join(dir, "control", ".env"))
		m := regexp.MustCompile(`(?m)^LOKI_PUSH_PASSWORD=([0-9a-f]{32})$`).FindStringSubmatch(env)
		if m == nil {
			t.Fatalf(".env has no 32-hex LOKI_PUSH_PASSWORD line:\n%s", env)
		}
		sum := sha1.Sum([]byte(m[1]))
		if want := "basicauth.users=pstack:{SHA}" + base64.StdEncoding.EncodeToString(sum[:]) + "\n"; !strings.Contains(yaml, want) {
			t.Errorf("compose lacks %q", want)
		}
		if !strings.Contains(out.String(), "  logging   loki at https://loki.preview.example.com (push only)") {
			t.Errorf("no logging summary line:\n%s", out.String())
		}
		// Nobody needs it printed: upgrade reads it back from .env, deploys from the loki container's label.
		if strings.Contains(out.String(), m[1]) {
			t.Error("init printed the push password")
		}
	})

	t.Run("PSTACK_LOKI_PASSWORD is used verbatim", func(t *testing.T) {
		// negative control: replace `os.Getenv("PSTACK_LOKI_PASSWORD")` with "" in Init — a generated password is written instead.
		t.Setenv("PSTACK_LOKI_PASSWORD", pw)
		dir, _ := render(t, loki(okRunner("inactive", ""), &bytes.Buffer{}))
		if env := read(t, filepath.Join(dir, "control", ".env")); !strings.Contains(env, "\nLOKI_PUSH_PASSWORD="+pw+"\n") {
			t.Errorf(".env does not carry the supplied password:\n%s", env)
		}
	})

	t.Run("a malformed PSTACK_LOKI_PASSWORD fails before anything runs or is written", func(t *testing.T) {
		// negative control: move the push-password block below `── 0. Preconditions` — the precondition commands run first.
		bad := "0123456789abcdef0123456789abcde$" // 32 characters; `$` is what Compose would expand in .env
		t.Setenv("PSTACK_LOKI_PASSWORD", bad)
		r := okRunner("inactive", "")
		dir := t.TempDir()
		err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, Runner: r, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.HasPrefix(err.Error(), "PSTACK_LOKI_PASSWORD must be 32 lowercase hex characters") {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), bad) {
			t.Error("the error echoes the secret")
		}
		if n := len(r.Commands()); n != 0 {
			t.Errorf("%d commands ran before the refusal: %v", n, r.Commands())
		}
		if _, err := os.Stat(filepath.Join(dir, "control", ".env")); !os.IsNotExist(err) {
			t.Errorf("control/.env was written: %v", err)
		}
	})

	t.Run("the plugin installs before the control stack comes up, and a failed install fails init", func(t *testing.T) {
		// negative control: move the `── 1c.` block below `── 4. Bring it up` — plugin runs after up, and up runs before the error.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		r := okRunner("inactive", "")
		render(t, loki(r, &bytes.Buffer{}))
		plugin, up := -1, -1
		for i, c := range r.Commands() {
			if c == swarm.LokiPluginInstall {
				plugin = i
			}
			if strings.Contains(c, "-p pstack-control") && strings.HasSuffix(c, " up -d --remove-orphans") {
				up = i
			}
		}
		if plugin < 0 || up < 0 || plugin > up {
			t.Errorf("plugin at %d, up at %d:\n%s", plugin, up, strings.Join(r.Commands(), "\n"))
		}

		failing := okRunner("inactive", "")
		ok := failing.Answer
		// Answer, not Fail: a Fake consults Fail only when Answer declines, and okRunner answers everything.
		failing.Answer = func(cmd string) (exec.Result, bool) {
			if cmd == swarm.LokiPluginInstall {
				return exec.Result{OK: false, Code: 1, Stderr: "Error response from daemon: pull access denied\n"}, true
			}
			return ok(cmd)
		}
		err := initctl.Init(initctl.Options{
			DataDir: t.TempDir(), Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, Runner: failing, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.Contains(err.Error(), "loki log plugin") || !strings.Contains(err.Error(), "pull access denied") ||
			!strings.Contains(err.Error(), swarm.LokiPluginInstall) {
			t.Fatalf("got %v", err)
		}
		for _, c := range failing.Commands() {
			if strings.Contains(c, " up -d") {
				t.Errorf("the control stack came up after the plugin failed: %s", c)
			}
		}
	})

	t.Run("control/loki/config.yaml is the embedded config at 0644", func(t *testing.T) {
		// negative control: write config.yaml at 0o600 — the mode check fails (Loki runs as uid 10001 and could not read it).
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir, _ := render(t, loki(okRunner("inactive", ""), &bytes.Buffer{}))
		p := filepath.Join(dir, "control", "loki", "config.yaml")
		if got := read(t, p); got != pstack.LokiConfig {
			t.Errorf("config.yaml differs from pstack.LokiConfig:\n%s", got)
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("mode %o, want 644", st.Mode().Perm())
		}
	})

	t.Run("logging off: no loki dir, no password line, no plugin — even with PSTACK_LOKI_PASSWORD set", func(t *testing.T) {
		// negative control: drop the `if logging == Loki` guard around the `── 1c.` block — a `docker plugin` command runs.
		t.Setenv("PSTACK_LOKI_PASSWORD", pw)
		r := okRunner("inactive", "")
		var out bytes.Buffer
		dir, yaml := render(t, func(o *initctl.Options) { o.Runner, o.Out = r, &out })
		if _, err := os.Stat(filepath.Join(dir, "control", "loki")); !os.IsNotExist(err) {
			t.Errorf("control/loki exists: %v", err)
		}
		if env := read(t, filepath.Join(dir, "control", ".env")); strings.Contains(env, "LOKI") {
			t.Errorf(".env mentions Loki:\n%s", env)
		}
		if strings.Contains(yaml, "loki") {
			t.Error("compose mentions loki")
		}
		for _, c := range r.Commands() {
			if strings.Contains(c, "docker plugin") {
				t.Errorf("plugin command with logging off: %s", c)
			}
		}
		if strings.Contains(out.String(), "logging") {
			t.Errorf("summary mentions logging:\n%s", out.String())
		}
	})

	t.Run("dry-run names the plugin step and the config file, and writes nothing", func(t *testing.T) {
		// negative control: wrap the `── 1c.` block in `if !dryRun` like the health wait — `[dry-run] loki log plugin` is missing.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir := t.TempDir()
		var out bytes.Buffer
		// The real runner, not a Fake: only it prints the `[dry-run] <label>` lines.
		if err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, DryRun: true,
			Runner: exec.New(exec.Options{DryRun: true, Out: &out}), Out: &out,
		}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"  [dry-run] loki log plugin\n",
			"  [dry-run] mkdir -p " + filepath.Join(dir, "control", "loki") + "\n",
			"  [dry-run] write " + filepath.Join(dir, "control", "loki", "config.yaml") + " (",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q:\n%s", want, out.String())
			}
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("dry-run wrote %v (%v)", entries, err)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run TestInitLoki
```

Expected: a build failure. `Options` has no `Logging` field yet (T2 added the type, not the field):

```
internal/initctl/init_test.go:NNN:NN: o.Logging undefined (type *initctl.Options has no field or method Logging)
internal/initctl/init_test.go:NNN:NN: unknown field Logging in struct literal of type initctl.Options
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl [build failed]
```

- [ ] **Step 3: Implement**

All edits are in `packages/pstack/internal/initctl/init.go`. The file already imports everything used here (`crypto/rand`, `encoding/hex`, `errors`, `os`, `path/filepath`, `strings`, `pstack`, `exec`, `swarm`), so no import changes are needed.

**3a. `Options.Logging`.** Replace:

```go
	Orchestrator spec.Orchestrator
	// DNSProvider is the lego DNS-01 provider code, e.g. `cloudflare`. Required for, and only used by, `dns01`.
```

with:

```go
	Orchestrator spec.Orchestrator
	// Logging is where this host's previews' logs go.
	//
	//   none  DEFAULT ("" counts as none). Nothing is added: the control stack, .env and every deploy
	//         stay exactly what they were.
	//   loki  A Loki service in the control stack, reachable only at loki.<domain>'s push path; its log
	//         plugin on this node; a push password in .env. Deploys give every service without its
	//         own `logging:` the loki driver.
	Logging Logging
	// DNSProvider is the lego DNS-01 provider code, e.g. `cloudflare`. Required for, and only used by, `dns01`.
```

**3b. `randomToken` → `randomHex`.** Replace:

```go
// randomToken is 24 random bytes, hex. Hex on purpose: `$` in a `.env` value is expanded by Compose.
func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
```

with:

```go
// randomHex is n random bytes, hex. Hex on purpose: `$` in a `.env` value is expanded by Compose, and
// the Loki push password also sits in a URL's userinfo, where a reserved character would break it.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
```

Replace `		pstackToken = randomToken()` with `		pstackToken = randomHex(24)`.

**3c. Destructure `logging`.** Replace:

```go
	challenge, ui, orchestrator, dryRun, runner := opts.Challenge, opts.UI, opts.Orchestrator, opts.DryRun, opts.Runner
```

with:

```go
	challenge, ui, orchestrator, logging, dryRun, runner := opts.Challenge, opts.UI, opts.Orchestrator, opts.Logging, opts.DryRun, opts.Runner
```

**3d. The push password, before step 0.** Replace:

```go
	adminUser, adminPassword := os.Getenv("PSTACK_ADMIN_USER"), os.Getenv("PSTACK_ADMIN_PASSWORD")

	// ── 0. Preconditions ────────────────────────────────────────────────────────────────────────
```

with:

```go
	adminUser, adminPassword := os.Getenv("PSTACK_ADMIN_USER"), os.Getenv("PSTACK_ADMIN_PASSWORD")

	// Loki's push password, only with --logging loki. `||` like the token: a set one is kept — upgrade
	// hands back the one in .env, because every running container pushes with it — and an empty one is
	// generated. 16 bytes is 128 bits, which is why Traefik's fast {SHA} check is no weakness. Checked
	// before step 0 runs anything: it lands in .env, where Compose expands `$`, and in the push URL's
	// userinfo. The error never echoes it.
	lokiPassword := ""
	if logging == Loki {
		lokiPassword = os.Getenv("PSTACK_LOKI_PASSWORD")
		if lokiPassword == "" {
			lokiPassword = randomHex(16)
		} else if len(lokiPassword) != 32 || strings.Trim(lokiPassword, "0123456789abcdef") != "" {
			return errors.New("PSTACK_LOKI_PASSWORD must be 32 lowercase hex characters, like LOKI_PUSH_PASSWORD in control/.env")
		}
	}

	// ── 0. Preconditions ────────────────────────────────────────────────────────────────────────
```

(`strings.Trim` with the hex alphabet leaves `""` only when every byte is `[0-9a-f]`, so together with the length check it is exactly `^[0-9a-f]{32}$` with no `regexp` import. `hex.DecodeString` would accept uppercase.)

**3e. The loki config directory, step 1.** Replace:

```go
	if err := ensureDir(out, filepath.Join(dataDir, "db"), dryRun, 0o700); err != nil {
		return err
	}
```

with:

```go
	if err := ensureDir(out, filepath.Join(dataDir, "db"), dryRun, 0o700); err != nil {
		return err
	}
	// Loki's config, mounted as a DIRECTORY (a file mount keeps the old inode when the file is replaced
	// by rename). Left to the umask: the image runs as uid 10001 and must traverse it.
	if logging == Loki {
		if err := ensureDir(out, filepath.Join(controlDir, "loki"), dryRun, noMode); err != nil {
			return err
		}
	}
```

**3f. Section 1c, the plugin.** Replace:

```go
	// ── 2. External networks ────────────────────────────────────────────────────────────────────
```

with:

```go
	// ── 1c. The Loki log plugin ─────────────────────────────────────────────────────────────────
	// Only with --logging loki, and fatal. Under compose every service given the loki driver fails to
	// CREATE without the plugin, so a host that says "logging on" but cannot run a logged preview is
	// worse than an init that stops here, before any config is written. The line is idempotent
	// (swarm.LokiPluginInstall), so re-running init is the retry. Workers get it from their join material.
	if logging == Loki {
		r := runner.Run(swarm.LokiPluginInstall, exec.RunOptions{Label: "loki log plugin"})
		if !r.OK {
			return errors.New("the loki log plugin did not install:\n" + exec.Indent(firstOf(r.Stderr, r.Stdout)) + "\n" +
				"  Run it by hand on this host, then re-run pstack init:\n" +
				"  " + swarm.LokiPluginInstall)
		}
	}

	// ── 2. External networks ────────────────────────────────────────────────────────────────────
```

**3g. Render Loki into the compose file.** Replace:

```go
	template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui), 1)
	if err := write(out, composePath, template, 0o644, dryRun); err != nil {
		return err
	}
```

with:

```go
	// Loki rides on the last marker instead of adding one: every marker leaves a line behind when off, so
	// a new marker would change every existing host's file (and the 8 render goldens). Its other three
	// edits are anchors that LokiWiring checks before replacing. Both are no-ops unless logging is loki.
	template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui)+LokiService(logging, challenge, lokiPassword), 1)
	template, err := LokiWiring(template, logging)
	if err != nil {
		return err
	}
	if err := write(out, composePath, template, 0o644, dryRun); err != nil {
		return err
	}
```

(`:=` is legal here. `template` already exists in the function scope, and no function-scope `err` exists yet: every earlier `err` is `if err :=`-scoped.)

**3h. Pass the password to `.env`.** Replace:

```go
		adminUser: adminUser, adminPassword: adminPassword,
```

with:

```go
		adminUser: adminUser, adminPassword: adminPassword, lokiPassword: lokiPassword,
```

**3i. Write `config.yaml`.** Replace:

```go
	if err := write(out, filepath.Join(controlDir, "dns.env"), dnsEnvFile(dnsProvider, opts.Token), 0o600, dryRun); err != nil {
		return err
	}
```

with:

```go
	if err := write(out, filepath.Join(controlDir, "dns.env"), dnsEnvFile(dnsProvider, opts.Token), 0o600, dryRun); err != nil {
		return err
	}

	// 0644, not 0600 like its neighbours: the Loki image runs as uid 10001 and must read it, and it holds
	// no credential. The password stays in .env and reaches Traefik only as a hash.
	if logging == Loki {
		if err := write(out, filepath.Join(controlDir, "loki", "config.yaml"), pstack.LokiConfig, 0o644, dryRun); err != nil {
			return err
		}
	}
```

**3j. Summary line.** Replace:

```go
		lines = append(lines, "            workers need "+swarm.PortList()+" open to and from this host")
	}
```

with:

```go
		lines = append(lines, "            workers need "+swarm.PortList()+" open to and from this host")
	}
	if logging == Loki {
		lines = append(lines, "  logging   loki at https://loki."+domain+" (push only); services without `logging:` ship to it")
	}
```

**3k. `envValues` field.** Replace:

```go
	adminUser     string
	adminPassword string
}
```

with:

```go
	adminUser     string
	adminPassword string
	lokiPassword  string
}
```

**3l. The `.env` block.** The two comment lines are the final wording: T13's render goldens pin them. They stay true after T4, where `pstack upgrade` carries the password back as env, `pstack logging off` re-runs init without Loki (so the line is dropped), and `pstack logging loki` on a host with no line generates a fresh one. Replace:

```go
			"PSTACK_ADMIN_PASSWORD="+v.adminPassword,
			"",
		)
	}
	return strings.Join(lines, "\n")
```

with:

```go
			"PSTACK_ADMIN_PASSWORD="+v.adminPassword,
			"",
		)
	}
	// Only with --logging loki, so a logging-off .env is byte-identical to before. Compose interpolates it
	// into the loki container's push-url label, which is how deploys learn the URL; upgrade reads it back.
	if v.lokiPassword != "" {
		lines = append(lines,
			"# Loki's push password. Every node's log plugin sends it; Traefik checks its {SHA} hash.",
			"# `pstack upgrade` keeps it; `pstack logging off` removes it, and turning Loki back on makes a new one.",
			"LOKI_PUSH_PASSWORD="+v.lokiPassword,
			"",
		)
	}
	return strings.Join(lines, "\n")
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run 'TestInitLoki|TestInitGoldens' -v 2>&1 | grep -E '^(\s*--- (PASS|FAIL)|ok|FAIL)'
```

Expected: every `TestInitLoki/…` subtest and every `TestInitGoldens/…` subtest (8 files, 8 dry-run transcripts, the generated-token case) is `--- PASS`, ending with:

```
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl	N.NNNs
```

`TestInitGoldens` passing unchanged is the logging-off byte-identity proof: files, modes, transcripts and dry-run byte counts.

- [ ] **Step 5: Run each negative control**

Apply one mutation at a time, run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/initctl/ -run TestInitLoki`, confirm the named subtest FAILS, then revert the mutation before the next one:

1. In 3g, pass `""` instead of `lokiPassword` to `LokiService`. Fails `a generated push password is 32 hex…` (hash mismatch).
2. In 3d, replace `os.Getenv("PSTACK_LOKI_PASSWORD")` with `""`. Fails `PSTACK_LOKI_PASSWORD is used verbatim`.
3. Move the 3d block to just below the `── 0. Preconditions` loop. Fails `a malformed PSTACK_LOKI_PASSWORD…` (commands ran).
4. Move the 3f `── 1c.` block to just below `── 4. Bring it up`. Fails `the plugin installs before…`.
5. In 3i, change `0o644` to `0o600`. Fails `control/loki/config.yaml is the embedded config at 0644`.
6. In 3f, delete the `if logging == Loki {` guard, keeping the body. Fails `logging off: …`.
7. In 3f, change the guard to `if logging == Loki && !dryRun {`. Fails `dry-run names the plugin step…`.

After reverting all seven, rerun Step 4: it is green again.

- [ ] **Step 6: Format and run the neighbouring packages**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/initctl && go vet ./internal/initctl/ && go test -race -timeout 120s ./internal/initctl/ ./internal/upgrade/ ./internal/cli/
```

Expected: `gofmt -l` prints nothing, `go vet` prints nothing, and:

```
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl	N.NNNs
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade	N.NNNs
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/cli	N.NNNs
```

(upgrade and cli call `initctl.Init` without `Logging`, so they exercise the path where `""` means off.)

- [ ] **Step 7: Document the init steps**

In `docs/usage.md`, §7 "What it does, in order", replace the line:

```markdown
| 1b. Swarm | under `--orchestrator swarm`: `docker swarm init` if this daemon is not already a manager. Never leaves a swarm |
```

with:

```markdown
| 1b. Swarm | under `--orchestrator swarm`: `docker swarm init` if this daemon is not already a manager. Never leaves a swarm |
| 1c. Loki plugin | only with `--logging loki`: installs `grafana/loki-docker-driver:3.7.7` on this node as `loki` and enables it — skipped when present, enabled when disabled. A failure fails `init`: under compose every logged service would fail to create |
```

In the same table, replace the end of the `3. Config` row:

```markdown
Traefik gains a metrics entrypoint on `:8082` (unpublished; the API reads it for `sleep.idle`) and the pstack container the catch-all `pstack-wake` router |
```

with:

```markdown
Traefik gains a metrics entrypoint on `:8082` (unpublished; the API reads it for `sleep.idle`) and the pstack container the catch-all `pstack-wake` router. With `--logging loki`: the `loki` service, `control/loki/config.yaml` (`0644` — Loki runs as uid 10001; no credential) and `LOKI_PUSH_PASSWORD` in `.env` (`PSTACK_LOKI_PASSWORD`, or 32 generated hex characters) |
```

(The flag/env table rows for `--logging` and `PSTACK_LOKI_PASSWORD` are Task 5's.)

- [ ] **Step 8: Update the control template's file tree**

In `packages/pstack/templates/control/README.md`, "What `pstack init` writes, and where" (lines 24-26; spaces, no tabs), replace:

```
    ├── .env             0600    DOMAIN, ACME_EMAIL, DNS_PROVIDER, PSTACK_IMAGE, PSTACK_TOKEN
    ├── dns.env          0600    the DNS-01 credential, under the variable name lego expects
    └── traefik-dynamic/         the file provider's watched directory (starts empty)
```

with:

```
    ├── .env             0600    DOMAIN, ACME_EMAIL, DNS_PROVIDER, PSTACK_IMAGE, PSTACK_TOKEN, LOKI_PUSH_PASSWORD (only with --logging loki)
    ├── dns.env          0600    the DNS-01 credential, under the variable name lego expects
    ├── loki/config.yaml 0644    Loki's fixed config (only with --logging loki)
    └── traefik-dynamic/         the file provider's watched directory (starts empty)
```

The README is not embedded (`assets.go` embeds by explicit path) and no test reads it, so nothing reruns.

- [ ] **Step 9: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of `packages/pstack/internal/initctl/init.go`, `packages/pstack/internal/initctl/init_test.go`, `docs/usage.md` and `packages/pstack/templates/control/README.md`. Include nothing else, and never `golden/host/**` or the untracked `pstack.db-shm`/`pstack.db-wal`. Then:

```bash
but commit -b claude/loki-logging -m "feat(initctl): init --logging loki installs the plugin and writes the push password" <init.go-id> <init_test.go-id> <usage.md-id> <README.md-id>
```

---

### Task 4: upgrade: read Loki back; SwitchLogging; LoggedDeployments

**Files:**
- Modify: `packages/pstack/internal/upgrade/upgrade.go:38-52` (imports), `:60-84` (ControlState), `:86-91` (regexp vars), `:180-192` (ReadControlState tail), `:245-257` (initFlags), `:315-322` (initEnv), `:359-387` (SwitchUI loop → shared runSwitch; SwitchLogging, loggingWord, LoggedDeployments added after it), `:511` (dry-run carried-env list)
- Test: `packages/pstack/internal/upgrade/upgrade_test.go`

**Interfaces:**
- Consumes: `initctl.Logging`, `initctl.LoggingNone` (`"none"`), `initctl.Loki` (`"loki"`) (T2); `initctl.Options.Logging` and Init reading `PSTACK_LOKI_PASSWORD`, writing `LOKI_PUSH_PASSWORD=<32 hex>` to control/.env only when Loki, rendering `  loki:` into control/docker-compose.yml (T3); `autolabel.GeneratedCompose` (autolabel.go:81, existing); `registry.New(dataDir).Root` (registry.go:151-154, existing).
- Produces:
  - `type ControlState struct { …; DNSToken string; Logging initctl.Logging; LokiPassword string }`
  - `initFlags` appends `--logging loki` after `--orchestrator` when `state.Logging == initctl.Loki`. `initEnv` sets `PSTACK_LOKI_PASSWORD` when `state.LokiPassword != ""`. Upgrade's dry-run carried list is `{"PSTACK_TOKEN", "PSTACK_DNS_TOKEN", "PSTACK_LOKI_PASSWORD"}`.
  - `type SwitchLoggingOptions struct { DataDir string; Logging initctl.Logging; Runner exec.Runner; Log func(string) }`
  - `func SwitchLogging(opts SwitchLoggingOptions) (changed bool, steps []Step, err error)`. For an off target the init step's Cmd ends ` --logging none` (see amendments).
  - `func LoggedDeployments(dataDir string) int`

Why these values pass initguard (T5). An upgrade on a loki host types `--logging loki` and carries `PSTACK_LOKI_PASSWORD`, so neither new check fires. off→loki types `--logging loki`, and `state.LokiPassword == ""`, so check (2) stays quiet. loki→off types `--logging none`, so check (1) (`!typed("--logging") && state.Logging == Loki && p.Logging != Loki`) does not fire. Without the typed flag, every real `pstack logging off` would be refused with exit 3.

---

- [ ] **Step 1: Write the failing read-back tests**

In `packages/pstack/internal/upgrade/upgrade_test.go`, add this directly after `realControl` (after its closing `}`, before `func cmds`):

```go
// lokiPassword is a push password in the shape init generates: 32 lowercase hex.
const lokiPassword = "0123456789abcdef0123456789abcdef"

// lokiControl is realControl with `--logging loki`, the push password pinned through the environment
// the way `upgrade` hands init one. Generated by the REAL init for realControl's reason: the detector
// must read what init emits, not what a fixture guesses it emits.
func lokiControl(t *testing.T, password string) string {
	t.Helper()
	t.Setenv("PSTACK_LOKI_PASSWORD", password)
	dataDir := t.TempDir()
	if err := initctl.Init(initctl.Options{
		DataDir: dataDir, Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: initctl.HTTP01, UI: initctl.Basic, Orchestrator: spec.Compose, Logging: initctl.Loki,
		Runner: healthyRunner(), Out: &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}
	return dataDir
}
```

Inside `TestUpgrade`, insert these three subtests immediately before `t.Run("a Go-release target is simply installed — this binary IS the Go release", …`:

```go
	t.Run("a loki host keeps Loki and its push password through an upgrade", func(t *testing.T) {
		// `init` mints a new push password when PSTACK_LOKI_PASSWORD is unset, and re-renders without
		// Loki when --logging is left off. Either one cuts every running container's logs, from a
		// command meant to change nothing but the version.
		// negative control: drop the lokiSvc match in ReadControlState — Logging reads none and the plan has no --logging loki.
		s := mustRead(t, lokiControl(t, lokiPassword))
		if s.Logging != initctl.Loki || s.LokiPassword != lokiPassword {
			t.Fatalf("logging = %q, password = %q", s.Logging, s.LokiPassword)
		}
		init := find(PlanUpgrade(PlanArgs{Phase: Resume, Target: "x", State: s}), "pstack init")
		if init == nil || !strings.HasSuffix(init.Cmd, " --orchestrator compose --logging loki") || init.Env["PSTACK_LOKI_PASSWORD"] != lokiPassword {
			t.Errorf("init step: %+v", init)
		}
	})

	t.Run("the dry-run transcript names the push password among what it carries", func(t *testing.T) {
		// negative control: leave PSTACK_LOKI_PASSWORD out of Upgrade's carried-env list — the parenthesis names only the token.
		var said []string
		if _, err := Upgrade(Options{DataDir: lokiControl(t, lokiPassword), Target: "0.28.0", Runner: quiet(), Log: func(l string) { said = append(said, l) }}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(said, "\n"), "--logging loki (with the existing PSTACK_TOKEN and PSTACK_LOKI_PASSWORD)") {
			t.Errorf("transcript:\n%s", strings.Join(said, "\n"))
		}
	})

	t.Run("a logging-off host reads none, is not refused for having no password, and plans no --logging", func(t *testing.T) {
		// negative control: read LOKI_PUSH_PASSWORD through need() — ReadControlState refuses the off host.
		s := mustRead(t, realControl(t, initctl.Basic, initctl.HTTP01))
		if s.Logging != initctl.LoggingNone || s.LokiPassword != "" {
			t.Errorf("logging = %q, password = %q", s.Logging, s.LokiPassword)
		}
		init := find(PlanUpgrade(PlanArgs{Phase: Resume, Target: "x", State: s}), "pstack init")
		if strings.Contains(init.Cmd, "--logging") {
			t.Errorf("cmd: %s", init.Cmd)
		}
		if _, ok := init.Env["PSTACK_LOKI_PASSWORD"]; ok {
			t.Errorf("env: %v", init.Env)
		}
	})
```

- [ ] **Step 2: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run TestUpgrade
```

Expected: a build failure (the line numbers will vary):

```
internal/upgrade/upgrade_test.go:NN:NN: s.Logging undefined (type *ControlState has no field or method Logging)
internal/upgrade/upgrade_test.go:NN:NN: s.LokiPassword undefined (type *ControlState has no field or method LokiPassword)
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade [build failed]
```

If the error is instead `unknown field Logging in struct literal of type initctl.Options`, T3 has not landed. Stop.

- [ ] **Step 3: Implement the read-back, the flag and the env**

In `packages/pstack/internal/upgrade/upgrade.go`, replace the end of `ControlState` (lines 78-84):

```go
	// DNSToken is the DNS-01 credential from `control/dns.env`, or "" when the host has none (http01,
	// a tokenless provider, or the file is missing). Read back for the same reason the token is:
	// `init` rewrites dns.env from PSTACK_DNS_TOKEN on every run, so an upgrade that does not carry
	// it zeroes the only copy — Traefik is recreated with no credential, the existing wildcard keeps
	// serving, and the renewal silently fails weeks later.
	DNSToken string
}
```

with:

```go
	// DNSToken is the DNS-01 credential from `control/dns.env`, or "" when the host has none (http01,
	// a tokenless provider, or the file is missing). Read back for the same reason the token is:
	// `init` rewrites dns.env from PSTACK_DNS_TOKEN on every run, so an upgrade that does not carry
	// it zeroes the only copy — Traefik is recreated with no credential, the existing wildcard keeps
	// serving, and the renewal silently fails weeks later.
	DNSToken string
	// Logging is `loki` when the generated compose has the loki service. Not in `.env`, for UI's
	// reason: init bakes the decision into the compose file, so it is read back from there. Absent
	// means none — every host from before the option, and every host that turned it off.
	Logging initctl.Logging
	// LokiPassword is LOKI_PUSH_PASSWORD from `.env`, or "" when the host has none. Carried for the
	// token's reason: `init` mints a new one when PSTACK_LOKI_PASSWORD is unset, and every running
	// container keeps pushing with the old one — 401, never retried, logs gone until it is redeployed.
	LokiPassword string
}
```

Replace the regexp var block (lines 86-91):

```go
var (
	envLineRe    = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(.*)$`)
	dnsChallenge = regexp.MustCompile(`(?i)dnschallenge`)
	advancedSvc  = regexp.MustCompile(`(?m)^\s{2}advanced-ui:`)
	semverRe     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)
)
```

with:

```go
var (
	envLineRe    = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(.*)$`)
	dnsChallenge = regexp.MustCompile(`(?i)dnschallenge`)
	advancedSvc  = regexp.MustCompile(`(?m)^\s{2}advanced-ui:`)
	lokiSvc      = regexp.MustCompile(`(?m)^\s{2}loki:`)
	semverRe     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)
)
```

Replace the tail of `ReadControlState` (lines 180-192):

```go
		UI:           initctl.Basic,
		Orchestrator: spec.Compose,
	}
	if dnsChallenge.MatchString(compose) {
		s.Challenge = initctl.DNS01
	}
	if advancedSvc.MatchString(compose) {
		s.UI = initctl.Advanced
	}
	if env["PSTACK_ORCHESTRATOR"] == "swarm" {
		s.Orchestrator = spec.Swarm
	}
	return s, nil
}
```

with:

```go
		UI:           initctl.Basic,
		Orchestrator: spec.Compose,
		Logging:      initctl.LoggingNone,
		// Not need(): absent is a host without Loki, never a refusal.
		LokiPassword: env["LOKI_PUSH_PASSWORD"],
	}
	if dnsChallenge.MatchString(compose) {
		s.Challenge = initctl.DNS01
	}
	if advancedSvc.MatchString(compose) {
		s.UI = initctl.Advanced
	}
	// The loki service and the `loki:` volume beside it both match, and both exist only when init
	// ran with --logging loki: the logging-off template has no `loki` anywhere.
	if lokiSvc.MatchString(compose) {
		s.Logging = initctl.Loki
	}
	if env["PSTACK_ORCHESTRATOR"] == "swarm" {
		s.Orchestrator = spec.Swarm
	}
	return s, nil
}
```

In `initFlags`, replace (lines 256-257):

```go
	flags = append(flags, "--orchestrator "+string(state.Orchestrator))
	return strings.Join(flags, " ")
```

with:

```go
	flags = append(flags, "--orchestrator "+string(state.Orchestrator))
	if state.Logging == initctl.Loki {
		flags = append(flags, "--logging loki")
	}
	return strings.Join(flags, " ")
```

Replace `initEnv` with its comment (lines 315-322):

```go
// initEnv is what `init` must be handed so that NOTHING rotates: the machine token always, and the
// DNS-01 credential when the host has one. `init` writes dns.env from PSTACK_DNS_TOKEN
// unconditionally, so omitting it here is how an upgrade used to blank a host's Cloudflare token.
func initEnv(state *ControlState) map[string]string {
	env := map[string]string{"PSTACK_TOKEN": state.Token}
	if state.DNSToken != "" {
		env["PSTACK_DNS_TOKEN"] = state.DNSToken
	}
	return env
}
```

with:

```go
// initEnv is what `init` must be handed so that NOTHING rotates: the machine token always, and the
// DNS-01 credential and the Loki push password when the host has them. `init` writes dns.env from
// PSTACK_DNS_TOKEN unconditionally, so omitting it here is how an upgrade used to blank a host's
// Cloudflare token; an omitted push password is minted fresh the same way.
func initEnv(state *ControlState) map[string]string {
	env := map[string]string{"PSTACK_TOKEN": state.Token}
	if state.DNSToken != "" {
		env["PSTACK_DNS_TOKEN"] = state.DNSToken
	}
	if state.LokiPassword != "" {
		env["PSTACK_LOKI_PASSWORD"] = state.LokiPassword
	}
	return env
}
```

In `Upgrade`'s dry-run block, replace (line 511):

```go
			for _, k := range []string{"PSTACK_TOKEN", "PSTACK_DNS_TOKEN"} {
```

with:

```go
			for _, k := range []string{"PSTACK_TOKEN", "PSTACK_DNS_TOKEN", "PSTACK_LOKI_PASSWORD"} {
```

The loop adds only keys that are present, so a host without Loki prints exactly what it printed before.

- [ ] **Step 4: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run 'TestUpgrade|TestUISwitch'
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade	<time>`. `TestUpgradeGoldens` (all 16 upgrade-plan and ui-switch transcripts) passes unchanged, which proves logging-off output is byte-identical.

- [ ] **Step 5: Write the failing LoggedDeployments test**

Add `"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"` to the test file's internal imports, as the first internal import. Append after `TestUISwitch`:

```go
func TestLoggedDeployments(t *testing.T) {
	t.Run("counts the deployments whose generated compose still names the loki driver", func(t *testing.T) {
		// negative control: count every compose.generated.yml without checking for "loki-url" — b is counted, 2 ≠ 1.
		dataDir := t.TempDir()
		for _, d := range []struct{ id, generated string }{
			{"a", `{"services":{"web":{"logging":{"driver":"loki","options":{"loki-url":"https://pstack:x@loki.preview.example.com/loki/api/v1/push"}}}}}`},
			{"b", `{"services":{"web":{"logging":{"driver":"json-file"}}}}`},
			{"c", ""}, // never brought up: no derived file
		} {
			dir := filepath.Join(dataDir, "deployments", d.id)
			if err := os.MkdirAll(dir, 0o777); err != nil {
				t.Fatal(err)
			}
			if d.generated == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, autolabel.GeneratedCompose), []byte(d.generated), 0o666); err != nil {
				t.Fatal(err)
			}
		}
		if n := LoggedDeployments(dataDir); n != 1 {
			t.Errorf("n = %d", n)
		}
	})

	t.Run("a host with no deployments directory counts zero", func(t *testing.T) {
		// negative control: return -1 when the deployments directory cannot be read — n is -1.
		if n := LoggedDeployments(t.TempDir()); n != 0 {
			t.Errorf("n = %d", n)
		}
	})
}
```

- [ ] **Step 6: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run TestLoggedDeployments
```

Expected: `internal/upgrade/upgrade_test.go:NN:NN: undefined: LoggedDeployments` then `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade [build failed]`.

- [ ] **Step 7: Implement LoggedDeployments**

Replace the internal import block of `upgrade.go` (lines 47-51):

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
```

with:

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
```

These imports create no cycle. `go list -deps ./internal/autolabel` holds no `upgrade`, and the only test importing upgrade from below is `initctl_test`, an external test package.

Insert this directly after `SwitchUI`'s closing `}` (line 387), before `// Options are Upgrade's inputs.`:

```go
// LoggedDeployments is how many deployments' generated compose still names the loki driver: the
// ones whose containers keep pushing after logging is turned off, until their next deploy.
//
// Read from the file, not from docker: a sleeping deployment has no containers to ask and still
// carries the driver until wake re-materializes it. The derived file sits in the deployment
// directory itself — autolabel writes it beside the submitted compose.yml, which registry.Put puts
// there. JSON despite the extension, so the key is matched with its quotes. A deployment whose file
// is missing or unreadable is not counted.
func LoggedDeployments(dataDir string) int {
	root := registry.New(dataDir).Root
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0 // nothing deployed yet
	}
	n := 0
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, e.Name(), autolabel.GeneratedCompose))
		if err == nil && strings.Contains(string(b), `"loki-url"`) {
			n++
		}
	}
	return n
}
```

- [ ] **Step 8: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run TestLoggedDeployments
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade	<time>`.

- [ ] **Step 9: Write the failing switch test**

Append after `TestLoggedDeployments`:

```go
func TestLoggingSwitch(t *testing.T) {
	os.Unsetenv("PSTACK_TOKEN")
	os.Unsetenv("PSTACK_IMAGE")
	os.Unsetenv("PSTACK_UI_IMAGE")

	t.Run("off → loki re-inits once with --logging loki and the stored token", func(t *testing.T) {
		// negative control: drop the `--logging loki` append from initFlags — the command differs.
		var said []string
		changed, steps, err := SwitchLogging(SwitchLoggingOptions{DataDir: realControl(t, initctl.Basic, initctl.HTTP01), Logging: initctl.Loki, Runner: quiet(), Log: func(l string) { said = append(said, l) }})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"pstack init --domain 'preview.example.com' --acme-email 'ops@example.com' --challenge http01 --orchestrator compose --logging loki"}
		if !changed || !reflect.DeepEqual(cmds(steps), want) {
			t.Fatalf("changed=%v got %v", changed, cmds(steps))
		}
		// The token, always. No push password yet: this init generates the first one.
		if steps[0].Env["PSTACK_TOKEN"] == "" {
			t.Error("no token")
		}
		if _, ok := steps[0].Env["PSTACK_LOKI_PASSWORD"]; ok {
			t.Errorf("env: %v", steps[0].Env)
		}
		if !contains(said, "off → loki   (preview.example.com)") || strings.Contains(strings.Join(said, "\n"), "still carry") {
			t.Errorf("said %v", said)
		}
	})

	t.Run("loki → off types --logging none and counts what still carries the driver", func(t *testing.T) {
		// initguard refuses an init on a loki host that leaves --logging off: the untyped omission is the
		// silent revert it exists to stop. The switch is the decision, so it spells it.
		// negative control: drop the ` --logging none` suffix in SwitchLogging — the command differs.
		dataDir := lokiControl(t, lokiPassword)
		dep := filepath.Join(dataDir, "deployments", "pr-7")
		if err := os.MkdirAll(dep, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dep, autolabel.GeneratedCompose), []byte(`{"services":{"web":{"logging":{"driver":"loki","options":{"loki-url":"https://pstack:x@loki.preview.example.com/loki/api/v1/push"}}}}}`), 0o666); err != nil {
			t.Fatal(err)
		}
		var said []string
		changed, steps, err := SwitchLogging(SwitchLoggingOptions{DataDir: dataDir, Logging: initctl.LoggingNone, Runner: quiet(), Log: func(l string) { said = append(said, l) }})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"pstack init --domain 'preview.example.com' --acme-email 'ops@example.com' --challenge http01 --orchestrator compose --logging none"}
		if !changed || !reflect.DeepEqual(cmds(steps), want) {
			t.Fatalf("changed=%v got %v", changed, cmds(steps))
		}
		if !contains(said, "loki → off   (preview.example.com)") ||
			!contains(said, "  1 deployment(s) still carry the loki driver; each drops it on its next deploy, a sleeping one on wake.") {
			t.Errorf("said %v", said)
		}
	})

	t.Run("the mode already in use recreates nothing", func(t *testing.T) {
		// Recreating the control stack is a brief outage; "already off" is an answer, not a reason for one.
		// negative control: drop the `from == to` early return — the Fake records a `pstack init` and changed is true.
		for _, c := range []struct {
			dataDir string
			logging initctl.Logging
			said    string
		}{
			{realControl(t, initctl.Basic, initctl.HTTP01), initctl.LoggingNone, "Logging is already off. Nothing to do."},
			{realControl(t, initctl.Basic, initctl.HTTP01), "", "Logging is already off. Nothing to do."},
			{lokiControl(t, lokiPassword), initctl.Loki, "Logging is already loki. Nothing to do."},
		} {
			f := exec.NewFake(nil, "")
			var said []string
			changed, steps, err := SwitchLogging(SwitchLoggingOptions{DataDir: c.dataDir, Logging: c.logging, Runner: f, Log: func(l string) { said = append(said, l) }})
			if err != nil {
				t.Fatal(err)
			}
			if changed || steps == nil || len(steps) != 0 || len(f.Commands()) != 0 || !contains(said, c.said) {
				t.Errorf("%q: changed=%v steps=%v ran=%v said=%v", c.logging, changed, steps, f.Commands(), said)
			}
		}
	})
}
```

- [ ] **Step 10: Run it and watch it fail**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run TestLoggingSwitch
```

Expected: `internal/upgrade/upgrade_test.go:NN:NN: undefined: SwitchLogging`, `undefined: SwitchLoggingOptions`, then `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade [build failed]`.

- [ ] **Step 11: Implement SwitchLogging, sharing SwitchUI's step loop**

In `SwitchUI`, replace the loop and return (lines 375-387):

```go
	say(string(current.UI) + " UI → " + string(opts.UI) + " UI   (" + state.Domain + ")")
	for _, step := range steps {
		say("  → " + step.Label)
		r := opts.Runner.Run(step.Cmd, exec.RunOptions{Env: step.Env, Label: step.Label})
		if !r.OK && !r.Skipped {
			return false, nil, &Error{step.Label + " failed (exit " + strconv.Itoa(r.Code) + ").\n" + lastLines(firstOf(r.Stderr, r.Stdout), 8)}
		}
		if strings.TrimSpace(r.Stdout) != "" {
			say(exec.Indent(r.Stdout))
		}
	}
	return true, steps, nil
}
```

with the code below. Output and errors are byte-identical, and the `ui-switch-dry-*` goldens in `TestUpgradeGoldens` pin that. `cli.isUpgradeError` (run.go:340) type-asserts `*upgrade.Error`, and the returned `error` still holds one.

```go
	say(string(current.UI) + " UI → " + string(opts.UI) + " UI   (" + state.Domain + ")")
	if err := runSwitch(say, opts.Runner, steps); err != nil {
		return false, nil, err
	}
	return true, steps, nil
}

// runSwitch runs a switch's steps in order and stops at the first failure. SwitchUI and SwitchLogging
// share it so the two cannot drift in what they print or how they fail.
func runSwitch(say func(string), runner exec.Runner, steps []Step) error {
	for _, step := range steps {
		say("  → " + step.Label)
		r := runner.Run(step.Cmd, exec.RunOptions{Env: step.Env, Label: step.Label})
		if !r.OK && !r.Skipped {
			return &Error{step.Label + " failed (exit " + strconv.Itoa(r.Code) + ").\n" + lastLines(firstOf(r.Stderr, r.Stdout), 8)}
		}
		if strings.TrimSpace(r.Stdout) != "" {
			say(exec.Indent(r.Stdout))
		}
	}
	return nil
}

// SwitchLoggingOptions are SwitchLogging's inputs.
type SwitchLoggingOptions struct {
	DataDir string
	Logging initctl.Logging
	Runner  exec.Runner
	// Log receives each progress line (stdout when nil).
	Log func(string)
}

// SwitchLogging turns Loki on or off for a host that exists — `pstack logging loki|off`.
//
// SwitchUI's shape, for SwitchUI's reason: everything init needs is on disk, the token and the push
// password included, and retyping them is how a host stops answering CI or starts refusing its own
// containers' logs. One step: Loki is a pulled image, nothing to build. Returns changed=false and no
// steps for a no-op.
//
// Off does not reach running containers. Their driver is fixed when they are created, so each keeps
// pushing (now a 404, dropped without retry) until it is recreated; the closing line says how many.
func SwitchLogging(opts SwitchLoggingOptions) (changed bool, steps []Step, err error) {
	say := sayer(opts.Log)
	current, err := ReadControlState(opts.DataDir)
	if err != nil {
		return false, nil, err
	}
	from, to := loggingWord(current.Logging), loggingWord(opts.Logging)
	if from == to {
		say("Logging is already " + to + ". Nothing to do.")
		return false, []Step{}, nil
	}

	state := *current
	state.Logging = opts.Logging
	cmd := "pstack init " + initFlags(&state)
	if state.Logging != initctl.Loki {
		// Spelled out. initFlags reproduces a host by leaving defaults off, and initguard refuses an
		// init that drops Loki without --logging typed; this switch is the decision, so it types it.
		cmd += " --logging none"
	}
	steps = []Step{{Label: "re-run init with logging " + to, Cmd: cmd, Env: initEnv(&state)}}
	say(from + " → " + to + "   (" + state.Domain + ")")
	if err := runSwitch(say, opts.Runner, steps); err != nil {
		return false, nil, err
	}
	if state.Logging != initctl.Loki {
		say("  " + strconv.Itoa(LoggedDeployments(opts.DataDir)) + " deployment(s) still carry the loki driver; each drops it on its next deploy, a sleeping one on wake.")
	}
	return true, steps, nil
}

// loggingWord is the command's vocabulary, `loki|off`. "" reads as off, like none.
func loggingWord(l initctl.Logging) string {
	if l == initctl.Loki {
		return "loki"
	}
	return "off"
}
```

`LoggedDeployments` from Step 7 now sits after `loggingWord`, keeping the file's read-then-act order. Moving it is optional; the placement does not affect compilation.

- [ ] **Step 12: Run it and watch it pass**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/upgrade && go vet ./internal/upgrade/ && go test -race -timeout 120s ./internal/upgrade/ ./internal/cli/
```

Expected: `gofmt -l` prints nothing, `go vet` prints nothing, then:

```
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade	<time>
ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/cli	<time>
```

- [ ] **Step 13: Run every negative control (AGENTS.md Go rule 17)**

Apply each mutation to `upgrade.go`, run the named test, confirm it FAILS, and revert before the next one. Use `-run` with the test and subtest name: `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/upgrade/ -run '<Test>/<subtest>'`.
1. Delete the `if lokiSvc.MatchString(compose) { … }` block. `TestUpgrade/a_loki_host_keeps_Loki` fails with `logging = "none"`.
2. Remove `"PSTACK_LOKI_PASSWORD"` from the carried list. `TestUpgrade/the_dry-run_transcript_names_the_push_password` fails.
3. Change the `LokiPassword:` field to `env["LOKI_PUSH_PASSWORD"]` read through `need("LOKI_PUSH_PASSWORD")`: add `lokiPassword, err := need("LOKI_PUSH_PASSWORD"); if err != nil { return nil, err }` before `s :=`. `TestUpgrade/a_logging-off_host_reads_none` fails inside `mustRead`.
4. Replace the `strings.Contains(string(b), `"loki-url"`)` condition with `err == nil`. `TestLoggedDeployments/counts_the_deployments` fails with `n = 2`.
5. Change `return 0 // nothing deployed yet` to `return -1`. `TestLoggedDeployments/a_host_with_no_deployments_directory` fails with `n = -1`.
6. Delete the `if state.Logging == initctl.Loki { flags = append(flags, "--logging loki") }` block in `initFlags`. `TestLoggingSwitch/off_→_loki` fails, and so does `TestUpgrade/a_loki_host_keeps_Loki`.
7. Delete the `cmd += " --logging none"` block. `TestLoggingSwitch/loki_→_off` fails.
8. Delete the `if from == to { … }` early return. `TestLoggingSwitch/the_mode_already_in_use` fails with `changed=true`.

After reverting, re-run Step 12's command. It must be green.

- [ ] **Step 14: Commit**

```
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of `packages/pstack/internal/upgrade/upgrade.go` and `packages/pstack/internal/upgrade/upgrade_test.go` only; never the untracked `golden/host/db/pstack.db-shm`/`-wal`. Then:

```
but commit -b claude/loki-logging -m "feat(upgrade): read Loki back so upgrade and pstack logging keep it" <upgrade.go id> <upgrade_test.go id>
```

---

### Task 5: cli: --logging flag, env, help, completion, initguard, cloud-init passthrough

**Files:**
- Modify: `packages/pstack/internal/cli/args.go:52` (Parsed field), `:138` (default), `:238-244` (parse case), `:345-347` (Usage)
- Modify: `packages/pstack/internal/cli/commands.go:107-122` (init page + flags), `:198-208` (cloud-init page + flags)
- Modify: `packages/pstack/internal/cli/completion.go:47-53` (enumFlags)
- Modify: `packages/pstack/internal/cli/initguard.go:13-14` (header), `:48-53` (doc + signature), `:68-74` (password check goes after it), `:93-99` (logging check goes after it)
- Modify: `packages/pstack/internal/cli/run.go:236-245` (init guard arg + Options.Logging), `:547` (cloud-init Answers.Logging)
- Modify: `packages/pstack/internal/cloudinit/cloudinit.go:206-208` (Answers.Logging), `:383-385` (initFlags)
- Modify: `packages/conformance/golden/cli/help.json`, `packages/conformance/golden/cli/help-h.json`, `packages/conformance/golden/cli/no-args.json` (regenerated, never hand-edited)
- Modify: `docs/usage.md:70`, `:1299-1301`, `:1443-1446`, `:3079`, `:3123`; `docs/bootstrap.md:265-268`, `:331-333`
- Test: `packages/pstack/internal/cli/args_test.go`, `packages/pstack/internal/cli/commands_test.go`, `packages/pstack/internal/cli/initguard_test.go`, `packages/pstack/internal/cloudinit/cloudinit_test.go`

**Interfaces:**
- Consumes:
  - From T2: `initctl.Logging`, `initctl.LoggingNone`, `initctl.Loki`.
  - From T3: `initctl.Options.Logging`.
  - From T4: `upgrade.ControlState.Logging initctl.Logging`, `upgrade.ControlState.LokiPassword string`, `upgrade.SwitchLogging(opts SwitchLoggingOptions) (changed bool, steps []Step, err error)` and `upgrade.SwitchLoggingOptions{DataDir, Logging, Runner, Log}`. T4's off step's Cmd already ends in ` --logging none`. T4 owns that suffix, and this task only verifies it (Step 8).
  - Existing: `initctl.Init(o Options) error`; `upgrade.ReadControlState(dataDir string) (*ControlState, error)` (upgrade.go:97); `upgrade.PlanUpgrade(a PlanArgs) []Step` (upgrade.go:283) with `PlanArgs{Phase, Target, State, BinPath}` (upgrade.go:270-277); `upgrade.Step{Label, Cmd string; Env map[string]string}`; `exec.NewFake(fail func(cmd string) bool, stdout string) *Fake` (exec.go:270); `orNone(s string) string` (initguard.go:102); `initRevertRefusal(reverts []revert) string` (initguard.go:112).
- Produces:
  - `cli.Parsed.Logging string // none | loki`. Its default is `or("PSTACK_LOGGING", "none")`, and `p.Typed["--logging"]` is set when the flag is spelled.
  - `func initReverts(state *upgrade.ControlState, dataDir string, p *Parsed, hasToken, hasDNSToken, hasLokiPassword bool) []revert`
  - `cloudinit.Answers.Logging string`. `"loki"` appends `--logging loki` to the init call, after `--orchestrator`.
  - `enumFlags["--logging"] = {"none", "loki"}`. `--logging` is added to `commandHelps["init"].flags` and `commandHelps["cloud-init"].flags`.

- [ ] **Step 1: Write the failing parse tests**

In `packages/pstack/internal/cli/args_test.go`, `TestEnvDefaultsUseTheRightNullishness`, make two changes (lines 97 and 107). First:

```go
		case "PSTACK_CHALLENGE", "PSTACK_ORCHESTRATOR", "PSTACK_UI":
```
becomes
```go
		case "PSTACK_CHALLENGE", "PSTACK_ORCHESTRATOR", "PSTACK_UI", "PSTACK_LOGGING":
```
Second:
```go
	if p.Challenge != "http01" || p.Orchestrator != "swarm" || p.UI != "basic" || p.Tag != "custom:tag" || p.Domain != "" {
```
becomes
```go
	if p.Challenge != "http01" || p.Orchestrator != "swarm" || p.UI != "basic" || p.Logging != "none" || p.Tag != "custom:tag" || p.Domain != "" {
```

Insert directly before `func TestCloudInitCredentialFlagsTakeNoEnvDefault` (line 112):

```go
func TestLoggingFlag(t *testing.T) {
	// negative control: drop the none|loki check in the `--logging` case — `--logging on` parses,
	// and init renders a host with logging off without saying so.
	p, e := ParseArgs([]string{"init", "--logging", "loki"}, noEnv)
	if e != nil {
		t.Fatal(e)
	}
	if p.Logging != "loki" || !p.Typed["--logging"] {
		t.Errorf("--logging loki: %+v", p)
	}
	def, _ := ParseArgs([]string{"init"}, noEnv)
	if def.Logging != "none" || def.Typed["--logging"] {
		t.Errorf("default: %+v", def)
	}
	if _, e := ParseArgs([]string{"init", "--logging", "on"}, noEnv); e == nil || e.Code != ExitUsage || e.Msg != `--logging must be none or loki, got "on"` {
		t.Errorf("bad --logging: %+v", e)
	}
	// The environment sets the value but is not a SPELLED flag: the guard judges Typed.
	env := func(k string) (string, bool) {
		if k == "PSTACK_LOGGING" {
			return "loki", true
		}
		return "", false
	}
	fromEnv, _ := ParseArgs([]string{"init"}, env)
	if fromEnv.Logging != "loki" || fromEnv.Typed["--logging"] {
		t.Errorf("PSTACK_LOGGING: %+v", fromEnv)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

`cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run 'TestLoggingFlag|TestEnvDefaultsUseTheRightNullishness'`

Expected: the build fails with `p.Logging undefined (type *Parsed has no field or method Logging)`, then `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/cli [build failed]`.

- [ ] **Step 3: Implement the flag**

In `packages/pstack/internal/cli/args.go:52`, replace:
```go
	Orchestrator string // swarm | compose
```
with
```go
	Orchestrator string // swarm | compose
	Logging      string // none | loki
```

In `args.go:138`, replace:
```go
		UI:           or("PSTACK_UI", "basic"),
```
with
```go
		UI:           or("PSTACK_UI", "basic"),
		Logging:      or("PSTACK_LOGGING", "none"),
```

In `args.go:238-244`, replace:
```go
		case "--orchestrator":
			p.Typed["--orchestrator"] = true
			o := next(&i, "")
			if o != "swarm" && o != "compose" {
				return nil, fail(fmt.Sprintf(`--orchestrator must be swarm or compose, got "%s"`, o))
			}
			p.Orchestrator = o
```
with
```go
		case "--orchestrator":
			p.Typed["--orchestrator"] = true
			o := next(&i, "")
			if o != "swarm" && o != "compose" {
				return nil, fail(fmt.Sprintf(`--orchestrator must be swarm or compose, got "%s"`, o))
			}
			p.Orchestrator = o
		case "--logging":
			p.Typed["--logging"] = true
			l := next(&i, "")
			if l != "none" && l != "loki" {
				return nil, fail(fmt.Sprintf(`--logging must be none or loki, got "%s"`, l))
			}
			p.Logging = l
```

- [ ] **Step 4: Run it and watch it pass**

`cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run 'TestLoggingFlag|TestEnvDefaultsUseTheRightNullishness'`

Expected: `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/cli`.

Negative control: comment out the `if l != "none" && l != "loki"` block and re-run. Expect `bad --logging: <nil>`. Restore the block.

- [ ] **Step 5: Write the failing guard tests**

In `packages/pstack/internal/cli/initguard_test.go`, replace the import block (lines 3-10):
```go
import (
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
)
```
with
```go
import (
	"bytes"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
)
```

Make `bareInit()` (line 32) mirror what the parser really produces. Replace:
```go
		Challenge: "http01", UI: "basic", Orchestrator: "swarm",
```
with
```go
		Challenge: "http01", UI: "basic", Orchestrator: "swarm", Logging: "none",
```

Each of the six existing call sites gains a trailing `false`, meaning no `PSTACK_LOKI_PASSWORD`:
- line 41: `initReverts(richHost(), "/var/lib/pstack", bareInit(), false, false)` → `initReverts(richHost(), "/var/lib/pstack", bareInit(), false, false, false)`
- line 77: `initReverts(richHost(), "/var/lib/pstack", p, true, true)` → `initReverts(richHost(), "/var/lib/pstack", p, true, true, false)`
- line 89: `initReverts(nil, "/var/lib/pstack", bareInit(), false, false)` → `initReverts(nil, "/var/lib/pstack", bareInit(), false, false, false)`
- line 97: `initReverts(same, "/var/lib/pstack", bareInit(), true, false)` → `initReverts(same, "/var/lib/pstack", bareInit(), true, false, false)`
- line 107: `initReverts(host, "/var/lib/pstack", bareInit(), true, false)` → `initReverts(host, "/var/lib/pstack", bareInit(), true, false, false)`
- line 110: `initReverts(host, "/var/lib/pstack", bareInit(), false, false)` → `initReverts(host, "/var/lib/pstack", bareInit(), false, false, false)`

`richHost()` has no `Logging`, so `TestABareReInitIsRefusedForEveryThingItWouldSilentlyChange` still counts 5.

Append to the end of the file:

```go
func TestALokiHostRefusesDroppingLokiOrItsPassword(t *testing.T) {
	// A Loki host with nothing else to lose: the token env is set and every other axis matches the
	// defaults, so each count below is the Loki checks alone.
	host := func() *upgrade.ControlState {
		return &upgrade.ControlState{
			Token: "t", Challenge: initctl.Challenge("http01"), UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm"),
			Logging: initctl.Loki, LokiPassword: "0123456789abcdef0123456789abcdef",
		}
	}

	t.Run("a bare re-run would remove Loki", func(t *testing.T) {
		// negative control: delete the `state.Logging == initctl.Loki` check — no revert, and the
		// re-run removes Loki from under every running container's pushes.
		got := initReverts(host(), "/var/lib/pstack", bareInit(), true, false, false)
		if len(got) != 1 {
			t.Fatalf("expected the Loki logging revert alone, got %d: %+v", len(got), got)
		}
		msg := initRevertRefusal(got)
		for _, want := range []string{"Loki logging", "loki  →  none", "keep it with:  --logging loki"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal must name %q:\n%s", want, msg)
			}
		}
	})

	t.Run("keeping Loki without its password would mint a new one", func(t *testing.T) {
		// negative control: drop `!hasLokiPassword` from the password check — the run that carries
		// the password is refused too, and `pstack upgrade` on a Loki host stops working.
		p := bareInit()
		p.Logging, p.Typed["--logging"] = "loki", true
		got := initReverts(host(), "/var/lib/pstack", p, true, false, false)
		if len(got) != 1 || got[0].what != "the Loki push password" || !strings.Contains(got[0].fix, `echo "$LOKI_PUSH_PASSWORD"`) {
			t.Fatalf("expected the push password revert alone: %+v", got)
		}
		if got := initReverts(host(), "/var/lib/pstack", p, true, false, true); len(got) != 0 {
			t.Errorf("a SET password is the caller's choice: %+v", got)
		}
	})

	t.Run("a spelled --logging none is a decision", func(t *testing.T) {
		// negative control: drop `!typed("--logging")` — turning Loki off on purpose is refused, and
		// so is the init line `pstack logging off` runs.
		p := bareInit()
		p.Typed["--logging"] = true
		if got := initReverts(host(), "/var/lib/pstack", p, true, false, false); len(got) != 0 {
			t.Errorf("--logging none was asked for: %+v", got)
		}
	})
}

// The init lines `pstack upgrade` and `pstack logging off` run are guarded too — the child `pstack
// init` reads the same host — so a line that trips the guard breaks the command that wrote it,
// and no dry-run golden would show it. The host is generated by the REAL init, with docker faked.
func TestTheHostsOwnInitLinesPassTheGuard(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"
	t.Setenv("PSTACK_LOKI_PASSWORD", pw)
	dataDir := t.TempDir()
	if err := initctl.Init(initctl.Options{
		DataDir: dataDir, Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: initctl.HTTP01, UI: initctl.Basic, Orchestrator: spec.Compose, Logging: initctl.Loki,
		Runner: exec.NewFake(nil, "healthy"), Out: &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}
	state, err := upgrade.ReadControlState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Logging != initctl.Loki || state.LokiPassword != pw {
		t.Fatalf("the fixture is not a Loki host: %+v", state)
	}
	// passes parses a step's command the way the child would and runs the guard against this host.
	// Shq-quoted values keep their quotes under strings.Fields; the guard never reads them.
	passes := func(t *testing.T, step upgrade.Step) {
		t.Helper()
		words := strings.Fields(step.Cmd)
		if len(words) < 2 || words[0] != "pstack" || words[1] != "init" {
			t.Fatalf("not an init line: %q", step.Cmd)
		}
		p, ex := ParseArgs(words[1:], func(k string) (string, bool) { v, ok := step.Env[k]; return v, ok })
		if ex != nil {
			t.Fatalf("%q does not parse: %v", step.Cmd, ex)
		}
		_, hasToken := step.Env["PSTACK_TOKEN"]
		_, hasDNSToken := step.Env["PSTACK_DNS_TOKEN"]
		_, hasLokiPassword := step.Env["PSTACK_LOKI_PASSWORD"]
		if got := initReverts(state, dataDir, p, hasToken, hasDNSToken, hasLokiPassword); len(got) != 0 {
			t.Errorf("%q is refused on its own host:\n%s", step.Cmd, initRevertRefusal(got))
		}
	}

	t.Run("pstack upgrade", func(t *testing.T) {
		// negative control: drop the PSTACK_LOKI_PASSWORD line from upgrade's initEnv — the line is
		// refused as "the Loki push password".
		steps := upgrade.PlanUpgrade(upgrade.PlanArgs{Phase: upgrade.Resume, Target: "0.0.0", State: state})
		passes(t, steps[len(steps)-1])
	})

	t.Run("pstack logging off", func(t *testing.T) {
		// negative control: drop ` --logging none` from T4's SwitchLogging off step — the line is
		// refused as "Loki logging".
		changed, steps, err := upgrade.SwitchLogging(upgrade.SwitchLoggingOptions{
			DataDir: dataDir, Logging: initctl.LoggingNone, Runner: exec.NewFake(nil, "healthy"), Log: func(string) {},
		})
		if err != nil || !changed || len(steps) != 1 {
			t.Fatalf("changed=%v steps=%+v err=%v", changed, steps, err)
		}
		passes(t, steps[0])
	})
}
```

- [ ] **Step 6: Run it and watch it fail**

`cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run 'TestALokiHost|TestTheHostsOwnInitLines|TestABareReInit|TestWhatYouSpelled|TestTheGuardIsSilent|TestTheTokenIsJudged'`

Expected: the build fails with `too many arguments in call to initReverts`, `have (*upgrade.ControlState, string, *Parsed, bool, bool, bool)`, `want (*upgrade.ControlState, string, *Parsed, bool, bool)`.

- [ ] **Step 7: Implement the guard and the init passthrough**

In `packages/pstack/internal/cli/initguard.go:13-14`, replace:
```go
//	· a run without `--challenge dns01` flips a wildcard host to per-hostname issuance, which burns
//	  the weekly rate limit and cannot be undone by re-running.
```
with
```go
//	· a run without `--challenge dns01` flips a wildcard host to per-hostname issuance, which burns
//	  the weekly rate limit and cannot be undone by re-running.
//	· a run without `--logging loki` on a Loki host removes Loki; one without PSTACK_LOKI_PASSWORD
//	  mints a new push password, and every running container's pushes are refused until redeployed.
```

In `initguard.go:51-53`, replace:
```go
// `hasToken`/`hasDNSToken` are whether the two environment variables were SET (presence, not
// emptiness — an explicitly empty one is a choice).
func initReverts(state *upgrade.ControlState, dataDir string, p *Parsed, hasToken, hasDNSToken bool) []revert {
```
with
```go
// `hasToken`/`hasDNSToken`/`hasLokiPassword` are whether the three environment variables were SET
// (presence, not emptiness — an explicitly empty one is a choice).
func initReverts(state *upgrade.ControlState, dataDir string, p *Parsed, hasToken, hasDNSToken, hasLokiPassword bool) []revert {
```

In `initguard.go:68-74`, replace:
```go
	// The DNS-01 credential, same shape. Only meaningful on a host that uses one.
	if !hasDNSToken && state.DNSToken != "" {
		out = append(out, revert{
			what: "the DNS-01 credential", from: "the one this host has", to: "EMPTY — renewals would fail later, silently",
			fix: "PSTACK_DNS_TOKEN=<the token>",
		})
	}
```
with
```go
	// The DNS-01 credential, same shape. Only meaningful on a host that uses one.
	if !hasDNSToken && state.DNSToken != "" {
		out = append(out, revert{
			what: "the DNS-01 credential", from: "the one this host has", to: "EMPTY — renewals would fail later, silently",
			fix: "PSTACK_DNS_TOKEN=<the token>",
		})
	}
	// The Loki push password, same shape — only when this run KEEPS Loki. A run that drops Loki is
	// the check at the bottom, and it needs no password.
	if !hasLokiPassword && state.LokiPassword != "" && initctl.Logging(p.Logging) == initctl.Loki {
		out = append(out, revert{
			what: "the Loki push password", from: "the one this host has",
			to:  "a NEWLY GENERATED one — running containers' pushes are refused until redeployed",
			fix: "PSTACK_LOKI_PASSWORD=$(. " + dataDir + "/control/.env; echo \"$LOKI_PUSH_PASSWORD\")",
		})
	}
```

In `initguard.go:93-99`, replace:
```go
	if !typed("--orchestrator") && string(state.Orchestrator) != "" && spec.Orchestrator(p.Orchestrator) != state.Orchestrator {
		out = append(out, revert{
			what: "the orchestrator", from: string(state.Orchestrator), to: p.Orchestrator,
			fix: "--orchestrator " + string(state.Orchestrator),
		})
	}
	return out
```
with
```go
	if !typed("--orchestrator") && string(state.Orchestrator) != "" && spec.Orchestrator(p.Orchestrator) != state.Orchestrator {
		out = append(out, revert{
			what: "the orchestrator", from: string(state.Orchestrator), to: p.Orchestrator,
			fix: "--orchestrator " + string(state.Orchestrator),
		})
	}
	// Only a host that HAS Loki can lose it, so a host without one never trips this.
	if !typed("--logging") && state.Logging == initctl.Loki && initctl.Logging(p.Logging) != initctl.Loki {
		out = append(out, revert{
			what: "Loki logging", from: "loki", to: orNone(p.Logging),
			fix: "--logging loki",
		})
	}
	return out
```

In `packages/pstack/internal/cli/run.go:236-245`, replace:
```go
				_, hasToken := io.Env("PSTACK_TOKEN")
				if msg := initRevertRefusal(initReverts(state, dataDir, args, hasToken, hasDNSToken)); msg != "" {
					return fail(msg)
				}
			}
		}
		err := initctl.Init(initctl.Options{
			DataDir: dataDir, Domain: args.Domain, AcmeEmail: args.AcmeEmail, DNSProvider: args.DNSProvider,
			Challenge: initctl.Challenge(args.Challenge), UI: initctl.UI(args.UI), Orchestrator: spec.Orchestrator(args.Orchestrator),
			Token: token, DryRun: args.DryRun, Runner: runner, Out: out,
		})
```
with
```go
				_, hasToken := io.Env("PSTACK_TOKEN")
				_, hasLokiPassword := io.Env("PSTACK_LOKI_PASSWORD")
				if msg := initRevertRefusal(initReverts(state, dataDir, args, hasToken, hasDNSToken, hasLokiPassword)); msg != "" {
					return fail(msg)
				}
			}
		}
		err := initctl.Init(initctl.Options{
			DataDir: dataDir, Domain: args.Domain, AcmeEmail: args.AcmeEmail, DNSProvider: args.DNSProvider,
			Challenge: initctl.Challenge(args.Challenge), UI: initctl.UI(args.UI), Orchestrator: spec.Orchestrator(args.Orchestrator),
			Logging: initctl.Logging(args.Logging), Token: token, DryRun: args.DryRun, Runner: runner, Out: out,
		})
```

Run:

`cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run 'TestALokiHost|TestTheHostsOwnInitLines|TestABareReInit|TestWhatYouSpelled|TestTheGuardIsSilent|TestTheTokenIsJudged'`

Expected: `ok  github.com/samishal1998/preview-stacks/packages/pstack/internal/cli`. Every test passes, including `TestTheHostsOwnInitLinesPassTheGuard/pstack_logging_off`. T4 is already in the tree, and its `SwitchLogging` off step spells ` --logging none`, so the child init line clears the new `Loki logging` check.

If `pstack_logging_off` fails, the output reads `"pstack init --domain … --orchestrator compose" is refused on its own host:` followed by `Loki logging`. That means T4 did not land its suffix. Stop and fix it in T4. Do not add it here.

Run each negative control below, one at a time. After each, re-run the same command, see the named failure, and restore the code:
- Delete the `state.Logging == initctl.Loki` block in initguard.go. `a_bare_re-run_would_remove_Loki` fails with `got 0`.
- Replace `!hasLokiPassword &&` with `true &&` in initguard.go. `keeping_Loki_without_its_password_would_mint_a_new_one` fails with `a SET password is the caller's choice`.
- Replace `!typed("--logging") &&` with `true &&` in initguard.go. `a_spelled_--logging_none_is_a_decision` fails with `--logging none was asked for`, and `pstack_logging_off` fails naming `Loki logging`.
- Temporarily delete the ` --logging none` string from T4's `SwitchLogging` in `packages/pstack/internal/upgrade/upgrade.go` (Step 8's grep finds the line). `pstack_logging_off` fails with `is refused on its own host:` and `Loki logging`. Restore it byte for byte. T5 leaves that file unchanged.
- Temporarily delete the `PSTACK_LOKI_PASSWORD` line from T4's `initEnv` in upgrade.go. `pstack_upgrade` fails naming `the Loki push password`. Restore it.

- [ ] **Step 8: Verify T4's off step spells its choice (no edit)**

`grep -n '"--logging none"\| --logging none' /Volumes/S1/code/preview-stacks/packages/pstack/internal/upgrade/upgrade.go`

Expected: at least one line inside `SwitchLogging` that appends ` --logging none` to the off step's `pstack init` command, for example `+ " --logging none"`. No output means T4 is incomplete, so stop and do not edit upgrade.go in this task.

Then run `cd /Volumes/S1/code/preview-stacks && but diff` and confirm it lists no file under `packages/pstack/internal/upgrade/`. That shows the Step 7 negative controls were restored.

- [ ] **Step 9: Write the failing cloud-init passthrough test**

In `packages/pstack/internal/cloudinit/cloudinit_test.go`, insert directly before the `t.Run("no config repo drops the clone line rather than emitting an empty one"` subtest (line 216):

```go
	t.Run("--logging loki reaches the init call; none adds nothing", func(t *testing.T) {
		// negative control: drop the Logging branch of initFlags — the init call has no --logging loki.
		a := base
		a.Logging = "loki"
		out := render(t, a)
		if !strings.Contains(initCall(t, out), "--logging loki") {
			t.Errorf("--logging loki missing from the init call: %q", initCall(t, out))
		}
		if !yamlOK(t, out) {
			t.Error("not valid YAML")
		}
		// The manager needs no step of its own: init installs the plugin. And off renders nothing,
		// which TestCloudInitGoldens proves byte for byte.
		for _, off := range []string{"", "none"} {
			a.Logging = off
			if strings.Contains(initCall(t, render(t, a)), "--logging") {
				t.Errorf("Logging %q carries --logging", off)
			}
		}
	})
```

Run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cloudinit/ -run TestCloudInitGeneration`

Expected: the build fails with `a.Logging undefined (type Answers has no field or method Logging)`.

- [ ] **Step 10: Implement the passthrough**

In `packages/pstack/internal/cloudinit/cloudinit.go:206-208`, replace:
```go
	// Orchestrator is `swarm` (the default for a new host) or `compose`; passed to
	// `pstack init --orchestrator` only when set.
	Orchestrator string
}
```
with
```go
	// Orchestrator is `swarm` (the default for a new host) or `compose`; passed to
	// `pstack init --orchestrator` only when set.
	Orchestrator string
	// Logging is "none" or "loki"; loki adds `--logging loki` to the init call. No step of its own:
	// init installs the plugin on the manager.
	Logging string
}
```

In `cloudinit.go:383-385`, replace:
```go
	if a.Orchestrator != "" {
		initFlags = append(initFlags, "--orchestrator "+a.Orchestrator)
	}
```
with
```go
	if a.Orchestrator != "" {
		initFlags = append(initFlags, "--orchestrator "+a.Orchestrator)
	}
	if a.Logging == "loki" {
		initFlags = append(initFlags, "--logging loki")
	}
```

In `packages/pstack/internal/cli/run.go:547`, replace:
```go
		Challenge: args.Challenge, DNSProvider: args.DNSProvider, UI: args.UI, Orchestrator: args.Orchestrator,
```
with
```go
		Challenge: args.Challenge, DNSProvider: args.DNSProvider, UI: args.UI, Orchestrator: args.Orchestrator, Logging: args.Logging,
```

Run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cloudinit/`

Expected: `ok`, with `TestCloudInitGoldens` passing unchanged.

Negative control: delete the `a.Logging == "loki"` branch and re-run. Expect `--logging loki missing from the init call`. Restore the branch.

- [ ] **Step 11: Write the failing help/completion test**

In `packages/pstack/internal/cli/commands_test.go`, `TestTheShellIsOfferedTheFlagsTheHelpDescribes`, replace line 47:
```go
		{"build-image", "--tag"},
```
with
```go
		{"build-image", "--tag"},
		{"init", "--logging"},
		{"cloud-init", "--logging"},
```

Run `cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run TestTheShellIsOfferedTheFlagsTheHelpDescribes`

Expected: `init should complete --logging; got [...]`, `init completes --logging but its help never mentions it`, and the same two lines for `cloud-init`.

- [ ] **Step 12: Implement help, Usage and completion**

In `packages/pstack/internal/cli/commands.go`, on the init page, replace lines 107-108:
```go
      --ui basic|advanced        default basic (embedded, no extra container)
      --orchestrator swarm|compose
```
with
```go
      --ui basic|advanced        default basic (embedded, no extra container)
      --orchestrator swarm|compose
      --logging none|loki        default none. loki: Loki here, every deploy's logs shipped to it
```
Replace lines 118-119:
```go
Also reads PSTACK_TOKEN: absent on an existing host, a NEW machine token is minted and every CI
job holding the old one starts getting 401s.`,
```
with
```go
Also reads PSTACK_TOKEN: absent on an existing host, a NEW machine token is minted and every CI
job holding the old one starts getting 401s. PSTACK_LOKI_PASSWORD likewise, for Loki's pushes.`,
```
Replace line 122:
```go
			"--extra-domain", "--ui", "--orchestrator", "--force", "--dry-run", "--help",
```
with
```go
			"--extra-domain", "--ui", "--orchestrator", "--logging", "--force", "--dry-run", "--help",
```

On the cloud-init page, replace line 198:
```go
      --config-repo <git-url>   cloned to /opt/preview/config
```
with
```go
      --config-repo <git-url>   cloned to /opt/preview/config
      --logging none|loki       default none. loki: passed to init
```
Replace line 208:
```go
			"--ui", "--orchestrator", "--config", "--config-url", "--config-repo", "--help",
```
with
```go
			"--ui", "--orchestrator", "--logging", "--config", "--config-url", "--config-repo", "--help",
```

In `packages/pstack/internal/cli/completion.go:51`, replace:
```go
	"--orchestrator": {"swarm", "compose"},
```
with
```go
	"--orchestrator": {"swarm", "compose"},
	"--logging":      {"none", "loki"},
```

In `packages/pstack/internal/cli/args.go:345-347`, replace:
```go
		"            --orchestrator swarm|compose    (default swarm — one manager; workers join from the Swarm page)",
		"",
		"cloud-init: --domain --acme-email [--distro ubuntu|debian|fedora|suse|arch|alpine] [--ssh-key] [--password] [--challenge] [--ui] [--orchestrator]",
```
with the lines below. There are 13 spaces after `--logging none|loki`, so the parenthesis lines up at column 45 like the line above it:
```go
		"            --orchestrator swarm|compose    (default swarm — one manager; workers join from the Swarm page)",
		"            --logging none|loki             (default none — loki: Loki in the control stack, its plugin on every node)",
		"",
		"cloud-init: --domain --acme-email [--distro ubuntu|debian|fedora|suse|arch|alpine] [--ssh-key] [--password] [--challenge] [--ui] [--orchestrator] [--logging]",
```

- [ ] **Step 13: Run the cli tests and watch only the usage golden fail**

`cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/`

Expected: `TestTheShellIsOfferedTheFlagsTheHelpDescribes` and `TestTheGeneratedScriptsParse` pass. `TestUsageMatchesTheGolden` fails with `--help differs from golden/cli/help.json:`, a line number, ` want `, then ` got              --logging none|loki             (default none — …`. The golden is out of date on purpose.

- [ ] **Step 14: Regenerate the help goldens deliberately**

```
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts
```
Expected: the output ends with `wrote <N> goldens`.

Then run `cd /Volumes/S1/code/preview-stacks && but diff`. Among goldens, exactly `packages/conformance/golden/cli/help.json`, `help-h.json` and `no-args.json` changed, each only by the `--logging` line and the ` [--logging]` suffix. Every `cloud-init-*`, `init-*`, `upgrade-plan-*`, `ui-switch-dry-*` and `swarm-join-*` file is unchanged, and so is everything under `golden/render/control/**`. If anything else shows up, stop: logging off is no longer byte-identical.

Then:
```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ ./internal/cloudinit/
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
cd /Volumes/S1/code/preview-stacks/packages/pstack && gofmt -l internal/cli internal/cloudinit
```
Expected: `ok` for both Go packages, `0 fail` from bun, and no output from gofmt.

- [ ] **Step 15: Docs**

In `docs/usage.md`, §1 help sample, insert after line 70 (`            --dns-provider <lego-code>      (dns01 only; token via PSTACK_DNS_TOKEN)`):
```
            --logging none|loki             (default none — loki: Loki in the control stack, its plugin on every node)
```

In `docs/usage.md`, init table, insert after line 1299 (the `--orchestrator swarm\|compose` row):
```
| `--logging none\|loki` | `PSTACK_LOGGING` | `none` | `loki`: a Loki service in the control stack, its log plugin on this node, and a `logging:` block on every deployed service that has none. Anything else exits 3. `upgrade` keeps whatever the host has |
```
Insert after line 1301 (the `PSTACK_TOKEN` *generated* row):
```
| — | `PSTACK_LOKI_PASSWORD` | *generated* | with `--logging loki`: the push password (32 lowercase hex), kept in `control/.env` as `LOKI_PUSH_PASSWORD`. Supply it to keep the host's |
```

In `docs/usage.md`, silent-revert guard, replace lines 1443-1446:
```
It refuses **omissions only**. A flag you spelled is a decision and passes through — `--challenge
dns01` on an http01 host is still how you switch modes — a first `init` has nothing to compare
against, and `pstack upgrade` supplies every value it read back, so it never trips. `--force`
proceeds anyway.
```
with
```
It refuses **omissions only**. A flag you spelled is a decision and passes through — `--challenge
dns01` on an http01 host is still how you switch modes — a first `init` has nothing to compare
against, and `pstack upgrade` supplies every value it read back, so it never trips. `--force`
proceeds anyway.

On a Loki host it refuses two more:

- **Loki logging** — a run without `--logging loki` removes Loki. Keep it with `--logging loki`;
  `--logging none` turns it off on purpose.
- **The Loki push password** — a run that keeps Loki without `PSTACK_LOKI_PASSWORD` mints a new one,
  and running containers' pushes are refused until each stack is redeployed. Keep it with
  `PSTACK_LOKI_PASSWORD=$(. /var/lib/pstack/control/.env; echo "$LOKI_PUSH_PASSWORD")`.
```

In `docs/usage.md`, flag reference, insert after line 3079 (the `--orchestrator swarm\|compose` | `init` `cloud-init` `upgrade` row):
```
| `--logging none\|loki` | `init` `cloud-init` | default **`none`** (or `PSTACK_LOGGING`). `loki` runs Loki in the control stack and ships every deployed service's logs to it; `cloud-init` passes it to `init`. Any other value exits 3. |
```

In `docs/usage.md`, environment reference, insert after line 3123 (the `PSTACK_ORCHESTRATOR` row):
```
| `PSTACK_LOGGING` | `init` `cloud-init` | `none` | same as `--logging` |
| `PSTACK_LOKI_PASSWORD` | `init` | *generated* | the Loki push password under `--logging loki`; written to `control/.env` as `LOKI_PUSH_PASSWORD`. **Flag-less on purpose.** |
```

In `docs/bootstrap.md`, §3 init table, insert after line 265 (the `--orchestrator swarm\|compose` row):
```
| `--logging none\|loki` | `PSTACK_LOGGING` | **`none`**. `upgrade` re-passes what the host runs |
```
Insert after line 268 (`| *(no flag)* | `PSTACK_TOKEN` | generated, printed once |`):
```
| *(no flag)* | `PSTACK_LOKI_PASSWORD` | generated with `--logging loki`, kept in `control/.env` |
```

In `docs/bootstrap.md`, §4, insert after line 333 (`an account it does not.`) a blank line, then:
```
`--logging loki` passes through to the file's `init` call, which puts Loki in the control stack and
installs the log plugin on the manager. The file needs no step of its own.
```

- [ ] **Step 16: Commit**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ ./internal/cloudinit/
cd /Volumes/S1/code/preview-stacks && but status
```
Take the IDs for exactly these files:
- `packages/pstack/internal/cli/args.go`, `args_test.go`, `commands.go`, `commands_test.go`, `completion.go`, `initguard.go`, `initguard_test.go`, `run.go`
- `packages/pstack/internal/cloudinit/cloudinit.go`, `cloudinit_test.go`
- `packages/conformance/golden/cli/help.json`, `help-h.json`, `no-args.json`
- `docs/usage.md`, `docs/bootstrap.md`

Leave these out:
- `packages/pstack/internal/upgrade/**` (T4's, and unchanged here)
- `packages/conformance/golden/host/**`
- the untracked `pstack.db-shm` and `pstack.db-wal`
- `packages/pstack/bin/pstack`

`but commit -b claude/loki-logging -m "feat(cli): --logging none|loki on init and cloud-init" <ids>`

---

### Task 6: cli: pstack logging loki|off

**Files:**
- Modify: `packages/pstack/internal/cli/args.go:288` (Commands), `:315` (Usage line), `:364-368` (the `ui:` block, followed by a new `logging:` block)
- Modify: `packages/pstack/internal/cli/commands.go:138-145` (new `"logging"` entry after `"ui"`)
- Modify: `packages/pstack/internal/cli/completion.go:38-44` (subcommands)
- Modify: `packages/pstack/internal/cli/run.go:270-290` (new `case "logging"` after `case "ui"`)
- Modify: `docs/usage.md:1448` (new subsection before `### Why \`init\` is CLI-only, and always will be`)
- Modify (regenerated): `packages/conformance/golden/cli/help.json`, `help-h.json`, `no-args.json`, `unknown-command.json`
- Test: `packages/pstack/internal/cli/commands_test.go`

The line numbers are from the current tree. T3 and T5 edit args.go, commands.go, run.go and usage.md before this task and shift them by a few lines, so every edit below is anchored on quoted text.

**Interfaces:**
- Consumes (T4): `package upgrade; type SwitchLoggingOptions struct { DataDir string; Logging initctl.Logging; Runner exec.Runner; Log func(string) }` and `func SwitchLogging(opts SwitchLoggingOptions) (changed bool, steps []Step, err error)`. SwitchLogging prints the no-op line, the plan and (for off) the count line itself. It returns `*upgrade.Error` for refusals.
- Consumes (T2): `initctl.Logging`, `initctl.LoggingNone`, `initctl.Loki`.
- Consumes existing code: `fail` (args.go:35), `ExitUsage`/`ExitFailed` (args.go:20-22), `isUpgradeError` (run.go:340), `registry.DataDir()` (registry.go:474, reads `PSTACK_DATA` from the process env), `globalFlags` (commands.go:37).
- Produces: `Commands` gains `"logging"` after `"ui"`. `Usage()` lists it and gets a `logging:` block. `commandHelps["logging"]` exists. `subcommands["logging"] = {"loki", "off"}`. run.go gets `case "logging"`. No Go API that later tasks call; T13's `logging-*-dry-*` goldens run this command.

- [ ] **Step 1: Write the failing test**

In `packages/pstack/internal/cli/commands_test.go`, add `"bytes"` to the imports (lines 3-9 become):

```go
import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)
```

Append at the end of the file, after `TestExtraDomainIsRepeatable`:

```go
func TestLoggingCommandRefusesAnythingButLokiOrOff(t *testing.T) {
	// negative control: accept `none` as a sub (`case "off", "none":` in run.go) — `logging none`
	// reaches upgrade.SwitchLogging, which answers "no control stack found at …" instead of the
	// usage line, and the stderr comparison fails.
	//
	// `none` is the one worth refusing by name: it is the FLAG's word for off (--logging none), so it
	// is the word someone types. PSTACK_DATA points at an empty directory, so a command that got past
	// the refusal finds no control stack to re-run init on, whatever this machine holds at /var/lib/pstack.
	data := t.TempDir()
	t.Setenv("PSTACK_DATA", data)
	for _, argv := range [][]string{{"logging", "none"}, {"logging"}} {
		var out, errOut bytes.Buffer
		code := Run(argv, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, Env: noEnv})
		if code != ExitUsage || errOut.String() != "pstack: usage: pstack logging <loki|off>\n" {
			t.Errorf("pstack %s: exit %d, stderr %q; want exit 3 and the usage line", strings.Join(argv, " "), code, errOut.String())
		}
		// A switch prints its plan before its first step, so empty stdout means nothing was planned or run.
		if out.Len() != 0 {
			t.Errorf("pstack %s printed %q; a refusal runs nothing", strings.Join(argv, " "), out.String())
		}
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("a refused switch wrote %d entries into the data dir", len(entries))
	}
}
```

`noEnv` is at args_test.go:42, same package. `TestEveryCommandHasItsOwnHelp` (commands_test.go:11) walks `Commands`, so it starts covering `logging` as soon as the command is added. Nothing to write for it.

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run TestLoggingCommandRefusesAnythingButLokiOrOff
```

Expected: FAIL. `logging` is not in `Commands`, so `run` returns `UnknownCommand`. That is also exit 3, but with a different message:

```
--- FAIL: TestLoggingCommandRefusesAnythingButLokiOrOff
    commands_test.go:…: pstack logging none: exit 3, stderr "pstack: unknown command \"logging\". This build is pstack … — if you expected a newer command, …"; want exit 3 and the usage line
    commands_test.go:…: pstack logging: exit 3, stderr "pstack: unknown command \"logging\". …"; want exit 3 and the usage line
FAIL
```

- [ ] **Step 3: Implement — args.go (Commands, Usage)**

`packages/pstack/internal/cli/args.go:288`, replace:

```go
var Commands = append(append([]string{}, SpecCommands...), "init", "serve", "build-image", "cloud-init", "dockerfile", "upgrade", "ui", "swarm", "pull", "push", "healthcheck", "api", "completion")
```

with:

```go
var Commands = append(append([]string{}, SpecCommands...), "init", "serve", "build-image", "cloud-init", "dockerfile", "upgrade", "ui", "logging", "swarm", "pull", "push", "healthcheck", "api", "completion")
```

`args.go:315`, replace:

```go
		"Usage: pstack <up|down|verify|status|validate|cloud-init|dockerfile|build-image|init|upgrade|ui|swarm|pull|push|serve|api|completion> [flags]",
```

with:

```go
		"Usage: pstack <up|down|verify|status|validate|cloud-init|dockerfile|build-image|init|upgrade|ui|logging|swarm|pull|push|serve|api|completion> [flags]",
```

`args.go:364-368`, replace:

```go
		"ui:         pstack ui <basic|advanced>   switch which UI control.<domain> serves.",
		"            Reuses the stored token and domain; builds the SPA image when switching to",
		"            advanced. No version change — that is `upgrade`.",
		"",
		"swarm:      pstack swarm [status]            the nodes previews run on (exit 1 if this is not a manager)",
```

with:

```go
		"ui:         pstack ui <basic|advanced>   switch which UI control.<domain> serves.",
		"            Reuses the stored token and domain; builds the SPA image when switching to",
		"            advanced. No version change — that is `upgrade`.",
		"",
		"logging:    pstack logging <loki|off>    ship every deployment's logs to Loki on this host, or stop.",
		"            Reuses the stored token and domain. A deployment switches on its next deploy.",
		"",
		"swarm:      pstack swarm [status]            the nodes previews run on (exit 1 if this is not a manager)",
```

The description starts at column 41, the same as `ui:`'s. `pstack logging <loki|off>` is 25 characters plus 4 spaces; `pstack ui <basic|advanced>` is 26 plus 3.

- [ ] **Step 4: Implement — commands.go (help page)**

`packages/pstack/internal/cli/commands.go:138-146`, replace:

```go
	"ui": {
		summary: "switch which UI control.<domain> serves",
		body: `pstack ui <basic|advanced>

Reuses the stored token and domain, and builds the SPA image when switching to advanced. No
version change — that is upgrade.`,
		flags: globalFlags,
	},
	"serve": {
```

with:

```go
	"ui": {
		summary: "switch which UI control.<domain> serves",
		body: `pstack ui <basic|advanced>

Reuses the stored token and domain, and builds the SPA image when switching to advanced. No
version change — that is upgrade.`,
		flags: globalFlags,
	},
	"logging": {
		summary: "turn Loki logging on or off for this host",
		body: `pstack logging <loki|off> [-n]

loki   Loki in the control stack, its log plugin on this node, and the loki log driver on every
       deployed service without a logging: of its own
off    Loki removed; the plugin stays. Each deployment drops the driver on its next deploy

Reuses the stored token and domain. A worker gets the plugin from pstack swarm join --format
script or cloud-config.

  -n, --dry-run   print the plan and change nothing`,
		flags: globalFlags,
	},
	"serve": {
```

The summary contains no `<` and no `:`, so zsh's `zshQuote` path (completion.go:131-137) is safe. `TestTheGeneratedScriptsParse` still checks this.

- [ ] **Step 5: Implement — completion.go (subcommands)**

`packages/pstack/internal/cli/completion.go:38-44`, replace:

```go
var subcommands = map[string][]string{
	"ui":         {"basic", "advanced"},
	"swarm":      {"status", "join"},
	"pull":       {"config"},
	"push":       {"config"},
	"completion": Shells,
}
```

with:

```go
var subcommands = map[string][]string{
	"ui":         {"basic", "advanced"},
	"logging":    {"loki", "off"},
	"swarm":      {"status", "join"},
	"pull":       {"config"},
	"push":       {"config"},
	"completion": Shells,
}
```

The scripts read this map by key through `sortedCommands()`, never by ranging over it, so the output stays deterministic.

- [ ] **Step 6: Implement — run.go (dispatch)**

In `packages/pstack/internal/cli/run.go`, the `case "ui":` block ends (current lines 286-292) with:

```go
		if changed && !args.DryRun {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Now serving the "+mode+" UI on control.<domain>. The token and domain are unchanged.")
		}
		return nil

	case "swarm":
		return swarmCmd(args, runner, io)
```

Replace it with:

```go
		if changed && !args.DryRun {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Now serving the "+mode+" UI on control.<domain>. The token and domain are unchanged.")
		}
		return nil

	case "logging":
		// Same shape as `ui`: re-runs init from what is on disk. The command's words are loki|off, not
		// the flag's none|loki — `none` is refused, not guessed.
		var target initctl.Logging
		switch args.Sub {
		case "loki":
			target = initctl.Loki
		case "off":
			target = initctl.LoggingNone
		default:
			return fail("usage: pstack logging <loki|off>")
		}
		changed, _, err := upgrade.SwitchLogging(upgrade.SwitchLoggingOptions{DataDir: registry.DataDir(), Logging: target, Runner: runner, Log: func(l string) { fmt.Fprintln(out, l) }})
		if err != nil {
			if isUpgradeError(err) {
				return fail(err.Error())
			}
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		if changed && !args.DryRun {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Logging is now "+args.Sub+".")
		}
		return nil

	case "swarm":
		return swarmCmd(args, runner, io)
```

`initctl`, `upgrade` and `registry` are already imported (run.go:27, :31, :36). No import changes.

- [ ] **Step 7: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run 'TestLoggingCommandRefusesAnythingButLokiOrOff|TestEveryCommandHasItsOwnHelp|TestTheShellIsOfferedTheFlagsTheHelpDescribes|TestTheGeneratedScriptsParse'
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/cli	…`

Do NOT run the whole package yet. `TestUsageMatchesTheGolden` (args_test.go:44) and `TestUnknownCommandMatchesTheGolden` (args_test.go:52) compare against `golden/cli/help.json` and `unknown-command.json`, and they fail until Step 9 regenerates those goldens. That failure is the golden doing its job.

- [ ] **Step 8: Run the negative control**

In run.go, change `case "off":` to `case "off", "none":` inside the new `case "logging"`, then run:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/ -run TestLoggingCommandRefusesAnythingButLokiOrOff
```

Expected: FAIL with `pstack logging none: exit 3, stderr "pstack: no control stack found at …/control (…/control/.env is missing). …"; want exit 3 and the usage line`. Revert the mutation to `case "off":` and re-run: `ok`.

- [ ] **Step 9: Regenerate the goldens deliberately**

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts
cd /Volumes/S1/code/preview-stacks && but diff
```

Expected: among `packages/conformance/golden/**`, exactly four files change. Checked with grep: they are the only goldens holding `|ui|swarm|` or `ui, swarm`.
- `golden/cli/help.json`, `help-h.json`, `no-args.json`: the Usage line gains `|ui|logging|swarm|`, and the two `logging:` lines plus a blank line appear after the `ui:` block.
- `golden/cli/unknown-command.json`: `Commands: … upgrade, ui, logging, swarm, pull, …`.

Any other modified golden (a render file, a transcript) is an unintended contract change: stop and find it. The untracked `golden/host/db/pstack.db-shm` and `pstack.db-wal` are not this task's; never commit them.

Then:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cli/
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
```

Expected: `ok  	…/internal/cli`, and `cli-goldens.test.ts` all pass. The case count is unchanged: this task adds no table rows, so `expected-pass.json` is untouched.

- [ ] **Step 10: Docs — docs/usage.md §7**

In `docs/usage.md`, insert the following immediately before the line `### Why \`init\` is CLI-only, and always will be` (currently line 1448, after the silent-revert guard section that T5 extends):

````markdown
### Turn Loki logging on or off: `pstack logging`

```console
$ pstack logging loki       # Loki on this host
$ pstack logging off
$ pstack logging loki -n    # print the plan, change nothing
```

Like `pstack ui`, it re-runs `init` from what `control/.env` and the generated compose already hold,
so the token, the DNS token and the domain stay as they are. Asking for the mode the host is already
in recreates nothing.

**`loki`** adds a Loki service to the control stack, installs and enables the `loki` log plugin on
this node, and writes a push password to `control/.env`. If the plugin does not install, the switch
fails and says so. From then on, a deploy gives every service without a `logging:` key of its own
the loki driver, labelled `service_name=<stack>-<service>`. A service with its own `logging:` is
left alone and named in the job log. Deployments already running switch on their next deploy, a
sleeping one on wake.

**`off`** removes the Loki service; the plugin stays installed. New deploys get no driver.
Containers still running with it push to `loki.<domain>` and get a 404, which the plugin does not
retry: their logs are dropped and stopping them is not delayed. The command prints how many
deployments still carry the driver in their `compose.generated.yml`. Each drops it on its next
deploy, a sleeping one on wake.

`pstack upgrade` keeps whichever mode the host is in, and the push password with it. Workers get the
plugin from the join material; see [Swarm mode](#swarm-mode).

#### Upgrading the plugin by hand

The plugin is pinned to Loki's version (`grafana/loki-docker-driver:3.7.7-<arch>`), and
`pstack upgrade` never upgrades it. **Doing it interrupts every preview on that node**, because
dockerd restarts. On each node, with `<arch>` set to `amd64` or `arm64`:

```console
$ docker plugin disable --force loki
$ docker plugin upgrade loki grafana/loki-docker-driver:<new>-<arch> --grant-all-permissions
$ docker plugin enable loki
$ sudo systemctl restart docker      # rc-service docker restart on Alpine
```
````

The console block shows commands only, no captured output, like `### Upgrading a host` (usage.md:1332-1337). The output needs a real host.

- [ ] **Step 11: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Pick the IDs for exactly these files: `packages/pstack/internal/cli/args.go`, `commands.go`, `completion.go`, `run.go`, `commands_test.go`, `docs/usage.md` (this task's hunk only, if another task's edit is still uncommitted there), and `packages/conformance/golden/cli/help.json`, `help-h.json`, `no-args.json`, `unknown-command.json`. Exclude `golden/host/**`, `packages/pstack/bin/pstack`, and anything else.

```bash
but commit -b claude/loki-logging -m "feat(cli): pstack logging loki|off" <ids>
```

---

### Task 7: inspect + swarm: discover Loki; read each node's log plugins

**Files:**
- Modify: `packages/pstack/internal/inspect/control.go:1-13` (header), `:84-86` (new `LokiPushURL` between `ControlRuntime` and the refusal vars)
- Modify: `packages/pstack/internal/swarm/swarm.go:53-61` (import), `:559-573` (`Node`), `:701` (new `MarkLokiPlugins` after `stringPtr`), `:933-945` (`SwarmReport`). T1 moved these lines, so find each edit by the exact text shown.
- Modify: `packages/conformance/golden/host/expected/swarm.json:18-19`. This is the one existing golden this task changes. See Step 11.
- Test: `packages/pstack/internal/inspect/control_test.go` (append after line 104)
- Test: `packages/pstack/internal/swarm/swarm_test.go` (new consts after line 65, a new test after line 375, and one literal on line 324)

**Interfaces:**
- Consumes: `initctl.PushURLLabel` (T2, `const PushURLLabel = "pstack.logging.push-url"`), `swarm.LokiPluginInstall` (T1, a single-line `const`)
- Produces:
  - `package inspect; func LokiPushURL(r exec.Runner) string`
  - `package swarm; type Node struct { …; Self bool `json:"self"`; LokiPlugin *bool `json:"lokiPlugin"` }` (last field, a pointer, no omitempty)
  - `package swarm; func MarkLokiPlugins(r exec.Runner, info *Info)`
  - `SwarmReport(info Info) string` keeps its signature. Output is byte-identical unless some node has `LokiPlugin` false.

Facts this relies on:
- `idsByLabel` returns `nil, false` when `docker ps` fails (inspect.go:185-192).
- `inspectIDs` returns nil for zero ids or a failed inspect (inspect.go:160-181).
- `rawInspect.Config.Labels` is a `map[string]string` (inspect.go:139-143).
- `DetectChallenge` is the precedent (inspect.go:765-787).
- `init` runs `up -d --remove-orphans` (init.go:391).
- `shim()` answers only exact `TrimSpace(cmd)` matches, and returns OK with empty stdout for anything else (swarm_test.go:50-59).
- The `node-ls.jsonl` ids are `n1abcdef01234567` (preview-host) and `n2abcdef01234567` (worker-1).
- `packages/conformance/test/host-fixture.test.ts:112` compares `/api/swarm` to `golden/host/expected/swarm.json` with `toEqual`. Checked in a scratchpad bun test: Bun's `toEqual` fails on an extra `lokiPlugin: null` key. So the new field makes that golden change.

- [ ] **Step 1: Write the failing discovery test.** Append to `packages/pstack/internal/inspect/control_test.go`. The imports are already there: `strings`, `testing`, `exec`.

```go
// lokiPush is a push URL as init renders it, with a fixed password.
const lokiPush = "https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"

// lokiHost scripts the control project's `docker ps` and `docker inspect` separately, so a case can
// make either one lie. The fixtures spell the label keys as docker returns them, never through the
// Go constants, so a drifted constant fails here.
func lokiHost(ps exec.Result, inspect string) *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq") && strings.Contains(cmd, "com.docker.compose.project=pstack-control"):
			return ps, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: inspect}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func TestLokiPushURLComesFromTheControlStacksLokiContainer(t *testing.T) {
	// negative control: drop the `com.docker.compose.service == "loki"` check — traefik carrying the label reads as Loki.
	traefik := `{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.6.1","Labels":{"com.docker.compose.service":"traefik"}}}`
	loki := `{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki","pstack.logging.push-url":"` + lokiPush + `"}}}`

	t.Run("the loki container's label is the push URL", func(t *testing.T) {
		// negative control: change initctl.PushURLLabel to "pstack.logging.push_url" — the fixture's literal key no longer matches and the URL reads "".
		if got := LokiPushURL(lokiHost(exec.Result{OK: true, Stdout: "t1\nl1\n"}, "["+traefik+","+loki+"]")); got != lokiPush {
			t.Errorf("got %q", got)
		}
	})

	t.Run("the label on any other control service is not Loki", func(t *testing.T) {
		// negative control: drop the `com.docker.compose.service == "loki"` check — traefik's label is returned.
		squat := `{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Labels":{"com.docker.compose.service":"traefik","pstack.logging.push-url":"` + lokiPush + `"}}}`
		if got := LokiPushURL(lokiHost(exec.Result{OK: true, Stdout: "t1\n"}, "["+squat+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("no control containers is logging off", func(t *testing.T) {
		// negative control: drop inspectIDs' `len(ids) == 0` return — a bare `docker inspect` is asked and this fake's loki answer leaks through.
		if got := LokiPushURL(lokiHost(exec.Result{OK: true}, "["+loki+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("docker not answering is logging off", func(t *testing.T) {
		// negative control: drop idsByLabel's `!res.OK` return — the failed `docker ps` still printed l1, and its inspect reads a URL.
		failed := exec.Result{OK: false, Code: 1, Stdout: "l1\n", Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}
		if got := LokiPushURL(lokiHost(failed, "["+loki+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/ -run TestLokiPushURLComesFromTheControlStacksLokiContainer
```
Expected:
```
internal/inspect/control_test.go:NNN:NN: undefined: LokiPushURL
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect [build failed]
```

- [ ] **Step 3: Implement `LokiPushURL`.** Two edits to `packages/pstack/internal/inspect/control.go`.

First, the header records the new read. Replace:
```go
// it — the rule the control template's header states, enforced by name instead of by distance.
package inspect
```
with:
```go
// it — the rule the control template's header states, enforced by name instead of by distance.
//
// And one read that deploys depend on: LokiPushURL. Whether a deploy injects a loki logging block
// comes from the running loki container's label, not from a setting. So `pstack up` on the host and
// the API always agree, and nothing needs syncing when `pstack logging` switches.
package inspect
```

Second, insert the function. Replace:
```go
	return out
}

// The three ways RestartControlService refuses, for the route to map onto statuses.
```
with:
```go
	return out
}

// LokiPushURL is the URL deploys put in every injected logging block. It is the
// `pstack.logging.push-url` label that `pstack init --logging loki` renders onto the control stack's
// loki service, read from the container the way DetectChallenge reads Traefik's flags. "" means
// logging is off: docker did not answer, or no control container is the loki service with the label.
//
// `-a` (idsByLabel) is deliberate: a Loki that is restarting must not flip deploys between injected
// and not. `pstack logging off` re-runs init, whose `up --remove-orphans` removes the container, so
// it stops being found.
func LokiPushURL(r exec.Runner) string {
	// nil when docker did not answer, and inspectIDs of nil asks nothing.
	ids, _ := idsByLabel(r, "com.docker.compose.project="+ControlProject)
	for _, raw := range inspectIDs(r, ids) {
		if raw.Config == nil || raw.Config.Labels["com.docker.compose.service"] != "loki" {
			continue
		}
		if push := raw.Config.Labels[initctl.PushURLLabel]; push != "" {
			return push
		}
	}
	return ""
}

// The three ways RestartControlService refuses, for the route to map onto statuses.
```
(`initctl` and `exec` are already imported, control.go:20-21.)

- [ ] **Step 4: Run it and watch it pass, then run each negative control**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/
```
Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect	…`

Apply each mutation below, run `go test -race -timeout 120s ./internal/inspect/ -run TestLokiPushURLComesFromTheControlStacksLokiContainer`, confirm the named subtest FAILs, then revert:
1. In `LokiPushURL`, change `raw.Config.Labels["com.docker.compose.service"] != "loki"` to `false`. Fails "the label on any other control service is not Loki".
2. In initctl/init.go, change `PushURLLabel` to `"pstack.logging.push_url"`. Fails "the loki container's label is the push URL".
3. In inspect.go `inspectIDs`, delete `if len(ids) == 0 { return nil }`. Fails "no control containers is logging off".
4. In inspect.go `idsByLabel`, delete `if !res.OK { return nil, false }`. Fails "docker not answering is logging off".

- [ ] **Step 5: Write the failing node-plugin tests.** In `packages/pstack/internal/swarm/swarm_test.go`, replace the const block at lines 61-65:
```go
const (
	cmdInfo   = "docker info --format '{{json .Swarm}}'"
	cmdNodeLs = "docker node ls --format '{{json .}}'"
	cmdToken  = "docker swarm join-token -q worker"
)
```
with:
```go
const (
	cmdInfo   = "docker info --format '{{json .Swarm}}'"
	cmdNodeLs = "docker node ls --format '{{json .}}'"
	cmdToken  = "docker swarm join-token -q worker"
	// cmdNodeInspect is the one `docker node inspect` MarkLokiPlugins asks for node-ls.jsonl's two nodes.
	cmdNodeInspect = "docker node inspect --format '{{json .}}' 'n1abcdef01234567' 'n2abcdef01234567'"
)

// nodeInspect answers cmdNodeInspect, trimmed to the fields around the list swarm schedules on.
// Docker reports `--alias loki` as `loki:latest`, beside the built-in log drivers. worker-1 has only
// the built-ins.
var nodeInspect = strings.Join([]string{
	`{"ID":"n1abcdef01234567","Description":{"Hostname":"preview-host","Engine":{"EngineVersion":"28.0.1","Plugins":[{"Type":"Log","Name":"json-file"},{"Type":"Log","Name":"loki:latest"},{"Type":"Network","Name":"overlay"},{"Type":"Volume","Name":"local"}]}}}`,
	`{"ID":"n2abcdef01234567","Description":{"Hostname":"worker-1","Engine":{"EngineVersion":"28.0.1","Plugins":[{"Type":"Log","Name":"json-file"},{"Type":"Network","Name":"overlay"},{"Type":"Volume","Name":"local"}]}}}`,
}, "\n") + "\n"

// loggedManager is manager(t) that also answers the node inspect.
func loggedManager(t *testing.T) *exec.Fake {
	return shim(map[string]string{
		cmdInfo:        read(t, "info-active.json"),
		cmdNodeLs:      read(t, "node-ls.jsonl"),
		cmdToken:       "SWMTKN-1-abcdef-ghijkl\n",
		cmdNodeInspect: nodeInspect,
	})
}
```

In `TestSwarmDiscovery`, the wire-shape prefix on line 324 now also pins the new tri-state as `null`. This makes the assertion stricter, not weaker. Replace:
```go
		if !strings.HasPrefix(js, `{"reachable":true,"active":true,"nodeId":"n1","managerAddr":"10.0.0.1:2377","nodes":[{"id":"n1","hostname":"mgr","role":"manager","status":"ready","availability":"active","managerStatus":"leader","engineVersion":"28.0.1","self":true},{"id":"n2","hostname":"wrk","role":"worker","status":"ready","availability":"active","managerStatus":null,`) {
```
with:
```go
		if !strings.HasPrefix(js, `{"reachable":true,"active":true,"nodeId":"n1","managerAddr":"10.0.0.1:2377","nodes":[{"id":"n1","hostname":"mgr","role":"manager","status":"ready","availability":"active","managerStatus":"leader","engineVersion":"28.0.1","self":true,"lokiPlugin":null},{"id":"n2","hostname":"wrk","role":"worker","status":"ready","availability":"active","managerStatus":null,`) {
```

Insert the new test between `TestSwarmDiscovery`'s closing `}` and `func TestPstackSwarmTheCLIHalf(t *testing.T) {`:
```go
func TestLokiPluginOnNodes(t *testing.T) {
	// negative control: match Name == "loki" only — docker reports the alias as loki:latest, so preview-host reads false.
	t.Run("one node inspect marks each node: the aliased Log plugin is true, the built-ins alone are false", func(t *testing.T) {
		// negative control: match Name == "loki" only — preview-host reads false.
		f := loggedManager(t)
		info := SwarmInfo(f)
		MarkLokiPlugins(f, &info)
		rows := ""
		for _, n := range info.Nodes {
			rows += n.Hostname + ":" + jsonOf(t, n.LokiPlugin) + " "
		}
		// A drifted command line gets the shim's empty answer and reads null:null.
		if rows != "preview-host:true worker-1:false " {
			t.Errorf("nodes: %s", rows)
		}
	})

	t.Run("docker not answering leaves every node unknown, never missing", func(t *testing.T) {
		// negative control: drop the `!res.OK` return — the partial answer marks preview-host true.
		f := loggedManager(t)
		answer := f.Answer
		f.Answer = func(cmd string) (exec.Result, bool) {
			if strings.TrimSpace(cmd) == cmdNodeInspect {
				first, _, _ := strings.Cut(nodeInspect, "\n")
				return exec.Result{OK: false, Code: 1, Stdout: first + "\n", Stderr: "Error response from daemon: node n2abcdef01234567 not found\n"}, true
			}
			return answer(cmd)
		}
		info := SwarmInfo(f)
		MarkLokiPlugins(f, &info)
		if len(info.Nodes) != 2 {
			t.Fatalf("nodes: %s", jsonOf(t, info))
		}
		for _, n := range info.Nodes {
			if n.LokiPlugin != nil {
				t.Errorf("%s: %s", n.Hostname, jsonOf(t, n.LokiPlugin))
			}
		}
	})

	t.Run("no nodes, no command", func(t *testing.T) {
		// negative control: drop the `len(info.Nodes) == 0` return — a bare `docker node inspect` runs on a host that is not a manager.
		f := inactive()
		info := SwarmInfo(f)
		before := len(f.Commands())
		MarkLokiPlugins(f, &info)
		if got := f.Commands(); len(got) != before {
			t.Errorf("commands: %v", got)
		}
	})

	t.Run("pstack swarm names each node without the plugin, and the line to run there", func(t *testing.T) {
		// negative control: append the flag after the ports table — it no longer sits between the nodes and `add a worker:`.
		f := loggedManager(t)
		info := SwarmInfo(f)
		MarkLokiPlugins(f, &info)
		report := SwarmReport(info)
		want := "  worker-1        worker            ready   active        28.0.1  n2abcdef0123\n" +
			"\n" +
			"no loki log plugin on worker-1 — logged services will not run there. Run on each:\n" +
			"  " + LokiPluginInstall + "\n" +
			"\n" +
			"add a worker:"
		if !strings.Contains(report, want) {
			t.Errorf("report:\n%s\nwant within it:\n%s", report, want)
		}
		// preview-host has the plugin, so the flag never names it.
		if strings.Contains(report, "on preview-host") {
			t.Errorf("preview-host has the plugin:\n%s", report)
		}
	})
}
```
Leave the exact `wantActive` transcript in `TestPstackSwarmTheCLIHalf` (swarm_test.go:391-411) unchanged. Every `LokiPlugin` is nil there, so it proves that nil nodes are never flagged.

- [ ] **Step 6: Run it and watch it fail**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ -run 'TestLokiPluginOnNodes|TestSwarmDiscovery'
```
Expected (build failure):
```
internal/swarm/swarm_test.go:NNN:NN: undefined: MarkLokiPlugins
internal/swarm/swarm_test.go:NNN:NN: n.LokiPlugin undefined (type Node has no field or method LokiPlugin)
FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm [build failed]
```

- [ ] **Step 7: Implement the `Node` field and `MarkLokiPlugins`.** In `packages/pstack/internal/swarm/swarm.go`:

Import: replace
```go
import (
	"math"
	"strings"
	"unicode"
```
with
```go
import (
	"encoding/json"
	"math"
	"strings"
	"unicode"
```

`Node`: replace
```go
	// Self is the node this API runs on.
	Self bool `json:"self"`
}
```
with
```go
	// Self is the node this API runs on.
	Self bool `json:"self"`
	// LokiPlugin: nil = not checked (this host's logging is off) or docker did not answer; false = no
	// Log plugin named loki / loki:latest, so swarm keeps logged services off this node.
	LokiPlugin *bool `json:"lokiPlugin"`
}
```

After `func stringPtr(s string) *string { return &s }`, insert:
```go

// nodePlugins is the part of `docker node inspect` that MarkLokiPlugins reads.
type nodePlugins struct {
	ID          string
	Description struct {
		Engine struct {
			Plugins []struct{ Type, Name string }
		}
	}
}

// MarkLokiPlugins sets each node's LokiPlugin from the plugin list the node reports to the manager.
// That is the same list swarm's scheduler filters on: a task with `logging.driver: loki` is placed
// only on a node reporting an ENABLED Log plugin named `loki` or `loki:latest` (`--alias loki` is
// reported as the latter), and stays pending anywhere else. One `docker node inspect` covers every
// node. When docker does not answer, every LokiPlugin stays nil: unknown, never "missing".
//
// A worker that joined before logging was on cannot be fixed from here, because there is no remote
// exec to workers (see the package comment). This flag is how the Swarm page and `pstack swarm` name
// it.
func MarkLokiPlugins(r exec.Runner, info *Info) {
	if len(info.Nodes) == 0 {
		return
	}
	ids := make([]string, len(info.Nodes))
	for i, n := range info.Nodes {
		ids[i] = Shq(n.ID)
	}
	res := r.Run("docker node inspect --format '{{json .}}' "+strings.Join(ids, " "), exec.RunOptions{Label: "docker node inspect"})
	if !res.OK {
		return
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		var node nodePlugins
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &node) != nil {
			continue
		}
		has := false
		for _, p := range node.Description.Engine.Plugins {
			if p.Type == "Log" && (p.Name == "loki" || p.Name == "loki:latest") {
				has = true
			}
		}
		for i := range info.Nodes {
			if info.Nodes[i].ID == node.ID {
				info.Nodes[i].LokiPlugin = &has
			}
		}
	}
}
```
The fields are untagged on purpose: `encoding/json` matches docker's `ID`, `Description`, `Engine`, `Plugins`, `Type` and `Name` keys by name. A node missing from the output keeps nil.

- [ ] **Step 8: Implement the `SwarmReport` flag.** In `SwarmReport`, replace
```go
	for _, r := range rows {
		out = append(out, line(r))
	}
	out = append(out,
		"",
		"add a worker:",
```
with
```go
	for _, r := range rows {
		out = append(out, line(r))
	}
	// Only a node that was checked and found without the plugin is named. nil means logging is off or
	// docker did not answer, and neither is a reason to send someone to a worker.
	var noPlugin []string
	for _, n := range info.Nodes {
		if n.LokiPlugin != nil && !*n.LokiPlugin {
			noPlugin = append(noPlugin, n.Hostname)
		}
	}
	if len(noPlugin) > 0 {
		out = append(out, "", "no loki log plugin on "+strings.Join(noPlugin, ", ")+" — logged services will not run there. Run on each:", "  "+LokiPluginInstall)
	}
	out = append(out,
		"",
		"add a worker:",
```

- [ ] **Step 9: Run it and watch it pass**

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/
```
Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm	…`. This includes the unchanged `wantActive` transcript and the updated `TestSwarmDiscovery` prefix.

- [ ] **Step 10: Run the swarm negative controls.** Apply each mutation, run `go test -race -timeout 120s ./internal/swarm/ -run 'TestLokiPluginOnNodes|TestPstackSwarmTheCLIHalf'`, confirm the named subtest FAILs, then revert:
1. Change `(p.Name == "loki" || p.Name == "loki:latest")` to `p.Name == "loki"`. Fails "one node inspect marks each node…".
2. Delete `if !res.OK { return }` in `MarkLokiPlugins`. Fails "docker not answering leaves every node unknown…".
3. Delete `if len(info.Nodes) == 0 { return }`. Fails "no nodes, no command".
4. Move the `if len(noPlugin) > 0 {…}` block below the ports loop, just before `return strings.Join(out, "\n")`. Fails "pstack swarm names each node without the plugin…".
5. Change `n.LokiPlugin != nil && !*n.LokiPlugin` to `n.LokiPlugin == nil || !*n.LokiPlugin`. Fails the `wantActive` exact transcript in `TestPstackSwarmTheCLIHalf`, which proves nil is never flagged.

- [ ] **Step 11: The host golden learns the new null field.** `/api/swarm` spreads `Info` (routes.go:186), so every node now carries `"lokiPlugin": null`. `test/host-fixture.test.ts:112` compares the body with `toEqual`, which rejects the extra key. First build and watch it fail:

```sh
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts
```
Expected: `(fail) the golden host opens unchanged > GET /api/swarm answers the reference bytes`, with the diff line `+       "lokiPlugin": null,`.

Edit only `packages/conformance/golden/host/expected/swarm.json` by hand. Replace:
```json
        "engineVersion": "28.0.1",
        "self": true
      }
```
with:
```json
        "engineVersion": "28.0.1",
        "self": true,
        "lokiPlugin": null
      }
```
Do NOT run `bun gen/host-fixture.ts`: it rewrites the whole fixture, database included. Do not touch the untracked `golden/host/db/pstack.db-shm` or `pstack.db-wal`. Rerun:
```sh
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts
```
Expected: `0 fail`. The pass count matches `expected-pass.json` (`"test/host-fixture.test.ts": 34`).

- [ ] **Step 12: Package gate.** Run the direct consumers of `Node` and the inspect package too:

```sh
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/inspect/ ./internal/swarm/ ./internal/api/ ./internal/cli/
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts test/api-share-sleep-swarm.test.ts
```
Expected: four `ok` lines and `0 fail`. The `swarm-status` golden is unchanged because no node is checked.

- [ ] **Step 13: Commit.** Run `but status` to get the IDs of exactly these five files:
- `packages/pstack/internal/inspect/control.go`
- `packages/pstack/internal/inspect/control_test.go`
- `packages/pstack/internal/swarm/swarm.go`
- `packages/pstack/internal/swarm/swarm_test.go`
- `packages/conformance/golden/host/expected/swarm.json`

Leave out `golden/host/db/pstack.db-shm` and `pstack.db-wal`. Then:
```sh
but commit -b claude/loki-logging -m "feat(inspect,swarm): discover Loki and read each node's log plugins" <ids>
```

---

### Task 8: autolabel: inject the loki logging block; open the compose write gates

**Files:**
- Modify: `packages/pstack/internal/autolabel/autolabel.go`:
  - `:1-2` package synopsis
  - `:51-53` new LOGGING header section, before THE CHALLENGE PROBE
  - `:120-123` DetectLogging, after DetectChallenge
  - `:408-411` InjectLogging, after AugmentComposeDoc
  - `:416-417` MaterializeArgs.Challenge comment
  - `:420-427` MaterializeResult
  - `:436-438`, `:452-454`, `:473-477`, `:498-508` and `:518` in MaterializeCompose
- Modify: `docs/control-plane.md`:
  - `:978-980` new `## 5g. Loki logging` section before `## 6. Submitting a deployment`
  - `:1265` "five explicit paths" becomes "seven explicit paths"
- Test: `packages/pstack/internal/autolabel/autolabel_test.go` (append after line 537)
- Test: `packages/pstack/internal/compose/compose_test.go:21-23` (TestMain after the import block)

**Interfaces:**
- Consumes: `func LokiPushURL(r exec.Runner) string` (package inspect, T7). It runs `docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'` through `idsByLabel` (inspect.go:187-193), then `docker inspect <ids>` through `inspectIDs` (inspect.go:161-182). It returns `Config.Labels["pstack.logging.push-url"]` from the first container whose `Config.Labels["com.docker.compose.service"] == "loki"`, or `""`.
- Produces:
  - `var DetectLogging = func(r exec.Runner) string { return inspect.LokiPushURL(r) }` (package autolabel). `""` means logging is off.
  - `func InjectLogging(doc *omap.Map, stack, pushURL string) (out *omap.Map, logged, own []string)`. It is pure, and both slices are non-nil.
  - `MaterializeResult.Logged []string` and `MaterializeResult.LogNotes []string`, non-nil on every return path. Each LogNotes line is exactly `logging: service <svc> has its own logging: — left alone`.
  - `MaterializeCompose(a MaterializeArgs) (*MaterializeResult, error)`, signature unchanged. With a push URL, a compose stack gets `compose.generated.yml` whenever a service was injected.
  - `func TestMain(m *testing.M)` in package compose, pinning `autolabel.DetectLogging` to `""`.

Why the existing tests stay green:
- **autolabel:** its fakes (`exec.NewFake(nil, "")`, and the fakes that answer for traefik at autolabel_test.go:425-485) list no container whose compose service is `loki`, so discovery returns `""`.
- **stack, api and cli:** their tests put no compose file on disk (no testdata dir, no WriteFile of a compose file). `MaterializeCompose` returns at `os.ReadFile` (autolabel.go:456-461), before discovery runs.
- **compose:** it is the only package with a test that reads `Commands()[0]` after materializing a real file (compose_test.go:235-264). The TestMain pin is there for that test.

- [ ] **Step 1: Write the failing test.** Append the code below to `packages/pstack/internal/autolabel/autolabel_test.go`, after `TestSwarmLabelsGoUnderDeploy`, which ends at line 537. The file already imports everything it needs: `os`, `filepath`, `reflect`, `strings`, `testing`, `exec` and `omap`.

```go
// lokiURL is the push URL a logging-on control stack advertises, the password interpolated.
const lokiURL = "https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"

// lokiBlockWeb is the fixed block for service web of stack pr-7, as JSON, key order included — the
// design doc's block, every value a string.
const lokiBlockWeb = `{"driver":"loki","options":{"loki-url":"` + lokiURL + `","loki-external-labels":"service_name=pr-7-web","loki-relabel-config":"[{action: labeldrop, regex: filename}]","loki-retries":"2","loki-timeout":"1s","loki-max-backoff":"800ms","mode":"non-blocking","keep-file":"false","max-size":"10m","max-file":"3"}}`

// logTo pins the discovery seam for one test and restores it after.
func logTo(t *testing.T, url string) {
	t.Helper()
	prev := DetectLogging
	DetectLogging = func(exec.Runner) string { return url }
	t.Cleanup(func() { DetectLogging = prev })
}

func TestLoggingInjection(t *testing.T) {
	quiet := exec.NewFake(nil, "")
	http01 := HTTP01
	// web has no logging; db and cache opted out, one with a driver and one with an empty block.
	mixed := "services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    logging: {driver: json-file}\n  cache:\n    image: redis\n    logging: {}\n"
	write := func(t *testing.T, dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	written := func(t *testing.T, dir string) *omap.Map {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, GeneratedCompose))
		if err != nil {
			t.Fatal(err)
		}
		v, err := omap.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		return v.(*omap.Map)
	}

	t.Run("a service without logging gets exactly the fixed block, labelled <stack>-<service>", func(t *testing.T) {
		// negative control: label the stream "service_name="+name, without the stack — the JSON
		// differs.
		out, logged, _ := InjectLogging(doc(t, mixed), "pr-7", lokiURL)
		if got := jsonOf(t, out.GetMap("services").GetMap("web").GetMap("logging")); got != lokiBlockWeb {
			t.Errorf("block:\n%s\n%s", got, lokiBlockWeb)
		}
		if !reflect.DeepEqual(logged, []string{"web"}) {
			t.Errorf("logged: %v", logged)
		}
	})

	t.Run("any logging key, json-file or empty, is left byte-unchanged and listed", func(t *testing.T) {
		// negative control: drop the svc.Has("logging") branch — db's json-file block becomes
		// loki's.
		in := doc(t, mixed)
		out, _, own := InjectLogging(in, "pr-7", lokiURL)
		for _, name := range []string{"db", "cache"} {
			if got, want := jsonOf(t, out.GetMap("services").GetMap(name)), jsonOf(t, in.GetMap("services").GetMap(name)); got != want {
				t.Errorf("%s changed: %s, submitted %s", name, got, want)
			}
		}
		if !reflect.DeepEqual(own, []string{"db", "cache"}) {
			t.Errorf("own: %v", own)
		}
	})

	t.Run("the input document is never mutated", func(t *testing.T) {
		// negative control: drop the Clone() in InjectLogging — the input's web gains the block.
		in := doc(t, mixed)
		before := jsonOf(t, in)
		InjectLogging(in, "pr-7", lokiURL)
		if jsonOf(t, in) != before {
			t.Errorf("input mutated: %s", jsonOf(t, in))
		}
	})

	t.Run("logging off: no block, and a stack with no routing keeps its own file", func(t *testing.T) {
		// negative control: drop the `pushURL != ""` guard around InjectLogging — the routed app
		// gains a block with an empty loki-url.
		logTo(t, "")
		dir := t.TempDir()
		write(t, dir, "services:\n  app:\n    image: nginx\n")
		r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
		if err != nil || r.File != "docker-compose.yml" || r.Logged == nil || len(r.Logged) != 0 || r.LogNotes == nil {
			t.Errorf("plain: %+v, %v", r, err)
		}
		if exists(filepath.Join(dir, GeneratedCompose)) {
			t.Error("derived file written with logging off and nothing routed")
		}
		write(t, dir, "services:\n  app:\n    image: nginx\n    labels: [pstack.routing.port=80]\n")
		r, err = MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
		if err != nil || r.File != GeneratedCompose || len(r.Logged) != 0 {
			t.Fatalf("routed: %+v, %v", r, err)
		}
		if written(t, dir).GetMap("services").GetMap("app").Has("logging") {
			t.Errorf("block injected with logging off")
		}
	})

	t.Run("logging on: a stack with no routing gets the derived file, and own blocks are named", func(t *testing.T) {
		// negative control: drop `&& pushURL == ""` from the pre-check — the submitted file is
		// used.
		logTo(t, lokiURL)
		dir := t.TempDir()
		write(t, dir, mixed)
		r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
		if err != nil || r.File != GeneratedCompose {
			t.Fatalf("got %+v, %v", r, err)
		}
		if !reflect.DeepEqual(r.Logged, []string{"web"}) {
			t.Errorf("logged: %v", r.Logged)
		}
		wantNotes := []string{
			"logging: service db has its own logging: — left alone",
			"logging: service cache has its own logging: — left alone",
		}
		if !reflect.DeepEqual(r.LogNotes, wantNotes) {
			t.Errorf("notes: %q", r.LogNotes)
		}
		services := written(t, dir).GetMap("services")
		if got := jsonOf(t, services.GetMap("web").GetMap("logging")); got != lokiBlockWeb {
			t.Errorf("web: %s", got)
		}
		if got := jsonOf(t, services.GetMap("db").GetMap("logging")); got != `{"driver":"json-file"}` {
			t.Errorf("db: %s", got)
		}
	})

	t.Run("logging on but every service has its own: the submitted file, still named in the job log", func(t *testing.T) {
		// negative control: return untouched() from the compose early return — LogNotes comes back
		// empty.
		logTo(t, lokiURL)
		dir := t.TempDir()
		write(t, dir, "services:\n  db:\n    image: postgres\n    logging: {driver: json-file}\n")
		r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
		if err != nil || r.File != "docker-compose.yml" || exists(filepath.Join(dir, GeneratedCompose)) {
			t.Fatalf("got %+v, %v", r, err)
		}
		if !reflect.DeepEqual(r.LogNotes, []string{"logging: service db has its own logging: — left alone"}) {
			t.Errorf("notes: %q", r.LogNotes)
		}
	})

	t.Run("under swarm the block survives the conversion", func(t *testing.T) {
		// negative control: add {"logging", "x"} to swarm.swarmUnsupported — Swarmify drops the
		// block.
		logTo(t, lokiURL)
		st := specFrom(t, "spec.yml", map[string]string{"PR": "7", "PSTACK_ORCHESTRATOR": "swarm"})
		dir := t.TempDir()
		// No `profiles:`, so Swarmify keeps web whatever the spec selects.
		write(t, dir, "services:\n  web:\n    image: nginx\n")
		r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: st, Runner: quiet, Challenge: &http01})
		if err != nil || r.File != GeneratedCompose {
			t.Fatalf("got %+v, %v", r, err)
		}
		if got := jsonOf(t, written(t, dir).GetMap("services").GetMap("web").GetMap("logging")); got != lokiBlockWeb {
			t.Errorf("after Swarmify: %s", got)
		}
	})

	t.Run("the seam's default reads the control stack's loki container", func(t *testing.T) {
		// negative control: make DetectLogging's default `return ""` — a host running Loki gets no
		// block (the DetectChallenge seam shipped exactly that bug once).
		lokiHost := exec.NewFake(nil, "")
		lokiHost.Answer = func(cmd string) (exec.Result, bool) {
			if strings.HasPrefix(cmd, "docker ps") {
				return exec.Result{OK: true, Stdout: "l0k1\n"}, true
			}
			if strings.HasPrefix(cmd, "docker inspect") {
				return exec.Result{OK: true, Stdout: `[{"Id":"l0k1","Name":"/pstack-control-loki-1","Config":{"Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"loki","pstack.logging.push-url":"` + lokiURL + `"}}}]`}, true
			}
			return exec.Result{}, false
		}
		dir := t.TempDir()
		write(t, dir, "services:\n  web:\n    image: nginx\n")
		r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: lokiHost, Challenge: &http01})
		if err != nil || !reflect.DeepEqual(r.Logged, []string{"web"}) {
			t.Fatalf("got %+v, %v", r, err)
		}
		if got := jsonOf(t, written(t, dir).GetMap("services").GetMap("web").GetMap("logging")); got != lokiBlockWeb {
			t.Errorf("web: %s", got)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ -run TestLoggingInjection
```

Expected: a build failure. The output contains `undefined: DetectLogging`, `undefined: InjectLogging` and `r.Logged undefined (type *MaterializeResult has no field or method Logged)`. It ends with `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel [build failed]`.

- [ ] **Step 3: Implement the package header and the seam** in `packages/pstack/internal/autolabel/autolabel.go`.

3a. Replace the synopsis at lines 1-2:

```go
// Package autolabel generates the Traefik labels and network wiring a preview service needs, from
// one short label.
```

with:

```go
// Package autolabel generates the Traefik labels and network wiring a preview service needs, from
// one short label — and, on a host that runs Loki, the logging block that ships its output there.
```

3b. Insert a LOGGING section between the SWARM section and THE CHALLENGE PROBE. Replace lines 51-53:

```go
// provider reads service labels and its docker provider must not see a second copy on the task.
//
// ── THE CHALLENGE PROBE (a Go-only note) ─────────────────────────────────────────────────────────
```

with:

```go
// provider reads service labels and its docker provider must not see a second copy on the task.
//
// ── LOGGING ──────────────────────────────────────────────────────────────────────────────────────
//
// On a host that runs Loki (`pstack init --logging loki`), every service WITHOUT a `logging` key
// gets the loki driver block — after the labels, before the swarm conversion, so one step serves
// both orchestrators. Whether Loki runs is not a setting: DetectLogging reads the push URL off the
// control stack's loki container, the way DetectChallenge reads Traefik, so `pstack up` on the host
// and the API always inject the same thing. No container, no block.
//
// A service with ANY `logging` key — `json-file`, even an empty one — is left alone and named in
// the job log. The traefik.* rule again, for the same reason: pstack's options could be refused by
// the user's driver, so nothing is merged.
//
// Every option but the URL and the `service_name` label is a constant, never a setting. When Loki
// is unreachable the driver holds a node-wide lock while it retries, so every `docker stop` on that
// node waits out the retry loop; only small retries, timeout and backoff bound the wait to seconds.
//
// The consequence under compose: a stack with no routing labels runs from the derived file whenever
// a service got the block. With logging off nothing here runs, and the old gates hold exactly.
//
// ── THE CHALLENGE PROBE (a Go-only note) ─────────────────────────────────────────────────────────
```

The `── LOGGING ─…` rule is 100 characters wide, matching the other three.

3c. Add the seam right after `DetectChallenge`. Replace lines 120-123:

```go
	return Challenge(inspect.DetectChallenge(r))
}

// RoutingRequest is what a service asked for, read from its `pstack.routing.*` labels.
```

with:

```go
	return Challenge(inspect.DetectChallenge(r))
}

// DetectLogging is the push URL deploys ship logs to, "" when this host's logging is off — behind
// a variable for the same reason as DetectChallenge. The default reads the pstack.logging.push-url
// label off the control stack's loki container (inspect.LokiPushURL), so the CLI and the API agree
// with no setting to keep in sync.
var DetectLogging = func(r exec.Runner) string { return inspect.LokiPushURL(r) }

// RoutingRequest is what a service asked for, read from its `pstack.routing.*` labels.
```

- [ ] **Step 4: Implement InjectLogging and the new fields.**

4a. Insert `InjectLogging` between `AugmentComposeDoc` and `MaterializeArgs`. Replace lines 408-411:

```go
	return &AugmentResult{Doc: doc, Generated: generated, Skipped: skipped}, nil
}

// MaterializeArgs is what MaterializeCompose takes.
```

with:

```go
	return &AugmentResult{Doc: doc, Generated: generated, Skipped: skipped}, nil
}

// InjectLogging gives every service without a `logging` key the loki driver block, and returns the
// services that got it and the ones left alone because they have their own, both in file order.
//
// Pure: the document is cloned, as in AugmentComposeDoc. The `service_name=<stack>-<service>`
// label replaces the driver's default container_name, whose swarm value carries the task id and
// would start new streams on every redeploy; `filename` is dropped for the same churn — a busy host
// would reach Loki's stream limit and have new streams refused. The other options are constants on
// purpose (see the package comment).
func InjectLogging(doc *omap.Map, stack, pushURL string) (out *omap.Map, logged, own []string) {
	out = doc.Clone()
	logged, own = []string{}, []string{}
	services := out.GetMap("services")
	for _, name := range services.Keys() {
		svc := services.GetMap(name)
		if svc == nil {
			// A scalar, list or null service has nowhere to put the key; compose reports it better.
			continue
		}
		// Yours wins, entirely: a `logging` key of any shape — json-file, empty, null — opts out,
		// as a traefik.* label does for routing. Nothing is merged: your driver may refuse ours.
		if svc.Has("logging") {
			own = append(own, name)
			continue
		}
		svc.Set("logging", omap.From(
			"driver", "loki",
			"options", omap.From(
				"loki-url", pushURL,
				"loki-external-labels", "service_name="+stack+"-"+name,
				"loki-relabel-config", "[{action: labeldrop, regex: filename}]",
				// Small, so a stop on a node whose Loki is unreachable waits seconds, not minutes.
				"loki-retries", "2",
				"loki-timeout", "1s",
				"loki-max-backoff", "800ms",
				// Protects the app's stdout from a slow push. It does not protect the stop.
				"mode", "non-blocking",
				// `true` leaves one directory per container id in the plugin, forever.
				"keep-file", "false",
				"max-size", "10m",
				"max-file", "3",
			),
		))
		logged = append(logged, name)
	}
	return out, logged, own
}

// MaterializeArgs is what MaterializeCompose takes.
```

4b. The `MaterializeArgs.Challenge` field comment says the field skips the docker probe. Logging discovery now runs anyway, so reword it. Replace lines 416-417:

```go
	// Challenge skips the docker probe when the caller already knows (tests, or a batch).
	Challenge *Challenge
```

with:

```go
	// Challenge skips the challenge probe when the caller already knows (tests, or a batch).
	// Logging discovery still runs — pin DetectLogging to skip that too.
	Challenge *Challenge
```

4c. Replace `MaterializeResult` (lines 420-427):

```go
// MaterializeResult is the filename compose should use and what was done.
type MaterializeResult struct {
	File      string
	Generated *omap.Map
	Skipped   *omap.Map
	// Notes is what the swarm conversion changed, one line each. Empty under compose.
	Notes []string
}
```

with:

```go
// MaterializeResult is the filename compose should use and what was done.
type MaterializeResult struct {
	File      string
	Generated *omap.Map
	Skipped   *omap.Map
	// Notes is what the swarm conversion changed, one line each. Empty under compose.
	Notes []string
	// Logged is the services that got the loki logging block, in file order. Empty, never nil.
	Logged []string
	// LogNotes is one job-log line per service left alone because it has its own `logging:`. Empty,
	// never nil.
	LogNotes []string
}
```

- [ ] **Step 5: Implement the MaterializeCompose changes.**

5a. Replace the doc comment at lines 436-438:

```go
// Returns the filename compose should use — the derived one when anything was generated, and the
// original otherwise, so a deployment that writes its own labels gets exactly the file it submitted
// with no derived artefact left lying around.
```

with:

```go
// Returns the filename compose should use — the derived one when anything was generated or a
// service got the logging block, and the original otherwise, so a deployment that writes its own
// labels gets exactly the file it submitted with no derived artefact left lying around.
```

5b. Replace `untouched` at lines 452-454:

```go
	untouched := func() *MaterializeResult {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: omap.New(), Notes: []string{}}
	}
```

with:

```go
	untouched := func() *MaterializeResult {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: omap.New(), Notes: []string{}, Logged: []string{}, LogNotes: []string{}}
	}
```

5c. Discovery and the pre-check. Replace lines 473-477:

```go
	// Cheap pre-check: no pstack.routing.* anywhere means nothing to generate, and no reason to shell
	// out to docker for the challenge mode. Under swarm the conversion still has to run.
	wantsRouting := strings.Contains(raw, "pstack.routing.")
	if !wantsRouting && !isSwarm {
		return untouched(), nil
	}
```

with:

```go
	// Discovery runs before the pre-check because Loki on its own is a reason to write the derived
	// file — but only once the file parsed, so a missing or broken file still costs no docker call.
	pushURL := DetectLogging(a.Runner)

	// Cheap pre-check: no pstack.routing.* anywhere and no Loki means nothing to generate, and no
	// reason to ask docker for the challenge mode. Under swarm the conversion still has to run.
	wantsRouting := strings.Contains(raw, "pstack.routing.")
	if !wantsRouting && !isSwarm && pushURL == "" {
		return untouched(), nil
	}
```

5d. Injection, the swarm step and the compose early return. Replace lines 498-508:

```go
	notes := []string{}
	if isSwarm {
		// After the labels, so the generated ones are already where the swarm provider reads them and the
		// conversion only has to move what the author wrote by hand.
		converted := swarm.Swarmify(out, a.Spec.Compose.Profiles)
		out = converted.Doc
		notes = converted.Notes
	}
	if !isSwarm && generated.Len() == 0 {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: skipped, Notes: []string{}}, nil
	}
```

with:

```go
	// After the labels and before the swarm conversion: one step for both orchestrators, and
	// Swarmify keeps `logging`.
	logged, logNotes := []string{}, []string{}
	if pushURL != "" {
		var own []string
		out, logged, own = InjectLogging(out, a.Spec.Stack, pushURL)
		for _, svc := range own {
			logNotes = append(logNotes, "logging: service "+svc+" has its own logging: — left alone")
		}
	}
	notes := []string{}
	if isSwarm {
		// After the labels, so the generated ones are already where the swarm provider reads them and the
		// conversion only has to move what the author wrote by hand.
		converted := swarm.Swarmify(out, a.Spec.Compose.Profiles)
		out = converted.Doc
		notes = converted.Notes
	}
	if !isSwarm && generated.Len() == 0 && len(logged) == 0 {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: skipped, Notes: []string{}, Logged: []string{}, LogNotes: logNotes}, nil
	}
```

5e. The written result. Replace line 518:

```go
	return &MaterializeResult{File: generatedRel, Generated: generated, Skipped: skipped, Notes: notes}, nil
```

with:

```go
	return &MaterializeResult{File: generatedRel, Generated: generated, Skipped: skipped, Notes: notes, Logged: logged, LogNotes: logNotes}, nil
```

- [ ] **Step 6: Run it and watch it pass.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ -run TestLoggingInjection -v
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/
```

Expected:
- The first command prints `--- PASS: TestLoggingInjection`, with all 8 subtests passing.
- The second prints `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel`, so every existing test still passes.

- [ ] **Step 7: Run every negative control.** Take the table one row at a time:
  1. Apply the mutation.
  2. Run `go test -race -timeout 120s ./internal/autolabel/ -run TestLoggingInjection`.
  3. Confirm the named subtest FAILS.
  4. Undo the mutation by hand (no git) before the next row.

| Mutation (autolabel.go unless noted) | Subtest that must fail |
|---|---|
| `"service_name="+stack+"-"+name` → `"service_name="+name` | a service without logging gets exactly the fixed block… |
| delete the `if svc.Has("logging") { … }` block | any logging key, json-file or empty, is left byte-unchanged… |
| `out = doc.Clone()` → `out = doc` | the input document is never mutated |
| `if pushURL != "" {` → `if true {` | logging off: no block… |
| remove `&& pushURL == ""` from the pre-check | logging on: a stack with no routing gets the derived file… |
| replace the compose early return's body with `return untouched(), nil` | logging on but every service has its own… |
| add `{"logging", "x"},` to `swarmUnsupported` in swarm.go:163-170 | under swarm the block survives the conversion |
| replace DetectLogging's body with `return ""` | the seam's default reads the control stack's loki container |

After the last undo, `go test -race -timeout 120s ./internal/autolabel/` prints `ok` again.

- [ ] **Step 8: Watch compose's first-command assertion break under the default discovery.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/compose/ -run TestComposeDispatchesOnTheOrchestrator
```

Expected: `--- FAIL: TestComposeDispatchesOnTheOrchestrator/materialize_writes_the_converted_file_beside_the_compose_file,_also_under_the_CLI_(no_cwd)` with `deploy: docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'`. The subtest's own `MaterializeCompose` call now asks docker for the control stack before anything else, and the fake records that call.

- [ ] **Step 9: Pin discovery off for the compose package.** In `packages/pstack/internal/compose/compose_test.go`, replace lines 21-23:

```go
)

func specFrom(t *testing.T, file string, env map[string]string) *spec.Stack {
```

with:

```go
)

// TestMain pins logging discovery off. These tests are about the compose invocation on a Loki-less
// host, and some assert the FIRST command a fake saw — a `docker ps` of the control stack under the
// default discovery. Tests about a logged deploy set the seam themselves.
func TestMain(m *testing.M) {
	autolabel.DetectLogging = func(exec.Runner) string { return "" }
	os.Exit(m.Run())
}

func specFrom(t *testing.T, file string, env map[string]string) *spec.Stack {
```

The file already imports `os`, `testing`, `autolabel` and `exec` (compose_test.go:9-20). TestMain is not a `Test…` function, so it carries no negative-control line; Step 8 serves as its control. Then run:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ ./internal/compose/ ./internal/stack/ ./internal/api/ ./internal/cli/
```

Expected: five `ok` lines. `stack`, `api` and `cli` put no compose file on disk, so `MaterializeCompose` returns at the read, before discovery, and their fakes see no extra `docker ps`.

- [ ] **Step 10: Document it.** Two edits in `docs/control-plane.md`.

10a. Insert the new section between the end of `## 5f. Single sign-on (0.27.0)` and `## 6. Submitting a deployment`. Replace the anchor at lines 978-980:

```markdown
stored, and the protection is the 0700 directory and the 0600 file, as with every other secret here.

## 6. Submitting a deployment
```

with:

```markdown
stored, and the protection is the 0700 directory and the 0600 file, as with every other secret here.

## 5g. Loki logging

`pstack init --logging loki` puts Loki in the control stack and the Loki Docker log plugin on the
node. From then on every deployed service ships its output there. The design and its failure modes
are in [`loki-logging-design.md`](loki-logging-design.md); this section records where it plugs in.

### Discovery: the running control stack is the setting

A deploy never reads a flag to learn whether logging is on. `autolabel.DetectLogging` reads the
`pstack.logging.push-url` label off the control stack's `loki` container (`inspect.LokiPushURL`),
the way the challenge mode is read off Traefik's argv. No container, no injection — so `pstack up`
on the host and the API always inject the same thing, and there is no second copy of the answer to
drift. The lookup is `docker ps -a`, so a restarting Loki does not flip deploys between logged and
not; `pstack logging off` recreates the control stack without the container, and discovery stops
finding it.

### Injection sits in `MaterializeCompose`

After the routing labels and before the swarm conversion: the one step every compose and swarm
invocation already passes through, regenerated per subcommand (§4a). `Swarmify` keeps `logging`.
The control stack never passes through it, so Loki never ships its own output to itself;
`kind: shared` stacks are injected like any other.

Every service **without** a `logging` key gets the loki driver, its push URL and
`service_name=<stack>-<service>`. A service with **any** `logging` key — `json-file` counts — is
left alone and named in the job log: the `traefik.*` opt-out again. Nothing is merged into a user's
block, because pstack's options could be refused by the user's driver.

The rule only sees `compose.file` as parsed. Overlays (`compose.overlays`) are applied after the
derived file, so a `logging:` in an overlay is not skipped — it replaces the injected block (compose
merges the options when the driver is the same or unset). `extends` is not resolved, so a service
that inherits `logging:` through `extends` still gets injected. A `<<: *anchor` merge that brings in
`logging` counts as the service's own: yamlx expands anchors before the check.

Under compose this changes one thing: a stack with no routing labels now runs from
`compose.generated.yml` whenever a service got the block. With logging off the gates are exactly what
they were.

### The options are constants

Only the URL and the label vary. When Loki is unreachable the driver holds a node-wide lock while it
retries, so `docker stop` of **every** container on that node waits out the retry loop
(grafana/loki#2361). `mode: non-blocking` protects the app's stdout, not the stop; only
`loki-retries: "2"`, `loki-timeout: 1s` and `loki-max-backoff: 800ms` bound the wait, to seconds. A
setting would let someone trade that bound away without seeing the cost.

`filename` is dropped, and `service_name` replaces the driver's `container_name`, because both change
per container: every redeploy would open new streams until Loki's 1000-stream limit refused them.
`keep-file: "false"` because `true` leaves one directory per container in the plugin, forever.

### Pushes go through Traefik

The plugin runs in the host's network namespace, so overlay DNS (`http://loki:3100`) is unreachable
from it. `https://loki.<domain>/loki/api/v1/push` with basic auth needs only 443, which every node
already reaches. The alternative, `<manager-ip>:3100`, needs a firewall rule per worker, and pstack
manages no firewall. The router matches only the push path; Loki's query API is reachable from the
`logs` network alone. The cost: the password is visible in `docker inspect` of a preview container
and in `compose.generated.yml` (it can only push), and pushes land only while Traefik is up.

## 6. Submitting a deployment
```

10b. Correct the embed count. After T2, `assets.go` embeds seven files by explicit path: UIHTML, ShareHTML, ControlTemplate, CloudInitTemplate, PackageJSON, OpenAPISpec and LokiConfig. Replace line 1265:

```markdown
`//go:embed`ded (`packages/pstack/assets.go`, five explicit paths — never a glob, so the READMEs
```

with:

```markdown
`//go:embed`ded (`packages/pstack/assets.go`, seven explicit paths — never a glob, so the READMEs
```

- [ ] **Step 11: Final check and commit.** Run the tests first:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ ./internal/compose/ ./internal/stack/ ./internal/api/ ./internal/cli/
```

Expected: five `ok` lines. Then get the file IDs:

```
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these four files. `docs/control-plane.md` carries both the new section and the line-1265 fix.
- `packages/pstack/internal/autolabel/autolabel.go`
- `packages/pstack/internal/autolabel/autolabel_test.go`
- `packages/pstack/internal/compose/compose_test.go`
- `docs/control-plane.md`

Never pick `packages/conformance/golden/host/db/pstack.db-shm` or `pstack.db-wal`. Then commit:

```
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging -m "feat(autolabel): inject the loki logging block into deployed services" <id-autolabel.go> <id-autolabel_test.go> <id-compose_test.go> <id-control-plane.md>
```

---

### Task 9: compose: check the plugin before a logged deploy; name what was skipped

**Files:**
- Modify: `packages/pstack/internal/compose/compose.go:26-29` (package header), `:47-102` (baseFor / fileArgsFor / filesFor), `:130-155` (ComposeUp)
- Modify: `docs/control-plane.md` (one paragraph at the end of the Loki logging section T8 adds)
- Test: `packages/pstack/internal/compose/compose_test.go` (imports `:9-21`; new test appended after `TestMaterializingTheFileThroughCompose`)

**Interfaces:**
- Consumes:
  - `autolabel.MaterializeResult.Logged []string` and `.LogNotes []string` (T8). Both are non-nil on every return path. A LogNotes line is `"logging: service " + svc + " has its own logging: — left alone"`.
  - `var autolabel.DetectLogging = func(r exec.Runner) string` (T8), plus T8's `TestMain` in compose_test.go that pins it to `""`. This task assumes that TestMain is present and adds no second one.
  - `func swarm.MarkLokiPlugins(r exec.Runner, info *swarm.Info)` and `swarm.Node.LokiPlugin *bool` (T7). The command it runs is `docker node inspect --format '{{json .}}' '<id>' '<id>'…`.
  - `const swarm.LokiPluginInstall` (T1). It is one line, 344 runes.
  - Existing: `func swarm.SwarmInfo(r exec.Runner) swarm.Info` (swarm.go:599), `stack.Up`'s step message `firstLine(or(res.Stderr, res.Stdout))`, which `js.Truncate`s to 300 runes (stack.go:110-116, 208-210). stack.Up always passes a non-nil note (stack.go:204): `log.Writer` under the CLI (cli/run.go:148), the job buffer under the API (api/server.go:553).
- Produces:
  - `func filesFor(st *spec.Stack, r exec.Runner) (files []string, m *autolabel.MaterializeResult, err error)`. m is nil under dry-run.
  - `func baseOf(st *spec.Stack, files []string) string` (private). `fileArgsFor` is removed; its only caller was `baseFor`.
  - `func ComposeUp(st *spec.Stack, r exec.Runner, extraEnv map[string]string, note func(string)) (exec.Result, error)`. The signature is unchanged. Job-log lines, in order:
    1. `"swarm: " + m.Notes[i]`
    2. `m.LogNotes[i]`
    3. swarm only: `"logging: could not read the nodes' plugins (docker node inspect did not answer)"` (at most once), then `"logging: node " + hostname + " has no loki log plugin, so swarm keeps logged services off it. On that node: " + swarm.LokiPluginInstall` for each node
    4. compose only, on failure: `"logging: install the loki log plugin on this host: " + swarm.LokiPluginInstall`
  - When the compose plugin check fails, ComposeUp returns `exec.Result{OK: false, Code: 1, Stderr: "the loki log plugin is not installed and enabled on this host — install it (the line is in the log), then deploy again"}`, and `up` never runs.

- [ ] **Step 1: Write the failing test**

In `packages/pstack/internal/compose/compose_test.go`, extend the import block (`:9-21`) with two stdlib packages:

```go
import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)
```

Append this test directly after `TestMaterializingTheFileThroughCompose` (before `TestComposeLogsForOneService`):

```go
func TestLoggedDeploysCheckThePluginFirst(t *testing.T) {
	// What the control stack's loki container carries on a host that ships logs to Loki.
	const pushURL = "https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"
	const cmdPlugin = "docker plugin inspect -f '{{.Enabled}}' loki"
	const deploy = `docker stack deploy -c 'compose.generated.yml' --prune --with-registry-auth --detach=true 's1'`
	// web gets the loki block; db keeps its own.
	const file = "services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    logging: {driver: json-file}\n"

	// logging points discovery at url for one subtest; TestMain's pin comes back after it.
	logging := func(t *testing.T, url string) {
		prev := autolabel.DetectLogging
		autolabel.DetectLogging = func(exec.Runner) string { return url }
		t.Cleanup(func() { autolabel.DetectLogging = prev })
	}
	// host is a deployment directory holding dc.yml — the file plain.yml and swarm.yml name — and a
	// runner answering by exact command; anything else succeeds with no output.
	host := func(t *testing.T, answers map[string]exec.Result) *exec.Fake {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "dc.yml"), []byte(file), 0o666); err != nil {
			t.Fatal(err)
		}
		r := exec.NewFake(nil, "").WithCwd(dir)
		r.Answer = func(cmd string) (exec.Result, bool) {
			res, ok := answers[strings.TrimSpace(cmd)]
			return res, ok
		}
		return r
	}
	// swarmHost is a manager with two nodes; inspect is what `docker node inspect` answers for both.
	swarmHost := func(t *testing.T, inspect exec.Result) *exec.Fake {
		t.Helper()
		return host(t, map[string]exec.Result{
			"docker info --format '{{json .Swarm}}'": {OK: true, Stdout: `{"NodeID":"n1abcdef01234567","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true}`},
			"docker node ls --format '{{json .}}'": {OK: true, Stdout: `{"ID":"n1abcdef01234567","Hostname":"preview-host","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}` + "\n" +
				`{"ID":"n2abcdef01234567","Hostname":"worker-1","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}` + "\n"},
			"docker node inspect --format '{{json .}}' 'n1abcdef01234567' 'n2abcdef01234567'": inspect,
		})
	}
	ran := func(r *exec.Fake, prefix string) bool {
		for _, c := range r.Commands() {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}

	t.Run("compose: no enabled plugin fails before `up`, and the install line goes to the log", func(t *testing.T) {
		// negative control: drop the `return` after the failed plugin check — `docker compose … up` runs.
		logging(t, pushURL)
		// Disabled, and absent (docker exits 1 with nothing on stdout).
		for _, answer := range []exec.Result{{OK: true, Stdout: "false\n"}, {OK: false, Code: 1, Stderr: "Error: No such plugin: loki"}} {
			r := host(t, map[string]exec.Result{cmdPlugin: answer})
			notes := []string{}
			res, err := ComposeUp(specFrom(t, "plain.yml", nil), r, nil, func(l string) { notes = append(notes, l) })
			if err != nil {
				t.Fatal(err)
			}
			if res.OK || res.Code != 1 || !strings.Contains(res.Stderr, "loki log plugin") {
				t.Errorf("result: %+v", res)
			}
			// stack.Up shows the first line cut at 300 runes, so the sentence must fit whole; the install
			// line (344 runes on its own) cannot, which is why it is a note.
			if strings.Contains(res.Stderr, "\n") || utf8.RuneCountInString(res.Stderr) > 300 {
				t.Errorf("the step message would be cut: %q", res.Stderr)
			}
			if !slices.Contains(notes, "logging: install the loki log plugin on this host: "+swarm.LokiPluginInstall) {
				t.Errorf("notes: %v", notes)
			}
			if ran(r, "docker compose") {
				t.Errorf("ran anyway: %v", r.Commands())
			}
		}
	})

	t.Run("compose: an enabled plugin lets `up` run, on the derived file", func(t *testing.T) {
		// negative control: run the plugin check after `up` instead of before — Commands()[0] is the compose line.
		logging(t, pushURL)
		r := host(t, map[string]exec.Result{cmdPlugin: {OK: true, Stdout: "true\n"}})
		must(t)(ComposeUp(specFrom(t, "plain.yml", nil), r, nil, nil))
		want := cmdPlugin + "\n" + "docker compose -p 's1' -f 'compose.generated.yml' --profile 'a' up -d --remove-orphans"
		if got := strings.Join(r.Commands(), "\n"); got != want {
			t.Errorf("commands:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("compose: logging off never asks docker about the plugin", func(t *testing.T) {
		// negative control: drop `len(m.Logged) > 0` from `logged` — `docker plugin inspect` runs and, unanswered, fails the deploy.
		logging(t, "")
		r := host(t, nil)
		must(t)(ComposeUp(specFrom(t, "plain.yml", nil), r, nil, nil))
		if got := strings.Join(r.Commands(), "\n"); got != "docker compose -p 's1' -f 'dc.yml' --profile 'a' up -d --remove-orphans" {
			t.Errorf("commands:\n%s", got)
		}
	})

	t.Run("swarm: each node without the plugin is named with the install line, and the deploy still runs", func(t *testing.T) {
		// negative control: skip swarm.MarkLokiPlugins — every node stays unread and no `node worker-1` line appears.
		logging(t, pushURL)
		r := swarmHost(t, exec.Result{OK: true, Stdout: `{"ID":"n1abcdef01234567","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"}]}}}` + "\n" +
			`{"ID":"n2abcdef01234567","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}` + "\n"})
		notes := []string{}
		must(t)(ComposeUp(specFrom(t, "swarm.yml", nil), r, nil, func(l string) { notes = append(notes, l) }))
		if !slices.Contains(notes, "logging: node worker-1 has no loki log plugin, so swarm keeps logged services off it. On that node: "+swarm.LokiPluginInstall) {
			t.Errorf("notes: %v", notes)
		}
		for _, n := range notes {
			if strings.Contains(n, "preview-host") || strings.Contains(n, "could not read") {
				t.Errorf("note: %s", n)
			}
		}
		if log := r.Commands(); log[len(log)-1] != deploy {
			t.Errorf("commands: %v", log)
		}
	})

	t.Run("swarm: nodes docker would not describe get one line, not one each", func(t *testing.T) {
		// negative control: drop the `break` after the could-not-read note — it appears twice.
		logging(t, pushURL)
		r := swarmHost(t, exec.Result{OK: false, Code: 1, Stderr: "boom"})
		notes := []string{}
		must(t)(ComposeUp(specFrom(t, "swarm.yml", nil), r, nil, func(l string) { notes = append(notes, l) }))
		count := 0
		for _, n := range notes {
			if n == "logging: could not read the nodes' plugins (docker node inspect did not answer)" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("notes: %v", notes)
		}
		if log := r.Commands(); log[len(log)-1] != deploy {
			t.Errorf("commands: %v", log)
		}
	})

	t.Run("a service with its own logging is named in the log", func(t *testing.T) {
		// negative control: stop emitting m.LogNotes in ComposeUp — the db line is missing.
		logging(t, pushURL)
		r := host(t, map[string]exec.Result{cmdPlugin: {OK: true, Stdout: "true\n"}})
		notes := []string{}
		must(t)(ComposeUp(specFrom(t, "plain.yml", nil), r, nil, func(l string) { notes = append(notes, l) }))
		if !slices.Contains(notes, "logging: service db has its own logging: — left alone") {
			t.Errorf("notes: %v", notes)
		}
	})
}
```

(`plain.yml` is stack `s1`, `dc.yml`, profiles `[a]`, compose. `swarm.yml` is the same file under `orchestrator: swarm` with profiles `[a, b]`. The services here carry no `profiles`, so Swarmify keeps both, swarm.go:245. Neither file has `pstack.routing.*`, so no challenge probe runs, and with the seam set, discovery runs no command either.)

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/compose/ -run TestLoggedDeploysCheckThePluginFirst
```

Expected: the file compiles, because it only uses T1/T7/T8 symbols. The run fails:
- `…/compose:_no_enabled_plugin_fails_before_`up`…` fails with `result: {OK:true …}` and `ran anyway: [docker compose -p 's1' -f 'compose.generated.yml' …]`. `up` runs because nothing checks the plugin yet.
- `…/compose:_an_enabled_plugin_lets_`up`_run…` fails with `commands:` showing only the `docker compose … up` line.
- `…/swarm:_each_node_without_the_plugin…` fails with `notes: []`.
- `…/swarm:_nodes_docker_would_not_describe…` fails with `notes: []`.
- `…/a_service_with_its_own_logging…` fails with `notes: []`. The compose branch emits no notes today.
- `…/compose:_logging_off_never_asks…` PASSES. It guards behaviour that already holds, and Step 5 runs its negative control.
- The run ends with `FAIL	github.com/samishal1998/preview-stacks/packages/pstack/internal/compose`.

- [ ] **Step 3: Implement**

**3a. Package header.** In `packages/pstack/internal/compose/compose.go`, replace lines 26-29:

```go
// Every verb returns an error only when the derived compose file could not be produced (a
// *spec.Error from autolabel — the TS threw the same); a command that ran and failed is a Result
// with OK false, never an error.
package compose
```

with:

```go
// Every verb returns an error only when the derived compose file could not be produced (a
// *spec.Error from autolabel — the TS threw the same); a command that ran and failed is a Result
// with OK false, never an error.
//
// Logging: on a host that ships logs to Loki, `up` checks the plugin before it deploys anything
// autolabel gave the loki driver. Under compose a missing or disabled plugin fails every logged
// container's create with dockerd's error, which names the driver and not the fix — so the check
// answers a Result with OK false (the rule above) and `up` never runs. Under swarm the scheduler
// keeps logged tasks off a node without the plugin, so the deploy runs and the log names those
// nodes; if none qualifies, readiness times out and those lines are the only thing saying why. The
// install line always goes to `note`, never into the Result: stack.Up keeps the first 300 runes of
// the Result's first line, and the line alone is longer.
package compose
```

**3b. One materialization, readable by ComposeUp.** Replace lines 47-102, from `// baseFor is the prefix every subcommand shares.` through the closing `}` of `filesFor`:

```go
// baseFor is the prefix every subcommand shares.
//
// `-p <stack>` is the namespacing primitive: it prefixes containers, networks and volumes, so two
// stacks from the same compose file never collide. The `-f` list comes from fileArgsFor, which
// substitutes the augmented file when the submitted one asked for generated labels.
func baseFor(st *spec.Stack, r exec.Runner) (string, error) {
	files, err := fileArgsFor(st, r)
	if err != nil {
		return "", err
	}
	return "docker compose -p " + Shq(st.Stack) + " " + files, nil
}

// fileArgsFor resolves which compose file to pass to `-f`, generating the augmented one when the
// submitted file asks for it with `pstack.routing.*` labels.
//
// Called by EVERY subcommand, not just `up`. The generated labels are derived from the resolved spec,
// so regenerating each time is what stops `up` and `down` disagreeing about what a router was called —
// and compose reads the file on every subcommand anyway.
//
// `runner.Cwd()` is the deployment directory (the registry sets it); the CLI sets none and docker runs
// from the shell's directory, which is then where the compose file is read from and the derived one
// written (beside it — see autolabel.MaterializeCompose).
func fileArgsFor(st *spec.Stack, r exec.Runner) (string, error) {
	files, _, err := filesFor(st, r)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = "-f " + Shq(f)
	}
	return strings.Join(parts, " "), nil
}

// filesFor is the compose files to pass, in order, plus what the swarm conversion changed (for the
// job log).
func filesFor(st *spec.Stack, r exec.Runner) (files, notes []string, err error) {
	c := st.Compose
	// Nothing is written under --dry-run: a dry run must not have side effects, and the point of it is
	// to show what WOULD happen.
	if r.DryRun() {
		return append([]string{c.File}, c.Overlays...), []string{}, nil
	}
	// The deployment directory under the API; the shell's directory under the CLI — which is also
	// where docker itself will resolve the `-f` path, so the two cannot disagree.
	dir := r.Cwd()
	if dir == "" {
		dir, _ = os.Getwd()
	}
	m, err := autolabel.MaterializeCompose(autolabel.MaterializeArgs{Dir: dir, Spec: st, Runner: r})
	if err != nil {
		return nil, nil, err
	}
	return append([]string{m.File}, c.Overlays...), m.Notes, nil
}
```

with:

```go
// baseFor is the prefix every subcommand shares.
//
// `-p <stack>` is the namespacing primitive: it prefixes containers, networks and volumes, so two
// stacks from the same compose file never collide. The `-f` list comes from filesFor, which
// substitutes the augmented file when the submitted one asked for generated labels.
func baseFor(st *spec.Stack, r exec.Runner) (string, error) {
	files, _, err := filesFor(st, r)
	if err != nil {
		return "", err
	}
	return baseOf(st, files), nil
}

// baseOf is baseFor over files already resolved. ComposeUp resolves them itself, once, because it
// also reads what the materialization did.
func baseOf(st *spec.Stack, files []string) string {
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = "-f " + Shq(f)
	}
	return "docker compose -p " + Shq(st.Stack) + " " + strings.Join(parts, " ")
}

// filesFor resolves which compose files to pass to `-f`, in order, generating the augmented one when
// the submitted file needs it: `pstack.routing.*` labels, swarm, or a host that ships logs to Loki.
// m is what the materialization did, for the job log; nil under --dry-run.
//
// Called by EVERY subcommand, not just `up`. The generated labels are derived from the resolved spec,
// so regenerating each time is what stops `up` and `down` disagreeing about what a router was called —
// and compose reads the file on every subcommand anyway.
//
// `runner.Cwd()` is the deployment directory (the registry sets it); the CLI sets none and docker runs
// from the shell's directory, which is then where the compose file is read from and the derived one
// written (beside it — see autolabel.MaterializeCompose).
func filesFor(st *spec.Stack, r exec.Runner) (files []string, m *autolabel.MaterializeResult, err error) {
	c := st.Compose
	// Nothing is written under --dry-run: a dry run must not have side effects, and the point of it is
	// to show what WOULD happen.
	if r.DryRun() {
		return append([]string{c.File}, c.Overlays...), nil, nil
	}
	// The deployment directory under the API; the shell's directory under the CLI — which is also
	// where docker itself will resolve the `-f` path, so the two cannot disagree.
	dir := r.Cwd()
	if dir == "" {
		dir, _ = os.Getwd()
	}
	m, err = autolabel.MaterializeCompose(autolabel.MaterializeArgs{Dir: dir, Spec: st, Runner: r})
	if err != nil {
		return nil, nil, err
	}
	return append([]string{m.File}, c.Overlays...), m, nil
}
```

**3c. ComposeUp.** Replace lines 130-155, from `// ComposeUp is \`compose up\` / \`stack deploy\`.` through its closing `}`:

```go
// ComposeUp is `compose up` / `stack deploy`. note receives the swarm conversion's lines; nil for the CLI,
// which has no job log.
func ComposeUp(st *spec.Stack, r exec.Runner, extraEnv map[string]string, note func(string)) (exec.Result, error) {
	c := st.Compose
	if isSwarm(st) {
		files, notes, err := filesFor(st, r)
		if err != nil {
			return exec.Result{}, err
		}
		if note != nil {
			for _, n := range notes {
				note("swarm: " + n)
			}
		}
		// `--prune` is `--remove-orphans`; profiles were resolved into the file by the conversion.
		return r.Run(swarm.StackDeployCmd(st.Stack, files), exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "stack deploy"}), nil
	}
	base, err := baseFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	cmd := base + " " + profileArgs(c.Profiles) + " up -d --remove-orphans"
	// --remove-orphans drops services that were in a previous deploy but are not selected now, so a
	// relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
	return r.Run(cmd, exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "compose up"}), nil
}
```

with:

```go
// ComposeUp is `compose up` / `stack deploy`. note receives the job-log lines: what the swarm
// conversion changed, which services kept their own `logging:`, and what the loki plugin check found.
// nil drops them.
func ComposeUp(st *spec.Stack, r exec.Runner, extraEnv map[string]string, note func(string)) (exec.Result, error) {
	c := st.Compose
	files, m, err := filesFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	if note == nil {
		note = func(string) {}
	}
	// m is nil under --dry-run: nothing was materialized, so nothing is said and no plugin is checked.
	logged := m != nil && len(m.Logged) > 0
	if m != nil {
		for _, n := range m.Notes {
			note("swarm: " + n)
		}
		for _, n := range m.LogNotes {
			note(n)
		}
	}
	if isSwarm(st) {
		if logged {
			// A node whose plugins could not be read is unknown, not a node without the plugin.
			info := swarm.SwarmInfo(r)
			swarm.MarkLokiPlugins(r, &info)
			for _, n := range info.Nodes {
				if n.LokiPlugin == nil {
					note("logging: could not read the nodes' plugins (docker node inspect did not answer)")
					break
				}
			}
			for _, n := range info.Nodes {
				if n.LokiPlugin != nil && !*n.LokiPlugin {
					note("logging: node " + n.Hostname + " has no loki log plugin, so swarm keeps logged services off it. On that node: " + swarm.LokiPluginInstall)
				}
			}
		}
		// `--prune` is `--remove-orphans`; profiles were resolved into the file by the conversion.
		return r.Run(swarm.StackDeployCmd(st.Stack, files), exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "stack deploy"}), nil
	}
	if logged {
		// Anything but `true` — absent, disabled, docker not answering — fails every logged create alike.
		res := r.Run("docker plugin inspect -f '{{.Enabled}}' loki", exec.RunOptions{Label: "loki plugin"})
		if strings.TrimSpace(res.Stdout) != "true" {
			note("logging: install the loki log plugin on this host: " + swarm.LokiPluginInstall)
			return exec.Result{OK: false, Code: 1, Stderr: "the loki log plugin is not installed and enabled on this host — install it (the line is in the log), then deploy again"}, nil
		}
	}
	cmd := baseOf(st, files) + " " + profileArgs(c.Profiles) + " up -d --remove-orphans"
	// --remove-orphans drops services that were in a previous deploy but are not selected now, so a
	// relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
	return r.Run(cmd, exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "compose up"}), nil
}
```

The imports are unchanged (`os`, `strings`, autolabel, exec, spec, swarm are already there). `ComposeDown`, `ComposeSleep`, `ComposeLogsCommand` and `ComposePs` still call `baseFor` and ignore m. They never check the plugin: a stop or a read does not create a logged container.

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/compose/ -run TestLoggedDeploysCheckThePluginFirst -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL|PASS)'
```

Expected: all six subtests show `--- PASS`, and the run ends with `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/compose`.

Then the neighbours (existing tests unchanged):

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go vet ./internal/compose/ && go test -race -timeout 120s ./internal/compose/ ./internal/stack/ ./internal/api/
```

Expected output is three `ok` lines. Some existing tests matter here:
- The dry-run subtest (compose_test.go:328) still names `-f 'docker-compose.yml'`: m is nil, so no plugin check runs.
- The materialize subtest (:222) still sees `Commands()[0]` as the stack deploy, and `notes[0]` still starts with `swarm: service app: `. m.Notes are emitted before m.LogNotes, and T8's TestMain keeps discovery off.
- `TestComposeInvocation` still sees its label list `compose ps|compose up|…`.

- [ ] **Step 5: Run every negative control** (AGENTS.md Go rule 17)

Make each mutation in `compose.go`, then run:

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/compose/ -run TestLoggedDeploysCheckThePluginFirst
```

Confirm the named subtest FAILS, then undo the mutation before the next one.

1. Delete `return exec.Result{OK: false, …}, nil` after the plugin note. `compose:_no_enabled_plugin…` fails with `ran anyway`.
2. Move the `if logged { … plugin inspect … }` block below `cmd := …` and run `r.Run(cmd, …)` before it. Run the check, then return the up result. `compose:_an_enabled_plugin…` fails, because `Commands()[0]` is the compose line.
3. Change `logged := m != nil && len(m.Logged) > 0` to `logged := m != nil`. `compose:_logging_off…` fails, because `commands:` starts with `docker plugin inspect`.
4. Delete `swarm.MarkLokiPlugins(r, &info)`. `swarm:_each_node…` fails with `notes: [logging: could not read …]`.
5. Delete the `break` after the could-not-read note. `swarm:_nodes_docker_would_not_describe…` fails, because the note appears twice.
6. Delete the `for _, n := range m.LogNotes` loop. `a_service_with_its_own_logging…` fails with `notes:` lacking the db line.

After the last revert, rerun Step 4's two commands. Both must be green.

- [ ] **Step 6: Document the deploy-time checks**

In `docs/control-plane.md`, find the Loki logging section T8 added (the `## ` heading containing `Loki logging`). Append this paragraph at the end of that section, directly before the next `## ` heading:

```markdown
**Plugin checks at deploy.** A deploy that gave at least one service the loki block checks the plugin
before it runs; a dry run checks nothing. Under compose, `docker plugin inspect -f '{{.Enabled}}' loki`
must print `true`. Anything else fails the compose step before `up` with one sentence, and the install
line goes to the job log: a step message keeps 300 characters, and the line is longer. Under swarm the
deploy always runs. The scheduler keeps logged tasks off a node whose engine has no `Log` plugin named
`loki`/`loki:latest`, so the job log names each such node with the install line. If no node qualifies,
readiness times out after 180s and that line is the reason. A service that kept its own `logging:` is
named in the job log too.
```

- [ ] **Step 7: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of `packages/pstack/internal/compose/compose.go`, `packages/pstack/internal/compose/compose_test.go` and `docs/control-plane.md`. Take nothing under `packages/conformance/golden/host/`.

```bash
cd /Volumes/S1/code/preview-stacks && but commit -b claude/loki-logging -m "feat(compose): check the loki plugin before a logged deploy" <compose.go id> <compose_test.go id> <control-plane.md id>
```

---

### Task 10: swarm + cloudinit: join material installs the plugin when logging is on

**Files:**
- Modify: `packages/pstack/internal/swarm/swarm.go:780-795` (CloudInit seam + JoinArgs), `:844-847` (JoinMaterial switch), `:952-972` (JoinScript)
- Modify: `packages/pstack/internal/cloudinit/cloudinit.go:36-48` (init seam), `:748-841` (WorkerAnswers, RenderWorkerCloudInit)
- Modify: `packages/pstack/internal/cli/run.go:27-28` (import), `:640` (swarm join; line numbers drift after T5/T6, but the text is unchanged)
- Modify: `packages/pstack/internal/api/routes.go:200` (`/api/swarm/join`; `inspect` is already imported at routes.go:14)
- Modify: `docs/usage.md:2022-2024` (Swarm mode, adding a worker)
- Test: `packages/pstack/internal/swarm/swarm_test.go` (TestPstackSwarmTheCLIHalf, swarm_test.go:377-540)
- Test: `packages/pstack/internal/cloudinit/cloudinit_test.go` (TestWorkerCloudInit, cloudinit_test.go:576-613)

**Interfaces:**
- Consumes: `swarm.LokiVersion` (T1, `const LokiVersion = "3.7.7"`), `swarm.LokiPluginInstall` (T1, one POSIX-sh line, no trailing newline), `inspect.LokiPushURL(r exec.Runner) string` (T7)
- Produces:
  - `func JoinScript(token, managerAddr string, logging bool) string` (package swarm)
  - `type JoinArgs struct { Runner exec.Runner; Format string; Distro *string; Logging bool }` (package swarm)
  - `var CloudInit struct { Distros []string; Render func(token, managerAddr, distro string, logging bool) string }` (package swarm)
  - `type WorkerAnswers struct { Token, ManagerAddr, Distro, SSHKey string; Logging bool }` (package cloudinit)
  - callers: `swarm.JoinArgs{…, Logging: inspect.LokiPushURL(runner) != ""}` in cli run.go, `swarm.JoinArgs{…, Logging: inspect.LokiPushURL(s.host) != ""}` in api routes.go

Facts this task relies on, checked against the tree:
- No CLI or API Go test pins the exact command list for swarm join. `internal/api/permissions_test.go:101,324,357` only checks gate status. So the extra discovery `docker ps` in both callers breaks no existing assertion.
- The conformance docker shim answers unknown argv with `*) exit 0` and prints nothing (`packages/conformance/harness/docker-shim.ts:31`). So `LokiPushURL` gets no container ids and returns `""`, and every `swarm-join-*` golden and the existing `/api/swarm/join` test stay byte-identical.
- `go list -deps ./internal/inspect` has no `internal/cli`, and `./internal/swarm` has no `internal/inspect`. The new `cli → inspect` import adds no cycle, and swarm stays the leaf.
- The shell semantics were checked in bash with the contract's `LokiPluginInstall` literal and a fake docker whose `plugin *` exits 1. Wrapped `{ …; } || echo …`: exit 0, and the log ends `info --format {{.Swarm.LocalNodeState}}` / `swarm join --token T 1.2.3.4:2377`. Unwrapped: `exit=1`, and the log stops at `plugin install grafana/loki-docker-driver:3.7.7-amd64 …`.

- [ ] **Step 1: Write the failing swarm tests**

In `packages/pstack/internal/swarm/swarm_test.go`, subtest `join material: every format, from the one function the API route also calls` (swarm_test.go:428), change the replacement Render to the new signature. Replace:

```go
		CloudInit.Render = func(token, managerAddr, distro string) string {
			rendered = append(rendered, distro)
			return "#cloud-config\n# " + distro + "\n" + JoinCommand(token, managerAddr) + "\n"
		}
```

with:

```go
		CloudInit.Render = func(token, managerAddr, distro string, logging bool) string {
			rendered = append(rendered, distro)
			return "#cloud-config\n# " + distro + "\n" + JoinCommand(token, managerAddr) + "\n"
		}
```

In subtest `the join script is the fixed transcript` (swarm_test.go:512), replace:

```go
		if got := JoinScript("T", "1.2.3.4:2377"); got != want {
```

with:

```go
		if got := JoinScript("T", "1.2.3.4:2377", false); got != want {
```

Then add two subtests at the end of `TestPstackSwarmTheCLIHalf`, right after the closing `})` of `the join script is the fixed transcript` and before the function's final `}`:

```go
	t.Run("join material hands logging to the script and the cloud-config, not to token or command", func(t *testing.T) {
		// negative control: pass `false` instead of a.Logging to CloudInit.Render in JoinMaterial — the
		// cloud-config logging=true check fails.
		prev := CloudInit
		defer func() { CloudInit = prev }()
		var logged []bool
		CloudInit.Distros = []string{"ubuntu"}
		CloudInit.Render = func(token, managerAddr, distro string, logging bool) string {
			logged = append(logged, logging)
			return "#cloud-config\n"
		}
		r := manager(t)
		for _, on := range []bool{false, true} {
			if m := JoinMaterial(JoinArgs{Runner: r, Format: "cloud-config", Logging: on}); !m.OK || logged[len(logged)-1] != on {
				t.Errorf("cloud-config logging=%v: rendered with %v", on, logged)
			}
			if m := JoinMaterial(JoinArgs{Runner: r, Format: "script", Logging: on}); !m.OK || strings.Contains(m.Text, LokiPluginInstall) != on {
				t.Errorf("script logging=%v:\n%s", on, m.Text)
			}
			if m := JoinMaterial(JoinArgs{Runner: r, Format: "command", Logging: on}); m.Text != "docker swarm join --token SWMTKN-1-abcdef-ghijkl 10.0.0.1:2377\n" {
				t.Errorf("command logging=%v: %q", on, m.Text)
			}
		}
	})

	t.Run("with logging on, the join script installs the loki plugin first, and a failed install still joins", func(t *testing.T) {
		// negative control: drop the `{ …; } || echo …` wrapper in JoinScript (leave LokiPluginInstall
		// bare) — `set -e` aborts at the failing `plugin install`, bash exits 1, and the fake docker
		// never sees `swarm join`.
		script := JoinScript("T", "1.2.3.4:2377", true)
		enable := strings.Index(script, "systemctl enable --now docker")
		plugin := strings.Index(script, LokiPluginInstall)
		already := strings.Index(script, "already part of a swarm")
		join := strings.Index(script, "docker swarm join --token T 1.2.3.4:2377")
		// Before the "already part of a swarm" exit: re-running the script on a worker that joined
		// earlier is how that worker gets the plugin.
		if enable < 0 || plugin < enable || already < plugin || join < already {
			t.Fatalf("plugin line out of place:\n%s", script)
		}
		if strings.Contains(JoinScript("T", "1.2.3.4:2377", false), "loki") {
			t.Error("logging off carries the plugin line")
		}

		bash, err := osexec.LookPath("bash")
		if err != nil {
			t.Skip("no bash")
		}
		dir := t.TempDir()
		calls := filepath.Join(dir, "calls.log")
		// Every plugin command fails — the case the wrapper exists for. PATH is ONLY dir, so no real
		// systemctl, curl or docker can run, and `command -v docker` finds the fake.
		for _, f := range []struct{ name, body string }{
			{"docker", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\ncase \"$*\" in\n  plugin*) exit 1 ;;\n  \"info --format {{.Swarm.LocalNodeState}}\") printf '%s\\n' inactive ;;\nesac\nexit 0\n"},
			{"uname", "#!/bin/sh\nprintf '%s\\n' x86_64\n"},
		} {
			if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := osexec.Command(bash, "-c", script)
		cmd.Env = []string{"PATH=" + dir}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the script did not finish: %v\n%s", err, out)
		}
		log, _ := os.ReadFile(calls)
		if !strings.Contains(string(log), "plugin install grafana/loki-docker-driver:"+LokiVersion+"-amd64") || !strings.Contains(string(log), "swarm join --token T 1.2.3.4:2377") {
			t.Errorf("docker calls:\n%s", log)
		}
		if !strings.Contains(string(out), "loki plugin not installed: logged services will not run here") {
			t.Errorf("output:\n%s", out)
		}
	})
```

(`os`, `osexec`, `filepath` and `strings` are already imported at swarm_test.go:9-13.)

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ -run TestPstackSwarmTheCLIHalf
```

Expected: a build failure. Among the errors: `too many arguments in call to JoinScript`, `unknown field Logging in struct literal of type JoinArgs`, and `cannot use func(token, managerAddr, distro string, logging bool) string {…} … as func(token string, managerAddr string, distro string) string value in assignment`. Result: `FAIL github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm [build failed]`.

- [ ] **Step 3: Implement in swarm.go**

Replace the seam and JoinArgs (swarm.go:780-795):

```go
// CloudInit is the seam cloudinit fills at init (cloudinit imports this package for JoinCommand and
// the port table, so the call back cannot be an import — the TS used a dynamic import for the same
// reason). Distros is cloudinit's own list, the one `distro` is validated against; Render is
// renderWorkerCloudInit. Until it is filled, every `cloud-config` request is a bad-distro refusal.
var CloudInit struct {
	Distros []string
	Render  func(token, managerAddr, distro string) string
}

// JoinArgs is what JoinMaterial takes.
type JoinArgs struct {
	Runner exec.Runner
	Format string
	// Distro is only read for `cloud-config`; nil means ubuntu, "" is refused (the API's `?distro=`).
	Distro *string
}
```

with:

```go
// CloudInit is the seam cloudinit fills at init (cloudinit imports this package for JoinCommand and
// the port table, so the call back cannot be an import — the TS used a dynamic import for the same
// reason). Distros is cloudinit's own list, the one `distro` is validated against; Render is
// renderWorkerCloudInit, `logging` its WorkerAnswers.Logging. Until it is filled, every
// `cloud-config` request is a bad-distro refusal.
var CloudInit struct {
	Distros []string
	Render  func(token, managerAddr, distro string, logging bool) string
}

// JoinArgs is what JoinMaterial takes.
type JoinArgs struct {
	Runner exec.Runner
	Format string
	// Distro is only read for `cloud-config`; nil means ubuntu, "" is refused (the API's `?distro=`).
	Distro *string
	// Logging is whether this host ships logs to Loki: `script` and `cloud-config` then install the
	// loki plugin before joining. Callers read it with inspect.LokiPushURL — the way deploys do —
	// because swarm is the leaf and cannot import inspect.
	Logging bool
}
```

In JoinMaterial (swarm.go:844-847), replace:

```go
	case "script":
		text = JoinScript(token, addr)
	default:
		text = CloudInit.Render(token, addr, distro)
```

with:

```go
	case "script":
		text = JoinScript(token, addr, a.Logging)
	default:
		text = CloudInit.Render(token, addr, distro, a.Logging)
```

Replace the whole JoinScript (swarm.go:952-972):

```go
// JoinScript is a shell script that installs Docker (the vendor convenience script — the same one
// every quickstart uses; distro-exact installs are the cloud-config's job) and joins. `set -e` so a
// failed install never reaches the join with nothing to join.
func JoinScript(token, managerAddr string) string {
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"# Join this machine to the pstack swarm as a WORKER. Run as root (or with sudo).",
		"# Open " + PortList() + " between this machine and the manager first.",
		"set -euo pipefail",
		"if ! command -v docker >/dev/null 2>&1; then",
		"  curl -fsSL https://get.docker.com | sh",
		"fi",
		"systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true",
		`if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]; then`,
		`  echo "already part of a swarm: $(docker info --format '{{.Swarm.NodeID}}')"; exit 0`,
		"fi",
		JoinCommand(token, managerAddr),
		"docker info --format 'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})'",
		"",
	}, "\n")
}
```

with:

```go
// JoinScript is a shell script that installs Docker (the vendor convenience script — the same one
// every quickstart uses; distro-exact installs are the cloud-config's job) and joins. `set -e` so a
// failed install never reaches the join with nothing to join.
//
// With logging on, the loki plugin line is the one step allowed to fail: `{ …; } || echo` turns
// `set -e` off inside the braces, so a worker without the plugin still joins and swarm keeps logged
// services off it. It sits BEFORE the "already part of a swarm" exit, so re-running the script on a
// worker that joined earlier installs the plugin too. Logging off is byte-identical to before.
func JoinScript(token, managerAddr string, logging bool) string {
	lines := []string{
		"#!/usr/bin/env bash",
		"# Join this machine to the pstack swarm as a WORKER. Run as root (or with sudo).",
		"# Open " + PortList() + " between this machine and the manager first.",
		"set -euo pipefail",
		"if ! command -v docker >/dev/null 2>&1; then",
		"  curl -fsSL https://get.docker.com | sh",
		"fi",
		"systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true",
	}
	if logging {
		lines = append(lines, "{ "+LokiPluginInstall+"; } || echo \"loki plugin not installed: logged services will not run here\"")
	}
	return strings.Join(append(lines,
		`if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]; then`,
		`  echo "already part of a swarm: $(docker info --format '{{.Swarm.NodeID}}')"; exit 0`,
		"fi",
		JoinCommand(token, managerAddr),
		"docker info --format 'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})'",
		"",
	), "\n")
}
```

- [ ] **Step 4: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm`. The swarm package compiles on its own. cloudinit, cli and api don't compile until Step 7.

- [ ] **Step 5: Write the failing cloudinit test**

In `packages/pstack/internal/cloudinit/cloudinit_test.go`, subtest `fills swarm's CloudInit seam at init` (cloudinit_test.go:577), replace:

```go
		if got := swarm.CloudInit.Render("SWMTKN-1-abc-def", "10.0.0.1:2377", "ubuntu"); !strings.Contains(got, "docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377") {
```

with:

```go
		if got := swarm.CloudInit.Render("SWMTKN-1-abc-def", "10.0.0.1:2377", "ubuntu", false); !strings.Contains(got, "docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377") {
```

Add this subtest at the end of `TestWorkerCloudInit`, after the closing `})` of `renders the join for the manager, valid YAML, with the distro's docker install` and before the function's final `}`:

```go
	t.Run("with logging on, the loki plugin step sits between docker and the join, and cannot block it", func(t *testing.T) {
		// negative control: append the plugin step after the JoinCommand line instead of before the
		// "2. Join" header — the runcmd order check fails.
		on := WorkerAnswers{Token: "SWMTKN-1-abc-def", ManagerAddr: "10.0.0.1:2377", Distro: "debian", Logging: true}
		out, err := RenderWorkerCloudInit(on)
		if err != nil {
			t.Fatal(err)
		}
		if !yamlOK(t, out) {
			t.Fatalf("not valid YAML:\n%s", out)
		}
		// Parsed, like initCall: the step must be a runcmd ITEM between the two, not text that happens
		// to sit between them.
		cmds := runcmd(t, out)
		docker := indexOf(cmds, func(c string) bool { return c == "docker --version" })
		plugin := indexOf(cmds, func(c string) bool { return strings.Contains(c, swarm.LokiPluginInstall) })
		join := indexOf(cmds, func(c string) bool { return strings.HasPrefix(c, "docker swarm join ") })
		if docker < 0 || plugin < 0 || join < 0 || !(docker < plugin && plugin < join) {
			t.Fatalf("runcmd order: docker %d, plugin %d, join %d\n%v", docker, plugin, join, cmds)
		}
		if !strings.Contains(cmds[plugin], `; } || echo "loki plugin not installed: logged services will not run here"`) {
			t.Errorf("plugin step can block the join: %q", cmds[plugin])
		}

		off := on
		off.Logging = false
		plain, err := RenderWorkerCloudInit(off)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(plain, "loki") {
			t.Errorf("logging off mentions loki:\n%s", plain)
		}
	})
```

- [ ] **Step 6: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/cloudinit/ -run TestWorkerCloudInit
```

Expected: a build failure. cloudinit.go:41 no longer matches the seam (`cannot use func(token, managerAddr, distro string) string {…} … as func(token string, managerAddr string, distro string, logging bool) string value in assignment`), and the test has `unknown field Logging in struct literal of type WorkerAnswers`. Result: `FAIL … internal/cloudinit [build failed]`.

- [ ] **Step 7: Implement in cloudinit.go and the two callers**

In `cloudinit.go` init() (cloudinit.go:41-42), replace:

```go
	swarm.CloudInit.Render = func(token, managerAddr, distro string) string {
		out, err := RenderWorkerCloudInit(WorkerAnswers{Token: token, ManagerAddr: managerAddr, Distro: distro})
```

with:

```go
	swarm.CloudInit.Render = func(token, managerAddr, distro string, logging bool) string {
		out, err := RenderWorkerCloudInit(WorkerAnswers{Token: token, ManagerAddr: managerAddr, Distro: distro, Logging: logging})
```

Replace WorkerAnswers (cloudinit.go:748-754):

```go
// WorkerAnswers are RenderWorkerCloudInit's inputs.
type WorkerAnswers struct {
	Token       string
	ManagerAddr string
	Distro      string
	SSHKey      string
}
```

with:

```go
// WorkerAnswers are RenderWorkerCloudInit's inputs.
type WorkerAnswers struct {
	Token       string
	ManagerAddr string
	Distro      string
	SSHKey      string
	// Logging adds the loki plugin step between Docker and the join.
	Logging bool
}
```

Replace the first three lines of RenderWorkerCloudInit's doc comment (cloudinit.go:756-758):

```go
// RenderWorkerCloudInit is the user-data for a swarm WORKER: install Docker, join the manager.
// Nothing else — no Bun, no pstack, no control stack; a worker runs tasks the manager schedules
// and is managed from the manager.
```

with:

```go
// RenderWorkerCloudInit is the user-data for a swarm WORKER: install Docker, join the manager.
// Nothing else — no Bun, no pstack, no control stack; a worker runs tasks the manager schedules
// and is managed from the manager.
//
// The one optional step is Logging's loki plugin, between Docker and the join. Its section is
// UNNUMBERED so the logging-off file stays byte-identical, and it is a `|` literal block because the
// line starts with `{` and its echo carries `: ` — as a plain scalar YAML reads a flow mapping.
// `{ …; } || echo` so a failed install is a line in cloud-init's log, never a worker that did not join.
```

In the runcmd section (cloudinit.go:823-831), replace:

```go
	lines = append(lines,
		"",
		"runcmd:",
		"  # ── 1. Docker ───────────────────────────────────────────────────────────────────────────",
		profile.pkgSetup,
		profile.dockerEnable,
		"  - docker --version",
		"  # ── 2. Join ─────────────────────────────────────────────────────────────────────────────",
```

with:

```go
	lines = append(lines,
		"",
		"runcmd:",
		"  # ── 1. Docker ───────────────────────────────────────────────────────────────────────────",
		profile.pkgSetup,
		profile.dockerEnable,
		"  - docker --version",
	)
	if a.Logging {
		lines = append(lines,
			"  # ── Loki log plugin ─────────────────────────────────────────────────────────────────────",
			"  - |",
			"    { "+swarm.LokiPluginInstall+"; } || echo \"loki plugin not installed: logged services will not run here\"",
		)
	}
	lines = append(lines,
		"  # ── 2. Join ─────────────────────────────────────────────────────────────────────────────",
```

(The rest of that append, from `"  - "+swarm.JoinCommand(a.Token, a.ManagerAddr),` through `"",` and `)`, is unchanged. All three headers are 92 runes.)

In `packages/pstack/internal/cli/run.go`, add the import between initctl and js (run.go:27-28). Replace:

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
```

with:

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
```

In swarmCmd's `join` case (run.go:640 before T5/T6), replace:

```go
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: runner, Format: args.Format, Distro: distro})
```

with:

```go
		// Logging is read the way deploys read it, so this and GET /api/swarm/join hand out the same material.
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: runner, Format: args.Format, Distro: distro, Logging: inspect.LokiPushURL(runner) != ""})
```

In `packages/pstack/internal/api/routes.go` (routes.go:200), replace:

```go
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: s.host, Format: format, Distro: distro})
```

with:

```go
		// Logging is read the way deploys read it, so this and `pstack swarm join` hand out the same material.
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: s.host, Format: format, Distro: distro, Logging: inspect.LokiPushURL(s.host) != ""})
```

- [ ] **Step 8: Run it and watch it pass**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/swarm/ ./internal/cloudinit/ ./internal/cli/ ./internal/api/ && gofmt -l internal/swarm internal/cloudinit internal/cli internal/api
```

Expected: four `ok` lines (swarm, cloudinit, cli, api) and no gofmt output. TestCloudInitGoldens and the fixed JoinScript transcript pass unchanged.

- [ ] **Step 9: Run each negative control (Go rule 17), then revert it**

Make each mutation, run the command, confirm the failure, then undo the mutation exactly:
1. swarm.go JoinScript: replace `"{ "+LokiPluginInstall+"; } || echo \"loki plugin not installed: logged services will not run here\""` with `LokiPluginInstall`. Run `go test -race -timeout 120s ./internal/swarm/ -run 'TestPstackSwarmTheCLIHalf/with_logging_on'`. Expected: `the script did not finish: exit status 1`.
2. swarm.go JoinMaterial: `CloudInit.Render(token, addr, distro, a.Logging)` → `CloudInit.Render(token, addr, distro, false)`. Run `go test -race -timeout 120s ./internal/swarm/ -run 'TestPstackSwarmTheCLIHalf/join_material_hands_logging'`. Expected: `cloud-config logging=true: rendered with [false false]`.
3. cloudinit.go: move the `if a.Logging { … }` block below the final `lines = append(…)`, so the plugin step follows the join. Run `go test -race -timeout 120s ./internal/cloudinit/ -run 'TestWorkerCloudInit/with_logging_on'`. Expected: `runcmd order: docker 7, plugin -1, join 8` (or plugin after join). The step now lands after `final_message`, outside runcmd, so it is not found.

After reverting all three, re-run Step 8's command: green.

- [ ] **Step 10: Conformance — logging-off join material is byte-identical**

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts test/api-share-sleep-swarm.test.ts
```

Expected: `0 fail`. `swarm-join-command|script|cloud-config|token|cloud-config-alpine|bad-format|not-manager` match their goldens: the shim's `*) exit 0` answers `ps -aq --filter label=com.docker.compose.project=pstack-control` with nothing, so Logging is false. `swarm discovery and the swarm routes` passes. Run `but diff` and confirm no file under `packages/conformance/golden/` changed.

- [ ] **Step 11: Docs — docs/usage.md, Swarm mode, adding a worker**

In `docs/usage.md`, after the closing fence of the "Or over the API" block (usage.md:2022, the fence after `hcloud server create --name worker-1 …`) and before `The token is a **secret**` (usage.md:2024), insert this paragraph with one blank line on each side:

```markdown
With Loki logging on (`pstack init --logging loki`), `script` and `cloud-config` install the loki log
plugin after Docker and before the join. A failed install never blocks the join: swarm keeps logged
services off that node. Run the script again on a worker that joined earlier to give it the plugin.
```

- [ ] **Step 12: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these files: `packages/pstack/internal/swarm/swarm.go`, `packages/pstack/internal/swarm/swarm_test.go`, `packages/pstack/internal/cloudinit/cloudinit.go`, `packages/pstack/internal/cloudinit/cloudinit_test.go`, `packages/pstack/internal/cli/run.go`, `packages/pstack/internal/api/routes.go`, `docs/usage.md`. Never `packages/conformance/golden/host/**` or `pstack.db-shm` / `pstack.db-wal`. Then:

```bash
but commit -b claude/loki-logging -m "feat(swarm): join material installs the loki plugin when logging is on" <ids>
```

---

### Task 11: Swarm page + pstack swarm: flag nodes without the plugin

**Files:**
- Modify: `packages/pstack/internal/api/routes.go:179-187` (the `/api/swarm` GET block)
- Modify: `packages/pstack/internal/cli/run.go:626-628` (`swarmCmd`, `case "status":`. T5, T6 and T10 shift these numbers, so anchor on the text)
- Modify: `packages/client/src/types.ts:98-123`
- Modify: `apps/ui/src/api/types.ts:70-91`
- Modify: `apps/ui/src/views/SwarmView.vue:1-26, 159-169`
- Modify: `docs/usage.md:2032-2041` (the Swarm mode JSON sample. T6 and T10 shift it, so anchor on the text)
- Modify: `packages/conformance/expected-pass.json:14`
- Modify: `packages/conformance/golden/host/expected/swarm.json:36-37`. Line numbers are after T7's `"lokiPlugin": null` line. Hand-edit this file. Never regenerate it with `gen/host-fixture.ts`, which rewrites all of `golden/host`, the db included.
- Test: `packages/conformance/test/api-share-sleep-swarm.test.ts` (import after :17; new test inside `describe('swarm discovery and the swarm routes')` (:392), inserted before the `/**` at :441)

There is no `api/openapi.yaml` edit. `/api/swarm` answers `$ref '#/components/responses/Ok'` (openapi.yaml:690), which is `{ description: OK. }` with no schema (openapi.yaml:902). So apicli needs no regeneration either.

`test/host-fixture.test.ts:112` compares the whole `/api/swarm` body against `golden/host/expected/swarm.json` with `toEqual`. So the new top-level `lokiPluginInstall` key has to go into that golden, or the test fails. The fixture shim's control-stack container is Traefik (`harness/host-fixture.ts:31-32`), so `LokiPushURL` returns `""` there and the node's `lokiPlugin` stays `null`. The install line is the golden's only change.

**Interfaces:**
- Consumes:
  - `func MarkLokiPlugins(r exec.Runner, info *Info)` (package swarm, T7)
  - `Node.LokiPlugin *bool \`json:"lokiPlugin"\`` (package swarm, T7)
  - `func LokiPushURL(r exec.Runner) string` (package inspect, T7)
  - `SwarmReport(info Info) string`, which flags nodes whose `LokiPlugin` is false (T7)
  - T7's hand-added `"lokiPlugin": null` after `"self": true` in `packages/conformance/golden/host/expected/swarm.json`
  - `const LokiPluginInstall` (package swarm, T1)
  - `/api/swarm/join` passing `Logging: inspect.LokiPushURL(s.host) != ""` (T10). The new test asserts through it.
  - the `inspect` import T10 adds to `internal/cli/run.go`. T11 adds no import: `routes.go` already imports inspect at :15.
- Produces:
  - `GET /api/swarm`: every node gains `lokiPlugin` (`true` | `false` | `null`). The top level gains `lokiPluginInstall: string`, always present, placed between `ports` and `note`.
  - `golden/host/expected/swarm.json` gains `"lokiPluginInstall"` between `"ports"` and `"note"`. T13 and T14 list it as a changed golden.
  - `pstack swarm` / `pstack swarm status`: plugin presence is marked before `SwarmReport`.
  - TS types: `SwarmNode.lokiPlugin: boolean | null` and `SwarmInfo.lokiPluginInstall: string`, identical in `packages/client/src/types.ts` and `apps/ui/src/api/types.ts`.
  - `SwarmView.vue`: a `No loki plugin` badge on each node that reads false, and a warn banner that names those nodes and shows the install line.
  - conformance: `api-share-sleep-swarm.test.ts` passes 10 (was 9). `host-fixture.test.ts` still passes 34.

- [ ] **Step 1: Write the failing test**

In `packages/conformance/test/api-share-sleep-swarm.test.ts`, add this import right after the existing line `import { eventTap } from '../harness/receiver.ts';` (:17):

```ts
import { SWARM_SHIM } from '../gen/goldens.table.ts';
```

`SWARM_SHIM` (gen/goldens.table.ts:34-38) has the same three arms as the first three of the local `swarmShim()`: info, node ls and join-token. They cover every docker call that `/api/swarm` and `/api/swarm/join` make. The local closure inlines its arms, so it can't be extended without hoisting them.

Then insert the following immediately BEFORE this existing text (:441-442), which opens the route-page shim's doc comment:

```
  /**
   * The stack `sw` as the ROUTE pages read it — a different `docker service ls` from the panel's
```

Insert:

```ts
  /**
   * A host that ships logs to Loki: the control stack's loki container carries the push URL (what
   * inspect.LokiPushURL reads), and `docker node inspect` lists each node's plugins — n1 has the loki
   * log plugin under the name swarm reports (`loki:latest`), n2 only an overlay driver.
   */
  const lokiContainerArms = [
    `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'l0k1' ;;`,
    `  "inspect l0k1") printf '%s\\n' '[{"Id":"l0k1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"loki","pstack.logging.push-url":"https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"}},"State":{"Status":"running"}}]' ;;`,
  ].join('\n');
  const nodePluginArms = `  "node inspect --format {{json .}} n1 n2") printf '%s\\n' '{"ID":"n1","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"},{"Type":"Network","Name":"overlay"}]}}}' '{"ID":"n2","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}' ;;`;

  test('with Loki on, /api/swarm flags the node without its plugin and the join script installs it', async () => {
    // negative control: drop swarm.MarkLokiPlugins from the /api/swarm route — both nodes read
    // lokiPlugin null, and the page has nothing to flag.
    const docker = dockerShim([SWARM_SHIM, lokiContainerArms, nodePluginArms].join('\n'));
    const s = await bootServer({ tag: 'swarm-loki', pathPrefix: docker.dir });
    const { base, H } = s;
    type Panel = { nodes: Array<{ hostname: string; lokiPlugin: boolean | null }>; lokiPluginInstall: string };
    const panel = async () => (await (await fetch(`${base}/api/swarm`, { headers: H })).json()) as Panel;
    try {
      const on = await panel();
      expect(on.nodes.map((n) => [n.hostname, n.lokiPlugin])).toEqual([
        ['mgr', true],
        ['wrk', false],
      ]);
      expect(on.lokiPluginInstall).toContain('docker plugin install grafana/loki-docker-driver:3.7.7-$arch');
      const script = await (await fetch(`${base}/api/swarm/join?format=script`, { headers: H })).text();
      expect(script).toContain('loki plugin not installed');

      // negative control: mark the plugins whether or not a loki container was found — wrk reads
      // false on a host that ships no logs. null is "not checked", never "missing".
      docker.rewrite([SWARM_SHIM, nodePluginArms].join('\n'));
      const off = await panel();
      expect(off.nodes.map((n) => n.lokiPlugin)).toEqual([null, null]);
      expect(off.lokiPluginInstall).toBe(on.lokiPluginInstall);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-share-sleep-swarm.test.ts -t 'with Loki on'
```

Expected: 1 fail at the first `toEqual`. T7 already puts `"lokiPlugin": null` on every node, but nothing marks them yet:

```
error: expect(received).toEqual(expected)
...
-     true,
+     null,
...
-     false,
+     null,
(fail) swarm discovery and the swarm routes > with Loki on, /api/swarm flags the node without its plugin and the join script installs it
 0 pass
 1 fail
```

`lokiPluginInstall` is also `undefined` at this point. The join-script assertion already holds because of T10.

- [ ] **Step 3: Implement the route and the CLI read**

In `packages/pstack/internal/api/routes.go`, replace

```go
	if path == "/api/swarm" && r.Method == http.MethodGet {
		info := swarm.SwarmInfo(s.host)
		note := "This daemon is not a swarm manager. Previews run with docker compose on this host; `pstack init --orchestrator swarm` (on the host) enables swarm mode."
		if info.Active {
			note = "Add a worker: GET /api/swarm/join?format=command|script|cloud-config, run it on the new machine."
		}
		writeJSON(w, 200, append(spread(info), jsonx.KV{K: "ports", V: swarm.SwarmPorts}, jsonx.KV{K: "note", V: note}))
		return nil
	}
```

with

```go
	if path == "/api/swarm" && r.Method == http.MethodGet {
		info := swarm.SwarmInfo(s.host)
		// Plugins are read only on a host that ships logs, found the way deploys find it; with logging
		// off every lokiPlugin stays null — "not checked", never "missing". Nothing reaches a worker to
		// install one, so the route hands out the line to run there.
		if len(info.Nodes) > 0 && inspect.LokiPushURL(s.host) != "" {
			swarm.MarkLokiPlugins(s.host, &info)
		}
		note := "This daemon is not a swarm manager. Previews run with docker compose on this host; `pstack init --orchestrator swarm` (on the host) enables swarm mode."
		if info.Active {
			note = "Add a worker: GET /api/swarm/join?format=command|script|cloud-config, run it on the new machine."
		}
		writeJSON(w, 200, append(spread(info), jsonx.KV{K: "ports", V: swarm.SwarmPorts}, jsonx.KV{K: "lokiPluginInstall", V: swarm.LokiPluginInstall}, jsonx.KV{K: "note", V: note}))
		return nil
	}
```

`len(info.Nodes) > 0` comes first on purpose. On a compose-only or unreachable host, the page's 10-second poll then issues no `docker ps` or `docker inspect`. `MarkLokiPlugins` does nothing on an empty node list anyway, so the output is the same.

In `packages/pstack/internal/cli/run.go`, in `swarmCmd`, replace

```go
	case "status":
		info := swarm.SwarmInfo(runner)
		fmt.Fprintln(out, swarm.SwarmReport(info))
```

with

```go
	case "status":
		info := swarm.SwarmInfo(runner)
		// Read exactly as /api/swarm reads it, so the table flags the nodes the Swarm page flags.
		if len(info.Nodes) > 0 && inspect.LokiPushURL(runner) != "" {
			swarm.MarkLokiPlugins(runner, &info)
		}
		fmt.Fprintln(out, swarm.SwarmReport(info))
```

T10 already imports `inspect` in run.go. Don't add a second import.

- [ ] **Step 4: Run it and watch it pass; update the golden host's swarm response; check the CLI by hand**

```bash
cd /Volumes/S1/code/preview-stacks/packages/pstack && go vet ./internal/api/ ./internal/cli/ && go test -race -timeout 120s ./internal/api/ ./internal/cli/ ./internal/swarm/
cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-share-sleep-swarm.test.ts test/cli-goldens.test.ts
```

Expected: every Go package prints `ok`, and the bun run ends with `0 fail`. `api-share-sleep-swarm` passes all 10 tests, including `(pass) swarm discovery and the swarm routes > with Loki on, …`. The `swarm-status` golden stays byte-identical. `SWARM_SHIM` has no arm for the new `docker ps -aq --filter label=com.docker.compose.project=pstack-control`, so the shim's default `*) exit 0` (harness/docker-shim.ts:31) answers it with no output. `LokiPushURL` then returns `""` and nothing is marked.

Now the golden host. It must fail first:

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts
```

Expected: `33 pass`, `1 fail`. The failure is `(fail) the golden host opens unchanged > GET /api/swarm answers the reference bytes`. Its diff shows one received-only line, `"lokiPluginInstall": "arch=$(uname -m); …"`, between the `ports` array and `"note"`. Nothing else differs: the node's `"lokiPlugin": null` (T7) already matches, because the fixture's control container is Traefik and nothing gets marked.

Hand-edit `packages/conformance/golden/host/expected/swarm.json`. Don't use `gen/host-fixture.ts`. Replace

```
        "why": "overlay network traffic — VXLAN (every node ↔ every node)"
      }
    ],
    "note": "Add a worker: GET /api/swarm/join?format=command|script|cloud-config, run it on the new machine."
```

with

```
        "why": "overlay network traffic — VXLAN (every node ↔ every node)"
      }
    ],
    "lokiPluginInstall": "arch=$(uname -m); case \"$arch\" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac; docker plugin inspect loki >/dev/null 2>&1 || docker plugin install grafana/loki-docker-driver:3.7.7-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn; [ \"$(docker plugin inspect -f '{{.Enabled}}' loki)\" = true ] || docker plugin enable loki",
    "note": "Add a worker: GET /api/swarm/join?format=command|script|cloud-config, run it on the new machine."
```

The inserted value is the JSON escape of T1's `swarm.LokiPluginInstall` (344 characters). Compare it with the received value in the failing diff above. If they differ, commit the received value exactly, and report the drift from T1's contract string. `maskHost` (harness/host-fixture.ts:81-89) leaves this string alone: it holds no data dir, no `127.0.0.1:<port>`, no quoted pstack version and no `lastUsedAt`.

Re-run:

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/host-fixture.test.ts
```

Expected: `34 pass`, `0 fail`. The count is unchanged from `expected-pass.json`'s `"test/host-fixture.test.ts": 34`.

No automated check covers `pstack swarm status` in this task, so check it by hand. Write a shim once, into a fixed temp path that later steps reuse. It is a single shell invocation, and the arms use printf, never echo:

```bash
D="${TMPDIR:-/tmp}/pstack-t11"; rm -rf "$D"; mkdir -p "$D/bin" "$D/data"
cat > "$D/bin/docker" <<'EOF'
#!/bin/sh
case "$*" in
  "info --format {{json .Swarm}}") printf '%s\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;
  "node ls --format {{json .}}") printf '%s\n' '{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' '{"ID":"n2","Hostname":"wrk","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ;;
  "swarm join-token -q worker") printf '%s\n' 'SWMTKN-1-abc-def' ;;
  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\n' 'l0k1' ;;
  "inspect l0k1") printf '%s\n' '[{"Id":"l0k1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"loki","pstack.logging.push-url":"https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"}},"State":{"Status":"running"}}]' ;;
  "node inspect --format {{json .}} n1 n2") printf '%s\n' '{"ID":"n1","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"}]}}}' '{"ID":"n2","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}' ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$D/bin/docker"
cd /Volumes/S1/code/preview-stacks && PATH="$D/bin:$PATH" ./packages/pstack/bin/pstack swarm; echo "exit $?"
```

Expected: the node table, then T7's lines `no loki log plugin on wrk — logged services will not run there. Run on each:` and `  arch=$(uname -m); …`, then `exit 0`. Next, comment out the `swarm.MarkLokiPlugins(runner, &info)` line in run.go, rebuild and re-run: the `no loki log plugin` line is gone. Restore the line and rebuild.

- [ ] **Step 5: Run both negative controls and the null server**

For each mutation below: make the edit, rebuild (`cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`), run `cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/api-share-sleep-swarm.test.ts -t 'with Loki on'`, and watch it fail. Then restore and rebuild.

1. In routes.go, comment out `swarm.MarkLokiPlugins(s.host, &info)`. The test fails at the first `toEqual`: `true` expected, `null` received.
2. In routes.go, change `if len(info.Nodes) > 0 && inspect.LokiPushURL(s.host) != "" {` to `if len(info.Nodes) > 0 {`. The first phase passes. The second fails: `[null, null]` expected, `[true, false]` received.

After restoring both, rebuild and re-run: 1 pass.

Null server:

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=null bun test test/api-share-sleep-swarm.test.ts -t 'with Loki on'
```

Expected: `1 fail` with `TypeError: undefined is not an object (evaluating 'on.nodes.map')`, because the null server answers `{}`.

- [ ] **Step 6: Ratchet the pass count**

In `packages/conformance/expected-pass.json`, replace

```json
    "test/api-share-sleep-swarm.test.ts": 9,
```

with

```json
    "test/api-share-sleep-swarm.test.ts": 10,
```

Leave `"test/host-fixture.test.ts": 34` as it is.

```bash
cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run ratchet
```

Expected: `test/api-share-sleep-swarm.test.ts  10/10  (recorded 10) complete` and `test/host-fixture.test.ts  34/34`, no `REGRESSED` line, exit 0.

- [ ] **Step 7: The client and SPA types**

In `packages/client/src/types.ts`, replace

```ts
  /** The node this API runs on. */
  self: boolean;
};
```

with

```ts
  /** The node this API runs on. */
  self: boolean;
  /** null: logging is off or docker did not answer. false: no loki log plugin — logged services will not run there. */
  lokiPlugin: boolean | null;
};
```

and replace

```ts
  ports: Array<{ port: string; why: string }>;
  note: string;
```

with

```ts
  ports: Array<{ port: string; why: string }>;
  /** The one line that installs the loki log plugin on a node. */
  lokiPluginInstall: string;
  note: string;
```

In `apps/ui/src/api/types.ts`, replace

```ts
  engineVersion: string;
  self: boolean;
};
```

with

```ts
  engineVersion: string;
  self: boolean;
  /** null: logging is off or docker did not answer. false: no loki log plugin — logged services will not run there. */
  lokiPlugin: boolean | null;
};
```

and replace

```ts
  ports: Array<{ port: string; why: string }>;
  note: string;
```

with

```ts
  ports: Array<{ port: string; why: string }>;
  /** The one line that installs the loki log plugin on a node. */
  lokiPluginInstall: string;
  note: string;
```

Both `ports…/note` anchors are unique in their files. `note: string;` on its own is not unique in the SPA types.

- [ ] **Step 8: The Swarm page**

In `apps/ui/src/views/SwarmView.vue`, first the header. Replace

```
 * docker did not answer (nothing is known), this daemon is not a manager (previews run with
 * compose), and active (the node table).
 *
```

with

```
 * docker did not answer (nothing is known), this daemon is not a manager (previews run with
 * compose), and active (the node table).
 *
 * WITH LOKI LOGGING ON, a node without the loki log plugin is flagged and the install line shown.
 * Swarm keeps logged services off that node, and nothing here can reach a worker to install it — the
 * line is run there by hand. `lokiPlugin: null` is "not checked" (logging off), never "missing".
 *
```

Replace

```ts
import { onBeforeUnmount, ref } from 'vue';
```

with

```ts
import { computed, onBeforeUnmount, ref } from 'vue';
```

Replace

```ts
usePolling(load, 10_000);
```

with

```ts
usePolling(load, 10_000);

// Only `false` is missing; null is "not checked".
const unplugged = computed(() => info.value?.nodes.filter((n) => n.lokiPlugin === false) ?? []);
```

In the hostname cell, replace

```html
                <span v-if="n.self" class="badge info" title="the node this control plane runs on">This node</span>
```

with

```html
                <span v-if="n.self" class="badge info" title="the node this control plane runs on">This node</span>
                <span v-if="n.lokiPlugin === false" class="badge warn">No loki plugin</span>
```

After the table, inside the active `<template v-else>`, replace

```html
        <p v-else class="mute">Docker lists no nodes.</p>
      </template>
```

with

```html
        <p v-else class="mute">Docker lists no nodes.</p>
        <div v-if="unplugged.length" class="banner warn">
          <b>No loki plugin on {{ unplugged.map((n) => n.hostname || n.id.slice(0, 12)).join(', ') }}.</b>
          <p>Logged services will not run there. Run on each:</p>
          <pre class="code" style="white-space: pre-wrap; word-break: break-all">{{ info.lokiPluginInstall }}</pre>
        </div>
      </template>
```

The inline `white-space`/`word-break` follows this file's own precedent for the revealed join material (SwarmView.vue:220). `pre.code` defaults to `white-space: pre` (app.css:1251), which would make this 344-character line scroll sideways at 320px. `badge warn`, `banner warn` and `pre.code` are existing classes (app.css:747, :813, :1251). The basic UI (`packages/pstack/ui/index.html`) does not change.

- [ ] **Step 9: Typecheck both TS packages**

```bash
cd /Volumes/S1/code/preview-stacks/packages/client && bun run typecheck
cd /Volumes/S1/code/preview-stacks/apps/ui && bun run typecheck
cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck
```

Expected: all three exit 0 with no diagnostics.

- [ ] **Step 10: Look at it in a browser**

Reuse the Step 4 shim. Start each of these in the background, one invocation each:

```bash
D="${TMPDIR:-/tmp}/pstack-t11"; cd /Volumes/S1/code/preview-stacks && PSTACK_TOKEN=dev-token-0123456789abcdef0123456789abcdef PSTACK_DATA="$D/data" PATH="$D/bin:$PATH" ./packages/pstack/bin/pstack serve
```

```bash
cd /Volumes/S1/code/preview-stacks/apps/ui && bun run dev
```

1. Open `http://127.0.0.1:5273/login`, choose "Use a token instead", and paste `dev-token-0123456789abcdef0123456789abcdef` in Settings.
2. Open `http://127.0.0.1:5273/swarm`.
3. Check the page:
   - `wrk` carries a `No loki plugin` badge and `mgr` does not.
   - Under the table, a warn banner reads `No loki plugin on wrk.`, and the install line wraps inside the banner.
4. Resize the viewport to 320px wide. The table collapses to cards, the badge stays in the hostname card, and the page body doesn't scroll sideways.
5. Switch to the other theme. The badge and banner text stay legible.
6. Stop both processes, then run `rm -rf "${TMPDIR:-/tmp}/pstack-t11"`.

If no browser is available, say so in the task report. Don't claim the check was done.

- [ ] **Step 11: Docs**

In `docs/usage.md`, in the Swarm mode JSON sample, replace

```
               "availability": "active", "managerStatus": "leader", "engineVersion": "28.0.1", "self": true } ],
  "ports": [ … ], "note": "…" }
```

with

```
               "availability": "active", "managerStatus": "leader", "engineVersion": "28.0.1", "self": true,
               "lokiPlugin": null } ],
  "ports": [ … ], "lokiPluginInstall": "arch=$(uname -m); … docker plugin enable loki", "note": "…" }
```

Then replace

```
`active: false` means the host runs previews with compose; `pstack init --orchestrator swarm` (on
the host, with every preview torn down first — the networks have to be recreated) switches it.
```

with

```
`active: false` means the host runs previews with compose; `pstack init --orchestrator swarm` (on
the host, with every preview torn down first — the networks have to be recreated) switches it.

`lokiPlugin: false` means the node has no `loki` log plugin, so swarm keeps logged services off it:
run `lokiPluginInstall` on that node. `null` means not checked (this host's logging is off). The
Swarm page and `pstack swarm` flag the same nodes.
```

- [ ] **Step 12: Full gate**

```bash
cd /Volumes/S1/code/preview-stacks && bun run check
```

Expected: every turbo task succeeds. That covers `go vet` and `go test -race` in packages/pstack, the client's `bun test && tsc --noEmit && build`, apps/ui's typecheck and build, and the conformance `bun test && tsc --noEmit`, which includes `host-fixture.test.ts` against the edited golden.

- [ ] **Step 13: Commit**

```bash
cd /Volumes/S1/code/preview-stacks && but status
```

Take the IDs of exactly these files:
- `packages/pstack/internal/api/routes.go`
- `packages/pstack/internal/cli/run.go`
- `packages/client/src/types.ts`
- `apps/ui/src/api/types.ts`
- `apps/ui/src/views/SwarmView.vue`
- `docs/usage.md`
- `packages/conformance/expected-pass.json`
- `packages/conformance/golden/host/expected/swarm.json`
- `packages/conformance/test/api-share-sleep-swarm.test.ts`

Commit nothing else under `golden/host`. Never commit the untracked `golden/host/db/pstack.db-shm` or `golden/host/db/pstack.db-wal`, and never commit `packages/pstack/bin`.

```bash
but commit -b claude/loki-logging -m "feat(ui): flag swarm nodes without the loki plugin" <ids>
```

---

### Task 12: hostnames: loki. is a control hostname; previews cannot claim one; masking proof

**Files:**
- Modify: `packages/pstack/internal/routing/domains.go:244-260` (IsControlHostname and its doc comment)
- Modify: `packages/pstack/internal/autolabel/autolabel.go:29-32` (package header, new section), `:120-123` (new `ControlHostname` var), `:286-293` (refusal in AugmentComposeDoc). These line numbers are from the current tree. T8 edits this file first, so apply each edit by its quoted text, not by line number.
- Modify: `docs/usage.md:1489-1500` (Hostnames table and paragraph), `docs/usage.md:3266-3275` (Control-stack hostnames reference table)
- Test: `packages/pstack/internal/routing/domains_test.go` (append)
- Test: `packages/pstack/internal/autolabel/autolabel_test.go` (one import and one appended test)
- Test: `packages/pstack/internal/redact/redact_test.go:136-142` (one new t.Run)

**Interfaces:**
- Consumes: `autolabel.MaterializeCompose` flow from T8. Nothing is called from T8 directly: the refusal sits in `AugmentComposeDoc`, which T8's `MaterializeCompose` still calls (autolabel.go:490 today). T8's edits to autolabel.go and autolabel_test.go must already be committed.
- Produces:
  - `func (s *RoutingStore) IsControlHostname(hostname, primary string) bool`. The signature is unchanged. It now also matches `loki.<d>` for the primary and every added domain.
  - `package autolabel` / `var ControlHostname = func(host string) bool { return routing.New(routing.DynamicDir(registry.DataDir())).IsControlHostname(host, os.Getenv("PSTACK_DOMAIN")) }`
  - The reserved-host refusal in `AugmentComposeDoc`: a `*spec.Error` when `req.Host != "" && ControlHostname(req.Host)`.
  - Masking needs no code change. `redact.RedactText` already rewrites `https://pstack:<pw>@loki.<d>/…` to `https://pstack:••••@loki.<d>/…`. This was verified by running the three regexes from redact.go:152-158 on both the plain form and the JSON form.

---

- [ ] **Step 1: Write the failing routing test.** Append this to the end of `packages/pstack/internal/routing/domains_test.go`. The file already imports `strings` and `testing` and nothing new is needed.

```go
func TestLokiIsAControlHostnameOnEveryDomain(t *testing.T) {
	// negative control: drop `|| h == "loki."+d` from IsControlHostname — every true-expecting
	// assertion below fails, and loki.<domain> is left to the wake router and to any preview that
	// asks for it with pstack.routing.host.
	s := New(t.TempDir())
	if _, err := s.SetDomains([]string{"preview.new.com"}, DomainOptions{Primary: "preview.old.com", Mode: "http01"}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"loki.preview.old.com", "loki.preview.new.com", "LOKI.Preview.New.com"} {
		if !s.IsControlHostname(h, "preview.old.com") {
			t.Errorf("%s must be a control hostname", h)
		}
	}
	// An exact name, not a prefix: a convention hostname that happens to start with loki is a preview's.
	if s.IsControlHostname("loki-pr-1.preview.old.com", "preview.old.com") {
		t.Error("a preview hostname is not the control plane's")
	}
	// The primary needs no file to be reserved — the nil store still answers for it.
	var none *RoutingStore
	if !none.IsControlHostname("loki.preview.old.com", "preview.old.com") {
		t.Error("loki.<primary> must be a control hostname on a nil store")
	}
}
```

- [ ] **Step 2: Run it and watch it fail.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/ -run TestLokiIsAControlHostnameOnEveryDomain
```

Expected:

```
--- FAIL: TestLokiIsAControlHostnameOnEveryDomain
    domains_test.go:…: loki.preview.old.com must be a control hostname
    domains_test.go:…: loki.preview.new.com must be a control hostname
    domains_test.go:…: LOKI.Preview.New.com must be a control hostname
    domains_test.go:…: loki.<primary> must be a control hostname on a nil store
FAIL
```

- [ ] **Step 3: Implement.** In `packages/pstack/internal/routing/domains.go`, replace lines 244-260 as shown below.

Existing:

```go
// IsControlHostname reports whether a hostname is one of the control plane's own — the primary's or
// any additional domain's. Those are never a preview's to wake.
//
// The PRIMARY is checked even on a nil store, because it is the one that must never be answered
// with a waking page whatever else is missing.
func (s *RoutingStore) IsControlHostname(hostname, primary string) bool {
	h := strings.ToLower(hostname)
	for _, d := range append([]string{primary}, s.Domains()...) {
		if d == "" {
			continue
		}
		if h == "control."+strings.ToLower(d) || h == "api."+strings.ToLower(d) {
			return true
		}
	}
	return false
}
```

Replacement:

```go
// IsControlHostname reports whether a hostname is one of the control plane's own — the primary's or
// any additional domain's. Those are never a preview's to wake, and never a preview's to claim:
// autolabel refuses a `pstack.routing.host` that names one.
//
// The PRIMARY is checked even on a nil store, because it is the one that must never be answered
// with a waking page whatever else is missing.
//
// `loki.` counts ALWAYS, not only on a host running `--logging loki`: a host that turned logging
// off must still never answer `loki.` with a waking page, and no preview can take the name meanwhile.
func (s *RoutingStore) IsControlHostname(hostname, primary string) bool {
	h := strings.ToLower(hostname)
	for _, d := range append([]string{primary}, s.Domains()...) {
		if d == "" {
			continue
		}
		d = strings.ToLower(d)
		if h == "control."+d || h == "api."+d || h == "loki."+d {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run it and watch it pass, then run the negative control.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/routing	…s`. The existing `TestEachDomainGetsConsoleAPIAndAWakeRouter` still passes.
Negative control: delete ` || h == "loki."+d` and rerun with `-run TestLokiIsAControlHostnameOnEveryDomain`. It must FAIL with the Step 2 messages. Restore the clause and rerun to green.

- [ ] **Step 5: Write the failing autolabel test.** In `packages/pstack/internal/autolabel/autolabel_test.go`, add the `routing` import between omap and spec.

Existing:

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
```

Replacement:

```go
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
```

Append this test to the end of the file. It uses the existing helpers `doc`, `s`, `augment`, `labelsOf` and `contains` (autolabel_test.go:22-91).

```go
func TestAPreviewCannotClaimAControlHostname(t *testing.T) {
	// The real seam, not a pin: the primary comes from PSTACK_DOMAIN and the added domains from the
	// routing dir, exactly as a deploy inside the control container resolves them.
	dyn := t.TempDir()
	t.Setenv("PSTACK_ROUTING_DIR", dyn)
	t.Setenv("PSTACK_DOMAIN", "preview.example.com")
	if _, err := routing.New(dyn).SetDomains([]string{"other.example.org"}, routing.DomainOptions{Primary: "preview.example.com", Mode: "http01"}); err != nil {
		t.Fatal(err)
	}
	claiming := func(t *testing.T, host string) *omap.Map {
		return doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80, pstack.routing.host="+host+"]\n")
	}
	refused := func(t *testing.T, host string) {
		t.Helper()
		_, err := AugmentComposeDoc(AugmentArgs{Doc: claiming(t, host), Spec: s(t), Challenge: HTTP01})
		if err == nil || !spec.IsSpecError(err) || !strings.Contains(err.Error(), "pstack.routing.host="+host+" — a control hostname") {
			t.Fatalf("%s must be refused as a control hostname, got %v", host, err)
		}
	}

	t.Run("loki. of the primary domain is refused", func(t *testing.T) {
		// negative control: pass "" as the primary in ControlHostname's default — loki.preview.example.com
		// is in no added domain, so it gets a router and this fails with `got <nil>`.
		refused(t, "loki.preview.example.com")
	})

	t.Run("api. of an added domain is refused", func(t *testing.T) {
		// negative control: read no store in ControlHostname's default
		// (`(*routing.RoutingStore)(nil).IsControlHostname(host, os.Getenv("PSTACK_DOMAIN"))`) —
		// api.other.example.org gets a router and this fails with `got <nil>`.
		refused(t, "api.other.example.org")
	})

	t.Run("any other explicit host still gets its router", func(t *testing.T) {
		// negative control: drop `&& ControlHostname(req.Host)` from the refusal — every explicit host
		// is refused and augment fails the test.
		r := augment(t, claiming(t, "app.preview.example.com"), s(t), HTTP01)
		if l := labelsOf(t, r.Doc, "app"); !contains(l, "traefik.http.routers.app-pr-7.rule=Host(`app.preview.example.com`)") {
			t.Errorf("labels: %v", l)
		}
	})
}
```

- [ ] **Step 6: Run it and watch it fail.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/ -run TestAPreviewCannotClaimAControlHostname
```

Expected. The file compiles because the test never names `ControlHostname`.

```
--- FAIL: TestAPreviewCannotClaimAControlHostname
    --- FAIL: TestAPreviewCannotClaimAControlHostname/loki._of_the_primary_domain_is_refused
        autolabel_test.go:…: loki.preview.example.com must be refused as a control hostname, got <nil>
    --- FAIL: TestAPreviewCannotClaimAControlHostname/api._of_an_added_domain_is_refused
        autolabel_test.go:…: api.other.example.org must be refused as a control hostname, got <nil>
FAIL
```

- [ ] **Step 7: Implement.** There are three edits in `packages/pstack/internal/autolabel/autolabel.go`. `fmt`, `os`, `registry`, `routing` and `spec` are already imported (autolabel.go:60-75), so there is no import edit.

(a) Package header. Record the decision after the "IT NEVER OVERRIDES YOU" section.

Existing:

```go
// A service with neither a `pstack.routing.*` label nor a `traefik.*` one is also left alone — that is
// a database or a worker, and forcing a hostname onto it would be wrong.
//
// ── WHY A DERIVED FILE AND NOT AN OVERLAY ────────────────────────────────────────────────────────
```

Replacement:

```go
// A service with neither a `pstack.routing.*` label nor a `traefik.*` one is also left alone — that is
// a database or a worker, and forcing a hostname onto it would be wrong.
//
// ── NOR DOES IT HAND OUT A CONTROL HOSTNAME ──────────────────────────────────────────────────────
//
// A `pstack.routing.host` naming `control.`, `api.` or `loki.` of any domain this host answers on
// is refused (ControlHostname): its router would compete with the control plane's own for the
// console, the API, or every node's log pushes and the password on them. `loki.` is reserved even
// with logging off. A service with its own `traefik.*` labels is not checked — that is the escape
// hatch above, and specs are CI-trusted.
//
// The refusal is a spec error like the missing-domain one, so it fires on EVERY compose subcommand
// that re-materializes the file — `down` included, where stack.Down records it as non-fatal.
//
// ── WHY A DERIVED FILE AND NOT AN OVERLAY ────────────────────────────────────────────────────────
```

(b) The seam goes after the challenge probe. Insert it immediately before the `RoutingRequest` doc comment. That anchor holds even though T8 puts `DetectLogging` after `DetectChallenge`.

Existing:

```go
// RoutingRequest is what a service asked for, read from its `pstack.routing.*` labels.
```

Replacement:

```go
// ControlHostname reports whether a hostname is the control plane's — `control.`, `api.` or `loki.`
// of the primary domain or any added one — behind a variable, like DetectChallenge, so a caller's
// test can pin it. Both halves are resolved from the PROCESS ENVIRONMENT, the rule DetectChallenge
// documents: the primary from PSTACK_DOMAIN, the added domains from routing.DynamicDir. The control
// container always has both. A host-side CLI without PSTACK_DOMAIN checks the added domains only;
// that caller holds the docker socket and could route the name by hand anyway.
var ControlHostname = func(host string) bool {
	return routing.New(routing.DynamicDir(registry.DataDir())).IsControlHostname(host, os.Getenv("PSTACK_DOMAIN"))
}

// RoutingRequest is what a service asked for, read from its `pstack.routing.*` labels.
```

(c) The refusal goes in `AugmentComposeDoc`, right after the hostname is resolved.

Existing:

```go
		id := req.Name + "-" + st.Stack
		host := req.Host
		if host == "" {
			host = req.Name + "-" + st.Stack + "." + domain
		}
```

Replacement:

```go
		id := req.Name + "-" + st.Stack
		host := req.Host
		if host == "" {
			host = req.Name + "-" + st.Stack + "." + domain
		}
		// A preview never gets a control hostname — see the package comment. Only an EXPLICIT host can
		// collide: a generated one's first label is `<name>-<stack>`, which always has a dash in it.
		if req.Host != "" && ControlHostname(req.Host) {
			return nil, &spec.Error{Msg: fmt.Sprintf(`service "%s" sets pstack.routing.host=%s — a control hostname of this host (control., api. and loki. on every domain it answers on). Pick another host.`, name, req.Host)}
		}
```

- [ ] **Step 8: Run it and watch it pass, then run the three negative controls.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/autolabel/
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel	…s`. Every existing test passes unchanged, including "service_name overrides the hostname, and an explicit host overrides both" with `custom.example.com`.
Run each mutation with `-run TestAPreviewCannotClaimAControlHostname`, watch the named subtest FAIL, then restore:
1. In `ControlHostname`, replace `os.Getenv("PSTACK_DOMAIN")` with `""`. `loki._of_the_primary_domain_is_refused` fails with `got <nil>`.
2. In `ControlHostname`, replace `routing.New(routing.DynamicDir(registry.DataDir()))` with `(*routing.RoutingStore)(nil)`. `api._of_an_added_domain_is_refused` fails with `got <nil>`. `registry` stays used by DetectChallenge, so it still compiles.
3. In the refusal, delete `&& ControlHostname(req.Host)`. `any_other_explicit_host_still_gets_its_router` fails because augment's `t.Fatal` fires on the spec error. The loki/api subtests still pass, which is expected.

Rerun the package to green after restoring.

- [ ] **Step 9: Masking proof. It passes at once, so the negative control carries the weight.** In `packages/pstack/internal/redact/redact_test.go`, insert a new t.Run after the swarm-token subtest.

Existing (lines 136-142):

```go
	t.Run("redactText strips a swarm join token", func(t *testing.T) {
		// negative control: drop the swarmToken replacement.
		if got := RedactText("joined with SWMTKN-1-abc-def now"); got != "joined with SWMTKN-•••••• now" {
			t.Fatalf("got %q", got)
		}
	})
}
```

Replacement:

```go
	t.Run("redactText strips a swarm join token", func(t *testing.T) {
		// negative control: drop the swarmToken replacement.
		if got := RedactText("joined with SWMTKN-1-abc-def now"); got != "joined with SWMTKN-•••••• now" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a loki push URL keeps its user and loses its password", func(t *testing.T) {
		// negative control: drop the urlPassword replacement — the push password survives in both forms.
		// Four bullets, the shape every URL password already gets (pinned above), not the design doc's eight.
		in := "loki-url: https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"
		if got := RedactText(in); got != "loki-url: https://pstack:••••@loki.preview.example.com/loki/api/v1/push" {
			t.Fatalf("got %q", got)
		}
		// compose.generated.yml is JSON, so the URL sits between quotes with its key before it.
		generated := `{"loki-url":"https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push","loki-retries":"2"}`
		if got := RedactText(generated); got != `{"loki-url":"https://pstack:••••@loki.preview.example.com/loki/api/v1/push","loki-retries":"2"}` {
			t.Fatalf("json form: %q", got)
		}
	})
}
```

Run:

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/redact/ -run 'TestRedaction/a_loki_push_URL'
```

Expected: `ok  	github.com/samishal1998/preview-stacks/packages/pstack/internal/redact	…s`, with no code change.
Negative control: comment out `out = urlPassword.ReplaceAllString(out, "${1}:••••@")` at `packages/pstack/internal/redact/redact.go:167` and rerun. It must FAIL with `got "loki-url: https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"`. Restore the line and rerun to green.

- [ ] **Step 10: Docs.** There are two edits in `docs/usage.md`.

(a) The Hostnames table and its paragraph (lines 1489-1500).

Existing:

```markdown
| Hostname | Serves |
|---|---|
| `control.<domain>` | the web UI (an operator's browser) |
| `api.<domain>` | the API (CI, `curl`, scripts) |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
| `<surface>-pr-<n>.<domain>` | a per-PR surface, e.g. `backend-pr-123.<domain>` |

`control` and `api` are **two routers pointing at one container** — the API process serves the UI. The
UI calls the API with relative `/api/…` paths, so it is same-origin from `control.<domain>` and needs
no CORS; `api.<domain>` exists to give external callers an honest name that is not "the UI host".
```

Replacement:

```markdown
| Hostname | Serves |
|---|---|
| `control.<domain>` | the web UI (an operator's browser) |
| `api.<domain>` | the API (CI, `curl`, scripts) |
| `loki.<domain>` | Loki's push endpoint, for the nodes' log plugins (`--logging loki`); reserved even with logging off |
| `<service-name>.<domain>` | the convention for a shared service's own hostname |
| `<surface>-pr-<n>.<domain>` | a per-PR surface, e.g. `backend-pr-123.<domain>` |

`control` and `api` are **two routers pointing at one container** — the API process serves the UI. The
UI calls the API with relative `/api/…` paths, so it is same-origin from `control.<domain>` and needs
no CORS; `api.<domain>` exists to give external callers an honest name that is not "the UI host".

A `pstack.routing.host` naming `control.`, `api.` or `loki.` on any domain this host answers on is
refused at deploy.
```

(b) The Control-stack hostnames reference table (lines 3268-3271).

Existing:

```markdown
| Hostname | Router | Serves |
|---|---|---|
| `control.<domain>` | `pstack-ui` | the web UI |
| `api.<domain>` | `pstack-api` | the API |
```

Replacement:

```markdown
| Hostname | Router | Serves |
|---|---|---|
| `control.<domain>` | `pstack-ui` | the web UI |
| `api.<domain>` | `pstack-api` | the API |
| `loki.<domain>` | `pstack-loki` | Loki's push path, only with `--logging loki` |
```

- [ ] **Step 11: Package gate.**

```
cd /Volumes/S1/code/preview-stacks/packages/pstack && go test -race -timeout 120s ./internal/routing/ ./internal/autolabel/ ./internal/redact/ ./internal/api/
```

Expected: four `ok` lines. `internal/api` is here only as a regression check. Its wake test (http_test.go:327) still expects `control.`/`api.` to fall through, and nothing there changes.

- [ ] **Step 12: Commit.** Run `but status` to get the IDs for `packages/pstack/internal/routing/domains.go`, `packages/pstack/internal/routing/domains_test.go`, `packages/pstack/internal/autolabel/autolabel.go`, `packages/pstack/internal/autolabel/autolabel_test.go`, `packages/pstack/internal/redact/redact_test.go` and `docs/usage.md`. Commit only those files and never golden/host/**. Then:

```
but commit -b claude/loki-logging -m "fix(routing): loki. is a control hostname; refuse previews that claim one" <ids>
```

---

### Task 13: conformance: loki goldens, deploy injection over HTTP, ratchet

**Files:**
- Create: `packages/conformance/test/api-logging.test.ts`
- Create (generated, never hand-written): `packages/conformance/golden/cli/{cloud-init-loki,logging-loki-dry-dns01-advanced-swarm,init-dry-http01-basic-compose-loki,init-http01-basic-compose-loki,upgrade-plan-http01-basic-compose-loki,logging-off-dry-http01-basic-compose-loki,init-dry-dns01-advanced-swarm-loki,init-dns01-advanced-swarm-loki,upgrade-plan-dns01-advanced-swarm-loki,logging-off-dry-dns01-advanced-swarm-loki,swarm-status-loki,swarm-join-script-loki,swarm-join-cloud-config-loki}.json` (13)
- Create (generated): `packages/conformance/golden/render/control/http01-basic-compose-loki/{docker-compose.yml,.env,dns.env,loki/config.yaml}`
- Create (generated): `packages/conformance/golden/render/control/dns01-advanced-swarm-loki/{docker-compose.yml,.env,dns.env,loki/config.yaml}`
- Modify: `packages/conformance/gen/goldens.table.ts:5-8` (header), `:17` (LOKI_PASSWORD), `:24` (initArgv/initEnv/LOKI_CELLS), `:37-38` (LOKI_SHIM), `:90` (cloud-init-loki), `:92-106` (CELLS uses the helpers; logging-loki-dry row; LOKI_CELLS rows), `:113` (swarm-status-loki), `:116` (swarm-join loki rows)
- Modify: `packages/conformance/test/cli-goldens.test.ts:9-11` (header names loki/config.yaml)
- Modify: `packages/conformance/expected-pass.json:8-9` (api-logging row), `:22` (cli-goldens 81 → 94)
- Test: `packages/conformance/test/cli-goldens.test.ts` (replays the table; header is the only change), `packages/conformance/test/api-logging.test.ts`

Not in this task: `packages/conformance/golden/host/expected/swarm.json`. T7 (`"lokiPlugin": null`) and T11 (`lokiPluginInstall`) already hand-edited and committed it.

**Interfaces:**
- Consumes:
  - T3: `initctl.Options.Logging`; the `── 1c` plugin step `runner.Run(swarm.LokiPluginInstall, exec.RunOptions{Label: "loki log plugin"})`; `control/loki/config.yaml` (0644); the `.env` line `LOKI_PUSH_PASSWORD=<32 hex>`, read from `PSTACK_LOKI_PASSWORD`. Under `INIT_SHIM` the plugin line needs no new arm. `plugin inspect loki` hits the shim's `*) exit 0` (docker-shim.ts:31), so install is skipped. The empty `{{.Enabled}}` answer then runs `plugin enable loki`, which also exits 0.
  - T4: `upgrade.SwitchLogging(SwitchLoggingOptions) (bool, []Step, error)` and its `N deployment(s) still carry the loki driver` line. `initFlags` gains `--logging loki`; `initEnv` gains `PSTACK_LOKI_PASSWORD`.
  - T5: `cli.Parsed.Logging` (`--logging none|loki`) and `cloudinit.Answers.Logging`.
  - T6: `run.go case "logging"` (`pstack logging loki|off`; `-n` prints the plan).
  - T7: `inspect.LokiPushURL(r exec.Runner) string`. It issues `docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'` (inspect.go:187-188 `idsByLabel`), then `docker inspect 'l0k1'` (inspect.go:161-169 `inspectIDs`). After bash unquotes them, "$*" is `ps -aq --filter label=com.docker.compose.project=pstack-control` and `inspect l0k1`.
  - T7: `swarm.MarkLokiPlugins(r, &info)` issues `docker node inspect --format '{{json .}}' 'n1' 'n2'`, so "$*" is `node inspect --format {{json .}} n1 n2`. `swarm.SwarmReport`: when a node has `LokiPlugin == false`, after the table it prints `no loki log plugin on <hostnames> — logged services will not run there. Run on each:` and `  ` + `swarm.LokiPluginInstall`, then `add a worker:`.
  - T8: `autolabel.InjectLogging` and `MaterializeResult.LogNotes` (`logging: service <svc> has its own logging: — left alone`).
  - T9: `compose.ComposeUp` (signature unchanged).
    - The compose plugin check issues `docker plugin inspect -f '{{.Enabled}}' loki`, so "$*" is `plugin inspect -f {{.Enabled}} loki`.
    - When the check fails, ComposeUp first notes `logging: install the loki log plugin on this host: ` + `swarm.LokiPluginInstall`. It then returns `Stderr` `the loki log plugin is not installed and enabled on this host — install it (the line is in the log), then deploy again`, which `stack.Up` makes the compose step's message (stack.go:208-210).
    - The swarm branch notes `logging: node <hostname> has no loki log plugin, …`.
  - T10: `swarm.JoinScript(token, managerAddr string, logging bool)`, `JoinArgs.Logging`, `cloudinit.WorkerAnswers.Logging`.
  - T11: `pstack swarm status` runs `if len(info.Nodes) > 0 && inspect.LokiPushURL(runner) != "" { swarm.MarkLokiPlugins(runner, &info) }` before `swarm.SwarmReport(info)`. Today that spot is run.go:626-628. `/api/swarm` has its own conformance test in T11, and nothing here re-tests it.
  - T12 is an ordering dependency only. Go unit tests prove the reserved-host refusal.
- Produces:
  - `export const LOKI_PASSWORD = '0123456789abcdef0123456789abcdef';` (gen/goldens.table.ts)
  - `export const LOKI_SHIM: string` (gen/goldens.table.ts): two case arms, exactly as the contract signature.
  - 13 new `CASES` rows, which T14's CHANGELOG lists:
    - `cloud-init-loki`
    - `logging-loki-dry-dns01-advanced-swarm`
    - `init-dry-<cell>-loki`, `init-<cell>-loki`, `upgrade-plan-<cell>-loki` and `logging-off-dry-<cell>-loki`, for cells `http01-basic-compose` and `dns01-advanced-swarm`
    - `swarm-status-loki`
    - `swarm-join-script-loki`
    - `swarm-join-cloud-config-loki`
  - `describe('Loki logging: injection at deploy', …)` with 3 tests in `test/api-logging.test.ts`.
  - `expected-pass.json`: `"test/api-logging.test.ts": 3`, `"test/cli-goldens.test.ts": 94`.

Row order is the contract. Nothing reads `after:`. gen/goldens.ts:34 and cli-goldens.test.ts:41 both walk the table in position order.
- `logging-loki-dry-dns01-advanced-swarm` goes right after the CELLS flatMap. The row above it, `ui-switch-dry-dns01-advanced-swarm`, is a dry run, so the data dir still holds `init-dns01-advanced-swarm`, a logging-off host.
- `LOKI_CELLS` comes next. The row after it, `init-generated-token`, uses freshData, so no existing row sees a different data dir.
- `swarm-status-loki` sits right after `swarm-status`. Neither reads the data dir.

- [ ] **Step 1: Write the failing test — the golden table rows**

  Every edit in this step except the last is in `/Volumes/S1/code/preview-stacks/packages/conformance/gen/goldens.table.ts`.

  Header (lines 5-8). Replace:
  ```ts
   * Determinism: every case pins what would otherwise vary — the data dir (`<DATA>`), the token, the
   * DNS credential, the cloud-init password and SSH key — and the consumer masks the implementation's
   * own version to `<VERSION>` so a binary of any version is graded against the same transcripts.
  ```
  with:
  ```ts
   * Determinism: every case pins what would otherwise vary — the data dir (`<DATA>`), the token, the
   * DNS credential, the Loki push password, the cloud-init password and SSH key — and the consumer
   * masks the implementation's own version to `<VERSION>` so a binary of any version is graded
   * against the same transcripts.
  ```

  Replace:
  ```ts
  export const DNS_TOKEN = 'golden-dns-token-0123456789';
  ```
  with:
  ```ts
  export const DNS_TOKEN = 'golden-dns-token-0123456789';
  /** init takes it from PSTACK_LOKI_PASSWORD instead of generating one, so `.env` and the `{SHA}` label are fixed bytes. */
  export const LOKI_PASSWORD = '0123456789abcdef0123456789abcdef';
  ```

  Replace:
  ```ts
  export const cellName = (c: Cell) => `${c.challenge}-${c.ui}-${c.orchestrator}`;
  ```
  with:
  ```ts
  export const cellName = (c: Cell) => `${c.challenge}-${c.ui}-${c.orchestrator}`;
  const initArgv = (c: Cell) => ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--challenge', c.challenge, '--ui', c.ui, '--orchestrator', c.orchestrator, ...(c.challenge === 'dns01' ? ['--dns-provider', 'cloudflare'] : [])];
  const initEnv = (c: Cell) => ({ PSTACK_DATA: DATA_DIR, PSTACK_TOKEN: TOKEN, ...(c.challenge === 'dns01' ? { PSTACK_DNS_TOKEN: DNS_TOKEN } : {}) });

  /**
   * `--logging loki` on two cells, not eight: together they cover both TLS label forms (http01 adds
   * `tls.certresolver`) and both orchestrators, and logging varies with nothing else a cell varies.
   */
  // negative control: in initctl.LokiService, drop the HTTP01 `tls.certresolver=le` label and rebuild —
  // init-http01-basic-compose-loki fails on docker-compose.yml.
  const LOKI_CELLS: Cell[] = [
    { challenge: 'http01', ui: 'basic', orchestrator: 'compose' },
    { challenge: 'dns01', ui: 'advanced', orchestrator: 'swarm' },
  ];
  ```

  Replace the end of SWARM_SHIM (lines 37-38):
  ```ts
    `  "swarm join-token -q worker") printf '%s\\n' 'SWMTKN-1-abc-def' ;;`,
  ].join('\n');
  ```
  with:
  ```ts
    `  "swarm join-token -q worker") printf '%s\\n' 'SWMTKN-1-abc-def' ;;`,
  ].join('\n');

  /**
   * A host with logging on: the control stack's `loki` container and its push-url label, which is how
   * deploys and the join material find out (inspect.LokiPushURL). Arms are the argv after bash unquotes it.
   */
  export const LOKI_SHIM = [
    `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'l0k1' ;;`,
    `  "inspect l0k1") printf '%s\\n' '[{"Id":"l0k1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"loki","pstack.logging.push-url":"https://pstack:${LOKI_PASSWORD}@loki.preview.example.com/loki/api/v1/push"}},"State":{"Status":"running"}}]' ;;`,
  ].join('\n');
  ```

  Replace:
  ```ts
    { name: 'cloud-init-bad-distro', argv: [...CLOUD_INIT, '--distro', 'plan9'] },
  ```
  with:
  ```ts
    { name: 'cloud-init-loki', argv: [...CLOUD_INIT, '--logging', 'loki'] },
    { name: 'cloud-init-bad-distro', argv: [...CLOUD_INIT, '--distro', 'plan9'] },
  ```

  Replace the CELLS flatMap head (lines 92-95):
  ```ts
    ...CELLS.flatMap((c) => {
      const name = cellName(c);
      const argv = ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--challenge', c.challenge, '--ui', c.ui, '--orchestrator', c.orchestrator, ...(c.challenge === 'dns01' ? ['--dns-provider', 'cloudflare'] : [])];
      const env = { PSTACK_DATA: DATA_DIR, PSTACK_TOKEN: TOKEN, ...(c.challenge === 'dns01' ? { PSTACK_DNS_TOKEN: DNS_TOKEN } : {}) };
  ```
  with:
  ```ts
    ...CELLS.flatMap((c) => {
      const name = cellName(c);
      const argv = initArgv(c);
      const env = initEnv(c);
  ```

  Replace the CELLS flatMap tail (lines 104-106):
  ```ts
        { name: `ui-switch-dry-${name}`, argv: ['ui', c.ui === 'basic' ? 'advanced' : 'basic', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
      ];
    }),
  ```
  with:
  ```ts
        { name: `ui-switch-dry-${name}`, argv: ['ui', c.ui === 'basic' ? 'advanced' : 'basic', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
      ];
    }),
    // Position is what counts (`after` is documentation): the row above is a dry run, so the data dir
    // still holds init-dns01-advanced-swarm, a logging-off host.
    { name: 'logging-loki-dry-dns01-advanced-swarm', argv: ['logging', 'loki', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: 'init-dns01-advanced-swarm' },
    ...LOKI_CELLS.flatMap((c) => {
      const name = `${cellName(c)}-loki`;
      const argv = [...initArgv(c), '--logging', 'loki'];
      const env = { ...initEnv(c), PSTACK_LOKI_PASSWORD: LOKI_PASSWORD };
      return [
        { name: `init-dry-${name}`, argv: [...argv, '-n'], env, shim: INIT_SHIM, freshData: true } as Case,
        { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env', 'control/loki/config.yaml'] } } as Case,
        { name: `upgrade-plan-${name}`, argv: ['upgrade', '-n', '--to', '0.29.1'], env: { PSTACK_DATA: DATA_DIR, PSTACK_INSTALL_DIR: '/usr/local/bin' }, after: `init-${name}` } as Case,
        { name: `logging-off-dry-${name}`, argv: ['logging', 'off', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
      ];
    }),
  ```

  Replace:
  ```ts
    { name: 'swarm-status', argv: ['swarm'], shim: SWARM_SHIM },
  ```
  with:
  ```ts
    { name: 'swarm-status', argv: ['swarm'], shim: SWARM_SHIM },
    // Logging on: mgr lists the plugin (`loki:latest`, as an enabled one appears), wrk has none — the
    // report names wrk and the line to run there.
    // negative control: drop the swarm.MarkLokiPlugins call from run.go's `swarm status` — the
    // `no loki log plugin on wrk` lines vanish and this golden fails.
    { name: 'swarm-status-loki', argv: ['swarm'], shim: SWARM_SHIM + '\n' + LOKI_SHIM + '\n' + `  "node inspect --format {{json .}} n1 n2") printf '%s\\n' '{"ID":"n1","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"}]}}}' '{"ID":"n2","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}' ;;` },
  ```
  (`\\n` inside the backtick literal gives the shell `printf '%s\n'`, the same as every arm above.)

  Replace:
  ```ts
    { name: 'swarm-join-cloud-config-alpine', argv: ['swarm', 'join', '--format', 'cloud-config', '--distro', 'alpine'], shim: SWARM_SHIM },
  ```
  with:
  ```ts
    { name: 'swarm-join-cloud-config-alpine', argv: ['swarm', 'join', '--format', 'cloud-config', '--distro', 'alpine'], shim: SWARM_SHIM },
    // Logging on: both forms install the plugin before the join (cloud-config's step is a literal block).
    { name: 'swarm-join-script-loki', argv: ['swarm', 'join', '--format', 'script'], shim: SWARM_SHIM + '\n' + LOKI_SHIM },
    { name: 'swarm-join-cloud-config-loki', argv: ['swarm', 'join', '--format', 'cloud-config'], shim: SWARM_SHIM + '\n' + LOKI_SHIM },
  ```

  In `/Volumes/S1/code/preview-stacks/packages/conformance/test/cli-goldens.test.ts` (lines 9-11), replace:
  ```ts
   * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells) is
   * compared the same way: it is what `upgrade` reads back, and what keeps the letsencrypt volume
   * named the same across versions.
  ```
  with:
  ```ts
   * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells, plus
   * loki/config.yaml for the two Loki cells) is compared the same way: it is what `upgrade` reads
   * back, and what keeps the letsencrypt volume named the same across versions.
  ```

- [ ] **Step 2: Run it and watch it fail**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run typecheck && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
  ```
  Expected: typecheck is clean, and the test run ends `81 pass` / `13 fail`. Each new row fails on its first line, before it runs anything:
  `ENOENT: no such file or directory, open '/Volumes/S1/code/preview-stacks/packages/conformance/golden/cli/cloud-init-loki.json'`
  The other 12 names fail the same way. Every existing row passes.

- [ ] **Step 3: Implement — generate the goldens from the binary**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun gen/goldens.ts
  ```
  Leave PSTACK_IMPL unset or set to `go`; gen/goldens.ts:15 throws for any other value.

  Expected output: `pstack <version>`, then one `  <name>: exit <code>` line per row, ending `wrote 93 goldens` (80 + 13). Every new row prints `exit 0`. If a new row exits non-zero, the bug is in the task that owns that command. Stop and fix it there, and don't commit a golden of the failure.

  Then:
  ```sh
  but status
  but diff
  ```
  gen/goldens.ts:29-31 wipes and rewrites only `golden/cli` and `golden/render/control`. So those are the only golden paths the diff may show, and only as NEW files:
  - 13 in `golden/cli/`
  - 8 in the two `golden/render/control/*-loki/` dirs

  T7 and T11 already hand-edited and committed `golden/host/expected/swarm.json`, and this command never touches it.

  If an existing file under `golden/cli` or `golden/render/control` shows as modified, logging-off output has changed. That breaks the spec's "opt-in changes nothing". Stop, fix the owning task, and never commit over it. Ignore the untracked `golden/host/db/pstack.db-shm` and `pstack.db-wal`; they predate this work.

- [ ] **Step 4: Run it and watch it pass**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts
  ```
  Expected: `94 pass`, `0 fail`.

- [ ] **Step 5: Read the new goldens by eye**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance
  ls golden/cli/*loki*.json | wc -l                        # 13
  grep -L '"code": 0' golden/cli/*loki*.json               # prints nothing
  cd golden/render/control
  grep -n 'routers.pstack-loki.tls' http01-basic-compose-loki/docker-compose.yml   # tls=true AND tls.certresolver=le
  grep -n 'routers.pstack-loki.tls' dns01-advanced-swarm-loki/docker-compose.yml   # tls=true only
  grep -nE 'networks: \[preview-ingress, logs\]|^  loki:$|^  logs: \{\}$' */docker-compose.yml   # per file: 1 networks line, 2 `  loki:` (service + volume), 1 `  logs: {}`
  grep -c 'basicauth.users=pstack:{SHA}sXdaeF8JpuuvLcM9bq65iXTZzbg=' */docker-compose.yml         # 1 each
  tail -n 1 */.env                                         # LOKI_PUSH_PASSWORD=0123456789abcdef0123456789abcdef
  sed -n 132,182p /Volumes/S1/code/preview-stacks/docs/loki-logging-design.md | diff - http01-basic-compose-loki/loki/config.yaml && diff http01-basic-compose-loki/loki/config.yaml dns01-advanced-swarm-loki/loki/config.yaml && echo config byte-exact
  ```
  `sXdaeF8JpuuvLcM9bq65iXTZzbg=` is the output of `printf %s 0123456789abcdef0123456789abcdef | openssl sha1 -binary | base64`. Design doc lines 132-182 are the spec's config block (`auth_enabled: false` … `  reporting_enabled: false`). Only T14 edits that file, and T14 runs later.

  Transcripts:
  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance
  grep -c -- '--logging loki' golden/cli/upgrade-plan-*-loki.json golden/cli/cloud-init-loki.json   # >= 1 each (argv holds '--logging','loki' as separate strings, so a hit is stdout)
  grep -c 'PSTACK_LOKI_PASSWORD' golden/cli/upgrade-plan-*-loki.json                               # >= 1 each (their env field does not hold it)
  grep -o 'off → loki' golden/cli/logging-loki-dry-dns01-advanced-swarm.json
  grep -oE 'loki → off|0 deployment\(s\) still carry the loki driver' golden/cli/logging-off-dry-*-loki.json   # both, for both files
  grep -c loki golden/cli/swarm-status.json golden/cli/swarm-join-script.json golden/cli/swarm-join-cloud-config.json golden/cli/cloud-init-ubuntu.json   # 0 each
  bun -e "const o = (await Bun.file('golden/cli/swarm-status-loki.json').json()).stdout; const f = o.indexOf('no loki log plugin on wrk'); console.log(f > -1 && f < o.indexOf('add a worker:'), o.includes('docker plugin install grafana/loki-docker-driver:3.7.7-\$arch --alias loki --grant-all-permissions LOG_LEVEL=warn'), o.includes('no loki log plugin on mgr'))"
  bun -e "for (const n of ['swarm-join-script-loki', 'swarm-join-cloud-config-loki']) { const o = (await Bun.file('golden/cli/' + n + '.json').json()).stdout; const p = o.indexOf('docker plugin install'), j = o.indexOf('docker swarm join --token'); console.log(n, p > -1 && p < j, o.includes('loki plugin not installed: logged services will not run here')) }"
  bun -e "const o = (await Bun.file('golden/cli/cloud-init-loki.json').json()).stdout; console.log(o.split('\n').filter((l) => l.includes('pstack init ')).join('\n'))"
  ```
  Expected:
  - swarm-status-loki prints `true true false`: wrk is named before `add a worker:` with the install line, and mgr is not named.
  - Both swarm-join files print `swarm-join-*-loki true true`.
  - Every printed cloud-init line that runs `pstack init` ends with `--logging loki`.

  If anything reads wrong, fix the owning task, rebuild, and go back to Step 3.

- [ ] **Step 6: Break two goldens (negative controls)**

  Apply each mutation on its own. Rebuild, run the one case and watch it fail, then undo the edit by hand, rebuild and rerun it to see it pass.

  Build command:
  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  ```
  1. In `packages/pstack/internal/initctl/init.go` `LokiService`, delete the line that adds `traefik.http.routers.pstack-loki.tls.certresolver=le` under HTTP01. Run:
     ```sh
     cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts -t 'init-http01-basic-compose-loki'
     ```
     Expected: `1 fail`, because the docker-compose.yml comparison is missing `…pstack-loki.tls.certresolver=le`. After undoing the edit: `1 pass`.
  2. In `packages/pstack/internal/cli/run.go` `swarmCmd` `case "status"`, delete the `swarm.MarkLokiPlugins(runner, &info)` call that T11 added. If that leaves the gate's `if` block empty, delete the block too. Run:
     ```sh
     cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=go bun test test/cli-goldens.test.ts -t 'swarm-status-loki'
     ```
     Expected: `1 fail`. The stdout comparison is missing the `no loki log plugin on wrk — …` line and the install line. After undoing the edit: `1 pass`.

  After both: `but status` lists nothing under `packages/pstack/`.

- [ ] **Step 7: Write the failing test — injection at deploy**

  The file below holds three tests:
  - (A) `a compose deploy gives every service without its own logging the loki block`
  - (B) `a compose deploy on a host without the plugin fails before up, with the install line`
  - (C) `a swarm deploy keeps the block through the conversion and names the node without the plugin`

  Create `/Volumes/S1/code/preview-stacks/packages/conformance/test/api-logging.test.ts`:
  ```ts
  /**
   * Loki logging at deploy time, over HTTP.
   *
   * Logging is on when the control stack runs a `loki` container carrying the push-url label; the
   * docker shim plays that container (LOKI_SHIM, shared with the goldens). What only a real process
   * shows: the derived compose file a deploy hands docker, read back from disk; the plugin check that
   * stops a compose deploy before `up`; and the swarm job log naming the node without the plugin.
   */
  import { describe, expect, test } from 'bun:test';
  import { readFileSync } from 'node:fs';
  import { join } from 'node:path';
  import { bootServer, waitJob, type Booted } from '../harness/server.ts';
  import { arm, dockerShim } from '../harness/docker-shim.ts';
  import { LOKI_PASSWORD, LOKI_SHIM, SWARM_SHIM } from '../gen/goldens.table.ts';

  type Job = {
    state: string;
    outcome?: { steps: Array<{ phase: string; message?: string }> };
    log?: Array<{ level: string; message: string }>;
  };

  /** A service with no `logging:` beside one with json-file, which must come through untouched. */
  const COMPOSE = 'services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    logging:\n      driver: json-file\n';

  /** The injected block in its fixed key order. Every option is a string: the driver reads nothing else. */
  const block = (stack: string, service: string) => ({
    driver: 'loki',
    options: {
      'loki-url': `https://pstack:${LOKI_PASSWORD}@loki.preview.example.com/loki/api/v1/push`,
      'loki-external-labels': `service_name=${stack}-${service}`,
      'loki-relabel-config': '[{action: labeldrop, regex: filename}]',
      'loki-retries': '2',
      'loki-timeout': '1s',
      'loki-max-backoff': '800ms',
      mode: 'non-blocking',
      'keep-file': 'false',
      'max-size': '10m',
      'max-file': '3',
    },
  });

  describe('Loki logging: injection at deploy', () => {
    const plugin = (enabled: 'true' | 'false') => arm('plugin inspect -f {{.Enabled}} loki', enabled);

    // A short readiness watch: an ok `up` hands off to one, and the 180s default would outlive the test.
    const boot = (docker: { dir: string }) => bootServer({ tag: 'loki', pathPrefix: docker.dir, readiness: { pollMs: 20, timeoutMs: 200 } });

    /** PUT the stack with COMPOSE, POST up, wait for the job to settle. */
    const deploy = async (s: Booted, id: string, orchestrator: 'compose' | 'swarm'): Promise<Job> => {
      const put = await fetch(`${s.base}/api/deployments/${id}`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({ spec: `version: 1\nstack: ${id}\ncompose:\n  file: compose.yml\n  orchestrator: ${orchestrator}\naxes: []\n`, compose: COMPOSE }),
      });
      expect(put.status).toBe(201);
      const up = await fetch(`${s.base}/api/deployments/${id}/up`, { method: 'POST', headers: s.H });
      expect(up.status).toBe(202);
      const { job } = (await up.json()) as { job: { id: string } };
      return (await waitJob(s, job.id)) as Job;
    };

    /** The derived file docker was given: JSON despite the extension, and unredacted, as docker reads it. */
    const generated = (s: Booted, id: string) =>
      (JSON.parse(readFileSync(join(s.dataDir, 'deployments', id, 'compose.generated.yml'), 'utf8')) as { services: Record<string, { logging?: unknown }> }).services;

    const said = (job: Job) => (job.log ?? []).map((e) => e.message);

    // negative control: make autolabel.InjectLogging's `svc.Has("logging")` check always false — db's
    // json-file block is replaced by loki's and the db assertion fails.
    test('a compose deploy gives every service without its own logging the loki block', async () => {
      const docker = dockerShim(`${LOKI_SHIM}\n${plugin('true')}`);
      const s = await boot(docker);
      try {
        const job = await deploy(s, 'lg-compose', 'compose');
        expect(job.state).toBe('ok');
        const services = generated(s, 'lg-compose');
        // Stringified so the key order is asserted too: JSON.parse keeps the file's order.
        expect(JSON.stringify(services.web!.logging)).toBe(JSON.stringify(block('lg-compose', 'web')));
        expect(services.db!.logging).toEqual({ driver: 'json-file' });
        expect(said(job).some((m) => m.startsWith('logging: service db has its own logging:'))).toBe(true);
        expect(said(job).some((m) => m.startsWith('logging: service web'))).toBe(false);
      } finally {
        await s.stop();
        docker.remove();
      }
    }, 30_000);

    // negative control: make compose.ComposeUp's plugin check pass whatever docker prints — `compose up`
    // runs and the job is ok.
    test('a compose deploy on a host without the plugin fails before up, with the install line', async () => {
      const docker = dockerShim(`${LOKI_SHIM}\n${plugin('false')}`, { record: true });
      const s = await boot(docker);
      try {
        const job = await deploy(s, 'lg-noplugin', 'compose');
        expect(job.state).toBe('failed');
        // One sentence, not dockerd's container-create error. The install line (344 characters) is
        // longer than the step message's 300-character cap (stack.firstLine), so it goes to the job log whole.
        const step = job.outcome!.steps.find((st) => st.phase === 'compose');
        expect(step?.message).toStartWith('the loki log plugin is not installed and enabled on this host');
        expect(said(job).some((m) => m.includes('docker plugin install grafana/loki-docker-driver:3.7.7-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn'))).toBe(true);
        expect(docker.calls()).toContain('plugin inspect -f {{.Enabled}} loki');
        expect(docker.calls().filter((c) => c.startsWith('compose ') && c.includes(' up '))).toEqual([]);
      } finally {
        await s.stop();
        docker.remove();
      }
    }, 30_000);

    // negative control: drop compose.ComposeUp's note for a node whose LokiPlugin is false — the job log
    // never names wrk.
    test('a swarm deploy keeps the block through the conversion and names the node without the plugin', async () => {
      // mgr (n1) lists the plugin the way an enabled one appears (`loki:latest`); wrk (n2) has none. No
      // `plugin inspect` arm: the swarm branch never runs the compose plugin check.
      const nodes = arm(
        'node inspect --format {{json .}} n1 n2',
        '{"ID":"n1","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"},{"Type":"Network","Name":"overlay"}]}}}\n' +
          '{"ID":"n2","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}',
      );
      const docker = dockerShim(`${SWARM_SHIM}\n${LOKI_SHIM}\n${nodes}`, { record: true });
      const s = await boot(docker);
      try {
        const job = await deploy(s, 'lg-swarm', 'swarm');
        expect(job.state).toBe('ok');
        expect(JSON.stringify(generated(s, 'lg-swarm').web!.logging)).toBe(JSON.stringify(block('lg-swarm', 'web')));
        const nodeNotes = said(job).filter((m) => m.startsWith('logging: node '));
        expect(nodeNotes).toHaveLength(1);
        expect(nodeNotes[0]!).toStartWith('logging: node wrk has no loki log plugin');
        // Swarm keeps logged services off wrk by itself; the note says why, and the deploy still runs.
        expect(docker.calls().some((c) => c.startsWith('stack deploy '))).toBe(true);
      } finally {
        await s.stop();
        docker.remove();
      }
    }, 30_000);
  });
  ```

- [ ] **Step 8: Run it against the null server and watch every test fail**

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && PSTACK_IMPL=null bun test test/api-logging.test.ts
  ```
  Expected: `0 pass`, `3 fail`. Each test fails at once with `expect(received).toBe(expected)`, `Expected: 201` / `Received: 200`: the null server answers `200 {}` to the PUT (harness/impl.ts:5).

- [ ] **Step 9: Run it against the binary and watch it pass**

  ```sh
  cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack
  cd packages/conformance && bun run typecheck && PSTACK_IMPL=go bun test test/api-logging.test.ts
  ```
  Expected: typecheck is clean, then `3 pass`, `0 fail`.

  If test (B) fails on the job-log assertion, check that T9's note line is present in ComposeUp. Do not change T9's Stderr.

- [ ] **Step 10: Break each deploy test once (negative controls)**

  Apply each mutation on its own. Rebuild with `cd /Volumes/S1/code/preview-stacks && go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`, run the one test and watch it fail. Then undo the edit by hand and rebuild.
  1. In `packages/pstack/internal/autolabel/autolabel.go` `InjectLogging`, change the `svc.Has("logging")` condition to `false`. Run `PSTACK_IMPL=go bun test test/api-logging.test.ts -t 'without its own logging'`. Expected: it fails at `expect(services.db!.logging).toEqual({ driver: 'json-file' })`, because the received value has `driver: "loki"`.
  2. In `packages/pstack/internal/compose/compose.go` `ComposeUp`, compose branch, change the plugin check's condition (`if strings.TrimSpace(res.Stdout) != "true" {`) to `if false {`. Run `PSTACK_IMPL=go bun test test/api-logging.test.ts -t 'fails before up'`. Expected: it fails at `expect(job.state).toBe('failed')` with `Received: "ok"`.
  3. In `packages/pstack/internal/compose/compose.go` `ComposeUp`, swarm branch, wrap the note for a node whose `LokiPlugin` is false in `if false { … }`. Run `PSTACK_IMPL=go bun test test/api-logging.test.ts -t 'names the node'`. Expected: it fails at `expect(nodeNotes).toHaveLength(1)` with `Received length: 0`.

  After all three, `but status` lists nothing under `packages/pstack/`. Rerun `PSTACK_IMPL=go bun test test/api-logging.test.ts` and expect `3 pass`.

- [ ] **Step 11: Ratchet the pass counts; prove no new test is vacuous**

  In `/Volumes/S1/code/preview-stacks/packages/conformance/expected-pass.json`, replace:
  ```json
      "test/api-jobs-and-containers.test.ts": 12,
      "test/api-openapi.test.ts": 4,
  ```
  with:
  ```json
      "test/api-jobs-and-containers.test.ts": 12,
      "test/api-logging.test.ts": 3,
      "test/api-openapi.test.ts": 4,
  ```
  and replace:
  ```json
      "test/cli-goldens.test.ts": 81,
  ```
  with:
  ```json
      "test/cli-goldens.test.ts": 94,
  ```
  Leave `test/api-share-sleep-swarm.test.ts` at T11's value.

  ```sh
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run ratchet && bun run vacuity
  ```
  Expected from ratchet (scripts/ratchet.ts:29-30): lines `test/api-logging.test.ts … 3/3 (recorded 3) complete` and `test/cli-goldens.test.ts … 94/94 (recorded 94) complete`, no `REGRESSED`, exit 0.

  Expected from vacuity (scripts/vacuity.ts:16): `null mode: … failed (good), … skipped (CLI-only), 0 vacuous`, exit 0.

- [ ] **Step 12: Commit**

  ```sh
  but status
  ```
  Take the IDs of exactly these files:
  - `packages/conformance/gen/goldens.table.ts`
  - `packages/conformance/test/cli-goldens.test.ts`
  - `packages/conformance/test/api-logging.test.ts`
  - `packages/conformance/expected-pass.json`
  - the 13 new `packages/conformance/golden/cli/*loki*.json`
  - the 8 files under `packages/conformance/golden/render/control/http01-basic-compose-loki/` and `…/dns01-advanced-swarm-loki/`

  Never take anything under `golden/host/**` (including the untracked `pstack.db-shm` and `pstack.db-wal`) or under `packages/pstack`.
  ```sh
  but commit -b claude/loki-logging -m "test(conformance): Loki logging goldens and deploy injection" <ids>
  ```

---

### Task 14: CHANGELOG, doc status, full gate, real-host checklist

**Files:**
- Modify: `packages/pstack/CHANGELOG.md:1-3` (insert `## Unreleased` between `# Changelog` and `## 0.39.1 — 2026-09-14`)
- Modify: `docs/README.md:91-98` (the `loki-logging-design.md` row)
- Modify: `docs/loki-logging-design.md:3-5` (the top banner)
- Test: none. This task only edits docs. Its checks are the fact greps in Step 1, then `bun run check`, `bun run vacuity` and `bun run ratchet`.

`packages/conformance/golden/host/expected/swarm.json` is not edited here. T7 added `lokiPlugin` and T11 added `lokiPluginInstall` to it. This task only checks that both are there and names the change in the CHANGELOG.

**Interfaces:**
- Consumes: T13's 12 new `CASES` rows (`cloud-init-loki`, `logging-loki-dry-dns01-advanced-swarm`, `init-dry-<cell>-loki`, `init-<cell>-loki`, `upgrade-plan-<cell>-loki`, `logging-off-dry-<cell>-loki` for `http01-basic-compose` and `dns01-advanced-swarm`, `swarm-join-script-loki`, `swarm-join-cloud-config-loki`), a 13th row `swarm-status-loki` if T13 added it, and its two render cells. Also T7's and T11's hand edits to `golden/host/expected/swarm.json`. The entry also names what T1–T12 produced: `--logging`/`PSTACK_LOGGING` (T5), `PSTACK_LOKI_PASSWORD`/`LOKI_PUSH_PASSWORD` (T3), `pstack logging loki|off` (T6), the join-material plugin step (T10), `/api/swarm` `lokiPluginInstall` + per-node `lokiPlugin` (T7, T11), the `loki.` control hostname and the reserved-host refusal (T12), and the compose plugin check (T9).
- Produces: the `## Unreleased` CHANGELOG entry and the "slice 1 built" status in both docs. No code identifiers.

The house precedent (commit `6e365cc`) is that a feature commit adds `## Unreleased` and the maintainer's `chore(release)` commit renames it. No release tooling parses the file: `.goreleaser.yaml:58-59` has `changelog: disable: true`.

- [ ] **Step 1: Check the facts the entry states (no test file: docs only)**

  The entry must describe what T1–T13 actually built, not the contract. Run these read-only checks first. If any output differs from what is expected below, change the prose in Steps 2–4 to match the code and note the difference in the task report. Do not edit code in this task.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/pstack
  grep -n '"logging"' internal/cli/args.go
  grep -n 'PSTACK_LOGGING' internal/cli/args.go
  grep -n 'PSTACK_LOKI_PASSWORD' internal/initctl/init.go
  grep -n 'the Loki push password' internal/cli/initguard.go
  grep -n 'lokiPluginInstall' internal/api/routes.go
  grep -n 'json:"lokiPlugin"' internal/swarm/swarm.go
  grep -n '"loki."' internal/routing/domains.go
  grep -n 'ControlHostname(req.Host)' internal/autolabel/autolabel.go
  grep -n -e 'loki plugin not installed' -e 'already part of a swarm' internal/swarm/swarm.go
  grep -c 'lokiPlugin' api/openapi.yaml
  grep -n '••••@' internal/redact/redact.go
  ```

  Expected:
  - Each of the first eight greps prints at least one line.
  - The join grep prints the `loki plugin not installed` line at a lower line number than `already part of a swarm` (today that is `swarm.go:966`). So re-running the script on a worker that already joined still installs the plugin.
  - `grep -c` on openapi.yaml prints `0`.
  - The redact grep prints `out = urlPassword.ReplaceAllString(out, "${1}:••••@")` (today `redact.go:167`).

  ```bash
  cd /Volumes/S1/code/preview-stacks
  grep -n -e '"lokiPlugin": null' -e '"lokiPluginInstall"' packages/conformance/golden/host/expected/swarm.json
  ```

  Expected: exactly two lines. First `"lokiPlugin": null`, on the line right after `"self": true,` in the `fixture-mgr` node. Then `"lokiPluginInstall": "arch=$(uname -m); …`, after the `ports` array and before `"note"`. If either is missing, `test/host-fixture.test.ts` fails in Step 5. That golden belongs to T7 (`lokiPlugin`) or T11 (`lokiPluginInstall`): hand-edit it and amend it into that commit (see Step 5). Never regenerate it with `gen/host-fixture.ts`.

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance
  ls golden/cli | grep loki
  ls golden/render/control | grep loki
  grep -l -i -E 'logging|loki' golden/cli/*.json | grep -v loki
  grep -rl -i -E 'logging|loki' golden/render | grep -v -- '-loki/'
  grep -rl -i -E 'logging|loki' golden/host/expected
  grep -n -E 'cli-goldens|api-logging|api-share-sleep-swarm' expected-pass.json
  ```

  Expected:
  - The first command lists exactly these 12, in any order: `cloud-init-loki.json`, `init-dns01-advanced-swarm-loki.json`, `init-dry-dns01-advanced-swarm-loki.json`, `init-dry-http01-basic-compose-loki.json`, `init-http01-basic-compose-loki.json`, `logging-loki-dry-dns01-advanced-swarm.json`, `logging-off-dry-dns01-advanced-swarm-loki.json`, `logging-off-dry-http01-basic-compose-loki.json`, `swarm-join-cloud-config-loki.json`, `swarm-join-script-loki.json`, `upgrade-plan-dns01-advanced-swarm-loki.json`, `upgrade-plan-http01-basic-compose-loki.json`. If T13 added `swarm-status-loki`, there are 13, the 13th being `swarm-status-loki.json`. Note which case holds: Steps 2 and 7 depend on it.
  - The second lists `dns01-advanced-swarm-loki` and `http01-basic-compose-loki`.
  - The third lists exactly `golden/cli/help-h.json`, `golden/cli/help.json`, `golden/cli/no-args.json` and `golden/cli/unknown-command.json`. Before this feature no golden mentioned logging or loki; only these four print the usage text or the command list.
  - The fourth prints nothing, so no existing render cell mentions Loki.
  - The fifth prints exactly `golden/host/expected/swarm.json`.
  - The last shows `"test/api-logging.test.ts": 3` and `"test/api-share-sleep-swarm.test.ts": 10`. It also shows `"test/cli-goldens.test.ts": 93`, or `94` if T13 added `swarm-status-loki`.

- [ ] **Step 2: Write the CHANGELOG entry**

  In `packages/pstack/CHANGELOG.md`, replace lines 1-3:

  ```markdown
  # Changelog

  ## 0.39.1 — 2026-09-14
  ```

  with:

  ```markdown
  # Changelog

  ## Unreleased

  ### Added

  - **`--logging loki` on `init` and `cloud-init`** (`PSTACK_LOGGING`, default `none`). The control
    stack gains a Loki container (`grafana/loki:3.7.7`, 7-day retention, a fixed config at
    `control/loki/config.yaml`) that nodes push to at `https://loki.<domain>/loki/api/v1/push`,
    through Traefik, with basic auth. The push password is 32 hex characters generated on the first
    run and kept in `control/.env` as `LOKI_PUSH_PASSWORD`; `PSTACK_LOKI_PASSWORD` supplies one, and
    there is no flag for it, because argv is readable through `ps`. `init` installs the Grafana Loki
    Docker plugin on the manager and fails if it cannot: under compose every logged service would
    fail to create. `cloud-init --logging loki` puts the flag on the `init` call it writes.
  - **`pstack logging loki|off`** switches a host that already exists. Like `pstack ui`, it re-runs
    `init` from the saved state — the token, the DNS token and the push password travel as env — and
    `-n` prints the plan. `off` prints how many deployments still carry the driver: each drops it on
    its next deploy, a sleeping one on wake, and until then its pushes get a 404, which is not retried,
    so stops are not delayed. `pstack upgrade` keeps Loki and its password.
  - **Every deployed service without a `logging:` key ships its logs to Loki**, on both
    orchestrators, labelled `service_name=<stack>-<service>`. Whether to inject is read from the
    running control stack's `loki` container, so `pstack up` on the host and the API always agree. A
    service with its own `logging:` (`json-file` counts) is left alone and named in the job log. Every
    option but the URL and the label is a constant: when Loki is unreachable the driver holds a
    node-wide lock while it retries, and only small retries, timeout and backoff keep a `docker stop`
    on that node to seconds.
  - **The plugin on new workers, and the nodes without it.** On a logging-on host, `pstack swarm join
    --format script|cloud-config` and `/api/swarm/join` install the plugin before joining, and a
    failure never blocks the join; a worker that joined earlier gets the plugin by running the script
    again. A compose deploy checks the plugin before `up` and fails with the install line; a swarm
    deploy names every node without it in the job log. The Swarm page and `pstack swarm` flag those
    nodes with the line to run there. `/api/swarm` gains `lokiPluginInstall` and a per-node
    `lokiPlugin` (`null` when logging is off or docker did not answer).
  - **`init` refuses a re-run that would drop Loki or mint a new push password** — two new cases for
    the silent-revert guard. A new password would get every running container's pushes refused
    until it is redeployed.

  ### Changed

  - **A compose stack on a logging-on host runs from `compose.generated.yml`** whenever a service
    got the logging block, even with no `pstack.routing.*` labels. With logging off nothing changes.
  - **`loki.<domain>` is a control hostname**, on the primary and every added domain, always — with
    logging off too, so it never answers with a waking page.
  - **Goldens.** `help`, `help-h`, `no-args` and `unknown-command` were regenerated for the new flag
    and command (`bun gen/goldens.ts`). `golden/host/expected/swarm.json` was edited by hand: every
    node gains `lokiPlugin` (`null` on a logging-off host) and the body gains `lokiPluginInstall`,
    because `GET /api/swarm` now carries both on every host. New transcripts: `cloud-init-loki`;
    `logging-loki-dry-dns01-advanced-swarm`; `init-dry-`, `init-`, `upgrade-plan-` and
    `logging-off-dry-` for `http01-basic-compose-loki` and `dns01-advanced-swarm-loki`;
    `swarm-join-script-loki` and `swarm-join-cloud-config-loki`. New render cells:
    `http01-basic-compose-loki` and `dns01-advanced-swarm-loki`. The existing render cells, the
    cloud-init goldens and the swarm-join goldens stay byte-identical.

  ### Fixed

  - **A preview could claim a control hostname.** A deploy whose `pstack.routing.host` is `control.`,
    `api.` or `loki.` of the primary or an added domain is now refused. For `control.` and `api.` the
    hole predates Loki; for `loki.` it would have handed a preview every node's pushes, password
    included.

  ## 0.39.1 — 2026-09-14
  ```

  If Step 1 listed `swarm-status-loki.json`, replace this line of the Goldens bullet:

  ```markdown
    `swarm-join-script-loki` and `swarm-join-cloud-config-loki`. New render cells:
  ```

  with:

  ```markdown
    `swarm-join-script-loki`, `swarm-join-cloud-config-loki` and `swarm-status-loki`. New render cells:
  ```

- [ ] **Step 3: Mark slice 1 built in the design doc**

  In `docs/loki-logging-design.md`, replace lines 3-5:

  ```markdown
  > **Nothing in it is built.** Slice 1 (`--logging loki`) is specified below and was approved section
  > by section on 2026-09-14. Slices 2 and 3 record the decisions already taken; each gets its own spec
  > before it is built. Three releases, in order — the risky runtime part lands and is exercised first.
  ```

  with:

  ```markdown
  > **Slice 1 (`--logging loki`) is built (Unreleased); slices 2 and 3 are not.** Using it:
  > [usage.md](usage.md), `pstack logging`. Slice 1's spec below is kept as approved section by section
  > on 2026-09-14. Where the build differs from it:
  >
  > - `init` also refuses a re-run that would mint a new push password: running containers' pushes
  >   would be refused until redeployed.
  > - The `no-args` and `unknown-command` goldens changed too: both print the command list.
  > - `/api/swarm` gains `lokiPlugin` and `lokiPluginInstall` on every host, logging off included, so
  >   the host-fixture golden (`golden/host/expected/swarm.json`) changed.
  > - `/api/swarm` needed no `openapi.yaml` or `apicli` change: its response is untyped.
  > - Re-running the join script on a worker that joined earlier installs the plugin.
  > - Masked userinfo is `://user:••••@`, the mask `redact` already used.
  >
  > Slices 2 and 3 record the decisions already taken; each gets its own spec before it is built.
  ```

  If Step 1 showed a different outcome for any bullet, rewrite or drop that bullet to match the code.

- [ ] **Step 4: Match the docs index row**

  In `docs/README.md`, replace lines 91-98:

  ```markdown
  ### [`loki-logging-design.md`](loki-logging-design.md) — a design in three slices, none built yet

  **Nothing in it is built.** Loki as a logging option: a Loki container in the control stack, the
  Grafana Loki Docker plugin on every node, and a `logging:` block pstack adds to every deployed
  service that has none — pushed through Traefik with basic auth. Slice 1 is specified in full;
  slices 2 (Loki settings in the UI: chunking, retention, filesystem or S3) and 3 (Grafana with pstack
  sign-in) record their decisions and get their own specs. Read before touching logging, the control
  template, or the join material.
  ```

  with:

  ```markdown
  ### [`loki-logging-design.md`](loki-logging-design.md) — a design in three slices, slice 1 built

  **Slice 1 (`--logging loki`) is built (Unreleased); slices 2 and 3 are not.** Loki as a logging
  option: a Loki container in the control stack, the Grafana Loki Docker plugin on every node, and a
  `logging:` block pstack adds to every deployed service that has none — pushed through Traefik with
  basic auth. Slice 1's spec is kept as approved, with where the build differs at the top; slices 2
  (Loki settings in the UI: chunking, retention, filesystem or S3) and 3 (Grafana with pstack sign-in)
  record their decisions and get their own specs. Read before touching logging, the control template,
  or the join material.
  ```

  The bold first sentence is the design banner's lead, word for word.

- [ ] **Step 5: Run the full gate**

  ```bash
  cd /Volumes/S1/code/preview-stacks && bun run check
  ```

  This takes several minutes (Bash timeout 600000, or run_in_background). It builds `packages/pstack/bin/pstack` first (turbo `check` → `build`). Then it runs Go vet + `go test -race`, every package's bun tests and typecheck, the conformance suite in go mode (including `test/host-fixture.test.ts`, which compares `GET /api/swarm` with `golden/host/expected/swarm.json`), and the `apps/ui` `vue-tsc --build`.

  Expected: turbo ends with `Tasks:    <n> successful, <n> total`, the two numbers equal, and the command exits 0.

  A failure belongs to the task that owns the failing package or golden. Fix it there and amend it into that task's commit (`but status -fv`, then `but amend -t <that-commit-id> <file-id>`), not into this docs commit.

- [ ] **Step 6: Vacuity — no new test passes against the null server**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run vacuity
  ```

  Expected: the last line is `null mode: <n> failed (good), <n> skipped (CLI-only), 0 vacuous`, and it exits 0. Any test it lists goes back to its task: `test/api-logging.test.ts` to T13, the new `api-share-sleep-swarm` test to T11.

- [ ] **Step 7: Ratchet — counts only go up, and are recorded**

  ```bash
  cd /Volumes/S1/code/preview-stacks/packages/conformance && bun run ratchet
  ```

  Expected: exit 0, with no `REGRESSED` row, no `advanced` row and no `run with --write` line. The rows include `test/api-logging.test.ts … 3/3 (recorded 3) complete`, `test/api-share-sleep-swarm.test.ts … 10/… (recorded 10)`, `test/host-fixture.test.ts … (recorded 34)` and `test/cli-goldens.test.ts … 93/93 (recorded 93) complete`. If T13 added `swarm-status-loki`, the last row reads `94/94 (recorded 94)`.

  If a row says `advanced`, T13 or T11 under-recorded `expected-pass.json`. Run `bun scripts/ratchet.ts --write`, then `but status -fv`, then `but amend -t <T13-commit-id> <expected-pass.json-file-id>`. The fix belongs in that commit, not this one.

- [ ] **Step 8: Commit**

  ```bash
  cd /Volumes/S1/code/preview-stacks && but status -fv
  ```

  Take the file IDs of exactly three files: `packages/pstack/CHANGELOG.md`, `docs/README.md` and `docs/loki-logging-design.md`. Leave out the untracked `packages/conformance/golden/host/db/pstack.db-shm` and `pstack.db-wal`, and anything in `packages/conformance/.status/`. `golden/host/expected/swarm.json` should not show as uncommitted here, because T7 and T11 committed it. If it does, amend it into T11's commit instead.

  ```bash
  but commit -b claude/loki-logging -m "docs(changelog): Loki logging, slice 1" <changelog-id> <readme-id> <design-id>
  ```

  No attribution footer. Do not push or tag.

- [ ] **Step 9: Hand the owner the real-host checks (in the task report, not a file)**

  Paste this into the final report as-is, under the heading **Real host, before the release — NOT RUN (pre-release)**:

  1. A throwaway host from `cloud-init --logging loki`; deploy `examples/preview.yml`; query Loki from the `logs` network for `{service_name="<stack>-web"}` and see its lines.
  2. Add a worker with the join script: the plugin is installed before the join, and a swarm task on the worker ships its logs.
  3. Stop Loki, then `pstack down` a stack: container stops take seconds, not minutes.
  4. `pstack logging off`: stops are not delayed; a redeploy removes the driver.
  5. `pstack upgrade`: Loki and its password survive.
  6. `journalctl -u docker` does not contain the password.

  Add one line for the maintainer: "Unreleased" now appears in `packages/pstack/CHANGELOG.md`, `docs/loki-logging-design.md` and `docs/README.md`. At release, replace all three with the version (`grep -rn Unreleased docs packages/pstack/CHANGELOG.md`).

---
