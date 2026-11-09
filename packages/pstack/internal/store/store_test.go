package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		for _, tbl := range []string{"users", "sessions", "tokens", "notifiers", "deliveries", "terminal_sessions", "host_vars", "sso_providers", "sso_links", "sso_state", "settings", "loki_config", "custom_routes", "forward_config", "scaler_config", "scaler_presets"} {
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

	t.Run("migration 8 adds an EMPTY settings table and touches nothing else", func(t *testing.T) {
		// negative control: delete the CREATE TABLE from migration 8's string, leaving the entry (so
		// user_version still reaches 8 and the failure is about the table, not the version) → the
		// SELECT COUNT(*) FROM settings errors with "no such table" and this fails.
		//
		// The checked-in host fixture is a real database that every future run migrates. An empty
		// settings table is what makes that database behave EXACTLY as it did before: every reader
		// in internal/settings falls back to the environment and then to the built-in default.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:7]
		s, err := Open(dir) // a real v7 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='settings'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v7 already has a settings table (%d, %v)", n, err)
		}
		// A row that predates the migration, to prove the hop is additive.
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 8
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM settings").Scan(&n); err != nil || n != 0 {
			t.Fatalf("settings after migration 8: %d rows, %v — it must exist and be empty", n, err)
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v7 data did not survive migration 8: %q %v", region, err)
		}
	})

	t.Run("migration 9 adds an EMPTY loki_config table and touches nothing else", func(t *testing.T) {
		// negative control: delete the CREATE TABLE from migration 9's string, leaving the entry (so
		// user_version still reaches 9 and the failure is about the table, not the version) → the
		// SELECT COUNT(*) FROM loki_config errors with "no such table" and this fails.
		//
		// An empty loki_config is slice 1's fixed config, so every host that migrates through this —
		// the checked-in host fixture included — runs Loki exactly as before.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:8]
		s, err := Open(dir) // a real v8 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='loki_config'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v8 already has a loki_config table (%d, %v)", n, err)
		}
		// A row that predates the migration, to prove the hop is additive.
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 9
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM loki_config").Scan(&n); err != nil || n != 0 {
			t.Fatalf("loki_config after migration 9: %d rows, %v — it must exist and be empty", n, err)
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v8 data did not survive migration 9: %q %v", region, err)
		}
	})

	t.Run("migration 10 adds an EMPTY custom_routes table and touches nothing else", func(t *testing.T) {
		// negative control: delete the CREATE TABLE from migration 10's string, leaving the entry (so
		// user_version still reaches 10 and the failure is about the table, not the version) → the
		// SELECT COUNT(*) FROM custom_routes errors with "no such table" and this fails.
		//
		// An empty custom_routes renders no pstack-routes.yml, so every host that migrates through
		// this (the checked-in host fixture included) routes exactly as before.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:9]
		s, err := Open(dir) // a real v9 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='custom_routes'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v9 already has a custom_routes table (%d, %v)", n, err)
		}
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 10
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM custom_routes").Scan(&n); err != nil || n != 0 {
			t.Fatalf("custom_routes after migration 10: %d rows, %v — it must exist and be empty", n, err)
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v9 data did not survive migration 10: %q %v", region, err)
		}
	})

	t.Run("migration 11 adds an EMPTY forward_config table and touches nothing else", func(t *testing.T) {
		// negative control: delete the CREATE TABLE from migration 11's string, leaving the entry (so
		// user_version still reaches 11 and the failure is about the table, not the version) → the
		// SELECT COUNT(*) FROM forward_config errors with "no such table" and this fails.
		//
		// An empty forward_config means no forwarding, so every host that migrates through this (the
		// checked-in host fixture included) serves exactly as before.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:10]
		s, err := Open(dir) // a real v10 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='forward_config'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v10 already has a forward_config table (%d, %v)", n, err)
		}
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 11
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM forward_config").Scan(&n); err != nil || n != 0 {
			t.Fatalf("forward_config after migration 11: %d rows, %v — it must exist and be empty", n, err)
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v10 data did not survive migration 11: %q %v", region, err)
		}
	})

	t.Run("forward_config holds one row: a second id violates CHECK (id = 1)", func(t *testing.T) {
		// negative control: drop `CHECK (id = 1)` from migration 11 → the id = 2 INSERT succeeds and this fails
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.DB.Exec("INSERT INTO forward_config (id, url, token, established_at, last_ok_at) VALUES (1, 'https://api.b', 't', 1, 1)"); err != nil {
			t.Fatal(err)
		}
		_, err = s.DB.Exec("INSERT INTO forward_config (id, url, token, established_at, last_ok_at) VALUES (2, 'https://api.b', 't', 1, 1)")
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("a second forward_config row: %v, want a CHECK constraint failure", err)
		}
	})

	t.Run("migration 12 adds EMPTY scaler_config and scaler_presets tables and touches nothing else", func(t *testing.T) {
		// negative control: delete either CREATE TABLE from migration 12's string, leaving the entry (so
		// user_version still reaches 12 and the failure is about the table, not the version) → its
		// SELECT COUNT(*) errors with "no such table" and this fails.
		//
		// An empty scaler_config means the scaler is off, so every host that migrates through this (the
		// checked-in host fixture included) serves exactly as before.
		dir := t.TempDir()
		saved := Migrations
		Migrations = saved[:11]
		s, err := Open(dir) // a real v11 database, made by the shipped migrations
		Migrations = saved
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('scaler_config', 'scaler_presets')").Scan(&n); err != nil || n != 0 {
			t.Fatalf("v11 already has a scaler table (%d, %v)", n, err)
		}
		if _, err := s.DB.Exec("INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES ('REGION', 'eu', 0, 1, 1)"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s, err = Open(dir) // reopening runs migration 12
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for _, tbl := range []string{"scaler_config", "scaler_presets"} {
			if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s after migration 12: %d rows, %v — it must exist and be empty", tbl, n, err)
			}
		}
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(Migrations) {
			t.Fatalf("user_version %d, want %d", v, len(Migrations))
		}
		var region string
		if err := s.DB.QueryRow("SELECT value FROM host_vars WHERE name='REGION'").Scan(&region); err != nil || region != "eu" {
			t.Fatalf("the v11 data did not survive migration 12: %q %v", region, err)
		}
	})

	t.Run("scaler_config holds one row: a second id violates CHECK (id = 1)", func(t *testing.T) {
		// negative control: drop `CHECK (id = 1)` from migration 12 → the id = 2 INSERT succeeds and this fails
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.DB.Exec("INSERT INTO scaler_config (id) VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		_, err = s.DB.Exec("INSERT INTO scaler_config (id) VALUES (2)")
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("a second scaler_config row: %v, want a CHECK constraint failure", err)
		}
	})

	t.Run("loki_config holds one row: a second id violates CHECK (id = 1)", func(t *testing.T) {
		// negative control: drop `CHECK (id = 1)` from migration 9 → the id = 2 INSERT succeeds and this fails
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		// id 1 with previous_* NULL is the row at rest: the same column list, accepted.
		if _, err := s.DB.Exec("INSERT INTO loki_config (id, config, secret, updated_at) VALUES (1, '{}', '', 1)"); err != nil {
			t.Fatal(err)
		}
		_, err = s.DB.Exec("INSERT INTO loki_config (id, config, secret, updated_at) VALUES (2, '{}', '', 1)")
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("a second loki_config row: %v, want a CHECK constraint failure", err)
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

func TestForwardRoundTrip(t *testing.T) {
	// negative control: drop `last_ok_at = ?` from TouchForwardOK's UPDATE → LastOkAt stays 1 and
	// this fails; make SetForward insert without ON CONFLICT → the second Set errors.
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if f, err := GetForward(s.DB); err != nil || f != nil {
		t.Fatalf("GetForward on an empty table: %+v, %v — want nil, nil", f, err)
	}
	want := Forward{To: "https://api.b", Via: "10.0.0.2", Token: "t", EstablishedAt: 1, LastOkAt: 1}
	if err := SetForward(s.DB, want); err != nil {
		t.Fatal(err)
	}
	if f, err := GetForward(s.DB); err != nil || f == nil || *f != want {
		t.Fatalf("GetForward after Set: %+v, %v — want %+v", f, err, want)
	}
	// A second Set replaces the one row.
	want.To = "https://api.c"
	if err := SetForward(s.DB, want); err != nil {
		t.Fatal(err)
	}
	if err := TouchForwardOK(s.DB, 5); err != nil {
		t.Fatal(err)
	}
	want.LastOkAt = 5
	if f, err := GetForward(s.DB); err != nil || f == nil || *f != want {
		t.Fatalf("GetForward after Touch: %+v, %v — want %+v", f, err, want)
	}
	if err := DeleteForward(s.DB); err != nil {
		t.Fatal(err)
	}
	if f, err := GetForward(s.DB); err != nil || f != nil {
		t.Fatalf("GetForward after Delete: %+v, %v — want nil, nil", f, err)
	}
}

func TestScalerConfigRoundTrip(t *testing.T) {
	// negative control: add `report = excluded.report` to SetScalerSettings' ON CONFLICT → the report
	// set before it is wiped and this fails; change the SQL DEFAULT for max to 4 → the row
	// SetScalerReport creates reads back Max 4, not the Go default 3.
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defaults := ScalerConfig{Vars: map[string]string{}, Max: 3, CooldownMin: 5, GraceMin: 15, Credentials: map[string]string{}}
	c, err := GetScalerConfig(s.DB)
	if err != nil || c == nil || !reflect.DeepEqual(*c, defaults) || c.Vars == nil || c.Credentials == nil {
		t.Fatalf("GetScalerConfig on an empty table: %+v, %v — want %+v with non-nil maps", c, err, defaults)
	}

	// A report alone creates the row from the SQL DEFAULTs; they must equal the Go defaults.
	if err := SetScalerReport(s.DB, `{"x":1}`); err != nil {
		t.Fatal(err)
	}
	want := defaults
	want.Report = `{"x":1}`
	if c, err := GetScalerConfig(s.DB); err != nil || !reflect.DeepEqual(*c, want) {
		t.Fatalf("GetScalerConfig after a report on an empty table: %+v, %v — want %+v", c, err, want)
	}

	// Settings leave the report unchanged.
	want = ScalerConfig{Preset: "hetzner", Vars: map[string]string{"location": "fsn1"}, Min: 1, Max: 5, CooldownMin: 2,
		GraceMin: 9, Credentials: map[string]string{"HCLOUD_TOKEN": "tok"}, Report: `{"x":1}`}
	in := want
	in.Report = "ignored"
	if err := SetScalerSettings(s.DB, in); err != nil {
		t.Fatal(err)
	}
	if c, err := GetScalerConfig(s.DB); err != nil || !reflect.DeepEqual(*c, want) {
		t.Fatalf("GetScalerConfig after SetScalerSettings: %+v, %v — want %+v", c, err, want)
	}

	// A report leaves the settings unchanged.
	if err := SetScalerReport(s.DB, `{"y":2}`); err != nil {
		t.Fatal(err)
	}
	want.Report = `{"y":2}`
	if c, err := GetScalerConfig(s.DB); err != nil || !reflect.DeepEqual(*c, want) {
		t.Fatalf("GetScalerConfig after SetScalerReport: %+v, %v — want %+v", c, err, want)
	}

	// Nil maps are stored as {} and read back non-nil.
	if err := SetScalerSettings(s.DB, ScalerConfig{Max: 3}); err != nil {
		t.Fatal(err)
	}
	if c, err := GetScalerConfig(s.DB); err != nil || c.Vars == nil || c.Credentials == nil {
		t.Fatalf("GetScalerConfig after nil maps: %+v, %v — want non-nil maps", c, err)
	}
}

func TestScalerPresetRoundTrip(t *testing.T) {
	// negative control: drop `ORDER BY name` from ListScalerPresets → rows come back in insert order
	// (b, a) and this fails; return true unconditionally from DeleteScalerPreset → the second Delete
	// reports true.
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := ScalerPreset{Name: "b", Files: map[string]string{"main.tf": "b"}, UpdatedAt: 2}
	a := ScalerPreset{Name: "a", Files: map[string]string{"main.tf": "a", "vars.tf": "v"}, UpdatedAt: 1}
	for _, p := range []ScalerPreset{b, a} {
		if err := PutScalerPreset(s.DB, p); err != nil {
			t.Fatal(err)
		}
	}
	// A second Put replaces the row.
	b.Files["main.tf"], b.UpdatedAt = "b2", 3
	if err := PutScalerPreset(s.DB, b); err != nil {
		t.Fatal(err)
	}
	if l, err := ListScalerPresets(s.DB); err != nil || !reflect.DeepEqual(l, []ScalerPreset{a, b}) {
		t.Fatalf("ListScalerPresets: %+v, %v — want [a b]", l, err)
	}
	if p, err := GetScalerPreset(s.DB, "a"); err != nil || p == nil || !reflect.DeepEqual(*p, a) {
		t.Fatalf("GetScalerPreset(a): %+v, %v — want %+v", p, err, a)
	}
	if p, err := GetScalerPreset(s.DB, "absent"); err != nil || p != nil {
		t.Fatalf("GetScalerPreset(absent): %+v, %v — want nil, nil", p, err)
	}
	if ok, err := DeleteScalerPreset(s.DB, "a"); err != nil || !ok {
		t.Fatalf("first DeleteScalerPreset: %v, %v — want true", ok, err)
	}
	if ok, err := DeleteScalerPreset(s.DB, "a"); err != nil || ok {
		t.Fatalf("second DeleteScalerPreset: %v, %v — want false", ok, err)
	}
}

func TestScalerConfigSet(t *testing.T) {
	// negative control: drop the scaler_presets EXISTS from ScalerConfigSet → a db holding only a
	// preset reports false and this fails.
	for _, tc := range []struct {
		name string
		set  func(q Querier) error
		want bool
	}{
		{"empty", func(Querier) error { return nil }, false},
		{"a scaler_config row", func(q Querier) error { return SetScalerReport(q, "") }, true},
		{"a preset only", func(q Querier) error { return PutScalerPreset(q, ScalerPreset{Name: "p", UpdatedAt: 1}) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := tc.set(s.DB); err != nil {
				t.Fatal(err)
			}
			if got, err := ScalerConfigSet(s.DB); err != nil || got != tc.want {
				t.Fatalf("ScalerConfigSet: %v, %v — want %v", got, err, tc.want)
			}
		})
	}
}
