package routing

import (
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// routersOf parses the stored file the way Traefik would and returns name → router mapping.
func routersOf(t *testing.T, s *RoutingStore) *omap.Map {
	t.Helper()
	content, err := s.Read(DomainsYAML)
	if err != nil {
		t.Fatalf("no domains file: %v", err)
	}
	doc, err := ValidateRoutingContent(content)
	if err != nil {
		t.Fatalf("the file this writes must be config Traefik accepts: %v", err)
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

func TestEachDomainGetsConsoleAPIAndAWakeRouter(t *testing.T) {
	// negative control: drop the pstack-wake-<slug> router from renderDomains — a preview on the
	// added domain still DEPLOYS and still sleeps, and then nothing routes its hostname here, so it
	// can never be woken: the one failure this file exists to prevent, and invisible until someone
	// visits a sleeping preview.
	s := New(t.TempDir())
	stored, err := s.SetDomains([]string{"preview.new.com"}, DomainOptions{Primary: "preview.old.com", Mode: "http01"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stored, ",") != "preview.new.com" {
		t.Fatalf("stored: %v", stored)
	}
	routers := routersOf(t, s)
	for _, want := range []string{"pstack-ui-preview-new-com", "pstack-api-preview-new-com", "pstack-wake-preview-new-com"} {
		if routers.GetMap(want) == nil {
			t.Errorf("missing router %s — have %v", want, routers.Keys())
		}
	}
	ui := routers.GetMap("pstack-ui-preview-new-com")
	if ui.GetString("rule") != "Host(`control.preview.new.com`)" {
		t.Errorf("ui rule: %q", ui.GetString("rule"))
	}
	// The service reference MUST carry the provider suffix: unqualified resolves to pstack@file,
	// which does not exist, and Traefik drops the router instead of falling back.
	if ui.GetString("service") != "pstack@docker" {
		t.Errorf("service must be provider-qualified: %q", ui.GetString("service"))
	}
	// `tls` is a sibling of rule/service, not a child of one — an indentation slip here parses as
	// valid YAML and silently produces a router with no TLS at all.
	if ui.GetMap("tls") == nil {
		t.Errorf("tls must be a router field: %v", ui.Keys())
	}
	wake := routers.GetMap("pstack-wake-preview-new-com")
	if wake.GetString("rule") != "HostRegexp(`^[a-z0-9-]+\\.preview\\.new\\.com$`)" {
		t.Errorf("wake rule: %q", wake.GetString("rule"))
	}
	// Priority 1 — the lowest — or the catch-all outranks the previews' own routers: Traefik's
	// default priority is the RULE LENGTH, and this rule is longer than most Host() rules.
	if n, _ := wake.Get("priority"); n != int64(1) {
		t.Errorf("wake priority must be pinned to 1, got %v", n)
	}

	// The list is derived from the file, not stored beside it.
	if got := strings.Join(s.Domains(), ","); got != "preview.new.com" {
		t.Errorf("Domains(): %q", got)
	}
	// The control plane's own hostnames on EVERY domain are excluded from waking.
	for _, h := range []string{"control.preview.new.com", "api.preview.new.com", "control.preview.old.com", "API.preview.old.com"} {
		if !s.IsControlHostname(h, "preview.old.com") {
			t.Errorf("%s must be a control hostname", h)
		}
	}
	if s.IsControlHostname("app-pr-1.preview.new.com", "preview.old.com") {
		t.Error("a preview hostname is not the control plane's")
	}

	// Emptying removes the file entirely rather than leaving an empty one Traefik would still read.
	if _, err := s.SetDomains(nil, DomainOptions{Primary: "preview.old.com"}); err != nil {
		t.Fatal(err)
	}
	if len(s.Domains()) != 0 {
		t.Error("no domains left")
	}
	if _, err := s.Read(DomainsYAML); err == nil {
		t.Error("the file must be gone, not empty")
	}
}

func TestTheConsoleRouterFollowsTheHostsUI(t *testing.T) {
	// negative control: hardcode apiService for the console router — an added domain serves the
	// EMBEDDED basic UI on control.<d> while control.<primary> serves the SPA. Same console, two
	// answers depending on which hostname you typed, and nothing says why. That shipped.
	adv := New(t.TempDir())
	if _, err := adv.SetDomains([]string{"preview.new.com"}, DomainOptions{ConsoleService: AdvancedUIService}); err != nil {
		t.Fatal(err)
	}
	r := routersOf(t, adv)
	if got := r.GetMap("pstack-ui-preview-new-com").GetString("service"); got != "advanced-ui@docker" {
		t.Errorf("the console must follow the host's UI: %q", got)
	}
	// The API is the API on every host — the SPA CALLS this hostname, it is not served by it.
	if got := r.GetMap("pstack-api-preview-new-com").GetString("service"); got != "pstack@docker" {
		t.Errorf("api must stay the API: %q", got)
	}
	// So must the wake catch-all: the waking page is rendered by the API.
	if got := r.GetMap("pstack-wake-preview-new-com").GetString("service"); got != "pstack@docker" {
		t.Errorf("wake must stay the API: %q", got)
	}

	// A basic host serves the console from the API container, which is the default.
	basic := New(t.TempDir())
	if _, err := basic.SetDomains([]string{"preview.new.com"}, DomainOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := routersOf(t, basic).GetMap("pstack-ui-preview-new-com").GetString("service"); got != "pstack@docker" {
		t.Errorf("a basic host serves the console from the API: %q", got)
	}
}

func TestDomainsRefusalsAndNormalisation(t *testing.T) {
	// negative control: drop the Primary equality check — the added domain renders a SECOND
	// pstack-ui router for a hostname the container's labels already serve, and Traefik deletes
	// BOTH copies of a name defined twice with different configs. The console goes dark.
	s := New(t.TempDir())
	if _, err := s.SetDomains([]string{"preview.old.com"}, DomainOptions{Primary: "preview.old.com"}); err == nil || !strings.Contains(err.Error(), "primary domain") {
		t.Errorf("the primary must be refused: %v", err)
	}
	if _, err := s.SetDomains([]string{"not a domain"}, DomainOptions{}); err == nil {
		t.Error("garbage must be refused")
	}
	// Trailing dot, case and duplicates are the same domain.
	stored, err := s.SetDomains([]string{"B.example.com.", "b.example.com", "a.example.com"}, DomainOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stored, ",") != "a.example.com,b.example.com" {
		t.Fatalf("normalised: %v", stored)
	}
}

func TestTheTLSBlockFollowsTheHostsMode(t *testing.T) {
	// negative control: return the http01 block for every mode — on a dns01 host each added domain
	// keeps a certresolver with no wildcard pin, so every hostname under it orders its own
	// certificate; on dns-persist-01 the resolver reappears and the stored wildcard is bypassed.
	// Both are the rate-limit burn the wildcard modes exist to stop.
	for _, tc := range []struct{ mode, wantIn, wantNotIn string }{
		{"http01", "certResolver: le", "domains:"},
		{"dns01", "sans:", ""},
		{"dns-persist-01", "tls: {}", "certResolver"},
	} {
		s := New(t.TempDir())
		if _, err := s.SetDomains([]string{"preview.new.com"}, DomainOptions{Mode: tc.mode}); err != nil {
			t.Fatal(err)
		}
		content, _ := s.Read(DomainsYAML)
		if !strings.Contains(content, tc.wantIn) {
			t.Errorf("%s: expected %q in\n%s", tc.mode, tc.wantIn, content)
		}
		if tc.wantNotIn != "" && strings.Contains(content, tc.wantNotIn) {
			t.Errorf("%s: must not contain %q in\n%s", tc.mode, tc.wantNotIn, content)
		}
		// Whatever the mode, the file has to be something Traefik will load.
		routersOf(t, s)
	}
}

func TestTheDomainsFileIsNotAnOperatorFile(t *testing.T) {
	// negative control: drop DomainsYAML from IsReserved — a maintainer can rewrite the domain list
	// through the generic routing API, and GET /api/domains then reports whatever they wrote as if
	// pstack had configured it.
	s := New(t.TempDir())
	if _, err := s.Write(DomainsYAML, "http: {}\n"); err == nil || !strings.Contains(err.Error(), "managed by pstack") {
		t.Errorf("writing it by hand must be refused: %v", err)
	}
	if _, err := s.Remove(DomainsYAML); err == nil || !strings.Contains(err.Error(), "managed by pstack") {
		t.Errorf("deleting it by hand must be refused: %v", err)
	}
}

func TestTheWildcardMustCoverEveryRegisteredDomain(t *testing.T) {
	// negative control: check only the primary in SetWildcard — a pair that covers preview.old.com
	// alone is accepted on a host that also answers on preview.new.com, and every preview under the
	// new domain serves a browser warning. The only symptom is a visitor's screenshot, weeks later.
	s := New(t.TempDir())
	now := time.Now()
	both := []string{"preview.old.com", "preview.new.com"}

	onlyOld, onlyOldKey := mint(t, []string{"*.preview.old.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	if _, err := s.SetWildcard(onlyOld, onlyOldKey, both); err == nil || !strings.Contains(err.Error(), "*.preview.new.com") {
		t.Fatalf("an uncovered domain must be named in the refusal: %v", err)
	}
	// It is fine for the host it actually covers.
	if _, err := s.SetWildcard(onlyOld, onlyOldKey, []string{"preview.old.com"}); err != nil {
		t.Fatalf("covering the only domain is enough: %v", err)
	}
	// A pair with both SANs is accepted for both.
	cert, key := mint(t, []string{"*.preview.old.com", "*.preview.new.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	if _, err := s.SetWildcard(cert, key, both); err != nil {
		t.Fatalf("a pair covering both must be accepted: %v", err)
	}
}
