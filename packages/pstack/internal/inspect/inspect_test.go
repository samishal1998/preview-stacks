package inspect

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

func TestHostsFromRule(t *testing.T) {
	// negative control: replace ruleArgs with a `\(([^)]*)\)` regexp — the HostRegexp with parens truncates.
	if got := HostsFromRule("Host(`app-pr-1.example.com`)"); strings.Join(got, ",") != "app-pr-1.example.com" {
		t.Errorf("got %v", got)
	}
	if got := HostsFromRule("Host(`a.example.com`,`b.example.com`)"); strings.Join(got, ",") != "a.example.com,b.example.com" {
		t.Errorf("got %v", got)
	}
	// A regexp is shown as a pattern rather than pretended to be a hostname you can click.
	if got := HostsFromRule("HostRegexp(`^[a-z]+\\.app-pr-1\\.example\\.com$`)"); strings.Join(got, ",") != `(pattern) ^[a-z]+\.app-pr-1\.example\.com$` {
		t.Errorf("got %v", got)
	}
	if got := HostsFromRule("HostRegexp(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\\.app-wk\\.example\\.com$`)"); len(got) != 1 || !strings.HasSuffix(got[0], `\.app-wk\.example\.com$`) {
		t.Errorf("parens inside a pattern: %v", got)
	}
	if got := HostsFromRule("PathPrefix(`/api`)"); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestRoutesJoinServicePortAndTarget(t *testing.T) {
	// negative control: build target when only the port is known — "no ingress IP" yields ":80".
	// negative control (reason): make the missing-target switch fall through to "not-on-ingress"
	// instead of "unknown-node" — the on-ingress-but-no-address case asserts a network fault that
	// nobody measured, which is the bug this field exists to end.
	yes, no := true, false
	ip := "172.20.0.5"
	r := RoutesFromLabels("pr-1-app-1", map[string]string{
		"traefik.enable":                                         "true",
		"traefik.http.routers.app-pr1.rule":                      "Host(`app-pr-1.example.com`)",
		"traefik.http.routers.app-pr1.service":                   "app-pr1",
		"traefik.http.services.app-pr1.loadbalancer.server.port": "80",
	}, &ip, &yes)
	if len(r) != 1 || strings.Join(r[0].Hosts, ",") != "app-pr-1.example.com" || r[0].Port == nil || *r[0].Port != 80 || r[0].Target == nil || *r[0].Target != "172.20.0.5:80" {
		t.Errorf("got %+v", r)
	}
	// A target that IS known carries no reason at all.
	if r[0].TargetReason != "" {
		t.Errorf("a resolved target needs no reason: %q", r[0].TargetReason)
	}
	// A service label named after the router is picked up without an explicit .service.
	ip2 := "10.0.0.2"
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "3000"}, &ip2, &yes)
	if *r[0].Port != 3000 || *r[0].Target != "10.0.0.2:3000" {
		t.Errorf("got %+v", r[0])
	}
	// An empty .service label does not become a port of "".
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.routers.app.service": "", "traefik.http.services.app.loadbalancer.server.port": "8080"}, &ip2, &yes)
	if r[0].Service != nil || *r[0].Port != 8080 || *r[0].Target != "10.0.0.2:8080" {
		t.Errorf("got %+v", r[0])
	}
	// No ingress IP means no target, rather than a half-built one — and the three ways that happens
	// are three different facts, because the page says which one out loud.
	routed := map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "80"}
	for _, c := range []struct {
		name      string
		ip        *string
		onIngress *bool
		want      string
	}{
		{"attached, address not knowable from here", nil, &yes, "unknown-node"},
		{"attachment could not be determined", nil, nil, "unknown-node"},
		{"measured, and genuinely off the network", nil, &no, "not-on-ingress"},
	} {
		r = RoutesFromLabels("c", routed, c.ip, c.onIngress)
		if *r[0].Port != 80 || r[0].Target != nil || r[0].TargetReason != c.want {
			t.Errorf("%s: target %v reason %q, want reason %q", c.name, r[0].Target, r[0].TargetReason, c.want)
		}
	}
	// No port is no port whatever the network says — the half the spec author controls comes first.
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)"}, &ip2, &no)
	if r[0].Target != nil || r[0].TargetReason != "no-port" {
		t.Errorf("no port: %+v", r[0])
	}
}

// dockerRunner answers `docker ps -aq` and `docker inspect` from a fixture.
func dockerRunner(containers []any, ids ...string) exec.Runner {
	if len(ids) == 0 {
		ids = []string{"abc123"}
	}
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		if strings.HasPrefix(cmd, "docker ps -aq") {
			return exec.Result{OK: true, Stdout: strings.Join(ids, "\n")}, true
		}
		if strings.HasPrefix(cmd, "docker inspect") {
			b, _ := json.Marshal(containers)
			return exec.Result{OK: true, Stdout: string(b)}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func container(labels map[string]string, networks map[string]any) map[string]any {
	all := map[string]any{"com.docker.compose.service": "app"}
	for k, v := range labels {
		all[k] = v
	}
	if networks == nil {
		networks = map[string]any{"preview-ingress": map[string]any{"IPAddress": "172.20.0.5"}}
	}
	return map[string]any{
		"Id":              "abc123def456",
		"Name":            "/pr-1-app-1",
		"Config":          map[string]any{"Image": "nginx:alpine", "Labels": all},
		"State":           map[string]any{"Status": "running"},
		"NetworkSettings": map[string]any{"Networks": networks, "Ports": map[string]any{"80/tcp": nil}},
	}
}

func run(labels map[string]string, networks map[string]any, challenge Challenge) Runtime {
	return DeploymentRuntime(RuntimeArgs{Stack: "pr-1", Runner: dockerRunner([]any{container(labels, networks)}), Challenge: challenge})
}

func find(rt Runtime, substr string) *Finding {
	for i := range rt.Findings {
		if strings.Contains(rt.Findings[i].Message, substr) {
			return &rt.Findings[i]
		}
	}
	return nil
}

func TestFindings(t *testing.T) {
	t.Run("a container with NO Traefik labels is named as unreachable", func(t *testing.T) {
		// negative control: drop the `!hasAny` branch — no finding.
		rt := run(nil, nil, HTTP01)
		if !rt.Reachable || len(rt.Routes) != 0 {
			t.Fatalf("got %+v", rt)
		}
		f := find(rt, "no Traefik labels")
		if f == nil || !strings.Contains(f.Message, "traefik.enable=true") {
			t.Errorf("got %v", rt.Findings)
		}
	})
	t.Run("labels without traefik.enable=true is an error, because exposedbydefault is false", func(t *testing.T) {
		// negative control: make the level "warn".
		rt := run(map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "80"}, nil, HTTP01)
		f := find(rt, "exposedbydefault")
		if f == nil || f.Level != "error" {
			t.Errorf("got %v", rt.Findings)
		}
	})
	t.Run("compose's own look-alike network is called out by name", func(t *testing.T) {
		// negative control: drop the lookalike branch — the generic "not attached" message appears instead.
		rt := run(map[string]string{"traefik.enable": "true"}, map[string]any{"pr-1_preview-ingress": map[string]any{"IPAddress": "172.30.0.2"}}, HTTP01)
		f := find(rt, "pr-1_preview-ingress")
		if f == nil || f.Level != "error" || !strings.Contains(f.Message, "external: true") {
			t.Errorf("got %v", rt.Findings)
		}
	})
	t.Run("TLS without a certresolver is an error on an HTTP-01 host, and fine on DNS-01", func(t *testing.T) {
		// negative control: check `challenge == DNS01` for the error — inverted.
		labels := map[string]string{"traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.routers.app.tls": "true", "traefik.http.services.app.loadbalancer.server.port": "80"}
		if find(run(labels, nil, HTTP01), "HTTP-01") == nil {
			t.Error("http01 must flag it")
		}
		if find(run(labels, nil, DNS01), "HTTP-01") != nil {
			t.Error("dns01 must not")
		}
	})
	t.Run("a certresolver on a DNS-01 host warns about the rate limit", func(t *testing.T) {
		// negative control: drop the dns01 branch.
		labels := map[string]string{"traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.routers.app.tls": "true", "traefik.http.routers.app.tls.certresolver": "le", "traefik.http.services.app.loadbalancer.server.port": "80"}
		if find(run(labels, nil, DNS01), "50-per-week") == nil {
			t.Error("missing")
		}
	})
	t.Run("a duplicate router name across the host is an error — the namespace is global", func(t *testing.T) {
		// negative control: compare len(owners) > 2.
		rt := DeploymentRuntime(RuntimeArgs{Stack: "pr-1", Runner: dockerRunner([]any{container(map[string]string{"traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "80", "traefik.http.routers.app.tls.certresolver": "le", "traefik.http.routers.app.tls": "true"}, nil)}), Challenge: HTTP01, AllRouters: map[string][]string{"app": {"pr-1-app-1", "pr-2-app-1"}}})
		f := find(rt, "namespace is global")
		if f == nil || f.Level != "error" {
			t.Errorf("got %v", rt.Findings)
		}
	})
	t.Run(`docker not answering reports unreachable, never "nothing running"`, func(t *testing.T) {
		// negative control: return empty() with Reachable:true — the tri-state collapses.
		f := exec.NewFake(func(string) bool { return true }, "")
		rt := DeploymentRuntime(RuntimeArgs{Stack: "pr-1", Runner: f, Challenge: Unknown})
		if rt.Reachable || len(rt.Containers) != 0 || len(rt.Findings) != 0 || rt.Containers == nil || rt.Findings == nil || rt.Routes == nil {
			t.Errorf("got %+v", rt)
		}
	})
	t.Run("the container environment is never included, and label values are redacted", func(t *testing.T) {
		// negative control: copy raw.Config.Env into ContainerInfo — the blob carries the token.
		c := container(map[string]string{"traefik.enable": "true", "traefik.http.middlewares.a.basicauth.users": "admin:PLAINTEXT_PASSWORD_TOKEN=abcdefgh"}, nil)
		c["Config"].(map[string]any)["Env"] = []string{"DATABASE_URL=postgres://u:p@h/db", "API_TOKEN=super-secret-token-value"}
		rt := DeploymentRuntime(RuntimeArgs{Stack: "pr-1", Runner: dockerRunner([]any{c}), Challenge: HTTP01})
		blob := string(jsonx.Must(rt))
		for _, bad := range []string{"super-secret-token-value", "postgres://u:p@h/db", "DATABASE_URL"} {
			if strings.Contains(blob, bad) {
				t.Errorf("leaked %q", bad)
			}
		}
	})
}

func TestTaskStateAndSwarmDiscovery(t *testing.T) {
	// negative control: map "failed" to "exited" — the restarting verdict the readiness watcher needs is gone.
	for in, want := range map[string]string{"Running 3 minutes ago": "running", "Failed 1 minute ago": "restarting", "Rejected": "restarting", "Shutdown 2 hours ago": "exited", "Complete": "exited", "": "unknown", "Pending": "pending"} {
		if got := TaskState(in); got != want {
			t.Errorf("TaskState(%q) = %q, want %q", in, got, want)
		}
	}
	// The swarm shim from features.test.ts: one service, a live task here, a replaced task's corpse, and a remote task.
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			return exec.Result{OK: true, Stdout: "aaa111aaa111\nbbb222bbb222\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: `[{"Id":"aaa111aaa111","Name":"/sw_app.1.task1","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"task1","com.docker.swarm.node.id":"n1"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:01:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}},{"Id":"bbb222bbb222","Name":"/sw_app.1.oldtask","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"dead0","com.docker.swarm.node.id":"n1"}},"State":{"Status":"exited","ExitCode":1},"NetworkSettings":{"Networks":{},"Ports":{}}}]`}, true
		case strings.HasPrefix(cmd, "docker service ls"):
			return exec.Result{OK: true, Stdout: `{"ID":"svc1","Name":"sw_app","Mode":"replicated","Replicas":"2/2"}` + "\n"}, true
		case strings.HasPrefix(cmd, "docker service inspect"):
			return exec.Result{OK: true, Stdout: "[{\"ID\":\"svc1xxxxxxxxxxxx\",\"UpdatedAt\":\"2026-08-20T10:00:00Z\",\"Spec\":{\"Name\":\"sw_app\",\"Labels\":{\"com.docker.stack.namespace\":\"sw\",\"traefik.enable\":\"true\",\"traefik.swarm.network\":\"preview-ingress\",\"traefik.http.routers.app-sw.rule\":\"Host(`app-sw.example.com`)\",\"traefik.http.services.app-sw.loadbalancer.server.port\":\"80\"},\"TaskTemplate\":{\"ContainerSpec\":{\"Image\":\"nginx\"},\"Networks\":[{\"Target\":\"net1\"}]}}}]"}, true
		case strings.HasPrefix(cmd, "docker network ls"):
			return exec.Result{OK: true, Stdout: "net1 preview-ingress\nnet2 sw_default\n"}, true
		case strings.HasPrefix(cmd, "docker stack ps"):
			return exec.Result{OK: true, Stdout: `{"ID":"task1","Name":"sw_app.1","Image":"nginx","Node":"mgr","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}` + "\n" + `{"ID":"task2","Name":"sw_app.2","Image":"nginx","Node":"wrk","DesiredState":"Running","CurrentState":"Running 1 minute ago"}` + "\n"}, true
		}
		return exec.Result{OK: true}, true
	}
	rt := DeploymentRuntime(RuntimeArgs{Stack: "sw", Runner: f, Challenge: DNS01, Orchestrator: spec.Swarm})
	if !rt.Reachable || len(rt.Containers) != 2 {
		t.Fatalf("got %d containers: %+v", len(rt.Containers), rt.Containers)
	}
	local, remote := rt.Containers[0], rt.Containers[1]
	if local.Remote || local.Node == nil || *local.Node != "mgr" || local.State != "running" || *local.Service != "app" {
		t.Errorf("local task: %+v", local)
	}
	if !remote.Remote || *remote.Node != "wrk" || remote.Name != "sw_app.2" || *remote.Service != "app" || len(remote.Networks) != 1 || remote.Networks[0] != "preview-ingress" {
		t.Errorf("remote task: %+v", remote)
	}
	if len(rt.Routes) != 1 || rt.Routes[0].Container != "app" || strings.Join(rt.Routes[0].Hosts, ",") != "app-sw.example.com" {
		t.Errorf("routes from SERVICE labels: %+v", rt.Routes)
	}
	if find(rt, "runs on node wrk") == nil {
		t.Errorf("the remote task must be named: %v", rt.Findings)
	}
	if find(rt, "exposedbydefault") != nil || find(rt, "not attached") != nil {
		t.Errorf("the service is enabled and on the ingress: %v", rt.Findings)
	}
}

// jobRunner fakes a swarm stack holding ONE replicated-job service (the seed) and nothing else.
// `replicas` is the progress column docker renders; `staleTask` adds a Complete task from a PREVIOUS
// job iteration, which is the case a task-counting implementation gets wrong.
func jobRunner(replicas string, staleTask bool) exec.Runner {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			return exec.Result{OK: true, Stdout: ""}, true
		case strings.HasPrefix(cmd, "docker service ls"):
			return exec.Result{OK: true, Stdout: `{"ID":"seed1","Name":"sw_seed","Mode":"replicated job","Replicas":"` + replicas + `"}` + "\n"}, true
		case strings.HasPrefix(cmd, "docker service inspect"):
			return exec.Result{OK: true, Stdout: `[{"ID":"seed1full0000","Spec":{"Name":"sw_seed","Labels":{"com.docker.stack.namespace":"sw"},"TaskTemplate":{"ContainerSpec":{"Image":"seed:1"},"Networks":[]}}}]`}, true
		case strings.HasPrefix(cmd, "docker network ls"):
			return exec.Result{OK: true, Stdout: ""}, true
		case strings.HasPrefix(cmd, "docker stack ps"):
			if staleTask {
				return exec.Result{OK: true, Stdout: `{"ID":"old1","Name":"sw_seed.1","Image":"seed:1","Node":"mgr","DesiredState":"Running","CurrentState":"Complete 9 minutes ago"}` + "\n"}, true
			}
			return exec.Result{OK: true, Stdout: ""}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func TestReplicatedJobVerdictComesFromTheServiceNotItsTasks(t *testing.T) {
	// negative control: drop the `len(jobsByName) > 0` block — every case below yields 0 containers
	// (a finished job's task is never desired-running, so nothing survives the corpse skip), and a
	// stack whose seed FAILED then reads as ready over whatever else is in it.
	// negative control (Job): drop `Job: true` from the synthetic row — the marker check fails, and
	// the UI is back to offering Start/Stop on a "container" that is really a service id.
	// negative control (finding): drop the unparseable-column `else` branch — the finding check
	// fails, and an operator watching readiness burn its deadline gets no diagnosis.
	cases := []struct {
		name      string
		replicas  string
		staleTask bool
		wantState string
		wantExit  bool // exit code 0 present ⇒ readiness calls it ready
	}{
		{"completed", "0/0 (1/1 completed)", false, "exited", true},
		{"mid-run", "1/1 (0/1 completed)", false, "created", false},
		{"retries exhausted, nothing completed", "0/1 (0/1 completed)", false, "created", false},
		// The case task-counting gets wrong: a Complete task from the PREVIOUS iteration is present,
		// but THIS iteration has completed nothing. Must not read as done.
		{"stale prior-iteration task", "0/1 (0/1 completed)", true, "created", false},
		// Fail closed: a progress column we cannot parse is NOT evidence the seed ran.
		{"unparseable progress", "something else", false, "created", false},
	}
	for _, c := range cases {
		rt := DeploymentRuntime(RuntimeArgs{Stack: "sw", Runner: jobRunner(c.replicas, c.staleTask), Challenge: DNS01, Orchestrator: spec.Swarm})
		if len(rt.Containers) != 1 {
			t.Fatalf("%s: want exactly one entry for the job service, got %d: %+v", c.name, len(rt.Containers), rt.Containers)
		}
		got := rt.Containers[0]
		if got.State != c.wantState {
			t.Fatalf("%s: state = %q, want %q", c.name, got.State, c.wantState)
		}
		if hasExit := got.ExitCode != nil && *got.ExitCode == 0; hasExit != c.wantExit {
			t.Fatalf("%s: exit-0 = %v, want %v", c.name, hasExit, c.wantExit)
		}
		if got.Service == nil || *got.Service != "seed" {
			t.Fatalf("%s: service name not carried: %+v", c.name, got.Service)
		}
		// The row is synthesised from the SERVICE — its id/name answer to no container, so the UI
		// needs the marker to withhold start/stop/shell.
		if !got.Job {
			t.Fatalf("%s: the synthetic service row must carry job:true: %+v", c.name, got)
		}
		// Fail closed AND say why: an unparseable column must surface a finding quoting the raw
		// text, or readiness reads `created` forever with no diagnosis.
		f := find(rt, "replicas column")
		if c.replicas == "something else" {
			if f == nil || !strings.Contains(f.Message, `"something else"`) {
				t.Fatalf("%s: want a finding quoting the raw column, got %v", c.name, rt.Findings)
			}
		} else if f != nil {
			t.Fatalf("%s: a parseable column must not warn: %+v", c.name, f)
		}
	}
}

// swarmRouteRunner is ONE swarm service (`sw_app`, one router, port 80) whose ingress address is
// discoverable in different ways: `vip` is what `docker service inspect` reports on
// Endpoint.VirtualIPs (empty for none — endpoint_mode: dnsrr, or an older daemon), `localTask` puts
// a running task of it ON THIS NODE with its own ingress IP, `netLs` is what `docker network ls`
// answers, and `svcNet` is the network id the service declares.
func swarmRouteRunner(vip string, localTask bool, netLs, svcNet string) exec.Runner {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			if localTask {
				return exec.Result{OK: true, Stdout: "aaa111aaa111\n"}, true
			}
			return exec.Result{OK: true, Stdout: ""}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: `[{"Id":"aaa111aaa111","Name":"/sw_app.1.task1","Config":{"Image":"nginx","Labels":{"com.docker.stack.namespace":"sw","com.docker.swarm.service.name":"sw_app","com.docker.swarm.task.id":"task1"}},"State":{"Status":"running"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}}]`}, true
		case strings.HasPrefix(cmd, "docker service ls"):
			return exec.Result{OK: true, Stdout: `{"ID":"svc1","Name":"sw_app","Mode":"replicated","Replicas":"1/1"}` + "\n"}, true
		case strings.HasPrefix(cmd, "docker service inspect"):
			endpoint := ""
			if vip != "" {
				endpoint = `,"Endpoint":{"VirtualIPs":[{"NetworkID":"` + svcNet + `","Addr":"` + vip + `"}]}`
			}
			return exec.Result{OK: true, Stdout: "[{\"ID\":\"svc1xxxxxxxxxxxx\",\"UpdatedAt\":\"2026-08-20T10:00:00Z\",\"Spec\":{\"Name\":\"sw_app\",\"Labels\":{\"com.docker.stack.namespace\":\"sw\",\"traefik.enable\":\"true\",\"traefik.swarm.network\":\"preview-ingress\",\"traefik.http.routers.app-sw.rule\":\"Host(`app-sw.example.com`)\",\"traefik.http.services.app-sw.loadbalancer.server.port\":\"80\"},\"TaskTemplate\":{\"ContainerSpec\":{\"Image\":\"nginx\"},\"Networks\":[{\"Target\":\"" + svcNet + "\"}]}}" + endpoint + "}]"}, true
		case strings.HasPrefix(cmd, "docker network ls"):
			return exec.Result{OK: true, Stdout: netLs}, true
		case strings.HasPrefix(cmd, "docker stack ps"):
			if localTask {
				return exec.Result{OK: true, Stdout: `{"ID":"task1","Name":"sw_app.1","Image":"nginx","Node":"mgr","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}` + "\n"}, true
			}
			return exec.Result{OK: true, Stdout: `{"ID":"task9","Name":"sw_app.1","Image":"nginx","Node":"wrk","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}` + "\n"}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func TestSwarmRouteTargetIsTheServiceVIPThenALocalTask(t *testing.T) {
	// negative control: pass nil for the ingress IP at the swarm routes call site (what it did
	// before) — every case below loses its target and the first two fail. Second mutation, run
	// separately: drop the `strings.Cut(v.Addr, "/")` and use v.Addr — the VIP case forwards to
	// "10.0.9.2/24:80", an address that dials nowhere. Third: make OnIngress return &false when
	// NetworksKnown is false — the unresolved-network case claims "not-on-ingress" about a service
	// that is on it, which is the original bug wearing the new field's name.
	const ingressID, otherID = "net1full0000abcd", "net2full0000abcd"
	ingressLs := "net1full0000 preview-ingress\nnet2full0000 sw_default\n"
	cases := []struct {
		name       string
		vip        string
		localTask  bool
		netLs      string
		svcNet     string
		wantTarget string
		wantReason string
	}{
		// The VIP is what the swarm provider dials, and the manager knows it wherever tasks run —
		// so it wins even when a task of this service is sitting on this node.
		{"the service VIP, mask stripped", "10.0.9.2/24", true, ingressLs, ingressID, "10.0.9.2:80", ""},
		// No VIP (dnsrr): a task on THIS node has an address, and it is the same `docker inspect`
		// the compose path reads.
		{"a task on this node", "", true, ingressLs, ingressID, "10.0.1.5:80", ""},
		// Nothing here can know it: no VIP, every task elsewhere. NOT a network diagnosis.
		{"no VIP and every task remote", "", false, ingressLs, ingressID, "", "unknown-node"},
		// `docker network ls` did not answer, so the service's network is a raw id — "this is not
		// preview-ingress" is exactly the conclusion that must not be drawn from that.
		{"network names unresolved", "", false, "", ingressID, "", "unknown-node"},
		// Measured, and it really is off the ingress network.
		{"genuinely off the ingress network", "", false, ingressLs, otherID, "", "not-on-ingress"},
	}
	for _, c := range cases {
		rt := DeploymentRuntime(RuntimeArgs{Stack: "sw", Runner: swarmRouteRunner(c.vip, c.localTask, c.netLs, c.svcNet), Challenge: DNS01, Orchestrator: spec.Swarm})
		if len(rt.Routes) != 1 {
			t.Fatalf("%s: want one route, got %+v", c.name, rt.Routes)
		}
		got := rt.Routes[0]
		target := ""
		if got.Target != nil {
			target = *got.Target
		}
		if target != c.wantTarget || got.TargetReason != c.wantReason {
			t.Errorf("%s: target %q reason %q, want %q / %q", c.name, target, got.TargetReason, c.wantTarget, c.wantReason)
		}
		if strings.Contains(target, "/") {
			t.Errorf("%s: %q is a CIDR, not an address Traefik dials", c.name, target)
		}
	}
}
