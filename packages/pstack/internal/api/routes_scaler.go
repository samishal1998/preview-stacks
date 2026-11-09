// `/api/scaler` — the worker scaler's settings, its Tofu presets, and the two root-only routes the
// scaler itself calls (internal/scaler talks to pstack over HTTP only). Maintainer reads; admin
// writes, for blast radius: a save decides what machines this host pays for (permissions.go).
//
// Credentials go in and never come back out, except through GET /api/scaler/config: root only,
// gated twice like GET /api/config (routes_config.go's header), because the scaler needs them.
//
// The scaler is the authority on what it owns (/state/scaler.json). Each report's ownership and lock
// record is mirrored in scaler_config.report so the 409s below hold across a pstack restart; the rest
// of a report (worker state, last error, orphans …) lives in memory only (W-R2).
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scaler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// scalerEvents are the only events a report may ask pstack to emit.
var scalerEvents = []string{"scaler.created", "scaler.removed", "scaler.failed"}

// credKeyRe: a credential reaches Tofu as an environment variable.
var credKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// scalerRecord is the persisted half of a report: ownership and the lock, nothing else.
type scalerRecord struct {
	HostID         string               `json:"hostId"`
	Workers        []scalerRecordWorker `json:"workers"`
	Locked         *scaler.Locked       `json:"locked"`
	PendingDestroy string               `json:"pendingDestroy"`
}

type scalerRecordWorker struct {
	Name      string            `json:"name"`
	Vars      map[string]string `json:"vars"`
	CreatedAt int64             `json:"createdAt"`
	Removing  bool              `json:"removing"`
}

// scalerView is GET /api/scaler.
type scalerView struct {
	Settings    scaler.Settings `json:"settings"`
	Credentials map[string]bool `json:"credentials"`
	Report      *scaler.Report  `json:"report"`
	Enabled     *bool           `json:"enabled"`
}

// scalerPresetView is one preset; Files only on the single-preset routes.
type scalerPresetView struct {
	Name    string            `json:"name"`
	Builtin bool              `json:"builtin"`
	Files   map[string]string `json:"files,omitempty"`
	Preset  scaler.Preset     `json:"preset"`
	Locked  bool              `json:"locked"`
}

// controlServiceOn is nil when docker did not answer, else whether the control stack runs service.
func controlServiceOn(view inspect.ControlView, service string) *bool {
	if !view.Reachable {
		return nil
	}
	on := slices.ContainsFunc(view.Containers, func(c inspect.ControlContainer) bool {
		return c.Service != nil && *c.Service == service
	})
	return &on
}

// scalerLastReport is the last report: memory when this process has one, else the stored record
// (nil before the first). Callers hold scalerMu.
func (s *Server) scalerLastReport() (*scaler.Report, error) {
	if s.scalerLast != nil {
		return s.scalerLast, nil
	}
	row, err := store.GetScalerConfig(s.store.DB)
	if err != nil || row.Report == "" {
		return nil, err
	}
	var rep scaler.Report
	if err := json.Unmarshal([]byte(row.Report), &rep); err != nil {
		return nil, err
	}
	return normalizeReport(&rep), nil
}

// scalerLock is the locked config while the scaler owns any worker; nil otherwise.
func scalerLock(rep *scaler.Report) *scaler.Locked {
	if rep == nil || len(rep.Workers) == 0 {
		return nil
	}
	return rep.Locked
}

// normalizeReport writes the lists as [] and the maps as {}, never null.
func normalizeReport(r *scaler.Report) *scaler.Report {
	if r.Workers == nil {
		r.Workers = []scaler.Worker{}
	}
	for i := range r.Workers {
		if r.Workers[i].Vars == nil {
			r.Workers[i].Vars = map[string]string{}
		}
	}
	if r.DoesNotFit == nil {
		r.DoesNotFit = []string{}
	}
	if r.Orphans == nil {
		r.Orphans = []string{}
	}
	r.Event = nil
	return r
}

// scalerPresetFiles is a preset's files, built-in or custom; nil when there is no such preset.
func (s *Server) scalerPresetFiles(name string) (files map[string]string, builtin bool, err error) {
	if f, ok := scaler.Builtins()[name]; ok {
		return f, true, nil
	}
	p, err := store.GetScalerPreset(s.store.DB, name)
	if err != nil || p == nil {
		return nil, false, err
	}
	return p.Files, false, nil
}

// scalerSettings is the stored settings plus the in-memory dismissed list. Callers hold scalerMu.
func (s *Server) scalerSettings() (scaler.Settings, *store.ScalerConfig, error) {
	row, err := store.GetScalerConfig(s.store.DB)
	if err != nil {
		return scaler.Settings{}, nil, err
	}
	dismissed := s.scalerDismissed
	if dismissed == nil {
		dismissed = []string{}
	}
	return scaler.Settings{Preset: row.Preset, Vars: row.Vars, Min: row.Min, Max: row.Max,
		CooldownMin: row.CooldownMin, GraceMin: row.GraceMin, Dismissed: dismissed}, row, nil
}

func (s *Server) scalerGet(w http.ResponseWriter) error {
	// The docker call first, outside scalerMu.
	enabled := controlServiceOn(inspect.ControlRuntime(s.host), "scaler")
	s.scalerMu.Lock()
	defer s.scalerMu.Unlock()
	set, row, err := s.scalerSettings()
	if err != nil {
		return err
	}
	rep, err := s.scalerLastReport()
	if err != nil {
		return err
	}
	creds := map[string]bool{}
	for k := range row.Credentials {
		creds[k] = true
	}
	writeJSON(w, 200, scalerView{Settings: set, Credentials: creds, Report: rep, Enabled: enabled})
	return nil
}

func (s *Server) scalerPut(w http.ResponseWriter, r *http.Request) error {
	// Absent fields are the defaults, never the stored values: a PUT replaces the settings (W-R16).
	body := struct {
		scaler.Settings
		Credentials map[string]*string `json:"credentials"`
	}{Settings: scaler.Settings{Max: 3, CooldownMin: 5, GraceMin: 15}}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeError(w, 400, "body must be the scaler settings: { preset, vars, min, max, cooldownMin, graceMin, dismissed, credentials }")
		return nil
	}
	set := body.Settings
	if set.Vars == nil {
		set.Vars = map[string]string{}
	}
	if set.Dismissed == nil {
		set.Dismissed = []string{}
	}
	if set.Preset == "" {
		writeError(w, 400, "preset required")
		return nil
	}
	if set.Min < 0 || set.Min > set.Max {
		writeError(w, 400, "want 0 ≤ min ≤ max")
		return nil
	}
	if set.CooldownMin < 0 || set.GraceMin < 0 {
		writeError(w, 400, "cooldownMin and graceMin must be ≥ 0")
		return nil
	}
	for k := range body.Credentials {
		if !credKeyRe.MatchString(k) {
			writeError(w, 400, fmt.Sprintf("credential %q: want an environment variable name", k))
			return nil
		}
	}
	files, _, err := s.scalerPresetFiles(set.Preset)
	if err != nil {
		return err
	}
	if files == nil {
		writeError(w, 400, fmt.Sprintf("unknown preset %q", set.Preset))
		return nil
	}
	preset, err := scaler.ParsePreset(files)
	if err != nil {
		return err
	}
	for k := range set.Vars {
		if !slices.Contains(preset.PerWorker, k) && !slices.Contains(preset.Shared, k) {
			writeError(w, 400, fmt.Sprintf("preset %s has no variable %q", set.Preset, k))
			return nil
		}
	}

	// The docker call before scalerMu, never under it.
	enabled := controlServiceOn(inspect.ControlRuntime(s.host), "scaler")
	s.scalerMu.Lock()
	defer s.scalerMu.Unlock()
	rep, err := s.scalerLastReport()
	if err != nil {
		return err
	}
	for _, name := range set.Dismissed {
		if rep == nil || !slices.Contains(rep.Orphans, name) {
			writeError(w, 400, fmt.Sprintf("%s is not a possible orphan", name))
			return nil
		}
	}
	if lock := scalerLock(rep); lock != nil {
		if set.Preset != lock.Preset {
			writeError(w, 409, "the scaler owns workers on preset "+lock.Preset+": set max 0, wait for zero, then switch")
			return nil
		}
		for k, v := range lock.Shared {
			if set.Vars[k] != v {
				writeError(w, 409, fmt.Sprintf("%s is locked while the scaler owns workers", k))
				return nil
			}
		}
	}
	row, err := store.GetScalerConfig(s.store.DB)
	if err != nil {
		return err
	}
	creds := row.Credentials
	for k, v := range body.Credentials {
		switch {
		case v == nil:
			// P-R5: the API cannot know which keys the old preset still needs.
			if _, ok := creds[k]; ok && rep != nil && (len(rep.Workers) > 0 || rep.PendingDestroy != "") {
				writeError(w, 409, fmt.Sprintf("%s is kept while the scaler owns workers or a destroy is pending", k))
				return nil
			}
			delete(creds, k)
		case *v == "" || *v == secretMask:
			// keep
		default:
			creds[k] = *v
		}
	}
	err = store.SetScalerSettings(s.store.DB, store.ScalerConfig{Preset: set.Preset, Vars: set.Vars, Min: set.Min,
		Max: set.Max, CooldownMin: set.CooldownMin, GraceMin: set.GraceMin, Credentials: creds})
	if err != nil {
		return err
	}
	s.scalerDismissed = set.Dismissed
	keys := map[string]bool{}
	for k := range creds {
		keys[k] = true
	}
	writeJSON(w, 200, scalerView{Settings: set, Credentials: keys, Report: rep, Enabled: enabled})
	return nil
}

func (s *Server) scalerPresetList(w http.ResponseWriter) error {
	s.scalerMu.Lock()
	rep, err := s.scalerLastReport()
	s.scalerMu.Unlock()
	if err != nil {
		return err
	}
	all := map[string]bool{}
	files := map[string]map[string]string{}
	for name, f := range scaler.Builtins() {
		all[name], files[name] = true, f
	}
	custom, err := store.ListScalerPresets(s.store.DB)
	if err != nil {
		return err
	}
	for _, p := range custom {
		all[p.Name], files[p.Name] = false, p.Files
	}
	out := []scalerPresetView{}
	for name, builtin := range all {
		v, err := scalerPresetOf(name, builtin, files[name], rep)
		if err != nil {
			return err
		}
		v.Files = nil
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
	return nil
}

func scalerPresetOf(name string, builtin bool, files map[string]string, rep *scaler.Report) (scalerPresetView, error) {
	p, err := scaler.ParsePreset(files)
	if err != nil {
		return scalerPresetView{}, err
	}
	lock := scalerLock(rep)
	return scalerPresetView{Name: name, Builtin: builtin, Files: files, Preset: p, Locked: lock != nil && lock.Preset == name}, nil
}

func (s *Server) scalerPresetGet(w http.ResponseWriter, name string) error {
	s.scalerMu.Lock()
	rep, err := s.scalerLastReport()
	s.scalerMu.Unlock()
	if err != nil {
		return err
	}
	files, builtin, err := s.scalerPresetFiles(name)
	if err != nil {
		return err
	}
	if files == nil {
		writeError(w, 404, "no preset "+name)
		return nil
	}
	v, err := scalerPresetOf(name, builtin, files, rep)
	if err != nil {
		return err
	}
	writeJSON(w, 200, v)
	return nil
}

func (s *Server) scalerPresetPut(w http.ResponseWriter, r *http.Request, name string) error {
	if err := scaler.ValidPresetName(name); err != nil {
		return err
	}
	var body struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil || len(body.Files) == 0 {
		writeError(w, 400, "body must be { files: { \"main.tf\": \"…\" } }")
		return nil
	}
	p, err := scaler.ParsePreset(body.Files)
	if err != nil {
		return err
	}
	s.scalerMu.Lock()
	defer s.scalerMu.Unlock()
	rep, err := s.scalerLastReport()
	if err != nil {
		return err
	}
	// Restoring the locked files is accepted (spec: a change that restores the lock).
	if lock := scalerLock(rep); lock != nil && lock.Preset == name && lock.FilesHash != p.Hash {
		writeError(w, 409, "preset "+name+" is locked while the scaler owns workers")
		return nil
	}
	if err := store.PutScalerPreset(s.store.DB, store.ScalerPreset{Name: name, Files: body.Files, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		return err
	}
	v, err := scalerPresetOf(name, false, body.Files, rep)
	if err != nil {
		return err
	}
	writeJSON(w, 200, v)
	return nil
}

func (s *Server) scalerPresetDelete(w http.ResponseWriter, name string) error {
	if err := scaler.ValidPresetName(name); err != nil {
		return err
	}
	s.scalerMu.Lock()
	defer s.scalerMu.Unlock()
	rep, err := s.scalerLastReport()
	if err != nil {
		return err
	}
	if lock := scalerLock(rep); lock != nil && lock.Preset == name {
		writeError(w, 409, "preset "+name+" is locked while the scaler owns workers")
		return nil
	}
	deleted, err := store.DeleteScalerPreset(s.store.DB, name)
	if err != nil {
		return err
	}
	if !deleted {
		writeError(w, 404, "no preset "+name)
		return nil
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
	return nil
}

// scalerConfigGet is everything the scaler needs, credential values included. Root only, checked
// here a second time (routes_config.go's mayMoveConfig) before anything is read.
func (s *Server) scalerConfigGet(w http.ResponseWriter, who *auth.Principal) error {
	if !mayMoveConfig(who) {
		writeError(w, 403, "the scaler's config carries its cloud credentials: the PSTACK_TOKEN bearer only")
		return nil
	}
	s.scalerMu.Lock()
	set, row, err := s.scalerSettings()
	s.scalerMu.Unlock()
	if err != nil {
		return err
	}
	files := map[string]string{}
	if set.Preset != "" {
		f, _, err := s.scalerPresetFiles(set.Preset)
		if err != nil {
			return err
		}
		if f != nil {
			files = f
		}
	}
	writeJSON(w, 200, scaler.Config{Settings: set, Credentials: row.Credentials, Files: files}, configNoStore)
	return nil
}

// scalerReportPost takes the scaler's status. The ownership and lock record is stored, the rest kept
// in memory; the one optional event is emitted as sent (its Data is never re-encoded, W-R3).
func (s *Server) scalerReportPost(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	if !mayMoveConfig(who) {
		writeError(w, 403, "only the scaler reports: the PSTACK_TOKEN bearer only")
		return nil
	}
	var rep scaler.Report
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&rep); err != nil {
		writeError(w, 400, "body must be a scaler report")
		return nil
	}
	ev := rep.Event
	if ev != nil {
		if !slices.Contains(scalerEvents, ev.Name) {
			writeError(w, 400, fmt.Sprintf("event %q: want one of scaler.created, scaler.removed, scaler.failed", ev.Name))
			return nil
		}
		if !bytes.HasPrefix(bytes.TrimSpace(ev.Data), []byte("{")) {
			writeError(w, 400, "event data must be an object")
			return nil
		}
	}
	normalizeReport(&rep)
	rec := scalerRecord{HostID: rep.HostID, Workers: []scalerRecordWorker{}, Locked: rep.Locked, PendingDestroy: rep.PendingDestroy}
	for _, wk := range rep.Workers {
		rec.Workers = append(rec.Workers, scalerRecordWorker{Name: wk.Name, Vars: wk.Vars, CreatedAt: wk.CreatedAt, Removing: wk.Removing})
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	s.scalerMu.Lock()
	err = store.SetScalerReport(s.store.DB, string(b))
	if err == nil {
		s.scalerLast = &rep
		// W-R18: a dismissal lasts until the scaler stops listing the name.
		kept := []string{}
		for _, name := range s.scalerDismissed {
			if slices.Contains(rep.Orphans, name) {
				kept = append(kept, name)
			}
		}
		s.scalerDismissed = kept
	}
	s.scalerMu.Unlock()
	if err != nil {
		return err
	}
	if ev != nil {
		s.bus.Emit(ev.Name, ev.Data)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
