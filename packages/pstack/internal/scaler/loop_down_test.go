package scaler

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// removing marks an owned worker removing, as steps 3, 5 and 6 would, and restarts on it.
func (h *harness) removing(name, reason string) {
	h.t.Helper()
	h.edit(func(s *State) {
		for i := range s.Workers {
			if s.Workers[i].Name == name {
				s.Workers[i].Removing, s.Workers[i].Reason = true, reason
			}
		}
	})
}

func (h *harness) owned() (names []string) {
	for _, w := range h.state().Workers {
		if !w.Removing {
			names = append(names, w.Name)
		}
	}
	return names
}

func (h *harness) step() { h.t.Helper(); h.advance(Tick); h.tick() }

func TestRemovalSteps(t *testing.T) {
	// negative control: let step 4 run on the drain tick (or apply before draining) → tick 1 applies and this fails
	h := newHarness(t)
	h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 0, time.Hour)
	h.removing("pstack-aaaaaa", "empty")
	h.stuck("pr-42_api", fits) // the drain is the tick's action: no create either

	h.step()
	if got := h.nodeCalls(); len(got) != 1 || got[0] != "POST /api/swarm/nodes/id-pstack-aaaaaa/drain" {
		t.Fatalf("tick 1 node calls = %v", got)
	}
	if h.calls("apply") != 0 {
		t.Fatalf("tick 1 ran an apply while draining: %v", readCalls(h.tofuDir))
	}
	h.with(func(a *fakeAPI) { a.sig.Stuck = nil })

	h.step()
	if h.applies() != 1 || slices.Contains(h.workerNames(), "pstack-aaaaaa") || !h.worker("pstack-aaaaaa").OutOfState {
		t.Fatalf("tick 2: applies %d, map %v", h.applies(), h.workerNames())
	}
	h.step() // still ready: waiting for down is not an action
	if len(h.nodeCalls()) != 1 || h.applies() != 1 {
		t.Fatalf("waiting: node calls %v, applies %d", h.nodeCalls(), h.applies())
	}

	h.setNode("pstack-aaaaaa", func(n *sigNode) { n.State = "down" })
	h.step()
	if got := h.nodeCalls(); len(got) != 2 || got[1] != "DELETE /api/swarm/nodes/id-pstack-aaaaaa" {
		t.Fatalf("tick 3 node calls = %v", got)
	}
	if len(h.state().Workers) != 0 || h.last().Locked != nil {
		t.Errorf("not forgotten: %+v", h.state().Workers)
	}
	if ev := h.events("scaler.removed"); len(ev) != 1 || string(ev[0].Data) != `{"name":"pstack-aaaaaa","reason":"empty"}` {
		t.Errorf("scaler.removed = %+v", ev)
	}
	h.noSecrets()

	t.Run("no node", func(t *testing.T) {
		// negative control: drop the forget after the removal apply → still owned after its apply and this fails
		h := newHarness(t)
		h.own("pstack-aaaaaa")
		h.removing("pstack-aaaaaa", "never-joined")
		h.step()
		if h.applies() != 1 || len(h.state().Workers) != 0 || len(h.nodeCalls()) != 0 {
			t.Fatalf("applies %d, workers %+v, node calls %v", h.applies(), h.state().Workers, h.nodeCalls())
		}
		if ev := h.events("scaler.removed"); len(ev) != 1 || string(ev[0].Data) != `{"name":"pstack-aaaaaa","reason":"never-joined"}` {
			t.Errorf("scaler.removed = %+v", ev)
		}
	})

	t.Run("DELETE 404 is gone", func(t *testing.T) {
		// negative control: treat every DELETE error as a failure → the worker stays and this fails
		h := newHarness(t)
		h.own("pstack-aaaaaa")
		h.node("pstack-aaaaaa", "down", 0, time.Hour)
		h.setNode("pstack-aaaaaa", func(n *sigNode) { n.Availability = "drain" })
		h.removing("pstack-aaaaaa", "lost")
		h.step() // apply without it
		// Someone removes the node between the scaler's read and its DELETE.
		h.with(func(a *fakeAPI) {
			a.afterSignals = func(a *fakeAPI) { a.sig.Nodes = a.sig.Nodes[:1]; a.afterSignals = nil }
		})
		h.step()
		if got := h.nodeCalls(); len(got) != 1 || !strings.HasPrefix(got[0], "DELETE ") || len(h.state().Workers) != 0 || h.last().LastError != "" {
			t.Errorf("node calls %v, workers %+v, lastError %q", got, h.state().Workers, h.last().LastError)
		}
	})
}

func TestRemovalSurvivesRestart(t *testing.T) {
	// negative control: forget only after a DELETE of our own (or never set OutOfState) → a removal stuck after the crash, and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 0, time.Hour)
	h.removing("pstack-aaaaaa", "empty")
	file := filepath.Join(h.stateDir, stateFile)
	// crash runs a tick, then puts back the scaler.json from before it: a kill after the tick's
	// external call, before anything was saved.
	crash := func() {
		t.Helper()
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		h.step()
		if err := os.WriteFile(file, before, 0o600); err != nil {
			t.Fatal(err)
		}
		h.restart()
	}
	never := func(when string) {
		t.Helper()
		if slices.Contains(h.workerNames(), "pstack-aaaaaa") {
			t.Fatalf("%s: pstack-aaaaaa back in the workers map", when)
		}
	}

	crash() // drained
	crash() // already drained: no second drain; applied without it, not recorded
	never("after the apply")
	h.step()
	h.restart()
	never("after the repeated apply")
	h.setNode("pstack-aaaaaa", func(n *sigNode) { n.State = "down" })
	crash() // DELETE succeeded, not recorded
	h.step()

	var drains, deletes int
	for _, c := range h.nodeCalls() {
		drains += strings.Count(c, "/drain")
		deletes += strings.Count(c, "DELETE")
	}
	if drains != 1 || deletes != 1 {
		t.Errorf("node calls = %v, want one drain and one DELETE", h.nodeCalls())
	}
	if len(h.state().Workers) != 0 || len(h.events("scaler.removed")) == 0 {
		t.Errorf("not forgotten: %+v", h.state().Workers)
	}
	h.with(func(a *fakeAPI) {
		if len(a.joinQueries) != 0 {
			t.Errorf("created again: %v", a.joinQueries)
		}
	})

	t.Run("no node", func(t *testing.T) {
		// negative control: forget before the apply succeeds → the server is never destroyed and this fails
		h := newHarness(t)
		h.own("pstack-aaaaaa")
		h.removing("pstack-aaaaaa", "never-joined")
		file := filepath.Join(h.stateDir, stateFile)
		before, _ := os.ReadFile(file)
		h.step()
		os.WriteFile(file, before, 0o600)
		h.restart()
		h.step()
		if h.applies() != 2 || len(h.state().Workers) != 0 || len(h.nodeCalls()) != 0 {
			t.Errorf("applies %d, workers %+v, node calls %v", h.applies(), h.state().Workers, h.nodeCalls())
		}
		if slices.Contains(h.workerNames(), "pstack-aaaaaa") {
			t.Error("pstack-aaaaaa back in the workers map")
		}
	})
}

func TestScaleDown(t *testing.T) {
	// negative control: skip the re-read → the moved and the busy rows mark removing and this fails
	grace := 15 * time.Minute
	setup := func(t *testing.T) *harness {
		h := newHarness(t)
		h.own("pstack-aaaaaa", "pstack-bbbbbb")
		h.with(func(a *fakeAPI) { a.cfg.Min = 1 })
		h.node("pstack-aaaaaa", "ready", 0, grace+time.Minute)
		h.node("pstack-bbbbbb", "ready", 2, 0)
		return h
	}

	h := setup(t)
	reads := 0
	h.with(func(a *fakeAPI) { reads = a.sigReads })
	h.step()
	w := h.worker("pstack-aaaaaa")
	if !w.Removing || w.Reason != "empty" || h.worker("pstack-bbbbbb").Removing {
		t.Fatalf("a = %+v", w)
	}
	h.with(func(a *fakeAPI) {
		if a.sigReads-reads != 2 {
			t.Errorf("signals read %d times, want 2 (the re-read)", a.sigReads-reads)
		}
	})

	for name, change := range map[string]func(n *sigNode){
		"emptySince moved": func(n *sigNode) { at := *n.EmptySince + 1; n.EmptySince = &at },
		"a task appeared":  func(n *sigNode) { n.Tasks, n.EmptySince = 1, nil },
	} {
		t.Run(name, func(t *testing.T) {
			// negative control: skip the re-read before marking removing → this row marks it and fails
			h := setup(t)
			h.with(func(a *fakeAPI) {
				a.afterSignals = func(a *fakeAPI) { change(&a.sig.Nodes[1]); a.afterSignals = nil }
			})
			h.step()
			if h.worker("pstack-aaaaaa").Removing {
				t.Error("marked removing after the re-read changed")
			}
		})
	}

	t.Run("at min, or inside grace", func(t *testing.T) {
		// negative control: drop the min (or grace) check → a is removed and this fails
		h := newHarness(t)
		h.own("pstack-aaaaaa", "pstack-bbbbbb")
		h.node("pstack-aaaaaa", "ready", 0, time.Hour)
		h.node("pstack-bbbbbb", "ready", 0, 5*time.Minute)
		h.step()
		h.step()
		if got := h.owned(); len(got) != 1 || got[0] != "pstack-bbbbbb" {
			t.Fatalf("owned = %v, want only b (inside grace)", got)
		}
		h.with(func(a *fakeAPI) { a.cfg.Min = 1 })
		h.node("pstack-bbbbbb", "ready", 0, time.Hour)
		h.step()
		if got := h.owned(); len(got) != 1 {
			t.Errorf("owned = %v, want b kept at min", got)
		}
	})
}

func TestMaxBelowOwnedKeepsBusy(t *testing.T) {
	// negative control: remove down to max regardless of tasks → a busy worker becomes removing and this fails
	h := newHarness(t)
	names := []string{"pstack-aaaaaa", "pstack-bbbbbb", "pstack-cccccc"}
	h.own(names...)
	for _, n := range names {
		h.node(n, "ready", 2, 0)
	}
	h.with(func(a *fakeAPI) { a.cfg.Max, a.cfg.Min = 1, 1 })
	for range 3 {
		h.step()
	}
	if got := h.owned(); len(got) != 3 || len(h.nodeCalls()) != 0 || h.applies() != 0 {
		t.Fatalf("owned %v, node calls %v, applies %d", got, h.nodeCalls(), h.applies())
	}
	for i, n := range names {
		h.node(n, "ready", 0, time.Hour)
		h.step()
		want := 2 - i
		if i == 2 {
			want = 1 // min
		}
		if got := h.owned(); len(got) != want {
			t.Fatalf("after %s went empty: owned %v, want %d", n, got, want)
		}
	}
}

func TestStaleSettingsReplaced(t *testing.T) {
	// negative control: keep min for stale workers too → the old worker stays and no new one is made
	h := newHarness(t)
	h.own("pstack-aaaaaa") // before min 1: own's first tick would fill it
	h.with(func(a *fakeAPI) { a.cfg.Min, a.cfg.CooldownMin = 1, 0 })
	h.node("pstack-aaaaaa", "ready", 0, time.Hour)
	h.step()
	if h.worker("pstack-aaaaaa").Removing {
		t.Fatal("removed at min with the current settings")
	}
	h.with(func(a *fakeAPI) { a.cfg.Vars["server_type"] = "cx32" })
	h.step()
	if w := h.worker("pstack-aaaaaa"); !w.Removing || w.Reason != "replaced" {
		t.Fatalf("worker = %+v", w)
	}
	for range 4 {
		h.step()
	}
	got := h.owned()
	if len(got) != 1 || got[0] == "pstack-aaaaaa" || h.worker(got[0]).Vars["server_type"] != "cx32" {
		t.Fatalf("owned = %v", got)
	}
}

func TestMaxZero(t *testing.T) {
	// negative control: wait for grace on max 0 → nothing is marked and this fails
	h := newHarness(t)
	names := []string{"pstack-aaaaaa", "pstack-bbbbbb", "pstack-cccccc"}
	h.own(names...)
	h.node("pstack-aaaaaa", "ready", 0, 0)
	h.node("pstack-bbbbbb", "ready", 3, 0)
	h.node("pstack-cccccc", "ready", 0, 0)
	h.with(func(a *fakeAPI) { a.cfg.Max = 0 })
	for i := range names {
		h.step()
		if got := h.owned(); len(got) != 2-i {
			t.Fatalf("tick %d: owned %v", i+1, got)
		}
	}
	for _, n := range names {
		if w := h.worker(n); w.Reason != "max" {
			t.Errorf("%s: %+v", n, w)
		}
	}
	h.with(func(a *fakeAPI) {
		if len(a.joinQueries) != 0 {
			t.Errorf("created under max 0: %v", a.joinQueries)
		}
	})

	t.Run("a failed create is not retried", func(t *testing.T) {
		// negative control: drop observe's never-applied-above-max mark → only scaleDown marks it, it is still owned after the tick and this fails
		h := newHarness(t)
		os.WriteFile(filepath.Join(h.tofuDir, "fail"), []byte("Error: quota\n"), 0o600)
		h.stuck("pr-42_api", fits)
		h.tick()
		name := h.owned()[0]
		os.Remove(filepath.Join(h.tofuDir, "fail"))
		h.with(func(a *fakeAPI) { a.cfg.Max = 0 })
		h.advance(time.Hour)
		h.tick()
		ev := h.events("scaler.removed")
		if len(h.state().Workers) != 0 || len(h.events("scaler.created")) != 0 || len(ev) != 1 || string(ev[0].Data) != `{"name":"`+name+`","reason":"max"}` {
			t.Errorf("workers %+v, removed %+v, created %d", h.state().Workers, ev, len(h.events("scaler.created")))
		}
	})
}

func TestPendingAboveMax(t *testing.T) {
	// negative control: drop observe's never-applied-above-max mark → aaaaaa's removal apply carries cccccc's create and this fails
	setup := func(t *testing.T, max int) *harness {
		h := newHarness(t)
		h.own("pstack-aaaaaa", "pstack-bbbbbb")
		h.node("pstack-bbbbbb", "ready", 2, 0)
		h.edit(func(s *State) {
			s.Workers[0].Removing, s.Workers[0].Reason = true, "never-joined"
			// A create that failed a minute ago, in backoff.
			s.Workers = append(s.Workers, OwnedWorker{Name: "pstack-cccccc", Vars: map[string]string{"server_type": "cx22", "location": "fsn1"}, CreatedAt: h.ms()})
			s.Failures, s.NextTryAt = 1, h.ms()+time.Minute.Milliseconds()
		})
		h.with(func(a *fakeAPI) { a.cfg.Max, a.cfg.Vars["server_type"] = max, "cx32" })
		h.advance(time.Minute)
		h.tick()
		return h
	}
	h := setup(t, 1)
	if len(h.events("scaler.created")) != 0 || slices.Contains(h.workerNames(), "pstack-cccccc") {
		t.Fatalf("bought above max: created %+v, map %v", h.events("scaler.created"), h.workerNames())
	}
	if got := h.owned(); !slices.Equal(got, []string{"pstack-bbbbbb"}) || len(h.state().Workers) != 1 {
		t.Errorf("owned %v, workers %+v", got, h.state().Workers)
	}
	if ev := h.events("scaler.removed"); !slices.ContainsFunc(ev, func(e Event) bool { return string(e.Data) == `{"name":"pstack-cccccc","reason":"max"}` }) {
		t.Errorf("scaler.removed = %+v", ev)
	}

	t.Run("within max", func(t *testing.T) {
		// negative control: mark every never-applied worker removing → cccccc is never created and this fails
		// negative control: refresh a pending worker's vars in scaleUp only → aaaaaa's removal apply creates it with cx22
		h := setup(t, 3)
		if ev := h.events("scaler.created"); len(ev) != 1 || !strings.Contains(string(ev[0].Data), "pstack-cccccc") || !strings.Contains(string(ev[0].Data), `"server_type":"cx32"`) {
			t.Errorf("scaler.created = %+v", ev)
		}
	})
}

func TestRemovalPastAFailingApply(t *testing.T) {
	// negative control: set OutOfState only after a successful apply → aaaaaa stays removing, its node is never DELETEd and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 0, time.Hour)
	h.removing("pstack-aaaaaa", "empty")
	os.WriteFile(filepath.Join(h.tofuDir, "fail"), []byte("Error: creating server pstack-bbbbbb: quota\n"), 0o600)
	h.step() // drain
	h.step() // the apply fails
	// It destroyed aaaaaa before failing on another worker.
	os.WriteFile(filepath.Join(h.tofuDir, "state.json"), []byte(`[]`), 0o600)
	h.advance(time.Minute) // past the backoff
	h.tick()
	if w := h.worker("pstack-aaaaaa"); !w.OutOfState || h.last().LastAction != "apply failed" {
		t.Fatalf("worker %+v, action %q", w, h.last().LastAction)
	}
	h.setNode("pstack-aaaaaa", func(n *sigNode) { n.State = "down" })
	h.step()
	if got := h.nodeCalls(); len(got) != 2 || got[1] != "DELETE /api/swarm/nodes/id-pstack-aaaaaa" || len(h.state().Workers) != 0 {
		t.Errorf("node calls %v, workers %+v", got, h.state().Workers)
	}
}

func TestOwnershipOnly(t *testing.T) {
	// negative control: act on any worker node that is empty → the hand-added worker is drained and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 0, time.Hour)
	h.node("worker-1", "ready", 0, time.Hour)
	h.with(func(a *fakeAPI) {
		at := h.now.Add(-time.Hour).UnixMilli()
		a.sig.Nodes[0].EmptySince = &at // the manager
		a.cfg.Max = 0
	})
	for range 3 {
		h.step()
	}
	h.setNode("pstack-aaaaaa", func(n *sigNode) { n.State = "down" })
	for range 3 {
		h.step()
	}
	for _, c := range h.nodeCalls() {
		if !strings.Contains(c, "/id-pstack-aaaaaa") {
			t.Errorf("touched a node it does not own: %s", c)
		}
	}
	if len(h.state().Workers) != 0 || len(h.nodeCalls()) != 2 {
		t.Errorf("own worker not removed: %+v, calls %v", h.state().Workers, h.nodeCalls())
	}
}

func TestOrphanCheck(t *testing.T) {
	// negative control: skip the first-tick check → the cut-off create is retried under its name and this fails
	cutOff := func(h *harness, name string) {
		h.edit(func(s *State) {
			s.Workers = append(s.Workers, OwnedWorker{Name: name, CreatedAt: h.ms(), ApplyingAt: h.ms()})
		})
	}
	h := newHarness(t)
	h.own("pstack-bbbbbb") // in state
	h.node("pstack-bbbbbb", "ready", 2, 0)
	cutOff(h, "pstack-aaaaaa")
	h.edit(func(s *State) {
		s.Workers = append(s.Workers,
			OwnedWorker{Name: "pstack-cccccc", Removing: true, Reason: "empty", AppliedAt: h.ms()}, // removing: not an orphan
			OwnedWorker{Name: "pstack-dddddd", CreatedAt: h.ms()},                                  // a failed create: a retry
		)
	})
	h.step()
	s := h.state()
	if !slices.Equal(s.Orphans, []string{"pstack-aaaaaa"}) || !slices.Equal(h.last().Orphans, s.Orphans) {
		t.Fatalf("orphans = %v", s.Orphans)
	}
	if ev := h.events("scaler.failed"); len(ev) != 1 || string(ev[0].Data) != `{"action":"orphan","error":"pstack-aaaaaa not in Tofu state","attempt":1}` {
		t.Errorf("scaler.failed = %+v", ev)
	}
	for _, w := range s.Workers {
		if w.Name == "pstack-aaaaaa" {
			t.Errorf("orphan still owned after its removal apply: %+v", w)
		}
		if w.Name == "pstack-dddddd" && (w.Removing || w.ApplyingAt != 0) {
			t.Errorf("failed create = %+v", w)
		}
	}
	if ev := h.events("scaler.removed"); !slices.ContainsFunc(ev, func(e Event) bool { return string(e.Data) == `{"name":"pstack-aaaaaa","reason":"orphan"}` }) {
		t.Errorf("scaler.removed = %+v", ev)
	}

	// Kept on the card until dismissed (W-R18).
	h.step()
	if got := h.last().Orphans; len(got) != 1 {
		t.Errorf("orphans = %v", got)
	}
	h.with(func(a *fakeAPI) { a.cfg.Dismissed = []string{"pstack-aaaaaa"} })
	h.step()
	if got := h.last().Orphans; len(got) != 0 {
		t.Errorf("dismissed orphan still listed: %v", got)
	}

	t.Run("no state file", func(t *testing.T) {
		// negative control: treat `No state file was found` as an error → the tick fails, no orphan
		h := newHarness(t)
		h.tick()
		cutOff(h, "pstack-aaaaaa")
		h.step()
		if got := h.state().Orphans; !slices.Equal(got, []string{"pstack-aaaaaa"}) || strings.Contains(h.last().LastError, "state") {
			t.Errorf("orphans %v, lastError %q", got, h.last().LastError)
		}
	})

	t.Run("cut off but in state", func(t *testing.T) {
		// negative control: call every cut-off apply an orphan → this one is removed and this fails
		h := newHarness(t)
		h.tick()
		cutOff(h, "pstack-aaaaaa")
		os.WriteFile(filepath.Join(h.tofuDir, "state.json"), []byte(`["pstack-aaaaaa"]`), 0o600)
		h.step()
		w := h.worker("pstack-aaaaaa")
		if w.Removing || len(h.state().Orphans) != 0 || w.AppliedAt != h.ms() {
			t.Errorf("worker = %+v, orphans %v", w, h.state().Orphans)
		}
	})
}

func TestPresetSwitch(t *testing.T) {
	// negative control: drop the LastPreset write in create() → no destroy in tofu/hetzner and this fails
	toAWS := func(t *testing.T, failDestroy bool) *harness {
		h := newHarness(t)
		if failDestroy {
			os.WriteFile(filepath.Join(h.tofuDir, "fail-destroy"), []byte("Error: firewall in use\n"), 0o600)
		}
		h.stuck("pr-42_api", fits)
		h.tick() // a worker under hetzner
		h.with(func(a *fakeAPI) { a.sig.Stuck, a.cfg.Max = nil, 0 })
		h.step() // removing (max)
		h.step() // applied without it, forgotten
		if s := h.state(); len(s.Workers) != 0 || s.LastPreset != "hetzner" {
			t.Fatalf("workers %+v, lastPreset %q", s.Workers, s.LastPreset)
		}
		h.with(func(a *fakeAPI) {
			a.cfg.Preset, a.cfg.Files, a.cfg.Max = "aws", Builtins()["aws"], 3
			a.cfg.Vars = map[string]string{"instance_type": "t3.small", "region": "eu-central-1", "subnet_id": "subnet-1", "key_name": "k"}
		})
		h.step() // inits aws: a Tofu action, so no destroy yet
		if h.state().PendingDestroy != "hetzner" || h.calls("destroy") != 0 {
			t.Fatalf("pendingDestroy %q, destroys %d", h.state().PendingDestroy, h.calls("destroy"))
		}
		h.step()
		return h
	}
	hetznerDir := func(h *harness) string { return filepath.Join(h.stateDir, "tofu", "hetzner") }

	h := toAWS(t, false)
	b, _ := os.ReadFile(filepath.Join(h.tofuDir, "dirs.log"))
	if !strings.Contains(string(b), hetznerDir(h)+" destroy -auto-approve") {
		t.Errorf("no destroy in tofu/hetzner:\n%s", b)
	}
	if s := h.state(); s.PendingDestroy != "" || h.last().PendingDestroy != "" {
		t.Errorf("pendingDestroy = %q", s.PendingDestroy)
	}
	if _, err := os.Stat(hetznerDir(h)); !os.IsNotExist(err) {
		t.Errorf("tofu/hetzner kept after its destroy: %v", err)
	}

	t.Run("destroy fails", func(t *testing.T) {
		// negative control: give the destroy the shared NextTryAt again → aws waits out its backoff and this fails
		h := toAWS(t, true)
		ev := h.events("scaler.failed")
		if len(ev) != 1 || !strings.HasPrefix(string(ev[0].Data), `{"action":"destroy","error":"Error: firewall in use`) {
			t.Errorf("scaler.failed = %+v", ev)
		}
		if _, err := os.Stat(hetznerDir(h)); err != nil || h.last().PendingDestroy != "hetzner" {
			t.Errorf("dir %v, pendingDestroy %q", err, h.last().PendingDestroy)
		}
		h.stuck("pr-42_api", fits)
		h.with(func(a *fakeAPI) { a.cfg.CooldownMin = 0 })
		h.step() // inside the destroy's 1 min backoff
		if _, err := os.Stat(filepath.Join(h.stateDir, "tofu", "aws", "workers.auto.tfvars.json")); err != nil || len(h.owned()) != 1 {
			t.Fatalf("aws did not proceed: %v, owned %v, action %q", err, h.owned(), h.last().LastAction)
		}
		// negative control: clear LastError on every successful apply → the destroy's error vanishes.
		// negative control: report only NextTryAt → nextRetryAt is 0.
		if r, s := h.last(), h.state(); !strings.Contains(r.LastError, "firewall in use") || r.NextRetryAt != s.DestroyNextTryAt {
			t.Errorf("after aws's apply: lastError %q, nextRetryAt %d, want the destroy's %d", r.LastError, r.NextRetryAt, s.DestroyNextTryAt)
		}
		h.step() // the destroy's backoff is over; the aws worker boots, so no other Tofu runs
		if s := h.state(); h.calls("destroy") != 2 || s.DestroyFailures != 2 || s.DestroyNextTryAt != h.ms()+2*time.Minute.Milliseconds() || s.Failures != 0 {
			t.Errorf("destroys %d, state %+v", h.calls("destroy"), s)
		}
	})
}

func TestPresetSwitchedBack(t *testing.T) {
	// negative control: keep PendingDestroy when its preset is selected again → the active preset's directory is destroyed
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.tofuDir, "fail-destroy"), []byte("Error: firewall in use\n"), 0o600)
	h.stuck("pr-42_api", fits)
	h.tick()
	h.with(func(a *fakeAPI) { a.sig.Stuck, a.cfg.Max = nil, 0 })
	h.step()
	h.step()
	hetzner := map[string]string{"server_type": "cx22", "location": "fsn1", "ssh_keys": `["k1"]`}
	h.with(func(a *fakeAPI) {
		a.cfg.Preset, a.cfg.Files = "aws", Builtins()["aws"]
		a.cfg.Vars = map[string]string{"instance_type": "t3.small", "region": "eu-central-1", "subnet_id": "subnet-1", "key_name": "k"}
	})
	h.step()
	h.step() // the destroy fails
	h.with(func(a *fakeAPI) { a.cfg.Preset, a.cfg.Files, a.cfg.Vars = "hetzner", Builtins()["hetzner"], hetzner })
	os.Remove(filepath.Join(h.tofuDir, "fail-destroy"))
	destroys := h.calls("destroy")
	h.advance(time.Hour)
	h.tick()
	h.step()
	// negative control: keep the old destroy's backoff on switch-back → DestroyFailures stays 1.
	if s := h.state(); s.DestroyFailures != 0 || s.DestroyNextTryAt != 0 {
		t.Errorf("switch-back kept the destroy backoff: %d failures, next %d", s.DestroyFailures, s.DestroyNextTryAt)
	}
	if s := h.state(); s.PendingDestroy != "" || s.LastPreset != "hetzner" || h.calls("destroy") != destroys {
		t.Errorf("pendingDestroy %q, lastPreset %q, destroys %d (was %d)", s.PendingDestroy, s.LastPreset, h.calls("destroy"), destroys)
	}
	if _, err := os.Stat(filepath.Join(h.stateDir, "tofu", "hetzner")); err != nil {
		t.Errorf("tofu/hetzner: %v", err)
	}
}

func TestLostNotReady(t *testing.T) {
	// negative control: never time out a down node → the worker is never removing and this fails
	h := newHarness(t)
	h.own("pstack-aaaaaa")
	h.node("pstack-aaaaaa", "ready", 2, 0)
	h.step()
	h.setNode("pstack-aaaaaa", func(n *sigNode) { n.State = "down" })
	h.step()
	h.advance(LostAfter - Tick)
	h.tick()
	if h.worker("pstack-aaaaaa").Removing {
		t.Fatal("lost before LostAfter")
	}
	h.step()
	if w := h.worker("pstack-aaaaaa"); !w.Removing || w.Reason != "lost" {
		t.Errorf("worker = %+v", w)
	}
}

func TestPresetSwitchedTwice(t *testing.T) {
	// negative control: drop the create() guard on LastPreset → gcp's create overwrites aws, which is never destroyed, and this fails
	h := newHarness(t)
	failDestroy := filepath.Join(h.tofuDir, "fail-destroy")
	os.WriteFile(failDestroy, []byte("Error: firewall in use\n"), 0o600)
	until := func(what string, ok func(s *State) bool) {
		t.Helper()
		for range 10 {
			if ok(h.state()) {
				return
			}
			h.advance(time.Hour)
			h.tick()
		}
		t.Fatalf("%s: state %+v, last %+v", what, h.state(), h.last())
	}
	drain := func() {
		h.with(func(a *fakeAPI) { a.sig.Stuck, a.cfg.Max = nil, 0 })
		until("workers gone", func(s *State) bool { return len(s.Workers) == 0 })
	}
	h.stuck("pr-42_api", fits)
	h.tick() // a worker under hetzner
	drain()
	h.with(func(a *fakeAPI) {
		a.cfg.Preset, a.cfg.Files, a.cfg.Max = "aws", Builtins()["aws"], 3
		a.cfg.Vars = map[string]string{"instance_type": "t3.small", "region": "eu-central-1", "subnet_id": "subnet-1", "key_name": "k"}
	})
	h.stuck("pr-42_api", fits)
	until("a worker under aws", func(s *State) bool { return len(s.Workers) == 1 && s.Workers[0].AppliedAt > 0 })
	drain()
	if s := h.state(); s.PendingDestroy != "hetzner" || s.LastPreset != "aws" {
		t.Fatalf("pendingDestroy %q, lastPreset %q", s.PendingDestroy, s.LastPreset)
	}

	h.with(func(a *fakeAPI) {
		a.cfg.Preset, a.cfg.Files, a.cfg.Max = "gcp", Builtins()["gcp"], 3
		a.cfg.Vars = map[string]string{"project": "p", "zone": "europe-west3-a", "machine_type": "e2-small"}
	})
	h.stuck("pr-42_api", fits)
	h.advance(time.Hour)
	h.tick() // inits gcp: no destroy this tick
	if a := h.last().LastAction; a != "hetzner destroy pending" {
		t.Errorf("action %q", a)
	}
	h.advance(time.Hour)
	h.tick() // the destroy fails again
	if s := h.state(); len(s.Workers) != 0 || s.LastPreset != "aws" {
		t.Fatalf("created behind a pending destroy: workers %+v, lastPreset %q", s.Workers, s.LastPreset)
	}

	os.Remove(failDestroy)
	until("hetzner destroyed", func(s *State) bool { return s.PendingDestroy != "hetzner" })
	if h.last().PendingDestroy != "aws" {
		t.Errorf("aws not queued in the destroying report: %q", h.last().PendingDestroy)
	}
	until("a worker under gcp", func(s *State) bool { return len(s.Workers) == 1 && s.LastPreset == "gcp" })
	until("aws destroyed", func(s *State) bool { return s.PendingDestroy == "" })
	b, _ := os.ReadFile(filepath.Join(h.tofuDir, "dirs.log"))
	for _, p := range []string{"hetzner", "aws"} {
		if !strings.Contains(string(b), filepath.Join(h.stateDir, "tofu", p)+" destroy -auto-approve") {
			t.Errorf("no destroy in tofu/%s:\n%s", p, b)
		}
	}

	t.Run("switched back past a later preset", func(t *testing.T) {
		// negative control: clear PendingDestroy without taking LastPreset → aws is forgotten and this fails
		h := newHarness(t)
		h.edit(func(s *State) { s.PendingDestroy, s.LastPreset = "hetzner", "aws" })
		h.tick()
		if s := h.state(); s.PendingDestroy != "aws" || s.LastPreset != "hetzner" {
			t.Errorf("pendingDestroy %q, lastPreset %q", s.PendingDestroy, s.LastPreset)
		}
	})
}
