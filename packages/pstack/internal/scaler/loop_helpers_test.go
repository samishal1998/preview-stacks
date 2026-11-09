package scaler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The secrets the fixtures carry (W-R22): a credential value, a JSON credential's private_key
// leaf, and a swarm join token in the join cloud-config.
const (
	testToken = "hc-secret-1234567"
	testKey   = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n"
	testJoin  = "#cloud-config\nruncmd:\n  - docker swarm join --token SWMTKN-1-abc-def 203.0.113.10:2377\n"
)

// secretBodies are the parts of the secrets that must never leave the scaler.
var secretBodies = []string{testToken, "MIIEvQIBADANBgkq", "abc-def"}

// postedReport is one POST /api/scaler/report as the fake API received it. Applies is how many
// `apply -auto-approve` lines the fake tofu had logged when it arrived.
type postedReport struct {
	Raw     string
	Report  Report
	Applies int
}

// fakeAPI is pstack's side of the scaler's HTTP: config, signals, swarm, join, the node routes,
// and the reports it records. Guarded by mu; tests mutate it between ticks.
type fakeAPI struct {
	mu          sync.Mutex
	cfg         Config
	cfgStatus   int // non-zero: GET /api/scaler/config answers this status
	sig         sigView
	managerAddr *string
	joinQueries []string
	nodeCalls   []string
	reports     []postedReport
	tofuDir     string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer root" {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"unauthorized"}`)
		return
	}
	asJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	path := r.URL.Path
	switch {
	case r.Method == "GET" && path == "/api/scaler/config":
		if f.cfgStatus != 0 {
			w.WriteHeader(f.cfgStatus)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		asJSON(f.cfg)
	case r.Method == "GET" && path == "/api/signals":
		asJSON(f.sig)
	case r.Method == "GET" && path == "/api/swarm":
		asJSON(map[string]*string{"managerAddr": f.managerAddr})
	case r.Method == "GET" && path == "/api/swarm/join":
		f.joinQueries = append(f.joinQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, testJoin)
	case strings.HasPrefix(path, "/api/swarm/nodes/"):
		f.nodeCalls = append(f.nodeCalls, r.Method+" "+path)
		asJSON(map[string]bool{"ok": true})
	case r.Method == "POST" && path == "/api/scaler/report":
		b, _ := io.ReadAll(r.Body)
		var rep Report
		if err := json.Unmarshal(b, &rep); err != nil {
			w.WriteHeader(400)
			return
		}
		f.reports = append(f.reports, postedReport{Raw: string(b), Report: rep, Applies: countApplies(f.tofuDir)})
		asJSON(map[string]bool{"ok": true})
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"no route"}`)
	}
}

func countApplies(tofuDir string) int {
	n := 0
	for _, c := range readCalls(tofuDir) {
		if strings.HasPrefix(c, "apply ") && !strings.Contains(c, "-refresh-only") {
			n++
		}
	}
	return n
}

func readCalls(tofuDir string) []string {
	b, _ := os.ReadFile(filepath.Join(tofuDir, "calls.log"))
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// harness is one scaler under test: a fake API, a fake tofu, a state dir and a fake clock.
type harness struct {
	t        *testing.T
	api      *fakeAPI
	tofu     string
	tofuDir  string
	stateDir string
	now      time.Time
	log      bytes.Buffer
	loop     *Loop
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tofu, tofuDir := fakeTofu(t)
	gcp, _ := json.Marshal(map[string]string{"type": "service_account", "private_key": testKey})
	mgr := "203.0.113.10:2377"
	h := &harness{
		t: t, tofu: tofu, tofuDir: tofuDir, stateDir: t.TempDir(),
		now: time.UnixMilli(1_800_000_000_000),
		api: &fakeAPI{
			tofuDir:     tofuDir,
			managerAddr: &mgr,
			cfg: Config{
				Settings: Settings{
					Preset: "hetzner",
					Vars:   map[string]string{"server_type": "cx22", "location": "fsn1", "ssh_keys": `["k1"]`, "network_id": ""},
					Min:    0, Max: 3, CooldownMin: 5, GraceMin: 15, Dismissed: []string{},
				},
				Credentials: map[string]string{"HCLOUD_TOKEN": testToken, "GOOGLE_CREDENTIALS": string(gcp)},
				Files:       Builtins()["hetzner"],
			},
			sig: sigView{Swarm: true, Reachable: true, Nodes: []sigNode{
				{ID: "mgr1", Hostname: "manager", Role: "manager", State: "ready", Availability: "active"},
			}, Stuck: []sigStuck{}},
		},
	}
	srv := httptest.NewServer(h.api)
	t.Cleanup(srv.Close)
	h.loop = h.newLoop(srv)
	return h
}

func (h *harness) newLoop(srv *httptest.Server) *Loop {
	h.t.Helper()
	l, err := New(Options{
		API:      Client{Base: srv.URL, Token: "root", HTTP: srv.Client()},
		StateDir: h.stateDir, Tofu: h.tofu,
		Now: func() time.Time { return h.now },
		Log: &h.log,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

// restart is a new Loop on the same state dir, as after a process restart.
func (h *harness) restart() {
	h.t.Helper()
	l, err := New(Options{API: h.loop.o.API, StateDir: h.stateDir, Tofu: h.tofu, Now: func() time.Time { return h.now }, Log: &h.log})
	if err != nil {
		h.t.Fatal(err)
	}
	h.loop = l
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.loop.Tick(); err != nil {
		h.t.Fatalf("Tick: %v", err)
	}
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }
func (h *harness) ms() int64               { return h.now.UnixMilli() }

func (h *harness) with(f func(a *fakeAPI)) {
	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	f(h.api)
}

func (h *harness) stuck(service, reason string) {
	h.with(func(a *fakeAPI) {
		a.sig.Stuck = append(a.sig.Stuck, sigStuck{Task: service + ".1", Service: service, Stack: "pr-42", Reason: reason})
	})
}

// node puts or replaces a worker node in signals. emptyFor > 0 sets emptySince that long ago.
func (h *harness) node(name, state string, tasks int, emptyFor time.Duration) {
	h.with(func(a *fakeAPI) {
		n := sigNode{ID: "id-" + name, Hostname: name, Role: "worker", State: state, Availability: "active", Tasks: tasks}
		if tasks == 0 {
			at := h.now.Add(-emptyFor).UnixMilli()
			n.EmptySince = &at
		}
		a.sig.Nodes = slices.DeleteFunc(a.sig.Nodes, func(x sigNode) bool { return x.Hostname == name })
		a.sig.Nodes = append(a.sig.Nodes, n)
	})
}

func (h *harness) applies() int { return countApplies(h.tofuDir) }

func (h *harness) calls(prefix string) int {
	n := 0
	for _, c := range readCalls(h.tofuDir) {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (h *harness) state() *State {
	h.t.Helper()
	s, err := LoadState(h.stateDir)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// edit changes scaler.json and restarts the loop on it.
func (h *harness) edit(f func(s *State)) {
	h.t.Helper()
	s := h.state()
	f(s)
	if err := s.Save(h.stateDir); err != nil {
		h.t.Fatal(err)
	}
	h.restart()
}

// own seeds applied, locked workers into scaler.json and the fake tofu's state, after one idle
// tick has initialised the preset.
func (h *harness) own(names ...string) {
	h.t.Helper()
	h.tick()
	h.edit(func(s *State) {
		for _, n := range names {
			s.Workers = append(s.Workers, OwnedWorker{
				Name: n, Vars: map[string]string{"server_type": "cx22", "location": "fsn1"},
				CreatedAt: h.ms(), AppliedAt: h.ms(),
			})
		}
		s.Locked = &Locked{Preset: "hetzner", FilesHash: s.Inited["hetzner"], Shared: map[string]string{"ssh_keys": `["k1"]`, "network_id": ""}}
	})
	b, _ := json.Marshal(names)
	if err := os.WriteFile(filepath.Join(h.tofuDir, "state.json"), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) reports() []postedReport {
	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	return slices.Clone(h.api.reports)
}

func (h *harness) last() Report {
	h.t.Helper()
	r := h.reports()
	if len(r) == 0 {
		h.t.Fatal("no report posted")
	}
	return r[len(r)-1].Report
}

// events is every event posted so far, of the given name.
func (h *harness) events(name string) []Event {
	var out []Event
	for _, r := range h.reports() {
		if r.Report.Event != nil && r.Report.Event.Name == name {
			out = append(out, *r.Report.Event)
		}
	}
	return out
}

// tfvars is the last workers.auto.tfvars.json written for the hetzner preset.
func (h *harness) tfvars() map[string]json.RawMessage {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.stateDir, "tofu", "hetzner", "workers.auto.tfvars.json"))
	if err != nil {
		h.t.Fatal(err)
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) workerNames() []string {
	var w map[string]json.RawMessage
	if err := json.Unmarshal(h.tfvars()["workers"], &w); err != nil {
		h.t.Fatal(err)
	}
	names := []string{}
	for n := range w {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (h *harness) worker(name string) OwnedWorker {
	h.t.Helper()
	for _, w := range h.state().Workers {
		if w.Name == name {
			return w
		}
	}
	h.t.Fatalf("no owned worker %s", name)
	return OwnedWorker{}
}

// noSecrets fails when any secret body is in a posted report (events included) or the log.
func (h *harness) noSecrets() {
	h.t.Helper()
	for _, r := range h.reports() {
		for _, s := range append(secretBodies, "user_data", "userData", "#cloud-config") {
			if strings.Contains(r.Raw, s) {
				h.t.Errorf("report carries %q: %s", s, r.Raw)
			}
		}
	}
	for _, s := range secretBodies {
		if strings.Contains(h.log.String(), s) {
			h.t.Errorf("log carries %q:\n%s", s, h.log.String())
		}
	}
}
