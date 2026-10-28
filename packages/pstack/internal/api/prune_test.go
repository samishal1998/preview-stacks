package api

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
)

const pruneCreate = "docker service create --name pstack-prune-"

// swarmAgent is the swarm fixtures (n1 the drained manager, worker-1 reporting from *logs); prune
// answers the prune commands, nil fails them all.
func swarmAgent(t *testing.T, prune map[string]string) (*Server, *string) {
	t.Helper()
	return swarmAgentLs(t, prune, statsNodeLs)
}

// swarmAgentLs is swarmAgent with nodeLs as `node ls`'s answer.
func swarmAgentLs(t *testing.T, prune map[string]string, nodeLs string) (*Server, *string) {
	t.Helper()
	s := statsServer(t)
	logs := agentOutput(t)
	f := statsFake(map[string]string{statsInfoCmd: statsSwarmInfo, statsNodeLsCmd: nodeLs})
	answer := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		cmd = strings.TrimSpace(cmd)
		if cmd == nodestats.AgentLogsCmds(true)[1] {
			return exec.Result{OK: true, Stdout: logs}, true
		}
		for prefix, out := range prune {
			if strings.HasPrefix(cmd, prefix) {
				return exec.Result{OK: true, Stdout: out}, true
			}
		}
		return answer(cmd)
	}
	s.host = f
	return s, &logs
}

// tickAt is one stats tick at `at`, with the agent's batch renumbered to seq so it is news (NP-R2).
func tickAt(t *testing.T, s *Server, logs *string, base string, at, seq int64) {
	t.Helper()
	*logs = strings.ReplaceAll(base, `"seq":1,`, `"seq":`+strconv.FormatInt(seq, 10)+`,`)
	s.statsNow = func() int64 { return at }
	s.statsTick()
}

func commandsWith(s *Server, prefix string) []string {
	out := []string{}
	for _, c := range s.host.(*exec.Fake).Commands() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// negative control: compare `now-last <= 10 min` → the +10 min tick starts no second prune.
// negative control: drop the lastEmergency write → every tick prunes again (the fake's create
// fails, so "only on success" is the same mutation).
func TestEmergencyPruneOncePerTenMinutes(t *testing.T) {
	const t0 = int64(1_000_000)
	s, logs := swarmAgent(t, nil)
	base := agentOutput(t)
	for i, at := range []int64{t0, t0 + 60_000, t0 + 300_000} {
		tickAt(t, s, logs, base, at, int64(i+1))
	}
	creates := commandsWith(s, pruneCreate)
	if len(creates) != 1 {
		t.Fatalf("creates in 10 min = %q, want one", creates)
	}
	want := nodestats.PruneJobCmd("pstack-prune-1000000", nodestats.PruneScript(0, false), "node.id=="+statsWorkerID, false)
	if creates[0] != want {
		t.Fatalf("create:\n got %s\nwant %s", creates[0], want)
	}
	// The drained manager has no batch: unknown is never pressure.
	if local := commandsWith(s, nodestats.PruneScript(0, false)); len(local) != 0 {
		t.Fatalf("self pruned = %q, want none", local)
	}
	tickAt(t, s, logs, base, t0+600_000, 4)
	if creates := commandsWith(s, pruneCreate); len(creates) != 2 || !strings.HasPrefix(creates[1], pruneCreate+"1600000 --mode replicated-job") {
		t.Fatalf("creates at +10 min = %q, want a second replicated-job", creates)
	}

	// negative control: drop the pruneRunning check → a third create.
	t.Run("not while a prune runs", func(t *testing.T) {
		s.pruneRunning.Store(true)
		tickAt(t, s, logs, base, t0+1_300_000, 5)
		if creates := commandsWith(s, pruneCreate); len(creates) != 2 {
			t.Fatalf("creates = %q, want still two", creates)
		}
	})
	// negative control: read the batch with Get instead of Fresh in inPressureLocked → the stale node
	// is pruned (invariant 11).
	t.Run("a stale node is never pruned", func(t *testing.T) {
		s, logs := swarmAgent(t, nil)
		tickAt(t, s, logs, base, t0, 1)
		s.statsNow = func() int64 { return t0 + 600_000 }
		s.statsTick() // the same batch: stale by now
		if creates := commandsWith(s, pruneCreate); len(creates) != 1 {
			t.Fatalf("creates = %q, want only the first", creates)
		}
	})
}

// negative control: initialise lastScheduled from time.Now() in New → the pinned clock never
// reaches +6 h (NP-R3).
// negative control: drop the global job → worker-1 has no event and lastFreed is 1 GB.
func TestScheduledPruneAfterSixHours(t *testing.T) {
	const t0 = int64(1_000_000)
	const sixH = 6 * 3_600_000
	script := nodestats.PruneScript(168, true)
	s, logs := swarmAgent(t, map[string]string{
		script:      "pstack-prune v1 n1 1GB\n",
		pruneCreate: "overall progress: 1 out of 1 tasks\n",
		"docker service logs --raw pstack-prune-": "pstack-prune v1 " + statsWorkerID + " 2GB\npstack-prune v1 " + statsWorkerID + " 500MB\n",
		"docker service rm pstack-prune-":         "",
	})
	base := strings.ReplaceAll(agentOutput(t), fixtureDiskFree, calmDiskFree)
	seen := pressureEvents(t, s)
	tickAt(t, s, logs, base, t0, 1)
	tickAt(t, s, logs, base, t0+sixH-60_000, 2)
	if got := commandsWith(s, "docker service create"); len(got) != 0 || len(commandsWith(s, script)) != 0 {
		t.Fatalf("pruned before 6 h: %q", s.host.(*exec.Fake).Commands())
	}
	tickAt(t, s, logs, base, t0+sixH, 3)
	if got := commandsWith(s, script); len(got) != 1 {
		t.Fatalf("self runs = %q, want one", got)
	}
	name := "pstack-prune-" + strconv.FormatInt(t0+sixH, 10)
	if got, want := commandsWith(s, "docker service create"), []string{nodestats.PruneJobCmd(name, script, "node.id!=n1", true)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("creates:\n got %q\nwant %q", got, want)
	}
	if got := commandsWith(s, "docker service rm "+name); len(got) != 1 {
		t.Fatalf("rm = %q, want one", got)
	}
	at := strconv.FormatInt(t0+sixH, 10)
	want := []string{
		`node.pruned {"node":"n1","hostname":"mgr","reason":"scheduled","freedBytes":1000000000}`,
		`node.pruned {"node":"` + statsWorkerID + `","hostname":"worker-1","reason":"scheduled","freedBytes":2500000000}`,
	}
	if !reflect.DeepEqual(*seen, want) {
		t.Fatalf("events:\n got %q\nwant %q", *seen, want)
	}
	body, _ := statsRead(t, s)
	if p := body["prune"].(map[string]any); p["lastAt"] != float64(t0+sixH) || p["lastFreed"] != float64(3_500_000_000) {
		t.Fatalf("prune = %v, want lastAt %s, lastFreed 3.5 GB", p, at)
	}
	nodes := statsNodes(t, body)
	if p, _ := nodes[1]["prune"].(map[string]any); p == nil || p["at"] != float64(t0+sixH) || p["freedBytes"] != float64(2_500_000_000) || p["reason"] != "scheduled" {
		t.Fatalf("worker-1 prune = %v", nodes[1]["prune"])
	}

	// negative control: ignore image_prune_after_h → a second scheduled prune at +12 h.
	t.Run("image_prune_after_h 0 is off", func(t *testing.T) {
		if err := s.settings.Set(settings.KeyImagePruneAfterH, "0"); err != nil {
			t.Fatal(err)
		}
		tickAt(t, s, logs, base, t0+2*sixH, 4)
		if got := commandsWith(s, script); len(got) != 1 {
			t.Fatalf("self runs = %q, want still one", got)
		}
	})
}

// takesTasks lists worker-1 as ready and active, so a job pinned to it is created.
func takesTasks(s *Server) {
	s.nodeMeta[statsWorkerID] = nodeMeta{Hostname: "worker-1", Role: "worker", Status: "ready", Availability: "active"}
}

// negative control: rm only after a successful create → both subtests fail.
func TestPruneAlwaysRemovesTheJob(t *testing.T) {
	// negative control: rm only after a successful create → no rm.
	// negative control: read the logs after a failed create → `service logs` appears.
	t.Run("create fails", func(t *testing.T) {
		s := statsServer(t)
		s.host = statsFake(map[string]string{})
		s.onSwarm, s.selfKey = true, "n1"
		takesTasks(s)
		seen := pressureEvents(t, s)
		s.prune("pressure", []string{statsWorkerID})
		if got := commandsWith(s, "docker service rm pstack-prune-1000000"); len(got) != 1 {
			t.Fatalf("rm = %q in %q", got, s.host.(*exec.Fake).Commands())
		}
		if got := commandsWith(s, "docker service logs"); len(got) != 0 {
			t.Fatalf("logs read after a failed create: %q", got)
		}
		if len(*seen) != 0 {
			t.Fatalf("events = %q, want none", *seen)
		}
	})
	// NP-R5: the deadline has passed, so the create's runner refuses; the rm still runs, on a live ctx.
	// negative control: run the rm on the prune's ctx → its call is recorded dead.
	t.Run("deadline passed", func(t *testing.T) {
		s := statsServer(t)
		s.onSwarm, s.selfKey = true, "n1"
		takesTasks(s)
		type call struct {
			cmd  string
			live bool
		}
		calls := []call{}
		s.pruneRunner = func(ctx context.Context) exec.Runner {
			f := exec.NewFake(nil, "")
			f.Answer = func(cmd string) (exec.Result, bool) {
				live := ctx.Err() == nil
				calls = append(calls, call{strings.TrimSpace(cmd), live})
				return exec.Result{OK: live}, true
			}
			return f
		}
		s.cancel()
		s.prune("pressure", []string{statsWorkerID})
		if len(calls) != 2 || calls[0].live || !strings.HasPrefix(calls[0].cmd, pruneCreate) ||
			calls[1] != (call{"docker service rm pstack-prune-1000000", true}) {
			t.Fatalf("calls = %+v, want a dead create then a live rm", calls)
		}
	})
}

// A job no node can run waits forever (Engine 29.9: "waiting for tasks"), so none is created.
// negative control: set takesTask true for every node → the drained, paused and down cases create a
// job.
func TestPruneJobNeedsANodeThatTakesTasks(t *testing.T) {
	const t0 = int64(1_000_000)
	const sixH = 6 * 3_600_000
	mgr := func(availability string) string {
		return `{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"` + availability + `","ManagerStatus":"Leader"}` + "\n"
	}
	worker := func(status, availability string) string {
		return `{"ID":"` + statsWorkerID + `","Hostname":"worker-1","Status":"` + status + `","Availability":"` + availability + `","ManagerStatus":""}` + "\n"
	}
	full, calm := agentOutput(t), strings.ReplaceAll(agentOutput(t), fixtureDiskFree, calmDiskFree)
	for _, c := range []struct{ name, nodeLs, base string }{
		{"scheduled: the worker drained", mgr("Drain") + worker("Ready", "Drain"), calm},
		{"scheduled: one active manager", mgr("Active"), calm},
		{"emergency: the worker paused", mgr("Drain") + worker("Ready", "Pause"), full},
		{"emergency: the worker down", mgr("Drain") + worker("Down", "Active"), full},
	} {
		// negative control: takesTask true for every node fails the drained, paused and down cases;
		// dropping `&& key != self` from othersTake fails "one active manager".
		t.Run(c.name, func(t *testing.T) {
			s, logs := swarmAgentLs(t, nil, c.nodeLs)
			tickAt(t, s, logs, c.base, t0, 1)
			tickAt(t, s, logs, c.base, t0+sixH, 2)
			if got := commandsWith(s, "docker service create"); len(got) != 0 {
				t.Fatalf("creates = %q, want none", got)
			}
			if c.base == calm && len(commandsWith(s, nodestats.PruneScript(168, true))) != 1 {
				t.Fatalf("self not pruned: %q", s.host.(*exec.Fake).Commands())
			}
		})
	}
}

// negative control: skip a node whose freed bytes are 0 → no node.pruned.
// negative control: key "" as itself instead of selfKey → the compose row's prune stays null.
func TestPruneZeroBytes(t *testing.T) {
	s, _ := composeAgent(t)
	f := s.host.(*exec.Fake)
	answer := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.TrimSpace(cmd) == nodestats.PruneScript(0, false) {
			return exec.Result{OK: true, Stdout: "pstack-prune v1 - 0B\n"}, true
		}
		return answer(cmd)
	}
	seen := pressureEvents(t, s)
	s.statsTick()
	want := []string{"node.pressure " + pressureWant, `node.pruned {"node":null,"hostname":"worker-1","reason":"pressure","freedBytes":0}`}
	if !reflect.DeepEqual(*seen, want) {
		t.Fatalf("events:\n got %q\nwant %q", *seen, want)
	}
	body, _ := statsRead(t, s)
	if p := body["prune"].(map[string]any); p["lastAt"] != float64(1_000_000) || p["lastFreed"] != float64(0) {
		t.Fatalf("prune = %v", p)
	}
	if p := statsNodes(t, body)[0]["prune"]; !reflect.DeepEqual(p, map[string]any{"at": float64(1_000_000), "freedBytes": float64(0), "reason": "pressure"}) {
		t.Fatalf("node prune = %#v", p)
	}
}
