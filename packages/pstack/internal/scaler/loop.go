package scaler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The loop's numbers (spec, The loop and Failures).
const (
	Tick        = 30 * time.Second
	BootTimeout = 10 * time.Minute
	LostAfter   = 10 * time.Minute
	BackoffMax  = 30 * time.Minute
)

// Options is what a Loop needs. Now and Log default to time.Now and io.Discard.
type Options struct {
	API            Client
	StateDir, Tofu string
	Now            func() time.Time
	Log            io.Writer
}

// The parts of GET /api/signals the loop reads.
type sigNode struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Role         string `json:"role"`
	State        string `json:"state"`
	Availability string `json:"availability"`
	Tasks        int    `json:"tasks"`
	EmptySince   *int64 `json:"emptySince"`
}

type sigStuck struct {
	Task    string `json:"task"`
	Service string `json:"service"`
	Stack   string `json:"stack"`
	Reason  string `json:"reason"`
}

type sigView struct {
	Swarm     bool       `json:"swarm"`
	Reachable bool       `json:"reachable"`
	Nodes     []sigNode  `json:"nodes"`
	Stuck     []sigStuck `json:"stuck"`
}

// Event payloads, in spec field order (W-R3).
type failedData struct {
	Action  string `json:"action"`
	Error   string `json:"error"`
	Attempt int    `json:"attempt"`
}

type createdData struct {
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings"`
}

// Loop is the scaler. One goroutine calls Tick; nothing here is shared.
type Loop struct {
	o Options
	s *State

	// The last good read; kept across a failed one so the report still names node states.
	cfg   Config
	sig   sigView
	mgrIP netip.Addr

	// This tick's working set.
	dir      string
	tofu     Tofu
	preset   Preset          // parsed from the files in dir, the ones Tofu runs
	declared map[string]bool // variables those files declare
	action   string
	note     string
	failed   bool
	logged   string
}

// New loads <StateDir>/scaler.json (creating it with a fresh host id).
func New(o Options) (*Loop, error) {
	if o.StateDir == "" || o.Tofu == "" {
		return nil, errors.New("scaler: StateDir and Tofu are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	s, err := LoadState(o.StateDir)
	if err != nil {
		return nil, err
	}
	return &Loop{o: o, s: s}, nil
}

var clauseRe = regexp.MustCompile(`^(insufficient resources on \d+ nodes?|\d+ nodes? not available for new tasks)$`)

// Qualifies says whether a stuck task's reason is one a new worker can fix: `no suitable node (…)`
// with at least one clause, every clause short resources or a drained node.
func Qualifies(reason string) bool {
	rest, ok := strings.CutPrefix(reason, "no suitable node (")
	if !ok || !strings.HasSuffix(rest, ")") {
		return false
	}
	for _, c := range strings.Split(strings.TrimSuffix(rest, ")"), "; ") {
		if !clauseRe.MatchString(c) {
			return false
		}
	}
	return true
}

// Tick is one pass of the spec's steps 1–7, at most one apply. The error is a local failure
// (scaler.json unwritable) or a report pstack refused; everything else is in the report.
func (l *Loop) Tick() error {
	now := l.o.Now().UnixMilli()
	var elapsed int64
	if l.s.SeenAt > 0 && now > l.s.SeenAt {
		elapsed = now - l.s.SeenAt
	}
	l.s.SeenAt = now
	l.action, l.note, l.failed = "", "", false

	live, err := l.step(now)
	if err != nil {
		return err
	}
	if !live {
		l.freeze(elapsed)
	} else if !l.failed && l.s.NextTryAt <= now {
		l.s.LastError = ""
	}
	if len(l.s.Workers) == 0 {
		l.s.Locked = nil
	}
	if err := l.save(); err != nil {
		return err
	}
	l.log()
	return l.flush(now)
}

// step runs the tick's work. false: nothing could act this tick, so no clock advances.
func (l *Loop) step(now int64) (bool, error) {
	if !l.read() || !l.prepare() {
		return false, nil
	}
	l.settings()
	l.observe(now)
	// Task 7: finish removals (step 2), lost after LostAfter not ready (step 3), scale down (5–6).
	return true, l.scaleUp(now)
}

// read is step 1: config, signals, swarm.
func (l *Loop) read() bool {
	var cfg Config
	if err := l.o.API.Do("GET", "/api/scaler/config", nil, &cfg); err != nil {
		l.action = "config: " + err.Error()
		return false
	}
	l.cfg = cfg
	var sig sigView
	if err := l.o.API.Do("GET", "/api/signals", nil, &sig); err != nil {
		l.action = "signals: " + err.Error()
		return false
	}
	switch {
	case !sig.Reachable:
		l.action = "docker did not answer"
		return false
	case !sig.Swarm:
		l.action = "not a swarm manager"
		return false
	}
	l.sig = sig
	var sw struct {
		ManagerAddr *string `json:"managerAddr"`
	}
	if err := l.o.API.Do("GET", "/api/swarm", nil, &sw); err != nil {
		l.action = "swarm: " + err.Error()
		return false
	}
	if sw.ManagerAddr == nil {
		l.action = "manager address unknown"
		return false
	}
	host, _, err := net.SplitHostPort(*sw.ManagerAddr)
	if err != nil {
		host = *sw.ManagerAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		// The built-in firewalls write manager_address/32 (W-R37).
		l.fail(fmt.Sprintf("manager advertises %s: the scaler needs an IPv4 advertise address", host))
		return false
	}
	l.mgrIP = ip
	return true
}

// prepare checks the pulled config against the lock, keeps the preset directory in step with it
// and parses the files Tofu will run.
func (l *Loop) prepare() bool {
	cfg := l.cfg
	if cfg.Preset == "" {
		l.action = "no preset set"
		return false
	}
	if lk := l.s.Locked; lk != nil && lk.Preset != cfg.Preset {
		l.fail(fmt.Sprintf("locked to %s: preset is %s", lk.Preset, cfg.Preset))
		return false
	}
	want, err := ParsePreset(cfg.Files)
	if err != nil {
		l.fail("preset " + cfg.Preset + ": " + err.Error())
		return false
	}
	l.dir = filepath.Join(l.o.StateDir, "tofu", cfg.Preset)
	l.tofu = Tofu{Bin: l.o.Tofu, Dir: l.dir, StateDir: l.o.StateDir, Creds: cfg.Credentials}
	if l.s.Inited[cfg.Preset] != want.Hash {
		if len(l.s.Workers) > 0 {
			// W-R35: owned workers keep the files they were made with.
			l.note = "preset files changed; applied after workers reach zero"
		} else if !l.initPreset(want.Hash) {
			return false
		}
	}
	files, err := readPresetDir(l.dir)
	if err == nil {
		l.preset, err = ParsePreset(files)
	}
	if err != nil {
		l.fail("preset " + cfg.Preset + ": " + err.Error())
		return false
	}
	l.declared = map[string]bool{}
	for name, body := range files {
		if name != lockFileName {
			for _, m := range variableRe.FindAllStringSubmatch(body, -1) {
				l.declared[m[1]] = true
			}
		}
	}
	if lk := l.s.Locked; lk != nil {
		for _, k := range slices.Sorted(maps.Keys(lk.Shared)) {
			if cfg.Vars[k] != lk.Shared[k] {
				l.fail(fmt.Sprintf("locked to %s: %s differs", lk.Preset, k))
				return false
			}
		}
	}
	if l.mgrIP.IsPrivate() {
		for _, k := range []string{"network_id", "vpc_uuid"} {
			if slices.Contains(l.preset.Shared, k) && cfg.Vars[k] == "" {
				l.fail(fmt.Sprintf("manager advertises the private address %s: set %s", l.mgrIP, k))
				return false
			}
		}
	}
	return true
}

// initPreset writes the pulled files into the preset directory, then init and validate. A
// failure leaves Inited as it was, so the next tick tries again; it takes no backoff.
func (l *Loop) initPreset(hash string) bool {
	if err := syncPresetDir(l.dir, l.cfg.Files); err != nil {
		l.fail("preset " + l.cfg.Preset + ": " + err.Error())
		return false
	}
	for _, args := range [][]string{{"init", "-input=false", "-no-color"}, {"validate", "-no-color"}} {
		if _, err := l.tofu.Run(args...); err != nil {
			l.fail(args[0] + ": " + err.Error())
			return false
		}
	}
	l.s.Inited[l.cfg.Preset] = hash
	return true
}

func isPresetFile(name string) bool { return name == lockFileName || fileNameRe.MatchString(name) }

func syncPresetDir(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, keep := files[e.Name()]; !keep && e.Type().IsRegular() && isPresetFile(e.Name()) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func readPresetDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() && isPresetFile(e.Name()) {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			files[e.Name()] = string(b)
		}
	}
	return files, nil
}

// current is the per-worker settings a new worker gets now; unset ones are left to the preset.
func (l *Loop) current() map[string]string {
	vars := map[string]string{}
	for _, k := range l.preset.PerWorker {
		if v := l.cfg.Vars[k]; v != "" {
			vars[k] = v
		}
	}
	return vars
}

// settings resets the never-joined count and does-not-fit marks when the preset or a per-worker
// variable changed.
func (l *Loop) settings() {
	b, _ := json.Marshal(l.current())
	h := l.cfg.Preset + "\x00" + string(b)
	if h != l.s.SettingsHash {
		l.s.SettingsHash, l.s.NeverJoined, l.s.DoesNotFit = h, 0, nil
	}
}

// node is the swarm node of an owned worker: hostname = name, role worker only.
func (l *Loop) node(name string) (sigNode, bool) {
	for _, n := range l.sig.Nodes {
		if n.Hostname == name && n.Role == "worker" {
			return n, true
		}
	}
	return sigNode{}, false
}

func (l *Loop) stuckServices(keep func(sigStuck) bool) []string {
	var out []string
	for _, st := range l.sig.Stuck {
		if keep(st) && !slices.Contains(out, st.Service) {
			out = append(out, st.Service)
		}
	}
	return out
}

// observe records workers becoming ready, and marks never-joined and does-not-fit.
func (l *Loop) observe(now int64) {
	stuck := l.stuckServices(func(sigStuck) bool { return true })
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if w.Removing {
			continue
		}
		n, ok := l.node(w.Name)
		if ok {
			w.NodeID = n.ID
		}
		if ok && n.State == "ready" && w.ReadyAt == 0 {
			w.ReadyAt = now
			l.s.NeverJoined = 0 // "in a row"
		}
		switch {
		case !ok || n.State != "ready" || n.Availability != "active":
			w.ReadySince = 0
		case w.ReadySince == 0:
			w.ReadySince = now
		}
		if w.AppliedAt > 0 && w.ReadyAt == 0 && now-w.AppliedAt >= BootTimeout.Milliseconds() {
			w.Removing, w.Reason = true, "never-joined"
			l.s.NeverJoined++
			l.backoff(now)
			l.queueFailed("never-joined", fmt.Sprintf("%s not ready after %d min", w.Name, int(BootTimeout.Minutes())), l.s.NeverJoined)
			continue
		}
		still := slices.DeleteFunc(slices.Clone(w.StuckServices), func(s string) bool { return !slices.Contains(stuck, s) })
		if len(still) == 0 {
			w.StuckServices = nil
			continue
		}
		// Swarm's empty clock runs while the node is down too, so the window starts at the later
		// of empty and ready: a full tick that swarm could have placed the services and did not.
		if w.ReadySince > 0 && n.Tasks == 0 && n.EmptySince != nil && now-max(*n.EmptySince, w.ReadySince) >= Tick.Milliseconds() {
			for _, s := range still {
				if !slices.Contains(l.s.DoesNotFit, s) {
					l.s.DoesNotFit = append(l.s.DoesNotFit, s)
				}
			}
			w.StuckServices = nil
			l.queueFailed("does-not-fit", strings.Join(still, ", ")+" does not fit a new worker", 1)
		}
	}
}

// scaleUp is step 4: retry a create whose apply failed, or create a worker when every guard holds.
func (l *Loop) scaleUp(now int64) error {
	owned := 0
	var pending, booting *OwnedWorker
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if w.Removing {
			continue
		}
		owned++
		switch {
		case w.AppliedAt == 0:
			pending = w
		case w.ReadyAt == 0:
			booting = w
		}
	}
	// A failed create is retried as it was: desired state is the workers map (P-R1).
	if pending != nil {
		if now < l.s.NextTryAt {
			l.action = "backoff: retry " + pending.Name
			return nil
		}
		return l.applyNew(now, pending.Name)
	}

	cfg := l.cfg
	want := l.stuckServices(func(st sigStuck) bool {
		return Qualifies(st.Reason) && !slices.Contains(l.s.DoesNotFit, st.Service)
	})
	if len(want) == 0 {
		l.s.MaxReported = false
	}
	cur := l.current()
	var readyEmpty *OwnedWorker
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if n, ok := l.node(w.Name); ok && !w.Removing && n.State == "ready" && n.Tasks == 0 && maps.Equal(w.Vars, cur) {
			readyEmpty = w
			break
		}
	}
	if owned >= cfg.Min {
		switch {
		case len(want) == 0:
			if dnf := l.stuckServices(func(st sigStuck) bool { return Qualifies(st.Reason) }); len(dnf) > 0 {
				l.action = "does not fit: " + strings.Join(dnf, ", ")
			} else {
				l.action = "idle"
			}
			return nil
		case readyEmpty != nil:
			l.action = readyEmpty.Name + " is ready and empty"
			return nil
		}
	}
	if owned >= cfg.Max {
		l.action = fmt.Sprintf("max %d reached", cfg.Max)
		if len(want) > 0 && !l.s.MaxReported {
			l.s.MaxReported = true
			l.queueFailed("max", l.action, 1)
		}
		return nil
	}
	l.s.MaxReported = false
	switch {
	case l.s.LastScaleUp > 0 && now-l.s.LastScaleUp < int64(cfg.CooldownMin)*time.Minute.Milliseconds():
		l.action = "cooldown"
	case booting != nil:
		l.action = booting.Name + " is booting"
	case l.s.NeverJoined >= 2:
		l.action = fmt.Sprintf("%d workers never joined: change the settings to retry", l.s.NeverJoined)
	case now < l.s.NextTryAt:
		l.action = "backoff"
	default:
		return l.create(now, want)
	}
	return nil
}

// create records a new worker, reports its name (so the API's lock holds), then applies.
func (l *Loop) create(now int64, services []string) error {
	name := l.newName()
	distro := l.preset.Distro
	if distro == "" {
		distro = "ubuntu"
	}
	var join string
	if err := l.o.API.Do("GET", "/api/swarm/join?format=cloud-config&distro="+url.QueryEscape(distro), nil, &join); err != nil {
		l.action = "join: " + err.Error()
		return nil
	}
	userData, err := WithHostname(join, name)
	if err != nil {
		l.fail("join: " + err.Error())
		return nil
	}
	l.s.Workers = append(l.s.Workers, OwnedWorker{
		Name: name, Vars: l.current(), UserData: userData, CreatedAt: now, StuckServices: services,
	})
	l.s.LastScaleUp = now
	if l.s.Locked == nil {
		shared := map[string]string{}
		for _, k := range l.preset.Shared {
			shared[k] = l.cfg.Vars[k] // every declared one, empty included (W-R36)
		}
		l.s.Locked = &Locked{Preset: l.cfg.Preset, FilesHash: l.s.Inited[l.cfg.Preset], Shared: shared}
	}
	if err := l.save(); err != nil {
		return err
	}
	return l.applyNew(now, name)
}

// applyNew reports the owned names before an apply that creates name, so the API's lock holds
// first; a refused report leaves it unapplied, to retry next tick.
func (l *Loop) applyNew(now int64, name string) error {
	l.action = "creating " + name
	if err := l.post(now, nil); err != nil {
		return err
	}
	return l.apply(now, "created "+name)
}

// newName is pstack-<6 hex>, unlike any owned worker or swarm node.
//
// ponytail: 24 random bits checked against what is visible now, not every name ever used; a
// ledger of past names if a reuse is ever seen.
func (l *Loop) newName() string {
	for {
		b := make([]byte, 3)
		rand.Read(b)
		name := "pstack-" + hex.EncodeToString(b)
		taken := slices.ContainsFunc(l.s.Workers, func(w OwnedWorker) bool { return w.Name == name }) ||
			slices.ContainsFunc(l.sig.Nodes, func(n sigNode) bool { return n.Hostname == name })
		if !taken {
			return name
		}
	}
}

// apply refreshes, marks applied workers missing from state as lost (step 3), and applies the
// workers map: every owned worker not removing.
func (l *Loop) apply(now int64, did string) error {
	if err := l.writeVars(); err != nil {
		return err
	}
	if _, err := l.tofu.Run("apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color"); err != nil {
		l.applyFailed(now, "refresh", err)
		return nil
	}
	list, err := l.tofu.Run("state", "list", "-no-color")
	if err != nil {
		l.applyFailed(now, "refresh", err)
		return nil
	}
	var in []*OwnedWorker
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if w.Removing {
			continue
		}
		if w.AppliedAt > 0 && !strings.Contains(list, `["`+w.Name+`"]`) { // P-R11
			w.Removing, w.Reason = true, "lost"
			continue
		}
		w.ApplyingAt = now
		in = append(in, w)
	}
	if err := l.save(); err != nil {
		return err
	}
	if err := l.writeVars(); err != nil {
		return err
	}
	_, err = l.tofu.Run("apply", "-auto-approve", "-input=false", "-no-color")
	for _, w := range in {
		w.ApplyingAt = 0
	}
	if err != nil {
		l.applyFailed(now, "apply", err)
		return nil
	}
	for _, w := range in {
		if w.AppliedAt == 0 {
			w.AppliedAt = now
			l.queue("scaler.created", createdData{Name: w.Name, Settings: nonNilVars(w.Vars)})
		}
	}
	l.s.Failures, l.s.NextTryAt, l.s.LastError = 0, 0, ""
	l.action = did
	return l.save()
}

func (l *Loop) applyFailed(now int64, action string, err error) {
	l.backoff(now)
	l.fail(err.Error())
	l.action = action + " failed"
	l.queueFailed(action, l.scrub(err.Error()), l.s.Failures)
}

// backoff counts a failure: the next try in 1, 2, 4 … 30 min.
func (l *Loop) backoff(now int64) {
	l.s.Failures++
	d := BackoffMax
	if l.s.Failures <= 6 {
		d = min(time.Minute<<(l.s.Failures-1), BackoffMax)
	}
	l.s.NextTryAt = now + d.Milliseconds()
}

// writeVars writes the workers map and the shared and filled variables the preset declares.
// Empty values are left out, so the preset's default applies.
func (l *Loop) writeVars() error {
	workers := map[string]any{}
	for _, w := range l.s.Workers {
		if w.Removing {
			continue
		}
		entry := map[string]any{"user_data": w.UserData}
		for k, v := range w.Vars {
			if v != "" {
				entry[k] = tfValue(v)
			}
		}
		workers[w.Name] = entry
	}
	vars := map[string]any{"workers": workers}
	for _, k := range l.preset.Shared {
		if v := l.cfg.Vars[k]; v != "" {
			vars[k] = tfValue(v)
		}
	}
	if l.declared["manager_address"] {
		vars["manager_address"] = l.mgrIP.String()
	}
	if l.declared["host_id"] {
		vars["host_id"] = l.s.HostID
	}
	return WriteTFVars(l.dir, vars)
}

// tfValue is P-R6: a value starting with `[` or `{` reaches Tofu as JSON.
func tfValue(v string) any {
	if (v[0] == '[' || v[0] == '{') && json.Valid([]byte(v)) {
		return json.RawMessage(v)
	}
	return v
}

// freeze shifts the loop's clocks by d: a tick that could not act does not count toward cooldown,
// backoff or the boot timeout.
func (l *Loop) freeze(d int64) {
	if d == 0 {
		return
	}
	if l.s.LastScaleUp > 0 {
		l.s.LastScaleUp += d
	}
	if l.s.NextTryAt > 0 {
		l.s.NextTryAt += d
	}
	for i := range l.s.Workers {
		if l.s.Workers[i].AppliedAt > 0 {
			l.s.Workers[i].AppliedAt += d
		}
	}
}

func (l *Loop) fail(msg string) {
	l.failed = true
	l.s.LastError = msg
	if l.action == "" {
		l.action = "no action"
	}
}

func (l *Loop) scrub(s string) string { return Scrub(s, l.cfg.Credentials) }

func (l *Loop) queueFailed(action, msg string, attempt int) {
	l.queue("scaler.failed", failedData{Action: action, Error: l.scrub(msg), Attempt: attempt})
}

func (l *Loop) queue(name string, data any) {
	b, _ := json.Marshal(data)
	l.s.Events = append(l.s.Events, Event{Name: name, Data: b})
}

func (l *Loop) save() error { return l.s.Save(l.o.StateDir) }

// log writes the action and error when they changed since the last line.
func (l *Loop) log() {
	line := l.scrub(l.lastAction())
	if e := l.s.LastError; e != "" {
		line += "\nerror: " + l.scrub(e)
	}
	if line != l.logged {
		l.logged = line
		fmt.Fprintf(l.o.Log, "scaler: %s\n", line)
	}
}

func (l *Loop) lastAction() string {
	if l.note != "" {
		return l.action + "; " + l.note
	}
	return l.action
}

// flush posts the queued events, one report each, in order (W-R4); with none, one plain report.
// An event leaves the queue only once pstack took it.
func (l *Loop) flush(now int64) error {
	if len(l.s.Events) == 0 {
		return l.post(now, nil)
	}
	for len(l.s.Events) > 0 {
		ev := l.s.Events[0]
		if err := l.post(now, &ev); err != nil {
			return err
		}
		l.s.Events = l.s.Events[1:]
		if err := l.save(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) post(now int64, ev *Event) error {
	return l.o.API.Do("POST", "/api/scaler/report", l.report(now, ev), nil)
}

// report is the status as pstack shows it: never user_data, lists never null.
func (l *Loop) report(now int64, ev *Event) Report {
	r := Report{
		At: now, HostID: l.s.HostID, Workers: []Worker{}, Locked: l.s.Locked,
		LastAction: l.scrub(l.lastAction()), LastError: l.scrub(l.s.LastError),
		DoesNotFit: append([]string{}, l.s.DoesNotFit...), Orphans: append([]string{}, l.s.Orphans...),
		PendingDestroy: l.s.PendingDestroy, Event: ev,
	}
	if l.s.NextTryAt > now {
		r.NextRetryAt = l.s.NextTryAt
	}
	for _, w := range l.s.Workers {
		r.Workers = append(r.Workers, Worker{Name: w.Name, Vars: nonNilVars(w.Vars), State: l.workerState(w), CreatedAt: w.CreatedAt, Removing: w.Removing})
	}
	return r
}

func (l *Loop) workerState(w OwnedWorker) string {
	n, ok := l.node(w.Name)
	switch {
	case w.Removing:
		return "removing"
	case ok && n.State == "ready":
		return "ready"
	case ok:
		return "down"
	case w.ReadyAt > 0:
		return "lost"
	}
	return "booting"
}

func nonNilVars(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}
