package scaler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const fits = "no suitable node (insufficient resources on 2 nodes)"

func TestQualifies(t *testing.T) {
	// negative control: accept any clause after `no suitable node (` → the scheduling-constraints row passes and this fails
	for reason, want := range map[string]bool{
		"no suitable node (insufficient resources on 2 nodes)":                                                 true,
		"no suitable node (insufficient resources on 1 node)":                                                  true,
		"no suitable node (1 node not available for new tasks)":                                                true,
		"no suitable node (insufficient resources on 2 nodes; 1 node not available for new tasks)":             true,
		"no suitable node (scheduling constraints not satisfied on 1 node; insufficient resources on 2 nodes)": false,
		"no suitable node":    false,
		"no suitable node ()": false,
		"never allocated: no suitable node (insufficient resources on 2 nodes)": false,
	} {
		if got := Qualifies(reason); got != want {
			t.Errorf("Qualifies(%q) = %v, want %v", reason, got, want)
		}
	}
}

func TestScaleUpOnStuck(t *testing.T) {
	// negative control: post the lock report after the apply → the first report naming the worker sees one apply and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.Vars["undeclared"] = "x" })
	h.stuck("pr-42_api", fits)
	h.tick()

	if n := h.applies(); n != 1 {
		t.Fatalf("applies = %d, want 1", n)
	}
	names := h.workerNames()
	if len(names) != 1 || !regexp.MustCompile(`^pstack-[0-9a-f]{6}$`).MatchString(names[0]) {
		t.Fatalf("workers = %v", names)
	}
	name := names[0]
	var workers map[string]map[string]string
	json.Unmarshal(h.tfvars()["workers"], &workers)
	entry := workers[name]
	if !strings.Contains(entry["user_data"], "hostname: "+name+"\npreserve_hostname: false\n") || entry["server_type"] != "cx22" || entry["location"] != "fsn1" {
		t.Errorf("entry = %v", entry)
	}
	v := h.tfvars()
	if keys := []string{}; json.Unmarshal(v["ssh_keys"], &keys) != nil || len(keys) != 1 || keys[0] != "k1" {
		t.Errorf(`ssh_keys = %s, want the JSON list ["k1"] (P-R6)`, v["ssh_keys"])
	}
	if string(v["manager_address"]) != `"203.0.113.10"` || string(v["host_id"]) != `"`+h.state().HostID+`"` {
		t.Errorf("manager_address = %s, host_id = %s", v["manager_address"], v["host_id"])
	}
	for _, k := range []string{"undeclared", "network_id"} {
		if _, ok := v[k]; ok {
			t.Errorf("%s written: not declared, or empty", k)
		}
	}
	h.with(func(a *fakeAPI) {
		if q := a.joinQueries; len(q) != 1 || q[0] != "format=cloud-config&distro=debian" {
			t.Errorf("join queries = %v", q)
		}
	})

	// The owned name reaches pstack before the apply; scaler.created only after it.
	for _, r := range h.reports() {
		if len(r.Report.Workers) > 0 && r.Report.Workers[0].Name == name {
			if r.Applies != 0 || r.Report.Event != nil {
				t.Errorf("first report naming %s: applies %d, event %v", name, r.Applies, r.Report.Event)
			}
			if r.Report.Locked == nil || r.Report.Locked.Preset != "hetzner" || len(r.Report.Locked.Shared) != 2 {
				t.Errorf("lock = %+v, want hetzner with both shared vars (W-R36)", r.Report.Locked)
			}
			break
		}
	}
	created := h.events("scaler.created")
	if len(created) != 1 || string(created[0].Data) != `{"name":"`+name+`","settings":{"location":"fsn1","server_type":"cx22"}}` {
		t.Errorf("scaler.created = %+v", created)
	}
	w := h.worker(name)
	if w.AppliedAt != h.ms() || w.ApplyingAt != 0 || len(w.StuckServices) != 1 {
		t.Errorf("worker = %+v", w)
	}
	if got := h.last().Workers; len(got) != 1 || got[0].State != "booting" {
		t.Errorf("reported workers = %+v", got)
	}
	h.noSecrets()

	t.Run("custom preset without the filled variables", func(t *testing.T) {
		// negative control: write manager_address and host_id whether declared or not → this fails
		h := newHarness(t)
		h.with(func(a *fakeAPI) {
			a.cfg.Preset = "tiny"
			a.cfg.Files = map[string]string{"main.tf": "variable \"workers\" {\n  type = map(object({ user_data = string, size = string }))\n}\n"}
			a.cfg.Vars = map[string]string{"size": "s1"}
		})
		h.stuck("pr-42_api", fits)
		h.tick()
		b, _ := os.ReadFile(filepath.Join(h.stateDir, "tofu", "tiny", "workers.auto.tfvars.json"))
		var v map[string]json.RawMessage
		json.Unmarshal(b, &v)
		if len(v) != 1 || v["workers"] == nil {
			t.Errorf("tfvars keys = %v, want workers only", v)
		}
		h.with(func(a *fakeAPI) {
			if q := a.joinQueries; len(q) != 1 || !strings.HasSuffix(q[0], "distro=ubuntu") {
				t.Errorf("join queries = %v, want the default distro", q)
			}
		})
	})
}

func TestMinFill(t *testing.T) {
	// negative control: drop the booting guard → the second tick creates a second worker and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.Min, a.cfg.CooldownMin = 2, 0 })
	h.tick()
	if h.applies() != 1 {
		t.Fatalf("applies = %d, want 1", h.applies())
	}
	first := h.workerNames()[0]
	h.advance(Tick)
	h.tick()
	if h.applies() != 1 || !strings.Contains(h.last().LastAction, first+" is booting") {
		t.Fatalf("applies = %d, lastAction %q", h.applies(), h.last().LastAction)
	}
	h.node(first, "ready", 0, 0)
	h.advance(Tick)
	h.tick()
	if h.applies() != 2 || len(h.workerNames()) != 2 {
		t.Fatalf("applies = %d, workers %v", h.applies(), h.workerNames())
	}
	h.node(h.workerNames()[0], "ready", 0, 0)
	h.node(h.workerNames()[1], "ready", 0, 0)
	h.advance(Tick)
	h.tick()
	if h.applies() != 2 {
		t.Errorf("min reached, still applied: %d", h.applies())
	}
}

func TestScaleUpGuards(t *testing.T) {
	cases := []struct {
		name, want string
		setup      func(h *harness)
	}{
		// negative control (each row): remove that guard → an apply happens and the row fails
		{"cooldown", "cooldown", func(h *harness) {
			h.own("pstack-aaaaaa")
			h.node("pstack-aaaaaa", "ready", 2, 0)
			h.edit(func(s *State) { s.LastScaleUp = h.ms() - time.Minute.Milliseconds() })
		}},
		{"booting", "pstack-aaaaaa is booting", func(h *harness) { h.own("pstack-aaaaaa") }},
		{"max", "max 1 reached", func(h *harness) {
			h.with(func(a *fakeAPI) { a.cfg.Max = 1 })
			h.own("pstack-aaaaaa")
			h.node("pstack-aaaaaa", "ready", 2, 0)
		}},
		{"never joined", "2 workers never joined", func(h *harness) {
			h.tick()
			h.edit(func(s *State) { s.NeverJoined = 2 })
		}},
		{"ready and empty", "pstack-aaaaaa is ready and empty", func(h *harness) {
			h.own("pstack-aaaaaa")
			h.node("pstack-aaaaaa", "ready", 0, 0)
		}},
		{"does not fit", "does not fit: pr-42_api", func(h *harness) {
			h.tick()
			h.edit(func(s *State) { s.DoesNotFit = []string{"pr-42_api"} })
		}},
		{"swarm false", "not a swarm manager", func(h *harness) { h.with(func(a *fakeAPI) { a.sig.Swarm = false }) }},
		{"reachable false", "docker did not answer", func(h *harness) { h.with(func(a *fakeAPI) { a.sig.Reachable = false }) }},
		{"config 401", "config: unauthorized", func(h *harness) { h.with(func(a *fakeAPI) { a.cfgStatus = 401 }) }},
		{"no preset", "no preset set", func(h *harness) { h.with(func(a *fakeAPI) { a.cfg.Preset = "" }) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// negative control: see the table's comment
			h := newHarness(t)
			c.setup(h)
			h.stuck("pr-42_api", fits)
			h.advance(Tick)
			h.tick()
			if h.applies() != 0 {
				t.Errorf("applies = %d, want 0", h.applies())
			}
			if got := h.last().LastAction; !strings.Contains(got, c.want) {
				t.Errorf("lastAction = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("one max event per stuck episode", func(t *testing.T) {
		// negative control: queue the max event on every tick → three events and this fails
		h := newHarness(t)
		h.with(func(a *fakeAPI) { a.cfg.Max = 1 })
		h.own("pstack-aaaaaa")
		h.node("pstack-aaaaaa", "ready", 2, 0)
		h.stuck("pr-42_api", fits)
		for range 3 {
			h.advance(Tick)
			h.tick()
		}
		got := h.events("scaler.failed")
		if len(got) != 1 || string(got[0].Data) != `{"action":"max","error":"max 1 reached","attempt":1}` {
			t.Fatalf("events = %+v", got)
		}
		h.with(func(a *fakeAPI) { a.sig.Stuck = nil })
		h.advance(Tick)
		h.tick()
		h.stuck("pr-42_api", fits)
		h.advance(Tick)
		h.tick()
		if n := len(h.events("scaler.failed")); n != 2 {
			t.Errorf("a new episode: %d max events, want 2", n)
		}
	})
}

func TestFrozenClocks(t *testing.T) {
	// negative control: skip the shift on a frozen tick → the worker is never-joined and cooldown has passed
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.edit(func(s *State) { s.LastScaleUp = h.ms() })
	h.stuck("pr-42_api", fits)
	h.with(func(a *fakeAPI) { a.sig.Reachable = false })
	for range 20 {
		h.advance(Tick)
		h.tick()
	}
	h.with(func(a *fakeAPI) { a.sig.Reachable = true })
	h.advance(Tick)
	h.tick()
	if w := h.worker("pstack-aaaaaa"); w.Removing {
		t.Errorf("declared %s after 30 s of live time", w.Reason)
	}
	if got := h.last().LastAction; got != "cooldown" {
		t.Errorf("lastAction = %q, want cooldown", got)
	}
}

func TestNeverJoined(t *testing.T) {
	// negative control: never count never-joined workers → the third scale-up happens and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	h.stuck("pr-42_api", fits)
	h.tick()
	first := h.workerNames()[0]
	h.advance(BootTimeout)
	h.tick()
	w := h.worker(first)
	if !w.Removing || w.Reason != "never-joined" || h.state().NeverJoined != 1 {
		t.Fatalf("worker = %+v, count %d", w, h.state().NeverJoined)
	}
	if ev := h.events("scaler.failed"); len(ev) != 1 || !strings.HasPrefix(string(ev[0].Data), `{"action":"never-joined","error":"`+first+` not ready after 10 min","attempt":1}`) {
		t.Errorf("events = %+v", ev)
	}

	// It counts toward the backoff: a second one after 1 min.
	h.advance(time.Minute)
	h.tick()
	if h.applies() != 2 {
		t.Fatalf("applies = %d, want 2", h.applies())
	}
	h.advance(BootTimeout)
	h.tick()
	if h.state().NeverJoined != 2 {
		t.Fatalf("count = %d", h.state().NeverJoined)
	}
	h.advance(time.Hour)
	h.tick()
	if h.applies() != 2 || !strings.Contains(h.last().LastAction, "2 workers never joined") {
		t.Fatalf("applies = %d, lastAction %q", h.applies(), h.last().LastAction)
	}

	// A settings change resets the count.
	h.with(func(a *fakeAPI) { a.cfg.Vars["server_type"] = "cx32" })
	h.advance(Tick)
	h.tick()
	if h.applies() != 3 || h.state().NeverJoined != 0 {
		t.Errorf("applies = %d, count %d", h.applies(), h.state().NeverJoined)
	}
}

func TestDoesNotFit(t *testing.T) {
	// negative control: never mark does-not-fit → the tick after cooldown buys a second worker and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	h.stuck("pr-42_api", fits)
	h.tick()
	name := h.workerNames()[0]
	h.advance(Tick)
	h.node(name, "ready", 0, 0)
	h.tick()
	if len(h.state().DoesNotFit) != 0 {
		t.Fatal("marked before a full empty tick")
	}
	h.node(name, "ready", 0, Tick)
	h.advance(Tick)
	h.tick()
	if got := h.state().DoesNotFit; len(got) != 1 || got[0] != "pr-42_api" {
		t.Fatalf("DoesNotFit = %v", got)
	}
	if ev := h.events("scaler.failed"); len(ev) != 1 || string(ev[0].Data) != `{"action":"does-not-fit","error":"pr-42_api does not fit a new worker","attempt":1}` {
		t.Errorf("events = %+v", ev)
	}
	if got := h.last().DoesNotFit; len(got) != 1 {
		t.Errorf("report doesNotFit = %v", got)
	}
	h.advance(time.Hour)
	h.node(name, "ready", 0, time.Hour)
	h.tick()
	if h.applies() != 1 {
		t.Fatalf("bought a second worker for a service that does not fit")
	}

	// A per-worker var change clears it; the old empty worker no longer blocks.
	h.with(func(a *fakeAPI) { a.cfg.Vars["server_type"] = "cx52" })
	h.advance(Tick)
	h.tick()
	if len(h.state().DoesNotFit) != 0 || h.applies() != 2 {
		t.Errorf("DoesNotFit = %v, applies %d", h.state().DoesNotFit, h.applies())
	}
}

func TestDoesNotFitNeedsAReadyTick(t *testing.T) {
	// negative control: measure from emptySince alone (or ignore state) → marked on the first ready tick, or while down
	notMarked := func(t *testing.T, h *harness, when string) {
		t.Helper()
		if got := h.state().DoesNotFit; len(got) != 0 {
			t.Fatalf("%s: DoesNotFit = %v", when, got)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(h *harness, name string)
	}{
		{"empty before ready", func(h *harness, name string) {
			h.node(name, "down", 0, 0)
			h.advance(2 * Tick)
			h.tick()
		}},
		{"down after ready", func(h *harness, name string) {
			h.node(name, "ready", 0, 0)
			h.tick()
			h.node(name, "down", 0, Tick/2)
			h.advance(Tick)
			h.tick()
			notMarked(h.t, h, "down")
			h.advance(Tick)
			h.tick()
		}},
		{"drained after ready", func(h *harness, name string) {
			h.node(name, "ready", 0, 0)
			h.tick()
			h.with(func(a *fakeAPI) { a.sig.Nodes[len(a.sig.Nodes)-1].Availability = "drain" })
			h.advance(Tick)
			h.tick()
			notMarked(h.t, h, "drained")
			h.advance(Tick)
			h.tick()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.stuck("pr-42_api", fits)
			h.tick()
			name := h.workerNames()[0]
			h.advance(Tick)
			tc.setup(h, name)
			notMarked(h.t, h, "before ready")
			// Back to ready and active: swarm's empty clock is old, the ready run is new.
			h.with(func(a *fakeAPI) {
				for i := range a.sig.Nodes {
					if a.sig.Nodes[i].Hostname == name {
						a.sig.Nodes[i].State, a.sig.Nodes[i].Availability = "ready", "active"
					}
				}
			})
			h.tick()
			notMarked(h.t, h, "first ready tick")
			h.advance(Tick)
			h.tick()
			if got := h.state().DoesNotFit; len(got) != 1 || got[0] != "pr-42_api" {
				t.Fatalf("after a ready tick: DoesNotFit = %v", got)
			}
		})
	}
}

func TestFailedApplyRetries(t *testing.T) {
	// negative control: treat a missing, never-applied worker as lost → it becomes removing and a new name is created
	// negative control: retry with apply instead of applyNew → fewer lock reports than applies
	// negative control (W-R22): drop Scrub from Tofu.Run and Loop.scrub → noSecrets finds all three secrets
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	escaped, _ := json.Marshal(testKey)
	fail := "Error: creating server: quota exceeded\n" +
		"token " + testToken + "\n" +
		"decoded " + testKey +
		"escaped " + string(escaped[1:len(escaped)-1]) + "\n" +
		"join SWMTKN-1-abc-def\n"
	failPath := filepath.Join(h.tofuDir, "fail")
	os.WriteFile(failPath, []byte(fail), 0o600)
	h.stuck("pr-42_api", fits)
	h.tick()

	name := h.workerNames()[0]
	s := h.state()
	if s.NextTryAt != h.ms()+time.Minute.Milliseconds() || s.Failures != 1 {
		t.Fatalf("NextTryAt = %d (now %d), failures %d", s.NextTryAt, h.ms(), s.Failures)
	}
	ev := h.events("scaler.failed")
	if len(ev) != 1 || !strings.HasPrefix(string(ev[0].Data), `{"action":"apply","error":"`) || !strings.Contains(string(ev[0].Data), "quota exceeded") || !strings.HasSuffix(string(ev[0].Data), `"attempt":1}`) {
		t.Fatalf("events = %+v", ev)
	}
	if !strings.Contains(h.last().LastError, "quota exceeded") || h.last().NextRetryAt != s.NextTryAt {
		t.Errorf("report = %+v", h.last())
	}
	h.noSecrets()

	// Backoff doubles, survives a restart, and stops at 30 min.
	for i, want := range []time.Duration{2 * time.Minute, 4 * time.Minute} {
		h.restart()
		h.advance(30 * time.Second)
		before := h.calls("apply -auto")
		h.tick()
		if h.calls("apply -auto") != before {
			t.Fatalf("retry %d: applied during backoff", i)
		}
		h.advance(time.Duration(h.state().NextTryAt-h.ms()) * time.Millisecond)
		h.tick()
		if got := h.state().NextTryAt - h.ms(); got != want.Milliseconds() {
			t.Errorf("retry %d: backoff %d ms, want %v", i, got, want)
		}
	}
	h.edit(func(s *State) { s.Failures = 9 })
	h.advance(time.Hour)
	h.tick()
	if got := h.state().NextTryAt - h.ms(); got != BackoffMax.Milliseconds() {
		t.Errorf("backoff %d ms, want %v", got, BackoffMax)
	}

	os.Remove(failPath)
	h.advance(BackoffMax)
	h.tick()
	if got := h.workerNames(); len(got) != 1 || got[0] != name {
		t.Fatalf("retried as %v, want %s", got, name)
	}
	w := h.worker(name)
	s = h.state()
	if w.AppliedAt != h.ms() || w.Removing || len(s.Orphans) != 0 || s.Failures != 0 || s.NextTryAt != 0 || len(s.Workers) != 1 {
		t.Errorf("worker = %+v, state = %+v", w, s)
	}
	if h.last().LastError != "" || len(h.events("scaler.created")) != 1 {
		t.Errorf("lastError %q, created events %d", h.last().LastError, len(h.events("scaler.created")))
	}
	// Every apply attempt, retries included, follows a report naming the worker.
	lockReports := 0
	for _, r := range h.reports() {
		if r.Report.LastAction == "creating "+name && r.Applies == lockReports {
			lockReports++
		}
	}
	if lockReports != h.calls("apply -auto") {
		t.Errorf("%d lock reports before %d applies", lockReports, h.calls("apply -auto"))
	}
	h.noSecrets()
}

func TestFailedValidateBlocks(t *testing.T) {
	// negative control: record Inited before validate → the next tick skips validate and this fails
	h := newHarness(t)
	h.tick()
	h.edit(func(s *State) { s.LastScaleUp = h.ms() })
	h.with(func(a *fakeAPI) { a.cfg.Files = map[string]string{"main.tf": a.cfg.Files["main.tf"] + "\n# v2\n"} })
	os.WriteFile(filepath.Join(h.tofuDir, "fail-validate"), []byte("Error: Unsupported argument "+testToken+"\n"), 0o600)
	h.stuck("pr-42_api", fits)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	lastScaleUp := h.state().LastScaleUp
	h.advance(Tick)
	h.tick()
	s := h.state()
	if h.applies() != 0 || s.Failures != 0 || s.NextTryAt != 0 {
		t.Errorf("applies %d, failures %d, nextTryAt %d", h.applies(), s.Failures, s.NextTryAt)
	}
	if !strings.Contains(h.last().LastError, "validate: Error: Unsupported argument") {
		t.Errorf("lastError = %q", h.last().LastError)
	}
	if s.LastScaleUp != lastScaleUp+Tick.Milliseconds() {
		t.Errorf("clock advanced: LastScaleUp %d, want %d", s.LastScaleUp, lastScaleUp+Tick.Milliseconds())
	}
	h.noSecrets()

	// Fixed → the next tick validates again, then applies.
	os.Remove(filepath.Join(h.tofuDir, "fail-validate"))
	validates := h.calls("validate")
	h.advance(Tick)
	h.tick()
	if h.calls("validate") != validates+1 || h.applies() != 1 || h.last().LastError != "" {
		t.Errorf("validates %d (was %d), applies %d, lastError %q", h.calls("validate"), validates, h.applies(), h.last().LastError)
	}
}

func TestLostBeforeApply(t *testing.T) {
	// negative control: skip the refresh before apply → the lost worker stays in the map and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 2, 0)
	os.WriteFile(filepath.Join(h.tofuDir, "deleted"), []byte("pstack-aaaaaa\n"), 0o600)
	h.stuck("pr-42_api", fits)
	h.advance(Tick)
	h.tick()
	if w := h.worker("pstack-aaaaaa"); !w.Removing || w.Reason != "lost" {
		t.Errorf("worker = %+v", w)
	}
	if got := h.workerNames(); len(got) != 1 || got[0] == "pstack-aaaaaa" {
		t.Errorf("apply map = %v", got)
	}
	calls := readCalls(h.tofuDir)
	i := len(calls) - 1
	if calls[i-2] != "apply -refresh-only -auto-approve -input=false -no-color" || calls[i-1] != "state list -no-color" || calls[i] != "apply -auto-approve -input=false -no-color" {
		t.Errorf("calls = %v", calls)
	}
}

func TestLockedConfigRefused(t *testing.T) {
	// negative control: skip the shared-var check → the apply runs and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 2, 0)
	h.with(func(a *fakeAPI) { a.cfg.Vars["ssh_keys"] = `["k2"]`; a.cfg.CooldownMin = 0 })
	h.stuck("pr-42_api", fits)
	h.advance(Tick)
	h.tick()
	if h.applies() != 0 || h.last().LastError != "locked to hetzner: ssh_keys differs" {
		t.Errorf("applies %d, lastError %q", h.applies(), h.last().LastError)
	}
}

func TestUpgradedPresetKept(t *testing.T) {
	// negative control: sync the new files while workers are owned → main.tf in the state dir changes and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 2, 0)
	var old string
	h.with(func(a *fakeAPI) {
		old = a.cfg.Files["main.tf"]
		a.cfg.Files = map[string]string{"main.tf": old + "\n# upgraded\n", ".terraform.lock.hcl": a.cfg.Files[".terraform.lock.hcl"]}
		a.cfg.CooldownMin = 0
	})
	inits := h.calls("init")
	h.stuck("pr-42_api", fits)
	h.advance(Tick)
	h.tick()
	mainTF := filepath.Join(h.stateDir, "tofu", "hetzner", "main.tf")
	if b, _ := os.ReadFile(mainTF); string(b) != old || h.calls("init") != inits {
		t.Errorf("files synced while workers are owned")
	}
	if h.applies() != 1 || !strings.HasSuffix(h.last().LastAction, "; preset files changed; applied after workers reach zero") {
		t.Errorf("applies %d, lastAction %q", h.applies(), h.last().LastAction)
	}

	// Owned workers reach zero (Task 7 forgets them) → the new files are synced and initialised.
	h.edit(func(s *State) { s.Workers, s.Locked = nil, nil })
	h.with(func(a *fakeAPI) { a.sig.Stuck = nil })
	h.advance(Tick)
	h.tick()
	if b, _ := os.ReadFile(mainTF); !strings.HasSuffix(string(b), "# upgraded\n") || h.calls("init") != inits+1 {
		t.Errorf("new files not synced once workers reached zero")
	}
}

func TestPrivateManagerNeedsNetwork(t *testing.T) {
	// negative control: drop the private-address check → the first tick applies and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { mgr := "10.0.0.1:2377"; a.managerAddr = &mgr })
	h.stuck("pr-42_api", fits)
	h.tick()
	if h.applies() != 0 || !strings.Contains(h.last().LastError, "network_id") {
		t.Fatalf("applies %d, lastError %q", h.applies(), h.last().LastError)
	}
	h.with(func(a *fakeAPI) { a.cfg.Vars["network_id"] = "123" })
	h.advance(Tick)
	h.tick()
	if h.applies() != 1 || h.last().LastError != "" {
		t.Errorf("applies %d, lastError %q", h.applies(), h.last().LastError)
	}
	if string(h.tfvars()["network_id"]) != `"123"` || string(h.tfvars()["manager_address"]) != `"10.0.0.1"` {
		t.Errorf("tfvars = %v", h.tfvars())
	}

	t.Run("an IPv6 advertise address", func(t *testing.T) {
		// negative control: accept any address → the tick applies and this fails
		h := newHarness(t)
		h.with(func(a *fakeAPI) { mgr := "[2001:db8::1]:2377"; a.managerAddr = &mgr })
		h.stuck("pr-42_api", fits)
		h.tick()
		if h.applies() != 0 || h.last().LastError != "manager advertises 2001:db8::1: the scaler needs an IPv4 advertise address" {
			t.Errorf("applies %d, lastError %q", h.applies(), h.last().LastError)
		}
	})
}

func TestTfvarsNeverOnArgv(t *testing.T) {
	// negative control: pass the workers map as -var on the apply → calls.log carries it and this fails
	h := newHarness(t)
	h.stuck("pr-42_api", fits)
	h.tick()
	if h.applies() != 1 {
		t.Fatalf("applies = %d", h.applies())
	}
	log := strings.Join(readCalls(h.tofuDir), "\n")
	for _, s := range []string{"cx22", "fsn1", "k1", "SWMTKN", "hostname", "pstack-", testToken, "203.0.113.10", "-var"} {
		if strings.Contains(log, s) {
			t.Errorf("argv carries %q:\n%s", s, log)
		}
	}
	fi, err := os.Stat(filepath.Join(h.stateDir, "tofu", "hetzner", "workers.auto.tfvars.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("tfvars mode = %v, %v", fi, err)
	}
}
