// The agent's docker commands, as strings. Init runs the create commands (invariant 12: the agent's
// lifecycle is init, and so upgrade, `pstack ui` and `pstack logging`, never the API); the api's
// stats loop runs the logs commands.
//
//   - A compose host, and the control-plane node of a swarm, run the agent as one local container.
//     On swarm the global service skips that node (`node.id!=<self>`): a drained manager
//     (`--previews-on workers`) runs no swarm task at all, and it is the node whose disk filled
//     (NP-R10). Without the constraint an undrained manager would report twice.
//   - The image is AgentImage unless PSTACK_AGENT_IMAGE is set at init time. That override is
//     env-only and never read back: re-export it on every `pstack init` and `pstack upgrade`, or the
//     agent switches back to AgentImage. Cloud-init does not pass it (NP-R6).
//   - Logs are capped at 1 MB in json-file: the stats loop reads only the last 90 s.
package nodestats

import "github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"

// AgentImage is the stock docker CLI image the agent script runs in on every node.
const AgentImage = "docker:29-cli"

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
