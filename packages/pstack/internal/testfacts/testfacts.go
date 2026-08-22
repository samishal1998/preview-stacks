// Package testfacts locates packages/conformance/golden/facts — the measured Bun semantics the
// foundation packages are tested against. Test-only; it walks up from the package directory to the
// repo root rather than embedding, so regenerating the facts never needs a rebuild.
package testfacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Dir is the absolute path of golden/facts.
func Dir(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "turbo.json")); err == nil {
			return filepath.Join(dir, "packages", "conformance", "golden", "facts")
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the repo root from internal/testfacts")
	return ""
}

// Load decodes golden/facts/<name>.json into v.
func Load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Dir(t), name))
	if err != nil {
		t.Fatalf("read facts %s: %v (run `bun run gen` in packages/conformance)", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode facts %s: %v", name, err)
	}
}

// Golden is the absolute path of packages/conformance/golden.
func Golden(t *testing.T) string { return filepath.Dir(Dir(t)) }
