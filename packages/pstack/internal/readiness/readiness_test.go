package readiness

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
)

// A docker whose inspect answer the test REWRITES between polls — health transitions and crash
// loops are the whole subject, and a fixed answer could only prove the first observation.
type mutableDocker struct {
	mu       sync.Mutex
	ids      string
	state    string
	health   string
	exit     int64
	restarts int64
}

func (d *mutableDocker) set(state, health string, exit, restarts int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state, d.health, d.exit, d.restarts = state, health, exit, restarts
}

func (d *mutableDocker) empty() { d.mu.Lock(); d.ids = ""; d.mu.Unlock() }

func (d *mutableDocker) runner() exec.Runner {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if strings.HasPrefix(cmd, "docker ps -aq") {
			return exec.Result{OK: true, Stdout: d.ids}, true
		}
		if strings.HasPrefix(cmd, "docker inspect") {
			st := map[string]any{"Status": d.state, "ExitCode": d.exit}
			if d.health != "" {
				st["Health"] = map[string]any{"Status": d.health}
			}
			b, _ := json.Marshal([]any{map[string]any{"Id": "c1", "Name": "/probe-app-1", "RestartCount": d.restarts, "Config": map[string]any{"Image": "nginx", "Labels": map[string]string{"com.docker.compose.service": "app", "com.docker.compose.project": "probe"}}, "State": st, "NetworkSettings": map[string]any{"Networks": map[string]any{"preview-ingress": map[string]any{"IPAddress": "172.20.0.5"}}, "Ports": map[string]any{}}}})
			return exec.Result{OK: true, Stdout: string(b)}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func newDocker() *mutableDocker { return &mutableDocker{ids: "c1\n", state: "running"} }

type capture struct {
	mu   sync.Mutex
	seen []events.Event
	off  func()
}

func captureOn(bus *events.Bus) *capture {
	c := &capture{}
	c.off = bus.On(func(e events.Event) { c.mu.Lock(); c.seen = append(c.seen, e); c.mu.Unlock() })
	return c
}

func (c *capture) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{}
	for _, e := range c.seen {
		out = append(out, e.Event)
	}
	return out
}

func (c *capture) data(name string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.seen {
		if e.Event == name {
			var m map[string]any
			_ = json.Unmarshal(e.Data, &m)
			return m
		}
	}
	return nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func newWatcher(bus *events.Bus, timeoutMs int64) *Watcher {
	return New(Options{PollMs: 20, TimeoutMs: timeoutMs, Bus: bus})
}

func TestNoHealthcheckIsReadyWhenRunningAndSaysNobodyProbed(t *testing.T) {
	// negative control: make readinessOf require health == "healthy" for ready — a plain running container never settles.
	bus := events.New()
	c := captureOn(bus)
	defer c.off()
	d := newDocker()
	ws := newWatcher(bus, 5000)
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	s, _ := ws.Wait("probe", 3000)
	if s.State != Ready || len(s.Containers) != 1 || !s.Containers[0].Ready || s.Containers[0].HasHealthcheck {
		t.Fatalf("got %+v", s)
	}
	names := c.names()
	if !has(names, "container.ready") || !has(names, "stack.ready") || has(names, "healthcheck.started") {
		t.Errorf("events: %v", names)
	}
	if d := c.data("container.ready"); d["hasHealthcheck"] != false {
		t.Errorf("container.ready must say nobody probed: %v", d)
	}
}

func TestAHealthcheckIsNarratedStartedUpdatedFinished(t *testing.T) {
	// negative control: drop the `prev == nil` branch in healthEvents — "started" is never emitted for a first observation.
	bus := events.New()
	c := captureOn(bus)
	defer c.off()
	d := newDocker()
	d.set("running", "starting", 0, 0)
	ws := newWatcher(bus, 5000)
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	time.Sleep(80 * time.Millisecond)
	if s, _ := ws.Get("probe"); s.State != Watching {
		t.Fatalf("a starting healthcheck is still converging: %+v", s)
	}
	d.set("running", "healthy", 0, 0)
	s, _ := ws.Wait("probe", 3000)
	if s.State != Ready {
		t.Fatalf("got %+v", s)
	}
	names := c.names()
	want := []string{"healthcheck.started", "healthcheck.updated", "healthcheck.finished", "container.ready", "stack.ready"}
	for _, w := range want {
		if !has(names, w) {
			t.Errorf("missing %s in %v", w, names)
		}
	}
	if u := c.data("healthcheck.updated"); u["previous"] != "starting" || u["status"] != "healthy" {
		t.Errorf("updated: %v", u)
	}
	if f := c.data("healthcheck.finished"); f["healthy"] != true {
		t.Errorf("finished: %v", f)
	}
}

func TestACrashOnBootFailsButAOneShotThatExitsZeroDoesNot(t *testing.T) {
	// negative control: treat every exited container as failed — the migration container fails the stack.
	bus := events.New()
	c := captureOn(bus)
	defer c.off()
	d := newDocker()
	d.set("exited", "", 1, 0)
	ws := newWatcher(bus, 5000)
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	s, _ := ws.Wait("probe", 3000)
	if s.State != Failed || !s.Containers[0].Failed || *s.Containers[0].Reason != "exited with code 1" {
		t.Fatalf("got %+v", s)
	}
	if d := c.data("container.start-failed"); d["reason"] != "exited with code 1" {
		t.Errorf("start-failed: %v", d)
	}
	if sf := c.data("stack.failed"); sf["ready"] != float64(0) || len(sf["failedContainers"].([]any)) != 1 {
		t.Errorf("stack.failed: %v", sf)
	}

	d2 := newDocker()
	d2.set("exited", "", 0, 0)
	ws2 := newWatcher(bus, 5000)
	ws2.Start("probe", d2.runner(), StartOptions{Emit: true})
	s2, _ := ws2.Wait("probe", 3000)
	if s2.State != Ready || !s2.Containers[0].Ready {
		t.Fatalf("a one-shot that exited 0 is ready: %+v", s2)
	}
}

func TestACrashLoopIsReportedASingleRestartForgiven(t *testing.T) {
	// negative control: set RestartLoop to 1 — the single restart is called a crash loop.
	bus := events.New()
	d := newDocker()
	d.set("running", "", 0, 1)
	ws := newWatcher(bus, 5000)
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	s, _ := ws.Wait("probe", 3000)
	if s.State != Ready {
		t.Fatalf("one restart is forgiven: %+v", s)
	}
	d2 := newDocker()
	d2.set("restarting", "", 137, 3)
	ws2 := newWatcher(bus, 5000)
	ws2.Start("probe", d2.runner(), StartOptions{Emit: true})
	s2, _ := ws2.Wait("probe", 3000)
	if s2.State != Failed || !strings.Contains(*s2.Containers[0].Reason, "crash loop") || !strings.Contains(*s2.Containers[0].Reason, "last exit 137") {
		t.Fatalf("got %+v", s2)
	}
}

func TestAHealthcheckThatNeverSettlesTimesOutAndNamesIt(t *testing.T) {
	// negative control: never call settle(TimedOut) from loop — Wait returns `watching` and no terminal event fires.
	bus := events.New()
	c := captureOn(bus)
	defer c.off()
	d := newDocker()
	d.set("running", "starting", 0, 0)
	ws := newWatcher(bus, 150)
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	s, _ := ws.Wait("probe", 3000)
	if s.State != TimedOut || s.EndedAt == nil {
		t.Fatalf("got %+v", s)
	}
	names := c.names()
	if !has(names, "healthcheck.timedout") || !has(names, "stack.timedout") {
		t.Errorf("events: %v", names)
	}
	if st := c.data("stack.timedout"); len(st["pendingContainers"].([]any)) != 1 {
		t.Errorf("the pending container must be named: %v", st)
	}
}

func TestEveryWatchEndsInExactlyOneTerminalEventAndASilentWatchEmitsNothing(t *testing.T) {
	// negative control: emit from a watch started with Emit:false — the silent read manufactures stack.timedout.
	bus := events.New()
	c := captureOn(bus)
	defer c.off()
	d := newDocker()
	d.empty()
	ws := newWatcher(bus, 100)
	ws.Start("probe", d.runner(), StartOptions{Emit: false})
	s, _ := ws.Wait("probe", 3000)
	if s.State != TimedOut {
		t.Fatalf("an empty stack never converges: %+v", s)
	}
	if len(c.names()) != 0 {
		t.Errorf("a silent watch emitted %v", c.names())
	}

	// Loud: exactly one terminal event, and a second waiter sees the same snapshot.
	d2 := newDocker()
	ws2 := newWatcher(bus, 5000)
	ws2.Start("probe", d2.runner(), StartOptions{Emit: true})
	var wg sync.WaitGroup
	results := make([]StackReadiness, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], _ = ws2.Wait("probe", 3000) }(i)
	}
	wg.Wait()
	if results[0].State != Ready || results[1].State != Ready {
		t.Fatalf("both waiters settle: %+v %+v", results[0], results[1])
	}
	terminal := 0
	for _, n := range c.names() {
		if strings.HasPrefix(n, "stack.") {
			terminal++
		}
	}
	if terminal != 1 {
		t.Errorf("exactly one terminal event, got %v", c.names())
	}
	// A restart after settling replaces the verdict rather than returning last hour's.
	d2.set("exited", "", 2, 0)
	ws2.Start("probe", d2.runner(), StartOptions{Emit: true, Restart: true})
	s3, _ := ws2.Wait("probe", 3000)
	if s3.State != Failed {
		t.Errorf("restart re-evaluates: %+v", s3)
	}
	ws2.StopAll()
}

func TestDockerNotAnsweringIsReportedAndTheWatchKeepsGoing(t *testing.T) {
	// negative control: settle(Failed) when !rt.Reachable — a daemon hiccup becomes a verdict.
	bus := events.New()
	f := exec.NewFake(func(string) bool { return true }, "")
	ws := newWatcher(bus, 120)
	ws.Start("probe", f, StartOptions{Emit: true})
	time.Sleep(60 * time.Millisecond)
	if s, _ := ws.Get("probe"); s.State != Watching || s.Reachable {
		t.Fatalf("unreachable yet still watching: %+v", s)
	}
	s, _ := ws.Wait("probe", 3000)
	if s.State != TimedOut || s.Reachable {
		t.Errorf("the deadline is what ends it: %+v", s)
	}
}

func TestRestartLoopThresholdIsConfigurable(t *testing.T) {
	// negative control: drop RestartLoop back to the default 3 — the three-restart container below is
	// then a crash loop and the first assertion fails. This is the knob a SWARM host turns up: with no
	// `depends_on`, a dependent restarts a few times while its database converges, and calling that a
	// crash loop fails the deploy.
	bus := events.New()
	loose := func(restarts int64) StackReadiness {
		d := newDocker()
		d.set("running", "", 0, restarts)
		ws := New(Options{PollMs: 20, TimeoutMs: 5000, RestartLoop: 6, Bus: bus})
		ws.Start("probe", d.runner(), StartOptions{Emit: true})
		s, _ := ws.Wait("probe", 3000)
		return s
	}
	if s := loose(3); s.State != Ready {
		t.Fatalf("3 restarts is under a threshold of 6: %+v", s)
	}
	if s := loose(6); s.State != Failed || !strings.Contains(*s.Containers[0].Reason, "crash loop") {
		t.Fatalf("6 restarts reaches a threshold of 6: %+v", s)
	}
	// A non-positive value is the default, not "never a crash loop".
	d := newDocker()
	d.set("running", "", 0, 3)
	ws := New(Options{PollMs: 20, TimeoutMs: 5000, RestartLoop: 0, Bus: bus})
	ws.Start("probe", d.runner(), StartOptions{Emit: true})
	s, _ := ws.Wait("probe", 3000)
	if s.State != Failed {
		t.Fatalf("RestartLoop 0 falls back to the default of %d: %+v", RestartLoop, s)
	}
}
