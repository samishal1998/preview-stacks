// Package signals answers the two questions something outside pstack asks before it turns a worker
// machine on or off:
//
//   - "Do you need another machine?" — a task docker refused to place says yes, in docker's words.
//   - "Can I take this one away?" — a worker running nothing says yes.
//
// That is all it does. It counts no CPU and no memory, and it decides nothing: how much spare room
// a fleet should keep is the machine manager's policy, not pstack's, and swarm only counts the
// reservations a compose file happens to declare, which is usually none. Reporting what docker says
// is both simpler and true.
//
// Nothing here is stored. `Look` runs three read-only docker commands every time it is called
// (invariant 10); the only memory in the feature is the api package's "when did I first see this
// node empty" map, which is allowed to forget on restart because a late signal is harmless and an
// early one is not.
//
// TWO DOCKER DETAILS THIS DEPENDS ON, both checked against docker 28:
//
//   - `docker service ps` truncates its error column at 30 characters unless `--no-trunc` is passed,
//     which would leave every reason reading `no suitable node (insufficie…`.
//   - It wraps that error in literal double quotes, so the decoded JSON string is `"…"` and has to
//     be unwrapped before anything can match on it.
//
// A task's `Node` is the machine's HOSTNAME, not its id, and is empty while the task is unplaced —
// so tasks join to nodes on hostname.
package signals

import (
	"encoding/json"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// Unplaced is how swarm starts the error on a task no node would take. The rest of the sentence is
// docker's explanation, passed through untouched.
const Unplaced = "no suitable node"

// Node is one machine in the swarm and how much work it is carrying.
type Node struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// Role is `manager` or `worker`. The manager is never a machine to take away: it runs pstack
	// itself, and the isolation-axis hooks run beside it.
	Role string `json:"role"`
	// State is docker's own word: ready / down / unknown / disconnected.
	State string `json:"state"`
	// Availability is active / pause / drain.
	Availability string `json:"availability"`
	// Tasks counts the replicated tasks placed here. A global service's tasks are excluded: one
	// follows every machine, so counting them would leave no node ever looking empty.
	Tasks int `json:"tasks"`
}

// Stuck is one task docker would not place.
type Stuck struct {
	Task    string `json:"task"`
	Service string `json:"service"`
	// Stack is the swarm namespace — the part of the service name before the first `_`.
	Stack string `json:"stack"`
	// Reason is docker's sentence, unquoted and otherwise unedited.
	Reason string `json:"reason"`
}

// View is the whole answer. Swarm is false on a host that runs compose, where there is nothing to
// report; Reachable is false when docker did not answer at all, which is not the same thing.
type View struct {
	Swarm     bool
	Reachable bool
	Nodes     []Node
	Stuck     []Stuck
}

// Look asks docker what the swarm looks like right now.
func Look(r exec.Runner) View {
	v := View{Nodes: []Node{}, Stuck: []Stuck{}}

	// SwarmInfo already runs `docker info` and `docker node ls` and parses both; this needs the same
	// rows plus a task count, so it reuses them rather than asking twice.
	info := swarm.SwarmInfo(r)
	v.Reachable = info.Reachable
	if !info.Reachable || !info.Active {
		return v
	}
	v.Swarm = true

	at := map[string]int{} // hostname → index in v.Nodes
	for _, n := range info.Nodes {
		v.Nodes = append(v.Nodes, Node{
			ID: n.ID, Hostname: n.Hostname, Role: n.Role,
			State: n.Status, Availability: n.Availability,
		})
		at[n.Hostname] = len(v.Nodes) - 1
	}

	ids, global := services(r)
	if len(ids) == 0 {
		return v
	}
	for _, t := range tasks(r, ids) {
		service := serviceOf(t.Name)
		if t.pending() {
			if reason := unquote(t.Error); strings.HasPrefix(reason, Unplaced) {
				v.Stuck = append(v.Stuck, Stuck{
					Task: t.ID, Service: service, Stack: stackOf(service), Reason: reason,
				})
			}
			continue
		}
		if t.Node == "" || global[service] {
			continue
		}
		if i, ok := at[t.Node]; ok {
			v.Nodes[i].Tasks++
		}
	}
	return v
}

// rawService is the part of `docker service ls --format '{{json .}}'` this reads.
type rawService struct {
	ID, Name, Mode string
}

// services returns every service id, in docker's order, and the names of the global ones.
func services(r exec.Runner) (ids []string, global map[string]bool) {
	global = map[string]bool{}
	res := r.Run("docker service ls --format '{{json .}}'", exec.RunOptions{Label: "docker service ls"})
	if !res.OK {
		return nil, global
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s rawService
		if json.Unmarshal([]byte(line), &s) != nil || s.ID == "" {
			continue
		}
		ids = append(ids, s.ID)
		if strings.EqualFold(s.Mode, "global") {
			global[s.Name] = true
		}
	}
	return ids, global
}

// rawTask is the part of `docker service ps --format '{{json .}}'` this reads. The full set is
// ID, Name, Image, Node, DesiredState, CurrentState, Error, Ports.
type rawTask struct {
	ID, Name, Node, DesiredState, CurrentState, Error string
}

// pending is `Pending 3 minutes ago` — match the first word, as inspect.TaskState does.
func (t rawTask) pending() bool {
	f := strings.Fields(t.CurrentState)
	return len(f) > 0 && strings.EqualFold(f[0], "pending")
}

// tasks lists every task that should be running, across the services given.
func tasks(r exec.Runner, ids []string) []rawTask {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = swarm.Shq(id)
	}
	cmd := "docker service ps --no-trunc --filter desired-state=running --format '{{json .}}' " + strings.Join(quoted, " ")
	res := r.Run(cmd, exec.RunOptions{Label: "docker service ps"})
	if !res.OK {
		return nil
	}
	out := []rawTask{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var t rawTask
		if json.Unmarshal([]byte(line), &t) == nil {
			out = append(out, t)
		}
	}
	return out
}

// serviceOf is `pr-42_web.1` → `pr-42_web`. The suffix is a slot number or a node id, neither of
// which contains a dot, while a service name may — so cut at the LAST one.
func serviceOf(taskName string) string {
	if i := strings.LastIndex(taskName, "."); i > 0 {
		return taskName[:i]
	}
	return taskName
}

// stackOf is `pr-42_web` → `pr-42`: swarm names a stack's services `<stack>_<service>`.
func stackOf(service string) string {
	if i := strings.Index(service, "_"); i > 0 {
		return service[:i]
	}
	return ""
}

// unquote removes the one pair of double quotes docker's formatter wraps a non-empty error in.
func unquote(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		return s[1 : len(s)-1]
	}
	return s
}
