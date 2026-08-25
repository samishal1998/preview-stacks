package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/hostvars"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/readiness"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func TestCrToNl(t *testing.T) {
	// negative control: drop the `i++` on CRLF — "ls\r\n" becomes two newlines and runs an extra empty command.
	cases := map[string]string{"ls\r": "ls\n", "ls\r\n": "ls\n", "a\rb\r\nc\n": "a\nb\nc\n", "": "", "\r\r\n": "\n\n"}
	for in, want := range cases {
		if got := string(CrToNl([]byte(in))); got != want {
			t.Errorf("CrToNl(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVarsFromDropsTokenAndLastWins(t *testing.T) {
	// negative control: use url.ParseQuery's first-value — PR=2 is lost.
	m := varsFrom("PR=1&PR=2&token=eyJ&tail=5&a=b+c")
	if m["PR"] != "2" || m["tail"] != "5" || m["a"] != "b c" {
		t.Errorf("got %v", m)
	}
	if _, has := m["token"]; has {
		t.Error("token must never become a variable")
	}
}

func TestSessionCandidatesReadsEveryCookie(t *testing.T) {
	// negative control: return only the first match — the second (live) session is never consulted.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("cookie", "pstack_session=dead; other=1; pstack_session=live")
	got := sessionCandidates(r)
	if len(got) != 2 || got[0] != "dead" || got[1] != "live" {
		t.Errorf("got %v", got)
	}
}

func TestSessionCookieSecureOnlyBehindTLS(t *testing.T) {
	// negative control: always append Secure — loopback dev login cookies stop being sent.
	r := httptest.NewRequest("GET", "/", nil)
	if c := sessionCookie(r, "v", 10); c != "pstack_session=v; HttpOnly; Path=/; SameSite=Lax; Max-Age=10" {
		t.Errorf("plain: %s", c)
	}
	r.Header.Set("x-forwarded-proto", "https")
	if c := sessionCookie(r, "v", 10); c != "pstack_session=v; HttpOnly; Path=/; SameSite=Lax; Max-Age=10; Secure" {
		t.Errorf("tls: %s", c)
	}
}

func TestBaseURLPrefersConfigOverHeaders(t *testing.T) {
	// negative control: read the headers first — a forwarded host changes the SSO redirect_uri.
	r := httptest.NewRequest("GET", "http://127.0.0.1:7878/x", nil)
	if baseURL("p.example.com", r) != "https://control.p.example.com" {
		t.Error("domain wins")
	}
	if baseURL("", r) != "http://127.0.0.1:7878" {
		t.Errorf("host fallback: %s", baseURL("", r))
	}
	r.Header.Set("x-forwarded-proto", "https")
	r.Header.Set("x-forwarded-host", "preview.example.com, internal")
	if baseURL("", r) != "https://preview.example.com, internal" {
		t.Errorf("forwarded: %s", baseURL("", r))
	}
	if requestHost(r) != "preview.example.com" {
		t.Errorf("requestHost: %s", requestHost(r))
	}
	r2 := httptest.NewRequest("GET", "http://App-PR-7.Example.com:8080/", nil)
	if requestHost(r2) != "app-pr-7.example.com" {
		t.Errorf("requestHost: %s", requestHost(r2))
	}
}

func TestTuningFromEnv(t *testing.T) {
	// negative control: accept "-5" — a negative timeout becomes a timeout.
	env := map[string]string{"PSTACK_READINESS_POLL_MS": "50", "PSTACK_READINESS_TIMEOUT_MS": "-5", "PSTACK_SSO_STATE_TTL_S": "soon", "PSTACK_SSO_DISCOVERY_TTL_S": ""}
	tu := TuningFromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if tu.ReadinessPollMs != 50 || tu.ReadinessTimeoutMs != 0 || tu.SSOStateTTLS != 0 || tu.SSODiscoveryTTLS != 0 {
		t.Errorf("got %+v", tu)
	}
}

func TestWatchTimeoutMs(t *testing.T) {
	// negative control: make watchTimeoutMs fall back to 180_000 instead of 0 — the "unset" cases
	// fail, because 180s here would silently override PSTACK_READINESS_TIMEOUT_MS. (Also run: accept
	// raw >= 0 in boundedSeconds — timeout=0 floors to 5s instead of deferring to the watcher.)
	cases := []struct {
		query string
		want  int64 // milliseconds, as StartOptions.TimeoutMs receives them
	}{
		{"timeout=600", 600_000},                       // in range
		{"timeout=99999", ReadinessTimeoutMaxS * 1000}, // capped, never rejected
		{"timeout=3600", ReadinessTimeoutMaxS * 1000},  // exactly max
		{"timeout=1", ReadinessTimeoutMinS * 1000},     // floored: the up route's watch emits, and a sub-poll deadline fires stack.timedout on a SUCCESSFUL deploy
		{"timeout=0.01", ReadinessTimeoutMinS * 1000},  // likewise
		// 0 is "unset" — and unset must reach the watcher AS 0, whose <= 0 branch is the ONLY place
		// the host's PSTACK_READINESS_TIMEOUT_MS default applies. Any concrete fallback here (the
		// old 180) is > 0, so the env knob would never be consulted. 0 is still not "no deadline":
		// the watcher substitutes its own default before starting the clock.
		{"timeout=0", 0},
		{"timeout=-5", 0},  // negative likewise
		{"timeout=abc", 0}, // NaN likewise
		{"", 0},            // absent
	}
	for _, c := range cases {
		if got := watchTimeoutMs(c.query); got != c.want {
			t.Fatalf("watchTimeoutMs(%q) = %v, want %v", c.query, got, c.want)
		}
	}
	// The `wait` bound is a different ceiling on the same helper — a long poll must not outlast the
	// client's own read timeout — and it has NO floor: a 1s long-poll is legitimate.
	if got := boundedSeconds("wait=300", "wait", 0, 0, 60); got != 60 {
		t.Fatalf("wait clamps to 60, got %v", got)
	}
	if got := boundedSeconds("wait=1", "wait", 0, 0, 60); got != 1 {
		t.Fatalf("wait=1 stays 1, got %v", got)
	}
}

// ── multi-provider SSO: /start chooses, the state binds ──────────────────────────────────────────

// ssoTestServer is the smallest Server the two SSO legs touch: the store-backed auth (providers +
// the transient state), the sso client, and a log sink. Nothing listens.
func ssoTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// `settings` too: the SSO sign-in options resolve an inheriting provider's role through it, and
	// a nil one is a panic rather than a default.
	return &Server{store: st, auth: auth.New(st), settings: settings.New(st, 0), ssoClient: sso.NewClient(nil), ssoTTL: 60, opts: Options{Log: func(string) {}}}
}

func saveSsoProvider(t *testing.T, a *auth.Auth, key, raw string) {
	t.Helper()
	v, err := omap.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := sso.ParseConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetSsoProvider(key, cfg, "shh"); err != nil {
		t.Fatal(err)
	}
}

// ssoErrorOf is the decoded sso_error a navigation failure landed on the login page with, or ""
// when the response went somewhere else (the provider's authorize URL).
func ssoErrorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	loc := w.Header().Get("location")
	if w.Code != 302 {
		t.Fatalf("status %d (location %q), want 302", w.Code, loc)
	}
	if !strings.HasPrefix(loc, "/login?sso_error=") {
		return ""
	}
	msg, err := url.QueryUnescape(strings.TrimPrefix(loc, "/login?sso_error="))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestSsoStartRefusesToChooseBetweenProviders(t *testing.T) {
	// negative control: make the keyless default arm pick enabled[0] instead of failing — the
	// two-provider case silently starts a login with whichever key sorts first, and the "(corp,
	// github)" assertion fails. (Run, observed failing, restored.)
	s := ssoTestServer(t)
	saveSsoProvider(t, s.auth, "github", `{"provider":"github","clientId":"c1"}`)
	saveSsoProvider(t, s.auth, "corp", `{"mode":"oauth2","clientId":"c2","label":"Corp","authorizeUrl":"https://corp.example/auth","tokenUrl":"https://corp.example/token"}`)

	start := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ssoStart(w, httptest.NewRequest("GET", "/api/auth/sso/start"+query, nil))
		return w
	}

	// Two enabled providers and no ?provider= is a refusal NAMING BOTH, never a guess.
	if msg := ssoErrorOf(t, start("")); !strings.Contains(msg, "(corp, github)") {
		t.Fatalf("keyless start with two providers: %q must name both keys", msg)
	}
	// A named provider starts against exactly that provider.
	if w := start("?provider=corp"); !strings.HasPrefix(w.Header().Get("location"), "https://corp.example/auth?") {
		t.Fatalf("?provider=corp went to %q", w.Header().Get("location"))
	}
	if msg := ssoErrorOf(t, start("?provider=nope")); !strings.Contains(msg, `"nope"`) {
		t.Fatalf("unknown provider: %q must name it", msg)
	}
	// With exactly one ENABLED provider the keyless start still works — the 0.30.0 login link.
	saveSsoProvider(t, s.auth, "corp", `{"mode":"oauth2","clientId":"c2","label":"Corp","authorizeUrl":"https://corp.example/auth","tokenUrl":"https://corp.example/token","enabled":false}`)
	if w := start(""); !strings.HasPrefix(w.Header().Get("location"), "https://github.com/login/oauth/authorize?") {
		t.Fatalf("single-enabled fallback went to %q", w.Header().Get("location"))
	}
}

func TestSsoCallbackBindsTheStateToItsProvider(t *testing.T) {
	// negative control: on a parked key with no matching row, fall back to enabled[0] instead of
	// refusing — the code is exchanged against a provider this login never started with, and the
	// `no longer configured` assertions fail. (Run, observed failing, restored.)
	s := ssoTestServer(t)
	saveSsoProvider(t, s.auth, "github", `{"provider":"github","clientId":"c1"}`)
	saveSsoProvider(t, s.auth, "corp", `{"mode":"oauth2","clientId":"c2","label":"Corp","authorizeUrl":"https://corp.example/auth","tokenUrl":"https://corp.example/token"}`)

	park := func(state, value string) {
		if err := s.auth.Transient().Set("sso:"+state, value, 60); err != nil {
			t.Fatal(err)
		}
	}
	callback := func(state string) string {
		w := httptest.NewRecorder()
		s.ssoCallback(w, httptest.NewRequest("GET", "/api/auth/sso/callback?code=abc&state="+state, nil))
		return ssoErrorOf(t, w)
	}

	// A state carrying a key with no row must refuse, not complete against some other provider.
	park("s1", `{"verifier":"v","next":"/","provider":"gone"}`)
	if msg := callback("s1"); !strings.Contains(msg, `sign-in provider "gone" is no longer configured`) {
		t.Fatalf("forged/stale provider key: %q", msg)
	}
	// Deleted between /start and the consent screen: same refusal, naming the key.
	park("s2", `{"verifier":"v","next":"/","provider":"corp"}`)
	if _, err := s.auth.DeleteSsoProvider("corp"); err != nil {
		t.Fatal(err)
	}
	if msg := callback("s2"); !strings.Contains(msg, `sign-in provider "corp" is no longer configured`) {
		t.Fatalf("deleted provider: %q", msg)
	}
	// A state parked before provider keys existed carries none and reads as expired.
	park("s3", `{"verifier":"v","next":"/"}`)
	if msg := callback("s3"); !strings.Contains(msg, "expired") {
		t.Fatalf("keyless parked state: %q", msg)
	}
}

// ── wake-on-call: the page is served until the stack SERVES, not until the sleep record goes ─────

// wakingServer is the smallest Server wakeFor's woken branch touches: a registry directory holding
// one deployment with NO sleep record, the index, the waking map and a readiness watcher. It
// resolves no spec and starts no job, which is the point — by then the wake has already run.
func wakingServer(t *testing.T, stack string) *Server {
	t.Helper()
	dir := t.TempDir()
	dep := filepath.Join(dir, "deployments", "pr-9")
	if err := os.MkdirAll(dep, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "spec.yml"), []byte("version: 1\nstack: "+stack+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "meta.json"), []byte(`{"id":"pr-9","kind":"isolated","createdAt":1,"updatedAt":1}`), 0o666); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		registry:   registry.New(dir),
		sleepIndex: scheduler.NewSleepIndex(),
		waking:     map[string]wakingUp{},
		readiness:  readiness.New(readiness.Options{PollMs: 20, TimeoutMs: 60_000}),
		opts:       Options{Domain: "preview.example.com", Log: func(string) {}},
	}
	t.Cleanup(s.readiness.StopAll)
	// Exactly what startLifecycle leaves behind after a successful wake: the record cleared, the
	// hostnames kept, the stack name kept so no re-resolve is needed.
	s.waking["pr-9"] = wakingUp{stack: stack, entry: scheduler.SleepEntry{ID: "pr-9", Hosts: []string{"app-pr-9.preview.example.com"}}}
	s.reindex()
	return s
}

func get(s *Server, host string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	if !s.wakeFor(w, host) {
		w.Code = 0 // fell through — the caller would serve the control UI here
	}
	return w
}

func TestWakeForHoldsTheHostnameUntilReadinessSettles(t *testing.T) {
	// negative control: restore `dep.Sleep == nil → return false` (drop the waking branch) — every
	// assertion below that expects 503 gets the fall-through instead, which is the reported bug:
	// the embedded control UI served on a preview's hostname.
	s := wakingServer(t, "pr-9")

	// Docker that never answers: the watch stays `watching`, which is the whole window between
	// `up` returning and the app listening — the one the visitor spent on a 502 or the control UI.
	s.readiness.Start("pr-9", exec.NewFake(func(string) bool { return true }, ""), readiness.StartOptions{TimeoutMs: 60_000, Restart: true})
	w := get(s, "app-pr-9.preview.example.com")
	if w.Code != 503 || w.Header().Get("x-pstack-wake") != "1" {
		t.Fatalf("status %d, x-pstack-wake %q — the hostname must still be the visitor's", w.Code, w.Header().Get("x-pstack-wake"))
	}
	if body := w.Body.String(); !strings.Contains(body, "is awake and its containers are starting") {
		t.Errorf("body: %s", body)
	}
	// It is not one answer that gets it right: the page is served for as long as it takes.
	if get(s, "app-pr-9.preview.example.com").Code != 503 {
		t.Error("a second request must not fall through either")
	}
	// The control plane's own hostnames are still nobody's to wake.
	if get(s, "control.preview.example.com").Code != 0 || get(s, "api.preview.example.com").Code != 0 {
		t.Error("control./api. must never be answered by the wake page")
	}

	// Settling ENDS it, in either direction. Nothing watching any more (a teardown cancels the
	// watch) is the same answer: this process knows nothing Traefik does not.
	s.readiness.Cancel("pr-9")
	if got := get(s, "app-pr-9.preview.example.com"); got.Code != 0 {
		t.Errorf("with no watch the hostname is not ours: %d", got.Code)
	}
	if s.sleepIndex.Size() != 0 {
		t.Errorf("the entry must be forgotten, not leaked: size %d", s.sleepIndex.Size())
	}
}

func TestWakeForShowsTheFAILUREOfAPreviewThatNeverServes(t *testing.T) {
	// negative control: return keep=false for the failed/timedout arm of wakeVerdict — the broken
	// preview falls through to the control UI instead of saying what died, and both 503s below fail.
	s := wakingServer(t, "pr-9")
	// A container that exits 1 on boot: `up` succeeded, so the sleep record is already gone, and
	// readiness is the only thing that knows this preview is never going to answer.
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			return exec.Result{OK: true, Stdout: "aaa111aaa111\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: `[{"Id":"aaa111aaa111","Name":"/pr-9-app-1","Config":{"Image":"app:1","Labels":{"com.docker.compose.service":"app"}},"State":{"Status":"exited","ExitCode":1},"NetworkSettings":{"Networks":{},"Ports":{}}}]`}, true
		}
		return exec.Result{OK: true}, true
	}
	s.readiness.Start("pr-9", f, readiness.StartOptions{TimeoutMs: 60_000, Restart: true})
	if rd, _ := s.readiness.Wait("pr-9", 5_000); rd.State != readiness.Failed {
		t.Fatalf("the watch must settle failed, got %q", rd.State)
	}
	w := get(s, "app-pr-9.preview.example.com")
	if w.Code != 503 {
		t.Fatalf("status %d — a broken preview must show its failure, never the control UI", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "could not start") || !strings.Contains(body, "app: exited with code 1") {
		t.Errorf("the page must name what failed: %s", body)
	}
	// And it keeps saying so: an eternal spinner is what this replaces, not an eternal 200.
	if get(s, "app-pr-9.preview.example.com").Code != 503 {
		t.Error("the failure is the answer for every later request too")
	}
}

func TestWakeVerdict(t *testing.T) {
	// negative control: return `keep` true for the !watching arm — a stack nothing is watching keeps
	// the page forever, and the first case fails. (Also run: make readinessWhy prefer the container
	// name over the service — "app: exited" becomes "pr-9-app-1: exited".)
	reason := func(s string) *string { return &s }
	svc := "app"
	failed := readiness.StackReadiness{State: readiness.Failed, Containers: []readiness.ContainerReadiness{
		{Name: "pr-9-app-1", Service: &svc, Failed: true, Reason: reason("exited with code 1")},
	}}
	cases := []struct {
		name  string
		rd    readiness.StackReadiness
		watch bool
		state scheduler.WakeState
		why   string
		keep  bool
	}{
		{"nothing is watching", readiness.StackReadiness{}, false, "", "", false},
		{"still converging", readiness.StackReadiness{State: readiness.Watching}, true, scheduler.Starting, "", true},
		{"serving", readiness.StackReadiness{State: readiness.Ready}, true, "", "", false},
		{"a container died", failed, true, scheduler.Failed, "app: exited with code 1", true},
		// The bound: a stack that never converges is timed out by readiness, and the visitor is told
		// the deadline rather than watching a spinner that means nothing.
		{"deadline", readiness.StackReadiness{State: readiness.TimedOut, TimeoutMs: 180_000}, true, scheduler.Failed, "it did not start answering within 3m", true},
		// Settled without a reason: still a failure, never a spinner.
		{"failed, no reason given", readiness.StackReadiness{State: readiness.Failed}, true, scheduler.Failed, "it started, then stopped before it served anything", true},
	}
	for _, c := range cases {
		state, why, keep := wakeVerdict(c.rd, c.watch)
		if state != c.state || why != c.why || keep != c.keep {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, state, why, keep, c.state, c.why, c.keep)
		}
	}
}

func TestWakeForForgetsAWakingDeploymentThatIsGone(t *testing.T) {
	// negative control: drop the `s.forgetWaking(id)` from wakeFor's deleted-deployment branch — the
	// entry survives every rebuild, so the index keeps answering for a hostname whose deployment no
	// longer exists, and both checks below fail. (Run, observed failing, restored.)
	//
	// The OTHER end of the waking state, and the one a readiness verdict can never reach: DELETE
	// removes the record while a wake is in flight, so nothing is coming back and the hostname is
	// nobody's again.
	s := wakingServer(t, "pr-9")
	if err := s.registry.Remove("pr-9"); err != nil {
		t.Fatal(err)
	}
	if get(s, "app-pr-9.preview.example.com").Code != 0 {
		t.Fatal("a deleted deployment must not go on being served the waking page")
	}
	if _, still := s.wakingOf("pr-9"); still || s.sleepIndex.Size() != 0 {
		t.Errorf("the entry must be forgotten, not re-added by the next rebuild (waking=%v size=%d)", still, s.sleepIndex.Size())
	}
}

func TestClearSleepHandsTheHostnamesToTheWakingEntry(t *testing.T) {
	// negative control: drop the `s.waking[id] = wakingUp{...}` assignment in clearSleep — the
	// hostname goes out of the index with the record and Find returns "", which is the fall-through
	// to the control UI. (Also run: drop the `else if !waking { delete }` arm — a torn-down
	// deployment keeps waking its own hostname forever.)
	host := "app-pr-9.preview.example.com"
	s := wakingServer(t, "pr-9")
	asleep := func() {
		t.Helper()
		if err := s.registry.SetSleep("pr-9", &registry.SleepRecord{Since: 1, Reason: "operator", Hosts: []string{host}, Rules: []string{}}); err != nil {
			t.Fatal(err)
		}
		s.wakeMu.Lock()
		delete(s.waking, "pr-9")
		s.wakeMu.Unlock()
		s.reindex()
		if s.sleepIndex.Find(host) != "pr-9" {
			t.Fatal("asleep: the record's hostname must be indexed")
		}
	}

	// A wake: the record goes, the hostname does NOT. That swap is the fix.
	asleep()
	s.clearSleep("pr-9", "pr-9", true)
	dep, err := s.registry.Get("pr-9")
	if err != nil || dep == nil || dep.Sleep != nil {
		t.Fatalf("the sleep record must be cleared: %+v (%v)", dep, err)
	}
	if _, ok := s.wakingOf("pr-9"); !ok {
		t.Fatal("a woken deployment must be held as waking")
	}
	if s.sleepIndex.Find(host) != "pr-9" {
		t.Fatal("the hostname must outlive the record it came from")
	}

	// A teardown of what that wake brought back — no sleep record left to clear, only the waking
	// entry, and it has to go: nothing should wake what was deliberately torn down.
	s.clearSleep("pr-9", "pr-9", false)
	if _, ok := s.wakingOf("pr-9"); ok {
		t.Fatal("a torn-down deployment is not waking")
	}
	if s.sleepIndex.Find(host) != "" {
		t.Fatal("its hostname is nobody's")
	}
}

// ── PSTACK_MAX_JOBS: the global concurrency cap, from the environment to the registry ─────────
//
// The queue's HTTP behaviour is covered further down; these two are the KNOB, whose failure mode is
// silence — a cap that is never read still runs jobs, just not the number the operator asked for.

func TestTuningReadsMaxJobs(t *testing.T) {
	// negative control: drop MaxJobs from TuningFromEnv's returned literal — the 2 below reads 0.
	// (Run, observed failing, restored.) The `0` cases are what makes jobs.DefaultMaxRunning apply:
	// any concrete fallback here would silently override it.
	env := map[string]string{"PSTACK_MAX_JOBS": "2"}
	if tu := TuningFromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); tu.MaxJobs != 2 {
		t.Fatalf("MaxJobs = %v, want 2", tu.MaxJobs)
	}
	for _, bad := range []string{"0", "-1", "nope", ""} {
		env["PSTACK_MAX_JOBS"] = bad
		if tu := TuningFromEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); tu.MaxJobs != 0 {
			t.Fatalf("%q must read as unset, got %v", bad, tu.MaxJobs)
		}
	}
}

func TestMaxJobsOptionReachesTheRegistry(t *testing.T) {
	// negative control: pass o.ReadinessPollMs (or nothing) to jobs.New in server.go — the second
	// stack runs instead of queueing, because the cap silently stays at the default 4. (Run,
	// observed failing, restored.) THE PLUMBING TEST: nothing else fails if this option is dropped.
	s, err := New(Options{DataDir: t.TempDir(), Bus: events.New(), MaxJobs: 1, Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	done := make(chan struct{})
	defer close(done)
	block := func(log.Sink, context.Context) (stack.Outcome, error) { <-done; return stack.Outcome{OK: true}, nil }
	a, _ := s.jobs.Start("a", jobs.Verify, block, nil)
	b, _ := s.jobs.Start("b", jobs.Verify, block, nil)
	if a.State != jobs.Running || b.State != jobs.Queued {
		t.Fatalf("one slot: %q then %q, want running then queued", a.State, b.State)
	}
}

// ── the per-stack queue, as the HTTP surface shows it (0.30.0) ────────────────────────────────────
//
// A busy stack no longer refuses a lifecycle POST — it queues one, depth one. These drive the
// routes directly (no listener, no docker): the point is the STATUS and the BODY, which is what a
// CI script and the UI read.

// jobsFixture is the smallest Server the lifecycle and cancel routes touch: a registry directory
// with two deployments, the host-variable store resolveDep needs, and a job registry whose global
// cap the caller chooses — cap 1 is how a job can be observed WAITING with nothing else running on
// its own stack.
func jobsFixture(t *testing.T, maxRunning int) *Server {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []struct{ id, stack string }{{"pr-1", "app-pr-1"}, {"pr-2", "app-pr-2"}} {
		p := filepath.Join(dir, "deployments", d.id)
		if err := os.MkdirAll(p, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "spec.yml"), []byte("version: 1\nstack: "+d.stack+"\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "meta.json"), []byte(`{"id":"`+d.id+`","kind":"isolated","createdAt":1,"updatedAt":1}`), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Server{
		registry: registry.New(dir),
		store:    st,
		hostVars: hostvars.New(st),
		jobs:     jobs.New(bus, maxRunning),
		bus:      bus,
		// jobStream selects on it: a nil ctx is a nil dereference, not an idle stream.
		ctx:    ctx,
		cancel: cancel,
		opts:   Options{Log: func(string) {}},
	}
}

// occupy starts a job that runs until it is released or cancelled, so the stack is genuinely busy.
// The returned func lets it finish; it is never needed when the test cancels the stack instead.
func occupy(t *testing.T, s *Server, stackName string) func() {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	j, ok := s.jobs.Start(stackName, jobs.Verify, func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Info, "holding "+stackName)
		select {
		case <-ch:
		case <-ctx.Done():
		}
		return stack.Outcome{OK: true}, nil
	}, nil)
	if !ok || j.State != jobs.Running {
		t.Fatalf("the occupying job must be running, got %q (ok=%v)", j.State, ok)
	}
	return release
}

// post drives one route the way routes() would, with a developer principal.
func post(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, nil)
	if err := s.routes(w, r, path, principalOf("developer"), varsFrom("")); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestLifecycleQueuesForABusyStackInsteadOf409(t *testing.T) {
	// negative control: put the old refusal back at the top of lifecycle —
	// `if s.jobs.IsBusy(st.Stack) { writeJSON(w, 409, …); return nil }` — and the second POST is a
	// 409 again, which is the contract this test exists to pin. (Run, observed failing, restored.)
	//
	// THE CONTRACT CHANGE. Five rapid pushes to a PR used to be one deploy and four 409s a CI script
	// had to interpret; they are now one deploy and exactly one more carrying the newest spec.
	s := jobsFixture(t, 4)
	occupy(t, s, "app-pr-1")

	w := post(t, s, "/api/deployments/pr-1/up")
	if w.Code != 202 {
		t.Fatalf("a busy stack must ACCEPT, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"state": "queued"`) {
		t.Fatalf("the 202 must say the job is waiting: %s", w.Body.String())
	}
	// Depth one: a second POST supersedes the first waiting job rather than stacking a third.
	w2 := post(t, s, "/api/deployments/pr-1/up")
	if w2.Code != 202 || !strings.Contains(w2.Body.String(), `"state": "queued"`) {
		t.Fatalf("the replacement must also be accepted: %d %s", w2.Code, w2.Body.String())
	}
	queued := 0
	for _, j := range s.jobs.List() {
		if j.State == jobs.Queued {
			queued++
		}
		if j.State == jobs.Superseded && j.StartedAt != nil {
			t.Error("a superseded job never started, so startedAt must stay null")
		}
	}
	if queued != 1 {
		t.Fatalf("depth is one, got %d queued", queued)
	}
	s.jobs.CancelStack("app-pr-1", "test")
}

func TestLifecycleStill409sWhileTheDeploymentIsHeld(t *testing.T) {
	// negative control: change lifecycle's `if !ok {` to `if !ok && false {` — the POST answers 202
	// with an empty job stub while PUT/DELETE is mid-write, which is the race Hold exists to stop.
	// (Run, observed failing with `a held stack must still refuse: 202`, restored.)
	//
	// The ONE refusal left. It is a different fact from "busy" and must not have gone away with it.
	s := jobsFixture(t, 4)
	release, ok := s.jobs.Hold("app-pr-1")
	if !ok {
		t.Fatal("an idle stack must be holdable")
	}
	defer release()
	w := post(t, s, "/api/deployments/pr-1/up")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "being reconfigured") {
		t.Fatalf("a held stack must still refuse: %d %s", w.Code, w.Body.String())
	}
}

func TestCancelStackStopsTheRunningJobAndTheQueuedOne(t *testing.T) {
	// negative control: have cancelStack call `s.jobs.Cancel(running.ID, by)` instead of
	// CancelStack — the queued job survives and dispatches the moment the running one dies, so the
	// "nothing outstanding" check at the end fails. (Also run: report `acted` as a plain
	// `var stubs []jobs.Stub` — the empty case serialises as `null` and its assertion fails. Both
	// observed failing, restored.)
	s := jobsFixture(t, 4)
	occupy(t, s, "app-pr-1")
	if post(t, s, "/api/deployments/pr-1/up").Code != 202 {
		t.Fatal("the second job must queue")
	}

	w := post(t, s, "/api/deployments/pr-1/cancel")
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("cancel answered %d: %s", w.Code, body)
	}
	// The running one first, then the one that never started — and the warning is the running
	// job's, because only a job that RAN can have left anything behind.
	// The running one still reads `running` — its own goroutine writes the terminal record when the
	// work returns — while the one that never started is already `cancelled`.
	if !strings.Contains(body, `"state": "running"`) || !strings.Contains(body, `"state": "cancelled"`) {
		t.Fatalf("both jobs must be reported: %s", body)
	}
	if strings.Index(body, `"state": "running"`) > strings.Index(body, `"state": "cancelled"`) {
		t.Fatalf("the running job comes first: %s", body)
	}
	if !strings.Contains(body, "run verify to see what exists") {
		t.Fatalf("a cancelled RUNNING job leaves partial state and must say so: %s", body)
	}
	// POLLED, not asserted inline. CancelStack closes the running job's context; `r.live` is cleared
	// on that job's OWN goroutine in finish's third critical section, so "the POST returned" and
	// "the registry has let go" are different instants. Asserting inline passes on an idle laptop
	// and fails under the parallel load of `go test ./...`, which is exactly how CI runs it.
	busyUntil := time.Now().Add(3 * time.Second)
	for s.jobs.IsBusy("app-pr-1") && time.Now().Before(busyUntil) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.jobs.IsBusy("app-pr-1") {
		t.Fatal("nothing may be left outstanding for the stack")
	}
	// Idempotent: nothing outstanding is a 200 with an EMPTY list, never null and never a 404.
	again := post(t, s, "/api/deployments/pr-1/cancel").Body.String()
	if !strings.Contains(again, `"cancelled": []`) {
		t.Fatalf("an idle stack cancels nothing: %s", again)
	}
}

func TestCancelStackOnAJobWaitingForAGlobalSlotSaysNothingRan(t *testing.T) {
	// negative control: give cancelStack's `warning` the RUNNING job's text as its default, so the
	// `ran` split stops mattering — this operator is sent hunting for partial state that cannot
	// exist, the exact reason `superseded` is not a flavour of `cancelled`. (Run, observed failing,
	// restored.)
	//
	// Cap of ONE: pr-2's stack is idle, and the job still waits — the global cap, not its own stack.
	s := jobsFixture(t, 1)
	occupy(t, s, "app-pr-1")
	if w := post(t, s, "/api/deployments/pr-2/up"); w.Code != 202 || !strings.Contains(w.Body.String(), `"state": "queued"`) {
		t.Fatalf("over the cap a job waits, it is not refused: %d %s", w.Code, w.Body.String())
	}
	body := post(t, s, "/api/deployments/pr-2/cancel").Body.String()
	if !strings.Contains(body, "Nothing had started") || strings.Contains(body, "run verify") {
		t.Fatalf("a job that never ran left nothing behind: %s", body)
	}
}

func TestJobStreamStaysOpenForAQueuedJobAndDeliversItsLog(t *testing.T) {
	// negative control: restore `if state != jobs.Running` in jobStream — the stream closes with
	// `done` before the job ever starts and BOTH halves below fail: the early-close check fires,
	// and the line the job later emits never reaches the stream. (Run, observed failing, restored.)
	//
	// The two failures are different. Closing early is the visible one; the other is a stream that
	// opens, stays silent, and then misses everything once the job runs.
	s := jobsFixture(t, 1)
	release := occupy(t, s, "app-pr-1")

	waiting, ok := s.jobs.Start("app-pr-2", jobs.Up, func(sink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink.Emit(log.Info, "the queued job ran")
		return stack.Outcome{OK: true}, nil
	}, nil)
	if !ok || waiting.State != jobs.Queued {
		t.Fatalf("the second stack's job must be waiting for a slot, got %q", waiting.State)
	}

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.jobStream(w, httptest.NewRequest("GET", "/api/jobs/"+waiting.ID+"/stream", nil), waiting)
	}()
	select {
	case <-done:
		t.Fatal("a queued job's stream must stay open — it has not started, it is not over")
	case <-time.After(100 * time.Millisecond):
	}

	release() // the slot frees, the queued job dispatches
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream must end when the job does")
	}
	body := w.Body.String()
	if !strings.Contains(body, "the queued job ran") {
		t.Fatalf("every line the job emits after it starts must reach the stream: %s", body)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Fatalf("the stream must end with done: %s", body)
	}
}

func TestCancelStackIsADeveloperRouteOfItsOwn(t *testing.T) {
	// negative control: drop the `{re: deployCancelRe, …}` row from permissions.go — the route falls
	// through to default-deny and `required` returns rootOnly, so the first check fails. (Run,
	// observed failing, restored. Also run: fold `cancel` into deploymentRe's verb group — the
	// deploymentRe check below fails, which is the drift this shape refuses.)
	if got := required("POST", "/api/deployments/pr-1/cancel"); got != auth.Developer {
		t.Fatalf("stopping work is the same tier as starting it, got %q", got)
	}
	// Its own pattern: the lifecycle regex must NOT swallow it, or the two can never hold
	// different tiers.
	if deploymentRe.MatchString("/api/deployments/pr-1/cancel") {
		t.Fatal("deploymentRe must not match the cancel route")
	}
	if deployCancelRe.MatchString("/api/deployments/pr-1/up") {
		t.Fatal("deployCancelRe must not match a lifecycle action")
	}
}

func TestMaxJobsKnobIsReadLikeEveryOtherServeKnob(t *testing.T) {
	// negative control: drop `MaxJobs: num("PSTACK_MAX_JOBS")` from TuningFromEnv — the knob reads 0
	// forever, the host silently runs the default 4, and the first assertion fails. (Run, observed
	// failing, restored.)
	//
	// 0 is not "no cap": it is "unset", and jobs.New turns <= 0 into jobs.DefaultMaxRunning. An
	// operator who writes PSTACK_MAX_JOBS=0 meaning "stop running jobs" gets the default, which is
	// the same `??` semantics every other knob here has.
	read := func(v string) float64 {
		return TuningFromEnv(func(k string) (string, bool) {
			if k == "PSTACK_MAX_JOBS" {
				return v, true
			}
			return "", false
		}).MaxJobs
	}
	if got := read("2"); got != 2 {
		t.Fatalf("PSTACK_MAX_JOBS=2 read as %v", got)
	}
	for _, bad := range []string{"0", "-1", "many", ""} {
		if got := read(bad); got != 0 {
			t.Fatalf("PSTACK_MAX_JOBS=%q must fall back to the default, read as %v", bad, got)
		}
	}
}

func TestJobCancelRouteTakesAQueuedJobAndSaysNothingRan(t *testing.T) {
	// negative control: restore `if job.State != jobs.Running` in routes.go's cancel block — a
	// QUEUED job answers `409 already finished (queued)` and the first check fails, which is a
	// client handed a job id in a 202 and then refused the one thing it can do with it. (Run,
	// observed failing, restored. Also run: drop the Queued arm of the warning — the "It had not
	// started" check fails and the operator is sent hunting for partial state that cannot exist.)
	s := jobsFixture(t, 4)
	occupy(t, s, "app-pr-1")
	if w := post(t, s, "/api/deployments/pr-1/up"); w.Code != 202 {
		t.Fatalf("the second job must queue: %d %s", w.Code, w.Body.String())
	}
	id := ""
	for _, j := range s.jobs.List() {
		if j.State == jobs.Queued {
			id = j.ID
		}
	}
	if id == "" {
		t.Fatal("no queued job to cancel")
	}

	w := post(t, s, "/api/jobs/"+id+"/cancel")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "It had not started") {
		t.Fatalf("cancelling a queued job: %d %s", w.Code, w.Body.String())
	}
	if j, ok := s.jobs.Get(id); !ok || j.State != jobs.Cancelled {
		t.Fatalf("it must be terminal under its own id: %+v (ok=%v)", j, ok)
	}
	// Terminal is forever: the SAME route now answers 409, which is the half of the old guard that
	// was right.
	if again := post(t, s, "/api/jobs/"+id+"/cancel"); again.Code != 409 {
		t.Fatalf("a finished job is still a 409, got %d: %s", again.Code, again.Body.String())
	}
}

// ── the two runtime host settings: the read, the two writes, and the cap that applies NOW ────────
//
// The failure mode of every one of these is SILENCE: a stored cap that boot ignores, a PUT that
// stores a number nothing reads until a restart, a `source` that says "env" while the database is
// what answered. None of them breaks anything visibly, and all of them make an operator's box lie.

// settingsFixture is the smallest Server the settings routes touch: a real store (the table is
// migration 8), the settings reader over it with `envMaxJobs` as the caller chooses, and a job
// registry so a write can be observed reaching it.
func settingsFixture(t *testing.T, envMaxJobs int) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := events.New()
	return &Server{
		store:    st,
		settings: settings.New(st, envMaxJobs),
		jobs:     jobs.New(bus, envMaxJobs),
		bus:      bus,
		opts:     Options{MaxJobs: envMaxJobs, Log: func(string) {}},
	}
}

// callSettings drives one settings route as routes() would, with a root principal (the tiers
// themselves are permissions_test.go's job).
func callSettings(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if err := s.routes(w, r, path, &auth.Principal{Kind: auth.KindRoot}, map[string]string{}); err != nil {
		s.fail(w, err)
	}
	return w
}

// TestSettingsReadSaysWhereEachValueCameFrom is the whole point of the read: a box saying 4 is
// unexplainable unless it says whether 4 is the operator's choice, the environment's, or the
// binary's.
//
// negative control: return a constant "db" from settingRow's source (or drop the `s.opts.MaxJobs >=
// 1` arm) → the env and default cases fail; emit `s.opts.MaxJobs` unconditionally in the `env`
// block → the null case fails, and a host that never set the variable reads as one that set it to
// a value this binary ignored. All three run, observed failing, restored.
func TestSettingsReadSaysWhereEachValueCameFrom(t *testing.T) {
	// Nothing stored, nothing in the environment: both keys are the shipped default.
	s := settingsFixture(t, 0)
	body := callSettings(t, s, "GET", "/api/settings", "").Body.String()
	for _, want := range []string{`"key": "max_jobs"`, `"value": 4`, `"source": "default"`,
		`"key": "default_role"`, `"value": "viewer"`, `"minRole": "maintainer"`, `"minRole": "admin"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("a bare host: %s\nmissing %s", body, want)
		}
	}

	// …and the environment block says NULL rather than 0, because 0 is how `PSTACK_MAX_JOBS=0` is
	// read (unset) and a literal 0 here could not be told apart from "never set".
	if !strings.Contains(body, `"PSTACK_MAX_JOBS": null`) {
		t.Fatalf("an unset PSTACK_MAX_JOBS must read as null: %s", body)
	}

	// PSTACK_MAX_JOBS set, nothing stored: the value is the environment's, and it SAYS so.
	s = settingsFixture(t, 2)
	body = callSettings(t, s, "GET", "/api/settings", "").Body.String()
	if !strings.Contains(body, `"value": 2`) || !strings.Contains(body, `"source": "env"`) || !strings.Contains(body, `"PSTACK_MAX_JOBS": 2`) {
		t.Fatalf("PSTACK_MAX_JOBS=2: %s", body)
	}

	// Stored: the database outranks the environment, and the source changes with it.
	if err := s.settings.Set(settings.KeyMaxJobs, "7"); err != nil {
		t.Fatal(err)
	}
	if err := s.settings.Set(settings.KeyDefaultRole, "developer"); err != nil {
		t.Fatal(err)
	}
	body = callSettings(t, s, "GET", "/api/settings", "").Body.String()
	if !strings.Contains(body, `"value": 7`) || !strings.Contains(body, `"source": "db"`) {
		t.Fatalf("a stored cap must outrank the environment and say db: %s", body)
	}
	if !strings.Contains(body, `"value": "developer"`) || strings.Count(body, `"source": "db"`) != 2 {
		t.Fatalf("both keys stored: %s", body)
	}

	// A HAND-EDITED ROW resolves down — and must not claim the database answered. This is the case
	// string-comparing the stored text against the resolved value gets wrong in the other direction
	// ("008" resolves to 8 and IS the database's answer), which is why the source mirrors the
	// package's own predicate instead.
	for _, bad := range []struct{ key, value, wantSource string }{
		{settings.KeyMaxJobs, "0", "env"},
		{settings.KeyDefaultRole, "superuser", "default"},
	} {
		if _, err := s.store.DB.Exec("UPDATE settings SET value = ? WHERE key = ?", bad.value, bad.key); err != nil {
			t.Fatal(err)
		}
		body = callSettings(t, s, "GET", "/api/settings", "").Body.String()
		if got := sourceOf(t, body, bad.key); got != bad.wantSource {
			t.Fatalf("a %s row of %q reads as %q, want %q: %s", bad.key, bad.value, got, bad.wantSource, body)
		}
	}
	if !strings.Contains(body, `"value": "viewer"`) {
		t.Fatalf("an unrankable stored role must resolve DOWN to viewer, never up: %s", body)
	}
	if _, err := s.store.DB.Exec("UPDATE settings SET value = '008' WHERE key = 'max_jobs'"); err != nil {
		t.Fatal(err)
	}
	body = callSettings(t, s, "GET", "/api/settings", "").Body.String()
	if !strings.Contains(body, `"value": 8`) || !strings.Contains(body, `"source": "db"`) {
		t.Fatalf(`"008" is what the resolver took, so the source is db: %s`, body)
	}
}

// TestStoredMaxJobsBeatsTheEnvironmentAtBoot is the boot half of the contract: an operator who set
// the cap in the UI is not overridden by the container's environment on the next restart.
//
// negative control: restore `jobs.New(o.Bus, o.MaxJobs)` in server.go's New → the second job runs
// instead of queueing, because the stored 1 was never consulted. Run, observed failing, restored.
// THE PLUMBING TEST: nothing else in the suite fails if that resolution is dropped.
func TestStoredMaxJobsBeatsTheEnvironmentAtBoot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.New(st, 0).Set(settings.KeyMaxJobs, "1"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// PSTACK_MAX_JOBS says 4; the database says 1. The database wins.
	s, err := New(Options{DataDir: dir, Bus: events.New(), MaxJobs: 4, Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	done := make(chan struct{})
	defer close(done)
	block := func(log.Sink, context.Context) (stack.Outcome, error) { <-done; return stack.Outcome{OK: true}, nil }
	a, _ := s.jobs.Start("a", jobs.Verify, block, nil)
	b, _ := s.jobs.Start("b", jobs.Verify, block, nil)
	if a.State != jobs.Running || b.State != jobs.Queued {
		t.Fatalf("the stored cap of 1: %q then %q, want running then queued", a.State, b.State)
	}
}

// TestPutMaxJobsAppliesWithoutARestart is the other half, and the reason the feature exists: a
// raised cap starts a job that is ALREADY WAITING, on this request, and a lowered one cancels
// nothing.
//
// negative control: drop `s.jobs.SetMaxRunning(s.settings.MaxJobs())` from putSetting → the queued
// job is still queued after the PUT and the first check fails; drop the `note` → the "run to
// completion" check fails, and an operator who typed 1 while four jobs ran is left believing three
// were cancelled. Both run, observed failing, restored.
func TestPutMaxJobsAppliesWithoutARestart(t *testing.T) {
	s := settingsFixture(t, 1)
	done := make(chan struct{})
	defer close(done)
	block := func(log.Sink, context.Context) (stack.Outcome, error) { <-done; return stack.Outcome{OK: true}, nil }
	running, _ := s.jobs.Start("a", jobs.Verify, block, nil)
	waiting, _ := s.jobs.Start("b", jobs.Verify, block, nil)
	if running.State != jobs.Running || waiting.State != jobs.Queued {
		t.Fatalf("the fixture must start one job waiting for a slot: %q %q", running.State, waiting.State)
	}

	w := callSettings(t, s, "PUT", "/api/settings/max_jobs", `{"value":2}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"value": 2`) || !strings.Contains(w.Body.String(), `"source": "db"`) {
		t.Fatalf("PUT max_jobs: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "run to completion") {
		t.Fatalf("the response must say lowering the cap cancels nothing: %s", w.Body.String())
	}
	// The slot that did not exist a moment ago now does, and the pump used it.
	if j, ok := s.jobs.Get(waiting.ID); !ok || j.State != jobs.Running {
		t.Fatalf("the waiting job must be running after the cap was raised, got %q", j.State)
	}

	// Lowering it mid-flight KILLS NOTHING. Two jobs are running and the cap is now 1.
	if w := callSettings(t, s, "PUT", "/api/settings/max_jobs", `{"value":1}`); w.Code != 200 {
		t.Fatalf("lowering: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{running.ID, waiting.ID} {
		if j, _ := s.jobs.Get(id); j.State != jobs.Running {
			t.Fatalf("lowering the cap stopped a running job: %q", j.State)
		}
	}
}

// TestPutSettingRefusesWhatItCannotStore: the validator's refusals must reach the wire as 400s, not
// 500s, and a key the chain never dispatches must 404 rather than reaching a validator at all.
//
// negative control: drop `settings.IsError(err)` from fail() in server.go → every refusal below is
// a 500. Run, observed failing, restored.
func TestPutSettingRefusesWhatItCannotStore(t *testing.T) {
	s := settingsFixture(t, 0)
	for _, c := range []struct{ path, body string }{
		{"/api/settings/max_jobs", `{"value":0}`},
		{"/api/settings/max_jobs", `{"value":8.5}`},
		{"/api/settings/max_jobs", `{"value":"lots"}`},
		{"/api/settings/max_jobs", `{"value":true}`},
		{"/api/settings/default_role", `{"value":"superuser"}`},
		{"/api/settings/default_role", `{"value":""}`},
	} {
		if w := callSettings(t, s, "PUT", c.path, c.body); w.Code != 400 {
			t.Errorf("PUT %s %s = %d %s, want 400", c.path, c.body, w.Code, w.Body.String())
		}
	}
	// A missing `value` names the shape rather than storing "".
	if w := callSettings(t, s, "PUT", "/api/settings/max_jobs", `{}`); w.Code != 400 {
		t.Errorf("no value: %d %s", w.Code, w.Body.String())
	}
	// `8.0` is 8 — rule 7, a JSON number arrives as a float64.
	if w := callSettings(t, s, "PUT", "/api/settings/max_jobs", `{"value":8.0}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"value": 8`) {
		t.Errorf("8.0: %d %s", w.Code, w.Body.String())
	}
	// A third key is not a setting: the chain never dispatches it.
	if w := callSettings(t, s, "PUT", "/api/settings/shell_command", `{"value":"rm -rf /"}`); w.Code != 404 {
		t.Errorf("an unknown key: %d %s", w.Code, w.Body.String())
	}
	// And nothing above wrote anything: both keys still resolve to what the host shipped with.
	body := callSettings(t, s, "GET", "/api/settings", "").Body.String()
	if !strings.Contains(body, `"value": 8`) || !strings.Contains(body, `"value": "viewer"`) {
		t.Errorf("after the refusals: %s", body)
	}
}

// TestDefaultRoleSettingDecidesAnAbsentRole covers both consumers of the host default in one place,
// because they are one decision: an account created with no role named, and an SSO provider that
// inherits.
//
// THE DIRECTION IS THE POINT. Omission may never yield admin — not through the route, not through a
// provider that names nothing, not on a host where nobody ever opened the settings page.
//
// negative control: put `role := string(auth.Viewer)` back in the users POST → the developer case
// fails; make ssoSignInOpts return cfg.DefaultRole unresolved → an inheriting provider mints "" and
// the SSO case fails; drop one field from ssoSignInOpts's literal → the allow-rules case fails.
// All three run, observed failing, restored.
//
// What no Go test here reaches is the CALL SITE in ssoCallback: driving it needs a parked state, a
// provider row and a token exchange against a fake identity provider, which is what
// packages/conformance/test/api-sso.test.ts exists for. That end-to-end assertion belongs there.
func TestDefaultRoleSettingDecidesAnAbsentRole(t *testing.T) {
	s := settingsFixture(t, 0)
	s.auth = auth.New(s.store)

	create := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/users", strings.NewReader(body))
		done, err := s.accountRoutes(w, r, "/api/users", &auth.Principal{Kind: auth.KindRoot})
		if err != nil {
			s.fail(w, err)
		}
		if !done {
			t.Fatal("the users POST was not dispatched")
		}
		return w
	}

	// Nobody set a default: viewer, exactly as before this feature existed.
	if w := create(`{"username":"a1","password":"password123"}`); w.Code != 201 || !strings.Contains(w.Body.String(), `"role": "viewer"`) {
		t.Fatalf("no setting: %d %s", w.Code, w.Body.String())
	}
	// Set it, and the next account created with no role named takes it.
	if err := s.settings.Set(settings.KeyDefaultRole, "developer"); err != nil {
		t.Fatal(err)
	}
	if w := create(`{"username":"a2","password":"password123"}`); w.Code != 201 || !strings.Contains(w.Body.String(), `"role": "developer"`) {
		t.Fatalf("with the setting: %d %s", w.Code, w.Body.String())
	}
	// An explicit role still wins over it, in both directions.
	if w := create(`{"username":"a3","password":"password123","role":"viewer"}`); !strings.Contains(w.Body.String(), `"role": "viewer"`) {
		t.Fatalf("an explicit role must win: %s", w.Body.String())
	}

	// BOOTSTRAP IS NOT A DEFAULT-ROLE CONSUMER and must never become one: the first account on a
	// host is an admin because there is nobody to promote it. A host whose default is `developer`
	// still bootstraps an admin, or an operator locks themselves out of their own control plane.
	first := settingsFixture(t, 0)
	first.auth = auth.New(first.store)
	if err := first.settings.Set(settings.KeyDefaultRole, "developer"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if !first.preGate(w, httptest.NewRequest("POST", "/api/auth/bootstrap", strings.NewReader(`{"username":"boss","password":"password123"}`)), "/api/auth/bootstrap") {
		t.Fatal("the bootstrap route was not dispatched")
	}
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"role": "admin"`) {
		t.Fatalf("bootstrap must mint an admin whatever the host default says: %d %s", w.Code, w.Body.String())
	}

	// THE SSO HALF, through the same function ssoCallback hands to SsoSignIn. A provider whose
	// defaultRole is EMPTY inherits the host default, resolved at sign-in — never inside SsoSignIn,
	// which is one transaction on the single pooled connection.
	inherit, err := sso.ParseConfig(mustParseObject(t, `{"mode":"oauth2","provider":"github","clientId":"c"}`))
	if err != nil {
		t.Fatal(err)
	}
	if inherit.DefaultRole != "" {
		t.Fatalf("a provider that names no role must store none, got %q", inherit.DefaultRole)
	}
	if got := s.ssoSignInOpts(inherit, nil).DefaultRole; got != "developer" {
		t.Fatalf("an inheriting provider must take the host default, got %q", got)
	}
	// And it provisions at that role — the end of the chain, not just the opts.
	signed, err := s.auth.SsoSignIn("github", &sso.Identity{Subject: "s1", Username: "octo", Groups: []string{}}, s.ssoSignInOpts(inherit, nil))
	if err != nil || signed.User.Role != "developer" {
		t.Fatalf("an inheriting provider provisioned %+v %v", signed.User, err)
	}
	// A provider that NAMES a role keeps it, whatever the host default is — inherit is opt-in.
	named, err := sso.ParseConfig(mustParseObject(t, `{"mode":"oauth2","provider":"github","clientId":"c","defaultRole":"viewer"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ssoSignInOpts(named, nil).DefaultRole; got != "viewer" {
		t.Fatalf("an explicit provider role must survive the host default, got %q", got)
	}
	// On a host where nobody ever set a default, inherit is a VIEWER — never an admin by omission,
	// which is the whole reason this resolution is written down twice.
	bare := settingsFixture(t, 0)
	bare.auth = auth.New(bare.store)
	if got := bare.ssoSignInOpts(inherit, nil).DefaultRole; got != "viewer" {
		t.Fatalf("inherit on a bare host resolved to %q", got)
	}
	signed, err = bare.auth.SsoSignIn("github", &sso.Identity{Subject: "s1", Username: "octo", Groups: []string{}}, bare.ssoSignInOpts(inherit, nil))
	if err != nil || signed.User.Role != "viewer" {
		t.Fatalf("inherit on a bare host provisioned %+v %v", signed.User, err)
	}
	// The rest of the rules travel with it: assembling these anywhere but ssoSignInOpts is how half
	// an allow-list goes missing.
	rules, err := sso.ParseConfig(mustParseObject(t, `{"mode":"oauth2","provider":"github","clientId":"c","allowedEmailDomains":["example.com"],"allowedUsernames":["octo*"],"requiredGroups":["acme"],"scopes":"read:user user:email read:org"}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := bare.ssoSignInOpts(rules, context.Canceled)
	if len(opts.AllowedEmailDomains) != 1 || len(opts.AllowedUsernames) != 1 || len(opts.RequiredGroups) != 1 || opts.GroupsErr == nil {
		t.Fatalf("the allow-rules did not travel: %+v", opts)
	}
}

func mustParseObject(t *testing.T, s string) any {
	t.Helper()
	v, err := omap.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// sourceOf is one row's `source` out of the read, by key: the rows carry their own key, so this
// does not depend on the order they are emitted in.
func sourceOf(t *testing.T, body, key string) string {
	t.Helper()
	i := strings.Index(body, `"key": "`+key+`"`)
	if i < 0 {
		t.Fatalf("no %s row in %s", key, body)
	}
	rest := body[i:]
	j := strings.Index(rest, `"source": "`)
	if j < 0 {
		t.Fatalf("the %s row carries no source: %s", key, body)
	}
	rest = rest[j+len(`"source": "`):]
	return rest[:strings.Index(rest, `"`)]
}

// An INHERITING SSO provider never mints an administrator, whatever the host default says.
//
// The composition that made this necessary, measured against a real IdP before the clamp existed:
// `default_role: admin` (an admin's legitimate choice for POST /api/users) plus a provider saved
// straight from a preset — defaultRole omitted, allowedEmailDomains/allowedUsernames/requiredGroups
// all empty — meant ANY stranger who completed the OAuth flow became a full administrator. Two sane
// settings, one catastrophic product.
//
// Explicit still wins: a provider whose own form says admin gets admin, because that is somebody
// choosing in words rather than a default composing its way to the top.
//
// negative control: delete the `>= auth.Admin.Rank()` clamp in ssoSignInOpts — the first case
// returns "admin".
func TestAnInheritingSsoProviderNeverMintsAnAdmin(t *testing.T) {
	s := ssoTestServer(t)
	if err := s.settings.Set(settings.KeyDefaultRole, string(auth.Admin)); err != nil {
		t.Fatal(err)
	}
	// Inheriting: the provider names no role of its own.
	if got := s.ssoSignInOpts(&sso.Config{}, nil).DefaultRole; got == string(auth.Admin) {
		t.Fatalf("an inheriting provider resolved to admin — a stranger completing OAuth becomes one")
	} else if !auth.ValidRole(got) {
		t.Fatalf("the clamped role %q is not a role", got)
	}
	// Explicit admin on the provider itself is honoured: deliberate, on its own form.
	if got := s.ssoSignInOpts(&sso.Config{DefaultRole: string(auth.Admin)}, nil).DefaultRole; got != string(auth.Admin) {
		t.Fatalf("an explicit admin provider was clamped to %q — explicit must win", got)
	}
	// And a host default BELOW admin passes through untouched.
	if err := s.settings.Set(settings.KeyDefaultRole, string(auth.Developer)); err != nil {
		t.Fatal(err)
	}
	if got := s.ssoSignInOpts(&sso.Config{}, nil).DefaultRole; got != string(auth.Developer) {
		t.Fatalf("inherit = %q, want developer", got)
	}
}
