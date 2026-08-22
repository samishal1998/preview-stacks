// Package registries is private registry credentials for the control plane.
//
// ── WHY THE HOST BEING LOGGED IN IS NOT ENOUGH ───────────────────────────────────────────────────
//
// An image pull is authenticated by the **client**, not the daemon. `docker pull` reads the client's
// own `config.json`, finds the entry for that registry, and sends it to the daemon in an
// `X-Registry-Auth` header with the create-image call. The daemon never consults the client's
// config — it just uses whatever credential it was handed.
//
// pstack shells out to `docker compose` from **inside the control container**, so the client is the
// docker CLI in that container, and its `config.json` is the one that matters. A `docker login` run
// on the host writes `/root/.docker/config.json` on the *host*, which that container cannot see. The
// result is `pull access denied` for a private image on a host that is demonstrably logged in — and
// nothing in the error points at the reason.
//
// So the control stack mounts a `DOCKER_CONFIG` directory, and this module manages the file in it.
//
// ── ON DEMAND, WITH NO RESTART ───────────────────────────────────────────────────────────────────
//
// The CLI reads `config.json` on **every** invocation, so a credential added now applies to the
// next pull. Nothing needs recreating, and there is no cache to bust. Two ways in, both landing in
// the same file:
//
//   - from the host:  `docker login --config /var/lib/pstack/control/docker <registry>`
//   - from the API:   `PUT /api/registries/:host  { username, password }`
//
// ── `auths` IS NOT ENCRYPTION ────────────────────────────────────────────────────────────────────
//
// A config.json entry is `base64("user:password")` — trivially reversible, which is why this module
// NEVER returns it. `State()` gives hostnames and usernames only; there is no read path for the
// secret and no route that exposes one. The file is written 0600, atomically, and the token that can
// write here already commands a read-write Docker socket, so the boundary that matters is the API's,
// not this file's.
//
// ── CREDENTIAL HELPERS DO NOT TRANSPLANT ─────────────────────────────────────────────────────────
//
// Copying a laptop's `config.json` in is a trap: on macOS and Docker Desktop it usually contains
// `credsStore: "desktop"` or `"osxkeychain"` and **no** `auths` at all, because the secrets live in
// the OS keychain. Inside this container that helper binary does not exist, so every pull fails with
// `error getting credentials`. `State()` reports helpers so the UI can say that rather than let
// someone debug an empty `auths`.
//
// The file is handled as an ordered document (*omap.Map): everything docker wrote that this module
// does not understand is preserved, in place.
package registries

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// Error is a refused host, a missing field or an unwritable directory. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// DockerHubKey is Docker Hub's canonical key. `docker login docker.io` writes THIS, not
// `docker.io` — so a credential stored under the friendly name is silently never used for
// `nginx:alpine`.
const DockerHubKey = "https://index.docker.io/v1/"

var hubAliases = map[string]bool{
	"docker.io":               true,
	"index.docker.io":         true,
	"registry-1.docker.io":    true,
	"https://docker.io":       true,
	"https://index.docker.io": true,
	DockerHubKey:              true,
}

// A host, optionally with a port, optionally with a scheme docker tolerates. No path: a config key
// is a registry, not a URL to an image.
var hostRe = regexp.MustCompile(`(?i)^(https?://)?[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?$`)

var trailingSlashes = regexp.MustCompile(`/+$`)

// NormalizeRegistry is a registry host, as docker's config keys them. Hub's aliases collapse to the
// canonical key.
func NormalizeRegistry(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", &Error{"a registry host is required"}
	}
	bare := trailingSlashes.ReplaceAllString(trimmed, "")
	if hubAliases[bare] || hubAliases[strings.ToLower(bare)] {
		return DockerHubKey, nil
	}
	if !hostRe.MatchString(bare) {
		return "", &Error{fmt.Sprintf(`"%s" does not look like a registry host. Use e.g. ghcr.io, registry.example.com:5000, or docker.io for Docker Hub.`, input)}
	}
	return bare, nil
}

// RegistryEntry is what a caller may know about a stored credential. Never the secret.
type RegistryEntry struct {
	Registry string `json:"registry"`
	// Username is decoded from the stored `auth`, because knowing *which account* is the point of a
	// list view. null when it cannot be told.
	Username *string `json:"username"`
	// ViaHelper is true when this entry is served by a credential helper rather than a stored secret.
	ViaHelper bool `json:"viaHelper"`
}

// RegistryState is the GET /api/registries body.
type RegistryState struct {
	// Dir is the DOCKER_CONFIG directory, as this process sees it.
	Dir string `json:"dir"`
	// Writable is false on a control stack that predates the mount — the fix is `pstack init`.
	Writable bool            `json:"writable"`
	Entries  []RegistryEntry `json:"entries"`
	// Helpers are the `credsStore` / `credHelpers` found in the file. Present here means "these will
	// not work in this container", which is a different problem from having no credentials at all.
	Helpers []string `json:"helpers"`
}

// RegistryAuthStore manages config.json in a DOCKER_CONFIG directory. Stateless apart from Dir;
// safe to share. No lock: the write is whole-file and atomic, last one wins.
type RegistryAuthStore struct {
	Dir string
}

// New returns the store over dir.
func New(dir string) *RegistryAuthStore { return &RegistryAuthStore{Dir: dir} }

func (s *RegistryAuthStore) path() string { return filepath.Join(s.Dir, "config.json") }

// read is the file as a document. Absent or unparseable both mean "no usable credentials".
// Refusing to start over a corrupt file would leave no way to fix it from the API.
func (s *RegistryAuthStore) read() *omap.Map {
	raw, err := os.ReadFile(s.path())
	if err != nil {
		return omap.New()
	}
	v, err := omap.Parse(raw)
	if err != nil {
		return omap.New()
	}
	if m, ok := v.(*omap.Map); ok {
		return m
	}
	return omap.New()
}

// Writable is true when this process can write the config — probed by writing, so a `:ro` mount
// is caught.
func (s *RegistryAuthStore) Writable() bool {
	if err := os.MkdirAll(s.Dir, 0o777); err != nil {
		return false
	}
	probe := filepath.Join(s.Dir, fmt.Sprintf(".pstack-probe-%d", os.Getpid()))
	if err := os.WriteFile(probe, nil, 0o666); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// State is the list view: hostnames, usernames, helpers. Never a secret.
func (s *RegistryAuthStore) State() RegistryState {
	cfg := s.read()
	credsStore, _ := cfg.Get("credsStore")
	credHelpers := cfg.GetMap("credHelpers")
	helperFor := func(registry string) bool {
		if truthy(credsStore) {
			return true
		}
		v, _ := credHelpers.Get(registry)
		return truthy(v)
	}

	entries := []RegistryEntry{}
	cfg.GetMap("auths").Each(func(registry string, v any) {
		e := RegistryEntry{Registry: registry, ViaHelper: helperFor(registry)}
		if m, ok := v.(*omap.Map); ok {
			// decodeUsername(v?.auth) ?? v?.username ?? null
			e.Username = DecodeUsername(m.GetString("auth"))
			if e.Username == nil {
				if u, ok := m.Get("username"); ok && u != nil {
					e.Username = jsonx.Str(js.ToString(u))
				}
			}
		}
		entries = append(entries, e)
	})

	helpers := []string{}
	if truthy(credsStore) {
		helpers = append(helpers, "credsStore: "+js.ToString(credsStore))
	}
	credHelpers.Each(func(r string, h any) {
		helpers = append(helpers, fmt.Sprintf("credHelpers[%s]: %s", r, js.ToString(h)))
	})

	// Byte order where the reference used localeCompare (rule 6).
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Registry < entries[j].Registry })
	return RegistryState{Dir: s.Dir, Writable: s.Writable(), Entries: entries, Helpers: helpers}
}

// Put stores one credential, preserving everything else in the file. Returns the normalized key.
//
// Written atomically for the same reason Traefik's config is: `docker compose` may read this file
// at any moment, and a truncated-then-filled config is a parse error that looks like having no
// credentials at all.
func (s *RegistryAuthStore) Put(registryInput, username, password string) (string, error) {
	registry, err := NormalizeRegistry(registryInput)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(username) == "" {
		return "", &Error{"a username is required"}
	}
	if password == "" {
		return "", &Error{"a password or token is required"}
	}
	if !s.Writable() {
		return "", &Error{fmt.Sprintf("the registry credential directory is not writable from here (%s). The control stack mounts it into the API from 0.7.0 onward — re-run `pstack init` on the host.", s.Dir)}
	}

	cfg := s.read()
	// cfg.auths = { ...(cfg.auths ?? {}) }: an existing `auths` keeps its place, a new one appends.
	auths := cfg.GetMap("auths")
	if auths == nil {
		auths = omap.New()
		cfg.Set("auths", auths)
	}
	// `auth` only — never `username`/`password` as separate plaintext fields, which docker also
	// accepts but which puts the secret in the file twice.
	auths.Set(registry, omap.From("auth", base64.StdEncoding.EncodeToString([]byte(username+":"+password))))
	if err := s.write(cfg); err != nil {
		return "", err
	}
	return registry, nil
}

// Remove deletes one. Returns false when it was not there, so a caller can answer 404 rather than lie.
func (s *RegistryAuthStore) Remove(registryInput string) (bool, error) {
	registry, err := NormalizeRegistry(registryInput)
	if err != nil {
		return false, err
	}
	if !s.Writable() {
		return false, &Error{fmt.Sprintf("the registry credential directory is not writable from here (%s).", s.Dir)}
	}
	cfg := s.read()
	auths := cfg.GetMap("auths")
	if auths == nil || !auths.Has(registry) {
		return false, nil
	}
	auths.Delete(registry)
	if err := s.write(cfg); err != nil {
		return false, err
	}
	return true, nil
}

// write is `${JSON.stringify(cfg, null, 2)}\n` — WITH a trailing newline, unlike meta.json — to a
// temp file chmod 0600 BEFORE the rename, so the file is never briefly world-readable at its real
// name; then a best-effort 0700 on the directory.
func (s *RegistryAuthStore) write(cfg *omap.Map) error {
	if err := os.MkdirAll(s.Dir, 0o777); err != nil {
		return &Error{"could not write the registry credentials: " + err.Error()}
	}
	tmp := filepath.Join(s.Dir, fmt.Sprintf(".pstack-tmp-%d.json", os.Getpid()))
	fail := func(err error) error {
		_ = os.Remove(tmp)
		return &Error{"could not write the registry credentials: " + err.Error()}
	}
	b, err := jsonx.MarshalIndent(cfg)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(tmp, append(b, '\n'), 0o666); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return fail(err)
	}
	_ = os.Chmod(s.Dir, 0o700)
	return nil
}

// DecodeUsername is the username half of a stored `auth`, and only that half — nil when there is
// none to tell.
//
// Exported for testing. A caller wanting the password should not find a helper here that returns
// it — there is deliberately no such function in this module.
func DecodeUsername(auth string) *string {
	if auth == "" {
		return nil
	}
	decoded, ok := bufferBase64(auth)
	if !ok {
		return nil
	}
	colon := strings.IndexByte(decoded, ':')
	if colon <= 0 {
		return nil
	}
	return jsonx.Str(decoded[:colon])
}

// bufferBase64 is Buffer.from(s, 'base64').toString(): measured on Bun, the decoder skips every
// character outside the alphabet, takes `-`/`_` as the URL-safe pair, stops at the first `=`, and
// drops a dangling sextet. js.B64Decode is stricter (an unknown byte is an error), which would turn
// a junk-suffixed `auth` into "no username" where the reference showed one.
func bufferBase64(s string) (string, bool) {
	var clean strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '=':
			i = len(s)
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean.WriteByte(c)
		case c == '-':
			clean.WriteByte('+')
		case c == '_':
			clean.WriteByte('/')
		}
	}
	t := clean.String()
	if len(t)%4 == 1 {
		t = t[:len(t)-1]
	}
	b, err := base64.RawStdEncoding.DecodeString(t)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// truthy is JavaScript's `!!v` over a document value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return true
}
