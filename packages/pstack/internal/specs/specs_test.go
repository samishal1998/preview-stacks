package specs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// SPEC is the fixture the reference suite used: two variables it cannot satisfy, PR and GIT_SHA.
func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "spec.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNamedSpecsStoreOnceReferenceMany(t *testing.T) {
	// negative control: each subtest names its own.
	t.Run("reports the variables a caller must supply", func(t *testing.T) {
		// negative control: return after the first round in FindRequiredVars — only PR is found
		// (env.TAG is parsed before stack, so GIT_SHA comes first; either way one is missing).
		// Surfaced up front so a list view can say "this needs PR and GIT_SHA", instead of the
		// caller discovering them one 400 at a time.
		if got := FindRequiredVars(fixture(t)); !reflect.DeepEqual(got, []string{"GIT_SHA", "PR"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("required vars are NOT satisfied by the server's own environment", func(t *testing.T) {
		// negative control: seed env from os.Environ() in FindRequiredVars — the list comes back empty.
		// Otherwise a spec would validate on a box where PR happens to be exported and fail on one
		// where it is not — a works-on-my-box that only appears in production.
		t.Setenv("PSTACK_TEST_LEAKY", "set")
		if got := FindRequiredVars("version: 1\nstack: s-${PSTACK_TEST_LEAKY}\naxes: []"); !reflect.DeepEqual(got, []string{"PSTACK_TEST_LEAKY"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("stores and reads back a spec with its kind", func(t *testing.T) {
		// negative control: write "shared" as the kind in Put — the kind assertion fails.
		store := New(t.TempDir())
		desc := "the web app"
		stored, err := store.Put("web", fixture(t), PutOptions{Description: &desc})
		if err != nil {
			t.Fatal(err)
		}
		if stored.Kind != "isolated" {
			t.Fatalf("kind = %q", stored.Kind)
		}
		if !reflect.DeepEqual(stored.RequiredVars, []string{"GIT_SHA", "PR"}) {
			t.Fatalf("requiredVars = %v", stored.RequiredVars)
		}
		list, _ := store.List()
		if len(list) != 1 || list[0].Name != "web" || list[0].Description != "the web app" {
			t.Fatalf("list = %+v", list)
		}
		src, err := store.Source("web")
		if err != nil || src != fixture(t) {
			t.Fatalf("source = %q, %v", src, err)
		}
		// The file: literal order, two-space indent, no trailing newline.
		b, _ := os.ReadFile(filepath.Join(store.Root, "web", "meta.json"))
		if !strings.HasPrefix(string(b), "{\n  \"name\": \"web\",\n  \"kind\": \"isolated\",\n  \"description\": \"the web app\",\n  \"createdAt\": ") ||
			!strings.HasSuffix(string(b), "\"requiredVars\": [\n    \"GIT_SHA\",\n    \"PR\"\n  ]\n}") {
			t.Fatalf("meta.json:\n%s", b)
		}
	})

	t.Run("a malformed spec is rejected and nothing is written", func(t *testing.T) {
		// negative control: move the MkdirAll above the parse in Put — the directory-absent check fails.
		store := New(t.TempDir())
		_, err := store.Put("bad", "axes: [oops", PutOptions{})
		if err == nil || !strings.Contains(err.Error(), "spec rejected") {
			t.Fatalf("err = %v", err)
		}
		if got, _ := store.Get("bad"); got != nil {
			t.Fatal("written")
		}
		if _, err := os.Stat(filepath.Join(store.Root, "bad")); err == nil {
			t.Fatal("directory created")
		}
	})

	t.Run("rejects a name that could escape the store directory", func(t *testing.T) {
		// negative control: drop AssertValidSpecName from dir() — the Put succeeds outside Root.
		store := New(t.TempDir())
		_, err := store.Put("../etc", fixture(t), PutOptions{})
		if err == nil || !strings.Contains(err.Error(), "invalid spec name") {
			t.Fatalf("err = %v", err)
		}
		if err.Error() != `invalid spec name "../etc" — must match /^[a-z0-9][a-z0-9._-]{0,63}$/ (lowercase, no traversal, no spaces)` {
			t.Fatalf("text: %s", err)
		}
	})

	t.Run("a record written before requiredVars existed reads as an empty list", func(t *testing.T) {
		// negative control: drop the `requiredVars ?? []` repair in readMeta — RequiredVars is still
		// [] from fromDoc, but Doc lacks the key; assert on Doc.
		store := New(t.TempDir())
		dir := filepath.Join(store.Root, "old")
		os.MkdirAll(dir, 0o777)
		os.WriteFile(filepath.Join(dir, "spec.yml"), []byte("version: 1\nstack: s\naxes: []\n"), 0o666)
		os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"name":"old","kind":"isolated","createdAt":1,"updatedAt":2,"x":true}`), 0o666)
		got, err := store.Get("old")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Doc.Keys(), []string{"name", "kind", "createdAt", "updatedAt", "x", "requiredVars"}) || len(got.RequiredVars) != 0 {
			t.Fatalf("doc keys = %v", got.Doc.Keys())
		}
	})
}
