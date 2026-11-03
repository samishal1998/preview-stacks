package cli

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/api"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func init() { migratePoll = time.Millisecond }

const migVars = `{"B":"2","A":"1"}`

// migRow is one GET /api/deployments row in state st (running | asleep | down | unknown).
func migRow(id string, updatedAt int, st string) string {
	tail := map[string]string{
		"running": `"asleep":null,"running":true`,
		"asleep":  `"asleep":{"since":1,"reason":"idle 2h","hosts":[],"rules":[]},"running":false`,
		"down":    `"asleep":null,"running":false`,
		"unknown": `"asleep":null,"running":null`,
	}[st]
	return `{"id":"` + id + `","kind":"isolated","vars":` + migVars + `,"createdAt":1,"updatedAt":` + strconv.Itoa(updatedAt) + `,` + tail + `}`
}

func migList(rows ...string) answer {
	return answer{200, `{"deployments":[` + strings.Join(rows, ",") + `]}`}
}

func migSource(id string) answer {
	return answer{200, `{"id":"` + id + `","specName":null,"spec":"spec-` + id + `","compose":"compose-` + id + `"}`}
}

func migJob(id string) answer { return answer{202, `{"job":{"id":"` + id + `","state":"queued"}}`} }

var migOK = answer{200, `{"job":{"id":"x","state":"ok"}}`}

func runMigrate(t *testing.T, from, to string, argv ...string) (int, string, string) {
	t.Helper()
	env := map[string]string{"PSTACK_FROM_TOKEN": "ft", "PSTACK_TO_TOKEN": "tt"}
	return runForward(t, env, append([]string{"migrate", "--from", from, "--to", to}, argv...)...)
}

// writes is every call that is not a GET, as `METHOD path body`.
func writes(calls []routeCall) []string {
	out := []string{}
	for _, c := range calls {
		if c.method != "GET" {
			out = append(out, strings.TrimSpace(c.method+" "+c.path+" "+c.body))
		}
	}
	return out
}

func sameList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\ngot  %q\nwant %q", what, got, want)
	}
}

// carryFixture is a source with one running, one asleep and one down deployment, and an empty target.
func carryFixture(t *testing.T) (src, tgt string, srcCalls, tgtCalls func() []routeCall) {
	t.Helper()
	s, sc := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "asleep"), migRow("pr-3", 10, "down")),
		"GET /api/deployments/pr-1/source": migSource("pr-1"),
		"GET /api/deployments/pr-2/source": migSource("pr-2"),
		"GET /api/deployments/pr-3/source": {200, `{"id":"pr-3","specName":null,"spec":"spec-pr-3","compose":null}`},
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(),
		"PUT /api/deployments/pr-1":        {200, `{}`},
		"PUT /api/deployments/pr-2":        {200, `{}`},
		"PUT /api/deployments/pr-3":        {200, `{}`},
		"POST /api/deployments/pr-1/up":    migJob("j1"),
		"POST /api/deployments/pr-2/up":    migJob("j2"),
		"POST /api/deployments/pr-2/sleep": migJob("j3"),
		"GET /api/jobs/j1":                 migOK,
		"GET /api/jobs/j2":                 migOK,
		"GET /api/jobs/j3":                 migOK,
	})
	return s.URL, d.URL, sc, dc
}

func TestMigrateCarriesState(t *testing.T) {
	// negative control: skip the target sleep for asleep rows — pr-2 arrives running and the
	// `POST …/pr-2/sleep` write is missing; send `compose: null` for pr-3 — its PUT body differs.
	src, tgt, sc, tc := carryFixture(t)
	code, out, errOut := runMigrate(t, src, tgt)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if want := "pr-1  running  copied, up\npr-2  asleep  copied, up, slept\npr-3  down  copied\n"; out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
	sameList(t, "target writes", writes(tc()), []string{
		`PUT /api/deployments/pr-1 {"spec":"spec-pr-1","compose":"compose-pr-1","vars":` + migVars + `}`,
		`POST /api/deployments/pr-1/up`,
		`PUT /api/deployments/pr-2 {"spec":"spec-pr-2","compose":"compose-pr-2","vars":` + migVars + `}`,
		`POST /api/deployments/pr-2/up`,
		`PUT /api/deployments/pr-3 {"spec":"spec-pr-3","vars":` + migVars + `}`,
		`POST /api/deployments/pr-2/sleep`,
	})
	sameList(t, "source writes", writes(sc()), nil)
}

func TestMigrateReadsTheSourceItself(t *testing.T) {
	// negative control: pass the forwarded-by header to the target calls too (or to neither) — one
	// of the two loops below fails; swap the two tokens — the Authorization checks fail.
	src, tgt, sc, tc := carryFixture(t)
	if code, _, errOut := runMigrate(t, src, tgt); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, c := range sc() {
		if c.hdr.Get("X-Pstack-Forwarded-By") != "migrate" || c.auth != "Bearer ft" {
			t.Fatalf("source call %s %s: header %q, auth %q", c.method, c.path, c.hdr.Get("X-Pstack-Forwarded-By"), c.auth)
		}
	}
	for _, c := range tc() {
		if _, ok := c.hdr["X-Pstack-Forwarded-By"]; ok || c.auth != "Bearer tt" {
			t.Fatalf("target call %s %s carries the header or auth %q", c.method, c.path, c.auth)
		}
	}
}

func TestMigrateKeepsNamedSpecs(t *testing.T) {
	// negative control: always copy the inline source — the PUT body carries `spec` and the
	// source sees a GET …/source.
	row := strings.Replace(migRow("pr-1", 10, "running"), `"kind"`, `"specName":"app","kind"`, 1)
	s, sc := routeAPI(t, map[string]answer{"GET /api/deployments": migList(row)})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":          migList(),
		"PUT /api/deployments/pr-1":     {200, `{}`},
		"POST /api/deployments/pr-1/up": migJob("j1"),
		"GET /api/jobs/j1":              migOK,
	})
	if code, _, errOut := runMigrate(t, s.URL, d.URL); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	sameList(t, "target writes", writes(dc()), []string{
		`PUT /api/deployments/pr-1 {"specName":"app","vars":` + migVars + `}`,
		`POST /api/deployments/pr-1/up`,
	})
	for _, c := range sc() {
		if strings.HasSuffix(c.path, "/source") {
			t.Fatalf("read the source of a named spec: %s", c.path)
		}
	}
}

func TestMigrateNewestWins(t *testing.T) {
	// negative control: drop the updatedAt comparison and copy whatever the target has — pr-1 is PUT
	// over a newer target.
	s, _ := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "running")),
		"GET /api/deployments/pr-1/source": migSource("pr-1"),
		"GET /api/deployments/pr-2/source": migSource("pr-2"),
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":          migList(migRow("pr-1", 20, "running"), migRow("pr-2", 5, "running")),
		"PUT /api/deployments/pr-2":     {200, `{}`},
		"POST /api/deployments/pr-2/up": migJob("j2"),
		"GET /api/jobs/j2":              migOK,
	})
	code, out, errOut := runMigrate(t, s.URL, d.URL)
	if code != 0 || out != "pr-1  running  unchanged\npr-2  running  copied, up\n" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	sameList(t, "target writes", writes(dc()), []string{
		`PUT /api/deployments/pr-2 {"spec":"spec-pr-2","compose":"compose-pr-2","vars":` + migVars + `}`,
		`POST /api/deployments/pr-2/up`,
	})
}

func TestMigrateMatchesStateOnANewerTarget(t *testing.T) {
	// negative control: act whenever the states differ (P-R4 alone) — pr-1's running target is put
	// to sleep and the writes gain `POST …/pr-1/up` + `…/pr-1/sleep` (MF-R19).
	s, sc := routeAPI(t, map[string]answer{
		"GET /api/deployments": migList(migRow("pr-1", 10, "asleep"), migRow("pr-2", 10, "running"), migRow("pr-3", 10, "asleep"), migRow("pr-4", 10, "running")),
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 20, "running"), migRow("pr-2", 20, "down"), migRow("pr-3", 20, "down"), migRow("pr-4", 20, "asleep")),
		"POST /api/deployments/pr-2/up":    migJob("j2"),
		"POST /api/deployments/pr-3/up":    migJob("j3"),
		"POST /api/deployments/pr-4/up":    migJob("j4"),
		"POST /api/deployments/pr-3/sleep": migJob("j5"),
		"GET /api/jobs/j2":                 migOK,
		"GET /api/jobs/j3":                 migOK,
		"GET /api/jobs/j4":                 migOK,
		"GET /api/jobs/j5":                 migOK,
	})
	code, out, errOut := runMigrate(t, s.URL, d.URL)
	if want := "pr-1  asleep  unchanged\npr-2  running  up\npr-3  asleep  up, slept\npr-4  running  up\n"; code != 0 || out != want {
		t.Fatalf("exit %d, err %q, got:\n%q\nwant:\n%q", code, errOut, out, want)
	}
	sameList(t, "target writes", writes(dc()), []string{
		`POST /api/deployments/pr-2/up`, `POST /api/deployments/pr-3/up`, `POST /api/deployments/pr-4/up`, `POST /api/deployments/pr-3/sleep`,
	})
	sameList(t, "source writes", writes(sc()), nil)
}

func TestMigrateSecondRunDoesNothing(t *testing.T) {
	// negative control: copy on `>=` — pr-1's equal updatedAt is PUT again; always `up` a running
	// source — the target sees `POST …/pr-1/up`.
	s, sc := routeAPI(t, map[string]answer{
		"GET /api/deployments": migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "asleep"), migRow("pr-3", 10, "down")),
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments": migList(migRow("pr-1", 10, "running"), migRow("pr-2", 20, "asleep"), migRow("pr-3", 10, "down")),
	})
	code, out, errOut := runMigrate(t, s.URL, d.URL)
	if want := "pr-1  running  unchanged\npr-2  asleep  unchanged\npr-3  down  unchanged\n"; code != 0 || out != want {
		t.Fatalf("exit %d, err %q, out %q", code, errOut, out)
	}
	sameList(t, "target writes", writes(dc()), nil)
	sameList(t, "source writes", writes(sc()), nil)
}

func TestMigrateSleepSource(t *testing.T) {
	// negative control: count `left running` as a failure — exit 1 (MF-R18); sleep the source
	// before checking the target's up — pr-4's source copy is slept although its up failed.
	shared := `{"error":"refusing to put \"x\" to sleep: kind is ` + "`shared`" + `.","stack":"x","kind":"shared"}`
	s, sc := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "running"), migRow("pr-3", 10, "running"), migRow("pr-5", 10, "asleep")),
		"POST /api/deployments/pr-1/sleep": migJob("s1"),
		"POST /api/deployments/pr-2/sleep": {409, shared},
		"POST /api/deployments/pr-3/sleep": {400, `{"error":"this spec has no compose section — there is nothing to put to sleep"}`},
		"GET /api/jobs/s1":                 migOK,
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments": migList(migRow("pr-1", 20, "running"), migRow("pr-2", 20, "running"), migRow("pr-3", 20, "running"), migRow("pr-5", 20, "asleep")),
	})
	code, out, errOut := runMigrate(t, s.URL, d.URL, "--sleep-source")
	want := "pr-1  running  source slept\npr-2  running  left running (shared)\npr-3  running  left running (no compose)\npr-5  asleep  unchanged\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d, err %q, got:\n%q\nwant:\n%q", code, errOut, out, want)
	}
	sameList(t, "source writes", writes(sc()), []string{
		`POST /api/deployments/pr-1/sleep`, `POST /api/deployments/pr-2/sleep`, `POST /api/deployments/pr-3/sleep`,
	})
	sameList(t, "target writes", writes(dc()), nil)

	// The target's up failed: the source copy keeps serving.
	s, sc = routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-4", 10, "running")),
		"GET /api/deployments/pr-4/source": migSource("pr-4"),
	})
	d, dc = routeAPI(t, map[string]answer{
		"GET /api/deployments":          migList(),
		"PUT /api/deployments/pr-4":     {200, `{}`},
		"POST /api/deployments/pr-4/up": migJob("j4"),
		"GET /api/jobs/j4":              {200, `{"job":{"id":"j4","state":"failed","error":"pull access denied"}}`},
	})
	if code, _, _ := runMigrate(t, s.URL, d.URL, "--sleep-source"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	sameList(t, "source writes after a failed up", writes(sc()), nil)
	for _, c := range append(sc(), dc()...) {
		if strings.HasSuffix(c.path, "/down") {
			t.Fatalf("down: %s", c.path)
		}
	}
}

func TestMigrateFailedUpExits1(t *testing.T) {
	// negative control: treat any terminal state as ok — pr-1 reads `copied, up`, exit 0; retry a
	// 404 job — GET /api/jobs/j2 is called more than once (MF-R11).
	s, _ := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "running")),
		"GET /api/deployments/pr-1/source": migSource("pr-1"),
		"GET /api/deployments/pr-2/source": migSource("pr-2"),
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":          migList(),
		"PUT /api/deployments/pr-1":     {200, `{}`},
		"PUT /api/deployments/pr-2":     {200, `{}`},
		"POST /api/deployments/pr-1/up": migJob("j1"),
		"POST /api/deployments/pr-2/up": migJob("j2"),
		"GET /api/jobs/j1":              {200, `{"job":{"id":"j1","state":"failed","error":"pull access denied"}}`},
	})
	code, out, _ := runMigrate(t, s.URL, d.URL)
	want := "pr-1  running  copied, failed (up: pull access denied)\npr-2  running  copied, failed (job record gone)\n"
	if code != 1 || out != want {
		t.Fatalf("exit %d, got:\n%q\nwant:\n%q", code, out, want)
	}
	n := 0
	for _, c := range dc() {
		if c.path == "/api/jobs/j2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("GET /api/jobs/j2 %d times", n)
	}
}

func TestMigrateUnknownStateIsSkipped(t *testing.T) {
	// negative control: read `running: null` as down — the line says `down  copied`, exit 0 (MF-R4).
	s, _ := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "unknown")),
		"GET /api/deployments/pr-1/source": migSource("pr-1"),
	})
	d, dc := routeAPI(t, map[string]answer{"GET /api/deployments": migList(), "PUT /api/deployments/pr-1": {200, `{}`}})
	code, out, _ := runMigrate(t, s.URL, d.URL)
	if code != 1 || out != "pr-1  unknown  skipped (docker did not answer)\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	sameList(t, "target writes", writes(dc()), nil)
}

func TestMigrateUsage(t *testing.T) {
	// negative control: read the tokens from PSTACK_TOKEN (the env remap dropped) — the two
	// missing-token cases resolve and call out; drop the Typed check — the
	// default `latest` reaches the URL check, not the usage line.
	s, sc := routeAPI(t, nil)
	d, dc := routeAPI(t, nil)
	full := []string{"migrate", "--from", s.URL, "--to", d.URL}
	for _, tc := range []struct {
		name string
		argv []string
		drop string
		says string
	}{
		{"no --from", []string{"migrate", "--to", d.URL}, "", "usage:"},
		{"no --to", []string{"migrate", "--from", s.URL}, "", "usage:"},
		{"no source token", full, "PSTACK_FROM_TOKEN", "PSTACK_FROM_TOKEN"},
		{"no target token", full, "PSTACK_TO_TOKEN", "PSTACK_TO_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"PSTACK_FROM_TOKEN": "ft", "PSTACK_TO_TOKEN": "tt", "PSTACK_TOKEN": "tok", "PSTACK_API_URL": s.URL}
			delete(env, tc.drop)
			code, _, errOut := runForward(t, env, tc.argv...)
			if code != ExitUsage || !strings.Contains(errOut, tc.says) {
				t.Fatalf("exit %d, err %q", code, errOut)
			}
		})
	}
	if n := len(sc()) + len(dc()); n != 0 {
		t.Fatalf("%d calls", n)
	}
}

func TestMigrateOnlyTheIdsGiven(t *testing.T) {
	// negative control: ignore the positional ids — pr-1 is copied too.
	s, sc := routeAPI(t, map[string]answer{
		"GET /api/deployments":             migList(migRow("pr-1", 10, "running"), migRow("pr-2", 10, "running")),
		"GET /api/deployments/pr-1/source": migSource("pr-1"),
		"GET /api/deployments/pr-2/source": migSource("pr-2"),
	})
	d, dc := routeAPI(t, map[string]answer{
		"GET /api/deployments":          migList(),
		"PUT /api/deployments/pr-1":     {200, `{}`},
		"PUT /api/deployments/pr-2":     {200, `{}`},
		"POST /api/deployments/pr-1/up": migJob("j1"),
		"POST /api/deployments/pr-2/up": migJob("j2"),
		"GET /api/jobs/j1":              migOK,
		"GET /api/jobs/j2":              migOK,
	})
	code, out, errOut := runMigrate(t, s.URL, d.URL, "pr-2")
	if code != 0 || out != "pr-2  running  copied, up\n" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	sameList(t, "target writes", writes(dc()), []string{
		`PUT /api/deployments/pr-2 {"spec":"spec-pr-2","compose":"compose-pr-2","vars":` + migVars + `}`,
		`POST /api/deployments/pr-2/up`,
	})
	for _, c := range sc() {
		if strings.Contains(c.path, "pr-1") {
			t.Fatalf("touched pr-1 on the source: %s", c.path)
		}
	}

	code, out, _ = runMigrate(t, s.URL, d.URL, "pr-9")
	if code != 1 || out != "pr-9  missing  failed (not on the source)\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

// ---- against two real servers ----

// migDocker is a fake docker that remembers which compose projects are up, so `compose ls` (the
// list's `running`) tells the truth, and logs every argv to <dir>/argv.
func migDocker(t *testing.T) (path, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "up"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
D='` + dir + `'
echo "$*" >> "$D/argv"
case "$*" in
"compose ls"*)
  printf '['; sep=''
  for f in "$D"/up/*; do [ -e "$f" ] || continue; printf '%s{"Name":"%s"}' "$sep" "${f##*/}"; sep=','; done
  echo ']' ;;
"compose -p "*" up "*) touch "$D/up/$3" ;;
"compose -p "*" down"*) rm -f "$D/up/$3" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + ":" + os.Getenv("PATH"), filepath.Join(dir, "argv")
}

// migServer is a started api server on loopback with root token tok, and its docker's argv log.
func migServer(t *testing.T, domain, tok string, setup func(*store.Store)) (base, argvLog string) {
	t.Helper()
	data := t.TempDir()
	if setup != nil {
		st, err := store.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		setup(st)
		st.Close()
	}
	path, argvLog := migDocker(t)
	s, err := api.New(api.Options{DataDir: data, Token: tok, RoutingDir: t.TempDir(), Domain: domain, Host: "127.0.0.1", Bus: events.New(),
		Log: func(string) {}, Env: exec.Merge(exec.Environ(), map[string]string{"PATH": path})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return "http://" + s.Addr().String(), argvLog
}

// migDo is one request that must answer 2xx; the body comes back.
func migDo(t *testing.T, method, url, tok, body string, hdr http.Header) []byte {
	t.Helper()
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	status, got, err := apiCall(method, url, tok, b, hdr)
	if err != nil || status < 200 || status >= 300 {
		t.Fatalf("%s %s = %d, %v: %s", method, url, status, err, got)
	}
	return got
}

// migRun POSTs a lifecycle action and waits for its job to end ok.
func migRun(t *testing.T, base, tok, id, action string) {
	t.Helper()
	var r struct{ Job struct{ ID string } }
	if err := json.Unmarshal(migDo(t, "POST", base+"/api/deployments/"+id+"/"+action, tok, "", nil), &r); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var j struct{ Job struct{ State jobs.State } }
		if err := json.Unmarshal(migDo(t, "GET", base+"/api/jobs/"+r.Job.ID, tok, "", nil), &j); err != nil {
			t.Fatal(err)
		}
		if j.Job.State == jobs.OK {
			return
		}
		if j.Job.State.Terminal() || time.Now().After(deadline) {
			t.Fatalf("%s %s: job %s", id, action, j.Job.State)
		}
	}
}

// migStates is id → state as migrate reads it.
func migStates(t *testing.T, base, tok string, hdr http.Header) map[string]string {
	t.Helper()
	var l struct{ Deployments []migrateRow }
	if err := json.Unmarshal(migDo(t, "GET", base+"/api/deployments", tok, "", hdr), &l); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range l.Deployments {
		out[r.ID] = r.state()
	}
	return out
}

func TestMigrateBetweenTwoServers(t *testing.T) {
	// negative control: drop the forwarded-by header from source calls — A forwards the list to B,
	// which is empty, and nothing migrates; issue `down` instead of the source sleep — `down -v`
	// lands in A's docker argv.
	var devTok string
	b, bArgv := migServer(t, "b.example.com", "root-b", func(st *store.Store) {
		a := auth.New(st)
		u, err := a.CreateUser("deployer", "correct-horse", auth.CreateOpts{Role: string(auth.Developer)})
		if err != nil {
			t.Fatal(err)
		}
		if devTok, _, err = a.CreateToken(u.ID, "forward"); err != nil {
			t.Fatal(err)
		}
	})
	a, aArgv := migServer(t, "a.example.com", "root-a", nil)

	spec := func(stack, kind string) string {
		b, _ := json.Marshal(map[string]any{
			"spec":    "version: 1\nstack: " + stack + "\nkind: " + kind + "\nenv:\n  PREVIEW_DOMAIN: preview.example.com\ncompose:\n  file: compose.yml\naxes: []\n",
			"compose": "services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n",
			"vars":    map[string]string{"PR": stack},
		})
		return string(b)
	}
	for id, kind := range map[string]string{"pr-run": "isolated", "pr-sleep": "isolated", "pr-down": "isolated", "pr-shared": "shared"} {
		migDo(t, "PUT", a+"/api/deployments/"+id, "root-a", spec("mig-"+id, kind), nil)
	}
	migRun(t, a, "root-a", "pr-run", "up")
	migRun(t, a, "root-a", "pr-sleep", "up")
	migRun(t, a, "root-a", "pr-shared", "up")
	migRun(t, a, "root-a", "pr-sleep", "sleep")
	self := http.Header{"X-Pstack-Forwarded-By": {"migrate"}}
	want := map[string]string{"pr-run": "running", "pr-sleep": "asleep", "pr-down": "down", "pr-shared": "running"}
	if got := migStates(t, a, "root-a", nil); !maps.Equal(got, want) {
		t.Fatalf("A before: %v", got)
	}
	// From here every deploy call to A reaches B.
	migDo(t, "PUT", a+"/api/forward", "root-a", `{"to":"`+b+`","token":"`+devTok+`"}`, nil)

	env := map[string]string{"PSTACK_FROM_TOKEN": "root-a", "PSTACK_TO_TOKEN": "root-b"}
	code, out, errOut := runForward(t, env, "migrate", "--from", a, "--to", b, "--sleep-source")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	sameList(t, "lines", lines, []string{
		"pr-down  down  copied",
		"pr-run  running  copied, up, source slept",
		"pr-shared  running  copied, up, left running (shared)",
		"pr-sleep  asleep  copied, up, slept",
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := migStates(t, b, "root-b", nil); !maps.Equal(got, want) {
		t.Fatalf("B: %v", got)
	}
	if got := migStates(t, a, "root-a", self); !maps.Equal(got, map[string]string{"pr-run": "asleep", "pr-sleep": "asleep", "pr-down": "down", "pr-shared": "running"}) {
		t.Fatalf("A after: %v", got)
	}
	// Sleep is `compose down` WITHOUT -v; a teardown is `down -v`.
	for _, f := range []string{aArgv, bArgv} {
		log, _ := os.ReadFile(f)
		if strings.Contains(string(log), "down -v") {
			t.Fatalf("a teardown ran:\n%s", log)
		}
	}

	// A second run finds nothing to do.
	code, out, errOut = runForward(t, env, "migrate", "--from", a, "--to", b)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasSuffix(l, "  unchanged") {
			t.Fatalf("second run: exit %d, err %q, out:\n%s", code, errOut, out)
		}
	}
	if code != 0 {
		t.Fatalf("second run: exit %d: %s", code, errOut)
	}
}
