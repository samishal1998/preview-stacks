// Package testfacts locates packages/conformance/golden/facts — the measured Bun semantics the
// foundation packages are tested against. Test-only; it walks up from the package directory to the
// repo root rather than embedding, so regenerating the facts never needs a rebuild.
package testfacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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

// GoDivergent names the golden lines that differ BY DESIGN between the TypeScript reference and
// this build: the Bun install block and the npm-based install step on the reference side, the
// installer-based lines on this side. The conformance suite applies the same expression
// (gen/goldens.table.ts BUN_LINES) to both sides in go mode; keep the two in step.
var GoDivergent = regexp.MustCompile(`bun|BUN_|@samyx/preview-stacks|unzip|install pstack |releases/download|releases/latest|debian:bookworm|useradd|apt-get|COPY pstack|/usr/local/bin/pstack --version|"pstack", "`)

// WithoutDivergent drops the GoDivergent lines from text.
func WithoutDivergent(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !GoDivergent.MatchString(l) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}
