package nodestats

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// negative control: drop swarm.Shq around the script → every builder subtest fails.
func TestAgentCommands(t *testing.T) {
	t.Run("container", func(t *testing.T) {
		// negative control: swap --restart unless-stopped for --restart always → fails.
		want := "docker rm -f pstack-agent >/dev/null 2>&1; docker run -d --name pstack-agent --restart unless-stopped" +
			" --label pstack.role=agent -v /var/run/docker.sock:/var/run/docker.sock:ro -v /:/host:ro" +
			" --log-driver json-file --log-opt max-size=1m --entrypoint sh docker:29-cli -c 'echo hi'"
		if got := AgentContainerCmd("echo hi", AgentImage); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("service", func(t *testing.T) {
		// negative control: drop --mode global → fails.
		// negative control: set constraint to "" whatever the id → fails.
		want := "docker service rm pstack-agent >/dev/null 2>&1; docker service create --detach --mode global --name pstack-agent" +
			" --label pstack.role=agent --constraint 'node.id!=n1'" +
			" --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly" +
			" --mount type=bind,src=/,dst=/host,readonly --log-driver json-file --log-opt max-size=1m --restart-condition any" +
			" --entrypoint sh docker:29-cli -c 'echo hi'"
		if got := AgentServiceCmd("echo hi", AgentImage, "n1"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("service with no self id has no constraint", func(t *testing.T) {
		// negative control: always add the constraint → `node.id!=` appears with an empty id.
		want := "docker service rm pstack-agent >/dev/null 2>&1; docker service create --detach --mode global --name pstack-agent" +
			" --label pstack.role=agent" +
			" --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly" +
			" --mount type=bind,src=/,dst=/host,readonly --log-driver json-file --log-opt max-size=1m --restart-condition any" +
			" --entrypoint sh docker:29-cli -c 'echo hi'"
		if got := AgentServiceCmd("echo hi", AgentImage, ""); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("a quote in the script survives", func(t *testing.T) {
		// negative control: wrap the script in bare single quotes → `'it'\''s'` is not produced.
		want := `docker rm -f pstack-agent >/dev/null 2>&1; docker run -d --name pstack-agent --restart unless-stopped` +
			` --label pstack.role=agent -v /var/run/docker.sock:/var/run/docker.sock:ro -v /:/host:ro` +
			` --log-driver json-file --log-opt max-size=1m --entrypoint sh img -c 'it'\''s'`
		if got := AgentContainerCmd("it's", "img"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("logs", func(t *testing.T) {
		// negative control: drop --raw from the service logs line → fails.
		// negative control: start from a nil slice instead of the docker logs line → fails.
		local := "docker logs --since 90s pstack-agent 2>/dev/null"
		if got := AgentLogsCmds(false); !reflect.DeepEqual(got, []string{local}) {
			t.Errorf("compose: %q", got)
		}
		if got := AgentLogsCmds(true); !reflect.DeepEqual(got, []string{local, "docker service logs --since 90s --raw pstack-agent 2>/dev/null"}) {
			t.Errorf("swarm: %q", got)
		}
	})
}

// negative control: drop math.Round from ParseSize → 1.234GB lands one byte short.
func TestParseSize(t *testing.T) {
	// negative control: map kB to 1024 → 512kB reads 524288.
	for in, want := range map[string]int64{"0B": 0, "512kB": 512000, "1.234GB": 1234000000, "12.5MB": 12500000, "2TB": 2e12} {
		if got, ok := ParseSize(in); !ok || got != want {
			t.Errorf("ParseSize(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	// negative control: return true for an unknown unit → "1.2XB" reads as ok.
	for _, in := range []string{"junk", "", "GB", "1.2XB"} {
		if _, ok := ParseSize(in); ok {
			t.Errorf("ParseSize(%q) ok, want false", in)
		}
	}
}

// negative control: keep "-" as the node → the compose row is "-", not "".
// negative control: overwrite instead of adding → n2 reads 512000, not 1234512000.
func TestParsePrune(t *testing.T) {
	out := "pstack-prune v1 n2 1.234GB\nsome docker progress line\npstack-prune v1 - 0B\r\n" +
		"pstack-prune v1 n2 512kB\npstack-prune v1 n3 junk\n"
	want := []Reclaimed{{Node: "", Bytes: 0}, {Node: "n2", Bytes: 1234512000}}
	if got := ParsePrune(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// negative control: start from a nil slice → nothing parsed is nil.
	if got := ParsePrune(""); got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v, want []", got)
	}
}

// negative control: drop the builder line from PruneScript → the builder subtest's string differs.
func TestPruneScript(t *testing.T) {
	head := `n=$(docker info --format "{{.Swarm.NodeID}}" 2>/dev/null); n=${n:--}; `
	t.Run("scheduled", func(t *testing.T) {
		// negative control: print "until=168" without the h → fails.
		want := head + `docker image prune -af --filter until=168h | sed -n "s/^Total reclaimed space: /pstack-prune v1 $n /p"; ` +
			`docker builder prune -af --filter until=168h | sed -n "s/^Total\( reclaimed space\)\{0,1\}:[[:space:]]*/pstack-prune v1 $n /p"`
		if got := PruneScript(168, true); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("emergency", func(t *testing.T) {
		// negative control: keep the until filter at 0 → `--filter until=0h` appears.
		want := head + `docker image prune -af | sed -n "s/^Total reclaimed space: /pstack-prune v1 $n /p"`
		if got := PruneScript(0, false); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	// The script against docker's own output shapes, through a real sh: buildx (agent image) prints
	// `Total:\t<size>`, the classic builder (pstack image) `Total reclaimed space: <size>`.
	// negative control: match `Total reclaimed space:` for the builder only → the buildx 512kB is lost.
	// negative control: match `Total:` for the builder only → the classic 512kB is lost.
	// negative control: print the node with no "-" fallback → the compose line has an empty field.
	t.Run("through sh", func(t *testing.T) {
		dir := t.TempDir()
		fake := "#!/bin/sh\ncase \"$1 $2\" in\n" +
			"'info --format') printf '%s\\n' \"$NODE\" ;;\n" +
			"'image prune') printf 'Deleted Images:\\nuntagged: a:1\\n\\nTotal reclaimed space: 1.234GB\\n' ;;\n" +
			"'builder prune') if [ -n \"$CLASSIC\" ]; then printf 'Deleted build cache objects:\\nabc\\n\\nTotal reclaimed space: 512kB\\n';" +
			" else printf 'ID\\tRECLAIMABLE\\tSIZE\\nabc\\ttrue\\t512kB\\nTotal:\\t512kB\\n'; fi ;;\n" +
			"esac\n"
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
		classic := ""
		run := func(node string) []Reclaimed {
			cmd := osexec.Command("sh", "-c", PruneScript(168, true))
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "NODE=" + node, "CLASSIC=" + classic}
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			return ParsePrune(string(out))
		}
		if got, want := run("n2"), []Reclaimed{{Node: "n2", Bytes: 1234512000}}; !reflect.DeepEqual(got, want) {
			t.Errorf("swarm: got %+v, want %+v", got, want)
		}
		if got, want := run(""), []Reclaimed{{Node: "", Bytes: 1234512000}}; !reflect.DeepEqual(got, want) {
			t.Errorf("compose: got %+v, want %+v", got, want)
		}
		classic = "1"
		if got, want := run(""), []Reclaimed{{Node: "", Bytes: 1234512000}}; !reflect.DeepEqual(got, want) {
			t.Errorf("classic builder: got %+v, want %+v", got, want)
		}
	})
}

// negative control: drop swarm.Shq around the constraint → the quoted constraint is missing.
func TestPruneJobCmd(t *testing.T) {
	tail := ` --restart-condition none --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock --entrypoint sh` +
		` "$(docker service inspect -f '{{.Spec.TaskTemplate.ContainerSpec.Image}}' pstack-agent)" -c 'echo hi'`
	t.Run("global", func(t *testing.T) {
		// negative control: add --detach → a create returns before the job ran, and fails.
		want := `docker service create --name pstack-prune-1 --mode global-job --constraint 'node.id!=n1'` + tail
		if got := PruneJobCmd("pstack-prune-1", "echo hi", "node.id!=n1", true); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("replicated", func(t *testing.T) {
		// negative control: drop --replicas 1 → fails.
		want := `docker service create --name pstack-prune-1 --mode replicated-job --replicas 1 --constraint 'node.id==n2'` + tail
		if got := PruneJobCmd("pstack-prune-1", "echo hi", "node.id==n2", false); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("no constraint", func(t *testing.T) {
		// negative control: always add the constraint → `--constraint ''` appears.
		want := `docker service create --name pstack-prune-1 --mode global-job` + tail
		if got := PruneJobCmd("pstack-prune-1", "echo hi", "", true); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}
