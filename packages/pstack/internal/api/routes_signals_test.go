package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// signalsServer is a real New() server; nothing listens and it is never Started, because Start runs
// the docker probe. Tests assign s.host themselves.
func signalsServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// signalsShim answers the four commands `signals.Look` issues on a manager. The fixtures live with
// the signals package, since they are docker's output and nothing here re-states them.
func signalsShim(t *testing.T, empty bool) *exec.Fake {
	t.Helper()
	dir := filepath.Join("..", "signals", "testdata")
	readFile := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ps := readFile("service-ps.jsonl")
	if empty {
		ps = "" // no tasks anywhere: every worker reads empty
	}
	answers := map[string]string{
		"docker info --format '{{json .Swarm}}'":  `{"LocalNodeState":"active","ControlAvailable":true,"NodeID":"n1abcdef01234567","NodeAddr":"10.0.0.1"}`,
		"docker node ls --format '{{json .}}'":    readFile("node-ls.jsonl"),
		"docker service ls --format '{{json .}}'": readFile("service-ls.jsonl"),
		"docker service ps --no-trunc --filter desired-state=running --format '{{json .}}' 'svc1web00000000' 'svc2api00000000' 'svc3log00000000'": ps,
	}
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if out, ok := answers[strings.TrimSpace(cmd)]; ok {
			return exec.Result{OK: true, Stdout: out}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func getSignals(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.signalsGet(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return body
}

// negative control: return the nodes slice without initialising it (nil) → `nodes` serialises as
// null and the type assertion below fails, which is the shape the UI and any consumer would break on.
func TestSignalsRouteReportsTheCluster(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)

	body := getSignals(t, s)
	if body["swarm"] != true || body["reachable"] != true {
		t.Fatalf("body = %v", body)
	}
	if body["v"] != float64(1) {
		t.Fatalf("v = %v", body["v"])
	}
	nodes, ok := body["nodes"].([]any)
	if !ok || len(nodes) != 3 {
		t.Fatalf("nodes = %v", body["nodes"])
	}
	stuck, ok := body["stuck"].([]any)
	if !ok || len(stuck) != 1 {
		t.Fatalf("stuck = %v", body["stuck"])
	}
	first := stuck[0].(map[string]any)
	if first["reason"] != "no suitable node (insufficient resources on 2 nodes)" {
		t.Fatalf("reason = %v", first["reason"])
	}
	// The worker carrying a preview task is busy and has no emptySince; the other one is empty.
	for _, raw := range nodes {
		n := raw.(map[string]any)
		switch n["hostname"] {
		case "worker-1":
			if n["tasks"] != float64(1) || n["emptySince"] != nil {
				t.Fatalf("worker-1 = %v", n)
			}
		case "worker-2":
			if n["tasks"] != float64(0) || n["emptySince"] == nil {
				t.Fatalf("worker-2 = %v", n)
			}
		}
	}
}

// negative control: restart the empty clock on every read (write the map unconditionally) → the
// second read reports a later emptySince and this fails.
func TestSignalsEmptySinceHoldsAcrossReads(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, true)

	first := getSignals(t, s)
	second := getSignals(t, s)
	at := func(body map[string]any, host string) any {
		for _, raw := range body["nodes"].([]any) {
			if n := raw.(map[string]any); n["hostname"] == host {
				return n["emptySince"]
			}
		}
		t.Fatalf("no node %q", host)
		return nil
	}
	if a, b := at(first, "worker-2"), at(second, "worker-2"); a == nil || a != b {
		t.Fatalf("emptySince moved: %v then %v", a, b)
	}
}

// negative control: report swarm: true on a compose host → this fails, and the route would claim a
// cluster that does not exist.
func TestSignalsRouteOnAComposeHost(t *testing.T) {
	s := signalsServer(t)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.Contains(cmd, "docker info") {
			return exec.Result{OK: true, Stdout: `{"LocalNodeState":"inactive"}`}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f

	body := getSignals(t, s)
	if body["swarm"] != false {
		t.Fatalf("swarm = %v", body["swarm"])
	}
	if nodes, ok := body["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Fatalf("nodes = %v", body["nodes"])
	}
	if stuck, ok := body["stuck"].([]any); !ok || len(stuck) != 0 {
		t.Fatalf("stuck = %v", body["stuck"])
	}
}

// captureBus records what the ticker emits, in order.
func captureBus(t *testing.T, s *Server) *[]string {
	t.Helper()
	seen := []string{}
	off := s.bus.On(func(e events.Event) {
		var d map[string]any
		_ = json.Unmarshal(e.Data, &d)
		seen = append(seen, e.Event+" "+asString(d["id"]))
	})
	t.Cleanup(off)
	return &seen
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// negative control: emit on every tick instead of on the change → the second tick repeats both
// lines and this fails.
func TestSignalsTickEmitsOnChangeOnly(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)
	seen := captureBus(t, s)

	s.signalsTick()
	s.signalsTick()

	if len(*seen) != 2 {
		t.Fatalf("emitted %v", *seen)
	}
	want := map[string]bool{"signal.raised stuck/pr-42_api": true, "signal.raised empty/n3abcdef01234567": true}
	for _, line := range *seen {
		if !want[line] {
			t.Fatalf("unexpected %q in %v", line, *seen)
		}
	}
}

// negative control: store the new tick's payload unconditionally → since moves to the second tick.
func TestSignalsTickKeepsTheFirstSince(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)
	s.signalsTick()
	first := s.signalsSent["stuck/pr-42_api"].Since
	time.Sleep(5 * time.Millisecond)
	s.signalsTick()
	if got := s.signalsSent["stuck/pr-42_api"].Since; got != first {
		t.Fatalf("since moved from %d to %d", first, got)
	}
}

// negative control: skip the cleared half → the second tick emits nothing and this fails.
func TestSignalsTickClearsWhenTheClusterRecovers(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)
	s.signalsTick()
	seen := captureBus(t, s)

	// Every task placed, nothing stuck: worker-1 now carries the api task too.
	s.host = signalsShim(t, true)
	s.signalsTick()

	got := map[string]bool{}
	for _, line := range *seen {
		got[line] = true
	}
	if !got["signal.cleared stuck/pr-42_api"] {
		t.Fatalf("no clear for the stuck task: %v", *seen)
	}
	if !got["signal.raised empty/n2abcdef01234567"] {
		t.Fatalf("worker-1 went empty and nobody said so: %v", *seen)
	}
}

// negative control: clear on an unreadable tick → this sees a cleared event, and one flaky docker
// call would tell a consumer the cluster is fine.
func TestSignalsTickIgnoresAnUnreadableDocker(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)
	s.signalsTick()
	seen := captureBus(t, s)

	s.host = exec.NewFake(func(string) bool { return true }, "")
	s.signalsTick()

	if len(*seen) != 0 {
		t.Fatalf("emitted %v on an unreadable docker", *seen)
	}
}

// failing is signalsShim with every command that starts with prefix refused.
func failing(t *testing.T, prefix string) *exec.Fake {
	t.Helper()
	f := signalsShim(t, false)
	answer := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(strings.TrimSpace(cmd), prefix) {
			return exec.Result{OK: false, Stderr: "context deadline exceeded"}, true
		}
		return answer(cmd)
	}
	return f
}

// negative control: drop the `len(view.Nodes) == 0` half of answered() → the node-ls case clears
// `empty/n3…` and wipes its clock; drop the answered() guard in signalsView → the unreachable case
// loses the clock. Drop the ok checks in signals.Look → the service cases raise `empty/n2…` and
// clear `stuck/pr-42_api`.
func TestSignalsTickIgnoresAFailedDockerCommand(t *testing.T) {
	for _, c := range []struct {
		name string
		host func(t *testing.T) exec.Runner
	}{
		{"node ls", func(t *testing.T) exec.Runner { return failing(t, "docker node ls") }},
		{"service ls", func(t *testing.T) exec.Runner { return failing(t, "docker service ls") }},
		{"service ps", func(t *testing.T) exec.Runner { return failing(t, "docker service ps") }},
		{"unreachable", func(t *testing.T) exec.Runner { return exec.NewFake(func(string) bool { return true }, "") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := signalsServer(t)
			s.host = signalsShim(t, false)
			s.signalsTick()
			before := s.emptySince["n3abcdef01234567"]
			seen := captureBus(t, s)

			s.host = c.host(t)
			s.signalsTick()
			_ = getSignals(t, s)

			if len(*seen) != 0 {
				t.Fatalf("emitted %v", *seen)
			}
			if after, ok := s.emptySince["n3abcdef01234567"]; before == 0 || !ok || after != before {
				t.Fatalf("emptySince moved: %d then %d (%v)", before, after, ok)
			}
		})
	}
}

// negative control: drop the `len(view.Nodes) == 0` check in swarmNodeRoutes → a node that exists
// answers 404 `no such node` while `node ls` is failing.
func TestDrainWhileNodeLsFails(t *testing.T) {
	s := signalsServer(t)
	s.host = failing(t, "docker node ls")
	if code, body := nodeAction(t, s, "POST", "/api/swarm/nodes/n2abcdef01234567/drain"); code != 409 || !strings.Contains(body, "docker did not answer") {
		t.Fatalf("status %d: %s", code, body)
	}
}

// negative control: drop the deferred s.recovered in signalsTickSafe → the panic escapes and the
// test binary dies.
func TestSignalsTickSurvivesAPanic(t *testing.T) {
	s := signalsServer(t)
	f := exec.NewFake(nil, "")
	f.Answer = func(string) (exec.Result, bool) { panic("bad docker output") }
	s.host = f
	logged := []string{}
	s.opts.Log = func(line string) { logged = append(logged, line) }

	s.signalsTickSafe()

	if len(logged) != 1 || logged[0] != "signals: tick failed: bad docker output" {
		t.Fatalf("logged %q", logged)
	}
}

// The stream pumps pass stop/cancel as then: a recovered pump must still end its stream.
// negative control: drop the then() call in recovered → stopped stays false.
func TestRecoveredLogsAndRunsThen(t *testing.T) {
	s := signalsServer(t)
	logged := []string{}
	s.opts.Log = func(line string) { logged = append(logged, line) }
	stopped := false
	func() {
		defer s.recovered("logs: stream pump failed", func() { stopped = true })
		panic("boom")
	}()
	if !stopped || len(logged) != 1 || logged[0] != "logs: stream pump failed: boom" {
		t.Fatalf("stopped %v, logged %q", stopped, logged)
	}
}

func nodeAction(t *testing.T, s *Server, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	return rec.Code, rec.Body.String()
}

// negative control: drop the `state == down` half of the DELETE guard → removing a ready node
// answers 200 and `docker node rm` is recorded, which is how a network blip orphans live tasks.
func TestNodeDeleteRefusesANodeThatIsStillUp(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	s.host = f

	code, body := nodeAction(t, s, "DELETE", "/api/swarm/nodes/n2abcdef01234567")
	if code != 409 {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, cmd := range f.Commands() {
		if strings.Contains(cmd, "node rm") {
			t.Fatalf("ran %q on a live node", cmd)
		}
	}
}

// negative control: pass --force on the remove → the command below stops matching, and a node with
// live tasks would go anyway.
func TestNodeDeleteRemovesADownAndDrainedNode(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	// worker-2 has gone: docker reports it down and drained.
	gone := `{"ID":"n1abcdef01234567","Hostname":"preview-host","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}
{"ID":"n3abcdef01234567","Hostname":"worker-2","Status":"Down","Availability":"Drain","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}`
	inner := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.Contains(cmd, "node ls") {
			return exec.Result{OK: true, Stdout: gone}, true
		}
		return inner(cmd)
	}
	s.host = f

	code, body := nodeAction(t, s, "DELETE", "/api/swarm/nodes/n3abcdef01234567")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	found := false
	for _, cmd := range f.Commands() {
		if cmd == "docker node rm 'n3abcdef01234567'" {
			found = true
		}
	}
	if !found {
		t.Fatalf("node rm not run: %v", f.Commands())
	}
}

func ran(f *exec.Fake, cmd string) bool {
	for _, c := range f.Commands() {
		if c == cmd {
			return true
		}
	}
	return false
}

// negative control: send the availability straight from the URL instead of the fixed words → a
// request for /drain would run whatever the caller typed.
func TestDrainRunsOneDockerCommand(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	s.host = f

	if code, body := nodeAction(t, s, "POST", "/api/swarm/nodes/n2abcdef01234567/drain"); code != 200 {
		t.Fatalf("drain: %d %s", code, body)
	}
	if !ran(f, "docker node update --availability 'drain' 'n2abcdef01234567'") {
		t.Fatalf("drain not run: %v", f.Commands())
	}
}

// negative control: drop the "already there" check in setAvailability → undraining an active node
// shells out to docker for nothing, and this fails.
func TestUndrainIsIdempotentAndRunsWhenItHasTo(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	s.host = f

	// n2 is already active: 200, and nothing runs.
	if code, body := nodeAction(t, s, "POST", "/api/swarm/nodes/n2abcdef01234567/undrain"); code != 200 {
		t.Fatalf("undrain: %d %s", code, body)
	}
	if ran(f, "docker node update --availability 'active' 'n2abcdef01234567'") {
		t.Fatalf("ran an update on a node already active: %v", f.Commands())
	}

	// n3 is drained: the same call runs the command.
	drained := `{"ID":"n1abcdef01234567","Hostname":"preview-host","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}
{"ID":"n3abcdef01234567","Hostname":"worker-2","Status":"Ready","Availability":"Drain","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}`
	inner := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.Contains(cmd, "node ls") {
			return exec.Result{OK: true, Stdout: drained}, true
		}
		return inner(cmd)
	}
	if code, body := nodeAction(t, s, "POST", "/api/swarm/nodes/n3abcdef01234567/undrain"); code != 200 {
		t.Fatalf("undrain: %d %s", code, body)
	}
	if !ran(f, "docker node update --availability 'active' 'n3abcdef01234567'") {
		t.Fatalf("undrain not run: %v", f.Commands())
	}
}

// negative control: skip the unknown-node check → draining a node that does not exist answers 200
// and shells out to docker for nothing.
func TestDrainAnUnknownNode(t *testing.T) {
	s := signalsServer(t)
	s.host = signalsShim(t, false)
	if code, _ := nodeAction(t, s, "POST", "/api/swarm/nodes/nope/drain"); code != 404 {
		t.Fatalf("status %d", code)
	}
}

// negative control: answer these routes on a compose host → a single-machine host claims a swarm.
func TestNodeRoutesOnAComposeHost(t *testing.T) {
	s := signalsServer(t)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.Contains(cmd, "docker info") {
			return exec.Result{OK: true, Stdout: `{"LocalNodeState":"inactive"}`}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f
	if code, _ := nodeAction(t, s, "POST", "/api/swarm/nodes/n2/drain"); code != 409 {
		t.Fatalf("status %d", code)
	}
}

// negative control: drop the `state == down` half of the DELETE guard → this node, drained but
// still up and still holding whatever swarm has not moved yet, is removed and its tasks orphaned.
func TestNodeDeleteRefusesADrainedNodeThatIsStillUp(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	up := `{"ID":"n1abcdef01234567","Hostname":"preview-host","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}
{"ID":"n3abcdef01234567","Hostname":"worker-2","Status":"Ready","Availability":"Drain","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}`
	inner := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.Contains(cmd, "node ls") {
			return exec.Result{OK: true, Stdout: up}, true
		}
		return inner(cmd)
	}
	s.host = f

	code, body := nodeAction(t, s, "DELETE", "/api/swarm/nodes/n3abcdef01234567")
	if code != 409 {
		t.Fatalf("status %d: %s", code, body)
	}
	if ran(f, "docker node rm 'n3abcdef01234567'") {
		t.Fatalf("removed a node that is still up: %v", f.Commands())
	}
}

// negative control: run SetPreviewsOn with a fixed "workers" instead of the URL's word → POST
// /previews/any labels the manager off and the `on` command is never recorded.
func TestSwarmPreviewsLabelsThisManager(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	s.host = f

	code, body := nodeAction(t, s, "POST", "/api/swarm/previews/workers")
	if code != 200 || !strings.Contains(body, `"previewsOn": "workers"`) {
		t.Fatalf("workers: %d %s", code, body)
	}
	if !ran(f, "docker node update --label-add 'pstack.previews=off' 'n1abcdef01234567'") {
		t.Fatalf("label not set: %v", f.Commands())
	}
	if code, body := nodeAction(t, s, "POST", "/api/swarm/previews/any"); code != 200 || !strings.Contains(body, `"previewsOn": "any"`) {
		t.Fatalf("any: %d %s", code, body)
	}
	if !ran(f, "docker node update --label-add 'pstack.previews=on' 'n1abcdef01234567'") {
		t.Fatalf("label not cleared: %v", f.Commands())
	}
	if code, _ := nodeAction(t, s, "POST", "/api/swarm/previews/everywhere"); code != 404 {
		t.Fatalf("everywhere: %d", code)
	}

	s.host = exec.NewFake(nil, "") // docker answers, but not as a swarm manager
	if code, body := nodeAction(t, s, "POST", "/api/swarm/previews/workers"); code != 409 {
		t.Fatalf("not a manager: %d %s", code, body)
	}
}

// negative control: read previewsOn as "any" whatever the label says → a manager labelled off reads
// as running previews, and the UI offers to switch to what is already set.
func TestSwarmReadsPreviewsOnFromTheLabel(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	inner := f.Answer
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.TrimSpace(cmd) == swarm.PreviewsOnCmd {
			return exec.Result{OK: true, Stdout: "off\n"}, true
		}
		return inner(cmd)
	}
	s.host = f
	if code, body := nodeAction(t, s, "GET", "/api/swarm"); code != 200 || !strings.Contains(body, `"previewsOn": "workers"`) {
		t.Fatalf("%d %s", code, body)
	}
}

// negative control: drop the `node.ID == view.Self` refusal → draining this manager answers 200 and
// `docker node update --availability drain` is recorded.
func TestDrainRefusesThisManager(t *testing.T) {
	s := signalsServer(t)
	f := signalsShim(t, false)
	s.host = f
	if code, body := nodeAction(t, s, "POST", "/api/swarm/nodes/n1abcdef01234567/drain"); code != 409 {
		t.Fatalf("%d %s", code, body)
	}
	for _, c := range f.Commands() {
		if strings.Contains(c, "node update") {
			t.Fatalf("ran %q", c)
		}
	}
}
