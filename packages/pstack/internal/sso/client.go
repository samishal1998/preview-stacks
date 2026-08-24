package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// ── endpoints: discovery (mode A) or the preset table (mode B) ─────────────────────────────────────

// TokenAuth is how the client secret travels to the token endpoint.
type TokenAuth string

// The two token-endpoint authentication methods.
const (
	Basic TokenAuth = "basic"
	Post  TokenAuth = "post"
)

// Endpoints is everything the flow needs to talk to this provider.
type Endpoints struct {
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	EmailsURL    string
	// GroupsURL and GroupsKey travel together — the endpoint that lists this user's groups, and the
	// field of each item that names one. The key is the PRESET's (Config carries no claim for it),
	// so a self-hosted host with a typed groupsUrl is still read with the preset's field name.
	// Empty in mode A: a discovery document declares no such endpoint, which is why ParseConfig
	// refuses a group rule there.
	GroupsURL string
	GroupsKey string
	// Issuer and JwksURI, mode A only — what an ID token's `iss` must equal, and where its signing keys are.
	Issuer  string
	JwksURI string
	// TokenAuth is `client_secret_basic` when the provider advertises only that; `client_secret_post` otherwise.
	TokenAuth TokenAuth
}

type cached[T any] struct {
	value T
	at    int64
}

// DefaultCacheMs is an hour: long enough that discovery is not a per-login fetch, short enough
// that a moved endpoint heals without a restart.
const DefaultCacheMs = 60 * 60 * 1000

// JwksCooldownMs is how long after a fetch a REFETCH-for-an-unknown-kid is refused. Anyone can
// present a token with a junk kid, so without a floor every such request would be a fetch against
// the provider. The cost is that a key rotation heals within this window rather than instantly,
// which is the same trade every remote-JWKS implementation makes.
const JwksCooldownMs = 30_000

// Client is the outbound half: the fetch seam and the two caches the TypeScript kept as module
// globals. One per server. Owner of mu: the caches only — a fetch never runs under it.
type Client struct {
	// HTTP follows redirects (an IdP may well 302 its well-known document) and has a timeout; the
	// notify client is the one that must do neither.
	HTTP *http.Client

	mu        sync.Mutex
	cacheMs   int64
	discovery map[string]cached[Endpoints]
	jwks      map[string]cached[[]JWK]
}

// NewClient returns a Client over h (a 15 s-timeout client when nil).
func NewClient(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{HTTP: h, cacheMs: DefaultCacheMs, discovery: map[string]cached[Endpoints]{}, jwks: map[string]cached[[]JWK]{}}
}

// SetDiscoveryTTL is how long a discovery document and a JWKS are trusted before being refetched.
// `PSTACK_SSO_DISCOVERY_TTL_S` on `serve`; a test sets it to milliseconds so a provider that moves
// mid-run is observable. Non-positive restores the default.
func (c *Client) SetDiscoveryTTL(ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ms > 0 {
		c.cacheMs = ms
	} else {
		c.cacheMs = DefaultCacheMs
	}
}

// Forget is the test seam and the "re-validate on save" path — `SetSsoConfig` drops the entry so the
// next login re-reads. With no argument both caches are cleared; with URLs, those discovery entries.
func (c *Client) Forget(urls ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(urls) == 0 {
		c.discovery = map[string]cached[Endpoints]{}
		c.jwks = map[string]cached[[]JWK]{}
		return
	}
	for _, u := range urls {
		delete(c.discovery, u)
	}
}

func wellKnown(raw string) string {
	if strings.Contains(raw, "/.well-known/") {
		return raw
	}
	return strings.TrimRight(raw, "/") + "/.well-known/openid-configuration"
}

// getJSON is `fetch(url, { headers: { accept: 'application/json' } })`: the response, its body,
// and the parsed JSON (nil when the body is not JSON — `res.json().catch(() => null)`).
func (c *Client) getJSON(u string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return res, body, err
}

// ok is `res.ok`: 200–299.
func ok(res *http.Response) bool { return res.StatusCode >= 200 && res.StatusCode <= 299 }

// Discover fetches and validates an OIDC discovery document. Cached; `force` is the config-save validation.
func (c *Client) Discover(discoveryURL string, force bool) (Endpoints, error) {
	key := discoveryURL
	c.mu.Lock()
	hit, have := c.discovery[key]
	cacheMs := c.cacheMs
	c.mu.Unlock()
	if !force && have && time.Now().UnixMilli()-hit.at < cacheMs {
		return hit.value, nil
	}

	u := wellKnown(discoveryURL)
	res, body, err := c.getJSON(u, nil)
	if err != nil {
		return Endpoints{}, errorf("could not reach " + u + ": " + err.Error())
	}
	if !ok(res) {
		return Endpoints{}, errorf(u + " answered " + js.NumberString(float64(res.StatusCode)) + " — is the issuer right?")
	}
	parsed, perr := omap.Parse(body)
	if perr != nil || !truthy(parsed) {
		return Endpoints{}, errorf(u + " did not return JSON")
	}
	doc, _ := parsed.(*omap.Map)
	if doc == nil {
		doc = omap.New() // a JSON array or scalar: every lookup below misses, as `doc[k]` would
	}
	need := func(k string) (string, error) {
		v, _ := doc.Get(k)
		s, isStr := v.(string)
		if !isStr || s == "" {
			return "", errorf("the discovery document at " + u + " has no " + k)
		}
		return s, nil
	}
	var methods []string
	for _, m := range doc.GetSlice("token_endpoint_auth_methods_supported") {
		methods = append(methods, js.ToString(m))
	}
	out := Endpoints{UserInfoURL: doc.GetString("userinfo_endpoint"), TokenAuth: Post}
	if out.AuthorizeURL, err = need("authorization_endpoint"); err != nil {
		return Endpoints{}, err
	}
	if out.TokenURL, err = need("token_endpoint"); err != nil {
		return Endpoints{}, err
	}
	// The issuer an ID token must claim is the one the DOCUMENT declares, not the URL it came from.
	if out.Issuer, err = need("issuer"); err != nil {
		return Endpoints{}, err
	}
	if out.JwksURI, err = need("jwks_uri"); err != nil {
		return Endpoints{}, err
	}
	// Only when Basic is the sole option: post is what most providers want, and sending both would
	// be rejected by the strict ones.
	if len(methods) > 0 && !containsStr(methods, "client_secret_post") && containsStr(methods, "client_secret_basic") {
		out.TokenAuth = Basic
	}
	c.mu.Lock()
	c.discovery[key] = cached[Endpoints]{value: out, at: time.Now().UnixMilli()}
	c.mu.Unlock()
	return out, nil
}

// EndpointsFor is everything the flow needs to talk to this provider, from discovery (A) or the config (B).
func (c *Client) EndpointsFor(cfg *Config) (Endpoints, error) {
	if cfg.Mode == OIDC {
		return c.Discover(cfg.DiscoveryURL, false)
	}
	groupsKey := ""
	if p := PresetFor(cfg.Provider); p != nil {
		groupsKey = p.GroupsKey
	}
	return Endpoints{
		AuthorizeURL: cfg.AuthorizeURL,
		TokenURL:     cfg.TokenURL,
		UserInfoURL:  cfg.UserInfoURL,
		EmailsURL:    cfg.EmailsURL,
		GroupsURL:    cfg.GroupsURL,
		GroupsKey:    groupsKey,
		TokenAuth:    Post,
	}, nil
}

// ── the three HTTP steps ──────────────────────────────────────────────────────────────────────────

// AuthorizeArgs are the per-login values of the authorize URL.
type AuthorizeArgs struct {
	RedirectURI string
	State       string
	Challenge   string
}

// AuthorizeURL builds the URL the browser is sent to. Whatever the provider already put in the
// query (some tenant URLs carry one) is preserved, and the new parameters are appended in this
// order — `URLSearchParams.set` semantics, not a sorted encoding.
func AuthorizeURL(cfg *Config, endpoints Endpoints, args AuthorizeArgs) (string, error) {
	u, err := parseURL(endpoints.AuthorizeURL)
	if err != nil {
		return "", err
	}
	params := js.ParseQuery(u.RawQuery)
	set := func(k, v string) { params = setParam(params, k, v) }
	set("response_type", "code")
	set("client_id", cfg.ClientID)
	set("redirect_uri", args.RedirectURI)
	if cfg.Scopes != "" {
		set("scope", cfg.Scopes)
	}
	set("state", args.State)
	set("code_challenge", args.Challenge)
	set("code_challenge_method", "S256")
	u.RawQuery = formEncodePairs(params)
	u.ForceQuery = false
	return whatwgString(u), nil
}

// setParam is `URLSearchParams.set`: the first pair with that name takes the value and any later
// duplicates are dropped; a new name is appended.
func setParam(pairs []js.KV, k, v string) []js.KV {
	out := pairs[:0:0]
	done := false
	for _, p := range pairs {
		if p.K != k {
			out = append(out, p)
		} else if !done {
			out = append(out, js.KV{K: k, V: v})
			done = true
		}
	}
	if !done {
		out = append(out, js.KV{K: k, V: v})
	}
	return out
}

// formEncode is the application/x-www-form-urlencoded byte serializer URLSearchParams uses:
// `*-._` and alphanumerics as-is, space as `+`, everything else `%XX` (uppercase). Not
// url.QueryEscape (which keeps `~` and escapes `*`) and not EncodeURIComponent (`%20`, keeps `!'()`).
func formEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func formEncodePairs(pairs []js.KV) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(formEncode(p.K))
		b.WriteByte('=')
		b.WriteString(formEncode(p.V))
	}
	return b.String()
}

// TokenResponse is the token endpoint's body as parsed: JSON (any value shape the provider used)
// or the form-decoded pairs. `access_token` / `id_token` are read with GetString.
type TokenResponse = *omap.Map

// ExchangeArgs are the inputs of the code exchange.
type ExchangeArgs struct {
	Code         string
	RedirectURI  string
	Verifier     string
	ClientSecret string
}

// ExchangeCode swaps the code for a token.
//
// `Accept: application/json` is not decoration: without it GitHub answers
// `access_token=…&scope=…` in `application/x-www-form-urlencoded` and a JSON parse throws on a
// perfectly successful exchange.
func (c *Client) ExchangeCode(cfg *Config, endpoints Endpoints, args ExchangeArgs) (TokenResponse, error) {
	body := []js.KV{
		{K: "grant_type", V: "authorization_code"},
		{K: "code", V: args.Code},
		{K: "redirect_uri", V: args.RedirectURI},
		{K: "client_id", V: cfg.ClientID},
		{K: "code_verifier", V: args.Verifier},
	}
	headers := map[string]string{
		"content-type": "application/x-www-form-urlencoded",
		"accept":       "application/json",
	}
	if endpoints.TokenAuth == Basic {
		// RFC 6749 §2.3.1 wants both halves form-urlencoded before the base64; the TypeScript used
		// encodeURIComponent and the provider conformance suite checks for exactly those bytes.
		headers["authorization"] = "Basic " + js.B64([]byte(js.EncodeURIComponent(cfg.ClientID)+":"+js.EncodeURIComponent(args.ClientSecret)))
	} else {
		body = setParam(body, "client_secret", args.ClientSecret)
	}

	req, err := http.NewRequest("POST", endpoints.TokenURL, strings.NewReader(formEncodePairs(body)))
	if err != nil {
		return nil, errorf("the token endpoint was unreachable: " + err.Error())
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errorf("the token endpoint was unreachable: " + err.Error())
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errorf("the token endpoint was unreachable: " + err.Error())
	}
	text := string(raw)
	parsed := parseTokenBody(text)
	if !ok(res) {
		detail := DescribeError(parsed)
		if detail == "" {
			detail = js.Truncate(text, 200)
		}
		return nil, errorf("the token exchange failed (" + js.NumberString(float64(res.StatusCode)) + "): " + detail)
	}
	// A 200 carrying an `error` is GitHub's shape for a bad code — treating it as success hands the
	// rest of the flow an undefined access token and a much worse message.
	if oops := DescribeError(parsed); oops != "" {
		return nil, errorf("the token exchange failed: " + oops)
	}
	at, _ := parsed.Get("access_token")
	it, _ := parsed.Get("id_token")
	if !truthy(at) && !truthy(it) {
		return nil, errorf("the token endpoint returned neither an access token nor an id token")
	}
	return parsed, nil
}

// parseTokenBody is JSON, or the form-encoded body a provider sends when it ignores `Accept`.
func parseTokenBody(text string) *omap.Map {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		if v, err := omap.Parse([]byte(trimmed)); err == nil {
			if m, ok := v.(*omap.Map); ok {
				return m
			}
		}
		// fall through to form decoding
	}
	return FromQuery(trimmed)
}

// FromQuery is `Object.fromEntries(new URLSearchParams(raw))`: last value wins, first appearance orders.
func FromQuery(raw string) *omap.Map {
	order, vals := js.LastWins(js.ParseQuery(raw))
	m := omap.New()
	for _, k := range order {
		m.Set(k, vals[k])
	}
	return m
}

// DescribeError is the `error` / `error_description` pair, from a token body or from the callback's own query.
func DescribeError(o *omap.Map) string {
	code := o.GetString("error")
	if code == "" {
		return ""
	}
	if desc := o.GetString("error_description"); desc != "" {
		return code + ": " + desc
	}
	return code
}

// FetchJSON is an authenticated GET of a provider resource, parsed.
func (c *Client) FetchJSON(u, accessToken string) (any, error) {
	res, body, err := c.getJSON(u, map[string]string{
		"authorization": "Bearer " + accessToken,
		// GitHub requires a User-Agent and answers 403 without one.
		"user-agent": "pstack",
	})
	if err != nil {
		return nil, err
	}
	if !ok(res) {
		return nil, errorf(u + " answered " + js.NumberString(float64(res.StatusCode)))
	}
	return omap.Parse(body)
}

// ── ID token verification (mode A) ────────────────────────────────────────────────────────────────
//
// RS256 and ES256 only, over crypto/rsa and crypto/ecdsa — this package carries no crypto dependency.
//
// The allow-list is the point. `alg: none` and `alg: HS256` are the two classic JWT forgeries (the
// second signs with the PUBLIC key, which the attacker also has), and both are refused here before
// any key is looked at.

// JWK is a JWKS entry, as much of one as verification looks at. Alg and Kid are pointers because
// "absent" and "present but empty" are different things to the key-picking rule and to the message.
type JWK struct {
	Kty string  `json:"kty"`
	Kid string  `json:"kid"`
	Alg *string `json:"alg"`
	Use string  `json:"use"`
	N   string  `json:"n"`
	E   string  `json:"e"`
	Crv string  `json:"crv"`
	X   string  `json:"x"`
	Y   string  `json:"y"`
}

// JWTHeader is the JOSE header, as much as is read.
type JWTHeader struct {
	Alg string
	Kid *string
	Typ string
}

// JWT is a decoded-but-unverified token.
type JWT struct {
	Header JWTHeader
	Claims *omap.Map
	// Signed is `header.payload`, the bytes the signature covers.
	Signed    string
	Signature []byte
}

// decodeSegment is `JSON.parse(Buffer.from(seg, 'base64url'))`. A non-object JSON value decodes
// to an empty map — every lookup on it misses, as it would on a string or a number in JS.
func decodeSegment(seg string) (*omap.Map, error) {
	raw, err := js.B64URLDecode(seg)
	if err != nil {
		return nil, errorf("the id token is not a readable JWT")
	}
	v, err := omap.Parse(raw)
	if err != nil {
		return nil, errorf("the id token is not a readable JWT")
	}
	m, _ := v.(*omap.Map)
	if m == nil {
		m = omap.New()
	}
	return m, nil
}

// DecodeJWT is header + claims WITHOUT verifying anything. Only for deciding which key to fetch.
func DecodeJWT(token string) (*JWT, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errorf("the id token is not a JWT (expected three dot-separated parts)")
	}
	// Buffer.from(…, 'base64url') ignores what it cannot decode; a signature that will not decode
	// is one that will not verify, which is the same outcome one step later.
	sig, _ := js.B64URLDecode(parts[2])
	h, err := decodeSegment(parts[0])
	if err != nil {
		return nil, err
	}
	claims, err := decodeSegment(parts[1])
	if err != nil {
		return nil, err
	}
	out := &JWT{Claims: claims, Signed: parts[0] + "." + parts[1], Signature: sig}
	out.Header.Alg = h.GetString("alg")
	out.Header.Typ = h.GetString("typ")
	if kid, ok := h.Get("kid"); ok {
		s := js.ToString(kid)
		out.Header.Kid = &s
	}
	return out, nil
}

func (c *Client) fetchJwks(uri string, force bool, cooldownMs int64) ([]JWK, error) {
	c.mu.Lock()
	hit, have := c.jwks[uri]
	cacheMs := c.cacheMs
	c.mu.Unlock()
	if have {
		age := time.Now().UnixMilli() - hit.at
		if age < cacheMs && (!force || age < cooldownMs) {
			return hit.value, nil
		}
	}
	res, body, err := c.getJSON(uri, nil)
	if err != nil {
		return nil, err
	}
	if !ok(res) {
		return nil, errorf("the JWKS endpoint " + uri + " answered " + js.NumberString(float64(res.StatusCode)))
	}
	// `Array.isArray(doc?.keys) ? doc.keys : []`, with each entry decoded on its own so one junk
	// entry does not hide the good keys next to it (it would just fail to import, as in JS).
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	keys := []JWK{}
	if json.Unmarshal(body, &doc) == nil {
		for _, raw := range doc.Keys {
			var k JWK
			_ = json.Unmarshal(raw, &k)
			keys = append(keys, k)
		}
	}
	c.mu.Lock()
	c.jwks[uri] = cached[[]JWK]{value: keys, at: time.Now().UnixMilli()}
	c.mu.Unlock()
	return keys, nil
}

// VerifyArgs are what an ID token is checked against.
type VerifyArgs struct {
	Issuer   string
	ClientID string
	JwksURI  string
	// Now is epoch milliseconds; 0 means the wall clock.
	Now int64
	// JwksCooldownMs overrides JwksCooldownMs when non-nil (a test passes 0 to make rotation
	// observable; 0 must not mean "default").
	JwksCooldownMs *int64
}

// VerifyIDToken validates an ID token: signature against the JWKS, then `iss`, `aud` and `exp`.
// Returns the claims. Every failure is an *Error — there is no "probably fine" path.
func (c *Client) VerifyIDToken(token string, args VerifyArgs) (*omap.Map, error) {
	jwt, err := DecodeJWT(token)
	if err != nil {
		return nil, err
	}
	alg := jwt.Header.Alg
	if alg != "RS256" && alg != "ES256" {
		shown := alg
		if shown == "" {
			shown = "none"
		}
		return nil, errorf(`id token algorithm "` + shown + `" is not accepted — only RS256 and ES256 are`)
	}

	kid := ""
	if jwt.Header.Kid != nil {
		kid = *jwt.Header.Kid
	}
	pick := func(keys []JWK) []JWK {
		usable := []JWK{}
		for _, k := range keys {
			if k.Alg == nil || *k.Alg == alg {
				usable = append(usable, k)
			}
		}
		if kid == "" {
			return usable
		}
		byKid := []JWK{}
		for _, k := range usable {
			if k.Kid == kid {
				byKid = append(byKid, k)
			}
		}
		return byKid
	}

	cooldownMs := int64(JwksCooldownMs)
	if args.JwksCooldownMs != nil {
		cooldownMs = *args.JwksCooldownMs
	}
	keys, err := c.fetchJwks(args.JwksURI, false, cooldownMs)
	if err != nil {
		return nil, err
	}
	candidates := pick(keys)
	// Unknown kid ⇒ the provider probably rotated. Refetch once, no sooner than the cooldown.
	if len(candidates) == 0 {
		if keys, err = c.fetchJwks(args.JwksURI, true, cooldownMs); err != nil {
			return nil, err
		}
		candidates = pick(keys)
	}
	if len(candidates) == 0 {
		shown := "(none)"
		if jwt.Header.Kid != nil {
			shown = *jwt.Header.Kid
		}
		return nil, errorf(`no signing key matching kid "` + shown + `" in ` + args.JwksURI)
	}

	digest := sha256.Sum256([]byte(jwt.Signed))
	verified := false
	for _, jwk := range candidates {
		// A key that will not import is not a key that verifies — try the next one.
		if verifyWith(alg, jwk, digest[:], jwt.Signature) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, errorf("the id token signature did not verify against the provider JWKS")
	}

	claims := jwt.Claims
	if iss, _ := claims.Get("iss"); iss != args.Issuer {
		return nil, errorf(`the id token issuer is "` + jsString(claims, "iss") + `", expected "` + args.Issuer + `"`)
	}
	audiences := []string{}
	switch aud, _ := claims.Get("aud"); x := aud.(type) {
	case []any:
		for _, a := range x {
			audiences = append(audiences, js.ToString(a))
		}
	case string:
		audiences = append(audiences, x)
	}
	if !containsStr(audiences, args.ClientID) {
		return nil, errorf("the id token audience does not include this client id")
	}
	nowMs := args.Now
	if nowMs == 0 {
		nowMs = time.Now().UnixMilli()
	}
	now := float64(nowMs / 1000)
	exp, _ := numberOf(claims, "exp")
	// 60s leeway, for clock skew between here and the provider. Not more: an expired token is the
	// one thing `exp` exists to stop.
	if exp == 0 || exp+60 < now {
		return nil, errorf("the id token has expired")
	}
	nbf, _ := numberOf(claims, "nbf")
	if nbf != 0 && nbf-60 > now {
		return nil, errorf("the id token is not valid yet")
	}
	return claims, nil
}

// verifyWith imports jwk for alg and checks the signature. ES256 JWS signatures are raw r||s, split
// at 32 — no DER. Anything that will not import returns false.
func verifyWith(alg string, jwk JWK, digest, sig []byte) bool {
	switch alg {
	case "RS256":
		if jwk.Kty != "RSA" {
			return false
		}
		n, err1 := js.B64URLDecode(jwk.N)
		e, err2 := js.B64URLDecode(jwk.E)
		if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 {
			return false
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil
	case "ES256":
		if jwk.Kty != "EC" || jwk.Crv != "P-256" || len(sig) != 64 {
			return false
		}
		x, err1 := js.B64URLDecode(jwk.X)
		y, err2 := js.B64URLDecode(jwk.Y)
		if err1 != nil || err2 != nil {
			return false
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		return ecdsa.Verify(pub, digest, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	}
	return false
}

// numberOf is `typeof claims[k] === 'number' ? claims[k] : 0`.
func numberOf(m *omap.Map, k string) (float64, bool) {
	switch x, _ := m.Get(k); v := x.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

// jsString is `String(claims[k])`: "undefined" when absent, "null" for null, scalars as JS prints
// them, and a composite as its JSON (close enough to `[object Object]` for an error message).
func jsString(m *omap.Map, k string) string {
	v, present := m.Get(k)
	switch x := v.(type) {
	case nil:
		if !present {
			return "undefined"
		}
		return "null"
	case string, bool, int64, float64:
		return js.ToString(x)
	}
	return string(jsonx.Must(v))
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
