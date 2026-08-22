package redact

import (
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

// Ported from test/stack.test.ts 'redaction — what a browser may see' and the redactText
// assertion in test/features.test.ts 'swarmInfo and the API routes; the join token is admin-only text'.

func TestRedaction(t *testing.T) {
	t.Run("secret-looking names are masked, topology names are shown", func(t *testing.T) {
		// negative control: drop `(?i)_?PORT$` from safe — PORT reads as secret.
		for _, k := range []string{"DATABASE_URL", "PSTACK_TOKEN", "API_AUTH_KEY", "STRIPE_SECRET", "DSN", "SESSION_KEY"} {
			if !IsSecretName(k) {
				t.Errorf("IsSecretName(%q) = false, want true", k)
			}
		}
		for _, k := range []string{"PR", "PR_NUMBER", "PREVIEW_DOMAIN", "PORT", "IMAGE_TAG", "GIT_SHA", "LOG_LEVEL"} {
			if IsSecretName(k) {
				t.Errorf("IsSecretName(%q) = true, want false", k)
			}
		}
	})

	t.Run("deny by default — an unrecognised name is treated as secret", func(t *testing.T) {
		// negative control: make the fall-through `return false`.
		// The asymmetry that drives this: wrongly masking a domain is an annoyance, wrongly revealing
		// a connection string in a browser tab is a breach.
		if !IsSecretName("SOMETHING_NOBODY_ANTICIPATED") {
			t.Fatal("unrecognised name should be secret")
		}
	})

	t.Run("a secret name wins over a safe-looking suffix", func(t *testing.T) {
		// negative control: check the safe list before the secret list.
		// `_IMAGE` is on the safe list, but this is a pull secret.
		if !IsSecretName("REGISTRY_IMAGE_PULL_SECRET") {
			t.Fatal("REGISTRY_IMAGE_PULL_SECRET should be secret")
		}
		// `_?IMAGE$` is end-anchored, so the name above never reached the safe list; this one does
		// (`_?HOST$`) and is still secret because AUTH is checked first.
		if !IsSecretName("AUTH_HOST") {
			t.Fatal("AUTH_HOST should be secret — the secret list is checked first")
		}
	})

	t.Run("mask reveals no prefix of the real value", func(t *testing.T) {
		// negative control: return value[:4] + "••••" from Mask.
		// A deliberately fake-looking fixture: a realistic `sk_live_…` shape trips secret scanners on
		// every commit, and the property under test (no prefix survives) does not need realism.
		m := Mask("NOT-A-REAL-TOKEN-0123456789")
		if strings.Contains(m, "NOT-A-REAL") {
			t.Fatalf("mask leaked a prefix: %q", m)
		}
		if !regexp.MustCompile(`^•+$`).MatchString(m) {
			t.Fatalf("mask is not all bullets: %q", m)
		}
		if m != strings.Repeat("•", 12) {
			t.Fatalf("mask should cap at 12 bullets, got %q", m)
		}
		if Mask("") != "(empty)" {
			t.Fatalf("Mask(\"\") = %q", Mask(""))
		}
	})

	t.Run("only DECLARED vars are rendered, never the ambient environment", func(t *testing.T) {
		// negative control: range over env instead of declaredEnv — PSTACK_TOKEN appears.
		// The whole point: Stack.env holds every secret the process has.
		env := map[string]string{"PR": "7", "DATABASE_URL": "postgres://u:p@h/db", "PSTACK_TOKEN": "supersecrettoken"}
		out := DisplayDeclared([]string{"PR", "DATABASE_URL"}, env)
		keys := []string{}
		for _, v := range out {
			keys = append(keys, v.Key)
		}
		if strings.Join(keys, ",") != "PR,DATABASE_URL" {
			t.Fatalf("keys = %v", keys)
		}
		j := string(jsonx.Must(out))
		if strings.Contains(j, "supersecrettoken") || strings.Contains(j, "postgres://u:p@h/db") {
			t.Fatalf("secret leaked: %s", j)
		}
		if out[0].Value != "7" {
			t.Fatalf("PR value = %q", out[0].Value)
		}
		if out[1].Length != len("postgres://u:p@h/db") { // shape without content
			t.Fatalf("length = %d", out[1].Length)
		}
		// The wire shape the UI reads — four camelCase fields in this order.
		want := `[{"key":"PR","value":"7","visibility":"shown","length":1},{"key":"DATABASE_URL","value":"••••••••••••","visibility":"masked","length":19}]`
		if j != want {
			t.Fatalf("json = %s\nwant   %s", j, want)
		}
		if DisplayDeclared(nil, env) == nil {
			t.Fatal("DisplayDeclared must never return nil")
		}
	})

	t.Run("redactText strips URL passwords and secret assignments from output", func(t *testing.T) {
		// negative control: drop the urlPassword replacement — hunter2 survives.
		in := "connecting to postgres://admin:hunter2@db.internal/app with API_TOKEN=abc123xyz"
		r := RedactText(in)
		if strings.Contains(r, "hunter2") || strings.Contains(r, "abc123xyz") {
			t.Fatalf("leak: %q", r)
		}
		if !strings.Contains(r, "postgres://admin:••••@db.internal/app") {
			t.Fatalf("shape lost: %q", r)
		}
		if want := "connecting to postgres://admin:••••@db.internal/app with API_TOKEN=••••••"; r != want {
			t.Fatalf("got  %q\nwant %q", r, want)
		}
	})

	t.Run("redactText masks known secret values wherever they appear", func(t *testing.T) {
		// negative control: filter extras with `< 8` instead of `>= 8`.
		r := RedactText("token is supersecrettoken here", "supersecrettoken")
		if strings.Contains(r, "supersecrettoken") {
			t.Fatalf("leak: %q", r)
		}
		if r != "token is •••••• here" {
			t.Fatalf("got %q", r)
		}
		// Short extras are ignored so a 3-character "secret" does not shred every ordinary word.
		if got := RedactText("the cat sat", "cat"); got != "the cat sat" {
			t.Fatalf("short secret should be ignored: %q", got)
		}
		// Longest first: a secret containing another is replaced whole, not partially.
		if got := RedactText("x=abcdefgh-ijklmnop", "abcdefgh", "abcdefgh-ijklmnop"); got != "x=••••••" {
			t.Fatalf("longest-first: %q", got)
		}
	})

	t.Run("redactText strips a swarm join token", func(t *testing.T) {
		// negative control: drop the swarmToken replacement.
		if got := RedactText("joined with SWMTKN-1-abc-def now"); got != "joined with SWMTKN-•••••• now" {
			t.Fatalf("got %q", got)
		}
	})
}
