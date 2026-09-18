// Grafana at G = https://grafana.<domain> (`pstack init --logging loki`), signed in with pstack
// accounts. Spec of record: docs/loki-logging-design.md, "Slice 3".
//
// ── IS IT ON ─────────────────────────────────────────────────────────────────────────────────────
//
// Read from docker (inspect.GrafanaOn) and cached in s.grafana by Start and reindexLoop. It is never a
// setting and never asked per request, so a `pstack logging` switch shows within 30 s (the next
// reindexLoop tick), with no pstack restart. Sign-in also needs PSTACK_DOMAIN (every URL here is
// built from config, never from a request header) and PSTACK_TOKEN (the cookie's MAC key). When off,
// /api/health has no `grafana` key.
//
// Sign-in. Traefik's forwardAuth asks verify about EVERY request to grafana.<domain>, and verify
// answers from __Host-pstack_grafana alone: never pstack_session, never a bearer, never principal().
// That cookie is `<id_hash>.<mac>`, the parent pstack session's stored hash signed with PSTACK_TOKEN
// under a "pstack-grafana\n" prefix: the share-link precedent, so there is no table, and the session
// row IS the Grafana session (a pstack sign-out ends Grafana access on the next request). Getting the
// cookie is a redirect sign-in: verify sets a state cookie and sends a top-level navigation to start
// on control.<domain>; start parks a 60 s single-use code; the callback is a branch of verify, because
// forwardAuth relays a 302 and its Set-Cookie. Roles: developer and maintainer are Editor, admin is
// Admin, and viewers are refused (owner, 2026-09-15): a Grafana Viewer still queries Loki's
// unredacted lines.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
)

// grafanaOn: this host runs Grafana (s.grafana, from docker) and has what sign-in needs.
func (s *Server) grafanaOn() bool {
	return s.opts.Domain != "" && s.opts.Token != "" && s.grafana.Load()
}

// grafanaURL is G. From config, never from a request header.
func (s *Server) grafanaURL() string { return "https://grafana." + s.opts.Domain }

// __Host- names: a browser keeps one only host-only, Secure and Path=/, so a preview on *.<domain>
// cannot plant either (cookie tossing).
const (
	grafanaSessionCookie = "__Host-pstack_grafana"
	grafanaStateCookie   = "__Host-pstack_grafana_state"
	// Answered INSIDE verify: forwardAuth relays a 302 and its Set-Cookie verbatim, so the callback
	// needs no router of its own and never reaches Grafana.
	grafanaCallbackPath = "/-/pstack/callback"
)

// A lookup table, never ranged into output (rule 5). A role missing here is refused, not defaulted.
// No auth.Viewer: viewers are refused (owner, 2026-09-15) — a Grafana Viewer can still query Loki's
// unredacted lines through panels and /api/ds/query.
var grafanaRoles = map[auth.Role]string{auth.Developer: "Editor", auth.Maintainer: "Editor", auth.Admin: "Admin"}

var grafanaStateRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// hostCookie is a __Host- Set-Cookie. Secure ALWAYS, unlike sessionCookie: a browser drops a __Host-
// cookie without it, and Grafana is only reached through Traefik's TLS. maxAge < 0: none.
func hostCookie(name, value string, maxAge int) [2]string {
	v := name + "=" + value + "; Path=/; Secure; HttpOnly; SameSite=Lax"
	if maxAge >= 0 {
		v += "; Max-Age=" + strconv.Itoa(maxAge)
	}
	return [2]string{"set-cookie", v}
}

func (s *Server) grafanaMAC(hash string) string {
	m := hmac.New(sha256.New, []byte(s.opts.Token))
	m.Write([]byte("pstack-grafana\n" + hash))
	return js.B64URL(m.Sum(nil))
}

// grafanaUser is the account behind this browser's Grafana cookie, or nil. The parent session's row
// IS the Grafana session: delete it and this answers nil on the very next request.
// ponytail: one SQLite statement per Grafana request on the single pooled connection; if page loads
// stall behind transactions, cache hash→user for a few seconds (revocation then lags by that TTL).
func (s *Server) grafanaUser(r *http.Request) *auth.UserRow {
	c, err := r.Cookie(grafanaSessionCookie)
	if err != nil {
		return nil
	}
	hash, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(s.grafanaMAC(hash))) {
		return nil
	}
	u, _ := s.auth.SessionHashUser(hash)
	return u
}

// grafanaVerify is Traefik's forwardAuth for grafana.<domain>. Every Location is absolute and built
// from config: Traefik resolves a relative one against http://pstack:7878.
func (s *Server) grafanaVerify(w http.ResponseWriter, r *http.Request) {
	if !s.grafanaOn() {
		http.Error(w, "Not found.", 404)
		return
	}
	u, err := url.ParseRequestURI(r.Header.Get("x-forwarded-uri"))
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	user := s.grafanaUser(r)
	if u.Path == grafanaCallbackPath {
		code, _ := query(u.RawQuery, "code")
		if s.grafanaCallback(w, r, code) {
			return
		}
		if user != nil { // a reloaded callback: nothing to finish
			redirect(w, s.grafanaURL()+"/")
			return
		}
		u = &url.URL{Path: "/"} // expired, replayed or not this browser's: sign in afresh
	}
	if user == nil {
		// A top-level navigation only. fetch gets 401 (a 302 would be a CORS error in Grafana's
		// frontend), and so does an <iframe>: a same-site preview must not frame a silent sign-in.
		if r.Header.Get("sec-fetch-mode") != "navigate" || r.Header.Get("sec-fetch-dest") != "document" {
			w.WriteHeader(401)
			return
		}
		state := sso.RandomB64URL(32)
		redirect(w, baseURL(s.opts.Domain, r)+"/api/auth/grafana/start?state="+state+
			"&next="+js.EncodeURIComponent(sso.SafeNext(u.RequestURI())),
			hostCookie(grafanaStateCookie, state, 900))
		return
	}
	role, ok := grafanaRoles[auth.Role(user.Role)]
	if !ok {
		http.Error(w, "No Grafana access.", 403)
		return
	}
	// Every preview on *.<domain> is SAME-SITE with Grafana: SameSite=Lax does not stop its page
	// posting here with the cookie attached. Traefik sets X-Forwarded-Method from the real request.
	if m := r.Header.Get("x-forwarded-method"); m != http.MethodGet && m != http.MethodHead && r.Header.Get("origin") != s.grafanaURL() {
		http.Error(w, "Cross-origin request refused.", 403)
		return
	}
	w.Header().Set("X-WEBAUTH-USER", user.Username)
	w.Header().Set("X-WEBAUTH-ROLE", role)
	w.WriteHeader(204)
}

// grafanaCallback finishes a sign-in. Take FIRST: any presentation burns the code, including one
// that fails the state check, so a code read from Traefik's access log is already spent.
func (s *Server) grafanaCallback(w http.ResponseWriter, r *http.Request, code string) bool {
	if code == "" {
		return false
	}
	raw, found, err := s.auth.Transient().Take("grafana:" + code)
	if err != nil || !found {
		return false
	}
	parsed, _ := omap.Parse([]byte(raw))
	parked, _ := parsed.(*omap.Map)
	state, _ := getStr(parked, "state")
	session, _ := getStr(parked, "session")
	next, _ := getStr(parked, "next")
	c, err := r.Cookie(grafanaStateCookie)
	if err != nil || state == "" || session == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		return false
	}
	redirect(w, s.grafanaURL()+sso.SafeNext(next),
		hostCookie(grafanaSessionCookie, session+"."+s.grafanaMAC(session), -1),
		hostCookie(grafanaStateCookie, "", 0))
	return true
}

// grafanaStart runs on control.<domain>, where pstack_session lives: it turns that session into a
// 60-second, single-use code bound to the state cookie Grafana's host set. A browser navigated here,
// so every failure is plain text (the ssoFailed rule, routes_auth.go:52-53).
func (s *Server) grafanaStart(w http.ResponseWriter, r *http.Request) {
	if !s.grafanaOn() {
		http.Error(w, "Not found.", 404)
		return
	}
	state, _ := query(r.URL.RawQuery, "state")
	if !grafanaStateRe.MatchString(state) {
		http.Error(w, "Sign-in link is invalid.", 400)
		return
	}
	next, _ := query(r.URL.RawQuery, "next")
	next = sso.SafeNext(next)
	for _, c := range sessionCandidates(r) {
		if u, _ := s.auth.SessionUser(c); u != nil {
			code := sso.RandomB64URL(32)
			parked := jsonx.Must(jsonx.O("session", auth.HashToken(c), "state", state, "next", next))
			if err := s.auth.Transient().Set("grafana:"+code, string(parked), 60); err != nil {
				s.opts.Log("[grafana] sign-in code not stored: " + err.Error())
				http.Error(w, "Sign-in failed.", 500)
				return
			}
			redirect(w, s.grafanaURL()+grafanaCallbackPath+"?code="+code)
			return
		}
	}
	redirect(w, "/login?next="+js.EncodeURIComponent("/api/auth/grafana/start?state="+state+"&next="+js.EncodeURIComponent(next)))
}
