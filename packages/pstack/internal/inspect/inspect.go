// Package inspect answers "what is actually running, and what was Traefik actually told about it".
//
// WHY THIS EXISTS. "The hostname does not work" was undiagnosable from this tool. The registry knows
// what you *submitted*; `compose ps` knows what is *running*; the reason a request 404s lives in
// neither — it is in the container's Traefik labels, which pstack never writes. Every check below is
// one of the five rules in examples/docker-compose.preview.yml, turned from prose into a finding.
//
// NEVER RETURN A RAW INSPECT. `docker inspect` includes Config.Env — the container's whole
// environment, where database passwords live. Each field is picked out by name, and label values
// go through redact.RedactText, because a basicauth users label is a credential too.
//
// SWARM. A swarm stack's containers carry com.docker.stack.namespace; Traefik's routes come from
// SERVICE labels; a task may run on another node, listed from `docker stack ps` with remote:true.
// Only tasks whose desired state is `running` count — swarm replaces a failed task rather than
// restarting the container, so `docker ps -a` on a manager accumulates corpses.
//
// AND A SWARM ROUTE'S TARGET IS THE SERVICE'S VIP. `docker inspect` gives a task container's own
// ingress IP, but only for a task on THIS node — and with the default (vip) endpoint mode that is
// not the address Traefik dials anyway. The manager knows the one that is: Endpoint.VirtualIPs on
// the `docker service inspect` we already make, node-independent. Every fallback below degrades to
// a NAMED reason (RouteInfo.TargetReason) rather than to a guess: this page's whole job is to stop
// saying "not on the ingress network" about a container nobody looked at.
package inspect

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// The labels docker stamps. Mirrored in internal/swarm; docker's, not ours.
const (
	StackLabel   = "com.docker.stack.namespace"
	ServiceLabel = "com.docker.swarm.service.name"
	TaskLabel    = "com.docker.swarm.task.id"
	NodeLabel    = "com.docker.swarm.node.id"
	Ingress      = "preview-ingress"
)

// shq is single-quote shell quoting: wrap in single quotes, with each embedded quote spelled '\”.
func shq(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

// PortMap is one published port mapping.
type PortMap struct {
	ContainerPort int64   `json:"containerPort"`
	Protocol      string  `json:"protocol"`
	HostPort      *string `json:"hostPort,omitempty"`
}

// ContainerInfo is one container as the deployment page shows it.
type ContainerInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Service       *string           `json:"service"`
	Image         string            `json:"image"`
	State         string            `json:"state"`
	Health        *string           `json:"health"`
	ExitCode      *int64            `json:"exitCode"`
	RestartCount  int64             `json:"restartCount"`
	Networks      []string          `json:"networks"`
	IngressIP     *string           `json:"ingressIp"`
	Ports         []PortMap         `json:"ports"`
	TraefikLabels map[string]string `json:"traefikLabels"`
	StartedAt     *int64            `json:"startedAt"`
	Node          *string           `json:"node"`
	Remote        bool              `json:"remote"`
	// Job marks the SYNTHESISED row that stands for a one-shot swarm service's verdict — its id and
	// name are the service's, and no container answers to them, so start/stop/exec can only fail.
	// Plain bool without omitempty (Go rule 2): false must serialize.
	Job bool `json:"job"`
}

// RouteInfo is a Traefik router as declared by labels, joined to the service it points at.
type RouteInfo struct {
	Router       string   `json:"router"`
	Container    string   `json:"container"`
	Rule         string   `json:"rule"`
	Hosts        []string `json:"hosts"`
	Service      *string  `json:"service"`
	Port         *int64   `json:"port"`
	Entrypoints  *string  `json:"entrypoints"`
	TLS          bool     `json:"tls"`
	Certresolver *string  `json:"certresolver"`
	Priority     *string  `json:"priority"`
	Target       *string  `json:"target"`
	// TargetReason says WHY Target is nil, so the page can state a fact instead of the single guess
	// it used to make about every one of these. "" when Target is set; otherwise exactly one of
	// "no-port" (the router declares no loadbalancer.server.port), "not-on-ingress" (the subject is
	// POSITIVELY not attached to preview-ingress) and "unknown-node" (nothing here can know the
	// address: a swarm task on another node, or network names docker would not resolve). Plain
	// string without omitempty (Go rule 2) — "" must serialize.
	TargetReason string `json:"targetReason"`
}

// Finding is one diagnosis. Short, specific, names the fix.
type Finding struct {
	Level   string `json:"level"` // error | warn | info
	Message string `json:"message"`
}

// Challenge is which ACME challenge the host's Traefik runs.
type Challenge string

const (
	HTTP01  Challenge = "http01"
	DNS01   Challenge = "dns01"
	Unknown Challenge = "unknown"
	// DNSPersist: a stored wildcard beside Traefik's dynamic config covers every preview. Never
	// detected HERE (DetectChallenge reads argv, and Traefik's argv keeps its init-time flags in
	// this mode) — the server passes it in when the stored wildcard exists.
	DNSPersist Challenge = "dns-persist-01"
)

// Runtime is everything the deployment page needs.
type Runtime struct {
	Stack      string          `json:"stack"`
	Containers []ContainerInfo `json:"containers"`
	Routes     []RouteInfo     `json:"routes"`
	Findings   []Finding       `json:"findings"`
	Challenge  Challenge       `json:"challenge"`
	// Reachable: true when docker answered. False means every field above is "unknown", never "empty".
	Reachable bool `json:"reachable"`
}

// rawInspect is `docker inspect` output, the fields this module reads. Everything else is ignored.
type rawInspect struct {
	ID           string   `json:"Id"`
	Name         string   `json:"Name"`
	RestartCount *int64   `json:"RestartCount"`
	Args         []string `json:"Args"`
	Config       *struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
		Cmd    []string          `json:"Cmd"`
	} `json:"Config"`
	State *struct {
		Status    string                   `json:"Status"`
		ExitCode  *int64                   `json:"ExitCode"`
		StartedAt string                   `json:"StartedAt"`
		OOMKilled *bool                    `json:"OOMKilled"`
		Health    *struct{ Status string } `json:"Health"`
	} `json:"State"`
	HostConfig *struct {
		Memory *int64 `json:"Memory"`
	} `json:"HostConfig"`
	NetworkSettings *struct {
		Networks map[string]*struct{ IPAddress string } `json:"Networks"`
		Ports    map[string][]struct{ HostPort string } `json:"Ports"`
	} `json:"NetworkSettings"`
}

// inspectIDs is one `docker inspect` for all of them rather than one each.
func inspectIDs(r exec.Runner, ids []string) []rawInspect {
	if len(ids) == 0 {
		return nil
	}
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = shq(id)
	}
	res := r.Run("docker inspect "+strings.Join(q, " "), exec.RunOptions{Label: "docker inspect"})
	if !res.OK {
		return nil
	}
	var parsed []rawInspect
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		out = "[]"
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil
	}
	return parsed
}

// idsByLabel lists container ids carrying a label, `-a` so a stopped container still counts. nil,
// never empty, when docker did not answer — collapsing the two is how a UI reports a live stack as
// torn down.
func idsByLabel(r exec.Runner, label string) ([]string, bool) {
	res := r.Run("docker ps -aq --filter "+shq("label="+label), exec.RunOptions{Label: "docker ps"})
	if !res.OK {
		return nil, false
	}
	return lines(res.Stdout), true
}

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// traefikLabelsOf keeps only Traefik labels, values redacted.
func traefikLabelsOf(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if strings.HasPrefix(k, "traefik.") {
			out[k] = redact.RedactText(v)
		}
	}
	return out
}

// epochMs is `2026-08-21T10:00:00.123456789Z` → ms; docker's zero time (0001-01-01…) is nil.
func epochMs(iso string) *int64 {
	if iso == "" || strings.HasPrefix(iso, "0001-") {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

func toContainer(raw rawInspect, stack string) ContainerInfo {
	labels := map[string]string{}
	if raw.Config != nil && raw.Config.Labels != nil {
		labels = raw.Config.Labels
	}
	// Compose stamps the service name; swarm stamps `<stack>_<service>`.
	var service *string
	if s, ok := labels["com.docker.compose.service"]; ok {
		service = &s
	} else if sw, ok := labels[ServiceLabel]; ok {
		s := sw
		if stack != "" && strings.HasPrefix(sw, stack+"_") {
			s = sw[len(stack)+1:]
		}
		service = &s
	}
	ports := []PortMap{}
	networks := []string{}
	var ingressIP *string
	if raw.NetworkSettings != nil {
		specs := make([]string, 0, len(raw.NetworkSettings.Ports))
		for k := range raw.NetworkSettings.Ports {
			specs = append(specs, k)
		}
		sort.Strings(specs) // docker's map is unordered in both runtimes; a stable order beats a random one
		for _, sp := range specs {
			portStr, protocol, _ := strings.Cut(sp, "/")
			if protocol == "" {
				protocol = "tcp"
			}
			n := js.ParseNumber(portStr)
			if !js.IsFinite(n) {
				continue
			}
			bindings := raw.NetworkSettings.Ports[sp]
			if len(bindings) > 0 {
				for _, b := range bindings {
					hp := b.HostPort
					ports = append(ports, PortMap{ContainerPort: int64(n), Protocol: protocol, HostPort: &hp})
				}
			} else {
				ports = append(ports, PortMap{ContainerPort: int64(n), Protocol: protocol})
			}
		}
		names := make([]string, 0, len(raw.NetworkSettings.Networks))
		for k := range raw.NetworkSettings.Networks {
			names = append(names, k)
		}
		sort.Strings(names)
		networks = names
		if n := raw.NetworkSettings.Networks[Ingress]; n != nil && n.IPAddress != "" {
			ip := n.IPAddress
			ingressIP = &ip
		}
	}
	state := "unknown"
	var health *string
	var exit *int64
	var started *int64
	if raw.State != nil {
		if raw.State.Status != "" {
			state = raw.State.Status
		}
		if raw.State.Health != nil && raw.State.Health.Status != "" {
			h := raw.State.Health.Status
			health = &h
		}
		exit = raw.State.ExitCode
		started = epochMs(raw.State.StartedAt)
	}
	var restarts int64
	if raw.RestartCount != nil {
		restarts = *raw.RestartCount
	}
	var node *string
	if _, ok := labels[NodeLabel]; ok {
		n := "this node"
		node = &n
	}
	image := ""
	if raw.Config != nil {
		image = raw.Config.Image
	}
	id := raw.ID
	if len(id) > 12 {
		id = id[:12]
	}
	return ContainerInfo{
		ID:            id,
		Name:          strings.TrimPrefix(raw.Name, "/"),
		Service:       service,
		Image:         image,
		State:         state,
		Health:        health,
		ExitCode:      exit,
		RestartCount:  restarts,
		Networks:      networks,
		IngressIP:     ingressIP,
		Ports:         ports,
		TraefikLabels: traefikLabelsOf(labels),
		StartedAt:     started,
		Node:          node,
		Remote:        false,
	}
}

// ── swarm services and tasks ─────────────────────────────────────────────────────────────────────

type rawService struct {
	ID        string `json:"ID"`
	UpdatedAt string `json:"UpdatedAt"`
	CreatedAt string `json:"CreatedAt"`
	Spec      *struct {
		Name         string            `json:"Name"`
		Labels       map[string]string `json:"Labels"`
		TaskTemplate *struct {
			ContainerSpec *struct{ Image string }   `json:"ContainerSpec"`
			Networks      []struct{ Target string } `json:"Networks"`
		} `json:"TaskTemplate"`
	} `json:"Spec"`
	// Endpoint.VirtualIPs is the service's address on each network it is attached to — what the
	// swarm provider dials, and the only ingress address a manager can know for a task on another
	// node. Empty under endpoint_mode: dnsrr, where there IS no VIP; the task fallback covers that.
	Endpoint *struct {
		VirtualIPs []struct {
			NetworkID string `json:"NetworkID"`
			Addr      string `json:"Addr"`
		} `json:"VirtualIPs"`
	} `json:"Endpoint"`
}

// SwarmService is a swarm service with its routing labels.
type SwarmService struct {
	ID            string
	Name          string // `<stack>_<service>`
	Service       string // the compose service name
	Stack         *string
	Image         string
	Networks      []string
	TraefikLabels map[string]string
	UpdatedAt     *int64
	// IngressIP is the service's VIP on preview-ingress, nil when it has none (dnsrr) or the
	// network list could not be read.
	IngressIP *string
	// NetworksKnown is false when a network id did not resolve to a name, so Networks holds a raw
	// id and "this is not the ingress network" is NOT established.
	NetworksKnown bool
}

// OnIngress is whether the service is attached to preview-ingress — nil for "could not determine"
// (invariant 11). "Not attached" is a diagnosis with a fix attached to it; it has to be measured.
func (s SwarmService) OnIngress() *bool {
	if !s.NetworksKnown {
		return nil
	}
	on := containsStr(s.Networks, Ingress)
	return &on
}

// onIngress is the same answer for a CONTAINER, whose network names docker reported directly.
func onIngress(networks []string) *bool {
	on := containsStr(networks, Ingress)
	return &on
}

// swarmJob is a ONE-SHOT service's progress: `docker stack deploy` has accepted
// `deploy.mode: replicated-job` since docker/cli#2907 (merged 2022-05-17), which maps
// `deploy.replicas` to both MaxConcurrent and TotalCompletions.
//
// The completed count lives in ServiceStatus, which the daemon populates on the service LIST only —
// `docker service inspect` leaves it nil (cli/command/service/formatter.go guards on exactly that),
// so `docker service ls` is the one call that can answer "did the seed succeed". We were already
// making it for the ids.
type swarmJob struct {
	Completed int64
	Total     int64
}

// jobReplicas parses the `R/D (C/T completed)` column docker renders for a job — the format string
// is `"%d/%d (%d/%d completed)"` in the CLI's formatter.
var jobReplicas = regexp.MustCompile(`\((\d+)/(\d+) completed\)`)

// rawServiceLs is `docker service ls --format '{{json .}}'`. Mode is rendered `replicated job` /
// `global job` (with a space) — a display string, so job-ness is matched on the suffix rather than
// on either literal.
type rawServiceLs struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	Mode     string `json:"Mode"`
	Replicas string `json:"Replicas"`
}

type rawTask struct {
	ID           string `json:"ID"`
	Name         string `json:"Name"`
	Image        string `json:"Image"`
	Node         string `json:"Node"`
	DesiredState string `json:"DesiredState"`
	CurrentState string `json:"CurrentState"`
	Error        string `json:"Error"`
}

func inspectServices(r exec.Runner, ids []string, networkNames map[string]string) []SwarmService {
	if len(ids) == 0 {
		return nil
	}
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = shq(id)
	}
	res := r.Run("docker service inspect "+strings.Join(q, " "), exec.RunOptions{Label: "docker service inspect"})
	if !res.OK {
		return nil
	}
	var parsed []rawService
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		out = "[]"
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil
	}
	svcs := make([]SwarmService, 0, len(parsed))
	for _, raw := range parsed {
		labels := map[string]string{}
		name := ""
		image := ""
		networks := []string{}
		known := true
		if raw.Spec != nil {
			if raw.Spec.Labels != nil {
				labels = raw.Spec.Labels
			}
			name = raw.Spec.Name
			if raw.Spec.TaskTemplate != nil {
				if raw.Spec.TaskTemplate.ContainerSpec != nil {
					image = raw.Spec.TaskTemplate.ContainerSpec.Image
				}
				for _, n := range raw.Spec.TaskTemplate.Networks {
					if n.Target == "" {
						continue
					}
					nm, ok := netName(networkNames, n.Target)
					if !ok {
						known = false
					}
					networks = append(networks, nm)
				}
			}
		}
		var stack *string
		if s, ok := labels[StackLabel]; ok {
			stack = &s
		}
		service := name
		if stack != nil && strings.HasPrefix(name, *stack+"_") {
			service = name[len(*stack)+1:]
		}
		id := raw.ID
		if len(id) > 12 {
			id = id[:12]
		}
		updated := raw.UpdatedAt
		if updated == "" {
			updated = raw.CreatedAt
		}
		var vip *string
		if raw.Endpoint != nil {
			for _, v := range raw.Endpoint.VirtualIPs {
				if nm, ok := netName(networkNames, v.NetworkID); !ok || nm != Ingress {
					continue
				}
				// `10.0.9.2/24` → `10.0.9.2`. The mask is the network's; it is not part of an
				// address anyone can dial, and printed it would be a confidently wrong answer.
				if addr, _, _ := strings.Cut(v.Addr, "/"); addr != "" {
					vip = &addr
				}
				break
			}
		}
		svcs = append(svcs, SwarmService{ID: id, Name: name, Service: service, Stack: stack, Image: image, Networks: networks, TraefikLabels: traefikLabelsOf(labels), UpdatedAt: epochMs(updated), IngressIP: vip, NetworksKnown: known})
	}
	return svcs
}

// netName resolves a network id to its name; ok=false when the list does not hold it (a
// `docker network ls` that did not answer, a network this daemon cannot see). The caller gets the
// raw id back so output still says something, and the false so it does not draw a conclusion from
// it. `docker network ls` prints 12-char ids; a service's Networks[].Target is the full one.
func netName(names map[string]string, id string) (string, bool) {
	if nm := names[id]; nm != "" {
		return nm, true
	}
	if len(id) > 12 {
		if nm := names[id[:12]]; nm != "" {
			return nm, true
		}
	}
	return id, false
}

// networkNames is network id → name.
func networkNames(r exec.Runner) map[string]string {
	out := map[string]string{}
	res := r.Run("docker network ls --format '{{.ID}} {{.Name}}'", exec.RunOptions{Label: "docker network ls"})
	if !res.OK {
		return out
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

var nothingFound = regexp.MustCompile(`(?i)nothing found`)

// stackTasks is `docker stack ps`, running-desired tasks only; ok=false when docker did not answer.
func stackTasks(r exec.Runner, stack string) ([]rawTask, bool) {
	res := r.Run("docker stack ps "+shq(stack)+" --no-trunc --filter desired-state=running --format '{{json .}}'", exec.RunOptions{Label: "docker stack ps"})
	if !res.OK {
		// "Nothing found in stack" is an exit 1 with nothing running — not a failure to answer.
		if nothingFound.MatchString(res.Stderr + res.Stdout) {
			return []rawTask{}, true
		}
		return nil, false
	}
	tasks := []rawTask{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var t rawTask
		if err := json.Unmarshal([]byte(line), &t); err == nil {
			tasks = append(tasks, t)
		}
	}
	return tasks, true
}

// TaskState is `Running 3 minutes ago` → running; Failed/Rejected → restarting (swarm is about to
// replace it); Shutdown/Complete → exited.
func TaskState(current string) string {
	word := ""
	if f := strings.Fields(current); len(f) > 0 {
		word = strings.ToLower(f[0])
	}
	switch word {
	case "running":
		return "running"
	case "failed", "rejected":
		return "restarting"
	case "shutdown", "complete":
		return "exited"
	case "":
		return "unknown"
	}
	return word
}

// ruleArgs is the arguments of every `<name>(…)` call in a Traefik rule. A scanner, not a regexp: a
// HostRegexp argument routinely contains parentheses, and backticks delimit the argument.
func ruleArgs(rule, name string) []string {
	var out []string
	from := 0
	for {
		at := strings.Index(rule[from:], name+"(")
		if at < 0 {
			break
		}
		at += from
		// Only a whole matcher name: `HostRegexp(` must not be read as `Host(`.
		if at > 0 && isLetter(rule[at-1]) {
			from = at + 1
			continue
		}
		i := at + len(name) + 1
		depth := 1
		var quote byte
		start := i
		for ; i < len(rule) && depth > 0; i++ {
			c := rule[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '`' || c == '"' || c == '\'' {
				quote = c
			} else if c == '(' {
				depth++
			} else if c == ')' {
				depth--
			}
		}
		if depth != 0 {
			break
		}
		out = append(out, rule[start:i-1])
		from = i
	}
	return out
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

var quoteEdges = regexp.MustCompile("^[`'\"]|[`'\"]$")

// HostsFromRule pulls hostnames out of a Traefik rule: `Host(a, b)` yields both; a HostRegexp yields
// `(pattern) <regexp>`.
func HostsFromRule(rule string) []string {
	hosts := []string{}
	for _, arg := range ruleArgs(rule, "Host") {
		for _, h := range strings.Split(arg, ",") {
			if c := quoteEdges.ReplaceAllString(strings.TrimSpace(h), ""); c != "" {
				hosts = append(hosts, c)
			}
		}
	}
	for _, arg := range ruleArgs(rule, "HostRegexp") {
		if c := quoteEdges.ReplaceAllString(strings.TrimSpace(arg), ""); c != "" {
			hosts = append(hosts, "(pattern) "+c)
		}
	}
	return hosts
}

var (
	routerLabel  = regexp.MustCompile(`^traefik\.http\.routers\.([^.]+)\.(.+)$`)
	servicePortL = regexp.MustCompile(`^traefik\.http\.services\.([^.]+)\.loadbalancer\.server\.port$`)
)

// RoutesFromLabels turns one container's Traefik labels into routers joined to their service ports.
// A router with no explicit .service is left nil rather than guessed. Routers are returned in the
// order their first label was seen — labels are iterated in sorted order, so the result is stable.
//
// `onIngress` is whether the subject is attached to preview-ingress, and NIL means the caller could
// not determine it. That nil is the whole point of this parameter: a missing target used to be
// rendered as "not on the ingress network" whether or not anyone had looked, which under swarm was
// false on every row. TargetReason separates the three facts.
func RoutesFromLabels(container string, labels map[string]string, ingressIP *string, onIngress *bool) []RouteInfo {
	type partial struct {
		rule, service, entrypoints, certresolver, priority *string
		tls                                                bool
	}
	routers := map[string]*partial{}
	var order []string
	servicePorts := map[string]int64{}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := labels[key]
		if m := routerLabel.FindStringSubmatch(key); m != nil {
			name, prop := m[1], m[2]
			r := routers[name]
			if r == nil {
				r = &partial{}
				routers[name] = r
				order = append(order, name)
			}
			v := value
			switch prop {
			case "rule":
				r.rule = &v
			case "service":
				r.service = &v
			case "entrypoints":
				r.entrypoints = &v
			case "tls":
				r.tls = value == "true"
			case "tls.certresolver":
				r.certresolver = &v
			case "priority":
				r.priority = &v
			}
			continue
		}
		if m := servicePortL.FindStringSubmatch(key); m != nil {
			if n := js.ParseNumber(value); js.IsFinite(n) {
				servicePorts[m[1]] = int64(n)
			}
		}
	}
	out := make([]RouteInfo, 0, len(order))
	for _, router := range order {
		r := routers[router]
		// `|| null`: an empty `.service=` label is present-but-useless.
		var service *string
		if r.service != nil && *r.service != "" {
			service = r.service
		}
		// Fall back to a service named after the router — the example's convention — but only when
		// such a service declared a port. Computed once, so port and target cannot disagree.
		var port *int64
		if service != nil {
			if p, ok := servicePorts[*service]; ok {
				port = &p
			}
		}
		if port == nil {
			if p, ok := servicePorts[router]; ok {
				port = &p
			}
		}
		rule := ""
		hosts := []string{}
		if r.rule != nil {
			rule = *r.rule
			hosts = HostsFromRule(rule)
		}
		var target *string
		if ingressIP != nil && port != nil {
			t := *ingressIP + ":" + js.ToString(*port)
			target = &t
		}
		// Port first: it is the half the author controls, and it was the one case the UI already got
		// right. "not-on-ingress" only where attachment was actually measured and came back false.
		reason := ""
		switch {
		case target != nil:
		case port == nil:
			reason = "no-port"
		case onIngress != nil && !*onIngress:
			reason = "not-on-ingress"
		default:
			reason = "unknown-node"
		}
		out = append(out, RouteInfo{Router: router, Container: container, Rule: rule, Hosts: hosts, Service: service, Port: port, Entrypoints: r.entrypoints, TLS: r.tls, Certresolver: r.certresolver, Priority: r.priority, Target: target, TargetReason: reason})
	}
	return out
}

// DetectChallenge reads which ACME challenge the host's Traefik runs from its own flags. It decides
// an OPPOSITE rule for per-PR routers, so reading the running container beats asking.
func DetectChallenge(r exec.Runner) Challenge {
	ids, ok := idsByLabel(r, "com.docker.compose.project="+ControlProject)
	if !ok || len(ids) == 0 {
		return Unknown
	}
	for _, raw := range inspectIDs(r, ids) {
		var argv []string
		if raw.Config != nil {
			argv = append(argv, raw.Config.Cmd...)
		}
		argv = append(argv, raw.Args...)
		joined := strings.ToLower(strings.Join(argv, " "))
		if strings.Contains(joined, "dnschallenge") {
			return DNS01
		}
		if strings.Contains(joined, "httpchallenge") {
			return HTTP01
		}
	}
	return Unknown
}

// RuntimeArgs parameterise DeploymentRuntime.
type RuntimeArgs struct {
	Stack  string
	Runner exec.Runner
	// Challenge, when known; "" means detect.
	Challenge Challenge
	// AllRouters is router name → container names across the WHOLE host, for the collision check.
	AllRouters map[string][]string
	// Orchestrator decides which labels name this stack's containers. Default compose.
	Orchestrator spec.Orchestrator
}

type subject struct {
	name     string
	labels   map[string]string
	networks []string
	state    string
	health   *string
}

// DeploymentRuntime is everything the deployment page needs: containers, routes, findings.
func DeploymentRuntime(a RuntimeArgs) Runtime {
	stack, r := a.Stack, a.Runner
	swarm := a.Orchestrator == spec.Swarm
	empty := func() Runtime {
		return Runtime{Stack: stack, Containers: []ContainerInfo{}, Routes: []RouteInfo{}, Findings: []Finding{}, Challenge: Unknown, Reachable: false}
	}
	label := "com.docker.compose.project"
	if swarm {
		label = StackLabel
	}
	ids, ok := idsByLabel(r, label+"="+stack)
	if !ok {
		return empty()
	}
	inspected := inspectIDs(r, ids)
	containers := make([]ContainerInfo, 0, len(inspected))
	for _, raw := range inspected {
		containers = append(containers, toContainer(raw, stack))
	}
	routes := []RouteInfo{}
	findings := []Finding{}
	var subjects []subject

	if swarm {
		sres := r.Run("docker service ls --format '{{json .}}' --filter "+shq("label="+StackLabel+"="+stack), exec.RunOptions{Label: "docker service ls"})
		if !sres.OK {
			return empty()
		}
		sids := []string{}
		// Keyed by NAME: `docker service ls` truncates ids and `docker service inspect` does not, so
		// an id-keyed lookup silently never matches. `<stack>_<service>` is unique within a stack.
		jobsByName := map[string]swarmJob{}
		for _, line := range lines(sres.Stdout) {
			var ls rawServiceLs
			if err := json.Unmarshal([]byte(line), &ls); err != nil || ls.ID == "" {
				continue
			}
			sids = append(sids, ls.ID)
			if !strings.HasSuffix(ls.Mode, "job") {
				continue
			}
			// A job whose progress column does not parse stays a job with Total 0, which reads as
			// NOT DONE below. Fail closed: an unrecognised format must not certify a seed that may
			// never have run. But say so — otherwise readiness reads `created` on every poll and
			// burns its whole deadline with no diagnosis.
			j := swarmJob{}
			if m := jobReplicas.FindStringSubmatch(ls.Replicas); m != nil {
				j.Completed, _ = strconv.ParseInt(m[1], 10, 64)
				j.Total, _ = strconv.ParseInt(m[2], 10, 64)
			} else {
				findings = append(findings, Finding{"warn", ls.Name + " is a one-shot job, but its `docker service ls` replicas column reads " + strconv.Quote(ls.Replicas) + " where \"(<completed>/<total> completed)\" was expected, so its verdict cannot be read and readiness will treat it as never finishing. Check it by hand with `docker service ps " + ls.Name + "`; if it completed, this docker version renders the column differently — report that."})
			}
			jobsByName[ls.Name] = j
		}
		var services []SwarmService
		tasks := []rawTask{}
		if len(sids) > 0 {
			services = inspectServices(r, sids, networkNames(r))
			var tok bool
			if tasks, tok = stackTasks(r, stack); !tok {
				return empty()
			}
		}
		// Only the tasks swarm still wants running; a replaced task's container is a corpse.
		wanted := map[string]rawTask{}
		for _, t := range tasks {
			wanted[t.ID] = t
		}
		type local struct {
			info ContainerInfo
			task string
		}
		locals := make([]local, 0, len(inspected))
		for _, raw := range inspected {
			task := ""
			if raw.Config != nil {
				task = raw.Config.Labels[TaskLabel]
			}
			locals = append(locals, local{info: toContainer(raw, stack), task: task})
		}
		containers = containers[:0]
		seenTasks := map[string]bool{}
		for _, l := range locals {
			seenTasks[l.task] = true
			t, isWanted := wanted[l.task]
			if l.task != "" && !isWanted {
				continue
			}
			info := l.info
			if isWanted && t.Node != "" {
				n := t.Node
				info.Node = &n
			}
			containers = append(containers, info)
		}
		byName := map[string]SwarmService{}
		for _, svc := range services {
			byName[svc.Name] = svc
		}
		for _, t := range tasks {
			if t.ID == "" || seenTasks[t.ID] {
				continue
			}
			// `<stack>_<svc>.<slot>` → the service; the slot keeps replicas distinguishable.
			svcName := t.Name
			if i := strings.LastIndexByte(svcName, '.'); i >= 0 {
				svcName = svcName[:i]
			}
			svc, hasSvc := byName[svcName]
			var service *string
			if hasSvc {
				s := svc.Service
				service = &s
			} else if strings.HasPrefix(svcName, stack+"_") {
				s := svcName[len(stack)+1:]
				service = &s
			} else if svcName != "" {
				s := svcName
				service = &s
			}
			image := t.Image
			if image == "" && hasSvc {
				image = svc.Image
			}
			var exit *int64
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.CurrentState)), "complete") {
				zero := int64(0)
				exit = &zero
			}
			id := t.ID
			if len(id) > 12 {
				id = id[:12]
			}
			name := t.Name
			if name == "" {
				name = id
			}
			networks := []string{}
			labels := map[string]string{}
			var started *int64
			if hasSvc {
				networks = svc.Networks
				labels = svc.TraefikLabels
				started = svc.UpdatedAt
			}
			var node *string
			if t.Node != "" {
				n := t.Node
				node = &n
			}
			containers = append(containers, ContainerInfo{ID: id, Name: name, Service: service, Image: image, State: TaskState(t.CurrentState), Health: nil, ExitCode: exit, RestartCount: 0, Networks: networks, IngressIP: nil, Ports: []PortMap{}, TraefikLabels: labels, StartedAt: started, Node: node, Remote: true})
		}
		// A one-shot service's verdict belongs to the SERVICE, not to its tasks. Two reasons its
		// task containers cannot answer it: a finished job's task is not desired-running, so
		// `docker stack ps --filter desired-state=running` never returns it and the corpse skip
		// above drops its container — the seed simply vanishes, and a stack whose seed FAILED then
		// reads as ready over the remaining services. And a task that is mid-run is `running` with
		// no healthcheck, which readiness calls ready — green while the database is still empty.
		if len(jobsByName) > 0 {
			jobByService := map[string]swarmJob{}
			for _, svc := range services {
				if j, ok := jobsByName[svc.Name]; ok {
					jobByService[svc.Service] = j
				}
			}
			kept := make([]ContainerInfo, 0, len(containers))
			for _, c := range containers {
				if c.Service != nil {
					if _, isJob := jobByService[*c.Service]; isJob {
						continue
					}
				}
				kept = append(kept, c)
			}
			for _, svc := range services {
				j, isJob := jobByService[svc.Service]
				if !isJob {
					continue
				}
				service := svc.Service
				info := ContainerInfo{ID: svc.ID, Name: svc.Name, Service: &service, Image: svc.Image, Networks: svc.Networks, Ports: []PortMap{}, TraefikLabels: svc.TraefikLabels, StartedAt: svc.UpdatedAt, Job: true}
				if j.Total > 0 && j.Completed >= j.Total {
					zero := int64(0)
					// Exactly what a compose one-shot looks like when it succeeds, so readiness needs
					// no new rule: `exited` + code 0 is already "a one-shot that finished".
					info.State, info.ExitCode = "exited", &zero
				} else {
					// `created`, deliberately — readiness treats it as still converging. NOT
					// `running`, which with no healthcheck is READY there.
					info.State = "created"
				}
				kept = append(kept, info)
			}
			containers = kept
		}
		// The address Traefik dials is the service's VIP, which the manager knows wherever the tasks
		// run. Only when there is none (dnsrr) does a task container that happens to be on THIS node
		// answer — its own ingress IP, from the same `docker inspect` the compose path uses.
		for _, svc := range services {
			ip := svc.IngressIP
			if ip == nil {
				for _, c := range containers {
					if !c.Remote && c.Service != nil && *c.Service == svc.Service && c.IngressIP != nil {
						ip = c.IngressIP
						break
					}
				}
			}
			routes = append(routes, RoutesFromLabels(svc.Service, svc.TraefikLabels, ip, svc.OnIngress())...)
		}
		for _, svc := range services {
			var mine []ContainerInfo
			for _, c := range containers {
				if c.Service != nil && *c.Service == svc.Service {
					mine = append(mine, c)
				}
			}
			state := "no tasks"
			running := false
			for _, c := range mine {
				if c.State == "running" {
					running = true
				}
			}
			if running {
				state = "running"
			} else if len(mine) > 0 {
				state = mine[0].State
			}
			var health *string
			for _, c := range mine {
				if c.Health != nil && *c.Health == "unhealthy" {
					health = c.Health
					break
				}
			}
			if health == nil {
				for _, c := range mine {
					if c.Health != nil {
						health = c.Health
						break
					}
				}
			}
			subjects = append(subjects, subject{name: svc.Service, labels: svc.TraefikLabels, networks: svc.Networks, state: state, health: health})
		}
	} else {
		for _, c := range containers {
			routes = append(routes, RoutesFromLabels(c.Name, c.TraefikLabels, c.IngressIP, onIngress(c.Networks))...)
			name := c.Name
			if c.Service != nil {
				name = *c.Service
			}
			subjects = append(subjects, subject{name: name, labels: c.TraefikLabels, networks: c.Networks, state: c.State, health: c.Health})
		}
	}

	challenge := a.Challenge
	if challenge == "" {
		challenge = DetectChallenge(r)
	}
	networkKey := "traefik.docker.network"
	kind := "container"
	if swarm {
		networkKey = "traefik.swarm.network"
		kind = "service"
	}
	if len(containers) == 0 {
		findings = append(findings, Finding{"info", "Nothing is running for this stack. Deploy it, or check that the services you expect are behind a profile the spec selects — a service whose profile is not enabled is not started."})
	}
	for _, c := range subjects {
		enabled := c.labels["traefik.enable"] == "true"
		hasAny := len(c.labels) > 0
		var cRoutes int
		for _, rt := range routes {
			if rt.Container == c.name {
				cRoutes++
			}
		}
		if !hasAny {
			findings = append(findings, Finding{"warn", c.name + " has no Traefik labels, so no hostname reaches it. If it is meant to be reachable it needs traefik.enable=true, " + networkKey + "=" + Ingress + ", a router rule, and a loadbalancer.server.port — see examples/docker-compose.preview.yml."})
		} else if !enabled {
			findings = append(findings, Finding{"error", c.name + " has Traefik labels but not traefik.enable=true. The control stack runs with exposedbydefault=false, so Traefik ignores this " + kind + " entirely — the hostname will 404 with nothing logged anywhere."})
		}
		if hasAny && !containsStr(c.networks, Ingress) {
			lookalike := ""
			for _, n := range c.networks {
				if strings.HasSuffix(n, "_"+Ingress) {
					lookalike = n
					break
				}
			}
			if lookalike != "" {
				findings = append(findings, Finding{"error", c.name + ` is on "` + lookalike + `", not "` + Ingress + `". Compose created its own network because the compose file declares it without ` + "`external: true`" + `. The container is healthy and unreachable, and Traefik answers 404.`})
			} else {
				on := strings.Join(c.networks, ", ")
				if on == "" {
					on = "none"
				}
				findings = append(findings, Finding{"error", c.name + ` is not attached to "` + Ingress + `" (on: ` + on + `). Traefik dials containers over that network, so it cannot reach this one.`})
			}
		}
		if len(c.networks) > 1 && enabled && c.labels[networkKey] == "" {
			findings = append(findings, Finding{"warn", c.name + " is on " + js.ToString(int64(len(c.networks))) + " networks and does not set " + networkKey + "=" + Ingress + ". Traefik has to pick one, and picking the per-project network yields an unreachable backend."})
		}
		if enabled && cRoutes == 0 {
			findings = append(findings, Finding{"warn", c.name + " is Traefik-enabled but declares no router rule, so nothing routes to it."})
		}
		if c.state == "running" && c.health != nil && *c.health == "unhealthy" {
			findings = append(findings, Finding{"warn", c.name + " is running but unhealthy — Traefik will not route to it while it stays that way."})
		}
	}
	for _, c := range containers {
		if c.Remote {
			node := "?"
			if c.Node != nil {
				node = *c.Node
			}
			findings = append(findings, Finding{"info", c.Name + " runs on node " + node + ". Logs reach it through the manager; a terminal and stop/start do not."})
		}
	}
	for _, rt := range routes {
		if rt.Rule == "" {
			findings = append(findings, Finding{"error", `Router "` + rt.Router + `" has no rule, so it matches nothing.`})
		}
		if rt.Port == nil {
			findings = append(findings, Finding{"warn", `Router "` + rt.Router + `" has no loadbalancer.server.port, so Traefik guesses the container's exposed port. If the image exposes none — or several — the result is a 502.`})
		}
		if challenge == HTTP01 && rt.TLS && rt.Certresolver == nil {
			findings = append(findings, Finding{"error", `Router "` + rt.Router + `" requests TLS but sets no tls.certresolver, and this host uses HTTP-01, where every hostname resolves its own certificate. The route will exist and the TLS handshake will fail.`})
		}
		if (challenge == DNS01 || challenge == DNSPersist) && rt.Certresolver != nil {
			findings = append(findings, Finding{"warn", `Router "` + rt.Router + `" sets tls.certresolver on a host whose wildcard already covers every preview (` + string(challenge) + `). This makes the PR order its own certificate and burn the ~50-per-week limit. Use tls=true alone — a redeploy regenerates the labels.`})
		}
		// Traefik's router namespace is global: a duplicate name means one PR serves another's app.
		if owners := a.AllRouters[rt.Router]; len(owners) > 1 {
			findings = append(findings, Finding{"error", `Router name "` + rt.Router + `" is declared by ` + js.ToString(int64(len(owners))) + " containers on this host (" + strings.Join(owners, ", ") + "). Traefik's router namespace is global: these overwrite each other, and one hostname will serve the wrong container. Include the stack in the router name."})
		}
	}
	return Runtime{Stack: stack, Containers: containers, Routes: routes, Findings: findings, Challenge: challenge, Reachable: true}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// HostRoute is a route with the compose project (or swarm stack) that declares it.
type HostRoute struct {
	RouteInfo
	Project *string `json:"project"`
}

// AllRouters is every Traefik router declared by any container on the host. Two uses: the global
// collision check, and the Routing page.
type AllRouters struct {
	Reachable bool
	Routes    []HostRoute
	ByName    map[string][]string
}

// AllTraefikRouters reads every traefik.enable=true container and service.
func AllTraefikRouters(r exec.Runner) AllRouters {
	ids, ok := idsByLabel(r, "traefik.enable=true")
	if !ok {
		return AllRouters{Reachable: false, Routes: []HostRoute{}, ByName: map[string][]string{}}
	}
	routes := []HostRoute{}
	byName := map[string][]string{}
	for _, raw := range inspectIDs(r, ids) {
		c := toContainer(raw, "")
		var project *string
		if raw.Config != nil {
			if p, ok := raw.Config.Labels["com.docker.compose.project"]; ok {
				project = &p
			}
		}
		for _, rt := range RoutesFromLabels(c.Name, c.TraefikLabels, c.IngressIP, onIngress(c.Networks)) {
			routes = append(routes, HostRoute{RouteInfo: rt, Project: project})
			byName[rt.Router] = append(byName[rt.Router], c.Name)
		}
	}
	// Swarm services declare their routers on the SERVICE. A daemon that is not a manager answers
	// with an error — which is "no swarm routes", not "docker did not answer".
	svc := r.Run("docker service ls -q --filter 'label=traefik.enable=true'", exec.RunOptions{Label: "docker service ls"})
	if svc.OK {
		if sids := lines(svc.Stdout); len(sids) > 0 {
			for _, s := range inspectServices(r, sids, networkNames(r)) {
				for _, rt := range RoutesFromLabels(s.Name, s.TraefikLabels, s.IngressIP, s.OnIngress()) {
					routes = append(routes, HostRoute{RouteInfo: rt, Project: s.Stack})
					byName[rt.Router] = append(byName[rt.Router], s.Name)
				}
			}
		}
	}
	// Byte order, not localeCompare — the documented divergence (rule 6).
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Router < routes[j].Router })
	return AllRouters{Reachable: true, Routes: routes, ByName: byName}
}
