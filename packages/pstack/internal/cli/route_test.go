package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// routeCall is one request the fake API saw.
type routeCall struct{ method, path, body, auth string }

// routeAPI is a fake /api/custom-routes: each "METHOD path" answers the status and body given.
// Anything else is a 404, so a wrong path fails the test rather than passing on a default.
func routeAPI(t *testing.T, answers map[string]struct {
	status int
	body   string
}) (*httptest.Server, func() []routeCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []routeCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, routeCall{r.Method, r.URL.EscapedPath(), string(b), r.Header.Get("Authorization")})
		mu.Unlock()
		a, ok := answers[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no fake for ` + r.Method + " " + r.URL.EscapedPath() + `"}`))
			return
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []routeCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]routeCall{}, calls...)
	}
}

type answer = struct {
	status int
	body   string
}

func runRoute(t *testing.T, base string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"PSTACK_API_URL": base, "PSTACK_TOKEN": "tok"}
	code := Run(argv, IO{Stdout: &out, Stderr: &errOut, Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	return code, out.String(), errOut.String()
}

const (
	assignedRow   = `{"name":"checkout","deployment":"pr-123","stack":"myapp-pr-123","hosts":["web-checkout.preview.example.com","api-checkout.preview.example.com"],"createdAt":1,"updatedAt":2}`
	unassignedRow = `{"name":"spare","deployment":null,"stack":null,"hosts":[],"createdAt":1,"updatedAt":1}`
)

func TestRouteLsPrintsOneLinePerRoute(t *testing.T) {
	// negative control: print the deployment id instead of the stack (`if r.Stack != nil` → false)
	// — the first line reads `checkout  pr-123  …` and this fails.
	srv, calls := routeAPI(t, map[string]answer{"GET /api/custom-routes": {200, "[" + assignedRow + "," + unassignedRow + "]"}})
	code, out, errOut := runRoute(t, srv.URL, "route", "ls")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := "checkout  myapp-pr-123  web-checkout.preview.example.com, api-checkout.preview.example.com\nspare  (unassigned)\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
	if c := calls(); len(c) != 1 || c[0].auth != "Bearer tok" {
		t.Fatalf("calls: %+v", c)
	}
}

func TestRouteJSONPrintsTheAPIBody(t *testing.T) {
	// negative control: ignore --json — the line format is printed and the body never appears.
	body := "[" + assignedRow + "]"
	srv, _ := routeAPI(t, map[string]answer{"GET /api/custom-routes": {200, body}})
	code, out, _ := runRoute(t, srv.URL, "route", "ls", "--json")
	if code != 0 || strings.TrimSpace(out) != body {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestRouteWritesSendTheBodyTheAPIDocuments(t *testing.T) {
	// negative control: one per case below, each run.
	for _, tc := range []struct {
		name     string
		argv     []string
		method   string
		path     string
		wantBody string
		status   int
		answer   string
		wantOut  string
	}{
		// negative control: drop omitempty from routeBody.Deployment — an unnamed one goes out as
		// "" and the body comparison fails.
		{"create unassigned", []string{"route", "create", "spare"}, "POST", "/api/custom-routes",
			`{"name":"spare"}`, 201, unassignedRow, "spare  (unassigned)\n"},
		// negative control: drop the deployment from the create body — the route is created
		// unassigned and the body comparison fails.
		{"create assigned", []string{"route", "create", "checkout", "pr-123"}, "POST", "/api/custom-routes",
			`{"name":"checkout","deployment":"pr-123"}`, 201, assignedRow, "checkout  myapp-pr-123  web-checkout.preview.example.com, api-checkout.preview.example.com\n"},
		// negative control: drop Force from the assign body — the move becomes a held refusal on
		// a real server, and here the body comparison fails.
		{"assign forced", []string{"route", "assign", "checkout", "pr-123", "--force"}, "PUT", "/api/custom-routes/checkout/assignment",
			`{"deployment":"pr-123","force":true}`, 200, assignedRow, "checkout  myapp-pr-123  web-checkout.preview.example.com, api-checkout.preview.example.com\n"},
		// negative control: send unassign as DELETE /api/custom-routes/{name} — that deletes the
		// route, and the path comparison fails.
		{"unassign", []string{"route", "unassign", "spare"}, "DELETE", "/api/custom-routes/spare/assignment",
			``, 200, unassignedRow, "spare  (unassigned)\n"},
		// negative control: print the raw body for rm — the output is `{"deleted":"spare"}`.
		{"rm", []string{"route", "rm", "spare"}, "DELETE", "/api/custom-routes/spare",
			``, 200, `{"deleted":"spare"}`, "spare  deleted\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := routeAPI(t, map[string]answer{tc.method + " " + tc.path: {tc.status, tc.answer}})
			code, out, errOut := runRoute(t, srv.URL, tc.argv...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if out != tc.wantOut {
				t.Errorf("out %q, want %q", out, tc.wantOut)
			}
			c := calls()
			if len(c) != 1 || c[0].body != tc.wantBody {
				t.Fatalf("calls: %+v, want body %q", c, tc.wantBody)
			}
		})
	}
}

func TestRouteHeldNamesTheHolderAndForce(t *testing.T) {
	// negative control: print apiError's line for every 409 — the message loses "(--force moves
	// it)" and this fails.
	held := `{"error":"checkout is assigned to myapp-pr-122","assignedTo":{"deployment":"pr-122","stack":"myapp-pr-122"}}`
	srv, _ := routeAPI(t, map[string]answer{"PUT /api/custom-routes/checkout/assignment": {409, held}})
	code, out, errOut := runRoute(t, srv.URL, "route", "assign", "checkout", "pr-123")
	if code != ExitFailed || out != "" {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if errOut != "pstack: checkout is assigned to myapp-pr-122 (--force moves it)\n" {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestRouteOtherErrorsAreTheAPIsLine(t *testing.T) {
	// negative control: treat any 409 as held — a host collision would claim `--force` moves it,
	// which it does not.
	srv, _ := routeAPI(t, map[string]answer{"PUT /api/custom-routes/checkout/assignment": {409, `{"error":"web-checkout.preview.example.com is served by other","host":"web-checkout.preview.example.com"}`}})
	code, _, errOut := runRoute(t, srv.URL, "route", "assign", "checkout", "pr-123", "--force")
	if code != ExitFailed || errOut != "pstack: web-checkout.preview.example.com is served by other\n" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestRouteUsageErrorsExit3BeforeAnyRequest(t *testing.T) {
	// negative control: drop `name == ""` from the ls case — `route ls extra` sends a request and
	// exits 0.
	srv, calls := routeAPI(t, map[string]answer{})
	for _, argv := range [][]string{
		{"route"},
		{"route", "nope"},
		{"route", "create"},
		{"route", "assign", "checkout"},
		{"route", "unassign"},
		{"route", "rm"},
		{"route", "ls", "extra"},
		{"route", "rm", "a", "b"},
	} {
		code, _, errOut := runRoute(t, srv.URL, argv...)
		if code != ExitUsage || !strings.Contains(errOut, "usage: pstack route") {
			t.Errorf("%v: exit %d, stderr %q", argv, code, errOut)
		}
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("usage errors sent requests: %+v", c)
	}
}

func TestRouteRefusesWithoutAnAPIURL(t *testing.T) {
	// negative control: default PSTACK_API_URL to loopback — this exits 1 on a refused connection
	// instead of 3 with the sentence.
	var out, errOut bytes.Buffer
	code := Run([]string{"route", "ls"}, IO{Stdout: &out, Stderr: &errOut, Env: func(string) (string, bool) { return "", false }})
	if code != ExitUsage || !strings.Contains(errOut.String(), "set PSTACK_API_URL") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	// A route is not /api/config: the token sentence must not claim the root token is needed.
	// negative control: return apiBase's exit unchanged — the message says "root-token only".
	env := map[string]string{"PSTACK_API_URL": "http://127.0.0.1:1"}
	errOut.Reset()
	code = Run([]string{"route", "ls"}, IO{Stdout: &out, Stderr: &errOut, Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	if code != ExitUsage || strings.Contains(errOut.String(), "root") || !strings.Contains(errOut.String(), "PSTACK_TOKEN") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}
