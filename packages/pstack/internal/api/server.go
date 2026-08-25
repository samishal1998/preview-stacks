// Server: the control plane's remote surface. The route table is api.ts's header comment; the
// rules that matter are in AGENTS.md.
//
// THIS API MUST NEVER MANAGE THE STACK IT RUNS IN (invariant 12). VARIABLES are merged from the
// request's `?query` (and a PUT's `env`) over the process env, once, at resolve time. SECURITY:
// every route but health/login/logout/bootstrap/sso is behind the principal gate; responses are
// built field by field and never echo a resolved Stack.Env.
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/hostvars"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/readiness"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registries"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

// Options configure a server.
type Options struct {
	// DataDir is the registry root; deployments live under <DataDir>/deployments.
	DataDir string
	Port    int
	Host    string
	Token   string
	// RoutingDir is Traefik's dynamic-config directory. Default: the in-container mount path
	// /etc/traefik/dynamic; on a host-side run, <dataDir>/control/traefik-dynamic.
	RoutingDir string
	// RegistryDir is the DOCKER_CONFIG directory holding private-registry credentials. Default:
	// $DOCKER_CONFIG, else /docker-config.
	RegistryDir string
	// TerminalArgv opens a container shell. Default: docker exec -i <id> <shell>. Injectable for
	// the same reason Runner is: the machine this is developed on has no Docker.
	TerminalArgv func(containerID, shell string) []string
	// Readiness tuning; zero means the defaults (2s / 180s / 3 restarts).
	ReadinessPollMs    int64
	ReadinessTimeoutMs int64
	// ReadinessRestartLoop is the crash-loop threshold. Raise it on a SWARM host: without
	// `depends_on`, a dependent legitimately restarts while its database converges.
	ReadinessRestartLoop int64
	// Domain is the preview domain (PSTACK_DOMAIN): share links and the SSO callback are built on
	// control.<domain>; without it the request's own origin is used.
	Domain string
	// MetricsURL is Traefik's Prometheus endpoint (PSTACK_TRAEFIK_METRICS). What sleep.idle reads.
	MetricsURL string
	// Scheduler tuning — a minute on a host, milliseconds in a test.
	SchedulerTickMs int64
	SchedulerFetch  scheduler.Fetcher
	// SSO: how long a half-finished sign-in is remembered (default 300s) and how long a provider's
	// discovery document is trusted (default 1h). Zero = default.
	SSOStateTTLS     int64
	SSODiscoveryTTLS int64
	// Bus defaults to events.Default.
	Bus *events.Bus
	// Env is the process environment the server hands to hooks and reads variables from; nil
	// means os.Environ().
	Env map[string]string
	// Log receives the scheduler's and the SSO adoption lines (stderr on a host).
	Log func(string)
	// Version is what /api/health reports.
	Version string
	// UIHTML and ShareHTML are the two embedded pages.
	UIHTML    string
	ShareHTML string
}

// Server is one running control plane.
//
// Owner of writeMu: the registry/spec mutations that were racy check-then-acts in the reference
// (PUT/DELETE deployment, DELETE spec, the sleep record). Everything else has its own owner.
type Server struct {
	opts       Options
	env        map[string]string
	jobs       *jobs.Registry
	registry   *registry.Registry
	specs      *specs.SpecStore
	routing    *routing.RoutingStore
	registries *registries.RegistryAuthStore
	store      *store.Store
	auth       *auth.Auth
	hooks      *webhooks.Webhooks
	terminals  *terminal.Audit
	hostVars   *hostvars.HostVars
	readiness  *readiness.Watcher
	dispatcher *notify.Dispatcher
	detach     func()
	host       exec.Runner
	sleepIndex *scheduler.SleepIndex
	meter      *scheduler.TrafficMeter
	scheduler  *scheduler.Scheduler
	ssoClient  *sso.Client
	bus        *events.Bus
	ssoTTL     int64

	writeMu sync.Mutex

	// followers are the live `compose logs --follow` children, so stopping the server stops them.
	followMu  sync.Mutex
	followers map[int]func()
	followSeq int
	// streams counts the SSE/WS handlers in flight; Stop waits for them.
	streams sync.WaitGroup
	// terms are the open terminal sockets, closed with 1001 on Stop.
	termMu  sync.Mutex
	terms   map[int]func()
	termSeq int

	ctx    context.Context
	cancel context.CancelFunc
	http   *http.Server
	ln     net.Listener
	reidx  chan struct{}
}

// New wires a server. Nothing listens until Start.
func New(o Options) (*Server, error) {
	if o.Bus == nil {
		o.Bus = events.Default
	}
	if o.Log == nil {
		o.Log = func(l string) { fmt.Fprintln(os.Stderr, l) }
	}
	env := o.Env
	if env == nil {
		env = exec.Environ()
	}
	st, err := store.Open(o.DataDir)
	if err != nil {
		return nil, err
	}
	routingDir := o.RoutingDir
	if routingDir == "" {
		routingDir = "/etc/traefik/dynamic"
	}
	registryDir := o.RegistryDir
	if registryDir == "" {
		if v, ok := env["DOCKER_CONFIG"]; ok {
			registryDir = v
		} else {
			registryDir = "/docker-config"
		}
	}
	if o.TerminalArgv == nil {
		o.TerminalArgv = terminal.ExecArgv
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		opts:       o,
		env:        env,
		jobs:       jobs.New(o.Bus),
		registry:   registry.New(o.DataDir),
		specs:      specs.New(o.DataDir),
		routing:    routing.New(routingDir),
		registries: registries.New(registryDir),
		store:      st,
		auth:       auth.New(st),
		terminals:  terminal.NewAudit(st),
		hostVars:   hostvars.New(st),
		readiness:  readiness.New(readiness.Options{PollMs: o.ReadinessPollMs, TimeoutMs: o.ReadinessTimeoutMs, RestartLoop: o.ReadinessRestartLoop, Bus: o.Bus}),
		sleepIndex: scheduler.NewSleepIndex(),
		ssoClient:  sso.NewClient(nil),
		bus:        o.Bus,
		followers:  map[int]func(){},
		terms:      map[int]func(){},
		ctx:        ctx,
		cancel:     cancel,
		reidx:      make(chan struct{}, 1),
	}
	s.registry.Env = env
	s.hooks = webhooks.New(st, notify.PublicConfig)
	s.dispatcher = notify.New(s.hooks)
	// The bus is a singleton and this listener is per-server: the subscription ends with the server,
	// or one event fans out into every database any server ever opened.
	s.detach = o.Bus.On(func(e events.Event) { s.dispatcher.Dispatch(e) })
	// For host-level queries that belong to no deployment. BaseEnv is not optional: a runner built
	// without it hands bash an empty env — no PATH, no DOCKER_HOST — and the DELETE guard would
	// refuse forever.
	s.host = exec.New(exec.Options{Level: exec.Quiet, BaseEnv: env, Ctx: ctx})
	s.ssoTTL = 5 * 60
	if o.SSOStateTTLS > 0 {
		s.ssoTTL = o.SSOStateTTLS
	}
	if o.SSODiscoveryTTLS > 0 {
		s.ssoClient.SetDiscoveryTTL(o.SSODiscoveryTTLS * 1000)
	}
	s.meter = scheduler.NewTrafficMeter(o.MetricsURL, o.SchedulerFetch, o.Log)
	s.scheduler = scheduler.New(scheduler.Deps{
		List: func() ([]scheduler.Candidate, error) {
			metas, err := s.registry.List()
			if err != nil {
				return nil, err
			}
			out := make([]scheduler.Candidate, 0, len(metas))
			for _, m := range metas {
				out = append(out, scheduler.Candidate{ID: m.ID, Kind: m.Kind, Asleep: m.Sleep != nil})
			}
			return out, nil
		},
		Resolve: func(id string) (*spec.Stack, error) { return s.resolveDep(id, nil) },
		Runtime: func(st *spec.Stack) (scheduler.RuntimeView, error) {
			rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: st.Stack, Runner: s.host, Challenge: inspect.Unknown, Orchestrator: orchestratorOf(st)})
			view := scheduler.RuntimeView{Reachable: rt.Reachable, StartedAt: []*int64{}, Routers: []string{}}
			for _, c := range rt.Containers {
				view.StartedAt = append(view.StartedAt, c.StartedAt)
			}
			for _, r := range rt.Routes {
				view.Routers = append(view.Routers, r.Router)
			}
			return view, nil
		},
		IsBusy: s.jobs.IsBusy,
		Sleep: func(id string, st *spec.Stack, reason string) {
			dep, err := s.registry.Get(id)
			if err == nil && dep != nil {
				s.startLifecycle(id, dep, st, jobs.Sleep, lifecycleOptions{By: "scheduler", Reason: reason})
			}
		},
		Meter:  s.meter,
		Log:    o.Log,
		TickMs: o.SchedulerTickMs,
	})
	s.reindex()
	return s, nil
}

func orchestratorOf(st *spec.Stack) spec.Orchestrator {
	if st == nil || st.Compose == nil {
		return ""
	}
	return st.Compose.Orchestrator
}

// resolveDep is every registry.Resolve on this server, so a spec referencing ${vars.*}/${secrets.*}
// resolves identically on every route.
func (s *Server) resolveDep(id string, vars map[string]string) (*spec.Stack, error) {
	hv, sec, err := s.hostVars.ResolveMaps()
	if err != nil {
		return nil, err
	}
	return s.registry.Resolve(id, vars, &spec.HostValues{Vars: hv, Secrets: sec})
}

func (s *Server) hostValues() *spec.HostValues {
	hv, sec, _ := s.hostVars.ResolveMaps()
	if hv == nil {
		hv = map[string]string{}
	}
	if sec == nil {
		sec = map[string]string{}
	}
	return &spec.HostValues{Vars: hv, Secrets: sec}
}

func (s *Server) secretValues() []string {
	v, _ := s.hostVars.SecretValues()
	return v
}

// scrubbedSink scrubs host-secret VALUES from every job log line, at the one choke point all of
// up/down/verify write through: by-name redaction cannot catch a value, only content can.
type scrubbedSink struct {
	inner   log.Sink
	secrets []string
}

func (s scrubbedSink) Emit(level log.Level, message string) {
	s.inner.Emit(level, redact.RedactText(message, s.secrets...))
}

func (s *Server) scrub(inner log.Sink) log.Sink {
	secrets := s.secretValues()
	if len(secrets) == 0 {
		return inner
	}
	return scrubbedSink{inner: inner, secrets: secrets}
}

// runnerFor is a runner scoped to one deployment's directory. cwd is load-bearing: compose must be
// invoked from the deployment directory, and axis hooks run there too (inline shell or absolute
// paths only). ctx is present only for a job's own runner — the handle POST /api/jobs/:id/cancel
// pulls.
func (s *Server) runnerFor(st *spec.Stack, dir string, ctx context.Context) exec.Runner {
	if ctx == nil {
		ctx = context.Background()
	}
	return exec.New(exec.Options{Level: exec.Quiet, Cwd: dir, BaseEnv: exec.Merge(s.env, st.Env), Ctx: ctx})
}

// composeProjects: compose project names docker knows about, or nil when it could not answer.
// Best-effort by design — an old compose that cannot emit JSON yields nil so the response says
// "unknown" instead of confidently claiming nothing is running.
func (s *Server) composeProjects() map[string]bool {
	r := s.host.Run("docker compose ls --all --format json", exec.RunOptions{})
	if !r.OK {
		return nil
	}
	raw := strings.TrimSpace(r.Stdout)
	if raw == "" {
		raw = "[]"
	}
	var rows []struct {
		Name string `json:"Name"`
	}
	if err := jsonUnmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	names := map[string]bool{}
	for _, p := range rows {
		if p.Name != "" {
			names[p.Name] = true
		}
	}
	// Swarm stacks too. A daemon that is not a manager answers with an error, which here means "no
	// swarm stacks" — the compose answer above is still good.
	if st := s.host.Run("docker stack ls --format '{{.Name}}'", exec.RunOptions{}); st.OK {
		for _, n := range strings.Split(st.Stdout, "\n") {
			if n = strings.TrimSpace(n); n != "" {
				names[n] = true
			}
		}
	}
	return names
}

// containersFor: container ids belonging to a project, read straight from the labels. Used only by
// DELETE, where a wrong answer orphans containers: -a counts a stopped-but-present container, and
// nil means "docker could not answer" — never "empty".
func (s *Server) containersFor(stackName string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, label := range []string{"com.docker.compose.project=" + stackName, swarm.StackLabel + "=" + stackName} {
		r := s.host.Run("docker ps -aq --filter "+compose.Shq("label="+label), exec.RunOptions{})
		if !r.OK {
			return nil
		}
		for _, l := range strings.Split(r.Stdout, "\n") {
			if l = strings.TrimSpace(l); l != "" && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// ── sleep / wake ────────────────────────────────────────────────────────────────────────────

func (s *Server) reindex() {
	metas, err := s.registry.List()
	if err != nil {
		return
	}
	entries := []scheduler.SleepEntry{}
	for _, m := range metas {
		if m.Sleep != nil {
			entries = append(entries, scheduler.SleepEntry{ID: m.ID, Hosts: m.Sleep.Hosts, Rules: m.Sleep.Rules})
		}
	}
	s.sleepIndex.Rebuild(entries)
}

// clearSleep forgets the sleep record, reading the current one rather than the copy a request
// resolved earlier.
func (s *Server) clearSleep(id string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now, err := s.registry.Get(id)
	if err != nil || now == nil || now.Sleep == nil {
		return
	}
	_ = s.registry.SetSleep(id, nil)
	s.reindex()
}

// WakeRetryMs: after a failed wake, how long a request to the hostname waits before trying again.
const WakeRetryMs = 60_000

type lifecycleOptions struct {
	Verify *bool
	Force  *bool
	By     string
	Reason string
	// ReadinessTimeoutMs is the deadline of the watch an `up`/`wake` hands off to; 0 means the
	// watcher default. Set from `?timeout=` by the POST route only — the scheduler and the wake path
	// deliberately keep the default.
	ReadinessTimeoutMs int64
}

// startLifecycle starts a lifecycle job. ONE place, because three callers start them — the POST
// route, a request to a sleeping hostname (wake), and the scheduler (sleep) — and the per-stack
// lock, the readiness hand-off, the sleep record and the events must agree whoever asked.
func (s *Server) startLifecycle(id string, dep *registry.Deployment, st *spec.Stack, action jobs.Action, o lifecycleOptions) (jobs.Job, bool) {
	hostSecrets := s.secretValues()
	scrub := func(text string) string {
		if len(hostSecrets) == 0 {
			return text
		}
		return redact.RedactText(text, hostSecrets...)
	}
	orchestrator := orchestratorOf(st)
	work := func(rawSink log.Sink, ctx context.Context) (stack.Outcome, error) {
		sink := s.scrub(rawSink)
		runner := s.runnerFor(st, dep.Dir, ctx)
		switch action {
		case jobs.Up, jobs.Wake:
			if action == jobs.Wake {
				s.bus.Emit("stack.woken", jsonx.O("stack", st.Stack, "deployment", id, "by", o.By))
			}
			outcome := stack.Up(st, runner, sink)
			// Readiness picks up exactly where the job stops: `compose up -d` returns once the
			// containers are CREATED. Only after a success, and NOT with the job's runner — a watch
			// outlives the deploy that started it.
			if outcome.OK {
				s.readiness.Start(st.Stack, s.runnerFor(st, dep.Dir, s.ctx), readiness.StartOptions{TimeoutMs: o.ReadinessTimeoutMs, Restart: true, Emit: true, Orchestrator: orchestrator})
				s.clearSleep(id)
			}
			return outcome, nil
		case jobs.Verify:
			return stack.Verify(st, runner, sink), nil
		case jobs.Sleep:
			// The hostnames BEFORE teardown: once the containers are gone nothing on the host
			// remembers which Host() rules were this stack's.
			rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: st.Stack, Runner: s.host, Challenge: inspect.Unknown, Orchestrator: orchestrator})
			all := []string{}
			for _, r := range rt.Routes {
				all = append(all, r.Hosts...)
			}
			hosts, rules := scheduler.SplitHosts(all)
			previous := dep.Sleep
			s.readiness.Cancel(st.Stack)
			outcome := stack.Sleep(st, runner, sink)
			if outcome.OK {
				if len(hosts) == 0 && previous != nil {
					hosts = previous.Hosts
				}
				if len(rules) == 0 && previous != nil {
					rules = previous.Rules
				}
				record := &registry.SleepRecord{Since: time.Now().UnixMilli(), Reason: o.Reason, Hosts: dedupe(hosts), Rules: dedupe(rules)}
				s.writeMu.Lock()
				_ = s.registry.SetSleep(id, record)
				s.reindex()
				s.writeMu.Unlock()
				if len(record.Hosts) > 0 || len(record.Rules) > 0 {
					names := append([]string{}, record.Hosts...)
					for _, r := range record.Rules {
						names = append(names, "/"+r+"/")
					}
					sink.Emit(log.Info, "asleep — a request to "+strings.Join(names, ", ")+" wakes it")
				} else {
					sink.Emit(log.Info, "asleep — no hostnames were found on its containers, so only POST …/wake brings it back")
				}
				s.bus.Emit("stack.slept", jsonx.O("stack", st.Stack, "deployment", id, "reason", o.Reason, "hosts", record.Hosts))
			}
			return outcome, nil
		}
		// Teardown makes every pending readiness question moot.
		s.readiness.Cancel(st.Stack)
		verify, force := true, false
		if o.Verify != nil {
			verify = *o.Verify
		}
		if o.Force != nil {
			force = *o.Force
		}
		outcome := stack.Down(st, runner, stack.DownOptions{NoVerify: !verify, Force: force}, sink)
		// Torn down is not asleep: nothing should wake it.
		s.clearSleep(id)
		return outcome, nil
	}
	return s.jobs.Start(st.Stack, action, work, scrub)
}

func dedupe(xs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// wakeFor answers a request for a sleeping stack's hostname: starts the wake (once) and answers the
// page that polls until Traefik routes the hostname to the app again. Returns false when the
// hostname is nobody's.
func (s *Server) wakeFor(w http.ResponseWriter, hostname string) bool {
	// The control plane's own hostnames are never a preview's to wake.
	if d := s.opts.Domain; d != "" && (hostname == "control."+d || hostname == "api."+d) {
		return false
	}
	id := s.sleepIndex.Find(hostname)
	if id == "" {
		return false
	}
	dep, err := s.registry.Get(id)
	if err != nil || dep == nil || dep.Sleep == nil {
		s.reindex() // the index was stale — a hand edit is not a change it sees
		return false
	}
	page := func(state scheduler.WakeState, stackName, errText string) bool {
		h := w.Header()
		h.Set("content-type", "text/html; charset=utf-8")
		h.Set("cache-control", "no-store")
		h.Set("retry-after", "5")
		h.Set("x-pstack-wake", "1")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(scheduler.WakePage(hostname, stackName, state, errText)))
		return true
	}
	st, err := s.resolveDep(id, nil)
	if err != nil {
		return page(scheduler.Failed, id, "the deployment cannot be resolved: "+err.Error())
	}
	if s.jobs.IsBusy(st.Stack) {
		return page(scheduler.Busy, st.Stack, "")
	}
	// The last wake failed? Say so instead of spinning forever — and do not try again for a minute.
	var last *jobs.Job
	for _, j := range s.jobs.List() {
		if j.Stack == st.Stack && (j.Action == jobs.Wake || j.Action == jobs.Up) {
			jj := j
			last = &jj
			break
		}
	}
	failedWhy := func(j *jobs.Job) string {
		if j.Outcome != nil {
			for _, step := range j.Outcome.Steps {
				if !step.OK {
					msg := "exit " + fmt.Sprint(step.Code)
					if step.Message != nil {
						msg = *step.Message
					}
					return string(step.Phase) + " " + step.Axis + ": " + msg
				}
			}
		}
		if j.Error != nil {
			return *j.Error
		}
		return ""
	}
	if last != nil && last.State == jobs.Failed {
		ended := last.StartedAt
		if last.EndedAt != nil {
			ended = *last.EndedAt
		}
		if time.Now().UnixMilli()-ended < WakeRetryMs {
			return page(scheduler.Failed, st.Stack, failedWhy(last))
		}
	}
	if _, ok := s.startLifecycle(id, dep, st, jobs.Wake, lifecycleOptions{By: "request:" + hostname, Reason: "request to " + hostname}); !ok {
		return page(scheduler.Busy, st.Stack, "")
	}
	if last != nil && last.State == jobs.Failed {
		return page(scheduler.Failed, st.Stack, failedWhy(last))
	}
	return page(scheduler.Waking, st.Stack, "")
}

// ── listening ───────────────────────────────────────────────────────────────────────────────

// Start binds and serves in the background. Port 0 picks a free port; Addr() says which.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port)))
	if err != nil {
		return err
	}
	s.ln = ln
	// WriteTimeout stays 0: the log follower lives for an hour. Per-request deadlines are set where
	// a route needs one.
	s.http = &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 240 * time.Second}
	s.scheduler.Start()
	go s.reindexLoop()
	go func() { _ = s.http.Serve(ln) }()
	return nil
}

// Addr is the bound address.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

func (s *Server) reindexLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.reindex()
		}
	}
}

// Stop is the reference's `server.stop`: stop listening, close the streams, detach from the bus,
// stop the followers, the readiness watches, the scheduler and the reindex loop. Jobs derive from
// context.Background and outlive this on purpose.
func (s *Server) Stop() {
	s.detach()
	s.cancel()
	if s.http != nil {
		ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.http.Shutdown(ctx)
		done()
		_ = s.http.Close()
	}
	s.termMu.Lock()
	terms := s.terms
	s.terms = map[int]func(){}
	s.termMu.Unlock()
	for _, closeConn := range terms {
		closeConn()
	}
	s.followMu.Lock()
	followers := s.followers
	s.followers = map[int]func(){}
	s.followMu.Unlock()
	for _, stop := range followers {
		stop()
	}
	s.streams.Wait()
	s.readiness.StopAll()
	s.scheduler.Stop()
	_ = s.store.Close()
}

// ── the handler ─────────────────────────────────────────────────────────────────────────────

var shareViewRe = regexp.MustCompile(`^/deployments/[^/]+/public-logs-view/?$`)

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()

	// wake-on-call FIRST: a request that reached this process through the catch-all router carries
	// a PREVIEW hostname, and if it belongs to a sleeping stack the whole request is the visitor's.
	// Cheap when nothing sleeps: one lookup.
	if s.sleepIndex.Size() > 0 {
		if h := requestHost(r); h != "" && s.wakeFor(w, h) {
			return
		}
	}

	// The UI: a single embedded document, so there is no filesystem lookup and no path traversal
	// to contain. Every non-/api path serves it.
	if !strings.HasPrefix(path, "/api/") {
		page := s.opts.UIHTML
		if shareViewRe.MatchString(path) {
			page = s.opts.ShareHTML
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(page))
		return
	}

	if path == "/api/health" {
		s.health(w, r)
		return
	}

	// Pre-gate: login, logout, bootstrap, the SSO round trip. These run before the gate, or nobody
	// could ever log in.
	if done := s.preGate(w, r, path); done {
		return
	}

	// THE GATE: every route below requires a principal, reads included.
	who := s.principal(r)
	if who == nil {
		writeError(w, 401, "unauthorized")
		return
	}
	if !shareAllows(who, r.Method, path) {
		writeError(w, 403, "this link is read-only and scoped to one deployment")
		return
	}
	// A share link gets the STORED variables only.
	vars := map[string]string{}
	if who.Kind != auth.KindShare {
		vars = varsFrom(r.URL.RawQuery)
	}
	if err := s.routes(w, r, path, who, vars); err != nil {
		s.fail(w, err)
	}
}

// fail maps a domain error to its status. A missing ${VAR} and a malformed spec are both the
// caller's problem, and both name the offending field — 400 with that text beats a 500 with none.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var se *spec.Error
	if errors.As(err, &se) {
		writeError(w, 400, "spec: "+se.Msg)
		return
	}
	switch {
	case registry.IsError(err), routing.IsError(err), registries.IsError(err), sso.IsError(err),
		hostvars.IsError(err), auth.IsError(err), specs.IsError(err), webhooks.IsError(err), notify.IsError(err):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 500, err.Error())
	}
}
