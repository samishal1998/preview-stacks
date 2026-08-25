# Webhook events

Every event pstack sends to a registered notifier: when it fires, exactly what the payload
carries, and what a receiver must do to consume it safely. Register notifiers on the
**Notifiers** page or via `POST /api/notifiers`; the delivery rules (retries, timeouts,
concurrency, the log) live in [control-plane.md §4d](control-plane.md).

Everything here is a **compatibility contract**: event names and payload fields are added,
never renamed or removed — they live in stored registrations, and renaming one would silently
stop deliveries for everyone subscribed to it. Subscribing to `*` covers events added in later
versions; a specific list does not.

## The envelope (type `webhook`)

Every delivery is an HTTP `POST` with a JSON body of exactly four fields:

```json
{
  "id": "evt_m1abc_7_x9k2pq",
  "event": "job.leaked",
  "at": 1754000000000,
  "data": { }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Unique per **emission**, stable across retries and shared by every notifier receiving this event. **Dedupe on this** — delivery is at-least-once. |
| `event` | string | One of the names below. |
| `at` | number | Epoch **milliseconds** when the event was emitted. Inside the signed material, so a replayed delivery cannot claim a fresh time. |
| `data` | object | Event-specific fields, catalogued below. |

### Headers

| Header | Value |
|---|---|
| `content-type` | `application/json` |
| `user-agent` | `pstack` |
| `x-pstack-event` | same as `event` |
| `x-pstack-delivery` | same as `id` — the dedupe key |
| `x-pstack-timestamp` | same as `at` |
| `x-pstack-signature` | `sha256=` + HMAC (below) |
| `x-pstack-redelivery` | `1` on a replay of an event already sent; absent otherwise |

### Verifying the signature

The signing secret is shown **once**, at registration; the server keeps no way to show it again.

```
expected = "sha256=" + hex(HMAC_SHA256(secret, timestamp + "." + rawBody))
```

- Use the **raw request body** — re-serialising the JSON changes the bytes and the comparison fails
  against a perfectly good delivery.
- `timestamp` is the `x-pstack-timestamp` header value, as a string.
- Reject deliveries whose timestamp is older than a few minutes: the timestamp is signed, so this
  bounds replays.
- Compare constant-time if your platform makes that easy.

A worked receiver (Bun):

```ts
Bun.serve({
  port: 8080,
  async fetch(req) {
    const raw = await req.text();
    const ts = req.headers.get('x-pstack-timestamp') ?? '';
    const expected = 'sha256=' +
      new Bun.CryptoHasher('sha256', process.env.PSTACK_WEBHOOK_SECRET!)
        .update(`${ts}.${raw}`).digest('hex');
    if (req.headers.get('x-pstack-signature') !== expected) return new Response('bad signature', { status: 401 });
    if (Math.abs(Date.now() - Number(ts)) > 5 * 60_000) return new Response('stale', { status: 401 });
    const e = JSON.parse(raw);
    // dedupe on e.id before acting — retries reuse it
    return new Response('ok');
  },
});
```

### Delivery semantics

- **At-least-once.** A timeout after the receiver processed the request produces a retry with the
  same `id`.
- 3 attempts (immediately, then +1s, then +5s), 5s timeout each. Redirects are treated as failures,
  never followed.
- **No ordering guarantee** across events or notifiers. Use `at` (and the job fields) to order.
- **One delivery per notifier is in flight at a time**, and further events **queue** behind it (up to
  200 per notifier). A slow receiver therefore delays its own events and nobody else's.
  Past that cap the oldest is dropped and recorded as dropped — bounded, so an outage cannot turn the
  log into a disk leak, and visible, so it is never silent. (Before 0.25.0 there was no queue at all:
  anything arriving while a delivery was in progress was dropped immediately, which made a burst —
  the five readiness events one deploy emits — unsurvivable.)
- **Redelivery.** Every delivery stores the envelope it sent, so
  `POST /api/notifiers/:id/deliveries/:deliveryId/redeliver` (the **Redeliver** button in the
  delivery log) replays it. The replay keeps the **original event `id`** — a receiver that already
  processed it dedupes exactly as it should — with a **fresh timestamp and signature** (a correct
  receiver rejects stale ones) and an `x-pstack-redelivery: 1` header so a receiver that never
  recorded it can tell a replay from a new event. Rows written before 0.25.0 have no stored payload
  and are refused rather than reconstructed.
- Deleting a notifier stops pending retries.

## Chat types (`slack`, `discord`)

Chat notifiers do **not** receive the envelope. They receive one human-readable line per event —
`{ "text": "…" }` for Slack-compatible receivers (Slack, Mattermost, Rocket.Chat),
`{ "content": "…" }` for Discord — no signature and no `x-pstack-*` headers, because for an
incoming-webhook URL the URL itself is the credential. The lines come from one shared formatter,
so both providers always describe an event identically, e.g.:

```
Deploy succeeded on shopfront-pr-7 in 42s.
Teardown LEAKED on shopfront-pr-7 — resources survived and nothing will retry. Leaked: db, dns.
Test delivery from pstack — the connection works. No job ran.
```

Build on the `webhook` type if you need the structured payload.

## What is never in a payload

Payloads carry **identity and status — never contents or credentials**. Job events send state and
counts, not the outcome object (whose `outputs` is the documented inter-axis credential channel).
`routing.changed` sends the file's *name*, never its content (dynamic-config files can hold
basicAuth hashes). Spec events send names and metadata, never spec source (hooks routinely embed
credentials). Additionally, host-secret values are scrubbed from the `error` field by content.

---

## Event catalogue

### `deployment.created` / `deployment.updated`

Fires when `PUT /api/deployments/:id` stores a new record (`created`) or replaces an existing one
(`updated`). Fires on submission — **it does not mean containers started**; that is `job.*`.

| `data.` field | Type | Meaning |
|---|---|---|
| `id` | string | The deployment id, e.g. `pr-7`. |
| `kind` | `"isolated"` \| `"shared"` | From the spec. |
| `stack` | string | The resolved stack name (compose project). |
| `specName` | string \| null | The stored spec it references, or `null` when the spec is inline. |
| `stackSharedWith` | string[] | Other deployment ids resolving to the **same stack** — a collision warning: `down` on either stops the other's containers. Usually empty. |

```json
{ "id": "pr-7", "kind": "isolated", "stack": "shopfront-pr-7", "specName": null, "stackSharedWith": [] }
```

### `deployment.deleted`

Fires when `DELETE /api/deployments/:id` forgets the record. **Nothing was torn down** — the
server refuses to forget while containers exist, so by the time this fires the stack was already
gone or was never up.

| `data.` field | Type | Meaning |
|---|---|---|
| `id` | string | The forgotten deployment id. |
| `stack` | string | Its resolved stack name. |
| `kind` | `"isolated"` \| `"shared"` | From the spec. |

### `job.started`

Fires when a job is **dispatched** — the moment its stack is free and a concurrency slot exists,
which is not always the moment it was accepted. A stack runs one job at a time and the host runs at
most `PSTACK_MAX_JOBS` (4) at once, so an accepted job may sit `queued` first and emit nothing until
its turn comes. There is deliberately **no** `job.queued` event: the `202` that accepted it already
told the one caller holding that id.

| `data.` field | Type | Meaning |
|---|---|---|
| `jobId` | string | Follow it at `/jobs/<jobId>` (UI) or `GET /api/jobs/<jobId>`. |
| `stack` | string | The stack being acted on. |
| `action` | `"up"` \| `"down"` \| `"verify"` \| `"sleep"` \| `"wake"` | `sleep` takes the compose project down and keeps its volumes and axes; `wake` is `up` recorded under its own name (0.26.0). |
| `startedAt` | number | Epoch ms. |

### `job.succeeded` / `job.failed` / `job.cancelled` / `job.leaked` / `job.superseded`

Exactly one fires per job, when it reaches a terminal state. All five share one payload shape.

**`job.cancelled` is not a failure.** A person stopped the job with `POST /api/jobs/:id/cancel`; the
shell command in flight was killed and every later one refused. **Nothing was undone** — whatever the
job had already created or destroyed is still that way, which is why this is its own event and not a
flavour of `job.failed`. `data.cancelledBy` names who stopped it.

**`job.superseded` means nothing ran.** A stack runs one job at a time and queues at most one more;
a third replaces the queued one. The replaced job had never started, so nothing was created, nothing
was half-done, and there is nothing to check — it fires only so a client holding that job id learns
the id is finished with. It carries no `startedAt` and no duration, because there was none. Do not
alert on it: pushing twice in a minute produces one, and treating it like a cancellation trains
people to ignore cancellations, which are not routine at all. Note the consequence for a receiver
that pairs events by `jobId`: a superseded job emits **no** `job.started`, so an id can reach a
terminal event having never produced a starting one. That is the contract, not a lost delivery.

**`job.leaked` is the event this product exists for**: a teardown ran and `assert_gone` found
resources still present. They will not be retried — nothing else will clean them up. If you page
on one thing, page on this.

| `data.` field | Type | Meaning |
|---|---|---|
| `jobId` | string | |
| `stack` | string | |
| `action` | `"up"` \| `"down"` \| `"verify"` | |
| `state` | `"ok"` \| `"failed"` \| `"cancelled"` \| `"leaked"` \| `"superseded"` | Matches the event name. |
| `cancelledBy` | string? | `job.cancelled` only — the operator who stopped it. |
| `startedAt` | number \| **null** | Epoch ms, or `null` for a `superseded` job — it never started, and `0` would be a lie about 1970. |
| `endedAt` | number | Epoch ms. |
| `durationMs` | number | `endedAt - startedAt`, and `0` when the job never started. |
| `leakedAxes` | string[] | The axes whose `assert_gone` failed — the operator-actionable part of a leak. Empty unless `leaked`. |
| `verified` | boolean \| **null** | Whether teardown was actually **proven**: `true` = at least one `assert_gone` ran; `false` = nobody looked (`verify: false`, or a spec with no `assert_gone`) — so `ok` does **not** mean "proven clean"; `null` = not applicable (`up` runs no `assert_gone` by design). |
| `unverifiable` | number | Steps that could not check anything (no probe defined). Non-zero means silence, not proof. |
| `error` | string? | Crash path only: first line of the failure, redacted. Absent on success. |

```json
{
  "jobId": "down-shopfront-pr-7-42-x1y2z3",
  "stack": "shopfront-pr-7",
  "action": "down",
  "state": "leaked",
  "startedAt": 1754000000000,
  "endedAt": 1754000012000,
  "durationMs": 12000,
  "leakedAxes": ["db", "dns"],
  "verified": true,
  "unverifiable": 0
}
```

### Readiness: `healthcheck.*`, `container.*`, `stack.*`

`job.succeeded` for an `up` means **the commands ran** — `compose up -d` returns once the containers
are *created*. Whether the app booted, passed its healthcheck, or crashed two seconds later is a
separate question, and these events are the answer to it.

A **watch** starts automatically after every successful `up`. It polls Docker every 2s and ends in
**exactly one** of `stack.ready`, `stack.failed`, `stack.timedout` — never in silence — after 180s
by default. A teardown cancels the watch, so no terminal event fires for containers being removed on
purpose.

`GET /api/deployments/:id/readiness` also starts a watch when the stack has none (so a stack
deployed from the CLI, or before a server restart, is still answerable) — but that one is **silent**:
it emits nothing. A read must not manufacture an event about a deploy nobody ran, and without this a
page view of a submitted-but-never-deployed deployment would post "did not become ready in time" to
every notifier three minutes later. The reader still gets the full state in the response.

**What "ready" means.** A container *with* a healthcheck is ready when Docker reports `healthy`. One
*without* is ready when it is simply running — the honest ceiling, since nothing on the host knows
what "serving" means for that image. Payloads carry `hasHealthcheck` so "ready" is never read as
"probed". A container that **exited 0** counts as ready: one-shot migration and seed services are
supposed to finish, and calling that a crash would fail every stack that has one. A container that
has **restarted 3+ times** is a crash loop and fails — with a `restart:` policy a container that dies
on boot cycles `exited → restarting → running`, so no single sample of its state can tell that from a
slow start, but the restart counter can. One or two restarts are forgiven: an app that dies once
waiting for its database is the normal case.

#### `healthcheck.started` / `healthcheck.updated` / `healthcheck.finished` / `healthcheck.timedout`

One container's health probe, as Docker's own status string changes. `started` on the first
observation, `updated` on every change after it, `finished` when it settles (`healthy` or
`unhealthy` — the probe is terminal even though the container keeps running), `timedout` when the
watch's deadline arrived while it was still `starting`. A first observation that is already settled
emits `started` **and** `finished` — that is the honest report, not a duplicate.

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `container` | string | Container name, e.g. `shopfront-pr-7-app-1`. |
| `service` | string \| null | The compose service, when the container carries the label. |
| `status` | `"starting"` \| `"healthy"` \| `"unhealthy"` | Docker's health status. |
| `previous` | string | `updated` only — the status it changed from. |
| `healthy` | boolean | `finished` only. |
| `waitedMs` | number | `timedout` only — how long the watch waited. |

#### `container.started` / `container.stopped` / `container.restarted`

A **person** acted on one container through
`POST /api/deployments/:id/containers/:name/(start|stop|restart)`. Separate from the readiness
family, which observes what docker did on its own: these carry `by`.

`container.stopped` is worth a rule of its own — it also **cancels the readiness watch** for that
stack, because a watch left running would report `stack.failed` about a container someone meant to
stop. A start or restart (re)starts the watch instead, so "did it come back healthy" is answered
without anyone asking.

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `deployment` | string | The deployment id the container belongs to. |
| `container` | string | Container name. |
| `service` | string \| null | Its compose service. |
| `action` | `"start"` \| `"stop"` \| `"restart"` | |
| `by` | string | Who did it — a username, or `root (PSTACK_TOKEN)`. |

#### `container.ready` / `container.start-failed`

The per-container verdict, at most once per container per watch.

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `container`, `service` | string, string\|null | As above. |
| `state` | string | Docker's word: `running`, `exited`, `restarting`, … |
| `health` | string \| null | `null` when the image declares no healthcheck. |
| `hasHealthcheck` | boolean | `container.ready` only — `false` means running, not probed. |
| `exitCode` | number \| null | `container.start-failed` only. |
| `restartCount` | number | Restarts Docker has performed (snapshot only; see the crash-loop rule above). |
| `reason` | string | `container.start-failed` only, one line: `exited with code 1`, `restarted 5 times — crash loop (last exit 1)`, `healthcheck reports unhealthy`, `container is dead`. |

#### `stack.ready` / `stack.failed` / `stack.timedout`

The aggregate verdict — **exactly one per watch**. `ready` when every container is ready (and there
is at least one); `failed` as soon as any container fails; `timedout` when the deadline passed with
containers still converging.

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `state` | `"ready"` \| `"failed"` \| `"timedout"` | Matches the event name. |
| `containers` | number | How many were seen. |
| `ready` | number | How many were ready at the verdict. |
| `failedContainers` | string[] | Names that failed. |
| `pendingContainers` | string[] | Names still converging — the list that makes a timeout actionable. |
| `durationMs` | number | Watch duration. |
| `reachable` | boolean | `false` means Docker stopped answering; every container field is then last-known, not current. |

```json
{
  "stack": "shopfront-pr-7",
  "state": "timedout",
  "containers": 3,
  "ready": 2,
  "failedContainers": [],
  "pendingContainers": ["shopfront-pr-7-worker-1"],
  "durationMs": 180000,
  "reachable": true
}
```

**Polling or waiting instead of subscribing.** `GET /api/deployments/:id/readiness` returns the same
snapshot the events narrate (`state`, plus a `containers[]` of `{ name, service, state, health,
hasHealthcheck, exitCode, restartCount, ready, failed, reason? }`). Add `?wait=<seconds>` (max 60) to long-poll —
it returns as soon as the state is terminal, or with `state: "watching"` when the wait expires, so a
CI step loops while `state === "watching"` with no missed edge. `?refresh=1` re-runs a watch that
already settled; `?timeout=<seconds>` overrides the deadline for a watch this request starts.

### `spec.stored`

Fires when `PUT /api/specs/:name` stores (`replaced: false`) or replaces (`replaced: true`) a
named spec. Deployments referencing it pick the change up on their **next** action.

| `data.` field | Type | Meaning |
|---|---|---|
| `name` | string | The spec's name. |
| `kind` | `"isolated"` \| `"shared"` | |
| `replaced` | boolean | |
| `requiredVars` | string[] | Variables the spec needs at resolve time, e.g. `["PR"]`. |

### `spec.deleted`

Fires when `DELETE /api/specs/:name` removes a stored spec (refused while deployments reference it).

| `data.` field | Type | Meaning |
|---|---|---|
| `name` | string | |

### `routing.changed`

Fires when a Traefik dynamic-config file is written or deleted through the API. The file **name**
only — the content can hold credentials and is never sent.

| `data.` field | Type | Meaning |
|---|---|---|
| `file` | string | e.g. `auth.yml`. |
| `action` | `"created"` \| `"replaced"` \| `"deleted"` | |

### `stack.slept`

Fires when a stack's compose project was taken down **while its volumes and axes stayed** — by the
scheduler (`sleep: { idle, after }` in the spec) or by `POST …/sleep`. A request to any of `hosts`
wakes it. (0.26.0)

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `deployment` | string | The registry id. |
| `reason` | string | `idle 2h`, `after 3d`, or `operator: <actor>`. |
| `hosts` | string[] | The exact hostnames captured from its Traefik labels before teardown — what wakes it. Wildcard patterns are not listed here. |

### `stack.woken`

Fires when a sleeping stack starts coming back — at the **start** of the wake job, so a notifier sees
the request that caused it, not only the outcome (the `job.*` family reports that). (0.26.0)

| `data.` field | Type | Meaning |
|---|---|---|
| `stack` | string | |
| `deployment` | string | |
| `by` | string | `request:<hostname>` when traffic through the catch-all router woke it; otherwise the actor who called `POST …/wake`. |

### `share.created`

Fires when a read-only link to one deployment is minted (`POST …/share`). Says **what was granted**
— never the token, which exists only in the 201 response. (0.26.0)

| `data.` field | Type | Meaning |
|---|---|---|
| `deployment` | string | The registry id. |
| `stack` | `null` | Present for shape stability; the link is keyed on the deployment, not the resolved stack. |
| `views` | `("details" \| "logs")[]` | What the link can open. |
| `expiresAt` | number | Epoch ms. |
| `by` | string | The actor who minted it. |

---

## Test deliveries

`POST /api/notifiers/:id/test` (the **Test** button) sends directly to one notifier, not through
the bus. The envelope reuses the `job.succeeded` event name, so receivers that format per event
**must** check `data.test === true` — the payload is
`{ "test": true, "note": "Test delivery from pstack — no job ran." }`. The chat types already do.
