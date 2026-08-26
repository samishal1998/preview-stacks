package api

import (
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
)

func route(router, service, target string) inspect.RouteInfo {
	r := inspect.RouteInfo{Router: router}
	if service != "" {
		s := service
		r.Service = &s
	}
	if target != "" {
		t := target
		r.Target = &t
	}
	return r
}

// The pick has to be STABLE. The runtime's route order comes from docker's inspect output, which is
// not a promise, so a probe that took "the first one" as docker listed it would answer about a
// different container between two polls of the same stack — and a CI loop that flips between two
// services is worse than one that is consistently wrong.
func TestProbeTargetIsDeterministic(t *testing.T) {
	// negative control: drop the sort in probeTarget — this returns "10.0.0.2:8080" (docker's
	// order) and the reversed case below returns the other one, so the two disagree.
	forward := inspect.Runtime{Routes: []inspect.RouteInfo{
		route("zeta", "zeta", "10.0.0.2:8080"),
		route("alpha", "alpha", "10.0.0.1:80"),
	}}
	reversed := inspect.Runtime{Routes: []inspect.RouteInfo{
		route("alpha", "alpha", "10.0.0.1:80"),
		route("zeta", "zeta", "10.0.0.2:8080"),
	}}
	if got := probeTarget(forward, ""); got != "10.0.0.1:80" {
		t.Fatalf("forward: got %q, want the alphabetically first router's target", got)
	}
	if a, b := probeTarget(forward, ""), probeTarget(reversed, ""); a != b {
		t.Fatalf("order of docker's output changed the answer: %q vs %q", a, b)
	}
}

// A router without a target is not an answer. `TargetReason` says why one is missing — no port, not
// on preview-ingress, a task on a node this host cannot address — and none of those are dialable.
func TestProbeTargetSkipsRoutersWithNothingToDial(t *testing.T) {
	// negative control: return `*ro.Target` without the nil check — this panics rather than
	// answering `no-target`, on a route shape the runtime page renders every day.
	rt := inspect.Runtime{Routes: []inspect.RouteInfo{
		route("aaa", "aaa", ""),
		route("bbb", "bbb", "10.0.0.9:3000"),
	}}
	if got := probeTarget(rt, ""); got != "10.0.0.9:3000" {
		t.Fatalf("got %q, want the one router that has an address", got)
	}
	if got := probeTarget(inspect.Runtime{Routes: []inspect.RouteInfo{route("aaa", "aaa", "")}}, ""); got != "" {
		t.Fatalf("got %q, want empty so the route answers no-target", got)
	}
	if got := probeTarget(inspect.Runtime{}, ""); got != "" {
		t.Fatalf("no routes at all: got %q, want empty", got)
	}
}

// `?service=` names WHICH one, for a stack that publishes more than one. Case-folded because the
// caller is typing it and docker service names are already lowercase by convention.
func TestProbeTargetFiltersByService(t *testing.T) {
	// negative control: ignore the filter and always take the first — `api` below answers with the
	// web container's address, so a CI job polls the wrong service and never learns it.
	rt := inspect.Runtime{Routes: []inspect.RouteInfo{
		route("api-r", "api", "10.0.0.2:8080"),
		route("web-r", "web", "10.0.0.1:80"),
	}}
	if got := probeTarget(rt, "api"); got != "10.0.0.2:8080" {
		t.Fatalf("got %q, want the api service's address", got)
	}
	if got := probeTarget(rt, "API"); got != "10.0.0.2:8080" {
		t.Fatalf("case: got %q, want the api service's address", got)
	}
	// A name nothing publishes is `no-target`, not "the first one anyway": answering about a
	// service the caller did not ask about is the failure this filter exists to prevent.
	if got := probeTarget(rt, "worker"); got != "" {
		t.Fatalf("unknown service: got %q, want empty", got)
	}
	// A router with a target but no service is unreachable through the filter, and reachable
	// without it.
	only := inspect.Runtime{Routes: []inspect.RouteInfo{route("r", "", "10.0.0.3:80")}}
	if got := probeTarget(only, ""); got != "10.0.0.3:80" {
		t.Fatalf("unfiltered: got %q", got)
	}
	if got := probeTarget(only, "anything"); got != "" {
		t.Fatalf("filtered on a router with no service name: got %q, want empty", got)
	}
}
