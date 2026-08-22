/**
 * Accounts, sessions and personal API tokens, on top of the Store.
 *
 * ── THE MODEL, AND WHY COOKIES ───────────────────────────────────────────────────────────────────
 *
 * Three principals can authenticate a request:
 *
 *   1. `PSTACK_TOKEN` — the root/machine credential. It PREDATES accounts, `init` generates it, and
 *      CI pipelines hold it; retiring it would be a forced migration for every caller. It stays.
 *   2. A personal API token (`pstack_pat_…`) — per user, for scripts that should not hold root.
 *   3. A session — httpOnly cookie set by username+password login, OR by a completed SSO round trip
 *      (sso.ts): an SSO login mints exactly the same row, so nothing downstream of the cookie —
 *      the gate, the terminal, personal tokens — knows the difference. Cookies are the deliberate
 *      choice, not an implementation detail: `EventSource` (the job log stream) and `WebSocket`
 *      (the coming terminal) cannot send an `Authorization` header from a browser, but the browser
 *      attaches cookies to both automatically on the same origin. Session auth is what makes those
 *      surfaces authenticable at all.
 *
 * ── STORAGE DISCIPLINE ───────────────────────────────────────────────────────────────────────────
 *
 * Passwords: argon2id via `Bun.password` (built in — no dependency). Sessions and tokens: the
 * database stores the SHA-256 of the secret, never the secret. A database file read (or a backup
 * lying around) must not be a session hijack or a token theft. Verification is hash-then-lookup,
 * which also makes it constant-time-ish by construction — the lookup key is a digest, not the
 * caller's input.
 *
 * ── BOOTSTRAP ────────────────────────────────────────────────────────────────────────────────────
 *
 * The first admin comes from `PSTACK_ADMIN_USER`/`PSTACK_ADMIN_PASSWORD` at boot, or from
 * `POST /api/auth/bootstrap` authenticated by `PSTACK_TOKEN`. Both are ONLY honoured while the users
 * table is empty — after that they are inert, so a leaked compose file with the env pair in it
 * cannot mint extra admins later.
 */

import type { Store, UserRow } from './store.ts';
import {
  emailAllowed,
  parseSsoConfig,
  sanitizeUsername,
  SqliteTransientStore,
  SsoError,
  type SsoConfig,
  type SsoIdentity,
  type TransientStore,
} from './sso.ts';

export class AuthError extends Error {}

/** Thirty days. Fixed, not sliding — a stolen cookie should not be renewable forever. */
const SESSION_TTL_MS = 30 * 24 * 60 * 60 * 1000;

const USERNAME = /^[a-z0-9][a-z0-9._-]{1,31}$/;

/**
 * Who a request is. `root` is PSTACK_TOKEN; a user carries its row; `share` is a signed read-only
 * link to ONE deployment (share.ts) — it can see what its `views` allow on that deployment and
 * nothing else, which api.ts enforces right after the gate, before any route.
 */
export type Principal =
  | { kind: 'root' }
  | { kind: 'user'; user: UserRow }
  | { kind: 'share'; deployment: string; views: ShareView[] };

export type ShareView = 'details' | 'logs';

function sha256(input: string): string {
  return new Bun.CryptoHasher('sha256').update(input).digest('hex');
}

function randomSecret(prefix: string): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return `${prefix}${[...b].map((n) => n.toString(16).padStart(2, '0')).join('')}`;
}

type UserCols = {
  id: number;
  username: string;
  role: string;
  email: string | null;
  created_at: number;
};

const toUser = (r: UserCols): UserRow => ({
  id: r.id,
  username: r.username,
  role: r.role,
  email: r.email ?? null,
  createdAt: r.created_at,
});

/** Every column `toUser` needs, spelled once — five queries used to drift apart on this. */
const USER_COLS = 'id, username, role, email, created_at';

export class Auth {
  #store: Store;
  #transient: TransientStore | undefined;

  constructor(store: Store) {
    this.#store = store;
  }

  // ── users ─────────────────────────────────────────────────────────────────────────────────────

  userCount(): number {
    return (this.#store.db.query('SELECT COUNT(*) AS n FROM users').get() as { n: number }).n;
  }

  async createUser(
    username: string,
    password: string,
    opts?: { role?: string; email?: string | null },
  ): Promise<UserRow> {
    if (!USERNAME.test(username)) {
      throw new AuthError(
        `username must match ${USERNAME} — lowercase, 2–32 chars, letters/digits/._-`,
      );
    }
    if (password.length < 8) {
      throw new AuthError('password must be at least 8 characters');
    }
    const hash = await Bun.password.hash(password, 'argon2id');
    try {
      const row = this.#store.db
        .query(
          `INSERT INTO users (username, password_hash, role, email, created_at) VALUES (?, ?, COALESCE(?, 'admin'), ?, ?) RETURNING ${USER_COLS}`,
        )
        .get(username, hash, opts?.role ?? null, opts?.email ?? null, Date.now()) as UserCols;
      return toUser(row);
    } catch (err) {
      if (String(err).includes('UNIQUE')) throw new AuthError(`user "${username}" already exists`);
      throw err;
    }
  }

  listUsers(): UserRow[] {
    return (
      this.#store.db
        .query(`SELECT ${USER_COLS} FROM users ORDER BY username`)
        .all() as UserCols[]
    ).map(toUser);
  }

  /**
   * Refuses to delete the last user: an instance with accounts and no way to log in is only
   * recoverable by editing the database over SSH, and nothing in the UI can explain that state.
   */
  deleteUser(id: number): boolean {
    if (this.userCount() <= 1) {
      throw new AuthError('cannot delete the last user — create another account first');
    }
    // Sessions and tokens go with the row (ON DELETE CASCADE).
    return this.#store.db.query('DELETE FROM users WHERE id = ?').run(id).changes > 0;
  }

  /**
   * Change a password.
   *
   * Every session and token for that user is revoked in the same transaction. A password change is
   * usually a response to "someone may have this" — leaving the sessions it was protecting alive
   * would make the change theatre. The caller's own session dies with the rest; the UI signs them
   * back in rather than pretending nothing happened.
   */
  async setPassword(id: number, password: string): Promise<boolean> {
    if (password.length < 8) {
      throw new AuthError('password must be at least 8 characters');
    }
    const hash = await Bun.password.hash(password);
    const apply = this.#store.db.transaction(() => {
      const changed = this.#store.db
        .query('UPDATE users SET password_hash = ? WHERE id = ?')
        .run(hash, id).changes;
      if (changed > 0) {
        this.#store.db.query('DELETE FROM sessions WHERE user_id = ?').run(id);
        this.#store.db.query('DELETE FROM tokens WHERE user_id = ?').run(id);
      }
      return changed > 0;
    });
    return apply();
  }

  /**
   * First-admin bootstrap. Only while the table is empty — see the header. Returns null when it
   * declined, so the caller can distinguish "created" from "already bootstrapped" without a race.
   */
  async bootstrap(username: string, password: string): Promise<UserRow | null> {
    if (this.userCount() > 0) return null;
    return this.createUser(username, password);
  }

  // ── sessions ──────────────────────────────────────────────────────────────────────────────────

  /** Verify credentials and mint a session. The returned value is the COOKIE value; only its hash is stored. */
  async login(username: string, password: string): Promise<{ session: string; user: UserRow }> {
    const row = this.#store.db
      .query(`SELECT ${USER_COLS}, password_hash FROM users WHERE username = ?`)
      .get(username) as (UserCols & { password_hash: string }) | null;
    // One error for both wrong-user and wrong-password: naming which half failed turns the login
    // form into a username oracle.
    if (!row || !(await Bun.password.verify(password, row.password_hash))) {
      throw new AuthError('invalid username or password');
    }
    return { session: this.#mintSession(row.id), user: toUser(row) };
  }

  /** The one place a session row is created — password login and SSO both come through here. */
  #mintSession(userId: number): string {
    const session = randomSecret('pstack_ses_');
    this.#store.db
      .query('INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)')
      .run(sha256(session), userId, Date.now(), Date.now() + SESSION_TTL_MS);
    return session;
  }

  sessionUser(session: string): UserRow | null {
    const row = this.#store.db
      .query(
        `SELECT u.id, u.username, u.role, u.email, u.created_at FROM sessions s
         JOIN users u ON u.id = s.user_id
         WHERE s.id_hash = ? AND s.expires_at > ?`,
      )
      .get(sha256(session), Date.now()) as UserCols | null;
    return row ? toUser(row) : null;
  }

  logout(session: string): void {
    this.#store.db.query('DELETE FROM sessions WHERE id_hash = ?').run(sha256(session));
  }

  /** Housekeeping — expired rows are already unusable; this just stops them accumulating. */
  pruneSessions(): void {
    this.#store.db.query('DELETE FROM sessions WHERE expires_at <= ?').run(Date.now());
  }

  // ── personal API tokens ───────────────────────────────────────────────────────────────────────

  /** Mint a token for a user. The plaintext is returned ONCE and never retrievable again. */
  createToken(userId: number, name: string): { token: string; id: number } {
    if (!name.trim()) throw new AuthError('a token needs a name — it is the only handle left later');
    const token = randomSecret('pstack_pat_');
    const row = this.#store.db
      .query(
        'INSERT INTO tokens (user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?) RETURNING id',
      )
      .get(userId, name.trim(), sha256(token), Date.now()) as { id: number };
    return { token, id: row.id };
  }

  tokenUser(token: string): UserRow | null {
    const row = this.#store.db
      .query(
        `SELECT u.id, u.username, u.role, u.email, u.created_at, t.id AS token_id FROM tokens t
         JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`,
      )
      .get(sha256(token)) as (UserCols & { token_id: number }) | null;
    if (!row) return null;
    // Best-effort bookkeeping — an operator deciding which stale token to revoke needs this.
    this.#store.db
      .query('UPDATE tokens SET last_used_at = ? WHERE id = ?')
      .run(Date.now(), row.token_id);
    return toUser(row);
  }

  listTokens(userId: number): Array<{ id: number; name: string; createdAt: number; lastUsedAt: number | null }> {
    return (
      this.#store.db
        .query(
          'SELECT id, name, created_at, last_used_at FROM tokens WHERE user_id = ? ORDER BY created_at DESC',
        )
        .all(userId) as Array<{ id: number; name: string; created_at: number; last_used_at: number | null }>
    ).map((r) => ({ id: r.id, name: r.name, createdAt: r.created_at, lastUsedAt: r.last_used_at }));
  }

  deleteToken(userId: number, id: number): boolean {
    // Scoped to the owner: one user must not be able to revoke another's token by guessing ids.
    return (
      this.#store.db.query('DELETE FROM tokens WHERE id = ? AND user_id = ?').run(id, userId)
        .changes > 0
    );
  }

  // ── SSO: the operator's identity provider ─────────────────────────────────────────────────────
  //
  // The protocol lives in sso.ts. What lives HERE is the part that touches accounts: the stored
  // configuration, the (provider, subject) → user links, and turning a verified identity into the
  // same session a password login produces.

  /**
   * Where the PKCE verifier waits out the round trip. SQLite, because that is what this service
   * already wires up; the interface is what makes Redis or Postgres a config change later.
   */
  get transient(): TransientStore {
    this.#transient ??= new SqliteTransientStore(this.#store.db);
    return this.#transient;
  }

  ssoConfig(): { config: SsoConfig; clientSecret: string; updatedAt: number } | null {
    const row = this.#store.db
      .query('SELECT config, client_secret, updated_at FROM sso_config WHERE id = 1')
      .get() as { config: string; client_secret: string; updated_at: number } | null;
    if (!row) return null;
    try {
      // Re-validated on read, not trusted: a row written by an older version (or edited by hand
      // over SSH, which this project expects) must not put a half-shape into the flow.
      return { config: parseSsoConfig(JSON.parse(row.config)), clientSecret: row.client_secret, updatedAt: row.updated_at };
    } catch {
      return null;
    }
  }

  /**
   * Save. An EMPTY `clientSecret` keeps the stored one — the read endpoint returns a mask, so a
   * form that round-trips the mask must not overwrite the real secret with it. There is no stored
   * secret to keep on the first save, so an empty one is refused there.
   */
  setSsoConfig(config: SsoConfig, clientSecret: string): void {
    const existing = this.#store.db
      .query('SELECT client_secret FROM sso_config WHERE id = 1')
      .get() as { client_secret: string } | null;
    const secret = clientSecret || existing?.client_secret || '';
    if (!secret) throw new AuthError('clientSecret is required');
    this.#store.db
      .query(
        'INSERT INTO sso_config (id, config, client_secret, updated_at) VALUES (1, ?, ?, ?) ' +
          'ON CONFLICT(id) DO UPDATE SET config = excluded.config, client_secret = excluded.client_secret, updated_at = excluded.updated_at',
      )
      .run(JSON.stringify(config), secret, Date.now());
  }

  /** Forget the provider. The links stay: those accounts keep their password and their tokens. */
  clearSsoConfig(): boolean {
    return this.#store.db.query('DELETE FROM sso_config WHERE id = 1').run().changes > 0;
  }

  ssoLinks(userId: number): Array<{ providerKey: string; subject: string; createdAt: number; lastLoginAt: number | null }> {
    return (
      this.#store.db
        .query('SELECT provider_key, subject, created_at, last_login_at FROM sso_links WHERE user_id = ?')
        .all(userId) as Array<{ provider_key: string; subject: string; created_at: number; last_login_at: number | null }>
    ).map((r) => ({ providerKey: r.provider_key, subject: r.subject, createdAt: r.created_at, lastLoginAt: r.last_login_at }));
  }

  /**
   * Turn a verified provider identity into a session.
   *
   * THE ORDER IS THE SECURITY. `(providerKey, subject)` first, because it is the only stable
   * identity; email second and ONLY to adopt a pre-existing local account, gated on the provider
   * having said the address is verified; a new account last. An unverified email must never reach
   * an existing account — that is the one path in this feature that could hand over someone else's
   * privileges, and it is why `emailVerified: null` (the provider never said) is not good enough.
   *
   * `how` tells the caller which of the three happened, so the adoption case can be logged.
   */
  async ssoSignIn(
    providerKey: string,
    identity: SsoIdentity,
    opts: { defaultRole?: string; allowedEmailDomains?: string[] } = {},
  ): Promise<{ session: string; user: UserRow; how: 'linked' | 'adopted' | 'created' }> {
    // Fail CLOSED: a non-empty allow-list with no email to check is a refusal, not a pass. GitHub
    // returns a null email for a private profile, so this is a real case and not a theoretical one.
    if (!emailAllowed(identity.email, opts.allowedEmailDomains ?? [])) {
      throw new SsoError(
        identity.email
          ? `${identity.email} is not in an allowed email domain`
          : 'this provider returned no email address, and sign-in is restricted to specific email domains',
      );
    }

    const link = this.#store.db
      .query('SELECT user_id FROM sso_links WHERE provider_key = ? AND subject = ?')
      .get(providerKey, identity.subject) as { user_id: number } | null;
    if (link) {
      const row = this.#store.db.query(`SELECT ${USER_COLS} FROM users WHERE id = ?`).get(link.user_id) as UserCols | null;
      if (row) {
        this.#touchLink(providerKey, identity.subject);
        // The provider owns the address; keep the local copy current so the allow-list and the UI
        // do not go stale after someone changes it upstream.
        if (identity.email && identity.email !== row.email) {
          this.#store.db.query('UPDATE users SET email = ? WHERE id = ?').run(identity.email, row.id);
          row.email = identity.email;
        }
        return { session: this.#mintSession(row.id), user: toUser(row), how: 'linked' };
      }
      // The account was deleted out from under the link (CASCADE should have taken it, so this is
      // a repaired database). Drop the orphan and provision afresh.
      this.#store.db.query('DELETE FROM sso_links WHERE provider_key = ? AND subject = ?').run(providerKey, identity.subject);
    }

    if (identity.email && identity.emailVerified === true) {
      const matches = this.#store.db
        .query(`SELECT ${USER_COLS} FROM users WHERE email = ?`)
        .all(identity.email) as UserCols[];
      // Exactly one, and it must not already belong to another provider subject. Two rows sharing
      // an address is an ambiguity, and guessing which one to adopt is guessing whose account to
      // hand over.
      const only = matches.length === 1 ? matches[0]! : null;
      if (only && this.ssoLinks(only.id).length === 0) {
        this.#link(providerKey, identity.subject, only.id);
        return { session: this.#mintSession(only.id), user: toUser(only), how: 'adopted' };
      }
    }

    const user = await this.#provision(identity, opts.defaultRole);
    this.#link(providerKey, identity.subject, user.id);
    return { session: this.#mintSession(user.id), user, how: 'created' };
  }

  /**
   * Create the local account behind an SSO identity.
   *
   * The password is 32 random bytes nobody will ever hold, hashed like any other: the row stays
   * ordinary, `login()` needs no "is this an SSO user" branch, and there is no null-password state
   * for some future code path to treat as "no password required". An operator who wants this
   * account to also have a password sets one the usual way.
   *
   * The username is COSMETIC — identity is `(providerKey, subject)`. A collision therefore takes a
   * suffix rather than linking to whoever already holds the name.
   */
  async #provision(identity: SsoIdentity, defaultRole?: string): Promise<UserRow> {
    const base = sanitizeUsername(identity.username || identity.email.split('@')[0] || '', identity.subject);
    for (let attempt = 0; attempt < 50; attempt++) {
      const suffix = attempt === 0 ? '' : `-${attempt + 1}`;
      const username = `${base.slice(0, 32 - suffix.length)}${suffix}`;
      try {
        return await this.createUser(username, randomSecret(''), {
          role: defaultRole || 'admin',
          email: identity.email || null,
        });
      } catch (err) {
        if (err instanceof AuthError && err.message.includes('already exists')) continue;
        throw err;
      }
    }
    throw new AuthError(`could not find a free username for "${base}" — 50 variants were taken`);
  }

  #link(providerKey: string, subject: string, userId: number): void {
    this.#store.db
      .query('INSERT INTO sso_links (provider_key, subject, user_id, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)')
      .run(providerKey, subject, userId, Date.now(), Date.now());
  }

  #touchLink(providerKey: string, subject: string): void {
    this.#store.db
      .query('UPDATE sso_links SET last_login_at = ? WHERE provider_key = ? AND subject = ?')
      .run(Date.now(), providerKey, subject);
  }
}
