// Package sso is sign-in with the operator's own identity provider.
//
// ── WHAT THIS IS, AND WHAT IT DELIBERATELY IS NOT ────────────────────────────────────────────────
//
// The operator registers ONE OAuth/OIDC app in their own org (Google Workspace, Okta, GitHub, …) and
// pastes the client id and secret in. We are the relying party and nothing more: their directory
// stays theirs, and anyone who can authenticate against it can sign in here without an account being
// created by hand first.
//
// It is four steps — authorize URL with a PKCE challenge, callback, token exchange, fetch the user —
// and this package is all of them. There is no provider registry with lifecycle hooks, no session
// framework, no plugin system: a new preset is a row in `Presets`, and the identity that comes out
// the far end is handed to `auth.SsoSignIn`, which mints the SAME session a password login mints.
// Nothing downstream of the cookie knows SSO exists.
//
// ── THE PARTS THAT ARE SECURITY, NOT PLUMBING ────────────────────────────────────────────────────
//
//   - PKCE, always. Even with a confidential client and a secret: it is what makes an intercepted
//     `code` useless, and it costs one hash.
//   - No `nonce`. PKCE plus a single-use `state` covers the replay this feature is exposed to, and
//     a nonce that is SENT but never CHECKED is worse than none — it reads as a defence in review and
//     is not one. If ID tokens ever arrive anywhere but straight from the token endpoint, add it and
//     verify it in the same change.
//   - `alg` is an allow-list (`RS256`/`ES256`), read from the header and matched against the JWKS
//     key. `none` and every HMAC alg are refused by construction: an attacker who may choose the
//     algorithm can sign with the public key.
//   - `aud` may be a string or an array. It must CONTAIN the client id. `iss` must equal the
//     issuer the discovery document declares — not the URL it was fetched from.
//   - Identity is `(providerKey, subject)`, never email. Emails move between people; subjects do
//     not. `MapClaims` carries `EmailVerified` precisely so the one place that links an SSO login to a
//     PRE-EXISTING local account (auth) can refuse an unverified one — that branch is the only
//     account-takeover surface in the feature.
//
// ── ON GITHUB AND OIDC ───────────────────────────────────────────────────────────────────────────
//
// GitHub publishes an OIDC discovery document, and it is a trap: it signs GitHub ACTIONS job tokens
// for cloud workload identity. It is not a user login endpoint. GitHub user login is `mode: 'oauth2'`
// and always will be.
//
// ── THE PORT ─────────────────────────────────────────────────────────────────────────────────────
//
// The module-global caches and the `fetchImpl` parameters became a Client: its http.Client is the
// fetch seam (it FOLLOWS redirects — the notify client is the one that must not — and has a 15 s
// timeout), and the discovery and JWKS caches hang off it under a mutex. WHATWG URL serialisation
// (`new URL(raw).toString()`) is explicit where the TypeScript relied on it; the form encoding of
// URLSearchParams is hand-rolled because Go's url.Values sorts keys and escapes a different set.
package sso

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// Error is what the flow throws — something a person can act on, never a bare error.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is an *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func errorf(msg string) error { return &Error{Msg: msg} }

// Mode is how the provider is talked to.
type Mode string

// The modes.
const (
	OIDC   Mode = "oidc"
	OAuth2 Mode = "oauth2"
)

// ClaimMap says which key of a userinfo/claims object holds each field we care about. Flat lookups —
// not JSONPath (one dotted path is tolerated, see lookup).
type ClaimMap struct {
	Subject  string `json:"subject"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar"`
}

// OIDCClaims are the standard OIDC claim names, and the default whenever a provider does not override them.
var OIDCClaims = ClaimMap{Subject: "sub", Username: "preferred_username", Email: "email", Name: "name", Avatar: "picture"}

// Preset is one row of the provider table.
type Preset struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	UserInfoURL  string `json:"userInfoUrl"`
	// EmailsURL is where to look when the userinfo response carries no email — providers that let a
	// user keep it private serve it from a second endpoint. Optional, and the ONLY per-provider
	// special case in this package; adding one stays a table edit.
	EmailsURL string   `json:"emailsUrl,omitempty"`
	Scopes    string   `json:"scopes"`
	ClaimMap  ClaimMap `json:"claimMap"`
}

// Presets is the preset table. Adding a provider is one entry here and nothing else — no code
// change, no new branch. `custom` is not in the table: it is the absence of a preset (the operator
// supplies the three URLs themselves).
var Presets = []Preset{
	{
		Key:          "github",
		Label:        "GitHub",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		UserInfoURL:  "https://api.github.com/user",
		// `GET /user` returns `email: null` for anyone whose profile email is private, which is the
		// default. Without this, `allowedEmailDomains` would reject the entire org.
		EmailsURL: "https://api.github.com/user/emails",
		Scopes:    "read:user user:email",
		ClaimMap:  ClaimMap{Subject: "id", Username: "login", Email: "email", Name: "name", Avatar: "avatar_url"},
	},
	{
		Key:          "gitlab",
		Label:        "GitLab",
		AuthorizeURL: "https://gitlab.com/oauth/authorize",
		TokenURL:     "https://gitlab.com/oauth/token",
		UserInfoURL:  "https://gitlab.com/api/v4/user",
		Scopes:       "read_user",
		ClaimMap:     ClaimMap{Subject: "id", Username: "username", Email: "email", Name: "name", Avatar: "avatar_url"},
	},
	{
		Key:          "bitbucket",
		Label:        "Bitbucket",
		AuthorizeURL: "https://bitbucket.org/site/oauth2/authorize",
		TokenURL:     "https://bitbucket.org/site/oauth2/access_token",
		UserInfoURL:  "https://api.bitbucket.org/2.0/user",
		// Bitbucket's /2.0/user carries no email at all — it is always this second call.
		EmailsURL: "https://api.bitbucket.org/2.0/user/emails",
		Scopes:    "account email",
		ClaimMap:  ClaimMap{Subject: "uuid", Username: "username", Email: "email", Name: "display_name", Avatar: "links.avatar.href"},
	},
}

// PresetFor returns the preset with that key, or nil.
func PresetFor(key string) *Preset {
	for i := range Presets {
		if Presets[i].Key == key {
			return &Presets[i]
		}
	}
	return nil
}

// ── configuration ─────────────────────────────────────────────────────────────────────────────────

// Config is the stored shape. Field order is the order `JSON.stringify` wrote it (the base fields
// first, then the mode-specific ones) — what sso_config.config holds and what the API returns.
type Config struct {
	Mode Mode `json:"mode"`
	// Enabled off keeps the row (and the secret) but hides the button and refuses `/start`.
	Enabled  bool   `json:"enabled"`
	ClientID string `json:"clientId"`
	// AllowedEmailDomains non-empty ⇒ a login whose email is outside the list is refused — including one with NO email.
	AllowedEmailDomains []string `json:"allowedEmailDomains"`
	// DefaultRole is the role for auto-provisioned users.
	DefaultRole string `json:"defaultRole"`
	// Label is the button text and the `providerKey` half of a link's identity.
	Label string `json:"label"`
	// DiscoveryURL, mode A: an issuer (`https://accounts.google.com`) or a full `.well-known` URL.
	DiscoveryURL string `json:"discoveryUrl"`
	// Provider, mode B: a `Presets` key, or `custom`.
	Provider     string `json:"provider"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	UserInfoURL  string `json:"userInfoUrl"`
	// EmailsURL is consulted ONLY when the userinfo response carries no email. Preset-filled (GitHub
	// and Bitbucket both keep the address off the profile); an operator only types it for a
	// self-hosted provider.
	EmailsURL string   `json:"emailsUrl"`
	Scopes    string   `json:"scopes"`
	ClaimMap  ClaimMap `json:"claimMap"`
}

// str is `typeof v === 'string' ? v.trim() : ""`.
func str(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// truthy is `!!v` over the document value universe.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// parseURL is `new URL(raw)`: absolute URLs only.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return nil, errors.New("Invalid URL")
	}
	return u, nil
}

// whatwgHostname is `u.hostname`: lowercase, and an IPv6 literal keeps its brackets.
func whatwgHostname(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

// whatwgString is `u.toString()`: lowercase scheme and host, the default port dropped, an empty
// path serialised as `/`. (Re-percent-encoding of the path is not reproduced; the URLs that reach
// this are provider endpoints an operator pasted, and the discovery fetch strips the trailing slash
// itself, so the residue is display and cache-key only.)
func whatwgString(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if (scheme == "https" && strings.HasSuffix(host, ":443")) || (scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	if u.User != nil {
		b.WriteString(u.User.String())
		b.WriteByte('@')
	}
	b.WriteString(host)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	b.WriteString(path)
	if u.ForceQuery || u.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(u.RawQuery)
	}
	if u.Fragment != "" {
		b.WriteByte('#')
		b.WriteString(u.EscapedFragment())
	}
	return b.String()
}

func httpsURL(raw, what string) (string, error) {
	u, err := parseURL(raw)
	if err != nil {
		return "", errorf(what + ` must be an absolute URL — got "` + raw + `"`)
	}
	// http is allowed on loopback only: a real IdP is https, and a plaintext token exchange to
	// anywhere else puts the client secret on the wire.
	h := whatwgHostname(u)
	local := h == "localhost" || h == "127.0.0.1" || h == "[::1]"
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && local) {
		return "", errorf(what + ` must be https (http is only accepted on localhost) — got "` + raw + `"`)
	}
	return whatwgString(u), nil
}

// ParseConfig validates what an operator submitted (a parsed JSON value: *omap.Map or anything) into
// the stored shape. Errors are *Error with something a person can act on — this is a
// paste-four-fields form, and every one of them can be pasted wrong.
func ParseConfig(input any) (*Config, error) {
	o, _ := input.(*omap.Map)
	if o == nil {
		o = omap.New()
	}
	get := func(k string) any { v, _ := o.Get(k); return v }
	mode := str(get("mode"))
	if mode == "" {
		mode = "oidc"
	}
	if mode != "oidc" && mode != "oauth2" {
		return nil, errorf("mode must be 'oidc' or 'oauth2'")
	}
	clientID := str(get("clientId"))
	if clientID == "" {
		return nil, errorf("clientId is required")
	}
	domains := []string{}
	if list, ok := get("allowedEmailDomains").([]any); ok {
		for _, d := range list {
			s := strings.TrimPrefix(strings.ToLower(str(d)), "@")
			if s != "" {
				domains = append(domains, s)
			}
		}
	}
	cfg := &Config{
		Mode:                Mode(mode),
		Enabled:             true,
		ClientID:            clientID,
		AllowedEmailDomains: domains,
		// Only 'admin' exists today (store migration 1 defaults the column to it and there is no
		// role UI). The field is here so granting less than admin is a data change when roles land.
		DefaultRole: str(get("defaultRole")),
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "admin"
	}
	if v, ok := o.Get("enabled"); ok {
		cfg.Enabled = truthy(v)
	}

	if mode == "oidc" {
		discoveryURL := str(get("discoveryUrl"))
		if discoveryURL == "" {
			discoveryURL = str(get("issuer"))
		}
		if discoveryURL == "" {
			return nil, errorf("issuer or discoveryUrl is required for OIDC")
		}
		// The TypeScript computed the label (`new URL(discoveryUrl).hostname`) before validating the
		// URL, so an unparsable issuer with no label surfaced as a TypeError; here it is the *Error
		// httpsURL produces either way.
		normalized, err := httpsURL(discoveryURL, "issuer/discoveryUrl")
		if err != nil {
			return nil, err
		}
		cfg.Label = str(get("label"))
		if cfg.Label == "" {
			u, _ := parseURL(discoveryURL)
			cfg.Label = whatwgHostname(u)
		}
		cfg.DiscoveryURL = normalized
		cfg.Scopes = str(get("scopes"))
		if cfg.Scopes == "" {
			cfg.Scopes = "openid profile email"
		}
		cfg.ClaimMap = mergeClaims(OIDCClaims, pickClaims(get("claimMap")))
		return cfg, nil
	}

	provider := str(get("provider"))
	if provider == "" {
		provider = "custom"
	}
	preset := PresetFor(provider)
	if preset == nil && provider != "custom" {
		keys := make([]string, len(Presets))
		for i, p := range Presets {
			keys[i] = p.Key
		}
		return nil, errorf(`unknown provider "` + provider + `" — use one of ` + strings.Join(keys, ", ") + ", or custom")
	}
	// A preset fills the endpoints; anything the operator typed still wins, so a self-hosted GitLab
	// is the gitlab preset with three URLs replaced rather than `custom` with five fields.
	presetOr := func(typed string, fromPreset func(*Preset) string) string {
		if typed != "" {
			return typed
		}
		if preset != nil {
			return fromPreset(preset)
		}
		return ""
	}
	authorizeURL := presetOr(str(get("authorizeUrl")), func(p *Preset) string { return p.AuthorizeURL })
	tokenURL := presetOr(str(get("tokenUrl")), func(p *Preset) string { return p.TokenURL })
	if authorizeURL == "" || tokenURL == "" {
		return nil, errorf("authorizeUrl and tokenUrl are required for a custom OAuth 2.0 provider")
	}
	userInfoURL := presetOr(str(get("userInfoUrl")), func(p *Preset) string { return p.UserInfoURL })
	// The preset's emails endpoint is inherited ONLY while the userinfo endpoint is still the
	// preset's too. A self-hosted GitLab is not gitlab.com, and sending its access token there would
	// hand a third party a live credential.
	emailsURL := str(get("emailsUrl"))
	if emailsURL == "" && preset != nil && userInfoURL == preset.UserInfoURL {
		emailsURL = preset.EmailsURL
	}
	var err error
	cfg.Label = str(get("label"))
	if cfg.Label == "" {
		cfg.Label = presetOr("", func(p *Preset) string { return p.Label })
		if cfg.Label == "" {
			cfg.Label = "SSO"
		}
	}
	cfg.Provider = provider
	if cfg.AuthorizeURL, err = httpsURL(authorizeURL, "authorizeUrl"); err != nil {
		return nil, err
	}
	if cfg.TokenURL, err = httpsURL(tokenURL, "tokenUrl"); err != nil {
		return nil, err
	}
	if userInfoURL != "" {
		if cfg.UserInfoURL, err = httpsURL(userInfoURL, "userInfoUrl"); err != nil {
			return nil, err
		}
	}
	if emailsURL != "" {
		if cfg.EmailsURL, err = httpsURL(emailsURL, "emailsUrl"); err != nil {
			return nil, err
		}
	}
	cfg.Scopes = presetOr(str(get("scopes")), func(p *Preset) string { return p.Scopes })
	cfg.ClaimMap = OIDCClaims
	if preset != nil {
		cfg.ClaimMap = mergeClaims(cfg.ClaimMap, preset.ClaimMap)
	}
	cfg.ClaimMap = mergeClaims(cfg.ClaimMap, pickClaims(get("claimMap")))
	return cfg, nil
}

// pickClaims keeps the non-empty string entries of a submitted claim map.
func pickClaims(v any) ClaimMap {
	o, _ := v.(*omap.Map)
	return ClaimMap{Subject: str(o.GetString("subject")), Username: str(o.GetString("username")), Email: str(o.GetString("email")), Name: str(o.GetString("name")), Avatar: str(o.GetString("avatar"))}
}

// mergeClaims is `{ ...base, ...over }` where over only carries the keys it set.
func mergeClaims(base, over ClaimMap) ClaimMap {
	pick := func(a, b string) string {
		if b != "" {
			return b
		}
		return a
	}
	return ClaimMap{
		Subject:  pick(base.Subject, over.Subject),
		Username: pick(base.Username, over.Username),
		Email:    pick(base.Email, over.Email),
		Name:     pick(base.Name, over.Name),
		Avatar:   pick(base.Avatar, over.Avatar),
	}
}

// ── PKCE and state ────────────────────────────────────────────────────────────────────────────────

// RandomB64URL is n random bytes, base64url.
func RandomB64URL(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return js.B64URL(b)
}

// CodeChallenge is RFC 7636 S256: `code_challenge = base64url(sha256(ascii(code_verifier)))`.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return js.B64URL(sum[:])
}

// PKCE returns a verifier (43–128 chars of the unreserved set — 32 random bytes base64url is 43)
// and its challenge.
func PKCE() (verifier, challenge string) {
	verifier = RandomB64URL(32)
	return verifier, CodeChallenge(verifier)
}

// SafeNext is where to send the browser back after a successful login.
//
// A SAME-ORIGIN PATH ONLY. This value arrives in a query parameter on a route that then issues a
// redirect, which is the textbook open-redirect shape: `//evil.example` and `https://evil.example`
// are both absolute to a browser. The UI only ever sends `to.fullPath`, so nothing is lost. An
// absent parameter is the empty string here (null in the TypeScript) and lands on `/` the same way.
func SafeNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, `\`) {
		return "/"
	}
	return raw
}

// ── identity ──────────────────────────────────────────────────────────────────────────────────────

// Identity is what comes out of a provider, in local terms.
type Identity struct {
	Subject  string `json:"subject"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar"`
	// EmailVerified is false only when the provider says so explicitly. nil means it never said —
	// which is NOT permission to link an existing local account (auth requires true).
	EmailVerified *bool `json:"emailVerified"`
}

// lookup is `payload[key]`, or `links.avatar.href` — one dotted path, because two providers nest
// the avatar and nothing else. The second result is `!== undefined`.
func lookup(payload *omap.Map, key string) (any, bool) {
	if !strings.Contains(key, ".") {
		return payload.Get(key)
	}
	var cur any = payload
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(*omap.Map)
		if !ok || m == nil {
			return nil, false
		}
		cur, ok = m.Get(part)
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// asText is a trimmed string, a number as String(number), or the empty string.
func asText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case int64, float64:
		return js.ToString(x)
	}
	return ""
}

// MapClaims turns a userinfo/claims object into an Identity.
func MapClaims(payload any, cm ClaimMap) (*Identity, error) {
	o, _ := payload.(*omap.Map)
	if o == nil {
		o = omap.New()
	}
	text := func(key string) string { v, _ := lookup(o, key); return asText(v) }
	subject := text(cm.Subject)
	if subject == "" {
		return nil, errorf(`the provider's response has no "` + cm.Subject + `" — check the claim mapping`)
	}
	id := &Identity{
		Subject:  subject,
		Username: text(cm.Username),
		Email:    strings.ToLower(text(cm.Email)),
		Name:     text(cm.Name),
		Avatar:   text(cm.Avatar),
	}
	switch v, _ := o.Get("email_verified"); x := v.(type) {
	case bool:
		id.EmailVerified = &x
	case string:
		if x == "true" || x == "false" {
			b := x == "true"
			id.EmailVerified = &b
		}
	}
	return id, nil
}

// PrimaryEmail is one row of a provider's separate emails endpoint.
type PrimaryEmail struct {
	Email    string
	Verified bool
}

// PrimaryEmailOf finds the primary verified address from a provider's separate emails endpoint.
// Tolerates both shapes in the wild: a bare array (GitHub) and `{ values: [...] }` (Bitbucket), with
// either key spelling. nil when there is none.
func PrimaryEmailOf(payload any) *PrimaryEmail {
	var list []any
	switch x := payload.(type) {
	case []any:
		list = x
	case *omap.Map:
		list = x.GetSlice("values")
	}
	rows := []*omap.Map{}
	for _, r := range list {
		if m, ok := r.(*omap.Map); ok && m != nil {
			rows = append(rows, m)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	primary := rows[0]
	for _, r := range rows {
		if isTrue(r, "primary") || isTrue(r, "is_primary") {
			primary = r
			break
		}
	}
	email := strings.ToLower(asText(primary.GetString("email")))
	if email == "" {
		return nil
	}
	return &PrimaryEmail{Email: email, Verified: isTrue(primary, "verified") || isTrue(primary, "is_confirmed")}
}

func isTrue(m *omap.Map, k string) bool {
	v, _ := m.Get(k)
	b, ok := v.(bool)
	return ok && b
}

var (
	notUsernameChars = regexp.MustCompile(`[^a-z0-9._-]+`)
	leadingJunk      = regexp.MustCompile(`^[^a-z0-9]+`)
	trailingDashes   = regexp.MustCompile(`-+$`)
	usernameShape    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)
	notAlnum         = regexp.MustCompile(`(?i)[^a-z0-9]`)
)

// SanitizeUsername makes a username the local schema accepts. Cosmetic — identity is `(providerKey, subject)`.
func SanitizeUsername(raw, fallback string) string {
	base := strings.ToLower(raw)
	base = notUsernameChars.ReplaceAllString(base, "-")
	base = leadingJunk.ReplaceAllString(base, "")
	base = trailingDashes.ReplaceAllString(base, "")
	base = js.Slice(base, 0, 32)
	if usernameShape.MatchString(base) {
		return base
	}
	tail := js.Slice(strings.ToLower(notAlnum.ReplaceAllString(fallback, "")), 0, 20)
	if tail == "" {
		tail = "sso"
	}
	return js.Slice("user-"+tail, 0, 32)
}

// EmailAllowed fails CLOSED: an empty list allows everything, a non-empty one requires an email
// that matches it.
func EmailAllowed(email string, domains []string) bool {
	if len(domains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range domains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}
