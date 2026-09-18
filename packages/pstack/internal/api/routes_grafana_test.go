package api

import (
	"net/http/httptest"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// grafanaServer is a real New() server with a domain and a root token. Nothing listens. Not
// ssoTestServer: handle() dereferences routing and sleepIndex, and that one leaves both nil. Never
// Start it: Start runs the docker probe, so tests set s.grafana directly.
func grafanaServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Domain: "preview.example.com", Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// healthBody is GET /api/health through handle(), decoded as the ordered map a client sees.
func healthBody(t *testing.T, s *Server) *omap.Map {
	t.Helper()
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest("GET", "/api/health", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return mustParseObject(t, w.Body.String()).(*omap.Map)
}

// The UI finds Grafana through /api/health. When off the key is ABSENT: Has() is the check, because
// a null is still a present key (Go rule 2). Every host without Grafana answers byte-identically.
func TestHealthNamesGrafanaOnlyWhenOn(t *testing.T) {
	// negative control: `if s.grafanaOn() {` → `if true {` in health — the off, no-token and
	// no-domain subtests fail.
	t.Run("off: no grafana key", func(t *testing.T) {
		// negative control: `if s.grafanaOn() {` → `if true {` in health — Has("grafana") is true.
		s := grafanaServer(t)
		if b := healthBody(t, s); b.Has("grafana") {
			v, _ := b.Get("grafana")
			t.Fatalf("grafana is off, got grafana=%v", v)
		}
	})
	t.Run("on: the configured domain's URL, last", func(t *testing.T) {
		// negative control: delete the `body = append(body, jsonx.KV{K: "grafana", …})` line — got "";
		// and, run separately, prepend it (`body = append(jsonx.Object{{K: "grafana", V: s.grafanaURL()}}, body...)`) — the last key is "version".
		s := grafanaServer(t)
		s.grafana.Store(true)
		b := healthBody(t, s)
		if got := b.GetString("grafana"); got != "https://grafana.preview.example.com" {
			t.Fatalf("grafana = %q, want https://grafana.preview.example.com", got)
		}
		if keys := b.Keys(); keys[len(keys)-1] != "grafana" {
			t.Fatalf("grafana goes after version, got keys %v", keys)
		}
	})
	t.Run("no root token: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Token != "" &&` from grafanaOn — the key is present.
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Token = ""
		if healthBody(t, s).Has("grafana") {
			t.Fatal("no PSTACK_TOKEN means no sign-in, so no grafana key")
		}
	})
	t.Run("no domain: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Domain != "" &&` from grafanaOn — the key is "https://grafana.".
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Domain = ""
		if b := healthBody(t, s); b.Has("grafana") {
			t.Fatalf("no PSTACK_DOMAIN, got grafana=%q", b.GetString("grafana"))
		}
	})
}
