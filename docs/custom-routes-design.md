# Custom routes, Grafana links and the compact Hearth theme

Status: design, 2026-10-26. Owner decisions are marked **(owner)** and bind. Everything else is a
controller default the owner may overrule.

## Why

A developer working in a `gh-stack` stack opens one PR per layer. The head PR keeps changing, so the
preview hostname (`web-myapp-pr-123.<domain>`) keeps changing too, and every link they shared breaks.
A custom route is a stable name that follows whichever stack the developer points it at.

## 1. Custom routes

### What a route is

A custom route is **an alias for a stack (owner)**. Route `checkout` assigned to the deployment whose
stack is `myapp-pr-123` makes every host that stack serves answer on a twin host with the alias in
place of the stack name:

| Stack host (autolabel) | Alias host |
|---|---|
| `web-myapp-pr-123.preview.example.com` | `web-checkout.preview.example.com` |
| `api-myapp-pr-123.preview.example.com` | `api-checkout.preview.example.com` |
| wildcard `*.web-myapp-pr-123.preview.example.com` | `*.web-checkout.preview.example.com` |

- The host shape is `<service>-<alias>.<domain>`, the same shape autolabel builds from the stack name
  (`autolabel.go` `host = req.Name + "-" + st.Stack + "." + domain`). `<domain>` is that stack's own
  preview domain. **(owner: `<name>.<domain>` form, not arbitrary FQDNs.)**
- **Every** routed service is mirrored **(owner)**, including wildcard subdomain routers.
- A service with an explicit `pstack.routing.host`, or with its own `traefik.*` labels, is **not**
  mirrored: its host does not contain the stack name and is already stable.
- A route exists unassigned too. Unassigned, it serves nothing.

### Names

- A DNS label: `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`. Refused otherwise (400).
- Refused when it equals `control`, `api`, `loki` or `grafana` (invariant 22), or the stack name of
  any registered deployment (their hosts would collide). 409 for a taken name.
- On assign, every alias host is computed; if one equals a host another deployment or route already
  serves, the assign is refused (409, naming the host). Never two routers with the same rule.

### Assignment rules **(owner)**

- Assign succeeds when the route is **unassigned**, or already assigned to the same deployment (a
  no-op, 200).
- Assigned to another deployment: **409** `{ error, assignedTo: { deployment, stack } }`, unless
  `force: true`. With `force`, the route moves in **one** transaction: unassign from X, assign to Y,
  one re-render. Never two calls.
- Unassign is always allowed.
- The deployment id is matched against the registry (404 if unknown, invariant 13). A route may be
  assigned to a sleeping or not-yet-ready deployment: it stores the intent.
- When a deployment is forgotten (down + forget), its routes become unassigned. The routes are kept.

### Storage and routing

- Source of truth: SQLite table `custom_routes` (migration 10, append-only):
  `name TEXT PRIMARY KEY, deployment_id TEXT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL`.
  It is operator config (invariant 10): the mapping, never runtime state.
- Traefik reads a derived, reserved dynamic file `pstack-routes.yml`, written beside
  `pstack-domains.yml` by `routing.RoutingStore` (atomic write) and added to `routing.IsReserved`, so
  `/api/routing/<file>` can't hand-edit it.
- One router per alias host, named `pstack-route-<alias>-<service>` (and `…-wild`), with the same
  rule shape, priority, entrypoint and TLS block (`tlsFor`) as the stack's own router. It points at the
  stack's own service, provider-qualified: `<service>-<stack>@docker` or `@swarm`.
- **No redeploy.** Assign, move, unassign and delete rewrite the file (Traefik picks it up in about
  2 s) and call `s.reindex()`.
- The file is re-rendered from the table:
  - after every create, assign, move, unassign or delete;
  - at boot (`reconcileCustomRoutes`, like `reconcileDomains`), so the table and file heal;
  - after a successful deploy or redeploy of an assigned deployment, since its services may change;
  - after a forget that unassigned routes.
- The services and hosts of a stack come from the same autolabel computation the deploy uses, never
  from guesses. Render is total: a deployment whose compose can't be read renders no routers for its
  routes and logs why. It must not drop the rest of the file.
- Sleep and wake: `reindex()` adds the alias hosts of a sleeping deployment to its sleep entry, so a
  request to `web-checkout.<domain>` wakes that stack. Because the hosts are added at reindex time,
  not captured at sleep time, a move while asleep follows the assignment.

### API

All under `/api/custom-routes`, described in `packages/pstack/api/openapi.yaml`. The generated
`pstack api custom-routes …` CLI comes from there.

| Method and path | Body | Result | Role |
|---|---|---|---|
| `GET /api/custom-routes` | | `[{ name, deployment, stack, hosts[], createdAt, updatedAt }]` | Viewer |
| `POST /api/custom-routes` | `{ name, deployment?, force? }` | 201, the route | Developer |
| `DELETE /api/custom-routes/{name}` | | 200 | Developer |
| `PUT /api/custom-routes/{name}/assignment` | `{ deployment, force? }` | 200, the route; 409 when held | Developer |
| `DELETE /api/custom-routes/{name}/assignment` | | 200, the route | Developer |

- `deployment` and `stack` are `null` when unassigned. `hosts` lists the alias hosts it serves now.
- Share principals never reach these routes (invariant 16): they are not in `shareAllows`.
- `GET /api/deployments/{id}` responses gain nothing. The UIs filter the list by deployment.

### CLI

Generated from the openapi (`pstack api custom-routes list|create|delete|assign|unassign`). `force`
is a boolean body field, so it surfaces as `--force` like `downDeployment`'s. No hand-written
top-level command, to avoid four help goldens (add `pstack route` later if the generated one is too
long to type).

### Events

Append to `events.Names` (add-only, invariant 14): `route.assigned` (`route`, `deployment`, `stack`,
`previous` when moved) and `route.unassigned` (`route`, `deployment`, `stack`, `reason`:
`unassigned|deleted|forgotten`). A move emits one `route.assigned` with `previous`. Document them in
`docs/webhook-events.md`.

### UI **(owner: Hearth and the advanced UI; basic UI stays API/CLI only)**

- **Routing page:** a "Custom routes" section listing name, hosts, assigned stack and actions, plus
  New route.
  - Assigning a route that another stack holds asks first: "Move checkout from myapp-pr-122 to
    myapp-pr-123?". Confirming sends one `PUT … {force: true}`.
  - Hearth uses an `HAlertDialog`. The advanced UI uses its armed `ActionButton` confirm, never
    `window.confirm`.
- **Deployment page:** the routes assigned to it, with their hosts as links, and Assign route
  (pick or create one, with the same move confirm) and Unassign.
- Writes are hidden below Developer, as a courtesy: the server is the authority.
- Copy is terse (docs/ui-rules.md).

## 2. Grafana links

- The server already returns `grafana: "https://grafana.<domain>"` in `/api/health` only when Grafana
  runs. The links are built client-side from it; there is no API change.
- Loki streams carry only `service_name=<stack>-<service>`. A per-container link is therefore per
  service: swarm replicas share one stream. Adding a container label would break invariant 23.
- **Stack link:** Explore, datasource uid `loki`, query
  `{service_name=~"<stack>-(<svc1>|<svc2>…)"}`. It is built from the stack's service list, with
  regex metacharacters escaped, so stack `a` never matches `a-b`. Range `now-1h`.
- **Service link:** `{service_name="<stack>-<svc>"}`, same range.
- Shown on the deployment page header (stack) and beside each service's logs (service), in Hearth and
  the advanced UI.
- Gated exactly like the existing sidebar link: developer and up, no token-only session, and Grafana
  present. Never shown on `PublicLogsView` (share links).
- One shared builder per UI, in `lib/`, with unit tests.

## 3. Compact Hearth theme **(owner)**

- Hearth's existing `compact` density becomes the default for prefs that don't name one. A stored
  `comfortable` is kept.
- Every density gets slightly smaller corners, by overriding the `--h-radius-*` tokens through
  `HTheme`'s `tokens` prop in `App.vue`. The target is about 60 % of the library values (for example
  10/16/20 → 6/10/14 px), taken from the installed `@hearth-ui/vue` 0.7.1 tokens after `bun install`.
  Check that overlays (dialogs, popovers, the command palette) inherit them; if they portal out of the
  theme island, set the tokens on `:root` instead.
- The advanced UI's radius scale is untouched.

## Out of scope

- Arbitrary FQDN routes, path rules, and per-route TLS settings.
- A per-container Loki label, and provisioned Grafana dashboards.
- Custom routes in the basic embedded UI.
