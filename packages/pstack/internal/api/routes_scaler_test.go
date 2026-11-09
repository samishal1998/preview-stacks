package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scaler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// scServer is a real New() server over dataDir, root token t0ken, docker a fake with no containers.
func scServer(t *testing.T, dataDir string) *Server {
	t.Helper()
	s, err := New(Options{DataDir: dataDir, Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	return s
}

// scCall is one request with bearer tok ("t0ken" is root; "session:<s>" is a session cookie); status
// and raw body come back.
func scCall(t *testing.T, s *Server, tok, method, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if session, ok := strings.CutPrefix(tok, "session:"); ok {
		req.Header.Set("cookie", "pstack_session="+session)
	} else {
		req.Header.Set("authorization", "Bearer "+tok)
	}
	s.handle(rec, req)
	return rec.Code, rec.Body.String()
}

// scGet is GET /api/scaler as tok, decoded.
func scGet(t *testing.T, s *Server, tok string) map[string]any {
	t.Helper()
	code, body := scCall(t, s, tok, "GET", "/api/scaler", "")
	if code != 200 {
		t.Fatalf("GET /api/scaler: %d %s", code, body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// scConfig is GET /api/scaler/config as root, decoded.
func scConfig(t *testing.T, s *Server) scaler.Config {
	t.Helper()
	code, body := scCall(t, s, "t0ken", "GET", "/api/scaler/config", "")
	if code != 200 {
		t.Fatalf("GET /api/scaler/config: %d %s", code, body)
	}
	var c scaler.Config
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func hetznerHash(t *testing.T) string {
	t.Helper()
	p, err := scaler.ParsePreset(scaler.Builtins()["hetzner"])
	if err != nil {
		t.Fatal(err)
	}
	return p.Hash
}

// A custom preset that passes the contract: a workers variable with user_data, one shared var.
const scCustomMain = `variable "workers" {
  type = map(object({
    user_data = string
    size      = string
  }))
}

variable "region" {
  type = string
}

resource "null_resource" "w" {
  for_each = var.workers
  triggers = { user_data = each.value.user_data }
}
`

func scFiles(t *testing.T, main string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"files": map[string]string{"main.tf": main}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// negative control: give scalerView a `credentials map[string]string` filled from the stored row —
// the maintainer's body then carries tok-123456789 and the second check fails.
func TestScalerGetHidesCredentials(t *testing.T) {
	s := scServer(t, t.TempDir())
	admin := fwToken(t, s, "ada", auth.Admin)
	maint := fwToken(t, s, "max", auth.Maintainer)
	if code, body := scCall(t, s, admin, "PUT", "/api/scaler", `{"preset":"hetzner","credentials":{"HCLOUD_TOKEN":"tok-123456789"}}`); code != 200 {
		t.Fatalf("PUT as admin: %d %s", code, body)
	}
	code, body := scCall(t, s, maint, "GET", "/api/scaler", "")
	if code != 200 || strings.Contains(body, "tok-123456789") {
		t.Fatalf("GET as maintainer: %d %s", code, body)
	}
	m := scGet(t, s, maint)
	if creds, _ := m["credentials"].(map[string]any); creds["HCLOUD_TOKEN"] != true || len(creds) != 1 {
		t.Fatalf("credentials = %v", m["credentials"])
	}
	// Absent fields are the defaults, and the lists are [] and {}, never null.
	set, _ := m["settings"].(map[string]any)
	if set["max"] != 3.0 || set["cooldownMin"] != 5.0 || set["graceMin"] != 15.0 {
		t.Fatalf("settings = %v", set)
	}
	if !strings.Contains(body, `"dismissed": []`) || !strings.Contains(body, `"vars": {}`) || !strings.Contains(body, `"report": null`) {
		t.Fatalf("body = %s", body)
	}
}

// negative control: make the null arm keep the stored value — the `A:null` check fails; make the
// mask arm store the mask — the second check reads "••••••••".
func TestScalerCredentialKeepRules(t *testing.T) {
	s := scServer(t, t.TempDir())
	put := func(creds string) {
		t.Helper()
		if code, body := scCall(t, s, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner","credentials":`+creds+`}`); code != 200 {
			t.Fatalf("PUT %s: %d %s", creds, code, body)
		}
	}
	put(`{"A":"old"}`)
	for _, creds := range []string{`{"A":""}`, `{"A":"••••••••"}`, `{}`} {
		put(creds)
		if got := scConfig(t, s).Credentials["A"]; got != "old" {
			t.Fatalf("after %s: A = %q, want old", creds, got)
		}
	}
	put(`{"A":"new"}`)
	if got := scConfig(t, s).Credentials["A"]; got != "new" {
		t.Fatalf("A = %q, want new", got)
	}
	put(`{"A":null}`)
	if _, ok := scConfig(t, s).Credentials["A"]; ok {
		t.Fatal("A survived null")
	}
	// No `credentials` at all keeps the map too.
	put(`{"B":"b"}`)
	if code, _ := scCall(t, s, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner"}`); code != 200 || scConfig(t, s).Credentials["B"] != "b" {
		t.Fatalf("B lost to a PUT without credentials: %d", code)
	}
}

// negative control: drop the `min > max` check — the `{min:2,max:1}` case answers 200.
func TestScalerPutValidates(t *testing.T) {
	s := scServer(t, t.TempDir())
	for _, body := range []string{
		`{}`,
		`not json`,
		`{"preset":"nope"}`,
		`{"preset":"hetzner","min":2,"max":1}`,
		`{"preset":"hetzner","min":-1}`,
		`{"preset":"hetzner","cooldownMin":-1}`,
		`{"preset":"hetzner","vars":{"region":"x"}}`,
		`{"preset":"hetzner","credentials":{"BAD KEY":"x"}}`,
		`{"preset":"hetzner","dismissed":["pstack-abc123"]}`,
	} {
		code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", body)
		if code != 400 || !strings.Contains(out, `"error"`) {
			t.Errorf("%s: %d %s, want 400", body, code, out)
		}
	}
	if _, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", `{}`); !strings.Contains(out, "preset required") {
		t.Errorf("{} answered %s", out)
	}
	// Per-worker and shared names both pass.
	if code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner","vars":{"server_type":"cx22","ssh_keys":"[\"k\"]"}}`); code != 200 {
		t.Errorf("valid vars: %d %s", code, out)
	}
	// Nothing invalid was stored along the way.
	if c := scConfig(t, s); c.Min != 0 || c.Max != 3 {
		t.Errorf("stored %+v", c.Settings)
	}
}

func scReport(t *testing.T, s *Server, body string) {
	t.Helper()
	if code, out := scCall(t, s, "t0ken", "POST", "/api/scaler/report", body); code != 200 {
		t.Fatalf("report: %d %s", code, out)
	}
}

// negative control: read the lock from memory only (skip the stored report) — the fresh server
// answers 200 to the ssh_keys change. Compare only the preset name — the ssh_keys PUT answers 200.
func TestScalerLockedConfig(t *testing.T) {
	dir := t.TempDir()
	s := scServer(t, dir)
	base := `{"preset":"hetzner","vars":{"ssh_keys":"[\"k\"]","server_type":"cx22"},"credentials":{"HCLOUD_TOKEN":"tok"}}`
	if code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", base); code != 200 {
		t.Fatalf("seed: %d %s", code, out)
	}
	locked := `{"hostId":"ab12cd34","workers":[{"name":"pstack-abc123","vars":{"server_type":"cx22"},"state":"booting","createdAt":1,"removing":false}],` +
		`"locked":{"preset":"hetzner","filesHash":"` + hetznerHash(t) + `","shared":{"ssh_keys":"[\"k\"]"}},"lastError":"boom"}`
	scReport(t, s, locked)

	cases := []struct {
		name, body string
		want       int
	}{
		{"shared var changed", `{"preset":"hetzner","vars":{"ssh_keys":"[\"other\"]"}}`, 409},
		{"preset changed", `{"preset":"aws"}`, 409},
		{"credential deleted", `{"preset":"hetzner","vars":{"ssh_keys":"[\"k\"]"},"credentials":{"HCLOUD_TOKEN":null}}`, 409},
		{"locked values restored, new min and per-worker var", `{"preset":"hetzner","min":1,"vars":{"ssh_keys":"[\"k\"]","server_type":"cx32"}}`, 200},
	}
	check := func(s *Server, label string) {
		t.Helper()
		for _, c := range cases {
			if code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", c.body); code != c.want {
				t.Errorf("%s, %s: %d %s, want %d", label, c.name, code, out, c.want)
			}
		}
	}
	check(s, "live")

	// The stored record is the persisted half only: no lastError, no state, no event.
	row, err := store.GetScalerConfig(s.store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Report, "boom") || strings.Contains(row.Report, "booting") || !strings.Contains(row.Report, "pstack-abc123") {
		t.Fatalf("stored report = %s", row.Report)
	}

	s.Stop()
	fresh := scServer(t, dir)
	check(fresh, "after a restart")
	// The card still shows the owned worker after a restart, from the stored half.
	if rep, _ := scGet(t, fresh, "t0ken")["report"].(map[string]any); rep["hostId"] != "ab12cd34" {
		t.Fatalf("report after a restart = %v", rep)
	}

	scReport(t, fresh, `{"hostId":"ab12cd34","workers":[],"locked":null}`)
	if code, out := scCall(t, fresh, "t0ken", "PUT", "/api/scaler", cases[0].body); code != 200 {
		t.Fatalf("after the lock cleared: %d %s", code, out)
	}

	// An upgrade changed the locked built-in's files: max 0 is still accepted.
	// negative control: restore the `|| preset.Hash != lock.FilesHash` clause → 409.
	scReport(t, fresh, `{"hostId":"ab12cd34","workers":[{"name":"pstack-abc123","vars":{},"createdAt":1,"removing":false}],`+
		`"locked":{"preset":"hetzner","filesHash":"stale","shared":{"ssh_keys":"[\"k\"]"}}}`)
	if code, out := scCall(t, fresh, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner","vars":{"ssh_keys":"[\"k\"]"},"max":0}`); code != 200 {
		t.Fatalf("stale files hash, max 0: %d %s", code, out)
	}
}

// negative control: drop the lock check in scalerPresetPut — the locked team-a PUT answers 200.
func TestScalerPresetRoutes(t *testing.T) {
	s := scServer(t, t.TempDir())
	maint := fwToken(t, s, "max", auth.Maintainer)
	if code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler/presets/team-a", scFiles(t, scCustomMain)); code != 200 || !strings.Contains(out, `"builtin": false`) {
		t.Fatalf("PUT team-a: %d %s", code, out)
	}
	code, out := scCall(t, s, maint, "GET", "/api/scaler/presets", "")
	var list []struct {
		Name    string        `json:"name"`
		Builtin bool          `json:"builtin"`
		Preset  scaler.Preset `json:"preset"`
		Locked  bool          `json:"locked"`
	}
	if err := json.Unmarshal([]byte(out), &list); code != 200 || err != nil {
		t.Fatalf("list: %d %s", code, out)
	}
	found := map[string]bool{}
	for _, p := range list {
		found[p.Name] = p.Builtin
	}
	if b, ok := found["team-a"]; !ok || b || !found["hetzner"] {
		t.Fatalf("list = %v", found)
	}
	if code, out := scCall(t, s, maint, "GET", "/api/scaler/presets/hetzner", ""); code != 200 || !strings.Contains(out, `"main.tf"`) {
		t.Fatalf("GET hetzner: %d", code)
	}
	if code, _ := scCall(t, s, maint, "GET", "/api/scaler/presets/nope", ""); code != 404 {
		t.Fatalf("GET nope: %d", code)
	}

	for _, c := range []struct{ name, path, body, want string }{
		{"a built-in", "/api/scaler/presets/hetzner", scFiles(t, scCustomMain), "built in"},
		{"a bad name", "/api/scaler/presets/My%20X", scFiles(t, scCustomMain), "preset name"},
		{"no workers variable", "/api/scaler/presets/team-b", scFiles(t, "variable \"region\" {}\n"), `no variable \"workers\"`},
	} {
		code, out := scCall(t, s, "t0ken", "PUT", c.path, c.body)
		if code != 400 || !strings.Contains(out, c.want) {
			t.Errorf("%s: %d %s, want 400 naming %q", c.name, code, out, c.want)
		}
	}
	if code, _ := scCall(t, s, "t0ken", "DELETE", "/api/scaler/presets/team-a", ""); code != 200 {
		t.Fatalf("DELETE team-a: %d", code)
	}
	if code, _ := scCall(t, s, "t0ken", "DELETE", "/api/scaler/presets/team-a", ""); code != 404 {
		t.Fatalf("DELETE team-a again: %d", code)
	}

	// Locked: a different body is 409, the same body (same hash) is 200, DELETE is 409.
	scCall(t, s, "t0ken", "PUT", "/api/scaler/presets/team-a", scFiles(t, scCustomMain))
	p, _ := scaler.ParsePreset(map[string]string{"main.tf": scCustomMain})
	scReport(t, s, `{"hostId":"ab12cd34","workers":[{"name":"pstack-abc123","vars":{},"createdAt":1}],"locked":{"preset":"team-a","filesHash":"`+p.Hash+`","shared":{}}}`)
	if code, _ := scCall(t, s, "t0ken", "PUT", "/api/scaler/presets/team-a", scFiles(t, scCustomMain+"# edit\n")); code != 409 {
		t.Errorf("locked PUT: %d, want 409", code)
	}
	if code, _ := scCall(t, s, "t0ken", "PUT", "/api/scaler/presets/team-a", scFiles(t, scCustomMain)); code != 200 {
		t.Errorf("locked PUT, same files: %d, want 200", code)
	}
	if code, _ := scCall(t, s, "t0ken", "DELETE", "/api/scaler/presets/team-a", ""); code != 409 {
		t.Errorf("locked DELETE: %d, want 409", code)
	}
	if code, out := scCall(t, s, maint, "GET", "/api/scaler/presets/team-a", ""); code != 200 || !strings.Contains(out, `"locked": true`) {
		t.Errorf("locked GET: %d %s", code, out)
	}
}

// negative control: in scalerConfigGet, serve only custom presets' files — the built-in case gets {}.
func TestScalerConfigIsRootOnly(t *testing.T) {
	s := scServer(t, t.TempDir())
	_, session, _ := grafanaAccount(t, s, "ada", auth.Admin)
	pat := fwToken(t, s, "bob", auth.Admin)
	for name, tok := range map[string]string{"admin session": "session:" + session, "admin PAT": pat} {
		if code, _ := scCall(t, s, tok, "GET", "/api/scaler/config", ""); code != 403 {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	scCall(t, s, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner","credentials":{"HCLOUD_TOKEN":"tok-123456789"}}`)
	c := scConfig(t, s)
	if c.Credentials["HCLOUD_TOKEN"] != "tok-123456789" || c.Files["main.tf"] != scaler.Builtins()["hetzner"]["main.tf"] || c.Dismissed == nil {
		t.Fatalf("config = %+v", c)
	}
}

// W-R14: the handlers' own gate, without the table in front of it.
//
// negative control: delete `if !mayMoveConfig(who)` from scalerConfigGet — the admin GET reaches
// the store on a zero Server and panics instead of answering 403.
func TestScalerRootRoutesRefuseAtTheHandler(t *testing.T) {
	who := &auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{Username: "alice", Role: "admin"}}
	s := &Server{}
	for name, h := range map[string]func(*httptest.ResponseRecorder) error{
		"config": func(w *httptest.ResponseRecorder) error { return s.scalerConfigGet(w, who) },
		"report": func(w *httptest.ResponseRecorder) error {
			return s.scalerReportPost(w, httptest.NewRequest("POST", "/api/scaler/report", strings.NewReader(`{}`)), who)
		},
	} {
		w := httptest.NewRecorder()
		if err := h(w); err != nil || w.Code != 403 {
			t.Errorf("%s: %d %v, want 403", name, w.Code, err)
		}
	}
}

// negative control: decode Event.Data into map[string]any and re-marshal it — the payload keys
// come back sorted ("name" before "settings" still, but "action","attempt","error" for failed).
func TestScalerReport(t *testing.T) {
	s := scServer(t, t.TempDir())
	var got []events.Event
	off := s.bus.On(func(e events.Event) {
		if strings.HasPrefix(e.Event, "scaler.") {
			got = append(got, e)
		}
	})
	defer off()

	scReport(t, s, `{"hostId":"ab12cd34","workers":[],"locked":null,"event":{"name":"scaler.created","data":{"name":"pstack-abc123","settings":{"server_type":"cx22"}}}}`)
	scReport(t, s, `{"hostId":"ab12cd34","workers":[],"locked":null,"event":{"name":"scaler.failed","data":{"action":"max","error":"","attempt":1}}}`)
	if len(got) != 2 || got[0].Event != "scaler.created" || string(got[0].Data) != `{"name":"pstack-abc123","settings":{"server_type":"cx22"}}` ||
		string(got[1].Data) != `{"action":"max","error":"","attempt":1}` {
		t.Fatalf("events = %+v", got)
	}

	if code, _ := scCall(t, s, "t0ken", "POST", "/api/scaler/report", `{"hostId":"zz","workers":[],"event":{"name":"job.failed","data":{}}}`); code != 400 {
		t.Fatalf("a foreign event: %d, want 400", code)
	}
	if len(got) != 2 {
		t.Fatalf("a refused report emitted: %+v", got)
	}
	row, err := store.GetScalerConfig(s.store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Report, "event") || strings.Contains(row.Report, `"zz"`) || !strings.Contains(row.Report, "ab12cd34") {
		t.Fatalf("stored report = %s", row.Report)
	}

	admin := fwToken(t, s, "ada", auth.Admin)
	if code, _ := scCall(t, s, admin, "POST", "/api/scaler/report", `{"workers":[]}`); code != 403 {
		t.Fatalf("admin POST: %d, want 403", code)
	}
}

// W-R18: a dismissed orphan must be one the report lists, and drops out once it is no longer listed.
//
// negative control: drop the intersection in scalerReportPost — dismissed still holds the name
// after the report without it.
func TestScalerDismissed(t *testing.T) {
	s := scServer(t, t.TempDir())
	scReport(t, s, `{"hostId":"ab12cd34","workers":[],"orphans":["pstack-abc123"]}`)
	if code, out := scCall(t, s, "t0ken", "PUT", "/api/scaler", `{"preset":"hetzner","dismissed":["pstack-abc123"]}`); code != 200 {
		t.Fatalf("dismiss: %d %s", code, out)
	}
	if d := scConfig(t, s).Dismissed; len(d) != 1 || d[0] != "pstack-abc123" {
		t.Fatalf("dismissed = %v", d)
	}
	scReport(t, s, `{"hostId":"ab12cd34","workers":[]}`)
	if d := scConfig(t, s).Dismissed; d == nil || len(d) != 0 {
		t.Fatalf("dismissed after the report = %v, want []", d)
	}
}

// negative control: make controlServiceOn return a pointer to false when !Reachable — the silent
// case reads false.
func TestScalerEnabled(t *testing.T) {
	const inspectScaler = `[{"Id":"c1","Name":"/pstack-control-scaler-1","Config":{"Image":"pstack","Labels":{"com.docker.compose.service":"scaler"}},"State":{"Status":"running","StartedAt":"2026-09-15T10:00:00Z"}}]`
	for _, c := range []struct {
		name string
		ps   exec.Result
		want any
	}{
		{"absent", exec.Result{OK: true}, false},
		{"present", exec.Result{OK: true, Stdout: "c1\n"}, true},
		{"docker silent", exec.Result{OK: false, Code: 1}, nil},
	} {
		s := scServer(t, t.TempDir())
		f := exec.NewFake(nil, "")
		ps := c.ps
		f.Answer = func(cmd string) (exec.Result, bool) {
			switch {
			case strings.HasPrefix(cmd, "docker ps -aq"):
				return ps, true
			case strings.HasPrefix(cmd, "docker inspect"):
				return exec.Result{OK: true, Stdout: inspectScaler}, true
			}
			return exec.Result{OK: true}, true
		}
		s.host = f
		if v, ok := scGet(t, s, "t0ken")["enabled"]; !ok || v != c.want {
			t.Errorf("%s: enabled = %v (present %v), want %v", c.name, v, ok, c.want)
		}
	}
}
