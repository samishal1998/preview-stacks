package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runForward is runRoute with the environment in the test's hands, so a case can leave a variable out.
func runForward(t *testing.T, env map[string]string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(argv, IO{Stdout: &out, Stderr: &errOut, Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	return code, out.String(), errOut.String()
}

func fwEnv(base string) map[string]string {
	return map[string]string{"PSTACK_API_URL": base, "PSTACK_TOKEN": "tok", "PSTACK_FORWARD_TOKEN": "ft"}
}

func TestForwardShow(t *testing.T) {
	// negative control: print `ago` from establishedAt instead of lastOkAt — the row line reads
	// `last ok 2h ago` and this fails.
	srv, calls := routeAPI(t, map[string]answer{"GET /api/forward": {200, "null"}})
	code, out, errOut := runForward(t, fwEnv(srv.URL), "forward")
	if code != 0 || out != "Not forwarding.\n" {
		t.Fatalf("null: exit %d, out %q, err %q", code, out, errOut)
	}
	if c := calls(); len(c) != 1 || c[0].auth != "Bearer tok" {
		t.Fatalf("calls: %+v", c)
	}

	now := time.Now().UnixMilli()
	row := `{"to":"https://api.b","via":"10.0.0.2","establishedAt":` + strconv.FormatInt(now-2*3600_000, 10) + `,"lastOkAt":` + strconv.FormatInt(now-3*60_000, 10) + `}`
	srv, _ = routeAPI(t, map[string]answer{"GET /api/forward": {200, row}})
	code, out, errOut = runForward(t, fwEnv(srv.URL), "forward")
	if code != 0 || out != "Forwarding to https://api.b · last ok 3m ago\n" {
		t.Fatalf("row: exit %d, out %q, err %q", code, out, errOut)
	}
}

func TestForwardAgo(t *testing.T) {
	// negative control: round instead of floor — 119 s reads `2m ago` and this fails.
	for _, tc := range []struct {
		s    int64
		want string
	}{{0, "just now"}, {59, "just now"}, {60, "1m ago"}, {119, "1m ago"}, {3600, "1h ago"}, {86400, "1d ago"}, {-5, "just now"}} {
		if got := ago(1_000_000-tc.s*1000, 1_000_000); got != tc.want {
			t.Errorf("%ds: got %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestForwardSetSendsTheEnvToken(t *testing.T) {
	// negative control: send the token as `PSTACK_TOKEN` (the caller's) — the body carries `tok`
	// and this fails; print the PUT body raw — `ft` reaches stdout and this fails.
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	srv, calls := routeAPI(t, map[string]answer{"PUT /api/forward": {200, `{"to":"https://api.b","via":"10.0.0.2","establishedAt":` + now + `,"lastOkAt":` + now + `,"token":"ft"}`}})
	code, out, errOut := runForward(t, fwEnv(srv.URL), "forward", "set", "https://api.b", "--via", "10.0.0.2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := calls()
	if len(c) != 1 || c[0].method != "PUT" || c[0].body != `{"to":"https://api.b","via":"10.0.0.2","token":"ft"}` {
		t.Fatalf("calls: %+v", c)
	}
	if out != "Forwarding to https://api.b · last ok just now\n" {
		t.Fatalf("out %q", out)
	}
	if strings.Contains(out, "ft") || strings.Contains(errOut, "ft") {
		t.Fatalf("token printed: out %q err %q", out, errOut)
	}
}

func TestForwardUsageExits3BeforeAnyRequest(t *testing.T) {
	// negative control: read PSTACK_FORWARD_TOKEN after the request is built and send an empty
	// one — the no-token case reaches the fake and this fails.
	srv, calls := routeAPI(t, map[string]answer{})
	noToken := fwEnv(srv.URL)
	delete(noToken, "PSTACK_FORWARD_TOKEN")
	for _, tc := range []struct {
		name string
		env  map[string]string
		argv []string
	}{
		{"set without a URL", fwEnv(srv.URL), []string{"forward", "set"}},
		{"set without the token", noToken, []string{"forward", "set", "https://api.b"}},
		{"unknown sub", fwEnv(srv.URL), []string{"forward", "bogus"}},
		{"--via without set", fwEnv(srv.URL), []string{"forward", "--via", "10.0.0.2"}},
	} {
		if code, _, errOut := runForward(t, tc.env, tc.argv...); code != 3 || errOut == "" {
			t.Errorf("%s: exit %d, err %q", tc.name, code, errOut)
		}
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("calls: %+v", c)
	}
}

func TestForwardOff(t *testing.T) {
	// negative control: send off as PUT {} — the fake 404s and this fails.
	srv, calls := routeAPI(t, map[string]answer{"DELETE /api/forward": {200, `{"deleted":true}`}})
	code, out, errOut := runForward(t, fwEnv(srv.URL), "forward", "off")
	if code != 0 || out != "Not forwarding.\n" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	if c := calls(); len(c) != 1 || c[0].method != "DELETE" {
		t.Fatalf("calls: %+v", c)
	}
}
