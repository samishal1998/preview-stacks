// `GET /api/signals`: what the swarm looks like right now, for whatever adds and removes worker
// machines. internal/signals asks docker; this file is the HTTP surface and the one piece of memory
// the feature has.
//
// ── THE READ CHANGES NOTHING ────────────────────────────────────────────────────────────────────
//
// It emits no event and starts no work (AGENTS.md's rule for a new route). The ticker in
// server.go is the only thing that raises and clears. A consumer can poll this as often as it
// likes; the cost is three docker commands.
//
// ── emptySince IS THE ONLY STATE ────────────────────────────────────────────────────────────────
//
// A node with no tasks gets a timestamp the first time it is seen that way, and loses it the moment
// it picks up work. It lives in memory (invariant 10): a restart forgets, so the clock starts again
// from boot. That errs the safe way — a machine looks busier for longer than it is, never emptier
// sooner. Whoever reads this decides how long "empty" has to last before acting; pstack has no
// opinion, because the policy belongs with the thing that pays for the machines.
package api

import (
	"net/http"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/signals"
)

// signalsView is one `Look`, with the empty clocks applied. The ticker and the route share it so
// neither can describe the cluster differently.
func (s *Server) signalsView() (signals.View, map[string]int64) {
	view := signals.Look(s.host)
	now := time.Now().UnixMilli()

	s.emptyMu.Lock()
	defer s.emptyMu.Unlock()
	seen := map[string]bool{}
	for _, n := range view.Nodes {
		seen[n.ID] = true
		// The manager is never a machine to take away, so it never gets a clock.
		if n.Tasks > 0 || n.Role != "worker" {
			delete(s.emptySince, n.ID)
			continue
		}
		if _, ok := s.emptySince[n.ID]; !ok {
			s.emptySince[n.ID] = now
		}
	}
	for id := range s.emptySince {
		if !seen[id] {
			delete(s.emptySince, id) // the machine is gone; so is its clock
		}
	}
	out := make(map[string]int64, len(s.emptySince))
	for id, at := range s.emptySince {
		out[id] = at
	}
	return view, out
}

// signalsBody is the response, built once so the route and its test read the same shape.
func signalsBody(view signals.View, empty map[string]int64) jsonx.Object {
	nodes := []jsonx.Object{}
	for _, n := range view.Nodes {
		row := jsonx.Object{
			{K: "id", V: n.ID},
			{K: "hostname", V: n.Hostname},
			{K: "role", V: n.Role},
			{K: "state", V: n.State},
			{K: "availability", V: n.Availability},
			{K: "tasks", V: n.Tasks},
		}
		// emptySince is null, not absent: a consumer keys on the field being there.
		var since *int64
		if at, ok := empty[n.ID]; ok {
			at := at
			since = &at
		}
		row = append(row, jsonx.KV{K: "emptySince", V: since})
		nodes = append(nodes, row)
	}
	stuck := []jsonx.Object{}
	for _, t := range view.Stuck {
		stuck = append(stuck, jsonx.Object{
			{K: "task", V: t.Task},
			{K: "service", V: t.Service},
			{K: "stack", V: t.Stack},
			{K: "reason", V: t.Reason},
		})
	}
	return jsonx.Object{
		{K: "v", V: 1},
		{K: "swarm", V: view.Swarm},
		{K: "reachable", V: view.Reachable},
		{K: "at", V: time.Now().UnixMilli()},
		{K: "nodes", V: nodes},
		{K: "stuck", V: stuck},
	}
}

func (s *Server) signalsGet(w http.ResponseWriter) error {
	view, empty := s.signalsView()
	writeJSON(w, 200, signalsBody(view, empty))
	return nil
}
