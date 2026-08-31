// ADDITIONAL DOMAINS: the hostnames this host answers on besides the one `init` rendered.
//
// ── WHY A DYNAMIC FILE AND NOT A RE-INIT ─────────────────────────────────────────────────────────
//
// The primary domain's `control.` and `api.` routers are docker LABELS on the pstack container, and
// they must stay there: routing.go's header states the property they buy — no file written here can
// lock an operator out of the UI and the API that would let them undo it. Adding a SECOND domain
// does not touch that. The primary keeps answering from its labels whatever happens to this file,
// so the recovery path survives, and the addition costs no re-render, no restart and no downtime —
// Traefik picks the file up in about two seconds.
//
// A sidecar proxy was considered and rejected: Traefik owns :443, so nothing reaches a sidecar until
// a router has already matched the hostname — the router is the whole job, and the sidecar would be
// a second hop behind it, living in the init-owned control stack that adding a domain would then
// have to recreate.
//
// ── WHAT ONE DOMAIN NEEDS ────────────────────────────────────────────────────────────────────────
//
//	control.<d>  and  api.<d>   → the console and the API, so the new name is usable end to end
//	a wake catch-all             → so a SLEEPING preview on that domain can be woken at all
//
// The wake router is the one people forget. The sleep index is already domain-agnostic (it stores
// the hostnames a deployment's own routers carry), so a preview on a second domain is indexed
// correctly the day it is deployed — but with nothing routing `*.<d>` to this container while its
// own routers are gone, the request never arrives and the stack stays asleep forever.
//
// ── SERVICE REFERENCES ARE PROVIDER-QUALIFIED ────────────────────────────────────────────────────
//
// `service: pstack@docker`, and the suffix is LOAD-BEARING: an unqualified name resolves against the
// REFERENCING object's provider, so a bare `pstack` here means `pstack@file`, which does not exist,
// and Traefik drops the router with an error rather than falling back.
//
// `@docker` is right on a swarm host too, and that is not an oversight to fix later: `pstack init`
// brings the control stack up with `docker compose -p pstack-control` in both orchestrator modes and
// leaves `--providers.docker=true` on, so this container is always the docker provider's. Only the
// per-PR stacks move to `@swarm`.
package routing

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// DomainsYAML is the dynamic file holding every additional domain's routers. Reserved, like the
// wildcard pointer: it is derived state, and a hand edit would make the API's answer a lie.
const DomainsYAML = "pstack-domains.yml"

// The API service every `api.<d>` router points at — see the package comment on why the suffix is
// not optional and why it is `@docker` in both orchestrator modes.
const apiService = "pstack@docker"

// AdvancedUIService is what `control.<d>` targets on a host running the advanced UI. On a basic
// host the console IS the API container, so it targets apiService instead.
const AdvancedUIService = "advanced-ui@docker"

// A hostname this host could plausibly answer on. Deliberately strict: it becomes a Traefik rule and
// a regexp, and an unvalidated string reaches both.
var domainRe = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$`)

// controlRule pulls the domain back out of a rendered `control.` router rule — the file is the
// state, so the list is DERIVED from it rather than stored beside it where the two could disagree.
var controlRule = regexp.MustCompile("^Host\\(`control\\.([^`]+)`\\)$")

// DomainOptions is what the rendering needs to know about the host.
type DomainOptions struct {
	// Primary is the domain `init` rendered onto the container's labels. Refused as an addition:
	// it already has routers, and a second set would be a name collision — Traefik drops BOTH
	// routers when one name is defined twice with different configurations.
	Primary string
	// Mode is the host's certificate mode (http01 | dns01 | dns-persist-01 | unknown). It decides
	// only the TLS block; see tlsFor.
	Mode string
	// ConsoleService is the Traefik service `control.<d>` targets. Empty means the API container,
	// which is what a BASIC host serves the console from.
	//
	// It exists because the primary domain's own router already makes this choice — `init` renders
	// `pstack-ui.service=advanced-ui` on an advanced host — and an added domain that ignored it
	// served the embedded basic UI on `control.<new-domain>` while `control.<primary>` served the
	// SPA. Same console, two different answers depending on which hostname you typed.
	ConsoleService string
}

// consoleService is the service the console router targets, defaulting to the API container.
func (o DomainOptions) consoleService() string {
	if o.ConsoleService != "" {
		return o.ConsoleService
	}
	return apiService
}

// Domains is every additional domain, sorted. Derived from the file; empty when there is none.
//
// Total on a nil receiver: a host with no dynamic directory has no additional domains, and this is
// read on the REQUEST PATH (the wake exclusion), where a panic would be a 500 on every hostname.
func (s *RoutingStore) Domains() []string {
	out := []string{}
	if s == nil {
		return out
	}
	content, err := s.Read(DomainsYAML)
	if err != nil {
		return out
	}
	doc, err := ValidateRoutingContent(content)
	if err != nil {
		return out
	}
	http, _ := doc.Get("http")
	httpMap, ok := http.(*omap.Map)
	if !ok {
		return out
	}
	routers := httpMap.GetMap("routers")
	if routers == nil {
		return out
	}
	seen := map[string]bool{}
	for _, name := range routers.Keys() {
		r := routers.GetMap(name)
		if r == nil {
			continue
		}
		m := controlRule.FindStringSubmatch(r.GetString("rule"))
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// SetDomains rewrites the file so this host answers on exactly these additional domains. An empty
// list removes it. Returns the normalised list actually stored.
func (s *RoutingStore) SetDomains(domains []string, o DomainOptions) ([]string, error) {
	clean := []string{}
	seen := map[string]bool{}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(d), ".")))
		if d == "" {
			continue
		}
		if !domainRe.MatchString(d) {
			return nil, &Error{fmt.Sprintf("%q is not a hostname — expected something like preview.example.com", d)}
		}
		if o.Primary != "" && strings.EqualFold(d, o.Primary) {
			return nil, &Error{fmt.Sprintf("%s is this host's primary domain — it already has routers, on the container's own labels. Adding it here would define the same router twice, and Traefik drops BOTH copies when it sees that", d)}
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		clean = append(clean, d)
	}
	sort.Strings(clean)

	if len(clean) == 0 {
		if _, err := s.remove(DomainsYAML); err != nil && !strings.Contains(err.Error(), "no such routing file") {
			return nil, err
		}
		return clean, nil
	}
	if _, err := s.write(DomainsYAML, renderDomains(clean, o)); err != nil {
		return nil, err
	}
	return clean, nil
}

// slug is a domain as a router-name component.
func slug(d string) string { return strings.ReplaceAll(d, ".", "-") }

// tlsFor is the TLS block for one domain's control/api routers, by mode.
//
//   - http01: each hostname resolves its own certificate, so the resolver goes on the router.
//   - dns01: one router per domain carries the wildcard pin, which orders a SECOND wildcard —
//     independent of the primary's, so a domain whose DNS is not pointed yet cannot take the
//     primary's certificate down with it. Needs a DNS credential that can serve this zone too.
//   - dns-persist-01: the stored wildcard already covers it (PUT /api/tls/wildcard refuses a pair
//     that does not), so `tls: {}` and it is inherited by SNI.
func tlsFor(d, mode string, wildcardPin bool) string {
	// Six spaces: `tls` is a SIBLING of rule/service on the router, not a child of one.
	switch mode {
	case "dns01":
		if wildcardPin {
			return "      tls:\n" +
				"        certResolver: le\n" +
				"        domains:\n" +
				"          - main: " + d + "\n" +
				"            sans:\n" +
				"              - \"*." + d + "\"\n"
		}
		return "      tls: {}\n"
	case "dns-persist-01":
		return "      tls: {}\n"
	default: // http01, and unknown — which errs toward asking for a certificate
		return "      tls:\n        certResolver: le\n"
	}
}

func renderDomains(domains []string, o DomainOptions) string {
	var b strings.Builder
	b.WriteString("# Written by pstack (PUT /api/domains) — the additional domains this host answers on.\n")
	b.WriteString("# Derived state: the routers below ARE the list, so editing this by hand would make\n")
	b.WriteString("# GET /api/domains disagree with the host. The routing API refuses this filename.\n")
	b.WriteString("#\n")
	b.WriteString("# The primary domain is NOT here — its routers are labels on the pstack container, so no\n")
	b.WriteString("# mistake in this file can cost you the console that would undo it.\n")
	b.WriteString("http:\n  routers:\n")
	for _, d := range domains {
		s := slug(d)
		// The console and the API. One carries the dns01 wildcard pin (if any), so the pair costs
		// one certificate rather than two.
		b.WriteString("    pstack-ui-" + s + ":\n")
		b.WriteString("      rule: \"Host(`control." + d + "`)\"\n")
		b.WriteString("      entryPoints: [websecure]\n")
		b.WriteString("      service: \"" + o.consoleService() + "\"\n")
		b.WriteString(tlsFor(d, o.Mode, true))
		// The API is the API on every host: `api.<d>` never points at the SPA, whatever the console
		// serves, because the advanced UI calls this hostname rather than being served by it.
		b.WriteString("    pstack-api-" + s + ":\n")
		b.WriteString("      rule: \"Host(`api." + d + "`)\"\n")
		b.WriteString("      entryPoints: [websecure]\n")
		b.WriteString("      service: \"" + apiService + "\"\n")
		b.WriteString(tlsFor(d, o.Mode, false))
		// WAKE-ON-CALL for this domain. Priority 1 — the lowest — so a live preview's own router
		// always wins; this only catches hostnames nothing else claims, which is what a sleeping
		// stack looks like. `tls: {}` alone, exactly like the primary's: a HostRegexp gives ACME no
		// domain to order, so it serves whatever certificate already covers that name.
		b.WriteString("    pstack-wake-" + s + ":\n")
		b.WriteString("      rule: \"HostRegexp(`^[a-z0-9-]+\\\\." + strings.ReplaceAll(d, ".", "\\\\.") + "$`)\"\n")
		b.WriteString("      priority: 1\n")
		b.WriteString("      entryPoints: [websecure]\n")
		b.WriteString("      service: \"" + apiService + "\"\n")
		b.WriteString("      tls: {}\n")
	}
	return b.String()
}

// IsControlHostname reports whether a hostname is one of the control plane's own — the primary's or
// any additional domain's. Those are never a preview's to wake.
//
// The PRIMARY is checked even on a nil store, because it is the one that must never be answered
// with a waking page whatever else is missing.
func (s *RoutingStore) IsControlHostname(hostname, primary string) bool {
	h := strings.ToLower(hostname)
	for _, d := range append([]string{primary}, s.Domains()...) {
		if d == "" {
			continue
		}
		if h == "control."+strings.ToLower(d) || h == "api."+strings.ToLower(d) {
			return true
		}
	}
	return false
}
