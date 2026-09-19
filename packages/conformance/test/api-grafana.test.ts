/**
 * Grafana sign-in over HTTP: forwardAuth's verify, start, and the callback verify answers itself.
 *
 * No Grafana runs. The docker shim plays the control stack's grafana container, which is how pstack
 * learns Grafana is on (inspect.GrafanaOn, read at Start before it serves). Each verify request plays
 * Traefik: it sends the X-Forwarded-* and Sec-Fetch-* headers forwardAuth passes on and reads the
 * answer with redirect: 'manual', as forwardAuth does.
 */
import { describe, expect, test } from 'bun:test';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';
import { bootServer, type Booted } from '../harness/server.ts';

/** The control stack's grafana container. Arms are the argv after bash unquotes it. */
const GRAFANA_SHIM = [
  `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'gr4f' ;;`,
  `  "inspect gr4f") printf '%s\\n' '[{"Id":"gr4f","Name":"/pstack-control-grafana-1","Config":{"Image":"grafana/grafana:13.2.1","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"grafana"}},"State":{"Status":"running"}}]' ;;`,
].join('\n');

const DOMAIN = 'preview.example.com';
const G = `https://grafana.${DOMAIN}`;
const C = `https://control.${DOMAIN}`;
const SESSION = '__Host-pstack_grafana=';
const STATE = '__Host-pstack_grafana_state=';
const ATTRS = '; Path=/; Secure; HttpOnly; SameSite=Lax';
/** A top-level navigation: the only request verify starts a sign-in for. */
const NAV = { 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document' };
/** A fetch from Grafana's frontend. */
const XHR = { 'sec-fetch-mode': 'cors', 'sec-fetch-dest': 'empty' };

describe('Grafana sign-in', () => {
  /** A server on a host whose docker answers `arms`. stop() removes the shim too. */
  const boot = async (arms: string): Promise<Booted> => {
    const docker = dockerShim(arms);
    const s = await bootServer({ tag: 'grafana', domain: DOMAIN, pathPrefix: docker.dir });
    return { ...s, stop: async () => { await s.stop(); docker.remove(); } };
  };

  /** What forwardAuth sends pstack for one request to grafana.<domain>. GET by default: with no method, the Origin rule refuses. */
  const verify = (s: Booted, uri: string, h: Record<string, string> = {}) =>
    fetch(`${s.base}/api/auth/grafana/verify`, {
      redirect: 'manual',
      headers: { 'x-forwarded-uri': uri, 'x-forwarded-method': 'GET', 'x-forwarded-host': `grafana.${DOMAIN}`, ...h },
    });

  /** Bootstrap sami (admin) with the bearer and log in: the pstack_session value. */
  const pstackSession = async (s: Booted): Promise<string> => {
    const made = await fetch(`${s.base}/api/auth/bootstrap`, { method: 'POST', headers: s.H, body: JSON.stringify({ username: 'sami', password: 'correct-horse' }) });
    expect(made.status).toBe(201);
    const login = await fetch(`${s.base}/api/auth/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'sami', password: 'correct-horse' }) });
    expect(login.status).toBe(200);
    return /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';
  };

  /** A browser at G/d/x with no Grafana cookie, signed in to pstack: verify → start → callback. Every answer, for the caller to assert on. */
  const grafanaSignIn = async (s: Booted, session: string) => {
    const first = await verify(s, '/d/x', NAV);
    const state = first.headers.getSetCookie().find((c) => c.startsWith(STATE))?.slice(STATE.length).split(';')[0] ?? '';
    const startAt = new URL(first.headers.get('location') ?? '/', C);
    const start = await fetch(`${s.base}${startAt.pathname}${startAt.search}`, { redirect: 'manual', headers: { cookie: `pstack_session=${session}` } });
    const code = new URL(start.headers.get('location') ?? '/', G).searchParams.get('code') ?? '';
    const callback = await verify(s, `/-/pstack/callback?code=${code}`, { ...NAV, cookie: `${STATE}${state}` });
    /** `__Host-pstack_grafana=<hash>.<mac>`, ready to send back. */
    const cookie = callback.headers.getSetCookie().find((c) => c.startsWith(SESSION))?.split(';')[0] ?? '';
    return { first, state, start, code, callback, cookie };
  };

  // negative control: in grafanaVerify, drop `baseURL(s.opts.Domain, r)+` from the start redirect (a
  // relative Location) — the first Location assertion fails.
  test('verify → start → callback → 204: a pstack session becomes a Grafana sign-in', async () => {
    const s = await boot(GRAFANA_SHIM);
    try {
      const session = await pstackSession(s);
      const f = await grafanaSignIn(s, session);

      // No Grafana cookie, a top-level navigation: an absolute 302 to start, and the state cookie.
      expect(f.first.status).toBe(302);
      expect(f.state).toMatch(/^[A-Za-z0-9_-]{43}$/);
      expect(f.first.headers.get('location')).toBe(`${C}/api/auth/grafana/start?state=${f.state}&next=%2Fd%2Fx`);
      expect(f.first.headers.getSetCookie()).toEqual([`${STATE}${f.state}${ATTRS}; Max-Age=900`]);

      // A fetch, and a navigation inside an <iframe>: a bare 401. Nothing to follow, no state cookie.
      for (const h of [XHR, { 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe' }]) {
        const r = await verify(s, '/d/x', h);
        expect(r.status).toBe(401);
        expect(r.headers.get('location')).toBeNull();
        expect(r.headers.getSetCookie()).toEqual([]);
      }

      // start, signed in: a one-time code for Grafana's callback. Signed out: the login page, returning to start.
      expect(f.start.status).toBe(302);
      expect(f.start.headers.get('location')).toStartWith(`${G}/-/pstack/callback?code=`);
      expect(f.code).toMatch(/^[A-Za-z0-9_-]{43}$/);
      const signedOut = await fetch(`${s.base}/api/auth/grafana/start?state=${f.state}&next=%2Fd%2Fx`, { redirect: 'manual' });
      expect(signedOut.status).toBe(302);
      expect(signedOut.headers.get('location')).toBe(`/login?next=${encodeURIComponent(`/api/auth/grafana/start?state=${f.state}&next=${encodeURIComponent('/d/x')}`)}`);

      // The callback, answered inside verify: back to /d/x with the Grafana cookie, the state cookie cleared.
      expect(f.callback.status).toBe(302);
      expect(f.callback.headers.get('location')).toBe(`${G}/d/x`);
      expect(f.cookie).toMatch(/^__Host-pstack_grafana=[0-9a-f]{64}\.[A-Za-z0-9_-]{43}$/);
      expect(f.callback.headers.getSetCookie()).toEqual([`${f.cookie}${ATTRS}`, `${STATE}${ATTRS}; Max-Age=0`]);

      // The same code again is spent: sign-in starts afresh (a new state cookie) and no Grafana cookie.
      const replay = await verify(s, `/-/pstack/callback?code=${f.code}`, { ...NAV, cookie: `${STATE}${f.state}` });
      expect(replay.status).toBe(302);
      expect(replay.headers.get('location')).toMatch(/^https:\/\/control\.preview\.example\.com\/api\/auth\/grafana\/start\?state=[A-Za-z0-9_-]{43}&next=%2F$/);
      expect(replay.headers.getSetCookie().some((c) => c.startsWith(STATE))).toBe(true);
      expect(replay.headers.getSetCookie().some((c) => c.startsWith(SESSION))).toBe(false);

      // Every request after: 204 with the two headers Grafana trusts. An unsafe method from a preview's origin: 403.
      const ok = await verify(s, '/api/user', { ...XHR, cookie: f.cookie });
      expect(ok.status).toBe(204);
      expect(ok.headers.get('x-webauth-user')).toBe('sami');
      expect(ok.headers.get('x-webauth-role')).toBe('Admin');
      const post = await verify(s, '/api/dashboards/db', { ...XHR, cookie: f.cookie, 'x-forwarded-method': 'POST', origin: `https://pr-1.${DOMAIN}` });
      expect(post.status).toBe(403);
      expect(await post.text()).toBe('Cross-origin request refused.\n');
    } finally {
      await s.stop();
    }
  }, 30_000);

  // negative control: in grafanaUser, cache hash → user in a package-level sync.Map (Load before
  // s.auth.SessionHashUser, Store after) — the verify after sign-out answers 204.
  test('a pstack sign-out ends Grafana access on the next request', async () => {
    const s = await boot(GRAFANA_SHIM);
    try {
      const session = await pstackSession(s);
      const { cookie } = await grafanaSignIn(s, session);
      // The cookie works first, so the 401 below is the sign-out's doing.
      expect((await verify(s, '/api/user', { ...XHR, cookie })).status).toBe(204);

      const out = await fetch(`${s.base}/api/auth/logout`, { method: 'POST', headers: { cookie: `pstack_session=${session}` } });
      expect(out.status).toBe(200);
      const after = await verify(s, '/api/user', { ...XHR, cookie });
      expect(after.status).toBe(401);
      expect(after.headers.get('x-webauth-user')).toBeNull();
    } finally {
      await s.stop();
    }
  }, 30_000);

  // negative control: delete the service-hostname refusal at the top of handle() — `/` serves the
  // embedded UI, 200 text/html.
  test('grafana.<domain> reaching pstack answers 503, never the UI', async () => {
    const s = await boot(GRAFANA_SHIM);
    try {
      // What the wake catch-all forwards once Grafana's own router is gone: the Host, nothing else.
      const res = await fetch(`${s.base}/`, { headers: { host: `grafana.${DOMAIN}` } });
      expect(res.status).toBe(503);
      expect(res.headers.get('content-type')).toStartWith('text/plain');
      expect(await res.text()).toBe('Grafana is not running.\n');
    } finally {
      await s.stop();
    }
  }, 30_000);

  // negative control: drop `&& s.grafana.Load()` from grafanaOn() — the ALWAYS_OK host's health names
  // Grafana and its verify answers 302.
  test('off: /api/health has no grafana key, and verify and start answer 404', async () => {
    const on = await boot(GRAFANA_SHIM);
    try {
      // Start reads docker before it serves, so the first health answer is already right.
      expect(((await (await fetch(`${on.base}/api/health`)).json()) as { grafana?: unknown }).grafana).toBe(G);
    } finally {
      await on.stop();
    }
    // Same domain and token: the 404s below come from docker listing no grafana container, nothing else.
    const off = await boot(ALWAYS_OK);
    try {
      const health = (await (await fetch(`${off.base}/api/health`)).json()) as Record<string, unknown>;
      expect('grafana' in health).toBe(false);
      for (const r of [
        await verify(off, '/d/x', NAV),
        await fetch(`${off.base}/api/auth/grafana/start?state=${'a'.repeat(43)}&next=%2F`, { redirect: 'manual' }),
      ]) {
        expect(r.status).toBe(404);
        expect(await r.text()).toBe('Not found.\n');
      }
    } finally {
      await off.stop();
    }
  }, 30_000);
});
