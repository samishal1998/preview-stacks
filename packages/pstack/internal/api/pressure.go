// Node pressure, held in memory: which kinds each node is in, and the `node.pressure` /
// `node.recovered` events on the edges. nodestats/pressure.go judges a reading; this file keeps the
// state between ticks and tells the world when it changes.
//
// ── EDGES ONLY ──────────────────────────────────────────────────────────────────────────────────
//
// `node.pressure` fires when a node's kinds go from none to some, `node.recovered` from some to none.
// A change of kinds inside pressure fires nothing: `/api/stats` carries the current picture. The
// state is memory only (invariant 10), so a restart raises again whatever is still true; receivers
// dedupe on `id`, as they do for signals.
//
// ── UNKNOWN IS NEVER PRESSURE ───────────────────────────────────────────────────────────────────
//
// Only a fresh, readable batch is judged (invariant 11). A stale node's entry is left as it was and
// nothing fires: it did not recover, it went quiet. inPressure says false for it, so no guard acts.
//
// ── NO ROOM, NO NEW WORK ────────────────────────────────────────────────────────────────────────
//
// capacityRefusal is the guard startLifecycle runs before an up or a wake. On swarm it refuses when
// every active node is fresh and in pressure: which node a task lands on cannot be known. With no
// active node, or no data, it allows (NP-R18): unknown is never pressure. A compose deployment, on
// either host, is refused when the self node is in pressure.
//
// ── SLEEP THE LEAST RECENTLY USED ───────────────────────────────────────────────────────────────
//
// pressureSleep is the memory and cpu answer. Each stats tick it sleeps ONE stack in total, across
// every node (NP-R21): of the stacks with a container on a node in memory or cpu pressure, the one
// whose scheduler.Recency is oldest (NP-R20), ties by stack name. Eligible means awake, not `kind:
// shared`, not busy, and unused for `pressure_sleep_idle_min`; 0 turns it off. It does nothing when
// the traffic meter cannot read Traefik: silence is not idle. `st.Sleep` is not required, since a
// wake runs `up` off the record. The reason is `<hostname> <Describe>` of the stack's first node in
// key order, as Describe words it (NP-R22): `worker-2 memory 6% free ≤ 10%`.
//
// ── THE id ──────────────────────────────────────────────────────────────────────────────────────
//
// `pressure/<key>` (NP-R17): the swarm node id on swarm, as the spec spells it, and the hostname on a
// compose host, where the spec's `pressure/<node>` would read `pressure/`. `node` is null there
// (NP-R15).
package api

import (
	"math"
	"sort"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/nodestats"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

type pressureThresholds struct {
	Disk   int     `json:"disk"`
	Memory int     `json:"memory"`
	CPU    float64 `json:"cpu"`
}

// pressurePayload is both events' data. Readings are rounded to one decimal; kinds is [] on
// node.recovered.
type pressurePayload struct {
	ID         string             `json:"id"`
	Node       *string            `json:"node"`
	Hostname   string             `json:"hostname"`
	Kinds      []nodestats.Kind   `json:"kinds"`
	Disk       float64            `json:"disk"`
	Memory     float64            `json:"memory"`
	CPU        float64            `json:"cpu"`
	Thresholds pressureThresholds `json:"thresholds"`
}

type pressureEdge struct {
	event string
	data  pressurePayload
}

// evaluatePressure judges every fresh node against the settings as they are now, then emits the
// edges outside statsMu (Go rule 14).
func (s *Server) evaluatePressure(now int64) {
	t := nodestats.Thresholds{
		DiskPct: s.settings.PressureDiskPct(),
		MemPct:  s.settings.PressureMemPct(),
		CPULoad: s.settings.PressureCPULoad(),
	}
	edges := []pressureEdge{}
	func() {
		s.statsMu.Lock()
		defer s.statsMu.Unlock()
		for _, key := range s.stats.Keys() {
			b, fresh := s.stats.Fresh(key, now)
			if !fresh {
				continue
			}
			r, ok := nodestats.Read(b.Node)
			if !ok {
				continue
			}
			prev := s.pressure[key]
			kinds := nodestats.Evaluate(prev, r, t)
			if len(kinds) == 0 {
				delete(s.pressure, key)
			} else {
				s.pressure[key] = kinds
			}
			event := ""
			switch {
			case len(prev) == 0 && len(kinds) > 0:
				event = "node.pressure"
			case len(prev) > 0 && len(kinds) == 0:
				event = "node.recovered"
			default:
				continue
			}
			hostname := s.hostnameLocked(key, b)
			var node *string
			if b.Node.Node != "" {
				node = &b.Node.Node
			}
			edges = append(edges, pressureEdge{event, pressurePayload{
				ID: "pressure/" + key, Node: node, Hostname: hostname, Kinds: kinds,
				Disk: round1(r.DiskUsedPct), Memory: round1(r.MemFreePct), CPU: round1(r.LoadPerCore),
				Thresholds: pressureThresholds{Disk: t.DiskPct, Memory: t.MemPct, CPU: t.CPULoad},
			}})
		}
		// The store never forgets a key today, so this is for the day it does.
		for key := range s.pressure {
			if _, listed := s.nodeMeta[key]; !listed {
				if _, seen := s.stats.Get(key); !seen {
					delete(s.pressure, key)
				}
			}
		}
	}()
	for _, e := range edges {
		s.bus.Emit(e.event, e.data)
	}
}

// inPressure is the node's kinds and reading, for the guards. False unless the node is fresh,
// readable and in pressure: unknown is never pressure.
func (s *Server) inPressure(key string, now int64) ([]nodestats.Kind, nodestats.Reading, bool) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.inPressureLocked(key, now)
}

// inPressureLocked is inPressure for a caller that holds statsMu.
func (s *Server) inPressureLocked(key string, now int64) ([]nodestats.Kind, nodestats.Reading, bool) {
	b, fresh := s.stats.Fresh(key, now)
	if !fresh {
		return []nodestats.Kind{}, nodestats.Reading{}, false
	}
	r, ok := nodestats.Read(b.Node)
	kinds := append([]nodestats.Kind{}, s.pressure[key]...)
	return kinds, r, ok && len(kinds) > 0
}

// capacityRefusal says why an up or wake of a deployment on o is refused: `worker-2: disk 94% ≥ 90%`,
// the first node by hostname and its first kind.
func (s *Server) capacityRefusal(o spec.Orchestrator, now int64) (why string, refused bool) {
	t := nodestats.Thresholds{
		DiskPct: s.settings.PressureDiskPct(),
		MemPct:  s.settings.PressureMemPct(),
		CPULoad: s.settings.PressureCPULoad(),
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	keys := []string{s.selfKey}
	if o == spec.Swarm {
		keys = []string{}
		for key, m := range s.nodeMeta {
			if m.Availability == "active" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		sort.SliceStable(keys, func(i, j int) bool { return s.nodeMeta[keys[i]].Hostname < s.nodeMeta[keys[j]].Hostname })
	}
	first := ""
	for _, key := range keys {
		kinds, r, in := s.inPressureLocked(key, now)
		if !in {
			return "", false
		}
		if first == "" {
			b, _ := s.stats.Fresh(key, now)
			first = s.hostnameLocked(key, b) + ": " + nodestats.Describe(kinds[0], r, t)
		}
	}
	return first, first != ""
}

// pressureSleep sleeps at most one stack: the least recently used on a node in memory or cpu
// pressure. The nodes are read under statsMu; docker and startLifecycle run after it.
func (s *Server) pressureSleep(now int64) {
	idleMin := s.settings.PressureSleepIdleMin()
	if idleMin == 0 || !s.meter.OK() {
		return
	}
	t := nodestats.Thresholds{
		DiskPct: s.settings.PressureDiskPct(),
		MemPct:  s.settings.PressureMemPct(),
		CPULoad: s.settings.PressureCPULoad(),
	}
	reasons := map[string]string{} // stack → why, from its first node in key order
	func() {
		s.statsMu.Lock()
		defer s.statsMu.Unlock()
		for _, key := range s.stats.Keys() {
			kinds, r, in := s.inPressureLocked(key, now)
			if !in {
				continue
			}
			var kind nodestats.Kind
			for _, k := range kinds {
				if k == nodestats.Memory || k == nodestats.CPU {
					kind = k
					break
				}
			}
			if kind == "" {
				continue
			}
			b, _ := s.stats.Fresh(key, now)
			why := s.hostnameLocked(key, b) + " " + nodestats.Describe(kind, r, t)
			for _, c := range b.Ctrs {
				if _, seen := reasons[c.Stack]; c.Stack != "" && !seen {
					reasons[c.Stack] = why
				}
			}
		}
	}()
	if len(reasons) == 0 {
		return
	}
	metas, err := s.registry.List()
	if err != nil {
		return
	}
	var best struct {
		id     string
		st     *spec.Stack
		recent int64
	}
	for _, m := range metas {
		if m.Sleep != nil || m.Kind == spec.Shared {
			continue
		}
		st, err := s.resolveDep(m.ID, nil)
		if err != nil || st.Compose == nil || reasons[st.Stack] == "" || s.jobs.IsBusy(st.Stack) {
			continue
		}
		rt, err := s.runtimeView(st)
		if err != nil {
			continue
		}
		recent, ok := scheduler.Recency(s.scheduler.BootAt, rt, s.meter)
		if !ok || now-recent < int64(idleMin)*60_000 {
			continue
		}
		if best.st == nil || recent < best.recent || recent == best.recent && st.Stack < best.st.Stack {
			best.id, best.st, best.recent = m.ID, st, recent
		}
	}
	if best.st == nil {
		return
	}
	dep, err := s.registry.Get(best.id)
	if err != nil || dep == nil {
		return
	}
	s.startLifecycle(best.id, dep, best.st, jobs.Sleep, lifecycleOptions{By: "pressure", Reason: reasons[best.st.Stack], OnlyIfIdle: true})
}

// hostnameLocked is the node's name for people: `node ls`'s on swarm, else the agent's. Caller holds
// statsMu.
func (s *Server) hostnameLocked(key string, b nodestats.Batch) string {
	if m, ok := s.nodeMeta[key]; ok && m.Hostname != "" {
		return m.Hostname
	}
	return b.Node.Hostname
}

// pressureKinds is the node's kinds for /api/stats: [] unless fresh. Caller holds statsMu.
func (s *Server) pressureKinds(key string, fresh bool) []nodestats.Kind {
	if !fresh {
		return []nodestats.Kind{}
	}
	return append([]nodestats.Kind{}, s.pressure[key]...)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
