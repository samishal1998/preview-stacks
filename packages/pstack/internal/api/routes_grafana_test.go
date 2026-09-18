package api

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
)

// grafanaServer is a real New() server with a domain and a root token. Nothing listens. Not
// ssoTestServer: handle() dereferences routing and sleepIndex, and that one leaves both nil. Never
// Start it: Start runs the docker probe, so tests set s.grafana directly.
func grafanaServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Domain: "preview.example.com", Token: "t0ken", RoutingDir: t.TempDir(), Bus: events.New(), Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// healthBody is GET /api/health through handle(), decoded as the ordered map a client sees.
func healthBody(t *testing.T, s *Server) *omap.Map {
	t.Helper()
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest("GET", "/api/health", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return mustParseObject(t, w.Body.String()).(*omap.Map)
}

// The UI finds Grafana through /api/health. When off the key is ABSENT: Has() is the check, because
// a null is still a present key (Go rule 2). Every host without Grafana answers byte-identically.
func TestHealthNamesGrafanaOnlyWhenOn(t *testing.T) {
	// negative control: `if s.grafanaOn() {` → `if true {` in health — the off, no-token and
	// no-domain subtests fail.
	t.Run("off: no grafana key", func(t *testing.T) {
		// negative control: `if s.grafanaOn() {` → `if true {` in health — Has("grafana") is true.
		s := grafanaServer(t)
		if b := healthBody(t, s); b.Has("grafana") {
			v, _ := b.Get("grafana")
			t.Fatalf("grafana is off, got grafana=%v", v)
		}
	})
	t.Run("on: the configured domain's URL, last", func(t *testing.T) {
		// negative control: delete the `body = append(body, jsonx.KV{K: "grafana", …})` line — got "";
		// and, run separately, prepend it (`body = append(jsonx.Object{{K: "grafana", V: s.grafanaURL()}}, body...)`) — the last key is "version".
		s := grafanaServer(t)
		s.grafana.Store(true)
		b := healthBody(t, s)
		if got := b.GetString("grafana"); got != "https://grafana.preview.example.com" {
			t.Fatalf("grafana = %q, want https://grafana.preview.example.com", got)
		}
		if keys := b.Keys(); keys[len(keys)-1] != "grafana" {
			t.Fatalf("grafana goes after version, got keys %v", keys)
		}
	})
	t.Run("no root token: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Token != "" &&` from grafanaOn — the key is present.
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Token = ""
		if healthBody(t, s).Has("grafana") {
			t.Fatal("no PSTACK_TOKEN means no sign-in, so no grafana key")
		}
	})
	t.Run("no domain: no grafana key", func(t *testing.T) {
		// negative control: drop `s.opts.Domain != "" &&` from grafanaOn — the key is "https://grafana.".
		s := grafanaServer(t)
		s.grafana.Store(true)
		s.opts.Domain = ""
		if b := healthBody(t, s); b.Has("grafana") {
			t.Fatalf("no PSTACK_DOMAIN, got grafana=%q", b.GetString("grafana"))
		}
	})
}

// ── Grafana sign-in: verify, the callback inside it, and start ──────────────────────────────────

// forwardAuth is one call Traefik's forwardAuth makes for grafana.<domain>, sent through handle so
// preGate's routing is exercised: Host is pstack's service name, and the X-Forwarded-* headers are
// the real request's. The defaults are a top-level GET navigation to /d/x with no cookie. Each
// name/value pair in hdr overrides one header; an empty value deletes it.
func forwardAuth(s *Server, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/grafana/verify", nil)
	r.Host = "pstack:7878"
	r.Header.Set("x-forwarded-host", "grafana.preview.example.com")
	r.Header.Set("x-forwarded-uri", "/d/x")
	r.Header.Set("x-forwarded-method", "GET")
	r.Header.Set("sec-fetch-mode", "navigate")
	r.Header.Set("sec-fetch-dest", "document")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			r.Header.Del(hdr[i])
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	s.handle(w, r)
	return w
}

// grafanaStartReq is a browser's GET of start on control.<domain>, with the Cookie header it sends.
func grafanaStartReq(s *Server, rawQuery, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/grafana/start?"+rawQuery, nil)
	if cookie != "" {
		r.Header.Set("cookie", cookie)
	}
	w := httptest.NewRecorder()
	s.handle(w, r)
	return w
}

// grafanaAccount creates name at role (admin is the bootstrap, so create it first) and signs it in.
// It returns the account, its pstack session, and the Cookie header of a browser Grafana's callback
// signed in: exactly the value grafanaCallback sets. Each account is two argon2 runs, so tests share.
func grafanaAccount(t *testing.T, s *Server, name string, role auth.Role) (*auth.UserRow, string, string) {
	t.Helper()
	var err error
	if role == auth.Admin {
		_, err = s.auth.Bootstrap(name, "correct-horse")
	} else {
		_, err = s.auth.CreateUser(name, "correct-horse", auth.CreateOpts{Role: string(role)})
	}
	if err != nil {
		t.Fatal(err)
	}
	session, u, err := s.auth.Login(name, "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	h := auth.HashToken(session)
	return u, session, grafanaSessionCookie + "=" + h + "." + s.grafanaMAC(h)
}

// park is what grafanaStart leaves for session's browser: the code the callback presents.
func park(t *testing.T, s *Server, session, state, next string, ttl int64) string {
	t.Helper()
	code := sso.RandomB64URL(32)
	v := jsonx.Must(jsonx.O("session", auth.HashToken(session), "state", state, "next", next))
	if err := s.auth.Transient().Set("grafana:"+code, string(v), ttl); err != nil {
		t.Fatal(err)
	}
	return code
}

// setCookies is every Set-Cookie line, in order. Get would read only the first.
func setCookies(w *httptest.ResponseRecorder) string {
	return strings.Join(w.Header().Values("set-cookie"), "\n")
}

// negative control: delete grafanaVerify's `if !s.grafanaOn() {…}` — the off subtest fails (302, not 404).
func TestGrafanaVerify(t *testing.T) {
	s := grafanaServer(t)
	s.grafana.Store(true)
	_, _, sami := grafanaAccount(t, s, "sami", auth.Admin)
	_, devSession, dev := grafanaAccount(t, s, "dev", auth.Developer)
	_, _, maint := grafanaAccount(t, s, "maint", auth.Maintainer)
	_, _, viewer := grafanaAccount(t, s, "view", auth.Viewer)

	t.Run("a navigation without a cookie goes to start, absolute, with the state cookie", func(t *testing.T) {
		// negative control: redirect to "/api/auth/grafana/start?state=…" without baseURL(s.opts.Domain, r) — Location is relative.
		w := forwardAuth(s)
		loc := w.Header().Get("location")
		u, _ := url.Parse(loc)
		state := u.Query().Get("state")
		if w.Code != 302 || !grafanaStateRe.MatchString(state) {
			t.Fatalf("got %d %q", w.Code, loc)
		}
		if want := "https://control.preview.example.com/api/auth/grafana/start?state=" + state + "&next=%2Fd%2Fx"; loc != want {
			t.Fatalf("location %q, want %q", loc, want)
		}
		if got, want := setCookies(w), grafanaStateCookie+"="+state+"; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=900"; got != want {
			t.Fatalf("set-cookie %q, want %q", got, want)
		}
	})

	t.Run("a fetch without a cookie is 401 with no Location and no cookie", func(t *testing.T) {
		// negative control: drop the sec-fetch-mode clause — the cors request with dest=document gets a 302.
		// dest=document isolates the mode clause: a real fetch sends dest=empty, which the dest clause refuses too.
		for _, dest := range []string{"empty", "document"} {
			w := forwardAuth(s, "sec-fetch-mode", "cors", "sec-fetch-dest", dest)
			if w.Code != 401 || w.Header().Get("location") != "" || setCookies(w) != "" || w.Body.Len() != 0 {
				t.Fatalf("dest=%s: %d location=%q set-cookie=%q body=%q", dest, w.Code, w.Header().Get("location"), setCookies(w), w.Body.String())
			}
		}
	})

	t.Run("a frame without a cookie is 401 with no state cookie", func(t *testing.T) {
		// negative control: drop the sec-fetch-dest clause — the iframe gets a 302 and a state cookie.
		w := forwardAuth(s, "sec-fetch-dest", "iframe")
		if w.Code != 401 || setCookies(w) != "" {
			t.Fatalf("got %d set-cookie=%q", w.Code, setCookies(w))
		}
	})

	t.Run("X-Forwarded-Host never builds the Location", func(t *testing.T) {
		// negative control: build the start URL from "https://"+requestHost(r) instead of baseURL(s.opts.Domain, r) — Location names evil.example.
		w := forwardAuth(s, "x-forwarded-host", "evil.example")
		if loc := w.Header().Get("location"); !strings.HasPrefix(loc, "https://control.preview.example.com/api/auth/grafana/start?state=") {
			t.Fatalf("location %q", loc)
		}
	})

	t.Run("a protocol-relative X-Forwarded-Uri returns to the root", func(t *testing.T) {
		// negative control: EncodeURIComponent(u.RequestURI()) without sso.SafeNext — next is %2F%2Fevil.example%2Fx.
		w := forwardAuth(s, "x-forwarded-uri", "//evil.example/x")
		if loc := w.Header().Get("location"); !strings.HasSuffix(loc, "&next=%2F") {
			t.Fatalf("location %q", loc)
		}
	})

	t.Run("developer and maintainer are Editor, admin is Admin", func(t *testing.T) {
		// negative control: map auth.Maintainer to "Admin" in grafanaRoles — maint answers Admin.
		for _, c := range []struct{ cookie, user, role string }{{dev, "dev", "Editor"}, {maint, "maint", "Editor"}, {sami, "sami", "Admin"}} {
			w := forwardAuth(s, "cookie", c.cookie)
			if w.Code != 204 || w.Header().Get("x-webauth-user") != c.user || w.Header().Get("x-webauth-role") != c.role {
				t.Fatalf("%s: %d user=%q role=%q", c.user, w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
			}
		}
	})

	t.Run("a viewer is refused", func(t *testing.T) {
		// negative control: add `auth.Viewer: "Viewer"` to grafanaRoles — the viewer gets 204.
		w := forwardAuth(s, "cookie", viewer)
		if w.Code != 403 || w.Body.String() != "No Grafana access.\n" || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d %q user=%q", w.Code, w.Body.String(), w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("a hand-set role is refused, never defaulted", func(t *testing.T) {
		// negative control: `if !ok { role, ok = "Viewer", true }` after the grafanaRoles lookup — superuser gets 204.
		odd, _, cookie := grafanaAccount(t, s, "odd", auth.Viewer)
		// CreateUser and SetRole refuse an unrankable role; an operator's SQL does not.
		if _, err := s.store.DB.Exec("UPDATE users SET role = 'superuser' WHERE id = ?", odd.ID); err != nil {
			t.Fatal(err)
		}
		w := forwardAuth(s, "cookie", cookie)
		if w.Code != 403 || w.Body.String() != "No Grafana access.\n" {
			t.Fatalf("got %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("X-WEBAUTH-* sent by the client are never echoed", func(t *testing.T) {
		// negative control: at the top of grafanaVerify, copy a non-empty r.Header X-Webauth-User/-Role onto w.Header() — the cookieless 401 carries x-webauth-user: admin.
		w := forwardAuth(s, "cookie", dev, "x-webauth-user", "admin", "x-webauth-role", "Admin")
		if w.Code != 204 || w.Header().Get("x-webauth-user") != "dev" || w.Header().Get("x-webauth-role") != "Editor" {
			t.Fatalf("with dev's cookie: %d user=%q role=%q", w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
		}
		w = forwardAuth(s, "sec-fetch-mode", "cors", "x-webauth-user", "admin", "x-webauth-role", "Admin")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" || w.Header().Get("x-webauth-role") != "" {
			t.Fatalf("without a cookie: %d user=%q role=%q", w.Code, w.Header().Get("x-webauth-user"), w.Header().Get("x-webauth-role"))
		}
	})

	t.Run("a forged MAC is no cookie", func(t *testing.T) {
		// negative control: drop `|| !hmac.Equal(…)` from grafanaUser — the forged cookie gets 204.
		forged := grafanaSessionCookie + "=" + auth.HashToken(devSession) + "." + js.B64URL(make([]byte, 32))
		if w := forwardAuth(s, "cookie", forged, "sec-fetch-mode", "cors"); w.Code != 401 {
			t.Fatalf("fetch: got %d", w.Code)
		}
		if w := forwardAuth(s, "cookie", forged); w.Code != 302 {
			t.Fatalf("navigation: got %d", w.Code)
		}
	})

	t.Run("pstack_session alone never passes", func(t *testing.T) {
		// negative control: first thing in grafanaUser, `if who := s.principal(r); who != nil && who.User != nil { return who.User }` — 204.
		w := forwardAuth(s, "cookie", "pstack_session="+devSession, "sec-fetch-mode", "cors")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d user=%q", w.Code, w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("the PSTACK_TOKEN bearer alone never passes", func(t *testing.T) {
		// negative control: first thing in grafanaUser, `if who := s.principal(r); who != nil && who.Kind == auth.KindRoot { return &auth.UserRow{Username: "root", Role: "admin"} }` — 204.
		w := forwardAuth(s, "authorization", "Bearer t0ken", "sec-fetch-mode", "cors")
		if w.Code != 401 || w.Header().Get("x-webauth-user") != "" {
			t.Fatalf("got %d user=%q", w.Code, w.Header().Get("x-webauth-user"))
		}
	})

	t.Run("sign-out, a password change and deletion end access; a demotion to viewer is 403", func(t *testing.T) {
		// negative control: cache the user — a package-level map from cookie value to the row SessionHashUser first returned, answered before the lookup — every revoked cookie still gets 204.
		_, outSession, outCookie := grafanaAccount(t, s, "out", auth.Developer)
		pw, _, pwCookie := grafanaAccount(t, s, "pw", auth.Developer)
		gone, _, goneCookie := grafanaAccount(t, s, "gone", auth.Developer)
		down, _, downCookie := grafanaAccount(t, s, "down", auth.Developer)
		for _, c := range []string{outCookie, pwCookie, goneCookie, downCookie} {
			if w := forwardAuth(s, "cookie", c); w.Code != 204 {
				t.Fatalf("before revoking: got %d", w.Code)
			}
		}
		if err := s.auth.Logout(outSession); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.SetPassword(pw.ID, "another-horse"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.DeleteUser(gone.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.SetRole(down.ID, auth.Viewer); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ after, cookie string }{{"sign-out", outCookie}, {"password change", pwCookie}, {"deletion", goneCookie}} {
			if w := forwardAuth(s, "cookie", c.cookie, "sec-fetch-mode", "cors"); w.Code != 401 {
				t.Errorf("after %s: got %d, want 401", c.after, w.Code)
			}
		}
		if w := forwardAuth(s, "cookie", downCookie); w.Code != 403 {
			t.Errorf("after demotion to viewer: got %d, want 403", w.Code)
		}
	})

	t.Run("an unsafe method needs Grafana's Origin", func(t *testing.T) {
		// negative control: compare requestHost(r) with "grafana."+s.opts.Domain instead of Origin with s.grafanaURL() — the preview POST gets 204.
		for _, c := range []struct {
			name, method, origin string
			code                 int
		}{
			{"POST from Grafana", "POST", "https://grafana.preview.example.com", 204},
			{"POST from a preview", "POST", "https://pr-1.preview.example.com", 403},
			{"POST with no Origin", "POST", "", 403},
			{"no X-Forwarded-Method, a preview Origin", "", "https://pr-1.preview.example.com", 403},
			{"GET from a preview", "GET", "https://pr-1.preview.example.com", 204},
		} {
			w := forwardAuth(s, "cookie", dev, "x-forwarded-method", c.method, "origin", c.origin)
			if w.Code != c.code || (c.code == 403 && w.Body.String() != "Cross-origin request refused.\n") {
				t.Errorf("%s: got %d %q, want %d", c.name, w.Code, w.Body.String(), c.code)
			}
		}
	})

	// The callback cases present a code parked for dev's session, with the navigation headers a
	// browser following Grafana's redirect sends.
	callback := func(code, state string) *httptest.ResponseRecorder {
		return forwardAuth(s, "x-forwarded-uri", grafanaCallbackPath+"?code="+code, "cookie", grafanaStateCookie+"="+state)
	}

	t.Run("callback: a good code and its state cookie sign the browser in", func(t *testing.T) {
		// negative control: drop `hostCookie(grafanaStateCookie, "", 0)` from the callback's redirect — one Set-Cookie, the state never cleared.
		state := sso.RandomB64URL(32)
		w := callback(park(t, s, devSession, state, "/d/x", 60), state)
		h := auth.HashToken(devSession)
		signedIn := grafanaSessionCookie + "=" + h + "." + s.grafanaMAC(h)
		want := signedIn + "; Path=/; Secure; HttpOnly; SameSite=Lax\n" + grafanaStateCookie + "=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0"
		if w.Code != 302 || w.Header().Get("location") != "https://grafana.preview.example.com/d/x" || setCookies(w) != want {
			t.Fatalf("got %d %q\n%s", w.Code, w.Header().Get("location"), setCookies(w))
		}
		if w := forwardAuth(s, "cookie", signedIn); w.Code != 204 || w.Header().Get("x-webauth-user") != "dev" {
			t.Fatalf("with the cookie it set: %d", w.Code)
		}
	})

	t.Run("callback: a replayed code restarts sign-in", func(t *testing.T) {
		// negative control: Get instead of Take in grafanaCallback — the second presentation signs in again.
		state := sso.RandomB64URL(32)
		code := park(t, s, devSession, state, "/d/x", 60)
		if w := callback(code, state); !strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("first presentation: %d %q", w.Code, setCookies(w))
		}
		w := callback(code, state)
		if loc := w.Header().Get("location"); w.Code != 302 || !strings.HasPrefix(loc, "https://control.preview.example.com/api/auth/grafana/start?state=") || !strings.HasSuffix(loc, "&next=%2F") || strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("replay: %d %q %q", w.Code, loc, setCookies(w))
		}
	})

	t.Run("callback: a state mismatch signs nobody in and burns the code", func(t *testing.T) {
		// negative control: redirect before comparing the state (compare after the redirect) — the mismatched browser gets the session cookie. Also run: Get, compare, Take only on a match — the retry with the right state signs in.
		state := sso.RandomB64URL(32)
		code := park(t, s, devSession, state, "/d/x", 60)
		if w := callback(code, sso.RandomB64URL(32)); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("a mismatched state signed in: %q", setCookies(w))
		}
		if w := callback(code, state); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("the code survived a mismatch: %q", setCookies(w))
		}
	})

	t.Run("callback: an expired code signs nobody in", func(t *testing.T) {
		// negative control: drop `AND expires_at > ?` (and its argument) from SqliteTransientStore.Take in sso/transient.go — the ttl-0 code signs in.
		// Set(…, 0) stores expires_at = now, and Take's `expires_at > now` is already false: no sleep.
		state := sso.RandomB64URL(32)
		if w := callback(park(t, s, devSession, state, "/d/x", 0), state); strings.Contains(setCookies(w), grafanaSessionCookie+"=") {
			t.Fatalf("an expired code signed in: %q", setCookies(w))
		}
	})

	t.Run("callback: a reload by a signed-in browser goes to Grafana's home", func(t *testing.T) {
		// negative control: delete the `if user != nil { redirect(w, s.grafanaURL()+"/") … }` branch — the reload gets 204.
		w := forwardAuth(s, "x-forwarded-uri", grafanaCallbackPath+"?code="+sso.RandomB64URL(32), "cookie", dev)
		if w.Code != 302 || w.Header().Get("location") != "https://grafana.preview.example.com/" {
			t.Fatalf("got %d %q", w.Code, w.Header().Get("location"))
		}
	})

	t.Run("off: no domain, no token or no grafana container is a plain-text 404", func(t *testing.T) {
		// negative control: drop `if !s.grafanaOn() {…}` from grafanaVerify — each off server answers 302.
		for _, c := range []struct {
			name string
			set  func(*Server)
		}{
			{"no domain", func(o *Server) { o.grafana.Store(true); o.opts.Domain = "" }},
			{"no token", func(o *Server) { o.grafana.Store(true); o.opts.Token = "" }},
			{"no grafana container", func(o *Server) {}},
		} {
			o := grafanaServer(t)
			c.set(o)
			w := forwardAuth(o)
			if w.Code != 404 || w.Body.String() != "Not found.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
				t.Errorf("%s: got %d %q %q", c.name, w.Code, w.Body.String(), w.Header().Get("content-type"))
			}
		}
	})
}

// negative control: writeError(w, 400, "Sign-in link is invalid.") — the malformed-state subtest fails (JSON body).
func TestGrafanaStart(t *testing.T) {
	s := grafanaServer(t)
	s.grafana.Store(true)
	_, session, _ := grafanaAccount(t, s, "sami", auth.Admin)
	state := sso.RandomB64URL(32)

	t.Run("signed in: an absolute 302 to the callback, and the code parks the hash, the state and a safe next", func(t *testing.T) {
		// negative control: park `c` instead of `auth.HashToken(c)` — the parked session is the raw cookie. Also run: drop `next = sso.SafeNext(next)` (parks //evil.example/x); range only sessionCandidates(r)[:1] (the stale cookie ends on /login).
		for _, c := range []struct{ next, parked string }{{"%2Fd%2Fx", "/d/x"}, {"%2F%2Fevil.example%2Fx", "/"}} {
			// A stale cookie first: start takes the first candidate SessionUser resolves.
			w := grafanaStartReq(s, "state="+state+"&next="+c.next, "pstack_session=pstack_ses_stale; pstack_session="+session)
			loc := w.Header().Get("location")
			code := strings.TrimPrefix(loc, "https://grafana.preview.example.com/-/pstack/callback?code=")
			if w.Code != 302 || !grafanaStateRe.MatchString(code) {
				t.Fatalf("next=%s: got %d %q", c.next, w.Code, loc)
			}
			raw, found, err := s.auth.Transient().Get("grafana:" + code)
			if err != nil || !found {
				t.Fatalf("next=%s: nothing parked (%v)", c.next, err)
			}
			v, _ := omap.Parse([]byte(raw))
			m, _ := v.(*omap.Map)
			var got [3]string
			got[0], _ = getStr(m, "session")
			got[1], _ = getStr(m, "state")
			got[2], _ = getStr(m, "next")
			if want := [3]string{auth.HashToken(session), state, c.parked}; got != want {
				t.Fatalf("next=%s: parked %q, want %q", c.next, got, want)
			}
		}
	})

	t.Run("signed out: 302 to the login page, whose next decodes back to this start URL", func(t *testing.T) {
		// negative control: drop the inner js.EncodeURIComponent(next) — the decoded next ends &next=/d/x.
		w := grafanaStartReq(s, "state="+state+"&next=%2Fd%2Fx", "")
		loc := w.Header().Get("location")
		back, ok := js.DecodeURIComponent(strings.TrimPrefix(loc, "/login?next="))
		if w.Code != 302 || !strings.HasPrefix(loc, "/login?next=") || !ok || back != "/api/auth/grafana/start?state="+state+"&next=%2Fd%2Fx" {
			t.Fatalf("got %d %q (decodes to %q)", w.Code, loc, back)
		}
	})

	t.Run("a malformed state is a plain-text 400", func(t *testing.T) {
		// negative control: writeError(w, 400, "Sign-in link is invalid.") — the body is JSON and the content-type application/json.
		for _, q := range []string{"", "state=short&next=%2F", "state=" + strings.Repeat("a", 42) + ".&next=%2F"} {
			w := grafanaStartReq(s, q, "pstack_session="+session)
			if w.Code != 400 || w.Body.String() != "Sign-in link is invalid.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
				t.Errorf("%q: got %d %q %q", q, w.Code, w.Body.String(), w.Header().Get("content-type"))
			}
		}
	})

	t.Run("off: a plain-text 404", func(t *testing.T) {
		// negative control: drop `if !s.grafanaOn() {…}` from grafanaStart — the off server answers 302 to /login.
		o := grafanaServer(t)
		w := grafanaStartReq(o, "state="+state+"&next=%2F", "")
		if w.Code != 404 || w.Body.String() != "Not found.\n" || !strings.HasPrefix(w.Header().Get("content-type"), "text/plain") {
			t.Fatalf("got %d %q %q", w.Code, w.Body.String(), w.Header().Get("content-type"))
		}
	})
}
