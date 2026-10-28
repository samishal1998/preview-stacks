package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

// The agent fixture's disk is 95.1 % used; this diskFree puts it at 80 %.
const (
	fixtureDiskFree = `"diskFree":5136490496`
	calmDiskFree    = `"diskFree":21030435226`
)

// pressureEvents records node.* events: name, then the payload as sent.
func pressureEvents(t *testing.T, s *Server) *[]string {
	t.Helper()
	seen := []string{}
	off := s.bus.On(func(e events.Event) {
		if strings.HasPrefix(e.Event, "node.") {
			seen = append(seen, e.Event+" "+string(e.Data))
		}
	})
	t.Cleanup(off)
	return &seen
}

const pressureWant = `{"id":"pressure/worker-1","node":null,"hostname":"worker-1","kinds":["disk"],` +
	`"disk":95.1,"memory":12.9,"cpu":0.1,"thresholds":{"disk":90,"memory":10,"cpu":2}}`

// negative control: emit on every tick while the set is non-empty → three node.pressure lines.
// negative control: serialise Node "" as "" → the payload's `"node":null` comparison fails (NP-R15).
// negative control: take the hostname from nodeMeta only → "" on compose, which has none.
func TestPressureEmitsOnEdgesOnly(t *testing.T) {
	s, logs := composeAgent(t)
	seen := pressureEvents(t, s)
	for range 3 {
		s.statsTick()
	}
	if want := []string{"node.pressure " + pressureWant}; !reflect.DeepEqual(*seen, want) {
		t.Fatalf("events after three 95%% ticks:\n got %q\nwant %q", *seen, want)
	}
	body, _ := statsRead(t, s)
	if p := statsNodes(t, body)[0]["pressure"]; !reflect.DeepEqual(p, []any{"disk"}) {
		t.Fatalf("/api/stats pressure = %#v, want [disk]", p)
	}
	if kinds, r, ok := s.inPressure("worker-1", s.statsNow()); !ok || !reflect.DeepEqual(kinds, []nodestats.Kind{nodestats.Disk}) || r.DiskUsedPct < 95 {
		t.Fatalf("inPressure = %v %+v %v", kinds, r, ok)
	}

	*logs = strings.ReplaceAll(*logs, fixtureDiskFree, calmDiskFree)
	for range 2 {
		s.statsTick()
	}
	want := `{"id":"pressure/worker-1","node":null,"hostname":"worker-1","kinds":[],` +
		`"disk":80,"memory":12.9,"cpu":0.1,"thresholds":{"disk":90,"memory":10,"cpu":2}}`
	if len(*seen) != 2 || (*seen)[1] != "node.recovered "+want {
		t.Fatalf("events after two 80%% ticks:\n got %q\nwant [.. %q]", *seen, "node.recovered "+want)
	}
	if _, _, ok := s.inPressure("worker-1", s.statsNow()); ok {
		t.Fatal("inPressure after recovery = true")
	}
	body, _ = statsRead(t, s)
	if p := statsNodes(t, body)[0]["pressure"]; !reflect.DeepEqual(p, []any{}) {
		t.Fatalf("/api/stats pressure after recovery = %#v, want []", p)
	}
}

// negative control: read the thresholds once and cache them → the second tick still sees 90 and
// emits nothing.
func TestPressureThresholdZeroRecovers(t *testing.T) {
	s, _ := composeAgent(t)
	seen := pressureEvents(t, s)
	s.statsTick()
	if err := s.settings.Set(settings.KeyPressureDiskPct, "0"); err != nil {
		t.Fatal(err)
	}
	s.statsTick()
	if len(*seen) != 2 || !strings.HasPrefix((*seen)[1], `node.recovered {"id":"pressure/worker-1"`) {
		t.Fatalf("events = %q, want node.pressure then node.recovered", *seen)
	}
	if !strings.Contains((*seen)[1], `"thresholds":{"disk":0,`) {
		t.Fatalf("recovered payload = %s, want the 0 threshold", (*seen)[1])
	}
}

// negative control: evaluate a stale node too (skip the Fresh check) → unknown reads as calm and
// node.recovered fires.
func TestPressureStaleIsSilent(t *testing.T) {
	s, _ := composeAgent(t)
	seen := pressureEvents(t, s)
	s.statsTick()
	if _, _, ok := s.inPressure("worker-1", s.statsNow()); !ok {
		t.Fatal("inPressure before stale = false")
	}
	// The same logs: no new batch, so the node's batch ages past StaleMs.
	s.statsNow = func() int64 { return 1_000_000 + nodestats.StaleMs + 1_000 }
	s.statsTick()
	if len(*seen) != 1 {
		t.Fatalf("events = %q, want only the first node.pressure", *seen)
	}
	if _, _, ok := s.inPressure("worker-1", s.statsNow()); ok {
		t.Fatal("inPressure on a stale node = true")
	}
	body, _ := statsRead(t, s)
	if p := statsNodes(t, body)[0]["pressure"]; !reflect.DeepEqual(p, []any{}) {
		t.Fatalf("stale /api/stats pressure = %#v, want []", p)
	}
}

// negative control: build the id from the hostname instead of the node key → `pressure/worker-1`.
func TestPressureSwarmPayload(t *testing.T) {
	s := statsServer(t)
	s.host = statsFake(map[string]string{
		statsInfoCmd:                     statsSwarmInfo,
		statsNodeLsCmd:                   statsNodeLs,
		nodestats.AgentLogsCmds(true)[1]: agentOutput(t),
	})
	seen := pressureEvents(t, s)
	s.statsTick()
	want := `node.pressure {"id":"pressure/` + statsWorkerID + `","node":"` + statsWorkerID + `","hostname":"worker-1",`
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], want) {
		t.Fatalf("events = %q, want one starting %s", *seen, want)
	}
}

// composeAgent is a compose host whose agent reports the fixture; the returned logs are what the next
// tick reads.
func composeAgent(t *testing.T) (*Server, *string) {
	t.Helper()
	s := statsServer(t)
	logs := strings.ReplaceAll(agentOutput(t), `"node":"`+statsWorkerID+`"`, `"node":""`)
	f := statsFake(map[string]string{statsInfoCmd: `{"LocalNodeState":"inactive","NodeID":"","ControlAvailable":false}`})
	answer := f.Answer
	f.Answer = func(cmd string) (r exec.Result, ok bool) {
		if strings.TrimSpace(cmd) == nodestats.AgentLogsCmds(false)[0] {
			return exec.Result{OK: true, Stdout: logs}, true
		}
		return answer(cmd)
	}
	s.host = f
	return s, &logs
}

const capNow = int64(1_000_000)

// capNode seeds one node's batch, as statsTick would leave it: 94 % disk when pressured, 50 %
// otherwise. at is when the batch arrived; meta is set only when availability is non-empty (swarm).
func capNode(s *Server, key, hostname, availability string, pressured bool, at int64) {
	free := int64(50)
	if pressured {
		free = 6
	}
	n := nodestats.NodeLine{Hostname: hostname, Cores: 2, MemTotal: 100, MemAvailable: 50, DiskTotal: 100, DiskFree: free}
	if availability != "" {
		n.Node = key
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Record(at, []nodestats.Batch{{Key: key, Raw: key + hostname, Node: n}})
	if availability != "" {
		s.onSwarm = true
		s.nodeMeta[key] = nodeMeta{Hostname: hostname, Role: "worker", Availability: availability}
	}
	if pressured {
		s.pressure[key] = []nodestats.Kind{nodestats.Disk}
	}
}

// negative control: refuse when ANY active node is in pressure → "one in pressure" is refused.
func TestCapacityRefusalSwarmRule(t *testing.T) {
	// Keys sort opposite to hostnames in "both in pressure", so first by hostname is not first by key.
	cases := []struct {
		name string
		seed func(s *Server)
		why  string
	}{
		// negative control: return refused when the active set is empty → this case is refused.
		{"no node data", func(s *Server) {}, ""},
		// negative control: stop at the first pressured node instead of the first free one → refused.
		{"one of two in pressure", func(s *Server) {
			capNode(s, "a", "worker-1", "active", true, capNow)
			capNode(s, "b", "worker-2", "active", false, capNow)
		}, ""},
		// negative control: sort the candidates by key → names worker-2.
		{"both in pressure", func(s *Server) {
			capNode(s, "a", "worker-2", "active", true, capNow)
			capNode(s, "b", "worker-1", "active", true, capNow)
		}, "worker-1: disk 94% ≥ 90%"},
		// negative control: count every listed node, drained or not → the drained calm node allows it.
		{"the free node drained", func(s *Server) {
			capNode(s, "a", "worker-2", "active", true, capNow)
			capNode(s, "b", "worker-1", "drain", false, capNow)
		}, "worker-2: disk 94% ≥ 90%"},
		// negative control: treat a stale node as busy → refused (invariant 11).
		{"the free node stale", func(s *Server) {
			capNode(s, "a", "worker-2", "active", true, capNow)
			capNode(s, "b", "worker-1", "active", false, capNow-nodestats.StaleMs-1_000)
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := statsServer(t)
			c.seed(s)
			why, refused := s.capacityRefusal(spec.Swarm, capNow)
			if refused != (c.why != "") || why != c.why {
				t.Fatalf("capacityRefusal = %q %v, want %q", why, refused, c.why)
			}
		})
	}
}

// negative control: apply the swarm rule to every orchestrator → a compose host has no nodeMeta, so
// self in pressure is allowed.
func TestCapacityRefusalCompose(t *testing.T) {
	// negative control: always return allowed → the self node in pressure is not refused.
	t.Run("self in pressure", func(t *testing.T) {
		s := statsServer(t)
		capNode(s, "worker-1", "worker-1", "", true, capNow)
		s.selfKey = "worker-1"
		why, refused := s.capacityRefusal(spec.Compose, capNow)
		if !refused || why != "worker-1: disk 94% ≥ 90%" {
			t.Fatalf("capacityRefusal = %q %v", why, refused)
		}
	})
	// negative control: refuse when any node is in pressure, not only self → the worker refuses it.
	t.Run("a worker in pressure only", func(t *testing.T) {
		s := statsServer(t)
		capNode(s, "n1", "mgr", "active", false, capNow)
		capNode(s, "n2", "worker-1", "active", true, capNow)
		s.selfKey = "n1"
		if why, refused := s.capacityRefusal(spec.Compose, capNow); refused {
			t.Fatalf("capacityRefusal = %q, want allowed", why)
		}
	})
}

// capServer is crServer with a docker that records every argv to the returned log, and the self node
// (worker-1, a compose host) in disk pressure.
func capServer(t *testing.T) (*Server, string) {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {},
		Env: exec.Merge(exec.Environ(), map[string]string{"PATH": bin + ":" + os.Getenv("PATH")})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	s.statsNow = func() int64 { return capNow }
	crDeploy(t, s, "pr-1", "myapp-pr-1", crCompose)
	capNode(s, "worker-1", "worker-1", "", true, capNow)
	s.selfKey = "worker-1"
	return s, calls
}

// capRun starts one lifecycle job on pr-1 and returns it once it has ended, with every event the
// bus carried meanwhile.
func capRun(t *testing.T, s *Server, action jobs.Action) (jobs.Job, []string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	off := s.bus.On(func(e events.Event) { mu.Lock(); seen = append(seen, e.Event); mu.Unlock() })
	defer off()
	dep, err := s.registry.Get("pr-1")
	if err != nil || dep == nil {
		t.Fatal(dep, err)
	}
	st, err := s.resolveDep("pr-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := s.startLifecycle("pr-1", dep, st, action, lifecycleOptions{By: "test"})
	if !ok {
		t.Fatal("not started")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		j, _ := s.jobs.Get(job.ID)
		if j.State != jobs.Queued && j.State != jobs.Running {
			s.readiness.Cancel(st.Stack)
			mu.Lock()
			defer mu.Unlock()
			return j, append([]string{}, seen...)
		}
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
	}
}

func capCalls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// negative control: drop the capacityRefusal guard from work → both subtests fail.
func TestUpRefusedUnderPressure(t *testing.T) {
	for _, action := range []jobs.Action{jobs.Up, jobs.Wake} {
		// negative control: drop the capacityRefusal guard from work → compose up runs and the job
		// succeeds.
		// negative control: put the guard after the stack.woken emit → the wake emits it.
		t.Run(string(action), func(t *testing.T) {
			s, calls := capServer(t)
			j, seen := capRun(t, s, action)
			why := "worker-1: disk 94% ≥ 90%"
			want := []stack.StepResult{{Axis: "myapp-pr-1", Phase: stack.PhaseCapacity, OK: false, Code: 1, Message: &why}}
			if j.State != jobs.Failed || j.Outcome == nil || !reflect.DeepEqual(j.Outcome.Steps, want) {
				t.Fatalf("job = %s %+v", j.State, j.Outcome)
			}
			if c := capCalls(t, calls); strings.Contains(c, "compose") {
				t.Fatalf("docker ran:\n%s", c)
			}
			if slices.Contains(seen, "stack.woken") || !slices.Contains(seen, "job.failed") {
				t.Fatalf("events = %q", seen)
			}
		})
	}
}

// negative control: guard every action in work → down is refused too and never runs compose down.
func TestDownAllowedUnderPressure(t *testing.T) {
	s, calls := capServer(t)
	j, _ := capRun(t, s, jobs.Down)
	if j.Outcome == nil {
		t.Fatalf("job = %s, no outcome", j.State)
	}
	for _, step := range j.Outcome.Steps {
		if step.Phase == stack.PhaseCapacity {
			t.Fatalf("down refused: %+v", j.Outcome.Steps)
		}
	}
	if c := capCalls(t, calls); !strings.Contains(c, "compose") {
		t.Fatalf("compose down never ran:\n%s", c)
	}
}

const sleepNow = int64(1_800_000_000_000)

// sleepFixture is a server whose jobs run a docker that answers ok, whose meter has read Traefik,
// booted long ago, with one deployment per stack (pr-N holds myapp-pr-N). started is how many minutes
// ago each stack's one container started; s.host answers the runtime view of exactly those stacks.
type sleepFixture struct {
	s       *Server
	metrics *string
	mu      sync.Mutex
	slept   []string // stack.slept: "<stack> <reason>"
}

func newSleepFixture(t *testing.T, started map[string]int, fetchOK bool) *sleepFixture {
	t.Helper()
	fx := &sleepFixture{metrics: new(string)}
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {},
		MetricsURL: "http://traefik/metrics", SchedulerFetch: func(string) (string, bool) { return *fx.metrics, fetchOK },
		Env: exec.Merge(exec.Environ(), map[string]string{"PATH": fakeDockerPath(t)})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	fx.s = s
	s.statsNow = func() int64 { return sleepNow }
	s.pruneStart = s.prune
	s.pruneRunner = func(context.Context) exec.Runner { return s.host }
	s.scheduler.BootAt = 0
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		for stack, min := range started {
			switch strings.TrimSpace(cmd) {
			case "docker ps -aq --filter 'label=com.docker.compose.project=" + stack + "'":
				return exec.Result{OK: true, Stdout: "c-" + stack + "\n"}, true
			case "docker inspect 'c-" + stack + "'":
				at := time.UnixMilli(sleepNow - int64(min)*60_000).UTC().Format(time.RFC3339Nano)
				return exec.Result{OK: true, Stdout: `[{"Id":"c-` + stack + `","Name":"/` + stack + `-web-1","State":{"Status":"running","StartedAt":"` + at + `"}}]`}, true
			}
		}
		return exec.Result{OK: false, Stderr: "Cannot connect to the Docker daemon"}, true
	}
	s.host = f
	for stack := range started {
		crDeploy(t, s, strings.TrimPrefix(stack, "myapp-"), stack, crCompose)
	}
	s.meter.Sample(sleepNow)
	s.bus.On(func(e events.Event) {
		if e.Event == "stack.slept" {
			var d struct{ Stack, Reason string }
			_ = json.Unmarshal(e.Data, &d)
			fx.mu.Lock()
			fx.slept = append(fx.slept, d.Stack+" "+d.Reason)
			fx.mu.Unlock()
		}
	})
	return fx
}

// node seeds a fresh batch for key with a container of each stack, in the given kinds: memory reads
// 6 % free, cpu 3 per core.
func (fx *sleepFixture) node(key, hostname string, kinds []nodestats.Kind, stacks ...string) {
	n := nodestats.NodeLine{Hostname: hostname, Cores: 2, MemTotal: 100, MemAvailable: 50, DiskTotal: 100, DiskFree: 50}
	for _, k := range kinds {
		switch k {
		case nodestats.Memory:
			n.MemAvailable = 6
		case nodestats.CPU:
			n.Load5 = 6
		case nodestats.Disk:
			n.DiskFree = 6
		}
	}
	ctrs := []nodestats.CtrLine{}
	for _, st := range stacks {
		ctrs = append(ctrs, nodestats.CtrLine{ID: "c-" + st, Stack: st})
	}
	fx.s.statsMu.Lock()
	defer fx.s.statsMu.Unlock()
	fx.s.stats.Record(sleepNow, []nodestats.Batch{{Key: key, Raw: key + hostname, Node: n, Ctrs: ctrs}})
	if len(kinds) > 0 {
		fx.s.pressure[key] = kinds
	}
}

// settle waits until no stack has a job outstanding, then returns what slept.
func (fx *sleepFixture) settle(t *testing.T, stacks ...string) []string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		busy := false
		for _, st := range stacks {
			busy = busy || fx.s.jobs.IsBusy(st)
		}
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a job never finished")
		}
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string{}, fx.slept...)
}

// negative control: rank by newest instead of oldest → myapp-pr-3 sleeps.
// negative control: drop the call from statsTick → nothing sleeps.
func TestPressureSleepsTheLeastRecentlyUsed(t *testing.T) {
	stacks := map[string]int{"myapp-pr-1": 120, "myapp-pr-2": 180, "myapp-pr-3": 60, "myapp-pr-4": 600}
	fx := newSleepFixture(t, stacks, true)
	fx.node("n2", "worker-2", []nodestats.Kind{nodestats.Memory}, "myapp-pr-1", "myapp-pr-2", "myapp-pr-3")
	// negative control: take every deployment, not only those on the node → myapp-pr-4 sleeps.
	fx.node("n1", "worker-1", nil, "myapp-pr-4")
	fx.s.statsTick()
	got := fx.settle(t, "myapp-pr-1", "myapp-pr-2", "myapp-pr-3", "myapp-pr-4")
	if want := []string{"myapp-pr-2 worker-2 memory 6% free ≤ 10%"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("slept = %q, want %q", got, want)
	}
}

// negative control: drop the meter check → pr-1 sleeps on a meter that cannot read Traefik.
func TestPressureSleepSkipsWhenMeterNotOK(t *testing.T) {
	fx := newSleepFixture(t, map[string]int{"myapp-pr-1": 600}, false)
	fx.node("n2", "worker-2", []nodestats.Kind{nodestats.Memory}, "myapp-pr-1")
	fx.s.pressureSleep(sleepNow)
	if got := fx.settle(t, "myapp-pr-1"); len(got) != 0 {
		t.Fatalf("slept = %q", got)
	}
}

// negative control: an empty pressureSleep → shared, busy and already asleep fail (pr-2 never sleeps).
func TestPressureSleepEligibility(t *testing.T) {
	memory := []nodestats.Kind{nodestats.Memory}
	next := []string{"myapp-pr-2 worker-2 memory 6% free ≤ 10%"}
	cases := []struct {
		name string
		seed func(t *testing.T, fx *sleepFixture)
		want []string
	}{
		// negative control: drop the Shared check → the shared pr-1 is picked and pr-2 never sleeps.
		{"shared", func(t *testing.T, fx *sleepFixture) {
			compose := crCompose
			shared := strings.Replace(crSpec("myapp-pr-1"), "version: 1\n", "version: 1\nkind: shared\n", 1)
			if _, err := fx.s.registry.Put("pr-1", shared, registry.PutOptions{ComposeYaml: &compose}); err != nil {
				t.Fatal(err)
			}
			fx.node("n2", "worker-2", memory, "myapp-pr-1", "myapp-pr-2")
		}, next},
		// negative control: drop the IsBusy check → pr-1 is picked, OnlyIfIdle refuses it, and pr-2
		// never sleeps.
		{"busy", func(t *testing.T, fx *sleepFixture) {
			release, ok := fx.s.jobs.Hold("myapp-pr-1")
			if !ok {
				t.Fatal("hold refused")
			}
			t.Cleanup(release)
			fx.node("n2", "worker-2", memory, "myapp-pr-1", "myapp-pr-2")
		}, next},
		// negative control: drop the asleep check → pr-1 sleeps again.
		{"already asleep", func(t *testing.T, fx *sleepFixture) {
			if err := fx.s.registry.SetSleep("pr-1", &registry.SleepRecord{Since: 1, Reason: "operator", Hosts: []string{}, Rules: []string{}}); err != nil {
				t.Fatal(err)
			}
			fx.node("n2", "worker-2", memory, "myapp-pr-1", "myapp-pr-2")
		}, next},
		// negative control: compare idle against the setting in seconds, not minutes → pr-1 sleeps.
		// negative control: rank by container start only, not the last request → pr-1 sleeps.
		{"used 10 min ago", func(t *testing.T, fx *sleepFixture) {
			fx.s.wakeMu.Lock()
			if fx.s.routeRouters == nil {
				fx.s.routeRouters = map[string][]string{}
			}
			fx.s.routeRouters["myapp-pr-1"] = []string{"pstack-route-checkout-web"}
			fx.s.wakeMu.Unlock()
			line := `traefik_router_requests_total{router="pstack-route-checkout-web@file"} `
			*fx.metrics = line + "1"
			fx.s.meter.Sample(sleepNow - 20*60_000)
			*fx.metrics = line + "2"
			fx.s.meter.Sample(sleepNow - 10*60_000)
			fx.node("n2", "worker-2", memory, "myapp-pr-1")
		}, []string{}},
		// negative control: act on disk too → pr-1 sleeps on a full disk.
		{"disk-only pressure", func(t *testing.T, fx *sleepFixture) {
			fx.node("n2", "worker-2", []nodestats.Kind{nodestats.Disk}, "myapp-pr-1", "myapp-pr-2")
		}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// pr-1 is the least recently used and disqualified; where pr-2 is on the node, it is next.
			fx := newSleepFixture(t, map[string]int{"myapp-pr-1": 600, "myapp-pr-2": 300}, true)
			c.seed(t, fx)
			fx.s.pressureSleep(sleepNow)
			stacks := []string{"myapp-pr-1", "myapp-pr-2"}
			if c.name == "busy" {
				stacks = stacks[1:] // held until cleanup
			}
			if got := fx.settle(t, stacks...); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("slept = %q, want %q", got, c.want)
			}
		})
	}
}

// negative control: start every eligible stack instead of the best one → both sleep.
func TestPressureSleepOnePerTick(t *testing.T) {
	fx := newSleepFixture(t, map[string]int{"myapp-pr-1": 600, "myapp-pr-2": 300}, true)
	fx.node("n1", "worker-1", []nodestats.Kind{nodestats.CPU}, "myapp-pr-1")
	fx.node("n2", "worker-2", []nodestats.Kind{nodestats.Memory}, "myapp-pr-2")
	fx.s.pressureSleep(sleepNow)
	if got, want := fx.settle(t, "myapp-pr-1", "myapp-pr-2"), []string{"myapp-pr-1 worker-1 load 3 per core ≥ 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("slept = %q, want %q", got, want)
	}
}

// negative control: drop the zero check → pr-1 sleeps with auto-sleep off.
func TestPressureSleepOffAtZero(t *testing.T) {
	fx := newSleepFixture(t, map[string]int{"myapp-pr-1": 600}, true)
	if err := fx.s.settings.Set(settings.KeyPressureSleepIdleMin, "0"); err != nil {
		t.Fatal(err)
	}
	fx.node("n2", "worker-2", []nodestats.Kind{nodestats.Memory}, "myapp-pr-1")
	fx.s.pressureSleep(sleepNow)
	if got := fx.settle(t, "myapp-pr-1"); len(got) != 0 {
		t.Fatalf("slept = %q", got)
	}
}
