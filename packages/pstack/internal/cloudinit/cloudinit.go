// Package cloudinit is `pstack cloud-init` — fill the cloud-config template and print a
// ready-to-boot user-data file.
//
// The template is embedded at build time (like the control-stack compose file), so this works from
// a global install with no checkout — the same property `build-image` needed.
//
// Placeholders are `{{NAME}}`, deliberately NOT `${NAME}`: the template contains real shell that
// must survive rendering untouched (`$(dpkg --print-architecture)`, `${STACK}`, `${DOMAIN}` for
// Compose). Using the same syntax for both would mean the generator eating lines meant for bash.
//
// `{{...}}` does collide with one other thing in the file — Docker's Go templates, e.g.
// `docker inspect -f '{{.State.Health.Status}}'`. They coexist because the pattern here is
// `[A-Z_]+` only: a Go template carries dots and lower case, so neither the substitution nor the
// leftover-check below can touch it. Keep placeholder names SHOUT_CASE and that stays true.
//
// Substitution is strings.ReplaceAll over an ORDERED list (rule 8) — the TS did
// `out.split('{{K}}').join(v)`, which has none of String.replace's `$` magic, and neither does this.
package cloudinit

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// The TS swarm.ts dynamically imported cloudinit for the worker cloud-config; Go has no cycles, so
// swarm is the leaf and this package fills its seam. Token and address come from docker itself, so
// the render cannot be refused; if it ever is, the message is the text — visible, not an empty file.
func init() {
	swarm.CloudInit.Distros = Distros
	swarm.CloudInit.Render = func(token, managerAddr, distro string) string {
		out, err := RenderWorkerCloudInit(WorkerAnswers{Token: token, ManagerAddr: managerAddr, Distro: distro})
		if err != nil {
			return err.Error()
		}
		return out
	}
}

// Distros are the targets the generator can render for.
//
// What actually differs is smaller than it looks: how Docker is installed and how its service is
// enabled. Everything else — cloud-init's own `users`/`groups`/`packages` stages, the Bun installer,
// pstack, the compose files — is identical across them. So a distro is a table row, not a template.
//
// Docker sources, and why they differ:
//   - ubuntu/debian/fedora: Docker's OWN repositories, which is the vendor-recommended path and what
//     the original Ubuntu-only template already did. Fedora avoids `dnf config-manager` on purpose —
//     its syntax CHANGED between dnf4 and dnf5 (Fedora ≥41), and downloading the .repo file directly
//     sidesteps the divergence entirely.
//   - suse/arch/alpine: Docker publishes no repository for these; the distro's own packages are the
//     supported path. Their compose package may ship a standalone `docker-compose` binary rather than
//     the CLI plugin, which is why the template carries a plugin-symlink fallback after this block.
//
// Alpine is the odd one out twice: OpenRC instead of systemd, and no bash in the base image — the
// template's own runcmd lines use `bash -c`, so bash (and sudo, for the `preview` user's sudoers
// entry) ride in through extraPackages.
var Distros = []string{"ubuntu", "debian", "fedora", "suse", "arch", "alpine"}

type distroProfile struct {
	// imageHint is the example image name for the header's `hcloud server create` line.
	imageHint string
	// extraPackages are extra entries for cloud-init's `packages:` stage.
	extraPackages []string
	// pkgSetup is the runcmd fragment installing docker + compose. Complete YAML list items, 2-space indented.
	pkgSetup string
	// dockerEnable is the runcmd fragment enabling + starting the docker service.
	dockerEnable string
	// note is rendered into the file header when the target needs a warning.
	note string
}

func aptSetup(repo string) string {
	return strings.Join([]string{
		"  - install -m 0755 -d /etc/apt/keyrings",
		"  - curl -fsSL https://download.docker.com/linux/" + repo + "/gpg -o /etc/apt/keyrings/docker.asc",
		"  - chmod a+r /etc/apt/keyrings/docker.asc",
		"  - >",
		`    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc]`,
		`    https://download.docker.com/linux/` + repo + ` $(. /etc/os-release && echo "$VERSION_CODENAME") stable"`,
		"    > /etc/apt/sources.list.d/docker.list",
		"  - apt-get update",
		"  - DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io",
		"      docker-buildx-plugin docker-compose-plugin",
	}, "\n")
}

const systemdEnable = "  - systemctl enable --now docker"

var distroProfiles = map[string]distroProfile{
	"ubuntu": {
		imageHint:     "ubuntu-24.04",
		extraPackages: []string{},
		pkgSetup:      aptSetup("ubuntu"),
		dockerEnable:  systemdEnable,
	},
	"debian": {
		imageHint:     "debian-12",
		extraPackages: []string{},
		pkgSetup:      aptSetup("debian"),
		dockerEnable:  systemdEnable,
	},
	"fedora": {
		imageHint:     "fedora-40",
		extraPackages: []string{},
		// The .repo file straight into yum.repos.d — NOT `dnf config-manager`, whose flag syntax changed
		// between dnf4 and dnf5. A curl works identically on both.
		pkgSetup: strings.Join([]string{
			"  - curl -fsSL https://download.docker.com/linux/fedora/docker-ce.repo -o /etc/yum.repos.d/docker-ce.repo",
			"  - dnf -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		}, "\n"),
		dockerEnable: systemdEnable,
	},
	"suse": {
		imageHint:     "opensuse-leap-15.6",
		extraPackages: []string{},
		// Docker publishes no SUSE repository; the distro packages are the supported path.
		pkgSetup:     "  - zypper --non-interactive install docker docker-compose docker-buildx",
		dockerEnable: systemdEnable,
		note:         "openSUSE installs Docker from the distro repositories (Docker publishes none for SUSE).",
	},
	"arch": {
		imageHint:     "arch-linux (cloud image)",
		extraPackages: []string{"sudo"},
		// -Syu, not -Sy: a partial sync followed by an install is the classic Arch breakage.
		pkgSetup:     "  - pacman -Syu --noconfirm docker docker-compose docker-buildx",
		dockerEnable: systemdEnable,
		note: "Arch cloud images vary in cloud-init support — verify yours ships cloud-init before booting " +
			"with this file. Docker comes from the distro repositories.",
	},
	"alpine": {
		imageHint: "alpine-3.20 (cloud image)",
		// The template runs `bash -c` in several runcmd lines and grants the preview user sudo; neither
		// ships in the Alpine base image.
		extraPackages: []string{"bash", "sudo"},
		pkgSetup:      "  - apk add --no-cache docker docker-cli-compose docker-cli-buildx",
		// OpenRC, not systemd.
		dockerEnable: strings.Join([]string{"  - rc-update add docker boot", "  - service docker start"}, "\n"),
		note: "Alpine: OpenRC (not systemd), musl (the static pstack binary runs unchanged), " +
			"and cloud-init support varies by image — verify yours ships it. bash and sudo are added to " +
			"the package list because this file depends on both.",
	},
}

// Answers are the inputs to RenderCloudInit. Plain strings where the TS had literal unions
// (`challenge`, `ui`, `orchestrator`): a value that is not one of the expected words renders the
// default branch, exactly as an untyped JS caller would get.
type Answers struct {
	Domain    string
	AcmeEmail string
	// SSHKey is optional: most providers inject their own key at boot (hcloud --ssh-key).
	SSHKey            string
	DashboardPassword string
	// Challenge is "http01" or "dns01".
	Challenge   string
	DNSProvider string
	// UI is "basic" or "advanced".
	UI string
	// AdminUser is the first pstack account, created by the control plane on first boot. Empty (the
	// default) renders no account at all, and the operator makes one afterwards with
	// `POST /api/auth/bootstrap` and the bearer token.
	AdminUser string
	// AdminPassword is that account's password. Only read when AdminUser is set.
	AdminPassword string
	// ExtraDomains are additional hostnames the host answers on from its first boot, passed through
	// to `init` as --extra-domain. The case: the primary's DNS still points at the old box, so its
	// routers wait while one of these is usable now.
	ExtraDomains []string
	// DNSToken is the DNS-01 credential (PSTACK_DNS_TOKEN on the rendered init call). REQUIRED with
	// `dns01`: without it the host boots, renders `dns.env` empty, and every ACME order fails with
	// "some credentials information are missing" — a wildcard host that never gets a certificate,
	// which is not a state worth rendering a file for. It DOES land in instance metadata, so the
	// header says so rather than claiming the file holds no credential.
	DNSToken string
	// Token is PSTACK_TOKEN for the host. Empty (the default) is the SAFER one: `init` generates a
	// token on the box and prints it once into the boot log, so it never exists in instance metadata
	// at all. Set it only when something already holds the token — a CI secret, a second host.
	Token string
	// ConfigRepo is an optional git URL cloned to /opt/preview/config for driving the CLI from the host.
	ConfigRepo string
	// ConfigSealed is the sealed bytes of a `pstack pull config` export, embedded with write_files;
	// ConfigURL is the alternative, fetched by the host at boot. Mutually exclusive, and either one
	// needs ConfigKey. Opaque here on purpose: this package renders text and has no business
	// importing the store that `internal/config` reaches — the CLI unseals the file before calling,
	// which is where a wrong passphrase is caught.
	ConfigSealed []byte
	ConfigURL    string
	// ConfigKey is the passphrase, and it IS embedded in both modes: the box being provisioned has to
	// open the file with no human present. That is the security statement the SECRETS header has to
	// make honestly rather than paper over — with --config the payload sits beside the key in
	// instance metadata, so the seal protects it everywhere except here; with --config-url only the
	// key is here, and opening the export needs the metadata AND access to the URL.
	ConfigKey string
	// Distro decides the Docker install/enable fragments. Default ubuntu.
	Distro string
	// Orchestrator is `swarm` (the default for a new host) or `compose`; passed to
	// `pstack init --orchestrator` only when set.
	Orchestrator string
}

// Error is a refused input or a template that did not render. The CLI prints its message and exits 3.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// RandomPassword is hex, because a `$` in a password would break the single-quoted shell that
// hashes it.
func RandomPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var (
	domainRe = regexp.MustCompile(`(?i)^[a-z0-9.-]+\.[a-z]{2,}$`)
	// The same rule internal/auth enforces when it creates the account.
	adminUserRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)
	emailRe     = regexp.MustCompile(`(?i)^[^@\s]+@[^@\s]+\.[a-z]{2,}$`)
	sshKeyRe    = regexp.MustCompile(`^(ssh-(rsa|ed25519)|ecdsa-sha2-\S+) \S+`)
	tokenRe     = regexp.MustCompile(`^SWMTKN-[A-Za-z0-9-]+$`)
	// The URL is interpolated into a single-quoted `curl` argument in the boot script, so a quote or
	// whitespace would end it early and hand curl a different address than the operator was shown.
	configURLRe = regexp.MustCompile(`^https?://[^\s'"\\]+$`)
	addrRe      = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)
	wrapRe      = regexp.MustCompile(`.{1,92}(\s|$)`)
	leftoverRe  = regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
)

func validate(a Answers) error {
	if !domainRe.MatchString(a.Domain) {
		return &Error{`domain "` + a.Domain + `" does not look like a hostname`}
	}
	if !emailRe.MatchString(a.AcmeEmail) {
		return &Error{`acme email "` + a.AcmeEmail + `" does not look like an address`}
	}
	// Optional, because a provider usually injects one already (`hcloud server create --ssh-key`) and
	// demanding a second copy just to render a file is friction. But if one IS given it is checked:
	// a malformed key produces a booted host you cannot log into, which is the one failure here with
	// no cheap recovery.
	if a.SSHKey != "" && !sshKeyRe.MatchString(a.SSHKey) {
		return &Error{`ssh key must be an authorized_keys line, e.g. "ssh-ed25519 AAAA… you@laptop"`}
	}
	if a.Challenge == "dns01" && a.DNSProvider == "" {
		return &Error{"dns01 needs a provider code (see https://go-acme.github.io/lego/dns/)"}
	}
	// A dns01 file with no credential renders a host that boots BROKEN: `init` writes an empty
	// variable into dns.env and Traefik answers every order with "some credentials information are
	// missing". Refuse it rather than hand over a file the operator will believe works — the same
	// rule an admin password with no account follows.
	for _, d := range a.ExtraDomains {
		if !domainRe.MatchString(d) {
			return &Error{"--extra-domain " + d + " is not a hostname"}
		}
		if strings.EqualFold(d, a.Domain) {
			return &Error{"--extra-domain " + d + " is already the primary domain — it has routers either way"}
		}
	}
	if a.Challenge == "dns01" && a.DNSToken == "" {
		return &Error{"dns01 needs the credential too, or the host boots with an empty dns.env and never gets a certificate — pass --dns-token-file <path> or set PSTACK_DNS_TOKEN. It is embedded in the rendered file, which the provider stores as instance metadata"}
	}
	if strings.Contains(a.DashboardPassword, "'") {
		// It is interpolated into a single-quoted shell command in the template.
		return &Error{"dashboard password must not contain a single quote"}
	}
	// The admin pair is checked HERE and not on the host, because everything it can get wrong fails
	// at a point where nobody is watching: `serve` refuses the account with one line on the container's
	// stderr, the boot reports success, and the operator meets the failure as "my password does not
	// work" days later. The rule is auth's own (internal/auth `createUser`) — copied rather than
	// imported so the generator does not pull the database layer in for one regexp; if that rule ever
	// moves, this is the message that goes stale.
	if a.AdminUser != "" && !adminUserRe.MatchString(a.AdminUser) {
		return &Error{`admin username must match /^[a-z0-9][a-z0-9._-]{1,31}$/ — lowercase, 2–32 chars, letters/digits/._-`}
	}
	if a.AdminUser != "" && js.Len(a.AdminPassword) < 8 {
		return &Error{"admin password must be at least 8 characters"}
	}
	// Both values travel through a single-quoted shell assignment in the boot script and then into
	// control/.env, which Compose interpolates — so `'` ends the quoting and `$` silently becomes
	// something else by the time the container reads it. A newline is worse than either: the init
	// call is a YAML folded scalar, where one becomes a space, so the credential the container gets
	// is not the credential the operator was shown. That is precisely the failure this feature exists
	// to remove, so the three are refused rather than escaped. (The generated password is hex for the
	// same reason, and so is `init`'s generated token.)
	for _, s := range []struct{ what, value string }{{"admin password", a.AdminPassword}, {"PSTACK_TOKEN", a.Token}} {
		if strings.ContainsAny(s.value, "'$\n\r") {
			return &Error{s.what + " must be one line with no single quote and no `$`: it is shell-quoted into this file and expanded by Compose on the host"}
		}
	}
	// A credential may not contain a substitution marker, because it is PLACED by substitution and the
	// list is walked in order: a `{{PSTACK_VERSION}}` inside a value put down at INIT_ENV (index 6) is
	// still there when PSTACK_VERSION (last) is replaced, so the host boots with `0.29.0` as its
	// password while the operator was shown the marker. A marker that comes EARLIER in the list is
	// caught by leftoverRe instead and hard-errors, so today the same input either corrupts silently
	// or fails obscurely depending on which marker was named — neither is an answer.
	//
	// `{{` and not `{`: expansion needs the literal marker, so this refuses exactly the values that
	// could be rewritten and leaves a password with one brace in it working. Every credential is
	// checked, including the dashboard password, whose quoting rules above differ but whose exposure
	// to this does not.
	for _, s := range []struct{ what, value string }{
		{"admin password", a.AdminPassword}, {"PSTACK_TOKEN", a.Token}, {"dashboard password", a.DashboardPassword},
		// The passphrase and the fetch URL go through exactly the same ordered ReplaceAll, so they
		// belong in this list and not in a second one beside it. The sealed PAYLOAD does not: it is
		// base64-encoded before it is placed, and `{` is not in that alphabet.
		{"config passphrase", a.ConfigKey}, {"config URL", a.ConfigURL},
	} {
		if strings.Contains(s.value, "{{") {
			return &Error{s.what + " must not contain `{{`: this file is rendered by substitution, so a marker inside a credential is replaced and the host gets a different credential than you were shown"}
		}
	}
	// ── the portable config ─────────────────────────────────────────────────────────────────────
	// Both forms embed the passphrase, so a key with nothing to open is a credential in instance
	// metadata for no reason, and a payload with no key is a boot step that can only fail. Neither
	// is a state this generator will render.
	if len(a.ConfigSealed) > 0 && a.ConfigURL != "" {
		return &Error{"pass either --config or --config-url, not both — the host applies one file"}
	}
	if a.ConfigKey != "" && len(a.ConfigSealed) == 0 && a.ConfigURL == "" {
		return &Error{"a config passphrase was given but no --config or --config-url: that would put a key in instance metadata with nothing to open"}
	}
	if (len(a.ConfigSealed) > 0 || a.ConfigURL != "") && a.ConfigKey == "" {
		return &Error{"applying a config at boot needs its passphrase (PSTACK_CONFIG_KEY) — the host has no one to ask"}
	}
	// Single quotes only. The passphrase is a shell ASSIGNMENT inside the boot script, never an
	// argument, so `$` is literal there and stays allowed; a `'` would end the quoting, and a newline
	// would end the line — either hands the host a different passphrase than the file was sealed
	// with, and the failure surfaces as "the config was not applied" long after anyone is watching.
	if strings.ContainsAny(a.ConfigKey, "'\n\r") {
		return &Error{"the config passphrase must be one line with no single quote: it is shell-quoted into the boot script"}
	}
	if a.ConfigURL != "" && !configURLRe.MatchString(a.ConfigURL) {
		return &Error{`--config-url must be an http(s) URL with no quote or whitespace, e.g. https://example.com/pstack.sealed`}
	}
	return nil
}

func profileFor(distro string) (string, distroProfile, error) {
	if distro == "" {
		distro = "ubuntu"
	}
	p, ok := distroProfiles[distro]
	if !ok {
		return "", distroProfile{}, &Error{`unknown distro "` + distro + `" — expected one of ` + strings.Join(Distros, ", ")}
	}
	return distro, p, nil
}

// kv is one ordered substitution.
type kv struct{ k, v string }

// RenderCloudInit renders the template. Pure: no prompting, no filesystem — so it is trivially testable.
func RenderCloudInit(a Answers) (string, error) {
	if err := validate(a); err != nil {
		return "", err
	}
	distro, profile, err := profileFor(a.Distro)
	if err != nil {
		return "", err
	}

	var initFlags []string
	if a.Challenge == "dns01" {
		initFlags = append(initFlags, "--challenge dns01", "--dns-provider "+a.DNSProvider)
	}
	for _, d := range a.ExtraDomains {
		initFlags = append(initFlags, "--extra-domain "+d)
	}
	if a.UI == "advanced" {
		initFlags = append(initFlags, "--ui advanced")
	}
	if a.Orchestrator != "" {
		initFlags = append(initFlags, "--orchestrator "+a.Orchestrator)
	}

	// Omit the key list entirely rather than emit an empty one: cloud-init would accept
	// `ssh_authorized_keys:` with nothing under it, and the result is a user with no way in and no
	// error to explain it. With the block absent, the provider's own injected key is the only one,
	// which is the normal case.
	sshBlock := "    # No key here: the provider injects its own at boot (e.g. `hcloud server create --ssh-key`).\n"
	if a.SSHKey != "" {
		sshBlock = "    ssh_authorized_keys:\n      - " + a.SSHKey + "\n"
	}
	// Continuation lines: the init call is a YAML folded scalar, so each flag goes on its own line
	// at the same indentation.
	initExtra := ""
	if len(initFlags) > 0 {
		initExtra = "\n    " + strings.Join(initFlags, "\n    ")
	}
	// The advanced UI needs its own image built before `init --ui advanced` can find it. Only the
	// build — the UI package is a build-time input fetched INSIDE the image, so nothing is
	// installed on the host for it.
	uiImageStep := ""
	if a.UI == "advanced" {
		uiImageStep = "\n  # The advanced UI is opt-in and needs its own image.\n" +
			"  - /usr/local/bin/pstack build-image --ui"
	}
	// A caveat the operator must read BEFORE booting belongs in the artifact itself, not in the
	// terminal output that scrolled away — this file gets saved and reused.
	distroNote := ""
	if profile.note != "" {
		var lines []string
		for _, l := range wrapRe.FindAllString(profile.note, -1) {
			lines = append(lines, "# "+strings.TrimSpace(l))
		}
		distroNote = "\n#\n# ── DISTRO NOTE (" + distro + ") ─────────────────────────────────────────────────────────────────\n" +
			strings.Join(lines, "\n")
	}
	// ── The credentials this file may carry ─────────────────────────────────────────────────────
	// An env PREFIX on the init call rather than flags, because that is how `init` already takes
	// PSTACK_TOKEN (there is no --token) and `serve` already takes the admin pair — one spelling of
	// each name across the host. Single-quoted: validate() has refused `'` and `$` in them, so this
	// quoting is what makes that refusal sufficient rather than a second guess at the shell's rules.
	//
	// Every block below defaults to EXACTLY what the file said before this existed. A cloud-config
	// with no admin and no token in it is unchanged, down to the byte — including the header
	// sentence "nothing in this file is a credential except the dashboard password", which is true
	// only of that file and had to stop being unconditional prose for it to stay true.
	var initEnv []string
	if a.AdminUser != "" {
		initEnv = append(initEnv, "PSTACK_ADMIN_USER='"+a.AdminUser+"'", "PSTACK_ADMIN_PASSWORD='"+a.AdminPassword+"'")
	}
	if a.Token != "" {
		initEnv = append(initEnv, "PSTACK_TOKEN='"+a.Token+"'")
	}
	if a.DNSToken != "" {
		initEnv = append(initEnv, "PSTACK_DNS_TOKEN='"+a.DNSToken+"'")
	}
	initEnvPrefix := ""
	if len(initEnv) > 0 {
		initEnvPrefix = strings.Join(initEnv, " ") + " "
	}

	// The TLS section of the header. It was static HTTP-01 prose, which a dns01 file then carried
	// verbatim — telling the operator "there is no DNS credential anywhere in this file" on the one
	// file that needs one. The http01 branch is that original text, unchanged.
	tlsSection := strings.Join([]string{
		"# ── TLS: HTTP-01, so there is no DNS credential anywhere in this file ────────────────────────",
		"# Traefik answers the challenge on port 80, so **port 80 must be reachable from the internet** —",
		"# do not firewall it off \"because everything is HTTPS\". The web→websecure redirect does not break",
		"# it: Traefik installs an internal ACME router at maximum priority that bypasses the redirect for",
		"# /.well-known/acme-challenge/.",
		"#",
		"# The cost of HTTP-01: it CANNOT issue wildcards, so every hostname gets its own certificate.",
		"# Let's Encrypt allows ~50 new certificates per registered domain per week, so at ~3 surfaces per",
		"# PR that is roughly **16 new PRs/week** before issuance starts failing — and a preview URL is not",
		"# valid until its container actually exists. When you outgrow that, switch to a wildcard by adding",
		"#     --challenge dns01 --dns-provider <lego-code>",
		"# plus PSTACK_DNS_TOKEN=… to the `pstack init` call. Nothing else changes.",
	}, "\n")
	if a.Challenge == "dns01" {
		tlsSection = strings.Join([]string{
			"# ── TLS: DNS-01, and the credential for it IS in this file ───────────────────────────────────",
			"# Traefik proves the domain by writing a TXT record through your DNS provider, so port 80 needs",
			"# no special treatment and a hostname is covered before its container exists.",
			"#",
			"# ONE wildcard certificate covers `*." + a.Domain + "` — no per-hostname issuance, so none of",
			"# HTTP-01's ~50-per-registered-domain-per-week ceiling applies. The cost is the credential on",
			"# the `pstack init` call below (PSTACK_DNS_TOKEN), which lands in instance metadata with this",
			"# file. It can edit DNS for the zone, so scope it to that zone and rotate it if this file leaks.",
			"#",
			"# The per-PR rule INVERTS here: every preview router carries `tls=true` and NOTHING else, and",
			"# one always-on router requests the wildcard. A `tls.certresolver` copied from an HTTP-01 host",
			"# makes each PR order its own certificate.",
		}, "\n")
	}

	secrets := []string{
		"# Nothing in this file is a credential except the dashboard password: HTTP-01 needs no DNS token,",
		"# and PSTACK_TOKEN is generated by `init` and printed once into /var/log/cloud-init-output.log.",
		"# Read it from there, store it in your password manager, then use it as the API bearer token.",
	}
	initNote := "  # No PSTACK_TOKEN ⇒ generated, stored 0600 in control/.env, and printed once (see the header)."
	tokenMessage := strings.Join([]string{
		"  The API bearer token was generated by `pstack init` and printed above in this log",
		"  (/var/log/cloud-init-output.log). Save it now — it is also in",
		"  /var/lib/pstack/control/.env, mode 0600.",
	}, "\n")
	// The config passphrase is embedded in BOTH --config and --config-url, so it counts here exactly
	// as the admin pair and PSTACK_TOKEN do: the header's benign three lines would be a lie beside it.
	if len(initEnv) > 0 || a.ConfigKey != "" {
		// A provider stores user-data as instance metadata: readable by every process on the box and
		// by anyone with the provider's API. Saying so once at the top of the file is the only
		// protection this generator can offer for what it just wrote into it.
		secrets = []string{
			"# THIS FILE CARRIES CREDENTIALS. A provider keeps user-data as instance metadata: every process",
			"# on the box can read it, and so can anyone holding the provider's own API credentials. Treat",
			"# all of it as already disclosed to this host, and rotate it if the file gets out:",
			"#",
			"#   - the Traefik dashboard password — hashed into an htpasswd on the host, plain text here.",
		}
		// Only when there IS one: with a config but no admin and no token, the init call carries no
		// env prefix and a line describing one would be prose about something that is not in the file.
		var note []string
		if len(initEnv) > 0 {
			note = append(note, "  # The env prefix on the init call below carries credentials IN THE CLEAR (see the header).")
		}
		if a.AdminUser != "" {
			secrets = append(secrets,
				`#   - the first pstack account ("`+a.AdminUser+`") and its password. It is spent on first boot —`,
				"#     the account is created only while there is none — so change it once you have signed in.")
			note = append(note, "  #   PSTACK_ADMIN_*  the first admin account, created on first boot and inert after.")
		}
		if a.DNSToken != "" {
			secrets = append(secrets,
				"#   - the DNS-01 credential (PSTACK_DNS_TOKEN). It can create and delete DNS records in this",
				"#     zone, which is how Traefik proves the domain — rotate it at your DNS provider if this",
				"#     file gets out. It cannot touch this host.")
			note = append(note, "  #   PSTACK_DNS_TOKEN  the DNS-01 credential; without it dns.env is empty and no certificate ever arrives.")
		}
		if a.Token != "" {
			secrets = append(secrets,
				"#   - PSTACK_TOKEN — the bearer token of an API that holds a read-write Docker socket, so it",
				"#     is root on this host. Rotate it with `PSTACK_TOKEN=<new> pstack init …` over SSH.")
			note = append(note, "  #   PSTACK_TOKEN    used as given; `init` generates nothing and prints nothing.")
			tokenMessage = strings.Join([]string{
				"  The API bearer token is the one this file set, stored in",
				"  /var/lib/pstack/control/.env, mode 0600. It was NOT printed above — whoever",
				"  rendered this file already has it.",
			}, "\n")
		} else {
			secrets = append(secrets,
				"#   - not the API bearer token: `init` generates one on the host and prints it once into",
				"#     /var/log/cloud-init-output.log. Read it from there and store it.")
			note = append(note, "  # No PSTACK_TOKEN ⇒ generated, stored 0600 in control/.env, and printed once (see the header).")
		}
		initNote = strings.Join(note, "\n")
		// The boot log is where an operator looks when the console says "up after N seconds", so the
		// account they can actually sign in with belongs there — the name, never the password: this
		// message is printed to the console and kept in the log, and the password has already been
		// handed to whoever rendered the file.
		if a.AdminUser != "" {
			tokenMessage = "  Sign in at https://control." + a.Domain + " as `" + a.AdminUser + "` with the password shown\n" +
				"  when this file was rendered. It is also in /var/lib/pstack/control/.env (0600).\n\n" + tokenMessage
		}
		// The two modes differ in ONE fact and it is the only one worth the space: whether the sealed
		// export is in this file too. Saying "the config is encrypted" and stopping there would be
		// true and useless, because with --config the key that opens it is three lines further down.
		if len(a.ConfigSealed) > 0 {
			secrets = append(secrets,
				"#   - PSTACK_CONFIG_KEY *and the sealed export itself* (write_files, below). Both are in this",
				"#     file, so the seal protects that export everywhere EXCEPT here: whoever reads this",
				"#     metadata can open it. It holds every credential of the host it came from — account",
				"#     password hashes, API tokens, host secrets, notifier URLs and registry logins.",
				"#     Use --config-url instead to keep the payload off instance metadata.")
		}
		if a.ConfigURL != "" {
			secrets = append(secrets,
				"#   - PSTACK_CONFIG_KEY, but NOT the export it opens: the host fetches that at boot from",
				"#     "+a.ConfigURL+".",
				"#     Opening it needs this file AND access to that URL, which is the combination worth",
				"#     having. Serve it from somewhere only this host can reach, and take it down after.")
		}
		if a.ConfigKey != "" {
			secrets = append(secrets,
				"#     The host DELETES /var/lib/pstack/config.sealed as soon as it has applied it.")
			// What that deletion is worth differs by mode, and claiming it "so the export is not left
			// at rest on the box" was only true for --config-url. cloud-init keeps this very file at
			// /var/lib/cloud/instance/user-data.txt for the life of the instance, so with --config the
			// payload AND the key are still on disk after the rm — root-only, but present. Saying
			// otherwise would tell an operator a copy is gone when it is not.
			if len(a.ConfigSealed) > 0 {
				secrets = append(secrets,
					"#     That does NOT clear this mode, though: cloud-init keeps the user-data it booted from",
					"#     at /var/lib/cloud/instance/user-data.txt for the life of the instance, and the sealed",
					"#     export and its key are both in it. Root-only, but not gone. --config-url is the mode",
					"#     where the deletion means what it sounds like.")
			}
		}
	}

	extraPackages := ""
	if len(profile.extraPackages) > 0 {
		var lines []string
		for _, p := range profile.extraPackages {
			lines = append(lines, "  - "+p)
		}
		extraPackages = "\n" + strings.Join(lines, "\n")
	}

	// ── the portable config ─────────────────────────────────────────────────────────────────────
	// write_files runs before runcmd and cloud-init creates the parent directory, so the export is on
	// disk 0600 before anything else starts. `encoding: b64` rather than a literal block because the
	// payload is JSON full of braces and quotes: base64 is the one form in which nothing about its
	// bytes can reach the YAML around it, and the alphabet contains no `{`, which makes it the one
	// value in this file that cannot be rewritten by the substitution that places it.
	//
	// The sentence introducing the section is a marker too, and its default is the one that was
	// there before this existed, byte for byte: a file with no config in it must render EXACTLY as
	// the checked-in transcripts do, and "No `write_files:` here any more" would otherwise be a lie
	// sitting directly under a `write_files:` block.
	configFile, writeFilesNote := "", "No `write_files:` here any more."
	if len(a.ConfigSealed) > 0 {
		writeFilesNote = "The `write_files:` above is the sealed config and nothing else."
		configFile = "\n\n" + strings.Join([]string{
			"# THE SEALED CONFIG, embedded. The last runcmd step applies it and then deletes it from the",
			"# host; the SECRETS header above says what it holds and who can open it.",
			"write_files:",
			"  - path: /var/lib/pstack/config.sealed",
			"    owner: root:root",
			"    permissions: '0600'",
			"    encoding: b64",
			"    content: " + base64.StdEncoding.EncodeToString(a.ConfigSealed),
		}, "\n")
	}
	// The apply step. A YAML LITERAL block, not the folded scalar the init call uses: this needs real
	// lines, and it needs the passphrase to be a shell variable rather than a command argument —
	// /proc/<pid>/cmdline is world-readable, so an env prefix on the command would publish the key to
	// every process on the box, while a variable inside the script does not.
	configStep := ""
	if a.ConfigKey != "" {
		// With --config-url the fetch is the first link in the same `&&` chain: a URL that 404s or
		// returns an error page would otherwise be written to disk and then fail to unseal, which
		// reports the wrong problem.
		fetch := ""
		if a.ConfigURL != "" {
			fetch = `curl -fsSL --retry 3 --retry-delay 2 -o "$f" '` + a.ConfigURL + `' && `
		}
		configStep = "\n" + strings.Join([]string{
			"",
			"  # ── 8. Apply the exported config — LAST, because it needs the API answering ────────────────",
			"  # A `pstack pull config` export from another host: accounts, API tokens, host vars and",
			"  # secrets, notifiers, SSO, registry logins, routing files and named specs. `push config`",
			"  # CREATES what is missing and never overwrites, so re-running it changes nothing already here.",
			"  #",
			"  # It runs INSIDE the control container, against http://127.0.0.1:7878, and NOT against",
			"  # https://api." + a.Domain + ": this early in a first boot the DNS records may not have",
			"  # propagated and Traefik may hold no certificate yet, and a config that silently failed to",
			"  # apply is the one outcome this step must not have.",
			"  #",
			"  # It does NOT dial the container's own address from the host, which is what the first",
			"  # version did. Under `--orchestrator swarm` — the default — preview-ingress and",
			"  # preview-shared are ATTACHABLE OVERLAY networks, so the only addresses `docker inspect`",
			"  # can report are overlay ones (10.0.x.x) that the host has no route to. That is not a",
			"  # race the step can wait out: it fails every time, and it fails as an i/o TIMEOUT rather",
			"  # than a refused connection, which is exactly what an unroutable address looks like.",
			"  #",
			"  # PSTACK_TOKEN is not read here at all: the container already holds it in its own",
			"  # environment, so `docker exec` inherits it and the token never enters a host shell.",
			"  #",
			"  # The sealed file is deleted from BOTH sides whether the apply worked or not: a sealed",
			"  # export left anywhere is a credential at rest that nothing else would ever clean up.",
			"  - |",
			"    umask 077",
			"    f=/var/lib/pstack/config.sealed",
			"    PSTACK_CONFIG_KEY='" + a.ConfigKey + "'",
			"    export PSTACK_CONFIG_KEY",
			`    cid="$(docker compose -p pstack-control ps -q pstack 2>/dev/null)" || cid=`,
			"    applied=no",
			`    if ` + fetch + `[ -n "$cid" ] && docker cp "$f" "$cid:/tmp/config.sealed"; then`,
			"      # `init` already blocked on the container being healthy, so one attempt should do.",
			"      # The retries are for the gap between healthy and serving, and they are bounded: a",
			"      # config that cannot apply must fail the boot loudly, not hang it.",
			"      n=1",
			"      while [ \"$n\" -le 5 ]; do",
			`        if docker exec -e PSTACK_CONFIG_KEY -e PSTACK_API_URL=http://127.0.0.1:7878 "$cid" \`,
			"           pstack push config -i /tmp/config.sealed -y; then applied=yes; break; fi",
			"        echo \"pstack: config apply attempt $n failed; retrying in 5s\" >&2",
			"        n=$((n+1)); sleep 5",
			"      done",
			`      docker exec "$cid" rm -f /tmp/config.sealed >/dev/null 2>&1 || true`,
			"    fi",
			`    rm -f "$f"`,
			`    [ "$applied" = yes ] || echo 'pstack: THE SEALED CONFIG WAS NOT APPLIED — /var/lib/pstack/config.sealed has been deleted. Re-run ` + "`pstack push config`" + ` by hand.'`,
		}, "\n")
	}

	values := []kv{
		{"DOMAIN", a.Domain},
		{"ACME_EMAIL", a.AcmeEmail},
		{"SSH_BLOCK", sshBlock},
		{"DASHBOARD_PASSWORD", a.DashboardPassword},
		{"SECRETS_LIST", strings.Join(secrets, "\n")},
		{"INIT_ENV_NOTE", initNote},
		{"INIT_ENV", initEnvPrefix},
		{"TLS_SECTION", tlsSection},
		{"TOKEN_MESSAGE", tokenMessage},
		{"INIT_EXTRA_FLAGS", initExtra},
		{"UI_IMAGE_STEP", uiImageStep},
		{"CONFIG_REPO", a.ConfigRepo},
		{"IMAGE_HINT", profile.imageHint},
		{"DISTRO_NOTE", distroNote},
		{"EXTRA_PACKAGES", extraPackages},
		{"PKG_SETUP", profile.pkgSetup},
		{"DOCKER_ENABLE", profile.dockerEnable},
		{"WRITE_FILES_NOTE", writeFilesNote},
		{"CONFIG_FILE", configFile},
		{"CONFIG_STEP", configStep},
		// THE PINS — the point of this generator knowing versions at all. Stamped from the running
		// CLI (same pattern as the generated Dockerfiles in image), so the file reproduces the
		// toolchain that rendered it rather than whatever is latest on the day someone reuses it.
		{"PSTACK_VERSION", version.Get()},
	}

	out := pstack.CloudInitTemplate
	for _, e := range values {
		out = strings.ReplaceAll(out, "{{"+e.k+"}}", e.v)
	}

	// No config repo: drop the clone line AND the comment that introduces it. Emitting
	// `git clone  /opt/preview/config` would fail and abort the rest of cloud-init; leaving the
	// comment behind would describe a step that is not there.
	if a.ConfigRepo == "" {
		lines := strings.Split(out, "\n")
		idx := -1
		for i, l := range lines {
			if strings.Contains(l, "git clone --depth 1  /opt/preview/config") {
				idx = i
				break
			}
		}
		if idx != -1 {
			from := idx
			for from > 0 && strings.HasPrefix(strings.TrimLeft(lines[from-1], " \t\n\r\v\f"), "#") {
				from--
			}
			lines = append(lines[:from], lines[idx+1:]...)
			out = strings.Join(lines, "\n")
		}
	}

	if missed := leftoverRe.FindAllString(out, -1); missed != nil {
		// Fail rather than hand over a file that boots most of the way and then breaks.
		var uniq []string
		seen := map[string]bool{}
		for _, m := range missed {
			if !seen[m] {
				seen[m] = true
				uniq = append(uniq, m)
			}
		}
		return "", &Error{"template left unrendered placeholders: " + strings.Join(uniq, ", ")}
	}
	return out, nil
}

// WorkerAnswers are RenderWorkerCloudInit's inputs.
type WorkerAnswers struct {
	Token       string
	ManagerAddr string
	Distro      string
	SSHKey      string
}

// RenderWorkerCloudInit is the user-data for a swarm WORKER: install Docker, join the manager.
// Nothing else — no Bun, no pstack, no control stack; a worker runs tasks the manager schedules
// and is managed from the manager.
//
// Its own small template rather than a third of the manager's with conditionals: the generator has
// no conditional syntax (deliberately — see the header), and the two files share only the Docker
// install fragments, which are reused here verbatim.
//
// THE TOKEN IS A SECRET: whoever can read this file can add a node that runs any task on the cluster.
// Treat the rendered file like the manager's `.env`.
func RenderWorkerCloudInit(a WorkerAnswers) (string, error) {
	distro, profile, err := profileFor(a.Distro)
	if err != nil {
		return "", err
	}
	if !tokenRe.MatchString(a.Token) {
		return "", &Error{"that is not a swarm join token (SWMTKN-…)"}
	}
	if !addrRe.MatchString(a.ManagerAddr) {
		return "", &Error{"manager address must be host:port"}
	}
	if a.SSHKey != "" && !sshKeyRe.MatchString(a.SSHKey) {
		return "", &Error{`ssh key must be an authorized_keys line, e.g. "ssh-ed25519 AAAA… you@laptop"`}
	}
	var ports []string
	for _, p := range swarm.SwarmPorts {
		ports = append(ports, "#     "+js.PadEnd(p.Port, 14)+" "+p.Why)
	}
	sshLine := "    # No key here: the provider injects its own at boot (e.g. `hcloud server create --ssh-key`)."
	if a.SSHKey != "" {
		sshLine = "    ssh_authorized_keys:\n      - " + a.SSHKey
	}
	lines := []string{
		"#cloud-config",
		"#",
		"# pstack swarm WORKER — joins the manager at " + a.ManagerAddr + ".",
		"# Rendered by pstack " + version.Get() + " for " + distro + ". Boot a machine with it; nothing to run afterwards.",
		"#",
		"# ── FIREWALL, BEFORE YOU BOOT ────────────────────────────────────────────────────────────────",
		"# Between this machine and every other node (the manager included):",
		strings.Join(ports, "\n"),
		"#",
		"# ── THIS FILE HOLDS THE JOIN TOKEN ──────────────────────────────────────────────────────────",
		"# Whoever has it can add a node that runs any task on the cluster. Rotate it from the manager",
		"# with `docker swarm join-token --rotate worker` if this file leaks.",
		"# ─────────────────────────────────────────────────────────────────────────────────────────────",
		"",
		"groups:",
		"  - docker",
		"",
		"users:",
		"  - default",
		"  - name: preview",
		"    gecos: preview stack operator",
		"    shell: /bin/bash",
		"    groups: [docker]",
		"    sudo: 'ALL=(ALL) NOPASSWD:ALL'",
		"    lock_passwd: true",
		sshLine,
		"",
		"package_update: true",
		"packages:",
		"  - ca-certificates",
		"  - curl",
	}
	for _, p := range profile.extraPackages {
		lines = append(lines, "  - "+p)
	}
	lines = append(lines,
		"",
		"runcmd:",
		"  # ── 1. Docker ───────────────────────────────────────────────────────────────────────────",
		profile.pkgSetup,
		profile.dockerEnable,
		"  - docker --version",
		"  # ── 2. Join ─────────────────────────────────────────────────────────────────────────────",
		"  - "+swarm.JoinCommand(a.Token, a.ManagerAddr),
		"  - docker info --format 'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})'",
		"",
		"final_message: |",
		"  swarm worker is up after $UPTIME seconds — joined "+a.ManagerAddr+".",
		"  Check from the manager: docker node ls",
		"",
	)
	return strings.Join(lines, "\n"), nil
}

// Ask asks for anything not already supplied: prints `<label>[ [fallback]]:` and reads a line.
//
// EOF (`prompt` returned null in the TS) is an error, so a piped/CI invocation with missing answers
// fails loudly here instead of silently rendering an empty domain.
func Ask(in *bufio.Reader, out io.Writer, label, fallback string) (string, error) {
	suffix := ""
	if fallback != "" {
		suffix = " [" + fallback + "]"
	}
	fmt.Fprint(out, label+suffix+": ")
	answer, err := in.ReadString('\n')
	if err != nil && answer == "" {
		return "", &Error{`no answer for "` + label + `" (stdin closed). Pass it as a flag for non-interactive use.`}
	}
	v := strings.TrimSpace(answer)
	if v == "" {
		v = fallback
	}
	if v == "" {
		return "", &Error{`"` + label + `" is required`}
	}
	return v, nil
}

// AskOptional asks for something the answer may legitimately be "nothing".
//
// Separate from Ask because EOF must mean "skip it", not an error: a piped or CI invocation that
// runs out of input should still produce a file, and an OPTIONAL field is exactly the one place
// where no answer is a valid answer.
func AskOptional(in *bufio.Reader, out io.Writer, label string) string {
	fmt.Fprint(out, label+" (blank to skip): ")
	answer, _ := in.ReadString('\n')
	return strings.TrimSpace(answer)
}
