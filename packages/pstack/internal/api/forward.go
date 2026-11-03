// Forwarding (docs/migrate-forward-design.md): this host (A) passes deploys and visitors it no
// longer serves to another pstack (B). This file is what every forwarding path shares: the stored
// row, the transport that reaches B, and the deploy-API proxy. routes_forward.go is the setting's API.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// forwardedBy marks a request one pstack passed to another. A request carrying it is served where
// it lands, never passed on again: the loop guard, and how `migrate` reaches A itself.
const forwardedBy = "X-Pstack-Forwarded-By"

// Package vars so tests can shorten them.
var (
	// forwardWaits are the pauses between dial attempts: 3 attempts in total.
	forwardWaits = []time.Duration{2 * time.Second, 5 * time.Second}
	// forwardHeaderTimeout bounds B's wait before its response headers. Above GET …/readiness's
	// 60 s ?wait=, which answers only when the wait ends; streams send headers at once, so no
	// overall timeout cuts a log or job stream short.
	forwardHeaderTimeout = 90 * time.Second
	// forwardDial makes one TCP connection; a test wraps it to count attempts.
	forwardDial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
)

// dialError is a dial that failed every attempt: B was never reached.
type dialError struct {
	attempts int
	err      error
}

func (e *dialError) Error() string { return e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// forwardRow is the setting, or nil when unset. A read error is logged and reads as unset: this
// host then serves locally instead of failing every request.
func (s *Server) forwardRow() *store.Forward {
	f, err := store.GetForward(s.store.DB)
	if err != nil {
		s.opts.Log("forward: " + err.Error())
		return nil
	}
	return f
}

// forwardTransport reaches B. It dials f.Via, or f.To's host, never the request URL's host (a
// preview hostname, or api.<domain> before DNS moves, resolves back to A). Host and the TLS server
// name stay the request URL's, so B's certificate is checked for the name asked for.
//
// retry is for the proxies; the establishing check passes false and dials once. Only the TCP dial
// (and its DNS lookup) is retried: a TLS handshake or anything later may have reached B, and a
// request B received may already have started a job (P-R2).
func forwardTransport(f *store.Forward, retry bool, base *tls.Config) *http.Transport {
	to, _ := url.Parse(f.To)
	tries := 1
	if retry {
		tries += len(forwardWaits)
	}
	return &http.Transport{
		TLSClientConfig: base.Clone(),
		// ponytail: a new connection per request; cache one Transport per (to, via, retry) if a
		// retiring host's throughput ever matters.
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: forwardHeaderTimeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			addr = forwardDialAddr(f.Via, to, addr)
			for i := 1; ; i++ {
				c, err := forwardDial(ctx, network, addr)
				if err == nil {
					return c, nil
				}
				if i == tries {
					return nil, &dialError{attempts: i, err: err}
				}
				select {
				case <-time.After(forwardWaits[i-1]):
				case <-ctx.Done():
					return nil, &dialError{attempts: i, err: err}
				}
			}
		},
	}
}

// forwardsAPI is the part of the API that follows the deploys to B (spec §1).
func forwardsAPI(path string) bool {
	for _, p := range []string{"/api/deployments", "/api/jobs"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// forwardAPI passes /api/deployments… and /api/jobs… to B while forwarding is on, after A's own
// gate has admitted the caller at the route's tier. True when it answered. Served here instead: a
// share link (signed with A's key, so B could not verify it), a request another pstack already
// forwarded, and a job A itself holds (a control job such as loki-apply, MF-R13).
func (s *Server) forwardAPI(w http.ResponseWriter, r *http.Request, path string, who *auth.Principal) bool {
	if !forwardsAPI(path) || who.Kind == auth.KindShare {
		return false
	}
	if _, ok := r.Header[forwardedBy]; ok {
		return false
	}
	for _, re := range []*regexp.Regexp{cancelRe, jobRe} {
		if m := re.FindStringSubmatch(path); m != nil {
			if id, err := segment(m[1]); err == nil {
				if _, ok := s.jobs.Get(id); ok {
					return false
				}
			}
			break
		}
	}
	f := s.forwardRow()
	if f == nil {
		return false
	}
	to, err := url.Parse(f.To)
	if err != nil {
		s.opts.Log("forward: " + err.Error())
		return false
	}
	rp := &httputil.ReverseProxy{
		Transport: forwardTransport(f, true, s.forwardTLS),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(to) // and Out.Host = "": Host is to's host
			pr.Out.Header.Set("Authorization", "Bearer "+f.Token)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set(forwardedBy, s.opts.Domain)
		},
		ModifyResponse: func(*http.Response) error {
			s.forwardTouch()
			return nil
		},
		// Built from to's host and err only: the outbound request carries the token.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				return // the caller went away; B did not fail
			}
			msg, attempts := "forward: "+to.Host+": "+err.Error(), 1
			var de *dialError
			if errors.As(err, &de) {
				msg = "forward: " + to.Host + " unreachable after " + strconv.Itoa(de.attempts) + " attempts"
				attempts = de.attempts
			}
			s.bus.Emit("forward.failed", jsonx.O("to", f.To, "error", err.Error(), "attempts", attempts))
			writeError(w, 502, msg)
		},
	}
	rp.ServeHTTP(w, r)
	return true
}

// forwardTouch records an answer from B as lastOkAt, at most once a minute (P-R3): every forwarded
// request would otherwise be a database write. The mark resets when the setting changes.
func (s *Server) forwardTouch() {
	now := time.Now().UnixMilli()
	if s.forwardNow != nil {
		now = s.forwardNow()
	}
	last := s.forwardTouched.Load()
	if now-last < 60_000 || !s.forwardTouched.CompareAndSwap(last, now) {
		return
	}
	if err := store.TouchForwardOK(s.store.DB, now); err != nil {
		s.opts.Log("forward: " + err.Error())
	}
}

// forwardDialAddr is where to connect for a request Go would send to addr: via's address, else to's
// host; via's port, else addr's (to's port for an API call, 443 for visitor traffic).
func forwardDialAddr(via string, to *url.URL, addr string) string {
	if ap, err := netip.ParseAddrPort(via); err == nil {
		return ap.String()
	}
	_, port, _ := net.SplitHostPort(addr)
	host := to.Hostname()
	if a, err := netip.ParseAddr(via); err == nil {
		host = a.String()
	}
	return net.JoinHostPort(host, port)
}
