// Wildcard subdomains turned on from the API instead of the spec: per deployment and service here,
// per custom route in routes_customroutes.go. A deployment's rows (store.DeploymentSubdomains) are
// merged into its resolved spec by resolveDep, so every consumer (deploy, sleep index, routes) sees
// one list; a spec entry for the same service wins. A change applies on the next deploy, since the
// stack's own `-wild` router is a container label.
package api

import (
	"maps"
	"net/http"
	"slices"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// hostSubdomains appends deployment id's stored wildcards to st's compose subdomains and returns
// the services it added. A service the spec already names (or whose PSTACK_WILD_ name it takes) is
// skipped, and so is everything when the stack has no compose block or no preview domain.
func (s *Server) hostSubdomains(id string, st *spec.Stack) ([]string, error) {
	if s.store == nil || st.Compose == nil {
		return nil, nil
	}
	rows, err := store.DeploymentSubdomains(s.store.DB, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	domain := spec.ResolvePreviewDomain(st.Env, st.DeclaredEnv, nil)
	if domain == "" {
		return nil, nil
	}
	added := []string{}
	for _, svc := range slices.Sorted(maps.Keys(rows)) {
		varName := spec.SubdomainVarName(svc)
		if slices.ContainsFunc(st.Compose.Subdomains, func(r spec.SubdomainRoute) bool { return r.Profile == svc || r.VarName == varName }) {
			continue
		}
		host, depth := svc+"-"+st.Stack+"."+domain, spec.Depth(rows[svc])
		st.Compose.Subdomains = append(st.Compose.Subdomains,
			spec.SubdomainRoute{Profile: svc, Host: host, Depth: depth, VarName: varName, Rule: spec.WildcardRule(host, depth)})
		added = append(added, svc)
	}
	return added, nil
}

// routedService is one service of a deployment with a generated host, and its wildcard: depth ""
// none, from "spec" | "host" | "". host is the wildcard's base: the spec entry's own host when it
// names one.
type routedService struct {
	name, host string
	depth      spec.Depth
	from       string
}

// routedServices lists them, then every remaining compose.subdomains entry (added: what
// hostSubdomains merged). Nil when the compose file cannot be read.
func routedServices(st *spec.Stack, dir string, added []string) []routedService {
	t, _, err := aliasTarget(st, dir, "", "")
	if err != nil {
		return nil
	}
	out := []routedService{}
	entry := func(name string) int {
		return slices.IndexFunc(st.Compose.Subdomains, func(sr spec.SubdomainRoute) bool { return sr.Profile == name })
	}
	for _, svc := range t.Services {
		r := routedService{name: svc.Name, host: svc.Name + "-" + st.Stack + "." + svc.Domain}
		if i := entry(svc.Name); i >= 0 {
			r.host, r.depth, r.from = st.Compose.Subdomains[i].Host, st.Compose.Subdomains[i].Depth, "spec"
			if slices.Contains(added, svc.Name) {
				r.from = "host"
			}
		}
		out = append(out, r)
	}
	// Then every entry not listed yet: a host row whose service lost its generated host (so it can be
	// cleared), and a spec entry for a service with its own traefik labels or none (read-only).
	for _, e := range st.Compose.Subdomains {
		if !slices.ContainsFunc(out, func(r routedService) bool { return r.name == e.Profile }) {
			from := "spec"
			if slices.Contains(added, e.Profile) {
				from = "host"
			}
			out = append(out, routedService{name: e.Profile, host: e.Host, depth: e.Depth, from: from})
		}
	}
	return out
}

// getDeploymentSubdomains is GET /api/deployments/{id}/subdomains: a bare array of
// `{ service, host, subdomains, from }`, nulls for none. Empty when the compose file cannot be read.
func (s *Server) getDeploymentSubdomains(w http.ResponseWriter, dep *registry.Deployment) error {
	st, added, err := s.resolveDepAdded(dep.ID, nil)
	if err != nil {
		return err
	}
	out := []jsonx.Object{}
	if st.Compose != nil {
		for _, r := range routedServices(st, dep.Dir, added) {
			out = append(out, jsonx.O("service", r.name, "host", r.host, "subdomains", nullable(string(r.depth)), "from", nullable(r.from)))
		}
	}
	writeJSON(w, 200, out)
	return nil
}

// putDeploymentSubdomain is PUT /api/deployments/{id}/subdomains { service, depth: "one" | "any" |
// null }. Turning one on needs a service with a generated host the spec does not name (400, 409);
// null always deletes the row.
func (s *Server) putDeploymentSubdomain(w http.ResponseWriter, r *http.Request, dep *registry.Deployment) error {
	body := bodyObject(r)
	svc, ok := getStr(body, "service")
	v, present := bodyGet(body, "depth")
	depth, _ := v.(string)
	if !ok || !present || (v != nil && depth != "one" && depth != "any" && depth != "off") {
		writeError(w, 400, `body must be { service, depth: "one" | "any" | "off" | null }`)
		return nil
	}
	if depth == "off" {
		depth = ""
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	st, added, err := s.resolveDepAdded(dep.ID, nil)
	if err != nil {
		return err
	}
	var list []routedService
	if st.Compose != nil {
		list = routedServices(st, dep.Dir, added)
	}
	i := slices.IndexFunc(list, func(r routedService) bool { return r.name == svc })
	switch {
	case depth == "":
		// Off always clears the row, even one the spec now hides or whose service is gone.
	case i < 0:
		writeError(w, 400, svc+" is not a service with a generated host")
		return nil
	case list[i].from == "spec":
		writeError(w, 409, svc+": set in the spec")
		return nil
	}
	if err := store.SetDeploymentSubdomain(s.store.DB, dep.ID, svc, depth); err != nil {
		return err
	}
	// A route on this deployment mirrors the stack's wildcards, and that file applies at once.
	s.reconcileCustomRoutes()
	writeJSON(w, 200, jsonx.O("service", svc, "subdomains", nullable(depth), "note", "Applies on the next deploy."))
	return nil
}
