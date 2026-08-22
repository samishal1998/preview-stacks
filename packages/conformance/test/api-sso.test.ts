/**
 * SSO / OIDC login — the flow, end to end, through the real server. Ported from
 * packages/pstack/test/sso.test.ts ('API: the SSO round trip').
 *
 * Same disciplines as the rest: a real (spawned) server, a real fake IDENTITY PROVIDER on another
 * port (Bun.serve, not a mocked fetch — the token endpoint really does check the PKCE verifier and
 * the id token really is RSA-signed with a key served from a real JWKS document), and assertions on
 * whole response bodies. Every test here was broken on purpose before it was kept.
 *
 * The original's `forgetDiscovery()` seam has no black-box equivalent: every bootServer is a fresh
 * process, and the one test that needs the discovery cache to lapse mid-run boots with a one-second
 * `discoveryTtlS` and waits it out. The `SsoError instanceof Error` test is in-process and stays behind.
 */
import { afterEach, describe, expect, test } from 'bun:test';
import { bootServer, type BootOptions, type Booted } from '../harness/server.ts';
import { fakeProvider, rsaSigner } from '../harness/idp.ts';

describe('API: the SSO round trip', () => {
  const servers: Array<{ stop: () => void | Promise<void> }> = [];
  afterEach(async () => {
    for (const s of servers.splice(0)) await s.stop();
  });

  const boot = async (sso: BootOptions['sso'] = { stateTtlS: 300 }): Promise<Booted> => {
    const s = await bootServer({ tag: 'flow', sso });
    servers.push(s);
    return s;
  };

  const configure = (s: Booted, body: Record<string, unknown>) =>
    fetch(`${s.base}/api/sso/config`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });

  /** Start the flow and hand back the state + the challenge the provider must now expect. */
  async function start(base: string, next?: string) {
    const res = await fetch(`${base}/api/auth/sso/start${next ? `?next=${encodeURIComponent(next)}` : ''}`, { redirect: 'manual' });
    expect(res.status).toBe(302);
    const to = new URL(res.headers.get('location')!);
    return {
      to,
      state: to.searchParams.get('state')!,
      challenge: to.searchParams.get('code_challenge')!,
    };
  }

  test('OAuth 2.0: authorize → callback → a session, with PKCE actually checked', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base } = s;
    p.userInfo = { id: 77, login: 'octocat', email: null, name: 'The Octocat', avatar_url: 'https://x/y.png' };
    p.emails = [{ email: 'octo@example.com', primary: true, verified: true }];

    expect(
      (await configure(s, {
        mode: 'oauth2',
        provider: 'custom',
        label: 'Corp',
        clientId: 'cid',
        clientSecret: 'shh',
        authorizeUrl: `${p.base}/authorize`,
        tokenUrl: `${p.base}/token`,
        userInfoUrl: `${p.base}/userinfo`,
        emailsUrl: `${p.base}/emails`,
        claimMap: { subject: 'id', username: 'login', email: 'email', name: 'name', avatar: 'avatar_url' },
      })).status,
    ).toBe(200);

    // The login page learns the button exists — before authenticating.
    const health = (await (await fetch(`${base}/api/health`)).json()) as { sso: { enabled: boolean; label: string } | null };
    expect(health.sso).toEqual({ enabled: true, label: 'Corp' });

    const { to, state, challenge } = await start(base, '/deployments/pr-7');
    expect(to.origin).toBe(p.base);
    expect(to.searchParams.get('response_type')).toBe('code');
    expect(to.searchParams.get('client_id')).toBe('cid');
    expect(to.searchParams.get('code_challenge_method')).toBe('S256');
    expect(to.searchParams.get('redirect_uri')).toBe(`${base}/api/auth/sso/callback`);
    p.expectChallenge = challenge;

    const cb = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(state)}`, { redirect: 'manual' });
    expect(cb.status).toBe(302);
    expect(cb.headers.get('location')).toBe('/deployments/pr-7');
    const cookie = cb.headers.get('set-cookie')!;
    expect(cookie).toMatch(/^pstack_session=pstack_ses_[0-9a-f]{64}; HttpOnly/);

    // That cookie is an ordinary session: the gate accepts it everywhere.
    const me = (await (await fetch(`${base}/api/auth/me`, { headers: { cookie: cookie.split(';')[0]! } })).json()) as {
      root: boolean;
      user: { username: string; email: string };
    };
    expect(me.root).toBe(false);
    expect(me.user.username).toBe('octocat');
    // The profile hid the address, so the emails endpoint was consulted — and only because of that.
    expect(me.user.email).toBe('octo@example.com');
  });

  test('OIDC: discovery, a signed id token, and no second round trip when the claims are complete', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base } = s;
    const { jwk, sign } = await rsaSigner();
    p.jwks = { keys: [jwk] };

    expect((await configure(s, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh', label: 'Corp SSO' })).status).toBe(200);
    // Validated AT SAVE TIME: the discovery document was fetched before anyone tried to log in.
    expect(p.hits.some((h) => h.includes('openid-configuration'))).toBe(true);

    const now = Math.floor(Date.now() / 1000);
    p.idToken = await sign({
      iss: p.base,
      aud: 'cid',
      sub: 'oidc-sub-1',
      exp: now + 300,
      preferred_username: 'alice',
      email: 'alice@example.com',
      email_verified: true,
      name: 'Alice',
    });

    const { state, challenge } = await start(base);
    p.expectChallenge = challenge;
    const before = p.hits.filter((h) => h.endsWith('/userinfo')).length;
    const cb = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(state)}`, { redirect: 'manual' });
    expect(cb.status).toBe(302);
    expect(cb.headers.get('location')).toBe('/');
    // The id token carried everything, so userinfo was never called.
    expect(p.hits.filter((h) => h.endsWith('/userinfo')).length).toBe(before);

    const cookie = cb.headers.get('set-cookie')!.split(';')[0]!;
    const me = (await (await fetch(`${base}/api/auth/me`, { headers: { cookie } })).json()) as { user: { username: string; email: string } };
    expect(me.user).toMatchObject({ username: 'alice', email: 'alice@example.com' });
  });

  test('an id token signed by the wrong key never becomes a session', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base } = s;
    p.jwks = { keys: [(await rsaSigner('real')).jwk] };
    const attacker = await rsaSigner('real'); // same kid, different key
    await configure(s, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh' });
    p.idToken = await attacker.sign({ iss: p.base, aud: 'cid', sub: 'admin', exp: Math.floor(Date.now() / 1000) + 300 });

    const { state, challenge } = await start(base);
    p.expectChallenge = challenge;
    const cb = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(state)}`, { redirect: 'manual' });
    expect(cb.status).toBe(302);
    expect(cb.headers.get('location')).toMatch(/^\/login\?sso_error=.*did%20not%20verify/);
    expect(cb.headers.get('set-cookie')).toBeNull();
  });

  test('unknown state, replayed state and expired state all refuse — and none of them mints a session', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot({ stateTtlS: 1 });
    const { base } = s;
    p.userInfo = { sub: 'u1', preferred_username: 'u1', email: 'u1@example.com' };
    await configure(s, {
      mode: 'oauth2',
      provider: 'custom',
      clientId: 'cid',
      clientSecret: 'shh',
      authorizeUrl: `${p.base}/authorize`,
      tokenUrl: `${p.base}/token`,
      userInfoUrl: `${p.base}/userinfo`,
    });

    const fail = async (qs: string, pattern: RegExp) => {
      const res = await fetch(`${base}/api/auth/sso/callback?${qs}`, { redirect: 'manual' });
      expect(res.status).toBe(302);
      expect(decodeURIComponent(res.headers.get('location')!)).toMatch(pattern);
      expect(res.headers.get('set-cookie')).toBeNull();
      return res;
    };

    await fail('code=abc&state=never-issued', /expired or was already used/);
    await fail('state=only-state', /did not return an authorization code/);
    await fail('code=only-code', /did not return an authorization code/);

    // Replay: the same state twice. The first spends it.
    const first = await start(base);
    p.expectChallenge = first.challenge;
    const ok = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(first.state)}`, { redirect: 'manual' });
    expect(ok.headers.get('set-cookie')).toBeTruthy();
    await fail(`code=abc&state=${encodeURIComponent(first.state)}`, /expired or was already used/);

    // Expiry: this server was booted with a one-second TTL.
    const stale = await start(base);
    p.expectChallenge = stale.challenge;
    await Bun.sleep(1_100);
    await fail(`code=abc&state=${encodeURIComponent(stale.state)}`, /expired or was already used/);
  });

  test("the provider's own refusal and a failed exchange both land on the login page", async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base } = s;
    await configure(s, {
      mode: 'oauth2',
      provider: 'custom',
      clientId: 'cid',
      clientSecret: 'shh',
      authorizeUrl: `${p.base}/authorize`,
      tokenUrl: `${p.base}/token`,
      userInfoUrl: `${p.base}/userinfo`,
    });

    // The consent screen said no. Its own words reach the operator.
    const denied = await fetch(`${base}/api/auth/sso/callback?error=access_denied&error_description=the+user+said+no`, { redirect: 'manual' });
    expect(decodeURIComponent(denied.headers.get('location')!)).toBe('/login?sso_error=access_denied: the user said no');
    expect(denied.headers.get('set-cookie')).toBeNull();

    // The exchange failed (a wrong client secret looks exactly like this).
    p.tokenStatus = 401;
    p.tokenBody = JSON.stringify({ error: 'invalid_client', error_description: 'bad secret' });
    const s1 = await start(base);
    const broken = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(s1.state)}`, { redirect: 'manual' });
    expect(decodeURIComponent(broken.headers.get('location')!)).toMatch(/token exchange failed \(401\): invalid_client: bad secret/);
    expect(broken.headers.get('set-cookie')).toBeNull();

    // A 200 that carries an `error` is GitHub's shape and is NOT success.
    p.tokenStatus = 200;
    p.tokenBody = JSON.stringify({ error: 'bad_verification_code', error_description: 'expired' });
    const s2 = await start(base);
    const alsoBroken = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(s2.state)}`, { redirect: 'manual' });
    expect(decodeURIComponent(alsoBroken.headers.get('location')!)).toMatch(/bad_verification_code: expired/);
    expect(alsoBroken.headers.get('set-cookie')).toBeNull();

    // A PKCE verifier that does not match is refused by the provider, not by us — which is the point.
    p.tokenBody = null;
    p.expectChallenge = 'something-else';
    const s3 = await start(base);
    const pkceFail = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(s3.state)}`, { redirect: 'manual' });
    expect(decodeURIComponent(pkceFail.headers.get('location')!)).toMatch(/PKCE verifier does not match/);
  });

  test('the client secret has no read path, and the mask round-trips without clobbering it', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base, H } = s;
    p.userInfo = { sub: 'u1', preferred_username: 'u1' };
    p.expectSecret = 'the-real-secret';
    const body = {
      mode: 'oauth2',
      provider: 'custom',
      clientId: 'cid',
      clientSecret: 'the-real-secret',
      authorizeUrl: `${p.base}/authorize`,
      tokenUrl: `${p.base}/token`,
      userInfoUrl: `${p.base}/userinfo`,
      allowedEmailDomains: ['example.com'],
    };
    expect((await configure(s, body)).status).toBe(200);

    const read = await fetch(`${base}/api/sso/config`, { headers: H });
    const seen = await read.text();
    expect(seen).not.toContain('the-real-secret');
    const cfg = JSON.parse(seen) as { configured: boolean; clientSecret: string; callbackUrl: string; config: { clientId: string }; presets: Array<{ key: string }> };
    expect(cfg.configured).toBe(true);
    expect(cfg.clientSecret).toBe('••••••••');
    expect(cfg.callbackUrl).toBe(`${base}/api/auth/sso/callback`);
    expect(cfg.config.clientId).toBe('cid');
    expect(cfg.presets.map((x) => x.key)).toEqual(['github', 'gitlab', 'bitbucket']);

    // Submitting the mask back keeps the stored secret rather than storing the bullets.
    expect((await configure(s, { ...body, clientSecret: '••••••••', label: 'Renamed' })).status).toBe(200);
    const { state, challenge } = await start(base);
    p.expectChallenge = challenge;
    // It still works, which is only true if the real secret survived.
    const cb = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(state)}`, { redirect: 'manual' });
    expect(decodeURIComponent(cb.headers.get('location') ?? '')).toBe(
      '/login?sso_error=this provider returned no email address, and sign-in is restricted to specific email domains',
    );

    // Deleting forgets the provider; /start then has nothing to send anyone to.
    expect((await fetch(`${base}/api/sso/config`, { method: 'DELETE', headers: H })).status).toBe(200);
    const gone = await fetch(`${base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(gone.status).toBe(302);
    expect(decodeURIComponent(gone.headers.get('location')!)).toMatch(/not configured/);
    expect(((await (await fetch(`${base}/api/health`)).json()) as { sso: unknown }).sso).toBeNull();
  });

  test('a provider that advertises only client_secret_basic gets HTTP Basic, not a body field', async () => {
    const p = await fakeProvider();
    servers.push(p);
    const s = await boot();
    const { base } = s;
    p.basicAuth = true;
    // A secret with characters that MUST be percent-encoded before the base64 (RFC 6749 §2.3.1).
    p.expectSecret = 'se%cr+et/=';
    p.userInfo = { sub: 'basic-1', preferred_username: 'basil', email: 'basil@example.com' };
    const { jwk, sign } = await rsaSigner();
    p.jwks = { keys: [jwk] };
    expect((await configure(s, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'se%cr+et/=' })).status).toBe(200);
    p.idToken = await sign({
      iss: p.base,
      aud: 'cid',
      sub: 'basic-1',
      exp: Math.floor(Date.now() / 1000) + 300,
      preferred_username: 'basil',
      email: 'basil@example.com',
    });

    const { state, challenge } = await start(base);
    p.expectChallenge = challenge;
    const cb = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(state)}`, { redirect: 'manual' });
    expect(decodeURIComponent(cb.headers.get('location') ?? '')).toBe('/');
    const cookie = cb.headers.get('set-cookie')!.split(';')[0]!;
    expect(((await (await fetch(`${base}/api/auth/me`, { headers: { cookie } })).json()) as { user: { username: string } }).user.username).toBe('basil');
  });

  test('/start is reached by NAVIGATION, so its failures land on the login page too', async () => {
    // A one-second discovery TTL stands in for the original's forgetDiscovery(): once the provider
    // is gone, the cached document must lapse before /start is asked again.
    const s = await boot({ stateTtlS: 300, discoveryTtlS: 1 });
    const { base } = s;
    // Nothing configured at all.
    const none = await fetch(`${base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(none.status).toBe(302);
    expect(decodeURIComponent(none.headers.get('location')!)).toBe('/login?sso_error=single sign-on is not configured on this host');

    // Configured, then the provider goes away — the realistic case, and the one that must not be
    // a raw JSON body in somebody's address bar.
    const p = await fakeProvider();
    servers.push(p);
    expect((await configure(s, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh' })).status).toBe(200);
    p.stop();
    await Bun.sleep(1_100);
    const dead = await fetch(`${base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(dead.status).toBe(302);
    expect(decodeURIComponent(dead.headers.get('location')!)).toMatch(/^\/login\?sso_error=could not reach/);
  });

  test('a share link cannot read or write the provider configuration', async () => {
    const s = await boot();
    const { base, H } = s;
    await fetch(`${base}/api/deployments/pr-1`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({ spec: 'version: 1\nstack: pr-1\naxes:\n  - name: a\n    up: "true"\n' }),
    });
    const mint = (await (await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: '{}' })).json()) as { token: string };
    const res = await fetch(`${base}/api/sso/config?token=${mint.token}`);
    expect(res.status).toBe(403);
  });

  test("a bad issuer is refused at SAVE time, not discovered at somebody's first login", async () => {
    const s = await boot();
    const { base } = s;
    const res = await configure(s, { mode: 'oidc', issuer: 'https://127.0.0.1:1/nope', clientId: 'c', clientSecret: 's' });
    expect(res.status).toBe(400);
    expect(((await res.json()) as { error: string }).error).toMatch(/could not reach|answered/);
    expect(((await (await fetch(`${base}/api/health`)).json()) as { sso: unknown }).sso).toBeNull();
  });
});
