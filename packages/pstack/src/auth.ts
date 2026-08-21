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
 *   3. A session — httpOnly cookie set by username+password login. Cookies are the deliberate
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

const toUser = (r: {
  id: number;
  username: string;
  role: string;
  created_at: number;
}): UserRow => ({ id: r.id, username: r.username, role: r.role, createdAt: r.created_at });

export class Auth {
  #store: Store;

  constructor(store: Store) {
    this.#store = store;
  }

  // ── users ─────────────────────────────────────────────────────────────────────────────────────

  userCount(): number {
    return (this.#store.db.query('SELECT COUNT(*) AS n FROM users').get() as { n: number }).n;
  }

  async createUser(username: string, password: string): Promise<UserRow> {
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
          'INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?) RETURNING id, username, role, created_at',
        )
        .get(username, hash, Date.now()) as {
        id: number;
        username: string;
        role: string;
        created_at: number;
      };
      return toUser(row);
    } catch (err) {
      if (String(err).includes('UNIQUE')) throw new AuthError(`user "${username}" already exists`);
      throw err;
    }
  }

  listUsers(): UserRow[] {
    return (
      this.#store.db
        .query('SELECT id, username, role, created_at FROM users ORDER BY username')
        .all() as Array<{ id: number; username: string; role: string; created_at: number }>
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
      .query('SELECT id, username, role, created_at, password_hash FROM users WHERE username = ?')
      .get(username) as
      | { id: number; username: string; role: string; created_at: number; password_hash: string }
      | null;
    // One error for both wrong-user and wrong-password: naming which half failed turns the login
    // form into a username oracle.
    if (!row || !(await Bun.password.verify(password, row.password_hash))) {
      throw new AuthError('invalid username or password');
    }
    const session = randomSecret('pstack_ses_');
    this.#store.db
      .query('INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)')
      .run(sha256(session), row.id, Date.now(), Date.now() + SESSION_TTL_MS);
    return { session, user: toUser(row) };
  }

  sessionUser(session: string): UserRow | null {
    const row = this.#store.db
      .query(
        `SELECT u.id, u.username, u.role, u.created_at FROM sessions s
         JOIN users u ON u.id = s.user_id
         WHERE s.id_hash = ? AND s.expires_at > ?`,
      )
      .get(sha256(session), Date.now()) as
      | { id: number; username: string; role: string; created_at: number }
      | null;
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
        `SELECT u.id, u.username, u.role, u.created_at, t.id AS token_id FROM tokens t
         JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`,
      )
      .get(sha256(token)) as
      | { id: number; username: string; role: string; created_at: number; token_id: number }
      | null;
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
}
