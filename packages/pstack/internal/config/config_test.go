package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/hostvars"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registries"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

const (
	password    = "hunter2hunter2"
	slackURL    = "https://hooks.slack.com/services/T00/B00/xoxb-the-credential"
	webhookURL  = "https://receiver.example/hooks/pstack"
	registryPwd = "ghp_the_registry_token"
	specYAML    = "version: 1\nkind: isolated\nstack: pr-${PR}\naxes:\n  - name: db\n    up: \"true\"\n    assert_gone: \"true\"\n"
	goodHash    = "$argon2id$v=19$m=65536,t=2,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaA"
	routingYAML = "http:\n  middlewares:\n    dashboard-auth:\n      basicAuth:\n        users:\n          - \"admin:$apr1$x\"\n"
)

type host struct {
	Sources
	dir string
}

func newHost(t *testing.T) host {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	regDir, routeDir := filepath.Join(dir, "docker"), filepath.Join(dir, "routing")
	for _, d := range []string{regDir, routeDir} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	return host{Sources{
		Store:      st,
		Auth:       auth.New(st),
		HostVars:   hostvars.New(st),
		Webhooks:   webhooks.New(st, notify.PublicConfig),
		Registries: registries.New(regDir),
		Routing:    routing.New(routeDir),
		Specs:      specs.New(dir),
	}, dir}
}

// populate is one of every kind of thing that travels.
func (h host) populate(t *testing.T) {
	t.Helper()
	u, err := h.Auth.CreateUser("alice", password, auth.CreateOpts{Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Auth.CreateToken(u.ID, "ci-deploy"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.HostVars.Put("REGION", "eu-west-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.HostVars.Put("DB_PASSWORD", "p@ssw0rd", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Webhooks.Create(webhooks.CreateArgs{
		Type: "webhook", Name: "ci bot", Config: omap.From("url", webhookURL),
		Events: []string{"job.failed"}, ValidateConfig: notify.ValidateConfig, Signs: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Webhooks.Create(webhooks.CreateArgs{
		Type: "slack", Name: "team channel", Config: omap.From("webhookUrl", slackURL),
		Events: []string{"*"}, ValidateConfig: notify.ValidateConfig, Signs: false,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := sso.ParseConfig(parseMap(t, `{"mode":"oidc","clientId":"pstack","issuer":"https://accounts.example.com","allowedEmailDomains":["example.com"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Auth.SetSsoProvider("corp", cfg, "the-client-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Registries.Put("ghcr.io", "robot", registryPwd); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Routing.Write("extra.yml", routingYAML); err != nil {
		t.Fatal(err)
	}
	desc := "the web app"
	compose := "services:\n  web:\n    image: nginx\n"
	if _, err := h.Specs.Put("web", specYAML, specs.PutOptions{Description: &desc, ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
}

func parseMap(t *testing.T, s string) *omap.Map {
	t.Helper()
	v, err := omap.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := v.(*omap.Map)
	return m
}

// overTheWire is what production actually hands Apply: the document as JSON (GET /api/config, or a
// sealed file) and back through Parse. Applying the in-memory struct instead would skip the one
// encoding step every real caller performs.
func overTheWire(t *testing.T, d *Document) *Document {
	t.Helper()
	body, err := jsonx.MarshalIndent(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	// The ordered-map config is the one field encoding/json cannot handle on its own. Asserted here
	// rather than assumed, because a document whose notifier config decoded to {} would be refused
	// by Apply's validator and look like a policy skip, not a decoding bug.
	for _, n := range parsed.Notifiers {
		if n.Type == "slack" && n.Config.GetString("webhookUrl") != slackURL {
			t.Fatalf("the notifier config did not survive JSON: %v", n.Config)
		}
	}
	return parsed
}

func assemble(t *testing.T, h host) *Document {
	t.Helper()
	d, err := h.Assemble()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// stable is the document as comparable bytes: the two fields that legitimately differ between two
// hosts are zeroed, and notifiers are sorted by name because their listing is by created_at, which
// two rows written in one millisecond can tie on.
func stable(t *testing.T, d *Document) string {
	t.Helper()
	c := *d
	c.ExportedAt = 0
	c.Notifiers = append([]Notifier{}, d.Notifiers...)
	sort.SliceStable(c.Notifiers, func(i, j int) bool { return c.Notifiers[i].Name < c.Notifiers[j].Name })
	b, err := jsonx.MarshalIndent(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// negative control: build the row's Config from `r.Config` (what Webhooks.List returns) instead of
// from RawConfigOf — the exported Slack config becomes "••••••••••••" and both assertions fail.
//
// The fixture is a Slack notifier deliberately: `notify.PublicConfig` masks only fields a type
// marked Secret, and `webhookUrl` is the one field in the whole registry that carries that flag. A
// webhook-type URL is not masked, so a fixture built from it would prove nothing.
func TestAssembleCarriesTheUnmaskedNotifierConfig(t *testing.T) {
	h := newHost(t)
	h.populate(t)

	listed, err := h.Webhooks.List()
	if err != nil {
		t.Fatal(err)
	}
	masked := false
	for _, r := range listed {
		if r.Type == "slack" && strings.Contains(r.Config.GetString("webhookUrl"), "•") {
			masked = true
		}
	}
	if !masked {
		t.Fatal("the fixture is not masked by the read path, so this test proves nothing")
	}

	d := assemble(t, h)
	body := stable(t, d)
	if strings.Contains(body, "•") {
		t.Fatalf("a masked value reached the document:\n%s", body)
	}
	if !strings.Contains(body, slackURL) {
		t.Fatalf("the real Slack URL is missing:\n%s", body)
	}
	// The signing secret travels too: minting a new one on the far host breaks every receiver's
	// HMAC check, which is the whole point of carrying the registration.
	for _, n := range d.Notifiers {
		if n.Type == "webhook" && !strings.HasPrefix(n.Secret, "whsec_") {
			t.Fatalf("no signing secret carried: %+v", n)
		}
	}
	// And so does everything else that has no second copy anywhere.
	for _, want := range []string{"p@ssw0rd", registryPwd, "the-client-secret", "$argon2id$"} {
		if !strings.Contains(body, want) {
			t.Fatalf("%q did not travel:\n%s", want, body)
		}
	}
}

// negative control: drop `s.applyRegistries` from Apply's step list — the second host's document
// loses its registry entry and the comparison fails. (Any one step removed fails it the same way,
// which is the point: this asserts the WHOLE document survives the trip.)
func TestRoundTripOntoAFreshHost(t *testing.T) {
	a := newHost(t)
	a.populate(t)
	doc := assemble(t, a)

	b := newHost(t)
	sum, err := b.Apply(overTheWire(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Skipped) != 0 {
		t.Fatalf("nothing should have been skipped on an empty host: %v", sum.Skipped)
	}
	if len(sum.Created) != 10 { // user, token, 2 vars, 2 notifiers, sso, registry, routing, spec
		t.Fatalf("created %d: %v", len(sum.Created), sum.Created)
	}

	if got, want := stable(t, assemble(t, b)), stable(t, doc); got != want {
		t.Fatalf("the document did not survive the trip:\n--- new host ---\n%s\n--- original ---\n%s", got, want)
	}

	// The point of carrying hashes rather than values: the same password logs in on the new host.
	if _, u, err := b.Auth.Login("alice", password); err != nil || u == nil {
		t.Fatalf("login on the new host: %v %v", u, err)
	}
	// And the registry credential is usable, not just present: the config.json auth blob decodes to
	// the original pair.
	blob, err := os.ReadFile(filepath.Join(b.dir, "docker", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), base64Of("robot:"+registryPwd)) {
		t.Fatalf("registry credential not written as docker expects it:\n%s", blob)
	}
}

// negative control: delete the `here[v.Name]` check in applyVars — the second run calls
// hostvars.Put again, which is create-or-replace, so it reports a creation and the assertion fails.
func TestApplyTwiceIsANoOp(t *testing.T) {
	a := newHost(t)
	a.populate(t)
	doc := assemble(t, a)

	b := newHost(t)
	if _, err := b.Apply(overTheWire(t, doc)); err != nil {
		t.Fatal(err)
	}
	after := stable(t, assemble(t, b))

	sum, err := b.Apply(overTheWire(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Created) != 0 {
		t.Fatalf("the second apply created things: %v", sum.Created)
	}
	if len(sum.Skipped) != 10 {
		t.Fatalf("skipped %d: %v", len(sum.Skipped), sum.Skipped)
	}
	if got := stable(t, assemble(t, b)); got != after {
		t.Fatalf("the second apply changed the host:\n%s\n---\n%s", got, after)
	}
}

// negative control: delete the same `here[v.Name]` check — REGION is overwritten with eu-west-1 and
// the assertion that the host kept its own value fails.
//
// Apply never updates and never deletes: a config file from elsewhere must not be able to repoint a
// working host's variables, credentials or notifiers, only to add what is missing.
func TestApplyNeverOverwritesWhatIsAlreadyHere(t *testing.T) {
	a := newHost(t)
	a.populate(t)
	doc := assemble(t, a)

	b := newHost(t)
	if _, err := b.HostVars.Put("REGION", "us-east-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Registries.Put("ghcr.io", "the-real-robot", "the-real-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Routing.Write("extra.yml", "http: {}\n"); err != nil {
		t.Fatal(err)
	}
	sum, err := b.Apply(doc)
	if err != nil {
		t.Fatal(err)
	}

	vars, err := b.HostVars.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.Name == "REGION" && (v.Value == nil || *v.Value != "us-east-1") {
			t.Fatalf("REGION was overwritten: %+v", v)
		}
	}
	entries := b.Registries.State().Entries
	if len(entries) != 1 || entries[0].Username == nil || *entries[0].Username != "the-real-robot" {
		t.Fatalf("the registry credential was overwritten: %+v", entries)
	}
	if got, _ := b.Routing.Read("extra.yml"); got != "http: {}\n" {
		t.Fatalf("the routing file was overwritten: %q", got)
	}
	if len(sum.Skipped) != 3 {
		t.Fatalf("skipped %v", sum.Skipped)
	}
}

// negative control: delete the credsStore/credHelpers branch in registryEntries — the helper-backed
// entry is exported with an empty username and password and the count assertion fails. For the
// Docker Hub half: drop the DockerHubKey case in normalizeForTravel — the entry moves to Skipped.
func TestRegistryEntriesThatCannotBeRebuiltAreSkipped(t *testing.T) {
	h := newHost(t)
	cfg := `{
  "credHelpers": { "helper.example.com": "ecr-login" },
  "auths": {
    "helper.example.com": {},
    "token.example.com": { "auth": "` + base64Of("robot:pw") + `", "identitytoken": "an-oauth-refresh-token" },
    "junk.example.com": { "auth": "not-base64-of-a-pair" },
    "good.example.com": { "auth": "` + base64Of("robot:pw") + `", "email": "ops@example.com" },
    "https://index.docker.io/v1/": { "auth": "` + base64Of("hubuser:hubpw") + `" }
  }
}`
	if err := os.WriteFile(filepath.Join(h.dir, "docker", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	d := assemble(t, h)
	if len(d.Registry) != 2 || d.Registry[1].Registry != "good.example.com" || d.Registry[1].Password != "pw" {
		t.Fatalf("carried %+v", d.Registry)
	}
	// Docker Hub is the commonest credential of all, and its stored key is one NormalizeRegistry
	// refuses verbatim — so it travels as the friendly name that normalizes back to it.
	if d.Registry[0].Registry != "docker.io" || d.Registry[0].Username != "hubuser" {
		t.Fatalf("Docker Hub did not travel: %+v", d.Registry)
	}
	if _, err := h.Registries.Put(d.Registry[0].Registry, d.Registry[0].Username, d.Registry[0].Password); err != nil {
		t.Fatalf("what travelled does not go back in: %v", err)
	}
	joined := strings.Join(d.Skipped, "\n")
	for _, want := range []string{"credential helper", "identitytoken", "not base64 of user:password"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("skip reasons did not name %q:\n%s", want, joined)
		}
	}
}

func TestApplyingAnOldExportWithThePointerSkipsIt(t *testing.T) {
	// negative control: return a non-routing.Error from the reserved gate — applyRouting's
	// IsError branch misses it and the whole apply aborts on a document that is merely stale.
	h := newHost(t)
	d := &Document{Version: FormatVersion, Routing: []RoutingFile{
		{Name: "tls-wildcard.yml", Content: "tls:\n  certificates: []\n"},
		{Name: "later.yml", Content: "http: {}\n"},
	}}
	sum, err := h.Apply(d)
	if err != nil {
		t.Fatalf("a stale export must not abort the apply: %v", err)
	}
	joined := strings.Join(sum.Skipped, "\n")
	if !strings.Contains(joined, "tls-wildcard.yml") || !strings.Contains(joined, "managed by pstack") {
		t.Fatalf("the pointer must be skipped with its reason: %v", sum.Skipped)
	}
	// And the file AFTER it in the document still applies — one skip is not a stop.
	if _, err := h.Routing.Read("later.yml"); err != nil {
		t.Fatalf("the rest of the document must still apply: %v", err)
	}
}

func TestTheWildcardPointerDoesNotTravel(t *testing.T) {
	// negative control: drop the routing.IsReserved skip in assemble — tls-wildcard.yml rides along
	// without the pair it names, and applying this document puts the TARGET host in dns-persist-01
	// with no certificate: every new deploy drops its certresolver and Traefik points at files that
	// were never copied. TLS silently off, on a host nobody touched.
	h := newHost(t)
	cert, key := mintPair(t, "*.preview.example.com")
	if _, err := h.Routing.SetWildcard(cert, key, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Routing.Write("extra.yml", "http: {}\n"); err != nil {
		t.Fatal(err)
	}
	d := assemble(t, h)
	for _, f := range d.Routing {
		if f.Name == routing.WildcardYAML {
			t.Fatalf("the pointer travelled without its pair: %+v", d.Routing)
		}
	}
	if len(d.Routing) != 1 || d.Routing[0].Name != "extra.yml" {
		t.Fatalf("ordinary routing files must still travel: %+v", d.Routing)
	}
	if !strings.Contains(strings.Join(d.Skipped, "\n"), "PUT /api/tls/wildcard") {
		t.Fatalf("the skip must say how to store one on the target: %v", d.Skipped)
	}
}

// negative control: drop the `d.Version != FormatVersion` check in Parse — the unknown document
// parses, nothing names the writer, and this fails.
func TestParseRefusesADocumentVersionItDoesNotUnderstand(t *testing.T) {
	_, err := Parse([]byte(`{"version":7,"pstackVersion":"9.9.9"}`))
	if err == nil || !strings.Contains(err.Error(), "pstack 9.9.9") || !strings.Contains(err.Error(), "version 7") {
		t.Fatalf("err = %v", err)
	}
	if !IsError(err) {
		t.Fatalf("err = %T, want *Error", err)
	}
	// A document this build DOES understand parses, and a sealed round trip preserves it exactly.
	h := newHost(t)
	h.populate(t)
	body, err := jsonx.MarshalIndent(assemble(t, h))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(body, pass)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Unseal(sealed, pass)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stable(t, d), string(mustStable(t, body)); got != want {
		t.Fatalf("the seal round trip changed the document:\n%s\n---\n%s", got, want)
	}
}

// negative control: return the raw config from Trusts without the notifier loop — the Slack URL
// stops being listed and the assertion fails. An operator must see which registries and URLs a file
// is about to make this host trust BEFORE it is written.
func TestTrustsNamesEveryRegistryAndURL(t *testing.T) {
	h := newHost(t)
	h.populate(t)
	lines := strings.Join(assemble(t, h).Trusts(), "\n")
	for _, want := range []string{"ghcr.io", "robot", slackURL, webhookURL} {
		if !strings.Contains(lines, want) {
			t.Fatalf("Trusts did not name %q:\n%s", want, lines)
		}
	}
}

// negative control: drop any one of the argon2Re / sha256Re / usernameRe / notifierNameRe / token
// name guards in applyUsers, applyTokens or applyNotifiers — that row is inserted, so it appears in
// Created and the assertion that dave is the only creation fails.
//
// A document is an untrusted file. A row that would create an account nothing can log into, or a
// token hash that is not a digest, is refused rather than written.
func TestApplyRefusesMalformedAccountRows(t *testing.T) {
	h := newHost(t)
	doc := &Document{
		Version: FormatVersion,
		Users: []auth.ExportUser{
			// Every row names a role: an absent one is refused by Parse and by Apply, so leaving it
			// off here would make this test fail on the role rather than on the thing it is about.
			{Username: "Alice", Role: "viewer", PasswordHash: goodHash, CreatedAt: 1}, // capital: not a usable username
			{Username: "bob", Role: "viewer", PasswordHash: "plaintext-password", CreatedAt: 1},
			{Username: "carol", Role: "viewer", PasswordHash: "", CreatedAt: 1},
			{Username: "dave", Role: "viewer", PasswordHash: goodHash, CreatedAt: 1}, // the one good row
		},
		// dave EXISTS by the time this is read, so the only thing that can refuse this row is the
		// digest check — otherwise "there is no account dave" would answer for it.
		Tokens: []auth.ExportToken{
			{Username: "dave", Name: "t", TokenHash: "nope-not-a-digest"},
			{Username: "dave", Name: "  ", TokenHash: strings.Repeat("a", 64)},
		},
		Notifiers: []Notifier{{
			Type: "webhook", Name: " a name Create would refuse", Config: omap.From("url", webhookURL),
			Events: []string{"job.failed"}, Secret: "whsec_x",
		}},
	}
	sum, err := h.Apply(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Created) != 1 || sum.Created[0] != "user dave" {
		t.Fatalf("created %v", sum.Created)
	}
	if len(sum.Skipped) != 6 {
		t.Fatalf("skipped %v", sum.Skipped)
	}
	users, err := h.Auth.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "dave" {
		t.Fatalf("users %+v", users)
	}
	toks, err := h.Auth.ListTokens(users[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 0 {
		t.Fatalf("a token with a non-digest hash was stored: %+v", toks)
	}
}

// base64Of is the `auth` blob docker stores: base64("user:password").
func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustStable(t *testing.T, body []byte) []byte {
	t.Helper()
	var d Document
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	return []byte(stable(t, &d))
}

// An omitted role must never mean "admin". This document is an untrusted FILE: under the old
// defaulting, the way to obtain an administrator on someone else's host was to leave the field out —
// the one spelling a reviewer skimming the YAML would not notice, because there is nothing to see.
//
// negative control: restore `if role == "" { role = "admin" }` in applyUsers and drop the Parse
// guard — `mallory` is created, and `ListUsers` reports role "admin".
func TestAnOmittedRoleIsRefusedRatherThanMadeAdmin(t *testing.T) {
	h := newHost(t)
	doc := &Document{Version: FormatVersion, Users: []auth.ExportUser{{Username: "mallory", PasswordHash: goodHash, CreatedAt: 1}}}

	// Refused whole, before anything is written — not skipped, so a half-applied import cannot leave
	// the account behind either.
	plain, err := jsonx.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(plain); err == nil || !strings.Contains(err.Error(), "has no role") {
		t.Fatalf("Parse accepted a roleless account: %v", err)
	}
	if _, err := h.Apply(doc); err == nil || !strings.Contains(err.Error(), "has no role") {
		t.Fatalf("Apply accepted a roleless account: %v", err)
	}
	users, err := h.Auth.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == "mallory" {
			t.Fatal("mallory was created")
		}
	}
}

// A 0.30.0 export carries ONE provider as a bare "sso" object. It must keep applying: Parse folds
// it into SSOProviders under the key store migration 7 derives for the same config in the
// database ("oidc" here, since the config names no provider), and Apply lands it there.
//
// negative control: empty the foldLegacySSO body → SSOProviders stays empty after Parse, nothing is
// created, and both halves of this test fail.
func TestParseAcceptsTheOldSingleProviderShape(t *testing.T) {
	// The old shape verbatim: version 1, the full normalised sso.Config under "sso" — the fields
	// populate's provider stores, as 0.30.0's ssoConfig() exported them.
	oldDoc := `{
  "version": 1,
  "pstackVersion": "0.30.0",
  "exportedAt": 1,
  "skipped": [], "users": [], "tokens": [], "vars": [], "notifiers": [],
  "sso": {
    "config": {"mode":"oidc","enabled":true,"clientId":"pstack","allowedEmailDomains":["example.com"],"allowedUsernames":[],"requiredGroups":[],"defaultRole":"admin","label":"accounts.example.com","discoveryUrl":"https://accounts.example.com/","provider":"","authorizeUrl":"","tokenUrl":"","userInfoUrl":"","emailsUrl":"","groupsUrl":"","scopes":"openid profile email","claimMap":{"subject":"sub","username":"preferred_username","email":"email","name":"name","avatar":"picture"}},
    "clientSecret": "the-client-secret"
  },
  "registries": [], "routing": [], "specs": []
}`
	d, err := Parse([]byte(oldDoc))
	if err != nil {
		t.Fatal(err)
	}
	if d.SSO != nil {
		t.Fatal("the legacy object survived Parse")
	}
	if len(d.SSOProviders) != 1 || d.SSOProviders[0].Key != "oidc" || d.SSOProviders[0].ClientSecret != "the-client-secret" {
		t.Fatalf("folded %+v", d.SSOProviders)
	}
	h := newHost(t)
	sum, err := h.Apply(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sum.Created, "\n") != "sso provider oidc" {
		t.Fatalf("created %v (skipped %v)", sum.Created, sum.Skipped)
	}
	row, err := h.Auth.SsoProvider("oidc")
	if err != nil || row == nil || row.ClientSecret != "the-client-secret" || row.Config.DiscoveryURL != "https://accounts.example.com/" {
		t.Fatalf("landed %+v %v", row, err)
	}
	// An oauth2 single provider derives its preset key instead — and reaching Apply WITHOUT Parse
	// (it is exported) folds identically.
	gh, err := sso.ParseConfig(parseMap(t, `{"mode":"oauth2","provider":"github","clientId":"cid"}`))
	if err != nil {
		t.Fatal(err)
	}
	byHand := &Document{Version: FormatVersion, SSO: &SSO{Config: gh, ClientSecret: "shh"}}
	if _, err := h.Apply(byHand); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.Auth.SsoProvider("github"); row == nil || row.Config.Label != "GitHub" {
		t.Fatalf("oauth2 legacy landed %+v", row)
	}
}

// Trusts() is the ONLY thing an operator sees before a push writes. A grant it does not name is a
// grant nobody reviewed — and accounts, tokens and the identity provider are all larger grants than
// the registry redirect the summary was originally written for.
//
// negative control: delete any one of the Users / Tokens / SSO loops in Trusts — that assertion
// fails, and for a document carrying only that kind, Trusts returns an empty list, which reads to an
// operator as "this file is inert".
func TestTrustsNamesEveryGrantNotJustRegistries(t *testing.T) {
	doc := &Document{
		Version: FormatVersion,
		Users:   []auth.ExportUser{{Username: "svc-backup", Role: "admin"}},
		Tokens:  []auth.ExportToken{{Username: "svc-backup", Name: "backup"}},
		// TWO providers: the multi-provider point is that every one is named, not the first.
		SSOProviders: []SSOProvider{
			{Key: "gh", Config: &sso.Config{Provider: "github", Label: "Company SSO", DefaultRole: "admin", AuthorizeURL: "https://evil.example.com/authorize"}},
			{Key: "corp", Config: &sso.Config{Label: "Corp IdP", DefaultRole: "admin", DiscoveryURL: "https://idp.corp.example"}},
		},
	}
	got := strings.Join(doc.Trusts(), "\n")
	for _, want := range []string{"svc-backup", "admin", "backup", "evil.example.com", "Company SSO", "idp.corp.example", "Corp IdP"} {
		if !strings.Contains(got, want) {
			t.Errorf("Trusts() never mentions %q:\n%s", want, got)
		}
	}
	// The specific shape that made this dangerous: a document with no registry at all must NOT
	// summarise as nothing to trust.
	if len(doc.Trusts()) == 0 {
		t.Fatal("a document that creates an admin and an IdP summarised as trusting nothing")
	}
}

// A document an operator AUTHORS, declaring the credentials a rebuilt host should come up holding.
//
// The point of the whole feature: a migration should not mean re-issuing every token and re-pasting
// it into every CI secret. An exported document already carries tokens by hash, so an export→apply
// round trip preserves them — this is the other direction, where nobody exported anything and the
// author picked the value.
//
// negative control: drop the `auth.HashToken(t.Token)` branch in applyTokens — the row has no
// tokenHash, so it is skipped as "not a sha256 digest" and the token below authenticates as nobody.
func TestApplyAcceptsAPlaintextTokenAnAuthorChose(t *testing.T) {
	h := newHost(t)
	const chosen = "pstack_pat_a_value_the_operator_picked"
	sum, err := h.Apply(&Document{
		Version: FormatVersion,
		Users:   []auth.ExportUser{{Username: "ci", Role: "developer", PasswordHash: goodHash, CreatedAt: 1}},
		Tokens:  []auth.ExportToken{{Username: "ci", Name: "pipeline", Token: chosen, CreatedAt: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Skipped) != 0 {
		t.Fatalf("skipped %v", sum.Skipped)
	}
	// The real assertion: the token AUTHENTICATES. Comparing digests would only prove this test can
	// call the same function the code did.
	row, err := h.Auth.TokenUser(chosen)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Username != "ci" || row.Role != "developer" {
		t.Fatalf("the chosen token does not authenticate as ci/developer: %+v", row)
	}
}

// A row carrying BOTH, disagreeing, is a document saying two different things about one credential.
//
// negative control: let `token` win silently when a tokenHash is also present — the host then holds
// a credential the author did not intend, and nothing anywhere says so.
func TestApplyRefusesATokenThatContradictsItsOwnHash(t *testing.T) {
	h := newHost(t)
	sum, err := h.Apply(&Document{
		Version: FormatVersion,
		Users:   []auth.ExportUser{{Username: "ci", Role: "viewer", PasswordHash: goodHash, CreatedAt: 1}},
		Tokens: []auth.ExportToken{
			{Username: "ci", Name: "mismatched", Token: "one-value", TokenHash: strings.Repeat("a", 64), CreatedAt: 1},
			// The same value in both places is not a contradiction — it is an export that also
			// carries the plaintext, and it must apply.
			{Username: "ci", Name: "agreeing", Token: "another-value", TokenHash: auth.HashToken("another-value"), CreatedAt: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Skipped) != 1 || !strings.Contains(sum.Skipped[0], "not the same credential") {
		t.Fatalf("skipped %v", sum.Skipped)
	}
	if row, err := h.Auth.TokenUser("one-value"); err != nil || row != nil {
		t.Fatalf("the contradicted token was stored anyway: %+v (%v)", row, err)
	}
	if row, err := h.Auth.TokenUser("another-value"); err != nil || row == nil {
		t.Fatalf("the agreeing row should have applied: %+v (%v)", row, err)
	}
}

// An EXPORT never emits a plaintext token — the host does not have one to emit, which is the whole
// reason the tokens table stores a digest.
//
// negative control: give ExportToken.Token no `omitempty` and set it during export — every exported
// document starts carrying `"token": ""`, and the field stops meaning "an author chose this".
func TestExportNeverCarriesAPlaintextToken(t *testing.T) {
	h := newHost(t)
	const chosen = "pstack_pat_round_trip"
	if _, err := h.Apply(&Document{
		Version: FormatVersion,
		Users:   []auth.ExportUser{{Username: "ci", Role: "viewer", PasswordHash: goodHash, CreatedAt: 1}},
		Tokens:  []auth.ExportToken{{Username: "ci", Name: "pipeline", Token: chosen, CreatedAt: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	doc, err := h.Assemble()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tokens) != 1 {
		t.Fatalf("tokens %+v", doc.Tokens)
	}
	if doc.Tokens[0].Token != "" {
		t.Fatalf("the export carried a plaintext token: %q", doc.Tokens[0].Token)
	}
	if doc.Tokens[0].TokenHash != auth.HashToken(chosen) {
		t.Fatalf("the export lost the digest: %+v", doc.Tokens[0])
	}
	// And the round trip still works: the hash alone reinstalls a working credential on a new host.
	h2 := newHost(t)
	if _, err := h2.Apply(&Document{Version: FormatVersion,
		Users:  []auth.ExportUser{{Username: "ci", Role: "viewer", PasswordHash: goodHash, CreatedAt: 1}},
		Tokens: doc.Tokens}); err != nil {
		t.Fatal(err)
	}
	if row, err := h2.Auth.TokenUser(chosen); err != nil || row == nil {
		t.Fatalf("the migrated token does not authenticate: %+v (%v)", row, err)
	}
}

// The pre-write summary distinguishes the two. A hash proves nothing and cannot be replayed; a
// plaintext token means whoever wrote the file HOLDS the credential.
//
// negative control: use one sentence for both — an operator accepting a file from somewhere else
// cannot tell which kind of grant they are being handed.
func TestTrustsNamesAPlaintextTokenAsStronger(t *testing.T) {
	hashed := (&Document{Tokens: []auth.ExportToken{{Username: "ci", Name: "a", TokenHash: strings.Repeat("a", 64)}}}).Trusts()
	plain := (&Document{Tokens: []auth.ExportToken{{Username: "ci", Name: "a", Token: "x"}}}).Trusts()
	if len(hashed) != 1 || len(plain) != 1 {
		t.Fatalf("hashed %v plain %v", hashed, plain)
	}
	if strings.Contains(hashed[0], "PLAINTEXT") {
		t.Errorf("a hashed token is described as plaintext: %q", hashed[0])
	}
	if !strings.Contains(plain[0], "PLAINTEXT") {
		t.Errorf("a plaintext token is not called out: %q", plain[0])
	}
}

// mintPair is a self-signed certificate for one name, valid now — the cheapest thing SetWildcard
// accepts, so a test can put a host into dns-persist-01 without a fixture file.
func mintPair(t *testing.T, name string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}
