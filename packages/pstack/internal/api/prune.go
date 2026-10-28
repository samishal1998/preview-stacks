// Image prunes, decided on the stats tick: an emergency prune of a node whose disk is in pressure,
// and a scheduled prune of every node every 6 hours. nodestats/commands.go spells the commands and
// reads their output; this file decides when, runs them, and tells the world.
//
// ── WHEN ────────────────────────────────────────────────────────────────────────────────────────
//
//   - Emergency (`pressure`): a fresh node in disk pressure, at most once per node per 10 minutes,
//     so a disk full of something other than images is not pruned in a loop. Unused images of any
//     age; the build cache is left alone. lastEmergency is set when the prune starts, whether or not
//     it works (NP-R2).
//   - Scheduled: every 6 hours from the first tick (NP-R3), when image_prune_after_h is above 0.
//     Images and build cache older than that many hours. It does not wait for agent data (NP-R26).
//   - One prune at a time. An emergency wins a tick over the schedule.
//
// ── WHERE ───────────────────────────────────────────────────────────────────────────────────────
//
// The control-plane node (selfKey) is always pruned by the host runner: a drained manager runs no
// swarm task, job or not, and it is the node whose disk filled (NP-R10). On swarm the scheduled
// prune adds one global-job excluding self, skipped when the self id is unknown (never
// unconstrained), and an emergency on another node is a replicated-job pinned to it. A job is
// create, `service logs --raw`, then `service rm` on every path, on a fresh 30 s ctx: the 10-minute
// one may be what expired (NP-R5). A job no ready, active node can run waits forever, so the global
// one needs such a node besides self and the pinned one needs its own; otherwise it is skipped.
//
// ── WHAT IS SAID ────────────────────────────────────────────────────────────────────────────────
//
// `node.pruned` once per node that reported, `0` included; a failed prune reports nothing and sends
// nothing. `node` is null on compose (NP-R15), where the script's `-` is selfKey. Memory only
// (invariant 10): /api/stats shows the newest per node, and a restart forgets them.
package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
)

const (
	pruneEmergencyEveryMs = 10 * 60_000
	pruneScheduledEveryMs = 6 * 3_600_000
	pruneTimeout          = 10 * time.Minute
)

type pruneRecord struct {
	At, Freed int64
	Reason    string
}

type prunedPayload struct {
	Node       *string `json:"node"`
	Hostname   string  `json:"hostname"`
	Reason     string  `json:"reason"`
	FreedBytes int64   `json:"freedBytes"`
}

// maybePrune starts at most one prune, from statsTick after pressure is judged.
func (s *Server) maybePrune(now int64) {
	if !s.pruneRunning.CompareAndSwap(false, true) {
		return
	}
	afterH := s.settings.ImagePruneAfterH()
	reason, keys := "", []string{}
	func() {
		s.statsMu.Lock()
		defer s.statsMu.Unlock()
		if s.lastScheduled == 0 {
			s.lastScheduled = now
		}
		for _, key := range s.stats.Keys() {
			kinds, _, ok := s.inPressureLocked(key, now)
			if !ok || !slices.Contains(kinds, nodestats.Disk) {
				continue
			}
			if last, seen := s.lastEmergency[key]; seen && now-last < pruneEmergencyEveryMs {
				continue
			}
			s.lastEmergency[key] = now
			keys = append(keys, key)
		}
		switch {
		case len(keys) > 0:
			reason = "pressure"
		case afterH > 0 && now-s.lastScheduled >= pruneScheduledEveryMs:
			s.lastScheduled = now
			reason = "scheduled"
		}
	}()
	if reason == "" {
		s.pruneRunning.Store(false)
		return
	}
	s.pruneStart(reason, keys)
}

// prune runs one prune: "scheduled" over every node, or "pressure" over keys. Its own goroutine in
// production; tests call it in place.
func (s *Server) prune(reason string, keys []string) {
	defer s.pruneRunning.Store(false)
	defer func() {
		if p := recover(); p != nil {
			s.opts.Log(fmt.Sprintf("prune: failed: %v", p))
		}
	}()
	ctx, cancel := context.WithTimeout(s.ctx, pruneTimeout)
	defer cancel()
	s.statsMu.Lock()
	self, onSwarm := s.selfKey, s.onSwarm
	takesTask, othersTake := map[string]bool{}, false // node id → a job task can start there
	for key, m := range s.nodeMeta {
		takesTask[key] = m.Status == "ready" && m.Availability == "active"
		othersTake = othersTake || (takesTask[key] && key != self)
	}
	s.statsMu.Unlock()

	var out strings.Builder
	local := func(script string) {
		out.WriteString(s.pruneRunner(ctx).Run(script, exec.RunOptions{Label: "image prune"}).Stdout + "\n")
	}
	if reason == "scheduled" {
		afterH := s.settings.ImagePruneAfterH()
		if afterH <= 0 {
			return
		}
		script := nodestats.PruneScript(afterH, true)
		local(script)
		switch {
		case !onSwarm:
		case self == "":
			s.opts.Log("prune: this node's swarm id is unknown; the other nodes wait for the next prune")
		case !othersTake:
			s.opts.Log("prune: no other node is ready and active; only this one is pruned")
		default:
			out.WriteString(s.pruneJob(ctx, script, "node.id!="+self, true))
		}
	} else {
		script := nodestats.PruneScript(0, false)
		for _, key := range keys {
			switch {
			case !onSwarm || key == self:
				local(script)
			case !takesTask[key]:
				s.opts.Log("prune: node " + key + " is not ready and active; not pruned")
			default:
				out.WriteString(s.pruneJob(ctx, script, "node.id=="+key, false))
			}
		}
	}
	s.recordPrune(reason, nodestats.ParsePrune(out.String()))
}

// pruneJob runs script as a swarm job and returns its logs, "" when the create failed. The rm runs
// on every path.
func (s *Server) pruneJob(ctx context.Context, script, constraint string, global bool) string {
	name := fmt.Sprintf("pstack-prune-%d", s.statsNow())
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.pruneRunner(rmCtx).Run("docker service rm "+name, exec.RunOptions{Label: "docker service rm " + name})
	}()
	r := s.pruneRunner(ctx)
	if !r.Run(nodestats.PruneJobCmd(name, script, constraint, global), exec.RunOptions{Label: "image prune " + name}).OK {
		s.opts.Log("prune: " + name + " failed")
		return ""
	}
	return r.Run("docker service logs --raw "+name, exec.RunOptions{Label: "docker service logs " + name}).Stdout
}

// recordPrune keeps each node's result and sends node.pruned for it, after statsMu (Go rule 14).
func (s *Server) recordPrune(reason string, got []nodestats.Reclaimed) {
	now := s.statsNow()
	sent := []prunedPayload{}
	func() {
		s.statsMu.Lock()
		defer s.statsMu.Unlock()
		for _, rc := range got {
			key := rc.Node
			if key == "" {
				key = s.selfKey
			}
			s.lastPrune[key] = pruneRecord{At: now, Freed: rc.Bytes, Reason: reason}
			hostname := s.nodeMeta[key].Hostname
			if e, ok := s.stats.Get(key); ok && hostname == "" {
				hostname = e.Batch.Node.Hostname
			}
			if hostname == "" && !s.onSwarm {
				hostname = key // the compose key is the hostname
			}
			var node *string
			if s.onSwarm && key != "" {
				node = &key
			}
			sent = append(sent, prunedPayload{Node: node, Hostname: hostname, Reason: reason, FreedBytes: rc.Bytes})
		}
	}()
	for _, p := range sent {
		s.bus.Emit("node.pruned", p)
	}
}

// pruneBody is a node's newest prune for /api/stats, nil when none. Caller holds statsMu.
func (s *Server) pruneBody(key string) any {
	p, ok := s.lastPrune[key]
	if !ok {
		return nil
	}
	return jsonx.Object{{K: "at", V: p.At}, {K: "freedBytes", V: p.Freed}, {K: "reason", V: p.Reason}}
}

// pruneTotals is the newest run's time and what it freed over every node: the records that share
// the newest At. Caller holds statsMu.
func (s *Server) pruneTotals() jsonx.Object {
	var lastAt, lastFreed *int64
	for _, p := range s.lastPrune {
		if lastAt == nil || p.At > *lastAt {
			at, freed := p.At, int64(0)
			lastAt, lastFreed = &at, &freed
		}
		if p.At == *lastAt {
			*lastFreed += p.Freed
		}
	}
	return jsonx.Object{{K: "lastAt", V: lastAt}, {K: "lastFreed", V: lastFreed}}
}
