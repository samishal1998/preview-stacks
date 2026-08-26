// `GET /api/probe/<id>` — "is this preview serving?", answered WITHOUT a token and WITHOUT
// depending on the preview's own certificate.
//
// ── WHY IT EXISTS ────────────────────────────────────────────────────────────────────────────────
//
// Under HTTP-01 every preview hostname resolves its OWN certificate on its first HTTPS request, so
// a CI job that polls `https://app-pr-123.example.com` right after a deploy is waiting on ACME, not
// on the app — and the more stacks a host carries, the more of that waiting there is. This route
// rides a hostname whose certificate is already warm (the control plane's own, obtained when `init`
// ran and waited for the healthcheck) and asks the container directly, over `preview-ingress`. The
// answer is available the moment the container answers, whatever ACME is doing.
//
// The real fix for slow issuance is a wildcard, which is what `--challenge dns01` already does: one
// router holds `${DOMAIN}` + `*.${DOMAIN}` and every per-PR router inherits it by SNI. This route is
// not a substitute for that; it is what CI polls in either mode.
//
// ── UNAUTHENTICATED, AND THEREFORE DELIBERATELY MUTE ─────────────────────────────────────────────
//
// It sits in the pre-gate beside `/api/health`. `docs/secret-exposure.md` records what unauthenticated
// reads cost this project once — job outcomes carry captured credentials by design, and for a while
// anyone could read them — so the rule here is absolute and structural rather than careful:
//
//   - **No body. Ever.** Not the upstream's, not an error message, not a byte. `Content-Length: 0`
//     on every path. An endpoint that cannot return content cannot become a data feed, which is the
//     failure mode that finding was about.
//   - **No upstream headers.** They carry `Set-Cookie`, `Server`, framework banners.
//   - **No redirects followed.** Otherwise it is an open prober for whatever a preview redirects to.
//   - **The path is fixed at `/`.** Never taken from the query: a caller-chosen path would reach
//     endpoints a preview's own Traefik middleware protects, from a route that has no auth at all.
//
// What it does disclose, unavoidably and by design: whether a deployment id exists, and whether it
// is up. That is the question being asked. `PSTACK_PROBE=off` turns the route off for a host that
// does not want even that much (it then 404s like any unknown path).
//
// ── IT NEVER WAKES ANYTHING ──────────────────────────────────────────────────────────────────────
//
// A sleeping stack answers `503 asleep` and stays asleep. `wakeFor` triggers on PREVIEW hostnames
// and this route is reached on the control plane's, so it is not in that path to begin with — but
// the sleep record is checked explicitly all the same, because an unauthenticated route that starts
// a `docker compose up` is an unauthenticated deploy, and relying on "the other mechanism happens
// not to fire here" is not a boundary.
//
// ── THE ANSWER IS THE STATUS CODE, THE REASON IS A HEADER ────────────────────────────────────────
//
// `curl -o /dev/null -w '%{http_code}'` is the intended client, so the upstream's status is passed
// through unchanged — a 404 from the app means the app answered, which is what "is it serving"
// asks. `x-pstack-probe` says which kind of answer it is, because a 503 the app produced and a 503
// meaning "asleep" are different facts:
//
//	upstream    the container answered; the status is ITS status
//	unknown     no such deployment (404)
//	asleep      sleeping, and left that way (503)
//	no-target   nothing to dial: no router, no port, or not on preview-ingress (503)
//	unresolved  the spec would not resolve without request variables (503)
//	unreachable the dial or the read failed, or took longer than the timeout (502)
//	busy        too many probes in flight (503)
package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
)

// probeTimeout bounds one upstream request. Short on purpose: the caller is polling, and a probe
// that hangs is worse than one that says `unreachable` and gets asked again.
const probeTimeout = 3 * time.Second

// probeSlots bounds how much WORK an unauthenticated caller can start. Each probe resolves the
// deployment's routers through docker, which is several `docker inspect` calls, so this is the
// amplification bound: at most this many in flight, the rest answered `busy` immediately.
//
// ponytail: a fixed semaphore, not a rate limiter or a per-target cache. If polling at scale makes
// `busy` common, cache the resolved target per stack for a few seconds — the address only changes
// when the stack is redeployed.
const probeSlots = 4

var probeRe = regexp.MustCompile(`^/api/probe/([^/]+)$`)

// probeAnswer writes the whole response: a status, one header, and no body.
func probeAnswer(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("x-pstack-probe", reason)
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("content-length", "0")
	w.WriteHeader(status)
}

// probe is GET /api/probe/<id>, in the pre-gate. Returns false when the path is not a probe, so the
// caller falls through to the rest of the chain.
func (s *Server) probe(w http.ResponseWriter, r *http.Request, path string) bool {
	m := probeRe.FindStringSubmatch(path)
	if m == nil || s.opts.ProbeOff {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		probeAnswer(w, 405, "unknown")
		return true
	}
	select {
	case s.probeSem <- struct{}{}:
		defer func() { <-s.probeSem }()
	default:
		probeAnswer(w, 503, "busy")
		return true
	}

	dep, err := s.registry.Get(m[1])
	if err != nil || dep == nil {
		probeAnswer(w, 404, "unknown")
		return true
	}
	// Before anything else, and never followed by a wake.
	if dep.Sleep != nil {
		probeAnswer(w, 503, "asleep")
		return true
	}
	// No request variables: this route takes none, so a spec that cannot resolve from its STORED
	// vars is reported rather than guessed at.
	st, err := s.resolveDep(dep.ID, map[string]string{})
	if err != nil {
		probeAnswer(w, 503, "unresolved")
		return true
	}
	target := probeTarget(inspect.DeploymentRuntime(inspect.RuntimeArgs{
		Stack:        st.Stack,
		Runner:       s.host,
		Challenge:    inspect.DetectChallenge(s.host),
		AllRouters:   inspect.AllTraefikRouters(s.host).ByName,
		Orchestrator: orchestratorOf(st),
	}), r.URL.Query().Get("service"))
	if target == "" {
		probeAnswer(w, 503, "no-target")
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	// Fixed path, plaintext, inside preview-ingress. The address came from the registry and docker,
	// never from the request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+target+"/", nil)
	if err != nil {
		probeAnswer(w, 502, "unreachable")
		return true
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		probeAnswer(w, 502, "unreachable")
		return true
	}
	// Drained and closed without being read: the body is never forwarded, and an unread body leaks
	// the connection.
	_ = resp.Body.Close()
	probeAnswer(w, resp.StatusCode, "upstream")
	return true
}

// probeClient never follows a redirect — a 301 IS the answer, and following it would let a preview
// point this route at anything it likes.
var probeClient = &http.Client{
	Timeout:       probeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// probeTarget picks the address to dial: the routers' targets, sorted by router name so a stack with
// several answers the same one every time. `service` filters by the router's service name when the
// caller names one — an empty filter takes the first.
//
// A router without a target is skipped rather than reported: `TargetReason` already says why the
// runtime page cannot show one, and this route's answer for "none of them are dialable" is the same
// `no-target` however many of them there are.
func probeTarget(rt inspect.Runtime, service string) string {
	routes := append([]inspect.RouteInfo(nil), rt.Routes...)
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Router < routes[j].Router })
	for _, ro := range routes {
		if ro.Target == nil || *ro.Target == "" {
			continue
		}
		if service != "" && (ro.Service == nil || !strings.EqualFold(*ro.Service, service)) {
			continue
		}
		return *ro.Target
	}
	return ""
}
