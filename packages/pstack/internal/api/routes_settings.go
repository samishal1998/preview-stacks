// The two host settings an operator may change at RUNTIME: `GET /api/settings` and
// `PUT /api/settings/<key>`. internal/settings owns the keys, the validation and the precedence;
// this file is the HTTP surface and the two things that happen the moment a value changes.
//
// ── THE READ SAYS WHERE THE VALUE CAME FROM ─────────────────────────────────────────────────────
//
// Every row carries `source`: "db" (someone set it here), "env" (PSTACK_MAX_JOBS) or "default"
// (what the binary ships with). Without it a box saying 4 is unexplainable — an operator who typed
// 8 into it and still sees 4 has no way to tell a rejected write from a stale page from a value
// their environment is overriding, and "why did my change not stick" is unanswerable.
//
// The SOURCE is computed here, per key, mirroring internal/settings' own predicate for "is this
// stored row usable" — that package resolves a VALUE and exposes no Source(key). It is a small
// duplication and it is deliberate; the alternative (comparing the resolved value with the stored
// text) lies about a hand-edited "008", which Atoi accepts and string equality does not.
//
// ── A WRITE APPLIES NOW, AND LOWERING THE CAP CANCELS NOTHING ───────────────────────────────────
//
// PUT max_jobs calls jobs.Registry.SetMaxRunning, so the new cap is in force for the next dispatch
// without restarting the container — the whole point of the feature. Jobs already RUNNING run to
// completion, so the response says so in words: an operator who types 1 while four jobs run must
// not read the 200 as "three were just cancelled".
//
// ── THE PERMISSION IS PER KEY, AND LIVES IN permissions.go ──────────────────────────────────────
//
// max_jobs is operational (MAINTAINER, with host configuration); default_role is user management by
// another name (ADMIN, with the promotion paths). The two rows are exact paths, so a key nobody
// listed is root's by the table's default-deny — and the chain 404s it, because this file
// dispatches on the two literals only. `minRole` in the read is taken FROM that table rather than
// written down again here: a second copy is the drift permissions.go's header exists to prevent.
package api

import (
	"net/http"
	"strconv"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
)

// storedSetting is the raw row for one key, or "" when there is none (and when the read failed,
// which resolves the same way settings' own reader does).
func (s *Server) storedSetting(key string) string {
	var v string
	if err := s.store.DB.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// settingRow is one setting as the read returns it: the resolved value, where it came from, and the
// least role that may change it.
func (s *Server) settingRow(key string) jsonx.Object {
	source := "default"
	var value any
	switch key {
	case settings.KeyMaxJobs:
		value = s.settings.MaxJobs()
		if n, err := strconv.Atoi(s.storedSetting(key)); err == nil && n >= 1 {
			source = "db"
		} else if s.opts.MaxJobs >= 1 {
			source = "env"
		}
	case settings.KeyDefaultRole:
		value = s.settings.DefaultRole()
		if auth.ValidRole(s.storedSetting(key)) {
			source = "db"
		}
	}
	// Straight from the gate's own table, so this cannot say maintainer while the route refuses
	// anyone below admin.
	p, _ := requiredRole(http.MethodPut, "/api/settings/"+key)
	return jsonx.O("key", key, "value", value, "source", source, "minRole", string(p.min))
}

// getSettings is GET /api/settings: both keys, resolved, with their source.
func (s *Server) getSettings(w http.ResponseWriter) error {
	// The environment's contribution, named once: it is what `source: "env"` refers to. NULL, not
	// 0, when nothing usable was set — `PSTACK_MAX_JOBS=0` is read as unset (TuningFromEnv's `??`
	// semantics), so a literal 0 here could not be told apart from "never set" and would contradict
	// the row's own `source: "default"`.
	var envMaxJobs any
	if s.opts.MaxJobs >= 1 {
		envMaxJobs = s.opts.MaxJobs
	}
	writeJSON(w, 200, jsonx.O(
		"settings", []jsonx.Object{s.settingRow(settings.KeyMaxJobs), s.settingRow(settings.KeyDefaultRole)},
		"env", jsonx.O("PSTACK_MAX_JOBS", envMaxJobs),
		"precedence", "database > environment > built-in default",
	))
	return nil
}

// putSetting is PUT /api/settings/<key>, for the two keys the chain dispatches. The value is
// validated by internal/settings — an unknown key or a bad value is its *Error, which fail() maps
// to 400.
func (s *Server) putSetting(w http.ResponseWriter, r *http.Request, key string) error {
	if r.Method != http.MethodPut {
		writeError(w, 405, "use PUT")
		return nil
	}
	// writeMu, like every other check-then-act in this package. `Set` is a single upsert, so the
	// TABLE is safe on its own — the pair below is not: two concurrent PUTs of max_jobs could store
	// one value and leave the registry running at the other, which is the silent drift between the
	// stored cap and the live one this feature exists to end. Human-rate route; the cost is nothing.
	//
	// NOT deferred, and that is the point. `SetMaxRunning` pumps, `pump` emits `job.started`, and
	// `events.Emit` calls its listeners SYNCHRONOUSLY and in order (never `go fn(e)`) — so the
	// notify dispatcher, and a SQLite read inside it, would run under this mutex. Raising the cap
	// from 1 to 50 would do up to fifty of those reads holding the server's global write lock.
	// Rule 14 is about exactly this, and every other site in this package already avoids it:
	// routes_deploy.go registers `defer release()` BEFORE `defer s.writeMu.Unlock()` so LIFO frees
	// the mutex first. Here there is no second defer to order against, so the unlock is explicit
	// and every path below returns after it.
	s.writeMu.Lock()
	body := bodyObject(r)
	if body == nil {
		s.writeMu.Unlock()
		writeError(w, 400, "body must be { value }")
		return nil
	}
	raw, present := body.Get("value")
	if !present {
		s.writeMu.Unlock()
		writeError(w, 400, "body must be { value }")
		return nil
	}
	// js.ToString is rule 7: a JSON number arrives as float64 and `8.0` must store as "8". Anything
	// that is not a string or a number becomes "" and is refused by the validator, which names the
	// key it refused.
	if err := s.settings.Set(key, js.ToString(raw)); err != nil {
		s.writeMu.Unlock()
		return err
	}
	out := append(s.settingRow(key), jsonx.KV{K: "stored", V: true})
	resolved := s.settings.MaxJobs()
	s.writeMu.Unlock() // the store is consistent from here; what follows reaches the bus.
	if key == settings.KeyMaxJobs {
		// WITHOUT A RESTART. The registry pumps after raising, so a job that has been waiting for a
		// slot starts on this request.
		s.jobs.SetMaxRunning(resolved)
		out = append(out, jsonx.KV{K: "note", V: "In force now — no restart. Jobs already running " +
			"run to completion, so lowering the cap cancelled nothing; it applies to the next job " +
			"that starts."})
	}
	writeJSON(w, 200, out)
	return nil
}
