// CUSTOM ROUTES: a stable alias for a stack. Route `checkout` on stack `myapp-pr-123` makes
// `web-myapp-pr-123.<domain>` also answer on `web-checkout.<domain>`, with no redeploy.
//
// The routers live in one derived, reserved dynamic file, re-rendered from the custom_routes table.
// Each mirrors one autolabel router: same rule shape, entrypoint and priority, and it points at the
// stack's own Traefik service, provider-qualified (`<svc>-<stack>@docker|@swarm`), because an
// unqualified name resolves to `@file`, which does not exist.
//
// The input is pure data. The caller derives it from the same autolabel computation the deploy
// used, since routing cannot import autolabel (autolabel imports routing).
package routing

import (
	"slices"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// CustomRoutesYAML is the dynamic file holding every assigned route's routers. Reserved: its
// routers ARE the assignments, so a hand edit would disagree with GET /api/custom-routes.
const CustomRoutesYAML = "pstack-routes.yml"

// AliasTarget is one assigned route: the alias, the stack it follows, and the services it mirrors.
type AliasTarget struct {
	Alias    string
	Stack    string
	Provider string // "docker" | "swarm"
	Services []AliasService
}

// AliasService is one routed service of the stack whose host autolabel generated.
type AliasService struct {
	// Name is the routing service name (pstack.routing.service_name aware): the Traefik service is
	// `<Name>-<Stack>` and the alias host `<Name>-<Alias>.<Domain>`.
	Name   string
	Domain string
	// Depth is the wildcard subdomain depth when the stack has a derived `-wild` router for this
	// service; "" means none. Autolabel renders at most one per service.
	Depth spec.Depth
}

func (t AliasTarget) host(s AliasService) string { return s.Name + "-" + t.Alias + "." + s.Domain }

func (t AliasTarget) router(s AliasService) string { return "pstack-route-" + t.Alias + "-" + s.Name }

// AliasRouters is every router name the route renders (`…-wild` included), for the assign-time
// check that two routes never render the same name.
func AliasRouters(t AliasTarget) []string {
	out := []string{}
	for _, s := range t.Services {
		out = append(out, t.router(s))
		if s.Depth != "" {
			out = append(out, t.router(s)+"-wild")
		}
	}
	return out
}

// AliasHosts is every host the route serves: the plain hosts in service order, then each mirrored
// wildcard as `*.<host>`.
func AliasHosts(t AliasTarget) []string {
	out, wild := []string{}, []string{}
	for _, s := range t.Services {
		out = append(out, t.host(s))
		if s.Depth != "" {
			wild = append(wild, "*."+t.host(s))
		}
	}
	return append(out, wild...)
}

const customRoutesHeader = "# Written by pstack (/api/custom-routes) from the custom_routes table.\n" +
	"# Derived state: the routing API refuses this filename, and every assign rewrites it.\n"

// RenderCustomRoutes builds the file for every target; "" when there is no router to render, so the
// caller removes the file rather than leaving an empty one on every host.
//
// Total: a router whose name is already taken (alias `a-b` + service `c` and alias `a` + service
// `b-c` both give `pstack-route-a-b-c`) or whose provider is unknown is left out with a comment
// naming it, and the rest of the file still renders. Targets and services are sorted first, so which
// one wins never depends on the caller's order.
//
// TLS is never the dns01 wildcard pin: the stack's own certificate story covers the alias host the
// same way it covers the stack host, and a pin per alias would order a wildcard certificate each.
func RenderCustomRoutes(targets []AliasTarget, mode string) (string, error) {
	targets = slices.Clone(targets)
	slices.SortFunc(targets, func(a, b AliasTarget) int { return strings.Compare(a.Alias, b.Alias) })
	var routers, notes strings.Builder
	seen := map[string]bool{}
	router := func(name, rule, priority, service, host string) {
		if seen[name] {
			notes.WriteString("# dropped " + name + ": the name is taken by another route\n")
			return
		}
		seen[name] = true
		routers.WriteString("    " + name + ":\n")
		routers.WriteString("      rule: " + strconv.Quote(rule) + "\n")
		if priority != "" {
			routers.WriteString("      priority: " + priority + "\n")
		}
		routers.WriteString("      entryPoints: [websecure]\n")
		routers.WriteString("      service: " + strconv.Quote(service) + "\n")
		routers.WriteString(tlsFor(host, mode, false))
	}
	for _, t := range targets {
		if t.Provider != "docker" && t.Provider != "swarm" {
			notes.WriteString("# dropped route " + t.Alias + ": unknown provider " + strconv.Quote(t.Provider) + "\n")
			continue
		}
		services := slices.Clone(t.Services)
		slices.SortFunc(services, func(a, b AliasService) int { return strings.Compare(a.Name, b.Name) })
		for _, s := range services {
			name := t.router(s)
			host := t.host(s)
			service := s.Name + "-" + t.Stack + "@" + t.Provider
			router(name, "Host(`"+host+"`)", "", service, host)
			if s.Depth != "" {
				// Priority 2, like autolabel's -wild: any exact host still wins.
				router(name+"-wild", spec.WildcardRule(host, s.Depth), "2", service, host)
			}
		}
	}
	if routers.Len() == 0 {
		return "", nil
	}
	content := customRoutesHeader + notes.String() + "http:\n  routers:\n" + routers.String()
	if _, err := ValidateRoutingContent(content); err != nil {
		return "", err
	}
	return content, nil
}

// SetCustomRoutes writes the rendered file atomically; "" removes it.
func (s *RoutingStore) SetCustomRoutes(content string) error {
	if content == "" {
		if _, err := s.remove(CustomRoutesYAML); err != nil && !strings.Contains(err.Error(), "no such routing file") {
			return err
		}
		return nil
	}
	_, err := s.write(CustomRoutesYAML, content)
	return err
}
