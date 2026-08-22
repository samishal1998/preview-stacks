package api

import (
	"bufio"
	"io"
	"net/http"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
)

// sseWriter serialises writes to one event stream: four callbacks (replay, live events, the end
// frame, the keepalive) wrote the Bun stream from one thread; here they are goroutines.
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	flush  http.Flusher
	closed bool
}

func newSSE(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("content-type", "text/event-stream")
	h.Set("cache-control", "no-cache")
	h.Set("connection", "keep-alive")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return &sseWriter{w: w, flush: f}
}

// send writes one `data:` frame. Returns false once the peer is gone.
func (s *sseWriter) send(v any) bool {
	return s.raw("data: " + string(jsonx.Must(v)) + "\n\n")
}

func (s *sseWriter) raw(frame string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, err := io.WriteString(s.w, frame); err != nil {
		s.closed = true
		return false
	}
	if s.flush != nil {
		s.flush.Flush()
	}
	return true
}

func (s *sseWriter) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// jobStream is GET /api/jobs/:id/stream: replay the buffered log, then stream live until the job
// ends. Subscribe hands back the replay and the state in ONE critical section, so a stream that
// opens between the last event and the job's end sends `done` immediately.
func (s *Server) jobStream(w http.ResponseWriter, r *http.Request, job jobs.Job) {
	s.streams.Add(1)
	defer s.streams.Done()
	out := newSSE(w)
	// Live events are queued by the job's goroutine and written by this one, AFTER the replay —
	// a line that arrived mid-replay must not be written ahead of the lines before it.
	var qmu sync.Mutex
	var queue []log.Event
	wake := make(chan struct{}, 1)
	replay, state, off, ok := s.jobs.Subscribe(job.ID, func(e log.Event) {
		qmu.Lock()
		queue = append(queue, e)
		qmu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if !ok {
		out.send(jsonx.O("done", true, "state", job.State))
		return
	}
	defer off()
	for _, e := range replay {
		out.send(e)
	}
	if state != jobs.Running {
		out.send(jsonx.O("done", true, "state", state))
		return
	}
	drain := func() (ended bool) {
		qmu.Lock()
		batch := queue
		queue = nil
		qmu.Unlock()
		for _, e := range batch {
			if e.Message == "__end__" && e.Seq < 0 {
				st := job.State
				if j, ok := s.jobs.Get(job.ID); ok {
					st = j.State
				}
				out.send(jsonx.O("done", true, "state", st))
				return true
			}
			out.send(e)
		}
		return false
	}
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-wake:
			if drain() {
				return
			}
		case <-keep.C:
			if !out.raw(": keepalive\n\n") {
				return
			}
		case <-r.Context().Done():
			// The client disconnected: drop the subscription NOW (deferred off), not when the job ends.
			out.close()
			return
		case <-s.ctx.Done():
			out.close()
			return
		}
	}
}

// logStream is GET …/logs/stream: one `compose logs --follow` per connection, killed when the
// client disconnects, when the server stops, and after a hard one-hour cap.
func (s *Server) logStream(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, vars map[string]string) error {
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	q := r.URL.RawQuery
	svc, hasSvc := query(q, "service")
	if hasSvc && !serviceRe.MatchString(svc) {
		writeError(w, 400, `"`+svc+`" is not a valid compose service name`)
		return nil
	}
	tail := clamp(numParam(q, "tail", 200), 1, 2000, 200)
	ts, _ := query(q, "timestamps")
	built, err := compose.ComposeLogsCommand(st, s.runnerFor(st, dep.Dir, nil), tail, svc, compose.LogsOptions{Follow: true, Timestamps: ts == "1"})
	if err != nil {
		return err
	}
	if built == nil {
		writeError(w, 400, "this spec has no compose section")
		return nil
	}
	secrets := append([]string{s.opts.Token}, s.secretValues()...)

	cmd := osexec.Command("bash", "-c", built.Cmd)
	cmd.Dir = dep.Dir
	cmd.Env = exec.EnvList(exec.Merge(s.env, st.Env, built.Env))
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.streams.Add(1)
	defer s.streams.Done()
	out := newSSE(w)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		})
	}
	s.followMu.Lock()
	s.followSeq++
	fid := s.followSeq
	s.followers[fid] = stop
	s.followMu.Unlock()
	defer func() {
		s.followMu.Lock()
		delete(s.followers, fid)
		s.followMu.Unlock()
	}()

	// Pumps split on newlines, keeping the partial last line. Redacted PER LINE, before it leaves
	// the host — the same contract the fetched read has.
	var pumps sync.WaitGroup
	pump := func(rd io.Reader, level string) {
		defer pumps.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			if !out.send(jsonx.O("level", level, "line", redact.RedactText(sc.Text(), secrets...))) {
				stop()
			}
		}
	}
	pumps.Add(2)
	go pump(stdout, "info")
	go pump(stderr, "error")

	exited := make(chan error, 1)
	go func() {
		// Rule 15: pumps to EOF, then Wait, then the terminal frame.
		pumps.Wait()
		exited <- cmd.Wait()
	}()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	capTimer := time.NewTimer(time.Hour)
	defer capTimer.Stop()
	finish := func(reason string) {
		out.send(jsonx.O("done", true, "reason", reason))
		stop()
		out.close()
	}
	for {
		select {
		case err := <-exited:
			code := 0
			if ee, ok := err.(*osexec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				code = -1
			}
			if code == 0 {
				finish("compose stopped following")
			} else {
				finish("compose exited (" + jsonx.NumberString(float64(code)) + ")")
			}
			return nil
		case <-capTimer.C:
			finish("reached the one-hour follow limit")
			<-exited
			return nil
		case <-keep.C:
			if !out.raw(": keepalive\n\n") {
				stop()
			}
		case <-r.Context().Done():
			out.close()
			stop()
			<-exited
			return nil
		case <-s.ctx.Done():
			out.close()
			stop()
			<-exited
			return nil
		}
	}
}
