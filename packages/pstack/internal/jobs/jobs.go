// Package jobs is the in-memory job registry.
//
// `up`/`down` take minutes, so the HTTP API cannot answer them synchronously. A POST starts a job
// and returns its id; the client polls or subscribes to the log stream. Deliberately in-memory and
// unpersisted (invariant 10): a job record is the transcript of an attempt, never the truth about
// what exists. Restarting the server loses history, not correctness.
//
// ── CONCURRENCY ──────────────────────────────────────────────────────────────────────────────────
//
// Bun ran one request at a time; here a goroutine per request means the per-stack lock — the
// product's central correctness guarantee (a `down` deleting the database branch an `up` just
// created) — is a real mutex held across the whole check-then-act, and IsBusy reads under the same
// one. The other rule is that NO method calls a sink, a subscriber or the bus while holding mu
// (rule 14): `finish` is four critical sections with those calls in between, because the sink
// re-enters the registry to fan out and a subscriber's callback calls off().
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

// State: `cancelled` is its OWN state, not a flavour of `failed`. A failed job tried and did not
// succeed; a cancelled one was stopped part-way by a person, and what it had already created or
// destroyed is still out there.
type State string

const (
	Running   State = "running"
	OK        State = "ok"
	Failed    State = "failed"
	Leaked    State = "leaked"
	Cancelled State = "cancelled"
)

// Job is one transcript. The wire shape of GET /api/jobs/:id — see MarshalJSON for the key order.
type Job struct {
	ID          string
	Stack       string
	Action      Action
	State       State
	StartedAt   int64
	EndedAt     *int64
	Outcome     *stack.Outcome
	Error       *string
	Log         []log.Event
	CancelledBy *string
	seq         int64
}

// MarshalJSON emits the keys in the order the reference ASSIGNED them: the record literal (id,
// stack, action, state, startedAt, log), then cancelledBy (set mid-run by Cancel), then outcome or
// error, then endedAt. Absent fields are omitted, never null.
func (j Job) MarshalJSON() ([]byte, error) {
	logs := j.Log
	if logs == nil {
		logs = []log.Event{}
	}
	out := jsonx.O("id", j.ID, "stack", j.Stack, "action", j.Action, "state", j.State, "startedAt", j.StartedAt, "log", logs)
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

// MaxJobs bounds the transcripts kept, so a long-lived server cannot grow without limit.
const MaxJobs = 50

// Work is a job's body: narrate into the sink, stop when the context is done.
type Work func(sink log.Sink, ctx context.Context) (stack.Outcome, error)

type entry struct {
	// seq is the insertion order — the tiebreak a stable sort over a JS Map gave for free.
	seq    int64
	job    *Job
	buf    *log.Buffer
	cancel context.CancelFunc
	ctx    context.Context
}

// Registry holds the jobs.
//
// Owner: the server. mu guards every map; the job records inside are mutated only by their own
// goroutine (and `Cancel`, under mu) and copied out under mu by Get/List.
type Registry struct {
	mu    sync.Mutex
	jobs  map[string]*entry
	locks map[string]bool
	subs  map[string]map[int]func(log.Event)
	subID int
	seq   int64
	bus   *events.Bus
}

// New makes a registry on a bus.
func New(bus *events.Bus) *Registry {
	if bus == nil {
		bus = events.Default
	}
	return &Registry{jobs: map[string]*entry{}, locks: map[string]bool{}, subs: map[string]map[int]func(log.Event){}, bus: bus}
}

func (r *Registry) copyLocked(e *entry) Job {
	j := *e.job
	j.seq = e.seq
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

// newerFirst orders by StartedAt desc, insertion order among equals (a stable sort over a Map).
func newerFirst(a, b Job) bool {
	if a.StartedAt != b.StartedAt {
		return a.StartedAt > b.StartedAt
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

// IsBusy reports whether the stack has a job in flight — under the same mutex Start takes.
func (r *Registry) IsBusy(stack string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.locks[stack]
}

// Hold takes the per-stack lock without starting a job — for a route that must not race a
// lifecycle job over the registry (PUT/DELETE of a deployment). ok=false when a job holds it.
func (r *Registry) Hold(stack string) (release func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[stack] {
		return nil, false
	}
	r.locks[stack] = true
	return func() {
		r.mu.Lock()
		delete(r.locks, stack)
		r.mu.Unlock()
	}, true
}

// Cancel stops a running job. Returns false when there is nothing to stop — an unknown id, or a
// job that already finished: "it was already done" and "it has been stopped" are different facts.
// It undoes NOTHING; every resource created before the abort stays created.
func (r *Registry) Cancel(id, by string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.jobs[id]
	if e == nil || e.job.State != Running || e.cancel == nil {
		return false
	}
	e.job.CancelledBy = &by
	e.cancel()
	return true
}

// Subscribe attaches fn to a job's log. Returns the replay (every event so far), the job's state
// AT THAT INSTANT, and off — all from one critical section, so a stream that opens between the
// last event and the job's end either sees the end in the replay's state or receives `__end__`.
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

// Start begins a job. Returns ok=false when that stack already has one in flight — the caller
// surfaces that as 409 rather than queueing. `scrub` is applied to every string that lands in the
// RECORD: step messages, captured outputs and the crash error.
func (r *Registry) Start(stackName string, action Action, work Work, scrub func(string) string) (Job, bool) {
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	r.mu.Lock()
	if r.locks[stackName] {
		r.mu.Unlock()
		return Job{}, false
	}
	r.locks[stackName] = true
	r.seq++
	id := fmt.Sprintf("%s-%s-%d-%s", action, stackName, r.seq, randomTail())
	ctx, cancel := context.WithCancel(context.Background())
	buf := log.NewBuffer(func(ev log.Event) { r.fanout(id, ev) })
	job := &Job{ID: id, Stack: stackName, Action: action, State: Running, StartedAt: time.Now().UnixMilli(), Log: []log.Event{}}
	e := &entry{seq: r.seq, job: job, buf: buf, cancel: cancel, ctx: ctx}
	r.jobs[id] = e
	r.evictLocked()
	stub := r.copyLocked(e)
	r.mu.Unlock()

	// Emitted AFTER unlocking: a listener may call IsBusy.
	r.bus.Emit("job.started", jsonx.O("jobId", id, "stack", stackName, "action", action, "startedAt", job.StartedAt))

	// Fire and forget: the HTTP handler returns immediately with the job id. Jobs derive from
	// context.Background, not the server: stopping the server never aborts a teardown half-way.
	go r.run(e, work, scrub)
	return stub, true
}

func (r *Registry) run(e *entry, work Work, scrub func(string) string) {
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
	cancelled := e.ctx.Err() != nil

	// ── 1. record the outcome (under mu) ──
	r.mu.Lock()
	job := e.job
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
		e.buf.Emit(log.Error, "cancelled by "+by+" — whatever ran before this point was NOT undone. Run verify to see what exists.")
	}

	// ── 3. release the lock (under mu) ──
	r.mu.Lock()
	delete(r.locks, job.Stack)
	e.cancel = nil
	snapshot := r.copyLocked(e)
	r.mu.Unlock()

	// ── 4. wake every SSE stream so it can observe the terminal state and close ──
	r.fanout(job.ID, log.Event{Seq: -1, At: ended, Level: log.Info, Message: "__end__"})

	// ── 5. THE choke point: success, failure, leaked and crash converge here with `state` assigned.
	// Emitted after the lock is released, so a listener that starts a follow-on job does not hit
	// its own 409. Built FIELD BY FIELD and deliberately without `outcome` (its outputs are the
	// inter-axis credential channel).
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
	switch state {
	case Leaked:
		name = "job.leaked"
	case OK:
		name = "job.succeeded"
	case Cancelled:
		name = "job.cancelled"
	}
	payload := jsonx.O(
		"jobId", snapshot.ID,
		"stack", snapshot.Stack,
		"action", snapshot.Action,
		"state", snapshot.State,
		"startedAt", snapshot.StartedAt,
		"endedAt", ended,
		"durationMs", ended-snapshot.StartedAt,
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

// evictLocked drops the oldest finished jobs past MaxJobs. Never a running one, however old.
func (r *Registry) evictLocked() {
	if len(r.jobs) <= MaxJobs {
		return
	}
	done := make([]*entry, 0, len(r.jobs))
	for _, e := range r.jobs {
		if e.job.State != Running {
			done = append(done, e)
		}
	}
	sort.SliceStable(done, func(i, j int) bool {
		if done[i].job.StartedAt != done[j].job.StartedAt {
			return done[i].job.StartedAt > done[j].job.StartedAt
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
