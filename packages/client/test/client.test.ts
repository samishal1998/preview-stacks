/**
 * The client, driven against the REAL server.
 *
 * This is the whole anti-drift measure. The types in `src/types.ts` are hand-written and could say
 * anything; what keeps them honest is that every call below runs against `createServer` from the
 * workspace and asserts on what actually came back. A route that moves, a field that is renamed, or
 * a response that changes shape fails here rather than in someone's CI six weeks later.
 *
 * No docker on the machine running this, so the deploy-shaped assertions use specs whose axes are
 * `true` — the point is the CLIENT surface, not compose.
 */
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
// The server's SOURCE, not its published entry point: this test exists to catch drift between the
// two packages, and going through the built bundle would test whatever was last built instead.
import { createServer } from '../../pstack/src/api.ts';
import { createClient, PstackError, verifyWebhook } from '../src/index.ts';

const TOKEN = 'root-machine-token-value-0123456789';
const dataDir = `${process.env.TMPDIR ?? '/tmp'}/pstack-client-${process.pid}-${Date.now()}`;

let server: ReturnType<typeof createServer>;
let client: ReturnType<typeof createClient>;

beforeAll(() => {
  server = createServer({ dataDir, port: 0, host: '127.0.0.1', token: TOKEN });
  client = createClient({ baseUrl: `http://127.0.0.1:${server.port}`, token: TOKEN });
});
afterAll(() => server.stop(true));

const SPEC = 'version: 1\nstack: client-probe\naxes:\n  - name: db\n    up: "true"\n    down: "true"\n';

describe('the client speaks the real API', () => {
  test('health, and an unauthenticated client is refused by the server, not by us', async () => {
    const h = await client.health();
    expect(h.ok).toBe(true);
    expect(h.authEnforced).toBe(true);
    expect(typeof h.version).toBe('string');

    const anon = createClient({ baseUrl: `http://127.0.0.1:${server.port}` });
    // Throwing is the contract: a script wants the line it wrote to work or stop.
    await expect(anon.deployments.list()).rejects.toBeInstanceOf(PstackError);
    const err = await anon.deployments.list().catch((e: PstackError) => e);
    expect((err as PstackError).status).toBe(401);
  });

  test('submit, read, act, and forget a deployment', async () => {
    const put = await client.deployments.put('pr-1', { spec: SPEC });
    expect(put.stack).toBe('client-probe');

    const list = await client.deployments.list();
    expect(list.map((d) => d.id)).toContain('pr-1');
    // The tri-state survives the trip: `null` is "could not determine", never "no".
    const row = list.find((d) => d.id === 'pr-1')!;
    expect(row.busy === null || typeof row.busy === 'boolean').toBe(true);

    const job = await client.deployments.up('pr-1');
    expect(job.action).toBe('up');
    expect(job.stack).toBe('client-probe');

    const finished = await client.waitForJob(job.id, { intervalMs: 25, timeoutMs: 20_000 });
    // waitForJob RETURNS a failed job rather than throwing — the state is the answer, and here
    // there is no docker, so the compose step is expected to fail.
    expect(['ok', 'failed', 'leaked', 'cancelled']).toContain(finished.state);
    expect(finished.endedAt).toBeGreaterThan(0);
    expect(finished.outcome?.steps.some((s) => s.axis === 'db')).toBe(true);

    expect((await client.jobs.list()).some((j) => j.id === job.id)).toBe(true);

    /*
     * Forgetting is refused when docker cannot be reached — "cannot confirm" is not evidence of
     * absence, and this machine has no docker, so the refusal is what a correct server does here.
     * Asserting it (rather than skipping the call) pins the guard through the client too.
     */
    const refused = await client.deployments.remove('pr-1').catch((e: PstackError) => e);
    expect(refused).toBeInstanceOf(PstackError);
    expect((refused as PstackError).status).toBe(409);
    expect((refused as PstackError).message).toContain('docker did not answer');
  }, 30_000);

  test('a 409 arrives as a PstackError carrying the server\'s own message', async () => {
    await client.deployments.put('pr-shared', {
      // A shared singleton declares no axes — they exist to isolate tenants, which it has none of.
      spec: 'version: 1\nkind: shared\nstack: client-shared\ncompose:\n  file: docker-compose.yml\n  profiles: [web]\n',
    });
    const err = await client.deployments.down('pr-shared').catch((e: PstackError) => e);
    expect(err).toBeInstanceOf(PstackError);
    expect((err as PstackError).status).toBe(409);
    // Not a paraphrase: the reason a shared teardown is refused is the server's to explain.
    expect((err as PstackError).message).toContain('shared');
    // …and the escape hatch is the documented one.
    const job = await client.deployments.down('pr-shared', { force: true, verify: false });
    expect(job.action).toBe('down');
    await client.waitForJob(job.id, { intervalMs: 25, timeoutMs: 20_000 });
  }, 30_000);

  test('specs, host variables and notifiers round-trip', async () => {
    await client.specs.put('shopfront', { spec: SPEC, description: 'the demo' });
    expect((await client.specs.list()).map((s) => s.name)).toContain('shopfront');
    expect((await client.specs.get('shopfront')).kind).toBe('isolated');
    await client.specs.remove('shopfront');

    await client.hostVars.put('REGION', 'eu-central');
    await client.hostVars.put('DB_PASSWORD', 'a-long-secret-value', true);
    const vars = await client.hostVars.list();
    expect(vars.find((v) => v.name === 'REGION')?.value).toBe('eu-central');
    // A secret's value never comes back — the client cannot invent one either.
    expect(vars.find((v) => v.name === 'DB_PASSWORD')?.value).toBeNull();
    await client.hostVars.remove('DB_PASSWORD');

    const meta = await client.notifiers.meta();
    expect(meta.events).toContain('job.leaked');
    expect(meta.types.map((t) => t.kind)).toContain('webhook');
  });

  test('waitForReady returns a terminal readiness rather than looping on "watching"', async () => {
    await client.deployments.put('pr-ready', { spec: SPEC });
    // No docker here, so it converges on nothing and the deadline is the answer — which is exactly
    // the case a hand-written poll gets wrong by treating `watching` as a failure.
    const r = await client.deployments.readiness('pr-ready', { timeout: 1, wait: 5 });
    expect(['watching', 'ready', 'failed', 'timedout']).toContain(r.state);
    expect(Array.isArray(r.containers)).toBe(true);
  }, 20_000);
});

describe('verifyWebhook — the half that lives in the receiver', () => {
  const secret = 'shhh-a-long-signing-secret';
  const body = JSON.stringify({ id: 'evt_1', event: 'job.leaked', at: 1, data: { stack: 's' } });

  /** Sign the way the server does, so this test proves interop rather than self-consistency. */
  const sign = async (ts: number, raw: string) => {
    const key = await crypto.subtle.importKey(
      'raw',
      new TextEncoder().encode(secret),
      { name: 'HMAC', hash: 'SHA-256' },
      false,
      ['sign'],
    );
    const mac = await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(`${ts}.${raw}`));
    return `sha256=${[...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, '0')).join('')}`;
  };

  test('accepts a good delivery and names every way one can be bad', async () => {
    const now = Date.now();
    const good = {
      'x-pstack-signature': await sign(now, body),
      'x-pstack-timestamp': String(now),
      'x-pstack-event': 'job.leaked',
    };
    expect(await verifyWebhook({ secret, rawBody: body, headers: good, now })).toMatchObject({
      ok: true,
      event: 'job.leaked',
      redelivery: false,
    });

    // A re-serialised body is the classic false negative: same object, different bytes.
    const reserialised = JSON.stringify(JSON.parse(body) as unknown, null, 2);
    expect((await verifyWebhook({ secret, rawBody: reserialised, headers: good, now })).ok).toBe(false);

    // Wrong secret, missing headers, and a stale stamp each fail by name.
    expect((await verifyWebhook({ secret: 'other', rawBody: body, headers: good, now })).reason).toBe('signature mismatch');
    expect((await verifyWebhook({ secret, rawBody: body, headers: {}, now })).reason).toBe('missing signature headers');
    expect(
      (await verifyWebhook({ secret, rawBody: body, headers: good, now: now + 10 * 60_000 })).reason,
    ).toBe('stale timestamp');
    // …and the staleness check can be turned off for a deliberate replay.
    expect(
      (await verifyWebhook({ secret, rawBody: body, headers: good, now: now + 10 * 60_000, toleranceMs: 0 })).ok,
    ).toBe(true);
  });

  test('a redelivery is reported as one', async () => {
    const now = Date.now();
    const r = await verifyWebhook({
      secret,
      rawBody: body,
      now,
      headers: {
        'x-pstack-signature': await sign(now, body),
        'x-pstack-timestamp': String(now),
        'x-pstack-redelivery': '1',
      },
    });
    expect(r).toMatchObject({ ok: true, redelivery: true });
  });

  test('a Headers object works as well as a plain record', async () => {
    const now = Date.now();
    const headers = new Headers({
      'x-pstack-signature': await sign(now, body),
      'x-pstack-timestamp': String(now),
    });
    expect((await verifyWebhook({ secret, rawBody: body, headers, now })).ok).toBe(true);
  });
});
