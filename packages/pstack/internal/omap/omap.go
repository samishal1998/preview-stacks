// Package omap is the ordered map the whole compose pipeline runs on.
//
// JavaScript objects remember insertion order and Bun.YAML.parse hands back exactly that, so every
// label list, every swarmify note and every key in compose.generated.yml comes out in the order the
// author wrote it. A Go map does not, and ranging one into output is the single most common way a
// port becomes byte-different. So a parsed document is nil | bool | int64 | float64 | string |
// []any | *Map, and *Map is this.
//
// One JS quirk is reproduced on purpose: integer-like keys ("0", "1", "42") are hoisted ahead of
// every other key, in ascending numeric order — that is what JSON.stringify does to an object, and
// the facts fixture pins it ("key order is insertion order"). A document with such keys is rare in a
// compose file (a `ports` map, a numbered service) and exactly the case a byte-diff would catch.
package omap

import (
	"sort"
	"strconv"
)

// Map is an insertion-ordered string-keyed map.
type Map struct {
	keys []string
	m    map[string]any
}

// New returns an empty Map.
func New() *Map { return &Map{m: map[string]any{}} }

// From builds a Map from key/value pairs in the order given.
func From(pairs ...any) *Map {
	m := New()
	for i := 0; i+1 < len(pairs); i += 2 {
		m.Set(pairs[i].(string), pairs[i+1])
	}
	return m
}

// Get returns the value and whether the key exists.
func (m *Map) Get(k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.m[k]
	return v, ok
}

// Has reports whether the key exists.
func (m *Map) Has(k string) bool {
	_, ok := m.Get(k)
	return ok
}

// Set stores v under k. An existing key keeps its position (JS semantics); a new one is appended.
func (m *Map) Set(k string, v any) {
	if _, ok := m.m[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.m[k] = v
}

// Delete removes k if present.
func (m *Map) Delete(k string) {
	if _, ok := m.m[k]; !ok {
		return
	}
	delete(m.m, k)
	for i, kk := range m.keys {
		if kk == k {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// Len is the number of keys.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Keys returns the keys in JS property order: integer-like keys first, ascending; then the rest in
// insertion order. A copy — callers may mutate the map while ranging it.
func (m *Map) Keys() []string {
	if m == nil {
		return nil
	}
	var ints, rest []string
	for _, k := range m.keys {
		if isArrayIndex(k) {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(ints, func(i, j int) bool {
		a, _ := strconv.ParseUint(ints[i], 10, 64)
		b, _ := strconv.ParseUint(ints[j], 10, 64)
		return a < b
	})
	out := make([]string, 0, len(m.keys))
	out = append(out, ints...)
	out = append(out, rest...)
	return out
}

// isArrayIndex is the ECMAScript "array index" test: a canonical decimal string < 2^32-1.
func isArrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for _, c := range k {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 4294967295
}

// Clone deep-copies the map: nested *Map and []any values are copied, scalars shared.
func (m *Map) Clone() *Map {
	if m == nil {
		return nil
	}
	out := New()
	for _, k := range m.keys {
		out.Set(k, CloneValue(m.m[k]))
	}
	return out
}

// CloneValue deep-copies a document value.
func CloneValue(v any) any {
	switch x := v.(type) {
	case *Map:
		return x.Clone()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = CloneValue(e)
		}
		return out
	default:
		return v
	}
}

// GetMap returns the nested *Map at k, or nil when absent or not a map.
func (m *Map) GetMap(k string) *Map {
	v, _ := m.Get(k)
	mm, _ := v.(*Map)
	return mm
}

// GetString returns the string at k, or "" when absent or not a string.
func (m *Map) GetString(k string) string {
	v, _ := m.Get(k)
	s, _ := v.(string)
	return s
}

// GetSlice returns the []any at k, or nil.
func (m *Map) GetSlice(k string) []any {
	v, _ := m.Get(k)
	s, _ := v.([]any)
	return s
}

// Ensure returns the nested *Map at k, creating an empty one (appended) when absent.
func (m *Map) Ensure(k string) *Map {
	if mm := m.GetMap(k); mm != nil {
		return mm
	}
	mm := New()
	m.Set(k, mm)
	return mm
}

// Each calls fn for every key in JS order. Safe against mutation of the map during iteration.
func (m *Map) Each(fn func(k string, v any)) {
	for _, k := range m.Keys() {
		if v, ok := m.m[k]; ok {
			fn(k, v)
		}
	}
}
