// Package redact is redaction for anything a human is shown.
//
// The UI wants to display a deployment's configuration, and a spec's `env:` block is where
// connection strings and tokens legitimately live. So values are masked by DEFAULT and revealed
// only when the name looks unambiguously harmless — deny-by-default, because the cost of the two
// mistakes is wildly asymmetric: wrongly masking `PREVIEW_DOMAIN` is a mild annoyance, wrongly
// revealing `DATABASE_URL` in a browser tab (or a screenshot, or a support ticket) is a breach.
//
// This is defence in depth, NOT the primary control. The primary control is that responses are
// built field-by-field and `Stack.Env` is never serialised — see the api package. If you find
// yourself relying on this package to keep a secret out of a response, the response is wrong.
package redact

import (
	"regexp"
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
)

// safe are names that are safe in the clear: describe topology or scale, never authorise anything.
var safe = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^PR(_NUMBER)?$`),
	regexp.MustCompile(`(?i)^STACK$`),
	regexp.MustCompile(`(?i)_?DOMAIN$`),
	regexp.MustCompile(`(?i)_?HOST(NAME)?$`),
	regexp.MustCompile(`(?i)_?PORT$`),
	regexp.MustCompile(`(?i)_?REGION$`),
	regexp.MustCompile(`(?i)_?ENV(IRONMENT)?$`),
	regexp.MustCompile(`(?i)_?PROFILE$`),
	regexp.MustCompile(`(?i)_?TAG$`),
	regexp.MustCompile(`(?i)_?VERSION$`),
	regexp.MustCompile(`(?i)_?REPLICAS$`),
	regexp.MustCompile(`(?i)_?TIMEOUT$`),
	regexp.MustCompile(`(?i)_?LOG_LEVEL$`),
	regexp.MustCompile(`(?i)^GIT_(SHA|REF|BRANCH)$`),
	regexp.MustCompile(`(?i)_?IMAGE$`),
}

// secret are names that are secret even though they might read as safe. Checked FIRST, because the
// safe list is pattern-based and a name like `REGISTRY_IMAGE_PULL_SECRET` would otherwise match
// `_IMAGE`.
var secret = []*regexp.Regexp{
	regexp.MustCompile(`(?i)TOKEN`),
	regexp.MustCompile(`(?i)SECRET`),
	regexp.MustCompile(`(?i)PASSWORD`),
	regexp.MustCompile(`(?i)PASSWD`),
	regexp.MustCompile(`(?i)CREDENTIAL`),
	regexp.MustCompile(`(?i)PRIVATE`),
	regexp.MustCompile(`(?i)_KEY$`),
	regexp.MustCompile(`(?i)^KEY$`),
	regexp.MustCompile(`(?i)API_?KEY`),
	regexp.MustCompile(`(?i)AUTH`),
	regexp.MustCompile(`(?i)SESSION`),
	regexp.MustCompile(`(?i)COOKIE`),
	regexp.MustCompile(`(?i)SALT`),
	regexp.MustCompile(`(?i)SIGNATURE`),
	// A URL with credentials in it — postgres://user:pass@host — is the single most common leak.
	regexp.MustCompile(`(?i)(DATABASE|DB|REDIS|AMQP|MONGO|POSTGRES|MYSQL)_?(URL|URI|DSN|CONN)`),
	regexp.MustCompile(`(?i)^DSN$`),
}

// Visibility is whether a DisplayVar carries the real value or a mask.
type Visibility string

const (
	Shown  Visibility = "shown"
	Masked Visibility = "masked"
)

// DisplayVar is one variable, ready to render.
type DisplayVar struct {
	Key string `json:"key"`
	// Value is the value, or a mask. Never the real value when Visibility is Masked.
	Value      string     `json:"value"`
	Visibility Visibility `json:"visibility"`
	// Length is the character count (UTF-16 units, as `.length` counted it) of the real value —
	// useful for "did it get set at all?" without revealing it.
	Length int `json:"length"`
}

// IsSecretName reports whether a variable name must be masked.
func IsSecretName(key string) bool {
	for _, re := range secret {
		if re.MatchString(key) {
			return true
		}
	}
	for _, re := range safe {
		if re.MatchString(key) {
			return false
		}
	}
	return true
}

// Mask masks preserving only shape. Not a prefix/suffix reveal: a 4-character prefix of a
// 20-character token is a meaningful head start for an attacker and buys the operator almost
// nothing.
func Mask(value string) string {
	n := js.Len(value)
	if n == 0 {
		return "(empty)"
	}
	return strings.Repeat("•", min(n, 12))
}

// Display is the TS displayVar: one variable, ready to render.
func Display(key, value string) DisplayVar {
	isSecret := IsSecretName(key)
	v := DisplayVar{Key: key, Value: value, Visibility: Shown, Length: js.Len(value)}
	if isSecret {
		v.Value = Mask(value)
		v.Visibility = Masked
	}
	return v
}

// DisplayDeclared renders a spec's DECLARED variables only.
//
// `Stack.Env` also contains the entire ambient environment (see its doc comment), so iterating it
// would dump every secret the process holds. declaredEnv is the authored key list. Never nil.
func DisplayDeclared(declaredEnv []string, env map[string]string) []DisplayVar {
	out := make([]DisplayVar, 0, len(declaredEnv))
	for _, k := range declaredEnv {
		out = append(out, Display(k, env[k])) // `env[k] ?? ''` — a missing key renders as empty
	}
	return out
}

var (
	// A swarm join token. Whoever holds one can add a node that runs any task on the cluster, and
	// `docker swarm join-token` prints it in a line a hook could easily echo.
	swarmToken = regexp.MustCompile(`SWMTKN-[A-Za-z0-9-]+`)
	// scheme://user:password@host  →  keep the shape, drop the password
	urlPassword = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^\s:/@]+):[^\s@]+@`)
	// NAME=value where NAME reads as a secret
	secretAssign = regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|CREDENTIAL)[A-Z0-9_]*)=(\S+)`)
)

// RedactText strips anything that looks like a secret out of free text — command output, a log
// line, an error. Values are matched by content, since a log line has no key to inspect.
//
// Deliberately conservative: it catches the shapes that actually leak (a URL with a password, a
// long opaque token assigned to a secret-looking name) and does not attempt to be exhaustive. Text
// shown to a human is a weaker channel than a JSON field, but hook output is written by the spec
// author and can echo anything.
//
// `\s` here is RE2's ASCII class where JS's also covered Unicode spaces (U+00A0, U+FEFF, …): a
// password containing a non-breaking space is redacted one character shorter than Bun would. A
// documented divergence, not worth a hand-rolled class.
func RedactText(text string, extraSecrets ...string) string {
	out := text
	// Longest first, so a secret containing another secret is not partially replaced.
	extras := make([]string, 0, len(extraSecrets))
	for _, s := range extraSecrets {
		if js.Len(s) >= 8 {
			extras = append(extras, s)
		}
	}
	sort.SliceStable(extras, func(i, j int) bool { return js.Len(extras[i]) > js.Len(extras[j]) })
	for _, s := range extras {
		out = strings.ReplaceAll(out, s, "••••••")
	}
	out = swarmToken.ReplaceAllString(out, "SWMTKN-••••••")
	out = urlPassword.ReplaceAllString(out, "${1}:••••@")
	out = secretAssign.ReplaceAllString(out, "${1}=••••••")
	return out
}
