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
	"strconv"
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

type removedData struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
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
	tofuRan  bool // an init or apply ran this tick: no old-preset destroy (W-R19)
	checked  bool // the orphan check ran in this process
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

var (
	clauseRe     = regexp.MustCompile(`^(insufficient resources on \d+ nodes?|\d+ nodes? not available for new tasks)$`)
	constraintRe = regexp.MustCompile(`^scheduling constraints not satisfied on (\d+) nodes?$`)
)

// Qualifies says whether a stuck task's reason is one a new worker can fix: `no suitable node (…)`
// with at least one clause, every clause short resources, a drained node, or constraints that only
// managers fail (`--previews-on workers` labels managers off). A user's own constraint that a manager
// also fails qualifies too; the worker it buys marks the service does-not-fit, so it costs one.
func Qualifies(reason string, managers int) bool {
	rest, ok := strings.CutPrefix(reason, "no suitable node (")
	if !ok || !strings.HasSuffix(rest, ")") {
		return false
	}
	for _, c := range strings.Split(strings.TrimSuffix(rest, ")"), "; ") {
		if m := constraintRe.FindStringSubmatch(c); m != nil {
			if n, err := strconv.Atoi(m[1]); err != nil || n > managers {
				return false
			}
			continue
		}
		if !clauseRe.MatchString(c) {
			return false
		}
	}
	return true
}

// managers counts the manager nodes in this tick's signals.
func (l *Loop) managers() int {
	n := 0
	for _, nd := range l.sig.Nodes {
		if nd.Role == "manager" {
			n++
		}
	}
	return n
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
	l.action, l.note, l.failed, l.tofuRan = "", "", false, false

	live, err := l.step(now)
	if err != nil {
		return err
	}
	if !live {
		l.freeze(elapsed)
	} else if !l.failed && l.s.NextTryAt <= now && l.s.DestroyNextTryAt <= now {
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
	l.s.Orphans = slices.DeleteFunc(l.s.Orphans, func(n string) bool { return slices.Contains(l.cfg.Dismissed, n) })
	if !l.checked && !l.orphans() {
		return false, nil
	}
	l.settings()
	l.observe(now)
	acted, err := l.removals(now)
	if err == nil && !acted {
		err = l.scaleUp(now)
	}
	if err == nil {
		l.scaleDown(now)
	}
	if err == nil && !acted {
		err = l.destroyOld(now)
	}
	return true, err
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
	if l.s.PendingDestroy == cfg.Preset {
		// Switched back before the destroy: its shared resources are wanted again, and destroyed
		// on the next switch away. A preset used in between takes its place in the queue.
		l.s.PendingDestroy, l.s.LastPreset = l.s.LastPreset, cfg.Preset
		// Another preset now waits for its destroy: it starts without the old one's backoff.
		l.s.DestroyFailures, l.s.DestroyNextTryAt = 0, 0
	}
	l.promote()
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

// promote queues a switched-from preset's destroy (destroyOld) once nothing else runs. One waits
// at a time: a later switch leaves LastPreset set, and create refuses until it moves.
func (l *Loop) promote() {
	if len(l.s.Workers) == 0 && l.s.PendingDestroy == "" && l.s.LastPreset != "" && l.s.LastPreset != l.cfg.Preset {
		l.s.PendingDestroy, l.s.LastPreset = l.s.LastPreset, ""
	}
}

// initPreset writes the pulled files into the preset directory, then init and validate. A
// failure leaves Inited as it was, so the next tick tries again; it takes no backoff.
func (l *Loop) initPreset(hash string) bool {
	if err := syncPresetDir(l.dir, l.cfg.Files); err != nil {
		l.fail("preset " + l.cfg.Preset + ": " + err.Error())
		return false
	}
	l.tofuRan = true
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
	cur := l.current()
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if w.Removing {
			continue
		}
		n, ok := l.node(w.Name)
		if ok {
			w.NodeID = n.ID
		}
		if w.AppliedAt == 0 && !ok {
			// A create never applied has no server to replace: it takes the settings as they are now,
			// before removals' apply could write it into the map. Not once a node joined under it.
			w.Vars = cur
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
		// Step 3: a worker that was ready and has not been for LostAfter is lost.
		switch {
		case w.ReadyAt == 0 || ok && n.State == "ready":
			w.NotReadySince = 0
		case w.NotReadySince == 0:
			w.NotReadySince = now
		case now-w.NotReadySince >= LostAfter.Milliseconds():
			w.Removing, w.Reason = true, "lost"
			continue
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
	// Step 6 for a create never applied: it has no server, so above max it goes now, before
	// removals' apply would write it into the map and buy it.
	over := -l.cfg.Max
	for _, w := range l.s.Workers {
		if !w.Removing {
			over++
		}
	}
	for i := range l.s.Workers {
		if w := &l.s.Workers[i]; over > 0 && !w.Removing && w.AppliedAt == 0 {
			w.Removing, w.Reason = true, "max"
			over--
		}
	}
}

// orphans is the check after an unclean stop, on the first tick that can run Tofu (W-R1): a worker
// whose create apply was cut off (ApplyingAt still set, never applied) and that Tofu's state does
// not hold may exist in the provider unrecorded. It becomes removing, so it is never created twice.
func (l *Loop) orphans() bool {
	var cut []*OwnedWorker
	for i := range l.s.Workers {
		if w := &l.s.Workers[i]; !w.Removing && w.AppliedAt == 0 && w.ApplyingAt != 0 {
			cut = append(cut, w)
		}
	}
	if len(cut) > 0 {
		list, err := l.tofu.Run("state", "list", "-no-color")
		if err != nil {
			if !strings.Contains(err.Error(), "No state file was found") {
				l.fail("state: " + err.Error())
				return false
			}
			list = "" // the cut-off apply never wrote one
		}
		for _, w := range cut {
			if !strings.Contains(list, `["`+w.Name+`"]`) {
				w.Removing, w.Reason = true, "orphan"
				l.s.Orphans = append(l.s.Orphans, w.Name)
				l.queueFailed("orphan", w.Name+" not in Tofu state", 1)
			}
		}
	}
	for i := range l.s.Workers {
		l.s.Workers[i].ApplyingAt = 0
	}
	l.checked = true
	return true
}

// removals is step 2. One action per tick, in order: drain a node that still takes tasks, apply
// without the removing workers, DELETE a node that is down. true: it acted (or waits on backoff),
// so no apply follows this tick. Waiting for a node to go down is not an action.
func (l *Loop) removals(now int64) (bool, error) {
	l.forget()
	var rm []*OwnedWorker
	for i := range l.s.Workers {
		if l.s.Workers[i].Removing {
			rm = append(rm, &l.s.Workers[i])
		}
	}
	if len(rm) == 0 {
		return false, nil
	}
	// The removing mark is on disk before anything acts on it.
	if err := l.save(); err != nil {
		return false, err
	}
	for _, w := range rm {
		if n, ok := l.node(w.Name); ok && n.Availability != "drain" {
			l.action = "draining " + w.Name
			if err := l.o.API.Do("POST", "/api/swarm/nodes/"+url.PathEscape(n.ID)+"/drain", nil, nil); err != nil && !noNode(err) {
				l.fail("drain " + w.Name + ": " + err.Error())
			}
			return true, nil
		}
	}
	var in []string
	for _, w := range rm {
		if !w.OutOfState {
			in = append(in, w.Name)
		}
	}
	if len(in) > 0 {
		if now < l.s.NextTryAt {
			l.action = "backoff: remove " + strings.Join(in, ", ")
			return true, nil
		}
		err := l.apply(now, "removed "+strings.Join(in, ", ")+" from Tofu")
		l.forget()
		return true, err
	}
	for _, w := range rm {
		n, ok := l.node(w.Name)
		if !ok || n.State != "down" {
			continue
		}
		l.action = "removing node " + w.Name
		if err := l.o.API.Do("DELETE", "/api/swarm/nodes/"+url.PathEscape(n.ID), nil, nil); err != nil && !noNode(err) {
			l.fail("remove node " + w.Name + ": " + err.Error())
			return true, nil
		}
		l.sig.Nodes = slices.DeleteFunc(l.sig.Nodes, func(x sigNode) bool { return x.ID == n.ID })
		l.forget()
		return true, nil
	}
	return false, nil
}

// noNode is the node routes' 404: the node is already gone, which is what drain and DELETE wanted.
func noNode(err error) bool { return strings.HasPrefix(err.Error(), "no such node") }

// forget drops removing workers that are out of Tofu state and have no swarm node.
func (l *Loop) forget() {
	l.s.Workers = slices.DeleteFunc(l.s.Workers, func(w OwnedWorker) bool {
		if _, ok := l.node(w.Name); !w.Removing || !w.OutOfState || ok {
			return false
		}
		l.queue("scaler.removed", removedData{Name: w.Name, Reason: w.Reason})
		return true
	})
}

// scaleDown is steps 5 and 6: at most one worker becomes removing per tick (observe already took
// the never-applied ones above max). max 0 takes any; otherwise one ready, active and empty past grace, above min or with
// stale settings, still so on a re-read of signals.
func (l *Loop) scaleDown(now int64) {
	owned := slices.IndexFunc(l.s.Workers, func(w OwnedWorker) bool { return !w.Removing })
	if owned < 0 {
		return
	}
	mark := func(w *OwnedWorker, reason string) {
		w.Removing, w.Reason = true, reason
		l.action = "removing " + w.Name
	}
	if l.cfg.Max == 0 {
		mark(&l.s.Workers[owned], "max")
		return
	}
	count := 0
	for _, w := range l.s.Workers {
		if !w.Removing {
			count++
		}
	}
	cur := l.current()
	grace := int64(l.cfg.GraceMin) * time.Minute.Milliseconds()
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		n, ok := l.node(w.Name)
		if w.Removing || !ok || n.State != "ready" || n.Availability != "active" || n.Tasks > 0 || n.EmptySince == nil || now-*n.EmptySince < grace {
			continue
		}
		stale := !maps.Equal(w.Vars, cur)
		if count <= l.cfg.Min && !stale {
			continue
		}
		var again sigView
		if err := l.o.API.Do("GET", "/api/signals", nil, &again); err != nil {
			return
		}
		j := slices.IndexFunc(again.Nodes, func(x sigNode) bool { return x.ID == n.ID })
		if j < 0 || again.Nodes[j].Tasks > 0 || again.Nodes[j].EmptySince == nil || *again.Nodes[j].EmptySince != *n.EmptySince {
			continue
		}
		if stale {
			mark(w, "replaced")
		} else {
			mark(w, "empty")
		}
		return
	}
}

// destroyOld runs `tofu destroy` in a switched-from preset's directory, on a tick that ran no
// other Tofu command, with its own backoff so the new preset proceeds (spec; amends W-R19's shared
// one). A failure keeps the directory and retries.
func (l *Loop) destroyOld(now int64) error {
	old := l.s.PendingDestroy
	if old == "" || l.tofuRan || now < l.s.DestroyNextTryAt {
		return nil
	}
	if err := l.save(); err != nil {
		return err
	}
	t := l.tofu
	t.Dir = filepath.Join(l.o.StateDir, "tofu", old)
	if _, err := t.Run("destroy", "-auto-approve", "-input=false", "-no-color"); err != nil {
		l.s.DestroyFailures++
		l.s.DestroyNextTryAt = now + delay(l.s.DestroyFailures)
		l.fail(err.Error())
		l.action = "destroy failed"
		l.queueFailed("destroy", err.Error(), l.s.DestroyFailures)
		return nil
	}
	if err := os.RemoveAll(t.Dir); err != nil {
		l.fail("destroy " + old + ": " + err.Error())
		return nil
	}
	delete(l.s.Inited, old)
	l.s.PendingDestroy, l.s.DestroyFailures, l.s.DestroyNextTryAt = "", 0, 0
	l.promote() // in this report, so the API keeps the next one's credentials
	l.action = "destroyed " + old
	return nil
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
	// A failed create is retried under its name: desired state is the workers map (P-R1). observe
	// gave it the current settings, and made it removing past max.
	if pending != nil && owned <= l.cfg.Max {
		if now < l.s.NextTryAt {
			l.action = "backoff: retry " + pending.Name
			return nil
		}
		return l.applyNew(now, pending.Name)
	}

	cfg := l.cfg
	want := l.stuckServices(func(st sigStuck) bool {
		return Qualifies(st.Reason, l.managers()) && !slices.Contains(l.s.DoesNotFit, st.Service)
	})
	if len(want) == 0 {
		l.s.MaxReported = false
	}
	cur := l.current()
	var readyEmpty *OwnedWorker
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if n, ok := l.node(w.Name); ok && !w.Removing && n.State == "ready" && n.Availability == "active" && n.Tasks == 0 && maps.Equal(w.Vars, cur) {
			readyEmpty = w
			break
		}
	}
	if owned >= cfg.Min {
		switch {
		case len(want) == 0:
			if dnf := l.stuckServices(func(st sigStuck) bool { return Qualifies(st.Reason, l.managers()) }); len(dnf) > 0 {
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
	if l.s.LastPreset != "" && l.s.LastPreset != l.cfg.Preset {
		// It waits behind PendingDestroy; the LastPreset write below would leak its shared resources.
		l.action = l.s.PendingDestroy + " destroy pending"
		return nil
	}
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
		l.s.LastPreset = l.cfg.Preset // its shared resources exist from this apply on
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
// workers map: every owned worker not removing. removals runs first and takes the tick while a
// removing worker's node is not drained, so no apply destroys a server swarm still schedules on.
func (l *Loop) apply(now int64, did string) error {
	l.tofuRan = true
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
		// Gone from state already: a partial apply destroyed it before failing on something else.
		if w := &l.s.Workers[i]; w.Removing && !strings.Contains(list, `["`+w.Name+`"]`) {
			w.OutOfState = true
		}
	}
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
	if err := l.writeVars(); err != nil {
		return err
	}
	if err := l.save(); err != nil {
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
	for i := range l.s.Workers {
		if l.s.Workers[i].Removing {
			l.s.Workers[i].OutOfState = true
		}
	}
	l.s.Failures, l.s.NextTryAt = 0, 0
	if l.s.DestroyNextTryAt == 0 {
		l.s.LastError = "" // a failed old-preset destroy keeps its error until it retries
	}
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
	l.s.NextTryAt = now + delay(l.s.Failures)
}

// delay is the wait in ms after n failures in a row.
func delay(n int) int64 {
	if n > 6 {
		return BackoffMax.Milliseconds()
	}
	return min(time.Minute<<(n-1), BackoffMax).Milliseconds()
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
	if l.s.DestroyNextTryAt > 0 {
		l.s.DestroyNextTryAt += d
	}
	for i := range l.s.Workers {
		w := &l.s.Workers[i]
		if w.AppliedAt > 0 {
			w.AppliedAt += d
		}
		if w.NotReadySince > 0 {
			w.NotReadySince += d
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
	for _, t := range []int64{l.s.NextTryAt, l.s.DestroyNextTryAt} {
		if t > now && (r.NextRetryAt == 0 || t < r.NextRetryAt) {
			r.NextRetryAt = t
		}
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
