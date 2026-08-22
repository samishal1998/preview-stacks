/**
 * Source is a secret: GET /api/specs/:name, GET /api/deployments/:id/source. Plus the HTTP half of
 * per-service logs (the service-name validation) and the stack-sharing report on PUT.
 *
 * Ported from packages/pstack/test/stack.test.ts — 'a spec source is a secret; its metadata is
 * not', "a deployment's stored source", and the 'over HTTP' / 'two deployments on one stack'
 * parts of 'per-service logs, and duplicating a deployment'. The pure composeLogsCommand tests
 * stay behind: they exercise a function, not the contract.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer } from '../harness/server.ts';

describe('a spec source is a secret; its metadata is not', () => {
  /*
   * Reads are unauthenticated on this API by design — see the note on the route. A spec's SOURCE is
   * the one read that cannot follow that rule: hook bodies are shell strings, and a hook routinely
   * carries a credential inline. The resolved-spec view already withholds hook bodies, so serving
   * the whole file unauthenticated handed them out one route over.
   *
   * This boots the real server rather than calling a handler, because the thing under test is the
   * authorization decision on a live request — a unit test of `specs.source()` would pass either way.
   */
  const SECRET = 'hunter2-registry-password';
  const SPEC = [
    'version: 1',
    'kind: isolated',
    'stack: web-${PR}',
    'axes:',
    '  - name: registry',
    `    up: "docker login -u ci -p ${SECRET}"`,
    '    assert_gone: "docker info >/dev/null 2>&1 || exit 1; ! docker login-check"',
    '',
  ].join('\n');

  const boot = async () => {
    const s = await bootServer({ tag: 'api' });
    const put = await fetch(`${s.base}/api/specs/web`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: SPEC, description: 'the web app' }),
    });
    expect(put.status).toBe(201);
    return s;
  };

  test('without auth the route is 401 — and the secret appears nowhere in the refusal', async () => {
    // 0.10.0 reversed the reads-are-open design: job outcomes carry captured credentials BY DESIGN,
    // so every read now sits behind the gate. This suite originally asserted public metadata with a
    // withheld source; the withheld mechanism still exists for a future role split, but the gate
    // fronts it.
    const s = await boot();
    try {
      const r = await fetch(`${s.base}/api/specs/web`);
      expect(r.status).toBe(401);
      // The whole refusal body, not just the status: a leak anywhere is a leak.
      const raw = await r.text();
      expect(raw).not.toContain(SECRET);
      expect(raw).not.toContain('docker login');
      expect(raw).not.toContain('web'); // not even the spec's existence
    } finally {
      await s.stop();
    }
  });

  test('with a token the source is served in full', async () => {
    const s = await boot();
    try {
      const r = await fetch(`${s.base}/api/specs/web`, { headers: s.H });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { source?: string; sourceWithheld?: boolean };
      expect(body.source).toContain(SECRET);
      expect(body.sourceWithheld).toBeUndefined();
    } finally {
      await s.stop();
    }
  });

  test('a wrong token and no token are indistinguishable', async () => {
    // The non-oracle property survives the auth reversal: both refusals must be byte-identical, so
    // this route cannot be used to probe whether a guessed token is valid.
    const s = await boot();
    try {
      const withWrong = await fetch(`${s.base}/api/specs/web`, { headers: { authorization: 'Bearer wrong' } });
      const withNone = await fetch(`${s.base}/api/specs/web`);
      expect(withWrong.status).toBe(401);
      expect(withNone.status).toBe(401);
      expect(await withWrong.text()).toBe(await withNone.text());
    } finally {
      await s.stop();
    }
  });

  test('the LIST route never carried a source to begin with', async () => {
    const s = await boot();
    try {
      const raw = await (await fetch(`${s.base}/api/specs`, { headers: s.H })).text();
      expect(raw).not.toContain(SECRET);
      expect(JSON.parse(raw).specs).toHaveLength(1);
    } finally {
      await s.stop();
    }
  });
});

describe("a deployment's stored source — so replacing is editing, not retyping", () => {
  const SECRET = 'deploy-hook-secret-value';
  const SPEC = ['version: 1', 'kind: shared', 'stack: shared-ingress', 'axes: []', ''].join('\n');
  const COMPOSE = [
    'services:',
    '  postgres:',
    '    image: postgres:17',
    '    environment:',
    `      POSTGRES_PASSWORD: ${SECRET}`,
    '',
  ].join('\n');

  const boot = async () => {
    const s = await bootServer({ tag: 'src' });
    const put = await fetch(`${s.base}/api/deployments/ingress`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: SPEC, compose: COMPOSE }),
    });
    expect(put.status).toBe(201);
    return s;
  };

  test('with a token it returns the spec and the compose file verbatim', async () => {
    // The point: the replace form can pre-fill. Retyping a spec from memory drops whatever you
    // forget, and a dropped axis stops being tracked while what it created keeps running.
    const s = await boot();
    try {
      const r = await fetch(`${s.base}/api/deployments/ingress/source`, { headers: s.H });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { spec: string; compose: string | null; specName: string | null };
      expect(body.spec).toBe(SPEC);
      expect(body.compose).toBe(COMPOSE);
      expect(body.specName).toBeNull();
    } finally {
      await s.stop();
    }
  });

  test('without auth it is a 401, and the compose secret does not leak', async () => {
    const s = await boot();
    try {
      const r = await fetch(`${s.base}/api/deployments/ingress/source`);
      expect(r.status).toBe(401);
      expect(await r.text()).not.toContain(SECRET);
    } finally {
      await s.stop();
    }
  });

  test('an unknown deployment is a 404', async () => {
    const s = await boot();
    try {
      const r = await fetch(`${s.base}/api/deployments/nope/source`, { headers: s.H });
      expect(r.status).toBe(404);
    } finally {
      await s.stop();
    }
  });
});

describe('per-service logs, and duplicating a deployment', () => {
  describe('compose logs for one service', () => {
    test('over HTTP: an invalid service name is refused before it reaches a shell', async () => {
      const s = await bootServer({ tag: 'ls' });
      try {
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({
            spec: 'version: 1\nstack: s\ncompose:\n  file: dc.yml\n  profiles: [app]\naxes: []\n',
          }),
        });
        const r = await fetch(`${s.base}/api/deployments/d/logs?service=${encodeURIComponent('a; rm -rf /')}`, {
          headers: s.H,
        });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/not a valid compose service name/);
      } finally {
        await s.stop();
      }
    });
  });

  describe('two deployments on one stack', () => {
    /*
     * The hazard duplicating introduces: copy a spec whose `stack:` is a literal, give it a new id, and
     * two records drive the same compose project — `down` on either stops the other's containers.
     * Reported, not refused: it can be deliberate, and refusing over a guess would be worse.
     */
    const LITERAL = 'version: 1\nkind: shared\nstack: shared-ingress\naxes: []\n';

    const put = (s: { base: string; H: Record<string, string> }, id: string, body: Record<string, unknown>) =>
      fetch(`${s.base}/api/deployments/${id}`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });

    test('a new deployment sharing a stack is reported, naming the other id', async () => {
      const s = await bootServer({ tag: 'ls' });
      try {
        const first = (await (await put(s, 'ingress', { spec: LITERAL })).json()) as {
          stackSharedWith?: string[];
        };
        // Absent rather than an empty array, so "checked and found none" cannot be confused with
        // "not checked".
        expect(first.stackSharedWith).toBeUndefined();

        const second = (await (await put(s, 'ingress-copy', { spec: LITERAL })).json()) as {
          stackSharedWith?: string[];
        };
        expect(second.stackSharedWith).toEqual(['ingress']);
      } finally {
        await s.stop();
      }
    });

    test('a stack that interpolates a variable does not collide', async () => {
      const s = await bootServer({ tag: 'ls' });
      try {
        const VAR = 'version: 1\nstack: pr-${PR}\naxes: []\n';
        // Both creates must land (201), or "absent" would mean "not checked" after all.
        expect((await put(s, 'pr-1', { spec: VAR, vars: { PR: '1' } })).status).toBe(201);
        const res = await put(s, 'pr-2', { spec: VAR, vars: { PR: '2' } });
        expect(res.status).toBe(201);
        const other = (await res.json()) as { stackSharedWith?: string[] };
        // Different values, different stacks — which is what makes duplicating such a spec safe.
        expect(other.stackSharedWith).toBeUndefined();
      } finally {
        await s.stop();
      }
    });

    test('REPLACING a deployment is not reported as colliding with itself', async () => {
      const s = await bootServer({ tag: 'ls' });
      try {
        expect((await put(s, 'ingress', { spec: LITERAL })).status).toBe(201);
        const res = await put(s, 'ingress', { spec: LITERAL });
        // 200, not 201: the second PUT was a replace, so the only record on this stack is itself.
        expect(res.status).toBe(200);
        const again = (await res.json()) as { stackSharedWith?: string[] };
        expect(again.stackSharedWith).toBeUndefined();
      } finally {
        await s.stop();
      }
    });
  });
});
