// Package js reproduces the JavaScript primitives the contract leans on.
//
// Every function here exists because a Go idiom would produce different bytes: `len()` counts
// bytes where `.length` counts UTF-16 units; `strconv.Atoi` rejects what `Number()` accepts;
// `url.QueryEscape` and `encodeURIComponent` disagree on `!'()*~` and the space; Go's base64url
// decoder is strict where Buffer's is lenient; `html.EscapeString` writes `&#34;` where the
// hand-rolled esc() wrote `&quot;`. Each is pinned by golden/facts/{coerce,esc,json-numbers}.json.
package js

import (
	"encoding/base64"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

// Len is `.length`: UTF-16 code units.
func Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Slice is `s.slice(start, end)` over UTF-16 units (end < 0 means "to the end").
func Slice(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	if end < 0 || end > len(u) {
		end = len(u)
	}
	if start < 0 {
		start = 0
	}
	if start > end {
		return ""
	}
	return string(utf16.Decode(u[start:end]))
}

// Truncate keeps the first n runes — the port's deliberate reading of `.slice(0, 300)` on an error
// message, which in JS can split a surrogate pair; cutting on a rune boundary is the safe superset.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ParseNumber is `Number(s)`: trims ASCII whitespace, "" → 0, accepts 0x/0o/0b prefixes, a decimal
// with an optional exponent, Infinity; anything else is NaN.
func ParseNumber(s string) float64 {
	t := strings.Trim(s, " \t\n\r\v\f")
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(t) > 2 && t[0] == '0' {
		base := 0
		switch t[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(t[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	if !decimalLiteral.MatchString(t) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		// Overflow parses as ±Inf in JS.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// StrDecimalLiteral: optional sign, digits with optional fraction (or a leading-dot fraction), optional exponent.
var decimalLiteral = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// IsInteger is Number.isInteger.
func IsInteger(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) }

// IsFinite is Number.isFinite.
func IsFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// NumberString is String(number).
func NumberString(f float64) string { return jsonx.NumberString(f) }

// ToString is String(v) for a YAML/JSON scalar: nil → "", bool → "true"/"false", numbers via
// NumberString, strings as-is. What spec.ts's asString() does to a value on its way into a stack name.
func ToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return NumberString(x)
	default:
		return ""
	}
}

// EncodeURIComponent keeps A–Z a–z 0–9 - _ . ! ~ * ' ( ) and percent-encodes everything else
// (UTF-8 bytes, uppercase hex). Not url.QueryEscape: that one turns a space into `+`, escapes
// `!'()*` and leaves `~` — and a client_secret_basic credential is built from this.
func EncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// DecodeURIComponent is decodeURIComponent: %XX sequences to bytes, which must form valid UTF-8;
// `+` is NOT a space. The error is JS's URIError.
func DecodeURIComponent(s string) (string, bool) {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			out = append(out, s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		hi, ok1 := unhex(s[i+1])
		lo, ok2 := unhex(s[i+2])
		if !ok1 || !ok2 {
			return "", false
		}
		out = append(out, hi<<4|lo)
		i += 2
	}
	if !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// KV is one query pair.
type KV struct{ K, V string }

// ParseQuery is `new URLSearchParams(raw)`: split on `&`, each pair on the FIRST `=`, `+` is a
// space, percent-escapes decoded where valid and left literal where not, `;` is an ordinary
// character, empty pairs skipped. The order is the order on the wire.
func ParseQuery(raw string) []KV {
	raw = strings.TrimPrefix(raw, "?")
	var out []KV
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, KV{K: formDecode(k), V: formDecode(v)})
	}
	return out
}

// LastWins collapses pairs the way Object.fromEntries does: the last value for a key wins, first
// appearance decides order.
func LastWins(pairs []KV) ([]string, map[string]string) {
	m := map[string]string{}
	var order []string
	for _, p := range pairs {
		if _, seen := m[p.K]; !seen {
			order = append(order, p.K)
		}
		m[p.K] = p.V
	}
	return order, m
}

// formDecode is URLSearchParams' decoder: `+` → space, valid %XX decoded (bytes must not have to
// form valid UTF-8 in the spec — invalid sequences become U+FFFD — but a bad ESCAPE stays literal).
func formDecode(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+':
			out = append(out, ' ')
		case c == '%' && i+2 < len(s):
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
			} else {
				out = append(out, c)
			}
		default:
			out = append(out, c)
		}
	}
	return strings.ToValidUTF8(string(out), "�")
}

// B64URL is Buffer.toString('base64url'): unpadded, URL alphabet.
func B64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// B64URLDecode is Buffer.from(s, 'base64url'): LENIENT — accepts padding, the standard `+/`
// alphabet, and ignores whitespace. A provider's ID token segment with `=` padding verifies under
// Bun; it must here too.
func B64URLDecode(s string) ([]byte, error) {
	t := strings.NewReplacer("+", "-", "/", "_", "=", "", "\n", "", "\r", "", " ", "").Replace(s)
	return base64.RawURLEncoding.DecodeString(t)
}

// B64 is Buffer.toString('base64'): standard alphabet, padded.
func B64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// B64Decode is Buffer.from(s, 'base64'): lenient about padding and alphabet.
func B64Decode(s string) ([]byte, error) {
	t := strings.NewReplacer("-", "+", "_", "/", "=", "", "\n", "", "\r", "", " ", "").Replace(s)
	return base64.RawStdEncoding.DecodeString(t)
}

// Esc is the hand-rolled HTML escaper from scheduler.ts: exactly these five, exactly these
// entities (`&quot;` for the double quote, where Go's html.EscapeString writes `&#34;`).
var Esc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace

// PadEnd is `s.padEnd(n)` counting UTF-16 units.
func PadEnd(s string, n int) string {
	if l := Len(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}

// Base36 is `n.toString(36)`.
func Base36(n int64) string { return strconv.FormatInt(n, 36) }
