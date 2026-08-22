// Package jsonx is JSON.stringify.
//
// Every byte this program emits as JSON goes through here, because three of those byte sequences
// are signed or stored and replayed (the share JWT, the webhook envelope, a delivery's payload),
// every API response is two-space pretty-printed, and a Go encoder left to its defaults does three
// things JavaScript does not: sorts map keys, HTML-escapes `<>&`, and appends a newline. So:
// structs (field order) or *omap.Map (insertion order), never map[string]any; SetEscapeHTML(false);
// the trailing newline trimmed. Numbers are emitted the way JSON.stringify emits them — an integer
// is an integer, a float is the shortest round-trippable form — which is what encoding/json does
// for int64/float64 when the value has the right static type. Keep it typed.
package jsonx

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// Marshal is JSON.stringify(v).
func Marshal(v any) ([]byte, error) { return encode(v, "") }

// MarshalIndent is JSON.stringify(v, null, 2).
func MarshalIndent(v any) ([]byte, error) { return encode(v, "  ") }

// Must is Marshal for values that cannot fail (structs of primitives). Panics otherwise.
func Must(v any) []byte {
	b, err := Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func encode(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	// encoding/json escapes U+2028/U+2029 unconditionally (a JS-embedding precaution); JSON.stringify
	// has emitted them raw since ES2019. The escapes only ever occur inside strings, so a literal
	// substitution is exact.
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	return out, nil
}

// Number renders a float64 the way JavaScript's Number.prototype.toString does (which is also how
// JSON.stringify renders it): integers without a fraction, otherwise the shortest representation
// that round-trips, with exponent notation outside [1e-7, 1e21). NaN and ±Infinity are "null" in
// JSON and "NaN"/"Infinity" in String() — callers that need the latter use NumberString.
func Number(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	return NumberString(f)
}

// NumberString is String(number) in JavaScript.
func NumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0" // JS prints -0 as "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		// ES exponent form: "1e+21", "1e-7" — Go's 'e' format gives "1e+21" and "1e-07"; fix the exponent padding.
		s := strconv.FormatFloat(f, 'e', -1, 64)
		mant, exp, _ := bytes.Cut([]byte(s), []byte("e"))
		sign := exp[0]
		digits := bytes.TrimLeft(exp[1:], "0")
		if len(digits) == 0 {
			digits = []byte("0")
		}
		return string(mant) + "e" + string(sign) + string(digits)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Object is an ordered list of key/value pairs that marshals in order — for ad-hoc response
// bodies built at a route, where a struct would be ceremony. Prefer structs for anything reused.
type Object []KV

// KV is one pair of an Object.
type KV struct {
	K string
	V any
}

// MarshalJSON emits the pairs in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := Marshal(kv.K)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		v, err := Marshal(kv.V)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Bool, Str and Int are the pointer helpers for the tri-state and nullable-not-absent fields:
// `*bool` without omitempty marshals `null` when nil and `false` when false — the distinction
// invariant 11 exists to keep.
func Bool(b bool) *bool { return &b }

// Str returns a pointer to s.
func Str(s string) *string { return &s }

// Int returns a pointer to i.
func Int(i int64) *int64 { return &i }

// O builds an Object from alternating keys and values: O("a", 1, "b", "x"). The shape every
// event payload takes — ordered like the TS object literal it replaces, without a struct per event.
func O(pairs ...any) Object {
	out := make(Object, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, KV{K: pairs[i].(string), V: pairs[i+1]})
	}
	return out
}
