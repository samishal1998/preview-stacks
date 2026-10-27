package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

const crCompose = "services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n"

func crSpec(stack string) string {
	return "version: 1\nstack: " + stack + "\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web, c, b-c]\naxes: []\n"
}

// crServer is a real New() server holding deployments pr-1 (stack myapp-pr-1) and pr-2 (myapp-pr-2),
// each with one generated `web` host. docker is a fake that answers every command empty.
func crServer(t *testing.T) (*Server, *exec.Fake) {
	t.Helper()
	// Jobs run a docker that answers ok: PATH goes in before New, never onto a live server.
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {},
		Env: exec.Merge(exec.Environ(), map[string]string{"PATH": fakeDockerPath(t)})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f := exec.NewFake(nil, "")
	s.host = f
	crDeploy(t, s, "pr-1", "myapp-pr-1", crCompose)
	crDeploy(t, s, "pr-2", "myapp-pr-2", crCompose)
	return s, f
}

func crDeploy(t *testing.T, s *Server, id, stack, compose string) *registry.Deployment {
	t.Helper()
	dep, err := s.registry.Put(id, crSpec(stack), registry.PutOptions{ComposeYaml: &compose})
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

func crCall(t *testing.T, s *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: body is not a JSON object: %v\n%s", method, path, err, rec.Body.String())
	}
	return rec.Code, out
}

// crList is GET /api/custom-routes, a bare array.
func crList(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/custom-routes", nil)
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	var out []map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("GET /api/custom-routes = %d: %s", rec.Code, rec.Body.String())
	}
	return out
}

func crExpect(t *testing.T, s *Server, method, path, body string, want int) map[string]any {
	t.Helper()
	code, out := crCall(t, s, method, path, body)
	if code != want {
		t.Fatalf("%s %s %s = %d, want %d: %v", method, path, body, code, want, out)
	}
	return out
}

// crEvents records the route.* events, as `name {json data}`.
func crEvents(t *testing.T, s *Server) *[]string {
	t.Helper()
	seen := []string{}
	off := s.bus.On(func(e events.Event) {
		if strings.HasPrefix(e.Event, "route.") {
			seen = append(seen, e.Event+" "+string(e.Data))
		}
	})
	t.Cleanup(off)
	return &seen
}

func crFile(t *testing.T, s *Server) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(s.routing.Dir, routing.CustomRoutesYAML))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func crHosts(v any) []string {
	out := []string{}
	for _, h := range v.([]any) {
		out = append(out, h.(string))
	}
	return out
}

func TestCustomRouteCreateListAssign(t *testing.T) {
	s, _ := crServer(t)
	seen := crEvents(t, s)

	// negative control: answer 200 instead of 201 → this fails.
	made := crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout"}`, 201)
	if made["name"] != "checkout" || made["deployment"] != nil || made["stack"] != nil || len(crHosts(made["hosts"])) != 0 {
		t.Fatalf("unassigned route = %v", made)
	}
	if _, ok := made["createdAt"].(float64); !ok {
		t.Fatalf("createdAt = %v", made["createdAt"])
	}
	if crFile(t, s) != "" || len(*seen) != 0 {
		t.Fatalf("an unassigned route wrote %q / emitted %v", crFile(t, s), *seen)
	}

	got := crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-1"}`, 200)
	if got["deployment"] != "pr-1" || got["stack"] != "myapp-pr-1" ||
		!slices.Equal(crHosts(got["hosts"]), []string{"web-checkout.preview.example.com"}) {
		t.Fatalf("assigned route = %v", got)
	}
	// negative control: skip renderCustomRoutes after the commit → the file is empty and this fails.
	file := crFile(t, s)
	if !strings.Contains(file, "pstack-route-checkout-web:") || !strings.Contains(file, `"web-myapp-pr-1@docker"`) {
		t.Fatalf("file:\n%s", file)
	}
	if !slices.Equal(*seen, []string{`route.assigned {"route":"checkout","deployment":"pr-1","stack":"myapp-pr-1"}`}) {
		t.Fatalf("events %v", *seen)
	}

	// The same deployment again is a no-op: 200, no event.
	// negative control: drop the same-deployment early return → 409, held by itself.
	crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-1"}`, 200)
	if len(*seen) != 1 {
		t.Fatalf("a no-op assign emitted %v", *seen)
	}

	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"alpha"}`, 201)
	// negative control: wrap the list in { routes } → crList cannot decode it and this fails.
	rows := crList(t, s)
	if len(rows) != 2 || rows[0]["name"] != "alpha" || rows[1]["stack"] != "myapp-pr-1" {
		t.Fatalf("list = %v", rows)
	}
}

func TestCustomRouteCreateAssigned(t *testing.T) {
	s, _ := crServer(t)
	seen := crEvents(t, s)
	// negative control: ignore `deployment` on POST → deployment is null and this fails.
	got := crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-2","force":true}`, 201)
	// A null optional field is an absent one.
	// negative control: drop the `v != nil` guard on deployment → 400 and this fails.
	if n := crExpect(t, s, "POST", "/api/custom-routes", `{"name":"cart","deployment":null,"force":null}`, 201); n["deployment"] != nil {
		t.Fatalf("null deployment = %v", n)
	}
	if got["deployment"] != "pr-2" || got["stack"] != "myapp-pr-2" {
		t.Fatalf("route = %v", got)
	}
	if !strings.Contains(crFile(t, s), `"web-myapp-pr-2@docker"`) || len(*seen) != 1 {
		t.Fatalf("file %q events %v", crFile(t, s), *seen)
	}
	// POST never moves, force or not.
	// negative control: on an existing name, AssignCustomRoute(force) and answer 201 → this fails.
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1","force":true}`, 409)
}

func TestCustomRouteHeldThenForcedMove(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	seen := crEvents(t, s)

	// negative control: drop the held check (`row.DeploymentID != "" && !force`) → the store refuses
	// unrouted, a 500, and this fails.
	held := crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-2"}`, 409)
	to, _ := held["assignedTo"].(map[string]any)
	if to["deployment"] != "pr-1" || to["stack"] != "myapp-pr-1" || held["error"] != "checkout is assigned to myapp-pr-1" {
		t.Fatalf("held = %v", held)
	}
	if !strings.Contains(crFile(t, s), "web-myapp-pr-1@docker") || len(*seen) != 0 {
		t.Fatalf("a refused assign changed something: %q %v", crFile(t, s), *seen)
	}

	// One call moves it: the file shows only the new target, and one event carries `previous`.
	moved := crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-2","force":true}`, 200)
	if moved["deployment"] != "pr-2" {
		t.Fatalf("moved = %v", moved)
	}
	file := crFile(t, s)
	if !strings.Contains(file, "web-myapp-pr-2@docker") || strings.Contains(file, "myapp-pr-1") {
		t.Fatalf("file after the move:\n%s", file)
	}
	// negative control: emit without `previous` → this fails.
	if !slices.Equal(*seen, []string{`route.assigned {"route":"checkout","deployment":"pr-2","stack":"myapp-pr-2","previous":"pr-1"}`}) {
		t.Fatalf("events %v", *seen)
	}
	row, _, _ := store.GetCustomRoute(s.store.DB, "checkout")
	if row.DeploymentID != "pr-2" {
		t.Fatalf("row = %+v", row)
	}
}

func TestCustomRouteNames(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"taken"}`, 201)
	cases := []struct {
		body string
		want int
	}{
		// negative control: drop the regex check → "Bad_Name" is 201 and this fails.
		{`{"name":"Bad_Name"}`, 400},
		{`{"name":"-x"}`, 400},
		{`{"name":"x-"}`, 400},
		{`{"name":"` + strings.Repeat("a", 41) + `"}`, 400},
		{`{"name":""}`, 400},
		{`{}`, 400},
		{`[]`, 400},
		{`{"name":"checkout","deployment":7}`, 400},
		// negative control: drop the reserved check → "grafana" is 201 and this fails.
		{`{"name":"grafana"}`, 400},
		{`{"name":"control"}`, 400},
		// negative control: drop the stack-name check → "myapp-pr-1" is 201 and this fails.
		{`{"name":"myapp-pr-1"}`, 409},
		// negative control: drop the exists check → the store refuses unrouted, a 500.
		{`{"name":"taken"}`, 409},
		{`{"name":"checkout","deployment":"pr-404"}`, 404},
	}
	for _, c := range cases {
		code, out := crCall(t, s, "POST", "/api/custom-routes", c.body)
		if code != c.want {
			t.Errorf("POST %s = %d, want %d: %v", c.body, code, c.want, out)
		}
	}
	// Forty characters is the longest label the regex admits.
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"`+strings.Repeat("a", 40)+`"}`, 201)
	if _, ok, _ := store.GetCustomRoute(s.store.DB, "checkout"); ok {
		t.Fatal("a refused create stored a row")
	}
}

func TestCustomRouteUnknownsAndBadBodies(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout"}`, 201)
	cases := []struct {
		method, path, body string
		want               int
	}{
		// negative control: skip the deployment lookup → the assign stores pr-404 and answers 200.
		{"PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-404"}`, 404},
		{"PUT", "/api/custom-routes/nope/assignment", `{"deployment":"pr-1"}`, 404},
		{"DELETE", "/api/custom-routes/nope/assignment", "", 404},
		{"DELETE", "/api/custom-routes/nope", "", 404},
		{"PUT", "/api/custom-routes/checkout/assignment", `{}`, 400},
		{"PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-1","force":"yes"}`, 400},
		{"PUT", "/api/custom-routes/checkout/assignment", `{"deployment":null}`, 400},
		{"GET", "/api/custom-routes/checkout", "", 405},
		{"POST", "/api/custom-routes/checkout/assignment", "", 405},
	}
	for _, c := range cases {
		code, out := crCall(t, s, c.method, c.path, c.body)
		if code != c.want {
			t.Errorf("%s %s %s = %d, want %d: %v", c.method, c.path, c.body, code, c.want, out)
		}
	}
}

func TestCustomRouteUnassignAndDelete(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"cart","deployment":"pr-2"}`, 201)
	seen := crEvents(t, s)

	got := crExpect(t, s, "DELETE", "/api/custom-routes/checkout/assignment", "", 200)
	if got["deployment"] != nil || got["stack"] != nil || len(crHosts(got["hosts"])) != 0 {
		t.Fatalf("unassigned = %v", got)
	}
	// negative control: skip the render on unassign → checkout's router stays and this fails.
	if file := crFile(t, s); strings.Contains(file, "checkout") || !strings.Contains(file, "pstack-route-cart-web") {
		t.Fatalf("file after unassign:\n%s", file)
	}
	// Unassigning an unassigned route is allowed and says nothing.
	crExpect(t, s, "DELETE", "/api/custom-routes/checkout/assignment", "", 200)

	crExpect(t, s, "DELETE", "/api/custom-routes/cart", "", 200)
	crExpect(t, s, "DELETE", "/api/custom-routes/checkout", "", 200)
	// negative control: render "" without removing → the file stays and this fails.
	if crFile(t, s) != "" {
		t.Fatalf("no route is assigned, yet the file is:\n%s", crFile(t, s))
	}
	want := []string{
		`route.unassigned {"route":"checkout","deployment":"pr-1","stack":"myapp-pr-1","reason":"unassigned"}`,
		`route.unassigned {"route":"cart","deployment":"pr-2","stack":"myapp-pr-2","reason":"deleted"}`,
	}
	if !slices.Equal(*seen, want) {
		t.Fatalf("events:\n got %v\nwant %v", *seen, want)
	}
	if rows := crList(t, s); len(rows) != 0 {
		t.Fatalf("list = %v", rows)
	}
}

func TestCustomRouteCollisions(t *testing.T) {
	t.Run("a host another deployment serves", func(t *testing.T) {
		// negative control: drop the registry hosts from the taken set → 200 and this fails.
		s, _ := crServer(t)
		crDeploy(t, s, "pr-3", "other", "services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: \"80\", pstack.routing.host: web-checkout.preview.example.com}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout"}`, 201)
		out := crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-1"}`, 409)
		if out["host"] != "web-checkout.preview.example.com" || out["error"] != "web-checkout.preview.example.com is served by other" {
			t.Fatalf("409 = %v", out)
		}
		// The create-and-assign path runs the same check, and stores nothing when it refuses.
		crExpect(t, s, "DELETE", "/api/custom-routes/checkout", "", 200)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 409)
		if _, ok, _ := store.GetCustomRoute(s.store.DB, "checkout"); ok {
			t.Fatal("a refused create stored a row")
		}
	})

	t.Run("a router name another route renders", func(t *testing.T) {
		// Route a-b on service c and route a on service b-c both name pstack-route-a-b-c.
		// negative control: drop the router-name check → 201 and this fails.
		s, _ := crServer(t)
		crDeploy(t, s, "pr-c", "stack-c", "services:\n  c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crDeploy(t, s, "pr-bc", "stack-bc", "services:\n  b-c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a-b","deployment":"pr-c"}`, 201)
		out := crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a","deployment":"pr-bc"}`, 409)
		if out["router"] != "pstack-route-a-b-c" {
			t.Fatalf("409 = %v", out)
		}
	})

	t.Run("a host a live router serves", func(t *testing.T) {
		// A hand-labelled container is invisible to the registry computation.
		// negative control: skip the live routers → 200 and this fails.
		s, f := crServer(t)
		f.Answer = func(cmd string) (exec.Result, bool) {
			switch strings.TrimSpace(cmd) {
			case "docker ps -aq --filter 'label=traefik.enable=true'":
				return exec.Result{OK: true, Stdout: "c1\n"}, true
			case "docker inspect 'c1'":
				return exec.Result{OK: true, Stdout: `[{"Id":"c1","Name":"/legacy","Config":{"Labels":{"traefik.enable":"true","traefik.http.routers.legacy.rule":"Host(` + "`web-checkout.preview.example.com`" + `)"}}}]`}, true
			}
			return exec.Result{}, false
		}
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout"}`, 201)
		out := crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-1"}`, 409)
		if out["host"] != "web-checkout.preview.example.com" {
			t.Fatalf("409 = %v", out)
		}
	})

	// Assign takes the other routes' hosts and routers from what the last render served (S2-R5).
	t.Run("a host another route lost names its winner", func(t *testing.T) {
		// Route b-c on service a and route c on service a-b both give a-b-c.<domain>.
		// negative control (applied): take the other routes from the unfiltered routeTarget(row) → the
		// 409 says "served by route b-c" and this fails.
		s, _ := crServer(t)
		crDeploy(t, s, "pr-a", "stack-a", "services:\n  a:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crDeploy(t, s, "pr-ab", "stack-ab", "services:\n  a-b:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"b-c","deployment":"pr-a"}`, 201)
		crPut(t, s, "pr-3", "holder", "services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: \"80\", pstack.routing.host: a-b-c.preview.example.com}\n")
		out := crExpect(t, s, "POST", "/api/custom-routes", `{"name":"c","deployment":"pr-ab"}`, 409)
		if out["error"] != "a-b-c.preview.example.com is served by holder" {
			t.Fatalf("409 = %v", out)
		}
	})

	t.Run("a router another route does not render is free", func(t *testing.T) {
		// Route a-b on service c and route a on service b-c both name pstack-route-a-b-c; a-b lost
		// its only host, so it renders no router.
		// negative control (applied): take the other routes from the unfiltered routeTarget(row) → 409
		// "router pstack-route-a-b-c is taken by route a-b" and this fails.
		s, _ := crServer(t)
		crDeploy(t, s, "pr-c", "stack-c", "services:\n  c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crDeploy(t, s, "pr-bc", "stack-bc", "services:\n  b-c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a-b","deployment":"pr-c"}`, 201)
		crPut(t, s, "pr-3", "holder", "services:\n  web:\n    image: nginx\n    labels: {pstack.routing.port: \"80\", pstack.routing.host: c-a-b.preview.example.com}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a","deployment":"pr-bc"}`, 201)
		if got := crServes(t, s, "a"); got != `[["b-c-a.preview.example.com"],[]]` {
			t.Fatalf("a serves %s", got)
		}
		if file := crFile(t, s); !strings.Contains(file, "Host(`b-c-a.preview.example.com`)") {
			t.Fatalf("file:\n%s", file)
		}
	})

	t.Run("a route's own hosts never collide with themselves", func(t *testing.T) {
		// negative control: include the route being assigned in the taken set → the move is 409.
		s, _ := crServer(t)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
		crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-2","force":true}`, 200)
	})
}

func TestCustomRouteRenderIsTotal(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	dep, _ := s.registry.Get("pr-1")
	if err := os.Remove(filepath.Join(dep.Dir, "compose.yml")); err != nil {
		t.Fatal(err)
	}
	// negative control: return on the first unreadable target → cart's router is missing and this fails.
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"cart","deployment":"pr-2"}`, 201)
	if file := crFile(t, s); !strings.Contains(file, "pstack-route-cart-web") || strings.Contains(file, "checkout") {
		t.Fatalf("file:\n%s", file)
	}
	// The unreadable one still lists, with no hosts.
	row := crList(t, s)[1]
	if row["name"] != "checkout" || row["deployment"] != "pr-1" || len(crHosts(row["hosts"])) != 0 {
		t.Fatalf("row = %v", row)
	}
}

// crPut registers a deployment through PUT /api/deployments/:id, the path an operator takes.
func crPut(t *testing.T, s *Server, id, stack, compose string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"spec": crSpec(stack), "compose": compose})
	if code, out := crCall(t, s, "PUT", "/api/deployments/"+id, string(body)); code != 200 && code != 201 {
		t.Fatalf("PUT %s = %d: %v", id, code, out)
	}
}

// crRoute is GET's row for route name.
func crRoute(t *testing.T, s *Server, name string) map[string]any {
	t.Helper()
	for _, r := range crList(t, s) {
		if r["name"] == name {
			return r
		}
	}
	t.Fatalf("no route %s", name)
	return nil
}

// crServes is `hosts | blocked` of route name, as JSON.
func crServes(t *testing.T, s *Server, name string) string {
	t.Helper()
	r := crRoute(t, s, name)
	b, _ := json.Marshal([]any{r["hosts"], r["blocked"]})
	return string(b)
}

const crTwo = crCompose + "  c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n"

func TestCustomRouteLaterCollisions(t *testing.T) {
	const dom = ".preview.example.com"
	t.Run("a deploy that adds a colliding service", func(t *testing.T) {
		// negative control: seed the render's claims with nothing from the deployments → the route
		// still serves web-checkout and this fails.
		s, _ := crServer(t)
		crPut(t, s, "pr-1", "myapp-pr-1", crTwo)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
		crPut(t, s, "pr-2", "myapp-pr-2", crCompose+"  shop:\n    image: nginx\n    labels: {pstack.routing.port: \"80\", pstack.routing.host: web-checkout"+dom+"}\n")
		want := `[["c-checkout` + dom + `"],[{"by":{"kind":"deployment","name":"myapp-pr-2"},"host":"web-checkout` + dom + `"}]]`
		if got := crServes(t, s, "checkout"); got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
		file := crFile(t, s)
		if strings.Contains(file, "pstack-route-checkout-web:") || !strings.Contains(file, "pstack-route-checkout-c:") {
			t.Fatalf("file:\n%s", file)
		}
	})

	t.Run("a stack later named like a route, until it is deleted", func(t *testing.T) {
		// negative control: drop the render from putDeployment → the route keeps web-checkout and this fails.
		s, _ := crServer(t)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
		crPut(t, s, "pr-3", "checkout", crCompose)
		want := `[[],[{"by":{"kind":"deployment","name":"checkout"},"host":"web-checkout` + dom + `"}]]`
		if got := crServes(t, s, "checkout"); got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
		// Its only host is blocked: nothing to render, so no file.
		if file := crFile(t, s); file != "" {
			t.Fatalf("file:\n%s", file)
		}
		// negative control: drop the any-assigned render from removeDeployment → still blocked and this fails.
		crExpect(t, s, "DELETE", "/api/deployments/pr-3", "", 200)
		if got := crServes(t, s, "checkout"); got != `[["web-checkout`+dom+`"],[]]` {
			t.Fatalf("after the stack went: %s", got)
		}
		if !strings.Contains(crFile(t, s), "pstack-route-checkout-web:") {
			t.Fatalf("file:\n%s", crFile(t, s))
		}
	})

	t.Run("two routes after a redeploy: the earlier wins, until it is unassigned", func(t *testing.T) {
		// Route a-b on service c and route a on service b-c both name pstack-route-a-b-c. `a` sorts
		// first by name; a-b was assigned first.
		// negative control: drop the updated_at sort → the walk goes by name, a wins and this fails.
		s, _ := crServer(t)
		crDeploy(t, s, "pr-c", "stack-c", "services:\n  c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a-b","deployment":"pr-c"}`, 201)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"a","deployment":"pr-1"}`, 201)
		// Distinct milliseconds, so the order is the claim order and not the name tie-break.
		if _, err := s.store.DB.Exec("UPDATE custom_routes SET updated_at = CASE name WHEN 'a-b' THEN 1000 ELSE 2000 END"); err != nil {
			t.Fatal(err)
		}
		crPut(t, s, "pr-1", "myapp-pr-1", crCompose+"  b-c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
		want := `[["web-a` + dom + `"],[{"by":{"kind":"route","name":"a-b"},"host":"b-c-a` + dom + `"}]]`
		if got := crServes(t, s, "a"); got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
		if got := crServes(t, s, "a-b"); got != `[["c-a-b`+dom+`"],[]]` {
			t.Fatalf("the winner: %s", got)
		}
		file := crFile(t, s)
		if !strings.Contains(file, "Host(`c-a-b"+dom+"`)") || strings.Contains(file, "b-c-a"+dom) || strings.Contains(file, "# dropped") {
			t.Fatalf("file:\n%s", file)
		}
		// negative control: skip the render on unassign → a-b still holds the name and this fails.
		crExpect(t, s, "DELETE", "/api/custom-routes/a-b/assignment", "", 200)
		if got := crServes(t, s, "a"); got != `[["web-a`+dom+`","b-c-a`+dom+`"],[]]` {
			t.Fatalf("after the winner went: %s", got)
		}
		if !strings.Contains(crFile(t, s), "Host(`b-c-a"+dom+"`)") {
			t.Fatalf("file:\n%s", crFile(t, s))
		}
	})

	t.Run("a blocked host never wakes the loser", func(t *testing.T) {
		// Stack checkout (a-win) serves web-checkout itself; route checkout on pr-1 is blocked there.
		// negative control: pass the unfiltered target to appendWake → Find answers pr-1 and this fails.
		s, _ := crServer(t)
		crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
		crPut(t, s, "a-win", "checkout", crCompose)
		for _, id := range []string{"a-win", "pr-1"} {
			host := "web-" + map[string]string{"a-win": "checkout", "pr-1": "myapp-pr-1"}[id] + dom
			if err := s.registry.SetSleep(id, &registry.SleepRecord{Since: 1, Reason: "test", Hosts: []string{host}, Rules: []string{}}); err != nil {
				t.Fatal(err)
			}
		}
		s.reindex()
		if got := s.sleepIndex.Find("web-checkout" + dom); got != "a-win" {
			t.Fatalf("Find = %q, want a-win", got)
		}
	})
}

func TestCustomRouteListShape(t *testing.T) {
	// negative control: leave blocked out of routeJSON → the key is missing and this fails.
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"spare"}`, 201)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/custom-routes", nil)
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	var compact bytes.Buffer
	if err := json.Compact(&compact, rec.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	body := compact.String()
	for _, want := range []string{
		`"name":"checkout","deployment":"pr-1","stack":"myapp-pr-1","hosts":["web-checkout.preview.example.com"],"blocked":[],"createdAt":`,
		`"name":"spare","deployment":null,"stack":null,"hosts":[],"blocked":[],"createdAt":`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("want %s in\n%s", want, body)
		}
	}
}
