package api

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// Two routed services (one renamed through service_name, one with a derived wildcard), an
// explicit-host service, a hand-labelled one and an unrouted database.
const targetCompose = `services:
  web:
    image: nginx
    labels: {pstack.routing.port: "80"}
  api:
    image: api
    labels: {pstack.routing.port: "3000", pstack.routing.service_name: backend}
  admin:
    image: admin
    labels: {pstack.routing.port: "80", pstack.routing.host: admin.other.test}
  legacy:
    image: legacy
    labels: [traefik.enable=true]
  db:
    image: postgres
`

func targetFixture(t *testing.T, specYaml, compose string) (*spec.Stack, string) {
	t.Helper()
	reg := registry.New(t.TempDir())
	dep, err := reg.Put("dep1", specYaml, registry.PutOptions{ComposeYaml: &compose})
	if err != nil {
		t.Fatal(err)
	}
	st, err := reg.Resolve("dep1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, dep.Dir
}

func targetSpec(orchestrator, subdomains string) string {
	return "version: 1\nstack: myapp-pr-1\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web, backend, admin]\n" + orchestrator + subdomains + "axes: []\n"
}

func TestAliasTargetMirrorsOnlyGeneratedHosts(t *testing.T) {
	// negative control: mirror every Generated service (drop the `host == id+"."+domain` check) —
	// admin joins Services and the Services comparison fails.
	st, dir := targetFixture(t, targetSpec("", "  subdomains: [web]\n"), targetCompose)
	got, hosts, err := aliasTarget(st, dir, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	want := routing.AliasTarget{Alias: "checkout", Stack: "myapp-pr-1", Provider: "docker", Services: []routing.AliasService{
		{Name: "web", Domain: "preview.example.com", Depth: spec.DepthOne},
		{Name: "backend", Domain: "preview.example.com"},
	}}
	if got.Alias != want.Alias || got.Stack != want.Stack || got.Provider != want.Provider || !slices.Equal(got.Services, want.Services) {
		t.Fatalf("target:\n got %+v\nwant %+v", got, want)
	}
	if a := routing.AliasHosts(got); !slices.Equal(a, []string{
		"web-checkout.preview.example.com", "backend-checkout.preview.example.com", "*.web-checkout.preview.example.com",
	}) {
		t.Fatalf("alias hosts: %v", a)
	}
	// Every host the stack serves, the explicit one included, for the collision check.
	// negative control: append only mirrored hosts — admin.other.test goes missing.
	if !slices.Equal(hosts, []string{
		"web-myapp-pr-1.preview.example.com", "backend-myapp-pr-1.preview.example.com", "admin.other.test",
		"*.web-myapp-pr-1.preview.example.com",
	}) {
		t.Fatalf("hosts: %v", hosts)
	}
}

func TestAliasTargetSkipsAnExplicitWildcardHost(t *testing.T) {
	// negative control: set Depth whenever a -wild router exists (drop the derived-host check) —
	// web gets Depth "one" and an alias wildcard nobody asked for.
	st, dir := targetFixture(t, targetSpec("", "  subdomains:\n    web: {host: shop.preview.example.com}\n"), targetCompose)
	got, hosts, err := aliasTarget(st, dir, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 2 || got.Services[0].Name != "web" || got.Services[0].Depth != "" {
		t.Fatalf("services: %+v", got.Services)
	}
	if !slices.Contains(hosts, "*.shop.preview.example.com") {
		t.Fatalf("hosts: %v", hosts)
	}
}

func TestAliasTargetSwarm(t *testing.T) {
	// negative control: hardcode Provider "docker" — this fails.
	st, dir := targetFixture(t, targetSpec("  orchestrator: swarm\n", ""), targetCompose)
	got, _, err := aliasTarget(st, dir, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "swarm" || len(got.Services) != 2 {
		t.Fatalf("target: %+v", got)
	}
}

func TestAliasTargetIsTotal(t *testing.T) {
	t.Run("unreadable compose", func(t *testing.T) {
		// negative control: return an empty target on a read error (as MaterializeCompose does) —
		// err is nil and this fails.
		st, dir := targetFixture(t, targetSpec("", ""), targetCompose)
		if err := os.Remove(filepath.Join(dir, "compose.yml")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := aliasTarget(st, dir, "checkout"); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("compose not a mapping", func(t *testing.T) {
		// negative control: skip the mapping check — the nil doc augments to nothing and err is nil.
		st, dir := targetFixture(t, targetSpec("", ""), "- web\n")
		if _, _, err := aliasTarget(st, dir, "checkout"); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("no compose", func(t *testing.T) {
		// negative control: drop the nil-Compose guard — this panics.
		got, hosts, err := aliasTarget(&spec.Stack{Stack: "s"}, t.TempDir(), "checkout")
		if err != nil || len(got.Services) != 0 || len(hosts) != 0 {
			t.Fatalf("got %+v %v %v", got, hosts, err)
		}
	})
}
