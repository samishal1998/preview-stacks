// Package store is the persistent state that is not the registry: users, sessions, API tokens,
// webhook registrations and delivery logs, the terminal audit, host variables, SSO.
//
// ── WHY SQLITE, AND WHY THE REGISTRY STAYS FILES ─────────────────────────────────────────────────
//
// SQLite needs no new container, no credential, no migration tooling — one file under DATA_DIR, so
// "back up the host" remains "copy the directory". The deployment registry does NOT move here: it is
// a deliberate cache-of-intent (a directory of YAML an operator can read and repair over SSH), and
// nothing about accounts changes that. SQLite is only for the domains that are *relational and
// secret-bearing* — exactly what a directory of YAML is bad at.
//
// Open takes a directory, not a file, because WAL mode creates `-wal`/`-shm` siblings — permissions
// have to be right on the *directory* (0700), not just the db file.
//
// ── LOCATION ─────────────────────────────────────────────────────────────────────────────────────
//
// `<dataDir>/db/pstack.db`. NOT `<dataDir>/pstack.db`: the control container mounts only chosen
// subdirectories of DATA_DIR (`deployments/`, and `db/`) — a file at the top level would live in
// the container's own filesystem and silently vanish on every `docker compose up -d` that recreates
// it. `pstack init` creates the directory 0700.
//
// ── MIGRATIONS ───────────────────────────────────────────────────────────────────────────────────
//
// `PRAGMA user_version` + an ordered list. Each entry runs once, in a transaction, and the version
// is bumped after it. Editing a shipped migration is forbidden (it will not re-run anywhere it
// already ran) — append a new one. This is deliberately the whole framework: a table of two-line
// DDL strings does not need a dependency.
//
// ── ONE CONNECTION (rule 16) ─────────────────────────────────────────────────────────────────────
//
// The pool is capped at ONE connection, which is what makes the port behave like the single
// synchronous bun:sqlite handle the TypeScript had: statements from concurrent requests serialise
// instead of interleaving. The consequences are strict. Inside Tx only the *sql.Tx is used — a
// nested `DB.*` call waits for the one connection the transaction holds, forever. And a *sql.Rows
// must be read fully and closed before the next statement, for the same reason.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // the driver; pure Go, no cgo
)

// Querier is the statement surface both *sql.DB and *sql.Tx satisfy. Code that must run inside
// and outside a transaction takes one of these.
type Querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// Store is the open database.
type Store struct {
	DB *sql.DB
	// Dir is `<dataDir>/db`.
	Dir string
}

// Open creates the directory (0700) and the database (0600), enables WAL and foreign keys, and
// applies every pending migration.
func Open(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "db")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	// Explicit, past the umask: this directory holds password hashes and token hashes, and WAL puts
	// live data in sibling files the file-level chmod below cannot cover.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "pstack.db")
	// WAL: readers do not block the writer. The API handles requests concurrently and every one of
	// them may touch a session row. foreign_keys is load-bearing (ON DELETE CASCADE everywhere).
	// busy_timeout and an IMMEDIATE BEGIN are what a second process (the CLI's bootstrap opens its
	// own handle next to the server's) needs to wait rather than fail.
	dsn := "file:" + file + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Ping creates the file, so the chmod has something to act on.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(file, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db, Dir: dir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var current int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	for v := current + 1; v <= len(Migrations); v++ {
		err := s.Tx(func(q Querier) error {
			if _, err := q.Exec(Migrations[v-1]); err != nil {
				return err
			}
			_, err := q.Exec(fmt.Sprintf("PRAGMA user_version = %d", v))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", v, err)
		}
	}
	return nil
}

// Tx runs fn in a transaction (BEGIN IMMEDIATE — it is a write lock from the first statement) and
// commits unless fn errors or panics. fn MUST use only the Querier it is handed: with one pooled
// connection, a `store.DB.*` call from inside fn waits for the connection fn's own transaction
// holds — a permanent self-deadlock, not a slow query.
func (s *Store) Tx(fn func(q Querier) error) (err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }
