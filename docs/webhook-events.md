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
- Under pressure a delivery is **dropped and recorded as dropped** in the delivery log — never
  queued unboundedly. One delivery per notifier is in flight at a time.
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

Fires when a lifecycle action is accepted and its job begins.

| `data.` field | Type | Meaning |
|---|---|---|
| `jobId` | string | Follow it at `/jobs/<jobId>` (UI) or `GET /api/jobs/<jobId>`. |
| `stack` | string | The stack being acted on. |
| `action` | `"up"` \| `"down"` \| `"verify"` | |
| `startedAt` | number | Epoch ms. |

### `job.succeeded` / `job.failed` / `job.leaked`

Exactly one fires per job, when it reaches a terminal state. All three share one payload shape.

**`job.leaked` is the event this product exists for**: a teardown ran and `assert_gone` found
resources still present. They will not be retried — nothing else will clean them up. If you page
on one thing, page on this.

| `data.` field | Type | Meaning |
|---|---|---|
| `jobId` | string | |
| `stack` | string | |
| `action` | `"up"` \| `"down"` \| `"verify"` | |
| `state` | `"ok"` \| `"failed"` \| `"leaked"` | Matches the event name. |
| `startedAt`, `endedAt` | number | Epoch ms. |
| `durationMs` | number | `endedAt - startedAt`. |
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

---

## Test deliveries

`POST /api/notifiers/:id/test` (the **Test** button) sends directly to one notifier, not through
the bus. The envelope reuses the `job.succeeded` event name, so receivers that format per event
**must** check `data.test === true` — the payload is
`{ "test": true, "note": "Test delivery from pstack — no job ran." }`. The chat types already do.
