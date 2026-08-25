// Package jobs is the in-memory job registry.
//
// `up`/`down` take minutes, so the HTTP API cannot answer them synchronously. A POST starts a job
// and returns its id; the client polls or subscribes to the log stream. Deliberately in-memory and
// unpersisted (invariant 10): a job record is the transcript of an attempt, never the truth about
// what exists. Restarting the server loses history, not correctness.
//
// ── THE STATE MACHINE ────────────────────────────────────────────────────────────────────────────
//
//	                   Start
//	                     │
//	                     ▼
//	  ┌──────────────  queued  ──── pump: stack idle AND a slot free ────▶  running ──┐
//	  │                  │                                                     │      │
//	  │ Start replaces   │ Cancel(id) / CancelStack                            │      │ work returns
//	  │ it (depth one)   │ (never ran)                                         │      ▼
//	  ▼                  ▼                                        Cancel: ctx ends    ok / failed /
//	superseded       cancelled                                            └──▶ cancelled    leaked
//
// A job is exactly one of three things: waiting for its turn (`queued`), doing work (`running`), or
// finished — `ok`, `failed`, `leaked`, `cancelled`, `superseded`. `State.Terminal()` is the
// predicate; terminal is forever.
//
// What moves a job:
//
//   - Start accepts it as `queued`, WITH an id and a record. The caller is handed that id in a 202
//     and polls it, so the record must exist from the moment of acceptance — never at dispatch.
//   - pump moves `queued` → `running` when that stack has nothing running and a global slot is
//     free, in acceptance order across stacks. It runs inline at the end of Start, so the common
//     case (an idle stack, a free slot) still returns a 202 that says `running`.
//   - run moves `running` → `ok`/`failed`/`leaked`/`cancelled`, then frees the stack and the slot
//     and pumps.
//   - Start on a stack that ALREADY has a queued job supersedes the old one. Depth is one, always:
//     five rapid pushes to a PR run the first deploy and then exactly one more, carrying the newest
//     spec. The middle three are dead the moment a newer one arrives — but they were handed out as
//     job ids, so each reaches `superseded` under its own id rather than silently vanishing.
//   - Cancel(id) ends a `running` job through its context (the work returns, run records it) or a
//     `queued` one on the spot, nothing having run.
//   - CancelStack(stack) is both at once for one stack, decided in ONE critical section.
//
// Impossible, because every mutation above happens under mu:
//
//   - Two `running` jobs for one stack. THE product guarantee — a `down` deleting the database
//     branch an `up` just created is the failure this package exists to prevent.
//   - Two `queued` jobs for one stack: r.queue is keyed BY stack, so depth-one is structural.
//   - More than maxRunning running jobs, or two stacks finishing at once and both taking the same
//     free slot: r.inFlight is incremented in the same critical section that reads it. SetMaxRunning
//     is the ONE exception and it is transient: LOWERING the cap while jobs run leaves the count
//     above it until they finish, because nothing is killed to make the number fit.
//   - A terminal job moving again: every mutation re-checks the state under mu.
//   - A `queued` or `running` job being evicted: evictLocked considers Terminal() jobs only.
//   - A `queued` job with a StartedAt, or a running/terminal one without: pump assigns it at the
//     same instant it assigns `running`.
//
// ── WHY `superseded` IS ITS OWN STATE ────────────────────────────────────────────────────────────
//
// `cancelled` documents a specific fact, here and in events.Names: a person stopped it part-way and
// whatever it had already created or destroyed is still that way. A superseded job never ran — there
// is nothing out there from it. Reporting one as `cancelled` sends an operator hunting for partial
// state that cannot exist. It emits `job.superseded` and, having never started, no `job.started`.
//
// ── WHAT PREEMPTS ───────────────────────────────────────────────────────────────────────────────
//
// `preempts` is a table, not an `if` in Start: a teardown never waits behind a deploy. The
// preempting action clears the stack (running job cancelled, queued job dropped) and then queues
// normally — it cannot literally start until the cancelled job's work returns and frees the stack.
//
// ── THE GLOBAL CAP ──────────────────────────────────────────────────────────────────────────────
//
// At most maxRunning jobs run at once across all stacks (DefaultMaxRunning, PSTACK_MAX_JOBS).
// Over the cap a job WAITS; it is never refused. Dispatch is FIFO by acceptance order, skipping
// stacks that are busy — the same shape as notify's pump one tier up, for the same reason: one
// stack must not starve the others, and a stack that is busy cannot use the slot anyway. The cap is
// global and a preempting `down` does NOT jump ahead of other stacks' older waiting jobs: it
// preempts its own stack, not the host.
//
// The cap is MUTABLE at runtime (SetMaxRunning): the stored `max_jobs` setting outranks
// PSTACK_MAX_JOBS, and an operator changing it must not have to restart the container. Raising it
// pumps; lowering it kills nothing and applies to the next dispatch.
//
// ── CONCURRENCY ──────────────────────────────────────────────────────────────────────────────────
//
// ONE mutex (mu) guards jobs, queue, live, held, inFlight, subs and seq, and it is held across
// every check-then-act. The other rule is that NO method calls a sink, a subscriber or the bus
// while holding mu (rule 14): `run` is four critical sections with those calls in between, because
// the sink re-enters the registry to fan out and a subscriber's callback calls off(). The same
// hazard governs the two additions:
//
//   - pump assigns `running` under mu and emits `job.started` and starts the goroutine AFTER
//     unlocking.
//   - a job that dies without running (superseded, or cancelled while queued) has its terminal
//     state assigned under mu by endLocked, and its transcript line, its `__end__` frame and its
//     bus event delivered by announce, outside. Callers holding mu collect []term and announce
//     after unlocking.
package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

// Action is what a job does. `sleep` and `wake` are the scheduler's verbs (wake IS `up`, recorded
// under its own name so a transcript says why the stack came back).
type Action string

const (
	Up     Action = "up"
	Down   Action = "down"
	Verify Action = "verify"
	Sleep  Action = "sleep"
	Wake   Action = "wake"
)

// preempts names the actions that CLEAR a stack instead of queueing behind it: the running job is
// cancelled and the queued one dropped, then the preempting job queues like any other. One row per
// action — adding another is one line here, not another branch in Start.
var preempts = map[Action]bool{
	// A teardown must never wait behind a deploy. The deploy is building exactly what the operator
	// just asked to destroy, and every second it keeps running is more to clean up afterwards.
	Down: true,
}

// State: `cancelled` is its OWN state, not a flavour of `failed`. A failed job tried and did not
// succeed; a cancelled one was stopped part-way by a person, and what it had already created or
// destroyed is still out there. `superseded` is its own state for the mirror-image reason — see the
// package header.
type State string

const (
	Queued     State = "queued"
	Running    State = "running"
	OK         State = "ok"
	Failed     State = "failed"
	Leaked     State = "leaked"
	Cancelled  State = "cancelled"
	Superseded State = "superseded"
)

// Terminal reports whether the job is finished and will never move again. The predicate every
// caller needs: eviction spares anything that is not terminal, and an SSE stream stays open for
// anything that is not (a queued job's stream must not close before it runs).
func (s State) Terminal() bool { return s != Queued && s != Running }

// Job is one transcript. The wire shape of GET /api/jobs/:id — see MarshalJSON for the key order.
type Job struct {
	ID     string
	Stack  string
	Action Action
	State  State
	// StartedAt is nil for as long as the job is queued — it has not started, and `0` would be a
	// lie a UI renders as 1970 (invariant 11: null is not absent, and not a zero either).
	StartedAt   *int64
	EndedAt     *int64
	Outcome     *stack.Outcome
	Error       *string
	Log         []log.Event
	CancelledBy *string
	seq         int64
	// acceptedAt is when Start took it. Not served: it is the sort key while StartedAt is nil.
	acceptedAt int64
}

// sortAt is when this job last became interesting — it started, or, while still queued, it was
// accepted. List is newest-first over this, so a queued job sorts where it belongs.
func (j Job) sortAt() int64 {
	if j.StartedAt != nil {
		return *j.StartedAt
	}
	return j.acceptedAt
}

// MarshalJSON emits the keys in the order the reference ASSIGNED them: the record literal (id,
// stack, action, state, startedAt, log), then cancelledBy (set mid-run by Cancel), then outcome or
// error, then endedAt. Absent fields are omitted, never null — `startedAt` is the exception, and
// deliberately so: it is a tri-state, null while the job waits.
func (j Job) MarshalJSON() ([]byte, error) {
	logs := j.Log
	if logs == nil {
		logs = []log.Event{}
	}
	var started any
	if j.StartedAt != nil {
		started = *j.StartedAt
	}
	out := jsonx.O("id", j.ID, "stack", j.Stack, "action", j.Action, "state", j.State, "startedAt", started, "log", logs)
	if j.CancelledBy != nil {
		out = append(out, jsonx.KV{K: "cancelledBy", V: *j.CancelledBy})
	}
	if j.Outcome != nil {
		out = append(out, jsonx.KV{K: "outcome", V: *j.Outcome})
	}
	if j.Error != nil {
		out = append(out, jsonx.KV{K: "error", V: *j.Error})
	}
	if j.EndedAt != nil {
		out = append(out, jsonx.KV{K: "endedAt", V: *j.EndedAt})
	}
	return jsonx.Marshal(out)
}

// Stub is the 202 body: four fields, not the whole job.
type Stub struct {
	ID     string `json:"id"`
	Stack  string `json:"stack"`
	Action Action `json:"action"`
	State  State  `json:"state"`
}

// Stub is the 202 shape of a job.
func (j Job) Stub() Stub { return Stub{ID: j.ID, Stack: j.Stack, Action: j.Action, State: j.State} }

// MaxJobs bounds the transcripts KEPT, so a long-lived server cannot grow without limit. Nothing
// to do with concurrency — that is DefaultMaxRunning.
const MaxJobs = 50

// DefaultMaxRunning is how many jobs RUN at once across every stack — the floor of the precedence
// internal/settings owns: the stored `max_jobs` setting, else PSTACK_MAX_JOBS, else this.
// Four because this process also runs docker: each job is a compose invocation plus its hooks, and
// the host has one docker socket and one set of file descriptors to share among them.
const DefaultMaxRunning = 4

// Work is a job's body: narrate into the sink, stop when the context is done.
type Work func(sink log.Sink, ctx context.Context) (stack.Outcome, error)

type entry struct {
	// seq is the insertion order — the tiebreak a stable sort over a JS Map gave for free, and the
	// FIFO key pump dispatches by.
	seq        int64
	acceptedAt int64
	job        *Job
	buf        *log.Buffer
	cancel     context.CancelFunc
	ctx        context.Context
	// work and scrub wait here from acceptance until dispatch.
	work  Work
	scrub func(string) string
}

// term is a job whose terminal state was assigned under mu and whose watchers have not been told
// yet. It exists because nothing may reach a sink, a subscriber or the bus under the lock: the
// holder builds these, unlocks, and hands each to announce.
type term struct {
	e     *entry
	line  string
	level log.Level
	ended int64
	snap  Job
}

// Registry holds the jobs.
//
// Owner: the server. mu guards every map, the counters and the queue; the job records inside are
// mutated only under mu (by their own goroutine in `run`, or by Cancel/pump/Start) and copied out
// under mu by Get/List.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]*entry
	// queue is stack → the ONE job waiting for it. Depth-one is structural, not policed.
	queue map[string]*entry
	// live is stack → the job RUNNING on it. The per-stack mutual exclusion, and what CancelStack
	// cancels.
	live map[string]*entry
	// held is stack → a Hold is out: a route is mutating that deployment and no job may run or be
	// accepted for it.
	held       map[string]bool
	inFlight   int
	maxRunning int
	subs       map[string]map[int]func(log.Event)
	subID      int
	seq        int64
	bus        *events.Bus
}

// New makes a registry on a bus. maxRunning <= 0 means DefaultMaxRunning.
func New(bus *events.Bus, maxRunning int) *Registry {
	if bus == nil {
		bus = events.Default
	}
	if maxRunning <= 0 {
		maxRunning = DefaultMaxRunning
	}
	return &Registry{
		jobs:       map[string]*entry{},
		queue:      map[string]*entry{},
		live:       map[string]*entry{},
		held:       map[string]bool{},
		maxRunning: maxRunning,
		subs:       map[string]map[int]func(log.Event){},
		bus:        bus,
	}
}

// SetMaxRunning changes the global cap without a restart. n <= 0 means DefaultMaxRunning, exactly
// as in New, so a setting that somehow arrives as 0 cannot stop the host from dispatching anything.
//
// RAISING PUMPS. There may be jobs waiting for a slot that now exists, and nothing else would
// dispatch them until some unrelated job happened to finish.
//
// LOWERING KILLS NOTHING. Jobs already running run to completion; the new cap applies to the next
// dispatch, so inFlight can sit ABOVE maxRunning until they finish. An operator who types 1 while
// four jobs are running has cancelled nothing — say so wherever this is exposed.
//
// The usual two rules: the assignment under mu, the pump AFTER unlocking (it emits `job.started`).
func (r *Registry) SetMaxRunning(n int) {
	if n <= 0 {
		n = DefaultMaxRunning
	}
	r.mu.Lock()
	r.maxRunning = n
	r.mu.Unlock()
	r.pump()
}

func (r *Registry) copyLocked(e *entry) Job {
	j := *e.job
	j.seq = e.seq
	j.acceptedAt = e.acceptedAt
	j.Log = e.buf.Events()
	if j.Outcome != nil {
		o := *j.Outcome
		o.Steps = append([]stack.StepResult{}, j.Outcome.Steps...)
		j.Outcome = &o
	}
	return j
}

// List returns copies, newest first.
func (r *Registry) List() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, 0, len(r.jobs))
	for _, e := range r.jobs {
		out = append(out, r.copyLocked(e))
	}
	sort.SliceStable(out, func(i, j int) bool { return newerFirst(out[i], out[j]) })
	return out
}

// newerFirst orders by sortAt desc, insertion order among equals (a stable sort over a Map).
func newerFirst(a, b Job) bool {
	if a.sortAt() != b.sortAt() {
		return a.sortAt() > b.sortAt()
	}
	return a.seq < b.seq
}

// Get returns a copy.
func (r *Registry) Get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.jobs[id]
	if e == nil {
		return Job{}, false
	}
	return r.copyLocked(e), true
}

// IsBusy reports whether the stack has work outstanding — running, queued or held — under the same
// mutex Start takes. Queued counts: the stack has a deploy pending, and a caller asking "is this
// busy" (the scheduler deciding whether to sleep it, the UI's tri-state) needs the answer to
// include work that has been accepted and not yet started.
func (r *Registry) IsBusy(stack string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[stack] != nil || r.queue[stack] != nil || r.held[stack]
}

// Hold takes the per-stack lock without starting a job — for a route that must not race a
// lifecycle job over the registry (PUT/DELETE of a deployment). ok=false when a job is running OR
// queued for that stack: a queued job would otherwise dispatch the instant the hold is released,
// against a deployment that was just replaced or deleted.
func (r *Registry) Hold(stack string) (release func(), ok bool) {
	r.mu.Lock()
	if r.live[stack] != nil || r.queue[stack] != nil || r.held[stack] {
		r.mu.Unlock()
		return nil, false
	}
	r.held[stack] = true
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.held, stack)
		r.mu.Unlock()
		// The hold may have been the only thing a queued job was waiting for. Today it never is —
		// nothing can queue for a held stack, because Start refuses one — so this is the line that
		// keeps that from mattering if the refusal ever softens. It does mean release() reaches the
		// bus: call it holding no other lock (both callers in internal/api unlock first).
		r.pump()
	}, true
}

// Cancel stops a job. A running one ends through its context — its own goroutine writes the record.
// A QUEUED one ends here, having run nothing. Returns false when there is nothing to stop — an
// unknown id, or a job that already finished: "it was already done" and "it has been stopped" are
// different facts. It undoes NOTHING; every resource created before the abort stays created.
func (r *Registry) Cancel(id, by string) bool {
	r.mu.Lock()
	e := r.jobs[id]
	if e == nil || e.job.State.Terminal() {
		r.mu.Unlock()
		return false
	}
	if e.job.State == Queued {
		t := r.endLocked(e, Cancelled, &by, log.Error, "cancelled by "+by+" before it started — nothing ran, so there is nothing to undo.")
		r.mu.Unlock()
		r.announce(t)
		return true
	}
	if e.cancel == nil {
		r.mu.Unlock()
		return false
	}
	e.job.CancelledBy = &by
	e.cancel()
	r.mu.Unlock()
	return true
}

// CancelStack stops everything one stack has outstanding: the running job is cancelled (its context
// ends; its own goroutine writes the terminal record, so the returned copy still says `running`)
// and the queued one is terminated here, having never run. Returns the jobs it acted on, the
// running one first — empty when the stack had nothing outstanding, which is not an error.
//
// It is what a preempting action uses, so there is one implementation of "clear this stack" and
// not two.
func (r *Registry) CancelStack(stack, by string) []Job {
	r.mu.Lock()
	acted, pending := r.clearStackLocked(stack, by)
	r.mu.Unlock()
	for _, t := range pending {
		r.announce(t)
	}
	return acted
}

// clearStackLocked is CancelStack's critical section. Callers announce the returned terms after
// unlocking — never here.
func (r *Registry) clearStackLocked(stack, by string) (acted []Job, pending []term) {
	acted = []Job{}
	if e := r.live[stack]; e != nil && e.job.State == Running && e.cancel != nil {
		b := by
		e.job.CancelledBy = &b
		e.cancel()
		acted = append(acted, r.copyLocked(e))
	}
	if e := r.queue[stack]; e != nil {
		b := by
		t := r.endLocked(e, Cancelled, &b, log.Error, "cancelled by "+by+" before it started — nothing ran, so there is nothing to undo.")
		acted = append(acted, t.snap)
		pending = append(pending, t)
	}
	return acted, pending
}

// endLocked moves a job that never ran to a terminal state. It holds no stack and no slot, so
// there is nothing to release — but its watchers must still be told, which is announce's job.
func (r *Registry) endLocked(e *entry, state State, by *string, level log.Level, line string) term {
	e.job.State = state
	if by != nil {
		e.job.CancelledBy = by
	}
	ended := time.Now().UnixMilli()
	e.job.EndedAt = &ended
	delete(r.queue, e.job.Stack)
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	return term{e: e, line: line, level: level, ended: ended, snap: r.copyLocked(e)}
}

// announce tells everyone watching a job that never ran that it is over: the transcript line, the
// `__end__` frame that closes every open SSE stream for that id, then the bus event. Outside mu,
// always — all three re-enter the registry or reach a listener.
func (r *Registry) announce(t term) {
	t.e.buf.Emit(t.level, t.line)
	r.fanout(t.e.job.ID, log.Event{Seq: -1, At: t.ended, Level: log.Info, Message: "__end__"})
	r.emitTerminal(t.snap, t.ended)
}

// Subscribe attaches fn to a job's log. Returns the replay (every event so far), the job's state
// AT THAT INSTANT, and off — all from one critical section, so a stream that opens between the
// last event and the job's end either sees the end in the replay's state or receives `__end__`.
// The state may be `queued`; the stream stays open until `__end__`, which every terminal path
// sends (State.Terminal() is the test, not `!= Running`).
func (r *Registry) Subscribe(id string, fn func(log.Event)) (replay []log.Event, state State, off func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.jobs[id]
	if e == nil {
		return nil, "", nil, false
	}
	r.subID++
	sid := r.subID
	set := r.subs[id]
	if set == nil {
		set = map[int]func(log.Event){}
		r.subs[id] = set
	}
	set[sid] = fn
	off = func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if s := r.subs[id]; s != nil {
			delete(s, sid)
			if len(s) == 0 {
				delete(r.subs, id)
			}
		}
	}
	return e.buf.Events(), e.job.State, off, true
}

// fanout delivers to every subscriber of a job, OUTSIDE the mutex, each isolated: a broken
// listener must not break the job.
func (r *Registry) fanout(id string, e log.Event) {
	r.mu.Lock()
	fns := make([]func(log.Event), 0, len(r.subs[id]))
	for _, fn := range r.subs[id] {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		func() {
			defer func() { _ = recover() }()
			fn(e)
		}()
	}
}

// Start accepts a job. It is never refused for being busy: a second job for a busy stack QUEUES,
// and a third REPLACES the queued one (depth one, last write wins) — the replaced record reaches
// `superseded` under its own id, because its caller was handed that id and is polling it.
//
// ok=false means the stack is HELD by a route mutating the deployment (Hold), which the caller
// surfaces as 409 — the one remaining refusal.
//
// The returned stub says `running` when the job went straight out (an idle stack and a free slot,
// the common case) and `queued` when it is waiting. `scrub` is applied to every string that lands
// in the RECORD: step messages, captured outputs and the crash error.
func (r *Registry) Start(stackName string, action Action, work Work, scrub func(string) string) (Job, bool) {
	return r.start(stackName, action, work, scrub, false)
}

// StartIfIdle is Start for a caller that wants the OLD refuse-don't-queue rule: ok=false unless the
// stack has nothing running, nothing queued and no hold.
//
// It exists because two callers were built on `Start` refusing a busy stack, and queueing silently
// broke both. `wakeFor` checked IsBusy and then started, with a registry read in between — harmless
// while Start refused, but eight parallel GETs to a sleeping hostname (a browser fetching a page and
// its subresources) then all pass the check and all get accepted, so the stack is woken and woken
// again. The scheduler's idle sweep has the same shape with a `docker inspect` inside the window,
// and its consequence is worse: a `sleep` that used to be refused for racing an operator's deploy now
// QUEUES behind it and tears the compose project down the moment that deploy finishes.
//
// The fix is not another check-then-act. It is one atomic accept-or-refuse under the same mutex,
// named for what those callers actually mean.
func (r *Registry) StartIfIdle(stackName string, action Action, work Work, scrub func(string) string) (Job, bool) {
	return r.start(stackName, action, work, scrub, true)
}

func (r *Registry) start(stackName string, action Action, work Work, scrub func(string) string, onlyIfIdle bool) (Job, bool) {
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	r.mu.Lock()
	// The refusal comes FIRST: a preempting action that is going to be refused must not have killed
	// the running job on its way out.
	if r.held[stackName] {
		r.mu.Unlock()
		return Job{}, false
	}
	if onlyIfIdle && (r.live[stackName] != nil || r.queue[stackName] != nil) {
		r.mu.Unlock()
		return Job{}, false
	}
	var pending []term
	if preempts[action] {
		_, pending = r.clearStackLocked(stackName, "a "+string(action)+" for this stack")
	}
	r.seq++
	now := time.Now().UnixMilli()
	id := fmt.Sprintf("%s-%s-%d-%s", action, stackName, r.seq, randomTail())
	// Depth one: whatever was waiting for this stack is dead the moment a newer job for it arrives.
	// (A preempting action already took it, and endLocked removed it from the queue.)
	//
	// EXCEPT a waiting PREEMPTING action, which outranks the newcomer. Without this carve-out the
	// contract eats itself: a `down` preempts the running `up` — cancelling it, leaving the stack
	// half torn down — then waits its turn, and an `up` arriving a second later supersedes the
	// teardown and redeploys over the wreckage. The operator who asked for a teardown gets a 202,
	// no teardown, and a stack in a state nobody chose. `down` is the action that must never be
	// silently dropped, which is the same reason it preempts in the first place.
	//
	// The newcomer is REFUSED rather than queued behind it: depth stays one, and the caller learns
	// its request did not happen instead of believing it is waiting.
	if old := r.queue[stackName]; old != nil {
		if preempts[old.job.Action] && !preempts[action] {
			r.mu.Unlock()
			return Job{}, false
		}
		pending = append(pending, r.endLocked(old, Superseded, nil, log.Warn,
			"superseded by "+id+" — a newer "+string(action)+" for this stack was accepted, and this job never started."))
	}
	ctx, cancel := context.WithCancel(context.Background())
	buf := log.NewBuffer(func(ev log.Event) { r.fanout(id, ev) })
	job := &Job{ID: id, Stack: stackName, Action: action, State: Queued, Log: []log.Event{}}
	e := &entry{seq: r.seq, acceptedAt: now, job: job, buf: buf, cancel: cancel, ctx: ctx, work: work, scrub: scrub}
	r.jobs[id] = e
	r.queue[stackName] = e
	r.evictLocked()
	r.mu.Unlock()

	// Both outside the lock: announce reaches sinks, subscribers and the bus; pump emits
	// `job.started` and a listener may call IsBusy.
	for _, t := range pending {
		r.announce(t)
	}
	r.pump()

	// Re-read: pump may have dispatched this very job, and the 202 must say which it did.
	stub, ok := r.Get(id)
	if !ok {
		// Unreachable — eviction never drops a non-terminal job — but a stub is owed either way.
		return Job{ID: id, Stack: stackName, Action: action, State: Queued}, true
	}
	return stub, true
}

// pump dispatches every job that can start NOW, in acceptance order across stacks. Called at the
// end of Start, when a job finishes and when a hold is released — anywhere a slot or a stack frees.
//
// NEVER call it holding mu: it emits `job.started`. It does not recurse into run either — the work
// goes to a new goroutine.
func (r *Registry) pump() {
	type dispatch struct {
		e  *entry
		at int64
	}
	r.mu.Lock()
	var out []dispatch
	if len(r.queue) > 0 {
		// At most one waiting job per stack, so this is tiny; seq order IS acceptance order.
		waiting := make([]*entry, 0, len(r.queue))
		for _, e := range r.queue {
			waiting = append(waiting, e)
		}
		sort.SliceStable(waiting, func(i, j int) bool { return waiting[i].seq < waiting[j].seq })
		for _, e := range waiting {
			if r.inFlight >= r.maxRunning {
				break // globally saturated; the next finish pumps again
			}
			s := e.job.Stack
			// A busy stack cannot use a free slot, so skipping it is not queue-jumping — it is the
			// only thing that stops one stack's backlog from starving every other stack.
			if r.live[s] != nil || r.held[s] {
				continue
			}
			at := time.Now().UnixMilli()
			delete(r.queue, s)
			r.live[s] = e
			r.inFlight++
			e.job.State = Running
			e.job.StartedAt = &at
			out = append(out, dispatch{e: e, at: at})
		}
	}
	r.mu.Unlock()

	for _, d := range out {
		// Emitted AFTER unlocking: a listener may call IsBusy.
		r.bus.Emit("job.started", jsonx.O("jobId", d.e.job.ID, "stack", d.e.job.Stack, "action", d.e.job.Action, "startedAt", d.at))
		// Fire and forget: the HTTP handler returned long ago with the job id. Jobs derive from
		// context.Background, not the server: stopping the server never aborts a teardown half-way.
		go r.run(d.e)
	}
}

func (r *Registry) run(e *entry) {
	work, scrub := e.work, e.scrub
	var outcome stack.Outcome
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("%v", p)
			}
		}()
		outcome, err = work(e.buf, e.ctx)
	}()

	// ── 1. record the outcome (under mu) ──
	r.mu.Lock()
	job := e.job
	// Read under mu, WITH the record it decides: a cancel landing between the work returning and
	// this lock sets CancelledBy, and reading ctx.Err() outside would miss it and file the job as
	// `ok` while the record says who stopped it. After this point the state is terminal and both
	// Cancel and clearStackLocked refuse it, so the two can no longer disagree.
	cancelled := e.ctx.Err() != nil
	if err == nil {
		o := outcome
		o.Steps = make([]stack.StepResult, len(outcome.Steps))
		for i, s := range outcome.Steps {
			if s.Message != nil {
				m := scrub(*s.Message)
				s.Message = &m
			}
			o.Steps[i] = s
		}
		// `outputs` is the documented inter-axis credential channel; host secrets echoed into it
		// must not survive into the served record.
		o.Outputs = map[string]string{}
		for k, v := range outcome.Outputs {
			o.Outputs[k] = scrub(v)
		}
		job.Outcome = &o
		// Cancellation wins over the outcome it produced: a stopped run reports non-ok steps by
		// construction, and calling that `failed` would say the work was attempted and failed.
		switch {
		case cancelled:
			job.State = Cancelled
		case outcome.Leaked():
			job.State = Leaked
		case outcome.OK:
			job.State = OK
		default:
			job.State = Failed
		}
	} else {
		if cancelled {
			job.State = Cancelled
		} else {
			job.State = Failed
		}
		msg := scrub(err.Error())
		job.Error = &msg
	}
	ended := time.Now().UnixMilli()
	job.EndedAt = &ended
	crashed := job.Error
	cancelledBy := job.CancelledBy
	state := job.State
	r.mu.Unlock()

	// ── 2. the sink lines (the buffer re-enters the registry to fan out) ──
	if crashed != nil {
		e.buf.Emit(log.Error, "job crashed: "+*crashed)
	}
	if state == Cancelled {
		by := "an operator"
		if cancelledBy != nil {
			by = *cancelledBy
		}
		// Reached on a preempting `down` too: a cancelled `up` left half a stack behind whether a
		// person or a teardown stopped it.
		e.buf.Emit(log.Error, "cancelled by "+by+" — whatever ran before this point was NOT undone. Run verify to see what exists.")
	}

	// ── 3. release the stack and the global slot (under mu) ──
	r.mu.Lock()
	if r.live[job.Stack] == e {
		delete(r.live, job.Stack)
	}
	r.inFlight--
	e.cancel = nil
	snapshot := r.copyLocked(e)
	r.mu.Unlock()
	// Whoever was waiting for that slot or that stack goes now — deferred so it happens after the
	// event below (an operator reads "ended" before "started"), and so a panic in the fan-out
	// cannot strand a freed slot.
	defer r.pump()

	// ── 4. wake every SSE stream so it can observe the terminal state and close ──
	r.fanout(job.ID, log.Event{Seq: -1, At: ended, Level: log.Info, Message: "__end__"})

	// ── 5. THE choke point: success, failure, leaked and crash converge here with `state` assigned.
	// Emitted after the lock is released, so a listener that starts a follow-on job does not block.
	r.emitTerminal(snapshot, ended)
}

// emitTerminal publishes the one event that ends a job, from wherever it ended. Built FIELD BY
// FIELD and deliberately without `outcome` (its outputs are the inter-axis credential channel).
func (r *Registry) emitTerminal(snapshot Job, ended int64) {
	leakedAxes := []string{}
	unverifiable := 0
	verified := any(nil)
	if snapshot.Outcome != nil {
		sawAssert := false
		for _, s := range snapshot.Outcome.Steps {
			if s.Phase == stack.PhaseAssertGone && !s.OK {
				leakedAxes = append(leakedAxes, s.Axis)
			}
			if s.Message != nil && strings.HasPrefix(*s.Message, "unverifiable") {
				unverifiable++
			}
			if s.Phase == stack.PhaseAssertGone && !s.Skipped {
				sawAssert = true
			}
		}
		if snapshot.Action != Up {
			verified = sawAssert
		}
	} else if snapshot.Action != Up {
		verified = false
	}
	name := "job.failed"
	switch snapshot.State {
	case Leaked:
		name = "job.leaked"
	case OK:
		name = "job.succeeded"
	case Cancelled:
		name = "job.cancelled"
	case Superseded:
		name = "job.superseded"
	}
	// A job that never started has no startedAt and no duration; `null` is the honest answer.
	started := any(nil)
	durationMs := int64(0)
	if snapshot.StartedAt != nil {
		started = *snapshot.StartedAt
		durationMs = ended - *snapshot.StartedAt
	}
	payload := jsonx.O(
		"jobId", snapshot.ID,
		"stack", snapshot.Stack,
		"action", snapshot.Action,
		"state", snapshot.State,
		"startedAt", started,
		"endedAt", ended,
		"durationMs", durationMs,
		"leakedAxes", leakedAxes,
		"verified", verified,
		"unverifiable", unverifiable,
	)
	if snapshot.CancelledBy != nil {
		payload = append(payload, jsonx.KV{K: "cancelledBy", V: *snapshot.CancelledBy})
	}
	if snapshot.Error != nil {
		// First line only, and only on the crash path — hook stderr can carry a credential.
		first := redact.RedactText(*snapshot.Error)
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		payload = append(payload, jsonx.KV{K: "error", V: js.Truncate(first, 300)})
	}
	r.bus.Emit(name, payload)
}

// evictLocked drops the oldest FINISHED jobs past MaxJobs. Never a running one, however old, and
// never a queued one — a job nobody has run yet is the last thing that should disappear.
func (r *Registry) evictLocked() {
	if len(r.jobs) <= MaxJobs {
		return
	}
	done := make([]*entry, 0, len(r.jobs))
	for _, e := range r.jobs {
		if e.job.State.Terminal() {
			done = append(done, e)
		}
	}
	// The same key List sorts by, without copying a transcript per comparison.
	at := func(e *entry) int64 {
		if e.job.StartedAt != nil {
			return *e.job.StartedAt
		}
		return e.acceptedAt
	}
	sort.SliceStable(done, func(i, j int) bool {
		if at(done[i]) != at(done[j]) {
			return at(done[i]) > at(done[j])
		}
		return done[i].seq < done[j].seq
	})
	if len(done) > MaxJobs-1 {
		for _, e := range done[MaxJobs-1:] {
			delete(r.jobs, e.job.ID)
		}
	}
}

// randomTail is Math.random().toString(36).slice(2, 8): up to six base36 chars. Exactly six here;
// the conformance masks accept 1–6.
func randomTail() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 6)
	for i := range b {
		b[i] = alphabet[randByte()%36]
	}
	return string(b)
}
