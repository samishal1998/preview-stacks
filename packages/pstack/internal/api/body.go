package api

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// maxBody bounds a JSON body: a spec plus a compose file is kilobytes, never megabytes.
const maxBody = 8 << 20

// readBody is `await req.json().catch(() => null)`: the decoded value, or (nil, false) when the
// body is not JSON (trailing data included, like req.json()). A JSON `null` is (nil, true).
func readBody(r *http.Request) (any, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, false
	}
	v, err := omap.Parse(b)
	if err != nil {
		return nil, false
	}
	return v, true
}

// bodyObject is the body as an object; nil for anything else (`!body`, or `body.x` on a non-object).
func bodyObject(r *http.Request) *omap.Map {
	v, ok := readBody(r)
	if !ok {
		return nil
	}
	m, _ := v.(*omap.Map)
	return m
}

// bodyOrEmpty is `await req.json().catch(() => ({}))` for the routes whose body is optional.
func bodyOrEmpty(r *http.Request) *omap.Map {
	if m := bodyObject(r); m != nil {
		return m
	}
	return omap.New()
}

func getStr(m *omap.Map, k string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m.Get(k)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getBool(m *omap.Map, k string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m.Get(k)
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// truthy is JS truthiness over a document value.
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
		return x != 0 && !math.IsNaN(x)
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	}
	return true
}

// jsonUnmarshal is plain decoding for docker's own JSON (input, never output).
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// spread turns a struct into the ordered pairs `{...value}` would contribute, so a route can append
// its own fields after them.
func spread(v any) jsonx.Object {
	b, err := jsonx.Marshal(v)
	if err != nil {
		return jsonx.Object{}
	}
	parsed, err := omap.Parse(b)
	m, ok := parsed.(*omap.Map)
	if err != nil || !ok {
		return jsonx.Object{}
	}
	out := jsonx.Object{}
	m.Each(func(k string, v any) { out = append(out, jsonx.KV{K: k, V: v}) })
	return out
}

// numParam is `Number(url.searchParams.get(key) ?? fallback)`.
func numParam(rawQuery, key string, fallback float64) float64 {
	v, ok := query(rawQuery, key)
	if !ok {
		return fallback
	}
	return js.ParseNumber(v)
}

// boundedSeconds is a `?key=<seconds>` parameter: a positive value capped at max, anything else the
// fallback. ONE definition because `up?timeout=`, `readiness?timeout=` and `readiness?wait=` must
// agree on the ceiling — a watch deadline the deploy route would accept and the read route would
// clamp is a watch nobody can poll to completion.
func boundedSeconds(rawQuery, key string, fallback, max float64) float64 {
	raw := numParam(rawQuery, key, 0)
	if js.IsFinite(raw) && raw > 0 {
		if raw < max {
			return raw
		}
		return max
	}
	return fallback
}

// clamp is Math.min(Math.max(n, lo), hi) with NaN falling back.
func clamp(n, lo, hi, fallback float64) float64 {
	if !js.IsFinite(n) {
		return fallback
	}
	return math.Min(math.Max(n, lo), hi)
}

// intID is `Number(m[1])` for a `\d+` capture.
func intID(s string) int64 {
	n := js.ParseNumber(s)
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return first
}
