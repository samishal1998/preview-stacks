package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

func open(t *testing.T) *Auth {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s)
}

func usernames(t *testing.T, a *Auth) []string {
	t.Helper()
	users, err := a.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

// Port of the two direct-Auth tests in test/stack.test.ts.
func TestAuth(t *testing.T) {
	t.Run("PSTACK_ADMIN_USER env bootstraps the first admin, and only the first", func(t *testing.T) {
		// negative control: drop the `n > 0` guard in Bootstrap → mallory is created
		a := open(t)
		u, err := a.Bootstrap("sami", "correct-horse")
		if err != nil || u == nil {
			t.Fatalf("first bootstrap: %v %v", u, err)
		}
		// Inert once accounts exist — a leaked compose file carrying the env pair cannot mint admins.
		u, err = a.Bootstrap("mallory", "evil-password")
		if err != nil || u != nil {
			t.Fatalf("second bootstrap: %v %v", u, err)
		}
		if got := usernames(t, a); strings.Join(got, ",") != "sami" {
			t.Fatalf("users %v", got)
		}
	})

	t.Run("the last user cannot be deleted", func(t *testing.T) {
		// negative control: change `n <= 1` to `n < 1` → the first DeleteUser succeeds
		// An instance with accounts and no way to log in is only recoverable by editing the database
		// over SSH, and nothing in the UI can explain that state.
		a := open(t)
		u, err := a.CreateUser("sami", "correct-horse", CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.DeleteUser(u.ID); !IsError(err) {
			t.Fatalf("deleting the last user: %v", err)
		}
		if _, err := a.CreateUser("backup", "another-pass", CreateOpts{}); err != nil {
			t.Fatal(err)
		}
		if ok, err := a.DeleteUser(u.ID); !ok || err != nil {
			t.Fatalf("delete: %v %v", ok, err)
		}
		if got := usernames(t, a); strings.Join(got, ",") != "backup" {
			t.Fatalf("users %v", got)
		}
	})

	t.Run("createUser validates, refuses duplicates with the exact message, and login is one error for both halves", func(t *testing.T) {
		// negative control: change the username regex to allow uppercase → the "Sami" case passes
		a := open(t)
		if _, err := a.CreateUser("Sami", "correct-horse", CreateOpts{}); err == nil || err.Error() != "username must match /^[a-z0-9][a-z0-9._-]{1,31}$/ — lowercase, 2–32 chars, letters/digits/._-" {
			t.Fatalf("username: %v", err)
		}
		if _, err := a.CreateUser("sami", "short", CreateOpts{}); err == nil || err.Error() != "password must be at least 8 characters" {
			t.Fatalf("password: %v", err)
		}
		u, err := a.CreateUser("sami", "correct-horse", CreateOpts{Email: "sami@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if u.Role != "admin" || u.Email == nil || *u.Email != "sami@example.com" {
			t.Fatalf("row %+v", u)
		}
		if _, err := a.CreateUser("sami", "correct-horse", CreateOpts{}); err == nil || err.Error() != `user "sami" already exists` {
			t.Fatalf("duplicate: %v", err)
		}
		if _, _, err := a.Login("sami", "wrong-password"); err == nil || err.Error() != "invalid username or password" {
			t.Fatalf("wrong password: %v", err)
		}
		if _, _, err := a.Login("nobody", "correct-horse"); err == nil || err.Error() != "invalid username or password" {
			t.Fatalf("wrong user: %v", err)
		}
		session, got, err := a.Login("sami", "correct-horse")
		if err != nil || got.ID != u.ID || !strings.HasPrefix(session, "pstack_ses_") || len(session) != len("pstack_ses_")+64 {
			t.Fatalf("login: %q %+v %v", session, got, err)
		}
		su, _ := a.SessionUser(session)
		if su == nil || su.ID != u.ID {
			t.Fatalf("session user %+v", su)
		}
		// The JSON shape: email is null-not-absent for a local account.
		local, _ := a.CreateUser("local", "correct-horse", CreateOpts{})
		if b := string(jsonx.Must(local)); !strings.Contains(b, `"email":null`) || !strings.HasPrefix(b, `{"id":`) {
			t.Fatalf("json %s", b)
		}
	})

	t.Run("setPassword revokes every session and token in the same transaction", func(t *testing.T) {
		// negative control: drop the DELETE FROM sessions → the old session still resolves
		a := open(t)
		u, _ := a.CreateUser("sami", "correct-horse", CreateOpts{})
		session, _, _ := a.Login("sami", "correct-horse")
		token, id, err := a.CreateToken(u.ID, " ci-deploy ")
		if err != nil || !strings.HasPrefix(token, "pstack_pat_") {
			t.Fatalf("token %q %v", token, err)
		}
		if tu, _ := a.TokenUser(token); tu == nil || tu.ID != u.ID {
			t.Fatal("token did not resolve")
		}
		list, _ := a.ListTokens(u.ID)
		if len(list) != 1 || list[0].ID != id || list[0].Name != "ci-deploy" || list[0].LastUsedAt == nil {
			t.Fatalf("tokens %+v", list)
		}
		if ok, err := a.SetPassword(u.ID, "new-password-1"); !ok || err != nil {
			t.Fatalf("setPassword %v %v", ok, err)
		}
		if su, _ := a.SessionUser(session); su != nil {
			t.Fatal("session survived a password change")
		}
		if tu, _ := a.TokenUser(token); tu != nil {
			t.Fatal("token survived a password change")
		}
		if _, _, err := a.Login("sami", "new-password-1"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.SetPassword(999, "new-password-1"); ok {
			t.Fatal("unknown user changed")
		}
		if _, _, err := a.CreateToken(u.ID, "  "); err == nil || err.Error() != "a token needs a name — it is the only handle left later" {
			t.Fatalf("empty name: %v", err)
		}
	})

	t.Run("sso config: empty secret keeps the stored one, first save refuses it, read re-validates", func(t *testing.T) {
		// negative control: drop the `secret == ""` refusal → the first save with no secret succeeds
		a := open(t)
		cfg, _ := sso.ParseConfig(mustParse(t, `{"mode":"oauth2","provider":"github","clientId":"cid"}`))
		if err := a.SetSsoConfig(cfg, ""); err == nil || err.Error() != "clientSecret is required" {
			t.Fatalf("first save: %v", err)
		}
		if err := a.SetSsoConfig(cfg, "shh"); err != nil {
			t.Fatal(err)
		}
		if err := a.SetSsoConfig(cfg, ""); err != nil {
			t.Fatal(err)
		}
		row, err := a.SsoConfig()
		if err != nil || row == nil || row.ClientSecret != "shh" || row.Config.Label != "GitHub" {
			t.Fatalf("read: %+v %v", row, err)
		}
		// The stored JSON is the full normalised shape in the TypeScript's key order.
		var raw string
		a.store.DB.QueryRow("SELECT config FROM sso_config WHERE id = 1").Scan(&raw)
		if !strings.HasPrefix(raw, `{"mode":"oauth2","enabled":true,"clientId":"cid","allowedEmailDomains":[],"defaultRole":"admin","label":"GitHub","discoveryUrl":"","provider":"github",`) {
			t.Fatalf("stored %s", raw)
		}
		// A hand-edited row that no longer validates reads as "no provider".
		a.store.DB.Exec("UPDATE sso_config SET config = '{\"mode\":\"saml\"}' WHERE id = 1")
		if row, err := a.SsoConfig(); row != nil || err != nil {
			t.Fatalf("invalid row: %+v %v", row, err)
		}
		if ok, _ := a.ClearSsoConfig(); !ok {
			t.Fatal("clear")
		}
		if row, _ := a.SsoConfig(); row != nil {
			t.Fatal("cleared row still read")
		}
	})
}

func mustParse(t *testing.T, s string) any {
	t.Helper()
	v, err := omap.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ── provisioning: port of test/sso.test.ts 'sso: provisioning' ───────────────────────────────────

func identity(over func(*sso.Identity)) *sso.Identity {
	id := &sso.Identity{Subject: "s-1", Username: "Octo Cat", Email: "octo@example.com", Name: "The Octocat", EmailVerified: jsonx.Bool(true)}
	if over != nil {
		over(id)
	}
	return id
}

func TestProvisioning(t *testing.T) {
	t.Run("first login creates, second login finds the same user, a changed email does not duplicate", func(t *testing.T) {
		// negative control: skip the sso_links lookup → the second login is 'created' and a second row appears
		a := open(t)
		first, err := a.SsoSignIn("github", identity(nil), SsoSignInOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if first.How != Created || first.User.Username != "octo-cat" || *first.User.Email != "octo@example.com" {
			t.Fatalf("first %+v", first)
		}
		second, _ := a.SsoSignIn("github", identity(nil), SsoSignInOpts{})
		if second.How != Linked || second.User.ID != first.User.ID || second.Session == first.Session {
			t.Fatalf("second %+v", second)
		}
		// The person changed their address upstream. Same subject ⇒ same account, with the new email.
		third, _ := a.SsoSignIn("github", identity(func(i *sso.Identity) { i.Email = "new@example.com" }), SsoSignInOpts{})
		if third.How != Linked || third.User.ID != first.User.ID || *third.User.Email != "new@example.com" {
			t.Fatalf("third %+v", third)
		}
		if len(usernames(t, a)) != 1 {
			t.Fatal("duplicated")
		}
		// A different subject at the same provider is a different person, even at the same address.
		other, _ := a.SsoSignIn("github", identity(func(i *sso.Identity) { i.Subject = "s-2"; i.Email = "new@example.com" }), SsoSignInOpts{})
		if other.How != Created || other.User.ID == first.User.ID {
			t.Fatalf("other %+v", other)
		}
		// The username was taken, so it was suffixed rather than linked to whoever holds it.
		if other.User.Username != "octo-cat-2" {
			t.Fatalf("username %q", other.User.Username)
		}
		if len(usernames(t, a)) != 2 {
			t.Fatal("count")
		}
		links, _ := a.SsoLinks(first.User.ID)
		if len(links) != 1 || links[0].ProviderKey != "github" || links[0].Subject != "s-1" || links[0].LastLoginAt == nil {
			t.Fatalf("links %+v", links)
		}
	})

	t.Run("an existing local account is adopted only on a VERIFIED email", func(t *testing.T) {
		// negative control: accept EmailVerified == nil in the adoption branch → the unverified login adopts alice
		a := open(t)
		local, _ := a.CreateUser("alice", "password123", CreateOpts{Email: "alice@example.com"})
		// The provider never said the address was verified: adopting here would be account takeover.
		unverified, err := a.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Subject = "x1"; i.Email = "alice@example.com"; i.EmailVerified = nil }), SsoSignInOpts{})
		if err != nil || unverified.How != Created || unverified.User.ID == local.ID {
			t.Fatalf("unverified %+v %v", unverified, err)
		}
		verified, _ := a.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Subject = "x2"; i.Email = "alice@example.com" }), SsoSignInOpts{})
		if verified.How != Created { // two accounts now share the address ⇒ ambiguous, so no adoption
			t.Fatalf("ambiguous %+v", verified)
		}

		clean := open(t)
		bob, _ := clean.CreateUser("bob", "password123", CreateOpts{Email: "bob@example.com"})
		adopted, _ := clean.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Subject = "y1"; i.Email = "bob@example.com" }), SsoSignInOpts{})
		if adopted.How != Adopted || adopted.User.ID != bob.ID || len(usernames(t, clean)) != 1 {
			t.Fatalf("adopted %+v", adopted)
		}
		// And it is a link from then on — the email is no longer consulted.
		again, _ := clean.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Subject = "y1"; i.Email = "moved@example.com" }), SsoSignInOpts{})
		if again.How != Linked {
			t.Fatalf("again %+v", again)
		}
	})

	t.Run("the email allow-list refuses before anything is created", func(t *testing.T) {
		// negative control: move the EmailAllowed check after provisioning → a user row exists
		a := open(t)
		_, err := a.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Email = "x@outside.test" }), SsoSignInOpts{AllowedEmailDomains: []string{"example.com"}})
		if !sso.IsError(err) || !strings.Contains(err.Error(), "not in an allowed email domain") {
			t.Fatalf("outside: %v", err)
		}
		// And a provider that returned no address at all is refused too, not waved through.
		_, err = a.SsoSignIn("okta", identity(func(i *sso.Identity) { i.Email = "" }), SsoSignInOpts{AllowedEmailDomains: []string{"example.com"}})
		if !sso.IsError(err) || !strings.Contains(err.Error(), "no email address") {
			t.Fatalf("no email: %v", err)
		}
		if len(usernames(t, a)) != 0 {
			t.Fatal("something was created")
		}
	})

	t.Run("an SSO account has no usable password, and a session behaves like any other", func(t *testing.T) {
		// negative control: provision with an empty password instead of randomSecret → login('') succeeds
		a := open(t)
		in, err := a.SsoSignIn("github", identity(nil), SsoSignInOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if su, _ := a.SessionUser(in.Session); su == nil || su.ID != in.User.ID {
			t.Fatal("session")
		}
		// The row is ordinary — there is no null-password state for anything to treat as "no password
		// needed" — but nobody holds the 32 random bytes behind it.
		for _, pw := range []string{"", "password"} {
			if _, _, err := a.Login(in.User.Username, pw); err == nil || err.Error() != "invalid username or password" {
				t.Fatalf("login %q: %v", pw, err)
			}
		}
	})
}

// ── the golden database and the argon2 facts ─────────────────────────────────────────────────────

func TestGolden(t *testing.T) {
	golden := testfacts.Golden(t)

	t.Run("the fixture database opens unchanged and its four passwords verify", func(t *testing.T) {
		// negative control: hardcode m=65536 in VerifyPassword instead of reading it → dave (m=19456,t=3) fails
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "db"), 0o700)
		src, err := os.ReadFile(filepath.Join(golden, "host", "db", "pstack.db"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "db", "pstack.db"), src, 0o600)
		s, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var v int
		s.DB.QueryRow("PRAGMA user_version").Scan(&v)
		if v != len(store.Migrations) {
			t.Fatalf("fixture user_version %d — the fixture and the migrations list disagree", v)
		}
		var fx struct {
			Admin, Bob, Carol, Dave struct {
				Username, Password, Hash string
			}
			Sessions map[string]string
			Tokens   map[string]struct {
				ID    int64
				Token string
			}
		}
		testfacts.Load(t, "../host/FIXTURE.json", &fx)
		a := New(s)
		for _, u := range []struct{ Username, Password string }{
			{fx.Admin.Username, fx.Admin.Password}, {fx.Bob.Username, fx.Bob.Password}, {fx.Carol.Username, fx.Carol.Password}, {fx.Dave.Username, fx.Dave.Password},
		} {
			if _, got, err := a.Login(u.Username, u.Password); err != nil || got.Username != u.Username {
				t.Fatalf("login %s: %v", u.Username, err)
			}
			if _, _, err := a.Login(u.Username, "wrong"); err == nil {
				t.Fatalf("login %s accepted 'wrong'", u.Username)
			}
		}
		if !VerifyPassword(fx.Dave.Password, fx.Dave.Hash) {
			t.Fatal("dave's m=19456,t=3 hash from FIXTURE.json did not verify")
		}
		// The fixture's live sessions and tokens resolve through the same sha256-hex lookup.
		if su, _ := a.SessionUser(fx.Sessions["bob"]); su == nil || su.Username != "bob" {
			t.Fatal("bob's fixture session did not resolve")
		}
		if tu, _ := a.TokenUser(fx.Tokens["admin"].Token); tu == nil || tu.Username != "admin" {
			t.Fatal("admin's fixture token did not resolve")
		}
	})

	t.Run("golden/facts/argon2.json: every Bun-written hash verifies its password and rejects 'wrong'", func(t *testing.T) {
		// negative control: encode the salt with padded base64 in VerifyPassword → every row fails
		var facts struct {
			Password, WrongPassword string
			Rows                    []struct{ Shape, Hash string }
		}
		testfacts.Load(t, "argon2.json", &facts)
		if len(facts.Rows) != 3 {
			t.Fatalf("%d rows", len(facts.Rows))
		}
		for _, r := range facts.Rows {
			if !VerifyPassword(facts.Password, r.Hash) {
				t.Fatalf("%s: did not verify", r.Shape)
			}
			if VerifyPassword(facts.WrongPassword, r.Hash) {
				t.Fatalf("%s: accepted the wrong password", r.Shape)
			}
		}
		// And what this port writes is the same shape, at the documented default cost.
		h := HashPassword(facts.Password)
		if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") || strings.Contains(h, "=$") || !VerifyPassword(facts.Password, h) {
			t.Fatalf("own hash %s", h)
		}
	})
}
