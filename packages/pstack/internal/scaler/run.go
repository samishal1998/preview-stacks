package scaler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// RunOptions is what `pstack scaler` hands Run. API.HTTP must carry a timeout (Go rule 12).
type RunOptions struct {
	API      Client
	StateDir string
	Log      io.Writer
	tick     time.Duration // zero: Tick; tests shorten it so an apply outlasts it
}

// Run is the daemon: lock the state dir, make sure Tofu is there, then tick at once and every
// Tick until ctx is done. ctx is read only between ticks, so a tick in progress — an apply —
// finishes first, and none starts after cancel (spec, SIGTERM row): an apply longer than Tick
// leaves t.C ready beside ctx.Done(), and select picks either. A tick's error is logged, not fatal: the next tick retries.
func Run(ctx context.Context, o RunOptions) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return err
	}
	release, err := Lock(o.StateDir)
	if err != nil {
		return err
	}
	defer release()
	bin, err := EnsureTofu(filepath.Join(o.StateDir, "bin"), &http.Client{Timeout: 10 * time.Minute})
	if err != nil {
		return err
	}
	l, err := New(Options{API: o.API, StateDir: o.StateDir, Tofu: bin, Log: o.Log})
	if err != nil {
		return err
	}
	if o.tick == 0 {
		o.tick = Tick
	}
	t := time.NewTicker(o.tick)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := l.Tick(); err != nil {
			fmt.Fprintf(o.Log, "scaler: %s\n", l.scrub(err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
