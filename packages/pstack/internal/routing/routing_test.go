package routing

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func valid(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func store(t *testing.T) *RoutingStore {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".keep"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	return New(dir)
}

func names(files []RoutingFile) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %v, want /%s/", err, substr)
	}
	if !IsError(err) {
		t.Fatalf("not a routing.Error: %T", err)
	}
}

// Traefik's documented behaviour: an unparseable file in the watched directory is a parse error for
// the DIRECTORY, and the rest of it can be discarded with it. So the failure mode is not "my new
// middleware is broken", it is routes elsewhere vanishing. Everything below defends that.
func TestTraefikDynamicConfig(t *testing.T) {
	// negative control: each leaf subtest names its own.
	t.Run("validation happens before anything touches disk", func(t *testing.T) {
		// negative control: see the leaves.
		t.Run("unparseable YAML is refused", func(t *testing.T) {
			// negative control: return the parse error's text without the "not valid YAML: " prefix.
			_, err := ValidateRoutingContent("http:\n  - [unclosed")
			wantErr(t, err, "not valid YAML")
		})

		t.Run("a typo in a top-level section is refused, not silently ignored", func(t *testing.T) {
			// negative control: add "htttp" to sections — the first assertion passes content it should not.
			// The insidious case: `htttp:` is perfectly good YAML, so Traefik loads the file and applies
			// nothing. Without this check the symptom is "I added the middleware and nothing happened".
			_, err := ValidateRoutingContent("htttp:\n  middlewares: {}")
			wantErr(t, err, "unknown top-level")
			if err.Error() != "unknown top-level section(s): htttp. Traefik reads only http, tcp, udp, tls — it would load this file and silently apply nothing. Middlewares and routers go *under* `http:`." {
				t.Fatalf("text: %s", err)
			}
			// Same shape: middlewares belong UNDER http, and at the top level they do nothing at all.
			_, err = ValidateRoutingContent("middlewares:\n  a:\n    basicAuth: {}")
			wantErr(t, err, "unknown top-level")
		})

		t.Run("empty, non-mapping and section-less content are all refused", func(t *testing.T) {
			// negative control: drop the len(keys) == 0 check — `{}` is accepted.
			_, err := ValidateRoutingContent("   ")
			wantErr(t, err, "empty")
			_, err = ValidateRoutingContent("- a\n- b")
			wantErr(t, err, "must be a mapping")
			_, err = ValidateRoutingContent("{}")
			wantErr(t, err, "no sections")
		})

		t.Run("every real Traefik section is accepted", func(t *testing.T) {
			// negative control: remove "udp" from sections — udp is refused.
			for _, s := range []string{"http", "tcp", "udp", "tls"} {
				if _, err := ValidateRoutingContent(s + ":\n  routers: {}"); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
		})
	})

	t.Run("filenames", func(t *testing.T) {
		// negative control: see the leaves.
		t.Run("traversal and separators are refused", func(t *testing.T) {
			// negative control: loosen nameRe to `^.*\.ya?ml$` — `a/b.yml` is written.
			s := store(t)
			for _, bad := range []string{"../evil.yml", "a/b.yml", "..%2Fx.yml", ".hidden.yml"} {
				_, err := s.Write(bad, valid(t))
				wantErr(t, err, "invalid filename")
			}
		})

		t.Run("a non-YAML extension is refused — Traefik would not read it anyway", func(t *testing.T) {
			// negative control: make the extension optional in nameRe — `middleware` is written.
			s := store(t)
			_, err := s.Write("middleware.txt", valid(t))
			wantErr(t, err, "invalid filename")
			_, err = s.Write("middleware", valid(t))
			wantErr(t, err, "invalid filename")
			if err.Error() != `invalid filename "middleware" — must be lowercase, end in .yml or .yaml, and contain no path separators (it becomes a file in Traefik's watched directory)` {
				t.Fatalf("text: %s", err)
			}
		})

		t.Run(".yml and .yaml both work", func(t *testing.T) {
			// negative control: nameRe `\.yml$` — b.yaml is refused.
			s := store(t)
			if _, err := s.Write("a.yml", valid(t)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Write("b.yaml", valid(t)); err != nil {
				t.Fatal(err)
			}
			if got := names(s.List()); !reflect.DeepEqual(got, []string{"a.yml", "b.yaml"}) {
				t.Fatalf("list = %v", got)
			}
		})
	})

	t.Run("writing", func(t *testing.T) {
		// negative control: see the leaves.
		t.Run("a rejected file is never created", func(t *testing.T) {
			// negative control: validate AFTER the rename in Write — bad.yml appears in the list.
			s := store(t)
			_, err := s.Write("bad.yml", "htttp: {}")
			wantErr(t, err, "unknown top-level")
			if got := s.List(); len(got) != 0 {
				t.Fatalf("list = %v", got)
			}
		})

		t.Run("write returns the previous content, so a caller can offer an undo", func(t *testing.T) {
			// negative control: read `previous` after the rename — the second Write returns `next`.
			// There is deliberately NO on-disk history: the obvious place to keep it is the one
			// directory that must contain nothing but dynamic config.
			s := store(t)
			prev, err := s.Write("m.yml", valid(t))
			if err != nil || prev != nil { // new
				t.Fatalf("prev = %v, err = %v", prev, err)
			}
			next := "http:\n  routers: {}\n"
			prev, err = s.Write("m.yml", next)
			if err != nil || prev == nil || *prev != valid(t) { // replaced
				t.Fatalf("prev = %v, err = %v", prev, err)
			}
			if got, _ := s.Read("m.yml"); got != next {
				t.Fatalf("read = %q", got)
			}
		})

		t.Run("it leaves no temp file behind — anything in that directory gets parsed", func(t *testing.T) {
			// negative control: write to the target directly and skip the rename, leaving tmp — the filter finds it.
			s := store(t)
			if _, err := s.Write("m.yml", valid(t)); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(s.Dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), "pstack-tmp") {
					t.Fatalf("left behind: %s", e.Name())
				}
			}
		})

		t.Run("list only reports files Traefik would actually read", func(t *testing.T) {
			// negative control: drop the nameRe filter in List — notes.txt and .hidden.yml appear.
			s := store(t)
			if _, err := s.Write("good.yml", valid(t)); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(s.Dir, "notes.txt"), []byte("ignore me"), 0o666)
			os.WriteFile(filepath.Join(s.Dir, ".hidden.yml"), []byte(valid(t)), 0o666)
			got := s.List()
			if !reflect.DeepEqual(names(got), []string{"good.yml"}) {
				t.Fatalf("list = %v", names(got))
			}
			if got[0].Size != int64(len(valid(t))) || got[0].UpdatedAt < 1_600_000_000_000 {
				t.Fatalf("entry = %+v", got[0])
			}
		})

		t.Run("a missing directory reads as not-writable rather than throwing", func(t *testing.T) {
			// negative control: return true from Writable when Stat fails — Write's error changes.
			// The pre-0.4.0 control stack does not mount this into the API at all, and the answer has
			// to be "re-run pstack init", not an ENOENT on every request.
			s := New(filepath.Join(t.TempDir(), "definitely-absent"))
			if s.Writable() {
				t.Fatal("writable")
			}
			if got := s.List(); len(got) != 0 {
				t.Fatalf("list = %v", got)
			}
			_, err := s.Write("m.yml", valid(t))
			wantErr(t, err, "re-run `pstack init`")
		})
	})

	t.Run("delete returns what it removed", func(t *testing.T) {
		// negative control: return "" from Remove — the content assertion fails.
		s := store(t)
		if _, err := s.Write("m.yml", valid(t)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Remove("m.yml")
		if err != nil || got != valid(t) {
			t.Fatalf("removed = %q, %v", got, err)
		}
		if l := s.List(); len(l) != 0 {
			t.Fatalf("list = %v", l)
		}
		_, err = s.Remove("m.yml")
		wantErr(t, err, "no such routing file")
	})
}
