package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	v, err := omap.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustConfig(t *testing.T, s string) *Config {
	t.Helper()
	cfg, err := ParseConfig(parse(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func wantErr(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error matching %q", pattern)
	}
	if !IsError(err) {
		t.Fatalf("not an *sso.Error: %v", err)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("error %q does not match %q", err.Error(), pattern)
	}
}

// ── the transient store, one suite against every backend ──────────────────────────────────────────

func TestTransientStore(t *testing.T) {
	backends := []struct {
		name string
		make func(t *testing.T) TransientStore
	}{
		{"memory", func(t *testing.T) TransientStore { return NewMemoryTransientStore() }},
		{"sqlite", func(t *testing.T) TransientStore {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return &SqliteTransientStore{DB: s.DB}
		}},
	}
	for _, b := range backends {
		t.Run(b.name+": round-trips, expires, deletes, and takes exactly once", func(t *testing.T) {
			// negative control: make Take a plain Get → the second Take still returns value-d
			s := b.make(t)
			get := func(k string) (string, bool) {
				v, ok, err := s.Get(k)
				if err != nil {
					t.Fatal(err)
				}
				return v, ok
			}
			take := func(k string) (string, bool) {
				v, ok, err := s.Take(k)
				if err != nil {
					t.Fatal(err)
				}
				return v, ok
			}
			s.Set("a", "value-a", 60)
			if v, ok := get("a"); !ok || v != "value-a" {
				t.Fatalf("a: %q %v", v, ok)
			}
			// Expired reads as absent — not as a stale value, and not as an error.
			s.Set("b", "value-b", -1)
			if _, ok := get("b"); ok {
				t.Fatal("expired b read")
			}
			s.Set("c", "value-c", 60)
			s.Delete("c")
			if _, ok := get("c"); ok {
				t.Fatal("deleted c read")
			}
			// `take` is the single-use primitive the callback depends on.
			s.Set("d", "value-d", 60)
			if v, ok := take("d"); !ok || v != "value-d" {
				t.Fatalf("take d: %q %v", v, ok)
			}
			if _, ok := take("d"); ok {
				t.Fatal("d taken twice")
			}
			if _, ok := get("d"); ok {
				t.Fatal("d still readable")
			}
			// An expired row cannot be taken either.
			s.Set("e", "value-e", -1)
			if _, ok := take("e"); ok {
				t.Fatal("expired e taken")
			}
			if _, ok := get("never-written"); ok {
				t.Fatal("phantom")
			}
		})
	}

	t.Run("memory: Take is atomic under concurrency — exactly one winner", func(t *testing.T) {
		// negative control: drop the mutex from MemoryTransientStore.Take → -race reports, or two winners
		s := NewMemoryTransientStore()
		s.Set("k", "v", 60)
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := 0
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, ok, _ := s.Take("k"); ok {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if winners != 1 {
			t.Fatalf("%d winners", winners)
		}
	})
}

// ── PKCE ──────────────────────────────────────────────────────────────────────────────────────────

func TestPKCE(t *testing.T) {
	t.Run("S256 matches the RFC 7636 appendix B vector", func(t *testing.T) {
		// negative control: base64-standard instead of base64url → '+'/'/' appear and the vector differs
		if got := CodeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
			t.Fatalf("%s", got)
		}
	})

	t.Run("a generated verifier is in the legal length range and derives its own challenge", func(t *testing.T) {
		// negative control: RandomB64URL(16) in PKCE → the verifier is 22 chars, under 43
		verifier, challenge := PKCE()
		if len(verifier) < 43 || len(verifier) > 128 {
			t.Fatalf("len %d", len(verifier))
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(verifier) {
			t.Fatalf("alphabet %q", verifier)
		}
		if challenge != CodeChallenge(verifier) {
			t.Fatal("challenge")
		}
		if v2, _ := PKCE(); v2 == verifier {
			t.Fatal("not random")
		}
	})

	t.Run("redirectAfterLogin only ever survives as a same-origin path", func(t *testing.T) {
		// negative control: drop the `//` check → '//evil.example' survives
		if SafeNext("/deployments/pr-7") != "/deployments/pr-7" {
			t.Fatal("path")
		}
		// Every one of these is absolute to a browser.
		for _, bad := range []string{"//evil.example", "https://evil.example", `/\evil.example`, ""} {
			if SafeNext(bad) != "/" {
				t.Fatalf("%q survived", bad)
			}
		}
	})
}

// ── claim mapping ─────────────────────────────────────────────────────────────────────────────────

func TestClaimMapping(t *testing.T) {
	t.Run("a GitHub-shaped payload and an OIDC-shaped one both produce the expected user", func(t *testing.T) {
		// negative control: drop the ToLower on email → 'Alice@Example.com' comes back unchanged
		github := PresetFor("github")
		got, err := MapClaims(parse(t, `{"id":4242,"login":"octocat","email":"octo@example.com","name":"The Octocat","avatar_url":"https://x/y.png"}`), github.ClaimMap)
		if err != nil {
			t.Fatal(err)
		}
		want := &Identity{
			Subject:  "4242", // a numeric id is still an identity
			Username: "octocat",
			Email:    "octo@example.com",
			Name:     "The Octocat",
			Avatar:   "https://x/y.png",
			// GitHub never says, and nil is NOT permission to adopt an account
			Groups: []string{}, // never nil, and empty until something asks (routes_auth)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("github %+v", got)
		}
		got, _ = MapClaims(parse(t, `{"sub":"abc|123","preferred_username":"Alice","email":"Alice@Example.com","email_verified":true,"name":"Alice","picture":"https://x/a.png"}`), OIDCClaims)
		want = &Identity{
			Subject:       "abc|123",
			Username:      "Alice",
			Email:         "alice@example.com", // lower-cased, so the allow-list and the lookup agree
			Name:          "Alice",
			Avatar:        "https://x/a.png",
			EmailVerified: jsonx.Bool(true),
			Groups:        []string{},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("oidc %+v", got)
		}
		// The JSON shape carries emailVerified as null, not absent — and groups as [], not null.
		if b := string(jsonx.Must(MapClaimsMust(t, `{"sub":"x"}`))); !strings.HasSuffix(b, `"emailVerified":null,"groups":[]}`) {
			t.Fatalf("json %s", b)
		}
		// String booleans are honoured; anything else is "never said".
		if got, _ := MapClaims(parse(t, `{"sub":"x","email_verified":"false"}`), OIDCClaims); got.EmailVerified == nil || *got.EmailVerified {
			t.Fatal("'false'")
		}
		if got, _ := MapClaims(parse(t, `{"sub":"x","email_verified":1}`), OIDCClaims); got.EmailVerified != nil {
			t.Fatal("1")
		}
	})

	t.Run("one dotted path is supported (Bitbucket nests the avatar) and a missing subject is fatal", func(t *testing.T) {
		// negative control: drop the dotted-path branch in lookup → the avatar is empty
		bb := PresetFor("bitbucket")
		mapped, err := MapClaims(parse(t, `{"uuid":"{u-1}","username":"bb","display_name":"BB","links":{"avatar":{"href":"https://x/b.png"}}}`), bb.ClaimMap)
		if err != nil || mapped.Avatar != "https://x/b.png" || mapped.Subject != "{u-1}" {
			t.Fatalf("%+v %v", mapped, err)
		}
		_, err = MapClaims(parse(t, `{"login":"nope"}`), OIDCClaims)
		wantErr(t, err, `no "sub"`)
	})

	t.Run("the primary verified address is found in both shapes a provider serves", func(t *testing.T) {
		// negative control: ignore `is_primary` → the Bitbucket row is still found (first row) but flip the rows and it is not
		got := PrimaryEmailOf(parse(t, `[{"email":"a@x.com","primary":false,"verified":true},{"email":"b@x.com","primary":true,"verified":true}]`))
		if !reflect.DeepEqual(got, &PrimaryEmail{Email: "b@x.com", Verified: true}) {
			t.Fatalf("github %+v", got)
		}
		got = PrimaryEmailOf(parse(t, `{"values":[{"email":"x@x.com","is_primary":false},{"email":"C@X.com","is_primary":true,"is_confirmed":false}]}`))
		if !reflect.DeepEqual(got, &PrimaryEmail{Email: "c@x.com", Verified: false}) {
			t.Fatalf("bitbucket %+v", got)
		}
		if PrimaryEmailOf(parse(t, `[]`)) != nil || PrimaryEmailOf(nil) != nil {
			t.Fatal("empty")
		}
	})

	t.Run("the email allow-list fails CLOSED", func(t *testing.T) {
		// negative control: return true when `at < 0` → a list and no email passes
		cases := []struct {
			email   string
			domains []string
			want    bool
		}{
			{"", []string{}, true},               // empty list allows everything, including no email
			{"", []string{"example.com"}, false}, // a list and no email is a refusal
			{"a@example.com", []string{"example.com"}, true},
			{"a@sub.example.com", []string{"example.com"}, true},
			{"a@notexample.com", []string{"example.com"}, false},
			{"a@example.com.evil.net", []string{"example.com"}, false},
		}
		for _, c := range cases {
			if EmailAllowed(c.email, c.domains) != c.want {
				t.Fatalf("%q %v", c.email, c.domains)
			}
		}
	})

	t.Run("the username allow-list fails CLOSED, and `*` does not rescue a missing username", func(t *testing.T) {
		// negative control: drop the `username == ""` guard in UsernameAllowed → the {"*"} + "" case passes
		cases := []struct {
			username string
			patterns []string
			want     bool
		}{
			{"", []string{}, true},      // no rule allows everything, including no username
			{"", []string{"*"}, false},  // path.Match("*", "") is TRUE in Go — the guard is what refuses
			{"", []string{"a*"}, false}, // and a list with no username is a refusal either way
			{"octocat", []string{"octocat"}, true},
			{"OctoCat", []string{"octocat"}, true}, // GitHub logins are case-insensitive
			{"octocat", []string{"OCTOCAT"}, true}, // …so BOTH sides are folded
			{"octocat", []string{"octo*"}, true},   // globs
			{"octocat", []string{"oct?at"}, false}, // `?` is exactly ONE character, never a run
			{"octo-1", []string{"octo-[0-9]"}, true},
			{"octo-x", []string{"octo-[0-9]"}, false}, // a character class is more than */?
			{"mallory", []string{"octo*", "alice"}, false},
			{"alice", []string{"octo*", "alice"}, true}, // any-of
		}
		for _, c := range cases {
			if UsernameAllowed(c.username, c.patterns) != c.want {
				t.Fatalf("%q %v → %v", c.username, c.patterns, !c.want)
			}
		}
	})

	t.Run("group membership is any-of, exact and case-insensitive", func(t *testing.T) {
		// negative control: drop the ToLower on the member's name → the "Acme" case fails
		cases := []struct {
			groups   []string
			required []string
			want     bool
		}{
			{[]string{}, []string{}, true},          // no rule allows everything
			{[]string{}, []string{"acme"}, false},   // a rule and no groups is a refusal
			{[]string{""}, []string{"acme"}, false}, // and a blank name matches nothing
			{[]string{"Acme"}, []string{"acme"}, true},
			{[]string{"acme"}, []string{"other", "acme"}, true},
			{[]string{"acme-labs"}, []string{"acme"}, false}, // exact, not a prefix
			{[]string{"acme/backend"}, []string{"acme/backend"}, true},
			// NOT globbed: `*` is a literal group name here, which is why it matches nothing. If
			// group globbing is ever added, path.Match's `/` rule bites exactly this case.
			{[]string{"acme/backend"}, []string{"*"}, false},
		}
		for _, c := range cases {
			if GroupsAllowed(c.groups, c.required) != c.want {
				t.Fatalf("%v %v → %v", c.groups, c.required, !c.want)
			}
		}
	})

	t.Run("group names are read out of both response shapes, by the preset's key", func(t *testing.T) {
		// negative control: ignore the `key` argument and read "login" always → the GitLab case is empty
		gh := GroupNamesOf(parse(t, `[{"login":"acme","id":1},{"id":2},{"login":""},"junk"]`), PresetFor("github").GroupsKey)
		if !reflect.DeepEqual(gh, []string{"acme"}) { // an item with no name, and a non-object, are skipped
			t.Fatalf("github %v", gh)
		}
		gl := GroupNamesOf(parse(t, `[{"id":1,"name":"Backend","path":"backend","full_path":"acme/backend"}]`), PresetFor("gitlab").GroupsKey)
		if !reflect.DeepEqual(gl, []string{"acme/backend"}) { // the PATH, not the display name
			t.Fatalf("gitlab %v", gl)
		}
		if got := GroupNamesOf(parse(t, `{"values":[{"login":"a"}]}`), "login"); !reflect.DeepEqual(got, []string{"a"}) {
			t.Fatalf("values %v", got)
		}
		// Never nil (Go rule 3), and no key means nothing can be read.
		for _, got := range [][]string{GroupNamesOf(parse(t, `[]`), "login"), GroupNamesOf(nil, "login"), GroupNamesOf(parse(t, `[{"login":"a"}]`), "")} {
			if got == nil || len(got) != 0 {
				t.Fatalf("empty %v", got)
			}
		}
	})

	t.Run("a username is sanitized into the local shape, and is never the identity", func(t *testing.T) {
		// negative control: drop the leadingJunk strip → '!!' yields '--', not 'user-sub42'
		cases := [][3]string{
			{"Octo Cat", "s1", "octo-cat"},
			{"alice@example.com", "s1", "alice-example.com"},
			{"!!", "sub-42", "user-sub42"},
			{"", "x", "user-x"},
		}
		for _, c := range cases {
			if got := SanitizeUsername(c[0], c[1]); got != c[2] {
				t.Fatalf("%q → %q, want %q", c[0], got, c[2])
			}
		}
		if got := SanitizeUsername(strings.Repeat("A", 60), "s"); len(got) != 32 {
			t.Fatalf("len %d", len(got))
		}
	})
}

// MapClaimsMust is MapClaims for a payload that must map.
func MapClaimsMust(t *testing.T, payload string) *Identity {
	t.Helper()
	id, err := MapClaims(parse(t, payload), OIDCClaims)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ── configuration ─────────────────────────────────────────────────────────────────────────────────

func TestConfiguration(t *testing.T) {
	t.Run("a preset fills the endpoints in and the operator can still override one", func(t *testing.T) {
		// negative control: let the preset win over a typed authorizeUrl → the self-hosted URL is gitlab.com's
		cfg := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"cid"}`)
		if cfg.AuthorizeURL != "https://github.com/login/oauth/authorize" || cfg.UserInfoURL != "https://api.github.com/user" || cfg.Scopes != "read:user user:email" || cfg.ClaimMap.Subject != "id" || cfg.Label != "GitHub" {
			t.Fatalf("%+v", cfg)
		}
		selfHosted := mustConfig(t, `{"mode":"oauth2","provider":"gitlab","clientId":"cid","authorizeUrl":"https://git.corp.example/oauth/authorize","tokenUrl":"https://git.corp.example/oauth/token","userInfoUrl":"https://git.corp.example/api/v4/user"}`)
		if selfHosted.AuthorizeURL != "https://git.corp.example/oauth/authorize" {
			t.Fatalf("%+v", selfHosted)
		}
		if selfHosted.ClaimMap.Username != "username" { // the preset's mapping survives
			t.Fatalf("%+v", selfHosted.ClaimMap)
		}
	})

	t.Run("a preset's emails endpoint is never inherited by a self-hosted userinfo endpoint", func(t *testing.T) {
		// negative control: inherit preset.EmailsURL unconditionally → the GHE case carries api.github.com
		// GitHub keeps the address off the profile, so the preset carries a second URL...
		if got := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c"}`).EmailsURL; got != "https://api.github.com/user/emails" {
			t.Fatalf("%q", got)
		}
		// ...but pointing userinfo at your own host must NOT then send that host's access token to
		// api.github.com. Inheriting it here would leak a live credential to a third party.
		if got := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c","userInfoUrl":"https://ghe.corp.example/api/v3/user"}`).EmailsURL; got != "" {
			t.Fatalf("%q", got)
		}
		if got := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c","userInfoUrl":"https://ghe.corp.example/api/v3/user","emailsUrl":"https://ghe.corp.example/api/v3/user/emails"}`).EmailsURL; got != "https://ghe.corp.example/api/v3/user/emails" {
			t.Fatalf("%q", got)
		}
	})

	t.Run("the preset's groups endpoint follows the same inheritance rule as its emails endpoint", func(t *testing.T) {
		// negative control: inherit preset.GroupsURL unconditionally → the self-hosted case carries gitlab.com
		if got := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c"}`).GroupsURL; got != "https://api.github.com/user/orgs" {
			t.Fatalf("%q", got)
		}
		// A self-hosted userinfo endpoint means this is NOT gitlab.com, and sending its access token
		// there to ask about groups would hand a third party a live credential.
		selfHosted := `{"mode":"oauth2","provider":"gitlab","clientId":"c","authorizeUrl":"https://git.corp.example/oauth/authorize","tokenUrl":"https://git.corp.example/oauth/token","userInfoUrl":"https://git.corp.example/api/v4/user"`
		if got := mustConfig(t, selfHosted+`}`).GroupsURL; got != "" {
			t.Fatalf("%q", got)
		}
		typed := mustConfig(t, selfHosted+`,"groupsUrl":"https://git.corp.example/api/v4/groups","requiredGroups":["Acme/Backend"],"scopes":"read_user read_api"}`)
		if typed.GroupsURL != "https://git.corp.example/api/v4/groups" {
			t.Fatalf("%q", typed.GroupsURL)
		}
		// The lists are normalised the way the domain list is: trimmed, lowercased, empties dropped.
		if !reflect.DeepEqual(typed.RequiredGroups, []string{"acme/backend"}) {
			t.Fatalf("%v", typed.RequiredGroups)
		}
		if got := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c","allowedUsernames":[" Octo* ","","QA-[0-9]*"]}`).AllowedUsernames; !reflect.DeepEqual(got, []string{"octo*", "qa-[0-9]*"}) {
			t.Fatalf("%v", got)
		}
	})

	t.Run("a group rule the token could not read is refused AT SAVE, naming the scope", func(t *testing.T) {
		// negative control: drop the hasScope check → the github-without-read:org case saves and every login then refuses
		// The github preset's own scopes do not include read:org, so requiring a group with them is
		// a config that would refuse every login instead of a sign-in that works.
		_, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"github","clientId":"c","requiredGroups":["acme"]}`))
		wantErr(t, err, `requiredGroups needs the "read:org" scope`)
		if err.Error() != `requiredGroups needs the "read:org" scope to read https://api.github.com/user/orgs (or any of: read:org, user, write:org, admin:org) — add it to scopes (currently "read:user user:email")` {
			t.Fatalf("%q", err.Error())
		}
		// The preset's own default scopes are `read:user user:email`. Neither token IS `user`, so the
		// refusal above is not an accident of substring matching — it is the real state of that config.
		// Adding it is what the sentence asks for, and then it saves.
		ok := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c","requiredGroups":["Acme"],"scopes":"read:user user:email read:org"}`)
		if !reflect.DeepEqual(ok.RequiredGroups, []string{"acme"}) || ok.Scopes != "read:user user:email read:org" {
			t.Fatalf("%+v", ok)
		}
		// A comma-separated paste is the same set of scopes.
		if _, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"github","clientId":"c","requiredGroups":["acme"],"scopes":"read:user,read:org"}`)); err != nil {
			t.Fatal(err)
		}
		// GitHub documents `user` as granting this read too, so a config naming it must SAVE. The check
		// asks what the provider accepts, not what this table happens to recommend first.
		// negative control: make hasAnyScope compare only GroupsScopes[0] → this save is refused.
		if _, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"github","clientId":"c","requiredGroups":["acme"],"scopes":"user user:email"}`)); err != nil {
			t.Fatalf("`user` is sufficient per GitHub's own docs, and was refused: %v", err)
		}
		// A scope that merely CONTAINS the string is not the scope.
		_, err = ParseConfig(parse(t, `{"mode":"oauth2","provider":"github","clientId":"c","requiredGroups":["acme"],"scopes":"read:organization"}`))
		wantErr(t, err, `needs the "read:org" scope`)
		// GitLab's read_user is the /user endpoint only; groups need read_api.
		_, err = ParseConfig(parse(t, `{"mode":"oauth2","provider":"gitlab","clientId":"c","requiredGroups":["acme"]}`))
		wantErr(t, err, `requiredGroups needs the "read_api" scope`)
	})

	t.Run("a group rule with nothing to read it with is refused too", func(t *testing.T) {
		// negative control: scope the requiredGroups block to `preset != nil` → the custom case is accepted and every login then refuses
		// No preset ⇒ nothing says which field of the response names a group.
		_, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"custom","clientId":"c","authorizeUrl":"https://x.test/a","tokenUrl":"https://x.test/t","groupsUrl":"https://x.test/groups","requiredGroups":["acme"]}`))
		wantErr(t, err, `requiredGroups is not supported for provider "custom"`)
		// A self-hosted GitLab did not inherit gitlab.com's endpoint and did not type one.
		_, err = ParseConfig(parse(t, `{"mode":"oauth2","provider":"gitlab","clientId":"c","userInfoUrl":"https://git.corp.example/api/v4/user","requiredGroups":["acme"],"scopes":"read_api"}`))
		wantErr(t, err, `requiredGroups needs a groups endpoint — set groupsUrl`)
		// An OIDC issuer declares no groups endpoint in its discovery document and this host maps no
		// groups claim, so a group rule there could only ever fail every login.
		_, err = ParseConfig(parse(t, `{"mode":"oidc","clientId":"c","issuer":"https://accounts.google.com","requiredGroups":["acme"]}`))
		wantErr(t, err, `requiredGroups needs a provider whose groups endpoint this host knows`)
		// And a groups endpoint is held to the same https rule every other endpoint is.
		_, err = ParseConfig(parse(t, `{"mode":"oauth2","provider":"gitlab","clientId":"c","groupsUrl":"http://git.corp.example/api/v4/groups"}`))
		wantErr(t, err, `groupsUrl must be https`)
	})

	t.Run("a malformed username glob is refused while a human is looking at the form", func(t *testing.T) {
		// negative control: drop the path.Match probe loop → "qa-[0-9" is stored and silently matches nobody
		_, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"github","clientId":"c","allowedUsernames":["ok*","qa-[0-9"]}`))
		wantErr(t, err, `allowedUsernames entry "qa-\[0-9" is not a valid pattern`)
	})

	t.Run("what it refuses", func(t *testing.T) {
		// negative control: allow http off-loopback in httpsURL → the 'http://idp.example.com' case passes
		refuse := func(input, pattern string) {
			t.Helper()
			_, err := ParseConfig(parse(t, input))
			wantErr(t, err, pattern)
		}
		refuse(`{"mode":"oauth2","provider":"custom","clientId":"c"}`, `authorizeUrl and tokenUrl`)
		refuse(`{"mode":"oidc"}`, `clientId`)
		refuse(`{"mode":"oidc","clientId":"c"}`, `issuer or discoveryUrl`)
		refuse(`{"mode":"saml","clientId":"c"}`, `mode must be`)
		refuse(`{"mode":"oidc","clientId":"c","issuer":"http://idp.example.com"}`, `https`)
		refuse(`{"mode":"oauth2","provider":"nope","clientId":"c"}`, `unknown provider`)
		refuse(`{"mode":"oidc","clientId":"c","issuer":"not a url"}`, `must be an absolute URL`)
		// http IS allowed on loopback, or nothing could be developed against a local IdP.
		if got := mustConfig(t, `{"mode":"oidc","clientId":"c","issuer":"http://127.0.0.1:9999"}`).DiscoveryURL; !strings.Contains(got, "127.0.0.1") {
			t.Fatalf("%q", got)
		}
		// The exact texts.
		_, err := ParseConfig(parse(t, `{"mode":"oauth2","provider":"nope","clientId":"c"}`))
		if err.Error() != `unknown provider "nope" — use one of github, gitlab, bitbucket, google, microsoft, okta, auth0, keycloak, or custom` {
			t.Fatalf("%q", err.Error())
		}
		// An unknown provider is refused whatever the mode — including no mode at all, which used
		// to fall into the oidc branch and complain about a missing issuer instead.
		_, err = ParseConfig(parse(t, `{"provider":"nope","clientId":"c"}`))
		wantErr(t, err, `unknown provider "nope"`)
		_, err = ParseConfig(parse(t, `{"mode":"oidc","clientId":"c","issuer":"http://idp.example.com"}`))
		if err.Error() != `issuer/discoveryUrl must be https (http is only accepted on localhost) — got "http://idp.example.com"` {
			t.Fatalf("%q", err.Error())
		}
	})

	t.Run("every preset row is fully enriched: mode, button, setup, and its mode's endpoints", func(t *testing.T) {
		// negative control: blank google's DiscoveryURL (or github's SetupHint) → this fails naming the row
		for _, p := range Presets {
			if p.Mode != OIDC && p.Mode != OAuth2 {
				t.Fatalf("%s: mode %q", p.Key, p.Mode)
			}
			if p.ButtonLabel == "" || p.SetupURL == "" || p.SetupHint == "" {
				t.Fatalf("%s: missing button/setup enrichment: %+v", p.Key, p)
			}
			// The hint is the form's walkthrough; the one thing every provider setup shares is
			// pasting the callback URL in as the redirect URI, so every hint must say so.
			if !strings.Contains(strings.ToLower(p.SetupHint), "callback url") {
				t.Fatalf("%s: the setup hint never mentions the callback URL: %q", p.Key, p.SetupHint)
			}
			switch p.Mode {
			case OIDC:
				if p.DiscoveryURL == "" || p.AuthorizeURL != "" || p.TokenURL != "" {
					t.Fatalf("%s: an oidc preset carries a DiscoveryURL and no endpoints: %+v", p.Key, p)
				}
			case OAuth2:
				if p.DiscoveryURL != "" || p.AuthorizeURL == "" || p.TokenURL == "" {
					t.Fatalf("%s: an oauth2 preset carries endpoints and no DiscoveryURL: %+v", p.Key, p)
				}
			}
		}
		// The templates are exactly the tenant-specific providers; google is a real issuer.
		for _, c := range []struct {
			key      string
			template bool
		}{{"google", false}, {"microsoft", true}, {"okta", true}, {"auth0", true}, {"keycloak", true}} {
			if got := strings.Contains(PresetFor(c.key).DiscoveryURL, "<"); got != c.template {
				t.Fatalf("%s: template=%v, want %v (%q)", c.key, got, c.template, PresetFor(c.key).DiscoveryURL)
			}
		}
	})

	t.Run("an oidc preset fills the issuer in, a template placeholder is refused until replaced", func(t *testing.T) {
		// negative control: drop the strings.Contains(discoveryURL, "<") refusal → the okta template
		// reaches httpsURL and the error stops mentioning the placeholder
		cfg := mustConfig(t, `{"provider":"google","clientId":"cid"}`)
		if cfg.Mode != OIDC || cfg.DiscoveryURL != "https://accounts.google.com/" || cfg.Label != "Google" || cfg.Scopes != "openid email profile" || cfg.Provider != "google" {
			t.Fatalf("%+v", cfg)
		}
		if cfg.DerivedKey() != "google" {
			t.Fatalf("derived %q", cfg.DerivedKey())
		}
		// A template saved verbatim would fail every login at discovery; refuse it at the form.
		_, err := ParseConfig(parse(t, `{"provider":"okta","clientId":"cid"}`))
		wantErr(t, err, `still carries a <placeholder>`)
		// The same rule reads a typed URL: the operator pasted the template without replacing it.
		_, err = ParseConfig(parse(t, `{"mode":"oidc","clientId":"cid","discoveryUrl":"https://login.microsoftonline.com/<tenant-id>/v2.0"}`))
		wantErr(t, err, `still carries a <placeholder>`)
		// Replaced, it parses — the typed URL wins over the preset's template.
		ok := mustConfig(t, `{"provider":"okta","clientId":"cid","discoveryUrl":"https://acme.okta.com/oauth2/default"}`)
		if ok.DiscoveryURL != "https://acme.okta.com/oauth2/default" || ok.Label != "Okta" || ok.Provider != "okta" {
			t.Fatalf("%+v", ok)
		}
		// A config with no preset is exactly what it was before presets learned oidc.
		bare := mustConfig(t, `{"mode":"oidc","clientId":"c","issuer":"https://accounts.example.com"}`)
		if bare.Provider != "" || bare.Label != "accounts.example.com" || bare.Scopes != "openid profile email" || bare.DerivedKey() != "oidc" {
			t.Fatalf("%+v", bare)
		}
	})

	t.Run("a preset's mode is authoritative: it fills an absent mode and refuses a contradicting one", func(t *testing.T) {
		// negative control: drop the preset/mode mismatch refusal → github-under-oidc parses (the documented trap)
		if got := mustConfig(t, `{"provider":"github","clientId":"c"}`); got.Mode != OAuth2 || got.AuthorizeURL == "" {
			t.Fatalf("%+v", got)
		}
		_, err := ParseConfig(parse(t, `{"mode":"oidc","provider":"github","clientId":"c","issuer":"https://token.actions.githubusercontent.com"}`))
		wantErr(t, err, `provider "github" is an oauth2 preset`)
		_, err = ParseConfig(parse(t, `{"mode":"oauth2","provider":"google","clientId":"c"}`))
		wantErr(t, err, `provider "google" is an oidc preset`)
	})

	t.Run("claim overrides merge onto the preset, and email domains are normalized", func(t *testing.T) {
		// negative control: skip the `@` strip → '@Example.COM' keeps its @
		cfg := mustConfig(t, `{"mode":"oauth2","provider":"github","clientId":"c","claimMap":{"email":"work_email"},"allowedEmailDomains":["@Example.COM","","other.test"]}`)
		if !reflect.DeepEqual(cfg.ClaimMap, ClaimMap{Subject: "id", Username: "login", Email: "work_email", Name: "name", Avatar: "avatar_url"}) {
			t.Fatalf("%+v", cfg.ClaimMap)
		}
		if !reflect.DeepEqual(cfg.AllowedEmailDomains, []string{"example.com", "other.test"}) {
			t.Fatalf("%v", cfg.AllowedEmailDomains)
		}
	})

	t.Run("the stored JSON is the full normalised shape, WHATWG-serialised, in the TypeScript key order", func(t *testing.T) {
		// negative control: return u.String() instead of whatwgString → the issuer loses its trailing '/'
		// This is the `config` the golden fixture's GET /api/sso/config carries (FIXTURE.json googleExpected).
		cfg := mustConfig(t, `{"mode":"oidc","clientId":"google-cid","issuer":"https://accounts.google.com","label":"Google"}`)
		want := `{"mode":"oidc","enabled":true,"clientId":"google-cid","allowedEmailDomains":[],"allowedUsernames":[],"requiredGroups":[],"defaultRole":"admin","label":"Google","discoveryUrl":"https://accounts.google.com/","provider":"","authorizeUrl":"","tokenUrl":"","userInfoUrl":"","emailsUrl":"","groupsUrl":"","scopes":"openid profile email","claimMap":{"subject":"sub","username":"preferred_username","email":"email","name":"name","avatar":"picture"}}`
		if got := string(jsonx.Must(cfg)); got != want {
			t.Fatalf("\n got %s\nwant %s", got, want)
		}
		// A label is derived from the hostname when absent; the default port and case are normalised.
		cfg = mustConfig(t, `{"mode":"oidc","clientId":"c","issuer":"HTTPS://IdP.Example.com:443/tenant","enabled":false}`)
		if cfg.Label != "idp.example.com" || cfg.DiscoveryURL != "https://idp.example.com/tenant" || cfg.Enabled {
			t.Fatalf("%+v", cfg)
		}
		if got := mustConfig(t, `{"mode":"oidc","clientId":"c","issuer":"http://[::1]:9000"}`); got.Label != "[::1]" || got.DiscoveryURL != "http://[::1]:9000/" {
			t.Fatalf("%+v", got)
		}
		// Re-parsing its own output is a fixed point.
		again := mustConfig(t, string(jsonx.Must(cfg)))
		if !reflect.DeepEqual(again, cfg) {
			t.Fatalf("not a fixed point: %+v", again)
		}
	})
}

// ── the HTTP steps: authorize URL, token exchange, discovery ──────────────────────────────────────

func TestHTTPSteps(t *testing.T) {
	t.Run("the authorize URL preserves the tenant's query and appends ours in order, form-encoded", func(t *testing.T) {
		// negative control: encode with url.Values → the keys come out sorted
		cfg := mustConfig(t, `{"mode":"oauth2","provider":"custom","clientId":"c id","authorizeUrl":"https://idp.example/auth?tenant=t1&state=old","tokenUrl":"https://idp.example/token","scopes":"openid a*b~c"}`)
		got, err := AuthorizeURL(cfg, Endpoints{AuthorizeURL: cfg.AuthorizeURL}, AuthorizeArgs{RedirectURI: "https://me.example/api/auth/sso/callback", State: "st", Challenge: "ch"})
		if err != nil {
			t.Fatal(err)
		}
		want := "https://idp.example/auth?tenant=t1&state=st&response_type=code&client_id=c+id&redirect_uri=https%3A%2F%2Fme.example%2Fapi%2Fauth%2Fsso%2Fcallback&scope=openid+a*b%7Ec&code_challenge=ch&code_challenge_method=S256"
		if got != want {
			t.Fatalf("\n got %s\nwant %s", got, want)
		}
		// No scopes ⇒ no scope parameter.
		cfg.Scopes = ""
		got, _ = AuthorizeURL(cfg, Endpoints{AuthorizeURL: "https://idp.example/auth"}, AuthorizeArgs{RedirectURI: "r", State: "s", Challenge: "c"})
		if got != "https://idp.example/auth?response_type=code&client_id=c+id&redirect_uri=r&state=s&code_challenge=c&code_challenge_method=S256" {
			t.Fatalf("%s", got)
		}
	})

	t.Run("the token exchange: body in order, Basic credential percent-encoded on both halves, errors by shape", func(t *testing.T) {
		// negative control: build the Basic credential without EncodeURIComponent → the provider's decode check fails
		var gotBody, gotAuth, gotAccept string
		var status int
		var reply string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := readAll(r)
			gotBody, gotAuth, gotAccept = b, r.Header.Get("Authorization"), r.Header.Get("Accept")
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(status)
			w.Write([]byte(reply))
		}))
		defer srv.Close()
		c := NewClient(nil)
		cfg := mustConfig(t, `{"mode":"oauth2","provider":"custom","clientId":"c id","authorizeUrl":"https://idp.example/auth","tokenUrl":"`+srv.URL+`/token"}`)
		// Loopback http is accepted by the config, and the token URL must be the normalised one.
		if cfg.TokenURL != srv.URL+"/token" {
			t.Fatalf("%q", cfg.TokenURL)
		}
		args := ExchangeArgs{Code: "the code", RedirectURI: "https://me/cb", Verifier: "ver", ClientSecret: "s3cret/with space+plus"}

		status, reply = 200, `{"access_token":"at-1","token_type":"bearer"}`
		tok, err := c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL, TokenAuth: Post}, args)
		if err != nil || tok.GetString("access_token") != "at-1" {
			t.Fatalf("%v %v", tok, err)
		}
		if gotBody != "grant_type=authorization_code&code=the+code&redirect_uri=https%3A%2F%2Fme%2Fcb&client_id=c+id&code_verifier=ver&client_secret=s3cret%2Fwith+space%2Bplus" {
			t.Fatalf("body %s", gotBody)
		}
		if gotAuth != "" || gotAccept != "application/json" {
			t.Fatalf("headers %q %q", gotAuth, gotAccept)
		}

		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL, TokenAuth: Basic}, args)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(gotBody, "client_secret") {
			t.Fatalf("secret sent in the body too: %s", gotBody)
		}
		if gotAuth != "Basic "+js.B64([]byte("c%20id:s3cret%2Fwith%20space%2Bplus")) {
			t.Fatalf("auth %q", gotAuth)
		}

		// GitHub's shape: a 200 carrying an error.
		reply = `{"error":"bad_verification_code","error_description":"the code is expired"}`
		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL}, args)
		wantErr(t, err, `^the token exchange failed: bad_verification_code: the code is expired$`)
		// A non-2xx with a described error, and one without.
		status, reply = 401, `{"error":"invalid_client"}`
		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL}, args)
		wantErr(t, err, `^the token exchange failed \(401\): invalid_client$`)
		status, reply = 500, "<html>boom"
		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL}, args)
		wantErr(t, err, `^the token exchange failed \(500\): <html>boom$`)
		// A form-encoded 200 (a provider that ignored Accept) still parses.
		status, reply = 200, "access_token=at-2&scope=read"
		tok, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL}, args)
		if err != nil || tok.GetString("access_token") != "at-2" {
			t.Fatalf("%v %v", tok, err)
		}
		reply = `{"token_type":"bearer"}`
		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: cfg.TokenURL}, args)
		wantErr(t, err, `neither an access token nor an id token`)
		// Unreachable.
		_, err = c.ExchangeCode(cfg, Endpoints{TokenURL: "http://127.0.0.1:1/token"}, args)
		wantErr(t, err, `^the token endpoint was unreachable: `)
	})

	t.Run("discovery: validated, cached, Basic only when it is the sole method, and forgettable", func(t *testing.T) {
		// negative control: pick Basic whenever it is listed → the two-method document yields Basic
		hits := 0
		methods := `["client_secret_post","client_secret_basic"]`
		doc := func(base string) string {
			return `{"issuer":"` + base + `","authorization_endpoint":"` + base + `/authorize","token_endpoint":"` + base + `/token","userinfo_endpoint":"` + base + `/userinfo","jwks_uri":"` + base + `/jwks","token_endpoint_auth_methods_supported":` + methods + `}`
		}
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if r.URL.Path != "/.well-known/openid-configuration" || r.Header.Get("Accept") != "application/json" {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(doc(srv.URL)))
		}))
		defer srv.Close()
		c := NewClient(nil)
		ep, err := c.Discover(srv.URL+"/", false)
		if err != nil || ep.Issuer != srv.URL || ep.TokenAuth != Post || ep.JwksURI != srv.URL+"/jwks" || ep.AuthorizeURL != srv.URL+"/authorize" {
			t.Fatalf("%+v %v", ep, err)
		}
		if _, err := c.Discover(srv.URL+"/", false); err != nil || hits != 1 {
			t.Fatalf("cached: %d %v", hits, err)
		}
		methods = `["client_secret_basic"]`
		if ep, _ := c.Discover(srv.URL+"/", true); ep.TokenAuth != Basic || hits != 2 {
			t.Fatalf("force: %+v %d", ep, hits)
		}
		c.Forget(srv.URL + "/")
		if _, err := c.Discover(srv.URL+"/", false); err != nil || hits != 3 {
			t.Fatalf("forgotten: %d %v", hits, err)
		}
		// EndpointsFor routes by mode.
		cfg := mustConfig(t, `{"mode":"oidc","clientId":"c","issuer":"`+srv.URL+`"}`)
		if ep, err := c.EndpointsFor(cfg); err != nil || ep.Issuer != srv.URL {
			t.Fatalf("%+v %v", ep, err)
		}
		// The failures, with their texts.
		_, err = c.Discover(srv.URL+"/nope", false)
		wantErr(t, err, `^`+regexp.QuoteMeta(srv.URL+"/nope/.well-known/openid-configuration answered 404 — is the issuer right?")+`$`)
		_, err = c.Discover("http://127.0.0.1:1", false)
		wantErr(t, err, `^could not reach http://127.0.0.1:1/.well-known/openid-configuration: `)
		methods = `[]`
		doc = func(string) string { return `{"issuer":"x"}` }
		c.Forget()
		_, err = c.Discover(srv.URL, false)
		wantErr(t, err, `has no authorization_endpoint$`)
		doc = func(string) string { return `not json` }
		_, err = c.Discover(srv.URL, false)
		wantErr(t, err, `did not return JSON$`)
	})

	t.Run("fetchJSON sends the bearer, an Accept and the User-Agent GitHub insists on", func(t *testing.T) {
		// negative control: drop the user-agent header → the handler answers 403
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("User-Agent") != "pstack" || r.Header.Get("Authorization") != "Bearer at-1" || r.Header.Get("Accept") != "application/json" {
				w.WriteHeader(403)
				return
			}
			w.Write([]byte(`{"login":"octocat"}`))
		}))
		defer srv.Close()
		c := NewClient(nil)
		v, err := c.FetchJSON(srv.URL+"/user", "at-1")
		if err != nil || v.(*omap.Map).GetString("login") != "octocat" {
			t.Fatalf("%v %v", v, err)
		}
		_, err = c.FetchJSON(srv.URL+"/user", "wrong")
		wantErr(t, err, `^`+regexp.QuoteMeta(srv.URL+"/user answered 403")+`$`)
	})

	t.Run("the client follows redirects and has a timeout", func(t *testing.T) {
		// negative control: set CheckRedirect = ErrUseLastResponse on the default client → the 302 is answered as a 302
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/old" {
				http.Redirect(w, r, "/new", 302)
				return
			}
			w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()
		c := NewClient(nil)
		if c.HTTP.Timeout != 15*time.Second {
			t.Fatalf("timeout %v", c.HTTP.Timeout)
		}
		v, err := c.FetchJSON(srv.URL+"/old", "t")
		if err != nil || v.(*omap.Map).Len() != 1 {
			t.Fatalf("%v %v", v, err)
		}
	})
}

func readAll(r *http.Request) (string, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String(), nil
		}
	}
}

// ── id token verification ─────────────────────────────────────────────────────────────────────────

// signer is an RS256 keypair, its JWKS entry, and a signer — the id-token half of mode A, for real.
type signer struct {
	jwk  string // the JSON of the JWKS entry
	sign func(claims string, header string) string
}

func rsaSigner(t *testing.T, kid string) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jsonx.Object{
		{K: "kty", V: "RSA"}, {K: "n", V: js.B64URL(key.N.Bytes())}, {K: "e", V: js.B64URL(big.NewInt(int64(key.E)).Bytes())},
		{K: "kid", V: kid}, {K: "alg", V: "RS256"}, {K: "use", V: "sig"},
	}
	return signer{
		jwk: string(jsonx.Must(jwk)),
		sign: func(claims, header string) string {
			if header == "" {
				header = `{"alg":"RS256","typ":"JWT","kid":"` + kid + `"}`
			}
			h, b := js.B64URL([]byte(header)), js.B64URL([]byte(claims))
			sum := sha256.Sum256([]byte(h + "." + b))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
			if err != nil {
				t.Fatal(err)
			}
			return h + "." + b + "." + js.B64URL(sig)
		},
	}
}

// jwksServer serves `{keys: [...]}` from a mutable list and counts hits.
type jwksServer struct {
	*httptest.Server
	mu   sync.Mutex
	keys []string
	hits int
}

func newJwksServer(t *testing.T) *jwksServer {
	s := &jwksServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/jwks" {
			w.WriteHeader(404)
			return
		}
		s.hits++
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"keys":[` + strings.Join(s.keys, ",") + `]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) set(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func (s *jwksServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func TestIDToken(t *testing.T) {
	t.Run("a well-formed token verifies; every defect is fatal", func(t *testing.T) {
		// negative control: skip the aud check → the 'someone-else' audience verifies
		p := newJwksServer(t)
		s := rsaSigner(t, "k1")
		p.set(s.jwk)
		c := NewClient(nil)
		args := VerifyArgs{Issuer: p.URL, ClientID: "cid", JwksURI: p.URL + "/jwks"}
		now := time.Now().Unix()
		claims := func(over string) string {
			// `{ ...good, ...over }` spelled as JSON: the override keys replace the base ones.
			base := omap.From("iss", p.URL, "aud", "cid", "sub", "u1", "exp", now+300, "iat", now)
			if over != "" {
				o := parse(t, over).(*omap.Map)
				o.Each(func(k string, v any) {
					if v == nil {
						base.Delete(k) // `exp: undefined`
					} else {
						base.Set(k, v)
					}
				})
			}
			return string(jsonx.Must(base))
		}
		verify := func(token string) (*omap.Map, error) { return c.VerifyIDToken(token, args) }

		got, err := verify(s.sign(claims(""), ""))
		if err != nil || got.GetString("sub") != "u1" {
			t.Fatalf("%v %v", got, err)
		}
		// aud may be an ARRAY, and must contain the client id.
		if got, err := verify(s.sign(claims(`{"aud":["other","cid"]}`), "")); err != nil || got.GetString("sub") != "u1" {
			t.Fatalf("array aud: %v", err)
		}
		_, err = verify(s.sign(claims(`{"aud":"someone-else"}`), ""))
		wantErr(t, err, `audience`)
		_, err = verify(s.sign(claims(`{"aud":["a","b"]}`), ""))
		wantErr(t, err, `audience`)
		_, err = verify(s.sign(claims(`{"iss":"https://evil.example"}`), ""))
		wantErr(t, err, `^the id token issuer is "https://evil.example", expected "`+regexp.QuoteMeta(p.URL)+`"$`)
		_, err = verify(s.sign(claims(`{"exp":`+js.NumberString(float64(now-120))+`}`), ""))
		wantErr(t, err, `expired`)
		_, err = verify(s.sign(claims(`{"exp":null}`), ""))
		wantErr(t, err, `expired`)
		_, err = verify(s.sign(claims(`{"nbf":`+js.NumberString(float64(now+600))+`}`), ""))
		wantErr(t, err, `not valid yet`)
		// 60s of skew is tolerated — a provider's clock is not this one's.
		if got, err := verify(s.sign(claims(`{"exp":`+js.NumberString(float64(now-30))+`}`), "")); err != nil || got.GetString("sub") != "u1" {
			t.Fatalf("skew: %v", err)
		}

		// THE FORGERIES. `none` and HMAC are refused before a key is even looked up.
		body := js.B64URL([]byte(claims("")))
		noneHead := js.B64URL([]byte(`{"alg":"none","typ":"JWT"}`))
		_, err = verify(noneHead + "." + body + ".")
		wantErr(t, err, `^id token algorithm "none" is not accepted — only RS256 and ES256 are$`)
		hsHead := js.B64URL([]byte(`{"alg":"HS256","typ":"JWT"}`))
		_, err = verify(hsHead + "." + body + ".aaaa")
		wantErr(t, err, `^id token algorithm "HS256" is not accepted`)
		if p.count() != 1 {
			t.Fatalf("a forgery reached the JWKS: %d fetches", p.count())
		}

		// A body swapped after signing does not verify.
		token := s.sign(claims(""), "")
		parts := strings.Split(token, ".")
		forged := js.B64URL([]byte(claims(`{"sub":"admin"}`)))
		_, err = verify(parts[0] + "." + forged + "." + parts[2])
		wantErr(t, err, `did not verify`)

		_, err = verify("not-a-jwt")
		wantErr(t, err, `three dot-separated`)
		_, err = verify("!!!.!!!.!!!")
		wantErr(t, err, `not a readable JWT`)
	})

	t.Run("an unknown kid refetches the JWKS once — key rotation heals without a restart", func(t *testing.T) {
		// negative control: drop the cooldown (always force) → the inside-cooldown verify succeeds and the fetch count grows
		p := newJwksServer(t)
		first := rsaSigner(t, "old")
		p.set(first.jwk)
		c := NewClient(nil)
		args := VerifyArgs{Issuer: p.URL, ClientID: "cid", JwksURI: p.URL + "/jwks"}
		now := time.Now().Unix()
		good := `{"iss":"` + p.URL + `","aud":"cid","sub":"u1","exp":` + js.NumberString(float64(now+300)) + `}`
		if got, err := c.VerifyIDToken(first.sign(good, ""), args); err != nil || got.GetString("sub") != "u1" {
			t.Fatal(err)
		}
		fetches := p.count()

		// The provider rotates. The cached document has no key with this kid.
		second := rsaSigner(t, "new")
		p.set(second.jwk)

		// Inside the cooldown the refetch is REFUSED — that floor is what stops a junk kid from
		// turning every request into a fetch against the provider.
		_, err := c.VerifyIDToken(second.sign(good, ""), args)
		wantErr(t, err, `^no signing key matching kid "new" in `+regexp.QuoteMeta(p.URL+"/jwks")+`$`)
		if p.count() != fetches {
			t.Fatalf("refetched inside the cooldown: %d", p.count())
		}

		// Past it, rotation heals with nothing restarted.
		args.JwksCooldownMs = jsonx.Int(0)
		if got, err := c.VerifyIDToken(second.sign(good, ""), args); err != nil || got.GetString("sub") != "u1" {
			t.Fatalf("after rotation: %v", err)
		}
		if p.count() != fetches+1 {
			t.Fatalf("fetches %d, want %d", p.count(), fetches+1)
		}
		// A junk entry next to the good key is skipped, not fatal.
		p.set("5", `"str"`, second.jwk)
		c.Forget()
		if _, err := c.VerifyIDToken(second.sign(good, ""), args); err != nil {
			t.Fatalf("junk entry: %v", err)
		}
		// A token with no kid at all: every usable key is tried, and the message says (none).
		p.set()
		c.Forget()
		_, err = c.VerifyIDToken(second.sign(good, `{"alg":"RS256","typ":"JWT"}`), args)
		wantErr(t, err, `^no signing key matching kid "\(none\)" in `)
	})

	t.Run("ES256 is the raw r||s split at 32, against an EC JWK", func(t *testing.T) {
		// negative control: pass the DER-encoded signature → it is 70-ish bytes and is refused
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pad := func(b *big.Int) []byte { return b.FillBytes(make([]byte, 32)) }
		jwk := string(jsonx.Must(jsonx.Object{{K: "kty", V: "EC"}, {K: "crv", V: "P-256"}, {K: "x", V: js.B64URL(pad(key.X))}, {K: "y", V: js.B64URL(pad(key.Y))}, {K: "kid", V: "e1"}}))
		p := newJwksServer(t)
		p.set(jwk)
		now := time.Now().Unix()
		h := js.B64URL([]byte(`{"alg":"ES256","kid":"e1"}`))
		b := js.B64URL([]byte(`{"iss":"` + p.URL + `","aud":"cid","sub":"u1","exp":` + js.NumberString(float64(now+300)) + `}`))
		sum := sha256.Sum256([]byte(h + "." + b))
		r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		token := h + "." + b + "." + js.B64URL(append(pad(r), pad(s)...))
		c := NewClient(nil)
		got, err := c.VerifyIDToken(token, VerifyArgs{Issuer: p.URL, ClientID: "cid", JwksURI: p.URL + "/jwks"})
		if err != nil || got.GetString("sub") != "u1" {
			t.Fatalf("%v %v", got, err)
		}
		// An RSA key under ES256, or a mismatched `alg` on the JWK, is not a candidate.
		rs := rsaSigner(t, "e1")
		p.set(rs.jwk)
		c.Forget()
		_, err = c.VerifyIDToken(token, VerifyArgs{Issuer: p.URL, ClientID: "cid", JwksURI: p.URL + "/jwks"})
		wantErr(t, err, `no signing key matching kid "e1"`)
	})

	t.Run("SsoError is what the flow throws, not a bare Error", func(t *testing.T) {
		// negative control: return errors.New in errorf → IsError is false
		var target *Error
		if err := errorf("x"); !errors.As(err, &target) || err.Error() != "x" {
			t.Fatal("not an *Error")
		}
	})
}

// ── the query helpers the callback route leans on ─────────────────────────────────────────────────

func TestQuery(t *testing.T) {
	t.Run("FromQuery is Object.fromEntries(new URLSearchParams(raw)): last wins, first orders, + is a space", func(t *testing.T) {
		// negative control: keep the first value → error_description is 'a'
		m := FromQuery("error=access_denied&error_description=a&error_description=the+user+said+no")
		if DescribeError(m) != "access_denied: the user said no" {
			t.Fatalf("%q", DescribeError(m))
		}
		if DescribeError(FromQuery("code=x")) != "" || DescribeError(FromQuery("error=e")) != "e" {
			t.Fatal("describe")
		}
		var keys []string
		m.Each(func(k string, _ any) { keys = append(keys, k) })
		if strings.Join(keys, ",") != "error,error_description" {
			t.Fatalf("%v", keys)
		}
	})
}
