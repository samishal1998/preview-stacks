package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

type cliGolden struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func golden(t *testing.T, name string) cliGolden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testfacts.Golden(t), "cli", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var g cliGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func noEnv(string) (string, bool) { return "", false }

func TestUsageMatchesTheGolden(t *testing.T) {
	// negative control: change one character of the usage text — the byte comparison fails.
	g := golden(t, "help")
	if got := Usage("<VERSION>"); got != g.Stdout {
		t.Errorf("--help differs from golden/cli/help.json:\n%s", diffLines(g.Stdout, got))
	}
}

func TestUnknownCommandMatchesTheGolden(t *testing.T) {
	// negative control: drop the `Commands:` line — the golden fails.
	g := golden(t, "unknown-command")
	e := UnknownCommand("upgradee", "<VERSION>")
	if e.Code != g.Code || "pstack: "+e.Msg+"\n" != g.Stderr {
		t.Errorf("got code %d\n%s\nwant\n%s", e.Code, "pstack: "+e.Msg+"\n", g.Stderr)
	}
}

func TestFlagsAnywhereAndTheUIPeek(t *testing.T) {
	// negative control: make --ui always consume the next word — `build-image --ui --tag x` misparses.
	p, e := ParseArgs([]string{"down", "-n", "-v", "--set", "PR=7", "--set", "PR=8", "--set", "X=1", "--no-verify"}, noEnv)
	if e != nil {
		t.Fatal(e)
	}
	if p.Cmd != "down" || !p.DryRun || p.Level != "verbose" || !p.NoVerify || p.Overrides["PR"] != "8" || strings.Join(p.OverrideKeys, ",") != "PR,X" {
		t.Errorf("got %+v", p)
	}
	p, _ = ParseArgs([]string{"build-image", "--ui", "--tag", "x"}, noEnv)
	if !p.UIImage || p.Tag != "x" || p.UI != "basic" {
		t.Errorf("--ui as a switch: %+v", p)
	}
	p, _ = ParseArgs([]string{"init", "--ui", "advanced"}, noEnv)
	if p.UIImage || p.UI != "advanced" || !p.Typed["--ui"] {
		t.Errorf("--ui as a value: %+v", p)
	}
	if _, e := ParseArgs([]string{"init", "--ui", "fancy"}, noEnv); e == nil || e.Code != ExitUsage || e.Msg != `--ui must be basic or advanced, got "fancy"` {
		t.Errorf("bad --ui: %+v", e)
	}
	if _, e := ParseArgs([]string{"--bogus"}, noEnv); e == nil || e.Msg != "unknown flag --bogus" {
		t.Errorf("unknown flag: %+v", e)
	}
	if _, e := ParseArgs([]string{"validate", "--set", "=v"}, noEnv); e == nil || !strings.Contains(e.Msg, "KEY=VALUE") {
		t.Errorf("--set =v: %+v", e)
	}
	p, _ = ParseArgs([]string{"swarm", "join", "--format", "script"}, noEnv)
	if p.Cmd != "swarm" || p.Sub != "join" || p.Format != "script" {
		t.Errorf("sub: %+v", p)
	}
}

func TestEnvDefaultsUseTheRightNullishness(t *testing.T) {
	// negative control: read PSTACK_CHALLENGE with `get` instead of `or` — an empty value stops meaning http01.
	env := func(k string) (string, bool) {
		switch k {
		case "PSTACK_CHALLENGE", "PSTACK_ORCHESTRATOR", "PSTACK_UI":
			return "", true // set but empty: the `||` sites fall back
		case "PSTACK_DOMAIN":
			return "", true // the `??` site keeps the empty string
		case "PSTACK_IMAGE":
			return "custom:tag", true
		}
		return "", false
	}
	p, _ := ParseArgs([]string{"init"}, env)
	if p.Challenge != "http01" || p.Orchestrator != "swarm" || p.UI != "basic" || p.Tag != "custom:tag" || p.Domain != "" {
		t.Errorf("got %+v", p)
	}
}

func diffLines(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + itoa(i+1) + ":\n want " + a + "\n got  " + b
		}
	}
	return "(no line differs)"
}

func itoa(i int) string { return strconv.Itoa(i) }
