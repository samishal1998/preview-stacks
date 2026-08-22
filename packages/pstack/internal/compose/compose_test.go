package compose

// Port of test/stack.test.ts 'compose invocation' (the argv and shq assertions; the TS went through
// stack.up/down, which with no axes issues exactly the compose command first), the nested 'compose
// invocation' of 'wildcard subdomains', the composeUp/composeDown halves of 'materializing the
// file', the pure parts of 'per-service logs', and test/features.test.ts 'compose.ts dispatches on
// the orchestrator' (stack.sleep belongs to stack).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

func specFrom(t *testing.T, file string, env map[string]string) *spec.Stack {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	st, err := spec.Parse(string(b), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

var pr7 = map[string]string{"PR": "7"}

// envRunner captures the env a compose subcommand runs with — exec.Fake records commands only.
type envRunner struct {
	seen []map[string]string
	cmds []string
}

func (e *envRunner) Run(cmd string, o exec.RunOptions) exec.Result {
	e.seen = append(e.seen, o.Env)
	e.cmds = append(e.cmds, cmd)
	return exec.Result{OK: true}
}
func (*envRunner) DryRun() bool             { return false }
func (*envRunner) Cwd() string              { return "" }
func (*envRunner) Context() context.Context { return context.Background() }

// must is `must(t)(ComposeUp(...))` — Go will not splice a two-value call after a leading argument.
func must(t *testing.T) func(exec.Result, error) {
	return func(_ exec.Result, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestComposeInvocation(t *testing.T) {
	s := specFrom(t, "two-profiles.yml", pr7)

	t.Run("up enables only the selected profiles", func(t *testing.T) {
		// negative control: drop `--remove-orphans` from the up line.
		r := exec.NewFake(nil, "")
		must(t)(ComposeUp(s, r, nil, nil))
		cmd := r.Commands()[0]
		for _, want := range []string{"--profile 'backend' --profile 'frontend'", "-p 'pr-7'", "--remove-orphans"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("missing %q in %s", want, cmd)
			}
		}
		if cmd != "docker compose -p 'pr-7' -f 'dc.yml' --profile 'backend' --profile 'frontend' up -d --remove-orphans" {
			t.Errorf("up: %s", cmd)
		}
	})

	t.Run("down enables EVERY profile — the network-leak fix", func(t *testing.T) {
		// negative control: pass c.Profiles[:1] on down — the second --profile disappears.
		// Compose treats a non-enabled profile's services as absent, so omitting one leaves that
		// profile's resources (notably the project default network) behind forever.
		r := exec.NewFake(nil, "")
		must(t)(ComposeDown(s, r))
		cmd := r.Commands()[0]
		if !strings.Contains(cmd, "--profile 'backend' --profile 'frontend'") || !strings.Contains(cmd, "down -v --remove-orphans") {
			t.Errorf("down: %s", cmd)
		}
	})

	t.Run("shq makes hostile values shell-safe", func(t *testing.T) {
		// negative control: escape `'` as `\'` — the first assertion fails.
		if Shq("a'b") != `'a'\''b'` {
			t.Errorf("got %s", Shq("a'b"))
		}
		if Shq("a b$c") != `'a b$c'` {
			t.Errorf("got %s", Shq("a b$c"))
		}
	})

	t.Run("ps and the labels every verb passes", func(t *testing.T) {
		// negative control: label `compose ps` as `compose up` — the label check fails.
		r := &envRunner{}
		labels := []string{}
		rec := &labelRunner{envRunner: r, labels: &labels}
		must(t)(ComposePs(s, rec))
		must(t)(ComposeUp(s, rec, nil, nil))
		must(t)(ComposeDown(s, rec))
		must(t)(ComposeSleep(s, rec))
		must(t)(ComposeLogs(s, rec, 10, "", LogsOptions{}))
		must(t)(ComposeLogs(s, rec, 10, "web", LogsOptions{}))
		if got := strings.Join(labels, "|"); got != "compose ps|compose up|compose down|compose down (sleep)|compose logs|compose logs web" {
			t.Errorf("labels: %s", got)
		}
		if r.cmds[0] != "docker compose -p 'pr-7' -f 'dc.yml' --profile 'backend' --profile 'frontend' ps" {
			t.Errorf("ps: %s", r.cmds[0])
		}
	})
}

// labelRunner also records RunOptions.Label.
type labelRunner struct {
	*envRunner
	labels *[]string
}

func (l *labelRunner) Run(cmd string, o exec.RunOptions) exec.Result {
	*l.labels = append(*l.labels, o.Label)
	return l.envRunner.Run(cmd, o)
}

func TestWildcardSubdomainsComposeInvocation(t *testing.T) {
	s := func() *spec.Stack { return specFrom(t, "wild.yml", pr7) }

	t.Run("up exports the rule for a label to interpolate", func(t *testing.T) {
		// negative control: drop spec.SubdomainEnv from ComposeEnv — the variable is absent.
		r := &envRunner{}
		must(t)(ComposeUp(s(), r, map[string]string{}, nil))
		if got := r.seen[0]["PSTACK_WILD_BACKEND"]; got != spec.WildcardRule("backend-pr-7.preview.example.com", spec.DepthOne) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("DOWN exports it too — the reason this is not only on `up`", func(t *testing.T) {
		// negative control: pass st.Env (not ComposeEnv) to the down command — the variable is absent.
		// Compose interpolates the compose FILE on every subcommand. Without the variable here, a
		// label referencing it substitutes empty, so `down` reasons about a differently-labelled
		// project than `up` created.
		r := &envRunner{}
		must(t)(ComposeDown(s(), r))
		if _, ok := r.seen[0]["PSTACK_WILD_BACKEND"]; !ok {
			t.Errorf("env: %v", r.seen[0])
		}
	})

	t.Run("STACK wins over everything, and extra env is layered", func(t *testing.T) {
		// negative control: merge STACK before `extra` — an extra STACK overrides the identity.
		r := &envRunner{}
		must(t)(ComposeUp(s(), r, map[string]string{"STACK": "evil", "X": "1"}, nil))
		if r.seen[0]["STACK"] != "pr-7" || r.seen[0]["X"] != "1" || r.seen[0]["DOMAIN"] != "preview.example.com" {
			t.Errorf("env: %v", r.seen[0])
		}
	})
}

func TestComposeDispatchesOnTheOrchestrator(t *testing.T) {
	swarmSpec := specFrom(t, "swarm.yml", nil)
	composeSpec := specFrom(t, "plain.yml", nil)

	t.Run("up/down/sleep/logs/ps become docker stack / service commands", func(t *testing.T) {
		// negative control: drop the isSwarm branch in ComposeUp — log[0] is a `docker compose` line.
		r := exec.NewFake(nil, "")
		// dryRun-free runner with no cwd and a missing file: materialize falls back to the original name.
		must(t)(ComposeUp(swarmSpec, r, map[string]string{}, nil))
		must(t)(ComposeDown(swarmSpec, r))
		must(t)(ComposeSleep(swarmSpec, r))
		logs, err := ComposeLogsCommand(swarmSpec, r, 50, "web", LogsOptions{Follow: true, Timestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		log := r.Commands()
		if log[0] != `docker stack deploy -c 'dc.yml' --prune --with-registry-auth --detach=true 's1'` {
			t.Errorf("deploy: %s", log[0])
		}
		if log[1] != swarm.StackRmCmd("s1", true) || !strings.Contains(log[1], "docker volume rm") {
			t.Errorf("rm: %s", log[1])
		}
		if log[2] != swarm.StackRmCmd("s1", false) || strings.Contains(log[2], "docker volume") {
			t.Errorf("sleep: %s", log[2])
		}
		if logs.Cmd != `docker service logs --no-color --tail 50 --follow --timestamps 's1_web'` {
			t.Errorf("logs: %s", logs.Cmd)
		}
		if !strings.Contains(swarm.StackLogsCmd("s1", 10, "", LogsOptions{Follow: true}), "wait") {
			t.Errorf("follow needs wait")
		}
		if strings.Contains(swarm.StackLogsCmd("s1", 10, "", LogsOptions{}), "wait") {
			t.Errorf("unfollowed has wait")
		}
		must(t)(ComposePs(swarmSpec, r))
		if got := r.Commands()[3]; got != "docker stack ps 's1'" {
			t.Errorf("ps: %s", got)
		}
	})

	t.Run("compose sleep is `down` WITHOUT -v; down keeps -v", func(t *testing.T) {
		// negative control: add ` -v` to the sleep line — the verbatim assertion fails (invariant 17).
		r := exec.NewFake(nil, "")
		must(t)(ComposeSleep(composeSpec, r))
		must(t)(ComposeDown(composeSpec, r))
		log := r.Commands()
		if log[0] != `docker compose -p 's1' -f 'dc.yml' --profile 'a' down --remove-orphans` {
			t.Errorf("sleep: %s", log[0])
		}
		if log[1] != `docker compose -p 's1' -f 'dc.yml' --profile 'a' down -v --remove-orphans` {
			t.Errorf("down: %s", log[1])
		}
	})

	t.Run("materialize writes the converted file beside the compose file, also under the CLI (no cwd)", func(t *testing.T) {
		// negative control: write the generated file at `dir` instead of beside the original — m.File
		// still says infra/… but the read at that path fails.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "infra"), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "infra", "dc.yml"), []byte("services:\n  app:\n    image: nginx\n    restart: always\n    profiles: [a]\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		s := specFrom(t, "mat-swarm.yml", nil)
		r := exec.NewFake(nil, "").WithCwd(dir)
		http01 := autolabel.HTTP01
		m, err := autolabel.MaterializeCompose(autolabel.MaterializeArgs{Dir: dir, Spec: s, Runner: r, Challenge: &http01})
		if err != nil {
			t.Fatal(err)
		}
		if m.File != "infra/"+autolabel.GeneratedCompose {
			t.Errorf("file: %s", m.File)
		}
		b, err := os.ReadFile(filepath.Join(dir, "infra", autolabel.GeneratedCompose))
		if err != nil {
			t.Fatal(err)
		}
		written, err := omap.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		app := written.(*omap.Map).GetMap("services").GetMap("app")
		if got := string(mustJSON(t, app.GetMap("deploy"))); got != `{"restart_policy":{"condition":"any"}}` {
			t.Errorf("deploy: %s", got)
		}
		if app.Has("profiles") {
			t.Errorf("profiles kept: %s", b)
		}
		if len(m.Notes) == 0 {
			t.Errorf("no notes")
		}
		// The swarm deploy names the converted file, and the notes reach the job log prefixed.
		notes := []string{}
		must(t)(ComposeUp(s, r, nil, func(l string) { notes = append(notes, l) }))
		if cmd := r.Commands()[0]; cmd != `docker stack deploy -c 'infra/compose.generated.yml' --prune --with-registry-auth --detach=true 's1'` {
			t.Errorf("deploy: %s", cmd)
		}
		if len(notes) == 0 || !strings.HasPrefix(notes[0], "swarm: service app: ") {
			t.Errorf("notes: %v", notes)
		}
		// Under compose with nothing to generate, nothing is written.
		plain := specFrom(t, "mat-plain.yml", nil)
		os.Remove(filepath.Join(dir, "infra", autolabel.GeneratedCompose))
		m2, err := autolabel.MaterializeCompose(autolabel.MaterializeArgs{Dir: dir, Spec: plain, Runner: r, Challenge: &http01})
		if err != nil || m2.File != "infra/dc.yml" {
			t.Errorf("plain: %+v %v", m2, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "infra", autolabel.GeneratedCompose)); err == nil {
			t.Errorf("written under compose with nothing to generate")
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := v.(interface{ MarshalJSON() ([]byte, error) }).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMaterializingTheFileThroughCompose(t *testing.T) {
	s := func() *spec.Stack { return specFrom(t, "routed.yml", pr7) }
	write := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=80\n"), 0o666); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("composeUp and composeDown both pass the DERIVED file to -f", func(t *testing.T) {
		// negative control: pass c.File (not m.File) in filesFor — `-f 'docker-compose.yml'` appears.
		// The integration point. If `down` used the submitted file it would compute a different project
		// shape than `up` created — which is the whole reason the file is regenerated per subcommand
		// rather than only at deploy time.
		dir := t.TempDir()
		write(t, dir)
		r := exec.NewFake(nil, "").WithCwd(dir)
		must(t)(ComposeUp(s(), r, map[string]string{}, nil))
		must(t)(ComposeDown(s(), r))
		cmds := r.Commands()
		if len(cmds) != 2 {
			t.Fatalf("cmds: %v", cmds)
		}
		for _, c := range cmds {
			if !strings.HasPrefix(c, "docker compose") || !strings.Contains(c, "-f '"+autolabel.GeneratedCompose+"'") {
				t.Errorf("cmd: %s", c)
			}
		}
	})

	t.Run("a dry run writes nothing — it must have no side effects", func(t *testing.T) {
		// negative control: drop the DryRun() early return in filesFor — the file appears.
		dir := t.TempDir()
		write(t, dir)
		r := exec.NewFake(nil, "").WithCwd(dir).WithDryRun(true)
		must(t)(ComposeUp(s(), r, map[string]string{}, nil))
		if _, err := os.Stat(filepath.Join(dir, autolabel.GeneratedCompose)); err == nil {
			t.Errorf("generated file written under dry-run")
		}
		if cmd := r.Commands()[0]; !strings.Contains(cmd, "-f 'docker-compose.yml'") {
			t.Errorf("dry-run names the submitted file: %s", cmd)
		}
	})

	t.Run("a spec error from the derived file surfaces from the verb", func(t *testing.T) {
		// negative control: swallow the MaterializeCompose error in filesFor — err is nil.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=web\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		r := exec.NewFake(nil, "").WithCwd(dir)
		_, err := ComposeUp(s(), r, nil, nil)
		if err == nil || !spec.IsSpecError(err) || !strings.Contains(err.Error(), "not a port") {
			t.Errorf("got %v", err)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("ran anyway: %v", r.Commands())
		}
	})
}

func TestComposeLogsForOneService(t *testing.T) {
	s := func() *spec.Stack { return specFrom(t, "logs.yml", pr7) }

	t.Run("no service reads the whole stack, exactly as before", func(t *testing.T) {
		// negative control: append ` ''` when service is empty — the line no longer ends in `--tail 200`.
		r := exec.NewFake(nil, "")
		must(t)(ComposeLogs(s(), r, 200, "", LogsOptions{}))
		cmd := r.Commands()[0]
		if !strings.Contains(cmd, "logs --no-color --tail 200") {
			t.Errorf("cmd: %s", cmd)
		}
		// Nothing appended, so compose reads every service in the enabled profiles.
		if !strings.HasSuffix(strings.TrimRight(cmd, " \t\n"), "--tail 200") {
			t.Errorf("cmd: %s", cmd)
		}
	})

	t.Run("a service is appended, and shell-quoted", func(t *testing.T) {
		// negative control: append the service unquoted — `--tail 50 'worker'` is absent.
		r := exec.NewFake(nil, "")
		must(t)(ComposeLogs(s(), r, 50, "worker", LogsOptions{}))
		if cmd := r.Commands()[0]; !strings.Contains(cmd, "--tail 50 "+Shq("worker")) {
			t.Errorf("cmd: %s", cmd)
		}
	})

	t.Run("a hostile service name cannot break out of the command", func(t *testing.T) {
		// negative control: append the service unquoted — the raw form appears.
		// The API validates the name first; this is the second line of defence, and the one that holds if
		// a future caller forgets. Asserted via shq itself rather than a hand-written escape, so the test
		// cannot drift from the quoting it is checking.
		hostile := "a'; rm -rf /; echo '"
		r := exec.NewFake(nil, "")
		must(t)(ComposeLogs(s(), r, 50, hostile, LogsOptions{}))
		cmd := r.Commands()[0]
		if !strings.Contains(cmd, Shq(hostile)) {
			t.Errorf("cmd: %s", cmd)
		}
		// The raw form — the one that would actually execute — never appears.
		if strings.Contains(cmd, "--tail 50 "+hostile) {
			t.Errorf("cmd: %s", cmd)
		}
	})

	t.Run("flags come in a fixed order, tail is truncated, and no compose section is a soft answer", func(t *testing.T) {
		// negative control: emit --timestamps before --follow — the verbatim line fails.
		r := exec.NewFake(nil, "")
		built, err := ComposeLogsCommand(s(), r, 1.5, "app", LogsOptions{Follow: true, Timestamps: true, Since: "10m", Until: "now"})
		if err != nil {
			t.Fatal(err)
		}
		if built.Cmd != "docker compose -p 'pr-7' -f 'dc.yml' --profile 'app' --profile 'worker' logs --no-color --tail 1 --follow --timestamps --since '10m' --until 'now' 'app'" {
			t.Errorf("cmd: %s", built.Cmd)
		}
		if built.Env["STACK"] != "pr-7" {
			t.Errorf("env: %v", built.Env)
		}
		none := specFrom(t, "nocompose.yml", nil)
		if b, err := ComposeLogsCommand(none, r, 1, "", LogsOptions{}); b != nil || err != nil {
			t.Errorf("got %+v %v", b, err)
		}
		res, err := ComposeLogs(none, r, 1, "", LogsOptions{})
		if err != nil || !res.OK || res.Code != 0 || res.Stderr != "(no compose section in spec)" || res.Skipped {
			t.Errorf("got %+v %v", res, err)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("ran: %v", r.Commands())
		}
	})
}
