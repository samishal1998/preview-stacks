// Package scheduler is sleep and wake-on-call: the SleepIndex (hostname → the deployment the
// catch-all router's request belongs to), the TrafficMeter (Traefik's per-router counters → "last
// request"), the tick that applies a spec's `sleep:` policy, and the "spinning up" page.
//
// The index holds two kinds of deployment, and the second is not this package's to decide: one that
// is ASLEEP, and one that has been WOKEN but is not serving yet. The api package keeps the latter
// indexed until its readiness watch settles, because the sleep record is cleared the moment `up`
// returns and the hostname would otherwise fall through to the control plane's own UI. Everything
// here is the same either way — a hostname lookup does not care why it is listed.
//
// SLEEP is the middle between tearing a preview down on a timer (losing the state that made it
// useful) and leaving it up for days: the compose project goes down, volumes and axes stay, and the
// NEXT REQUEST to its hostname brings it back. Everything here is IN MEMORY (invariant 10): a
// restart forgets when a stack was last visited, so the idle clock restarts from boot — the cost is
// one more idle period awake, never a sleep that comes early.
//
// The scheduler will not: sleep a `kind: shared` deployment, one with a job in flight, one already
// asleep or unresolvable; sleep on `idle` when the meter cannot read Traefik (silence from an
// unreachable endpoint is not "no traffic"); or wake anything from a page view on the control plane.
package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// FormatDuration is `2h` for 7_200_000 ms — the string a human reads in the event and the job
// transcript. Hand-rolled: Go's Duration.String() would write `2h0m0s`.
func FormatDuration(ms int64) string {
	units := []struct {
		size  int64
		label string
	}{{86_400_000, "d"}, {3_600_000, "h"}, {60_000, "m"}, {1_000, "s"}}
	var b strings.Builder
	rest := ms
	for _, u := range units {
		if n := rest / u.size; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.label)
			rest -= n * u.size
		}
	}
	if b.Len() == 0 {
		return "0s"
	}
	return b.String()
}

// ── hostname → sleeping deployment ───────────────────────────────────────────────────────────────

// SleepEntry is what the index needs from a sleeping deployment's record.
type SleepEntry struct {
	ID    string
	Hosts []string
	Rules []string
}

type snapshot struct {
	hosts map[string]string
	rules []rule
}

type rule struct {
	re *regexp.Regexp
	id string
}

// SleepIndex answers "is this hostname a sleeping stack's" for every request. Rebuilt from the
// registry whenever a sleep record changes and on a slow timer; a lookup costs one map read plus a
// regexp per wildcard rule, never a disk read.
//
// Owner: whoever calls Rebuild (the server). Readers swap in an immutable snapshot atomically, so a
// request never sees a half-built index and there is no lock on the request path.
type SleepIndex struct {
	snap atomic.Pointer[snapshot]
}

// NewSleepIndex returns an empty index.
func NewSleepIndex() *SleepIndex {
	s := &SleepIndex{}
	s.snap.Store(&snapshot{hosts: map[string]string{}})
	return s
}

// Rebuild replaces the index from the sleeping deployments.
func (s *SleepIndex) Rebuild(entries []SleepEntry) {
	next := &snapshot{hosts: map[string]string{}}
	for _, d := range entries {
		for _, h := range d.Hosts {
			next.hosts[strings.ToLower(h)] = d.ID
		}
		for _, r := range d.Rules {
			// Go and JS agree on the subset a hostname rule uses. A pattern RE2 rejects matches
			// nothing — the exact hosts still do — exactly the swallow the TS try/catch performed.
			re, err := regexp.Compile("(?i)" + r)
			if err != nil {
				continue
			}
			next.rules = append(next.rules, rule{re: re, id: d.ID})
		}
	}
	s.snap.Store(next)
}

// Find returns the deployment id a hostname belongs to, or "".
func (s *SleepIndex) Find(host string) string {
	h := strings.ToLower(host)
	sn := s.snap.Load()
	if id, ok := sn.hosts[h]; ok {
		return id
	}
	for _, r := range sn.rules {
		if r.re.MatchString(h) {
			return r.id
		}
	}
	return ""
}

// Size is how many hosts and rules are indexed — zero means the request path skips the lookup.
func (s *SleepIndex) Size() int {
	sn := s.snap.Load()
	return len(sn.hosts) + len(sn.rules)
}

// SplitHosts separates what inspect's HostsFromRule returns into exact hostnames and HostRegexp
// patterns (prefixed `(pattern) `).
func SplitHosts(hosts []string) (exact []string, rules []string) {
	exact, rules = []string{}, []string{}
	for _, h := range hosts {
		if strings.HasPrefix(h, "(pattern) ") {
			rules = append(rules, strings.TrimPrefix(h, "(pattern) "))
		} else {
			exact = append(exact, h)
		}
	}
	return exact, rules
}

// ── traffic ──────────────────────────────────────────────────────────────────────────────────────

// Fetcher returns the metrics text, or ok=false when Traefik did not answer.
type Fetcher func(url string) (text string, ok bool)

// TrafficMeter reads Traefik's request counters and remembers when each router last moved.
//
// Owner: the scheduler goroutine calls Sample; the tick reads LastActivity and OK on the same
// goroutine. The mutex exists because a test drives Sample and asserts from another goroutine.
type TrafficMeter struct {
	mu     sync.Mutex
	url    string
	fetch  Fetcher
	totals map[string]float64
	moved  map[string]int64
	warned bool
	ok     bool
	Log    func(string)
}

var metricLine = regexp.MustCompile(`router="([^"@]+)(?:@[^"]*)?"[^}]*}\s+([0-9.e+]+)`)

// NewTrafficMeter makes a meter over a metrics URL ("" means idle can never trigger).
func NewTrafficMeter(url string, fetch Fetcher, log func(string)) *TrafficMeter {
	if fetch == nil {
		client := &http.Client{Timeout: 5 * time.Second}
		fetch = func(u string) (string, bool) {
			r, err := client.Get(u)
			if err != nil {
				return "", false
			}
			defer r.Body.Close()
			if r.StatusCode < 200 || r.StatusCode > 299 {
				return "", false
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return "", false
			}
			return string(b), true
		}
	}
	if log == nil {
		log = func(string) {}
	}
	return &TrafficMeter{url: url, fetch: fetch, totals: map[string]float64{}, moved: map[string]int64{}, Log: log}
}

// OK reports whether the most recent read succeeded — `idle` is only decidable when it did.
func (m *TrafficMeter) OK() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ok
}

// Sample reads the counters once. Safe on every tick; never panics.
func (m *TrafficMeter) Sample(now int64) {
	if m.url == "" {
		m.mu.Lock()
		m.ok = false
		m.mu.Unlock()
		return
	}
	text, ok := m.fetch(m.url)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok {
		m.ok = false
		if !m.warned {
			m.warned = true
			m.Log(fmt.Sprintf("scheduler: cannot read Traefik metrics at %s — `sleep.idle` will not trigger until it answers (sleep.after still does)", m.url))
		}
		return
	}
	m.ok = true
	m.warned = false
	next := map[string]float64{}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "traefik_router_requests_total{") {
			continue
		}
		sm := metricLine.FindStringSubmatch(line)
		if sm == nil {
			continue
		}
		next[sm[1]] += js.ParseNumber(sm[2])
	}
	for router, total := range next {
		// Strictly greater: a counter reset (Traefik restarted) reads lower and is a new baseline.
		if prev, seen := m.totals[router]; seen && total > prev {
			m.moved[router] = now
		}
	}
	m.totals = next
}

// LastActivity is when any of these routers last saw a request (epoch ms), or 0 if never since boot.
func (m *TrafficMeter) LastActivity(routers []string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest int64
	for _, r := range routers {
		if t, ok := m.moved[r]; ok && t > latest {
			latest = t
		}
	}
	return latest
}

// ── the loop ─────────────────────────────────────────────────────────────────────────────────────

// Candidate is a deployment as the scheduler sees it.
type Candidate struct {
	ID     string
	Kind   spec.Kind
	Asleep bool
}

// RuntimeView is the slice of a deployment's runtime the tick reads.
type RuntimeView struct {
	Reachable bool
	// StartedAt of every container (nil when docker did not say). Empty means nothing to sleep.
	StartedAt []*int64
	// Routers are the router names, for the meter.
	Routers []string
}

// Deps are what the scheduler calls — injected so a test converges in milliseconds without docker.
type Deps struct {
	List    func() ([]Candidate, error)
	Resolve func(id string) (*spec.Stack, error)
	Runtime func(st *spec.Stack) (RuntimeView, error)
	IsBusy  func(stack string) bool
	// Sleep starts the sleep job. The scheduler never tears anything down itself.
	Sleep  func(id string, st *spec.Stack, reason string)
	Meter  *TrafficMeter
	Log    func(string)
	TickMs int64
	Now    func() int64
}

// Scheduler runs the tick on a timer.
//
// Owner: one goroutine started by Start; Tick may also be called directly by a test. `running`
// stops a slow docker from stacking ticks.
type Scheduler struct {
	deps    Deps
	BootAt  int64
	running atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New makes a scheduler.
func New(deps Deps) *Scheduler {
	if deps.Now == nil {
		deps.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if deps.Log == nil {
		deps.Log = func(string) {}
	}
	if deps.TickMs == 0 {
		deps.TickMs = 60_000
	}
	return &Scheduler{deps: deps, BootAt: deps.Now()}
}

// Start begins ticking. Idempotent.
func (s *Scheduler) Start() {
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(time.Duration(s.deps.TickMs) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Tick()
			}
		}
	}()
}

// Stop ends the loop and waits for an in-flight tick.
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
}

// Tick evaluates every deployment once. Public so a test can drive it without waiting.
func (s *Scheduler) Tick() {
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	defer s.running.Store(false)
	defer func() {
		if p := recover(); p != nil {
			s.deps.Log(fmt.Sprintf("scheduler: tick failed: %v", p))
		}
	}()
	now := s.deps.Now()
	s.deps.Meter.Sample(now)
	list, err := s.deps.List()
	if err != nil {
		s.deps.Log("scheduler: tick failed: " + err.Error())
		return
	}
	for _, meta := range list {
		if meta.Asleep || meta.Kind == spec.Shared {
			continue
		}
		st, err := s.deps.Resolve(meta.ID)
		if err != nil {
			continue // unresolvable — nothing to act on, and the list route already reports it
		}
		if st.Sleep == nil || st.Compose == nil {
			continue
		}
		if s.deps.IsBusy(st.Stack) {
			continue
		}
		rt, err := s.deps.Runtime(st)
		if err != nil || !rt.Reachable || len(rt.StartedAt) == 0 {
			continue // unknown, or nothing to sleep
		}
		var lastDeploy int64
		known := false
		for _, t := range rt.StartedAt {
			if t != nil && (!known || *t > lastDeploy) {
				lastDeploy, known = *t, true
			}
		}
		if st.Sleep.AfterMs > 0 && known && now-lastDeploy >= st.Sleep.AfterMs {
			s.deps.Sleep(meta.ID, st, "after "+FormatDuration(st.Sleep.AfterMs))
			continue
		}
		if st.Sleep.IdleMs > 0 && s.deps.Meter.OK() {
			// The idle clock starts at the later of: boot, the last deploy, the last request.
			floor := s.BootAt
			if known && lastDeploy > floor {
				floor = lastDeploy
			}
			since := floor
			if la := s.deps.Meter.LastActivity(rt.Routers); la > since {
				since = la
			}
			if now-since >= st.Sleep.IdleMs {
				s.deps.Sleep(meta.ID, st, "idle "+FormatDuration(st.Sleep.IdleMs))
			}
		}
	}
}

// ── the page ─────────────────────────────────────────────────────────────────────────────────────

// WakeState is what the page says.
type WakeState string

const (
	Waking WakeState = "waking"
	Busy   WakeState = "busy"
	// Starting is AFTER the wake succeeded and before the stack answers: the containers exist and
	// are converging. Its own state because the sleep record is gone by then, so the visitor cannot
	// truthfully be told a sleeping stack is being brought back — and because the alternative, once
	// the record went, was falling through to the control plane's own UI on a preview's hostname.
	// See the api package's wakeVerdict, which is the only thing that ends this state.
	Starting WakeState = "starting"
	Failed   WakeState = "failed"
)

// WakePage is what a visitor sees while the stack wakes. Self-contained (served on the PREVIEW's
// hostname, where nothing of the control plane exists) and it polls ITSELF: when the response stops
// carrying x-pstack-wake, Traefik routes the hostname to the app and a reload lands on it.
func WakePage(host, stack string, state WakeState, errText string) string {
	title, aria := "Waking your preview", "your preview is waking"
	var detail, failure string
	switch state {
	case Failed:
		title, aria = "Your preview couldn't start", "waking failed"
		if errText == "" {
			errText = "unknown error"
		}
		detail = "Waking <b>" + js.Esc(stack) + "</b> didn't work this time. Reload to try again — and if it keeps happening, the note below is what the person who runs your previews will want to see."
		failure = "\n  <pre class=\"why\"><code>" + js.Esc(errText) + "</code></pre>"
	case Busy:
		aria = "your preview is busy"
		detail = "<b>" + js.Esc(stack) + "</b> is in the middle of another update. Your preview will answer as soon as that wraps up — nothing for you to do."
	case Starting:
		// Deliberately not "was asleep": it is awake. This is the window that used to serve the
		// control plane's own UI here. It is NOT the 502 the same visitor may also see — once
		// Traefik has the container's route, the request goes to the container and never reaches
		// this process at all, so nothing on this page can replace that one.
		aria = "your preview is almost ready"
		detail = "<b>" + js.Esc(stack) + "</b> is awake and getting ready to answer — almost there. You'll be taken in the moment it responds."
	default:
		detail = "<b>" + js.Esc(stack) + "</b> was asleep. Previews doze off when nobody is visiting, and come back the moment someone does — it's happening now. This page will take you in as soon as it answers."
	}
	// The hostname is the page's one large typographic element: the preview's own name bright, the
	// shared domain dimmed, a <wbr> letting an arbitrarily long name break where it means something.
	name, domain, _ := strings.Cut(host, ".")
	hostLine := "<span>" + js.Esc(name) + "</span>"
	if domain != "" {
		hostLine += "<wbr>." + js.Esc(domain)
	}
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>` + js.Esc(title) + `</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="15">
<meta name="robots" content="noindex, nofollow">
<meta name="color-scheme" content="dark">
<meta name="theme-color" content="#10131c">
<link rel="icon" href="data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20viewBox='0%200%2032%2032'%3E%3Crect%20width='32'%20height='32'%20rx='7'%20fill='%230e1116'/%3E%3Crect%20x='7'%20y='7'%20width='18'%20height='4.6'%20rx='2.3'%20fill='%234a9dff'/%3E%3Crect%20x='7'%20y='13.7'%20width='18'%20height='4.6'%20rx='2.3'%20fill='%237c8798'/%3E%3Crect%20x='7'%20y='20.4'%20width='18'%20height='4.6'%20rx='2.3'%20fill='%2349525f'/%3E%3C/svg%3E">
<style>
  :root{--night:#10131c;--bright:#eceef4;--mist:#a5abbc;--dim:#5d6373;--ember:#f2a65a;--ember-core:#ffd9a8;--pane:#181c28}
  *{box-sizing:border-box}
  body{margin:0;min-height:100vh;min-height:100svh;display:grid;place-items:center;font:16px/1.65 system-ui,-apple-system,"Segoe UI",sans-serif;background:var(--night);color:var(--mist);-webkit-font-smoothing:antialiased}
  main{max-width:36rem;padding:min(8vw,3rem) min(6vw,2rem);text-align:center}
  /* THE LAMP — a sleeping machine breathes. ~13 breaths a minute while waking; quicker once the
     preview is awake and about to answer; a still, cold ember when the wake failed. */
  .lamp{height:5.5rem;display:grid;place-items:center;margin-bottom:.5rem}
  .lamp i{width:.85rem;height:.85rem;border-radius:50%;background:var(--ember-core);
    box-shadow:0 0 .6rem .15rem var(--ember),0 0 2.6rem .9rem rgba(242,166,90,.38),0 0 7rem 2.6rem rgba(242,166,90,.14);
    animation:breathe 4.6s ease-in-out infinite}
  .starting .lamp i{animation-duration:1.7s}
  .failed .lamp i{animation:none;background:#8a4a33;box-shadow:0 0 .5rem .1rem rgba(180,86,46,.45),0 0 2rem .6rem rgba(180,86,46,.12);opacity:.8}
  @keyframes breathe{0%,100%{transform:scale(.82);opacity:.62}50%{transform:scale(1.06);opacity:1}}
  @media (prefers-reduced-motion:reduce){.lamp i{animation:none}}
  h1{font-size:clamp(1.35rem,4.5vw,1.7rem);font-weight:650;letter-spacing:-.015em;color:var(--bright);margin:0 0 .35rem}
  .host{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:clamp(.82rem,2.8vw,.95rem);color:var(--dim);word-break:break-all;margin:0 0 1.1rem}
  .host span{color:var(--ember);font-weight:600}
  .what{margin:0 auto;max-width:32rem}
  .what b{color:var(--bright);font-weight:600}
  .why{margin:1.1rem auto 0;max-width:32rem;max-height:9rem;overflow:auto;text-align:left;background:var(--pane);border:1px solid #232838;border-radius:8px;padding:.7rem .9rem}
  .why code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.82rem;line-height:1.55;color:var(--mist);white-space:pre-wrap;word-break:break-word}
  .tick{margin:1.2rem 0 0;font-size:.82rem;color:var(--dim);min-height:1.2em}
  footer{margin-top:2.4rem;font-size:.78rem;letter-spacing:.03em;color:var(--dim)}
</style></head>
<body class="` + string(state) + `"><main>
  <div class="lamp" role="status" aria-label="` + js.Esc(aria) + `"><i></i></div>
  <h1>` + js.Esc(title) + `</h1>
  <p class="host">` + hostLine + `</p>
  <p class="what">` + detail + `</p>` + failure + `
  <p class="tick" aria-hidden="true"></p>
  <footer>pstack · wake-on-call</footer>
</main>
<script>
  (function () {
    var tries = 0, born = Date.now();
    var tick = document.querySelector('.tick');
    setInterval(function () {
      var s = Math.round((Date.now() - born) / 1000);
      if (s >= 5 && tick) tick.textContent = 'still checking — ' + (s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's');
    }, 1000);
    function poll() {
      fetch(location.href, { cache: 'no-store', redirect: 'manual' }).then(function (r) {
        if (r.headers.get('x-pstack-wake') !== '1' && r.status < 500) { location.reload(); return; }
        tries++; setTimeout(poll, tries < 20 ? 3000 : 8000);
      }).catch(function () { tries++; setTimeout(poll, 5000); });
    }
    setTimeout(poll, 3000);
  })();
</script>
</body></html>
`
}
