// `/api/domains` — the additional hostnames this host answers on, added and removed at runtime.
//
// The motivation is a domain migration you can perform gradually: register the new domain, point a
// deployment at it by setting `PREVIEW_DOMAIN` in its vars, watch it, move the next one. Nothing is
// re-rendered and nothing restarts — the routers land in Traefik's watched directory and take
// effect in about two seconds (internal/routing/domains.go explains why that is safe).
//
// WHAT THIS ROUTE DOES NOT DO, deliberately: it does not move any deployment. A preview's hostname
// comes from its own spec's PREVIEW_DOMAIN, which is already per-deployment and already editable —
// so the migration's unit is one deployment, and its rollback is that same deployment. Registering
// a domain only makes the host able to SERVE and WAKE names under it.
//
// Maintainer, like the other host-configuration surfaces and like the routing files this writes.
package api

import (
	"net/http"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
)

// consoleService is what an added domain's `control.<d>` must target: the SPA when this host runs
// the advanced UI, otherwise the API container that serves the embedded one.
//
// DERIVED from the running control stack rather than a stored setting, for the same reason the
// certificate mode is: `pstack ui advanced` adds a container and re-renders the primary router, and
// a setting written when the domain was added would still say `basic` afterwards.
func (s *Server) consoleService() string {
	for _, c := range inspect.ControlRuntime(s.host).Containers {
		if c.Service != nil && *c.Service == advancedUIService {
			return routing.AdvancedUIService
		}
	}
	return ""
}

// advancedUIService is the compose service name `init` gives the SPA container.
const advancedUIService = "advanced-ui"

// domainOptions is what the renderer needs to know about this host, in one place so the PUT and the
// boot-time reconcile cannot disagree about it.
func (s *Server) domainOptions() routing.DomainOptions {
	return routing.DomainOptions{
		Primary:        s.opts.Domain,
		Mode:           string(s.challenge()),
		ConsoleService: s.consoleService(),
	}
}

// reconcileDomains rewrites the domains file from the host as it is NOW.
//
// Called at startup, which is what makes `pstack ui advanced` heal an added domain: that command
// re-runs init, which recreates this container, and the rewrite on the way back up points every
// `control.<d>` at the SPA that now exists. Without it the primary domain would serve the new
// console and every added domain would still serve the old one.
func (s *Server) reconcileDomains() {
	existing := s.routing.Domains()
	if len(existing) == 0 {
		return
	}
	if _, err := s.routing.SetDomains(existing, s.domainOptions()); err != nil {
		s.opts.Log("domains: could not reconcile " + strings.Join(existing, ", ") + ": " + err.Error())
	}
}

func (s *Server) domainsGet(w http.ResponseWriter) error {
	extra := s.routing.Domains()
	writeJSON(w, 200, jsonx.O(
		// The one `init` rendered onto the container's labels. It cannot be removed from here, and
		// that is the point: it is the hostname that still answers when a file is wrong.
		"primary", s.opts.Domain,
		"domains", extra,
		"mode", string(s.challenge()),
		// Which console `control.<d>` serves — the SPA on an advanced host, the embedded UI
		// otherwise. Shown because "why does this hostname look different" is the question it
		// answers.
		"console", consoleName(s.consoleService()),
		"note", domainsNote(len(extra), string(s.challenge())),
	))
	return nil
}

func consoleName(service string) string {
	if service == routing.AdvancedUIService {
		return "advanced"
	}
	return "basic"
}

func domainsNote(n int, mode string) string {
	if n == 0 {
		return "Only the primary domain. Add one to serve control./api. and wake sleeping previews under it; point a deployment at it with PREVIEW_DOMAIN in its vars."
	}
	switch mode {
	case "dns01":
		return "Each added domain pins its own wildcard, so one failing to validate cannot take the others down — but the DNS credential must be able to serve every zone listed."
	case "dns-persist-01":
		return "The stored wildcard must cover every domain here; PUT /api/tls/wildcard refuses a pair that does not."
	default:
		return "Under HTTP-01 each hostname resolves its own certificate, so every added domain brings its own weekly Let's Encrypt budget — and its own port-80 reachability requirement."
	}
}

func (s *Server) domainsPut(w http.ResponseWriter, r *http.Request) error {
	body := bodyOrEmpty(r)
	raw, present := body.Get("domains")
	if !present {
		writeError(w, 400, "body must be { domains: [ … ] } — the complete list, since it replaces what is stored")
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		writeError(w, 400, "`domains` must be an array of hostnames")
		return nil
	}
	want := make([]string, 0, len(list))
	for _, v := range list {
		sv, ok := v.(string)
		if !ok {
			writeError(w, 400, "`domains` must be an array of hostnames")
			return nil
		}
		want = append(want, sv)
	}
	stored, err := s.routing.SetDomains(want, s.domainOptions())
	if err != nil {
		if routing.IsError(err) {
			writeError(w, 400, err.Error())
			return nil
		}
		return err
	}
	// The index keys the wake path off hostnames, and the set of hostnames that are the CONTROL
	// plane's own just changed — rebuild so a `control.<new>` request is not mistaken for a
	// preview's while the old snapshot is live.
	s.reindex()
	writeJSON(w, 200, jsonx.O(
		"ok", true,
		"primary", s.opts.Domain,
		"domains", stored,
		"note", "Traefik picks these up within a couple of seconds. Point a deployment at one by setting PREVIEW_DOMAIN in its vars and redeploying it — existing stacks keep the domain they were deployed under.",
	))
	return nil
}
