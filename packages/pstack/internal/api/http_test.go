package api

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/readiness"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
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
	return &Server{store: st, auth: auth.New(st), ssoClient: sso.NewClient(nil), ssoTTL: 60, opts: Options{Log: func(string) {}}}
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
