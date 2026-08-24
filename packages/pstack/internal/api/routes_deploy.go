package api

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/readiness"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

var (
	deploymentRe = regexp.MustCompile(`^/api/deployments/([^/]+)(?:/(up|down|verify|sleep|wake))?$`)
	shareRe      = regexp.MustCompile(`^/api/deployments/([^/]+)/share$`)
	logsRe       = regexp.MustCompile(`^/api/deployments/([^/]+)/logs$`)
	logStreamRe  = regexp.MustCompile(`^/api/deployments/([^/]+)/logs/stream$`)
	sourceRe     = regexp.MustCompile(`^/api/deployments/([^/]+)/source$`)
	containerRe  = regexp.MustCompile(`^/api/deployments/([^/]+)/containers/([^/]+)/(start|stop|restart)$`)
	terminalRe   = regexp.MustCompile(`^/api/deployments/([^/]+)/terminal$`)
	runtimeRe    = regexp.MustCompile(`^/api/deployments/([^/]+)/runtime$`)
	readinessRe  = regexp.MustCompile(`^/api/deployments/([^/]+)/readiness$`)
	serviceRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	durationRe   = regexp.MustCompile(`^(\d+[smhd])+$`)
	rfc3339Re    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[\d:.+Z-]{4,}$`)
)

// segment is decodeURIComponent(m[i]); a malformed escape is the reference's URIError → 500.
func segment(s string) (string, error) {
	v, ok := js.DecodeURIComponent(s)
	if !ok {
		return "", &uriError{}
	}
	return v, nil
}

type uriError struct{}

func (*uriError) Error() string { return "URI malformed" }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// swarmNotesFor is what the swarm conversion will change about a submitted compose file, produced by
// the same pure `Swarmify` the deploy runs, so the submission and the job log can never name
// different keys. A `depends_on` that swarm ignores is the reason this exists: hearing about it once
// the container is already not waiting is too late.
//
// The document is the SUBMITTED one, before the routing labels autolabel generates — so these notes
// name what the AUTHOR wrote, which is what the author can act on, and `up`'s list can be longer
// (the generated traefik.* labels move too).
//
// Advisory, so every reason not to look is silence, never a rejection: not swarm, no compose file,
// or a compose file that does not parse (which `up` will report far better than a guess here).
func swarmNotesFor(st *spec.Stack, composeSource *string) []string {
	if orchestratorOf(st) != spec.Swarm || composeSource == nil {
		return []string{}
	}
	parsed, err := yamlx.ParseString(*composeSource)
	if err != nil {
		return []string{}
	}
	doc, ok := parsed.(*omap.Map)
	if !ok || doc == nil {
		return []string{}
	}
	// The spec's profiles, exactly as autolabel passes them. With none, every service behind a
	// profile would be reported as left out of a stack that will in fact contain it.
	return swarm.Swarmify(doc, st.Compose.Profiles).Notes
}

// listDeployments is GET /api/deployments.
func (s *Server) listDeployments(w http.ResponseWriter, vars map[string]string) error {
	projects := s.composeProjects()
	metas, err := s.registry.List()
	if err != nil {
		return err
	}
	deployments := make([]jsonx.Object, 0, len(metas))
	for _, meta := range metas {
		// One deployment with a missing variable must not fail the whole listing.
		st, err := s.resolveDep(meta.ID, vars)
		row := meta.Pairs()
		var asleep any
		if meta.Sleep != nil {
			asleep = meta.Sleep
		}
		// The record itself, not a boolean folded into `running` (invariant 11).
		row = append(row, jsonx.KV{K: "asleep", V: asleep})
		if err != nil {
			row = append(row,
				jsonx.KV{K: "orchestrator", V: nil},
				jsonx.KV{K: "stack", V: nil},
				jsonx.KV{K: "busy", V: nil},
				jsonx.KV{K: "running", V: nil},
				jsonx.KV{K: "unresolved", V: err.Error()},
			)
		} else {
			var running any
			if projects != nil {
				running = projects[st.Stack]
			}
			row = append(row,
				jsonx.KV{K: "orchestrator", V: nullable(string(orchestratorOf(st)))},
				jsonx.KV{K: "stack", V: st.Stack},
				// Both `null`, never `false`, when they could not be determined.
				jsonx.KV{K: "busy", V: s.jobs.IsBusy(st.Stack)},
				jsonx.KV{K: "running", V: running},
			)
		}
		deployments = append(deployments, row)
	}
	writeJSON(w, 200, jsonx.O("deployments", deployments))
	return nil
}

// putDeployment is PUT /api/deployments/:id — the one route that does not require the deployment
// to exist yet.
func (s *Server) putDeployment(w http.ResponseWriter, r *http.Request, id string) error {
	body := bodyObject(r)
	if body == nil {
		writeError(w, 400, "body must be JSON")
		return nil
	}
	inline, _ := getStr(body, "spec")
	ref, _ := getStr(body, "specName")
	hasInline := strings.TrimSpace(inline) != ""
	hasRef := strings.TrimSpace(ref) != ""
	if hasInline && hasRef {
		writeError(w, 400, "pass `spec` OR `specName`, not both — one deployment has one source of truth")
		return nil
	}
	if !hasInline && !hasRef {
		writeError(w, 400, "body must be { specName: string, vars?: object } to reference a stored spec, "+
			"or { spec: string, compose?: string, env?: object } for an inline one")
		return nil
	}
	// `vars` are STORED with the deployment, unlike `env` which only validates an inline
	// submission: a later `down` resolves the same stack `up` created.
	rawVars, _ := body.Get("vars")
	varKeys, vars, ok := coerceEnv(rawVars)
	if !ok {
		writeError(w, 400, "`vars` must be a mapping of NAME to a string value")
		return nil
	}
	var specSource string
	var composeSource *string
	var specName *string
	if hasRef {
		name := strings.TrimSpace(ref)
		specName = &name
		stored, err := s.specs.Get(name)
		if err != nil {
			if specs.IsError(err) {
				writeError(w, 400, err.Error())
				return nil
			}
			return err
		}
		if stored == nil {
			writeError(w, 404, "no such spec: "+name)
			return nil
		}
		specSource, err = s.specs.Source(name)
		if err != nil {
			return err
		}
		// Copy the spec's compose file alongside, so the deployment directory is self-contained.
		if b, err := os.ReadFile(filepath.Join(stored.Dir, "compose.yml")); err == nil {
			c := string(b)
			composeSource = &c
		}
		// Name every variable the spec needs but was not given, instead of failing later with a
		// parse error that names only the first one.
		missing := []string{}
		for _, v := range stored.RequiredVars {
			if _, ok := vars[v]; ok {
				continue
			}
			if _, ok := s.env[v]; ok {
				continue
			}
			missing = append(missing, v)
		}
		if len(missing) > 0 {
			writeJSON(w, 400, jsonx.O("error", `spec "`+name+`" needs variable(s) not supplied: `+strings.Join(missing, ", "), "requiredVars", stored.RequiredVars))
			return nil
		}
	} else {
		specSource = inline
		if c, present := body.Get("compose"); present {
			str, isStr := c.(string)
			if !isStr {
				writeError(w, 400, "`compose` must be a string: the compose file contents")
				return nil
			}
			composeSource = &str
		}
	}
	rawEnv, _ := body.Get("env")
	_, env, ok := coerceEnv(rawEnv)
	if !ok {
		writeError(w, 400, "`env` must be a mapping of NAME to a string value")
		return nil
	}
	// Validate BEFORE anything touches disk: on a REPLACE a rejected spec would otherwise delete
	// a perfectly good record over a typo while its containers keep running.
	host := s.hostValues()
	parsed, err := spec.Parse(specSource, exec.Merge(s.env, vars, env), host)
	if err != nil {
		if spec.IsSpecError(err) {
			writeError(w, 400, "spec: "+err.Error())
			return nil
		}
		return err
	}
	// Swapping the spec mid-job means the eventual `down` tears down with different profiles and
	// axes than `up` created. Held across the write: a lifecycle POST racing this gets its 409.
	release, ok := s.jobs.Hold(parsed.Stack)
	if !ok {
		writeJSON(w, 409, jsonx.O("error", "stack "+parsed.Stack+" has a job in flight — wait for it before replacing the spec", "stack", parsed.Stack))
		return nil
	}
	defer release()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, err := s.registry.Get(id)
	if err != nil {
		return err
	}
	existed := existing != nil
	// Does another deployment already resolve to this stack? A WARNING, NOT A REFUSAL, and only for
	// a NEW deployment: on a replace the id already owns the stack.
	stackSharedWith := []string{}
	if !existed {
		others, err := s.registry.List()
		if err != nil {
			return err
		}
		for _, other := range others {
			if other.ID == id {
				continue
			}
			if st, err := s.resolveDep(other.ID, nil); err == nil && st.Stack == parsed.Stack {
				stackSharedWith = append(stackSharedWith, other.ID)
			}
		}
	}
	// The other advisory finding about this submission: the keys swarm drops. Same shape as
	// stackSharedWith — reported, never a refusal.
	swarmNotes := swarmNotesFor(parsed, composeSource)
	ordered := omap.New()
	for _, k := range varKeys {
		ordered.Set(k, vars[k])
	}
	dep, err := s.registry.Put(id, specSource, registry.PutOptions{ComposeYaml: composeSource, Env: env, Vars: ordered, SpecName: specName, Host: host})
	if err != nil {
		return err
	}
	// Identity only: the sources routinely carry inline credentials, and a webhook URL is
	// outside the auth gate that protects every other reader of them.
	name := "deployment.created"
	if existed {
		name = "deployment.updated"
	}
	s.bus.Emit(name, jsonx.O("id", dep.ID, "kind", dep.Kind, "stack", parsed.Stack, "specName", nullable(dep.SpecName), "stackSharedWith", stackSharedWith))
	storedVars, _ := dep.Doc.Get("vars")
	if storedVars == nil {
		storedVars = omap.New()
	}
	resp := jsonx.O("id", dep.ID, "kind", dep.Kind, "stack", parsed.Stack, "specName", nullable(dep.SpecName),
		"vars", storedVars, "createdAt", dep.CreatedAt, "updatedAt", dep.UpdatedAt)
	// Both omitted when empty, so a client cannot mistake `[]` for "not checked" — and under
	// compose, where nothing was checked, `swarmNotes` is absent for exactly that reason.
	if len(stackSharedWith) > 0 {
		resp = append(resp, jsonx.KV{K: "stackSharedWith", V: stackSharedWith})
	}
	if len(swarmNotes) > 0 {
		resp = append(resp, jsonx.KV{K: "swarmNotes", V: swarmNotes})
	}
	status := 201
	if existed {
		status = 200
	}
	writeJSON(w, status, resp)
	return nil
}

// getDeployment is GET /api/deployments/:id — field by field on purpose (guard 3): axis HOOK
// NAMES, never hook bodies; declared env, never Stack.Env.
func (s *Server) getDeployment(w http.ResponseWriter, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	var sleep any
	if st.Sleep != nil {
		var idle, after any
		if st.Sleep.IdleMs > 0 {
			idle = scheduler.FormatDuration(st.Sleep.IdleMs)
		}
		if st.Sleep.AfterMs > 0 {
			after = scheduler.FormatDuration(st.Sleep.AfterMs)
		}
		sleep = jsonx.O("idle", idle, "after", after)
	}
	var asleep any
	if dep.Sleep != nil {
		asleep = dep.Sleep
	}
	var comp any
	if st.Compose != nil {
		comp = jsonx.O("file", st.Compose.File, "profiles", st.Compose.Profiles, "overlays", st.Compose.Overlays, "subdomains", st.Compose.Subdomains)
	}
	requires := make([]jsonx.Object, 0, len(st.Requires))
	for _, rq := range st.Requires {
		requires = append(requires, jsonx.O("name", rq.Name, "hint", nullable(rq.Hint)))
	}
	axes := make([]jsonx.Object, 0, len(st.Axes))
	for _, a := range st.Axes {
		// Surfaced so the UI can flag the one axis shape `verify` cannot prove clean.
		axes = append(axes, jsonx.O("name", a.Name, "hooks", a.Hooks(), "verifiable", a.AssertGone != ""))
	}
	writeJSON(w, 200, jsonx.O(
		"id", dep.ID,
		"kind", dep.Kind,
		"createdAt", dep.CreatedAt,
		"updatedAt", dep.UpdatedAt,
		"stack", st.Stack,
		"busy", s.jobs.IsBusy(st.Stack),
		"orchestrator", nullable(string(orchestratorOf(st))),
		"sleep", sleep,
		"asleep", asleep,
		"compose", comp,
		"requires", requires,
		"axes", axes,
		"env", redact.DisplayDeclared(st.DeclaredEnv, st.Env),
	))
	return nil
}

// deleteDeployment is DELETE /api/deployments/:id. Fail closed: forgetting a deployment whose
// containers still exist orphans them beyond the control plane's view.
func (s *Server) deleteDeployment(w http.ResponseWriter, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	release, ok := s.jobs.Hold(st.Stack)
	if !ok {
		writeJSON(w, 409, jsonx.O("error", "stack "+st.Stack+" has a job in flight", "stack", st.Stack))
		return nil
	}
	defer release()
	containers := s.containersFor(st.Stack)
	if containers == nil {
		writeJSON(w, 409, jsonx.O("error", "cannot confirm "+st.Stack+" is torn down — docker did not answer. Refusing to "+
			"forget a deployment that may still have containers.", "stack", st.Stack))
		return nil
	}
	if len(containers) > 0 {
		writeJSON(w, 409, jsonx.O("error", st.Stack+" still has "+js.NumberString(float64(len(containers)))+" container(s). Run down first — "+
			"removing the record now would orphan them beyond the control plane's view.", "stack", st.Stack, "containers", len(containers)))
		return nil
	}
	/*
	 * Stop watching it BEFORE the directory goes.
	 *
	 * A readiness watch outlives the `up` that started it and polls docker with the deployment
	 * directory as its cwd; Remove deletes that directory. `down` and container-stop already
	 * cancel for the reason that applies here too — nothing should go on narrating about a
	 * deployment that is gone, least of all emitting `stack.failed` about one an operator
	 * deliberately forgot.
	 */
	s.readiness.Cancel(st.Stack)
	s.writeMu.Lock()
	err = s.registry.Remove(dep.ID)
	if err == nil {
		s.reindex()
	}
	s.writeMu.Unlock()
	if err != nil {
		return err
	}
	s.bus.Emit("deployment.deleted", jsonx.O("id", dep.ID, "stack", st.Stack, "kind", dep.Kind))
	writeJSON(w, 200, jsonx.O("removed", dep.ID, "stack", st.Stack))
	return nil
}

// lifecycle is POST /api/deployments/:id/(up|down|verify|sleep|wake).
func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, action jobs.Action, who *auth.Principal, vars map[string]string) error {
	body := bodyOrEmpty(r)
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	force, _ := body.Get("force")
	// Answer the shared-kind refusal synchronously rather than handing back a job id that is going
	// to fail. Deliberate duplication — stack.Down holds the authoritative guard.
	if action == jobs.Down && st.Kind == spec.Shared && !truthy(force) {
		writeJSON(w, 409, jsonx.O("error", `refusing to tear down "`+st.Stack+"\": kind is `shared`. down runs compose down -v, "+
			`which destroys volumes every tenant depends on. Re-send with { "force": true } if `+
			"that is truly intended.", "stack", st.Stack, "kind", st.Kind))
		return nil
	}
	if action == jobs.Sleep && st.Kind == spec.Shared {
		writeJSON(w, 409, jsonx.O("error", `refusing to put "`+st.Stack+"\" to sleep: kind is `shared`.", "stack", st.Stack, "kind", st.Kind))
		return nil
	}
	if action == jobs.Sleep && st.Compose == nil {
		writeError(w, 400, "this spec has no compose section — there is nothing to put to sleep")
		return nil
	}
	o := lifecycleOptions{By: terminal.ActorOf(*who), Reason: "operator: " + terminal.ActorOf(*who)}
	if v, present := body.Get("verify"); present && v != nil {
		b := truthy(v)
		o.Verify = &b
	}
	if v, present := body.Get("force"); present && v != nil {
		b := truthy(v)
		o.Force = &b
	}
	job, ok := s.startLifecycle(dep.ID, dep, st, action, o)
	if !ok {
		// One job per stack: a `down` racing an `up` over the same database branch is corruption,
		// not contention, so the conflict is surfaced instead of queued.
		writeJSON(w, 409, jsonx.O("error", "stack "+st.Stack+" already has a job in flight", "stack", st.Stack))
		return nil
	}
	writeJSON(w, 202, jsonx.O("job", job.Stub()))
	return nil
}

// shareDeployment is POST /api/deployments/:id/share — a read-only link, narrower than the minter.
func (s *Server) shareDeployment(w http.ResponseWriter, r *http.Request, id string, who *auth.Principal) error {
	if r.Method != http.MethodPost {
		writeError(w, 405, "use POST")
		return nil
	}
	dep, err := s.registry.Get(id)
	if err != nil {
		return err
	}
	if dep == nil {
		writeError(w, 404, "no such deployment: "+id)
		return nil
	}
	if s.opts.Token == "" {
		writeError(w, 400, "share links are signed with PSTACK_TOKEN, and this server has none (loopback dev mode)")
		return nil
	}
	body := bodyOrEmpty(r)
	views := []share.View{}
	if raw, present := body.Get("views"); !present {
		views = append(views, share.Views...)
	} else {
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			writeError(w, 400, "views must be a non-empty list of: "+joinViews())
			return nil
		}
		for _, v := range list {
			str, _ := v.(string)
			if !share.IsView(str) {
				writeError(w, 400, `unknown view "`+js.ToString(v)+`" — views are: `+joinViews())
				return nil
			}
			views = append(views, share.View(str))
		}
	}
	// 7 days by default, 30 at most: there is no per-link revocation, so the TTL is the only thing
	// bounding a leaked link.
	const maxTTL = int64(30 * 86_400_000)
	ttlMs := int64(7 * 86_400_000)
	if raw, present := body.Get("ttl"); present {
		str, isStr := raw.(string)
		parsed, ok := int64(0), false
		if isStr {
			parsed, ok = spec.ParseDuration(str)
		}
		if !ok {
			writeError(w, 400, "ttl must be a duration like 2h, 7d (max 30d)")
			return nil
		}
		if parsed > maxTTL {
			writeError(w, 400, "ttl must be 30d or less")
			return nil
		}
		ttlMs = parsed
	}
	token, claims, err := share.Sign(s.opts.Token, id, views, ttlMs, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	link := baseURL(s.opts.Domain, r) + "/deployments/" + js.EncodeURIComponent(id) + "/public-logs-view?token=" + token
	// What was granted and by whom — never the token (the envelope goes to webhook URLs).
	s.bus.Emit("share.created", jsonx.O("deployment", id, "stack", nil, "views", views, "expiresAt", claims.Exp*1000, "by", terminal.ActorOf(*who)))
	writeJSON(w, 201, jsonx.O("url", link, "token", token, "views", views, "expiresAt", claims.Exp*1000))
	return nil
}

func joinViews() string {
	parts := make([]string, len(share.Views))
	for i, v := range share.Views {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

// logsDuration: a duration compose understands, or "". Validated against a narrow shape rather
// than passed through: it reaches a shell.
func logsDuration(v string) string {
	if v == "" {
		return ""
	}
	if durationRe.MatchString(v) || rfc3339Re.MatchString(v) {
		return v
	}
	return ""
}

// deploymentLogs is GET /api/deployments/:id/logs.
func (s *Server) deploymentLogs(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	q := r.URL.RawQuery
	// Bounded: an unbounded tail on a chatty stack would stream megabytes into a browser tab.
	tail := clamp(numParam(q, "tail", 200), 1, 2000, 200)
	svc, hasSvc := query(q, "service")
	if hasSvc && !serviceRe.MatchString(svc) {
		writeError(w, 400, `"`+svc+`" is not a valid compose service name`)
		return nil
	}
	for _, key := range []string{"since", "until"} {
		if raw, ok := query(q, key); ok && logsDuration(raw) == "" {
			writeError(w, 400, key+`="`+raw+`" is not a duration (10m, 2h, 1h30m) or an RFC3339 time`)
			return nil
		}
	}
	_, hasUntil := query(q, "until")
	if orchestratorOf(st) == spec.Swarm && hasUntil {
		// Refused rather than dropped: a narrowed read silently widened is a wrong answer.
		writeError(w, 400, "`until` is not supported for a swarm stack (docker service logs has no --until)")
		return nil
	}
	ts, _ := query(q, "timestamps")
	since, _ := query(q, "since")
	until, _ := query(q, "until")
	opts := compose.LogsOptions{Timestamps: ts == "1", Since: logsDuration(since), Until: logsDuration(until)}
	res, err := compose.ComposeLogs(st, s.runnerFor(st, dep.Dir, nil), tail, svc, opts)
	if err != nil {
		return err
	}
	// Application logs are the one place a secret shows up as free text. Redact by content before
	// it leaves the host, and mask this process's own token explicitly.
	raw := res.Stdout
	if res.Stderr != "" {
		raw += "\n" + res.Stderr
	}
	text := redact.RedactText(raw, append([]string{s.opts.Token}, s.secretValues()...)...)
	lines := 0
	if text != "" {
		lines = strings.Count(text, "\n") + 1
	}
	var service any
	if hasSvc {
		service = svc
	}
	writeJSON(w, 200, jsonx.O(
		"stack", st.Stack,
		"tail", tail,
		"service", service,
		"timestamps", ts == "1",
		"since", nullable(logsDuration(since)),
		"until", nullable(logsDuration(until)),
		"lines", lines,
		"ok", res.OK,
		"text", text,
	))
	return nil
}

// deploymentSource is GET /api/deployments/:id/source — restricted exactly like a named spec's.
func (s *Server) deploymentSource(w http.ResponseWriter, r *http.Request, dep *registry.Deployment) error {
	if !s.authed(r) {
		writeJSON(w, 200, jsonx.O("id", dep.ID, "specName", nullable(dep.SpecName), "sourceWithheld", true))
		return nil
	}
	specYaml, comp, err := s.registry.Source(dep.ID)
	if err != nil {
		return err
	}
	var composeAny any
	if comp != nil {
		composeAny = *comp
	}
	writeJSON(w, 200, jsonx.O("id", dep.ID, "specName", nullable(dep.SpecName), "spec", specYaml, "compose", composeAny))
	return nil
}

// containerAction is POST …/containers/:name/(start|stop|restart): one container, matched against
// the containers this deployment OWNS — never trusted from the request.
func (s *Server) containerAction(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, wanted, action string, who *auth.Principal, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	orch := orchestratorOf(st)
	rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: st.Stack, Runner: s.host, Challenge: inspect.Unknown, Orchestrator: orch})
	if !rt.Reachable {
		writeError(w, 503, "docker did not answer, so ownership of this container could not be checked")
		return nil
	}
	c := findContainer(rt, wanted)
	if c == nil {
		writeJSON(w, 404, jsonx.O("error", `no container "`+wanted+`" in deployment `+dep.ID, "containers", containerNames(rt)))
		return nil
	}
	if c.Remote {
		writeJSON(w, 409, jsonx.O("error", `container "`+c.Name+`" runs on node `+nodeOf(c)+", which this control plane cannot reach directly. Redeploy the stack, or act on the worker itself.", "node", c.Node))
		return nil
	}
	// Seconds docker waits before SIGKILL. Clamped: an unbounded value is a request that never returns.
	graceRaw := numParam(r.URL.RawQuery, "grace", 10)
	grace := 10.0
	if js.IsFinite(graceRaw) {
		grace = clamp(math.Trunc(graceRaw), 1, 120, 10)
	}
	timing := ""
	if action != "start" {
		timing = " -t " + js.NumberString(grace)
	}
	res := s.host.Run("docker "+action+timing+" "+compose.Shq(c.Name), exec.RunOptions{Label: "docker " + action + " " + c.Name})
	by := terminal.ActorOf(*who)
	if !res.OK {
		out := strings.TrimSpace(res.Stderr)
		if out == "" {
			out = strings.TrimSpace(res.Stdout)
		}
		msg := firstLine(out)
		if msg == "" {
			msg = "exit " + js.NumberString(float64(res.Code))
		}
		writeJSON(w, 409, jsonx.O("error", "docker "+action+" failed: "+msg, "container", c.Name, "action", action))
		return nil
	}
	name := map[string]string{"restart": "container.restarted", "stop": "container.stopped", "start": "container.started"}[action]
	s.bus.Emit(name, jsonx.O("stack", st.Stack, "deployment", dep.ID, "container", c.Name, "service", c.Service, "action", action, "by", by))
	// Readiness follows the intent: a start/restart raises exactly the question a watch answers; a
	// STOP is deliberate, and a watch left running would report stack.failed about it.
	if action == "stop" {
		s.readiness.Cancel(st.Stack)
	} else {
		s.readiness.Start(st.Stack, s.runnerFor(st, dep.Dir, s.ctx), readiness.StartOptions{Restart: true, Emit: true, Orchestrator: orch})
	}
	note := "Docker has started it. Whether it comes back healthy is what readiness reports."
	if action == "stop" {
		note = "Stopped. It stays stopped until something starts it — a deploy, or Start here."
	}
	writeJSON(w, 200, jsonx.O("container", c.Name, "service", c.Service, "action", action, "by", by, "note", note))
	return nil
}

func findContainer(rt inspect.Runtime, wanted string) *inspect.ContainerInfo {
	for i := range rt.Containers {
		if rt.Containers[i].ID == wanted || rt.Containers[i].Name == wanted {
			return &rt.Containers[i]
		}
	}
	return nil
}

func containerNames(rt inspect.Runtime) []string {
	out := make([]string, 0, len(rt.Containers))
	for _, c := range rt.Containers {
		out = append(out, c.Name)
	}
	return out
}

func nodeOf(c *inspect.ContainerInfo) string {
	if c.Node == nil {
		return "?"
	}
	return *c.Node
}

// deploymentRuntime is GET …/runtime: containers, routes and findings, read live.
func (s *Server) deploymentRuntime(w http.ResponseWriter, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	// Gathered once and passed in: the router-name collision check is global across the daemon.
	all := inspect.AllTraefikRouters(s.host)
	rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: st.Stack, Runner: s.host, Challenge: inspect.DetectChallenge(s.host), AllRouters: all.ByName, Orchestrator: orchestratorOf(st)})
	var asleep any
	if dep.Sleep != nil {
		asleep = dep.Sleep
	}
	out := jsonx.O("id", dep.ID, "orchestrator", nullable(string(orchestratorOf(st))), "asleep", asleep)
	writeJSON(w, 200, append(out, spread(rt)...))
	return nil
}

// deploymentReadiness is GET …/readiness: is it SERVING yet — poll, or ?wait=<seconds>.
func (s *Server) deploymentReadiness(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	q := r.URL.RawQuery
	num := func(key string, fallback, max float64) float64 {
		raw := numParam(q, key, 0)
		if js.IsFinite(raw) && raw > 0 {
			if raw < max {
				return raw
			}
			return max
		}
		return fallback
	}
	waitMs := int64(num("wait", 0, 60) * 1000)
	var timeoutMs int64
	if _, has := query(q, "timeout"); has {
		timeoutMs = int64(num("timeout", 180, 3600) * 1000)
	}
	refresh, _ := query(q, "refresh")
	existing, found := s.readiness.Get(st.Stack)
	if !found || (existing.State != readiness.Watching && refresh == "1") {
		// SILENT: a watch a read started must not announce itself to every notifier.
		s.readiness.Start(st.Stack, s.runnerFor(st, dep.Dir, s.ctx), readiness.StartOptions{TimeoutMs: timeoutMs, Restart: true, Emit: false, Orchestrator: orchestratorOf(st)})
	}
	var snap readiness.StackReadiness
	var ok bool
	if waitMs > 0 {
		snap, ok = s.readiness.Wait(st.Stack, waitMs)
	} else {
		snap, ok = s.readiness.Get(st.Stack)
	}
	out := jsonx.O("id", dep.ID)
	if ok {
		out = append(out, spread(snap)...)
	} else {
		out = append(out, jsonx.KV{K: "stack", V: st.Stack}, jsonx.KV{K: "state", V: "watching"}, jsonx.KV{K: "containers", V: []any{}})
	}
	writeJSON(w, 200, out)
	return nil
}
