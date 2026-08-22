package image

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// fakeBinary is a stand-in for the running Linux binary: Build copies whatever file it is handed,
// so the test never depends on the host's own architecture.
func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pstack")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho stub\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

var buildRe = regexp.MustCompile(`docker build --pull -t "[^"]+" "([^"]+)"`)

func findCmd(log []string, prefix string) (int, string) {
	for i, c := range log {
		if strings.HasPrefix(c, prefix) {
			return i, c
		}
	}
	return -1, ""
}

// The bug this closes: an installed pstack had no Dockerfile to build the control image from and
// no registry to pull it from, while `init` refuses to run without it. The way out is that the
// running binary IS the whole application: the context is a copy of it plus the Dockerfile.
func TestBuildImage(t *testing.T) {
	t.Run("refuses on a non-Linux build and names the two alternatives", func(t *testing.T) {
		// negative control: drop the GOOS check — a darwin binary is copied into a Linux image.
		r := exec.NewFake(nil, "")
		err := Build(BuildOptions{Tag: "pstack:test", Runner: r, GOOS: "darwin", Out: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), "docker build -t pstack:local .") || !strings.Contains(err.Error(), "PSTACK_BINARY=<path>") {
			t.Fatalf("got %v", err)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("ran %v", r.Commands())
		}
	})

	t.Run("builds the configured tag and cleans up its context", func(t *testing.T) {
		// negative control: drop `defer os.RemoveAll(ctx)` in Build — the Dockerfile survives and the exists check fails.
		r := exec.NewFake(nil, "")
		var out bytes.Buffer
		if err := Build(BuildOptions{Tag: "pstack:test", Runner: r, Binary: fakeBinary(t), Out: &out}); err != nil {
			t.Fatal(err)
		}
		_, cmd := findCmd(r.Commands(), "docker build")
		if cmd == "" {
			t.Fatal("no docker build command")
		}
		if !strings.Contains(cmd, `"pstack:test"`) {
			t.Errorf("tag not JSON-quoted: %s", cmd)
		}
		// --pull, so a stale cached base image cannot silently pin an old Bun runtime.
		if !strings.Contains(cmd, "--pull") {
			t.Errorf("no --pull: %s", cmd)
		}
		// The context is a temp dir, never the install directory: an install may be read-only or
		// shared, and writing a Dockerfile into it would mutate an installed dependency.
		m := buildRe.FindStringSubmatch(cmd)
		if m == nil {
			t.Fatalf("unexpected build command: %s", cmd)
		}
		if !strings.Contains(m[1], "pstack-image-") {
			t.Errorf("context not a pstack-image- temp dir: %s", m[1])
		}
		// Removed even on success — a build context left in /tmp is litter nobody looks for.
		if exists(filepath.Join(m[1], "Dockerfile")) {
			t.Error("build context not removed")
		}
	})

	t.Run("the Dockerfile reaches docker byte-for-byte, never through a shell", func(t *testing.T) {
		// The regression: the default path built `printf '%s' <json> | docker build -`. JSON escaping
		// is not shell escaping — in double quotes `\n` stays two literal characters and every backtick
		// in the Dockerfile's own comments is command substitution, so `docker compose`'s help text got
		// spliced into line 37 and the build died on `unknown instruction: Define`.
		//
		// So: assert the file docker is handed is identical to what we generated, and that the command
		// does not carry the document at all.
		// negative control: write `df + "x"` to the Dockerfile in Build — the byte compare fails.
		written := ""
		r := exec.NewFake(nil, "")
		r.Answer = func(cmd string) (exec.Result, bool) {
			// The retag that precedes every build is not the build.
			if strings.HasPrefix(cmd, "docker image tag") {
				return exec.Result{OK: true}, true
			}
			m := buildRe.FindStringSubmatch(cmd)
			if m == nil {
				t.Errorf("unexpected command %s", cmd)
				return exec.Result{OK: false, Code: 1}, true
			}
			if strings.Contains(cmd, "FROM") { // the document is not on the command line
				t.Errorf("Dockerfile on the command line: %s", cmd)
			}
			b, err := os.ReadFile(filepath.Join(m[1], "Dockerfile"))
			if err != nil {
				t.Error(err)
			}
			written = string(b)
			return exec.Result{OK: true}, true
		}
		var out bytes.Buffer
		if err := Build(BuildOptions{Tag: "pstack:test", Runner: r, Binary: fakeBinary(t), Out: &out}); err != nil {
			t.Fatal(err)
		}
		if written != ControlDockerfile("") {
			t.Error("control Dockerfile differs from ControlDockerfile()")
		}
		if !strings.Contains(written, "`docker compose`") { // the backticks that caused it, intact
			t.Error("backticks lost")
		}
		if err := Build(BuildOptions{Tag: "pstack-ui:test", Runner: r, UI: true, Out: &out}); err != nil {
			t.Fatal(err)
		}
		if written != UIDockerfile("") {
			t.Error("UI Dockerfile differs from UIDockerfile()")
		}
	})

	t.Run("a failed build surfaces docker output instead of a bare exit code", func(t *testing.T) {
		// negative control: return only `docker build failed (exit N)` without the output — the substring check fails.
		r := exec.NewFake(nil, "")
		r.Answer = func(string) (exec.Result, bool) {
			return exec.Result{OK: false, Code: 1, Stderr: "no space left on device"}, true
		}
		err := Build(BuildOptions{Tag: "pstack:test", Runner: r, Binary: fakeBinary(t), Out: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), "no space left on device") {
			t.Fatalf("expected docker output in the error, got %v", err)
		}
	})

	t.Run("dry-run executes nothing", func(t *testing.T) {
		// negative control: drop the `if opts.DryRun` early return — the runner log is no longer empty.
		r := exec.NewFake(nil, "")
		var out bytes.Buffer
		if err := Build(BuildOptions{Tag: "pstack:test", Runner: r, DryRun: true, Binary: fakeBinary(t), Out: &out}); err != nil {
			t.Fatal(err)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("commands ran under dry-run: %v", r.Commands())
		}
		if !strings.Contains(out.String(), "  [dry-run] docker image tag pstack:test pstack:test-previous\n") ||
			!strings.Contains(out.String(), "  [dry-run] docker build -t pstack:test <context>\n") {
			t.Errorf("dry-run lines: %q", out.String())
		}
	})

	t.Run("keeps the previous image under <tag>-previous BEFORE building — the rollback path", func(t *testing.T) {
		// `docker build -t pstack:local` moves the tag and the image that was running becomes an
		// anonymous <none> layer. If the new build comes up unhealthy after init recreates the stack,
		// the control plane is gone and nothing names the image that worked. The retag has to come
		// first, or it would keep the new image and the point is lost.
		// negative control: swap the two runner calls in Build — the order assertion fails.
		r := exec.NewFake(nil, "")
		if err := Build(BuildOptions{Tag: "pstack:test", Runner: r, Binary: fakeBinary(t), Out: &bytes.Buffer{}}); err != nil {
			t.Fatal(err)
		}
		log := r.Commands()
		tagAt, tagCmd := findCmd(log, "docker image tag")
		buildAt, _ := findCmd(log, "docker build")
		if tagAt < 0 {
			t.Fatal("no retag")
		}
		if want := "docker image tag 'pstack:test' 'pstack:test-previous' 2>/dev/null || true"; tagCmd != want {
			t.Errorf("retag = %q, want %q", tagCmd, want)
		}
		if buildAt <= tagAt {
			t.Errorf("build (%d) must come after the retag (%d)", buildAt, tagAt)
		}
	})
}

// fakeUIPackage is a stand-in for an installed @samyx/preview-stacks-ui: built assets plus the nginx config.
func fakeUIPackage(t *testing.T, withConf bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("<!doctype html><title>ui</title>"), 0o666); err != nil {
		t.Fatal(err)
	}
	if withConf {
		if err := os.WriteFile(filepath.Join(root, "nginx.conf"), []byte("server { listen 80; }"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "dist")
}

func TestBuildImageUI(t *testing.T) {
	t.Run("builds the UI image from a package dist, taking nginx.conf beside it", func(t *testing.T) {
		// negative control: build with `UI: false` — the tag assertion below still passes but the nginx.conf copy is skipped; assert on the context instead (below).
		r := exec.NewFake(nil, "")
		hadConf := false
		r.Answer = func(cmd string) (exec.Result, bool) {
			if m := buildRe.FindStringSubmatch(cmd); m != nil {
				hadConf = exists(filepath.Join(m[1], "nginx.conf")) && exists(filepath.Join(m[1], "dist", "index.html"))
			}
			return exec.Result{OK: true}, true
		}
		if err := Build(BuildOptions{Tag: "pstack-ui:test", Runner: r, UI: true, UIDist: fakeUIPackage(t, true), Out: &bytes.Buffer{}}); err != nil {
			t.Fatal(err)
		}
		_, cmd := findCmd(r.Commands(), "docker build")
		if !strings.Contains(cmd, `"pstack-ui:test"`) {
			t.Errorf("tag: %s", cmd)
		}
		if !hadConf {
			t.Error("context lacked nginx.conf or dist/index.html")
		}
	})

	t.Run("refuses a package with assets but no nginx.conf", func(t *testing.T) {
		// Without it the image would serve the SPA with no /api proxy and no deep-link fallback —
		// a container that starts, looks healthy, and is unusable.
		// negative control: drop the `!exists(conf)` refusal — Build returns a read error naming a different message.
		err := Build(BuildOptions{Tag: "x", Runner: exec.NewFake(nil, ""), UI: true, UIDist: fakeUIPackage(t, false), Out: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), "no nginx.conf beside them") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an explicit --ui-dist without built assets fails by name", func(t *testing.T) {
		// negative control: drop the index.html check — the error becomes CopyFS's, without `no built UI at`.
		err := Build(BuildOptions{Tag: "x", Runner: exec.NewFake(nil, ""), UI: true, UIDist: "/nonexistent/dist", Out: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), "no built UI at") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestGeneratedDockerfiles(t *testing.T) {
	t.Run("the control image copies the binary; the UI image fetches its package", func(t *testing.T) {
		// negative control: drop the version pin from the UI `bun add` — the `@9.9.9` check fails.
		if !strings.Contains(UIDockerfile("9.9.9"), "@samyx/preview-stacks-ui@9.9.9") {
			t.Error("UI image not pinned")
		}
		df := ControlDockerfile("9.9.9")
		for _, want := range []string{"COPY pstack /usr/local/bin/pstack", `CMD ["pstack", "serve"]`, `CMD ["pstack", "healthcheck"]`,
			"releases/download/v9.9.9/pstack_linux_", "FROM debian:bookworm-slim", "/usr/local/bin/pstack --version"} {
			if !strings.Contains(df, want) {
				t.Errorf("control image lacks %q", want)
			}
		}
		if strings.Contains(df, "bun") || strings.Contains(df, "npm") {
			t.Error("control image still carries a JavaScript runtime")
		}
	})

	t.Run("the UI image takes both the assets and the nginx config from the package", func(t *testing.T) {
		// negative control: drop the nginx.conf COPY line — the first check fails.
		df := UIDockerfile("")
		if !strings.Contains(df, "/nginx.conf /etc/nginx/conf.d/default.conf") || !strings.Contains(df, "/dist /usr/share/nginx/html") {
			t.Error("UI Dockerfile does not take both from the package")
		}
	})

	// The conformance transcripts: `pstack dockerfile [--ui]` prints the Dockerfile plus one newline.
	t.Run("matches the dockerfile goldens byte-for-byte", func(t *testing.T) {
		// negative control: change any byte of ControlDockerfile — the compare fails.
		for name, df := range map[string]string{"dockerfile": ControlDockerfile(""), "dockerfile-ui": UIDockerfile("")} {
			b, err := os.ReadFile(filepath.Join(testfacts.Golden(t), "cli", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var g struct{ Stdout string }
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(df, version.Get(), "<VERSION>") + "\n"
			want := g.Stdout
			if got != want {
				t.Errorf("%s: rendered Dockerfile differs from the golden\n--- got\n%s\n--- want\n%s", name, got, want)
			}
		}
	})
}
