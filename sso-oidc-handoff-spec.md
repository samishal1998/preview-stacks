# SSO / OIDC login — implementation spec

**Handoff note for the implementing agent:** this is deliberately small. It is four steps: build an authorize URL with a PKCE challenge, receive the code on a callback, exchange it for a token, fetch the user. Do not introduce a session framework, a plugin system, or a provider registry with lifecycle hooks. Match the existing codebase's language, HTTP client, config loading, and error conventions — do not add new dependencies unless there is no stdlib/existing option.

---

## 1. Goal

The service today has admin credentials plus a user-management panel where users are created manually with local credentials. Add a third option: the operator configures **their own** identity provider once, and anyone who can authenticate against it can sign in — no per-user setup.

The operator pastes in credentials from an OAuth/OIDC app they created in their own org (GitHub org, Google Workspace, Okta, etc.). We are the relying party. We never own their user directory.

## 2. Two configuration modes

### Mode A — OIDC (preferred)

Operator provides:

| Field | Required | Notes |
|---|---|---|
| `issuer` or `discoveryUrl` | yes | e.g. `https://accounts.google.com` — fetch `/.well-known/openid-configuration` |
| `clientId` | yes | |
| `clientSecret` | yes | |
| `scopes` | no | default `openid profile email` |

Everything else (authorize endpoint, token endpoint, userinfo endpoint, JWKS) comes from discovery. Fetch discovery once at config save time to validate, and cache it.

User identity comes from the ID token claims. Fall back to the userinfo endpoint only if a needed claim is absent.

### Mode B — OAuth 2.0

For providers without user-facing OIDC (GitHub is the motivating case).

| Field | Required | Notes |
|---|---|---|
| `provider` | yes | preset key, or `custom` |
| `clientId` | yes | |
| `clientSecret` | yes | |
| `authorizeUrl` | only if `custom` | |
| `tokenUrl` | only if `custom` | |
| `userInfoUrl` | no | if omitted, identity is taken from the token response |
| `scopes` | no | preset default, or operator-supplied |
| `claimMap` | no | see below |

**Preset dropdown** fills `authorizeUrl`, `tokenUrl`, `userInfoUrl`, default `scopes`, and `claimMap`. Ship with: GitHub, GitLab, Bitbucket, and `custom`. Adding a preset must be a single entry in a static table — no code changes.

**`claimMap`** maps the userinfo response onto our user shape. If absent, assume standard OIDC claim names:

```
subject  <- "sub"
username <- "preferred_username"
email    <- "email"
name     <- "name"
avatar   <- "picture"
```

GitHub's preset overrides this to `id`, `login`, `email`, `name`, `avatar_url`.

Values are flat key lookups on the JSON response. Do not build a JSONPath evaluator.

### A note on GitHub and OIDC

GitHub does publish an OIDC discovery document, but it is for Actions workload identity — signing job tokens for cloud providers. It is **not** a user login endpoint. GitHub user login is Mode B only.

## 3. Flow

1. **Start** (`GET /auth/sso/start`)
   - generate `state` (random, 32 bytes, base64url) and `code_verifier` (43–128 chars, base64url)
   - `code_challenge = base64url(sha256(code_verifier))`, method `S256`
   - store `{ codeVerifier, redirectAfterLogin }` under key `state` with a short TTL (default 5 minutes)
   - redirect to the authorize URL with `client_id`, `redirect_uri`, `response_type=code`, `scope`, `state`, `code_challenge`, `code_challenge_method`

2. **Callback** (`GET /auth/sso/callback?code=&state=`)
   - look up `state`; if missing or expired → fail. **Delete it immediately** (single use)
   - if the provider returned `error`, surface `error_description` and stop
   - POST to the token endpoint with `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`, `code_verifier`, and the client secret (form-encoded body; use HTTP Basic if the provider requires it — Mode A discovery advertises this via `token_endpoint_auth_methods_supported`)

3. **Identify**
   - Mode A: validate the ID token (signature against JWKS, `iss`, `aud`, `exp`) and read claims
   - Mode B: `GET userInfoUrl` with `Authorization: Bearer <access_token>`, apply `claimMap`
   - if no userinfo endpoint is configured, take `subject` from the token response and stop there

4. **Provision & sign in** — see §5. Issue the service's existing session/token. Nothing about the local session mechanism changes.

Send `Accept: application/json` on token requests (GitHub returns form-encoded otherwise).

## 4. Callback URL

Show the operator the exact callback URL, read-only and copyable, on the config screen:

```
{serviceBaseUrl}/auth/sso/callback
```

Single fixed path for all providers. It must match what they register on their side or the exchange fails. Derive `serviceBaseUrl` from existing config — do not guess from request headers.

## 5. User provisioning

On first successful login, create a local user. Key on **`(providerKey, subject)`**, not email — email changes, subjects don't.

- if a link for `(providerKey, subject)` exists → sign in as that user
- else create the user from the mapped claims, store the link, sign in
- if a local user already exists with the same email and no link → link them (log it)

Default posture: **anyone who successfully authenticates gets an account.** The provider already owns that policy — Google Workspace restricts to internal users, GitHub OAuth apps can be org-approved. We don't duplicate it.

Two optional knobs on the config:

- `allowedEmailDomains: string[]` — if non-empty, reject logins whose email is outside the list
- `defaultRole` — role assigned to auto-provisioned users, defaults to the lowest-privilege existing role

Admin credentials and manually-created local users keep working exactly as they do now. SSO is additive.

## 6. Transient storage

Only the PKCE verifier and state need storing, only for the duration of the round trip. One interface:

```ts
interface TransientStore {
  set(key: string, value: string, ttlSeconds: number): Promise<void>;
  get(key: string): Promise<string | null>;   // null if absent or expired
  delete(key: string): Promise<void>;
}
```

**Check what the service already wires up** — it likely has one or more of these already. Selection order:

1. **Redis** — native TTL, nothing else to do
2. **Postgres** — `expires_at` column; treat rows past it as absent, delete lazily on read
3. **SQLite** — same as Postgres
4. **In-memory map** — fallback, plus a timer sweep

Redis and Postgres are preferred over SQLite because SQLite doesn't survive going multi-instance. In-memory is functionally fine today (single instance), but implement behind the interface so scaling out is a config change, not a rewrite.

## 7. Config storage

The client secret is a secret — store it however the service stores its other secrets, and never return it from the config read endpoint (return a masked placeholder and only overwrite on non-empty submit).

## 8. Tests

- **Store:** one suite run against every backend — set/get round-trip, expired key reads as absent, delete removes.
- **PKCE:** challenge derivation matches a known-good vector.
- **Flow:** a fake provider (local HTTP handler) covering happy path, unknown state, replayed state, expired state, provider error response, and token exchange failure.
- **Claim mapping:** GitHub-shaped and OIDC-shaped userinfo payloads both produce the expected user.
- **Provisioning:** first login creates, second login links to the same user, changed email doesn't create a duplicate.

## 9. Explicitly out of scope

Refresh tokens, token introspection, SCIM, group/role sync from the provider, multiple simultaneous providers, SAML, dynamic client registration, back-channel logout. If any of these look necessary to finish the above, they aren't — flag it instead of building it.
