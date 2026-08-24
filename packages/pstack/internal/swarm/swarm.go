// Package swarm is Docker Swarm: the compose→swarm conversion, the `docker stack` command lines, and
// the node/join helpers behind the swarm panel.
//
// ── WHY SWARM AT ALL ─────────────────────────────────────────────────────────────────────────────
//
// One box runs out. The cheapest way to add a second without changing what a spec author writes is
// swarm: the manager is the box you already have, a worker is `docker swarm join` on another, and
// `docker stack deploy` schedules a preview's services across whichever nodes have room. Nothing else
// in the product changes — the axes, the leak gate and the registry are exactly as they were, because
// they never cared which daemon a container landed on.
//
// ── THE CONVERSION, AND WHY IT IS AUTOMATIC ──────────────────────────────────────────────────────
//
// `docker stack deploy` reads the compose v3 schema, STRICTLY: `mem_limit`, `cpus`, `profiles`,
// `pull_policy` and friends are "Additional property … is not allowed" — a hard error, not a warning —
// and `restart:` is silently ignored, which under swarm's default restart policy (`any`) turns a
// one-shot migration container into an infinite loop. Asking every spec author to learn that list is
// how previews stop getting written. So pstack converts the submitted file on every invocation
// (the same pipeline that generates Traefik labels — see autolabel) and reports what it changed in
// the job log. The original file is never modified.
//
// Every rule in Swarmify is FAITHFUL: the converted file means what the plain one meant under
// compose. A missing `restart:` becomes `condition: none` (compose would not restart it either), not
// swarm's default. Where there is no faithful translation (`privileged`, `devices`, `build`,
// `container_name`) the key is dropped and named, because a deploy that fails with "privileged is not
// allowed" teaches less than one that says "dropped privileged from service worker — swarm cannot run
// privileged tasks; pin this deployment to compose with `compose.orchestrator: compose`".
//
// Traefik labels move to `deploy.labels`: the swarm provider reads SERVICE labels, and leaving them on
// the container too would make the docker provider (still on, for the control stack) build a second
// copy of every router. `traefik.docker.network` becomes `traefik.swarm.network` for the same reason.
//
// ── CEILINGS, STATED ─────────────────────────────────────────────────────────────────────────────
//
//   - `docker stack rm` never removes volumes. Teardown removes the stack's labelled volumes on the
//     MANAGER; a volume a task created on a worker stays there. (ponytail: worker volumes are a
//     known leak; an axis with `docker -H ssh://worker volume rm` is the upgrade path.)
//   - `docker exec`, `stop`, `inspect` are node-local. A task on a worker is listed (from
//     `docker stack ps`) but the terminal and per-container actions refuse it by name.
//   - Relative bind mounts resolve against the compose file's directory ON EACH NODE. A preview that
//     bind-mounts `./data` works on the manager and fails on a worker that has no such directory.
//     Use named volumes.
//
// ── THE IMPORT GRAPH (a Go-only note) ────────────────────────────────────────────────────────────
//
// The TypeScript had a cycle — compose imported autolabel and swarm, autolabel imported swarm, swarm
// imported compose (shq) and autolabel (labelsToMap). Go refuses cycles, so this package is the LEAF:
// Shq and LabelsToMap are defined here and re-exported by compose and autolabel under their original
// names. cloudinit imports this package (JoinCommand, SwarmPorts); the one call back — rendering a
// worker cloud-config — goes through the CloudInit seam, which cloudinit fills in at init.
package swarm

import (
	"math"
	"strings"
	"unicode"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// StackLabel is the label swarm stamps on every task container, network and volume of a stack.
const StackLabel = "com.docker.stack.namespace"

// ServiceLabel is the service name label on a task container: `<stack>_<service>`.
const ServiceLabel = "com.docker.swarm.service.name"

// TaskLabel is the task id label on a task container.
const TaskLabel = "com.docker.swarm.task.id"

// NodeLabel is the node id label on a task container.
const NodeLabel = "com.docker.swarm.node.id"

// Shq single-quotes a value for bash. Wrapping in single quotes and escaping embedded single quotes
// is the only form that is safe for arbitrary content — a stack name or path with a space, a quote or
// a `$` would otherwise be re-split or expanded by the shell. (compose.ts's shq; it lives here
// because of the import graph — see the package comment.)
func Shq(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// LabelsToMap normalises a compose service's labels — either a list of `k=v` or a mapping — to an
// ordered mapping of string → string. A list entry splits at the FIRST `=`; no `=` means value "";
// a non-string entry is ignored. A mapping's values go through String(v ?? ""). (autolabel.ts's
// labelsToMap; here because of the import graph.)
func LabelsToMap(labels any) *omap.Map {
	out := omap.New()
	switch x := labels.(type) {
	case []any:
		for _, entry := range x {
			s, ok := entry.(string)
			if !ok {
				continue
			}
			if k, v, found := strings.Cut(s, "="); found {
				out.Set(k, v)
			} else {
				out.Set(s, "")
			}
		}
	case *omap.Map:
		x.Each(func(k string, v any) {
			if v == nil {
				out.Set(k, "")
			} else {
				out.Set(k, JSString(v))
			}
		})
	}
	return out
}

// JSString is String(v) for a document value: null → "null", numbers via Number.prototype.toString,
// an array joins its elements with "," (Array.prototype.toString; null elements print empty), a
// mapping is "[object Object]".
func JSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool, int64, int, float64:
		return js.ToString(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e == nil {
				parts[i] = ""
			} else {
				parts[i] = JSString(e)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// v3ServiceKeys is every key the compose v3.13 schema allows under a service, verbatim from docker/cli
// `config_schema_v3.13.json`. Anything else is a hard error at deploy time, so it is dropped here
// with a note. `x-*` extension keys are allowed by the schema's patternProperties.
var v3ServiceKeys = map[string]bool{}

func init() {
	for _, k := range []string{
		"deploy", "build", "cap_add", "cap_drop", "cgroupns_mode", "cgroup_parent", "command", "configs",
		"container_name", "credential_spec", "depends_on", "devices", "dns", "dns_search", "domainname",
		"entrypoint", "env_file", "environment", "expose", "external_links", "extra_hosts", "healthcheck",
		"hostname", "image", "init", "ipc", "isolation", "labels", "links", "logging", "mac_address",
		"network_mode", "networks", "pid", "ports", "privileged", "read_only", "restart", "security_opt",
		"shm_size", "secrets", "sysctls", "stdin_open", "stop_grace_period", "stop_signal", "tmpfs", "tty",
		"ulimits", "oom_score_adj", "user", "userns_mode", "volumes", "working_dir",
	} {
		v3ServiceKeys[k] = true
	}
}

// swarmUnsupported is the schema-legal keys that swarm nonetheless cannot honour. Dropped with a note
// rather than passed through: `docker stack deploy` prints "Ignoring unsupported options" once, to a
// terminal nobody is watching, and the preview then runs without the thing the author asked for.
// An ordered list, because the notes come out in this order.
var swarmUnsupported = []struct{ key, why string }{
	{"build", "swarm deploys images, it does not build them — build and push the image first"},
	{"container_name", "swarm names task containers itself (<stack>_<service>.<slot>.<task>)"},
	{"privileged", "swarm cannot run privileged tasks; pin this deployment to compose with `compose.orchestrator: compose`"},
	{"devices", "swarm tasks cannot mount host devices; pin this deployment to compose"},
	{"network_mode", "swarm tasks always use overlay networking"},
	{"depends_on", "swarm starts services independently — give the dependent a healthcheck or a retry loop"},
}

// SwarmifyResult is the converted document and what changed.
type SwarmifyResult struct {
	Doc *omap.Map
	// Notes is one line per change, for the job log. Empty (never nil) when the file was already
	// swarm-shaped.
	Notes []string
}

// toLabelList renders a labels mapping as a `k=v` list. Lists survive round-trips through every
// parser identically.
func toLabelList(labels *omap.Map) []any {
	out := make([]any, 0, labels.Len())
	labels.Each(func(k string, v any) {
		if v == "" {
			out = append(out, k)
		} else {
			out = append(out, k+"="+v.(string))
		}
	})
	return out
}

func dictOf(v any) (*omap.Map, bool) {
	m, ok := v.(*omap.Map)
	return m, ok && m != nil
}

// Swarmify converts a compose document to the swarm subset. Pure; never mutates the input.
//
// profiles is the spec's profiles — the services swarm will see. A service behind a profile not in
// this list is dropped, exactly as compose would not start it. `--prune` on deploy then removes a
// service that was selected last time and is not now, which is what `--remove-orphans` does for
// compose.
func Swarmify(input *omap.Map, profiles []string) SwarmifyResult {
	doc := input.Clone()
	notes := []string{}
	note := func(svc, what string) {
		if svc != "" {
			notes = append(notes, "service "+svc+": "+what)
		} else {
			notes = append(notes, what)
		}
	}

	if doc.Has("name") {
		doc.Delete("name")
		note("", "dropped top-level `name` — the stack name comes from the spec")
	}
	if v, ok := doc.Get("version"); ok {
		if s, isStr := v.(string); isStr && strings.HasPrefix(s, "2") {
			note("", "dropped `version: "+s+"` — swarm reads the v3 schema")
			doc.Delete("version")
		}
	}

	servicesV, _ := doc.Get("services")
	services, ok := dictOf(servicesV)
	if !ok {
		services = omap.New()
	}
	selected := map[string]bool{}
	for _, p := range profiles {
		selected[p] = true
	}

	for _, name := range services.Keys() {
		raw, _ := services.Get(name)
		svc, ok := dictOf(raw)
		if !ok {
			continue
		}

		// Profiles first: a dropped service needs no further conversion, and its notes would be noise.
		if svc.Has("profiles") {
			mine := []string{}
			for _, p := range svc.GetSlice("profiles") {
				mine = append(mine, JSString(p))
			}
			some := false
			for _, p := range mine {
				if selected[p] {
					some = true
					break
				}
			}
			if len(mine) > 0 && !some {
				services.Delete(name)
				note(name, "not in the selected profiles ("+strings.Join(mine, ", ")+") — left out of the stack")
				continue
			}
			svc.Delete("profiles")
		}

		deploy, ok := dictOf(svc.GetMap("deploy"))
		if !ok {
			deploy = omap.New()
		}
		setIfAbsent := func(obj *omap.Map, key string, value any) bool {
			if obj.Has(key) {
				return false
			}
			obj.Set(key, value)
			return true
		}

		// restart → restart_policy. Faithful: absent/no → none, which is what compose does.
		{
			hasRestart := svc.Has("restart")
			restart := "no"
			if hasRestart {
				rv, _ := svc.Get("restart")
				restart = JSString(rv)
			}
			policy, authored := dictOf(deploy.GetMap("restart_policy"))
			if !authored {
				policy = omap.New()
			}
			var condition string
			var maxAttempts any
			switch {
			case restart == "no" || restart == "none":
				condition = "none"
			case restart == "always" || restart == "unless-stopped":
				condition = "any"
			case strings.HasPrefix(restart, "on-failure"):
				condition = "on-failure"
				n := math.NaN() // Number(undefined) when there is no `:N`
				if _, after, found := strings.Cut(restart, ":"); found {
					// split(':')[1] — only the part up to the NEXT colon.
					after, _, _ = strings.Cut(after, ":")
					n = js.ParseNumber(after)
				}
				if js.IsInteger(n) && n > 0 {
					if n < 1<<63 {
						maxAttempts = int64(n)
					} else {
						maxAttempts = n
					}
				}
			default:
				condition = "any"
			}
			if !authored {
				policy.Set("condition", condition)
				if maxAttempts != nil {
					policy.Set("max_attempts", maxAttempts)
				}
				deploy.Set("restart_policy", policy)
				what := "no `restart`"
				if hasRestart {
					what = "restart: " + restart
				}
				what += " → deploy.restart_policy.condition: " + condition
				if maxAttempts != nil {
					what += " (max_attempts " + js.ToString(maxAttempts) + ")"
				}
				note(name, what)
			}
			svc.Delete("restart")
		}

		// Resource limits: v2/compose-spec keys → deploy.resources.
		{
			resources, ok := dictOf(deploy.GetMap("resources"))
			if !ok {
				resources = omap.New()
			}
			limits, ok := dictOf(resources.GetMap("limits"))
			if !ok {
				limits = omap.New()
			}
			reservations, ok := dictOf(resources.GetMap("reservations"))
			if !ok {
				reservations = omap.New()
			}
			moved := []string{}
			move := func(from string, into *omap.Map, key, label string) {
				v, ok := svc.Get(from)
				if !ok {
					return
				}
				switch v.(type) {
				case int64, float64:
					v = JSString(v)
				}
				if setIfAbsent(into, key, v) {
					moved = append(moved, from+" → "+label)
				}
				svc.Delete(from)
			}
			move("mem_limit", limits, "memory", "deploy.resources.limits.memory")
			move("cpus", limits, "cpus", "deploy.resources.limits.cpus")
			move("pids_limit", limits, "pids", "deploy.resources.limits.pids")
			move("mem_reservation", reservations, "memory", "deploy.resources.reservations.memory")
			for _, k := range []string{"cpu_shares", "cpu_quota", "cpu_period", "cpuset", "memswap_limit", "mem_swappiness", "oom_kill_disable"} {
				if svc.Has(k) {
					svc.Delete(k)
					note(name, "dropped `"+k+"` — no swarm equivalent (use deploy.resources)")
				}
			}
			if limits.Len() > 0 {
				resources.Set("limits", limits)
			}
			if reservations.Len() > 0 {
				resources.Set("reservations", reservations)
			}
			if resources.Len() > 0 {
				deploy.Set("resources", resources)
			}
			if len(moved) > 0 {
				note(name, strings.Join(moved, ", "))
			}
		}

		// Traefik labels → deploy.labels. The docker provider must not see them on the task container.
		{
			lv, _ := svc.Get("labels")
			container := LabelsToMap(lv)
			dlv, _ := deploy.Get("labels")
			service := LabelsToMap(dlv)
			movedAny := false
			renamed := false
			for _, k := range container.Keys() {
				if !strings.HasPrefix(k, "traefik.") {
					continue
				}
				key := k
				if k == "traefik.docker.network" {
					key = "traefik.swarm.network"
				}
				if key != k {
					renamed = true
				}
				if !service.Has(key) {
					v, _ := container.Get(k)
					service.Set(key, v)
				}
				container.Delete(k)
				movedAny = true
			}
			if service.Has("traefik.docker.network") {
				if !service.Has("traefik.swarm.network") {
					v, _ := service.Get("traefik.docker.network")
					service.Set("traefik.swarm.network", v)
				}
				service.Delete("traefik.docker.network")
				renamed = true
			}
			if movedAny {
				note(name, "moved traefik.* labels to deploy.labels (the swarm provider reads service labels)")
			}
			if renamed {
				note(name, "traefik.docker.network → traefik.swarm.network (the swarm provider reads the latter)")
			}
			if svc.Has("labels") {
				if container.Len() > 0 {
					svc.Set("labels", toLabelList(container))
				} else {
					svc.Delete("labels")
				}
			}
			if service.Len() > 0 {
				deploy.Set("labels", toLabelList(service))
			}
		}

		for _, u := range swarmUnsupported {
			if svc.Has(u.key) {
				svc.Delete(u.key)
				note(name, "dropped `"+u.key+"` — "+u.why)
			}
		}

		for _, k := range svc.Keys() {
			if strings.HasPrefix(k, "x-") || v3ServiceKeys[k] {
				continue
			}
			svc.Delete(k)
			note(name, "dropped `"+k+"` — not in the compose v3 schema swarm reads")
		}

		if deploy.Len() > 0 {
			svc.Set("deploy", deploy)
		}
	}

	return SwarmifyResult{Doc: doc, Notes: notes}
}

// ── command lines ─────────────────────────────────────────────────────────────────────────────────

// StackDeployCmd is `docker stack deploy`. `--prune` is `--remove-orphans`; `--with-registry-auth`
// ships the manager's registry credential (the one registries manages) to workers, which otherwise
// cannot pull a private image; `--detach=true` returns once the services exist — convergence is
// readiness's job, same as `compose up -d`.
func StackDeployCmd(stack string, files []string) string {
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = "-c " + Shq(f)
	}
	return "docker stack deploy " + strings.Join(parts, " ") + " --prune --with-registry-auth --detach=true " + Shq(stack)
}

// StackRmCmd is `docker stack rm`, then WAIT. `stack rm` returns before the tasks and networks are
// gone, and a redeploy that races it fails with "network … has active endpoints". Bounded at 60s.
//
// volumes: also remove the stack's labelled volumes (teardown). Sleep keeps them.
func StackRmCmd(stack string, volumes bool) string {
	s := Shq(stack)
	wait := "for i in $(seq 1 60); do " +
		"[ -z \"$(docker stack ps " + s + " -q 2>/dev/null)\" ] && " +
		"[ -z \"$(docker network ls -q --filter " + Shq("label="+StackLabel+"="+stack) + ")\" ] && break; sleep 1; done"
	vols := ""
	if volumes {
		vols = "; docker volume ls -q --filter " + Shq("label="+StackLabel+"="+stack) + " | xargs -r docker volume rm >/dev/null"
	}
	return "docker stack rm " + s + "; " + wait + vols
}

// StackPsCmd is `docker stack ps`.
func StackPsCmd(stack string) string {
	return "docker stack ps " + Shq(stack)
}

// LogsOptions is the rest of what `logs` can be asked for. Until is read by compose only — `docker
// service logs` has no `--until`, and the API refuses that parameter in swarm mode rather than
// dropping it.
type LogsOptions struct {
	Timestamps bool
	Since      string
	Until      string
	Follow     bool
}

// StackLogsCmd is `docker service logs`, per service. A whole-stack read loops over the stack's
// services; FOLLOWED, they run in parallel and `wait` keeps the shell alive until every one ends.
// service "" means the whole stack. tail is a JS number (the API echoes `?tail=1.5`), truncated.
//
// The flags are `docker service logs`'s OWN, not compose's. It takes --details, --follow,
// --no-resolve, --no-task-ids, --no-trunc, --raw, --since, --tail and --timestamps, and answers
// anything else with `unknown flag` and a usage dump — so one borrowed flag breaks every log read on
// the host, not just the option that was borrowed. `--no-color` was borrowed from `compose logs` and
// did precisely that. It bought nothing to begin with: this runs down a pipe, and the prefix is only
// coloured on a TTY.
//
// `--no-task-ids` is deliberately NOT passed, though it is the noisiest thing here — every line
// arrives prefixed `<stack>_<svc>.<slot>.<taskid>@<node>    |`, some 45 bytes of it, which is why a
// swarm log read is so much heavier than a compose one. The task id is the only in-band evidence
// that swarm destroyed a task and scheduled a new one; drop it and a crash-looping preview reads as
// one unbroken log, which is the failure this product exists to make visible. Prefix noise a reader
// can skim beats a restart a reader cannot see. It is also the bytes GET /logs returns, so flipping
// it is a contract decision, not a cleanup — which is the other reason it is not bundled in here.
func StackLogsCmd(stack string, tail float64, service string, opts LogsOptions) string {
	flags := []string{"--tail " + TruncString(tail)}
	if opts.Follow {
		flags = append(flags, "--follow")
	}
	if opts.Timestamps {
		flags = append(flags, "--timestamps")
	}
	if opts.Since != "" {
		flags = append(flags, "--since "+Shq(opts.Since))
	}
	f := strings.Join(flags, " ")
	if service != "" {
		return "docker service logs " + f + " " + Shq(stack+"_"+service)
	}
	s := Shq(stack)
	each := "docker service logs " + f + " \"$svc\" 2>&1"
	// `&` is itself a command terminator: `cmd &; done` is a bash syntax error, not a background job,
	// and it took the whole followed read down with it. So the separator before `done` is the `&` when
	// following, a `;` when not.
	sep, tail2 := "; ", ""
	if opts.Follow {
		each += " &"
		sep, tail2 = " ", "; wait"
	}
	return "svcs=$(docker stack services --format '{{.Name}}' " + s + "); " +
		"[ -n \"$svcs\" ] || { echo \"no services in stack " + stack + "\" >&2; exit 1; }; " +
		"for svc in $svcs; do " + each + sep + "done" + tail2
}

// TruncString is `${Math.trunc(n)}`.
func TruncString(n float64) string { return js.NumberString(math.Trunc(n)) }

// ── the cluster ──────────────────────────────────────────────────────────────────────────────────

// Node is one row of `docker node ls`.
type Node struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// Role is `manager` or `worker`.
	Role string `json:"role"`
	// Status is `ready` / `down` / `unknown` … as docker reports it, lowercased.
	Status       string `json:"status"`
	Availability string `json:"availability"`
	// ManagerStatus is `leader` / `reachable` / `unreachable` for a manager; null for a worker.
	ManagerStatus *string `json:"managerStatus"`
	EngineVersion string  `json:"engineVersion"`
	// Self is the node this API runs on.
	Self bool `json:"self"`
}

// Info is what the swarm panel shows.
type Info struct {
	// Reachable is false when docker did not answer — every other field is then unknown, not empty.
	Reachable bool `json:"reachable"`
	// Active is whether this daemon is a swarm manager. `inactive` hosts run compose.
	Active bool    `json:"active"`
	NodeID *string `json:"nodeId"`
	// ManagerAddr is `<advertise-addr>:2377`, what a worker joins.
	ManagerAddr *string `json:"managerAddr"`
	Nodes       []Node  `json:"nodes"`
	Error       *string `json:"error,omitempty"`
}

func unreachable() Info { return Info{Nodes: []Node{}} }

// getStr is `m[key] ?? def` for a string-valued field, with `.toLowerCase()` left to the caller.
func getStr(m *omap.Map, key, def string) string {
	v, ok := m.Get(key)
	if !ok || v == nil {
		return def
	}
	return JSString(v)
}

// SwarmInfo is what the swarm panel shows. Read straight from docker every time — nothing here is
// cached (invariant 10).
func SwarmInfo(r exec.Runner) Info {
	info := r.Run("docker info --format '{{json .Swarm}}'", exec.RunOptions{Label: "docker info"})
	if !info.OK {
		return unreachable()
	}
	src := info.Stdout
	if src == "" {
		src = "{}"
	}
	parsed, err := omap.Parse([]byte(src))
	if err != nil {
		return unreachable()
	}
	raw, _ := parsed.(*omap.Map)
	if raw == nil {
		raw = omap.New()
	}
	active := getStr(raw, "LocalNodeState", "") == "active"
	var self *string
	if v, ok := raw.Get("NodeID"); ok && v != nil {
		self = stringPtr(JSString(v))
	}
	// RemoteManagers.find(m => m.NodeID === self)?.Addr ?? RemoteManagers[0]?.Addr ?? null
	var manager *string
	if managers := raw.GetSlice("RemoteManagers"); managers != nil {
		for _, m := range managers {
			mm, _ := m.(*omap.Map)
			if mm == nil || self == nil {
				continue
			}
			if id, ok := mm.Get("NodeID"); ok && id != nil && JSString(id) == *self {
				if a, ok := mm.Get("Addr"); ok && a != nil {
					manager = stringPtr(JSString(a))
				}
				break
			}
		}
		if manager == nil && len(managers) > 0 {
			if mm, _ := managers[0].(*omap.Map); mm != nil {
				if a, ok := mm.Get("Addr"); ok && a != nil {
					manager = stringPtr(JSString(a))
				}
			}
		}
	}
	if manager == nil {
		if addr := getStr(raw, "NodeAddr", ""); addr != "" {
			manager = stringPtr(addr + ":2377")
		}
	}
	out := Info{Reachable: true, Active: active, NodeID: self, ManagerAddr: manager, Nodes: []Node{}}
	if e := getStr(raw, "Error", ""); e != "" {
		out.Error = stringPtr(e)
	}
	ctrl, _ := raw.Get("ControlAvailable")
	if !active || ctrl != true {
		return out
	}

	ls := r.Run("docker node ls --format '{{json .}}'", exec.RunOptions{Label: "docker node ls"})
	if !ls.OK {
		first, _, _ := strings.Cut(strings.TrimSpace(ls.Stderr), "\n")
		out.Error = stringPtr(first)
		return out
	}
	for _, line := range strings.Split(ls.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parsed, err := omap.Parse([]byte(line))
		if err != nil {
			continue
		}
		n, _ := parsed.(*omap.Map)
		if n == nil {
			n = omap.New()
		}
		var managerStatus *string
		if ms := strings.ToLower(getStr(n, "ManagerStatus", "")); ms != "" {
			managerStatus = stringPtr(ms)
		}
		role := "worker"
		if managerStatus != nil {
			role = "manager"
		}
		id := getStr(n, "ID", "")
		out.Nodes = append(out.Nodes, Node{
			ID:            id,
			Hostname:      getStr(n, "Hostname", ""),
			Role:          role,
			Status:        strings.ToLower(getStr(n, "Status", "unknown")),
			Availability:  strings.ToLower(getStr(n, "Availability", "")),
			ManagerStatus: managerStatus,
			EngineVersion: getStr(n, "EngineVersion", ""),
			Self:          getStr(n, "Self", "") == "true" || (self != nil && n.Has("ID") && id == *self),
		})
	}
	return out
}

func stringPtr(s string) *string { return &s }

// WorkerJoinToken is the worker join token, or "" when docker would not hand one out. A SECRET:
// whoever holds it can add a node that runs any task.
func WorkerJoinToken(r exec.Runner) string {
	res := r.Run("docker swarm join-token -q worker", exec.RunOptions{Label: "docker swarm join-token"})
	tok := strings.TrimSpace(res.Stdout)
	if res.OK && tok != "" {
		return tok
	}
	return ""
}

// Port is one row of SwarmPorts.
type Port struct {
	Port string `json:"port"`
	Why  string `json:"why"`
}

// SwarmPorts is the ports a worker must reach on the manager (and managers on each other), from
// Docker's own swarm docs. Stated once here so the panel, the script and the docs agree.
var SwarmPorts = []Port{
	{"2377/tcp", "cluster management (worker → manager)"},
	{"7946/tcp+udp", "node discovery (every node ↔ every node)"},
	{"4789/udp", "overlay network traffic — VXLAN (every node ↔ every node)"},
}

// PortList is `SWARM_PORTS.map(p => p.port).join(', ')` — what init, the CLI and the script print.
func PortList() string {
	parts := make([]string, len(SwarmPorts))
	for i, p := range SwarmPorts {
		parts[i] = p.Port
	}
	return strings.Join(parts, ", ")
}

// JoinCommand is the `docker swarm join` line.
func JoinCommand(token, managerAddr string) string {
	return "docker swarm join --token " + token + " " + managerAddr
}

// JoinFormats is the shapes the join material comes in. Add one here and both the API and the CLI
// offer it.
var JoinFormats = []string{"token", "command", "script", "cloud-config"}

// IsJoinFormat reports whether v is one of JoinFormats.
func IsJoinFormat(v string) bool {
	for _, f := range JoinFormats {
		if f == v {
			return true
		}
	}
	return false
}

// JoinRefusal is why the join material could not be produced. The CALLER maps it — to an HTTP status
// in api, to an exit code in cli — because the reasons differ in kind: NotAManager is a refusal
// about this host, Unreachable is "could not tell", and the two Bad* are the caller's own input.
type JoinRefusal string

const (
	Unreachable JoinRefusal = "unreachable"
	NotAManager JoinRefusal = "not-a-manager"
	NoToken     JoinRefusal = "no-token"
	BadFormat   JoinRefusal = "bad-format"
	BadDistro   JoinRefusal = "bad-distro"
)

// JoinResult is the join material, or the refusal.
type JoinResult struct {
	OK bool
	// Text and ManagerAddr are set when OK.
	Text        string
	ManagerAddr string
	// Kind and Message are set when !OK.
	Kind    JoinRefusal
	Message string
}

// CloudInit is the seam cloudinit fills at init (cloudinit imports this package for JoinCommand and
// the port table, so the call back cannot be an import — the TS used a dynamic import for the same
// reason). Distros is cloudinit's own list, the one `distro` is validated against; Render is
// renderWorkerCloudInit. Until it is filled, every `cloud-config` request is a bad-distro refusal.
var CloudInit struct {
	Distros []string
	Render  func(token, managerAddr, distro string) string
}

// JoinArgs is what JoinMaterial takes.
type JoinArgs struct {
	Runner exec.Runner
	Format string
	// Distro is only read for `cloud-config`; nil means ubuntu, "" is refused (the API's `?distro=`).
	Distro *string
}

// JoinMaterial is what a new worker runs, in the requested shape.
//
// ONE implementation, because there are two callers — `GET /api/swarm/join` and `pstack swarm join`
// — and a drifted copy here would hand two operators different commands for the same cluster.
//
// THE RESULT IS A SECRET whichever shape it takes: every one of them embeds the worker join token,
// and whoever holds that can add a node that runs any task on this swarm. Neither caller logs it,
// and redact masks `SWMTKN-…` anywhere it might otherwise be echoed.
func JoinMaterial(a JoinArgs) JoinResult {
	if !IsJoinFormat(a.Format) {
		return JoinResult{Kind: BadFormat, Message: "format must be one of: " + strings.Join(JoinFormats, ", ")}
	}
	distro := "ubuntu"
	if a.Distro != nil {
		distro = *a.Distro
	}
	if a.Format == "cloud-config" {
		known := false
		for _, d := range CloudInit.Distros {
			if d == distro {
				known = true
			}
		}
		if !known {
			return JoinResult{Kind: BadDistro, Message: "distro must be one of: " + strings.Join(CloudInit.Distros, ", ")}
		}
	}

	info := SwarmInfo(a.Runner)
	if !info.Reachable {
		return JoinResult{Kind: Unreachable, Message: "docker did not answer"}
	}
	if !info.Active || info.ManagerAddr == nil || *info.ManagerAddr == "" {
		return JoinResult{Kind: NotAManager, Message: "this daemon is not a swarm manager — nothing to join"}
	}
	token := WorkerJoinToken(a.Runner)
	if token == "" {
		return JoinResult{Kind: NoToken, Message: "docker would not hand out a worker join token"}
	}

	addr := *info.ManagerAddr
	var text string
	switch a.Format {
	case "token":
		text = token + "\n"
	case "command":
		text = JoinCommand(token, addr) + "\n"
	case "script":
		text = JoinScript(token, addr)
	default:
		text = CloudInit.Render(token, addr, distro)
	}
	return JoinResult{OK: true, Text: text, ManagerAddr: addr}
}

// SwarmReport is the cluster as a person reads it — `pstack swarm` on the host.
//
// Says WHICH of the three states it is in rather than printing an empty table for two of them:
// docker did not answer (nothing is known), this daemon is not a manager (previews run with
// compose), or here are the nodes. Never carries a join token.
func SwarmReport(info Info) string {
	if !info.Reachable {
		return strings.Join([]string{
			"docker did not answer.",
			"",
			"  Nothing about the swarm is known — which is not the same as there being no swarm.",
			"  Check the daemon: `docker info`.",
		}, "\n")
	}
	if !info.Active {
		lines := []string{
			"this host is not a swarm manager.",
			"",
			"  Previews here run with `docker compose` on this box. To scale out, re-run init in swarm",
			"  mode — every preview must be torn down first, because the two shared networks have to be",
			"  recreated as overlays:",
			"",
			"      pstack init --domain <domain> --acme-email <you@example.com> --orchestrator swarm",
		}
		if info.Error != nil && *info.Error != "" {
			lines = append(lines, "", "  docker also said: "+*info.Error)
		}
		return strings.Join(lines, "\n")
	}

	rows := make([][]string, 0, len(info.Nodes))
	anySelf := false
	for _, n := range info.Nodes {
		host := n.Hostname
		if n.Self {
			host += " *"
			anySelf = true
		}
		role := n.Role
		if n.ManagerStatus != nil && *n.ManagerStatus != "" {
			role += " (" + *n.ManagerStatus + ")"
		}
		rows = append(rows, []string{host, role, n.Status, n.Availability, n.EngineVersion, js.Slice(n.ID, 0, 12)})
	}
	head := []string{"HOSTNAME", "ROLE", "STATUS", "AVAILABILITY", "ENGINE", "ID"}
	width := make([]int, len(head))
	for i, h := range head {
		width[i] = js.Len(h)
		for _, r := range rows {
			if l := js.Len(r[i]); l > width[i] {
				width[i] = l
			}
		}
	}
	line := func(cells []string) string {
		padded := make([]string, len(cells))
		for i, c := range cells {
			padded[i] = js.PadEnd(c, width[i])
		}
		return strings.TrimRightFunc("  "+strings.Join(padded, "  "), unicode.IsSpace)
	}

	addr := "(address unknown)"
	if info.ManagerAddr != nil {
		addr = *info.ManagerAddr
	}
	plural := "s"
	if len(info.Nodes) == 1 {
		plural = ""
	}
	selfNote := ""
	if anySelf {
		selfNote = "  (* this host)"
	}
	out := []string{
		"swarm manager  " + addr,
		js.ToString(int64(len(info.Nodes))) + " node" + plural + selfNote,
	}
	if info.Error != nil && *info.Error != "" {
		out = append(out, "docker also said: "+*info.Error)
	}
	out = append(out, "", line(head))
	for _, r := range rows {
		out = append(out, line(r))
	}
	out = append(out,
		"",
		"add a worker:",
		"  pstack swarm join                       the `docker swarm join` line",
		"  pstack swarm join --format script       installs Docker first, then joins",
		"  pstack swarm join --format cloud-config --distro debian -o worker.yaml",
		"",
		"open between every pair of nodes first:",
	)
	for _, p := range SwarmPorts {
		out = append(out, "  "+js.PadEnd(p.Port, 14)+" "+p.Why)
	}
	return strings.Join(out, "\n")
}

// JoinScript is a shell script that installs Docker (the vendor convenience script — the same one
// every quickstart uses; distro-exact installs are the cloud-config's job) and joins. `set -e` so a
// failed install never reaches the join with nothing to join.
func JoinScript(token, managerAddr string) string {
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"# Join this machine to the pstack swarm as a WORKER. Run as root (or with sudo).",
		"# Open " + PortList() + " between this machine and the manager first.",
		"set -euo pipefail",
		"if ! command -v docker >/dev/null 2>&1; then",
		"  curl -fsSL https://get.docker.com | sh",
		"fi",
		"systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true",
		`if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]; then`,
		`  echo "already part of a swarm: $(docker info --format '{{.Swarm.NodeID}}')"; exit 0`,
		"fi",
		JoinCommand(token, managerAddr),
		"docker info --format 'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})'",
		"",
	}, "\n")
}
