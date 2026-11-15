package api

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func TestCustomRouteSubdomains(t *testing.T) {
	s, _ := crServer(t)
	seen := crEvents(t, s)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	*seen = nil
	if r := crRoute(t, s, "checkout"); r["subdomains"] != nil {
		t.Fatalf("a new route has subdomains %v", r["subdomains"])
	}

	// negative control: skip s.rendered() in the handler → no -wild router in the file and this fails.
	got := crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":"one"}`, 200)
	if got["subdomains"] != "one" || !slices.Equal(crHosts(got["hosts"]),
		[]string{"web-checkout.preview.example.com", "*.web-checkout.preview.example.com"}) {
		t.Fatalf("route = %v", got)
	}
	if file := crFile(t, s); !strings.Contains(file, "pstack-route-checkout-web-wild:") {
		t.Fatalf("file:\n%s", file)
	}
	if len(*seen) != 0 {
		t.Fatalf("a subdomains change emitted %v", *seen)
	}

	got = crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":null}`, 200)
	if got["subdomains"] != nil || len(crHosts(got["hosts"])) != 1 || strings.Contains(crFile(t, s), "-wild") {
		t.Fatalf("off: %v\n%s", got, crFile(t, s))
	}

	// "off" is null's spelling for the generated CLI's --depth.
	crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":"one"}`, 200)
	if r := crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":"off"}`, 200); r["subdomains"] != nil {
		t.Fatalf("off = %v", r)
	}
	crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":"two"}`, 400)
	crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{}`, 400)
	crExpect(t, s, "PUT", "/api/custom-routes/nope/subdomains", `{"depth":"one"}`, 404)
	crExpect(t, s, "POST", "/api/custom-routes/checkout/subdomains", `{"depth":"one"}`, 405)

	// Unassigned: stored, nothing rendered; it applies once assigned.
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"later"}`, 201)
	if r := crExpect(t, s, "PUT", "/api/custom-routes/later/subdomains", `{"depth":"any"}`, 200); r["subdomains"] != "any" {
		t.Fatalf("unassigned = %v", r)
	}
	r := crExpect(t, s, "PUT", "/api/custom-routes/later/assignment", `{"deployment":"pr-2"}`, 200)
	if !slices.Contains(crHosts(r["hosts"]), "*.web-later.preview.example.com") {
		t.Fatalf("assigned later = %v", r)
	}
}

func TestCustomRouteSubdomainsConflict(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	// pr-3's own subtree is *.web-checkout.preview.example.com.
	spec := "version: 1\nstack: myapp-pr-3\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web]\n  subdomains:\n    web: {host: web-checkout.preview.example.com}\naxes: []\n"
	compose := crCompose
	if _, err := s.registry.Put("pr-3", spec, registry.PutOptions{ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
	// negative control: skip the conflict check in the handler → 200 and this fails.
	got := crExpect(t, s, "PUT", "/api/custom-routes/checkout/subdomains", `{"depth":"one"}`, 409)
	if got["host"] != "*.web-checkout.preview.example.com" {
		t.Fatalf("409 = %v", got)
	}
	if r := crRoute(t, s, "checkout"); r["subdomains"] != nil {
		t.Fatalf("a refused change was stored: %v", r)
	}
}

func depRouted(t *testing.T, s *Server, id string) []any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/deployments/"+id+"/subdomains", nil)
	req.Header.Set("authorization", "Bearer t0ken")
	s.handle(rec, req)
	var out []any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("GET subdomains = %d: %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestDeploymentSubdomains(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	routed := depRouted(t, s, "pr-1")
	if len(routed) != 1 {
		t.Fatalf("routed = %v", routed)
	}
	if w := routed[0].(map[string]any); w["service"] != "web" || w["host"] != "web-myapp-pr-1.preview.example.com" ||
		w["subdomains"] != nil || w["from"] != nil {
		t.Fatalf("routed web = %v", w)
	}

	// negative control: don't merge the host rows in resolveDep → routed stays off and this fails.
	got := crExpect(t, s, "PUT", "/api/deployments/pr-1/subdomains", `{"service":"web","depth":"one"}`, 200)
	if got["note"] != "Applies on the next deploy." {
		t.Fatalf("PUT = %v", got)
	}
	w := depRouted(t, s, "pr-1")[0].(map[string]any)
	if w["subdomains"] != "one" || w["from"] != "host" {
		t.Fatalf("after PUT = %v", w)
	}
	// The detail is the spec's: host rows stay out of compose.subdomains.
	// negative control: render the detail from resolveDepAdded's merged list → one entry and this fails.
	d := crExpect(t, s, "GET", "/api/deployments/pr-1", "", 200)
	if subs := d["compose"].(map[string]any)["subdomains"].([]any); len(subs) != 0 {
		t.Fatalf("compose.subdomains = %v", subs)
	}
	// A route on it mirrors the subtree at once.
	if !slices.Contains(crHosts(crRoute(t, s, "checkout")["hosts"]), "*.web-checkout.preview.example.com") {
		t.Fatalf("route = %v", crRoute(t, s, "checkout"))
	}
	// pr-2 is untouched.
	if w := depRouted(t, s, "pr-2")[0].(map[string]any); w["subdomains"] != nil {
		t.Fatalf("pr-2 = %v", w)
	}

	crExpect(t, s, "PUT", "/api/deployments/pr-1/subdomains", `{"service":"db","depth":"one"}`, 400)
	crExpect(t, s, "PUT", "/api/deployments/pr-1/subdomains", `{"service":"web","depth":"two"}`, 400)
	crExpect(t, s, "PUT", "/api/deployments/nope/subdomains", `{"service":"web","depth":"one"}`, 404)
	crExpect(t, s, "POST", "/api/deployments/pr-1/subdomains", "", 405)

	crExpect(t, s, "PUT", "/api/deployments/pr-1/subdomains", `{"service":"web","depth":null}`, 200)
	if w := depRouted(t, s, "pr-1")[0].(map[string]any); w["subdomains"] != nil {
		t.Fatalf("off = %v", w)
	}

	// A row whose service has no generated host any more is listed, and null clears it.
	// negative control: drop the orphan rows from routedServices → it is not listed and this fails.
	if err := store.SetDeploymentSubdomain(s.store.DB, "pr-1", "gone", "one"); err != nil {
		t.Fatal(err)
	}
	r := depRouted(t, s, "pr-1")
	if g := r[len(r)-1].(map[string]any); g["service"] != "gone" || g["from"] != "host" || g["subdomains"] != "one" {
		t.Fatalf("orphan = %v", r)
	}
	// (null clears whatever is listed; the spec-set case below pins the bypass.)
	crExpect(t, s, "PUT", "/api/deployments/pr-1/subdomains", `{"service":"gone","depth":null}`, 200)
	if r := depRouted(t, s, "pr-1"); len(r) != 1 {
		t.Fatalf("after clearing = %v", r)
	}

	// Rows go with the deployment: one re-created under the id starts off.
	crExpect(t, s, "PUT", "/api/deployments/pr-2/subdomains", `{"service":"web","depth":"one"}`, 200)
	crExpect(t, s, "DELETE", "/api/deployments/pr-2", "", 200)
	if rows, _ := store.DeploymentSubdomains(s.store.DB, "pr-2"); len(rows) != 0 {
		t.Fatalf("rows after delete: %v", rows)
	}
}

func TestDeploymentSubdomainsSetInSpec(t *testing.T) {
	s, _ := crServer(t)
	spec := "version: 1\nstack: myapp-pr-3\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web]\n  subdomains: [web]\naxes: []\n"
	compose := crCompose
	if _, err := s.registry.Put("pr-3", spec, registry.PutOptions{ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
	w := depRouted(t, s, "pr-3")[0].(map[string]any)
	if w["subdomains"] != "one" || w["from"] != "spec" {
		t.Fatalf("spec-set = %v", w)
	}
	// negative control: drop the spec check → 200 and this fails.
	crExpect(t, s, "PUT", "/api/deployments/pr-3/subdomains", `{"service":"web","depth":"any"}`, 409)
	// A dormant host row behind the spec can still be cleared.
	// negative control: keep the spec check for null → 409 and this fails.
	_ = store.SetDeploymentSubdomain(s.store.DB, "pr-3", "web", "any")
	crExpect(t, s, "PUT", "/api/deployments/pr-3/subdomains", `{"service":"web","depth":null}`, 200)
	if rows, _ := store.DeploymentSubdomains(s.store.DB, "pr-3"); len(rows) != 0 {
		t.Fatalf("dormant row kept: %v", rows)
	}
}

func TestDeploymentSubdomainsExplicitSpecHost(t *testing.T) {
	s, _ := crServer(t)
	spec := "version: 1\nstack: myapp-pr-3\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web]\n  subdomains:\n    web: {host: shop.preview.example.com}\naxes: []\n"
	compose := crCompose
	if _, err := s.registry.Put("pr-3", spec, registry.PutOptions{ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
	// negative control: list the generated host for a spec entry → web-myapp-pr-3… and this fails.
	if w := depRouted(t, s, "pr-3")[0].(map[string]any); w["host"] != "shop.preview.example.com" || w["from"] != "spec" {
		t.Fatalf("explicit = %v", w)
	}
}

// A spec entry for a service with no generated host (here: its own traefik labels) is listed too, so
// the UIs show every wildcard the deployment has.
// negative control: drop the trailing spec-entry loop in routedServices → only web and api are listed.
func TestDeploymentSubdomainsListsSpecEntriesWithoutAGeneratedHost(t *testing.T) {
	s, _ := crServer(t)
	spec := "version: 1\nstack: myapp-pr-3\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [own]\n  subdomains: [own]\naxes: []\n"
	compose := crCompose + "  own:\n    image: nginx\n    labels: [traefik.enable=true]\n"
	if _, err := s.registry.Put("pr-3", spec, registry.PutOptions{ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
	r := depRouted(t, s, "pr-3")
	if len(r) != 2 {
		t.Fatalf("routed = %v", r)
	}
	if o := r[1].(map[string]any); o["service"] != "own" || o["host"] != "own-myapp-pr-3.preview.example.com" || o["from"] != "spec" || o["subdomains"] != "one" {
		t.Fatalf("own = %v", o)
	}
}
