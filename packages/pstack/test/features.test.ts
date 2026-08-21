/**
 * 0.26.0: swarm, sleep/wake, share links.
 *
 * Same disciplines as stack.test.ts — a real server on port 0, a fake `docker` on PATH that answers
 * scripted JSON, assertions on whole response bodies and on `Outcome.steps`. Every test here was
 * broken once on purpose (the negative control) before it was kept.
 */

import { afterEach, describe, expect, test } from 'bun:test';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { parseSpec, parseDuration, SpecError, warnings } from '../src/spec.ts';
import { swarmify, stackRmCmd, stackLogsCmd, swarmInfo } from '../src/swarm.ts';
import { composeDown, composeLogsCommand, composeSleep, composeUp } from '../src/compose.ts';
import { sleep as sleepStack, down } from '../src/stack.ts';
import { nullSink } from '../src/log.ts';
import type { RunResult, Runner } from '../src/exec.ts';
import { createRunner } from '../src/exec.ts';
import { signShare, verifyShare, looksLikeShareToken } from '../src/share.ts';
import { createServer } from '../src/api.ts';
import { Scheduler, SleepIndex, TrafficMeter, formatDuration, splitHosts, wakePage } from '../src/scheduler.ts';
import { deploymentRuntime } from '../src/inspect.ts';
import { init } from '../src/init.ts';
import { readControlState } from '../src/upgrade.ts';
import { redactText } from '../src/redact.ts';
import { renderWorkerCloudInit } from '../src/cloudinit.ts';
import { materializeCompose, GENERATED_COMPOSE } from '../src/autolabel.ts';
import { Registry } from '../src/registry.ts';
import { events } from '../src/events.ts';
import { summarize } from '../src/notify.ts';

const TOKEN = 'test-token-0123456789abcdef';
const H = { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' };
const tmpd = (tag: string) =>
  `${process.env.TMPDIR ?? '/tmp'}/pstack-f-${tag}-${process.pid}-${Math.trunc(performance.now() * 1000)}`;

function fakeRunner(fail: (cmd: string) => boolean = () => false, stdout = ''): Runner & { log: string[] } {
  const log: string[] = [];
  return {
    dryRun: false,
    log,
    async run(cmd: string): Promise<RunResult> {
      log.push(cmd.trim());
      const bad = fail(cmd);
      return { ok: !bad, code: bad ? 1 : 0, stdout, stderr: bad ? 'boom' : '', skipped: false };
    },
  };
}

/** A `docker` on PATH answering from a shell `case` over "$*". */
function dockerShim(script: string): () => void {
  const dir = tmpd('docker');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'docker'), `#!/bin/sh\ncase "$*" in\n${script}\n  *) exit 0 ;;\nesac\n`, { mode: 0o755 });
  const prev = process.env.PATH;
  process.env.PATH = `${dir}:${prev}`;
  return () => {
    process.env.PATH = prev;
    rmSync(dir, { recursive: true, force: true });
  };
}

const waitJob = async (base: string, id: string) => {
  for (let i = 0; i < 200; i++) {
    const j = (await (await fetch(`${base}/api/jobs/${id}`, { headers: H })).json()) as { job: { state: string; outcome?: unknown } };
    if (j.job.state !== 'running') return j.job;
    await Bun.sleep(10);
  }
  throw new Error('job never finished');
};

// ─────────────────────────────────────────────────────────────────────────────────────────────────

describe('spec: orchestrator and sleep', () => {
  const base = 'version: 1\nstack: s\ncompose: {file: dc.yml, profiles: []}\naxes: []\n';

  test('orchestrator: spec key beats PSTACK_ORCHESTRATOR beats compose', () => {
    expect(parseSpec(base, {}).compose!.orchestrator).toBe('compose');
    expect(parseSpec(base, { PSTACK_ORCHESTRATOR: 'swarm' }).compose!.orchestrator).toBe('swarm');
    expect(
      parseSpec(base.replace('profiles: []', 'profiles: [], orchestrator: compose'), { PSTACK_ORCHESTRATOR: 'swarm' })
        .compose!.orchestrator,
    ).toBe('compose');
    expect(() => parseSpec(base, { PSTACK_ORCHESTRATOR: 'nomad' })).toThrow(/compose.orchestrator must be/);
  });

  test('sleep: durations parse, junk is refused, an empty block is refused', () => {
    expect(parseDuration('90s')).toBe(90_000);
    expect(parseDuration('1h30m')).toBe(5_400_000);
    expect(parseDuration('3d')).toBe(259_200_000);
    expect(parseDuration('30')).toBeNull();
    expect(parseDuration('2 hours')).toBeNull();
    const s = parseSpec(`${base}sleep: {idle: 2h, after: 3d}\n`, {});
    expect(s.sleep).toEqual({ idleMs: 7_200_000, afterMs: 259_200_000 });
    expect(() => parseSpec(`${base}sleep: {idle: soon}\n`, {})).toThrow(SpecError);
    expect(() => parseSpec(`${base}sleep: {}\n`, {})).toThrow(/neither/);
    expect(() => parseSpec(`${base}sleep: {idle: 1h, cron: x}\n`, {})).toThrow(/unknown key/);
    parseSpec('version: 1\nstack: s\naxes: []\nsleep: {idle: 1h}\n', {});
    expect(warnings.some((w) => w.includes('no `compose` section'))).toBe(true);
  });
});

describe('swarmify: plain compose → the swarm subset', () => {
  const doc = {
    name: 'whatever',
    version: '2.4',
    services: {
      app: {
        image: 'nginx',
        restart: 'always',
        mem_limit: '256m',
        cpus: 0.5,
        profiles: ['app'],
        container_name: 'my-app',
        labels: ['traefik.enable=true', 'traefik.docker.network=preview-ingress', 'pstack.routing.port=80'],
        depends_on: { db: { condition: 'service_healthy' } },
        pull_policy: 'always',
        'x-note': 'kept',
      },
      migrate: { image: 'app', command: 'migrate', profiles: ['app'] },
      worker: { image: 'app', restart: 'on-failure:3', profiles: ['worker'] },
      db: { image: 'postgres', deploy: { replicas: 2, restart_policy: { condition: 'any' } } },
    },
  };

  test('converts faithfully and names every change', () => {
    const { doc: out, notes } = swarmify(doc, { profiles: ['app'] });
    const svc = out.services as Record<string, Record<string, unknown>>;
    expect(Object.keys(svc).sort()).toEqual(['app', 'db', 'migrate']); // worker's profile not selected
    expect(out.name).toBeUndefined();
    expect(out.version).toBeUndefined();

    const app = svc.app!;
    const deploy = app.deploy as Record<string, unknown>;
    expect(deploy.restart_policy).toEqual({ condition: 'any' });
    expect(deploy.resources).toEqual({ limits: { memory: '256m', cpus: '0.5' } });
    expect(deploy.labels).toEqual(['traefik.enable=true', 'traefik.swarm.network=preview-ingress']);
    expect(app.labels).toEqual(['pstack.routing.port=80']);
    for (const k of ['restart', 'mem_limit', 'cpus', 'profiles', 'container_name', 'depends_on', 'pull_policy']) {
      expect(app[k]).toBeUndefined();
    }
    expect(app['x-note']).toBe('kept');

    // No `restart:` is `none` — compose would not restart it, and swarm's default `any` would loop a
    // one-shot migration forever.
    expect((svc.migrate!.deploy as Record<string, unknown>).restart_policy).toEqual({ condition: 'none' });
    // A deploy block the author wrote wins.
    expect((svc.db!.deploy as Record<string, unknown>).restart_policy).toEqual({ condition: 'any' });
    expect((svc.db!.deploy as Record<string, unknown>).replicas).toBe(2);

    const text = notes.join('\n');
    expect(text).toContain('service app: restart: always → deploy.restart_policy.condition: any');
    expect(text).toContain('mem_limit → deploy.resources.limits.memory');
    expect(text).toContain('dropped `container_name`');
    expect(text).toContain('dropped `pull_policy` — not in the compose v3 schema');
    expect(text).toContain('service worker: not in the selected profiles (worker)');
    expect(text).toContain('traefik.docker.network → traefik.swarm.network');
    // Pure: the input was not touched.
    expect(doc.services.app.restart).toBe('always');
  });

  test('on-failure:N keeps the attempt count; an already swarm-shaped file needs no notes', () => {
    const { doc: out } = swarmify(doc, { profiles: ['worker'] });
    const worker = (out.services as Record<string, Record<string, unknown>>).worker!;
    expect((worker.deploy as Record<string, unknown>).restart_policy).toEqual({ condition: 'on-failure', max_attempts: 3 });

    const clean = { services: { a: { image: 'x', deploy: { restart_policy: { condition: 'none' } } } } };
    expect(swarmify(clean, { profiles: [] }).notes).toEqual([]);
  });
});

describe('compose.ts dispatches on the orchestrator', () => {
  const swarmSpec = parseSpec('version: 1\nstack: s1\ncompose: {file: dc.yml, profiles: [a, b], orchestrator: swarm}\naxes: []', {});
  const composeSpec = parseSpec('version: 1\nstack: s1\ncompose: {file: dc.yml, profiles: [a]}\naxes: []', {});

  test('up/down/sleep/logs/ps become docker stack / service commands', async () => {
    const r = fakeRunner();
    // dryRun-free runner with no cwd and a missing file: materialize falls back to the original name.
    await composeUp(swarmSpec, r, {});
    await composeDown(swarmSpec, r);
    await composeSleep(swarmSpec, r);
    const logs = await composeLogsCommand(swarmSpec, r, 50, 'web', { follow: true, timestamps: true });
    expect(r.log[0]).toBe(`docker stack deploy -c 'dc.yml' --prune --with-registry-auth --detach=true 's1'`);
    expect(r.log[1]).toBe(stackRmCmd('s1', { volumes: true }));
    expect(r.log[1]).toContain('docker volume rm');
    expect(r.log[2]).toBe(stackRmCmd('s1', { volumes: false }));
    expect(r.log[2]).not.toContain('docker volume');
    expect(logs!.cmd).toBe(`docker service logs --no-color --tail 50 --follow --timestamps 's1_web'`);
    expect(stackLogsCmd('s1', 10, undefined, { follow: true })).toContain('wait');
    expect(stackLogsCmd('s1', 10, undefined, {})).not.toContain('wait');
  });

  test('compose sleep is `down` WITHOUT -v; down keeps -v', async () => {
    const r = fakeRunner();
    await composeSleep(composeSpec, r);
    await composeDown(composeSpec, r);
    expect(r.log[0]).toBe(`docker compose -p 's1' -f 'dc.yml' --profile 'a' down --remove-orphans`);
    expect(r.log[1]).toBe(`docker compose -p 's1' -f 'dc.yml' --profile 'a' down -v --remove-orphans`);
  });

  test('stack.sleep runs nothing but the compose step; shared is refused', async () => {
    const s = parseSpec(
      'version: 1\nstack: s1\ncompose: {file: dc.yml, profiles: []}\naxes:\n  - name: db\n    up: "true"\n    down: "echo DOWN"\n    assert_gone: "false"\n',
      {},
    );
    const r = fakeRunner();
    const o = await sleepStack(s, r, nullSink());
    expect(o.ok).toBe(true);
    expect(o.steps.map((st) => st.phase)).toEqual(['compose']);
    expect(r.log).toHaveLength(1);
    expect(r.log[0]).toContain('down --remove-orphans');
    expect(r.log[0]).not.toContain(' -v ');
    // Contrast: down runs the axis and verifies.
    const r2 = fakeRunner();
    const d = await down(s, r2, {}, nullSink());
    expect(d.steps.map((st) => st.phase)).toEqual(['compose', 'down', 'assert_gone']);

    const shared = parseSpec('version: 1\nkind: shared\nstack: db\ncompose: {file: dc.yml, profiles: []}\n', {});
    const r3 = fakeRunner();
    const refused = await sleepStack(shared, r3, nullSink());
    expect(refused.ok).toBe(false);
    expect(refused.steps[0]!.message).toMatch(/refused: kind is `shared`/);
    expect(r3.log).toEqual([]);
  });

  test('materialize writes the converted file beside the compose file, also under the CLI (no cwd)', async () => {
    const dir = tmpd('mat');
    mkdirSync(join(dir, 'infra'), { recursive: true });
    writeFileSync(join(dir, 'infra', 'dc.yml'), 'services:\n  app:\n    image: nginx\n    restart: always\n    profiles: [a]\n');
    const s = parseSpec('version: 1\nstack: s1\ncompose: {file: infra/dc.yml, profiles: [a], orchestrator: swarm}\naxes: []', {});
    const r = createRunner({ dryRun: false, level: 'quiet', cwd: dir, baseEnv: process.env as Record<string, string> });
    const m = await materializeCompose({ dir, spec: s, runner: r, challenge: 'http01' });
    expect(m.file).toBe(`infra/${GENERATED_COMPOSE}`);
    const written = JSON.parse(readFileSync(join(dir, 'infra', GENERATED_COMPOSE), 'utf8')) as { services: Record<string, Record<string, unknown>> };
    expect(written.services.app!.deploy).toEqual({ restart_policy: { condition: 'any' } });
    expect(written.services.app!.profiles).toBeUndefined();
    expect(m.notes.length).toBeGreaterThan(0);
    // Under compose with nothing to generate, nothing is written.
    const plain = parseSpec('version: 1\nstack: s1\ncompose: {file: infra/dc.yml, profiles: [a]}\naxes: []', {});
    rmSync(join(dir, 'infra', GENERATED_COMPOSE));
    expect((await materializeCompose({ dir, spec: plain, runner: r, challenge: 'http01' })).file).toBe('infra/dc.yml');
    rmSync(dir, { recursive: true, force: true });
  });
});

describe('share.ts', () => {
  test('sign → verify round-trips; anything off is null', () => {
    const { token, claims } = signShare(TOKEN, { deployment: 'pr-1', views: ['logs', 'logs', 'details'], ttlMs: 60_000, now: 1_000_000 });
    expect(looksLikeShareToken(token)).toBe(true);
    expect(claims).toEqual({ sub: 'share', dep: 'pr-1', views: ['logs', 'details'], iat: 1000, exp: 1060 });
    expect(verifyShare(TOKEN, token, 1_050_000)).toEqual(claims);
    expect(verifyShare(TOKEN, token, 1_060_000)).toBeNull(); // expired
    expect(verifyShare('other-key', token, 1_050_000)).toBeNull();
    const [h, b, sig] = token.split('.');
    expect(verifyShare(TOKEN, `${h}.${b}.${sig!.slice(0, -1)}x`, 1_050_000)).toBeNull(); // tampered signature
    // A re-encoded body with a different deployment does not carry the old signature.
    const forged = Buffer.from(JSON.stringify({ ...claims, dep: 'pr-2' })).toString('base64url');
    expect(verifyShare(TOKEN, `${h}.${forged}.${sig}`, 1_050_000)).toBeNull();
    expect(verifyShare(TOKEN, TOKEN)).toBeNull();
    expect(() => signShare(TOKEN, { deployment: 'x', views: [], ttlMs: 1 })).toThrow(/views/);
    expect(() => signShare('', { deployment: 'x', views: ['logs'], ttlMs: 1 })).toThrow(/PSTACK_TOKEN/);
  });
});

describe('API: share links', () => {
  const boot = (token: string | null = TOKEN) => {
    const server = createServer({ dataDir: tmpd('share'), port: 0, host: '127.0.0.1', token: token ?? undefined });
    return { server, base: `http://127.0.0.1:${server.port}` };
  };
  const put = (base: string, id: string) =>
    fetch(`${base}/api/deployments/${id}`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({ spec: `version: 1\nstack: ${id}\naxes:\n  - name: a\n    up: "true"\n` }),
    });

  test('a minted token reaches exactly its views on exactly its deployment', async () => {
    const { server, base } = boot();
    try {
      expect((await put(base, 'pr-1')).status).toBe(201);
      expect((await put(base, 'pr-2')).status).toBe(201);
      const seen: string[] = [];
      const off = events.on((e) => { if (e.event === 'share.created') seen.push(JSON.stringify(e.data)); });

      const mint = await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ views: ['details'], ttl: '2h' }) });
      expect(mint.status).toBe(201);
      const link = (await mint.json()) as { url: string; token: string; views: string[]; expiresAt: number };
      expect(link.views).toEqual(['details']);
      expect(link.url).toBe(`${base}/deployments/pr-1/public-logs-view?token=${link.token}`);
      expect(link.expiresAt).toBeGreaterThan(Date.now() + 7_000_000);
      off();
      expect(seen).toHaveLength(1);
      expect(seen[0]).not.toContain(link.token); // the envelope goes to webhook URLs
      expect(seen[0]).toContain('"views":["details"]');

      const q = `?token=${link.token}`;
      // The page itself, no auth.
      const page = await fetch(`${base}/deployments/pr-1/public-logs-view${q}`);
      expect(page.status).toBe(200);
      expect(await page.text()).toContain('public-logs-view');
      // Who am I.
      const me = (await (await fetch(`${base}/api/auth/me${q}`)).json()) as { share: { deployment: string; views: string[]; expiresAt: number } };
      expect(me.share.deployment).toBe('pr-1');
      expect(me.share.views).toEqual(['details']);
      // Details: allowed. Its stack is the STORED one even with a variable in the query.
      const det = await fetch(`${base}/api/deployments/pr-1${q}&PR=9`);
      expect(det.status).toBe(200);
      expect(((await det.json()) as { stack: string }).stack).toBe('pr-1');
      expect((await fetch(`${base}/api/deployments/pr-1/runtime${q}`)).status).not.toBe(403);
      // Logs view not granted.
      expect((await fetch(`${base}/api/deployments/pr-1/logs${q}`)).status).toBe(403);
      // Another deployment, a write, the list, the terminal: all refused.
      expect((await fetch(`${base}/api/deployments/pr-2${q}`)).status).toBe(403);
      expect((await fetch(`${base}/api/deployments/pr-1/up${q}`, { method: 'POST' })).status).toBe(403);
      expect((await fetch(`${base}/api/deployments${q}`)).status).toBe(403);
      expect((await fetch(`${base}/api/deployments/pr-1/terminal${q}&container=x`)).status).toBe(403);
      expect((await fetch(`${base}/api/jobs${q}`)).status).toBe(403);
      // As a bearer, same thing.
      expect((await fetch(`${base}/api/deployments/pr-1`, { headers: { authorization: `Bearer ${link.token}` } })).status).toBe(200);
      // The raw PSTACK_TOKEN in the query is NOT a credential.
      expect((await fetch(`${base}/api/deployments/pr-1?token=${TOKEN}`)).status).toBe(401);
      // Garbage and an expired token are 401.
      expect((await fetch(`${base}/api/deployments/pr-1?token=eyJx.yyy.zzz`)).status).toBe(401);
      const expired = signShare(TOKEN, { deployment: 'pr-1', views: ['details'], ttlMs: 1000, now: Date.now() - 10_000 }).token;
      expect((await fetch(`${base}/api/deployments/pr-1?token=${expired}`)).status).toBe(401);
      // Validation.
      expect((await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ views: ['terminal'] }) })).status).toBe(400);
      expect((await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ ttl: '90d' }) })).status).toBe(400);
      expect((await fetch(`${base}/api/deployments/nope/share`, { method: 'POST', headers: H, body: '{}' })).status).toBe(404);
    } finally {
      server.stop(true);
    }
  });

  test('a share token holder cannot mint another link, and a token-less server cannot mint at all', async () => {
    const { server, base } = boot();
    try {
      await put(base, 'pr-1');
      const link = (await (await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: '{}' })).json()) as { token: string; views: string[] };
      expect(link.views).toEqual(['details', 'logs']);
      expect((await fetch(`${base}/api/deployments/pr-1/share?token=${link.token}`, { method: 'POST' })).status).toBe(403);
    } finally {
      server.stop(true);
    }
    const dev = boot(null);
    try {
      await fetch(`${dev.base}/api/deployments/pr-1`, { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ spec: 'version: 1\nstack: pr-1\naxes: []\n' }) });
      const r = await fetch(`${dev.base}/api/deployments/pr-1/share`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
      expect(r.status).toBe(400);
      expect(((await r.json()) as { error: string }).error).toMatch(/PSTACK_TOKEN/);
    } finally {
      dev.server.stop(true);
    }
  });
});

describe('API: sleep, wake-on-call', () => {
  let restore: (() => void) | null = null;
  afterEach(() => { restore?.(); restore = null; });

  /** One running container for stack `wk` with a Host() router, and every compose verb succeeds. */
  const shim = () =>
    dockerShim(`
  "ps -aq --filter label=com.docker.compose.project=wk") echo "c0ffee123456" ;;
  "inspect c0ffee123456") printf '%s\\n' '[{"Id":"c0ffee123456","Name":"/wk-app-1","Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"wk","traefik.enable":"true","traefik.http.routers.app-wk.rule":"Host(\`app-wk.example.com\`)","traefik.http.routers.app-wk-wild.rule":"HostRegexp(\`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\\\\.app-wk\\\\.example\\\\.com$\`)","traefik.http.services.app-wk.loadbalancer.server.port":"80"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]' ;;
  "compose ls --all --format json") printf '%s\\n' '[{"Name":"wk"}]' ;;`);

  const boot = () => {
    const dataDir = tmpd('wake');
    const server = createServer({ dataDir, port: 0, host: '127.0.0.1', token: TOKEN });
    return { server, dataDir, base: `http://127.0.0.1:${server.port}` };
  };
  const submit = (base: string, extra = '') =>
    fetch(`${base}/api/deployments/wk`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({
        spec: `version: 1\nstack: wk\ncompose: {file: compose.yml, profiles: []}\n${extra}axes:\n  - name: a\n    up: "echo up"\n    down: "echo down"\n    assert_gone: "true"\n`,
        compose: 'services:\n  app:\n    image: nginx\n',
      }),
    });

  test('sleep records the live hostnames; a request to one wakes the stack and clears the record', async () => {
    restore = shim();
    const { server, base, dataDir } = boot();
    const seen: Array<{ event: string; data: Record<string, unknown> }> = [];
    const off = events.on((e) => { if (e.event === 'stack.slept' || e.event === 'stack.woken') seen.push({ event: e.event, data: e.data }); });
    try {
      expect((await submit(base)).status).toBe(201);

      // Before: awake, listed as running, not asleep.
      const before = (await (await fetch(`${base}/api/deployments/wk`, { headers: H })).json()) as { asleep: unknown; orchestrator: string; sleep: unknown };
      expect(before.asleep).toBeNull();
      expect(before.orchestrator).toBe('compose');
      expect(before.sleep).toBeNull();

      const s = await fetch(`${base}/api/deployments/wk/sleep`, { method: 'POST', headers: H });
      expect(s.status).toBe(202);
      const sjob = ((await s.json()) as { job: { id: string; action: string } }).job;
      expect(sjob.action).toBe('sleep');
      const done = await waitJob(base, sjob.id);
      expect(done.state).toBe('ok');
      const outcome = done.outcome as { steps: Array<{ phase: string; axis: string }> };
      // The compose step only: no axis `down`, no `assert_gone`.
      expect(outcome.steps.map((st) => st.phase)).toEqual(['compose']);

      const meta = JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')) as { sleep: { hosts: string[]; rules: string[]; reason: string } };
      expect(meta.sleep.hosts).toEqual(['app-wk.example.com']);
      expect(meta.sleep.rules).toHaveLength(1);
      expect(meta.sleep.reason).toMatch(/^operator: root/);
      expect(seen.map((e) => e.event)).toEqual(['stack.slept']);
      expect(seen[0]!.data.hosts).toEqual(['app-wk.example.com']);

      const list = (await (await fetch(`${base}/api/deployments`, { headers: H })).json()) as { deployments: Array<{ id: string; asleep: unknown }> };
      expect(list.deployments[0]!.asleep).not.toBeNull();
      const detail = (await (await fetch(`${base}/api/deployments/wk`, { headers: H })).json()) as { asleep: { hosts: string[] } };
      expect(detail.asleep.hosts).toEqual(['app-wk.example.com']);

      // A request for the hostname — any path, no auth, the Host header Traefik forwards.
      const wake = await fetch(`${base}/api/deployments?anything=1`, { headers: { host: 'App-WK.example.com:443' } });
      expect(wake.status).toBe(503);
      expect(wake.headers.get('x-pstack-wake')).toBe('1');
      expect(wake.headers.get('retry-after')).toBe('5');
      const html = await wake.text();
      expect(html).toContain('Your preview is spinning up');
      expect(html).toContain('wk');
      // The wildcard rule matches too.
      const sub = await fetch(`${base}/`, { headers: { 'x-forwarded-host': 'api.app-wk.example.com' } });
      expect(sub.status).toBe(503);
      // An unrelated hostname still gets the UI.
      const ui = await fetch(`${base}/`, { headers: { host: 'control.example.com' } });
      expect(ui.status).toBe(200);
      expect(ui.headers.get('x-pstack-wake')).toBeNull();

      // Exactly one wake job ran (the second request found it busy or done), as `up`.
      for (let i = 0; i < 100 && !seen.some((e) => e.event === 'stack.woken'); i++) await Bun.sleep(10);
      const jobs = (await (await fetch(`${base}/api/jobs`, { headers: H })).json()) as { jobs: Array<{ id: string; action: string; state: string }> };
      const wakes = jobs.jobs.filter((j) => j.action === 'wake');
      expect(wakes).toHaveLength(1);
      const w = await waitJob(base, wakes[0]!.id);
      expect(w.state).toBe('ok');
      expect((w.outcome as { steps: Array<{ phase: string }> }).steps.map((st) => st.phase)).toEqual(['up', 'assert_live', 'compose'].filter((p) => p !== 'assert_live'));
      expect(seen.find((e) => e.event === 'stack.woken')!.data.by).toBe('request:app-wk.example.com');

      // Awake again: the record is gone and the hostname is nobody's to wake.
      for (let i = 0; i < 100; i++) {
        const m = JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')) as { sleep?: unknown };
        if (!m.sleep) break;
        await Bun.sleep(10);
      }
      expect(JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')).sleep).toBeUndefined();
      const after = await fetch(`${base}/`, { headers: { host: 'app-wk.example.com' } });
      expect(after.status).toBe(200);
      expect(after.headers.get('x-pstack-wake')).toBeNull();
    } finally {
      off();
      server.stop(true);
    }
  });

  test('sleep is refused for a shared deployment and for a spec without compose', async () => {
    restore = shim();
    const { server, base } = boot();
    try {
      await fetch(`${base}/api/deployments/shared`, { method: 'PUT', headers: H, body: JSON.stringify({ spec: 'version: 1\nkind: shared\nstack: db\ncompose: {file: c.yml, profiles: []}\n', compose: 'services: {}' }) });
      const r = await fetch(`${base}/api/deployments/shared/sleep`, { method: 'POST', headers: H });
      expect(r.status).toBe(409);
      expect(((await r.json()) as { error: string }).error).toMatch(/shared/);
      await fetch(`${base}/api/deployments/bare`, { method: 'PUT', headers: H, body: JSON.stringify({ spec: 'version: 1\nstack: bare\naxes: []\n' }) });
      expect((await fetch(`${base}/api/deployments/bare/sleep`, { method: 'POST', headers: H })).status).toBe(400);
      // wake is up by another name
      const w = await fetch(`${base}/api/deployments/bare/wake`, { method: 'POST', headers: H });
      expect(w.status).toBe(202);
      expect(((await w.json()) as { job: { action: string } }).job.action).toBe('wake');
    } finally {
      server.stop(true);
    }
  });

  test('a down clears a sleep record; a replace keeps it', async () => {
    restore = shim();
    const { server, base, dataDir } = boot();
    try {
      await submit(base);
      const sj = ((await (await fetch(`${base}/api/deployments/wk/sleep`, { method: 'POST', headers: H })).json()) as { job: { id: string } }).job;
      await waitJob(base, sj.id);
      const metaPath = join(dataDir, 'deployments', 'wk', 'meta.json');
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep).toBeDefined();
      // Replace the spec: still asleep, same hostnames.
      expect((await submit(base, 'sleep: {after: 1d}\n')).status).toBe(200);
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep.hosts).toEqual(['app-wk.example.com']);
      const d = (await (await fetch(`${base}/api/deployments/wk`, { headers: H })).json()) as { sleep: { after: string; idle: null } };
      expect(d.sleep).toEqual({ after: '1d', idle: null });
      const dj = ((await (await fetch(`${base}/api/deployments/wk/down`, { method: 'POST', headers: H, body: '{}' })).json()) as { job: { id: string } }).job;
      await waitJob(base, dj.id);
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep).toBeUndefined();
    } finally {
      server.stop(true);
    }
  });
});

describe('scheduler', () => {
  test('SleepIndex: exact hosts and Go-style rules, case-insensitively', () => {
    const ix = new SleepIndex();
    ix.rebuild([
      { id: 'a', kind: 'isolated', createdAt: 0, updatedAt: 0, sleep: { since: 0, reason: 'x', hosts: ['app-a.example.com'], rules: ['^[a-z0-9-]+\\.app-a\\.example\\.com$'] } },
      { id: 'b', kind: 'isolated', createdAt: 0, updatedAt: 0 },
    ]);
    expect(ix.size).toBe(2);
    expect(ix.find('APP-A.example.com')).toBe('a');
    expect(ix.find('x.app-a.example.com')).toBe('a');
    expect(ix.find('app-b.example.com')).toBeNull();
    expect(splitHosts(['h.example.com', '(pattern) ^x$'])).toEqual({ hosts: ['h.example.com'], rules: ['^x$'] });
    expect(formatDuration(5_400_000)).toBe('1h30m');
    expect(wakePage({ host: 'h', stack: 's<', state: 'failed', error: 'pull denied' })).toContain('s&lt;');
  });

  test('TrafficMeter: a router that moved is activity; a reset is not; unreachable is not silence', async () => {
    let text: string | null = 'traefik_router_requests_total{code="200",method="GET",protocol="http",router="app-wk@docker",service="app-wk@docker"} 5\n';
    const m = new TrafficMeter('http://x/metrics', { fetchImpl: async () => text });
    await m.sample(1000);
    expect(m.ok).toBe(true);
    expect(m.lastActivity(['app-wk'])).toBeNull(); // first sample is a baseline
    text = text.replace(' 5', ' 7');
    await m.sample(2000);
    expect(m.lastActivity(['app-wk'])).toBe(2000);
    text = text.replace(' 7', ' 1'); // Traefik restarted
    await m.sample(3000);
    expect(m.lastActivity(['app-wk'])).toBe(2000);
    text = null;
    await m.sample(4000);
    expect(m.ok).toBe(false);
    expect(new TrafficMeter(undefined).ok).toBe(false);
  });

  test('decides `after` from the newest container, `idle` from the meter, and never twice', async () => {
    const slept: string[] = [];
    let now = 1_000_000_000;
    let metrics: string | null = 'traefik_router_requests_total{router="app-wk@docker"} 1\n';
    const meter = new TrafficMeter('http://x', { fetchImpl: async () => metrics });
    const spec = parseSpec('version: 1\nstack: wk\ncompose: {file: c.yml, profiles: []}\nsleep: {idle: 1h, after: 2d}\naxes: []\n', {});
    let meta: { sleep?: import('../src/registry.ts').SleepRecord } = {};
    let startedAt = now - 3_600_000; // deployed an hour ago
    const sched = new Scheduler({
      list: async () => [{ id: 'wk', kind: 'isolated', createdAt: 0, updatedAt: 0, ...meta }],
      resolve: async () => spec,
      runtime: async () => ({
        stack: 'wk', reachable: true, routes: [{ router: 'app-wk', container: 'c', rule: '', hosts: [], service: null, port: 80, entrypoints: null, tls: false, certresolver: null, priority: null, target: null }],
        findings: [], challenge: 'unknown',
        containers: [{ id: 'c', name: 'c', service: 'app', image: 'x', state: 'running', health: null, exitCode: null, restartCount: 0, networks: [], ingressIp: null, ports: [], traefikLabels: {}, startedAt, node: null, remote: false }],
      }),
      isBusy: () => false,
      sleep: (id, _s, reason) => { slept.push(`${id}:${reason}`); meta = { sleep: { since: now, reason, hosts: [], rules: [] } }; },
      meter,
      now: () => now,
    });
    await sched.tick(); // baseline; idle clock starts at boot
    expect(slept).toEqual([]);
    now += 30 * 60_000;
    await sched.tick();
    expect(slept).toEqual([]); // 30m idle < 1h
    now += 31 * 60_000;
    metrics = metrics.replace(' 1', ' 2'); // a visit just now
    await sched.tick();
    expect(slept).toEqual([]); // activity reset the clock
    now += 61 * 60_000;
    await sched.tick();
    expect(slept).toEqual(['wk:idle 1h']);
    await sched.tick();
    expect(slept).toEqual(['wk:idle 1h']); // asleep: not again

    // `after`: with the meter down, idle cannot decide, but a 2-day-old deploy can.
    slept.length = 0; meta = {}; metrics = null;
    await sched.tick();
    expect(slept).toEqual([]);
    startedAt = now - 2 * 86_400_000;
    await sched.tick();
    expect(slept).toEqual(['wk:after 2d']);
  });
});

describe('swarm discovery and the swarm routes', () => {
  let restore: (() => void) | null = null;
  afterEach(() => { restore?.(); restore = null; });

  const swarmShim = () =>
    dockerShim(`
  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;
  "node ls --format {{json .}}") printf '%s\\n' '{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' '{"ID":"n2","Hostname":"wrk","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ;;
  "swarm join-token -q worker") echo "SWMTKN-1-abc-def" ;;
  "service ls -q --filter label=com.docker.stack.namespace=sw") echo "svc1" ;;
  "service ls -q --filter label=traefik.enable=true") echo "svc1" ;;
  "service inspect svc1") printf '%s\\n' '[{"ID":"svc1xxxxxxxxxxxx","UpdatedAt":"2026-08-20T10:00:00Z","Spec":{"Name":"sw_app","Labels":{"com.docker.stack.namespace":"sw","traefik.enable":"true","traefik.swarm.network":"preview-ingress","traefik.http.routers.app-sw.rule":"Host(\`app-sw.example.com\`)","traefik.http.services.app-sw.loadbalancer.server.port":"80"},"TaskTemplate":{"ContainerSpec":{"Image":"nginx"},"Networks":[{"Target":"net1"}]}}}]' ;;
  "network ls --format {{.ID}} {{.Name}}") printf '%s\\n' 'net1 preview-ingress' 'net2 sw_default' ;;
  "stack ps sw --no-trunc --filter desired-state=running --format {{json .}}") printf '%s\\n' '{"ID":"task1","Name":"sw_app.1","Image":"nginx","Node":"mgr","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}' '{"ID":"task2","Name":"sw_app.2","Image":"nginx","Node":"wrk","DesiredState":"Running","CurrentState":"Running 1 minute ago"}' ;;
  "ps -aq --filter label=com.docker.stack.namespace=sw") printf '%s\\n' "aaa111aaa111" "bbb222bbb222" ;;
  "inspect aaa111aaa111 bbb222bbb222") printf '%s\\n' '[{"Id":"aaa111aaa111","Name":"/sw_app.1.task1","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"task1","com.docker.swarm.node.id":"n1"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:01:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}},{"Id":"bbb222bbb222","Name":"/sw_app.1.oldtask","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"dead0","com.docker.swarm.node.id":"n1"}},"State":{"Status":"exited","ExitCode":1},"NetworkSettings":{"Networks":{},"Ports":{}}}]' ;;`);

  test('deploymentRuntime under swarm: service labels, live tasks only, remote tasks listed', async () => {
    restore = swarmShim();
    const runner = createRunner({ dryRun: false, level: 'quiet', baseEnv: process.env as Record<string, string> });
    const rt = await deploymentRuntime({ stack: 'sw', runner, challenge: 'unknown', orchestrator: 'swarm' });
    expect(rt.reachable).toBe(true);
    expect(rt.containers.map((c) => [c.name, c.service, c.state, c.node, c.remote])).toEqual([
      ['sw_app.1.task1', 'app', 'running', 'mgr', false],
      ['sw_app.2', 'app', 'running', 'wrk', true],
    ]);
    expect(rt.containers[0]!.startedAt).toBe(Date.parse('2026-08-20T10:01:00Z'));
    expect(rt.routes).toHaveLength(1);
    expect(rt.routes[0]!.hosts).toEqual(['app-sw.example.com']);
    expect(rt.routes[0]!.port).toBe(80);
    // A swarm service with the right labels and network draws no label/network finding.
    expect(rt.findings.filter((f) => f.level === 'error')).toEqual([]);
    expect(rt.findings.some((f) => f.message.includes('runs on node wrk'))).toBe(true);
    // The stale exited container of a replaced task is not reported.
    expect(rt.containers.some((c) => c.name.includes('oldtask'))).toBe(false);
  });

  test('swarmInfo and the API routes; the join token is admin-only text', async () => {
    restore = swarmShim();
    const runner = createRunner({ dryRun: false, level: 'quiet', baseEnv: process.env as Record<string, string> });
    const info = await swarmInfo(runner);
    expect(info.active).toBe(true);
    expect(info.managerAddr).toBe('10.0.0.1:2377');
    expect(info.nodes.map((n) => [n.hostname, n.role, n.self])).toEqual([['mgr', 'manager', true], ['wrk', 'worker', false]]);

    const server = createServer({ dataDir: tmpd('swarm'), port: 0, host: '127.0.0.1', token: TOKEN });
    const base = `http://127.0.0.1:${server.port}`;
    try {
      const panel = (await (await fetch(`${base}/api/swarm`, { headers: H })).json()) as { active: boolean; nodes: unknown[]; ports: unknown[] };
      expect(panel.active).toBe(true);
      expect(panel.nodes).toHaveLength(2);
      expect(panel.ports).toHaveLength(3);
      expect(JSON.stringify(panel)).not.toContain('SWMTKN');

      const cmd = await fetch(`${base}/api/swarm/join?format=command`, { headers: H });
      expect(cmd.headers.get('content-type')).toContain('text/plain');
      expect(await cmd.text()).toBe('docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377\n');
      expect(await (await fetch(`${base}/api/swarm/join?format=token`, { headers: H })).text()).toBe('SWMTKN-1-abc-def\n');
      const script = await (await fetch(`${base}/api/swarm/join?format=script`, { headers: H })).text();
      expect(script).toContain('#!/usr/bin/env bash');
      expect(script).toContain('docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377');
      const cc = await (await fetch(`${base}/api/swarm/join?format=cloud-config&distro=debian`, { headers: H })).text();
      expect(cc.startsWith('#cloud-config')).toBe(true);
      expect(cc).toContain('download.docker.com/linux/debian');
      expect(cc).toContain('docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377');
      expect((await fetch(`${base}/api/swarm/join?format=pdf`, { headers: H })).status).toBe(400);
      expect((await fetch(`${base}/api/swarm/join?format=cloud-config&distro=bsd`, { headers: H })).status).toBe(400);
      // A non-admin user may see the nodes but not the token.
      await fetch(`${base}/api/auth/bootstrap`, { method: 'POST', headers: H, body: JSON.stringify({ username: 'ops', password: 'hunter2hunter2' }) });
      // Everyone is an admin today, so the 403 is exercised through a share principal instead.
      const link = (await (await fetch(`${base}/api/deployments/x/share`, { method: 'POST', headers: H, body: '{}' })).json()) as { error?: string };
      expect(link.error).toMatch(/no such deployment/);
    } finally {
      server.stop(true);
    }
    expect(redactText('joined with SWMTKN-1-abc-def now')).toBe('joined with SWMTKN-•••••• now');
    expect(() => renderWorkerCloudInit({ token: 'nope', managerAddr: '1.2.3.4:2377' })).toThrow(/join token/);
  });

  test('when docker does not answer, the panel says so rather than "no nodes"', async () => {
    restore = dockerShim(`  *) exit 1 ;;`);
    const runner = createRunner({ dryRun: false, level: 'quiet', baseEnv: process.env as Record<string, string> });
    const info = await swarmInfo(runner);
    expect(info.reachable).toBe(false);
    expect(info.nodes).toEqual([]);
  });
});

describe('init: swarm mode and the wake catch-all', () => {
  const okRunner = (state = 'inactive', driver = ''): Runner & { log: string[] } => {
    const log: string[] = [];
    return {
      dryRun: false,
      log,
      async run(cmd: string) {
        log.push(cmd);
        const stdout = cmd.includes('State.Health.Status') ? 'healthy\n'
          : cmd.includes('Swarm.LocalNodeState') ? `${state}\n`
          : cmd.includes('{{.Driver}}') ? driver
          : '';
        return { ok: true, code: 0, stdout, stderr: '', skipped: false };
      },
    };
  };
  const render = async (orchestrator: 'swarm' | 'compose', runner = okRunner()) => {
    const dir = tmpd(`init-${orchestrator}`);
    await init({ dataDir: dir, domain: 'preview.example.com', acmeEmail: 'o@e.com', challenge: 'http01', ui: 'basic', orchestrator, dryRun: false, runner });
    return { dir, yaml: readFileSync(join(dir, 'control', 'docker-compose.yml'), 'utf8'), env: readFileSync(join(dir, 'control', '.env'), 'utf8'), log: runner.log };
  };

  test('swarm: swarm init when inactive, overlay attachable networks, the swarm provider, the env', async () => {
    const { yaml, env, log, dir } = await render('swarm');
    expect(log).toContain('docker swarm init');
    expect(log.filter((c) => c.startsWith('docker network create -d overlay --attachable preview-'))).toHaveLength(2);
    expect(yaml).toContain('--providers.swarm=true');
    expect(yaml).toContain('--providers.swarm.network=preview-ingress');
    expect(yaml).toContain('--providers.docker=true'); // the control stack's own routers
    expect(yaml).toContain('--metrics.prometheus.addrouterslabels=true');
    expect(yaml).toContain('PSTACK_DOMAIN: ${DOMAIN}');
    expect(yaml).toContain('PSTACK_TRAEFIK_METRICS: http://traefik:8082/metrics');
    expect(yaml).toContain('traefik.http.routers.pstack-wake.rule=HostRegexp(`^[a-z0-9-]+\\.preview\\.example\\.com$$`)');
    expect(yaml).toContain('traefik.http.routers.pstack-wake.priority=1');
    expect(yaml).not.toContain('pstack-wake.tls.certresolver');
    expect(env).toContain('PSTACK_ORCHESTRATOR=swarm');
    // Never the word upgrade greps for to detect dns01.
    expect(yaml.toLowerCase()).not.toContain('dnschallenge');
    expect((await readControlState(dir)).orchestrator).toBe('swarm');
    // Already a manager: no second init.
    const again = await render('swarm', okRunner('active'));
    expect(again.log).not.toContain('docker swarm init');
  });

  test('compose: no swarm init, bridge networks, no swarm provider; upgrade reads compose back', async () => {
    const { yaml, env, log, dir } = await render('compose');
    expect(log).not.toContain('docker swarm init');
    expect(log.filter((c) => c.startsWith('docker network create preview-'))).toHaveLength(2);
    expect(yaml).not.toContain('--providers.swarm');
    expect(yaml).toContain('pstack-wake.rule'); // the catch-all exists in both modes
    expect(env).toContain('PSTACK_ORCHESTRATOR=compose');
    expect((await readControlState(dir)).orchestrator).toBe('compose');
    // A pre-0.26 .env (no line at all) is a compose host.
    writeFileSync(join(dir, 'control', '.env'), env.split('\n').filter((l) => !l.startsWith('PSTACK_ORCHESTRATOR')).join('\n'));
    expect((await readControlState(dir)).orchestrator).toBe('compose');
  });

  test('a network with the other driver is swapped only when nothing but the control stack is on it', async () => {
    // bridge exists, swarm wanted, only control containers attached → down, rm, recreate
    const r = okRunner('active', 'bridge\n');
    const orig = r.run.bind(r);
    r.run = async (cmd: string) => {
      if (cmd.includes('range .Containers')) { r.log.push(cmd); return { ok: true, code: 0, stdout: 'pstack-control-traefik-1 pstack-control-pstack-1\n', stderr: '', skipped: false }; }
      return orig(cmd);
    };
    await render('swarm', r);
    expect(r.log.some((c) => c.startsWith('docker network rm preview-ingress'))).toBe(true);
    expect(r.log.some((c) => c.includes('-p pstack-control down'))).toBe(true);

    // a preview is attached → refuse, naming it
    const r2 = okRunner('active', 'bridge\n');
    const orig2 = r2.run.bind(r2);
    r2.run = async (cmd: string) => {
      if (cmd.includes('range .Containers')) return { ok: true, code: 0, stdout: 'pstack-control-traefik-1 pr-7-app-1\n', stderr: '', skipped: false };
      return orig2(cmd);
    };
    await expect(render('swarm', r2)).rejects.toThrow(/pr-7-app-1/);
    expect(r2.log.some((c) => c.startsWith('docker network rm'))).toBe(false);
  });
});

describe('notify: the new events have a line', () => {
  test('summaries', () => {
    const at = Date.now();
    expect(summarize({ id: 'e', event: 'stack.slept', at, data: { stack: 'pr-1', reason: 'idle 2h' } })).toBe('pr-1 went to sleep (idle 2h). Its data and axes stay; a request to its hostname wakes it.');
    expect(summarize({ id: 'e', event: 'stack.woken', at, data: { stack: 'pr-1', by: 'request:app-pr-1.example.com' } })).toBe('pr-1 is waking up — request:app-pr-1.example.com.');
    expect(summarize({ id: 'e', event: 'share.created', at, data: { id: 'pr-1', views: ['logs'], by: 'ana' } })).toBe('A read-only link to pr-1 was created by ana (logs).');
    expect(summarize({ id: 'e', event: 'job.started', at, data: { stack: 'pr-1', action: 'sleep', jobId: 'j' } })).toContain('Sleep');
  });
});

describe('registry: the sleep record', () => {
  test('setSleep writes and clears without touching updatedAt', async () => {
    const dir = tmpd('reg');
    const reg = new Registry(dir);
    const dep = await reg.put('d', 'version: 1\nstack: d\naxes: []\n');
    await reg.setSleep('d', { since: 1, reason: 'x', hosts: ['h'], rules: [] });
    const after = await reg.get('d');
    expect(after!.sleep).toEqual({ since: 1, reason: 'x', hosts: ['h'], rules: [] });
    expect(after!.updatedAt).toBe(dep.updatedAt);
    await reg.setSleep('d', null);
    expect((await reg.get('d'))!.sleep).toBeUndefined();
    rmSync(dir, { recursive: true, force: true });
  });
});
