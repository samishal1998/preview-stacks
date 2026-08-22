package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	t.Run("creates db/ 0700 and pstack.db 0600, applies every migration once", func(t *testing.T) {
		// negative control: drop the os.Chmod(dir, 0o700) → the directory mode assertion fails
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		st, _ := os.Stat(filepath.Join(dir, "db"))
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("db dir mode %o", st.Mode().Perm())
		}
		st, _ = os.Stat(filepath.Join(dir, "db", "pstack.db"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("db file mode %o", st.Mode().Perm())
		}
		var v int
		if err := s.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(Migrations) {
			t.Fatalf("user_version %d %v, want %d", v, err, len(Migrations))
		}
		var mode string
		s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode)
		if mode != "wal" {
			t.Fatalf("journal_mode %q", mode)
		}
		var fk int
		s.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk)
		if fk != 1 {
			t.Fatal("foreign_keys off")
		}
		// Every table the six migrations declare exists — the multi-statement Exec ran to the end.
		for _, tbl := range []string{"users", "sessions", "tokens", "notifiers", "deliveries", "terminal_sessions", "host_vars", "sso_config", "sso_links", "sso_state"} {
			var n int
			if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&n); err != nil || n != 1 {
				t.Fatalf("table %s missing", tbl)
			}
		}
		// Re-opening runs nothing (a second CREATE TABLE users would fail).
		s.Close()
		s2, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s2.Close()
	})

	t.Run("a migration is a transaction: a failing entry leaves user_version untouched", func(t *testing.T) {
		// negative control: apply the PRAGMA user_version bump outside the Tx → version advances past the failed entry
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		saved := Migrations
		defer func() { Migrations = saved }()
		Migrations = append(append([]string{}, saved...), "CREATE TABLE extra (x); CREATE TABLE users (x);")
		if err := s.migrate(); err == nil {
			t.Fatal("expected the duplicate CREATE to fail")
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(saved) {
			t.Fatalf("user_version %d after a failed migration", v)
		}
		var n int
		s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='extra'").Scan(&n)
		if n != 0 {
			t.Fatal("the first statement of a failed migration survived")
		}
	})

	t.Run("Tx commits on nil and rolls back on error", func(t *testing.T) {
		// negative control: make Tx Commit on error → the rolled-back row is found
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.Tx(func(q Querier) error {
			_, err := q.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('a', '1', 0, 0, 0)")
			return err
		})
		s.Tx(func(q Querier) error {
			q.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('b', '1', 0, 0, 0)")
			return os.ErrInvalid
		})
		var names string
		s.DB.QueryRow("SELECT group_concat(name) FROM host_vars").Scan(&names)
		if names != "a" {
			t.Fatalf("host_vars after the two transactions: %q", names)
		}
	})
}
