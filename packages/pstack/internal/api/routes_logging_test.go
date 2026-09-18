package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

// loggingDocker is what the fake docker says about the control stack.
type loggingDocker int

const (
	loggingDockerSilent loggingDocker = iota // `docker ps` fails: Reachable false
	loggingNoLoki                            // reachable, no control containers
	loggingLokiUp                            // one running `loki` container, l1
)

const loggingInspect = `[{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}},"State":{"Status":"running","StartedAt":"2026-09-15T10:00:00Z"}}]`

// Slice 1's settings, and one change, as PUT /api/logging bodies.
const (
	loggingDefaults    = `{"retentionDays":7,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`
	loggingRetention14 = `{"retentionDays":14,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`
	loggingSecret      = "wJalrXUtnFEMIK7MDENGbPxRfiCY"
)

// loggingFixture is a Server over a LokiDir holding slice 1's config.yaml (when mounted), with the
// host runner and the apply's runners on one exec.Fake.
func loggingFixture(t *testing.T, docker loggingDocker, mounted bool, lokiUID int) (*Server, *exec.Fake) {
	t.Helper()
	dir := t.TempDir()
	if mounted {
		// Written BEFORE New, so reconcileLoki finds slice 1's bytes and starts nothing.
		if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(pstack.LokiConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{DataDir: t.TempDir(), LokiDir: dir, LokiUID: lokiUID, Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			switch docker {
			case loggingDockerSilent:
				return exec.Result{OK: false, Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
			case loggingNoLoki:
				return exec.Result{OK: true}, true
			}
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: loggingInspect}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f
	s.lokiRunner = func(context.Context) exec.Runner { return f }
	s.lokiPoll = time.Millisecond
	return s, f
}

// loggingCall drives the gated chain as root, so dispatch and the gate are in the path.
func loggingCall(s *Server, method, path, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, rd)
	if err := s.routes(w, r, path, &auth.Principal{Kind: auth.KindRoot}, map[string]string{}); err != nil {
		s.fail(w, err)
	}
	return w
}

func loggingBody(t *testing.T, w *httptest.ResponseRecorder) *omap.Map {
	t.Helper()
	v, err := omap.Parse(w.Body.Bytes())
	m, ok := v.(*omap.Map)
	if err != nil || !ok {
		t.Fatalf("not a JSON object: %d %s", w.Code, w.Body.String())
	}
	return m
}

func loggingKeys(m *omap.Map) string {
	var ks []string
	m.Each(func(k string, _ any) { ks = append(ks, k) })
	return strings.Join(ks, ",")
}

// loggingUntilDone waits for a job a PUT started, so nothing writes into a t.TempDir being removed.
func loggingUntilDone(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := s.jobs.Get(id); ok && j.State.Terminal() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %q never finished", id)
}

func loggingS3Body(endpoint, cutover, secret string) string {
	return `{"type":"s3","endpoint":"` + endpoint + `","region":"us-east-1","bucket":"pstack-logs","pathStyle":true,"accessKeyId":"AKIDEXAMPLE","secretAccessKey":"` + secret + `","cutover":"` + cutover + `"}`
}

// loggingSaveS3 stores a finished S3 save, the state after a successful apply.
func loggingSaveS3(t *testing.T, s *Server) {
	t.Helper()
	saved := loki.Defaults()
	saved.Storage = loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: "AKIDEXAMPLE", Cutover: "2026-09-16",
	}}
	if err := loki.Save(s.store, saved, loggingSecret, nil); err != nil {
		t.Fatal(err)
	}
	if err := loki.Finish(s.store); err != nil {
		t.Fatal(err)
	}
}

// loggingFakeS3 answers PUT 200 and DELETE 204, or 403 InvalidAccessKeyId to everything while refusing.
func loggingFakeS3(t *testing.T) (endpoint string, calls func() []string, refuse func(bool)) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	no := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		refused := no
		mu.Unlock()
		switch {
		case refused:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidAccessKeyId</Code><Message>body-text-never-echoed</Message></Error>`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL,
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) },
		func(v bool) { mu.Lock(); no = v; seen = nil; mu.Unlock() }
}

// TestLoggingPutAnswersBeforeReadingTheBody covers steps 1 and 2 of both PUTs. The rbac conformance
// rows rely on a bodiless PUT on a logging-off host being a 409.
//
// negative control: move `parse(bodyOrEmpty(r))` above step 1 in lokiSave. The bodiless no-loki PUTs
// then answer 400. Also: delete the loki.Writable check, and the unmounted PUT answers 202.
func TestLoggingPutAnswersBeforeReadingTheBody(t *testing.T) {
	for _, c := range []struct {
		docker loggingDocker
		status int
		msg    string
	}{
		{loggingDockerSilent, 503, "docker did not answer"},
		{loggingNoLoki, 409, "Loki is not running on this host — run pstack logging loki on the host"},
	} {
		s, _ := loggingFixture(t, c.docker, true, os.Geteuid())
		for _, path := range []string{"/api/logging", "/api/logging/storage"} {
			w := loggingCall(s, http.MethodPut, path, "")
			if w.Code != c.status || loggingBody(t, w).GetString("error") != c.msg {
				t.Errorf("PUT %s = %d %s, want %d %q", path, w.Code, w.Body.String(), c.status, c.msg)
			}
		}
	}
	s, _ := loggingFixture(t, loggingLokiUp, false, os.Geteuid())
	w := loggingCall(s, http.MethodPut, "/api/logging", loggingRetention14)
	if w.Code != 409 || loggingBody(t, w).GetString("error") != "Loki's config.yaml is missing or read-only — run pstack upgrade on the host" {
		t.Errorf("unmounted: %d %s", w.Code, w.Body.String())
	}
}

// TestLoggingPutRefusesABadBodyWith400 checks that a malformed or out-of-range field is the
// caller's to fix, names the field, and starts nothing.
//
// negative control: drop `n == math.Trunc(n)` from wholeNumber. `retentionDays: 1.5` then saves as
// 1 and answers 202.
func TestLoggingPutRefusesABadBodyWith400(t *testing.T) {
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	for _, c := range []struct{ path, body, msg string }{
		{"/api/logging", `{"retentionDays":0,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`, "retentionDays"},
		{"/api/logging", `{"retentionDays":1.5,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"}}`, "retentionDays must be a whole number"},
		{"/api/logging", `{"retentionDays":7}`, "chunks.idlePeriodMinutes must be a whole number"},
		{"/api/logging/storage", `{"type":"gcs"}`, "type must be filesystem or s3"},
		{"/api/logging/storage", `{"type":"s3","endpoint":"https://s3.example.com","region":"us-east-1","bucket":"pstack-logs","accessKeyId":"AKIDEXAMPLE","secretAccessKey":"` + loggingSecret + `","cutover":"2099-01-01"}`, "pathStyle must be true or false"},
	} {
		w := loggingCall(s, http.MethodPut, c.path, c.body)
		if w.Code != 400 || !strings.Contains(loggingBody(t, w).GetString("error"), c.msg) {
			t.Errorf("PUT %s %s = %d %s, want 400 naming %q", c.path, c.body, w.Code, w.Body.String(), c.msg)
		}
	}
	if n := len(s.jobs.List()); n != 0 {
		t.Errorf("a refused body started %d job(s)", n)
	}
}

// TestLoggingPutAnswersAnUnchangedBodyByState: an unchanged body is a 200 only on an idle key.
// Otherwise the job decides.
func TestLoggingPutAnswersAnUnchangedBodyByState(t *testing.T) {
	t.Run("idle: 200 changed:false, nothing restarted, no job", func(t *testing.T) {
		// negative control: delete step 5's branch in lokiSave. Both calls then answer 202.
		s, f := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		for _, c := range []struct{ path, body string }{
			{"/api/logging", loggingDefaults},
			{"/api/logging/storage", `{"type":"filesystem"}`},
		} {
			w := loggingCall(s, http.MethodPut, c.path, c.body)
			if w.Code != 200 || strings.Join(strings.Fields(w.Body.String()), "") != `{"changed":false}` {
				t.Errorf("PUT %s = %d %s, want 200 {\"changed\":false}", c.path, w.Code, w.Body.String())
			}
		}
		for _, cmd := range f.Commands() {
			if strings.HasPrefix(cmd, "docker restart") {
				t.Errorf("an unchanged save restarted something: %s", cmd)
			}
		}
		if n := len(s.jobs.List()); n != 0 {
			t.Errorf("an unchanged save started %d job(s)", n)
		}
	})

	t.Run("busy: 202 with a queued loki-apply stub", func(t *testing.T) {
		// negative control: drop `!s.jobs.IsBusy(inspect.ControlProject)` from step 5. This then answers 200.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		done := make(chan struct{})
		block := func(log.Sink, context.Context) (stack.Outcome, error) { <-done; return stack.Outcome{OK: true}, nil }
		if _, ok := s.jobs.Start(inspect.ControlProject, jobs.Verify, block, nil); !ok {
			close(done)
			t.Fatal("could not occupy the control key")
		}
		w := loggingCall(s, http.MethodPut, "/api/logging", loggingDefaults)
		close(done)
		if w.Code != 202 {
			t.Fatalf("busy key: %d %s, want 202", w.Code, w.Body.String())
		}
		job := loggingBody(t, w).GetMap("job")
		if job.GetString("action") != "loki-apply" || job.GetString("stack") != inspect.ControlProject || job.GetString("state") != "queued" {
			t.Errorf("stub = %s", w.Body.String())
		}
		loggingUntilDone(t, s, job.GetString("id"))
	})

	t.Run("a refused Start: 409, and the save stays pending", func(t *testing.T) {
		// negative control: write 202 on `!ok` in lokiSave. This then reads 202.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		release, ok := s.jobs.Hold(inspect.ControlProject)
		if !ok {
			t.Fatal("the control key was already busy")
		}
		defer release()
		w := loggingCall(s, http.MethodPut, "/api/logging", loggingRetention14)
		if w.Code != 409 || loggingBody(t, w).GetString("error") != "pstack-control is busy with a teardown — retry" {
			t.Errorf("held key: %d %s", w.Code, w.Body.String())
		}
		if !s.lokiPending() {
			t.Error("a refused save must stay pending, for the next save's job to carry")
		}
	})
}

// TestLoggingStoragePutKeepsS3OneWayAndFixed checks that a stored S3 save cannot go back to
// filesystem or move, and that both refusals are 409s.
//
// negative control: remove the errors.Is(ErrOneWay/ErrFixed) branch in lokiSave. Both refusals then
// reach fail() and read 500.
func TestLoggingStoragePutKeepsS3OneWayAndFixed(t *testing.T) {
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	loggingSaveS3(t, s)
	for _, c := range []struct {
		body string
		want error
	}{
		{`{"type":"filesystem"}`, loki.ErrOneWay},
		{`{"type":"s3","endpoint":"https://s3.eu-central-1.amazonaws.com","region":"eu-central-1","bucket":"other-logs","pathStyle":false,"accessKeyId":"AKIDEXAMPLE","secretAccessKey":"","cutover":"2026-09-16"}`, loki.ErrFixed},
	} {
		w := loggingCall(s, http.MethodPut, "/api/logging/storage", c.body)
		if w.Code != 409 || loggingBody(t, w).GetString("error") != c.want.Error() {
			t.Errorf("PUT %s = %d %s, want 409 %q", c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if n := len(s.jobs.List()); n != 0 {
		t.Errorf("a refused storage save started %d job(s)", n)
	}
}

// TestLoggingStoragePutNeedsAnOwnableCredentialsFile covers step 4. It sets LokiUID one above this
// process's euid, because the loki package's geteuid seam is unexported.
//
// negative control: delete step 4 in lokiSave. The probe then reaches the fake S3, and the PUT
// answers 202.
func TestLoggingStoragePutNeedsAnOwnableCredentialsFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("euid 0 can always chown the credentials file to Loki's uid")
	}
	endpoint, calls, _ := loggingFakeS3(t)
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid()+1)
	w := loggingCall(s, http.MethodPut, "/api/logging/storage", loggingS3Body(endpoint, loki.EarliestCutover(time.Now(), s.lokiLead()), loggingSecret))
	if w.Code != 409 || loggingBody(t, w).GetString("error") != loki.ErrNeedsRoot.Error() {
		t.Errorf("non-root: %d %s, want 409 %q", w.Code, w.Body.String(), loki.ErrNeedsRoot)
	}
	if n := len(calls()); n != 0 {
		t.Errorf("the probe sent %d request(s) before the refusal", n)
	}
}

// TestLoggingStoragePutProbesBeforeItRecordsAnything covers step 6, before step 7: a refused probe
// records nothing, and a good one is a PUT, then a DELETE, of the same object.
//
// negative control: move step 6 (loki.Probe) below step 7 in lokiSave. The refused probe then
// leaves a pending save and a job behind.
func TestLoggingStoragePutProbesBeforeItRecordsAnything(t *testing.T) {
	endpoint, calls, refuse := loggingFakeS3(t)
	s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
	body := loggingS3Body(endpoint, loki.EarliestCutover(time.Now(), s.lokiLead()), loggingSecret)

	refuse(true)
	w := loggingCall(s, http.MethodPut, "/api/logging/storage", body)
	if msg := loggingBody(t, w).GetString("error"); w.Code != 400 || !strings.Contains(msg, "403 InvalidAccessKeyId") || strings.Contains(msg, "body-text-never-echoed") {
		t.Fatalf("refused probe: %d %s", w.Code, w.Body.String())
	}
	if s.lokiPending() || len(s.jobs.List()) != 0 {
		t.Fatal("a refused probe must record nothing and start nothing")
	}

	refuse(false)
	w = loggingCall(s, http.MethodPut, "/api/logging/storage", body)
	if w.Code != 202 {
		t.Fatalf("good probe: %d %s, want 202", w.Code, w.Body.String())
	}
	got := calls()
	if len(got) != 2 || !strings.HasPrefix(got[0], "PUT /pstack-logs/pstack-probe-") || got[1] != "DELETE"+strings.TrimPrefix(got[0], "PUT") {
		t.Errorf("S3 saw %q, want PUT then DELETE of one pstack-probe-<hex> key", got)
	}
	loggingUntilDone(t, s, loggingBody(t, w).GetMap("job").GetString("id"))
}

func TestLoggingGet(t *testing.T) {
	t.Run("defaults: spec order, s3 and updatedAt null, encodings an array", func(t *testing.T) {
		// negative control: add `,omitempty` to loggingStorageView.S3's json tag. "s3" then drops out
		// of the storage keys.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		s.lokiNow = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
		w := loggingCall(s, http.MethodGet, "/api/logging", "")
		if w.Code != 200 {
			t.Fatalf("GET: %d %s", w.Code, w.Body.String())
		}
		m := loggingBody(t, w)
		if got := loggingKeys(m); got != "enabled,source,updatedAt,retentionDays,chunks,storage,limits" {
			t.Errorf("keys = %s", got)
		}
		if v, _ := m.Get("enabled"); v != true {
			t.Errorf("enabled = %v, want true", v)
		}
		if m.GetString("source") != "default" {
			t.Errorf("source = %q", m.GetString("source"))
		}
		if v, ok := m.Get("updatedAt"); !ok || v != nil {
			t.Errorf("updatedAt = %v (present %v), want null", v, ok)
		}
		if v, _ := m.Get("retentionDays"); v != int64(7) {
			t.Errorf("retentionDays = %v", v)
		}
		st := m.GetMap("storage")
		if got := loggingKeys(st); got != "type,s3" || st.GetString("type") != "filesystem" {
			t.Errorf("storage = %s / %q", got, st.GetString("type"))
		}
		if v, ok := st.Get("s3"); !ok || v != nil {
			t.Errorf("storage.s3 = %v (present %v), want null", v, ok)
		}
		lim := m.GetMap("limits")
		if got := loggingKeys(lim); got != "retentionDays,idlePeriodMinutes,maxAgeMinutes,targetSizeKiB,encodings,earliestCutover" {
			t.Errorf("limits keys = %s", got)
		}
		if enc := lim.GetSlice("encodings"); len(enc) != 4 {
			t.Errorf("encodings = %v, want an array of 4", enc)
		}
		// 12:00 + 2×15m + 10m is 12:40 UTC, so the first midnight strictly after it is tomorrow's.
		if lim.GetString("earliestCutover") != "2026-09-16" {
			t.Errorf("earliestCutover = %q", lim.GetString("earliestCutover"))
		}
	})

	t.Run("enabled is what docker says: null when silent, false with no loki", func(t *testing.T) {
		// negative control: make lokiEnabled return a pointer to false when !Reachable. The silent
		// case then reads false.
		for _, c := range []struct {
			docker loggingDocker
			want   any
		}{{loggingDockerSilent, nil}, {loggingNoLoki, false}} {
			s, _ := loggingFixture(t, c.docker, true, os.Geteuid())
			w := loggingCall(s, http.MethodGet, "/api/logging", "")
			if v, ok := loggingBody(t, w).Get("enabled"); w.Code != 200 || !ok || v != c.want {
				t.Errorf("docker %d: %d enabled=%v (present %v), want %v", c.docker, w.Code, v, ok, c.want)
			}
		}
	})

	t.Run("a stored S3 row: source db, secretSet, never the secret or the mask", func(t *testing.T) {
		// negative control: add `Secret string `json:"secretAccessKey"`` to loggingS3View and fill it
		// with secretMask. The mask then shows in the body.
		s, _ := loggingFixture(t, loggingLokiUp, true, os.Geteuid())
		loggingSaveS3(t, s)
		w := loggingCall(s, http.MethodGet, "/api/logging", "")
		if text := w.Body.String(); strings.Contains(text, loggingSecret) || strings.Contains(text, secretMask) {
			t.Fatalf("the GET leaks the secret or its mask: %s", text)
		}
		m := loggingBody(t, w)
		if m.GetString("source") != "db" {
			t.Errorf("source = %q", m.GetString("source"))
		}
		if v, _ := m.Get("updatedAt"); v == nil {
			t.Error("updatedAt must be set on a stored row")
		}
		s3 := m.GetMap("storage").GetMap("s3")
		if got := loggingKeys(s3); got != "endpoint,region,bucket,pathStyle,accessKeyId,secretSet,cutover" {
			t.Errorf("s3 keys = %s", got)
		}
		if v, _ := s3.Get("secretSet"); v != true {
			t.Errorf("secretSet = %v", v)
		}
	})
}
