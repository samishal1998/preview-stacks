package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
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
		"docker info --format '{{json .Swarm}}'":    `{"LocalNodeState":"active","ControlAvailable":true,"NodeID":"n1abcdef01234567","NodeAddr":"10.0.0.1"}`,
		"docker node ls --format '{{json .}}'":      readFile("node-ls.jsonl"),
		"docker service ls --format '{{json .}}'":   readFile("service-ls.jsonl"),
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
