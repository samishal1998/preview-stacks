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

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
)

func (s *Server) domainsGet(w http.ResponseWriter) error {
	extra := s.routing.Domains()
	writeJSON(w, 200, jsonx.O(
		// The one `init` rendered onto the container's labels. It cannot be removed from here, and
		// that is the point: it is the hostname that still answers when a file is wrong.
		"primary", s.opts.Domain,
		"domains", extra,
		"mode", string(s.challenge()),
		"note", domainsNote(len(extra), string(s.challenge())),
	))
	return nil
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
	stored, err := s.routing.SetDomains(want, routing.DomainOptions{Primary: s.opts.Domain, Mode: string(s.challenge())})
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
