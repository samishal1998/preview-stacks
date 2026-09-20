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

// ── telling people, instead of being asked ──────────────────────────────────────────────────────
//
// A ticker compares what is true now with what was true last time and sends the difference:
// `signal.raised` when something appears, `signal.cleared` when it goes. Nothing repeats while it
// stays true — a notifier subscribed to everything would otherwise post the same line all day, and
// the poll route above is there for anyone who wants the current picture.
//
// Ids are `stuck/<service>` and `empty/<node id>`, which are stable for as long as the thing is
// true. A restart forgets what was sent and raises everything still true, so a listener that missed
// a delivery catches up; acting twice on one id has to be harmless on the receiving side, which is
// the usual webhook contract.
//
// A docker that does not answer changes NOTHING: silence is not "the problem went away".

type signalPayload struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Node     string `json:"node,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Task     string `json:"task,omitempty"`
	Service  string `json:"service,omitempty"`
	Stack    string `json:"stack,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Since    int64  `json:"since"`
}

// signalsNow is every signal that is true in this view, by id.
func signalsNow(view signals.View, empty map[string]int64) map[string]signalPayload {
	out := map[string]signalPayload{}
	for _, t := range view.Stuck {
		id := "stuck/" + t.Service
		out[id] = signalPayload{
			ID: id, Type: "stuck", Task: t.Task, Service: t.Service, Stack: t.Stack,
			Reason: t.Reason, Since: time.Now().UnixMilli(),
		}
	}
	for _, n := range view.Nodes {
		at, ok := empty[n.ID]
		if !ok {
			continue
		}
		id := "empty/" + n.ID
		out[id] = signalPayload{ID: id, Type: "empty", Node: n.ID, Hostname: n.Hostname, Since: at}
	}
	return out
}

// signalsTick is one comparison. Public enough for a test to drive it without waiting for a timer.
func (s *Server) signalsTick() {
	view, empty := s.signalsView()
	if !view.Swarm || !view.Reachable {
		return
	}
	now := signalsNow(view, empty)

	// Work out the difference under the lock, then emit outside it: events.Emit calls its listeners
	// there and then, and one of them writes to the database.
	var raised, cleared []signalPayload
	s.emptyMu.Lock()
	for id, p := range now {
		if _, had := s.signalsSent[id]; !had {
			raised = append(raised, p)
		}
		s.signalsSent[id] = p
	}
	for id, p := range s.signalsSent {
		if _, still := now[id]; !still {
			cleared = append(cleared, p)
			delete(s.signalsSent, id)
		}
	}
	s.emptyMu.Unlock()

	for _, p := range raised {
		s.bus.Emit("signal.raised", p)
	}
	for _, p := range cleared {
		s.bus.Emit("signal.cleared", p)
	}
}

// signalsLoop ticks until the server stops. Started only when the interval is above zero.
func (s *Server) signalsLoop() {
	t := time.NewTicker(time.Duration(s.opts.SignalsTickMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.signalsTick()
		}
	}
}
