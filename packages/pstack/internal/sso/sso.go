// Package sso is sign-in with the operator's own identity provider.
//
// ── WHAT THIS IS, AND WHAT IT DELIBERATELY IS NOT ────────────────────────────────────────────────
//
// The operator registers an OAuth/OIDC app in their own org (Google Workspace, Okta, GitHub, …) and
// pastes the client id and secret in — several of them, since multi-provider landed; each is a row
// in auth's sso_providers. We are the relying party and nothing more: their directory stays theirs,
// and anyone who can authenticate against it can sign in here without an account being created by
// hand first.
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
	"path"
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
	Key   string `json:"key"`
	Label string `json:"label"`
	// Mode says how this provider is talked to. An oidc preset carries a DiscoveryURL and no
	// endpoint URLs (discovery serves those); an oauth2 preset is the inverse. ParseConfig defaults
	// the config's mode from it when the preset is named, and refuses a config that contradicts it.
	Mode Mode `json:"mode"`
	// ButtonLabel is the login-page button text ("Continue with GitHub").
	ButtonLabel string `json:"buttonLabel"`
	// SetupURL is where the operator registers the OAuth app/client on the provider's side, and
	// SetupHint is the two-or-three-sentence walkthrough the config form shows beside it: where to
	// create the app, that this host's callback URL must be pasted in as the redirect URI, and any
	// scope notes. Both are display-only — nothing in the flow reads them.
	SetupURL  string `json:"setupUrl"`
	SetupHint string `json:"setupHint"`
	// DiscoveryURL is the issuer, for an oidc preset. One containing "<" is a TEMPLATE — the
	// provider has no single issuer (each tenant/domain/realm gets its own) — and the operator must
	// replace the placeholder: ParseConfig refuses it verbatim, and the UI renders it as a field to
	// fill in rather than a value to accept.
	DiscoveryURL string `json:"discoveryUrl,omitempty"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	UserInfoURL  string `json:"userInfoUrl"`
	// EmailsURL is where to look when the userinfo response carries no email — providers that let a
	// user keep it private serve it from a second endpoint. Optional, and the ONLY per-provider
	// special case in this package; adding one stays a table edit.
	EmailsURL string `json:"emailsUrl,omitempty"`
	// GroupsURL is where the provider lists the groups/orgs this user belongs to, GroupsKey is the
	// field of each returned item that NAMES one, and GroupsScope is the OAuth scope that endpoint
	// needs. All three or none: without a key there is nothing to read the response with, which is
	// why `requiredGroups` is refused for a provider that has no preset (ParseConfig).
	GroupsURL string `json:"groupsUrl,omitempty"`
	GroupsKey string `json:"groupsKey,omitempty"`
	// GroupsScopes is every scope SUFFICIENT to read GroupsURL, not one required scope — GitHub
	// documents `user` as granting the same read as `read:org`, and refusing a config that names it
	// would contradict the provider. The FIRST entry is the one the refusal message recommends.
	GroupsScopes []string `json:"groupsScopes,omitempty"`
	Scopes       string   `json:"scopes"`
	ClaimMap     ClaimMap `json:"claimMap"`
}

// Presets is the preset table. Adding a provider is one entry here and nothing else — no code
// change, no new branch. `custom` is not in the table: it is the absence of a preset (the operator
// supplies the three URLs themselves).
var Presets = []Preset{
	{
		Key:         "github",
		Label:       "GitHub",
		Mode:        OAuth2,
		ButtonLabel: "Continue with GitHub",
		SetupURL:    "https://github.com/settings/developers",
		SetupHint: "Create an OAuth App under Developer settings (an org-owned app works the same way). " +
			"Paste this host's callback URL in as the Authorization callback URL. " +
			"The default scopes cover sign-in; add read:org to use a group (organization) rule.",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		UserInfoURL:  "https://api.github.com/user",
		// `GET /user` returns `email: null` for anyone whose profile email is private, which is the
		// default. Without this, `allowedEmailDomains` would reject the entire org.
		EmailsURL: "https://api.github.com/user/emails",
		// `GET /user/orgs` — "List organizations for the authenticated user", each item carrying
		// `login`. THE SCOPE IS NOT OPTIONAL, which is the whole reason ParseConfig refuses a group
		// rule without it. Two sources, both read:
		//   - https://docs.github.com/en/rest/orgs/orgs (current): this endpoint "requires at least
		//     user or read:org scope for OAuth app tokens and personal access tokens (classic)".
		//   - https://developer.github.com/changes/22/ (the changelog that announced that rule):
		//     "If you have `user`, `read:org`, `write:org`, or `admin:org` scope, the response also
		//     includes your private organization memberships."
		// So without the scope a member of a PRIVATE org is either refused outright or simply comes
		// back invisible — a legitimate member locked out, with no diagnostic anywhere.
		GroupsURL: "https://api.github.com/user/orgs",
		GroupsKey: "login",
		// Any of these grants the read; `read:org` is the least of them, so it leads.
		GroupsScopes: []string{"read:org", "user", "write:org", "admin:org"},
		Scopes:       "read:user user:email",
		ClaimMap:     ClaimMap{Subject: "id", Username: "login", Email: "email", Name: "name", Avatar: "avatar_url"},
	},
	{
		Key:         "gitlab",
		Label:       "GitLab",
		Mode:        OAuth2,
		ButtonLabel: "Continue with GitLab",
		SetupURL:    "https://gitlab.com/-/user_settings/applications",
		SetupHint: "Add an application under your user (or group) settings on GitLab. " +
			"Paste this host's callback URL in as the Redirect URI. " +
			"The read_user scope covers sign-in; add read_api to use a group rule, and for a self-hosted GitLab replace the three endpoint URLs with your own host's.",
		AuthorizeURL: "https://gitlab.com/oauth/authorize",
		TokenURL:     "https://gitlab.com/oauth/token",
		UserInfoURL:  "https://gitlab.com/api/v4/user",
		// `GET /groups` — as documented at https://docs.gitlab.com/api/groups/, an AUTHENTICATED
		// request returns "only the groups you're a member of and does not include public groups",
		// and each item carries `full_path` ("acme/backend"), which is the name an operator writes.
		// `read_user` is not enough: it is the `/user` endpoint only, while `read_api` "Grants read
		// access to the API, including all groups and projects" — the scopes are as listed at
		// https://docs.gitlab.com/integration/oauth_provider/.
		GroupsURL: "https://gitlab.com/api/v4/groups",
		GroupsKey: "full_path",
		// `api` is a superset of `read_api` — GitLab grants full API access with it.
		GroupsScopes: []string{"read_api", "api"},
		Scopes:       "read_user",
		ClaimMap:     ClaimMap{Subject: "id", Username: "username", Email: "email", Name: "name", Avatar: "avatar_url"},
	},
	{
		Key:         "bitbucket",
		Label:       "Bitbucket",
		Mode:        OAuth2,
		ButtonLabel: "Continue with Bitbucket",
		SetupURL:    "https://support.atlassian.com/bitbucket-cloud/docs/use-oauth-on-bitbucket-cloud/",
		SetupHint: "Add an OAuth consumer in your Bitbucket workspace settings (the linked page walks through it). " +
			"Paste this host's callback URL in as the Callback URL. " +
			"The account and email scopes are all sign-in needs.",
		AuthorizeURL: "https://bitbucket.org/site/oauth2/authorize",
		TokenURL:     "https://bitbucket.org/site/oauth2/access_token",
		UserInfoURL:  "https://api.bitbucket.org/2.0/user",
		// Bitbucket's /2.0/user carries no email at all — it is always this second call.
		EmailsURL: "https://api.bitbucket.org/2.0/user/emails",
		Scopes:    "account email",
		ClaimMap:  ClaimMap{Subject: "uuid", Username: "username", Email: "email", Name: "display_name", Avatar: "links.avatar.href"},
	},
	// ── the oidc presets ──────────────────────────────────────────────────────────────────────────
	//
	// These carry a DiscoveryURL instead of endpoint URLs: discovery serves the endpoints, and the
	// id token — not a userinfo scrape — is the identity. A DiscoveryURL with a "<placeholder>" is a
	// template (see the field comment): the provider gives every tenant its own issuer.
	{
		Key:         "google",
		Label:       "Google",
		Mode:        OIDC,
		ButtonLabel: "Continue with Google",
		SetupURL:    "https://console.cloud.google.com/apis/credentials",
		SetupHint: "Create an OAuth client ID of type \"Web application\" on the Google Cloud console's Credentials page (configure the consent screen first if the console asks). " +
			"Paste this host's callback URL in as an Authorized redirect URI. " +
			"The default scopes (openid email profile) are all sign-in needs.",
		// The issuer, verified 2026-08-25: https://accounts.google.com/.well-known/openid-configuration
		// resolves and declares this exact issuer.
		DiscoveryURL: "https://accounts.google.com",
		Scopes:       "openid email profile",
		ClaimMap:     OIDCClaims,
	},
	{
		Key:         "microsoft",
		Label:       "Microsoft",
		Mode:        OIDC,
		ButtonLabel: "Continue with Microsoft",
		SetupURL:    "https://entra.microsoft.com",
		SetupHint: "Register an application in Microsoft Entra ID (App registrations) and create a client secret under Certificates & secrets. " +
			"Paste this host's callback URL in as a Web redirect URI. " +
			"Replace <tenant-id> below with your Directory (tenant) ID from the app's overview page.",
		// TENANT-SPECIFIC ON PURPOSE, not the multi-tenant `common`/`organizations` endpoints:
		// those discovery documents publish the LITERAL string {tenantid} as their issuer (verified
		// 2026-08-25 against https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration
		// and .../organizations/v2.0/... — both answer "issuer": "https://login.microsoftonline.com/{tenantid}/v2.0";
		// https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc documents that
		// tokens carry the tenant-substituted value instead). iss validation compares the token's
		// issuer against the document's verbatim, so the placeholder issuer rightly refuses every
		// login. A single-tenant URL has a real issuer and works.
		DiscoveryURL: "https://login.microsoftonline.com/<tenant-id>/v2.0",
		Scopes:       "openid profile email",
		ClaimMap:     OIDCClaims,
	},
	{
		Key:         "okta",
		Label:       "Okta",
		Mode:        OIDC,
		ButtonLabel: "Continue with Okta",
		SetupURL:    "https://developer.okta.com/docs/guides/sign-into-web-app-redirect/",
		SetupHint: "Create an OIDC Web Application in the Okta admin console (Applications → Create App Integration; the linked guide walks through it). " +
			"Paste this host's callback URL in as the Sign-in redirect URI. " +
			"Replace <your-domain> below with your Okta domain; the /oauth2/default path is the org's default authorization server.",
		DiscoveryURL: "https://<your-domain>.okta.com/oauth2/default",
		Scopes:       "openid profile email",
		ClaimMap:     OIDCClaims,
	},
	{
		Key:         "auth0",
		Label:       "Auth0",
		Mode:        OIDC,
		ButtonLabel: "Continue with Auth0",
		SetupURL:    "https://manage.auth0.com/#/applications",
		SetupHint: "Create a Regular Web Application in the Auth0 dashboard. " +
			"Paste this host's callback URL in as an Allowed Callback URL. " +
			"Replace <your-tenant> below with your tenant name (a region suffix like .eu may be part of your domain — copy the Domain field from the application's settings).",
		DiscoveryURL: "https://<your-tenant>.auth0.com",
		Scopes:       "openid profile email",
		ClaimMap:     OIDCClaims,
	},
	{
		Key:         "keycloak",
		Label:       "Keycloak",
		Mode:        OIDC,
		ButtonLabel: "Continue with Keycloak",
		SetupURL:    "https://www.keycloak.org/docs/latest/server_admin/index.html#_oidc_clients",
		SetupHint: "Create an OpenID Connect client in your realm's admin console with client authentication on (a confidential client, so it has a secret). " +
			"Paste this host's callback URL in as a Valid redirect URI. " +
			"Replace <host> and <realm> below with your Keycloak host and realm name.",
		DiscoveryURL: "https://<host>/realms/<realm>",
		Scopes:       "openid profile email",
		ClaimMap:     OIDCClaims,
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
// first, then the mode-specific ones) — what sso_providers.config holds and what the API returns.
type Config struct {
	Mode Mode `json:"mode"`
	// Enabled off keeps the row (and the secret) but hides the button and refuses `/start`.
	Enabled  bool   `json:"enabled"`
	ClientID string `json:"clientId"`
	// AllowedEmailDomains non-empty ⇒ a login whose email is outside the list is refused — including one with NO email.
	AllowedEmailDomains []string `json:"allowedEmailDomains"`
	// AllowedUsernames non-empty ⇒ a login whose username matches none of these globs is refused,
	// and — the same way the email list fails closed — a login with NO username is refused too.
	//
	// The username is whatever `ClaimMap.Username` names in the provider's response (MapClaims), so
	// this rule is meaningful on GitHub (`login`) and GitLab (`username`) and is FREQUENTLY INERT
	// elsewhere: a bare OIDC provider often supplies no `preferred_username` at all, and then every
	// login is refused rather than none — which is why an empty list is the default and means "no
	// rule". Matching is `path.Match` over lowercased pattern and value: `*` and `?`, and also
	// `[a-z]` character classes, so `qa-[0-9]*` is a legal rule.
	AllowedUsernames []string `json:"allowedUsernames"`
	// RequiredGroups non-empty ⇒ the provider is asked which groups/orgs this user belongs to, and a
	// login in none of them is refused. EXACT names, case-insensitive — not globs, because a GitLab
	// group is a path (`acme/backend`) and `path.Match` gives `/` a meaning that would surprise
	// whoever typed `*`. GitHub org logins are case-preserving but case-insensitive, hence the fold.
	RequiredGroups []string `json:"requiredGroups"`
	// DefaultRole is the role for auto-provisioned users — and EMPTY MEANS INHERIT THE HOST
	// DEFAULT, resolved when an account is actually provisioned rather than frozen into this row
	// when it was saved. See ParseConfig, which used to fill it in.
	DefaultRole string `json:"defaultRole"`
	// Label is the button text and the `providerKey` half of a link's identity.
	Label string `json:"label"`
	// DiscoveryURL, mode A: an issuer (`https://accounts.google.com`) or a full `.well-known` URL.
	DiscoveryURL string `json:"discoveryUrl"`
	// Provider: in oauth2 mode, the `Presets` key the endpoints came from, or `custom`. In oidc
	// mode it is the oidc preset's key when one was named ("google") and "" otherwise — display and
	// DerivedKey only; identity in oidc mode keys on the discovery issuer, never on this.
	Provider     string `json:"provider"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	UserInfoURL  string `json:"userInfoUrl"`
	// EmailsURL is consulted ONLY when the userinfo response carries no email. Preset-filled (GitHub
	// and Bitbucket both keep the address off the profile); an operator only types it for a
	// self-hosted provider.
	EmailsURL string `json:"emailsUrl"`
	// GroupsURL is consulted ONLY when RequiredGroups is non-empty. Preset-filled under the same
	// rule EmailsURL is, and typed by hand for a self-hosted provider.
	GroupsURL string   `json:"groupsUrl"`
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

// lowerList is a submitted string array, trimmed, lowercased, empties dropped — and never nil
// (Go rule 3: a stored `null` is a blank list in the UI).
func lowerList(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, x := range list {
		if s := strings.ToLower(str(x)); s != "" {
			out = append(out, s)
		}
	}
	return out
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
	// The provider (a Presets key, "custom", or absent) is resolved FIRST: a preset knows its own
	// mode, so `{"provider":"google"}` needs no mode typed — and an unknown name is refused the
	// same way whichever mode was.
	provider := str(get("provider"))
	preset := PresetFor(provider)
	if preset == nil && provider != "" && provider != "custom" {
		keys := make([]string, len(Presets))
		for i, p := range Presets {
			keys[i] = p.Key
		}
		return nil, errorf(`unknown provider "` + provider + `" — use one of ` + strings.Join(keys, ", ") + ", or custom")
	}
	mode := str(get("mode"))
	if mode == "" {
		if preset != nil {
			mode = string(preset.Mode)
		} else {
			mode = "oidc"
		}
	}
	if mode != "oidc" && mode != "oauth2" {
		return nil, errorf("mode must be 'oidc' or 'oauth2'")
	}
	// A preset and a contradicting mode is a config that cannot work: an oidc preset carries no
	// endpoint URLs for the oauth2 branch to use, and github-under-oidc is the documented trap (its
	// discovery document signs Actions job tokens, not user logins — see the package header).
	if preset != nil && Mode(mode) != preset.Mode {
		return nil, errorf(`provider "` + provider + `" is an ` + string(preset.Mode) + ` preset — omit mode or set it to "` + string(preset.Mode) + `"`)
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
		// Both lists are read HERE, in the literal, and not in a mode branch: the scope check below
		// reads RequiredGroups, and a field populated after it would leave that check reading an
		// empty slice — a refusal that never fires and a test that proves nothing.
		AllowedUsernames: lowerList(get("allowedUsernames")),
		RequiredGroups:   lowerList(get("requiredGroups")),
		// The role every account this provider auto-provisions is created with — KEPT EXACTLY AS
		// TYPED, empty included.
		//
		// EMPTY MEANS INHERIT THE HOST DEFAULT (the `default_role` setting, `viewer` when nobody
		// set one), resolved at provision time by whoever calls auth.SsoSignIn. It has NEVER meant
		// admin and must never mean admin again: this line filled an empty value with "admin" back
		// when admin was the only role, and with allowedEmailDomains, allowedUsernames and
		// requiredGroups all empty — the shape every preset saves with — any stranger who completed
		// the OAuth flow was minted a full administrator, able to create and delete accounts and
		// rewrite this very config. It then filled it with "viewer", which was safe but froze the
		// answer into the stored row: an operator who later changed the host default found their
		// providers still minting viewers.
		//
		// So the fill is gone, in BOTH directions. A host that wants SSO logins to arrive as
		// anything in particular says so — here, or once for the whole host. Granting privilege is
		// not a thing to do by omission, and neither is refusing to follow the host's own default.
		//
		// The string is not validated here: internal/auth imports this package, so this package
		// cannot import it back to reach auth.ValidRole. The API validates a NON-EMPTY value before
		// storing (routes_auth), and auth-side tests pin what empty resolves to at every step.
		DefaultRole: str(get("defaultRole")),
	}
	if v, ok := o.Get("enabled"); ok {
		cfg.Enabled = truthy(v)
	}
	// A malformed glob is refused here rather than silently matching nothing forever. `path.Match`
	// reports ErrBadPattern for `qa-[0-9` whatever it is matched against, so an empty name is a
	// complete probe of the pattern's syntax.
	for _, p := range cfg.AllowedUsernames {
		if _, err := path.Match(p, ""); err != nil {
			return nil, errorf(`allowedUsernames entry "` + p + `" is not a valid pattern: ` + err.Error())
		}
	}

	if mode == "oidc" {
		discoveryURL := str(get("discoveryUrl"))
		if discoveryURL == "" {
			discoveryURL = str(get("issuer"))
		}
		// An oidc preset's contribution is its issuer; anything the operator typed still wins,
		// exactly as an oauth2 preset's endpoints do below.
		if discoveryURL == "" && preset != nil {
			discoveryURL = preset.DiscoveryURL
		}
		if discoveryURL == "" {
			return nil, errorf("issuer or discoveryUrl is required for OIDC")
		}
		if strings.Contains(discoveryURL, "<") {
			return nil, errorf(`discoveryUrl "` + discoveryURL + `" still carries a <placeholder> — replace it with your own value (your tenant, domain or realm) before saving`)
		}
		// "<" marks a template placeholder (okta's "<your-domain>", microsoft's "<tenant-id>" — see
		// Preset.DiscoveryURL): the provider has no single issuer, and saving the template verbatim
		// would fail every login at discovery. Checked BEFORE httpsURL, whose "must be an absolute
		// URL" answer would send the operator to fix the wrong thing.
		// The TypeScript computed the label (`new URL(discoveryUrl).hostname`) before validating the
		// URL, so an unparsable issuer with no label surfaced as a TypeError; here it is the *Error
		// httpsURL produces either way.
		normalized, err := httpsURL(discoveryURL, "issuer/discoveryUrl")
		if err != nil {
			return nil, err
		}
		cfg.Label = str(get("label"))
		if cfg.Label == "" && preset != nil {
			cfg.Label = preset.Label
		}
		if cfg.Label == "" {
			u, _ := parseURL(discoveryURL)
			cfg.Label = whatwgHostname(u)
		}
		cfg.DiscoveryURL = normalized
		// The preset key is KEPT on an oidc config ("google"), so a UI can show which table row it
		// came from and DerivedKey can name it. Identity never reads it in oidc mode — the callback
		// keys links on the discovery ISSUER (routes_auth), so two configs on one directory agree.
		if preset != nil {
			cfg.Provider = provider
		}
		cfg.Scopes = str(get("scopes"))
		if cfg.Scopes == "" && preset != nil {
			cfg.Scopes = preset.Scopes
		}
		if cfg.Scopes == "" {
			cfg.Scopes = "openid profile email"
		}
		// There is no groups endpoint in a discovery document and no groups claim in the mapping, so
		// an OIDC config with a group rule could only ever fail every login with "your groups could
		// not be determined". Refuse it while the operator is looking at the form.
		if len(cfg.RequiredGroups) > 0 {
			return nil, errorf("requiredGroups needs a provider whose groups endpoint this host knows — that is an OAuth 2.0 preset (github, gitlab), not a discovered OIDC issuer")
		}
		cm := OIDCClaims
		if preset != nil {
			cm = mergeClaims(cm, preset.ClaimMap)
		}
		cfg.ClaimMap = mergeClaims(cm, pickClaims(get("claimMap")))
		return cfg, nil
	}

	if provider == "" {
		provider = "custom"
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
	// Same rule, same reason, for the groups endpoint: gitlab.com is not the self-hosted GitLab
	// whose token this is, and asking it for the user's groups would hand a third party a live
	// credential. An operator who moved userinfo types their own groupsUrl (the response shape is
	// the preset's, so its GroupsKey still reads it).
	groupsURL := str(get("groupsUrl"))
	if groupsURL == "" && preset != nil && userInfoURL == preset.UserInfoURL {
		groupsURL = preset.GroupsURL
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
	if groupsURL != "" {
		if cfg.GroupsURL, err = httpsURL(groupsURL, "groupsUrl"); err != nil {
			return nil, err
		}
	}
	cfg.Scopes = presetOr(str(get("scopes")), func(p *Preset) string { return p.Scopes })
	// THE SCOPE IS RESOLVED HERE, at save time, and not at /start: cfg.Scopes is what the operator
	// reads back in the UI and what AuthorizeURL sends, and if the two could disagree the IdP would
	// be handed a scope nobody chose. A group rule the token has no scope for is not a runtime
	// hiccup either — every login refuses, identically, forever — so it is refused while there is
	// still a human at the form to read the sentence.
	if len(cfg.RequiredGroups) > 0 {
		if preset == nil || preset.GroupsKey == "" {
			return nil, errorf(`requiredGroups is not supported for provider "` + provider + `" — no preset says which field of its groups response names a group`)
		}
		if cfg.GroupsURL == "" {
			return nil, errorf("requiredGroups needs a groups endpoint — set groupsUrl (this host does not assume " + preset.GroupsURL + " belongs to a self-hosted " + preset.Label + ")")
		}
		if len(preset.GroupsScopes) > 0 && !hasAnyScope(cfg.Scopes, preset.GroupsScopes) {
			return nil, errorf(`requiredGroups needs the "` + preset.GroupsScopes[0] + `" scope to read ` + cfg.GroupsURL +
				` (or any of: ` + strings.Join(preset.GroupsScopes, ", ") + `) — add it to scopes (currently "` + cfg.Scopes + `")`)
		}
	}
	cfg.ClaimMap = OIDCClaims
	if preset != nil {
		cfg.ClaimMap = mergeClaims(cfg.ClaimMap, preset.ClaimMap)
	}
	cfg.ClaimMap = mergeClaims(cfg.ClaimMap, pickClaims(get("claimMap")))
	return cfg, nil
}

// DerivedKey is the sso_providers slug for a config that never chose one: the provider name when
// set, else "oidc". It exists for the pre-multi-provider shapes — store migration 7 derives the
// same key in SQL (keep the two in sync), a config-document "sso" object from 0.30.0 lands under
// it, and a keyless PUT on an empty host uses it so old scripts keep working.
func (c *Config) DerivedKey() string {
	if c.Provider != "" {
		return c.Provider
	}
	return "oidc"
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
	// Groups are the group/org names the provider listed, as it spelled them. Non-nil but EMPTY
	// unless the callback actually asked (it only asks when there is a rule to answer), so empty
	// here is never evidence of anything — "the fetch failed" travels separately, as an error, and
	// the two produce different refusals in auth.SsoSignIn.
	Groups []string `json:"groups"`
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
		Groups:   []string{},
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

// UsernameAllowed fails CLOSED, exactly as EmailAllowed does: an empty list allows everything, and a
// non-empty one requires a username that matches — so NO username is a refusal, not a pass.
//
// The empty-username guard is explicit and cannot be folded into the loop: `path.Match("*", "")` is
// true in Go, so the commonest pattern of all would otherwise wave through the identity that has
// nothing to check.
//
// `path.Match` is the whole matcher — `*`, `?`, and `[a-z]` character classes, so a rule can be
// `qa-[0-9]*` and not only `qa-*`. It also gives `/` a meaning (`*` does NOT cross one), which is
// nothing to a username and would be a trap for a GitLab group path; groups are matched by name.
func UsernameAllowed(username string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	if username == "" {
		return false
	}
	name := strings.ToLower(username)
	for _, p := range patterns {
		// GitHub usernames are case-preserving but case-insensitive, so both sides are folded. A
		// pattern that failed to compile was refused by ParseConfig; here it simply matches nothing.
		if ok, err := path.Match(strings.ToLower(p), name); err == nil && ok {
			return true
		}
	}
	return false
}

// GroupsAllowed fails CLOSED: an empty requirement allows everything, and a non-empty one needs the
// user to be in at least ONE of the named groups. Exact names, case-folded — not globs (see the
// RequiredGroups comment on Config).
func GroupsAllowed(groups, required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, g := range groups {
		name := strings.ToLower(strings.TrimSpace(g))
		for _, want := range required {
			if name != "" && name == strings.ToLower(want) {
				return true
			}
		}
	}
	return false
}

// GroupNamesOf pulls the group names out of a provider's groups endpoint, `key` being the field that
// names one (Preset.GroupsKey). Tolerates the two shapes PrimaryEmailOf does: a bare array (GitHub,
// GitLab) and `{ values: [...] }`. Never nil.
func GroupNamesOf(payload any, key string) []string {
	out := []string{}
	if key == "" {
		return out
	}
	var list []any
	switch x := payload.(type) {
	case []any:
		list = x
	case *omap.Map:
		list = x.GetSlice("values")
	}
	for _, r := range list {
		m, ok := r.(*omap.Map)
		if !ok || m == nil {
			continue
		}
		if v, found := lookup(m, key); found {
			if name := asText(v); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// hasScope is whether an OAuth scope string already asks for `want`. Space-delimited per RFC 6749;
// commas are tolerated because GitHub accepts a comma-separated list and operators paste one.
func hasAnyScope(scopes string, sufficient []string) bool {
	for _, s := range strings.FieldsFunc(scopes, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		for _, want := range sufficient {
			if s == want {
				return true
			}
		}
	}
	return false
}
