package pstack

// agent.sh runs against fixtures under templates/agent/testdata: fake docker, df and nproc on PATH,
// and /proc files through PSTACK_AGENT_ROOT. Each case runs under every awk this machine has
// (system, mawk, busybox), because the image ships busybox and CI ships dash and mawk: a gawk-only
// construct, a gawk reserved name or busybox's 2^31 `%d` truncation fails here rather than on a host.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// awk variants, each linked as `awk` ahead of /usr/bin. The shell is always `sh`: Ubuntu's
// `busybox sh` runs its own df and nproc applets ahead of PATH, past the fakes.
var agentAwks = []string{"awk", "mawk", "busybox"}

func runAgent(t *testing.T, awk string, extraEnv ...string) (stdout, stderr, dfArgs string) {
	t.Helper()
	testdata, err := filepath.Abs("templates/agent/testdata")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := exec.LookPath(awk)
	if err != nil {
		t.Skipf("%s not installed", awk)
	}
	awkDir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(awkDir, "awk")); err != nil {
		t.Fatal(err)
	}
	dfFile := filepath.Join(t.TempDir(), "df-args")
	cmd := exec.Command("sh", "-c", AgentScript)
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(testdata, "bin") + ":" + awkDir + ":/usr/bin:/bin",
		"PSTACK_AGENT_ROOT=" + testdata,
		"PSTACK_AGENT_ONCE=1",
		"DOCKER_HOST=unix:///nonexistent",
		"FAKE_DF_ARGS=" + dfFile,
	}, extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("agent.sh: %v\nstderr: %s", err, errb.String())
	}
	args, _ := os.ReadFile(dfFile)
	return out.String(), errb.String(), string(args)
}

// negative control: print memTotal with `%d` instead of `%.0f` — the busybox case fails (-2147483648).
func TestAgentScriptPrintsOneBatch(t *testing.T) {
	want, err := os.ReadFile("templates/agent/testdata/agent-output.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, awk := range agentAwks {
		// negative control: swap "$stats" and "$ps" in the final printf — ctr lines follow stats order (the
		// fixture lists them differently) with stats fields as labels.
		// negative control: drop the `"`/`\` stack check — two of the fake's last three containers print, one
		// with a second "stack" key. Make `NF == 3` `NF >= 1` — the `|` one prints as stack "x".
		t.Run(awk, func(t *testing.T) {
			stdout, stderr, dfArgs := runAgent(t, awk)
			if stdout != string(want) {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
			}
			if stderr != "" {
				t.Errorf("stderr: %s", stderr)
			}
			if dfArgs != "-P /host/var/lib/docker\n" {
				t.Errorf("df argv %q, want %q", dfArgs, "-P /host/var/lib/docker\n")
			}
		})
	}
}

// negative control: drop `|| return 1` after the docker info read — a partial batch is printed.
func TestAgentScriptPrintsNothingWhenDockerFails(t *testing.T) {
	for _, read := range []string{"info", "ps", "stats"} {
		for _, awk := range agentAwks {
			// negative control: drop `|| return 1` after the docker stats read — the stats case prints a batch.
			t.Run(read+"/"+awk, func(t *testing.T) {
				if stdout, _, _ := runAgent(t, awk, "FAKE_DOCKER_FAIL="+read); stdout != "" {
					t.Errorf("stdout %q, want empty", stdout)
				}
			})
		}
	}
}
