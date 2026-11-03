package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
)

// `pstack migrate` — copies deployments and their state from one host to another, over both hosts'
// APIs. The hosts never call each other for it.

const migrateUsage = "usage: pstack migrate --from <API URL> --to <API URL> [<id>...] [--sleep-source]"

// migratePoll is the gap between two rounds of job polls; a test shortens it.
var migratePoll = time.Second

// migrateRow is as much of a GET /api/deployments row as migrate reads. Vars stay raw, so they are
// copied byte for byte, key order included.
type migrateRow struct {
	ID        string          `json:"id"`
	UpdatedAt int64           `json:"updatedAt"`
	SpecName  *string         `json:"specName"`
	Vars      json.RawMessage `json:"vars"`
	Asleep    any             `json:"asleep"`
	Running   *bool           `json:"running"`
}

// state is running | asleep | down | unknown. `running: null` is docker not answering, never down
// (invariant 11).
func (r migrateRow) state() string {
	switch {
	case r.Running != nil && *r.Running:
		return "running"
	case r.Asleep != nil:
		return "asleep"
	case r.Running != nil:
		return "down"
	}
	return "unknown"
}

// migrateHost is one end. The source's calls carry X-Pstack-Forwarded-By, so a source that forwards
// its deploys answers them itself.
type migrateHost struct {
	name, base, token string
	hdr               http.Header
}

func (h migrateHost) call(method, path string, body []byte) (int, []byte, error) {
	return apiCall(method, h.base+path, h.token, body, h.hdr)
}

// migrateHostFor resolves one end through apiBaseAnyRole, with flag in PSTACK_API_URL's place and
// tokenVar in PSTACK_TOKEN's, and says so in its errors.
func migrateHostFor(ios IO, name, flag, raw, tokenVar string, hdr http.Header) (migrateHost, *Exit) {
	env := func(k string) (string, bool) {
		switch k {
		case "PSTACK_API_URL":
			return raw, true
		case "PSTACK_TOKEN":
			return ios.Env(tokenVar)
		}
		return "", false
	}
	base, tok, ex := apiBaseAnyRole(env)
	if ex != nil {
		ex.Msg = strings.NewReplacer("PSTACK_API_URL", flag, "PSTACK_TOKEN", tokenVar).Replace(ex.Msg)
		return migrateHost{}, ex
	}
	return migrateHost{name, base, tok, hdr}, nil
}

func (h migrateHost) list() ([]migrateRow, *Exit) {
	status, got, err := h.call("GET", "/api/deployments", nil)
	if err != nil {
		return nil, &Exit{Code: ExitFailed, Msg: h.name + ": " + err.Error()}
	}
	if status != 200 {
		return nil, &Exit{Code: ExitFailed, Msg: h.name + ": " + apiError(status, got)}
	}
	var l struct{ Deployments []migrateRow }
	if err := json.Unmarshal(got, &l); err != nil {
		return nil, &Exit{Code: ExitFailed, Msg: h.name + ": the API answered with a body this pstack does not understand (" + err.Error() + ")"}
	}
	return l.Deployments, nil
}

// migration is one deployment's progress: what was done, in order, for its line.
type migration struct {
	row    migrateRow
	state  string
	done   []string
	failed bool
}

func (m *migration) fail(why string) {
	m.done = append(m.done, "failed ("+why+")")
	m.failed = true
}

// pendingJob is a job migrate waits for: ok appends okWord to m's line; anything else fails m.
type pendingJob struct {
	m                *migration
	host             migrateHost
	id, what, okWord string
}

// lifecycle POSTs action and returns the job to wait for; nil after a refusal, with the status and
// body for the caller to read.
func (m *migration) lifecycle(h migrateHost, action, what, okWord string) (*pendingJob, int, []byte) {
	status, got, err := h.call("POST", "/api/deployments/"+url.PathEscape(m.row.ID)+"/"+action, nil)
	if err != nil {
		m.fail(what + ": " + err.Error())
		return nil, 0, nil
	}
	var r struct{ Job struct{ ID string } }
	if status != 202 || json.Unmarshal(got, &r) != nil || r.Job.ID == "" {
		return nil, status, got
	}
	return &pendingJob{m, h, r.Job.ID, what, okWord}, status, got
}

// waitJobs polls every job until it ends, one GET per job per round, and stops polling each at its
// first terminal answer. A 404 is a record the host already evicted (jobs.MaxJobs) and is final:
// re-running migrate is the retry.
func waitJobs(pending []*pendingJob) {
	for len(pending) > 0 {
		var left []*pendingJob
		for _, p := range pending {
			status, got, err := p.host.call("GET", "/api/jobs/"+url.PathEscape(p.id), nil)
			var j struct {
				Job struct {
					State jobs.State
					Error *string
				}
			}
			switch {
			case err != nil:
				p.m.fail(p.what + ": " + err.Error())
			case status == 404:
				p.m.fail("job record gone")
			case status != 200:
				p.m.fail(p.what + ": " + apiError(status, got))
			case json.Unmarshal(got, &j) != nil:
				p.m.fail(p.what + ": unreadable job")
			case !j.Job.State.Terminal():
				left = append(left, p)
			case j.Job.State == jobs.OK:
				p.m.done = append(p.m.done, p.okWord)
			case j.Job.Error != nil:
				p.m.fail(p.what + ": " + *j.Job.Error)
			default:
				p.m.fail(p.what + ": " + string(j.Job.State))
			}
		}
		pending = left
		if len(pending) > 0 {
			time.Sleep(migratePoll)
		}
	}
}

// copyTo PUTs the source's deployment on dst unchanged: a named spec by name (the target has it
// from `push config`), an inline one with its compose file.
func (m *migration) copyTo(src, dst migrateHost) bool {
	body := struct {
		SpecName *string         `json:"specName,omitempty"`
		Spec     string          `json:"spec,omitempty"`
		Compose  *string         `json:"compose,omitempty"`
		Vars     json.RawMessage `json:"vars,omitempty"`
	}{SpecName: m.row.SpecName, Vars: m.row.Vars}
	path := "/api/deployments/" + url.PathEscape(m.row.ID)
	if m.row.SpecName == nil {
		status, got, err := src.call("GET", path+"/source", nil)
		if err == nil && status != 200 {
			err = fmt.Errorf("%s", apiError(status, got))
		}
		if err == nil {
			err = json.Unmarshal(got, &body)
		}
		if err != nil {
			m.fail("copy: " + err.Error())
			return false
		}
		body.SpecName = nil
	}
	payload, _ := json.Marshal(body)
	status, got, err := dst.call("PUT", path, payload)
	if err == nil && (status < 200 || status >= 300) {
		err = fmt.Errorf("%s", apiError(status, got))
	}
	if err != nil {
		m.fail("copy: " + err.Error())
		return false
	}
	m.done = append(m.done, "copied")
	return true
}

func migrateCmd(args *Parsed, ios IO) *Exit {
	if args.From == "" || !args.Typed["--to"] {
		return fail(migrateUsage)
	}
	src, ex := migrateHostFor(ios, "source", "--from", args.From, "PSTACK_FROM_TOKEN", http.Header{"X-Pstack-Forwarded-By": {"migrate"}})
	if ex != nil {
		return ex
	}
	dst, ex := migrateHostFor(ios, "target", "--to", args.To, "PSTACK_TO_TOKEN", nil)
	if ex != nil {
		return ex
	}
	srcRows, ex := src.list()
	if ex != nil {
		return ex
	}
	dstRows, ex := dst.list()
	if ex != nil {
		return ex
	}
	targets := map[string]migrateRow{}
	for _, r := range dstRows {
		targets[r.ID] = r
	}

	var all []*migration
	if ids := migrateIDs(args); len(ids) > 0 {
		have := map[string]migrateRow{}
		for _, r := range srcRows {
			have[r.ID] = r
		}
		for _, id := range ids {
			r, ok := have[id]
			m := &migration{row: r, state: r.state()}
			if !ok {
				m.row.ID, m.state = id, "missing"
				m.fail("not on the source")
			}
			all = append(all, m)
		}
	} else {
		for _, r := range srcRows {
			all = append(all, &migration{row: r, state: r.state()})
		}
	}

	// ponytail: newest updatedAt wins, comparing two hosts' clocks. Fine under NTP; a per-deployment
	// revision counter if clocks ever disagree by more than a deploy's length.
	var ups []*pendingJob
	sleepAfter := map[*migration]bool{}
	for _, m := range all {
		if m.failed {
			continue
		}
		if m.state == "unknown" {
			m.done = append(m.done, "skipped (docker did not answer)")
			m.failed = true
			continue
		}
		// A sleep pass copies nothing and changes nothing on the target. It runs after DNS, often
		// hours after the copy, and by then CI has deleted or downed previews on the target through
		// the forwarding source, whose own rows went stale. Copying or bringing those up again would
		// re-run their up hooks for closed pull requests.
		if args.SleepSource {
			continue
		}
		t, onTarget := targets[m.row.ID]
		copied := !onTarget || m.row.UpdatedAt > t.UpdatedAt
		if copied && !m.copyTo(src, dst) {
			continue
		}
		// A row this run did not copy is only ever brought UP to the source's state: a newer target
		// that is running stays running, and nothing here ever issues `down`.
		up := false
		switch m.state {
		case "running":
			up = copied || t.state() != "running"
		case "asleep":
			up = copied || t.state() == "down"
			sleepAfter[m] = up
		}
		if !up {
			continue
		}
		p, status, got := m.lifecycle(dst, "up", "up", "up")
		if p != nil {
			ups = append(ups, p)
		} else if status != 0 {
			m.fail("up: " + apiError(status, got))
		}
	}
	waitJobs(ups)

	var sleeps []*pendingJob
	for _, p := range ups {
		if m := p.m; !m.failed && sleepAfter[m] {
			p, status, got := m.lifecycle(dst, "sleep", "sleep", "slept")
			if p != nil {
				sleeps = append(sleeps, p)
			} else if status != 0 {
				m.fail("sleep: " + apiError(status, got))
			}
		}
	}
	waitJobs(sleeps)

	if args.SleepSource {
		var srcSleeps []*pendingJob
		for _, m := range all {
			if m.failed || m.state != "running" {
				continue
			}
			// Gone from the target: CI deleted it there. The source copy is left as it is.
			if _, ok := targets[m.row.ID]; !ok {
				m.done = append(m.done, "not on target")
				continue
			}
			p, status, got := m.lifecycle(src, "sleep", "source sleep", "source slept")
			var refused struct{ Kind string }
			_ = json.Unmarshal(got, &refused)
			switch {
			case p != nil:
				srcSleeps = append(srcSleeps, p)
			case status == 0: // already failed
			// Refusals the spec expects: the source copy keeps serving, and that is not a failure.
			case status == 409 && refused.Kind == "shared":
				m.done = append(m.done, "left running (shared)")
			case status == 400 && strings.Contains(apiError(status, got), "no compose"):
				m.done = append(m.done, "left running (no compose)")
			default:
				m.fail("source sleep: " + apiError(status, got))
			}
		}
		waitJobs(srcSleeps)
	}

	failed := false
	for _, m := range all {
		result := strings.Join(m.done, ", ")
		if result == "" {
			result = "unchanged"
		}
		fmt.Fprintln(ios.Stdout, m.row.ID+"  "+m.state+"  "+result)
		failed = failed || m.failed
	}
	if failed {
		return &Exit{Code: ExitFailed}
	}
	return nil
}

// migrateIDs is every positional word after `migrate`.
func migrateIDs(args *Parsed) []string {
	var ids []string
	for _, id := range append([]string{args.Sub, args.Arg}, args.Extra...) {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
