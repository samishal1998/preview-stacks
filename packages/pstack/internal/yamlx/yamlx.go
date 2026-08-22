// Package yamlx is Bun.YAML.parse.
//
// The library underneath (goccy/go-yaml) is used as a PARSER ONLY: it hands back a syntax tree and
// this package resolves every scalar itself, by the YAML 1.2 core schema Bun implements. No
// library's own resolver is trusted, because the two dialects disagree exactly where a compose file
// is dangerous: `restart: no` is the STRING "no" in 1.2 and the boolean false in 1.1, `0755` is the
// decimal 755 in 1.2 and octal 493 in 1.1. golden/facts/yaml.json — measured on Bun — is the test.
//
// What comes out is the document universe the rest of the program runs on: nil | bool | int64 |
// float64 | string | []any | *omap.Map, mappings in insertion order, duplicate keys last-wins,
// anchors and merge keys expanded, a multi-document stream as []any.
package yamlx

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
)

// ErrNotYAML wraps every parse failure so callers can map it to their own error type.
var ErrNotYAML = errors.New("not valid YAML")

// Parse is Bun.YAML.parse(text).
func Parse(src []byte) (any, error) {
	// A document that is only a comment, or empty, is null — as Bun says.
	f, err := parser.ParseBytes(src, 0, parser.AllowDuplicateMapKey())
	if err != nil && strings.Contains(err.Error(), "tab character") {
		// Bun tolerates a tab where YAML forbids one (measured: "\t- tab" parses). Indentation is
		// the only thing a tab can mean there, so substitute and retry once.
		f, err = parser.ParseBytes([]byte(strings.ReplaceAll(string(src), "\t", " ")), 0, parser.AllowDuplicateMapKey())
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotYAML, firstLine(err.Error()))
	}
	w := &walker{anchors: map[string]any{}}
	var docs []any
	for _, d := range f.Docs {
		if d.Body == nil {
			docs = append(docs, nil)
			continue
		}
		v, err := w.node(d.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrNotYAML, err)
		}
		docs = append(docs, v)
	}
	switch len(docs) {
	case 0:
		return nil, nil
	case 1:
		return docs[0], nil
	default:
		return docs, nil
	}
}

// ParseString is Parse over a string.
func ParseString(s string) (any, error) { return Parse([]byte(s)) }

type walker struct {
	anchors map[string]any
}

func (w *walker) node(n ast.Node) (any, error) {
	switch x := n.(type) {
	case *ast.MappingNode:
		m := omap.New()
		for _, kv := range x.Values {
			if err := w.pair(m, kv); err != nil {
				return nil, err
			}
		}
		return m, nil
	case *ast.MappingValueNode:
		// A single key: value at document top level arrives as a MappingValueNode.
		m := omap.New()
		if err := w.pair(m, x); err != nil {
			return nil, err
		}
		return m, nil
	case *ast.SequenceNode:
		out := make([]any, 0, len(x.Values))
		for _, e := range x.Values {
			v, err := w.node(e)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *ast.AnchorNode:
		v, err := w.node(x.Value)
		if err != nil {
			return nil, err
		}
		if x.Name != nil {
			w.anchors[strings.TrimPrefix(x.Name.GetToken().Value, "&")] = v
		}
		return v, nil
	case *ast.AliasNode:
		name := strings.TrimPrefix(x.Value.GetToken().Value, "*")
		v, ok := w.anchors[name]
		if !ok {
			return nil, fmt.Errorf("unknown anchor *%s", name)
		}
		return omap.CloneValue(v), nil
	case *ast.TagNode:
		return w.node(x.Value)
	case *ast.LiteralNode:
		// Block scalars (| and >) are always strings; the library has already folded/chomped.
		return x.Value.GetValue().(string), nil
	case *ast.NullNode, *ast.BoolNode, *ast.IntegerNode, *ast.FloatNode, *ast.InfinityNode, *ast.NanNode, *ast.StringNode:
		return w.scalar(n), nil
	case *ast.MergeKeyNode:
		return "<<", nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported YAML node %T", n)
	}
}

func (w *walker) pair(m *omap.Map, kv *ast.MappingValueNode) error {
	// `<<: *base` — fold the target mapping(s) in, existing keys keep their position.
	if _, isMerge := kv.Key.(*ast.MergeKeyNode); isMerge {
		v, err := w.node(kv.Value)
		if err != nil {
			return err
		}
		targets := []any{v}
		if seq, ok := v.([]any); ok {
			targets = seq
		}
		// Measured on Bun (facts: "merge keys"): each merged mapping's keys land in REVERSE order,
		// mappings of a sequence in sequence order, and a key already present — from an earlier
		// explicit entry or an earlier merge — is never overwritten. A later explicit key then
		// overrides in place. `{x:1,y:2}` merged alone therefore reads back as {y:2,x:1}.
		for _, t := range targets {
			tm, ok := t.(*omap.Map)
			if !ok {
				continue
			}
			keys := tm.Keys()
			for i := len(keys) - 1; i >= 0; i-- {
				if !m.Has(keys[i]) {
					v, _ := tm.Get(keys[i])
					m.Set(keys[i], omap.CloneValue(v))
				}
			}
		}
		return nil
	}
	key := keyString(w.scalar(kv.Key))
	v, err := w.node(kv.Value)
	if err != nil {
		return err
	}
	m.Set(key, v)
	return nil
}

// keyString is what JS does to a property key: everything becomes a string.
func keyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// scalar resolves a scalar node by the 1.2 core schema from its TOKEN, not the library's verdict.
func (w *walker) scalar(n ast.Node) any {
	tok := n.GetToken()
	if tok == nil {
		return nil
	}
	switch tok.Type {
	case token.SingleQuoteType, token.DoubleQuoteType:
		// Quoted is always a string; the library has already processed escapes into Value.
		if s, ok := n.(*ast.StringNode); ok {
			return s.Value
		}
		return tok.Value
	}
	return resolvePlain(tok.Value)
}

var (
	reInt     = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reOct     = regexp.MustCompile(`^0o[0-7]+$`)
	reHex     = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reFloat   = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	reInf     = regexp.MustCompile(`^[-+]?(\.inf|\.Inf|\.INF)$`)
	reNan     = regexp.MustCompile(`^(\.nan|\.NaN|\.NAN)$`)
	reComment = regexp.MustCompile(`^#`)
)

// resolvePlain is the YAML 1.2 core schema: https://yaml.org/spec/1.2.2/#103-core-schema
func resolvePlain(s string) any {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}
	if reComment.MatchString(s) {
		// A plain scalar starting with # is a comment, so the value was never there.
		return nil
	}
	if reInt.MatchString(s) {
		// Leading zeros are DECIMAL in 1.2 ("0755" → 755). Beyond int64 the JS number loses precision
		// the way a float64 does, which is what Bun reports (9007199254740993 → 9007199254740992).
		if i, err := strconv.ParseInt(strings.TrimPrefix(s, "+"), 10, 64); err == nil {
			if i > 1<<53 || i < -(1<<53) {
				return float64(i)
			}
			return i
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	if reOct.MatchString(s) {
		i, _ := strconv.ParseInt(s[2:], 8, 64)
		return i
	}
	if reHex.MatchString(s) {
		i, _ := strconv.ParseInt(s[2:], 16, 64)
		return i
	}
	if reFloat.MatchString(s) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s
		}
		// An integral float prints as an integer in JS ("1.0" → 1, "1e3" → 1000) — the document
		// universe keeps it as int64 when it fits so JSON.stringify agrees.
		if f == math.Trunc(f) && math.Abs(f) <= 1<<53 {
			return int64(f)
		}
		return f
	}
	if reInf.MatchString(s) {
		if strings.HasPrefix(s, "-") {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	if reNan.MatchString(s) {
		return math.NaN()
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
