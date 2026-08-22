// Package upgrade is `pstack upgrade` — move a host to a new version of pstack.
//
// ── WHY A COMMAND AND NOT A PARAGRAPH IN THE DOCS ────────────────────────────────────────────────
//
// The upgrade was always three commands (install, rebuild the image, re-run `init`), and the third
// one has a trap: `init` takes `PSTACK_TOKEN` from the environment and MINTS A FRESH ONE when it is
// absent. So the obvious `pstack init --domain … --acme-email …` silently rotates the machine token
// and every CI job starts getting 401s — from a command whose purpose was "change nothing but the
// version". Everything needed to avoid that is already on disk in `control/.env`; nothing but habit
// was stopping the CLI from reading it.
//
// ── THE RE-EXEC IS LOAD-BEARING ──────────────────────────────────────────────────────────────────
//
// `pstack build-image` writes a Dockerfile that installs `@samyx/preview-stacks@<the running CLI's
// own version>` — that pin is what stops the image and the CLI disagreeing. Which means a single
// process CANNOT do the whole upgrade: after installing 0.26.0, this process is still the 0.25.x
// code that was loaded at start, so its `build-image` would faithfully build a 0.25.x image and
// report success. The observable result is an "upgrade" that changes the CLI and leaves the running
// server exactly where it was.
//
// So it is two phases. Phase 1 installs and then executes the NEWLY INSTALLED binary with
// `--resume`; phase 2 (running as the new version) rebuilds and re-inits. The handoff is a real
// process boundary, which is also why the second half is a plain `pstack upgrade --resume` an
// operator can run by hand if the first half already succeeded.
//
// ── WHAT IT DELIBERATELY DOES NOT DO ─────────────────────────────────────────────────────────────
//
// It does not run from the API. The control plane is a container inside the stack being recreated;
// upgrading itself means killing the process mid-request, and a broken new image would leave the
// host with no control plane and no remote way to repair it. This is CLI-only for the same reason
// `init` is — see the header of initctl.
//
// DISTRIBUTION. pstack is one static binary from GitHub Releases; phase `install` runs that
// release's own install.sh (checksum-verified) into the directory the running binary lives in,
// then re-execs `pstack upgrade --resume` — which is also what a 0.28.0 host's one-time hop runs.
package upgrade

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// Error is an upgrade the operator must act on. The CLI prints its message and exits 3.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// ControlState is everything `init` needs, recovered from what a previous `init` left on disk.
type ControlState struct {
	// Token is the existing machine token. Reused, never regenerated — the whole point of this command.
	Token       string
	Domain      string
	AcmeEmail   string
	DNSProvider string
	// Challenge is not in `.env`: the challenge is baked into the GENERATED compose file as Traefik
	// flags, so it is read back from there. Getting this wrong is expensive in a way a re-run cannot
	// undo — under HTTP-01 each router resolves its own certificate, under DNS-01 one wildcard
	// covers them, and flipping a live host to the wrong one burns the weekly rate limit.
	Challenge initctl.Challenge
	// UI is `advanced` when the control stack has the separate UI container, so it gets rebuilt too.
	UI initctl.UI
	// Orchestrator is `swarm` or `compose`, from `.env`. A host provisioned before 0.26.0 has no such
	// line and IS a compose host — so absent means compose, never the new-host default. Switching is
	// explicit (`--orchestrator`) because it recreates the networks, which needs every preview down.
	Orchestrator spec.Orchestrator
	// DNSToken is the DNS-01 credential from `control/dns.env`, or "" when the host has none (http01,
	// a tokenless provider, or the file is missing). Read back for the same reason the token is:
	// `init` rewrites dns.env from PSTACK_DNS_TOKEN on every run, so an upgrade that does not carry
	// it zeroes the only copy — Traefik is recreated with no credential, the existing wildcard keeps
	// serving, and the renewal silently fails weeks later.
	DNSToken string
}

var (
	envLineRe    = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(.*)$`)
	dnsChallenge = regexp.MustCompile(`(?i)dnschallenge`)
	advancedSvc  = regexp.MustCompile(`(?m)^\s{2}advanced-ui:`)
	semverRe     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)
)

// ReadControlState reads what the running control stack was configured with.
//
// Parsed rather than re-asked, because every value here is one an operator would have to remember
// exactly — and a wrong `--domain` on an upgrade rewrites every router rule on the host.
func ReadControlState(dataDir string) (*ControlState, error) {
	controlDir := filepath.Join(dataDir, "control")
	envPath := filepath.Join(controlDir, ".env")
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		return nil, &Error{"no control stack found at " + controlDir + " (" + envPath + " is missing). This host has never run " +
			"`pstack init`, or its data directory is elsewhere — set PSTACK_DATA to point at it."}
	}

	env := map[string]string{}
	for _, line := range strings.Split(string(envBytes), "\n") {
		if m := envLineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			env[m[1]] = m[2]
		}
	}
	// need: absent OR EMPTY is missing (`if (!v)`).
	need := func(key string) (string, error) {
		v := env[key]
		if v == "" {
			return "", &Error{envPath + " has no " + key + ". It was written by an older `pstack init` or edited by hand; " +
				"re-run `pstack init` with the flags you want and this file will be complete again."}
		}
		return v, nil
	}

	// The compose file is generated, so reading it back is reading `init`'s own decision rather than
	// guessing. Absent (a half-finished install) is a refusal, not a default: defaulting to http01 on
	// a dns01 host would make every per-PR router order its own certificate.
	composePath := filepath.Join(controlDir, "docker-compose.yml")
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		return nil, &Error{composePath + " is missing, so the ACME challenge mode cannot be read back. Re-run " +
			"`pstack init` with the flags this host needs instead of upgrading blind."}
	}
	compose := string(composeBytes)

	// Same KEY=VALUE grammar as .env; a missing file is simply "no credential", never a refusal — an
	// http01 host has one with no token line at all.
	dnsProvider := env["DNS_PROVIDER"]
	dnsVar, known := initctl.DNSTokenVar[dnsProvider]
	if !known {
		dnsVar = "CHANGEME_VARIABLE_NAME"
	}
	dnsToken := ""
	if dnsVar != "" { // "" is a tokenless provider: nothing to read
		if b, err := os.ReadFile(filepath.Join(controlDir, "dns.env")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if m := envLineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[1] == dnsVar {
					dnsToken = m[2]
				}
			}
		} /* else: no dns.env — nothing to carry */
	}

	token, err := need("PSTACK_TOKEN")
	if err != nil {
		return nil, err
	}
	domain, err := need("DOMAIN")
	if err != nil {
		return nil, err
	}
	acmeEmail, err := need("ACME_EMAIL")
	if err != nil {
		return nil, err
	}
	s := &ControlState{
		Token:       token,
		Domain:      domain,
		AcmeEmail:   acmeEmail,
		DNSProvider: dnsProvider,
		DNSToken:    dnsToken,
		Challenge:   initctl.HTTP01,
		/*
		 * The service init injects is `advanced-ui`. It used to look for `pstack-ui:` — which appears in
		 * every generated file as a Traefik ROUTER name (`traefik.http.routers.pstack-ui.rule=…`), never
		 * as a service key — so the anchored pattern matched nothing, every host read as `basic`, and an
		 * upgrade silently removed the advanced UI it was supposed to preserve.
		 *
		 * The fixture that "passed" was hand-written with the name I assumed. The test now generates a
		 * real control directory with `init` and reads THAT, which is the only version of this check that
		 * could have failed honestly.
		 */
		UI:           initctl.Basic,
		Orchestrator: spec.Compose,
	}
	if dnsChallenge.MatchString(compose) {
		s.Challenge = initctl.DNS01
	}
	if advancedSvc.MatchString(compose) {
		s.UI = initctl.Advanced
	}
	if env["PSTACK_ORCHESTRATOR"] == "swarm" {
		s.Orchestrator = spec.Swarm
	}
	return s, nil
}

// InstallDirFor is the directory the installer must write to for THIS install to be the one
// replaced: where the running binary actually is, not the installer's default. A cloud-config host
// has it at /usr/local/bin/pstack, which is also the installer's default; a user-prefix install
// (~/.local/bin) would otherwise end up with two binaries and the old one first on PATH — an
// upgrade that reports success and changes nothing. "" in, "" out (not on PATH).
func InstallDirFor(binPath string) string {
	if binPath == "" {
		return ""
	}
	return path.Dir(binPath)
}

// ReleaseBase is where the binaries and the installer live.
const ReleaseBase = "https://github.com/samishal1998/preview-stacks/releases"

// InstallCommand is the installer one-liner for a target: the install.sh of THAT release, pinned,
// into the directory the running binary lives in. `latest` resolves on the release page.
func InstallCommand(target, dir string) string {
	v := strings.TrimPrefix(strings.TrimSpace(target), "v")
	script := ReleaseBase + "/latest/download/install.sh"
	env := ""
	if v != "latest" {
		script = ReleaseBase + "/download/v" + v + "/install.sh"
		env = "PSTACK_VERSION=" + swarm.Shq(v) + " "
	}
	if dir != "" {
		env += "PSTACK_INSTALL_DIR=" + swarm.Shq(dir) + " "
	}
	return "curl -fsSL " + script + " | " + env + "sh"
}

// Step is one command of a plan. Env is nil when the step adds nothing to the environment.
type Step struct {
	Label string
	Cmd   string
	Env   map[string]string
}

// initFlags is the `init` flags that reproduce a host's current configuration.
//
// One builder for both the upgrade and the UI switch: two copies would drift, and the failure mode of
// a drifted copy here is a re-init that quietly changes something nobody asked to change.
func initFlags(state *ControlState) string {
	flags := []string{
		"--domain " + swarm.Shq(state.Domain),
		"--acme-email " + swarm.Shq(state.AcmeEmail),
		"--challenge " + string(state.Challenge),
	}
	if state.Challenge == initctl.DNS01 && state.DNSProvider != "" {
		flags = append(flags, "--dns-provider "+swarm.Shq(state.DNSProvider))
	}
	if state.UI == initctl.Advanced {
		flags = append(flags, "--ui advanced")
	}
	flags = append(flags, "--orchestrator "+string(state.Orchestrator))
	return strings.Join(flags, " ")
}

// Phase is which half of the upgrade runs.
type Phase string

const (
	Install Phase = "install"
	Resume  Phase = "resume"
)

// PlanArgs are PlanUpgrade's inputs.
type PlanArgs struct {
	Phase  Phase
	Target string
	State  *ControlState
	// BinPath is the resolved path of the `pstack` binary, for the re-exec and the install prefix
	// ("" when not on PATH).
	BinPath string
}

// PlanUpgrade is the commands, in order, without running any of them.
//
// Split out so a `--dry-run` prints exactly what a real run would execute, and so the sequence is
// testable on a machine with neither docker nor a control stack.
func PlanUpgrade(a PlanArgs) []Step {
	if a.Phase == Install {
		install := Step{
			Label: "install pstack " + a.Target,
			// The release's own installer: checksum-verified, into the directory the running
			// binary lives in (or the installer's default when that could not be derived).
			Cmd: InstallCommand(a.Target, InstallDirFor(a.BinPath)),
		}
		return []Step{
			install,
			{
				// The handoff. `pstack` from PATH is now the NEW version — see the file header for why this
				// cannot be a function call.
				Label: "rebuild and re-init as the new version",
				Cmd:   "pstack upgrade --resume",
			},
		}
	}

	steps := []Step{{Label: "build the control image", Cmd: "pstack build-image"}}
	if a.State.UI == initctl.Advanced {
		steps = append(steps, Step{Label: "build the advanced UI image", Cmd: "pstack build-image --ui"})
	}
	return append(steps, Step{
		Label: "re-run init (same domain, same token)",
		Cmd:   "pstack init " + initFlags(a.State),
		// THE line this command exists for. Without it `init` generates a new token and every CI job
		// holding the old one starts getting 401s.
		Env: initEnv(a.State),
	})
}

// initEnv is what `init` must be handed so that NOTHING rotates: the machine token always, and the
// DNS-01 credential when the host has one. `init` writes dns.env from PSTACK_DNS_TOKEN
// unconditionally, so omitting it here is how an upgrade used to blank a host's Cloudflare token.
func initEnv(state *ControlState) map[string]string {
	env := map[string]string{"PSTACK_TOKEN": state.Token}
	if state.DNSToken != "" {
		env["PSTACK_DNS_TOKEN"] = state.DNSToken
	}
	return env
}

// PlanUISwitch is the steps for changing ONLY the UI mode.
//
// Not PlanUpgrade's resume list: that rebuilds the control image, which takes minutes and has
// nothing to do with which UI is served. Switching to `advanced` builds the SPA image (init refuses
// without it, by name); switching back to `basic` builds nothing, because the API already carries
// the embedded UI.
func PlanUISwitch(state *ControlState) []Step {
	var steps []Step
	if state.UI == initctl.Advanced {
		steps = append(steps, Step{Label: "build the advanced UI image", Cmd: "pstack build-image --ui"})
	}
	return append(steps, Step{
		Label: "re-run init with the " + string(state.UI) + " UI",
		Cmd:   "pstack init " + initFlags(state),
		// Same reason as an upgrade: without it `init` mints a new token and every CI job breaks.
		Env: initEnv(state),
	})
}

// SwitchUIOptions are SwitchUI's inputs.
type SwitchUIOptions struct {
	DataDir string
	UI      initctl.UI
	Runner  exec.Runner
	// Log receives each progress line (stdout when nil).
	Log func(string)
}

// SwitchUI switches a host between the embedded UI and the standalone SPA container.
//
// Everything needed is already on disk, which is the whole point — the alternative is re-typing
// `init`'s full command line with the token, and getting the token wrong is how a working host stops
// answering CI. Returns changed=false and no steps for a no-op.
func SwitchUI(opts SwitchUIOptions) (changed bool, steps []Step, err error) {
	say := sayer(opts.Log)
	current, err := ReadControlState(opts.DataDir)
	if err != nil {
		return false, nil, err
	}
	if current.UI == opts.UI {
		// No container recreation for a no-op: recreating the control stack is a brief outage, and
		// "already advanced" is an answer, not a reason to cause one.
		say("Already serving the " + string(opts.UI) + " UI. Nothing to do.")
		return false, []Step{}, nil
	}

	state := *current
	state.UI = opts.UI
	steps = PlanUISwitch(&state)
	say(string(current.UI) + " UI → " + string(opts.UI) + " UI   (" + state.Domain + ")")
	for _, step := range steps {
		say("  → " + step.Label)
		r := opts.Runner.Run(step.Cmd, exec.RunOptions{Env: step.Env, Label: step.Label})
		if !r.OK && !r.Skipped {
			return false, nil, &Error{step.Label + " failed (exit " + strconv.Itoa(r.Code) + ").\n" + lastLines(firstOf(r.Stderr, r.Stdout), 8)}
		}
		if strings.TrimSpace(r.Stdout) != "" {
			say(exec.Indent(r.Stdout))
		}
	}
	return true, steps, nil
}

// Options are Upgrade's inputs.
type Options struct {
	DataDir string
	// Target is an npm dist-tag or exact version. `latest` is resolved by bun, not by us. Default latest.
	Target string
	// Phase defaults to install.
	Phase Phase
	// UI forces the UI mode instead of reading it back ("" reads it back).
	//
	// Exists because detection was wrong once: a host whose advanced UI was dropped by a bad upgrade
	// now reads as `basic` — truthfully, since that IS its current state — so putting it back needs a
	// way to say so without hand-writing the whole `init` line and its token.
	UI initctl.UI
	// Orchestrator switches the host's orchestrator. Only when typed; init refuses while previews
	// hold the networks. "" keeps what was detected.
	Orchestrator spec.Orchestrator
	Runner       exec.Runner
	// Log receives each progress line (stdout when nil).
	Log func(string)
}

// Result is what an upgrade reports.
type Result struct {
	From  string
	To    string
	Steps []Step
}

// LookPath resolves `pstack` the way the re-exec will (`Bun.which` in the TS). A var so a test can
// pin it without touching PATH.
var LookPath = func() string {
	p, err := osexec.LookPath("pstack")
	if err != nil {
		return ""
	}
	return p
}

// Upgrade runs it.
//
// Stops at the first failing step and says which — a half-applied upgrade is recoverable by hand
// (the steps are ordinary commands), and continuing past a failed image build would recreate the
// stack from an image that does not exist.
func Upgrade(opts Options) (*Result, error) {
	say := sayer(opts.Log)
	phase := opts.Phase
	if phase == "" {
		phase = Install
	}
	target := opts.Target
	if target == "" {
		target = "latest"
	}
	detected, err := ReadControlState(opts.DataDir)
	if err != nil {
		return nil, err
	}
	state := *detected
	if opts.UI != "" {
		state.UI = opts.UI
	}
	if opts.Orchestrator != "" {
		state.Orchestrator = opts.Orchestrator
	}

	// The same PATH the re-exec below will use, so the prefix and the handoff can never disagree
	// about which `pstack` is being talked about.
	binPath := LookPath()
	steps := PlanUpgrade(PlanArgs{Phase: phase, Target: target, State: &state, BinPath: binPath})

	if phase == Install {
		line := "pstack " + version.Get() + " → " + target + "   (" + state.Domain + ", " + string(state.Challenge) + ", " + string(state.UI) + " UI, " + string(state.Orchestrator)
		if opts.UI != "" && opts.UI != detected.UI {
			line += " — overriding the detected " + string(detected.UI)
		}
		if opts.Orchestrator != "" && opts.Orchestrator != detected.Orchestrator {
			line += " — switching from " + string(detected.Orchestrator)
		}
		say(line + ")")
		if binPath == "" {
			say("  note: `pstack` is not on PATH, so the install directory could not be derived.")
			say("        The installer will use its default (/usr/local/bin), which may not be where this binary lives.")
		}
	} else {
		say("pstack " + version.Get() + " — rebuilding and recreating the control stack")
	}

	for i, step := range steps {
		say("  → " + step.Label)
		r := opts.Runner.Run(step.Cmd, exec.RunOptions{Env: step.Env, Label: step.Label})
		if !r.OK && !r.Skipped {
			var rest []string
			for _, s := range steps[i+1:] {
				rest = append(rest, "  "+s.Cmd)
			}
			remaining := strings.Join(rest, "\n")
			if remaining == "" {
				remaining = "  (none)"
			}
			return nil, &Error{step.Label + " failed (exit " + strconv.Itoa(r.Code) + ").\n" +
				lastLines(firstOf(r.Stderr, r.Stdout), 8) + "\n\n" +
				"Nothing after this step ran. The remaining steps are ordinary commands you can run by " +
				"hand:\n" + remaining}
		}
		// The install step's own output is noise; the re-exec's is the upgrade's real transcript.
		if strings.HasPrefix(step.Cmd, "pstack ") && strings.TrimSpace(r.Stdout) != "" {
			say(exec.Indent(r.Stdout))
		}
	}

	/*
	 * A dry run must show the WHOLE upgrade, not half of it. Phase 2 runs in a process that a dry run
	 * never spawns, so its commands would otherwise be invisible — and those are the interesting ones:
	 * they carry the domain, the challenge mode and the token passthrough an operator wants to check
	 * before letting this touch a live host.
	 */
	if opts.Runner.DryRun() && phase == Install {
		say("  then, as the newly installed version:")
		for _, step := range PlanUpgrade(PlanArgs{Phase: Resume, Target: target, State: &state, BinPath: binPath}) {
			var carried []string
			for _, k := range []string{"PSTACK_TOKEN", "PSTACK_DNS_TOKEN"} {
				if step.Env[k] != "" {
					carried = append(carried, k)
				}
			}
			env := ""
			if len(carried) > 0 {
				env = " (with the existing " + strings.Join(carried, " and ") + ")"
			}
			say("    " + step.Cmd + env)
		}
	}

	return &Result{From: version.Get(), To: target, Steps: steps}, nil
}

func sayer(log func(string)) func(string) {
	if log != nil {
		return log
	}
	return func(l string) { fmt.Println(l) }
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// lastLines is `s.trim().split('\n').slice(-n).join('\n')`.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
