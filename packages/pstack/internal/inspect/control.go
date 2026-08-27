// The CONTROL STACK's own runtime — traefik, pstack, the optional advanced UI — as an operator's
// page shows it.
//
// The point of this view is the two numbers nothing surfaced before: RESTART COUNT and OOM-KILLED.
// A Traefik that restarts loses its in-memory ACME challenge tokens, so certificates quietly stop
// issuing while every container reports healthy — an incident that was diagnosed from a default
// certificate's notBefore timestamp because the restart itself was visible nowhere.
//
// One mutation lives here too, and one refusal: RestartControlService restarts any control
// container EXCEPT pstack's own. Recreating the container performing the operation kills the
// operation, and if the new image is broken, the thing that could have repaired the host died with
// it — the rule the control template's header states, enforced by name instead of by distance.
package inspect

import (
	"errors"
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
)

// ControlProject is initctl's — the project `pstack init` actually creates — re-exported, never a
// second literal: this label is the restart's entire trust boundary, and a drifted copy would leave
// `pstack-control` an unclaimed namespace any deployment could squat.
const ControlProject = initctl.ControlProject

// selfService is the one service the API must never restart: its own.
const selfService = "pstack"

// ControlContainer is ContainerInfo plus the two fields that only matter for the control stack.
type ControlContainer struct {
	ContainerInfo
	// OOMKilled is docker's own flag: the LAST exit was the kernel's, not the process's. Paired
	// with RestartCount it answers "why did traefik restart at noon" without an SSH session.
	OOMKilled bool `json:"oomKilled"`
	// MemLimitBytes is the container's limit, nil when unlimited — the denominator OOMKilled needs.
	// Docker itself says 0 for "no limit"; that is normalized to nil here so the wire carries ONE
	// encoding of the fact (rule 2: a tri-state is a pointer, and its nil must mean something).
	MemLimitBytes *int64 `json:"memLimitBytes"`
}

// ControlView is what GET /api/control/runtime answers.
type ControlView struct {
	Containers []ControlContainer `json:"containers"`
	// Reachable: false means docker did not answer — every field above is "unknown", never "empty".
	Reachable bool `json:"reachable"`
}

// ControlRuntime lists the control stack's containers. Same probes DeploymentRuntime uses, scoped
// by the pinned project label.
func ControlRuntime(r exec.Runner) ControlView {
	out := ControlView{Containers: []ControlContainer{}}
	ids, ok := idsByLabel(r, "com.docker.compose.project="+ControlProject)
	if !ok {
		return out
	}
	out.Reachable = true
	for _, raw := range inspectIDs(r, ids) {
		c := ControlContainer{ContainerInfo: toContainer(raw, "")}
		if raw.State != nil && raw.State.OOMKilled != nil {
			c.OOMKilled = *raw.State.OOMKilled
		}
		if raw.HostConfig != nil && raw.HostConfig.Memory != nil && *raw.HostConfig.Memory > 0 {
			c.MemLimitBytes = raw.HostConfig.Memory
		}
		out.Containers = append(out.Containers, c)
	}
	sort.SliceStable(out.Containers, func(i, j int) bool {
		a, b := "", ""
		if out.Containers[i].Service != nil {
			a = *out.Containers[i].Service
		}
		if out.Containers[j].Service != nil {
			b = *out.Containers[j].Service
		}
		if a != b {
			return a < b
		}
		return out.Containers[i].Name < out.Containers[j].Name
	})
	return out
}

// The three ways RestartControlService refuses, for the route to map onto statuses.
var (
	ErrRestartSelf = errors.New(`refusing to restart "pstack": it is the container answering this request. Recreating it kills the operation in flight, and if its image is broken, the thing that could repair this host dies with it. Restart it from the host: docker compose -p pstack-control restart pstack`)
	ErrNoDocker    = errors.New("docker did not answer")
	ErrNoService   = errors.New("no such control service")
)

// RestartControlService restarts one control-stack container by its compose service name and
// returns the container's name. `pstack` is refused — see the package comment.
func RestartControlService(r exec.Runner, service string) (string, error) {
	if service == selfService {
		return "", ErrRestartSelf
	}
	view := ControlRuntime(r)
	if !view.Reachable {
		return "", ErrNoDocker
	}
	for _, c := range view.Containers {
		if c.Service != nil && *c.Service == service {
			res := r.Run("docker restart "+shq(c.ID), exec.RunOptions{Label: "docker restart"})
			if !res.OK {
				return "", errors.New("docker restart failed: " + strings.TrimSpace(res.Stderr))
			}
			return c.Name, nil
		}
	}
	return "", ErrNoService
}
