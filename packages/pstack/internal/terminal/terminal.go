// Package terminal is a shell inside a preview container, over a WebSocket.
//
// ── THIS IS THE MOST DANGEROUS ROUTE IN THE PRODUCT ──────────────────────────────────────────────
//
// `docker exec` accepts ANY container name the daemon knows — Traefik, another PR's stack, and the
// pstack control container itself, whose filesystem holds pstack.db. So the name is never trusted:
// the handler asks the deployment what containers it actually owns and matches the request against
// THAT list; anything not in it is a 404. Quoting is not the defence — ExecArgv is an argv, there
// is no shell to quote for.
//
// ── NO PTY, DELIBERATELY ─────────────────────────────────────────────────────────────────────────
//
// `docker exec -i` covers what an operator opens a terminal for: ls, cat, env, psql, a migration.
// No job control, no curses UIs, no readline, no ^C, no prompt. The UI says so out loud.
//
// ponytail: no pty. Upgrade path is `script -qec` (or a pty binding) VERIFIED ON A REAL HOST.
package terminal

import (
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Shells worth offering. An argv element, never a shell string — but an allowlist is cheap and it
// keeps a typo from becoming a confusing "exec format error" from the daemon.
var Shells = []string{"sh", "bash", "ash", "zsh", "fish"}

// IsShell reports whether v names an offered shell.
func IsShell(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, sh := range Shells {
		if sh == s {
			return true
		}
	}
	return false
}

// ExecArgv is the command that opens the shell — an argv ARRAY, exec'd directly with no shell in
// between, so a container id cannot be re-split or expanded no matter what it holds.
func ExecArgv(containerID, shell string) []string {
	return []string{"docker", "exec", "-i", containerID, shell}
}

// ActorOf is who did it, in one string, for the audit row.
func ActorOf(who auth.Principal) string {
	switch who.Kind {
	case auth.KindRoot:
		return "root (PSTACK_TOKEN)"
	case auth.KindShare:
		return "share-link (" + who.Deployment + ")"
	}
	return who.User.Username
}

// MayOpenTerminal: may this principal open a terminal? Everyone is role admin today, so this does
// nothing yet — which is exactly why it goes in now. A share link is read-only by construction.
func MayOpenTerminal(who auth.Principal) bool {
	if who.Kind == auth.KindShare {
		return false
	}
	return who.Kind == auth.KindRoot || (who.User != nil && who.User.Role == "admin")
}

// Session is one audit row.
type Session struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
	Shell      string `json:"shell"`
	StartedAt  int64  `json:"startedAt"`
	EndedAt    *int64 `json:"endedAt"`
}

// Audit is the terminal_sessions table.
type Audit struct{ Store *store.Store }

// NewAudit wraps a store.
func NewAudit(s *store.Store) *Audit { return &Audit{Store: s} }

// OpenArgs describe a session at open.
type OpenArgs struct {
	Actor, Deployment, Container, ContainerID, Shell string
}

// Open writes the row at OPEN, not at close: a session that ends because the process died or
// pstack itself was killed must still have left a trace.
func (a *Audit) Open(o OpenArgs) (int64, error) {
	var id int64
	err := a.Store.DB.QueryRow(
		"INSERT INTO terminal_sessions (actor, deployment, container, container_id, shell, started_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id",
		o.Actor, o.Deployment, o.Container, o.ContainerID, o.Shell, time.Now().UnixMilli()).Scan(&id)
	return id, err
}

// Close stamps ended_at once.
func (a *Audit) Close(id int64) error {
	_, err := a.Store.DB.Exec("UPDATE terminal_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL", time.Now().UnixMilli(), id)
	return err
}

// Recent lists the newest first; limit clamped to 1..500.
func (a *Audit) Recent(limit int64) ([]Session, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := a.Store.DB.Query("SELECT id, actor, deployment, container, shell, started_at, ended_at FROM terminal_sessions ORDER BY started_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Actor, &s.Deployment, &s.Container, &s.Shell, &s.StartedAt, &s.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
