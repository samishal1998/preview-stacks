package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	// negative control: delete one entry from commandHelps — `pstack <that> --help` silently falls
	// back to the whole manual, which is the failure this table exists to prevent, and the shell
	// offers that command no flags at all.
	for _, c := range Commands {
		if c == "completion" {
			continue // its own page is the usage text's one line; it takes a shell, not flags
		}
		page := CommandHelp(c)
		if page == "" {
			t.Errorf("command %q has no help of its own", c)
			continue
		}
		if !strings.HasPrefix(page, "pstack "+c+" — ") {
			t.Errorf("%q's page must open by naming itself: %q", c, strings.SplitN(page, "\n", 2)[0])
		}
		// Every page ends by pointing at the full manual, because a per-command page is a dead end
		// for someone who does not yet know what else exists.
		if !strings.Contains(page, "pstack --help") {
			t.Errorf("%q's page does not point back at the manual", c)
		}
	}
	if CommandHelp("no-such-command") != "" {
		t.Error("an unknown command has no page")
	}
}

func TestTheShellIsOfferedTheFlagsTheHelpDescribes(t *testing.T) {
	// negative control: have completionFlags return globalFlags for everything — the shell stops
	// offering --dns-token-file, --challenge and the rest, and nobody notices, because a missing
	// completion reads as "my shell needs reloading".
	for _, tc := range []struct{ cmd, flag string }{
		{"init", "--dns-token-file"},
		{"init", "--challenge"},
		{"down", "--force"},
		{"cloud-init", "--admin-user"},
		{"build-image", "--tag"},
	} {
		got := completionFlags(tc.cmd)
		found := false
		for _, f := range got {
			if f == tc.flag {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should complete %s; got %v", tc.cmd, tc.flag, got)
		}
		// The flag the shell offers must be one the page actually documents.
		if !strings.Contains(CommandHelp(tc.cmd), tc.flag) {
			t.Errorf("%s completes %s but its help never mentions it", tc.cmd, tc.flag)
		}
	}
}

func TestTheGeneratedScriptsParse(t *testing.T) {
	// negative control: drop zshQuote and interpolate the summary raw — `ui`'s description contains
	// `<`, zsh reads it as a redirection, and the WHOLE script fails to parse. A completion script
	// fails silently, so nothing but this test would have said so. It did.
	for _, shell := range Shells {
		script, exit := Completion(shell)
		if exit != nil {
			t.Fatalf("%s: %v", shell, exit)
		}
		if !strings.Contains(script, "pstack") || len(script) < 200 {
			t.Errorf("%s: script looks empty: %q", shell, script)
		}
		bin, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s not installed — syntax unchecked", shell)
			continue
		}
		path := filepath.Join(t.TempDir(), "completion."+shell)
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		// -n is "parse, do not execute" in bash, zsh and fish alike.
		if out, err := exec.Command(bin, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse its own completion script: %v\n%s", shell, err, out)
		}
	}
	if _, exit := Completion("powershell"); exit == nil {
		t.Error("an unknown shell must be refused, naming the ones that exist")
	}
}

func TestHelpForOneCommandIsNotTheWholeManual(t *testing.T) {
	// negative control: drop the `p.Cmd = rest[0]` line added to the --help case in args.go — the
	// command is lost before dispatch reads it and every `pstack <cmd> --help` prints the manual,
	// which is what it did before this existed.
	p, err := ParseArgs([]string{"init", "--help"}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if !p.Help || p.Cmd != "init" {
		t.Fatalf("`init --help` must keep the command: help=%v cmd=%q", p.Help, p.Cmd)
	}
	// And a bare --help still asks for everything.
	bare, err := ParseArgs([]string{"--help"}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if !bare.Help || bare.Cmd != "" {
		t.Fatalf("bare --help is the manual: help=%v cmd=%q", bare.Help, bare.Cmd)
	}
}
