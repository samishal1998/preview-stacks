package routing

import (
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// renderedRouters parses rendered content the way Traefik would and returns name → router.
func renderedRouters(t *testing.T, content string) *omap.Map {
	t.Helper()
	doc, err := ValidateRoutingContent(content)
	if err != nil {
		t.Fatalf("the file must be config Traefik accepts: %v\n%s", err, content)
	}
	http, _ := doc.Get("http")
	m, ok := http.(*omap.Map)
	if !ok {
		t.Fatalf("no http section: %s", content)
	}
	routers := m.GetMap("routers")
	if routers == nil {
		t.Fatalf("no routers: %s", content)
	}
	return routers
}

func checkout(provider string, services ...AliasService) AliasTarget {
	return AliasTarget{Alias: "checkout", Stack: "myapp-pr-123", Provider: provider, Services: services}
}

func render(t *testing.T, mode string, targets ...AliasTarget) string {
	t.Helper()
	content, err := RenderCustomRoutes(targets, mode)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestCustomRoutesGolden(t *testing.T) {
	// negative control: render the exact router with `priority: 2` like its -wild twin — the exact
	// alias host then ties with the wildcard instead of winning on rule length, and the bytes differ.
	got := render(t, "http01", checkout("docker",
		AliasService{Name: "web", Domain: "preview.example.com", Depth: spec.DepthOne},
	))
	want := customRoutesHeader +
		"http:\n  routers:\n" +
		"    pstack-route-checkout-web:\n" +
		"      rule: \"Host(`web-checkout.preview.example.com`)\"\n" +
		"      entryPoints: [websecure]\n" +
		"      service: \"web-myapp-pr-123@docker\"\n" +
		"      tls:\n        certResolver: le\n" +
		"    pstack-route-checkout-web-wild:\n" +
		"      rule: \"HostRegexp(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\\\\.web-checkout\\\\.preview\\\\.example\\\\.com$`)\"\n" +
		"      priority: 2\n" +
		"      entryPoints: [websecure]\n" +
		"      service: \"web-myapp-pr-123@docker\"\n" +
		"      tls:\n        certResolver: le\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	renderedRouters(t, got)
}

func TestCustomRoutesMirrorEveryServiceOnTheAliasHost(t *testing.T) {
	for _, tc := range []struct{ provider, service string }{
		// negative control: drop the `@`+Provider suffix — an unqualified service resolves to
		// `<svc>-<stack>@file`, which does not exist, and Traefik drops the router.
		{"docker", "web-myapp-pr-123@docker"},
		// negative control: hardcode `@docker` — a swarm stack's alias points at a service the
		// docker provider never registered.
		{"swarm", "web-myapp-pr-123@swarm"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			routers := renderedRouters(t, render(t, "http01", checkout(tc.provider,
				AliasService{Name: "web", Domain: "preview.example.com"},
				AliasService{Name: "api", Domain: "preview.example.com"},
			)))
			if strings.Join(routers.Keys(), ",") != "pstack-route-checkout-api,pstack-route-checkout-web" {
				t.Fatalf("one exact router per service, sorted, no -wild: %v", routers.Keys())
			}
			web := routers.GetMap("pstack-route-checkout-web")
			if web.GetString("rule") != "Host(`web-checkout.preview.example.com`)" {
				t.Errorf("rule: %q", web.GetString("rule"))
			}
			if web.GetString("service") != tc.service {
				t.Errorf("service: %q, want %q", web.GetString("service"), tc.service)
			}
			if api := routers.GetMap("pstack-route-checkout-api"); api.GetString("service") != strings.Replace(tc.service, "web", "api", 1) {
				t.Errorf("api service: %q", api.GetString("service"))
			}
			if _, ok := web.Get("priority"); ok {
				t.Errorf("the exact router takes Traefik's default priority, like the label router")
			}
		})
	}
}

func TestCustomRoutesWildcardService(t *testing.T) {
	for _, depth := range []spec.Depth{spec.DepthOne, spec.DepthAny} {
		t.Run(string(depth), func(t *testing.T) {
			// negative control: build the -wild rule from the STACK host — the alias's wildcard then
			// shadows the stack's own and *.web-checkout.<domain> matches nothing.
			routers := renderedRouters(t, render(t, "http01", checkout("swarm",
				AliasService{Name: "web", Domain: "preview.example.com", Depth: depth},
			)))
			wild := routers.GetMap("pstack-route-checkout-web-wild")
			if wild == nil {
				t.Fatalf("no -wild router: %v", routers.Keys())
			}
			if want := spec.WildcardRule("web-checkout.preview.example.com", depth); wild.GetString("rule") != want {
				t.Errorf("rule: %q, want %q", wild.GetString("rule"), want)
			}
			if p, _ := wild.Get("priority"); p != int64(2) {
				t.Errorf("priority: %v, want 2", p)
			}
			if wild.GetString("service") != "web-myapp-pr-123@swarm" {
				t.Errorf("service: %q", wild.GetString("service"))
			}
		})
	}
}

func TestCustomRoutesTLSByMode(t *testing.T) {
	for _, tc := range []struct{ mode, wantIn string }{
		// negative control: return `tls: {}` for every mode — an http01 alias host gets no
		// certificate at all.
		{"http01", "certResolver: le"},
		// negative control: same `tls: {}` mutation — an unknown mode must still ask for a certificate.
		{"", "certResolver: le"},
		// negative control: call tlsFor with wildcardPin=true — each dns01 alias orders a SECOND
		// wildcard certificate and burns the rate limit.
		{"dns01", "tls: {}"},
		// negative control: return `certResolver: le` for every mode — the stored wildcard is
		// bypassed and each alias host orders its own certificate.
		{"dns-persist-01", "tls: {}"},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			content := render(t, tc.mode, checkout("docker",
				AliasService{Name: "web", Domain: "preview.example.com", Depth: spec.DepthOne},
			))
			if got := strings.Count(content, tc.wantIn); got != 2 {
				t.Errorf("%q on %d routers, want both:\n%s", tc.wantIn, got, content)
			}
			for _, pin := range []string{"domains:", "sans:"} {
				if strings.Contains(content, pin) {
					t.Errorf("an alias router never carries the wildcard pin (%s):\n%s", pin, content)
				}
			}
			renderedRouters(t, content)
		})
	}
}

func TestNoCustomRoutesIsNoFile(t *testing.T) {
	// negative control: render the header and an empty `routers:` for no targets — a
	// pstack-routes.yml then appears on every host at boot (R4).
	for name, targets := range map[string][]AliasTarget{
		"nil":         nil,
		"no services": {checkout("docker")},
	} {
		if got := render(t, "http01", targets...); got != "" {
			t.Errorf("%s: want \"\", got:\n%s", name, got)
		}
	}
	s := New(t.TempDir())
	// negative control: drop the `no such routing file` tolerance — boot with an empty table fails.
	if err := s.SetCustomRoutes(""); err != nil {
		t.Fatalf("removing a file that is not there is fine: %v", err)
	}
	if err := s.SetCustomRoutes(render(t, "http01", checkout("docker", AliasService{Name: "web", Domain: "d.example.com"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(CustomRoutesYAML); err != nil {
		t.Fatalf("written: %v", err)
	}
	if err := s.SetCustomRoutes(""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(CustomRoutesYAML); err == nil {
		t.Fatal("an empty render must remove the file")
	}
}

func TestCustomRoutesDropADuplicateRouterName(t *testing.T) {
	// negative control: drop the seen-name check — the YAML carries one key twice, the parser keeps
	// the LAST, and alias `a-b` silently serves the host meant for alias `a`.
	content := render(t, "http01",
		AliasTarget{Alias: "a-b", Stack: "s2", Provider: "docker", Services: []AliasService{{Name: "c", Domain: "d.example.com"}}},
		AliasTarget{Alias: "a", Stack: "s1", Provider: "docker", Services: []AliasService{{Name: "b-c", Domain: "d.example.com"}}},
	)
	if n := strings.Count(content, "    pstack-route-a-b-c:\n"); n != 1 {
		t.Fatalf("router key appears %d times:\n%s", n, content)
	}
	r := renderedRouters(t, content).GetMap("pstack-route-a-b-c")
	if r.GetString("rule") != "Host(`b-c-a.d.example.com`)" {
		t.Errorf("first by alias wins, whatever the caller's order: %q", r.GetString("rule"))
	}
	if !strings.Contains(content, "# dropped pstack-route-a-b-c") {
		t.Errorf("the drop must be visible in the file:\n%s", content)
	}
}

func TestCustomRoutesDropAnUnknownProvider(t *testing.T) {
	// negative control: drop the provider check — `@kube` reaches Traefik, which drops the router
	// for a provider it does not run, and nothing says why.
	content := render(t, "http01",
		AliasTarget{Alias: "bad", Stack: "s1", Provider: "kube", Services: []AliasService{{Name: "web", Domain: "d.example.com"}}},
		checkout("docker", AliasService{Name: "web", Domain: "d.example.com"}),
	)
	if strings.Contains(content, "@kube") || !strings.Contains(content, "# dropped route bad") {
		t.Errorf("an unknown provider is dropped with a note:\n%s", content)
	}
	if renderedRouters(t, content).GetMap("pstack-route-checkout-web") == nil {
		t.Errorf("the rest of the file still renders:\n%s", content)
	}
}

func TestAliasHosts(t *testing.T) {
	// negative control: append the `*.` entry right after its plain host — the order becomes
	// web, *.web, api and the API's hosts[] no longer lists plain hosts first (R31).
	got := AliasHosts(checkout("docker",
		AliasService{Name: "web", Domain: "preview.example.com", Depth: spec.DepthOne},
		AliasService{Name: "api", Domain: "preview.example.com"},
	))
	want := "web-checkout.preview.example.com,api-checkout.preview.example.com,*.web-checkout.preview.example.com"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestCustomRoutesServeOneHalfOfABlockedService(t *testing.T) {
	t.Run("NoHost serves the wildcard only", func(t *testing.T) {
		// negative control: ignore NoHost in RenderCustomRoutes → the plain router renders and this fails.
		svc := AliasService{Name: "web", Domain: "preview.example.com", Depth: spec.DepthOne, NoHost: true}
		tg := checkout("docker", svc)
		routers := renderedRouters(t, render(t, "http01", tg))
		if routers.Len() != 1 || routers.GetMap("pstack-route-checkout-web-wild") == nil {
			t.Fatalf("routers = %v", routers.Keys())
		}
		// negative control: ignore NoHost in AliasHosts or AliasRouters → the plain entry is listed and this fails.
		if h, r := strings.Join(AliasHosts(tg), ","), strings.Join(AliasRouters(tg), ","); h != "*.web-checkout.preview.example.com" || r != "pstack-route-checkout-web-wild" {
			t.Fatalf("hosts %s routers %s", h, r)
		}
	})
	t.Run("a cleared Depth serves the plain host only", func(t *testing.T) {
		// negative control: render -wild whatever Depth says → two routers and this fails.
		routers := renderedRouters(t, render(t, "http01", checkout("docker", AliasService{Name: "web", Domain: "preview.example.com"})))
		if routers.Len() != 1 || routers.GetMap("pstack-route-checkout-web") == nil {
			t.Fatalf("routers = %v", routers.Keys())
		}
	})
	t.Run("nothing served is no file", func(t *testing.T) {
		// negative control: ignore NoHost in RenderCustomRoutes → a router renders and this fails.
		if got := render(t, "http01", checkout("docker", AliasService{Name: "web", Domain: "preview.example.com", NoHost: true})); got != "" {
			t.Fatalf("got:\n%s", got)
		}
	})
}

func TestReservedFilesNameTheirOwningRoute(t *testing.T) {
	s := New(t.TempDir())
	for _, tc := range []struct{ name, route string }{
		// negative control: drop CustomRoutesYAML from IsReserved — PUT /api/routing/pstack-routes.yml
		// hand-edits the file the custom_routes table derives, and the next render silently undoes it.
		{CustomRoutesYAML, "/api/custom-routes"},
		// negative control: keep one wildcard-only message for every file — an operator is told to use
		// /api/tls/wildcard to change a domain or a route (R7).
		{DomainsYAML, "/api/domains"},
		{WildcardYAML, "/api/tls/wildcard"},
	} {
		if !IsReserved(tc.name) {
			t.Errorf("%s must be reserved", tc.name)
		}
		_, werr := s.Write(tc.name, "http: {}\n")
		_, rerr := s.Remove(tc.name)
		for _, err := range []error{werr, rerr} {
			if err == nil || !strings.Contains(err.Error(), "managed by pstack") || !strings.Contains(err.Error(), tc.route) {
				t.Errorf("%s: want a refusal naming %s, got %v", tc.name, tc.route, err)
			}
		}
	}
	if IsReserved("extra.yml") {
		t.Error("an operator file is not reserved")
	}
}
