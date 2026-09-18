package initctl_test

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// okRunner reports success for everything, so preconditions pass without Docker — and answers the
// health probe with `healthy`, otherwise init's wait loop polls for 60s and the test times out
// rather than failing. `state` answers the swarm probe, `driver` the network-driver probe.
func okRunner(state, driver string) *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.Contains(cmd, "State.Health.Status"):
			return exec.Result{OK: true, Stdout: "healthy\n"}, true
		case strings.Contains(cmd, "Swarm.LocalNodeState"):
			return exec.Result{OK: true, Stdout: state + "\n"}, true
		case strings.Contains(cmd, "{{.Driver}}"):
			return exec.Result{OK: true, Stdout: driver}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// render runs init into a fresh data dir and returns the generated compose file.
func render(t *testing.T, over func(*initctl.Options)) (dir, yaml string) {
	t.Helper()
	dir = t.TempDir()
	o := initctl.Options{
		DataDir: dir, Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: initctl.HTTP01, Orchestrator: spec.Compose, Runner: okRunner("inactive", ""), Out: &bytes.Buffer{},
	}
	if over != nil {
		over(&o)
	}
	if err := initctl.Init(o); err != nil {
		t.Fatal(err)
	}
	return dir, read(t, filepath.Join(dir, "control", "docker-compose.yml"))
}

// These assert the CONTROL STACK MANIFEST, which is the artifact a host actually runs. Rendering
// it wrong is invisible until Traefik is up and certificates silently never arrive.
func TestInitACMEChallengeRendering(t *testing.T) {
	dns01 := func(o *initctl.Options) { o.Challenge, o.DNSProvider = initctl.DNS01, "hetzner" }

	t.Run("http01 is the default and needs no DNS credential", func(t *testing.T) {
		// negative control: return the dns01 block from AcmeChallengeArgs for http01 — fails.
		_, yaml := render(t, nil)
		for _, want := range []string{"acme.httpchallenge=true", "acme.httpchallenge.entrypoint=web", "routers.pstack-ui.tls.certresolver=le"} {
			if !strings.Contains(yaml, want) {
				t.Errorf("missing %q", want)
			}
		}
		// HTTP-01 cannot issue a wildcard, so no router may ask for one; and each hostname must
		// resolve its own cert, so routers DO carry certresolver.
		for _, no := range []string{"acme.dnschallenge=true", "tls.domains[0].sans"} {
			if strings.Contains(yaml, no) {
				t.Errorf("unexpected %q", no)
			}
		}
	})

	t.Run("dns01 renders the wildcard on exactly one router", func(t *testing.T) {
		// negative control: add `tls.domains[0].main` to pstack-api too — the count is 2.
		_, yaml := render(t, dns01)
		if !strings.Contains(yaml, "acme.dnschallenge=true") || !strings.Contains(yaml, "tls.domains[0].sans=*.${DOMAIN}") {
			t.Error("dns01 block missing")
		}
		if strings.Contains(yaml, "acme.httpchallenge=true") {
			t.Error("http01 block present")
		}
		// Exactly one router requests it; a second would order a separate cert and burn the
		// ~50-certs-per-registered-domain-per-week limit.
		if n := len(regexp.MustCompile(`tls\.domains\[0\]\.main`).FindAllString(yaml, -1)); n != 1 {
			t.Errorf("wildcard requested by %d routers", n)
		}
	})

	t.Run("both modes route control.<domain> and api.<domain> at one service", func(t *testing.T) {
		// negative control: make ControlUIService return `service=advanced-ui` for basic — fails.
		for _, over := range []func(*initctl.Options){nil, dns01} {
			_, yaml := render(t, over)
			for _, want := range []string{
				"routers.pstack-ui.rule=Host(`control.${DOMAIN}`)",
				"routers.pstack-api.rule=Host(`api.${DOMAIN}`)",
				// One service behind both, so the UI's relative /api/… calls stay same-origin (no CORS).
				"routers.pstack-ui.service=pstack",
				"routers.pstack-api.service=pstack",
			} {
				if !strings.Contains(yaml, want) {
					t.Errorf("missing %q", want)
				}
			}
		}
	})

	t.Run("no unrendered markers survive", func(t *testing.T) {
		// negative control: misspell one marker in the strings.Replace call — it survives.
		_, yaml := render(t, nil)
		if strings.Contains(yaml, "__ACME_CHALLENGE__") || strings.Contains(yaml, "__ACME_ROUTER_TLS__") {
			t.Error("marker survived")
		}
	})
}

func TestAdvancedUIIsOptIn(t *testing.T) {
	ui := func(ui initctl.UI) func(*initctl.Options) {
		return func(o *initctl.Options) { o.AcmeEmail, o.UI = "o@e.com", ui }
	}

	t.Run("basic adds no container at all", func(t *testing.T) {
		// negative control: return the service block from AdvancedUIService for basic — fails.
		_, yaml := render(t, ui(initctl.Basic))
		// Absent, not merely disabled — the basic UI is embedded in the API bundle, so there is
		// nothing extra to run, build or keep current.
		if strings.Contains(yaml, "advanced-ui") || !strings.Contains(yaml, "routers.pstack-ui.service=pstack") {
			t.Error("basic is not basic")
		}
	})

	t.Run("advanced adds the container and repoints control.<domain> at it", func(t *testing.T) {
		// negative control: drop the `#__ADVANCED_UI_SERVICE__` replacement — `  advanced-ui:` is missing.
		_, yaml := render(t, ui(initctl.Advanced))
		for _, want := range []string{
			"  advanced-ui:", "image: ${PSTACK_UI_IMAGE}", "routers.pstack-ui.service=advanced-ui",
			"services.advanced-ui.loadbalancer.server.port=80",
			// The API keeps api.<domain>, so a broken UI image never leaves the host with no interface.
			"routers.pstack-api.rule=Host(`api.${DOMAIN}`)",
		} {
			if !strings.Contains(yaml, want) {
				t.Errorf("missing %q", want)
			}
		}
	})

	t.Run("both modes leave no unrendered markers", func(t *testing.T) {
		// negative control: replace the marker with itself — fails.
		for _, u := range []initctl.UI{initctl.Basic, initctl.Advanced} {
			_, yaml := render(t, ui(u))
			if strings.Contains(yaml, "__CONTROL_UI_SERVICE__") || strings.Contains(yaml, "__ADVANCED_UI_SERVICE__") {
				t.Errorf("%s: marker survived", u)
			}
		}
	})

	t.Run("the rendered compose is valid YAML in both modes", func(t *testing.T) {
		// A marker replacement that broke indentation would otherwise only surface on the host, as a
		// compose parse error during `init`.
		// negative control: indent the advanced-ui block by one space — the parse fails.
		for _, u := range []initctl.UI{initctl.Basic, initctl.Advanced} {
			_, yaml := render(t, ui(u))
			if _, err := yamlx.ParseString(yaml); err != nil {
				t.Errorf("%s: %v", u, err)
			}
		}
	})
}

func TestInitRefusesMissingAdvancedUIImage(t *testing.T) {
	t.Run("fails by name instead of letting compose try to pull it", func(t *testing.T) {
		// What actually happened on a real host: the UI image was absent, compose tried to PULL
		// `pstack-ui:local`, got "pull access denied", and took the WHOLE control stack down with it —
		// Traefik included. An optional UI must not be able to kill the host.
		// negative control: drop the advanced-UI precondition from reqs — init succeeds.
		r := exec.NewFake(nil, "")
		r.Answer = func(cmd string) (exec.Result, bool) {
			missing := strings.Contains(cmd, "image inspect") && strings.Contains(cmd, "pstack-ui:local")
			res := exec.Result{OK: !missing}
			if missing {
				res.Code = 1
			}
			if strings.Contains(cmd, "State.Health.Status") {
				res.Stdout = "healthy\n"
			}
			return res, true
		}
		err := initctl.Init(initctl.Options{
			DataDir: t.TempDir(), Domain: "preview.example.com", AcmeEmail: "o@e.com",
			Challenge: initctl.HTTP01, UI: initctl.Advanced, Orchestrator: spec.Compose, Runner: r, Out: &bytes.Buffer{},
		})
		if err == nil || !regexp.MustCompile(`(?s)advanced UI image.*build-image --ui`).MatchString(err.Error()) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("the basic default never checks for it", func(t *testing.T) {
		// negative control: always append the advanced-UI precondition — init fails here.
		r := exec.NewFake(nil, "")
		r.Answer = func(cmd string) (exec.Result, bool) {
			// Fail the UI inspect if it is ever attempted — it must not be.
			if strings.Contains(cmd, "pstack-ui:local") {
				return exec.Result{OK: false, Code: 1}, true
			}
			if strings.Contains(cmd, "State.Health.Status") {
				return exec.Result{OK: true, Stdout: "healthy\n"}, true
			}
			return exec.Result{OK: true}, true
		}
		if err := initctl.Init(initctl.Options{
			DataDir: t.TempDir(), Domain: "preview.example.com", AcmeEmail: "o@e.com",
			Challenge: initctl.HTTP01, UI: initctl.Basic, Orchestrator: spec.Compose, Runner: r, Out: &bytes.Buffer{},
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// From features.test.ts.
func TestInitSwarmModeAndWakeCatchAll(t *testing.T) {
	type rendered struct {
		dir, yaml, env string
		log            []string
	}
	renderWith := func(t *testing.T, orchestrator spec.Orchestrator, runner *exec.Fake) rendered {
		t.Helper()
		dir := t.TempDir()
		if err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			UI: initctl.Basic, Orchestrator: orchestrator, Runner: runner, Out: &bytes.Buffer{},
		}); err != nil {
			t.Fatal(err)
		}
		return rendered{dir, read(t, filepath.Join(dir, "control", "docker-compose.yml")), read(t, filepath.Join(dir, "control", ".env")), runner.Commands()}
	}
	count := func(log []string, prefix string) int {
		n := 0
		for _, c := range log {
			if strings.HasPrefix(c, prefix) {
				n++
			}
		}
		return n
	}
	has := func(log []string, s string) bool { return count(log, s) > 0 }

	t.Run("swarm: swarm init when inactive, overlay attachable networks, the swarm provider, the env", func(t *testing.T) {
		// negative control: drop the `$$` from WakeRouterLabels — the verbatim rule assertion fails.
		r := renderWith(t, spec.Swarm, okRunner("inactive", ""))
		if !has(r.log, "docker swarm init") {
			t.Error("no swarm init")
		}
		if n := count(r.log, "docker network create -d overlay --attachable preview-"); n != 2 {
			t.Errorf("overlay creates: %d", n)
		}
		for _, want := range []string{
			"--providers.swarm=true", "--providers.swarm.network=preview-ingress",
			"--providers.docker=true", // the control stack's own routers
			"--metrics.prometheus.addrouterslabels=true",
			"PSTACK_DOMAIN: ${DOMAIN}", "PSTACK_TRAEFIK_METRICS: http://traefik:8082/metrics",
			"traefik.http.routers.pstack-wake.rule=HostRegexp(`^[a-z0-9-]+\\.preview\\.example\\.com$$`)",
			"traefik.http.routers.pstack-wake.priority=1",
		} {
			if !strings.Contains(r.yaml, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(r.yaml, "pstack-wake.tls.certresolver") {
			t.Error("wake router orders its own certificate")
		}
		if !strings.Contains(r.env, "PSTACK_ORCHESTRATOR=swarm") {
			t.Error("env lacks the orchestrator")
		}
		// Never the word upgrade greps for to detect dns01.
		if strings.Contains(strings.ToLower(r.yaml), "dnschallenge") {
			t.Error("http01 file mentions dnschallenge")
		}
		s, err := upgrade.ReadControlState(r.dir)
		if err != nil || s.Orchestrator != spec.Swarm {
			t.Errorf("read back: %v %v", s, err)
		}
		// Already a manager: no second init.
		again := renderWith(t, spec.Swarm, okRunner("active", ""))
		if has(again.log, "docker swarm init") {
			t.Error("second swarm init")
		}
	})

	t.Run("compose: no swarm init, bridge networks, no swarm provider; upgrade reads compose back", func(t *testing.T) {
		// negative control: default Orchestrator to swarm in ReadControlState when the line is absent — the last check fails.
		r := renderWith(t, spec.Compose, okRunner("inactive", ""))
		if has(r.log, "docker swarm init") {
			t.Error("swarm init under compose")
		}
		if n := count(r.log, "docker network create preview-"); n != 2 {
			t.Errorf("bridge creates: %d", n)
		}
		if strings.Contains(r.yaml, "--providers.swarm") {
			t.Error("swarm provider under compose")
		}
		if !strings.Contains(r.yaml, "pstack-wake.rule") { // the catch-all exists in both modes
			t.Error("no wake router")
		}
		if !strings.Contains(r.env, "PSTACK_ORCHESTRATOR=compose") {
			t.Error("env lacks the orchestrator")
		}
		if s, err := upgrade.ReadControlState(r.dir); err != nil || s.Orchestrator != spec.Compose {
			t.Errorf("read back: %v %v", s, err)
		}
		// A pre-0.26 .env (no line at all) is a compose host.
		var kept []string
		for _, l := range strings.Split(r.env, "\n") {
			if !strings.HasPrefix(l, "PSTACK_ORCHESTRATOR") {
				kept = append(kept, l)
			}
		}
		if err := os.WriteFile(filepath.Join(r.dir, "control", ".env"), []byte(strings.Join(kept, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		if s, err := upgrade.ReadControlState(r.dir); err != nil || s.Orchestrator != spec.Compose {
			t.Errorf("pre-0.26 read back: %v %v", s, err)
		}
	})

	t.Run("a network with the other driver is swapped only when nothing but the control stack is on it", func(t *testing.T) {
		// negative control: drop the `pstack-control-` prefix filter — the first case refuses too.
		// bridge exists, swarm wanted, only control containers attached → down, rm, recreate
		r := okRunner("active", "bridge\n")
		orig := r.Answer
		r.Answer = func(cmd string) (exec.Result, bool) {
			if strings.Contains(cmd, "range .Containers") {
				return exec.Result{OK: true, Stdout: "pstack-control-traefik-1 pstack-control-pstack-1\n"}, true
			}
			return orig(cmd)
		}
		got := renderWith(t, spec.Swarm, r)
		if !has(got.log, "docker network rm preview-ingress") {
			t.Error("network not removed")
		}
		if !some(got.log, func(c string) bool { return strings.Contains(c, "-p pstack-control down") }) {
			t.Error("control stack not taken down")
		}

		// a preview is attached → refuse, naming it
		r2 := okRunner("active", "bridge\n")
		orig2 := r2.Answer
		r2.Answer = func(cmd string) (exec.Result, bool) {
			if strings.Contains(cmd, "range .Containers") {
				return exec.Result{OK: true, Stdout: "pstack-control-traefik-1 pr-7-app-1\n"}, true
			}
			return orig2(cmd)
		}
		err := initctl.Init(initctl.Options{
			DataDir: t.TempDir(), Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			UI: initctl.Basic, Orchestrator: spec.Swarm, Runner: r2, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.Contains(err.Error(), "pr-7-app-1") {
			t.Fatalf("got %v", err)
		}
		if has(r2.Commands(), "docker network rm") {
			t.Error("removed a network with a preview on it")
		}
	})
}

func some(list []string, pred func(string) bool) bool {
	for _, s := range list {
		if pred(s) {
			return true
		}
	}
	return false
}

// The control container gets the first admin from .env or not at all: the compose template has
// passed ${PSTACK_ADMIN_USER:-} through since accounts existed, and Compose resolves it from this
// file — so a pair that never reaches .env is a pair the operator sets and never sees applied.
func TestInitCarriesTheFirstAdminIntoTheEnvFile(t *testing.T) {
	const password = "correct-horse-battery"
	initEnv := func(t *testing.T, user, pass string) (env, log string) {
		t.Helper()
		t.Setenv("PSTACK_ADMIN_USER", user)
		t.Setenv("PSTACK_ADMIN_PASSWORD", pass)
		dir := t.TempDir()
		var out bytes.Buffer
		if err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			UI: initctl.Basic, Orchestrator: spec.Compose, Runner: okRunner("inactive", ""), Out: &out,
		}); err != nil {
			t.Fatal(err)
		}
		return read(t, filepath.Join(dir, "control", ".env")), out.String()
	}

	t.Run("both set: the pair is in .env, and the password is in nothing init prints", func(t *testing.T) {
		// negative control: drop the admin block from envFile — both line assertions fail.
		env, log := initEnv(t, "alice", password)
		for _, want := range []string{"PSTACK_ADMIN_USER=alice\n", "PSTACK_ADMIN_PASSWORD=" + password + "\n"} {
			if !strings.Contains(env, want) {
				t.Errorf(".env lacks %q:\n%s", want, env)
			}
		}
		// Unlike a generated token, this password has an owner who already has a copy — printing it
		// puts it in the terminal scrollback and the cloud-init log for nothing.
		if strings.Contains(log, password) {
			t.Errorf("init printed the admin password:\n%s", log)
		}
	})

	t.Run("unset, or half a pair, writes no admin lines at all", func(t *testing.T) {
		// negative control: write the two lines unconditionally in envFile — both cases fail.
		// `serve` honours only a complete pair, so a half one is a slot that looks fillable by hand
		// and is not: the file is read at `up` time, and by then the users table has decided.
		for _, c := range []struct{ user, pass string }{{"", ""}, {"alice", ""}, {"", password}} {
			env, _ := initEnv(t, c.user, c.pass)
			if strings.Contains(env, "PSTACK_ADMIN_") {
				t.Errorf("user=%q pass=%q wrote an admin line:\n%s", c.user, c.pass, env)
			}
		}
	})
}

// The conformance cells: the golden generator's DATA_DIR, token and DNS credential.
const (
	goldenDataDir  = "/tmp/pstack-golden-data"
	goldenToken    = "golden-token-0123456789abcdef0123456789abcdef"
	goldenDNSToken = "golden-dns-token-0123456789"
)

type cell struct {
	challenge    initctl.Challenge
	ui           initctl.UI
	orchestrator spec.Orchestrator
}

func cells() []cell {
	var out []cell
	for _, c := range []initctl.Challenge{initctl.HTTP01, initctl.DNS01} {
		for _, u := range []initctl.UI{initctl.Basic, initctl.Advanced} {
			for _, o := range []spec.Orchestrator{spec.Compose, spec.Swarm} {
				out = append(out, cell{c, u, o})
			}
		}
	}
	return out
}

func (c cell) name() string {
	return string(c.challenge) + "-" + string(c.ui) + "-" + string(c.orchestrator)
}

func (c cell) options(dataDir string, dryRun bool, runner exec.Runner, out *bytes.Buffer) initctl.Options {
	o := initctl.Options{
		DataDir: dataDir, Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: c.challenge, UI: c.ui, Orchestrator: c.orchestrator, DryRun: dryRun, Runner: runner, Out: out,
	}
	if c.challenge == initctl.DNS01 {
		o.DNSProvider, o.Token = "cloudflare", goldenDNSToken
	}
	return o
}

// initShim is INIT_SHIM from packages/conformance/gen/goldens.table.ts: the health wait sees
// `healthy`, the swarm state is `active`, everything else succeeds silently.
func initShim() *exec.Fake {
	f := exec.NewFake(nil, "")
	f.Answer = func(cmd string) (exec.Result, bool) {
		switch {
		case strings.Contains(cmd, "State.Health.Status"):
			return exec.Result{OK: true, Stdout: "healthy\n"}, true
		case strings.Contains(cmd, "Swarm.LocalNodeState"):
			return exec.Result{OK: true, Stdout: "active\n"}, true
		}
		return exec.Result{OK: true}, true
	}
	return f
}

// mask is harness/goldens.ts mask(): the data dir to <DATA>, the token to <TOKEN>.
func mask(text, dataDir string) string {
	text = strings.ReplaceAll(text, dataDir, "<DATA>")
	text = strings.ReplaceAll(text, "/private"+dataDir, "<DATA>")
	return strings.ReplaceAll(text, goldenToken, "<TOKEN>")
}

func goldenCLI(t *testing.T, name string) (stdout string, code int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testfacts.Golden(t), "cli", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Stdout string
		Code   int
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g.Stdout, g.Code
}

// The test that protects the letsencrypt volume and upgrade's read-back: every cell's three files,
// byte-for-byte against golden/render/control/<cell>/.
func TestInitGoldens(t *testing.T) {
	t.Setenv("PSTACK_TOKEN", goldenToken)
	os.Unsetenv("PSTACK_IMAGE")
	os.Unsetenv("PSTACK_UI_IMAGE")
	// A stray PSTACK_LOKI_PASSWORD is no longer inert under logging off — it lands in .env.
	os.Unsetenv("PSTACK_LOKI_PASSWORD")
	for _, c := range cells() {
		t.Run(c.name()+" files", func(t *testing.T) {
			// negative control: change `name: pstack-control` handling, any marker block, or the .env line order — the compare fails.
			dir := t.TempDir()
			var out bytes.Buffer
			if err := initctl.Init(c.options(dir, false, initShim(), &out)); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"docker-compose.yml", ".env", "dns.env"} {
				got := mask(read(t, filepath.Join(dir, "control", f)), dir)
				want := read(t, filepath.Join(testfacts.Golden(t), "render", "control", c.name(), f))
				if got != want {
					t.Errorf("%s differs from the golden\n--- got\n%s\n--- want\n%s", f, got, want)
				}
			}
			// The modes init promises: .env and dns.env 0600, the compose file 0644.
			for f, mode := range map[string]os.FileMode{".env": 0o600, "dns.env": 0o600, "docker-compose.yml": 0o644} {
				st, err := os.Stat(filepath.Join(dir, "control", f))
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode().Perm() != mode {
					t.Errorf("%s mode %o, want %o", f, st.Mode().Perm(), mode)
				}
			}
			// And the transcript (the CLI's stdout is init's Out).
			want, _ := goldenCLI(t, "init-"+c.name())
			if got := mask(out.String(), dir); got != want {
				t.Errorf("transcript differs\n--- got\n%s\n--- want\n%s", got, want)
			}
		})

		t.Run(c.name()+" dry-run transcript", func(t *testing.T) {
			// negative control: count bytes with len() instead of js.Len in write()'s dry-run line — the sizes differ.
			// The FIXED golden data dir: `init --dry-run` prints the byte size of the .env it would
			// write, and that file holds the path. A dry run creates nothing, so the path need not exist.
			var out bytes.Buffer
			runner := exec.New(exec.Options{DryRun: true, Out: &out})
			if err := initctl.Init(c.options(goldenDataDir, true, runner, &out)); err != nil {
				t.Fatal(err)
			}
			want, _ := goldenCLI(t, "init-dry-"+c.name())
			if got := mask(out.String(), goldenDataDir); got != want {
				t.Errorf("dry-run transcript differs\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}

	t.Run("a generated token is printed exactly once, 48 hex", func(t *testing.T) {
		// negative control: print the `taken from the environment` line when generated — fails.
		os.Unsetenv("PSTACK_TOKEN")
		dir := t.TempDir()
		var out bytes.Buffer
		// The golden ran with the CLI's defaults: orchestrator swarm.
		if err := initctl.Init(cells()[1].options(dir, false, initShim(), &out)); err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`PSTACK_TOKEN=([0-9a-f]{48})\n  \^ generated`).FindStringSubmatch(out.String())
		if m == nil {
			t.Fatalf("no generated token line:\n%s", out.String())
		}
		if !strings.Contains(read(t, filepath.Join(dir, "control", ".env")), "PSTACK_TOKEN="+m[1]+"\n") {
			t.Error(".env holds a different token")
		}
		want, _ := goldenCLI(t, "init-generated-token")
		got := regexp.MustCompile(`PSTACK_TOKEN=[0-9a-f]{48}`).ReplaceAllString(mask(out.String(), dir), "PSTACK_TOKEN=<GENERATED_TOKEN>")
		if got != want {
			t.Errorf("transcript differs\n--- got\n%s\n--- want\n%s", got, want)
		}
	})
}

// substituted is the control template after Init's six marker substitutions (init.go, "── 3.
// Configuration"), with `loki` appended after the advanced UI where Init appends LokiService. It is
// LokiWiring's input; its first subtest proves it still matches the file Init writes.
func substituted(challenge initctl.Challenge, ui initctl.UI, loki string) string {
	s := pstack.ControlTemplate
	s = strings.Replace(s, "      #__ACME_CHALLENGE__", initctl.AcmeChallengeArgs(challenge, "cloudflare"), 1)
	s = strings.Replace(s, "      #__ACME_ROUTER_TLS__", initctl.AcmeRouterLabels(challenge), 1)
	s = strings.Replace(s, "      #__CONTROL_UI_SERVICE__", initctl.ControlUIService(ui), 1)
	s = strings.Replace(s, "      #__SWARM_PROVIDER__", initctl.SwarmProviderArgs(spec.Compose), 1)
	s = strings.Replace(s, "      #__WAKE_ROUTER__", initctl.WakeRouterLabels("preview.example.com"), 1)
	return strings.Replace(s, "#__ADVANCED_UI_SERVICE__", initctl.AdvancedUIService(ui)+loki, 1)
}

// The loki service. Its TLS labels are the part that varies by host, and getting them backwards is
// silent until the first push: DNS-01 would order a second certificate, HTTP-01 would serve none.
func TestLokiService(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("http01 orders loki its own certificate, dns01 inherits the wildcard", func(t *testing.T) {
		// negative control: append the certresolver label for every challenge — the dns01 check fails.
		http := initctl.LokiService(initctl.Loki, initctl.HTTP01, pw)
		dns := initctl.LokiService(initctl.Loki, initctl.DNS01, pw)
		for _, block := range []string{http, dns} {
			if !strings.Contains(block, "      - traefik.http.routers.pstack-loki.tls=true\n") {
				t.Errorf("no tls=true:\n%s", block)
			}
		}
		if !strings.Contains(http, "      - traefik.http.routers.pstack-loki.tls.certresolver=le\n") {
			t.Error("http01 has no certresolver")
		}
		if strings.Contains(dns, "- traefik.http.routers.pstack-loki.tls.certresolver") {
			t.Error("dns01 orders its own certificate")
		}
	})

	t.Run("basic auth is pstack:{SHA} of the password; deploys find the push URL on a label", func(t *testing.T) {
		// negative control: hash with sha256.Sum256 in LokiService — the users label differs.
		sum := sha1.Sum([]byte(pw))
		block := initctl.LokiService(initctl.Loki, initctl.HTTP01, pw)
		for _, want := range []string{
			"      - traefik.http.middlewares.pstack-loki-auth.basicauth.users=pstack:{SHA}" + base64.StdEncoding.EncodeToString(sum[:]) + "\n",
			// ${…} stays for compose to fill from .env.
			"      - pstack.logging.push-url=https://pstack:${LOKI_PUSH_PASSWORD}@loki.${DOMAIN}/loki/api/v1/push\n",
		} {
			if !strings.Contains(block, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(block, pw) {
			t.Error("the password itself is in the compose file")
		}
	})

	t.Run("loki is on the logs network only", func(t *testing.T) {
		// Every preview container sits on preview-ingress, and Loki has no auth of its own.
		// negative control: render `networks: [preview-ingress, logs]` in LokiService — the list has two entries.
		v, err := yamlx.ParseString("services:\n" + initctl.LokiService(initctl.Loki, initctl.DNS01, pw))
		if err != nil {
			t.Fatal(err)
		}
		if n := fmt.Sprint(v.(*omap.Map).GetMap("services").GetMap("loki").GetSlice("networks")); n != "[logs]" {
			t.Errorf("networks %s", n)
		}
	})

	t.Run("Loki reads S3 credentials from its mount, the line after GOMEMLIMIT", func(t *testing.T) {
		// negative control: delete the AWS_SHARED_CREDENTIALS_FILE line from LokiService — the contiguous substring is missing.
		// A container's env is fixed at creation and `docker restart` keeps it, so init sets this, not the API.
		const want = "      GOMEMLIMIT: 1600MiB\n      AWS_SHARED_CREDENTIALS_FILE: /etc/loki/s3-credentials\n"
		for _, c := range []initctl.Challenge{initctl.HTTP01, initctl.DNS01} {
			if block := initctl.LokiService(initctl.Loki, c, pw); strings.Count(block, want) != 1 {
				t.Errorf("%s: no %q:\n%s", c, want, block)
			}
		}
	})

	t.Run("logging off renders nothing", func(t *testing.T) {
		// negative control: drop the `logging != Loki` early return — both cases render the service.
		for _, l := range []initctl.Logging{initctl.LoggingNone, ""} {
			if got := initctl.LokiService(l, initctl.HTTP01, pw); got != "" {
				t.Errorf("%q rendered:\n%s", l, got)
			}
		}
	})
}

// Loki's plumbing is literal edits of lines the template already has. A template change that moves
// one must fail by name, never render a Loki that Traefik cannot reach.
func TestLokiWiring(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("substituted is the file Init writes", func(t *testing.T) {
		// negative control: drop the WAKE_ROUTER replacement from substituted — it differs from Init's file.
		if _, yaml := render(t, nil); substituted(initctl.HTTP01, initctl.Basic, "") != yaml {
			t.Error("substituted no longer mirrors Init's marker substitutions")
		}
	})

	t.Run("each edit lands once: Traefik's networks, the top-level volumes and networks", func(t *testing.T) {
		// negative control: strings.ReplaceAll for the networks anchor in LokiWiring — advanced-ui joins logs too.
		for _, c := range []initctl.Challenge{initctl.HTTP01, initctl.DNS01} {
			got, err := initctl.LokiWiring(substituted(c, initctl.Advanced, initctl.LokiService(initctl.Loki, c, pw)), initctl.Loki)
			if err != nil {
				t.Fatal(err)
			}
			for _, edit := range []string{
				"    networks: [preview-ingress, logs]\n",
				"volumes:\n  letsencrypt:\n  loki:\n",
				"  preview-shared:\n    external: true\n  logs: {}\n",
			} {
				if n := strings.Count(got, edit); n != 1 {
					t.Errorf("%s: %q appears %d times", c, edit, n)
				}
			}
			v, err := yamlx.ParseString(got)
			if err != nil {
				t.Fatalf("%s: %v", c, err)
			}
			d := v.(*omap.Map)
			svcs := d.GetMap("services")
			if n := fmt.Sprint(svcs.GetMap("traefik").GetSlice("networks")); n != "[preview-ingress logs]" {
				t.Errorf("%s: traefik networks %s", c, n)
			}
			// The advanced UI's identical line comes after Traefik's and must stay off `logs`.
			if n := fmt.Sprint(svcs.GetMap("advanced-ui").GetSlice("networks")); n != "[preview-ingress]" {
				t.Errorf("%s: advanced-ui networks %s", c, n)
			}
			if svcs.GetMap("loki") == nil || !d.GetMap("volumes").Has("loki") || !d.GetMap("networks").Has("logs") {
				t.Errorf("%s: the loki service, volume or network is missing", c)
			}
		}
	})

	t.Run("only Traefik's own copy of the networks line counts, not the advanced UI's identical one", func(t *testing.T) {
		// negative control: change the Traefik entry's haystack from traefikBlock to identity — the
		// advanced UI's identical line stands in for Traefik's removed one, and no error is returned.
		const anchor = "    networks: [preview-ingress]\n"
		// n=1 removes the FIRST copy only: Traefik's own line, which sits earlier in the file than the
		// advanced UI's (inserted later, at the #__ADVANCED_UI_SERVICE__ marker near the volumes block).
		in := strings.Replace(substituted(initctl.HTTP01, initctl.Advanced, ""), anchor, "", 1)
		if _, err := initctl.LokiWiring(in, initctl.Loki); err == nil || !strings.Contains(err.Error(), "Traefik networks line") {
			t.Fatalf("got %v, want an error naming the Traefik networks line", err)
		}
	})

	t.Run("logging off returns the template byte-identical", func(t *testing.T) {
		// negative control: drop the `logging != Loki` early return — the file gains the logs network.
		in := substituted(initctl.HTTP01, initctl.Basic, "")
		for _, l := range []initctl.Logging{initctl.LoggingNone, ""} {
			if got, err := initctl.LokiWiring(in, l); err != nil || got != in {
				t.Errorf("%q changed the template (err %v)", l, err)
			}
		}
	})

	for _, c := range []struct{ what, anchor string }{
		{"Traefik networks line", "    networks: [preview-ingress]\n"},
		{"letsencrypt volume", "volumes:\n  letsencrypt:\n"},
		{"preview-shared network", "  preview-shared:\n    external: true\n"},
	} {
		t.Run("a template without the "+c.what+" fails by name", func(t *testing.T) {
			// negative control: skip the strings.Contains check in LokiWiring — no error.
			// ReplaceAll: the advanced UI carries a second copy of the networks line.
			in := strings.ReplaceAll(substituted(initctl.HTTP01, initctl.Advanced, ""), c.anchor, "")
			if _, err := initctl.LokiWiring(in, initctl.Loki); err == nil || !strings.Contains(err.Error(), c.what) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// lokiConfigSpecLines is docs/loki-logging-design.md's Loki config block (the ```yaml fence after
// "### Loki config"), copied here — not extracted at test time — so a Go test fails on day-one drift
// between the design doc and the shipped config.yaml. The render golden is generated FROM the binary,
// so it cannot catch a wrong first generation (spec 308: "Loki config: byte-exact").
var lokiConfigSpecLines = []string{
	"auth_enabled: false",
	"",
	"server:",
	"  http_listen_port: 3100            # `loki -health` probes localhost:3100/ready",
	"",
	"common:",
	"  instance_addr: 127.0.0.1",
	"  path_prefix: /loki                # WAL, compactor, tsdb-shipper dirs — on the volume",
	"  replication_factor: 1",
	"  ring:",
	"    kvstore:",
	"      store: inmemory",
	"",
	"# storage_config, never common.storage: common.storage takes one backend, and slice 2's",
	"# filesystem→S3 switch needs both configured at once.",
	"storage_config:",
	"  filesystem:",
	"    directory: /loki/chunks",
	"",
	"schema_config:",
	"  configs:",
	"    - from: \"2024-04-01\"            # a fixed past date, never \"today\": a future date means no active",
	"      store: tsdb                   # schema (Loki stores nothing); a changed date makes data unreadable",
	"      object_store: filesystem",
	"      schema: v13",
	"      index:",
	"        prefix: index_",
	"        period: 24h                 # tsdb requires 24h",
	"",
	"ingester:",
	"  chunk_idle_period: 30m",
	"  max_chunk_age: 2h                 # must stay <= querier.query_ingesters_within",
	"  chunk_target_size: 1572864",
	"  chunk_encoding: snappy",
	"  wal:",
	"    replay_memory_ceiling: 1GB      # default 4GB exceeds the 2g container limit",
	"",
	"querier:",
	"  query_ingesters_within: 3h",
	"",
	"limits_config:",
	"  retention_period: 168h            # 0s would keep forever",
	"  max_query_lookback: 168h",
	"  max_global_streams_per_user: 1000",
	"",
	"compactor:",
	"  retention_enabled: true",
	"  delete_request_store: filesystem  # required once retention is on",
	"",
	"analytics:",
	"  reporting_enabled: false",
	"",
}

// Loki's config is fixed, and some of its values fail silently when changed: a moved schema `from`
// makes stored logs unreadable, a retention of 0s keeps everything forever. The second subtest pins
// the whole file byte-for-byte against the design doc (R8): the render golden is generated FROM the
// binary, so it cannot catch a wrong first generation — only a Go test with an independent copy can.
func TestLokiConfig(t *testing.T) {
	t.Run("the load-bearing values", func(t *testing.T) {
		// negative control: change the schema `from` in templates/control/loki/config.yaml to "2026-09-15" — fails.
		v, err := yamlx.ParseString(pstack.LokiConfig)
		if err != nil {
			t.Fatal(err)
		}
		cfg := v.(*omap.Map)
		configs := cfg.GetMap("schema_config").GetSlice("configs")
		if len(configs) != 1 {
			t.Fatalf("%d schema configs, want 1", len(configs))
		}
		schema, _ := configs[0].(*omap.Map)
		port, _ := cfg.GetMap("server").Get("http_listen_port")
		for _, c := range []struct {
			what      string
			got, want any
		}{
			{"schema from", schema.GetString("from"), "2024-04-01"},
			{"schema store", schema.GetString("store"), "tsdb"},
			{"index period", schema.GetMap("index").GetString("period"), "24h"},
			{"retention", cfg.GetMap("limits_config").GetString("retention_period"), "168h"},
			{"WAL replay ceiling", cfg.GetMap("ingester").GetMap("wal").GetString("replay_memory_ceiling"), "1GB"},
			{"listen port", port, int64(3100)},
			{"delete request store", cfg.GetMap("compactor").GetString("delete_request_store"), "filesystem"},
		} {
			if c.got != c.want {
				t.Errorf("%s = %#v, want %#v", c.what, c.got, c.want)
			}
		}
	})

	t.Run("byte-exact against the design doc's Loki config block", func(t *testing.T) {
		// negative control: change "tsdb requires 24h" to "tsdb requires 24hrs" in config.yaml — a
		// comment-only edit the load-bearing-values subtest above cannot see, but this one catches.
		if want := strings.Join(lokiConfigSpecLines, "\n"); pstack.LokiConfig != want {
			t.Error("templates/control/loki/config.yaml no longer matches docs/loki-logging-design.md's Loki config block byte-for-byte")
		}
	})
}

// init --logging loki: the push password, its .env line, Loki's config and the plugin step. Logging off
// renders no Loki beyond pstack's ./loki mount — TestInitGoldens pins all eight cells' bytes.
func TestInitLoki(t *testing.T) {
	loki := func(r *exec.Fake, out *bytes.Buffer) func(*initctl.Options) {
		return func(o *initctl.Options) { o.Logging, o.Runner, o.Out = initctl.Loki, r, out }
	}
	const pw = "0123456789abcdef0123456789abcdef"

	t.Run("a generated push password is 32 hex in .env and its {SHA} hash is the basicauth label", func(t *testing.T) {
		// negative control: pass "" instead of lokiPassword to LokiService in Init — the label hash no longer matches.
		t.Setenv("PSTACK_LOKI_PASSWORD", "") // empty is unset (`||`), and it keeps the operator's shell out of the test
		var out bytes.Buffer
		dir, yaml := render(t, loki(okRunner("inactive", ""), &out))
		env := read(t, filepath.Join(dir, "control", ".env"))
		m := regexp.MustCompile(`(?m)^LOKI_PUSH_PASSWORD=([0-9a-f]{32})$`).FindStringSubmatch(env)
		if m == nil {
			t.Fatalf(".env has no 32-hex LOKI_PUSH_PASSWORD line:\n%s", env)
		}
		sum := sha1.Sum([]byte(m[1]))
		if want := "basicauth.users=pstack:{SHA}" + base64.StdEncoding.EncodeToString(sum[:]) + "\n"; !strings.Contains(yaml, want) {
			t.Errorf("compose lacks %q", want)
		}
		if !strings.Contains(out.String(), "  logging   loki at https://loki.preview.example.com (push only)") {
			t.Errorf("no logging summary line:\n%s", out.String())
		}
		// Nobody needs it printed: upgrade reads it back from .env, deploys from the loki container's label.
		if strings.Contains(out.String(), m[1]) {
			t.Error("init printed the push password")
		}
	})

	t.Run("PSTACK_LOKI_PASSWORD is used verbatim", func(t *testing.T) {
		// negative control: replace `os.Getenv("PSTACK_LOKI_PASSWORD")` with "" in Init — a generated password is written instead.
		t.Setenv("PSTACK_LOKI_PASSWORD", pw)
		dir, _ := render(t, loki(okRunner("inactive", ""), &bytes.Buffer{}))
		if env := read(t, filepath.Join(dir, "control", ".env")); !strings.Contains(env, "\nLOKI_PUSH_PASSWORD="+pw+"\n") {
			t.Errorf(".env does not carry the supplied password:\n%s", env)
		}
	})

	t.Run("a malformed PSTACK_LOKI_PASSWORD fails before anything runs or is written", func(t *testing.T) {
		// negative control: move the push-password block below `── 0. Preconditions` — the precondition commands run first.
		bad := "0123456789abcdef0123456789abcde$" // 32 characters; `$` is what Compose would expand in .env
		t.Setenv("PSTACK_LOKI_PASSWORD", bad)
		r := okRunner("inactive", "")
		dir := t.TempDir()
		err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, Runner: r, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.HasPrefix(err.Error(), "PSTACK_LOKI_PASSWORD must be 32 lowercase hex characters") {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), bad) {
			t.Error("the error echoes the secret")
		}
		if n := len(r.Commands()); n != 0 {
			t.Errorf("%d commands ran before the refusal: %v", n, r.Commands())
		}
		if _, err := os.Stat(filepath.Join(dir, "control", ".env")); !os.IsNotExist(err) {
			t.Errorf("control/.env was written: %v", err)
		}
	})

	t.Run("the plugin installs before the control stack comes up, and a failed install fails init", func(t *testing.T) {
		// negative control: move the `── 1c.` block below `── 4. Bring it up` — plugin runs after up, and up runs before the error.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		r := okRunner("inactive", "")
		render(t, loki(r, &bytes.Buffer{}))
		plugin, up := -1, -1
		for i, c := range r.Commands() {
			if c == swarm.LokiPluginInstall {
				plugin = i
			}
			if strings.Contains(c, "-p pstack-control") && strings.HasSuffix(c, " up -d --remove-orphans") {
				up = i
			}
		}
		if plugin < 0 || up < 0 || plugin > up {
			t.Errorf("plugin at %d, up at %d:\n%s", plugin, up, strings.Join(r.Commands(), "\n"))
		}

		failing := okRunner("inactive", "")
		ok := failing.Answer
		// Answer, not Fail: a Fake consults Fail only when Answer declines, and okRunner answers everything.
		failing.Answer = func(cmd string) (exec.Result, bool) {
			if cmd == swarm.LokiPluginInstall {
				return exec.Result{OK: false, Code: 1, Stderr: "Error response from daemon: pull access denied\n"}, true
			}
			return ok(cmd)
		}
		err := initctl.Init(initctl.Options{
			DataDir: t.TempDir(), Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, Runner: failing, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.Contains(err.Error(), "loki log plugin") || !strings.Contains(err.Error(), "pull access denied") ||
			!strings.Contains(err.Error(), swarm.LokiPluginInstall) {
			t.Fatalf("got %v", err)
		}
		for _, c := range failing.Commands() {
			if strings.Contains(c, " up -d") {
				t.Errorf("the control stack came up after the plugin failed: %s", c)
			}
		}
	})

	t.Run("control/loki/config.yaml is the embedded config at 0644", func(t *testing.T) {
		// negative control: write config.yaml at 0o600 — the mode check fails (Loki runs as uid 10001 and could not read it).
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir, _ := render(t, loki(okRunner("inactive", ""), &bytes.Buffer{}))
		p := filepath.Join(dir, "control", "loki", "config.yaml")
		if got := read(t, p); got != pstack.LokiConfig {
			t.Errorf("config.yaml differs from pstack.LokiConfig:\n%s", got)
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("mode %o, want 644", st.Mode().Perm())
		}
	})

	// After a slice-2 save the file is the API's: an S3 schema period lives only there. init re-runs on
	// every upgrade and `pstack logging loki`, and a rewrite would drop that period.
	t.Run("an existing loki config.yaml is kept, and a dry run says keep", func(t *testing.T) {
		// negative control: drop the os.Stat guard in Init so write always runs — the file is overwritten and the dry run prints `write`.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir := t.TempDir()
		p := filepath.Join(dir, "control", "loki", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		saved := strings.Replace(pstack.LokiConfig, "  retention_period: 168h", "  retention_period: 72h", 1)
		if saved == pstack.LokiConfig {
			t.Fatal("templates/control/loki/config.yaml has no `  retention_period: 168h` line")
		}
		if err := os.WriteFile(p, []byte(saved), 0o644); err != nil {
			t.Fatal(err)
		}
		opts := func(dryRun bool, r exec.Runner, out *bytes.Buffer) initctl.Options {
			return initctl.Options{
				DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
				Orchestrator: spec.Compose, Logging: initctl.Loki, DryRun: dryRun, Runner: r, Out: out,
			}
		}

		var out bytes.Buffer
		if err := initctl.Init(opts(false, okRunner("inactive", ""), &out)); err != nil {
			t.Fatal(err)
		}
		if got := read(t, p); got != saved {
			t.Errorf("init rewrote config.yaml:\n%s", got)
		}
		// "keep "+p, not a bare "keep": t.TempDir() folds this subtest's name into every printed path.
		if strings.Contains(out.String(), "keep "+p) {
			t.Errorf("a real run printed a keep line:\n%s", out.String())
		}

		// The real runner, not a Fake: only it prints `[dry-run]` lines.
		var dry bytes.Buffer
		if err := initctl.Init(opts(true, exec.New(exec.Options{DryRun: true, Out: &dry}), &dry)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dry.String(), "  [dry-run] keep "+p+"\n") {
			t.Errorf("no keep line:\n%s", dry.String())
		}
		if strings.Contains(dry.String(), "  [dry-run] write "+p+" (") {
			t.Errorf("the dry run would write config.yaml:\n%s", dry.String())
		}
	})

	// The mount is in the template, not added by LokiWiring: `pstack logging loki|off` then leaves pstack's
	// service byte-identical, so the switch never recreates pstack (and never kills a job in flight).
	t.Run("pstack mounts ./loki read-write at /etc/loki with logging off and on", func(t *testing.T) {
		// negative control: delete `      - ./loki:/etc/loki` from templates/control/docker-compose.yml — pstack's loki mounts read [] in both modes.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		for _, l := range []initctl.Logging{initctl.LoggingNone, initctl.Loki} {
			_, yaml := render(t, func(o *initctl.Options) { o.Logging = l })
			v, err := yamlx.ParseString(yaml)
			if err != nil {
				t.Fatal(err)
			}
			svcs := v.(*omap.Map).GetMap("services")
			lokiMounts := func(svc string) (out []string) {
				for _, m := range svcs.GetMap(svc).GetSlice("volumes") {
					if s, _ := m.(string); strings.HasPrefix(s, "./loki:") {
						out = append(out, s)
					}
				}
				return out
			}
			// Same container path as Loki's own: a filename means the same file to both.
			if m := fmt.Sprint(lokiMounts("pstack")); m != "[./loki:/etc/loki]" {
				t.Errorf("%q: pstack's loki mounts %s", l, m)
			}
			if l == initctl.Loki {
				if m := fmt.Sprint(lokiMounts("loki")); m != "[./loki:/etc/loki:ro]" {
					t.Errorf("loki's own mounts %s", m)
				}
			}
		}
	})

	t.Run("control/loki is 0755 regardless of umask", func(t *testing.T) {
		// negative control: pass noMode instead of 0o755 to ensureDir for control/loki — under a
		// restrictive umask the directory renders 0700 and uid 10001 (Loki) cannot traverse it.
		old := syscall.Umask(0o077)
		defer syscall.Umask(old)
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir, _ := render(t, loki(okRunner("inactive", ""), &bytes.Buffer{}))
		st, err := os.Stat(filepath.Join(dir, "control", "loki"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Errorf("control/loki mode %o, want 0755", st.Mode().Perm())
		}
	})

	// R5 amends the plan: LOKI_PUSH_PASSWORD survives `pstack logging off` (spec 291 — `pstack upgrade`
	// and `pstack logging` reuse the stored password), so a valid env password is kept even with logging
	// none. "Off" has no plugin, no loki service and no config.yaml; control/loki itself exists in every
	// mode, because pstack mounts it in every mode.
	t.Run("logging off: control/loki at 0755 but no config.yaml, no plugin, no loki service — PSTACK_LOKI_PASSWORD is kept in .env", func(t *testing.T) {
		// negative control: re-gate `os.Getenv("PSTACK_LOKI_PASSWORD")` under `if logging == Loki` (pre-R5) — the .env line disappears.
		// negative control: re-wrap control/loki's ensureDir in `if logging == Loki` — control/loki does not exist.
		t.Setenv("PSTACK_LOKI_PASSWORD", pw)
		r := okRunner("inactive", "")
		var out bytes.Buffer
		dir, yaml := render(t, func(o *initctl.Options) { o.Runner, o.Out = r, &out })
		if st, err := os.Stat(filepath.Join(dir, "control", "loki")); err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("control/loki: %v, want a 0755 directory", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "control", "loki", "config.yaml")); !os.IsNotExist(err) {
			t.Errorf("control/loki/config.yaml exists: %v", err)
		}
		if env := read(t, filepath.Join(dir, "control", ".env")); !strings.Contains(env, "\nLOKI_PUSH_PASSWORD="+pw+"\n") {
			t.Errorf(".env dropped the password it was handed (spec 291: it must survive `logging off`):\n%s", env)
		}
		// Not a bare "loki": pstack's `./loki:/etc/loki` mount is there in every mode.
		if strings.Contains(yaml, "\n  loki:\n") || strings.Contains(yaml, "\n  logs: {}\n") {
			t.Error("compose has the loki service or the logs network")
		}
		for _, c := range r.Commands() {
			if strings.Contains(c, "docker plugin") {
				t.Errorf("plugin command with logging off: %s", c)
			}
		}
		// Not a bare "logging" check: t.TempDir() folds this subtest's own name into the printed
		// config/registry paths above, and that name starts with "logging off" too.
		if strings.Contains(out.String(), "  logging   loki at ") {
			t.Errorf("summary mentions the loki logging line:\n%s", out.String())
		}
	})

	t.Run("a malformed PSTACK_LOKI_PASSWORD fails logging off too, before anything runs or is written", func(t *testing.T) {
		// negative control: re-gate the validation under `if logging == Loki` — a bad env password is accepted and written.
		bad := "0123456789abcdef0123456789abcde$"
		t.Setenv("PSTACK_LOKI_PASSWORD", bad)
		r := okRunner("inactive", "")
		dir := t.TempDir()
		err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Runner: r, Out: &bytes.Buffer{},
		})
		if err == nil || !strings.HasPrefix(err.Error(), "PSTACK_LOKI_PASSWORD must be 32 lowercase hex characters") {
			t.Fatalf("got %v", err)
		}
		if n := len(r.Commands()); n != 0 {
			t.Errorf("%d commands ran before the refusal: %v", n, r.Commands())
		}
		if _, err := os.Stat(filepath.Join(dir, "control", ".env")); !os.IsNotExist(err) {
			t.Errorf("control/.env was written: %v", err)
		}
	})

	t.Run("dry-run names the plugin step and the config file, and writes nothing", func(t *testing.T) {
		// negative control: wrap the `── 1c.` block in `if !dryRun` like the health wait — `[dry-run] loki log plugin` is missing.
		t.Setenv("PSTACK_LOKI_PASSWORD", "")
		dir := t.TempDir()
		var out bytes.Buffer
		// The real runner, not a Fake: only it prints the `[dry-run] <label>` lines.
		if err := initctl.Init(initctl.Options{
			DataDir: dir, Domain: "preview.example.com", AcmeEmail: "o@e.com", Challenge: initctl.HTTP01,
			Orchestrator: spec.Compose, Logging: initctl.Loki, DryRun: true,
			Runner: exec.New(exec.Options{DryRun: true, Out: &out}), Out: &out,
		}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"  [dry-run] loki log plugin\n",
			"  [dry-run] mkdir -p " + filepath.Join(dir, "control", "loki") + "\n",
			"  [dry-run] write " + filepath.Join(dir, "control", "loki", "config.yaml") + " (",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q:\n%s", want, out.String())
			}
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("dry-run wrote %v (%v)", entries, err)
		}
	})
}
