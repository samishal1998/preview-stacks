// Package settings is the handful of host knobs an operator may change at RUNTIME, without
// restarting the control container.
//
// ── TWO KEYS, AND THE LIST IS CLOSED ─────────────────────────────────────────────────────────────
//
//	max_jobs      integer >= 1     how many lifecycle jobs run at once across every stack
//	default_role  one of the four  the role an account created with no role named gets
//
// This is NOT a generic key-value store: Set validates both the key and the value, and an unknown
// key is REFUSED rather than stored. A settings table that accepts anything becomes the dumping
// ground the migration says it is not, and every "just one more key" arrives without a validator.
// Adding a key is an arm in Set's switch, a reader beside MaxJobs, and a permissions row.
//
// It is also not a hole in invariant 10: every row is configuration an operator CHOSE. Nothing here
// describes what is running.
//
// ── PRECEDENCE, PER KEY ──────────────────────────────────────────────────────────────────────────
//
//	max_jobs      database > PSTACK_MAX_JOBS (envMaxJobs) > jobs.DefaultMaxRunning
//	default_role  database > viewer                        — there is NO environment variable
//
// The environment variable is the DEFAULT, not the authority. An operator who never opens the UI
// keeps exactly the behaviour they had, and one who sets the value in the UI is not overridden by
// the container's environment on the next restart. (`PSTACK_MAX_JOBS` is parsed once at boot by the
// API's TuningFromEnv and handed in here — this package reads no environment itself, so there is
// one parse of that variable and one place to find it.)
//
// ── A READ NEVER FAILS, IT RESOLVES DOWNWARD ─────────────────────────────────────────────────────
//
// The readers return a value and no error. A database that will not answer, a missing row and a
// row an operator hand-edited to nonsense all fall to the next source down, because every caller's
// alternative is worse in exactly one direction:
//
//   - max_jobs — a zero cap would stall every job on the host. Falling back keeps it running.
//   - default_role — an unrankable role is refused by auth.Role.Rank, so a garbage row would mint
//     accounts that can do nothing; falling back to `viewer` is the same least-privilege direction
//     the rest of the auth code takes. It can never resolve UPWARD, and never to admin.
//
// ── NEVER CALL A READER FROM INSIDE store.Tx ─────────────────────────────────────────────────────
//
// Every method here goes to Store.DB, and the pool is ONE connection (store's header, rule 16): a
// read from inside a transaction waits for the connection that transaction holds — a permanent
// self-deadlock. auth.SsoSignIn is one long transaction, so the SSO default role must be resolved
// BEFORE it is called and handed in as SsoSignInOpts.DefaultRole, never looked up in provision.
package settings

import (
	"strconv"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Error is a refused key or value. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool { _, ok := err.(*Error); return ok }

// The keys. There are no others, and Set refuses anything that is not one of these.
const (
	KeyMaxJobs     = "max_jobs"
	KeyDefaultRole = "default_role"
)

// Settings is the table. Owner: the server; every method is one statement.
type Settings struct {
	Store *store.Store
	// envMaxJobs is PSTACK_MAX_JOBS as the API parsed it at boot, or 0 when it was unset or not a
	// positive number. A DEFAULT that a stored row outranks — see the header.
	envMaxJobs int
}

// New wraps a store. envMaxJobs is api.Options.MaxJobs (PSTACK_MAX_JOBS), zero meaning unset.
func New(s *store.Store, envMaxJobs int) *Settings {
	return &Settings{Store: s, envMaxJobs: envMaxJobs}
}

// get is the stored value, or "" for no row — and for a read that failed, which resolves the same
// way on purpose (see the header).
func (s *Settings) get(key string) string {
	var v string
	if err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// MaxJobs is the running-job cap: database > PSTACK_MAX_JOBS > jobs.DefaultMaxRunning.
//
// The result is what jobs.Registry.SetMaxRunning is given, at boot and after every write. Lowering
// it cancels nothing that is already running — see that method.
func (s *Settings) MaxJobs() int {
	if n, err := strconv.Atoi(s.get(KeyMaxJobs)); err == nil && n >= 1 {
		return n
	}
	if s.envMaxJobs >= 1 {
		return s.envMaxJobs
	}
	return jobs.DefaultMaxRunning
}

// DefaultRole is the role for an account created with no role named: database > viewer.
//
// It NEVER widens what a caller may do. It decides what accounts they create get, and creating an
// account is already admin-only; the SSO side asks for it only when a provider's own defaultRole is
// empty ("inherit the host default"). A stored value that is not one of the four is ignored, so
// this cannot resolve to anything unrankable — or to admin by omission.
func (s *Settings) DefaultRole() string {
	if v := s.get(KeyDefaultRole); auth.ValidRole(v) {
		return v
	}
	return string(auth.Viewer)
}

// Set validates and stores one setting. The key must be one of the two and the value must be valid
// FOR that key; anything else returns *Error and writes nothing.
//
// Values are stored in canonical form ("008" becomes "8"), so a reader never has to be lenient
// about what a writer accepted.
func (s *Settings) Set(key, value string) error {
	switch key {
	case KeyMaxJobs:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return &Error{Msg: "max_jobs must be a whole number of 1 or more"}
		}
		value = strconv.Itoa(n)
	case KeyDefaultRole:
		if !auth.ValidRole(value) {
			return &Error{Msg: "default_role must be one of: " + auth.RolesText}
		}
	default:
		return &Error{Msg: "unknown setting: " + key}
	}
	_, err := s.Store.DB.Exec(
		"INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)"+
			" ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
		key, value, time.Now().UnixMilli())
	return err
}
