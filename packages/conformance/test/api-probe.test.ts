/**
 * `GET /api/probe/<id>` — the UNAUTHENTICATED "is this preview serving?" route.
 *
 * Every assertion here is one a caller with **no token** makes, because that is the whole point:
 * a CI job polls this instead of the preview's own hostname, which under HTTP-01 is waiting on ACME
 * rather than on the app. So the tests send no `Authorization` header at all — a suite that quietly
 * passed `s.H` would prove nothing about the property the route exists for.
 *
 * Four things are pinned, and they are the four that could each turn this convenience into a
 * liability:
 *
 *   1. **THERE IS NEVER A BODY.** Not on success, not on 404, not on an upstream that returns
 *      content. `docs/secret-exposure.md` is this project's record of what an unauthenticated read
 *      cost it once; an endpoint structurally incapable of returning bytes cannot repeat it.
 *   2. **A SLEEPING STACK IS NOT WOKEN.** An unauthenticated route that starts a deploy is an
 *      unauthenticated deploy. The job list is checked afterwards, because "it answered 503" and
 *      "it did not start anything" are different claims and only the second one matters here.
 *   3. **The upstream's status is passed through**, so `curl -w '%{http_code}'` is the client. The
 *      shim points the router's target at a real local server, so this is the genuine dial — not a
 *      mock of one.
 *   4. **`PSTACK_PROBE=off` removes the route**, and removing it means falling through to the gate
 *      (401), not a special-cased refusal.
 */
import { describe, expect, test } from 'bun:test';
import { dockerShim } from '../harness/docker-shim.ts';
import { bootServer, type Booted } from '../harness/server.ts';

/** No Authorization header, ever. That is the property under test. */
const probe = (s: Booted, id: string, q = '') => fetch(`${s.base}/api/probe/${id}${q}`);

const submit = (s: Booted, id: string, stack: string) =>
  fetch(`${s.base}/api/deployments/${id}`, {
    method: 'PUT',
    headers: s.H,
    body: JSON.stringify({
      spec: `version: 1\nstack: ${stack}\ncompose: {file: compose.yml, profiles: []}\naxes:\n  - name: a\n    up: "echo up"\n    down: "echo down"\n    assert_gone: "true"\n`,
      compose: 'services:\n  app:\n    image: nginx\n',
    }),
  });

/**
 * A shim whose one container carries a Traefik router whose target is `127.0.0.1:<port>` — the
 * address the probe will actually dial. Everything else is the standard running-container shape.
 */
const shimFor = (stack: string, port: number | undefined) =>
  dockerShim(`
  "ps -aq --filter label=com.docker.compose.project=${stack}") printf '%s\\n' 'c0ffee123456' ;;
  "inspect c0ffee123456") printf '%s\\n' '[{"Id":"c0ffee123456","Name":"/${stack}-app-1","Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"${stack}","traefik.enable":"true","traefik.http.routers.app-${stack}.rule":"Host(\`app-${stack}.example.com\`)","traefik.http.routers.app-${stack}.service":"app-${stack}","traefik.http.services.app-${stack}.loadbalancer.server.port":"${port}"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"127.0.0.1"}},"Ports":{}}}]' ;;
  "compose ls --all --format json") printf '%s\\n' '[{"Name":"${stack}"}]' ;;`);

/** Asserts the response carries no bytes, whatever else it says. */
const expectMute = async (r: Response): Promise<void> => {
  expect(r.headers.get('content-length')).toBe('0');
  expect(await r.text()).toBe('');
};

describe('the unauthenticated probe', () => {
  test('an unknown deployment is 404 `unknown`, with no token and no body', async () => {
    // negative control: drop the `s.opts.ProbeOff` guard's sibling — the id check — and let the
    // route resolve anything; a missing deployment then 500s with an error BODY, which is the one
    // thing this route may never produce.
    const s = await bootServer({ tag: 'probe-404' });
    try {
      const r = await probe(s, 'nope');
      expect(r.status).toBe(404);
      expect(r.headers.get('x-pstack-probe')).toBe('unknown');
      await expectMute(r);
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('a live container answers, and ITS status is the answer', async () => {
    // negative control: return a fixed 200 instead of `resp.StatusCode` — the 418 below becomes
    // 200 and the route stops reporting anything about the app at all.
    const upstream = Bun.serve({ port: 0, fetch: () => new Response('teapot body', { status: 418 }) });
    const docker = shimFor('pb', upstream.port);
    const s = await bootServer({ tag: 'probe-up', pathPrefix: docker.dir });
    try {
      expect((await submit(s, 'pb', 'pb')).status).toBe(201);
      const r = await probe(s, 'pb');
      expect(r.status).toBe(418);
      expect(r.headers.get('x-pstack-probe')).toBe('upstream');
      // The upstream sent a body. This route did not forward it — that is the whole rule.
      await expectMute(r);
    } finally {
      upstream.stop(true);
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('a deployment with nothing running is 503 `no-target`, never a dial', async () => {
    // negative control: fall back to some default address when no router has a target — the probe
    // then dials whatever that default is and reports someone else's status as this stack's.
    const s = await bootServer({ tag: 'probe-none' });
    try {
      expect((await submit(s, 'idle', 'idle')).status).toBe(201);
      const r = await probe(s, 'idle');
      expect(r.status).toBe(503);
      expect(['no-target', 'unresolved']).toContain(r.headers.get('x-pstack-probe') ?? '');
      await expectMute(r);
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('a sleeping stack answers `asleep` and is STILL asleep afterwards — no job was started', async () => {
    // negative control: reuse the deployment route's resolution helper (which runs the wake path)
    // instead of checking `dep.Sleep` first — the probe then starts a `wake` job, and an
    // unauthenticated caller has just deployed.
    const upstream = Bun.serve({ port: 0, fetch: () => new Response('ok') });
    const docker = shimFor('nap', upstream.port);
    const s = await bootServer({ tag: 'probe-sleep', pathPrefix: docker.dir });
    try {
      expect((await submit(s, 'nap', 'nap')).status).toBe(201);
      expect((await fetch(`${s.base}/api/deployments/nap/sleep`, { method: 'POST', headers: s.H })).status).toBe(202);
      // The sleep job has to finish before the record exists.
      await Bun.sleep(1200);
      const jobsBefore = ((await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: unknown[] }).jobs.length;

      const r = await probe(s, 'nap');
      expect(r.status).toBe(503);
      expect(r.headers.get('x-pstack-probe')).toBe('asleep');
      await expectMute(r);

      const after = (await (await fetch(`${s.base}/api/deployments/nap`, { headers: s.H })).json()) as { asleep: unknown };
      expect(after.asleep).not.toBeNull();
      const jobsAfter = ((await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: unknown[] }).jobs.length;
      expect(jobsAfter).toBe(jobsBefore);
    } finally {
      upstream.stop(true);
      await s.stop();
      docker.remove();
    }
  }, 30_000);

  test('PSTACK_PROBE=off falls through to the gate — 401, not a special refusal', async () => {
    // negative control: make the off switch answer 404 itself — the route would then be
    // distinguishable from an unknown path, which is the one thing turning it off is meant to
    // prevent.
    const s = await bootServer({ tag: 'probe-off', env: { PSTACK_PROBE: 'off' } });
    try {
      const r = await probe(s, 'anything');
      expect(r.status).toBe(401);
    } finally {
      await s.stop();
    }
  }, 20_000);
});
