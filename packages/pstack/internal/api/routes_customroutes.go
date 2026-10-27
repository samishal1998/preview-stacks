// `/api/custom-routes` — a stable alias for a stack. Route `checkout` assigned to the deployment of
// stack `myapp-pr-123` makes `web-myapp-pr-123.<domain>` also answer on `web-checkout.<domain>`,
// with no redeploy (internal/routing/customroutes.go renders the routers).
//
// The custom_routes table is the source of truth; pstack-routes.yml is derived from it. Every write
// that changes an assignment re-renders the file and rebuilds the sleep index.
//
// THE WRITE SHAPE, the same as removeDeployment's: writeMu, then every check (they resolve specs,
// which reads host vars from the store — never inside store.Tx, rule 16), then the Tx holding store
// calls only, then the render, then unlock, then Emit. writeMu makes the check-then-act safe: every
// write to the table runs under it.
//
// Developer, like the deployments they point at. Share principals never reach this (invariant 16).
package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// routeNameRe is a DNS label of at most 40 characters.
var routeNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// reservedRouteNames are the labels pstack owns on every domain (invariant 22).
var reservedRouteNames = map[string]bool{"control": true, "api": true, "loki": true, "grafana": true}

func (s *Server) customRoutesRoutes(w http.ResponseWriter, r *http.Request, name string, assignment bool) error {
	switch {
	case !assignment && r.Method == http.MethodDelete:
		return s.customRouteDrop(w, name, "deleted")
	case assignment && r.Method == http.MethodPut:
		return s.customRouteAssign(w, r, name)
	case assignment && r.Method == http.MethodDelete:
		return s.customRouteDrop(w, name, "unassigned")
	case assignment:
		writeError(w, 405, "use PUT or DELETE")
	default:
		writeError(w, 405, "use DELETE")
	}
	return nil
}

// resolved is one deployment as the routes see it: its resolved spec and directory.
type resolved struct {
	st  *spec.Stack
	dir string
}

// resolveForRoute resolves deployment id; an error when it is gone or does not resolve.
func (s *Server) resolveForRoute(id string) (resolved, error) {
	dep, err := s.registry.Get(id)
	if err != nil {
		return resolved{}, err
	}
	if dep == nil {
		return resolved{}, errors.New("no such deployment: " + id)
	}
	st, err := s.resolveDep(id, nil)
	if err != nil {
		return resolved{}, err
	}
	return resolved{st, dep.Dir}, nil
}

// routeTarget is what an assigned row renders.
func (s *Server) routeTarget(row store.CustomRoute) (routing.AliasTarget, error) {
	d, err := s.resolveForRoute(row.DeploymentID)
	if err != nil {
		return routing.AliasTarget{}, err
	}
	t, _, err := aliasTarget(d.st, d.dir, row.Name)
	return t, err
}

// stackOf is a deployment's stack name, or nil when it does not resolve.
func (s *Server) stackOf(id string) any {
	if d, err := s.resolveForRoute(id); err == nil {
		return d.st.Stack
	}
	return nil
}

// routeJSON is the API's route: `{ name, deployment, stack, hosts[], createdAt, updatedAt }`, with
// deployment and stack null when unassigned. hosts is what it serves now; empty when its
// deployment's compose cannot be read.
func (s *Server) routeJSON(row store.CustomRoute) jsonx.Object {
	var dep, stack any
	hosts := []string{}
	if row.DeploymentID != "" {
		dep = row.DeploymentID
		if d, err := s.resolveForRoute(row.DeploymentID); err == nil {
			stack = d.st.Stack
			if t, _, err := aliasTarget(d.st, d.dir, row.Name); err == nil {
				hosts = routing.AliasHosts(t)
			}
		}
	}
	return jsonx.O("name", row.Name, "deployment", dep, "stack", stack, "hosts", hosts,
		"createdAt", row.CreatedAt, "updatedAt", row.UpdatedAt)
}

// renderCustomRoutes rewrites pstack-routes.yml from the table, then rebuilds the sleep index. It
// takes no lock: the caller holds writeMu.
//
// Total: a route whose deployment is gone or whose compose cannot be read renders nothing and is
// logged; the rest of the file still renders.
func (s *Server) renderCustomRoutes() error {
	rows, err := store.ListCustomRoutes(s.store.DB)
	if err != nil {
		return err
	}
	targets := []routing.AliasTarget{}
	wake := map[string]scheduler.SleepEntry{}
	for _, row := range rows {
		if row.DeploymentID == "" {
			continue
		}
		t, err := s.routeTarget(row)
		if err != nil {
			s.opts.Log("custom routes: " + row.Name + " renders nothing: " + err.Error())
			continue
		}
		targets = append(targets, t)
		e := wake[row.DeploymentID]
		e.Hosts, e.Rules = appendWake(e.Hosts, e.Rules, t)
		wake[row.DeploymentID] = e
	}
	s.wakeMu.Lock()
	s.routeWake = wake
	s.wakeMu.Unlock()
	content := ""
	if len(targets) > 0 {
		// challenge() asks docker, so only when there is something to render.
		if content, err = routing.RenderCustomRoutes(targets, string(s.challenge())); err != nil {
			s.reindex()
			return err
		}
	}
	err = s.routing.SetCustomRoutes(content)
	s.reindex()
	return err
}

// appendWake adds what route t serves to a sleep entry's hosts and rules, in the shape a sleep
// record keeps them: plain hosts, and each `-wild` router's HostRegexp pattern.
func appendWake(hosts, rules []string, t routing.AliasTarget) ([]string, []string) {
	all := []string{}
	for _, h := range routing.AliasHosts(t) {
		if !strings.HasPrefix(h, "*.") {
			all = append(all, h)
		}
	}
	for _, rule := range routing.AliasWildcardRules(t) {
		all = append(all, inspect.HostsFromRule(rule)...)
	}
	h, r := scheduler.SplitHosts(all)
	return append(hosts, h...), append(rules, r...)
}

// reconcileCustomRoutes re-renders at boot, so the table and the file heal. Nothing assigned returns
// before any docker call or file write. Total: a failure is logged.
func (s *Server) reconcileCustomRoutes() {
	if s.store == nil {
		return
	}
	rows, err := store.ListCustomRoutes(s.store.DB)
	if err != nil {
		s.opts.Log("custom routes: could not read the table: " + err.Error())
		return
	}
	if slices.ContainsFunc(rows, func(r store.CustomRoute) bool { return r.DeploymentID != "" }) {
		s.rendered()
	}
}

// rerenderRoutesOf re-renders after deployment id deployed, when a route is assigned to it: its
// services may have changed. Nothing assigned to it is nothing to do.
func (s *Server) rerenderRoutesOf(id string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := store.ListCustomRoutes(s.store.DB)
	if err != nil {
		s.opts.Log("custom routes: could not read the table: " + err.Error())
		return
	}
	if slices.ContainsFunc(rows, func(r store.CustomRoute) bool { return r.DeploymentID == id }) {
		s.rendered()
	}
}

// rendered re-renders after a committed write. A failure is logged, not answered: the table holds
// the change and boot re-renders it, while an error status would invite a retry the table refuses.
func (s *Server) rendered() {
	if err := s.renderCustomRoutes(); err != nil {
		s.opts.Log("custom routes: could not write " + routing.CustomRoutesYAML + ": " + err.Error())
	}
}

// assignConflict answers 409 when route name on deployment d would serve a host something else
// serves, or render a router name another route renders. Plain hosts and `*.` wildcards compare as
// separate strings. Taken: every resolvable deployment's routed hosts (explicit ones included),
// every other assigned route's alias hosts and routers, and the live label routers when docker
// answers (hand-labelled hosts). true = answered.
func (s *Server) assignConflict(w http.ResponseWriter, name string, d resolved) (bool, error) {
	t, _, err := aliasTarget(d.st, d.dir, name)
	if err != nil {
		return false, nil // stores the intent; the render logs why it serves nothing
	}
	taken := map[string]string{} // host → who serves it
	metas, err := s.registry.List()
	if err != nil {
		return false, err
	}
	for _, m := range metas {
		o, err := s.resolveForRoute(m.ID)
		if err != nil {
			continue
		}
		if _, hosts, err := aliasTarget(o.st, o.dir, name); err == nil {
			for _, h := range hosts {
				taken[h] = o.st.Stack
			}
		}
	}
	rows, err := store.ListCustomRoutes(s.store.DB)
	if err != nil {
		return false, err
	}
	routers := map[string]string{} // router → route
	for _, row := range rows {
		if row.Name == name || row.DeploymentID == "" {
			continue
		}
		ot, err := s.routeTarget(row)
		if err != nil {
			continue
		}
		for _, h := range routing.AliasHosts(ot) {
			taken[h] = "route " + row.Name
		}
		for _, rt := range routing.AliasRouters(ot) {
			routers[rt] = row.Name
		}
	}
	if live := inspect.AllTraefikRouters(s.host); live.Reachable {
		for _, rt := range live.Routes {
			for _, h := range rt.Hosts {
				if _, ok := taken[h]; !ok {
					taken[h] = "router " + rt.Router
				}
			}
		}
	}
	for _, h := range routing.AliasHosts(t) {
		if who, ok := taken[h]; ok {
			writeJSON(w, 409, jsonx.O("error", h+" is served by "+who, "host", h))
			return true, nil
		}
	}
	for _, rt := range routing.AliasRouters(t) {
		if other, ok := routers[rt]; ok {
			writeJSON(w, 409, jsonx.O("error", "router "+rt+" is taken by route "+other, "router", rt))
			return true, nil
		}
	}
	return false, nil
}

// deploymentOr404 resolves the deployment a body names. (resolved{}, false) = answered. A
// deployment that exists but does not resolve is still assignable: it stores the intent.
func (s *Server) deploymentOr404(w http.ResponseWriter, id string) (resolved, bool, error) {
	dep, err := s.registry.Get(id)
	if err != nil {
		return resolved{}, false, err
	}
	if dep == nil {
		writeError(w, 404, "no such deployment: "+id)
		return resolved{}, false, nil
	}
	st, err := s.resolveDep(id, nil)
	if err != nil {
		return resolved{dir: dep.Dir}, true, nil
	}
	return resolved{st, dep.Dir}, true, nil
}

// customRoutesList is GET: a bare array, as the spec gives it.
func (s *Server) customRoutesList(w http.ResponseWriter) error {
	rows, err := store.ListCustomRoutes(s.store.DB)
	if err != nil {
		return err
	}
	out := make([]jsonx.Object, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.routeJSON(row))
	}
	writeJSON(w, 200, out)
	return nil
}

// routeEvent is what a write emits once writeMu is released: Emit runs its listeners there and
// then, and one of them writes to the store.
type routeEvent struct {
	name string
	data jsonx.Object
}

// underWriteMu runs fn, which answers the request itself, under writeMu, then emits its event.
func (s *Server) underWriteMu(fn func() (*routeEvent, error)) error {
	ev, err := func() (*routeEvent, error) {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		return fn()
	}()
	if ev != nil {
		s.bus.Emit(ev.name, ev.data)
	}
	return err
}

// customRouteCreate is POST { name, deployment?, force? }; a null optional field is an absent one. It never moves a route: a new one is
// never held, and an existing name is 409. `force` is accepted and ignored.
func (s *Server) customRouteCreate(w http.ResponseWriter, r *http.Request) error {
	body := bodyObject(r)
	name, ok := getStr(body, "name")
	depID, depOK := getStr(body, "deployment")
	if v, present := bodyGet(body, "deployment"); present && v != nil && !depOK {
		ok = false
	}
	if body == nil || !ok {
		writeError(w, 400, "body must be { name, deployment?, force? }")
		return nil
	}
	if !routeNameRe.MatchString(name) {
		writeError(w, 400, "bad route name "+name+": a DNS label, a-z 0-9 and -, at most 40")
		return nil
	}
	if reservedRouteNames[name] {
		writeError(w, 400, name+" is reserved")
		return nil
	}
	return s.underWriteMu(func() (*routeEvent, error) {
		if _, exists, err := store.GetCustomRoute(s.store.DB, name); err != nil || exists {
			if exists {
				writeError(w, 409, name+" exists")
			}
			return nil, err
		}
		metas, err := s.registry.List()
		if err != nil {
			return nil, err
		}
		for _, m := range metas {
			if st, err := s.resolveDep(m.ID, nil); err == nil && st.Stack == name {
				writeError(w, 409, name+" is the stack of "+m.ID)
				return nil, nil
			}
		}
		var stack any
		if depID != "" {
			d, found, err := s.deploymentOr404(w, depID)
			if !found || err != nil {
				return nil, err
			}
			if d.st != nil {
				stack = d.st.Stack
				if answered, err := s.assignConflict(w, name, d); answered || err != nil {
					return nil, err
				}
			}
		}
		// The checks above ran under writeMu, which every write to the table holds: nothing can
		// have taken the name, or the route, since.
		if err := s.store.Tx(func(q store.Querier) error { return store.CreateCustomRoute(q, name, depID) }); err != nil {
			return nil, err
		}
		row, _, err := store.GetCustomRoute(s.store.DB, name)
		if err != nil {
			return nil, err
		}
		if depID == "" {
			writeJSON(w, 201, s.routeJSON(row)) // the file cannot have changed
			return nil, nil
		}
		s.rendered()
		writeJSON(w, 201, s.routeJSON(row))
		return &routeEvent{"route.assigned", jsonx.O("route", name, "deployment", depID, "stack", stack)}, nil
	})
}

// customRouteAssign is PUT …/assignment { deployment, force? }. Held by another deployment is 409
// with assignedTo unless force, which moves it in one transaction and one render. The same
// deployment again is a no-op.
func (s *Server) customRouteAssign(w http.ResponseWriter, r *http.Request, name string) error {
	body := bodyObject(r)
	depID, ok := getStr(body, "deployment")
	force, forceOK := getBool(body, "force")
	if v, present := bodyGet(body, "force"); present && v != nil && !forceOK {
		ok = false
	}
	if body == nil || !ok || depID == "" {
		writeError(w, 400, "body must be { deployment, force? }")
		return nil
	}
	return s.underWriteMu(func() (*routeEvent, error) {
		row, exists, err := store.GetCustomRoute(s.store.DB, name)
		if err != nil {
			return nil, err
		}
		if !exists {
			writeError(w, 404, "no such route: "+name)
			return nil, nil
		}
		d, found, err := s.deploymentOr404(w, depID)
		if !found || err != nil {
			return nil, err
		}
		if row.DeploymentID == depID {
			writeJSON(w, 200, s.routeJSON(row))
			return nil, nil
		}
		if row.DeploymentID != "" && !force {
			s.writeHeld(w, name, row.DeploymentID)
			return nil, nil
		}
		var stack any
		if d.st != nil {
			stack = d.st.Stack
			if answered, err := s.assignConflict(w, name, d); answered || err != nil {
				return nil, err
			}
		}
		var previous string
		err = s.store.Tx(func(q store.Querier) error {
			var err error
			previous, err = store.AssignCustomRoute(q, name, depID, force)
			return err
		})
		if err != nil {
			return nil, err
		}
		s.rendered()
		if row, _, err = store.GetCustomRoute(s.store.DB, name); err != nil {
			return nil, err
		}
		writeJSON(w, 200, s.routeJSON(row))
		data := jsonx.O("route", name, "deployment", depID, "stack", stack)
		if previous != "" {
			data = append(data, jsonx.KV{K: "previous", V: previous})
		}
		return &routeEvent{"route.assigned", data}, nil
	})
}

func (s *Server) writeHeld(w http.ResponseWriter, name, holder string) {
	stack := s.stackOf(holder)
	who := holder
	if st, ok := stack.(string); ok {
		who = st
	}
	writeJSON(w, 409, jsonx.O("error", name+" is assigned to "+who, "assignedTo", jsonx.O("deployment", holder, "stack", stack)))
}

// customRouteDrop is DELETE …/assignment (reason "unassigned": always allowed, the route is kept)
// and DELETE /api/custom-routes/{name} (reason "deleted"). route.unassigned only when it was held.
func (s *Server) customRouteDrop(w http.ResponseWriter, name, reason string) error {
	return s.underWriteMu(func() (*routeEvent, error) {
		var previous string
		err := s.store.Tx(func(q store.Querier) error {
			var err error
			if reason == "deleted" {
				previous, err = store.DeleteCustomRoute(q, name)
			} else {
				previous, err = store.UnassignCustomRoute(q, name)
			}
			return err
		})
		if errors.Is(err, store.ErrCustomRouteNotFound) {
			writeError(w, 404, "no such route: "+name)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if reason == "deleted" {
			writeJSON(w, 200, jsonx.O("deleted", name))
		} else {
			row, _, err := store.GetCustomRoute(s.store.DB, name)
			if err != nil {
				return nil, err
			}
			writeJSON(w, 200, s.routeJSON(row))
		}
		if previous == "" {
			return nil, nil
		}
		s.rendered()
		return &routeEvent{"route.unassigned", jsonx.O("route", name, "deployment", previous, "stack", s.stackOf(previous), "reason", reason)}, nil
	})
}

func bodyGet(m *omap.Map, k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	return m.Get(k)
}
