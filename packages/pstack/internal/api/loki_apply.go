// The loki-apply job: Loki's saved settings, onto control/loki and the loki container.
//
// ONE JOB ON THE CONTROL PROJECT'S KEY (inspect.ControlProject). Applies run one at a time, one
// waits, and each counts against PSTACK_MAX_JOBS. A save's job and a boot job are the same job.
//
// PENDING PATCHES. A PUT stores its section's patch with a generation under lokiMu, then starts a job
// that holds only that number. The job takes every patch at or below it. jobs.Start keeps one waiting
// job per key and supersedes the rest, so a superseded save is carried by its successor, and a running
// job never takes a newer save.
//
// THE STEPS. find → render → verify → commit → swap → restart → ready → finish → log, then
// logging.changed when the row changed. Nothing live changes before the swap: a failure up to the
// commit removes the .next files and leaves the row. The row is saved with previous_* BEFORE the swap
// and finished after ready, so a pstack that dies in between leaves its own undo record.
//
// THE POST RUNNER. From the swap on, commands run on a runner with its own 2×lead deadline. It is not
// the job's runner, which refuses every command once cancelled (exec.go:110), and not s.host, which
// Stop cancels. A cancel after the swap still restarts and waits.
//
// SCRUBBING. The secret, the previous secret and the key id are known only once the patches are
// merged. One scrub serves the sink and the record, and it reads them when it is called, on the job's
// goroutine.
package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scheduler"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
)

// lokiEntry is one pending Loki save: a section's patch and who sent it. gen orders the saves; a
// job takes every entry with gen <= its own, so a superseded save is carried, never lost.
type lokiEntry struct {
	gen     uint64
	by      string
	chunks  *loki.ChunksPatch
	storage *loki.StoragePatch
}

// lokiLead is the period guard's lead: one restart plus a ready wait, with slack.
func (s *Server) lokiLead() time.Duration {
	return loki.Lead(time.Duration(s.opts.LokiReadyTimeoutMs) * time.Millisecond)
}

// lokiService is the compose service name init gives Loki.
const lokiService = "loki"

// The apply's step phases. They are local because package stack's phases belong to the lifecycle
// hooks. None of these is PhaseAssertGone, so Outcome.Leaked() never fires for an apply.
const (
	phaseFind    stack.Phase = "find"
	phaseRender  stack.Phase = "render"
	phaseVerify  stack.Phase = "verify"
	phaseCommit  stack.Phase = "commit"
	phaseSwap    stack.Phase = "swap"
	phaseRestart stack.Phase = "restart"
	phaseReady   stack.Phase = "ready"
	phaseFinish  stack.Phase = "finish"
	phaseLog     stack.Phase = "log"
)

// lokiPut makes a save's patch its section's pending entry and returns the entry's gen. A newer save
// of the same section replaces the entry, because a PUT body is the whole section.
func (s *Server) lokiPut(by string, c *loki.ChunksPatch, sp *loki.StoragePatch) uint64 {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	s.lokiGen++
	e := &lokiEntry{gen: s.lokiGen, by: by, chunks: c, storage: sp}
	if c != nil {
		s.lokiChunks = e
	}
	if sp != nil {
		s.lokiStorage = e
	}
	return e.gen
}

// lokiTake removes and returns the pending patches saved at or before gen g. by is the newest taken
// save's principal, or "" when nothing was taken.
func (s *Server) lokiTake(g uint64) (c *loki.ChunksPatch, sp *loki.StoragePatch, by string) {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	var newest uint64
	if e := s.lokiChunks; e != nil && e.gen <= g {
		c, newest, by = e.chunks, e.gen, e.by
		s.lokiChunks = nil
	}
	if e := s.lokiStorage; e != nil && e.gen <= g {
		sp = e.storage
		if e.gen > newest {
			by = e.by
		}
		s.lokiStorage = nil
	}
	return c, sp, by
}

// lokiPending reports whether any save is waiting for a job.
func (s *Server) lokiPending() bool {
	s.lokiMu.Lock()
	defer s.lokiMu.Unlock()
	return s.lokiChunks != nil || s.lokiStorage != nil
}

// lokiContainer is the control stack's loki container, if it has one.
func lokiContainer(view inspect.ControlView) (inspect.ControlContainer, bool) {
	for _, c := range view.Containers {
		if c.Service != nil && *c.Service == lokiService {
			return c, true
		}
	}
	return inspect.ControlContainer{}, false
}

// lokiRender is what a settings value writes: config.yaml, and s3-credentials ("" without S3).
func lokiRender(set loki.Settings, secret string) (config, creds string, err error) {
	if config, err = loki.Render(set); err != nil {
		return "", "", err
	}
	if set.Storage.Type == loki.StorageS3 {
		creds = loki.Credentials(set.Storage.S3.AccessKeyID, secret)
	}
	return config, creds, nil
}

// lokiDisk is what control/loki holds now. config.yaml's read error is returned as is, so a caller
// can tell absent (fs.ErrNotExist) from unreadable. An absent s3-credentials is credsHere=false.
func (s *Server) lokiDisk() (config, creds string, credsHere bool, err error) {
	b, err := os.ReadFile(filepath.Join(s.opts.LokiDir, loki.ConfigFile))
	if err != nil {
		return "", "", false, err
	}
	c, err := os.ReadFile(filepath.Join(s.opts.LokiDir, loki.CredentialsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return string(b), "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return string(b), string(c), true, nil
}

// lokiWriteNext writes config.yaml.next and, with S3, s3-credentials.next. A failed write leaves
// neither.
func (s *Server) lokiWriteNext(config, creds string) error {
	nextCfg := filepath.Join(s.opts.LokiDir, loki.ConfigFile+loki.NextSuffix)
	nextCreds := filepath.Join(s.opts.LokiDir, loki.CredentialsFile+loki.NextSuffix)
	err := loki.WriteFile(nextCfg, config, 0o644, s.opts.LokiUID)
	if err == nil && creds != "" {
		err = loki.WriteFile(nextCreds, creds, 0o600, s.opts.LokiUID)
	}
	if err != nil {
		_ = os.Remove(nextCreds)
		_ = os.Remove(nextCfg)
	}
	return err
}

// lokiSwap moves the .next files into place. Credentials go first, so a config that names S3 never
// lands without them. Without S3, s3-credentials is removed.
func (s *Server) lokiSwap(withCreds bool) error {
	cfg := filepath.Join(s.opts.LokiDir, loki.ConfigFile)
	creds := filepath.Join(s.opts.LokiDir, loki.CredentialsFile)
	if withCreds {
		if err := os.Rename(creds+loki.NextSuffix, creds); err != nil {
			return err
		}
	} else if err := os.Remove(creds); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(cfg+loki.NextSuffix, cfg)
}

// lokiApply is one loki-apply job. Owner: the job's goroutine. startLokiApply sets s, g, by, boot, idc
// and scrubs before Start. The scrub closure Start holds also runs on the job's goroutine, after the
// work returns (jobs.go:689-719).
type lokiApply struct {
	s         *Server
	g         uint64
	by        string
	boot      bool
	idc       chan string
	scrubs    []string
	sink      log.Sink
	container inspect.ControlContainer
	steps     []stack.StepResult
}

// lokiSink scrubs each line with the job's values as they stand when the line is emitted.
type lokiSink struct {
	inner log.Sink
	scrub func(string) string
}

func (k lokiSink) Emit(level log.Level, message string) { k.inner.Emit(level, k.scrub(message)) }

// startLokiApply accepts a loki-apply job for the saves up to gen g. g 0 takes none (a boot apply).
// by names who asked. Call it holding no lock, because Start emits (rule 14). ok=false is Start's
// refusal: a waiting preempting `down` on the control key.
func (s *Server) startLokiApply(g uint64, by string, boot bool) (jobs.Job, bool) {
	a := &lokiApply{s: s, g: g, by: by, boot: boot, idc: make(chan string, 1), scrubs: append([]string{}, s.secretValues()...)}
	scrub := func(text string) string { return redact.RedactText(text, a.scrubs...) }
	work := func(rawSink log.Sink, ctx context.Context) (stack.Outcome, error) {
		a.sink = lokiSink{inner: rawSink, scrub: scrub}
		return a.run(ctx), nil
	}
	job, ok := s.jobs.Start(inspect.ControlProject, jobs.LokiApply, work, scrub)
	if ok {
		// The work reads its id at step 10. Start may already have dispatched it (pump runs inline),
		// so that receive may wait a moment for this send. The work of a refused or superseded job
		// never runs, so no receive ever waits for a send that never comes.
		a.idc <- job.ID
	}
	return job, ok
}

// run is the work. Every return records the step that ended it.
func (a *lokiApply) run(ctx context.Context) stack.Outcome {
	s := a.s
	lead := s.lokiLead()
	runner := s.lokiRunner(ctx)
	// Taken before anything can fail, so a failed job reports the saves it took and none stays
	// pending. The set is the same as at render: gen ≤ g.
	c, sp, by := s.lokiTake(a.g)
	if by == "" {
		by = a.by
	}
	a.sink.Emit(log.Info, "by "+by)

	// 1. find
	view := inspect.ControlRuntime(runner)
	if !view.Reachable {
		return a.end(phaseFind, false, "docker did not answer")
	}
	container, found := lokiContainer(view)
	if !found && a.boot {
		return a.end(phaseFind, true, "no loki container")
	}
	if !found {
		return a.end(phaseFind, false, "Loki is not running on this host")
	}
	a.container = container
	a.step(phaseFind, true, "")

	// 2. render: the row as read now, the patches over it, every save rule again, then the period guard.
	row, err := loki.Read(s.store)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	// An apply pstack stopped in: finish or undo it, then this job's saves as a second pass. A failed
	// resume fails this job, and with it the saves it took at the top of run.
	if row != nil && row.InFlight {
		if !a.resume(runner, row) {
			return a.outcome()
		}
		if row, err = loki.Read(s.store); err != nil {
			return a.end(phaseRender, false, err.Error())
		}
	}
	merged, secret, err := loki.Merge(row, c, sp, lead)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	a.scrubs = append(a.scrubs, secret)
	if row != nil {
		a.scrubs = append(a.scrubs, row.Secret)
	}
	cutover := any(nil)
	if merged.Storage.Type == loki.StorageS3 {
		a.scrubs = append(a.scrubs, merged.Storage.S3.AccessKeyID)
		cutover = merged.Storage.S3.Cutover
	}
	config, creds, err := lokiRender(merged, secret)
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	disk, diskCreds, credsHere, err := s.lokiDisk()
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	loaded, err := loki.Periods(disk)
	if err == nil {
		var next []loki.Period
		if next, err = loki.Periods(config); err == nil {
			err = loki.CheckPeriods(loaded, next, s.lokiNow(), lead, true)
		}
	}
	if err != nil {
		return a.end(phaseRender, false, err.Error())
	}
	changed := loki.Changed(row, merged, secret)
	if disk == config && credsHere == (creds != "") && diskCreds == creds && len(changed) == 0 {
		return a.end(phaseRender, true, "nothing to change")
	}
	a.step(phaseRender, true, "")

	// 3. verify. The .next files are removed on every exit. Once swapped they no longer exist; before
	// that, a failure or a cancel mid-verify leaves none behind.
	defer os.Remove(filepath.Join(s.opts.LokiDir, loki.ConfigFile+loki.NextSuffix))
	defer os.Remove(filepath.Join(s.opts.LokiDir, loki.CredentialsFile+loki.NextSuffix))
	if !a.verify(runner, config, creds) {
		return a.outcome()
	}
	// The last point where a cancel stops the job with nothing changed. From the commit on, the work
	// ignores ctx.
	if ctx.Err() != nil {
		return a.outcome()
	}

	// 4. commit: the save and, in the same statement, the row it replaces.
	if err := loki.Save(s.store, merged, secret, row); err != nil {
		return a.end(phaseCommit, false, "could not record the save: "+err.Error())
	}
	a.step(phaseCommit, true, "")

	// 5. swap
	postCtx, cancel := context.WithTimeout(context.Background(), 2*lead)
	defer cancel()
	post := s.lokiRunner(postCtx)
	if err := s.lokiSwap(creds != ""); err != nil {
		a.step(phaseSwap, false, err.Error())
		return a.rollback(post, row)
	}
	a.step(phaseSwap, true, "")

	// 6. restart Loki only, through the control restart path.
	restartedAt := time.Now()
	if _, err := inspect.RestartControlService(post, lokiService); err != nil {
		a.step(phaseRestart, false, err.Error())
		return a.rollback(post, row)
	}
	a.step(phaseRestart, true, "")

	// 7. ready
	if !a.ready(post) {
		a.step(phaseReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
		return a.rollback(post, row)
	}
	a.step(phaseReady, true, "")

	// 8. finish
	if err := loki.Finish(s.store); err != nil {
		return a.end(phaseFinish, false, "could not record the finished apply")
	}
	a.step(phaseFinish, true, "")

	// 9. log: what Loki has said since the restart. Non-fatal (invariant 1's convention).
	logs := post.Run("docker logs --since "+compose.Shq(restartedAt.UTC().Format(time.RFC3339))+" "+compose.Shq(a.container.ID),
		exec.RunOptions{Label: "docker logs"})
	errs := []string{}
	for _, l := range strings.Split(logs.Stdout+"\n"+logs.Stderr, "\n") {
		if strings.Contains(l, "level=error") {
			errs = append(errs, strings.TrimSpace(l))
		}
	}
	if len(errs) > 0 {
		a.sink.Emit(log.Warn, strings.Join(errs, "\n"))
		a.step(phaseLog, true, "non-fatal: Loki logged "+strconv.Itoa(len(errs))+" errors after restart: "+js.Truncate(errs[0], 300))
	}

	// 10. the event, outside every mutex (rule 14). A job that changed no row (every boot apply) has
	// nothing to announce.
	if len(changed) > 0 {
		s.bus.Emit("logging.changed", jsonx.O("by", by, "job", <-a.idc, "changed", changed,
			"storage", merged.Storage.Type, "cutover", cutover, "retentionDays", merged.RetentionDays))
	}
	return a.outcome()
}

// ready polls Loki's own health check every lokiPoll until it answers or LokiReadyTimeoutMs has
// passed. exec is the only way to ask, because pstack is not on the logs network. It uses the wall
// clock, never lokiNow.
func (a *lokiApply) ready(post exec.Runner) bool {
	cmd := "docker exec " + compose.Shq(a.container.ID) + " /usr/bin/loki -health"
	deadline := time.Now().Add(time.Duration(a.s.opts.LokiReadyTimeoutMs) * time.Millisecond)
	for {
		if post.Run(cmd, exec.RunOptions{Label: "loki -health"}).OK {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(a.s.lokiPoll)
	}
}

// step records one step.
func (a *lokiApply) step(phase stack.Phase, ok bool, message string) {
	r := stack.StepResult{Axis: "loki", Phase: phase, OK: ok}
	if message != "" {
		r.Message = &message
	}
	a.steps = append(a.steps, r)
}

// end records the step that ends the job and returns the outcome.
func (a *lokiApply) end(phase stack.Phase, ok bool, message string) stack.Outcome {
	a.step(phase, ok, message)
	return a.outcome()
}

// outcome is ok when every recorded step is.
func (a *lokiApply) outcome() stack.Outcome {
	o := stack.Outcome{OK: true, Steps: a.steps}
	for _, r := range a.steps {
		o.OK = o.OK && r.OK
	}
	return o
}

// lastLine returns a failed command's last non-empty stderr line, which is where Loki says why,
// capped like every step message. When stderr is empty it returns the exit code.
func lastLine(stderr string, code int) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return js.Truncate(l, 300)
	}
	return "exit " + strconv.Itoa(code)
}

// rollback undoes an apply whose swap, restart or ready wait failed after the commit. row is the row
// the apply replaced; nil means the table was empty, so the defaults. The files Render(previous) gives
// go back (an s3-credentials the save created is removed), the row goes back (loki.Revert reads
// previous_*), and Loki restarts on them.
//
// Rule 1 first, against the files on disk, never the row: when undoing would drop a period starting
// at or before now+lead, the save stays, previous_* is cleared, and Loki is left to come up on it.
//
// Every command runs on post, never the job's runner, so a cancel cannot strand swapped files. An
// error before the revert leaves previous_* set, and the next job resumes.
func (a *lokiApply) rollback(post exec.Runner, row *loki.Row) stack.Outcome {
	s := a.s
	done := func(message string) stack.Outcome { return a.end("rollback", false, message) }
	prev, secret := loki.Defaults(), ""
	if row != nil {
		prev, secret = row.Settings, row.Secret
	}
	config, creds, err := lokiRender(prev, secret)
	disk := ""
	if err == nil {
		disk, _, _, err = s.lokiDisk()
	}
	var loaded, next []loki.Period
	if err == nil {
		loaded, err = loki.Periods(disk)
	}
	if err == nil {
		next, err = loki.Periods(config)
	}
	if err != nil {
		return done("rollback failed: " + err.Error())
	}
	if loki.CheckPeriods(loaded, next, s.lokiNow(), s.lokiLead(), false) != nil {
		// The refused period: the first on-disk one the previous config does not carry at its index.
		from := ""
		for i, p := range loaded {
			if i >= len(next) || next[i] != p {
				from = p.From
				break
			}
		}
		if loki.Finish(s.store) != nil {
			return done("could not record the finished apply")
		}
		return done("not ready — S3 from " + from + " starts too soon to undo; left in place")
	}
	err = s.lokiWriteNext(config, creds)
	if err == nil {
		err = s.lokiSwap(creds != "")
	}
	if err == nil {
		err = loki.Revert(s.store)
	}
	if err != nil {
		return done("rollback failed: " + err.Error())
	}
	if _, err := inspect.RestartControlService(post, lokiService); err != nil {
		a.sink.Emit(log.Error, "rollback: "+err.Error())
		return done("rollback did not come up")
	}
	if !a.ready(post) {
		return done("rollback did not come up")
	}
	return done("rolled back")
}

// ── resume and boot ─────────────────────────────────────────────────────────────────────────────
//
// previous_* is written with the save (step 4) and cleared by finish or undo. If it is set when a job
// reaches step 2, pstack stopped mid-apply: a stop, a recreate, an OOM kill or a reboot. The job goes
// FORWARD to the row. The one way back is when rule 2 refuses and Loki never loaded the files.

// lokiBootBy is who a boot apply runs as, in its transcript's first line.
const lokiBootBy = "pstack (boot)"

// verify writes the .next files and has the running image's Loki parse them, recording the verify
// step. A failure leaves no .next file.
func (a *lokiApply) verify(runner exec.Runner, config, creds string) bool {
	s := a.s
	if err := s.lokiWriteNext(config, creds); err != nil {
		a.step(phaseVerify, false, err.Error())
		return false
	}
	res := runner.Run("docker run --rm --network none --volumes-from "+compose.Shq(a.container.ID+":ro")+" "+
		compose.Shq(a.container.Image)+" -config.file=/etc/loki/"+loki.ConfigFile+loki.NextSuffix+" -verify-config",
		exec.RunOptions{Label: "loki -verify-config"})
	if res.OK {
		a.step(phaseVerify, true, "")
		return true
	}
	for _, name := range []string{loki.ConfigFile, loki.CredentialsFile} {
		os.Remove(filepath.Join(s.opts.LokiDir, name+loki.NextSuffix))
	}
	if out := strings.TrimSpace(res.Stderr); out != "" {
		a.sink.Emit(log.Error, out)
	}
	a.step(phaseVerify, false, lastLine(res.Stderr, res.Code))
	return false
}

// resume finishes or undoes the cut-off apply that row records. false means it recorded the step that
// ends the job. No event.
func (a *lokiApply) resume(runner exec.Runner, row *loki.Row) bool {
	s := a.s
	a.step(phaseRender, true, "unfinished apply")
	for _, r := range []*loki.Row{row, row.Previous} {
		if r != nil {
			a.scrubs = append(a.scrubs, r.Secret)
			if r.Settings.Storage.S3 != nil {
				a.scrubs = append(a.scrubs, r.Settings.Storage.S3.AccessKeyID)
			}
		}
	}
	fail := func(phase stack.Phase, msg string) bool {
		a.step(phase, false, msg)
		return false
	}
	prev, prevSecret := loki.Defaults(), ""
	if row.Previous != nil {
		prev, prevSecret = row.Previous.Settings, row.Previous.Secret
	}
	beforeCfg, beforeCreds, err := lokiRender(prev, prevSecret)
	if err != nil {
		return fail(phaseRender, err.Error())
	}
	config, creds, err := lokiRender(row.Settings, row.Secret)
	if err != nil {
		return fail(phaseRender, err.Error())
	}
	diskCfg, diskCreds, credsHere, err := s.lokiDisk()
	if err != nil {
		return fail(phaseRender, err.Error())
	}
	// Has Loki loaded the files on disk? Only if it started after both were written. The mtimes come
	// from the host's clock, and pstack's rename is the only writer. No StartedAt means no.
	var written time.Time
	for _, name := range []string{loki.ConfigFile, loki.CredentialsFile} {
		if st, err := os.Stat(filepath.Join(s.opts.LokiDir, name)); err == nil && st.ModTime().After(written) {
			written = st.ModTime()
		}
	}
	loaded := a.container.StartedAt != nil && *a.container.StartedAt > written.UnixMilli()
	// The period guard reads what Loki runs: the files if it loaded them, else the previous render.
	running := beforeCfg
	if loaded {
		running = diskCfg
	}
	live, err := loki.Periods(running)
	if err != nil {
		return fail(phaseRender, err.Error())
	}
	next, err := loki.Periods(config)
	if err != nil {
		return fail(phaseRender, err.Error())
	}
	now, lead := s.lokiNow(), s.lokiLead()
	if guard := loki.CheckPeriods(live, next, now, lead, true); guard != nil {
		if loaded || loki.CheckPeriods(live, next, now, lead, false) != nil {
			return fail(phaseRender, guard.Error())
		}
		// Rule 2 alone, and Loki still runs the previous files: put them back. Files go first, then the
		// row, so a stop between the two resumes into this same branch. No restart.
		if err := s.lokiWriteNext(beforeCfg, beforeCreds); err != nil {
			return fail(phaseSwap, err.Error())
		}
		if err := s.lokiSwap(beforeCreds != ""); err != nil {
			return fail(phaseSwap, err.Error())
		}
		if err := loki.Revert(s.store); err != nil {
			return fail(phaseCommit, "could not record the undo: "+err.Error())
		}
		msg := guard.Error()
		if s3 := row.Settings.Storage.S3; s3 != nil {
			msg = "cutover " + s3.Cutover + " passed while pstack was down — save again"
		}
		return fail(phaseRender, msg)
	}
	// From here on it is the forward path's post runner: a cancel still restarts, waits and finishes.
	postCtx, cancel := context.WithTimeout(context.Background(), 2*s.lokiLead())
	defer cancel()
	post := s.lokiRunner(postCtx)
	restart := !loaded
	if diskCfg != config || credsHere != (creds != "") || diskCreds != creds {
		if !a.verify(runner, config, creds) {
			return false
		}
		if err := s.lokiSwap(creds != ""); err != nil {
			a.step(phaseSwap, false, err.Error())
			a.rollback(post, row.Previous)
			return false
		}
		a.step(phaseSwap, true, "swapped")
		restart = true
	}
	if restart {
		if _, err := inspect.RestartControlService(post, lokiService); err != nil {
			a.step(phaseRestart, false, err.Error())
			a.rollback(post, row.Previous)
			return false
		}
		a.step(phaseRestart, true, "")
	}
	if !a.ready(post) {
		a.step(phaseReady, false, "not ready within "+scheduler.FormatDuration(s.opts.LokiReadyTimeoutMs))
		a.rollback(post, row.Previous)
		return false
	}
	a.step(phaseReady, true, "")
	if err := loki.Finish(s.store); err != nil {
		return fail(phaseFinish, "could not record the finished apply")
	}
	a.step(phaseFinish, true, "resumed")
	return true
}

// reconcileLoki starts a boot loki-apply job when an apply was cut off, or when the files are not what
// the row (or the defaults) renders. It runs no docker command. The job does, on its own goroutine, so
// a wedged dockerd cannot stall New.
func (s *Server) reconcileLoki() {
	diskCfg, diskCreds, credsHere, err := s.lokiDisk()
	if errors.Is(err, fs.ErrNotExist) {
		return // logging off, or control/loki is not mounted here
	}
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	row, err := loki.Read(s.store)
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	if row != nil && row.InFlight {
		s.startLokiApply(0, lokiBootBy, true)
		return
	}
	set, secret := loki.Defaults(), ""
	if row != nil {
		set, secret = row.Settings, row.Secret
	}
	// Render runs the save rules, so a row this release rejects (a later release wrote it, or hand SQL)
	// is a line, never a job.
	config, creds, err := lokiRender(set, secret)
	if err != nil {
		s.opts.Log("loki: " + err.Error())
		return
	}
	if config == diskCfg && credsHere == (creds != "") && creds == diskCreds {
		return
	}
	if creds != "" {
		if err := loki.CredentialsOwner(s.opts.LokiUID); err != nil {
			s.opts.Log("loki: " + err.Error())
			return
		}
	}
	s.startLokiApply(0, lokiBootBy, true)
}
