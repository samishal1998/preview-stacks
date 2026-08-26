// Package config is the portable host configuration document: everything that makes a pstack host
// *this* host, in a form that can be carried to a new one.
//
// ── WHAT TRAVELS, AND WHY THAT LIST AND NOT A BACKUP ─────────────────────────────────────────────
//
// Portable, host-independent state only: accounts (argon2 hashes, so logins keep working), personal
// API tokens (sha256 hashes, same reasoning), host vars and secrets, notifier registrations with
// their signing secrets, the SSO providers and their client secrets, registry credentials, Traefik
// routing files, named specs.
//
// OUT, deliberately: deployments, sessions, `sso_state`, terminal audit, delivery history. Those
// describe what is happening on ONE host. Restoring a deployment row onto a machine where that
// stack does not run is not merely useless, it is a lie about the world (invariant 10), and a
// restored session id is a live credential nobody minted.
//
// ── THIS PACKAGE ADDS A ROUTE, NOT A READ PATH ───────────────────────────────────────────────────
//
// Every unmasked value here already had an accessor for the deploy path — `hostvars.ResolveMaps`,
// `webhooks.RawConfigOf`/`SecretOf`, `auth.ListSsoProviders`, `specs.Source`, `routing.Read`. This package
// calls those and writes no second way to read a secret. Two exceptions, both stated rather than
// hidden:
//
//   - Users and tokens had no hash-bearing listing at all. `auth.ExportUsers`/`ExportTokens` are new
//     read-only helpers, in the package that owns those tables, named to be conspicuous in a diff.
//   - `registries` deliberately has NO read path for a stored credential (its header says so). The
//     `auths` block of `config.json` is therefore read here, directly, and only for entries this
//     document can faithfully rebuild — see registryEntries.
//
// ── THE DOCUMENT IS A CREDENTIAL DUMP. TREAT EVERY CALLER AS ONE ─────────────────────────────────
//
// Assemble's return value is the most dangerous object in this codebase: one struct holding every
// secret on the host. It is plaintext by design (the CLI seals it, see seal.go), which puts the
// whole burden on WHO may call — `auth.KindRoot` only, never a browser session, or an XSS is a
// full credential exfiltration. That gate is the API's to enforce; this package cannot.
//
// ── APPLY CREATES OR SKIPS. IT NEVER UPDATES AND NEVER DELETES ───────────────────────────────────
//
// Upsert by natural key, where "upsert" stops at insert: an existing name is left exactly as it is
// and reported as skipped. Three reasons, all of them the same reason:
//
//   - Overwriting is how a hostile file replaces a working registry credential or repoints a
//     notifier. Refusing to touch what exists means a bad file can only ADD, which is visible in
//     the summary before it is written (Trusts).
//   - It makes apply-twice a byte-level no-op. An update path would re-run `specs.Put` and bump
//     `updatedAt` on every run.
//   - It makes a PARTIAL apply safe. There is no outer transaction — `hostvars.Put` opens its own
//     `Store.Tx`, and a nested `db.*` call inside one is a permanent self-deadlock (Go rule 16) —
//     so a failure halfway leaves a subset of the document applied. Because nothing is ever
//     replaced or deleted, re-running completes it.
package config

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/hostvars"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/notify"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registries"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/sso"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

// FormatVersion is the document AND envelope version. Bump only for a shape a previous pstack
// could not apply correctly — an added field that older code ignores is not a bump.
const FormatVersion = 1

// Error is a refused document, envelope or passphrase. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is an *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// ── the document ────────────────────────────────────────────────────────────────────────────────

// Document is the whole portable configuration. Every slice is non-nil at construction (Go rule 3).
type Document struct {
	Version int `json:"version"`
	// PstackVersion is what wrote it — outside any seal, so a refusal can name it.
	PstackVersion string `json:"pstackVersion"`
	ExportedAt    int64  `json:"exportedAt"`
	// Skipped is what Assemble could NOT carry, with the reason. Informational: apply ignores it.
	// It is in the document rather than in a second return value so the reason survives the seal
	// and is visible to whoever opens the file, not only to whoever ran the export.
	Skipped      []string           `json:"skipped"`
	Users        []auth.ExportUser  `json:"users"`
	Tokens       []auth.ExportToken `json:"tokens"`
	Vars         []Var              `json:"vars"`
	Notifiers    []Notifier         `json:"notifiers"`
	SSOProviders []SSOProvider      `json:"ssoProviders"`
	// SSO is the single-provider shape a pre-multi-provider pstack (≤0.30.0) exported. Accepted on
	// DECODE only — Parse folds it into SSOProviders under sso.Config.DerivedKey and clears it, so
	// an old export keeps applying — and never written: Assemble leaves it nil, and omitempty keeps
	// a nil out of new documents.
	SSO      *SSO          `json:"sso,omitempty"`
	Registry []Registry    `json:"registries"`
	Routing  []RoutingFile `json:"routing"`
	Specs    []Spec        `json:"specs"`
}

// String keeps a Document out of a log by accident. Every field on it is a credential or points at
// one, and `%v` on a struct prints all of them; the JSON encoders do not consult this method, so
// the export path is unaffected.
func (d *Document) String() string {
	return fmt.Sprintf("config.Document{version:%d, %d users, %d tokens, %d vars, %d notifiers, %d registries, %d routing files, %d specs — contents withheld}",
		d.Version, len(d.Users), len(d.Tokens), len(d.Vars), len(d.Notifiers), len(d.Registry), len(d.Routing), len(d.Specs))
}

// Var is one host variable or secret. Secret says which — a secret that arrives as a variable
// would become readable through the API on the new host.
type Var struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// Notifier is one registration, with the UNMASKED config and the signing secret. The secret travels
// because it is what receivers verify: minting a new one on the new host silently breaks every
// receiver's signature check, which is the failure this whole feature exists to avoid.
type Notifier struct {
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	Config    *omap.Map `json:"config"`
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	Secret    string    `json:"secret"`
	CreatedAt int64     `json:"createdAt"`
}

// SSOProvider is one stored identity provider: its slug, the config and the client secret it is
// paired with.
type SSOProvider struct {
	Key          string      `json:"key"`
	Config       *sso.Config `json:"config"`
	ClientSecret string      `json:"clientSecret"`
}

// SSO is the legacy single-provider pair — see the Document.SSO comment.
type SSO struct {
	Config       *sso.Config `json:"config"`
	ClientSecret string      `json:"clientSecret"`
}

// Registry is one docker registry credential, split into the pair `registries.Put` accepts.
type Registry struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// RoutingFile is one Traefik dynamic-config file.
type RoutingFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Spec is one named spec. Compose is the compose file stored beside it, when there is one.
type Spec struct {
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	Compose     *string `json:"compose,omitempty"`
	Description *string `json:"description,omitempty"`
}

// ── assembly ────────────────────────────────────────────────────────────────────────────────────

// Sources is everything the document is read from and written to. The API server already holds
// every one of these; Store is here because apply writes users, tokens and notifiers, and those
// three packages expose no way to insert a value that is ALREADY hashed or minted.
type Sources struct {
	Store      *store.Store
	Auth       *auth.Auth
	HostVars   *hostvars.HostVars
	Webhooks   *webhooks.Webhooks
	Registries *registries.RegistryAuthStore
	Routing    *routing.RoutingStore
	Specs      *specs.SpecStore
}

// Assemble reads the whole portable configuration. The result holds every secret on the host.
func (s Sources) Assemble() (*Document, error) {
	d := &Document{
		Version:       FormatVersion,
		PstackVersion: version.Get(),
		ExportedAt:    time.Now().UnixMilli(),
		Skipped:       []string{},
		Users:         []auth.ExportUser{},
		Tokens:        []auth.ExportToken{},
		Vars:          []Var{},
		Notifiers:     []Notifier{},
		SSOProviders:  []SSOProvider{},
		Registry:      []Registry{},
		Routing:       []RoutingFile{},
		Specs:         []Spec{},
	}
	var err error
	if d.Users, err = s.Auth.ExportUsers(); err != nil {
		return nil, err
	}
	if d.Tokens, err = s.Auth.ExportTokens(); err != nil {
		return nil, err
	}
	if d.Vars, err = s.vars(); err != nil {
		return nil, err
	}
	if d.Notifiers, err = s.notifiers(); err != nil {
		return nil, err
	}
	if d.SSOProviders, err = s.ssoProviders(); err != nil {
		return nil, err
	}
	regs, skipped := s.registryEntries()
	d.Registry, d.Skipped = regs, append(d.Skipped, skipped...)
	for _, f := range s.Routing.List() {
		content, err := s.Routing.Read(f.Name)
		if err != nil {
			d.Skipped = append(d.Skipped, "routing "+f.Name+": "+err.Error())
			continue
		}
		d.Routing = append(d.Routing, RoutingFile{Name: f.Name, Content: content})
	}
	if d.Specs, d.Skipped, err = s.specList(d.Skipped); err != nil {
		return nil, err
	}
	return d, nil
}

// vars is every host variable and secret. ResolveMaps is the full-value accessor — its own comment
// says a second caller outside the resolve path is the moment to ask why, so: this is that caller,
// and the answer is that a portable export without secret VALUES would carry names that resolve to
// nothing on the new host. SecretValues() is the other candidate and cannot be used: it returns
// values without names.
func (s Sources) vars() ([]Var, error) {
	vars, secrets, err := s.HostVars.ResolveMaps()
	if err != nil {
		return nil, err
	}
	out := []Var{}
	for _, m := range []struct {
		src    map[string]string
		secret bool
	}{{vars, false}, {secrets, true}} {
		names := make([]string, 0, len(m.src))
		for name := range m.src {
			names = append(names, name)
		}
		// Never range a Go map into output (Go rule 5).
		sort.Strings(names)
		for _, name := range names {
			out = append(out, Var{Name: name, Value: m.src[name], Secret: m.secret})
		}
	}
	return out, nil
}

// notifiers carries the RAW config, not the listed one.
//
// `Webhooks.List()` runs the public mask over `config`, which turns a Slack notifier's webhookUrl —
// the credential itself, for a type that does not sign — into `https://***`. Exporting that would
// install a notifier pointing at a mask on the new host. Invariant 15 records this exact confusion
// having already happened once here, which is why the raw config is fetched per row instead.
func (s Sources) notifiers() ([]Notifier, error) {
	rows, err := s.Webhooks.List()
	if err != nil {
		return nil, err
	}
	out := []Notifier{}
	for _, r := range rows {
		raw, err := s.Webhooks.RawConfigOf(r.ID)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			continue // deleted between the list and the read
		}
		secret, err := s.Webhooks.SecretOf(r.ID)
		if err != nil {
			return nil, err
		}
		n := Notifier{Type: r.Type, Name: r.Name, Config: raw, Events: r.Events, Enabled: r.Enabled, CreatedAt: r.CreatedAt}
		if secret != nil {
			n.Secret = *secret
		}
		out = append(out, n)
	}
	return out, nil
}

func (s Sources) ssoProviders() ([]SSOProvider, error) {
	rows, err := s.Auth.ListSsoProviders()
	if err != nil {
		return nil, err
	}
	out := []SSOProvider{}
	for _, r := range rows {
		out = append(out, SSOProvider{Key: r.Key, Config: r.Config, ClientSecret: r.ClientSecret})
	}
	return out, nil
}

// registryEntries is the `auths` block of the DOCKER_CONFIG config.json, restricted to entries
// `registries.Put` can rebuild EXACTLY. Anything else is skipped with its reason rather than
// half-carried:
//
//   - a credential-helper-backed entry has no secret in the file at all (the package header calls
//     transplanting one a trap — it fails on the new host with "error getting credentials");
//   - an entry with an `identitytoken` is a token-based login `Put` would silently drop, leaving a
//     401 that points nowhere;
//   - a key `NormalizeRegistry` rejects would be rewritten or refused on the way back in.
//
// Read here rather than through `registries` because that package deliberately exposes no read path
// for a stored credential. Writing still goes through `registries.Put`, so the atomic
// temp→chmod-0600→rename discipline has exactly one implementation.
func (s Sources) registryEntries() ([]Registry, []string) {
	out, skipped := []Registry{}, []string{}
	raw, err := os.ReadFile(filepath.Join(s.Registries.Dir, "config.json"))
	if err != nil {
		return out, skipped
	}
	parsed, err := omap.Parse(raw)
	if err != nil {
		return out, append(skipped, "registries: config.json does not parse")
	}
	cfg, ok := parsed.(*omap.Map)
	if !ok {
		return out, append(skipped, "registries: config.json is not an object")
	}
	credsStore := cfg.GetString("credsStore")
	credHelpers := cfg.GetMap("credHelpers")
	cfg.GetMap("auths").Each(func(key string, v any) {
		skip := func(why string) { skipped = append(skipped, "registry "+key+": "+why) }
		entry, ok := v.(*omap.Map)
		if !ok {
			skip("the entry is not an object")
			return
		}
		if credsStore != "" || (credHelpers != nil && credHelpers.GetString(key) != "") {
			skip("served by a credential helper, so the credential is not in this file — log in again on the new host")
			return
		}
		for _, k := range entry.Keys() {
			// `email` is legacy and inert; anything else (identitytoken, registrytoken) changes how
			// the credential is presented and would be dropped on the way back in.
			if k != "auth" && k != "email" {
				skip("the entry carries `" + k + "`, which this document cannot rebuild")
				return
			}
		}
		username, password, ok := splitAuth(entry.GetString("auth"))
		if !ok {
			skip("its `auth` is not base64 of user:password")
			return
		}
		normalized, err := normalizeForTravel(key)
		if err != nil {
			skip("this key would not survive normalization: " + err.Error())
			return
		}
		out = append(out, Registry{Registry: normalized, Username: username, Password: password})
	})
	// Byte order, as registries.State() sorts (Go rule 6).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Registry < out[j].Registry })
	return out, skipped
}

// normalizeForTravel is the config.json key as a value NormalizeRegistry accepts back.
//
// Docker Hub needs the special case, and it is not an edge case — it is the commonest credential on
// any host. `docker login` writes the key `https://index.docker.io/v1/`, and NormalizeRegistry
// REJECTS that string (verified): it strips the trailing slash to `https://index.docker.io/v1`,
// which is not one of the aliases it collapses, and its host pattern admits no path. Carrying the
// friendly `docker.io` instead round-trips exactly — Put normalizes it straight back to the
// canonical key, which is also what State() reports, so the apply-side existence check still
// matches. Without this, the one credential most hosts have is the one that does not travel.
func normalizeForTravel(key string) (string, error) {
	if key == registries.DockerHubKey {
		return "docker.io", nil
	}
	return registries.NormalizeRegistry(key)
}

// splitAuth is the `auth` blob as the pair that produced it. Strict standard base64 on purpose: a
// blob this does not decode is one `Put` could not reproduce byte for byte, and a silently mangled
// credential is worse than a reported skip.
func splitAuth(a string) (username, password string, ok bool) {
	if a == "" {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(a)
	if err != nil {
		return "", "", false
	}
	u, p, found := strings.Cut(string(decoded), ":")
	if !found || u == "" || p == "" {
		return "", "", false
	}
	return u, p, true
}

func (s Sources) specList(skipped []string) ([]Spec, []string, error) {
	out := []Spec{}
	list, err := s.Specs.List()
	if err != nil {
		return nil, skipped, err
	}
	for _, meta := range list {
		stored, err := s.Specs.Get(meta.Name)
		if err != nil || stored == nil {
			skipped = append(skipped, "spec "+meta.Name+": could not be read")
			continue
		}
		source, err := s.Specs.Source(meta.Name)
		if err != nil {
			skipped = append(skipped, "spec "+meta.Name+": "+err.Error())
			continue
		}
		sp := Spec{Name: meta.Name, Source: source}
		// compose.yml is read from the spec's directory the way the deploy route reads it
		// (routes_deploy.go) — the store has no accessor for it, and a spec that references a
		// compose file is broken on the new host without it.
		if b, err := os.ReadFile(filepath.Join(stored.Dir, "compose.yml")); err == nil {
			sp.Compose = jsonx.Str(string(b))
		}
		if meta.Description != "" {
			sp.Description = jsonx.Str(meta.Description)
		}
		out = append(out, sp)
	}
	return out, skipped, nil
}

// ── what applying would make this host trust ────────────────────────────────────────────────────

// Trusts is the pre-write summary: the registries the new host would pull images from and the URLs
// it would post to. Every string in a notifier's config is listed, not just the field named `url` —
// for a chat notifier the URL *is* the credential, and a type added later may name its field
// anything. A hostile config file otherwise silently repoints image pulls at an attacker.
func (d *Document) Trusts() []string {
	out := []string{}
	// Accounts and tokens FIRST, because they are the larger grant and the one an operator skimming
	// this list would otherwise never see. A document naming no registry at all used to print an
	// empty list, which reads as "this file is inert" — the most dangerous thing this summary could
	// say about a file that is about to create an administrator.
	for _, u := range d.Users {
		out = append(out, fmt.Sprintf("sign in as %q with the role %q", u.Username, u.Role))
	}
	for _, t := range d.Tokens {
		// A plaintext token is named as one. Whoever wrote the document HOLDS that credential —
		// unlike a hash, which proves nothing and cannot be replayed — and an operator accepting a
		// file from somewhere else is entitled to know which of the two they are being handed.
		how := "the token %q, as %q"
		if t.Token != "" {
			how = "the token %q, as %q — carried in PLAINTEXT, so whoever wrote this file holds it"
		}
		out = append(out, fmt.Sprintf("call this API with "+how, t.Name, t.Username))
	}
	// The SSO providers are the widest grant of all: each delegates who may sign in — and with what
	// role — to whoever controls that issuer. EVERY provider is named, not the first.
	for _, p := range d.SSOProviders {
		if p.Config == nil {
			continue
		}
		c := p.Config
		where := c.DiscoveryURL
		if where == "" {
			where = c.AuthorizeURL
		}
		if where == "" {
			where = c.Provider
		}
		// An EMPTY role means "inherit this host's default", and printing `""` here said nothing at
		// all about a grant that could be substantial. This summary is the last thing an operator
		// reads before accepting a document from somewhere else; it does not get to be coy.
		role := fmt.Sprintf("%q", c.DefaultRole)
		if c.DefaultRole == "" {
			role = "whatever this host's default role is (never admin — an inheriting provider is capped below it)"
		}
		out = append(out, fmt.Sprintf("let %s sign people in (%q), giving each new account %s", where, c.Label, role))
	}
	for _, r := range d.Registry {
		out = append(out, fmt.Sprintf("pull images from %s as %s", r.Registry, r.Username))
	}
	for _, n := range d.Notifiers {
		if n.Config == nil {
			continue
		}
		n.Config.Each(func(k string, v any) {
			if str, ok := v.(string); ok && str != "" {
				out = append(out, fmt.Sprintf("send %s notifications to %s=%s (%q)", n.Type, k, str, n.Name))
			}
		})
	}
	return out
}

// ── apply ───────────────────────────────────────────────────────────────────────────────────────

// Summary is what apply did. Both lists are non-nil and carry human-readable lines, not ids: the
// caller printing them is a CLI operator deciding whether the result is what they meant.
type Summary struct {
	Created []string `json:"created"`
	Skipped []string `json:"skipped"`
}

func (s *Summary) created(what string) { s.Created = append(s.Created, what) }
func (s *Summary) skip(what, why string) {
	s.Skipped = append(s.Skipped, what+": "+why)
}

// Parse decodes a plaintext document and refuses a version it does not understand — by name, and
// before touching anything, rather than applying the half of it that happens to still fit.
func Parse(plaintext []byte) (*Document, error) {
	var d Document
	if err := json.Unmarshal(plaintext, &d); err != nil {
		return nil, &Error{"this is not a pstack config document: " + err.Error()}
	}
	if d.Version != FormatVersion {
		writer := "an unknown pstack version"
		if d.PstackVersion != "" {
			writer = "pstack " + d.PstackVersion
		}
		return nil, &Error{fmt.Sprintf("this is config document version %d, written by %s; this is pstack %s, which understands version %d. Upgrade pstack to apply it.",
			d.Version, writer, version.Get(), FormatVersion)}
	}
	d.foldLegacySSO()
	// A role is REQUIRED, and this is the only place that can say so before anything is written.
	//
	// `applyUsers` used to read an empty role as "admin", copying the convention `auth` uses for the
	// operator's OWN sso defaultRole. That convention is indefensible here: this document is UNTRUSTED
	// input, and under it the way to obtain an administrator was to OMIT the field rather than ask for
	// it — the one spelling that no reviewer reading the file would notice. Refuse the document whole,
	// naming the account, so a half-applied import cannot leave the account behind either.
	for _, u := range d.Users {
		if strings.TrimSpace(u.Role) == "" {
			return nil, &Error{"account " + strconv.Quote(u.Username) + " has no role — every account in a config document must name one explicitly, because a missing role cannot be told apart from an intended privilege"}
		}
	}
	return &d, nil
}

// foldLegacySSO maps the pre-multi-provider "sso" object into SSOProviders under the derived key —
// the key store migration 7 gives the same config when it migrates the database instead of a
// document. Parse calls it, and Apply again (Apply is exported and reachable without Parse — the
// applyUsers role guard sets the precedent), so a 0.30.0 export lands identically either way.
func (d *Document) foldLegacySSO() {
	if d.SSO == nil {
		return
	}
	if d.SSO.Config != nil {
		d.SSOProviders = append(d.SSOProviders, SSOProvider{Key: d.SSO.Config.DerivedKey(), Config: d.SSO.Config, ClientSecret: d.SSO.ClientSecret})
	}
	d.SSO = nil
}

// Apply creates what is missing and leaves everything that exists alone. See the package header for
// why there is no transaction and why nothing is ever updated or deleted.
func (s Sources) Apply(d *Document) (*Summary, error) {
	if d == nil {
		return nil, &Error{"no document to apply"}
	}
	d.foldLegacySSO()
	if d.Version != FormatVersion {
		return nil, &Error{fmt.Sprintf("refusing config document version %d — this pstack understands version %d", d.Version, FormatVersion)}
	}
	sum := &Summary{Created: []string{}, Skipped: []string{}}
	for _, step := range []func(*Document, *Summary) error{
		s.applyUsers,  // before tokens: a token references its user
		s.applyTokens, //
		s.applyVars,   //
		s.applyNotifiers,
		s.applySSO,
		s.applyRegistries,
		s.applyRouting,
		s.applySpecs,
	} {
		if err := step(d, sum); err != nil {
			return nil, err
		}
	}
	return sum, nil
}

// usernameRe is auth's rule, copied. auth enforces it only inside CreateUser, which hashes a
// plaintext password and so cannot be used to import an existing hash. Keep the two greppably
// identical: internal/auth/auth.go `var username`.
var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// argon2Re is the shape phc.go's VerifyPassword parses. Anything else is not a password hash, and
// storing it would create an account nothing can ever log into — VerifyPassword fails closed on a
// malformed string (verified), so this is defence in depth, not the only guard.
var argon2Re = regexp.MustCompile(`^\$argon2(id|i)\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)

// sha256Re is the shape auth stores a session or token digest in.
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// notifierNameRe is webhooks' rule, copied for the same reason usernameRe is: Create enforces it,
// and Create is unusable here because it mints a new signing secret. Keep the two greppably
// identical: internal/webhooks/webhooks.go `var nameRe`.
var notifierNameRe = regexp.MustCompile(`^[\w][\w .:@/-]{0,63}$`)

func (s Sources) applyUsers(d *Document, sum *Summary) error {
	for _, u := range d.Users {
		what := "user " + u.Username
		switch {
		case !usernameRe.MatchString(u.Username):
			sum.skip(what, "not a usable username")
			continue
		case !argon2Re.MatchString(u.PasswordHash):
			sum.skip(what, "its password hash is not an argon2 PHC string")
			continue
		}
		var id int64
		err := s.Store.DB.QueryRow("SELECT id FROM users WHERE username = ?", u.Username).Scan(&id)
		switch {
		case err == nil:
			sum.skip(what, "an account with that name already exists here")
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		// Parse refuses an empty role, so this cannot fire through the normal path. It stays because
		// Apply is exported and a future caller could reach it without Parse — and the failure it
		// guards against is silently minting an administrator.
		role := strings.TrimSpace(u.Role)
		if role == "" {
			return &Error{"account " + strconv.Quote(u.Username) + " has no role"}
		}
		var email sql.NullString
		if u.Email != nil && *u.Email != "" {
			email = sql.NullString{String: *u.Email, Valid: true}
		}
		if _, err := s.Store.DB.Exec(
			"INSERT INTO users (username, password_hash, role, email, created_at) VALUES (?, ?, ?, ?, ?)",
			u.Username, u.PasswordHash, role, email, u.CreatedAt); err != nil {
			return err
		}
		sum.created(what)
	}
	return nil
}

func (s Sources) applyTokens(d *Document, sum *Summary) error {
	for _, t := range d.Tokens {
		what := fmt.Sprintf("token %q of %s", t.Name, t.Username)
		if strings.TrimSpace(t.Name) == "" {
			// CreateToken's rule: the name is the only handle left once the secret is gone.
			sum.skip(what, "it has no name")
			continue
		}
		// A hand-authored document may declare the token itself rather than its digest. Hashing it
		// here — with the same function `CreateToken` uses — is what lets an operator write the
		// credentials a rebuilt host should come up holding.
		hash := t.TokenHash
		if t.Token != "" {
			derived := auth.HashToken(t.Token)
			// Both, disagreeing, is a document saying two different things about one credential.
			// Guessing which it meant would silently install a token the author did not intend; a
			// skip with the reason is the only honest answer.
			if hash != "" && !strings.EqualFold(hash, derived) {
				sum.skip(what, "it carries both a token and a tokenHash, and they are not the same credential")
				continue
			}
			hash = derived
		}
		if !sha256Re.MatchString(hash) {
			sum.skip(what, "its hash is not a sha256 digest")
			continue
		}
		var userID int64
		err := s.Store.DB.QueryRow("SELECT id FROM users WHERE username = ?", t.Username).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			sum.skip(what, "there is no account "+t.Username+" here")
			continue
		}
		if err != nil {
			return err
		}
		var id int64
		err = s.Store.DB.QueryRow("SELECT id FROM tokens WHERE token_hash = ?", hash).Scan(&id)
		switch {
		case err == nil:
			sum.skip(what, "that token already exists here")
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := s.Store.DB.Exec(
			"INSERT INTO tokens (user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?)",
			userID, t.Name, hash, t.CreatedAt); err != nil {
			return err
		}
		sum.created(what)
	}
	return nil
}

func (s Sources) applyVars(d *Document, sum *Summary) error {
	existing, err := s.HostVars.List()
	if err != nil {
		return err
	}
	here := map[string]bool{}
	for _, r := range existing {
		here[r.Name] = true
	}
	for _, v := range d.Vars {
		what := "var " + v.Name
		if v.Secret {
			what = "secret " + v.Name
		}
		if here[v.Name] {
			sum.skip(what, "already set on this host")
			continue
		}
		// Put validates the name and refuses an empty value, and is create-or-REPLACE — which is
		// why the existence check above is not optional.
		if _, err := s.HostVars.Put(v.Name, v.Value, v.Secret); err != nil {
			if hostvars.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		here[v.Name] = true
		sum.created(what)
	}
	return nil
}

// applyNotifiers inserts directly rather than through webhooks.Create, which MINTS A NEW SIGNING
// SECRET (CreateArgs has no field for an existing one). A new secret means every receiver's
// signature check fails on the new host, which is precisely the breakage this document exists to
// prevent — so the carried secret is written as-is, and Create's validations are replicated here.
func (s Sources) applyNotifiers(d *Document, sum *Summary) error {
	for _, n := range d.Notifiers {
		what := fmt.Sprintf("notifier %q", n.Name)
		if !notifierNameRe.MatchString(n.Name) {
			sum.skip(what, "not a usable notifier name")
			continue
		}
		t, err := notify.TypeOf(n.Type)
		if err != nil {
			sum.skip(what, err.Error())
			continue
		}
		if len(n.Events) == 0 {
			sum.skip(what, "it subscribes to nothing")
			continue
		}
		unknown := []string{}
		for _, e := range n.Events {
			if !events.IsSubscribable(e) {
				unknown = append(unknown, e)
			}
		}
		if len(unknown) > 0 {
			// Refused at write time, exactly as Create does: a notifier subscribed to a name this
			// build never emits is silence that looks like a working registration.
			sum.skip(what, "unknown event(s): "+strings.Join(unknown, ", "))
			continue
		}
		cfg := n.Config
		if cfg == nil {
			cfg = omap.New()
		}
		if err := notify.ValidateConfig(n.Type, cfg); err != nil {
			sum.skip(what, err.Error())
			continue
		}
		if t.Signs && n.Secret == "" {
			sum.skip(what, "it signs but carries no signing secret")
			continue
		}
		var id int64
		err = s.Store.DB.QueryRow("SELECT id FROM notifiers WHERE type = ? AND name = ?", n.Type, n.Name).Scan(&id)
		switch {
		case err == nil:
			sum.skip(what, "a "+n.Type+" notifier with that name already exists here")
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		cfgJSON, err := jsonx.Marshal(cfg)
		if err != nil {
			return err
		}
		evs, err := jsonx.Marshal(n.Events)
		if err != nil {
			return err
		}
		enabled := 0
		if n.Enabled {
			enabled = 1
		}
		if _, err := s.Store.DB.Exec(
			"INSERT INTO notifiers (type, name, config, events, secret, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			n.Type, n.Name, string(cfgJSON), string(evs), n.Secret, enabled, n.CreatedAt); err != nil {
			return err
		}
		sum.created(what)
	}
	return nil
}

// applySSO re-validates each provider through sso.ParseConfig rather than trusting the decoded
// struct: that is the same door the API's save uses, and it normalizes the allow-lists (a
// case-mismatched email domain would otherwise fail closed and lock everyone out). Creates-never-
// updates PER KEY: a key that exists here keeps its config and its secret, whatever the file says.
func (s Sources) applySSO(d *Document, sum *Summary) error {
	for _, p := range d.SSOProviders {
		if p.Config == nil {
			continue
		}
		what := "sso provider " + p.Key
		existing, err := s.Auth.SsoProvider(p.Key)
		if err != nil {
			return err
		}
		if existing != nil {
			sum.skip(what, "this host already has an SSO provider under that key")
			continue
		}
		if p.ClientSecret == "" {
			sum.skip(what, "it carries no client secret, and an empty one keeps the stored secret — there is none under that key")
			continue
		}
		body, err := jsonx.Marshal(p.Config)
		if err != nil {
			return err
		}
		v, err := omap.Parse(body)
		if err != nil {
			sum.skip(what, "its config does not parse")
			continue
		}
		cfg, err := sso.ParseConfig(v)
		if err != nil {
			sum.skip(what, err.Error())
			continue
		}
		if err := s.Auth.SetSsoProvider(p.Key, cfg, p.ClientSecret); err != nil {
			if auth.IsError(err) || sso.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		sum.created(what)
	}
	return nil
}

func (s Sources) applyRegistries(d *Document, sum *Summary) error {
	here := map[string]bool{}
	for _, e := range s.Registries.State().Entries {
		here[e.Registry] = true
	}
	for _, r := range d.Registry {
		what := "registry " + r.Registry
		key, err := registries.NormalizeRegistry(r.Registry)
		if err != nil {
			sum.skip(what, err.Error())
			continue
		}
		if here[key] {
			sum.skip(what, "a credential for it is already stored here")
			continue
		}
		if _, err := s.Registries.Put(key, r.Username, r.Password); err != nil {
			if registries.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		here[key] = true
		sum.created(what + " as " + r.Username)
	}
	return nil
}

func (s Sources) applyRouting(d *Document, sum *Summary) error {
	here := map[string]bool{}
	for _, f := range s.Routing.List() {
		here[f.Name] = true
	}
	for _, f := range d.Routing {
		what := "routing file " + f.Name
		if here[f.Name] {
			sum.skip(what, "a file with that name is already here")
			continue
		}
		// Write validates the name and the content, and would REPLACE — hence the check above.
		if _, err := s.Routing.Write(f.Name, f.Content); err != nil {
			if routing.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		here[f.Name] = true
		sum.created(what)
	}
	return nil
}

func (s Sources) applySpecs(d *Document, sum *Summary) error {
	for _, sp := range d.Specs {
		what := "spec " + sp.Name
		stored, err := s.Specs.Get(sp.Name)
		if err != nil {
			if specs.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		if stored != nil {
			sum.skip(what, "a spec with that name is already here")
			continue
		}
		// Put validates, and would replace — hence the check above.
		if _, err := s.Specs.Put(sp.Name, sp.Source, specs.PutOptions{ComposeYaml: sp.Compose, Description: sp.Description}); err != nil {
			if specs.IsError(err) {
				sum.skip(what, err.Error())
				continue
			}
			return err
		}
		sum.created(what)
	}
	return nil
}
