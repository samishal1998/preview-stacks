package inspect

import (
	"errors"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
)

// controlHost scripts a docker whose control project holds traefik (OOM-killed, 3 restarts, 256m)
// and pstack.
func controlHost() *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq"):
			if !strings.Contains(cmd, "com.docker.compose.project=pstack-control") {
				return exec.Result{OK: true, Stdout: ""}, true
			}
			return exec.Result{OK: true, Stdout: "t1\np1\n"}, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: `[
			  {"Id":"t1","Name":"/pstack-control-traefik-1","RestartCount":3,
			   "Config":{"Image":"traefik:v3.6.1","Labels":{"com.docker.compose.service":"traefik"}},
			   "State":{"Status":"running","StartedAt":"2026-08-27T12:00:15Z","OOMKilled":true},
			   "HostConfig":{"Memory":268435456}},
			  {"Id":"p1","Name":"/pstack-control-pstack-1","RestartCount":0,
			   "Config":{"Image":"pstack:local","Labels":{"com.docker.compose.service":"pstack"}},
			   "State":{"Status":"running","StartedAt":"2026-08-27T01:00:00Z"},
			   "HostConfig":{"Memory":0}}
			]`}, true
		case strings.HasPrefix(cmd, "docker restart"):
			return exec.Result{OK: true}, true
		}
		return exec.Result{}, false
	}
	return f
}

func TestControlRuntimeSurfacesRestartsAndOOM(t *testing.T) {
	// negative control: drop the OOMKilled/HostConfig fields from rawInspect — both new fields
	// zero out and the page loses the two numbers it exists to show.
	v := ControlRuntime(controlHost())
	if !v.Reachable || len(v.Containers) != 2 {
		t.Fatalf("view: %+v", v)
	}
	// pstack sorts before traefik; the order is the service name's, not docker's.
	if *v.Containers[0].Service != "pstack" || *v.Containers[1].Service != "traefik" {
		t.Fatalf("order: %+v", v.Containers)
	}
	tr := v.Containers[1]
	if !tr.OOMKilled || tr.RestartCount != 3 || tr.MemLimitBytes == nil || *tr.MemLimitBytes != 268435456 {
		t.Errorf("traefik: %+v", tr)
	}
	if v.Containers[0].OOMKilled {
		t.Error("pstack was never OOM-killed")
	}
	// Docker says Memory:0 for "no limit"; the wire must carry nil — one encoding per fact.
	if v.Containers[0].MemLimitBytes != nil {
		t.Errorf("unlimited must be nil, not %d", *v.Containers[0].MemLimitBytes)
	}

	// Docker not answering is "unknown", never "empty" — same rule as DeploymentRuntime.
	down := ControlRuntime(exec.NewFake(func(string) bool { return true }, ""))
	if down.Reachable || down.Containers == nil || len(down.Containers) != 0 {
		t.Errorf("unreachable: %+v", down)
	}
}

func TestRestartControlServiceRefusesItself(t *testing.T) {
	// negative control: drop the selfService check — the API restarts its own container, killing
	// the request (and any job) in flight, which is the exact incident the template header names.
	f := controlHost()
	if _, err := RestartControlService(f, "pstack"); !errors.Is(err, ErrRestartSelf) {
		t.Fatalf("pstack must be refused, got %v", err)
	}
	for _, c := range f.Log {
		if strings.HasPrefix(c, "docker restart") {
			t.Fatal("the refusal must happen before any docker command")
		}
	}

	name, err := RestartControlService(f, "traefik")
	if err != nil || name != "pstack-control-traefik-1" {
		t.Fatalf("traefik: %q %v", name, err)
	}
	found := false
	for _, c := range f.Log {
		if c == "docker restart 't1'" {
			found = true
		}
	}
	if !found {
		t.Errorf("restart must target the inspected id: %v", f.Log)
	}

	if _, err := RestartControlService(f, "nothing"); !errors.Is(err, ErrNoService) {
		t.Errorf("unknown service: %v", err)
	}
	if _, err := RestartControlService(exec.NewFake(func(string) bool { return true }, ""), "traefik"); !errors.Is(err, ErrNoDocker) {
		t.Errorf("docker down: %v", err)
	}
}

// lokiPush is a push URL as init renders it, with a fixed password.
const lokiPush = "https://pstack:0123456789abcdef0123456789abcdef@loki.preview.example.com/loki/api/v1/push"

// lokiHost scripts the control project's `docker ps` and `docker inspect` separately, so a case can
// make either one lie. The fixtures spell the label keys as docker returns them, never through the
// Go constants, so a drifted constant fails here.
func lokiHost(ps exec.Result, inspect string) *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.HasPrefix(cmd, "docker ps -aq") && strings.Contains(cmd, "com.docker.compose.project=pstack-control"):
			return ps, true
		case strings.HasPrefix(cmd, "docker inspect"):
			return exec.Result{OK: true, Stdout: inspect}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func TestLokiPushURLComesFromTheControlStacksLokiContainer(t *testing.T) {
	// negative control: drop the `com.docker.compose.service == "loki"` check — traefik carrying the label reads as Loki.
	traefik := `{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.6.1","Labels":{"com.docker.compose.service":"traefik"}}}`
	loki := `{"Id":"l1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.service":"loki","pstack.logging.push-url":"` + lokiPush + `"}}}`

	t.Run("the loki container's label is the push URL", func(t *testing.T) {
		// negative control: change initctl.PushURLLabel to "pstack.logging.push_url" — the fixture's literal key no longer matches and the URL reads "".
		if got := LokiPushURL(lokiHost(exec.Result{OK: true, Stdout: "t1\nl1\n"}, "["+traefik+","+loki+"]")); got != lokiPush {
			t.Errorf("got %q", got)
		}
	})

	t.Run("the label on any other control service is not Loki", func(t *testing.T) {
		// negative control: drop the `com.docker.compose.service == "loki"` check — traefik's label is returned.
		squat := `{"Id":"t1","Name":"/pstack-control-traefik-1","Config":{"Labels":{"com.docker.compose.service":"traefik","pstack.logging.push-url":"` + lokiPush + `"}}}`
		if got := LokiPushURL(lokiHost(exec.Result{OK: true, Stdout: "t1\n"}, "["+squat+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("no control containers is logging off", func(t *testing.T) {
		// negative control: drop inspectIDs' `len(ids) == 0` return — a bare `docker inspect` is asked and this fake's loki answer leaks through.
		if got := LokiPushURL(lokiHost(exec.Result{OK: true}, "["+loki+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("docker not answering is logging off", func(t *testing.T) {
		// negative control: drop idsByLabel's `!res.OK` return — the failed `docker ps` still printed l1, and its inspect reads a URL.
		failed := exec.Result{OK: false, Code: 1, Stdout: "l1\n", Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}
		if got := LokiPushURL(lokiHost(failed, "["+loki+"]")); got != "" {
			t.Errorf("got %q", got)
		}
	})
}
