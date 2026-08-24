// Package cli is argv, dispatch and exit codes. Logic belongs in the modules, not here.
//
// The parser is hand-rolled and positional: flags may appear ANYWHERE, including after the command
// word, `--ui` peeks ahead (a switch for build-image, a value for init), and unrecognised positional
// words accumulate into `cmd` and `sub`. Go's `flag` stops at the first non-flag argument and cobra
// would own the usage text — both would change observable behaviour that the CLI goldens pin.
package cli

import (
	"fmt"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
)

// Exit codes. The distinct code for leaks lets CI treat "torn down but leaked" differently from
// "teardown errored", which are different problems with different owners.
const (
	ExitOK     = 0
	ExitFailed = 1
	ExitLeaked = 2
	ExitUsage  = 3
)

// Exit is how a command ends: a code and an optional stderr line (`pstack: <msg>`).
type Exit struct {
	Code int
	// Msg is printed to stderr prefixed with `pstack: ` when non-empty.
	Msg string
}

func (e *Exit) Error() string { return e.Msg }

// fail is the `pstack: <msg>` + exit 3 every usage error takes.
func fail(msg string) *Exit { return &Exit{Code: ExitUsage, Msg: msg} }

// Parsed is argv, resolved against the environment defaults.
type Parsed struct {
	Cmd          string
	Sub          string
	File         string
	DryRun       bool
	Level        exec.Level
	Overrides    map[string]string
	OverrideKeys []string
	NoVerify     bool
	Force        bool
	Domain       string
	AcmeEmail    string
	DNSProvider  string
	Challenge    string // http01 | dns01
	Orchestrator string // swarm | compose
	Distro       string
	Format       string
	Tag          string
	UI           string // basic | advanced
	UIImage      bool
	UIDist       string
	SSHKey       string
	Password     string
	// AdminUser / AdminPassword / APIToken are cloud-init's credential flags. Deliberately WITHOUT
	// environment defaults, unlike --password (PSTACK_DASHBOARD_PASSWORD) beside them: an operator's
	// shell has PSTACK_TOKEN set precisely because it is talking to a host that already exists, and a
	// default would silently clone that host's bearer token into a new host's user-data. The pair
	// follows the token because they are the same decision — the credentials of one specific host.
	AdminUser     string
	AdminPassword string
	APIToken      string
	ConfigRepo    string
	Out           string
	Yes           bool
	To            string
	Resume        bool
	// Typed records which value flags were spelled on the command line, for the two commands that
	// must only act on an EXPLICIT --ui / --orchestrator (upgrade, ui).
	Typed map[string]bool
	// Help and Version short-circuit dispatch.
	Help    bool
	Version bool
}

// ParseArgs walks argv. `env` is os.LookupEnv-shaped so a test can pin the defaults.
func ParseArgs(argv []string, env func(string) (string, bool)) (*Parsed, *Exit) {
	get := func(k, def string) string {
		if v, ok := env(k); ok {
			return v
		}
		return def
	}
	or := func(k, def string) string { // the `||` sites: empty counts as unset
		if v, ok := env(k); ok && v != "" {
			return v
		}
		return def
	}
	p := &Parsed{
		File:        "preview.yml",
		Level:       exec.Normal,
		Overrides:   map[string]string{},
		Typed:       map[string]bool{},
		Domain:      get("PSTACK_DOMAIN", ""),
		AcmeEmail:   get("PSTACK_ACME_EMAIL", ""),
		DNSProvider: get("PSTACK_DNS_PROVIDER", ""),
		// HTTP-01 by default: it needs no DNS credential, so `init` works with nothing but a domain
		// pointed at the box.
		Challenge: or("PSTACK_CHALLENGE", "http01"),
		// Swarm by default for a NEW host. `upgrade` reads back what the host runs and ignores this.
		Orchestrator: or("PSTACK_ORCHESTRATOR", "swarm"),
		Format:       "command",
		Tag:          get("PSTACK_IMAGE", "pstack:local"),
		UI:           or("PSTACK_UI", "basic"),
		SSHKey:       get("PSTACK_SSH_KEY", ""),
		Password:     get("PSTACK_DASHBOARD_PASSWORD", ""),
		Distro:       get("PSTACK_DISTRO", "ubuntu"),
		To:           "latest",
	}
	var rest []string
	next := func(i *int, cur string) string {
		if *i+1 < len(argv) {
			*i++
			return argv[*i]
		}
		return cur
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "-f", "--file":
			p.File = next(&i, p.File)
		case "--dry-run", "-n":
			p.DryRun = true
		case "-v", "--verbose":
			p.Level = exec.Verbose
		case "-q", "--quiet":
			p.Level = exec.Quiet
		case "--no-verify":
			p.NoVerify = true
		case "--force":
			p.Force = true
		case "--domain":
			p.Domain = next(&i, p.Domain)
		case "--acme-email":
			p.AcmeEmail = next(&i, p.AcmeEmail)
		case "--dns-provider":
			p.DNSProvider = next(&i, p.DNSProvider)
		case "--tag":
			p.Tag = next(&i, p.Tag)
		case "--ui-dist":
			p.UIDist = next(&i, p.UIDist)
		case "--ssh-key":
			p.SSHKey = next(&i, p.SSHKey)
		case "--password":
			p.Password = next(&i, p.Password)
		case "--admin-user":
			p.AdminUser = next(&i, p.AdminUser)
		case "--admin-password":
			p.AdminPassword = next(&i, p.AdminPassword)
		case "--api-token":
			p.APIToken = next(&i, p.APIToken)
		case "--config-repo":
			p.ConfigRepo = next(&i, p.ConfigRepo)
		case "-o", "--out":
			p.Out = next(&i, p.Out)
		case "-y", "--yes":
			p.Yes = true
		case "--to":
			p.To = next(&i, p.To)
		case "--resume":
			p.Resume = true
		case "--ui":
			// `build-image --ui` is a switch (build the SPA image); `init --ui <mode>` takes a value.
			// Peek rather than always consuming, so neither form has to be spelled differently.
			p.Typed["--ui"] = true
			if i+1 < len(argv) {
				n := argv[i+1]
				if n == "basic" || n == "advanced" {
					p.UI = n
					i++
					continue
				}
				if !strings.HasPrefix(n, "-") {
					return nil, fail(fmt.Sprintf(`--ui must be basic or advanced, got "%s"`, n))
				}
			}
			p.UIImage = true
		case "--challenge":
			c := next(&i, "")
			if c != "http01" && c != "dns01" {
				return nil, fail(fmt.Sprintf(`--challenge must be http01 or dns01, got "%s"`, c))
			}
			p.Challenge = c
		case "--distro":
			p.Distro = next(&i, "")
		case "--format":
			// Validated by JoinMaterial against its own list, so the CLI and the API cannot disagree.
			p.Format = next(&i, p.Format)
		case "--orchestrator":
			p.Typed["--orchestrator"] = true
			o := next(&i, "")
			if o != "swarm" && o != "compose" {
				return nil, fail(fmt.Sprintf(`--orchestrator must be swarm or compose, got "%s"`, o))
			}
			p.Orchestrator = o
		case "--set":
			kv := next(&i, "")
			eq := strings.IndexByte(kv, '=')
			if eq < 1 {
				return nil, fail(fmt.Sprintf(`--set expects KEY=VALUE, got "%s"`, kv))
			}
			k := kv[:eq]
			if _, seen := p.Overrides[k]; !seen {
				p.OverrideKeys = append(p.OverrideKeys, k)
			}
			p.Overrides[k] = kv[eq+1:]
		case "-h", "--help":
			p.Help = true
			return p, nil
		case "-V", "--version":
			p.Version = true
			return p, nil
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fail("unknown flag " + a)
			}
			rest = append(rest, a)
		}
	}
	if len(rest) > 0 {
		p.Cmd = rest[0]
	}
	if len(rest) > 1 {
		p.Sub = rest[1]
	}
	return p, nil
}

// SpecCommands read a spec; Commands is every command that exists. An unknown command must fail as
// unknown, never by hunting for a spec file — that bug shipped once.
var SpecCommands = []string{"up", "down", "verify", "status", "validate"}

// Commands in usage order.
var Commands = append(append([]string{}, SpecCommands...), "init", "serve", "build-image", "cloud-init", "dockerfile", "upgrade", "ui", "swarm", "healthcheck")

// IsCommand reports whether name is a known command.
func IsCommand(name string) bool {
	for _, c := range Commands {
		if c == name {
			return true
		}
	}
	return false
}

// IsSpecCommand reports whether name reads a spec.
func IsSpecCommand(name string) bool {
	for _, c := range SpecCommands {
		if c == name {
			return true
		}
	}
	return false
}

// Usage is the --help text, byte for byte.
func Usage(version string) string {
	return strings.Join([]string{
		"pstack " + version + " — declarative lifecycle for ephemeral preview stacks",
		"",
		"Usage: pstack <up|down|verify|status|validate|cloud-init|dockerfile|build-image|init|upgrade|ui|swarm|serve> [flags]",
		"",
		"Flags:",
		"  -f, --file <path>   spec file (default: preview.yml)",
		"  -n, --dry-run       print what would run, change nothing",
		"  -v, --verbose       echo commands and their output",
		"  -q, --quiet         suppress per-step chatter",
		"  -V, --version       print the version and exit",
		"      --set K=V       override/define a variable (repeatable)",
		"      --no-verify     down: skip the post-teardown leak check",
		"      --force         down: allow tearing down a `kind: shared` deployment",
		"",
		"",
		"dockerfile:  --ui                           print the advanced UI Dockerfile instead",
		"",
		"build-image: --tag <name:tag>               (default pstack:local, = PSTACK_IMAGE)",
		"             --ui                           build the advanced UI image instead",
		"             --ui-dist <path>               use a built UI dist from a specific path",
		"",
		"init flags: --domain <preview.example.com>  --acme-email <you@example.com>",
		"            --challenge http01|dns01        (default http01 — no DNS credential needed)",
		"            --ui basic|advanced             (default basic — embedded, no extra container)",
		"            --dns-provider <lego-code>      (dns01 only; token via PSTACK_DNS_TOKEN)",
		"            --orchestrator swarm|compose    (default swarm — one manager; workers join from the Swarm page)",
		"",
		"cloud-init: --domain --acme-email [--distro ubuntu|debian|fedora|suse|arch|alpine] [--ssh-key] [--password] [--challenge] [--ui] [--orchestrator]",
		"            [--config-repo <git-url>]  [-o file]  [-y]   (-y = never prompt)",
		"            --admin-user <name> [--admin-password <pw>]  the first UI account, created on first boot",
		"            --api-token <PSTACK_TOKEN>   default: `init` generates one on the host and prints it once",
		"            Both land in the rendered file, which the provider stores as instance metadata.",
		"",
		"upgrade:    --to <version|latest>  (default latest)   [-n to print the plan and change nothing]",
		"            [--ui basic|advanced to override what it detects]  [--orchestrator swarm|compose to switch — previews must be down]",
		"            Reads control/.env for the token, domain and email so NOTHING rotates, rebuilds",
		"            the image and re-runs init. Over SSH — never from inside the control stack.",
		"",
		"ui:         pstack ui <basic|advanced>   switch which UI control.<domain> serves.",
		"            Reuses the stored token and domain; builds the SPA image when switching to",
		"            advanced. No version change — that is `upgrade`.",
		"",
		"swarm:      pstack swarm [status]            the nodes previews run on (exit 1 if this is not a manager)",
		"            pstack swarm join                what a new worker runs — a SECRET",
		"              --format command|script|cloud-config|token   (default command)",
		"              --distro ubuntu|debian|fedora|suse|arch|alpine   (cloud-config only)",
		"              -o <file>                  write it to a file instead of stdout",
		"",
		"serve env:  PSTACK_TOKEN (required to bind off-loopback) · PSTACK_PORT (7878)",
		"            PSTACK_HOST (127.0.0.1) · PSTACK_DATA (/var/lib/pstack)",
		"            PSTACK_ORCHESTRATOR (compose) · PSTACK_DOMAIN · PSTACK_TRAEFIK_METRICS (for sleep.idle)",
		"            PSTACK_READINESS_POLL_MS · PSTACK_READINESS_TIMEOUT_MS · PSTACK_SSO_STATE_TTL_S · PSTACK_SSO_DISCOVERY_TTL_S",
		"",
		"healthcheck: GET /api/health on PSTACK_PORT, exit 0 or 1 — the container HEALTHCHECK.",
		"",
		"Exit: 0 ok · 1 failed · 2 leaked · 3 bad spec/usage",
	}, "\n") + "\n"
}

// UnknownCommand is the refusal for a command this build does not have.
func UnknownCommand(cmd, version string) *Exit {
	return fail(fmt.Sprintf("unknown command \"%s\". This build is pstack %s — if you expected a newer command, the host is on an older version than you think (`pstack --version`).\n\nCommands: %s", cmd, version, strings.Join(Commands, ", ")))
}
