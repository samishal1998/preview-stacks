package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scaler"
)

// countingAPI answers 500 to everything and counts the requests.
func countingAPI(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestScalerCmdNeedsToken(t *testing.T) {
	// negative control: read PSTACK_TOKEN after scaler.Run starts → the state dir gains scaler.lock
	// and this fails, or the exit is 1, not 3
	srv, n := countingAPI(t)
	dir := t.TempDir()
	code, _, errOut := runForward(t, map[string]string{"PSTACK_API_URL": srv.URL, "PSTACK_SCALER_STATE": dir}, "scaler")
	if code != ExitUsage || !strings.Contains(errOut, "PSTACK_TOKEN") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if n.Load() != 0 {
		t.Errorf("%d requests", n.Load())
	}
	if e, _ := os.ReadDir(dir); len(e) != 0 {
		t.Errorf("state dir touched: %v", e)
	}
}

func TestScalerCmdSecondInstance(t *testing.T) {
	// negative control: take the lock after EnsureTofu → the second instance downloads OpenTofu
	// (or fails on it) instead of naming the holder, and this fails
	srv, n := countingAPI(t)
	dir := t.TempDir()
	release, err := scaler.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, _, errOut := runForward(t, map[string]string{"PSTACK_API_URL": srv.URL, "PSTACK_TOKEN": "root", "PSTACK_SCALER_STATE": dir}, "scaler")
	if code != ExitFailed || !strings.Contains(errOut, "another pstack scaler holds "+dir) {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if n.Load() != 0 {
		t.Errorf("%d requests", n.Load())
	}
}
