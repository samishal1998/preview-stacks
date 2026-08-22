/**
 * Build golden/host — a COMPLETE data directory as the Bun reference leaves it after real use —
 * plus the responses the reference gives over it. The Go binary must open this directory unchanged:
 * every login verifies, every session resolves, every route answers the same bytes.
 *
 *   bun gen/host-fixture.ts        (Bun reference only)
 *
 * Everything is produced BLACK-BOX through the CLI and the API, except four rows the API has no
 * write path for, which are inserted with bun:sqlite at the end (terminal audit rows, deliveries
 * with NULL columns from an older release, a user hashed with non-default argon2 cost, an SSO link
 * for a real-world provider key). FIXTURE.json records the plaintext credentials and ids a consumer
 * needs; expected/<route>.json records the reference responses.
 */
if (process.env.PSTACK_IMPL && process.env.PSTACK_IMPL !== 'bun') throw new Error('the host fixture is generated from the Bun reference only');
process.env.PSTACK_IMPL = 'bun';

const { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } = await import('node:fs');
const { join } = await import('node:path');
const { Database } = await import('bun:sqlite');
const { runCli } = await import('../harness/cli.ts');
const { bootServer, tmpd, waitJob, until } = await import('../harness/server.ts');
const { dockerShim } = await import('../harness/docker-shim.ts');
const { receiver } = await import('../harness/receiver.ts');
const { fakeProvider, rsaSigner } = await import('../harness/idp.ts');
const { GOLDEN, mask } = await import('../harness/goldens.ts');
const { INIT_SHIM } = await import('./goldens.table.ts');
const { FIXTURE_SHIM, FIXTURE_TOKEN, ROUTES, maskHost } = await import('../harness/host-fixture.ts');

const OUT = join(GOLDEN, 'host');
const version = (await runCli(['--version'])).stdout.trim();
const data = tmpd('hostfixture');
mkdirSync(data, { recursive: true });
rmSync(OUT, { recursive: true, force: true });

// ── 1. the control directory: the richest cell (dns01, advanced, swarm) ────────────────────────
{
  const shim = dockerShim(INIT_SHIM);
  const r = await runCli(
    ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--challenge', 'dns01', '--dns-provider', 'cloudflare', '--ui', 'advanced', '--orchestrator', 'swarm'],
    { env: { PSTACK_DATA: data, PSTACK_TOKEN: FIXTURE_TOKEN, PSTACK_DNS_TOKEN: 'fixture-dns-token-0123456789' }, pathPrefix: shim.dir },
  );
  shim.remove();
  if (r.code !== 0) throw new Error(`init failed:\n${r.all}`);
}

// ── 2. the server, driven like a real host ───────────────────────────────────────────────────────
const rx = receiver();
const idp = await fakeProvider();
const signer = await rsaSigner('fixture-k1');
idp.jwks = { keys: [signer.jwk] };
idp.expectSecret = 'se%cr+et/=';
const shim = dockerShim(FIXTURE_SHIM);
const s = await bootServer({
  dataDir: data,
  token: FIXTURE_TOKEN,
  keep: true,
  pathPrefix: shim.dir,
  routingDir: join(data, 'control', 'traefik-dynamic'),
  registryDir: join(data, 'control', 'docker'),
  env: { PSTACK_ADMIN_USER: 'admin', PSTACK_ADMIN_PASSWORD: 'admin-password-1', PSTACK_ORCHESTRATOR: 'swarm' },
});
const H = s.H;
const j = async (method: string, path: string, body?: unknown, headers: Record<string, string> = H) => {
  const res = await fetch(`${s.base}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  let parsed: unknown = null;
  try {
    parsed = JSON.parse(text);
  } catch {
    /* not json */
  }
  return { status: res.status, body: parsed as Record<string, unknown>, text, headers: res.headers };
};
const must = async (method: string, path: string, body?: unknown, headers?: Record<string, string>) => {
  const r = await j(method, path, body, headers);
  if (r.status >= 400) throw new Error(`${method} ${path} → ${r.status}: ${r.text}`);
  return r;
};
const fixture: Record<string, unknown> = { version, token: FIXTURE_TOKEN, admin: { username: 'admin', password: 'admin-password-1' } };

try {
  // users — admin came from the env bootstrap (createUser → hash(pw,'argon2id')); bob's second
  // password goes through setPassword (the bare hash(pw) call shape).
  const bob = (await must('POST', '/api/users', { username: 'bob', password: 'bob-password-11', email: 'bob@example.com' })).body.user as { id: number };
  await must('PUT', `/api/users/${bob.id}/password`, { password: 'bob-password-22' });
  fixture.bob = { id: bob.id, username: 'bob', password: 'bob-password-22', email: 'bob@example.com' };
  const carol = (await must('POST', '/api/users', { username: 'carol', password: 'carol-password-1' })).body.user as { id: number };
  fixture.carol = { id: carol.id, username: 'carol', password: 'carol-password-1' };

  const login = async (username: string, password: string) => {
    const r = await must('POST', '/api/auth/login', { username, password });
    return /pstack_session=([^;]+)/.exec(r.headers.get('set-cookie') ?? '')?.[1] ?? '';
  };
  const adminSession = await login('admin', 'admin-password-1');
  const bobSession = await login('bob', 'bob-password-22');
  fixture.sessions = { admin: adminSession, bob: bobSession };
  const asAdmin = { cookie: `pstack_session=${adminSession}`, 'content-type': 'application/json' };
  const asBob = { cookie: `pstack_session=${bobSession}`, 'content-type': 'application/json' };

  // personal tokens — plaintext recorded; the db holds only hashes.
  const t1 = (await must('POST', '/api/tokens', { name: 'ci-deploy' }, asAdmin)).body as { id: number; token: string };
  const t2 = (await must('POST', '/api/tokens', { name: 'laptop' }, asBob)).body as { id: number; token: string };
  fixture.tokens = { admin: t1, bob: t2 };

  // host variables and a secret
  await must('PUT', '/api/host-vars/REGION', { value: 'eu-central', secret: false });
  await must('PUT', '/api/host-vars/DB_PASS', { value: 's3cret-db-pass', secret: true });

  // a private registry credential and a Traefik dynamic file
  await must('PUT', '/api/registries/ghcr.io', { username: 'ci', password: 'ghp_fixturetoken' });
  await must('PUT', '/api/routing/extra.yml', { content: 'http:\n  middlewares:\n    compress:\n      compress: {}\n' });

  // a named spec, and four deployments in four shapes
  const SPEC = (stack: string, extra = '') =>
    `version: 1\nstack: ${stack}\ncompose:\n  file: docker-compose.yml\n  profiles: []\n${extra}axes:\n  - name: db\n    up: "true"\n    down: "true"\n    assert_gone: "true"\n`;
  const COMPOSE = 'services:\n  app:\n    image: nginx:alpine\n    labels:\n      - pstack.routing.port=80\n    restart: always\n    mem_limit: 128m\n';
  await must('PUT', '/api/specs/web', { spec: SPEC('pr-${PR}'), compose: COMPOSE, description: 'the web preview' });
  await must('PUT', '/api/deployments/pr-1', { spec: SPEC('pr-1'), compose: COMPOSE, env: { PR: '1' } });
  await must('PUT', '/api/deployments/pr-2', { specName: 'web', vars: { PR: '2' } });
  await must('PUT', '/api/deployments/shared-db', { spec: 'version: 1\nkind: shared\nstack: shared-db\ncompose:\n  file: docker-compose.yml\n', compose: 'services:\n  db:\n    image: postgres:16\n' });
  await must('PUT', '/api/deployments/sleepy', { spec: SPEC('sleepy', 'sleep:\n  after: 3d\n'), compose: COMPOSE });

  // pr-1 and sleepy go up (the shim says yes to everything), then sleepy goes to sleep: its
  // meta.json gains the sleep record with the hosts the shim's inspect answer carries.
  const up1 = (await must('POST', '/api/deployments/pr-1/up')).body.job as { id: string };
  await waitJob(s, up1.id);
  const upS = (await must('POST', '/api/deployments/sleepy/up')).body.job as { id: string };
  await waitJob(s, upS.id);
  const sl = (await must('POST', '/api/deployments/sleepy/sleep')).body.job as { id: string };
  await waitJob(s, sl.id);
  const sleepyMeta = JSON.parse(readFileSync(join(data, 'deployments', 'sleepy', 'meta.json'), 'utf8')) as { sleep?: { hosts: string[] } };
  if (!sleepyMeta.sleep?.hosts?.length) throw new Error('sleepy did not record its hosts — is the shim answering inspect?');
  fixture.sleepy = sleepyMeta.sleep;

  // notifiers: a webhook with real deliveries, a slack and a disabled discord
  const wh = (await must('POST', '/api/notifiers', { type: 'webhook', name: 'ops', config: { url: rx.url }, events: ['*'] })).body as { notifier: { id: number }; secret: string };
  const slack = (await must('POST', '/api/notifiers', { type: 'slack', name: 'chat', config: { webhookUrl: rx.url }, events: ['stack.ready', 'stack.failed'] })).body as { notifier: { id: number } };
  const discord = (await must('POST', '/api/notifiers', { type: 'discord', name: 'alerts', config: { webhookUrl: rx.url }, events: ['job.failed'] })).body as { notifier: { id: number } };
  await must('PATCH', `/api/notifiers/${discord.notifier.id}`, { enabled: false });
  await must('POST', `/api/notifiers/${wh.notifier.id}/test`);
  await must('PUT', '/api/specs/web', { spec: SPEC('pr-${PR}'), compose: COMPOSE, description: 'the web preview, revised' }); // spec.stored → a real delivery
  await rx.waitFor(2, 5000);
  fixture.notifiers = { webhook: { id: wh.notifier.id, secret: wh.secret }, slack: slack.notifier.id, discord: discord.notifier.id };

  // SSO: the Google preset as an operator would store it (captured), then the fake provider for a
  // full round trip so sso_links and a provisioned account exist.
  await must('PUT', '/api/sso/config', { mode: 'oidc', issuer: 'https://accounts.google.com', clientId: 'google-cid', clientSecret: 'google-secret', label: 'Google' }).catch(() => null);
  const google = await j('GET', '/api/sso/config');
  await must('PUT', '/api/sso/config', { mode: 'oidc', issuer: idp.base, clientId: 'cid', clientSecret: 'se%cr+et/=', label: 'Fixture IdP' });
  const now = Math.floor(Date.now() / 1000);
  idp.idToken = await signer.sign({ iss: idp.base, aud: 'cid', sub: 'fixture-sub-1', exp: now + 3600, iat: now, preferred_username: 'basil', email: 'basil@example.com', email_verified: true, name: 'Basil' });
  const start = await fetch(`${s.base}/api/auth/sso/start?next=/deployments`, { redirect: 'manual' });
  const to = new URL(start.headers.get('location')!);
  idp.expectChallenge = to.searchParams.get('code_challenge')!;
  const bounced = await fetch(to.toString(), { redirect: 'manual' });
  const cb = await fetch(bounced.headers.get('location')!, { redirect: 'manual' });
  const basilSession = /pstack_session=([^;]+)/.exec(cb.headers.get('set-cookie') ?? '')?.[1];
  if (!basilSession) throw new Error(`the SSO round trip did not mint a session: ${cb.status} ${cb.headers.get('location')}`);
  (fixture.sessions as Record<string, string>).basil = basilSession;
  fixture.sso = { issuer: idp.base, subject: 'fixture-sub-1', username: 'basil', googleExpected: google.body };

  // the wake page for the sleeping stack — captured now, because asking for it WAKES the stack
  mkdirSync(join(OUT, 'expected'), { recursive: true });
  const wake = await fetch(`${s.base}/`, { headers: { host: (fixture.sleepy as { hosts: string[] }).hosts[0]! } });
  writeFileSync(join(OUT, 'expected', 'wake-page.json'), JSON.stringify({ status: wake.status, headers: { 'x-pstack-wake': wake.headers.get('x-pstack-wake'), 'retry-after': wake.headers.get('retry-after'), 'content-type': wake.headers.get('content-type') }, body: maskHost(await wake.text(), version, data) }, null, 2) + '\n');
  await until(async () => ((await j('GET', '/api/jobs')).body.jobs as Array<{ state: string }>), (jobs) => jobs.every((x) => x.state !== 'running'), 10_000);
  // …and back to sleep, so the fixture ships with a sleeping stack
  const sl2 = (await must('POST', '/api/deployments/sleepy/sleep')).body.job as { id: string };
  await waitJob(s, sl2.id);
  fixture.sleepy = (JSON.parse(readFileSync(join(data, 'deployments', 'sleepy', 'meta.json'), 'utf8')) as { sleep: unknown }).sleep;
} finally {
  await s.stop();
  shim.remove();
  rx.stop();
  idp.stop();
}

// ── 4. the rows the API has no write path for ────────────────────────────────────────────────────
{
  const db = new Database(join(data, 'db', 'pstack.db'));
  const now = Date.now();
  db.query(`INSERT INTO terminal_sessions (actor, deployment, container, container_id, shell, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?)`).run('admin', 'pr-1', 'pr-1-app-1', 'c0ffee123456', 'sh', now - 60_000, now - 30_000);
  db.query(`INSERT INTO terminal_sessions (actor, deployment, container, container_id, shell, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?)`).run('root (PSTACK_TOKEN)', 'pr-1', 'pr-1-app-1', 'c0ffee123456', 'bash', now - 20_000, null);
  // deliveries as a pre-0.25.0 release left them: no stored payload, no response code, no error
  const whId = (fixture.notifiers as { webhook: { id: number } }).webhook.id;
  db.query(`INSERT INTO deliveries (notifier_id, event_id, event, status, attempts, response_code, error, created_at, updated_at, payload) VALUES (?, ?, ?, 'ok', 1, NULL, NULL, ?, ?, NULL)`).run(whId, 'evt_legacy_1', 'stack.ready', now - 90_000, now - 90_000);
  db.query(`INSERT INTO deliveries (notifier_id, event_id, event, status, attempts, response_code, error, created_at, updated_at, payload) VALUES (?, ?, ?, 'failed', 3, 503, 'x', ?, ?, NULL)`).run(whId, 'evt_legacy_2', 'stack.failed', now - 80_000, now - 80_000);
  // a user whose hash carries non-default cost: a verifier that hardcodes m/t/p locks them out
  const costly = await Bun.password.hash('dave-password-1', { algorithm: 'argon2id', memoryCost: 19456, timeCost: 3 });
  const dave = db.query(`INSERT INTO users (username, password_hash, role, email, created_at) VALUES ('dave', ?, 'admin', 'dave@example.com', ?) RETURNING id`).get(costly, now) as { id: number };
  fixture.dave = { id: dave.id, username: 'dave', password: 'dave-password-1', hash: costly };
  // an SSO link under a real provider key, for bob
  db.query(`INSERT INTO sso_links (provider_key, subject, user_id, created_at, last_login_at) VALUES ('https://accounts.google.com', '1182734587234', ?, ?, ?)`).run((fixture.bob as { id: number }).id, now - 1000, now - 1000);
  db.close();
}

// ── 5. an unknown meta.json field, which must survive a sleep/wake rewrite ───────────────────────
{
  const p = join(data, 'deployments', 'pr-2', 'meta.json');
  const meta = JSON.parse(readFileSync(p, 'utf8')) as Record<string, unknown>;
  meta['x-future'] = { k: 1 };
  writeFileSync(p, JSON.stringify(meta, null, 2));
}

// ── 6. the reference responses, from a FRESH process over the finished directory ─────────────────
// A fresh boot is what a consumer does too: job history is gone, no readiness watch exists, and the
// rows inserted above are visible. Everything recorded here is what any implementation must answer.
{
  const shim2 = dockerShim(FIXTURE_SHIM);
  const s2 = await bootServer({ dataDir: data, token: FIXTURE_TOKEN, keep: true, pathPrefix: shim2.dir, routingDir: join(data, 'control', 'traefik-dynamic'), registryDir: join(data, 'control', 'docker'), env: { PSTACK_ORCHESTRATOR: 'swarm' } });
  try {
    const sessions = fixture.sessions as Record<string, string>;
    for (const route of ROUTES) {
      const headers = route.as ? { cookie: `pstack_session=${sessions[route.as]}` } : s2.H;
      const res = await fetch(`${s2.base}${route.path}`, { headers });
      const text = await res.text();
      writeFileSync(join(OUT, 'expected', `${route.name}.json`), JSON.stringify({ path: route.path, as: route.as ?? 'token', status: res.status, body: JSON.parse(maskHost(text, version, data, route.volatile)) }, null, 2) + '\n');
    }
  } finally {
    await s2.stop();
    shim2.remove();
  }
}

// ── 7. ship it ───────────────────────────────────────────────────────────────────────────────────
cpSync(data, OUT, { recursive: true });
rmSync(join(OUT, 'db', 'pstack.db-wal'), { force: true });
rmSync(join(OUT, 'db', 'pstack.db-shm'), { force: true });
// the rendered control files carry the fixture's tmp path — mask it like every other golden
for (const f of ['control/.env', 'control/docker-compose.yml']) {
  const p = join(OUT, f);
  writeFileSync(p, mask(readFileSync(p, 'utf8').split(data).join('<DATA>'), version));
}
fixture.shim = FIXTURE_SHIM;
writeFileSync(join(OUT, 'FIXTURE.json'), JSON.stringify(fixture, null, 2) + '\n');
rmSync(data, { recursive: true, force: true });
console.log(`wrote golden/host (${ROUTES.length} expected routes)`);

export {};
