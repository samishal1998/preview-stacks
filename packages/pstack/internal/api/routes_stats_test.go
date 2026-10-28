package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
)

const (
	statsInfoCmd   = "docker info --format '{{json .Swarm}}'"
	statsNodeLsCmd = "docker node ls --format '{{json .}}'"
	statsWorkerID  = "x8k2m9qv1n3p4r5s6t7u8w9y0" // the node id agent-output.txt carries
	statsSwarmInfo = `{"LocalNodeState":"active","ControlAvailable":true,"NodeID":"n1","NodeAddr":"10.0.0.1"}`
	statsNodeLs    = `{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Drain","ManagerStatus":"Leader"}` + "\n" +
		`{"ID":"` + statsWorkerID + `","Hostname":"worker-1","Status":"Ready","Availability":"Active","ManagerStatus":""}` + "\n"
)

// statsServer is signalsServer with the stats clock pinned, so every `at` in a body is known.
func statsServer(t *testing.T) *Server {
	t.Helper()
	s := signalsServer(t)
	s.statsNow = func() int64 { return 1_000_000 }
	return s
}

// agentOutput is the agent script's own fixture: one node, three containers, one of them in no stack.
func agentOutput(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "templates", "agent", "testdata", "agent-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// statsFake answers exactly these commands; anything else fails, the way an absent docker does.
func statsFake(answers map[string]string) *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if out, ok := answers[strings.TrimSpace(cmd)]; ok {
			return exec.Result{OK: true, Stdout: out}, true
		}
		return exec.Result{OK: false, Stderr: "Cannot connect to the Docker daemon"}, true
	}
	return f
}

func statsRead(t *testing.T, s *Server) (map[string]any, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.statsGet(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return body, rec.Body.Bytes()
}

func statsNodes(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	list, ok := body["nodes"].([]any)
	if !ok {
		t.Fatalf("nodes = %#v, want a list", body["nodes"])
	}
	out := []map[string]any{}
	for _, n := range list {
		out = append(out, n.(map[string]any))
	}
	return out
}

// statsValueKeys are the readings that are null while a node is unknown.
var statsValueKeys = []string{"at", "cores", "load1", "load5", "memTotal", "memAvailable", "diskTotal", "diskFree"}

// negative control: count the stack-less container as a stack "" → stacks gains a third row and the
// body comparison fails; and serialising Node "" as "" instead of null fails it too (NP-R15).
func TestStatsComposeHost(t *testing.T) {
	s := statsServer(t)
	// A compose host's agent has no swarm node id: its batch carries node "".
	logs := strings.ReplaceAll(agentOutput(t), `"node":"`+statsWorkerID+`"`, `"node":""`)
	s.host = statsFake(map[string]string{
		statsInfoCmd:                      `{"LocalNodeState":"inactive","NodeID":"","ControlAvailable":false}`,
		nodestats.AgentLogsCmds(false)[0]: logs,
	})
	s.statsTick()

	_, raw := statsRead(t, s)
	want := `{"v":1,"at":1000000,"agent":true,"nodes":[{"node":null,"hostname":"worker-1","role":null,` +
		`"at":1000000,"stale":false,"cores":8,"load1":1.02,"load5":1.1,"memTotal":33399881728,` +
		`"memAvailable":4294967296,"diskTotal":105152176128,"diskFree":5136490496,"pressure":[],"prune":null,` +
		`"history":[{"at":1000000,"load5":1.1,"memAvailable":4294967296,"diskFree":5136490496}]}],` +
		`"stacks":[{"stack":"pr-7","cpuPct":0.52,"mem":1610612736,"containers":1,"nodes":["worker-1"]},` +
		`{"stack":"pr-42","cpuPct":3.1,"mem":129394278,"containers":1,"nodes":["worker-1"]}],` +
		`"prune":{"lastAt":null,"lastFreed":null}}`
	var got bytes.Buffer
	if err := json.Compact(&got, raw); err != nil {
		t.Fatal(err)
	}
	if got.String() != want {
		t.Fatalf("body:\n got %s\nwant %s", got.String(), want)
	}
	if s.selfKey != "worker-1" {
		t.Fatalf("selfKey = %q, want the only batch's key", s.selfKey)
	}
}

// negative control: list the store's keys instead of node ls's on swarm → the silent manager has no
// row at all, and the stale row assertions below fail.
func TestStatsSwarmJoinsNodeLs(t *testing.T) {
	s := statsServer(t)
	s.host = statsFake(map[string]string{
		statsInfoCmd:                     statsSwarmInfo,
		statsNodeLsCmd:                   statsNodeLs,
		nodestats.AgentLogsCmds(true)[1]: agentOutput(t),
	})
	s.statsTick()

	body, _ := statsRead(t, s)
	if body["agent"] != true {
		t.Fatalf("agent = %v, want true", body["agent"])
	}
	nodes := statsNodes(t, body)
	if len(nodes) != 2 || nodes[0]["hostname"] != "mgr" || nodes[1]["hostname"] != "worker-1" {
		t.Fatalf("nodes = %v, want mgr then worker-1 (sorted by hostname)", nodes)
	}
	mgr, wrk := nodes[0], nodes[1]
	if mgr["node"] != "n1" || mgr["role"] != "manager" || mgr["stale"] != true {
		t.Fatalf("mgr = %v", mgr)
	}
	for _, k := range statsValueKeys {
		if v, ok := mgr[k]; !ok || v != nil {
			t.Errorf("mgr %s = %v (present %v), want null", k, v, ok)
		}
	}
	if h, ok := mgr["history"].([]any); !ok || len(h) != 0 {
		t.Errorf("mgr history = %#v, want []", mgr["history"])
	}
	if wrk["node"] != statsWorkerID || wrk["role"] != "worker" || wrk["stale"] != false || wrk["memAvailable"] != float64(4294967296) {
		t.Fatalf("worker-1 = %v", wrk)
	}
	if s.selfKey != "n1" || !s.onSwarm {
		t.Fatalf("selfKey = %q onSwarm = %v, want n1 on swarm", s.selfKey, s.onSwarm)
	}
}

func TestStatsNoAgent(t *testing.T) {
	// negative control: build the rows only from batches the store holds → both rows vanish, and the
	// null assertions would pass on an empty list, so the length check fails first (NP-R14).
	t.Run("swarm rows are all null", func(t *testing.T) {
		s := statsServer(t)
		s.host = statsFake(map[string]string{statsInfoCmd: statsSwarmInfo, statsNodeLsCmd: statsNodeLs})
		s.statsTick()

		body, _ := statsRead(t, s)
		if body["agent"] != false {
			t.Fatalf("agent = %v, want false", body["agent"])
		}
		nodes := statsNodes(t, body)
		if len(nodes) != 2 {
			t.Fatalf("nodes = %v, want both node ls rows", nodes)
		}
		for _, n := range nodes {
			if n["stale"] != true {
				t.Errorf("%v stale = %v", n["hostname"], n["stale"])
			}
			for _, k := range statsValueKeys {
				if v, ok := n[k]; !ok || v != nil {
					t.Errorf("%v %s = %v (present %v), want null", n["hostname"], k, v, ok)
				}
			}
			if h, ok := n["history"].([]any); !ok || len(h) != 0 {
				t.Errorf("%v history = %#v, want []", n["hostname"], n["history"])
			}
			if p, ok := n["pressure"].([]any); !ok || len(p) != 0 {
				t.Errorf("%v pressure = %#v, want []", n["hostname"], n["pressure"])
			}
		}
		if st, ok := body["stacks"].([]any); !ok || len(st) != 0 {
			t.Fatalf("stacks = %#v, want []", body["stacks"])
		}
	})
	// negative control: initialise nodes as a nil slice → `nodes` serialises as null, not [].
	t.Run("compose has no rows", func(t *testing.T) {
		s := statsServer(t)
		s.host = statsFake(map[string]string{statsInfoCmd: `{"LocalNodeState":"inactive"}`})
		s.statsTick()

		body, _ := statsRead(t, s)
		if body["agent"] != false {
			t.Fatalf("agent = %v, want false", body["agent"])
		}
		if n, ok := body["nodes"].([]any); !ok || len(n) != 0 {
			t.Fatalf("nodes = %#v, want []", body["nodes"])
		}
		if st, ok := body["stacks"].([]any); !ok || len(st) != 0 {
			t.Fatalf("stacks = %#v, want []", body["stacks"])
		}
	})
}

func TestStatsDockerSilentKeepsNodes(t *testing.T) {
	first := func(t *testing.T) *Server {
		s := statsServer(t)
		s.host = statsFake(map[string]string{
			statsInfoCmd:                     statsSwarmInfo,
			statsNodeLsCmd:                   statsNodeLs,
			nodestats.AgentLogsCmds(true)[1]: agentOutput(t),
		})
		s.statsTick()
		return s
	}
	check := func(t *testing.T, s *Server) {
		t.Helper()
		body, _ := statsRead(t, s)
		nodes := statsNodes(t, body)
		if len(nodes) != 2 || nodes[0]["role"] != "manager" || nodes[1]["hostname"] != "worker-1" {
			t.Fatalf("nodes = %v, want the first tick's two rows", nodes)
		}
		if body["agent"] != true || nodes[1]["stale"] != false {
			t.Fatalf("agent = %v worker-1 = %v, want the first tick's batch still fresh", body["agent"], nodes[1])
		}
	}
	// negative control: replace nodeMeta with SwarmInfo's answer even when it is unreachable → the
	// second tick empties the list.
	t.Run("docker info fails", func(t *testing.T) {
		s := first(t)
		s.host = statsFake(map[string]string{})
		s.statsTick()
		check(t, s)
	})
	// negative control: accept an active swarm's empty node list → one flaky node ls wipes every row.
	t.Run("node ls fails", func(t *testing.T) {
		s := first(t)
		s.host = statsFake(map[string]string{statsInfoCmd: statsSwarmInfo})
		s.statsTick()
		check(t, s)
	})
}

// negative control: parse PSTACK_STATS_TICK_MS with num() → an explicit "0" reads as unset and serve
// turns the loop on at 30 s.
func TestTuningStatsTick(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	for _, c := range []struct {
		in  map[string]string
		ms  float64
		set bool
	}{
		{map[string]string{}, 0, false},
		{map[string]string{"PSTACK_STATS_TICK_MS": "0"}, 0, true},
		{map[string]string{"PSTACK_STATS_TICK_MS": "100"}, 100, true},
		{map[string]string{"PSTACK_STATS_TICK_MS": "-5"}, 0, false},
	} {
		got := TuningFromEnv(env(c.in))
		if got.StatsTickMs != c.ms || got.StatsTickSet != c.set {
			t.Errorf("%v → (%v, %v), want (%v, %v)", c.in, got.StatsTickMs, got.StatsTickSet, c.ms, c.set)
		}
	}
}
