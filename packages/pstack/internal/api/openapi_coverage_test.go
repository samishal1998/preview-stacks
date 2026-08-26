// The OpenAPI document is only worth having if it cannot fall behind the routes.
//
// `pstack api …` is generated from `packages/pstack/api/openapi.yaml`, so a route added without a
// path there is a route with no command — silently, and forever, because nothing else would notice.
// This test reads the ROUTE TABLE OUT OF THE SOURCE (the `…Re = regexp.MustCompile` block and the
// literal `path == "/api/…"` comparisons in routes.go, routes_auth.go and server.go) and requires
// every one of them to be in the document or in `notInTheSpec` below with a reason.
//
// Reading the source rather than the permissions table is deliberate: that table is DEFAULT-DENY, so
// a route nobody listed is root's and simply absent from it, and the whole pre-gate set — health,
// probe, login, logout, bootstrap, the SSO round trip — never appears there at all. A coverage test
// built on it would pass while missing seven routes.
package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// notInTheSpec is every route deliberately absent from the document, and why. A route may only be
// here with a reason a reader can check.
var notInTheSpec = map[string]string{
	// Streaming. A generated command executes one request and prints the response, so each of these
	// would buffer an unbounded stream and print it when the server finally closes — an hour later
	// for the log follower, never for the terminal.
	"/api/deployments/{id}/logs/stream": "SSE: an endless response a one-shot command cannot render",
	"/api/deployments/{id}/terminal":    "a WebSocket upgrade; there is nothing for an HTTP command to do with it",
	"/api/jobs/{jobId}/stream":          "SSE: same as the log stream",

	// Browser flows. Both are 302s a browser follows, carrying a cookie or a provider redirect; a
	// command that printed the Location header would be a worse `curl`.
	"/api/auth/sso/start":    "a 302 into a provider's authorize URL, for a browser",
	"/api/auth/sso/callback": "the provider's redirect back, for a browser",

	// Session cookies, which a CLI has no use for: it authenticates with a bearer token, and
	// `pstack api tokens create` is how it gets one.
	"/api/auth/login":     "mints a session COOKIE; the CLI uses a bearer token",
	"/api/auth/logout":    "revokes a session cookie",
	"/api/auth/bootstrap": "creates the first account before any credential exists; run it with curl, once",
}

// specPaths is every `paths:` key in the document. A line-oriented read rather than a YAML parse:
// this package must not grow a YAML dependency for a test, and the document indents its path keys
// at exactly two spaces.
func specPaths(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	out := map[string]bool{}
	inPaths := false
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "paths:" {
			inPaths = true
			continue
		}
		if inPaths && line != "" && !strings.HasPrefix(line, " ") {
			break // a new top-level key: components.
		}
		if m := regexp.MustCompile(`^  (/[^:]*):\s*$`).FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("found no paths in the spec — the reader is broken, not the spec")
	}
	return out
}

// templated turns a route regexp's source into an OpenAPI path, so `^/api/specs/([^/]+)$` and
// `/api/specs/{name}` compare equal. The parameter NAME is not recoverable from a regexp, so the
// comparison is on shape: every capture group becomes `{}`.
var (
	captureRe = regexp.MustCompile(`\((\?:)?[^)]*\)\??`)
	braceRe   = regexp.MustCompile(`\{[^}]*\}`)
)

func shapeOf(path string) string {
	s := braceRe.ReplaceAllString(path, "{}")
	return strings.TrimSuffix(s, "/")
}

// routeShapes reads the routes out of the package's own source.
func routeShapes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	// The DISPATCH surface only: `routes*.go` and `server.go`. Not every `/api/…` regexp in the
	// package is a route — `principal.go`'s `shareRouteRe` is a SCOPE predicate matching
	// `/api/deployments/<id>/anything` to decide what a share link may read, and treating it as a
	// route would demand a spec path for a URL nothing serves.
	var files []string
	for _, g := range []string{"routes*.go", "server.go"} {
		found, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	// `x = regexp.MustCompile(`^/api/…$`)` — the route regexps.
	reDecl := regexp.MustCompile("regexp\\.MustCompile\\(`\\^(/api/[^`]*)\\$`\\)")
	// `path == "/api/…"` — the literal comparisons.
	litDecl := regexp.MustCompile(`path == "(/api/[^"]*)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reDecl.FindAllStringSubmatch(string(src), -1) {
			expandRoute(out, m[1], f)
		}
		for _, m := range litDecl.FindAllStringSubmatch(string(src), -1) {
			out[shapeOf(m[1])] = f
		}
	}
	return out
}

// expandRoute turns one route regexp into every concrete path it matches.
//
// The optional groups are the reason this is not a string replace: `deploymentRe` is
// `([^/]+)(?:/(up|down|verify|sleep|wake))?`, which is SIX routes — the bare deployment and five
// actions — and `notifierRe` and `jobRe` have the same shape. A test that treated each as one route
// would let `POST …/wake` vanish from the spec unnoticed.
func expandRoute(out map[string]string, src, file string) {
	// An alternation inside an optional group: expand it into the bare form plus one per branch.
	if m := regexp.MustCompile(`^(.*)\(\?:/\(([a-z|]+)\)\)\?$`).FindStringSubmatch(src); m != nil {
		out[shapeOf(captureRe.ReplaceAllString(m[1], "{}"))] = file
		for _, alt := range strings.Split(m[2], "|") {
			out[shapeOf(captureRe.ReplaceAllString(m[1], "{}"))+"/"+alt] = file
		}
		return
	}
	// A bare alternation as a path segment: `/(start|stop|restart)$` is three routes, but they are
	// one OpenAPI path with an enum parameter, so it collapses to a single `{}`.
	out[shapeOf(captureRe.ReplaceAllString(src, "{}"))] = file
}

// negative control: delete `/api/deployments/{id}/wake` from openapi.yaml — this fails, naming it.
// The route stays reachable over HTTP, which is exactly why nothing else would catch it.
func TestEveryRouteIsInTheSpecOrExcusedByName(t *testing.T) {
	spec := map[string]bool{}
	for p := range specPaths(t) {
		spec[shapeOf(p)] = true
	}
	excused := map[string]bool{}
	for p := range notInTheSpec {
		excused[shapeOf(p)] = true
	}
	var missing []string
	for shape, file := range routeShapes(t) {
		if spec[shape] || excused[shape] {
			continue
		}
		missing = append(missing, shape+"  ("+file+")")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d route(s) reachable over HTTP with no OpenAPI path, so no `pstack api` command:\n  %s\n\nAdd them to packages/pstack/api/openapi.yaml, or to notInTheSpec with a reason.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// The other direction: a path in the document that no route serves would generate a command that
// 404s, which is worse than a missing one — it looks supported.
//
// negative control: add `/api/deployments/{id}/rollback` to openapi.yaml — this fails, naming it.
func TestEverySpecPathIsARealRoute(t *testing.T) {
	routes := routeShapes(t)
	var phantom []string
	for p := range specPaths(t) {
		if _, ok := routes[shapeOf(p)]; !ok {
			phantom = append(phantom, p)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Fatalf("%d spec path(s) that no route serves — each generates a command that can only 404:\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}

// An excuse for a route that no longer exists is a stale comment pretending to be a decision.
//
// negative control: rename `/api/auth/login` in routes_auth.go — this fails, naming the excuse.
func TestEveryExclusionStillNamesARealRoute(t *testing.T) {
	routes := routeShapes(t)
	for p, why := range notInTheSpec {
		if _, ok := routes[shapeOf(p)]; !ok {
			t.Errorf("notInTheSpec excuses %q (%s), but no route matches it any more", p, why)
		}
	}
}
