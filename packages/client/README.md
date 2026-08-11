# @samyx/preview-stacks-client

Typed API client for a [pstack](https://github.com/samishal1998/preview-stacks) control plane —
deployments, jobs, readiness, containers, logs and notifiers, plus webhook verification for the
receiving end.

Zero dependencies. Works in Node, Bun, Deno and the browser (it uses `fetch` and WebCrypto, nothing
else).

```bash
bun add @samyx/preview-stacks-client   # or npm / pnpm / yarn
```

## Use it

```ts
import { createClient } from '@samyx/preview-stacks-client';

const pstack = createClient({
  baseUrl: 'https://api.preview.example.com',
  token: process.env.PSTACK_TOKEN,          // machine token, or a personal pstack_pat_…
});

// Submit a spec and deploy it. Variables travel with EVERY call — `down` needs the same ones as
// `up`, or teardown targets a different stack than deploy created.
await pstack.deployments.put(`pr-${pr}`, { specName: 'shopfront', vars: { PR: String(pr) } });
const job = await pstack.deployments.up(`pr-${pr}`, { PR: pr });

// Wait for the commands to finish…
const finished = await pstack.waitForJob(job.id);
if (finished.state !== 'ok') throw new Error(`deploy ${finished.state}`);

// …and then for the app to actually be serving, which is a different question.
const ready = await pstack.waitForReady(`pr-${pr}`, { vars: { PR: pr } });
if (ready.state !== 'ready') {
  console.error(ready.containers.filter((c) => c.failed).map((c) => `${c.name}: ${c.reason}`));
}
```

### Errors throw

Every non-2xx becomes a `PstackError` carrying the status and the server's own parsed body, so a
script either works or stops:

```ts
import { PstackError } from '@samyx/preview-stacks-client';

try {
  await pstack.deployments.down('shared-services');
} catch (e) {
  if (e instanceof PstackError && e.status === 409) {
    // Refused: tearing down a `kind: shared` stack destroys volumes every preview depends on.
    await pstack.deployments.down('shared-services', { force: true });
  } else throw e;
}
```

The two waiters are the exception: `waitForJob` and `waitForReady` **return** a failed job or an
unready stack rather than throwing. The state is the answer, and a CI step usually wants to branch on
which one it got. They throw only when the wait itself fails.

### Containers

```ts
const rt = await pstack.deployments.runtime('pr-7', { PR: 7 });
for (const c of rt.containers.filter((c) => c.restartCount > 2)) {
  await pstack.containers.restart('pr-7', c.name, { vars: { PR: 7 } });
}
const logs = await pstack.deployments.logs('pr-7', { service: 'web', tail: 500, timestamps: true, vars: { PR: 7 } });
```

### Verify a webhook

The half that lives in your receiver. It prevents the two mistakes that make a correct signature look
wrong: re-serialising the body before hashing, and skipping the staleness check the signed timestamp
exists for.

```ts
import { verifyWebhook } from '@samyx/preview-stacks-client';

Bun.serve({
  async fetch(req) {
    const rawBody = await req.text();               // the untouched bytes, never a re-stringify
    const v = await verifyWebhook({ secret: process.env.HOOK_SECRET!, rawBody, headers: req.headers });
    if (!v.ok) return new Response(v.reason, { status: 401 });

    const event = JSON.parse(rawBody);
    if (v.redelivery) console.log('replay of', event.id);   // dedupe on event.id
    return new Response('ok');
  },
});
```

## What it does not do

- **No log streaming.** `deployments.logs()` fetches a tail; following is an SSE endpoint
  (`/api/deployments/:id/logs/stream`) and is left to `EventSource` or your own reader.
- **No terminal.** That is a WebSocket carrying a session cookie, which belongs to a browser.
- **No spec authoring helpers.** A spec is YAML you own; this client submits it verbatim.

Anything not wrapped is still reachable with the same auth and error handling:

```ts
await pstack.request('GET', '/api/routing/live');
```

## Versioning

Released in lockstep with the server. A client is compatible with any server of the same minor or
newer — routes and payload fields are added, never renamed (the same contract the webhook events
carry). Full API reference: [docs/usage.md](https://github.com/samishal1998/preview-stacks/blob/main/docs/usage.md).
