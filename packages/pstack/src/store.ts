/**
 * Persistent state that is not the registry: users, sessions, API tokens — and, in later phases,
 * webhook registrations and delivery logs.
 *
 * ── WHY SQLITE, AND WHY THE REGISTRY STAYS FILES ─────────────────────────────────────────────────
 *
 * `bun:sqlite` is built into the runtime: no new container, no credential, no migration tooling —
 * one file under DATA_DIR, so "back up the host" remains "copy the directory". The deployment
 * registry does NOT move here: it is a deliberate cache-of-intent (a directory of YAML an operator
 * can read and repair over SSH), and nothing about accounts changes that. SQLite is only for the
 * domains that are *relational and secret-bearing* — exactly what a directory of YAML is bad at.
 *
 * The constructor takes a directory, not a file, because WAL mode creates `-wal`/`-shm` siblings —
 * permissions have to be right on the *directory* (0700), not just the db file.
 *
 * ── LOCATION ─────────────────────────────────────────────────────────────────────────────────────
 *
 * `<dataDir>/db/pstack.db`. NOT `<dataDir>/pstack.db`: the control container mounts only chosen
 * subdirectories of DATA_DIR (`deployments/`, and now `db/`) — a file at the top level would live in
 * the container's own filesystem and silently vanish on every `docker compose up -d` that recreates
 * it. The mount is added to the control compose template in this same change; `pstack init` creates
 * the directory 0700.
 *
 * ── MIGRATIONS ───────────────────────────────────────────────────────────────────────────────────
 *
 * `PRAGMA user_version` + an ordered list. Each entry runs once, in a transaction, and the version
 * is bumped after it. Editing a shipped migration is forbidden (it will not re-run anywhere it
 * already ran) — append a new one. This is deliberately the whole framework: a table of two-line
 * DDL strings does not need a dependency.
 *
 * A future user-provided Postgres (`PSTACK_DB_URL`) slots in by implementing this class's public
 * surface against `pg` — the callers only see prepared-statement-shaped methods, never SQL strings.
 */

import { Database } from 'bun:sqlite';
import { chmodSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

/**
 * Append-only. Version N is applied when `user_version` < N; the array index (1-based) IS the
 * version, so ordering mistakes are structurally impossible.
 */
const MIGRATIONS: string[] = [
  // 1 — users, sessions, personal API tokens.
  `
  CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    -- argon2id via Bun.password; never a raw or reversible form.
    password_hash TEXT NOT NULL,
    -- Everyone is 'admin' until real access control lands; the column exists so adding roles is a
    -- data change, not a schema change.
    role        TEXT NOT NULL DEFAULT 'admin',
    created_at  INTEGER NOT NULL
  );
  CREATE TABLE sessions (
    -- SHA-256 of the cookie value. A database read must not be a session hijack.
    id_hash     TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
  );
  CREATE TABLE tokens (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Label like "ci-deploy" — the only handle an operator has once the secret is gone.
    name        TEXT NOT NULL,
    -- SHA-256 of the full token. The plaintext is shown exactly once, at creation.
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    last_used_at INTEGER
  );
  `,
];

export type UserRow = {
  id: number;
  username: string;
  role: string;
  createdAt: number;
};

export class Store {
  readonly db: Database;
  readonly dir: string;

  constructor(dataDir: string) {
    this.dir = join(dataDir, 'db');
    mkdirSync(this.dir, { recursive: true });
    // Explicit, past the umask: this directory holds password hashes and token hashes, and WAL puts
    // live data in sibling files the file-level chmod below cannot cover.
    chmodSync(this.dir, 0o700);

    this.db = new Database(join(this.dir, 'pstack.db'), { create: true });
    chmodSync(join(this.dir, 'pstack.db'), 0o600);
    // WAL: readers do not block the writer. The API handles requests concurrently and every one of
    // them may touch a session row.
    this.db.exec('PRAGMA journal_mode = WAL;');
    this.db.exec('PRAGMA foreign_keys = ON;');
    this.#migrate();
  }

  #migrate(): void {
    const current = (this.db.query('PRAGMA user_version').get() as { user_version: number })
      .user_version;
    for (let v = current + 1; v <= MIGRATIONS.length; v++) {
      const apply = this.db.transaction(() => {
        this.db.exec(MIGRATIONS[v - 1]!);
        this.db.exec(`PRAGMA user_version = ${v}`);
      });
      apply();
    }
  }

  close(): void {
    this.db.close();
  }
}
