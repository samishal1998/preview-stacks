package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// quiet is a registry with an empty ambient environment, so nothing exported in the test shell
// can satisfy a variable by accident.
func quiet(t *testing.T) *Registry {
	t.Helper()
	r := New(t.TempDir())
	r.Env = map[string]string{}
	return r
}

func TestDeploymentVariablesAreStoredNotRePassed(t *testing.T) {
	// negative control: each subtest names its own.
	t.Run("resolve uses stored vars, and request vars still win", func(t *testing.T) {
		// negative control: drop dep.Vars from the Merge in Resolve — the first resolve fails on ${PR}.
		// The footgun this closes: variables used to travel as query params, so `up` with PR=7 and a
		// later `down` with PR=8 tore down a DIFFERENT stack and orphaned the first.
		reg := quiet(t)
		if _, err := reg.Put("pr-7", "version: 1\nstack: pr-${PR}\naxes: []", PutOptions{Vars: omap.From("PR", "7")}); err != nil {
			t.Fatal(err)
		}
		stored, err := reg.Resolve("pr-7", nil, nil) // no variables supplied at all
		if err != nil {
			t.Fatal(err)
		}
		if stored.Stack != "pr-7" {
			t.Fatalf("stack = %q", stored.Stack)
		}
		overridden, err := reg.Resolve("pr-7", map[string]string{"PR": "9"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if overridden.Stack != "pr-9" {
			t.Fatalf("stack = %q", overridden.Stack)
		}
	})

	t.Run("a redeploy supplying one variable does not drop the others", func(t *testing.T) {
		// negative control: start mergedVars from omap.New() instead of prev's vars in Put — the
		// second Put is rejected on ${REGION}.
		reg := quiet(t)
		spec := "version: 1\nstack: pr-${PR}\nenv:\n  R: ${REGION}\naxes: []"
		if _, err := reg.Put("d", spec, PutOptions{Vars: omap.From("PR", "7", "REGION", "eu")}); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Put("d", spec, PutOptions{Vars: omap.From("PR", "8")}); err != nil { // only PR this time
			t.Fatal(err)
		}
		s, err := reg.Resolve("d", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.Stack != "pr-8" {
			t.Fatalf("stack = %q", s.Stack)
		}
		if s.Env["R"] != "eu" { // REGION survived
			t.Fatalf("R = %q", s.Env["R"])
		}
	})
}

func TestRegistryTheSleepRecord(t *testing.T) {
	// negative control: the subtest names its own.
	t.Run("setSleep writes and clears without touching updatedAt", func(t *testing.T) {
		// negative control: make SetSleep also Set("updatedAt", now) — the updatedAt assertion fails.
		reg := quiet(t)
		dep, err := reg.Put("d", "version: 1\nstack: d\naxes: []\n", PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // so a bumped updatedAt could not hide in the same millisecond
		if err := reg.SetSleep("d", &SleepRecord{Since: 1, Reason: "x", Hosts: []string{"h"}, Rules: []string{}}); err != nil {
			t.Fatal(err)
		}
		after, err := reg.Get("d")
		if err != nil {
			t.Fatal(err)
		}
		want := &SleepRecord{Since: 1, Reason: "x", Hosts: []string{"h"}, Rules: []string{}}
		if !reflect.DeepEqual(after.Sleep, want) {
			t.Fatalf("sleep = %+v", after.Sleep)
		}
		if after.UpdatedAt != dep.UpdatedAt {
			t.Fatalf("updatedAt moved: %d → %d", dep.UpdatedAt, after.UpdatedAt)
		}
		if err := reg.SetSleep("d", nil); err != nil {
			t.Fatal(err)
		}
		cleared, _ := reg.Get("d")
		if cleared.Sleep != nil || cleared.Doc.Has("sleep") {
			t.Fatalf("sleep not cleared: %s", jsonx.Must(cleared))
		}
	})
}

func TestMetaJSONBytes(t *testing.T) {
	// negative control: each subtest names its own.
	t.Run("two-space indent, no trailing newline, literal key order", func(t *testing.T) {
		// negative control: append "\n" in writeDoc — the exact-bytes comparison fails.
		reg := quiet(t)
		name := "web"
		if _, err := reg.Put("pr-1", "version: 1\nstack: pr-${PR}\naxes: []", PutOptions{Vars: omap.From("PR", "1"), SpecName: &name}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(reg.Root, "pr-1", "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		dep, _ := reg.Get("pr-1")
		want := "{\n  \"id\": \"pr-1\",\n  \"kind\": \"isolated\",\n  \"createdAt\": " + jsonx.Number(float64(dep.CreatedAt)) +
			",\n  \"updatedAt\": " + jsonx.Number(float64(dep.UpdatedAt)) + ",\n  \"specName\": \"web\",\n  \"vars\": {\n    \"PR\": \"1\"\n  }\n}"
		if string(b) != want {
			t.Fatalf("meta.json:\n%s\nwant:\n%s", b, want)
		}
	})

	t.Run("a rejected spec rolls the directory back", func(t *testing.T) {
		// negative control: remove the os.RemoveAll in Put's failure path — Get returns a record.
		reg := quiet(t)
		_, err := reg.Put("bad", "axes: [oops", PutOptions{})
		if err == nil || !strings.HasPrefix(err.Error(), "spec rejected: ") {
			t.Fatalf("err = %v", err)
		}
		if dep, _ := reg.Get("bad"); dep != nil {
			t.Fatal("half-created deployment left behind")
		}
	})

	t.Run("an id that could escape the directory is refused with the JS regex in the message", func(t *testing.T) {
		// negative control: drop the `..` check AND change idText — both halves are asserted.
		err := AssertValidID("../etc")
		if err == nil || err.Error() != `invalid deployment id "../etc" — must match /^[a-z0-9][a-z0-9._-]{0,63}$/ (lowercase, no traversal, no spaces)` {
			t.Fatalf("err = %v", err)
		}
		if AssertValidID("pr-1.2_x") != nil || AssertValidID("Pr") == nil || AssertValidID("") == nil {
			t.Fatal("alphabet")
		}
	})
}

// The golden host: a complete data directory written by the reference, which the port must open
// unchanged. The list order is the one its expected/deployments.json shows, and a sleep on pr-2 must
// hand back the `x-future` key another version wrote, byte-for-byte, where it was.
func TestGoldenHost(t *testing.T) {
	// negative control: each subtest names its own; the CopyFS skip is the only way out.
	const golden = "../../../conformance/golden/host"
	data := t.TempDir()
	if err := os.CopyFS(data, os.DirFS(filepath.Join(golden, "deployments"))); err != nil {
		t.Skipf("golden host not available: %v", err)
	}
	reg := &Registry{Root: data, Env: map[string]string{}}

	t.Run("lists every deployment in the order the reference served", func(t *testing.T) {
		// negative control: flip the sort to ascending — ids come back reversed.
		var expected struct {
			Body struct {
				Deployments []struct {
					ID string `json:"id"`
				} `json:"deployments"`
			} `json:"body"`
		}
		b, err := os.ReadFile(filepath.Join(golden, "expected", "deployments.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &expected); err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for _, d := range expected.Body.Deployments {
			want = append(want, d.ID)
		}
		list, err := reg.List()
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, m := range list {
			got = append(got, m.ID)
		}
		if !reflect.DeepEqual(got, want) || len(got) != 4 {
			t.Fatalf("order = %v, want %v", got, want)
		}
		// The typed view decodes what the reference wrote.
		for _, m := range list {
			if m.ID == "sleepy" && (m.Sleep == nil || m.Sleep.Hosts[0] != "app-sleepy.preview.example.com" || m.Sleep.Since != 1787425041682) {
				t.Fatalf("sleepy: %+v", m.Sleep)
			}
			if m.ID == "pr-2" && (m.SpecName != "web" || m.Vars["PR"] != "2") {
				t.Fatalf("pr-2: %+v", m)
			}
		}
	})

	t.Run("a meta.json field pstack never heard of survives a sleep, in place", func(t *testing.T) {
		// negative control: decode meta.json into a struct of known fields in readMeta — x-future
		// is gone from the rewritten file.
		path := filepath.Join(data, "pr-2", "meta.json")
		before, _ := os.ReadFile(path)
		if err := reg.SetSleep("pr-2", &SleepRecord{Since: 1, Reason: "x", Hosts: []string{"h"}, Rules: []string{}}); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(path)
		// JS: `meta.sleep = sleep` appends a NEW key after everything the file had — so the file is the
		// old one, minus its closing brace, plus the sleep block.
		want := strings.TrimSuffix(string(before), "\n}") + ",\n  \"sleep\": {\n    \"since\": 1,\n    \"reason\": \"x\",\n    \"hosts\": [\n      \"h\"\n    ],\n    \"rules\": []\n  }\n}"
		if string(after) != want {
			t.Fatalf("meta.json after sleep:\n%s\nwant:\n%s", after, want)
		}
		if !strings.Contains(string(after), "\"x-future\": {\n    \"k\": 1\n  }") {
			t.Fatal("x-future lost")
		}
		// And the response shape keeps the file's order: vars, x-future, sleep.
		dep, _ := reg.Get("pr-2")
		keys := dep.Doc.Keys()
		if !reflect.DeepEqual(keys, []string{"id", "kind", "createdAt", "updatedAt", "specName", "vars", "x-future", "sleep"}) {
			t.Fatalf("keys = %v", keys)
		}
		// Clearing it removes exactly that key.
		if err := reg.SetSleep("pr-2", nil); err != nil {
			t.Fatal(err)
		}
		restored, _ := os.ReadFile(path)
		if string(restored) != string(before) {
			t.Fatalf("after clear:\n%s", restored)
		}
	})

	t.Run("a replace keeps the sleep record and drops nothing it was asked for", func(t *testing.T) {
		// negative control: stop copying prev's sleep in Put — sleepy wakes on a replace.
		src, _ := os.ReadFile(filepath.Join(data, "sleepy", "spec.yml"))
		dep, err := reg.Put("sleepy", string(src), PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if dep.Sleep == nil || dep.Sleep.Reason != "operator: root (PSTACK_TOKEN)" {
			t.Fatalf("sleep = %+v", dep.Sleep)
		}
		if dep.CreatedAt != 1787425040647 || dep.UpdatedAt == 1787425040647 {
			t.Fatalf("createdAt %d updatedAt %d", dep.CreatedAt, dep.UpdatedAt)
		}
	})
}
