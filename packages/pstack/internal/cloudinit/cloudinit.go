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
	// ConfigRepo is an optional git URL cloned to /opt/preview/config for driving the CLI from the host.
	ConfigRepo string
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
	domainRe   = regexp.MustCompile(`(?i)^[a-z0-9.-]+\.[a-z]{2,}$`)
	emailRe    = regexp.MustCompile(`(?i)^[^@\s]+@[^@\s]+\.[a-z]{2,}$`)
	sshKeyRe   = regexp.MustCompile(`^(ssh-(rsa|ed25519)|ecdsa-sha2-\S+) \S+`)
	tokenRe    = regexp.MustCompile(`^SWMTKN-[A-Za-z0-9-]+$`)
	addrRe     = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)
	wrapRe     = regexp.MustCompile(`.{1,92}(\s|$)`)
	leftoverRe = regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
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
	if strings.Contains(a.DashboardPassword, "'") {
		// It is interpolated into a single-quoted shell command in the template.
		return &Error{"dashboard password must not contain a single quote"}
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
	extraPackages := ""
	if len(profile.extraPackages) > 0 {
		var lines []string
		for _, p := range profile.extraPackages {
			lines = append(lines, "  - "+p)
		}
		extraPackages = "\n" + strings.Join(lines, "\n")
	}

	values := []kv{
		{"DOMAIN", a.Domain},
		{"ACME_EMAIL", a.AcmeEmail},
		{"SSH_BLOCK", sshBlock},
		{"DASHBOARD_PASSWORD", a.DashboardPassword},
		{"INIT_EXTRA_FLAGS", initExtra},
		{"UI_IMAGE_STEP", uiImageStep},
		{"CONFIG_REPO", a.ConfigRepo},
		{"IMAGE_HINT", profile.imageHint},
		{"DISTRO_NOTE", distroNote},
		{"EXTRA_PACKAGES", extraPackages},
		{"PKG_SETUP", profile.pkgSetup},
		{"DOCKER_ENABLE", profile.dockerEnable},
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
