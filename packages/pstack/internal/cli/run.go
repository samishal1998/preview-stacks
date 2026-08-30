package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/cloudinit"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/config"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/image"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// IO is the process's streams and environment, so a test can drive Run without a process.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    func(string) (string, bool)
	// Environ is the whole environment as a map (what hooks inherit); nil means os.Environ().
	Environ map[string]string
	// UIHTML and ShareHTML are the embedded pages `serve` hosts.
	UIHTML    string
	ShareHTML string
	// OpenAPISpec is the embedded OpenAPI document `serve` publishes.
	OpenAPISpec []byte
}

// Run is the CLI: argv in, exit code out. Every `process.exit` in the reference is a return here.
func Run(argv []string, io IO) int {
	ex := run(argv, io)
	if ex == nil {
		return ExitOK
	}
	if ex.Msg != "" {
		fmt.Fprintln(io.Stderr, "pstack: "+ex.Msg)
	}
	return ex.Code
}

func run(argv []string, io IO) *Exit {
	if io.Env == nil {
		io.Env = os.LookupEnv
	}
	if io.Environ == nil {
		io.Environ = exec.Environ()
	}
	if io.Stdin == nil {
		io.Stdin = os.Stdin
	}
	// BEFORE ParseArgs, deliberately: the `api` subtree is cobra's, and the walker would refuse
	// `pstack api deployments up --id pr-1` as a verb with three unknown arguments. See api.go.
	if isAPICommand(argv) {
		return apiCmd(argv, io)
	}
	out, errOut := io.Stdout, io.Stderr
	args, ex := ParseArgs(argv, io.Env)
	if ex != nil {
		return ex
	}
	if args.Version {
		// Bare, so it is scriptable: `[ "$(pstack --version)" = 0.25.1 ] || pstack upgrade`.
		fmt.Fprintln(out, version.Get())
		return nil
	}
	if args.Help {
		fmt.Fprint(out, Usage(version.Get()))
		return nil
	}
	if args.Cmd == "" {
		fmt.Fprint(out, Usage(version.Get()))
		return &Exit{Code: ExitUsage}
	}
	if !IsCommand(args.Cmd) {
		return UnknownCommand(args.Cmd, version.Get())
	}

	var st *spec.Stack
	if IsSpecCommand(args.Cmd) {
		var err error
		st, err = spec.Load(args.File, exec.Merge(io.Environ, args.Overrides), nil)
		if err != nil {
			if spec.IsSpecError(err) {
				return fail(err.Error())
			}
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
	}
	baseEnv := io.Environ
	if st != nil {
		baseEnv = exec.Merge(io.Environ, st.Env)
	}
	runner := exec.New(exec.Options{DryRun: args.DryRun, Level: args.Level, BaseEnv: baseEnv, Out: out})
	if args.Level != exec.Quiet && st != nil {
		suffix := ""
		if args.DryRun {
			suffix = "  (dry-run)"
		}
		fmt.Fprintln(out, "stack: "+st.Stack+suffix)
	}

	switch args.Cmd {
	case "validate":
		return validate(args, st, out)

	case "up":
		r := stack.Up(st, runner, log.Writer{Out: out, Err: errOut})
		fmt.Fprintln(out, stack.Report(r))
		if r.OK {
			return nil
		}
		return &Exit{Code: ExitFailed}

	case "down":
		r := stack.Down(st, runner, stack.DownOptions{NoVerify: args.NoVerify, Force: args.Force}, log.Writer{Out: out, Err: errOut})
		fmt.Fprintln(out, stack.Report(r))
		// A leak is reported distinctly from a teardown error: "down ran but something survived" is
		// the interesting signal.
		switch {
		case r.Leaked():
			return &Exit{Code: ExitLeaked}
		case r.OK:
			return nil
		}
		return &Exit{Code: ExitFailed}

	case "verify":
		r := stack.Verify(st, runner, log.Writer{Out: out, Err: errOut})
		fmt.Fprintln(out, stack.Report(r))
		if r.OK {
			return nil
		}
		return &Exit{Code: ExitLeaked}

	case "status":
		fmt.Fprintln(out, stack.Status(st, runner))
		return nil

	case "dockerfile":
		// For anyone who wants to build, tag, push or edit the image themselves.
		if args.UIImage {
			fmt.Fprintln(out, image.UIDockerfile(""))
		} else {
			fmt.Fprintln(out, image.ControlDockerfile(""))
		}
		return nil

	case "cloud-init":
		return cloudInit(args, io)

	case "build-image":
		// Default the tag to whichever image is being built, so the two never collide.
		tag := args.Tag
		if args.UIImage && tag == image.DefaultImageTag {
			tag = image.DefaultUIImageTag
		}
		// PSTACK_BINARY: copy a local binary into the image instead of installing this version from
		// its release — for a version that is not published yet, or a host with no network at build time.
		binary, _ := io.Env("PSTACK_BINARY")
		if err := image.Build(image.BuildOptions{Tag: tag, Runner: runner, DryRun: args.DryRun, UI: args.UIImage, UIDist: args.UIDist, Binary: binary, Out: out}); err != nil {
			return fail(err.Error())
		}
		return nil

	case "init":
		if args.Domain == "" {
			return fail("init needs --domain (or PSTACK_DOMAIN), e.g. preview.example.com")
		}
		if args.AcmeEmail == "" {
			return fail("init needs --acme-email (or PSTACK_ACME_EMAIL)")
		}
		// Only dns01 needs a provider; http01 answers on port 80 with no credential at all.
		if args.Challenge == "dns01" && args.DNSProvider == "" {
			return fail("--challenge dns01 needs --dns-provider (or PSTACK_DNS_PROVIDER), e.g. cloudflare")
		}
		// The DNS-01 credential: `--dns-token-file <path>` or PSTACK_DNS_TOKEN. Both are accepted,
		// neither puts the secret in argv — see the flag's comment in args.go.
		token, hasDNSToken := io.Env("PSTACK_DNS_TOKEN")
		if args.DNSTokenFile != "" {
			b, err := os.ReadFile(args.DNSTokenFile)
			if err != nil {
				return fail("--dns-token-file: " + err.Error())
			}
			// Trailing newline is what every editor and `echo >` leaves; a credential with one is
			// rejected by the provider as a wrong token, which reads as a permissions problem.
			token, hasDNSToken = strings.TrimSpace(string(b)), true
		}
		// A host that already exists is re-rendered from these arguments alone, so anything this run
		// would change WITHOUT being asked to is refused — see initguard.go. A first init reads no
		// state and passes straight through, and so does `pstack upgrade`, which supplies every
		// value it read back.
		dataDir := registry.DataDir()
		if !args.Force {
			if state, err := upgrade.ReadControlState(dataDir); err == nil {
				_, hasToken := io.Env("PSTACK_TOKEN")
				if msg := initRevertRefusal(initReverts(state, dataDir, args, hasToken, hasDNSToken)); msg != "" {
					return fail(msg)
				}
			}
		}
		err := initctl.Init(initctl.Options{
			DataDir: dataDir, Domain: args.Domain, AcmeEmail: args.AcmeEmail, DNSProvider: args.DNSProvider,
			Challenge: initctl.Challenge(args.Challenge), UI: initctl.UI(args.UI), Orchestrator: spec.Orchestrator(args.Orchestrator),
			Token: token, DryRun: args.DryRun, Runner: runner, Out: out,
		})
		if err != nil {
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		return nil

	case "ui":
		// Switching UI mode used to mean re-typing init's whole command line — including the token.
		mode := args.Sub
		if mode == "" && args.Typed["--ui"] {
			mode = args.UI
		}
		if mode != "basic" && mode != "advanced" {
			return fail("usage: pstack ui <basic|advanced>   (basic = embedded in the API, advanced = the SPA container)")
		}
		changed, _, err := upgrade.SwitchUI(upgrade.SwitchUIOptions{DataDir: registry.DataDir(), UI: initctl.UI(mode), Runner: runner, Log: func(l string) { fmt.Fprintln(out, l) }})
		if err != nil {
			if isUpgradeError(err) {
				return fail(err.Error())
			}
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		if changed && !args.DryRun {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Now serving the "+mode+" UI on control.<domain>. The token and domain are unchanged.")
		}
		return nil

	case "swarm":
		return swarmCmd(args, runner, io)

	case "pull":
		return pullConfig(args, io)

	case "push":
		return pushConfig(args, io)

	case "upgrade":
		opts := upgrade.Options{DataDir: registry.DataDir(), Target: args.To, Phase: upgrade.Install, Runner: runner, Log: func(l string) { fmt.Fprintln(out, l) }}
		if args.Resume {
			opts.Phase = upgrade.Resume
		}
		// Only when typed: `ui` defaults to basic in the parser, and passing that unconditionally
		// would override the detection with a default.
		if args.Typed["--ui"] {
			opts.UI = initctl.UI(args.UI)
		}
		if args.Typed["--orchestrator"] {
			opts.Orchestrator = spec.Orchestrator(args.Orchestrator)
		}
		if _, err := upgrade.Upgrade(opts); err != nil {
			if isUpgradeError(err) {
				return fail(err.Error())
			}
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		// Never claim an upgrade a dry run did not perform.
		if args.DryRun {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Dry run — nothing was installed, built or recreated.")
		} else if !args.Resume {
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "  Upgraded. The control stack was recreated with the same token and domain.")
			fmt.Fprintln(out, "  Check it: curl -sf https://<domain>/api/health   (or `docker compose -p pstack-control ps`)")
		}
		return nil

	case "healthcheck":
		return Healthcheck(io.Env)

	case "serve":
		return Serve(ServeOptions{UIHTML: io.UIHTML, ShareHTML: io.ShareHTML, OpenAPISpec: io.OpenAPISpec, Stdout: out, Stderr: errOut, Env: io.Env})
	}
	return fail(`unknown command "` + args.Cmd + `" (try: ` + strings.Join(Commands, ", ") + `)`)
}

func isUpgradeError(err error) bool { _, ok := err.(*upgrade.Error); return ok }

var hostRuleRe = regexp.MustCompile("Host\\(`([^`]+)`\\)")

func validate(args *Parsed, st *spec.Stack, out io.Writer) *Exit {
	fmt.Fprintln(out, "✓ spec parses — kind: "+string(st.Kind)+", "+js.NumberString(float64(len(st.Axes)))+` axis/axes, stack "`+st.Stack+`"`)
	for _, r := range st.Requires {
		fmt.Fprintln(out, "  requires: "+r.Name)
	}
	if st.Compose != nil {
		profiles := strings.Join(st.Compose.Profiles, ", ")
		if profiles == "" {
			profiles = "no profiles"
		}
		fmt.Fprintln(out, "  compose: "+st.Compose.File+" ["+profiles+"]")
		// What the generated labels would be. Read-only — `validate` must not write the derived
		// file — and shown because pstack derives the hostname.
		abs, _ := filepath.Abs(args.File)
		if raw, err := os.ReadFile(filepath.Join(filepath.Dir(abs), st.Compose.File)); err == nil && strings.Contains(string(raw), "pstack.routing.") {
			if parsed, err := yamlx.Parse(raw); err == nil {
				if doc, ok := parsed.(*omap.Map); ok {
					// unknown rather than probing docker: `validate` is offline by contract.
					aug, err := autolabel.AugmentComposeDoc(autolabel.AugmentArgs{Doc: doc, Spec: st, Challenge: autolabel.Unknown})
					if err != nil {
						if spec.IsSpecError(err) {
							fmt.Fprintln(out, "  ! "+err.Error())
						}
					} else {
						aug.Generated.Each(func(svc string, v any) {
							added := strs(v)
							host, port := "?", "?"
							for _, l := range added {
								if strings.Contains(l, ".rule=Host(") {
									if m := hostRuleRe.FindStringSubmatch(l); m != nil {
										host = m[1]
									}
								}
								if strings.Contains(l, "loadbalancer.server.port") {
									if i := strings.IndexByte(l, '='); i >= 0 {
										port = l[i+1:]
									}
								}
							}
							fmt.Fprintln(out, "  generated: "+svc+" → https://"+host+" (container port "+port+")")
						})
						aug.Skipped.Each(func(svc string, v any) {
							why, _ := v.(string)
							if strings.Contains(why, "own traefik") {
								fmt.Fprintln(out, "  generated: "+svc+" skipped — "+why)
							}
						})
					}
				}
			}
		}
		for _, s := range st.Compose.Subdomains {
			depth := "one label"
			if s.Depth == spec.DepthAny {
				depth = "any depth — HTTP only, no cert can cover it"
			}
			fmt.Fprintln(out, "  subdomains: *."+s.Host+" → "+s.Profile+"  ("+depth+")")
			fmt.Fprintln(out, "              "+s.VarName+" — interpolate it into a router label")
		}
	}
	for _, a := range st.Axes {
		fmt.Fprintln(out, "  - "+a.Name+": "+strings.Join(a.Hooks(), ", "))
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(out, "  ! "+w)
	}
	return nil
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, js.ToString(x))
	}
	return out
}

func cloudInit(args *Parsed, io IO) *Exit {
	out, errOut := io.Stdout, io.Stderr
	in := bufio.NewReader(io.Stdin)
	var failed *Exit
	// Every field: flag first, then a prompt — unless -y, which makes a missing required flag an
	// error instead.
	need := func(label, given, fallback string) string {
		if failed != nil {
			return ""
		}
		if given != "" {
			return given
		}
		if args.Yes {
			if fallback != "" {
				return fallback
			}
			failed = fail("--yes was given but \"" + label + "\" is missing — pass it as a flag")
			return ""
		}
		v, err := cloudinit.Ask(in, errOut, label, fallback)
		if err != nil {
			failed = fail(err.Error())
		}
		return v
	}
	domain := need("Preview domain (e.g. preview.example.com)", args.Domain, "")
	acmeEmail := need("Let's Encrypt contact email", args.AcmeEmail, "")
	if failed != nil {
		return failed
	}
	// Optional: most providers inject a key at boot. Asked for, never required.
	sshKey := args.SSHKey
	if sshKey == "" && !args.Yes {
		sshKey = cloudinit.AskOptional(in, errOut, "Extra SSH public key line")
	}
	// The DNS-01 credential, read the same two ways `init` reads it. Required for dns01 — validate()
	// refuses the render rather than hand over a file whose host boots with an empty dns.env.
	dnsToken, _ := io.Env("PSTACK_DNS_TOKEN")
	if args.DNSTokenFile != "" {
		b, err := os.ReadFile(args.DNSTokenFile)
		if err != nil {
			return fail("--dns-token-file: " + err.Error())
		}
		dnsToken = strings.TrimSpace(string(b))
	}
	// Generated by default: a password typed at a prompt tends to be one already in use elsewhere.
	generated := args.Password == ""
	dashboardPassword := args.Password
	if generated {
		dashboardPassword = cloudinit.RandomPassword()
	}
	// The first pstack account. OPT-IN, unlike the dashboard password: this file ends up in instance
	// metadata, so an account nobody asked for is a credential nobody asked for — and with no admin
	// named, `POST /api/auth/bootstrap` with the bearer token is still the way in, exactly as before.
	adminUser := args.AdminUser
	if adminUser == "" && !args.Yes {
		adminUser = cloudinit.AskOptional(in, errOut, "First pstack admin username")
	}
	// A password on a flag with no account to attach it to is a credential that silently does
	// nothing. Refuse it rather than render a file the operator will believe carries it.
	if adminUser == "" && args.AdminPassword != "" {
		return fail("--admin-password needs --admin-user (or answer the admin prompt) — on its own it creates no account")
	}
	// Generated for the same reason as the dashboard password, and one more that is specific to this
	// one: it is readable by every process on the box for the life of the instance, so it must be a
	// value that exists nowhere else and can be thrown away after the first sign-in. Never prompted.
	adminGenerated := adminUser != "" && args.AdminPassword == ""
	adminPassword := args.AdminPassword
	if adminGenerated {
		adminPassword = cloudinit.RandomPassword()
	}
	// Blank is the SAFER answer here, so it is the default and never generated: `init` mints a token
	// on the host and prints it once into the boot log, which keeps it out of instance metadata
	// altogether. Set one only when something already holds it — a CI secret, a second host.
	//
	// Flag only, deliberately unprompted, like the dashboard password beside it: a prompt whose right
	// answer is almost always "press enter" is a prompt that teaches operators to paste a bearer
	// token into one.
	apiToken := args.APIToken
	configRepo := args.ConfigRepo
	if configRepo == "" && !args.Yes {
		configRepo = cloudinit.AskOptional(in, errOut, "Config repo git URL")
	}
	// The portable config, if one was asked for. Its passphrase is resolved exactly as `push config`
	// resolves it — PSTACK_CONFIG_KEY or a no-echo prompt — and never from a flag.
	var sealed []byte
	configKey := ""
	if args.Config != "" || args.ConfigURL != "" {
		var ex *Exit
		configKey, ex = configPassphrase(io, false)
		if ex != nil {
			return ex
		}
	}
	if args.Config != "" {
		b, err := os.ReadFile(args.Config)
		if err != nil {
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		// OPEN IT HERE. The host has nobody to ask, so a passphrase that does not match the file shows
		// up as an instance that booted with none of its credentials and one line in a log nobody
		// reads. This is the last moment a human is present to be told. (--config-url cannot be
		// checked this way: the URL is often reachable only from the host being provisioned.)
		plain, err := config.Unseal(b, configKey)
		if err != nil {
			return fail(err.Error())
		}
		doc, err := config.Parse(plain)
		if err != nil {
			return fail(err.Error())
		}
		sealed = b
		// Same rule as `push config`: a notifier URL IS a credential, so the list goes to a terminal
		// or nowhere — never into whatever this command's stderr was redirected to.
		if trusts := doc.Trusts(); len(trusts) > 0 && isTerminal(errOut) {
			fmt.Fprintln(errOut, "The host this file boots will trust:")
			for _, t := range trusts {
				fmt.Fprintln(errOut, "  - "+t)
			}
			fmt.Fprintln(errOut, "")
		}
	}
	yaml, err := cloudinit.RenderCloudInit(cloudinit.Answers{
		Domain: domain, AcmeEmail: acmeEmail, SSHKey: sshKey, DashboardPassword: dashboardPassword,
		Challenge: args.Challenge, DNSProvider: args.DNSProvider, UI: args.UI, Orchestrator: args.Orchestrator,
		AdminUser: adminUser, AdminPassword: adminPassword, Token: apiToken, DNSToken: dnsToken,
		ConfigRepo: configRepo, Distro: args.Distro,
		ConfigSealed: sealed, ConfigURL: args.ConfigURL, ConfigKey: configKey,
	})
	if err != nil {
		if _, ok := err.(*cloudinit.Error); ok {
			return fail(err.Error())
		}
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if args.Out != "" {
		if err := os.WriteFile(args.Out, []byte(yaml), 0o666); err != nil {
			return &Exit{Code: ExitFailed, Msg: err.Error()}
		}
		fmt.Fprintln(errOut, "wrote "+args.Out)
	} else {
		// stdout, so `pstack cloud-init … > user-data.yaml` composes.
		fmt.Fprintln(out, yaml)
	}
	gen := ""
	if generated {
		gen = "  (generated)"
	}
	fmt.Fprintln(errOut, "")
	fmt.Fprintln(errOut, "  Traefik dashboard:  admin / "+dashboardPassword+gen)
	fmt.Fprintln(errOut, "  Save it now — it is hashed into the file, not recoverable from it.")
	// This is the only time the admin password is shown: the file carries it, but `init` on the host
	// never prints it, so a copy taken here is the copy the operator keeps.
	if adminUser != "" {
		adminGen := ""
		if adminGenerated {
			adminGen = "  (generated)"
		}
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "  pstack admin:       "+adminUser+" / "+adminPassword+adminGen)
		fmt.Fprintln(errOut, "  Created on first boot only — sign in at https://control."+domain+".")
	}
	fmt.Fprintln(errOut, "")
	fmt.Fprintln(errOut, "  This file carries that password and your ACME email, and a provider stores")
	fmt.Fprintln(errOut, "  user-data where any process on the instance can read it. Do not commit it.")
	// Named one by one, because "do not commit it" is advice and "the bearer token for a Docker
	// socket is in this file" is a fact about blast radius.
	var also []string
	if adminUser != "" {
		also = append(also, "the admin password")
	}
	if apiToken != "" {
		also = append(also, "PSTACK_TOKEN")
	}
	// The passphrase is in the file in BOTH modes; the sealed export is only in the --config one.
	// Naming them separately is the whole point — it is the difference the operator is choosing between.
	if configKey != "" {
		also = append(also, "PSTACK_CONFIG_KEY")
	}
	if len(sealed) > 0 {
		also = append(also, "the sealed config export itself, which that key opens")
	}
	if len(also) > 0 {
		fmt.Fprintln(errOut, "  It also carries "+andList(also)+".")
	}
	fmt.Fprintln(errOut, "")
	fmt.Fprintln(errOut, "  DNS first:  "+domain+"  and  *."+domain+"   A -> <server-ip>")
	if args.Challenge == "http01" {
		fmt.Fprintln(errOut, "  HTTP-01 needs port 80 reachable from the internet.")
	}
	return nil
}

// swarmCmd: read-only, both subcommands. Creating the swarm belongs to `init`; leaving one is
// `docker swarm leave`.
func swarmCmd(args *Parsed, runner exec.Runner, io IO) *Exit {
	out, errOut := io.Stdout, io.Stderr
	sub := args.Sub
	if sub == "" {
		sub = "status"
	}
	switch sub {
	case "status":
		info := swarm.SwarmInfo(runner)
		fmt.Fprintln(out, swarm.SwarmReport(info))
		// Exit 1 when there is no swarm to report on: a script gets its answer from the status.
		if info.Reachable && info.Active {
			return nil
		}
		return &Exit{Code: ExitFailed}
	case "join":
		var distro *string
		if args.Distro != "" {
			d := args.Distro
			distro = &d
		}
		made := swarm.JoinMaterial(swarm.JoinArgs{Runner: runner, Format: args.Format, Distro: distro})
		if !made.OK {
			// A bad flag is usage (3); a host that is not a manager, or a docker that did not
			// answer, is a failed operation (1).
			if made.Kind == swarm.BadFormat || made.Kind == swarm.BadDistro {
				return fail(made.Message)
			}
			return &Exit{Code: ExitFailed, Msg: made.Message}
		}
		if args.Out != "" {
			if err := os.WriteFile(args.Out, []byte(made.Text), 0o666); err != nil {
				return &Exit{Code: ExitFailed, Msg: err.Error()}
			}
			fmt.Fprintln(errOut, "wrote "+args.Out)
		} else {
			// stdout, so `pstack swarm join --format script > join.sh` composes.
			fmt.Fprint(out, made.Text)
		}
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "  This is a SECRET — whoever has it can add a node that runs any task on this")
		fmt.Fprintln(errOut, "  swarm. Rotate it with `docker swarm join-token --rotate worker`.")
		fmt.Fprintln(errOut, "  Open "+swarm.PortList()+" between every pair of nodes.")
		return nil
	}
	return fail("usage: pstack swarm <status|join>   (join: --format " + strings.Join(swarm.JoinFormats, "|") + " [--distro <name>] [-o <file>])")
}

// ── `pstack pull config` / `pstack push config` ─────────────────────────────────────────────────
//
// THE FIRST COMMANDS THAT TALK TO A REMOTE pstack, and there was no convention for addressing one.
// Everything else either runs on the host it manages (`init`, `upgrade`, `swarm`) or IS the server
// (`serve`; `healthcheck` hardcodes 127.0.0.1 because it runs inside the container it probes). So
// PSTACK_API_URL is invented here, and deliberately has NO DEFAULT: the control stack publishes no
// host port, so a loopback default would fail on the very host an operator would try it on, and any
// other guess would silently talk to the wrong pstack. An empty value is a refusal with a sentence.
//
// THE PASSPHRASE HAS NO FLAG, on any command, ever. `/proc/<pid>/cmdline` is world-readable and
// `ps` prints it, so a flag would publish it to every user on the box for the life of the process.
// It comes from PSTACK_CONFIG_KEY or from a no-echo prompt, and is never echoed, logged or written.
//
// SEALING IS THE CLIENT'S JOB (see the design note): the API egresses plaintext over authenticated
// TLS and the passphrase never leaves this process, because sending it to the server would put
// every host's key in the place the keys are protecting.
//
// This is more logic than `cli` normally holds — the package is meant to be argv, dispatch and exit
// codes. It is here because `internal/config` is owned elsewhere and these functions are HTTP,
// prompting and file modes, which is CLI work; if a second caller ever needs them they belong in a
// package of their own.

// isTerminal reports whether v is a real terminal. It is the ONE gate on printing credentials: the
// pre-write summary below names notifier URLs, and for a chat notifier the URL *is* the secret, so
// it is shown to a human looking at a tty and never to a pipe — which at boot is
// /var/log/cloud-init-output.log.
// A character-device test is NOT a terminal test, which is how this was first written: `/dev/null`
// is a character device and passed it, and so does `/dev/console` — whose output most providers
// persist and expose over their API, which is the copy this gate exists to keep credentials out of.
// So the mode check is only a cheap pre-filter (a pipe cannot be a terminal, and rejecting one costs
// no subprocess), and the answer comes from asking the terminal driver.
//
// `stty` rather than golang.org/x/term for the reason noEcho gives below: four justified
// dependencies, and this does not make five. When stty is missing — a real state on a minimal image
// — every caller sees "not a terminal", which refuses to print credentials and demands `-y`. That is
// the safe direction, and it is the same direction noEcho already fails in.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	c := osexec.Command("stty")
	c.Stdin = f
	return c.Run() == nil
}

// noEcho turns terminal echo off and returns the restore, or ok=false when it could not.
//
// FAILING CLOSED IS THE POINT: if echo cannot be turned off, nothing is prompted for. Reading a
// passphrase onto a screen that is echoing it — and into the scrollback, and into whatever recorded
// the session — is the one outcome this must never have, and the cost of refusing is one
// environment variable. `stty` is a subprocess, so "it is not installed" is a real state on a
// minimal image; the earlier version swallowed that error and typed the passphrase in the clear.
//
// Via `stty` rather than golang.org/x/term because go.mod holds four dependencies, each justified,
// and one passphrase prompt does not justify a fifth (AGENTS.md). It acts on the PROCESS's stdin,
// not on IO.Stdin, because a terminal mode is a property of a file descriptor and an injected
// io.Reader has none — which is also why the descriptor is a PARAMETER: a test can hand it one that
// is provably not a terminal and get the refusal deterministically, on any machine.
//
// The interrupt handler is not decoration — Ctrl-C at a passphrase prompt is common, and a terminal
// left with echo off is invisible typing until the operator knows to run `stty sane`.
func noEcho(tty *os.File) (func(), bool) {
	if tty == nil || !isTerminal(tty) {
		return nil, false
	}
	stty := func(arg string) error {
		c := osexec.Command("stty", arg)
		c.Stdin = tty
		return c.Run()
	}
	if err := stty("-echo"); err != nil {
		return nil, false
	}
	var once sync.Once
	restore := func() { once.Do(func() { _ = stty("echo") }) }
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		if _, ok := <-sig; ok {
			restore()
			os.Exit(130) // 128 + SIGINT, what an interrupted shell command returns
		}
	}()
	return func() {
		signal.Stop(sig)
		close(sig)
		restore()
	}, true
}

// askSecret prompts without echoing. Only the line terminator is stripped — a passphrase may
// legitimately start or end with a space, and TrimSpace would silently change it.
func askSecret(in *bufio.Reader, tty *os.File, out io.Writer, label string) (string, error) {
	restore, ok := noEcho(tty)
	if !ok {
		return "", &Exit{Code: ExitUsage, Msg: "cannot turn terminal echo off, so a typed passphrase would be shown and kept in the scrollback — set PSTACK_CONFIG_KEY instead"}
	}
	defer restore()
	fmt.Fprint(out, label+": ")
	v, err := in.ReadString('\n')
	fmt.Fprintln(out) // the Enter the terminal did not echo
	if err != nil && v == "" {
		return "", &Exit{Code: ExitUsage, Msg: "no passphrase on stdin (it closed). Set PSTACK_CONFIG_KEY for non-interactive use."}
	}
	return strings.TrimRight(v, "\r\n"), nil
}

// configPassphrase resolves PSTACK_CONFIG_KEY, or prompts. `confirm` is for `pull`, where a typo
// produces a file that nothing — including the operator who made it — can ever open again.
func configPassphrase(ios IO, confirm bool) (string, *Exit) {
	if v, ok := ios.Env("PSTACK_CONFIG_KEY"); ok && v != "" {
		return v, nil
	}
	if !isTerminal(ios.Stdin) {
		return "", fail("no passphrase: set PSTACK_CONFIG_KEY, or run this on a terminal so it can be asked for. There is no flag on purpose — argv is world-readable through `ps`.")
	}
	in := bufio.NewReader(ios.Stdin)
	// isTerminal above has already established that this is one, so the assertion cannot fail here;
	// noEcho re-checks anyway, because it is the function that must never be wrong about it.
	tty, _ := ios.Stdin.(*os.File)
	p, err := askSecret(in, tty, ios.Stderr, "Config passphrase")
	if err != nil {
		return "", fail(err.Error())
	}
	if p == "" {
		return "", fail("the passphrase was empty — an unsealed export is a plaintext copy of every credential on the host")
	}
	if confirm {
		again, err := askSecret(in, tty, ios.Stderr, "Again")
		if err != nil {
			return "", fail(err.Error())
		}
		if again != p {
			return "", fail("the two passphrases differ — nothing was written")
		}
	}
	return p, nil
}

// apiBase resolves and CHECKS the remote before anything is sent to it.
func apiBase(env func(string) (string, bool)) (string, string, *Exit) {
	base, _ := env("PSTACK_API_URL")
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", "", fail("set PSTACK_API_URL to the pstack this should talk to, e.g. https://api.preview.example.com — there is deliberately no default, because a guess would silently talk to the wrong host")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fail(`PSTACK_API_URL must be an http(s) URL, e.g. https://api.preview.example.com (got "` + base + `")`)
	}
	// Plain HTTP is allowed only where it cannot leave the machine or the local network: the boot
	// step talks to the control container's bridge address, which has no certificate and needs none.
	// Anywhere else, `http://` would put a root token and every credential on the host on the wire
	// in the clear, and this is the one command where that is the entire payload.
	if u.Scheme == "http" && !privateAddr(u.Hostname()) {
		return "", "", fail("refusing to send the root token and every credential on a host over plain http to " + u.Hostname() + " — use https://, or a loopback/private address")
	}
	tok, _ := env("PSTACK_TOKEN")
	if tok == "" {
		return "", "", fail("set PSTACK_TOKEN — /api/config is root-token only, and an admin session is deliberately not enough for a full credential dump")
	}
	return base, tok, nil
}

// privateAddr is Go rule 13: parse, never compare strings. `localhost` is spelled, not resolved —
// a name that resolves off-box is exactly what this must not admit.
func privateAddr(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

// apiCall is one authenticated request. A redirect is a FAILURE, never a hop (the same control
// `notify` uses): following one would resend the bearer token — and, on push, the entire plaintext
// config — to whatever host the redirect names.
func apiCall(method, endpoint, token string, body []byte) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, endpoint, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := &http.Client{
		Timeout:       120 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	// Bounded, and a body AT the bound is an error rather than a silent truncation: this response
	// becomes the sealed export, and half a document sealed successfully is worse than no file.
	// 64 MiB is far above any real config (the largest thing in one is a stored compose file).
	const max = 64 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err == nil && len(b) > max {
		return resp.StatusCode, nil, errors.New("the API's answer is larger than 64 MiB — refusing to seal a response that may be truncated")
	}
	return resp.StatusCode, b, err
}

// apiError turns a non-2xx into one line. `fail()`'s shape first, then the raw body truncated —
// never the whole thing, because an unexpected body could be anything at all.
func apiError(status int, body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "the API answered " + js.NumberString(float64(status))
	}
	return "the API answered " + js.NumberString(float64(status)) + ": " + js.Truncate(s, 300)
}

func pullConfig(args *Parsed, ios IO) *Exit {
	if args.Sub != "config" {
		return fail("usage: pstack pull config -o <file>")
	}
	if args.Out == "" {
		// Never stdout: a terminal's scrollback, a shell's history and a CI log are all places this
		// file must not be, and `-o` is the only spelling that can also set the mode.
		return fail("pull config needs -o <file> — the export is written 0600 to a file, never to stdout")
	}
	base, token, ex := apiBase(ios.Env)
	if ex != nil {
		return ex
	}
	pass, ex := configPassphrase(ios, true)
	if ex != nil {
		return ex
	}
	status, body, err := apiCall("GET", base+"/api/config", token, nil)
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if status < 200 || status >= 300 {
		return &Exit{Code: ExitFailed, Msg: apiError(status, body)}
	}
	sealed, err := config.Seal(body, pass)
	if err != nil {
		return fail(err.Error())
	}
	// 0600 at creation, so the window in which a world-readable file holds the export never exists;
	// the Chmod after it is for the case where the path already existed with a looser mode, which
	// O_TRUNC does not change.
	if err := os.WriteFile(args.Out, sealed, 0o600); err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if err := os.Chmod(args.Out, 0o600); err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	errOut := ios.Stderr
	fmt.Fprintln(errOut, "wrote "+args.Out+" (0600, sealed with "+config.SealScheme+")")
	fmt.Fprintln(errOut, "")
	fmt.Fprintln(errOut, "  This is EVERY credential on that host: account password hashes, API tokens,")
	fmt.Fprintln(errOut, "  host secrets, notifier URLs, the SSO client secret and registry logins. A copy")
	fmt.Fprintln(errOut, "  that leaks is offline-crackable against every account. Keep the file and the")
	fmt.Fprintln(errOut, "  passphrase apart, and commit neither.")
	return nil
}

func pushConfig(args *Parsed, ios IO) *Exit {
	if args.Sub != "config" {
		return fail("usage: pstack push config -i <file>")
	}
	if args.In == "" {
		return fail("push config needs -i <file> — the sealed export written by `pstack pull config`")
	}
	base, token, ex := apiBase(ios.Env)
	if ex != nil {
		return ex
	}
	// Read BEFORE prompting: a missing or unreadable file should not cost the operator a passphrase.
	sealed, err := os.ReadFile(args.In)
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	pass, ex := configPassphrase(ios, false)
	if ex != nil {
		return ex
	}
	plain, err := config.Unseal(sealed, pass)
	if err != nil {
		return fail(err.Error())
	}
	doc, err := config.Parse(plain)
	if err != nil {
		return fail(err.Error())
	}
	errOut := ios.Stderr
	// THE PRE-WRITE SUMMARY. A hostile file otherwise silently repoints this host's image pulls at
	// an attacker's registry and its notifications at an attacker's URL. Those strings ARE
	// credentials, so the list itself is printed only to a terminal; a pipe gets the count. Under
	// -y at boot this output is /var/log/cloud-init-output.log, which is not a place for either.
	trusts := doc.Trusts()
	if len(trusts) > 0 {
		if isTerminal(errOut) {
			fmt.Fprintln(errOut, "Applying "+args.In+" would make this host trust:")
			for _, t := range trusts {
				fmt.Fprintln(errOut, "  - "+t)
			}
		} else {
			fmt.Fprintln(errOut, "Applying "+args.In+" would make this host trust "+js.NumberString(float64(len(trusts)))+" registries and notifier URLs — run it on a terminal to see them (they are credentials, so they are not written to a log).")
		}
	}
	if !args.Yes {
		if !isTerminal(ios.Stdin) {
			return fail("push config will not apply a file unattended without -y — and -y means you have read what it trusts")
		}
		in := bufio.NewReader(ios.Stdin)
		fmt.Fprint(errOut, "Apply it? [y/N]: ")
		answer, _ := in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fail("not applied")
		}
	}
	status, body, err := apiCall("POST", base+"/api/config", token, plain)
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if status < 200 || status >= 300 {
		return &Exit{Code: ExitFailed, Msg: apiError(status, body)}
	}
	// Decoded, never echoed. The route answers 200 with `trusts` alongside `created`/`skipped`, and
	// `trusts` is the list of registries and notifier URLs — credentials. Printing the raw body as a
	// fallback would put them on stdout, which is the very thing the terminal gate above exists to
	// stop, so an unrecognised body is an error with no body in it.
	var sum config.Summary
	if err := json.Unmarshal(body, &sum); err != nil {
		return &Exit{Code: ExitFailed, Msg: "the config was applied, but the API answered with a body this pstack does not understand — check that both ends are the same version (" + err.Error() + ")"}
	}
	out := ios.Stdout
	for _, c := range sum.Created {
		fmt.Fprintln(out, "created  "+c)
	}
	for _, s := range sum.Skipped {
		fmt.Fprintln(out, "skipped  "+s)
	}
	fmt.Fprintln(errOut, "")
	fmt.Fprintln(errOut, "  "+js.NumberString(float64(len(sum.Created)))+" created, "+js.NumberString(float64(len(sum.Skipped)))+" skipped. Nothing was overwritten — `push` only ever creates,")
	fmt.Fprintln(errOut, "  so anything already on this host kept the value it had.")
	return nil
}

// andList is "a", "a and b", "a, b and c". Two items is the existing wording, byte for byte — the
// cloud-init summary could only ever say two things before this.
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
