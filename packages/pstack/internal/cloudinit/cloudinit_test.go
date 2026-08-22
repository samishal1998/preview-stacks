package cloudinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

var base = Answers{
	Domain:            "preview.example.com",
	AcmeEmail:         "ops@example.com",
	SSHKey:            "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest key@host",
	DashboardPassword: "deadbeef1234",
	Challenge:         "http01",
	UI:                "basic",
}

func render(t *testing.T, a Answers) string {
	t.Helper()
	out, err := RenderCloudInit(a)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// yamlOK is the TS's `ruby -ryaml` check: the artifact parses as a YAML document.
func yamlOK(t *testing.T, text string) bool {
	t.Helper()
	_, err := yamlx.ParseString(text)
	return err == nil
}

// runcmd is the `pstack init` invocation, taken from the PARSED runcmd list.
//
// Parsed, not regex-scraped: the template's prose explains `--challenge dns01` as the wildcard
// upgrade path and mentions `pstack init` several times, so a text match finds documentation
// and reports on the wrong thing entirely. Comments do not survive a YAML parse, which is
// exactly the property wanted here.
func runcmd(t *testing.T, yaml string) []string {
	t.Helper()
	doc, err := yamlx.ParseString(yaml)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range doc.(*omap.Map).GetSlice("runcmd") {
		s, _ := c.(string)
		out = append(out, s)
	}
	return out
}

func initCall(t *testing.T, yaml string) string {
	for _, c := range runcmd(t, yaml) {
		if strings.Contains(c, "pstack init") {
			return c
		}
	}
	return ""
}

func some(list []string, pred func(string) bool) bool {
	for _, s := range list {
		if pred(s) {
			return true
		}
	}
	return false
}

func indexOf(list []string, pred func(string) bool) int {
	for i, s := range list {
		if pred(s) {
			return i
		}
	}
	return -1
}

func TestCloudInitGeneration(t *testing.T) {
	t.Run("renders valid cloud-config with no placeholders left", func(t *testing.T) {
		// negative control: drop the BUN_VERSION entry from `values` — the leftover check throws.
		out := render(t, base)
		if !strings.HasPrefix(out, "#cloud-config") {
			t.Error("not a cloud-config")
		}
		// Either of these makes cloud-init discard the whole file silently.
		if strings.Contains(out, "\t") {
			t.Error("tab in output")
		}
		if !yamlOK(t, out) {
			t.Error("not valid YAML")
		}
		if leftoverRe.MatchString(out) {
			t.Error("placeholders left")
		}
	})

	t.Run("Docker's own Go templates survive rendering", func(t *testing.T) {
		// `{{.State.Health.Status}}` shares the delimiters. It is left alone because placeholder names
		// are SHOUT_CASE with no dots — if that ever changes, this breaks first.
		// negative control: substitute `{{.State.Health.Status}}` with "" in RenderCloudInit — fails.
		if !strings.Contains(render(t, base), "{{.State.Health.Status}}") {
			t.Error("Go template eaten")
		}
	})

	t.Run("writes no fallback router file — init renders the catch-all since 0.26.0", func(t *testing.T) {
		// The cloud-config used to drop `fallback.yml` into Traefik's watched directory. That router is
		// now rendered by `init` on every host (it is what wakes a sleeping preview), so a second copy
		// here would be two priority-1 routers for the same hostnames.
		// negative control: always append `--orchestrator swarm` to initFlags — the no-flag check fails.
		yaml := render(t, base)
		if regexp.MustCompile(`(?m)^write_files:`).MatchString(yaml) || strings.Contains(yaml, "fallback.yml") {
			t.Error("fallback router file present")
		}
		// The flag reaches the init line only when asked for; the CLI passes its default explicitly.
		if regexp.MustCompile(`(?m)^\s+--orchestrator`).MatchString(yaml) {
			t.Error("--orchestrator without asking")
		}
		sw := base
		sw.Orchestrator = "swarm"
		if !regexp.MustCompile(`(?m)^\s+--orchestrator swarm$`).MatchString(render(t, sw)) {
			t.Error("--orchestrator swarm missing")
		}
	})

	t.Run("dns01 adds the challenge flags to the init call; http01 adds none", func(t *testing.T) {
		// negative control: drop the dns01 branch of initFlags — the dns assertions fail.
		a := base
		a.Challenge, a.DNSProvider = "dns01", "hetzner"
		dns := initCall(t, render(t, a))
		if !strings.Contains(dns, "--challenge dns01") || !strings.Contains(dns, "--dns-provider hetzner") {
			t.Errorf("dns01 init call: %q", dns)
		}
		if strings.Contains(initCall(t, render(t, base)), "--challenge") {
			t.Error("http01 carries --challenge")
		}
	})

	t.Run("advanced UI adds its install and image build", func(t *testing.T) {
		// negative control: render UI_IMAGE_STEP as "" for advanced — the build-image --ui check fails.
		a := base
		a.UI = "advanced"
		adv := render(t, a)
		if !strings.Contains(initCall(t, adv), "--ui advanced") {
			t.Error("--ui advanced missing")
		}
		has := func(c string) bool { return strings.Contains(c, "build-image --ui") }
		if !some(runcmd(t, adv), has) {
			t.Error("no build-image --ui")
		}
		if some(runcmd(t, render(t, base)), has) {
			t.Error("basic builds the UI image")
		}
	})

	t.Run("no config repo drops the clone line rather than emitting an empty one", func(t *testing.T) {
		// `git clone  /opt/preview/config` would fail and abort the rest of cloud-init.
		// negative control: skip the splice when ConfigRepo is "" — the regexp matches.
		out := render(t, base)
		if regexp.MustCompile(`git clone --depth 1\s+/opt`).MatchString(out) {
			t.Error("empty clone line emitted")
		}
		if !yamlOK(t, out) {
			t.Error("not valid YAML")
		}
	})

	t.Run("rejects inputs that would produce a broken or locked-out host", func(t *testing.T) {
		// negative control: drop the single-quote check in validate — the last case passes.
		expectErr := func(a Answers, re string) {
			t.Helper()
			_, err := RenderCloudInit(a)
			if err == nil || !regexp.MustCompile(re).MatchString(err.Error()) {
				t.Errorf("want error /%s/, got %v", re, err)
			}
			if _, ok := err.(*Error); !ok {
				t.Errorf("not a *cloudinit.Error: %T", err)
			}
		}
		a := base
		a.Domain = "notadomain"
		expectErr(a, "hostname")
		a = base
		a.AcmeEmail = "nope"
		expectErr(a, "address")
		// Optional — but a MALFORMED one is still refused, because it produces a booted host nobody
		// can log into, the one failure here with no cheap recovery.
		a = base
		a.SSHKey = "my-key"
		expectErr(a, "authorized_keys")
		a = base
		a.Challenge = "dns01"
		expectErr(a, "provider")
		// Interpolated into a single-quoted shell command.
		a = base
		a.DashboardPassword = "a'b"
		expectErr(a, "single quote")
	})

	t.Run("omitting the ssh key omits the whole key list, not an empty one", func(t *testing.T) {
		// `ssh_authorized_keys:` with nothing under it parses fine and yields a user with no way in
		// and no error — worse than not writing the block at all. Providers inject their own key.
		// negative control: emit `    ssh_authorized_keys:\n` for an empty key — the check fails.
		a := base
		a.SSHKey = ""
		out := render(t, a)
		if strings.Contains(out, "ssh_authorized_keys") {
			t.Error("empty key list emitted")
		}
		if !yamlOK(t, out) {
			t.Error("not valid YAML")
		}
	})

	t.Run("/opt/preview is created before it is chowned", func(t *testing.T) {
		// It failed on a real boot: with no config repo there was no clone, so the directory the chown
		// targeted never existed and the step errored.
		// negative control: splice one extra line above the clone (the mkdir) — mk becomes -1.
		cmds := runcmd(t, render(t, base))
		mk := indexOf(cmds, func(c string) bool { return strings.Contains(c, "mkdir -p /opt/preview") })
		ch := indexOf(cmds, func(c string) bool { return strings.Contains(c, "chown -R preview:preview /opt/preview") })
		if mk < 0 || mk >= ch {
			t.Errorf("mkdir at %d, chown at %d", mk, ch)
		}
	})

	t.Run("generated passwords are shell-safe", func(t *testing.T) {
		// Hex on purpose: a `$` would be expanded by the shell that hashes it.
		// negative control: base64-encode instead of hex — fails.
		for i := 0; i < 20; i++ {
			if p := RandomPassword(); !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(p) || len(p) != 24 {
				t.Errorf("password %q", p)
			}
		}
	})
}

func TestCloudInitPinnedVersionsAndDistros(t *testing.T) {
	pinned := Answers{
		Domain:            "preview.example.com",
		AcmeEmail:         "ops@example.com",
		DashboardPassword: "pw",
		Challenge:         "http01",
		UI:                "basic",
	}
	with := func(distro string) Answers { a := pinned; a.Distro = distro; return a }

	/*
	 * The reported failure mode: a saved cloud-config reused months later installs whatever is latest
	 * that day — a control plane the rest of the file was never written for. (A mere restart never
	 * re-runs runcmd; re-provisioning does.) So the generator stamps the toolchain it ran under.
	 */
	t.Run("pstack is pinned to the generator's own version, and nothing else is installed", func(t *testing.T) {
		// negative control: substitute PSTACK_VERSION with "latest" — the first check fails.
		out := render(t, pinned)
		v := version.Get()
		if !strings.Contains(out, "releases/download/v"+v+"/install.sh | PSTACK_VERSION="+v+" sh") {
			t.Error("pstack not pinned")
		}
		// The installer of that release, never `latest`, and no JavaScript runtime anywhere.
		if strings.Contains(out, "releases/latest") || strings.Contains(out, "bun ") || strings.Contains(out, "unzip") {
			t.Error("unpinned installer, or a runtime the Go build does not need")
		}
	})

	t.Run("docker is deliberately NOT pinned, and the file says why", func(t *testing.T) {
		// Distro security updates are wanted; the reproducibility risk is the pstack toolchain, which is
		// pinned above. The reasoning must live in the artifact, since that is what gets saved.
		// negative control: edit the template's comment — fails (the template is the contract).
		if !strings.Contains(render(t, pinned), "DELIBERATELY not version-pinned") {
			t.Error("reasoning missing")
		}
	})

	managers := map[string]*regexp.Regexp{
		"ubuntu": regexp.MustCompile(`apt-get install`),
		"debian": regexp.MustCompile(`apt-get install`),
		"fedora": regexp.MustCompile(`dnf -y install`),
		"suse":   regexp.MustCompile(`zypper --non-interactive install`),
		"arch":   regexp.MustCompile(`pacman -Syu --noconfirm`),
		"alpine": regexp.MustCompile(`apk add --no-cache`),
	}

	for _, distro := range Distros {
		t.Run(distro+": renders valid YAML using only its own package manager", func(t *testing.T) {
			// negative control: point the debian profile at aptSetup("ubuntu") plus the fedora pkgSetup — cross-contamination fails.
			out := render(t, with(distro))
			// The whole file must stay parseable cloud-config — a distro fragment with wrong indentation
			// would corrupt the document while looking fine in a grep.
			doc, err := yamlx.ParseString(out)
			if err != nil {
				t.Fatal(err)
			}
			if doc.(*omap.Map).GetSlice("runcmd") == nil {
				t.Error("runcmd is not a list")
			}
			if !managers[distro].MatchString(out) {
				t.Error("own package manager missing")
			}
			// Cross-contamination is the failure this table design invites: one distro's commands leaking
			// into another's render. Assert every FOREIGN manager is absent — keyed on the pattern, not
			// the distro name, because ubuntu and debian legitimately share apt.
			for other, re := range managers {
				if other != distro && re.String() != managers[distro].String() && re.MatchString(out) {
					t.Errorf("%s's package manager leaked into %s", other, distro)
				}
			}
		})
	}

	t.Run("every systemd distro enables docker with systemctl; alpine uses OpenRC", func(t *testing.T) {
		// negative control: give alpine systemdEnable — the OpenRC assertions fail.
		for _, distro := range []string{"ubuntu", "debian", "fedora", "suse", "arch"} {
			out := render(t, with(distro))
			if !strings.Contains(out, "systemctl enable --now docker") || strings.Contains(out, "rc-update") {
				t.Errorf("%s: wrong enable", distro)
			}
		}
		alpine := render(t, with("alpine"))
		if !strings.Contains(alpine, "rc-update add docker boot") || !strings.Contains(alpine, "service docker start") || strings.Contains(alpine, "systemctl") {
			t.Error("alpine: wrong enable")
		}
	})

	t.Run("alpine adds bash and sudo — the template depends on both and the base image has neither", func(t *testing.T) {
		// negative control: empty alpine's extraPackages — fails.
		packages := func(distro string) []string {
			doc, err := yamlx.ParseString(render(t, with(distro)))
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, p := range doc.(*omap.Map).GetSlice("packages") {
				out = append(out, p.(string))
			}
			return out
		}
		alpine := packages("alpine")
		has := func(list []string, s string) bool { return some(list, func(x string) bool { return x == s }) }
		if !has(alpine, "bash") || !has(alpine, "sudo") {
			t.Errorf("alpine packages: %v", alpine)
		}
		// And ubuntu is NOT burdened with extras it already has.
		if has(packages(""), "bash") {
			t.Error("ubuntu carries bash")
		}
	})

	t.Run("debian pulls from the debian docker repo, not the ubuntu one", func(t *testing.T) {
		// negative control: aptSetup("ubuntu") for debian — fails.
		out := render(t, with("debian"))
		if !strings.Contains(out, "download.docker.com/linux/debian") || strings.Contains(out, "download.docker.com/linux/ubuntu") {
			t.Error("wrong apt repo")
		}
	})

	t.Run("the compose-plugin fallback ships on every distro", func(t *testing.T) {
		// suse/arch may package compose as a standalone binary; pstack shells out to `docker compose`
		// (the plugin form), so the symlink fallback is what keeps them working.
		// negative control: remove the fallback line from the template — fails.
		for _, distro := range Distros {
			if !strings.Contains(render(t, with(distro)), "cli-plugins/docker-compose") {
				t.Errorf("%s: no fallback", distro)
			}
		}
	})

	t.Run("distros that need a warning carry it in the FILE, and the default carries none", func(t *testing.T) {
		// The file is what gets saved and reused; a terminal warning scrolls away.
		// negative control: render DISTRO_NOTE as "" always — fails.
		for _, distro := range []string{"suse", "arch", "alpine"} {
			if !strings.Contains(render(t, with(distro)), "DISTRO NOTE ("+distro+")") {
				t.Errorf("%s: note missing", distro)
			}
		}
		if strings.Contains(render(t, pinned), "DISTRO NOTE") {
			t.Error("ubuntu carries a note")
		}
	})

	t.Run("an unknown distro is refused by name", func(t *testing.T) {
		// negative control: fall back to ubuntu for an unknown distro — no error.
		_, err := RenderCloudInit(with("gentoo"))
		if err == nil || !strings.Contains(err.Error(), `unknown distro "gentoo"`) {
			t.Errorf("got %v", err)
		}
	})
}

func TestWorkerCloudInit(t *testing.T) {
	t.Run("fills swarm's CloudInit seam at init", func(t *testing.T) {
		// negative control: drop the init() in cloudinit.go — Render is nil.
		if swarm.CloudInit.Render == nil || len(swarm.CloudInit.Distros) != len(Distros) {
			t.Fatal("seam not filled")
		}
		if got := swarm.CloudInit.Render("SWMTKN-1-abc-def", "10.0.0.1:2377", "ubuntu"); !strings.Contains(got, "docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377") {
			t.Errorf("seam render: %q", got)
		}
	})
	t.Run("refuses a non-token", func(t *testing.T) {
		// negative control: drop the SWMTKN check — no error.
		_, err := RenderWorkerCloudInit(WorkerAnswers{Token: "nope", ManagerAddr: "1.2.3.4:2377"})
		if err == nil || !strings.Contains(err.Error(), "join token") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("renders the join for the manager, valid YAML, with the distro's docker install", func(t *testing.T) {
		// negative control: drop the JoinCommand line — the join check fails.
		out, err := RenderWorkerCloudInit(WorkerAnswers{Token: "SWMTKN-1-abc-def", ManagerAddr: "10.0.0.1:2377", Distro: "alpine"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "  - docker swarm join --token SWMTKN-1-abc-def 10.0.0.1:2377") {
			t.Error("no join")
		}
		if !strings.Contains(out, "#     2377/tcp       cluster management (worker → manager)") {
			t.Error("port table misaligned")
		}
		if !strings.Contains(out, "apk add --no-cache") || !strings.Contains(out, "  - bash\n  - sudo\n") {
			t.Error("alpine fragments missing")
		}
		if !yamlOK(t, out) {
			t.Error("not valid YAML")
		}
	})
}

// The conformance transcripts: `pstack cloud-init … -y` prints the rendered file plus one newline,
// masked for the version. This is the whole-file compare.
func TestCloudInitGoldens(t *testing.T) {
	golden := Answers{
		Domain:            "preview.example.com",
		AcmeEmail:         "ops@example.com",
		SSHKey:            "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGoldenKeyForTheConformanceSuite0000000 golden@example",
		DashboardPassword: "golden-dashboard-password",
		Challenge:         "http01",
		UI:                "basic",
		// The CLI passes its default orchestrator explicitly.
		Orchestrator: "swarm",
	}
	cases := map[string]Answers{}
	for _, d := range Distros {
		a := golden
		a.Distro = d
		cases["cloud-init-"+d] = a
	}
	adv := golden
	adv.Challenge, adv.DNSProvider, adv.UI, adv.ConfigRepo = "dns01", "cloudflare", "advanced", "https://github.com/example/previews.git"
	cases["cloud-init-dns01-advanced-swarm"] = adv

	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			// negative control: change the wrap width in wrapRe to 80 — the suse/arch/alpine notes re-wrap and fail.
			b, err := os.ReadFile(filepath.Join(testfacts.Golden(t), "cli", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var g struct{ Stdout string }
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			got := render(t, a) + "\n"
			got = strings.ReplaceAll(got, version.Get(), "<VERSION>")
			if got != g.Stdout {
				t.Errorf("differs from the golden\n--- got\n%s\n--- want\n%s", got, g.Stdout)
			}
		})
	}
}
