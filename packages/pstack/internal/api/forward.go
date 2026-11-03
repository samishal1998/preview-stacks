// Forwarding (docs/migrate-forward-design.md): this host (A) passes deploys and visitors it no
// longer serves to another pstack (B). This file is what every forwarding path shares: the stored
// row and the transport that reaches B. routes_forward.go is the setting's API.
package api

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

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
// retry is for the proxies; the establishing check passes false and dials once.
func forwardTransport(f *store.Forward, retry bool, base *tls.Config) *http.Transport {
	_ = retry
	to, _ := url.Parse(f.To)
	return &http.Transport{
		TLSClientConfig: base.Clone(),
		// ponytail: a new connection per request; cache one Transport per (to, via, retry) if a
		// retiring host's throughput ever matters.
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, forwardDialAddr(f.Via, to, addr))
		},
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
