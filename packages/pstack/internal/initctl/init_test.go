package initctl_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
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
