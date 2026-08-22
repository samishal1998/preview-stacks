package omap

import (
	"bytes"
	"encoding/json"
)

// MarshalJSON emits keys in JS property order. Values go through the standard encoder with HTML
// escaping OFF — a compose label can carry `<` or `&` and JSON.stringify leaves them alone.
func (m *Map) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.Keys() {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := encodeNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := encodeNoEscape(m.m[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	return out, nil
}

// UnmarshalJSON decodes an object preserving key order; nested objects become *Map, arrays []any,
// numbers float64 when fractional or beyond int64, else int64 — the same value universe the YAML
// parser produces, so a document that went to disk as JSON (compose.generated.yml, meta.json)
// comes back indistinguishable from one parsed from YAML.
func (m *Map) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return err
	}
	mm, ok := v.(*Map)
	if !ok {
		return &json.UnmarshalTypeError{Value: "non-object", Type: nil}
	}
	*m = *mm
	return nil
}

// Parse decodes any JSON value into the document universe.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	// Trailing data is an error, as JSON.parse would say.
	if _, err := dec.Token(); err == nil {
		return nil, &json.SyntaxError{Offset: dec.InputOffset()}
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := New()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				m.Set(k, v)
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return m, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, &json.SyntaxError{}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		f, err := t.Float64()
		return f, err
	default:
		return tok, nil
	}
}
