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

func waitDone(t *testing.T, r *Registry, id string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := r.Get(id)
		if ok && j.State != Running {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return Job{}
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
func (c *captured) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(c.ev[len(c.ev)-1].Data, &m)
	return m
}

// negative control: drop the `r.locks[stackName] = true` line in Start → second Start succeeds.
func TestPerStackLockAndIds(t *testing.T) {
	bus := events.New()
	r := New(bus)
	release := make(chan struct{})
	work := func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		<-release
		return stack.Outcome{OK: true, Steps: []stack.StepResult{}, Outputs: map[string]string{}}, nil
	}
	j, ok := r.Start("pr-1", Up, work, nil)
	if !ok {
		t.Fatal("first start refused")
	}
	if !regexp.MustCompile(`^up-pr-1-1-[a-z0-9]{6}$`).MatchString(j.ID) {
		t.Fatalf("id %q", j.ID)
	}
	if j.State != Running || j.Stack != "pr-1" || j.Action != Up {
		t.Fatalf("stub %+v", j)
	}
	if _, ok := r.Start("pr-1", Down, work, nil); ok {
		t.Fatal("second job on a busy stack must be refused")
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
	if _, ok := r.Start("pr-2", Up, work, nil); ok {
		t.Fatal("Start under Hold must be refused")
	}
	rel()
	if r.IsBusy("pr-2") {
		t.Fatal("release did not free pr-2")
	}
	close(release)
	done := waitDone(t, r, j.ID)
	if done.State != OK || done.EndedAt == nil || r.IsBusy("pr-1") {
		t.Fatalf("after finish: %+v busy=%v", done, r.IsBusy("pr-1"))
	}
	if _, ok := r.Start("pr-1", Down, okWork(stack.Outcome{OK: true}), nil); !ok {
		t.Fatal("lock not released after finish")
	}
}

// negative control: emit `job.started` before unlocking → the listener's IsBusy sees… the same, but
// swap the payload order (stack before jobId) → the key order assertion fails.
func TestStateFromOutcomeAndEventPayload(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus)
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
		{Down, stack.Outcome{OK: false, Steps: []stack.StepResult{assertLeak}}, Leaked, "job.leaked", []any{"db"}, true},
		{Down, stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseAssertGone, OK: true}}}, OK, "job.succeeded", []any{}, true},
		{Down, stack.Outcome{OK: true, Steps: []stack.StepResult{{Axis: "db", Phase: stack.PhaseAssertGone, OK: true, Skipped: true}}}, OK, "job.succeeded", []any{}, false},
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
		done := waitDone(t, r, j.ID)
		if done.State != tc.state {
			t.Fatalf("case %d: state %s want %s", i, done.State, tc.state)
		}
		if done.Outcome == nil || done.Outcome.Steps == nil || done.Outcome.Outputs == nil || done.Log == nil {
			t.Fatalf("case %d: nil collections in %+v", i, done)
		}
		names := c.names()
		if len(names) != 2 || names[0] != "job.started" || names[1] != tc.event {
			t.Fatalf("case %d: events %v", i, names)
		}
		raw := string(c.ev[1].Data)
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
	r := New(bus)
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
	p := c.last()
	if p["error"] != "boom ***" || p["state"] != "failed" {
		t.Fatalf("crash event %v", p)
	}
	if r.IsBusy("s") {
		t.Fatal("lock leaked after crash")
	}
}

// negative control: make finish fan out `__end__` while holding mu → deadlock (the -race run
// hangs); or skip step 4 → the subscriber never sees the end frame.
func TestCancelWakesSubscriber(t *testing.T) {
	bus := events.New()
	c := &captured{}
	bus.On(c.on)
	r := New(bus)
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
	// The bus event is step 5 of finish, after the __end__ fan-out: wait for it.
	for i := 0; i < 200 && len(c.names()) < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
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

// negative control: let evictLocked drop running jobs → the 51st running job disappears.
func TestEvictionNeverRemovesRunning(t *testing.T) {
	r := New(events.New())
	release := make(chan struct{})
	hold := func(log.Sink, context.Context) (stack.Outcome, error) {
		<-release
		return stack.Outcome{OK: true}, nil
	}
	first, _ := r.Start("hold", Up, hold, nil)
	for i := 0; i < MaxJobs+10; i++ {
		j, ok := r.Start("fill", Up, okWork(stack.Outcome{OK: true}), nil)
		if !ok {
			t.Fatal("fill refused")
		}
		waitDone(t, r, j.ID)
	}
	// TS parity: evict runs at Start, so one running + the newest (itself running) job → MaxJobs+1.
	if n := len(r.List()); n > MaxJobs+1 {
		t.Fatalf("%d jobs kept", n)
	}
	if _, ok := r.Get(first.ID); !ok {
		t.Fatal("running job evicted")
	}
	list := r.List()
	for i := 1; i < len(list); i++ {
		if list[i-1].StartedAt < list[i].StartedAt {
			t.Fatal("List not newest-first")
		}
	}
	close(release)
	waitDone(t, r, first.ID)
}

// negative control: return the internal slice from Get → mutating the copy changes the record.
func TestGetReturnsCopies(t *testing.T) {
	r := New(events.New())
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

// negative control: sort List by StartedAt alone → two jobs started in the same millisecond come
// out in map order, and this fails about half the time.
func TestListTiebreakIsInsertionOrder(t *testing.T) {
	r := New(events.New())
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
		if a.StartedAt < b.StartedAt || (a.StartedAt == b.StartedAt && a.seq > b.seq) {
			t.Fatalf("order broken at %d: %s then %s", i, a.ID, b.ID)
		}
	}
}
