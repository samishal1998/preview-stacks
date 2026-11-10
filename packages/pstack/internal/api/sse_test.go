package api

import (
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
)

// A log line over the scanner's 16 MiB cap must end the stream, not leave it on keepalives.
// negative control: drop the `if sc.Err() != nil { stop() }` after the scan loop → compose is never
// stopped, no `done` frame arrives and this times out.
func TestLogStreamEndsOnAnOverlongLine(t *testing.T) {
	if _, err := osexec.LookPath("python3"); err != nil {
		t.Skip("python3 writes the line")
	}
	bin := t.TempDir()
	// `logs` is one process (exec), so SIGTERM closes the pipes: compose's own shape. Every other
	// docker call answers empty.
	script := "#!/bin/sh\ncase \"$*\" in *' logs '*) ;; *) exit 0 ;; esac\n" +
		"exec python3 -c \"import sys,time; sys.stdout.write('a'*(17<<20)+'\\n'); sys.stdout.flush(); time.sleep(30)\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DataDir: t.TempDir(), Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {},
		Env: exec.Merge(exec.Environ(), map[string]string{"PATH": bin + ":" + os.Getenv("PATH")})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.host = exec.NewFake(nil, "")
	crDeploy(t, s, "pr-1", "myapp-pr-1", crCompose)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/deployments/pr-1/logs/stream", nil)
	req.Header.Set("authorization", "Bearer t0ken")
	done := make(chan struct{})
	go func() { s.handle(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the stream did not end")
	}
	if body := rec.Body.String(); !strings.Contains(body, `"done": true`) && !strings.Contains(body, `"done":true`) {
		t.Fatalf("no done frame: %.300s", body)
	}
}
