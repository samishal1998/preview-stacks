package api

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
)

// The two sentinels the table has no Role for: the self-service floor and the root-only fallback.
const (
	anyRole  = auth.Role("<any authenticated>")
	rootOnly = auth.Role("<root only>")
)

// required is the table's answer for one request, flattened to a single value the cases can state.
func required(method, path string) auth.Role {
	p, found := requiredRole(method, path)
	switch {
	case !found:
		return rootOnly
	case p.anyRole:
		return anyRole
	case p.min == "":
		return rootOnly
	}
	return p.min
}

// TestPermissionTableIsTheSpecification enumerates EVERY route the gated chain dispatches and
// states the role it requires. This test is the specification: a change to who may reach a route is
// a change to this list, made on purpose, in the same commit.
//
// negative control: set the deploymentRe write row to auth.Viewer (the viewer/developer boundary) →
// the five lifecycle POSTs, the PUT and the DELETE all fail; set the POST /api/users row to
// auth.Viewer → the POST /api/users case fails; drop the `{path: "/api/config"}` row → its two
// cases still pass (default-deny returns root either way), which is the fallback working; give
// /api/settings/default_role the same maintainer tier as /api/settings/max_jobs → the default_role
// case fails, which is the per-key half of the settings contract. All four were run.
func TestPermissionTableIsTheSpecification(t *testing.T) {
	cases := []struct {
		method, path string
		want         auth.Role
	}{
		// self-service — any authenticated principal, whatever its role
		{"GET", "/api/auth/me", anyRole},
		{"GET", "/api/tokens", anyRole},
		{"POST", "/api/tokens", anyRole},
		{"DELETE", "/api/tokens/7", anyRole},

		// people
		{"GET", "/api/users", auth.Viewer},
		{"POST", "/api/users", auth.Admin},
		{"PATCH", "/api/users/3", auth.Admin},
		{"DELETE", "/api/users/3", auth.Admin},
		// Someone else's password. The caller's own is the `self` row — TestOwnPasswordIsSelfService.
		{"PUT", "/api/users/3/password", auth.Admin},

		// the portable host config
		{"GET", "/api/config", rootOnly},
		{"POST", "/api/config", rootOnly},
		{"POST", "/api/config/sealed", auth.Admin},

		// deployments
		{"GET", "/api/deployments", auth.Viewer},
		{"GET", "/api/deployments/pr-1", auth.Viewer},
		{"PUT", "/api/deployments/pr-1", auth.Developer},
		{"DELETE", "/api/deployments/pr-1", auth.Developer},
		{"POST", "/api/deployments/pr-1/up", auth.Developer},
		{"POST", "/api/deployments/pr-1/down", auth.Developer},
		{"POST", "/api/deployments/pr-1/verify", auth.Developer},
		{"POST", "/api/deployments/pr-1/sleep", auth.Developer},
		{"POST", "/api/deployments/pr-1/wake", auth.Developer},
		// Stopping this stack's work is the same tier as starting it: a developer who may run `up`
		// may cancel it. Its OWN pattern, not deploymentRe's verb group — welding it to the
		// lifecycle regex would make it permanently unable to hold a different tier from up/down.
		{"POST", "/api/deployments/pr-1/cancel", auth.Developer},
		{"POST", "/api/deployments/pr-1/share", auth.Developer},

		// single sign-on: the read is host configuration, the write mints accounts
		{"GET", "/api/sso/config", auth.Maintainer},
		{"PUT", "/api/sso/config", auth.Admin},
		{"DELETE", "/api/sso/config", auth.Admin},
		{"PUT", "/api/sso/config/gh", auth.Admin},
		{"DELETE", "/api/sso/config/gh", auth.Admin},

		// the two runtime host settings: one read for both, one write tier PER KEY
		{"GET", "/api/settings", auth.Viewer},
		{"PUT", "/api/settings/max_jobs", auth.Maintainer},
		{"PUT", "/api/settings/default_role", auth.Admin},
		// A key nobody listed is root's, like any unlisted route — and the chain 404s it.
		{"PUT", "/api/settings/shell_command", rootOnly},
		{"POST", "/api/settings/max_jobs", rootOnly},

		// the swarm
		{"GET", "/api/swarm", auth.Viewer},
		{"GET", "/api/swarm/join", auth.Maintainer},

		// host variables & secrets
		{"GET", "/api/host-vars", auth.Viewer},
		{"PUT", "/api/host-vars/DB_URL", auth.Maintainer},
		{"DELETE", "/api/host-vars/DB_URL", auth.Maintainer},

		// named specs
		{"GET", "/api/specs", auth.Viewer},
		{"GET", "/api/specs/web", auth.Viewer},
		{"PUT", "/api/specs/web", auth.Developer},
		{"DELETE", "/api/specs/web", auth.Developer},

		// the control stack
		{"GET", "/api/control", auth.Viewer},

		// logs, source, containers, the shell, runtime, readiness
		{"GET", "/api/deployments/pr-1/logs", auth.Viewer},
		{"GET", "/api/deployments/pr-1/logs/stream", auth.Viewer},
		{"GET", "/api/deployments/pr-1/source", auth.Viewer},
		{"POST", "/api/deployments/pr-1/containers/app/start", auth.Developer},
		{"POST", "/api/deployments/pr-1/containers/app/stop", auth.Developer},
		{"POST", "/api/deployments/pr-1/containers/app/restart", auth.Developer},
		{"GET", "/api/deployments/pr-1/terminal", auth.Developer},
		{"GET", "/api/terminal-sessions", auth.Viewer},
		{"GET", "/api/deployments/pr-1/runtime", auth.Viewer},
		{"GET", "/api/deployments/pr-1/readiness", auth.Viewer},

		// routing
		{"GET", "/api/routing/live", auth.Viewer},
		{"GET", "/api/routing", auth.Viewer},
		{"GET", "/api/routing/extra.yml", auth.Viewer},
		{"PUT", "/api/routing/extra.yml", auth.Maintainer},
		{"DELETE", "/api/routing/extra.yml", auth.Maintainer},

		// notifiers
		{"GET", "/api/notifiers/meta", auth.Viewer},
		{"GET", "/api/notifiers", auth.Viewer},
		{"POST", "/api/notifiers", auth.Maintainer},
		{"GET", "/api/notifiers/1", auth.Viewer},
		{"GET", "/api/notifiers/1/deliveries", auth.Viewer},
		{"POST", "/api/notifiers/1/test", auth.Maintainer},
		{"PATCH", "/api/notifiers/1", auth.Maintainer},
		{"DELETE", "/api/notifiers/1", auth.Maintainer},
		{"POST", "/api/notifiers/1/deliveries/2/redeliver", auth.Maintainer},

		// private registry credentials
		{"GET", "/api/registries", auth.Viewer},
		{"PUT", "/api/registries/ghcr.io", auth.Maintainer},
		{"DELETE", "/api/registries/ghcr.io", auth.Maintainer},

		// jobs
		{"GET", "/api/jobs", auth.Viewer},
		{"GET", "/api/jobs/j1", auth.Viewer},
		{"GET", "/api/jobs/j1/stream", auth.Viewer},
		{"POST", "/api/jobs/j1/cancel", auth.Developer},

		// nobody listed these: default-deny
		{"GET", "/api/nope", rootOnly},
		{"POST", "/api/deployments", rootOnly},
		{"PATCH", "/api/deployments/pr-1", rootOnly},
	}
	for _, c := range cases {
		if got := required(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// TestEveryRowNamesItsMethods: an empty method set would match nothing and silently leave its route
// on the default-deny fallback, which reads like a gate but is not the one the row claims.
//
// negative control: drop `methods: mGet` from the /api/control row → fails.
func TestEveryRowNamesItsMethods(t *testing.T) {
	for i, p := range permissions {
		if len(p.methods) == 0 {
			t.Errorf("row %d (%s%v) names no method", i, p.path, p.re)
		}
		if (p.path == "") == (p.re == nil) {
			t.Errorf("row %d must set exactly one of path/re", i)
		}
		if p.self && p.re == nil {
			t.Errorf("row %d is self but has no pattern to read an id from", i)
		}
	}
}

// ── drift protection: the chain may not dispatch a route the table has never heard of ───────────

// routeSources are the files the GATED chain dispatches in. server.go's handle() and preGate() are
// deliberately excluded: they answer before the gate exists.
var routeSources = []string{"routes.go", "routes_auth.go"}

// preGatePaths are the literals in those files that are answered before the gate — matched by the
// scan, excluded here ON PURPOSE, so adding a pre-gate route is a deliberate edit to this list
// rather than a silent hole.
var preGatePaths = map[string]bool{
	"/api/auth/login":        true,
	"/api/auth/logout":       true,
	"/api/auth/bootstrap":    true,
	"/api/auth/sso/start":    true,
	"/api/auth/sso/callback": true,
}

// matchers maps the identifier a dispatch site uses to the compiled pattern. A matcher the scan
// finds that is NOT here fails the test — that is the drift protection: a new route matcher cannot
// be added to the chain without being named to the table.
var matchers = map[string]*regexp.Regexp{
	"hostVarRe":      hostVarRe,
	"specRe":         specRe,
	"routingFileRe":  routingFileRe,
	"redeliverRe":    redeliverRe,
	"notifierRe":     notifierRe,
	"registryRe":     registryRe,
	"cancelRe":       cancelRe,
	"jobRe":          jobRe,
	"deploymentRe":   deploymentRe,
	"deployCancelRe": deployCancelRe,
	"shareRe":        shareRe,
	"logsRe":         logsRe,
	"logStreamRe":    logStreamRe,
	"sourceRe":       sourceRe,
	"containerRe":    containerRe,
	"terminalRe":     terminalRe,
	"runtimeRe":      runtimeRe,
	"readinessRe":    readinessRe,
	"userPasswordRe": userPasswordRe,
	"userRe":         userRe,
	"tokenRe":        tokenRe,
	"ssoProviderRe":  ssoProviderRe,
}

var (
	dispatchLiteral = regexp.MustCompile(`path == "(/api/[^"]*)"`)
	dispatchMatcher = regexp.MustCompile(`(\w+Re)\.(?:FindStringSubmatch|MatchString)\(path\)`)
)

// TestEveryDispatchedRouteHasARow reads the source of the chain and fails if it dispatches on a
// path or a pattern with no row in the table. Without this, "forgot to add an entry" is a 403 an
// operator reports weeks later — or, if the row is added for one method and the handler grows
// another, nothing at all.
//
// negative control: delete the `{re: terminalRe, …}` row → "terminalRe" is reported; add
// `if path == "/api/zzz" {}` to routes.go → "/api/zzz" is reported; rename the matchers key
// "terminalRe" → "terminalReX" → the never-heard-of branch fires; point routeSources at a file that
// does not exist, or break either scan pattern → the floor below fires. All four were run.
func TestEveryDispatchedRouteHasARow(t *testing.T) {
	tabledPaths := map[string]bool{}
	tabledRes := map[*regexp.Regexp]bool{}
	for _, p := range permissions {
		if p.path != "" {
			tabledPaths[p.path] = true
		}
		if p.re != nil {
			tabledRes[p.re] = true
		}
	}
	seen := 0
	for _, file := range routeSources {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		seen += len(dispatchLiteral.FindAllString(text, -1)) + len(dispatchMatcher.FindAllString(text, -1))
		for _, m := range dispatchLiteral.FindAllStringSubmatch(text, -1) {
			if preGatePaths[m[1]] || tabledPaths[m[1]] {
				continue
			}
			t.Errorf("%s dispatches on %q with no row in permissions.go", file, m[1])
		}
		for _, m := range dispatchMatcher.FindAllStringSubmatch(text, -1) {
			re, known := matchers[m[1]]
			if !known {
				t.Errorf("%s dispatches on %s, which permissions_test.go's matchers map has never heard of — add it there and give it a row in permissions.go", file, m[1])
				continue
			}
			if !tabledRes[re] {
				t.Errorf("%s dispatches on %s with no row in permissions.go", file, m[1])
			}
		}
	}
	// THE SCAN ITSELF MUST NOT SILENTLY FIND NOTHING. A renamed helper or a changed spelling in the
	// chain would leave both patterns matching zero sites, and a test that checks nothing passes —
	// which is exactly the class of bug this file exists to prevent.
	if seen < 30 || len(tabledPaths) < 10 || len(tabledRes) < 10 {
		t.Fatalf("the scan found %d dispatch sites and the table has %d paths / %d patterns — one of them stopped working", seen, len(tabledPaths), len(tabledRes))
	}
}

// ── the gate ────────────────────────────────────────────────────────────────────────────────────

func principalOf(role string) *auth.Principal {
	return &auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{ID: 3, Username: "u" + role, Role: role}}
}

func allowed(who *auth.Principal, method, path string) bool {
	return permit(httptest.NewRecorder(), who, method, path)
}

// TestGateBoundaries walks the rungs of the ladder: each role reaches its own tier and everything
// below it, and nothing above.
//
// negative control: change `who.Allows(p.min)` in permit to `who.Allows(auth.Viewer)` → every
// "must be refused" line for a viewer fails. Run.
func TestGateBoundaries(t *testing.T) {
	viewer, dev := principalOf("viewer"), principalOf("developer")
	maint, admin := principalOf("maintainer"), principalOf("admin")
	root := &auth.Principal{Kind: auth.KindRoot}

	mustReach := []struct {
		who          *auth.Principal
		method, path string
	}{
		{viewer, "GET", "/api/deployments/pr-1"},
		{viewer, "GET", "/api/users"},
		{viewer, "GET", "/api/jobs"},
		{dev, "GET", "/api/deployments/pr-1"},
		{dev, "POST", "/api/deployments/pr-1/up"},
		{dev, "GET", "/api/deployments/pr-1/terminal"},
		{dev, "PUT", "/api/specs/web"},
		{maint, "POST", "/api/deployments/pr-1/up"},
		{maint, "PUT", "/api/host-vars/DB_URL"},
		{maint, "GET", "/api/swarm/join"},
		{maint, "GET", "/api/sso/config"},
		// The cap is operational, so it stops at maintainer…
		{viewer, "GET", "/api/settings"},
		{maint, "PUT", "/api/settings/max_jobs"},
		{admin, "PUT", "/api/settings/default_role"},
		{admin, "PUT", "/api/host-vars/DB_URL"},
		{admin, "POST", "/api/users"},
		{admin, "PUT", "/api/sso/config"},
		{admin, "POST", "/api/config/sealed"},
		{root, "GET", "/api/config"},
		{root, "GET", "/api/nope"},
	}
	for _, c := range mustReach {
		if !allowed(c.who, c.method, c.path) {
			t.Errorf("%s must reach %s %s", c.who.User.Role, c.method, c.path)
		}
	}

	mustNot := []struct {
		who          *auth.Principal
		method, path string
	}{
		// the viewer/developer boundary
		{viewer, "POST", "/api/deployments/pr-1/up"},
		{viewer, "DELETE", "/api/deployments/pr-1"},
		{viewer, "GET", "/api/deployments/pr-1/terminal"},
		{viewer, "PUT", "/api/specs/web"},
		{viewer, "POST", "/api/jobs/j1/cancel"},
		// the developer/maintainer boundary
		{dev, "PUT", "/api/host-vars/DB_URL"},
		{dev, "PUT", "/api/routing/extra.yml"},
		{dev, "POST", "/api/notifiers"},
		{dev, "GET", "/api/swarm/join"},
		{dev, "GET", "/api/sso/config"},
		// the maintainer/admin boundary — the promotion paths
		{maint, "POST", "/api/users"},
		{maint, "PATCH", "/api/users/9"},
		{maint, "DELETE", "/api/users/9"},
		{maint, "PUT", "/api/users/9/password"},
		{maint, "PUT", "/api/sso/config"},
		{maint, "DELETE", "/api/sso/config/gh"},
		{maint, "POST", "/api/config/sealed"},
		// …and the role a new account is created with is user management, so it does not.
		{maint, "PUT", "/api/settings/default_role"},
		{dev, "PUT", "/api/settings/max_jobs"},
		// Not even an admin invents a third setting: an unlisted key is root's.
		{admin, "PUT", "/api/settings/shell_command"},
		// and nothing but root moves the whole host's credentials
		{admin, "GET", "/api/config"},
		{admin, "POST", "/api/config"},
		{admin, "GET", "/api/nope"},
	}
	for _, c := range mustNot {
		if allowed(c.who, c.method, c.path) {
			t.Errorf("%s must NOT reach %s %s", c.who.User.Role, c.method, c.path)
		}
	}
}

// TestUnknownRoleReachesNothingAboveTheFloor: a users row hand-edited to "superuser" must LOSE
// access, never gain it. It keeps the self-service floor (its own tokens, its own identity) —
// that floor is by KIND, not by rank, and grants nothing another account does not already have.
//
// negative control: make auth.Role.Rank return 4 instead of 0 for an unrecognised role → every
// refusal below fails. Run.
func TestUnknownRoleReachesNothingAboveTheFloor(t *testing.T) {
	weird := principalOf("superuser")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/deployments"},
		{"GET", "/api/users"},
		{"GET", "/api/jobs"},
		{"POST", "/api/deployments/pr-1/up"},
		{"PUT", "/api/host-vars/DB_URL"},
		{"POST", "/api/users"},
		{"GET", "/api/config"},
	} {
		if allowed(weird, c.method, c.path) {
			t.Errorf("an unknown role reached %s %s", c.method, c.path)
		}
	}
	if !allowed(weird, "GET", "/api/auth/me") || !allowed(weird, "GET", "/api/tokens") {
		t.Error("the self-service floor is by kind, not by rank")
	}
}

// TestDefaultDenyIsRootOnly: a route nobody listed belongs to root. This is the property that makes
// forgetting an entry safe, so it is asserted on paths that do not exist as much as on ones that do.
//
// negative control: in permit, return true in the `!found` branch unconditionally → fails.
func TestDefaultDenyIsRootOnly(t *testing.T) {
	unlisted := [][2]string{
		{"GET", "/api/a-route-added-tomorrow"},
		{"POST", "/api/deployments/pr-1/nuke"},
		{"DELETE", "/api/swarm"},
		{"PATCH", "/api/host-vars/DB_URL"},
	}
	for _, c := range unlisted {
		for _, role := range []string{"viewer", "developer", "maintainer", "admin"} {
			if allowed(principalOf(role), c[0], c[1]) {
				t.Errorf("%s reached unlisted %s %s", role, c[0], c[1])
			}
		}
		if !allowed(&auth.Principal{Kind: auth.KindRoot}, c[0], c[1]) {
			t.Errorf("root must reach unlisted %s %s", c[0], c[1])
		}
	}
}

// TestOwnPasswordIsSelfService: your own password is yours whatever your role; anyone else's is an
// admin operation. Both halves are one row.
//
// negative control: drop `self: true` from the userPasswordRe row → the first case fails; drop the
// id comparison in isSelf (`return m != nil`) → the second fails. Both run.
func TestOwnPasswordIsSelfService(t *testing.T) {
	viewer := principalOf("viewer") // id 3
	if !allowed(viewer, "PUT", "/api/users/3/password") {
		t.Error("a viewer must be able to change their own password")
	}
	if allowed(viewer, "PUT", "/api/users/4/password") {
		t.Error("a viewer must NOT be able to change someone else's password")
	}
	if !allowed(principalOf("admin"), "PUT", "/api/users/4/password") {
		t.Error("an admin sets anyone's password")
	}
	if !allowed(&auth.Principal{Kind: auth.KindRoot}, "PUT", "/api/users/4/password") {
		t.Error("root sets anyone's password")
	}
}

// TestShareIsDecidedBeforeThisTable: a share principal passes this gate untouched because
// shareAllows (principal.go) already answered — GET only, its own deployment, its own views. The
// two halves are asserted together so nobody "fixes" one of them alone.
//
// negative control: make permit fall through for KindShare instead of returning true → the first
// case fails (every share link on every host stops working).
func TestShareIsDecidedBeforeThisTable(t *testing.T) {
	sh := &auth.Principal{Kind: auth.KindShare, Deployment: "pr-1", Views: []share.View{share.Details, share.Logs}}
	if !allowed(sh, "GET", "/api/deployments/pr-1") {
		t.Error("the role table must not second-guess shareAllows")
	}
	// …and shareAllows is what actually holds the line.
	if shareAllows(sh, "GET", "/api/users") {
		t.Error("shareAllows must refuse a route outside the link's deployment")
	}
	if shareAllows(sh, "POST", "/api/deployments/pr-1/up") {
		t.Error("shareAllows must refuse a write")
	}
	// Directly: a share is not a role at any rank.
	for _, min := range []auth.Role{auth.Viewer, auth.Developer, auth.Maintainer, auth.Admin} {
		if sh.Allows(min) {
			t.Errorf("a share must not rank as %s", min)
		}
	}
}

// TestDeniedSaysWhichRole: an operator staring at a 403 has to be able to tell "wrong role" from
// "wrong URL".
//
// negative control: return a bare "forbidden" from denied → fails.
func TestDeniedSaysWhichRole(t *testing.T) {
	w := httptest.NewRecorder()
	permit(w, principalOf("viewer"), "POST", "/api/deployments/pr-1/up")
	body := w.Body.String()
	if w.Code != 403 || !strings.Contains(body, "developer") || !strings.Contains(body, "viewer") {
		t.Errorf("%d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	permit(w, principalOf("admin"), "GET", "/api/config")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "PSTACK_TOKEN") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}
