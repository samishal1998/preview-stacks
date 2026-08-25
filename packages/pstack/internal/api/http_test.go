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

func TestBoundedSeconds(t *testing.T) {
	// negative control: return `raw` unclamped — the 99999 case then yields a watch deadline that
	// outlives every job, and `up?timeout=99999` parks a watcher nothing will ever drive.
	cases := []struct {
		query string
		want  float64
	}{
		{"timeout=600", 600},                      // in range
		{"timeout=99999", ReadinessTimeoutMaxS},   // capped, never rejected
		{"timeout=0", ReadinessTimeoutDefaultS},   // 0 is "unset", not "no deadline"
		{"timeout=-5", ReadinessTimeoutDefaultS},  // negative likewise
		{"timeout=abc", ReadinessTimeoutDefaultS}, // NaN likewise
		{"", ReadinessTimeoutDefaultS},            // absent
		{"timeout=3600", ReadinessTimeoutMaxS},    // exactly max
	}
	for _, c := range cases {
		if got := boundedSeconds(c.query, "timeout", ReadinessTimeoutDefaultS, ReadinessTimeoutMaxS); got != c.want {
			t.Fatalf("boundedSeconds(%q) = %v, want %v", c.query, got, c.want)
		}
	}
	// The `wait` bound is a different ceiling on the same helper — a long poll must not outlast the
	// client's own read timeout.
	if got := boundedSeconds("wait=300", "wait", 0, 60); got != 60 {
		t.Fatalf("wait clamps to 60, got %v", got)
	}
}
