package jobs

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

func okWork(o stack.Outcome) Work {
	return func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Info, "hello")
		return o, nil
	}
}

// blockWork runs until release is closed. The building block of every queue test: a stack with one
// of these on it is busy for exactly as long as the test wants and not one scheduling quantum more.
func blockWork(release <-chan struct{}) Work {
	return func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Step, "working")
		select {
		case <-release:
		case <-ctx.Done():
		}
		return stack.Outcome{OK: true, Steps: []stack.StepResult{}, Outputs: map[string]string{}}, nil
	}
}

// waitDone blocks until the job has finished AND the registry has let go of its stack.
//
// The state alone is the wrong signal, and this is not theoretical — it is a CI failure. `run`
// runs in FOUR critical sections: section 1 assigns the terminal state and unlocks, and section 3
// releases the stack and the global slot. Between them `r.Get` reports the job done while the next
// job for that stack has not been dispatched. A loop that starts a job, waits for it, and starts
// the next one — which is exactly what the eviction test does — then races on a loaded runner and
// never on an unloaded laptop.
//
// So this waits for both. Only for a job whose stack has nothing else outstanding; use waitTerminal
// for one that was superseded or cancelled out from under a busy stack.
//
// The bus event is later still (section 5): assertions about events use waitEvents, never this.
func waitDone(t *testing.T, r *Registry, id string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := r.Get(id)
		if ok && j.State.Terminal() && !r.IsBusy(j.Stack) {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return Job{}
}

// waitTerminal waits for the record alone — the job's stack may well be busy with its replacement.
func waitTerminal(t *testing.T, r *Registry, id string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.Get(id); ok && j.State.Terminal() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached a terminal state", id)
	return Job{}
}

// waitState waits for one specific state. Dispatch out of the queue happens on another goroutine
// (run's deferred pump), so "it should be running now" is a poll, never an assertion.
func waitState(t *testing.T, r *Registry, id string, want State) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last State
	for time.Now().Before(deadline) {
		if j, ok := r.Get(id); ok {
			if j.State == want {
				return j
			}
			last = j.State
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s is %s, wanted %s", id, last, want)
	return Job{}
}

// stateOf is the state right now — for asserting that something has NOT moved.
func stateOf(t *testing.T, r *Registry, id string) State {
	t.Helper()
	j, ok := r.Get(id)
	if !ok {
		t.Fatalf("job %s is gone", id)
	}
	return j.State
}

type captured struct {
	mu sync.Mutex
	ev []events.Event
}

func (c *captured) on(e events.Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }
func (c *captured) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{}
	for _, e := range c.ev {
		out = append(out, e.Event)
	}
	return out
}

// waitEvents blocks until n events have been captured.
//
// `waitDone` is NOT enough on its own, and the difference is the source of a real CI failure.
// `run` publishes the job's terminal state to the registry, RELEASES the mutex, and only then
// builds the payload and emits — deliberately, because no registry method may call the bus while
// holding the lock. So there is a window in which `r.Get` already reports the job finished and the
// terminal event has not been dispatched yet. On an unloaded laptop it never opens; on a loaded
// runner it does, and the test failed with `events [job.started]`.
//
// Every assertion about an event therefore waits for the EVENT, never for the state.
func waitEvents(t *testing.T, c *captured, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.ev)
		c.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %v arrived; wanted %d events", c.names(), n)
}

// data is one captured payload, read under the lock — the listener appends from the job's goroutine.
func (c *captured) data(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.ev) {
		return ""
	}
	return string(c.ev[i].Data)
}

// payloadFor is the last captured payload for one job id.
//
// c.last() is only safe when one job is in play: a finishing job releases its stack in section 3
// and emits its terminal event in section 5, so a job started in between can have its `job.started`
// land FIRST. Anything asserting about a specific job's event asks for it by id.
func (c *captured) payloadFor(t *testing.T, id string) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.ev) - 1; i >= 0; i-- {
		var m map[string]any
		_ = json.Unmarshal(c.ev[i].Data, &m)
		if m["jobId"] == id {
			return m
		}
	}
	t.Fatalf("no event for %s", id)
	return nil
}

func (c *captured) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(c.ev[len(c.ev)-1].Data, &m)
	return m
}

// endWatcher subscribes to a job and reports whether its stream was closed with `__end__`.
func endWatcher(t *testing.T, r *Registry, id string) (ended <-chan struct{}, lines func() []string) {
	t.Helper()
	done := make(chan struct{})
	var mu sync.Mutex
	var seen []string
	var once sync.Once
	_, _, _, ok := r.Subscribe(id, func(e log.Event) {
		mu.Lock()
		seen = append(seen, e.Message)
		mu.Unlock()
		if e.Message == "__end__" {
			once.Do(func() { close(done) })
		}
	})
	if !ok {
		t.Fatalf("subscribe to %s failed", id)
	}
	return done, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, seen...)
	}
}

func mustEnd(t *testing.T, ended <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: the stream never received __end__", what)
	}
}

// negative control: drop `r.live[s] = e` in pump → the second job for pr-1 dispatches beside the
// first and the "one running job per stack" assertion fails.
func TestPerStackQueueAndIds(t *testing.T) {
	bus := events.New()
	r := New(bus, 0)
	release := make(chan struct{})
	j, ok := r.Start("pr-1", Up, blockWork(release), nil)
	if !ok {
		t.Fatal("first start refused")
	}
	if !regexp.MustCompile(`^up-pr-1-1-[a-z0-9]{6}$`).MatchString(j.ID) {
		t.Fatalf("id %q", j.ID)
	}
	if j.State != Running || j.Stack != "pr-1" || j.Action != Up || j.StartedAt == nil {
		t.Fatalf("stub %+v", j)
	}
	// A second job for a busy stack is ACCEPTED and queued, not refused — and it has an id and a
	// record from that instant, because the caller was handed one in a 202. (`verify`, not `down`:
	// a down would preempt rather than queue — see TestDownPreemptsRunningAndDropsQueued.)
	qRelease := make(chan struct{})
	q, ok := r.Start("pr-1", Verify, blockWork(qRelease), nil)
	if !ok {
		t.Fatal("second job on a busy stack must be accepted")
	}
	if q.State != Queued || q.StartedAt != nil || q.EndedAt != nil {
		t.Fatalf("queued stub %+v", q)
	}
	if got, ok := r.Get(q.ID); !ok || got.State != Queued {
		t.Fatalf("queued job not observable by id: %+v %v", got, ok)
	}
	if stateOf(t, r, j.ID) != Running {
		t.Fatal("queueing must not disturb the running job")
	}
	if !r.IsBusy("pr-1") || r.IsBusy("pr-2") {
		t.Fatal("IsBusy wrong")
	}
	if _, ok := r.Hold("pr-1"); ok {
		t.Fatal("Hold on a busy stack must fail")
	}
	rel, ok := r.Hold("pr-2")
	if !ok {
		t.Fatal("Hold on an idle stack must succeed")
	}
	if !r.IsBusy("pr-2") {
		t.Fatal("a held stack is busy")
	}
	if _, ok := r.Start("pr-2", Up, okWork(stack.Outcome{OK: true}), nil); ok {
		t.Fatal("Start under Hold must be refused — it is the one remaining 409")
	}
	rel()
	if r.IsBusy("pr-2") {
		t.Fatal("release did not free pr-2")
	}
	close(release)
	// The queued job takes the stack as soon as the first releases it, with no further Start.
	done := waitState(t, r, q.ID, Running)
	if done.StartedAt == nil {
		t.Fatal("a dispatched job must have a startedAt")
	}
	first, _ := r.Get(j.ID)
	if first.State != OK || first.EndedAt == nil {
		t.Fatalf("first job %+v", first)
	}
	close(qRelease)
	waitDone(t, r, q.ID)
	if r.IsBusy("pr-1") {
		t.Fatal("stack never freed")
	}
}

// negative control: emit `job.started` before unlocking → the listener's IsBusy sees… the same, but
// swap the payload order (stack before jobId) → the key order assertion fails.
func TestStateFromOutcomeAndEventPayload(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus, 0)
	assertLeak := stack.StepResult{Axis: "db", Phase: stack.PhaseAssertGone, OK: false, Code: 1}
	cases := []struct {
		action Action
		o      stack.Outcome
		state  State
		event  string
		leaked []any
		verif  any
	}{
		{Up, stack.Outcome{OK: true}, OK, "job.succeeded", []any{}, nil},
		{Up, stack.Outcome{OK: false, Steps: []stack.StepResult{{Axis: "a", Phase: stack.PhaseUp, Code: 1}}}, Failed, "job.failed", []any{}, nil},
		{Verify, stack.Outcome{OK: false, Steps: []stack.StepResult{assertLeak}}, Leaked, "job.leaked", []any{"db"}, true},
		{Verify, stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseAssertGone, OK: true}}}, OK, "job.succeeded", []any{}, true},
		{Verify, stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseAssertGone, OK: true, Skipped: true}}}, OK, "job.succeeded", []any{}, false},
		{Verify, stack.Outcome{OK: true}, OK, "job.succeeded", []any{}, false},
	}
	for i, tc := range cases {
		c.mu.Lock()
		c.ev = nil
		c.mu.Unlock()
		j, ok := r.Start("s", tc.action, okWork(tc.o), nil)
		if !ok {
			t.Fatal(i)
		}
		// NOT `== Running`: Start re-reads the record after pump, so work that finishes before this
		// goroutine resumes yields a stub already in a terminal state. Every real hook shells out and
		// takes milliseconds, so nothing else observes it — but `-count=100` does, a few runs in.
		// What the 202 actually promises is "this did not queue", and that is what is asserted.
		if j.State == Queued {
			t.Fatalf("case %d: an idle stack with a free slot dispatches inline; got %s", i, j.State)
		}
		done := waitDone(t, r, j.ID)
		if done.State != tc.state {
			t.Fatalf("case %d: state %s want %s", i, done.State, tc.state)
		}
		if done.Outcome == nil || done.Outcome.Steps == nil || done.Outcome.Outputs == nil || done.Log == nil {
			t.Fatalf("case %d: nil collections in %+v", i, done)
		}
		waitEvents(t, c, 2)
		names := c.names()
		if len(names) != 2 || names[0] != "job.started" || names[1] != tc.event {
			t.Fatalf("case %d: events %v", i, names)
		}
		raw := c.data(1)
		want := `{"jobId":"` + j.ID + `","stack":"s","action":"` + string(tc.action) + `","state":"` + string(tc.state) + `","startedAt":`
		if !strings.HasPrefix(raw, want) {
			t.Fatalf("case %d: payload order %s", i, raw)
		}
		if strings.Contains(raw, "outcome") || strings.Contains(raw, "outputs") {
			t.Fatalf("case %d: outcome leaked into the event: %s", i, raw)
		}
		p := c.last()
		if got := p["leakedAxes"].([]any); len(got) != len(tc.leaked) {
			t.Fatalf("case %d: leakedAxes %v", i, got)
		}
		if p["verified"] != tc.verif {
			t.Fatalf("case %d: verified %v want %v", i, p["verified"], tc.verif)
		}
		if p["unverifiable"] != float64(0) {
			t.Fatalf("case %d: unverifiable %v", i, p["unverifiable"])
		}
		if _, has := p["error"]; has {
			t.Fatalf("case %d: error on a non-crash", i)
		}
	}
}

// negative control: remove scrub on outputs → "s3cret" survives in the record.
func TestCrashAndScrub(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus, 0)
	scrub := func(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }
	m := "token s3cret here"
	o := stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "a", Phase: stack.PhaseUp, OK: true, Message: &m}}, Outputs: map[string]string{"K": "s3cret"}}
	j, _ := r.Start("s", Up, okWork(o), scrub)
	done := waitDone(t, r, j.ID)
	if *done.Outcome.Steps[0].Message != "token *** here" || done.Outcome.Outputs["K"] != "***" {
		t.Fatalf("not scrubbed: %+v", done.Outcome)
	}
	if m != "token s3cret here" {
		t.Fatal("scrub mutated the caller's outcome")
	}

	j2, _ := r.Start("s", Up, func(log.Sink, context.Context) (stack.Outcome, error) {
		panic("boom s3cret\nsecond line")
	}, scrub)
	done = waitDone(t, r, j2.ID)
	if done.State != Failed || done.Error == nil || *done.Error != "boom ***\nsecond line" || done.Outcome != nil {
		t.Fatalf("crash record %+v", done)
	}
	if len(done.Log) != 1 || done.Log[0].Message != "job crashed: boom ***\nsecond line" || done.Log[0].Level != log.Error {
		t.Fatalf("crash log %+v", done.Log)
	}
	waitEvents(t, c, 4) // two jobs × (started, terminal)
	p := c.payloadFor(t, j2.ID)
	if p["error"] != "boom ***" || p["state"] != "failed" {
		t.Fatalf("crash event %v", p)
	}
	if r.IsBusy("s") {
		t.Fatal("lock leaked after crash")
	}
}

// negative control: make run fan out `__end__` while holding mu → deadlock (the -race run
// hangs); or skip step 4 → the subscriber never sees the end frame.
func TestCancelWakesSubscriber(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus, 0)
	work := func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Step, "working")
		<-ctx.Done()
		return stack.Outcome{OK: false, Steps: []stack.StepResult{{Axis: "a", Phase: stack.PhaseUp, Code: 130}}}, nil
	}
	j, _ := r.Start("s", Up, work, nil)
	got := make(chan log.Event, 16)
	var off func()
	replay, state, off, ok := r.Subscribe(j.ID, func(e log.Event) {
		got <- e
		if e.Message == "__end__" {
			off() // a subscriber calling off() from inside its callback must not deadlock
		}
	})
	if !ok || state != Running {
		t.Fatalf("subscribe ok=%v state=%s", ok, state)
	}
	// the replay contains at least the started line; it may or may not include "working" yet
	_ = replay
	if !r.Cancel(j.ID, "alice") {
		t.Fatal("cancel refused")
	}
	deadline := time.After(time.Second)
	var seen []string
	for {
		select {
		case e := <-got:
			seen = append(seen, e.Message)
			if e.Message == "__end__" {
				goto ended
			}
		case <-deadline:
			t.Fatalf("no __end__ within 1s; saw %v", seen)
		}
	}
ended:
	done := waitDone(t, r, j.ID)
	if done.State != Cancelled || done.CancelledBy == nil || *done.CancelledBy != "alice" {
		t.Fatalf("record %+v", done)
	}
	last := done.Log[len(done.Log)-1]
	if last.Message != "cancelled by alice — whatever ran before this point was NOT undone. Run verify to see what exists." || last.Level != log.Error {
		t.Fatalf("cancel line %+v", last)
	}
	joined := strings.Join(seen, "|")
	if !strings.Contains(joined, "cancelled by alice") {
		t.Fatalf("subscriber missed the cancel line: %v", seen)
	}
	// The bus event is step 5 of run, after the __end__ fan-out: wait for it.
	waitEvents(t, c, 2)
	if names := c.names(); names[len(names)-1] != "job.cancelled" || c.last()["cancelledBy"] != "alice" {
		t.Fatalf("events %v %v", names, c.last())
	}
	if r.Cancel(j.ID, "x") {
		t.Fatal("cancel of a finished job must return false")
	}
	if r.Cancel("nope", "x") {
		t.Fatal("cancel of unknown must return false")
	}
	if _, state, _, ok := r.Subscribe(j.ID, func(log.Event) {}); !ok || state != Cancelled {
		t.Fatalf("late subscribe state %s", state)
	}
}

// ── the queue ───────────────────────────────────────────────────────────────────────────────────

// negative control: drop the `if old := r.queue[stackName]` block in Start → two jobs sit in the
// queue map for one stack (the second overwrites the first), the first NEVER reaches a terminal
// state and its subscriber waits forever: "the stream never received __end__".
func TestQueueDepthOneLastWriteWins(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus, 0)
	release := make(chan struct{})
	first, _ := r.Start("pr-1", Up, blockWork(release), nil)
	if first.State != Running {
		t.Fatalf("first %+v", first)
	}
	second, _ := r.Start("pr-1", Up, okWork(stack.Outcome{OK: true}), nil)
	if second.State != Queued {
		t.Fatalf("second %+v", second)
	}
	ended, lines := endWatcher(t, r, second.ID)

	// The third REPLACES the second. Depth is one, always.
	third, ok := r.Start("pr-1", Up, okWork(stack.Outcome{OK: true}), nil)
	if !ok || third.State != Queued {
		t.Fatalf("third %+v ok=%v", third, ok)
	}
	r.mu.Lock()
	depth := len(r.queue)
	head := r.queue["pr-1"]
	r.mu.Unlock()
	if depth != 1 || head == nil || head.job.ID != third.ID {
		t.Fatalf("queue depth %d, head %v — want only %s", depth, head, third.ID)
	}

	// The replaced job reaches a terminal state UNDER ITS OWN ID: the caller holds that id.
	sup := waitTerminal(t, r, second.ID)
	if sup.State != Superseded {
		t.Fatalf("replaced job is %s", sup.State)
	}
	if sup.StartedAt != nil || sup.EndedAt == nil {
		t.Fatalf("a superseded job never started and must still have ended: %+v", sup)
	}
	if sup.CancelledBy != nil {
		t.Fatal("nobody cancelled it — superseded is not cancelled")
	}
	want := "superseded by " + third.ID + " — a newer up for this stack was accepted, and this job never started."
	if len(sup.Log) == 0 || sup.Log[len(sup.Log)-1].Message != want {
		t.Fatalf("transcript %+v", sup.Log)
	}
	mustEnd(t, ended, "superseded job")
	if got := strings.Join(lines(), "|"); !strings.Contains(got, "superseded by "+third.ID) {
		t.Fatalf("subscriber missed the reason: %v", got)
	}

	// Five pushes → the first deploy and exactly one more, carrying the newest spec.
	close(release)
	waitDone(t, r, third.ID)
	if stateOf(t, r, first.ID) != OK {
		t.Fatal("the running job must be untouched by all of this")
	}

	// Ordering, in full: nothing is emitted for the queued job when it is accepted (it has not
	// started), the terminal event of a finishing job precedes the started event of its successor.
	waitEvents(t, c, 5)
	got := c.names()
	wantNames := []string{"job.started", "job.superseded", "job.succeeded", "job.started", "job.succeeded"}
	if strings.Join(got, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("events %v want %v", got, wantNames)
	}
	var p map[string]any
	c.mu.Lock()
	_ = json.Unmarshal(c.ev[1].Data, &p)
	c.mu.Unlock()
	if p["jobId"] != second.ID || p["state"] != "superseded" || p["startedAt"] != nil || p["durationMs"] != float64(0) {
		t.Fatalf("job.superseded payload %v", p)
	}
}

// negative control: in Cancel, delete the `if e.job.State == Queued` branch (fall through to the
// ctx path) → Cancel returns false for a queued job and it later RUNS.
func TestCancelQueuedById(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus, 0)
	release := make(chan struct{})
	first, _ := r.Start("pr-1", Up, blockWork(release), nil)
	ran := make(chan struct{}, 1)
	q, _ := r.Start("pr-1", Up, func(log.Sink, context.Context) (stack.Outcome, error) {
		ran <- struct{}{}
		return stack.Outcome{OK: true}, nil
	}, nil)
	ended, _ := endWatcher(t, r, q.ID)
	if !r.Cancel(q.ID, "alice") {
		t.Fatal("cancelling a queued job must work")
	}
	j := waitTerminal(t, r, q.ID)
	if j.State != Cancelled || j.CancelledBy == nil || *j.CancelledBy != "alice" || j.StartedAt != nil {
		t.Fatalf("record %+v", j)
	}
	// The line must NOT be the running-job one: nothing ran, so there is nothing left behind.
	line := j.Log[len(j.Log)-1].Message
	if line != "cancelled by alice before it started — nothing ran, so there is nothing to undo." {
		t.Fatalf("transcript line %q", line)
	}
	mustEnd(t, ended, "cancelled queued job")
	if r.Cancel(q.ID, "bob") {
		t.Fatal("cancelling it twice must return false")
	}
	close(release)
	waitDone(t, r, first.ID)
	select {
	case <-ran:
		t.Fatal("a cancelled queued job must never run")
	case <-time.After(50 * time.Millisecond):
	}
	waitEvents(t, c, 3)
	if names := c.names(); strings.Join(names, ",") != "job.started,job.cancelled,job.succeeded" {
		t.Fatalf("events %v", names)
	}
}

// negative control: drop `defer r.pump()` from run's section 3 → nothing dispatches when a slot
// frees, the queued job is stranded and waitDone reports "job … never finished".
func TestQueuedJobSurvivesACrashedPredecessor(t *testing.T) {
	r := New(events.New(), 0)
	gate := make(chan struct{})
	crash, _ := r.Start("pr-1", Up, func(log.Sink, context.Context) (stack.Outcome, error) {
		<-gate
		panic("boom")
	}, nil)
	next, _ := r.Start("pr-1", Verify, okWork(stack.Outcome{OK: true}), nil)
	if next.State != Queued {
		t.Fatalf("next %+v", next)
	}
	ended, _ := endWatcher(t, r, next.ID)
	close(gate)
	if got := waitState(t, r, crash.ID, Failed); got.Error == nil {
		t.Fatal("the crash was not recorded")
	}
	// A failed predecessor is not a verdict on its successor: it runs, on its own merits.
	done := waitDone(t, r, next.ID)
	if done.State != OK || done.StartedAt == nil {
		t.Fatalf("successor %+v", done)
	}
	mustEnd(t, ended, "successor of a crashed job")
}

// negative control: empty the `preempts` table → the down queues behind the running up, the up is
// never cancelled and "up not cancelled by the down" fires.
func TestDownPreemptsRunningAndDropsQueued(t *testing.T) {
	bus := events.New()
	r := New(bus, 0)
	// letGo keeps the cancelled `up` inside its work function after the context ends, so the down
	// is observably QUEUED — a hook that traps SIGTERM does exactly this, at a longer timescale.
	letGo := make(chan struct{})
	up := func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Step, "creating the database branch")
		<-ctx.Done()
		<-letGo
		return stack.Outcome{OK: false, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseUp, Code: 130}}}, nil
	}
	u, _ := r.Start("pr-1", Up, up, nil)
	if u.State != Running {
		t.Fatalf("up %+v", u)
	}
	v, _ := r.Start("pr-1", Verify, okWork(stack.Outcome{OK: true}), nil)
	if v.State != Queued {
		t.Fatalf("verify %+v", v)
	}
	vEnded, _ := endWatcher(t, r, v.ID)

	d, ok := r.Start("pr-1", Down, okWork(stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseAssertGone, OK: true}}}), nil)
	if !ok {
		t.Fatal("a down must always be accepted")
	}
	// It cannot literally run until the cancelled job's work returns — but nothing is ahead of it.
	if d.State != Queued {
		t.Fatalf("down %+v", d)
	}

	// The queued deploy is dropped, never run.
	jv := waitTerminal(t, r, v.ID)
	if jv.State != Cancelled || jv.StartedAt != nil {
		t.Fatalf("dropped queued job %+v", jv)
	}
	mustEnd(t, vEnded, "dropped queued job")

	// The running deploy is cancelled — and the line that says the stack is now half-built must
	// still reach the transcript on THIS path.
	close(letGo)
	ju := waitState(t, r, u.ID, Cancelled)
	if ju.CancelledBy == nil || *ju.CancelledBy != "a down for this stack" {
		t.Fatalf("cancelledBy %v", ju.CancelledBy)
	}
	last := ju.Log[len(ju.Log)-1]
	if last.Message != "cancelled by a down for this stack — whatever ran before this point was NOT undone. Run verify to see what exists." {
		t.Fatalf("cancel line %q", last.Message)
	}

	// And the teardown runs.
	jd := waitDone(t, r, d.ID)
	if jd.State != OK || jd.StartedAt == nil {
		t.Fatalf("down %+v", jd)
	}
	if _, ok := r.Get(v.ID); !ok {
		t.Fatal("the dropped job's record must survive for its caller to poll")
	}
}

// negative control: drop the `if r.inFlight >= r.maxRunning { break }` guard in pump → all four
// jobs run at once and "3 running" fires.
func TestGlobalCapCapsAndIsFIFOAcrossStacks(t *testing.T) {
	r := New(events.New(), 2)
	// One channel per job: a job released is not a job finished, and every assertion below is about
	// which job holds a slot at a given moment.
	rel := []chan struct{}{}
	ids := []Job{}
	for _, s := range []string{"a", "b", "c", "d"} {
		ch := make(chan struct{})
		rel = append(rel, ch)
		j, ok := r.Start(s, Up, blockWork(ch), nil)
		if !ok {
			t.Fatalf("%s refused", s)
		}
		ids = append(ids, j)
	}
	// pump runs inline in Start, so the split is deterministic — no polling needed.
	running, queued := 0, 0
	for _, j := range r.List() {
		switch j.State {
		case Running:
			running++
		case Queued:
			queued++
		}
	}
	if running != 2 || queued != 2 {
		t.Fatalf("%d running, %d queued — the cap is 2", running, queued)
	}
	if ids[0].State != Running || ids[1].State != Running || ids[2].State != Queued || ids[3].State != Queued {
		t.Fatalf("the first two accepted must be the two that run: %v", []State{ids[0].State, ids[1].State, ids[2].State, ids[3].State})
	}

	// Freeing ONE slot dispatches exactly one job, and it is the older of the two waiting.
	close(rel[0])
	waitState(t, r, ids[2].ID, Running)
	if s := stateOf(t, r, ids[3].ID); s != Queued {
		t.Fatalf("one freed slot started two jobs (d is %s)", s)
	}
	close(rel[1])
	waitState(t, r, ids[3].ID, Running)
	close(rel[2])
	close(rel[3])
	for _, j := range ids {
		waitDone(t, r, j.ID)
	}
	// c started before d: acceptance order across stacks, not map order.
	c3, _ := r.Get(ids[2].ID)
	d4, _ := r.Get(ids[3].ID)
	if c3.StartedAt == nil || d4.StartedAt == nil || *c3.StartedAt > *d4.StartedAt {
		t.Fatalf("FIFO broken: c started %v, d started %v", c3.StartedAt, d4.StartedAt)
	}
}

// negative control: replace pump's `continue` on a busy stack with `break` → the freed slot is
// never handed to pr-3, because pr-1's queued job sits at the head of the queue and its stack is
// still busy. waitState times out with "job … is queued, wanted running".
func TestBusyStackDoesNotStarveOthers(t *testing.T) {
	r := New(events.New(), 2)
	// Both slots busy, one per stack.
	relA, relB := make(chan struct{}), make(chan struct{})
	hogA, _ := r.Start("pr-1", Up, blockWork(relA), nil)
	hogB, _ := r.Start("pr-2", Up, blockWork(relB), nil)
	// pr-1 queues ANOTHER job for itself, accepted before pr-3's — and its stack stays busy for the
	// whole test. It is the head of the queue and it can never use a slot.
	again, _ := r.Start("pr-1", Up, okWork(stack.Outcome{OK: true}), nil)
	relC := make(chan struct{})
	third, _ := r.Start("pr-3", Up, blockWork(relC), nil)
	if again.State != Queued || third.State != Queued {
		t.Fatalf("both slots are taken: %s %s", again.State, third.State)
	}

	// Free ONE slot, on a stack that is not pr-1.
	close(relB)
	waitDone(t, r, hogB.ID)
	// It must go to pr-3. A stack that cannot use a slot must not block the stacks that can.
	waitState(t, r, third.ID, Running)
	if s := stateOf(t, r, again.ID); s != Queued {
		t.Fatalf("pr-1 ran a second job while its first was still going (%s)", s)
	}

	close(relA)
	close(relC)
	waitDone(t, r, hogA.ID)
	waitDone(t, r, third.ID)
	waitDone(t, r, again.ID)
}

// negative control: in CancelStack's critical section, drop the `r.queue[stack]` branch → the
// queued job survives a cancel-everything and later runs.
func TestCancelStackShapes(t *testing.T) {
	// negative control: `var acted []Job` instead of `acted = []Job{}` → it returns nil and the
	// non-nil check fires (rule 3: never null where a caller expects a list).
	t.Run("nothing outstanding", func(t *testing.T) {
		r := New(events.New(), 0)
		if got := r.CancelStack("idle", "alice"); len(got) != 0 {
			t.Fatalf("acted on %v", got)
		}
		if got := r.CancelStack("idle", "alice"); got == nil {
			t.Fatal("must return an empty slice, never nil (rule 3)")
		}
	})

	// negative control: drop the `r.queue[stack]` branch in clearStackLocked → nothing is acted on
	// and the queued job later runs.
	t.Run("only queued", func(t *testing.T) {
		// One slot, held by another stack: pr-9's job is queued with NOTHING running for it.
		r := New(events.New(), 1)
		release := make(chan struct{})
		hog, _ := r.Start("other", Up, blockWork(release), nil)
		q, _ := r.Start("pr-9", Up, okWork(stack.Outcome{OK: true}), nil)
		if q.State != Queued {
			t.Fatalf("q %+v", q)
		}
		ended, _ := endWatcher(t, r, q.ID)
		acted := r.CancelStack("pr-9", "alice")
		if len(acted) != 1 || acted[0].ID != q.ID || acted[0].State != Cancelled {
			t.Fatalf("acted %+v", acted)
		}
		mustEnd(t, ended, "queued-only CancelStack")
		if r.IsBusy("pr-9") {
			t.Fatal("pr-9 still busy")
		}
		close(release)
		waitDone(t, r, hog.ID)
		if stateOf(t, r, q.ID) != Cancelled {
			t.Fatal("the cancelled job ran anyway")
		}
	})

	// negative control: append the queued job before the running one → the ordering assertion fires.
	t.Run("running and queued", func(t *testing.T) {
		r := New(events.New(), 0)
		release := make(chan struct{})
		run, _ := r.Start("pr-1", Up, blockWork(release), nil)
		q, _ := r.Start("pr-1", Verify, okWork(stack.Outcome{OK: true}), nil)
		acted := r.CancelStack("pr-1", "alice")
		if len(acted) != 2 || acted[0].ID != run.ID || acted[1].ID != q.ID {
			t.Fatalf("acted %+v — the running job comes first", acted)
		}
		if acted[0].State != Running {
			t.Fatal("the running job's record still says running: its own goroutine ends it")
		}
		waitTerminal(t, r, q.ID)
		got := waitState(t, r, run.ID, Cancelled)
		if got.CancelledBy == nil || *got.CancelledBy != "alice" {
			t.Fatalf("cancelledBy %v", got.CancelledBy)
		}
		close(release)
		// waitDone, not a bare IsBusy: the terminal state is assigned in section 1 and the stack
		// released in section 3, and this test is not allowed to guess which one has happened.
		waitDone(t, r, run.ID)
	})

	// negative control: read `cancelled := e.ctx.Err() != nil` before run's section 1 takes mu → a
	// cancel landing in that window stamps cancelledBy on a job filed as `ok`.
	t.Run("mid-finish", func(t *testing.T) {
		// The job's work has RETURNED and `run` is somewhere in its four critical sections. A
		// cancel-everything landing in that window must neither deadlock nor resurrect it.
		for i := 0; i < 50; i++ {
			r := New(events.New(), 0)
			returned := make(chan struct{})
			j, _ := r.Start("pr-1", Up, func(log.Sink, context.Context) (stack.Outcome, error) {
				close(returned)
				return stack.Outcome{OK: true}, nil
			}, nil)
			<-returned
			r.CancelStack("pr-1", "alice")
			done := waitDone(t, r, j.ID)
			if done.State != OK && done.State != Cancelled {
				t.Fatalf("iteration %d: %s", i, done.State)
			}
			if done.EndedAt == nil {
				t.Fatalf("iteration %d: no endedAt", i)
			}
			// cancelledBy and `cancelled` are one decision, taken together under mu: a job filed as
			// `ok` was not stopped by anybody, whichever section the cancel landed in.
			if done.State == OK && done.CancelledBy != nil {
				t.Fatalf("iteration %d: state ok but cancelledBy %q", i, *done.CancelledBy)
			}
			if done.State == Cancelled && done.CancelledBy == nil {
				t.Fatalf("iteration %d: cancelled by nobody", i)
			}
		}
	})
}

// negative control: let evictLocked drop jobs on `State != Running` instead of Terminal() → the
// queued job is evicted and "queued job evicted" fires.
func TestEvictionSparesRunningAndQueued(t *testing.T) {
	r := New(events.New(), 0)
	release := make(chan struct{})
	first, _ := r.Start("hold", Up, blockWork(release), nil)
	queued, _ := r.Start("hold", Up, okWork(stack.Outcome{OK: true}), nil)
	if queued.State != Queued {
		t.Fatalf("queued %+v", queued)
	}
	for i := 0; i < MaxJobs+10; i++ {
		j, ok := r.Start("fill", Up, okWork(stack.Outcome{OK: true}), nil)
		if !ok {
			t.Fatal("fill refused")
		}
		waitDone(t, r, j.ID)
	}
	// TS parity: evict runs at Start, so the two non-terminal jobs plus the newest (itself
	// non-terminal at that moment) survive on top of MaxJobs-1 finished ones.
	if n := len(r.List()); n > MaxJobs+2 {
		t.Fatalf("%d jobs kept", n)
	}
	if _, ok := r.Get(first.ID); !ok {
		t.Fatal("running job evicted")
	}
	if j, ok := r.Get(queued.ID); !ok || j.State != Queued {
		t.Fatalf("queued job evicted (%v %+v)", ok, j)
	}
	list := r.List()
	for i := 1; i < len(list); i++ {
		if newerFirst(list[i], list[i-1]) {
			t.Fatal("List not newest-first")
		}
	}
	close(release)
	waitDone(t, r, first.ID)
	waitDone(t, r, queued.ID)
}

// negative control: return the internal slice from Get → mutating the copy changes the record.
func TestGetReturnsCopies(t *testing.T) {
	r := New(events.New(), 0)
	j, _ := r.Start("s", Up, okWork(stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "a"}}}), nil)
	done := waitDone(t, r, j.ID)
	done.Outcome.Steps[0].Axis = "mutated"
	done.Log[0].Message = "mutated"
	again, _ := r.Get(j.ID)
	if again.Outcome.Steps[0].Axis == "mutated" || again.Log[0].Message == "mutated" {
		t.Fatal("Get leaked internal state")
	}
	b, _ := json.Marshal(again)
	// Key order is the reference's assignment order: …startedAt, log, outcome, endedAt.
	if !regexp.MustCompile(`^\{"id":"[^"]+","stack":"s","action":"up","state":"ok","startedAt":\d+,"log":\[\{"seq":1,.*\],"outcome":\{"ok":true,.*\},"endedAt":\d+\}$`).Match(b) {
		t.Fatalf("wire shape %s", b)
	}
}

// negative control: give Job.StartedAt an `omitempty` (or make it a plain int64) → the key vanishes
// (or reads 0) and the queued wire-shape regexp fails. A tri-state is a pointer WITHOUT omitempty:
// null is not absent, and it is certainly not 1970.
func TestQueuedWireShapeSaysStartedAtNull(t *testing.T) {
	r := New(events.New(), 0)
	release := make(chan struct{})
	first, _ := r.Start("s", Up, blockWork(release), nil)
	q, _ := r.Start("s", Verify, okWork(stack.Outcome{OK: true}), nil)
	got, _ := r.Get(q.ID)
	b, _ := json.Marshal(got)
	if !regexp.MustCompile(`^\{"id":"[^"]+","stack":"s","action":"verify","state":"queued","startedAt":null,"log":\[\]\}$`).Match(b) {
		t.Fatalf("queued wire shape %s", b)
	}
	if got.Stub().State != Queued {
		t.Fatalf("stub %+v", got.Stub())
	}

	r.Cancel(q.ID, "alice")
	dead := waitTerminal(t, r, q.ID)
	b, _ = json.Marshal(dead)
	// Terminal, never started: startedAt stays null and endedAt is there.
	if !regexp.MustCompile(`^\{"id":"[^"]+","stack":"s","action":"verify","state":"cancelled","startedAt":null,"log":\[.*\],"cancelledBy":"alice","endedAt":\d+\}$`).Match(b) {
		t.Fatalf("cancelled-while-queued wire shape %s", b)
	}
	close(release)
	waitDone(t, r, first.ID)
}

// negative control: sort List by StartedAt alone → two jobs started in the same millisecond come
// out in map order, and this fails about half the time.
func TestListTiebreakIsInsertionOrder(t *testing.T) {
	r := New(events.New(), 0)
	ids := []string{}
	for i := 0; i < 20; i++ {
		j, _ := r.Start("s"+string(rune('a'+i)), Up, okWork(stack.Outcome{OK: true}), nil)
		ids = append(ids, j.ID)
	}
	for _, id := range ids {
		waitDone(t, r, id)
	}
	list := r.List()
	for i := 1; i < len(list); i++ {
		a, b := list[i-1], list[i]
		if a.sortAt() < b.sortAt() || (a.sortAt() == b.sortAt() && a.seq > b.seq) {
			t.Fatalf("order broken at %d: %s then %s", i, a.ID, b.ID)
		}
	}
}

// negative control: widen the mutex — e.g. call r.pump() from inside run's section 3 while mu is
// held — and this deadlocks rather than failing.
func TestConcurrentHammerLeavesNothingHeld(t *testing.T) {
	r := New(events.New(), 3)
	stacks := []string{"a", "b", "c", "d", "e"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	for _, s := range stacks {
		for k := 0; k < 12; k++ {
			wg.Add(1)
			go func(s string, k int) {
				defer wg.Done()
				action := Up
				if k%4 == 3 {
					action = Down // preempts: cancels whatever that stack is doing
				}
				j, ok := r.Start(s, action, okWork(stack.Outcome{OK: true, Steps: []stack.StepResult{}, Outputs: map[string]string{}}), nil)
				if !ok {
					return // a Hold was out
				}
				mu.Lock()
				ids[j.ID] = true
				mu.Unlock()
				if k%5 == 0 {
					r.Cancel(j.ID, "alice")
				}
				if k%7 == 0 {
					r.CancelStack(s, "alice")
				}
				if rel, ok := r.Hold(s); ok {
					rel()
				}
				_ = r.IsBusy(s)
				_ = r.List()
			}(s, k)
		}
	}
	wg.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending := 0
		mu.Lock()
		for id := range ids {
			if j, ok := r.Get(id); ok && !j.State.Terminal() {
				pending++
			}
		}
		mu.Unlock()
		if pending == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	for id := range ids {
		if j, ok := r.Get(id); ok && !j.State.Terminal() {
			mu.Unlock()
			t.Fatalf("job %s stuck in %s", id, j.State)
		}
	}
	mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight != 0 || len(r.live) != 0 || len(r.queue) != 0 || len(r.held) != 0 {
		t.Fatalf("registry not drained: inFlight=%d live=%d queue=%d held=%d", r.inFlight, len(r.live), len(r.queue), len(r.held))
	}
}

// A queued `down` outranks a later `up`, and this is the failure that made the carve-out necessary.
//
// Without it the contract eats itself: `down` preempts the running `up` — cancelling it, leaving the
// stack half torn down — and then waits its turn; an `up` a second later supersedes the teardown and
// redeploys over the wreckage. The operator who asked for a teardown got a 202, no teardown, and a
// stack in a state nobody chose. Depth-one last-write-wins is right for two deploys and catastrophic
// for a deploy displacing a teardown.
//
// negative control: delete the `preempts[old.job.Action] && !preempts[action]` branch in Start — the
// down reaches `superseded` and the second up is accepted.
func TestAQueuedDownIsNotSupersededByALaterUp(t *testing.T) {
	r := New(events.New(), 1)
	release := make(chan struct{})
	hold := func(log.Sink, context.Context) (stack.Outcome, error) { <-release; return stack.Outcome{OK: true}, nil }

	up1, ok := r.Start("s", Up, hold, nil)
	if !ok {
		t.Fatal("the first up was refused")
	}
	// `down` preempts: it cancels up1 and takes the queue slot.
	down, ok := r.Start("s", Down, hold, nil)
	if !ok {
		t.Fatal("the down was refused")
	}
	// A later `up` must NOT displace it.
	if _, ok := r.Start("s", Up, hold, nil); ok {
		t.Fatal("an up was accepted over a waiting down — it would redeploy over a half-torn-down stack")
	}
	if j, _ := r.Get(down.ID); j.State == Superseded {
		t.Fatalf("the teardown was superseded: %+v", j)
	}
	close(release)
	d := waitDone(t, r, down.ID)
	if d.State != OK {
		t.Fatalf("the teardown did not run: %s", d.State)
	}
	// up1 was cancelled by the preemption, which is the documented cost of preempting.
	if j, _ := r.Get(up1.ID); j.State != Cancelled {
		t.Fatalf("up1 = %s, want cancelled", j.State)
	}
	// A second down is still allowed to replace a waiting one — preempting does not mean frozen.
	r2 := New(events.New(), 1)
	if _, ok := r2.Start("s", Up, hold, nil); !ok {
		t.Fatal("setup")
	}
	if _, ok := r2.Start("s", Down, hold, nil); !ok {
		t.Fatal("first down refused")
	}
	if _, ok := r2.Start("s", Down, hold, nil); !ok {
		t.Fatal("a down must be able to replace a waiting down")
	}
}

// negative control: drop the r.pump() from SetMaxRunning → the raised cap frees a slot nobody
// claims, the waiting job stays queued and waitState times out with "job … is queued, wanted
// running".
func TestSetMaxRunning(t *testing.T) {
	t.Run("raising the cap dispatches a job that was waiting for a slot", func(t *testing.T) {
		// negative control: as above.
		r := New(events.New(), 1)
		relA, relB := make(chan struct{}), make(chan struct{})
		a, _ := r.Start("pr-1", Up, blockWork(relA), nil)
		b, _ := r.Start("pr-2", Up, blockWork(relB), nil)
		if a.State != Running || b.State != Queued {
			t.Fatalf("a cap of 1 runs one and queues one: %s %s", a.State, b.State)
		}
		// No new Start, and nothing has finished: the raise alone must dispatch b.
		r.SetMaxRunning(2)
		waitState(t, r, b.ID, Running)
		close(relA)
		close(relB)
		waitDone(t, r, a.ID)
		waitDone(t, r, b.ID)
	})

	t.Run("lowering the cap kills nothing; it applies at the next dispatch", func(t *testing.T) {
		// negative control: make SetMaxRunning cancel running jobs down to the new cap → the two
		// jobs that were already running are no longer running and the assertion fires.
		r := New(events.New(), 2)
		relA, relB := make(chan struct{}), make(chan struct{})
		a, _ := r.Start("pr-1", Up, blockWork(relA), nil)
		b, _ := r.Start("pr-2", Up, blockWork(relB), nil)
		if a.State != Running || b.State != Running {
			t.Fatalf("both must be running before the cap moves: %s %s", a.State, b.State)
		}
		r.SetMaxRunning(1)
		if sa, sb := stateOf(t, r, a.ID), stateOf(t, r, b.ID); sa != Running || sb != Running {
			t.Fatalf("lowering the cap cancelled a running job: %s %s", sa, sb)
		}
		// The new cap governs the NEXT dispatch, not the current one.
		c, _ := r.Start("pr-3", Up, okWork(stack.Outcome{OK: true}), nil)
		if c.State != Queued {
			t.Fatalf("the lowered cap did not apply to the next dispatch: %s", c.State)
		}
		// Two running against a cap of one: freeing ONE slot still leaves the host over the cap, so
		// c waits for the second.
		close(relA)
		waitDone(t, r, a.ID)
		if s := stateOf(t, r, c.ID); s != Queued {
			t.Fatalf("dispatched while inFlight was still at the cap: %s", s)
		}
		close(relB)
		waitDone(t, r, b.ID)
		waitDone(t, r, c.ID)
	})

	t.Run("a non-positive cap is the built-in default, as in New", func(t *testing.T) {
		// negative control: assign n without the `n <= 0` guard → maxRunning is 0, every job on the
		// host stops dispatching, and the assertion fires.
		r := New(events.New(), 1)
		r.SetMaxRunning(0)
		r.mu.Lock()
		got := r.maxRunning
		r.mu.Unlock()
		if got != DefaultMaxRunning {
			t.Fatalf("SetMaxRunning(0) left the cap at %d", got)
		}
	})
}
