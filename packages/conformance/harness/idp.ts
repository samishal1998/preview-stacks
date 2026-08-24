/**
 * A fake identity provider: discovery, JWKS, an authorize endpoint that bounces straight back, a
 * token endpoint that REALLY checks the PKCE verifier and the client secret, userinfo, emails,
 * groups. Ported from packages/pstack/test/sso.test.ts so the SSO round trip can be graded
 * black-box.
 */
const b64url = (b: ArrayBuffer | Uint8Array) => Buffer.from(b as ArrayBuffer).toString('base64url');

export type FakeProvider = {
  base: string;
  /** The token endpoint refuses a verifier that does not hash to this. Set it from /start's redirect. */
  expectChallenge: string;
  /** What the token endpoint demands as the client secret. */
  expectSecret: string;
  /** Advertise ONLY client_secret_basic and read the credential from the header. */
  basicAuth: boolean;
  tokenStatus: number;
  tokenBody: string | null;
  userInfo: Record<string, unknown>;
  emails: unknown;
  /** What /groups serves, and the status it serves it with — an endpoint that fails is its own case. */
  groups: unknown;
  groupsStatus: number;
  idToken: string | null;
  jwks: { keys: unknown[] };
  hits: string[];
  stop: () => void;
};

export function codeChallenge(verifier: string): string {
  return b64url(new Bun.CryptoHasher('sha256').update(verifier, 'ascii').digest());
}

export async function fakeProvider(): Promise<FakeProvider> {
  const p = {
    base: '',
    expectChallenge: '',
    expectSecret: 'shh',
    basicAuth: false,
    tokenStatus: 200,
    tokenBody: null as string | null,
    userInfo: {} as Record<string, unknown>,
    emails: [] as unknown,
    groups: [] as unknown,
    groupsStatus: 200,
    idToken: null as string | null,
    jwks: { keys: [] as unknown[] },
    hits: [] as string[],
    stop: () => {},
  };
  const server = Bun.serve({
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
      if (url.pathname === '/authorize') {
        // No consent screen: bounce straight back with a code, as a signed-in browser would.
        const back = new URL(url.searchParams.get('redirect_uri')!);
        back.searchParams.set('code', 'the-code');
        back.searchParams.set('state', url.searchParams.get('state')!);
        return Response.redirect(back.toString(), 302);
      }
      if (url.pathname === '/token') {
        if (p.tokenBody !== null || p.tokenStatus !== 200) {
          return new Response(p.tokenBody ?? JSON.stringify({ error: 'bad_verification_code', error_description: 'the code is expired' }), {
            status: p.tokenStatus,
            headers: { 'content-type': 'application/json' },
          });
        }
        const form = new URLSearchParams(await req.text());
        if (p.basicAuth) {
          const raw = /^Basic (.+)$/.exec(req.headers.get('authorization') ?? '')?.[1] ?? '';
          const [id, sec] = Buffer.from(raw, 'base64').toString('utf8').split(':');
          if (form.has('client_secret')) return j({ error: 'invalid_request', error_description: 'secret sent in the body too' }, 400);
          let decoded: [string, string];
          try {
            decoded = [decodeURIComponent(id ?? ''), decodeURIComponent(sec ?? '')];
          } catch {
            return j({ error: 'invalid_client', error_description: `basic auth is not form-urlencoded: "${raw}"` }, 401);
          }
          if (decoded[0] !== 'cid' || decoded[1] !== p.expectSecret) return j({ error: 'invalid_client', error_description: `basic auth was "${raw}"` }, 401);
        } else if (form.get('client_secret') !== p.expectSecret) {
          return j({ error: 'invalid_client', error_description: `client_secret was "${form.get('client_secret')}"` }, 401);
        }
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
      if (url.pathname === '/groups') {
        // Behind the access token, as GitHub's `/user/orgs` is — so a caller that forgot to send it
        // gets a 401 here rather than an empty membership list that looks like "not a member".
        if (req.headers.get('authorization') !== 'Bearer at-1') return j({ error: 'unauthorized' }, 401);
        return j(p.groupsStatus === 200 ? p.groups : { message: 'no' }, p.groupsStatus);
      }
      return new Response('no', { status: 404 });
    },
  });
  p.base = `http://127.0.0.1:${server.port}`;
  p.stop = () => server.stop(true);
  return p;
}

/** An RS256 keypair, its JWKS entry, and a signer — the id-token half of OIDC, for real. */
export async function rsaSigner(kid = 'k1') {
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
