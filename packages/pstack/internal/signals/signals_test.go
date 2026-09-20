package signals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// shim answers the exact command strings, like internal/swarm's own test helper.
func shim(t *testing.T, answers map[string]string) *exec.Fake {
	t.Helper()
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if out, ok := answers[strings.TrimSpace(cmd)]; ok {
			return exec.Result{OK: true, Stdout: out}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

const infoActive = `{"LocalNodeState":"active","ControlAvailable":true,"NodeID":"n1abcdef01234567","NodeAddr":"10.0.0.1"}`

func manager(t *testing.T) *exec.Fake {
	t.Helper()
	return shim(t, map[string]string{
		"docker info --format '{{json .Swarm}}'":    infoActive,
		"docker node ls --format '{{json .}}'":      read(t, "node-ls.jsonl"),
		"docker service ls --format '{{json .}}'":   read(t, "service-ls.jsonl"),
		"docker service ps --no-trunc --filter desired-state=running --format '{{json .}}' 'svc1web00000000' 'svc2api00000000' 'svc3log00000000'": read(t, "service-ps.jsonl"),
	})
}

// negative control: drop the `--no-trunc` flag from the service ps command → the shim no longer
// answers it, so no task is seen and both the counts and Stuck come back empty.
func TestLookReportsNodesAndTheirTaskCounts(t *testing.T) {
	v := Look(manager(t))
	if !v.Swarm || !v.Reachable {
		t.Fatalf("swarm=%v reachable=%v", v.Swarm, v.Reachable)
	}
	if len(v.Nodes) != 3 {
		t.Fatalf("nodes = %d", len(v.Nodes))
	}
	want := map[string]struct {
		role  string
		tasks int
	}{
		"preview-host": {"manager", 0},
		"worker-1":     {"worker", 1}, // one preview task; its global logs task does not count
		"worker-2":     {"worker", 0}, // only a global task
	}
	for _, n := range v.Nodes {
		w, ok := want[n.Hostname]
		if !ok {
			t.Fatalf("unexpected node %q", n.Hostname)
		}
		if n.Role != w.role || n.Tasks != w.tasks {
			t.Fatalf("%s: role=%q tasks=%d, want %q and %d", n.Hostname, n.Role, n.Tasks, w.role, w.tasks)
		}
		if n.State != "ready" || n.Availability != "active" {
			t.Fatalf("%s: state=%q availability=%q", n.Hostname, n.State, n.Availability)
		}
	}
}

// negative control: stop stripping the quotes docker wraps the error in → Reason keeps its `"` and
// the HasPrefix check below fails, so nothing is reported as stuck.
func TestLookReportsStuckTasksWithDockersOwnReason(t *testing.T) {
	v := Look(manager(t))
	if len(v.Stuck) != 1 {
		t.Fatalf("stuck = %+v", v.Stuck)
	}
	s := v.Stuck[0]
	if s.Task != "task2api0000" || s.Service != "pr-42_api" || s.Stack != "pr-42" {
		t.Fatalf("stuck = %+v", s)
	}
	if s.Reason != "no suitable node (insufficient resources on 2 nodes)" {
		t.Fatalf("reason = %q", s.Reason)
	}
}

// negative control: report Swarm: true when docker did not answer → this reads 3 nodes.
func TestLookOnAnUnreachableDocker(t *testing.T) {
	down := exec.NewFake(func(string) bool { return true }, "")
	v := Look(down)
	if v.Swarm || v.Reachable || len(v.Nodes) != 0 || len(v.Stuck) != 0 {
		t.Fatalf("view = %+v", v)
	}
}

// negative control: treat a compose host as a swarm → Swarm is true with no nodes, and the route
// above it would advertise a cluster that does not exist.
func TestLookOnAComposeHost(t *testing.T) {
	f := shim(t, map[string]string{
		"docker info --format '{{json .Swarm}}'": `{"LocalNodeState":"inactive"}`,
	})
	v := Look(f)
	if v.Swarm || !v.Reachable {
		t.Fatalf("swarm=%v reachable=%v", v.Swarm, v.Reachable)
	}
	if len(v.Nodes) != 0 || len(v.Stuck) != 0 {
		t.Fatalf("view = %+v", v)
	}
	for _, cmd := range f.Commands() {
		if strings.Contains(cmd, "service") {
			t.Fatalf("asked about services on a compose host: %q", cmd)
		}
	}
}

// negative control: keep asking for tasks when no service exists → the command runs with no ids and
// docker would error; the fake records the call and this fails.
func TestLookWithNoServices(t *testing.T) {
	f := shim(t, map[string]string{
		"docker info --format '{{json .Swarm}}'":  infoActive,
		"docker node ls --format '{{json .}}'":    read(t, "node-ls.jsonl"),
		"docker service ls --format '{{json .}}'": "",
	})
	v := Look(f)
	if len(v.Nodes) != 3 || len(v.Stuck) != 0 {
		t.Fatalf("view = %+v", v)
	}
	for _, cmd := range f.Commands() {
		if strings.Contains(cmd, "service ps") {
			t.Fatalf("asked for tasks with no services: %q", cmd)
		}
	}
}
