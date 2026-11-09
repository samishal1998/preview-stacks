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
	stateDir := t.TempDir()
	// The fake where EnsureTofu looks, so it never downloads (W-R26).
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{API: Client{Base: srv.URL, Token: "root", HTTP: srv.Client()}, StateDir: stateDir})
	}()
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
