package spec

// Wildcard subdomain routing for a per-PR surface — subdomains.ts, verbatim in intent. It lives in
// this package rather than its own because the TS split existed only to dodge an import cycle
// over SpecError; Go has no such cycle to dodge.
//
// THE GOAL. Given profile `backend` on stack `pr-123`, make anything under
// `backend-pr-123.<domain>` reach the same place the bare host reaches. pstack does not own your
// per-PR Traefik labels; it computes the one fiddly part — a correctly escaped, correctly anchored
// Go regexp — and exposes it as `PSTACK_WILD_<PROFILE>` for a label you write.
//
// THE CEILINGS: a DNS wildcard and a certificate wildcard each match exactly ONE label, so `depth:
// any` is routing-only, permanently. `one` is the default because it is what can actually be
// delivered.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// Depth is how many labels the wildcard spans.
type Depth string

const (
	DepthOne Depth = "one"
	DepthAny Depth = "any"
)

// SubdomainRoute is one resolved wildcard.
type SubdomainRoute struct {
	Profile string `json:"profile"`
	Host    string `json:"host"`
	Depth   Depth  `json:"depth"`
	VarName string `json:"varName"`
	Rule    string `json:"rule"`
}

// EscapeHostRegexp escapes a hostname for embedding in a Go regexp. regexp.QuoteMeta escapes the
// same fourteen bytes as the JS class `[.*+?^${}()|[\]\\]` (verified against the Go source), so the
// wake-router line and every generated HostRegexp stay byte-identical.
func EscapeHostRegexp(host string) string { return regexp.QuoteMeta(host) }

// WildcardRule is the Traefik rule for one wildcard host. Anchored at both ends; the bare host is
// deliberately NOT matched — that is the exact-host router's job.
func WildcardRule(host string, depth Depth) string {
	h := EscapeHostRegexp(host)
	const label = `[a-z0-9]([a-z0-9-]*[a-z0-9])?`
	prefix := label + `\.`
	if depth == DepthAny {
		prefix = `(` + label + `\.)+`
	}
	// Traefik v3 rules quote their argument with BACKTICKS, not quotes.
	return "HostRegexp(`^" + prefix + h + "$`)"
}

// SubdomainVarName is `backend` → `PSTACK_WILD_BACKEND`. Dashes become underscores, which makes
// `admin-api` and `admin_api` collide — the resolver checks for that.
func SubdomainVarName(profile string) string {
	return "PSTACK_WILD_" + strings.ReplaceAll(strings.ToUpper(profile), "-", "_")
}

// SubdomainConfig is one `compose.subdomains` entry before the host is resolved.
type SubdomainConfig struct {
	Profile string
	Depth   Depth
	// Host is the explicit host when the spec gave one (a pointer: `host: ""` was given, and the TS
	// `??` kept it), nil to derive `<profile>-<stack>.<domain>`.
	Host *string
}

// depthOf validates a PRESENT depth value; the callers map an absent one to DepthOne themselves,
// because a present null (`depth: ~`) is refused here with `Got null.` exactly as the TS did.
func depthOf(v any, at string) (Depth, error) {
	s, _ := v.(string)
	if s != "one" && s != "any" {
		return "", &Error{fmt.Sprintf(`%s must be "one" (a single label — what DNS and TLS can cover) or "any" (any depth, routing only: no certificate can cover it). Got %s.`, at, string(jsonx.Must(v)))}
	}
	return Depth(s), nil
}

// ParseSubdomains accepts two shapes: a list of profile names (depth one, host derived) or a mapping
// of profile → depth | { depth?, host? }. Call it only when the key is present: an absent
// `compose.subdomains` is no subdomains, but a present null is the "must be a list…" error below.
func ParseSubdomains(raw any, where string, interp func(v, at string) (string, error)) ([]SubdomainConfig, error) {
	if list, ok := raw.([]any); ok {
		out := make([]SubdomainConfig, 0, len(list))
		for i, p := range list {
			s, ok := p.(string)
			if !ok {
				return nil, &Error{fmt.Sprintf("%s[%d] must be a profile name", where, i)}
			}
			v, err := interp(s, fmt.Sprintf("%s[%d]", where, i))
			if err != nil {
				return nil, err
			}
			out = append(out, SubdomainConfig{Profile: v, Depth: DepthOne})
		}
		return out, nil
	}
	m, ok := raw.(*omap.Map)
	if !ok {
		return nil, &Error{where + " must be a list of profile names or a mapping of profile to options"}
	}
	out := make([]SubdomainConfig, 0, m.Len())
	var outer error
	m.Each(func(profile string, v any) {
		if outer != nil {
			return
		}
		at := where + "." + profile
		switch x := v.(type) {
		case nil:
			// `backend:` with no value — `v ?? undefined` in the TS, so the default depth.
			out = append(out, SubdomainConfig{Profile: profile, Depth: DepthOne})
		case string:
			d, err := depthOf(x, at)
			if err != nil {
				outer = err
				return
			}
			out = append(out, SubdomainConfig{Profile: profile, Depth: d})
		case *omap.Map:
			for _, k := range x.Keys() {
				if k != "depth" && k != "host" {
					outer = &Error{fmt.Sprintf("%s.%s is not a known option — expected `depth` or `host`", at, k)}
					return
				}
			}
			var host *string
			if hv, ok := x.Get("host"); ok {
				h, err := interp(toStr(hv), at+".host")
				if err != nil {
					outer = err
					return
				}
				host = &h
			}
			d := DepthOne
			if dv, ok := x.Get("depth"); ok {
				var err error
				if d, err = depthOf(dv, at+".depth"); err != nil {
					outer = err
					return
				}
			}
			out = append(out, SubdomainConfig{Profile: profile, Depth: d, Host: host})
		default:
			outer = &Error{fmt.Sprintf(`%s must be a depth ("one"/"any") or a mapping of { depth?, host? }`, at)}
		}
	})
	return out, outer
}

// toStr is JS String(v) over a document value: null is "null", an array joins its elements with
// "," (a null element as ""), a mapping is "[object Object]".
func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = toStr(e)
			}
		}
		return strings.Join(parts, ",")
	case *omap.Map:
		return "[object Object]"
	default:
		return js.ToString(x)
	}
}

// ResolveSubdomains turns configs into routes. A missing domain is a hard error: a rule anchored on
// an empty suffix matches nothing, and a router that silently never fires is worse than a refusal.
func ResolveSubdomains(configs []SubdomainConfig, stack string, profiles []string, domain string) ([]SubdomainRoute, error) {
	if len(configs) == 0 {
		return []SubdomainRoute{}, nil
	}
	if domain == "" {
		return nil, &Error{"`compose.subdomains` needs a domain to anchor its rules to. Declare it in the spec (`env:\n  PREVIEW_DOMAIN: preview.example.com`) or give each entry an explicit `host:`. `DOMAIN` is accepted as a legacy alias. Without it the generated rule would match nothing."}
	}
	seen := map[string]string{}
	out := make([]SubdomainRoute, 0, len(configs))
	for _, c := range configs {
		if !contains(profiles, c.Profile) {
			list := "empty"
			if len(profiles) > 0 {
				list = strings.Join(profiles, ", ")
			}
			return nil, &Error{fmt.Sprintf(`compose.subdomains names profile "%s", which is not in compose.profiles (%s). Its router would never match a running service.`, c.Profile, list)}
		}
		varName := SubdomainVarName(c.Profile)
		if clash, ok := seen[varName]; ok {
			return nil, &Error{fmt.Sprintf(`profiles "%s" and "%s" both map to %s (a dash and an underscore are the same character in an environment variable name), so one rule would overwrite the other. Rename one profile.`, clash, c.Profile, varName)}
		}
		seen[varName] = c.Profile
		host := c.Profile + "-" + stack + "." + domain
		if c.Host != nil {
			host = *c.Host
		}
		out = append(out, SubdomainRoute{Profile: c.Profile, Host: host, Depth: c.Depth, VarName: varName, Rule: WildcardRule(host, c.Depth)})
	}
	return out, nil
}

// SubdomainEnv is the env handed to compose so a label can interpolate `${PSTACK_WILD_<P>}`.
func SubdomainEnv(routes []SubdomainRoute) map[string]string {
	out := map[string]string{}
	for _, r := range routes {
		out[r.VarName] = r.Rule
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
