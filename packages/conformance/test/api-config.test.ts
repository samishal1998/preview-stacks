/**
 * `GET/POST /api/config` and `pstack pull/push config` — the portability proof, black-box.
 *
 * The claim this feature makes is not "an endpoint returns JSON". It is: **a host's configuration
 * can be moved to a different machine and that machine then behaves like the first one.** Nothing
 * short of two real servers proves that, so the first test boots two, seals an export from A with
 * the real CLI, unseals it onto an empty B, and then asks B the questions an operator would ask —
 * can my people still log in, are my variables here, does this host know my registry.
 *
 * The other four tests each cover a way this feature could betray someone rather than merely fail:
 *
 *   - a browser session (or an admin's personal token) must NOT be able to export. A full credential
 *     dump reachable from a cookie means one XSS exfiltrates the host. Both verbs, because POST is
 *     the half that repoints where this host pulls images from.
 *   - a wrong passphrase must fail cleanly and write NOTHING to the target.
 *   - apply must be idempotent, because the recovery procedure for a half-applied import is to run
 *     it again.
 *   - the export must carry no deployments, sessions or delivery history. Restoring those into a
 *     different host is wrong, not merely useless — and the load-bearing assertion for that is the
 *     document's key set, not a list of strings that happen to be absent.
 *
 * Two harness facts shape everything below. `cleanEnv()` strips every `PSTACK_*` from the child, so
 * the three variables travel explicitly. And `runCli` gives the child no tty, so `push` needs `-y`
 * and prints a COUNT of what it would trust rather than the list — that is the design (a
 * cloud-init log is not a terminal), and it is asserted rather than worked around.
 *
 * The timeouts are generous on purpose: sealing is scrypt, ~1s per derivation by design, and these
 * tests do several plus two server boots. A 5s default would read as a flake.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync, rmSync, chmodSync, statSync } from 'node:fs';
import { bootServer, tmpd, until, waitJob, type Booted } from '../harness/server.ts';
import { runCli } from '../harness/cli.ts';

/** Every key the document has. A new one carrying ephemeral state has to be added HERE, on purpose. */
const DOC_KEYS = [
  'version',
  'pstackVersion',
  'exportedAt',
  'skipped',
  'users',
  'tokens',
  'vars',
  'notifiers',
  'sso',
  'registries',
  'routing',
  'specs',
].sort();

const PASS = 'correct-horse-battery-staple';
const KEY = 'a-passphrase-with spaces and $ig(ns)';
const DB_SECRET = 'hunter2-swordfish-42';
const REG_PASSWORD = 'ghp_registry_password_9000';

const SPEC = [
  'version: 1',
  'stack: web-${PR}',
  'axes:',
  '  - name: db',
  '    up: "true"',
  '    assert_gone: "true"',
  '',
].join('\n');

const ROUTING = 'http:\n  middlewares:\n    dashboard-auth:\n      basicAuth:\n        users:\n          - "admin:$apr1$x"\n';

type Host = Booted & { registryDir: string; routingDir: string };

/** A server with its OWN docker config and routing dir — never the developer's ~/.docker. */
async function host(tag: string): Promise<Host> {
  const registryDir = tmpd(`${tag}-reg`);
  const routingDir = tmpd(`${tag}-routing`);
  mkdirSync(registryDir, { recursive: true });
  mkdirSync(routingDir, { recursive: true });
  const s = await bootServer({ tag, registryDir, routingDir });
  const stop = s.stop;
  return {
    ...s,
    registryDir,
    routingDir,
    stop: async () => {
      await stop();
      rmSync(registryDir, { recursive: true, force: true });
      rmSync(routingDir, { recursive: true, force: true });
    },
  };
}

/** The state an operator would actually miss: an account, a var, a secret, a notifier, a registry, a route, a spec. */
async function seed(s: Booted): Promise<void> {
  const ok = async (r: Promise<Response>, want: number[]) => {
    const res = await r;
    expect(`${res.url} → ${res.status}`).toBe(`${res.url} → ${want.find((w) => w === res.status) ?? want[0]}`);
    return res;
  };
  await ok(
    fetch(`${s.base}/api/auth/bootstrap`, { method: 'POST', headers: s.H, body: JSON.stringify({ username: 'alice', password: PASS }) }),
    [201],
  );
  await ok(fetch(`${s.base}/api/host-vars/REGION`, { method: 'PUT', headers: s.H, body: JSON.stringify({ value: 'eu-central', secret: false }) }), [201]);
  await ok(fetch(`${s.base}/api/host-vars/DB_PASSWORD`, { method: 'PUT', headers: s.H, body: JSON.stringify({ value: DB_SECRET, secret: true }) }), [201]);
  await ok(
    fetch(`${s.base}/api/registries/ghcr.io`, { method: 'PUT', headers: s.H, body: JSON.stringify({ username: 'bot', password: REG_PASSWORD }) }),
    [200],
  );
  await ok(fetch(`${s.base}/api/routing/auth.yml`, { method: 'PUT', headers: s.H, body: JSON.stringify({ content: ROUTING }) }), [201]);
  await ok(fetch(`${s.base}/api/specs/web`, { method: 'PUT', headers: s.H, body: JSON.stringify({ spec: SPEC, description: 'the web app' }) }), [201]);
  await ok(
    fetch(`${s.base}/api/notifiers`, {
      method: 'POST',
      headers: s.H,
      body: JSON.stringify({ name: 'ops', type: 'slack', events: ['*'], config: { webhookUrl: 'https://hooks.slack.com/services/T0/B0/zzzzzzzz' } }),
    }),
    [201],
  );
}

type Doc = {
  version: number;
  users: Array<{ username: string; passwordHash?: string }>;
  vars: Array<{ name: string; value: string; secret: boolean }>;
  notifiers: Array<{ name: string; type: string; secret: string; config: Record<string, string> }>;
  registries: Array<{ registry: string; username: string; password: string }>;
  routing: Array<{ name: string; content: string }>;
  specs: Array<{ name: string; source: string }>;
  sso: unknown;
};

const exportDoc = async (s: Booted): Promise<Doc> => {
  const r = await fetch(`${s.base}/api/config`, { headers: s.H });
  expect(r.status).toBe(200);
  return (await r.json()) as Doc;
};

const cliEnv = (s: Booted, key = KEY) => ({
  PSTACK_API_URL: s.base,
  PSTACK_TOKEN: s.token ?? '',
  PSTACK_CONFIG_KEY: key,
});

describe('moving a host configuration — pull, seal, push, and the new host answers like the old one', () => {
  test('a second, empty host adopts the first host\'s accounts, secrets and registries', async () => {
    const a = await host('cfg-a');
    const b = await host('cfg-b');
    const file = `${tmpd('cfg-file')}.sealed`;
    try {
      await seed(a);

      // B is EMPTY first — without this the assertions below could pass for the wrong reason.
      const before = await exportDoc(b);
      expect({ users: before.users.length, vars: before.vars.length, registries: before.registries.length, specs: before.specs.length }).toEqual({
        users: 0,
        vars: 0,
        registries: 0,
        specs: 0,
      });
      // …and nobody can log in on it.
      expect((await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) })).status).not.toBe(200);

      // ── seal, on the operator's machine, with the real CLI ────────────────────────────────────
      const pull = await runCli(['pull', 'config', '-o', file], { env: cliEnv(a) });
      expect(`${pull.code}: ${pull.all}`).toBe(`0: ${pull.all}`);
      // The file is sealed and 0600 — it is every credential on host A.
      const sealed = await Bun.file(file).text();
      expect(sealed).not.toContain(DB_SECRET);
      expect(sealed).not.toContain(REG_PASSWORD);
      expect(sealed).not.toContain('hooks.slack.com');
      expect((statSync(file).mode & 0o777).toString(8)).toBe('600');
      const envelope = JSON.parse(sealed) as { version: number; sealed: string; kdf: unknown; nonce: string; payload: string };
      expect(envelope.version).toBe(1);
      expect(envelope.sealed).toBe('scrypt-aes256gcm');

      // ── unseal onto B ────────────────────────────────────────────────────────────────────────
      const push = await runCli(['push', 'config', '-i', file, '-y'], { env: cliEnv(b) });
      expect(`${push.code}: ${push.all}`).toBe(`0: ${push.all}`);
      // Under -y into a pipe the trust list is a COUNT, not the URLs — a log is not a terminal.
      expect(push.stderr).toMatch(/would make this host trust \d+ registries and notifier URLs/);
      expect(push.all).not.toContain(REG_PASSWORD);
      expect(push.all).not.toContain('hooks.slack.com');
      expect(push.stdout).toContain('created  ');

      // ── B now answers like A ─────────────────────────────────────────────────────────────────
      // 1. The people. This is the only check that proves an argon2 hash moved in a form the auth
      //    path actually accepts — every other field could be carried and still be unusable.
      const login = await fetch(`${b.base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'alice', password: PASS }),
      });
      expect(login.status).toBe(200);
      expect(login.headers.get('set-cookie')).toContain('pstack_session=');

      // 2. The variables, over the routes that serve them — the value for a variable, and for a
      //    secret the fact that it is here and still write-only.
      const vars = (await (await fetch(`${b.base}/api/host-vars`, { headers: b.H })).json()) as {
        entries: Array<{ name: string; value: string | null; secret: boolean }>;
      };
      expect(vars.entries.find((e) => e.name === 'REGION')?.value).toBe('eu-central');
      const carried = vars.entries.find((e) => e.name === 'DB_PASSWORD');
      expect({ secret: carried?.secret, value: carried?.value }).toEqual({ secret: true, value: null });

      // 2b. Vars RESOLVE, which is a different claim from "the bytes arrived": a spec on B has to
      //     interpolate ${vars.*} and ${secrets.*} out of rows that no operator ever set here. The
      //     secret's value is scrubbed out of the job record by design, and the scrub marker is the
      //     evidence — nothing to mask would mean nothing resolved.
      const probe = [
        'version: 1',
        'stack: cfg-resolve',
        'env:',
        '  TOKEN_FOR_APP: ${secrets.DB_PASSWORD}',
        'axes:',
        '  - name: a',
        '    up: echo region is ${vars.REGION} and token is $TOKEN_FOR_APP 1>&2; exit 1',
        '    assert_gone: "true"',
      ].join('\n');
      expect((await fetch(`${b.base}/api/deployments/probe`, { method: 'PUT', headers: b.H, body: JSON.stringify({ spec: probe }) })).status).toBe(201);
      const started = await fetch(`${b.base}/api/deployments/probe/up`, { method: 'POST', headers: b.H, body: '{}' });
      expect(started.status).toBe(202);
      const jobId = ((await started.json()) as { job: { id: string } }).job.id;
      await waitJob(b, jobId, 20_000);
      const jobRaw = await (await fetch(`${b.base}/api/jobs/${jobId}`, { headers: b.H })).text();
      expect(jobRaw).toContain('eu-central');
      expect(jobRaw).not.toContain(DB_SECRET);
      expect(jobRaw).toContain('••••••');

      // 3. The registry, the route file, the spec and the notifier, each over its own route.
      const regs = (await (await fetch(`${b.base}/api/registries`, { headers: b.H })).json()) as {
        entries: Array<{ registry: string; username: string; viaHelper: boolean }>;
      };
      expect(regs.entries).toEqual([{ registry: 'ghcr.io', username: 'bot', viaHelper: false }]);
      expect(((await (await fetch(`${b.base}/api/routing/auth.yml`, { headers: b.H })).json()) as { content: string }).content).toBe(ROUTING);
      expect(((await (await fetch(`${b.base}/api/specs/web`, { headers: b.H })).json()) as { source: string }).source).toBe(SPEC);
      const hooks = (await (await fetch(`${b.base}/api/notifiers`, { headers: b.H })).json()) as { notifiers: Array<{ name: string; type: string }> };
      expect(hooks.notifiers.map((n) => n.name)).toContain('ops');

      // 4. The SECRET VALUES, which no read route will ever show. B's own export must carry the
      //    same bytes A's did: the host secret, the registry password, the notifier signing secret
      //    and its webhook URL. Selected fields, not whole documents — exportedAt differs.
      const from = await exportDoc(a);
      const to = await exportDoc(b);
      const creds = (d: Doc) => ({
        vars: d.vars.map((v) => [v.name, v.value, v.secret]).sort(),
        registries: d.registries.map((r) => [r.registry, r.username, r.password]).sort(),
        notifiers: d.notifiers.map((n) => [n.name, n.type, n.secret, JSON.stringify(n.config)]).sort(),
        users: d.users.map((u) => [u.username, u.passwordHash]).sort(),
        routing: d.routing.map((r) => [r.name, r.content]).sort(),
        specs: d.specs.map((sp) => [sp.name, sp.source]).sort(),
      });
      expect(creds(to)).toEqual(creds(from));
      // Belt and braces: the values really are the ones the test put in, not two matching blanks.
      expect(to.vars.find((v) => v.name === 'DB_PASSWORD')?.value).toBe(DB_SECRET);
      expect(to.registries[0]?.password).toBe(REG_PASSWORD);
    } finally {
      rmSync(file, { force: true });
      await b.stop();
      await a.stop();
    }
    // negative control: in internal/config/config.go, make applyUsers skip the INSERT (return nil
    // right after the duplicate check) — alice can no longer log in on B. Run.
  }, 120_000);

  test('a browser session and a personal token are both refused — GET and POST', async () => {
    const a = await host('cfg-gate');
    try {
      await seed(a);
      const login = await fetch(`${a.base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'alice', password: PASS }),
      });
      expect(login.status).toBe(200);
      const cookie = login.headers.get('set-cookie')!.split(';')[0]!;

      // The positive control FIRST: this really is a working admin session. A 403 from a server
      // that 403s everything would prove nothing.
      expect((await fetch(`${a.base}/api/auth/me`, { headers: { cookie } })).status).toBe(200);
      expect((await fetch(`${a.base}/api/deployments`, { headers: { cookie } })).status).toBe(200);
      expect((await fetch(`${a.base}/api/host-vars`, { headers: { cookie } })).status).toBe(200);

      // The same admin's personal API token — the credential an operator would actually reach for.
      const minted = await fetch(`${a.base}/api/tokens`, { method: 'POST', headers: { cookie, 'content-type': 'application/json' }, body: JSON.stringify({ name: 'ci' }) });
      expect(minted.status).toBe(201);
      const pat = ((await minted.json()) as { token: string }).token;
      expect((await fetch(`${a.base}/api/deployments`, { headers: { authorization: `Bearer ${pat}` } })).status).toBe(200);

      // Neither may move the configuration, in either direction, and the refusal carries nothing.
      const asAnyone: Array<[string, Record<string, string>]> = [
        ['session cookie', { cookie }],
        ['personal token', { authorization: `Bearer ${pat}` }],
      ];
      for (const [who, headers] of asAnyone) {
        for (const method of ['GET', 'POST']) {
          const r = await fetch(`${a.base}/api/config`, {
            method,
            headers: { ...headers, 'content-type': 'application/json' },
            body: method === 'POST' ? JSON.stringify({ version: 1, registries: [{ registry: 'evil.example', username: 'm', password: 'p' }] }) : undefined,
          });
          expect(`${who} ${method} → ${r.status}`).toBe(`${who} ${method} → 403`);
          const raw = await r.text();
          expect(raw).not.toContain(DB_SECRET);
          expect(raw).not.toContain(REG_PASSWORD);
          expect(raw).not.toContain('argon2');
        }
      }
      // The refused POST changed nothing.
      const doc = await exportDoc(a);
      expect(doc.registries.map((r) => r.registry)).toEqual(['ghcr.io']);
      // Anonymous is 401, not 403 — it never reaches the gate.
      expect((await fetch(`${a.base}/api/config`)).status).toBe(401);
    } finally {
      await a.stop();
    }
    // negative control: in internal/api/routes_config.go widen mayMoveConfig to routes.go:112's
    // form — `who.Kind == auth.KindRoot || (who.Kind == auth.KindUser && who.User.Role == "admin")`.
    // The cookie GET then returns 200. Run.
  }, 60_000);

  test('a wrong passphrase fails cleanly and writes nothing to the target', async () => {
    const a = await host('cfg-pass-a');
    const b = await host('cfg-pass-b');
    const file = `${tmpd('cfg-pass-file')}.sealed`;
    try {
      await seed(a);
      const pull = await runCli(['pull', 'config', '-o', file], { env: cliEnv(a) });
      expect(`${pull.code}: ${pull.all}`).toBe(`0: ${pull.all}`);

      const push = await runCli(['push', 'config', '-i', file, '-y'], { env: cliEnv(b, 'not-the-passphrase') });
      expect(push.code).not.toBe(0);
      // One error, and it says so in words an operator can act on. GCM cannot distinguish a wrong
      // passphrase from a tampered file, so the message must name both possibilities.
      expect(push.stderr.toLowerCase()).toMatch(/passphrase/);
      // It failed BEFORE writing anything — not halfway.
      expect(push.stdout).not.toContain('created  ');
      const after = await exportDoc(b);
      expect({ users: after.users.length, vars: after.vars.length, registries: after.registries.length }).toEqual({ users: 0, vars: 0, registries: 0 });
      expect((await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) })).status).not.toBe(200);

      // A single flipped byte in the payload is the same refusal — the seal is authenticated, not
      // merely encrypted, so a truncated or edited export cannot be half-applied.
      const env = JSON.parse(await Bun.file(file).text()) as { payload: string };
      const bytes = Buffer.from(env.payload, 'base64');
      bytes[Math.floor(bytes.length / 2)]! ^= 0xff;
      const tampered = `${file}.tampered`;
      await Bun.write(tampered, JSON.stringify({ ...env, payload: bytes.toString('base64') }));
      chmodSync(tampered, 0o600);
      const bad = await runCli(['push', 'config', '-i', tampered, '-y'], { env: cliEnv(b) });
      expect(bad.code).not.toBe(0);
      expect((await exportDoc(b)).users.length).toBe(0);
      rmSync(tampered, { force: true });
    } finally {
      rmSync(file, { force: true });
      await b.stop();
      await a.stop();
    }
    // negative control: in internal/config/seal.go make Unseal ignore the AEAD failure (return the
    // raw ciphertext on error) — the wrong-passphrase push then exits non-zero for a different
    // reason, and the tampered push stops being refused. Run.
  }, 120_000);

  test('applying the same document twice creates nothing the second time', async () => {
    const a = await host('cfg-idem-a');
    const b = await host('cfg-idem-b');
    const file = `${tmpd('cfg-idem-file')}.sealed`;
    try {
      await seed(a);
      expect((await runCli(['pull', 'config', '-o', file], { env: cliEnv(a) })).code).toBe(0);

      const first = await runCli(['push', 'config', '-i', file, '-y'], { env: cliEnv(b) });
      expect(`${first.code}: ${first.all}`).toBe(`0: ${first.all}`);
      const createdFirst = first.stdout.split('\n').filter((l) => l.startsWith('created  '));
      expect(createdFirst.length).toBeGreaterThan(0);
      const afterFirst = await exportDoc(b);

      const second = await runCli(['push', 'config', '-i', file, '-y'], { env: cliEnv(b) });
      expect(`${second.code}: ${second.all}`).toBe(`0: ${second.all}`);
      expect(second.stdout.split('\n').filter((l) => l.startsWith('created  '))).toEqual([]);
      expect(second.stdout.split('\n').filter((l) => l.startsWith('skipped  ')).length).toBe(createdFirst.length);

      // Idempotent means the HOST is unchanged, not just that the output said so.
      const afterSecond = await exportDoc(b);
      const stable = (d: Doc) => JSON.stringify({ ...d, exportedAt: 0 });
      expect(stable(afterSecond)).toBe(stable(afterFirst));
      // And the account still works — a second apply must not have rewritten the hash.
      expect(
        (await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) })).status,
      ).toBe(200);
    } finally {
      rmSync(file, { force: true });
      await b.stop();
      await a.stop();
    }
    // negative control: in internal/config/config.go make applyNotifiers insert unconditionally
    // (drop the existing-name check) — the second push creates a duplicate `ops`. Run.
  }, 120_000);

  test('the export carries no deployments, no sessions and no delivery history', async () => {
    const a = await host('cfg-ephemeral');
    try {
      await seed(a);

      // Make all three exist, so their absence is a decision and not an empty host.
      const dep = await fetch(`${a.base}/api/deployments/pr-9`, { method: 'PUT', headers: a.H, body: JSON.stringify({ spec: SPEC, env: { PR: '9' } }) });
      expect(dep.status).toBe(201);
      expect(((await (await fetch(`${a.base}/api/deployments`, { headers: a.H })).json()) as { deployments: unknown[] }).deployments.length).toBe(1);

      const login = await fetch(`${a.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) });
      const sessionValue = /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';
      expect(sessionValue.length).toBeGreaterThan(10);

      // A delivery attempt against a dead port: the attempt is recorded either way, which is the
      // history that must not travel.
      const tap = await fetch(`${a.base}/api/notifiers`, {
        method: 'POST',
        headers: a.H,
        body: JSON.stringify({ name: 'tap', type: 'webhook', events: ['*'], config: { url: 'http://127.0.0.1:1/hook' } }),
      });
      expect(tap.status).toBe(201);
      const tapId = ((await tap.json()) as { notifier: { id: number } }).notifier.id;
      expect((await fetch(`${a.base}/api/notifiers/${tapId}/test`, { method: 'POST', headers: a.H, body: '{}' })).status).toBeLessThan(300);
      const history = await until(
        async () => ((await (await fetch(`${a.base}/api/notifiers/${tapId}/deliveries`, { headers: a.H })).json()) as { deliveries: Array<{ id: number }> }).deliveries,
        (d) => d.length > 0,
        10_000,
      );
      // The positive control: there IS delivery history on this host to leave behind.
      expect(history.length).toBeGreaterThan(0);

      const r = await fetch(`${a.base}/api/config`, { headers: a.H });
      expect(r.status).toBe(200);
      // A cached credential dump is the one caching mistake in this codebase that cannot be undone.
      expect(r.headers.get('cache-control')).toBe('no-store');
      const raw = await r.text();
      const doc = JSON.parse(raw) as Doc;

      // THE assertion: the document's shape. A future field that carries ephemeral state has to be
      // added to DOC_KEYS deliberately, by someone who reads this test's name.
      expect(Object.keys(doc).sort()).toEqual(DOC_KEYS);
      expect(doc.version).toBe(1);

      // …and, riding along, that the things which do exist on this host are really not in it.
      expect(raw).not.toContain('pr-9');
      expect(raw).not.toContain(sessionValue);
      expect(raw).not.toContain('"deliveries"');
      // The state that DOES travel is here, so the absences above are not an empty document.
      expect(doc.users.map((u) => u.username)).toEqual(['alice']);
      expect(doc.registries.map((rg) => rg.registry)).toEqual(['ghcr.io']);
      expect(doc.notifiers.map((n) => n.name).sort()).toEqual(['ops', 'tap']);
    } finally {
      await a.stop();
    }
    // negative control: in internal/config/config.go add `Deployments []string \`json:"deployments"\``
    // to Document — the key-set assertion fails. Run.
  }, 60_000);
});

// The SEALED import (`POST /api/config/sealed`), which is what the UI's Apply-a-config page uses.
//
// Its gate is deliberately WIDER than /api/config's: an admin session may apply, but still may not
// export. Export is exfiltration — one GET and every credential is in the caller's hands — while
// import requires already holding the file AND its passphrase, and everything in it is about to be
// plaintext on this host anyway. These tests exist to keep that asymmetry from being "fixed" in
// either direction by someone who reads one half of it.
describe('applying a sealed config from a browser session', () => {
  test('an admin session may preview and apply, though it still may not export', async () => {
    const a = await host('cfg-sealed-a');
    const b = await host('cfg-sealed-b');
    const file = `${tmpd('cfg-sealed')}.sealed`;
    try {
      await seed(a);
      const pull = await runCli(['pull', 'config', '-o', file], { env: cliEnv(a) });
      expect(`${pull.code}: ${pull.all}`).toBe(`0: ${pull.all}`);
      const sealed = await Bun.file(file).text();

      // B needs an admin to hold the session, so bootstrap one that is NOT in the export.
      const made = await fetch(`${b.base}/api/users`, {
        method: 'POST',
        headers: { authorization: `Bearer ${b.token}`, 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'operator', password: PASS }),
      });
      expect(made.status).toBe(201);
      const login = await fetch(`${b.base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'operator', password: PASS }),
      });
      expect(login.status).toBe(200);
      const cookie = login.headers.get('set-cookie')!.split(';')[0]!;

      // The asymmetry, asserted directly: the same session that may apply may NOT export.
      expect((await fetch(`${b.base}/api/config`, { headers: { cookie } })).status).toBe(403);

      const post = (body: unknown, headers: Record<string, string> = { cookie }) =>
        fetch(`${b.base}/api/config/sealed`, { method: 'POST', headers: { ...headers, 'content-type': 'application/json' }, body: JSON.stringify(body) });

      // ── preview writes NOTHING ────────────────────────────────────────────────────────────────
      const pre = await post({ sealed, passphrase: KEY, preview: true });
      expect(pre.status).toBe(200);
      const preview = (await pre.json()) as { preview: boolean; trusts: string[]; users: number };
      expect(preview.preview).toBe(true);
      expect(preview.users).toBeGreaterThan(0);
      // It must name what the file will make this host trust — that is the whole reason it exists.
      expect(preview.trusts.join('\n')).toContain('alice');
      // …and alice still cannot log in, because preview applied nothing.
      expect((await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) })).status).not.toBe(200);

      // ── a wrong passphrase is a clean refusal, not a half-apply ───────────────────────────────
      const wrong = await post({ sealed, passphrase: `${KEY}-nope` });
      expect(wrong.status).toBe(400);
      expect((await wrong.text()).toLowerCase()).toContain('passphrase');
      expect((await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) })).status).not.toBe(200);

      // ── apply, for real ───────────────────────────────────────────────────────────────────────
      const done = await post({ sealed, passphrase: KEY });
      expect(done.status).toBe(200);
      const applied = (await done.json()) as { created: string[]; trusts: string[] };
      expect(applied.created.join('\n')).toContain('alice');
      const now = await fetch(`${b.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'alice', password: PASS }) });
      expect(now.status).toBe(200);
    } finally {
      await a.stop();
      await b.stop();
    }
    // negative control: change mayApplySealedConfig to require KindRoot — the preview call 403s.
  }, 180_000);

  test('an anonymous caller is refused', async () => {
    const a = await host('cfg-sealed-anon');
    try {
      const r = await fetch(`${a.base}/api/config/sealed`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ sealed: '{}', passphrase: 'x' }),
      });
      expect(r.status).toBe(401);
    } finally {
      await a.stop();
    }
    // negative control: move the /api/config/sealed branch above THE GATE in server.go — this 401
    // becomes a 400, and an unauthenticated caller reaches the unseal.
  }, 60_000);
});
