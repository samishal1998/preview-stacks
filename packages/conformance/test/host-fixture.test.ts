/**
 * The golden host: a complete data directory the Bun reference produced, which ANY implementation
 * must open unchanged — no migration, no data loss, every credential still good.
 *
 * golden/host is copied to a tmp dir, the selected implementation is booted over it with the same
 * docker the fixture "had", and then: every stored credential signs in (four argon2 call shapes,
 * two live sessions, two personal tokens), every recorded route answers the reference bytes, the
 * sleeping stack still wakes on a request to its hostname, and a field pstack never heard of
 * survives a meta.json rewrite.
 */
import { describe, expect, test } from 'bun:test';
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { IMPL } from '../harness/impl.ts';
import { bootServer, tmpd, until, waitJob, type Booted } from '../harness/server.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { runCli } from '../harness/cli.ts';
import { GOLDEN } from '../harness/goldens.ts';
import { FIXTURE_TOKEN, ROUTES, maskHost } from '../harness/host-fixture.ts';

type Fixture = {
  version: string;
  token: string;
  admin: { username: string; password: string };
  bob: { id: number; username: string; password: string; email: string };
  carol: { username: string; password: string };
  dave: { username: string; password: string; hash: string };
  sessions: { admin: string; bob: string; basil: string };
  tokens: { admin: { id: number; token: string }; bob: { id: number; token: string } };
  notifiers: { webhook: { id: number; secret: string }; slack: number; discord: number };
  sleepy: { since: number; hosts: string[] };
  sso: { issuer: string; subject: string; username: string };
  shim: string;
};

const HOST = join(GOLDEN, 'host');

describe('the golden host opens unchanged', () => {
  const fixture = JSON.parse(readFileSync(join(HOST, 'FIXTURE.json'), 'utf8')) as Fixture;
  let version = '';
  let s: Booted;
  let data = '';
  let shim: ReturnType<typeof dockerShim>;

  const boot = async () => {
    data = tmpd('goldenhost');
    mkdirSync(data, { recursive: true });
    cpSync(HOST, data, { recursive: true });
    rmSync(join(data, 'expected'), { recursive: true, force: true });
    rmSync(join(data, 'FIXTURE.json'), { force: true });
    shim = dockerShim(fixture.shim);
    s = await bootServer({
      dataDir: data,
      token: FIXTURE_TOKEN,
      pathPrefix: shim.dir,
      routingDir: join(data, 'control', 'traefik-dynamic'),
      registryDir: join(data, 'control', 'docker'),
      env: { PSTACK_ORCHESTRATOR: 'swarm' },
    });
  };
  const stop = async () => {
    await s.stop();
    shim.remove();
  };

  test('boots over the fixture directory', async () => {
    version = IMPL === 'null' ? fixture.version : (await runCli(['--version'])).stdout.trim();
    await boot();
    const h = (await (await fetch(`${s.base}/api/health`)).json()) as { ok: boolean; hasUsers: boolean; sso: { label: string } | null };
    expect(h.ok).toBe(true);
    expect(h.hasUsers).toBe(true);
    expect(h.sso?.label).toBe('Fixture IdP');
  }, 20_000);

  test('every stored password still verifies — both Bun call shapes, and a non-default cost', async () => {
    // admin: createUser → hash(pw,'argon2id'); bob: setPassword → bare hash(pw); carol: createUser;
    // dave: inserted with memoryCost 19456 / timeCost 3. A verifier that ASSUMES m/t/p instead of
    // parsing them passes three of these and locks dave out — the fourth is the point.
    for (const u of [fixture.admin, fixture.bob, fixture.carol, fixture.dave]) {
      const r = await fetch(`${s.base}/api/auth/login`, { method: 'POST', headers: s.H, body: JSON.stringify({ username: u.username, password: u.password }) });
      expect(`${u.username}:${r.status}`).toBe(`${u.username}:200`);
      const wrong = await fetch(`${s.base}/api/auth/login`, { method: 'POST', headers: s.H, body: JSON.stringify({ username: u.username, password: `${u.password}-wrong` }) });
      expect(wrong.status).toBe(400);
    }
    expect(fixture.dave.hash).toMatch(/^\$argon2id\$v=19\$m=19456,t=3,p=1\$/);
  });

  test('the sessions and personal tokens minted by the reference still authenticate', async () => {
    for (const [who, cookie] of Object.entries(fixture.sessions)) {
      const me = await fetch(`${s.base}/api/auth/me`, { headers: { cookie: `pstack_session=${cookie}` } });
      expect(`${who}:${me.status}`).toBe(`${who}:200`);
      const body = (await me.json()) as { user: { username: string } };
      expect(body.user.username).toBe(who);
    }
    for (const [who, t] of Object.entries(fixture.tokens)) {
      const me = await fetch(`${s.base}/api/auth/me`, { headers: { authorization: `Bearer ${t.token}` } });
      expect(`${who}:${me.status}`).toBe(`${who}:200`);
    }
    // A session nobody minted does not.
    const bogus = await fetch(`${s.base}/api/auth/me`, { headers: { cookie: 'pstack_session=pstack_ses_' + '0'.repeat(64) } });
    expect(bogus.status).toBe(401);
  });

  for (const route of ROUTES) {
    test(`GET ${route.path}${route.as ? ` as ${route.as}` : ''} answers the reference bytes`, async () => {
      const expected = JSON.parse(readFileSync(join(HOST, 'expected', `${route.name}.json`), 'utf8')) as { status: number; body: unknown };
      const headers = route.as ? { cookie: `pstack_session=${fixture.sessions[route.as]}` } : s.H;
      const res = await fetch(`${s.base}${route.path}`, { headers });
      const text = await res.text();
      expect(res.status).toBe(expected.status);
      expect(JSON.parse(maskHost(text, version, data, route.volatile))).toEqual(expected.body);
    });
  }

  test('the SSO link survives: the same subject signs in as the same account', async () => {
    // sso_links keyed on (providerKey, subject) is what makes a re-login find basil instead of
    // creating basil-2. Observable without the provider: the users list has exactly one basil and
    // the stored config still names the fixture issuer.
    const users = (await (await fetch(`${s.base}/api/users`, { headers: s.H })).json()) as { users: Array<{ username: string; email: string | null }> };
    expect(users.users.filter((u) => u.username.startsWith('basil'))).toEqual([{ username: 'basil', email: 'basil@example.com' }].map((u) => expect.objectContaining(u)));
    const cfg = (await (await fetch(`${s.base}/api/sso/config`, { headers: s.H })).json()) as { config: { discoveryUrl: string }; clientSecret: string };
    expect(cfg.config.discoveryUrl.replace(/:\d+\/?$/, '')).toBe(fixture.sso.issuer.replace(/:\d+$/, ''));
    expect(cfg.clientSecret).toBe('••••••••');
  });

  test('a delivery row from an older release (NULL payload) lists and refuses redelivery by name', async () => {
    const id = fixture.notifiers.webhook.id;
    const list = (await (await fetch(`${s.base}/api/notifiers/${id}/deliveries`, { headers: s.H })).json()) as { deliveries: Array<{ eventId: string; replayable?: boolean; error: string | null; responseCode: number | null }> };
    const legacy = list.deliveries.find((d) => d.eventId === 'evt_legacy_1');
    expect(legacy).toBeDefined();
    expect(legacy!.replayable).toBe(false);
    expect(legacy!.responseCode).toBeNull();
    expect(legacy!.error).toBeNull();
    const modern = list.deliveries.find((d) => d.eventId !== 'evt_legacy_1' && d.eventId !== 'evt_legacy_2');
    expect(modern?.replayable).toBe(true);
  });

  test('a request to the sleeping stack\'s hostname wakes it — the record written by the reference is honoured', async () => {
    const r = await fetch(`${s.base}/`, { headers: { host: fixture.sleepy.hosts[0]! } });
    expect(r.status).toBe(503);
    expect(r.headers.get('x-pstack-wake')).toBe('1');
    expect(r.headers.get('retry-after')).toBe('5');
    expect(await r.text()).toContain('sleepy');
    const jobs = await until(
      async () => ((await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: Array<{ id: string; action: string; state: string }> }).jobs,
      (js) => js.some((j) => j.action === 'wake' && j.state !== 'running'),
      10_000,
    );
    const wake = jobs.find((j) => j.action === 'wake')!;
    expect(wake.state).toBe('ok');
    // Awake now: the record is gone, the list says so.
    const dep = (await (await fetch(`${s.base}/api/deployments/sleepy`, { headers: s.H })).json()) as { asleep: unknown };
    expect(dep.asleep).toBeNull();
  }, 20_000);

  test('a meta.json field pstack never heard of survives a sleep', async () => {
    // pr-2 carries `x-future` — written by some other version. A struct without a catch-all drops
    // it on the next rewrite, which is the sleep record.
    expect((JSON.parse(readFileSync(join(data, 'deployments', 'pr-2', 'meta.json'), 'utf8')) as Record<string, unknown>)['x-future']).toEqual({ k: 1 });
    const up = (await (await fetch(`${s.base}/api/deployments/pr-2/up?PR=2`, { method: 'POST', headers: s.H })).json()) as { job: { id: string } };
    await waitJob(s, up.job.id);
    const sl = (await (await fetch(`${s.base}/api/deployments/pr-2/sleep?PR=2`, { method: 'POST', headers: s.H })).json()) as { job: { id: string } };
    await waitJob(s, sl.job.id);
    const meta = JSON.parse(readFileSync(join(data, 'deployments', 'pr-2', 'meta.json'), 'utf8')) as Record<string, unknown>;
    expect(meta['x-future']).toEqual({ k: 1 });
    expect(meta.sleep).toBeDefined();
    expect(meta.specName).toBe('web');
  }, 20_000);

  test('the control directory reads back exactly — what upgrade would act on', async () => {
    // dns01 + advanced + swarm, with the credential present: the one cell where every field matters.
    expect(existsSync(join(data, 'control', 'dns.env'))).toBe(true);
    expect(readFileSync(join(data, 'control', 'dns.env'), 'utf8')).toContain('CF_DNS_API_TOKEN=fixture-dns-token-0123456789');
    const r = await runCli(['upgrade', '-n', '--to', '0.28.1'], { env: { PSTACK_DATA: data } });
    expect(r.code).toBe(0);
    expect(r.stdout).toContain("--challenge dns01 --dns-provider 'cloudflare' --ui advanced --orchestrator swarm (with the existing PSTACK_TOKEN and PSTACK_DNS_TOKEN)");
    await stop();
  }, 20_000);
});
