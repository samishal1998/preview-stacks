package api

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// fwServer is a real New() server on domain (may be empty), root token t0ken, docker a fake.
func fwServer(t *testing.T, domain string) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Domain: domain, Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	return s
}

// fwToken is a personal token on s for a new account at role.
func fwToken(t *testing.T, s *Server, name string, role auth.Role) string {
	t.Helper()
	u, err := s.auth.CreateUser(name, "correct-horse", auth.CreateOpts{Role: string(role)})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.auth.CreateToken(u.ID, "forward")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// fwCall is one root-authenticated request to /api/forward; the raw body comes back.
func fwCall(t *testing.T, s *Server, method, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/forward", strings.NewReader(body))
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	return rec.Code, rec.Body.String()
}

// fwEvents records every forward.* event s emits. Emit is synchronous, so no lock.
func fwEvents(t *testing.T, s *Server) *[]events.Event {
	got := &[]events.Event{}
	off := s.bus.On(func(e events.Event) {
		if strings.HasPrefix(e.Event, "forward.") {
			*got = append(*got, e)
		}
	})
	t.Cleanup(off)
	return got
}

func fwBody(t *testing.T, to, via, token string) string {
	t.Helper()
	m := map[string]string{"to": to, "token": token}
	if via != "" {
		m["via"] = via
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// negative control: drop the store.SetForward call in forwardPut → GET stays null; emit the row
// itself (with its token) as forward.changed → the event data check fails; add "token" to forwardView
// → the no-token check fails.
func TestForwardPutEstablishes(t *testing.T) {
	b := fwServer(t, "")
	bs := httptest.NewServer(http.HandlerFunc(b.handle))
	defer bs.Close()
	tok := fwToken(t, b, "dev", auth.Developer)

	a := fwServer(t, "")
	got := fwEvents(t, a)
	code, body := fwCall(t, a, "PUT", fwBody(t, bs.URL, "", tok))
	if code != 200 {
		t.Fatalf("PUT = %d: %s", code, body)
	}
	if strings.Contains(body, tok) || strings.Contains(body, `"token"`) {
		t.Fatalf("PUT answered the token: %s", body)
	}
	code, body = fwCall(t, a, "GET", "")
	var view struct {
		To            string  `json:"to"`
		Via           *string `json:"via"`
		EstablishedAt int64   `json:"establishedAt"`
		LastOkAt      int64   `json:"lastOkAt"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &view) != nil {
		t.Fatalf("GET = %d: %s", code, body)
	}
	if view.To != bs.URL || view.Via != nil || view.EstablishedAt <= 0 || view.LastOkAt != view.EstablishedAt {
		t.Fatalf("GET = %s", body)
	}
	if strings.Contains(body, tok) || strings.Contains(body, `"token"`) {
		t.Fatalf("GET answered the token: %s", body)
	}
	if row, err := store.GetForward(a.store.DB); err != nil || row == nil || row.Token != tok {
		t.Fatalf("stored row = %+v, %v", row, err)
	}
	if len(*got) != 1 || (*got)[0].Event != "forward.changed" || string((*got)[0].Data) != `{"to":"`+bs.URL+`","via":null}` {
		t.Fatalf("events = %+v", *got)
	}
}

// negative control: skip the root check in forwardCheck → the root-token case answers 200; compare
// ranks with > instead of >= → the developer case of TestForwardPutEstablishes fails and the viewer
// case still passes; move the own-URL check after the dial → the own-URL case names `reach`, not
// `own`; drop CheckRedirect → the redirect case answers 200.
func TestForwardPutRefuses(t *testing.T) {
	b := fwServer(t, "")
	bs := httptest.NewServer(http.HandlerFunc(b.handle))
	defer bs.Close()
	viewer := fwToken(t, b, "watcher", auth.Viewer)

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + closed.Addr().String()
	_ = closed.Close()

	// A pstack-shaped answer reached only by following a redirect.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/me" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"root":false,"user":{"role":"admin"}}`))
	}))
	defer redirect.Close()

	cases := []struct {
		name, body, want, token string
	}{
		{"unreachable", fwBody(t, closedURL, "", "pstack_pat_leak1"), "cannot reach", "pstack_pat_leak1"},
		{"B answers 401", fwBody(t, bs.URL, "", "pstack_pat_nope"), "answered 401", "pstack_pat_nope"},
		{"root token", fwBody(t, bs.URL, "", "t0ken"), "root token", "t0ken"},
		{"viewer token", fwBody(t, bs.URL, "", viewer), "is viewer", viewer},
		{"own URL", fwBody(t, "https://api.preview.example.com", "", "pstack_pat_leak2"), "own API", "pstack_pat_leak2"},
		{"redirect", fwBody(t, redirect.URL, "", "pstack_pat_leak3"), "answered 302", "pstack_pat_leak3"},
		{"no token", `{"to":"` + bs.URL + `"}`, "token", ""},
		{"not a URL", fwBody(t, "api.b.example.com", "", "pstack_pat_leak4"), "http(s) URL", "pstack_pat_leak4"},
		{"plain http to a public host", fwBody(t, "http://api.b.example.com", "", "pstack_pat_leak5"), "https://", "pstack_pat_leak5"},
		{"via not an IP", fwBody(t, bs.URL, "b.example.com", "pstack_pat_leak6"), "`via`", "pstack_pat_leak6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// negative control: the mutation named for this case above, or deleting its check.
			a := fwServer(t, "preview.example.com")
			got := fwEvents(t, a)
			code, body := fwCall(t, a, "PUT", c.body)
			var e struct{ Error string }
			if code != 400 || json.Unmarshal([]byte(body), &e) != nil || !strings.Contains(e.Error, c.want) {
				t.Fatalf("PUT = %d %s, want 400 naming %q", code, body, c.want)
			}
			if c.token != "" && strings.Contains(body, c.token) {
				t.Fatalf("the refusal carries the token: %s", body)
			}
			if code, body := fwCall(t, a, "GET", ""); code != 200 || body != "null" {
				t.Fatalf("GET after a refusal = %d %s", code, body)
			}
			if len(*got) != 0 {
				t.Fatalf("a refusal emitted %+v", *got)
			}
		})
	}
}

// negative control: dial the request URL's host instead of via → api.preview.example.com does not
// resolve to B and the PUT answers 400; set TLSClientConfig.ServerName to via's address → B's
// certificate does not cover 127.0.0.1 and the PUT answers 400.
func TestForwardViaWins(t *testing.T) {
	pemPath := filepath.Join("..", "..", "..", "conformance", "harness", "tls-wildcard")
	cert, err := tls.LoadX509KeyPair(pemPath+".crt.pem", pemPath+".key.pem")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(pemPath + ".crt.pem")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("no certificate in tls-wildcard.crt.pem")
	}

	b := fwServer(t, "preview.example.com")
	tok := fwToken(t, b, "dev", auth.Developer)
	var mu sync.Mutex
	gotHost := ""
	bs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHost = r.Host
		mu.Unlock()
		b.handle(w, r)
	}))
	bs.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	bs.StartTLS()
	defer bs.Close()

	a := fwServer(t, "preview.example.com")
	a.forwardTLS = &tls.Config{RootCAs: pool}
	via := bs.Listener.Addr().String()
	code, body := fwCall(t, a, "PUT", fwBody(t, "https://api.preview.example.com", via, tok))
	if code != 200 {
		t.Fatalf("PUT = %d: %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotHost != "api.preview.example.com" {
		t.Fatalf("B saw Host %q", gotHost)
	}
	if _, body := fwCall(t, a, "GET", ""); !strings.Contains(body, `"via": "`+via+`"`) {
		t.Fatalf("GET = %s", body)
	}
}

// negative control: drop store.DeleteForward → GET is still the row; emit nil → data is {} and the
// event check fails; emit on an unset row → the second DELETE adds an event.
func TestForwardDelete(t *testing.T) {
	b := fwServer(t, "")
	bs := httptest.NewServer(http.HandlerFunc(b.handle))
	defer bs.Close()
	tok := fwToken(t, b, "dev", auth.Developer)

	a := fwServer(t, "")
	if code, body := fwCall(t, a, "PUT", fwBody(t, bs.URL, "", tok)); code != 200 {
		t.Fatalf("PUT = %d: %s", code, body)
	}
	got := fwEvents(t, a)
	if code, body := fwCall(t, a, "DELETE", ""); code != 200 || strings.ReplaceAll(body, " ", "") != "{\n\"deleted\":true\n}" {
		t.Fatalf("DELETE = %d %s", code, body)
	}
	if code, body := fwCall(t, a, "GET", ""); code != 200 || body != "null" {
		t.Fatalf("GET after DELETE = %d %s", code, body)
	}
	if code, _ := fwCall(t, a, "DELETE", ""); code != 200 {
		t.Fatalf("DELETE when unset = %d", code)
	}
	if len(*got) != 1 || (*got)[0].Event != "forward.changed" || string((*got)[0].Data) != `{"to":null,"via":null}` {
		t.Fatalf("events = %+v", *got)
	}
}
