package sso

import (
	"database/sql"
	"sync"
	"time"
)

// ── the transient store ───────────────────────────────────────────────────────────────────────────

// TransientStore is where the PKCE verifier waits out the round trip. Nothing else in this feature
// is transient, and nothing that lands here outlives five minutes.
//
// The interface exists so going multi-instance is a config change rather than a rewrite: Redis (its
// own TTL) or Postgres (an `expires_at` column) slot in behind it unchanged. SQLite is what this
// service already wires up, so it is what ships — and it is honest about the ceiling: it does not
// survive going multi-instance, which is the same ceiling everything else in this process has.
type TransientStore interface {
	Set(key, value string, ttlSeconds int64) error
	// Get is (value, true) or ("", false) if absent OR expired — a caller never has to check a timestamp.
	Get(key string) (string, bool, error)
	Delete(key string) error
	// Take reads and deletes in ONE statement. Single-use state is what stops a replayed callback,
	// and a get-then-delete pair has a window where two requests both read the same row.
	Take(key string) (string, bool, error)
}

// DB is structurally what the store's handle gives us — typed here so this package imports nothing
// of the store. *sql.DB and *sql.Tx both satisfy it.
type DB interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// SqliteTransientStore keeps the rows in sso_state.
type SqliteTransientStore struct{ DB DB }

// Set writes. Sweeps on write rather than on a timer: this table only grows when someone starts a
// login, so the write path is exactly where the garbage appears.
func (s *SqliteTransientStore) Set(key, value string, ttlSeconds int64) error {
	now := time.Now().UnixMilli()
	if _, err := s.DB.Exec("DELETE FROM sso_state WHERE expires_at <= ?", now); err != nil {
		return err
	}
	_, err := s.DB.Exec("INSERT OR REPLACE INTO sso_state (key, value, expires_at) VALUES (?, ?, ?)", key, value, now+ttlSeconds*1000)
	return err
}

// Get reads an unexpired row.
func (s *SqliteTransientStore) Get(key string) (string, bool, error) {
	return one(s.DB.QueryRow("SELECT value FROM sso_state WHERE key = ? AND expires_at > ?", key, time.Now().UnixMilli()))
}

// Delete removes a row.
func (s *SqliteTransientStore) Delete(key string) error {
	_, err := s.DB.Exec("DELETE FROM sso_state WHERE key = ?", key)
	return err
}

// Take is the single-statement read-and-delete.
func (s *SqliteTransientStore) Take(key string) (string, bool, error) {
	return one(s.DB.QueryRow("DELETE FROM sso_state WHERE key = ? AND expires_at > ? RETURNING value", key, time.Now().UnixMilli()))
}

func one(row *sql.Row) (string, bool, error) {
	var v string
	switch err := row.Scan(&v); err {
	case nil:
		return v, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, err
	}
}

// MemoryTransientStore is the fallback the interface promises, and what the store suite runs against
// a second time. The mutex is what the TypeScript's single thread gave it for free: Take really is
// atomic here, where the JS version was a get-then-delete that only the event loop kept safe.
type MemoryTransientStore struct {
	mu   sync.Mutex
	rows map[string]memRow
}

type memRow struct {
	value     string
	expiresAt int64
}

// NewMemoryTransientStore returns an empty store.
func NewMemoryTransientStore() *MemoryTransientStore {
	return &MemoryTransientStore{rows: map[string]memRow{}}
}

// Set writes, sweeping expired rows first.
func (m *MemoryTransientStore) Set(key, value string, ttlSeconds int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UnixMilli()
	for k, r := range m.rows {
		if r.expiresAt <= now {
			delete(m.rows, k)
		}
	}
	m.rows[key] = memRow{value: value, expiresAt: now + ttlSeconds*1000}
	return nil
}

// Get reads an unexpired row (and drops an expired one on the way).
func (m *MemoryTransientStore) Get(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(key)
}

func (m *MemoryTransientStore) get(key string) (string, bool, error) {
	r, ok := m.rows[key]
	if !ok {
		return "", false, nil
	}
	if r.expiresAt <= time.Now().UnixMilli() {
		delete(m.rows, key)
		return "", false, nil
	}
	return r.value, true, nil
}

// Delete removes a row.
func (m *MemoryTransientStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, key)
	return nil
}

// Take reads and deletes under one lock.
func (m *MemoryTransientStore) Take(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok, err := m.get(key)
	delete(m.rows, key)
	return v, ok, err
}
