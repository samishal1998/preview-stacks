package registries

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

const password = "ghp-super-secret-registry-token"

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func readCfg(t *testing.T, dir string) *omap.Map {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := omap.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*omap.Map)
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) || !IsError(err) {
		t.Fatalf("err = %v, want /%s/", err, substr)
	}
}

func TestPrivateRegistryCredentials(t *testing.T) {
	// negative control: each subtest names its own.
	t.Run("Docker Hub aliases collapse to the canonical key docker login writes", func(t *testing.T) {
		// negative control: remove "registry-1.docker.io" from hubAliases — it passes hostRe and
		// comes back unchanged, which fails the equality.
		// The trap: a credential stored under "docker.io" is silently never used for `nginx:alpine`,
		// because docker looks it up under https://index.docker.io/v1/.
		for _, alias := range []string{"docker.io", "index.docker.io", "registry-1.docker.io", "https://docker.io"} {
			got, err := NormalizeRegistry(alias)
			if err != nil || got != DockerHubKey {
				t.Fatalf("%s → %q, %v", alias, got, err)
			}
		}
		for in, want := range map[string]string{"ghcr.io": "ghcr.io", "registry.example.com:5000": "registry.example.com:5000", " ghcr.io/ ": "ghcr.io"} {
			got, err := NormalizeRegistry(in)
			if err != nil || got != want {
				t.Fatalf("%q → %q, %v", in, got, err)
			}
		}
	})

	t.Run("something that is not a registry host is refused", func(t *testing.T) {
		// negative control: allow `/` in hostRe's character class — ghcr.io/owner/image is accepted.
		for _, bad := range []string{"", "ghcr.io/owner/image", "not a host", "http://x/y"} {
			if _, err := NormalizeRegistry(bad); err == nil || !IsError(err) {
				t.Fatalf("%q accepted", bad)
			}
		}
		_, err := NormalizeRegistry("not a host")
		if err.Error() != `"not a host" does not look like a registry host. Use e.g. ghcr.io, registry.example.com:5000, or docker.io for Docker Hub.` {
			t.Fatalf("text: %s", err)
		}
	})

	t.Run("a stored credential is listed by host and username, never by secret", func(t *testing.T) {
		// negative control: put the raw `auth` into RegistryEntry.Username — the base64 check fails.
		store := New(t.TempDir())
		if _, err := store.Put("ghcr.io", "sami", password); err != nil {
			t.Fatal(err)
		}
		state := store.State()
		want := []RegistryEntry{{Registry: "ghcr.io", Username: jsonx.Str("sami"), ViaHelper: false}}
		if !reflect.DeepEqual(state.Entries, want) {
			t.Fatalf("entries = %+v", state.Entries)
		}
		// The whole state object, not just the field: base64 is reversible, so a leak anywhere is a leak.
		js := string(jsonx.Must(state))
		if strings.Contains(js, password) || strings.Contains(js, b64("sami:"+password)) {
			t.Fatalf("leak: %s", js)
		}
		if state.Helpers == nil || !state.Writable {
			t.Fatalf("state = %+v", state)
		}
	})

	t.Run("the file docker reads is the real thing, 0600, with the secret in `auth` only", func(t *testing.T) {
		// negative control: drop the Chmod(tmp, 0o600) — the mode assertion fails.
		dir := t.TempDir()
		if _, err := New(dir).Put("ghcr.io", "sami", password); err != nil {
			t.Fatal(err)
		}
		entry := readCfg(t, dir).GetMap("auths").GetMap("ghcr.io")
		if entry.GetString("auth") != b64("sami:"+password) {
			t.Fatalf("auth = %q", entry.GetString("auth"))
		}
		// Not also as separate plaintext fields, which docker accepts but which stores it twice.
		if entry.Has("username") || entry.Has("password") {
			t.Fatalf("plaintext fields: %v", entry.Keys())
		}
		st, _ := os.Stat(filepath.Join(dir, "config.json"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", st.Mode().Perm())
		}
		// And the bytes: two-space indent WITH the trailing newline docker's own writer leaves.
		b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if string(b) != "{\n  \"auths\": {\n    \"ghcr.io\": {\n      \"auth\": \""+b64("sami:"+password)+"\"\n    }\n  }\n}\n" {
			t.Fatalf("config.json:\n%s", b)
		}
	})

	t.Run("storing a second credential preserves the first, and everything else in the file", func(t *testing.T) {
		// negative control: start from omap.New() instead of read() in Put — credHelpers is gone.
		dir := t.TempDir()
		// A file that already has a helper and an unrelated key — docker writes both.
		os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credHelpers":{"gcr.io":"gcloud"},"currentContext":"default"}`), 0o600)
		store := New(dir)
		if _, err := store.Put("ghcr.io", "a", "p1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put("registry.example.com", "b", "p2"); err != nil {
			t.Fatal(err)
		}
		cfg := readCfg(t, dir)
		if got := cfg.GetMap("auths").Keys(); !reflect.DeepEqual(got, []string{"ghcr.io", "registry.example.com"}) {
			t.Fatalf("auths = %v", got)
		}
		if cfg.GetMap("credHelpers").GetString("gcr.io") != "gcloud" || cfg.GetString("currentContext") != "default" {
			t.Fatalf("lost keys: %s", jsonx.Must(cfg))
		}
		// File order: what docker wrote first, `auths` appended.
		if !reflect.DeepEqual(cfg.Keys(), []string{"credHelpers", "currentContext", "auths"}) {
			t.Fatalf("order = %v", cfg.Keys())
		}
		// And the helper is reported for its registry only.
		state := store.State()
		if !reflect.DeepEqual(state.Helpers, []string{"credHelpers[gcr.io]: gcloud"}) || state.Entries[0].ViaHelper {
			t.Fatalf("state = %+v", state)
		}
	})

	t.Run("credential helpers are reported, because they do not work in this container", func(t *testing.T) {
		// negative control: drop the credsStore line from Helpers — the list is empty.
		// Copying a laptop's config.json is the trap: on Docker Desktop it carries credsStore and NO
		// auths, the secrets being in the OS keychain. Inside the container that binary does not
		// exist, so every pull fails with `error getting credentials` and an empty auths looks like
		// the cause.
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credsStore":"desktop","auths":{}}`), 0o600)
		state := New(dir).State()
		if !reflect.DeepEqual(state.Helpers, []string{"credsStore: desktop"}) {
			t.Fatalf("helpers = %v", state.Helpers)
		}
	})

	t.Run("a corrupt config reads as no credentials rather than failing closed forever", func(t *testing.T) {
		// negative control: make read() keep a parse error as a fatal state — Put fails.
		// Failing the request would leave no way to fix the file from the API.
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600)
		if state := New(dir).State(); len(state.Entries) != 0 {
			t.Fatalf("entries = %+v", state.Entries)
		}
		// And a write repairs it.
		if _, err := New(dir).Put("ghcr.io", "sami", "p"); err != nil {
			t.Fatal(err)
		}
		if state := New(dir).State(); len(state.Entries) != 1 {
			t.Fatalf("entries = %+v", state.Entries)
		}
	})

	t.Run("removing reports whether there was anything to remove", func(t *testing.T) {
		// negative control: return true unconditionally from Remove — the second call's false fails.
		store := New(t.TempDir())
		if _, err := store.Put("ghcr.io", "sami", "p"); err != nil {
			t.Fatal(err)
		}
		if ok, err := store.Remove("ghcr.io"); !ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
		if ok, err := store.Remove("ghcr.io"); ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
		// Aliases resolve on the way out too, so you can delete what you created.
		if _, err := store.Put("docker.io", "sami", "p"); err != nil {
			t.Fatal(err)
		}
		if ok, err := store.Remove("index.docker.io"); !ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
	})

	t.Run("a missing username or password is refused", func(t *testing.T) {
		// negative control: check `username == ""` instead of the trimmed value — "  " is accepted.
		store := New(t.TempDir())
		_, err := store.Put("ghcr.io", "  ", "p")
		wantErr(t, err, "username is required")
		_, err = store.Put("ghcr.io", "sami", "")
		wantErr(t, err, "password or token is required")
		if _, err := os.Stat(filepath.Join(store.Dir, "config.json")); err == nil {
			t.Fatal("written")
		}
	})

	t.Run("the golden host's config.json reads as the reference served it", func(t *testing.T) {
		// negative control: swap the `auth` decode for the `username` field — ci is gone.
		b, err := os.ReadFile("../../../conformance/golden/host/control/docker/config.json")
		if err != nil {
			t.Skip(err)
		}
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
		state := New(dir).State()
		want := RegistryState{Dir: dir, Writable: true, Entries: []RegistryEntry{{Registry: "ghcr.io", Username: jsonx.Str("ci")}}, Helpers: []string{}}
		if !reflect.DeepEqual(state, want) {
			t.Fatalf("state = %s", jsonx.Must(state))
		}
	})

	t.Run("decodeUsername returns only the username half", func(t *testing.T) {
		// negative control: return the whole decoded string — "sami:pw" ≠ "sami".
		if got := DecodeUsername(b64("sami:pw")); got == nil || *got != "sami" {
			t.Fatalf("got %v", got)
		}
		if DecodeUsername("") != nil || DecodeUsername("not-base64-@@@") != nil {
			t.Fatal("expected nil")
		}
		// Measured on Bun: junk around a valid payload is skipped, `=` ends it, a bare colon is nobody.
		for in, want := range map[string]string{"c2FtaTpwdw==@@@": "sami", "@@@c2FtaTpwdw==": "sami", "c2FtaTpwdw==:junk": "sami", "c2FtaTpwdw": "sami", "c2FtaTpwdw====": "sami"} {
			if got := DecodeUsername(in); got == nil || *got != want {
				t.Fatalf("%q → %v", in, got)
			}
		}
		for _, in := range []string{b64(":pw"), b64("test")} {
			if DecodeUsername(in) != nil {
				t.Fatalf("%q → not nil", in)
			}
		}
	})
}
