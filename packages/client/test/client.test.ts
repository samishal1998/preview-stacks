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
// The REAL server, spawned as a process by the conformance harness: `PSTACK_IMPL=bun` drives
// `bun src/cli.ts serve`, `PSTACK_IMPL=go` the built binary. Going through the published bundle
// would test whatever was last built; going through a process tests what an operator runs.
import { bootServer, type Booted } from '../../conformance/harness/server.ts';
import { IMPL, REPO } from '../../conformance/harness/impl.ts';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { createClient, PstackError, TERMINAL_JOB_STATES, verifyWebhook } from '../src/index.ts';

const TOKEN = 'root-machine-token-value-0123456789';

let server: Booted;
let client: ReturnType<typeof createClient>;

beforeAll(async () => {
  server = await bootServer({ tag: 'client', token: TOKEN });
  client = createClient({ baseUrl: server.base, token: TOKEN });
});
afterAll(() => server.stop());

const SPEC = 'version: 1\nstack: client-probe\naxes:\n  - name: db\n    up: "true"\n    down: "true"\n';

describe('the client speaks the real API', () => {
  test('health, and an unauthenticated client is refused by the server, not by us', async () => {
    const h = await client.health();
    expect(h.ok).toBe(true);
    expect(h.authEnforced).toBe(true);
    expect(typeof h.version).toBe('string');
    // The spawned implementation's own version — the lockstep the release asserts. Both runtimes
    // read it from the same package.json, so this is `${IMPL}` speaking, not a constant.
    const pkg = JSON.parse(readFileSync(join(REPO, 'packages/pstack/package.json'), 'utf8')) as { version: string };
    expect(`${IMPL}:${h.version}`).toBe(`${IMPL}:${pkg.version}`);

    const anon = createClient({ baseUrl: server.base });
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
     * DELETE fails closed, and BOTH outcomes here are the server being correct — which one you get
     * depends on the machine, so the assertion accepts either:
     *
     *   docker absent   → 409, "cannot confirm … docker did not answer" (not evidence of absence)
     *   docker present  → 200, nothing is running, so the record goes
     *
     * An earlier revision asserted only the refusal, with a comment claiming "this machine has no
     * docker" — true of the box it was written on and false of a CI runner, where docker is
     * installed. It has failed on every push since.
     */
    const removed = await client.deployments.remove('pr-1').catch((e: PstackError) => e);
    if (removed instanceof PstackError) {
      expect(removed.status).toBe(409);
      expect(removed.message).toContain('docker did not answer');
    } else {
      expect(removed.removed).toBe('pr-1');
    }
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

describe('0.26.0: sleep/wake, share links, the swarm', () => {
  test('sleep and wake start jobs under their own names', async () => {
    await client.deployments.put('pr-nap', { spec: SPEC.replace('client-probe', 'nap') + 'compose: {file: compose.yml, profiles: []}\n', compose: 'services: {}\n' });
    const s = await client.deployments.sleep('pr-nap');
    expect(s.action).toBe('sleep');
    const done = await client.waitForJob(s.id, { intervalMs: 10 });
    // No docker here: the compose step is best-effort and the job still finishes.
    expect(['ok', 'failed']).toContain(done.state);
    const asleep = await client.deployments.get('pr-nap');
    expect(asleep.asleep).toMatchObject({ reason: expect.stringContaining('operator') });
    const w = await client.deployments.wake('pr-nap');
    expect(w.action).toBe('wake');
    const woke = await client.waitForJob(w.id, { intervalMs: 10 });
    const d = await client.deployments.get('pr-nap');
    expect(d.orchestrator).toBe('compose');
    expect(d.sleep).toBeNull();
    // A wake that could not bring the project up (no docker here) leaves the record — the next
    // request to its hostname tries again. One that did clears it.
    if (woke.state === 'ok') expect(d.asleep).toBeNull();
    else expect(d.asleep).not.toBeNull();
  }, 20_000);

  test('a share link reaches its deployment and nothing else', async () => {
    await client.deployments.put('pr-shared', { spec: SPEC.replace('client-probe', 'shared') });
    await client.deployments.put('pr-other', { spec: SPEC.replace('client-probe', 'other') });
    const link = await client.deployments.share('pr-shared', { views: ['details'], ttl: '1h' });
    expect(link.views).toEqual(['details']);
    expect(link.url).toContain('/deployments/pr-shared/public-logs-view?token=');
    expect(link.expiresAt).toBeGreaterThan(Date.now());

    const viewer = createClient({ baseUrl: server.base, token: link.token });
    const seen = await viewer.deployments.get('pr-shared');
    expect(seen.stack).toBe('shared');
    await expect(viewer.deployments.logs('pr-shared')).rejects.toMatchObject({ status: 403 });
    await expect(viewer.deployments.get('pr-other')).rejects.toMatchObject({ status: 403 });
    await expect(viewer.deployments.up('pr-shared')).rejects.toMatchObject({ status: 403 });
    await expect(viewer.deployments.share('pr-shared')).rejects.toMatchObject({ status: 403 });
    await expect(client.deployments.share('pr-shared', { ttl: '90d' })).rejects.toMatchObject({ status: 400 });
  });

  test('the swarm panel is honest when docker does not answer, and the join material is text', async () => {
    const info = await client.swarm.info();
    expect(typeof info.reachable).toBe('boolean');
    expect(Array.isArray(info.nodes)).toBe(true);
    expect(info.ports.map((p) => p.port)).toEqual(['2377/tcp', '7946/tcp+udp', '4789/udp']);
    if (!info.reachable || !info.active) {
      // Not a manager (or no docker at all): joining is refused with the server's reason, never a token.
      await expect(client.swarm.join({ format: 'command' })).rejects.toMatchObject({ status: expect.any(Number) });
    } else {
      expect(await client.swarm.join({ format: 'command' })).toContain('docker swarm join --token');
    }
  });
});

describe('0.27.0: single sign-on', () => {
  test('providers round-trip under keys, and the client secret has no read path', async () => {
    const empty = await client.sso.config();
    expect(empty.providers).toEqual([]);
    // The one string an operator must register with every provider — served, never guessed.
    expect(empty.callbackUrl).toBe(`http://127.0.0.1:${server.port}/api/auth/sso/callback`);
    expect(empty.presets.map((p) => p.key)).toEqual([
      'github',
      'gitlab',
      'bitbucket',
      'google',
      'microsoft',
      'okta',
      'auth0',
      'keycloak',
    ]);
    expect((await client.health()).sso).toBeNull();

    const saved = await client.sso.save({
      mode: 'oauth2',
      provider: 'github',
      clientId: 'cid',
      clientSecret: 'the-real-secret',
      allowedEmailDomains: ['Example.COM'],
    });
    // The keyless save (what a pre-multi-provider script sends) derived the slug; the preset
    // filled the endpoints in, and the domain was normalized.
    expect(saved.key).toBe('github');
    expect(saved.config.authorizeUrl).toBe('https://github.com/login/oauth/authorize');
    expect(saved.config.claimMap.subject).toBe('id');
    expect(saved.config.allowedEmailDomains).toEqual(['example.com']);
    expect(saved.config.label).toBe('GitHub');

    const read = await client.sso.config();
    expect(read.providers.map((p) => p.key)).toEqual(['github']);
    expect(read.providers[0]!.secretSet).toBe(true);
    expect(JSON.stringify(read)).not.toContain('the-real-secret');
    // The login page's half, readable before authenticating: one button per enabled provider.
    expect((await client.health()).sso).toEqual({ providers: [{ key: 'github', label: 'GitHub', preset: 'github' }] });

    // A field the server refuses says so, as a PstackError with the server's own sentence.
    await expect(
      client.sso.save({ key: 'corp', mode: 'oauth2', provider: 'custom', clientId: 'c', clientSecret: 's' }),
    ).rejects.toThrow(/authorizeUrl and tokenUrl/);

    expect((await client.sso.remove('github')).ok).toBe(true);
    expect((await client.sso.config()).providers).toEqual([]);
    await expect(client.sso.remove()).rejects.toThrow(PstackError);
  });
});

describe('roles: an account carries one, and the client can read and set it', () => {
  const PASSWORD = 'a-long-enough-password';

  // negative control: in src/index.ts — drop `role` from users.create's body → the `developer`
  // assertion fails (the server then defaults it to viewer); swap `request('PATCH', …)` in
  // users.setRole for `post(…)` → setRole 404s; return `[]` instead of `r.users` in users.list →
  // the roster assertion fails; point `me()` at another route → `root` reads undefined.
  test('a created account defaults to VIEWER, and its role is readable, listable and changeable', async () => {
    // The machine credential sits above every role and holds no account — there is no role to read.
    const root = await client.me();
    expect(root.root).toBe(true);
    expect(root.user).toBeUndefined();

    // AN ABSENT ROLE IS THE LEAST PRIVILEGE. This route used to mint an administrator every time;
    // that it now mints a viewer is the breaking change, and this is where the client pins it.
    const viewer = await client.users.create({ username: 'role-probe-viewer', password: PASSWORD });
    expect(viewer.role).toBe('viewer');
    expect(viewer.email).toBeNull();

    const dev = await client.users.create({ username: 'role-probe-dev', password: PASSWORD, role: 'developer' });
    expect(dev.role).toBe('developer');

    const roster = await client.users.list();
    expect(roster.filter((u) => u.username.startsWith('role-probe-')).map((u) => `${u.username}=${u.role}`)).toEqual([
      'role-probe-dev=developer',
      'role-probe-viewer=viewer',
    ]);

    expect(await client.users.setRole(viewer.id, 'maintainer')).toEqual({ updated: viewer.id, role: 'maintainer' });
    expect((await client.users.list()).find((u) => u.id === viewer.id)?.role).toBe('maintainer');

    // What a script actually branches on is its OWN role. A session is a cookie, so this client is
    // handed a `fetch` that carries one — the same seam a proxy or a custom TLS agent uses.
    const login = await fetch(`${server.base}/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ username: 'role-probe-dev', password: PASSWORD }),
    });
    const cookie = (login.headers.get('set-cookie') ?? '').split(';')[0]!;
    const asDev = createClient({
      baseUrl: server.base,
      fetch: (input, init) => fetch(input, { ...init, headers: { ...(init?.headers as Record<string, string>), cookie } }),
    });
    const mine = await asDev.me();
    expect(mine.root).toBe(false);
    expect(mine.user?.username).toBe('role-probe-dev');
    expect(mine.user?.role).toBe('developer');
    // …and the roster is a read a developer may do, while minting people is not.
    expect((await asDev.users.list()).some((u) => u.username === 'role-probe-dev')).toBe(true);
    await expect(
      asDev.users.create({ username: 'role-probe-sneak', password: PASSWORD, role: 'admin' }),
    ).rejects.toMatchObject({ status: 403 });

    expect(await client.users.remove(dev.id)).toEqual({ deleted: dev.id });
    // …and the roster cannot be emptied — a host with no accounts is one nobody can sign in to.
    // The reason is the server's to give; this asserts the sentence it actually gave.
    const refused = await client.users.remove(viewer.id).catch((e: PstackError) => e);
    expect((refused as PstackError).status).toBe(400);
    expect((refused as PstackError).message).toContain('last user');
    expect((await client.users.list()).map((u) => u.username)).toEqual(['role-probe-viewer']);
  }, 20_000);
});

describe('0.32.0: a job can be QUEUED, and waiting is not an answer', () => {
  // `sleep 2` is the whole point: it makes the first job demonstrably still running when the next
  // two arrive, so the queue is exercised deterministically instead of by racing a fast failure.
  const SLOW = 'version: 1\nstack: client-queue\naxes:\n  - name: db\n    up: "sleep 2"\n    down: "true"\n';

  // negative control: waitForJob's `TERMINAL_JOB_STATES.includes(job.state)` -> `job.state !== 'running'`
  test('a second up queues, a third supersedes it, and waitForJob waits for every one', async () => {
    await client.deployments.put('pr-queue', { spec: SLOW });

    // Depth one, last-write-wins. None of the three is refused — that is the contract change.
    const first = await client.deployments.up('pr-queue');
    const second = await client.deployments.up('pr-queue');
    const third = await client.deployments.up('pr-queue');
    expect(first.state).toBe('running');
    expect(second.state).toBe('queued');
    expect(third.state).toBe('queued');

    // The one in the middle reaches a terminal state UNDER ITS OWN ID rather than vanishing, and
    // it never ran — so `startedAt` is null, not 0, which a UI would render as 1970.
    const dropped = await client.waitForJob(second.id, { intervalMs: 25, timeoutMs: 20_000 });
    expect(dropped.state).toBe('superseded');
    expect(dropped.startedAt).toBe(null);

    // THE REGRESSION THIS TEST EXISTS FOR: `third` is `queued` at the first poll. Returning on
    // "not running" handed that back as a finished job, telling a CI pipeline the deploy was done
    // before it had started.
    const done = await client.waitForJob(third.id, { intervalMs: 25, timeoutMs: 20_000 });
    expect(TERMINAL_JOB_STATES).toContain(done.state);
    expect(done.startedAt).toBeGreaterThan(0);
  }, 30_000);
});

describe('0.32.0: stopping everything a stack has outstanding', () => {
  const SLOW = 'version: 1\nstack: client-cancel\naxes:\n  - name: db\n    up: "sleep 30"\n    down: "true"\n';

  // negative control: routes_deploy.go cancelStack -> act on the running job only, not CancelStack
  test('cancel takes the running job AND the one queued behind it, in one call', async () => {
    await client.deployments.put('pr-cancel', { spec: SLOW });
    const running = await client.deployments.up('pr-cancel');
    const waiting = await client.deployments.up('pr-cancel');
    expect(running.state).toBe('running');
    expect(waiting.state).toBe('queued');

    const res = await client.deployments.cancel('pr-cancel');
    expect(res.stack).toBe('client-cancel');
    // BOTH, not just the one holding the slot — a queue left behind would dispatch the moment the
    // running job died, which is the opposite of "stop everything".
    expect(res.cancelled.map((j) => j.id).sort()).toEqual([running.id, waiting.id].sort());
    // The running job's warning: it had started, so something may be half-done.
    expect(res.warning).toContain('verify');

    for (const j of [running, waiting]) {
      const done = await client.waitForJob(j.id, { intervalMs: 25, timeoutMs: 20_000 });
      expect(done.state).toBe('cancelled');
    }

    // Nothing outstanding: an empty ARRAY, never null, and the warning flips — nothing ran, so
    // there is nothing to go verify.
    const again = await client.deployments.cancel('pr-cancel');
    expect(again.cancelled).toEqual([]);
    expect(again.warning).toContain('Nothing had started');
  }, 30_000);
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
