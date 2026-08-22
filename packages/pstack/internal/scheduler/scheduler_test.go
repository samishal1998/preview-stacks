package scheduler

import (
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

func TestSleepIndexExactHostsAndGoRulesCaseInsensitively(t *testing.T) {
	// negative control: drop the "(?i)" prefix in Rebuild — APP-A.example.com stops matching.
	ix := NewSleepIndex()
	ix.Rebuild([]SleepEntry{
		{ID: "a", Hosts: []string{"app-a.example.com"}, Rules: []string{`^[a-z0-9-]+\.app-a\.example\.com$`}},
		{ID: "b"},
	})
	if ix.Size() != 2 {
		t.Errorf("size = %d", ix.Size())
	}
	if ix.Find("APP-A.example.com") != "a" || ix.Find("x.app-a.example.com") != "a" || ix.Find("app-b.example.com") != "" {
		t.Error("lookups")
	}
	// A rule RE2 rejects matches nothing and does not break the index.
	ix.Rebuild([]SleepEntry{{ID: "c", Hosts: []string{"c.example.com"}, Rules: []string{`(?=lookahead)`}}})
	if ix.Find("c.example.com") != "c" || ix.Size() != 1 {
		t.Error("a bad rule must be swallowed, the exact host kept")
	}
	exact, rules := SplitHosts([]string{"h.example.com", "(pattern) ^x$"})
	if strings.Join(exact, ",") != "h.example.com" || strings.Join(rules, ",") != "^x$" {
		t.Error("splitHosts")
	}
	if FormatDuration(5_400_000) != "1h30m" || FormatDuration(0) != "0s" || FormatDuration(86_400_000*3) != "3d" {
		t.Error("formatDuration")
	}
	if !strings.Contains(WakePage("h", "s<", Failed, "pull denied"), "s&lt;") {
		t.Error("wake page escaping")
	}
}

func TestTrafficMeterMovedResetUnreachable(t *testing.T) {
	// negative control: use `>=` instead of `>` in Sample — the reset at 3000 counts as a visit.
	text := `traefik_router_requests_total{code="200",method="GET",protocol="http",router="app-wk@docker",service="app-wk@docker"} 5` + "\n"
	ok := true
	m := NewTrafficMeter("http://x/metrics", func(string) (string, bool) { return text, ok }, nil)
	m.Sample(1000)
	if !m.OK() || m.LastActivity([]string{"app-wk"}) != 0 {
		t.Error("first sample is a baseline")
	}
	text = strings.Replace(text, " 5", " 7", 1)
	m.Sample(2000)
	if m.LastActivity([]string{"app-wk"}) != 2000 {
		t.Error("an increase is activity")
	}
	text = strings.Replace(text, " 7", " 1", 1) // Traefik restarted
	m.Sample(3000)
	if m.LastActivity([]string{"app-wk"}) != 2000 {
		t.Error("a reset is a new baseline, not a visit")
	}
	ok = false
	m.Sample(4000)
	if m.OK() {
		t.Error("unreachable is not silence")
	}
	if NewTrafficMeter("", nil, nil).OK() {
		t.Error("no url: never ok")
	}
}

func TestTickDecidesAfterFromNewestContainerIdleFromMeterNeverTwice(t *testing.T) {
	// negative control: skip the `meta.Asleep` check — the second tick after sleeping sleeps again.
	var slept []string
	now := int64(1_000_000_000)
	metrics := `traefik_router_requests_total{router="app-wk@docker"} 1` + "\n"
	metricsOK := true
	meter := NewTrafficMeter("http://x", func(string) (string, bool) { return metrics, metricsOK }, nil)
	st, err := spec.Parse("version: 1\nstack: wk\ncompose: {file: c.yml, profiles: []}\nsleep: {idle: 1h, after: 2d}\naxes: []\n", map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	asleep := false
	startedAt := now - 3_600_000 // deployed an hour ago
	s := New(Deps{
		List:    func() ([]Candidate, error) { return []Candidate{{ID: "wk", Kind: spec.Isolated, Asleep: asleep}}, nil },
		Resolve: func(string) (*spec.Stack, error) { return st, nil },
		Runtime: func(*spec.Stack) (RuntimeView, error) {
			sa := startedAt
			return RuntimeView{Reachable: true, StartedAt: []*int64{&sa}, Routers: []string{"app-wk"}}, nil
		},
		IsBusy: func(string) bool { return false },
		Sleep:  func(id string, _ *spec.Stack, reason string) { slept = append(slept, id+":"+reason); asleep = true },
		Meter:  meter,
		Now:    func() int64 { return now },
	})
	s.Tick() // baseline; idle clock starts at boot
	if len(slept) != 0 {
		t.Fatal("baseline")
	}
	now += 30 * 60_000
	s.Tick()
	if len(slept) != 0 {
		t.Fatal("30m idle < 1h")
	}
	now += 31 * 60_000
	metrics = strings.Replace(metrics, " 1", " 2", 1) // a visit just now
	s.Tick()
	if len(slept) != 0 {
		t.Fatal("activity reset the clock")
	}
	now += 61 * 60_000
	s.Tick()
	if strings.Join(slept, ",") != "wk:idle 1h" {
		t.Fatalf("got %v", slept)
	}
	s.Tick()
	if strings.Join(slept, ",") != "wk:idle 1h" {
		t.Fatal("asleep: not again")
	}

	// `after`: with the meter down, idle cannot decide, but a 2-day-old deploy can.
	slept = nil
	asleep = false
	metricsOK = false
	s.Tick()
	if len(slept) != 0 {
		t.Fatal("meter down: idle must not decide")
	}
	startedAt = now - 2*86_400_000
	s.Tick()
	if strings.Join(slept, ",") != "wk:after 2d" {
		t.Fatalf("got %v", slept)
	}
}
