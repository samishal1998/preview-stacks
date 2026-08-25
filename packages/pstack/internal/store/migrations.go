// Code generated from src/store.ts MIGRATIONS by a one-off script; the text is verbatim (the
// comments inside a CREATE TABLE land in sqlite_master, so a fresh Go database is byte-identical to
// one the TypeScript created). DO NOT EDIT a shipped entry — append (see AGENTS.md, "A database change").

package store

// Migrations is append-only. Version N is applied when `user_version` < N; the slice index (1-based)
// IS the version, so ordering mistakes are structurally impossible.
var Migrations = []string{
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
	// 2 — webhook registrations and their delivery log.
	`
  CREATE TABLE notifiers (
    id          INTEGER PRIMARY KEY,
    -- The COMPOSABILITY seam: 'webhook' today; 'slack', 'discord', 'email' later are new values
    -- with their own config shape and their own delivery module. Adding one is a new file, not a
    -- migration.
    type        TEXT NOT NULL,
    -- Human label, the only handle in a list view.
    name        TEXT NOT NULL,
    -- JSON, shape owned by the type: {url} for webhook, {webhookUrl,channel} for slack, …
    config      TEXT NOT NULL,
    -- JSON array of event names, or the single entry '*' for everything including events added in
    -- later versions. Validated against events.ts at write time, so an unsubscribable typo is
    -- refused rather than discovered as silence months later.
    events      TEXT NOT NULL,
    --
    -- THE SIGNING SECRET IS STORED, NOT HASHED — and that is not an oversight.
    --
    -- A session id or an API token is verified by hashing what the caller presented and comparing;
    -- the plaintext is never needed again, so one-way is strictly better. An HMAC signing secret is
    -- the opposite: this process must COMPUTE HMAC(secret, body) on every delivery, so it needs the
    -- secret itself. Hashing it would make signing impossible.
    --
    -- "Write-only" therefore means what it means for a docker registry credential: never returned by
    -- any route, shown exactly once at creation. The protection is the 0700 directory, the 0600 file,
    -- and the fact that no read path exists — not a one-way function.
    secret      TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    -- Bookkeeping for a list view: was this thing ever actually reached?
    last_status TEXT,
    last_at     INTEGER
  );
  CREATE TABLE deliveries (
    id           INTEGER PRIMARY KEY,
    notifier_id  INTEGER NOT NULL REFERENCES notifiers(id) ON DELETE CASCADE,
    event_id     TEXT NOT NULL,
    event        TEXT NOT NULL,
    -- 'ok' | 'failed' | 'pending'
    status       TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    -- HTTP status of the last attempt, or null when the request never completed.
    response_code INTEGER,
    -- First line of the failure, for a list view. NEVER the request body — that would put the
    -- payload (and anything an emit site got wrong) into a second store.
    error        TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
  );
  CREATE INDEX deliveries_by_notifier ON deliveries (notifier_id, created_at DESC);
  `,
	// 3 — the terminal audit log.
	`
  CREATE TABLE terminal_sessions (
    id           INTEGER PRIMARY KEY,
    -- 'root (PSTACK_TOKEN)' or a username. Denormalized on purpose: deleting the account must not
    -- erase the record that it opened a shell on the host.
    actor        TEXT NOT NULL,
    deployment   TEXT NOT NULL,
    container    TEXT NOT NULL,
    container_id TEXT NOT NULL,
    shell        TEXT NOT NULL,
    started_at   INTEGER NOT NULL,
    -- Null while open, and STAYS null if pstack dies mid-session. An audit log that only records
    -- tidy endings misses exactly the sessions worth auditing.
    ended_at     INTEGER
  );
  CREATE INDEX terminal_sessions_recent ON terminal_sessions (started_at DESC);
  `,
	// 4 — host-level variables and secrets, referenced from specs as \${vars.NAME} / \${secrets.NAME}.
	`
  CREATE TABLE host_vars (
    name        TEXT PRIMARY KEY,
    -- Stored plainly for BOTH kinds. A variable is meant to be read back; a secret is the notifier
    -- precedent (see the comment on notifiers.secret): this process must hand the plaintext to
    -- hooks and compose, so one-way storage is impossible. "Write-only" means no API route returns
    -- it — the protection is the 0700 directory, the 0600 file, and the absence of a read path.
    value       TEXT NOT NULL,
    -- 1 = secret: value never leaves the server, and it is scrubbed from job and compose logs.
    secret      INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
  );
  `,
	// 5 — the delivered payload, so a delivery can be REPLAYED rather than described.
	`
  ALTER TABLE deliveries ADD COLUMN payload TEXT;
  `,
	// 6 — SSO: the operator's identity provider, the accounts it maps to, and the PKCE round trip.
	`
  -- Nullable, and NULL for every account that predates SSO. That is not a gap to backfill: the
  -- email-matching branch in Auth.ssoSignIn is deliberately unable to fire for those rows, so an
  -- account created before this change can only ever be linked by an operator on purpose.
  ALTER TABLE users ADD COLUMN email TEXT;
  CREATE INDEX users_by_email ON users (email);

  -- ONE provider. ` + "`" + `CHECK (id = 1)` + "`" + ` is the schema saying so: multiple simultaneous providers are
  -- explicitly out of scope, and a single row means there is no "which one is active" question to
  -- get wrong.
  CREATE TABLE sso_config (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    -- JSON, the SsoConfig shape in sso.ts, WITHOUT the secret.
    config        TEXT NOT NULL,
    --
    -- STORED, NOT HASHED — the notifier-secret precedent (see notifiers.secret above). The token
    -- exchange must PRESENT this string to the provider, so a one-way form makes the feature
    -- impossible. "Write-only" means what it means there: no route returns it, the read endpoint
    -- answers with a mask, and a submit that leaves it empty keeps what is stored rather than
    -- clearing it. The protection is the 0700 directory and the 0600 file.
    client_secret TEXT NOT NULL,
    updated_at    INTEGER NOT NULL
  );

  -- IDENTITY IS (provider_key, subject) AND NEVER EMAIL. People change email addresses; a provider
  -- subject is stable for the life of the account, which is the entire reason this table is keyed
  -- the way it is. provider_key is kept even though only one provider can be configured, so that
  -- swapping providers later cannot silently re-link one org's subjects onto another's accounts.
  CREATE TABLE sso_links (
    provider_key  TEXT NOT NULL,
    subject       TEXT NOT NULL,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    PRIMARY KEY (provider_key, subject)
  );
  CREATE INDEX sso_links_by_user ON sso_links (user_id);

  -- The PKCE verifier, for the length of one round trip and no longer. The only table here whose
  -- rows are garbage five minutes after they are written; SqliteTransientStore sweeps on write.
  CREATE TABLE sso_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    expires_at INTEGER NOT NULL
  );
  `,
	// 7 — SSO becomes multi-provider: sso_config's single row moves to a keyed table.
	`
  -- N simultaneous providers now. The key is an operator-chosen slug (^[a-z0-9][a-z0-9-]{0,31}$,
  -- enforced by internal/auth — SQLite cannot). It names the ROW, not the identity:
  -- sso_links.provider_key stays what the callback derives (the discovery ISSUER for oidc, the
  -- preset key or "custom" for oauth2), so two rows on the same github preset share a link
  -- namespace — the same subject from the same upstream directory is the same person.
  CREATE TABLE sso_providers (
    key           TEXT PRIMARY KEY,
    -- JSON, the same shape sso_config.config held, WITHOUT the secret.
    config        TEXT NOT NULL,
    --
    -- STORED, NOT HASHED — the notifier-secret precedent (see notifiers.secret above). The token
    -- exchange must PRESENT this string to the provider, so a one-way form makes the feature
    -- impossible. "Write-only" means what it means there: no route returns it, the read endpoint
    -- answers with a mask, and a submit that leaves it empty keeps what is stored rather than
    -- clearing it. The protection is the 0700 directory and the 0600 file.
    client_secret TEXT NOT NULL,
    updated_at    INTEGER NOT NULL
  );
  -- The stored provider moves over under a DERIVED key: the config's provider field when non-empty
  -- (github/gitlab/bitbucket/custom — ParseConfig only ever set it in oauth2 mode), else 'oidc'.
  -- Keep sso.Config.DerivedKey in sync with this CASE. json_extract is available because
  -- modernc.org/sqlite compiles in the JSON1 functions — proven by the migration-7 test in
  -- store_test.go, which runs this statement over a real v6 database.
  INSERT INTO sso_providers (key, config, client_secret, updated_at)
    SELECT CASE WHEN COALESCE(json_extract(config, '$.provider'), '') <> ''
                THEN json_extract(config, '$.provider')
                ELSE 'oidc' END,
           config, client_secret, updated_at
    FROM sso_config;
  DROP TABLE sso_config;
  `,
}
