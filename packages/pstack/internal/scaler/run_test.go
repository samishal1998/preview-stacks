package scaler

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunFinishesTheTickOnCancel(t *testing.T) {
	// negative control: run Tick in a goroutine and return on ctx.Done() → Run returns mid-apply,
	// before the fake writes state.json or the worker records AppliedAt, and this fails
	h := newHarness(t)
	h.stuck("pr-42_api", fits)
	if err := os.WriteFile(filepath.Join(h.tofuDir, "sleep"), []byte("2s"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir, opts := runOptions(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	deadline := time.Now().Add(30 * time.Second)
	for h.applies() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no apply started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if _, err := os.Stat(filepath.Join(h.tofuDir, "state.json")); err != nil {
		t.Errorf("the apply did not finish: %v", err)
	}
	s, err := LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Workers) != 1 || s.Workers[0].AppliedAt == 0 || s.Workers[0].ApplyingAt != 0 {
		t.Errorf("workers = %+v, want one applied", s.Workers)
	}
}

func TestRunStartsNoTickAfterCancel(t *testing.T) {
	// negative control: drop the ctx.Err() check at the top of Run's loop → the first tick runs
	// on a cancelled ctx, starts an apply, and this fails. That check is what stops the extra
	// tick when an apply outlasts Tick and select picks t.C over ctx.Done().
	h := newHarness(t)
	h.stuck("pr-42_api", fits)
	_, opts := runOptions(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := h.applies(); n != 0 {
		t.Errorf("applies = %d after cancel, want 0", n)
	}
}

func TestRunStartsNoTickAfterAnApplyLongerThanTheTick(t *testing.T) {
	// negative control: drop the ctx.Err() check at the top of Run's loop → when the apply ends,
	// t.C and ctx.Done() are both ready, select picks t.C about half the time, a second tick reads
	// /api/signals, and this fails. Eight runs miss that 1 time in 256.
	for i := range 8 {
		h := newHarness(t)
		h.stuck("pr-42_api", fits)
		if err := os.WriteFile(filepath.Join(h.tofuDir, "sleep"), []byte("300ms"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, opts := runOptions(t, h)
		opts.tick = 20 * time.Millisecond

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, opts) }()
		deadline := time.Now().Add(30 * time.Second)
		for h.applies() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("no apply started")
			}
			time.Sleep(5 * time.Millisecond)
		}
		var reads int
		h.with(func(a *fakeAPI) { reads = a.sigReads })
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
		h.with(func(a *fakeAPI) {
			if a.sigReads != reads {
				t.Errorf("run %d: signals read %d more times after cancel, want 0 (a new tick)", i, a.sigReads-reads)
			}
		})
	}
}

// runOptions places the fake where EnsureTofu looks, so it never downloads (W-R26), and serves
// the harness API.
func runOptions(t *testing.T, h *harness) (string, RunOptions) {
	t.Helper()
	stateDir := t.TempDir()
	script, err := os.ReadFile(h.tofu)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(stateDir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tofu-"+TofuVersion), script, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.api)
	t.Cleanup(srv.Close)
	return stateDir, RunOptions{API: Client{Base: srv.URL, Token: "root", HTTP: srv.Client()}, StateDir: stateDir}
}
