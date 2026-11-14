# Subdomain TLS — design

**Status:** proposed, 2026-11-13. Plan: [subdomain-tls-plan.md](subdomain-tls-plan.md).

## The problem

`compose.subdomains` already routes a whole subtree to one service: `subdomains: [web]` on stack
`pr-1` makes autolabel emit a `web-pr-1-wild` router, `HostRegexp(^<label>\.web-pr-1\.<domain>$)`,
priority 2, same service (`internal/autolabel/autolabel.go:398-417`). Custom routes mirror it as
`pstack-route-<alias>-<svc>-wild` (`internal/routing/customroutes.go:143-145`). DNS already works:
a `*.<domain>` record answers `a.web-pr-1.<domain>` (RFC 4592 §2.2.1; pstack creates no
intermediate records).

Three gaps make it unusable over HTTPS:

1. **No certificate.** On a DNS-01 host the `-wild` router carries `tls=true` alone. The host's one
   pinned cert is `*.<domain>`, which covers one label, not `a.web-pr-1.<domain>`, so Traefik serves
   its default certificate. On HTTP-01 it carries `tls.certresolver=le`. A HostRegexp gives ACME no
   domain, so nothing is ordered, and HTTP-01 cannot issue wildcards anyway.
2. **No wake.** The wake catch-alls match one label (`init.go` `WakeRouterLabels`,
   `domains.go` `renderDomains`). `a.web-pr-1.<domain>` for a sleeping stack never reaches the API,
   so it 404s, even though the sleep index already stores `-wild` rules.
3. **A foot-gun.** `subdomains: {web: {host: <domain>}}` renders a priority-2 `*.<domain>` router
   that outranks the wake catch-all and swallows every sleeping stack's traffic.

The docs are also wrong in two places. `usage.md` §5b says "pstack does not write your Traefik
labels" and says DNS needs a per-PR wildcard record. The skill never mentions `subdomains`.

## Decisions (recommended defaults)

| # | Decision | Why | Cost if wrong |
|---|---|---|---|
| D1 | Opt-in stays `compose.subdomains`; no new syntax. | It exists and routes correctly. | none |
| D2 | **dns01:** the `-wild` router orders its own certificate with `tls.certresolver=le`, `tls.domains[0].main=<host>`, `tls.domains[0].sans=*.<host>`. That is one certificate per deployment per subdomains entry. Custom routes do the same for each alias `-wild` router, through the existing `tlsFor(host, "dns01", true)` pin. | It is the only way to cover a second-level wildcard. `main=<host>` reuses the pin shape `domains.go` already renders. | Rate limit: about 50 new certificates per registered domain per 7 days, shared with everything else under that domain (last-known Let's Encrypt figure, not re-checked). Hosts with heavy PR churn should know. |
| D3 | **http01, unknown:** the `-wild` router gets `tls=true` only, with no resolver, and the deploy log and `validate` say why. The deploy is not refused. | A HostRegexp resolver orders nothing and makes Traefik log an error. Refusing would break specs that deploy today and serve over HTTP or behind another proxy. | A user may miss the note and wonder about the browser error. |
| D4 | **dns-persist-01:** no per-deployment order; a note says the stored pair must cover `*.<host>`. A pair whose SANs do cover it is served by SNI with no change. | That mode's contract is "deploys stop ordering per-hostname certificates" (`routes_tls.go` tlsStatus note). | An operator on persist mode with a DNS-01 resolver in argv gets no automatic cert. Upgrade path: read `inspect.DetectChallenge` as well. |
| D5 | `depth: any` under dns01 still pins `*.<host>`. The note says deeper names get no certificate. | One label is the most a certificate can cover. | none |
| D6 | **Wake catch-alls widen** to `^([a-z0-9-]+\.)+<domain>$`, still priority 1, on the primary (labels) and on added domains (file, re-rendered at boot), so `pstack upgrade` applies both. | Without it a sleeping stack's subtree 404s. Exact `control.`/`api.` routers still win on priority. | Every control render golden moves once. An unknown deep name gets the same answer an unknown one-label name gets today. |
| D7 | Refuse a subdomains `host` that is a domain this host serves, a parent of one (with `depth: any` a parent's subtree contains the domain), or a control hostname. Refuse it in the spec against the preview domain, and in autolabel against every served domain. | Closes gap 3. | none: such a spec is broken today |
| D8 | Docs state the real DNS rule and its one caveat: while lego's `_acme-challenge.<host>` TXT record exists, `<host>` becomes an empty non-terminal. `<host>` and its subtree then stop resolving through `*.<domain>`, and resolvers cache the miss for the zone's negative TTL. This happens at first issue and about every 60 days at renewal. | It is true, and it only shows up in production. | Owner may prefer a per-PR explicit wildcard record instead. That is out of scope here. |
| D9 | Matching stays as it is: an entry attaches to the service whose routing name equals it, and it must also be a compose profile. An entry that attaches to no routed service gets a note. | Changing it would move hostnames. Today the no-match case is silent. | none |

## Custom routes (the owner's question)

Yes, they work today for routing. An alias `checkout` on a stack whose `web` service has
subdomains already renders `pstack-route-checkout-web-wild` for `*.web-checkout.<domain>`, and
the sleep index includes its rule. They have the same TLS gap and the same wake gap. D2, D3, D4 and
D6 close both, with the same mode rules:

- Under dns01 each `(alias, subdomain service)` pair orders one certificate. That overturns the
  file's "never a pin per alias" ruling for `-wild` routers only; plain alias routers keep inheriting
  `*.<domain>`.
- The alias host does not change when the route moves to another stack, so the certificate in
  `acme.json` is reused, not reordered.
- Boot and every deploy already re-render the routes file, so an upgraded host stamps the new TLS
  block without manual steps. Deployments need `POST /api/tls/redeploy` (or their next `up`) for
  their own labels.

## Not in scope

- Certificates deeper than one label (impossible by construction).
- Creating DNS records.
- An explicit per-PR wildcard record to avoid D8's window.
- Reading the argv resolver under dns-persist-01 (D4's upgrade path).

## Backlog: named subdomains over HTTP-01 (deferred 2026-11-14)

The owner asked whether one host can mix modes: the first level from the DNS-01 wildcard, the
second level from HTTP-01. That works for exact names, but not for a second-level wildcard, because
ACME issues wildcards only over DNS-01. Sketch:

- Spec: `subdomains: {web: {names: [api, admin]}}`, which can't be combined with `depth`. It renders
  one exact router, `Host(api.web-pr-1.<d>) || Host(admin.web-pr-1.<d>)`, and no `-wild` router.
  Custom routes mirror it the same way.
- TLS: on http01 the existing `le` resolver issues it. On dns01, init adds a second resolver,
  `le-http` (`httpchallenge.entrypoint=web`, same storage), and the router uses that. Expect one
  certificate per deployment covering the listed names (Traefik derives domains from the rule; not
  verified).
- Gains:
  - HTTP-01 hosts get subdomain certificates, which they don't under D3.
  - No `_acme-challenge` record, so D8's resolution window never happens.
  - No DNS provider calls.
- Costs:
  - On a dns01 host, port 80 must be reachable from the internet, which some DNS-01 hosts chose
    DNS-01 to avoid. Those hosts get no certificate, so the deploy note must say why.
  - Control render goldens move once; `pstack upgrade` applies it.
  - D6's widened wake rule already covers these names.
