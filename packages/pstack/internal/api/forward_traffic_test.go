package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// The preview domain tls-wildcard.crt.pem covers (*.preview.example.com), and a hostname on it.
const (
	tfDomain = "preview.example.com"
	tfHost   = "web-pr-1." + tfDomain
)

// tfB is B: a TLS server with the wildcard certificate whose handler h runs behind a request
// counter. The returned pool trusts that certificate.
func tfB(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32, *x509.CertPool) {
	t.Helper()
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
	n := &atomic.Int32{}
	bs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	bs.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	bs.StartTLS()
	t.Cleanup(bs.Close)
	return bs, n, pool
}

// tfEcho answers with the Host and the forwarded-by header it received.
func tfEcho(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "B "+r.Host+" "+r.Header.Get(forwardedBy))
}

// tfA is A on tfDomain, docker a fake that answers ok, trusting pool; its log lines are returned.
func tfA(t *testing.T, pool *x509.CertPool) (*Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Domain: tfDomain, Bus: events.New(),
		Log: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
		Env: exec.Merge(exec.Environ(), map[string]string{"PATH": fakeDockerPath(t)})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	s.forwardTLS = &tls.Config{RootCAs: pool}
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

// tfForward turns forwarding on, dialling via (traffic would dial 443 without a port).
func tfForward(t *testing.T, a *Server, via string) {
	t.Helper()
	if err := store.SetForward(a.store.DB, store.Forward{To: "https://api." + tfDomain, Via: via, Token: "b-token", EstablishedAt: 1, LastOkAt: 1}); err != nil {
		t.Fatal(err)
	}
}

// tfGet is a visitor's GET / for host on a, with extra headers h.
func tfGet(a *Server, host string, h map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = host
	for k, v := range h {
		req.Header.Set(k, v)
	}
	a.handle(rec, req)
	return rec
}

// tfDown fails unless rec is today's Down page for host, byte for byte.
func tfDown(t *testing.T, rec *httptest.ResponseRecorder, host string) {
	t.Helper()
	if rec.Code != 503 || rec.Header().Get("retry-after") != "5" || rec.Header().Get("cache-control") != "no-store" ||
		rec.Header().Get("content-type") != "text/html; charset=utf-8" || rec.Body.String() != scheduler.WakePage(host, "", scheduler.Down, "") {
		t.Fatalf("not the Down page: %d %v\n%s", rec.Code, rec.Header(), rec.Body)
	}
}

// negative control: drop the forward block from handle() → A answers its Down page and B sees
// nothing; set Out.Host to via → B sees Host 127.0.0.1:<port>.
func TestTrafficReachesB(t *testing.T) {
	bs, hits, pool := tfB(t, tfEcho)
	a, _ := tfA(t, pool)
	tfForward(t, a, bs.Listener.Addr().String())

	rec := tfGet(a, tfHost, nil)
	if rec.Code != 200 || rec.Body.String() != "B "+tfHost+" "+tfDomain || hits.Load() != 1 {
		t.Fatalf("%d %.200q, B saw %d", rec.Code, rec.Body, hits.Load())
	}
	if f, _ := store.GetForward(a.store.DB); f.LastOkAt == 1 {
		t.Fatal("lastOkAt not touched")
	}
}

// negative control: move the forward block below the wake block → A answers its waking page and
// starts a wake job, and B sees nothing.
func TestTrafficForwardsInsteadOfWaking(t *testing.T) {
	bs, hits, pool := tfB(t, tfEcho)
	a, _ := tfA(t, pool)
	crDeploy(t, a, "pr-1", "myapp-pr-1", crCompose)
	if err := a.registry.SetSleep("pr-1", &registry.SleepRecord{Since: 1, Reason: "test", Hosts: []string{tfHost}, Rules: []string{}}); err != nil {
		t.Fatal(err)
	}
	a.reindex()
	if a.sleepIndex.Find(tfHost) != "pr-1" {
		t.Fatal("not asleep on A")
	}
	tfForward(t, a, bs.Listener.Addr().String())

	if rec := tfGet(a, tfHost, nil); rec.Code != 200 || hits.Load() != 1 {
		t.Fatalf("%d %.200q, B saw %d", rec.Code, rec.Body, hits.Load())
	}
	if i := slices.IndexFunc(a.jobs.List(), func(j jobs.Job) bool { return j.Action == jobs.Wake }); i >= 0 {
		t.Fatalf("A started a wake: %+v", a.jobs.List()[i])
	}
}

// negative control: drop previewHostname from the forward check → control.<d> reaches B; guard with
// Header.Get(...) == "" → an empty X-Pstack-Forwarded-By reaches B and is not the Down page.
func TestTrafficSkips(t *testing.T) {
	bs, hits, pool := tfB(t, tfEcho)
	a, _ := tfA(t, pool)
	via := bs.Listener.Addr().String()

	for _, tc := range []struct {
		name, host string
		code       int
	}{
		{"control", "control." + tfDomain, 200},
		{"api", "api." + tfDomain, 200},
		{"grafana", "grafana." + tfDomain, 503},
		{"loki", "loki." + tfDomain, 404},
		// Not a skip any more: the wake catch-all routes any depth here, so a.b.<d> is a preview name
		// and is forwarded. This B's certificate covers one label, so the TLS handshake fails before
		// B sees a request and A answers the Down page — never the console (200).
		// negative control: keep previewHostname one label deep → 200, the console on a preview's URL.
		{"two labels is forwarded, not the console", "a.b." + tfDomain, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tfForward(t, a, via)
			if rec := tfGet(a, tc.host, nil); rec.Code != tc.code || hits.Load() != 0 {
				t.Fatalf("%s = %d, B saw %d", tc.host, rec.Code, hits.Load())
			}
		})
	}
	t.Run("already forwarded", func(t *testing.T) {
		tfForward(t, a, via)
		tfDown(t, tfGet(a, tfHost, map[string]string{forwardedBy: ""}), tfHost)
		tfDown(t, tfGet(a, tfHost, map[string]string{forwardedBy: "other.example.com"}), tfHost)
	})
	t.Run("forwarding off", func(t *testing.T) {
		if err := store.DeleteForward(a.store.DB); err != nil {
			t.Fatal(err)
		}
		tfDown(t, tfGet(a, tfHost, nil), tfHost)
	})
	if hits.Load() != 0 {
		t.Fatalf("B saw %d requests", hits.Load())
	}
}

// tfDials counts forwardDial calls until the test ends.
func tfDials(t *testing.T) *atomic.Int32 {
	n := &atomic.Int32{}
	old := forwardDial
	forwardDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		n.Add(1)
		return old(ctx, network, addr)
	}
	t.Cleanup(func() { forwardDial = old })
	return n
}

// negative control: forwardTraffic passes retry=true → the closed port is dialled 3 times; take
// ServerName from via → httptest's certificate covers 127.0.0.1 and B answers; write a 502 in the
// ErrorHandler → not the Down page.
func TestTrafficBadCertIsTheDownPage(t *testing.T) {
	defer fwShortWaits(t)()

	t.Run("a certificate not covering the hostname", func(t *testing.T) {
		hits := &atomic.Int32{}
		// httptest's own certificate: example.com and 127.0.0.1, not tfHost.
		bs := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			tfEcho(w, r)
		}))
		defer bs.Close()
		pool := x509.NewCertPool()
		pool.AddCert(bs.Certificate())
		a, logs := tfA(t, pool)
		tfForward(t, a, bs.Listener.Addr().String())
		dials := tfDials(t)

		tfDown(t, tfGet(a, tfHost, nil), tfHost)
		if dials.Load() != 1 || hits.Load() != 0 {
			t.Fatalf("dialled %d, B handled %d", dials.Load(), hits.Load())
		}
		if l := logs(); len(l) != 1 || !strings.HasPrefix(l[0], "forward: "+tfHost+": ") {
			t.Fatalf("log = %q", l)
		}
	})

	t.Run("a closed port", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_ = ln.Close()
		a, _ := tfA(t, x509.NewCertPool())
		tfForward(t, a, ln.Addr().String())
		dials := tfDials(t)

		tfDown(t, tfGet(a, tfHost, nil), tfHost)
		if dials.Load() != 1 {
			t.Fatalf("dialled %d times", dials.Load())
		}
	})
}

// negative control: pr.Out.Header.Del("Upgrade") in Rewrite → B answers 400 and no 101 comes back.
func TestTrafficWebSocketUpgrade(t *testing.T) {
	bs, _, pool := tfB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" || r.Host != tfHost {
			http.Error(w, "no upgrade", 400)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n') // echo one line through the tunnel
		_, _ = rw.WriteString(line)
		_ = rw.Flush()
	})
	a, _ := tfA(t, pool)
	tfForward(t, a, bs.Listener.Addr().String())
	as := httptest.NewServer(http.HandlerFunc(a.handle))
	defer as.Close()

	c, err := net.Dial("tcp", as.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: "+tfHost+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("handshake = %v %v", resp, err)
	}
	_, _ = io.WriteString(c, "ping\n")
	if line, _ := br.ReadString('\n'); line != "ping\n" {
		t.Fatalf("echo = %q", line)
	}
}
