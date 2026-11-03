// `/api/forward` — the setting that sends this host's deploys and unserved visitors to another pstack
// (forward.go). Maintainer reads; admin writes, for blast radius: it decides where every deploy runs
// (permissions.go).
//
// The token goes in and never comes back out (invariant 15): not in a read, an event, a log line or
// an error. PUT checks B before saving anything, and any failed check answers 400 naming it.
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// forwardView is the row without its token.
func forwardView(f *store.Forward) jsonx.Object {
	return jsonx.O("to", f.To, "via", nullable(f.Via), "establishedAt", f.EstablishedAt, "lastOkAt", f.LastOkAt)
}

func (s *Server) forwardGet(w http.ResponseWriter) error {
	f, err := store.GetForward(s.store.DB)
	if err != nil {
		return err
	}
	if f == nil {
		writeJSON(w, 200, nil)
		return nil
	}
	writeJSON(w, 200, forwardView(f))
	return nil
}

func (s *Server) forwardPut(w http.ResponseWriter, r *http.Request) error {
	body := bodyObject(r)
	to, _ := getStr(body, "to")
	via, _ := getStr(body, "via")
	token, _ := getStr(body, "token")
	to = strings.TrimRight(strings.TrimSpace(to), "/")
	if to == "" || token == "" {
		writeError(w, 400, "body must be { to, via?, token }: B's API URL and a user token on B")
		return nil
	}
	u, err := url.Parse(to)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		writeError(w, 400, "`to` must be an http(s) URL, e.g. https://api.b.example.com")
		return nil
	}
	if u.Scheme == "http" && !notify.IsPrivateHost(u.Hostname()) {
		writeError(w, 400, "plain http only to a loopback or private address: use https://")
		return nil
	}
	if via != "" {
		if _, err := netip.ParseAddrPort(via); err != nil {
			if _, err := netip.ParseAddr(via); err != nil {
				writeError(w, 400, "`via` must be an IP, optionally with a port")
				return nil
			}
		}
	} else if s.routing.IsControlHostname(u.Hostname(), s.opts.Domain) {
		// Only without via: with one, B's /api/auth/me check below and the loop guard catch a
		// misconfiguration, and api.<shared domain> is exactly what a via exists for.
		writeError(w, 400, u.Hostname()+" is this host's own API")
		return nil
	}
	f := store.Forward{To: to, Via: via, Token: token}
	if msg := s.forwardCheck(&f, u.Host); msg != "" {
		writeError(w, 400, msg)
		return nil
	}
	now := time.Now().UnixMilli()
	f.EstablishedAt, f.LastOkAt = now, now
	if err := store.SetForward(s.store.DB, f); err != nil {
		return err
	}
	s.bus.Emit("forward.changed", jsonx.O("to", f.To, "via", nullable(f.Via)))
	writeJSON(w, 200, forwardView(&f))
	return nil
}

// forwardCheck is the establishing check: B's GET /api/auth/me with the token answers 200 for an
// account (not root) at developer or higher, the deployment routes' tier. "" is a pass; otherwise
// the refusal, which never carries the token or B's body.
func (s *Server) forwardCheck(f *store.Forward, host string) string {
	client := &http.Client{
		Transport: forwardTransport(f, false, s.forwardTLS),
		Timeout:   10 * time.Second,
		// A redirect is a refusal, never a hop: following one would resend the token (Go rule 12).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, f.To+"/api/auth/me", nil)
	if err != nil {
		return "cannot reach " + host + ": " + err.Error()
	}
	req.Header.Set("authorization", "Bearer "+f.Token)
	res, err := client.Do(req)
	if err != nil {
		return "cannot reach " + host + ": " + err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return host + " answered " + res.Status + " to GET /api/auth/me"
	}
	var me struct {
		Root bool `json:"root"`
		User *struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&me); err != nil {
		return host + " did not answer as pstack"
	}
	if me.Root {
		return "that is " + host + "'s root token: use a user token, developer or higher"
	}
	role := "not an account"
	if me.User != nil {
		role = me.User.Role
	}
	if auth.Role(role).Rank() < auth.Developer.Rank() {
		return "the token on " + host + " is " + role + ": developer or higher"
	}
	return ""
}

func (s *Server) forwardDelete(w http.ResponseWriter) error {
	f := s.forwardRow()
	if err := store.DeleteForward(s.store.DB); err != nil {
		return err
	}
	if f != nil {
		// An object, never Emit(nil) (which sends {}): receivers read data.to.
		s.bus.Emit("forward.changed", jsonx.O("to", nil, "via", nil))
	}
	writeJSON(w, 200, jsonx.O("deleted", true))
	return nil
}
