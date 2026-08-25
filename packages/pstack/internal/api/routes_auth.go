package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
)

// secretMask is what the SSO config read returns in place of the client secret.
const secretMask = "••••••••"

const sessionMaxAge = 30 * 24 * 60 * 60

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	n, _ := s.auth.UserCount()
	var ssoSummary any
	if enabled, _ := s.enabledSsoProviders(); len(enabled) > 0 {
		// What the login page needs BEFORE authenticating: which buttons to draw, what to write on
		// each, and the key to hand /start. `preset` is for an icon; never anything else about a
		// provider. Null (not `{providers: []}`) when there is nothing to draw, as before.
		providers := make([]jsonx.Object, 0, len(enabled))
		for _, p := range enabled {
			providers = append(providers, jsonx.O("key", p.Key, "label", p.Config.Label, "preset", p.Config.Provider))
		}
		ssoSummary = jsonx.O("providers", providers)
	}
	writeJSON(w, 200, jsonx.O(
		"ok", true,
		"authEnforced", s.opts.Token != "",
		"hasUsers", n > 0,
		"sso", ssoSummary,
		"dataDir", s.opts.DataDir,
		"version", s.opts.Version,
	))
}

// ssoCallbackURL is THE callback URL, for every provider. Under /api/ deliberately: in advanced-UI
// mode /api/ is the only prefix nginx proxies to this process.
func (s *Server) ssoCallbackURL(r *http.Request) string {
	return baseURL(s.opts.Domain, r) + "/api/auth/sso/callback"
}

// ssoFailed: both SSO legs are reached by NAVIGATION, so every failure ends on the login page
// carrying the reason — a JSON body here would be painted in somebody's address bar.
func ssoFailed(w http.ResponseWriter, message string) {
	w.Header().Set("location", "/login?sso_error="+js.EncodeURIComponent(message))
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(302)
}

func redirect(w http.ResponseWriter, to string, headers ...[2]string) {
	w.Header().Set("location", to)
	w.Header().Set("cache-control", "no-store")
	for _, h := range headers {
		w.Header().Add(h[0], h[1])
	}
	w.WriteHeader(302)
}

// preGate handles the routes that run BEFORE the gate, or nobody could ever log in. Returns true
// when it answered.
func (s *Server) preGate(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "/api/auth/login" && r.Method == http.MethodPost:
		body := bodyObject(r)
		username, uok := getStr(body, "username")
		password, pok := getStr(body, "password")
		if body == nil || !uok || !pok {
			writeError(w, 400, "body must be { username, password }")
			return true
		}
		session, user, err := s.auth.Login(username, password)
		if err != nil {
			s.preGateFail(w, err)
			return true
		}
		writeJSON(w, 200, jsonx.O("user", user), [2]string{"set-cookie", sessionCookie(r, session, sessionMaxAge)})
		return true

	case path == "/api/auth/logout" && r.Method == http.MethodPost:
		// Every candidate, same reason as principal(): with duplicate cookies, revoking only the
		// first can leave the session the browser will actually use next time still alive.
		for _, c := range sessionCandidates(r) {
			_ = s.auth.Logout(c)
		}
		writeJSON(w, 200, jsonx.O("ok", true), [2]string{"set-cookie", sessionCookie(r, "", 0)})
		return true

	case path == "/api/auth/bootstrap" && r.Method == http.MethodPost:
		// Gated by PSTACK_TOKEN specifically, not by principal(): its whole purpose is to work
		// before any account exists.
		if s.opts.Token != "" && bearer(r) != s.opts.Token {
			writeError(w, 401, "bootstrap requires the PSTACK_TOKEN bearer")
			return true
		}
		body := bodyObject(r)
		username, uok := getStr(body, "username")
		password, pok := getStr(body, "password")
		if body == nil || !uok || !pok {
			writeError(w, 400, "body must be { username, password }")
			return true
		}
		user, err := s.auth.Bootstrap(username, password)
		if err != nil {
			s.preGateFail(w, err)
			return true
		}
		// 409, not silent success: "already bootstrapped" and "created" must be distinguishable.
		if user == nil {
			writeError(w, 409, "accounts already exist — bootstrap is only for the first one")
			return true
		}
		writeJSON(w, 201, jsonx.O("user", user))
		return true

	case path == "/api/auth/sso/start" && r.Method == http.MethodGet:
		s.ssoStart(w, r)
		return true

	case path == "/api/auth/sso/callback" && r.Method == http.MethodGet:
		s.ssoCallback(w, r)
		return true
	}
	return false
}

// preGateFail: the pre-gate routes are outside the big handler, so this is the only place an SSO
// failure on /start can become a message instead of a 500. 502 for the provider, 400 for auth.
func (s *Server) preGateFail(w http.ResponseWriter, err error) {
	switch {
	case auth.IsError(err):
		writeError(w, 400, err.Error())
	case sso.IsError(err):
		writeError(w, 502, err.Error())
	default:
		writeError(w, 500, err.Error())
	}
}

// enabledSsoProviders is the sign-in surface: every stored provider whose config is enabled, in
// key order. A disabled row keeps its config and secret but draws no button and refuses /start.
func (s *Server) enabledSsoProviders() ([]auth.SsoProviderRow, error) {
	rows, err := s.auth.ListSsoProviders()
	if err != nil {
		return nil, err
	}
	enabled := []auth.SsoProviderRow{}
	for _, row := range rows {
		if row.Config.Enabled {
			enabled = append(enabled, row)
		}
	}
	return enabled, nil
}

func ssoKeysOf(rows []auth.SsoProviderRow) string {
	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = row.Key
	}
	return strings.Join(keys, ", ")
}

// ssoNamed prefixes the provider's label onto a login-page failure, but only when the host has
// several providers — two buttons failing with identical sentences is undiagnosable, while the
// single-provider messages stay byte-identical to the pre-multi-provider ones.
func ssoNamed(label string, several bool, msg string) string {
	if several {
		return label + ": " + msg
	}
	return msg
}

func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.enabledSsoProviders()
	if err != nil {
		s.preGateFail(w, err)
		return
	}
	var stored *auth.SsoProviderRow
	if key, ok := query(r.URL.RawQuery, "provider"); ok && key != "" {
		for i := range enabled {
			if enabled[i].Key == key {
				stored = &enabled[i]
				break
			}
		}
		if stored == nil {
			// One sentence for "no such row" and "row disabled": what a browser may sign in with
			// is exactly the enabled set, and a disabled provider is not on the login page either.
			ssoFailed(w, `no sign-in provider "`+key+`" is configured on this host`)
			return
		}
	} else {
		switch len(enabled) {
		case 0:
			ssoFailed(w, "single sign-on is not configured on this host")
			return
		case 1:
			// The keyless /start of a single-provider host — every pre-multi-provider login link.
			stored = &enabled[0]
		default:
			ssoFailed(w, "this host has several sign-in providers ("+ssoKeysOf(enabled)+") — pass ?provider= to choose one")
			return
		}
	}
	failed := func(msg string) { ssoFailed(w, ssoNamed(stored.Config.Label, len(enabled) > 1, msg)) }
	// Discovery can be down or moved — the realistic failure here, and the one that must not be
	// a JSON blob.
	endpoints, err := s.ssoClient.EndpointsFor(stored.Config)
	if err != nil {
		if sso.IsError(err) {
			failed(err.Error())
			return
		}
		s.preGateFail(w, err)
		return
	}
	verifier, challenge := sso.PKCE()
	state := sso.RandomB64URL(32)
	next, _ := query(r.URL.RawQuery, "next")
	// The verifier is the half the provider never sees; parking it server-side under the state is
	// what makes an intercepted `code` useless to anyone but this process. The provider KEY rides
	// in the same value, so the callback answers with the config this login started against —
	// sso_state stays a generic KV.
	parked := jsonx.Must(jsonx.O("verifier", verifier, "next", sso.SafeNext(next), "provider", stored.Key))
	if err := s.auth.Transient().Set("sso:"+state, string(parked), s.ssoTTL); err != nil {
		s.preGateFail(w, err)
		return
	}
	to, err := sso.AuthorizeURL(stored.Config, endpoints, sso.AuthorizeArgs{RedirectURI: s.ssoCallbackURL(r), State: state, Challenge: challenge})
	if err != nil {
		if sso.IsError(err) {
			failed(err.Error())
			return
		}
		s.preGateFail(w, err)
		return
	}
	redirect(w, to)
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	// Whoever is here is a BROWSER coming back from a consent screen — see ssoFailed.
	back := func(msg string) { ssoFailed(w, msg) }
	// The provider refused (consent denied, app suspended, wrong redirect_uri). Its own words.
	if refused := sso.DescribeError(sso.FromQuery(r.URL.RawQuery)); refused != "" {
		back(refused)
		return
	}
	state, _ := query(r.URL.RawQuery, "state")
	code, _ := query(r.URL.RawQuery, "code")
	if state == "" || code == "" {
		back("the provider did not return an authorization code")
		return
	}
	// READ AND DELETE IN ONE STATEMENT: single-use state is what stops a replayed callback.
	parkedRaw, found, err := s.auth.Transient().Take("sso:" + state)
	if err != nil || !found {
		back("this sign-in has expired or was already used — try again")
		return
	}
	parsed, _ := omap.Parse([]byte(parkedRaw))
	parked, _ := parsed.(*omap.Map)
	verifier, _ := getStr(parked, "verifier")
	next, _ := getStr(parked, "next")
	// The state's own value says which provider this login STARTED against, and that is the only
	// config the code may be exchanged with — resolving "the" provider here instead would complete
	// a login begun against A with B's secret and rules. A state parked before this host knew about
	// provider keys carries none and reads as expired: the browser just starts over.
	parkedKey, _ := getStr(parked, "provider")
	if parkedKey == "" {
		back("this sign-in has expired or was already used — try again")
		return
	}
	enabled, err := s.enabledSsoProviders()
	if err != nil {
		s.preGateFail(w, err)
		return
	}
	var stored *auth.SsoProviderRow
	for i := range enabled {
		if enabled[i].Key == parkedKey {
			stored = &enabled[i]
			break
		}
	}
	if stored == nil {
		// Deleted or disabled between /start and the consent screen.
		back(`sign-in provider "` + parkedKey + `" is no longer configured on this host`)
		return
	}
	back = func(msg string) { ssoFailed(w, ssoNamed(stored.Config.Label, len(enabled) > 1, msg)) }

	fail := func(err error) {
		if sso.IsError(err) || auth.IsError(err) {
			back(err.Error())
			return
		}
		s.preGateFail(w, err)
	}
	cfg := stored.Config
	endpoints, err := s.ssoClient.EndpointsFor(cfg)
	if err != nil {
		fail(err)
		return
	}
	token, err := s.ssoClient.ExchangeCode(cfg, endpoints, sso.ExchangeArgs{Code: code, RedirectURI: s.ssoCallbackURL(r), Verifier: verifier, ClientSecret: stored.ClientSecret})
	if err != nil {
		fail(err)
		return
	}
	accessToken, _ := getStr(token, "access_token")
	var identity *sso.Identity
	var providerKey string
	if cfg.Mode == sso.OIDC {
		idToken, ok := getStr(token, "id_token")
		if !ok || idToken == "" {
			back(`the provider returned no id token — is "openid" in the scopes?`)
			return
		}
		claims, err := s.ssoClient.VerifyIDToken(idToken, sso.VerifyArgs{Issuer: endpoints.Issuer, ClientID: cfg.ClientID, JwksURI: endpoints.JwksURI})
		if err != nil {
			fail(err)
			return
		}
		identity, err = sso.MapClaims(claims, cfg.ClaimMap)
		if err != nil {
			fail(err)
			return
		}
		// Userinfo is the FALLBACK, not the default. The id token's claims still win the merge —
		// it is the signed half.
		if identity.Email == "" && endpoints.UserInfoURL != "" && accessToken != "" {
			if extra, err := s.ssoClient.FetchJSON(endpoints.UserInfoURL, accessToken); err == nil {
				if em, ok := extra.(*omap.Map); ok {
					merged := em.Clone()
					claims.Each(func(k string, v any) { merged.Set(k, v) })
					if id2, err := sso.MapClaims(merged, cfg.ClaimMap); err == nil {
						identity = id2
					} else {
						fail(err)
						return
					}
				}
			}
		}
		// THE LINK KEY IS NOT THE ROW'S SLUG. sso_links.provider_key names the upstream DIRECTORY
		// an identity came from — the discovery issuer here, the preset key or "custom" below —
		// never the operator-chosen sso_providers key. Two rows on the same issuer (or the same
		// github preset) therefore share a link namespace, which is correct: the same subject from
		// the same upstream directory is the same person, whichever button they clicked.
		//
		// The issuer, not the hostname someone typed: two configs pointing at one issuer are the
		// same directory and must resolve to the same links.
		providerKey = endpoints.Issuer
	} else {
		providerKey = cfg.Provider
		if providerKey == "" {
			providerKey = "custom"
		}
		if endpoints.UserInfoURL != "" {
			if accessToken == "" {
				back("the provider returned no access token")
				return
			}
			info, err := s.ssoClient.FetchJSON(endpoints.UserInfoURL, accessToken)
			if err != nil {
				fail(err)
				return
			}
			identity, err = sso.MapClaims(info, cfg.ClaimMap)
			if err != nil {
				fail(err)
				return
			}
		} else {
			// No userinfo endpoint configured: the subject comes from the token response and that
			// is the whole identity.
			identity, err = sso.MapClaims(token, cfg.ClaimMap)
			if err != nil {
				fail(err)
				return
			}
		}
	}
	// A provider that keeps the address off the profile serves it from a second endpoint
	// (GitHub's private-email default). Only asked for when it is actually missing.
	if identity.Email == "" && endpoints.EmailsURL != "" && accessToken != "" {
		if list, err := s.ssoClient.FetchJSON(endpoints.EmailsURL, accessToken); err == nil {
			if found := sso.PrimaryEmailOf(list); found != nil {
				identity.Email = found.Email
				v := found.Verified
				identity.EmailVerified = &v
			}
		}
	}
	// The group list, and ONLY when there is a rule that needs it: an operator who set no group rule
	// must not have a new scope requested on their behalf or a new call made against their provider.
	// The failure is CARRIED, not swallowed like the emails backfill above — an unanswered fetch is
	// not "no groups", and the gate turns the two into different refusals.
	var groupsErr error
	if len(cfg.RequiredGroups) > 0 {
		switch {
		case endpoints.GroupsURL == "" || endpoints.GroupsKey == "":
			// Unreachable through the API today: ParseConfig refuses this combination at save, and
			// parseSsoRow re-validates on every READ — so a hand-edited row carrying it reads as "not
			// configured" and never gets here. Fail closed rather than depend on that.
			groupsErr = errors.New("no groups endpoint is configured for this provider")
		case accessToken == "":
			groupsErr = errors.New("the provider returned no access token")
		default:
			list, err := s.ssoClient.FetchJSON(endpoints.GroupsURL, accessToken)
			if err != nil {
				groupsErr = err
			} else {
				identity.Groups = sso.GroupNamesOf(list, endpoints.GroupsKey)
			}
		}
	}
	signed, err := s.auth.SsoSignIn(providerKey, identity, auth.SsoSignInOpts{
		DefaultRole:         cfg.DefaultRole,
		AllowedEmailDomains: cfg.AllowedEmailDomains,
		AllowedUsernames:    cfg.AllowedUsernames,
		RequiredGroups:      cfg.RequiredGroups,
		GroupsErr:           groupsErr,
	})
	if err != nil {
		fail(err)
		return
	}
	// Adoption is the one outcome worth a line in the log: an SSO identity took over an account
	// that already existed locally, and nobody clicked anything to approve it.
	if signed.How != auth.Linked {
		what := "created account"
		if signed.How == auth.Adopted {
			what = "linked to existing account"
		}
		s.opts.Log("[sso] " + what + ` "` + signed.User.Username + `" for ` + providerKey + " subject " + identity.Subject)
	}
	redirect(w, sso.SafeNext(next), [2]string{"set-cookie", sessionCookie(r, signed.Session, sessionMaxAge)})
}

// ── gated: me, users, tokens, sso config ────────────────────────────────────────────────────

var (
	userPasswordRe = regexp.MustCompile(`^/api/users/(\d+)/password$`)
	userRe         = regexp.MustCompile(`^/api/users/(\d+)$`)
	tokenRe        = regexp.MustCompile(`^/api/tokens/(\d+)$`)
	// The segment pattern is auth's slug rule, so a path that could never name a row 404s from the
	// chain instead of reaching the store.
	ssoProviderRe = regexp.MustCompile(`^/api/sso/config/([a-z0-9][a-z0-9-]{0,31})$`)
)

// accountRoutes answers the account routes; ok=false means "not mine".
func (s *Server) accountRoutes(w http.ResponseWriter, r *http.Request, path string, who *auth.Principal) (bool, error) {
	if path == "/api/auth/me" && r.Method == http.MethodGet {
		if who.Kind == auth.KindShare {
			tok, ok := query(r.URL.RawQuery, "token")
			if !ok || tok == "" {
				tok = bearer(r)
			}
			var expiresAt any
			if c := share.Verify(s.opts.Token, tok, time.Now().UnixMilli()); c != nil {
				expiresAt = c.Exp * 1000
			}
			writeJSON(w, 200, jsonx.O("root", false, "share", jsonx.O("deployment", who.Deployment, "views", who.Views, "expiresAt", expiresAt)))
			return true, nil
		}
		if who.Kind == auth.KindRoot {
			writeJSON(w, 200, jsonx.O("root", true))
		} else {
			writeJSON(w, 200, jsonx.O("root", false, "user", who.User))
		}
		return true, nil
	}

	if path == "/api/users" && r.Method == http.MethodGet {
		users, err := s.auth.ListUsers()
		if err != nil {
			return true, err
		}
		writeJSON(w, 200, jsonx.O("users", users))
		return true, nil
	}
	if m := userPasswordRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodPut {
		body := bodyObject(r)
		password, ok := getStr(body, "password")
		if body == nil || !ok {
			writeError(w, 400, "body must be { password }")
			return true, nil
		}
		// Revokes that user's sessions and tokens. Says so, because a caller changing their OWN
		// password is about to be signed out and should not read that as a bug.
		changed, err := s.auth.SetPassword(intID(m[1]), password)
		if err != nil {
			return true, err
		}
		if !changed {
			writeError(w, 404, "no such user: "+m[1])
			return true, nil
		}
		writeJSON(w, 200, jsonx.O("ok", true, "revokedSessions", true))
		return true, nil
	}
	if path == "/api/users" && r.Method == http.MethodPost {
		body := bodyObject(r)
		username, uok := getStr(body, "username")
		password, pok := getStr(body, "password")
		if body == nil || !uok || !pok {
			writeError(w, 400, "body must be { username, password, email? }")
			return true, nil
		}
		// The optional email is what makes an SSO login able to ADOPT this account later.
		email := ""
		if e, ok := getStr(body, "email"); ok && strings.TrimSpace(e) != "" {
			email = strings.ToLower(strings.TrimSpace(e))
		}
		user, err := s.auth.CreateUser(username, password, auth.CreateOpts{Email: email})
		if err != nil {
			return true, err
		}
		writeJSON(w, 201, jsonx.O("user", user))
		return true, nil
	}
	if m := userRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		deleted, err := s.auth.DeleteUser(intID(m[1]))
		if err != nil {
			return true, err
		}
		if !deleted {
			writeError(w, 404, "no such user")
			return true, nil
		}
		writeJSON(w, 200, jsonx.O("deleted", intID(m[1])))
		return true, nil
	}

	// Personal tokens are scoped to the CALLER — root has PSTACK_TOKEN and needs none, and one
	// user must not mint or list another's.
	if path == "/api/tokens" {
		if who.Kind != auth.KindUser {
			writeError(w, 400, "personal tokens belong to an account — sign in as one")
			return true, nil
		}
		switch r.Method {
		case http.MethodGet:
			tokens, err := s.auth.ListTokens(who.User.ID)
			if err != nil {
				return true, err
			}
			writeJSON(w, 200, jsonx.O("tokens", tokens))
			return true, nil
		case http.MethodPost:
			body := bodyObject(r)
			name, ok := getStr(body, "name")
			if body == nil || !ok {
				writeError(w, 400, "body must be { name }")
				return true, nil
			}
			token, id, err := s.auth.CreateToken(who.User.ID, name)
			if err != nil {
				return true, err
			}
			// The one and only time the plaintext leaves the server.
			writeJSON(w, 201, jsonx.O("id", id, "token", token))
			return true, nil
		}
	}
	if m := tokenRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		if who.Kind != auth.KindUser {
			writeError(w, 400, "personal tokens belong to an account")
			return true, nil
		}
		deleted, err := s.auth.DeleteToken(who.User.ID, intID(m[1]))
		if err != nil {
			return true, err
		}
		if !deleted {
			writeError(w, 404, "no such token")
			return true, nil
		}
		writeJSON(w, 200, jsonx.O("deleted", intID(m[1])))
		return true, nil
	}

	// One keyed provider. Bare /api/sso/config is matched later in routes.go's chain; this keyed
	// variant lives here only because this file owns the SSO config handlers — the two paths
	// cannot overlap, so the split changes nothing about dispatch order.
	if m := ssoProviderRe.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		return true, s.deleteSsoProvider(w, m[1])
	}
	return false, nil
}

// ssoConfigRoutes: the stored providers, each under an operator-chosen slug. The client secret
// goes IN and never comes back out — a read learns `secretSet` and nothing else. The keyed DELETE
// (/api/sso/config/<key>) is matched in accountRoutes and lands in deleteSsoProvider below; the
// bare-path methods land here.
func (s *Server) ssoConfigRoutes(w http.ResponseWriter, r *http.Request) error {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.auth.ListSsoProviders()
		if err != nil {
			return err
		}
		presets := make([]jsonx.Object, 0, len(sso.Presets))
		for _, p := range sso.Presets {
			presets = append(presets, jsonx.O("key", p.Key, "label", p.Label, "mode", string(p.Mode),
				"buttonLabel", p.ButtonLabel, "setupUrl", p.SetupURL, "setupHint", p.SetupHint,
				"discoveryUrl", p.DiscoveryURL, "authorizeUrl", p.AuthorizeURL, "tokenUrl", p.TokenURL,
				"userInfoUrl", p.UserInfoURL, "scopes", p.Scopes, "claimMap", p.ClaimMap))
		}
		providers := make([]jsonx.Object, 0, len(rows))
		for _, row := range rows {
			providers = append(providers, jsonx.O("key", row.Key, "config", row.Config,
				"secretSet", row.ClientSecret != "", "updatedAt", row.UpdatedAt))
		}
		writeJSON(w, 200, jsonx.O(
			"providers", providers,
			// Exactly what the operator must register on their side — ONE URL for every provider,
			// built by the same helper the flow uses, so the two cannot drift.
			"callbackUrl", s.ssoCallbackURL(r),
			"presets", presets,
		))
		return nil
	case http.MethodPut:
		body := bodyObject(r)
		if body == nil {
			writeError(w, 400, "body must be an object")
			return nil
		}
		config, err := sso.ParseConfig(body)
		if err != nil {
			return err
		}
		key, _ := getStr(body, "key")
		key = strings.TrimSpace(key)
		if key == "" {
			// The keyless PUT is the pre-multi-provider surface. An empty host derives the slug
			// exactly as store migration 7 did, and a single-provider host replaces its one row —
			// both keep a 0.30.0 script working unchanged. Several rows is a guess this API
			// refuses to make.
			rows, err := s.auth.ListSsoProviders()
			if err != nil {
				return err
			}
			switch len(rows) {
			case 0:
				key = config.DerivedKey()
			case 1:
				key = rows[0].Key
			default:
				writeError(w, 400, "this host has several sign-in providers ("+ssoKeysOf(rows)+`) — pass "key" to say which one to replace`)
				return nil
			}
		}
		typed, _ := getStr(body, "clientSecret")
		typed = strings.TrimSpace(typed)
		secret := typed
		if typed == secretMask {
			// The mask round-tripped from a read means "keep THIS key's stored secret", which is
			// what SetSsoProvider does with an empty one.
			secret = ""
		}
		// Validate the provider NOW rather than letting a typo'd issuer surface as somebody's
		// failed first login. Discovery is refetched (force) and re-cached in the same call.
		if config.Mode == sso.OIDC {
			if _, err := s.ssoClient.Discover(config.DiscoveryURL, true); err != nil {
				return err
			}
		}
		if err := s.auth.SetSsoProvider(key, config, secret); err != nil {
			return err
		}
		// Drop the cache so a changed issuer takes effect on the next login, not in an hour.
		s.ssoClient.Forget()
		writeJSON(w, 200, jsonx.O("ok", true, "key", key, "config", config, "callbackUrl", s.ssoCallbackURL(r)))
		return nil
	case http.MethodDelete:
		return s.deleteSsoProvider(w, "")
	}
	writeError(w, 404, "not found")
	return nil
}

// deleteSsoProvider is DELETE /api/sso/config (key == "") and DELETE /api/sso/config/<key>. The
// links survive on purpose: those accounts keep their tokens and their history. The bare DELETE
// with exactly one provider removes it — what DELETE meant before keys existed — and with several
// refuses to guess, naming the choices.
func (s *Server) deleteSsoProvider(w http.ResponseWriter, key string) error {
	if key == "" {
		rows, err := s.auth.ListSsoProviders()
		if err != nil {
			return err
		}
		switch len(rows) {
		case 0:
			writeError(w, 404, "single sign-on was not configured")
			return nil
		case 1:
			key = rows[0].Key
		default:
			writeError(w, 400, "this host has several sign-in providers ("+ssoKeysOf(rows)+") — DELETE /api/sso/config/<key> to say which one")
			return nil
		}
	}
	s.ssoClient.Forget()
	deleted, err := s.auth.DeleteSsoProvider(key)
	if err != nil {
		return err
	}
	if !deleted {
		writeError(w, 404, `no sign-in provider "`+key+`" is configured`)
		return nil
	}
	writeJSON(w, 200, jsonx.O("ok", true))
	return nil
}
