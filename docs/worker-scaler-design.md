# Worker scaler — add and remove swarm workers from the signals

**Status:** design, approved in conversation 2026-11-08. Not built. Two adversarial reviews (50 and 29
findings, each checked by a skeptic) are folded in; their record is outside the repo in
`~/.claude/projects/-home-samimishal-projects-go-preview-stacks/scaler-artifacts/`.

## Why

[Node signals](node-signals-design.md) tell a machine manager two things: a task is stuck for want of
room, and a worker is running nothing. Today a person (or their own script) has to act on that. The
scaler is an optional control-stack service that acts on it: it creates a worker when previews do
not fit and removes one that has sat empty, through OpenTofu and a preset the operator picks.

pstack itself still creates no machines. The scaler is a separate container; with it off, nothing in
this document exists on the host.

## The shape

| Piece | What |
|---|---|
| `scaler` service | Optional, in the control compose. `image: ${PSTACK_IMAGE}` (the same image as `pstack`), command `pstack scaler`, `user: "0:0"` (the image's user is uid 1000; the service has no docker socket, so root inside gains nothing). No ports, no Traefik labels. `restart: unless-stopped`, `stop_grace_period: 5m`. One volume: `${DATA_DIR}/scaler:/state`, created 0700 by init. |
| Network | A private `scaler` network that only `scaler` and `pstack` join, like `logs` for Loki. The scaler sends the root token to `http://pstack:7878`; on the previews' network any preview could answer to the name `pstack`. Turning the scaler on adds `scaler` to pstack's `networks:` line and declares it at the top level, by checked anchor edits as `LokiWiring` does, so toggling it recreates pstack — it is an `init` run anyway. |
| OpenTofu | Downloaded by `pstack scaler` on first start into `/state/bin`. Version pinned in pstack's source with one sha256 per architecture (amd64, arm64, from the release's `SHA256SUMS`); a mismatch or another architecture refuses to run. Not in the image: hosts without the scaler carry nothing. |
| API identity | The host's root token, `PSTACK_TOKEN` interpolated from `.env` as for `pstack`. |
| Settings | Stored by pstack (migration 12), edited on the Control page or `PUT /api/scaler`. The scaler pulls them every tick, so a save needs no restart and no job. |
| Presets | A folder of `.tf` meeting one contract (below). Five are built in; operators add their own. |
| State | `/state/tofu/<preset>/` (Tofu state, one directory per preset, plus `workers.auto.tfvars.json` at 0600: the `workers` map reaches Tofu here, never on the command line) and `/state/scaler.json` (the scaler's record: host id, owned workers, the locked config, last scale-up, backoff, never-joined count, does-not-fit services). All hold or sit beside the swarm join token: secret, never exported, never logged. |

Turning it on: `pstack init --scaler on` (env `PSTACK_SCALER`, default `off`) and
`cloud-init --scaler on`, which renders the flag onto its `init` call like `--logging`. Swarm only:
both refuse on a compose host (cloud-init at render time, as it already does for `--previews-on`).
Settings and credentials are entered afterwards, on the Control page or with `PUT /api/scaler` —
never in instance user-data. Read back by `pstack upgrade` from the rendered compose
(`^\s{2}scaler:`), guarded by an initguard row like `--logging`, rendered by a builder that returns
`""` when off and rides the existing `#__ADVANCED_UI_SERVICE__` marker (invariant 20: no new marker).
Off renders byte-identical to today.

## The loop

Every 30 s, at most one action per tick, never two Tofu commands at once.

**1. Read** `GET /api/scaler/config` (settings, credential, preset files), `GET /api/signals` and
`GET /api/swarm` (for `managerAddr`). Any request failing, or signals answering `swarm: false` or
`reachable: false`: no action this tick, and no clock advances. Report why.

**2. Finish removals.** An owned worker is *removing* once the scaler decided to remove it (steps 3, 5,
6); the mark is written to `scaler.json` before anything else happens, so a crash anywhere resumes
here. For each removing worker, in order, skipping what is already done: drain its node if it has one
→ `apply` without it → wait for its node to be `down` → `DELETE /api/swarm/nodes/:id`. It is
forgotten when it is absent from Tofu state **and** it has no swarm node (never joined, or the DELETE
succeeded).

**3. Notice lost workers.** Before any apply, an owned worker whose server is missing from Tofu's
refreshed state (deleted in the provider's console), or whose node has not been `ready` for 10 min,
becomes removing. So a deleted server is never recreated under its old name.

**4. Scale up** when all of these hold:
- owned workers (not removing) < `max`, and the last scale-up is older than `cooldown`;
- no owned worker is still booting (created, not yet `ready`);
- fewer than 2 consecutive never-joined failures since the settings last changed;
- either owned workers < `min`, or some `stuck` entry qualifies: its reason is `no suitable node (…)`
  with at least one clause, every clause being `insufficient resources on N nodes` or a drained node
  (`not available for new tasks`), its service is not marked does-not-fit, and no owned worker
  **with the current per-worker settings** is `ready` and empty.

Then:
- Name `pstack-<6 hex>`, never reused (swarm keys task counts by hostname; a reused name can make a
  busy node look empty).
- `GET /api/swarm/join?format=cloud-config&distro=<preset's distro>`, with `hostname: <name>` and
  `preserve_hostname: false` added as top-level keys → the entry's `user_data`. The scaler sets the
  hostname itself because providers disagree: Hetzner, GCP, Azure and DigitalOcean name the machine
  after the server, AWS does not (`ip-10-…`), and swarm matches nodes by hostname.
- Write `{ createdAt, stuckServices, user_data, …current per-worker vars }` to `scaler.json`,
  **report the owned name at once** (so the API's lock holds before the apply), write the tfvars,
  `apply`.
- Not `ready` within 10 min: *never-joined*. It becomes removing; `scaler.failed`; the count goes up.
- Ready, but the services it was created for are still stuck while it stays empty for a full tick:
  those services are marked **does-not-fit** until the settings change (`scaler.failed`). A
  same-size machine would not fit them either, so no more are bought for them.

**5. Scale down** an owned worker that is `ready`, `active` and empty since longer than `grace`, when
owned workers > `min` — or regardless of `min` when its frozen per-worker settings differ from the
current ones (so a changed server type replaces the fleet as workers go empty; `min` fill then
creates new ones). Re-read signals **immediately before** marking it removing, and skip it if it has
tasks or its `emptySince` moved. A task placed between that re-read and the drain is rescheduled by
swarm — a known, small race, not a guard.

**6. `max: 0`** removes every owned worker: one per tick becomes removing without waiting for
`grace`. Draining evicts their tasks; previews with no room elsewhere go pending.

**7. Report** `POST /api/scaler/report` with the status (below) and any event.

Defaults: `min` 0, `max` 3, `cooldown` 5 min, `grace` 15 min.

**Ownership is the whole safety model.** The scaler acts only on names in its own `scaler.json` and
only on nodes whose role is `worker`. It never drains, removes or destroys anything else, so a
manager, or a worker someone added by hand, is never touched. Turning the scaler off destroys nothing;
the card keeps listing the workers it owns.

**After an unclean stop** (host reboot, SIGKILL past the grace period) Tofu may have created a server it
did not record. On start the scaler compares the owned workers that are **not** removing with
`tofu state list`. A name with no state address is reported as a possible orphan
(`scaler.failed {action: orphan, name}`, on the card), made removing (so it is never created twice),
and kept on the card until an operator dismisses it. Every server carries the scaler's label or tag
(below) so it can be found in the provider's console.

## Settings are frozen per worker

Each `workers` entry carries the per-worker variables it was created with (`each.value.server_type`,
not `var.server_type`). Editing one changes only workers created afterwards — on most providers a
changed server type or location forces Tofu to *replace* the server, which would kill the previews on
it. The fleet turns over as old workers go empty (step 5). The image is frozen too: presets ignore
changes to the image attribute, so a newer Ubuntu image never rebuilds a running worker.

A preset's *shared* variables (`region`, `project`, `subnet_id`, `network_id`, `ssh_keys` …: the
provider block or resources every worker uses) cannot be frozen per worker, so they are locked like
the preset itself. While the scaler owns any worker, the **locked config** — preset name, a hash of
its files, the shared variables — is fixed:

- **The scaler is the authority.** It records the locked config with its first owned worker and
  refuses to apply a pulled config that differs (reported, nothing applied).
- **The API answers early.** Each report persists the owned names and the locked config in
  `scaler_config`. `PUT /api/scaler` and the preset routes answer 409 when a change would differ from
  the locked config — a PUT that restores it is accepted — so the rule holds across a pstack restart.

To switch: `max: 0`, wait for zero, then switch. The scaler then runs `tofu destroy` in the old
preset's directory to remove its shared resources (a firewall, a security group). The API keeps the
old preset's credential keys until the scaler reports that destroy done. A failed destroy is reported,
the directory is kept and listed on the card, and the new preset proceeds.

## Presets

**Contract.** A preset is `main.tf` (more `.tf` files allowed) plus an optional `.terraform.lock.hcl`:

- `variable "workers"` — `map(object({ user_data = string, … }))`; its other attributes are the
  preset's per-worker variables. Any other variable is shared, written to the same tfvars file.
- Variables the scaler fills when a preset declares them: `manager_address` (the host part of
  `managerAddr`, the manager's swarm advertise address) and `host_id` (8 hex characters generated once
  into `scaler.json`). Shared resources are named and selected with `host_id`, so two pstack hosts
  using one cloud project never collide or manage each other's workers.
- One server per entry (`for_each = var.workers`), named after the map key, carrying the scaler label
  — `pstack-scaler=<host_id>` where labels are key/value, the tags `pstack-scaler-<host_id>` and
  `pstack-scaler-<host_id>:<name>` on DigitalOcean (whose tags allow no `=`) — and
  `lifecycle { ignore_changes = [...] }` naming **the attribute that carries the user-data**
  (`user_data`, `custom_data` on Azure, `metadata["user-data"]` on GCP) **and the image attribute**.
  The cloud-config embeds the pstack version and the join token; without this, every upgrade, token
  rotation or new OS image rebuilds every worker.
- A firewall a preset creates allows all outbound traffic explicitly: workers must reach the manager's
  2377, the Docker repository and image registries before they can join.
- Credentials come from the provider's own environment variables, supplied as the scaler's credential
  map.
- No `backend` or `cloud` block: state is local. The scaler refuses a preset that has one.

The scaler runs `tofu init` and `tofu validate` before a preset's first use and checks that a
`workers` variable exists; a failure is reported and nothing is applied.

**Network.** Swarm workers must reach the manager at its advertise address, and the manager must reach
each worker at the address the worker advertises. A worker therefore goes on the manager's provider
and, where the provider puts only a private address on the interface (AWS, GCP, Azure), in the
manager's private network. Workers on another provider than the manager are out of scope: they would
advertise an unreachable private address, and their overlay traffic would cross the internet as
cleartext VXLAN.

On Hetzner and DigitalOcean servers carry their public address on the interface, so a manager that
advertises its public address works anywhere on that provider — with the overlay unencrypted on the
provider's network, as for any worker joined by hand today. The optional shared `network_id` (a Hetzner
Cloud Network) or `vpc_uuid` (a DigitalOcean VPC) puts workers in the manager's private network
instead; a manager advertising a private (RFC 1918) address requires it, and the scaler refuses with
that reason when it is missing.

The manager's own firewall must let workers in: 2377/tcp, 7946/tcp+udp and 4789/udp. Where workers
have a range (a VPC, a subnet, a private network), allow that range. On Hetzner and DigitalOcean with
public addresses there is no range, so the preset's firewall includes 2377/tcp and the operator puts
the manager under it too: the label `pstack-scaler=<host_id>` (Hetzner) or the tag
`pstack-scaler-<host_id>` (DigitalOcean); both providers add the rules of every firewall that applies.
usage.md says all of this next to the preset table, and the card shows `host_id`.

**Built in**, each with its provider pinned by a lock file for linux amd64 and arm64, embedded in the
binary (`//go:embed all:…`, so the dot-file lock files are included, with a test that each is) and
shipped as a plain folder so it also runs by hand (option C below). Read-only through the API. All
boot amd64 images; an Arm machine type needs a custom preset.

| Preset | Server | Per-worker | Shared | Credential | Distro | Firewall it creates |
|---|---|---|---|---|---|---|
| `hetzner` | `hcloud_server`, image `debian-12` (fixed, matching the distro) | `server_type`, `location` | `ssh_keys`, `network_id` (optional) | `HCLOUD_TOKEN` | `debian` | One `hcloud_firewall` applied by the label selector: 2377/tcp, 7946 tcp/udp and 4789 udp from `manager_address` and the workers' addresses, 22 from anywhere, all outbound. |
| `aws` | `aws_instance`, Canonical Ubuntu LTS AMI, `associate_public_ip_address = true` (outbound for Docker and images) | `instance_type` | `region`, `subnet_id` (the manager's VPC), `key_name` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | `ubuntu` | One security group in the subnet's VPC: 7946 tcp/udp and 4789 udp from every CIDR block of the VPC (`cidr_block_associations`) and from itself (`self = true`); egress all (Terraform removes AWS's default egress rule). |
| `gcp` | `google_compute_instance`, Ubuntu LTS image (GCE's Debian images do not run cloud-init), user-data in metadata `user-data`, `access_config {}` | `machine_type`, `zone` | `project`, `subnetwork` (the manager's) | `GOOGLE_CREDENTIALS` (service-account JSON) | `ubuntu` | One firewall rule on the subnetwork's network: the same ports from the subnetwork's range to the workers' network tag. GCP allows egress by default. |
| `azure` | `azurerm_linux_virtual_machine`, Ubuntu LTS image, `custom_data = base64encode(each.value.user_data)`, `computer_name` = the key; per worker a NIC and a public IP | `vm_size` | `resource_group`, `location`, `subnet_id` (the manager's VNet), `admin_ssh_public_key` (Azure requires one) | `ARM_CLIENT_ID`, `ARM_CLIENT_SECRET`, `ARM_TENANT_ID`, `ARM_SUBSCRIPTION_ID` (a service principal) | `ubuntu` | None: Azure's default `AllowVnetInBound` rule admits the VNet, and outbound is open by default. |
| `digitalocean` | `digitalocean_droplet`, Ubuntu LTS image | `size`, `region` | `ssh_keys`, `vpc_uuid` (optional) | `DIGITALOCEAN_TOKEN` | `ubuntu` | One `digitalocean_firewall` by the shared tag (declared as a `digitalocean_tag` so it exists first): 2377/tcp, 7946 tcp/udp and 4789 udp from `manager_address` and from that tag, 22 from anywhere, outbound tcp/udp/icmp to anywhere (a DigitalOcean firewall with no outbound rules blocks all outbound). |

**Custom:** `PUT /api/scaler/presets/:name` with the files; listed, read and deleted the same way.
Names match the spec-name rule (`specs.go`) and may not shadow a built-in; files are basenames matching
`^[A-Za-z0-9_-]+\.tf$` or exactly `.terraform.lock.hcl`. Without a lock file, provider plugins download
unpinned — the card says so. **A custom preset is code**: Tofu runs it with the root token and the
cloud credentials in reach, and `validate` is not a sandbox. Uploading one is admin-only, the same
trust boundary as spec hooks.

## What pstack adds

**Storage** (migration 12): `scaler_config` (one row: preset, vars, min, max, cooldown, grace,
credential map, and from the last report the owned names and the locked config) and `scaler_presets`
(name, files, updatedAt). The credential map is a stored secret (invariant 15). `pull config` does not
carry any of it: `scaler: host-specific — re-enter it on the target` in `Skipped`.

**Routes**

| Route | Role | What |
|---|---|---|
| `GET /api/scaler` | maintainer | Settings, credential keys as `{"HCLOUD_TOKEN": true}` (set or not, never the value), the last report, the owned names, `host_id`, and `enabled` (`true`/`false`/`null` = the container is present / absent / docker did not answer). |
| `PUT /api/scaler` | admin | Settings and credential map. A credential key sent empty, omitted or as the mask keeps its stored value; `null` deletes it (refused for a key the old preset still needs during a switch). Validates the preset exists and `0 ≤ min ≤ max`. 409 when a change would differ from the locked config. |
| `GET /api/scaler/presets` · `GET`/`PUT`/`DELETE /api/scaler/presets/:name` | maintainer · admin | Built-ins read-only. 409 on editing or deleting the locked preset. |
| `GET /api/scaler/config` | root only (`PSTACK_TOKEN`, never an admin session) | Everything the scaler needs, credential values and preset files included. |
| `POST /api/scaler/report` | root only | The scaler's status: owned workers (name, per-worker vars, state, createdAt, removing — never `user_data`), the locked config, `host_id`, last action, last error, next retry, does-not-fit services, possible orphans, a failed old-preset destroy. Owned names and the locked config are persisted; the rest is kept in memory. Optionally one event to emit. |

`GET /api/scaler/config` is the second deliberate exception to invariant 15 after `GET /api/config`,
on the same terms; AGENTS.md's sentence naming "the ONE deliberate exception" is amended to name both.

**Events** (appended to `events.Names`): `scaler.created` `{name, settings}` (`settings` = the
per-worker vars, never `user_data`), `scaler.removed` `{name, reason: empty|lost|never-joined|max|
replaced}`, `scaler.failed` `{action, error, attempt}` (`action` includes `does-not-fit`, `orphan`,
`max`, `destroy`). Never the credential, never the join token. Tofu's output is scrubbed **whole, then
tailed**: every credential value, every string leaf of 8+ characters in a JSON credential
(`GOOGLE_CREDENTIALS`'s `private_key`, escaped and decoded), and `SWMTKN-…` (`redact.RedactText` with
extras).

**Control page**, both UIs: one "Workers" card, shown while the scaler is enabled or still owns
workers. Preset, its variables, min/max/cooldown/grace, credential keys (admin-only inputs,
write-only), `host_id`, the owned workers with their settings and state, the last action and error,
does-not-fit services, possible orphans. A preset editor for custom presets. Copy is terse.

## Failures

| Situation | Behaviour |
|---|---|
| `tofu apply` fails | `scaler.failed` with the last lines of Tofu's output, scrubbed. Retried on later ticks with backoff 1, 2, 4 … 30 min (kept in `scaler.json`, so a restart does not reset it). Desired state is the `workers` map, so a retry repeats the same intent. |
| A new worker not `ready` in 10 min | Removed; `scaler.failed`; counts toward the backoff. After 2 in a row, no more scale-ups until the settings change; the card says why. |
| A stuck task no new machine can fix | Only qualifying reasons count (step 4). A new worker that leaves its services stuck marks them does-not-fit until the settings change: no purchase loop. |
| Server deleted in the console, node lost | Removing at once if missing from refreshed state, after 10 min if not `ready`; never recreated under the old name. |
| Signals unreadable, swarm down, API unreachable, credential unset, preset invalid, private manager without `network_id`/`vpc_uuid` | No action and no clock advances; the reason is in the report and on the card. |
| `max` reached with tasks still stuck | No action; one `scaler.failed` with `action: max` per stuck episode. |
| SIGTERM mid-apply (restart, upgrade) | The scaler lets the running Tofu command finish within the 5 min grace. A local-state lock dies with the process. A kill past the grace is the orphan check above. |

## Option C — bring your own scaler

Nothing here is needed to scale workers another way. Signals, `signal.raised`/`signal.cleared`, the
drain/undrain/remove routes and `/api/swarm/join` behave exactly as before, scaler on or off. The
built-in presets are plain folders: `tofu apply -var-file=workers.json` works without pstack.

Two rules for a home-grown scaler, in the docs:

- Run it outside the `pstack-control` compose project. `init`, `upgrade`, `pstack ui` and
  `pstack logging` run `up --remove-orphans`, which deletes containers in that project that pstack did
  not render.
- Don't run both on one host unless they own disjoint workers. The built-in scaler only touches the
  names it created, but two policies reacting to the same stuck task will both add a machine.

## Amendment to node-signals-design.md

"No provisioning. No cloud credentials, ever." and "No settings, no new `init` flags." stay true of
the signals feature. Add: the optional scaler is a separate service that provisions; its credential is
stored by pstack like Loki's S3 secret and handed only to the root token.

## Out of scope

- Workers on another provider than the manager (see *Network*).
- Reacting to webhooks instead of polling. The poll needs no listener, and `GET /api/signals` is the
  picture the guards need anyway. A new machine takes 1–2 min to boot, so 30 s of polling latency does
  not matter.
- More than one active preset, Arm images in the built-ins, per-preview machine sizes, spot instances,
  scaling managers.
- Remote state backends. State is local to the manager; losing the manager loses the record of which
  servers the scaler made (they still carry the scaler's label or tag in the provider's console).
- Moving data off a worker. Preview data is disposable (node-signals-design.md, *Caveats*).

**Worth knowing:** past `min`, swarm reports `insufficient resources` only against reservations, so
the scaler adds workers for load only when previews declare `deploy.resources.reservations` (or
`mem_reservation`). A cluster with no active node at all (the manager drained by
`--previews-on workers`, `min: 0`) still gets its first worker: a drained-node reason qualifies.
usage.md says both.

## Testing

- Go: the loop against a fake `tofu` on `PATH` and an `httptest` API — scale-up only on qualifying
  reasons (an all-drained cluster included), `min` fill, cooldown, no scale-up while one is booting,
  does-not-fit marking and its reset on a settings change, a stale-settings worker replaced at `min`,
  re-read before removal, grace, never-joined cleanup and the stop after two, a removing worker with
  and without a node surviving a restart at every step, a server deleted in the console, the orphan
  check skipping removing workers, backoff surviving a restart, `swarm: false` freezing every clock,
  `max: 0`, the ownership rule (a hand-added worker and a manager are never touched), the scaler
  refusing a changed locked config, the owned name reported before the apply, tfvars written 0600 and
  nothing on argv, a private manager address without `network_id` refused, and the scrub (no
  credential value, no JSON credential leaf, no `SWMTKN` in reports, events or logs; no `user_data`
  in reports or events).
- API: the routes, roles, write-only credential keys, root-only `config` and `report`, the persisted
  locked config keeping the 409 across a restart and accepting a PUT that restores it, credential keys
  kept through a switch, custom preset name and file rules, events.
- Render goldens: the two new cells (scaler on, logging off and on), with `image: ${PSTACK_IMAGE}`
  and the `scaler` network on pstack's line and at the top level; off cells unchanged. `init` and
  `cloud-init` refuse `--scaler on` on compose.
- The five built-in presets: `tofu init -backend=false && tofu validate` in CI; a test that each lock
  file is embedded; a test that the hostname keys leave the join cloud-config valid YAML.
- **Out of band, before release:** a real run per provider, each with the manager on the same
  provider (and network where required) — stuck preview → worker joins → preview placed and
  reachable; empty past grace → drained, destroyed, node removed; a hand-added worker untouched; the
  firewall present with outbound open; a second host with the same preset in the same project does not
  touch the first host's workers.

## Docs

usage.md (a *Worker scaler* section: presets, the network rules, the manager's firewall and the label
or tag that puts it under the preset's firewall, reservations, the option-C rules), control-plane.md
(the service and its network), AGENTS.md (migration and table counts, event count, invariant 15's list
and its exception sentence), webhook-events.md, CHANGELOG, and the amendment above in
node-signals-design.md.
