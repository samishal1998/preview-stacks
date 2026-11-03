package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// fwDockerServer is fwServer whose jobs run a docker that answers ok, so an up reaches `ok`.
func fwDockerServer(t *testing.T, domain string) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Domain: domain, Bus: events.New(),
		Log: func(string) {}, Env: exec.Merge(exec.Environ(), map[string]string{"PATH": fakeDockerPath(t)})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	return s
}

// fwReq is one request to s with bearer tok (none when ""), extra headers h; the recorder comes back.
func fwReq(t *testing.T, s *Server, tok, method, path, body string, h map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("authorization", "Bearer "+tok)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	s.handle(rec, req)
	return rec
}

// fwCounting serves h behind a counter of the requests it received.
func fwCounting(h http.Handler) (*httptest.Server, *atomic.Int32) {
	n := &atomic.Int32{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h.ServeHTTP(w, r)
	})), n
}

// fwSet points a at to with tok through the real PUT.
func fwSet(t *testing.T, a *Server, to, tok string) {
	t.Helper()
	if code, body := fwCall(t, a, "PUT", fwBody(t, to, "", tok)); code != 200 {
		t.Fatalf("PUT /api/forward = %d: %s", code, body)
	}
}

// fwSeed stores a row directly, skipping PUT's check against B.
func fwSeed(t *testing.T, a *Server, to, tok string) {
	t.Helper()
	if err := store.SetForward(a.store.DB, store.Forward{To: to, Token: tok, EstablishedAt: 1, LastOkAt: 1}); err != nil {
		t.Fatal(err)
	}
}

// fwDeployBody is PUT /api/deployments/pr-1's body for stack myapp-pr-1.
func fwDeployBody(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"spec": crSpec("myapp-pr-1"), "compose": crCompose})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// negative control: drop the forwardAPI call from routes → the PUT and up run on A, A's registry
// has pr-1 and B's does not.
func TestForwardedUpRunsOnB(t *testing.T) {
	b := fwDockerServer(t, "b.example.com")
	bs := httptest.NewServer(http.HandlerFunc(b.handle))
	defer bs.Close()
	bTok := fwToken(t, b, "deployer", auth.Developer)

	a := fwDockerServer(t, "a.example.com")
	aTok := fwToken(t, a, "ci", auth.Developer)
	fwSet(t, a, bs.URL, bTok)

	if rec := fwReq(t, a, aTok, "PUT", "/api/deployments/pr-1", fwDeployBody(t), nil); rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("PUT deployment = %d: %s", rec.Code, rec.Body)
	}
	rec := fwReq(t, a, aTok, "POST", "/api/deployments/pr-1/up", "", nil)
	var up struct {
		Job struct{ ID string } `json:"job"`
	}
	if rec.Code != 202 || json.Unmarshal(rec.Body.Bytes(), &up) != nil || up.Job.ID == "" {
		t.Fatalf("up = %d: %s", rec.Code, rec.Body)
	}
	if dep, err := b.registry.Get("pr-1"); err != nil || dep == nil {
		t.Fatalf("B's registry: %v, %v", dep, err)
	}
	if dep, err := a.registry.Get("pr-1"); err != nil || dep != nil {
		t.Fatalf("A's registry has %v, %v", dep, err)
	}
	if _, ok := a.jobs.Get(up.Job.ID); ok {
		t.Fatal("the job is on A")
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		rec := fwReq(t, a, aTok, "GET", "/api/jobs/"+up.Job.ID, "", nil)
		var got struct {
			Job struct{ ID, State string } `json:"job"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Job.ID != up.Job.ID {
			t.Fatalf("GET job on A = %d: %s", rec.Code, rec.Body)
		}
		if got.Job.State == string(jobs.OK) {
			break
		}
		if s := jobs.State(got.Job.State); s.Terminal() || time.Now().After(deadline) {
			t.Fatalf("B's job is %s", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.readiness.Cancel("myapp-pr-1")
}

// negative control: call forwardAPI before permit in routes → the viewer's up reaches B, which
// answers it (B's token is a developer's) and B counts a request.
func TestForwardChecksAsTierFirst(t *testing.T) {
	bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 202, map[string]any{"job": map[string]string{"id": "x"}})
	}))
	defer bs.Close()
	a := fwServer(t, "")
	fwSeed(t, a, bs.URL, "b-token")
	viewer := fwToken(t, a, "watcher", auth.Viewer)

	if rec := fwReq(t, a, viewer, "POST", "/api/deployments/pr-1/up", "", nil); rec.Code != 403 {
		t.Fatalf("viewer up = %d: %s", rec.Code, rec.Body)
	}
	if hits.Load() != 0 {
		t.Fatalf("B saw %d request(s)", hits.Load())
	}
}

// negative control: drop the KindShare skip in forwardAPI → the share's GET goes to B and B counts it.
func TestForwardNeverForwardsShares(t *testing.T) {
	bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) }))
	defer bs.Close()
	a, _ := crServer(t)
	rec := fwReq(t, a, "t0ken", "POST", "/api/deployments/pr-1/share", `{}`, nil)
	var share struct{ Token string }
	if rec.Code != 200 && rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &share) != nil || share.Token == "" {
		t.Fatalf("mint share = %d: %s", rec.Code, rec.Body)
	}
	fwSeed(t, a, bs.URL, "b-token")

	rec = fwReq(t, a, "", "GET", "/api/deployments/pr-1?token="+share.Token, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "myapp-pr-1") {
		t.Fatalf("share GET = %d: %s", rec.Code, rec.Body)
	}
	if hits.Load() != 0 {
		t.Fatalf("B saw %d request(s)", hits.Load())
	}
}

// negative control: drop the X-Pstack-Forwarded-By skip in forwardAPI → the request goes to B.
func TestForwardedHeaderIsServedLocally(t *testing.T) {
	bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) }))
	defer bs.Close()
	a, _ := crServer(t)
	fwSeed(t, a, bs.URL, "b-token")

	// An empty value still counts: a host with no domain sends one.
	rec := fwReq(t, a, "t0ken", "GET", "/api/deployments/pr-1", "", map[string]string{"X-Pstack-Forwarded-By": ""})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "myapp-pr-1") {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	if hits.Load() != 0 {
		t.Fatalf("B saw %d request(s)", hits.Load())
	}
}

// negative control: an A-local job id still forwarded (drop the s.jobs.Get carve-out) → B answers
// 404 for it.
func TestForwardServesALocalJobLocally(t *testing.T) {
	bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "no such job") }))
	defer bs.Close()
	a, _ := crServer(t)
	dep, err := a.registry.Get("pr-1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.resolveDep("pr-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := a.startLifecycle("pr-1", dep, st, jobs.Up, lifecycleOptions{By: "test"})
	if !ok {
		t.Fatal("up refused")
	}
	waitLokiJob(t, a, job.ID)
	a.readiness.Cancel(st.Stack)
	fwSeed(t, a, bs.URL, "b-token")

	if rec := fwReq(t, a, "t0ken", "GET", "/api/jobs/"+job.ID, "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), job.ID) {
		t.Fatalf("GET local job = %d: %s", rec.Code, rec.Body)
	}
	if hits.Load() != 0 {
		t.Fatalf("B saw %d request(s)", hits.Load())
	}
	// A job A does not hold, and the list, go to B.
	if rec := fwReq(t, a, "t0ken", "GET", "/api/jobs/up-other-1-abc", "", nil); rec.Code != 404 || hits.Load() != 1 {
		t.Fatalf("GET B's job = %d, B saw %d", rec.Code, hits.Load())
	}
	fwReq(t, a, "t0ken", "GET", "/api/jobs", "", nil)
	if hits.Load() != 2 {
		t.Fatalf("GET /api/jobs: B saw %d", hits.Load())
	}
}

// negative control: include the outbound Authorization in the ErrorHandler message → the 502 body and
// forward.failed carry the token; drop the Cookie delete → B sees the cookie; keep the caller's
// Authorization → B sees A's token.
func TestForwardSwapsCredentials(t *testing.T) {
	const bTok = "pstack_pat_b-secret"
	var mu sync.Mutex
	var gotAuth, gotCookie []string
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		gotCookie = append(gotCookie, r.Header.Get("Cookie"))
		mu.Unlock()
		if r.URL.Path == "/api/auth/me" {
			writeJSON(w, 200, map[string]any{"root": false, "user": map[string]string{"role": "developer"}})
			return
		}
		writeJSON(w, 200, map[string]any{"deployments": []any{}})
	}))
	a := fwServer(t, "")
	got := fwEvents(t, a)
	code, body := fwCall(t, a, "PUT", fwBody(t, bs.URL, "", bTok))
	if code != 200 || strings.Contains(body, bTok) {
		t.Fatalf("PUT = %d: %s", code, body)
	}

	rec := fwReq(t, a, "t0ken", "GET", "/api/deployments", "", map[string]string{"Cookie": "pstack_session=a-session"})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), bTok) {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(gotAuth) != 2 || gotAuth[1] != "Bearer "+bTok || gotCookie[1] != "" {
		t.Fatalf("B saw authorization %q, cookie %q", gotAuth, gotCookie)
	}
	mu.Unlock()

	// B gone: the 502 and its event must not carry the token either.
	bs.Close()
	defer fwShortWaits(t)()
	rec = fwReq(t, a, "t0ken", "GET", "/api/deployments", "", nil)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), bTok) {
		t.Fatalf("GET with B gone = %d: %s", rec.Code, rec.Body)
	}
	if code, body := fwCall(t, a, "DELETE", ""); code != 200 || strings.Contains(body, bTok) {
		t.Fatalf("DELETE = %d: %s", code, body)
	}
	if len(*got) != 3 {
		t.Fatalf("events = %+v", *got)
	}
	for _, e := range *got {
		if strings.Contains(string(e.Data), bTok) {
			t.Fatalf("%s carries the token: %s", e.Event, e.Data)
		}
	}
}

// fwShortWaits shortens the retry waits; the returned func restores them.
func fwShortWaits(t *testing.T) func() {
	old := forwardWaits
	forwardWaits = []time.Duration{time.Millisecond, time.Millisecond}
	return func() { forwardWaits = old }
}

// negative control: retry every error, not only the dial (retry around RoundTrip) → the hanging B
// sees 3 requests; retry nothing → the closed port is dialled once.
func TestForwardRetriesOnlyDials(t *testing.T) {
	defer fwShortWaits(t)()

	t.Run("a closed port is dialled 3 times", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		to := "http://" + ln.Addr().String()
		_ = ln.Close()
		var dials atomic.Int32
		old := forwardDial
		forwardDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return old(ctx, network, addr)
		}
		defer func() { forwardDial = old }()

		a, _ := crServer(t)
		got := fwEvents(t, a)
		fwSeed(t, a, to, "b-token")
		rec := fwReq(t, a, "t0ken", "GET", "/api/deployments", "", nil)
		want := "forward: " + ln.Addr().String() + " unreachable after 3 attempts"
		if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"`+want+`"`) {
			t.Fatalf("GET = %d: %s, want 502 %q", rec.Code, rec.Body, want)
		}
		if dials.Load() != 3 {
			t.Fatalf("dialled %d times", dials.Load())
		}
		var data struct {
			To       string
			Error    string
			Attempts int
		}
		if len(*got) != 1 || (*got)[0].Event != "forward.failed" || json.Unmarshal((*got)[0].Data, &data) != nil ||
			data.To != to || data.Attempts != 3 || data.Error == "" {
			t.Fatalf("events = %+v", *got)
		}
	})

	t.Run("a B that accepts and never answers is tried once", func(t *testing.T) {
		old := forwardHeaderTimeout
		forwardHeaderTimeout = 200 * time.Millisecond
		defer func() { forwardHeaderTimeout = old }()
		release := make(chan struct{})
		bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer bs.Close()
		defer close(release)

		a, _ := crServer(t)
		got := fwEvents(t, a)
		fwSeed(t, a, bs.URL, "b-token")
		rec := fwReq(t, a, "t0ken", "POST", "/api/deployments/pr-1/up", "", nil)
		host := strings.TrimPrefix(bs.URL, "http://")
		if rec.Code != 502 || !strings.Contains(rec.Body.String(), "forward: "+host+": ") || strings.Contains(rec.Body.String(), "unreachable") {
			t.Fatalf("up = %d: %s", rec.Code, rec.Body)
		}
		if hits.Load() != 1 {
			t.Fatalf("B saw %d requests", hits.Load())
		}
		if len(*got) != 1 || (*got)[0].Event != "forward.failed" || !strings.Contains(string((*got)[0].Data), `"attempts":1`) {
			t.Fatalf("events = %+v", *got)
		}
	})
}

// negative control: retry or rewrite non-2xx answers in ModifyResponse → the status or body differs,
// or B counts more than one request, or forward.failed is emitted.
func TestForwardPassesBAnswersThrough(t *testing.T) {
	for _, status := range []int{409, 401} {
		bs, hits := fwCounting(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"B says no","state":"running"}`))
		}))
		a, _ := crServer(t)
		got := fwEvents(t, a)
		fwSeed(t, a, bs.URL, "b-token")
		rec := fwReq(t, a, "t0ken", "POST", "/api/deployments/pr-1/up", "", nil)
		bs.Close()
		if rec.Code != status || rec.Body.String() != `{"error":"B says no","state":"running"}` {
			t.Fatalf("%d: A answered %d %s", status, rec.Code, rec.Body)
		}
		if hits.Load() != 1 || len(*got) != 0 {
			t.Fatalf("%d: B saw %d, events %+v", status, hits.Load(), *got)
		}
	}
}

// negative control: drop the X-Pstack-Forwarded-By header from Rewrite → A and B pass the PUT back
// and forth until the test times out.
func TestForwardLoop(t *testing.T) {
	a := fwServer(t, "")
	as := httptest.NewServer(http.HandlerFunc(a.handle))
	defer as.Close()
	b := fwServer(t, "")
	bs := httptest.NewServer(http.HandlerFunc(b.handle))
	defer bs.Close()
	aTok, bTok := fwToken(t, a, "dev", auth.Developer), fwToken(t, b, "dev", auth.Developer)
	fwSet(t, a, bs.URL, bTok)
	fwSet(t, b, as.URL, aTok)

	if rec := fwReq(t, a, "t0ken", "PUT", "/api/deployments/pr-1", fwDeployBody(t), nil); rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
	}
	if dep, _ := b.registry.Get("pr-1"); dep == nil {
		t.Fatal("not on B")
	}
	if dep, _ := a.registry.Get("pr-1"); dep != nil {
		t.Fatal("on A")
	}
}

// negative control: write on every answer (drop the 60 s mark) → the +30 s call moves lastOkAt; never
// reset the mark on DELETE or PUT → the first call after it, inside a minute, writes nothing.
func TestForwardTouchesLastOk(t *testing.T) {
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"root": false, "user": map[string]string{"role": "developer"}})
	}))
	defer bs.Close()
	a, _ := crServer(t)
	now := int64(1_000_000)
	a.forwardNow = func() int64 { return now }
	fwSeed(t, a, bs.URL, "b-token")
	lastOk := func() int64 {
		t.Helper()
		f, err := store.GetForward(a.store.DB)
		if err != nil || f == nil {
			t.Fatal(f, err)
		}
		return f.LastOkAt
	}
	call := func(step int64) int64 {
		t.Helper()
		now += step
		if rec := fwReq(t, a, "t0ken", "GET", "/api/deployments", "", nil); rec.Code != 200 {
			t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
		}
		return lastOk()
	}

	if got := call(0); got != 1_000_000 {
		t.Fatalf("after the first call lastOkAt = %d", got)
	}
	if got := call(30_000); got != 1_000_000 {
		t.Fatalf("after +30s lastOkAt = %d", got)
	}
	if got := call(31_000); got != 1_061_000 {
		t.Fatalf("after +61s lastOkAt = %d", got)
	}

	// A new setting resets the mark: its first forwarded answer writes, inside the minute.
	if code, body := fwCall(t, a, "DELETE", ""); code != 200 {
		t.Fatalf("DELETE = %d: %s", code, body)
	}
	fwSeed(t, a, bs.URL, "b-token")
	if got := call(1_000); got != 1_062_000 {
		t.Fatalf("after DELETE lastOkAt = %d", got)
	}
	fwSet(t, a, bs.URL, "b-token")
	if got := call(1_000); got != 1_063_000 {
		t.Fatalf("after PUT lastOkAt = %d", got)
	}
}
