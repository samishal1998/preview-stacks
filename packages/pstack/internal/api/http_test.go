package api

import (
	"net/http/httptest"
	"testing"
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
