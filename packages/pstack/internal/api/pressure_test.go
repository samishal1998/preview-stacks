package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
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
