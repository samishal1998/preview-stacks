package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// TestMayMoveConfigIsRootOnly is the security test for this feature. The whole of /api/config's
// safety is that an admin BROWSER SESSION cannot reach it — an XSS or a stolen cookie would
// otherwise exfiltrate every credential on the host — so the admin row is the one that matters.
//
// negative control: widen mayMoveConfig to routes.go's admin form,
//
//	who.Kind == auth.KindRoot || (who.Kind == auth.KindUser && who.User.Role == "admin")
//
// and the "admin session" case fails. (Run, observed failing, restored.)
func TestMayMoveConfigIsRootOnly(t *testing.T) {
	admin := &auth.UserRow{Username: "alice", Role: "admin"}
	cases := []struct {
		name string
		who  *auth.Principal
		want bool
	}{
		{"root token", &auth.Principal{Kind: auth.KindRoot}, true},
		{"admin session", &auth.Principal{Kind: auth.KindUser, User: admin}, false},
		{"plain user", &auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{Username: "bob", Role: "user"}}, false},
		{"share link", &auth.Principal{Kind: auth.KindShare, Deployment: "7"}, false},
		{"nobody", nil, false},
	}
	for _, c := range cases {
		if got := mayMoveConfig(c.who); got != c.want {
			t.Errorf("mayMoveConfig(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestConfigRoutesRefusesBeforeTheMethod checks the order inside configRoutes: the gate runs before
// the verb, so a session principal gets 403 rather than a 405 that tells it which verbs exist. It
// also proves the refusal reaches the wire without a Server's dependencies being touched — nothing
// below the gate may run for a non-root caller.
//
// negative control: move the mayMoveConfig check below the method switch — a GET from an admin
// session then reaches exportConfig and panics on the nil Server fields instead of answering 403.
func TestConfigRoutesRefusesBeforeTheMethod(t *testing.T) {
	who := &auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{Username: "alice", Role: "admin"}}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		w := httptest.NewRecorder()
		s := &Server{}
		if err := s.configRoutes(w, httptest.NewRequest(method, "/api/config", nil), who); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if w.Code != 403 {
			t.Errorf("%s: status %d, want 403", method, w.Code)
		}
	}
}

// TestImportEmitsOnFailureToo is the visibility guarantee this route's header claims. Apply has no
// outer transaction, so a step that fails halfway has ALREADY written accounts (and can already
// have written registry credentials) — the case where a silent import matters most. A closed store
// forces exactly that failure.
//
// It also pins what the payload may contain: identities and counts, never a credential.
// `Trusts()` — which holds notifier URLs, and for a chat notifier the URL is the secret — must
// never reach an event body, because an event body is POSTed to every subscribed notifier.
//
// negative control: move the `emit(nil, true)` call below `return` on the error path (or delete it)
// — "an import that failed halfway emitted nothing" fires.
func TestImportEmitsOnFailureToo(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"pstackVersion":"test","users":[{"id":1,"username":"alice","role":"admin","email":null,"createdAt":1,"passwordHash":"$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2g"}],` +
		`"registries":[{"registry":"evil.example","username":"bot","password":"hunter2"}],` +
		`"notifiers":[{"type":"slack","name":"ops","config":{"webhookUrl":"https://hooks.slack.com/services/T/B/xyz"},"events":["job.leaked"],"enabled":true,"secret":"","createdAt":1}]}`

	_ = st.Close() // every query now fails: Apply gets as far as applyUsers and returns an error.

	var got []events.Event
	s := &Server{bus: events.New(), store: st, auth: auth.New(st)}
	s.bus.On(func(e events.Event) { got = append(got, e) })
	w := httptest.NewRecorder()
	s.importConfig(w, httptest.NewRequest("POST", "/api/config", strings.NewReader(doc)), &auth.Principal{Kind: auth.KindRoot})
	if w.Code == 200 {
		t.Fatalf("a closed store must not import successfully (status %d)", w.Code)
	}
	if len(got) != 1 || got[0].Event != "config.imported" {
		t.Fatalf("an import that failed halfway emitted nothing: %v", got)
	}
	body := string(got[0].Data)
	for _, want := range []string{`"failed":true`, `"created":null`, `"skipped":null`, `"evil.example"`, `"name":"ops"`} {
		if !strings.Contains(body, want) {
			t.Errorf("payload %s missing %s", body, want)
		}
	}
	// The credential half of the document, and Trusts()'s output, must not be in an event body.
	for _, never := range []string{"hunter2", "hooks.slack.com", "argon2", "pull images from"} {
		if strings.Contains(body, never) {
			t.Errorf("payload %s leaks %q to every notifier", body, never)
		}
	}
}

// TestConfigEventsAreRegistered: an event name that is not in events.Names cannot be subscribed to
// individually — a registration naming it is refused at write time — so the one event that says
// "every credential on this host was just dumped" would be reachable only through the wildcard.
//
// negative control: delete "config.exported" from events.Names — this fails.
func TestConfigEventsAreRegistered(t *testing.T) {
	for _, name := range []string{"config.exported", "config.imported"} {
		if !events.IsEventName(name) {
			t.Errorf("%s is not in events.Names, so nobody can subscribe to it", name)
		}
	}
}
