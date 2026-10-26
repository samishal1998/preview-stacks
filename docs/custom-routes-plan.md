# Custom routes, Grafana links and the compact Hearth theme: implementation plan

**Spec:** docs/custom-routes-design.md (binding).
**Branch:** claude/custom-routes (GitButler).

## Global Constraints

- The spec binds. Where this plan and the spec disagree, follow the spec and say so in the report.
- Read AGENTS.md before touching an area. Follow its "how to add a route / event" checklists.
- VCS writes go through `but` only. Commit with explicit file ids:
  `but commit -b claude/custom-routes -m "<type(scope): summary>" <ids>`. One or more commits per task.
- Never commit `.dns_token`, `hetzner_42.yaml`, `packages/conformance/golden/host/db/pstack.db-{shm,wal}`,
  anything under `.superpowers/`, credentials or sealed files. Commits carry no attribution footers.
- Never push, open PRs, publish, or touch the live host.
- Go: `gofmt` clean, `go vet ./packages/pstack/...` clean, `go test ./packages/pstack/...` green.
- After any change to `packages/pstack/api/openapi.yaml`, run `go generate ./packages/pstack/internal/apicli`.
- Goldens:
  - Rebuild the binary first: `go build -o packages/pstack/bin/pstack ./packages/pstack/cmd/pstack`.
  - Then `cd packages/conformance && bun gen/goldens.ts`. Never `bun run gen`.
  - The host db golden is regenerated only by the task that adds the migration.
  - Before regenerating, `packages/conformance/gen/host-fixture.ts` and `golden/host/db/pstack.db`
    already carry uncommitted owner changes. Leave those changes uncommitted. If a task must regenerate
    them, commit only the hunks that task caused, and say so in the report.
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
(+ `_test.go`), the regenerated `packages/conformance/golden/host/db/pstack.db` and
`gen/host-fixture.ts` if the migration changes them.

**Interfaces:**
- Migration 10 creates `custom_routes(name TEXT PRIMARY KEY, deployment_id TEXT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`.
- `type CustomRoute struct { Name string; DeploymentID string /* "" = unassigned */; CreatedAt, UpdatedAt string }`.
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

### Task 6: conformance black-box tests

**Files:** a new `packages/conformance/test/api-custom-routes.test.ts`, the harness only if needed, and
goldens regenerated for the openapi/CLI help changes.

**Interfaces:**
- Against the real binary with the fake docker:
  - create, list, assign, 409 when held, `force` move (one call, and the file shows only the new
    target), unassign, delete;
  - a bad name, a reserved name, and a name equal to a stack;
  - an unknown deployment (404);
  - the role gates (viewer refused on writes);
  - a share principal refused;
  - `pstack-routes.yml` contents after each step;
  - reserved: PUT `/api/routing/pstack-routes.yml` refused;
  - forget unassigns.
- One CLI case: `pstack api custom-routes assign … --force`.
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
- The default density is `compact` when stored prefs don't name one. A stored `comfortable` is kept.
- `HTheme :tokens` overrides the `--h-radius-*` tokens to about 60 % of the installed 0.7.1 values.
  Read them from `node_modules/@hearth-ui/vue` after `bun install`, and put the exact numbers in the
  report.
- Check whether overlays inherit the island tokens. If they don't, set the tokens on `:root`.
- A unit test covers the default and the kept stored value.

### Task 11: docs, CHANGELOG, and the browser check

**Files:** `docs/usage.md` (custom routes section and CLI lines), `docs/webhook-events.md` if Task 4
missed anything, `packages/pstack/CHANGELOG.md` (an Unreleased section with Added: custom routes,
Grafana links; Changed: compact Hearth default), and the design doc's "where the build differs" banner.

**Interfaces:**
- A real-browser check of both UIs with `.claude/skills/pstack-ui-verify/SKILL.md`:
  - the Routing page;
  - the deployment page with an assigned route;
  - the move confirm;
  - the Grafana links;
  - Hearth compact at 1280 and 320, in light and dark.
- Durable screenshot paths go in the report.
- Final `bun run check` from the repo root.
