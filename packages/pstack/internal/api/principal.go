package api

import (
	"net/http"
	"regexp"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
)

// principal is who this request is. Three ways in, checked cheapest-first:
//
//	Bearer PSTACK_TOKEN   → root (the machine credential)
//	Bearer pstack_pat_…   → the token's user
//	Cookie pstack_session → the session's user — the only form a browser can attach to
//	                        EventSource and WebSocket, which is why sessions are cookies at all
//
// With no PSTACK_TOKEN configured, everything is root: the loopback-only dev mode.
//
// A bearer that does not authenticate FALLS THROUGH to the cookie: a stray autofilled header must
// not turn a live session into a permanent lockout. It grants nothing — falling through can only
// authenticate the caller as the session they already hold.
func (s *Server) principal(r *http.Request) *auth.Principal {
	token := s.opts.Token
	if token == "" {
		return &auth.Principal{Kind: auth.KindRoot}
	}
	if b := bearer(r); b != "" {
		if len(b) == len(token) && b == token {
			return &auth.Principal{Kind: auth.KindRoot}
		}
		if len(b) > 11 && b[:11] == "pstack_pat_" {
			if u, _ := s.auth.TokenUser(b); u != nil {
				return &auth.Principal{Kind: auth.KindUser, User: u}
			}
		}
		if share.LooksLikeToken(b) {
			if c := share.Verify(token, b, time.Now().UnixMilli()); c != nil {
				return &auth.Principal{Kind: auth.KindShare, Deployment: c.Dep, Views: c.Views}
			}
		}
	}
	for _, candidate := range sessionCandidates(r) {
		if u, _ := s.auth.SessionUser(candidate); u != nil {
			return &auth.Principal{Kind: auth.KindUser, User: u}
		}
	}
	// A share link's token from the query string — the only way an EventSource can carry one. Last,
	// and only ever a JWT: the raw PSTACK_TOKEN in a URL would be root in every access log.
	if q, ok := query(r.URL.RawQuery, "token"); ok && q != "" && share.LooksLikeToken(q) {
		if c := share.Verify(token, q, time.Now().UnixMilli()); c != nil {
			return &auth.Principal{Kind: auth.KindShare, Deployment: c.Dep, Views: c.Views}
		}
	}
	return nil
}

var shareRouteRe = regexp.MustCompile(`^/api/deployments/([^/]+)(/.*)?$`)

// shareAllows is what a share principal may reach: GET only, its own deployment only, and only the
// routes its views name. Enforced before any route so a new route is closed to it by default.
func shareAllows(who *auth.Principal, method, path string) bool {
	if who.Kind != auth.KindShare {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	if path == "/api/auth/me" {
		return true
	}
	m := shareRouteRe.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	id, ok := js.DecodeURIComponent(m[1])
	if !ok || id != who.Deployment {
		return false
	}
	details, logs := false, false
	for _, v := range who.Views {
		switch v {
		case share.Details:
			details = true
		case share.Logs:
			logs = true
		}
	}
	switch m[2] {
	case "":
		return details
	case "/runtime", "/readiness":
		return details
	case "/logs", "/logs/stream":
		return logs
	}
	return false
}

// authed is `principal(req) !== null`, for the two routes that withhold a source otherwise.
func (s *Server) authed(r *http.Request) bool { return s.principal(r) != nil }
