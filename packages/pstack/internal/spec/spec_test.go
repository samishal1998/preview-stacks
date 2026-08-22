package spec

// Ported from test/stack.test.ts (describe 'spec', 'assert_gone lint', the parse-level half of
// 'deployment kinds' and 'requires — preflight', 'PREVIEW_DOMAIN vs DOMAIN') and
// test/features.test.ts (describe 'spec: orchestrator and sleep'). The TS read a module-global
// `warnings`; here it is Stack.Warnings. Every subtest names the mutation that fails it.

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parse is the TS `spec(yaml, env = { PR: '7' })`: a parse that must succeed.
func parse(t *testing.T, yaml string, env map[string]string) *Stack {
	t.Helper()
	if env == nil {
		env = map[string]string{"PR": "7"}
	}
	s, err := Parse(yaml, env, nil)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return s
}

// parseErr is `expect(() => parse(...)).toThrow(SpecError)`: the error, which must be a *Error.
func parseErr(t *testing.T, yaml string, env map[string]string) error {
	t.Helper()
	if env == nil {
		env = map[string]string{"PR": "7"}
	}
	_, err := Parse(yaml, env, nil)
	if err == nil {
		t.Fatal("expected a spec error, got none")
	}
	if !IsSpecError(err) {
		t.Fatalf("expected *spec.Error, got %T: %v", err, err)
	}
	return err
}

func match(t *testing.T, pattern, s string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(s) {
		t.Fatalf("expected %q to match /%s/", s, pattern)
	}
}

func noMatch(t *testing.T, pattern, s string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(s) {
		t.Fatalf("expected %q NOT to match /%s/", s, pattern)
	}
}

func warningsOf(s *Stack) string { return strings.Join(s.Warnings, "\n") }

func TestSpec(t *testing.T) {
	t.Run("interpolates and exposes STACK", func(t *testing.T) {
		// negative control: drop `vars["STACK"] = stack` in Parse — the hook reads `echo app-7.example.com ` with STACK undefined (a spec error).
		s := parse(t, fixture(t, "interpolates.yml"), nil)
		if s.Stack != "pr-7" {
			t.Fatalf("stack = %q", s.Stack)
		}
		if s.Env["HOST"] != "app-7.example.com" {
			t.Fatalf("HOST = %q", s.Env["HOST"])
		}
		// STACK is available to hooks without them reconstructing it
		if s.Axes[0].Up != "echo app-7.example.com pr-7" {
			t.Fatalf("up = %q", s.Axes[0].Up)
		}
	})

	t.Run("an undefined variable is a hard error, not an empty string", func(t *testing.T) {
		// negative control: make Interpolate substitute "" for a missing name instead of recording it — `pr-` parses.
		// The bug this prevents: `pr-${PR}` with PR unset silently becomes `pr-`, which every PR
		// then shares — cross-PR collision instead of isolation.
		parseErr(t, "stack: pr-${NOPE}\naxes: []", map[string]string{})
		_, err := Interpolate("${A}", map[string]string{}, "x", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		match(t, "undefined variable", err.Error())
	})

	t.Run("rejects a stack name that cannot be a compose project / hostname label", func(t *testing.T) {
		// negative control: loosen stackRe to `.*`.
		match(t, "must match", parseErr(t, "stack: PR_Seven!\naxes: []", nil).Error())
	})

	t.Run("rejects duplicate axis names and empty axes", func(t *testing.T) {
		// negative control: delete the `seen[name]` check, or the four-hooks-empty guard.
		match(t, "duplicate", parseErr(t, "stack: s\naxes:\n  - name: a\n    up: x\n  - name: a\n    up: y", nil).Error())
		match(t, `defines no up/down`, parseErr(t, "stack: s\naxes:\n  - name: a", nil).Error())
	})

	t.Run("warns when an axis can be created but never proven gone", func(t *testing.T) {
		// negative control: remove the `ax.Up != "" && ax.AssertGone == ""` warning.
		s := parse(t, "stack: s\naxes:\n  - name: a\n    up: touch f", nil)
		match(t, "no `assert_gone`", strings.Join(s.Warnings, ","))
	})

	t.Run("unsupported version is rejected", func(t *testing.T) {
		// negative control: accept any version.
		match(t, "unsupported version", parseErr(t, "version: 2\nstack: s\naxes: []", nil).Error())
	})
}

func TestAssertGoneLint(t *testing.T) {
	// 'assert_gone lint — protecting the core promise'
	env := map[string]string{"PR": "1"}

	t.Run("flags a bare `! probe` with no reachability guard", func(t *testing.T) {
		// negative control: make isNaiveNegation return false.
		// `! docker exec …` exits 0 when docker itself is missing, so "cannot tell" reads as "gone".
		s := parse(t, "stack: s\naxes:\n  - name: q\n    up: \"true\"\n    assert_gone: \"! docker exec c probe\"", env)
		match(t, "no reachability guard", warningsOf(s))
	})

	t.Run("accepts a guarded, fail-closed assert", func(t *testing.T) {
		// negative control: drop the `len(lines) != 1` check in isNaiveNegation — the two-line guarded form is flagged.
		s := parse(t, fixture(t, "guarded-assert.yml"), env)
		noMatch(t, "reachability guard", warningsOf(s))
	})

	t.Run("flags `|| true` in an assert, which can never fail", func(t *testing.T) {
		// negative control: remove the orTrueRe warning.
		s := parse(t, "stack: s\naxes:\n  - name: q\n    up: \"true\"\n    assert_gone: \"probe || true\"", env)
		match(t, "always pass", warningsOf(s))
	})
}

func TestDeploymentKinds(t *testing.T) {
	// Only the parse-level half of 'deployment kinds'; the `down` guard belongs to stack.
	env := map[string]string{"PR": "1"}

	t.Run("defaults to isolated", func(t *testing.T) {
		// negative control: default kind to "shared".
		if k := parse(t, "stack: s\naxes: []", env).Kind; k != Isolated {
			t.Fatalf("kind = %q", k)
		}
	})

	t.Run("rejects an unknown kind", func(t *testing.T) {
		// negative control: accept any kind string.
		match(t, `must be "shared" or "isolated"`, parseErr(t, "kind: wat\nstack: s\naxes: []", env).Error())
	})

	t.Run("a shared deployment cannot declare axes", func(t *testing.T) {
		// negative control: remove the Shared && len(Axes) > 0 refusal.
		// Axes isolate one tenant from another; a singleton has no such concern. Almost always a spec
		// that meant `kind: isolated`.
		match(t, "cannot declare axes", parseErr(t, "kind: shared\nstack: s\naxes:\n  - name: db\n    up: \"true\"", env).Error())
	})

	t.Run("warns when an isolated deployment has no axes", func(t *testing.T) {
		// negative control: remove the isolated-with-no-axes warning.
		s := parse(t, "kind: isolated\nstack: s\ncompose: {file: dc.yml, profiles: []}\naxes: []", env)
		match(t, "nothing per-tenant", warningsOf(s))
	})
}

func TestRequiresPreflight(t *testing.T) {
	// The parse half of 'requires — preflight': the requirement and the axis both come through with
	// their strings intact (the hint carries backticks). Running them is stack's test.
	t.Run("parses name, assert and hint", func(t *testing.T) {
		// negative control: drop the hint interpolation (hint stays "") or reorder Requires.
		s := parse(t, fixture(t, "requires.yml"), map[string]string{})
		want := []Requirement{{Name: "ingress network", Assert: "docker network inspect net", Hint: "run `pstack init` first"}}
		if !reflect.DeepEqual(s.Requires, want) {
			t.Fatalf("requires = %#v", s.Requires)
		}
		if len(s.Axes) != 1 || s.Axes[0].Up != "echo made-db" {
			t.Fatalf("axes = %#v", s.Axes)
		}
	})

	t.Run("a requirement without an assert is refused", func(t *testing.T) {
		// negative control: make a missing `assert` an empty string instead of an error.
		match(t, `requires\[0\]\.assert is required`, parseErr(t, "stack: s\nrequires:\n  - name: x", nil).Error())
	})
}

func TestPreviewDomainVsDomain(t *testing.T) {
	// 'PREVIEW_DOMAIN vs DOMAIN — one thing, two names'. PREVIEW_DOMAIN is what every example, doc
	// and the skill use; DOMAIN is accepted only because 0.3.0–0.7.0 read nothing else.
	t.Run("a declaration beats an ambient value, and PREVIEW_DOMAIN beats DOMAIN", func(t *testing.T) {
		// negative control: drop the `declared` step so the ambient PREVIEW_DOMAIN always wins — case two returns "ambient.example".
		// The bug this closes: `vars` is seeded from the whole process environment, so a stray exported
		// DOMAIN would anchor every generated hostname — silently, on the wrong domain.
		if got := ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "declared.example", "DOMAIN": "ambient.example"}, []string{"PREVIEW_DOMAIN"}, nil); got != "declared.example" {
			t.Fatalf("got %q", got)
		}
		// A spec that declared DOMAIN (as the 0.3.0–0.7.0 docs said to) still wins over an ambient
		// PREVIEW_DOMAIN.
		if got := ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "ambient.example", "DOMAIN": "declared.example"}, []string{"DOMAIN"}, nil); got != "declared.example" {
			t.Fatalf("got %q", got)
		}
		// Neither declared: the specific name wins.
		if got := ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "a.example", "DOMAIN": "b.example"}, nil, nil); got != "a.example" {
			t.Fatalf("got %q", got)
		}
		// Only the legacy name, anywhere: still works.
		if got := ResolvePreviewDomain(map[string]string{"DOMAIN": "b.example"}, nil, nil); got != "b.example" {
			t.Fatalf("got %q", got)
		}
		if got := ResolvePreviewDomain(map[string]string{}, nil, nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a present-but-empty name does not fall through (the chain is ??, not ||)", func(t *testing.T) {
		// negative control: pick on `v != ""` instead of presence — DOMAIN wins and "b.example" comes back.
		if got := ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "", "DOMAIN": "b.example"}, []string{"PREVIEW_DOMAIN"}, nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("both set and disagreeing warns rather than refusing to parse", func(t *testing.T) {
		// negative control: remove the warn call, or warn on equal values too.
		// A normal accident — an ambient DOMAIN beside a declared PREVIEW_DOMAIN — so failing would be
		// hostile. But the ambiguity has to be visible.
		var warnings []string
		warn := func(w string) { warnings = append(warnings, w) }
		ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "a.example", "DOMAIN": "b.example"}, []string{"PREVIEW_DOMAIN"}, warn)
		match(t, "both PREVIEW_DOMAIN .* and DOMAIN .* are set and differ", strings.Join(warnings, ","))
		// Agreeing is not worth a word.
		warnings = nil
		ResolvePreviewDomain(map[string]string{"PREVIEW_DOMAIN": "same.example", "DOMAIN": "same.example"}, nil, warn)
		noMatch(t, "differ", strings.Join(warnings, ","))
	})

	t.Run("the repo's own example spec shape now works end to end", func(t *testing.T) {
		// negative control: make ResolvePreviewDomain read only DOMAIN — "needs a domain" again.
		// This is what was broken: a spec copied from examples/preview.yml declares PREVIEW_DOMAIN, and
		// subdomains refused it with "needs a domain to anchor its rules to".
		s := parse(t, fixture(t, "preview-domain.yml"), nil)
		if h := s.Compose.Subdomains[0].Host; h != "backend-pr-7.preview.example.com" {
			t.Fatalf("host = %q", h)
		}
	})
}

func TestOrchestratorAndSleep(t *testing.T) {
	// features.test.ts 'spec: orchestrator and sleep'
	base := "version: 1\nstack: s\ncompose: {file: dc.yml, profiles: []}\naxes: []\n"
	none := map[string]string{}

	t.Run("orchestrator: spec key beats PSTACK_ORCHESTRATOR beats compose", func(t *testing.T) {
		// negative control: read the spec key only when PSTACK_ORCHESTRATOR is unset — case three returns "swarm".
		if o := parse(t, base, none).Compose.Orchestrator; o != Compose {
			t.Fatalf("got %q", o)
		}
		if o := parse(t, base, map[string]string{"PSTACK_ORCHESTRATOR": "swarm"}).Compose.Orchestrator; o != Swarm {
			t.Fatalf("got %q", o)
		}
		if o := parse(t, strings.Replace(base, "profiles: []", "profiles: [], orchestrator: compose", 1), map[string]string{"PSTACK_ORCHESTRATOR": "swarm"}).Compose.Orchestrator; o != Compose {
			t.Fatalf("got %q", o)
		}
		err := parseErr(t, base, map[string]string{"PSTACK_ORCHESTRATOR": "nomad"})
		match(t, "compose.orchestrator must be", err.Error())
		// The suffix names the source when the spec did not say.
		if want := `compose.orchestrator must be "compose" or "swarm", got "nomad" (from PSTACK_ORCHESTRATOR)`; err.Error() != want {
			t.Fatalf("got %q", err.Error())
		}
	})

	t.Run("sleep: durations parse, junk is refused, an empty block is refused", func(t *testing.T) {
		// negative control: make ParseDuration accept a bare number ("30" → 30000) or miscount `h`.
		cases := map[string]int64{"90s": 90_000, "1h30m": 5_400_000, "3d": 259_200_000}
		for in, want := range cases {
			ms, ok := ParseDuration(in)
			if !ok || ms != want {
				t.Fatalf("ParseDuration(%q) = %d, %v", in, ms, ok)
			}
		}
		for _, junk := range []string{"30", "2 hours", "", "0s", "1x"} {
			if ms, ok := ParseDuration(junk); ok {
				t.Fatalf("ParseDuration(%q) = %d, accepted", junk, ms)
			}
		}
		s := parse(t, base+"sleep: {idle: 2h, after: 3d}\n", none)
		if !reflect.DeepEqual(s.Sleep, &SleepPolicy{IdleMs: 7_200_000, AfterMs: 259_200_000}) {
			t.Fatalf("sleep = %#v", s.Sleep)
		}
		parseErr(t, base+"sleep: {idle: soon}\n", none)
		match(t, "neither", parseErr(t, base+"sleep: {}\n", none).Error())
		match(t, "unknown key", parseErr(t, base+"sleep: {idle: 1h, cron: x}\n", none).Error())
		noCompose := parse(t, "version: 1\nstack: s\naxes: []\nsleep: {idle: 1h}\n", none)
		found := false
		for _, w := range noCompose.Warnings {
			if strings.Contains(w, "no `compose` section") {
				found = true
			}
		}
		if !found {
			t.Fatalf("warnings = %q", noCompose.Warnings)
		}
	})

	t.Run("a digit run past int64 is still a duration, clamped", func(t *testing.T) {
		// negative control: sum in int64 (the first port did) — MaxInt64*1000 wraps to -1000 and the value is refused.
		ms, ok := ParseDuration("99999999999999999999s")
		if !ok || ms != math.MaxInt64 {
			t.Fatalf("got %d, %v", ms, ok)
		}
	})
}

func TestPresentNullIsNotAbsent(t *testing.T) {
	// The TS tested `=== undefined`: a key written with no value is present and null, and every
	// one of these is an error there. The port once read them as absent.
	cases := []struct{ name, yaml, want string }{
		// negative control for each: go back to `ok && v != nil` at that site — the spec parses.
		{"kind", "kind:\nstack: s\n", "kind: expected a string, got object"},
		{"env", "env:\nstack: s\n", "`env` must be a mapping of NAME: value"},
		{"compose", "stack: s\ncompose:\n", "`compose` must be a mapping"},
		{"compose.profiles", "stack: s\ncompose: {file: dc.yml, profiles: ~}\n", "`compose.profiles` must be a list"},
		{"compose.overlays", "stack: s\ncompose: {file: dc.yml, overlays: ~}\n", "`compose.overlays` must be a list"},
		{"compose.orchestrator", "stack: s\ncompose: {file: dc.yml, orchestrator: ~}\n", "compose.orchestrator: expected a string, got object"},
		{"compose.file", "stack: s\ncompose: {file: ~}\n", "compose.file: expected a string, got object"},
		{"compose.subdomains", "stack: s\ncompose: {file: dc.yml, subdomains: ~}\n", "compose.subdomains must be a list of profile names or a mapping of profile to options"},
		{"sleep", "stack: s\nsleep:\n", "`sleep` must be a mapping with `idle` and/or `after` durations (30m, 2h, 3d)"},
		{"sleep.idle", "stack: s\nsleep: {idle: ~}\n", "sleep.idle: expected a string, got object"},
		{"requires", "stack: s\nrequires:\n", "`requires` must be a list"},
		{"requires[].assert", "stack: s\nrequires: [{name: x, assert: ~}]\n", "requires.x.assert: expected a string, got object"},
		{"requires[].hint", "stack: s\nrequires: [{name: x, assert: y, hint: ~}]\n", "requires.x.hint: expected a string, got object"},
		{"axes", "stack: s\naxes:\n", "`axes` must be a list"},
		{"axes[].up", "stack: s\naxes: [{name: a, up: ~}]\n", "axes.a.up: expected a string, got object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseErr(t, c.yaml, nil).Error(); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}

	t.Run("version null is the default, as `?? 1` says", func(t *testing.T) {
		// negative control: treat a null version as Number(null) = 0 — "unsupported version 0".
		if v := parse(t, "version:\nstack: s\n", nil).Version; v != 1 {
			t.Fatalf("version = %d", v)
		}
	})

	t.Run("version is coerced the way Number() coerces", func(t *testing.T) {
		// negative control: coerce via ToString — `true` becomes NaN and is refused.
		if v := parse(t, "version: true\nstack: s\n", nil).Version; v != 1 {
			t.Fatalf("version = %d", v)
		}
		if got := parseErr(t, "version: {a: 1}\nstack: s\n", nil).Error(); got != "unsupported version NaN (this build understands version 1)" {
			t.Fatalf("got %q", got)
		}
		if got := parseErr(t, "version: 1.5\nstack: s\n", nil).Error(); got != "unsupported version 1.5 (this build understands version 1)" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestErrorMessages(t *testing.T) {
	// The exact texts: specs.findRequiredVars scrapes the undefined-variable sentence, and the CLI
	// golden validate-missing-var pins it byte for byte.
	t.Run("undefined variables, first-seen order, once each", func(t *testing.T) {
		// negative control: collect into a Go map and range it — the order flips between runs; or drop `seen` — ${A} is listed twice.
		_, err := Interpolate("${B}-${A}-${B}", map[string]string{}, "stack", nil)
		want := "stack: undefined variable(s) ${B}, ${A}. Plain names are passed in the environment or under `env:`; `vars.` and `secrets.` names are defined on the host's Variables page."
		if err == nil || err.Error() != want {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an empty value counts as undefined (invariant 7)", func(t *testing.T) {
		// negative control: test presence only (`!ok`) — "" substitutes and `pr-` parses.
		parseErr(t, "stack: pr-${PR}\n", map[string]string{"PR": ""})
	})

	t.Run("${vars.X} without a host store names the boundary", func(t *testing.T) {
		// negative control: fall through to the undefined-variable message when host is nil.
		_, err := Interpolate("${vars.REGION}", map[string]string{}, "env.R", nil)
		want := "env.R: ${vars.REGION} — host vars live on the control plane. Submit this spec to a pstack server, or use a plain ${REGION} and pass the value yourself."
		if err == nil || err.Error() != want {
			t.Fatalf("got %v", err)
		}
		// With a host store, vars and secrets resolve — and a missing one is reported namespaced.
		host := &HostValues{Vars: map[string]string{"REGION": "eu"}, Secrets: map[string]string{"TOKEN": "t0k"}}
		got, err := Interpolate("${vars.REGION}/${secrets.TOKEN}/${PR}", map[string]string{"PR": "7"}, "x", host)
		if err != nil || got != "eu/t0k/7" {
			t.Fatalf("got %q, %v", got, err)
		}
		_, err = Interpolate("${secrets.NOPE}", map[string]string{}, "x", host)
		match(t, `undefined variable\(s\) \$\{secrets\.NOPE\}`, err.Error())
	})

	t.Run("the stack refuses a secrets reference before resolving it", func(t *testing.T) {
		// negative control: drop the `${secrets.` check — with no host the message is the control-plane one instead.
		match(t, "must not reference", parseErr(t, "stack: ${secrets.S}\n", nil).Error())
	})

	t.Run("invalid YAML and a non-mapping document", func(t *testing.T) {
		// negative control: return the raw yamlx error — no `invalid YAML: ` prefix, not a *Error.
		match(t, "^invalid YAML: ", parseErr(t, "stack: [\n", nil).Error())
		if got := parseErr(t, "- a\n- b\n", nil).Error(); got != "spec must be a YAML mapping" {
			t.Fatalf("got %q", got)
		}
		if got := parseErr(t, "axes: []\n", nil).Error(); got != "`stack` is required (the stack identity)" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a missing file is a spec error", func(t *testing.T) {
		// negative control: return the os error unwrapped.
		_, err := Load(filepath.Join(t.TempDir(), "nope.yml"), nil, nil)
		var e *Error
		if !errors.As(err, &e) || !strings.HasPrefix(e.Msg, "spec not found: ") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestExamples(t *testing.T) {
	// The CLI golden validate-example pins the printed form; this pins the structure it was printed
	// from. Both example specs, with the env the golden used.
	t.Run("examples/preview.yml with PR=1 GIT_SHA=ci", func(t *testing.T) {
		// negative control: reverse the axes, or drop an axis hook from Hooks() — the names/hooks list differs.
		s, err := Load(filepath.Join("..", "..", "examples", "preview.yml"), map[string]string{"PR": "1", "GIT_SHA": "ci"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.Stack != "pr-1" || s.Kind != Isolated || s.Version != 1 {
			t.Fatalf("stack=%q kind=%q version=%d", s.Stack, s.Kind, s.Version)
		}
		if s.Compose == nil || s.Compose.File != "docker-compose.preview.yml" || s.Compose.Orchestrator != Compose {
			t.Fatalf("compose = %#v", s.Compose)
		}
		if !reflect.DeepEqual(s.Compose.Profiles, []string{"backend", "frontend"}) || len(s.Compose.Overlays) != 0 || len(s.Compose.Subdomains) != 0 {
			t.Fatalf("compose = %#v", s.Compose)
		}
		if s.Compose.Overlays == nil || s.Compose.Subdomains == nil {
			t.Fatal("nil slices in ComposeSpec")
		}
		if !reflect.DeepEqual(s.DeclaredEnv, []string{"PR_NUMBER", "PREVIEW_DOMAIN", "REGISTRY", "IMAGE_TAG"}) {
			t.Fatalf("declaredEnv = %q", s.DeclaredEnv)
		}
		for k, want := range map[string]string{"PR_NUMBER": "1", "PREVIEW_DOMAIN": "preview.example.com", "IMAGE_TAG": "ci", "STACK": "pr-1", "REGISTRY": "<region>-docker.pkg.dev/<your-project>/<repo>"} {
			if s.Env[k] != want {
				t.Fatalf("env[%s] = %q", k, s.Env[k])
			}
		}
		// The lines the golden prints, in order: `- <name>: <hooks>`.
		want := []string{
			"database: up, down, assert_gone, assert_live",
			"queue-namespace: up, down, assert_gone, assert_live",
			"images: down, assert_gone",
			"ingress: assert_gone",
		}
		var got []string
		for _, a := range s.Axes {
			got = append(got, a.Name+": "+strings.Join(a.Hooks(), ", "))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("axes = %q", got)
		}
		// Hooks are resolved: ${IMAGE_TAG} and ${PREVIEW_DOMAIN} expanded, `$STACK` left to bash.
		if !strings.Contains(s.Axes[2].AssertGone, `grep -q ":ci\$"`) {
			t.Fatalf("images.assert_gone = %q", s.Axes[2].AssertGone)
		}
		if !strings.Contains(s.Axes[3].AssertGone, `"https://backend-pr-1.preview.example.com/health"`) {
			t.Fatalf("ingress.assert_gone = %q", s.Axes[3].AssertGone)
		}
		if !strings.Contains(s.Axes[0].Up, `recreate "$STACK"`) {
			t.Fatalf("database.up = %q", s.Axes[0].Up)
		}
		if len(s.Requires) != 0 || s.Requires == nil || s.Sleep != nil {
			t.Fatalf("requires=%#v sleep=%#v", s.Requires, s.Sleep)
		}
		// Every assert_gone is guarded, no `|| true` in an assert: the golden shows no `!` lines.
		if len(s.Warnings) != 0 {
			t.Fatalf("warnings = %q", s.Warnings)
		}
	})

	t.Run("examples/shared.yml", func(t *testing.T) {
		// negative control: drop the isolated-with-no-axes warning — the golden's `!` line vanishes.
		s, err := Load(filepath.Join("..", "..", "examples", "shared.yml"), map[string]string{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.Stack != "shared" || s.Kind != Isolated {
			t.Fatalf("stack=%q kind=%q", s.Stack, s.Kind)
		}
		if s.Compose == nil || s.Compose.File != "docker-compose.shared.yml" || len(s.Compose.Profiles) != 0 || s.Compose.Profiles == nil {
			t.Fatalf("compose = %#v", s.Compose)
		}
		if !reflect.DeepEqual(s.DeclaredEnv, []string{"PREVIEW_DOMAIN", "ACME_EMAIL"}) || len(s.Axes) != 0 || s.Axes == nil {
			t.Fatalf("declaredEnv=%q axes=%#v", s.DeclaredEnv, s.Axes)
		}
		want := []string{"kind: isolated with no axes — nothing per-tenant is provisioned or verified, so this is just a compose project. If it is a host singleton, mark it `kind: shared` so `down` is guarded."}
		if !reflect.DeepEqual(s.Warnings, want) {
			t.Fatalf("warnings = %q", s.Warnings)
		}
	})

	t.Run("examples/preview.yml without PR is the golden's exit-3 message", func(t *testing.T) {
		// negative control: report the missing name without the `env.PR_NUMBER: ` site — the golden stderr differs.
		_, err := Load(filepath.Join("..", "..", "examples", "preview.yml"), map[string]string{"GIT_SHA": "ci"}, nil)
		want := "env.PR_NUMBER: undefined variable(s) ${PR}. Plain names are passed in the environment or under `env:`; `vars.` and `secrets.` names are defined on the host's Variables page."
		if err == nil || err.Error() != want {
			t.Fatalf("got %v", err)
		}
	})
}
