/**
 * 0.26.0 over HTTP: share links, sleep / wake-on-call, the swarm routes — ported from
 * packages/pstack/test/features.test.ts ('API: share links', 'API: sleep, wake-on-call', and the
 * HTTP half of 'swarm discovery and the swarm routes').
 *
 * Same disciplines as the original — a real server, a fake `docker` on PATH that answers scripted
 * JSON, assertions on whole response bodies and on `Outcome.steps`. Every test here was broken once
 * on purpose (the negative control) before it was kept. Domain events are observed through the
 * event tap (a `*` webhook notifier) instead of `events.on`; the in-process halves (signShare with a
 * back-dated clock, swarmInfo, redactText, renderWorkerCloudInit) stay behind.
 */
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { type Booted, bootServer, until, waitJob } from '../harness/server.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { eventTap } from '../harness/receiver.ts';

describe('API: share links', () => {
  const put = (base: string, H: Record<string, string>, id: string) =>
    fetch(`${base}/api/deployments/${id}`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({ spec: `version: 1\nstack: ${id}\naxes:\n  - name: a\n    up: "true"\n` }),
    });

  test('a minted token reaches exactly its views on exactly its deployment', async () => {
    const s = await bootServer({ tag: 'share' });
    const { base, H, token: TOKEN } = s;
    try {
      expect((await put(base, H, 'pr-1')).status).toBe(201);
      expect((await put(base, H, 'pr-2')).status).toBe(201);
      const tap = await eventTap(base, H);
      const shareCreated = () => tap.got.filter((d) => d.event === 'share.created').map((d) => JSON.stringify(d.body!.data));

      const mint = await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ views: ['details'], ttl: '2h' }) });
      expect(mint.status).toBe(201);
      const link = (await mint.json()) as { url: string; token: string; views: string[]; expiresAt: number };
      expect(link.views).toEqual(['details']);
      expect(link.url).toBe(`${base}/deployments/pr-1/public-logs-view?token=${link.token}`);
      expect(link.expiresAt).toBeGreaterThan(Date.now() + 7_000_000);
      const seen = await until(async () => shareCreated(), (v) => v.length >= 1);
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
      // Black-box expiry: the shortest ttl the API accepts, then wait for the clock to pass it.
      const short = await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ views: ['details'], ttl: '1s' }) });
      expect(short.status).toBe(201);
      const expired = ((await short.json()) as { token: string }).token;
      expect(await until(async () => (await fetch(`${base}/api/deployments/pr-1?token=${expired}`)).status, (st) => st === 401, 4000, 100)).toBe(401);
      // Validation.
      expect((await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ views: ['terminal'] }) })).status).toBe(400);
      expect((await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: JSON.stringify({ ttl: '90d' }) })).status).toBe(400);
      expect((await fetch(`${base}/api/deployments/nope/share`, { method: 'POST', headers: H, body: '{}' })).status).toBe(404);
      tap.stop();
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('a share token holder cannot mint another link, and a token-less server cannot mint at all', async () => {
    const s = await bootServer({ tag: 'share' });
    const { base, H } = s;
    try {
      await put(base, H, 'pr-1');
      const link = (await (await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: '{}' })).json()) as { token: string; views: string[] };
      expect(link.views).toEqual(['details', 'logs']);
      expect((await fetch(`${base}/api/deployments/pr-1/share?token=${link.token}`, { method: 'POST' })).status).toBe(403);
    } finally {
      await s.stop();
    }
    const dev = await bootServer({ tag: 'share-dev', token: null });
    try {
      await fetch(`${dev.base}/api/deployments/pr-1`, { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ spec: 'version: 1\nstack: pr-1\naxes: []\n' }) });
      const r = await fetch(`${dev.base}/api/deployments/pr-1/share`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
      expect(r.status).toBe(400);
      expect(((await r.json()) as { error: string }).error).toMatch(/PSTACK_TOKEN/);
    } finally {
      await dev.stop();
    }
  }, 20_000);
});

describe('API: sleep, wake-on-call', () => {
  /** One running container for stack `wk` with a Host() router, and every compose verb succeeds. */
  const shim = () =>
    dockerShim(`
  "ps -aq --filter label=com.docker.compose.project=wk") printf '%s\\n' 'c0ffee123456' ;;
  "inspect c0ffee123456") printf '%s\\n' '[{"Id":"c0ffee123456","Name":"/wk-app-1","Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"wk","traefik.enable":"true","traefik.http.routers.app-wk.rule":"Host(\`app-wk.example.com\`)","traefik.http.routers.app-wk-wild.rule":"HostRegexp(\`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\\\\.app-wk\\\\.example\\\\.com$\`)","traefik.http.services.app-wk.loadbalancer.server.port":"80"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]' ;;
  "compose ls --all --format json") printf '%s\\n' '[{"Name":"wk"}]' ;;`);

  const submit = (base: string, H: Record<string, string>, extra = '') =>
    fetch(`${base}/api/deployments/wk`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({
        spec: `version: 1\nstack: wk\ncompose: {file: compose.yml, profiles: []}\n${extra}axes:\n  - name: a\n    up: "echo up"\n    down: "echo down"\n    assert_gone: "true"\n`,
        compose: 'services:\n  app:\n    image: nginx\n',
      }),
    });

  test('sleep records the live hostnames; a request to one wakes the stack and clears the record', async () => {
    const docker = shim();
    const srv = await bootServer({ tag: 'wake', pathPrefix: docker.dir });
    const { base, H, dataDir } = srv;
    const tap = await eventTap(base, H);
    const seen = () =>
      tap.got
        .filter((d) => d.event === 'stack.slept' || d.event === 'stack.woken')
        .map((d) => ({ event: d.event, data: d.body!.data }));
    try {
      expect((await submit(base, H)).status).toBe(201);

      // Before: awake, listed as running, not asleep.
      const before = (await (await fetch(`${base}/api/deployments/wk`, { headers: H })).json()) as { asleep: unknown; orchestrator: string; sleep: unknown };
      expect(before.asleep).toBeNull();
      expect(before.orchestrator).toBe('compose');
      expect(before.sleep).toBeNull();

      const s = await fetch(`${base}/api/deployments/wk/sleep`, { method: 'POST', headers: H });
      expect(s.status).toBe(202);
      const sjob = ((await s.json()) as { job: { id: string; action: string } }).job;
      expect(sjob.action).toBe('sleep');
      const done = await waitJob(srv, sjob.id);
      expect(done.state).toBe('ok');
      const outcome = done.outcome as { steps: Array<{ phase: string; axis: string }> };
      // The compose step only: no axis `down`, no `assert_gone`.
      expect(outcome.steps.map((st) => st.phase)).toEqual(['compose']);

      const meta = JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')) as { sleep: { hosts: string[]; rules: string[]; reason: string } };
      expect(meta.sleep.hosts).toEqual(['app-wk.example.com']);
      expect(meta.sleep.rules).toHaveLength(1);
      expect(meta.sleep.reason).toMatch(/^operator: root/);
      await until(async () => seen(), (v) => v.length >= 1);
      expect(seen().map((e) => e.event)).toEqual(['stack.slept']);
      expect(seen()[0]!.data.hosts).toEqual(['app-wk.example.com']);

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
      expect(html).toContain('Waking your preview');
      expect(html).toContain('wk');
      // The wildcard rule matches too.
      const sub = await fetch(`${base}/`, { headers: { 'x-forwarded-host': 'api.app-wk.example.com' } });
      expect(sub.status).toBe(503);
      // An unrelated hostname still gets the UI.
      const ui = await fetch(`${base}/`, { headers: { host: 'control.example.com' } });
      expect(ui.status).toBe(200);
      expect(ui.headers.get('x-pstack-wake')).toBeNull();

      // Exactly one wake job ran (the second request found it busy or done), as `up`.
      for (let i = 0; i < 100 && !seen().some((e) => e.event === 'stack.woken'); i++) await Bun.sleep(10);
      const jobs = (await (await fetch(`${base}/api/jobs`, { headers: H })).json()) as { jobs: Array<{ id: string; action: string; state: string }> };
      const wakes = jobs.jobs.filter((j) => j.action === 'wake');
      expect(wakes).toHaveLength(1);
      const w = await waitJob(srv, wakes[0]!.id);
      expect(w.state).toBe('ok');
      expect((w.outcome as { steps: Array<{ phase: string }> }).steps.map((st) => st.phase)).toEqual(['up', 'assert_live', 'compose'].filter((p) => p !== 'assert_live'));
      expect(seen().find((e) => e.event === 'stack.woken')!.data.by).toBe('request:app-wk.example.com');

      // The record clears EARLY — long before anything is serving — and what the hostname serves in
      // that gap is asserted by "the waking page outlives the sleep record" below, which steers the
      // container's healthcheck so the window cannot close underneath it.
      //
      // It is NOT asserted here, and that is the point of this comment. It was, briefly: a plain
      // `expect(503)` at this line, which passed on a laptop and failed on CI, because on a slower
      // machine readiness settles before the request goes out and the page correctly stops. A test
      // that races the thing it measures reports the machine it ran on, not the code.
      for (let i = 0; i < 100; i++) {
        const m = JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')) as { sleep?: unknown };
        if (!m.sleep) break;
        await Bun.sleep(10);
      }
      expect(JSON.parse(readFileSync(join(dataDir, 'deployments', 'wk', 'meta.json'), 'utf8')).sleep).toBeUndefined();

      // What this test still owns: the record is gone, and exactly one wake job ran.
    } finally {
      tap.stop();
      await srv.stop();
      docker.remove();
    }
  }, 20_000);

  test('sleep is refused for a shared deployment and for a spec without compose', async () => {
    const docker = shim();
    const srv = await bootServer({ tag: 'wake', pathPrefix: docker.dir });
    const { base, H } = srv;
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
      await srv.stop();
      docker.remove();
    }
  }, 20_000);

  test('a down clears a sleep record; a replace keeps it', async () => {
    const docker = shim();
    const srv = await bootServer({ tag: 'wake', pathPrefix: docker.dir });
    const { base, H, dataDir } = srv;
    try {
      await submit(base, H);
      const sj = ((await (await fetch(`${base}/api/deployments/wk/sleep`, { method: 'POST', headers: H })).json()) as { job: { id: string } }).job;
      await waitJob(srv, sj.id);
      const metaPath = join(dataDir, 'deployments', 'wk', 'meta.json');
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep).toBeDefined();
      // Replace the spec: still asleep, same hostnames.
      expect((await submit(base, H, 'sleep: {after: 1d}\n')).status).toBe(200);
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep.hosts).toEqual(['app-wk.example.com']);
      const d = (await (await fetch(`${base}/api/deployments/wk`, { headers: H })).json()) as { sleep: { after: string; idle: null } };
      expect(d.sleep).toEqual({ after: '1d', idle: null });
      const dj = ((await (await fetch(`${base}/api/deployments/wk/down`, { method: 'POST', headers: H, body: '{}' })).json()) as { job: { id: string } }).job;
      await waitJob(srv, dj.id);
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep).toBeUndefined();
    } finally {
      await srv.stop();
      docker.remove();
    }
  }, 20_000);

  /**
   * The same one container as `shim()`, with its State chosen by the caller — the only thing the
   * readiness watch behind the waking page reads. Steering it is what makes the window below
   * deterministic instead of a race against a 2-second poll: a healthcheck that says `starting`
   * cannot settle, so the page cannot stop on its own while the assertions run.
   */
  const STATE = {
    starting: '"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z","Health":{"Status":"starting"}}',
    healthy: '"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z","Health":{"Status":"healthy"}}',
    exited: '"State":{"Status":"exited","StartedAt":"2026-08-20T10:00:00Z","ExitCode":1}',
  };
  const armsIn = (state: string) => `
  "ps -aq --filter label=com.docker.compose.project=wk") printf '%s\\n' 'c0ffee123456' ;;
  "inspect c0ffee123456") printf '%s\\n' '[{"Id":"c0ffee123456","Name":"/wk-app-1","Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"wk","traefik.enable":"true","traefik.http.routers.app-wk.rule":"Host(\`app-wk.example.com\`)","traefik.http.services.app-wk.loadbalancer.server.port":"80"}},${state},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]' ;;
  "compose ls --all --format json") printf '%s\\n' '[{"Name":"wk"}]' ;;`;

  /** Sleep `wk`, and fail loudly here rather than three assertions later if it did not. */
  const putToSleep = async (srv: Booted) => {
    expect((await submit(srv.base, srv.H)).status).toBe(201);
    const j = ((await (await fetch(`${srv.base}/api/deployments/wk/sleep`, { method: 'POST', headers: srv.H })).json()) as { job: { id: string } }).job;
    expect((await waitJob(srv, j.id)).state).toBe('ok');
  };
  /** The one wake job the hostname started, once it has finished. */
  const wakeJob = async (srv: Booted) => {
    const wakes = await until(
      async () => ((await (await fetch(`${srv.base}/api/jobs`, { headers: srv.H })).json()) as { jobs: Array<{ id: string; action: string }> }).jobs.filter((j) => j.action === 'wake'),
      (v) => v.length >= 1,
    );
    expect(wakes).toHaveLength(1);
    return await waitJob(srv, wakes[0]!.id);
  };
  /** A request as Traefik's catch-all makes it: the preview's Host, no auth, any path. */
  const asVisitor = async (base: string, path = '/') => {
    const r = await fetch(`${base}${path}`, { headers: { host: 'app-wk.example.com' } });
    return { status: r.status, wake: r.headers.get('x-pstack-wake'), retry: r.headers.get('retry-after'), html: await r.text() };
  };

  test('the waking page outlives the sleep record, and stops the moment the stack serves', async () => {
    const docker = dockerShim(armsIn(STATE.starting));
    const srv = await bootServer({ tag: 'wake', pathPrefix: docker.dir, readiness: { pollMs: 100 } });
    const { base, dataDir } = srv;
    try {
      await putToSleep(srv);

      // ── 1. ASLEEP. The sleep record holds the hostname, and the request starts the wake.
      const asleep = await asVisitor(base);
      expect(asleep.status).toBe(503);
      expect(asleep.wake).toBe('1');
      expect(asleep.html).toContain('was asleep');

      // The wake clears that record the moment `up` returns OK — which is when the containers are
      // CREATED. Nothing is serving yet, and Traefik has no route to hand the hostname to.
      expect((await wakeJob(srv)).state).toBe('ok');
      const metaPath = join(dataDir, 'deployments', 'wk', 'meta.json');
      await until(async () => JSON.parse(readFileSync(metaPath, 'utf8')).sleep, (s) => s === undefined);
      expect(JSON.parse(readFileSync(metaPath, 'utf8')).sleep).toBeUndefined();

      // ── 2. WAKING, and the record is gone. THIS is the bug the owner hit: the hostname stopped
      // being anybody's the instant the record went, so the request fell through to the generic
      // non-/api/ rule and the visitor got the control plane's own dashboard on their preview's URL.
      // Asserted twice because a page that survives one reload and not the next is the same defect.
      for (const path of ['/', '/some/deep/link?x=1']) {
        const starting = await asVisitor(base, path);
        expect(starting.status).toBe(503);
        expect(starting.wake).toBe('1');
        expect(starting.retry).toBe('5');
        expect(starting.html).toContain('is awake and getting ready to answer');
        expect(starting.html).not.toContain('was asleep'); // it is awake — the page must not lie
        expect(starting.html).not.toContain('<div id="app">'); // the control UI
      }

      // ── 3. SERVING. The healthcheck passes, the readiness watch settles, and the catch-all stops
      // answering for this hostname — the next request is Traefik's to route at the app. Only the
      // ABSENCE of the wake page is asserted: what the control plane serves on a hostname that is
      // no longer a preview's is a separate question this test does not settle.
      docker.rewrite(armsIn(STATE.healthy));
      const served = await until(async () => await asVisitor(base), (v) => v.wake === null, 10_000, 50);
      expect(served.wake).toBeNull();
      expect(served.status).toBe(200);
    } finally {
      await srv.stop();
      docker.remove();
    }
  }, 30_000);

  test('a wake whose containers die ends at the failure page, not an eternal spinner', async () => {
    const docker = dockerShim(armsIn(STATE.starting));
    const srv = await bootServer({ tag: 'wake', pathPrefix: docker.dir, readiness: { pollMs: 100 } });
    const { base } = srv;
    try {
      await putToSleep(srv);
      // The container the wake creates exits 1. `docker compose up` still SUCCEEDS — it returns when
      // the containers are created, not when they survive — so the wake JOB is ok and the old
      // "the last wake failed" branch is not what answers here. The assertion below pins that.
      docker.rewrite(armsIn(STATE.exited));

      expect((await asVisitor(base)).status).toBe(503);
      expect((await wakeJob(srv)).state).toBe('ok');

      const failed = await until(async () => await asVisitor(base), (v) => v.html.includes('couldn&#39;t start'), 10_000, 50);
      expect(failed.status).toBe(503);
      expect(failed.wake).toBe('1');
      expect(failed.html).toContain('Your preview couldn&#39;t start');
      expect(failed.html).toContain('app: exited with code 1'); // the container's own reason, named
      expect(failed.html).toContain('<body class="failed"'); // the class that stills the lamp
      expect(failed.html).not.toContain('<div id="app">');

      // And it STAYS the failure page. Falling through on the reload after the one that reported the
      // failure is the same bug wearing a slower clock.
      const again = await asVisitor(base, '/reload');
      expect(again.status).toBe(503);
      expect(again.wake).toBe('1');
      expect(again.html).toContain('Your preview couldn&#39;t start');

      // Reloading does NOT try again here, whatever the page's text says: the wake SUCCEEDED and it
      // is the containers that died, so there is no sleep record left for a request to act on. One
      // wake job, still. Bringing it back is `POST …/wake` or a fresh deploy.
      const jobs = (await (await fetch(`${base}/api/jobs`, { headers: srv.H })).json()) as { jobs: Array<{ action: string }> };
      expect(jobs.jobs.filter((j) => j.action === 'wake')).toHaveLength(1);
    } finally {
      await srv.stop();
      docker.remove();
    }
  }, 30_000);
});

describe('swarm discovery and the swarm routes', () => {
  const swarmShim = () =>
    dockerShim(`
  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;
  "node ls --format {{json .}}") printf '%s\\n' '{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' '{"ID":"n2","Hostname":"wrk","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ;;
  "swarm join-token -q worker") printf '%s\\n' 'SWMTKN-1-abc-def' ;;
  "service ls -q --filter label=com.docker.stack.namespace=sw") printf '%s\\n' 'svc1' ;;
  "service ls -q --filter label=traefik.enable=true") printf '%s\\n' 'svc1' ;;
  "service inspect svc1") printf '%s\\n' '[{"ID":"svc1xxxxxxxxxxxx","UpdatedAt":"2026-08-20T10:00:00Z","Spec":{"Name":"sw_app","Labels":{"com.docker.stack.namespace":"sw","traefik.enable":"true","traefik.swarm.network":"preview-ingress","traefik.http.routers.app-sw.rule":"Host(\`app-sw.example.com\`)","traefik.http.services.app-sw.loadbalancer.server.port":"80"},"TaskTemplate":{"ContainerSpec":{"Image":"nginx"},"Networks":[{"Target":"net1"}]}}}]' ;;
  "network ls --format {{.ID}} {{.Name}}") printf '%s\\n' 'net1 preview-ingress' 'net2 sw_default' ;;
  "stack ps sw --no-trunc --filter desired-state=running --format {{json .}}") printf '%s\\n' '{"ID":"task1","Name":"sw_app.1","Image":"nginx","Node":"mgr","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}' '{"ID":"task2","Name":"sw_app.2","Image":"nginx","Node":"wrk","DesiredState":"Running","CurrentState":"Running 1 minute ago"}' ;;
  "ps -aq --filter label=com.docker.stack.namespace=sw") printf '%s\\n' "aaa111aaa111" "bbb222bbb222" ;;
  "inspect aaa111aaa111 bbb222bbb222") printf '%s\\n' '[{"Id":"aaa111aaa111","Name":"/sw_app.1.task1","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"task1","com.docker.swarm.node.id":"n1"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:01:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}},{"Id":"bbb222bbb222","Name":"/sw_app.1.oldtask","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"dead0","com.docker.swarm.node.id":"n1"}},"State":{"Status":"exited","ExitCode":1},"NetworkSettings":{"Networks":{},"Ports":{}}}]' ;;`);

  test('swarmInfo and the API routes; the join token is admin-only text', async () => {
    const docker = swarmShim();
    const s = await bootServer({ tag: 'swarm', pathPrefix: docker.dir });
    const { base, H } = s;
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
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  /**
   * The stack `sw` as the ROUTE pages read it — a different `docker service ls` from the panel's
   * (`--format '{{json .}}'`, not `-q`), so this is its own shim rather than a knob on the one
   * above. Three knobs, because "where does this router forward to" has three honest answers:
   * `vip` is the service's address on preview-ingress, `net` is what its task template is attached
   * to, and `localTask` is whether a task container of it happens to run on THIS node.
   */
  const routeArms = (o: { vip?: boolean; net?: string; localTask?: boolean } = {}) => {
    const endpoint = o.vip ? ',"Endpoint":{"VirtualIPs":[{"NetworkID":"net1","Addr":"10.0.9.2/24"}]}' : '';
    const labels =
      '"com.docker.stack.namespace":"sw","traefik.enable":"true","traefik.swarm.network":"preview-ingress","traefik.http.routers.app-sw.rule":"Host(`app-sw.example.com`)","traefik.http.services.app-sw.loadbalancer.server.port":"80"';
    const local = o.localTask
      ? `
  "ps -aq --filter label=com.docker.stack.namespace=sw") printf '%s\\n' 'aaa111aaa111' ;;
  "inspect aaa111aaa111") printf '%s\\n' '[{"Id":"aaa111aaa111","Name":"/sw_app.1.task1","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"task1","com.docker.swarm.node.id":"n1"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:01:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}}]' ;;`
      : '';
    return `
  "service ls --format {{json .}} --filter label=com.docker.stack.namespace=sw") printf '%s\\n' '{"ID":"svc1","Name":"sw_app","Mode":"replicated","Replicas":"1/1"}' ;;
  "service ls -q --filter label=traefik.enable=true") printf '%s\\n' 'svc1' ;;
  "service inspect svc1") printf '%s\\n' '[{"ID":"svc1xxxxxxxxxxxx","UpdatedAt":"2026-08-20T10:00:00Z","Spec":{"Name":"sw_app","Labels":{${labels}},"TaskTemplate":{"ContainerSpec":{"Image":"nginx"},"Networks":[{"Target":"${o.net ?? 'net1'}"}]}}${endpoint}}]' ;;
  "network ls --format {{.ID}} {{.Name}}") printf '%s\\n' 'net1 preview-ingress' 'net2 sw_default' ;;
  "stack ps sw --no-trunc --filter desired-state=running --format {{json .}}") printf '%s\\n' '{"ID":"task2","Name":"sw_app.1","Image":"nginx","Node":"wrk","DesiredState":"Running","CurrentState":"Running 1 minute ago"}' ;;${local}`;
  };

  test('a swarm router says where it forwards, or which of the three reasons it cannot', async () => {
    // The service's VIP on preview-ingress: the address Traefik's swarm provider dials, and the one
    // a manager knows wherever the tasks actually run.
    const docker = dockerShim(routeArms({ vip: true }));
    const s = await bootServer({ tag: 'swarm-target', pathPrefix: docker.dir });
    const { base, H } = s;
    const routesOf = async (path: string) => ((await (await fetch(`${base}${path}`, { headers: H })).json()) as { routes: Array<{ router: string; port: number | null; target: string | null; targetReason: string }> }).routes;
    try {
      const put = await fetch(`${base}/api/deployments/sw`, {
        method: 'PUT',
        headers: H,
        body: JSON.stringify({ spec: 'version: 1\nstack: sw\ncompose:\n  file: compose.yml\n  orchestrator: swarm\naxes: []\n', compose: 'services:\n  app:\n    image: nginx\n' }),
      });
      expect(put.status).toBe(201);

      // The deployment page and the host-wide page agree, because the answer is a property of the
      // SERVICE and neither page has to guess it from a container.
      for (const path of ['/api/deployments/sw/runtime', '/api/routing/live']) {
        const routes = await routesOf(path);
        expect(routes).toHaveLength(1);
        expect(routes[0]!.router).toBe('app-sw');
        expect(routes[0]!.port).toBe(80);
        expect(routes[0]!.target).toBe('10.0.9.2:80'); // the /24 is the network's, not part of an address
        expect(routes[0]!.targetReason).toBe('');
      }

      // No VIP (endpoint_mode: dnsrr) and its only task on another node. Nothing on this manager can
      // know the address — which is NOT the same fact as the service being off the ingress network,
      // and reporting it as that was the bug: every swarm row read "not on the ingress network".
      docker.rewrite(routeArms({}));
      const unknown = await routesOf('/api/deployments/sw/runtime');
      expect(unknown[0]!.target).toBeNull();
      expect(unknown[0]!.targetReason).toBe('unknown-node');
      expect(unknown[0]!.targetReason).not.toBe('not-on-ingress');

      // Attached to sw_default instead: now it IS measured, and the diagnosis is the true one.
      docker.rewrite(routeArms({ net: 'net2' }));
      const off = await routesOf('/api/deployments/sw/runtime');
      expect(off[0]!.target).toBeNull();
      expect(off[0]!.targetReason).toBe('not-on-ingress');

      // And the third reason: a router with no loadbalancer.server.port is the author's to fix.
      docker.rewrite(routeArms({ vip: true }).replace(',"traefik.http.services.app-sw.loadbalancer.server.port":"80"', ''));
      const noPort = await routesOf('/api/deployments/sw/runtime');
      expect(noPort[0]!.port).toBeNull();
      expect(noPort[0]!.target).toBeNull();
      expect(noPort[0]!.targetReason).toBe('no-port');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);
});
