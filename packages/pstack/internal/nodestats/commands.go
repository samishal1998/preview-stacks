// The agent's and the prunes' docker commands, as strings, and the reading of a prune's output.
// Init runs the agent's create commands (invariant 12: the agent's lifecycle is init, and so upgrade,
// `pstack ui` and `pstack logging`, never the API); the api's stats loop runs the logs commands and
// the prunes (internal/api/prune.go).
//
//   - A compose host, and the control-plane node of a swarm, run the agent as one local container.
//     On swarm the global service skips that node (`node.id!=<self>`): a drained manager
//     (`--previews-on workers`) runs no swarm task at all, and it is the node whose disk filled
//     (NP-R10). Without the constraint an undrained manager would report twice.
//   - The image is AgentImage unless PSTACK_AGENT_IMAGE is set at init time. That override is
//     env-only and never read back: re-export it on every `pstack init` and `pstack upgrade`, or the
//     agent switches back to AgentImage. Cloud-init does not pass it (NP-R6).
//   - Logs are capped at 1 MB in json-file: the stats loop reads only the last 90 s.
//
// A PRUNE prints one `pstack-prune v1 <node id, or - on compose> <size>` line per docker prune that
// succeeded, from docker's `Total reclaimed space:` (image, and the classic builder) or `Total:`
// (BuildKit's builder) line. A failed prune prints nothing, so nothing is reported for it. POSIX sh and sed only:
// it runs in bash in the pstack container and in busybox in the agent image.
package nodestats

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// AgentImage is the stock docker CLI image the agent script runs in on every node, pinned by digest so
// every node runs the image the release was tested with. Bump it per release.
const AgentImage = "docker:29.9.2-cli@sha256:5e6baf67325a79f64ed3bed6863954d5e05d0f3d4e57cbdb14809346814bbfdb"

// AgentName is the agent's container name on the local node and its service name on swarm.
const AgentName = "pstack-agent"

// AgentContainerCmd replaces the local agent container: rm then run, so a re-run init picks up a new
// script or image.
func AgentContainerCmd(script, image string) string {
	return "docker rm -f " + AgentName + " >/dev/null 2>&1; docker run -d --name " + AgentName +
		" --restart unless-stopped --label pstack.role=agent" +
		" -v /var/run/docker.sock:/var/run/docker.sock:ro -v /:/host:ro" +
		" --log-driver json-file --log-opt max-size=1m --entrypoint sh " + image + " -c " + swarm.Shq(script)
}

// AgentServiceCmd replaces the global agent service on every node but selfNodeID. "" adds no
// constraint (a dry run, which reads no id); init never runs it live with "".
func AgentServiceCmd(script, image, selfNodeID string) string {
	constraint := ""
	if selfNodeID != "" {
		constraint = " --constraint " + swarm.Shq("node.id!="+selfNodeID)
	}
	return "docker service rm " + AgentName + " >/dev/null 2>&1; docker service create --detach --mode global --name " + AgentName +
		" --label pstack.role=agent" + constraint +
		" --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly" +
		" --mount type=bind,src=/,dst=/host,readonly --log-driver json-file --log-opt max-size=1m --restart-condition any" +
		" --entrypoint sh " + image + " -c " + swarm.Shq(script)
}

// AgentLogsCmds reads the last 90 s of agent output: the local container's, plus the service's
// (`--raw`, so no task prefix) on swarm.
func AgentLogsCmds(onSwarm bool) []string {
	cmds := []string{"docker logs --since 90s " + AgentName + " 2>/dev/null"}
	if onSwarm {
		cmds = append(cmds, "docker service logs --since 90s --raw "+AgentName+" 2>/dev/null")
	}
	return cmds
}

// PruneScript prunes unused images older than untilH hours, and the build cache too when builder.
// untilH 0 is any age (the emergency prune, which never touches the build cache).
func PruneScript(untilH int, builder bool) string {
	filter := ""
	if untilH > 0 {
		filter = " --filter until=" + strconv.Itoa(untilH) + "h"
	}
	s := `n=$(docker info --format "{{.Swarm.NodeID}}" 2>/dev/null); n=${n:--}; ` +
		`docker image prune -af` + filter + ` | sed -n "s/^Total reclaimed space: /pstack-prune v1 $n /p"`
	if builder {
		// The classic CLI in the pstack image prints `Total reclaimed space:`, buildx in the agent image
		// `Total:\t`. \{0,1\}, not \?: busybox sed runs the job.
		s += `; docker builder prune -af` + filter + ` | sed -n "s/^Total\( reclaimed space\)\{0,1\}:[[:space:]]*/pstack-prune v1 $n /p"`
	}
	return s
}

// PruneJobCmd runs script once on every node the constraint allows (global) or on one (replicated),
// in the agent's own image: a PSTACK_AGENT_IMAGE override reaches the prunes without the pstack
// container knowing it. Not --detach: a job's create returns when the job has completed.
func PruneJobCmd(name, script, constraint string, global bool) string {
	mode := " --mode replicated-job --replicas 1"
	if global {
		mode = " --mode global-job"
	}
	if constraint != "" {
		mode += " --constraint " + swarm.Shq(constraint)
	}
	return "docker service create --name " + name + mode + " --restart-condition none" +
		" --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock --entrypoint sh" +
		` "$(docker service inspect -f '{{.Spec.TaskTemplate.ContainerSpec.Image}}' ` + AgentName + `)" -c ` + swarm.Shq(script)
}

// Reclaimed is what one node's prunes freed. Node is "" on a compose host.
type Reclaimed struct {
	Node  string
	Bytes int64
}

const prunePrefix = "pstack-prune v1 "

// ParsePrune sums the prune lines per node, sorted by Node. Any other line, or a size that does not
// read, is skipped. Never nil.
func ParsePrune(out string) []Reclaimed {
	sums := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), prunePrefix)
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 {
			continue
		}
		n, ok := ParseSize(f[1])
		if !ok {
			continue
		}
		if f[0] == "-" {
			f[0] = ""
		}
		sums[f[0]] += n
	}
	got := make([]Reclaimed, 0, len(sums))
	for node, n := range sums {
		got = append(got, Reclaimed{node, n})
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].Node < got[j].Node })
	return got
}

// sizeUnits are docker's decimal units (go-units HumanSize).
var sizeUnits = map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}

// ParseSize reads docker's size: "0B", "512kB", "1.234GB".
func ParseSize(s string) (int64, bool) {
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	unit, ok := sizeUnits[s[i:]]
	if err != nil || !ok {
		return 0, false
	}
	return int64(math.Round(n * unit)), true
}
