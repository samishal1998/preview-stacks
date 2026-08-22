// Package stack is lifecycle orchestration. The whole point of this file is the asymmetry between
// `down` and `verify`:
//
//	down   is BEST-EFFORT — a resource may already be gone, a network may be busy, an API may
//	       flake. Aborting halfway leaves MORE garbage than continuing, so every failure is
//	       recorded and teardown carries on.
//	verify is STRICT — it asserts each axis is actually gone and exits non-zero if not.
//
// Homegrown teardown scripts almost always implement the first half and skip the second, which is
// why they leak: `docker compose down -v` never removes images, and a `down` that omits a profile
// silently leaves that profile's network behind. `verify` is the part that catches it.
package stack

import (
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/compose"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// Phase is which hook a step ran.
type Phase string

const (
	PhaseRequires   Phase = "requires"
	PhaseUp         Phase = "up"
	PhaseDown       Phase = "down"
	PhaseAssertGone Phase = "assert_gone"
	PhaseAssertLive Phase = "assert_live"
	PhaseCompose    Phase = "compose"
)

// StepResult is one hook's verdict. The wire shape the job transcript carries.
type StepResult struct {
	Axis    string  `json:"axis"`
	Phase   Phase   `json:"phase"`
	OK      bool    `json:"ok"`
	Code    int     `json:"code"`
	Message *string `json:"message,omitempty"`
	Skipped bool    `json:"skipped"`
}

// Outcome is a lifecycle action's result.
type Outcome struct {
	OK    bool         `json:"ok"`
	Steps []StepResult `json:"steps"`
	// Outputs is the env accumulated from axis outputs during `up` — the inter-axis credential
	// channel. Ordered (OutputKeys) so the job record matches the TS byte for byte.
	Outputs    map[string]string `json:"outputs"`
	OutputKeys []string          `json:"-"`
}

// MarshalJSON emits outputs in accumulation order (OutputKeys), the way Object.assign per axis
// left them; keys the list does not name (a hand-built Outcome) follow in byte order.
func (o Outcome) MarshalJSON() ([]byte, error) {
	outputs := make(jsonx.Object, 0, len(o.Outputs))
	seen := map[string]bool{}
	for _, k := range o.OutputKeys {
		if v, ok := o.Outputs[k]; ok && !seen[k] {
			outputs = append(outputs, jsonx.KV{K: k, V: v})
			seen[k] = true
		}
	}
	rest := []string{}
	for k := range o.Outputs {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		outputs = append(outputs, jsonx.KV{K: k, V: o.Outputs[k]})
	}
	steps := o.Steps
	if steps == nil {
		steps = []StepResult{}
	}
	return jsonx.Marshal(jsonx.O("ok", o.OK, "steps", steps, "outputs", outputs))
}

// Leaked is THE leak step scan, the one copy: a torn-down stack where something survived.
// The TS had four copies in three files; invariant 8 says they must agree, and one function agrees
// with itself.
func (o Outcome) Leaked() bool {
	for _, s := range o.Steps {
		if s.Phase == PhaseAssertGone && !s.OK {
			return true
		}
	}
	return false
}

func allOK(steps []StepResult) bool {
	for _, s := range steps {
		if !s.OK {
			return false
		}
	}
	return true
}

func msg(s string) *string { return &s }

// firstLine is the first line of a hook's output, capped at 300 characters.
func firstLine(s string) string {
	line := strings.TrimSpace(s)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return js.Truncate(line, 300)
}

func hookEnv(st *spec.Stack, outputs map[string]string) map[string]string {
	env := exec.Merge(st.Env, outputs)
	env["STACK"] = st.Stack
	return env
}

func emptyOutcome(ok bool, steps []StepResult) Outcome {
	if steps == nil {
		steps = []StepResult{}
	}
	return Outcome{OK: ok, Steps: steps, Outputs: map[string]string{}, OutputKeys: []string{}}
}

// Up brings a stack up: every requirement by name, every axis in declaration order, then compose
// up. Fails FAST — a half-provisioned stack should not proceed to deploy: if the database axis fails
// there is no point starting the app that migrates it.
func Up(st *spec.Stack, r exec.Runner, sink log.Sink) Outcome {
	steps := []StepResult{}
	outputs := map[string]string{}
	outputKeys := []string{}
	fail := func() Outcome { return Outcome{OK: false, Steps: steps, Outputs: outputs, OutputKeys: outputKeys} }

	// Preflight FIRST, before anything is created. A missing shared dependency should fail by name
	// here rather than as an opaque error from whatever CLI an axis hook happened to call.
	for _, req := range st.Requires {
		sink.Emit(log.Step, "→ requires: "+req.Name)
		res := r.Run(req.Assert, exec.RunOptions{Env: hookEnv(st, nil), Label: "requires " + req.Name})
		step := StepResult{Axis: req.Name, Phase: PhaseRequires, OK: res.OK, Code: res.Code, Skipped: res.Skipped}
		if !res.OK {
			m := "unmet"
			if req.Hint != "" {
				m += " — " + req.Hint
			}
			step.Message = msg(m)
		}
		steps = append(steps, step)
		if !res.OK {
			return fail()
		}
	}

	for _, axis := range st.Axes {
		if axis.Up != "" {
			sink.Emit(log.Step, "→ up: "+axis.Name)
			res := r.Run(axis.Up, exec.RunOptions{Env: hookEnv(st, outputs), Label: "up " + axis.Name})
			step := StepResult{Axis: axis.Name, Phase: PhaseUp, OK: res.OK, Code: res.Code, Skipped: res.Skipped}
			if !res.OK {
				step.Message = msg(firstLine(or(res.Stderr, res.Stdout)))
			}
			steps = append(steps, step)
			if !res.OK {
				return fail()
			}
			keys, vals := exec.CaptureOutputs(res.Stdout)
			for _, k := range keys {
				if _, seen := outputs[k]; !seen {
					outputKeys = append(outputKeys, k)
				}
				outputs[k] = vals[k]
			}
		}
		// Prove the resource really exists. A provision hook that exits 0 without creating anything
		// otherwise surfaces much later as an opaque connection error.
		if axis.AssertLive != "" {
			res := r.Run(axis.AssertLive, exec.RunOptions{Env: hookEnv(st, outputs), Label: "assert_live " + axis.Name})
			step := StepResult{Axis: axis.Name, Phase: PhaseAssertLive, OK: res.OK, Code: res.Code, Skipped: res.Skipped}
			if !res.OK {
				step.Message = msg("provisioned but assert_live failed — resource missing?")
			}
			steps = append(steps, step)
			if !res.OK {
				return fail()
			}
		}
	}

	if st.Compose != nil {
		verb := "compose up"
		if st.Compose.Orchestrator == spec.Swarm {
			verb = "stack deploy"
		}
		profiles := strings.Join(st.Compose.Profiles, ", ")
		if profiles == "" {
			profiles = "no profiles"
		}
		sink.Emit(log.Step, "→ "+verb+" ("+profiles+")")
		res, err := compose.ComposeUp(st, r, outputs, func(line string) { sink.Emit(log.Info, line) })
		if err != nil {
			res = exec.Result{OK: false, Code: 1, Stderr: err.Error()}
		}
		step := StepResult{Axis: "(compose)", Phase: PhaseCompose, OK: res.OK, Code: res.Code, Skipped: res.Skipped}
		if !res.OK {
			step.Message = msg(firstLine(or(res.Stderr, res.Stdout)))
		}
		steps = append(steps, step)
	}
	return Outcome{OK: allOK(steps), Steps: steps, Outputs: outputs, OutputKeys: outputKeys}
}

// DownOptions: `Verify` runs assert_gone after destroying (default true — a teardown that silently
// half-worked is the failure this tool exists to catch); `Force` allows a `kind: shared` teardown.
type DownOptions struct {
	NoVerify bool
	Force    bool
}

// Down tears a stack down: compose down (with EVERY profile), then axes in REVERSE order, then
// verify. Reverse order matters for the same reason as up's forward order.
func Down(st *spec.Stack, r exec.Runner, o DownOptions, sink log.Sink) Outcome {
	steps := []StepResult{}
	// `down` runs `compose down -v`, and on a shared singleton `-v` destroys the volumes every other
	// deployment depends on. Routine for a tenant, catastrophic here, and the verb is identical.
	if st.Kind == spec.Shared && !o.Force {
		sink.Emit(log.Error, `refusing to tear down shared deployment "`+st.Stack+`"`)
		return emptyOutcome(false, []StepResult{{Axis: st.Stack, Phase: PhaseCompose, OK: false, Code: 1, Skipped: false, Message: msg("refused: kind is `shared`. `down` removes volumes (-v), which on a shared deployment destroys state every tenant depends on. Re-run with --force if that is truly intended.")}})
	}
	if st.Compose != nil {
		if st.Compose.Orchestrator == spec.Swarm {
			sink.Emit(log.Step, "→ stack rm")
		} else {
			sink.Emit(log.Step, "→ compose down (all profiles)")
		}
		res, err := compose.ComposeDown(st, r)
		if err != nil {
			res = exec.Result{OK: false, Code: 1, Stderr: err.Error()}
		}
		// Best-effort: a missing project directory or an already-removed stack is normal on a retry.
		step := StepResult{Axis: "(compose)", Phase: PhaseCompose, OK: true, Code: res.Code, Skipped: res.Skipped}
		if !res.OK {
			step.Message = msg("non-fatal: " + firstLine(or(res.Stderr, res.Stdout)))
		}
		steps = append(steps, step)
	}
	// The reversed COPY: an in-place reverse would mutate the spec the API reuses across a request.
	for i := len(st.Axes) - 1; i >= 0; i-- {
		axis := st.Axes[i]
		if axis.Down == "" {
			continue
		}
		sink.Emit(log.Step, "→ down: "+axis.Name)
		res := r.Run(axis.Down, exec.RunOptions{Env: hookEnv(st, nil), Label: "down " + axis.Name})
		// Recorded but never fatal — see the package doc.
		step := StepResult{Axis: axis.Name, Phase: PhaseDown, OK: true, Code: res.Code, Skipped: res.Skipped}
		if !res.OK {
			step.Message = msg("non-fatal: " + firstLine(or(res.Stderr, res.Stdout)))
		}
		steps = append(steps, step)
	}
	if !o.NoVerify {
		steps = append(steps, Verify(st, r, sink).Steps...)
	}
	return emptyOutcome(allOK(steps), steps)
}

// Sleep puts a stack to SLEEP: the compose project goes down, its volumes and every axis stay.
// Not a teardown and not reported as one — there is no verify, because nothing is supposed to be
// gone. Refused for `kind: shared`: sleeping a singleton stops every preview that depends on it.
func Sleep(st *spec.Stack, r exec.Runner, sink log.Sink) Outcome {
	if st.Kind == spec.Shared {
		sink.Emit(log.Error, `refusing to put shared deployment "`+st.Stack+`" to sleep`)
		return emptyOutcome(false, []StepResult{{Axis: st.Stack, Phase: PhaseCompose, OK: false, Code: 1, Skipped: false, Message: msg("refused: kind is `shared`. Every preview that depends on this singleton would go with it.")}})
	}
	if st.Compose == nil {
		return emptyOutcome(true, nil)
	}
	if st.Compose.Orchestrator == spec.Swarm {
		sink.Emit(log.Step, "→ stack rm (sleep: volumes kept)")
	} else {
		sink.Emit(log.Step, "→ compose down (sleep: volumes kept)")
	}
	res, err := compose.ComposeSleep(st, r)
	if err != nil {
		res = exec.Result{OK: false, Code: 1, Stderr: err.Error()}
	}
	step := StepResult{Axis: "(compose)", Phase: PhaseCompose, OK: true, Code: res.Code, Skipped: res.Skipped}
	if !res.OK {
		step.Message = msg("non-fatal: " + firstLine(or(res.Stderr, res.Stdout)))
	}
	return emptyOutcome(true, []StepResult{step})
}

// Verify asserts every axis is gone. The leak gate: a non-zero exit means something survived
// teardown. Axes without assert_gone are reported `unverifiable` rather than passing silently.
func Verify(st *spec.Stack, r exec.Runner, sink log.Sink) Outcome {
	steps := []StepResult{}
	sink.Emit(log.Step, "→ verify (asserting resources are gone)")
	for _, axis := range st.Axes {
		if axis.AssertGone == "" {
			steps = append(steps, StepResult{Axis: axis.Name, Phase: PhaseAssertGone, OK: true, Code: 0, Skipped: true, Message: msg("unverifiable: no assert_gone defined")})
			continue
		}
		res := r.Run(axis.AssertGone, exec.RunOptions{Env: hookEnv(st, nil), Label: "assert_gone " + axis.Name})
		step := StepResult{Axis: axis.Name, Phase: PhaseAssertGone, OK: res.OK, Code: res.Code, Skipped: res.Skipped}
		if !res.OK {
			step.Message = msg("LEAKED: resource still present after teardown")
		}
		steps = append(steps, step)
	}
	return emptyOutcome(allOK(steps), steps)
}

// Status is what is currently running for this stack, straight from compose.
func Status(st *spec.Stack, r exec.Runner) string {
	if st.Compose == nil {
		return "(no compose section in spec)"
	}
	res, err := compose.ComposePs(st, r)
	if err != nil {
		return "(nothing running)"
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		return out
	}
	return "(nothing running)"
}

// Report renders an Outcome as a compact report. Presentation, not a contract.
func Report(o Outcome) string {
	var lines []string
	for _, s := range o.Steps {
		mark := "✗"
		if s.Skipped && s.Message != nil && strings.HasPrefix(*s.Message, "unverifiable") {
			mark = "?"
		} else if s.OK {
			mark = "✓"
		}
		m := ""
		if s.Message != nil {
			m = "  — " + *s.Message
		}
		lines = append(lines, "  "+mark+" "+js.PadEnd(string(s.Phase), 12)+" "+s.Axis+m)
	}
	leaks, unverifiable := 0, 0
	for _, s := range o.Steps {
		if s.Phase == PhaseAssertGone && !s.OK {
			leaks++
		}
		if s.Message != nil && strings.HasPrefix(*s.Message, "unverifiable") {
			unverifiable++
		}
	}
	var tail []string
	if leaks > 0 {
		tail = append(tail, js.ToString(int64(leaks))+" leaked resource(s)")
	}
	if unverifiable > 0 {
		tail = append(tail, js.ToString(int64(unverifiable))+" unverifiable axis/axes")
	}
	if len(tail) > 0 {
		lines = append(lines, "  "+strings.Join(tail, ", "))
	}
	return strings.Join(lines, "\n")
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
