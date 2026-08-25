// Package readiness answers "did the stack actually come up?".
//
// `compose up -d` returns as soon as the containers are *created*, so the deploy job reports success
// while the app is still booting — and the same success when the app boots, throws and exits two
// seconds later. This is the second answer, and it is deliberately observational: it reads what
// Docker already knows (state, exit code, the healthcheck the image declares) and narrates the
// convergence. It starts nothing, restarts nothing, repairs nothing.
//
// READY is not one rule: a container with a healthcheck is ready when Docker says `healthy`; one
// without is ready when it is running — the honest ceiling — and `hasHealthcheck` says which.
//
// TERMINAL, ALWAYS, EXACTLY ONCE: every watch ends in one of stack.ready / stack.failed /
// stack.timedout, so a consumer subscribed to all three never waits forever.
package readiness

import (
	"context"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// ContainerReadiness is one container's verdict.
type ContainerReadiness struct {
	Name           string  `json:"name"`
	Service        *string `json:"service"`
	State          string  `json:"state"`
	Health         *string `json:"health"`
	HasHealthcheck bool    `json:"hasHealthcheck"`
	ExitCode       *int64  `json:"exitCode"`
	RestartCount   int64   `json:"restartCount"`
	Ready          bool    `json:"ready"`
	Failed         bool    `json:"failed"`
	Reason         *string `json:"reason,omitempty"`
}

// State is where a watch is.
type State string

const (
	Watching State = "watching"
	Ready    State = "ready"
	Failed   State = "failed"
	TimedOut State = "timedout"
)

// StackReadiness is the snapshot a client reads.
type StackReadiness struct {
	Stack      string               `json:"stack"`
	State      State                `json:"state"`
	Containers []ContainerReadiness `json:"containers"`
	StartedAt  int64                `json:"startedAt"`
	Reachable  bool                 `json:"reachable"`
	TimeoutMs  int64                `json:"timeoutMs"`
	// EndedAt is last: the reference assigned it at settle, after the literal's other keys.
	EndedAt *int64 `json:"endedAt,omitempty"`
}

const (
	// PollMs: well under any realistic healthcheck interval, and two docker calls.
	PollMs = 2_000
	// DefaultTimeoutMs: an image pull plus a slow boot; short enough that a wedged stack is reported today.
	DefaultTimeoutMs = 180_000
	// RestartLoop is the DEFAULT restarts tolerated before a container is called a crash loop. Not 1
	// (an app that boots before its database dies once and comes back fine); not unbounded (a
	// container under `restart: unless-stopped` that dies on every boot cycles through a
	// healthy-looking sample).
	//
	// Overridable per watcher via Options.RestartLoop (PSTACK_READINESS_RESTART_LOOP). A host running
	// SWARM stacks wants it higher: swarm has no `depends_on`, so a dependent legitimately restarts
	// a few times while its database converges, and 3 calls that a crash loop.
	RestartLoop = 3
)

func readinessOf(c inspect.ContainerInfo, restartLoop int64) ContainerReadiness {
	base := ContainerReadiness{Name: c.Name, Service: c.Service, State: c.State, Health: c.Health, HasHealthcheck: c.Health != nil, ExitCode: c.ExitCode, RestartCount: c.RestartCount}
	reason := func(s string) *string { return &s }
	health := ""
	if c.Health != nil {
		health = *c.Health
	}
	if health == "unhealthy" {
		base.Failed, base.Reason = true, reason("healthcheck reports unhealthy")
		return base
	}
	// A PASSING healthcheck outranks the restart counter: evidence about now beats evidence about the past.
	if c.State == "running" && health == "healthy" {
		base.Ready = true
		return base
	}
	if c.RestartCount >= restartLoop {
		msg := "restarted " + js.ToString(c.RestartCount) + " times — crash loop"
		if c.ExitCode != nil && *c.ExitCode != 0 {
			msg += " (last exit " + js.ToString(*c.ExitCode) + ")"
		}
		base.Failed, base.Reason = true, reason(msg)
		return base
	}
	// A container that exited 0 is NOT a failure: one-shot migration and seed services finish.
	if c.State == "exited" {
		if c.ExitCode != nil && *c.ExitCode == 0 {
			base.Ready = true
			return base
		}
		code := "unknown"
		if c.ExitCode != nil {
			code = js.ToString(*c.ExitCode)
		}
		base.Failed, base.Reason = true, reason("exited with code "+code)
		return base
	}
	if c.State == "dead" {
		base.Failed, base.Reason = true, reason("container is dead")
		return base
	}
	if c.State == "running" && c.Health == nil {
		base.Ready = true
		return base
	}
	// created / restarting / paused / running-but-starting: still converging.
	return base
}

// watch is one stack's poll loop.
//
// Owner: its goroutine mutates snap/health/announced under mu; readers (Get, Wait) copy under mu.
type watch struct {
	mu           sync.Mutex
	snap         StackReadiness
	emit         bool
	orchestrator spec.Orchestrator
	restartLoop  int64
	health       map[string]*string
	announced    map[string]bool
	waiters      []chan StackReadiness
	cancel       context.CancelFunc
	ctx          context.Context
	done         chan struct{}
}

// Options tune the watcher — milliseconds in a test.
type Options struct {
	PollMs    int64
	TimeoutMs int64
	// RestartLoop is the crash-loop threshold; non-positive means the RestartLoop default.
	RestartLoop int64
	Bus         *events.Bus
}

// Watcher holds one watch per stack. Held by the server, never a singleton, so stopping a server
// stops its timers.
//
// Owner: the server. mu guards byStack and is held across the whole start-if-absent check-then-act
// (rule 14: a GET that starts a silent watch must not race a deploy that starts a loud one).
type Watcher struct {
	mu          sync.Mutex
	byStack     map[string]*watch
	pollMs      int64
	timeoutMs   int64
	restartLoop int64
	bus         *events.Bus
}

// New makes a watcher.
func New(o Options) *Watcher {
	if o.PollMs <= 0 {
		o.PollMs = PollMs
	}
	if o.TimeoutMs <= 0 {
		o.TimeoutMs = DefaultTimeoutMs
	}
	if o.RestartLoop <= 0 {
		o.RestartLoop = RestartLoop
	}
	if o.Bus == nil {
		o.Bus = events.Default
	}
	return &Watcher{byStack: map[string]*watch{}, pollMs: o.PollMs, timeoutMs: o.TimeoutMs, restartLoop: o.RestartLoop, bus: o.Bus}
}

// Get returns a copy of the current snapshot, if a watch exists.
func (ws *Watcher) Get(stack string) (StackReadiness, bool) {
	ws.mu.Lock()
	w := ws.byStack[stack]
	ws.mu.Unlock()
	if w == nil {
		return StackReadiness{}, false
	}
	return w.copy(), true
}

// StartOptions parameterise Start.
type StartOptions struct {
	TimeoutMs int64
	// Restart replaces a finished watch; a running one is returned as-is either way.
	Restart bool
	// Emit is whether the watch announces itself on the bus. FALSE for a watch a mere READ started:
	// opening a never-deployed deployment's page must not put "pr-9 did not become ready" in Slack.
	Emit         bool
	Orchestrator spec.Orchestrator
}

// Start begins watching, or returns the watch already running. A FINISHED watch is replaced, not
// returned: asking again after a deploy means "how is it NOW".
func (ws *Watcher) Start(stack string, r exec.Runner, o StartOptions) StackReadiness {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if existing := ws.byStack[stack]; existing != nil {
		if existing.state() == Watching && !o.Restart {
			return existing.copy()
		}
		existing.stop()
	}
	timeout := o.TimeoutMs
	if timeout <= 0 {
		timeout = ws.timeoutMs
	}
	orch := o.Orchestrator
	if orch == "" {
		orch = spec.Compose
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &watch{
		snap:         StackReadiness{Stack: stack, State: Watching, Containers: []ContainerReadiness{}, StartedAt: time.Now().UnixMilli(), Reachable: true, TimeoutMs: timeout},
		emit:         o.Emit,
		orchestrator: orch,
		restartLoop:  ws.restartLoop,
		health:       map[string]*string{},
		announced:    map[string]bool{},
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	ws.byStack[stack] = w
	go ws.loop(w, r)
	return w.copy()
}

// Wait resolves when the watch reaches a terminal state, or when `ms` elapse — the long-poll behind
// GET …/readiness?wait=30. Returning the CURRENT snapshot on expiry is what makes a client loop
// trivial; there is no missed-edge race because the snapshot carries the state.
func (ws *Watcher) Wait(stack string, ms int64) (StackReadiness, bool) {
	ws.mu.Lock()
	w := ws.byStack[stack]
	ws.mu.Unlock()
	if w == nil {
		return StackReadiness{}, false
	}
	w.mu.Lock()
	if w.snap.State != Watching {
		s := w.snap
		w.mu.Unlock()
		return cloneSnap(s), true
	}
	// Buffered and never closed: a settle that fires after the timer has given up writes into the
	// buffer and moves on, instead of blocking the watch goroutine or panicking on a closed channel.
	ch := make(chan StackReadiness, 1)
	w.waiters = append(w.waiters, ch)
	w.mu.Unlock()
	select {
	case s := <-ch:
		return s, true
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return w.copy(), true
	}
}

// Cancel stops watching — a teardown makes every pending readiness question moot.
func (ws *Watcher) Cancel(stack string) {
	ws.mu.Lock()
	w := ws.byStack[stack]
	delete(ws.byStack, stack)
	ws.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

// StopAll cancels every watch.
func (ws *Watcher) StopAll() {
	ws.mu.Lock()
	all := make([]*watch, 0, len(ws.byStack))
	for _, w := range ws.byStack {
		all = append(all, w)
	}
	ws.byStack = map[string]*watch{}
	ws.mu.Unlock()
	for _, w := range all {
		w.stop()
	}
}

func (w *watch) state() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snap.State
}

func (w *watch) copy() StackReadiness {
	w.mu.Lock()
	defer w.mu.Unlock()
	return cloneSnap(w.snap)
}

func cloneSnap(s StackReadiness) StackReadiness {
	out := s
	out.Containers = make([]ContainerReadiness, len(s.Containers))
	copy(out.Containers, s.Containers)
	return out
}

// stop cancels the loop, drains the waiters with the current snapshot, and waits for the goroutine.
func (w *watch) stop() {
	w.cancel()
	w.mu.Lock()
	s := cloneSnap(w.snap)
	waiters := w.waiters
	w.waiters = nil
	w.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- s:
		default:
		}
	}
	<-w.done
}

func (ws *Watcher) loop(w *watch, r exec.Runner) {
	defer close(w.done)
	defer func() { _ = recover() }()
	for w.ctx.Err() == nil && w.state() == Watching {
		ws.tick(w, r)
		if w.ctx.Err() != nil || w.state() != Watching {
			return
		}
		w.mu.Lock()
		expired := time.Now().UnixMilli()-w.snap.StartedAt >= w.snap.TimeoutMs
		w.mu.Unlock()
		if expired {
			ws.settle(w, TimedOut)
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(time.Duration(ws.pollMs) * time.Millisecond):
		}
	}
}

func (ws *Watcher) tick(w *watch, r exec.Runner) {
	w.mu.Lock()
	stack := w.snap.Stack
	w.mu.Unlock()
	rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: stack, Runner: r, Challenge: inspect.Unknown, Orchestrator: w.orchestrator})
	if w.ctx.Err() != nil {
		return
	}
	// Docker did not answer. Report it and keep watching: a momentary hiccup is not a verdict.
	if !rt.Reachable {
		w.mu.Lock()
		w.snap.Reachable = false
		w.mu.Unlock()
		return
	}
	containers := make([]ContainerReadiness, 0, len(rt.Containers))
	for _, c := range rt.Containers {
		containers = append(containers, readinessOf(c, w.restartLoop))
	}
	var toEmit []func()
	w.mu.Lock()
	w.snap.Reachable = true
	w.snap.Containers = containers
	for _, c := range containers {
		toEmit = append(toEmit, ws.healthEvents(w, c)...)
		if w.announced[c.Name] {
			continue
		}
		c := c
		if c.Failed {
			w.announced[c.Name] = true
			if w.emit {
				toEmit = append(toEmit, func() {
					ws.bus.Emit("container.start-failed", jsonx.O("stack", stack, "container", c.Name, "service", c.Service, "state", c.State, "health", c.Health, "exitCode", c.ExitCode, "reason", c.Reason))
				})
			}
		} else if c.Ready {
			w.announced[c.Name] = true
			if w.emit {
				toEmit = append(toEmit, func() {
					ws.bus.Emit("container.ready", jsonx.O("stack", stack, "container", c.Name, "service", c.Service, "state", c.State, "health", c.Health, "hasHealthcheck", c.HasHealthcheck))
				})
			}
		}
	}
	anyFailed, allReady := false, len(containers) > 0
	for _, c := range containers {
		if c.Failed {
			anyFailed = true
		}
		if !c.Ready {
			allReady = false
		}
	}
	w.mu.Unlock()
	// Listeners run OUTSIDE the watch mutex (rule 14).
	for _, fn := range toEmit {
		fn()
	}
	if anyFailed {
		ws.settle(w, Failed)
		return
	}
	// `len > 0` matters: compose creates containers a moment after `up` returns, and an empty list
	// read as "all ready" would report every stack ready before it had started.
	if allReady {
		ws.settle(w, Ready)
	}
}

// healthEvents computes healthcheck.* for one container from the change in Docker's own status
// string. Called under w.mu; returns the emits to perform after unlocking.
func (ws *Watcher) healthEvents(w *watch, c ContainerReadiness) []func() {
	prev, seen := w.health[c.Name]
	if seen && equalStr(prev, c.Health) {
		return nil
	}
	w.health[c.Name] = c.Health
	if c.Health == nil || !w.emit {
		return nil
	}
	stack := w.snap.Stack
	health := *c.Health
	base := func() jsonx.Object {
		return jsonx.O("stack", stack, "container", c.Name, "service", c.Service, "status", health)
	}
	var out []func()
	if !seen || prev == nil {
		out = append(out, func() { ws.bus.Emit("healthcheck.started", base()) })
	} else {
		p := *prev
		out = append(out, func() { ws.bus.Emit("healthcheck.updated", append(base(), jsonx.KV{K: "previous", V: p})) })
	}
	// healthy/unhealthy are terminal for the PROBE even when the container keeps running.
	if health == "healthy" || health == "unhealthy" {
		out = append(out, func() {
			ws.bus.Emit("healthcheck.finished", append(base(), jsonx.KV{K: "healthy", V: health == "healthy"}))
		})
	}
	return out
}

func equalStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (ws *Watcher) settle(w *watch, state State) {
	w.mu.Lock()
	if w.snap.State != Watching {
		w.mu.Unlock()
		return
	}
	w.snap.State = state
	ended := time.Now().UnixMilli()
	w.snap.EndedAt = &ended
	s := cloneSnap(w.snap)
	waiters := w.waiters
	w.waiters = nil
	emit := w.emit
	w.mu.Unlock()

	if state == TimedOut && emit {
		// Name what was still in flight when the deadline hit.
		for _, c := range s.Containers {
			if c.Health != nil && *c.Health == "starting" {
				ws.bus.Emit("healthcheck.timedout", jsonx.O("stack", s.Stack, "container", c.Name, "service", c.Service, "status", c.Health, "waitedMs", ended-s.StartedAt))
			}
		}
	}
	pending, failed := []string{}, []string{}
	ready := 0
	for _, c := range s.Containers {
		if c.Failed {
			failed = append(failed, c.Name)
		} else if !c.Ready {
			pending = append(pending, c.Name)
		}
		if c.Ready {
			ready++
		}
	}
	if emit {
		name := "stack.timedout"
		if state == Ready {
			name = "stack.ready"
		} else if state == Failed {
			name = "stack.failed"
		}
		ws.bus.Emit(name, jsonx.O("stack", s.Stack, "state", state, "containers", len(s.Containers), "ready", ready, "failedContainers", failed, "pendingContainers", pending, "durationMs", ended-s.StartedAt, "reachable", s.Reachable))
	}
	for _, ch := range waiters {
		select {
		case ch <- s:
		default:
		}
	}
}
