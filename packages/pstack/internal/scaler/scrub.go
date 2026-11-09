package scaler

import (
	"encoding/json"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
)

// Scrub removes every credential value, every 8+ character string leaf of a JSON credential
// (decoded and JSON-escaped, e.g. GOOGLE_CREDENTIALS's private_key) and swarm join tokens.
// Scrub the whole text before Tail, so a secret across the cut is still caught.
func Scrub(text string, creds map[string]string) string {
	var extras []string
	for _, v := range creds {
		extras = append(extras, v)
		var obj map[string]any
		if json.Unmarshal([]byte(v), &obj) == nil {
			extras = jsonLeaves(obj, extras)
		}
	}
	return redact.RedactText(text, extras...) // drops extras under 8 chars
}

func jsonLeaves(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		if len(x) >= 8 {
			b, _ := json.Marshal(x)
			out = append(out, x, string(b[1:len(b)-1]))
		}
	case map[string]any:
		for _, e := range x {
			out = jsonLeaves(e, out)
		}
	case []any:
		for _, e := range x {
			out = jsonLeaves(e, out)
		}
	}
	return out
}

// Tail keeps the last n lines of text.
func Tail(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
