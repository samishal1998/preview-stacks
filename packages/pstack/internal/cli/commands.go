// PER-COMMAND HELP AND SHELL COMPLETION, from one table.
//
// `pstack --help` prints the whole manual — every command, so a reader who does not yet know the
// vocabulary can find it. That text is a golden and stays exactly as it was. What was missing is
// the other direction: someone who knows they want `init` and needs only its flags, and a shell
// that can finish a word.
//
// Both come from `commandHelp` below, and that is the point of the file: a command's flags are
// spelled ONCE. A completion script listing a flag the help does not describe — or the reverse — is
// the drift this repo refuses everywhere else, and it is the failure nobody notices, because a
// missing completion looks like a shell that has not been reloaded.
//
// `commands_test.go` walks `Commands` and fails when an entry is missing, so a new command cannot
// ship with no help of its own.
package cli

import (
	"fmt"
	"sort"
	"strings"
)

// commandHelp is one command's own page and the flags a shell should offer for it.
type commandHelp struct {
	// summary is one line, for the completion menu and the "see also" list.
	summary string
	// body is what `pstack <cmd> --help` prints, minus the header this file adds.
	body string
	// flags are the long flags this command accepts, WITHOUT values. Completion offers these;
	// nothing else does, so a flag missing here is invisible to the shell and present nowhere else.
	flags []string
}

// The flag sets shared by several commands, spelled once so a change reaches every command that
// takes them.
var (
	globalFlags = []string{"--help", "--version", "--dry-run", "--verbose", "--quiet"}
	specFlags   = []string{"--file", "--set"}
)

var commandHelps = map[string]commandHelp{
	"up": {
		summary: "provision every axis, then bring the compose project up",
		body: `pstack up [-f preview.yml] [--set K=V]

Runs each axis's up hook in order, then the compose project. A failure stops at that axis and
tears down what it already created, so a half-provisioned stack is not left behind.

  -f, --file <path>   the spec (default preview.yml)
      --set K=V       define or override a variable (repeatable)
  -n, --dry-run       print what would run, change nothing
  -v, --verbose       echo commands and their output`,
		flags: append(append([]string{}, specFlags...), globalFlags...),
	},
	"down": {
		summary: "tear the stack down in reverse, then prove it is gone",
		body: `pstack down [-f preview.yml] [--force] [--no-verify]

Reverses the order up used, then runs every assert_gone. Exit 2 means something SURVIVED the
teardown — that is the leak check, and it is the reason this command exists rather than
` + "`docker compose down`" + `.

      --force         allow tearing down a ` + "`kind: shared`" + ` deployment (it destroys volumes
                      every tenant depends on, so it is refused without this)
      --no-verify     skip the post-teardown leak check
  -f, --file <path>   the spec (default preview.yml)`,
		flags: append(append([]string{"--force", "--no-verify"}, specFlags...), globalFlags...),
	},
	"verify": {
		summary: "run every assert_gone and report what survived",
		body: `pstack verify [-f preview.yml]

The leak check on its own, without tearing anything down. Exit 2 when something is still there.`,
		flags: append(append([]string{}, specFlags...), globalFlags...),
	},
	"status": {
		summary: "what this spec resolves to, and what is running",
		body:    "pstack status [-f preview.yml]\n\nResolves the spec and reports each axis and the compose project's containers.",
		flags:   append(append([]string{}, specFlags...), globalFlags...),
	},
	"validate": {
		summary: "parse and check a spec without touching anything",
		body: `pstack validate [-f preview.yml] [--set K=V]

Offline by contract: it never reads docker and never writes a file. Use it in CI on every spec
change — it is the cheapest gate you have.`,
		flags: append(append([]string{}, specFlags...), globalFlags...),
	},
	"init": {
		summary: "stand up the control stack on this host",
		body: `pstack init --domain <preview.example.com> --acme-email <you@example.com> [flags]

Renders the control stack (Traefik + pstack), creates the two networks, and waits for the API's
healthcheck. Idempotent: re-running it IS the upgrade path.

      --domain <host>            the preview domain (or PSTACK_DOMAIN)
      --acme-email <addr>        Let's Encrypt contact (or PSTACK_ACME_EMAIL)
      --challenge http01|dns01   default http01 — no DNS credential needed
      --dns-provider <code>      lego provider code; dns01 only
      --dns-token-file <path>    the DNS-01 credential, or PSTACK_DNS_TOKEN. A PATH, never the
                                 token itself: argv is world-readable through ps
      --extra-domain <host>      also answer on this domain (repeatable). For standing a host up
                                 on a primary whose DNS still points at the OLD box: the primary's
                                 routers are in place and start working the moment DNS moves, and
                                 this one is usable meanwhile. It only ADDS — remove it later from
                                 the Domains panel or PUT /api/domains
      --ui basic|advanced        default basic (embedded, no extra container)
      --orchestrator swarm|compose
      --force                    proceed even though this run would change something you did
                                 not ask it to change

IT READS NOTHING BACK. Every flag you leave off takes that flag's default, host-wide — which is
why a re-run that would change a value you did not name is refused. Ask the host for its own line
rather than composing one:

      pstack upgrade -n | grep 'pstack init'

Also reads PSTACK_TOKEN: absent on an existing host, a NEW machine token is minted and every CI
job holding the old one starts getting 401s.`,
		flags: []string{
			"--domain", "--acme-email", "--challenge", "--dns-provider", "--dns-token-file",
			"--extra-domain", "--ui", "--orchestrator", "--force", "--dry-run", "--help",
		},
	},
	"upgrade": {
		summary: "move this host to another pstack version, rotating nothing",
		body: `pstack upgrade [--to <version|latest>] [-n]

Reads control/.env for the token, domain and email, rebuilds the image and re-runs init with
everything it read back — so nothing rotates. Over SSH, never from inside the control stack.

      --to <version>   default latest
  -n, --dry-run        print the plan and change nothing
      --ui basic|advanced          override what it detects
      --orchestrator swarm|compose switch — every preview must be down first`,
		flags: []string{"--to", "--ui", "--orchestrator", "--resume", "--dry-run", "--help"},
	},
	"ui": {
		summary: "switch which UI control.<domain> serves",
		body: `pstack ui <basic|advanced>

Reuses the stored token and domain, and builds the SPA image when switching to advanced. No
version change — that is upgrade.`,
		flags: globalFlags,
	},
	"serve": {
		summary: "run the API and UI (what the control container does)",
		body: `pstack serve

Reads its configuration from the environment, never flags, because the control stack sets it:

      PSTACK_TOKEN     required to bind off-loopback
      PSTACK_PORT      7878          PSTACK_HOST      127.0.0.1
      PSTACK_DATA      /var/lib/pstack
      PSTACK_DOMAIN    share links and the SSO callback
      PSTACK_MAX_JOBS  4 — lifecycle jobs running at once, across every stack

Full list: pstack --help.`,
		flags: globalFlags,
	},
	"healthcheck": {
		summary: "GET /api/health, exit 0 or 1 (the container HEALTHCHECK)",
		body:    "pstack healthcheck\n\nOne request to /api/health on PSTACK_PORT. Exit 0 healthy, 1 not. Takes no flags.",
		flags:   []string{"--help"},
	},
	"build-image": {
		summary: "build the control image from the running binary",
		body: `pstack build-image [--tag <name:tag>] [--ui]

Builds FROM THE INSTALLED BINARY — no source checkout, no registry. Retags the previous image
<tag>-previous first, which is the rollback path.

      --tag <name:tag>   default pstack:local (= PSTACK_IMAGE)
      --ui               build the advanced UI image instead
      --ui-dist <path>   use a built UI dist from a specific path`,
		flags: []string{"--tag", "--ui", "--ui-dist", "--dry-run", "--help"},
	},
	"dockerfile": {
		summary: "print the control Dockerfile instead of building it",
		body:    "pstack dockerfile [--ui]\n\nPrints what build-image would build.\n\n      --ui   print the advanced UI Dockerfile instead",
		flags:   []string{"--ui", "--help"},
	},
	"cloud-init": {
		summary: "render a cloud-config that provisions a whole host",
		body: `pstack cloud-init --domain <host> --acme-email <addr> [flags]

Renders a complete #cloud-config: Docker, the pinned pstack binary, the control image, and init.
Prompts for anything missing unless -y.

      --distro ubuntu|debian|fedora|suse|arch|alpine
      --ssh-key <line>          an extra authorized key
      --password <pw>           the Traefik dashboard password (generated if omitted)
      --admin-user <name> [--admin-password <pw>]   the first pstack account
      --api-token <token>       default: init generates one and prints it once
      --dns-token-file <path>   REQUIRED with --challenge dns01, or the host boots with an empty
                                dns.env and never gets a certificate
      --config <file> | --config-url <url>   apply a sealed config export on first boot
      --config-repo <git-url>   cloned to /opt/preview/config
  -o <file>                     write it out instead of stdout
  -y                            never prompt

WHAT IT WRITES IS INSTANCE METADATA. Every credential in the rendered file is readable by every
process on the box and by anyone with your provider's API credentials; the file says so at the top
when it carries any.`,
		flags: []string{
			"--domain", "--acme-email", "--distro", "--ssh-key", "--password", "--admin-user",
			"--admin-password", "--api-token", "--dns-token-file", "--challenge", "--dns-provider",
			"--ui", "--orchestrator", "--config", "--config-url", "--config-repo", "--help",
		},
	},
	"swarm": {
		summary: "the nodes previews run on, and how to add one",
		body: `pstack swarm [status]      the node table (exit 1 if this host is not a manager)
pstack swarm join         what a new worker runs — A SECRET, it joins any machine to the cluster

      --format command|script|cloud-config|token   default command
      --distro <name>   cloud-config only
  -o <file>             write it out instead of stdout`,
		flags: []string{"--format", "--distro", "--help"},
	},
	"pull": {
		summary: "seal every credential on a host into one portable file",
		body: `pstack pull config -o <file>

Exports accounts, tokens, host variables, notifiers, SSO providers, registry credentials, routing
files and named specs — sealed with a passphrase.

      PSTACK_API_URL     the host, e.g. https://api.preview.example.com
      PSTACK_TOKEN       the ROOT token; an admin session is refused
      PSTACK_CONFIG_KEY  the passphrase; prompted (no echo) when unset on a tty

There is no passphrase FLAG on purpose: argv is world-readable through ps.`,
		flags: []string{"--help"},
	},
	"push": {
		summary: "apply a sealed export onto this host — creates, never overwrites",
		body: `pstack push config -i <file> [-y]

Names every registry and notifier URL it is about to trust and asks first. -y applies without
asking and prints counts instead, because a log is not a terminal.

Same three environment variables as pull.`,
		flags: []string{"--help"},
	},
	"api": {
		summary: "every HTTP route as a command, generated from the OpenAPI document",
		body: `pstack api <group> <command> [flags]

Generated from packages/pstack/api/openapi.yaml, so the commands and the routes cannot disagree.

      pstack api --help              the groups
      pstack api <group> --help      the commands in one group

      PSTACK_API_URL   the host (no default)
      PSTACK_TOKEN     the host's token, or a personal one`,
		flags: []string{"--help"},
	},
}

// CommandHelp is the page for one command, or "" when it has none.
func CommandHelp(cmd string) string {
	h, ok := commandHelps[cmd]
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pstack %s — %s\n\n%s\n", cmd, h.summary, h.body)
	b.WriteString("\nEvery command: pstack --help\n")
	return b.String()
}

// completionFlags is the long flags a shell should offer after a command name.
func completionFlags(cmd string) []string {
	h, ok := commandHelps[cmd]
	if !ok {
		return globalFlags
	}
	seen := map[string]bool{}
	out := []string{}
	for _, f := range h.flags {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
