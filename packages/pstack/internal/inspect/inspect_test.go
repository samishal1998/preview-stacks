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
	ip := "172.20.0.5"
	r := RoutesFromLabels("pr-1-app-1", map[string]string{
		"traefik.enable":                                         "true",
		"traefik.http.routers.app-pr1.rule":                      "Host(`app-pr-1.example.com`)",
		"traefik.http.routers.app-pr1.service":                   "app-pr1",
		"traefik.http.services.app-pr1.loadbalancer.server.port": "80",
	}, &ip)
	if len(r) != 1 || strings.Join(r[0].Hosts, ",") != "app-pr-1.example.com" || r[0].Port == nil || *r[0].Port != 80 || r[0].Target == nil || *r[0].Target != "172.20.0.5:80" {
		t.Errorf("got %+v", r)
	}
	// A service label named after the router is picked up without an explicit .service.
	ip2 := "10.0.0.2"
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "3000"}, &ip2)
	if *r[0].Port != 3000 || *r[0].Target != "10.0.0.2:3000" {
		t.Errorf("got %+v", r[0])
	}
	// An empty .service label does not become a port of "".
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.routers.app.service": "", "traefik.http.services.app.loadbalancer.server.port": "8080"}, &ip2)
	if r[0].Service != nil || *r[0].Port != 8080 || *r[0].Target != "10.0.0.2:8080" {
		t.Errorf("got %+v", r[0])
	}
	// No ingress IP means no target, rather than a half-built one.
	r = RoutesFromLabels("c", map[string]string{"traefik.http.routers.app.rule": "Host(`a.example.com`)", "traefik.http.services.app.loadbalancer.server.port": "80"}, nil)
	if *r[0].Port != 80 || r[0].Target != nil {
		t.Errorf("got %+v", r[0])
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
			return exec.Result{OK: true, Stdout: "svc1\n"}, true
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
