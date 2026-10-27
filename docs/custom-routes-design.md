# Custom routes, Grafana links and the compact Hearth theme

> **Built, unreleased.** Using it: [usage.md](usage.md#custom-routes-a-stable-name-for-a-moving-stack),
> `pstack route`. The build plan is [custom-routes-plan.md](custom-routes-plan.md). The spec below is
> kept as approved on 2026-10-26. Where the build differs from it:
>
> - **Status codes.** A reserved name (`control`, `api`, `loki`, `grafana`) is a 400, a bad name.
>   A stack's name or an existing route is a 409. An unknown route or deployment is a 404. POST
>   accepts `force` and ignores it: a new route is never held. DELETE answers `{ deleted }`.
> - **Router names can collide.** `a-b` with service `c` and `a` with service `b-c` both give
>   `pstack-route-a-b-c`. Create and assign refuse that with a 409 naming the router. A later one is
>   settled at render like a host: the earlier claim keeps the name. The renderer's `# dropped` note
>   stays as a safety net that never fires.
> - **Collisions are never ambiguous (owner, 2026-10-27).** Create and assign refuse a collision up
>   front (409). Anything that arises later, such as a deploy that claims a route host or a stack
>   named like a route, is settled at render: the deployment's own host wins and the route's claim
>   on that host is rejected. See §"Later collisions". The render settles it, so a GET reads what the
>   last render decided: rows carry `blocked: [{ host, by: { kind, name } }]` (`[]` when none), a
>   blocked wildcard spelled `*.<host>`, and kind `deployment` names the stack, or the compose
>   project or router of a live router no deployment explains.
> - **`hosts[]` lists wildcards** as `*.<host>` after the plain hosts. The UIs show them as text,
>   not links.
> - **No routes, no file.** With nothing to render, `pstack-routes.yml` is removed, not written
>   empty. Boot with an empty table touches nothing. Boot also skips when rows exist but none is
>   assigned, so a stale file left by a crash mid-unassign waits for the next write.
> - **Writes that change nothing do not re-render:** an unassigned create, a no-op assign, and an
>   unassign of an unassigned route. A registration (`PUT /api/deployments/:id`), a deploy and a
>   removal re-render whenever any route is assigned, whoever holds it, since each can settle a
>   collision. With nothing assigned they touch no file and ask docker nothing.
> - **`DELETE /api/deployments/:id` unassigns too,** not only a forget, both with reason `forgotten`.
> - **Waking, not only sleeping.** Alias hosts join a stack's waking entry as well as its sleep
>   entry, so a visitor between `up` and readiness gets the waking page. They come from a map the
>   render fills, so the request path parses no compose. `previewHostname` is unchanged: an alias
>   host is already one label under a known domain.
> - **Explicit `subdomains` hosts** get no twin, like an explicit `pstack.routing.host`. A service
>   with `pstack.routing.service_name` is mirrored under that name.
> - **CLI.** The generated commands are `pstack api custom-routes list|create|delete|assign|unassign`
>   with flags (`--name`, `--deployment`, `--force`). Completion offers the `pstack route`
>   subcommands only, with no route names or deployment ids, because completion makes no
>   authenticated calls.
> - **Advanced UI.** Its pure helpers live in `apps/ui/src/composables/` (`customRoutes.ts`,
>   `grafana.ts`), its convention, not `lib/`. One form creates or assigns, and its button reads
>   Create or Assign, in place of a separate New route control. The move question keeps the full
>   "Move checkout from myapp-pr-122 to myapp-pr-123?": at 320 px it wraps inside the panel.
> - **Grafana stack link (owner, 2026-10-27).** It is built from the services the compose declares
>   (`declaredServices` on `GET …/runtime`: the ones the log driver gets, in the selected profiles)
>   plus any running, so a stack that is down or asleep still has its link. See §2. The ceiling: a
>   service that ran under an older compose and was dropped by a later redeploy falls out of the
>   link; its streams stay in Loki. A share link gets no `declaredServices`.
> - **Corners** are 6/10/14 px (control, card, panel), the example here, not a strict 60 % (6/10/12).
>   Pill stays 999 px. Overlays use native top-layer dialogs and popovers inside the `HTheme` island,
>   so they inherit the tokens. Nothing is set on `:root`.

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
  `name TEXT PRIMARY KEY, deployment_id TEXT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL`
  (epoch milliseconds, like every sibling table and API timestamp).
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

### Later collisions **(owner, 2026-10-27: never ambiguous)**

Create and assign check collisions, but a later change can still make one, for example a deploy that
adds a service, or a new stack named like a route. Traefik must never get two routers for one rule,
so every render settles them deterministically:

1. **A deployment's own hosts always win.** An alias host is not rendered if it equals a host any
   deployment serves: its generated hosts, explicit `pstack.routing.host` and `subdomains` hosts,
   and live Traefik routers when they are reachable. This includes a deployment that has since been
   named like the route.
2. **Between two routes, the earlier claim wins.** That is the route assigned first (`updated_at`,
   then name). The later route's colliding host is rejected.
3. A rejected host is **not served by the route**. The route keeps serving its other hosts. The same
   applies to a router-name collision.
4. The route reports it. The API row gains `blocked: [{ host, by: { kind: "deployment"|"route", name } }]`.
   `hosts[]` lists only what is served. `pstack route ls` prints `blocked: <host> (<kind> <name>)`.
   Both UIs show a blocked host as muted text with "taken by <name>".
5. **Nothing silently disappears.** The render logs every rejected host. When a render newly blocks
   a host, `route.assigned` is not re-emitted. No new event name (add-only contract); a later
   release can add one.
6. Unassigning, deleting or redeploying the winner frees the host: the next render serves it again.

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

### CLI **(owner: `pstack route` on every surface)**

A hand-written `pstack route` command, a thin client over `/api/custom-routes` that reaches the host
the way `pstack pull config` does (`apiBase`: `PSTACK_API_URL` plus `PSTACK_TOKEN`):

```text
pstack route ls                                  every route: name, stack, hosts
pstack route create <name> [<deployment>] [--force]
pstack route assign <name> <deployment> [--force]
pstack route unassign <name>
pstack route rm <name>
```

- Output is terse, one line per route: `checkout  myapp-pr-123  web-checkout.preview.example.com, …`,
  or `checkout  (unassigned)`. `--json` prints the API body.
- A held route exits non-zero with `pstack: checkout is assigned to myapp-pr-122 (--force moves it)`.
- Bad usage exits 3 like every other command.
- It is a full CLI command per AGENTS.md: `ParseArgs`, `Usage()`, a `commandHelps` entry,
  `completion.go` (subcommands, plus route names and deployment ids where completion can reach
  them), the help goldens, and `docs/usage.md`.
- The generated `pstack api custom-routes …` stays as well, since it comes free from the openapi.

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
  `{service_name=~"<stack>-(<svc1>|<svc2>…)"}`. It is built from the stack's **declared** services
  (owner, 2026-10-27): the service names the deployment's compose gives the log driver. A stack that
  is down or asleep still gets the link to its past logs. Running services that the compose doesn't
  declare are added as well. Regex metacharacters are escaped, so stack `a` never matches `a-b`.
  Range `now-1h`.
- **Service link:** `{service_name="<stack>-<svc>"}`, same range.
- Shown on the deployment page header (stack) and beside each service's logs (service), in Hearth and
  the advanced UI.
- Gated exactly like the existing sidebar link: developer and up, no token-only session, and Grafana
  present. Never shown on `PublicLogsView` (share links).
- One shared builder per UI, in `lib/`, with unit tests.

## 3. Compact Hearth theme **(owner)**

- Hearth's existing `compact` density becomes the default. Stored prefs predate any real choice (the
  whole object is saved on any change), so a one-time prefs-version bump resets density to `compact`
  once. After that, the user's choice persists.
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
