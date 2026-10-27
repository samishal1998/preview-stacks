/**
 * `/api/custom-routes` and `pstack route` — a stable alias for a stack, over HTTP and the CLI.
 *
 * The Go tests prove the renderer and the handlers. What only a real process shows is that the
 * routers land in Traefik's watched directory as `pstack-routes.yml`, that the file follows every
 * write (a move rewrites it in one call, an unassign or a forget takes the routers away), and that
 * the CLI reaches the same routes and says the same things.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync } from 'node:fs';
import { NO_CLI } from '../harness/impl.ts';
import { runCli } from '../harness/cli.ts';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';
import { bootServer, tmpd, until, type Booted } from '../harness/server.ts';

const DOMAIN = 'preview.example.com';
const COMPOSE = 'services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: "80"}\n';
const spec = (stack: string) =>
  `version: 1\nstack: ${stack}\nenv:\n  PREVIEW_DOMAIN: ${DOMAIN}\ncompose:\n  file: compose.yml\n  profiles: []\naxes: []\n`;

/** A server with pr-1 (stack myapp-pr-1) and pr-2 (myapp-pr-2), each serving `web-<stack>.<domain>`. */
async function boot(tag: string) {
  const routingDir = tmpd(`${tag}-routing`);
  mkdirSync(routingDir, { recursive: true });
  const docker = dockerShim(ALWAYS_OK);
  const s = await bootServer({ tag, routingDir, pathPrefix: docker.dir, readiness: { pollMs: 20, timeoutMs: 200 } });
  for (const n of ['1', '2']) {
    const put = await j(s, 'PUT', `/api/deployments/pr-${n}`, { spec: spec(`myapp-pr-${n}`), compose: COMPOSE });
    expect(`put pr-${n} → ${put.status}`).toBe(`put pr-${n} → 201`);
  }
  return { s, stop: async () => { await s.stop(); docker.remove(); } };
}

const j = (s: Booted, method: string, path: string, body?: unknown) =>
  fetch(`${s.base}${path}`, { method, headers: s.H, body: body === undefined ? undefined : JSON.stringify(body) });

/** Status and parsed body, so one expect pins both. */
const call = async (s: Booted, method: string, path: string, body?: unknown) => {
  const r = await j(s, method, path, body);
  return { status: r.status, body: (await r.json()) as unknown };
};

const route = (name: string, n: string | null) => ({
  name,
  deployment: n === null ? null : `pr-${n}`,
  stack: n === null ? null : `myapp-pr-${n}`,
  hosts: n === null ? [] : [`web-${name}.${DOMAIN}`],
  blocked: [],
  createdAt: expect.any(Number),
  updatedAt: expect.any(Number),
});

/** pstack-routes.yml as Traefik reads it, or null when it is not there (the routing API answers 400). */
const routesFile = async (s: Booted): Promise<string | null> => {
  const r = await j(s, 'GET', '/api/routing/pstack-routes.yml');
  if (r.status === 400) return null;
  expect(r.status).toBe(200);
  return ((await r.json()) as { content: string }).content;
};

const settle = async (s: Booted, id: string) => {
  const job = await until(
    async () => ((await (await j(s, 'GET', `/api/jobs/${id}`)).json()) as { job?: { state: string } }).job,
    (v) => !!v && v.state !== 'queued' && v.state !== 'running',
    20_000,
  );
  expect(job?.state).toBe('ok');
};

describe('custom routes over HTTP', () => {
  test('create, assign, held, a forced move, unassign and delete, with the file after each step', async () => {
    // negative control: in routes_customroutes.go customRouteAssign, drop the `!force` from the
    // held check — the plain PUT moves the route instead of answering 409.
    const { s, stop } = await boot('cr-flow');
    try {
      // Unassigned: listed, serving nothing, and no file at all.
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'checkout' })).toEqual({ status: 201, body: route('checkout', null) });
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({ status: 200, body: [route('checkout', null)] });
      expect(await routesFile(s)).toBeNull();

      // Assigned: hosts are the alias twins of the stack's own, and the routers are written.
      expect(await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'pr-1' })).toEqual({ status: 200, body: route('checkout', '1') });
      const first = await routesFile(s);
      expect(first).toContain('pstack-route-checkout-web:');
      expect(first).toContain('Host(`web-checkout.preview.example.com`)');
      expect(first).toContain('service: "web-myapp-pr-1@docker"');

      // The same deployment again is a no-op 200.
      expect(await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'pr-1' })).toEqual({ status: 200, body: route('checkout', '1') });

      // Held by pr-1: a plain assign to pr-2 is refused, naming the holder, and nothing moves.
      expect(await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'pr-2' })).toEqual({
        status: 409,
        body: { error: 'checkout is assigned to myapp-pr-1', assignedTo: { deployment: 'pr-1', stack: 'myapp-pr-1' } },
      });
      expect(await routesFile(s)).toBe(first);

      // force: one call, and the file names only the new target.
      expect(await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'pr-2', force: true })).toEqual({ status: 200, body: route('checkout', '2') });
      const moved = await routesFile(s);
      expect(moved).toContain('service: "web-myapp-pr-2@docker"');
      expect(moved).not.toContain('web-myapp-pr-1@docker');
      expect(moved?.match(/pstack-route-checkout-web:/g)).toHaveLength(1);

      // Unassign keeps the route and takes the routers away.
      expect(await call(s, 'DELETE', '/api/custom-routes/checkout/assignment')).toEqual({ status: 200, body: route('checkout', null) });
      expect(await routesFile(s)).toBeNull();

      // Delete an assigned one: the route and its routers both go.
      expect((await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'pr-1' })).status).toBe(200);
      expect(await routesFile(s)).toContain('web-myapp-pr-1@docker');
      expect(await call(s, 'DELETE', '/api/custom-routes/checkout')).toEqual({ status: 200, body: { deleted: 'checkout' } });
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({ status: 200, body: [] });
      expect(await routesFile(s)).toBeNull();

      // Created assigned, in one call.
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'cart', deployment: 'pr-2' })).toEqual({ status: 201, body: route('cart', '2') });
      expect(await routesFile(s)).toContain('Host(`web-cart.preview.example.com`)');
    } finally {
      await stop();
    }
  }, 60_000);

  test('a deployment that later claims an alias host wins it; the route keeps its other hosts', async () => {
    // negative control: in routes_deploy.go putDeployment, drop the reconcileCustomRoutes call —
    // the route still serves web-checkout and the file keeps a second router for that rule.
    const { s, stop } = await boot('cr-later');
    try {
      const two = `${COMPOSE}  api:\n    image: nginx\n    labels: {pstack.routing.port: "80"}\n`;
      expect((await j(s, 'PUT', '/api/deployments/pr-1', { spec: spec('myapp-pr-1'), compose: two })).status).toBe(200);
      expect((await call(s, 'POST', '/api/custom-routes', { name: 'checkout', deployment: 'pr-1' })).status).toBe(201);
      expect(await routesFile(s)).toContain('Host(`web-checkout.preview.example.com`)');

      const claim = `services:\n  shop:\n    image: nginx\n    labels: {pstack.routing.port: "80", pstack.routing.host: web-checkout.${DOMAIN}}\n`;
      expect((await j(s, 'PUT', '/api/deployments/pr-3', { spec: spec('other'), compose: claim })).status).toBe(201);
      const blocked = { host: `web-checkout.${DOMAIN}`, by: { kind: 'deployment', name: 'other' } };
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({
        status: 200,
        body: [{ ...route('checkout', '1'), hosts: [`api-checkout.${DOMAIN}`], blocked: [blocked] }],
      });
      // The deployment's own label router is the only one for that rule: none in the file.
      const file = await routesFile(s);
      expect(file).not.toContain(`web-checkout.${DOMAIN}`);
      expect(file).toContain('Host(`api-checkout.preview.example.com`)');

      // The winner goes, the host comes back.
      expect((await j(s, 'DELETE', '/api/deployments/pr-3')).status).toBe(200);
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({
        status: 200,
        body: [{ ...route('checkout', '1'), hosts: [`web-checkout.${DOMAIN}`, `api-checkout.${DOMAIN}`] }],
      });
      expect(await routesFile(s)).toContain('Host(`web-checkout.preview.example.com`)');
    } finally {
      await stop();
    }
  }, 60_000);

  test('bad, reserved, taken and stack names; unknown deployments and routes', async () => {
    // negative control: in routes_customroutes.go customRouteCreate, delete the stack-name loop —
    // `myapp-pr-1` is created (201) and its alias host web-myapp-pr-1.<domain> collides with the
    // stack's own.
    const { s, stop } = await boot('cr-names');
    try {
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'Bad_Name' })).toEqual({
        status: 400,
        body: { error: 'bad route name Bad_Name: a DNS label, a-z 0-9 and -, at most 40' },
      });
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'api' })).toEqual({ status: 400, body: { error: 'api is reserved' } });
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'myapp-pr-1' })).toEqual({ status: 409, body: { error: 'myapp-pr-1 is the stack of pr-1' } });
      expect((await call(s, 'POST', '/api/custom-routes', { name: 'checkout' })).status).toBe(201);
      expect(await call(s, 'POST', '/api/custom-routes', { name: 'checkout', force: true })).toEqual({ status: 409, body: { error: 'checkout exists' } });

      expect(await call(s, 'POST', '/api/custom-routes', { name: 'cart', deployment: 'nope' })).toEqual({ status: 404, body: { error: 'no such deployment: nope' } });
      expect(await call(s, 'PUT', '/api/custom-routes/checkout/assignment', { deployment: 'nope' })).toEqual({ status: 404, body: { error: 'no such deployment: nope' } });
      expect(await call(s, 'PUT', '/api/custom-routes/nope/assignment', { deployment: 'pr-1' })).toEqual({ status: 404, body: { error: 'no such route: nope' } });
      expect(await call(s, 'DELETE', '/api/custom-routes/nope/assignment')).toEqual({ status: 404, body: { error: 'no such route: nope' } });
      expect(await call(s, 'DELETE', '/api/custom-routes/nope')).toEqual({ status: 404, body: { error: 'no such route: nope' } });

      // None of the refusals left anything behind.
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({ status: 200, body: [route('checkout', null)] });

      // Reserved: the generic routing API may not write the file whose routers ARE the table.
      expect(await call(s, 'PUT', '/api/routing/pstack-routes.yml', { content: 'http: {}\n' })).toEqual({
        status: 400,
        body: { error: 'pstack-routes.yml is managed by pstack, not by hand: it is rendered from the custom_routes table. Use /api/custom-routes.' },
      });
    } finally {
      await stop();
    }
  }, 60_000);

  test('forget and DELETE /api/deployments both unassign, and keep the route', async () => {
    // negative control: in routes_deploy.go removeDeployment, drop the UnassignDeploymentRoutes
    // call — both routes stay assigned to a deployment that no longer exists, and the file keeps
    // their routers.
    const { s, stop } = await boot('cr-forget');
    try {
      expect((await call(s, 'POST', '/api/custom-routes', { name: 'one', deployment: 'pr-1' })).status).toBe(201);
      expect((await call(s, 'POST', '/api/custom-routes', { name: 'two', deployment: 'pr-2' })).status).toBe(201);
      expect(await routesFile(s)).toContain('web-myapp-pr-1@docker');

      const down = await j(s, 'POST', '/api/deployments/pr-1/down', { forget: true });
      expect(down.status).toBe(202);
      await settle(s, ((await down.json()) as { job: { id: string } }).job.id);
      expect((await j(s, 'GET', '/api/deployments/pr-1')).status).toBe(404);
      const afterForget = await routesFile(s);
      expect(afterForget).not.toContain('web-myapp-pr-1@docker');
      expect(afterForget).toContain('web-myapp-pr-2@docker');

      expect((await j(s, 'DELETE', '/api/deployments/pr-2')).status).toBe(200);
      expect(await call(s, 'GET', '/api/custom-routes')).toEqual({ status: 200, body: [route('one', null), route('two', null)] });
      expect(await routesFile(s)).toBeNull();
    } finally {
      await stop();
    }
  }, 60_000);

  test('a share principal never reaches the routes', async () => {
    // negative control: in principal.go shareAllows, let `/api/custom-routes` through beside
    // `/api/auth/me` — the GET answers 200 to a share token.
    const { s, stop } = await boot('cr-share');
    try {
      const mint = await j(s, 'POST', '/api/deployments/pr-1/share', {});
      expect(mint.status).toBe(201);
      const q = `?token=${((await mint.json()) as { token: string }).token}`;
      // The token works where shares are allowed, so the 403s below are about the route.
      expect((await fetch(`${s.base}/api/deployments/pr-1${q}`)).status).toBe(200);
      for (const [method, path] of [
        ['GET', '/api/custom-routes'],
        ['POST', '/api/custom-routes'],
        ['PUT', '/api/custom-routes/checkout/assignment'],
        ['DELETE', '/api/custom-routes/checkout/assignment'],
        ['DELETE', '/api/custom-routes/checkout'],
      ] as [string, string][]) {
        expect(`${method} ${path} → ${(await fetch(`${s.base}${path}${q}`, { method })).status}`).toBe(`${method} ${path} → 403`);
      }
    } finally {
      await stop();
    }
  }, 60_000);
});

describe.skipIf(NO_CLI)('pstack route', () => {
  const env = (s: Booted) => ({ PSTACK_API_URL: s.base, PSTACK_TOKEN: s.token ?? undefined });

  test('create, ls, the held refusal, a forced move, unassign and rm', async () => {
    // negative control: in internal/cli/route.go print apiError's line for every 409 — the held
    // refusal loses "(--force moves it)". Also: drop Force from the assign body — the forced move
    // exits 1 with the held line.
    const { s, stop } = await boot('cr-cli');
    try {
      const create = await runCli(['route', 'create', 'checkout', 'pr-1'], { env: env(s) });
      expect([create.code, create.stdout, create.stderr]).toEqual([0, 'checkout  myapp-pr-1  web-checkout.preview.example.com\n', '']);

      expect((await runCli(['route', 'create', 'spare'], { env: env(s) })).stdout).toBe('spare  (unassigned)\n');
      const ls = await runCli(['route', 'ls'], { env: env(s) });
      expect([ls.code, ls.stdout]).toEqual([0, 'checkout  myapp-pr-1  web-checkout.preview.example.com\nspare  (unassigned)\n']);

      const held = await runCli(['route', 'assign', 'checkout', 'pr-2'], { env: env(s) });
      expect([held.code, held.stdout, held.stderr]).toEqual([1, '', 'pstack: checkout is assigned to myapp-pr-1 (--force moves it)\n']);

      const moved = await runCli(['route', 'assign', 'checkout', 'pr-2', '--force'], { env: env(s) });
      expect([moved.code, moved.stdout]).toEqual([0, 'checkout  myapp-pr-2  web-checkout.preview.example.com\n']);
      expect(await routesFile(s)).toContain('web-myapp-pr-2@docker');

      const json = await runCli(['route', 'ls', '--json'], { env: env(s) });
      expect(JSON.parse(json.stdout)).toEqual([route('checkout', '2'), route('spare', null)]);

      expect((await runCli(['route', 'unassign', 'checkout'], { env: env(s) })).stdout).toBe('checkout  (unassigned)\n');
      expect((await runCli(['route', 'rm', 'spare'], { env: env(s) })).stdout).toBe('spare  deleted\n');
      const missing = await runCli(['route', 'rm', 'spare'], { env: env(s) });
      expect([missing.code, missing.stderr]).toEqual([1, 'pstack: no such route: spare\n']);

      const usage = await runCli(['route', 'assign', 'checkout'], { env: env(s) });
      expect(usage.code).toBe(3);
      expect(usage.stderr).toContain('usage: pstack route');
    } finally {
      await stop();
    }
  }, 60_000);

  test('the generated `pstack api custom-routes assign --force` moves a held route', async () => {
    // negative control: drop `--force` from the argv — the server answers the held 409 and the
    // command exits 1.
    const { s, stop } = await boot('cr-api-cli');
    try {
      expect((await call(s, 'POST', '/api/custom-routes', { name: 'checkout', deployment: 'pr-1' })).status).toBe(201);
      const r = await runCli(['api', 'custom-routes', 'assign', '--name', 'checkout', '--deployment', 'pr-2', '--force'], { env: env(s) });
      expect(r.code).toBe(0);
      expect(JSON.parse(r.stdout)).toEqual(route('checkout', '2'));
      expect(await routesFile(s)).not.toContain('web-myapp-pr-1@docker');
    } finally {
      await stop();
    }
  }, 60_000);

  test('no PSTACK_API_URL is a usage refusal, before any request', async () => {
    // negative control: give PSTACK_API_URL a loopback default in apiBase — this exits 1 on a
    // refused connection instead of 3.
    const r = await runCli(['route', 'ls'], { env: { PSTACK_API_URL: undefined, PSTACK_TOKEN: 'x' } });
    expect(r.code).toBe(3);
    expect(r.stderr).toContain('set PSTACK_API_URL');
  }, 20_000);
});
