package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/cloudinit"
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
		token, _ := io.Env("PSTACK_DNS_TOKEN")
		err := initctl.Init(initctl.Options{
			DataDir: registry.DataDir(), Domain: args.Domain, AcmeEmail: args.AcmeEmail, DNSProvider: args.DNSProvider,
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
		return Serve(ServeOptions{UIHTML: io.UIHTML, ShareHTML: io.ShareHTML, Stdout: out, Stderr: errOut, Env: io.Env})
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
	yaml, err := cloudinit.RenderCloudInit(cloudinit.Answers{
		Domain: domain, AcmeEmail: acmeEmail, SSHKey: sshKey, DashboardPassword: dashboardPassword,
		Challenge: args.Challenge, DNSProvider: args.DNSProvider, UI: args.UI, Orchestrator: args.Orchestrator,
		AdminUser: adminUser, AdminPassword: adminPassword, Token: apiToken,
		ConfigRepo: configRepo, Distro: args.Distro,
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
	if len(also) > 0 {
		fmt.Fprintln(errOut, "  It also carries "+strings.Join(also, " and ")+".")
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
