// THE PERMISSION TABLE: which role may reach which route, in ONE ordered table consulted in ONE
// place, and DEFAULT-DENY.
//
// ── WHY A TABLE AND NOT A CHECK IN EACH HANDLER ─────────────────────────────────────────────────
//
// A check inside each handler is a rule you have to remember to write. Forgetting is silent, and it
// is not hypothetical: `POST /api/users` shipped reachable by ANY authenticated principal and — the
// route passing no role, so the insert fell through to `COALESCE(?, 'admin')` — always created an
// ADMIN. Any account on the host could mint itself a second, fully privileged one. Nothing in the
// code looked wrong, because the missing check was invisible.
//
// So the rule lives here instead, and a route with NO ENTRY requires root. Forgetting to add an
// entry costs an operator a 403 and one commit; forgetting a handler check costs the host. The
// fallback and a deliberate root-only row are the SAME expression — `who.Allows("")`, an unrankable
// minimum nobody but root meets — so there is no third behaviour to get wrong.
//
// ── THE SAME REGEXES routes.go DISPATCHES ON ────────────────────────────────────────────────────
//
// Every row points at the compiled pattern the chain itself matches (`deploymentRe`, `notifierRe`,
// …), never a second copy of it. Two patterns for one route is a drift bug with a fuse in it: the
// day someone widens the dispatch pattern, the gate keeps matching the old one and the new shape of
// the route is ungated. permissions_test.go walks the source of the chain and fails if a matcher or
// a literal path it dispatches on has no row here.
//
// ── THE FOUR ROLES, AND THE TWO PRINCIPALS THAT ARE NOT ROLES ───────────────────────────────────
//
// viewer < developer < maintainer < admin (internal/auth). Above them, `root` — the PSTACK_TOKEN
// bearer — passes everything.
//
// A SHARE PRINCIPAL SKIPS THIS TABLE ENTIRELY, and that is not a hole. `shareAllows` (principal.go)
// has already run, BEFORE any route: GET only, its own deployment only, only the routes its views
// name, everything else refused (invariant 16). Ranking a share here would be a second, weaker copy
// of that gate; denying it here would break every share link on every host. It is decided before it
// arrives, so this table lets it pass and `auth.Principal.Allows` refuses it at every rank in case
// anyone ever reaches for it directly.
//
// ── WHERE A ROW IS A DECISION, NOT A DEDUCTION ──────────────────────────────────────────────────
//
// Three rows below are worth stating out loud, because each looks wrong at a glance:
//
//   - `WS /api/deployments/:id/terminal` is DEVELOPER, not admin. A developer can already run
//     arbitrary compose through `up` — including a service that mounts the docker socket — so
//     refusing them the shell is theatre while the larger door stays open. Close the larger door
//     first and this row can follow it.
//   - `GET /api/swarm/join` is MAINTAINER, where it used to be admin. It is a host-configuration
//     read, and it sits with the other ones. It is still a real credential (the token joins a
//     machine to the cluster), so it stops at maintainer and goes no lower.
//   - `PUT/DELETE /api/sso/config*` is ADMIN even though every other host-configuration write is
//     maintainer, because a provider's `defaultRole` MINTS ACCOUNTS at whatever role it names. A
//     maintainer able to point SSO at an identity provider they control could sign in through it as
//     an admin — that is a promotion path, so it belongs with people, not with configuration.
//     The matching READ stays maintainer: it returns a mask, never the client secret.
package api

import (
	"net/http"
	"regexp"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
)

// perm is one row of the table: what it matches, and the least a caller must be.
//
// The ZERO VALUE IS ROOT-ONLY. `min` is an auth.Role, and the empty Role is unrankable, so a row
// that forgets to name a tier is structurally root's — default-deny is a property of the type here,
// not a convention someone has to honour.
type perm struct {
	// path is an exact path; re is the chain's own compiled pattern. Exactly one is set.
	path string
	re   *regexp.Regexp
	// methods is explicit and never empty: a method nobody listed falls through to root.
	methods []string
	// min is the least role that may reach it.
	min auth.Role
	// anyRole is the self-service floor: any authenticated principal, whatever role it holds. By
	// KIND, not by rank — "whatever its role" includes a role this build does not know.
	anyRole bool
	// self lowers min to the floor when the path's FIRST capture group is the calling account's own
	// id. One row uses it (a caller changing their own password); anyone else's is min.
	self bool
}

// Method sets, spelled once.
var (
	mGet       = []string{http.MethodGet}
	mPut       = []string{http.MethodPut}
	mPost      = []string{http.MethodPost}
	mGetPost   = []string{http.MethodGet, http.MethodPost}
	mPutDelete = []string{http.MethodPut, http.MethodDelete}
	mWrite     = []string{http.MethodPut, http.MethodDelete, http.MethodPost}
	mPatchDel  = []string{http.MethodPatch, http.MethodDelete}
	mNotifier  = []string{http.MethodPost, http.MethodPatch, http.MethodDelete}
)

// permissions is the table, in the chain's own order. First match wins, so a pattern that overlaps
// a narrower one (routingFileRe over /api/routing/live) must sit below it exactly as it does in
// routes.go.
var permissions = []perm{
	// ── self-service: any authenticated principal, whatever its role ────────────────────────────
	// (POST /api/auth/logout is self-service too, but it runs PRE-GATE — see preGate — because a
	// caller whose account was just demoted or deleted must still be able to end their session.)
	{path: "/api/auth/me", methods: mGet, anyRole: true},
	{path: "/api/tokens", methods: mGetPost, anyRole: true},
	{re: tokenRe, methods: []string{http.MethodDelete}, anyRole: true},
	// Someone else's password is an admin operation; your own is yours. Both are this one row.
	{re: userPasswordRe, methods: mPut, min: auth.Admin, self: true},

	// ── people ──────────────────────────────────────────────────────────────────────────────────
	// The roster is READ-ONLY team information — who to hand a deployment to — and reads no
	// secrets, so it sits with the other reads.
	{path: "/api/users", methods: mGet, min: auth.Viewer},
	{path: "/api/users", methods: mPost, min: auth.Admin},
	{re: userRe, methods: mPatchDel, min: auth.Admin},

	// ── the whole host, portable: every credential on it ────────────────────────────────────────
	// No tier: root, and root alone. routes_config.go's header is the argument, and mayMoveConfig
	// enforces it a second time at the handler — the one route in this package deliberately gated
	// twice.
	{path: "/api/config", methods: mGetPost},
	{path: "/api/config/sealed", methods: mPost, min: auth.Admin},

	// ── deployments ─────────────────────────────────────────────────────────────────────────────
	{path: "/api/deployments", methods: mGet, min: auth.Viewer},
	{re: deploymentRe, methods: mGet, min: auth.Viewer},
	{re: deploymentRe, methods: mWrite, min: auth.Developer},
	// Stopping this stack's jobs is the same tier as starting them, and the same tier as cancelling
	// one by id (cancelRe, below): whoever may run a deploy may stop the one they ran.
	{re: deployCancelRe, methods: mPost, min: auth.Developer},
	{re: shareRe, methods: mPost, min: auth.Developer},

	// ── single sign-on ──────────────────────────────────────────────────────────────────────────
	{path: "/api/sso/config", methods: mGet, min: auth.Maintainer},
	{path: "/api/sso/config", methods: mPutDelete, min: auth.Admin},
	{re: ssoProviderRe, methods: mPutDelete, min: auth.Admin},

	// ── the two runtime host settings: PER KEY, because they are different kinds of thing ───────
	// Reading both is a viewer's — the values explain why a job is queued and what a new account
	// gets, and neither is a secret. Writing splits: `max_jobs` is operational and sits with host
	// configuration; `default_role` decides the role of every account created without one, which is
	// user management by another name and belongs with the promotion paths.
	//
	// Exact paths, one per key. A third key would be a third row, and until it has one the table's
	// default-deny answers it — see routes_settings.go.
	{path: "/api/settings", methods: mGet, min: auth.Viewer},
	{path: "/api/settings/max_jobs", methods: mPut, min: auth.Maintainer},
	{path: "/api/settings/default_role", methods: mPut, min: auth.Admin},

	// ── the control stack ───────────────────────────────────────────────────────────────────────
	// Maintainer for both, like the other host-configuration surfaces. The restart's harder rule —
	// even root may not restart `pstack` itself — is a refusal in inspect.RestartControlService,
	// not a row: it is about which container answers the request, not about who asks.
	{path: "/api/control/runtime", methods: mGet, min: auth.Maintainer},
	{path: "/api/control/restart", methods: mPost, min: auth.Maintainer},

	// ── the swarm ───────────────────────────────────────────────────────────────────────────────
	{path: "/api/swarm", methods: mGet, min: auth.Viewer},
	{path: "/api/swarm/join", methods: mGet, min: auth.Maintainer},

	// ── host variables & secrets ────────────────────────────────────────────────────────────────
	// The list never returns a secret's value (invariant 15), so reading it is a viewer's.
	{path: "/api/host-vars", methods: mGet, min: auth.Viewer},
	{re: hostVarRe, methods: mPutDelete, min: auth.Maintainer},

	// ── named specs ─────────────────────────────────────────────────────────────────────────────
	{path: "/api/specs", methods: mGet, min: auth.Viewer},
	{re: specRe, methods: mGet, min: auth.Viewer},
	{re: specRe, methods: mPutDelete, min: auth.Developer},

	// ── the control stack: read only, always (invariant 12) ─────────────────────────────────────
	{path: "/api/control", methods: mGet, min: auth.Viewer},

	// ── logs, source, containers, the shell, runtime, readiness ─────────────────────────────────
	{re: logsRe, methods: mGet, min: auth.Viewer},
	{re: logStreamRe, methods: mGet, min: auth.Viewer},
	{re: sourceRe, methods: mGet, min: auth.Viewer},
	{re: containerRe, methods: mPost, min: auth.Developer},
	{re: terminalRe, methods: mGet, min: auth.Developer},
	{path: "/api/terminal-sessions", methods: mGet, min: auth.Viewer},
	{re: runtimeRe, methods: mGet, min: auth.Viewer},
	{re: readinessRe, methods: mGet, min: auth.Viewer},

	// ── routing ─────────────────────────────────────────────────────────────────────────────────
	// /api/routing/live is above routingFileRe, which would otherwise swallow it.
	{path: "/api/routing/live", methods: mGet, min: auth.Viewer},
	{path: "/api/routing", methods: mGet, min: auth.Viewer},
	{re: routingFileRe, methods: mGet, min: auth.Viewer},
	{re: routingFileRe, methods: mPutDelete, min: auth.Maintainer},

	// ── notifiers ───────────────────────────────────────────────────────────────────────────────
	{path: "/api/notifiers/meta", methods: mGet, min: auth.Viewer},
	{path: "/api/notifiers", methods: mGet, min: auth.Viewer},
	{path: "/api/notifiers", methods: mPost, min: auth.Maintainer},
	{re: redeliverRe, methods: mPost, min: auth.Maintainer},
	{re: notifierRe, methods: mGet, min: auth.Viewer},
	{re: notifierRe, methods: mNotifier, min: auth.Maintainer},

	// ── private registry credentials ────────────────────────────────────────────────────────────
	{path: "/api/registries", methods: mGet, min: auth.Viewer},
	{re: registryRe, methods: mPutDelete, min: auth.Maintainer},

	// ── jobs ────────────────────────────────────────────────────────────────────────────────────
	{path: "/api/jobs", methods: mGet, min: auth.Viewer},
	{re: cancelRe, methods: mPost, min: auth.Developer},
	{re: jobRe, methods: mGet, min: auth.Viewer},
}

func (p perm) matches(method, path string) bool {
	if p.path != "" {
		if p.path != path {
			return false
		}
	} else if p.re == nil || !p.re.MatchString(path) {
		return false
	}
	for _, m := range p.methods {
		if m == method {
			return true
		}
	}
	return false
}

// requiredRole is the lookup: the first row matching (method, path), and whether there WAS one.
// found=false is the default-deny fallback, and the zero perm it returns is already root-only —
// the flag exists so the refusal can say which of the two it was.
func requiredRole(method, path string) (perm, bool) {
	for _, p := range permissions {
		if p.matches(method, path) {
			return p, true
		}
	}
	return perm{}, false
}

// isSelf: does this row's first capture group name the calling account?
func isSelf(who *auth.Principal, p perm, path string) bool {
	if who.Kind != auth.KindUser || who.User == nil || p.re == nil {
		return false
	}
	m := p.re.FindStringSubmatch(path)
	return m != nil && intID(m[1]) == who.User.ID
}

// permit is THE GATE. False means it has answered 403 and the chain must stop.
func permit(w http.ResponseWriter, who *auth.Principal, method, path string) bool {
	// Already decided, before any route — see the header.
	if who.Kind == auth.KindShare {
		return true
	}
	p, found := requiredRole(method, path)
	switch {
	case !found:
		// No entry: root's, and nobody else's.
		if who.Allows("") {
			return true
		}
	case p.self && isSelf(who, p, path):
		return true
	case p.anyRole:
		if who.Kind == auth.KindRoot || who.Kind == auth.KindUser {
			return true
		}
	case who.Allows(p.min):
		return true
	}
	writeError(w, 403, denied(who, p, found))
	return false
}

// denied says which role was needed and which one the caller holds — an operator staring at a 403
// otherwise has no way to tell "wrong role" from "wrong URL".
func denied(who *auth.Principal, p perm, found bool) string {
	if found && p.anyRole {
		// Unreachable through permit (a share returns earlier, and root and every user meet the
		// floor), but a message that lies if it ever is reached would be worse than one that does not.
		return "this route requires an account"
	}
	if !found || p.min == "" {
		return "this route is restricted to the PSTACK_TOKEN bearer"
	}
	have := "signed in without one"
	if who.Kind == auth.KindUser && who.User != nil && who.User.Role != "" {
		have = "a " + who.User.Role
	}
	return "this route requires the " + string(p.min) + " role or higher — you are " + have
}
