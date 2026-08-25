// Package auth is accounts, sessions and personal API tokens, on top of the Store.
//
// ── THE MODEL, AND WHY COOKIES ───────────────────────────────────────────────────────────────────
//
// Three principals can authenticate a request:
//
//  1. `PSTACK_TOKEN` — the root/machine credential. It PREDATES accounts, `init` generates it, and
//     CI pipelines hold it; retiring it would be a forced migration for every caller. It stays.
//  2. A personal API token (`pstack_pat_…`) — per user, for scripts that should not hold root.
//  3. A session — httpOnly cookie set by username+password login, OR by a completed SSO round trip
//     (sso): an SSO login mints exactly the same row, so nothing downstream of the cookie — the
//     gate, the terminal, personal tokens — knows the difference. Cookies are the deliberate
//     choice, not an implementation detail: `EventSource` (the job log stream) and `WebSocket`
//     (the terminal) cannot send an `Authorization` header from a browser, but the browser
//     attaches cookies to both automatically on the same origin. Session auth is what makes those
//     surfaces authenticable at all.
//
// ── STORAGE DISCIPLINE ───────────────────────────────────────────────────────────────────────────
//
// Passwords: argon2id (phc.go). Sessions and tokens: the database stores the SHA-256 of the secret,
// never the secret. A database file read (or a backup lying around) must not be a session hijack or
// a token theft. Verification is hash-then-lookup, which also makes it constant-time-ish by
// construction — the lookup key is a digest, not the caller's input.
//
// ── BOOTSTRAP ────────────────────────────────────────────────────────────────────────────────────
//
// The first admin comes from `PSTACK_ADMIN_USER`/`PSTACK_ADMIN_PASSWORD` at boot, or from
// `POST /api/auth/bootstrap` authenticated by `PSTACK_TOKEN`. Both are ONLY honoured while the users
// table is empty — after that they are inert, so a leaked compose file with the env pair in it
// cannot mint extra admins later.
//
// ── THE PORT ─────────────────────────────────────────────────────────────────────────────────────
//
// bun:sqlite was synchronous, so a run of statements could not interleave with another request's.
// Here the multi-statement paths (SetPassword, SsoSignIn) run inside Store.Tx, and every helper
// takes a store.Querier so it works on the *sql.DB and on the *sql.Tx alike. Errors: *Error is the
// TypeScript's AuthError (the API answers 400 with its text), *sso.Error the SsoError; anything
// else is the database and is a 500.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Error is the TypeScript's AuthError: the caller's fault, and its text is shown to them.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is an *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func errorf(msg string) error { return &Error{Msg: msg} }

// SessionTTLMs is thirty days. Fixed, not sliding — a stolen cookie should not be renewable forever.
const SessionTTLMs = 30 * 24 * 60 * 60 * 1000

var username = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// UserRow is an account as every response carries it.
type UserRow struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Email is from an SSO provider's claims; null for every locally-created account.
	Email     *string `json:"email"`
	CreatedAt int64   `json:"createdAt"`
}

// Principal is who a request is. Root is PSTACK_TOKEN; a user carries its row; a share is a signed
// read-only link to ONE deployment (share) — it can see what its Views allow on that deployment
// and nothing else, which the API enforces right after the gate, before any route.
type Principal struct {
	Kind Kind
	// User is set for KindUser.
	User *UserRow
	// Deployment and Views are set for KindShare.
	Deployment string
	Views      []share.View
}

// Kind is a principal's kind.
type Kind string

// The kinds.
const (
	KindRoot  Kind = "root"
	KindUser  Kind = "user"
	KindShare Kind = "share"
)

func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// randomSecret is prefix + 32 random bytes as lowercase hex.
func randomSecret(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func now() int64 { return time.Now().UnixMilli() }

// userCols is every column a UserRow needs, spelled once — five queries used to drift apart on this.
const userCols = "id, username, role, email, created_at"

type scanner interface{ Scan(dest ...any) error }

func scanUser(s scanner, extra ...any) (*UserRow, error) {
	var u UserRow
	var email sql.NullString
	dest := append([]any{&u.ID, &u.Username, &u.Role, &email, &u.CreatedAt}, extra...)
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	if email.Valid {
		u.Email = &email.String
	}
	return &u, nil
}

// scanUserRow is scanUser for a single-row query: nil, nil when there is no row.
func scanUserRow(row *sql.Row, extra ...any) (*UserRow, error) {
	u, err := scanUser(row, extra...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// Auth is the account layer over one Store.
type Auth struct {
	store     *store.Store
	transient *sso.SqliteTransientStore
}

// New returns the Auth over s.
func New(s *store.Store) *Auth {
	return &Auth{store: s, transient: &sso.SqliteTransientStore{DB: s.DB}}
}

// ── users ───────────────────────────────────────────────────────────────────────────────────────

// UserCount is how many accounts exist.
func (a *Auth) UserCount() (int64, error) { return userCount(a.store.DB) }

func userCount(q store.Querier) (int64, error) {
	var n int64
	err := q.QueryRow("SELECT COUNT(*) AS n FROM users").Scan(&n)
	return n, err
}

// CreateOpts are the optional fields of a new account. An empty Role is 'admin'; an empty Email is
// stored as NULL.
type CreateOpts struct {
	Role  string
	Email string
}

// CreateUser validates, hashes and inserts.
func (a *Auth) CreateUser(username, password string, opts CreateOpts) (*UserRow, error) {
	return createUser(a.store.DB, username, password, opts)
}

func createUser(q store.Querier, name, password string, opts CreateOpts) (*UserRow, error) {
	if !username.MatchString(name) {
		return nil, errorf("username must match /^[a-z0-9][a-z0-9._-]{1,31}$/ — lowercase, 2–32 chars, letters/digits/._-")
	}
	if js.Len(password) < 8 {
		return nil, errorf("password must be at least 8 characters")
	}
	hash := HashPassword(password)
	var role, email sql.NullString
	if opts.Role != "" {
		role = sql.NullString{String: opts.Role, Valid: true}
	}
	if opts.Email != "" {
		email = sql.NullString{String: opts.Email, Valid: true}
	}
	u, err := scanUser(q.QueryRow(
		"INSERT INTO users (username, password_hash, role, email, created_at) VALUES (?, ?, COALESCE(?, 'admin'), ?, ?) RETURNING "+userCols,
		name, hash, role, email, now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errorf(`user "` + name + `" already exists`)
		}
		return nil, err
	}
	return u, nil
}

// ListUsers is every account, by username (SQLite BINARY collation).
func (a *Auth) ListUsers() ([]UserRow, error) {
	rows, err := a.store.DB.Query("SELECT " + userCols + " FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectUsers(rows)
}

func collectUsers(rows *sql.Rows) ([]UserRow, error) {
	out := []UserRow{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// DeleteUser refuses to delete the last user: an instance with accounts and no way to log in is
// only recoverable by editing the database over SSH, and nothing in the UI can explain that state.
func (a *Auth) DeleteUser(id int64) (bool, error) {
	n, err := a.UserCount()
	if err != nil {
		return false, err
	}
	if n <= 1 {
		return false, errorf("cannot delete the last user — create another account first")
	}
	// Sessions and tokens go with the row (ON DELETE CASCADE).
	return changed(a.store.DB.Exec("DELETE FROM users WHERE id = ?", id))
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetPassword changes a password.
//
// Every session and token for that user is revoked in the same transaction. A password change is
// usually a response to "someone may have this" — leaving the sessions it was protecting alive
// would make the change theatre. The caller's own session dies with the rest; the UI signs them
// back in rather than pretending nothing happened.
func (a *Auth) SetPassword(id int64, password string) (bool, error) {
	if js.Len(password) < 8 {
		return false, errorf("password must be at least 8 characters")
	}
	hash := HashPassword(password)
	var did bool
	err := a.store.Tx(func(q store.Querier) error {
		var err error
		if did, err = changed(q.Exec("UPDATE users SET password_hash = ? WHERE id = ?", hash, id)); err != nil || !did {
			return err
		}
		if _, err := q.Exec("DELETE FROM sessions WHERE user_id = ?", id); err != nil {
			return err
		}
		_, err = q.Exec("DELETE FROM tokens WHERE user_id = ?", id)
		return err
	})
	return did, err
}

// Bootstrap is the first-admin bootstrap. Only while the table is empty — see the package comment.
// Returns nil, nil when it declined, so the caller can distinguish "created" from "already
// bootstrapped" without a race.
func (a *Auth) Bootstrap(username, password string) (*UserRow, error) {
	n, err := a.UserCount()
	if err != nil || n > 0 {
		return nil, err
	}
	return a.CreateUser(username, password, CreateOpts{})
}

// ── sessions ────────────────────────────────────────────────────────────────────────────────────

// Login verifies credentials and mints a session. The returned value is the COOKIE value; only its
// hash is stored.
func (a *Auth) Login(username, password string) (session string, user *UserRow, err error) {
	var hash string
	row, err := scanUserRow(a.store.DB.QueryRow("SELECT "+userCols+", password_hash FROM users WHERE username = ?", username), &hash)
	if err != nil {
		return "", nil, err
	}
	// One error for both wrong-user and wrong-password: naming which half failed turns the login
	// form into a username oracle.
	if row == nil || !VerifyPassword(password, hash) {
		return "", nil, errorf("invalid username or password")
	}
	session, err = mintSession(a.store.DB, row.ID)
	return session, row, err
}

// mintSession is the one place a session row is created — password login and SSO both come through here.
func mintSession(q store.Querier, userID int64) (string, error) {
	session := randomSecret("pstack_ses_")
	_, err := q.Exec("INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		sha256Hex(session), userID, now(), now()+SessionTTLMs)
	return session, err
}

// SessionUser resolves a cookie value to its account, or nil.
func (a *Auth) SessionUser(session string) (*UserRow, error) {
	return scanUserRow(a.store.DB.QueryRow(
		`SELECT u.id, u.username, u.role, u.email, u.created_at FROM sessions s
         JOIN users u ON u.id = s.user_id
         WHERE s.id_hash = ? AND s.expires_at > ?`, sha256Hex(session), now()))
}

// Logout deletes the session row.
func (a *Auth) Logout(session string) error {
	_, err := a.store.DB.Exec("DELETE FROM sessions WHERE id_hash = ?", sha256Hex(session))
	return err
}

// PruneSessions is housekeeping — expired rows are already unusable; this just stops them accumulating.
func (a *Auth) PruneSessions() error {
	_, err := a.store.DB.Exec("DELETE FROM sessions WHERE expires_at <= ?", now())
	return err
}

// ── personal API tokens ─────────────────────────────────────────────────────────────────────────

// TokenRow is a personal token as the list shows it: never the secret.
type TokenRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

// CreateToken mints a token for a user. The plaintext is returned ONCE and never retrievable again.
func (a *Auth) CreateToken(userID int64, name string) (token string, id int64, err error) {
	if strings.TrimSpace(name) == "" {
		return "", 0, errorf("a token needs a name — it is the only handle left later")
	}
	token = randomSecret("pstack_pat_")
	err = a.store.DB.QueryRow("INSERT INTO tokens (user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?) RETURNING id",
		userID, strings.TrimSpace(name), sha256Hex(token), now()).Scan(&id)
	return token, id, err
}

// TokenUser resolves a personal token to its account, or nil, touching last_used_at on the way.
func (a *Auth) TokenUser(token string) (*UserRow, error) {
	var tokenID int64
	row, err := scanUserRow(a.store.DB.QueryRow(
		`SELECT u.id, u.username, u.role, u.email, u.created_at, t.id AS token_id FROM tokens t
         JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, sha256Hex(token)), &tokenID)
	if err != nil || row == nil {
		return nil, err
	}
	// Best-effort bookkeeping — an operator deciding which stale token to revoke needs this.
	_, _ = a.store.DB.Exec("UPDATE tokens SET last_used_at = ? WHERE id = ?", now(), tokenID)
	return row, nil
}

// ListTokens is a user's tokens, newest first.
func (a *Auth) ListTokens(userID int64) ([]TokenRow, error) {
	rows, err := a.store.DB.Query("SELECT id, name, created_at, last_used_at FROM tokens WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenRow{}
	for rows.Next() {
		var t TokenRow
		var last sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t.LastUsedAt = jsonx.Int(last.Int64)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken is scoped to the owner: one user must not be able to revoke another's token by guessing ids.
func (a *Auth) DeleteToken(userID, id int64) (bool, error) {
	return changed(a.store.DB.Exec("DELETE FROM tokens WHERE id = ? AND user_id = ?", id, userID))
}

// ── SSO: the operator's identity providers ────────────────────────────────────────────────────────
//
// The protocol lives in sso. What lives HERE is the part that touches accounts: the stored
// providers (keyed by an operator-chosen slug since migration 7), the (provider, subject) → user
// links, and turning a verified identity into the same session a password login produces. The slug
// names a ROW, never an identity — sso_links.provider_key is what the callback derives (the
// discovery issuer for oidc, the preset key or "custom" for oauth2), so two rows on the same
// upstream directory share their links, which is correct: the same subject there is the same person.

// Transient is where the PKCE verifier waits out the round trip. SQLite, because that is what this
// service already wires up; the interface is what makes Redis or Postgres a config change later.
func (a *Auth) Transient() sso.TransientStore { return a.transient }

// SsoProviderRow is one stored provider: its slug, the config and the secret it is paired with.
type SsoProviderRow struct {
	Key          string
	Config       *sso.Config
	ClientSecret string
	UpdatedAt    int64
}

// ssoKey is the slug rule (store migration 7's DDL states it; the schema cannot enforce it).
var ssoKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// parseSsoRow re-validates a stored config rather than trusting it: a row written by an older
// version (or edited by hand over SSH, which this project expects) must not put a half-shape into
// the flow. nil when it does not validate.
func parseSsoRow(key, raw, secret string, updatedAt int64) *SsoProviderRow {
	v, err := omap.Parse([]byte(raw))
	if err != nil {
		return nil
	}
	cfg, err := sso.ParseConfig(v)
	if err != nil {
		return nil
	}
	return &SsoProviderRow{Key: key, Config: cfg, ClientSecret: secret, UpdatedAt: updatedAt}
}

// ListSsoProviders is every stored provider, in key order (SQLite BINARY collation). A row that no
// longer validates is skipped, the way the single-provider read treated it — never a half-shape.
func (a *Auth) ListSsoProviders() ([]SsoProviderRow, error) {
	rows, err := a.store.DB.Query("SELECT key, config, client_secret, updated_at FROM sso_providers ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SsoProviderRow{}
	for rows.Next() {
		var key, raw, secret string
		var updatedAt int64
		if err := rows.Scan(&key, &raw, &secret, &updatedAt); err != nil {
			return nil, err
		}
		if r := parseSsoRow(key, raw, secret, updatedAt); r != nil {
			out = append(out, *r)
		}
	}
	return out, rows.Err()
}

// SsoProvider reads one provider, or nil when there is none or the row does not validate.
func (a *Auth) SsoProvider(key string) (*SsoProviderRow, error) {
	var raw, secret string
	var updatedAt int64
	err := a.store.DB.QueryRow("SELECT config, client_secret, updated_at FROM sso_providers WHERE key = ?", key).Scan(&raw, &secret, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseSsoRow(key, raw, secret, updatedAt), nil
}

// SetSsoProvider saves one provider under its slug. An EMPTY clientSecret keeps the one stored for
// THAT key — the read endpoint returns a mask, so a form that round-trips the mask must not
// overwrite the real secret with it. There is no stored secret to keep on a key's first save, so an
// empty one is refused there.
func (a *Auth) SetSsoProvider(key string, cfg *sso.Config, clientSecret string) error {
	if !ssoKey.MatchString(key) {
		return errorf("provider key must match /^[a-z0-9][a-z0-9-]{0,31}$/ — a lowercase slug of letters, digits and dashes")
	}
	secret := clientSecret
	if secret == "" {
		err := a.store.DB.QueryRow("SELECT client_secret FROM sso_providers WHERE key = ?", key).Scan(&secret)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if secret == "" {
		return errorf("clientSecret is required")
	}
	body, err := jsonx.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = a.store.DB.Exec(
		"INSERT INTO sso_providers (key, config, client_secret, updated_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(key) DO UPDATE SET config = excluded.config, client_secret = excluded.client_secret, updated_at = excluded.updated_at",
		key, string(body), secret, now())
	return err
}

// DeleteSsoProvider forgets one provider. The links stay: those accounts keep their password and
// their tokens, and a re-added provider on the same upstream directory resolves to the same people.
func (a *Auth) DeleteSsoProvider(key string) (bool, error) {
	return changed(a.store.DB.Exec("DELETE FROM sso_providers WHERE key = ?", key))
}

// SsoLink is one (provider, subject) → user row.
type SsoLink struct {
	ProviderKey string `json:"providerKey"`
	Subject     string `json:"subject"`
	CreatedAt   int64  `json:"createdAt"`
	LastLoginAt *int64 `json:"lastLoginAt"`
}

// SsoLinks lists a user's provider links.
func (a *Auth) SsoLinks(userID int64) ([]SsoLink, error) { return ssoLinks(a.store.DB, userID) }

func ssoLinks(q store.Querier, userID int64) ([]SsoLink, error) {
	rows, err := q.Query("SELECT provider_key, subject, created_at, last_login_at FROM sso_links WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SsoLink{}
	for rows.Next() {
		var l SsoLink
		var last sql.NullInt64
		if err := rows.Scan(&l.ProviderKey, &l.Subject, &l.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			l.LastLoginAt = jsonx.Int(last.Int64)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// How says which of the three SsoSignIn outcomes happened, so the adoption case can be logged.
type How string

// The outcomes.
const (
	Linked  How = "linked"
	Adopted How = "adopted"
	Created How = "created"
)

// SsoSignInOpts are the provider's provisioning rules: the whole allow-rule set travels here, as
// one argument, so that adding a rule is a field rather than a third positional parameter and every
// one of them is enforced in the single gate at the top of SsoSignIn.
type SsoSignInOpts struct {
	DefaultRole         string
	AllowedEmailDomains []string
	AllowedUsernames    []string
	RequiredGroups      []string
	// GroupsErr is why the provider's group list did not arrive, or nil. The caller does the fetch
	// (it holds the access token), and this is how the failure reaches the gate INSTEAD of being
	// swallowed: a rate limit, a revoked scope and a network blip are not "you are not a member",
	// and collapsing them into one refusal sends the operator to fix the wrong thing.
	GroupsErr error
}

// SignIn is a minted session.
type SignIn struct {
	Session string
	User    *UserRow
	How     How
}

// SsoSignIn turns a verified provider identity into a session.
//
// THE ORDER IS THE SECURITY. `(providerKey, subject)` first, because it is the only stable
// identity; email second and ONLY to adopt a pre-existing local account, gated on the provider
// having said the address is verified; a new account last. An unverified email must never reach
// an existing account — that is the one path in this feature that could hand over someone else's
// privileges, and it is why `EmailVerified == nil` (the provider never said) is not good enough.
//
// The whole thing is one transaction: the TypeScript's single thread made its lookup-then-insert
// atomic for free, and two callbacks for one new subject must not both provision.
func (a *Auth) SsoSignIn(providerKey string, identity *sso.Identity, opts SsoSignInOpts) (*SignIn, error) {
	// Fail CLOSED: a non-empty allow-list with no email to check is a refusal, not a pass. GitHub
	// returns a null email for a private profile, so this is a real case and not a theoretical one.
	if !sso.EmailAllowed(identity.Email, opts.AllowedEmailDomains) {
		if identity.Email != "" {
			return nil, &sso.Error{Msg: identity.Email + " is not in an allowed email domain"}
		}
		return nil, &sso.Error{Msg: "this provider returned no email address, and sign-in is restricted to specific email domains"}
	}
	// Same shape, same failure direction: a rule and nothing to check it against is a refusal. A
	// bare OIDC provider frequently supplies no username at all, which is why the message says which
	// half is missing rather than "access denied".
	if !sso.UsernameAllowed(identity.Username, opts.AllowedUsernames) {
		if identity.Username != "" {
			return nil, &sso.Error{Msg: identity.Username + " is not an allowed username"}
		}
		return nil, &sso.Error{Msg: "this provider returned no username, and sign-in is restricted to specific usernames"}
	}
	if len(opts.RequiredGroups) > 0 {
		// TWO DISTINCT REASONS, deliberately. "Not a member" is fixed by adding someone to a group;
		// "could not be determined" is a scope, a rate limit or an outage, and is the one an
		// operator would otherwise spend an afternoon debugging as the other.
		if opts.GroupsErr != nil {
			return nil, &sso.Error{Msg: "your group memberships could not be determined, and sign-in is restricted to specific groups — " + opts.GroupsErr.Error()}
		}
		if !sso.GroupsAllowed(identity.Groups, opts.RequiredGroups) {
			return nil, &sso.Error{Msg: "you are not in a group this host allows to sign in (" + strings.Join(opts.RequiredGroups, ", ") + ")"}
		}
	}

	var out *SignIn
	err := a.store.Tx(func(q store.Querier) error {
		var err error
		out, err = ssoSignIn(q, providerKey, identity, opts)
		return err
	})
	return out, err
}

func ssoSignIn(q store.Querier, providerKey string, identity *sso.Identity, opts SsoSignInOpts) (*SignIn, error) {
	var linkedID int64
	err := q.QueryRow("SELECT user_id FROM sso_links WHERE provider_key = ? AND subject = ?", providerKey, identity.Subject).Scan(&linkedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		row, err := scanUserRow(q.QueryRow("SELECT "+userCols+" FROM users WHERE id = ?", linkedID))
		if err != nil {
			return nil, err
		}
		if row != nil {
			if _, err := q.Exec("UPDATE sso_links SET last_login_at = ? WHERE provider_key = ? AND subject = ?", now(), providerKey, identity.Subject); err != nil {
				return nil, err
			}
			// The provider owns the address; keep the local copy current so the allow-list and the UI
			// do not go stale after someone changes it upstream.
			if identity.Email != "" && (row.Email == nil || identity.Email != *row.Email) {
				if _, err := q.Exec("UPDATE users SET email = ? WHERE id = ?", identity.Email, row.ID); err != nil {
					return nil, err
				}
				row.Email = jsonx.Str(identity.Email)
			}
			session, err := mintSession(q, row.ID)
			return &SignIn{Session: session, User: row, How: Linked}, err
		}
		// The account was deleted out from under the link (CASCADE should have taken it, so this is
		// a repaired database). Drop the orphan and provision afresh.
		if _, err := q.Exec("DELETE FROM sso_links WHERE provider_key = ? AND subject = ?", providerKey, identity.Subject); err != nil {
			return nil, err
		}
	}

	if identity.Email != "" && identity.EmailVerified != nil && *identity.EmailVerified {
		rows, err := q.Query("SELECT "+userCols+" FROM users WHERE email = ?", identity.Email)
		if err != nil {
			return nil, err
		}
		matches, err := collectUsers(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		// Exactly one, and it must not already belong to another provider subject. Two rows sharing
		// an address is an ambiguity, and guessing which one to adopt is guessing whose account to
		// hand over.
		if len(matches) == 1 {
			only := &matches[0]
			links, err := ssoLinks(q, only.ID)
			if err != nil {
				return nil, err
			}
			if len(links) == 0 {
				if err := link(q, providerKey, identity.Subject, only.ID); err != nil {
					return nil, err
				}
				session, err := mintSession(q, only.ID)
				return &SignIn{Session: session, User: only, How: Adopted}, err
			}
		}
	}

	user, err := provision(q, identity, opts.DefaultRole)
	if err != nil {
		return nil, err
	}
	if err := link(q, providerKey, identity.Subject, user.ID); err != nil {
		return nil, err
	}
	session, err := mintSession(q, user.ID)
	return &SignIn{Session: session, User: user, How: Created}, err
}

// provision creates the local account behind an SSO identity.
//
// The password is 32 random bytes nobody will ever hold, hashed like any other: the row stays
// ordinary, Login needs no "is this an SSO user" branch, and there is no null-password state for
// some future code path to treat as "no password required". An operator who wants this account to
// also have a password sets one the usual way.
//
// The username is COSMETIC — identity is `(providerKey, subject)`. A collision therefore takes a
// suffix rather than linking to whoever already holds the name.
func provision(q store.Querier, identity *sso.Identity, defaultRole string) (*UserRow, error) {
	raw := identity.Username
	if raw == "" {
		raw, _, _ = strings.Cut(identity.Email, "@")
	}
	base := sso.SanitizeUsername(raw, identity.Subject)
	role := defaultRole
	if role == "" {
		role = "admin"
	}
	for attempt := 0; attempt < 50; attempt++ {
		suffix := ""
		if attempt > 0 {
			suffix = "-" + js.NumberString(float64(attempt+1))
		}
		name := js.Slice(base, 0, 32-len(suffix)) + suffix
		u, err := createUser(q, name, randomSecret(""), CreateOpts{Role: role, Email: identity.Email})
		if err == nil {
			return u, nil
		}
		if IsError(err) && strings.Contains(err.Error(), "already exists") {
			continue
		}
		return nil, err
	}
	return nil, errorf(`could not find a free username for "` + base + `" — 50 variants were taken`)
}

func link(q store.Querier, providerKey, subject string, userID int64) error {
	_, err := q.Exec("INSERT INTO sso_links (provider_key, subject, user_id, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)",
		providerKey, subject, userID, now(), now())
	return err
}

// ── the portable export ─────────────────────────────────────────────────────────────────────────
//
// Two READ-ONLY listings that carry the stored digests, for internal/config's `pull config`. They
// are named to be conspicuous in a diff, like SecretOf in webhooks: a caller outside the export
// path is the moment to ask why, because everything else in this package is built so that no route
// returns these columns.
//
// A digest is not a credential — nobody logs in with an argon2 hash — but a file full of them is
// offline-crackable against every account at once, which is why the document that carries them is
// sealed and root-only. There is no matching Import* here: an insert with an ALREADY-hashed
// password is not an account operation this package offers (CreateUser hashes a plaintext,
// CreateToken mints a fresh secret), so the apply side does its own INSERT — see internal/config.

// ExportUser is one account as the portable document carries it. The JSON tags are that document's
// field names.
type ExportUser struct {
	Username string `json:"username"`
	// PasswordHash is the argon2id PHC string, verbatim, so a login keeps working on the new host.
	PasswordHash string  `json:"passwordHash"`
	Role         string  `json:"role"`
	Email        *string `json:"email"`
	CreatedAt    int64   `json:"createdAt"`
}

// ExportToken is one personal token as the portable document carries it. Keyed to its owner by
// USERNAME, not by id: row ids are per-host and mean nothing on the other side.
type ExportToken struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	// TokenHash is the SHA-256 of the token, verbatim, so scripts holding the token keep working.
	TokenHash string `json:"tokenHash"`
	CreatedAt int64  `json:"createdAt"`
}

// ExportUsers is every account WITH its password hash, by username.
func (a *Auth) ExportUsers() ([]ExportUser, error) {
	rows, err := a.store.DB.Query("SELECT username, password_hash, role, email, created_at FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExportUser{}
	for rows.Next() {
		var u ExportUser
		var email sql.NullString
		if err := rows.Scan(&u.Username, &u.PasswordHash, &u.Role, &email, &u.CreatedAt); err != nil {
			return nil, err
		}
		if email.Valid {
			u.Email = jsonx.Str(email.String)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ExportTokens is every personal token WITH its hash, oldest first.
func (a *Auth) ExportTokens() ([]ExportToken, error) {
	rows, err := a.store.DB.Query(
		`SELECT u.username, t.name, t.token_hash, t.created_at FROM tokens t
         JOIN users u ON u.id = t.user_id ORDER BY t.created_at, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExportToken{}
	for rows.Next() {
		var t ExportToken
		if err := rows.Scan(&t.Username, &t.Name, &t.TokenHash, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
