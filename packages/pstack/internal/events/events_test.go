package events

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

func TestNames(t *testing.T) {
	t.Run("the EVENTS list, in order, add-only", func(t *testing.T) {
		// negative control: swap "job.failed" and "job.cancelled" — the order assertion fails.
		want := "deployment.created,deployment.updated,deployment.deleted,job.started,job.succeeded,job.failed,job.cancelled,job.leaked,spec.stored,spec.deleted,routing.changed,healthcheck.started,healthcheck.updated,healthcheck.finished,healthcheck.timedout,container.ready,container.start-failed,container.started,container.stopped,container.restarted,stack.ready,stack.failed,stack.timedout,stack.slept,stack.woken,share.created,config.exported,config.imported"
		if got := strings.Join(Names, ","); got != want {
			t.Fatalf("Names = %s", got)
		}
	})

	t.Run("isEventName and isSubscribable", func(t *testing.T) {
		// negative control: make IsSubscribable ignore Wildcard — "*" is refused.
		if !IsEventName("job.leaked") || IsEventName("job.exploded") || IsEventName(42) || IsEventName(nil) {
			t.Fatal("IsEventName")
		}
		if !IsSubscribable("*") || !IsSubscribable("job.leaked") || IsSubscribable("**") || IsSubscribable(true) {
			t.Fatal("IsSubscribable")
		}
	})
}

func TestEnvelope(t *testing.T) {
	t.Run("the id is evt_<base36 ms>_<base36 seq>_<6 base36 chars> and seq increments", func(t *testing.T) {
		// negative control: drop the seq increment — both ids carry `_1_`.
		b := New()
		a := b.Emit("job.started", nil)
		c := b.Emit("job.started", nil)
		re := regexp.MustCompile(`^evt_[0-9a-z]+_([0-9a-z]+)_[0-9a-z]{6}$`)
		ma, mc := re.FindStringSubmatch(a.ID), re.FindStringSubmatch(c.ID)
		if ma == nil || mc == nil {
			t.Fatalf("ids %q %q", a.ID, c.ID)
		}
		if ma[1] != "1" || mc[1] != "2" {
			t.Fatalf("seq %q then %q", ma[1], mc[1])
		}
		if a.At <= 0 {
			t.Fatalf("at = %d", a.At)
		}
	})

	t.Run("the envelope is exactly {id,event,at,data}; replay is not a body field", func(t *testing.T) {
		// negative control: give Replay a json tag — a fifth field appears.
		b := New()
		e := b.Emit("job.leaked", struct {
			JobID string   `json:"jobId"`
			Axes  []string `json:"leakedAxes"`
		}{"j1", []string{"db"}})
		e.Replay = true
		got := string(jsonx.Must(e))
		want := `{"id":"` + e.ID + `","event":"job.leaked","at":` + jsonx.NumberString(float64(e.At)) + `,"data":{"jobId":"j1","leakedAxes":["db"]}}`
		if got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
	})

	t.Run("data is marshalled once by jsonx, in order, and nil is {}", func(t *testing.T) {
		// negative control: marshal with encoding/json.Marshal — `<` becomes <.
		b := New()
		e := b.Emit("routing.changed", omap.From("file", "a<b", "action", "created"))
		if string(e.Data) != `{"file":"a<b","action":"created"}` {
			t.Fatalf("data = %s", e.Data)
		}
		if n := b.Emit("spec.deleted", nil); string(n.Data) != "{}" {
			t.Fatalf("nil data = %s", n.Data)
		}
	})
}

type payload struct {
	Emitter int `json:"emitter"`
	N       int `json:"n"`
}

// recorder is a listener that notes every payload; its own mutex so the `go fn(e)` negative
// control fails on ORDER, not on a data race.
type recorder struct {
	mu   sync.Mutex
	seen []payload
}

func (r *recorder) listen(e Event) {
	var p payload
	_ = json.Unmarshal(e.Data, &p)
	r.mu.Lock()
	r.seen = append(r.seen, p)
	r.mu.Unlock()
}

func TestBus(t *testing.T) {
	t.Run("a listener that throws — synchronously or asynchronously — cannot reach the emitter", func(t *testing.T) {
		// negative control: remove the recover in call — the test binary panics.
		/*
		 * `emit` claims it "cannot throw, by construction". In Go the asynchronous half is the
		 * listener's own job: a goroutine it starts must carry its own recover (the bus cannot reach
		 * into it), so this exercises the synchronous guarantee and a well-behaved async listener.
		 */
		b := New()
		seen := []string{}
		var wg sync.WaitGroup
		offSync := b.On(func(Event) { panic("sync listener is broken") })
		offAsync := b.On(func(Event) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = recover() }()
				panic("async listener is broken")
			}()
		})
		offGood := b.On(func(e Event) { seen = append(seen, e.Event) })
		defer func() {
			offSync()
			offAsync()
			offGood()
		}()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("emit panicked: %v", r)
				}
			}()
			b.Emit("job.started", omap.From("jobId", "x"))
		}()
		wg.Wait()
		// …and a broken listener must not starve the ones registered after it.
		if strings.Join(seen, ",") != "job.started" {
			t.Fatalf("seen = %v", seen)
		}
	})

	t.Run("listeners fire in registration order, and off removes exactly one", func(t *testing.T) {
		// negative control: prepend instead of append in On — order reverses.
		b := New()
		order := []string{}
		offA := b.On(func(Event) { order = append(order, "a") })
		offB := b.On(func(Event) { order = append(order, "b") })
		b.On(func(Event) { order = append(order, "c") })
		b.Emit("job.started", nil)
		offB()
		offB() // idempotent
		b.Emit("job.started", nil)
		offA()
		b.Emit("job.started", nil)
		if got := strings.Join(order, ""); got != "abc"+"ac"+"c" {
			t.Fatalf("order = %s", got)
		}
	})

	t.Run("every listener receives every event in emit order per emitter, under concurrent emits", func(t *testing.T) {
		// negative control: `go call(l.fn, e)` in Emit — per-emitter order scrambles.
		const emitters, perEmitter = 8, 100
		b := New()
		recs := []*recorder{{}, {}, {}}
		for _, r := range recs {
			b.On(r.listen)
		}
		var wg sync.WaitGroup
		for i := 0; i < emitters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for n := 0; n < perEmitter; n++ {
					b.Emit("job.started", payload{i, n})
				}
			}(i)
		}
		wg.Wait()
		for li, r := range recs {
			if len(r.seen) != emitters*perEmitter {
				t.Fatalf("listener %d saw %d events", li, len(r.seen))
			}
			next := make([]int, emitters)
			for _, p := range r.seen {
				if p.N != next[p.Emitter] {
					t.Fatalf("listener %d: emitter %d event %d arrived before %d", li, p.Emitter, p.N, next[p.Emitter])
				}
				next[p.Emitter]++
			}
		}
	})

	t.Run("off() during dispatch is safe", func(t *testing.T) {
		// negative control: mutate the listener slice in place in off (`append(ls[:i], ls[i+1:]...)`)
		// — the race detector flags the snapshot being iterated.
		b := New()
		var offB, offSelf func()
		var calls sync.WaitGroup
		offB = b.On(func(Event) {})
		offSelf = b.On(func(Event) {
			offB()
			offSelf()
			// Re-register from inside a dispatch: no lock is held around the call, so no deadlock.
			b.On(func(Event) {})()
		})
		for i := 0; i < 8; i++ {
			calls.Add(1)
			go func() {
				defer calls.Done()
				for n := 0; n < 50; n++ {
					b.Emit("job.started", nil)
				}
			}()
		}
		calls.Wait()
		b.mu.RLock()
		n := len(b.listeners)
		b.mu.RUnlock()
		if n != 0 {
			t.Fatalf("%d listeners left", n)
		}
	})

	t.Run("the package-level Default bus", func(t *testing.T) {
		// negative control: make package On register on a fresh bus — nothing is seen.
		seen := 0
		off := On(func(Event) { seen++ })
		defer off()
		Emit("job.started", nil)
		if seen != 1 {
			t.Fatalf("seen = %d", seen)
		}
	})
}
