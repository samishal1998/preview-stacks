package spec

// Ported from test/stack.test.ts describe 'wildcard subdomains — routing a whole subtree at one
// profile' (the pure rule/varName tests and the 'spec parsing' block; 'compose invocation' belongs
// to compose).
//
// The rule is a Go regexp Traefik matches every request against, so "roughly right" is not a
// category: too loose routes someone else's hostname into this PR, too tight silently never fires.
// These tests exercise it as a regexp rather than string-comparing the output — and here the regexp
// engine IS the one Traefik uses.

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const wildHost = "backend-pr-123.preview.example.com"

// re extracts the pattern from HostRegexp(`…`) and compiles it the way Traefik would.
func re(t *testing.T, rule string) *regexp.Regexp {
	t.Helper()
	m := regexp.MustCompile("^HostRegexp\\(`(.+)`\\)$").FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("not a HostRegexp rule: %q", rule)
	}
	return regexp.MustCompile(m[1])
}

func TestWildcardSubdomains(t *testing.T) {
	t.Run(`depth "one" matches exactly one extra label`, func(t *testing.T) {
		// negative control: use the `any` prefix for both depths — `a.b.<host>` matches.
		r := re(t, WildcardRule(wildHost, DepthOne))
		for _, h := range []string{"api." + wildHost, "tenant-a." + wildHost} {
			if !r.MatchString(h) {
				t.Fatalf("%q should match", h)
			}
		}
		// Deeper must NOT match: DNS and TLS cannot cover it, so routing it would produce a host that
		// resolves, routes, and then fails the handshake — the worst of the three outcomes to debug.
		if r.MatchString("a.b." + wildHost) {
			t.Fatal("a.b.<host> matched depth one")
		}
		// And the bare host is left to the exact-host router that already owns it.
		if r.MatchString(wildHost) {
			t.Fatal("the bare host matched")
		}
	})

	t.Run(`depth "any" matches any depth`, func(t *testing.T) {
		// negative control: ignore depth — `a.b.<host>` stops matching.
		r := re(t, WildcardRule(wildHost, DepthAny))
		for _, h := range []string{"api." + wildHost, "a.b." + wildHost, "a.b.c.d." + wildHost} {
			if !r.MatchString(h) {
				t.Fatalf("%q should match", h)
			}
		}
		if r.MatchString(wildHost) {
			t.Fatal("the bare host matched")
		}
	})

	t.Run("the host is regexp-escaped, so a dot cannot act as a wildcard", func(t *testing.T) {
		// negative control: make EscapeHostRegexp the identity.
		r := re(t, WildcardRule(wildHost, DepthOne))
		// The bug an unescaped `.` produces: someone registers `backend-pr-123Xpreview.example.com`
		// and Traefik hands them this PR's backend.
		if r.MatchString("api.backend-pr-123Xpreview.example.com") {
			t.Fatal("an unescaped dot matched X")
		}
		if !strings.Contains(WildcardRule(wildHost, DepthOne), `backend-pr-123\.preview\.example\.com`) {
			t.Fatalf("rule = %q", WildcardRule(wildHost, DepthOne))
		}
	})

	t.Run("it is anchored at both ends", func(t *testing.T) {
		// negative control: drop the `^` or the `$` from the rule.
		r := re(t, WildcardRule(wildHost, DepthOne))
		// Unanchored, this would match — a suffix attack in the first case, a prefix one in the second.
		if r.MatchString("api." + wildHost + ".evil.test") {
			t.Fatal("suffix matched")
		}
		if !r.MatchString("prefix-api." + wildHost) { // a legitimate single label
			t.Fatal("prefix-api should match")
		}
		if r.MatchString("api/" + wildHost) {
			t.Fatal("api/ matched")
		}
	})

	t.Run("the rule text is byte-exact", func(t *testing.T) {
		// negative control: change the label class or the quoting — compose would hand Traefik a different rule.
		if got := WildcardRule(wildHost, DepthOne); got != "HostRegexp(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\\.backend-pr-123\\.preview\\.example\\.com$`)" {
			t.Fatalf("one = %q", got)
		}
		if got := WildcardRule(wildHost, DepthAny); got != "HostRegexp(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+backend-pr-123\\.preview\\.example\\.com$`)" {
			t.Fatalf("any = %q", got)
		}
	})

	t.Run("a profile name becomes a usable env var name", func(t *testing.T) {
		// negative control: stop replacing `-`.
		if got := SubdomainVarName("backend"); got != "PSTACK_WILD_BACKEND" {
			t.Fatalf("got %q", got)
		}
		// A dash cannot appear in an environment variable name.
		if got := SubdomainVarName("admin-api"); got != "PSTACK_WILD_ADMIN_API" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("spec parsing", func(t *testing.T) {
		withSubs := func(subs, extra string) string {
			return strings.NewReplacer("@SUBS@", subs, "@EXTRA@", extra).Replace(fixture(t, "with-subs.yml"))
		}

		t.Run("the short list form derives the host from profile and stack", func(t *testing.T) {
			// negative control: derive the host as `<stack>-<profile>` — the whole route differs.
			s := parse(t, withSubs("[backend]", ""), nil)
			want := []SubdomainRoute{{
				Profile: "backend",
				Host:    "backend-pr-7.preview.example.com",
				Depth:   DepthOne,
				VarName: "PSTACK_WILD_BACKEND",
				Rule:    WildcardRule("backend-pr-7.preview.example.com", DepthOne),
			}}
			if !reflect.DeepEqual(s.Compose.Subdomains, want) {
				t.Fatalf("subdomains = %#v", s.Compose.Subdomains)
			}
		})

		t.Run("the mapping form takes a depth, and an explicit host may interpolate", func(t *testing.T) {
			// negative control: skip interpolation of `host:` — `app-${STACK}.${DOMAIN}` comes back literally.
			s := parse(t, fixture(t, "subs-mapping.yml"), nil)
			backend, frontend := s.Compose.Subdomains[0], s.Compose.Subdomains[1]
			if backend.Depth != DepthAny {
				t.Fatalf("backend depth = %q", backend.Depth)
			}
			// ${STACK} resolves inside an explicit host — it is interpolated with everything else.
			if frontend.Host != "app-pr-7.preview.example.com" {
				t.Fatalf("frontend host = %q", frontend.Host)
			}
			if frontend.Depth != DepthOne {
				t.Fatalf("frontend depth = %q", frontend.Depth)
			}
		})

		t.Run("a subdomain for a profile that is never started is refused", func(t *testing.T) {
			// negative control: drop the `contains(profiles, c.Profile)` check.
			// Dead config, and the likeliest cause is a typo in one of the two lists.
			err := parseErr(t, withSubs("[nope]", ""), nil)
			match(t, "not in compose.profiles", err.Error())
			if want := `compose.subdomains names profile "nope", which is not in compose.profiles (backend, frontend). Its router would never match a running service.`; err.Error() != want {
				t.Fatalf("got %q", err.Error())
			}
		})

		t.Run("no DOMAIN is a hard error, not a rule that matches nothing", func(t *testing.T) {
			// negative control: anchor on "" when the domain is missing.
			match(t, "needs a domain", parseErr(t, "version: 1\nstack: s\ncompose:\n  file: dc.yml\n  profiles: [backend]\n  subdomains: [backend]\naxes: []", map[string]string{}).Error())
		})

		t.Run("two profiles colliding on one env var name is refused", func(t *testing.T) {
			// negative control: drop the `seen[varName]` check — the second rule silently overwrites the first.
			// `admin-api` and `admin_api` both become PSTACK_WILD_ADMIN_API, so one rule would silently
			// overwrite the other and that profile would route nothing.
			match(t, "both map to PSTACK_WILD_ADMIN_API", parseErr(t, fixture(t, "subs-collide.yml"), nil).Error())
		})

		t.Run("an unknown depth names the two valid ones", func(t *testing.T) {
			// negative control: accept any depth string.
			err := parseErr(t, withSubs("{ backend: deep }", ""), nil)
			match(t, `(?s)must be "one".*or "any"`, err.Error())
			// JSON.stringify of the offending value, so a null reads `null` and a string is quoted.
			if !strings.HasSuffix(err.Error(), `Got "deep".`) {
				t.Fatalf("got %q", err.Error())
			}
			if !strings.HasSuffix(parseErr(t, withSubs("{ backend: { depth: ~ } }", ""), nil).Error(), "Got null.") {
				t.Fatal("a present-null depth should be refused as `null`")
			}
		})

		t.Run("subdomains are absent from the spec unless asked for", func(t *testing.T) {
			// negative control: default `subdomains` to every profile.
			s := parse(t, "version: 1\nstack: s\ncompose:\n  file: dc.yml\n  profiles: [backend]\naxes: []", nil)
			if len(s.Compose.Subdomains) != 0 || s.Compose.Subdomains == nil {
				t.Fatalf("subdomains = %#v", s.Compose.Subdomains)
			}
		})

		t.Run("an unknown option in the mapping form is refused", func(t *testing.T) {
			// negative control: ignore unknown keys.
			if got := parseErr(t, withSubs("{ backend: { depth: one, port: 80 } }", ""), nil).Error(); got != "compose.subdomains.backend.port is not a known option — expected `depth` or `host`" {
				t.Fatalf("got %q", got)
			}
			if got := parseErr(t, withSubs("[1]", ""), nil).Error(); got != "compose.subdomains[0] must be a profile name" {
				t.Fatalf("got %q", got)
			}
			if got := parseErr(t, withSubs("{ backend: 1 }", ""), nil).Error(); got != `compose.subdomains.backend must be a depth ("one"/"any") or a mapping of { depth?, host? }` {
				t.Fatalf("got %q", got)
			}
		})

		t.Run("an empty depth value is the default, an explicit empty host is kept", func(t *testing.T) {
			// negative control: treat `host: ""` as absent — the host is derived instead of "".
			s := parse(t, withSubs("{ backend: ~, frontend: { host: \"\" } }", ""), nil)
			if s.Compose.Subdomains[0].Depth != DepthOne {
				t.Fatalf("depth = %q", s.Compose.Subdomains[0].Depth)
			}
			if s.Compose.Subdomains[1].Host != "" {
				t.Fatalf("host = %q", s.Compose.Subdomains[1].Host)
			}
		})
	})

	t.Run("SubdomainEnv maps every varName to its rule", func(t *testing.T) {
		// negative control: key on Profile instead of VarName.
		routes := []SubdomainRoute{{Profile: "backend", VarName: "PSTACK_WILD_BACKEND", Rule: "r1"}, {Profile: "admin-api", VarName: "PSTACK_WILD_ADMIN_API", Rule: "r2"}}
		if got := SubdomainEnv(routes); !reflect.DeepEqual(got, map[string]string{"PSTACK_WILD_BACKEND": "r1", "PSTACK_WILD_ADMIN_API": "r2"}) {
			t.Fatalf("got %#v", got)
		}
	})
}
