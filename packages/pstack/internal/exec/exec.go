// Package exec runs things. One place, so dry-run and output capture are impossible to forget.
//
// Hooks are shell strings, run via `bash -c`, with the resolved spec env injected. That is a
// deliberate trade: this tool orchestrates other people's CLIs (docker, gcloud, curl, psql), and a
// structured arg-array API would just push quoting into YAML. Hooks come from a spec file in your
// own repo — the same trust level as a CI workflow — so this is not a sandbox boundary.
package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// Result is what a command came back with.
type Result struct {
	OK     bool
	Code   int
	Stdout string
	Stderr string
	// Skipped is true when the command did not run because of --dry-run.
	Skipped bool
}

// RunOptions are the per-command overrides.
type RunOptions struct {
	// Env is merged OVER the runner's base env for this command only.
	Env map[string]string
	// Label is what a non-verbose dry run prints instead of the command.
	Label string
}

// Runner runs shell strings. The one seam every test fakes.
type Runner interface {
	Run(cmd string, o RunOptions) Result
	DryRun() bool
	// Cwd is the directory commands run in — the deployment directory for a registry-driven run,
	// empty for the CLI (commands run from the shell's directory, as typing them there would).
	// Exposed because compose needs it: pstack writes a derived compose file next to the submitted
	// one and passes that to -f.
	Cwd() string
	// Context is done when the job was cancelled: the CURRENT command is SIGTERMed and every later
	// one is refused.
	Context() context.Context
}

// Level is how chatty a runner is.
type Level string

const (
	Quiet   Level = "quiet"
	Normal  Level = "normal"
	Verbose Level = "verbose"
)

// Options configure a real runner.
type Options struct {
	DryRun bool
	Level  Level
	Cwd    string
	// BaseEnv is the WHOLE environment a command sees (a replacement, never an overlay on the
	// process env — the caller includes PATH). Nil means the process environment.
	BaseEnv map[string]string
	Ctx     context.Context
	// Out is where dry-run and verbose lines go (stdout when nil).
	Out io.Writer
}

type runner struct {
	o Options
}

// New returns the real runner.
func New(o Options) Runner {
	if o.Level == "" {
		o.Level = Normal
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.BaseEnv == nil {
		o.BaseEnv = Environ()
	}
	return &runner{o: o}
}

func (r *runner) DryRun() bool             { return r.o.DryRun }
func (r *runner) Cwd() string              { return r.o.Cwd }
func (r *runner) Context() context.Context { return r.o.Ctx }

func (r *runner) Run(cmd string, o RunOptions) Result {
	label := o.Label
	if label == "" {
		label = cmd
	}
	// Refuse AFTER a cancel as firmly as during one. Teardown is best-effort and keeps going past
	// failures (see stack), so without this check a cancelled `down` would carry on running every
	// remaining axis hook — the operator pressed stop and watched it continue.
	if r.o.Ctx.Err() != nil {
		return Result{OK: false, Code: 130, Stdout: "", Stderr: "cancelled"}
	}
	if r.o.DryRun {
		// Verbose shows the REAL command, not just the label. A dry-run exists to answer "what
		// exactly would run", and a label like `compose up` cannot answer it.
		if r.o.Level == Verbose {
			fmt.Fprintf(r.o.Out, "  [dry-run] $ %s\n", cmd)
		} else if r.o.Level != Quiet {
			fmt.Fprintf(r.o.Out, "  [dry-run] %s\n", label)
		}
		return Result{OK: true, Code: 0, Skipped: true}
	}
	if r.o.Level == Verbose {
		fmt.Fprintf(r.o.Out, "  $ %s\n", cmd)
	}

	c := osexec.CommandContext(r.o.Ctx, "bash", "-c", cmd)
	c.Dir = r.o.Cwd
	c.Env = EnvList(Merge(r.o.BaseEnv, o.Env))
	// SIGTERM first, so a hook can trap it and clean up. The default would be SIGKILL, which a bash
	// trap never sees. No process group: Bun did not kill one either, and that ceiling is parity.
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	// cmd.Run drains both pipes concurrently with waiting — a hook that prints more than a pipe
	// buffer on either stream cannot deadlock it.
	err := c.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*osexec.ExitError); ok {
			code = ee.ExitCode()
			if code == -1 {
				code = 130 // killed by a signal
			}
		} else {
			// bash itself could not start: report it as a failed command, not a crash.
			return Result{OK: false, Code: 127, Stdout: stdout.String(), Stderr: err.Error()}
		}
	}
	if r.o.Ctx.Err() != nil {
		if code == 0 {
			code = 130
		}
		se := stderr.String()
		if se == "" {
			se = "cancelled"
		}
		return Result{OK: false, Code: code, Stdout: stdout.String(), Stderr: se}
	}
	if r.o.Level == Verbose {
		if strings.TrimSpace(stdout.String()) != "" {
			fmt.Fprintln(r.o.Out, Indent(stdout.String()))
		}
		if strings.TrimSpace(stderr.String()) != "" {
			fmt.Fprintln(r.o.Out, Indent(stderr.String()))
		}
	}
	return Result{OK: code == 0, Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// Indent prefixes every line with `    | `, the verbose transcript shape.
func Indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, " \t\n\r"), "\n")
	for i, l := range lines {
		lines[i] = "    | " + l
	}
	return strings.Join(lines, "\n")
}

// Environ is the process environment as a map.
func Environ() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// Merge overlays b on a into a new map (a later map wins), skipping nothing.
func Merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// EnvList renders a map as KEY=VALUE pairs, sorted so a test can compare them.
func EnvList(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

var outputLine = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(.*)$`)

// CaptureOutputs collects `KEY=VALUE` lines from a provision hook's stdout, in order of first
// appearance (a repeated key keeps its position and takes the last value — JS object semantics).
//
// This is how a provisioned resource hands its coordinates to the rest of the stack — a freshly
// created database emits `DATABASE_URL=postgres://…` and compose picks it up as an env var. Only
// SHOUT_CASE keys are captured so ordinary log chatter is ignored.
func CaptureOutputs(stdout string) (keys []string, values map[string]string) {
	values = map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		m := outputLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if _, seen := values[m[1]]; !seen {
			keys = append(keys, m[1])
		}
		values[m[1]] = m[2]
	}
	if keys == nil {
		keys = []string{}
	}
	return keys, values
}

// MaskSecrets replaces values that look like credentials with ***. Best-effort, not a security
// boundary: only values of 8+ characters, so a short token does not shred every log line.
func MaskSecrets(text string, secrets []string) string {
	out := text
	for _, s := range secrets {
		if s != "" && len(s) >= 8 {
			out = strings.ReplaceAll(out, s, "***")
		}
	}
	return out
}

// Fake is the test runner: records every command, answers from a script, never spawns.
//
// Owner: the test. The mutex is only there so a job goroutine and the asserting test may touch Log.
type Fake struct {
	mu      sync.Mutex
	Log     []string
	Fail    func(cmd string) bool
	Stdout  string
	Answer  func(cmd string) (Result, bool)
	dryRun  bool
	cwd     string
	ctx     context.Context
	Verbose bool
}

// NewFake returns a runner whose every command succeeds with `stdout`, except those `fail` names.
func NewFake(fail func(cmd string) bool, stdout string) *Fake {
	return &Fake{Log: []string{}, Fail: fail, Stdout: stdout, ctx: context.Background()}
}

// WithCwd sets the cwd a fake reports.
func (f *Fake) WithCwd(dir string) *Fake { f.cwd = dir; return f }

// WithDryRun flips the dry-run flag a fake reports.
func (f *Fake) WithDryRun(v bool) *Fake { f.dryRun = v; return f }

// WithContext sets the cancellation context.
func (f *Fake) WithContext(ctx context.Context) *Fake { f.ctx = ctx; return f }

func (f *Fake) DryRun() bool             { return f.dryRun }
func (f *Fake) Cwd() string              { return f.cwd }
func (f *Fake) Context() context.Context { return f.ctx }

// Run records and answers.
func (f *Fake) Run(cmd string, o RunOptions) Result {
	f.mu.Lock()
	f.Log = append(f.Log, strings.TrimSpace(cmd))
	f.mu.Unlock()
	if f.Answer != nil {
		if r, ok := f.Answer(cmd); ok {
			return r
		}
	}
	bad := f.Fail != nil && f.Fail(cmd)
	if bad {
		return Result{OK: false, Code: 1, Stdout: f.Stdout, Stderr: "boom"}
	}
	return Result{OK: true, Code: 0, Stdout: f.Stdout}
}

// Commands is a copy of the log.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Log))
	copy(out, f.Log)
	return out
}
