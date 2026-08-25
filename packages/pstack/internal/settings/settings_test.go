package settings

import (
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func open(t *testing.T, envMaxJobs int) *Settings {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, envMaxJobs)
}

// negative control: in MaxJobs, check envMaxJobs before the stored row → "the stored value must beat
// the environment" fires. An operator who sets the cap in the UI would be silently overridden by the
// container's environment on every restart, which is the whole point of the precedence.
func TestMaxJobsPrecedence(t *testing.T) {
	s := open(t, 2)
	if got := s.MaxJobs(); got != 2 {
		t.Fatalf("with no row, PSTACK_MAX_JOBS is the default: got %d, want 2", got)
	}
	if err := s.Set(KeyMaxJobs, "8"); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxJobs(); got != 8 {
		t.Fatalf("the stored value must beat the environment: got %d, want 8", got)
	}

	if got := open(t, 0).MaxJobs(); got != jobs.DefaultMaxRunning {
		t.Fatalf("no row and no environment is the built-in: got %d, want %d", got, jobs.DefaultMaxRunning)
	}

	// A row an operator hand-edited over SSH. Resolving to 0 would stall every job on the host, so
	// nonsense falls to the next source down rather than being believed.
	if _, err := s.Store.DB.Exec("UPDATE settings SET value = 'nonsense' WHERE key = ?", KeyMaxJobs); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxJobs(); got != 2 {
		t.Fatalf("a garbage row must fall back to the environment: got %d, want 2", got)
	}
}

// negative control: drop the auth.ValidRole check from DefaultRole and return the stored value → the
// hand-edited 'superuser' row comes back and the least-privilege assertion fires.
func TestDefaultRoleNeverResolvesUpward(t *testing.T) {
	s := open(t, 0)
	if got := s.DefaultRole(); got != string(auth.Viewer) {
		t.Fatalf("unset must be viewer, got %q", got)
	}
	if err := s.Set(KeyDefaultRole, string(auth.Maintainer)); err != nil {
		t.Fatal(err)
	}
	if got := s.DefaultRole(); got != string(auth.Maintainer) {
		t.Fatalf("the stored role must be honoured, got %q", got)
	}
	// Garbage resolves DOWN, never to admin and never to something unrankable: an account minted
	// from this value is what an SSO provider with an empty defaultRole gets.
	for _, bad := range []string{"superuser", "ADMIN", ""} {
		if _, err := s.Store.DB.Exec("UPDATE settings SET value = ? WHERE key = ?", bad, KeyDefaultRole); err != nil {
			t.Fatal(err)
		}
		got := s.DefaultRole()
		if got != string(auth.Viewer) {
			t.Fatalf("a stored %q resolved to %q; it must be %q", bad, got, auth.Viewer)
		}
		if !auth.ValidRole(got) || got == string(auth.Admin) {
			t.Fatalf("a stored %q resolved to %q — omission must never yield admin or an unrankable role", bad, got)
		}
	}
}

// negative control: drop the `default:` arm from Set's switch → the unknown key is written and
// "unknown keys must not reach the table" fires.
func TestSetValidatesKeyAndValue(t *testing.T) {
	s := open(t, 0)

	for _, bad := range []string{"0", "-1", "1.5", "abc", "", " 8", "1e3", "9999999999999999999999"} {
		if err := s.Set(KeyMaxJobs, bad); err == nil || !IsError(err) {
			t.Fatalf("max_jobs = %q was accepted (%v)", bad, err)
		}
	}
	// Stored canonically, and a second write updates rather than duplicating.
	if err := s.Set(KeyMaxJobs, "008"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key = ?", KeyMaxJobs).Scan(&v); err != nil || v != "8" {
		t.Fatalf(`"008" was stored as %q (%v), want "8"`, v, err)
	}
	if err := s.Set(KeyMaxJobs, "9"); err != nil {
		t.Fatal(err)
	}
	var rows int
	s.Store.DB.QueryRow("SELECT COUNT(*) FROM settings WHERE key = ?", KeyMaxJobs).Scan(&rows)
	if s.MaxJobs() != 9 || rows != 1 {
		t.Fatalf("a second write left %d rows and MaxJobs %d", rows, s.MaxJobs())
	}

	for _, role := range []string{"viewer", "developer", "maintainer", "admin"} {
		if err := s.Set(KeyDefaultRole, role); err != nil {
			t.Fatalf("default_role = %q refused: %v", role, err)
		}
	}
	for _, bad := range []string{"superuser", "ADMIN", "", "root", "share"} {
		if err := s.Set(KeyDefaultRole, bad); err == nil || !IsError(err) {
			t.Fatalf("default_role = %q was accepted (%v)", bad, err)
		}
	}

	// An unknown key is refused AND writes nothing: this table is a closed set, not a KV store.
	if err := s.Set("shell_command", "rm -rf /"); err == nil || !IsError(err) {
		t.Fatalf("an unknown key was accepted (%v)", err)
	}
	var strays int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM settings WHERE key NOT IN (?, ?)", KeyMaxJobs, KeyDefaultRole).Scan(&strays); err != nil {
		t.Fatal(err)
	}
	if strays != 0 {
		t.Fatalf("unknown keys must not reach the table: %d stored", strays)
	}
}
