package stack

import (
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

func parse(t *testing.T, yaml string) *spec.Stack {
	t.Helper()
	st, err := spec.Parse("version: 1\n"+yaml, map[string]string{"PR": "7", "PATH": os.Getenv("PATH")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func cmds(f *exec.Fake) string { return strings.Join(f.Commands(), "|") }

const two = "stack: pr-${PR}\naxes:\n  - name: first\n    up: echo one\n  - name: second\n    up: echo two\n"
const three = "stack: s\naxes:\n  - name: db\n    down: echo rm-db\n    assert_gone: \"true\"\n  - name: queue\n    down: echo rm-queue\n    assert_gone: \"true\"\n  - name: images\n    down: echo rm-images\n    assert_gone: \"true\"\n"

func TestUp(t *testing.T) {
	t.Run("provisions in declaration order", func(t *testing.T) {
		// negative control: range st.Axes backwards in Up — the log reads two|one.
		f := exec.NewFake(nil, "")
		Up(parse(t, two), f, log.Null{})
		if cmds(f) != "echo one|echo two" {
			t.Errorf("got %s", cmds(f))
		}
	})
	t.Run("fails fast — a failed axis stops the rest and never reaches compose", func(t *testing.T) {
		// negative control: drop the `return fail()` after a failed up — "echo two" runs.
		f := exec.NewFake(func(c string) bool { return strings.Contains(c, "one") }, "")
		out := Up(parse(t, two), f, log.Null{})
		if out.OK || cmds(f) != "echo one" {
			t.Errorf("ok=%v cmds=%s", out.OK, cmds(f))
		}
	})
	t.Run("captures KEY=VALUE from a provision hook into later env, in order", func(t *testing.T) {
		// negative control: skip the CaptureOutputs call — the second hook sees no DB_URL.
		f := exec.NewFake(nil, "")
		f.Answer = func(c string) (exec.Result, bool) {
			if strings.Contains(c, "make") {
				return exec.Result{OK: true, Stdout: "noise\nDB_URL=postgres://x\nlower=ignored\nB=2\n"}, true
			}
			return exec.Result{}, false
		}
		out := Up(parse(t, "stack: s\naxes:\n  - name: db\n    up: echo make\n  - name: app\n    up: echo \"$DB_URL\"\n"), f, log.Null{})
		if !out.OK || out.Outputs["DB_URL"] != "postgres://x" || strings.Join(out.OutputKeys, ",") != "DB_URL,B" {
			t.Errorf("got %+v", out)
		}
		if _, has := out.Outputs["lower"]; has {
			t.Error("only SHOUT_CASE keys are captured")
		}
	})
	t.Run("assert_live failure fails the up", func(t *testing.T) {
		// negative control: ignore the assert_live result — the up reports ok.
		f := exec.NewFake(func(c string) bool { return strings.TrimSpace(c) == "false" }, "")
		out := Up(parse(t, "stack: s\naxes:\n  - name: db\n    up: echo made\n    assert_live: \"false\"\n"), f, log.Null{})
		if out.OK || out.Steps[len(out.Steps)-1].Phase != PhaseAssertLive || *out.Steps[len(out.Steps)-1].Message != "provisioned but assert_live failed — resource missing?" {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("requires run before any axis and block up when unmet, by name and with the hint", func(t *testing.T) {
		// negative control: run requires after the axes — "echo made" appears in the log.
		f := exec.NewFake(func(c string) bool { return strings.Contains(c, "probe") }, "")
		out := Up(parse(t, "stack: s\nrequires:\n  - name: ingress\n    assert: probe net\n    hint: run pstack init\naxes:\n  - name: db\n    up: echo made\n"), f, log.Null{})
		if out.OK || cmds(f) != "probe net" || out.Steps[0].Phase != PhaseRequires || *out.Steps[0].Message != "unmet — run pstack init" {
			t.Errorf("got %+v / %s", out, cmds(f))
		}
	})
}

func TestDown(t *testing.T) {
	t.Run("destroys in REVERSE declaration order", func(t *testing.T) {
		// negative control: iterate forwards — rm-db first.
		f := exec.NewFake(nil, "")
		Down(parse(t, three), f, DownOptions{NoVerify: true}, log.Null{})
		if cmds(f) != "echo rm-images|echo rm-queue|echo rm-db" {
			t.Errorf("got %s", cmds(f))
		}
	})
	t.Run("is best-effort: one failing destroy does not abort the others", func(t *testing.T) {
		// negative control: return on the first !res.OK — rm-db never runs and ok flips.
		f := exec.NewFake(func(c string) bool { return strings.Contains(c, "rm-queue") }, "")
		out := Down(parse(t, three), f, DownOptions{NoVerify: true}, log.Null{})
		if cmds(f) != "echo rm-images|echo rm-queue|echo rm-db" || !out.OK {
			t.Errorf("got ok=%v %s", out.OK, cmds(f))
		}
		found := false
		for _, s := range out.Steps {
			if s.Message != nil && strings.HasPrefix(*s.Message, "non-fatal") {
				found = true
			}
		}
		if !found {
			t.Error("the failure is recorded")
		}
	})
	t.Run("REFUSES a shared deployment without force, proceeds with it", func(t *testing.T) {
		// negative control: drop the Kind check — the shared stack is torn down.
		shared := "kind: shared\nstack: db\ncompose:\n  file: c.yml\n"
		f := exec.NewFake(nil, "")
		out := Down(parse(t, shared), f, DownOptions{}, log.Null{})
		if out.OK || len(f.Commands()) != 0 || !strings.HasPrefix(*out.Steps[0].Message, "refused: kind is `shared`") {
			t.Errorf("got %+v %v", out, f.Commands())
		}
		forced := Down(parse(t, shared), f, DownOptions{Force: true}, log.Null{})
		if !forced.OK || len(f.Commands()) == 0 {
			t.Errorf("forced: %+v %v", forced, f.Commands())
		}
	})
	t.Run("does not mutate the spec's axis order", func(t *testing.T) {
		// negative control: reverse st.Axes in place — the second Down runs forwards.
		st := parse(t, three)
		f := exec.NewFake(nil, "")
		Down(st, f, DownOptions{NoVerify: true}, log.Null{})
		Down(st, f, DownOptions{NoVerify: true}, log.Null{})
		if cmds(f) != "echo rm-images|echo rm-queue|echo rm-db|echo rm-images|echo rm-queue|echo rm-db" {
			t.Errorf("got %s", cmds(f))
		}
	})
}

func TestVerifyTheLeakGate(t *testing.T) {
	t.Run("a surviving resource makes verify fail", func(t *testing.T) {
		// negative control: set OK: true on the step regardless — the leak passes.
		f := exec.NewFake(func(c string) bool { return strings.TrimSpace(c) == "false" }, "")
		out := Verify(parse(t, "stack: s\naxes:\n  - name: db\n    down: \"true\"\n    assert_gone: \"false\"\n"), f, log.Null{})
		if out.OK || !strings.Contains(*out.Steps[0].Message, "LEAKED") || !out.Leaked() {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("an axis with no assert_gone is reported unverifiable, not silently passed", func(t *testing.T) {
		// negative control: skip axes without assert_gone — the step disappears.
		out := Verify(parse(t, "stack: s\naxes:\n  - name: db\n    down: \"true\"\n"), exec.NewFake(nil, ""), log.Null{})
		if !out.OK || !out.Steps[0].Skipped || !strings.HasPrefix(*out.Steps[0].Message, "unverifiable") || out.Leaked() {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("down runs verify by default and surfaces the leak", func(t *testing.T) {
		// negative control: default NoVerify to true — the leak is never checked.
		f := exec.NewFake(func(c string) bool { return strings.TrimSpace(c) == "false" }, "")
		out := Down(parse(t, "stack: s\naxes:\n  - name: db\n    down: \"true\"\n    assert_gone: \"false\"\n"), f, DownOptions{}, log.Null{})
		if out.OK || !out.Leaked() {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("end-to-end leak detection against the real filesystem", func(t *testing.T) {
		// negative control: make the down hook actually remove the file — verify then passes and the test fails.
		dir := t.TempDir()
		file := filepath.Join(dir, "leaked")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := parse(t, "stack: s\naxes:\n  - name: f\n    down: \"true\"\n    assert_gone: \"! test -e "+file+"\"\n")
		r := exec.New(exec.Options{Level: exec.Quiet})
		if out := Down(st, r, DownOptions{}, log.Null{}); out.OK || !out.Leaked() {
			t.Fatalf("the lying down must be caught: %+v", out)
		}
		_ = os.Remove(file)
		if out := Verify(st, r, log.Null{}); !out.OK {
			t.Fatalf("gone now: %+v", out)
		}
	})
}

func TestSleepAndReport(t *testing.T) {
	// negative control: make Sleep run Verify — the assert_gone step appears in a sleep outcome.
	f := exec.NewFake(nil, "")
	st := parse(t, "stack: s\ncompose:\n  file: c.yml\n  profiles: [a]\naxes:\n  - name: db\n    down: echo rm\n    assert_gone: \"false\"\n")
	out := Sleep(st, f, log.Null{})
	if !out.OK || len(out.Steps) != 1 || out.Steps[0].Axis != "(compose)" || cmds(f) != "docker compose -p 's' -f 'c.yml' --profile 'a' down --remove-orphans" {
		t.Errorf("got %+v %s", out, cmds(f))
	}
	shared := Sleep(parse(t, "kind: shared\nstack: db\ncompose:\n  file: c.yml\n"), f, log.Null{})
	if shared.OK || !strings.HasPrefix(*shared.Steps[0].Message, "refused: kind is `shared`") {
		t.Errorf("shared: %+v", shared)
	}
	rep := Report(Outcome{Steps: []StepResult{{Axis: "db", Phase: PhaseAssertGone, OK: false, Code: 1, Message: msg("LEAKED: resource still present after teardown")}, {Axis: "q", Phase: PhaseAssertGone, OK: true, Skipped: true, Message: msg("unverifiable: no assert_gone defined")}}})
	want := "  ✗ assert_gone  db  — LEAKED: resource still present after teardown\n  ? assert_gone  q  — unverifiable: no assert_gone defined\n  1 leaked resource(s), 1 unverifiable axis/axes"
	if rep != want {
		t.Errorf("report:\n%s\nwant\n%s", rep, want)
	}
}

// negative control: drop Outcome.MarshalJSON → encoding/json sorts the keys (A before Z).
func TestOutcomeOutputsKeepAccumulationOrder(t *testing.T) {
	o := Outcome{OK: true, Steps: []StepResult{}, Outputs: map[string]string{"Z": "1", "A": "2", "M": "3"}, OutputKeys: []string{"Z", "A"}}
	b, err := jsonx.Marshal(o)
	if err != nil || string(b) != `{"ok":true,"steps":[],"outputs":{"Z":"1","A":"2","M":"3"}}` {
		t.Fatalf("%s %v", b, err)
	}
}
