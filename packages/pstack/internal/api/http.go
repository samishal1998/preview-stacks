// Package api is the HTTP control plane. This file is the request/response plumbing every route
// shares: the JSON envelope, query variables, cookies, the base URL, and the byte-level helpers the
// terminal and the tuning knobs need. Routes live in routes.go; the principal and the gate in
// principal.go.
package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// writeJSON is `json(body, init)`: two-space pretty-printed, bare `application/json`. Every API
// response goes through it, so the whole API is formatted one way.
func writeJSON(w http.ResponseWriter, status int, body any, headers ...[2]string) {
	b, err := jsonx.MarshalIndent(body)
	if err != nil {
		b = []byte(`{"error":"could not encode the response"}`)
		status = 500
	}
	h := w.Header()
	h.Set("content-type", "application/json")
	for _, kv := range headers {
		h.Add(kv[0], kv[1])
	}
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// errorBody is `{ error }`, the one error shape.
type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// Tuning is what `serve` reads from the environment: the same knobs a test passes as options, so a
// black-box harness driving the real CLI can reach the expiry paths. `??` semantics — unset means
// the default, and a value that is not a positive number is ignored rather than turning a timeout
// into NaN.
type Tuning struct {
	ReadinessPollMs      float64
	ReadinessTimeoutMs   float64
	ReadinessRestartLoop float64
	SSOStateTTLS         float64
	SSODiscoveryTTLS     float64
	// MaxJobs is PSTACK_MAX_JOBS: how many lifecycle jobs RUN AT ONCE across every stack, zero
	// meaning jobs.DefaultMaxRunning. It is NOT jobs.MaxJobs — that one bounds how many finished
	// transcripts are kept and is not tunable. One letter apart in name, nothing in common.
	MaxJobs float64
}

// TuningFromEnv reads the knobs.
func TuningFromEnv(env func(string) (string, bool)) Tuning {
	num := func(k string) float64 {
		v, ok := env(k)
		if !ok || v == "" {
			return 0
		}
		n := js.ParseNumber(v)
		if !js.IsFinite(n) || n <= 0 {
			return 0
		}
		return n
	}
	return Tuning{
		ReadinessPollMs:      num("PSTACK_READINESS_POLL_MS"),
		ReadinessTimeoutMs:   num("PSTACK_READINESS_TIMEOUT_MS"),
		ReadinessRestartLoop: num("PSTACK_READINESS_RESTART_LOOP"),
		SSOStateTTLS:         num("PSTACK_SSO_STATE_TTL_S"),
		SSODiscoveryTTLS:     num("PSTACK_SSO_DISCOVERY_TTL_S"),
		MaxJobs:              num("PSTACK_MAX_JOBS"),
	}
}

// CrToNl is CR → NL for terminal input. With no pty there is no line discipline, so this is the only
// thing standing between "Enter" and a shell that never runs anything. CRLF collapses to ONE NL, or
// every Enter from a client that sends both would run the line and then an empty one. Same rule for
// text and binary frames.
func CrToNl(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\r' {
			out = append(out, '\n')
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// varsFrom is `?PR=7&REGION=eu` → {PR:7, REGION:eu}: every query parameter except `token` becomes a
// spec variable (the unused ones — tail, service, wait — are simply never read by resolution).
// `token` is how a share link authenticates, and a JWT landing in a hook's environment as `$token`
// would be a credential nobody asked for. Repeated keys: the LAST value wins (Object.fromEntries).
func varsFrom(rawQuery string) map[string]string {
	_, m := js.LastWins(js.ParseQuery(rawQuery))
	delete(m, "token")
	return m
}

// query reads one parameter with URLSearchParams semantics (last wins); ok=false when absent.
func query(rawQuery, key string) (string, bool) {
	_, m := js.LastWins(js.ParseQuery(rawQuery))
	v, ok := m[key]
	return v, ok
}

// coerceEnv turns a PUT body's `env`/`vars` into string variables, in the body's key order. Returns
// nil and false when the shape is wrong, so the caller answers 400 rather than interpolating
// `[object Object]` into a stack name. Absent (nil) is an empty map.
func coerceEnv(v any) (keys []string, out map[string]string, ok bool) {
	out = map[string]string{}
	keys = []string{}
	switch x := v.(type) {
	case nil:
		return keys, out, true
	case *omap.Map:
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			switch val.(type) {
			case string, int64, float64, bool:
				out[k] = js.ToString(val)
				keys = append(keys, k)
			default:
				return nil, nil, false
			}
		}
		return keys, out, true
	default:
		return nil, nil, false
	}
}

var sessionCookieRe = regexp.MustCompile(`(?:^|;\s*)pstack_session=([^;]+)`)

// sessionCandidates is EVERY `pstack_session` value in the Cookie header, in header order, not just
// the first. A browser can legitimately hold two cookies with the same name — one set with Secure
// over https and one over plain http, or a survivor from a previous server database — and it sends
// BOTH. Reading only the first locked operators out.
func sessionCandidates(r *http.Request) []string {
	var out []string
	for _, m := range sessionCookieRe.FindAllStringSubmatch(r.Header.Get("cookie"), -1) {
		if m[1] != "" {
			out = append(out, m[1])
		}
	}
	return out
}

// sessionCookie is the Set-Cookie value. `Secure` only when the request arrived over TLS (Traefik
// sets X-Forwarded-Proto) — hardcoding it would break plain-HTTP loopback development, and
// omitting it would let a production cookie travel over HTTP.
func sessionCookie(r *http.Request, value string, maxAgeSeconds int) string {
	secure := ""
	if r.Header.Get("x-forwarded-proto") == "https" {
		secure = "; Secure"
	}
	return "pstack_session=" + value + "; HttpOnly; Path=/; SameSite=Lax; Max-Age=" + strconv.Itoa(maxAgeSeconds) + secure
}

// baseURL is this service's own base URL: from CONFIG (PSTACK_DOMAIN) whenever there is one, and
// only then from the request's headers. A forwarded header is caller-controlled, and an SSO
// redirect_uri that differs between legs simply fails; the header fallback is for a loopback dev run.
func baseURL(domain string, r *http.Request) string {
	if domain != "" {
		return "https://control." + domain
	}
	proto := r.Header.Get("x-forwarded-proto")
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := r.Header.Get("x-forwarded-host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

// portRe is the TS `.replace(/:\d+$/, ”)`.
var portRe = regexp.MustCompile(`:\d+$`)

// requestHost is the hostname a request carried, as the wake catch-all reads it: the first
// X-Forwarded-Host hop, else Host, port stripped, lower-cased.
func requestHost(r *http.Request) string {
	h := r.Header.Get("x-forwarded-host")
	if h == "" {
		h = r.Host
	}
	h = strings.TrimSpace(strings.Split(h, ",")[0])
	return strings.ToLower(portRe.ReplaceAllString(h, ""))
}

// bearer is the `Authorization: Bearer x` value, or "".
func bearer(r *http.Request) string {
	h := r.Header.Get("authorization")
	if strings.HasPrefix(h, "Bearer ") && len(h) > 7 {
		return h[7:]
	}
	return ""
}
