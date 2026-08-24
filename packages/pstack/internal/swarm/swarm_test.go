package swarm

// Port of test/features.test.ts 'swarmify', the in-process half of 'swarm discovery and the swarm
// routes' (swarmInfo; deploymentRuntime and the HTTP routes belong to inspect/api) and 'pstack
// swarm — the CLI half'. The TS put a fake `docker` on PATH; here exec.Fake answers the same
// command lines from testdata.

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func docOf(t *testing.T, name string) *omap.Map {
	t.Helper()
	v, err := yamlx.ParseString(read(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*omap.Map)
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// shim is the TS dockerShim: a `case "$*"` over the command, `*) exit 0` for everything else.
func shim(answers map[string]string) *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if out, ok := answers[strings.TrimSpace(cmd)]; ok {
			return exec.Result{OK: true, Stdout: out}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

const (
	cmdInfo   = "docker info --format '{{json .Swarm}}'"
	cmdNodeLs = "docker node ls --format '{{json .}}'"
	cmdToken  = "docker swarm join-token -q worker"
)

func manager(t *testing.T) *exec.Fake {
	return shim(map[string]string{
		cmdInfo:   read(t, "info-active.json"),
		cmdNodeLs: read(t, "node-ls.jsonl"),
		cmdToken:  "SWMTKN-1-abcdef-ghijkl\n",
	})
}

func down() *exec.Fake { return exec.NewFake(func(string) bool { return true }, "") }

func inactive() *exec.Fake {
	return shim(map[string]string{cmdInfo: "{\"LocalNodeState\":\"inactive\"}\n"})
}

func TestSwarmify(t *testing.T) {
	t.Run("converts faithfully and names every change", func(t *testing.T) {
		// negative control: skip the profile filter — `worker` survives and the key list is four long.
		input := docOf(t, "swarmify.yml")
		before := jsonOf(t, input)
		res := Swarmify(input, []string{"app"})
		out := res.Doc
		svc := out.GetMap("services")
		keys := svc.Keys()
		sort.Strings(keys)
		if strings.Join(keys, ",") != "app,db,migrate" { // worker's profile not selected
			t.Errorf("services: %v", keys)
		}
		if out.Has("name") || out.Has("version") {
			t.Errorf("name/version kept: %s", jsonOf(t, out))
		}

		app := svc.GetMap("app")
		deploy := app.GetMap("deploy")
		if got := jsonOf(t, deploy.GetMap("restart_policy")); got != `{"condition":"any"}` {
			t.Errorf("restart_policy: %s", got)
		}
		if got := jsonOf(t, deploy.GetMap("resources")); got != `{"limits":{"memory":"256m","cpus":"0.5"}}` {
			t.Errorf("resources: %s", got)
		}
		if got := jsonOf(t, deploy.GetSlice("labels")); got != `["traefik.enable=true","traefik.swarm.network=preview-ingress"]` {
			t.Errorf("deploy.labels: %s", got)
		}
		if got := jsonOf(t, app.GetSlice("labels")); got != `["pstack.routing.port=80"]` {
			t.Errorf("labels: %s", got)
		}
		for _, k := range []string{"restart", "mem_limit", "cpus", "profiles", "container_name", "depends_on", "pull_policy"} {
			if app.Has(k) {
				t.Errorf("%s kept", k)
			}
		}
		if app.GetString("x-note") != "kept" {
			t.Errorf("x-note: %s", jsonOf(t, app))
		}

		// No `restart:` is `none` — compose would not restart it, and swarm's default `any` would loop a
		// one-shot migration forever.
		if got := jsonOf(t, svc.GetMap("migrate").GetMap("deploy").GetMap("restart_policy")); got != `{"condition":"none"}` {
			t.Errorf("migrate: %s", got)
		}
		// A deploy block the author wrote wins.
		db := svc.GetMap("db").GetMap("deploy")
		if got := jsonOf(t, db.GetMap("restart_policy")); got != `{"condition":"any"}` {
			t.Errorf("db: %s", got)
		}
		if v, _ := db.Get("replicas"); v != int64(2) {
			t.Errorf("replicas: %v", v)
		}

		text := strings.Join(res.Notes, "\n")
		for _, want := range []string{
			"service app: restart: always → deploy.restart_policy.condition: any",
			"mem_limit → deploy.resources.limits.memory",
			"dropped `container_name`",
			"dropped `pull_policy` — not in the compose v3 schema",
			"service worker: not in the selected profiles (worker)",
			"traefik.docker.network → traefik.swarm.network",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("note missing %q in:\n%s", want, text)
			}
		}
		// Pure: the input was not touched.
		if jsonOf(t, input) != before {
			t.Errorf("input mutated")
		}
	})

	t.Run("on-failure:N keeps the attempt count; an already swarm-shaped file needs no notes", func(t *testing.T) {
		// negative control: drop the `maxAttempts` Set — max_attempts is absent.
		res := Swarmify(docOf(t, "swarmify.yml"), []string{"worker"})
		worker := res.Doc.GetMap("services").GetMap("worker")
		if got := jsonOf(t, worker.GetMap("deploy").GetMap("restart_policy")); got != `{"condition":"on-failure","max_attempts":3}` {
			t.Errorf("worker: %s", got)
		}
		clean := Swarmify(docOf(t, "clean.yml"), []string{})
		if len(clean.Notes) != 0 || clean.Notes == nil {
			t.Errorf("notes: %v", clean.Notes)
		}
	})

	t.Run("every note in its order, byte for byte", func(t *testing.T) {
		// negative control: reorder the restart and resources blocks — the note order changes.
		res := Swarmify(docOf(t, "swarmify.yml"), []string{"app"})
		want := []string{
			"dropped top-level `name` — the stack name comes from the spec",
			"dropped `version: 2.4` — swarm reads the v3 schema",
			"service app: restart: always → deploy.restart_policy.condition: any",
			"service app: mem_limit → deploy.resources.limits.memory, cpus → deploy.resources.limits.cpus",
			"service app: moved traefik.* labels to deploy.labels (the swarm provider reads service labels)",
			"service app: traefik.docker.network → traefik.swarm.network (the swarm provider reads the latter)",
			"service app: dropped `container_name` — swarm names task containers itself (<stack>_<service>.<slot>.<task>)",
			"service app: dropped `depends_on` — swarm starts services independently — give the dependent a healthcheck or a retry loop",
			"service app: dropped `pull_policy` — not in the compose v3 schema swarm reads",
			"service migrate: no `restart` → deploy.restart_policy.condition: none",
			"service worker: not in the selected profiles (worker) — left out of the stack",
		}
		if jsonOf(t, res.Notes) != jsonOf(t, want) {
			t.Errorf("notes:\n%s\nwant:\n%s", strings.Join(res.Notes, "\n"), strings.Join(want, "\n"))
		}
		// The converted document's key order is the author's, with `deploy` appended last.
		if got := jsonOf(t, res.Doc.GetMap("services").GetMap("app")); got != `{"image":"nginx","labels":["pstack.routing.port=80"],"x-note":"kept","deploy":{"restart_policy":{"condition":"any"},"resources":{"limits":{"memory":"256m","cpus":"0.5"}},"labels":["traefik.enable=true","traefik.swarm.network=preview-ingress"]}}` {
			t.Errorf("app: %s", got)
		}
	})
}

func TestCommandLines(t *testing.T) {
	// negative control: drop `--with-registry-auth` from StackDeployCmd — the verbatim line fails.
	if got := StackDeployCmd("s1", []string{"dc.yml"}); got != `docker stack deploy -c 'dc.yml' --prune --with-registry-auth --detach=true 's1'` {
		t.Errorf("deploy: %s", got)
	}
	rm := StackRmCmd("s1", true)
	if !strings.HasPrefix(rm, "docker stack rm 's1'; for i in $(seq 1 60); do [ -z \"$(docker stack ps 's1' -q 2>/dev/null)\" ] && [ -z \"$(docker network ls -q --filter 'label=com.docker.stack.namespace=s1')\" ] && break; sleep 1; done") {
		t.Errorf("rm: %s", rm)
	}
	if !strings.HasSuffix(rm, "; docker volume ls -q --filter 'label=com.docker.stack.namespace=s1' | xargs -r docker volume rm >/dev/null") {
		t.Errorf("rm volumes: %s", rm)
	}
	if strings.Contains(StackRmCmd("s1", false), "docker volume") {
		t.Errorf("sleep touches volumes: %s", StackRmCmd("s1", false))
	}
	if got := StackPsCmd("a b"); got != "docker stack ps 'a b'" {
		t.Errorf("ps: %s", got)
	}
	if got := StackLogsCmd("s1", 50.9, "web", LogsOptions{Follow: true, Timestamps: true}); got != `docker service logs --tail 50 --follow --timestamps 's1_web'` {
		t.Errorf("logs: %s", got)
	}
	whole := StackLogsCmd("s1", 10, "", LogsOptions{Follow: true, Since: "1h"})
	if whole != `svcs=$(docker stack services --format '{{.Name}}' 's1'); [ -n "$svcs" ] || { echo "no services in stack s1" >&2; exit 1; }; for svc in $svcs; do docker service logs --tail 10 --follow --since '1h' "$svc" 2>&1 & done; wait` {
		t.Errorf("whole: %s", whole)
	}
	if strings.Contains(StackLogsCmd("s1", 10, "", LogsOptions{}), "wait") {
		t.Errorf("unfollowed has wait")
	}
	if Shq("a'b") != `'a'\''b'` || Shq("a b$c") != `'a b$c'` {
		t.Errorf("shq")
	}
}

// serviceLogsFlags is `docker service logs`'s flag set as documented at
// https://docs.docker.com/reference/cli/docker/service/logs/. Docker answers anything else with
// `unknown flag: …` and a usage dump, so an option borrowed from `docker compose logs` — which
// accepts a wider set — does not degrade, it takes down every log read on the host. That is how
// `--no-color` shipped.
//
// This list is TRANSCRIBED, not measured: no swarm host runs in CI, so nothing here has asked a real
// docker. It is safe in the direction that matters — every flag the code actually emits is checked
// against it, so an entry that is missing fails loudly. It is NOT safe in the other direction: an
// entry that should not be here would greenlight that flag if someone later emits it. Before adding
// a flag to StackLogsCmd, run `docker service logs --help` on a manager and confirm it there. The
// `bash -n` check below is the load-bearing one; it depends on nothing but bash.
var serviceLogsFlags = map[string]bool{
	"--details": true, "--follow": true, "--no-resolve": true, "--no-task-ids": true, "--no-trunc": true,
	"--raw": true, "--since": true, "--tail": true, "--timestamps": true,
}

// logsForms is every shape StackLogsCmd returns: one service or the whole stack, followed or not.
// Both axes matter — the flags differ by option, and the loop's shell syntax differs by follow.
func logsForms() []struct{ name, cmd string } {
	return []struct{ name, cmd string }{
		{"one service", StackLogsCmd("s1", 50.9, "web", LogsOptions{Timestamps: true, Since: "1h"})},
		{"one service followed", StackLogsCmd("s1", 10, "web", LogsOptions{Follow: true, Timestamps: true})},
		{"whole stack", StackLogsCmd("s1", 10, "", LogsOptions{Since: "1h"})},
		{"whole stack followed", StackLogsCmd("s1", 10, "", LogsOptions{Follow: true, Timestamps: true, Since: "1h"})},
	}
}

// Everything above compares StackLogsCmd's output to a string someone wrote down, which is only ever
// as right as the person who wrote it: `--no-color` and a `&;` that no shell will parse both matched
// their expected line exactly, for four releases. These two read the command the way docker and bash
// read it instead.
func TestStackLogsCmdIsSomethingDockerAndBashAccept(t *testing.T) {
	t.Run("no flag outside the set `docker service logs` takes", func(t *testing.T) {
		// negative control: put `--no-color ` back in StackLogsCmd's first flag — all four forms fail.
		for _, f := range logsForms() {
			i := strings.Index(f.cmd, "docker service logs ")
			if i < 0 {
				t.Fatalf("%s: no `docker service logs` in %s", f.name, f.cmd)
			}
			// Only what follows it: the whole-stack form's `--format` belongs to `docker stack
			// services`, a different command with a different flag set.
			seen := 0
			for _, tok := range strings.Fields(f.cmd[i:]) {
				if !strings.HasPrefix(tok, "--") {
					continue // a flag's value, `"$svc"`, the quoted name, `2>&1`, `done` — not our business
				}
				seen++
				if !serviceLogsFlags[tok] {
					t.Errorf("%s: %s is not a `docker service logs` flag: %s", f.name, tok, f.cmd)
				}
			}
			// Without this, renaming the command turns the scan into a loop over nothing that passes
			// forever — the same silence that let the borrowed flag through.
			if seen == 0 {
				t.Errorf("%s: scanned no flags at all: %s", f.name, f.cmd)
			}
		}
	})

	t.Run("every form is a script bash can parse", func(t *testing.T) {
		// negative control: restore `"; done"` after the backgrounded `&` — `bash -n` reports `syntax
		// error near unexpected token ';'` for both followed forms. `&` already terminates a command,
		// so `cmd &; done` never ran; the string assertion could not tell, because bytes that do not
		// parse still compare equal to themselves.
		bash, err := osexec.LookPath("bash")
		if err != nil {
			t.Skip("no bash")
		}
		dir := t.TempDir()
		for i, f := range logsForms() {
			script := filepath.Join(dir, "logs"+string(rune('a'+i))+".sh")
			if err := os.WriteFile(script, []byte(f.cmd), 0o666); err != nil {
				t.Fatal(err)
			}
			if out, err := osexec.Command(bash, "-n", script).CombinedOutput(); err != nil {
				t.Errorf("%s: %v: %s\n%s", f.name, err, out, f.cmd)
			}
		}
	})
}

func TestSwarmDiscovery(t *testing.T) {
	t.Run("swarmInfo reads the manager address and every node", func(t *testing.T) {
		// negative control: read `Self` from `ID == self` only — mgr's Self flips when NodeID is absent.
		info := SwarmInfo(shim(map[string]string{cmdInfo: read(t, "info-active.json"), cmdNodeLs: read(t, "node-ls-short.jsonl")}))
		if !info.Active || !info.Reachable || info.ManagerAddr == nil || *info.ManagerAddr != "10.0.0.1:2377" {
			t.Errorf("info: %s", jsonOf(t, info))
		}
		rows := ""
		for _, n := range info.Nodes {
			rows += n.Hostname + ":" + n.Role + ":" + jsonOf(t, n.Self) + " "
		}
		if rows != "mgr:manager:true wrk:worker:false " {
			t.Errorf("nodes: %s", rows)
		}
		// The wire shape — tri-states as null, no token anywhere.
		js := jsonOf(t, info)
		if !strings.HasPrefix(js, `{"reachable":true,"active":true,"nodeId":"n1","managerAddr":"10.0.0.1:2377","nodes":[{"id":"n1","hostname":"mgr","role":"manager","status":"ready","availability":"active","managerStatus":"leader","engineVersion":"28.0.1","self":true},{"id":"n2","hostname":"wrk","role":"worker","status":"ready","availability":"active","managerStatus":null,`) {
			t.Errorf("json: %s", js)
		}
		if strings.Contains(js, "SWMTKN") || strings.Contains(js, `"error"`) {
			t.Errorf("json: %s", js)
		}
	})

	t.Run("when docker does not answer, the panel says so rather than \"no nodes\"", func(t *testing.T) {
		// negative control: return Info{Reachable: true} on a failed `docker info` — reachable reads true.
		info := SwarmInfo(down())
		if info.Reachable || info.Nodes == nil || len(info.Nodes) != 0 {
			t.Errorf("info: %s", jsonOf(t, info))
		}
		if got := jsonOf(t, info); got != `{"reachable":false,"active":false,"nodeId":null,"managerAddr":null,"nodes":[]}` {
			t.Errorf("json: %s", got)
		}
	})

	t.Run("an inactive daemon stops before `node ls`; a failed `node ls` is reported", func(t *testing.T) {
		// negative control: drop the `!active` return — `node ls` runs and its stderr lands in error.
		f := inactive()
		info := SwarmInfo(f)
		if info.Active || len(f.Commands()) != 1 {
			t.Errorf("info %s after %v", jsonOf(t, info), f.Commands())
		}
		if info.ManagerAddr != nil || info.NodeID != nil {
			t.Errorf("addr from nothing: %s", jsonOf(t, info))
		}
		f2 := manager(t)
		f2.Answer = func(cmd string) (exec.Result, bool) {
			if strings.TrimSpace(cmd) == cmdNodeLs {
				return exec.Result{OK: false, Code: 1, Stderr: "Error response from daemon: boom\nsecond line\n"}, true
			}
			return exec.Result{OK: true, Stdout: read(t, "info-active.json")}, true
		}
		info = SwarmInfo(f2)
		if info.Error == nil || *info.Error != "Error response from daemon: boom" || !info.Active {
			t.Errorf("info: %s", jsonOf(t, info))
		}
	})

	t.Run("the ports table is pinned, in order", func(t *testing.T) {
		// negative control: swap the first two rows — the client test's order assertion fails.
		if got := jsonOf(t, SwarmPorts); got != `[{"port":"2377/tcp","why":"cluster management (worker → manager)"},{"port":"7946/tcp+udp","why":"node discovery (every node ↔ every node)"},{"port":"4789/udp","why":"overlay network traffic — VXLAN (every node ↔ every node)"}]` {
			t.Errorf("ports: %s", got)
		}
		if PortList() != "2377/tcp, 7946/tcp+udp, 4789/udp" {
			t.Errorf("list: %s", PortList())
		}
	})
}

func TestPstackSwarmTheCLIHalf(t *testing.T) {
	t.Run("the report names WHICH of the three states it is in, and never a token", func(t *testing.T) {
		// negative control: print the table for an inactive host too — `HOSTNAME` appears in `off`.
		active := SwarmReport(SwarmInfo(manager(t)))
		// The table, the manager address, and every node — `*` marks the host you are typing on.
		for _, want := range []string{"swarm manager  10.0.0.1:2377", "preview-host *", "manager (leader)", "worker-1", "2 nodes", "pstack swarm join", "2377/tcp"} {
			if !strings.Contains(active, want) {
				t.Errorf("missing %q in:\n%s", want, active)
			}
		}
		if strings.Contains(active, "SWMTKN") {
			t.Errorf("token in report")
		}
		// The exact transcript: padded columns, trailing spaces trimmed, ids cut to 12.
		wantActive := strings.Join([]string{
			"swarm manager  10.0.0.1:2377",
			"2 nodes  (* this host)",
			"",
			"  HOSTNAME        ROLE              STATUS  AVAILABILITY  ENGINE  ID",
			"  preview-host *  manager (leader)  ready   active        28.0.1  n1abcdef0123",
			"  worker-1        worker            ready   active        28.0.1  n2abcdef0123",
			"",
			"add a worker:",
			"  pstack swarm join                       the `docker swarm join` line",
			"  pstack swarm join --format script       installs Docker first, then joins",
			"  pstack swarm join --format cloud-config --distro debian -o worker.yaml",
			"",
			"open between every pair of nodes first:",
			"  2377/tcp       cluster management (worker → manager)",
			"  7946/tcp+udp   node discovery (every node ↔ every node)",
			"  4789/udp       overlay network traffic — VXLAN (every node ↔ every node)",
		}, "\n")
		if active != wantActive {
			t.Errorf("report:\n%s\nwant:\n%s", active, wantActive)
		}

		downReport := SwarmReport(SwarmInfo(down()))
		if !strings.Contains(downReport, "docker did not answer") {
			t.Errorf("down: %s", downReport)
		}
		// "could not tell" must never read as "there is no swarm".
		if strings.Contains(downReport, "not a swarm manager") {
			t.Errorf("down: %s", downReport)
		}

		off := SwarmReport(SwarmInfo(inactive()))
		if !strings.Contains(off, "not a swarm manager") || !strings.Contains(off, "--orchestrator swarm") || strings.Contains(off, "HOSTNAME") {
			t.Errorf("off: %s", off)
		}
	})

	t.Run("join material: every format, from the one function the API route also calls", func(t *testing.T) {
		// negative control: build `command` from JoinScript — the command text no longer ends in one line.
		prev := CloudInit
		defer func() { CloudInit = prev }()
		var rendered []string
		CloudInit.Distros = []string{"ubuntu", "debian", "fedora", "suse", "arch", "alpine"}
		CloudInit.Render = func(token, managerAddr, distro string) string {
			rendered = append(rendered, distro)
			return "#cloud-config\n# " + distro + "\n" + JoinCommand(token, managerAddr) + "\n"
		}
		r := manager(t)
		got := func(format string, distro *string) JoinResult {
			return JoinMaterial(JoinArgs{Runner: r, Format: format, Distro: distro})
		}
		str := func(s string) *string { return &s }

		command := got("command", nil)
		if !command.OK || command.Text != "docker swarm join --token SWMTKN-1-abcdef-ghijkl 10.0.0.1:2377\n" || command.ManagerAddr != "10.0.0.1:2377" {
			t.Errorf("command: %+v", command)
		}
		if tok := got("token", nil); !tok.OK || tok.Text != "SWMTKN-1-abcdef-ghijkl\n" {
			t.Errorf("token: %+v", tok)
		}
		script := got("script", nil)
		if !script.OK || !strings.HasPrefix(script.Text, "#!/usr/bin/env bash\n") || !strings.Contains(script.Text, "docker swarm join --token SWMTKN-1-abcdef-ghijkl") {
			t.Errorf("script: %+v", script)
		}
		cc := got("cloud-config", str("debian"))
		if !cc.OK || !strings.HasPrefix(cc.Text, "#cloud-config") || len(rendered) != 1 || rendered[0] != "debian" {
			t.Errorf("cloud-config: %+v (rendered %v)", cc, rendered)
		}
		// Every shape embeds the token — which is why both callers warn and neither logs it.
		for _, f := range JoinFormats {
			m := got(f, str("ubuntu"))
			if !m.OK || !strings.Contains(m.Text, "SWMTKN-1-abcdef-ghijkl") {
				t.Errorf("%s: %+v", f, m)
			}
		}
		// The distro defaults to ubuntu when absent, and "" (the API's `?distro=`) is refused.
		if got("cloud-config", nil); rendered[len(rendered)-1] != "ubuntu" {
			t.Errorf("default distro: %v", rendered)
		}

		// The caller's own input is a different KIND of refusal from a fact about the host, because the
		// API turns one into a 400 and the other into a 409/503, and the CLI into exit 3 vs 1.
		if m := got("pdf", nil); m.OK || m.Kind != BadFormat || m.Message != "format must be one of: token, command, script, cloud-config" {
			t.Errorf("pdf: %+v", m)
		}
		if m := got("cloud-config", str("plan9")); m.OK || m.Kind != BadDistro || m.Message != "distro must be one of: ubuntu, debian, fedora, suse, arch, alpine" {
			t.Errorf("plan9: %+v", m)
		}
		if m := got("cloud-config", str("")); m.OK || m.Kind != BadDistro {
			t.Errorf("empty distro: %+v", m)
		}
		// A bad distro is refused BEFORE docker is consulted.
		before := len(r.Commands())
		got("cloud-config", str("plan9"))
		if len(r.Commands()) != before {
			t.Errorf("docker consulted for a bad distro")
		}
	})

	t.Run("a host that is not a manager, and a docker that did not answer, refuse differently", func(t *testing.T) {
		// negative control: check Reachable only — the inactive host reads as `unreachable`.
		if m := JoinMaterial(JoinArgs{Runner: inactive(), Format: "command"}); m.OK || m.Kind != NotAManager || m.Message != "this daemon is not a swarm manager — nothing to join" {
			t.Errorf("inactive: %+v", m)
		}
		if m := JoinMaterial(JoinArgs{Runner: down(), Format: "command"}); m.OK || m.Kind != Unreachable || m.Message != "docker did not answer" {
			t.Errorf("down: %+v", m)
		}
		// A manager that will not hand out a token is its own refusal.
		noToken := manager(t)
		inner := noToken.Answer
		noToken.Answer = func(cmd string) (exec.Result, bool) {
			if strings.TrimSpace(cmd) == cmdToken {
				return exec.Result{OK: true, Stdout: "\n"}, true
			}
			return inner(cmd)
		}
		if m := JoinMaterial(JoinArgs{Runner: noToken, Format: "token"}); m.OK || m.Kind != NoToken {
			t.Errorf("no token: %+v", m)
		}
	})

	t.Run("the join script is the fixed transcript", func(t *testing.T) {
		// negative control: drop `set -euo pipefail` — the transcript differs.
		want := strings.Join([]string{
			"#!/usr/bin/env bash",
			"# Join this machine to the pstack swarm as a WORKER. Run as root (or with sudo).",
			"# Open 2377/tcp, 7946/tcp+udp, 4789/udp between this machine and the manager first.",
			"set -euo pipefail",
			"if ! command -v docker >/dev/null 2>&1; then",
			"  curl -fsSL https://get.docker.com | sh",
			"fi",
			"systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true",
			`if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]; then`,
			`  echo "already part of a swarm: $(docker info --format '{{.Swarm.NodeID}}')"; exit 0`,
			"fi",
			"docker swarm join --token T 1.2.3.4:2377",
			"docker info --format 'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})'",
			"",
		}, "\n")
		if got := JoinScript("T", "1.2.3.4:2377"); got != want {
			t.Errorf("script:\n%s", got)
		}
	})
}
