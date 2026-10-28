// `GET /api/stats`: what every node's pstack-agent last reported, and the stacks using it.
// internal/nodestats reads the agent's lines and keeps the samples; this file owns the loop that
// feeds it, the lock around it, and the HTTP surface. A sibling of routes_signals.go, which stays
// free of capacity arithmetic.
//
// ── THE LOOP ASKS DOCKER, THE ROUTE DOES NOT ────────────────────────────────────────────────────
//
// statsTick runs `docker info` (and `node ls` on a manager) and the agent's logs commands, then
// records the newest complete batch per node under statsMu, then judges pressure (pressure.go),
// which emits its edges after the lock, then may start a prune (prune.go), then may sleep one stack
// (pressureSleep). The route reads only memory (invariant 10: none of this is a table, and a restart
// forgets it). Reads emit nothing.
//
// ── UNKNOWN IS null ─────────────────────────────────────────────────────────────────────────────
//
//   - A node with no batch in nodestats.StaleMs is stale: `at` and every reading are null, and
//     `stale` is true. Its `history` is kept: the sparkline shows where it was (invariant 11).
//   - A docker that does not answer changes nothing: the last tick's node list, self key and
//     orchestrator stay. An active manager whose `node ls` came back empty did not answer either;
//     its own node is always on that list.
//
// ── WHICH ROWS ──────────────────────────────────────────────────────────────────────────────────
//
// On swarm, every node `node ls` lists, by hostname, whether or not its agent reported. On a compose
// host, every node the store has seen (one). `node` is null on compose (NP-R15).
//
// `stacks` sums fresh batches only, by the stack label the agent read. `pstack-control` is a stack
// like any other: it is real use of the node. A container in no stack is left out.
package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// nodeMeta is what `node ls` says about a node. Role is "" on compose, null in the body. Status and
// Availability are lowercased (`ready`, `active`); prune.go reads both.
type nodeMeta struct{ Hostname, Role, Status, Availability string }

// statsTick is one read: docker first, then the store under statsMu. Tests drive it directly.
func (s *Server) statsTick() {
	info := swarm.SwarmInfo(s.host)
	// Silence: unreachable, or a manager whose node list failed.
	answered := info.Reachable && !(info.Active && len(info.Nodes) == 0)

	s.statsMu.Lock()
	onSwarm := s.onSwarm
	s.statsMu.Unlock()
	if answered {
		onSwarm = info.Active
	}
	var logs strings.Builder
	for _, cmd := range nodestats.AgentLogsCmds(onSwarm) {
		if res := s.host.Run(cmd, exec.RunOptions{Label: "docker logs " + nodestats.AgentName}); res.OK {
			logs.WriteString(res.Stdout)
			logs.WriteString("\n")
		}
	}
	batches := nodestats.Parse(logs.String())
	now := s.statsNow()

	func() {
		s.statsMu.Lock()
		defer s.statsMu.Unlock()
		if answered {
			s.onSwarm = info.Active
			meta := map[string]nodeMeta{}
			if info.Active {
				for _, n := range info.Nodes {
					meta[n.ID] = nodeMeta{Hostname: n.Hostname, Role: n.Role, Status: n.Status, Availability: n.Availability}
				}
				if info.NodeID != nil {
					s.selfKey = *info.NodeID
				}
			}
			s.nodeMeta = meta
		}
		if !s.onSwarm && len(batches) == 1 {
			s.selfKey = batches[0].Key
		}
		s.stats.Record(now, batches)
	}()
	s.evaluatePressure(now)
	s.maybePrune(now)
	s.pressureSleep(now)
}

// statsLoop ticks until the server stops. Started only when the interval is above zero. A panic
// costs one tick, never the loop.
func (s *Server) statsLoop() {
	t := time.NewTicker(time.Duration(s.opts.StatsTickMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			func() {
				defer func() {
					if p := recover(); p != nil {
						s.opts.Log(fmt.Sprintf("stats: tick failed: %v", p))
					}
				}()
				s.statsTick()
			}()
		}
	}
}

type statsRow struct {
	key   string
	meta  nodeMeta
	entry nodestats.Entry
	fresh bool
	kinds []nodestats.Kind
	prune any
}

type stackSum struct {
	name       string
	cpuPct     float64
	mem        int64
	containers int
	nodes      []string
}

// statsBody is the response. The route and later tests share it.
func (s *Server) statsBody(now int64) jsonx.Object {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	rows := []statsRow{}
	if s.onSwarm {
		for key, m := range s.nodeMeta {
			rows = append(rows, statsRow{key: key, meta: m})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].meta.Hostname < rows[j].meta.Hostname })
	} else {
		for _, key := range s.stats.Keys() {
			rows = append(rows, statsRow{key: key})
		}
	}

	agent := false
	nodes := []jsonx.Object{}
	sums := []*stackSum{}
	byStack := map[string]*stackSum{}
	for i := range rows {
		r := &rows[i]
		r.entry, _ = s.stats.Get(r.key)
		_, r.fresh = s.stats.Fresh(r.key, now)
		r.kinds = s.pressureKinds(r.key, r.fresh)
		r.prune = s.pruneBody(r.key)
		agent = agent || r.fresh
		nodes = append(nodes, statsNodeRow(*r, s.onSwarm))
		if !r.fresh {
			continue
		}
		host := r.entry.Batch.Node.Hostname
		for _, c := range r.entry.Batch.Ctrs {
			if c.Stack == "" {
				continue
			}
			sum := byStack[c.Stack]
			if sum == nil {
				sum = &stackSum{name: c.Stack, nodes: []string{}}
				byStack[c.Stack] = sum
				sums = append(sums, sum)
			}
			sum.cpuPct += c.CPUPct
			sum.mem += c.Mem
			sum.containers++
			if len(sum.nodes) == 0 || sum.nodes[len(sum.nodes)-1] != host {
				sum.nodes = append(sum.nodes, host)
			}
		}
	}
	sort.SliceStable(sums, func(i, j int) bool { return sums[i].name < sums[j].name })
	sort.SliceStable(sums, func(i, j int) bool { return sums[i].mem > sums[j].mem })
	stacks := []jsonx.Object{}
	for _, sum := range sums {
		sort.Strings(sum.nodes)
		stacks = append(stacks, jsonx.Object{
			{K: "stack", V: sum.name},
			{K: "cpuPct", V: sum.cpuPct},
			{K: "mem", V: sum.mem},
			{K: "containers", V: sum.containers},
			{K: "nodes", V: sum.nodes},
		})
	}

	return jsonx.Object{
		{K: "v", V: 1},
		{K: "at", V: now},
		{K: "agent", V: agent},
		{K: "nodes", V: nodes},
		{K: "stacks", V: stacks},
		{K: "prune", V: s.pruneTotals()},
	}
}

// statsNodeRow is one node. Readings are pointers: null unless the batch is fresh.
func statsNodeRow(r statsRow, onSwarm bool) jsonx.Object {
	b := r.entry.Batch.Node
	var node, role *string
	hostname := r.meta.Hostname
	if onSwarm {
		node, role = &r.key, &r.meta.Role
	} else {
		hostname = b.Hostname
		if b.Node != "" {
			node = &b.Node
		}
	}
	var at *int64
	var cores *int
	var load1, load5 *float64
	var memTotal, memAvailable, diskTotal, diskFree *int64
	if r.fresh {
		at, cores, load1, load5 = &r.entry.At, &b.Cores, &b.Load1, &b.Load5
		memTotal, memAvailable, diskTotal, diskFree = &b.MemTotal, &b.MemAvailable, &b.DiskTotal, &b.DiskFree
	}
	history := r.entry.History
	if history == nil {
		history = []nodestats.Sample{}
	}
	return jsonx.Object{
		{K: "node", V: node},
		{K: "hostname", V: hostname},
		{K: "role", V: role},
		{K: "at", V: at},
		{K: "stale", V: !r.fresh},
		{K: "cores", V: cores},
		{K: "load1", V: load1},
		{K: "load5", V: load5},
		{K: "memTotal", V: memTotal},
		{K: "memAvailable", V: memAvailable},
		{K: "diskTotal", V: diskTotal},
		{K: "diskFree", V: diskFree},
		{K: "pressure", V: r.kinds},
		{K: "prune", V: r.prune},
		{K: "history", V: history},
	}
}

func (s *Server) statsGet(w http.ResponseWriter) error {
	writeJSON(w, 200, s.statsBody(s.statsNow()))
	return nil
}
