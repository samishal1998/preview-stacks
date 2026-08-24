// The two routes that carry a host's whole configuration — `GET /api/config` exports it,
// `POST /api/config` applies one. Everything else in this package is a route over ONE thing; these
// two are a route over EVERY secret the host holds, so the rules below are not the same rules.
//
// ── ROOT TOKEN ONLY. NOT AN ADMIN SESSION. THIS IS DELIBERATE ────────────────────────────────────
//
// `mayMoveConfig` admits `auth.KindRoot` and nothing else. Every other admin-gated route in this
// package admits an admin browser session too — see the join-token check in routes.go, which reads
// `who.Kind != auth.KindRoot && !(who.Kind == auth.KindUser && who.User.Role == "admin")`. Copying
// that shape here would be the single worst change anyone could make to this file:
//
//   - GET returns argon2 password hashes, API-token hashes, every host secret, every notifier
//     signing secret, the SSO client secret and every registry password, in plaintext. Reachable
//     from a session cookie, one XSS in the UI — or one stolen laptop with a live session — is a
//     complete credential exfiltration of the host, silently, from the victim's own browser.
//   - POST writes registry credentials, so the same cookie repoints where this host pulls images
//     from.
//
// The cost is real and was accepted: there is no button in the UI, and there never should be. A
// browser session is a human at a keyboard; `PSTACK_TOKEN` is the machine credential an operator
// deliberately put on a machine. Only the second one moves credentials. If you are here to "fix the
// inconsistency" with the other admin routes: the inconsistency is the security control.
//
// ── THE OTHER HALF OF THE GATE IS INVARIANT 9 ───────────────────────────────────────────────────
//
// `principal()` returns `KindRoot` for EVERY request when `s.opts.Token == ""` — the loopback dev
// mode. On this route that means an unauthenticated GET returns the whole credential dump. What
// stops that being a hole is invariant 9 in `internal/cli`: with no `PSTACK_TOKEN` the listener is
// forced to 127.0.0.1, and an explicit non-loopback `PSTACK_HOST` is a hard exit 3 rather than a
// silent downgrade. This is the route where that interlock is the ONLY thing between an anonymous
// request and every secret on the host. Weaken it and you weaken this.
//
// ── WHAT REACHES A NOTIFIER, AND WHAT NEVER DOES ────────────────────────────────────────────────
//
// Both routes emit, so a credential dump is visible rather than silent — that is the point of the
// events, and they are emitted BEFORE the response is written so a client that disconnects
// mid-body cannot suppress them (`Emit` is synchronous, in-order, and individually recovered).
//
// The payloads carry COUNTS AND IDENTITIES ONLY. `Document.Trusts()` returns credentials — for a
// chat notifier the webhook URL *is* the secret — so it goes in the HTTP response body, which
// travels over TLS to a caller that already holds the plaintext, and NEVER into an event. An event
// body is POSTed to every subscribed notifier; putting a Slack URL in one is invariant 15's named
// failure (`docs/secret-exposure.md`), and `events.Event.Data`'s own comment says nothing secret
// goes in. Registry HOSTNAMES and notifier names/types are in the import event on purpose: they are
// what an operator needs to notice a hostile file, and neither is a credential.
package api

import (
	"io"
	"net/http"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/config"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
)

// mayMoveConfig is the whole access rule for both directions, as one named predicate so it can be
// tested without booting a server and so there is exactly one copy of it. Root — the PSTACK_TOKEN
// bearer — and nothing else. See the file header before widening it.
func mayMoveConfig(who *auth.Principal) bool { return who != nil && who.Kind == auth.KindRoot }

// configNoStore is on both responses: this is the one route in the codebase where a cached response body
// is a complete credential dump.
var configNoStore = [2]string{"cache-control", "no-store"}

// configSources is the document's view of the server. Every field is one the server already holds.
func (s *Server) configSources() config.Sources {
	return config.Sources{
		Store: s.store, Auth: s.auth, HostVars: s.hostVars, Webhooks: s.hooks,
		Registries: s.registries, Routing: s.routing, Specs: s.specs,
	}
}

// configRoutes is /api/config. It answers every case itself — including its errors, so a refused
// document cannot fall through to a handler that has not read this file's header — and so always
// returns nil.
func (s *Server) configRoutes(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	// The gate first, before the method check: a non-root caller learns that it may not have this,
	// not which verbs it may not have it with.
	if !mayMoveConfig(who) {
		writeError(w, 403, "the host configuration carries every credential on this host; it moves for the PSTACK_TOKEN bearer only, never for a browser session")
		return nil
	}
	switch r.Method {
	case http.MethodGet:
		s.exportConfig(w, who)
	case http.MethodPost:
		s.importConfig(w, r, who)
	default:
		writeError(w, 405, "use GET or POST")
	}
	return nil
}

// exportConfig is GET /api/config: the plaintext document. The CLI seals it to disk — sealing here
// would mean sending the passphrase to the server, which is strictly worse.
func (s *Server) exportConfig(w http.ResponseWriter, who *auth.Principal) {
	doc, err := s.configSources().Assemble()
	if err != nil {
		s.failConfig(w, err)
		return
	}
	// Counts, never contents. `sso` is a boolean for the same reason.
	s.bus.Emit("config.exported", jsonx.O(
		"by", terminal.ActorOf(*who),
		"users", len(doc.Users), "tokens", len(doc.Tokens), "vars", len(doc.Vars),
		"notifiers", len(doc.Notifiers), "registries", len(doc.Registry),
		"routing", len(doc.Routing), "specs", len(doc.Specs),
		"sso", doc.SSO != nil, "skipped", len(doc.Skipped),
	))
	writeJSON(w, 200, doc, configNoStore)
}

// importConfig is POST /api/config: the plaintext document in, applied create-or-skip.
//
// `Trusts()` is computed BEFORE anything is written and returned with the result, because a config
// file this host did not write can add a registry credential — that is, choose where this host
// pulls images from. The operator's confirmation happens in the CLI, which unseals the file locally
// and therefore already holds the plaintext; this route's job is to make sure the answer is in the
// response and in the event whether anyone asked for it or not.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request, who *auth.Principal) {
	// maxBody+1, so a document that is merely too large is refused as too large rather than
	// truncated into a "this is not a pstack config document" that is not true.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeError(w, 400, "could not read the request body: "+err.Error())
		return
	}
	if len(body) > maxBody {
		writeError(w, 413, "the config document is larger than this API accepts")
		return
	}
	doc, err := config.Parse(body)
	if err != nil {
		s.failConfig(w, err)
		return
	}
	trusts := doc.Trusts()

	// Identities, not values: which registries this host will now pull from, and which notifiers
	// exist. Trusts() itself holds the URLs and stays out (see the header). Built BEFORE Apply
	// because a failed apply must emit too — Apply has no transaction (it cannot: hostvars.Put
	// opens its own, and nesting is a permanent self-deadlock), so a step that fails halfway has
	// already written accounts and possibly registry credentials. That is the case where a silent
	// import matters MOST, and it is the one an emit placed only on the success path would miss.
	hosts := []string{}
	for _, reg := range doc.Registry {
		hosts = append(hosts, reg.Registry)
	}
	hooks := []jsonx.Object{}
	for _, n := range doc.Notifiers {
		hooks = append(hooks, jsonx.O("name", n.Name, "type", n.Type))
	}
	// created/skipped are NULL on a failure rather than 0: Apply returns no summary then, and
	// "nothing was created" is a different fact from "we cannot say what was" (invariant 11).
	emit := func(sum *config.Summary, failed bool) {
		var created, skipped any
		if sum != nil {
			created, skipped = len(sum.Created), len(sum.Skipped)
		}
		s.bus.Emit("config.imported", jsonx.O(
			"by", terminal.ActorOf(*who),
			"registries", hosts, "notifiers", hooks,
			"created", created, "skipped", skipped, "failed", failed,
		))
	}

	sum, err := s.configSources().Apply(doc)
	if err != nil {
		emit(nil, true)
		s.failConfig(w, err)
		return
	}
	emit(sum, false)
	writeJSON(w, 200, jsonx.O("trusts", trusts, "created", sum.Created, "skipped", sum.Skipped), configNoStore)
}

// failConfig maps a refused document to 400 the way `fail` maps every other domain error. It is
// here rather than in `fail`'s switch only because server.go is not this change's to edit — the
// integrator should add `config.IsError(err)` to that list and delete this.
func (s *Server) failConfig(w http.ResponseWriter, err error) {
	if config.IsError(err) {
		writeError(w, 400, err.Error())
		return
	}
	s.fail(w, err)
}
