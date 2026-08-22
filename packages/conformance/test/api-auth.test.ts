/**
 * Authentication over HTTP: the gate on every data route, the three ways through it (machine
 * token, session cookie, personal token), the lockouts a browser used to talk itself into, and
 * what a password change revokes. Ported from packages/pstack/test/stack.test.ts — the two tests
 * that constructed Auth/Store directly are not here; they have no HTTP-observable equivalent.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer } from '../harness/server.ts';

describe('authentication — every route behind the gate, three ways through it', () => {
  const TOKEN = 'root-machine-token-value';
  const boot = () => bootServer({ tag: 'auth', token: TOKEN });

  const authz = { authorization: `Bearer ${TOKEN}` };
  const jsonHeaders = { ...authz, 'content-type': 'application/json' };

  test('the 401 sweep: every data route refuses an unauthenticated read', async () => {
    // The reversal this release exists for. Job outcomes carry captured credentials BY DESIGN
    // (outputs is the inter-axis env channel), so reads-are-open was a credential feed — measured
    // and documented in docs/secret-exposure.md, now retired by this gate.
    const s = await boot();
    const { base } = s;
    try {
      for (const path of [
        '/api/deployments',
        '/api/deployments/x',
        // A read that STARTS something (a readiness watch) is the last route that may be left open.
        '/api/deployments/x/readiness',
        '/api/jobs',
        '/api/jobs/nope',
        '/api/specs',
        '/api/routing',
        '/api/routing/live',
        '/api/registries',
        '/api/control',
        '/api/users',
        '/api/auth/me',
      ]) {
        const r = await fetch(`${base}${path}`);
        expect(`${path} → ${r.status}`).toBe(`${path} → 401`);
      }
      // The two that must stay open: liveness (init waits on it before any auth exists) and the
      // UI document itself (the login page has to load from somewhere).
      expect((await fetch(`${base}/api/health`)).status).toBe(200);
      expect((await fetch(`${base}/`)).status).toBe(200);
    } finally {
      await s.stop();
    }
  });

  test('bootstrap → login → cookie session → logout, end to end', async () => {
    const s = await boot();
    const { base } = s;
    try {
      // Bootstrap needs the machine token: its whole purpose is to run before accounts exist.
      const noAuth = await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      expect(noAuth.status).toBe(401);

      const made = await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: jsonHeaders,
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      expect(made.status).toBe(201);

      // Once only — a replayed bootstrap must NOT read as success, or it looks like it set the
      // password it carried.
      const again = await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: jsonHeaders,
        body: JSON.stringify({ username: 'mallory', password: 'evil-password' }),
      });
      expect(again.status).toBe(409);

      // Login sets an httpOnly cookie…
      const login = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      expect(login.status).toBe(200);
      const setCookie = login.headers.get('set-cookie')!;
      expect(setCookie).toContain('pstack_session=');
      expect(setCookie).toContain('HttpOnly');
      // No X-Forwarded-Proto here, so Secure must be absent — hardcoding it would break plain-HTTP
      // loopback development.
      expect(setCookie).not.toContain('Secure');
      const cookie = setCookie.split(';')[0]!;

      // …which authenticates a read on its own. This is the property EventSource and WebSocket
      // depend on: a browser can attach a cookie where it cannot attach a header.
      const me = await fetch(`${base}/api/auth/me`, { headers: { cookie } });
      expect(me.status).toBe(200);
      expect(((await me.json()) as { user: { username: string } }).user.username).toBe('sami');
      expect((await fetch(`${base}/api/deployments`, { headers: { cookie } })).status).toBe(200);

      // Logout kills the server-side session, not just the cookie.
      await fetch(`${base}/api/auth/logout`, { method: 'POST', headers: { cookie } });
      expect((await fetch(`${base}/api/auth/me`, { headers: { cookie } })).status).toBe(401);
    } finally {
      await s.stop();
    }
  });

  test('a wrong password and an unknown user produce the same refusal', async () => {
    // Naming which half failed turns the login form into a username oracle.
    const s = await boot();
    const { base } = s;
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: jsonHeaders,
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      const wrongPw = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'sami', password: 'wrong' }),
      });
      const noUser = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'ghost', password: 'wrong' }),
      });
      expect(wrongPw.status).toBe(400);
      expect(await wrongPw.text()).toBe(await noUser.text());
    } finally {
      await s.stop();
    }
  });

  test('personal tokens: minted once, hashed at rest, usable as a bearer, revocable', async () => {
    const s = await boot();
    const { base, dataDir } = s;
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: jsonHeaders,
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      const login = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      const cookie = login.headers.get('set-cookie')!.split(';')[0]!;

      const minted = await fetch(`${base}/api/tokens`, {
        method: 'POST',
        headers: { cookie, 'content-type': 'application/json' },
        body: JSON.stringify({ name: 'ci-deploy' }),
      });
      expect(minted.status).toBe(201);
      const { token, id } = (await minted.json()) as { token: string; id: number };
      expect(token.startsWith('pstack_pat_')).toBe(true);

      // The bearer works like PSTACK_TOKEN for data routes.
      const asPat = await fetch(`${base}/api/deployments`, {
        headers: { authorization: `Bearer ${token}` },
      });
      expect(asPat.status).toBe(200);

      // Hashed at rest: the plaintext appears NOWHERE on disk. WAL mode keeps fresh rows in the
      // -wal sibling, not the .db file — grepping only the .db would pass vacuously.
      const { readdir } = await import('node:fs/promises');
      let disk = '';
      for (const f of await readdir(`${dataDir}/db`)) {
        disk += await Bun.file(`${dataDir}/db/${f}`).text();
      }
      expect(disk).toContain('ci-deploy'); // proves the read actually captured the live rows
      expect(disk).not.toContain(token);
      expect(disk).not.toContain('correct-horse');

      // The list carries names and dates, never the secret.
      const listed = await (await fetch(`${base}/api/tokens`, { headers: { cookie } })).text();
      expect(listed).toContain('ci-deploy');
      expect(listed).not.toContain(token);

      // Revoked, it stops working immediately.
      await fetch(`${base}/api/tokens/${id}`, { method: 'DELETE', headers: { cookie } });
      expect(
        (await fetch(`${base}/api/deployments`, { headers: { authorization: `Bearer ${token}` } }))
          .status,
      ).toBe(401);
    } finally {
      await s.stop();
    }
  });

  test('with no PSTACK_TOKEN configured, loopback dev mode stays open', async () => {
    // The safety interlock in `serve` refuses to bind off-loopback without a token, so "everything
    // is root" is confined to 127.0.0.1 by construction.
    const s = await bootServer({ tag: 'auth-open', token: null });
    try {
      const r = await fetch(`${s.base}/api/deployments`);
      expect(r.status).toBe(200);
      // And the server admits the gate is off — the login page draws itself from this flag.
      const h = (await (await fetch(`${s.base}/api/health`)).json()) as { authEnforced?: boolean };
      expect(h.authEnforced).toBe(false);
    } finally {
      await s.stop();
    }
  });
});

describe('SSE over a session cookie — the reason sessions are cookies at all', () => {
  /*
   * `EventSource` cannot send an Authorization header. If the job log stream did not accept the
   * session cookie, putting reads behind the gate would have silently broken live job following for
   * every browser user — and the WebSocket terminal in the next phase depends on the same property.
   * So this asserts the mechanism, not just the happy path.
   */
  test('the job stream refuses anonymously and streams for a cookie', async () => {
    const s = await bootServer({ tag: 'sse', token: 'tok' });
    const { base } = s;
    const j = { 'content-type': 'application/json' };
    const rootAuth = { ...j, authorization: 'Bearer tok' };
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: rootAuth,
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      const login = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: j,
        body: JSON.stringify({ username: 'sami', password: 'correct-horse' }),
      });
      const cookie = login.headers.get('set-cookie')!.split(';')[0]!;

      await fetch(`${base}/api/deployments/d`, {
        method: 'PUT',
        headers: rootAuth,
        body: JSON.stringify({
          spec: 'version: 1\nstack: s\naxes:\n  - name: a\n    up: "echo hi"\n    assert_gone: "true"\n',
        }),
      });
      const started = (await (
        await fetch(`${base}/api/deployments/d/up`, { method: 'POST', headers: rootAuth, body: '{}' })
      ).json()) as { job: { id: string } };
      const url = `${base}/api/jobs/${started.job.id}/stream`;

      expect((await fetch(url)).status).toBe(401);

      const streamed = await fetch(url, { headers: { cookie } });
      expect(streamed.status).toBe(200);
      expect(streamed.headers.get('content-type')).toContain('text/event-stream');
      // Actually read a frame — a 200 with a dead body would pass a header-only assertion.
      const reader = streamed.body!.getReader();
      const first = await Promise.race([
        reader.read().then((x) => new TextDecoder().decode(x.value ?? new Uint8Array())),
        new Promise<string>((res) => setTimeout(() => res(''), 3000)),
      ]);
      expect(first).toContain('data:');
      await reader.cancel();
    } finally {
      await s.stop();
    }
  });
});

describe('every route sits inside the error-mapping try', () => {
  /*
   * The 0.10.0 defect: the users/tokens/me routes were placed between the two try blocks in api.ts,
   * so an AuthError escaped to Bun's default handler — HTTP 500 with an HTML error page for what is
   * plainly a validation failure. The route working is not enough; it has to fail correctly.
   */
  test('a validation failure on /api/users is 400 JSON, never a 500 HTML page', async () => {
    const s = await bootServer({ tag: 'map', token: 'tok' });
    const { base } = s;
    const headers = { authorization: 'Bearer tok', 'content-type': 'application/json' };
    try {
      for (const body of [
        { username: 'BAD NAME', password: 'longenough' },
        { username: 'ok', password: 'x' },
      ]) {
        const r = await fetch(`${base}/api/users`, { method: 'POST', headers, body: JSON.stringify(body) });
        expect(r.status).toBe(400);
        expect(r.headers.get('content-type')).toContain('application/json');
        // The message is the diagnosis — an empty 400 would be almost as unhelpful as the 500.
        expect(((await r.json()) as { error: string }).error.length).toBeGreaterThan(10);
      }
      // The happy path is unaffected by the move.
      const ok = await fetch(`${base}/api/users`, {
        method: 'POST',
        headers,
        body: JSON.stringify({ username: 'sami', password: 'longenough' }),
      });
      expect(ok.status).toBe(201);
    } finally {
      await s.stop();
    }
  });
});

describe('a disconnected SSE client must not break the job it was watching', () => {
  /*
   * Found as cross-test pollution: the SSE-over-cookie test cancels its reader, and the job finishing
   * afterwards threw `Invalid state: Controller is already closed` from inside JobRegistry's
   * `finally` — attributed to whichever test happened to be running. Two defects, one symptom: the
   * stream had no `cancel` handler so the subscription outlived the client, and the fanout was
   * unguarded so a throwing subscriber aborted job cleanup.
   */
  test('cancelling the stream mid-job leaves the job able to finish cleanly', async () => {
    const srv = await bootServer({ tag: 'sse2', token: 'tok' });
    const { base } = srv;
    const headers = { authorization: 'Bearer tok', 'content-type': 'application/json' };
    try {
      await fetch(`${base}/api/deployments/d`, {
        method: 'PUT',
        headers,
        body: JSON.stringify({
          // Long enough that the stream is cancelled while the job is still running.
          spec: 'version: 1\nstack: s\naxes:\n  - name: a\n    up: "sleep 0.6; echo hi"\n    assert_gone: "true"\n',
        }),
      });
      const started = (await (
        await fetch(`${base}/api/deployments/d/up`, { method: 'POST', headers, body: '{}' })
      ).json()) as { job: { id: string } };
      const jobId = started.job.id;

      // Open the stream, read one frame, then hang up — the browser-closes-the-tab case.
      const s = await fetch(`${base}/api/jobs/${jobId}/stream`, { headers });
      const reader = s.body!.getReader();
      await reader.read();
      await reader.cancel();

      // The job must still reach a terminal state. Before the fix the throw happened in `finally`,
      // *before* `endedAt` was set and the lock released — so the stack stayed locked forever.
      let state = 'running';
      for (let i = 0; i < 60 && state === 'running'; i++) {
        await new Promise((r) => setTimeout(r, 100));
        const j = (await (await fetch(`${base}/api/jobs/${jobId}`, { headers })).json()) as {
          job: { state: string; endedAt?: number };
        };
        state = j.job.state;
        if (state !== 'running') expect(j.job.endedAt).toBeDefined();
      }
      expect(state).not.toBe('running');

      // And the stack lock was released: a second job on the same stack is accepted, not 409'd.
      const second = await fetch(`${base}/api/deployments/d/verify`, { method: 'POST', headers, body: '{}' });
      expect(second.status).toBe(202);
    } finally {
      await srv.stop();
    }
  });
});

describe('duplicate session cookies — the lockout that only clearing cookies used to fix', () => {
  const TOKEN = 'root-machine-token-value';

  test('a dead cookie ahead of the live one no longer locks the operator out', async () => {
    /*
     * A browser can hold TWO cookies named pstack_session — one set with `Secure` over https and
     * one over plain http, or a survivor from a previous server database — and it sends both, the
     * stale one often first (RFC 6265 sorts older/longer-path cookies ahead). The server used to
     * read only the first: login "succeeded", every request 401'd, and no amount of re-logging-in
     * could fix it, because the new cookie never dislodged the dead duplicate.
     */
    const s = await bootServer({ tag: 'cookie', token: TOKEN });
    const { base, H } = s;
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: H,
        body: JSON.stringify({ username: 'alice', password: 'a-long-password-here' }),
      });
      const login = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'alice', password: 'a-long-password-here' }),
      });
      const live = /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';
      expect(live).not.toBe('');

      const me = (cookie: string) => fetch(`${base}/api/auth/me`, { headers: { cookie } });
      // The dead candidate FIRST — the exact arrangement that used to 401 forever.
      expect((await me(`pstack_session=dead-stale-value; pstack_session=${live}`)).status).toBe(200);
      // Order-independent, and a lone dead cookie still refuses.
      expect((await me(`pstack_session=${live}; pstack_session=dead-stale-value`)).status).toBe(200);
      expect((await me('pstack_session=dead-stale-value')).status).toBe(401);

      // Logout revokes EVERY candidate: with duplicates, revoking only the first can leave the
      // session the browser uses next time alive.
      await fetch(`${base}/api/auth/logout`, {
        method: 'POST',
        headers: { cookie: `pstack_session=dead-stale-value; pstack_session=${live}` },
      });
      expect((await me(`pstack_session=${live}`)).status).toBe(401);
    } finally {
      await s.stop();
    }
  });

  test('a junk bearer does not cancel a live session — the "opening Settings signs me out" lockout', async () => {
    /*
     * The second way one browser locked itself out. Settings holds the access token in an input the
     * browser autofills on sight (it was `type="password"`; managers fill those and ignore
     * `autocomplete="off"`), the UI persists whatever lands there and attaches it to every request,
     * and a non-matching bearer used to be a hard refusal — the cookie was never consulted. One
     * autofill and every route 401'd until site data was cleared.
     *
     * A bad bearer now falls through to the cookie. It grants nothing that the caller did not
     * already hold, which the last two assertions are here to prove.
     */
    const s = await bootServer({ tag: 'cookie2', token: TOKEN });
    const { base, H } = s;
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: H,
        body: JSON.stringify({ username: 'alice', password: 'a-long-password-here' }),
      });
      const login = await fetch(`${base}/api/auth/login`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: 'alice', password: 'a-long-password-here' }),
      });
      const live = /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';

      // A real data route, not just /auth/me: the lockout was total.
      const get = (headers: Record<string, string>) => fetch(`${base}/api/deployments`, { headers });
      const cookie = `pstack_session=${live}`;

      for (const junk of ['hunter2', 'pstack_pat_not-a-real-token', TOKEN.slice(0, -1)]) {
        const r = await get({ cookie, authorization: `Bearer ${junk}` });
        expect(`${junk} → ${r.status}`).toBe(`${junk} → 200`);
      }
      // …and it grants nothing on its own: the same junk with no cookie is still refused.
      expect((await get({ authorization: 'Bearer hunter2' })).status).toBe(401);
      expect((await get({})).status).toBe(401);
    } finally {
      await s.stop();
    }
  });
});

describe('changing a password', () => {
  const TOKEN = 'root-machine-token-value';

  test('revokes that account\'s sessions and tokens, and nobody else\'s', async () => {
    /*
     * A password change is normally a response to "someone may have this". Leaving the sessions it
     * was protecting alive would make the change theatre — so this is the assertion that matters
     * more than the hash actually changing.
     */
    const s = await bootServer({ tag: 'pw', token: TOKEN });
    const { base, H } = s;
    try {
      await fetch(`${base}/api/auth/bootstrap`, {
        method: 'POST',
        headers: H,
        body: JSON.stringify({ username: 'alice', password: 'first-password-here' }),
      });
      await fetch(`${base}/api/users`, {
        method: 'POST',
        headers: H,
        body: JSON.stringify({ username: 'bob', password: 'bobs-password-here' }),
      });
      const users = (await (await fetch(`${base}/api/users`, { headers: H })).json()) as {
        users: Array<{ id: number; username: string }>;
      };
      const alice = users.users.find((u) => u.username === 'alice')!;
      const bob = users.users.find((u) => u.username === 'bob')!;

      // Two live sessions, one each.
      const sessionOf = async (username: string, password: string) => {
        const r = await fetch(`${base}/api/auth/login`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ username, password }),
        });
        return /pstack_session=([^;]+)/.exec(r.headers.get('set-cookie') ?? '')?.[1] ?? '';
      };
      const aliceCookie = await sessionOf('alice', 'first-password-here');
      const bobCookie = await sessionOf('bob', 'bobs-password-here');
      const whoami = (cookie: string) =>
        fetch(`${base}/api/auth/me`, { headers: { cookie: `pstack_session=${cookie}` } });
      expect((await whoami(aliceCookie)).status).toBe(200);
      expect((await whoami(bobCookie)).status).toBe(200);

      const changed = await fetch(`${base}/api/users/${alice.id}/password`, {
        method: 'PUT',
        headers: H,
        body: JSON.stringify({ password: 'a-brand-new-password' }),
      });
      expect(changed.status).toBe(200);

      // Alice is out everywhere…
      expect((await whoami(aliceCookie)).status).toBe(401);
      // …and Bob, who had nothing to do with it, is untouched.
      expect((await whoami(bobCookie)).status).toBe(200);
      // The new password works and the old one does not.
      expect(await sessionOf('alice', 'a-brand-new-password')).not.toBe('');
      expect(await sessionOf('alice', 'first-password-here')).toBe('');

      // Too short is refused before anything is written.
      const short = await fetch(`${base}/api/users/${bob.id}/password`, {
        method: 'PUT',
        headers: H,
        body: JSON.stringify({ password: 'short' }),
      });
      expect(short.status).toBe(400);
      expect((await whoami(bobCookie)).status).toBe(200);
    } finally {
      await s.stop();
    }
  }, 20_000);
});
