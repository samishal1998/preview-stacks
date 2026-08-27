package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/settings"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

var (
	hostVarRe     = regexp.MustCompile(`^/api/host-vars/([A-Za-z0-9_]+)$`)
	specRe        = regexp.MustCompile(`^/api/specs/([^/]+)$`)
	routingFileRe = regexp.MustCompile(`^/api/routing/([^/]+)$`)
	redeliverRe   = regexp.MustCompile(`^/api/notifiers/(\d+)/deliveries/(\d+)/redeliver$`)
	notifierRe    = regexp.MustCompile(`^/api/notifiers/(\d+)(?:/(test|deliveries))?$`)
	registryRe    = regexp.MustCompile(`^/api/registries/(.+)$`)
	cancelRe      = regexp.MustCompile(`^/api/jobs/([^/]+)/cancel$`)
	jobRe         = regexp.MustCompile(`^/api/jobs/([^/]+)(?:/(stream))?$`)
)

// routes is the gated route table, in api.ts order. An ordered if-chain over precompiled
// regexps on the ESCAPED path: a mux cannot reproduce greedy /api/registries/(.+), any-method
// /api/jobs, or undecoded-path semantics.
func (s *Server) routes(w http.ResponseWriter, r *http.Request, path string, who *auth.Principal, vars map[string]string) error {
	// THE ROLE GATE, above every route in this file and in accountRoutes, and DEFAULT-DENY: a route
	// with no row in permissions.go is root's. See that file's header before adding a route.
	if !permit(w, who, r.Method, path) {
		return nil
	}
	if done, err := s.accountRoutes(w, r, path, who); done || err != nil {
		return err
	}

	// ---- the whole host, portable: every credential on it. ROOT TOKEN ONLY ----
	// First in the chain after the account routes, and matched on the exact path, so no pattern
	// added later can reach it before its own gate does. routes_config.go's header is the reason
	// this one route refuses the admin session every other admin route admits.
	if path == "/api/config" {
		return s.configRoutes(w, r, who)
	}
	// The sealed import, which an ADMIN SESSION may reach and the export above may not — the caller
	// must already hold the file and its passphrase, and everything in it is about to be plaintext
	// on this host regardless. routes_config.go's mayApplySealedConfig has the full argument.
	if path == "/api/config/sealed" {
		s.importSealedConfig(w, r, who)
		return nil
	}

	// ---- the whole registry ----
	if path == "/api/deployments" && r.Method == http.MethodGet {
		return s.listDeployments(w, vars)
	}

	// ---- one deployment ----
	if m := deploymentRe.FindStringSubmatch(path); m != nil {
		id, err := segment(m[1])
		if err != nil {
			return err
		}
		action := jobs.Action(m[2])
		if action == "" && r.Method == http.MethodPut {
			return s.putDeployment(w, r, id)
		}
		dep, err := s.registry.Get(id)
		if err != nil {
			return err
		}
		if dep == nil {
			writeError(w, 404, "no such deployment: "+id)
			return nil
		}
		if action == "" {
			switch r.Method {
			case http.MethodGet:
				return s.getDeployment(w, dep, vars)
			case http.MethodDelete:
				return s.deleteDeployment(w, dep, vars)
			}
			writeError(w, 405, "use GET, PUT or DELETE")
			return nil
		}
		if r.Method != http.MethodPost {
			writeError(w, 405, "use POST")
			return nil
		}
		return s.lifecycle(w, r, dep, action, who, vars)
	}

	// ---- stop everything one deployment's stack has outstanding ----
	if m := deployCancelRe.FindStringSubmatch(path); m != nil {
		if r.Method != http.MethodPost {
			writeError(w, 405, "use POST")
			return nil
		}
		dep, err := s.depOr404(w, m[1])
		if dep == nil || err != nil {
			return err
		}
		return s.cancelStack(w, dep, who, vars)
	}

	// ---- a read-only link to one deployment ----
	if m := shareRe.FindStringSubmatch(path); m != nil {
		id, err := segment(m[1])
		if err != nil {
			return err
		}
		return s.shareDeployment(w, r, id, who)
	}

	// ---- single sign-on: the configuration ----
	if path == "/api/sso/config" {
		return s.ssoConfigRoutes(w, r)
	}

	// ---- the two runtime host settings ----
	// One literal per key, not a pattern: the permission is PER KEY (maintainer for the cap, admin
	// for the role a new account gets), and a key nobody listed 404s here rather than reaching a
	// validator it would fail anyway. routes_settings.go's header has the rest.
	if path == "/api/settings" && r.Method == http.MethodGet {
		return s.getSettings(w)
	}
	if path == "/api/settings/max_jobs" {
		return s.putSetting(w, r, settings.KeyMaxJobs)
	}
	if path == "/api/settings/default_role" {
		return s.putSetting(w, r, settings.KeyDefaultRole)
	}

	// ---- the control stack, visible ----
	if path == "/api/control/runtime" && r.Method == http.MethodGet {
		return s.controlRuntime(w)
	}
	if path == "/api/control/restart" && r.Method == http.MethodPost {
		return s.controlRestart(w, r)
	}

	// ---- the swarm ----
	if path == "/api/swarm" && r.Method == http.MethodGet {
		info := swarm.SwarmInfo(s.host)
		note := "This daemon is not a swarm manager. Previews run with docker compose on this host; `pstack init --orchestrator swarm` (on the host) enables swarm mode."
		if info.Active {
			note = "Add a worker: GET /api/swarm/join?format=command|script|cloud-config, run it on the new machine."
		}
		writeJSON(w, 200, append(spread(info), jsonx.KV{K: "ports", V: swarm.SwarmPorts}, jsonx.KV{K: "note", V: note}))
		return nil
	}
	if path == "/api/swarm/join" && r.Method == http.MethodGet {
		// The admin check that used to be inline here is a row in permissions.go now, where it
		// reads MAINTAINER — a deliberate loosening, argued in that file's header.
		format, ok := query(r.URL.RawQuery, "format")
		if !ok {
			format = "command"
		}
		var distro *string
		if d, ok := query(r.URL.RawQuery, "distro"); ok {
			distro = &d
		}
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: s.host, Format: format, Distro: distro})
		if !made.OK {
			status := 503
			switch made.Kind {
			case swarm.BadFormat, swarm.BadDistro:
				status = 400
			case swarm.NotAManager:
				status = 409
			}
			writeError(w, status, made.Message)
			return nil
		}
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(made.Text))
		return nil
	}

	// ---- host variables & secrets ----
	if path == "/api/host-vars" && r.Method == http.MethodGet {
		rows, err := s.hostVars.List()
		if err != nil {
			return err
		}
		writeJSON(w, 200, jsonx.O("entries", rows))
		return nil
	}
	if m := hostVarRe.FindStringSubmatch(path); m != nil {
		name := m[1]
		switch r.Method {
		case http.MethodPut:
			body := bodyObject(r)
			value, vok := getStr(body, "value")
			secret, sok := getBool(body, "secret")
			if body == nil || !vok || !sok {
				writeError(w, 400, "body must be { value: string, secret: boolean }")
				return nil
			}
			created, err := s.hostVars.Put(name, value, secret)
			if err != nil {
				return err
			}
			// The value is echoed back ONLY for a variable.
			var echoed any
			if !secret {
				echoed = value
			}
			status := 200
			if created {
				status = 201
			}
			writeJSON(w, status, jsonx.O("name", name, "secret", secret, "value", echoed))
			return nil
		case http.MethodDelete:
			removed, err := s.hostVars.Remove(name)
			if err != nil {
				return err
			}
			if !removed {
				writeError(w, 404, "no such entry: "+name)
				return nil
			}
			writeJSON(w, 200, jsonx.O("deleted", name))
			return nil
		}
		writeError(w, 405, "use PUT or DELETE")
		return nil
	}

	// ---- named specs ----
	if path == "/api/specs" && r.Method == http.MethodGet {
		list, err := s.specs.List()
		if err != nil {
			return err
		}
		writeJSON(w, 200, jsonx.O("specs", list))
		return nil
	}
	if m := specRe.FindStringSubmatch(path); m != nil {
		name, err := segment(m[1])
		if err != nil {
			return err
		}
		return s.specRoutes(w, r, name)
	}

	// ---- the control stack: READ ONLY ----
	if path == "/api/control" && r.Method == http.MethodGet {
		return s.controlStack(w)
	}

	// ---- recent logs / follow / source / containers / terminal / runtime / readiness ----
	if m := logsRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.deploymentLogs(w, r, dep, vars)
	}
	if m := logStreamRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.logStream(w, r, dep, vars)
	}
	if m := sourceRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.deploymentSource(w, r, dep)
	}
	if m := containerRe.FindStringSubmatch(path); m != nil {
		if r.Method != http.MethodPost {
			writeError(w, 405, "use POST")
			return nil
		}
		wanted, err := segment(m[2])
		if err != nil {
			return err
		}
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.containerAction(w, r, dep, wanted, m[3], who, vars)
	}
	if m := terminalRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		id, err := segment(m[1])
		if err != nil {
			return err
		}
		// terminal.MayOpenTerminal's admin check used to be here; permissions.go's row says
		// DEVELOPER, and that file's header says why refusing the shell to someone who can already
		// `up` an arbitrary compose file is theatre. A share principal never reaches this line —
		// shareAllows refuses it before the chain.
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		_ = id
		return s.terminalRoute(w, r, dep, who, vars)
	}
	if path == "/api/terminal-sessions" && r.Method == http.MethodGet {
		rows, err := s.terminals.Recent(100)
		if err != nil {
			return err
		}
		writeJSON(w, 200, jsonx.O("sessions", rows))
		return nil
	}
	if m := runtimeRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.deploymentRuntime(w, dep, vars)
	}
	if m := readinessRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		dep, err := s.depOr404(w, m[1])
		if err != nil || dep == nil {
			return err
		}
		return s.deploymentReadiness(w, r, dep, vars)
	}

	// ---- every route Traefik has, from container labels ----
	if path == "/api/routing/live" && r.Method == http.MethodGet {
		all := inspect.AllTraefikRouters(s.host)
		writeJSON(w, 200, jsonx.O("reachable", all.Reachable, "routes", all.Routes))
		return nil
	}

	// ---- Traefik dynamic configuration ----
	if path == "/api/routing" && r.Method == http.MethodGet {
		writeJSON(w, 200, jsonx.O("dir", s.routing.Dir, "writable", s.routing.Writable(), "files", s.routing.List()))
		return nil
	}
	if m := routingFileRe.FindStringSubmatch(path); m != nil {
		name, err := segment(m[1])
		if err != nil {
			return err
		}
		switch r.Method {
		case http.MethodGet:
			// Dynamic config holds basicAuth hashes and forwardAuth URLs — the same secret class
			// as a spec's hooks.
			if !s.authed(r) {
				writeJSON(w, 200, jsonx.O("name", name, "sourceWithheld", true))
				return nil
			}
			content, err := s.routing.Read(name)
			if err != nil {
				return err
			}
			writeJSON(w, 200, jsonx.O("name", name, "content", content))
			return nil
		case http.MethodPut:
			body := bodyObject(r)
			content, ok := getStr(body, "content")
			if body == nil || !ok {
				writeError(w, 400, "body must be { content: string }")
				return nil
			}
			// `previous` is the undo: there is deliberately no on-disk history.
			previous, err := s.routing.Write(name, content)
			if err != nil {
				return err
			}
			action, status := "replaced", 200
			if previous == nil {
				action, status = "created", 201
			}
			// The name and the action only — never the content.
			s.bus.Emit("routing.changed", jsonx.O("file", name, "action", action))
			writeJSON(w, status, jsonx.O("name", name, "previous", previous))
			return nil
		case http.MethodDelete:
			previous, err := s.routing.Remove(name)
			if err != nil {
				return err
			}
			s.bus.Emit("routing.changed", jsonx.O("file", name, "action", "deleted"))
			writeJSON(w, 200, jsonx.O("deleted", name, "previous", previous))
			return nil
		}
		writeError(w, 405, "use GET, PUT or DELETE")
		return nil
	}

	// ---- notifiers ----
	if path == "/api/notifiers/meta" && r.Method == http.MethodGet {
		types := make([]jsonx.Object, 0, len(notify.Types))
		for _, ty := range notify.Types {
			types = append(types, jsonx.O("kind", ty.Kind, "label", ty.Label, "fields", ty.Fields, "signs", ty.Signs))
		}
		writeJSON(w, 200, jsonx.O("events", events.Names, "wildcard", events.Wildcard, "types", types))
		return nil
	}
	if path == "/api/notifiers" {
		switch r.Method {
		case http.MethodGet:
			rows, err := s.hooks.List()
			if err != nil {
				return err
			}
			writeJSON(w, 200, jsonx.O("notifiers", rows))
			return nil
		case http.MethodPost:
			body := bodyObject(r)
			name, ok := getStr(body, "name")
			if body == nil || !ok {
				writeError(w, 400, "body must be { name, events[], config{}, type? }")
				return nil
			}
			cfg := omap.New()
			if raw, present := body.Get("config"); present {
				m, isMap := raw.(*omap.Map)
				if !isMap {
					writeError(w, 400, "`config` must be an object")
					return nil
				}
				cfg = m
			}
			kind := "webhook"
			if t, ok := getStr(body, "type"); ok && t != "" {
				kind = t
			}
			evs := []string{}
			if raw, ok := body.Get("events"); ok {
				if list, isList := raw.([]any); isList {
					for _, e := range list {
						evs = append(evs, js.ToString(e))
					}
				}
			}
			ty, err := notify.TypeOf(kind)
			if err != nil {
				return err
			}
			row, secret, err := s.hooks.Create(webhooks.CreateArgs{Type: kind, Name: name, Config: cfg, Events: evs, ValidateConfig: notify.ValidateConfig, Signs: ty.Signs})
			if err != nil {
				return err
			}
			// The ONLY time the signing secret leaves the server, and null for a type that does
			// not sign.
			writeJSON(w, 201, jsonx.O("notifier", row, "secret", secret))
			return nil
		}
		writeError(w, 405, "use GET or POST")
		return nil
	}
	if m := redeliverRe.FindStringSubmatch(path); m != nil {
		return s.redeliver(w, r, intID(m[1]), intID(m[2]), m[2])
	}
	if m := notifierRe.FindStringSubmatch(path); m != nil {
		return s.notifierRoutes(w, r, intID(m[1]), m[2])
	}

	// ---- private registry credentials ----
	if path == "/api/registries" && r.Method == http.MethodGet {
		writeJSON(w, 200, s.registries.State())
		return nil
	}
	if m := registryRe.FindStringSubmatch(path); m != nil {
		host, err := segment(m[1])
		if err != nil {
			return err
		}
		switch r.Method {
		case http.MethodPut:
			body := bodyObject(r)
			username, uok := getStr(body, "username")
			password, pok := getStr(body, "password")
			if body == nil || !uok || !pok {
				writeError(w, 400, "body must be { username: string, password: string }")
				return nil
			}
			key, err := s.registries.Put(host, username, password)
			if err != nil {
				return err
			}
			// The normalized key is echoed because it may differ from what was sent.
			writeJSON(w, 200, jsonx.O("registry", key, "stored", true))
			return nil
		case http.MethodDelete:
			removed, err := s.registries.Remove(host)
			if err != nil {
				return err
			}
			if !removed {
				writeError(w, 404, "no credential stored for "+host)
				return nil
			}
			writeJSON(w, 200, jsonx.O("deleted", host))
			return nil
		}
		writeError(w, 405, "use PUT or DELETE")
		return nil
	}

	// ---- jobs ----
	if path == "/api/jobs" {
		writeJSON(w, 200, jsonx.O("jobs", s.jobs.List()))
		return nil
	}
	if m := cancelRe.FindStringSubmatch(path); m != nil {
		if r.Method != http.MethodPost {
			writeError(w, 405, "use POST")
			return nil
		}
		jobID, err := segment(m[1])
		if err != nil {
			return err
		}
		job, ok := s.jobs.Get(jobID)
		if !ok {
			writeError(w, 404, "no such job: "+jobID)
			return nil
		}
		// 409, not 404: the job exists and the caller's request is simply out of date. TERMINAL, not
		// `!= Running`: a QUEUED job is cancellable — it is the whole point of handing its id out in
		// a 202 — and refusing it here would leave a client polling a job it cannot stop.
		if job.State.Terminal() {
			writeJSON(w, 409, jsonx.O("error", "job "+jobID+" already finished ("+string(job.State)+")", "state", job.State))
			return nil
		}
		by := terminal.ActorOf(*who)
		s.jobs.Cancel(jobID, by)
		// The running job's warning would be a lie about a queued one: it never started, so there is
		// no partial state to go hunting for. Same reason `superseded` is not a flavour of
		// `cancelled` (internal/jobs' header).
		warning := "Nothing was undone. Whatever this job created or destroyed before it stopped is " +
			"still that way — run verify to see what exists."
		if job.State == jobs.Queued {
			warning = "It had not started, so nothing was undone."
		}
		writeJSON(w, 200, jsonx.O("cancelled", jobID, "stack", job.Stack, "action", job.Action, "by", by,
			"warning", warning))
		return nil
	}
	if m := jobRe.FindStringSubmatch(path); m != nil {
		jobID, err := segment(m[1])
		if err != nil {
			return err
		}
		job, ok := s.jobs.Get(jobID)
		if !ok {
			writeError(w, 404, "no such job")
			return nil
		}
		if m[2] != "stream" {
			writeJSON(w, 200, jsonx.O("job", job))
			return nil
		}
		s.jobStream(w, r, job)
		return nil
	}

	writeError(w, 404, "not found")
	return nil
}

// depOr404 resolves a path segment to a deployment, answering 404 itself. (nil, nil) = answered.
func (s *Server) depOr404(w http.ResponseWriter, raw string) (*registry.Deployment, error) {
	id, err := segment(raw)
	if err != nil {
		return nil, err
	}
	dep, err := s.registry.Get(id)
	if err != nil {
		return nil, err
	}
	if dep == nil {
		writeError(w, 404, "no such deployment: "+id)
		return nil, nil
	}
	return dep, nil
}

// specRoutes is /api/specs/:name.
func (s *Server) specRoutes(w http.ResponseWriter, r *http.Request, name string) error {
	if r.Method == http.MethodPut {
		body := bodyObject(r)
		src, ok := getStr(body, "spec")
		if body == nil || !ok || strings.TrimSpace(src) == "" {
			writeError(w, 400, "body must be { spec: string, compose?: string, description?: string }")
			return nil
		}
		opts := specs.PutOptions{}
		if c, present := body.Get("compose"); present {
			str, isStr := c.(string)
			if !isStr {
				writeError(w, 400, "`compose` must be a string: the compose file contents")
				return nil
			}
			opts.ComposeYaml = &str
		}
		if d, ok := getStr(body, "description"); ok {
			opts.Description = &d
		}
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		prev, err := s.specs.Get(name)
		if err != nil {
			return err
		}
		existed := prev != nil
		stored, err := s.specs.Put(name, src, opts)
		if err != nil {
			return err
		}
		s.bus.Emit("spec.stored", jsonx.O("name", stored.Name, "kind", stored.Kind, "replaced", existed, "requiredVars", stored.RequiredVars))
		status := 201
		if existed {
			status = 200
		}
		writeJSON(w, status, jsonx.O("spec", stored.SpecMeta))
		return nil
	}
	stored, err := s.specs.Get(name)
	if err != nil {
		return err
	}
	if stored == nil {
		writeError(w, 404, "no such spec: "+name)
		return nil
	}
	switch r.Method {
	case http.MethodGet:
		// THE SOURCE IS A SECRET, THE METADATA IS NOT: hook bodies are shell strings that routinely
		// carry a credential inline. Withheld EXPLICITLY rather than sent empty.
		out := stored.Pairs()
		if s.authed(r) {
			src, err := s.specs.Source(name)
			if err != nil {
				return err
			}
			out = append(out, jsonx.KV{K: "source", V: src})
		} else {
			out = append(out, jsonx.KV{K: "sourceWithheld", V: true})
		}
		writeJSON(w, 200, out)
		return nil
	case http.MethodDelete:
		// Fail closed: a deployment referencing a deleted spec can never be torn down.
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		metas, err := s.registry.List()
		if err != nil {
			return err
		}
		users := []string{}
		for _, d := range metas {
			if d.SpecName == name {
				users = append(users, d.ID)
			}
		}
		if len(users) > 0 {
			writeJSON(w, 409, jsonx.O("error", `spec "`+name+`" is referenced by `+js.NumberString(float64(len(users)))+" deployment(s) — deleting it "+
				"would leave them unresolvable, and an unresolvable deployment can never be "+
				"torn down. Remove them first.", "deployments", users))
			return nil
		}
		if err := s.specs.Remove(name); err != nil {
			return err
		}
		s.bus.Emit("spec.deleted", jsonx.O("name", name))
		writeJSON(w, 200, jsonx.O("deleted", name))
		return nil
	}
	writeError(w, 405, "use GET, PUT or DELETE")
	return nil
}

// controlStack is GET /api/control: READ-ONLY, never actionable (invariant 12). The viewer-rank
// summary — the maintainer's operator page, with restart counts and the one permitted action, is
// /api/control/runtime and /api/control/restart (routes_control.go). "Not managed through this
// API" stays true: a restart re-renders nothing and may not touch the pstack container.
func (s *Server) controlStack(w http.ResponseWriter) error {
	ps := s.host.Run("docker compose -p "+compose.Shq(initctl.ControlProject)+" ps --format json", exec.RunOptions{})
	services := []jsonx.Object{}
	parseError := false
	if ps.OK {
		// `--format json` emits either an array or newline-delimited objects depending on the
		// compose version. Handle both.
		raw := strings.TrimSpace(ps.Stdout)
		var rows []map[string]any
		if strings.HasPrefix(raw, "[") {
			if err := jsonUnmarshal([]byte(raw), &rows); err != nil {
				parseError = true
			}
		} else {
			for _, line := range strings.Split(raw, "\n") {
				if line == "" {
					continue
				}
				var row map[string]any
				if err := jsonUnmarshal([]byte(line), &row); err != nil {
					parseError = true
					rows = nil
					break
				}
				rows = append(rows, row)
			}
		}
		if !parseError {
			for _, row := range rows {
				name := strField(row, "Service")
				if name == "" {
					name = strField(row, "Name")
				}
				if name == "" {
					name = "?"
				}
				state := strField(row, "State")
				if state == "" {
					state = "?"
				}
				services = append(services, jsonx.O("name", name, "state", state, "health", strField(row, "Health"), "image", strField(row, "Image")))
			}
		}
	}
	writeJSON(w, 200, jsonx.O(
		"project", initctl.ControlProject,
		"reachable", ps.OK,
		"parseError", parseError,
		"services", services,
		"managedBy", "pstack init, from the host",
		"actionable", false,
		"note", "The control stack is not managed through this API: the process serving this request "+
			"runs inside it. Upgrade it with pstack init on the host.",
	))
	return nil
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func strField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// redeliver is POST /api/notifiers/:id/deliveries/:deliveryId/redeliver: the stored envelope, byte
// for byte, with the original id, a fresh timestamp and x-pstack-redelivery: 1.
func (s *Server) redeliver(w http.ResponseWriter, r *http.Request, nid, did int64, didRaw string) error {
	if r.Method != http.MethodPost {
		writeError(w, 405, "use POST")
		return nil
	}
	row, err := s.hooks.Get(nid)
	if err != nil {
		return err
	}
	if row == nil {
		writeError(w, 404, "no such notifier: "+js.NumberString(float64(nid)))
		return nil
	}
	d, err := s.hooks.DeliveryWithPayload(did)
	if err != nil {
		return err
	}
	// Belongs-to check, not just existence.
	if d == nil || d.NotifierID != nid {
		writeError(w, 404, "no delivery "+didRaw+" for notifier "+js.NumberString(float64(nid)))
		return nil
	}
	if d.Payload == nil || *d.Payload == "" {
		writeError(w, 409, "delivery "+didRaw+" has no stored payload — it was recorded before payloads were "+
			"captured (0.25.0), or it was dropped before an envelope existed. There is nothing "+
			"to replay, and inventing one would send an event that never happened.")
		return nil
	}
	parsed, perr := omap.Parse([]byte(*d.Payload))
	stored, isMap := parsed.(*omap.Map)
	if perr != nil || !isMap {
		writeError(w, 409, "delivery "+didRaw+" has an unreadable payload")
		return nil
	}
	eventID, _ := getStr(stored, "id")
	eventName, _ := getStr(stored, "event")
	data, _ := stored.Get("data")
	if data == nil {
		data = omap.New()
	}
	// The dispatcher needs the UNMASKED row (the read path masks).
	raw := *row
	if cfg, err := s.hooks.RawConfigOf(nid); err == nil && cfg != nil {
		raw.Config = cfg
	}
	s.dispatcher.Redeliver(raw, events.Event{ID: eventID, Event: eventName, At: time.Now().UnixMilli(), Data: jsonx.Must(data)})
	writeJSON(w, 200, jsonx.O("redelivered", did, "notifier", nid, "event", eventName, "eventId", eventID,
		"note", "Queued. It carries the original event id, so a receiver that already processed it "+
			"will dedupe — and x-pstack-redelivery: 1 so one that did not can tell."))
	return nil
}

// notifierRoutes is /api/notifiers/:id(/test|/deliveries).
func (s *Server) notifierRoutes(w http.ResponseWriter, r *http.Request, nid int64, sub string) error {
	row, err := s.hooks.Get(nid)
	if err != nil {
		return err
	}
	if row == nil {
		writeError(w, 404, "no such notifier: "+js.NumberString(float64(nid)))
		return nil
	}
	switch {
	case sub == "" && r.Method == http.MethodDelete:
		if _, err := s.hooks.Remove(nid); err != nil {
			return err
		}
		writeJSON(w, 200, jsonx.O("deleted", nid))
		return nil
	case sub == "" && r.Method == http.MethodPatch:
		body := bodyObject(r)
		enabled, ok := getBool(body, "enabled")
		if body == nil || !ok {
			writeError(w, 400, "body must be { enabled: boolean }")
			return nil
		}
		if _, err := s.hooks.SetEnabled(nid, enabled); err != nil {
			return err
		}
		updated, err := s.hooks.Get(nid)
		if err != nil {
			return err
		}
		writeJSON(w, 200, jsonx.O("notifier", updated))
		return nil
	case sub == "deliveries" && r.Method == http.MethodGet:
		rows, err := s.hooks.Deliveries(nid, 50)
		if err != nil {
			return err
		}
		out := make([]jsonx.Object, 0, len(rows))
		for _, d := range rows {
			// `replayable` rather than making the UI guess from the age of the row.
			replayable := false
			if dl, err := s.hooks.DeliveryWithPayload(d.ID); err == nil && dl != nil && dl.Payload != nil {
				replayable = true
			}
			out = append(out, append(spread(d), jsonx.KV{K: "replayable", V: replayable}))
		}
		writeJSON(w, 200, jsonx.O("deliveries", out, "queued", s.dispatcher.Queued(nid)))
		return nil
	case sub == "test" && r.Method == http.MethodPost:
		return s.testNotifier(w, nid, row)
	}
	writeError(w, 405, "use PATCH or DELETE here, or /deliveries (GET) and /test (POST)")
	return nil
}

// testNotifier sends a synthetic delivery DIRECTLY, never through the bus.
func (s *Server) testNotifier(w http.ResponseWriter, nid int64, row *webhooks.NotifierRow) error {
	ty, err := notify.TypeOf(row.Type)
	if err != nil {
		writeError(w, 400, `unknown notifier type "`+row.Type+`"`)
		return nil
	}
	secret := ""
	if sp, err := s.hooks.SecretOf(nid); err == nil && sp != nil {
		secret = *sp
	}
	// RAW, not row.Config: the row masks secret-marked fields for display.
	cfg := row.Config
	if raw, err := s.hooks.RawConfigOf(nid); err == nil && raw != nil {
		cfg = raw
	}
	now := time.Now().UnixMilli()
	eventID := "evt_test_" + js.Base36(now)
	deliveryID, err := s.hooks.StartDelivery(nid, eventID, "test", nil)
	if err != nil {
		return err
	}
	ctx, cancel := contextTimeout(5 * time.Second)
	defer cancel()
	result := ty.Send(ctx, events.Event{ID: eventID, Event: "job.succeeded", At: now,
		Data: jsonx.Must(jsonx.O("test", true, "note", "Test delivery from pstack — no job ran."))}, cfg, secret)
	status := "failed"
	if result.OK {
		status = "ok"
	}
	redacted := ""
	if result.Error != "" {
		redacted = notify.RedactForNotifier(result.Error, secret, cfg)
	}
	_ = s.hooks.FinishDelivery(deliveryID, webhooks.Result{Status: status, Attempts: 1, ResponseCode: result.Status, Error: redacted})
	_ = s.hooks.NoteResult(nid, status)
	_ = s.hooks.Prune(nid)
	out := jsonx.O("ok", result.OK)
	if result.Status != nil {
		out = append(out, jsonx.KV{K: "status", V: *result.Status})
	}
	if redacted != "" {
		out = append(out, jsonx.KV{K: "error", V: redacted})
	}
	writeJSON(w, 200, jsonx.O("result", out))
	return nil
}
