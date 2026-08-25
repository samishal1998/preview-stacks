/**
 * SSO / OIDC login — the flow, end to end, through the real server. Ported from
 * packages/pstack/test/sso.test.ts ('API: the SSO round trip').
 *
 * Same disciplines as the rest: a real (spawned) server, a real fake IDENTITY PROVIDER on another
 * port (Bun.serve, not a mocked fetch — the token endpoint really does check the PKCE verifier and
 * the id token really is RSA-signed with a key served from a real JWKS document), and assertions on
 * whole response bodies. Every test here was broken on purpose before it was kept.
 *
 * The provider became a LIST (operator-chosen slugs, `?provider=` on /start): the last describe
 * proves the parts that are new, while the single-provider tests above keep pinning that a
 * one-provider host — every host that existed before keys did — behaves exactly as it always has.
 *
 * The original's `forgetDiscovery()` seam has no black-box equivalent: every bootServer is a fresh
 * process, and the one test that needs the discovery cache to lapse mid-run boots with a one-second
 * `discoveryTtlS` and waits it out. The `SsoError instanceof Error` test is in-process and stays behind.
 */
import { afterEach, describe, expect, test } from 'bun:test';
import { bootServer, type BootOptions, type Booted } from '../harness/server.ts';
import { fakeProvider, rsaSigner, type FakeProvider } from '../harness/idp.ts';

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
  async function start(base: string, next?: string, provider?: string) {
    const qs = new URLSearchParams();
    if (provider) qs.set('provider', provider);
    if (next) qs.set('next', next);
    const res = await fetch(`${base}/api/auth/sso/start${qs.size ? `?${qs}` : ''}`, { redirect: 'manual' });
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

    // The login page learns the button exists — before authenticating. `key` is the slug /start
    // selects a provider by (derived here, since the PUT named none), `preset` what it was built from.
    const health = (await (await fetch(`${base}/api/health`)).json()) as { sso: { providers: unknown[] } | null };
    expect(health.sso).toEqual({ providers: [{ key: 'custom', label: 'Corp', preset: 'custom' }] });

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
    const cfg = JSON.parse(seen) as {
      providers: Array<{ key: string; secretSet: boolean; config: { clientId: string } }>;
      callbackUrl: string;
      presets: Array<{ key: string }>;
    };
    // Not even a mask comes back out: a read learns `secretSet` and nothing else.
    expect(cfg.providers.length).toBe(1);
    expect(cfg.providers[0]!.key).toBe('custom');
    expect(cfg.providers[0]!.secretSet).toBe(true);
    expect(cfg.providers[0]!.config.clientId).toBe('cid');
    expect(cfg.callbackUrl).toBe(`${base}/api/auth/sso/callback`);
    expect(cfg.presets.map((x) => x.key)).toEqual(['github', 'gitlab', 'bitbucket', 'google', 'microsoft', 'okta', 'auth0', 'keycloak']);

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

/**
 * The allow rules, end to end: username globs and group membership.
 *
 * A unit test can prove the matchers; only a round trip proves the WIRING — that each rule reaches
 * the gate at all, that the group list is fetched with the access token and only when a rule needs
 * it, and that a groups endpoint which does not answer is a DIFFERENT refusal from "you are not a
 * member". That last distinction is the point: one is fixed by adding somebody to a group, the
 * other is a scope, a rate limit or an outage, and an operator handed the wrong sentence spends the
 * afternoon on the wrong problem.
 *
 * One describe rather than three: the three tests share a boot + fake-provider pair and the login
 * helper, and splitting them would only duplicate that.
 */
describe('API: the SSO allow rules', () => {
  const servers: Array<{ stop: () => void | Promise<void> }> = [];
  afterEach(async () => {
    for (const s of servers.splice(0)) await s.stop();
  });

  /** A booted host and a fake provider, both torn down by the afterEach above. */
  async function pair(): Promise<{ p: FakeProvider; s: Booted }> {
    const p = await fakeProvider();
    servers.push(p);
    const s = await bootServer({ tag: 'rules', sso: { stateTtlS: 300 } });
    servers.push(s);
    return { p, s };
  }

  const put = (s: Booted, body: Record<string, unknown>) =>
    fetch(`${s.base}/api/sso/config`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });

  /** A whole login: /start, hand the provider the challenge it must now expect, then the callback. */
  async function login(s: Booted, p: FakeProvider): Promise<Response> {
    const started = await fetch(`${s.base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(started.status).toBe(302);
    const to = new URL(started.headers.get('location')!);
    p.expectChallenge = to.searchParams.get('code_challenge')!;
    const state = encodeURIComponent(to.searchParams.get('state')!);
    return fetch(`${s.base}/api/auth/sso/callback?code=abc&state=${state}`, { redirect: 'manual' });
  }

  const where = (res: Response) => decodeURIComponent(res.headers.get('location') ?? '');
  const username = async (s: Booted, res: Response) => {
    const cookie = res.headers.get('set-cookie')!.split(';')[0]!;
    const me = (await (await fetch(`${s.base}/api/auth/me`, { headers: { cookie } })).json()) as { user: { username: string } };
    return me.user.username;
  };

  /** Test 2 asserts this refusal; test 3 asserts it is NOT this one. Hence a shared constant. */
  const NOT_A_MEMBER = '/login?sso_error=you are not in a group this host allows to sign in (acme)';

  /** The group rule's whole configuration — the github preset, pointed at the fake provider. */
  const groupsCfg = (p: FakeProvider) => ({
    mode: 'oauth2',
    provider: 'github',
    clientId: 'cid',
    clientSecret: 'shh',
    authorizeUrl: `${p.base}/authorize`,
    tokenUrl: `${p.base}/token`,
    userInfoUrl: `${p.base}/userinfo`,
    // Typed by hand, because moving userinfo off api.github.com stops the preset's endpoints being
    // inherited — the host will not send a self-hosted provider's token to github.com.
    groupsUrl: `${p.base}/groups`,
    // Without read:org the save is refused, so a group rule can never be configured against a token
    // that cannot read the memberships.
    scopes: 'read:user user:email read:org',
    requiredGroups: ['acme'],
  });

  test('allowedUsernames: outside the globs is refused, inside is admitted, and no username at all is refused too', async () => {
    // negative control: delete `AllowedUsernames: cfg.AllowedUsernames,` from the SsoSignInOpts
    // literal in internal/api/routes_auth.go — mallory is admitted and the first assertion fails.
    const { p, s } = await pair();
    expect(
      (
        await put(s, {
          mode: 'oauth2',
          provider: 'custom',
          clientId: 'cid',
          clientSecret: 'shh',
          authorizeUrl: `${p.base}/authorize`,
          tokenUrl: `${p.base}/token`,
          userInfoUrl: `${p.base}/userinfo`,
          claimMap: { subject: 'id', username: 'login', email: 'email' },
          allowedUsernames: ['qa-[0-9]*', 'octo?at'],
        })
      ).status,
    ).toBe(200);

    p.userInfo = { id: 'u-mallory', login: 'mallory', email: 'mallory@example.com' };
    const refused = await login(s, p);
    expect(where(refused)).toBe('/login?sso_error=mallory is not an allowed username');
    expect(refused.headers.get('set-cookie')).toBeNull();

    // Fails CLOSED, the same direction allowedEmailDomains does: a rule and nothing to check it
    // against is a refusal, not a pass. This is exactly what the rule does to every login on a
    // provider that supplies no username claim.
    p.userInfo = { id: 'u-nameless', email: 'nameless@example.com' };
    const nameless = await login(s, p);
    expect(where(nameless)).toBe('/login?sso_error=this provider returned no username, and sign-in is restricted to specific usernames');
    expect(nameless.headers.get('set-cookie')).toBeNull();

    // `octo?at` matched the 'C' as well as a 'c': both sides of the match are folded. The account
    // is created here, by the login that passed — the two refusals above created nothing.
    p.userInfo = { id: 'u-octo', login: 'OctoCat', email: 'octo@example.com' };
    const admitted = await login(s, p);
    expect(where(admitted)).toBe('/');
    expect(await username(s, admitted)).toBe('octocat');

    // No group rule was configured, so the provider was never asked for a membership list — a rule
    // nobody set must not cost a call against somebody's provider.
    expect(p.hits.some((h) => h.endsWith('/groups'))).toBe(false);
  });

  test('requiredGroups: a member is admitted and a non-member is refused, on the same identity', async () => {
    // negative control: delete `RequiredGroups: cfg.RequiredGroups,` from the SsoSignInOpts literal
    // in internal/api/routes_auth.go — the non-member is admitted.
    const { p, s } = await pair();
    expect((await put(s, groupsCfg(p))).status).toBe(200);
    // One person throughout: only the membership changes, so nothing else can explain the two
    // outcomes.
    p.userInfo = { id: 42, login: 'gitter', email: 'gitter@example.com' };

    p.groups = [{ login: 'other-org' }, { login: 'personal' }];
    const refused = await login(s, p);
    expect(where(refused)).toBe(NOT_A_MEMBER);
    expect(refused.headers.get('set-cookie')).toBeNull();

    // Case-folded on both sides: the rule was stored as `acme`, GitHub spells the org `Acme`.
    p.groups = [{ login: 'other-org' }, { login: 'Acme' }];
    const admitted = await login(s, p);
    expect(where(admitted)).toBe('/');
    expect(await username(s, admitted)).toBe('gitter');

    // Both logins asked, and the fake serves /groups only to `Bearer at-1` — so the access token
    // reached the groups endpoint, which nothing short of a round trip can show.
    expect(p.hits.filter((h) => h === 'GET /groups').length).toBe(2);
  });

  test('a groups endpoint that fails refuses the login, and says something other than "not a member"', async () => {
    // negative control: in internal/api/routes_auth.go, drop the `groupsErr = err` carry in the
    // groups-fetch default branch (leave the empty identity.Groups) — the refusal collapses into
    // NOT_A_MEMBER, which is the distinction this test exists for.
    const { p, s } = await pair();
    expect((await put(s, groupsCfg(p))).status).toBe(200);
    p.userInfo = { id: 42, login: 'gitter', email: 'gitter@example.com' };
    // A member — of a list nobody can read. Being in the group must not save the login here.
    p.groups = [{ login: 'acme' }];
    p.groupsStatus = 500;

    const res = await login(s, p);
    expect(where(res)).toBe(
      `/login?sso_error=your group memberships could not be determined, and sign-in is restricted to specific groups — ${p.base}/groups answered 500`,
    );
    expect(where(res)).not.toBe(NOT_A_MEMBER);
    expect(res.headers.get('set-cookie')).toBeNull();
  });
});

/**
 * Several providers at once — the surface that did not exist before the provider became a list.
 * The describes above prove a one-provider host still behaves exactly as it always has; this one
 * proves the new parts: keyed selection on /start, the refusal to guess, the keyed DELETE, the
 * preset enrichment, and a pre-multi-provider config document still applying.
 *
 * Two SEPARATE fake providers with different client ids and different secrets: a login that
 * completes can only have been exchanged against the provider its /start named, because the other
 * one refuses the credential.
 */
describe('API: several sign-in providers', () => {
  const servers: Array<{ stop: () => void | Promise<void> }> = [];
  afterEach(async () => {
    for (const s of servers.splice(0)) await s.stop();
  });

  const put = (s: Booted, body: Record<string, unknown>) =>
    fetch(`${s.base}/api/sso/config`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });
  const health = async (s: Booted) =>
    (((await (await fetch(`${s.base}/api/health`)).json()) as { sso: unknown }).sso as {
      providers: Array<{ key: string; label: string; preset: string }>;
    } | null);
  const startRaw = (s: Booted, qs: string) => fetch(`${s.base}/api/auth/sso/start${qs}`, { redirect: 'manual' });
  const where = (res: Response) => decodeURIComponent(res.headers.get('location') ?? '');

  /** Finish a started login: hand the provider its challenge, spend the callback, ask who you are. */
  async function finish(s: Booted, p: FakeProvider, started: Response): Promise<string> {
    const to = new URL(started.headers.get('location')!);
    p.expectChallenge = to.searchParams.get('code_challenge')!;
    const state = encodeURIComponent(to.searchParams.get('state')!);
    const cb = await fetch(`${s.base}/api/auth/sso/callback?code=abc&state=${state}`, { redirect: 'manual' });
    expect(cb.status).toBe(302);
    expect(cb.headers.get('location')).toBe('/');
    const cookie = cb.headers.get('set-cookie')!.split(';')[0]!;
    const me = (await (await fetch(`${s.base}/api/auth/me`, { headers: { cookie } })).json()) as { user: { username: string } };
    return me.user.username;
  }

  /** A booted host with two rows: an OIDC issuer under `corp`, the github preset under `hub`. */
  async function twoProviders(): Promise<{ s: Booted; corp: FakeProvider; hub: FakeProvider }> {
    const corp = await fakeProvider();
    const hub = await fakeProvider();
    servers.push(corp, hub);
    const s = await bootServer({ tag: 'multi', sso: { stateTtlS: 300 } });
    servers.push(s);
    corp.expectSecret = 'corp-secret';
    const { jwk, sign } = await rsaSigner();
    corp.jwks = { keys: [jwk] };
    corp.idToken = await sign({
      iss: corp.base,
      aud: 'corp-cid',
      sub: 'corp-sub-1',
      exp: Math.floor(Date.now() / 1000) + 300,
      preferred_username: 'alice',
      email: 'alice@example.com',
    });
    hub.expectSecret = 'hub-secret';
    hub.userInfo = { id: 7, login: 'hubber', email: 'hubber@example.com' };
    const corpSaved = await put(s, { key: 'corp', mode: 'oidc', issuer: corp.base, clientId: 'corp-cid', clientSecret: 'corp-secret', label: 'Corp' });
    expect(corpSaved.status).toBe(200);
    expect(((await corpSaved.json()) as { key: string }).key).toBe('corp');
    // The slug is the operator's, not the preset's: the github preset lives under `hub` here.
    const hubSaved = await put(s, {
      key: 'hub',
      mode: 'oauth2',
      provider: 'github',
      clientId: 'hub-cid',
      clientSecret: 'hub-secret',
      authorizeUrl: `${hub.base}/authorize`,
      tokenUrl: `${hub.base}/token`,
      userInfoUrl: `${hub.base}/userinfo`,
    });
    expect(hubSaved.status).toBe(200);
    expect(((await hubSaved.json()) as { key: string }).key).toBe('hub');
    return { s, corp, hub };
  }

  test('two providers: the login page lists both, each /start round-trips against its own, and keyless /start refuses by name', async () => {
    // negative control: in internal/api/routes_auth.go ssoStart, force the keyed branch to
    // `stored = &enabled[0]` after the lookup — ?provider=hub lands on corp's authorize URL, so the
    // origin assertion fails (and ?provider=nope stops refusing).
    const { s, corp, hub } = await twoProviders();

    expect(await health(s)).toEqual({
      providers: [
        { key: 'corp', label: 'Corp', preset: '' },
        { key: 'hub', label: 'GitHub', preset: 'github' },
      ],
    });

    // No key and two buttons: which directory vouches for you is not this API's guess to make.
    const undecided = await startRaw(s, '');
    expect(undecided.status).toBe(302);
    expect(where(undecided)).toBe('/login?sso_error=this host has several sign-in providers (corp, hub) — pass ?provider= to choose one');

    const toCorp = await startRaw(s, '?provider=corp');
    expect(toCorp.status).toBe(302);
    const corpUrl = new URL(toCorp.headers.get('location')!);
    expect(corpUrl.origin).toBe(corp.base);
    expect(corpUrl.searchParams.get('client_id')).toBe('corp-cid');
    expect(await finish(s, corp, toCorp)).toBe('alice');

    const toHub = await startRaw(s, '?provider=hub');
    expect(toHub.status).toBe(302);
    const hubUrl = new URL(toHub.headers.get('location')!);
    expect(hubUrl.origin).toBe(hub.base);
    expect(hubUrl.searchParams.get('client_id')).toBe('hub-cid');
    expect(await finish(s, hub, toHub)).toBe('hubber');

    // A key that names nothing: one sentence, because the enabled set IS what the login page offers.
    expect(where(await startRaw(s, '?provider=nope'))).toBe('/login?sso_error=no sign-in provider "nope" is configured on this host');
  });

  test('the keyed DELETE removes one provider, and the survivor answers keyless /start again', async () => {
    // negative control: in internal/api/routes_auth.go deleteSsoProvider, replace the
    // `s.auth.DeleteSsoProvider(key)` call with `true, error(nil)` — corp survives, the login page
    // still lists two providers, and keyless /start still refuses to choose.
    const { s, hub } = await twoProviders();

    // The bare DELETE and the keyless PUT both refuse to guess between two rows, naming them.
    const bareDelete = await fetch(`${s.base}/api/sso/config`, { method: 'DELETE', headers: s.H });
    expect(bareDelete.status).toBe(400);
    expect(((await bareDelete.json()) as { error: string }).error).toBe(
      'this host has several sign-in providers (corp, hub) — DELETE /api/sso/config/<key> to say which one',
    );
    const keylessPut = await put(s, { mode: 'oidc', issuer: 'https://accounts.example.com', clientId: 'c', clientSecret: 's' });
    expect(keylessPut.status).toBe(400);
    expect(((await keylessPut.json()) as { error: string }).error).toBe(
      'this host has several sign-in providers (corp, hub) — pass "key" to say which one to replace',
    );

    expect((await fetch(`${s.base}/api/sso/config/corp`, { method: 'DELETE', headers: s.H })).status).toBe(200);
    // Gone from the login page; the other row untouched.
    expect(await health(s)).toEqual({ providers: [{ key: 'hub', label: 'GitHub', preset: 'github' }] });
    // Deleting the deleted names the key.
    const again = await fetch(`${s.base}/api/sso/config/corp`, { method: 'DELETE', headers: s.H });
    expect(again.status).toBe(404);
    expect(((await again.json()) as { error: string }).error).toBe('no sign-in provider "corp" is configured');

    // Exactly ONE enabled provider left: the keyless /start of every pre-multi-provider login link
    // picks it, and the whole round trip still completes.
    const started = await startRaw(s, '');
    expect(started.status).toBe(302);
    expect(new URL(started.headers.get('location')!).origin).toBe(hub.base);
    expect(await finish(s, hub, started)).toBe('hubber');
  });

  test('a pre-multi-provider config document carrying the single "sso" object still applies, under the derived key', async () => {
    // negative control: empty the foldLegacySSO body in internal/config/config.go — nothing is
    // created, and every assertion below fails.
    const s = await bootServer({ tag: 'legacy', sso: { stateTtlS: 300 } });
    servers.push(s);
    // The 0.30.0 export shape verbatim: version 1, ONE provider under "sso".
    const oldDoc = {
      version: 1,
      pstackVersion: '0.30.0',
      exportedAt: 1,
      skipped: [], users: [], tokens: [], vars: [], notifiers: [],
      sso: {
        config: {
          mode: 'oidc', enabled: true, clientId: 'pstack',
          allowedEmailDomains: ['example.com'], allowedUsernames: [], requiredGroups: [],
          defaultRole: 'admin', label: 'accounts.example.com',
          discoveryUrl: 'https://accounts.example.com/',
          provider: '', authorizeUrl: '', tokenUrl: '', userInfoUrl: '', emailsUrl: '', groupsUrl: '',
          scopes: 'openid profile email',
          claimMap: { subject: 'sub', username: 'preferred_username', email: 'email', name: 'name', avatar: 'picture' },
        },
        clientSecret: 'the-client-secret',
      },
      registries: [], routing: [], specs: [],
    };
    const applied = await fetch(`${s.base}/api/config`, { method: 'POST', headers: s.H, body: JSON.stringify(oldDoc) });
    expect(applied.status).toBe(200);
    expect(((await applied.json()) as { created: string[] }).created).toContain('sso provider oidc');

    // Landed under the key the config derives when it names no provider — the same key the
    // database migration gives the same config, so an old export and an old database agree.
    const cfg = (await (await fetch(`${s.base}/api/sso/config`, { headers: s.H })).json()) as {
      providers: Array<{ key: string; secretSet: boolean; config: { discoveryUrl: string } }>;
    };
    expect(cfg.providers.length).toBe(1);
    expect(cfg.providers[0]!.key).toBe('oidc');
    expect(cfg.providers[0]!.secretSet).toBe(true);
    expect(cfg.providers[0]!.config.discoveryUrl).toBe('https://accounts.example.com/');
    // Enabled, so the login page draws its button — nobody re-saved anything on the new host.
    expect(await health(s)).toEqual({ providers: [{ key: 'oidc', label: 'accounts.example.com', preset: '' }] });
  });

  test('the presets carry their setup walkthrough, and a template issuer is refused at save', async () => {
    // negative control: in internal/sso/sso.go ParseConfig, drop the
    // `strings.Contains(discoveryURL, "<")` refusal — the save fails later, at discovery, with a
    // different sentence, and the assertion on the placeholder sentence fails.
    const s = await bootServer({ tag: 'presets', sso: { stateTtlS: 300 } });
    servers.push(s);
    const cfg = (await (await fetch(`${s.base}/api/sso/config`, { headers: s.H })).json()) as {
      providers: unknown[];
      presets: Array<{ key: string; mode: string; buttonLabel: string; setupUrl: string; setupHint: string; discoveryUrl: string; authorizeUrl: string }>;
    };
    expect(cfg.providers).toEqual([]);
    expect(cfg.presets.map((p) => p.key)).toEqual(['github', 'gitlab', 'bitbucket', 'google', 'microsoft', 'okta', 'auth0', 'keycloak']);
    for (const p of cfg.presets) {
      // Every preset can say where to create the app and what to expect — that is what one is FOR.
      expect(['oauth2', 'oidc']).toContain(p.mode);
      expect(p.buttonLabel).toMatch(/^Continue with /);
      expect(p.setupUrl).toMatch(/^https:\/\//);
      expect(p.setupHint.length).toBeGreaterThan(0);
      if (p.mode === 'oidc') expect(p.discoveryUrl.length).toBeGreaterThan(0);
      else expect(p.authorizeUrl).toMatch(/^https:\/\//);
    }
    expect(cfg.presets.find((p) => p.key === 'google')!.discoveryUrl).toBe('https://accounts.google.com');
    // Microsoft's discovery URL is a template ON PURPOSE (see the preset's comment in
    // internal/sso/sso.go for why the tenantless documents cannot be preset). Saving it with the
    // placeholder still in place is refused with the sentence that says what to replace.
    expect(cfg.presets.find((p) => p.key === 'microsoft')!.discoveryUrl).toBe('https://login.microsoftonline.com/<tenant-id>/v2.0');
    const templated = await put(s, { provider: 'microsoft', clientId: 'c', clientSecret: 's' });
    expect(templated.status).toBe(400);
    expect(((await templated.json()) as { error: string }).error).toBe(
      'discoveryUrl "https://login.microsoftonline.com/<tenant-id>/v2.0" still carries a <placeholder> — replace it with your own value (your tenant, domain or realm) before saving',
    );
    expect(await health(s)).toBeNull();
  });
});
