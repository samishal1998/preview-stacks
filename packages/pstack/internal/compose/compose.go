// Package compose is the Docker Compose invocation.
//
// The one non-obvious rule, and the reason this file exists instead of an inline string:
//
//	`up` passes only the SELECTED profiles. `down` passes EVERY profile in the spec.
//
// Compose treats a service whose profile is not enabled as absent. So a stack brought up with
// `--profile backend` and torn down with the same flag leaves the *other* profiles' resources
// behind — most visibly the project's default network, which then accumulates one dead
// `<stack>_default` per PR forever. Enumerating every profile on `down` costs nothing (passing a
// profile with no matching service is a no-op) and is the difference between a clean box and a
// slow leak.
//
// Second rule: `down -v` removes containers, volumes and networks — but NOT images. Per-stack
// images are removed by an explicit axis (see examples/), not by compose.
//
// Third, since 0.26.0: every function here DISPATCHES on `spec.compose.orchestrator`. Under `swarm`
// the same verbs become `docker stack deploy` / `docker stack rm` / `docker service logs` /
// `docker stack ps` (built in swarm), and the file passed is the converted one. The callers in
// stack and api do not know which; that is the point of keeping one seam.
//
// `sleep` is the fourth verb: `down` WITHOUT `-v` (swarm: `stack rm`, which never touches volumes).
// The containers and networks go, the data stays, the axes are not consulted — a sleeping preview is
// one that can be brought back by `up` alone.
//
// Every verb returns an error only when the derived compose file could not be produced (a
// *spec.Error from autolabel — the TS threw the same); a command that ran and failed is a Result
// with OK false, never an error.
//
// Logging: on a host that ships logs to Loki, `up` checks the plugin before it deploys anything
// autolabel gave the loki driver. Under compose a missing or disabled plugin fails every logged
// container's create with dockerd's error, which names the driver and not the fix — so the check
// answers a Result with OK false (the rule above) and `up` never runs. Under swarm the scheduler
// keeps logged tasks off a node without the plugin, so the deploy runs and the log names those
// nodes; if none qualifies, readiness times out and those lines are the only thing saying why. The
// install line always goes to `note`, never into the Result: stack.Up keeps the first 300 runes of
// the Result's first line, and the line alone is longer.
package compose

import (
	"os"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// Shq single-quotes a value for bash — see swarm.Shq, where it lives because of the import graph.
func Shq(v string) string { return swarm.Shq(v) }

// LogsOptions is the rest of what `logs` can be asked for.
type LogsOptions = swarm.LogsOptions

// baseFor is the prefix every subcommand shares.
//
// `-p <stack>` is the namespacing primitive: it prefixes containers, networks and volumes, so two
// stacks from the same compose file never collide. The `-f` list comes from filesFor, which
// substitutes the augmented file when the submitted one asked for generated labels.
func baseFor(st *spec.Stack, r exec.Runner) (string, error) {
	files, _, err := filesFor(st, r)
	if err != nil {
		return "", err
	}
	return baseOf(st, files), nil
}

// baseOf is baseFor over files already resolved. ComposeUp resolves them itself, once, because it
// also reads what the materialization did.
func baseOf(st *spec.Stack, files []string) string {
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = "-f " + Shq(f)
	}
	return "docker compose -p " + Shq(st.Stack) + " " + strings.Join(parts, " ")
}

// filesFor resolves which compose files to pass to `-f`, in order, generating the augmented one when
// the submitted file needs it: `pstack.routing.*` labels, swarm, or a host that ships logs to Loki.
// m is what the materialization did, for the job log; nil under --dry-run.
//
// Called by EVERY subcommand, not just `up`. The generated labels are derived from the resolved spec,
// so regenerating each time is what stops `up` and `down` disagreeing about what a router was called —
// and compose reads the file on every subcommand anyway.
//
// `runner.Cwd()` is the deployment directory (the registry sets it); the CLI sets none and docker runs
// from the shell's directory, which is then where the compose file is read from and the derived one
// written (beside it — see autolabel.MaterializeCompose).
func filesFor(st *spec.Stack, r exec.Runner) (files []string, m *autolabel.MaterializeResult, err error) {
	c := st.Compose
	// Nothing is written under --dry-run: a dry run must not have side effects, and the point of it is
	// to show what WOULD happen.
	if r.DryRun() {
		return append([]string{c.File}, c.Overlays...), nil, nil
	}
	// The deployment directory under the API; the shell's directory under the CLI — which is also
	// where docker itself will resolve the `-f` path, so the two cannot disagree.
	dir := r.Cwd()
	if dir == "" {
		dir, _ = os.Getwd()
	}
	m, err = autolabel.MaterializeCompose(autolabel.MaterializeArgs{Dir: dir, Spec: st, Runner: r})
	if err != nil {
		return nil, nil, err
	}
	return append([]string{m.File}, c.Overlays...), m, nil
}

func isSwarm(st *spec.Stack) bool {
	return st.Compose != nil && st.Compose.Orchestrator == spec.Swarm
}

func profileArgs(profiles []string) string {
	parts := make([]string, len(profiles))
	for i, p := range profiles {
		parts[i] = "--profile " + Shq(p)
	}
	return strings.Join(parts, " ")
}

// ComposeEnv is the environment every compose subcommand runs with.
//
// The wildcard-subdomain rules go in here rather than only into `up` because compose interpolates the
// compose FILE on every subcommand. A label that reads `${PSTACK_WILD_BACKEND}` would otherwise make
// `down`, `logs` and `ps` warn about an unset variable and substitute an empty string — and on `down`
// that means compose is reasoning about a *differently-labelled* project than the one `up` created.
func ComposeEnv(st *spec.Stack, extra map[string]string) map[string]string {
	var subs []spec.SubdomainRoute
	if st.Compose != nil {
		subs = st.Compose.Subdomains
	}
	return exec.Merge(st.Env, spec.SubdomainEnv(subs), extra, map[string]string{"STACK": st.Stack})
}

// ComposeUp is `compose up` / `stack deploy`. note receives the job-log lines: what the swarm
// conversion changed, which services kept their own `logging:`, and what the loki plugin check found.
// nil drops them.
func ComposeUp(st *spec.Stack, r exec.Runner, extraEnv map[string]string, note func(string)) (exec.Result, error) {
	c := st.Compose
	files, m, err := filesFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	if note == nil {
		note = func(string) {}
	}
	// m is nil under --dry-run: nothing was materialized, so nothing is said and no plugin is checked.
	logged := m != nil && len(m.Logged) > 0
	if m != nil {
		for _, n := range m.Notes {
			note("swarm: " + n)
		}
		for _, n := range m.LogNotes {
			note(n)
		}
	}
	if isSwarm(st) {
		if logged {
			// A node whose plugins could not be read is unknown, not a node without the plugin.
			info := swarm.SwarmInfo(r)
			swarm.MarkLokiPlugins(r, &info)
			var missing []string
			unread := false
			for _, n := range info.Nodes {
				switch {
				case n.LokiPlugin == nil:
					unread = true
				case !*n.LokiPlugin:
					missing = append(missing, n.Hostname)
				}
			}
			if unread {
				note("logging: could not read the nodes' plugins (docker node inspect did not answer)")
			}
			for _, h := range missing {
				note("logging: no loki plugin: " + h)
			}
			if len(missing) > 0 {
				note("logging: " + swarm.LokiPluginInstall)
			}
		}
		// `--prune` is `--remove-orphans`; profiles were resolved into the file by the conversion.
		return r.Run(swarm.StackDeployCmd(st.Stack, files), exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "stack deploy"}), nil
	}
	if logged {
		// Anything but `true` — absent, disabled, docker not answering — fails every logged create alike.
		res := r.Run("docker plugin inspect -f '{{.Enabled}}' loki", exec.RunOptions{Label: "loki plugin"})
		if strings.TrimSpace(res.Stdout) != "true" {
			note("logging: install the loki log plugin on this host: " + swarm.LokiPluginInstall)
			// The fix is the note above; the Result only says what's wrong, not how to fix it — a step
			// message is cut at 300 runes and the install line alone is longer.
			return exec.Result{OK: false, Code: 1, Stderr: "loki log plugin not installed"}, nil
		}
	}
	cmd := baseOf(st, files) + " " + profileArgs(c.Profiles) + " up -d --remove-orphans"
	// --remove-orphans drops services that were in a previous deploy but are not selected now, so a
	// relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
	return r.Run(cmd, exec.RunOptions{Env: ComposeEnv(st, extraEnv), Label: "compose up"}), nil
}

// ComposeDown is `compose down -v` / `stack rm` plus the labelled volumes.
func ComposeDown(st *spec.Stack, r exec.Runner) (exec.Result, error) {
	c := st.Compose
	if isSwarm(st) {
		// `stack rm` takes nothing but the name; the conversion is not consulted and the labelled volumes
		// are removed afterwards (the `-v` equivalent — see swarm for the worker-node ceiling).
		return r.Run(swarm.StackRmCmd(st.Stack, true), exec.RunOptions{Env: ComposeEnv(st, nil), Label: "stack rm"}), nil
	}
	base, err := baseFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	// EVERY profile — see the package comment.
	cmd := base + " " + profileArgs(c.Profiles) + " down -v --remove-orphans"
	return r.Run(cmd, exec.RunOptions{Env: ComposeEnv(st, nil), Label: "compose down"}), nil
}

// ComposeSleep puts the project to sleep: containers and networks go, VOLUMES STAY. That is the whole
// difference from ComposeDown, and it is a separate function rather than a flag so the `down -v` the leak
// tests assert on cannot be weakened by a default.
func ComposeSleep(st *spec.Stack, r exec.Runner) (exec.Result, error) {
	c := st.Compose
	if isSwarm(st) {
		return r.Run(swarm.StackRmCmd(st.Stack, false), exec.RunOptions{Env: ComposeEnv(st, nil), Label: "stack rm (sleep)"}), nil
	}
	base, err := baseFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	// Every profile, for the same reason `down` passes them: a profile left out is a network left behind.
	cmd := base + " " + profileArgs(c.Profiles) + " down --remove-orphans"
	return r.Run(cmd, exec.RunOptions{Env: ComposeEnv(st, nil), Label: "compose down (sleep)"}), nil
}

// LogsCommand is a `logs` command line and the environment it needs.
type LogsCommand struct {
	Cmd string
	Env map[string]string
}

// ComposeLogsCommand is the `logs` command line and the environment it needs, without running it. nil when the
// spec has no compose section.
//
// Split out because FOLLOWING logs cannot go through Runner: that buffers a process to completion,
// and `--follow` never completes. The SSE route spawns this itself and streams the pipe. Both paths
// building their command here is what stops the followed stream and the fetched one disagreeing
// about which project, profiles or files they are reading.
//
// tail is a JS number (the API accepts and echoes `?tail=1.5`), truncated here. service "" reads the
// whole stack.
func ComposeLogsCommand(st *spec.Stack, r exec.Runner, tail float64, service string, opts LogsOptions) (*LogsCommand, error) {
	c := st.Compose
	if c == nil {
		return nil, nil
	}
	if isSwarm(st) {
		// `docker service logs` reads every node's tasks from the manager — the one thing swarm makes
		// easier. It has no `--until`; the API refuses that parameter in swarm mode rather than dropping it.
		return &LogsCommand{Cmd: swarm.StackLogsCmd(st.Stack, tail, service, opts), Env: ComposeEnv(st, nil)}, nil
	}
	only := ""
	if service != "" {
		only = " " + Shq(service)
	}
	flags := ""
	if opts.Follow {
		flags += " --follow"
	}
	if opts.Timestamps {
		flags += " --timestamps"
	}
	if opts.Since != "" {
		flags += " --since " + Shq(opts.Since)
	}
	if opts.Until != "" {
		flags += " --until " + Shq(opts.Until)
	}
	base, err := baseFor(st, r)
	if err != nil {
		return nil, err
	}
	return &LogsCommand{
		Cmd: base + " " + profileArgs(c.Profiles) + " logs --no-color --tail " + swarm.TruncString(tail) + flags + only,
		Env: ComposeEnv(st, nil),
	}, nil
}

// ComposeLogs is recent logs for a stack. `--no-color` because the output is rendered in a browser, where
// ANSI escapes are noise, and `--tail` because an unbounded fetch on a chatty stack would stream
// megabytes into a tab. Every profile is enabled for the same reason `down` enables them: compose
// treats an unenabled profile's services as absent, so their logs would silently be missing.
//
// service is one compose SERVICE to read, instead of the whole stack. On a stack with a chatty
// sidecar the interleaved output is unreadable and the interesting lines are already past the tail,
// so narrowing is often the difference between finding the error and not. Shell-quoted, because it
// arrives from a query parameter and is interpolated into a command.
//
// opts is the rest of what `docker compose logs` can tell you, which this used to throw away.
// `timestamps` is the one that matters most: without it a log line cannot be lined up against a
// deploy, a healthcheck flap or another service's line, and "when did this start" is unanswerable
// from the page. `since`/`until` are what make a long-lived container readable at all — a tail of
// 2000 on a service that logs every request is 2000 lines of the last four minutes.
func ComposeLogs(st *spec.Stack, r exec.Runner, tail float64, service string, opts LogsOptions) (exec.Result, error) {
	// `logs [SERVICE...]` — an unknown name makes compose exit non-zero with its own message, which is
	// better than anything this layer could invent.
	built, err := ComposeLogsCommand(st, r, tail, service, opts)
	if err != nil {
		return exec.Result{}, err
	}
	if built == nil {
		return exec.Result{OK: true, Code: 0, Stdout: "", Stderr: "(no compose section in spec)", Skipped: false}, nil
	}
	label := "compose logs"
	if service != "" {
		label = "compose logs " + service
	}
	return r.Run(built.Cmd, exec.RunOptions{Env: built.Env, Label: label}), nil
}

// ComposePs is `compose ps` / `stack ps`.
func ComposePs(st *spec.Stack, r exec.Runner) (exec.Result, error) {
	c := st.Compose
	if isSwarm(st) {
		return r.Run(swarm.StackPsCmd(st.Stack), exec.RunOptions{Env: ComposeEnv(st, nil), Label: "stack ps"}), nil
	}
	base, err := baseFor(st, r)
	if err != nil {
		return exec.Result{}, err
	}
	return r.Run(base+" "+profileArgs(c.Profiles)+" ps", exec.RunOptions{Env: ComposeEnv(st, nil), Label: "compose ps"}), nil
}
