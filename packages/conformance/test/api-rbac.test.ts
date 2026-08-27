/**
 * ROLES OVER HTTP: the whole authorization matrix, as statuses, from outside the binary.
 *
 * `internal/api/permissions.go` holds the table; its own unit test proves the table says what its
 * author meant. This file proves what the SERVER ACTUALLY ANSWERS — the only claim an operator can
 * check — and it is deliberately exhaustive rather than representative: every route the matrix
 * names, probed once per role, with the exact status pinned.
 *
 * ── WHY EVERY ROW CARRIES A SUCCESS AND NOT JUST A REFUSAL ──────────────────────────────────────
 *
 * A server that answered 403 to everything would satisfy "a viewer cannot deploy", and it is the
 * shape a bad gate actually takes (a matcher that stopped matching, a rank comparison inverted, a
 * default-deny fallback swallowing rows above it). So each row also names the status the LOWEST
 * ALLOWED ROLE gets — 200, 201, 400, 404, 409 — and the table asserts, over itself, that no such
 * status is 403. The role one step above the boundary succeeding is the positive control, and it is
 * checked on every row rather than sampled.
 *
 * ── WHY A 404 IS A PASS ─────────────────────────────────────────────────────────────────────────
 *
 * The gate runs before the handler, so "this role is allowed" is observable as *any answer the
 * handler itself produced*. Most write rows therefore aim at a resource that does not exist: the
 * allowed role gets the handler's 404, the refused role gets the gate's 403, and the two are never
 * confusable. Where a row can succeed cheaply and reversibly it does instead (per-role host
 * variables, registries, specs, routing files, notifiers, accounts) — those are the rows that prove
 * the tier really opens, not merely that the gate stepped aside.
 *
 * Every mutating row writes to a name derived from the CALLING ROLE (`{r}`), so no row's outcome
 * depends on which role went first. The role loop can be reordered without changing an expectation.
 *
 * ── ROOT IS NOT THE TOP OF THE SCALE, IT IS BESIDE IT ───────────────────────────────────────────
 *
 * The PSTACK_TOKEN bearer passes the gate everywhere, which is not the same as succeeding
 * everywhere: `/api/tokens` answers root a 400, because a personal token belongs to an ACCOUNT and
 * root is not one. Those rows carry an explicit `root:` override. The property this file pins for
 * root is the honest one — **no cell in the matrix refuses root with a 403** — and it is asserted
 * over the table, not claimed in a comment.
 *
 * A docker shim is on PATH so `GET /api/swarm/join` has a fixed answer (this daemon is not a
 * manager → 409) on a machine with docker, without it, or on one that is itself a swarm.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync } from 'node:fs';
import { bootServer, tmpd, type Booted } from '../harness/server.ts';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';

type Tier = 'viewer' | 'developer' | 'maintainer' | 'admin' | 'root';
type Caller = Tier;

/** viewer < developer < maintainer < admin < root. `root` is a tier no account can hold. */
const RANK: Record<Tier, number> = { viewer: 1, developer: 2, maintainer: 3, admin: 4, root: 5 };
const CALLERS: Caller[] = ['viewer', 'developer', 'maintainer', 'admin', 'root'];

type Row = {
  method: string;
  /** `{r}` is replaced with the calling role's name, so a write never collides with another role's. */
  path: string;
  /** JSON body; `{r}` is substituted in the serialised form for the same reason. */
  body?: unknown;
  /** The least role that may reach it. */
  min: Tier;
  /** What the lowest allowed role gets. Never 403 — asserted below. */
  ok: number;
  /** What the PSTACK_TOKEN bearer gets, when the handler treats it differently from an account. */
  root?: number;
};

const PASSWORD = 'correct-horse-battery-staple';

/**
 * THE MATRIX. Grouped exactly as the decision was written, so a row can be read against it.
 */
const TABLE: Row[] = [
  // ── self-service: any authenticated principal, whatever its role ────────────────────────────
  // (your own password and logout are their own test below — both end the session they use)
  { method: 'GET', path: '/api/auth/me', min: 'viewer', ok: 200 },
  // Personal tokens are scoped to the caller; root holds PSTACK_TOKEN and has no account to scope to.
  { method: 'GET', path: '/api/tokens', min: 'viewer', ok: 200, root: 400 },
  { method: 'POST', path: '/api/tokens', body: { name: 'pat-{r}' }, min: 'viewer', ok: 201, root: 400 },
  { method: 'DELETE', path: '/api/tokens/999999', min: 'viewer', ok: 404, root: 400 },

  // ── viewer: every read ──────────────────────────────────────────────────────────────────────
  { method: 'GET', path: '/api/deployments', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/deployments/nope', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/deployments/nope/runtime', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/deployments/nope/logs', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/deployments/nope/logs/stream', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/deployments/nope/source', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/deployments/nope/readiness', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/jobs', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/jobs/nope', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/jobs/nope/stream', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/specs', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/specs/nope', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/routing', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/routing/live', min: 'viewer', ok: 200 },
  // Seeded in setup: a viewer gets the file's CONTENT, not a withheld stub. Asserted on the body too.
  { method: 'GET', path: '/api/routing/seed.yml', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/registries', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/host-vars', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/notifiers', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/notifiers/meta', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/notifiers/999999', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/notifiers/999999/deliveries', min: 'viewer', ok: 404 },
  { method: 'GET', path: '/api/control', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/swarm', min: 'viewer', ok: 200 },
  { method: 'GET', path: '/api/terminal-sessions', min: 'viewer', ok: 200 },
  // Decided: the roster is ordinary team information — who to hand a deployment to — and carries no
  // secret. Reading it is a viewer's; every write on it is an admin's, three rows down.
  { method: 'GET', path: '/api/users', min: 'viewer', ok: 200 },

  // ── developer: stacks and deployments ───────────────────────────────────────────────────────
  { method: 'PUT', path: '/api/deployments/nope', body: {}, min: 'developer', ok: 400 },
  { method: 'DELETE', path: '/api/deployments/nope', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/up', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/down', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/verify', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/sleep', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/wake', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/containers/web/start', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/containers/web/stop', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/containers/web/restart', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/jobs/nope/cancel', min: 'developer', ok: 404 },
  { method: 'POST', path: '/api/deployments/nope/share', body: {}, min: 'developer', ok: 404 },
  // THE CONTAINER SHELL IS A DEVELOPER'S. Decided, and it looks wrong until you hold it next to the
  // row three lines up: the same principal can `up` an arbitrary compose file — including a service
  // that mounts the docker socket — so refusing them a shell is theatre while the larger door is
  // open. Close `up` first and this row can follow it. (A share principal never reaches here at all;
  // shareAllows refuses it before the chain — the share test at the bottom of this file.)
  { method: 'GET', path: '/api/deployments/nope/terminal', min: 'developer', ok: 404 },
  { method: 'PUT', path: '/api/specs/probe-{r}', body: { spec: 'version: 1\nstack: probe\naxes: []\n' }, min: 'developer', ok: 201 },
  { method: 'DELETE', path: '/api/specs/missing-{r}', min: 'developer', ok: 404 },

  // ── maintainer: host configuration ──────────────────────────────────────────────────────────
  { method: 'PUT', path: '/api/host-vars/PROBE_{r}', body: { value: 'v', secret: false }, min: 'maintainer', ok: 201 },
  { method: 'DELETE', path: '/api/host-vars/MISSING_{r}', min: 'maintainer', ok: 404 },
  { method: 'PUT', path: '/api/registries/reg-{r}.example.com', body: { username: 'u', password: 'p' }, min: 'maintainer', ok: 200 },
  { method: 'DELETE', path: '/api/registries/missing-{r}.example.com', min: 'maintainer', ok: 404 },
  { method: 'PUT', path: '/api/routing/probe-{r}.yml', body: { content: 'http: {}\n' }, min: 'maintainer', ok: 201 },
  // Seeded per role in setup so the allowed answer is a real deletion (200), not a 404 for a name
  // that was never there — the routing store answers a missing file with a 400, which would make a
  // weaker positive control than every other tier gets.
  { method: 'DELETE', path: '/api/routing/del-{r}.yml', min: 'maintainer', ok: 200 },
  { method: 'POST', path: '/api/notifiers', body: { name: 'n-{r}', events: ['*'], config: { url: 'http://127.0.0.1:1/unreachable' } }, min: 'maintainer', ok: 201 },
  { method: 'PATCH', path: '/api/notifiers/999999', body: { name: 'q' }, min: 'maintainer', ok: 404 },
  { method: 'DELETE', path: '/api/notifiers/999999', min: 'maintainer', ok: 404 },
  { method: 'POST', path: '/api/notifiers/999999/test', body: {}, min: 'maintainer', ok: 404 },
  { method: 'POST', path: '/api/notifiers/999999/deliveries/1/redeliver', body: {}, min: 'maintainer', ok: 404 },
  // LOOSENED from admin to maintainer, deliberately: it is a host-configuration read and it sits
  // with the others. Still a real credential (the token joins a machine to the cluster), so it stops
  // here and goes no lower. 409 = the handler answered: the shim's daemon is not a swarm manager.
  { method: 'GET', path: '/api/swarm/join', min: 'maintainer', ok: 409 },
  // The control stack's operator page and its one action. The shim's docker lists no control
  // containers, so the allowed role gets the handler's own answers: an empty-but-reachable view,
  // and a restart that 404s on a service the (empty) view does not name — never the gate's 403.
  { method: 'GET', path: '/api/control/runtime', min: 'maintainer', ok: 200 },
  { method: 'POST', path: '/api/control/restart', body: { service: 'traefik' }, min: 'maintainer', ok: 404 },
  // The certificate mode. Status and the redeploy loop sit with the host surfaces; the wildcard
  // WRITE is admin — whoever stores that key can impersonate every preview. The allowed role gets
  // the handler's own answers: a garbage pair is 400, deleting a wildcard nobody stored is 404.
  { method: 'GET', path: '/api/tls', min: 'maintainer', ok: 200 },
  { method: 'POST', path: '/api/tls/redeploy', body: {}, min: 'maintainer', ok: 200 },
  { method: 'PUT', path: '/api/tls/wildcard', body: { cert: 'not-a-cert', key: 'not-a-key' }, min: 'admin', ok: 400 },
  { method: 'DELETE', path: '/api/tls/wildcard', min: 'admin', ok: 404 },
  // Reading the SSO configuration is a maintainer's — it returns a mask, never the client secret.
  // Writing it is two rows down, and admin, for a reason worth reading there.
  { method: 'GET', path: '/api/sso/config', min: 'maintainer', ok: 200 },

  // ── admin: people, and anything that can mint them ──────────────────────────────────────────
  { method: 'POST', path: '/api/users', body: { username: 'made-by-{r}', password: PASSWORD }, min: 'admin', ok: 201 },
  { method: 'PATCH', path: '/api/users/999999', body: { role: 'viewer' }, min: 'admin', ok: 404 },
  { method: 'DELETE', path: '/api/users/999999', min: 'admin', ok: 404 },
  // Somebody ELSE'S password. Your own is the self-service test below, and it is the same route.
  { method: 'PUT', path: '/api/users/999999/password', body: { password: PASSWORD }, min: 'admin', ok: 404 },
  // WRITING SSO IS ADMIN THOUGH EVERY OTHER HOST-CONFIGURATION WRITE IS MAINTAINER. A provider's
  // `defaultRole` MINTS ACCOUNTS at whatever role it names, so a maintainer who could point this
  // host at an identity provider they control would be able to sign in through it as an admin. That
  // is a promotion path, and promotion paths live with people. 400 = the handler answered and
  // refused the body.
  { method: 'PUT', path: '/api/sso/config', body: { key: 'probe-{r}' }, min: 'admin', ok: 400 },
  { method: 'DELETE', path: '/api/sso/config', min: 'admin', ok: 404 },
  // The keyed PUT passes the gate and finds no handler — the chain answers 404. The row pins the
  // GATE's answer; it must not become reachable to a maintainer if the handler ever lands.
  { method: 'PUT', path: '/api/sso/config/probe-{r}', body: { preset: 'github' }, min: 'admin', ok: 404 },
  { method: 'DELETE', path: '/api/sso/config/probe-{r}', min: 'admin', ok: 404 },
  // An admin may IMPORT a sealed configuration (they already hold the file and its passphrase) and
  // may not EXPORT one — the row below.
  { method: 'POST', path: '/api/config/sealed', body: {}, min: 'admin', ok: 400 },

  // ── root: the whole host, portable, in plaintext ────────────────────────────────────────────
  { method: 'GET', path: '/api/config', min: 'root', ok: 200 },
  { method: 'POST', path: '/api/config', body: {}, min: 'root', ok: 400 },
];

/** What `caller` must get for `row`. The only formula in this file. */
const expected = (row: Row, caller: Caller): number =>
  caller === 'root' ? (row.root ?? row.ok) : RANK[caller] >= RANK[row.min] ? row.ok : 403;

const sub = (s: string, role: Caller): string => s.split('{r}').join(role);

/** Boot a server with a shimmed docker, four accounts (one per role), and the seeded routing files. */
async function bootWithRoles(tag: string): Promise<{ s: Booted; as: Record<Caller, Record<string, string>>; cleanup: () => void }> {
  const routingDir = tmpd(`${tag}-routing`);
  mkdirSync(routingDir, { recursive: true });
  const shim = dockerShim(ALWAYS_OK);
  const s = await bootServer({ tag, routingDir, pathPrefix: shim.dir });
  const { base, H } = s;

  const seed = async (name: string) =>
    fetch(`${base}/api/routing/${name}`, { method: 'PUT', headers: H, body: JSON.stringify({ content: 'http:\n  routers: {}\n' }) });
  await seed('seed.yml');
  // One deletable file PER CALLER, root included — including the two callers whose DELETE is going
  // to be refused. Seeding only the allowed roles would leave the root row with no target and turn
  // its 200 into a 400, which is exactly the kind of tidying that quietly weakens a positive
  // control. Viewer's and developer's copies are meant to survive the run.
  for (const role of CALLERS) await seed(`del-${role}.yml`);

  const as = {} as Record<Caller, Record<string, string>>;
  as.root = H;
  for (const role of ['viewer', 'developer', 'maintainer', 'admin'] as const) {
    const made = await fetch(`${base}/api/users`, { method: 'POST', headers: H, body: JSON.stringify({ username: role, password: PASSWORD, role }) });
    expect(`create ${role} → ${made.status}`).toBe(`create ${role} → 201`);
    as[role] = await session(base, role);
  }
  return { s, as, cleanup: () => shim.remove() };
}

/** Sign in and return the headers that authenticate as that account. */
async function session(base: string, username: string): Promise<Record<string, string>> {
  const login = await fetch(`${base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username, password: PASSWORD }) });
  const raw = login.headers.get('set-cookie');
  if (!raw) throw new Error(`login as ${username} returned ${login.status} and no session cookie`);
  return { cookie: raw.split(';')[0]!, 'content-type': 'application/json' };
}

/** The id of an account, by username — nothing here may depend on the order rows were inserted in. */
async function idOf(base: string, root: Record<string, string>, username: string): Promise<number> {
  const users = (JSON.parse((await probe(base, root, 'GET', '/api/users', 'root')).text) as { users: { id: number; username: string }[] }).users;
  const found = users.find((u) => u.username === username);
  if (!found) throw new Error(`no account named ${username} — the fixture did not create one`);
  return found.id;
}

/** One probe. The body is always drained, so nothing is left holding a socket. */
async function probe(base: string, headers: Record<string, string>, method: string, path: string, role: Caller, body?: unknown): Promise<{ status: number; text: string }> {
  const r = await fetch(`${base}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : sub(JSON.stringify(body), role),
    signal: AbortSignal.timeout(10_000),
    redirect: 'manual',
  });
  return { status: r.status, text: await r.text() };
}

describe('roles: the authorization matrix, end to end', () => {
  // negative control: in internal/api/permissions.go set the deploymentRe write row's `min` to
  // auth.Viewer — every `POST /api/deployments/nope/{up,down,…}` cell for the viewer goes 403 → 404
  // and this test reports them. Also: `{path: "/api/users", methods: mPost, min: auth.Admin}` →
  // auth.Viewer; `{re: hostVarRe, …, min: auth.Maintainer}` → auth.Developer.
  test('every route in the matrix answers every role the status the matrix names', async () => {
    // The table is the specification, so check the SPECIFICATION first: a server that refused
    // everything would satisfy every "…cannot…" in this file, and these three assertions are what
    // make that impossible to pass.
    for (const row of TABLE) {
      expect(`${row.method} ${row.path} allowed → ${row.ok}`).not.toBe(`${row.method} ${row.path} allowed → 403`);
      expect(`${row.method} ${row.path} root → ${row.root ?? row.ok}`).not.toBe(`${row.method} ${row.path} root → 403`);
    }
    // Every tier is exercised, so a table that quietly lost its maintainer rows is not a green run.
    for (const tier of CALLERS) expect(`${tier} rows: ${TABLE.filter((r) => r.min === tier).length > 0}`).toBe(`${tier} rows: true`);
    // And every non-viewer row really does refuse somebody — the positive control has a negative.
    for (const row of TABLE.filter((r) => r.min !== 'viewer')) {
      expect(`${row.method} ${row.path} refuses → ${CALLERS.filter((c) => expected(row, c) === 403).length > 0}`).toBe(`${row.method} ${row.path} refuses → true`);
    }

    const { s, as, cleanup } = await bootWithRoles('rbac');
    try {
      const wrong: string[] = [];
      for (const row of TABLE) {
        for (const caller of CALLERS) {
          const path = sub(row.path, caller);
          const got = await probe(s.base, as[caller], row.method, path, caller, row.body);
          const want = expected(row, caller);
          if (got.status !== want) {
            wrong.push(`${row.method} ${path} as ${caller} → want ${want}, got ${got.status}: ${got.text.replace(/\s+/g, ' ').slice(0, 120)}`);
          }
        }
      }
      expect(wrong).toEqual([]);

      // A read a viewer is allowed must return the THING, not a withheld stub — the routing store
      // has a second, principal-level check inside the handler and this is where it would show.
      const file = await probe(s.base, as.viewer, 'GET', '/api/routing/seed.yml', 'viewer');
      expect(JSON.parse(file.text)).toEqual({ name: 'seed.yml', content: 'http:\n  routers: {}\n' });

      // A 403 says which role was wanted and which one you hold. An operator staring at a bare
      // "forbidden" cannot tell a wrong role from a wrong URL, and guesses.
      const refused = await probe(s.base, as.developer, 'PUT', '/api/host-vars/X', 'developer', { value: 'v', secret: false });
      expect(JSON.parse(refused.text)).toEqual({ error: 'this route requires the maintainer role or higher — you are a developer' });
    } finally {
      cleanup();
      await s.stop();
    }
  }, 120_000);

  // negative control: in internal/api/permissions.go make permit's `!found` branch `return true`
  // unconditionally — all three of these stop refusing the four roles.
  test('a route with no row in the table is the root token\'s, and nobody else\'s', async () => {
    const { s, as, cleanup } = await bootWithRoles('rbac-deny');
    try {
      const wrong: string[] = [];
      // A method nobody listed on a route that exists; a path nobody dispatches; a wrong method on a
      // route that does exist. Each falls off the end of the table, and the fallback is root.
      for (const [method, path, rootStatus] of [
        ['POST', '/api/jobs', 200],
        ['GET', '/api/nothing-here', 404],
        ['GET', '/api/deployments/nope/containers/web/start', 405],
      ] as [string, string, number][]) {
        for (const caller of CALLERS) {
          const got = await probe(s.base, as[caller], method, path, caller);
          const want = caller === 'root' ? rootStatus : 403;
          if (got.status !== want) wrong.push(`${method} ${path} as ${caller} → ${want} (got ${got.status})`);
        }
      }
      expect(wrong).toEqual([]);
      // The refusal names the reason, and it is not the same sentence as a wrong-role one.
      const denied = await probe(s.base, as.admin, 'GET', '/api/nothing-here', 'admin');
      expect(JSON.parse(denied.text)).toEqual({ error: 'this route is restricted to the PSTACK_TOKEN bearer' });
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);

  // negative control: in internal/api/principal.go, resolve a personal token to a principal without
  // its owner's role (a KindUser with an empty User.Role) — `up` stops being 403 and this fails.
  test('a personal access token inherits its owner\'s role, and cannot exceed it', async () => {
    const { s, as, cleanup } = await bootWithRoles('rbac-pat');
    try {
      // Minted BY the viewer, FOR the viewer — the only way to get one.
      const minted = await probe(s.base, as.viewer, 'POST', '/api/tokens', 'viewer', { name: 'ci' });
      expect(minted.status).toBe(201);
      const pat = { authorization: `Bearer ${(JSON.parse(minted.text) as { token: string }).token}`, 'content-type': 'application/json' };

      // It authenticates, and it reads.
      expect((await probe(s.base, pat, 'GET', '/api/auth/me', 'viewer')).status).toBe(200);
      expect((await probe(s.base, pat, 'GET', '/api/deployments', 'viewer')).status).toBe(200);
      // A bearer token is the credential a CI job holds, so this is the row that matters: it is not
      // a machine credential, it is that viewer, and it deploys exactly as much as they do.
      expect((await probe(s.base, pat, 'POST', '/api/deployments/nope/up', 'viewer')).status).toBe(403);
      expect((await probe(s.base, pat, 'GET', '/api/deployments/nope/terminal', 'viewer')).status).toBe(403);
      expect((await probe(s.base, pat, 'PUT', '/api/host-vars/X', 'viewer', { value: 'v', secret: false })).status).toBe(403);
      expect((await probe(s.base, pat, 'POST', '/api/users', 'viewer', { username: 'by-pat', password: PASSWORD, role: 'admin' })).status).toBe(403);
      // And the same token becomes more powerful when the ACCOUNT does — the role is read fresh on
      // every request, so there is no stale-credential window to promote through.
      expect((await probe(s.base, as.root, 'PATCH', `/api/users/${await idOf(s.base, as.root, 'viewer')}`, 'root', { role: 'developer' })).status).toBe(200);
      expect((await probe(s.base, pat, 'POST', '/api/deployments/nope/up', 'viewer')).status).toBe(404);
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);

  // negative control: in internal/api/routes_auth.go drop the `role` default so POST /api/users
  // passes auth.CreateOpts{} (the pre-change behaviour) — the created account comes back "admin"
  // and this fails on the role assertion.
  test('POST /api/users is the hole this closes: admin-only, and viewer by default', async () => {
    const { s, as, cleanup } = await bootWithRoles('rbac-escalate');
    try {
      // THE ESCALATION. Before this change the route was reachable by any authenticated principal
      // and, passing no role, fell through to `COALESCE(?, 'admin')` — so any account on the host
      // could mint itself a fully privileged second one. Both halves are closed: the gate, and the
      // default.
      for (const caller of ['viewer', 'developer', 'maintainer'] as const) {
        const r = await probe(s.base, as[caller], 'POST', '/api/users', caller, { username: `escalated-by-${caller}`, password: PASSWORD, role: 'admin' });
        expect(`${caller} → ${r.status}`).toBe(`${caller} → 403`);
      }
      // An absent role is the LEAST privilege, never the most. This is the breaking change.
      const bare = await probe(s.base, as.admin, 'POST', '/api/users', 'admin', { username: 'no-role-given', password: PASSWORD });
      expect(bare.status).toBe(201);
      expect((JSON.parse(bare.text) as { user: { role: string } }).user.role).toBe('viewer');
      // A role this build does not know is refused at the door rather than stored and ranked 0 —
      // fail closed AND fail loud, because a typo'd role that silently reaches nothing is a support
      // ticket nobody can diagnose.
      const bogus = await probe(s.base, as.admin, 'POST', '/api/users', 'admin', { username: 'superuser-please', password: PASSWORD, role: 'superuser' });
      expect(bogus.status).toBe(400);
      expect(JSON.parse(bogus.text)).toEqual({ error: 'role must be one of: viewer, developer, maintainer, admin' });
      // Each of the four is accepted and is what comes back.
      for (const role of ['viewer', 'developer', 'maintainer', 'admin'] as const) {
        const r = await probe(s.base, as.root, 'POST', '/api/users', 'root', { username: `a-${role}`, password: PASSWORD, role });
        expect(`${role} → ${r.status} ${(JSON.parse(r.text) as { user: { role: string } }).user.role}`).toBe(`${role} → 201 ${role}`);
      }
      // Promotion is admin-only for the same reason: whoever can set a role can set their own.
      const viewerId = await idOf(s.base, as.root, 'viewer');
      expect((await probe(s.base, as.maintainer, 'PATCH', `/api/users/${viewerId}`, 'maintainer', { role: 'admin' })).status).toBe(403);
      expect((await probe(s.base, as.admin, 'PATCH', `/api/users/${viewerId}`, 'admin', { role: 'admin' })).status).toBe(200);
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);

  // negative control: in internal/auth/auth.go delete the lastAdmin guard from DeleteUser (then,
  // separately, from SetRole) — the delete returns 200 and the demote returns 200.
  test('the last admin cannot be deleted or demoted, not even by the root token', async () => {
    const { s, as, cleanup } = await bootWithRoles('rbac-lastadmin');
    try {
      const list = (JSON.parse((await probe(s.base, as.root, 'GET', '/api/users', 'root')).text) as { users: { id: number; username: string; role: string }[] }).users;
      const admins = list.filter((u) => u.role === 'admin');
      expect(admins).toHaveLength(1);
      const only = admins[0]!;

      // Root is NOT counted as an admin and cannot stand in for one: PSTACK_TOKEN may live in a CI
      // secret store, or have been rotated away from every human on the team. A host with zero
      // admin accounts is one nobody can add people to.
      const del = await probe(s.base, as.root, 'DELETE', `/api/users/${only.id}`, 'root');
      expect(del.status).toBe(400);
      expect(JSON.parse(del.text)).toEqual({ error: 'cannot delete the last admin — promote another account first' });
      const demote = await probe(s.base, as.root, 'PATCH', `/api/users/${only.id}`, 'root', { role: 'viewer' });
      expect(demote.status).toBe(400);
      expect(JSON.parse(demote.text)).toEqual({ error: 'cannot demote the last admin — promote another account first' });
      // An admin cannot walk out of the role either.
      expect((await probe(s.base, as.admin, 'PATCH', `/api/users/${only.id}`, 'admin', { role: 'developer' })).status).toBe(400);

      // Promote a second, and the guard lifts — it counts admins, it does not pin a particular one.
      const other = list.find((u) => u.username === 'maintainer')!;
      expect((await probe(s.base, as.root, 'PATCH', `/api/users/${other.id}`, 'root', { role: 'admin' })).status).toBe(200);
      expect((await probe(s.base, as.root, 'DELETE', `/api/users/${only.id}`, 'root')).status).toBe(200);
      // …and now THAT one is the last.
      expect((await probe(s.base, as.root, 'DELETE', `/api/users/${other.id}`, 'root')).status).toBe(400);
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);

  // negative control: in internal/api/permissions.go drop `self: true` from the userPasswordRe row
  // — every own-password call becomes 403. Or make isSelf stop comparing the id (`return true`) —
  // the maintainer changes the viewer's password and the "someone else's" assertion fails.
  test('self-service is by identity, not by rank: your own password, your own tokens, your own logout', async () => {
    const { s, as, cleanup } = await bootWithRoles('rbac-self');
    try {
      const ids = Object.fromEntries(
        (JSON.parse((await probe(s.base, as.root, 'GET', '/api/users', 'root')).text) as { users: { id: number; username: string }[] }).users.map((u) => [u.username, u.id]),
      ) as Record<string, number>;

      // Somebody else's password is admin, whatever your rank. The maintainer is the interesting
      // caller here: strictly above a developer, still nowhere near an account.
      const other = await probe(s.base, as.maintainer, 'PUT', `/api/users/${ids.developer}/password`, 'maintainer', { password: PASSWORD });
      expect(other.status).toBe(403);
      expect(JSON.parse(other.text)).toEqual({ error: 'this route requires the admin role or higher — you are a maintainer' });
      expect((await probe(s.base, as.admin, 'PUT', `/api/users/${ids.developer}/password`, 'admin', { password: PASSWORD })).status).toBe(200);

      // Your own is yours, at every rank — including the weakest one, which is the whole point.
      for (const role of ['viewer', 'developer', 'maintainer', 'admin'] as const) {
        const fresh = await session(s.base, role);
        const own = await probe(s.base, fresh, 'PUT', `/api/users/${ids[role]}/password`, role, { password: `${PASSWORD}-${role}` });
        expect(`${role} own password → ${own.status}`).toBe(`${role} own password → 200`);
        // It revoked that account's sessions, which is the documented cost of changing it.
        expect((await probe(s.base, fresh, 'GET', '/api/auth/me', role)).status).toBe(401);
      }

      // Logout runs BEFORE the role gate — an account that was just demoted, or whose role this
      // build no longer recognises, must still be able to end its own session. (The passwords were
      // rotated a few lines up, so each account signs in with its new one.)
      for (const role of ['viewer', 'developer', 'maintainer', 'admin'] as const) {
        const login = await fetch(`${s.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: role, password: `${PASSWORD}-${role}` }) });
        const cookie = { cookie: login.headers.get('set-cookie')!.split(';')[0]! };
        expect(`${role} logout → ${(await probe(s.base, cookie, 'POST', '/api/auth/logout', role)).status}`).toBe(`${role} logout → 200`);
        expect((await probe(s.base, cookie, 'GET', '/api/auth/me', role)).status).toBe(401);
      }
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);

  // negative control: in internal/api/permissions.go remove permit's `who.Kind == auth.KindShare`
  // early return — the share principal falls into the table, holds no role, and every one of these
  // 200s becomes a 403.
  test('a share principal is unaffected: it was decided before the table, and still is', async () => {
    const { s, cleanup } = await bootWithRoles('rbac-share');
    try {
      const { base, H } = s;
      expect((await fetch(`${base}/api/deployments/pr-1`, { method: 'PUT', headers: H, body: JSON.stringify({ spec: 'version: 1\nstack: pr-1\naxes:\n  - name: a\n    up: "true"\n' }) })).status).toBe(201);
      const mint = await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: '{}' });
      expect(mint.status).toBe(201);
      const q = `?token=${(await mint.json() as { token: string }).token}`;

      // Exactly what shareAllows grants, unchanged by roles existing: its own deployment, the views
      // it names, GET only.
      for (const path of [`/api/auth/me${q}`, `/api/deployments/pr-1${q}`, `/api/deployments/pr-1/logs${q}`, `/api/deployments/pr-1/runtime${q}`]) {
        expect(`${path} → ${(await fetch(`${base}${path}`)).status}`).toBe(`${path} → 200`);
      }
      // …and nothing a role would have opened. A share is not a viewer with a shorter lifetime.
      for (const [method, path] of [
        ['GET', `/api/users${q}`],
        ['GET', `/api/deployments${q}`],
        ['GET', `/api/jobs${q}`],
        ['POST', `/api/deployments/pr-1/up${q}`],
        ['GET', `/api/deployments/pr-1/terminal${q}`],
      ] as [string, string][]) {
        expect(`${method} ${path} → ${(await fetch(`${base}${path}`, { method })).status}`).toBe(`${method} ${path} → 403`);
      }
    } finally {
      cleanup();
      await s.stop();
    }
  }, 60_000);
});
