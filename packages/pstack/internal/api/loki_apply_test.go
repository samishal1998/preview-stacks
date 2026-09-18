package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
)

// lokiInspect is `docker inspect` of a running loki container, l1: the id, image, service label and
// start time the apply reads (inspect.go:229-330).
const lokiInspect = `[{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki"}},"State":{"Status":"running","StartedAt":"2026-09-15T10:00:00Z"}}]`

const lokiPS = "docker ps -aq --filter 'label=com.docker.compose.project=pstack-control'"

// lokiFixture is a server whose LokiDir holds slice 1's config, with every loki-apply command sent to
// one exec.Fake. override answers first. Anything it declines gets a running l1 and OK.
func lokiFixture(t *testing.T, override func(cmd string) (exec.Result, bool)) (*Server, *exec.Fake) {
	t.Helper()
	data := t.TempDir()
	dir := filepath.Join(data, "control", "loki")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, loki.ConfigFile), []byte(pstack.LokiConfig), 0o666); err != nil {
		t.Fatal(err)
	}
	// LokiUID is this process's euid: loki.WriteFile checks CredentialsOwner before a 0600 write, and
	// the default 10001 would refuse the S3 test's s3-credentials.next at verify.
	s, err := New(Options{DataDir: data, Bus: events.New(), Log: func(string) {}, LokiDir: dir, LokiReadyTimeoutMs: 300, LokiUID: os.Geteuid()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if override != nil {
			if r, ok := override(cmd); ok {
				return r, true
			}
		}
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: lokiInspect}, true
		}
		return exec.Result{OK: true}, true
	}
	s.host = f
	s.lokiRunner = func(context.Context) exec.Runner { return f }
	s.lokiPoll = time.Millisecond
	return s, f
}

// waitLokiJob polls the registry until the job is terminal. No such helper exists in package api;
// jobs_test.go's is package jobs'.
func waitLokiJob(t *testing.T, s *Server, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if j, ok := s.jobs.Get(id); ok && j.State.Terminal() {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is not terminal after 10s", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// loggingChanged collects logging.changed events. The mutex exists because the job goroutine emits
// and the test reads.
func loggingChanged(s *Server) func() []events.Event {
	var mu sync.Mutex
	got := []events.Event{}
	s.bus.On(func(e events.Event) {
		if e.Event == "logging.changed" {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
	})
	return func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event{}, got...)
	}
}

func retention(days int) *loki.ChunksPatch {
	return &loki.ChunksPatch{RetentionDays: days, Chunks: loki.Defaults().Chunks}
}

func readLoki(t *testing.T, s *Server, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.opts.LokiDir, name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func lastStep(t *testing.T, j jobs.Job) (phase string, ok bool, message string) {
	t.Helper()
	if j.Outcome == nil || len(j.Outcome.Steps) == 0 {
		t.Fatalf("job %s has no steps: %s", j.ID, jsonx.Must(j))
	}
	st := j.Outcome.Steps[len(j.Outcome.Steps)-1]
	if st.Message != nil {
		message = *st.Message
	}
	return string(st.Phase), st.OK, message
}

func assertNoNext(t *testing.T, s *Server) {
	t.Helper()
	for _, n := range []string{loki.ConfigFile, loki.CredentialsFile} {
		if _, err := os.Stat(filepath.Join(s.opts.LokiDir, n+loki.NextSuffix)); !os.IsNotExist(err) {
			t.Errorf("%s%s is left behind (%v)", n, loki.NextSuffix, err)
		}
	}
}

func restarted(f *exec.Fake) bool {
	for _, c := range f.Commands() {
		if strings.HasPrefix(c, "docker restart") {
			return true
		}
	}
	return false
}

func TestLokiApplyRunsTheStepsInOrder(t *testing.T) {
	// negative control: move the RestartControlService call above the `docker run … -verify-config`
	// line — command 2 is `docker ps`, not the verify run, and the order check fails. (Also run:
	// delete the logging.changed Emit — the event check fails; delete `a.sink.Emit(log.Info, "by "+by)`
	// — the first-log-line check fails.)
	s, f := lokiFixture(t, nil)
	changed := loggingChanged(s)
	job, ok := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if !ok || job.Action != jobs.LokiApply || job.Stack != inspect.ControlProject {
		t.Fatalf("a loki-apply job on the control key: %+v %v", job.Stub(), ok)
	}
	j := waitLokiJob(t, s, job.ID)
	if j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if len(j.Log) == 0 || j.Log[0].Message != "by alice" {
		t.Errorf("the transcript's first line must be `by alice`: %s", jsonx.Must(j.Log))
	}
	want := []string{
		lokiPS, "docker inspect 'l1'",
		"docker run --rm --network none --volumes-from 'l1:ro' 'grafana/loki:3.7.7' -config.file=/etc/loki/config.yaml.next -verify-config",
		lokiPS, "docker inspect 'l1'", "docker restart 'l1'",
		"docker exec 'l1' /usr/bin/loki -health",
	}
	cmds := f.Commands()
	if len(cmds) != len(want)+1 {
		t.Fatalf("commands:\n%s", strings.Join(cmds, "\n"))
	}
	for i, w := range want {
		if cmds[i] != w {
			t.Errorf("command %d = %q, want %q", i, cmds[i], w)
		}
	}
	if logs := cmds[len(want)]; !strings.HasPrefix(logs, "docker logs --since '") || !strings.HasSuffix(logs, "' 'l1'") {
		t.Errorf("last command = %q, want docker logs --since '<RFC3339>' 'l1'", logs)
	}
	phases := []string{}
	for _, st := range j.Outcome.Steps {
		phases = append(phases, string(st.Phase))
		if st.Axis != "loki" || !st.OK {
			t.Errorf("step %+v", st)
		}
	}
	if got := strings.Join(phases, " "); got != "find render verify commit swap restart ready finish" {
		t.Errorf("phases %q", got)
	}
	config, _ := readLoki(t, s, loki.ConfigFile)
	if !strings.Contains(config, "  retention_period: 336h") || !strings.Contains(config, "  max_query_lookback: 336h") {
		t.Errorf("config.yaml:\n%s", config)
	}
	if _, here := readLoki(t, s, loki.CredentialsFile); here {
		t.Error("a filesystem render writes no s3-credentials")
	}
	assertNoNext(t, s)
	row, err := loki.Read(s.store)
	if err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 14 {
		t.Fatalf("row: %+v (%v)", row, err)
	}
	evs := changed()
	wantData := `{"by":"alice","job":"` + job.ID + `","changed":["retention"],"storage":"filesystem","cutover":null,"retentionDays":14}`
	if len(evs) != 1 || string(evs[0].Data) != wantData {
		t.Fatalf("logging.changed: got %d events, want exactly one %s", len(evs), wantData)
	}
}

func TestLokiApplyFindsTheLokiContainer(t *testing.T) {
	// negative control: end the no-container arm `failed` for a boot apply too — the boot case reads
	// failed and the check fails. (Also run: move the lokiTake call below the find step — the save's
	// patch stays pending and the lokiPending check fails.)
	traefik := `[{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.5","Labels":{"com.docker.compose.service":"traefik"}},"State":{"Status":"running"}}]`
	for _, c := range []struct {
		name      string
		reachable bool
		boot      bool
		ok        bool
		message   string
	}{
		{"docker does not answer", false, false, false, "docker did not answer"},
		{"a save on a host without loki", true, false, false, "Loki is not running on this host"},
		{"a boot apply on a host without loki", true, true, true, "no loki container"},
	} {
		s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
			switch {
			case strings.HasPrefix(cmd, "docker ps -aq"):
				return exec.Result{OK: c.reachable, Stdout: "t1\n"}, true
			case strings.HasPrefix(cmd, "docker inspect"):
				return exec.Result{OK: true, Stdout: traefik}, true
			}
			return exec.Result{}, false
		})
		g := uint64(0)
		if !c.boot {
			g = s.lokiPut("alice", retention(14), nil)
		}
		job, _ := s.startLokiApply(g, "alice", c.boot)
		j := waitLokiJob(t, s, job.ID)
		phase, ok, msg := lastStep(t, j)
		if phase != "find" || ok != c.ok || msg != c.message || len(j.Outcome.Steps) != 1 {
			t.Errorf("%s: %s %v %q (%d steps)", c.name, phase, ok, msg, len(j.Outcome.Steps))
		}
		for _, cmd := range f.Commands() {
			if !strings.HasPrefix(cmd, "docker ps") && !strings.HasPrefix(cmd, "docker inspect") {
				t.Errorf("%s: ran %q", c.name, cmd)
			}
		}
		if s.lokiPending() {
			t.Errorf("%s: a failed job must not leave its save pending", c.name)
		}
		if got, _ := readLoki(t, s, loki.ConfigFile); got != pstack.LokiConfig {
			t.Errorf("%s: config.yaml changed", c.name)
		}
	}
}

func TestLokiApplyVerifyFailureChangesNothing(t *testing.T) {
	// negative control: move loki.Save above the verify run — the row is no longer empty and the row
	// check fails. (Also run: delete the two deferred os.Remove calls — assertNoNext fails.)
	s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker run ") {
			return exec.Result{OK: false, Code: 1, Stderr: "level=info msg=\"loading config\"\nlevel=error msg=\"invalid config\" err=\"ingester: bad chunk encoding\"\n\n"}, true
		}
		return exec.Result{}, false
	})
	changed := loggingChanged(s)
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	j := waitLokiJob(t, s, job.ID)
	phase, ok, msg := lastStep(t, j)
	if j.State != jobs.Failed || phase != "verify" || ok || msg != `level=error msg="invalid config" err="ingester: bad chunk encoding"` {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
		t.Error("config.yaml changed")
	}
	assertNoNext(t, s)
	if row, err := loki.Read(s.store); err != nil || row != nil {
		t.Errorf("the row must stay empty: %+v (%v)", row, err)
	}
	if restarted(f) {
		t.Error("a failed verify must not restart Loki")
	}
	if n := len(changed()); n != 0 {
		t.Errorf("%d logging.changed events for a failed apply", n)
	}
}

func TestLokiApplyRowWriteFailureRestartsNothing(t *testing.T) {
	// negative control: ignore loki.Save's error (`_ = loki.Save(…)` and carry on) — the job swaps
	// and issues `docker restart 'l1'`, and both checks fail.
	var s *Server
	s, f := lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker run ") {
			// Step 2 has already read the row. Every later statement now fails, as on a full disk.
			_ = s.store.Close()
			return exec.Result{OK: true}, true
		}
		return exec.Result{}, false
	})
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	j := waitLokiJob(t, s, job.ID)
	phase, ok, msg := lastStep(t, j)
	if j.State != jobs.Failed || phase != "commit" || ok || !strings.HasPrefix(msg, "could not record the save: ") {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	if restarted(f) {
		t.Error("a failed commit must not restart Loki")
	}
	if config, _ := readLoki(t, s, loki.ConfigFile); config != pstack.LokiConfig {
		t.Error("config.yaml changed")
	}
	assertNoNext(t, s)
}

func TestLokiApplySavesTheRowBeforeTheRestart(t *testing.T) {
	// negative control: move loki.Save (the commit step) below the ready wait — the row read at the
	// restart is still empty and the check fails.
	var s *Server
	var atRestart *loki.Row
	var readErr error
	s, _ = lokiFixture(t, func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker restart ") {
			// The job is between statements here, never inside store.Tx, so the one pooled connection
			// is free (rule 16).
			atRestart, readErr = loki.Read(s.store)
			return exec.Result{OK: true, Stdout: "l1\n"}, true
		}
		return exec.Result{}, false
	})
	job, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if j := waitLokiJob(t, s, job.ID); j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if readErr != nil || atRestart == nil || !atRestart.InFlight || atRestart.Settings.RetentionDays != 14 || atRestart.Previous != nil {
		t.Fatalf("at the restart the row must hold the save and its write-ahead record: %+v (%v)", atRestart, readErr)
	}
}

func TestLokiApplyCarriesASupersededSave(t *testing.T) {
	// negative control: make lokiTake take an entry only when `e.gen == g` — the successor applies
	// retention alone, the row stays filesystem, and the row check fails. (Also run: drop `secret`
	// from the scrub values appended at render — the job record carries the secret and the record
	// check fails.)
	const secret = "wJalrXUtnFEMI-K7MDENG-bPxRfiCY"
	const keyID = "AKIAIOSFODNN7EXAMPLE"
	release := make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	var s *Server
	s, _ = lokiFixture(t, func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker exec "):
			once.Do(func() { <-release }) // the first apply waits here, after its commit and swap
			return exec.Result{OK: true}, true
		case strings.HasPrefix(cmd, "docker logs "):
			// Loki echoes the credential it was given into its own log. The record must not keep it.
			if _, err := os.Stat(filepath.Join(s.opts.LokiDir, loki.CredentialsFile)); err == nil {
				return exec.Result{OK: true, Stderr: "level=info msg=\"Loki started\"\nlevel=error msg=\"flush failed\" detail=\"denied for " + secret + "\"\n"}, true
			}
		}
		return exec.Result{}, false
	})
	t.Cleanup(free)
	changed := loggingChanged(s)
	cutover := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	storage := &loki.StoragePatch{Storage: loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: keyID, Cutover: cutover,
	}}, Secret: secret}

	first, _ := s.startLokiApply(s.lokiPut("alice", retention(10), nil), "alice", false)
	queued, _ := s.startLokiApply(s.lokiPut("root (PSTACK_TOKEN)", nil, storage), "root (PSTACK_TOKEN)", false)
	successor, _ := s.startLokiApply(s.lokiPut("alice", retention(14), nil), "alice", false)
	if first.State != jobs.Running || queued.State != jobs.Queued || successor.State != jobs.Queued {
		t.Fatalf("want running, queued, queued: %q %q %q", first.State, queued.State, successor.State)
	}
	free()
	if j := waitLokiJob(t, s, first.ID); j.State != jobs.OK {
		t.Fatalf("first %q: %s", j.State, jsonx.Must(j))
	}
	j := waitLokiJob(t, s, successor.ID)
	if j.State != jobs.OK {
		t.Fatalf("successor %q: %s", j.State, jsonx.Must(j))
	}
	if q, _ := s.jobs.Get(queued.ID); q.State != jobs.Superseded || q.StartedAt != nil {
		t.Fatalf("the storage save's own job must never run: %q, startedAt %v", q.State, q.StartedAt)
	}
	row, err := loki.Read(s.store)
	if err != nil || row == nil || row.InFlight || row.Settings.RetentionDays != 14 || row.Settings.Storage.Type != loki.StorageS3 || row.Secret != secret {
		t.Fatalf("the successor must apply both saves: %+v (%v)", row, err)
	}
	config, _ := readLoki(t, s, loki.ConfigFile)
	for _, want := range []string{"  retention_period: 336h", "      object_store: s3", `    - from: "` + cutover + `"`} {
		if !strings.Contains(config, want) {
			t.Errorf("config.yaml lacks %q", want)
		}
	}
	if creds, here := readLoki(t, s, loki.CredentialsFile); !here || creds != loki.Credentials(keyID, secret) {
		t.Errorf("s3-credentials = %q (present %v)", creds, here)
	}
	if st, err := os.Stat(filepath.Join(s.opts.LokiDir, loki.CredentialsFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("s3-credentials must be 0600: %v (%v)", st, err)
	}
	assertNoNext(t, s)
	if phase, ok, msg := lastStep(t, j); phase != "log" || !ok || !strings.HasPrefix(msg, "non-fatal: Loki logged 1 errors after restart: ") {
		t.Errorf("log step: %s %v %q", phase, ok, msg)
	}
	if strings.Contains(string(jsonx.Must(j)), secret) {
		t.Error("the S3 secret reached the job record")
	}
	evs := changed()
	if len(evs) != 2 {
		t.Fatalf("one logging.changed per applying job, got %d", len(evs))
	}
	d := string(evs[1].Data)
	if !strings.Contains(d, `"by":"alice"`) || !strings.Contains(d, `"changed":["retention","storage","credentials"]`) ||
		!strings.Contains(d, `"cutover":"`+cutover+`"`) || strings.Contains(d, keyID) || strings.Contains(d, secret) {
		t.Errorf("successor's logging.changed: %s", d)
	}
}

func TestLokiApplyWhoseSaveWasReplacedChangesNothing(t *testing.T) {
	// negative control: drop `e.gen <= g` from both lokiTake branches — the older job takes bob's
	// newer save and applies it, and the `nothing to change` check fails.
	s, f := lokiFixture(t, nil)
	older := s.lokiPut("alice", retention(10), nil)
	newer := s.lokiPut("bob", retention(14), nil)
	job, _ := s.startLokiApply(older, "alice", false)
	j := waitLokiJob(t, s, job.ID)
	if phase, ok, msg := lastStep(t, j); j.State != jobs.OK || phase != "render" || !ok || msg != "nothing to change" {
		t.Fatalf("%q %s %v %q", j.State, phase, ok, msg)
	}
	for _, c := range f.Commands() {
		if c != lokiPS && c != "docker inspect 'l1'" {
			t.Fatalf("a job with nothing to change ran %q", c)
		}
	}
	if !s.lokiPending() {
		t.Fatal("bob's save must still be pending")
	}
	job2, _ := s.startLokiApply(newer, "bob", false)
	if j := waitLokiJob(t, s, job2.ID); j.State != jobs.OK {
		t.Fatalf("state %q: %s", j.State, jsonx.Must(j))
	}
	if row, err := loki.Read(s.store); err != nil || row == nil || row.Settings.RetentionDays != 14 {
		t.Errorf("row: %+v (%v)", row, err)
	}
	if s.lokiPending() {
		t.Error("nothing is pending once bob's save is applied")
	}
}
