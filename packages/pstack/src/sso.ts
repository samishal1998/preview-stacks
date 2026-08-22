/**
 * Sign in with the operator's own identity provider.
 *
 * ── WHAT THIS IS, AND WHAT IT DELIBERATELY IS NOT ────────────────────────────────────────────────
 *
 * The operator registers ONE OAuth/OIDC app in their own org (Google Workspace, Okta, GitHub, …) and
 * pastes the client id and secret in. We are the relying party and nothing more: their directory
 * stays theirs, and anyone who can authenticate against it can sign in here without an account being
 * created by hand first.
 *
 * It is four steps — authorize URL with a PKCE challenge, callback, token exchange, fetch the user —
 * and this file is all of them. There is no provider registry with lifecycle hooks, no session
 * framework, no plugin system: a new preset is a row in `PRESETS`, and the identity that comes out
 * the far end is handed to `Auth.ssoSignIn`, which mints the SAME session a password login mints.
 * Nothing downstream of the cookie knows SSO exists.
 *
 * ── THE PARTS THAT ARE SECURITY, NOT PLUMBING ────────────────────────────────────────────────────
 *
 *  - **PKCE, always.** Even with a confidential client and a secret: it is what makes an intercepted
 *    `code` useless, and it costs one hash.
 *  - **No `nonce`.** PKCE plus a single-use `state` covers the replay this feature is exposed to, and
 *    a nonce that is SENT but never CHECKED is worse than none — it reads as a defence in review and
 *    is not one. If ID tokens ever arrive anywhere but straight from the token endpoint, add it and
 *    verify it in the same change.
 *  - **`alg` is an allow-list** (`RS256`/`ES256`), read from the header and matched against the JWKS
 *    key. `none` and every HMAC alg are refused by construction: an attacker who may choose the
 *    algorithm can sign with the public key.
 *  - **`aud` may be a string or an array.** It must CONTAIN the client id. `iss` must equal the
 *    issuer the discovery document declares — not the URL it was fetched from.
 *  - **Identity is `(providerKey, subject)`, never email.** Emails move between people; subjects do
 *    not. `mapClaims` carries `emailVerified` precisely so the one place that links an SSO login to a
 *    PRE-EXISTING local account (auth.ts) can refuse an unverified one — that branch is the only
 *    account-takeover surface in the feature.
 *
 * ── ON GITHUB AND OIDC ───────────────────────────────────────────────────────────────────────────
 *
 * GitHub publishes an OIDC discovery document, and it is a trap: it signs GitHub ACTIONS job tokens
 * for cloud workload identity. It is not a user login endpoint. GitHub user login is `mode: 'oauth2'`
 * and always will be.
 */

export class SsoError extends Error {}

export type SsoMode = 'oidc' | 'oauth2';

/** Which key of a userinfo/claims object holds each field we care about. Flat lookups — not JSONPath. */
export type ClaimMap = {
  subject: string;
  username: string;
  email: string;
  name: string;
  avatar: string;
};

/** The standard OIDC claim names, and the default whenever a provider does not override them. */
export const OIDC_CLAIMS: ClaimMap = {
  subject: 'sub',
  username: 'preferred_username',
  email: 'email',
  name: 'name',
  avatar: 'picture',
};

export type Preset = {
  key: string;
  label: string;
  authorizeUrl: string;
  tokenUrl: string;
  userInfoUrl: string | null;
  /**
   * Where to look when the userinfo response carries no email — providers that let a user keep it
   * private serve it from a second endpoint. Optional, and the ONLY per-provider special case in
   * this file; adding one stays a table edit.
   */
  emailsUrl?: string;
  scopes: string;
  claimMap: ClaimMap;
};

/**
 * The preset table. Adding a provider is one entry here and nothing else — no code change, no new
 * branch. `custom` is not in the table: it is the absence of a preset (the operator supplies the
 * three URLs themselves).
 */
export const PRESETS: readonly Preset[] = [
  {
    key: 'github',
    label: 'GitHub',
    authorizeUrl: 'https://github.com/login/oauth/authorize',
    tokenUrl: 'https://github.com/login/oauth/access_token',
    userInfoUrl: 'https://api.github.com/user',
    // `GET /user` returns `email: null` for anyone whose profile email is private, which is the
    // default. Without this, `allowedEmailDomains` would reject the entire org.
    emailsUrl: 'https://api.github.com/user/emails',
    scopes: 'read:user user:email',
    claimMap: { subject: 'id', username: 'login', email: 'email', name: 'name', avatar: 'avatar_url' },
  },
  {
    key: 'gitlab',
    label: 'GitLab',
    authorizeUrl: 'https://gitlab.com/oauth/authorize',
    tokenUrl: 'https://gitlab.com/oauth/token',
    userInfoUrl: 'https://gitlab.com/api/v4/user',
    scopes: 'read_user',
    claimMap: { subject: 'id', username: 'username', email: 'email', name: 'name', avatar: 'avatar_url' },
  },
  {
    key: 'bitbucket',
    label: 'Bitbucket',
    authorizeUrl: 'https://bitbucket.org/site/oauth2/authorize',
    tokenUrl: 'https://bitbucket.org/site/oauth2/access_token',
    userInfoUrl: 'https://api.bitbucket.org/2.0/user',
    // Bitbucket's /2.0/user carries no email at all — it is always this second call.
    emailsUrl: 'https://api.bitbucket.org/2.0/user/emails',
    scopes: 'account email',
    claimMap: { subject: 'uuid', username: 'username', email: 'email', name: 'display_name', avatar: 'links.avatar.href' },
  },
] as const;

export const presetFor = (key: string): Preset | null => PRESETS.find((p) => p.key === key) ?? null;

// ── configuration ─────────────────────────────────────────────────────────────────────────────────

export type SsoConfig = {
  mode: SsoMode;
  /** Off keeps the row (and the secret) but hides the button and refuses `/start`. */
  enabled: boolean;
  /** Button text and the `providerKey` half of a link's identity. */
  label: string;
  clientId: string;
  /** Mode A: an issuer (`https://accounts.google.com`) or a full `.well-known` URL. */
  discoveryUrl: string;
  /** Mode B: a `PRESETS` key, or `custom`. */
  provider: string;
  authorizeUrl: string;
  tokenUrl: string;
  userInfoUrl: string;
  /**
   * Consulted ONLY when the userinfo response carries no email. Preset-filled (GitHub and Bitbucket
   * both keep the address off the profile); an operator only types it for a self-hosted provider.
   */
  emailsUrl: string;
  scopes: string;
  claimMap: ClaimMap;
  /** Non-empty ⇒ a login whose email is outside the list is refused — including one with NO email. */
  allowedEmailDomains: string[];
  /** Role for auto-provisioned users. */
  defaultRole: string;
};

const str = (v: unknown): string => (typeof v === 'string' ? v.trim() : '');

const httpsUrl = (raw: string, what: string): string => {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    throw new SsoError(`${what} must be an absolute URL — got "${raw}"`);
  }
  // http is allowed on loopback only: a real IdP is https, and a plaintext token exchange to
  // anywhere else puts the client secret on the wire.
  const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1' || u.hostname === '[::1]';
  if (u.protocol !== 'https:' && !(u.protocol === 'http:' && local)) {
    throw new SsoError(`${what} must be https (http is only accepted on localhost) — got "${raw}"`);
  }
  return u.toString();
};

/**
 * Validate what an operator submitted into the stored shape. Throws `SsoError` with something a
 * person can act on — this is a paste-four-fields form, and every one of them can be pasted wrong.
 */
export function parseSsoConfig(input: unknown): SsoConfig {
  const o = (input ?? {}) as Record<string, unknown>;
  const mode = str(o.mode) || 'oidc';
  if (mode !== 'oidc' && mode !== 'oauth2') throw new SsoError("mode must be 'oidc' or 'oauth2'");

  const clientId = str(o.clientId);
  if (!clientId) throw new SsoError('clientId is required');

  const allowedEmailDomains = (Array.isArray(o.allowedEmailDomains) ? o.allowedEmailDomains : [])
    .map((d) => str(d).toLowerCase().replace(/^@/, ''))
    .filter(Boolean);

  const base = {
    mode: mode as SsoMode,
    enabled: o.enabled === undefined ? true : !!o.enabled,
    clientId,
    allowedEmailDomains,
    // Only 'admin' exists today (store.ts migration 1 defaults the column to it and there is no
    // role UI). The field is here so granting less than admin is a data change when roles land.
    defaultRole: str(o.defaultRole) || 'admin',
  };

  if (mode === 'oidc') {
    const discoveryUrl = str(o.discoveryUrl) || str(o.issuer);
    if (!discoveryUrl) throw new SsoError('issuer or discoveryUrl is required for OIDC');
    return {
      ...base,
      label: str(o.label) || new URL(discoveryUrl).hostname,
      discoveryUrl: httpsUrl(discoveryUrl, 'issuer/discoveryUrl'),
      provider: '',
      authorizeUrl: '',
      tokenUrl: '',
      userInfoUrl: '',
      emailsUrl: '',
      scopes: str(o.scopes) || 'openid profile email',
      claimMap: { ...OIDC_CLAIMS, ...pickClaims(o.claimMap) },
    };
  }

  const provider = str(o.provider) || 'custom';
  const preset = presetFor(provider);
  if (!preset && provider !== 'custom') {
    throw new SsoError(`unknown provider "${provider}" — use one of ${PRESETS.map((p) => p.key).join(', ')}, or custom`);
  }
  // A preset fills the endpoints; anything the operator typed still wins, so a self-hosted GitLab
  // is the gitlab preset with three URLs replaced rather than `custom` with five fields.
  const authorizeUrl = str(o.authorizeUrl) || preset?.authorizeUrl || '';
  const tokenUrl = str(o.tokenUrl) || preset?.tokenUrl || '';
  if (!authorizeUrl || !tokenUrl) throw new SsoError('authorizeUrl and tokenUrl are required for a custom OAuth 2.0 provider');
  const userInfoUrl = str(o.userInfoUrl) || preset?.userInfoUrl || '';
  // The preset's emails endpoint is inherited ONLY while the userinfo endpoint is still the
  // preset's too. A self-hosted GitLab is not gitlab.com, and sending its access token there would
  // hand a third party a live credential.
  const emailsUrl =
    str(o.emailsUrl) || (preset && userInfoUrl === preset.userInfoUrl ? (preset.emailsUrl ?? '') : '');
  return {
    ...base,
    label: str(o.label) || preset?.label || 'SSO',
    discoveryUrl: '',
    provider,
    authorizeUrl: httpsUrl(authorizeUrl, 'authorizeUrl'),
    tokenUrl: httpsUrl(tokenUrl, 'tokenUrl'),
    userInfoUrl: userInfoUrl ? httpsUrl(userInfoUrl, 'userInfoUrl') : '',
    emailsUrl: emailsUrl ? httpsUrl(emailsUrl, 'emailsUrl') : '',
    scopes: str(o.scopes) || preset?.scopes || '',
    claimMap: { ...OIDC_CLAIMS, ...(preset?.claimMap ?? {}), ...pickClaims(o.claimMap) },
  };
}

function pickClaims(v: unknown): Partial<ClaimMap> {
  const o = (v ?? {}) as Record<string, unknown>;
  const out: Partial<ClaimMap> = {};
  for (const k of ['subject', 'username', 'email', 'name', 'avatar'] as const) {
    const s = str(o[k]);
    if (s) out[k] = s;
  }
  return out;
}

// ── PKCE and state ────────────────────────────────────────────────────────────────────────────────

const b64url = (b: Uint8Array | ArrayBuffer): string => Buffer.from(b as ArrayBuffer).toString('base64url');

export function randomB64Url(bytes = 32): string {
  const b = new Uint8Array(bytes);
  crypto.getRandomValues(b);
  return b64url(b);
}

/** RFC 7636 S256: `code_challenge = base64url(sha256(ascii(code_verifier)))`. */
export function codeChallenge(verifier: string): string {
  return b64url(new Bun.CryptoHasher('sha256').update(verifier, 'ascii').digest());
}

/** A verifier (43–128 chars of the unreserved set — 32 random bytes base64url is 43) and its challenge. */
export function pkce(): { verifier: string; challenge: string } {
  const verifier = randomB64Url(32);
  return { verifier, challenge: codeChallenge(verifier) };
}

/**
 * Where to send the browser back after a successful login.
 *
 * A SAME-ORIGIN PATH ONLY. This value arrives in a query parameter on a route that then issues a
 * redirect, which is the textbook open-redirect shape: `//evil.example` and `https://evil.example`
 * are both absolute to a browser. The UI only ever sends `to.fullPath`, so nothing is lost.
 */
export function safeNext(raw: string | null): string {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return '/';
  return raw;
}

// ── endpoints: discovery (mode A) or the preset table (mode B) ─────────────────────────────────────

export type Endpoints = {
  authorizeUrl: string;
  tokenUrl: string;
  userInfoUrl: string;
  emailsUrl: string;
  /** Mode A only — what an ID token's `iss` must equal, and where its signing keys are. */
  issuer: string;
  jwksUri: string;
  /** `client_secret_basic` when the provider advertises only that; `client_secret_post` otherwise. */
  tokenAuth: 'basic' | 'post';
};

export type Fetch = (url: string, init?: RequestInit) => Promise<Response>;

type Cached<T> = { value: T; at: number };
const discoveryCache = new Map<string, Cached<Endpoints>>();
const jwksCache = new Map<string, Cached<Jwk[]>>();
/** An hour. Long enough that discovery is not a per-login fetch, short enough that a moved endpoint heals without a restart. */
const CACHE_MS = 60 * 60 * 1000;

/** Test seam and the "re-validate on save" path — `setSsoConfig` drops the entry so the next login re-reads. */
export function forgetDiscovery(url?: string): void {
  if (url === undefined) {
    discoveryCache.clear();
    jwksCache.clear();
    return;
  }
  discoveryCache.delete(url);
}

const wellKnown = (raw: string): string =>
  raw.includes('/.well-known/') ? raw : `${raw.replace(/\/+$/, '')}/.well-known/openid-configuration`;

/** Fetch and validate an OIDC discovery document. Cached; `force` is the config-save validation. */
export async function discover(discoveryUrl: string, fetchImpl: Fetch = fetch, force = false): Promise<Endpoints> {
  const key = discoveryUrl;
  const hit = discoveryCache.get(key);
  if (!force && hit && Date.now() - hit.at < CACHE_MS) return hit.value;

  const url = wellKnown(discoveryUrl);
  let res: Response;
  try {
    res = await fetchImpl(url, { headers: { accept: 'application/json' } });
  } catch (err) {
    throw new SsoError(`could not reach ${url}: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (!res.ok) throw new SsoError(`${url} answered ${res.status} — is the issuer right?`);
  const doc = (await res.json().catch(() => null)) as Record<string, unknown> | null;
  if (!doc) throw new SsoError(`${url} did not return JSON`);

  const need = (k: string): string => {
    const v = doc[k];
    if (typeof v !== 'string' || !v) throw new SsoError(`the discovery document at ${url} has no ${k}`);
    return v;
  };
  const methods = Array.isArray(doc.token_endpoint_auth_methods_supported)
    ? doc.token_endpoint_auth_methods_supported.map(String)
    : [];
  const out: Endpoints = {
    authorizeUrl: need('authorization_endpoint'),
    tokenUrl: need('token_endpoint'),
    userInfoUrl: typeof doc.userinfo_endpoint === 'string' ? doc.userinfo_endpoint : '',
    emailsUrl: '',
    // The issuer an ID token must claim is the one the DOCUMENT declares, not the URL it came from.
    issuer: need('issuer'),
    jwksUri: need('jwks_uri'),
    // Only when Basic is the sole option: post is what most providers want, and sending both would
    // be rejected by the strict ones.
    tokenAuth: methods.length > 0 && !methods.includes('client_secret_post') && methods.includes('client_secret_basic') ? 'basic' : 'post',
  };
  discoveryCache.set(key, { value: out, at: Date.now() });
  return out;
}

/** Everything the flow needs to talk to this provider, from discovery (A) or the config (B). */
export async function endpointsFor(cfg: SsoConfig, fetchImpl: Fetch = fetch): Promise<Endpoints> {
  if (cfg.mode === 'oidc') return discover(cfg.discoveryUrl, fetchImpl);
  return {
    authorizeUrl: cfg.authorizeUrl,
    tokenUrl: cfg.tokenUrl,
    userInfoUrl: cfg.userInfoUrl,
    emailsUrl: cfg.emailsUrl,
    issuer: '',
    jwksUri: '',
    tokenAuth: 'post',
  };
}

// ── the three HTTP steps ──────────────────────────────────────────────────────────────────────────

export function authorizeUrl(
  cfg: SsoConfig,
  endpoints: Endpoints,
  args: { redirectUri: string; state: string; challenge: string },
): string {
  const u = new URL(endpoints.authorizeUrl);
  // Preserve whatever the provider already put in the query (some tenant URLs carry one).
  const p = u.searchParams;
  p.set('response_type', 'code');
  p.set('client_id', cfg.clientId);
  p.set('redirect_uri', args.redirectUri);
  if (cfg.scopes) p.set('scope', cfg.scopes);
  p.set('state', args.state);
  p.set('code_challenge', args.challenge);
  p.set('code_challenge_method', 'S256');
  return u.toString();
}

export type TokenResponse = {
  access_token?: string;
  id_token?: string;
  token_type?: string;
  scope?: string;
  [k: string]: unknown;
};

/**
 * Swap the code for a token.
 *
 * `Accept: application/json` is not decoration: without it GitHub answers
 * `access_token=…&scope=…` in `application/x-www-form-urlencoded` and `res.json()` throws on a
 * perfectly successful exchange.
 */
export async function exchangeCode(
  cfg: SsoConfig,
  endpoints: Endpoints,
  args: { code: string; redirectUri: string; verifier: string; clientSecret: string },
  fetchImpl: Fetch = fetch,
): Promise<TokenResponse> {
  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    code: args.code,
    redirect_uri: args.redirectUri,
    client_id: cfg.clientId,
    code_verifier: args.verifier,
  });
  const headers: Record<string, string> = {
    'content-type': 'application/x-www-form-urlencoded',
    accept: 'application/json',
  };
  if (endpoints.tokenAuth === 'basic') {
    headers.authorization = `Basic ${Buffer.from(`${encodeURIComponent(cfg.clientId)}:${encodeURIComponent(args.clientSecret)}`).toString('base64')}`;
  } else {
    body.set('client_secret', args.clientSecret);
  }

  let res: Response;
  try {
    res = await fetchImpl(endpoints.tokenUrl, { method: 'POST', headers, body: body.toString() });
  } catch (err) {
    throw new SsoError(`the token endpoint was unreachable: ${err instanceof Error ? err.message : String(err)}`);
  }
  const text = await res.text();
  const parsed = parseTokenBody(text);
  if (!res.ok) {
    throw new SsoError(`the token exchange failed (${res.status}): ${describeError(parsed) || text.slice(0, 200)}`);
  }
  // A 200 carrying an `error` is GitHub's shape for a bad code — treating it as success hands the
  // rest of the flow an undefined access token and a much worse message.
  const oops = describeError(parsed);
  if (oops) throw new SsoError(`the token exchange failed: ${oops}`);
  if (!parsed.access_token && !parsed.id_token) throw new SsoError('the token endpoint returned neither an access token nor an id token');
  return parsed;
}

/** JSON, or the form-encoded body a provider sends when it ignores `Accept`. */
function parseTokenBody(text: string): TokenResponse {
  const trimmed = text.trim();
  if (trimmed.startsWith('{')) {
    try {
      return JSON.parse(trimmed) as TokenResponse;
    } catch {
      /* fall through to form decoding */
    }
  }
  return Object.fromEntries(new URLSearchParams(trimmed)) as TokenResponse;
}

/** The `error` / `error_description` pair, from a token body or from the callback's own query. */
export function describeError(o: Record<string, unknown>): string {
  const code = typeof o.error === 'string' ? o.error : '';
  if (!code) return '';
  const desc = typeof o.error_description === 'string' ? o.error_description : '';
  return desc ? `${code}: ${desc}` : code;
}

export async function fetchJson(url: string, accessToken: string, fetchImpl: Fetch = fetch): Promise<unknown> {
  const res = await fetchImpl(url, {
    headers: {
      authorization: `Bearer ${accessToken}`,
      accept: 'application/json',
      // GitHub requires a User-Agent and answers 403 without one.
      'user-agent': 'pstack',
    },
  });
  if (!res.ok) throw new SsoError(`${url} answered ${res.status}`);
  return res.json();
}

// ── identity ──────────────────────────────────────────────────────────────────────────────────────

export type SsoIdentity = {
  subject: string;
  username: string;
  email: string;
  name: string;
  avatar: string;
  /**
   * `false` only when the provider says so explicitly. `null` means it never said — which is NOT
   * permission to link an existing local account (auth.ts requires `true`).
   */
  emailVerified: boolean | null;
};

/** `links.avatar.href` — one dotted path, because two providers nest the avatar and nothing else. */
function lookup(payload: Record<string, unknown>, key: string): unknown {
  if (!key.includes('.')) return payload[key];
  let cur: unknown = payload;
  for (const part of key.split('.')) {
    if (cur === null || typeof cur !== 'object') return undefined;
    cur = (cur as Record<string, unknown>)[part];
  }
  return cur;
}

const asText = (v: unknown): string => (typeof v === 'string' ? v.trim() : typeof v === 'number' ? String(v) : '');

export function mapClaims(payload: unknown, claimMap: ClaimMap): SsoIdentity {
  const o = (payload ?? {}) as Record<string, unknown>;
  const subject = asText(lookup(o, claimMap.subject));
  if (!subject) {
    throw new SsoError(`the provider's response has no "${claimMap.subject}" — check the claim mapping`);
  }
  const verified = o.email_verified;
  return {
    subject,
    username: asText(lookup(o, claimMap.username)),
    email: asText(lookup(o, claimMap.email)).toLowerCase(),
    name: asText(lookup(o, claimMap.name)),
    avatar: asText(lookup(o, claimMap.avatar)),
    emailVerified: typeof verified === 'boolean' ? verified : verified === 'true' ? true : verified === 'false' ? false : null,
  };
}

/**
 * The primary verified address from a provider's separate emails endpoint. Tolerates both shapes in
 * the wild: a bare array (GitHub) and `{ values: [...] }` (Bitbucket), with either key spelling.
 */
export function primaryEmail(payload: unknown): { email: string; verified: boolean } | null {
  const list = Array.isArray(payload)
    ? payload
    : Array.isArray((payload as { values?: unknown } | null)?.values)
      ? ((payload as { values: unknown[] }).values)
      : [];
  const rows = list.filter((r): r is Record<string, unknown> => !!r && typeof r === 'object');
  const primary = rows.find((r) => r.primary === true || r.is_primary === true) ?? rows[0];
  if (!primary) return null;
  const email = asText(primary.email).toLowerCase();
  if (!email) return null;
  return { email, verified: primary.verified === true || primary.is_confirmed === true };
}

/** A username the local schema accepts. Cosmetic — identity is `(providerKey, subject)`. */
export function sanitizeUsername(raw: string, fallback: string): string {
  const base = raw
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[^a-z0-9]+/, '')
    .replace(/-+$/, '')
    .slice(0, 32);
  if (/^[a-z0-9][a-z0-9._-]{1,31}$/.test(base)) return base;
  const alt = `user-${fallback.replace(/[^a-z0-9]/gi, '').toLowerCase().slice(0, 20) || 'sso'}`;
  return alt.slice(0, 32);
}

/** Fail CLOSED: an empty list allows everything, a non-empty one requires an email that matches it. */
export function emailAllowed(email: string, domains: string[]): boolean {
  if (domains.length === 0) return true;
  const at = email.lastIndexOf('@');
  if (at < 0) return false;
  const domain = email.slice(at + 1).toLowerCase();
  return domains.some((d) => domain === d || domain.endsWith(`.${d}`));
}

// ── ID token verification (mode A) ────────────────────────────────────────────────────────────────

/**
 * RS256 and ES256 only, both via WebCrypto — this package carries no runtime dependencies.
 *
 * The allow-list is the point. `alg: none` and `alg: HS256` are the two classic JWT forgeries (the
 * second signs with the PUBLIC key, which the attacker also has), and both are refused here before
 * any key is imported.
 */
/** Borrowed from WebCrypto's own signatures — `lib` here is ESNext, so the DOM's names do not exist. */
type ImportParams = Parameters<typeof crypto.subtle.importKey>[2];
type VerifyParams = Parameters<typeof crypto.subtle.verify>[0];
/**
 * `importKey`'s JWK overload wants a `JsonWebKey`, and this package compiles with `lib: ESNext` —
 * the DOM's type names are not in scope to name it. One narrow alias beats sprinkling casts.
 */
const importJwk = crypto.subtle.importKey.bind(crypto.subtle) as (
  format: 'jwk',
  key: Jwk,
  algorithm: ImportParams,
  extractable: boolean,
  usages: ['verify'],
) => Promise<CryptoKey>;

/** A JWKS entry, as much of one as verification looks at. */
export type Jwk = { kty?: string; kid?: string; alg?: string; use?: string; n?: string; e?: string; crv?: string; x?: string; y?: string };

const ALGS: Record<string, { importParams: ImportParams; verifyParams: VerifyParams }> = {
  RS256: {
    importParams: { name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' },
    verifyParams: { name: 'RSASSA-PKCS1-v1_5' },
  },
  ES256: {
    importParams: { name: 'ECDSA', namedCurve: 'P-256' },
    verifyParams: { name: 'ECDSA', hash: 'SHA-256' },
  },
};

export type JwtHeader = { alg?: string; kid?: string; typ?: string };

/**
 * A plain ArrayBuffer-backed copy. A Buffer (and TextEncoder's output) is a view over a pooled or
 * possibly-shared allocation, which WebCrypto's parameter types rightly refuse.
 */
function bytes(src: Uint8Array | string): Uint8Array<ArrayBuffer> {
  const b = typeof src === 'string' ? Buffer.from(src, 'utf8') : src;
  const out = new Uint8Array(new ArrayBuffer(b.byteLength));
  out.set(b);
  return out;
}

function decodeSegment(seg: string): Record<string, unknown> {
  try {
    return JSON.parse(Buffer.from(seg, 'base64url').toString('utf8')) as Record<string, unknown>;
  } catch {
    throw new SsoError('the id token is not a readable JWT');
  }
}

/** Header + claims WITHOUT verifying anything. Only for deciding which key to fetch. */
export function decodeJwt(token: string): { header: JwtHeader; claims: Record<string, unknown>; signed: string; signature: Uint8Array<ArrayBuffer> } {
  const parts = token.split('.');
  if (parts.length !== 3) throw new SsoError('the id token is not a JWT (expected three dot-separated parts)');
  // Copied into a plain ArrayBuffer-backed view: a Buffer is one over a pooled, shared allocation,
  // which WebCrypto's parameter types (rightly) will not take.
  const signature = bytes(Buffer.from(parts[2]!, 'base64url'));
  return {
    header: decodeSegment(parts[0]!) as JwtHeader,
    claims: decodeSegment(parts[1]!),
    signed: `${parts[0]}.${parts[1]}`,
    signature,
  };
}

/**
 * How long after a fetch a REFETCH-for-an-unknown-kid is refused. Anyone can present a token with a
 * junk kid, so without a floor every such request would be a fetch against the provider. The cost
 * is that a key rotation heals within this window rather than instantly, which is the same trade
 * every remote-JWKS implementation makes.
 */
export const JWKS_COOLDOWN_MS = 30_000;

async function jwks(uri: string, fetchImpl: Fetch, force: boolean, cooldownMs: number): Promise<Jwk[]> {
  const hit = jwksCache.get(uri);
  const age = hit ? Date.now() - hit.at : Infinity;
  if (hit && age < CACHE_MS && (!force || age < cooldownMs)) return hit.value;
  const res = await fetchImpl(uri, { headers: { accept: 'application/json' } });
  if (!res.ok) throw new SsoError(`the JWKS endpoint ${uri} answered ${res.status}`);
  const doc = (await res.json().catch(() => null)) as { keys?: Jwk[] } | null;
  const keys = Array.isArray(doc?.keys) ? doc.keys : [];
  jwksCache.set(uri, { value: keys, at: Date.now() });
  return keys;
}

/** Strip everything WebCrypto might choke on (`use`, `key_ops`, a mismatched `alg`). */
function importable(jwk: Jwk): Jwk {
  const { kty, n, e, crv, x, y } = jwk;
  return kty === 'RSA' ? { kty, n, e } : { kty, crv, x, y };
}

/**
 * Validate an ID token: signature against the JWKS, then `iss`, `aud` and `exp`.
 *
 * Returns the claims. Every failure is an `SsoError` — there is no "probably fine" path.
 */
export async function verifyIdToken(
  token: string,
  args: { issuer: string; clientId: string; jwksUri: string; now?: number; jwksCooldownMs?: number },
  fetchImpl: Fetch = fetch,
): Promise<Record<string, unknown>> {
  const { header, claims, signed, signature } = decodeJwt(token);
  const alg = header.alg ?? '';
  const spec = ALGS[alg];
  if (!spec) throw new SsoError(`id token algorithm "${alg || 'none'}" is not accepted — only RS256 and ES256 are`);

  const pick = (keys: Jwk[]): Jwk[] => {
    const usable = keys.filter((k) => k.alg === undefined || k.alg === alg);
    if (!header.kid) return usable;
    const byKid = usable.filter((k) => k.kid === header.kid);
    return byKid.length > 0 ? byKid : [];
  };

  const cooldownMs = args.jwksCooldownMs ?? JWKS_COOLDOWN_MS;
  let candidates = pick(await jwks(args.jwksUri, fetchImpl, false, cooldownMs));
  // Unknown kid ⇒ the provider probably rotated. Refetch once, no sooner than the cooldown.
  if (candidates.length === 0) candidates = pick(await jwks(args.jwksUri, fetchImpl, true, cooldownMs));
  if (candidates.length === 0) throw new SsoError(`no signing key matching kid "${header.kid ?? '(none)'}" in ${args.jwksUri}`);

  const data = bytes(signed);
  let ok = false;
  for (const jwk of candidates) {
    try {
      const key = await importJwk('jwk', importable(jwk), spec.importParams, false, ['verify']);
      // ES256 JWS signatures are raw r||s, which is exactly what WebCrypto ECDSA wants — no DER.
      if (await crypto.subtle.verify(spec.verifyParams, key, signature, data)) {
        ok = true;
        break;
      }
    } catch {
      // A key that will not import is not a key that verifies — try the next one.
    }
  }
  if (!ok) throw new SsoError('the id token signature did not verify against the provider JWKS');

  if (claims.iss !== args.issuer) {
    throw new SsoError(`the id token issuer is "${String(claims.iss)}", expected "${args.issuer}"`);
  }
  const aud = claims.aud;
  const audiences = Array.isArray(aud) ? aud.map(String) : typeof aud === 'string' ? [aud] : [];
  if (!audiences.includes(args.clientId)) {
    throw new SsoError('the id token audience does not include this client id');
  }
  const now = Math.floor((args.now ?? Date.now()) / 1000);
  const exp = typeof claims.exp === 'number' ? claims.exp : 0;
  // 60s leeway, for clock skew between here and the provider. Not more: an expired token is the
  // one thing `exp` exists to stop.
  if (!exp || exp + 60 < now) throw new SsoError('the id token has expired');
  const nbf = typeof claims.nbf === 'number' ? claims.nbf : 0;
  if (nbf && nbf - 60 > now) throw new SsoError('the id token is not valid yet');
  return claims;
}

// ── the transient store ───────────────────────────────────────────────────────────────────────────

/**
 * Where the PKCE verifier waits out the round trip. Nothing else in this feature is transient, and
 * nothing that lands here outlives five minutes.
 *
 * The interface exists so going multi-instance is a config change rather than a rewrite: Redis (its
 * own TTL) or Postgres (an `expires_at` column) slot in behind it unchanged. SQLite is what this
 * service already wires up, so it is what ships — and it is honest about the ceiling: it does not
 * survive going multi-instance, which is the same ceiling everything else in this process has.
 */
export interface TransientStore {
  set(key: string, value: string, ttlSeconds: number): Promise<void>;
  /** null if absent OR expired — a caller never has to check a timestamp. */
  get(key: string): Promise<string | null>;
  delete(key: string): Promise<void>;
  /**
   * Read and delete in ONE statement. Single-use state is what stops a replayed callback, and a
   * get-then-delete pair has a window where two requests both read the same row.
   */
  take(key: string): Promise<string | null>;
}

/** Structurally what `bun:sqlite`'s Database gives us — typed here so this file imports nothing. */
type Bind = string | number | null;
type Db = {
  query: (sql: string) => {
    get: (...params: Bind[]) => unknown;
    run: (...params: Bind[]) => unknown;
  };
};

export class SqliteTransientStore implements TransientStore {
  #db: Db;

  constructor(db: Db) {
    this.#db = db;
  }

  async set(key: string, value: string, ttlSeconds: number): Promise<void> {
    // Sweep on write rather than on a timer: this table only grows when someone starts a login, so
    // the write path is exactly where the garbage appears.
    this.#db.query('DELETE FROM sso_state WHERE expires_at <= ?').run(Date.now());
    this.#db
      .query('INSERT OR REPLACE INTO sso_state (key, value, expires_at) VALUES (?, ?, ?)')
      .run(key, value, Date.now() + ttlSeconds * 1000);
  }

  async get(key: string): Promise<string | null> {
    const row = this.#db.query('SELECT value FROM sso_state WHERE key = ? AND expires_at > ?').get(key, Date.now()) as
      | { value: string }
      | null;
    return row?.value ?? null;
  }

  async delete(key: string): Promise<void> {
    this.#db.query('DELETE FROM sso_state WHERE key = ?').run(key);
  }

  async take(key: string): Promise<string | null> {
    const row = this.#db
      .query('DELETE FROM sso_state WHERE key = ? AND expires_at > ? RETURNING value')
      .get(key, Date.now()) as { value: string } | null;
    return row?.value ?? null;
  }
}

/** The fallback the interface promises, and what the store suite runs against a second time. */
export class MemoryTransientStore implements TransientStore {
  #rows = new Map<string, { value: string; expiresAt: number }>();

  async set(key: string, value: string, ttlSeconds: number): Promise<void> {
    for (const [k, r] of this.#rows) if (r.expiresAt <= Date.now()) this.#rows.delete(k);
    this.#rows.set(key, { value, expiresAt: Date.now() + ttlSeconds * 1000 });
  }

  async get(key: string): Promise<string | null> {
    const row = this.#rows.get(key);
    if (!row) return null;
    if (row.expiresAt <= Date.now()) {
      this.#rows.delete(key);
      return null;
    }
    return row.value;
  }

  async delete(key: string): Promise<void> {
    this.#rows.delete(key);
  }

  async take(key: string): Promise<string | null> {
    const v = await this.get(key);
    this.#rows.delete(key);
    return v;
  }
}
