// Grafana at G = https://grafana.<domain> (`pstack init --logging loki`), signed in with pstack
// accounts. Spec of record: docs/loki-logging-design.md, "Slice 3".
//
// ── IS IT ON ─────────────────────────────────────────────────────────────────────────────────────
//
// Read from docker (inspect.GrafanaOn) and cached in s.grafana by Start and reindexLoop. It is never a
// setting and never asked per request, so a `pstack logging` switch shows within 30 s (the next
// reindexLoop tick), with no pstack restart. Sign-in also needs PSTACK_DOMAIN (every URL here is
// built from config, never from a request header) and PSTACK_TOKEN (the cookie's MAC key). When off,
// /api/health has no `grafana` key.
package api

// grafanaOn: this host runs Grafana (s.grafana, from docker) and has what sign-in needs.
func (s *Server) grafanaOn() bool {
	return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()
}

// grafanaURL is G. From config, never from a request header.
func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }
