package nodestats

import (
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
