# Custom routes, Grafana links and the compact Hearth theme: implementation plan

**Spec:** docs/custom-routes-design.md (binding).
**Branch:** claude/custom-routes (GitButler).

## Global Constraints

- The spec binds. Where this plan and the spec disagree, follow the spec and say so in the report.
- Read AGENTS.md before touching an area. Follow its "how to add a route / event" checklists.
- Every shell first runs `export GOROOT=/usr/lib/go-1.23`. The profile's `/usr/local/go` doesn't exist on this machine.
- VCS writes go through `but` only. Commit with explicit file ids read from a separate `but status` call:
  `but commit -b cl -m "<type(scope): summary>" <ids>`. `cl` is the branch's short id, because two
  stacks are applied. One or more commits per task. Never `git add` or `git reset`.
- Never commit `bun.lock`, `.dns_token`, `hetzner_42.yaml`, `packages/conformance/golden/host/**`
  (including `pstack.db-{shm,wal}`), `packages/conformance/gen/host-fixture.ts`, anything under
  `.superpowers/`, credentials or sealed files. Commits carry no attribution footers.
- Never push, open PRs, publish, or touch the live host.
- Go: `gofmt` clean, `go vet ./packages/pstack/...` clean, and `go test -race -timeout 120s ./packages/pstack/...`
  green. Every new Test/t.Run names its negative control in a `// negative control:` comment that was
  actually tried.
- After any change to `packages/pstack/api/openapi.yaml`, run `go generate ./packages/pstack/internal/apicli`.
- Goldens:
  - Rebuild the binary first: `go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`.
  - Then `cd packages/conformance && bun gen/goldens.ts`. Never `bun run gen`.
  - No task regenerates `golden/host`. The v6 fixture db already proves the upgrade path, since
    `store.Open` migrates a copy.
- Conformance: `cd packages/conformance && bun test` green.
- UIs: `bun run --cwd apps/ui-hearth build && bun test --cwd apps/ui-hearth`; same for `apps/ui`.
- UI copy is terse: state, not explanation (docs/ui-rules.md). No `window.confirm`.
- Event names are append-only. Migrations are append-only. Reserved hostnames per invariant 22.
- Prefer existing helpers over new ones. The map of reusable code is in the spec's sections and in:
  - `internal/routing/domains.go` (renderDomains, tlsFor, SetDomains);
  - `internal/routing/wildcard.go` (IsReserved);
  - `internal/api/routes_domains.go` (reconcileDomains, domainOptions);
  - `internal/api/server.go` (reindex, the sleep index);
  - `internal/autolabel/autolabel.go` (host shape and router ids, MaterializeCompose);
  - `internal/store/migrations.go`;
  - `internal/registry/registry.go`.

### Task 1: custom_routes table and store

**Files:** `packages/pstack/internal/store/migrations.go`, a new `packages/pstack/internal/store/customroutes.go`
(+ `_test.go`).

**Interfaces:**
- Migration 10 creates `custom_routes(name TEXT PRIMARY KEY, deployment_id TEXT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`, epoch milliseconds like every sibling table.
- `type CustomRoute struct { Name string; DeploymentID string /* "" = unassigned */; CreatedAt, UpdatedAt int64 }`.
- `ListCustomRoutes(q Querier) ([]CustomRoute, error)`, ordered by name.
- `GetCustomRoute(q, name) (CustomRoute, bool, error)`.
- `CreateCustomRoute(q, name, deploymentID string) error`, returning `ErrCustomRouteExists` on a duplicate.
- `AssignCustomRoute(q, name, deploymentID string, force bool) (previous string, err error)`, returning
  `ErrCustomRouteHeld{DeploymentID}` when the route is held by another deployment and not forced. Same
  deployment is a no-op.
- `UnassignCustomRoute(q, name) (previous string, err error)`.
- `DeleteCustomRoute(q, name) (previous string, err error)`.
- `UnassignDeploymentRoutes(q, deploymentID) ([]string, error)` returns the names it unassigned.
- Use the store's transaction pattern (`store.Tx`) so assign-with-force is one transaction.
- Unit tests cover each function, held/forced/no-op paths, and ordering.

### Task 2: render pstack-routes.yml

**Files:** `packages/pstack/internal/routing/customroutes.go` (+ `_test.go`), `packages/pstack/internal/routing/wildcard.go` (IsReserved).

**Interfaces:**
- `const CustomRoutesYAML = "pstack-routes.yml"`, which `IsReserved` also refuses.
- Input is pure data:
  `type AliasTarget struct { Alias, Stack, Provider string /* "docker"|"swarm" */; Services []AliasService }`
  and `type AliasService struct { Name, Domain string; Wildcards []Wildcard /* whatever autolabel/spec uses for -wild */ }`.
- `RenderCustomRoutes(targets []AliasTarget, mode Challenge-or-equivalent, wildcardPin ...) (string, error)` builds
  one router per alias host as the spec describes, through the same `tlsFor`. It renders an empty
  but valid file when there are no targets.
- `(*RoutingStore) SetCustomRoutes(content string) error` uses the atomic `write`, and `remove` when
  there are no targets (mirror `SetDomains`).
- `AliasHosts(t AliasTarget) []string` gives the plain hosts for the API and the collision check.
- Golden-style unit tests cover compose and swarm provider qualifiers, a wildcard service, http01
  versus dns01 TLS blocks, an empty input, and Traefik-valid YAML (`ValidateRoutingContent`).

### Task 3: deriving a deployment's alias target

**Files:** `packages/pstack/internal/api/customroutes_target.go` (+ `_test.go`), or the package where
autolabel's per-service routing results are reachable without an import cycle.

**Interfaces:**
- `aliasTarget(dep *registry.Deployment, alias string) (routing.AliasTarget, error)` recomputes the
  routed services exactly as the deploy did, through the same autolabel/spec path the deploy uses
  (`MaterializeCompose` / `AugmentComposeDoc` and `spec.ResolveSubdomains`). It mirrors only services
  whose host is the generated `<svc>-<stack>.<domain>`. It skips explicit `pstack.routing.host` and
  hand-labelled services.
- It is total: an unreadable compose returns an error that the caller logs and skips.
- Tests use a registry fixture with two services, one explicit-host service, one wildcard and a swarm
  stack.

### Task 4: API handlers, permissions, openapi, events

**Files:**
- A new `packages/pstack/internal/api/routes_customroutes.go` (+ `_test.go`).
- `internal/api/routes.go`, `internal/api/permissions.go`.
- `packages/pstack/api/openapi.yaml`, plus the regenerated `internal/apicli` files.
- `internal/events/events.go`, `docs/webhook-events.md`.

**Interfaces:**
- The five operations of spec §API, with the status codes and bodies given there:
  - 409 bodies carry `assignedTo: {deployment, stack}` for a held route, or name the colliding host;
  - 400 for a bad name.
- Name checks: DNS label regex, reserved labels, equal to a registered stack name, and alias-host
  collision against every other deployment's hosts (live routers through `inspect` or the registry
  computation) and other routes.
- Every write ends with `s.renderCustomRoutes()`, which loads the table, builds the targets, writes
  through `SetCustomRoutes`, then calls `s.reindex()`. Take `s.writeMu` the way `domainsPut` does.
- openapi:
  - `operationId`s `listCustomRoutes`, `createCustomRoute`, `deleteCustomRoute`, `assignCustomRoute`,
    `unassignCustomRoute`;
  - `x-cli-name`s `custom-routes list|create|delete|assign|unassign`;
  - tag `custom-routes`;
  - `force` as a boolean body field.
- Permissions: Viewer GET; Developer POST, PUT and DELETE. Not added to `shareAllows`.
- Events `route.assigned` and `route.unassigned`, appended to `events.Names` with the payloads in the spec.
- Tests: Go handler tests for every status code and the move (force) path in one transaction, the
  permissions table, and openapi coverage.

### Task 5: lifecycle hooks: boot, deploy, forget, sleep

**Files:** `internal/api/server.go`, `internal/api/routes_domains.go` (or wherever boot reconcile
runs), the deploy-job completion path, `forgetAfterDown`, and their tests.

**Interfaces:**
- `reconcileCustomRoutes()` runs at boot beside `reconcileDomains()`. It is total on a nil or
  non-writable store.
- After a successful deploy or redeploy of a deployment that has routes, re-render.
- `forgetAfterDown` calls `UnassignDeploymentRoutes`, emits `route.unassigned` with
  `reason: forgotten` per route, and re-renders.
- `reindex()` adds the alias hosts of each sleeping deployment to its `SleepEntry`, so a request to an
  alias host wakes it. `previewHostname` and the wake catch-all must accept alias hosts, which are
  one label under a known domain like any preview host.
- Tests cover each hook, including a sleeping deployment woken through its alias host.

### Task 6: the `pstack route` command and conformance black-box tests

**Files:**
- `packages/pstack/internal/cli/` (a new `route.go` + `_test.go`, `args.go`, `run.go`, `commands.go`,
  `completion.go`).
- A new `packages/conformance/test/api-custom-routes.test.ts`, the harness only if needed.
- Goldens regenerated for the help and CLI changes (`help.json`, `help-h.json`, `no-args.json`,
  `unknown-command.json`, and any others the change moves). Regenerate only through `bun gen/goldens.ts`.

**Interfaces:**
- `pstack route ls|create|assign|unassign|rm` exactly as spec §CLI:
  - positional args, `--force` and `--json`;
  - it reaches the server through the existing `apiBase` (as `pull config` does);
  - terse one-line-per-route output;
  - a held route exits non-zero naming the holder and `--force`;
  - bad usage exits 3.
- AGENTS.md "new CLI command" checklist: `ParseArgs` + `Usage()`, a `commandHelps` entry,
  `completion.go`, and the help goldens.
- Go tests cover each subcommand against an `httptest` server, held/forced/unassigned output, usage
  errors, and the missing-`PSTACK_API_URL` refusal.
- Conformance, against the real binary with the fake docker:
  - create, list, assign, 409 when held, `force` move (one call, and the file shows only the new
    target), unassign, delete;
  - a bad name, a reserved name, and a name equal to a stack;
  - an unknown deployment (404);
  - the role gates (viewer refused on writes);
  - a share principal refused;
  - `pstack-routes.yml` contents after each step;
  - reserved: PUT `/api/routing/pstack-routes.yml` refused;
  - forget and DELETE both unassign.
- CLI cases in the conformance suite:
  - `pstack route assign … --force`, `pstack route ls` and the held refusal;
  - one generated `pstack api custom-routes assign … --force`.
- Each test names its negative control in a comment, as the suite does.

### Task 7: Hearth custom routes UI

**Files:**
- `apps/ui-hearth/src/views/RoutingView.vue` (a new "Custom routes" section).
- The deployment page: `views/DeploymentView.vue` or an Overview tab card.
- A new `apps/ui-hearth/src/lib/customRoutes.ts` (+ `test/customRoutes.test.ts`).
- API client additions where Hearth keeps them.

**Interfaces:**
- List, create, assign (deployment select), the move confirm (`HAlertDialog`: "Move <name> from
  <stackX> to <stackY>?", which sends one `PUT … {force:true}`), unassign and delete with a confirm.
- The deployment page shows the assigned routes with their hosts as links, plus Assign route and Unassign.
- The lib holds pure logic: whether an assign needs a move confirm, the confirm copy, and host-list
  formatting. It is unit-tested.
- Writes are hidden below Developer.

### Task 8: advanced UI custom routes

**Files:** `apps/ui/src/views/RoutingView.vue` (or the routing page there), the deployment view in
`apps/ui`, `apps/ui/src/lib/customRoutes.ts` (+ test), and the client.

**Interfaces:**
- Same behaviour as Task 7.
- The move confirm uses the armed `ActionButton` confirm ("Move from <stackX>?"), never `window.confirm`.
- The terse copy and role gates follow the spec.

### Task 9: Grafana deep links in both UIs

**Files:**
- `apps/ui-hearth/src/lib/grafana.ts` (+ test) and `apps/ui/src/lib/grafana.ts` (+ test).
- The Hearth deployment header and per-service logs (`views/deployment/*`, `DeploymentView.vue`),
  and the matching `apps/ui` views.
- Move Hearth's `grafana` ref from `App.vue` into `state.ts` if the views need it.

**Interfaces:**
- `exploreUrl(base: string, query: string, from = 'now-1h'): string` builds Grafana Explore with
  datasource uid `loki`.
- `stackQuery(stack, services[])` gives `{service_name=~"<stack>-(a|b)"}`, regex-escaped.
- `serviceQuery(stack, svc)` gives `{service_name="<stack>-<svc>"}`.
- A link shows only when Grafana is present and the user passes the existing sidebar gate (developer
  and up, no token-only session). It never appears on `PublicLogsView`.
- Unit tests cover escaping (`a` versus `a-b`, dots and pluses), the URL shape, and the gate function.

### Task 10: compact Hearth default and smaller radius

**Files:** `apps/ui-hearth/src/state.ts`, `apps/ui-hearth/src/App.vue`,
`apps/ui-hearth/src/lib/settings.ts` if needed, and a test.

**Interfaces:**
- The default density is `compact`. A one-time prefs-version bump (a `v` key in the stored prefs) resets
  density to `compact` once, then the user's choice persists. Implement it as a pure merge in `lib/settings.ts`.
- `HTheme :tokens` overrides the `--h-radius-*` tokens to about 60 % of the installed 0.7.1 values.
  Read them from `node_modules/@hearth-ui/vue` after `bun install`, and put the exact numbers in the
  report.
- Check whether overlays inherit the island tokens. If they don't, set the tokens on `:root`.
- Unit tests cover fresh prefs, old prefs without `v` (reset to compact), and versioned prefs (choice kept).

### Task 11: docs, CHANGELOG, and the browser check

**Files:** `docs/usage.md` (custom routes section, and the `pstack route` command in the command list and its own section), `docs/webhook-events.md` if Task 4
missed anything, `packages/pstack/CHANGELOG.md` (an Unreleased section with Added: custom routes,
Grafana links; Changed: compact Hearth default), and the design doc's "where the build differs" banner.

**Interfaces:**
- A real-browser check of both UIs with `.claude/skills/pstack-ui-verify/SKILL.md`. Its scripts live at the repo-relative `.claude/skills/pstack-ui-verify/scripts/`, not `/Volumes/S1/...`:
  - the Routing page;
  - the deployment page with an assigned route;
  - the move confirm;
  - the Grafana links;
  - Hearth compact at 1280 and 320, in light and dark.
- Durable screenshot paths go in the report.
- Final `bun run check` from the repo root.

### Task 12: later collisions are settled at render, never ambiguous

**Files:** `packages/pstack/internal/api/routes_customroutes.go` (render path and list), the render
input in `internal/routing/customroutes.go` if it needs a blocked list, `internal/cli/route.go`,
`packages/pstack/api/openapi.yaml` (+ `go generate ./packages/pstack/internal/apicli`), the Go tests,
`packages/conformance/test/api-custom-routes.test.ts`, the Hearth and advanced custom-routes UI
(`lib/customRoutes.ts`, `composables/customRoutes.ts`, the views), `docs/usage.md`, and the design
doc banner.

**Interfaces:**
- Implement spec §"Later collisions" exactly. Every render computes the set of hosts and router
  names each deployment serves, using the same registry computation the assign check uses (plus live
  Traefik routers when reachable). It drops any alias router that collides, with deployment over
  route, then the earlier route over the later one (`updated_at`, then name), and logs each drop.
- `GET /api/custom-routes` rows gain `blocked: [{host, by: {kind, name}}]`, `[]` when none.
  `hosts[]` lists only served hosts. The openapi schema is updated.
- `pstack route ls` and the single-route outputs print `blocked: <host> (<kind> <name>)` lines.
  `--json` carries `blocked`.
- UIs: a blocked host is muted text with "taken by <name>", never a link.
- Tests:
  - Go: a deploy that adds a colliding service; a stack later named like a route; two routes
    colliding after a redeploy (earlier wins); unassigning the winner frees the host. Each with a
    negative control.
  - Conformance: one black-box case where a second deployment claims an alias host and the route
    reports it blocked, and the file has one router for that rule.

### Task 13: Grafana stack link from declared services

**Files:** the server source of a deployment's declared service names (prefer an existing endpoint;
otherwise add `declaredServices: string[]` to the deployment runtime response in
`internal/api/routes_deploy.go`, plus openapi, apicli regen and conformance), the Hearth
`DeploymentView.vue`, the advanced `DeploymentDetailView.vue`, and their tests.

**Interfaces:**
- Declared services are the names the log driver puts in `service_name=<stack>-<service>`, so they
  must use the same naming the deploy uses (`pstack.routing.service_name` does not apply to logs:
  check `internal/loki` / the logging driver options for the exact name). They are computed from the
  deployment's compose without docker calls.
- The stack link uses the declared services plus any running services the compose doesn't declare,
  deduplicated. A stack that is down or asleep keeps its header link. A deployment whose compose
  can't be read falls back to the running services.
- The gating is unchanged.
- Tests: the union and dedupe in each UI's helper (lib/composable). A server test covers the field
  for a stopped deployment. A conformance case shows a down deployment's runtime carrying its
  declared services.
