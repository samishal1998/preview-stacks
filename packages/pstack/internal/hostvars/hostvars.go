// Package hostvars is host-level variables and secrets — the GitHub-Actions model, scoped to one
// host.
//
// A VARIABLE is configuration everyone may read: a region, a base image tag, a feature flag. A
// SECRET is a credential: its value goes IN through the API and never comes back OUT — List
// returns names and dates only, and the value is scrubbed from job and compose logs by content.
//
// Specs reference them EXPLICITLY: `${vars.REGION}`, `${secrets.DB_PASSWORD}`. The namespace is the
// point — a plain `${REGION}` keeps meaning "whatever the request or the deployment supplied", and
// a spec that reads host state says so on its face.
//
// ONE KIND CHANGE IS REFUSED: secret → variable. Flipping the flag would turn a write-only value
// into a readable one with no re-entry of the value — an information-flow downgrade wearing an
// UPDATE's clothes. Going the other way (variable → secret) only tightens, so it is allowed.
package hostvars

import (
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Error is a refused name, value or kind change. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool { _, ok := err.(*Error); return ok }

// Same shape spec interpolation accepts — anything else could never be referenced anyway.
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Row is what a caller may see. Value is present for variables and ALWAYS null for secrets — the
// pointer without omitempty mirrors the API's guarantee.
type Row struct {
	Name      string  `json:"name"`
	Value     *string `json:"value"`
	Secret    bool    `json:"secret"`
	UpdatedAt int64   `json:"updatedAt"`
}

// HostVars is the table. Owner: the server; every method is one statement or one Tx.
type HostVars struct{ Store *store.Store }

// New wraps a store.
func New(s *store.Store) *HostVars { return &HostVars{Store: s} }

// List returns variable values and secret NAMES, by name.
func (h *HostVars) List() ([]Row, error) {
	rows, err := h.Store.DB.Query("SELECT name, value, secret, updated_at FROM host_vars ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		var value string
		var secret int
		if err := rows.Scan(&r.Name, &value, &secret, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Secret = secret == 1
		if !r.Secret {
			v := value
			r.Value = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolveMaps is the full maps, for spec RESOLUTION only. Named to be conspicuous in a diff, like
// `SecretOf` in webhooks — a second caller outside the resolve path is the moment to ask why.
func (h *HostVars) ResolveMaps() (vars, secrets map[string]string, err error) {
	rows, err := h.Store.DB.Query("SELECT name, value, secret FROM host_vars")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	vars, secrets = map[string]string{}, map[string]string{}
	for rows.Next() {
		var name, value string
		var secret int
		if err := rows.Scan(&name, &value, &secret); err != nil {
			return nil, nil, err
		}
		if secret == 1 {
			secrets[name] = value
		} else {
			vars[name] = value
		}
	}
	return vars, secrets, rows.Err()
}

// SecretValues is every secret VALUE, for log scrubbing. Values, not names — logs leak by content.
func (h *HostVars) SecretValues() ([]string, error) {
	rows, err := h.Store.DB.Query("SELECT value FROM host_vars WHERE secret = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Put upserts. created reports whether the name was new.
func (h *HostVars) Put(name, value string, secret bool) (created bool, err error) {
	if !nameRe.MatchString(name) {
		ns := "vars"
		if secret {
			ns = "secrets"
		}
		return false, &Error{`"` + name + `" is not a usable name — letters, digits and _ only, not starting with a digit ` +
			`(it has to be referenceable as ${` + ns + `.NAME} in a spec)`}
	}
	if value == "" {
		return false, &Error{"an empty value would make every spec referencing it fail to resolve — delete the entry instead"}
	}
	err = h.Store.Tx(func(q store.Querier) error {
		var existing int
		switch e := q.QueryRow("SELECT secret FROM host_vars WHERE name = ?", name).Scan(&existing); {
		case e == nil:
			if existing == 1 && !secret {
				return &Error{`"` + name + `" is a secret. Making it a readable variable would reveal a value that was ` +
					`stored write-only — delete it and create a variable with the value re-entered instead.`}
			}
		case errors.Is(e, sql.ErrNoRows):
			created = true
		default:
			return e
		}
		now := time.Now().UnixMilli()
		_, e := q.Exec(`INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(name) DO UPDATE SET value = excluded.value, secret = excluded.secret,
         updated_at = excluded.updated_at`, name, value, b2i(secret), now, now)
		return e
	})
	return created, err
}

// Remove deletes. false when nothing had that name.
func (h *HostVars) Remove(name string) (bool, error) {
	res, err := h.Store.DB.Exec("DELETE FROM host_vars WHERE name = ?", name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
