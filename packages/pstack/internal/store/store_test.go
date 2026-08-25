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
		// Every table the migrations declare exists — the multi-statement Exec ran to the end.
		// sso_config is deliberately absent: migration 7 replaced it with sso_providers.
		for _, tbl := range []string{"users", "sessions", "tokens", "notifiers", "deliveries", "terminal_sessions", "host_vars", "sso_providers", "sso_links", "sso_state"} {
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

	t.Run("migration 7 copies sso_config into sso_providers under the derived key", func(t *testing.T) {
		// negative control: replace the CASE in migration 7's INSERT with a bare
		// json_extract(config, '$.provider') → the oidc row lands under key "" and this fails
		//
		// The checked-in host fixture is a v6 database, so every future run executes migration 7
		// against it; this test is the same v6→v7 hop with both derivations pinned, and it is also
		// the proof that modernc.org/sqlite ships json_extract (JSON1).
		for _, c := range []struct{ name, config, wantKey string }{
			{"oauth2 keeps its provider name", `{"mode":"oauth2","provider":"github","clientId":"cid"}`, "github"},
			{"oidc (provider empty) derives 'oidc'", `{"mode":"oidc","provider":"","clientId":"cid","discoveryUrl":"https://accounts.example.com/"}`, "oidc"},
		} {
			dir := t.TempDir()
			saved := Migrations
			Migrations = saved[:6]
			s, err := Open(dir) // a real v6 database, made by the shipped migrations
			Migrations = saved
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec("INSERT INTO sso_config (id, config, client_secret, updated_at) VALUES (1, ?, 'shh', 42)", c.config); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			s.Close()
			s, err = Open(dir) // reopening runs migration 7
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			var key, config, secret string
			var updatedAt int64
			if err := s.DB.QueryRow("SELECT key, config, client_secret, updated_at FROM sso_providers").Scan(&key, &config, &secret, &updatedAt); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if key != c.wantKey || config != c.config || secret != "shh" || updatedAt != 42 {
				t.Fatalf("%s: got (%q, %q, %q, %d)", c.name, key, config, secret, updatedAt)
			}
			var n int
			s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='sso_config'").Scan(&n)
			if n != 0 {
				t.Fatalf("%s: sso_config survived", c.name)
			}
			s.Close()
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
