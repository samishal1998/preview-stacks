package scaler

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
)

func TestParsePresetHetzner(t *testing.T) {
	// negative control: drop "host_id" from the scaler-filled set in ParsePreset → Shared gains host_id and this fails
	p, err := ParsePreset(Builtins()["hetzner"])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.PerWorker, []string{"location", "server_type"}) {
		t.Errorf("PerWorker = %v", p.PerWorker)
	}
	if !slices.Equal(p.Shared, []string{"network_id", "ssh_keys"}) {
		t.Errorf("Shared = %v", p.Shared)
	}
	if p.Distro != "debian" || !p.LockFile {
		t.Errorf("Distro = %q, LockFile = %v", p.Distro, p.LockFile)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.Hash) {
		t.Errorf("Hash = %q", p.Hash)
	}
	again, _ := ParsePreset(Builtins()["hetzner"])
	if again.Hash != p.Hash {
		t.Errorf("hash not stable: %s vs %s", p.Hash, again.Hash)
	}
}

func TestEveryBuiltinEmbedsItsLockFile(t *testing.T) {
	// negative control: `//go:embed presets` without `all:` → the dot-file is not embedded and this fails
	b := Builtins()
	if len(b) == 0 {
		t.Fatal("no built-in presets")
	}
	for name, files := range b {
		lock, ok := files[".terraform.lock.hcl"]
		if !ok {
			t.Errorf("%s: no .terraform.lock.hcl", name)
			continue
		}
		if !strings.Contains(lock, `provider "registry.opentofu.org/`) {
			t.Errorf("%s: lock file has no registry.opentofu.org provider", name)
		}
		if n := strings.Count(lock, `"h1:`); n < 2 {
			t.Errorf("%s: %d h1: hashes, want ≥ 2 (amd64 and arm64)", name, n)
		}
	}
}

const workersTF = "variable \"workers\" {\n  type = map(object({\n    user_data = string\n    size      = string\n  }))\n}\n"

func TestParsePresetRefuses(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		// negative control: drop the workers-variable check → ParsePreset accepts and the case fails
		{"no workers", map[string]string{"main.tf": `variable "size" {}`}, `no variable "workers"`},
		// negative control: drop the user_data check → ParsePreset accepts and the case fails
		{"no user_data", map[string]string{"main.tf": "variable \"workers\" {\n  type = map(object({\n    size = string\n  }))\n}\n"}, "user_data"},
		// negative control: anchor the backend regex at line start (^\s*) → the one-line block slips through and the case fails
		{"backend one line", map[string]string{"main.tf": workersTF + `terraform { backend "s3" {} }`}, "backend"},
		// negative control: drop `backend\s+"` from the regex → the case fails
		{"backend multi-line", map[string]string{"main.tf": workersTF + "terraform {\n  backend \"s3\" {\n  }\n}\n"}, "backend"},
		// negative control: drop `cloud\s*\{` from the regex → the case fails
		{"cloud", map[string]string{"main.tf": workersTF + "terraform {\n  cloud {\n    organization = \"x\"\n  }\n}\n"}, "cloud"},
		// negative control: allow `\.tf(\.json)?$` in the file-name check → the case fails
		{"tf.json", map[string]string{"main.tf": workersTF, "x.tf.json": "{}"}, `"x.tf.json"`},
		// negative control: match file names unanchored → "../a.tf" passes and the case fails
		{"traversal", map[string]string{"main.tf": workersTF, "../a.tf": ""}, `"../a.tf"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePreset(c.files)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to name %q", err, c.want)
			}
		})
	}
}

func TestParsePresetOptionalAttrs(t *testing.T) {
	t.Run("optional", func(t *testing.T) {
		// negative control: read only bare-type attributes (attrRe `=\s*[a-z]+\s*$`) → `optional(…)` is skipped and size is lost
		files := map[string]string{"main.tf": "variable \"workers\" {\n  type = map(object({\n    user_data = string\n    size      = optional(string, \"cx22\")\n  }))\n}\n"}
		p, err := ParsePreset(files)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.PerWorker, []string{"size"}) {
			t.Errorf("PerWorker = %v", p.PerWorker)
		}
		// Go rule 3: an empty list is [], never null.
		if p.Shared == nil || len(p.Shared) != 0 {
			t.Errorf("Shared = %#v, want []string{}", p.Shared)
		}
	})
	t.Run("inline", func(t *testing.T) {
		// negative control: split the object body only on newlines → the inline body is one piece and size is lost
		files := map[string]string{"main.tf": "variable \"workers\" {\n  type = map(object({ user_data = string, size = string }))\n}\nvariable \"region\" {}\n"}
		p, err := ParsePreset(files)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.PerWorker, []string{"size"}) || !slices.Equal(p.Shared, []string{"region"}) {
			t.Errorf("PerWorker = %v, Shared = %v", p.PerWorker, p.Shared)
		}
	})
	t.Run("nested", func(t *testing.T) {
		// negative control: split the object body on every comma (ignore depth) → the nested `ssd` reads as a per-worker variable
		files := map[string]string{"main.tf": "variable \"workers\" {\n  type = map(object({ user_data = string, disk = optional(object({ gb = number, ssd = bool })) }))\n}\n"}
		p, err := ParsePreset(files)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.PerWorker, []string{"disk"}) {
			t.Errorf("PerWorker = %v", p.PerWorker)
		}
	})
}

func TestPresetHashIgnoresMapOrder(t *testing.T) {
	// negative control: hash in map iteration order instead of sorted names → the two hashes differ (most runs) and this fails
	a := map[string]string{}
	a["main.tf"] = workersTF
	a["b.tf"] = `variable "region" {}`
	a["c.tf"] = `variable "zone" {}`
	b := map[string]string{}
	b["c.tf"] = `variable "zone" {}`
	b["b.tf"] = `variable "region" {}`
	b["main.tf"] = workersTF
	pa, err := ParsePreset(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := ParsePreset(b)
	if pa.Hash != pb.Hash {
		t.Errorf("hash depends on map order: %s vs %s", pa.Hash, pb.Hash)
	}
	b["c.tf"] = `variable "zonf" {}`
	pc, _ := ParsePreset(b)
	if pc.Hash == pa.Hash {
		t.Error("one byte changed, same hash")
	}
}

func TestValidPresetName(t *testing.T) {
	for _, c := range []struct {
		name, want string
	}{
		// negative control: drop the built-in check → hetzner is accepted and the case fails
		{"hetzner", "built in"},
		// negative control: skip specs.AssertValidSpecName → the space is accepted and the case fails
		{"My Preset", "invalid preset name"},
		// negative control: skip specs.AssertValidSpecName → the traversal is accepted and the case fails
		{"../x", "invalid preset name"},
		// negative control: refuse every name → the valid case fails
		{"team-a", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidPresetName(c.name)
			if c.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "spec") {
				t.Fatalf("err = %v, want %q (and no \"spec\")", err, c.want)
			}
		})
	}
}

// The API maps only package error types to 4xx (api/server.go), so these must be *specs.Error.
// negative control: return errors.New from any of these paths → that case fails
func TestPresetErrorsAreSpecsErrors(t *testing.T) {
	for name, err := range map[string]error{
		"bad name": ValidPresetName("My X"),
		"built in": ValidPresetName("hetzner"),
		"bad file": func() error { _, err := ParsePreset(map[string]string{"x.tf.json": "{}"}); return err }(),
		"no workers": func() error {
			_, err := ParsePreset(map[string]string{"main.tf": `variable "token" {}`})
			return err
		}(),
		"backend": func() error {
			_, err := ParsePreset(map[string]string{"main.tf": `terraform { backend "s3" {} }`})
			return err
		}(),
		"no user_data": func() error {
			_, err := ParsePreset(map[string]string{"main.tf": `variable "workers" { type = map(object({ size = string })) }`})
			return err
		}(),
	} {
		if !specs.IsError(err) {
			t.Errorf("%s: err = %#v, want *specs.Error", name, err)
		}
	}
}
