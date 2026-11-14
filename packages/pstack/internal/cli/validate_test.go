package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The compose file is read from the shell's directory, as up/down read it; the spec sits in a
// subdirectory so a spec-relative read finds nothing.
// negative control: read the generated labels as []any (the old strs) → the line prints
// `https://? (container port ?)`.
// negative control: read filepath.Join(filepath.Dir(abs(args.File)), st.Compose.File) in validate →
// no "generated:" line.
func TestValidateShowsTheGeneratedHost(t *testing.T) {
	dir := t.TempDir()
	spec := "version: 1\nstack: pr-1\nenv:\n  PREVIEW_DOMAIN: preview.example.com\ncompose:\n  file: compose.yml\naxes: []\n"
	compose := "services:\n  web:\n    image: nginx\n    labels: [pstack.routing.port=80]\n"
	if err := os.Mkdir(filepath.Join(dir, "ci"), 0o777); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"ci/preview.yml": spec, "compose.yml": compose} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	var out, errOut bytes.Buffer
	code := Run([]string{"-f", "ci/preview.yml", "validate"}, IO{Stdout: &out, Stderr: &errOut, Env: func(string) (string, bool) { return "", false }})
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

// negative control: restore the old "interpolate it into a router label" line → the router line is missing.
func TestValidateSubdomainLines(t *testing.T) {
	dir := t.TempDir()
	spec := "version: 1\nstack: pr-1\nenv:\n  PREVIEW_DOMAIN: preview.example.com\ncompose:\n  file: compose.yml\n  profiles: [web]\n  subdomains: [web]\naxes: []\n"
	if err := os.WriteFile(filepath.Join(dir, "preview.yml"), []byte(spec), 0o666); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"-f", filepath.Join(dir, "preview.yml"), "validate"}, IO{Stdout: &out, Stderr: &errOut, Env: func(string) (string, bool) { return "", false }})
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
	want := "  subdomains: *.web-pr-1.preview.example.com → web  (one label)\n" +
		"              router generated; ${PSTACK_WILD_WEB} is for a service with its own traefik labels\n" +
		"              TLS: DNS-01 hosts order one certificate per deployment; other modes serve none\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("validate printed:\n%s", out.String())
	}
}
