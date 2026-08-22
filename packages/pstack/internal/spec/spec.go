// Package spec parses and validates a `preview.yml` into a resolved Stack.
//
// Design notes that matter:
//   - `${VAR}` interpolation happens ONCE, here, against a known env. Nothing downstream
//     re-interpolates, so a value containing `${...}` can never be re-expanded by accident.
//   - `stack` resolves to the single identity string every axis namespaces off. It is exported to
//     hooks as $STACK so a hook never has to reconstruct it.
//   - Axis order is significant: provisioned in declaration order, destroyed in REVERSE.
//
// The TS module kept a module-global `warnings` array; here they are a field on the result, which
// is what a goroutine-per-request server needs anyway.
//
// Presence, not nullness, is what every optional key is tested on: the TS checked `=== undefined`,
// and a key written with no value (`compose:` and nothing after it, `kind: ~`) is PRESENT and null
// — "`compose` must be a mapping", "kind: expected a string, got object" — never the default.
// Hence `raw.Get(k)` with only `ok` inspected, everywhere but `version`, whose `?? 1` does read a
// null as the default.
package spec

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// Error is a spec that will not parse, or will not resolve. Always the caller's problem: the API
// maps it to 400 with the prefix `spec: `, the CLI to exit 3.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsSpecError reports whether err is a *Error.
func IsSpecError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Axis is one provisioned thing.
type Axis struct {
	Name       string
	Up         string // Provision. Must be idempotent — `up` is re-run on every redeploy.
	Down       string // Destroy. Best-effort: a non-zero exit is reported but does not abort teardown.
	AssertGone string // Exit 0 ⇒ the resource is GONE. The leak gate.
	AssertLive string // Exit 0 ⇒ the resource EXISTS. Run after `up` to fail fast.
}

// Hooks lists which of the four hooks an axis declares, in the fixed order the API reports.
func (a Axis) Hooks() []string {
	out := []string{}
	if a.Up != "" {
		out = append(out, "up")
	}
	if a.Down != "" {
		out = append(out, "down")
	}
	if a.AssertGone != "" {
		out = append(out, "assert_gone")
	}
	if a.AssertLive != "" {
		out = append(out, "assert_live")
	}
	return out
}

// Kind is what a deployment IS: `shared` (a host singleton previews borrow — no axes, guarded down)
// or `isolated` (one tenant, has axes, leaves nothing behind). Explicit, never inferred.
type Kind string

const (
	Shared   Kind = "shared"
	Isolated Kind = "isolated"
)

// Requirement is a precondition checked by name before `up`.
type Requirement struct {
	Name   string
	Assert string
	Hint   string // "" when absent
}

// Orchestrator is how a compose project is driven.
type Orchestrator string

const (
	Compose Orchestrator = "compose"
	Swarm   Orchestrator = "swarm"
)

// ComposeSpec is the `compose:` section.
type ComposeSpec struct {
	File         string
	Orchestrator Orchestrator
	Profiles     []string // never nil
	Overlays     []string // never nil
	Subdomains   []SubdomainRoute
}

// SleepPolicy is the `sleep:` section; zero means absent.
type SleepPolicy struct {
	IdleMs  int64
	AfterMs int64
}

// HostValues are the control plane's `${vars.*}` / `${secrets.*}`. Nil under the bare CLI.
type HostValues struct {
	Vars    map[string]string
	Secrets map[string]string
}

// Stack is a fully resolved spec.
type Stack struct {
	Version int
	Kind    Kind
	// Stack is the resolved identity, e.g. `pr-123`. Exported to every hook as $STACK.
	Stack    string
	Compose  *ComposeSpec
	Sleep    *SleepPolicy
	Requires []Requirement
	Axes     []Axis
	// Env is the fully-resolved env handed to compose and every hook.
	//
	// DANGER — seeded from the WHOLE ambient environment before the spec's own `env:` is layered
	// on, so it holds every secret this process holds, PSTACK_TOKEN included. Never serialise it,
	// never log it, never return it from a response. Use DeclaredEnv to show a spec's variables.
	Env map[string]string
	// DeclaredEnv is the variable names the SPEC ITSELF declared under `env:`, in order.
	DeclaredEnv []string
	// Warnings are the non-fatal observations `pstack validate` prints.
	Warnings []string
}

var (
	varRe      = regexp.MustCompile(`\$\{(?:(vars|secrets)\.)?([A-Za-z_][A-Za-z0-9_]*)\}`)
	stackRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	durationRe = regexp.MustCompile(`^(\d+[smhd])+$`)
	durPartRe  = regexp.MustCompile(`(\d+)([smhd])`)
	orTrueRe   = regexp.MustCompile(`\|\|\s*true\b`)
	guardRe    = regexp.MustCompile(`\bexit\b|\|\||&&`)
)

// Interpolate substitutes `${VAR}` from vars and `${vars.X}`/`${secrets.X}` from host. An unknown
// or EMPTY variable is an error rather than "": silently expanding `${PR}` to "" yields a stack
// named `pr-` that collides across every PR. Missing names are reported in first-seen order.
func Interpolate(input string, vars map[string]string, where string, host *HostValues) (string, error) {
	var missing []string
	seen := map[string]bool{}
	var failure error
	out := replaceAllSubmatch(varRe, input, func(m []string) string {
		ns, name := m[1], m[2]
		if ns != "" {
			if host == nil {
				// The bare CLI has no host store. Naming the boundary beats a generic "undefined
				// variable": the spec is fine, the runtime is the wrong one.
				if failure == nil {
					failure = &Error{fmt.Sprintf("%s: ${%s.%s} — host %s live on the control plane. Submit this spec to a pstack server, or use a plain ${%s} and pass the value yourself.", where, ns, name, ns, name)}
				}
				return ""
			}
			src := host.Vars
			if ns == "secrets" {
				src = host.Secrets
			}
			v, ok := src[name]
			if !ok || v == "" {
				if !seen[ns+"."+name] {
					seen[ns+"."+name] = true
					missing = append(missing, ns+"."+name)
				}
				return ""
			}
			return v
		}
		v, ok := vars[name]
		if !ok || v == "" {
			if !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			return ""
		}
		return v
	})
	if failure != nil {
		return "", failure
	}
	if len(missing) > 0 {
		parts := make([]string, len(missing))
		for i, m := range missing {
			parts[i] = "${" + m + "}"
		}
		return "", &Error{fmt.Sprintf("%s: undefined variable(s) %s. Plain names are passed in the environment or under `env:`; `vars.` and `secrets.` names are defined on the host's Variables page.", where, strings.Join(parts, ", "))}
	}
	return out, nil
}

// replaceAllSubmatch is String.replace with a function that sees the submatches — Go's
// ReplaceAllStringFunc does not expose them.
func replaceAllSubmatch(re *regexp.Regexp, s string, fn func([]string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		groups := make([]string, len(loc)/2)
		for i := range groups {
			if loc[2*i] >= 0 {
				groups[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(fn(groups))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// isNaiveNegation: an assert_gone that is nothing but a negated probe, with no guard proving the
// probe could have answered. Deliberately narrow.
func isNaiveNegation(script string) bool {
	var lines []string
	for _, l := range strings.Split(script, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "!") {
		return false
	}
	return !guardRe.MatchString(lines[0])
}

// ParseDuration is `90s`, `30m`, `2h`, `3d`, `1h30m` → ms; 0 and false for anything else. Integers
// only; a bare number is refused because "sleep after 30" cannot be read without guessing the unit.
func ParseDuration(v string) (int64, bool) {
	s := strings.TrimSpace(v)
	if !durationRe.MatchString(s) {
		return 0, false
	}
	unit := map[string]float64{"s": 1000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}
	// Summed as a JS number would be, so a digit run past int64 is still a duration (the TS accepted
	// it); the ceiling is clamped rather than wrapped.
	var ms float64
	for _, m := range durPartRe.FindAllStringSubmatch(s, -1) {
		ms += js.ParseNumber(m[1]) * unit[m[2]]
	}
	if ms <= 0 {
		return 0, false
	}
	if ms >= math.MaxInt64 {
		return math.MaxInt64, true
	}
	return int64(ms), true
}

// ResolvePreviewDomain picks the domain generated hostnames anchor to: a DECLARED PREVIEW_DOMAIN,
// then a declared DOMAIN, then the ambient ones — a declaration beats an ambient value, because a
// stray DOMAIN in a shell would otherwise anchor every rule on the wrong domain. Both set and
// disagreeing is a warning, not an error.
//
// The chain is `??`, not `||`: the first candidate that is PRESENT wins even when it is "" (a declared
// `PREVIEW_DOMAIN: ""` yields no domain rather than falling through to DOMAIN), and "" is then
// returned as "none". A map key present with "" is how Go spells that distinction.
func ResolvePreviewDomain(vars map[string]string, declared []string, warn func(string)) string {
	chosen := ""
	for _, c := range []struct {
		key      string
		declared bool
	}{{"PREVIEW_DOMAIN", true}, {"DOMAIN", true}, {"PREVIEW_DOMAIN", false}, {"DOMAIN", false}} {
		if c.declared && !contains(declared, c.key) {
			continue
		}
		if v, ok := vars[c.key]; ok {
			chosen = v
			break
		}
	}
	if vars["PREVIEW_DOMAIN"] != "" && vars["DOMAIN"] != "" && vars["PREVIEW_DOMAIN"] != vars["DOMAIN"] && warn != nil {
		warn(fmt.Sprintf(`both PREVIEW_DOMAIN (%s) and DOMAIN (%s) are set and differ; generated hostnames use "%s". Declare PREVIEW_DOMAIN in the spec to be unambiguous.`, vars["PREVIEW_DOMAIN"], vars["DOMAIN"], chosen))
	}
	return chosen
}

func asString(v any, where string) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int64, float64, bool:
		return js.ToString(x), nil
	}
	return "", &Error{fmt.Sprintf("%s: expected a string, got %s", where, jsTypeof(v))}
}

// jsNumber is `Number(v)` over a document value: booleans are 0/1, an array goes through String()
// first (`[1]` is 1, `[]` is 0, `[1,2]` is NaN), a mapping is NaN.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		return js.ParseNumber(x)
	case []any:
		return js.ParseNumber(toStr(x))
	default:
		return math.NaN()
	}
}

// jsTypeof is what `typeof v` would say about a document value.
func jsTypeof(v any) string {
	switch v.(type) {
	case nil:
		return "object"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int64, float64:
		return "number"
	default:
		return "object"
	}
}

// Parse parses and fully resolves a spec. baseEnv is the environment to interpolate against
// (usually the process env plus CLI overrides); host is nil under the bare CLI.
func Parse(source string, baseEnv map[string]string, host *HostValues) (*Stack, error) {
	docAny, err := yamlx.ParseString(source)
	if err != nil {
		return nil, &Error{"invalid YAML: " + strings.TrimPrefix(err.Error(), "not valid YAML: ")}
	}
	raw, ok := docAny.(*omap.Map)
	if !ok {
		return nil, &Error{"spec must be a YAML mapping"}
	}
	st := &Stack{Requires: []Requirement{}, Axes: []Axis{}, DeclaredEnv: []string{}, Warnings: []string{}}
	warn := func(s string) { st.Warnings = append(st.Warnings, s) }
	interp := func(v, where string, vars map[string]string) (string, error) {
		return Interpolate(v, vars, where, host)
	}

	// version: Number(raw.version ?? 1), must be exactly 1
	version := 1.0
	if v, ok := raw.Get("version"); ok && v != nil {
		version = jsNumber(v)
	}
	if version != 1 {
		return nil, &Error{fmt.Sprintf("unsupported version %s (this build understands version 1)", js.NumberString(version))}
	}
	st.Version = 1

	kind := "isolated"
	if v, ok := raw.Get("kind"); ok {
		if kind, err = asString(v, "kind"); err != nil {
			return nil, err
		}
	}
	if kind != "shared" && kind != "isolated" {
		return nil, &Error{fmt.Sprintf(`kind must be "shared" or "isolated", got "%s"`, kind)}
	}
	st.Kind = Kind(kind)

	// Start from the ambient environment, then layer the spec's own `env:` on top, in declaration
	// order so a later entry can build on an earlier one.
	vars := map[string]string{}
	for k, v := range baseEnv {
		vars[k] = v
	}
	if ev, ok := raw.Get("env"); ok {
		em, ok := ev.(*omap.Map)
		if !ok {
			return nil, &Error{"`env` must be a mapping of NAME: value"}
		}
		for _, k := range em.Keys() {
			v, _ := em.Get(k)
			s, err := asString(v, "env."+k)
			if err != nil {
				return nil, err
			}
			r, err := interp(s, "env."+k, vars)
			if err != nil {
				return nil, err
			}
			vars[k] = r
			st.DeclaredEnv = append(st.DeclaredEnv, k)
		}
	}

	sv, ok := raw.Get("stack")
	if !ok {
		return nil, &Error{"`stack` is required (the stack identity)"}
	}
	rawStack, err := asString(sv, "stack")
	if err != nil {
		return nil, err
	}
	// The stack is IDENTITY: a compose project name, a hostname label, a value in every log line. A
	// secret interpolated into it would be published by every surface redaction cannot reach, so
	// the reference itself is refused.
	if strings.Contains(rawStack, "${secrets.") {
		return nil, &Error{"stack: must not reference ${secrets.*} — the stack name appears in container names, hostnames and logs, none of which can be redacted. Use ${vars.*} for identity parts."}
	}
	stack, err := interp(rawStack, "stack", vars)
	if err != nil {
		return nil, err
	}
	if !stackRe.MatchString(stack) {
		return nil, &Error{fmt.Sprintf(`stack "%s" must match /^[a-z0-9][a-z0-9_-]*$/ — it becomes a compose project name, a hostname label and (usually) a namespace identifier.`, stack)}
	}
	st.Stack = stack
	vars["STACK"] = stack

	if cv, ok := raw.Get("compose"); ok {
		cm, ok := cv.(*omap.Map)
		if !ok {
			return nil, &Error{"`compose` must be a mapping"}
		}
		fv, ok := cm.Get("file")
		if !ok {
			return nil, &Error{"`compose.file` is required when `compose` is set"}
		}
		profilesRaw := []any{}
		if pv, ok := cm.Get("profiles"); ok {
			if profilesRaw, ok = pv.([]any); !ok {
				return nil, &Error{"`compose.profiles` must be a list"}
			}
		}
		overlaysRaw := []any{}
		if ov, ok := cm.Get("overlays"); ok {
			if overlaysRaw, ok = ov.([]any); !ok {
				return nil, &Error{"`compose.overlays` must be a list"}
			}
		}
		profiles := make([]string, 0, len(profilesRaw))
		for i, p := range profilesRaw {
			at := fmt.Sprintf("compose.profiles[%d]", i)
			s, err := asString(p, at)
			if err != nil {
				return nil, err
			}
			r, err := interp(s, at, vars)
			if err != nil {
				return nil, err
			}
			profiles = append(profiles, r)
		}
		orch := vars["PSTACK_ORCHESTRATOR"]
		fromEnv := true
		if o, ok := cm.Get("orchestrator"); ok {
			fromEnv = false
			s, err := asString(o, "compose.orchestrator")
			if err != nil {
				return nil, err
			}
			if orch, err = interp(s, "compose.orchestrator", vars); err != nil {
				return nil, err
			}
		} else if orch == "" {
			orch = "compose"
		}
		if orch != "compose" && orch != "swarm" {
			suffix := ""
			if fromEnv {
				suffix = " (from PSTACK_ORCHESTRATOR)"
			}
			return nil, &Error{fmt.Sprintf(`compose.orchestrator must be "compose" or "swarm", got "%s"%s`, orch, suffix)}
		}
		fs, err := asString(fv, "compose.file")
		if err != nil {
			return nil, err
		}
		file, err := interp(fs, "compose.file", vars)
		if err != nil {
			return nil, err
		}
		overlays := make([]string, 0, len(overlaysRaw))
		for i, o := range overlaysRaw {
			at := fmt.Sprintf("compose.overlays[%d]", i)
			s, err := asString(o, at)
			if err != nil {
				return nil, err
			}
			r, err := interp(s, at, vars)
			if err != nil {
				return nil, err
			}
			overlays = append(overlays, r)
		}
		st.Compose = &ComposeSpec{File: file, Orchestrator: Orchestrator(orch), Profiles: profiles, Overlays: overlays, Subdomains: []SubdomainRoute{}}
		// After the profiles, because a subdomain naming a profile that is never started is dead
		// config; after vars, so `host:` can interpolate ${STACK} / ${DOMAIN}.
		// The callback deliberately passes no host: the TS did the same, so a `${vars.X}` in a
		// subdomain host is refused with the control-plane message even on the server.
		var configs []SubdomainConfig
		if subRaw, ok := cm.Get("subdomains"); ok {
			if configs, err = ParseSubdomains(subRaw, "compose.subdomains", func(v, at string) (string, error) { return Interpolate(v, vars, at, nil) }); err != nil {
				return nil, err
			}
		}
		if len(configs) > 0 {
			routes, err := ResolveSubdomains(configs, stack, profiles, ResolvePreviewDomain(vars, st.DeclaredEnv, warn))
			if err != nil {
				return nil, err
			}
			st.Compose.Subdomains = routes
		}
	}

	if slv, ok := raw.Get("sleep"); ok {
		sm, ok := slv.(*omap.Map)
		if !ok {
			return nil, &Error{"`sleep` must be a mapping with `idle` and/or `after` durations (30m, 2h, 3d)"}
		}
		for _, k := range sm.Keys() {
			if k != "idle" && k != "after" {
				return nil, &Error{fmt.Sprintf("sleep.%s: unknown key (idle, after)", k)}
			}
		}
		dur := func(k string) (int64, error) {
			v, ok := sm.Get(k)
			if !ok {
				return 0, nil
			}
			s, err := asString(v, "sleep."+k)
			if err != nil {
				return 0, err
			}
			text, err := interp(s, "sleep."+k, vars)
			if err != nil {
				return 0, err
			}
			ms, ok := ParseDuration(text)
			if !ok {
				return 0, &Error{fmt.Sprintf(`sleep.%s: "%s" is not a duration — use 90s, 30m, 2h, 3d or 1h30m`, k, text)}
			}
			return ms, nil
		}
		idle, err := dur("idle")
		if err != nil {
			return nil, err
		}
		after, err := dur("after")
		if err != nil {
			return nil, err
		}
		if idle == 0 && after == 0 {
			return nil, &Error{"`sleep` declares neither `idle` nor `after` — remove it"}
		}
		st.Sleep = &SleepPolicy{IdleMs: idle, AfterMs: after}
		if st.Compose == nil {
			warn("`sleep` is set but there is no `compose` section — nothing to put to sleep.")
		}
	}

	if rv, ok := raw.Get("requires"); ok {
		list, ok := rv.([]any)
		if !ok {
			return nil, &Error{"`requires` must be a list"}
		}
		for i, r := range list {
			rm, ok := r.(*omap.Map)
			if !ok {
				return nil, &Error{fmt.Sprintf("requires[%d] must be a mapping", i)}
			}
			nameV, _ := rm.Get("name")
			if nameV == nil {
				nameV = ""
			}
			name, err := asString(nameV, fmt.Sprintf("requires[%d].name", i))
			if err != nil {
				return nil, err
			}
			if name == "" {
				return nil, &Error{fmt.Sprintf("requires[%d].name is required", i)}
			}
			av, ok := rm.Get("assert")
			if !ok {
				return nil, &Error{fmt.Sprintf("requires[%d].assert is required", i)}
			}
			as, err := asString(av, "requires."+name+".assert")
			if err != nil {
				return nil, err
			}
			assert, err := interp(as, "requires."+name+".assert", vars)
			if err != nil {
				return nil, err
			}
			hint := ""
			if hv, ok := rm.Get("hint"); ok {
				hs, err := asString(hv, "requires."+name+".hint")
				if err != nil {
					return nil, err
				}
				if hint, err = interp(hs, "requires."+name+".hint", vars); err != nil {
					return nil, err
				}
			}
			st.Requires = append(st.Requires, Requirement{Name: name, Assert: assert, Hint: hint})
		}
	}

	if av, ok := raw.Get("axes"); ok {
		list, ok := av.([]any)
		if !ok {
			return nil, &Error{"`axes` must be a list"}
		}
		seen := map[string]bool{}
		for i, a := range list {
			am, ok := a.(*omap.Map)
			if !ok {
				return nil, &Error{fmt.Sprintf("axes[%d] must be a mapping", i)}
			}
			nameV, _ := am.Get("name")
			if nameV == nil {
				nameV = ""
			}
			name, err := asString(nameV, fmt.Sprintf("axes[%d].name", i))
			if err != nil {
				return nil, err
			}
			if name == "" {
				return nil, &Error{fmt.Sprintf("axes[%d].name is required", i)}
			}
			if seen[name] {
				return nil, &Error{fmt.Sprintf(`duplicate axis name "%s"`, name)}
			}
			seen[name] = true
			field := func(k string) (string, error) {
				v, ok := am.Get(k)
				if !ok {
					return "", nil
				}
				s, err := asString(v, "axes."+name+"."+k)
				if err != nil {
					return "", err
				}
				return interp(s, "axes."+name+"."+k, vars)
			}
			ax := Axis{Name: name}
			if ax.Up, err = field("up"); err != nil {
				return nil, err
			}
			if ax.Down, err = field("down"); err != nil {
				return nil, err
			}
			if ax.AssertGone, err = field("assert_gone"); err != nil {
				return nil, err
			}
			if ax.AssertLive, err = field("assert_live"); err != nil {
				return nil, err
			}
			if ax.Up == "" && ax.Down == "" && ax.AssertGone == "" && ax.AssertLive == "" {
				return nil, &Error{fmt.Sprintf(`axis "%s" defines no up/down/assert_gone/assert_live — remove it`, name)}
			}
			if ax.Up != "" && ax.AssertGone == "" {
				warn(fmt.Sprintf("axis \"%s\" has `up` but no `assert_gone` — `pstack verify` cannot prove it was cleaned up.", name))
			}
			if ax.AssertGone != "" && isNaiveNegation(ax.AssertGone) {
				warn(fmt.Sprintf("axis \"%s\": `assert_gone` is a bare `! <probe>` with no reachability guard — it will report \"gone\" if the probe itself fails. Fail closed instead:\n      <probe-is-usable> || exit 1\n      ! <probe-for-this-resource>", name))
			}
			if ax.AssertGone != "" && orTrueRe.MatchString(ax.AssertGone) {
				warn(fmt.Sprintf("axis \"%s\": `assert_gone` contains `|| true`, which makes it always pass. Tolerate failure in `down`, never in an assert.", name))
			}
			st.Axes = append(st.Axes, ax)
		}
	}

	if st.Kind == Shared && len(st.Axes) > 0 {
		return nil, &Error{fmt.Sprintf("kind: shared cannot declare axes (found %d). Axes exist to isolate one tenant from another and to prove a tenant's resources were cleaned up; a shared singleton has neither concern. Did you mean `kind: isolated`?", len(st.Axes))}
	}
	if st.Kind == Isolated && len(st.Axes) == 0 && st.Compose != nil {
		warn("kind: isolated with no axes — nothing per-tenant is provisioned or verified, so this is just a compose project. If it is a host singleton, mark it `kind: shared` so `down` is guarded.")
	}
	st.Env = vars
	return st, nil
}

// Load parses a spec file from disk.
func Load(path string, baseEnv map[string]string, host *HostValues) (*Stack, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{"spec not found: " + path}
	}
	return Parse(string(b), baseEnv, host)
}
