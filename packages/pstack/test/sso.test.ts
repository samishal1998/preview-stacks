/**
 * SSO / OIDC login.
 *
 * Same disciplines as the rest: a real server on port 0, a real fake IDENTITY PROVIDER on another
 * one (Bun.serve, not a mocked fetch — the token endpoint really does check the PKCE verifier and
 * the id token really is RSA-signed with a key served from a real JWKS document), and assertions on
 * whole response bodies. Every test here was broken on purpose before it was kept.
 */

import { afterEach, describe, expect, test } from 'bun:test';
import { createServer } from '../src/api.ts';
import { Store } from '../src/store.ts';
import { Auth } from '../src/auth.ts';
import {
  MemoryTransientStore,
  OIDC_CLAIMS,
  PRESETS,
  SqliteTransientStore,
  SsoError,
  codeChallenge,
  emailAllowed,
  mapClaims,
  parseSsoConfig,
  pkce,
  primaryEmail,
  safeNext,
  sanitizeUsername,
  verifyIdToken,
  forgetDiscovery,
  type TransientStore,
} from '../src/sso.ts';

const TOKEN = 'test-token-0123456789abcdef';
const H = { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' };
const tmpd = (tag: string) =>
  `${process.env.TMPDIR ?? '/tmp'}/pstack-sso-${tag}-${process.pid}-${Math.trunc(performance.now() * 1000)}`;

const b64url = (b: ArrayBuffer | Uint8Array) => Buffer.from(b as ArrayBuffer).toString('base64url');

// ── the fake identity provider ────────────────────────────────────────────────────────────────────

type FakeProvider = {
  server: ReturnType<typeof Bun.serve>;
  base: string;
  /** Set by a test from the /start redirect; /token refuses a verifier that does not hash to it. */
  expectChallenge: string;
  /** What the token endpoint demands as `client_secret`. A wrong one is refused, as a real one is. */
  expectSecret: string;
  /** Advertise ONLY `client_secret_basic`, and read the credential from the header instead. */
  basicAuth: boolean;
  /** Flip to make the token endpoint fail the way a wrong client secret does. */
  tokenStatus: number;
  tokenBody: string | null;
  userInfo: Record<string, unknown>;
  emails: unknown;
  /** Signed and served on demand; null means "answer without an id_token". */
  idToken: string | null;
  jwks: { keys: unknown[] };
  hits: string[];
};

async function fakeProvider(): Promise<FakeProvider> {
  const p: FakeProvider = {
    server: null as never,
    base: '',
    expectChallenge: '',
    expectSecret: 'shh',
    basicAuth: false,
    tokenStatus: 200,
    tokenBody: null,
    userInfo: {},
    emails: [],
    idToken: null,
    jwks: { keys: [] },
    hits: [],
  };
  p.server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    async fetch(req) {
      const url = new URL(req.url);
      p.hits.push(`${req.method} ${url.pathname}`);
      const j = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

      if (url.pathname === '/.well-known/openid-configuration') {
        return j({
          issuer: p.base,
          authorization_endpoint: `${p.base}/authorize`,
          token_endpoint: `${p.base}/token`,
          userinfo_endpoint: `${p.base}/userinfo`,
          jwks_uri: `${p.base}/jwks`,
          token_endpoint_auth_methods_supported: p.basicAuth ? ['client_secret_basic'] : ['client_secret_post', 'client_secret_basic'],
        });
      }
      if (url.pathname === '/jwks') return j(p.jwks);
      if (url.pathname === '/token') {
        if (p.tokenBody !== null || p.tokenStatus !== 200) {
          return new Response(p.tokenBody ?? JSON.stringify({ error: 'bad_verification_code', error_description: 'the code is expired' }), {
            status: p.tokenStatus,
            headers: { 'content-type': 'application/json' },
          });
        }
        const form = new URLSearchParams(await req.text());
        // The real client-secret check. Without it, a bug that stored the UI's mask in place of the
        // secret would sail through every test here.
        if (p.basicAuth) {
          // RFC 6749 §2.3.1: both halves are form-urlencoded before the base64.
          const raw = /^Basic (.+)$/.exec(req.headers.get('authorization') ?? '')?.[1] ?? '';
          const [id, sec] = Buffer.from(raw, 'base64').toString('utf8').split(':');
          if (form.has('client_secret')) return j({ error: 'invalid_request', error_description: 'secret sent in the body too' }, 400);
          let decoded: [string, string];
          try {
            decoded = [decodeURIComponent(id ?? ''), decodeURIComponent(sec ?? '')];
          } catch {
            // A credential that was NOT percent-encoded before the base64 does not survive the
            // decode a real provider performs — which is the whole point of the encoding.
            return j({ error: 'invalid_client', error_description: `basic auth is not form-urlencoded: "${raw}"` }, 401);
          }
          if (decoded[0] !== 'cid' || decoded[1] !== p.expectSecret) {
            return j({ error: 'invalid_client', error_description: `basic auth was "${raw}"` }, 401);
          }
        } else if (form.get('client_secret') !== p.expectSecret) {
          return j({ error: 'invalid_client', error_description: `client_secret was "${form.get('client_secret')}"` }, 401);
        }
        // The real PKCE check. Without this the tests would pass with the verifier never sent.
        if (!form.get('code_verifier') || codeChallenge(form.get('code_verifier')!) !== p.expectChallenge) {
          return j({ error: 'invalid_grant', error_description: 'PKCE verifier does not match' }, 400);
        }
        if (form.get('grant_type') !== 'authorization_code') return j({ error: 'unsupported_grant_type' }, 400);
        return j({ access_token: 'at-1', token_type: 'bearer', ...(p.idToken ? { id_token: p.idToken } : {}) });
      }
      if (url.pathname === '/userinfo') {
        if (req.headers.get('authorization') !== 'Bearer at-1') return j({ error: 'unauthorized' }, 401);
        return j(p.userInfo);
      }
      if (url.pathname === '/emails') return j(p.emails);
      return new Response('no', { status: 404 });
    },
  });
  p.base = `http://127.0.0.1:${p.server.port}`;
  return p;
}

/** An RS256 keypair, its JWKS entry, and a signer — the id-token half of mode A, for real. */
async function rsaSigner(kid = 'k1') {
  const pair = (await crypto.subtle.generateKey(
    { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' },
    true,
    ['sign', 'verify'],
  )) as CryptoKeyPair;
  const pub = (await crypto.subtle.exportKey('jwk', pair.publicKey)) as Record<string, unknown>;
  const jwk = { ...pub, kid, alg: 'RS256', use: 'sig' };
  const sign = async (claims: Record<string, unknown>, header: Record<string, unknown> = {}) => {
    const h = b64url(Buffer.from(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid, ...header })));
    const b = b64url(Buffer.from(JSON.stringify(claims)));
    const sig = await crypto.subtle.sign('RSASSA-PKCS1-v1_5', pair.privateKey, new TextEncoder().encode(`${h}.${b}`));
    return `${h}.${b}.${b64url(sig)}`;
  };
  return { jwk, sign };
}

// ── the transient store, one suite against every backend ──────────────────────────────────────────

describe('sso: the transient store', () => {
  const backends: Array<[string, () => TransientStore]> = [
    ['memory', () => new MemoryTransientStore()],
    [
      'sqlite',
      () => {
        const store = new Store(tmpd('ts'));
        return new SqliteTransientStore(store.db);
      },
    ],
  ];

  for (const [name, make] of backends) {
    test(`${name}: round-trips, expires, deletes, and takes exactly once`, async () => {
      const s = make();
      await s.set('a', 'value-a', 60);
      expect(await s.get('a')).toBe('value-a');

      // Expired reads as absent — not as a stale value, and not as an error.
      await s.set('b', 'value-b', -1);
      expect(await s.get('b')).toBeNull();

      await s.set('c', 'value-c', 60);
      await s.delete('c');
      expect(await s.get('c')).toBeNull();

      // `take` is the single-use primitive the callback depends on.
      await s.set('d', 'value-d', 60);
      expect(await s.take('d')).toBe('value-d');
      expect(await s.take('d')).toBeNull();
      expect(await s.get('d')).toBeNull();

      // An expired row cannot be taken either.
      await s.set('e', 'value-e', -1);
      expect(await s.take('e')).toBeNull();

      expect(await s.get('never-written')).toBeNull();
    });
  }
});

// ── PKCE ──────────────────────────────────────────────────────────────────────────────────────────

describe('sso: PKCE', () => {
  test('S256 matches the RFC 7636 appendix B vector', () => {
    expect(codeChallenge('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
  });

  test('a generated verifier is in the legal length range and derives its own challenge', () => {
    const { verifier, challenge } = pkce();
    expect(verifier.length).toBeGreaterThanOrEqual(43);
    expect(verifier.length).toBeLessThanOrEqual(128);
    expect(verifier).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(challenge).toBe(codeChallenge(verifier));
    expect(pkce().verifier).not.toBe(verifier);
  });

  test('redirectAfterLogin only ever survives as a same-origin path', () => {
    expect(safeNext('/deployments/pr-7')).toBe('/deployments/pr-7');
    // Every one of these is absolute to a browser.
    expect(safeNext('//evil.example')).toBe('/');
    expect(safeNext('https://evil.example')).toBe('/');
    expect(safeNext('/\\evil.example')).toBe('/');
    expect(safeNext(null)).toBe('/');
  });
});

// ── claim mapping ─────────────────────────────────────────────────────────────────────────────────

describe('sso: claim mapping', () => {
  test('a GitHub-shaped payload and an OIDC-shaped one both produce the expected user', () => {
    const github = PRESETS.find((p) => p.key === 'github')!;
    expect(
      mapClaims(
        { id: 4242, login: 'octocat', email: 'octo@example.com', name: 'The Octocat', avatar_url: 'https://x/y.png' },
        github.claimMap,
      ),
    ).toEqual({
      subject: '4242', // a numeric id is still an identity
      username: 'octocat',
      email: 'octo@example.com',
      name: 'The Octocat',
      avatar: 'https://x/y.png',
      emailVerified: null, // GitHub never says, and null is NOT permission to adopt an account
    });

    expect(
      mapClaims(
        { sub: 'abc|123', preferred_username: 'Alice', email: 'Alice@Example.com', email_verified: true, name: 'Alice', picture: 'https://x/a.png' },
        OIDC_CLAIMS,
      ),
    ).toEqual({
      subject: 'abc|123',
      username: 'Alice',
      email: 'alice@example.com', // lower-cased, so the allow-list and the lookup agree
      name: 'Alice',
      avatar: 'https://x/a.png',
      emailVerified: true,
    });
  });

  test('one dotted path is supported (Bitbucket nests the avatar) and a missing subject is fatal', () => {
    const bb = PRESETS.find((p) => p.key === 'bitbucket')!;
    const mapped = mapClaims({ uuid: '{u-1}', username: 'bb', display_name: 'BB', links: { avatar: { href: 'https://x/b.png' } } }, bb.claimMap);
    expect(mapped.avatar).toBe('https://x/b.png');
    expect(mapped.subject).toBe('{u-1}');
    expect(() => mapClaims({ login: 'nope' }, OIDC_CLAIMS)).toThrow(/no "sub"/);
  });

  test('the primary verified address is found in both shapes a provider serves', () => {
    expect(primaryEmail([{ email: 'a@x.com', primary: false, verified: true }, { email: 'b@x.com', primary: true, verified: true }]))
      .toEqual({ email: 'b@x.com', verified: true });
    expect(primaryEmail({ values: [{ email: 'C@X.com', is_primary: true, is_confirmed: false }] }))
      .toEqual({ email: 'c@x.com', verified: false });
    expect(primaryEmail([])).toBeNull();
    expect(primaryEmail(null)).toBeNull();
  });

  test('the email allow-list fails CLOSED', () => {
    expect(emailAllowed('', [])).toBe(true); // empty list allows everything, including no email
    expect(emailAllowed('', ['example.com'])).toBe(false); // a list and no email is a refusal
    expect(emailAllowed('a@example.com', ['example.com'])).toBe(true);
    expect(emailAllowed('a@sub.example.com', ['example.com'])).toBe(true);
    expect(emailAllowed('a@notexample.com', ['example.com'])).toBe(false);
    expect(emailAllowed('a@example.com.evil.net', ['example.com'])).toBe(false);
  });

  test('a username is sanitized into the local shape, and is never the identity', () => {
    expect(sanitizeUsername('Octo Cat', 's1')).toBe('octo-cat');
    expect(sanitizeUsername('alice@example.com', 's1')).toBe('alice-example.com');
    expect(sanitizeUsername('!!', 'sub-42')).toBe('user-sub42');
    expect(sanitizeUsername('', 'x')).toBe('user-x');
    expect(sanitizeUsername('A'.repeat(60), 's')).toHaveLength(32);
  });
});

// ── configuration ─────────────────────────────────────────────────────────────────────────────────

describe('sso: configuration', () => {
  test('a preset fills the endpoints in and the operator can still override one', () => {
    const cfg = parseSsoConfig({ mode: 'oauth2', provider: 'github', clientId: 'cid' });
    expect(cfg.authorizeUrl).toBe('https://github.com/login/oauth/authorize');
    expect(cfg.userInfoUrl).toBe('https://api.github.com/user');
    expect(cfg.scopes).toBe('read:user user:email');
    expect(cfg.claimMap.subject).toBe('id');
    expect(cfg.label).toBe('GitHub');

    const selfHosted = parseSsoConfig({
      mode: 'oauth2',
      provider: 'gitlab',
      clientId: 'cid',
      authorizeUrl: 'https://git.corp.example/oauth/authorize',
      tokenUrl: 'https://git.corp.example/oauth/token',
      userInfoUrl: 'https://git.corp.example/api/v4/user',
    });
    expect(selfHosted.authorizeUrl).toBe('https://git.corp.example/oauth/authorize');
    expect(selfHosted.claimMap.username).toBe('username'); // the preset's mapping survives
  });

  test('a preset\'s emails endpoint is never inherited by a self-hosted userinfo endpoint', () => {
    // GitHub keeps the address off the profile, so the preset carries a second URL...
    expect(parseSsoConfig({ mode: 'oauth2', provider: 'github', clientId: 'c' }).emailsUrl).toBe('https://api.github.com/user/emails');
    // ...but pointing userinfo at your own host must NOT then send that host's access token to
    // api.github.com. Inheriting it here would leak a live credential to a third party.
    expect(
      parseSsoConfig({ mode: 'oauth2', provider: 'github', clientId: 'c', userInfoUrl: 'https://ghe.corp.example/api/v3/user' }).emailsUrl,
    ).toBe('');
    expect(
      parseSsoConfig({
        mode: 'oauth2',
        provider: 'github',
        clientId: 'c',
        userInfoUrl: 'https://ghe.corp.example/api/v3/user',
        emailsUrl: 'https://ghe.corp.example/api/v3/user/emails',
      }).emailsUrl,
    ).toBe('https://ghe.corp.example/api/v3/user/emails');
  });

  test('what it refuses', () => {
    expect(() => parseSsoConfig({ mode: 'oauth2', provider: 'custom', clientId: 'c' })).toThrow(/authorizeUrl and tokenUrl/);
    expect(() => parseSsoConfig({ mode: 'oidc' })).toThrow(/clientId/);
    expect(() => parseSsoConfig({ mode: 'oidc', clientId: 'c' })).toThrow(/issuer or discoveryUrl/);
    expect(() => parseSsoConfig({ mode: 'saml', clientId: 'c' })).toThrow(/mode must be/);
    expect(() => parseSsoConfig({ mode: 'oidc', clientId: 'c', issuer: 'http://idp.example.com' })).toThrow(/https/);
    expect(() => parseSsoConfig({ mode: 'oauth2', provider: 'nope', clientId: 'c' })).toThrow(/unknown provider/);
    // http IS allowed on loopback, or nothing could be developed against a local IdP.
    expect(parseSsoConfig({ mode: 'oidc', clientId: 'c', issuer: 'http://127.0.0.1:9999' }).discoveryUrl).toContain('127.0.0.1');
  });

  test('claim overrides merge onto the preset, and email domains are normalized', () => {
    const cfg = parseSsoConfig({
      mode: 'oauth2',
      provider: 'github',
      clientId: 'c',
      claimMap: { email: 'work_email' },
      allowedEmailDomains: ['@Example.COM', '', 'other.test'],
    });
    expect(cfg.claimMap).toEqual({ subject: 'id', username: 'login', email: 'work_email', name: 'name', avatar: 'avatar_url' });
    expect(cfg.allowedEmailDomains).toEqual(['example.com', 'other.test']);
  });
});

// ── id token verification ─────────────────────────────────────────────────────────────────────────

describe('sso: id token verification', () => {
  test('a well-formed token verifies; every defect is fatal', async () => {
    const p = await fakeProvider();
    try {
      const { jwk, sign } = await rsaSigner();
      p.jwks = { keys: [jwk] };
      const args = { issuer: p.base, clientId: 'cid', jwksUri: `${p.base}/jwks` };
      const now = Math.floor(Date.now() / 1000);
      const good = { iss: p.base, aud: 'cid', sub: 'u1', exp: now + 300, iat: now };

      expect((await verifyIdToken(await sign(good), args)).sub).toBe('u1');
      // aud may be an ARRAY, and must contain the client id.
      expect((await verifyIdToken(await sign({ ...good, aud: ['other', 'cid'] }), args)).sub).toBe('u1');

      await expect(verifyIdToken(await sign({ ...good, aud: 'someone-else' }), args)).rejects.toThrow(/audience/);
      await expect(verifyIdToken(await sign({ ...good, aud: ['a', 'b'] }), args)).rejects.toThrow(/audience/);
      await expect(verifyIdToken(await sign({ ...good, iss: 'https://evil.example' }), args)).rejects.toThrow(/issuer/);
      await expect(verifyIdToken(await sign({ ...good, exp: now - 120 }), args)).rejects.toThrow(/expired/);
      await expect(verifyIdToken(await sign({ ...good, exp: undefined }), args)).rejects.toThrow(/expired/);
      await expect(verifyIdToken(await sign({ ...good, nbf: now + 600 }), args)).rejects.toThrow(/not valid yet/);

      // 60s of skew is tolerated — a provider's clock is not this one's.
      expect((await verifyIdToken(await sign({ ...good, exp: now - 30 }), args)).sub).toBe('u1');

      // THE FORGERIES. `none` and HMAC are refused before a key is even looked up.
      const claims = b64url(Buffer.from(JSON.stringify(good)));
      const noneHead = b64url(Buffer.from(JSON.stringify({ alg: 'none', typ: 'JWT' })));
      await expect(verifyIdToken(`${noneHead}.${claims}.`, args)).rejects.toThrow(/not accepted/);
      const hsHead = b64url(Buffer.from(JSON.stringify({ alg: 'HS256', typ: 'JWT' })));
      await expect(verifyIdToken(`${hsHead}.${claims}.aaaa`, args)).rejects.toThrow(/not accepted/);

      // A body swapped after signing does not verify.
      const token = await sign(good);
      const [h, , sig] = token.split('.');
      const forged = b64url(Buffer.from(JSON.stringify({ ...good, sub: 'admin' })));
      await expect(verifyIdToken(`${h}.${forged}.${sig}`, args)).rejects.toThrow(/did not verify/);

      await expect(verifyIdToken('not-a-jwt', args)).rejects.toThrow(/three dot-separated/);
    } finally {
      p.server.stop(true);
    }
  });

  test('an unknown kid refetches the JWKS once — key rotation heals without a restart', async () => {
    const p = await fakeProvider();
    try {
      const first = await rsaSigner('old');
      p.jwks = { keys: [first.jwk] };
      const args = { issuer: p.base, clientId: 'cid', jwksUri: `${p.base}/jwks` };
      const now = Math.floor(Date.now() / 1000);
      const good = { iss: p.base, aud: 'cid', sub: 'u1', exp: now + 300 };
      expect((await verifyIdToken(await first.sign(good), args)).sub).toBe('u1');
      const fetches = p.hits.filter((h) => h.endsWith('/jwks')).length;

      // The provider rotates. The cached document has no key with this kid.
      const second = await rsaSigner('new');
      p.jwks = { keys: [second.jwk] };

      // Inside the cooldown the refetch is REFUSED — that floor is what stops a junk kid from
      // turning every request into a fetch against the provider.
      await expect(verifyIdToken(await second.sign(good), args)).rejects.toThrow(/no signing key matching kid "new"/);
      expect(p.hits.filter((h) => h.endsWith('/jwks')).length).toBe(fetches);

      // Past it, rotation heals with nothing restarted.
      expect((await verifyIdToken(await second.sign(good), { ...args, jwksCooldownMs: 0 })).sub).toBe('u1');
      expect(p.hits.filter((h) => h.endsWith('/jwks')).length).toBe(fetches + 1);
    } finally {
      p.server.stop(true);
    }
  });
});

// ── provisioning ──────────────────────────────────────────────────────────────────────────────────

describe('sso: provisioning', () => {
  const identity = (over: Record<string, unknown> = {}) => ({
    subject: 's-1',
    username: 'Octo Cat',
    email: 'octo@example.com',
    name: 'The Octocat',
    avatar: '',
    emailVerified: true as boolean | null,
    ...over,
  });

  test('first login creates, second login finds the same user, a changed email does not duplicate', async () => {
    const auth = new Auth(new Store(tmpd('prov')));
    const first = await auth.ssoSignIn('github', identity());
    expect(first.how).toBe('created');
    expect(first.user.username).toBe('octo-cat');
    expect(first.user.email).toBe('octo@example.com');

    const second = await auth.ssoSignIn('github', identity());
    expect(second.how).toBe('linked');
    expect(second.user.id).toBe(first.user.id);
    expect(second.session).not.toBe(first.session);

    // The person changed their address upstream. Same subject ⇒ same account, with the new email.
    const third = await auth.ssoSignIn('github', identity({ email: 'new@example.com' }));
    expect(third.how).toBe('linked');
    expect(third.user.id).toBe(first.user.id);
    expect(third.user.email).toBe('new@example.com');
    expect(auth.listUsers()).toHaveLength(1);

    // A different subject at the same provider is a different person, even at the same address.
    const other = await auth.ssoSignIn('github', identity({ subject: 's-2', email: 'new@example.com' }));
    expect(other.how).toBe('created');
    expect(other.user.id).not.toBe(first.user.id);
    // The username was taken, so it was suffixed rather than linked to whoever holds it.
    expect(other.user.username).toBe('octo-cat-2');
    expect(auth.listUsers()).toHaveLength(2);
  });

  test('an existing local account is adopted only on a VERIFIED email', async () => {
    const auth = new Auth(new Store(tmpd('adopt')));
    const local = await auth.createUser('alice', 'password123', { email: 'alice@example.com' });

    // The provider never said the address was verified: adopting here would be account takeover.
    const unverified = await auth.ssoSignIn('okta', identity({ subject: 'x1', email: 'alice@example.com', emailVerified: null }));
    expect(unverified.how).toBe('created');
    expect(unverified.user.id).not.toBe(local.id);

    const verified = await auth.ssoSignIn('okta', identity({ subject: 'x2', email: 'alice@example.com', emailVerified: true }));
    expect(verified.how).toBe('created'); // two accounts now share the address ⇒ ambiguous, so no adoption

    const clean = new Auth(new Store(tmpd('adopt2')));
    const bob = await clean.createUser('bob', 'password123', { email: 'bob@example.com' });
    const adopted = await clean.ssoSignIn('okta', identity({ subject: 'y1', email: 'bob@example.com', emailVerified: true }));
    expect(adopted.how).toBe('adopted');
    expect(adopted.user.id).toBe(bob.id);
    expect(clean.listUsers()).toHaveLength(1);
    // And it is a link from then on — the email is no longer consulted.
    expect((await clean.ssoSignIn('okta', identity({ subject: 'y1', email: 'moved@example.com', emailVerified: true }))).how).toBe('linked');
  });

  test('the email allow-list refuses before anything is created', async () => {
    const auth = new Auth(new Store(tmpd('allow')));
    await expect(auth.ssoSignIn('okta', identity({ email: 'x@outside.test' }), { allowedEmailDomains: ['example.com'] }))
      .rejects.toThrow(/not in an allowed email domain/);
    // And a provider that returned no address at all is refused too, not waved through.
    await expect(auth.ssoSignIn('okta', identity({ email: '' }), { allowedEmailDomains: ['example.com'] }))
      .rejects.toThrow(/no email address/);
    expect(auth.listUsers()).toHaveLength(0);
  });

  test('an SSO account has no usable password, and a session behaves like any other', async () => {
    const auth = new Auth(new Store(tmpd('pw')));
    const { user, session } = await auth.ssoSignIn('github', identity());
    expect(auth.sessionUser(session)?.id).toBe(user.id);
    // The row is ordinary — there is no null-password state for anything to treat as "no password
    // needed" — but nobody holds the 32 random bytes behind it.
    await expect(auth.login(user.username, '')).rejects.toThrow(/invalid username or password/);
    await expect(auth.login(user.username, 'password')).rejects.toThrow(/invalid username or password/);
  });
});

// ── the flow, end to end, through the real server ────────────────────────────────────────────────

describe('API: the SSO round trip', () => {
  const servers: Array<{ stop: (f?: boolean) => void }> = [];
  afterEach(() => {
    for (const s of servers.splice(0)) s.stop(true);
    forgetDiscovery();
  });

  const boot = (stateTtlS = 300) => {
    const server = createServer({
      dataDir: tmpd('flow'),
      port: 0,
      host: '127.0.0.1',
      token: TOKEN,
      sso: { stateTtlS },
    });
    servers.push(server);
    return { server, base: `http://127.0.0.1:${server.port}` };
  };

  const configure = (base: string, body: Record<string, unknown>) =>
    fetch(`${base}/api/sso/config`, { method: 'PUT', headers: H, body: JSON.stringify(body) });

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
    servers.push(p.server);
    const { base } = boot();
    p.userInfo = { id: 77, login: 'octocat', email: null, name: 'The Octocat', avatar_url: 'https://x/y.png' };
    p.emails = [{ email: 'octo@example.com', primary: true, verified: true }];

    expect(
      (await configure(base, {
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
    servers.push(p.server);
    const { base } = boot();
    const { jwk, sign } = await rsaSigner();
    p.jwks = { keys: [jwk] };

    expect((await configure(base, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh', label: 'Corp SSO' })).status).toBe(200);
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
    servers.push(p.server);
    const { base } = boot();
    p.jwks = { keys: [(await rsaSigner('real')).jwk] };
    const attacker = await rsaSigner('real'); // same kid, different key
    await configure(base, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh' });
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
    servers.push(p.server);
    const { base } = boot(1);
    p.userInfo = { sub: 'u1', preferred_username: 'u1', email: 'u1@example.com' };
    await configure(base, {
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
    servers.push(p.server);
    const { base } = boot();
    await configure(base, {
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
    const s = await start(base);
    const broken = await fetch(`${base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(s.state)}`, { redirect: 'manual' });
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
    servers.push(p.server);
    const { base } = boot();
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
    expect((await configure(base, body)).status).toBe(200);

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
    expect((await configure(base, { ...body, clientSecret: '••••••••', label: 'Renamed' })).status).toBe(200);
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
    servers.push(p.server);
    const { base } = boot();
    p.basicAuth = true;
    // A secret with characters that MUST be percent-encoded before the base64 (RFC 6749 §2.3.1).
    p.expectSecret = 'se%cr+et/=';
    p.userInfo = { sub: 'basic-1', preferred_username: 'basil', email: 'basil@example.com' };
    const { jwk, sign } = await rsaSigner();
    p.jwks = { keys: [jwk] };
    expect((await configure(base, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'se%cr+et/=' })).status).toBe(200);
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
    const { base } = boot();
    // Nothing configured at all.
    const none = await fetch(`${base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(none.status).toBe(302);
    expect(decodeURIComponent(none.headers.get('location')!)).toBe('/login?sso_error=single sign-on is not configured on this host');

    // Configured, then the provider goes away — the realistic case, and the one that must not be
    // a raw JSON body in somebody's address bar.
    const p = await fakeProvider();
    servers.push(p.server);
    expect((await configure(base, { mode: 'oidc', issuer: p.base, clientId: 'cid', clientSecret: 'shh' })).status).toBe(200);
    p.server.stop(true);
    forgetDiscovery();
    const dead = await fetch(`${base}/api/auth/sso/start`, { redirect: 'manual' });
    expect(dead.status).toBe(302);
    expect(decodeURIComponent(dead.headers.get('location')!)).toMatch(/^\/login\?sso_error=could not reach/);
  });

  test('a share link cannot read or write the provider configuration', async () => {
    const { base } = boot();
    await fetch(`${base}/api/deployments/pr-1`, {
      method: 'PUT',
      headers: H,
      body: JSON.stringify({ spec: 'version: 1\nstack: pr-1\naxes:\n  - name: a\n    up: "true"\n' }),
    });
    const mint = (await (await fetch(`${base}/api/deployments/pr-1/share`, { method: 'POST', headers: H, body: '{}' })).json()) as { token: string };
    const res = await fetch(`${base}/api/sso/config?token=${mint.token}`);
    expect(res.status).toBe(403);
  });

  test('a bad issuer is refused at SAVE time, not discovered at somebody\'s first login', async () => {
    const { base } = boot();
    const res = await configure(base, { mode: 'oidc', issuer: 'https://127.0.0.1:1/nope', clientId: 'c', clientSecret: 's' });
    expect(res.status).toBe(400);
    expect(((await res.json()) as { error: string }).error).toMatch(/could not reach|answered/);
    expect(((await (await fetch(`${base}/api/health`)).json()) as { sso: unknown }).sso).toBeNull();
  });

  test('SsoError is what the flow throws, not a bare Error', () => {
    expect(new SsoError('x')).toBeInstanceOf(Error);
  });
});
