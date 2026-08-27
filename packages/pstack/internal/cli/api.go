// `pstack api …` — every HTTP route as a command, generated from `packages/pstack/api/openapi.yaml`.
//
// ── WHY THIS IS THE ONE COBRA SUBTREE IN A HAND-ROLLED CLI ───────────────────────────────────────
//
// The rest of `pstack` is parsed by the positional walker in args.go, deliberately: flags may appear
// anywhere, `--ui` peeks ahead, and `cli-goldens.test.ts` pins the usage text byte for byte. Cobra
// cannot reproduce any of that, so it never owns the root. It owns exactly the `api` subtree, which
// has none of those constraints because nobody has typed it before.
//
// The split is enforced by INTERCEPTION, not by cooperation: `run` hands the whole tail to this
// function before `ParseArgs` sees a single token. Otherwise `pstack api deployments up --id pr-1`
// would be parsed as the verb `api` with three unknown arguments, and the walker would refuse it
// with a message about a spec file.
//
// ── THE COMMANDS ARE NOT WRITTEN HERE, AND THAT IS THE POINT ─────────────────────────────────────
//
// `zz_generated.go` is produced by oascmd-gen from the OpenAPI document and carries a lock file, so
// a route that changes shape either regenerates cleanly or is reported as a BREAKING change to a
// command somebody may have scripted. Hand-writing sixty-nine subcommands would reproduce every
// path, method, parameter and enum a second time and let them drift — which is the same argument
// docs/README.md makes about route tables in prose.
//
// ── ADDRESSING AND CREDENTIALS ARE THE ONES THAT ALREADY EXISTED ─────────────────────────────────
//
// `PSTACK_API_URL` and `PSTACK_TOKEN`, resolved by the same `apiBase` that `pull config` and
// `push config` use — including its refusals: no default URL (a guess talks to the wrong host) and
// no plain http off a private address. Viper would be a third configuration mechanism beside the
// `Tuning` env reader and that pair, for values that are already one lookup each.
//
// The token is checked HERE rather than inside apiBase's config-specific sentence, because most of
// these routes are not root-only — a personal token at the right role is the normal credential, and
// telling somebody they need the root token to list deployments would be false.
package cli

import (
	"net/http"
	"strings"
	"time"

	oascmd "github.com/samishal1998/openapi-commands"
	"github.com/spf13/cobra"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/apicli"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// apiTimeout bounds one API call. Generous next to the probe's three seconds: `up` answers 202
// immediately, but `GET /api/config` on a large host serializes every credential it holds, and
// `/api/deployments` shells out to docker per deployment.
const apiTimeout = 60 * time.Second

// isAPICommand reports whether argv is `pstack api …`, before anything parses it.
//
// Bare `pstack api` is included so it prints the subtree's help rather than falling through to the
// walker, which would answer with the top-level usage and no mention of what `api` can do.
func isAPICommand(argv []string) bool { return len(argv) > 0 && argv[0] == "api" }

// isHelpOnly reports whether this invocation only asks what the commands ARE.
//
// Reading `pstack api --help` on a laptop must not require a configured host: the addressing is
// checked before the tree is built (so a typo'd URL fails once, not per command), and without this
// the most common first thing anyone types would answer with a sentence about PSTACK_API_URL
// instead of the list they asked for. Bare `pstack api` counts — cobra prints help for it.
func isHelpOnly(tail []string) bool {
	for _, a := range tail {
		if a == "--help" || a == "-h" || a == "help" || a == "--version" || a == "-v" {
			return true
		}
	}
	return len(tail) == 0
}

// apiCmd runs the generated tree against the configured host.
func apiCmd(argv []string, io IO) *Exit {
	base, token := "https://pstack.invalid", ""
	if !isHelpOnly(argv[1:]) {
		var ex *Exit
		base, token, ex = apiBase(io.Env)
		if ex != nil {
			// apiBase's token sentence is about /api/config specifically. Every other route admits
			// a personal token at the right role, so the general case gets the general sentence.
			if strings.Contains(ex.Msg, "root-token only") {
				return fail("set PSTACK_TOKEN — either the host's own token, or a personal one from `pstack api tokens create`")
			}
			return ex
		}
	}
	// No `Version` on this root: cobra would claim `-v`, which is `--verbose` everywhere else in
	// this CLI. `pstack --version` already answers the question.
	root := &cobra.Command{
		Use:           "api",
		Short:         "Call the pstack HTTP API. Every route is a command.",
		Long:          "Every route of the pstack HTTP API, generated from its OpenAPI document.\n\nTalks to $PSTACK_API_URL as $PSTACK_TOKEN.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetArgs(argv[1:])
	root.SetIn(io.Stdin)
	root.SetOut(io.Stdout)
	root.SetErr(io.Stderr)
	tree := apicli.NewCommandTree(oascmd.ExecOptions{
		BaseURL: base,
		Client:  &http.Client{Timeout: apiTimeout},
		Headers: http.Header{"User-Agent": {"pstack/" + version.Get()}},
		Auth:    oascmd.BearerAuth(token),
	})
	for _, c := range tree {
		if d, ok := groupShort[c.Name()]; ok {
			c.Short = d
		}
	}
	root.AddCommand(tree...)
	if err := root.Execute(); err != nil {
		// Cobra's own errors (unknown command, missing required flag) and the executor's (a non-2xx
		// response) arrive the same way. Both are the caller's problem to fix, so both are
		// ExitFailed — never a panic and never exit 0, which a script would read as success.
		return &Exit{Code: ExitFailed, Msg: strings.TrimSpace(err.Error())}
	}
	return nil
}

// groupShort is the one-line description beside each group in `pstack api --help`.
//
// Here rather than in the spec's `tags[].description` because oascmd derives the group from the tag
// NAME and does not carry its description into the command — so a spec-side description would look
// authoritative and render as a blank column. `TestEveryAPIGroupIsDescribed` fails when a new tag
// arrives without a line here, which is what keeps this from being the stale copy it looks like.
var groupShort = map[string]string{
	"deployments": "Submit, deploy, tear down, sleep, share and inspect a preview stack.",
	"jobs":        "What the host is doing, or just did.",
	"specs":       "Named specs, stored on the host.",
	"routing":     "Traefik's dynamic-config files, and what it is actually serving.",
	"registries":  "Private-registry logins, used when a deploy pulls.",
	"notifiers":   "Webhooks and chat notifiers, their deliveries, and redelivery.",
	"host-vars":   "Host variables and secrets, shared by every deployment.",
	"users":       "Accounts, roles and passwords.",
	"tokens":      "Personal API tokens.",
	"settings":    "The two settings changeable at runtime, without a restart.",
	"sso":         "Single sign-on providers.",
	"swarm":       "The swarm, and what a new worker runs to join it.",
	"tls":         "The host's certificate mode, and the bring-your-own wildcard.",
	"config":      "The whole portable configuration: export it, or apply one.",
	"host":        "The host itself — health, the control stack, terminal history, the probe.",
}
