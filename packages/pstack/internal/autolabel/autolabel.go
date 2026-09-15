// Package autolabel generates the Traefik labels and network wiring a preview service needs, from
// one short label — and, on a host that runs Loki, the logging block that ships its output there.
//
// THE PROBLEM. A reachable preview service needs four Traefik labels and two network declarations,
// every one of which has a silent failure mode — a missing `traefik.enable` makes the container
// invisible, a non-`external` network makes it unreachable while looking correct, a router name
// without the stack in it makes two PRs serve each other's containers. All six are boilerplate that
// differs only by service name and port, and getting any one wrong produces a 404 with nothing logged.
//
// So declare the part that is actually yours:
//
//	services:
//	  app:
//	    image: nginx:alpine
//	    profiles: [app]
//	    labels:
//	      - pstack.routing.port=80
//
// and pstack writes the rest, correctly, including the TLS rule that inverts between challenge modes.
//
// ── IT NEVER OVERRIDES YOU ───────────────────────────────────────────────────────────────────────
//
// A service carrying ANY `traefik.*` label is left completely alone — labels, networks and all. That
// is the escape hatch: the moment you need something this cannot express (a middleware chain, two
// routers on one service, a non-standard hostname), write the labels yourself and pstack stops having
// an opinion. There is no flag to remember and no merge to reason about; the presence of your own
// label IS the opt-out.
//
// A service with neither a `pstack.routing.*` label nor a `traefik.*` one is also left alone — that is
// a database or a worker, and forcing a hostname onto it would be wrong.
//
// ── NOR DOES IT HAND OUT A CONTROL HOSTNAME ──────────────────────────────────────────────────────
//
// A `pstack.routing.host` naming `control.`, `api.` or `loki.` of any domain this host answers on
// is refused (ControlHostname): its router would compete with the control plane's own for the
// console, the API, or every node's log pushes and the password on them. `loki.` is reserved even
// with logging off. A service with its own `traefik.*` labels is not checked — that is the escape
// hatch above, and specs are CI-trusted.
//
// The refusal is a spec error like the missing-domain one, so it fires on EVERY compose subcommand
// that re-materializes the file — `down` included, where stack.Down records it as non-fatal.
//
// ── WHY A DERIVED FILE AND NOT AN OVERLAY ────────────────────────────────────────────────────────
//
// The obvious implementation is a second `-f` overlay adding labels. It was rejected in 0.3.0 for the
// same reason it is rejected here: Compose's merge semantics for list-form `labels` decide whether
// YOUR routers survive the merge, and getting that wrong deletes them silently. Instead pstack reads
// the submitted file, transforms it in memory, and writes a COMPLETE derived file that compose reads
// on its own — no merge, nothing to reason about, and the result is inspectable on disk.
//
// The derived file is **JSON**, which is valid YAML 1.2 and what every YAML parser agrees on. Emitting
// YAML would mean trusting a stringifier's quoting to match Go's parser on values like
// Host(`app.example.com`) — a mismatch that would only show up on the user's host. Your original
// file is never modified; this is written beside it.
//
// ── SWARM ────────────────────────────────────────────────────────────────────────────────────────
//
// Under `compose.orchestrator: swarm` the same pipeline ALWAYS writes the derived file, whether or not
// anything asked for routing: the submitted file is converted to the swarm subset first (swarm —
// profiles resolved, `restart`/`mem_limit` mapped to `deploy`, unsupported keys dropped and named).
// Generated labels go under `deploy.labels` with `traefik.swarm.network`, because Traefik's swarm
// provider reads service labels and its docker provider must not see a second copy on the task.
//
// ── LOGGING ──────────────────────────────────────────────────────────────────────────────────────
//
// On a host that runs Loki (`pstack init --logging loki`), every service WITHOUT a `logging` key
// gets the loki driver block — after the labels, before the swarm conversion, so one step serves
// both orchestrators. Whether Loki runs is not a setting: DetectLogging reads the push URL off the
// control stack's loki container, the way DetectChallenge reads Traefik, so `pstack up` on the host
// and the API always inject the same thing. No container, no block.
//
// A service with ANY `logging` key — `json-file`, even an empty one — is left alone and named in
// the job log. The traefik.* rule again, for the same reason: pstack's options could be refused by
// the user's driver, so nothing is merged.
//
// Every option but the URL and the `service_name` label is a constant, never a setting. When Loki
// is unreachable the driver holds a node-wide lock while it retries, so every `docker stop` on that
// node waits out the retry loop; only small retries, timeout and backoff bound the wait to seconds.
//
// The consequence under compose: a stack with no routing labels runs from the derived file whenever
// a service got the block. With logging off nothing here runs, and the old gates hold exactly.
//
// ── THE CHALLENGE PROBE (a Go-only note) ─────────────────────────────────────────────────────────
//
// The TS called inspect.ts's detectChallenge directly; so does this package — the DetectChallenge
// variable defaults to inspect's probe and exists only so tests can pin a mode without scripting
// docker. When the probe cannot answer, the mode is `unknown` — which errs toward including the
// certresolver, the cheaper mistake.
package autolabel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// GeneratedCompose is the file pstack writes beside the submitted compose file. JSON, despite the
// extension — see the package comment.
const GeneratedCompose = "compose.generated.yml"

// Ingress and Shared are the two host-wide networks every preview joins.
const (
	Ingress = "preview-ingress"
	Shared  = "preview-shared"
)

// Challenge is the ACME challenge mode the running Traefik uses.
type Challenge string

const (
	HTTP01  Challenge = "http01"
	DNS01   Challenge = "dns01"
	Unknown Challenge = "unknown"
	// DNSPersist is the bring-your-own-wildcard mode: a certificate pair stored beside Traefik's
	// dynamic config covers every preview by SNI, so per-PR routers must not order their own —
	// the same label rule as DNS01, detected from the stored file rather than Traefik's argv.
	DNSPersist Challenge = "dns-persist-01"
)

// wildcardMode is the label question: does ONE always-on certificate cover every preview, so a
// per-router certresolver would order a redundant one (and, at PR volume, burn the weekly limit)?
func wildcardMode(c Challenge) bool { return c == DNS01 || c == DNSPersist }

// DetectChallenge asks the host which challenge mode it runs — behind a variable so tests can pin a
// mode without scripting docker. The stored wildcard is checked FIRST because it overrides whatever
// Traefik's argv still says (the argv keeps its init-time resolver flags in dns-persist mode); then
// inspect's argv probe. A host that answers neither is Unknown, which errs toward including the
// certresolver (see AugmentComposeDoc).
// The dynamic dir is resolved from the PROCESS ENVIRONMENT here, while the server holds its own
// Options.RoutingDir — `pstack serve` computes both from the same DynamicDir over the same env, so
// they agree. An embedder that passes a custom RoutingDir without setting PSTACK_ROUTING_DIR would
// split them: the API would report dns-persist-01 while deploys kept stamping certresolver, which
// is the exact burn this mode exists to stop. Keep the two resolutions on one source.
var DetectChallenge = func(r exec.Runner) Challenge {
	if routing.New(routing.DynamicDir(registry.DataDir())).WildcardActive() {
		return DNSPersist
	}
	return Challenge(inspect.DetectChallenge(r))
}

// DetectLogging is the push URL deploys ship logs to, "" when this host's logging is off — behind
// a variable for the same reason as DetectChallenge. The default reads the pstack.logging.push-url
// label off the control stack's loki container (inspect.LokiPushURL), so the CLI and the API agree
// with no setting to keep in sync.
var DetectLogging = func(r exec.Runner) string { return inspect.LokiPushURL(r) }

// ControlHostname reports whether a hostname is the control plane's — `control.`, `api.` or `loki.`
// of the primary domain or any added one — behind a variable, like DetectChallenge, so a caller's
// test can pin it. The added domains come from routing.DynamicDir. The primary is PSTACK_DOMAIN,
// which the control container always sets; `pstack up` run host-side has no such variable, so it
// falls back to DOMAIN= in <DataDir>/control/.env — the same file `init` wrote it to — read here
// rather than via upgrade.ReadControlState, which already imports this package and would cycle.
var ControlHostname = func(host string) bool {
	primary := os.Getenv("PSTACK_DOMAIN")
	if primary == "" {
		primary = domainFromControlEnv()
	}
	return routing.New(routing.DynamicDir(registry.DataDir())).IsControlHostname(host, primary)
}

// domainFromControlEnv reads DOMAIN= from control/.env — the KEY=VALUE grammar
// upgrade.ReadControlState parses the same file with. A missing file or line is "", which
// IsControlHostname skips, exactly like an unset primary.
func domainFromControlEnv() string {
	b, err := os.ReadFile(filepath.Join(registry.DataDir(), "control", ".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DOMAIN="); ok {
			return v
		}
	}
	return ""
}

// RoutingRequest is what a service asked for, read from its `pstack.routing.*` labels.
type RoutingRequest struct {
	// Port is the port INSIDE the container. Traefik dials the container's ingress IP on this.
	Port int
	// Name is used for the hostname and for the router/service ids. Defaults to the compose service
	// name, which is what `pstack.routing.service_name` overrides.
	Name string
	// Host is a complete hostname, when the `<name>-<stack>.<domain>` convention does not fit. ""
	// when absent.
	Host string
}

// AugmentResult is the transformed document and what was done to it.
type AugmentResult struct {
	// Doc is the transformed document, ready to serialise.
	Doc *omap.Map
	// Generated is service name → the labels pstack added ([]string), for reporting. Ordered.
	Generated *omap.Map
	// Skipped is service name → why it was left alone (string). Ordered.
	Skipped *omap.Map
}

// LabelsToMap normalises a service's labels (list or mapping) to an ordered mapping. Defined in
// swarm because of the import graph; the name is kept here because this is where it belongs.
func LabelsToMap(labels any) *omap.Map { return swarm.LabelsToMap(labels) }

// ReadRoutingRequest reads a service's routing request.
//
// Returns nil, nil when it asked for nothing. Errors on a port that is not a port: a service that
// declares `pstack.routing.port=web` wanted routing and will not get it, and silently skipping is how
// that becomes a 404 nobody can explain.
func ReadRoutingRequest(service string, labels *omap.Map) (*RoutingRequest, error) {
	portRaw, hasPort := labels.Get("pstack.routing.port")
	name := labels.GetString("pstack.routing.service_name")
	if name == "" {
		name = service
	}
	host := labels.GetString("pstack.routing.host")
	if !hasPort {
		// A name or host without a port cannot produce a working router, and asking for one implies the
		// rest was meant to be there.
		if labels.GetString("pstack.routing.service_name") != "" || labels.GetString("pstack.routing.host") != "" {
			return nil, &spec.Error{Msg: fmt.Sprintf(`service "%s" sets pstack.routing.* but no pstack.routing.port, so no router can be generated — Traefik needs the port inside the container.`, service)}
		}
		return nil, nil
	}
	raw, _ := portRaw.(string)
	port := js.ParseNumber(raw)
	if !js.IsInteger(port) || port < 1 || port > 65535 {
		return nil, &spec.Error{Msg: fmt.Sprintf(`service "%s" has pstack.routing.port="%s", which is not a port number. Use the port INSIDE the container, e.g. 80.`, service, raw)}
	}
	return &RoutingRequest{Port: int(port), Name: name, Host: host}, nil
}

// AugmentArgs is what AugmentComposeDoc takes.
type AugmentArgs struct {
	Doc       *omap.Map
	Spec      *spec.Stack
	Challenge Challenge
	// Domain overrides the spec's own resolution when non-nil (even when empty).
	Domain *string
}

// asList renders a labels mapping as a `k=v` list.
func asList(m *omap.Map) []any {
	out := make([]any, 0, m.Len())
	m.Each(func(k string, v any) {
		if v == "" {
			out = append(out, k)
		} else {
			out = append(out, k+"="+v.(string))
		}
	})
	return out
}

func hasTraefik(m *omap.Map) bool {
	for _, k := range m.Keys() {
		if strings.HasPrefix(k, "traefik.") {
			return true
		}
	}
	return false
}

// AugmentComposeDoc adds the labels and networks a routed service needs.
//
// `challenge` decides one line and inverts between modes: under HTTP-01 every router resolves its own
// certificate and so needs `tls.certresolver`; under DNS-01 one always-on router holds the wildcard and
// a per-router certresolver makes each PR order its own, burning the weekly rate limit. Getting this
// from the running Traefik (see DetectChallenge) rather than a setting means the generated labels
// cannot disagree with the host they are deployed to.
func AugmentComposeDoc(a AugmentArgs) (*AugmentResult, error) {
	st, challenge := a.Spec, a.Challenge
	// Structurally clone so a caller's document is never mutated — this runs on every compose command.
	doc := a.Doc.Clone()
	generated := omap.New()
	skipped := omap.New()

	servicesV, _ := doc.Get("services")
	var services *omap.Map
	switch x := servicesV.(type) {
	case nil:
		services = omap.New()
	case *omap.Map:
		services = x
	default:
		return nil, &spec.Error{Msg: "`services` in the compose file must be a mapping"}
	}

	anyRouted := false

	for _, name := range services.Keys() {
		svcRaw, _ := services.Get(name)
		svc, isMap := svcRaw.(*omap.Map)
		if !isMap {
			// A scalar or null is skipped outright; an array is an object to JS and reaches the "asked
			// for nothing" branch with no labels.
			if _, isList := svcRaw.([]any); !isList {
				continue
			}
			skipped.Set(name, "no pstack.routing.port — not routed (a database or worker needs no hostname)")
			continue
		}
		lv, _ := svc.Get("labels")
		labels := LabelsToMap(lv)
		// A swarm-shaped file may already carry its labels where the swarm provider reads them.
		deploy := svc.GetMap("deploy")
		if deploy == nil {
			deploy = omap.New()
		}
		dlv, _ := deploy.Get("labels")
		deployLabels := LabelsToMap(dlv)

		// Your labels win, entirely. Not merged, not partially applied.
		if hasTraefik(labels) || hasTraefik(deployLabels) {
			skipped.Set(name, "has its own traefik.* labels")
			continue
		}

		merged := deployLabels.Clone()
		labels.Each(func(k string, v any) { merged.Set(k, v) })
		req, err := ReadRoutingRequest(name, merged)
		if err != nil {
			return nil, err
		}
		if req == nil {
			skipped.Set(name, "no pstack.routing.port — not routed (a database or worker needs no hostname)")
			continue
		}

		// Same resolution as the spec's own, so a hostname generated here and one generated for
		// `compose.subdomains` can never be anchored to different domains.
		var domain string
		if a.Domain != nil {
			domain = *a.Domain
		} else {
			domain = spec.ResolvePreviewDomain(st.Env, st.DeclaredEnv, nil)
		}
		if req.Host == "" && domain == "" {
			return nil, &spec.Error{Msg: fmt.Sprintf(`service "%s" asks for routing but there is no domain to build a hostname from. Declare it in the spec (env:\n  PREVIEW_DOMAIN: preview.example.com) or set pstack.routing.host on the service. DOMAIN is accepted as a legacy alias.`, name)}
		}

		// The stack goes in the router and service ids because Traefik's namespace is GLOBAL across the
		// daemon: two deployments sharing a router name overwrite each other, and one hostname starts
		// serving the other's container.
		id := req.Name + "-" + st.Stack
		host := req.Host
		if host == "" {
			host = req.Name + "-" + st.Stack + "." + domain
		}
		// A preview never gets a control hostname — see the package comment. Only an EXPLICIT host can
		// collide: a generated one's first label is `<name>-<stack>`, which always has a dash in it.
		if req.Host != "" && ControlHostname(req.Host) {
			return nil, &spec.Error{Msg: fmt.Sprintf(`service "%s" sets pstack.routing.host=%s — a control hostname of this host (control., api. and loki. on every domain it answers on). Pick another host.`, name, req.Host)}
		}

		isSwarm := st.Compose != nil && st.Compose.Orchestrator == spec.Swarm
		networkKey := "docker"
		if isSwarm {
			networkKey = "swarm"
		}
		added := []string{
			// The host runs `--providers.docker.exposedbydefault=false`, so without this the container is
			// invisible to Traefik and the hostname 404s with nothing logged anywhere.
			"traefik.enable=true",
			// This container is on more than one network; Traefik must be told which to dial. The swarm
			// provider has its own key for the same setting.
			"traefik." + networkKey + ".network=" + Ingress,
			"traefik.http.routers." + id + ".rule=Host(`" + host + "`)",
			"traefik.http.routers." + id + ".entrypoints=websecure",
			"traefik.http.routers." + id + ".tls=true",
			"traefik.http.routers." + id + ".service=" + id,
			// The port INSIDE the container. Traefik resolves the container's ingress IP and dials this on
			// it — no host-port publishing is involved.
			"traefik.http.services." + id + ".loadbalancer.server.port=" + js.ToString(req.Port),
		}
		if !wildcardMode(challenge) {
			// Under HTTP-01 each hostname resolves its own certificate. Included when the mode is unknown
			// too: a missing certresolver on an HTTP-01 host means no certificate at all, while a spare one
			// on a wildcard-carrying host costs a redundant certificate — the cheaper mistake.
			added = append(added, "traefik.http.routers."+id+".tls.certresolver=le")
		}

		// A wildcard subdomain router, when the spec asked for one for this profile. Same service, lower
		// priority, so any exact host — including the one just generated — still wins.
		if st.Compose != nil {
			for _, sub := range st.Compose.Subdomains {
				if sub.Profile != req.Name {
					continue
				}
				added = append(added,
					"traefik.http.routers."+id+"-wild.rule="+spec.WildcardRule(sub.Host, sub.Depth),
					"traefik.http.routers."+id+"-wild.priority=2",
					"traefik.http.routers."+id+"-wild.entrypoints=websecure",
					"traefik.http.routers."+id+"-wild.tls=true",
					"traefik.http.routers."+id+"-wild.service="+id,
				)
				if !wildcardMode(challenge) {
					added = append(added, "traefik.http.routers."+id+"-wild.tls.certresolver=le")
				}
				break
			}
		}

		// Keep whatever the user wrote (the pstack.routing.* labels included, so the file stays
		// self-describing) and append. Always a list: mixing forms in one file is legal but confusing.
		// Under swarm the generated labels are SERVICE labels (`deploy.labels`) — that is where Traefik's
		// swarm provider looks, and the container labels stay exactly as submitted.
		addedAny := make([]any, len(added))
		for i, l := range added {
			addedAny[i] = l
		}
		if isSwarm {
			deploy.Set("labels", append(asList(deployLabels), addedAny...))
			svc.Set("deploy", deploy)
		} else {
			svc.Set("labels", append(asList(labels), addedAny...))
		}

		// Networks: append, never replace. A service already on its own networks keeps them.
		existing := []string{}
		if n := svc.GetSlice("networks"); n != nil {
			for _, e := range n {
				if s, ok := e.(string); ok {
					existing = append(existing, s)
				}
			}
		} else if nm := svc.GetMap("networks"); nm != nil {
			existing = nm.Keys()
		}
		if len(existing) == 0 {
			existing = []string{"default"}
		}
		wanted := []any{}
		seen := map[string]bool{}
		for _, n := range append(existing, Ingress) {
			if !seen[n] {
				seen[n] = true
				wanted = append(wanted, n)
			}
		}
		svc.Set("networks", wanted)

		generated.Set(name, added)
		anyRouted = true
	}

	// Root networks. Only the ones that are missing, so a user's own definition is never overwritten —
	// and `external: true` is the whole point: declared without it, compose creates
	// `<project>_preview-ingress` and the container is up, healthy and unreachable.
	if anyRouted {
		networks := doc.GetMap("networks")
		if networks == nil {
			networks = omap.New()
		}
		if !networks.Has("default") {
			networks.Set("default", omap.New())
		}
		if !networks.Has(Ingress) {
			networks.Set(Ingress, omap.From("external", true))
		}
		// Declared even when nothing uses it: every per-PR file in the wild references it, and an absent
		// external network is a hard compose error rather than a quiet one.
		if !networks.Has(Shared) {
			networks.Set(Shared, omap.From("external", true))
		}
		doc.Set("networks", networks)
	}

	return &AugmentResult{Doc: doc, Generated: generated, Skipped: skipped}, nil
}

// InjectLogging gives every service without a `logging` key the loki driver block, and returns the
// services that got it and the ones left alone because they have their own, both in file order.
//
// Pure: the document is cloned, as in AugmentComposeDoc. The `service_name=<stack>-<service>`
// label replaces the driver's default container_name, whose swarm value carries the task id and
// would start new streams on every redeploy; `filename` is dropped for the same churn — a busy host
// would reach Loki's stream limit and have new streams refused. The other options are constants on
// purpose (see the package comment).
func InjectLogging(doc *omap.Map, stack, pushURL string) (out *omap.Map, logged, own []string) {
	out = doc.Clone()
	logged, own = []string{}, []string{}
	services := out.GetMap("services")
	for _, name := range services.Keys() {
		svc := services.GetMap(name)
		if svc == nil {
			// A scalar, list or null service has nowhere to put the key; compose reports it better.
			continue
		}
		// Yours wins, entirely: a `logging` key of any shape — json-file, empty, null — opts out,
		// as a traefik.* label does for routing. Nothing is merged: your driver may refuse ours.
		if svc.Has("logging") {
			own = append(own, name)
			continue
		}
		svc.Set("logging", omap.From(
			"driver", "loki",
			"options", omap.From(
				"loki-url", pushURL,
				"loki-external-labels", "service_name="+stack+"-"+name,
				"loki-relabel-config", "[{action: labeldrop, regex: filename}]",
				// Small, so a stop on a node whose Loki is unreachable waits seconds, not minutes.
				"loki-retries", "2",
				"loki-timeout", "1s",
				"loki-max-backoff", "800ms",
				// Protects the app's stdout from a slow push. It does not protect the stop.
				"mode", "non-blocking",
				// `true` leaves one directory per container id in the plugin, forever.
				"keep-file", "false",
				"max-size", "10m",
				"max-file", "3",
			),
		))
		logged = append(logged, name)
	}
	return out, logged, own
}

// MaterializeArgs is what MaterializeCompose takes.
type MaterializeArgs struct {
	Dir    string
	Spec   *spec.Stack
	Runner exec.Runner
	// Challenge skips the challenge probe when the caller already knows (tests, or a batch).
	// Logging discovery still runs — pin DetectLogging to skip that too.
	Challenge *Challenge
}

// MaterializeResult is the filename compose should use and what was done.
type MaterializeResult struct {
	File      string
	Generated *omap.Map
	Skipped   *omap.Map
	// Notes is what the swarm conversion changed, one line each. Empty under compose.
	Notes []string
	// Logged is the services that got the loki logging block, in file order. Empty, never nil.
	Logged []string
	// LogNotes is one job-log line per service left alone because it has its own `logging:`. Empty,
	// never nil.
	LogNotes []string
}

// MaterializeCompose reads the submitted compose file, augments it, and writes the derived file next
// to it.
//
// `dir` is what relative paths in the spec resolve against: the deployment directory under the API,
// the process's working directory under the CLI (exec: the CLI runner sets no cwd, so docker runs
// from the shell's).
//
// Returns the filename compose should use — the derived one when anything was generated or a
// service got the logging block, and the original otherwise, so a deployment that writes its own
// labels gets exactly the file it submitted with no derived artefact left lying around.
//
// Regenerated on every compose invocation rather than cached at submit time, because the labels depend
// on the RESOLVED spec: `up` with one set of variables and `down` with another must not disagree about
// what the router was called.
func MaterializeCompose(a MaterializeArgs) (*MaterializeResult, error) {
	original := a.Spec.Compose.File
	source := filepath.Join(a.Dir, original)
	isSwarm := a.Spec.Compose.Orchestrator == spec.Swarm
	// Beside the ORIGINAL, not at `dir`: compose resolves a file's relative paths (bind mounts,
	// env_file) against the first `-f` file's directory, so the derived file must sit where the
	// submitted one does. In a deployment directory that is `dir` itself; under the CLI it is
	// wherever `-f` pointed.
	generatedRel := filepath.Join(filepath.Dir(original), GeneratedCompose)
	untouched := func() *MaterializeResult {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: omap.New(), Notes: []string{}, Logged: []string{}, LogNotes: []string{}}
	}

	rawBytes, err := os.ReadFile(source)
	if err != nil {
		// Not an error here: the file may legitimately live somewhere this process cannot read, and
		// compose will report a missing file far better than a guess would.
		return untouched(), nil
	}
	raw := string(rawBytes)

	parsed, err := yamlx.ParseString(raw)
	if err != nil {
		return nil, &spec.Error{Msg: "compose file " + original + " is not valid YAML: " + strings.TrimPrefix(err.Error(), "not valid YAML: ")}
	}
	doc, ok := parsed.(*omap.Map)
	if !ok {
		return nil, &spec.Error{Msg: "compose file " + original + " must be a mapping"}
	}

	// Discovery runs before the pre-check because Loki on its own is a reason to write the derived
	// file — but only once the file parsed, so a missing or broken file still costs no docker call.
	pushURL := DetectLogging(a.Runner)

	// Cheap pre-check: no pstack.routing.* anywhere and no Loki means nothing to generate, and no
	// reason to ask docker for the challenge mode. Under swarm the conversion still has to run.
	wantsRouting := strings.Contains(raw, "pstack.routing.")
	if !wantsRouting && !isSwarm && pushURL == "" {
		return untouched(), nil
	}

	out := doc
	generated := omap.New()
	skipped := omap.New()
	if wantsRouting {
		challenge := Unknown
		if a.Challenge != nil {
			challenge = *a.Challenge
		} else {
			challenge = DetectChallenge(a.Runner)
		}
		result, err := AugmentComposeDoc(AugmentArgs{Doc: out, Spec: a.Spec, Challenge: challenge})
		if err != nil {
			return nil, err
		}
		out = result.Doc
		generated = result.Generated
		skipped = result.Skipped
	}
	// After the labels and before the swarm conversion: one step for both orchestrators, and
	// Swarmify keeps `logging`.
	logged, logNotes := []string{}, []string{}
	if pushURL != "" {
		var own []string
		out, logged, own = InjectLogging(out, a.Spec.Stack, pushURL)
		for _, svc := range own {
			logNotes = append(logNotes, "logging: service "+svc+" has its own logging: — left alone")
		}
	}
	notes := []string{}
	if isSwarm {
		// After the labels, so the generated ones are already where the swarm provider reads them and the
		// conversion only has to move what the author wrote by hand.
		converted := swarm.Swarmify(out, a.Spec.Compose.Profiles)
		out = converted.Doc
		notes = converted.Notes
	}
	if !isSwarm && generated.Len() == 0 && len(logged) == 0 {
		return &MaterializeResult{File: original, Generated: omap.New(), Skipped: skipped, Notes: []string{}, Logged: []string{}, LogNotes: logNotes}, nil
	}

	// JSON, which every YAML parser reads identically. Written beside the original, never over it.
	b, err := jsonx.MarshalIndent(out)
	if err != nil {
		return nil, &spec.Error{Msg: "compose file " + original + " could not be serialised: " + err.Error()}
	}
	path := filepath.Join(a.Dir, generatedRel)
	if err := os.WriteFile(path, append(b, '\n'), 0o666); err != nil {
		return nil, err
	}
	// The injected loki-url carries the push password, and deployments/<id>/ is world-readable. The
	// `chmod` is separate because a create-time mode applies only on CREATE: a stack first deployed
	// with logging off already has this file at 0644 (initctl.write splits it for the same reason).
	if pushURL != "" {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, err
		}
	}
	return &MaterializeResult{File: generatedRel, Generated: generated, Skipped: skipped, Notes: notes, Logged: logged, LogNotes: logNotes}, nil
}
