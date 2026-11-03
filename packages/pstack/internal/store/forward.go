package store

import (
	"database/sql"
	"errors"
)

// Forward is the one forward_config row: where this host passes deploys and visitors (B), and the
// token on B it presents. Token is never returned by a read route.
type Forward struct {
	To, Via, Token string
	// LastOkAt is informational only (last contact with B); nothing decides on it.
	EstablishedAt, LastOkAt int64
}

// GetForward reads the setting; nil, nil when unset.
func GetForward(q Querier) (*Forward, error) {
	var f Forward
	err := q.QueryRow("SELECT url, via, token, established_at, last_ok_at FROM forward_config WHERE id = 1").
		Scan(&f.To, &f.Via, &f.Token, &f.EstablishedAt, &f.LastOkAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// SetForward writes the one row, replacing any previous setting.
func SetForward(q Querier, f Forward) error {
	_, err := q.Exec(`INSERT INTO forward_config (id, url, via, token, established_at, last_ok_at)
       VALUES (1, ?, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET url = excluded.url, via = excluded.via, token = excluded.token,
         established_at = excluded.established_at, last_ok_at = excluded.last_ok_at`,
		f.To, f.Via, f.Token, f.EstablishedAt, f.LastOkAt)
	return err
}

// DeleteForward clears the setting; a no-op when unset.
func DeleteForward(q Querier) error {
	_, err := q.Exec("DELETE FROM forward_config")
	return err
}

// TouchForwardOK records the last successful contact with B; a no-op when unset.
func TouchForwardOK(q Querier, at int64) error {
	_, err := q.Exec("UPDATE forward_config SET last_ok_at = ? WHERE id = 1", at)
	return err
}
