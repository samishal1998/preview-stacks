# Moving stacks between hosts: `pstack migrate` and forwarding

> **Design, approved 2026-11-03. Not built.** Owner decisions are marked **(owner)**. Cut to the
> smallest version on review (owner, 2026-11-03); what was cut and when to add it back is under
> "Out of scope".

## Why

Moving preview.hou-clou.dev's 89 deployments to a new host on 2026-11-02 took a hand-written script
(SSH, `docker exec`, `deployments source get` → `deployments put`) and showed:

1. **No command.** `pull config` / `push config` move a host's configuration and deliberately leave
   deployments out.
2. **State was lost.** Stacks asleep on the old host were only registered on the new one, so a
   request no longer woke them.
3. **CI kept deploying to the old host** while the copy ran (pr-19955, pr-19956, pr-19957 appeared
   mid-copy).

Two pieces close them: `pstack migrate` copies deployments and their state; **forwarding** makes the
old host pass deploys (§1) and visitor requests it no longer serves (§2) to the new one, so CI and
visitors keep working whichever host their DNS reaches.

## The setting

One optional setting per host **(owner: per host)**, shared by both kinds of forwarding. On host A:

```
forward: { to: <B's API URL>, via: <B's IP[:port]>?, token: <a token on B>, establishedAt, lastOkAt }
```

Stored in A's SQLite (one migration, one row). No read returns the token. `pull config` carries it
inside the sealed export like every other credential.

**`via`** is where A connects; the hostname is still sent as the Host header and the TLS server
name, so B's certificate is checked normally. It is needed whenever A and B answer on the same
domain: before DNS moves, `api.<domain>` resolves to A itself. Without `via`, A resolves `to`'s host.

## 1. Deploy forwarding

### What it does

While set, A passes every request under `/api/deployments` and `/api/jobs` to B unchanged, with B's
token in place of the caller's. One `httputil.ReverseProxy`. B's answer, status and body go back to
the caller as they are.

- **Deploys run on B only.** There is no mirror mode.
- **Job ids are B's.** CI's existing `up` → poll `GET /api/jobs/:id` → stream, all against A, reach
  B's job. Nothing on A tracks it.
- **The caller is still authenticated by A first,** at A's normal tier for that route; only then is
  the request passed on. B's token never reaches the caller.
- **A's own records go stale** while forwarding is on. A is the host being retired.
- The setting is read per request; `forward off` applies to the next call.
- B needs nothing new.

### Loop guard

A adds `X-Pstack-Forwarded-By: <A's domain>`. A request that already carries it is served locally,
never forwarded. `forward set` refuses a `to` that is A's own API URL. The same header is how
`migrate --sleep-source` acts on A itself while A forwards (§3).

### The token

A user token on B, **developer role or higher** — the deployment routes' tier (`permissions.go`). A
root token is refused: one host's root credential must not sit on another.

### Failure handling **(owner)**

Depends on whether forwarding has worked before.

**Establishing — any failure is a full failure.** `forward set` checks before saving anything:

1. `GET /api/auth/me` on B with the token answers 200;
2. `root: false`, and the role is developer or higher;
3. `to` is not A's own API URL.

Any failure answers 400 naming the check, and nothing is saved. Success sets `establishedAt` and
`lastOkAt`.

**Established — retry, then report.** A request that **never reached B** — connection refused, DNS
failure, TLS handshake failure — is retried: 3 attempts in total, 2 s then 5 s apart. Only those,
because a request B received may already have started a job, and sending it again could start a
second. When the attempts run out, A answers **502** `forward: api.B unreachable after 3 attempts`
and emits `forward.failed`. Anything B answers — a 401, a 409, a 500 — goes to the caller as B's
answer, unretried. Every answer from B updates `lastOkAt`.

### Surface

- `GET /api/forward` — `{ to, establishedAt, lastOkAt }` or `null`. Maintainer.
- `PUT /api/forward` — `{ to, via?, token }`, checked as above. **Admin**: it decides where every
  deploy runs. `DELETE /api/forward` — off. Admin.
- CLI: `pstack forward`, `pstack forward set <url> [--via <ip[:port]>]`, `pstack forward off`. The
  token comes from `PSTACK_FORWARD_TOKEN`, never a flag (argv is readable through `ps`). Plus the
  generated `pstack api forward …`.
- UI: one line on the Control page — `Forwarding to api.B · last ok 3m ago`.
- Events: `forward.changed` (`{ to, via }` or `null`) and `forward.failed` (`{ to, error, attempts }`).
  Neither carries the token.

## 2. Traffic forwarding **(owner, added 2026-11-03)**

While forwarding is on, a visitor request for a preview hostname **A does not serve** goes to B.
Previews still running on A keep serving from A: their Traefik routers outrank the catch-all.

### Where

Such requests already reach the pstack process: Traefik's priority-1 wake catch-all
(`pstack-wake-*`) sends every one-label preview hostname with no router of its own to pstack, which
today wakes a sleeping stack or answers the Down page (`server.go:1087-1107`). Traffic forwarding is
one check before the wake block:

```
forwarding on  &&  previewHostname(r.Host)  &&  no X-Pstack-Forwarded-By   →   proxy to B
```

- **Before wake, on purpose.** A stack asleep on A is forwarded, not woken on A: waking would start
  a stale copy on the host being retired. B wakes its own copy (`migrate` puts asleep stacks to sleep
  on B).
- One `httputil.ReverseProxy`. The outbound request keeps the original Host and URL host, so Go sends
  it as the TLS server name and checks B's certificate for it; the dialer connects to `via` (or `to`'s
  host) on 443 instead of resolving the preview hostname, which may point back at A. WebSockets pass
  through; pstack's server has no write timeout (`server.go:982`).
- `X-Pstack-Forwarded-By` is added, so B never forwards it again; a hostname B does not serve gets
  B's own Down page.
- These requests are unauthenticated visitor traffic, as they are today. The proxy only ever goes to
  B and never reaches A's UI or API.

### What B must have

The preview's domain must be **one B answers on** — B's primary, or added with `PUT /api/domains` /
`--extra-domain`. A preview's hostname comes from its own vars (`PREVIEW_DOMAIN`), which `migrate`
copies unchanged, so B's routers already carry A's hostnames; the domain requirement gives B the
certificate and its own wake catch-all for them.

**Certificates:**

- `dns-01` / `dns-persist-01`: B's wildcard covers the domain regardless of DNS. Works at once.
- `http-01`: B can obtain a hostname's certificate only once public DNS points at B. Until then the
  TLS check fails and the visitor gets A's Down page. Hence the recipe's order (§4).

### Failures

No server-side retry: a retry would hold a visitor's request open. Any failure to reach B — dial,
TLS, timeout — answers A's existing Down page (503, `retry-after: 5`), which the browser already
retries, and logs one line. No event per request; `GET /api/forward` shows `lastOkAt`.

## 3. `pstack migrate`

```
pstack migrate --from <API URL> --to <API URL> [<id>...] [--sleep-source]
```

Runs from a laptop or CI against both hosts' APIs **(owner)**; the hosts never call each other for
the copy. Tokens from the environment: `PSTACK_FROM_TOKEN`, `PSTACK_TO_TOKEN`. Separate from
`pstack forward` **(owner)**; the recipe below runs them in order.

For each deployment on the source (or each `<id>` given):

1. **Copy** spec, compose and stored vars, unchanged, when the target does not have it or the
   source's `updatedAt` is newer. Otherwise leave the target's alone.
2. **Match the source's state:**
   - running → `up`;
   - asleep → `up`, then `sleep`, so it wakes on request as before;
   - down → nothing.
3. Wait for the jobs and print one line: id, source state, result.

Exit 0 when all are ok, 1 when any failed. A failed `up` (a seed failing, an image that will not
pull) is a failure.

**Run it again to catch up.** Copies are skipped unless the source is newer, so a second run does
only what changed since the first. With forwarding on before the first run, there is nothing to
catch up.

Without `--sleep-source` it never changes the source's stacks.

**`--sleep-source`** puts each source copy to sleep once the target's copy is ok, so traffic for it
reaches B (§2). The call carries `X-Pstack-Forwarded-By: migrate`, so A runs it itself instead of
forwarding it. Sleep, never `down`: sleep removes containers and keeps volumes and runs no hooks,
while `down` runs the axis `down` hooks, and those delete external resources keyed by the stack name
(a database branch, a DNS record) that the copy on B uses too. Stacks sleep refuses (`kind: shared`,
no compose section) are left running and listed.

```go
// ponytail: newest updatedAt wins, comparing two hosts' clocks. Fine under NTP; a per-deployment
// revision counter if clocks ever disagree by more than a deploy's length.
```

## 4. Cutover recipe (for usage.md)

1. `pstack pull config` on A, `pstack push config` on B — accounts, registries, routing, settings.
   B must answer on A's domain (§2).
2. `pstack forward set <B's API URL> --via <B's IP>` on A — every deploy from here runs on B.
3. `pstack migrate --from A --to B`.
4. Switch DNS.
5. `pstack migrate --from A --to B --sleep-source` — copies nothing new; sleeps A's copies, so
   visitors still reaching A are forwarded to B. After DNS, because under `http-01` B gets its
   certificates only then.
6. When nothing reaches A any more: retire it. Destroying the machine runs no hooks, so B's external
   resources are safe. **Never `down` A's copies** (§3).

## Out of scope — cut on review, add back when needed

- **Mirror mode** (deploys on A and B). When A must stay a live fallback during a cutover.
- **Teardown everywhere** (`down` on A's old copies too). When A is not being retired.
- **A's job tracking B's job, A's UI listing forwarded deploys.** When someone needs A's own
  history of what it forwarded.
- **`migrate` preflight, `--dry-run`, conflict reporting, `--force`, the catch-up loop.** Re-running
  is the catch-up loop; newest-wins replaces conflicts.
- **Retrying forwarded visitor requests, events per request.** When a dashboard needs a traffic
  failure count.
- **Forwarding hostnames more than one label deep** (`a.web-pr-1.<domain>`): the catch-all matches one
  label only. When a wildcard-subdomain preview needs it.
- Copying data: volumes and databases do not move; each stack on the target runs its own seed.
- Stopping the source's copies. More than one peer. Host configuration (`pull`/`push config`).

## Testing

Go tests in `internal/api` with two in-process servers and the fake docker shim, each with a negative
control:

- forwarded `up` runs on B and not on A; CI's poll and stream through A reach B's job;
- establishing: unreachable, root token, viewer token, own URL — each refused, nothing saved;
- established: B down → 3 attempts then 502 and `forward.failed`; B answering 409 → passed through,
  not retried;
- the caller is checked by A's own tier before forwarding; a viewer cannot deploy through A;
- loop guard: A→B and B→A both on, one deploy → one job, on B;
- the token never appears in `GET /api/forward`, events, or any response;
- `via`: `to` names a host that resolves to A, `via` points at B → the call reaches B with B's Host.

Traffic, with B as an `httptest` TLS server whose certificate covers the preview domain:

- an unserved preview hostname on A reaches B with its original Host; the response is B's;
- a hostname asleep on A is forwarded, and no wake job starts on A;
- a request carrying `X-Pstack-Forwarded-By` is not forwarded (Down page);
- B's certificate not covering the hostname → A's Down page, no retry;
- control hostnames (`control.`, `api.`, `loki.`, `grafana.`) are never forwarded;
- forwarding off → today's behaviour, byte for byte.

`migrate`, against two in-process servers: running, asleep and down stacks each arrive in their
state; an older source is skipped; a newer source is copied and brought up; a second run does
nothing; `--sleep-source` sleeps the source copy through a forwarding A, runs no `down`, and lists a
`shared` stack it could not sleep.

`openapi.yaml` gains `/api/forward`, then `go generate ./packages/pstack/internal/apicli`.
Conformance goldens for the new CLI help and routes.

**Not automatable:** a real two-host cutover, done by hand on preview / preview-temp before release.

## Docs

`usage.md` (forwarding, `migrate`, the cutover recipe, and the warning never to `down` a migrated
source copy), `skills/pstack/SKILL.md`,
`webhook-events.md` (`forward.changed`, `forward.failed`), `docs/README.md`, the CHANGELOG.
