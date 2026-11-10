package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// negative control: read the generated labels as []any (the old strs) → the line prints
// `https://? (container port ?)`.
func TestValidateShowsTheGeneratedHost(t *testing.T) {
	dir := t.TempDir()
	spec := "version: 1\nstack: pr-1\nenv:\n  PREVIEW_DOMAIN: preview.example.com\ncompose:\n  file: compose.yml\naxes: []\n"
	compose := "services:\n  web:\n    image: nginx\n    labels: [pstack.routing.port=80]\n"
	for name, body := range map[string]string{"preview.yml": spec, "compose.yml": compose} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"-f", filepath.Join(dir, "preview.yml"), "validate"}, IO{Stdout: &out, Stderr: &errOut, Env: func(string) (string, bool) { return "", false }})
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "https://web-pr-1.preview.example.com (container port 80)") {
		t.Fatalf("validate printed:\n%s", out.String())
	}
}

// negative control: append "completion" in sortedCommands again → it is listed twice.
func TestCompletionListsEachCommandOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range sortedCommands() {
		if seen[c] {
			t.Fatalf("%q listed twice", c)
		}
		seen[c] = true
	}
	if !seen["completion"] {
		t.Fatal("completion missing")
	}
}
