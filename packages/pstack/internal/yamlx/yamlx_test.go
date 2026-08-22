package yamlx

import (
	"math"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

// The dialect is what Bun measured, not what a spec says. golden/facts/yaml.json records, for every
// scalar and document, the JSON Bun produced; the parser must reproduce those bytes through jsonx.

type yamlFacts struct {
	Scalars []struct {
		Input string  `json:"input"`
		Type  string  `json:"type"`
		Repr  *string `json:"repr"`
		JSON  *string `json:"json"`
	} `json:"scalars"`
	Documents []struct {
		Name  string  `json:"name"`
		YAML  string  `json:"yaml"`
		JSON  *string `json:"json"`
		Error *string `json:"error"`
	} `json:"documents"`
	Invalid []struct {
		YAML  string `json:"yaml"`
		Threw bool   `json:"threw"`
	} `json:"invalid"`
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScalarsResolveLikeBun(t *testing.T) {
	// negative control: let resolvePlain return strconv.ParseInt's octal reading for "0755" — 493 ≠ 755.
	var f yamlFacts
	testfacts.Load(t, "yaml.json", &f)
	for _, row := range f.Scalars {
		v, err := ParseString("k: " + row.Input)
		if row.Type == "error" {
			if err == nil {
				t.Errorf("%q: bun rejects it, we parsed %v", row.Input, v)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", row.Input, err)
			continue
		}
		m, _ := v.(interface{ Get(string) (any, bool) })
		got, _ := m.Get("k")
		// Infinity/NaN: JSON null on both sides; check the number itself through repr.
		if fl, ok := got.(float64); ok && (math.IsInf(fl, 0) || math.IsNaN(fl)) {
			if row.Repr == nil || jsonx.NumberString(fl) != *row.Repr {
				t.Errorf("%q: got %v, bun repr %v", row.Input, fl, row.Repr)
			}
			continue
		}
		if row.JSON != nil && jsonOf(t, got) != *row.JSON {
			t.Errorf("%q: got %s (%T), bun says %s", row.Input, jsonOf(t, got), got, *row.JSON)
		}
	}
}

func TestDocumentsSerialiseLikeBun(t *testing.T) {
	// negative control: build mappings into map[string]any — key order and the "1"/"0" hoist are lost.
	var f yamlFacts
	testfacts.Load(t, "yaml.json", &f)
	for _, d := range f.Documents {
		v, err := ParseString(d.YAML)
		if d.Error != nil {
			if err == nil {
				t.Errorf("%s: bun errors, we parsed", d.Name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", d.Name, err)
			continue
		}
		if got := jsonOf(t, v); got != *d.JSON {
			t.Errorf("%s:\n got  %s\n bun  %s", d.Name, got, *d.JSON)
		}
	}
}

func TestInvalidDocuments(t *testing.T) {
	// negative control: return (nil, nil) from Parse on a parser error — the ErrNotYAML assertion fails.
	var f yamlFacts
	testfacts.Load(t, "yaml.json", &f)
	for _, row := range f.Invalid {
		_, err := ParseString(row.YAML)
		if (err != nil) != row.Threw {
			t.Errorf("%q: threw=%v, bun says %v (%v)", row.YAML, err != nil, row.Threw, err)
		}
	}
}

func TestCorpus(t *testing.T) {
	// negative control: any resolver change shows up as a byte diff on the real example files.
	var f struct {
		Files []struct {
			Path  string  `json:"path"`
			JSON  *string `json:"json"`
			Error *string `json:"error"`
		} `json:"files"`
	}
	testfacts.Load(t, "yaml-corpus.json", &f)
	root := testfacts.Golden(t) + "/../../.."
	for _, file := range f.Files {
		src, err := readFile(root + "/" + file.Path)
		if err != nil {
			t.Fatalf("%s: %v", file.Path, err)
		}
		v, err := Parse(src)
		if file.Error != nil {
			if err == nil {
				t.Errorf("%s: bun errors, we parsed", file.Path)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", file.Path, err)
			continue
		}
		if got := jsonOf(t, v); got != *file.JSON {
			t.Errorf("%s: differs from bun\n got %.300s…\n bun %.300s…", file.Path, got, *file.JSON)
		}
	}
}
