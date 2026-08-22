package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunsACommandAndCapturesOutput(t *testing.T) {
	// negative control: return Result{} from Run — OK is false and Stdout empty.
	r := New(Options{Level: Quiet})
	res := r.Run("echo hi; echo err 1>&2; exit 0", RunOptions{})
	if !res.OK || res.Code != 0 || res.Stdout != "hi\n" || res.Stderr != "err\n" {
		t.Errorf("got %+v", res)
	}
}

func TestNonZeroExitIsReportedNotThrown(t *testing.T) {
	// negative control: treat every exit as OK — Code 3 would read ok:true.
	r := New(Options{Level: Quiet})
	res := r.Run("exit 3", RunOptions{})
	if res.OK || res.Code != 3 || res.Skipped {
		t.Errorf("got %+v", res)
	}
}

func TestDryRunExecutesNothing(t *testing.T) {
	// negative control: drop the DryRun branch — the file appears.
	var out bytes.Buffer
	dir := t.TempDir()
	r := New(Options{DryRun: true, Level: Verbose, Out: &out})
	res := r.Run("touch "+dir+"/ran", RunOptions{Label: "touch it"})
	if !res.Skipped || !res.OK {
		t.Errorf("got %+v", res)
	}
	if !strings.Contains(out.String(), "[dry-run] $ touch") {
		t.Errorf("verbose dry-run must print the real command, got %q", out.String())
	}
	r2 := New(Options{DryRun: true, Level: Normal, Out: &out})
	r2.Run("touch "+dir+"/ran", RunOptions{Label: "touch it"})
	if !strings.Contains(out.String(), "[dry-run] touch it") {
		t.Errorf("normal dry-run prints the label, got %q", out.String())
	}
}

func TestEnvIsAReplacementNotAnOverlay(t *testing.T) {
	// negative control: set c.Env = nil in Run — the inherited HOME leaks through.
	r := New(Options{Level: Quiet, BaseEnv: map[string]string{"PATH": "/usr/bin:/bin", "STACK": "pr-1"}})
	res := r.Run("echo \"$STACK|$HOME|$EXTRA\"", RunOptions{Env: map[string]string{"EXTRA": "x"}})
	if res.Stdout != "pr-1||x\n" {
		t.Errorf("got %q", res.Stdout)
	}
}

func TestCancelSendsSIGTERMAndRefusesLaterCommands(t *testing.T) {
	// negative control: remove c.Cancel — bash gets SIGKILL, the trap never prints, Stdout is empty.
	ctx, cancel := context.WithCancel(context.Background())
	r := New(Options{Level: Quiet, Ctx: ctx})
	done := make(chan Result, 1)
	go func() {
		done <- r.Run(`trap 'echo trapped; exit 7' TERM; sleep 2 & wait`, RunOptions{})
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.OK {
			t.Errorf("a cancelled command must not be ok: %+v", res)
		}
		if !strings.Contains(res.Stdout, "trapped") {
			t.Errorf("the hook's TERM trap must run (SIGTERM, not SIGKILL): %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the command")
	}
	// After the cancel, nothing runs at all.
	after := r.Run("echo nope", RunOptions{})
	if after.OK || after.Code != 130 || after.Stderr != "cancelled" || after.Stdout != "" {
		t.Errorf("post-cancel run should be refused: %+v", after)
	}
}

func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	// negative control: read stdout to completion BEFORE starting stderr — past 64KB on stderr it hangs.
	r := New(Options{Level: Quiet})
	res := r.Run("head -c 300000 /dev/zero | tr '\\0' a; head -c 300000 /dev/zero | tr '\\0' b 1>&2", RunOptions{})
	if !res.OK || len(res.Stdout) != 300000 || len(res.Stderr) != 300000 {
		t.Errorf("got ok=%v out=%d err=%d", res.OK, len(res.Stdout), len(res.Stderr))
	}
}

func TestCaptureOutputs(t *testing.T) {
	// negative control: drop the SHOUT_CASE anchor in outputLine — `lower=1` is captured.
	keys, vals := CaptureOutputs("noise\nDB_URL=postgres://x\n  PORT=5432 \nlower=1\nDB_URL=again\n")
	if strings.Join(keys, ",") != "DB_URL,PORT" || vals["DB_URL"] != "again" || vals["PORT"] != "5432" {
		t.Errorf("keys=%v vals=%v", keys, vals)
	}
	if k, _ := CaptureOutputs(""); k == nil {
		t.Error("keys must never be nil")
	}
}

func TestMaskSecrets(t *testing.T) {
	// negative control: drop the length guard — "ab" masks every line.
	if got := MaskSecrets("token=supersecret12 short=ab", []string{"supersecret12", "ab", ""}); got != "token=*** short=ab" {
		t.Errorf("got %q", got)
	}
}
