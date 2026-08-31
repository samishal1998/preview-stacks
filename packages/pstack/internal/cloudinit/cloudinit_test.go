package cloudinit

import (
	"encoding/base64"
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

	t.Run("extra domains reach the init call, and a bad one is refused at render", func(t *testing.T) {
		// negative control: drop the ExtraDomains loop from initFlags — the rendered host answers on
		// the primary only, and the temporary domain the operator asked for silently does not exist.
		// They find out when the hostname they were told to use does not resolve to anything.
		a := base
		a.ExtraDomains = []string{"temp-one.example.com", "temp-two.example.com"}
		call := initCall(t, render(t, a))
		for _, d := range a.ExtraDomains {
			if !strings.Contains(call, "--extra-domain "+d) {
				t.Errorf("%s is missing from the init call: %q", d, call)
			}
		}
		// A typo must fail HERE, not on the host at boot where it is a cloud-init log nobody reads.
		bad := base
		bad.ExtraDomains = []string{"not a hostname"}
		if _, err := RenderCloudInit(bad); err == nil {
			t.Error("a malformed extra domain must be refused at render")
		}
		// The primary already has routers; naming it again would define the same one twice.
		same := base
		same.ExtraDomains = []string{same.Domain}
		if _, err := RenderCloudInit(same); err == nil || !strings.Contains(err.Error(), "already the primary") {
			t.Errorf("the primary must be refused as an extra: %v", err)
		}
	})

	t.Run("dns01 without a credential is refused, not rendered broken", func(t *testing.T) {
		// negative control: drop the DNSToken check in validate() — the render succeeds and hands
		// over a file whose host boots, writes an EMPTY variable into dns.env, and answers every
		// ACME order with "some credentials information are missing". It looks like a working
		// wildcard host until someone visits a preview. That shipped, and this is why.
		a := base
		a.Challenge, a.DNSProvider = "dns01", "cloudflare"
		if _, err := RenderCloudInit(a); err == nil || !strings.Contains(err.Error(), "empty dns.env") {
			t.Errorf("dns01 with no token must be refused: %v", err)
		}
		// http01 needs none, and must stay renderable with nothing supplied.
		if _, err := RenderCloudInit(base); err != nil {
			t.Errorf("http01 needs no DNS credential: %v", err)
		}
	})

	t.Run("dns01 adds the challenge flags to the init call; http01 adds none", func(t *testing.T) {
		// negative control: drop the dns01 branch of initFlags — the dns assertions fail.
		a := base
		a.Challenge, a.DNSProvider, a.DNSToken = "dns01", "hetzner", "the-dns-token"
		dns := initCall(t, render(t, a))
		// The credential travels on the init call, or the host renders an empty dns.env and every
		// ACME order fails with "some credentials information are missing".
		if !strings.Contains(dns, "PSTACK_DNS_TOKEN='the-dns-token'") {
			t.Errorf("the credential must reach the init call: %q", dns)
		}
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

// The credentials a cloud-config may carry: the first admin account and PSTACK_TOKEN. Both reach
// `init` as an environment PREFIX on the boot command, which is the part that can go wrong quietly —
// a prefix that lands outside the command, or a quote that ends early, produces a host that boots
// clean and cannot be signed into.
func TestCloudInitAdminAccountAndToken(t *testing.T) {
	withCreds := base
	withCreds.AdminUser, withCreds.AdminPassword, withCreds.Token = "alice", "correct-horse-battery", "tok-0123456789abcdef"

	t.Run("the env prefix stays part of the init command the shell will run", func(t *testing.T) {
		// The init call is a YAML folded scalar: the assignments must fold onto the same command,
		// not become a line of their own. Taken from the PARSED runcmd, so this asserts what
		// cloud-init hands the shell rather than what the template looks like.
		// negative control: render INIT_ENV on its own template line — the call no longer carries the prefix.
		out := render(t, withCreds)
		call := initCall(t, out)
		for _, want := range []string{
			"PSTACK_ADMIN_USER='alice' ", "PSTACK_ADMIN_PASSWORD='correct-horse-battery' ",
			"PSTACK_TOKEN='tok-0123456789abcdef' /usr/local/bin/pstack init",
			"--domain preview.example.com",
		} {
			if !strings.Contains(call, want) {
				t.Errorf("init call lacks %q:\n%s", want, call)
			}
		}
		if !yamlOK(t, out) || leftoverRe.MatchString(out) {
			t.Error("invalid YAML or placeholders left")
		}
	})

	t.Run("the header names every credential the file actually carries, and only those", func(t *testing.T) {
		// The dashboard password's warning is already in the header; anything added sits in the same
		// instance metadata and gets the same treatment. The negative case matters as much: a file
		// with no credential in it must keep saying so.
		// negative control: make SECRETS_LIST unconditional (the credential branch always) — the plain case fails.
		plain := render(t, base)
		if !strings.Contains(plain, "Nothing in this file is a credential except the dashboard password") {
			t.Error("the plain file stopped saying it carries nothing")
		}
		if strings.Contains(plain, "PSTACK_ADMIN_USER") || strings.Contains(plain, "PSTACK_TOKEN=") {
			t.Error("a credential rendered into a file that was given none")
		}
		full := render(t, withCreds)
		for _, want := range []string{
			"THIS FILE CARRIES CREDENTIALS",
			`the first pstack account ("alice")`,
			"PSTACK_TOKEN — the bearer token",
			"carries credentials IN THE CLEAR",
		} {
			if !strings.Contains(full, want) {
				t.Errorf("header lacks %q", want)
			}
		}
		// With no token given, the file must still say where the generated one appears — and must not
		// claim to hold one.
		adminOnly := withCreds
		adminOnly.Token = ""
		out := render(t, adminOnly)
		if !strings.Contains(out, "not the API bearer token: `init` generates one on the host") {
			t.Error("no account of the generated token")
		}
		if strings.Contains(out, "PSTACK_TOKEN='") {
			t.Error("a token in a file that was given none")
		}
	})

	t.Run("refuses credentials that would reach the host altered, or not at all", func(t *testing.T) {
		// Each of these boots a host whose admin password is not the one the operator was shown, and
		// says nothing until they try to sign in.
		// negative control: drop the `'$\n\r` check in validate — the last three cases pass.
		expectErr := func(a Answers, re string) {
			t.Helper()
			_, err := RenderCloudInit(a)
			if err == nil || !regexp.MustCompile(re).MatchString(err.Error()) {
				t.Errorf("want error /%s/, got %v", re, err)
			}
			if _, ok := err.(*Error); err != nil && !ok {
				t.Errorf("not a *cloudinit.Error: %T", err)
			}
		}
		a := withCreds
		a.AdminUser = "Alice@example.com" // auth would refuse it on the host, with nobody watching
		expectErr(a, "admin username must match")
		a = withCreds
		a.AdminPassword = "short"
		expectErr(a, "at least 8 characters")
		a = withCreds
		a.AdminPassword = "pa'ssword" // ends the shell quoting
		expectErr(a, "single quote")
		a = withCreds
		a.AdminPassword = "pa$$word" // Compose expands it out of .env
		expectErr(a, `\$`)
		a = withCreds
		a.Token = "tok\nen" // the folded scalar turns it into a space
		expectErr(a, "one line")
		// A marker inside a credential is expanded by the renderer itself: the value is placed at
		// INIT_ENV and PSTACK_VERSION is substituted after it, so the host booted with "0.29.0" as
		// the password while the operator was shown "{{PSTACK_VERSION}}".
		// negative control: drop the `{{` loop in validate — these three pass, and the rendered file
		// then contains the version where the credential should be.
		for _, m := range []struct {
			what string
			set  func(*Answers)
		}{
			{"admin password", func(x *Answers) { x.AdminPassword = "{{PSTACK_VERSION}}" }},
			{"PSTACK_TOKEN", func(x *Answers) { x.Token = "{{PSTACK_VERSION}}" }},
			{"dashboard password", func(x *Answers) { x.DashboardPassword = "{{PSTACK_VERSION}}" }},
		} {
			a = withCreds
			m.set(&a)
			expectErr(a, "must not contain")
		}
		// One brace is not a marker and must keep working — the check is `{{`, not `{`.
		a = withCreds
		a.AdminPassword = "pa{ssword"
		if out := render(t, a); !strings.Contains(out, "pa{ssword") {
			t.Error("a single brace was refused or mangled")
		}
		// The username is only checked when there is one — the account is opt-in.
		a = base
		a.AdminPassword = "" // nothing set: renders, and carries no account
		if out := render(t, a); strings.Contains(out, "PSTACK_ADMIN") {
			t.Error("an account without a username")
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
	// The same value gen/goldens.table.ts passes as PSTACK_DNS_TOKEN — dns01 will not render
	// without one, because a host that boots with an empty dns.env never gets a certificate.
	adv.DNSToken = "golden-dns-token-0123456789"
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

// The portable config (`pstack pull config` → `--config` / `--config-url`). The whole feature is a
// trade the operator has to be able to see, so most of what is asserted here is the SECRETS header
// telling the truth about which half of it they took.
//
// Everything below checks rendered text. No instance is booted anywhere in this repo, so the boot
// step's behaviour — that the API answers on the bridge address, that the file is really gone — is
// NOT covered by any test; what is covered is that the commands cloud-init will hand the shell are
// the ones intended, taken from the PARSED runcmd rather than scraped out of the prose.
func TestCloudInitPortableConfig(t *testing.T) {
	sealedBytes := []byte(`{"version":1,"sealed":"scrypt-aes256gcm","payload":"AA=="}`)
	const key = "correct horse $tapler" // a `$` is legal: it is a shell assignment, not an argument
	embed := base
	embed.ConfigSealed, embed.ConfigKey = sealedBytes, key
	fetch := base
	fetch.ConfigURL, fetch.ConfigKey = "https://vault.example.com/pstack.sealed", key

	applyStep := func(t *testing.T, yaml string) string {
		t.Helper()
		for _, c := range runcmd(t, yaml) {
			if strings.Contains(c, "pstack push config") {
				return c
			}
		}
		return ""
	}
	writeFiles := func(t *testing.T, yaml string) []any {
		t.Helper()
		doc, err := yamlx.ParseString(yaml)
		if err != nil {
			t.Fatal(err)
		}
		return doc.(*omap.Map).GetSlice("write_files")
	}

	t.Run("--config embeds the sealed payload 0600, byte for byte", func(t *testing.T) {
		// negative control: render CONFIG_FILE as "" — write_files disappears and the first check fails.
		out := render(t, embed)
		if !yamlOK(t, out) || leftoverRe.MatchString(out) {
			t.Fatal("invalid YAML or placeholders left")
		}
		files := writeFiles(t, out)
		if len(files) != 1 {
			t.Fatalf("want one write_files entry, got %d", len(files))
		}
		f, ok := files[0].(*omap.Map)
		if !ok {
			t.Fatalf("write_files entry is %T", files[0])
		}
		if f.GetString("path") != "/var/lib/pstack/config.sealed" || f.GetString("permissions") != "0600" || f.GetString("encoding") != "b64" {
			t.Errorf("write_files entry: path=%q permissions=%q encoding=%q", f.GetString("path"), f.GetString("permissions"), f.GetString("encoding"))
		}
		// Base64 is not cosmetic: it is the one encoding whose alphabet contains no `{`, so the
		// payload cannot be rewritten by the substitution that places it, and no byte of it can
		// reach the YAML around it.
		got, err := base64.StdEncoding.DecodeString(f.GetString("content"))
		if err != nil || string(got) != string(sealedBytes) {
			t.Errorf("the embedded payload is not the sealed file: %q (%v)", got, err)
		}
	})

	t.Run("--config-url keeps the payload off instance metadata", func(t *testing.T) {
		// The only thing this mode buys is that the export is NOT here. If a write_files block ever
		// appeared for it, the two modes would be identical and the header's promise would be false.
		// negative control: render configFile for ConfigURL too — the write_files check fails.
		out := render(t, fetch)
		if !yamlOK(t, out) || leftoverRe.MatchString(out) {
			t.Fatal("invalid YAML or placeholders left")
		}
		if len(writeFiles(t, out)) != 0 {
			t.Error("--config-url wrote the payload into the file it was chosen to keep it out of")
		}
		step := applyStep(t, out)
		if !strings.Contains(step, `curl -fsSL --retry 3 --retry-delay 2 -o "$f" 'https://vault.example.com/pstack.sealed' &&`) {
			t.Errorf("no guarded fetch in the apply step:\n%s", step)
		}
	})

	t.Run("the header states which of the two was used, and both name the key", func(t *testing.T) {
		// This is the requirement: the seal protects the file everywhere except the box it provisions
		// under --config, and only the key is exposed under --config-url. Saying "it is encrypted" and
		// stopping would be true and useless.
		// negative control: use the --config wording for both — the --config-url assertions fail.
		e, f := render(t, embed), render(t, fetch)
		for _, out := range []string{e, f} {
			if !strings.Contains(out, "THIS FILE CARRIES CREDENTIALS") || !strings.Contains(out, "PSTACK_CONFIG_KEY") {
				t.Error("a file carrying the passphrase does not say so")
			}
			if !strings.Contains(out, "DELETES /var/lib/pstack/config.sealed as soon as it has applied it") {
				t.Error("the header does not promise the deletion")
			}
		}
		if !strings.Contains(e, "*and the sealed export itself*") || !strings.Contains(e, "the seal protects that export everywhere EXCEPT here") {
			t.Error("--config does not admit that the key sits beside the payload")
		}
		if strings.Contains(e, "but NOT the export it opens") {
			t.Error("--config claims the payload is elsewhere")
		}
		if !strings.Contains(f, "but NOT the export it opens") || !strings.Contains(f, "https://vault.example.com/pstack.sealed") {
			t.Error("--config-url does not say where the export actually is")
		}
		if strings.Contains(f, "*and the sealed export itself*") {
			t.Error("--config-url claims to carry the payload")
		}
		// A file with no config in it must go on saying it carries nothing.
		if !strings.Contains(render(t, base), "Nothing in this file is a credential except the dashboard password") {
			t.Error("the plain file stopped saying it carries nothing")
		}
	})

	t.Run("the apply step deletes the sealed file whether or not it worked", func(t *testing.T) {
		// A sealed export left in /var/lib is a credential at rest that nothing would ever clean up.
		// `rm` sits AFTER the if, not inside it, so the failure path deletes it too — that ordering is
		// the assertion, not the presence of the word.
		// negative control: drop the `rm -f "$f"` line from configStep — the first check fails.
		step := applyStep(t, render(t, embed))
		if step == "" {
			t.Fatal("no apply step in the rendered file")
		}
		rm := strings.Index(step, `rm -f "$f"`)
		iff := strings.Index(step, `if `)
		if rm < 0 || iff < 0 || rm < iff {
			t.Errorf("rm at %d, if at %d — the delete must follow the attempt unconditionally:\n%s", rm, iff, step)
		}
		// And from inside the container too, where `docker cp` put a second copy.
		if !strings.Contains(step, "rm -f /tmp/config.sealed") {
			t.Errorf("the copy inside the container is never deleted:\n%s", step)
		}
		if !strings.Contains(step, `[ "$applied" = yes ] || echo 'pstack: THE SEALED CONFIG WAS NOT APPLIED`) {
			t.Errorf("a failed apply is silent:\n%s", step)
		}
		// The passphrase is a shell VARIABLE, never an argument: /proc/<pid>/cmdline is world-readable.
		if !strings.Contains(step, "PSTACK_CONFIG_KEY='"+key+"'\nexport PSTACK_CONFIG_KEY") {
			t.Errorf("the passphrase is not a plain assignment:\n%s", step)
		}
		if strings.Contains(step, "PSTACK_CONFIG_KEY='"+key+"' /usr/local/bin/pstack") {
			t.Error("the passphrase became a command-line env prefix, visible in `ps`")
		}
		// The token is never read into a host shell at all: `docker exec` runs inside the container,
		// which already holds PSTACK_TOKEN in its own environment.
		if strings.Contains(step, "control/.env") {
			t.Errorf("the token is read out of control/.env, into a host shell that does not need it:\n%s", step)
		}
	})

	// THE BUG THIS EXISTS FOR: the first version dialled the control container's own address, taken
	// from `docker inspect`. Under `--orchestrator swarm` — the DEFAULT — preview-ingress and
	// preview-shared are attachable OVERLAY networks, so every address it can report is one the host
	// has no route to. A real Hetzner boot failed with `dial tcp 10.0.1.2:7878: i/o timeout`, and no
	// amount of waiting would have helped: a timeout rather than a refusal is what unroutable looks
	// like, and the container was already healthy by then because `init` blocks on it.
	//
	// negative control: put the `docker inspect …IPAddress…` line and `PSTACK_API_URL="http://$ip:7878"`
	// back — the first two checks fail.
	t.Run("the apply runs inside the container, never against an address the host must route to", func(t *testing.T) {
		step := applyStep(t, render(t, embed))
		if strings.Contains(step, "IPAddress") || strings.Contains(step, "docker inspect") {
			t.Errorf("the step still picks an address out of `docker inspect`:\n%s", step)
		}
		if !strings.Contains(step, "PSTACK_API_URL=http://127.0.0.1:7878") || !strings.Contains(step, "docker exec") {
			t.Errorf("the step does not run inside the container against loopback:\n%s", step)
		}
		// Bounded: a config that cannot apply must fail the boot loudly, not hang it forever.
		if !strings.Contains(step, `while [ "$n" -le 5 ]`) {
			t.Errorf("the retry is absent or unbounded:\n%s", step)
		}
	})

	t.Run("refuses a config that would reach the host altered, or a key with nothing to open", func(t *testing.T) {
		// negative control: drop {"config passphrase", a.ConfigKey} from the `{{` loop in validate — the
		// first case renders, and the rendered file then carries the pstack version as the passphrase.
		expectErr := func(a Answers, re string) {
			t.Helper()
			_, err := RenderCloudInit(a)
			if err == nil || !regexp.MustCompile(re).MatchString(err.Error()) {
				t.Errorf("want error /%s/, got %v", re, err)
			}
			if _, ok := err.(*Error); err != nil && !ok {
				t.Errorf("not a *cloudinit.Error: %T", err)
			}
		}
		a := embed
		a.ConfigKey = "{{PSTACK_VERSION}}"
		expectErr(a, "must not contain")
		a = embed
		a.ConfigKey = "pa'ssword" // ends the shell quoting inside the boot script
		expectErr(a, "single quote")
		a = embed
		a.ConfigKey = "two\nlines"
		expectErr(a, "one line")
		a = embed
		a.ConfigURL = fetch.ConfigURL // both at once
		expectErr(a, "not both")
		a = embed
		a.ConfigKey = ""
		expectErr(a, "needs its passphrase")
		a = base
		a.ConfigKey = key // a key in metadata with nothing to open
		expectErr(a, "nothing to open")
		a = fetch
		a.ConfigURL = "https://vault.example.com/a'b"
		expectErr(a, "no quote or whitespace")
		a = fetch
		a.ConfigURL = "file:///etc/shadow"
		expectErr(a, "http")
		// One brace is not a marker: the check is `{{`, and a legal passphrase must reach the boot
		// script byte for byte. This is the assertion on the OUTPUT that the refusals above cannot
		// make — with the `{{` check dropped, the same shape of value is silently rewritten instead.
		a = embed
		a.ConfigKey = "pa{ssword$x"
		if out := render(t, a); !strings.Contains(out, "PSTACK_CONFIG_KEY='pa{ssword$x'") {
			t.Error("a legal passphrase was refused or rewritten on its way into the file")
		}
	})

	t.Run("a file with no config in it is unchanged, down to the byte", func(t *testing.T) {
		// Both markers are appended to the END of an existing line for exactly this reason: an empty
		// value must leave no trace, or every checked-in cloud-init transcript moves.
		// negative control: drop the `len(a.ConfigSealed) > 0` guard on configFile — a write_files block
		// renders into a file that was given no config, and the first check fails.
		out := render(t, base)
		if strings.Contains(out, "config.sealed") || strings.Contains(out, "PSTACK_CONFIG_KEY") || regexp.MustCompile(`(?m)^write_files:`).MatchString(out) {
			t.Error("a config step rendered into a file that was given none")
		}
	})
}
